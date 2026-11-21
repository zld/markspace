package signal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to a loopback Server over HTTP.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a client for the server base URL.
func NewClient(base string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{base: base, http: &http.Client{Timeout: timeout}}
}

// Open starts a new signaling round.
func (c *Client) Open(ctx context.Context) (OpenResult, error) {
	var out OpenResult
	err := c.post(ctx, PathOpen, OpenRequest{}, &out)
	return out, err
}

// PutOffer uploads an offer for one link.
func (c *Client) PutOffer(ctx context.Context, id int64, link, sdp string) error {
	return c.post(ctx, PathOffer+link, Description{RequestID: id, Link: link, SDP: sdp}, nil)
}

// PutAnswer uploads an answer for one link.
func (c *Client) PutAnswer(ctx context.Context, id int64, link, sdp string) error {
	return c.post(ctx, PathAnswer+link, Description{RequestID: id, Link: link, SDP: sdp}, nil)
}

// Finish closes a completed round so its deadline stops applying.
func (c *Client) Finish(ctx context.Context, id int64) error {
	return c.post(ctx, PathFinish, OpenRequest{RequestID: id}, nil)
}

// State returns the server-side negotiation state.
func (c *Client) State(ctx context.Context) (Snapshot, error) {
	var out Snapshot
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+PathState, nil)
	if err != nil {
		return out, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, err
	}
	if resp.StatusCode != http.StatusOK {
		return out, decodeAPIError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp.StatusCode, respBody)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

// decodeAPIError reconstructs an APIError and keeps a readable message.
func decodeAPIError(status int, body []byte) error {
	var e APIError
	if err := json.Unmarshal(body, &e); err != nil || e.Code == "" {
		return fmt.Errorf("signaling: HTTP %d: %s", status, string(body))
	}
	e.Status = status
	return &e
}

// Error satisfies the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("signaling %s: %s (HTTP %d)", e.Code, e.Message, e.Status)
}
