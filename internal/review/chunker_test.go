package review

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
)

func TestSplitIntoChunks_SingleFile(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
+import "fmt"
`
	chunks := SplitIntoChunks(diff, 10000)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	if len(chunks[0].Files) != 1 || chunks[0].Files[0] != "main.go" {
		t.Errorf("Files = %v, want [main.go]", chunks[0].Files)
	}
}

func TestSplitIntoChunks_MultipleFiles(t *testing.T) {
	// Create a diff with 3 files, each ~50 bytes
	var sections []string
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("file%d.go", i)
		sections = append(sections, fmt.Sprintf(
			"diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,3 +1,4 @@\n+line\n",
			name, name, name, name,
		))
	}
	diff := strings.Join(sections, "")

	// With a small maxBytes, should split into multiple chunks
	chunks := SplitIntoChunks(diff, 80)
	if len(chunks) < 2 {
		t.Errorf("Expected multiple chunks with small maxBytes, got %d", len(chunks))
	}

	// All files should be present across chunks
	var allFiles []string
	for _, c := range chunks {
		allFiles = append(allFiles, c.Files...)
	}
	if len(allFiles) != 3 {
		t.Errorf("Total files across chunks = %d, want 3", len(allFiles))
	}
}

func TestSplitIntoChunks_LargeMaxBytes(t *testing.T) {
	// With large maxBytes, everything fits in one chunk
	diff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+line1\ndiff --git a/b.go b/b.go\n+++ b/b.go\n+line2\n"
	chunks := SplitIntoChunks(diff, 1000000)
	if len(chunks) != 1 {
		t.Errorf("got %d chunks, want 1 with large maxBytes", len(chunks))
	}
}

func TestSplitIntoChunks_EmptyDiff(t *testing.T) {
	chunks := SplitIntoChunks("", 1000)
	if len(chunks) != 0 {
		t.Errorf("got %d chunks for empty diff, want 0", len(chunks))
	}
}

func TestNeedsChunking(t *testing.T) {
	small := strings.Repeat("x", ChunkThreshold-1)
	if NeedsChunking(small) {
		t.Error("Should not need chunking for small diff")
	}

	large := strings.Repeat("x", ChunkThreshold+1)
	if !NeedsChunking(large) {
		t.Error("Should need chunking for large diff")
	}
}

func TestSplitIntoChunks_ChunkIndex(t *testing.T) {
	var sections []string
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("f%d.go", i)
		sections = append(sections, fmt.Sprintf(
			"diff --git a/%s b/%s\n+++ b/%s\n+data\n",
			name, name, name,
		))
	}
	diff := strings.Join(sections, "")
	chunks := SplitIntoChunks(diff, 50)

	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("Chunk %d has Index=%d", i, c.Index)
		}
	}
}

// mockReviewer implements providers.Reviewer for testing.
type mockReviewer struct {
	responses []string
	callCount int
}

func (m *mockReviewer) Review(_ context.Context, _ providers.ReviewRequest) (providers.ReviewResponse, error) {
	idx := m.callCount
	m.callCount++
	if idx < len(m.responses) {
		return providers.ReviewResponse{Content: m.responses[idx]}, nil
	}
	return providers.ReviewResponse{Content: "[]"}, nil
}

func (m *mockReviewer) Name() string { return "mock" }

func TestRunChunked(t *testing.T) {
	chunks := []Chunk{
		{Index: 0, Diff: "diff a", Files: []string{"a.go"}},
		{Index: 1, Diff: "diff b", Files: []string{"b.go"}},
	}

	mock := &mockReviewer{
		responses: []string{
			`[{"severity":"high","category":"bug","title":"Bug in A","message":"msg","suggestion":"fix","confidence":0.9,"path":"a.go","startLine":1,"endLine":2,"tags":[]}]`,
			`[{"severity":"low","category":"style","title":"Style in B","message":"msg","suggestion":"fix","confidence":0.5,"path":"b.go","startLine":5,"endLine":5,"tags":[]}]`,
		},
	}

	cfg := config.Default()
	findings, llmMs, err := RunChunked(context.Background(), chunks, mock, cfg)
	if err != nil {
		t.Fatalf("RunChunked error: %v", err)
	}

	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(findings))
	}

	// Should be sorted: high first, then low
	if findings[0].Severity != SeverityHigh {
		t.Errorf("findings[0].Severity = %q, want high", findings[0].Severity)
	}
	if findings[1].Severity != SeverityLow {
		t.Errorf("findings[1].Severity = %q, want low", findings[1].Severity)
	}

	if mock.callCount != 2 {
		t.Errorf("Provider called %d times, want 2", mock.callCount)
	}

	_ = llmMs // timing is non-deterministic in tests
}

// errorReviewer returns an error on every call.
type errorReviewer struct{}

func (e *errorReviewer) Review(_ context.Context, _ providers.ReviewRequest) (providers.ReviewResponse, error) {
	return providers.ReviewResponse{}, fmt.Errorf("provider error")
}
func (e *errorReviewer) Name() string { return "error-mock" }

// invalidJSONReviewer returns invalid JSON first, then valid JSON on repair.
type invalidJSONReviewer struct {
	callCount int
}

func (m *invalidJSONReviewer) Review(_ context.Context, _ providers.ReviewRequest) (providers.ReviewResponse, error) {
	m.callCount++
	if m.callCount == 1 {
		return providers.ReviewResponse{Content: "not valid json {{{"}, nil
	}
	return providers.ReviewResponse{Content: "[]"}, nil
}
func (m *invalidJSONReviewer) Name() string { return "invalid-json-mock" }

func TestRunChunked_ProviderError(t *testing.T) {
	chunks := []Chunk{
		{Index: 0, Diff: "diff a", Files: []string{"a.go"}},
	}
	cfg := config.Default()
	_, _, err := RunChunked(context.Background(), chunks, &errorReviewer{}, cfg)
	if err == nil {
		t.Error("Expected error from provider")
	}
	if !strings.Contains(err.Error(), "chunk 0") {
		t.Errorf("Error should reference chunk index, got: %v", err)
	}
}

func TestRunChunked_InvalidJSONWithRepair(t *testing.T) {
	chunks := []Chunk{
		{Index: 0, Diff: "diff a", Files: []string{"a.go"}},
	}
	mock := &invalidJSONReviewer{}
	cfg := config.Default()
	findings, _, err := RunChunked(context.Background(), chunks, mock, cfg)
	if err != nil {
		t.Fatalf("RunChunked error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("got %d findings, want 0", len(findings))
	}
	if mock.callCount != 2 {
		t.Errorf("Expected 2 calls (initial + repair), got %d", mock.callCount)
	}
}

func TestSplitIntoChunks_DefaultMaxBytes(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+line\n"
	chunks := SplitIntoChunks(diff, 0) // 0 means default
	if len(chunks) != 1 {
		t.Errorf("got %d chunks, want 1", len(chunks))
	}
}

func TestDeduplicateFindings(t *testing.T) {
	findings := []Finding{
		{ID: "a", Title: "Finding A"},
		{ID: "b", Title: "Finding B"},
		{ID: "a", Title: "Finding A duplicate"},
	}
	result := DeduplicateFindings(findings)
	if len(result) != 2 {
		t.Errorf("got %d findings, want 2", len(result))
	}
}

func TestFindingPath_NoLocations(t *testing.T) {
	f := Finding{Title: "No locations"}
	if findingPath(f) != "" {
		t.Errorf("findingPath with no locations should be empty")
	}
}

func TestFindingStartLine_NoLocations(t *testing.T) {
	f := Finding{Title: "No locations"}
	if findingStartLine(f) != 0 {
		t.Errorf("findingStartLine with no locations should be 0")
	}
}

func TestRunChunked_Deduplication(t *testing.T) {
	// Both chunks return the same finding
	same := `[{"severity":"high","category":"bug","title":"Same Bug","message":"msg","suggestion":"fix","confidence":0.9,"path":"shared.go","startLine":10,"endLine":12,"tags":[]}]`

	chunks := []Chunk{
		{Index: 0, Diff: "diff a", Files: []string{"shared.go"}},
		{Index: 1, Diff: "diff b", Files: []string{"shared.go"}},
	}

	mock := &mockReviewer{responses: []string{same, same}}

	cfg := config.Default()
	findings, _, err := RunChunked(context.Background(), chunks, mock, cfg)
	if err != nil {
		t.Fatalf("RunChunked error: %v", err)
	}

	if len(findings) != 1 {
		t.Errorf("got %d findings, want 1 (should deduplicate)", len(findings))
	}
}

func TestRunChunkedWithOptions_CustomBuilder(t *testing.T) {
	chunks := []Chunk{
		{Index: 0, Diff: "diff a", Files: []string{"a.go"}},
	}

	mock := &mockReviewer{
		responses: []string{
			`[{"severity":"medium","category":"correctness","title":"Issue","message":"msg","suggestion":"fix","confidence":0.8,"path":"a.go","startLine":1,"endLine":1,"tags":[]}]`,
		},
	}

	var calledWith []string
	customBuilder := func(chunkDiff string, files []string, cfg config.Config, rules *Rules) (string, string) {
		calledWith = append(calledWith, chunkDiff)
		return "custom system prompt", "custom user prompt for: " + chunkDiff
	}

	cfg := config.Default()
	findings, _, err := RunChunkedWithOptions(context.Background(), chunks, mock, cfg, nil, ChunkOptions{
		Builder: customBuilder,
	})
	if err != nil {
		t.Fatalf("RunChunkedWithOptions error: %v", err)
	}
	if len(findings) != 1 {
		t.Errorf("got %d findings, want 1", len(findings))
	}
	if len(calledWith) != 1 || calledWith[0] != "diff a" {
		t.Errorf("Custom builder called with %v, want [\"diff a\"]", calledWith)
	}
}

func TestRunChunkedWithOptions_NilBuilder(t *testing.T) {
	chunks := []Chunk{
		{Index: 0, Diff: "diff a", Files: []string{"a.go"}},
	}
	mock := &mockReviewer{responses: []string{`[]`}}
	cfg := config.Default()

	// nil builder should use default
	findings, _, err := RunChunkedWithOptions(context.Background(), chunks, mock, cfg, nil, ChunkOptions{})
	if err != nil {
		t.Fatalf("RunChunkedWithOptions error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("got %d findings, want 0", len(findings))
	}
}

func TestSortFindings_MixedSeverity(t *testing.T) {
	findings := []Finding{
		{ID: "1", Severity: SeverityLow, Title: "Low", Locations: []Location{{Path: "a.go", Lines: LineRange{Start: 1}}}},
		{ID: "2", Severity: SeverityHigh, Title: "High", Locations: []Location{{Path: "a.go", Lines: LineRange{Start: 2}}}},
		{ID: "3", Severity: SeverityMedium, Title: "Medium", Locations: []Location{{Path: "a.go", Lines: LineRange{Start: 3}}}},
	}
	SortFindings(findings)

	if findings[0].Severity != SeverityHigh {
		t.Errorf("findings[0].Severity = %q, want high", findings[0].Severity)
	}
	if findings[1].Severity != SeverityMedium {
		t.Errorf("findings[1].Severity = %q, want medium", findings[1].Severity)
	}
	if findings[2].Severity != SeverityLow {
		t.Errorf("findings[2].Severity = %q, want low", findings[2].Severity)
	}
}

func TestSortFindings_SameSeverityDifferentPaths(t *testing.T) {
	findings := []Finding{
		{ID: "1", Severity: SeverityMedium, Title: "Z file", Locations: []Location{{Path: "z.go", Lines: LineRange{Start: 1}}}},
		{ID: "2", Severity: SeverityMedium, Title: "A file", Locations: []Location{{Path: "a.go", Lines: LineRange{Start: 1}}}},
		{ID: "3", Severity: SeverityMedium, Title: "M file", Locations: []Location{{Path: "m.go", Lines: LineRange{Start: 1}}}},
	}
	SortFindings(findings)

	if findings[0].Locations[0].Path != "a.go" {
		t.Errorf("findings[0].Path = %q, want a.go", findings[0].Locations[0].Path)
	}
	if findings[1].Locations[0].Path != "m.go" {
		t.Errorf("findings[1].Path = %q, want m.go", findings[1].Locations[0].Path)
	}
	if findings[2].Locations[0].Path != "z.go" {
		t.Errorf("findings[2].Path = %q, want z.go", findings[2].Locations[0].Path)
	}
}

func TestSortFindings_SameSeverityAndPathDifferentLines(t *testing.T) {
	findings := []Finding{
		{ID: "1", Severity: SeverityHigh, Title: "Line 50", Locations: []Location{{Path: "x.go", Lines: LineRange{Start: 50}}}},
		{ID: "2", Severity: SeverityHigh, Title: "Line 10", Locations: []Location{{Path: "x.go", Lines: LineRange{Start: 10}}}},
		{ID: "3", Severity: SeverityHigh, Title: "Line 30", Locations: []Location{{Path: "x.go", Lines: LineRange{Start: 30}}}},
	}
	SortFindings(findings)

	if findings[0].Locations[0].Lines.Start != 10 {
		t.Errorf("findings[0].Lines.Start = %d, want 10", findings[0].Locations[0].Lines.Start)
	}
	if findings[1].Locations[0].Lines.Start != 30 {
		t.Errorf("findings[1].Lines.Start = %d, want 30", findings[1].Locations[0].Lines.Start)
	}
	if findings[2].Locations[0].Lines.Start != 50 {
		t.Errorf("findings[2].Lines.Start = %d, want 50", findings[2].Locations[0].Lines.Start)
	}
}

func TestSortFindings_SingleFinding(t *testing.T) {
	findings := []Finding{
		{ID: "1", Severity: SeverityHigh, Title: "Only", Locations: []Location{{Path: "a.go", Lines: LineRange{Start: 1}}}},
	}
	// Must not panic, must leave the single element in place.
	SortFindings(findings)
	if len(findings) != 1 {
		t.Errorf("got %d findings, want 1", len(findings))
	}
	if findings[0].Title != "Only" {
		t.Errorf("findings[0].Title = %q, want Only", findings[0].Title)
	}
}

func TestSortFindings_NilSlice(t *testing.T) {
	// Must not panic.
	SortFindings(nil)
}

func TestSortFindings_EmptySlice(t *testing.T) {
	// Must not panic.
	SortFindings([]Finding{})
}

func TestSortFindings_NoLocations(t *testing.T) {
	// Findings with no Locations use path="" and line=0 as the sort key.
	// "" < any non-empty path alphabetically, so they sort first within the
	// same severity — verify the invariant: no panic, correct order.
	findings := []Finding{
		{ID: "1", Severity: SeverityLow, Title: "Has location", Locations: []Location{{Path: "b.go", Lines: LineRange{Start: 5}}}},
		{ID: "2", Severity: SeverityLow, Title: "No location"},
	}
	SortFindings(findings)

	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(findings))
	}
	// "" < "b.go", so the no-location finding sorts first within the same severity.
	if findings[0].Title != "No location" {
		t.Errorf("findings[0].Title = %q, want No location (empty path sorts first)", findings[0].Title)
	}
	if findings[1].Title != "Has location" {
		t.Errorf("findings[1].Title = %q, want Has location", findings[1].Title)
	}
}
