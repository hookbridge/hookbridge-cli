package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hookbridge/hookbridge-cli/internal/config"
	"github.com/spf13/cobra"
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
	t.Setenv("HB_API_KEY", "")

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func TestEndpoints_HumanOutputUnchanged(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(endpointListResponse(
			map[string]any{"id": "ie_1", "name": "Stripe CLI", "mode": "cli", "active": true},
			map[string]any{"id": "ie_2", "name": "Old CLI", "mode": "cli", "active": false},
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{"endpoints"})
	err := cmd.Execute()
	require.NoError(t, err)

	out := outBuf.String()
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "ACTIVE")
	assert.Contains(t, out, "ie_1")
	assert.Contains(t, out, "yes")
	assert.Contains(t, out, "ie_2")
	assert.Contains(t, out, "no")
}

func TestEndpoints_JSON_ShapeIsIDNameActive(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(endpointListResponse(
			map[string]any{"id": "ie_1", "name": "Stripe CLI", "mode": "cli", "active": true},
			map[string]any{"id": "ie_2", "name": "Old CLI", "mode": "cli", "active": false},
			map[string]any{"id": "ie_3", "name": "Forward EP", "mode": "forward", "active": true},
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--json", "endpoints"})

	err := cmd.Execute()
	require.NoError(t, err)

	assert.Empty(t, errBuf.String())

	var got []map[string]any
	require.NoError(t, json.Unmarshal(outBuf.Bytes(), &got), "stdout must be valid JSON with nothing else mixed in")
	require.Len(t, got, 2, "non-cli endpoint must be filtered out")

	for _, ep := range got {
		assert.ElementsMatch(t, []string{"id", "name", "active"}, mapKeys(ep))
	}

	byID := map[string]map[string]any{}
	for _, ep := range got {
		byID[ep["id"].(string)] = ep
	}
	require.Contains(t, byID, "ie_1")
	require.Contains(t, byID, "ie_2")
	assert.Equal(t, true, byID["ie_1"]["active"])
	assert.Equal(t, false, byID["ie_2"]["active"])

	trimmed := strings.TrimRight(outBuf.String(), "\n")
	assert.NotContains(t, trimmed, "\n", "stdout should be exactly one JSON line")
}

func TestEndpoints_JSON_EmptyListIsEmptyArray(t *testing.T) {
	cases := []struct {
		name      string
		endpoints []map[string]any
	}{
		{"no endpoints", nil},
		{"only non-cli endpoint", []map[string]any{
			{"id": "ie_1", "name": "Forward EP", "mode": "forward", "active": true},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupTestHome(t)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(endpointListResponse(tc.endpoints...))
			}))
			defer server.Close()

			require.NoError(t, config.Save(&config.Config{
				APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
			}))

			cmd := rootCmd()
			outBuf := new(bytes.Buffer)
			errBuf := new(bytes.Buffer)
			cmd.SetOut(outBuf)
			cmd.SetErr(errBuf)
			cmd.SetArgs([]string{"--json", "endpoints"})

			err := cmd.Execute()
			require.NoError(t, err)

			assert.Empty(t, errBuf.String())

			trimmed := strings.TrimRight(outBuf.String(), "\n")
			assert.Equal(t, "[]", trimmed)
			assert.NotEqual(t, "null", trimmed)

			combined := outBuf.String() + errBuf.String()
			assert.NotContains(t, combined, "No CLI-mode endpoints found")
			assert.NotContains(t, combined, "hb endpoints create")
		})
	}
}

