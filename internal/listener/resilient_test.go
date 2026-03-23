package listener

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hookbridgehq/hookbridge-cli/internal/api"
	"github.com/hookbridgehq/hookbridge-cli/internal/forwarder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false)

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

	rl := NewResilientListener("ws://localhost:19999", "hb_live_key", "ie_1", apiClient, nil, false)
	rl.wsMaxRetries = 2
	rl.wsBaseBackoff = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	// Polling should have been called since WS failed
	assert.Greater(t, int(apiCallCount.Load()), 0, "Should have fallen back to polling")
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
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false)
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
	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false)

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

	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false)
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

	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, nil, false)
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

	rl := NewResilientListener(wsURL, "hb_live_key", "ie_1", apiClient, fwd, false)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = rl.Run(ctx)

	require.NotEmpty(t, receivedBody)
	assert.Contains(t, string(receivedBody), "websocket")
}
