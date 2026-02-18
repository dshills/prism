package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Entry represents a cached review result.
type Entry struct {
	Key       string    `json:"key"`
	Response  string    `json:"response"`
	CreatedAt time.Time `json:"createdAt"`
	TTL       int       `json:"ttl"`
}

// Cache provides file-based caching for LLM review responses.
type Cache struct {
	dir        string
	ttlSeconds int
	enabled    bool
}

// New creates a new Cache. If dir is empty, uses the default cache directory.
func New(enabled bool, dir string, ttlSeconds int) (*Cache, error) {
	if !enabled {
		return &Cache{enabled: false}, nil
	}
	if dir == "" {
		d, err := defaultCacheDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}
	return &Cache{
		dir:        dir,
		ttlSeconds: ttlSeconds,
		enabled:    true,
	}, nil
}

// Get retrieves a cached entry by key. Returns ("", false) on miss.
func (c *Cache) Get(key string) (string, bool) {
	if !c.enabled {
		return "", false
	}
	path := c.entryPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return "", false
	}
	// Check TTL
	if c.ttlSeconds > 0 && time.Since(entry.CreatedAt) > time.Duration(c.ttlSeconds)*time.Second {
		os.Remove(path)
		return "", false
	}
	return entry.Response, true
}

// Put stores a response in the cache.
func (c *Cache) Put(key, response string) error {
	if !c.enabled {
		return nil
	}
	entry := Entry{
		Key:       HashKey(key),
		Response:  response,
		CreatedAt: time.Now(),
		TTL:       c.ttlSeconds,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling cache entry: %w", err)
	}
	return os.WriteFile(c.entryPath(key), data, 0o644)
}

// Clear removes all cache entries.
func (c *Cache) Clear() error {
	if !c.enabled || c.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading cache directory: %w", err)
	}
	var removed int
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			if err := os.Remove(filepath.Join(c.dir, e.Name())); err == nil {
				removed++
			}
		}
	}
	return nil
}

// Stats returns cache statistics.
type Stats struct {
	Dir        string `json:"dir"`
	Entries    int    `json:"entries"`
	TotalBytes int64  `json:"totalBytes"`
	Expired    int    `json:"expired"`
}

// GetStats returns information about the cache.
func (c *Cache) GetStats() (Stats, error) {
	stats := Stats{Dir: c.dir}
	if !c.enabled || c.dir == "" {
		return stats, nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, fmt.Errorf("reading cache directory: %w", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		stats.Entries++
		stats.TotalBytes += info.Size()

		// Check if expired
		path := filepath.Join(c.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}
		if c.ttlSeconds > 0 && time.Since(entry.CreatedAt) > time.Duration(c.ttlSeconds)*time.Second {
			stats.Expired++
		}
	}
	return stats, nil
}

// Dir returns the cache directory path.
func (c *Cache) Dir() string {
	return c.dir
}

// Enabled returns whether caching is enabled.
func (c *Cache) Enabled() bool {
	return c.enabled
}

// HashKey creates a SHA-256 hash of the given key material.
func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", h)
}

// BuildCacheKey creates a cache key from the review inputs.
func BuildCacheKey(provider, model, diff string) string {
	return HashKey(fmt.Sprintf("%s:%s:%s", provider, model, diff))
}

func (c *Cache) entryPath(key string) string {
	return filepath.Join(c.dir, HashKey(key)+".json")
}

func defaultCacheDir() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "prism"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches", "prism"), nil
	case "windows":
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "prism", "cache"), nil
		}
		return filepath.Join(home, "AppData", "Local", "prism", "cache"), nil
	default:
		return filepath.Join(home, ".cache", "prism"), nil
	}
}
