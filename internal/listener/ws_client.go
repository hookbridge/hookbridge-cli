package listener

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hookbridge/hookbridge-cli/internal/api"
)

const (
	defaultBaseBackoff = 1 * time.Second
	maxBackoff         = 30 * time.Second
)

// WSClient manages a WebSocket connection to the HookBridge streaming service.
type WSClient struct {
	streamURL   string
	apiKey      string
	endpointID  string
	conn        *websocket.Conn
	mu          sync.Mutex
	baseBackoff time.Duration
}

// DeliveryResult is sent back to the server after forwarding a webhook locally.
type DeliveryResult struct {
	Type       string `json:"type"`
	MessageID  string `json:"message_id"`
	StatusCode int    `json:"status_code"`
	LatencyMs  int64  `json:"latency_ms"`
}

// NewWSClient creates a new WebSocket client.
func NewWSClient(streamURL, apiKey, endpointID string) *WSClient {
	return &WSClient{
		streamURL:   streamURL,
		apiKey:      apiKey,
		endpointID:  endpointID,
		baseBackoff: defaultBaseBackoff,
	}
}

// Connect establishes a WebSocket connection to the streaming service.
func (c *WSClient) Connect() error {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.apiKey)

	url := c.streamURL + "/v1/listen?endpoint_id=" + c.endpointID

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(url, header)
	if err != nil {
		return fmt.Errorf("WebSocket connect failed: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	return nil
}

// ConnectWithRetry attempts to connect with exponential backoff.
// Returns nil on success, error after maxAttempts failures.
func (c *WSClient) ConnectWithRetry(maxAttempts int) error {
	backoff := c.baseBackoff
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := c.Connect()
		if err == nil {
			return nil
		}

		if attempt == maxAttempts {
			return fmt.Errorf("failed to connect after %d attempts: %w", maxAttempts, err)
		}

		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	return fmt.Errorf("failed to connect after %d attempts", maxAttempts)
}

// ReadMessage reads and deserializes the next webhook message from the WebSocket.
// Blocks until a message is received or the connection is closed.
func (c *WSClient) ReadMessage() (*api.ListenMessage, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("WebSocket read failed: %w", err)
	}

	// Parse the envelope to check message type
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("could not parse message envelope: %w", err)
	}

	// Skip ping/keepalive messages
	if envelope.Type == "ping" {
		return c.ReadMessage()
	}

	var msg api.ListenMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("could not parse webhook message: %w", err)
	}

	return &msg, nil
}

// SendDeliveryResult sends a delivery result back to the server.
func (c *WSClient) SendDeliveryResult(messageID string, statusCode int, latencyMs int64) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	result := DeliveryResult{
		Type:       "delivery_result",
		MessageID:  messageID,
		StatusCode: statusCode,
		LatencyMs:  latencyMs,
	}

	return conn.WriteJSON(result)
}

// Close closes the WebSocket connection.
func (c *WSClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		c.conn.Close()
		c.conn = nil
	}
}

// Connected returns true if the WebSocket connection is established.
func (c *WSClient) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}
