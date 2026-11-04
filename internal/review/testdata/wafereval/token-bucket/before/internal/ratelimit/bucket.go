package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a token bucket: it holds up to capacity tokens and refills at
// rate tokens per second. Each allowed request takes one token.
type Bucket struct {
	mu       sync.Mutex
	capacity float64
	tokens   float64
	rate     float64
	last     time.Time
	now      func() time.Time
}

// New returns a full bucket.
func New(capacity int, ratePerSecond float64) *Bucket {
	return &Bucket{
		capacity: float64(capacity),
		tokens:   float64(capacity),
		rate:     ratePerSecond,
		last:     time.Now(),
		now:      time.Now,
	}
}

// Allow reports whether a request may proceed now, taking a token if so.
func (b *Bucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
