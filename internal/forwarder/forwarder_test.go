package forwarder

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	assert.GreaterOrEqual(t, result.LatencyMs, int64(0))
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

func TestForward_ParseFailureDoesNotLeakCredentials(t *testing.T) {
	fwd := New("http://token:hunter2@exa mple.com/hook")
	result := fwd.Forward(json.RawMessage(`{}`), "application/json", nil)

	require.NotEmpty(t, result.Error)
	assert.NotContains(t, result.Error, "hunter2")
	assert.NotContains(t, result.Error, "token")
}

func TestForward_TransportFailureDoesNotLeakUsername(t *testing.T) {
	fwd := New("http://token:hunter2@127.0.0.1:1/hook")
	result := fwd.Forward(json.RawMessage(`{}`), "application/json", nil)

	require.NotEmpty(t, result.Error)
	assert.NotContains(t, result.Error, "hunter2")
	assert.NotContains(t, result.Error, "token")
}

// TestForward_NoCredentialEverSurvives mirrors the shape of
// TestURL_NoCredentialEverSurvives in internal/redact/redact_test.go: a
// cross-product of schemes, usernames and passwords over a target that is
// guaranteed to fail (a refused port, or a host that fails to parse), to
// catch both leak paths in one sweep — http.NewRequest's verbatim parse
// error and http.Client.Do's only-the-password-stripped transport error.
func TestForward_NoCredentialEverSurvives(t *testing.T) {
	schemes := []string{"http://", "https://"}
	usernames := []string{"token", "us/er", ""}
	passwords := []string{"hunter2", "pa/ss", ""}
	hosts := []string{"127.0.0.1:1/hook", "exa mple.com/hook"}

	checked := 0
	leaked := 0
	for _, scheme := range schemes {
		for _, user := range usernames {
			for _, pass := range passwords {
				if user == "" && pass == "" {
					continue
				}
				for _, host := range hosts {
					target := scheme + user + ":" + pass + "@" + host
					fwd := New(target)
					result := fwd.Forward(json.RawMessage(`{}`), "application/json", nil)

					require.NotEmpty(t, result.Error, "target %q must fail", target)
					checked++

					leakedThisCase := false
					if user != "" && strings.Contains(result.Error, user) {
						leakedThisCase = true
					}
					if pass != "" && strings.Contains(result.Error, pass) {
						leakedThisCase = true
					}
					if leakedThisCase {
						leaked++
						t.Errorf("target %q leaked credentials into error %q", target, result.Error)
					}
				}
			}
		}
	}
	t.Logf("checked %d credential-bearing target/error cases, %d leaked", checked, leaked)
}

func TestForward_OrdinaryErrorsUnaffectedBySanitization(t *testing.T) {
	fwd := New("http://127.0.0.1:1/hook")
	result := fwd.Forward(json.RawMessage(`{}`), "application/json", nil)

	require.NotEmpty(t, result.Error)
	assert.Contains(t, result.Error, "connection refused or timeout")
}

type emptyMessageError struct{}

func (emptyMessageError) Error() string { return "" }

func TestSanitizeErr_DegenerateURLErrorShapes(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"nil error", nil},
		{"empty op, nil err", &url.Error{Op: "", URL: "http://example.com", Err: nil}},
		{"non-empty op, nil err", &url.Error{Op: "get", URL: "http://example.com", Err: nil}},
		{"empty op, non-nil err", &url.Error{Op: "", URL: "http://example.com", Err: errors.New("boom")}},
		{"empty op, wrapped err with empty message", &url.Error{Op: "", URL: "http://example.com", Err: emptyMessageError{}}},
		{"non-url.Error with empty message", emptyMessageError{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			assert.NotPanics(t, func() {
				got = sanitizeErr(tt.err)
			})
			assert.NotEmpty(t, got)
		})
	}
}

func TestForward_MalformedURLHintGating(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantHint bool
	}{
		{"slash in userinfo defeats authority boundary", "http://us/er:hunter2@127.0.0.1:1/hook", true},
		{"recognized userinfo, error already makes sense", "http://user:pass@127.0.0.1:1/hook", false},
		{"at sign only in query string", "http://127.0.0.1:1/hook?email=a@b.com", false},
		{"no at sign at all", "http://localhost:3000", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fwd := New(tt.target)
			got := fwd.malformedURLHint()
			if tt.wantHint {
				assert.Equal(t, malformedURLHintSuffix, got)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}
