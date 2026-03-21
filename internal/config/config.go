package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultAPIBaseURL    = "https://api.hookbridge.io"
	DefaultStreamURL     = "wss://stream.hookbridge.io"
	configDir            = ".hookbridge"
	configFile           = "config.json"
)

// Config holds the CLI credentials and settings.
type Config struct {
	APIKey     string `json:"api_key"`
	ProjectID  string `json:"project_id"`
	APIBaseURL string `json:"api_base_url,omitempty"`
	StreamURL  string `json:"stream_url,omitempty"`
}

// Path returns the full path to the config file.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, configDir, configFile), nil
}

// Load reads the config from disk.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not logged in — run 'hb login' first")
		}
		return nil, fmt.Errorf("could not read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("could not parse config: %w", err)
	}

	return &cfg, nil
}

// Save writes the config to disk with 0600 permissions.
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("could not write config: %w", err)
	}

	return nil
}

// Remove deletes the config file.
func Remove() error {
	path, err := Path()
	if err != nil {
		return err
	}

	err = os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not remove config: %w", err)
	}
	return nil
}

// APIBase returns the API base URL, falling back to the default.
func (c *Config) APIBase() string {
	if c.APIBaseURL != "" {
		return c.APIBaseURL
	}
	return DefaultAPIBaseURL
}

// Stream returns the stream URL, falling back to the default.
func (c *Config) Stream() string {
	if c.StreamURL != "" {
		return c.StreamURL
	}
	return DefaultStreamURL
}
