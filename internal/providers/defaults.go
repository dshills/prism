package providers

import "strings"

// DefaultRPM returns the conservative default requests-per-minute rate limit
// for a named provider. Returns 0 (unlimited) for local providers.
func DefaultRPM(name string) int {
	switch strings.ToLower(name) {
	case "anthropic":
		return 50
	case "openai":
		return 60
	case "gemini":
		return 60
	case "ollama":
		return 0
	default:
		return 60
	}
}

// DefaultMaxConcurrency returns the default number of concurrent LLM calls
// for a named provider. Local providers can handle more concurrency since
// they are not subject to network rate limits.
func DefaultMaxConcurrency(name string) int {
	switch strings.ToLower(name) {
	case "ollama":
		return 16
	default:
		return 8
	}
}
