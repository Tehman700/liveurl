package main

import (
	"fmt"
	"net"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Tehman700/liveurl/internal/agent"
	"github.com/Tehman700/liveurl/internal/cliconfig"
)

func httpCmd() *cobra.Command {
	var subdomain string
	var bufferRules []string
	cmd := &cobra.Command{
		Use:   "http <port>",
		Short: "Expose a local HTTP port through your tunnel",
		Long: `Expose a local HTTP port through your tunnel and keep it running until
interrupted (Ctrl+C). Reconnects automatically with backoff on any
disconnect, and resumes the same subdomain.

Paths matching --buffer are always treated as webhooks and buffered
while you're offline, regardless of how they'd otherwise be classified;
see the README's "How requests are classified while offline" section for
the full precedence order (explicit rules, then provider signature
headers, then a content-type/Accept heuristic).`,
		Example: `  liveurl http 3000
  liveurl http 3000 --subdomain myapp
  liveurl http 3000 --subdomain myapp --buffer "/webhooks/*" --buffer "/api/stripe/*"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid port %q: %w", args[0], err)
			}
			creds, err := cliconfig.Load()
			if err != nil {
				return err
			}
			if creds.Token == "" {
				return fmt.Errorf("not logged in — run `liveurl login <token>` first (see `liveurld seed`)")
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			cfg := agent.Config{
				ServerAddr:  creds.ServerAddr,
				TLS:         creds.TLS,
				Token:       creds.Token,
				Subdomain:   subdomain,
				BufferRules: bufferRules,
				LocalAddr:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			}
			return agent.Run(ctx, cfg)
		},
	}
	cmd.Flags().StringVar(&subdomain, "subdomain", "", "requested stable subdomain (random if omitted)")
	cmd.Flags().StringSliceVar(&bufferRules, "buffer", nil, "path globs (e.g. /webhooks/*) always buffered while offline")
	return cmd
}
