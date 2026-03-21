package forwarder

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForwarder_ForwardsBodyAndHeaders(t *testing.T) {
	var capturedBody []byte
	var capturedHeaders http.Header
	var capturedMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedHeaders = r.Header.Clone()
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	fwd := New(server.URL)
	result := fwd.Forward(
		json.RawMessage(`{"event":"checkout"}`),
		"application/json",
		map[string]string{
			"x-stripe-signature": "t=123,v1=abc",
		},
	)

	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Equal(t, http.MethodPost, capturedMethod)
	assert.JSONEq(t, `{"event":"checkout"}`, string(capturedBody))
	assert.Equal(t, "application/json", capturedHeaders.Get("Content-Type"))
	assert.Equal(t, "t=123,v1=abc", capturedHeaders.Get("X-Stripe-Signature"))
	assert.Greater(t, result.LatencyMs, int64(0))
	assert.Empty(t, result.Error)
}

func TestForwarder_ReturnsStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	fwd := New(server.URL)
	result := fwd.Forward(json.RawMessage(`{}`), "application/json", nil)

	assert.Equal(t, http.StatusInternalServerError, result.StatusCode)
}

func TestForwarder_ConnectionRefused(t *testing.T) {
	fwd := New("http://localhost:19999") // port nobody is listening on
	result := fwd.Forward(json.RawMessage(`{}`), "application/json", nil)

	assert.Equal(t, 0, result.StatusCode)
	require.NotEmpty(t, result.Error)
	assert.Contains(t, result.Error, "connection refused")
}

func TestForwarder_Timeout(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until test completes — the forwarder timeout will fire first
		<-done
	}))
	defer func() {
		close(done)
		server.Close()
	}()

	fwd := NewWithTimeout(server.URL, 1) // 1ms timeout
	result := fwd.Forward(json.RawMessage(`{}`), "application/json", nil)

	assert.Equal(t, 0, result.StatusCode)
	require.NotEmpty(t, result.Error)
}
