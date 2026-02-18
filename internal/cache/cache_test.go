package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCache_PutGet(t *testing.T) {
	dir := t.TempDir()
	c, err := New(true, dir, 86400)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	key := "test-key"
	value := `[{"severity":"high","title":"test"}]`

	// Miss before put
	if _, ok := c.Get(key); ok {
		t.Error("Expected cache miss before put")
	}

	// Put
	if err := c.Put(key, value); err != nil {
		t.Fatalf("Put error: %v", err)
	}

	// Hit after put
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("Expected cache hit after put")
	}
	if got != value {
		t.Errorf("Got = %q, want %q", got, value)
	}
}

func TestCache_TTLExpiration(t *testing.T) {
	dir := t.TempDir()
	c, err := New(true, dir, 1) // 1 second TTL
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	key := "expire-test"
	if err := c.Put(key, "data"); err != nil {
		t.Fatalf("Put error: %v", err)
	}

	// Should hit immediately
	if _, ok := c.Get(key); !ok {
		t.Error("Expected cache hit before expiration")
	}

	// Wait for expiration
	time.Sleep(1100 * time.Millisecond)

	// Should miss after TTL
	if _, ok := c.Get(key); ok {
		t.Error("Expected cache miss after TTL expiration")
	}
}

func TestCache_Disabled(t *testing.T) {
	c, err := New(false, "", 0)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if c.Enabled() {
		t.Error("Cache should be disabled")
	}

	// Operations should be no-ops
	if err := c.Put("key", "value"); err != nil {
		t.Errorf("Put on disabled cache should not error: %v", err)
	}
	if _, ok := c.Get("key"); ok {
		t.Error("Get on disabled cache should always miss")
	}
	if err := c.Clear(); err != nil {
		t.Errorf("Clear on disabled cache should not error: %v", err)
	}
}

func TestCache_Clear(t *testing.T) {
	dir := t.TempDir()
	c, err := New(true, dir, 86400)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	// Put some entries
	for i := 0; i < 5; i++ {
		key := string(rune('a' + i))
		if err := c.Put(key, "data"); err != nil {
			t.Fatalf("Put error: %v", err)
		}
	}

	// Verify entries exist
	entries, _ := os.ReadDir(dir)
	jsonCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsonCount++
		}
	}
	if jsonCount != 5 {
		t.Fatalf("Expected 5 cache entries, got %d", jsonCount)
	}

	// Clear
	if err := c.Clear(); err != nil {
		t.Fatalf("Clear error: %v", err)
	}

	// Verify entries are gone
	entries, _ = os.ReadDir(dir)
	jsonCount = 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsonCount++
		}
	}
	if jsonCount != 0 {
		t.Errorf("Expected 0 cache entries after clear, got %d", jsonCount)
	}
}

func TestCache_GetStats(t *testing.T) {
	dir := t.TempDir()
	c, err := New(true, dir, 86400)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	// Empty stats
	stats, err := c.GetStats()
	if err != nil {
		t.Fatalf("GetStats error: %v", err)
	}
	if stats.Entries != 0 {
		t.Errorf("Entries = %d, want 0", stats.Entries)
	}

	// Add entries
	c.Put("key1", "value1")
	c.Put("key2", "value2")

	stats, err = c.GetStats()
	if err != nil {
		t.Fatalf("GetStats error: %v", err)
	}
	if stats.Entries != 2 {
		t.Errorf("Entries = %d, want 2", stats.Entries)
	}
	if stats.TotalBytes <= 0 {
		t.Error("TotalBytes should be > 0")
	}
	if stats.Dir != dir {
		t.Errorf("Dir = %q, want %q", stats.Dir, dir)
	}
}

func TestHashKey(t *testing.T) {
	h1 := HashKey("test")
	h2 := HashKey("test")
	h3 := HashKey("other")

	if h1 != h2 {
		t.Error("Same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("Different input should produce different hash")
	}
	if len(h1) != 64 { // SHA-256 hex = 64 chars
		t.Errorf("Hash length = %d, want 64", len(h1))
	}
}

func TestBuildCacheKey(t *testing.T) {
	k1 := BuildCacheKey("anthropic", "claude-3-5-sonnet", "diff content")
	k2 := BuildCacheKey("anthropic", "claude-3-5-sonnet", "diff content")
	k3 := BuildCacheKey("openai", "gpt-4o", "diff content")

	if k1 != k2 {
		t.Error("Same inputs should produce same cache key")
	}
	if k1 == k3 {
		t.Error("Different provider should produce different cache key")
	}
}