func TestEndpoints_JSON_DoesNotLeakReceiveURL(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(endpointListResponse(
			map[string]any{
				"id": "ie_1", "name": "Stripe CLI", "mode": "cli", "active": true,
				"ingest_url": "https://receive.hookbridge.io/v1/webhooks/receive/ie_1/sk_super_secret_value",
			},
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--json", "endpoints"})

	err := cmd.Execute()
	require.NoError(t, err)

	// Security regression test: ingest_url/receive_url embeds a plaintext
	// secret in its path, so the JSON list output must never include it.
	combined := outBuf.String() + errBuf.String()
	assert.NotContains(t, combined, "sk_super_secret_value")
	assert.NotContains(t, combined, "ingest_url")
	assert.NotContains(t, combined, "receive_url")
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

func TestEndpointsCreate_TTLWithoutEphemeral_ErrorsBeforeAPICall(t *testing.T) {
	setupTestHome(t)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints", "create", "--ttl-minutes", "30"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ephemeral")
	assert.Equal(t, int32(0), requests.Load(), "validation must fail before any API call is made")
}

func TestEndpointsCreate_TTLOutOfRange_Errors(t *testing.T) {
	setupTestHome(t)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	for _, ttl := range []string{"0", "1441"} {
		cmd := rootCmd()
		cmd.SetArgs([]string{"endpoints", "create", "--ephemeral", "--ttl-minutes", ttl})
		err := cmd.Execute()
		require.Error(t, err, "ttl=%s should be rejected", ttl)
		assert.Contains(t, err.Error(), "--ttl-minutes must be between 1 and 1440")
	}
	assert.Equal(t, int32(0), requests.Load(), "validation must fail before any API call is made")
}

func TestEndpointsCreate_TTLBoundaries_Accepted(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(endpointCreatedResponse(
			"ie_new", "CLI Endpoint", "cli",
			"https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret",
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	for _, ttl := range []string{"1", "1440"} {
		cmd := rootCmd()
		cmd.SetArgs([]string{"endpoints", "create", "--ephemeral", "--ttl-minutes", ttl})
		err := cmd.Execute()
		require.NoError(t, err, "ttl=%s should be accepted", ttl)
	}
}

func TestEndpointsCreate_EphemeralWithTTL_HappyPath(t *testing.T) {
	setupTestHome(t)

	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(endpointCreatedResponse(
			"ie_new", "CLI Endpoint", "cli",
			"https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret",
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{"endpoints", "create", "--ephemeral", "--ttl-minutes", "30"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.Equal(t, true, capturedBody["ephemeral"])
	assert.Equal(t, float64(30), capturedBody["ttl_minutes"])
	assert.Contains(t, outBuf.String(), "Created endpoint:")
	assert.Contains(t, outBuf.String(), "Receive URL:")
}

func TestEndpointsCreate_EphemeralWithoutTTL_OmitsTTLFromBody(t *testing.T) {
	setupTestHome(t)

	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(endpointCreatedResponse(
			"ie_new", "CLI Endpoint", "cli",
			"https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret",
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints", "create", "--ephemeral"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.Equal(t, true, capturedBody["ephemeral"])
	_, hasTTL := capturedBody["ttl_minutes"]
	assert.False(t, hasTTL, "ttl_minutes must be omitted from the request body when --ttl-minutes is not set")
}

func TestEndpointsCreate_JSON_ShapeIsIDAndReceiveURL(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(endpointCreatedResponse(
			"ie_new", "CLI Endpoint", "cli",
			"https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret",
		))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--json", "endpoints", "create", "--ephemeral", "--ttl-minutes", "30"})

	err := cmd.Execute()
	require.NoError(t, err)

	assert.Empty(t, errBuf.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal(outBuf.Bytes(), &got), "stdout must be valid JSON with nothing else mixed in")
	assert.ElementsMatch(t, []string{"id", "receive_url"}, mapKeys(got))
	assert.Equal(t, "ie_new", got["id"])
	assert.Equal(t, "https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret", got["receive_url"])

	trimmed := strings.TrimRight(outBuf.String(), "\n")
	assert.NotContains(t, trimmed, "\n", "stdout should be exactly one JSON line")
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// --- Endpoints Delete Tests ---

func fakeTTY(t *testing.T, isTTY bool) {
	t.Helper()
	orig := stdinIsTTY
	stdinIsTTY = func(io.Reader) bool { return isTTY }
	t.Cleanup(func() { stdinIsTTY = orig })
}

func fakeOutputTTY(t *testing.T, tty bool) {
	t.Helper()
	orig := isTTY
	isTTY = func(any) bool { return tty }
	t.Cleanup(func() { isTTY = orig })
}

func TestEndpointsDelete_RequiresID(t *testing.T) {
	setupTestHome(t)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints", "delete"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Equal(t, int32(0), requests.Load(), "missing ID must fail before any API call is made")
}

func TestEndpointsDelete_NonTTY_DeletesWithoutPrompt(t *testing.T) {
	setupTestHome(t)
	fakeTTY(t, false)

	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	errBuf := new(bytes.Buffer)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"endpoints", "delete", "ie_abc"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "/v1/inbound-endpoints/ie_abc", path)
	assert.NotContains(t, errBuf.String(), "[y/N]")
}

func TestEndpointsDelete_TTY_PromptsAndDeletesOnYes(t *testing.T) {
	setupTestHome(t)
	fakeTTY(t, true)

	var deleteCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCount.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	errBuf := new(bytes.Buffer)
	cmd.SetErr(errBuf)
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetArgs([]string{"endpoints", "delete", "ie_abc"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.Equal(t, int32(1), deleteCount.Load())
	assert.Contains(t, errBuf.String(), "[y/N]")
	assert.Contains(t, errBuf.String(), "ie_abc")
}

func TestEndpointsDelete_TTY_AbortsOnNo(t *testing.T) {
	setupTestHome(t)
	fakeTTY(t, true)

	var deleteCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCount.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs([]string{"endpoints", "delete", "ie_abc"})
	err := cmd.Execute()
	require.Error(t, err)

	assert.Equal(t, int32(0), deleteCount.Load())
}

func TestEndpointsDelete_TTY_AbortsOnEmptyInput(t *testing.T) {
	setupTestHome(t)
	fakeTTY(t, true)

	var deleteCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCount.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"endpoints", "delete", "ie_abc"})
	err := cmd.Execute()
	require.Error(t, err)

	assert.Equal(t, int32(0), deleteCount.Load())
}

func TestEndpointsDelete_Force_SkipsPromptOnTTY(t *testing.T) {
	setupTestHome(t)
	fakeTTY(t, true)

	var deleteCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCount.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	errBuf := new(bytes.Buffer)
	cmd.SetErr(errBuf)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"endpoints", "delete", "ie_abc", "--force"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.Equal(t, int32(1), deleteCount.Load())
	assert.NotContains(t, errBuf.String(), "[y/N]")
}

func TestEndpointsDelete_UnknownID_Errors(t *testing.T) {
	setupTestHome(t)
	fakeTTY(t, false)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "not_found", "message": "inbound endpoint not found"},
		})
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	cmd.SetArgs([]string{"endpoints", "delete", "ie_missing"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestEndpointsDelete_JSON_Shape(t *testing.T) {
	setupTestHome(t)
	fakeTTY(t, false)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--json", "endpoints", "delete", "ie_abc"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.Empty(t, errBuf.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal(outBuf.Bytes(), &got), "stdout must be valid JSON with nothing else mixed in")
	assert.ElementsMatch(t, []string{"id", "deleted"}, mapKeys(got))
	assert.Equal(t, "ie_abc", got["id"])
	assert.Equal(t, true, got["deleted"])

	trimmed := strings.TrimRight(outBuf.String(), "\n")
	assert.NotContains(t, trimmed, "\n", "stdout should be exactly one JSON line")
}

func TestStdinIsTTY_NonTerminalsAreNotTTY(t *testing.T) {
	devNull, err := os.Open("/dev/null")
	require.NoError(t, err)
	t.Cleanup(func() { devNull.Close() })
	assert.False(t, stdinIsTTY(devNull), "/dev/null is a character device but not a terminal")

	tmpFile, err := os.CreateTemp(t.TempDir(), "stdin-tty-test")
	require.NoError(t, err)
	t.Cleanup(func() { tmpFile.Close() })
	assert.False(t, stdinIsTTY(tmpFile), "a regular file is not a terminal")

	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { pr.Close(); pw.Close() })
	assert.False(t, stdinIsTTY(pr), "the read end of a pipe is not a terminal")

	assert.False(t, stdinIsTTY(strings.NewReader("")), "a non-*os.File reader is not a terminal")
}

func TestEndpointsDelete_DevNullStdin_DeletesWithoutPrompt(t *testing.T) {
	setupTestHome(t)

	f, err := os.Open("/dev/null")
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })

	var deleteCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCount.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1", APIBaseURL: server.URL,
	}))

	cmd := rootCmd()
	errBuf := new(bytes.Buffer)
	cmd.SetErr(errBuf)
	cmd.SetIn(f)
	cmd.SetArgs([]string{"endpoints", "delete", "ie_abc"})
	err = cmd.Execute()
	require.NoError(t, err)

	assert.Equal(t, int32(1), deleteCount.Load())
	assert.NotContains(t, errBuf.String(), "[y/N]")
}

// --- Version Test ---

func TestVersion_PrintsVersion(t *testing.T) {
	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{"version"})
	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, outBuf.String(), "hb version")
}

// --- Global --json Flag Tests ---

func TestRootCmd_HasPersistentJSONFlag(t *testing.T) {
	cmd := rootCmd()
	flag := cmd.PersistentFlags().Lookup("json")
	require.NotNil(t, flag, "root command must define a persistent --json flag")
	assert.Equal(t, "bool", flag.Value.Type())
	assert.Equal(t, "false", flag.DefValue)
}

func TestRootCmd_JSONFlagResolvesOnSubcommand(t *testing.T) {
	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{"--json", "version"})

	var resolved bool
	for _, c := range cmd.Commands() {
		if c.Use == "version" {
			orig := c.RunE
			c.RunE = func(sub *cobra.Command, args []string) error {
				jsonMode, err := sub.Flags().GetBool("json")
				require.NoError(t, err)
				resolved = jsonMode
				if orig != nil {
					return orig(sub, args)
				}
				return nil
			}
		}
	}

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, resolved, "subcommand should resolve inherited persistent --json flag via cmd.Flags()")
}

