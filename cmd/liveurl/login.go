package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tehman700/liveurl/internal/cliconfig"
	"github.com/Tehman700/liveurl/internal/config"
)

func loginCmd() *cobra.Command {
	var serverAddr, controlURL string
	var useTLS bool
	cmd := &cobra.Command{
		Use:   "login <token>",
		Short: "Save an auth token issued by `liveurld seed`",
		Long: `Save an auth token issued by "liveurld seed", so later commands
(http, events, status) don't need it passed again. Credentials are stored
in ~/.liveurl/config.json.`,
		Example: `  # local dev server (defaults: plaintext, 127.0.0.1:4443)
  liveurl login lu_xxxxxxxxxxxx

  # a real deployment, over TLS
  liveurl login lu_xxxxxxxxxxxx --server example.com:4443 --tls`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			defaults := config.LoadAgentDefaults()
			if serverAddr == "" {
				serverAddr = defaults.ServerAddr
			}
			if controlURL == "" {
				controlURL = defaults.ControlURL
			}
			c := cliconfig.Config{
				ServerAddr: serverAddr,
				ControlURL: controlURL,
				Token:      args[0],
				TLS:        useTLS,
			}
			if err := cliconfig.Save(c); err != nil {
				return err
			}
			fmt.Println("saved credentials to ~/.liveurl/config.json")
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "tunnel server address (default from LIVEURL_SERVER_ADDR or 127.0.0.1:4443)")
	cmd.Flags().StringVar(&controlURL, "control-url", "", "control API base URL (default from LIVEURL_CONTROL_URL or http://127.0.0.1:8081)")
	cmd.Flags().BoolVar(&useTLS, "tls", false, "connect to the tunnel server over TLS (use for a real deployment, not local dev)")
	return cmd
}
