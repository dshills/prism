package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
)

// goFile is a diff section for x/x.go whose post-image runs from line 1:
// a header hunk, then a second hunk starting at line 40.
const goFile = "diff --git a/x/x.go b/x/x.go\n" +
	"--- a/x/x.go\n" +
	"+++ b/x/x.go\n" +
	"@@ -1,4 +1,4 @@\n" +
	" package x\n" +
	" \n" +
	"-func Old() {}\n" +
	"+func New() {}\n" +
	" // end of header\n" +
	"@@ -40,4 +40,5 @@ func Sum(xs []int) int {\n" +
	" \ttotal := 0\n" +
	"+\tfor i := 0; i <= len(xs); i++ {\n" +
	" \t\ttotal += xs[i]\n" +
	" \t}\n" +
	" \treturn total\n"

func finding(path string, start int, evidence string) Finding {
	return Finding{
		Title:     "t",
		Evidence:  evidence,
		Locations: []Location{{Path: path, Lines: LineRange{Start: start, End: start}}},
	}
}

func TestVerifyEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		f          Finding
		kept       bool
		reason     string
		wantLines  LineRange
		wantTags   []string
		wantNoTags []string
	}{
		{
			name: "found in place: kept, lines unchanged",
			f:    finding("x/x.go", 41, "for i := 0; i <= len(xs); i++ {"),
			kept: true, wantLines: LineRange{41, 41}, wantNoTags: []string{TagLocationCorrected, TagUnverified},
		},
		{
			name: "found 40 lines away: lines corrected",
			f:    finding("x/x.go", 1, "for i := 0; i <= len(xs); i++ {"),
			kept: true, wantLines: LineRange{41, 41}, wantTags: []string{TagLocationCorrected},
		},
		{
			name:   "absent: discarded",
			f:      finding("x/x.go", 41, "p.f[0] = -1"),
			reason: "quoted evidence not found in x/x.go",
		},
		{
			name:   "path not in diff: discarded",
			f:      finding("elsewhere.go", 1, "anything"),
			reason: "path not in diff",
		},
		{
			name: "no evidence: kept, unverified",
			f:    finding("x/x.go", 41, ""),
			kept: true, wantLines: LineRange{41, 41}, wantTags: []string{TagUnverified},
		},
		{
			// The lines between two hunks are not in the diff, so a quote
			// across the boundary is not contiguous code.
			name:   "spans two hunks: discarded",
			f:      finding("x/x.go", 4, "// end of header\ntotal := 0"),
			reason: "quoted evidence not found in x/x.go",
		},
		{
			name: "whitespace differences ignored",
			f:    finding("x/x.go", 41, "   for i := 0;   i <= len(xs);\ti++ {  "),
			kept: true, wantLines: LineRange{41, 41},
		},
		{
			name: "diff markers copied into the quote",
			f:    finding("x/x.go", 41, "+\tfor i := 0; i <= len(xs); i++ {"),
			kept: true, wantLines: LineRange{41, 41},
		},
		{
			name: "removed line only: kept, lines unchanged",
			f:    finding("x/x.go", 90, "func Old() {}"),
			kept: true, wantLines: LineRange{90, 90}, wantNoTags: []string{TagLocationCorrected},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, discarded := verifyEvidence([]Finding{tc.f}, goFile)
			if !tc.kept {
				if len(kept) != 0 || len(discarded) != 1 || discarded[0].Reason != tc.reason {
					t.Fatalf("kept %v, discarded %+v; want discarded with %q", kept, discarded, tc.reason)
				}
				return
			}
			if len(kept) != 1 || len(discarded) != 0 {
				t.Fatalf("kept %v, discarded %+v; want kept", kept, discarded)
			}
			if got := kept[0].Locations[0].Lines; got != tc.wantLines {
				t.Errorf("lines = %+v, want %+v", got, tc.wantLines)
			}
			for _, tag := range tc.wantTags {
				if !slices.Contains(kept[0].Tags, tag) {
					t.Errorf("tags %v lack %s", kept[0].Tags, tag)
				}
			}
			for _, tag := range tc.wantNoTags {
				if slices.Contains(kept[0].Tags, tag) {
					t.Errorf("tags %v should not have %s", kept[0].Tags, tag)
				}
			}
		})
	}
}

