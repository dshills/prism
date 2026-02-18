package review

import (
	"testing"
)

func TestParseFindings_ValidJSON(t *testing.T) {
	input := `[
		{
			"severity": "high",
			"category": "bug",
			"title": "Null pointer dereference",
			"message": "Variable x may be nil",
			"suggestion": "Add nil check",
			"confidence": 0.9,
			"path": "main.go",
			"startLine": 10,
			"endLine": 12,
			"tags": ["go", "nil"]
		},
		{
			"severity": "low",
			"category": "style",
			"title": "Unused variable",
			"message": "Variable y is declared but never used",
			"suggestion": "Remove the variable",
			"confidence": 1.0,
			"path": "main.go",
			"startLine": 20,
			"endLine": 20,
			"tags": []
		}
	]`

	findings, err := parseFindings(input)
	if err != nil {
		t.Fatalf("parseFindings error: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(findings))
	}

	f := findings[0]
	if f.Severity != SeverityHigh {
		t.Errorf("finding[0].Severity = %q, want %q", f.Severity, SeverityHigh)
	}
	if f.Category != CategoryBug {
		t.Errorf("finding[0].Category = %q, want %q", f.Category, CategoryBug)
	}
	if f.Title != "Null pointer dereference" {
		t.Errorf("finding[0].Title = %q", f.Title)
	}
	if len(f.Locations) != 1 {
		t.Fatalf("finding[0] has %d locations, want 1", len(f.Locations))
	}
	if f.Locations[0].Path != "main.go" {
		t.Errorf("finding[0].Locations[0].Path = %q", f.Locations[0].Path)
	}
	if f.Locations[0].Lines.Start != 10 || f.Locations[0].Lines.End != 12 {
		t.Errorf("finding[0] lines = %d-%d, want 10-12",
			f.Locations[0].Lines.Start, f.Locations[0].Lines.End)
	}
	if f.ID == "" {
		t.Error("finding[0].ID should be generated")
	}
}

func TestParseFindings_EmptyArray(t *testing.T) {
	findings, err := parseFindings("[]")
	if err != nil {
		t.Fatalf("parseFindings error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("got %d findings, want 0", len(findings))
	}
}

func TestParseFindings_MarkdownFences(t *testing.T) {
	input := "```json\n[{\"severity\":\"low\",\"category\":\"style\",\"title\":\"test\",\"message\":\"msg\",\"suggestion\":\"fix\",\"confidence\":0.5,\"path\":\"a.go\",\"startLine\":1,\"endLine\":1,\"tags\":[]}]\n```"
	findings, err := parseFindings(input)
	if err != nil {
		t.Fatalf("parseFindings with markdown fences error: %v", err)
	}
	if len(findings) != 1 {
		t.Errorf("got %d findings, want 1", len(findings))
	}
}

func TestParseFindings_InvalidJSON(t *testing.T) {
	_, err := parseFindings("not json at all")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestGenerateFindingID_Stable(t *testing.T) {
	f := Finding{
		Title: "Test finding",
		Locations: []Location{
			{Path: "main.go", Lines: LineRange{Start: 10, End: 12}},
		},
	}
	id1 := generateFindingID(f)
	id2 := generateFindingID(f)
	if id1 != id2 {
		t.Errorf("Finding IDs should be stable: %s != %s", id1, id2)
	}
}

func TestGenerateFindingID_Different(t *testing.T) {
	f1 := Finding{
		Title: "Finding A",
		Locations: []Location{
			{Path: "main.go", Lines: LineRange{Start: 10}},
		},
	}
	f2 := Finding{
		Title: "Finding B",
		Locations: []Location{
			{Path: "main.go", Lines: LineRange{Start: 10}},
		},
	}
	if generateFindingID(f1) == generateFindingID(f2) {
		t.Error("Different findings should have different IDs")
	}
}
