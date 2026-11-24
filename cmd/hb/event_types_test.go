package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hookbridge/hookbridge-cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captured struct {
	method  string
	path    string
	rawPath string
	query   map[string][]string
	body    string
	auth    string
}

func etServer(t *testing.T, status int, resp any) (*captured, *atomic.Int32) {
	t.Helper()
	setupTestHome(t)
	c := &captured{}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		c.method, c.path, c.rawPath, c.query, c.body, c.auth = r.Method, r.URL.Path, r.URL.EscapedPath(), r.URL.Query(), string(raw), r.Header.Get("Authorization")
		if s, ok := resp.(string); ok {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(s))
			return
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	require.NoError(t, config.Save(&config.Config{APIKey: "hb_live_key", ProjectID: "p", APIBaseURL: server.URL}))
	return c, &hits
}

func runHB(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := rootCmd()
	out, errBuf := new(bytes.Buffer), new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(errBuf)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0600))
	return p
}

func apiErrBody(code, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": msg}}
}

// --- event-types list ---

func TestEventTypesList_QueryAndTable(t *testing.T) {
	c, _ := etServer(t, 200, map[string]any{
		"data": []map[string]any{
			{"name": "order.created", "group": "orders", "deprecated": false},
			{"name": "order.old", "group": nil, "deprecated": true},
			{"name": "order.gone", "group": "orders", "archived_at": "2026-01-01T00:00:00Z"},
		},
		"meta": map[string]any{"next_cursor": "NEXT"},
	})

	out, _, err := runHB(t, "", "event-types", "list", "--group", "orders", "--prefix", "order.", "--include-archived", "--limit", "5", "--cursor", "C1")
	require.NoError(t, err)
	assert.Equal(t, "GET", c.method)
	assert.Equal(t, "/v1/event-types", c.path)
	assert.Equal(t, []string{"orders"}, c.query["group"])
	assert.Equal(t, []string{"order."}, c.query["name_prefix"])
	assert.Equal(t, []string{"true"}, c.query["include_archived"])
	assert.Equal(t, []string{"5"}, c.query["limit"])
	assert.Equal(t, []string{"C1"}, c.query["cursor"])
	assert.Equal(t, "Bearer hb_live_key", c.auth)
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "order.created")
	assert.Contains(t, out, "deprecated")
	assert.Contains(t, out, "archived")
	assert.Contains(t, out, "--cursor 'NEXT'")
}

func TestEventTypesList_Empty(t *testing.T) {
	etServer(t, 200, map[string]any{"data": []any{}})
	out, _, err := runHB(t, "", "event-types", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "No event types found")
}

func TestEventTypesList_JSON(t *testing.T) {
	etServer(t, 200, map[string]any{
		"data": []map[string]any{{"name": "order.created"}},
		"meta": map[string]any{"next_cursor": "NEXT"},
	})
	out, _, err := runHB(t, "", "event-types", "list", "--json")
	require.NoError(t, err)
	var got struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "order.created", got.Data[0]["name"])
	assert.Equal(t, "NEXT", got.NextCursor)
}

