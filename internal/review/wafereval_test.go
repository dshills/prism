package review_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/review"
)

// The seeded-bug corpus that benchmarks review models (TestWaferEval, behind
// the wafereval build tag). The tests in this file check the corpus and the
// scoring offline, so a broken case fails here rather than costing a live run.

const (
	evalDir = "testdata/wafereval"
	// defaultEvalTolerance is how many lines from a bug's anchor a finding
	// may be and still catch it.
	defaultEvalTolerance = 4
)

// evalCase is one change in the corpus. before/ is committed, after/ is laid
// over it and committed, and the review is of that commit, as
// `prism review commit HEAD` sees it. A case with no bugs is a control: the
// change is correct, so any medium or high finding on it is a false positive.
type evalCase struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Bugs        []evalBug `json:"bugs"`
	dir         string
}

// evalBug is a bug seeded in a case's after/ tree. A finding catches it when
// one of its locations is in Path and overlaps the Tolerance lines around
// the line holding Anchor, and its title, message or suggestion contains
// one of Keywords. The keywords keep a finding about a neighbouring line
// from being credited with the bug.
type evalBug struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Anchor    string   `json:"anchor"`
	Tolerance int      `json:"tolerance,omitempty"`
	Summary   string   `json:"summary"`
	Keywords  []string `json:"keywords"`
	line      int
}

// loadEvalCases reads the corpus and finds each bug's anchor line, which
// must occur exactly once in its file.
func loadEvalCases(t testing.TB) []evalCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(evalDir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []evalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("cases.json: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("cases.json: no cases")
	}
	// Results are keyed by case and bug ID, so both must be unique.
	seen := map[string]bool{}
	for _, c := range cases {
		ids := append([]string{c.ID}, bugIDs(c)...)
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("cases.json: duplicate ID %q", id)
			}
			seen[id] = true
		}
	}
	// Absolute, since building a case's diff changes the working directory.
	root, err := filepath.Abs(evalDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cases {
		c := &cases[i]
		c.dir = filepath.Join(root, c.ID)
		for j := range c.Bugs {
			b := &c.Bugs[j]
			if b.Tolerance == 0 {
				b.Tolerance = defaultEvalTolerance
			}
			src, err := os.ReadFile(filepath.Join(c.dir, "after", filepath.FromSlash(b.Path)))
			if err != nil {
				t.Fatalf("%s: %v", b.ID, err)
			}
			for n, line := range strings.Split(string(src), "\n") {
				if strings.Contains(line, b.Anchor) {
					if b.line != 0 {
						t.Fatalf("%s: anchor %q is on lines %d and %d", b.ID, b.Anchor, b.line, n+1)
					}
					b.line = n + 1
				}
			}
			if b.line == 0 {
				t.Fatalf("%s: anchor %q not in %s", b.ID, b.Anchor, b.Path)
			}
		}
	}
	return cases
}

func bugIDs(c evalCase) []string {
	ids := make([]string, len(c.Bugs))
	for i, b := range c.Bugs {
		ids[i] = b.ID
	}
	return ids
}

// evalDiffOptions are the diff options a review with cfg uses, as the CLI
// sets them.
func evalDiffOptions(cfg config.Config) gitctx.DiffOptions {
	return gitctx.DiffOptions{
		ContextLines:    cfg.ContextLines,
		MaxDiffBytes:    cfg.MaxDiffBytes,
		Include:         cfg.Include,
		Exclude:         cfg.Exclude,
		NoAutoExclude:   !cfg.ShouldAutoExclude(),
		FunctionContext: cfg.ShouldFunctionContext(),
	}
}

