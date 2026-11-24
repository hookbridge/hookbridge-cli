package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_429WithAPIError_SurfacesCodeAndMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code": "EVENT_TYPE_LIMIT_REACHED", "message": "Maximum event type limit reached for your plan.",
				"details": map[string]any{"limit": 1000},
			},
		})
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").GetEventType("order.created")
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 429, apiErr.StatusCode)
	assert.Equal(t, "EVENT_TYPE_LIMIT_REACHED", apiErr.Code)
	assert.Contains(t, err.Error(), "Maximum event type limit")
}

func TestClient_429WithoutBody_KeepsGenericMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").GetProject()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
}

func TestListEventTypes_QueryAndDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/event-types", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "orders", q.Get("group"))
		assert.Equal(t, "order.&x", q.Get("name_prefix"))
		assert.Equal(t, "true", q.Get("include_archived"))
		assert.Equal(t, "25", q.Get("limit"))
		assert.Equal(t, "cur 1", q.Get("cursor"))
		assert.Equal(t, "Bearer hb_key", r.Header.Get("Authorization"))
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"name": "order.created", "group": "orders", "deprecated": true, "schema": map[string]any{"type": "object"}}},
			"meta": map[string]any{"next_cursor": "next"},
		})
	}))
	defer server.Close()

	page, err := NewClient(server.URL, "hb_key").ListEventTypes(ListEventTypesParams{
		Group: "orders", NamePrefix: "order.&x", IncludeArchived: true, Limit: 25, Cursor: "cur 1",
	})
	require.NoError(t, err)
	require.Len(t, page.Data, 1)
	assert.Equal(t, "order.created", page.Data[0].Name)
	assert.Equal(t, "orders", *page.Data[0].Group)
	assert.True(t, page.Data[0].Deprecated)
	assert.JSONEq(t, `{"type":"object"}`, string(page.Data[0].Schema))
	assert.Equal(t, "next", page.NextCursor)
}

func TestListEventTypes_OmitsUnsetParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "", r.URL.RawQuery)
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").ListEventTypes(ListEventTypesParams{})
	require.NoError(t, err)
}

func TestGetEventType_EscapesName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/event-types/a%2Fb%3Fc", r.URL.EscapedPath())
		assert.Equal(t, "", r.URL.RawQuery)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "a/b?c"}})
	}))
	defer server.Close()

	et, err := NewClient(server.URL, "k").GetEventType("a/b?c")
	require.NoError(t, err)
	assert.Equal(t, "a/b?c", et.Name)
}

func TestPathSegments_RejectDotAndEmpty(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "k")
	for _, bad := range []string{"", ".", ".."} {
		_, err := c.GetEventType(bad)
		require.Error(t, err, "%q", bad)
		assert.NotContains(t, err.Error(), "connection")
		_, err = c.StartRecover(bad, "2026-01-01T00:00:00Z", "")
		require.Error(t, err, "%q", bad)
		assert.NotContains(t, err.Error(), "connection")
	}
}

func TestCreateEventType_Body(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/event-types", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"name":"order.created","description":"d","group":"orders","schema":{"type":"object"},"sample_payload":{"a":1},"deprecated":true}`, string(raw))
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "order.created"}})
	}))
	defer server.Close()

	et, err := NewClient(server.URL, "k").CreateEventType(CreateEventTypeRequest{
		Name: "order.created", Description: "d", Group: "orders",
		Schema: json.RawMessage(`{"type":"object"}`), SamplePayload: json.RawMessage(`{"a":1}`), Deprecated: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "order.created", et.Name)
}

