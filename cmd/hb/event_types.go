package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hookbridge/hookbridge-cli/internal/api"
	"github.com/hookbridge/hookbridge-cli/internal/config"
	"github.com/hookbridge/hookbridge-cli/internal/termsafe"
	"github.com/spf13/cobra"
)

const (
	maxPayloadBytes    = 1 << 20
	maxSchemaBytes     = 64 << 10
	maxSampleBytes     = 64 << 10
	maxEventTypesLimit = 1000
	maxEventsLimit     = 500
)

func newAPIClient() (*api.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return api.NewClient(cfg.APIBase(), cfg.APIKey).WithSendURL(cfg.SendBase()), nil
}

func readLimited(r io.Reader, max int) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, int64(max)+1))
}

func parseJSONInput(flag string, data []byte, wantObject bool, max int) (json.RawMessage, error) {
	if len(data) > max {
		return nil, fmt.Errorf("%s is too large (limit %s)", flag, formatBytes(max))
	}
	trimmed := bytes.TrimSpace(data)
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("%s must contain valid JSON", flag)
	}
	if wantObject && trimmed[0] != '{' {
		return nil, fmt.Errorf("%s must be a JSON object", flag)
	}
	return json.RawMessage(trimmed), nil
}

func formatBytes(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", n>>10)
}

func readJSONFile(flag, path string, wantObject bool, max int) (json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: could not read file: %w", flag, err)
	}
	defer func() { _ = f.Close() }()
	data, err := readLimited(f, max)
	if err != nil {
		return nil, fmt.Errorf("%s: could not read file: %w", flag, err)
	}
	return parseJSONInput(flag, data, wantObject, max)
}

func checkLimit(cmd *cobra.Command, max int) error {
	if !cmd.Flags().Changed("limit") {
		return nil
	}
	if n, _ := cmd.Flags().GetInt("limit"); n < 1 || n > max {
		return fmt.Errorf("--limit must be between 1 and %d", max)
	}
	return nil
}

func safeText(s string) string { return termsafe.Clean(s) }

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(safeText(s), "'", `'\''`) + "'"
}

func addPayloadFlags(cmd *cobra.Command) {
	cmd.Flags().String("data", "", `JSON object payload, or "-" to read it from stdin`)
	cmd.Flags().String("data-file", "", "Path to a file containing the JSON object payload")
}

// payloadFromFlags returns nil when neither flag is set.
func payloadFromFlags(cmd *cobra.Command) (json.RawMessage, error) {
	data, _ := cmd.Flags().GetString("data")
	file, _ := cmd.Flags().GetString("data-file")
	if cmd.Flags().Changed("data") && cmd.Flags().Changed("data-file") {
		return nil, fmt.Errorf("use only one of --data or --data-file")
	}
	switch {
	case cmd.Flags().Changed("data-file"):
		return readJSONFile("--data-file", file, true, maxPayloadBytes)
	case data == "-":
		in, err := readLimited(cmd.InOrStdin(), maxPayloadBytes)
		if err != nil {
			return nil, fmt.Errorf("--data: could not read stdin: %w", err)
		}
		return parseJSONInput("--data", in, true, maxPayloadBytes)
	case cmd.Flags().Changed("data"):
		return parseJSONInput("--data", []byte(data), true, maxPayloadBytes)
	}
	return nil, nil
}

func parseTimeFlag(flag, v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp, e.g. 2026-01-02T15:04:05Z", flag)
	}
	return t, nil
}

func parseTimeRange(cmd *cobra.Command) (since, until string, err error) {
	since, _ = cmd.Flags().GetString("since")
	until, _ = cmd.Flags().GetString("until")
	var s, u time.Time
	if since != "" {
		if s, err = parseTimeFlag("--since", since); err != nil {
			return "", "", err
		}
	}
	if until != "" {
		if u, err = parseTimeFlag("--until", until); err != nil {
			return "", "", err
		}
	}
	if since != "" && until != "" && !u.After(s) {
		return "", "", fmt.Errorf("--until must be after --since")
	}
	return since, until, nil
}

func confirm(cmd *cobra.Command, prompt string) error {
	p := printerFor(cmd)
	p.Note("%s [y/N]: ", prompt)
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && answer == "" {
		return fmt.Errorf("aborted")
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("aborted")
	}
	return nil
}

func eventTypeStatus(et api.EventType) string {
	switch {
	case et.ArchivedAt != nil:
		return "archived"
	case et.Deprecated:
		return "deprecated"
	}
	return "active"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return termsafe.EscapeC1(string(raw))
	}
	return termsafe.EscapeC1(buf.String())
}

