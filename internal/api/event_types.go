package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// ErrEventTypesDisabled is returned when an event-types route answers with a
// plain-text 404, which is how the API reports that the feature is off.
var ErrEventTypesDisabled = errors.New("event types are not enabled for this project")

// WithSendURL points webhook sends at a different host from the rest of the API.
func (c *Client) WithSendURL(sendURL string) *Client {
	c.sendURL = sendURL
	return c
}

func segment(name, value string) (string, error) {
	if value == "" || value == "." || value == ".." {
		return "", fmt.Errorf("invalid %s %q", name, value)
	}
	return url.PathEscape(value), nil
}

func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

func (c *Client) doEventTypesJSON(method, path string, body any, result any) error {
	err := c.doJSON(method, path, body, result)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && apiErr.Code == "" {
		return ErrEventTypesDisabled
	}
	return err
}

type EventType struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Group         *string         `json:"group"`
	Schema        json.RawMessage `json:"schema"`
	SamplePayload json.RawMessage `json:"sample_payload"`
	Deprecated    bool            `json:"deprecated"`
	Public        bool            `json:"public"`
	AutoCreated   bool            `json:"auto_created"`
	ArchivedAt    *string         `json:"archived_at"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

type EventTypePage struct {
	Data       []EventType
	NextCursor string
}

type ListEventTypesParams struct {
	Group           string
	NamePrefix      string
	IncludeArchived bool
	Limit           int
	Cursor          string
}

func (c *Client) ListEventTypes(p ListEventTypesParams) (*EventTypePage, error) {
	q := url.Values{}
	if p.Group != "" {
		q.Set("group", p.Group)
	}
	if p.NamePrefix != "" {
		q.Set("name_prefix", p.NamePrefix)
	}
	if p.IncludeArchived {
		q.Set("include_archived", "true")
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	var resp struct {
		Data []EventType `json:"data"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	if err := c.doEventTypesJSON(http.MethodGet, withQuery("/v1/event-types", q), nil, &resp); err != nil {
		return nil, err
	}
	return &EventTypePage{Data: resp.Data, NextCursor: resp.Meta.NextCursor}, nil
}

func (c *Client) GetEventType(name string) (*EventType, error) {
	seg, err := segment("event type name", name)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data EventType `json:"data"`
	}
	if err := c.doEventTypesJSON(http.MethodGet, "/v1/event-types/"+seg, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

type CreateEventTypeRequest struct {
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Group         string          `json:"group,omitempty"`
	Schema        json.RawMessage `json:"schema,omitempty"`
	SamplePayload json.RawMessage `json:"sample_payload,omitempty"`
	Deprecated    bool            `json:"deprecated,omitempty"`
}

func (c *Client) CreateEventType(req CreateEventTypeRequest) (*EventType, error) {
	var resp struct {
		Data EventType `json:"data"`
	}
	if err := c.doEventTypesJSON(http.MethodPost, "/v1/event-types", req, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

func (c *Client) ArchiveEventType(name string) (*EventType, error) {
	seg, err := segment("event type name", name)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data EventType `json:"data"`
	}
	if err := c.doEventTypesJSON(http.MethodDelete, "/v1/event-types/"+seg, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

type SendRequest struct {
	EndpointID     string          `json:"endpoint_id,omitempty"`
	EventType      string          `json:"event_type,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type SendResult struct {
	EventID   string `json:"event_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
	Status    string `json:"status"`
	MessageID string `json:"message_id,omitempty"`
}

func (c *Client) SendWebhook(req SendRequest) (*SendResult, error) {
	base := c.sendURL
	if base == "" {
		base = c.baseURL
	}
	var resp struct {
		Data SendResult `json:"data"`
	}
	if err := c.doJSONAt(base, http.MethodPost, "/v1/webhooks/send", req, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

type Event struct {
	ID               string         `json:"id"`
	EventType        string         `json:"event_type"`
	FanoutStatus     string         `json:"fanout_status"`
	MatchedCount     int            `json:"matched_count"`
	TargetEndpointID string         `json:"target_endpoint_id,omitempty"`
	StatusCounts     map[string]int `json:"status_counts,omitempty"`
	CreatedAt        string         `json:"created_at"`
}

type EventPage struct {
	Data       []Event
	NextCursor string
}

type ListEventsParams struct {
	EventType string
	Since     string
	Until     string
	Limit     int
	Cursor    string
}

func (c *Client) ListEvents(p ListEventsParams) (*EventPage, error) {
	q := url.Values{}
	if p.EventType != "" {
		q.Set("event_type", p.EventType)
	}
	if p.Since != "" {
		q.Set("since", p.Since)
	}
	if p.Until != "" {
		q.Set("until", p.Until)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	var resp struct {
		Data []Event `json:"data"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	if err := c.doEventTypesJSON(http.MethodGet, withQuery("/v1/events", q), nil, &resp); err != nil {
		return nil, err
	}
	return &EventPage{Data: resp.Data, NextCursor: resp.Meta.NextCursor}, nil
}

type RecoverJob struct {
	RecoverJobID  string `json:"recover_job_id"`
	EndpointID    string `json:"endpoint_id,omitempty"`
	Status        string `json:"status"`
	CreatedCount  int    `json:"created_count"`
	RequeuedCount int    `json:"requeued_count"`
	Since         string `json:"since,omitempty"`
	Until         string `json:"until,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
}

func (c *Client) StartRecover(endpointID, since, until string) (*RecoverJob, error) {
	seg, err := segment("endpoint id", endpointID)
	if err != nil {
		return nil, err
	}
	body := map[string]string{"since": since}
	if until != "" {
		body["until"] = until
	}
	var resp struct {
		Data RecoverJob `json:"data"`
	}
	if err := c.doEventTypesJSON(http.MethodPost, "/v1/endpoints/"+seg+"/recover", body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

func (c *Client) GetRecover(endpointID, jobID string) (*RecoverJob, error) {
	epSeg, err := segment("endpoint id", endpointID)
	if err != nil {
		return nil, err
	}
	jobSeg, err := segment("recover job id", jobID)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data RecoverJob `json:"data"`
	}
	if err := c.doEventTypesJSON(http.MethodGet, "/v1/endpoints/"+epSeg+"/recover/"+jobSeg, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

type TestEventResult struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
	EventType string `json:"event_type"`
	IsTest    bool   `json:"is_test"`
}

func (c *Client) SendTestEvent(endpointID, eventType string, payload json.RawMessage) (*TestEventResult, error) {
	seg, err := segment("endpoint id", endpointID)
	if err != nil {
		return nil, err
	}
	body := struct {
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload,omitempty"`
	}{eventType, payload}
	var resp struct {
		Data TestEventResult `json:"data"`
	}
	if err := c.doEventTypesJSON(http.MethodPost, "/v1/endpoints/"+seg+"/test", body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}
