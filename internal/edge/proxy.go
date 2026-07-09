package edge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Tehman700/liveurl/internal/edge/snapshot"
	"github.com/Tehman700/liveurl/internal/store"
)

const storeTimeout = 5 * time.Second

// maxTeeBody bounds how much of a live GET response body we'll buffer in
// memory in order to consider it for the snapshot cache. Responses larger
// than this are still streamed to the client normally; they're just never
// cached. The stored-per-tunnel size is separately capped by
// store.MaxSnapshotBytes.
const maxTeeBody = 20 << 20

// forwardLive sends r over a fresh yamux stream to the connected agent,
// forwards the response back to w, and transparently continues as a raw
// byte pipe if the response is a protocol upgrade (WebSocket). If st is
// non-nil and the request/response pair qualifies, the response body is
// also written into the snapshot cache for offline serving.
func forwardLive(w http.ResponseWriter, r *http.Request, sess *Session, tunnelID int64, st *store.Store) error {
	stream, err := sess.OpenStream()
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	if err := r.Write(stream); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	streamR := bufio.NewReader(stream)
	resp, err := http.ReadResponse(streamR, r)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusSwitchingProtocols {
		return passthroughUpgrade(w, resp, streamR, stream)
	}

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if st != nil && tunnelID != 0 && r.Method == http.MethodGet && snapshot.Cacheable(r, resp) {
		var cb cappedBuffer
		cb.limit = maxTeeBody
		if _, err := io.Copy(io.MultiWriter(w, &cb), resp.Body); err != nil {
			return err
		}
		if !cb.over {
			storeSnapshot(tunnelID, r, resp, cb.buf.Bytes(), st)
		}
		return nil
	}

	_, err = io.Copy(w, resp.Body)
	return err
}

func storeSnapshot(tunnelID int64, r *http.Request, resp *http.Response, body []byte, st *store.Store) {
	headers := snapshot.Headers(resp)
	contentType := resp.Header.Get("Content-Type")
	queryHash := store.QueryHash(r.URL.RawQuery)
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := st.PutSnapshot(ctx, tunnelID, r.URL.Path, queryHash, contentType, headers, body); err != nil {
		log.Printf("snapshot store failed for %s: %v", r.URL.Path, err)
	}
}

// cappedBuffer accumulates writes up to limit bytes; beyond that it just
// discards further data (still reporting success so io.Copy/MultiWriter
// keeps streaming to the real client uninterrupted) and flags itself as
// over so the caller knows not to persist a truncated body.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if !c.over {
		if c.buf.Len()+len(p) > c.limit {
			c.over = true
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

// passthroughUpgrade relays a successful protocol-upgrade response to the
// client and then pipes raw bytes between the client connection and the
// tunnel stream for the remaining lifetime of the connection (WebSockets).
func passthroughUpgrade(w http.ResponseWriter, resp *http.Response, streamR *bufio.Reader, stream io.ReadWriteCloser) error {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return errors.New("response writer does not support hijacking")
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return fmt.Errorf("hijack: %w", err)
	}
	defer conn.Close()

	if err := resp.Write(bufrw); err != nil {
		return fmt.Errorf("write upgrade response: %w", err)
	}
	if err := bufrw.Flush(); err != nil {
		return fmt.Errorf("flush upgrade response: %w", err)
	}

	// Drain any client bytes the server's buffered reader already
	// consumed from the socket before we took over raw copying.
	if n := bufrw.Reader.Buffered(); n > 0 {
		buf := make([]byte, n)
		if _, err := io.ReadFull(bufrw.Reader, buf); err != nil {
			return err
		}
		if _, err := stream.Write(buf); err != nil {
			return err
		}
	}

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(stream, conn)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(conn, streamR)
		errc <- err
	}()
	<-errc
	return nil
}
