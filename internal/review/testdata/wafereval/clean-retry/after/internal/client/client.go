package client

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"
)

// Client stores JSON documents in the inventory service.
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

// Put stores payload at path and returns the service's response, retrying
// transport errors and 5xx answers. PUT is idempotent, so a request the
// server may already have applied is safe to send again.
func (c *Client) Put(ctx context.Context, path string, payload []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		// A request's body is read when it is sent, so each attempt
		// builds a new one.
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}
		if err == nil {
			_ = resp.Body.Close()
			err = fmt.Errorf("put %s: server error %s", path, resp.Status)
		} else {
			err = fmt.Errorf("put %s: %w", path, err)
		}
		if attempt >= c.maxRetries {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.backoff(attempt + 1)):
		}
	}
}
