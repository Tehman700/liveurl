// Command liveurl is the agent CLI: it exposes a local port through a
// liveurl tunnel, and lets you inspect/replay buffered webhook events.
package main

import (
	"os"

	"github.com/spf13/cobra"
)

// Set via -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
// by goreleaser; "dev" when built directly with `go build`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	root := &cobra.Command{
		Use:   "liveurl",
		Short: "Expose a local port through a liveurl tunnel that survives you going offline",
		Long: `liveurl exposes a local HTTP server through a stable public URL.

Unlike a plain tunnel, the URL stays useful when your machine goes
offline: browser traffic falls back to a cached snapshot of pages you
visited while online, and webhook-shaped requests (Stripe, GitHub, ...)
are buffered and replayed — in order — the moment you reconnect.

Run "liveurl login <token>" once (get a token from whoever runs your
liveurl server, via "liveurld seed"), then "liveurl http <port>" any time
you want to expose something.`,
		Version: version,
	}
	root.SetVersionTemplate("liveurl {{.Version}} (commit " + commit + ", built " + date + ")\n")
	root.AddCommand(
		loginCmd(),
		httpCmd(),
		eventsCmd(),
		statusCmd(),
	)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
