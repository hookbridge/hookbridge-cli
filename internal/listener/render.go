package listener

import (
	"encoding/json"
	"time"

	"github.com/hookbridge/hookbridge-cli/internal/api"
	"github.com/hookbridge/hookbridge-cli/internal/forwarder"
	"github.com/hookbridge/hookbridge-cli/internal/output"
)

const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
)

// statusColor returns the ANSI colour for an HTTP status, or "" when colour is
// disabled. Callers pair it with colorReset so the escapes appear or vanish together.
func statusColor(p *output.Printer, code int) string {
	if !p.Color() {
		return ""
	}
	switch {
	case code >= 200 && code < 300:
		return colorGreen
	case code >= 400:
		return colorRed
	default:
		return colorYellow
	}
}

// renderWebhook writes the human-readable log line (and, if verbose, the
// headers/body block) for a received webhook. Callers must check
// p.JSONMode() and emit the NDJSON event instead before reaching here — that
// ordering, not this function, is what keeps colour escapes out of --json's
// stdout stream.
func renderWebhook(p *output.Printer, msg api.ListenMessage, sizeBytes int, result *forwarder.Result, verbose bool) {
	now := time.Now().Format("15:04:05")
	reset := ""
	errColor := ""
	if p.Color() {
		reset = colorReset
		errColor = colorYellow
	}

	if result != nil {
		if result.Error != "" {
			p.Out("%s  POST  →  %sERR%s  %s\n", now, errColor, reset, result.Error)
		} else {
			color := statusColor(p, result.StatusCode)
			p.Out("%s  POST  →  %s%d%s  %dms  %s  (%d bytes)\n",
				now, color, result.StatusCode, reset, result.LatencyMs,
				msg.ContentType, sizeBytes)
		}
	} else {
		p.Out("%s  POST  %s  (%d bytes)\n", now, msg.ContentType, sizeBytes)
	}

	if verbose {
		if len(msg.Headers) > 0 {
			p.Out("  Headers:\n")
			for k, v := range msg.Headers {
				p.Out("    %s: %s\n", k, v)
			}
		}
		if len(msg.Body) > 0 {
			var indented json.RawMessage
			if json.Unmarshal(msg.Body, &indented) == nil {
				if pretty, err := json.MarshalIndent(indented, "  ", "  "); err == nil {
					p.Out("  Body:\n  %s\n", string(pretty))
				} else {
					p.Out("  Body: %s\n", string(msg.Body))
				}
			} else {
				p.Out("  Body: %s\n", string(msg.Body))
			}
		}
		p.Out("\n")
	}
}
