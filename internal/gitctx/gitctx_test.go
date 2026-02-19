package gitctx

import (
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

func TestFilterExcluded(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
+import "fmt"
diff --git a/vendor/lib.go b/vendor/lib.go
--- a/vendor/lib.go
+++ b/vendor/lib.go
@@ -1,3 +1,4 @@
+package lib
`
	result := filterExcluded(diff, []string{"vendor/**"})
	if strings.Contains(result, "vendor/lib.go") {
		t.Error("vendor/lib.go should be excluded")
	}
	if !strings.Contains(result, "main.go") {
		t.Error("main.go should be kept")
	}
}

func TestMatchesAny(t *testing.T) {
	tests := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"vendor/lib.go", []string{"vendor/**"}, true},
		{"main.go", []string{"vendor/**"}, false},
		{"foo.gen.go", []string{"**/*.gen.go"}, true},
		{"pkg/foo.gen.go", []string{"**/*.gen.go"}, true},
		{"dist/bundle.js", []string{"**/dist/**"}, true},
		{"main.go", []string{"*.go"}, true},
	}
	for _, tt := range tests {
		got := MatchesAny(tt.path, tt.patterns)
		if got != tt.want {
			t.Errorf("MatchesAny(%q, %v) = %v, want %v", tt.path, tt.patterns, got, tt.want)
		}
	}
}

func TestSplitDiffSections(t *testing.T) {
	diff := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
+line1
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1,3 +1,4 @@
+line2
`
	sections := splitDiffSections(diff)
	if len(sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(sections))
	}
	if !strings.Contains(sections[0], "a.go") {
		t.Error("section 0 should contain a.go")
	}
	if !strings.Contains(sections[1], "b.go") {
		t.Error("section 1 should contain b.go")
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
	// Should contain the include pattern
	if args[len(args)-1] != "*.go" {
		t.Errorf("last arg = %q, want %q", args[len(args)-1], "*.go")
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

func TestExtractPathFromSection(t *testing.T) {
	section := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n+import\n"
	path := extractPathFromSection(section)
	if path != "main.go" {
		t.Errorf("extractPathFromSection = %q, want %q", path, "main.go")
	}
}

func TestExtractPathFromSection_NoPath(t *testing.T) {
	section := "diff --git a/main.go b/main.go\nsome other content\n"
	path := extractPathFromSection(section)
	if path != "" {
		t.Errorf("extractPathFromSection = %q, want empty", path)
	}
}

func TestFilterFileList(t *testing.T) {
	files := []string{"main.go", "vendor/lib.go", "pkg/util.go", "dist/bundle.js"}
	result := filterFileList(files, []string{"vendor/**", "**/dist/**"})
	if len(result) != 2 {
		t.Fatalf("filterFileList got %d files, want 2", len(result))
	}
	if result[0] != "main.go" {
		t.Errorf("result[0] = %q, want %q", result[0], "main.go")
	}
	if result[1] != "pkg/util.go" {
		t.Errorf("result[1] = %q, want %q", result[1], "pkg/util.go")
	}
}

func TestFilterFileList_Empty(t *testing.T) {
	result := filterFileList(nil, []string{"vendor/**"})
	if len(result) != 0 {
		t.Errorf("filterFileList nil input got %d, want 0", len(result))
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
	result, err := buildResult(diff, "unstaged", "", opts)
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
	result, err := buildResult(diff, "unstaged", "", opts)
	if err != nil {
		t.Fatalf("buildResult error: %v", err)
	}
	if !strings.Contains(result.Diff, "truncated") {
		t.Error("Large diff should be truncated")
	}
}

func TestBuildResult_MetadataAndMode(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n+ok\n"
	result, err := buildResult(diff, "staged", "abc..def", DiffOptions{})
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
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "util.go"), []byte("package main\n\nfunc helper() {}\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "vendor"), 0o755)
	os.WriteFile(filepath.Join(dir, "vendor", "lib.go"), []byte("package vendor\n"), 0o644)

	run("git", "add", "-A")
	run("git", "commit", "-m", "init")

	return dir
}

func TestWalkFiles(t *testing.T) {
	dir := setupTestRepo(t)
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

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
	os.Chdir(dir)
	defer os.Chdir(origDir)

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
	os.Chdir(dir)
	defer os.Chdir(origDir)

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
	os.Chdir(dir)
	defer os.Chdir(origDir)

	result, err := Codebase(DiffOptions{})
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
	os.Chdir(dir)
	defer os.Chdir(origDir)

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
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644)
	run("git", "add", "a.go")
	run("git", "commit", "-m", "add a.go")

	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n"), 0o644)
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
	os.Chdir(dir)
	defer os.Chdir(origDir)

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
	os.Chdir(dir)
	defer os.Chdir(origDir)

	result, err := Codebase(DiffOptions{MaxDiffBytes: 100})
	if err != nil {
		t.Fatalf("Codebase error: %v", err)
	}

	if len(result.Diff) > 200 { // some tolerance for the last file included
		t.Errorf("Diff should be limited by MaxDiffBytes, got %d bytes", len(result.Diff))
	}
}
