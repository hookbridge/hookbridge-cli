package listener

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func TestWSClient_ConnectsWithAuthAndEndpointID(t *testing.T) {
	var capturedAuth string
	var capturedEndpointID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedEndpointID = r.URL.Query().Get("endpoint_id")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		defer conn.Close()
		// Keep alive until client disconnects
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewWSClient(wsURL, "hb_live_testkey", "ie_abc123")

	err := client.Connect()
	require.NoError(t, err)
	client.Close()

	assert.Equal(t, "Bearer hb_live_testkey", capturedAuth)
	assert.Equal(t, "ie_abc123", capturedEndpointID)
}

func TestWSClient_ReceivesAndDeserializesMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		msg := map[string]any{
			"type":         "webhook",
			"message_id":   "msg_test123",
			"content_type": "application/json",
			"headers":      map[string]string{"x-stripe-signature": "t=1,v1=abc"},
			"body":         map[string]any{"event": "checkout"},
			"received_at":  "2026-03-21T10:30:00Z",
		}
		conn.WriteJSON(msg)

		// Keep alive briefly
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewWSClient(wsURL, "hb_live_key", "ie_1")
	require.NoError(t, client.Connect())
	defer client.Close()

	msg, err := client.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "msg_test123", msg.MessageID)
	assert.Equal(t, "application/json", msg.ContentType)
	assert.Equal(t, "t=1,v1=abc", msg.Headers["x-stripe-signature"])
}

func TestWSClient_SendsDeliveryResult(t *testing.T) {
	var received map[string]any
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		mu.Lock()
		json.Unmarshal(data, &received)
		mu.Unlock()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewWSClient(wsURL, "hb_live_key", "ie_1")
	require.NoError(t, client.Connect())
	defer client.Close()

	err := client.SendDeliveryResult("msg_abc", 200, 42)
	require.NoError(t, err)

	// Give the server time to process
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "delivery_result", received["type"])
	assert.Equal(t, "msg_abc", received["message_id"])
	assert.Equal(t, float64(200), received["status_code"])
	assert.Equal(t, float64(42), received["latency_ms"])
}

func TestWSClient_ReconnectsWithBackoff(t *testing.T) {
	var connectCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connectCount.Add(1)
		if n <= 2 {
			// Reject the first 2 connections
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Accept the 3rd connection
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
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewWSClient(wsURL, "hb_live_key", "ie_1")
	client.baseBackoff = 50 * time.Millisecond // speed up for test

	err := client.ConnectWithRetry(5)
	require.NoError(t, err)
	client.Close()

	assert.Equal(t, int32(3), connectCount.Load())
}

func TestWSClient_FailsAfterMaxRetries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := NewWSClient(wsURL, "hb_live_key", "ie_1")
	client.baseBackoff = 10 * time.Millisecond

	err := client.ConnectWithRetry(3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect after 3 attempts")
}
