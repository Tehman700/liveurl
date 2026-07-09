// Package control implements liveurld's private REST API, used by the
// liveurl CLI to list tunnels, inspect/replay buffered webhook events, and
// check tunnel status. It is not part of the public tunnel data plane.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Tehman700/liveurl/internal/edge"
	"github.com/Tehman700/liveurl/internal/edge/replay"
	"github.com/Tehman700/liveurl/internal/store"
)

const replayTimeout = 15 * time.Second

type Server struct {
	Store    *store.Store
	Presence *store.Presence
	Registry *edge.Registry
	mux      *http.ServeMux
}

func NewServer(st *store.Store, presence *store.Presence, registry *edge.Registry) *Server {
	s := &Server{Store: st, Presence: presence, Registry: registry, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/tunnels", s.withAuth(s.listTunnels))
	s.mux.HandleFunc("GET /api/events", s.withAuth(s.listEvents))
	s.mux.HandleFunc("DELETE /api/events", s.withAuth(s.clearEvents))
	s.mux.HandleFunc("POST /api/events/{id}/replay", s.withAuth(s.replayEvent))
	s.mux.HandleFunc("GET /api/status", s.withAuth(s.status))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

type ctxKey int

const userKey ctxKey = 0

func (s *Server) withAuth(next func(w http.ResponseWriter, r *http.Request, user store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		user, err := s.Store.UserByToken(r.Context(), token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next(w, r, user)
	}
}

// resolveOwnedTunnel looks up the tunnel named by the "tunnel" query param
// and verifies it belongs to user.
func (s *Server) resolveOwnedTunnel(r *http.Request, user store.User) (store.Tunnel, error) {
	sub := r.URL.Query().Get("tunnel")
	if sub == "" {
		return store.Tunnel{}, errMissingTunnelParam
	}
	t, err := s.Store.TunnelBySubdomain(r.Context(), sub)
	if err != nil {
		return store.Tunnel{}, err
	}
	if t.UserID != user.ID {
		return store.Tunnel{}, errNotOwner
	}
	return t, nil
}

var (
	errMissingTunnelParam = errors.New("missing ?tunnel= query parameter")
	errNotOwner           = errors.New("tunnel does not belong to this account")
)

func (s *Server) listTunnels(w http.ResponseWriter, r *http.Request, user store.User) {
	tunnels, err := s.Store.ListTunnels(r.Context(), user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		Subdomain string `json:"subdomain"`
		Online    bool   `json:"online"`
	}
	out := make([]item, 0, len(tunnels))
	for _, t := range tunnels {
		online, _ := s.Presence.IsOnline(r.Context(), t.Subdomain)
		out = append(out, item{Subdomain: t.Subdomain, Online: online})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, user store.User) {
	tunnel, err := s.resolveOwnedTunnel(r, user)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	state := r.URL.Query().Get("state")
	events, err := s.Store.ListEvents(r.Context(), tunnel.ID, state)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) clearEvents(w http.ResponseWriter, r *http.Request, user store.User) {
	tunnel, err := s.resolveOwnedTunnel(r, user)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	if err := s.Store.ClearEvents(r.Context(), tunnel.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request, user store.User) {
	tunnel, err := s.resolveOwnedTunnel(r, user)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	online, _ := s.Presence.IsOnline(r.Context(), tunnel.Subdomain)
	queued, err := s.Store.ListEvents(r.Context(), tunnel.ID, string(store.EventQueued))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapStats, err := s.Store.SnapshotStats(r.Context(), tunnel.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subdomain":      tunnel.Subdomain,
		"online":         online,
		"queued_events":  len(queued),
		"snapshot_pages": snapStats.Pages,
		"snapshot_bytes": snapStats.Bytes,
	})
}

func (s *Server) replayEvent(w http.ResponseWriter, r *http.Request, user store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid event id")
		return
	}
	event, err := s.Store.EventByID(r.Context(), id)
	if err != nil {
		writeErr(w, statusFor(err), "event not found")
		return
	}
	tunnel, err := s.Store.TunnelByID(r.Context(), event.TunnelID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tunnel.UserID != user.ID {
		writeErr(w, http.StatusForbidden, errNotOwner.Error())
		return
	}

	if err := s.Store.RequeueEvent(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	sess, ok := s.Registry.Get(tunnel.Subdomain)
	if !ok {
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "queued", "detail": "agent offline; will retry automatically on next connect",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), replayTimeout)
	defer cancel()
	status, err := replay.ReplayEvent(ctx, s.Store, sess, event)
	if err != nil {
		log.Printf("manual replay of event %d failed: %v", id, err)
		writeErr(w, http.StatusBadGateway, "replay failed: "+err.Error())
		return
	}
	if status >= 500 {
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "failed", "detail": fmt.Sprintf("local app returned %d; event re-queued for retry", status),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "replayed", "detail": fmt.Sprintf("local app returned %d", status),
	})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, errMissingTunnelParam):
		return http.StatusBadRequest
	case errors.Is(err, errNotOwner):
		return http.StatusForbidden
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
