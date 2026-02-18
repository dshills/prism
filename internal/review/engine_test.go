package review

import (
	"testing"
	"time"

	"github.com/dshills/prism/internal/gitctx"
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

func TestGenerateFindingID_NoLocations(t *testing.T) {
	f := Finding{Title: "No location finding"}
	id := generateFindingID(f)
	if id == "" {
		t.Error("ID should be generated even with no locations")
	}
	if len(id) != 16 { // sha256[:8] as hex = 16 chars
		t.Errorf("ID length = %d, want 16", len(id))
	}
}

func TestParseFindings_EmptyCodeFence(t *testing.T) {
	input := "```\n```"
	findings, err := parseFindings(input)
	if err != nil {
		t.Fatalf("parseFindings with empty code fence error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("got %d findings, want 0", len(findings))
	}
}

func TestParseFindings_WhitespaceOnly(t *testing.T) {
	_, err := parseFindings("   \n\t\n  ")
	if err == nil {
		t.Error("Expected error for whitespace-only input")
	}
}

func TestGenerateRunID(t *testing.T) {
	id1 := GenerateRunID()
	if id1 == "" {
		t.Error("RunID should not be empty")
	}
	if len(id1) != 32 { // sha256[:16] as hex = 32 chars
		t.Errorf("RunID length = %d, want 32", len(id1))
	}

	time.Sleep(time.Millisecond)
	id2 := GenerateRunID()
	if id1 == id2 {
		t.Error("Two RunIDs generated at different times should differ")
	}
}

func TestBuildReport(t *testing.T) {
	diff := gitctx.DiffResult{
		Mode:  "staged",
		Range: "abc..def",
		Repo: gitctx.RepoMeta{
			Root:   "/repo",
			Head:   "abc123",
			Branch: "main",
		},
	}
	findings := []Finding{
		{
			ID:       "f1",
			Severity: SeverityHigh,
			Category: CategoryBug,
			Title:    "Bug found",
			Message:  "There is a bug",
		},
		{
			ID:       "f2",
			Severity: SeverityLow,
			Category: CategoryStyle,
			Title:    "Style issue",
			Message:  "Naming could be better",
		},
	}

	r := BuildReport(diff, findings, 500, 1000)

	if r.Tool != "prism" {
		t.Errorf("Tool = %q, want %q", r.Tool, "prism")
	}
	if r.Version != "1.0" {
		t.Errorf("Version = %q, want %q", r.Version, "1.0")
	}
	if r.RunID == "" {
		t.Error("RunID should not be empty")
	}
	if r.Repo.Root != "/repo" {
		t.Errorf("Repo.Root = %q, want %q", r.Repo.Root, "/repo")
	}
	if r.Repo.Head != "abc123" {
		t.Errorf("Repo.Head = %q, want %q", r.Repo.Head, "abc123")
	}
	if r.Repo.Branch != "main" {
		t.Errorf("Repo.Branch = %q, want %q", r.Repo.Branch, "main")
	}
	if r.Inputs.Mode != "staged" {
		t.Errorf("Inputs.Mode = %q, want %q", r.Inputs.Mode, "staged")
	}
	if r.Inputs.Range != "abc..def" {
		t.Errorf("Inputs.Range = %q, want %q", r.Inputs.Range, "abc..def")
	}
	if len(r.Findings) != 2 {
		t.Fatalf("Findings count = %d, want 2", len(r.Findings))
	}
	if r.Findings[0].Title != "Bug found" {
		t.Errorf("Findings[0].Title = %q", r.Findings[0].Title)
	}
	if r.Timing.LLMMs != 500 {
		t.Errorf("Timing.LLMMs = %d, want 500", r.Timing.LLMMs)
	}
	if r.Timing.TotalMs != 1000 {
		t.Errorf("Timing.TotalMs = %d, want 1000", r.Timing.TotalMs)
	}
	// Summary should reflect findings
	if r.Summary.Counts.High != 1 {
		t.Errorf("Summary.Counts.High = %d, want 1", r.Summary.Counts.High)
	}
	if r.Summary.Counts.Low != 1 {
		t.Errorf("Summary.Counts.Low = %d, want 1", r.Summary.Counts.Low)
	}
	if r.Summary.HighestSeverity != SeverityHigh {
		t.Errorf("Summary.HighestSeverity = %q, want %q", r.Summary.HighestSeverity, SeverityHigh)
	}
}

func TestEmptyReport(t *testing.T) {
	diff := gitctx.DiffResult{
		Mode: "staged",
		Repo: gitctx.RepoMeta{
			Root:   "/repo",
			Head:   "abc123",
			Branch: "main",
		},
	}
	r := emptyReport(diff, time.Now())

	if r.Tool != "prism" {
		t.Errorf("Tool = %q, want %q", r.Tool, "prism")
	}
	if r.Version != "1.0" {
		t.Errorf("Version = %q, want %q", r.Version, "1.0")
	}
	if r.RunID == "" {
		t.Error("RunID should not be empty")
	}
	if r.Repo.Root != "/repo" {
		t.Errorf("Repo.Root = %q, want %q", r.Repo.Root, "/repo")
	}
	if r.Repo.Head != "abc123" {
		t.Errorf("Repo.Head = %q, want %q", r.Repo.Head, "abc123")
	}
	if r.Repo.Branch != "main" {
		t.Errorf("Repo.Branch = %q, want %q", r.Repo.Branch, "main")
	}
	if r.Inputs.Mode != "staged" {
		t.Errorf("Inputs.Mode = %q, want %q", r.Inputs.Mode, "staged")
	}
	if len(r.Findings) != 0 {
		t.Errorf("Findings = %d, want 0", len(r.Findings))
	}
}