func TestEventTypesList_JSON_EmptyIsArrayNotNull(t *testing.T) {
	etServer(t, 200, map[string]any{"data": []any{}})
	out, _, err := runHB(t, "", "event-types", "list", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"data":[]`)
}

func TestEventTypesList_FeatureDisabled(t *testing.T) {
	etServer(t, 404, "404 page not found\n")
	_, _, err := runHB(t, "", "event-types", "list")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event types are not enabled for this project")
}

// --- event-types get ---

func TestEventTypesGet_EscapesNameAndPrintsFields(t *testing.T) {
	c, _ := etServer(t, 200, map[string]any{"data": map[string]any{
		"name": "a/b?c", "description": "Sent on order", "group": "orders", "deprecated": true,
		"schema": map[string]any{"type": "object"}, "sample_payload": map[string]any{"id": 1},
		"created_at": "2026-01-01T00:00:00Z",
	}})
	out, _, err := runHB(t, "", "event-types", "get", "a/b?c")
	require.NoError(t, err)
	assert.Equal(t, "GET", c.method)
	assert.Equal(t, "/v1/event-types/a%2Fb%3Fc", c.rawPath)
	assert.Empty(t, c.query)
	assert.Contains(t, out, "Sent on order")
	assert.Contains(t, out, "deprecated")
	assert.Contains(t, out, "\"type\": \"object\"")
	assert.Contains(t, out, "\"id\": 1")
}

func TestEventTypesGet_NotFound(t *testing.T) {
	etServer(t, 404, apiErrBody("EVENT_TYPE_NOT_FOUND", "Event type not found"))
	_, _, err := runHB(t, "", "event-types", "get", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Event type not found")
	assert.NotContains(t, err.Error(), "not enabled")
}

func TestEventTypesGet_JSON(t *testing.T) {
	etServer(t, 200, map[string]any{"data": map[string]any{"name": "order.created", "schema": map[string]any{"type": "object"}}})
	out, _, err := runHB(t, "", "event-types", "get", "order.created", "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "order.created", got["name"])
	assert.Equal(t, map[string]any{"type": "object"}, got["schema"])
}

func TestEventTypesGet_RequiresName(t *testing.T) {
	_, hits := etServer(t, 200, map[string]any{})
	_, _, err := runHB(t, "", "event-types", "get")
	require.Error(t, err)
	assert.Equal(t, int32(0), hits.Load())
}

// --- event-types create ---

func TestEventTypesCreate_SendsFields(t *testing.T) {
	c, _ := etServer(t, 201, map[string]any{"data": map[string]any{"name": "order.created"}})
	schema := writeTemp(t, "schema.json", `{"type":"object"}`)
	sample := writeTemp(t, "sample.json", `{"id":1}`)

	out, _, err := runHB(t, "", "event-types", "create", "order.created",
		"--description", "d", "--group", "orders", "--schema-file", schema, "--sample-file", sample, "--deprecated")
	require.NoError(t, err)
	assert.Equal(t, "POST", c.method)
	assert.Equal(t, "/v1/event-types", c.path)
	assert.JSONEq(t, `{"name":"order.created","description":"d","group":"orders","schema":{"type":"object"},"sample_payload":{"id":1},"deprecated":true}`, c.body)
	assert.Contains(t, out, "Created event type order.created")
}

func TestEventTypesCreate_NameOnly(t *testing.T) {
	c, _ := etServer(t, 201, map[string]any{"data": map[string]any{"name": "x"}})
	_, _, err := runHB(t, "", "event-types", "create", "x")
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"x"}`, c.body)
}

