package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_AddsAuthorizationHeader(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "key_1", "label": "default"}}})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_testkey123")
	_, _ = client.GetProject()

	assert.Equal(t, "Bearer hb_live_testkey123", capturedAuth)
}

func TestClient_GetProject_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/api-keys", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "key_1", "label": "default"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	project, err := client.GetProject()
	require.NoError(t, err)
	assert.Equal(t, "authenticated", project.Name)
}

func TestClient_GetProject_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "UNAUTHORIZED", "message": "Invalid API key"},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_bad")
	_, err := client.GetProject()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid API key")
}

func TestClient_GetProject_RateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "RATE_LIMITED", "message": "Too many requests"},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	_, err := client.GetProject()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
}

func TestClient_ListInboundEndpoints_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/inbound-endpoints", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "ie_1", "name": "Stripe", "mode": "cli", "active": true, "ingest_url": "https://receive.hookbridge.io/v1/webhooks/receive/ie_1/secret1"},
				{"id": "ie_2", "name": "GitHub", "mode": "forward", "active": true, "ingest_url": "https://receive.hookbridge.io/v1/webhooks/receive/ie_2/secret2"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	endpoints, err := client.ListInboundEndpoints()
	require.NoError(t, err)
	require.Len(t, endpoints, 2)
	assert.Equal(t, "ie_1", endpoints[0].ID)
	assert.Equal(t, "cli", endpoints[0].Mode)
	assert.Equal(t, "ie_2", endpoints[1].ID)
	assert.Equal(t, "forward", endpoints[1].Mode)
}

func TestClient_CreateInboundEndpoint_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/inbound-endpoints", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "cli", body["mode"])
		assert.Equal(t, "Test Endpoint", body["name"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":          "ie_new",
				"name":        "Test Endpoint",
				"mode":        "cli",
				"active":      true,
				"ingest_url": "https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret_new",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	ep, err := client.CreateInboundEndpoint("Test Endpoint")
	require.NoError(t, err)
	assert.Equal(t, "ie_new", ep.ID)
	assert.Equal(t, "cli", ep.Mode)
	assert.Contains(t, ep.ReceiveURL, "ie_new")
}

func TestClient_ListenMessages_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/inbound-endpoints/ie_1/listen", r.URL.Path)
		assert.Equal(t, "cursor_abc", r.URL.Query().Get("after"))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"message_id":   "msg_1",
					"content_type": "application/json",
					"headers":      map[string]string{"x-hook": "test"},
					"body":         map[string]any{"event": "checkout"},
					"size_bytes":   42,
					"received_at":  "2026-03-21T10:30:00Z",
				},
			},
			"meta": map[string]any{
				"next_cursor": "msg_1",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	resp, err := client.ListenMessages("ie_1", "cursor_abc")
	require.NoError(t, err)
	require.Len(t, resp.Messages, 1)
	assert.Equal(t, "msg_1", resp.Messages[0].MessageID)
	assert.Equal(t, "application/json", resp.Messages[0].ContentType)
	assert.Equal(t, "msg_1", resp.NextCursor)
}

func TestClient_ListenMessages_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
			"meta": map[string]any{"next_cursor": nil},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	resp, err := client.ListenMessages("ie_1", "")
	require.NoError(t, err)
	assert.Empty(t, resp.Messages)
	assert.Empty(t, resp.NextCursor)
}
