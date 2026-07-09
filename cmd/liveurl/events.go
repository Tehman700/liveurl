package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

type eventJSON struct {
	ID         int64  `json:"ID"`
	TunnelID   int64  `json:"TunnelID"`
	Method     string `json:"Method"`
	Path       string `json:"Path"`
	Query      string `json:"Query"`
	ReceivedAt string `json:"ReceivedAt"`
	State      string `json:"State"`
	Attempts   int    `json:"Attempts"`
	LastStatus int    `json:"LastStatus"`
}

func eventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Inspect and replay buffered webhook events",
		Long: `Inspect and replay webhook events that were buffered while your tunnel's
agent was offline. Events move through the states queued -> replaying ->
delivered (or dead, after repeated failures) as they're automatically
drained on reconnect.`,
	}
	cmd.AddCommand(eventsListCmd(), eventsReplayCmd(), eventsClearCmd())
	return cmd
}

func eventsListCmd() *cobra.Command {
	var tunnel, state string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List buffered webhook events for a tunnel",
		Example: `  liveurl events list --tunnel myapp
  liveurl events list --tunnel myapp --state dead`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAPIClient()
			if err != nil {
				return err
			}
			q := url.Values{"tunnel": {tunnel}}
			if state != "" {
				q.Set("state", state)
			}
			var events []eventJSON
			if err := client.do("GET", "/api/events", q, &events); err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Println("no events")
				return nil
			}
			fmt.Printf("%-6s %-6s %-30s %-10s %-8s %s\n", "ID", "METHOD", "PATH", "STATE", "ATTEMPTS", "RECEIVED")
			for _, e := range events {
				path := e.Path
				if e.Query != "" {
					path += "?" + e.Query
				}
				fmt.Printf("%-6d %-6s %-30s %-10s %-8d %s\n", e.ID, e.Method, path, e.State, e.Attempts, e.ReceivedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tunnel, "tunnel", "", "subdomain to inspect (required)")
	cmd.Flags().StringVar(&state, "state", "", "filter by state: queued|replaying|delivered|dead")
	cmd.MarkFlagRequired("tunnel")
	return cmd
}

func eventsReplayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replay <event-id>",
		Short: "Replay a single buffered event now",
		Long: `Force an immediate replay of one buffered event, regardless of its
current state (including "dead" events that exhausted their automatic
retries) — requires the tunnel's agent to currently be online. If it
isn't, the event is simply re-queued for the next automatic drain.`,
		Example: `  liveurl events replay 42`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAPIClient()
			if err != nil {
				return err
			}
			var result map[string]string
			if err := client.do("POST", "/api/events/"+args[0]+"/replay", nil, &result); err != nil {
				return err
			}
			fmt.Println(result["status"], strings.TrimSpace(result["detail"]))
			return nil
		},
	}
	return cmd
}

func eventsClearCmd() *cobra.Command {
	var tunnel string
	cmd := &cobra.Command{
		Use:     "clear",
		Short:   "Delete all buffered events for a tunnel",
		Example: `  liveurl events clear --tunnel myapp`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAPIClient()
			if err != nil {
				return err
			}
			if err := client.do("DELETE", "/api/events", url.Values{"tunnel": {tunnel}}, nil); err != nil {
				return err
			}
			fmt.Println("cleared")
			return nil
		},
	}
	cmd.Flags().StringVar(&tunnel, "tunnel", "", "subdomain to clear (required)")
	cmd.MarkFlagRequired("tunnel")
	return cmd
}