func TestRootCmd_JSONFlagResolvesTwoLevelsDeep(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--json", "endpoints", "create", "--name", "Stripe Webhooks"})

	err := cmd.Execute()
	require.NoError(t, err)

	assert.Contains(t, outBuf.String(), `"id":"ie_new"`, "endpoints create is two levels below root; --json should still resolve and produce JSON output")
	assert.Empty(t, errBuf.String())
}

func TestVersion_JSON_StdoutIsPureJSON(t *testing.T) {
	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--json", "version"})

	err := cmd.Execute()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(outBuf.Bytes(), &got), "stdout must be valid JSON with nothing else mixed in")
	assert.Contains(t, got, "version")
	assert.Empty(t, errBuf.String())

	trimmed := strings.TrimRight(outBuf.String(), "\n")
	assert.NotContains(t, trimmed, "\n", "stdout should be exactly one JSON line")
}

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) {
	return 0, errors.New("write failed")
}

// --- colorEnabled Precedence Tests ---

func TestColorEnabled_JSONMode_ReturnsFalse(t *testing.T) {
	fakeOutputTTY(t, true)
	t.Setenv("NO_COLOR", "")

	cmd := rootCmd()
	require.NoError(t, cmd.ParseFlags(nil))

	assert.False(t, colorEnabled(cmd, true))
}