// buildEvalDiff commits the case's before and after trees in a new
// repository and returns the diff of the after commit. It changes the
// working directory for the rest of the test, since gitctx reads the
// repository it runs in; the review itself finds the files through the
// diff's repository root.
func buildEvalDiff(t *testing.T, c evalCase, cfg config.Config) gitctx.DiffResult {
	t.Helper()
	repo := t.TempDir()
	// Run from a git hook, these point every git command, gitctx's too, at
	// the outer repository instead of the case's. t.Setenv restores them.
	for _, v := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if _, ok := os.LookupEnv(v); ok {
			t.Setenv(v, "")
			_ = os.Unsetenv(v)
		}
	}
	git := func(args ...string) {
		t.Helper()
		// Nothing from the user's own git config: templates, excludes,
		// signing or hooks could change what is committed.
		base := []string{"-C", repo, "-c", "user.name=prism-eval", "-c", "user.email=eval@prism.test",
			"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull}
		cmd := exec.Command("git", append(base, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: git %s: %v\n%s", c.ID, strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	copyEvalTree(t, filepath.Join(c.dir, "before"), repo)
	git("add", "-A")
	git("commit", "-q", "--no-verify", "-m", "before")
	copyEvalTree(t, filepath.Join(c.dir, "after"), repo)
	git("add", "-A")
	git("commit", "-q", "--no-verify", "-m", c.Description)

	t.Chdir(repo)
	diff, err := gitctx.Commit(t.Context(), "HEAD", "", evalDiffOptions(cfg))
	if err != nil {
		t.Fatalf("%s: %v", c.ID, err)
	}
	return diff
}

// copyEvalTree copies the files under src into dst, keeping their paths.
func copyEvalTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// evalMatch is how a review's findings scored against a case.
type evalMatch struct {
	// Caught maps each caught bug's ID to the first finding that caught it.
	Caught map[string]review.Finding
	// Extra are the findings that caught no seeded bug. On a buggy case some
	// may be real issues; on a control case they are all false.
	Extra []review.Finding
}

// falsePositives is how many extra findings on a control case are medium or
// high: the ones that would have blocked or worried someone.
func (m evalMatch) falsePositives(c evalCase) int {
	if len(c.Bugs) > 0 {
		return 0
	}
	n := 0
	for _, f := range m.Extra {
		if review.SeverityRank(f.Severity) >= review.SeverityRank(review.SeverityMedium) {
			n++
		}
	}
	return n
}

func matchEval(c evalCase, findings []review.Finding) evalMatch {
	m := evalMatch{Caught: map[string]review.Finding{}}
	for _, f := range findings {
		hit := false
		for _, b := range c.Bugs {
			if b.caughtBy(f) {
				hit = true
				if _, ok := m.Caught[b.ID]; !ok {
					m.Caught[b.ID] = f
				}
			}
		}
		if !hit {
			m.Extra = append(m.Extra, f)
		}
	}
	return m
}

func (b evalBug) caughtBy(f review.Finding) bool {
	near := false
	for _, loc := range f.Locations {
		if strings.TrimPrefix(filepath.ToSlash(loc.Path), "./") != b.Path || loc.Lines.Start <= 0 {
			continue
		}
		end := max(loc.Lines.End, loc.Lines.Start)
		if loc.Lines.Start <= b.line+b.Tolerance && end >= b.line-b.Tolerance {
			near = true
			break
		}
	}
	if !near {
		return false
	}
	text := strings.ToLower(f.Title + "\n" + f.Message + "\n" + f.Suggestion)
	for _, kw := range b.Keywords {
		if strings.Contains(text, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// TestWaferEvalCorpus checks every case builds into a commit whose diff adds
// each bug's anchor line, and that the Go cases compile: a review model's
// compile claims are checked with go vet, so a case that does not build
// would be judged on the wrong thing.
func TestWaferEvalCorpus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	_, goErr := exec.LookPath("go")
	cfg := config.Default()
	for _, c := range loadEvalCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			diff := buildEvalDiff(t, c, cfg)
			if len(diff.Files) == 0 {
				t.Fatal("empty diff")
			}
			for _, b := range c.Bugs {
				if !addsLine(diff.Diff, b.Anchor) {
					t.Errorf("%s: the diff does not add the anchor line %q", b.ID, b.Anchor)
				}
			}
			if _, err := os.Stat("go.mod"); err == nil && goErr == nil {
				cmd := exec.Command("go", "vet", "./...")
				cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("go vet: %v\n%s", err, out)
				}
			}
		})
	}
}

// addsLine reports whether diff adds a line containing s.
func addsLine(diff, s string) bool {
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && strings.Contains(line, s) {
			return true
		}
	}
	return false
}

func TestWaferEvalScoring(t *testing.T) {
	c := evalCase{ID: "c", Bugs: []evalBug{
		{ID: "c/a", Path: "x.go", Tolerance: 2, Keywords: []string{"deadlock"}, line: 10},
		{ID: "c/b", Path: "x.go", Tolerance: 2, Keywords: []string{"leak"}, line: 20},
	}}
	at := func(path string, start, end int, title string) review.Finding {
		return review.Finding{Title: title, Severity: review.SeverityHigh,
			Locations: []review.Location{{Path: path, Lines: review.LineRange{Start: start, End: end}}}}
	}
	m := matchEval(c, []review.Finding{
		at("x.go", 11, 0, "Possible DEADLOCK"),  // near a, keyword: caught (End 0 is Start)
		at("x.go", 13, 13, "deadlock"),          // 3 lines away: too far
		at("y.go", 20, 20, "leak"),              // other file
		at("./x.go", 1, 40, "Connection leak"),  // spans b, keyword: caught
		at("x.go", 10, 10, "Unchecked error"),   // near a, no keyword
		at("x.go", 0, 0, "deadlock and a leak"), // no line
	})
	if _, ok := m.Caught["c/a"]; !ok {
		t.Error("c/a not caught")
	}
	if f, ok := m.Caught["c/b"]; !ok || f.Title != "Connection leak" {
		t.Errorf("c/b caught by %+v, %v", f, ok)
	}
	if len(m.Extra) != 4 {
		t.Errorf("extra = %d, want 4", len(m.Extra))
	}
	if n := m.falsePositives(c); n != 0 {
		t.Errorf("a buggy case has no false positives, got %d", n)
	}

	clean := evalCase{ID: "clean"}
	low := at("x.go", 1, 1, "nit")
	low.Severity = review.SeverityLow
	m = matchEval(clean, []review.Finding{at("x.go", 1, 1, "bug"), low})
	if n := m.falsePositives(clean); n != 1 {
		t.Errorf("control false positives = %d, want 1 (lows don't count)", n)
	}
}
