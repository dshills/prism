package gitctx

import (
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
		got := matchesAny(tt.path, tt.patterns)
		if got != tt.want {
			t.Errorf("matchesAny(%q, %v) = %v, want %v", tt.path, tt.patterns, got, tt.want)
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
