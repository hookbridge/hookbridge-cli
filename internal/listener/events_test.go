package listener

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/hookbridge/hookbridge-cli/internal/api"
	"github.com/hookbridge/hookbridge-cli/internal/forwarder"
	"github.com/hookbridge/hookbridge-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingWriter simulates a broken stdout (e.g. EPIPE from a downstream
// consumer that exited early) so tests can verify emit errors surface
// instead of being swallowed.
type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) {
	return 0, errors.New("write failed: broken pipe")
}

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestBuildWebhookEvent_InspectMode_NoForwardKeys(t *testing.T) {
	msg := api.ListenMessage{
		MessageID:   "msg_1",
		ContentType: "application/json",
		ReceivedAt:  "2026-08-14T10:00:00Z",
	}

	ev := buildWebhookEvent(msg, 42, nil)

	assert.Equal(t, "webhook", ev.Event)
	assert.Equal(t, "msg_1", ev.ID)
	assert.Equal(t, "application/json", ev.ContentType)
	assert.Equal(t, 42, ev.SizeBytes)
	assert.Equal(t, "2026-08-14T10:00:00Z", ev.ReceivedAt)
	assert.False(t, ev.Forwarded)

	assert.ElementsMatch(t, []string{"event", "id", "content_type", "size_bytes", "received_at", "forwarded"}, jsonKeys(t, ev))
}

func TestBuildWebhookEvent_SuccessfulForward_IncludesStatusAndLatency(t *testing.T) {
	msg := api.ListenMessage{MessageID: "msg_2", ContentType: "application/json", ReceivedAt: "2026-08-14T10:00:00Z"}
	result := &forwarder.Result{StatusCode: 200, LatencyMs: 12}

	ev := buildWebhookEvent(msg, 42, result)

	assert.True(t, ev.Forwarded)
	assert.Equal(t, 200, ev.StatusCode)
	require.NotNil(t, ev.LatencyMs)
	assert.Equal(t, int64(12), *ev.LatencyMs)
	assert.Empty(t, ev.Error)

	assert.ElementsMatch(t,
		[]string{"event", "id", "content_type", "size_bytes", "received_at", "forwarded", "status_code", "latency_ms"},
		jsonKeys(t, ev))
}

func TestBuildWebhookEvent_SuccessfulForward_ZeroLatency_IncludesLatencyMsKey(t *testing.T) {
	msg := api.ListenMessage{MessageID: "msg_zero", ContentType: "application/json", ReceivedAt: "2026-08-14T10:00:00Z"}
	result := &forwarder.Result{StatusCode: 200, LatencyMs: 0}

	ev := buildWebhookEvent(msg, 42, result)

	require.NotNil(t, ev.LatencyMs)
	assert.Equal(t, int64(0), *ev.LatencyMs)

	b, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"latency_ms":0`)

	assert.ElementsMatch(t,
		[]string{"event", "id", "content_type", "size_bytes", "received_at", "forwarded", "status_code", "latency_ms"},
		jsonKeys(t, ev))
}

func TestBuildWebhookEvent_FailedForward_IncludesErrorOnly(t *testing.T) {
	msg := api.ListenMessage{MessageID: "msg_3", ContentType: "application/json", ReceivedAt: "2026-08-14T10:00:00Z"}
	result := &forwarder.Result{Error: "connection refused", LatencyMs: 5}

	ev := buildWebhookEvent(msg, 42, result)

	assert.False(t, ev.Forwarded)
	assert.Equal(t, "connection refused", ev.Error)
	assert.Zero(t, ev.StatusCode)

	assert.ElementsMatch(t,
		[]string{"event", "id", "content_type", "size_bytes", "received_at", "forwarded", "error"},
		jsonKeys(t, ev))
}

func TestShutdownEvent_KeySet(t *testing.T) {
	ev := shutdownEvent{Event: "shutdown", Received: 3}
	assert.ElementsMatch(t, []string{"event", "received"}, jsonKeys(t, ev))
}

func TestDiagnosticEvent_KeySet(t *testing.T) {
	ev := diagnosticEvent{Event: "error", Message: "boom"}
	assert.ElementsMatch(t, []string{"event", "message"}, jsonKeys(t, ev))
}

func TestEmitWebhookEvent_WriteFailure_ReturnsError(t *testing.T) {
	printer := output.New(failingWriter{}, failingWriter{}, true)
	msg := api.ListenMessage{MessageID: "msg_fail", ContentType: "application/json", ReceivedAt: "2026-08-14T10:00:00Z"}

	err := emitWebhookEvent(printer, msg, 42, nil)

	assert.Error(t, err)
}

func TestEmitShutdown_JSONMode_WriteFailure_ReturnsError(t *testing.T) {
	printer := output.New(failingWriter{}, failingWriter{}, true)

	err := emitShutdown(printer, 3)

	assert.Error(t, err)
}