func isNullJSON(raw json.RawMessage) bool {
	return len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

type listJSON[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func eventTypesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "event-types",
		Short: "Manage event types",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List event types",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			group, _ := cmd.Flags().GetString("group")
			prefix, _ := cmd.Flags().GetString("prefix")
			archived, _ := cmd.Flags().GetBool("include-archived")
			limit, _ := cmd.Flags().GetInt("limit")
			cursor, _ := cmd.Flags().GetString("cursor")
			if err := checkLimit(cmd, maxEventTypesLimit); err != nil {
				return err
			}

			client, err := newAPIClient()
			if err != nil {
				return err
			}
			page, err := client.ListEventTypes(api.ListEventTypesParams{
				Group: group, NamePrefix: prefix, IncludeArchived: archived, Limit: limit, Cursor: cursor,
			})
			if err != nil {
				return err
			}
			if p.JSONMode() {
				data := page.Data
				if data == nil {
					data = []api.EventType{}
				}
				return p.Emit(listJSON[api.EventType]{Data: data, NextCursor: page.NextCursor})
			}
			if len(page.Data) == 0 {
				p.Out("No event types found.\n")
				return nil
			}
			p.Out("%-40s %-20s %-10s\n", "NAME", "GROUP", "STATUS")
			p.Out("%-40s %-20s %-10s\n", strings.Repeat("-", 38), strings.Repeat("-", 18), strings.Repeat("-", 8))
			for _, et := range page.Data {
				group := ""
				if et.Group != nil {
					group = *et.Group
				}
				p.Out("%-40s %-20s %-10s\n", safeText(et.Name), orDash(safeText(group)), eventTypeStatus(et))
			}
			if page.NextCursor != "" {
				p.Out("\nMore results: hb event-types list --cursor %s\n", shellQuote(page.NextCursor))
			}
			return nil
		},
	}
	listCmd.Flags().String("group", "", "Only list types in this group")
	listCmd.Flags().String("prefix", "", "Only list types whose name starts with this prefix")
	listCmd.Flags().Bool("include-archived", false, "Include archived event types")
	listCmd.Flags().Int("limit", 0, "Maximum number of results (1-1000)")
	listCmd.Flags().String("cursor", "", "Pagination cursor from a previous page")
	cmd.AddCommand(listCmd)

	getCmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show one event type",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			client, err := newAPIClient()
			if err != nil {
				return err
			}
			et, err := client.GetEventType(args[0])
			if err != nil {
				return err
			}
			if p.JSONMode() {
				return p.Emit(et)
			}
			group := ""
			if et.Group != nil {
				group = *et.Group
			}
			p.Out("Name:        %s\n", safeText(et.Name))
			p.Out("Status:      %s\n", eventTypeStatus(*et))
			p.Out("Group:       %s\n", orDash(safeText(group)))
			p.Out("Description: %s\n", orDash(safeText(et.Description)))
			if et.CreatedAt != "" {
				p.Out("Created:     %s\n", safeText(et.CreatedAt))
			}
			if !isNullJSON(et.Schema) {
				p.Out("\nSchema:\n%s\n", prettyJSON(et.Schema))
			}
			if !isNullJSON(et.SamplePayload) {
				p.Out("\nSample payload:\n%s\n", prettyJSON(et.SamplePayload))
			}
			return nil
		},
	}
	cmd.AddCommand(getCmd)

	createCmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an event type",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			req := api.CreateEventTypeRequest{Name: args[0]}
			req.Description, _ = cmd.Flags().GetString("description")
			req.Group, _ = cmd.Flags().GetString("group")
			req.Deprecated, _ = cmd.Flags().GetBool("deprecated")
			if path, _ := cmd.Flags().GetString("schema-file"); path != "" {
				schema, err := readJSONFile("--schema-file", path, true, maxSchemaBytes)
				if err != nil {
					return err
				}
				req.Schema = schema
			}
			if path, _ := cmd.Flags().GetString("sample-file"); path != "" {
				sample, err := readJSONFile("--sample-file", path, false, maxSampleBytes)
				if err != nil {
					return err
				}
				req.SamplePayload = sample
			}

			client, err := newAPIClient()
			if err != nil {
				return err
			}
			et, err := client.CreateEventType(req)
			if err != nil {
				return err
			}
			if p.JSONMode() {
				return p.Emit(et)
			}
			p.Out("Created event type %s\n", safeText(et.Name))
			return nil
		},
	}
	createCmd.Flags().String("description", "", "Human-readable description")
	createCmd.Flags().String("group", "", "Group name (lowercase letters, digits, _ and -)")
	createCmd.Flags().String("schema-file", "", "Path to a JSON Schema file")
	createCmd.Flags().String("sample-file", "", "Path to a sample payload JSON file")
	createCmd.Flags().Bool("deprecated", false, "Mark the event type as deprecated")
	cmd.AddCommand(createCmd)

	archiveCmd := &cobra.Command{
		Use:   "archive <name>",
		Short: "Archive an event type",
		Long:  "Archive an event type. Nothing is deleted: past events are untouched and the type stays readable.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			force, _ := cmd.Flags().GetBool("force")
			client, err := newAPIClient()
			if err != nil {
				return err
			}
			if !force && stdinIsTTY(cmd.InOrStdin()) {
				if err := confirm(cmd, fmt.Sprintf("Archive event type %s?", args[0])); err != nil {
					return err
				}
			}
			et, err := client.ArchiveEventType(args[0])
			if err != nil {
				return err
			}
			if p.JSONMode() {
				return p.Emit(et)
			}
			p.Out("Archived event type %s\n", safeText(args[0]))
			return nil
		},
	}
	archiveCmd.Flags().BoolP("force", "f", false, "Skip the confirmation prompt")
	cmd.AddCommand(archiveCmd)

	return cmd
}

func sendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a webhook or event",
		Long: `Send a JSON payload. Give --type to send an event (delivered to every
subscribed endpoint, or only to --endpoint when given), or --endpoint alone to
send straight to one outbound endpoint.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			eventType, _ := cmd.Flags().GetString("type")
			endpointID, _ := cmd.Flags().GetString("endpoint")
			key, _ := cmd.Flags().GetString("idempotency-key")
			if eventType == "" && endpointID == "" {
				return fmt.Errorf("--type or --endpoint is required")
			}
			payload, err := payloadFromFlags(cmd)
			if err != nil {
				return err
			}
			if payload == nil {
				return fmt.Errorf("--data or --data-file is required")
			}

			client, err := newAPIClient()
			if err != nil {
				return err
			}
			res, err := client.SendWebhook(api.SendRequest{
				EndpointID: endpointID, EventType: eventType, Payload: payload, IdempotencyKey: key,
			})
			if err != nil {
				return err
			}
			if p.JSONMode() {
				return p.Emit(res)
			}
			if res.EventID != "" {
				p.Out("Event ID:   %s\n", safeText(res.EventID))
			}
			if res.EventType != "" {
				p.Out("Type:       %s\n", safeText(res.EventType))
			}
			p.Out("Status:     %s\n", safeText(res.Status))
			if res.MessageID != "" {
				p.Out("Message ID: %s\n", safeText(res.MessageID))
			}
			return nil
		},
	}
	cmd.Flags().String("type", "", "Event type name, e.g. order.created")
	cmd.Flags().String("endpoint", "", "Outbound endpoint ID to deliver to")
	cmd.Flags().String("idempotency-key", "", "Key that makes retries safe to repeat")
	addPayloadFlags(cmd)
	return cmd
}

func eventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Inspect events",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List events created by typed sends, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			eventType, _ := cmd.Flags().GetString("type")
			limit, _ := cmd.Flags().GetInt("limit")
			cursor, _ := cmd.Flags().GetString("cursor")
			if err := checkLimit(cmd, maxEventsLimit); err != nil {
				return err
			}
			since, until, err := parseTimeRange(cmd)
			if err != nil {
				return err
			}

			client, err := newAPIClient()
			if err != nil {
				return err
			}
			page, err := client.ListEvents(api.ListEventsParams{
				EventType: eventType, Since: since, Until: until, Limit: limit, Cursor: cursor,
			})
			if err != nil {
				return err
			}
			if p.JSONMode() {
				data := page.Data
				if data == nil {
					data = []api.Event{}
				}
				return p.Emit(listJSON[api.Event]{Data: data, NextCursor: page.NextCursor})
			}
			if len(page.Data) == 0 {
				p.Out("No events found.\n")
				return nil
			}
			p.Out("%-38s %-30s %-8s %-8s %s\n", "ID", "TYPE", "FANOUT", "MATCHED", "CREATED")
			p.Out("%-38s %-30s %-8s %-8s %s\n", strings.Repeat("-", 36), strings.Repeat("-", 28), strings.Repeat("-", 6), strings.Repeat("-", 7), strings.Repeat("-", 20))
			for _, ev := range page.Data {
				p.Out("%-38s %-30s %-8s %-8d %s\n", safeText(ev.ID), safeText(ev.EventType), safeText(ev.FanoutStatus), ev.MatchedCount, safeText(ev.CreatedAt))
			}
			if page.NextCursor != "" {
				p.Out("\nMore results: hb events list --cursor %s\n", shellQuote(page.NextCursor))
			}
			return nil
		},
	}
	listCmd.Flags().String("type", "", "Only list events of this type")
	listCmd.Flags().String("since", "", "Only events at or after this RFC3339 time")
	listCmd.Flags().String("until", "", "Only events before this RFC3339 time")
	listCmd.Flags().Int("limit", 0, "Maximum number of results (1-500)")
	listCmd.Flags().String("cursor", "", "Pagination cursor from a previous page")
	cmd.AddCommand(listCmd)

	return cmd
}

func endpointRecoverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recover <outbound-endpoint-id>",
		Short: "Recover events an outbound endpoint missed",
		Long: `Start a job that gives an outbound endpoint the typed events it missed in a
