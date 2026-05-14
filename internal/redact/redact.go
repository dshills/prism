package redact

import (
	"path/filepath"
	"regexp"
	"strings"
)

const placeholder = "[REDACTED]"

// combinedSecretPattern is a single alternation of all secret heuristics.
// Each alternative uses (?i:...) to scope case-insensitivity to that branch
// only, avoiding flag leakage across alternatives.
// One pass over the input replaces all secret types simultaneously, compared
// to 14 sequential full-text scans with the previous per-pattern approach.
var combinedSecretPattern = regexp.MustCompile(
	`(?:` +
		// Generic API keys (long hex/base64 strings after common key patterns)
		`(?i:(?:api[_-]?key|apikey|api[_-]?secret)\s*[:=]\s*["']?[A-Za-z0-9/+=_-]{20,}["']?)` + `|` +
		// AWS access key IDs
		`AKIA[0-9A-Z]{16}` + `|` +
		// AWS secret access keys
		`(?i:(?:aws[_-]?secret[_-]?access[_-]?key)\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}["']?)` + `|` +
		// Generic secrets/tokens/passwords in assignments (quoted form first to avoid false positives)
		`(?i:(?:secret|token|password|passwd|credential)\s*[:=]\s*["'][^"']{8,}["'])` + `|` +
		// Generic secrets/tokens/passwords in assignments (unquoted)
		`(?i:(?:secret|token|password|passwd|credential)\s*[:=]\s*[^\s"']{8,})` + `|` +
		// Bearer tokens (including /, +, = in base64 tokens)
		`(?i:Bearer\s+[A-Za-z0-9._/+=-]{20,})` + `|` +
		// JWTs (three base64 segments separated by dots)
		`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}` + `|` +
		// Private key blocks
		`-----BEGIN\s+(?:RSA\s+)?PRIVATE KEY-----` + `|` +
		// Anthropic API keys (before generic sk- to take priority)
		`sk-ant-[A-Za-z0-9_-]{20,}` + `|` +
		// GitHub tokens
		`gh[pousr]_[A-Za-z0-9_]{36,}` + `|` +
		// Slack tokens
		`xox[bporas]-[A-Za-z0-9-]{10,}` + `|` +
		// OpenAI API keys
		`sk-[A-Za-z0-9]{20,}` + `|` +
		// Database connection strings with credentials
		`(?i:(?:mongodb|postgres|postgresql|mysql|redis|amqp)://[^\s"']+:[^\s"'@]+@[^\s"']+)` + `|` +
		// Generic long hex strings that look like secrets (32+ chars in an assignment)
		`(?i:(?:key|secret|token)\s*[:=]\s*["']?[0-9a-f]{32,}["']?)` +
		`)`,
)

// Secrets replaces detected secrets in text with [REDACTED].
// A single combined regex pass replaces all secret types at once.
func Secrets(text string) string {
	return combinedSecretPattern.ReplaceAllString(text, placeholder)
}

// ShouldRedactPath checks if a file path matches any of the redaction path patterns.
func ShouldRedactPath(path string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
		// Also try matching just the filename for patterns like "**/.env"
		cleanPattern := strings.TrimPrefix(pattern, "**/")
		if cleanPattern != pattern {
			base := filepath.Base(path)
			matched, err = filepath.Match(cleanPattern, base)
			if err == nil && matched {
				return true
			}
		}
	}
	return false
}

// Content redacts secrets from content and optionally redacts entire content
// if the file path matches redaction patterns.
func Content(content, path string, redactPaths []string) string {
	if ShouldRedactPath(path, redactPaths) {
		return placeholder + " (file content redacted by path policy)\n"
	}
	return Secrets(content)
}
