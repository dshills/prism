//go:build integration

package review

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dshills/prism/internal/providers"
)

// Each provider's real API accepts the findings schema and answers in
// structured form. The prompt asks for a bare array, so a response that is
// the {"findings": ...} object came from structured output, not from a
// fallback after the API refused the schema.
//
// Override the models with PRISM_IT_OPENAI_MODEL, PRISM_IT_ANTHROPIC_MODEL
// and PRISM_IT_GEMINI_MODEL.
func TestStructuredOutputLive(t *testing.T) {
	model := func(env, def string) string {
		if m := os.Getenv(env); m != "" {
			return m
		}
		return def
	}
	diff := "diff --git a/run.go b/run.go\n--- a/run.go\n+++ b/run.go\n@@ -1,3 +1,6 @@\n" +
		" package run\n+\n+import \"os/exec\"\n+\n+func Run(name string) { _ = exec.Command(\"sh\", \"-c\", \"echo \"+name).Run() }\n"
	for _, tc := range []struct{ provider, model, key string }{
		{"openai", model("PRISM_IT_OPENAI_MODEL", "gpt-6.1-sol"), "OPENAI_API_KEY"},
		{"anthropic", model("PRISM_IT_ANTHROPIC_MODEL", "claude-haiku-4-5"), "ANTHROPIC_API_KEY"},
		{"gemini", model("PRISM_IT_GEMINI_MODEL", "gemini-3-flash-preview"), "GEMINI_API_KEY"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			if os.Getenv(tc.key) == "" {
				t.Skipf("%s not set", tc.key)
			}
			p, err := providers.New(tc.provider, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			resp, err := p.Review(ctx, providers.ReviewRequest{
				SystemPrompt: SystemPrompt(),
				UserPrompt:   BuildUserPrompt(diff, []string{"run.go"}, 10, ""),
				MaxTokens:    8192,
				Output:       findingsOutput,
			})
			if err != nil {
				t.Fatalf("%s/%s: %v", tc.provider, tc.model, err)
			}
			if !strings.HasPrefix(strings.TrimSpace(resp.Content), "{") {
				t.Errorf("%s/%s answered without structured output:\n%s", tc.provider, tc.model, resp.Content)
			}
			findings, err := parseFindings(resp.Content)
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, resp.Content)
			}
			t.Logf("%s/%s: %d finding(s), %d tokens", tc.provider, tc.model, len(findings), resp.TokensUsed)
			for _, f := range findings {
				if f.Severity != SeverityLow && f.Severity != SeverityMedium && f.Severity != SeverityHigh {
					t.Errorf("severity %q outside the schema's enum", f.Severity)
				}
			}
		})
	}
}
