//go:build integration

package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// providerSpec defines a provider to test.
type providerSpec struct {
	name   string
	model  string
	envVar string // env var that must be set (empty for ollama)
}

var providerSpecs = []providerSpec{
	{"anthropic", "claude-sonnet-4-20250514", "ANTHROPIC_API_KEY"},
	{"openai", "gpt-4o-mini", "OPENAI_API_KEY"},
	{"gemini", "gemini-2.0-flash", "GEMINI_API_KEY"},
	{"ollama", "llama3", ""},
}

func skipIfEnvMissing(t *testing.T, envVar string) {
	t.Helper()
	if envVar == "" {
		return // no env var needed (e.g. ollama)
	}
	if os.Getenv(envVar) == "" {
		t.Skipf("skipping: %s not set", envVar)
	}
}

func skipIfOllamaUnavailable(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:11434/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("skipping: ollama not reachable: %v", err)
	}
	resp.Body.Close()
}

func integrationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// testDiff is a small Go diff with an obvious command injection vulnerability.
const testDiff = `diff --git a/cmd/run.go b/cmd/run.go
new file mode 100644
--- /dev/null
+++ b/cmd/run.go
@@ -0,0 +1,15 @@
+package cmd
+
+import (
+	"fmt"
+	"os/exec"
+)
+
+func RunUserCommand(userInput string) (string, error) {
+	cmd := exec.Command("bash", "-c", userInput)
+	out, err := cmd.CombinedOutput()
+	if err != nil {
+		return "", fmt.Errorf("command failed: %w", err)
+	}
+	return string(out), nil
+}
`

// systemPrompt is the review system prompt (duplicated here to avoid
// importing internal/review from internal/providers which would be a
// circular dependency in tests that share the same module).
const reviewSystemPrompt = `You are a strict, expert code reviewer. Your job is to review code diffs and produce structured findings in JSON format.

Rules:
1. Only review the changes shown in the diff. Do not comment on unchanged code.
2. Focus on bugs, security issues, performance problems, and correctness. Avoid bikeshedding on style unless it impacts readability significantly.
3. Be concise and actionable. Every finding must include a concrete suggestion.
4. Reference line numbers from the diff hunks.
5. Rate severity as "low", "medium", or "high".
6. Rate your confidence from 0.0 to 1.0.
7. Categorize each finding as one of: bug, security, performance, correctness, style, maintainability, testing, docs.

You MUST respond with ONLY a JSON array of findings. No markdown, no explanation, no preamble. Just the JSON array.

Each finding must have this exact structure:
{
  "severity": "low|medium|high",
  "category": "bug|security|performance|correctness|style|maintainability|testing|docs",
  "title": "Short descriptive title",
  "message": "What is wrong and why it matters",
  "suggestion": "How to fix it, with code if helpful",
  "confidence": 0.0-1.0,
  "path": "relative/file/path",
  "startLine": 1,
  "endLine": 1,
  "tags": ["optional", "tags"]
}

If there are no issues, respond with an empty array: []`

// testRawFinding mirrors review.rawFinding for JSON parsing in the
// providers package without importing review.
type testRawFinding struct {
	Severity   string   `json:"severity"`
	Category   string   `json:"category"`
	Title      string   `json:"title"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion"`
	Confidence float64  `json:"confidence"`
	Path       string   `json:"path"`
	StartLine  int      `json:"startLine"`
	EndLine    int      `json:"endLine"`
	Tags       []string `json:"tags"`
}

// parseFindingsFromContent parses LLM content into testRawFindings,
// stripping markdown fences if present.
func parseFindingsFromContent(content string) ([]testRawFinding, error) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		if len(lines) >= 2 {
			start := 1
			end := len(lines)
			if strings.TrimSpace(lines[end-1]) == "```" {
				end--
			}
			if start < end {
				content = strings.Join(lines[start:end], "\n")
			} else {
				content = "[]"
			}
		}
	}
	var findings []testRawFinding
	if err := json.Unmarshal([]byte(content), &findings); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w\ncontent: %s", err, content[:min(len(content), 500)])
	}
	return findings, nil
}

// validSeverities is the set of valid severity strings.
var validSeverities = map[string]bool{
	"low": true, "medium": true, "high": true,
}

