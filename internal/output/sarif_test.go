package output

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dshills/prism/internal/review"
)

func TestSARIFWriter_Empty(t *testing.T) {
	report := &review.Report{
		Tool:     "prism",
		Version:  "1.0",
		Inputs:   review.InputInfo{Mode: "unstaged"},
		Findings: []review.Finding{},
	}

	var buf bytes.Buffer
	w := &SARIFWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	var sarif sarifLog
	if err := json.Unmarshal(buf.Bytes(), &sarif); err != nil {
		t.Fatalf("Invalid SARIF JSON: %v", err)
	}
	if sarif.Version != "2.1.0" {
		t.Errorf("Version = %q, want %q", sarif.Version, "2.1.0")
	}
	if len(sarif.Runs) != 1 {
		t.Fatalf("Runs count = %d, want 1", len(sarif.Runs))
	}
	if len(sarif.Runs[0].Results) != 0 {
		t.Errorf("Results count = %d, want 0", len(sarif.Runs[0].Results))
	}
}

func TestSARIFWriter_WithFindings(t *testing.T) {
	report := &review.Report{
		Tool:    "prism",
		Version: "1.0",
		Inputs:  review.InputInfo{Mode: "staged"},
		Findings: []review.Finding{
			{
				ID:         "abc",
				Severity:   review.SeverityHigh,
				Category:   review.CategorySecurity,
				Title:      "SQL injection",
				Message:    "User input is not sanitized",
				Suggestion: "Use parameterized queries",
				Confidence: 0.95,
				Locations: []review.Location{
					{Path: "db/query.go", Lines: review.LineRange{Start: 42, End: 45}},
				},
				Tags: []string{"sql", "security"},
			},
			{
				ID:       "def",
				Severity: review.SeverityLow,
				Category: review.CategoryStyle,
				Title:    "Long function",
				Message:  "Function exceeds 100 lines",
				Locations: []review.Location{
					{Path: "main.go", Lines: review.LineRange{Start: 1, End: 120}},
				},
			},
		},
	}

	var buf bytes.Buffer
	w := &SARIFWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	var sarif sarifLog
	if err := json.Unmarshal(buf.Bytes(), &sarif); err != nil {
		t.Fatalf("Invalid SARIF JSON: %v", err)
	}

	run := sarif.Runs[0]

	// Check results
	if len(run.Results) != 2 {
		t.Fatalf("Results count = %d, want 2", len(run.Results))
	}

	// High severity -> error level
	if run.Results[0].Level != "error" {
		t.Errorf("Results[0].Level = %q, want %q", run.Results[0].Level, "error")
	}
	if run.Results[0].Message.Text != "User input is not sanitized" {
		t.Errorf("Results[0].Message = %q", run.Results[0].Message.Text)
	}

	// Check locations
	if len(run.Results[0].Locations) != 1 {
		t.Fatalf("Results[0] has %d locations, want 1", len(run.Results[0].Locations))
	}
	loc := run.Results[0].Locations[0].PhysicalLocation
	if loc.ArtifactLocation.URI != "db/query.go" {
		t.Errorf("URI = %q, want %q", loc.ArtifactLocation.URI, "db/query.go")
	}
	if loc.Region.StartLine != 42 || loc.Region.EndLine != 45 {
		t.Errorf("Region = %d-%d, want 42-45", loc.Region.StartLine, loc.Region.EndLine)
	}

	// Check fixes (suggestion)
	if len(run.Results[0].Fixes) != 1 {
		t.Fatalf("Results[0] has %d fixes, want 1", len(run.Results[0].Fixes))
	}
	if run.Results[0].Fixes[0].Description.Text != "Use parameterized queries" {
		t.Errorf("Fix text = %q", run.Results[0].Fixes[0].Description.Text)
	}

	// Low severity -> note level
	if run.Results[1].Level != "note" {
		t.Errorf("Results[1].Level = %q, want %q", run.Results[1].Level, "note")
	}

	// Check rules
	if len(run.Tool.Driver.Rules) != 2 {
		t.Fatalf("Rules count = %d, want 2", len(run.Tool.Driver.Rules))
	}

	// Check driver metadata
	if run.Tool.Driver.Name != "prism" {
		t.Errorf("Driver name = %q, want %q", run.Tool.Driver.Name, "prism")
	}
}

func TestSeverityToLevel(t *testing.T) {
	tests := []struct {
		severity review.Severity
		want     string
	}{
		{review.SeverityHigh, "error"},
		{review.SeverityMedium, "warning"},
		{review.SeverityLow, "note"},
		{review.Severity("unknown"), "note"},
	}
	for _, tt := range tests {
		got := severityToLevel(tt.severity)
		if got != tt.want {
			t.Errorf("severityToLevel(%q) = %q, want %q", tt.severity, got, tt.want)
		}
	}
}

func TestGenerateRuleID_Stable(t *testing.T) {
	f := review.Finding{
		Category: review.CategoryBug,
		Title:    "Null pointer",
	}
	id1 := generateRuleID(f)
	id2 := generateRuleID(f)
	if id1 != id2 {
		t.Errorf("Rule IDs should be stable: %q != %q", id1, id2)
	}
	if id1 == "" {
		t.Error("Rule ID should not be empty")
	}
}

