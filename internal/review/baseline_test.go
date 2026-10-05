package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
)

func TestBaselinePath(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	abs := filepath.Join(string(filepath.Separator), "etc", "accepted.json")
	for _, tc := range []struct {
		file, root, want string
	}{
		{"", root, filepath.Join(root, DefaultBaselineFile)},
		{"ci/baseline.json", root, filepath.Join(root, "ci", "baseline.json")},
		{abs, root, abs},
		{"none", root, ""},
		{"NONE", root, ""},
		{"", "", DefaultBaselineFile}, // no repo (a GitHub PR review): the working directory
	} {
		cfg := config.Default()
		cfg.BaselineFile = tc.file
		if got := BaselinePath(cfg, tc.root); got != tc.want {
			t.Errorf("BaselinePath(%q, %q) = %q, want %q", tc.file, tc.root, got, tc.want)
		}
	}
}

func TestLoadBaseline(t *testing.T) {
	dir := t.TempDir()
	b, err := LoadBaseline(filepath.Join(dir, "missing.json"))
	if err != nil || len(b.Findings) != 0 {
		t.Fatalf("missing file: %v, %d findings; want an empty baseline", err, len(b.Findings))
	}

	// A broken baseline fails the review instead of reporting every
	// accepted finding again.
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(bad); err == nil {
		t.Error("a corrupt baseline should be an error")
	}
}

// Save sorts by path then ID, so the committed file diffs cleanly, and the
// file round-trips.
func TestBaseline_SaveSortsAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultBaselineFile)
	b := &Baseline{}
	b.Add(BaselineEntry{ID: "ff", Path: "z.go"})
	b.Add(BaselineEntry{ID: "bb", Path: "a.go"})
	b.Add(BaselineEntry{ID: "aa", Path: "a.go"})
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got.Findings {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "aa,bb,ff" || got.Version != baselineVersion {
		t.Errorf("saved %v (version %d), want aa,bb,ff sorted by path then ID", ids, got.Version)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(data), "}\n") {
		t.Error("the file should end with a newline")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("Save left %d files in the directory, want only the baseline", len(entries))
	}
}

// A failed save leaves the existing baseline as it was.
func TestBaseline_FailedSaveKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultBaselineFile)
	old := &Baseline{}
	old.Add(BaselineEntry{ID: "keep"})
	if err := old.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // no new files in dir
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	b := &Baseline{}
	b.Add(BaselineEntry{ID: "new"})
	if err := b.Save(path); err == nil {
		t.Skip("the directory is still writable (running as root?)")
	}
	got, err := LoadBaseline(path)
	if err != nil || len(got.Findings) != 1 || got.Findings[0].ID != "keep" {
		t.Errorf("after a failed save: %+v, %v; want the old baseline intact", got, err)
	}
}

func TestBaseline_AddRemove(t *testing.T) {
	b := &Baseline{}
	if !b.Add(BaselineEntry{ID: "a", Reason: "old"}) {
		t.Error("first Add should report a new ID")
	}
	if b.Add(BaselineEntry{ID: "a", Reason: "new"}) {
		t.Error("adding the same ID again should update, not add")
	}
	if len(b.Findings) != 1 || b.Findings[0].Reason != "new" {
		t.Errorf("findings = %+v, want one entry with the new reason", b.Findings)
	}
	if !b.Has("a") || !b.Remove("a") || b.Has("a") || b.Remove("a") {
		t.Error("Remove should drop the entry once")
	}
}

func TestApplyBaseline(t *testing.T) {
	keep := evidenceFinding("a.go", CategoryBug, "keep", 1, "x")
	drop := evidenceFinding("a.go", CategoryBug, "drop", 2, "y")
	keep.ID, drop.ID = "k", "d"
	b := &Baseline{Findings: []BaselineEntry{{ID: "d", Reason: "accepted risk"}}}

	kept, suppressed := applyBaseline([]Finding{keep, drop}, b)
	if len(kept) != 1 || kept[0].ID != "k" {
		t.Errorf("kept = %v, want only k", kept)
	}
	if len(suppressed) != 1 || suppressed[0].Finding.ID != "d" || suppressed[0].Source != SuppressedByBaseline || suppressed[0].Reason != "accepted risk" {
		t.Errorf("suppressed = %+v, want d from the baseline with its reason", suppressed)
	}
}

// End to end: a baselined finding and an inline-ignored one are left out of
// the findings (and so the exit code) and listed as suppressed, on a fresh
// review and on a cache replay alike.
func TestRun_SuppressesBaselineAndInline(t *testing.T) {
	rev := &fileReviewer{}
	useProvider(t, rev)
	cfg := chunkTestConfig(t)
	root := t.TempDir()
	diff := threeChunkDiff("beta // prism:ignore bug \"tracked in #12\"")
	diff.Repo = gitctx.RepoMeta{Root: root}
	ctx := context.Background()

	first, err := Run(ctx, diff, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(findingPaths(first), ","); got != "d1/a.go,d3/c.go" {
		t.Fatalf("findings = %s, want d2/b.go suppressed inline", got)
	}
	if len(first.Suppressed) != 1 || first.Suppressed[0].Source != SuppressedInline || first.Suppressed[0].Reason != "tracked in #12" {
		t.Fatalf("suppressed = %+v, want d2/b.go by its prism:ignore", first.Suppressed)
	}

	// Accept d1/a.go's finding in the baseline at the repo root.
	var accepted Finding
	for _, f := range first.Findings {
		if findingPath(f) == "d1/a.go" {
			accepted = f
		}
	}
	b := &Baseline{}
	b.Add(EntryFor(accepted, "accepted", "2026-10-05"))
	if err := b.Save(filepath.Join(root, DefaultBaselineFile)); err != nil {
		t.Fatal(err)
	}

	again, err := Run(ctx, diff, cfg) // replayed from cache
	if err != nil {
		t.Fatal(err)
	}
	if !again.Coverage.CacheHit {
		t.Error("expected a cache replay")
	}
	if got := strings.Join(findingPaths(again), ","); got != "d3/c.go" || again.Summary.Counts.Medium != 1 {
		t.Errorf("findings = %s (%d medium), want only d3/c.go", got, again.Summary.Counts.Medium)
	}
	sources := map[string]int{}
	for _, s := range again.Suppressed {
		sources[s.Source]++
	}
	if sources[SuppressedByBaseline] != 1 || sources[SuppressedInline] != 1 {
		t.Errorf("suppressed by source = %v, want 1 baseline and 1 inline", sources)
	}

	// --baseline none reports the baselined finding again.
	cfg.BaselineFile = "none"
	all, err := Run(ctx, diff, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Findings) != 2 {
		t.Errorf("with the baseline off: %d findings, want 2", len(all.Findings))
	}
}

func TestRun_CorruptBaselineIsAnError(t *testing.T) {
	useProvider(t, &fileReviewer{})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, DefaultBaselineFile), []byte("[oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := threeChunkDiff("beta")
	diff.Repo = gitctx.RepoMeta{Root: root}
	if _, err := Run(context.Background(), diff, chunkTestConfig(t)); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("err = %v, want a baseline parse error", err)
	}
}