// With the evidence in two places, the one nearest the stated line is used.
func TestVerifyEvidence_NearestMatch(t *testing.T) {
	diff := "diff --git a/d.go b/d.go\n+++ b/d.go\n@@ -0,0 +1,60 @@\n" +
		strings.Repeat("+x := 1\n", 1) + strings.Repeat("+other()\n", 49) + "+x := 1\n" + strings.Repeat("+other()\n", 9)
	kept, _ := verifyEvidence([]Finding{finding("d.go", 48, "x := 1")}, diff)
	if got := kept[0].Locations[0].Lines.Start; got != 51 {
		t.Errorf("start = %d, want 51 (nearest of lines 1 and 51 to 48)", got)
	}
}

// Correcting a finding's lines must not alias the caller's Locations slice.
func TestVerifyEvidence_DoesNotMutateInput(t *testing.T) {
	in := []Finding{finding("x/x.go", 1, "for i := 0; i <= len(xs); i++ {")}
	verifyEvidence(in, goFile)
	if in[0].Locations[0].Lines.Start != 1 || len(in[0].Tags) != 0 {
		t.Errorf("input mutated: %+v", in[0])
	}
}

func TestIsCompileClaim(t *testing.T) {
	for _, tc := range []struct {
		f    Finding
		want bool
	}{
		{Finding{Title: "Invalid method call wg.Go causes compilation failure", Locations: []Location{{Path: "a.go"}}}, true},
		{Finding{Title: "x", Tags: []string{"compiler-error"}, Locations: []Location{{Path: "a.go"}}}, true},
		{Finding{Title: "undefined: foo", Locations: []Location{{Path: "a.go"}}}, true},
		// A finding that only discusses compilation in its message is not a
		// claim: the self-review lost a valid finding this way.
		{Finding{Title: "Tree matching excludes local dependency modules", Message: "a valid compile error finding can be discarded", Locations: []Location{{Path: "a.go"}}}, false},
		{Finding{Title: "sync.WaitGroup has no method Go", Locations: []Location{{Path: "a.go"}}}, true},
		{Finding{Title: "This won't compile", Locations: []Location{{Path: "a.ts"}}}, false}, // Go only
		{Finding{Title: "Off-by-one in loop", Locations: []Location{{Path: "a.go"}}}, false},
	} {
		if got := isCompileClaim(tc.f); got != tc.want {
			t.Errorf("isCompileClaim(%q, %q, %v) = %v, want %v", tc.f.Title, tc.f.Message, tc.f.Tags, got, tc.want)
		}
	}
}

// scriptGit answers the tree-match git commands for one working-tree
// scenario.
type scriptGit struct {
	untracked       bool            // untracked .go files in the module
	unstagedChanges bool            // the tree differs from the index
	tipDiffers      map[string]bool // the tree differs from these revisions
}

func (g scriptGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	switch {
	case args[0] == "ls-files":
		if g.untracked {
			return "extra/new.go\n", nil
		}
		return "", nil
	case args[0] == "diff" && args[2] == "--": // diff --quiet -- <specs>: tree vs index
		if g.unstagedChanges {
			return "", errors.New("exit 1")
		}
		return "", nil
	case args[0] == "diff": // diff --quiet <tip> -- <specs>
		if g.tipDiffers[args[2]] {
			return "", errors.New("exit 1")
		}
		return "", nil
	}
	return "", errors.New("unexpected git " + strings.Join(args, " "))
}

type fakeGo struct {
	available bool
	vetErr    map[string]error // by directory suffix; missing = passes
	sleep     time.Duration
	calls     map[string]int
	files     []string // the package's build files; nil = the usual test names
}

