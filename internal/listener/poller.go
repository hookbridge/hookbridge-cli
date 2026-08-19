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

	"github.com/hookbridge/hookbridge-cli/internal/api"
	"github.com/hookbridge/hookbridge-cli/internal/forwarder"
	"github.com/hookbridge/hookbridge-cli/internal/output"
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
	printer    *output.Printer
	count      int
}

// NewPoller creates a new polling listener.
func NewPoller(client *api.Client, endpointID string, fwd *forwarder.Forwarder, verbose bool, printer *output.Printer) *Poller {
	return &Poller{
		client:     client,
		endpointID: endpointID,
		forwarder:  fwd,
		verbose:    verbose,
		printer:    printer,
	}
}

// Run starts the polling loop. Blocks until context is cancelled or SIGINT/SIGTERM.
//
// Run does not emit a terminal event on exit — a polling phase can end
// because the session is switching to WebSocket, not just because the
// session is over. ResilientListener.Run is the only true session boundary
// and owns the single terminal shutdown emit.
func (p *Poller) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	for {
		resp, err := p.client.ListenMessages(p.endpointID, p.cursor)
		if err != nil {
			diagError(p.printer, fmt.Sprintf("poll error: %v", err), "  Poll error: %v\n", err)
			if !p.sleep(ctx, pollIntervalIdle) {
				break
			}
			continue
		}

		for _, msg := range resp.Messages {
			p.count++
			if err := p.handleMessage(msg); err != nil {
				return err
			}
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

	return nil
}

// Count returns the number of webhooks this poller has handled. Only safe
// to read after Run has returned; callers must synchronize through the
// channel Run's result was sent on — that receive is what happens-before
// orders Run's final write to count against this read.
func (p *Poller) Count() int {
	return p.count
}

func (p *Poller) handleMessage(msg api.ListenMessage) error {
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

	if p.printer.JSONMode() {
		return emitWebhookEvent(p.printer, msg, msg.SizeBytes, result)
	}

	renderWebhook(p.printer, msg, msg.SizeBytes, result, p.verbose)
	return nil
}

func (p *Poller) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
