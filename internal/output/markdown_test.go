package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/review"
)

func TestMarkdownWriter_Empty(t *testing.T) {
	report := &review.Report{
		Tool:     "prism",
		Version:  "1.0",
		Inputs:   review.InputInfo{Mode: "unstaged"},
		Findings: []review.Finding{},
		Summary:  review.ComputeSummary(nil),
	}

	var buf bytes.Buffer
	w := &MarkdownWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "## Prism Code Review") {
		t.Error("Missing heading")
	}
	if !strings.Contains(out, "No issues found") {
		t.Error("Expected 'No issues found' for empty report")
	}
	if !strings.Contains(out, "| **Total** | **0** |") {
		t.Error("Expected total count of 0")
	}
}

func TestMarkdownWriter_WithFindings(t *testing.T) {
	findings := []review.Finding{
		{
			ID:         "abc",
			Severity:   review.SeverityHigh,
			Category:   review.CategorySecurity,
			Title:      "SQL injection risk",
			Message:    "User input not sanitized",
			Suggestion: "Use parameterized queries",
			Confidence: 0.95,
			Locations: []review.Location{
				{Path: "db/query.go", Lines: review.LineRange{Start: 42, End: 45}},
			},
		},
		{
			ID:         "def",
			Severity:   review.SeverityMedium,
			Category:   review.CategoryBug,
			Title:      "Nil pointer",
			Message:    "Can panic on nil",
			Suggestion: "if err != nil { return err }",
			Confidence: 0.8,
			Locations: []review.Location{
				{Path: "main.go", Lines: review.LineRange{Start: 10, End: 12}},
			},
		},
		{
			ID:       "ghi",
			Severity: review.SeverityLow,
			Category: review.CategoryStyle,
			Title:    "Long line",
			Message:  "Line exceeds 120 chars",
			Locations: []review.Location{
				{Path: "util.go", Lines: review.LineRange{Start: 5, End: 5}},
			},
		},
	}

	report := &review.Report{
		Tool:     "prism",
		Version:  "1.0",
		Inputs:   review.InputInfo{Mode: "staged"},
		Summary:  review.ComputeSummary(findings),
		Findings: findings,
		Timing:   review.Timing{GitMs: 10, LLMMs: 500, TotalMs: 520},
	}

	var buf bytes.Buffer
	w := &MarkdownWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	out := buf.String()

	// Check severity counts in table
	if !strings.Contains(out, "| High     | 1    |") {
		t.Error("Missing high count")
	}
	if !strings.Contains(out, "| Medium   | 1    |") {
		t.Error("Missing medium count")
	}
	if !strings.Contains(out, "| Low      | 1    |") {
		t.Error("Missing low count")
	}

	// Check collapsible sections
	if !strings.Contains(out, "<details>") {
		t.Error("Missing collapsible details")
	}
	if !strings.Contains(out, "HIGH (1)") {
		t.Error("Missing HIGH severity section")
	}
	if !strings.Contains(out, "MEDIUM (1)") {
		t.Error("Missing MEDIUM severity section")
	}

	// Check finding content
	if !strings.Contains(out, "### SQL injection risk") {
		t.Error("Missing finding title")
	}
	if !strings.Contains(out, "db/query.go:42-45") {
		t.Error("Missing location")
	}

	// Check code suggestion is in a code fence (contains "if err != nil")
	if !strings.Contains(out, "```go") {
		t.Error("Expected go code fence for suggestion with code")
	}

	// Check timing footer
	if !strings.Contains(out, "520ms") {
		t.Error("Missing timing")
	}
}

func TestMarkdownWriter_SuggestionNonCode(t *testing.T) {
	report := &review.Report{
		Tool:    "prism",
		Version: "1.0",
		Summary: review.ComputeSummary([]review.Finding{
			{Severity: review.SeverityLow},
		}),
		Findings: []review.Finding{
			{
				ID:         "x",
				Severity:   review.SeverityLow,
				Category:   review.CategoryDocs,
				Title:      "Missing docs",
				Message:    "Add documentation",
				Suggestion: "Consider adding a README with usage examples",
				Confidence: 0.6,
				Locations: []review.Location{
					{Path: "README.md", Lines: review.LineRange{Start: 1, End: 1}},
				},
			},
		},
	}

	var buf bytes.Buffer
	w := &MarkdownWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	out := buf.String()
	// Non-code suggestion should be in a blockquote, not a code fence
	if !strings.Contains(out, "> Consider adding a README") {
		t.Error("Expected blockquote for non-code suggestion")
	}
}

func TestLooksLikeCode(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"func main() {}", true},
		{"if err != nil { return err }", true},
		{"Add more documentation", false},
		{"var x = 42", true},
		{"Consider renaming this", false},
	}
	for _, tt := range tests {
		got := looksLikeCode(tt.input)
		if got != tt.want {
			t.Errorf("looksLikeCode(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestInferLang(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"app.py", "python"},
		{"index.ts", "typescript"},
		{"unknown.xyz", ""},
	}
	for _, tt := range tests {
		got := inferLang(tt.path)
		if got != tt.want {
			t.Errorf("inferLang(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestMdSeverityIcon(t *testing.T) {
	if mdSeverityIcon(review.SeverityHigh) != ":red_circle:" {
		t.Error("High severity should be red")
	}
	if mdSeverityIcon(review.SeverityMedium) != ":orange_circle:" {
		t.Error("Medium severity should be orange")
	}
	if mdSeverityIcon(review.SeverityLow) != ":yellow_circle:" {
		t.Error("Low severity should be yellow")
	}
}
