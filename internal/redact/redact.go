package redact

import (
	"path/filepath"
	"regexp"
	"strings"
)

const placeholder = "[REDACTED]"

// secretPatterns are regex heuristics for common secret types.
var secretPatterns = []*regexp.Regexp{
	// Generic API keys (long hex/base64 strings after common key patterns)
	regexp.MustCompile(`(?i)(api[_-]?key|apikey|api[_-]?secret)\s*[:=]\s*["']?([A-Za-z0-9/+=_-]{20,})["']?`),
	// AWS access key IDs
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// AWS secret access keys
	regexp.MustCompile(`(?i)(aws[_-]?secret[_-]?access[_-]?key)\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})["']?`),
	// Generic secrets/tokens/passwords in assignments
	regexp.MustCompile(`(?i)(secret|token|password|passwd|credential)\s*[:=]\s*["']([^"']{8,})["']`),
	// Bearer tokens
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._-]{20,}`),
	// JWTs (three base64 segments separated by dots)
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	// Private key blocks
	regexp.MustCompile(`-----BEGIN\s+(RSA\s+)?PRIVATE KEY-----`),
	// GitHub tokens
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{36,}`),
	// Slack tokens
	regexp.MustCompile(`xox[bporas]-[A-Za-z0-9-]{10,}`),
	// Anthropic API keys
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),
	// OpenAI API keys
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	// Generic long hex strings that look like secrets (32+ chars in an assignment)
	regexp.MustCompile(`(?i)(key|secret|token)\s*[:=]\s*["']?[0-9a-f]{32,}["']?`),
}

// Secrets replaces detected secrets in text with [REDACTED].
func Secrets(text string) string {
	result := text
	for _, pat := range secretPatterns {
		result = pat.ReplaceAllStringFunc(result, func(match string) string {
			return placeholder
		})
	}
	return result
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
