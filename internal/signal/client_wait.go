package signal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// AwaitOffer long-polls for an offer on one link.
func (c *Client) AwaitOffer(ctx context.Context, id int64, link string) (string, error) {
	return c.wait(ctx, id, link, "offer")
}

// AwaitAnswer long-polls for an answer on one link.
func (c *Client) AwaitAnswer(ctx context.Context, id int64, link string) (string, error) {
	return c.wait(ctx, id, link, "answer")
}

func (c *Client) wait(ctx context.Context, id int64, link, kind string) (string, error) {
	url := fmt.Sprintf("%s%s%s?request_id=%d&kind=%s", c.base, PathWaitPrefix, link, id, kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", decodeAPIError(resp.StatusCode, body)
	}
	var desc Description
	if err := json.Unmarshal(body, &desc); err != nil {
		return "", err
	}
	if desc.SDP == "" {
		return "", ErrEmptySDP
	}
	return desc.SDP, nil
}
