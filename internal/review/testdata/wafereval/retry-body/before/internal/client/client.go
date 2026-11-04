package client

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"
)

// Client posts JSON documents to the inventory service.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for the service at base.
func New(base string) *Client {
	return &Client{base: base, http: &http.Client{Timeout: 10 * time.Second}}
}

// Post sends payload to path and returns the service's response.
func (c *Client) Post(ctx context.Context, path string, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post %s: %w", path, err)
	}
	return resp, nil
}
