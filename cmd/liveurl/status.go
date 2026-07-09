package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	var tunnel string
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Show a tunnel's online state, queued events, and snapshot cache size",
		Example: `  liveurl status --tunnel myapp`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAPIClient()
			if err != nil {
				return err
			}
			var result struct {
				Subdomain     string `json:"subdomain"`
				Online        bool   `json:"online"`
				QueuedEvents  int    `json:"queued_events"`
				SnapshotPages int64  `json:"snapshot_pages"`
				SnapshotBytes int64  `json:"snapshot_bytes"`
			}
			if err := client.do("GET", "/api/status", url.Values{"tunnel": {tunnel}}, &result); err != nil {
				return err
			}
			state := "OFFLINE"
			if result.Online {
				state = "ONLINE"
			}
			fmt.Printf("tunnel:          %s\n", result.Subdomain)
			fmt.Printf("state:           %s\n", state)
			fmt.Printf("queued events:   %d\n", result.QueuedEvents)
			fmt.Printf("snapshot pages:  %d\n", result.SnapshotPages)
			fmt.Printf("snapshot bytes:  %d\n", result.SnapshotBytes)
			return nil
		},
	}
	cmd.Flags().StringVar(&tunnel, "tunnel", "", "subdomain to inspect (required)")
	cmd.MarkFlagRequired("tunnel")
	return cmd
}
