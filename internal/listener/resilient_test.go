package listener

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hookbridge/hookbridge-cli/internal/api"
	"github.com/hookbridge/hookbridge-cli/internal/forwarder"
	"github.com/hookbridge/hookbridge-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failFirstWriter fails its first Write call, then succeeds on every call
// after. Used to prove a genuine session error is preserved even when a
// later shutdown emit on the same writer succeeds.
type failFirstWriter struct {
	failed bool
}

func (w *failFirstWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, errors.New("first write failed: simulated session error")
	}
	return len(p), nil
}

func TestResilient_UsesWebSocketWhenAvailable(t *testing.T) {
	var wsMessageSent atomic.Bool
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Send one message
		conn.WriteJSON(map[string]any{
			"type":         "webhook",
			"message_id":   "msg_ws",
			"content_type": "application/json",
			"headers":      map[string]string{},
			"body":         map[string]any{"from": "ws"},
			"size_bytes":   10,
			"received_at":  "2026-03-21T10:30:00Z",
		})
		wsMessageSent.Store(true)

		// Keep alive until client disconnects
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	// API server shouldn't be called when WS is working
	var apiCalled atomic.Bool
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled.Store(true)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, discardPrinter())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	assert.True(t, wsMessageSent.Load(), "WebSocket should have sent a message")
	assert.False(t, apiCalled.Load(), "API polling should not be used when WebSocket is connected")
}

func TestResilient_FallsBackToPollingAfterWSFailures(t *testing.T) {
	// No WebSocket server — all connections will fail
	var apiCallCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCallCount.Add(1)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	rl := NewResilientListener("ws://localhost:19999", "hb_live_key", "ie_1", apiClient, nil, false, discardPrinter())
	rl.wsMaxRetries = 2
	rl.wsBaseBackoff = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	// Polling should have been called since WS failed
	assert.Greater(t, int(apiCallCount.Load()), 0, "Should have fallen back to polling")
}

func TestResilient_RunPolling_JSONMode_CtxDone_PropagatesShutdownEmitError(t *testing.T) {
	// The poller no longer emits its own shutdown event — the terminal
	// emit now lives solely in ResilientListener.Run, so this drives the
	// polling-fallback ctx.Done() scenario through Run rather than calling
	// runPolling directly, while still proving the original guarantee: a
	// failed terminal emit propagates and the CLI exits non-zero.
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	apiClient := api.NewClient(apiServer.URL, "hb_live_key")
	printer := output.New(failingWriter{}, failingWriter{}, true)

	// No WebSocket server at this address — Run falls back to polling.
	rl := NewResilientListener("ws://localhost:19999", "hb_live_key", "ie_1", apiClient, nil, false, printer)
	rl.wsMaxRetries = 1
	rl.wsBaseBackoff = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := rl.Run(ctx)

	assert.Error(t, err, "Run must propagate the shutdown-emit failure instead of discarding it")
}

func TestResilient_RunPolling_NormalMode_CtxDone_ReturnsNil(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	apiClient := api.NewClient(apiServer.URL, "hb_live_key")
	rl := NewResilientListener("ws://localhost:19999", "hb_live_key", "ie_1", apiClient, nil, false, discardPrinter())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := rl.runPolling(ctx)

	assert.NoError(t, err, "a plain Ctrl-C in human mode must still exit 0")
}

func TestResilient_SizeBytesComputedFromBody(t *testing.T) {
	// The CLIWebhookEvent from the stream-service does NOT include size_bytes.
	// The CLI must compute size from the body when size_bytes is 0.
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Send message WITHOUT size_bytes (mimics real CLIWebhookEvent)
		conn.WriteJSON(map[string]any{
			"type":         "webhook",
			"message_id":   "msg_size",
			"content_type": "application/json",
			"headers":      map[string]string{},
			"body":         map[string]any{"event": "test", "data": "hello"},
			"received_at":  "2026-03-21T10:30:00Z",
		})

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient("http://localhost:19999", "hb_live_key")

	var capturedSize int
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, discardPrinter())
	rl.onMessage = func(msg *api.ListenMessage, size int) {
		capturedSize = size
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	assert.Greater(t, capturedSize, 0, "Size should be computed from body when size_bytes is 0")
}

