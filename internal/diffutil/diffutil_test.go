package diffutil

import (
	"strings"
	"testing"
)

func TestSplitSections_TwoFiles(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n+line1\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n+line2\n"
	sections := SplitSections(diff)
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

func TestSplitSections_WhitespaceOnly(t *testing.T) {
	sections := SplitSections("   \n\t\n  ")
	if len(sections) != 0 {
		t.Errorf("got %d sections for whitespace-only, want 0", len(sections))
	}
}

func TestSplitSections_Empty(t *testing.T) {
	if SplitSections("") != nil {
		t.Error("empty diff should return nil")
	}
}

func TestPathFromSection_Valid(t *testing.T) {
	section := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n+import\n"
	path := PathFromSection(section)
	if path != "main.go" {
		t.Errorf("PathFromSection = %q, want %q", path, "main.go")
	}
}

func TestPathFromSection_NoHeader(t *testing.T) {
	section := "diff --git a/main.go b/main.go\nsome content without +++ header\n"
	if PathFromSection(section) != "" {
		t.Error("PathFromSection should return empty for section without +++ b/ header")
	}
}

func TestPostImageLines_MultiHunk(t *testing.T) {
	section := "diff --git a/x.go b/x.go\n" +
		"index 111..222 100644\n" +
		"--- a/x.go\n" +
		"+++ b/x.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" package x\n" +
		"-old()\n" +
		"+new()\n" +
		" end()\n" +
		"@@ -40,2 +40,3 @@ func later() {\n" +
		" a := 1\n" +
		"+b := 2\n" +
		"\\ No newline at end of file\n"
	got := PostImageLines(section)
	want := []Line{
		{Text: "package x", NewLine: 1, Hunk: 1},
		{Text: "old()", Removed: true, Hunk: 1},
		{Text: "new()", NewLine: 2, Hunk: 1},
		{Text: "end()", NewLine: 3, Hunk: 1},
		{Text: "a := 1", NewLine: 40, Hunk: 2},
		{Text: "b := 2", NewLine: 41, Hunk: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPostImageLines_CodebaseStyleAndHeaderlessInput(t *testing.T) {
	got := PostImageLines("diff --git a/n.go b/n.go\n+++ b/n.go\n@@ -0,0 +1,2 @@\n+one\n+two\n")
	if len(got) != 2 || got[0].NewLine != 1 || got[1].NewLine != 2 {
		t.Errorf("codebase section = %+v", got)
	}
	if lines := PostImageLines("+++ b/x\n+not in a hunk\n"); len(lines) != 0 {
		t.Errorf("lines outside a hunk were returned: %+v", lines)
	}
}
