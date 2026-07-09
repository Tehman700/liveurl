// Package agent implements the liveurl CLI's tunnel client: it dials the
// server, performs the handshake, and forwards incoming streams to a local
// port.
package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math/rand"
	"net"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/Tehman700/liveurl/internal/proto"
)

type Config struct {
	ServerAddr  string
	TLS         bool // dial ServerAddr with TLS (real deployment) instead of plaintext (local dev)
	Token       string
	Subdomain   string
	BufferRules []string
	LocalAddr   string // e.g. "127.0.0.1:3000"
}

const (
	minBackoff = 500 * time.Millisecond
	maxBackoff = 30 * time.Second
)

// Run connects and serves forever, reconnecting with backoff on failure,
// until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		publicURL, err := connectAndServe(ctx, cfg)
		if err != nil {
			log.Printf("tunnel disconnected: %v", err)
		}
		if publicURL != "" {
			// We had a healthy session for a while; reset backoff.
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(backoff)):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

// connectAndServe performs one connection lifecycle. It returns the public
// URL if the handshake succeeded (even if the session later dropped), so
// the caller can decide whether to reset backoff.
func connectAndServe(ctx context.Context, cfg Config) (string, error) {
	var conn net.Conn
	var err error
	if cfg.TLS {
		host, _, splitErr := net.SplitHostPort(cfg.ServerAddr)
		if splitErr != nil {
			return "", fmt.Errorf("invalid server address %q: %w", cfg.ServerAddr, splitErr)
		}
		conn, err = tls.Dial("tcp", cfg.ServerAddr, &tls.Config{ServerName: host})
	} else {
		conn, err = net.Dial("tcp", cfg.ServerAddr)
	}
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", cfg.ServerAddr, err)
	}

	ymux, err := yamux.Client(conn, nil)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("yamux client: %w", err)
	}
	defer ymux.Close()

	hsStream, err := ymux.OpenStream()
	if err != nil {
		return "", fmt.Errorf("open handshake stream: %w", err)
	}

	hs := proto.Handshake{
		Version:     proto.Version,
		Token:       cfg.Token,
		Subdomain:   cfg.Subdomain,
		BufferRules: cfg.BufferRules,
	}
	if err := proto.WriteJSON(hsStream, hs); err != nil {
		return "", fmt.Errorf("send handshake: %w", err)
	}

	var reply proto.HandshakeReply
	if err := proto.ReadJSON(hsStream, &reply); err != nil {
		return "", fmt.Errorf("read handshake reply: %w", err)
	}
	hsStream.Close()

	if !reply.OK {
		return "", fmt.Errorf("server rejected handshake: %s", reply.Error)
	}

	log.Printf("connected — forwarding %s to %s", reply.PublicURL, cfg.LocalAddr)

	acceptLoop(ctx, ymux, cfg.LocalAddr)
	return reply.PublicURL, nil
}

// acceptLoop accepts server-opened streams (one per forwarded HTTP request)
// until the session closes or ctx is cancelled.
func acceptLoop(ctx context.Context, ymux *yamux.Session, localAddr string) {
	go func() {
		<-ctx.Done()
		ymux.Close()
	}()
	for {
		stream, err := ymux.AcceptStream()
		if err != nil {
			return
		}
		go handleStream(stream, localAddr)
	}
}