func TestResilient_NilForwarder_DisplaysOnly(t *testing.T) {
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		conn.WriteJSON(map[string]any{
			"type":         "webhook",
			"message_id":   "msg_nofwd",
			"content_type": "application/json",
			"headers":      map[string]string{"x-test": "value"},
			"body":         map[string]any{"display": "only"},
			"size_bytes":   15,
			"received_at":  "2026-03-21T10:30:00Z",
		})

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient("http://localhost:19999", "hb_live_key")

	// nil forwarder = --no-forward mode
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, discardPrinter())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := rl.Run(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 1, rl.count, "Should have received and counted one message without forwarding")
}

func TestResilient_RetriesWSWhilePolling(t *testing.T) {
	var wsConnected atomic.Bool
	wsAcceptAfter := time.Now().Add(500 * time.Millisecond)

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if time.Now().Before(wsAcceptAfter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		wsConnected.Store(true)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, discardPrinter())
	rl.wsMaxRetries = 1
	rl.wsBaseBackoff = 10 * time.Millisecond
	rl.wsRetryInterval = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	assert.True(t, wsConnected.Load(), "Should have reconnected to WebSocket while polling")
}

func TestResilient_WSReconnectStopsPolling(t *testing.T) {
	var wsConnectedAt atomic.Int64
	wsAcceptAfter := time.Now().Add(300 * time.Millisecond)

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if time.Now().Before(wsAcceptAfter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		wsConnectedAt.Store(time.Now().UnixMilli())
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	var pollAfterWS atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wsConnectedAt.Load() > 0 {
			pollAfterWS.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, discardPrinter())
	rl.wsMaxRetries = 1
	rl.wsBaseBackoff = 10 * time.Millisecond
	rl.wsRetryInterval = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	assert.True(t, wsConnectedAt.Load() > 0, "Should have reconnected via WebSocket")
	assert.LessOrEqual(t, int(pollAfterWS.Load()), 1, "Polling should stop after WebSocket reconnects (at most 1 in-flight request)")
}

func TestResilient_ForwardsWebSocketMessages(t *testing.T) {
	var receivedBody []byte
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		receivedBody = body[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		conn.WriteJSON(map[string]any{
			"type":         "webhook",
			"message_id":   "msg_fwd_ws",
			"content_type": "application/json",
			"headers":      map[string]string{},
			"body":         map[string]any{"via": "websocket"},
			"size_bytes":   25,
			"received_at":  "2026-03-21T10:30:00Z",
		})

		// Keep alive until client disconnects
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient("http://localhost:19999", "hb_live_key") // won't be used
	fwd := forwarder.New(localServer.URL)

	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, fwd, false, discardPrinter())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	require.NotEmpty(t, receivedBody)
	assert.Contains(t, string(receivedBody), "websocket")
}

func TestResilient_HandleWSMessage_JSONMode_EmitsOneWebhookLine(t *testing.T) {
	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	rl := &ResilientListener{verbose: true, printer: printer}

	msg := &api.ListenMessage{
		MessageID:   "msg_ws_json",
		ContentType: "application/json",
		Headers:     map[string]string{"x-test": "value"},
		Body:        json.RawMessage(`{"secret":"payload"}`),
		SizeBytes:   42,
		ReceivedAt:  "2026-08-14T10:00:00Z",
	}
	rl.handleWSMessage(&WSClient{}, msg)

	assert.Empty(t, stderr.String())

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 1)

	var ev map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &ev))
	assert.Equal(t, "webhook", ev["event"])
	assert.Equal(t, "msg_ws_json", ev["id"])
	assert.Equal(t, false, ev["forwarded"])

	combined := stdout.String() + stderr.String()
	assert.NotContains(t, combined, "secret")
	assert.NotContains(t, combined, "x-test")
	assert.NotContains(t, combined, "payload")
}

func TestResilient_HandleWSMessage_NormalMode_EventGoesToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, false)
	rl := &ResilientListener{verbose: true, printer: printer}

	msg := &api.ListenMessage{
		ContentType: "application/json",
		Headers:     map[string]string{"x-test": "value"},
		Body:        json.RawMessage(`{"secret":"payload"}`),
		SizeBytes:   42,
	}
	rl.handleWSMessage(&WSClient{}, msg)

	assert.Empty(t, stderr.String())
	out := stdout.String()
	assert.Contains(t, out, "POST")
	assert.Contains(t, out, "Headers:")
	assert.Contains(t, out, "x-test: value")
	assert.Contains(t, out, "Body:")
	assert.Contains(t, out, "secret")
}

