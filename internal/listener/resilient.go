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

// ResilientListener tries WebSocket first, falls back to polling on failure.
type ResilientListener struct {
	streamURL    string
	apiKey       string
	endpointID   string
	apiClient    *api.Client
	forwarder    *forwarder.Forwarder
	verbose      bool
	wsMaxRetries int
	wsBaseBackoff time.Duration
	count        int
}

// NewResilientListener creates a listener that prefers WebSocket with polling fallback.
func NewResilientListener(streamURL, apiKey, endpointID string, apiClient *api.Client, fwd *forwarder.Forwarder, verbose bool) *ResilientListener {
	return &ResilientListener{
		streamURL:    streamURL,
		apiKey:       apiKey,
		endpointID:   endpointID,
		apiClient:    apiClient,
		forwarder:    fwd,
		verbose:      verbose,
		wsMaxRetries: 3,
		wsBaseBackoff: 1 * time.Second,
	}
}

// Run starts the resilient listener. Tries WebSocket, falls back to polling.
func (rl *ResilientListener) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Try WebSocket first
	ws := NewWSClient(rl.streamURL, rl.apiKey, rl.endpointID)
	ws.baseBackoff = rl.wsBaseBackoff

	if err := ws.ConnectWithRetry(rl.wsMaxRetries); err != nil {
		fmt.Fprintf(os.Stderr, "WebSocket unavailable, using polling fallback: %v\n", err)
		return rl.runPolling(ctx)
	}

	fmt.Fprintln(os.Stderr, "Connected via WebSocket (real-time)")
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
			fmt.Printf("\nShutting down. %d webhook(s) received.\n", rl.count)
			return nil

		case msg := <-msgCh:
			rl.count++
			rl.handleWSMessage(ws, msg)

		case err := <-errCh:
			// WebSocket disconnected — try to reconnect, then fall back to polling
			fmt.Fprintf(os.Stderr, "\nWebSocket disconnected: %v\n", err)
			ws.Close()

			ws2 := NewWSClient(rl.streamURL, rl.apiKey, rl.endpointID)
			ws2.baseBackoff = rl.wsBaseBackoff
			if err := ws2.ConnectWithRetry(rl.wsMaxRetries); err != nil {
				fmt.Fprintf(os.Stderr, "Reconnect failed, switching to polling: %v\n", err)
				return rl.runPolling(ctx)
			}

			fmt.Fprintln(os.Stderr, "Reconnected via WebSocket")
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
	poller := NewPoller(rl.apiClient, rl.endpointID, rl.forwarder, rl.verbose)
	return poller.Run(ctx)
}

func (rl *ResilientListener) handleWSMessage(ws *WSClient, msg *api.ListenMessage) {
	now := time.Now().Format("15:04:05")

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

	if rl.verbose {
		if len(msg.Headers) > 0 {
			fmt.Println("  Headers:")
			for k, v := range msg.Headers {
				fmt.Printf("    %s: %s\n", k, v)
			}
		}
		if len(msg.Body) > 0 {
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
