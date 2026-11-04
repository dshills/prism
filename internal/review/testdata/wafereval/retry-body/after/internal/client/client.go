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
	base       string
	http       *http.Client
	maxRetries int
}

// New returns a client for the service at base. Requests that fail or get a
// 5xx answer are retried up to three times.
func New(base string) *Client {
	return &Client{base: base, http: &http.Client{Timeout: 10 * time.Second}, maxRetries: 3}
}

// backoff is the wait before the given retry: 100ms, 200ms, 400ms, ...
func (c *Client) backoff(attempt int) time.Duration {
	return 100 * time.Millisecond << (attempt - 1)
}

// Post sends payload to path and returns the service's response, retrying
// transient failures.
func (c *Client) Post(ctx context.Context, path string, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(c.backoff(attempt))
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("post %s: %w", path, err)
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("post %s: server error %s", path, resp.Status)
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}