func TestEventTypesCreate_BadSchemaFile_FailsLocally(t *testing.T) {
	_, hits := etServer(t, 201, map[string]any{})
	bad := writeTemp(t, "bad.json", `{not json`)
	_, _, err := runHB(t, "", "event-types", "create", "x", "--schema-file", bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--schema-file")
	assert.Contains(t, err.Error(), "valid JSON")
	assert.Equal(t, int32(0), hits.Load())
}

func TestEventTypesCreate_BadSampleFile_FailsLocally(t *testing.T) {
	_, hits := etServer(t, 201, map[string]any{})
	bad := writeTemp(t, "bad.json", ``)
	_, _, err := runHB(t, "", "event-types", "create", "x", "--sample-file", bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--sample-file")
	assert.Equal(t, int32(0), hits.Load())
}

func TestEventTypesCreate_MissingFile_FailsLocally(t *testing.T) {
	_, hits := etServer(t, 201, map[string]any{})
	_, _, err := runHB(t, "", "event-types", "create", "x", "--schema-file", "/nonexistent/s.json")
	require.Error(t, err)
	assert.Equal(t, int32(0), hits.Load())
}

func TestEventTypesCreate_Conflict(t *testing.T) {
	etServer(t, 409, apiErrBody("EVENT_TYPE_EXISTS", "An event type with this name already exists"))
	_, _, err := runHB(t, "", "event-types", "create", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EVENT_TYPE_EXISTS")
}

func TestEventTypesCreate_LimitReached(t *testing.T) {
	etServer(t, 429, apiErrBody("EVENT_TYPE_LIMIT_REACHED", "Maximum event type limit reached for your plan."))
	_, _, err := runHB(t, "", "event-types", "create", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EVENT_TYPE_LIMIT_REACHED")
}

func TestEventTypesCreate_JSON(t *testing.T) {
	etServer(t, 201, map[string]any{"data": map[string]any{"name": "x"}})
	out, _, err := runHB(t, "", "event-types", "create", "x", "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "x", got["name"])
}

// --- event-types archive ---

func TestEventTypesArchive_ForceDeletes(t *testing.T) {
	c, _ := etServer(t, 200, map[string]any{"data": map[string]any{"name": "a/b", "archived_at": "2026-01-01T00:00:00Z"}})
	out, _, err := runHB(t, "", "event-types", "archive", "a/b", "--force")
	require.NoError(t, err)
	assert.Equal(t, "DELETE", c.method)
	assert.Equal(t, "/v1/event-types/a%2Fb", c.rawPath)
	assert.Contains(t, out, "Archived event type a/b")
}

func TestEventTypesArchive_NonTTY_NoPrompt(t *testing.T) {
	c, _ := etServer(t, 200, map[string]any{"data": map[string]any{"name": "x"}})
	fakeTTY(t, false)
	_, _, err := runHB(t, "", "event-types", "archive", "x")
	require.NoError(t, err)
	assert.Equal(t, "DELETE", c.method)
}

func TestEventTypesArchive_TTY_DeclineAborts(t *testing.T) {
	_, hits := etServer(t, 200, map[string]any{"data": map[string]any{"name": "x"}})
	fakeTTY(t, true)
	_, _, err := runHB(t, "n\n", "event-types", "archive", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aborted")
	assert.Equal(t, int32(0), hits.Load())
}

func TestEventTypesArchive_TTY_ConfirmProceeds(t *testing.T) {
	c, _ := etServer(t, 200, map[string]any{"data": map[string]any{"name": "x"}})
	fakeTTY(t, true)
	_, _, err := runHB(t, "y\n", "event-types", "archive", "x")
	require.NoError(t, err)
	assert.Equal(t, "DELETE", c.method)
}

func TestEventTypesArchive_JSON(t *testing.T) {
	etServer(t, 200, map[string]any{"data": map[string]any{"name": "x", "archived_at": "2026-01-01T00:00:00Z"}})
	out, _, err := runHB(t, "", "event-types", "archive", "x", "--force", "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "2026-01-01T00:00:00Z", got["archived_at"])
}

// --- send ---

func TestSend_TypeAndData(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"event_id": "ev1", "event_type": "order.created", "status": "accepted"}})
	out, _, err := runHB(t, "", "send", "--type", "order.created", "--data", `{"a":1}`, "--idempotency-key", "k-1")
	require.NoError(t, err)
	assert.Equal(t, "POST", c.method)
	assert.Equal(t, "/v1/webhooks/send", c.path)
	assert.JSONEq(t, `{"event_type":"order.created","payload":{"a":1},"idempotency_key":"k-1"}`, c.body)
	assert.Contains(t, out, "ev1")
	assert.Contains(t, out, "accepted")
	assert.NotContains(t, out, "Message ID")
}

func TestSend_WithEndpointPrintsMessageID(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"event_id": "ev1", "status": "accepted", "message_id": "m1"}})
	out, _, err := runHB(t, "", "send", "--type", "t", "--endpoint", "ep_1", "--data", `{}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"event_type":"t","endpoint_id":"ep_1","payload":{}}`, c.body)
	assert.Contains(t, out, "m1")
}

func TestSend_EndpointOnly(t *testing.T) {
	c, _ := etServer(t, 200, map[string]any{"data": map[string]any{"message_id": "m1", "status": "queued"}})
	out, _, err := runHB(t, "", "send", "--endpoint", "ep_1", "--data", `{"x":true}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"endpoint_id":"ep_1","payload":{"x":true}}`, c.body)
	assert.Contains(t, out, "m1")
	assert.Contains(t, out, "queued")
}

func TestSend_DataFile(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"event_id": "ev1"}})
	f := writeTemp(t, "p.json", "{\n \"a\": [1,2]\n}\n")
	_, _, err := runHB(t, "", "send", "--type", "t", "--data-file", f)
	require.NoError(t, err)
	assert.JSONEq(t, `{"event_type":"t","payload":{"a":[1,2]}}`, c.body)
}

