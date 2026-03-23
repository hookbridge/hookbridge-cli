package listener

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hookbridgehq/hookbridge-cli/internal/api"
	"github.com/hookbridgehq/hookbridge-cli/internal/forwarder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoller_PollsAndUpdates_Cursor(t *testing.T) {
	var callCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			// First call: return a message
			assert.Empty(t, r.URL.Query().Get("after"))
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"message_id":   "msg_1",
						"content_type": "application/json",
						"headers":      map[string]string{},
						"body":         map[string]any{"test": true},
						"size_bytes":   10,
						"received_at":  "2026-03-21T10:30:00Z",
					},
				},
				"meta": map[string]any{"next_cursor": "msg_1"},
			})
		} else {
			// Second call: should have cursor from first response
			assert.Equal(t, "msg_1", r.URL.Query().Get("after"))
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []any{},
				"meta": map[string]any{"next_cursor": nil},
			})
		}
	}))
	defer apiServer.Close()

	client := api.NewClient(apiServer.URL, "hb_live_test")
	poller := NewPoller(client, "ie_1", nil, false)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_ = poller.Run(ctx)

	assert.GreaterOrEqual(t, int(callCount.Load()), 2)
	assert.Equal(t, "msg_1", poller.cursor)
}

func TestPoller_ForwardsToLocalServer(t *testing.T) {
	var receivedBody []byte
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = json.Marshal(map[string]any{"received": true})
		// Actually read the body to verify forwarding
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		receivedBody = body[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()

	var callCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"message_id":   "msg_fwd",
						"content_type": "application/json",
						"headers":      map[string]string{"x-test": "hello"},
						"body":         map[string]any{"forwarded": true},
						"size_bytes":   20,
						"received_at":  "2026-03-21T10:30:00Z",
					},
				},
				"meta": map[string]any{"next_cursor": "msg_fwd"},
			})
		} else {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []any{},
				"meta": map[string]any{"next_cursor": nil},
			})
		}
	}))
	defer apiServer.Close()

	client := api.NewClient(apiServer.URL, "hb_live_test")
	fwd := forwarder.New(localServer.URL)
	poller := NewPoller(client, "ie_1", fwd, false)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_ = poller.Run(ctx)

	require.NotEmpty(t, receivedBody)
	assert.Contains(t, string(receivedBody), "forwarded")
}

func TestPoller_VerboseOutput(t *testing.T) {
	// Capture stdout to verify verbose output
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	callCount := atomic.Int32{}
	apiServer := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			wr.WriteHeader(http.StatusOK)
			json.NewEncoder(wr).Encode(map[string]any{
				"data": []map[string]any{
					{
						"message_id":   "msg_verbose",
						"content_type": "application/json",
						"headers":      map[string]string{"x-custom-header": "test-header-value"},
						"body":         map[string]any{"verbose_key": "verbose_val"},
						"size_bytes":   30,
						"received_at":  "2026-03-21T10:30:00Z",
					},
				},
				"meta": map[string]any{"next_cursor": "msg_verbose"},
			})
		} else {
			wr.WriteHeader(http.StatusOK)
			json.NewEncoder(wr).Encode(map[string]any{
				"data": []any{},
				"meta": map[string]any{"next_cursor": nil},
			})
		}
	}))
	defer apiServer.Close()

	client := api.NewClient(apiServer.URL, "hb_live_key")
	poller := NewPoller(client, "ie_1", nil, true) // verbose=true

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_ = poller.Run(ctx)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	assert.Contains(t, output, "Headers:")
	assert.Contains(t, output, "x-custom-header: test-header-value")
	assert.Contains(t, output, "Body:")
	assert.Contains(t, output, "verbose_key")
}

func TestPoller_ContinuesOnPollError(t *testing.T) {
	var callCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	client := api.NewClient(apiServer.URL, "hb_live_test")
	poller := NewPoller(client, "ie_1", nil, false)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	_ = poller.Run(ctx)

	// Should have retried after error
	assert.GreaterOrEqual(t, int(callCount.Load()), 2)
}
