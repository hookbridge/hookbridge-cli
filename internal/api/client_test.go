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
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		assert.Equal(t, "cli", body["mode"])
		assert.Equal(t, "Test Endpoint", body["name"])
		assert.NotContains(t, body, "ephemeral")
		assert.NotContains(t, body, "ttl_minutes")

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
	ep, err := client.CreateInboundEndpoint(CreateInboundEndpointOptions{Name: "Test Endpoint"})
	require.NoError(t, err)
	assert.Equal(t, "ie_new", ep.ID)
	assert.Equal(t, "cli", ep.Mode)
	assert.Contains(t, ep.ReceiveURL, "ie_new")
}

func TestClient_CreateInboundEndpoint_EphemeralWithTTL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		assert.Equal(t, "cli", body["mode"])
		assert.Equal(t, "Test Endpoint", body["name"])
		assert.Equal(t, true, body["ephemeral"])
		assert.Equal(t, float64(60), body["ttl_minutes"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":         "ie_new",
				"name":       "Test Endpoint",
				"mode":       "cli",
				"active":     true,
				"ingest_url": "https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret_new",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	ep, err := client.CreateInboundEndpoint(CreateInboundEndpointOptions{
		Name:       "Test Endpoint",
		Ephemeral:  true,
		TTLMinutes: 60,
	})
	require.NoError(t, err)
	assert.Equal(t, "ie_new", ep.ID)
}

func TestClient_CreateInboundEndpoint_EphemeralWithoutTTL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		assert.Equal(t, true, body["ephemeral"])
		assert.NotContains(t, body, "ttl_minutes")

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":         "ie_new",
				"name":       "Test Endpoint",
				"mode":       "cli",
				"active":     true,
				"ingest_url": "https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret_new",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	ep, err := client.CreateInboundEndpoint(CreateInboundEndpointOptions{
		Name:      "Test Endpoint",
		Ephemeral: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "ie_new", ep.ID)
}

func TestClient_CreateInboundEndpoint_TTLWithoutEphemeral(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		assert.NotContains(t, body, "ephemeral")
		assert.Equal(t, float64(60), body["ttl_minutes"])

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":         "ie_new",
				"name":       "Test Endpoint",
				"mode":       "cli",
				"active":     true,
				"ingest_url": "https://receive.hookbridge.io/v1/webhooks/receive/ie_new/secret_new",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	ep, err := client.CreateInboundEndpoint(CreateInboundEndpointOptions{
		Name:       "Test Endpoint",
		Ephemeral:  false,
		TTLMinutes: 60,
	})
	require.NoError(t, err)
	assert.Equal(t, "ie_new", ep.ID)
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

func TestClient_DeleteInboundEndpoint_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/v1/inbound-endpoints/ie_1", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"id": "ie_1", "deleted": true},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	err := client.DeleteInboundEndpoint("ie_1")
	require.NoError(t, err)
}

func TestClient_DeleteInboundEndpoint_NoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	err := client.DeleteInboundEndpoint("ie_1")
	require.NoError(t, err)
}

func TestClient_DeleteInboundEndpoint_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "NOT_FOUND", "message": "Inbound endpoint not found"},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	err := client.DeleteInboundEndpoint("ie_missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ie_missing")
	assert.NotContains(t, err.Error(), "API error: HTTP 404")
}

func TestClient_DeleteInboundEndpoint_PathEscaping(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"id": "ie_1", "deleted": true},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	_ = client.DeleteInboundEndpoint("ie_1/../../v1/projects")

	assert.NotEqual(t, "/v1/projects", capturedPath)
	assert.Equal(t, "/v1/inbound-endpoints/ie_1/../../v1/projects", capturedPath)
}

func TestClient_DoJSON_NonNotFoundErrorUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	err := client.DeleteInboundEndpoint("ie_1")
	require.Error(t, err)
	assert.Equal(t, "API error: HTTP 500", err.Error())
}

func TestClient_DoJSON_NoContentWithResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	var result struct {
		Data string `json:"data"`
	}
	err := client.doJSON(http.MethodGet, "/v1/whatever", nil, &result)
	require.NoError(t, err)
}

func TestClient_DeleteInboundEndpoint_NotFoundEmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	err := client.DeleteInboundEndpoint("ie_missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ie_missing")
	assert.NotContains(t, err.Error(), "API error: HTTP 404")
}

func TestClient_DeleteInboundEndpoint_EmptyID(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, "hb_live_key")
	err := client.DeleteInboundEndpoint("")
	require.Error(t, err)
	assert.False(t, called)
}
