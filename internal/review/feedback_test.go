package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/gitctx"
)

func verdicts(category, provider, model string, confirmed, dismissed int) []FeedbackEntry {
	var out []FeedbackEntry
	for i := range confirmed + dismissed {
		v := VerdictConfirmed
		if i >= confirmed {
			v = VerdictDismissed
		}
		out = append(out, FeedbackEntry{ID: fmt.Sprintf("%s-%s-%d", category, model, i), Verdict: v, Category: category, Provider: provider, Model: model})
	}
	return out
}

func TestFeedbackStats(t *testing.T) {
	entries := append(verdicts("bug", "openai", "a", 4, 1), verdicts("bug", "openai", "b", 1, 2)...)
	entries = append(entries, verdicts("style", "openai", "a", 1, 3)...)
	s := NewFeedbackStats(entries)

	// Model a has 5 bug verdicts: its own rate.
	if c := s.For(Finding{Category: CategoryBug, Provider: "openai", Model: "a"}); c == nil || c.Scope != "model" || c.ConfirmRate != 0.8 || c.Samples != 5 {
		t.Errorf("model a bug: %+v", c)
	}
	// Model b has 3: the category's rate over every model.
	if c := s.For(Finding{Category: CategoryBug, Provider: "openai", Model: "b"}); c == nil || c.Scope != "category" || c.ConfirmRate != 0.625 || c.Samples != 8 {
		t.Errorf("model b bug: %+v", c)
	}
	// Style has 4 in all: too few to say.
	if c := s.For(Finding{Category: CategoryStyle, Provider: "openai", Model: "a"}); c != nil {
		t.Errorf("style: %+v, want none", c)
	}
}

// A finding judged twice counts once, by its latest verdict.
func TestFeedbackStats_LatestVerdictWins(t *testing.T) {
	entries := verdicts("bug", "p", "m", 0, 5)
	entries = append(entries, FeedbackEntry{ID: entries[0].ID, Verdict: VerdictConfirmed, Category: "bug", Provider: "p", Model: "m"})
	if c := NewFeedbackStats(entries).For(Finding{Category: CategoryBug, Provider: "p", Model: "m"}); c == nil || c.Samples != 5 || c.ConfirmRate != 0.2 {
		t.Errorf("= %+v, want 1 of 5 confirmed", c)
	}
}

func TestFeedbackLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "feedback.jsonl")
	if got, err := LoadFeedback(path); err != nil || got != nil {
		t.Fatalf("missing log = %v, %v", got, err)
	}
	if err := AppendFeedback(path, verdicts("bug", "p", "m", 2, 0)); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("{not json\n{\"id\":\"x\",\"verdict\":\"maybe\"}\n" + strings.Repeat("x", 2<<20) + "\n")
	_ = f.Close()
	if err := AppendFeedback(path, verdicts("bug", "p", "m", 0, 1)); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFeedback(path)
	if err != nil || len(got) != 3 {
		t.Errorf("loaded %d entries, %v; want 3, bad lines skipped", len(got), err)
	}
	if e := FeedbackFor(Finding{ID: "z"}, VerdictDismissed, strings.Repeat("é", maxReasonLen), "", ""); len(e.Reason) > maxReasonLen {
		t.Errorf("reason kept at %d bytes", len(e.Reason))
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v, want 0600", info.Mode().Perm())
	}
}

// Reviews carry the calibration of each finding from the log.
func TestFinalizeFindings_Calibrates(t *testing.T) {
	cfg := chunkTestConfig(t)
	cfg.BaselineFile = "none"
	path, err := FeedbackPath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendFeedback(path, verdicts("bug", "mock", "m", 3, 2)); err != nil {
		t.Fatal(err)
	}
	findings := []Finding{
		{ID: "x", Title: "x", Severity: SeverityHigh, Category: CategoryBug, Provider: "mock", Model: "m"},
		{ID: "y", Title: "y", Severity: SeverityLow, Category: CategoryDocs, Provider: "mock", Model: "m"},
	}
	got, _, _, err := FinalizeFindings(context.Background(), findings, gitctx.DiffResult{}, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := got[0].Calibration; c == nil || c.ConfirmRate != 0.6 || c.Scope != "model" {
		t.Errorf("bug finding calibration %+v", c)
	}
	if got[1].Calibration != nil {
		t.Error("a category without verdicts was calibrated")
	}
	if lines := NewFeedbackStats(verdicts("bug", "mock", "m", 3, 2)).CategoryLines(); len(lines) != 1 || !strings.Contains(lines[0], "60% confirmed (3 confirmed, 2 dismissed)") {
		t.Errorf("stats lines %q", lines)
	}
}