func TestResilient_HandleWSMessage_JSONMode_WriteFailure_ReturnsError(t *testing.T) {
	printer := output.New(failingWriter{}, failingWriter{}, true)
	rl := &ResilientListener{printer: printer}

	msg := &api.ListenMessage{
		MessageID:   "msg_ws_fail",
		ContentType: "application/json",
		Body:        json.RawMessage(`{}`),
		SizeBytes:   10,
		ReceivedAt:  "2026-08-14T10:00:00Z",
	}

	err := rl.handleWSMessage(&WSClient{}, msg)

	assert.Error(t, err)
}

func TestResilient_RunWebSocket_StopsOnEmitFailure(t *testing.T) {
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		conn.WriteJSON(map[string]any{
			"type":         "webhook",
			"message_id":   "msg_ws_broken_pipe",
			"content_type": "application/json",
			"headers":      map[string]string{},
			"body":         map[string]any{"from": "ws"},
			"size_bytes":   10,
			"received_at":  "2026-03-21T10:30:00Z",
		})

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	ws := NewWSClient(wsURL, "hb_live_key", "ie_1")
	require.NoError(t, ws.Connect())
	defer ws.Close()

	printer := output.New(failingWriter{}, failingWriter{}, true)
	rl := &ResilientListener{printer: printer}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := rl.runWebSocket(ctx, ws)

	assert.Error(t, err)
}

func TestResilient_Run_PureWebSocket_JSONMode_CtxDone_EmitsExactlyOneShutdown(t *testing.T) {
	// runWebSocket's own ctx.Done() branch no longer emits — Run is the
	// session boundary that owns the terminal event now, so this drives
	// the scenario through Run rather than calling runWebSocket directly.
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient("http://localhost:19999", "hb_live_key")

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, printer)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := rl.Run(ctx)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 1, "exactly one terminal event for a pure WebSocket session")

	var ev map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &ev))
	assert.Equal(t, "shutdown", ev["event"])
	assert.Equal(t, float64(0), ev["received"])
}

func TestResilient_Run_JSONMode_WSUnavailable_EmitsErrorEventToStderr(t *testing.T) {
	var apiCallCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCallCount.Add(1)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	rl := NewResilientListener("ws://localhost:19999", "hb_live_key", "ie_1", apiClient, nil, false, printer)
	rl.wsMaxRetries = 1
	rl.wsBaseBackoff = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	require.NotEmpty(t, lines)
	var ev map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &ev))
	assert.Equal(t, "error", ev["event"])
	assert.NotEmpty(t, ev["message"])

	for _, line := range strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var out map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &out))
	}
}

func TestResilient_Run_JSONMode_WSConnected_EmitsStatusEventToStderr(t *testing.T) {
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient("http://localhost:19999", "hb_live_key")

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, printer)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = rl.Run(ctx)

	found := false
	for _, line := range strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &ev))
		if ev["event"] == "status" && ev["message"] == "Connected via WebSocket (real-time)" {
			found = true
		}
	}
	assert.True(t, found, "expected a status event announcing the WebSocket connection")
}

func TestResilient_Run_PureWebSocket_NormalMode_CtxDone_PrintsShutdownOnce(t *testing.T) {
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient("http://localhost:19999", "hb_live_key")

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, false)
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, printer)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := rl.Run(ctx)
	require.NoError(t, err)

	out := stdout.String()
	assert.Equal(t, 1, strings.Count(out, "Shutting down"), "\"Shutting down\" text must appear exactly once")
}

func TestResilient_Run_PurePolling_CtxDone_EmitsExactlyOneShutdown(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	rl := NewResilientListener("ws://localhost:19999", "hb_live_key", "ie_1", apiClient, nil, false, printer)
	rl.wsMaxRetries = 1
	rl.wsBaseBackoff = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := rl.Run(ctx)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 1, "exactly one terminal event for a pure polling session")

	var ev map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &ev))
	assert.Equal(t, "shutdown", ev["event"])
	assert.Equal(t, float64(0), ev["received"])
}

