//go:build integration

package review_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/output"
	"github.com/dshills/prism/internal/review"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type engineProviderSpec struct {
	providerName string
	model        string
	envVar       string
}

var engineProviderSpecs = []engineProviderSpec{
	{"anthropic", "claude-sonnet-4-20250514", "ANTHROPIC_API_KEY"},
	{"openai", "gpt-4o-mini", "OPENAI_API_KEY"},
	{"gemini", "gemini-2.0-flash", "GEMINI_API_KEY"},
	{"ollama", "llama3", ""},
}

func skipIfEnvMissing(t *testing.T, envVar string) {
	t.Helper()
	if envVar == "" {
		return
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

func skipProvider(t *testing.T, spec engineProviderSpec) {
	t.Helper()
	skipIfEnvMissing(t, spec.envVar)
	if spec.providerName == "ollama" {
		skipIfOllamaUnavailable(t)
	}
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

func integrationDiffResult() gitctx.DiffResult {
	return gitctx.DiffResult{
		Diff:  testDiff,
		Files: []string{"cmd/run.go"},
		Mode:  "snippet",
		Repo: gitctx.RepoMeta{
			Root:   "/tmp/test-repo",
			Head:   "abc1234",
			Branch: "main",
		},
	}
}

func integrationConfig(provider, model, cacheDir string) config.Config {
	cfg := config.Default()
	cfg.Provider = provider
	cfg.Model = model
	cfg.MaxFindings = 20
	cfg.FailOn = "high"
	cfg.Format = "json"
	cfg.Privacy.RedactSecrets = false // test diff has no secrets
	if cacheDir != "" {
		cfg.Cache.Enabled = true
		cfg.Cache.Dir = cacheDir
		cfg.Cache.TTLSeconds = 3600
	} else {
		cfg.Cache.Enabled = false
	}
	return cfg
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestIntegration_Run_EndToEnd runs review.Run() for each provider and
// validates the Report structure.
func TestIntegration_Run_EndToEnd(t *testing.T) {
	diff := integrationDiffResult()

	for _, spec := range engineProviderSpecs {
		spec := spec
		t.Run(spec.providerName, func(t *testing.T) {
			t.Parallel()
			skipProvider(t, spec)

			ctx := integrationContext(t)
			cfg := integrationConfig(spec.providerName, spec.model, "")

			report, err := review.Run(ctx, diff, cfg)
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}

			// Validate Report structure
			if report.Tool != "prism" {
				t.Errorf("Tool = %q, want %q", report.Tool, "prism")
			}
			if report.RunID == "" {
				t.Error("RunID is empty")
			}
			if len(report.RunID) != 32 {
				t.Errorf("RunID length = %d, want 32 hex chars", len(report.RunID))
			}
			if report.Inputs.Mode != "snippet" {
				t.Errorf("Inputs.Mode = %q, want %q", report.Inputs.Mode, "snippet")
			}

			// Should have at least one finding
			if len(report.Findings) == 0 {
				t.Fatal("expected at least one finding")
			}

			// Validate findings
			for i, f := range report.Findings {
				if f.ID == "" {
					t.Errorf("finding[%d]: empty ID", i)
				}
				if f.Title == "" {
					t.Errorf("finding[%d]: empty title", i)
				}
				if f.Severity != review.SeverityLow && f.Severity != review.SeverityMedium && f.Severity != review.SeverityHigh {
					t.Errorf("finding[%d]: invalid severity %q", i, f.Severity)
				}
				if f.Confidence < 0 || f.Confidence > 1 {
					t.Errorf("finding[%d]: confidence %f out of range", i, f.Confidence)
				}
			}

			// Summary counts should match findings
			expectedSummary := review.ComputeSummary(report.Findings)
			if report.Summary != expectedSummary {
				t.Errorf("Summary mismatch: got %+v, want %+v", report.Summary, expectedSummary)
			}

			// Timing should be positive
			if report.Timing.LLMMs <= 0 {
				t.Errorf("Timing.LLMMs = %d, want > 0", report.Timing.LLMMs)
			}
			if report.Timing.TotalMs <= 0 {
				t.Errorf("Timing.TotalMs = %d, want > 0", report.Timing.TotalMs)
			}

			t.Logf("provider=%s findings=%d llmMs=%d totalMs=%d",
				spec.providerName, len(report.Findings), report.Timing.LLMMs, report.Timing.TotalMs)
		})
	}
}

// TestIntegration_Run_EmptyDiff verifies that an empty diff produces an
// empty report with no LLM call.
func TestIntegration_Run_EmptyDiff(t *testing.T) {
	ctx := integrationContext(t)

	diff := gitctx.DiffResult{
		Diff:  "",
		Files: nil,
		Mode:  "unstaged",
		Repo:  gitctx.RepoMeta{Root: "/tmp/empty", Head: "abc", Branch: "main"},
	}
	cfg := integrationConfig("anthropic", "claude-sonnet-4-20250514", "")

	report, err := review.Run(ctx, diff, cfg)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(report.Findings) != 0 {
		t.Errorf("expected 0 findings for empty diff, got %d", len(report.Findings))
	}
	if report.Timing.LLMMs != 0 {
		t.Errorf("expected 0 LLMMs for empty diff, got %d", report.Timing.LLMMs)
	}
	if report.Tool != "prism" {
		t.Errorf("Tool = %q, want %q", report.Tool, "prism")
	}
}

// TestIntegration_Run_CacheRoundTrip verifies that the second identical
// Run() call hits the cache and completes much faster.
func TestIntegration_Run_CacheRoundTrip(t *testing.T) {
	// Use first available cloud provider
	var spec engineProviderSpec
	found := false
	for _, s := range engineProviderSpecs {
		if s.envVar != "" && os.Getenv(s.envVar) != "" {
			spec = s
			found = true
			break
		}
	}
	if !found {
		t.Skip("skipping: no cloud provider API keys set")
	}

	ctx := integrationContext(t)
	cacheDir := t.TempDir()
	diff := integrationDiffResult()
	cfg := integrationConfig(spec.providerName, spec.model, cacheDir)

	// First run — hits the LLM
	start1 := time.Now()
	report1, err := review.Run(ctx, diff, cfg)
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	elapsed1 := time.Since(start1)

	if len(report1.Findings) == 0 {
		t.Fatal("first run: expected at least one finding")
	}
	t.Logf("first run: %d findings in %s", len(report1.Findings), elapsed1)

	// Second run — should hit cache
	start2 := time.Now()
	report2, err := review.Run(ctx, diff, cfg)
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	elapsed2 := time.Since(start2)

	t.Logf("second run: %d findings in %s (cached)", len(report2.Findings), elapsed2)

	// Cache should make second run much faster (<1s)
	if elapsed2 > 1*time.Second {
		t.Errorf("second run took %s, expected <1s (cache miss?)", elapsed2)
	}

	// Finding IDs should match between cached and uncached runs
	if len(report1.Findings) != len(report2.Findings) {
		t.Errorf("finding count mismatch: first=%d second=%d", len(report1.Findings), len(report2.Findings))
	} else {
		for i := range report1.Findings {
			if report1.Findings[i].ID != report2.Findings[i].ID {
				t.Errorf("finding[%d] ID mismatch: %s vs %s",
					i, report1.Findings[i].ID, report2.Findings[i].ID)
			}
		}
	}
}

// TestIntegration_RunCompare tests compare mode with 2 cloud providers.
func TestIntegration_RunCompare(t *testing.T) {
	// Need at least 2 cloud providers
	var available []engineProviderSpec
	for _, s := range engineProviderSpecs {
		if s.envVar == "" {
			continue // skip ollama for compare — it's slow
		}
		if os.Getenv(s.envVar) != "" {
			available = append(available, s)
		}
	}
	if len(available) < 2 {
		t.Skipf("skipping: need at least 2 cloud provider keys, have %d", len(available))
	}

	ctx := integrationContext(t)
	diff := integrationDiffResult()
	cfg := integrationConfig(available[0].providerName, available[0].model, "")

	models := make([]string, 2)
	for i := 0; i < 2; i++ {
		models[i] = available[i].providerName + ":" + available[i].model
	}

	result, err := review.RunCompare(ctx, diff.Diff, diff.Files, models, cfg, nil)
	if err != nil {
		t.Fatalf("RunCompare() error: %v", err)
	}

	// All findings should include consensus + unique
	totalUnique := 0
	for label, findings := range result.Unique {
		totalUnique += len(findings)
		t.Logf("unique[%s]: %d findings", label, len(findings))
	}
	t.Logf("consensus: %d findings, unique total: %d, all: %d",
		len(result.Consensus), totalUnique, len(result.All))

	// All = consensus + unique
	if len(result.All) != len(result.Consensus)+totalUnique {
		t.Errorf("All count %d != consensus %d + unique %d",
			len(result.All), len(result.Consensus), totalUnique)
	}

	// Should have at least some findings total
	if len(result.All) == 0 {
		t.Error("expected at least one finding across both providers")
	}

	// LLMMs should be positive
	if result.LLMMs <= 0 {
		t.Errorf("LLMMs = %d, want > 0", result.LLMMs)
	}
}

// TestIntegration_OutputFormats runs one review, then formats the report
// through all 4 output writers and validates basic structure.
func TestIntegration_OutputFormats(t *testing.T) {
	// Use first available provider
	var spec engineProviderSpec
	found := false
	for _, s := range engineProviderSpecs {
		if s.envVar != "" && os.Getenv(s.envVar) != "" {
			spec = s
			found = true
			break
		}
	}
	if !found {
		t.Skip("skipping: no cloud provider API keys set")
	}

	ctx := integrationContext(t)
	diff := integrationDiffResult()
	cfg := integrationConfig(spec.providerName, spec.model, "")

	report, err := review.Run(ctx, diff, cfg)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("need findings to test output formats")
	}

	// Text format
	t.Run("text", func(t *testing.T) {
		w, err := output.GetWriter("text")
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := w.Write(&buf, report); err != nil {
			t.Fatalf("text write: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "Prism Code Review") {
			t.Errorf("text output missing 'Prism Code Review' header")
		}
		t.Logf("text output: %d bytes", len(out))
	})

	// JSON format
	t.Run("json", func(t *testing.T) {
		w, err := output.GetWriter("json")
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := w.Write(&buf, report); err != nil {
			t.Fatalf("json write: %v", err)
		}
		var parsed review.Report
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("json output not valid JSON: %v", err)
		}
		if parsed.Tool != "prism" {
			t.Errorf("parsed Tool = %q, want %q", parsed.Tool, "prism")
		}
		if len(parsed.Findings) != len(report.Findings) {
			t.Errorf("parsed findings count = %d, want %d", len(parsed.Findings), len(report.Findings))
		}
	})

	// Markdown format
	t.Run("markdown", func(t *testing.T) {
		w, err := output.GetWriter("markdown")
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := w.Write(&buf, report); err != nil {
			t.Fatalf("markdown write: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "<details>") {
			t.Errorf("markdown output missing <details> tag")
		}
		t.Logf("markdown output: %d bytes", len(out))
	})

	// SARIF format
	t.Run("sarif", func(t *testing.T) {
		w, err := output.GetWriter("sarif")
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := w.Write(&buf, report); err != nil {
			t.Fatalf("sarif write: %v", err)
		}
		out := buf.String()
		// SARIF should have the version and results
		if !strings.Contains(out, "2.1.0") {
			t.Errorf("sarif output missing version 2.1.0")
		}
		if !strings.Contains(out, "results") {
			t.Errorf("sarif output missing 'results' key")
		}
		// Validate it's valid JSON
		var sarifParsed map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &sarifParsed); err != nil {
			t.Fatalf("sarif output not valid JSON: %v", err)
		}
	})
}

// TestIntegration_FindingID_Stability verifies that two uncached runs
// produce the same finding IDs for findings with the same
// path+title+startLine.
func TestIntegration_FindingID_Stability(t *testing.T) {
	// Use first available provider
	var spec engineProviderSpec
	found := false
	for _, s := range engineProviderSpecs {
		if s.envVar != "" && os.Getenv(s.envVar) != "" {
			spec = s
			found = true
			break
		}
	}
	if !found {
		t.Skip("skipping: no cloud provider API keys set")
	}

	ctx := integrationContext(t)
	diff := integrationDiffResult()

	// Two runs with cache disabled
	cfg1 := integrationConfig(spec.providerName, spec.model, "")
	cfg2 := integrationConfig(spec.providerName, spec.model, "")

	report1, err := review.Run(ctx, diff, cfg1)
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	report2, err := review.Run(ctx, diff, cfg2)
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}

	if len(report1.Findings) == 0 || len(report2.Findings) == 0 {
		t.Skipf("one run returned no findings (run1=%d, run2=%d) — cannot test ID stability",
			len(report1.Findings), len(report2.Findings))
	}

	// Build ID maps by path+title+startLine
	type findingKey struct {
		path      string
		title     string
		startLine int
	}
	ids1 := make(map[findingKey]string)
	for _, f := range report1.Findings {
		path := ""
		startLine := 0
		if len(f.Locations) > 0 {
			path = f.Locations[0].Path
			startLine = f.Locations[0].Lines.Start
		}
		ids1[findingKey{path, f.Title, startLine}] = f.ID
	}

	matched := 0
	for _, f := range report2.Findings {
		path := ""
		startLine := 0
		if len(f.Locations) > 0 {
			path = f.Locations[0].Path
			startLine = f.Locations[0].Lines.Start
		}
		key := findingKey{path, f.Title, startLine}
		if id1, ok := ids1[key]; ok {
			matched++
			if f.ID != id1 {
				t.Errorf("finding %q ID mismatch: run1=%s run2=%s", f.Title, id1, f.ID)
			}
		}
	}

	t.Logf("matched %d findings across runs (run1=%d, run2=%d)",
		matched, len(report1.Findings), len(report2.Findings))

	if matched == 0 {
		t.Log("warning: no findings matched by path+title+startLine — LLM outputs differed significantly between runs")
	}
}
