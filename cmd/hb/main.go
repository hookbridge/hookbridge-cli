package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hookbridge/hookbridge-cli/internal/api"
	"github.com/hookbridge/hookbridge-cli/internal/config"
	"github.com/hookbridge/hookbridge-cli/internal/forwarder"
	"github.com/hookbridge/hookbridge-cli/internal/listener"
	"github.com/hookbridge/hookbridge-cli/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Version is set at build time via -ldflags
var Version = "dev"

// isTTY reports whether v is backed by an interactive terminal. It takes any
// because it is used for both input and output streams.
//
// This must be a real terminal check, not an os.ModeCharDevice test: /dev/null
// is itself a character device, and `hb ... < /dev/null` is the standard way
// scripts signal that no interaction is possible.
var isTTY = func(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// stdinIsTTY reports whether the given stdin is an interactive terminal. It is
// a variable so tests can simulate a terminal without one being attached.
var stdinIsTTY = func(in io.Reader) bool { return isTTY(in) }

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

	// --json governs command *results*: it keeps stdout limited to machine-readable
	// output and moves human text to stderr. --help is a deliberate exception — it's
	// an explicit request for human output, not a result, so cobra's help still goes
	// to stdout under --json (e.g. `hb --json --help | less` must keep working).
	cmd.PersistentFlags().Bool("json", false, "Output machine-readable JSON on stdout")
	cmd.PersistentFlags().Bool("no-color", false, "Disable ANSI colour output")

	cmd.AddCommand(versionCmd())
	cmd.AddCommand(loginCmd())
	cmd.AddCommand(logoutCmd())
	cmd.AddCommand(endpointsCmd())
	cmd.AddCommand(listenCmd())

	return cmd
}

// colorEnabled decides whether ANSI colour may be written. Precedence, highest
// first: --json (stdout is a machine stream), --no-color, the NO_COLOR env var,
// and finally whether stdout is actually a terminal.
func colorEnabled(cmd *cobra.Command, jsonMode bool) bool {
	if jsonMode {
		return false
	}
	if noColor, _ := cmd.Flags().GetBool("no-color"); noColor {
		return false
	}
	// https://no-color.org: honour the variable when present and non-empty.
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return isTTY(cmd.OutOrStdout())
}

func printerFor(cmd *cobra.Command) *output.Printer {
	jsonMode, _ := cmd.Flags().GetBool("json")
	return output.New(cmd.OutOrStdout(), cmd.ErrOrStderr(), jsonMode).
		WithColor(colorEnabled(cmd, jsonMode))
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			if p.JSONMode() {
				return p.Emit(map[string]string{"version": Version})
			}
			p.Out("hb version %s\n", Version)
			return nil
		},
	}
}

func loginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with your HookBridge API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			key, _ := cmd.Flags().GetString("api-key")
			if key == "" {
				reader := bufio.NewReader(os.Stdin)
				p.Note("Enter your HookBridge API key: ")
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

			p.Out("Verifying... ")
			client := api.NewClient(baseURL, key)
			project, err := client.GetProject()
			if err != nil {
				p.Out("FAILED\n")
				return err
			}
			p.Out("OK\n")

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

			p.Out("Project: %s\n", project.Name)
			cfgPath, _ := config.Path()
			p.Out("Credentials saved to %s\n", cfgPath)
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
			printerFor(cmd).Out("Logged out successfully.\n")
			return nil
		},
	}
}

func endpointsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "endpoints",
		Short: "Manage inbound endpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
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

			if p.JSONMode() {
				type endpointJSON struct {
					ID     string `json:"id"`
					Name   string `json:"name"`
					Active bool   `json:"active"`
				}
				result := make([]endpointJSON, 0, len(cliEndpoints))
				for _, ep := range cliEndpoints {
					result = append(result, endpointJSON{ID: ep.ID, Name: ep.Name, Active: ep.Active})
				}
				return p.Emit(result)
			}

			if len(cliEndpoints) == 0 {
				p.Out("No CLI-mode endpoints found.\n")
				p.Out("Create one with: hb endpoints create --name \"My Endpoint\"\n")
				return nil
			}

			p.Out("%-40s %-20s %-8s\n", "ID", "NAME", "ACTIVE")
			p.Out("%-40s %-20s %-8s\n", strings.Repeat("-", 38), strings.Repeat("-", 18), strings.Repeat("-", 6))
			for _, ep := range cliEndpoints {
				active := "yes"
				if !ep.Active {
					active = "no"
				}
				p.Out("%-40s %-20s %-8s\n", ep.ID, ep.Name, active)
			}
			return nil
		},
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new CLI-mode inbound endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			name, _ := cmd.Flags().GetString("name")
			if name == "" {
				name = "CLI Endpoint"
			}
			ephemeral, _ := cmd.Flags().GetBool("ephemeral")
			ttlMinutes, _ := cmd.Flags().GetInt("ttl-minutes")

			if cmd.Flags().Changed("ttl-minutes") {
				if !ephemeral {
					return fmt.Errorf("--ttl-minutes requires --ephemeral")
				}
				if ttlMinutes < 1 || ttlMinutes > 1440 {
					return fmt.Errorf("--ttl-minutes must be between 1 and 1440")
				}
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg.APIBase(), cfg.APIKey)
			ep, err := client.CreateInboundEndpoint(api.CreateInboundEndpointOptions{
				Name:       name,
				Ephemeral:  ephemeral,
				TTLMinutes: ttlMinutes,
			})
			if err != nil {
				return err
			}

			if p.JSONMode() {
				return p.Emit(map[string]string{"id": ep.ID, "receive_url": ep.ReceiveURL})
			}
			p.Out("Created endpoint: %s (%s)\n", ep.Name, ep.ID)
			p.Out("Receive URL: %s\n", ep.ReceiveURL)
			return nil
		},
	}
	createCmd.Flags().String("name", "", "Endpoint name")
	createCmd.Flags().Bool("ephemeral", false, "Create a short-lived endpoint that expires automatically")
	createCmd.Flags().Int("ttl-minutes", 0, "Minutes until an ephemeral endpoint expires (1-1440)")
	cmd.AddCommand(createCmd)

	deleteCmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an inbound endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			id := args[0]
			force, _ := cmd.Flags().GetBool("force")

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if !force && stdinIsTTY(cmd.InOrStdin()) {
				p.Note("Delete endpoint %s? This cannot be undone. [y/N]: ", id)
				reader := bufio.NewReader(cmd.InOrStdin())
				answer, err := reader.ReadString('\n')
				if err != nil && answer == "" {
					return fmt.Errorf("aborted")
				}
				answer = strings.ToLower(strings.TrimSpace(answer))
				if answer != "y" && answer != "yes" {
					return fmt.Errorf("aborted")
				}
			}

			client := api.NewClient(cfg.APIBase(), cfg.APIKey)
			if err := client.DeleteInboundEndpoint(id); err != nil {
				return err
			}

			if p.JSONMode() {
				return p.Emit(map[string]any{"id": id, "deleted": true})
			}
			p.Out("Deleted endpoint %s\n", id)
			return nil
		},
	}
	deleteCmd.Flags().BoolP("force", "f", false, "Skip the confirmation prompt")
	cmd.AddCommand(deleteCmd)

	return cmd
}

func listenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Listen for webhooks and forward to localhost",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
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
					p.Note("Creating CLI endpoint... ")
					ep, err := client.CreateInboundEndpoint(api.CreateInboundEndpointOptions{Name: "CLI Endpoint"})
					if err != nil {
						return err
					}
					endpoint = ep
					p.Note("done\n")
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
			p.Out("\nHookBridge CLI v%s\n", Version)
			p.Out("Endpoint: %s (%s)\n", endpoint.Name, endpoint.ID)
			p.Out("\nWebhook URL: %s\n", endpoint.ReceiveURL)
			p.Out("\nPaste this URL into your webhook provider's settings.\n")
			if target != "" {
				p.Out("Forwarding to %s\n", target)
			} else {
				p.Out("Inspect mode — webhooks will be displayed but not forwarded.\n")
			}
			if p.JSONMode() {
				type readyEvent struct {
					Event      string `json:"event"`
					EndpointID string `json:"endpoint_id"`
					ForwardTo  string `json:"forward_to"`
				}
				if err := p.Emit(readyEvent{Event: "ready", EndpointID: endpoint.ID, ForwardTo: target}); err != nil {
					return err
				}
			} else {
				p.Out("Ready. Waiting for webhooks...\n")
			}

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
				p,
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