// newPollToWSTransitionServers builds a WebSocket server that refuses
// connections until wsAcceptAfter (then accepts and sends one webhook) and
// an API server that serves one webhook on its first poll and empties out
// after — driving a session that starts on polling, transitions to
// WebSocket mid-stream, and receives a webhook on each transport.
func newPollToWSTransitionServers(t *testing.T, wsAcceptAfter time.Time) (wsURL string, apiServer *httptest.Server) {
	t.Helper()

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if time.Now().Before(wsAcceptAfter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		conn.WriteJSON(map[string]any{
			"type":         "webhook",
			"message_id":   "msg_ws_after_switch",
			"content_type": "application/json",
			"headers":      map[string]string{},
			"body":         map[string]any{"via": "websocket"},
			"size_bytes":   10,
			"received_at":  "2026-03-21T10:30:00Z",
		})

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	t.Cleanup(wsServer.Close)

	var pollServed atomic.Bool
	apiServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pollServed.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"message_id":   "msg_poll_before_switch",
						"content_type": "application/json",
						"headers":      map[string]string{},
						"body":         map[string]any{"via": "poll"},
						"size_bytes":   10,
						"received_at":  "2026-03-21T10:30:00Z",
					},
				},
				"meta": map[string]any{"next_cursor": "msg_poll_before_switch"},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	t.Cleanup(apiServer.Close)

	return "ws" + strings.TrimPrefix(wsServer.URL, "http"), apiServer
}

func TestResilient_Run_PollToWebSocketTransition_EmitsExactlyOneShutdownAsLastLine(t *testing.T) {
	wsURL, apiServer := newPollToWSTransitionServers(t, time.Now().Add(150*time.Millisecond))
	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, printer)
	rl.wsMaxRetries = 1
	rl.wsBaseBackoff = 10 * time.Millisecond
	rl.wsRetryInterval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := rl.Run(ctx)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.NotEmpty(t, lines)

	shutdownCount := 0
	for i, line := range lines {
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &ev))
		if ev["event"] == "shutdown" {
			shutdownCount++
			assert.Equal(t, len(lines)-1, i, "shutdown must be the last line")
		}
	}
	assert.Equal(t, 1, shutdownCount, "exactly one shutdown event across the whole session, not one per transport switch")
}

func TestResilient_Run_PollToWebSocketTransition_ShutdownCountsBothTransports(t *testing.T) {
	wsURL, apiServer := newPollToWSTransitionServers(t, time.Now().Add(150*time.Millisecond))
	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, printer)
	rl.wsMaxRetries = 1
	rl.wsBaseBackoff = 10 * time.Millisecond
	rl.wsRetryInterval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := rl.Run(ctx)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.NotEmpty(t, lines)

	var shutdown map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &shutdown))
	require.Equal(t, "shutdown", shutdown["event"])
	assert.Equal(t, float64(2), shutdown["received"], "must count webhooks from both the polling and WebSocket phases, not just the last transport")
}

func TestResilient_Run_PollToWebSocketTransition_NormalMode_ShutdownTextOnceAtEndWithCorrectTotal(t *testing.T) {
	wsURL, apiServer := newPollToWSTransitionServers(t, time.Now().Add(150*time.Millisecond))
	apiClient := api.NewClient(apiServer.URL, "hb_live_key")

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, false)
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, printer)
	rl.wsMaxRetries = 1
	rl.wsBaseBackoff = 10 * time.Millisecond
	rl.wsRetryInterval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := rl.Run(ctx)
	require.NoError(t, err)

	out := stdout.String()
	assert.Equal(t, 1, strings.Count(out, "Shutting down"), "\"Shutting down\" text must not appear mid-session on the transition, only once at the end")
	trimmed := strings.TrimRight(out, "\n")
	assert.True(t, strings.HasSuffix(trimmed, "Shutting down. 2 webhook(s) received."),
		"shutdown text must be the last thing printed and report the correct cross-transport total, got: %q", out)
}

func TestResilient_Run_ExistingSessionError_NotClobberedByShutdownEmit(t *testing.T) {
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		conn.WriteJSON(map[string]any{
			"type":         "webhook",
			"message_id":   "msg_will_fail_to_emit",
			"content_type": "application/json",
			"headers":      map[string]string{},
			"body":         map[string]any{"from": "ws"},
			"size_bytes":   10,
			"received_at":  "2026-03-21T10:30:00Z",
		})

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	apiClient := api.NewClient("http://localhost:19999", "hb_live_key")

	stdout := &failFirstWriter{}
	var stderr bytes.Buffer
	printer := output.New(stdout, &stderr, true)
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false, printer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := rl.Run(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "first write failed: simulated session error",
		"the original session error must survive even though the later shutdown emit succeeds")
}
