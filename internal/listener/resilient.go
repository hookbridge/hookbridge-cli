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

// ResilientListener tries WebSocket first, falls back to polling on failure.
type ResilientListener struct {
	streamURL       string
	apiKey          string
	endpointID      string
	apiClient       *api.Client
	forwarder       *forwarder.Forwarder
	verbose         bool
	wsMaxRetries    int
	wsBaseBackoff   time.Duration
	wsRetryInterval time.Duration
	count           int
	onMessage       func(msg *api.ListenMessage, displaySize int) // optional test hook
	printer         *output.Printer
}

// NewResilientListener creates a listener that prefers WebSocket with polling fallback.
func NewResilientListener(streamURL, apiKey, endpointID string, apiClient *api.Client, fwd *forwarder.Forwarder, verbose bool, printer *output.Printer) *ResilientListener {
	return &ResilientListener{
		streamURL:       streamURL,
		apiKey:          apiKey,
		endpointID:      endpointID,
		apiClient:       apiClient,
		forwarder:       fwd,
		verbose:         verbose,
		wsMaxRetries:    3,
		wsBaseBackoff:   1 * time.Second,
		wsRetryInterval: 60 * time.Second,
		printer:         printer,
	}
}

// Run starts the resilient listener. Tries WebSocket, falls back to polling.
//
// Run is the only true session boundary: runPolling and runWebSocket can
// hand off to each other any number of times, but every path returns back
// through here, so the terminal shutdown event is emitted exactly once,
// after the session has truly ended.
func (rl *ResilientListener) Run(ctx context.Context) (err error) {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	defer func() {
		if serr := emitShutdown(rl.printer, rl.count); err == nil {
			err = serr
		}
	}()

	// Try WebSocket first
	ws := NewWSClient(rl.streamURL, rl.apiKey, rl.endpointID)
	ws.baseBackoff = rl.wsBaseBackoff

	if err := ws.ConnectWithRetry(rl.wsMaxRetries); err != nil {
		diagError(rl.printer, fmt.Sprintf("WebSocket unavailable, using polling fallback: %v", err),
			"WebSocket unavailable, using polling fallback: %v\n", err)
		return rl.runPolling(ctx)
	}

	diagStatus(rl.printer, "Connected via WebSocket (real-time)", "Connected via WebSocket (real-time)")
	return rl.runWebSocket(ctx, ws)
}

func (rl *ResilientListener) runWebSocket(ctx context.Context, ws *WSClient) error {
	defer ws.Close()

	msgCh := make(chan *api.ListenMessage)
	errCh := make(chan error, 1)

	// Read messages in a goroutine
	go func() {
		for {
			msg, err := ws.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil

		case msg := <-msgCh:
			rl.count++
			if err := rl.handleWSMessage(ws, msg); err != nil {
				return err
			}

		case err := <-errCh:
			// WebSocket disconnected — try to reconnect, then fall back to polling
			diagError(rl.printer, fmt.Sprintf("WebSocket disconnected: %v", err), "\nWebSocket disconnected: %v\n", err)
			ws.Close()

			ws2 := NewWSClient(rl.streamURL, rl.apiKey, rl.endpointID)
			ws2.baseBackoff = rl.wsBaseBackoff
			if err := ws2.ConnectWithRetry(rl.wsMaxRetries); err != nil {
				diagError(rl.printer, fmt.Sprintf("Reconnect failed, switching to polling: %v", err),
					"Reconnect failed, switching to polling: %v\n", err)
				return rl.runPolling(ctx)
			}

			diagStatus(rl.printer, "Reconnected via WebSocket", "Reconnected via WebSocket")
			ws = ws2

			// Restart read goroutine
			go func() {
				for {
					msg, err := ws.ReadMessage()
					if err != nil {
						errCh <- err
						return
					}
					msgCh <- msg
				}
			}()
		}
	}
}

func (rl *ResilientListener) runPolling(ctx context.Context) error {
	poller := NewPoller(rl.apiClient, rl.endpointID, rl.forwarder, rl.verbose, rl.printer)

	pollerCtx, pollerCancel := context.WithCancel(ctx)
	defer pollerCancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- poller.Run(pollerCtx)
	}()

	// Every return path below receives from errCh — directly, or via the
	// ticker.C branch's own <-errCh — before returning. That receive is
	// what happens-before orders the poller goroutine's final write to its
	// count against this read, so folding it into rl.count here, once this
	// function is done with the poller, is safe on every path (including
	// the hand-off to runWebSocket, whose own defers all run before this
	// one does).
	defer func() {
		rl.count += poller.Count()
	}()

	ticker := time.NewTicker(rl.wsRetryInterval)
	defer ticker.Stop()

	for {
		select {
		case err := <-errCh:
			return err
		case <-ticker.C:
			ws := NewWSClient(rl.streamURL, rl.apiKey, rl.endpointID)
			if err := ws.Connect(); err == nil {
				diagStatus(rl.printer, "WebSocket reconnected, switching from polling", "WebSocket reconnected, switching from polling")
				pollerCancel()
				if err := <-errCh; err != nil {
					return err
				}
				return rl.runWebSocket(ctx, ws)
			}
		case <-ctx.Done():
			pollerCancel()
			return <-errCh
		}
	}
}

func (rl *ResilientListener) handleWSMessage(ws *WSClient, msg *api.ListenMessage) error {
	// Compute display size: use SizeBytes if provided (polling), otherwise derive from body
	displaySize := msg.SizeBytes
	if displaySize == 0 {
		displaySize = len(msg.Body)
	}

	var result *forwarder.Result
	if rl.forwarder != nil {
		body := msg.Body
		if msg.BodyEncoding == "base64" {
			var encoded string
			if json.Unmarshal(body, &encoded) == nil {
				if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
					body = decoded
				}
			}
		}
		r := rl.forwarder.Forward(body, msg.ContentType, msg.Headers)
		result = &r

		// Send delivery result back via WebSocket
		if result.Error == "" {
			ws.SendDeliveryResult(msg.MessageID, result.StatusCode, result.LatencyMs)
		}
	}

	if rl.onMessage != nil {
		rl.onMessage(msg, displaySize)
	}

	if rl.printer.JSONMode() {
		return emitWebhookEvent(rl.printer, *msg, displaySize, result)
	}

	renderWebhook(rl.printer, *msg, displaySize, result, rl.verbose)
	return nil
}
