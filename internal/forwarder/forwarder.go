package forwarder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultTimeoutSeconds = 30

// Result holds the outcome of forwarding a webhook to the local server.
type Result struct {
	StatusCode int
	LatencyMs  int64
	Error      string
}

// Forwarder sends webhooks to a local HTTP server.
type Forwarder struct {
	targetURL  string
	httpClient *http.Client
}

// New creates a forwarder with the default 30s timeout.
func New(targetURL string) *Forwarder {
	return NewWithTimeout(targetURL, defaultTimeoutSeconds*1000)
}

// NewWithTimeout creates a forwarder with a custom timeout in milliseconds.
func NewWithTimeout(targetURL string, timeoutMs int) *Forwarder {
	return &Forwarder{
		targetURL: targetURL,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutMs) * time.Millisecond,
		},
	}
}

// Forward sends a webhook body and headers to the target URL via POST.
func (f *Forwarder) Forward(body json.RawMessage, contentType string, headers map[string]string) Result {
	start := time.Now()

	req, err := http.NewRequest(http.MethodPost, f.targetURL, bytes.NewReader(body))
	if err != nil {
		return Result{Error: fmt.Sprintf("could not create request: %v", err)}
	}

	// Set webhook headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Content-Type from the webhook takes precedence
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := f.httpClient.Do(req)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return Result{
			LatencyMs: latency,
			Error:     fmt.Sprintf("connection refused or timeout: %v", err),
		}
	}
	defer resp.Body.Close()

	return Result{
		StatusCode: resp.StatusCode,
		LatencyMs:  latency,
	}
}
