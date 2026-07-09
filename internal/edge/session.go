package edge

import (
	"context"
	"io"
	"sync"

	"github.com/hashicorp/yamux"
)

// Session wraps one agent's yamux connection along with the tunnel it
// serves.
type Session struct {
	Subdomain string
	TunnelID  int64
	ymux      *yamux.Session
}

// OpenStream opens a new logical stream to the agent for one HTTP request.
// It returns io.ReadWriteCloser (rather than the concrete *yamux.Stream) so
// that other packages, such as the webhook replay engine, can depend on a
// small local interface instead of importing this package.
func (s *Session) OpenStream() (io.ReadWriteCloser, error) {
	return s.ymux.OpenStream()
}

// Registry tracks the single active session per subdomain. Only one agent
// connection per subdomain is kept; a new connection replaces the old one.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

func (r *Registry) Put(sess *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.sessions[sess.Subdomain]; ok && old != sess {
		old.ymux.Close()
	}
	r.sessions[sess.Subdomain] = sess
}

func (r *Registry) Get(subdomain string) (*Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sess, ok := r.sessions[subdomain]
	return sess, ok
}

// Remove deletes the session only if it is still the current one for its
// subdomain (guards against a stale disconnect removing a newer session).
func (r *Registry) Remove(sess *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.sessions[sess.Subdomain]; ok && cur == sess {
		delete(r.sessions, sess.Subdomain)
	}
}

// Wait blocks until the underlying yamux session closes.
func (s *Session) Wait(ctx context.Context) {
	select {
	case <-s.ymux.CloseChan():
	case <-ctx.Done():
	}
}
