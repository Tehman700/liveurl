package replay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Tehman700/liveurl/internal/config"
	"github.com/Tehman700/liveurl/internal/store"
)

// fakeStream is a one-shot io.ReadWriteCloser: writes go to an internal
// buffer (the replayed request), reads come from a pre-canned HTTP
// response. It reports its captured request line back to the owning
// fakeSession on Close, which — since Drain processes events strictly
// sequentially and closes each stream before opening the next — preserves
// call order.
type fakeStream struct {
	sess    *fakeSession
	written bytes.Buffer
	respR   *bytes.Reader
}

func (f *fakeStream) Write(p []byte) (int, error) { return f.written.Write(p) }
func (f *fakeStream) Read(p []byte) (int, error)  { return f.respR.Read(p) }
func (f *fakeStream) Close() error {
	f.sess.mu.Lock()
	defer f.sess.mu.Unlock()
	f.sess.requestLines = append(f.sess.requestLines, requestLine(f.written.Bytes()))
	return nil
}

func requestLine(b []byte) string {
	i := bytes.IndexByte(b, '\n')
	if i == -1 {
		return string(b)
	}
	return string(bytes.TrimRight(b[:i], "\r\n"))
}

// fakeSession hands out canned status codes in order, one per OpenStream
// call, and records the request line seen on each stream in call order.
type fakeSession struct {
	mu           sync.Mutex
	statuses     []int
	idx          int
	requestLines []string
}

func (f *fakeSession) OpenStream() (io.ReadWriteCloser, error) {
	f.mu.Lock()
	status := f.statuses[f.idx]
	f.idx++
	f.mu.Unlock()
	resp := fmt.Sprintf("HTTP/1.1 %d OK\r\nContent-Length: 0\r\n\r\n", status)
	return &fakeStream{sess: f, respR: bytes.NewReader([]byte(resp))}, nil
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg := config.LoadServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		t.Skipf("postgres not reachable, skipping integration test: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func TestDrainReplaysInOrderAndHandlesFailures(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()

	ctx := context.Background()
	user, err := st.CreateUser(ctx, fmt.Sprintf("replay-test-%d@localhost", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tunnel, err := st.ClaimTunnel(ctx, user.ID, fmt.Sprintf("replay-test-%d", time.Now().UnixNano()), nil)
	if err != nil {
		t.Fatalf("claim tunnel: %v", err)
	}

	paths := []string{"/webhooks/first", "/webhooks/second", "/webhooks/third"}
	for _, p := range paths {
		if _, err := st.EnqueueEvent(ctx, tunnel.ID, "POST", p, "", map[string][]string{
			"Content-Type": {"application/json"},
		}, []byte(`{}`)); err != nil {
			t.Fatalf("enqueue event: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // ensure distinct received_at ordering
	}

	sess := &fakeSession{statuses: []int{200, 500, 200}}
	Drain(ctx, st, sess, tunnel.ID)

	// The 500 for "second" should have stopped the drain (Drain returns on
	// first failure so later events keep their original queue position),
	// so only the first request should have gone out.
	if len(sess.requestLines) != 2 {
		t.Fatalf("expected drain to stop after the failing event, got %d requests: %v", len(sess.requestLines), sess.requestLines)
	}
	if got := sess.requestLines[0]; got != "POST /webhooks/first HTTP/1.1" {
		t.Fatalf("unexpected first request line: %q", got)
	}
	if got := sess.requestLines[1]; got != "POST /webhooks/second HTTP/1.1" {
		t.Fatalf("unexpected second request line: %q", got)
	}

	events, err := st.ListEvents(ctx, tunnel.ID, "")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	byPath := map[string]store.WebhookEvent{}
	for _, e := range events {
		byPath[e.Path] = e
	}
	if byPath["/webhooks/first"].State != store.EventDelivered {
		t.Errorf("expected /webhooks/first to be delivered, got %s", byPath["/webhooks/first"].State)
	}
	if byPath["/webhooks/second"].State != store.EventQueued {
		t.Errorf("expected /webhooks/second to be re-queued after 500, got %s", byPath["/webhooks/second"].State)
	}
	if byPath["/webhooks/third"].State != store.EventQueued {
		t.Errorf("expected /webhooks/third to remain untouched in queue, got %s", byPath["/webhooks/third"].State)
	}

	// Reconnect: "second" should now succeed and "third" should follow.
	sess2 := &fakeSession{statuses: []int{200, 200}}
	Drain(ctx, st, sess2, tunnel.ID)
	if len(sess2.requestLines) != 2 {
		t.Fatalf("expected second drain to replay remaining 2 events, got %d: %v", len(sess2.requestLines), sess2.requestLines)
	}
	if sess2.requestLines[0] != "POST /webhooks/second HTTP/1.1" || sess2.requestLines[1] != "POST /webhooks/third HTTP/1.1" {
		t.Fatalf("unexpected replay order on reconnect: %v", sess2.requestLines)
	}

	events, err = st.ListEvents(ctx, tunnel.ID, "")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range events {
		if e.State != store.EventDelivered {
			t.Errorf("expected all events delivered after second drain, %s is %s", e.Path, e.State)
		}
	}
}
