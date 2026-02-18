package providers

import (
	"context"
	"time"
)

type rateLimitError struct {
	retryable bool
}

func (e *rateLimitError) Error() string { return "rate limited" }

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

		// Only retry rate limit errors
		if _, ok := lastErr.(*rateLimitError); !ok {
			return lastErr
		}

		if attempt < maxRetries {
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return lastErr
}