func TestSARIFWriter_Provenance(t *testing.T) {
	report := &review.Report{
		Tool:    "prism",
		Version: "1.0",
		Inputs:  review.InputInfo{Mode: "staged"},
		Findings: []review.Finding{
			{
				ID:         "abc",
				Severity:   review.SeverityHigh,
				Category:   review.CategorySecurity,
				Title:      "Hardcoded secret",
				Message:    "API key in source",
				Confidence: 0.9,
				Provider:   "anthropic",
				Model:      "claude-opus-4-5",
				Locations: []review.Location{
					{Path: "config.go", Lines: review.LineRange{Start: 1, End: 2}},
				},
			},
			{
				ID:         "def",
				Severity:   review.SeverityLow,
				Category:   review.CategoryStyle,
				Title:      "Long line",
				Message:    "Line exceeds limit",
				Confidence: 0.6,
				Provider:   "openai",
				Model:      "gpt-5.2",
				Locations: []review.Location{
					{Path: "main.go", Lines: review.LineRange{Start: 3, End: 3}},
				},
			},
		},
		Provenance: []review.Provenance{
			{AIGenerated: true, Provider: "anthropic", Model: "claude-opus-4-5"},
			{AIGenerated: true, Provider: "openai", Model: "gpt-5.2"},
		},
	}

	var buf bytes.Buffer
	w := &SARIFWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	var sarif sarifLog
	if err := json.Unmarshal(buf.Bytes(), &sarif); err != nil {
		t.Fatalf("Invalid SARIF JSON: %v", err)
	}
	run := sarif.Runs[0]

	// Driver properties carry the full provenance list.
	if run.Tool.Driver.Properties == nil {
		t.Fatalf("Expected driver.properties._provenance to be set")
	}
	if len(run.Tool.Driver.Properties.Provenance) != 2 {
		t.Errorf("Driver provenance count = %d, want 2", len(run.Tool.Driver.Properties.Provenance))
	}

	// Extensions list each model in provenance order.
	if len(run.Tool.Extensions) != 2 {
		t.Fatalf("Extensions count = %d, want 2", len(run.Tool.Extensions))
	}
	if run.Tool.Extensions[0].Name != "anthropic/claude-opus-4-5" {
		t.Errorf("Extensions[0].Name = %q", run.Tool.Extensions[0].Name)
	}
	if run.Tool.Extensions[0].Properties == nil || !run.Tool.Extensions[0].Properties.AIGenerated {
		t.Errorf("Expected ai_generated=true on extension properties")
	}

	// Results carry per-finding provenance and link to the right extension.
	if len(run.Results) != 2 {
		t.Fatalf("Results count = %d, want 2", len(run.Results))
	}
	r0 := run.Results[0]
	if r0.Properties == nil {
		t.Fatalf("Expected result[0].properties to be set")
	}
	if !r0.Properties.AIGenerated {
		t.Errorf("Expected ai_generated=true on result[0]")
	}
	if r0.Properties.Provider != "anthropic" || r0.Properties.Model != "claude-opus-4-5" {
		t.Errorf("result[0] provenance = %s/%s", r0.Properties.Provider, r0.Properties.Model)
	}
	r1 := run.Results[1]
	if r1.Properties == nil || r1.Properties.Provider != "openai" || r1.Properties.Model != "gpt-5.2" {
		t.Errorf("result[1] provenance = %+v", r1.Properties)
	}
	// Ensure the extension component for each result's (provider, model) exists
	// so SARIF consumers can resolve it by name lookup.
	var names []string
	for _, e := range run.Tool.Extensions {
		names = append(names, e.Name)
	}
	wantNames := map[string]bool{"anthropic/claude-opus-4-5": false, "openai/gpt-5.2": false}
	for _, n := range names {
		if _, ok := wantNames[n]; ok {
			wantNames[n] = true
		}
	}
	for n, found := range wantNames {
		if !found {
			t.Errorf("expected extension %q in %v", n, names)
		}
	}
}

func TestSARIFWriter_NoProvenance(t *testing.T) {
	// Legacy/test reports without provenance should produce no extensions
	// and no properties blocks — preserving the prior output shape.
	report := &review.Report{
		Tool:    "prism",
		Version: "1.0",
		Inputs:  review.InputInfo{Mode: "staged"},
		Findings: []review.Finding{
			{
				ID:       "abc",
				Severity: review.SeverityLow,
				Category: review.CategoryStyle,
				Title:    "Trivial",
				Message:  "msg",
				Locations: []review.Location{
					{Path: "a.go", Lines: review.LineRange{Start: 1, End: 1}},
				},
			},
		},
	}
	var buf bytes.Buffer
	w := &SARIFWriter{}
	if err := w.Write(&buf, report); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	var sarif sarifLog
	if err := json.Unmarshal(buf.Bytes(), &sarif); err != nil {
		t.Fatalf("Invalid SARIF JSON: %v", err)
	}
	run := sarif.Runs[0]
	if len(run.Tool.Extensions) != 0 {
		t.Errorf("Expected no extensions, got %d", len(run.Tool.Extensions))
	}
	if run.Tool.Driver.Properties != nil {
		t.Errorf("Expected no driver properties, got %+v", run.Tool.Driver.Properties)
	}
	if run.Results[0].Properties != nil {
		t.Errorf("Expected no result properties, got %+v", run.Results[0].Properties)
	}
}

func TestGenerateRuleID_Different(t *testing.T) {
	f1 := review.Finding{Category: review.CategoryBug, Title: "Bug A"}
	f2 := review.Finding{Category: review.CategoryBug, Title: "Bug B"}
	if generateRuleID(f1) == generateRuleID(f2) {
		t.Error("Different findings should have different rule IDs")
	}
}
