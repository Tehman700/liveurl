// Command liveurld runs the liveurl edge server: the public tunnel
// listener, the public HTTP listener, and the private control API.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tehman700/liveurl/internal/config"
	"github.com/Tehman700/liveurl/internal/control"
	"github.com/Tehman700/liveurl/internal/dashboard"
	"github.com/Tehman700/liveurl/internal/edge"
	"github.com/Tehman700/liveurl/internal/edge/replay"
	"github.com/Tehman700/liveurl/internal/store"
)

const shutdownTimeout = 5 * time.Second

// Set via -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
// by goreleaser; "dev" when built directly with `go build`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	root := &cobra.Command{
		Use:   "liveurld",
		Short: "liveurld is the liveurl edge server: tunnel listener, public HTTP listener, and control API",
		Long: `liveurld is the liveurl edge server.

It runs three listeners in one process:
  - the tunnel listener, which agents (the "liveurl" CLI) dial into
  - the public HTTP(S) listener, which serves live-proxied tunnel traffic,
    the embedded web dashboard, and the offline snapshot/webhook-buffer
    fallback when a tunnel's agent is disconnected
  - the control API, used by "liveurl events"/"status" and the dashboard

Configuration is entirely via environment variables (see the README) —
there are no serve-time flags. Requires Postgres and Redis; see
deploy/docker-compose.yml for a local Postgres+Redis stack.`,
		Version: version,
	}
	root.SetVersionTemplate("liveurld {{.Version}} (commit " + commit + ", built " + date + ")\n")
	root.AddCommand(serveCmd(), seedCmd(), resetPasswordCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the tunnel listener, public HTTP listener, and control API",
		Long: `Run the tunnel listener, public HTTP listener, and control API.

Reads its configuration from environment variables (LIVEURL_POSTGRES_DSN,
LIVEURL_REDIS_ADDR, LIVEURL_TUNNEL_ADDR, LIVEURL_PUBLIC_ADDR,
LIVEURL_CONTROL_ADDR, LIVEURL_PUBLIC_HOST, LIVEURL_TLS_CERT_FILE,
LIVEURL_TLS_KEY_FILE, and the LIVEURL_RATE_* rate-limit knobs — see the
README for the full list and their defaults) and runs until interrupted
(Ctrl+C / SIGTERM), shutting down its HTTP listeners gracefully.`,
		Example: `  # local dev, against docker-compose's Postgres/Redis, plaintext
  liveurld serve

  # real deployment, with TLS and a real public host
  LIVEURL_PUBLIC_HOST=example.com LIVEURL_PUBLIC_ADDR=:443 \
  LIVEURL_TLS_CERT_FILE=/etc/liveurl/tls/fullchain.pem \
  LIVEURL_TLS_KEY_FILE=/etc/liveurl/tls/privkey.pem \
  liveurld serve`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context())
		},
	}
}