time window, or check on a job with --job. Takes an outbound endpoint ID
(ep_...), not an inbound CLI endpoint.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			jobID, _ := cmd.Flags().GetString("job")
			since, until, err := parseTimeRange(cmd)
			if err != nil {
				return err
			}
			if jobID != "" && (since != "" || until != "") {
				return fmt.Errorf("--job cannot be combined with --since or --until")
			}
			if jobID == "" && since == "" {
				return fmt.Errorf("--since is required (or pass --job to check a job)")
			}

			client, err := newAPIClient()
			if err != nil {
				return err
			}
			if jobID != "" {
				job, err := client.GetRecover(args[0], jobID)
				if err != nil {
					return err
				}
				if p.JSONMode() {
					return p.Emit(job)
				}
				p.Out("Job:      %s\n", safeText(job.RecoverJobID))
				p.Out("Status:   %s\n", safeText(job.Status))
				p.Out("Created:  %d\n", job.CreatedCount)
				p.Out("Requeued: %d\n", job.RequeuedCount)
				if job.Since != "" {
					p.Out("Since:    %s\n", safeText(job.Since))
				}
				if job.Until != "" {
					p.Out("Until:    %s\n", safeText(job.Until))
				}
				if job.FinishedAt != "" {
					p.Out("Finished: %s\n", safeText(job.FinishedAt))
				}
				return nil
			}

			job, err := client.StartRecover(args[0], since, until)
			if err != nil {
				return err
			}
			if p.JSONMode() {
				return p.Emit(job)
			}
			p.Out("Recover job started: %s (%s)\n", safeText(job.RecoverJobID), safeText(job.Status))
			p.Out("Check progress with: hb endpoints recover %s --job %s\n", shellQuote(args[0]), shellQuote(job.RecoverJobID))
			return nil
		},
	}
	cmd.Flags().String("since", "", "Start of the window to recover (RFC3339)")
	cmd.Flags().String("until", "", "End of the window (RFC3339, defaults to now)")
	cmd.Flags().String("job", "", "Check the status of a recover job instead of starting one")
	return cmd
}

func endpointTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test <outbound-endpoint-id>",
		Short: "Send a test event to an outbound endpoint",
		Long: `Send one test message of an existing event type to an outbound endpoint (ep_...),
to check your receiver. Takes an outbound endpoint ID, not an inbound CLI
endpoint. Uses the type's sample payload unless --data or --data-file is given.
Limited to 10 test events per minute per endpoint.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFor(cmd)
			eventType, _ := cmd.Flags().GetString("type")
			if eventType == "" {
				return fmt.Errorf("--type is required")
			}
			payload, err := payloadFromFlags(cmd)
			if err != nil {
				return err
			}

			client, err := newAPIClient()
			if err != nil {
				return err
			}
			res, err := client.SendTestEvent(args[0], eventType, payload)
			if err != nil {
				return err
			}
			if p.JSONMode() {
				return p.Emit(res)
			}
			p.Out("Test event queued\n")
			p.Out("Message ID: %s\n", safeText(res.MessageID))
			p.Out("Status:     %s\n", safeText(res.Status))
			return nil
		},
	}
	cmd.Flags().String("type", "", "Event type to send (must already exist)")
	addPayloadFlags(cmd)
	return cmd
}
