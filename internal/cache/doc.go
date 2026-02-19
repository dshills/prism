// Package cache provides a file-based cache for LLM review responses.
//
// Cache entries are keyed by a SHA-256 hash of the provider name, model, and
// redacted diff content. Each entry stores the raw LLM response string along
// with a creation timestamp and a TTL (in seconds). Expired entries are
// skipped on read and removed during cache-clear operations.
//
// The default cache directory is $XDG_CACHE_HOME/prism (or the OS-appropriate
// equivalent). All payloads stored in the cache have already been through
// secret redaction.
package cache