func TestSend_DataFromStdin(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"event_id": "ev1"}})
	_, _, err := runHB(t, `{"from":"stdin"}`, "send", "--type", "t", "--data", "-")
	require.NoError(t, err)
	assert.JSONEq(t, `{"event_type":"t","payload":{"from":"stdin"}}`, c.body)
}

func TestSend_ValidationErrors_NoRequest(t *testing.T) {
	_, hits := etServer(t, 202, map[string]any{})
	file := writeTemp(t, "p.json", `{}`)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"neither type nor endpoint", []string{"send", "--data", "{}"}, "--type"},
		{"no data", []string{"send", "--type", "t"}, "--data"},
		{"both data and file", []string{"send", "--type", "t", "--data", "{}", "--data-file", file}, "only one"},
		{"bad json", []string{"send", "--type", "t", "--data", "{oops"}, "valid JSON"},
		{"array payload", []string{"send", "--type", "t", "--data", "[1]"}, "JSON object"},
		{"string payload", []string{"send", "--type", "t", "--data", `"x"`}, "JSON object"},
		{"bad file json", []string{"send", "--type", "t", "--data-file", writeTemp(t, "b.json", "nope")}, "valid JSON"},
	}
	for _, tc := range cases {
		_, _, err := runHB(t, "", tc.args...)
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.want, tc.name)
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestSend_ErrorDoesNotLeakPayloadOrKey(t *testing.T) {
	etServer(t, 400, apiErrBody("INVALID_REQUEST", "bad request"))
	out, stderr, err := runHB(t, "", "send", "--type", "t", "--data", `{"secret":"PAYLOADMARKER"}`)
	require.Error(t, err)
	all := out + stderr + err.Error()
	assert.Contains(t, err.Error(), "INVALID_REQUEST")
	assert.NotContains(t, all, "PAYLOADMARKER")
	assert.NotContains(t, all, "hb_live_key")
}

func TestSend_ConnectionError_DoesNotLeakPayloadOrKey(t *testing.T) {
	setupTestHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()
	require.NoError(t, config.Save(&config.Config{APIKey: "hb_live_key", APIBaseURL: url}))

	out, stderr, err := runHB(t, "", "send", "--type", "t", "--data", `{"secret":"PAYLOADMARKER"}`)
	require.Error(t, err)
	all := out + stderr + err.Error()
	assert.Contains(t, err.Error(), "connection")
	assert.NotContains(t, all, "PAYLOADMARKER")
	assert.NotContains(t, all, "hb_live_key")
}

func TestInputCaps_FailLocally(t *testing.T) {
	_, hits := etServer(t, 202, map[string]any{})
	bigObj := func(n int) string { return `{"a":"` + strings.Repeat("x", n) + `"}` }
	okPayload := bigObj(1<<20 - 100)
	tooBigPayload := bigObj(1 << 20)
	file := func(content string) string { return writeTemp(t, "f.json", content) }

	for _, args := range [][]string{
		{"send", "--type", "t", "--data", tooBigPayload},
		{"send", "--type", "t", "--data-file", file(tooBigPayload)},
		{"endpoints", "test", "ep_1", "--type", "t", "--data", tooBigPayload},
		{"event-types", "create", "x", "--schema-file", file(bigObj(64 << 10))},
		{"event-types", "create", "x", "--sample-file", file(bigObj(64 << 10))},
	} {
		_, _, err := runHB(t, "", args...)
		require.Error(t, err, args)
		assert.Contains(t, err.Error(), "too large", args)
	}
	_, _, err := runHB(t, tooBigPayload, "send", "--type", "t", "--data", "-")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
	assert.Equal(t, int32(0), hits.Load())

	_, _, err = runHB(t, "", "send", "--type", "t", "--data", okPayload)
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load())
}

