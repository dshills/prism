package output

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dshills/prism/internal/review"
)

func TestJSONWriter(t *testing.T) {
	report := &review.Report{
		Tool:    "prism",
		Version: "1.0",
		RunID:   "test-run",
		Inputs:  review.InputInfo{Mode: "unstaged"},
		Repo:    review.RepoInfo{Root: "/tmp/repo", Head: "abc123", Branch: "main"},
		Summary: review.Summary{
			Counts:          review.SeverityCounts{High: 1},
			HighestSeverity: review.SeverityHigh,
		},
		Findings: []review.Finding{
			{
				ID:       "abc",
				Severity: review.SeverityHigh,
				Category: review.CategoryBug,
				Title:    "Test",
				Message:  "Test message",
				Locations: []review.Location{
					{Path: "main.go", Lines: review.LineRange{Start: 1, End: 1}},
				},
			},
		},
	}

	var buf bytes.Buffer
	w := &JSONWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	// Verify it's valid JSON
	var parsed review.Report
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Output is not valid JSON: %v", err)
	}

	if parsed.Tool != "prism" {
		t.Errorf("Tool = %q, want %q", parsed.Tool, "prism")
	}
	if len(parsed.Findings) != 1 {
		t.Errorf("Findings count = %d, want 1", len(parsed.Findings))
	}
	if parsed.Findings[0].Title != "Test" {
		t.Errorf("Finding title = %q, want %q", parsed.Findings[0].Title, "Test")
	}
}
