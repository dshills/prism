package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/review"
	"github.com/spf13/cobra"
)

// --only-new without --since is a usage error, caught before any review.
func TestDeltaFlags_OnlyNewNeedsSince(t *testing.T) {
	cmd := &cobra.Command{Use: "x", Run: func(*cobra.Command, []string) {}}
	addDeltaFlags(cmd)
	t.Cleanup(func() { flagSince, flagOnlyNew, priorReview = "", false, nil })

	flagSince, flagOnlyNew = "", true
	if err := cmd.PreRunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "--since") {
		t.Errorf("err = %v, want --only-new rejected without --since", err)
	}
	baselineTestEnv(t) // temp config and cache: no last review yet
	flagSince = "last"
	if err := cmd.PreRunE(cmd, nil); err != nil {
		t.Errorf("with --since: %v", err)
	}

	// A bad --since file fails here, before any model is called.
	flagSince = filepath.Join(t.TempDir(), "missing.json")
	if err := cmd.PreRunE(cmd, nil); err == nil {
		t.Error("a missing --since file should fail before the review")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"not":"a report"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	flagSince = bad
	if err := cmd.PreRunE(cmd, nil); err == nil {
		t.Error("a file that is not a report should fail before the review")
	}
}

// --since last with no last review yet (a fix loop's first run) compares
// with nothing, so every finding is new; a file is read as a prior report.
func TestWithDelta(t *testing.T) {
	baselineTestEnv(t) // temp config and cache
	t.Cleanup(func() { flagSince, flagOnlyNew, priorReview = "", false, nil })
	ctx := context.Background()
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	report := &review.Report{Findings: []review.Finding{{ID: "A", Title: "a"}, {ID: "B", Title: "b"}}}

	flagSince = sinceLast
	if repoRoot(ctx) == "" {
		t.Skip("not in a git repository")
	}
	got, err := withDelta(ctx, report, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delta == nil || got.Delta.New != 2 {
		t.Errorf("first run: delta = %+v, want both new", got.Delta)
	}

	prior := filepath.Join(t.TempDir(), "prior.json")
	data, _ := json.Marshal(review.Report{Tool: "prism", Findings: []review.Finding{{ID: "A"}}})
	if err := os.WriteFile(prior, data, 0o644); err != nil {
		t.Fatal(err)
	}
	flagSince, flagOnlyNew = prior, true
	got, err = withDelta(ctx, report, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Findings[0].ID != "B" || len(report.Findings) != 2 {
		t.Errorf("only new: %d findings (%v), original %d; want B alone and the original whole", len(got.Findings), got.Findings, len(report.Findings))
	}

	flagSince = filepath.Join(t.TempDir(), "missing.json")
	if _, err := withDelta(ctx, report, cfg, nil); err == nil {
		t.Error("a missing --since file should be an error")
	}
}
