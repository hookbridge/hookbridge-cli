package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is an HTTP client for the HookBridge API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Project represents a HookBridge project.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InboundEndpoint represents an inbound webhook endpoint.
type InboundEndpoint struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	Active     bool   `json:"active"`
	ReceiveURL string `json:"receive_url"`
}

// ListenMessage represents a webhook message from the listen endpoint.
type ListenMessage struct {
	MessageID    string            `json:"message_id"`
	ContentType  string            `json:"content_type"`
	Headers      map[string]string `json:"headers"`
	Body         json.RawMessage   `json:"body"`
	BodyEncoding string            `json:"body_encoding,omitempty"`
	SizeBytes    int               `json:"size_bytes"`
	ReceivedAt   string            `json:"received_at"`
}

// ListenResponse is the response from the listen polling endpoint.
type ListenResponse struct {
	Messages   []ListenMessage
	NextCursor string
}

// NewClient creates a new HookBridge API client.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 35 * time.Second, // slightly above 30s long-poll timeout
		},
	}
}

// GetProject verifies the API key and returns the project details.
func (c *Client) GetProject() (*Project, error) {
	var resp struct {
		Data Project `json:"data"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/projects", nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// ListInboundEndpoints returns all inbound endpoints for the project.
func (c *Client) ListInboundEndpoints() ([]InboundEndpoint, error) {
	var resp struct {
		Data []InboundEndpoint `json:"data"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/inbound-endpoints", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// CreateInboundEndpoint creates a new cli-mode inbound endpoint.
func (c *Client) CreateInboundEndpoint(name string) (*InboundEndpoint, error) {
	body := map[string]string{"mode": "cli", "name": name}
	var resp struct {
		Data InboundEndpoint `json:"data"`
	}
	if err := c.doJSON(http.MethodPost, "/v1/inbound-endpoints", body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// ListenMessages polls for new webhook messages on a cli-mode endpoint.
func (c *Client) ListenMessages(endpointID, afterCursor string) (*ListenResponse, error) {
	path := "/v1/inbound-endpoints/" + url.PathEscape(endpointID) + "/listen"
	if afterCursor != "" {
		path += "?after=" + url.QueryEscape(afterCursor)
	}

	var resp struct {
		Data []ListenMessage `json:"data"`
		Meta struct {
			NextCursor *string `json:"next_cursor"`
		} `json:"meta"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}

	result := &ListenResponse{
		Messages: resp.Data,
	}
	if resp.Meta.NextCursor != nil {
		result.NextCursor = *resp.Meta.NextCursor
	}
	return result, nil
}

func (c *Client) doJSON(method, path string, body any, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("could not encode request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("could not create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("could not read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("invalid API key — run 'hb login' to re-authenticate")
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limited — please wait and try again")
	case http.StatusForbidden:
		return fmt.Errorf("access denied — check your API key permissions")
	}

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("API error (%s): %s", apiErr.Error.Code, apiErr.Error.Message)
		}
		return fmt.Errorf("API error: HTTP %d", resp.StatusCode)
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("could not parse response: %w", err)
		}
	}

	return nil
}
