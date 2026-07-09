// Package replay drains a tunnel's buffered webhook queue over a freshly
// (re)connected agent session, replaying each event byte-for-byte in the
// order it was originally received.
package replay

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/Tehman700/liveurl/internal/proto"
	"github.com/Tehman700/liveurl/internal/store"
)

// StreamOpener is satisfied by *edge.Session. Defined locally to avoid an
// import cycle between the edge and replay packages.
type StreamOpener interface {
	OpenStream() (io.ReadWriteCloser, error)
}

// Drain replays every queued webhook event for tunnelID, oldest first,
// through sess. It is called once per new agent connection.
func Drain(ctx context.Context, st *store.Store, sess StreamOpener, tunnelID int64) {
	if tunnelID == 0 {
		return
	}
	events, err := st.QueuedEvents(ctx, tunnelID)
	if err != nil {
		log.Printf("replay: list queued events failed: %v", err)
		return
	}
	if len(events) == 0 {
		return
	}
	log.Printf("replay: draining %d queued event(s) for tunnel %d", len(events), tunnelID)

	for _, e := range events {
		status, err := ReplayEvent(ctx, st, sess, e)
		if err != nil {
			log.Printf("replay: event %d failed, stopping drain: %v", e.ID, err)
			return
		}
		if status >= 500 {
			log.Printf("replay: event %d got %d, stopping drain — will retry on next connect", e.ID, status)
			return
		}
	}
}

// ReplayEvent replays a single webhook event through sess and updates its
// stored state accordingly. It returns the local app's HTTP status code
// (0 if a transport-level error occurred before any response was read) and
// an error only for transport-level failures (open/write/read), never for
// an ordinary non-2xx response from the local app — callers must inspect
// the returned status to learn the outcome. Exported so the control API
// can trigger a one-off manual replay in addition to Drain's automatic
// queue draining.
func ReplayEvent(ctx context.Context, st *store.Store, sess StreamOpener, e store.WebhookEvent) (int, error) {
	if err := st.MarkReplaying(ctx, e.ID); err != nil {
		return 0, fmt.Errorf("mark replaying: %w", err)
	}

	stream, err := sess.OpenStream()
	if err != nil {
		// Session is gone already (agent dropped mid-drain); requeue and
		// let the next reconnect pick it up.
		_ = st.RequeueEvent(ctx, e.ID)
		return 0, err
	}
	defer stream.Close()

	req := buildRequest(e)
	if err := req.Write(stream); err != nil {
		_ = st.RequeueEvent(ctx, e.ID)
		return 0, fmt.Errorf("write replayed request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		_ = st.RequeueEvent(ctx, e.ID)
		return 0, fmt.Errorf("read replay response: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 500 {
		if err := st.MarkFailed(ctx, e.ID, resp.StatusCode); err != nil {
			return resp.StatusCode, err
		}
		return resp.StatusCode, nil
	}

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		log.Printf("replay: event %d got %d — if this is a signature failure, your app's webhook "+
			"tolerance window likely rejected the buffered timestamp; see %s", e.ID, resp.StatusCode, proto.HeaderOriginalTimestamp)
	}
	if err := st.MarkDelivered(ctx, e.ID, resp.StatusCode); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func buildRequest(e store.WebhookEvent) *http.Request {
	body := e.Body
	req := &http.Request{
		Method:        e.Method,
		URL:           &url.URL{Path: e.Path, RawQuery: e.Query},
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        cloneHeader(e.Headers),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	if host := req.Header.Get("Host"); host != "" {
		req.Host = host
	}
	req.Header.Set(proto.HeaderBuffered, "true")
	req.Header.Set(proto.HeaderOriginalTimestamp, e.ReceivedAt.UTC().Format(time.RFC3339))
	return req
}

func cloneHeader(h map[string][]string) http.Header {
	out := make(http.Header, len(h))
	for k, vals := range h {
		cp := make([]string, len(vals))
		copy(cp, vals)
		out[k] = cp
	}
	return out
}
