package cache

import (
	"context"
	"sync"
	"time"
)

// Item is a cached value.
type Item struct {
	Value     []byte
	FetchedAt time.Time
}

func (i *Item) expired(now time.Time, ttl time.Duration) bool {
	return now.Sub(i.FetchedAt) > ttl
}

// Loader caches values fetched from a slower store for ttl.
type Loader struct {
	mu    sync.RWMutex
	items map[string]*Item
	ttl   time.Duration
	fetch func(ctx context.Context, key string) (*Item, error)
	now   func() time.Time
}

// NewLoader returns a loader that fetches misses with fetch and keeps each
// value for ttl.
func NewLoader(ttl time.Duration, fetch func(ctx context.Context, key string) (*Item, error)) *Loader {
	return &Loader{items: map[string]*Item{}, ttl: ttl, fetch: fetch, now: time.Now}
}

// Get returns key's value, fetching it again on a miss or once it has
// expired.
func (l *Loader) Get(ctx context.Context, key string) (*Item, error) {
	l.mu.RLock()
	item, ok := l.items[key]
	l.mu.RUnlock()
	if !ok || item.expired(l.now(), l.ttl) {
		item, err := l.fetch(ctx, key)
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		l.items[key] = item
		l.mu.Unlock()
	}
	return item, nil
}
