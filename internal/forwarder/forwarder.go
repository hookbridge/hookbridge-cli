package forwarder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeoutSeconds = 30

const malformedURLHintSuffix = " (the --forward URL may be malformed — check that special characters are percent-encoded)"

const unknownForwardingError = "unknown forwarding error"

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
		return Result{Error: fmt.Sprintf("could not create request: %s%s", sanitizeErr(err), f.malformedURLHint())}
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
			Error:     fmt.Sprintf("connection refused or timeout: %s%s", sanitizeErr(err), f.malformedURLHint()),
		}
	}
	defer resp.Body.Close()

	return Result{
		StatusCode: resp.StatusCode,
		LatencyMs:  latency,
	}
}

// malformedURLHint appends a static nudge toward percent-encoding when the
// target's "@" was very likely not recognised by net/url as userinfo: an
// unescaped "/" in userinfo (e.g. a username with a slash) makes net/url
// misread the host, producing a DNS-failure-shaped error — like "lookup us:
// no such host" — that gives no indication the real problem is the
// --forward URL itself. It fires only when the target contains "@", url.Parse
// did not resolve a User component, and that "@" sits before any "?"/"#" (so
// an "@" that is legitimately part of a query string, e.g. an email address,
// does not trigger it). A wrong verdict here only produces an odd hint, never
// a leak — the text itself never contains any part of the URL.
func (f *Forwarder) malformedURLHint() string {
	if !strings.Contains(f.targetURL, "@") {
		return ""
	}

	if u, err := url.Parse(f.targetURL); err == nil && u.User != nil {
		return ""
	}

	lastAt := strings.LastIndex(f.targetURL, "@")
	if boundary := strings.IndexAny(f.targetURL, "?#"); boundary != -1 && lastAt > boundary {
		return ""
	}

	return malformedURLHintSuffix
}

// sanitizeErr renders a forwarding error without the target URL. Both
// http.NewRequest and http.Client.Do return *url.Error, which embeds the
// target: the parse path verbatim, credentials and all, and the transport
// path with only the password stripped by net/http, leaving a username that
// is often itself a token. Dropping the URL field entirely is the only
// version of this that does not depend on guessing which part is secret.
//
// *url.Error is a struct, not validated at construction, so errors.As can
// hand back one with a zero Op and a nil Err — never observed from net/http
// itself, but nothing in the type guarantees it, and every branch here must
// stay total: a panic mid-Forward would kill hb listen on the first failed
// forward, worse than the leak this function exists to close. The function
// also never returns "", even for a nil err or a wrapped error whose own
// Error() is "": an empty Result.Error would make buildWebhookEvent report
// the forward as successful.
func sanitizeErr(err error) string {
	if err == nil {
		return unknownForwardingError
	}

	var ue *url.Error
	if errors.As(err, &ue) {
		msg := ""
		switch {
		case ue.Op != "" && ue.Err != nil:
			msg = ue.Op + ": " + ue.Err.Error()
		case ue.Op != "":
			msg = ue.Op
		case ue.Err != nil:
			msg = ue.Err.Error()
		}
		if msg == "" {
			return unknownForwardingError
		}
		return msg
	}

	if msg := err.Error(); msg != "" {
		return msg
	}
	return unknownForwardingError
}
