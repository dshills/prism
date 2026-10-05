package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	for _, tc := range []struct {
		size, chunkBytes int
		want             bool
	}{
		{DefaultChunkBytes, 0, false},    // unset → default; exactly one chunk
		{DefaultChunkBytes + 1, 0, true}, // one byte over the default
		{80000, 0, true},                 // the AHR-414 backend diff: must chunk
		{5000, 4000, true},               // an explicit, smaller chunk size
		{5000, 10000, false},
	} {
		if got := NeedsChunking(strings.Repeat("x", tc.size), tc.chunkBytes); got != tc.want {
			t.Errorf("NeedsChunking(%d bytes, chunkBytes %d) = %v, want %v", tc.size, tc.chunkBytes, got, tc.want)
		}
	}
}

// section builds one file's diff section of roughly size bytes.
func section(path string, size int) string {
	head := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n", path, path, path, path)
	body := "+" + strings.Repeat("x", max(0, size-len(head)-2)) + "\n"
	return head + body
}

func chunkFiles(chunks []Chunk) [][]string {
	out := make([][]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Files
	}
	return out
}

// A directory that fits in one chunk is never cut across two: when it would
// not fit in the space left, it starts a fresh chunk.
func TestSplitIntoChunks_KeepsDirectoriesTogether(t *testing.T) {
	diff := section("pkg/a/one.go", 300) + section("pkg/a/two.go", 300) +
		section("pkg/b/three.go", 300) + section("pkg/b/four.go", 300)
	got := chunkFiles(SplitIntoChunks(diff, 1000))
	want := [][]string{{"pkg/a/one.go", "pkg/a/two.go"}, {"pkg/b/three.go", "pkg/b/four.go"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("chunks = %v, want %v", got, want)
	}
}

// The AHR-414 shape that motivated the rule: a small directory first, then a
// package that fits in a chunk but not in what is left. The package must not
// be spread across the small directory's chunk and the next.
func TestSplitIntoChunks_PackageNotSpreadAfterSmallDir(t *testing.T) {
	diff := section("handler/h.go", 200) +
		section("pedigree/bench_test.go", 300) + section("pedigree/pedigree.go", 500)
	got := chunkFiles(SplitIntoChunks(diff, 900))
	want := [][]string{{"handler/h.go"}, {"pedigree/bench_test.go", "pedigree/pedigree.go"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("chunks = %v, want %v", got, want)
	}
}

// A directory larger than a chunk is split by size alone.
func TestSplitIntoChunks_OversizedDirectorySplitsBySize(t *testing.T) {
	diff := section("big/a.go", 400) + section("big/b.go", 400) + section("big/c.go", 400)
	got := chunkFiles(SplitIntoChunks(diff, 900))
	want := [][]string{{"big/a.go", "big/b.go"}, {"big/c.go"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("chunks = %v, want %v", got, want)
	}
}

// A directory too big for any one chunk does not close the chunk before it
// early: it would be split by size anyway, so it fills the space that is left.
func TestSplitIntoChunks_OversizedDirectoryDoesNotFlushEarly(t *testing.T) {
	diff := section("small/s.go", 200) +
		section("big/a.go", 400) + section("big/b.go", 400) + section("big/c.go", 400)
	got := chunkFiles(SplitIntoChunks(diff, 900))
	want := [][]string{{"small/s.go", "big/a.go"}, {"big/b.go", "big/c.go"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("chunks = %v, want %v", got, want)
	}
}

// Small directories still share a chunk, rather than each becoming a tiny one.
func TestSplitIntoChunks_SmallDirectoriesShare(t *testing.T) {
	diff := section("a/x.go", 100) + section("b/y.go", 100) + section("c/z.go", 100)
	if got := SplitIntoChunks(diff, 1000); len(got) != 1 {
		t.Errorf("got %d chunks %v, want 1", len(got), chunkFiles(got))
	}
}

// A file larger than a chunk is never split; it becomes a chunk of its own.
func TestSplitIntoChunks_OversizedFileAlone(t *testing.T) {
	diff := section("a/small.go", 200) + section("a/huge.go", 5000) + section("a/tail.go", 200)
	got := chunkFiles(SplitIntoChunks(diff, 1000))
	want := [][]string{{"a/small.go"}, {"a/huge.go"}, {"a/tail.go"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("chunks = %v, want %v", got, want)
	}
}

func TestOtherPartsNote(t *testing.T) {
	chunks := []Chunk{
		{Files: []string{"service/report.go"}},
		{Files: []string{"cmd/server/main.go", "handler/inbreeding.go"}},
	}
	if note := otherPartsNote(chunks[:1], 0); note != "" {
		t.Errorf("single chunk: note = %q, want none", note)
	}
	note := otherPartsNote(chunks, 0)
	for _, want := range []string{"split into 2 parts and this is part 1", "- cmd/server/main.go", "- handler/inbreeding.go", "do not report something as missing"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "service/report.go") {
		t.Errorf("note lists the part's own file:\n%s", note)
	}

	// Capped, with a count of the rest.
	many := []Chunk{{Files: []string{"self.go"}}, {}}
	for i := range maxContextFiles + 7 {
		many[1].Files = append(many[1].Files, fmt.Sprintf("f%d.go", i))
	}
	note = otherPartsNote(many, 0)
	if strings.Count(note, "\n- f") != maxContextFiles || !strings.Contains(note, "...and 7 more") {
		t.Errorf("cap not applied: %d listed", strings.Count(note, "\n- f"))
	}
}

// promptRecorder is a concurrency-safe provider that records each user prompt.
type promptRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (p *promptRecorder) Review(_ context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append(p.prompts, req.UserPrompt)
	return providers.ReviewResponse{Content: "[]"}, nil
}

func (p *promptRecorder) Name() string { return "mock" }

// Every chunk's prompt carries the other parts' files, for the default and for
// a custom (codebase) builder alike.
func TestRunChunked_PromptsListOtherParts(t *testing.T) {
	chunks := []Chunk{
		{Index: 0, Diff: "diff a", Files: []string{"a.go"}},
		{Index: 1, Diff: "diff b", Files: []string{"b.go"}},
	}
	rec := &promptRecorder{}
	builder := func(chunkDiff string, _ []string, _ config.Config, _ *Rules) (string, string) {
		return "sys", "review " + chunkDiff
	}
	if _, _, err := RunChunkedWithOptions(context.Background(), chunks, rec, config.Default(), nil, ChunkOptions{Builder: builder}); err != nil {
		t.Fatal(err)
	}
	if len(rec.prompts) != 2 {
		t.Fatalf("got %d prompts, want 2", len(rec.prompts))
	}
	for _, p := range rec.prompts {
		mine, other := "a.go", "b.go"
		if strings.HasPrefix(p, "review diff b") {
			mine, other = other, mine
		}
		if !strings.Contains(p, "- "+other) || strings.Contains(p, "- "+mine) {
			t.Errorf("prompt should list %s and not %s:\n%s", other, mine, p)
		}
	}
}

// A different chunk size is a different review, so it must not share a cache
// entry: results cached by the old single-prompt behaviour are not replayed.
func TestDiffCacheKey_IncludesChunkSize(t *testing.T) {
	a, b := config.Default(), config.Default()
	b.ChunkBytes = a.ChunkBytes * 2
	if diffCacheKey(a, "p", "diff") == diffCacheKey(b, "p", "diff") {
		t.Error("cache key ignores chunk size")
	}
	c := config.Default()
	c.ChunkBytes = 0 // unset means the default, so it keys like the default
	a.ChunkBytes = DefaultChunkBytes
	if diffCacheKey(a, "p", "diff") != diffCacheKey(c, "p", "diff") {
		t.Error("unset chunk size should key the same as the default")
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
// mockReviewer returns responses in call order. Chunks are reviewed
// concurrently, so the counter is guarded.
type mockReviewer struct {
	mu        sync.Mutex
	responses []string
	callCount int
}

func (m *mockReviewer) Review(_ context.Context, _ providers.ReviewRequest) (providers.ReviewResponse, error) {
	m.mu.Lock()
	idx := m.callCount
	m.callCount++
	m.mu.Unlock()
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

// A finding repeated with the same ID and title is dropped. The same ID with
// another title is a second issue on the same code, so it is kept.
func TestDeduplicateFindings(t *testing.T) {
	findings := []Finding{
		{ID: "a", Title: "Finding A"},
		{ID: "b", Title: "Finding B"},
		{ID: "a", Title: "Finding A"},
		{ID: "a", Title: "Another issue on A's code"},
	}
	result := DeduplicateFindings(findings)
	if len(result) != 3 {
		t.Errorf("got %d findings, want 3", len(result))
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
