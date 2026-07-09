package edge

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Tehman700/liveurl/internal/edge/classify"
	"github.com/Tehman700/liveurl/internal/edge/snapshot"
	"github.com/Tehman700/liveurl/internal/proto"
	"github.com/Tehman700/liveurl/internal/store"
)

// OfflineDispatcher is the default OfflineHandler: it classifies requests
// received while the agent is disconnected, buffering webhook-shaped
// traffic for later ordered replay and serving cached snapshots (with an
// offline banner) for everything else.
type OfflineDispatcher struct {
	Store *store.Store
}

func (d *OfflineDispatcher) ServeOffline(w http.ResponseWriter, r *http.Request, tunnel store.Tunnel) {
	if classify.IsWebhook(tunnel.BufferRules, r) {
		d.bufferWebhook(w, r, tunnel)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		d.serveSnapshot(w, r, tunnel)
		return
	}
	http.Error(w, brandedPage("503 Offline", "The live server for this tunnel is offline and this request isn't a page load, so there's nothing to show."), http.StatusServiceUnavailable)
}

func (d *OfflineDispatcher) bufferWebhook(w http.ResponseWriter, r *http.Request, tunnel store.Tunnel) {
	body, err := io.ReadAll(io.LimitReader(r.Body, proto.MaxWebhookBody+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(body) > proto.MaxWebhookBody {
		http.Error(w, "request body exceeds buffering limit", http.StatusRequestEntityTooLarge)
		return
	}

	headers := map[string][]string(r.Header)
	id, err := d.Store.EnqueueEvent(r.Context(), tunnel.ID, r.Method, r.URL.Path, r.URL.RawQuery, headers, body)
	if err != nil {
		log.Printf("enqueue webhook failed: %v", err)
		http.Error(w, "failed to buffer webhook", http.StatusInternalServerError)
		return
	}

	w.Header().Set(proto.HeaderBuffered, "true")
	w.Header().Set(proto.HeaderEventID, strconv.FormatInt(id, 10))
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("liveurl: agent offline, request buffered for replay\n"))
}

func (d *OfflineDispatcher) serveSnapshot(w http.ResponseWriter, r *http.Request, tunnel store.Tunnel) {
	queryHash := store.QueryHash(r.URL.RawQuery)
	snap, err := d.Store.GetSnapshot(r.Context(), tunnel.ID, r.URL.Path, queryHash)
	if err != nil {
		http.Error(w, brandedPage("Offline — no snapshot",
			"The live server is offline and this page was never captured in the snapshot cache."), http.StatusServiceUnavailable)
		return
	}

	for k, v := range snap.Headers {
		w.Header().Set(k, v)
	}
	body := snap.Body
	if isHTML(snap.ContentType) {
		body = snapshot.InjectBanner(body, snap.CapturedAt)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func isHTML(contentType string) bool {
	return strings.HasPrefix(contentType, "text/html")
}
