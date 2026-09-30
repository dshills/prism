package review

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/gitctx"
)

// findingsJSON renders model output: one finding per (path, line, title, evidence).
func findingsJSON(t *testing.T, fs ...[4]any) string {
	t.Helper()
	var raw []rawFinding
	for _, f := range fs {
		raw = append(raw, rawFinding{
			Severity: "high", Category: "bug", Title: f[2].(string), Message: f[2].(string),
			Suggestion: "fix", Confidence: 0.95, Path: f[0].(string),
			StartLine: f[1].(int), EndLine: f[1].(int), Evidence: f[3].(string),
		})
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Discarded findings are reported separately and count toward nothing.
func TestRun_DiscardedExcludedFromSummary(t *testing.T) {
	fakeOpenAI(t, always(findingsJSON(t,
		[4]any{"x/x.go", 41, "Off-by-one in loop", "for i := 0; i <= len(xs); i++ {"},
		[4]any{"x/x.go", 1, "Unknown parent must be -1", "p.f[0] = -1"},
	)))
	r, err := Run(context.Background(), diffResult(goFile, "x/x.go"), coverageConfig(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.Findings[0].Title != "Off-by-one in loop" {
		t.Errorf("findings = %+v", r.Findings)
	}
	if r.Summary.Counts.High != 1 {
		t.Errorf("summary counts the discarded finding: %+v", r.Summary.Counts)
	}
	if len(r.Discarded) != 1 || r.Discarded[0].Reason != "quoted evidence not found in x/x.go" {
		t.Errorf("discarded = %+v", r.Discarded)
	}
}

// FR-8: the cache holds findings from before verification. A second run with
// verification turned off replays both, including the one the first run
// discarded, without calling the model.
func TestRun_CacheStoresUnverifiedFindings(t *testing.T) {
	calls := fakeOpenAI(t, always(findingsJSON(t,
		[4]any{"x/x.go", 41, "Off-by-one in loop", "for i := 0; i <= len(xs); i++ {"},
		[4]any{"x/x.go", 1, "Unknown parent must be -1", "p.f[0] = -1"},
	)))
	cfg := coverageConfig(t, true)
	d := diffResult(goFile, "x/x.go")
	if r, err := Run(context.Background(), d, cfg); err != nil || len(r.Discarded) != 1 {
		t.Fatalf("first run: %v, discarded %+v", err, r)
	}
	off := false
	cfg.VerifyFindings = &off
	r, err := Run(context.Background(), d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 2 || len(r.Discarded) != 0 || !r.Coverage.CacheHit {
		t.Errorf("replay without verification: findings %d, discarded %d, cacheHit %v", len(r.Findings), len(r.Discarded), r.Coverage.CacheHit)
	}
	if calls.Load() != 1 {
		t.Errorf("model called %d times, want 1", calls.Load())
	}
}

// goRepo makes a git repo holding a Go module whose package p vets cleanly,
// with an unstaged edit to p/p.go, and chdirs into it.
func goRepo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("git", "init", "-q", "-b", "main")
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("p/p.go", "package p\n\n// X is a value.\nvar X = 1\n")
	run("git", "add", "-A")
	run("git", "commit", "-q", "-m", "init")
	write("p/p.go", "package p\n\n// X is a value.\nvar X = 2\n")
	t.Chdir(dir)
}

// AC 7: a Go compile claim on a package that vets cleanly is discarded when the
// working tree holds the reviewed code (unstaged mode), and kept as
// unverified when it cannot be checked (snippet mode). Real git and go vet.
func TestRun_CompileClaimCheckedWithGoVet(t *testing.T) {
	goRepo(t)
	fakeOpenAI(t, always(findingsJSON(t,
		[4]any{"p/p.go", 4, "X does not compile", "var X = 2"},
	)))
	cfg := coverageConfig(t, false)

	d, err := gitctx.Unstaged(context.Background(), gitctx.DiffOptions{ContextLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 0 || len(r.Discarded) != 1 || r.Discarded[0].Reason != "package compiles: go vet passed in p" {
		t.Fatalf("unstaged: findings %+v, discarded %+v", r.Findings, r.Discarded)
	}

	snip, err := gitctx.Snippet("package p\n\n// X is a value.\nvar X = 2\n", "p/p.go", "go", "")
	if err != nil {
		t.Fatal(err)
	}
	r, err = Run(context.Background(), snip, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || !slices.Contains(r.Findings[0].Tags, TagUnverified) || len(r.Discarded) != 0 {
		t.Fatalf("snippet: findings %+v, discarded %+v", r.Findings, r.Discarded)
	}
	if !strings.Contains(snip.Diff, "var X = 2") {
		t.Fatal("snippet diff does not carry the code under review")
	}
}
