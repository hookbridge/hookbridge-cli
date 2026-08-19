package listener

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func discardPrinter() *output.Printer {
	return output.New(io.Discard, io.Discard, false)
}

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
	poller := NewPoller(client, "ie_1", nil, false, discardPrinter())

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
	poller := NewPoller(client, "ie_1", fwd, false, discardPrinter())

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_ = poller.Run(ctx)

	require.NotEmpty(t, receivedBody)
	assert.Contains(t, string(receivedBody), "forwarded")
}

func TestPoller_VerboseOutput(t *testing.T) {
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
	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, false)
	poller := NewPoller(client, "ie_1", nil, true, printer) // verbose=true

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_ = poller.Run(ctx)

	out := stdout.String()

	assert.Contains(t, out, "Headers:")
	assert.Contains(t, out, "x-custom-header: test-header-value")
	assert.Contains(t, out, "Body:")
	assert.Contains(t, out, "verbose_key")
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
	poller := NewPoller(client, "ie_1", nil, false, discardPrinter())

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	_ = poller.Run(ctx)

	// Should have retried after error
	assert.GreaterOrEqual(t, int(callCount.Load()), 2)
}

func TestPoller_HandleMessage_JSONMode_EmitsOneWebhookLine(t *testing.T) {
	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	poller := NewPoller(nil, "ie_1", nil, true, printer)

	msg := api.ListenMessage{
		MessageID:   "msg_json",
		ContentType: "application/json",
		Headers:     map[string]string{"x-test": "value"},
		Body:        json.RawMessage(`{"secret":"payload"}`),
		SizeBytes:   42,
		ReceivedAt:  "2026-08-14T10:00:00Z",
	}
	poller.handleMessage(msg)

	assert.Empty(t, stderr.String())

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 1)

	var ev map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &ev))
	assert.Equal(t, "webhook", ev["event"])
	assert.Equal(t, "msg_json", ev["id"])
	assert.Equal(t, "application/json", ev["content_type"])
	assert.Equal(t, float64(42), ev["size_bytes"])
	assert.Equal(t, false, ev["forwarded"])
	assert.ElementsMatch(t, []string{"event", "id", "content_type", "size_bytes", "received_at", "forwarded"}, jsonKeys(t, ev))

	combined := stdout.String() + stderr.String()
	assert.NotContains(t, combined, "secret")
	assert.NotContains(t, combined, "x-test")
	assert.NotContains(t, combined, "payload")
}

func TestPoller_HandleMessage_NormalMode_EventGoesToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, false)
	poller := NewPoller(nil, "ie_1", nil, true, printer)

	msg := api.ListenMessage{
		ContentType: "application/json",
		Headers:     map[string]string{"x-test": "value"},
		Body:        json.RawMessage(`{"secret":"payload"}`),
		SizeBytes:   42,
	}
	poller.handleMessage(msg)

	assert.Empty(t, stderr.String())
	out := stdout.String()
	assert.Contains(t, out, "POST")
	assert.Contains(t, out, "Headers:")
	assert.Contains(t, out, "x-test: value")
	assert.Contains(t, out, "Body:")
	assert.Contains(t, out, "secret")
}

func TestPoller_Run_JSONMode_CtxDone_DoesNotEmitTerminalEvent(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	client := api.NewClient(apiServer.URL, "hb_live_test")
	poller := NewPoller(client, "ie_1", nil, false, printer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := poller.Run(ctx)

	assert.NoError(t, err)
	assert.Empty(t, stdout.String(), "poller must not emit a terminal event — that is ResilientListener.Run's job, since a polling phase can end mid-session on a transport switch")
	assert.Empty(t, stderr.String())
	assert.Equal(t, 0, poller.Count())
}

func TestPoller_Run_NormalMode_CtxDone_DoesNotPrintShutdownText(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer apiServer.Close()

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, false)
	client := api.NewClient(apiServer.URL, "hb_live_test")
	poller := NewPoller(client, "ie_1", nil, false, printer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := poller.Run(ctx)

	assert.NoError(t, err)
	assert.Empty(t, stderr.String())
	assert.NotContains(t, stdout.String(), "Shutting down")
}

func TestPoller_Run_JSONMode_PollErrorEmitsNDJSONToStderr(t *testing.T) {
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

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	client := api.NewClient(apiServer.URL, "hb_live_test")
	poller := NewPoller(client, "ie_1", nil, false, printer)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	err := poller.Run(ctx)
	assert.NoError(t, err)

	assert.Empty(t, stdout.String(), "poller no longer emits a terminal event")

	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	require.NotEmpty(t, lines)

	var errEv map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &errEv))
	assert.Equal(t, "error", errEv["event"])
	assert.NotEmpty(t, errEv["message"])
	assert.ElementsMatch(t, []string{"event", "message"}, jsonKeys(t, errEv))
}

func TestPoller_HandleMessage_JSONMode_WriteFailure_ReturnsError(t *testing.T) {
	printer := output.New(failingWriter{}, failingWriter{}, true)
	poller := NewPoller(nil, "ie_1", nil, false, printer)

	msg := api.ListenMessage{
		MessageID:   "msg_fail",
		ContentType: "application/json",
		Body:        json.RawMessage(`{}`),
		SizeBytes:   10,
		ReceivedAt:  "2026-08-14T10:00:00Z",
	}

	err := poller.handleMessage(msg)

	assert.Error(t, err)
}

func TestPoller_Run_JSONMode_StopsPollingOnEmitFailure(t *testing.T) {
	var callCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"message_id":   "msg_broken_pipe",
					"content_type": "application/json",
					"headers":      map[string]string{},
					"body":         map[string]any{"n": 1},
					"size_bytes":   10,
					"received_at":  "2026-03-21T10:30:00Z",
				},
			},
			"meta": map[string]any{"next_cursor": "msg_broken_pipe"},
		})
	}))
	defer apiServer.Close()

	printer := output.New(failingWriter{}, failingWriter{}, true)
	client := api.NewClient(apiServer.URL, "hb_live_test")
	poller := NewPoller(client, "ie_1", nil, false, printer)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	err := poller.Run(ctx)

	assert.Error(t, err)
	assert.Equal(t, int32(1), callCount.Load(), "poller must stop after the first emit failure instead of continuing to poll")
}

func TestPoller_Run_JSONMode_MultipleMessages_OneStdoutLineEach(t *testing.T) {
	var callCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"message_id":   "msg_a",
						"content_type": "application/json",
						"headers":      map[string]string{},
						"body":         map[string]any{"n": 1},
						"size_bytes":   10,
						"received_at":  "2026-03-21T10:30:00Z",
					},
					{
						"message_id":   "msg_b",
						"content_type": "application/json",
						"headers":      map[string]string{},
						"body":         map[string]any{"n": 2},
						"size_bytes":   11,
						"received_at":  "2026-03-21T10:30:01Z",
					},
				},
				"meta": map[string]any{"next_cursor": "msg_b"},
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

	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true)
	client := api.NewClient(apiServer.URL, "hb_live_test")
	poller := NewPoller(client, "ie_1", nil, false, printer)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	err := poller.Run(ctx)
	assert.NoError(t, err)

	assert.Empty(t, stderr.String())

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 2) // 2 webhook events, no terminal event from the poller anymore

	for _, line := range lines {
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &ev))
	}

	var first, second map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	assert.Equal(t, "msg_a", first["id"])
	assert.Equal(t, "msg_b", second["id"])

	assert.Equal(t, 2, poller.Count())
}
