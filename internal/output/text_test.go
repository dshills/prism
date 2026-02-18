package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/review"
)

func TestTextWriter_NoFindings(t *testing.T) {
	report := &review.Report{
		Tool:    "prism",
		Version: "1.0",
		Inputs:  review.InputInfo{Mode: "unstaged"},
		Repo:    review.RepoInfo{Root: "/tmp/repo", Branch: "main"},
		Summary: review.Summary{},
		Findings: []review.Finding{},
	}

	var buf bytes.Buffer
	w := &TextWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "unstaged") {
		t.Error("Output should mention mode")
	}
	if !strings.Contains(out, "Findings: 0 total") {
		t.Error("Output should show zero findings")
	}
	if !strings.Contains(out, "No issues found") {
		t.Error("Output should say no issues found")
	}
}

func TestTextWriter_WithFindings(t *testing.T) {
	report := &review.Report{
		Tool:    "prism",
		Version: "1.0",
		Inputs:  review.InputInfo{Mode: "staged"},
		Repo:    review.RepoInfo{Root: "/tmp/repo", Branch: "main"},
		Summary: review.ComputeSummary([]review.Finding{
			{
				Severity:   review.SeverityHigh,
				Category:   review.CategoryBug,
				Title:      "Null pointer",
				Message:    "x could be nil here",
				Suggestion: "Add a nil check",
				Locations: []review.Location{
					{Path: "main.go", Lines: review.LineRange{Start: 10, End: 12}},
				},
				Confidence: 0.95,
			},
			{
				Severity:   review.SeverityLow,
				Category:   review.CategoryStyle,
				Title:      "Long line",
				Message:    "Line exceeds 120 characters",
				Suggestion: "Break it up",
				Locations: []review.Location{
					{Path: "util.go", Lines: review.LineRange{Start: 5, End: 5}},
				},
				Confidence: 0.8,
			},
		}),
		Findings: []review.Finding{
			{
				Severity:   review.SeverityHigh,
				Category:   review.CategoryBug,
				Title:      "Null pointer",
				Message:    "x could be nil here",
				Suggestion: "Add a nil check",
				Locations: []review.Location{
					{Path: "main.go", Lines: review.LineRange{Start: 10, End: 12}},
				},
				Confidence: 0.95,
			},
			{
				Severity:   review.SeverityLow,
				Category:   review.CategoryStyle,
				Title:      "Long line",
				Message:    "Line exceeds 120 characters",
				Suggestion: "Break it up",
				Locations: []review.Location{
					{Path: "util.go", Lines: review.LineRange{Start: 5, End: 5}},
				},
				Confidence: 0.8,
			},
		},
		Timing: review.Timing{GitMs: 5, LLMMs: 1000, TotalMs: 1005},
	}

	var buf bytes.Buffer
	w := &TextWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "1 high") {
		t.Error("Output should show high count")
	}
	if !strings.Contains(out, "Null pointer") {
		t.Error("Output should contain finding title")
	}
	if !strings.Contains(out, "main.go:10-12") {
		t.Error("Output should show file:line range")
	}
	if !strings.Contains(out, "Suggestion:") {
		t.Error("Output should show suggestion")
	}
	if !strings.Contains(out, "HIGH") {
		t.Error("Output should have HIGH section")
	}
	if !strings.Contains(out, "LOW") {
		t.Error("Output should have LOW section")
	}
}