func TestColorEnabled_NoColorFlag_ReturnsFalse(t *testing.T) {
	fakeOutputTTY(t, true)
	t.Setenv("NO_COLOR", "")

	cmd := rootCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--no-color"}))

	assert.False(t, colorEnabled(cmd, false))
}

func TestColorEnabled_NoColorEnvSet_ReturnsFalse(t *testing.T) {
	fakeOutputTTY(t, true)
	t.Setenv("NO_COLOR", "1")

	cmd := rootCmd()
	require.NoError(t, cmd.ParseFlags(nil))

	assert.False(t, colorEnabled(cmd, false))
}

func TestColorEnabled_NoColorEnvEmpty_ReturnsTrue(t *testing.T) {
	fakeOutputTTY(t, true)
	t.Setenv("NO_COLOR", "")

	cmd := rootCmd()
	require.NoError(t, cmd.ParseFlags(nil))

	assert.True(t, colorEnabled(cmd, false), "an empty NO_COLOR means \"not set\" per the spec")
}

func TestColorEnabled_NoFlagsNoEnv_TTY_ReturnsTrue(t *testing.T) {
	fakeOutputTTY(t, true)
	t.Setenv("NO_COLOR", "")

	cmd := rootCmd()
	require.NoError(t, cmd.ParseFlags(nil))

	assert.True(t, colorEnabled(cmd, false))
}

func TestColorEnabled_NoFlagsNoEnv_NotTTY_ReturnsFalse(t *testing.T) {
	fakeOutputTTY(t, false)
	t.Setenv("NO_COLOR", "")

	cmd := rootCmd()
	require.NoError(t, cmd.ParseFlags(nil))

	assert.False(t, colorEnabled(cmd, false))
}

func TestVersion_JSON_WriteFailure_ReturnsError(t *testing.T) {
	cmd := rootCmd()
	cmd.SetOut(failingWriter{})
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"--json", "version"})

	err := cmd.Execute()

	require.Error(t, err, "a command that fails to emit its only output must not exit successfully")
}

