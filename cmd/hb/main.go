package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/hookbridgehq/hookbridge-cli/internal/api"
	"github.com/hookbridgehq/hookbridge-cli/internal/config"
	"github.com/hookbridgehq/hookbridge-cli/internal/forwarder"
	"github.com/hookbridgehq/hookbridge-cli/internal/listener"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags
var Version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "hb",
		Short:        "HookBridge CLI — receive webhooks locally",
		Long:         "HookBridge CLI lets you receive webhooks on your local machine during development.",
		SilenceUsage: true,
	}

	cmd.AddCommand(versionCmd())
	cmd.AddCommand(loginCmd())
	cmd.AddCommand(logoutCmd())
	cmd.AddCommand(endpointsCmd())
	cmd.AddCommand(listenCmd())

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("hb version %s\n", Version)
		},
	}
}

func loginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with your HookBridge API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			key, _ := cmd.Flags().GetString("api-key")
			if key == "" {
				reader := bufio.NewReader(os.Stdin)
				fmt.Print("Enter your HookBridge API key: ")
				var err error
				key, err = reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("could not read input: %w", err)
				}
				key = strings.TrimSpace(key)
			}
			if key == "" {
				return fmt.Errorf("API key cannot be empty")
			}

			// Load existing config for custom base URL, or use defaults
			existing, _ := config.Load()
			tmpCfg := &config.Config{}
			if existing != nil {
				tmpCfg = existing
			}
			baseURL := tmpCfg.APIBase()

			fmt.Print("Verifying... ")
			client := api.NewClient(baseURL, key)
			project, err := client.GetProject()
			if err != nil {
				fmt.Println("FAILED")
				return err
			}
			fmt.Println("OK")

			cfg := &config.Config{
				APIKey:    key,
				ProjectID: project.ID,
			}
			if existing != nil {
				cfg.APIBaseURL = existing.APIBaseURL
				cfg.StreamURL = existing.StreamURL
			}

			if err := config.Save(cfg); err != nil {
				return err
			}

			fmt.Printf("Project: %s\n", project.Name)
			cfgPath, _ := config.Path()
			fmt.Printf("Credentials saved to %s\n", cfgPath)
			return nil
		},
	}
	cmd.Flags().String("api-key", "", "API key (non-interactive)")
	return cmd
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.Remove(); err != nil {
				return err
			}
			fmt.Println("Logged out successfully.")
			return nil
		},
	}
}

func endpointsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "endpoints",
		Short: "Manage inbound endpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg.APIBase(), cfg.APIKey)
			endpoints, err := client.ListInboundEndpoints()
			if err != nil {
				return err
			}

			cliEndpoints := make([]api.InboundEndpoint, 0)
			for _, ep := range endpoints {
				if ep.Mode == "cli" {
					cliEndpoints = append(cliEndpoints, ep)
				}
			}

			if len(cliEndpoints) == 0 {
				fmt.Println("No CLI-mode endpoints found.")
				fmt.Println("Create one with: hb endpoints create --name \"My Endpoint\"")
				return nil
			}

			fmt.Printf("%-40s %-20s %-8s\n", "ID", "NAME", "ACTIVE")
			fmt.Printf("%-40s %-20s %-8s\n", strings.Repeat("-", 38), strings.Repeat("-", 18), strings.Repeat("-", 6))
			for _, ep := range cliEndpoints {
				active := "yes"
				if !ep.Active {
					active = "no"
				}
				fmt.Printf("%-40s %-20s %-8s\n", ep.ID, ep.Name, active)
			}
			return nil
		},
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new CLI-mode inbound endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			if name == "" {
				name = "CLI Endpoint"
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg.APIBase(), cfg.APIKey)
			ep, err := client.CreateInboundEndpoint(name)
			if err != nil {
				return err
			}

			fmt.Printf("Created endpoint: %s (%s)\n", ep.Name, ep.ID)
			fmt.Printf("Receive URL: %s\n", ep.ReceiveURL)
			return nil
		},
	}
	createCmd.Flags().String("name", "", "Endpoint name")
	cmd.AddCommand(createCmd)

	return cmd
}

func listenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Listen for webhooks and forward to localhost",
		RunE: func(cmd *cobra.Command, args []string) error {
			port, _ := cmd.Flags().GetInt("port")
			forwardURL, _ := cmd.Flags().GetString("forward")
			noForward, _ := cmd.Flags().GetBool("no-forward")
			verbose, _ := cmd.Flags().GetBool("verbose")
			endpointID, _ := cmd.Flags().GetString("endpoint")

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg.APIBase(), cfg.APIKey)

			// Find or create a CLI-mode endpoint
			var endpoint *api.InboundEndpoint
			if endpointID != "" {
				// Use specific endpoint — just verify it via list
				endpoints, err := client.ListInboundEndpoints()
				if err != nil {
					return err
				}
				for _, ep := range endpoints {
					if ep.ID == endpointID {
						endpoint = &ep
						break
					}
				}
				if endpoint == nil {
					return fmt.Errorf("endpoint %s not found", endpointID)
				}
				if endpoint.Mode != "cli" {
					return fmt.Errorf("endpoint %s is not in CLI mode", endpointID)
				}
			} else {
				// Find existing CLI-mode endpoint or create one
				endpoints, err := client.ListInboundEndpoints()
				if err != nil {
					return err
				}
				for _, ep := range endpoints {
					if ep.Mode == "cli" {
						endpoint = &ep
						break
					}
				}
				if endpoint == nil {
					fmt.Print("Creating CLI endpoint... ")
					ep, err := client.CreateInboundEndpoint("CLI Endpoint")
					if err != nil {
						return err
					}
					endpoint = ep
					fmt.Println("done")
				}
			}

			// Determine forwarding target
			var target string
			if !noForward {
				if forwardURL != "" {
					target = forwardURL
				} else {
					target = fmt.Sprintf("http://localhost:%d", port)
				}
			}

			// Print startup banner
			fmt.Printf("\nHookBridge CLI v%s\n", Version)
			fmt.Printf("Endpoint: %s (%s)\n", endpoint.Name, endpoint.ID)
			fmt.Printf("\nWebhook URL: %s\n", endpoint.ReceiveURL)
			fmt.Println("\nPaste this URL into your webhook provider's settings.")
			if target != "" {
				fmt.Printf("Forwarding to %s\n", target)
			} else {
				fmt.Println("Inspect mode — webhooks will be displayed but not forwarded.")
			}
			fmt.Println("Ready. Waiting for webhooks...")

			// Build forwarder
			var fwd *forwarder.Forwarder
			if target != "" {
				fwd = forwarder.New(target)
			}

			// Start resilient listener (WebSocket primary, polling fallback)
			rl := listener.NewResilientListener(
				cfg.Stream(),
				cfg.APIKey,
				endpoint.ID,
				client,
				fwd,
				verbose,
			)
			return rl.Run(cmd.Context())
		},
	}

	cmd.Flags().IntP("port", "p", 3000, "Localhost port to forward to")
	cmd.Flags().String("forward", "", "Full URL to forward to (overrides --port)")
	cmd.Flags().Bool("no-forward", false, "Display webhooks without forwarding")
	cmd.Flags().BoolP("verbose", "v", false, "Show full headers and body")
	cmd.Flags().String("endpoint", "", "Use a specific endpoint by ID")

	return cmd
}
