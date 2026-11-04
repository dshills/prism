package gitctx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractFiles(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
+import "fmt"
diff --git a/util.go b/util.go
--- a/util.go
+++ b/util.go
@@ -5,3 +5,4 @@
+func helper() {}
`
	files := extractFiles(diff)
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	if files[0] != "main.go" {
		t.Errorf("files[0] = %q, want %q", files[0], "main.go")
	}
	if files[1] != "util.go" {
		t.Errorf("files[1] = %q, want %q", files[1], "util.go")
	}
}

func TestExtractFiles_Dedup(t *testing.T) {
	diff := `+++ b/main.go
+++ b/main.go
`
	files := extractFiles(diff)
	if len(files) != 1 {
		t.Errorf("got %d files, want 1 (should dedup)", len(files))
	}
}

// diffOf is a diff touching each of paths.
func diffOf(paths ...string) string {
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,3 +1,4 @@\n+x\n", p, p, p, p)
	}
	return b.String()
}

func TestSelectPaths(t *testing.T) {
	all := []string{"main.go", "pkg/a.go", "pkg/deep/b.go", "vendor/github.com/x/y.go", "web/dist/js/app.js", "docs/guide.md"}
	for _, c := range []struct {
		name             string
		include, exclude []string
		want             []string
	}{
		{"defaults", []string{"**/*"}, []string{"vendor/**", "**/*.gen.go", "**/dist/**"},
			[]string{"main.go", "pkg/a.go", "pkg/deep/b.go", "docs/guide.md"}},
		// git's own "**/*.go" pathspec left out root files.
		{"include with globstar keeps root files", []string{"**/*.go"}, nil,
			[]string{"main.go", "pkg/a.go", "pkg/deep/b.go", "vendor/github.com/x/y.go"}},
		{"include a directory", []string{"pkg"}, nil, []string{"pkg/a.go", "pkg/deep/b.go"}},
		{"exclude a nested tree", nil, []string{"pkg/**"},
			[]string{"main.go", "vendor/github.com/x/y.go", "web/dist/js/app.js", "docs/guide.md"}},
		{"exclude by file name", nil, []string{"*.md", "*.js"},
			[]string{"main.go", "pkg/a.go", "pkg/deep/b.go", "vendor/github.com/x/y.go"}},
		{"no patterns", nil, nil, all},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := extractFiles(selectPaths(diffOf(all...), DiffOptions{Include: c.include, Exclude: c.exclude}))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("kept %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchesAny(t *testing.T) {
	tests := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"vendor/lib.go", []string{"vendor/**"}, true},
		{"vendor/github.com/x/y/z.go", []string{"vendor/**"}, true},
		{"main.go", []string{"vendor/**"}, false},
		{"foo.gen.go", []string{"**/*.gen.go"}, true},
		{"pkg/foo.gen.go", []string{"**/*.gen.go"}, true},
		{"dist/bundle.js", []string{"**/dist/**"}, true},
		{"web/dist/js/app.js", []string{"**/dist/**"}, true},
		{"internal/review/testdata/wafereval/x/after/y.ts", []string{"internal/review/testdata/**"}, true},
		{"internal/review/engine.go", []string{"internal/review/testdata/**"}, false},
		{"internal/review/engine.go", []string{"internal/review"}, true},
		{"internal/reviewer/x.go", []string{"internal/review"}, false},
		{"main.go", []string{"*.go"}, true},
		{"cmd/prism/main.go", []string{"*.go"}, true},
		{"README.md", []string{"*.go"}, false},
		{"main.go", nil, false},
	}
	for _, tt := range tests {
		got := MatchesAny(tt.path, tt.patterns)
		if got != tt.want {
			t.Errorf("MatchesAny(%q, %v) = %v, want %v", tt.path, tt.patterns, got, tt.want)
		}
	}
}

func TestSnippet_NoBase(t *testing.T) {
	content := "package main\n\nfunc main() {}\n"
	result, err := Snippet(content, "main.go", "go", "")
	if err != nil {
		t.Fatalf("Snippet error: %v", err)
	}
	if result.Mode != "snippet" {
		t.Errorf("Mode = %q, want %q", result.Mode, "snippet")
	}
	if len(result.Files) != 1 || result.Files[0] != "main.go" {
		t.Errorf("Files = %v, want [main.go]", result.Files)
	}
	if !strings.Contains(result.Diff, "+package main") {
		t.Error("Diff should contain added lines")
	}
	if !strings.Contains(result.Diff, "+++ b/main.go") {
		t.Error("Diff should contain file path header")
	}
}

func TestBuildDiffArgs(t *testing.T) {
	opts := DiffOptions{
		ContextLines: 5,
		Include:      []string{"*.go"},
	}
	args := buildDiffArgs(opts)
	if args[0] != "-U5" {
		t.Errorf("args[0] = %q, want %q", args[0], "-U5")
	}
	// Should contain -- separator
	found := false
	for _, a := range args {
		if a == "--" {
			found = true
		}
	}
	if !found {
		t.Error("args should contain -- separator")
	}
	// Include patterns are not pathspecs: the diff is filtered afterwards.
	if args[len(args)-1] != "--" {
		t.Errorf("args = %v, want no pathspecs after --", args)
	}
}

func TestBuildDiffArgs_DefaultInclude(t *testing.T) {
	opts := DiffOptions{
		ContextLines: 3,
		Include:      []string{"**/*"},
	}
	args := buildDiffArgs(opts)
	// **/* should NOT be passed to git (it's the default "include all")
	for _, a := range args {
		if a == "**/*" {
			t.Error("**/* should not be passed as a git path filter")
		}
	}
}

func TestBuildDiffArgs_NoContextLines(t *testing.T) {
	opts := DiffOptions{
		ContextLines: 0,
		Include:      []string{"*.go"},
	}
	args := buildDiffArgs(opts)
	// Should not have -U flag
	for _, a := range args {
		if strings.HasPrefix(a, "-U") {
			t.Error("Should not have -U flag with ContextLines=0")
		}
	}
}

func TestBuildResult_ExcludeBeforeTruncate(t *testing.T) {
	// Build a diff with a large excluded section and a small included section
	smallDiff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n+line\n"
	largeDiff := "diff --git a/vendor/big.go b/vendor/big.go\n--- a/vendor/big.go\n+++ b/vendor/big.go\n@@ -1,3 +1,4 @@\n+" + strings.Repeat("x", 500) + "\n"
	diff := largeDiff + smallDiff

	opts := DiffOptions{
		MaxDiffBytes: 100, // Very small limit
		Exclude:      []string{"vendor/**"},
	}
	result, err := buildResult(context.Background(), diff, "unstaged", "", opts)
	if err != nil {
		t.Fatalf("buildResult error: %v", err)
	}

	// After excluding vendor/, the remaining diff should be small enough to not truncate
	if strings.Contains(result.Diff, "truncated") {
		t.Error("Diff should not be truncated after excluding vendor/")
	}
	if !strings.Contains(result.Diff, "main.go") {
		t.Error("Diff should still contain main.go")
	}
}

func TestBuildResult_Truncation(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n+" + strings.Repeat("x", 200) + "\n"
	opts := DiffOptions{
		MaxDiffBytes: 50,
	}
	result, err := buildResult(context.Background(), diff, "unstaged", "", opts)
	if err != nil {
		t.Fatalf("buildResult error: %v", err)
	}
	if !strings.Contains(result.Diff, "truncated") {
		t.Error("Large diff should be truncated")
	}
	// FR-1: the cut is counted, and the reviewed size excludes the marker.
	if want := len(diff) - 50; result.TruncatedBytes != want {
		t.Errorf("TruncatedBytes = %d, want %d", result.TruncatedBytes, want)
	}
	if result.ReviewedBytes() != 50 {
		t.Errorf("ReviewedBytes = %d, want 50", result.ReviewedBytes())
	}
}

func TestBuildResult_NotTruncated(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n+++ b/main.go\n@@ -0,0 +1 @@\n+ok\n"
	result, err := buildResult(context.Background(), diff, "unstaged", "", DiffOptions{MaxDiffBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if result.TruncatedBytes != 0 || result.ReviewedBytes() != len(diff) {
		t.Errorf("TruncatedBytes = %d, ReviewedBytes = %d", result.TruncatedBytes, result.ReviewedBytes())
	}
}

func TestBuildResult_MetadataAndMode(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n+ok\n"
	result, err := buildResult(context.Background(), diff, "staged", "abc..def", DiffOptions{})
	if err != nil {
		t.Fatalf("buildResult error: %v", err)
	}
	if result.Mode != "staged" {
		t.Errorf("Mode = %q, want %q", result.Mode, "staged")
	}
	if result.Range != "abc..def" {
		t.Errorf("Range = %q, want %q", result.Range, "abc..def")
	}
	if len(result.Files) != 1 || result.Files[0] != "main.go" {
		t.Errorf("Files = %v, want [main.go]", result.Files)
	}
}

func TestSnippet_WithBase(t *testing.T) {
	base := "package main\n\nfunc main() {}\n"
	content := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n"
	result, err := Snippet(content, "main.go", "go", base)
	if err != nil {
		t.Fatalf("Snippet error: %v", err)
	}
	if result.Mode != "snippet" {
		t.Errorf("Mode = %q, want %q", result.Mode, "snippet")
	}
	if result.Diff == "" {
		t.Error("Diff should not be empty when base differs from content")
	}
}

func TestSnippet_EmptyPath(t *testing.T) {
	content := "x := 42\n"
	result, err := Snippet(content, "", "", "")
	if err != nil {
		t.Fatalf("Snippet error: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0] != "" {
		t.Errorf("Files = %v, want path as provided", result.Files)
	}
}

func TestExtractFiles_Empty(t *testing.T) {
	files := extractFiles("")
	if len(files) != 0 {
		t.Errorf("got %d files from empty diff, want 0", len(files))
	}
}

func TestMatchesAny_EmptyPatterns(t *testing.T) {
	if MatchesAny("main.go", nil) {
		t.Error("matchesAny with nil patterns should return false")
	}
	if MatchesAny("main.go", []string{}) {
		t.Error("matchesAny with empty patterns should return false")
	}
}

// setupTestRepo creates a temp git repo with some tracked files and returns
// the path. Caller must defer cleanup.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "checkout", "-b", "main")

	// Create source files
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "util.go"), []byte("package main\n\nfunc helper() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile util.go: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatalf("MkdirAll vendor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "lib.go"), []byte("package vendor\n"), 0o644); err != nil {
		t.Fatalf("WriteFile vendor/lib.go: %v", err)
	}

	run("git", "add", "-A")
	run("git", "commit", "-m", "init")

	return dir
}

// The path filters match nested files: "dir/**" covers every depth, and an
// include of "**/*.go" keeps the files at the root, which git's pathspec of
// the same text left out.
func TestStaged_NestedGlobs(t *testing.T) {
	dir := setupTestRepo(t)
	for path, body := range map[string]string{
		"main.go":                             "package main\n\nfunc main() { helper() }\n",
		"pkg/deep/x.go":                       "package deep\n",
		"vendor/github.com/x/y/z.go":          "package y\n",
		"internal/review/testdata/a/b/c.ts":   "export const x = 1;\n",
		"internal/review/testdata/a/notes.md": "# notes\n",
	} {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	for _, c := range []struct {
		name             string
		include, exclude []string
		want             []string
	}{
		{"exclude nested trees", []string{"**/*"}, []string{"vendor/**", "internal/review/testdata/**"},
			[]string{"main.go", "pkg/deep/x.go"}},
		{"include globstar keeps root files", []string{"**/*.go"}, []string{"vendor/**"},
			[]string{"main.go", "pkg/deep/x.go"}},
		{"include a directory", []string{"internal/review"}, []string{"*.md"},
			[]string{"internal/review/testdata/a/b/c.ts"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := Staged(t.Context(), DiffOptions{Include: c.include, Exclude: c.exclude, NoAutoExclude: true})
			if err != nil {
				t.Fatal(err)
			}
			// Files is read from the filtered diff, so it is what the review sees.
			if strings.Join(r.Files, ",") != strings.Join(c.want, ",") {
				t.Errorf("files = %v, want %v", r.Files, c.want)
			}
		})
	}
}

func TestWalkFiles(t *testing.T) {
	dir := setupTestRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	files, err := WalkFiles(DiffOptions{})
	if err != nil {
		t.Fatalf("WalkFiles error: %v", err)
	}

	if len(files) < 3 {
		t.Errorf("expected at least 3 files, got %d: %v", len(files), files)
	}

	// Check that files are sorted
	for i := 1; i < len(files); i++ {
		if files[i] < files[i-1] {
			t.Errorf("files not sorted: %v", files)
			break
		}
	}
}

func TestWalkFiles_WithInclude(t *testing.T) {
	dir := setupTestRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	files, err := WalkFiles(DiffOptions{Include: []string{"*.go"}})
	if err != nil {
		t.Fatalf("WalkFiles error: %v", err)
	}

	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			t.Errorf("include filter failed: got %q", f)
		}
	}
}

func TestWalkFiles_WithExclude(t *testing.T) {
	dir := setupTestRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	files, err := WalkFiles(DiffOptions{Exclude: []string{"vendor/**"}})
	if err != nil {
		t.Fatalf("WalkFiles error: %v", err)
	}

	for _, f := range files {
		if strings.HasPrefix(f, "vendor/") {
			t.Errorf("exclude filter failed: got %q", f)
		}
	}
}

func TestCodebase(t *testing.T) {
	dir := setupTestRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	result, err := Codebase(context.Background(), DiffOptions{})
	if err != nil {
		t.Fatalf("Codebase error: %v", err)
	}

	if result.Mode != "codebase" {
		t.Errorf("Mode = %q, want %q", result.Mode, "codebase")
	}

	if len(result.Files) == 0 {
		t.Error("Expected at least one file")
	}

	// Check synthetic diff format
	if !strings.Contains(result.Diff, "diff --git") {
		t.Error("Diff should contain diff headers")
	}
	if !strings.Contains(result.Diff, "+++ b/") {
		t.Error("Diff should contain +++ b/ headers")
	}
	if !strings.Contains(result.Diff, "+package main") {
		t.Error("Diff should contain file contents as added lines")
	}
}

func TestListCommits(t *testing.T) {
	dir := setupTestRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Get the initial commit SHA
	initSHA := run("git", "rev-parse", "HEAD")

	// Add two more commits
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile a.go: %v", err)
	}
	run("git", "add", "a.go")
	run("git", "commit", "-m", "add a.go")

	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile b.go: %v", err)
	}
	run("git", "add", "b.go")
	run("git", "commit", "-m", "add b.go")

	commits, err := ListCommits(initSHA+"..HEAD", false)
	if err != nil {
		t.Fatalf("ListCommits error: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(commits))
	}

	// Oldest first
	if commits[0].Subject != "add a.go" {
		t.Errorf("commits[0].Subject = %q, want %q", commits[0].Subject, "add a.go")
	}
	if commits[1].Subject != "add b.go" {
		t.Errorf("commits[1].Subject = %q, want %q", commits[1].Subject, "add b.go")
	}

	// SHAs should be 40-char hex
	if len(commits[0].SHA) != 40 {
		t.Errorf("SHA length = %d, want 40", len(commits[0].SHA))
	}
}

func TestListCommits_EmptyRange(t *testing.T) {
	dir := setupTestRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	commits, err := ListCommits("HEAD..HEAD", false)
	if err != nil {
		t.Fatalf("ListCommits error: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("got %d commits for empty range, want 0", len(commits))
	}
}

func TestCodebase_MaxDiffBytes(t *testing.T) {
	dir := setupTestRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	result, err := Codebase(context.Background(), DiffOptions{MaxDiffBytes: 100})
	if err != nil {
		t.Fatalf("Codebase error: %v", err)
	}

	if len(result.Diff) > 200 { // some tolerance for the last file included
		t.Errorf("Diff should be limited by MaxDiffBytes, got %d bytes", len(result.Diff))
	}
}
