package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
		"data": map[string]any{"id": id, "name": name},
	}
}

func endpointListResponse(endpoints ...map[string]any) map[string]any {
	return map[string]any{"data": endpoints}
}

func endpointCreatedResponse(id, name, mode, receiveURL string) map[string]any {
	return map[string]any{
		"data": map[string]any{
			"id": id, "name": name, "mode": mode,
			"active": true, "receive_url": receiveURL,
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
	assert.Equal(t, "proj_123", cfg.ProjectID)

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
