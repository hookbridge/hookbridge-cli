package listener

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hookbridge/hookbridge-cli/internal/api"
	"github.com/hookbridge/hookbridge-cli/internal/forwarder"
	"github.com/hookbridge/hookbridge-cli/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderTestMsg() api.ListenMessage {
	return api.ListenMessage{
		MessageID:   "msg_render",
		ContentType: "application/json",
		Headers:     map[string]string{"x-test": "value"},
		Body:        json.RawMessage(`{"hello":"world"}`),
		SizeBytes:   42,
		ReceivedAt:  "2026-08-14T10:00:00Z",
	}
}

func stripANSI(s string) string {
	for _, c := range []string{colorReset, colorGreen, colorRed, colorYellow} {
		s = strings.ReplaceAll(s, c, "")
	}
	return s
}

func TestRenderWebhook_ColorOff_NoEscapeCodes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := output.New(&stdout, &stderr, false)
	result := &forwarder.Result{StatusCode: 200, LatencyMs: 12}

	renderWebhook(p, renderTestMsg(), 42, result, true)

	combined := stdout.String() + stderr.String()
	assert.NotContains(t, combined, "\033[")
}

func TestRenderWebhook_ColorOn_200IsGreen(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := output.New(&stdout, &stderr, false).WithColor(true)
	result := &forwarder.Result{StatusCode: 200, LatencyMs: 5}

	renderWebhook(p, renderTestMsg(), 42, result, false)

	assert.Contains(t, stdout.String(), colorGreen+"200"+colorReset)
}

func TestRenderWebhook_ColorOn_404IsRed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := output.New(&stdout, &stderr, false).WithColor(true)
	result := &forwarder.Result{StatusCode: 404, LatencyMs: 5}

	renderWebhook(p, renderTestMsg(), 42, result, false)

	assert.Contains(t, stdout.String(), colorRed+"404"+colorReset)
}

func TestRenderWebhook_ColorOn_500IsRed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := output.New(&stdout, &stderr, false).WithColor(true)
	result := &forwarder.Result{StatusCode: 500, LatencyMs: 5}

	renderWebhook(p, renderTestMsg(), 42, result, false)

	assert.Contains(t, stdout.String(), colorRed+"500"+colorReset)
}

func TestRenderWebhook_ColorOn_ErrIsYellow(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := output.New(&stdout, &stderr, false).WithColor(true)
	result := &forwarder.Result{Error: "connection refused"}

	renderWebhook(p, renderTestMsg(), 42, result, false)

	assert.Contains(t, stdout.String(), colorYellow+"ERR"+colorReset)
}

func TestRenderWebhook_PlainTextIdenticalRegardlessOfColor(t *testing.T) {
	result := &forwarder.Result{StatusCode: 200, LatencyMs: 5}

	var onOut, onErr bytes.Buffer
	renderWebhook(output.New(&onOut, &onErr, false).WithColor(true), renderTestMsg(), 42, result, true)

	var offOut, offErr bytes.Buffer
	renderWebhook(output.New(&offOut, &offErr, false), renderTestMsg(), 42, result, true)

	assert.Equal(t, offOut.String(), stripANSI(onOut.String()))
	assert.Equal(t, offErr.String(), stripANSI(onErr.String()))
}

func TestPoller_HandleMessage_JSONMode_WithColorSet_NoEscapeCodes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true).WithColor(true)
	poller := NewPoller(nil, "ie_1", nil, false, printer)

	msg := renderTestMsg()
	err := poller.handleMessage(msg)
	require.NoError(t, err)

	assert.NotContains(t, stdout.String(), "\033[")
	var ev map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimRight(stdout.Bytes(), "\n"), &ev))
	assert.Equal(t, "webhook", ev["event"])
}

func TestResilient_HandleWSMessage_JSONMode_WithColorSet_NoEscapeCodes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	printer := output.New(&stdout, &stderr, true).WithColor(true)
	rl := &ResilientListener{printer: printer}

	msg := renderTestMsg()
	err := rl.handleWSMessage(&WSClient{}, &msg)
	require.NoError(t, err)

	assert.NotContains(t, stdout.String(), "\033[")
	var ev map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimRight(stdout.Bytes(), "\n"), &ev))
	assert.Equal(t, "webhook", ev["event"])
}

var (
	renderTimestamp = regexp.MustCompile(`\d{2}:\d{2}:\d{2}`)
	renderLatency   = regexp.MustCompile(`\d+ms`)
)

func TestPollerAndResilient_RenderWebhookIdentically(t *testing.T) {
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()

	fwd := forwarder.New(localServer.URL)
	msg := renderTestMsg()

	var pollerOut, pollerErr bytes.Buffer
	pollerPrinter := output.New(&pollerOut, &pollerErr, false).WithColor(true)
	poller := NewPoller(nil, "ie_1", fwd, true, pollerPrinter)
	require.NoError(t, poller.handleMessage(msg))

	var wsOut, wsErr bytes.Buffer
	wsPrinter := output.New(&wsOut, &wsErr, false).WithColor(true)
	rl := &ResilientListener{forwarder: fwd, verbose: true, printer: wsPrinter}
	require.NoError(t, rl.handleWSMessage(&WSClient{}, &msg))

	normalize := func(s string) string {
		s = renderTimestamp.ReplaceAllString(s, "TS")
		return renderLatency.ReplaceAllString(s, "Nms")
	}
	assert.Equal(t, normalize(pollerOut.String()), normalize(wsOut.String()))
	assert.Empty(t, pollerErr.String())
	assert.Empty(t, wsErr.String())
}