func TestLimitFlag_RejectsOutOfRange(t *testing.T) {
	_, hits := etServer(t, 200, map[string]any{"data": []any{}})
	for _, args := range [][]string{
		{"event-types", "list", "--limit", "0"},
		{"event-types", "list", "--limit", "1001"},
		{"event-types", "list", "--limit", "-1"},
		{"events", "list", "--limit", "0"},
		{"events", "list", "--limit", "501"},
	} {
		_, _, err := runHB(t, "", args...)
		require.Error(t, err, args)
		assert.Contains(t, err.Error(), "--limit must be between 1 and", args)
	}
	assert.Equal(t, int32(0), hits.Load())

	_, _, err := runHB(t, "", "event-types", "list", "--limit", "1000")
	require.NoError(t, err)
	_, _, err = runHB(t, "", "events", "list", "--limit", "500")
	require.NoError(t, err)
}

func TestCursorHint_IsShellQuoted(t *testing.T) {
	etServer(t, 200, map[string]any{
		"data": []map[string]any{{"name": "a"}},
		"meta": map[string]any{"next_cursor": "it's $(x)"},
	})
	out, _, err := runHB(t, "", "event-types", "list")
	require.NoError(t, err)
	assert.Contains(t, out, `--cursor 'it'\''s $(x)'`)
}

func TestHumanOutput_StripsControlCharsFromServerText(t *testing.T) {
	etServer(t, 200, map[string]any{
		"data": []map[string]any{{"name": "evil\x1b[31mname", "group": "g\rx", "description": "d\x07"}},
		"meta": map[string]any{"next_cursor": "c\x1b]0;t\x07"},
	})
	out, _, err := runHB(t, "", "event-types", "list")
	require.NoError(t, err)
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\r")
	assert.NotContains(t, out, "\x07")
	assert.Contains(t, out, "evil")
}

func TestEventTypesGet_StripsControlCharsFromDescription(t *testing.T) {
	etServer(t, 200, map[string]any{"data": map[string]any{"name": "n", "description": "ok\x1b[2Jbad"}})
	out, _, err := runHB(t, "", "event-types", "get", "n")
	require.NoError(t, err)
	assert.NotContains(t, out, "\x1b")
	assert.Contains(t, out, "ok")
}

func TestSend_Conflict409(t *testing.T) {
	etServer(t, 409, apiErrBody("IDEMPOTENCY_MISMATCH", "Idempotency key already used with different payload"))
	_, _, err := runHB(t, "", "send", "--type", "t", "--data", "{}", "--idempotency-key", "k")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IDEMPOTENCY_MISMATCH")
}

func TestSend_LimitReached429(t *testing.T) {
	etServer(t, 429, map[string]any{"error": map[string]any{
		"code": "EVENT_TYPE_LIMIT_REACHED", "message": "Maximum event type limit reached for your plan.",
		"details": map[string]any{"limit": 1000},
	}})
	_, _, err := runHB(t, "", "send", "--type", "t", "--data", "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EVENT_TYPE_LIMIT_REACHED")
	assert.Contains(t, err.Error(), "Maximum event type limit")
}

