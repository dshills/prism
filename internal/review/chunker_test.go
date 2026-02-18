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
	result := deduplicateFindings(findings)
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

func TestPathFromSection_NoHeader(t *testing.T) {
	section := "diff --git a/main.go b/main.go\nsome content without +++ header\n"
	if pathFromSection(section) != "" {
		t.Error("pathFromSection should return empty for section without +++ b/ header")
	}
}

func TestSplitSections_WhitespaceOnly(t *testing.T) {
	sections := splitSections("   \n\t\n  ")
	if len(sections) != 0 {
		t.Errorf("got %d sections for whitespace-only, want 0", len(sections))
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
