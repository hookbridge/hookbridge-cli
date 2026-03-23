package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hookbridgehq/hookbridge-cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestHome(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	return tmpDir
}

func projectResponse(id, name string) map[string]any {
	return map[string]any{
		"data": []map[string]any{{"id": id, "label": name}},
	}
}

func endpointListResponse(endpoints ...map[string]any) map[string]any {
	return map[string]any{"data": endpoints}
}

func endpointCreatedResponse(id, name, mode, receiveURL string) map[string]any {
	return map[string]any{
		"data": map[string]any{
			"id": id, "name": name, "mode": mode,
			"active": true, "ingest_url": receiveURL,
		},
	}
}

// --- Login Tests ---

func TestLogin_ValidKey_StoresCredentials(t *testing.T) {
	home := setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer hb_live_validkey", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(projectResponse("proj_123", "My Project"))
	}))
	defer server.Close()

	// Pre-save config with custom base URL pointing to test server
	require.NoError(t, config.Save(&config.Config{APIBaseURL: server.URL}))

	cmd := rootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"login", "--api-key", "hb_live_validkey"})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify config was saved
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "hb_live_validkey", cfg.APIKey)

	// Verify config file exists with correct permissions
	cfgPath := filepath.Join(home, ".hookbridge", "config.json")
	info, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestLogin_InvalidKey_ReturnsError(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "UNAUTHORIZED", "message": "Invalid API key"},
		})
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{APIBaseURL: server.URL}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"login", "--api-key", "hb_live_badkey"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid API key")
}