func TestSend_JSON(t *testing.T) {
	etServer(t, 202, map[string]any{"data": map[string]any{"event_id": "ev1", "event_type": "t", "status": "accepted", "message_id": "m1"}})
	out, _, err := runHB(t, "", "send", "--type", "t", "--data", "{}", "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "ev1", got["event_id"])
	assert.Equal(t, "m1", got["message_id"])
}

func TestSend_UsesSendURLFromConfig(t *testing.T) {
	setupTestHome(t)
	var apiHits, sendHits atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { apiHits.Add(1) }))
	defer apiServer.Close()
	sendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendHits.Add(1)
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"event_id": "ev1"}})
	}))
	defer sendServer.Close()
	require.NoError(t, config.Save(&config.Config{APIKey: "k", APIBaseURL: apiServer.URL, SendURL: sendServer.URL}))

	_, _, err := runHB(t, "", "send", "--type", "t", "--data", "{}")
	require.NoError(t, err)
	assert.Equal(t, int32(1), sendHits.Load())
	assert.Equal(t, int32(0), apiHits.Load())
}

// --- events list ---

func TestEventsList_QueryAndTable(t *testing.T) {
	c, _ := etServer(t, 200, map[string]any{
		"data": []map[string]any{{"id": "ev1", "event_type": "order.created", "fanout_status": "done", "matched_count": 3, "created_at": "2026-01-01T01:00:00Z"}},
		"meta": map[string]any{"has_more": true, "next_cursor": "NEXT"},
	})
	out, _, err := runHB(t, "", "events", "list", "--type", "order.created", "--since", "2026-01-01T00:00:00Z", "--until", "2026-01-02T00:00:00+02:00", "--limit", "10", "--cursor", "C")
	require.NoError(t, err)
	assert.Equal(t, "GET", c.method)
	assert.Equal(t, "/v1/events", c.path)
	assert.Equal(t, []string{"order.created"}, c.query["event_type"])
	assert.Equal(t, []string{"2026-01-01T00:00:00Z"}, c.query["since"])
	assert.Equal(t, []string{"2026-01-02T00:00:00+02:00"}, c.query["until"])
	assert.Equal(t, []string{"10"}, c.query["limit"])
	assert.Equal(t, []string{"C"}, c.query["cursor"])
	assert.Contains(t, out, "ev1")
	assert.Contains(t, out, "done")
	assert.Contains(t, out, "--cursor 'NEXT'")
}

func TestEventsList_BadTimes_FailLocally(t *testing.T) {
	_, hits := etServer(t, 200, map[string]any{"data": []any{}})
	for _, args := range [][]string{
		{"events", "list", "--since", "yesterday"},
		{"events", "list", "--until", "2026-01-01"},
		{"events", "list", "--since", "2026-01-02T00:00:00Z", "--until", "2026-01-01T00:00:00Z"},
	} {
		_, _, err := runHB(t, "", args...)
		require.Error(t, err, args)
		assert.Regexp(t, "RFC3339|after", err.Error())
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestEventsList_JSON(t *testing.T) {
	etServer(t, 200, map[string]any{
		"data": []map[string]any{{"id": "ev1", "status_counts": map[string]any{"succeeded": 2}}},
		"meta": map[string]any{"next_cursor": "N"},
	})
	out, _, err := runHB(t, "", "events", "list", "--json")
	require.NoError(t, err)
	var got struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "ev1", got.Data[0]["id"])
	assert.Equal(t, "N", got.NextCursor)
}

func TestEventsList_FeatureDisabled(t *testing.T) {
	etServer(t, 404, "404 page not found\n")
	_, _, err := runHB(t, "", "events", "list")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")
}

// --- endpoints recover ---

func TestEndpointsRecover_Start(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"recover_job_id": "job1", "status": "pending"}})
	out, _, err := runHB(t, "", "endpoints", "recover", "ep_1", "--since", "2026-01-01T00:00:00Z", "--until", "2026-01-02T00:00:00Z")
	require.NoError(t, err)
	assert.Equal(t, "POST", c.method)
	assert.Equal(t, "/v1/endpoints/ep_1/recover", c.path)
	assert.JSONEq(t, `{"since":"2026-01-01T00:00:00Z","until":"2026-01-02T00:00:00Z"}`, c.body)
	assert.Contains(t, out, "job1")
	assert.Contains(t, out, "--job 'job1'")
}

