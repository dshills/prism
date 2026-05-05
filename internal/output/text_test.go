package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/review"
)

func TestTextWriter_NoFindings(t *testing.T) {
	report := &review.Report{
		Tool:     "prism",
		Version:  "1.0",
		Inputs:   review.InputInfo{Mode: "unstaged"},
		Repo:     review.RepoInfo{Root: "/tmp/repo", Branch: "main"},
		Summary:  review.Summary{},
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

func TestTextWriter_Provenance(t *testing.T) {
	report := &review.Report{
		Tool:     "prism",
		Version:  "1.0",
		Inputs:   review.InputInfo{Mode: "staged"},
		Repo:     review.RepoInfo{Root: "/tmp/repo", Branch: "main"},
		Summary:  review.Summary{},
		Findings: []review.Finding{},
		Provenance: []review.Provenance{
			{Provider: "anthropic", Model: "claude-opus-4-5"},
			{Provider: "openai", Model: "gpt-4o"},
		},
	}

	var buf bytes.Buffer
	w := &TextWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Reviewed by:") {
		t.Error("Output should include 'Reviewed by:' label")
	}
	if !strings.Contains(out, "anthropic/claude-opus-4-5") {
		t.Error("Output should include first provenance entry")
	}
	if !strings.Contains(out, "openai/gpt-4o") {
		t.Error("Output should include second provenance entry")
	}
}

func TestTextWriter_NoProvenance(t *testing.T) {
	report := &review.Report{
		Tool:     "prism",
		Version:  "1.0",
		Inputs:   review.InputInfo{Mode: "staged"},
		Repo:     review.RepoInfo{Root: "/tmp/repo", Branch: "main"},
		Summary:  review.Summary{},
		Findings: []review.Finding{},
	}

	var buf bytes.Buffer
	w := &TextWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	if strings.Contains(buf.String(), "Reviewed by:") {
		t.Error("Output should not include 'Reviewed by:' when provenance is empty")
	}
}

func TestTextWriter_WithCommitSHA(t *testing.T) {
	findings := []review.Finding{
		{
			Severity:   review.SeverityMedium,
			Category:   review.CategoryBug,
			Title:      "Possible nil deref",
			Message:    "Check for nil",
			Confidence: 0.85,
			Locations: []review.Location{
				{
					Path:   "main.go",
					Lines:  review.LineRange{Start: 10, End: 12},
					Commit: "abc1234",
				},
			},
		},
	}
	report := &review.Report{
		Tool:     "prism",
		Version:  "1.0",
		Inputs:   review.InputInfo{Mode: "range"},
		Repo:     review.RepoInfo{Root: "/tmp/repo", Branch: "main"},
		Summary:  review.ComputeSummary(findings),
		Findings: findings,
	}

	var buf bytes.Buffer
	w := &TextWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "(abc1234)") {
		t.Errorf("Output should contain commit SHA (abc1234), got:\n%s", out)
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
