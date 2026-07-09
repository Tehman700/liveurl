// Package edge implements liveurld's two public listeners: the tunnel
// listener that agents dial into, and the HTTP listener that the internet
// (or in v1, the local machine) talks to.
package edge

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"log"
	"net"
	"regexp"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/Tehman700/liveurl/internal/proto"
	"github.com/Tehman700/liveurl/internal/store"
)

var subdomainRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// TunnelServer accepts agent connections, performs the handshake, and keeps
// their session registered and their presence heartbeat refreshed. Build
// one with NewTunnelServer, not a bare struct literal, so the handshake
// rate limiter is properly initialized.
type TunnelServer struct {
	Addr       string
	TLSConfig  *tls.Config // when set, the listener terminates TLS before yamux
	PublicHost string      // host:port reported back to agents, e.g. "tideover.site" or "lvh.me:8080"
	PublicTLS  bool        // whether the public HTTP listener serves https (for the reported PublicURL scheme)
	Store      *store.Store
	Presence   *store.Presence
	Registry   *Registry
	OnConnect  func(sess *Session) // e.g. trigger webhook queue drain

	handshakeLimiter *ipLimiter
}

// TunnelServerConfig configures NewTunnelServer. HandshakeRatePerMinute
// left at zero falls back to a sane default.
type TunnelServerConfig struct {
	Addr       string
	TLSConfig  *tls.Config
	PublicHost string
	PublicTLS  bool
	Store      *store.Store
	Presence   *store.Presence
	Registry   *Registry
	OnConnect  func(sess *Session)

	// HandshakeRatePerMinute caps connection attempts per source IP,
	// checked at Accept() time before any yamux/handshake work begins.
	// Over-limit connections are closed immediately — there's no HTTP
	// semantics this deep, so unlike the HTTP-facing limiters there is no
	// 429/Retry-After to send back.
	HandshakeRatePerMinute float64
}

func NewTunnelServer(cfg TunnelServerConfig) *TunnelServer {
	perMinute := cfg.HandshakeRatePerMinute
	if perMinute <= 0 {
		perMinute = 10
	}
	return &TunnelServer{
		Addr:             cfg.Addr,
		TLSConfig:        cfg.TLSConfig,
		PublicHost:       cfg.PublicHost,
		PublicTLS:        cfg.PublicTLS,
		Store:            cfg.Store,
		Presence:         cfg.Presence,
		Registry:         cfg.Registry,
		OnConnect:        cfg.OnConnect,
		handshakeLimiter: newIPLimiter(perMinute/60, 3, rateLimiterIdleTTL),
	}
}

func (t *TunnelServer) ListenAndServe(ctx context.Context) error {
	var ln net.Listener
	var err error
	if t.TLSConfig != nil {
		ln, err = tls.Listen("tcp", t.Addr, t.TLSConfig)
	} else {
		ln, err = net.Listen("tcp", t.Addr)
	}
	if err != nil {
		return err
	}
	log.Printf("tunnel listener on %s", t.Addr)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("tunnel accept error: %v", err)
				continue
			}
		}
		if host, _, splitErr := net.SplitHostPort(conn.RemoteAddr().String()); splitErr == nil && !t.handshakeLimiter.Allow(host) {
			conn.Close()
			continue
		}
		go t.handleConn(ctx, conn)
	}
}

func (t *TunnelServer) handleConn(ctx context.Context, conn net.Conn) {
	ymux, err := yamux.Server(conn, nil)
	if err != nil {
		log.Printf("yamux server setup failed: %v", err)
		conn.Close()
		return
	}

	hsStream, err := ymux.AcceptStream()
	if err != nil {
		ymux.Close()
		return
	}

	var hs proto.Handshake
	if err := proto.ReadJSON(hsStream, &hs); err != nil {
		log.Printf("handshake decode failed: %v", err)
		ymux.Close()
		return
	}

	reply := t.authenticate(ctx, &hs)
	if err := proto.WriteJSON(hsStream, reply); err != nil {
		ymux.Close()
		return
	}
	hsStream.Close()
	if !reply.OK {
		log.Printf("handshake rejected: %s", reply.Error)
		ymux.Close()
		return
	}

	sess := &Session{Subdomain: reply.Subdomain, ymux: ymux}
	tunnel, err := t.Store.TunnelBySubdomain(ctx, reply.Subdomain)
	if err == nil {
		sess.TunnelID = tunnel.ID
	}

	t.Registry.Put(sess)
	if err := t.Presence.Heartbeat(ctx, sess.Subdomain); err != nil {
		log.Printf("presence heartbeat failed: %v", err)
	}
	log.Printf("agent connected: subdomain=%s tunnel_id=%d", sess.Subdomain, sess.TunnelID)

	if t.OnConnect != nil {
		go t.OnConnect(sess)
	}

	t.heartbeatLoop(ctx, sess)
}

func (t *TunnelServer) heartbeatLoop(ctx context.Context, sess *Session) {
	ticker := time.NewTicker(store.PresenceTTL / 3)
	defer ticker.Stop()
	closeCh := sess.ymux.CloseChan()
	for {
		select {
		case <-ticker.C:
			if err := t.Presence.Heartbeat(ctx, sess.Subdomain); err != nil {
				log.Printf("presence heartbeat failed: %v", err)
			}
		case <-closeCh:
			t.Registry.Remove(sess)
			_ = t.Presence.Clear(context.Background(), sess.Subdomain)
			log.Printf("agent disconnected: subdomain=%s", sess.Subdomain)
			return
		case <-ctx.Done():
			return
		}
	}
}

func (t *TunnelServer) authenticate(ctx context.Context, hs *proto.Handshake) proto.HandshakeReply {
	if hs.Version != proto.Version {
		return proto.HandshakeReply{Error: "unsupported protocol version"}
	}
	user, err := t.Store.UserByToken(ctx, hs.Token)
	if err != nil {
		return proto.HandshakeReply{Error: "invalid auth token"}
	}

	subdomain := hs.Subdomain
	if subdomain == "" {
		subdomain = randomSubdomain()
	}
	if !subdomainRE.MatchString(subdomain) {
		return proto.HandshakeReply{Error: "invalid subdomain: use 2-63 lowercase letters, digits, hyphens"}
	}

	tunnel, err := t.Store.ClaimTunnel(ctx, user.ID, subdomain, hs.BufferRules)
	if err != nil {
		if err == store.ErrSubdomainTaken {
			return proto.HandshakeReply{Error: "subdomain already in use by another account"}
		}
		return proto.HandshakeReply{Error: "internal error claiming subdomain"}
	}

	scheme := "http"
	if t.PublicTLS {
		scheme = "https"
	}
	return proto.HandshakeReply{
		OK:        true,
		Subdomain: tunnel.Subdomain,
		PublicURL: scheme + "://" + tunnel.Subdomain + "." + t.PublicHost,
	}
}

func randomSubdomain() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "t-" + hex.EncodeToString(b)
}