func runServe(parent context.Context) error {
	cfg := config.LoadServer()

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	presence := store.OpenPresence(cfg.RedisAddr)
	defer presence.Close()
	if err := presence.Ping(ctx); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}

	registry := edge.NewRegistry()

	var tlsConfig *tls.Config
	useTLS := cfg.TLSCertFile != "" && cfg.TLSKeyFile != ""
	if useTLS {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load TLS cert/key: %w", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	tunnelServer := edge.NewTunnelServer(edge.TunnelServerConfig{
		Addr:       cfg.TunnelAddr,
		TLSConfig:  tlsConfig,
		PublicHost: cfg.PublicHost,
		PublicTLS:  useTLS,
		Store:      st,
		Presence:   presence,
		Registry:   registry,
		OnConnect: func(sess *edge.Session) {
			replay.Drain(context.Background(), st, sess, sess.TunnelID)
		},
		HandshakeRatePerMinute: cfg.HandshakeRatePerMinute,
	})

	// Shared between the loopback-only control API (unchanged, still the
	// primary defense-in-depth boundary) and the public dashboard mount
	// below — additive, not a replacement.
	controlHandler := control.NewServer(st, presence, registry)

	router := edge.NewRouter(edge.RouterConfig{
		BaseHost:           cfg.PublicHost,
		Store:              st,
		Presence:           presence,
		Registry:           registry,
		Offline:            &edge.OfflineDispatcher{Store: st},
		Dashboard:          dashboard.Handler(),
		DashboardAPI:       controlHandler,
		TunnelRateRPS:      cfg.TunnelRateRPS,
		TunnelRateBurst:    cfg.TunnelRateBurst,
		DashboardRateRPS:   cfg.DashboardRateRPS,
		DashboardRateBurst: cfg.DashboardRateBurst,
	})
	publicSrv := &http.Server{Addr: cfg.PublicAddr, Handler: router}

	controlSrv := &http.Server{Addr: cfg.ControlAddr, Handler: controlHandler}

	errc := make(chan error, 3)
	go func() { errc <- tunnelServer.ListenAndServe(ctx) }()
	go func() {
		log.Printf("public HTTP listener on %s (base host %s, tls=%v)", cfg.PublicAddr, cfg.PublicHost, useTLS)
		var err error
		if useTLS {
			err = publicSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = publicSrv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errc <- err
		}
	}()
	go func() {
		log.Printf("control API listener on %s", cfg.ControlAddr)
		if err := controlSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Printf("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = publicSrv.Shutdown(shutdownCtx)
		_ = controlSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errc:
		return err
	}
}

func seedCmd() *cobra.Command {
	var email string
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Create (or reuse) a user and print a fresh auth token",
		Long: `Create (or reuse, keyed by email) a user and print a fresh auth token.

The printed token is what an end user passes to "liveurl login" on the
machine hosting their app. Most accounts these days come from self-serve
signup (POST /api/signup on the dashboard) instead — this command remains
for operator-provisioned accounts and local dev.`,
		Example: `  liveurld seed
  liveurld seed --email you@example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.LoadServer()
			ctx := cmd.Context()
			st, err := store.Open(ctx, cfg.PostgresDSN)
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.Migrate(ctx); err != nil {
				return err
			}
			user, err := st.CreateUser(ctx, email)
			if err != nil {
				return err
			}
			token, err := st.NewToken(ctx, user.ID)
			if err != nil {
				return err
			}
			fmt.Println("user:", user.Email)
			fmt.Println("token:", token)
			fmt.Println()
			fmt.Println("Run this on the machine hosting your app:")
			fmt.Printf("  liveurl login %s\n", token)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "dev@localhost", "email to identify the dev user")
	return cmd
}

func resetPasswordCmd() *cobra.Command {
	var email string
	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Recover a locked-out account: set a new password and mint a fresh token",
		Long: `Operator-only account recovery.

Self-serve signup (POST /api/signup) has no email verification and no
self-service "forgot password" flow yet, so a user who forgets their
password has no way to recover their account on their own — and can't just
sign up again with the same email, since /api/signup deliberately refuses
to reuse an existing one. This command is the escape hatch: it sets a new
random password on the account and mints a fresh auth token, exactly like
a successful /api/login would.

Run this on the box, having verified out-of-band that the requester really
owns that email (this command does not verify identity itself), then relay
the printed password and token back to them.`,
		Example: `  liveurld reset-password --email you@example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.LoadServer()
			ctx := cmd.Context()
			st, err := store.Open(ctx, cfg.PostgresDSN)
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.Migrate(ctx); err != nil {
				return err
			}
			password, err := randomPassword()
			if err != nil {
				return err
			}
			user, err := st.ResetPassword(ctx, email, password)
			if err != nil {
				return err
			}
			token, err := st.NewToken(ctx, user.ID)
			if err != nil {
				return err
			}
			fmt.Println("user:", user.Email)
			fmt.Println("new password:", password)
			fmt.Println("new token:", token)
			fmt.Println()
			fmt.Println("Relay both back to the user. They can log in at /dashboard with the new")
			fmt.Println("password, or run this right now on the machine hosting their app:")
			fmt.Printf("  liveurl login %s\n", token)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "email of the account to recover (required)")
	cmd.MarkFlagRequired("email")
	return cmd
}

// randomPassword generates a recovery password strong enough that its
// randomness carries the security, not any complexity rule.
func randomPassword() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
