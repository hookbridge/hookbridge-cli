package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestHome(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	return tmpDir
}

func TestSave_CreatesConfigWithCorrectPermissions(t *testing.T) {
	home := setupTestHome(t)

	cfg := &Config{
		APIKey:    "hb_live_test123",
		ProjectID: "proj_abc",
	}

	err := Save(cfg)
	require.NoError(t, err)

	path := filepath.Join(home, ".hookbridge", "config.json")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var loaded Config
	err = json.Unmarshal(data, &loaded)
	require.NoError(t, err)
	assert.Equal(t, "hb_live_test123", loaded.APIKey)
	assert.Equal(t, "proj_abc", loaded.ProjectID)
}

func TestLoad_ReadsExistingConfig(t *testing.T) {
	home := setupTestHome(t)

	dir := filepath.Join(home, ".hookbridge")
	require.NoError(t, os.MkdirAll(dir, 0700))

	cfg := Config{
		APIKey:     "hb_live_xyz",
		ProjectID:  "proj_999",
		APIBaseURL: "https://custom.api.com",
	}
	data, _ := json.Marshal(cfg)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), data, 0600))

	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "hb_live_xyz", loaded.APIKey)
	assert.Equal(t, "proj_999", loaded.ProjectID)
	assert.Equal(t, "https://custom.api.com", loaded.APIBaseURL)
}

func TestLoad_MissingConfigReturnsLoginError(t *testing.T) {
	setupTestHome(t)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func TestSave_ThenLoad_RoundTrip(t *testing.T) {
	setupTestHome(t)

	original := &Config{
		APIKey:     "hb_live_roundtrip",
		ProjectID:  "proj_rt",
		APIBaseURL: "https://api.test.hookbridge.io",
		StreamURL:  "wss://stream.test.hookbridge.io",
	}

	require.NoError(t, Save(original))

	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, original.APIKey, loaded.APIKey)
	assert.Equal(t, original.ProjectID, loaded.ProjectID)
	assert.Equal(t, original.APIBaseURL, loaded.APIBaseURL)
	assert.Equal(t, original.StreamURL, loaded.StreamURL)
}

func TestRemove_DeletesConfigFile(t *testing.T) {
	home := setupTestHome(t)

	require.NoError(t, Save(&Config{APIKey: "hb_live_x", ProjectID: "proj_x"}))

	path := filepath.Join(home, ".hookbridge", "config.json")
	_, err := os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, Remove())

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestRemove_NoConfigIsNotError(t *testing.T) {
	setupTestHome(t)

	err := Remove()
	assert.NoError(t, err)
}

func TestConfig_APIBase_DefaultsWhenEmpty(t *testing.T) {
	cfg := &Config{}
	assert.Equal(t, DefaultAPIBaseURL, cfg.APIBase())
}

func TestConfig_APIBase_UsesCustomWhenSet(t *testing.T) {
	cfg := &Config{APIBaseURL: "https://custom.api.com"}
	assert.Equal(t, "https://custom.api.com", cfg.APIBase())
}

func TestConfig_Stream_DefaultsWhenEmpty(t *testing.T) {
	cfg := &Config{}
	assert.Equal(t, DefaultStreamURL, cfg.Stream())
}

func TestConfig_APIBase_EnvVarOverridesConfigFile(t *testing.T) {
	t.Setenv("HB_API_URL", "https://api.testing.example.com")
	cfg := &Config{APIBaseURL: "https://custom.api.com"}
	assert.Equal(t, "https://api.testing.example.com", cfg.APIBase())
}

func TestConfig_Stream_EnvVarOverridesConfigFile(t *testing.T) {
	t.Setenv("HB_STREAM_URL", "wss://stream.testing.example.com")
	cfg := &Config{StreamURL: "wss://custom.stream.com"}
	assert.Equal(t, "wss://stream.testing.example.com", cfg.Stream())
}

func TestConfig_APIBase_EnvVarOverridesDefault(t *testing.T) {
	t.Setenv("HB_API_URL", "https://api.testing.example.com")
	cfg := &Config{}
	assert.Equal(t, "https://api.testing.example.com", cfg.APIBase())
}

func TestConfig_Stream_EnvVarOverridesDefault(t *testing.T) {
	t.Setenv("HB_STREAM_URL", "wss://stream.testing.example.com")
	cfg := &Config{}
	assert.Equal(t, "wss://stream.testing.example.com", cfg.Stream())
}

func TestLoad_EnvVarAPIKeyOverridesConfigFile(t *testing.T) {
	setupTestHome(t)

	require.NoError(t, Save(&Config{APIKey: "hb_live_fromfile", ProjectID: "proj_1"}))

	t.Setenv("HB_API_KEY", "hb_live_fromenv")
	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "hb_live_fromenv", loaded.APIKey)
}

func TestLoad_EnvVarAPIKeyNotSetUsesConfigFile(t *testing.T) {
	setupTestHome(t)

	require.NoError(t, Save(&Config{APIKey: "hb_live_fromfile", ProjectID: "proj_1"}))

	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "hb_live_fromfile", loaded.APIKey)
}
