package edge

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter hands out a token-bucket limiter per key (an IP address, or an
// "IP|subdomain" pair — the caller decides what a key means). Idle entries
// are evicted lazily on Allow calls rather than via a background goroutine,
// so the type needs no explicit shutdown.
type ipLimiter struct {
	mu        sync.Mutex
	rps       rate.Limit
	burst     int
	idleTTL   time.Duration
	entries   map[string]*limiterEntry
	lastSweep time.Time
}

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newIPLimiter creates a limiter allowing rps requests/sec (with the given
// burst) per key. idleTTL controls how long an unused key's state is kept
// before being evicted to bound memory.
func newIPLimiter(rps float64, burst int, idleTTL time.Duration) *ipLimiter {
	return &ipLimiter{
		rps:     rate.Limit(rps),
		burst:   burst,
		idleTTL: idleTTL,
		entries: make(map[string]*limiterEntry),
	}
}

// Allow reports whether a request for key is within its rate limit right
// now, consuming a token if so. A nil *ipLimiter always allows — this lets
// Router/TunnelServer values built without going through their constructor
// (e.g. in tests) degrade to "no rate limiting" instead of panicking.
func (l *ipLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.sweepLocked(now)

	e, ok := l.entries[key]
	if !ok {
		e = &limiterEntry{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.entries[key] = e
	}
	e.lastSeen = now
	return e.limiter.Allow()
}

// sweepLocked evicts entries idle for longer than idleTTL. Called with mu
// already held; throttled to run at most once per idleTTL so Allow stays
// cheap on the common path.
func (l *ipLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.idleTTL {
		return
	}
	l.lastSweep = now
	for k, e := range l.entries {
		if now.Sub(e.lastSeen) > l.idleTTL {
			delete(l.entries, k)
		}
	}
}
