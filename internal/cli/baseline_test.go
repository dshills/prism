package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/review"
)

// baselineTestEnv points config, cache and the baseline file at temp dirs,
// so nothing touches the user's real files or this repository's baseline.
func baselineTestEnv(t *testing.T) (ctx context.Context, path string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path = filepath.Join(t.TempDir(), "baseline.json")
	flagBaseline, flagBaselineReason, flagBaselineForce = path, "", false
	t.Cleanup(func() { flagBaseline, flagBaselineReason, flagBaselineForce = "", "", false })
	ctx = context.Background()
	for _, c := range []interface{ SetContext(context.Context) }{baselineAddCmd, baselineRemoveCmd, baselineShowCmd} {
		c.SetContext(ctx)
	}
	return ctx, path
}

func TestBaselineCommands(t *testing.T) {
	ctx, path := baselineTestEnv(t)
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := review.Finding{
		ID: "0123456789abcdef", Title: "Hardcoded secret", Category: review.CategorySecurity,
		Locations: []review.Location{{Path: "auth/token.go", Lines: review.LineRange{Start: 42, End: 42}}},
	}
	rememberReport(&review.Report{Repo: review.RepoInfo{Root: repoRoot(ctx)}, Findings: []review.Finding{f}}, cfg)

	// add looks the ID up in the last review and records what it was.
	flagBaselineReason = "loaded from vault"
	if err := baselineAddCmd.RunE(baselineAddCmd, []string{f.ID}); err != nil {
		t.Fatal(err)
	}
	b, err := review.LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Findings) != 1 {
		t.Fatalf("baseline has %d entries, want 1", len(b.Findings))
	}
	if e := b.Findings[0]; e.Path != "auth/token.go" || e.Title != "Hardcoded secret" || e.Category != "security" || e.Reason != "loaded from vault" || e.Added == "" {
		t.Errorf("entry = %+v, want the finding's path, title, category, reason and date", e)
	}

	// An ID that is not in the last review needs --force, and nothing is
	// written when any ID is refused.
	if err := baselineAddCmd.RunE(baselineAddCmd, []string{"ffffffffffffffff", f.ID}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("unknown ID: err = %v, want a hint to use --force", err)
	}
	if b, _ := review.LoadBaseline(path); len(b.Findings) != 1 {
		t.Errorf("a refused add changed the baseline: %d entries", len(b.Findings))
	}
	flagBaselineForce = true
	if err := baselineAddCmd.RunE(baselineAddCmd, []string{"ffffffffffffffff"}); err != nil {
		t.Fatal(err)
	}

	if err := baselineShowCmd.RunE(baselineShowCmd, nil); err != nil {
		t.Fatal(err)
	}

	if err := baselineRemoveCmd.RunE(baselineRemoveCmd, []string{f.ID}); err != nil {
		t.Fatal(err)
	}
	b, _ = review.LoadBaseline(path)
	if len(b.Findings) != 1 || b.Findings[0].ID != "ffffffffffffffff" {
		t.Errorf("after remove: %+v, want only the forced ID", b.Findings)
	}
	if err := baselineRemoveCmd.RunE(baselineRemoveCmd, []string{f.ID}); err == nil {
		t.Error("removing an ID that is not there should fail")
	}
}

// A finding already suppressed in the last review can be found too, for
// example to update its reason.
func TestFindInReport_Suppressed(t *testing.T) {
	f := review.Finding{ID: "a"}
	r := &review.Report{Suppressed: []review.Suppression{{Finding: f, Source: review.SuppressedByBaseline}}}
	if _, ok := findInReport(r, "a"); !ok {
		t.Error("a suppressed finding should be found")
	}
	if _, ok := findInReport(nil, "a"); ok {
		t.Error("no report, no finding")
	}
}

func TestBaselineOff(t *testing.T) {
	baselineTestEnv(t)
	flagBaseline = "none"
	if err := baselineShowCmd.RunE(baselineShowCmd, nil); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Errorf("err = %v, want the baseline reported as turned off", err)
	}
}

// A report with no known repository (a GitHub PR review) is not kept, so it
// cannot stand in for another repository's last review.
func TestRememberReport_NeedsRepo(t *testing.T) {
	ctx, _ := baselineTestEnv(t)
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := review.Finding{ID: "1111222233334444"}
	rememberReport(&review.Report{Findings: []review.Finding{f}}, cfg)
	if r := loadLastReport(ctx); r != nil {
		if _, ok := findInReport(r, f.ID); ok {
			t.Error("a report without a repository was kept as this repository's last review")
		}
	}
}

// Reports hold evidence, so they are readable only by the user, even when an
// older report was world-readable.
func TestRememberReport_Private(t *testing.T) {
	ctx, _ := baselineTestEnv(t)
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	root := repoRoot(ctx)
	if root == "" {
		t.Skip("not in a git repository")
	}
	path, err := lastReportPath(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	rememberReport(&review.Report{Repo: review.RepoInfo{Root: root}}, cfg)
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", p, got, want)
		}
	}
}
