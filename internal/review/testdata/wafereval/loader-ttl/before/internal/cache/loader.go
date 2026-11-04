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

// Loader caches values fetched from a slower store.
type Loader struct {
	mu    sync.RWMutex
	items map[string]*Item
	fetch func(ctx context.Context, key string) (*Item, error)
}

// NewLoader returns a loader that fetches misses with fetch.
func NewLoader(fetch func(ctx context.Context, key string) (*Item, error)) *Loader {
	return &Loader{items: map[string]*Item{}, fetch: fetch}
}

// Get returns key's value, fetching it on a miss.
func (l *Loader) Get(ctx context.Context, key string) (*Item, error) {
	l.mu.RLock()
	item, ok := l.items[key]
	l.mu.RUnlock()
	if ok {
		return item, nil
	}
	item, err := l.fetch(ctx, key)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.items[key] = item
	l.mu.Unlock()
	return item, nil
}
