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
