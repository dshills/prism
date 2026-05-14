// Package ratelimit provides a token-bucket rate limiter for LLM API calls.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Limiter is a token-bucket rate limiter.
// The zero value and a Limiter with unlimited=true both allow unlimited throughput.
type Limiter struct {
	unlimited bool
	mu        sync.Mutex
	rate      float64 // tokens per nanosecond
	burst     float64 // bucket capacity
	avail     float64 // current token count
	last      time.Time
}

// New creates a Limiter capped at rpm requests per minute. An initial burst
// of up to 10 tokens is pre-loaded so the first requests proceed immediately.
// When rpm ≤ 0 a no-op Limiter is returned (unlimited throughput).
func New(rpm int) *Limiter {
	if rpm <= 0 {
		return &Limiter{unlimited: true}
	}
	burst := float64(rpm)
	if burst > 10 {
		burst = 10
	}
	return &Limiter{
		rate:  float64(rpm) / float64(time.Minute),
		burst: burst,
		avail: burst,
		last:  time.Now(),
	}
}

// Wait blocks until a token is available or ctx is cancelled.
// A nil Limiter, an unlimited Limiter, or the zero value all return immediately.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil || l.unlimited || l.rate == 0 {
		return nil
	}
	// l.mu serializes all state reads and writes (avail, last).
	// The lock is released before the timer wait so concurrent callers
	// can refill the bucket independently.
	for {
		l.mu.Lock()
		now := time.Now()
		l.avail += float64(now.Sub(l.last)) * l.rate
		if l.avail > l.burst {
			l.avail = l.burst
		}
		l.last = now

		if l.avail >= 1.0 {
			l.avail -= 1.0
			l.mu.Unlock()
			return nil
		}

		// Compute wait until the next token. Guard against float precision
		// producing a negative or sub-nanosecond value; enforce a 1ms minimum
		// so time.Duration truncation cannot produce a zero timer that spins.
		need := (1.0 - l.avail) / l.rate
		if need < float64(time.Millisecond) {
			need = float64(time.Millisecond)
		}
		wait := time.Duration(need)
		l.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
