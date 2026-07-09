package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// PresenceTTL is how long a subdomain is considered ONLINE after its last
// heartbeat. Expiry is what flips a tunnel into offline mode.
const PresenceTTL = 15 * time.Second

type Presence struct {
	rdb *redis.Client
}

func OpenPresence(addr string) *Presence {
	return &Presence{rdb: redis.NewClient(&redis.Options{Addr: addr})}
}

func (p *Presence) Ping(ctx context.Context) error {
	return p.rdb.Ping(ctx).Err()
}

func (p *Presence) Close() error { return p.rdb.Close() }

func presenceKey(subdomain string) string { return "presence:" + subdomain }

// Heartbeat marks a subdomain online for PresenceTTL. Callers should call
// this on connect and then repeatedly on a shorter interval than the TTL.
func (p *Presence) Heartbeat(ctx context.Context, subdomain string) error {
	return p.rdb.Set(ctx, presenceKey(subdomain), "1", PresenceTTL).Err()
}

// IsOnline reports whether a subdomain has a live, unexpired heartbeat.
func (p *Presence) IsOnline(ctx context.Context, subdomain string) (bool, error) {
	n, err := p.rdb.Exists(ctx, presenceKey(subdomain)).Result()
	return n > 0, err
}

// Clear removes presence immediately (used on graceful agent disconnect).
func (p *Presence) Clear(ctx context.Context, subdomain string) error {
	return p.rdb.Del(ctx, presenceKey(subdomain)).Err()
}
