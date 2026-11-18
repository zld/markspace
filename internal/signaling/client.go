package signaling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PhaseError annotates a failure with the negotiation phase it happened in.
// The CLI maps signaling/phase errors to exit code 2.
type PhaseError struct {
	Phase string
	Err   error
}

func (e *PhaseError) Error() string { return fmt.Sprintf("phase %s: %v", e.Phase, e.Err) }
func (e *PhaseError) Unwrap() error { return e.Err }

// Client is one peer's view of the HTTP signaling server.
type Client struct {
	baseURL    string
	sessionID  string
	peer       string
	httpClient *http.Client
	seq        uint64
}

// NewClient builds a client bound to an existing session.
func NewClient(baseURL, sessionID, peer string) *Client {
	return &Client{
		baseURL:    baseURL,
		sessionID:  sessionID,
		peer:       peer,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		seq:        0,
	}
}

// CreateSession asks the server to allocate a fresh session id.
func CreateSession(ctx context.Context, baseURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/session", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", &PhaseError{Phase: "create", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", &PhaseError{Phase: "create", Err: fmt.Errorf("status %d: %s", resp.StatusCode, body)}
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", &PhaseError{Phase: "create", Err: err}
	}
	return out.SessionID, nil
}

// Send posts a message, stamping the sender and the next monotonic seq.
func (c *Client) Send(ctx context.Context, m *Message) error {
	m.From = c.peer
	m.To = otherPeer(c.peer)
	c.seq++
	m.Seq = c.seq
	body, err := json.Marshal(m)
	if err != nil {
		return &PhaseError{Phase: "send", Err: err}
	}
	url := fmt.Sprintf("%s/session/%s", c.baseURL, c.sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &PhaseError{Phase: "send", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &PhaseError{Phase: "send", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return &PhaseError{Phase: "send", Err: fmt.Errorf("status %d: %s", resp.StatusCode, b)}
	}
	return nil
}

// Poll returns all currently queued messages, waiting at most one server tick.
func (c *Client) Poll(ctx context.Context) ([]Message, error) {
	url := fmt.Sprintf("%s/session/%s/%s", c.baseURL, c.sessionID, c.peer)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &PhaseError{Phase: "poll", Err: err}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &PhaseError{Phase: "poll", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, &PhaseError{Phase: "poll", Err: fmt.Errorf("status %d: %s", resp.StatusCode, b)}
	}
	var msgs []Message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, &PhaseError{Phase: "poll", Err: err}
	}
	return msgs, nil
}

// Close deletes the server-side session, releasing its state.
func (c *Client) Close(ctx context.Context) error {
	url := fmt.Sprintf("%s/session/%s", c.baseURL, c.sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// Peer reports the role of this client.
func (c *Client) Peer() string { return c.peer }

func otherPeer(p string) string {
	if p == PeerA {
		return PeerB
	}
	return PeerA
}
