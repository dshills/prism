package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a token bucket: it holds up to capacity tokens and refills at
// rate tokens per second. Each allowed request takes one token. Counts are
// integers, so a long-running bucket does not accumulate float error.
type Bucket struct {
	mu       sync.Mutex
	capacity int64
	tokens   int64
	rate     int64
	last     time.Time
	now      func() time.Time
}

// New returns a full bucket.
func New(capacity, ratePerSecond int64) *Bucket {
	return &Bucket{
		capacity: capacity,
		tokens:   capacity,
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
	b.tokens += int64(now.Sub(b.last)/time.Second) * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