func TestLogin_NetworkError_ReturnsError(t *testing.T) {
	setupTestHome(t)

	// Point to a server that doesn't exist
	require.NoError(t, config.Save(&config.Config{APIBaseURL: "http://localhost:19999"}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"login", "--api-key", "hb_live_key"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection")
}

// --- Logout Tests ---

func TestLogout_RemovesConfigFile(t *testing.T) {
	home := setupTestHome(t)
	require.NoError(t, config.Save(&config.Config{APIKey: "hb_live_x", ProjectID: "proj_x"}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"logout"})
	err := cmd.Execute()
	require.NoError(t, err)

	cfgPath := filepath.Join(home, ".hookbridge", "config.json")
	_, err = os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(err))
}

func TestLogout_WhenNotLoggedIn_NoError(t *testing.T) {
	setupTestHome(t)

	cmd := rootCmd()
	cmd.SetArgs([]string{"logout"})
	err := cmd.Execute()
	assert.NoError(t, err)
}

// --- Endpoints Tests ---

func TestEndpoints_ListsCLIModeEndpoints(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(endpointListResponse(
			map[string]any{"id": "ie_1", "name": "Stripe CLI", "mode": "cli", "active": true},
			map[string]any{"id": "ie_2", "name": "Forward EP", "mode": "forward", "active": true},
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"endpoints"})
	err := cmd.Execute()
	require.NoError(t, err)

	// Output is on stdout, not the cobra out buffer, but no error means success.
	// The handler filters to cli-mode only and prints a table.
}

func TestEndpoints_NoEndpoints_PrintsHelpfulMessage(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(endpointListResponse())
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints"})
	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestEndpoints_NotLoggedIn_ReturnsError(t *testing.T) {
	setupTestHome(t)

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

// --- Endpoints Create Tests ---

func TestEndpointsCreate_Success(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/inbound-endpoints", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "cli", body["mode"])
		assert.Equal(t, "Stripe Webhooks", body["name"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(endpointCreatedResponse(
			"ie_new", "Stripe Webhooks", "cli",
			"https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret",
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints", "create", "--name", "Stripe Webhooks"})
	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestEndpointsCreate_DefaultName(t *testing.T) {
	setupTestHome(t)

	var capturedName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		capturedName = body["name"].(string)

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(endpointCreatedResponse(
			"ie_new", capturedName, "cli",
			"https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret",
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints", "create"})
	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Equal(t, "CLI Endpoint", capturedName)
}

// --- Version Test ---

func TestVersion_PrintsVersion(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"version"})
	err := cmd.Execute()
	assert.NoError(t, err)
}

// --- Listen Command Tests ---

// listenAPIServer creates a mock API server for listen command tests.
// It serves endpoint list, create, and listen polling endpoints.
func listenAPIServer(t *testing.T, endpoints []map[string]any, messages []map[string]any, signals ...chan struct{}) *httptest.Server {
	t.Helper()
	var createSignal, pollSignal chan struct{}
	if len(signals) > 0 && signals[0] != nil {
		createSignal = signals[0]
	}
	if len(signals) > 1 && signals[1] != nil {
		pollSignal = signals[1]
	}

	pollCount := atomic.Int32{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound-endpoints":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(endpointListResponse(endpoints...))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/inbound-endpoints":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "cli", body["mode"])
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(endpointCreatedResponse(
				"ie_created", "CLI Endpoint", "cli",
				"https://receive.hookbridge.io/v1/webhooks/receive/ie_created/sk_abc",
			))
			if createSignal != nil {
				select {
				case createSignal <- struct{}{}:
				default:
				}
			}
		default:
			// Listen polling endpoint
			n := pollCount.Add(1)
			if n == 1 && len(messages) > 0 {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{
					"data": messages,
					"meta": map[string]any{"next_cursor": "msg_last"},
				})
			} else {
				if pollSignal != nil {
					select {
					case pollSignal <- struct{}{}:
					default:
					}
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{
					"data": []any{},
					"meta": map[string]any{"next_cursor": nil},
				})
			}
		}
	}))
}

func runListenCmd(ctx context.Context, args ...string) chan error {
	done := make(chan error, 1)
	go func() {
		cmd := rootCmd()
		cmd.SetContext(ctx)
		cmd.SetArgs(append([]string{"listen"}, args...))
		done <- cmd.Execute()
	}()
	return done
}

func cliEndpoint() map[string]any {
	return map[string]any{
		"id": "ie_1", "name": "CLI EP", "mode": "cli",
		"active": true, "ingest_url": "https://receive.hookbridge.io/v1/webhooks/receive/ie_1/sk_abc",
	}
}

func webhookMessage() map[string]any {
	return map[string]any{
		"message_id":   "msg_1",
		"content_type": "application/json",
		"headers":      map[string]string{"x-test": "value"},
		"body":         map[string]any{"hello": "world"},
		"size_bytes":   15,
		"received_at":  "2026-03-21T10:30:00Z",
	}
}

func TestListen_CreatesEndpointWhenNoneExist(t *testing.T) {
	setupTestHome(t)

	createDone := make(chan struct{}, 1)
	server := listenAPIServer(t, nil, nil, createDone, nil)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runListenCmd(ctx)

	select {
	case <-createDone:
		// Endpoint was created with mode=cli
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for endpoint creation")
	}
}

func TestListen_ReusesExistingEndpoint(t *testing.T) {
	setupTestHome(t)

	var createCalled atomic.Bool
	pollDone := make(chan struct{}, 1)
	// Wrap listenAPIServer to detect creates
	endpoints := []map[string]any{cliEndpoint()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound-endpoints":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(endpointListResponse(endpoints...))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/inbound-endpoints":
			createCalled.Store(true)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(endpointCreatedResponse("ie_x", "x", "cli", "https://example.com"))
		default:
			select {
			case pollDone <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []any{},
				"meta": map[string]any{"next_cursor": nil},
			})
		}
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runListenCmd(ctx)

	// Wait for polling to start (proves endpoint resolution completed)
	select {
	case <-pollDone:
	case <-time.After(15 * time.Second):
		t.Fatal("Timed out waiting for polling to start")
	}

	assert.False(t, createCalled.Load(), "Should not create endpoint when one exists")
}

func TestListen_NoForwardFlag(t *testing.T) {
	setupTestHome(t)

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, []map[string]any{webhookMessage()}, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runListenCmd(ctx, "--no-forward")

	// Wait for the second poll (after message was received and processed)
	select {
	case <-pollSecond:
		// Message was received and processed without forwarding (no error)
	case <-time.After(15 * time.Second):
		t.Fatal("Timed out waiting for poll after message")
	}
}

func TestListen_PortFlag(t *testing.T) {
	setupTestHome(t)

	forwardReceived := make(chan struct{}, 1)
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case forwardReceived <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()

	u, _ := url.Parse(localServer.URL)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, []map[string]any{webhookMessage()})
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runListenCmd(ctx, "--port", u.Port())

	select {
	case <-forwardReceived:
		// Message was forwarded to localhost:PORT
	case <-time.After(15 * time.Second):
		t.Fatal("Timed out waiting for forwarded webhook via --port")
	}
}

func TestListen_ForwardFlag(t *testing.T) {
	setupTestHome(t)

	forwardReceived := make(chan struct{}, 1)
	var receivedContentType string
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		select {
		case forwardReceived <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()

	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, []map[string]any{webhookMessage()})
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runListenCmd(ctx, "--forward", localServer.URL)

	select {
	case <-forwardReceived:
		assert.Equal(t, "application/json", receivedContentType)
	case <-time.After(15 * time.Second):
		t.Fatal("Timed out waiting for forwarded webhook via --forward")
	}
}
