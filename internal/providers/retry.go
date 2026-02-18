package providers

import (
	"context"
	"math/rand"
	"time"
)

type rateLimitError struct{}

func (e *rateLimitError) Error() string { return "rate limited" }

type serverError struct {
	statusCode int
	body       string
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

// IsAuthError checks if an error is an authentication error.
func IsAuthError(err error) bool {
	_, ok := err.(*authError)
	return ok
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
			base := time.Duration(1<<uint(attempt)) * time.Second
			// Add jitter: 50-150% of base to avoid thundering herd
			jitter := time.Duration(float64(base) * (0.5 + rand.Float64()))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(jitter):
			}
		}
	}
	return lastErr
}
