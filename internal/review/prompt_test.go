package review

import (
	"strings"
	"testing"
)

func TestBuildUserPrompt(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n+import \"fmt\"\n"
	files := []string{"main.go"}

	prompt := BuildUserPrompt(diff, files, 50, "high")

	if !strings.Contains(prompt, "BEGIN DIFF") {
		t.Error("Prompt should contain diff markers")
	}
	if !strings.Contains(prompt, diff) {
		t.Error("Prompt should contain the diff content")
	}
	if !strings.Contains(prompt, "50 findings") {
		t.Error("Prompt should mention max findings")
	}
	if !strings.Contains(prompt, "high") {
		t.Error("Prompt should mention fail-on severity")
	}
	if !strings.Contains(prompt, "Go") {
		t.Error("Prompt should detect Go language from .go files")
	}
}

func TestBuildUserPrompt_NoMaxFindings(t *testing.T) {
	prompt := BuildUserPrompt("some diff", nil, 0, "none")
	if strings.Contains(prompt, "findings") {
		t.Error("Prompt should not mention max findings when 0")
	}
}

func TestDetectLanguages(t *testing.T) {
	tests := []struct {
		files    []string
		expected []string
	}{
		{[]string{"main.go", "util.go"}, []string{"Go"}},
		{[]string{"app.py"}, []string{"Python"}},
		{[]string{"index.ts", "app.tsx"}, []string{"TypeScript", "TypeScript/React"}},
		{[]string{"README.md"}, nil},
	}

	for _, tt := range tests {
		langs := detectLanguages(tt.files)
		for _, exp := range tt.expected {
			found := false
			for _, l := range langs {
				if l == exp {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("detectLanguages(%v) missing %q, got %v", tt.files, exp, langs)
			}
		}
	}
}

func TestSystemPrompt(t *testing.T) {
	sp := SystemPrompt()
	if !strings.Contains(sp, "JSON") {
		t.Error("System prompt should mention JSON output")
	}
	if !strings.Contains(sp, "severity") {
		t.Error("System prompt should mention severity")
	}
}

func TestCodebaseSystemPrompt(t *testing.T) {
	sp := CodebaseSystemPrompt()
	if sp == "" {
		t.Fatal("CodebaseSystemPrompt should not be empty")
	}
	if !strings.Contains(sp, "source files") {
		t.Error("Codebase system prompt should mention source files")
	}
	if !strings.Contains(sp, "JSON") {
		t.Error("Codebase system prompt should mention JSON output")
	}
	if !strings.Contains(sp, "severity") {
		t.Error("Codebase system prompt should mention severity")
	}
}

func TestBuildCodebaseUserPrompt(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n+++ b/main.go\n+package main\n"
	files := []string{"main.go"}

	prompt := BuildCodebaseUserPrompt(diff, files, 50, 10, "high", nil)

	if !strings.Contains(prompt, "BEGIN SOURCE FILES") {
		t.Error("Prompt should contain source files markers")
	}
	if !strings.Contains(prompt, diff) {
		t.Error("Prompt should contain the content")
	}
	if !strings.Contains(prompt, "50 findings total") {
		t.Error("Prompt should mention max findings total")
	}
	if !strings.Contains(prompt, "10 findings per file") {
		t.Error("Prompt should mention max findings per file")
	}
	if !strings.Contains(prompt, "high") {
		t.Error("Prompt should mention fail-on severity")
	}
	if !strings.Contains(prompt, "Go") {
		t.Error("Prompt should detect Go language")
	}
}

func TestBuildCodebaseUserPrompt_NoLimits(t *testing.T) {
	prompt := BuildCodebaseUserPrompt("content", nil, 0, 0, "none", nil)
	if strings.Contains(prompt, "findings total") {
		t.Error("Prompt should not mention max findings total when 0")
	}
	if strings.Contains(prompt, "findings per file") {
		t.Error("Prompt should not mention max findings per file when 0")
	}
}
