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
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tehman700/liveurl/internal/edge"
	"github.com/Tehman700/liveurl/internal/edge/replay"
	"github.com/Tehman700/liveurl/internal/store"
)

const replayTimeout = 15 * time.Second

// authRateIdleTTL controls how long an idle client's signup/login attempt
// counter is kept before being evicted — mirrors internal/edge's own
// rate-limiter idle window; not shared directly since that constant is
// unexported and this is the only other caller of edge.NewIPLimiter.
const authRateIdleTTL = 5 * time.Minute

type Server struct {
	Store    *store.Store
	Presence *store.Presence
	Registry *edge.Registry
	mux      *http.ServeMux

	// authLimiter throttles /api/signup and /api/login by client IP. These
	// are the only unauthenticated, password-checking endpoints on the
	// control API, so they get a much tighter budget than the
	// already-authenticated routes to make password guessing impractical.
	authLimiter *edge.IPLimiter
}

func NewServer(st *store.Store, presence *store.Presence, registry *edge.Registry) *Server {
	s := &Server{
		Store:       st,
		Presence:    presence,
		Registry:    registry,
		mux:         http.NewServeMux(),
		authLimiter: edge.NewIPLimiter(5.0/60, 5, authRateIdleTTL), // ~5 attempts/minute, burst 5
	}
	s.mux.HandleFunc("POST /api/signup", s.rateLimitAuth(s.signUp))
	s.mux.HandleFunc("POST /api/login", s.rateLimitAuth(s.logIn))
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

// rateLimitAuth guards the unauthenticated signup/login endpoints, keyed on
// client IP since there's no token yet to key on.
func (s *Server) rateLimitAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authLimiter.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "too many attempts; try again in a minute")
			return
		}
		next(w, r)
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

var emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

const minPasswordLen = 8

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func decodeCredentials(r *http.Request) (email, password string, err error) {
	var c credentials
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		return "", "", errors.New("invalid JSON body")
	}
	email = strings.ToLower(strings.TrimSpace(c.Email))
	if !emailRE.MatchString(email) {
		return "", "", errors.New("invalid email address")
	}
	if len(c.Password) < minPasswordLen {
		return "", "", fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	return email, c.Password, nil
}

// signUp is the self-serve equivalent of `liveurld seed`: creates an
// account and mints its first auth token in one call, handing back exactly
// what a new user needs to run `liveurl login <token>`. There is no email
// verification step — consistent with the rest of this deployment, which
// has no outbound email sending at all yet.
func (s *Server) signUp(w http.ResponseWriter, r *http.Request) {
	email, password, err := decodeCredentials(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, token, err := s.Store.SignUp(r.Context(), email, password)
	if errors.Is(err, store.ErrEmailTaken) {
		writeErr(w, http.StatusConflict, "an account with that email already exists")
		return
	}
	if err != nil {
		log.Printf("signup error: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not create account")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"email": user.Email, "token": token})
}

// logIn authenticates by password and mints a fresh auth token. It cannot
// return a previously-issued token — only its SHA-256 hash is ever stored,
// by design, the same way `liveurld seed`'s tokens work — so logging in a
// second time (e.g. a new browser) hands back a new, additional valid
// token rather than the original one.
func (s *Server) logIn(w http.ResponseWriter, r *http.Request) {
	email, password, err := decodeCredentials(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := s.Store.VerifyPassword(r.Context(), email, password)
	if errors.Is(err, store.ErrInvalidCredentials) {
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err != nil {
		log.Printf("login error: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not log in")
		return
	}
	token, err := s.Store.NewToken(r.Context(), user.ID)
	if err != nil {
		log.Printf("login token mint error: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not create token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": user.Email, "token": token})
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
