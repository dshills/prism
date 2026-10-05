package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/review"
)

func TestFindingsCommands(t *testing.T) {
	ctx, _ := baselineTestEnv(t)
	for _, c := range []interface{ SetContext(context.Context) }{findingsConfirmCmd, findingsDismissCmd} {
		c.SetContext(ctx)
	}
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := review.Finding{ID: "0123456789abcdef", Title: "Nil map write", Category: review.CategoryBug, Severity: review.SeverityHigh, Provider: "openai", Model: "gpt"}
	g := review.Finding{ID: "fedcba9876543210", Title: "Style nit", Category: review.CategoryStyle, Provider: "openai", Model: "gpt"}
	rememberReport(&review.Report{Repo: review.RepoInfo{Root: repoRoot(ctx)}, Findings: []review.Finding{f, g}}, cfg)

	flagFindingsReason = "fixed in the same change"
	t.Cleanup(func() { flagFindingsReason = "" })
	if err := findingsConfirmCmd.RunE(findingsConfirmCmd, []string{f.ID}); err != nil {
		t.Fatal(err)
	}
	flagFindingsReason = ""
	if err := findingsDismissCmd.RunE(findingsDismissCmd, []string{g.ID}); err != nil {
		t.Fatal(err)
	}
	// An unknown ID writes nothing, even beside a known one.
	if err := findingsDismissCmd.RunE(findingsDismissCmd, []string{g.ID, "ffffffffffffffff"}); err == nil || !strings.Contains(err.Error(), "not in this repository's last review") {
		t.Errorf("unknown ID: %v", err)
	}

	path, _ := review.FeedbackPath(cfg)
	entries, err := review.LoadFeedback(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("log has %d entries, %v; want 2", len(entries), err)
	}
	if e := entries[0]; e.ID != f.ID || e.Verdict != review.VerdictConfirmed || e.Category != "bug" || e.Model != "gpt" || e.Reason != "fixed in the same change" || e.Time == "" {
		t.Errorf("entry = %+v", e)
	}
	if entries[1].Verdict != review.VerdictDismissed || entries[1].Category != "style" {
		t.Errorf("entry = %+v", entries[1])
	}
}
