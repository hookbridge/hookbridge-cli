package listener

import (
	"github.com/hookbridge/hookbridge-cli/internal/api"
	"github.com/hookbridge/hookbridge-cli/internal/forwarder"
	"github.com/hookbridge/hookbridge-cli/internal/output"
)

type webhookEvent struct {
	Event       string `json:"event"`
	ID          string `json:"id"`
	ContentType string `json:"content_type"`
	SizeBytes   int    `json:"size_bytes"`
	ReceivedAt  string `json:"received_at"`
	Forwarded   bool   `json:"forwarded"`
	StatusCode  int    `json:"status_code,omitempty"`
	LatencyMs   *int64 `json:"latency_ms,omitempty"`
	Error       string `json:"error,omitempty"`
}

type shutdownEvent struct {
	Event    string `json:"event"`
	Received int    `json:"received"`
}

type diagnosticEvent struct {
	Event   string `json:"event"`
	Message string `json:"message"`
}

func buildWebhookEvent(msg api.ListenMessage, sizeBytes int, result *forwarder.Result) webhookEvent {
	ev := webhookEvent{
		Event:       "webhook",
		ID:          msg.MessageID,
		ContentType: msg.ContentType,
		SizeBytes:   sizeBytes,
		ReceivedAt:  msg.ReceivedAt,
	}
	if result != nil {
		if result.Error != "" {
			ev.Error = result.Error
		} else {
			ev.Forwarded = true
			ev.StatusCode = result.StatusCode
			latency := result.LatencyMs
			ev.LatencyMs = &latency
		}
	}
	return ev
}

// emitWebhookEvent writes a webhook record to stdout. Callers must treat a
// non-nil error as unrecoverable data loss (e.g. a downstream EPIPE) and
// stop the listen loop rather than keep polling into a dead stdout.
func emitWebhookEvent(p *output.Printer, msg api.ListenMessage, sizeBytes int, result *forwarder.Result) error {
	return p.Emit(buildWebhookEvent(msg, sizeBytes, result))
}

// emitShutdown writes the closing summary. See emitWebhookEvent: a non-nil
// error here means stdout is gone and the caller must propagate it.
func emitShutdown(p *output.Printer, count int) error {
	if p.JSONMode() {
		return p.Emit(shutdownEvent{Event: "shutdown", Received: count})
	}
	p.Out("\nShutting down. %d webhook(s) received.\n", count)
	return nil
}

// diagError reports a failure. In normal mode it writes humanFormat to
// stderr, byte-identical to the old direct fmt.Fprintf calls it replaces. In
// --json mode it writes an NDJSON error event to stderr instead.
//
// Unlike emitWebhookEvent/emitShutdown, the write error is deliberately
// discarded here: this is a best-effort diagnostic on stderr, not the data
// stream on stdout, and failing to report a diagnostic must never itself
// take down the listener.
func diagError(p *output.Printer, jsonMessage, humanFormat string, humanArgs ...any) {
	if p.JSONMode() {
		_ = p.EmitErr(diagnosticEvent{Event: "error", Message: jsonMessage})
		return
	}
	p.Note(humanFormat, humanArgs...)
}

// diagStatus reports an informational connection transition. See diagError
// for why its write error is intentionally discarded.
func diagStatus(p *output.Printer, jsonMessage, humanText string) {
	if p.JSONMode() {
		_ = p.EmitErr(diagnosticEvent{Event: "status", Message: jsonMessage})
		return
	}
	p.Note("%s\n", humanText)
}
