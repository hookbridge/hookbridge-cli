package listener

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hookbridgehq/hookbridge-cli/internal/api"
	"github.com/hookbridgehq/hookbridge-cli/internal/forwarder"
)

const (
	pollInterval     = 2 * time.Second
	pollIntervalIdle = 5 * time.Second
)

// Poller polls the HookBridge API for new webhook messages and forwards them locally.
type Poller struct {
	client     *api.Client
	endpointID string
	forwarder  *forwarder.Forwarder
	verbose    bool
	cursor     string
}

// NewPoller creates a new polling listener.
func NewPoller(client *api.Client, endpointID string, fwd *forwarder.Forwarder, verbose bool) *Poller {
	return &Poller{
		client:     client,
		endpointID: endpointID,
		forwarder:  fwd,
		verbose:    verbose,
	}
}

// Run starts the polling loop. Blocks until context is cancelled or SIGINT/SIGTERM.
func (p *Poller) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var count int
	for {
		resp, err := p.client.ListenMessages(p.endpointID, p.cursor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Poll error: %v\n", err)
			if !p.sleep(ctx, pollIntervalIdle) {
				break
			}
			continue
		}

		for _, msg := range resp.Messages {
			count++
			p.handleMessage(msg)
		}

		if resp.NextCursor != "" {
			p.cursor = resp.NextCursor
		}

		interval := pollIntervalIdle
		if len(resp.Messages) > 0 {
			interval = pollInterval
		}
		if !p.sleep(ctx, interval) {
			break
		}
	}

	fmt.Printf("\nShutting down. %d webhook(s) received.\n", count)
	return nil
}

func (p *Poller) handleMessage(msg api.ListenMessage) {
	now := time.Now().Format("15:04:05")

	// Forward if configured
	var result *forwarder.Result
	if p.forwarder != nil {
		body := msg.Body
		// Decode base64 body if needed
		if msg.BodyEncoding == "base64" {
			var encoded string
			if json.Unmarshal(body, &encoded) == nil {
				if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
					body = decoded
				}
			}
		}
		r := p.forwarder.Forward(body, msg.ContentType, msg.Headers)
		result = &r
	}

	// Print log line
	if result != nil {
		if result.Error != "" {
			fmt.Printf("%s  POST  →  \033[33mERR\033[0m  %s\n", now, result.Error)
		} else {
			color := statusColor(result.StatusCode)
			fmt.Printf("%s  POST  →  %s%d\033[0m  %dms  %s  (%d bytes)\n",
				now, color, result.StatusCode, result.LatencyMs,
				msg.ContentType, msg.SizeBytes)
		}
	} else {
		fmt.Printf("%s  POST  %s  (%d bytes)\n", now, msg.ContentType, msg.SizeBytes)
	}

	// Verbose output
	if p.verbose {
		if len(msg.Headers) > 0 {
			fmt.Println("  Headers:")
			for k, v := range msg.Headers {
				fmt.Printf("    %s: %s\n", k, v)
			}
		}
		if len(msg.Body) > 0 {
			// Try to pretty-print JSON
			var indented json.RawMessage
			if json.Unmarshal(msg.Body, &indented) == nil {
				if pretty, err := json.MarshalIndent(indented, "  ", "  "); err == nil {
					fmt.Printf("  Body:\n  %s\n", string(pretty))
				} else {
					fmt.Printf("  Body: %s\n", string(msg.Body))
				}
			} else {
				fmt.Printf("  Body: %s\n", string(msg.Body))
			}
		}
		fmt.Println()
	}
}

func (p *Poller) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func statusColor(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "\033[32m" // green
	case code >= 400 && code < 500:
		return "\033[31m" // red
	case code >= 500:
		return "\033[31m" // red
	default:
		return "\033[33m" // yellow
	}
}
