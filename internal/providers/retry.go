package providers

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxRetryAfter caps how long a server may ask us to wait before a retry. A
// longer Retry-After fails at once: the review would stall past any useful
// point, and the caller is better served by the error.
const maxRetryAfter = 60 * time.Second

type rateLimitError struct {
	// retryAfter is the wait the server asked for (0 when it gave none).
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	if e.retryAfter > 0 {
		return fmt.Sprintf("rate limited (retry after %s)", e.retryAfter)
	}
	return "rate limited"
}

type serverError struct {
	statusCode int
	body       string
	// retryAfter is the wait the server asked for (0 when it gave none).
	retryAfter time.Duration
}

func (e *serverError) Error() string {
	return "server error: " + e.body
}

type authError struct {
	message string
}

func (e *authError) Error() string {
	return "authentication error: " + e.message
}

// IsAuthError checks if an error is, or wraps, an authentication error.
func IsAuthError(err error) bool {
	var ae *authError
	return errors.As(err, &ae)
}

// newRateLimitError is the error for a 429 response with header h.
func newRateLimitError(h http.Header) *rateLimitError {
	return &rateLimitError{retryAfter: parseRetryAfter(h, time.Now())}
}

// newServerError is the error for a 5xx response with header h and body.
func newServerError(status int, h http.Header, body string) *serverError {
	return &serverError{statusCode: status, body: body, retryAfter: parseRetryAfter(h, time.Now())}
}

// parseRetryAfter reads how long the server asked us to wait: retry-after-ms
// (milliseconds, sent by OpenAI and Anthropic) or Retry-After (seconds, or an
// HTTP date). It returns 0 when neither is present or usable, including a
// date already past.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After-Ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
		return 0
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func isRetryable(err error) bool {
	switch err.(type) {
	case *rateLimitError:
		return true
	case *serverError:
		return true
	default:
		return false
	}
}

// retryDelay is how long to wait before retry number attempt+1 after err. A
// wait the server asked for is used as given, with a little jitter so
// concurrent chunks told the same thing do not all return at once. Otherwise
// the backoff doubles from 1s, and starts at 2s for a rate limit, which needs
// time to refill rather than a quick second try. ok is false when the server
// asked for longer than maxRetryAfter.
func retryDelay(attempt int, err error) (d time.Duration, ok bool) {
	var asked time.Duration
	switch e := err.(type) {
	case *rateLimitError:
		asked = e.retryAfter
	case *serverError:
		asked = e.retryAfter
	}
	if asked > maxRetryAfter {
		return 0, false
	}
	if asked > 0 {
		return asked + time.Duration(float64(asked)*0.25*rand.Float64()), true
	}

	base := time.Duration(1<<uint(attempt)) * time.Second
	if _, limited := err.(*rateLimitError); limited {
		base *= 2
	}
	// Add jitter: 50-150% of base to avoid thundering herd
	return time.Duration(float64(base) * (0.5 + rand.Float64())), true
}

// sleep waits for d or until ctx is done. Tests replace it to skip the wait.
var sleep = func(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func retryWithBackoff(ctx context.Context, maxRetries int, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		// Don't retry auth errors
		if _, ok := lastErr.(*authError); ok {
			return lastErr
		}

		// Only retry retryable errors (rate limit, server errors)
		if !isRetryable(lastErr) {
			return lastErr
		}

		if attempt < maxRetries {
			d, ok := retryDelay(attempt, lastErr)
			if !ok {
				return lastErr
			}
			if err := sleep(ctx, d); err != nil {
				return err
			}
		}
	}
	return lastErr
}