func TestLogin_JSON_StdoutEmpty_HumanTextOnStderr(t *testing.T) {
	setupTestHome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(projectResponse("proj_123", "My Project"))
	}))
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{APIBaseURL: server.URL}))

	cmd := rootCmd()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"--json", "login", "--api-key", "hb_live_validkey"})

	err := cmd.Execute()
	require.NoError(t, err)

	assert.Empty(t, outBuf.String(), "stdout must stay empty under --json for login")
	assert.Contains(t, errBuf.String(), "Verifying...")
	assert.Contains(t, errBuf.String(), "OK")
	assert.Contains(t, errBuf.String(), "Project: authenticated")
	cfgPath, _ := config.Path()
	assert.Contains(t, errBuf.String(), cfgPath)
	assert.NotContains(t, errBuf.String(), "hb_live_validkey", "API key must never appear in output")
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

func runListenCmdCapturing(ctx context.Context, stdout, stderr io.Writer, args ...string) chan error {
	done := make(chan error, 1)
	go func() {
		cmd := rootCmd()
		cmd.SetContext(ctx)
		cmd.SetOut(stdout)
		cmd.SetErr(stderr)
		cmd.SetArgs(args)
		done <- cmd.Execute()
	}()
	return done
}

func TestListen_JSON_ReadyFirst_WebhookLine_PureNDJSON(t *testing.T) {
	setupTestHome(t)

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, []map[string]any{webhookMessage()}, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "--json", "listen", "--no-forward")

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll after message")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 2, "expected at least ready + shutdown lines")

	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &ev), "line must be valid JSON: %s", line)
		events = append(events, ev)
	}

	assert.Equal(t, "ready", events[0]["event"])
	assert.Equal(t, "ie_1", events[0]["endpoint_id"])
	assert.Equal(t, "", events[0]["forward_to"])
	assert.ElementsMatch(t, []string{"event", "endpoint_id", "forward_to"}, mapKeys(events[0]))

	sawWebhook := false
	for _, ev := range events[1:] {
		if ev["event"] == "webhook" {
			sawWebhook = true
			assert.Equal(t, "msg_1", ev["id"])
		}
	}
	assert.True(t, sawWebhook, "expected a webhook event after ready")

	assert.Equal(t, "shutdown", events[len(events)-1]["event"])
}

func TestListen_JSON_ForwardMode_WebhookEventShowsForwardedTrue(t *testing.T) {
	setupTestHome(t)

	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, []map[string]any{webhookMessage()}, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "--json", "listen", "--forward", localServer.URL)

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll after message")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.NotEmpty(t, lines)

	var ready map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &ready))
	assert.Equal(t, localServer.URL, ready["forward_to"])

	sawForwarded := false
	for _, line := range lines[1:] {
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &ev))
		if ev["event"] == "webhook" {
			assert.Equal(t, true, ev["forwarded"])
			assert.Equal(t, float64(200), ev["status_code"])
			assert.NotContains(t, ev, "error")
			sawForwarded = true
		}
	}
	assert.True(t, sawForwarded, "expected a forwarded webhook event")
}

func TestListen_JSON_DoesNotLeakReceiveURLOnStdout(t *testing.T) {
	setupTestHome(t)

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, nil, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "--json", "listen", "--no-forward")

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	// Security regression: cliEndpoint()'s ingest_url embeds a plaintext secret
	// (sk_abc). The JSON ready/webhook/shutdown stream on stdout must never
	// include it.
	assert.NotContains(t, stdout.String(), "sk_abc")
	assert.NotContains(t, stdout.String(), "receive_url")
	assert.NotContains(t, stdout.String(), "ingest_url")
}

func TestListen_ForwardFlag_RedactsCredentialsInBanner(t *testing.T) {
	setupTestHome(t)

	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()
	forwardURL := strings.Replace(localServer.URL, "http://", "http://user:hunter2@", 1)

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, nil, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "listen", "--forward", forwardURL)

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	assert.Contains(t, stdout.String(), "Forwarding to")
	assert.Contains(t, stdout.String(), "REDACTED")
	assert.NotContains(t, stdout.String(), "hunter2")
}