func TestCreateEventType_NameOnlyOmitsOptionalFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"name":"x"}`, string(raw))
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "x"}})
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").CreateEventType(CreateEventTypeRequest{Name: "x"})
	require.NoError(t, err)
}

func TestArchiveEventType_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/v1/event-types/order.created", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "order.created", "archived_at": "2026-01-01T00:00:00Z"}})
	}))
	defer server.Close()

	et, err := NewClient(server.URL, "k").ArchiveEventType("order.created")
	require.NoError(t, err)
	assert.Equal(t, "2026-01-01T00:00:00Z", *et.ArchivedAt)
}

func TestEventTypeRoutes_PlainText404_MeansFeatureDisabled(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := NewClient(server.URL, "k").ListEventTypes(ListEventTypesParams{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEventTypesDisabled))
	assert.Contains(t, err.Error(), "event types are not enabled for this project")
}

func TestEventTypeRoutes_JSON404_IsNormalNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "EVENT_TYPE_NOT_FOUND", "message": "Event type not found"}})
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").GetEventType("nope")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrEventTypesDisabled))
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "EVENT_TYPE_NOT_FOUND", apiErr.Code)
}

func TestSendWebhook_UsesSendURLAndBody(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("send must not hit the API host")
	}))
	defer apiServer.Close()
	sendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/webhooks/send", r.URL.Path)
		assert.Equal(t, "Bearer k", r.Header.Get("Authorization"))
		raw, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"endpoint_id":"ep_1","event_type":"order.created","payload":{"a":1},"idempotency_key":"key-1"}`, string(raw))
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"event_id": "ev1", "event_type": "order.created", "status": "accepted", "message_id": "m1",
		}})
	}))
	defer sendServer.Close()

	res, err := NewClient(apiServer.URL, "k").WithSendURL(sendServer.URL).SendWebhook(SendRequest{
		EndpointID: "ep_1", EventType: "order.created", Payload: json.RawMessage(`{"a":1}`), IdempotencyKey: "key-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "ev1", res.EventID)
	assert.Equal(t, "accepted", res.Status)
	assert.Equal(t, "m1", res.MessageID)
}

func TestSendWebhook_EndpointOnly_OmitsEventType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"endpoint_id":"ep_1","payload":{}}`, string(raw))
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"message_id": "m1", "status": "queued"}})
	}))
	defer server.Close()

	res, err := NewClient(server.URL, "k").SendWebhook(SendRequest{EndpointID: "ep_1", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.Equal(t, "m1", res.MessageID)
	assert.Equal(t, "", res.EventID)
}

func TestSendWebhook_409And429Surface(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{409, "IDEMPOTENCY_MISMATCH"}, {429, "EVENT_TYPE_LIMIT_REACHED"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": tc.code, "message": "msg " + tc.code}})
		}))
		_, err := NewClient(server.URL, "k").SendWebhook(SendRequest{EventType: "x", Payload: json.RawMessage(`{}`)})
		server.Close()
		var apiErr *APIError
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, tc.code, apiErr.Code)
	}
}

func TestListEvents_QueryAndDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/events", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "order.created", q.Get("event_type"))
		assert.Equal(t, "2026-01-01T00:00:00Z", q.Get("since"))
		assert.Equal(t, "2026-01-02T00:00:00Z", q.Get("until"))
		assert.Equal(t, "10", q.Get("limit"))
		assert.Equal(t, "c", q.Get("cursor"))
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "ev1", "event_type": "order.created", "fanout_status": "done", "matched_count": 3, "created_at": "2026-01-01T01:00:00Z"}},
			"meta": map[string]any{"has_more": true, "next_cursor": "n"},
		})
	}))
	defer server.Close()

	page, err := NewClient(server.URL, "k").ListEvents(ListEventsParams{
		EventType: "order.created", Since: "2026-01-01T00:00:00Z", Until: "2026-01-02T00:00:00Z", Limit: 10, Cursor: "c",
	})
	require.NoError(t, err)
	require.Len(t, page.Data, 1)
	assert.Equal(t, 3, page.Data[0].MatchedCount)
	assert.Equal(t, "n", page.NextCursor)
}

func TestStartRecover_RequestAndEscape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/endpoints/ep%2F1/recover", r.URL.EscapedPath())
		raw, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"since":"2026-01-01T00:00:00Z"}`, string(raw))
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"recover_job_id": "j1", "status": "pending"}})
	}))
	defer server.Close()

	job, err := NewClient(server.URL, "k").StartRecover("ep/1", "2026-01-01T00:00:00Z", "")
	require.NoError(t, err)
	assert.Equal(t, "j1", job.RecoverJobID)
	assert.Equal(t, "pending", job.Status)
}

func TestStartRecover_409Surfaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "RECOVER_JOB_ACTIVE", "message": "A recover job is already active"}})
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").StartRecover("ep_1", "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z")
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "RECOVER_JOB_ACTIVE", apiErr.Code)
}

func TestGetRecover_PathAndDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/endpoints/ep_1/recover/j%2F1", r.URL.EscapedPath())
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"recover_job_id": "j/1", "endpoint_id": "ep_1", "status": "done", "created_count": 4, "requeued_count": 2,
			"since": "2026-01-01T00:00:00Z", "until": "2026-01-02T00:00:00Z", "finished_at": "2026-01-02T00:01:00Z",
		}})
	}))
	defer server.Close()

	job, err := NewClient(server.URL, "k").GetRecover("ep_1", "j/1")
	require.NoError(t, err)
	assert.Equal(t, 4, job.CreatedCount)
	assert.Equal(t, 2, job.RequeuedCount)
}

func TestSendTestEvent_Body(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/endpoints/ep_1/test", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"event_type":"order.created","payload":{"a":1}}`, string(raw))
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"message_id": "m1", "status": "queued", "event_type": "order.created", "is_test": true}})
	}))
	defer server.Close()

	res, err := NewClient(server.URL, "k").SendTestEvent("ep_1", "order.created", json.RawMessage(`{"a":1}`))
	require.NoError(t, err)
	assert.Equal(t, "m1", res.MessageID)
	assert.True(t, res.IsTest)
}

func TestSendTestEvent_NoPayloadOmitsField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"event_type":"order.created"}`, string(raw))
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"message_id": "m1"}})
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").SendTestEvent("ep_1", "order.created", nil)
	require.NoError(t, err)
}

func TestSendTestEvent_429SurfacesMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "RATE_LIMITED", "message": "Too many test events"}})
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").SendTestEvent("ep_1", "x", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Too many test events")
}

func TestAPIError_StripsControlCharacters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("{\"error\":{\"code\":\"BAD\\u001b[2J\",\"message\":\"oops \\u001b[31mred\\u001b[0m \xc2\x9b0c\\nline\"}}"))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "k").GetEventType("x")
	require.Error(t, err)
	msg := err.Error()
	assert.NotContains(t, msg, "\x1b")
	assert.NotContains(t, msg, "\u009b")
	assert.NotContains(t, msg, "\n")
	assert.Contains(t, msg, "oops")
	assert.Contains(t, msg, "red")
}