func (g *fakeGo) Available() bool { return g.available }

func (g *fakeGo) PackageFiles(context.Context, string) ([]string, error) {
	if g.files != nil {
		return g.files, nil
	}
	return []string{"a.go", "b.go", "c.go", "x.go", "y.go"}, nil
}
func (g *fakeGo) Vet(ctx context.Context, dir string) error {
	g.calls[dir]++
	if g.sleep > 0 {
		select {
		case <-time.After(g.sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for suffix, err := range g.vetErr {
		if strings.HasSuffix(dir, suffix) {
			return err
		}
	}
	return nil
}

func compileClaim(path string) Finding {
	return Finding{Title: "wg.Go does not compile", Locations: []Location{{Path: path}}}
}

func env(mode string, git GitRunner, goRun *fakeGo) TreeEnv {
	return TreeEnv{RepoRoot: "/repo", Mode: mode, TipRev: "HEAD", Git: git, Go: goRun,
		PerPackageTimeout: time.Second, TotalBudget: 10 * time.Second}
}

func TestVerifyCompileClaims(t *testing.T) {
	clean := scriptGit{}
	for _, tc := range []struct {
		name    string
		mode    string
		tip     string
		git     scriptGit
		goRun   *fakeGo
		discard bool
		tag     string
	}{
		{"unstaged, vet passes: discarded", "unstaged", "", clean, &fakeGo{available: true}, true, ""},
		{"unstaged, untracked .go in the module: unverified", "unstaged", "", scriptGit{untracked: true}, &fakeGo{available: true}, false, TagUnverified},
		{"staged, module clean: discarded", "staged", "", clean, &fakeGo{available: true}, true, ""},
		{"staged, module has unstaged edits: unverified", "staged", "", scriptGit{unstagedChanges: true}, &fakeGo{available: true}, false, TagUnverified},
		{"range ending at HEAD: discarded", "range", "HEAD", clean, &fakeGo{available: true}, true, ""},
		{"range whose tip is not HEAD but matches the tree: discarded", "range", "feature", clean, &fakeGo{available: true}, true, ""},
		{"range whose tip differs from the tree: unverified", "range", "stale", scriptGit{tipDiffers: map[string]bool{"stale": true}}, &fakeGo{available: true}, false, TagUnverified},
		{"codebase: discarded", "codebase", "", clean, &fakeGo{available: true}, true, ""},
		{"snippet: never checked", "snippet", "", clean, &fakeGo{available: true}, false, TagUnverified},
		{"vet fails: kept, vet-failed", "unstaged", "", clean, &fakeGo{available: true, vetErr: map[string]error{"pkg": errors.New("undefined: Go")}}, false, TagVetFailed},
		{"go missing: unverified", "unstaged", "", clean, &fakeGo{available: false}, false, TagUnverified},
		{"module with local dependencies: unverified", "localdeps", "", clean, &fakeGo{available: true}, false, TagUnverified},
		{"file excluded by build constraints: unverified", "unstaged", "", clean, &fakeGo{available: true, files: []string{"b.go"}}, false, TagUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.goRun.calls = map[string]int{}
			mode := tc.mode
			e := env(mode, tc.git, tc.goRun)
			if mode == "localdeps" {
				e = env("unstaged", tc.git, tc.goRun)
				e.LocalDeps = func(string) bool { return true }
			}
			e.TipRev = tc.tip
			kept, discarded := verifyCompileClaims(context.Background(), []Finding{compileClaim("pkg/a.go")}, e)
			if tc.discard {
				if len(discarded) != 1 || !strings.HasPrefix(discarded[0].Reason, "package compiles: go vet passed in pkg") {
					t.Fatalf("kept %v, discarded %+v; want discarded", kept, discarded)
				}
				return
			}
			if len(kept) != 1 || !slices.Contains(kept[0].Tags, tc.tag) {
				t.Fatalf("kept %+v, discarded %+v; want kept with %s", kept, discarded, tc.tag)
			}
			if tc.tag == TagUnverified && len(tc.goRun.calls) != 0 {
				t.Errorf("vet ran (%v) although the claim could not be checked", tc.goRun.calls)
			}
		})
	}
}

func TestInputPathspecs(t *testing.T) {
	if got := trackedInputPathspecs("."); got[0] != ":(glob)**" || got[1] != ":(glob)go.work" {
		t.Errorf("root tracked = %v", got)
	}
	if got := trackedInputPathspecs("backend"); got[0] != ":(glob)backend/**" {
		t.Errorf("nested tracked = %v", got)
	}
	got := untrackedInputPathspecs("backend")
	for _, want := range []string{":(glob)backend/**/*.go", ":(glob)backend/**/*.c", ":(glob)backend/**/*.h", ":(glob)backend/**/*.s", ":(glob)backend/**/*.syso", ":(glob)backend/**/go.mod", ":(glob)go.work"} {
		if !slices.Contains(got, want) {
			t.Errorf("untracked specs %v lack %s", got, want)
		}
	}
}

func TestStampCommit(t *testing.T) {
	in := []Discard{{Finding: finding("a.go", 3, ""), Reason: "r"}}
	out := StampCommit(in, "abc1234")
	if out[0].Finding.Locations[0].Commit != "abc1234" || in[0].Finding.Locations[0].Commit != "" {
		t.Errorf("stamped %+v, input %+v", out[0].Finding.Locations, in[0].Finding.Locations)
	}
}

func TestModuleRootOnDisk(t *testing.T) {
	repo := t.TempDir()
	for _, d := range []string{"backend/internal/pedigree", "tools"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "backend", "go.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := moduleRootOnDisk(repo)
	if got := root("backend/internal/pedigree"); got != "backend" {
		t.Errorf("nested package: %q, want backend", got)
	}
	if got := root("tools"); got != "." {
		t.Errorf("no module above: %q, want .", got)
	}
}

// FR-5 before FR-6, through the real entry point: a compile claim whose quote
// is not in the diff is discarded for that reason and never reaches vet; one
// whose quote is present goes on to vet.
func TestVerifyFindings_EvidenceBeforeCompile(t *testing.T) {
	g := &fakeGo{available: true, calls: map[string]int{}}
	saved := treeEnvFor
	t.Cleanup(func() { treeEnvFor = saved })
	treeEnvFor = func(gitctx.DiffResult) TreeEnv { return env("unstaged", scriptGit{}, g) }

	absent := finding("x/x.go", 41, "wg.Go(func() {")
	absent.Title = "wg.Go does not compile"
	present := finding("x/x.go", 41, "for i := 0; i <= len(xs); i++ {")
	present.Title = "this loop does not compile"

	kept, discarded := VerifyFindings(context.Background(), []Finding{absent, present}, gitctx.DiffResult{Diff: goFile, Mode: "unstaged"}, config.Default())
	if len(kept) != 0 || len(discarded) != 2 {
		t.Fatalf("kept %+v, discarded %+v", kept, discarded)
	}
	if !strings.HasPrefix(discarded[0].Reason, "quoted evidence not found") {
		t.Errorf("first discard should be the evidence failure, got %q", discarded[0].Reason)
	}
	if !strings.HasPrefix(discarded[1].Reason, "package compiles") {
		t.Errorf("second discard should come from vet, got %q", discarded[1].Reason)
	}
	if len(g.calls) != 1 {
		t.Errorf("vet calls %v, want exactly one (for the claim that survived the evidence check)", g.calls)
	}
}

// Non-claims pass through untouched and never run vet.
func TestVerifyCompileClaims_NonClaimsUntouched(t *testing.T) {
	g := &fakeGo{available: true, calls: map[string]int{}}
	f := Finding{Title: "Off-by-one", Locations: []Location{{Path: "pkg/a.go"}}}
	kept, _ := verifyCompileClaims(context.Background(), []Finding{f}, env("unstaged", scriptGit{}, g))
	if len(kept) != 1 || len(kept[0].Tags) != 0 || len(g.calls) != 0 {
		t.Errorf("kept %+v, vet calls %v", kept, g.calls)
	}
}

// One vet per directory, however many claims point into it.
func TestVerifyCompileClaims_MemoisedPerDirectory(t *testing.T) {
	g := &fakeGo{available: true, calls: map[string]int{}}
	claims := []Finding{compileClaim("pkg/a.go"), compileClaim("pkg/b.go"), compileClaim("other/c.go")}
	_, discarded := verifyCompileClaims(context.Background(), claims, env("unstaged", scriptGit{}, g))
	if len(discarded) != 3 || len(g.calls) != 2 {
		t.Errorf("discarded %d, vet calls %v; want 3 discarded from 2 vets", len(discarded), g.calls)
	}
}

// A vet that times out leaves the claim unverified; once the total budget is
// spent, remaining claims are not checked.
func TestVerifyCompileClaims_TimeoutAndBudget(t *testing.T) {
	g := &fakeGo{available: true, sleep: 50 * time.Millisecond, calls: map[string]int{}}
	e := env("unstaged", scriptGit{}, g)
	e.PerPackageTimeout = 10 * time.Millisecond
	e.TotalBudget = 5 * time.Millisecond
	claims := []Finding{compileClaim("a/x.go"), compileClaim("b/y.go")}
	kept, discarded := verifyCompileClaims(context.Background(), claims, e)
	if len(discarded) != 0 || len(kept) != 2 {
		t.Fatalf("kept %d, discarded %d", len(kept), len(discarded))
	}
	for _, k := range kept {
		if !slices.Contains(k.Tags, TagUnverified) {
			t.Errorf("%s: tags %v, want unverified", primaryLoc(k).Path, k.Tags)
		}
	}
	if len(g.calls) != 1 {
		t.Errorf("vet ran for %v; the second claim should have hit the spent budget", g.calls)
	}
}

func TestRangeTip(t *testing.T) {
	for in, want := range map[string]string{
		"origin/main..HEAD": "HEAD", "a..feature": "feature", "a...b": "b", "a..": "HEAD", "": "HEAD", "abc123": "abc123",
	} {
		if got := rangeTip(in); got != want {
			t.Errorf("rangeTip(%q) = %q, want %q", in, got, want)
		}
	}
}

// With verification off, findings pass through with no tags and nothing is
// discarded.
func TestVerifyFindings_Disabled(t *testing.T) {
	off := false
	cfg := config.Default()
	cfg.VerifyFindings = &off
	in := []Finding{finding("nowhere.go", 1, "not in the diff")}
	kept, discarded := VerifyFindings(context.Background(), in, gitctx.DiffResult{Diff: goFile}, cfg)
	if len(kept) != 1 || len(discarded) != 0 || len(kept[0].Tags) != 0 {
		t.Errorf("kept %+v, discarded %+v", kept, discarded)
	}
}

func TestLocalDepsOnDisk(t *testing.T) {
	repo := t.TempDir()
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/go.mod", "module a\n\nrequire example.com/x v1.0.0\n")
	write("b/go.mod", "module b\n\nreplace example.com/lib => ../lib\n")
	deps := localDepsOnDisk(repo)
	if deps("a") || !deps("b") {
		t.Errorf("a: %v (want false), b: %v (want true)", deps("a"), deps("b"))
	}
	write("go.work", "go 1.22\nuse ./a\n")
	if !deps("a") {
		t.Error("a go.work at the root should count as local dependencies")
	}
}

func TestStepTimeout(t *testing.T) {
	e := TreeEnv{PerPackageTimeout: 60 * time.Second, TotalBudget: 120 * time.Second}
	if got := stepTimeout(e, 119*time.Second); got != time.Second {
		t.Errorf("near the budget: %v, want 1s", got)
	}
	if got := stepTimeout(e, 0); got != 60*time.Second {
		t.Errorf("fresh: %v, want 60s", got)
	}
}