// validCategories is the set of valid category strings.
var validCategories = map[string]bool{
	"bug": true, "security": true, "performance": true, "correctness": true,
	"style": true, "maintainability": true, "testing": true, "docs": true,
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestIntegration_Provider_BasicReview verifies that each provider returns
// non-empty content and a token count for a simple prompt.
func TestIntegration_Provider_BasicReview(t *testing.T) {
	for _, spec := range providerSpecs {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			skipIfEnvMissing(t, spec.envVar)
			if spec.name == "ollama" {
				skipIfOllamaUnavailable(t)
			}

			ctx := integrationContext(t)

			provider, err := New(spec.name, spec.model)
			if err != nil {
				t.Fatalf("New(%s, %s): %v", spec.name, spec.model, err)
			}

			resp, err := provider.Review(ctx, ReviewRequest{
				SystemPrompt: "You are a helpful assistant.",
				UserPrompt:   "Reply with exactly: HELLO INTEGRATION TEST",
				MaxTokens:    256,
			})
			if err != nil {
				t.Fatalf("Review() error: %v", err)
			}

			if resp.Content == "" {
				t.Fatal("expected non-empty response content")
			}
			if !strings.Contains(strings.ToUpper(resp.Content), "HELLO") {
				t.Logf("warning: response did not contain HELLO: %s", resp.Content)
			}
			t.Logf("provider=%s tokens=%d content_len=%d", spec.name, resp.TokensUsed, len(resp.Content))
		})
	}
}

// TestIntegration_Provider_StructuredReview verifies that each provider
// returns parseable JSON findings when given the review system prompt and
// test diff. It validates structure but not exact content (LLMs are
// non-deterministic).
func TestIntegration_Provider_StructuredReview(t *testing.T) {
	userPrompt := fmt.Sprintf("Review the following code diff.\nLanguages: Go\n\n--- BEGIN DIFF ---\n%s\n--- END DIFF ---\n", testDiff)

	for _, spec := range providerSpecs {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			skipIfEnvMissing(t, spec.envVar)
			if spec.name == "ollama" {
				skipIfOllamaUnavailable(t)
			}

			ctx := integrationContext(t)

			provider, err := New(spec.name, spec.model)
			if err != nil {
				t.Fatalf("New(%s, %s): %v", spec.name, spec.model, err)
			}

			resp, err := provider.Review(ctx, ReviewRequest{
				SystemPrompt: reviewSystemPrompt,
				UserPrompt:   userPrompt,
				MaxTokens:    4096,
			})
			if err != nil {
				t.Fatalf("Review() error: %v", err)
			}

			findings, err := parseFindingsFromContent(resp.Content)
			if err != nil {
				t.Fatalf("failed to parse findings: %v", err)
			}

			t.Logf("provider=%s findings=%d tokens=%d", spec.name, len(findings), resp.TokensUsed)

			if len(findings) == 0 {
				t.Fatal("expected at least one finding for command injection diff")
			}

			// Validate structure of each finding
			for i, f := range findings {
				if f.Title == "" {
					t.Errorf("finding[%d]: empty title", i)
				}
				if f.Message == "" {
					t.Errorf("finding[%d]: empty message", i)
				}
				if !validSeverities[f.Severity] {
					t.Errorf("finding[%d]: invalid severity %q", i, f.Severity)
				}
				if !validCategories[f.Category] {
					t.Errorf("finding[%d]: invalid category %q", i, f.Category)
				}
				if f.Confidence < 0 || f.Confidence > 1 {
					t.Errorf("finding[%d]: confidence %f out of [0,1] range", i, f.Confidence)
				}
			}

			// Check if any finding mentions security/injection (warn, non-fatal)
			foundSecurity := false
			for _, f := range findings {
				lower := strings.ToLower(f.Title + " " + f.Message + " " + f.Category)
				if strings.Contains(lower, "security") ||
					strings.Contains(lower, "injection") ||
					strings.Contains(lower, "command") {
					foundSecurity = true
					break
				}
			}
			if !foundSecurity {
				t.Log("warning: no finding explicitly mentions security/injection/command — LLM may have categorized differently")
			}
		})
	}
}