func TestEndpointsRecover_SinceOnly(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"recover_job_id": "job1"}})
	_, _, err := runHB(t, "", "endpoints", "recover", "ep_1", "--since", "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	assert.JSONEq(t, `{"since":"2026-01-01T00:00:00Z"}`, c.body)
}

func TestEndpointsRecover_EscapesID(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"recover_job_id": "job1"}})
	_, _, err := runHB(t, "", "endpoints", "recover", "ep/1?x", "--since", "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	assert.Equal(t, "/v1/endpoints/ep%2F1%3Fx/recover", c.rawPath)
}

func TestEndpointsRecover_Status(t *testing.T) {
	c, _ := etServer(t, 200, map[string]any{"data": map[string]any{
		"recover_job_id": "job1", "endpoint_id": "ep_1", "status": "done", "created_count": 4, "requeued_count": 2,
		"since": "2026-01-01T00:00:00Z", "until": "2026-01-02T00:00:00Z",
	}})
	out, _, err := runHB(t, "", "endpoints", "recover", "ep_1", "--job", "job1")
	require.NoError(t, err)
	assert.Equal(t, "GET", c.method)
	assert.Equal(t, "/v1/endpoints/ep_1/recover/job1", c.path)
	assert.Contains(t, out, "done")
	assert.Contains(t, out, "Created:  4")
	assert.Contains(t, out, "Requeued: 2")
}

func TestEndpointsRecover_JSON(t *testing.T) {
	etServer(t, 202, map[string]any{"data": map[string]any{"recover_job_id": "job1", "status": "pending"}})
	out, _, err := runHB(t, "", "endpoints", "recover", "ep_1", "--since", "2026-01-01T00:00:00Z", "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "job1", got["recover_job_id"])
}

func TestEndpointsRecover_ValidationErrors_NoRequest(t *testing.T) {
	_, hits := etServer(t, 202, map[string]any{})
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"neither", []string{"endpoints", "recover", "ep_1"}, "--since"},
		{"bad since", []string{"endpoints", "recover", "ep_1", "--since", "nope"}, "RFC3339"},
		{"bad until", []string{"endpoints", "recover", "ep_1", "--since", "2026-01-01T00:00:00Z", "--until", "2026-01-01"}, "RFC3339"},
		{"until before since", []string{"endpoints", "recover", "ep_1", "--since", "2026-01-02T00:00:00Z", "--until", "2026-01-01T00:00:00Z"}, "after"},
		{"job with since", []string{"endpoints", "recover", "ep_1", "--job", "j", "--since", "2026-01-01T00:00:00Z"}, "--job"},
		{"no id", []string{"endpoints", "recover", "--since", "2026-01-01T00:00:00Z"}, "arg"},
	}
	for _, tc := range cases {
		_, _, err := runHB(t, "", tc.args...)
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.want, tc.name)
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestEndpointsRecover_ActiveJob409(t *testing.T) {
	etServer(t, 409, apiErrBody("RECOVER_JOB_ACTIVE", "A recover job is already active"))
	_, _, err := runHB(t, "", "endpoints", "recover", "ep_1", "--since", "2026-01-01T00:00:00Z")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RECOVER_JOB_ACTIVE")
}

func TestEndpointsRecover_FeatureDisabled(t *testing.T) {
	etServer(t, 404, "404 page not found\n")
	_, _, err := runHB(t, "", "endpoints", "recover", "ep_1", "--since", "2026-01-01T00:00:00Z")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event types are not enabled for this project")
}

func TestEndpointsRecover_HelpMentionsOutbound(t *testing.T) {
	out, _, err := runHB(t, "", "endpoints", "recover", "--help")
	require.NoError(t, err)
	assert.Contains(t, strings.ToLower(out), "outbound")
}

// --- endpoints test ---