func TestListen_JSON_ForwardFlag_RedactsCredentialsInReadyEvent(t *testing.T) {
	setupTestHome(t)

	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()
	forwardURL := strings.Replace(localServer.URL, "http://", "http://user:hunter2@", 1)

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, nil, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "--json", "listen", "--forward", forwardURL)

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.NotEmpty(t, lines)

	var ready map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &ready))
	forwardTo, _ := ready["forward_to"].(string)
	assert.Contains(t, forwardTo, "REDACTED")
	assert.NotContains(t, stdout.String(), "hunter2")
}

func TestListen_NormalMode_ReadyLineUnchanged(t *testing.T) {
	setupTestHome(t)

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, nil, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "listen", "--no-forward")

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	assert.Contains(t, stdout.String(), "Ready. Waiting for webhooks...\n")
	assert.Contains(t, stdout.String(), "Webhook URL: https://receive.hookbridge.io/v1/webhooks/receive/ie_1/sk_abc")
}

func TestListen_Banner_VersionTag_PrintsSingleV(t *testing.T) {
	setupTestHome(t)

	original := Version
	Version = "v1.1.2"
	t.Cleanup(func() { Version = original })

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, nil, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "listen", "--no-forward")

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	assert.Contains(t, stdout.String(), "HookBridge CLI v1.1.2")
	assert.NotContains(t, stdout.String(), "vv")
}

func TestListen_Banner_DevVersion_PrintsDev(t *testing.T) {
	setupTestHome(t)

	original := Version
	Version = "dev"
	t.Cleanup(func() { Version = original })

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, nil, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "listen", "--no-forward")

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	assert.Contains(t, stdout.String(), "HookBridge CLI dev")
}

func TestListen_ReusedEndpoint_NoReceiveURL_OmitsBlankURLAndPasteLine(t *testing.T) {
	setupTestHome(t)

	reusedEndpoint := map[string]any{
		"id": "ie_1", "name": "CLI EP", "mode": "cli", "active": true,
	}

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{reusedEndpoint}, nil, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "listen", "--no-forward")

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	out := stdout.String()
	assert.NotContains(t, out, "Webhook URL: \n")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Webhook URL:") {
			assert.NotEqual(t, "Webhook URL:", strings.TrimSpace(line), "Webhook URL line must not be blank")
		}
	}
	assert.NotContains(t, out, "Paste this URL")
	assert.Contains(t, out, "Webhook URL: not shown — HookBridge returns it only when the endpoint is created.")
}

func TestListen_CreatedEndpoint_ShowsURLAndPasteLine(t *testing.T) {
	setupTestHome(t)

	createDone := make(chan struct{}, 1)
	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, nil, nil, createDone, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "listen", "--no-forward")

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	assert.Contains(t, stdout.String(), "Webhook URL: https://receive.hookbridge.io/v1/webhooks/receive/ie_created/sk_abc")
	assert.Contains(t, stdout.String(), "Paste this URL into your webhook provider's settings.")
}

// --- --no-color Flag Tests ---

func TestListen_ForwardMode_TTY_ColorsWithoutNoColorFlag(t *testing.T) {
	setupTestHome(t)
	fakeOutputTTY(t, true)
	t.Setenv("NO_COLOR", "")

	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, []map[string]any{webhookMessage()}, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "listen", "--forward", localServer.URL)

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll after message")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	assert.Contains(t, stdout.String(), "\033[32m", "expected the 200 status to render green on a simulated TTY")
}

func TestListen_NoColorFlag_ForwardMode_NoEscapeCodesOnStdout(t *testing.T) {
	setupTestHome(t)
	fakeOutputTTY(t, true)
	t.Setenv("NO_COLOR", "")

	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer localServer.Close()

	pollSecond := make(chan struct{}, 1)
	server := listenAPIServer(t, []map[string]any{cliEndpoint()}, []map[string]any{webhookMessage()}, nil, pollSecond)
	defer server.Close()

	require.NoError(t, config.Save(&config.Config{
		APIKey: "hb_live_key", ProjectID: "proj_1",
		APIBaseURL: server.URL, StreamURL: server.URL,
	}))

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := runListenCmdCapturing(ctx, &stdout, &stderr, "--no-color", "listen", "--forward", localServer.URL)

	select {
	case <-pollSecond:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("Timed out waiting for poll after message")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for listen command to exit")
	}

	assert.Contains(t, stdout.String(), "POST", "sanity check the webhook line was actually rendered")
	assert.NotContains(t, stdout.String(), "\033[")
}