func TestEndpointsTest_SendsTypeAndPayload(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"message_id": "m1", "status": "queued", "event_type": "order.created", "is_test": true}})
	out, _, err := runHB(t, "", "endpoints", "test", "ep_1", "--type", "order.created", "--data", `{"a":1}`)
	require.NoError(t, err)
	assert.Equal(t, "POST", c.method)
	assert.Equal(t, "/v1/endpoints/ep_1/test", c.path)
	assert.JSONEq(t, `{"event_type":"order.created","payload":{"a":1}}`, c.body)
	assert.Contains(t, out, "m1")
	assert.Contains(t, out, "queued")
}

func TestEndpointsTest_NoPayload_UsesSample(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"message_id": "m1"}})
	_, _, err := runHB(t, "", "endpoints", "test", "ep_1", "--type", "order.created")
	require.NoError(t, err)
	assert.JSONEq(t, `{"event_type":"order.created"}`, c.body)
}

func TestEndpointsTest_DataFile(t *testing.T) {
	c, _ := etServer(t, 202, map[string]any{"data": map[string]any{"message_id": "m1"}})
	f := writeTemp(t, "p.json", `{"b":2}`)
	_, _, err := runHB(t, "", "endpoints", "test", "ep_1", "--type", "t", "--data-file", f)
	require.NoError(t, err)
	assert.JSONEq(t, `{"event_type":"t","payload":{"b":2}}`, c.body)
}

func TestEndpointsTest_ValidationErrors_NoRequest(t *testing.T) {
	_, hits := etServer(t, 202, map[string]any{})
	file := writeTemp(t, "p.json", `{}`)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing type", []string{"endpoints", "test", "ep_1"}, "--type"},
		{"bad json", []string{"endpoints", "test", "ep_1", "--type", "t", "--data", "{x"}, "valid JSON"},
		{"non-object", []string{"endpoints", "test", "ep_1", "--type", "t", "--data", "5"}, "JSON object"},
		{"both", []string{"endpoints", "test", "ep_1", "--type", "t", "--data", "{}", "--data-file", file}, "only one"},
	}
	for _, tc := range cases {
		_, _, err := runHB(t, "", tc.args...)
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.want, tc.name)
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestEndpointsTest_RateLimited429(t *testing.T) {
	etServer(t, 429, apiErrBody("RATE_LIMITED", "More than 10 test events per minute"))
	_, _, err := runHB(t, "", "endpoints", "test", "ep_1", "--type", "t")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "More than 10 test events per minute")
}

func TestEndpointsTest_NotFound(t *testing.T) {
	etServer(t, 404, apiErrBody("ENDPOINT_NOT_FOUND", "Endpoint not found"))
	_, _, err := runHB(t, "", "endpoints", "test", "ep_1", "--type", "t")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ENDPOINT_NOT_FOUND")
}

func TestEndpointsTest_FeatureDisabled(t *testing.T) {
	etServer(t, 404, "404 page not found\n")
	_, _, err := runHB(t, "", "endpoints", "test", "ep_1", "--type", "t")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")
}

func TestEndpointsTest_JSON(t *testing.T) {
	etServer(t, 202, map[string]any{"data": map[string]any{"message_id": "m1", "is_test": true}})
	out, _, err := runHB(t, "", "endpoints", "test", "ep_1", "--type", "t", "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "m1", got["message_id"])
	assert.Equal(t, true, got["is_test"])
}

func TestEndpointsTest_HelpMentionsOutbound(t *testing.T) {
	out, _, err := runHB(t, "", "endpoints", "test", "--help")
	require.NoError(t, err)
	assert.Contains(t, strings.ToLower(out), "outbound")
}

func TestEventTypesGet_EscapesC1InPrettyJSON(t *testing.T) {
	etServer(t, 200, "{\"data\":{\"name\":\"n\",\"schema\":{\"title\":\"a\xc2\x9bb\"},\"sample_payload\":{\"k\":\"\xc2\x85x\"}}}")
	out, _, err := runHB(t, "", "event-types", "get", "n")
	require.NoError(t, err)
	assert.NotContains(t, out, "\u009b")
	assert.NotContains(t, out, "\u0085")
	assert.Contains(t, out, `"title": "a\u009bb"`)
	assert.Contains(t, out, `"k": "\u0085x"`)
}
