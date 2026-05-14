package review

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dshills/prism/internal/cache"
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

func TestBuildReport_NilFindings(t *testing.T) {
	diff := gitctx.DiffResult{Mode: "unstaged"}
	r := BuildReport(diff, nil, 0, 0)

	if r.Findings == nil {
		t.Error("Findings must not be nil when input is nil — want empty slice")
	}
	if len(r.Findings) != 0 {
		t.Errorf("Findings len = %d, want 0", len(r.Findings))
	}
}

func TestBuildReport_NonNilFindings(t *testing.T) {
	diff := gitctx.DiffResult{Mode: "staged"}
	input := []Finding{
		{ID: "x1", Severity: SeverityHigh, Title: "T1"},
		{ID: "x2", Severity: SeverityLow, Title: "T2"},
	}
	r := BuildReport(diff, input, 10, 20)

	if len(r.Findings) != 2 {
		t.Fatalf("Findings len = %d, want 2", len(r.Findings))
	}
	if r.Findings[0].ID != "x1" || r.Findings[1].ID != "x2" {
		t.Errorf("Findings do not match input slice: got %v", r.Findings)
	}
}

func TestBuildReport_ToolAndVersion(t *testing.T) {
	r := BuildReport(gitctx.DiffResult{}, []Finding{}, 0, 0)

	if r.Tool != "prism" {
		t.Errorf("Tool = %q, want prism", r.Tool)
	}
	if r.Version != "1.0" {
		t.Errorf("Version = %q, want 1.0", r.Version)
	}
}

func TestBuildReport_RunIDNonEmpty(t *testing.T) {
	r := BuildReport(gitctx.DiffResult{}, []Finding{}, 0, 0)
	if r.RunID == "" {
		t.Error("RunID must not be empty")
	}
}

func TestBuildReport_RepoFields(t *testing.T) {
	diff := gitctx.DiffResult{
		Repo: gitctx.RepoMeta{
			Root:   "/workspace/myrepo",
			Head:   "deadbeef",
			Branch: "feature-x",
		},
	}
	r := BuildReport(diff, []Finding{}, 0, 0)

	if r.Repo.Root != "/workspace/myrepo" {
		t.Errorf("Repo.Root = %q, want /workspace/myrepo", r.Repo.Root)
	}
	if r.Repo.Head != "deadbeef" {
		t.Errorf("Repo.Head = %q, want deadbeef", r.Repo.Head)
	}
	if r.Repo.Branch != "feature-x" {
		t.Errorf("Repo.Branch = %q, want feature-x", r.Repo.Branch)
	}
}

func TestBuildReport_InputsModeAndRange(t *testing.T) {
	diff := gitctx.DiffResult{
		Mode:  "commit",
		Range: "HEAD~3..HEAD",
	}
	r := BuildReport(diff, []Finding{}, 0, 0)

	if r.Inputs.Mode != "commit" {
		t.Errorf("Inputs.Mode = %q, want commit", r.Inputs.Mode)
	}
	if r.Inputs.Range != "HEAD~3..HEAD" {
		t.Errorf("Inputs.Range = %q, want HEAD~3..HEAD", r.Inputs.Range)
	}
}

func TestBuildReport_SummaryCounts(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityHigh},
		{Severity: SeverityHigh},
		{Severity: SeverityMedium},
		{Severity: SeverityLow},
	}
	r := BuildReport(gitctx.DiffResult{}, findings, 0, 0)

	if r.Summary.Counts.High != 2 {
		t.Errorf("Summary.Counts.High = %d, want 2", r.Summary.Counts.High)
	}
	if r.Summary.Counts.Medium != 1 {
		t.Errorf("Summary.Counts.Medium = %d, want 1", r.Summary.Counts.Medium)
	}
	if r.Summary.Counts.Low != 1 {
		t.Errorf("Summary.Counts.Low = %d, want 1", r.Summary.Counts.Low)
	}
	if r.Summary.HighestSeverity != SeverityHigh {
		t.Errorf("Summary.HighestSeverity = %q, want high", r.Summary.HighestSeverity)
	}
}

func TestBuildReport_Timing(t *testing.T) {
	r := BuildReport(gitctx.DiffResult{}, []Finding{}, 123, 456)

	if r.Timing.LLMMs != 123 {
		t.Errorf("Timing.LLMMs = %d, want 123", r.Timing.LLMMs)
	}
	if r.Timing.TotalMs != 456 {
		t.Errorf("Timing.TotalMs = %d, want 456", r.Timing.TotalMs)
	}
}

func TestBuildReport_Provenance(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityHigh, Provider: "anthropic", Model: "claude-3"},
		{Severity: SeverityLow, Provider: "openai", Model: "gpt-4"},
		{Severity: SeverityMedium, Provider: "anthropic", Model: "claude-3"}, // duplicate pair
	}
	r := BuildReport(gitctx.DiffResult{}, findings, 0, 0)

	// Expect exactly two distinct (provider, model) entries.
	if len(r.Provenance) != 2 {
		t.Fatalf("Provenance len = %d, want 2", len(r.Provenance))
	}
	// First appearance order: anthropic then openai.
	if r.Provenance[0].Provider != "anthropic" || r.Provenance[0].Model != "claude-3" {
		t.Errorf("Provenance[0] = %+v, want {anthropic claude-3}", r.Provenance[0])
	}
	if r.Provenance[1].Provider != "openai" || r.Provenance[1].Model != "gpt-4" {
		t.Errorf("Provenance[1] = %+v, want {openai gpt-4}", r.Provenance[1])
	}
	for _, p := range r.Provenance {
		if !p.AIGenerated {
			t.Errorf("Provenance entry %+v: AIGenerated must be true", p)
		}
	}
}

func TestBuildReport_ProvenanceEmptyWhenNoProvider(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityHigh, Title: "No provider or model"},
	}
	r := BuildReport(gitctx.DiffResult{}, findings, 0, 0)

	if len(r.Provenance) != 0 {
		t.Errorf("Provenance len = %d, want 0 when findings lack provider/model", len(r.Provenance))
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

// ---------------------------------------------------------------------------
// Helpers shared by per-file cache tests
// ---------------------------------------------------------------------------

// makeSection builds a minimal codebase-style pseudo-diff section for a file.
func makeSection(path, content string) string {
	return fmt.Sprintf("diff --git a/%s b/%s\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+%s\n",
		path, path, path, content)
}

// makeTestCache returns a real, isolated cache for a single test.
func makeTestCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.New(true, t.TempDir(), 86400)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	return c
}

// rawFindingJSON encodes a single finding as the JSON array the cache expects.
func rawFindingJSON(path, title string, line int) string {
	rf := []rawFinding{{
		Severity:  "medium",
		Category:  "correctness",
		Title:     title,
		Message:   "test message",
		Path:      path,
		StartLine: line,
		EndLine:   line,
	}}
	b, _ := json.Marshal(rf)
	return string(b)
}

// ---------------------------------------------------------------------------
// Per-file cache tests
// ---------------------------------------------------------------------------

// TestRunCodebaseFileCache_AllMiss — empty cache: all sections are misses,
// storeFindingsPerFile populates the cache, and a second lookup finds all hits.
func TestRunCodebaseFileCache_AllMiss(t *testing.T) {
	section1 := makeSection("foo.go", "package foo")
	section2 := makeSection("bar.go", "package bar")

	provider := "mock-provider"
	model := "mock-model"

	rc := makeTestCache(t)

	key1 := cache.BuildCacheKey(provider, model, section1)
	key2 := cache.BuildCacheKey(provider, model, section2)

	// Pre-verify: cache is empty, so both sections are misses.
	if _, ok := rc.Get(key1); ok {
		t.Fatal("expected cache miss for section1 before test")
	}
	if _, ok := rc.Get(key2); ok {
		t.Fatal("expected cache miss for section2 before test")
	}

	// Simulate what runCodebaseWithFileCache does after the LLM returns findings.
	freshFindings := []Finding{
		{Severity: SeverityMedium, Title: "Issue in foo", Locations: []Location{{Path: "foo.go", Lines: LineRange{Start: 1}}}},
		{Severity: SeverityMedium, Title: "Issue in bar", Locations: []Location{{Path: "bar.go", Lines: LineRange{Start: 1}}}},
	}
	for i := range freshFindings {
		freshFindings[i].ID = generateFindingID(freshFindings[i])
	}

	storeFindingsPerFile(rc, []string{section1, section2}, freshFindings, provider, model)

	// After storing, both keys must be cache hits.
	if _, ok := rc.Get(key1); !ok {
		t.Error("expected cache hit for section1 after storeFindingsPerFile")
	}
	if _, ok := rc.Get(key2); !ok {
		t.Error("expected cache hit for section2 after storeFindingsPerFile")
	}

	// Second pass: simulate the per-file cache loop — should produce zero uncached sections.
	var cachedFindings []Finding
	var uncachedSections []string
	for _, section := range []string{section1, section2} {
		key := cache.BuildCacheKey(provider, model, section)
		if got, ok := rc.Get(key); ok {
			parsed, err := parseFindings(got)
			if err == nil {
				cachedFindings = append(cachedFindings, parsed...)
				continue
			}
		}
		uncachedSections = append(uncachedSections, section)
	}

	if len(uncachedSections) != 0 {
		t.Errorf("uncachedSections = %d on second pass, want 0 (all cached)", len(uncachedSections))
	}
	if len(cachedFindings) != 2 {
		t.Errorf("cachedFindings = %d, want 2", len(cachedFindings))
	}
}

// TestRunCodebaseFileCache_AllCached — all sections in cache, provider not called.
func TestRunCodebaseFileCache_AllCached(t *testing.T) {
	section1 := makeSection("alpha.go", "package alpha")
	section2 := makeSection("beta.go", "package beta")

	provider := "mock-provider"
	model := "mock-model"

	rc := makeTestCache(t)

	// Pre-populate both sections.
	key1 := cache.BuildCacheKey(provider, model, section1)
	key2 := cache.BuildCacheKey(provider, model, section2)
	if err := rc.Put(key1, rawFindingJSON("alpha.go", "Finding Alpha", 5)); err != nil {
		t.Fatalf("Put key1: %v", err)
	}
	if err := rc.Put(key2, rawFindingJSON("beta.go", "Finding Beta", 10)); err != nil {
		t.Fatalf("Put key2: %v", err)
	}

	// Verify both are present before the test.
	if _, ok := rc.Get(key1); !ok {
		t.Fatal("expected key1 in cache")
	}
	if _, ok := rc.Get(key2); !ok {
		t.Fatal("expected key2 in cache")
	}

	// Simulate the per-file cache loop from runCodebaseWithFileCache.
	var cachedFindings []Finding
	var uncachedSections []string
	for _, section := range []string{section1, section2} {
		key := cache.BuildCacheKey(provider, model, section)
		if cached, ok := rc.Get(key); ok {
			parsed, err := parseFindings(cached)
			if err == nil {
				cachedFindings = append(cachedFindings, parsed...)
				continue
			}
		}
		uncachedSections = append(uncachedSections, section)
	}

	// All cached: provider must NOT be needed.
	if len(uncachedSections) != 0 {
		t.Errorf("uncachedSections = %d, want 0 (all should be cached)", len(uncachedSections))
	}
	if len(cachedFindings) != 2 {
		t.Errorf("cachedFindings = %d, want 2", len(cachedFindings))
	}
}

// TestRunCodebaseFileCache_PartialHit — one section cached, one is a miss.
func TestRunCodebaseFileCache_PartialHit(t *testing.T) {
	section1 := makeSection("cached.go", "package cached")
	section2 := makeSection("uncached.go", "package uncached")

	provider := "mock-provider"
	model := "mock-model"

	rc := makeTestCache(t)

	// Only cache section1.
	key1 := cache.BuildCacheKey(provider, model, section1)
	if err := rc.Put(key1, rawFindingJSON("cached.go", "Cached finding", 3)); err != nil {
		t.Fatalf("Put key1: %v", err)
	}

	var cachedFindings []Finding
	var uncachedSections []string
	for _, section := range []string{section1, section2} {
		key := cache.BuildCacheKey(provider, model, section)
		if cached, ok := rc.Get(key); ok {
			parsed, err := parseFindings(cached)
			if err == nil {
				cachedFindings = append(cachedFindings, parsed...)
				continue
			}
		}
		uncachedSections = append(uncachedSections, section)
	}

	// section1 cached, section2 is a miss.
	if len(cachedFindings) != 1 {
		t.Errorf("cachedFindings = %d, want 1", len(cachedFindings))
	}
	if len(uncachedSections) != 1 {
		t.Errorf("uncachedSections = %d, want 1", len(uncachedSections))
	}
	if uncachedSections[0] != section2 {
		t.Errorf("uncachedSections[0] != section2")
	}

	// Simulate fresh findings from LLM and storing them.
	freshFindings := []Finding{
		{Severity: SeverityLow, Title: "Fresh finding", Locations: []Location{{Path: "uncached.go", Lines: LineRange{Start: 1}}}},
	}
	for i := range freshFindings {
		freshFindings[i].ID = generateFindingID(freshFindings[i])
	}
	storeFindingsPerFile(rc, uncachedSections, freshFindings, provider, model)

	// Both findings in the merged set.
	allFindings := append(cachedFindings, freshFindings...)
	if len(allFindings) != 2 {
		t.Errorf("allFindings = %d, want 2", len(allFindings))
	}

	// The previously-uncached section should now be cached.
	key2 := cache.BuildCacheKey(provider, model, section2)
	if _, ok := rc.Get(key2); !ok {
		t.Error("expected section2 to be cached after storeFindingsPerFile")
	}
}

// TestRunCodebaseFileCache_EmptyFindingsStored — provider returns [], entry stored as [].
func TestRunCodebaseFileCache_EmptyFindingsStored(t *testing.T) {
	section := makeSection("clean.go", "package clean")
	provider := "mock-provider"
	model := "mock-model"

	rc := makeTestCache(t)

	// Simulate: LLM returns no findings for this file.
	freshFindings := []Finding{}
	storeFindingsPerFile(rc, []string{section}, freshFindings, provider, model)

	key := cache.BuildCacheKey(provider, model, section)
	cached, ok := rc.Get(key)
	if !ok {
		t.Fatal("expected cache entry to be written even when findings are empty")
	}
	if cached != "[]" {
		t.Errorf("cached = %q, want %q", cached, "[]")
	}

	// Second pass: section is now a cache hit with an empty array.
	var cachedFindings []Finding
	var uncachedSections []string
	if got, ok2 := rc.Get(key); ok2 {
		parsed, err := parseFindings(got)
		if err == nil {
			cachedFindings = append(cachedFindings, parsed...)
		} else {
			uncachedSections = append(uncachedSections, section)
		}
	} else {
		uncachedSections = append(uncachedSections, section)
	}

	if len(uncachedSections) != 0 {
		t.Error("expected cache hit on second pass (no LLM call needed)")
	}
	if len(cachedFindings) != 0 {
		t.Errorf("cachedFindings = %d, want 0 (clean file)", len(cachedFindings))
	}
}

// TestRunCodebaseFileCache_CorruptCacheEntry — corrupt JSON treated as miss, no error.
func TestRunCodebaseFileCache_CorruptCacheEntry(t *testing.T) {
	section := makeSection("buggy.go", "package buggy")
	provider := "mock-provider"
	model := "mock-model"

	rc := makeTestCache(t)

	// Write corrupt JSON directly to the cache key.
	key := cache.BuildCacheKey(provider, model, section)
	if err := rc.Put(key, "not valid json {{{{"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Run the per-file cache lookup logic.
	var cachedFindings []Finding
	var uncachedSections []string
	cached, ok := rc.Get(key)
	if ok {
		parsed, err := parseFindings(cached)
		if err == nil {
			cachedFindings = append(cachedFindings, parsed...)
		} else {
			// Corrupt entry → treat as miss.
			uncachedSections = append(uncachedSections, section)
		}
	} else {
		uncachedSections = append(uncachedSections, section)
	}

	// Must be treated as a miss so the provider is called.
	if len(uncachedSections) != 1 {
		t.Errorf("uncachedSections = %d, want 1 (corrupt entry = miss)", len(uncachedSections))
	}
	if len(cachedFindings) != 0 {
		t.Errorf("cachedFindings = %d, want 0", len(cachedFindings))
	}
	// No error should propagate — the function continues.
}

// TestRunCodebaseFileCache_MaxFindings — MaxFindings applied after merge.
func TestRunCodebaseFileCache_MaxFindings(t *testing.T) {
	// Build 3 cached + 2 fresh findings, MaxFindings = 4.
	makeFinding := func(path, title string, line int) Finding {
		f := Finding{
			Severity:  SeverityMedium,
			Title:     title,
			Locations: []Location{{Path: path, Lines: LineRange{Start: line, End: line}}},
		}
		f.ID = generateFindingID(f)
		return f
	}

	cachedFindings := []Finding{
		makeFinding("a.go", "C1", 1),
		makeFinding("b.go", "C2", 2),
		makeFinding("c.go", "C3", 3),
	}
	freshFindings := []Finding{
		makeFinding("d.go", "F1", 4),
		makeFinding("e.go", "F2", 5),
	}

	allFindings := append(cachedFindings, freshFindings...)
	allFindings = DeduplicateFindings(allFindings)
	SortFindings(allFindings)

	maxFindings := 4
	if maxFindings > 0 && len(allFindings) > maxFindings {
		allFindings = allFindings[:maxFindings]
	}

	if len(allFindings) != 4 {
		t.Errorf("len(allFindings) = %d, want 4", len(allFindings))
	}
}

// TestStoreFindingsPerFile_SkipsOnUnattributableFinding — finding with no path
// causes the entire batch to be skipped; no cache entries written.
func TestStoreFindingsPerFile_SkipsOnUnattributableFinding(t *testing.T) {
	section := makeSection("real.go", "package real")
	provider := "mock-provider"
	model := "mock-model"

	rc := makeTestCache(t)

	// One finding has no locations (unattributable).
	findings := []Finding{
		{Severity: SeverityHigh, Title: "No path finding"},
	}

	storeFindingsPerFile(rc, []string{section}, findings, provider, model)

	// No cache entry must be written.
	key := cache.BuildCacheKey(provider, model, section)
	if _, ok := rc.Get(key); ok {
		t.Error("expected NO cache entry when a finding has no primary path")
	}
}

// TestStoreFindingsPerFile_SkipsOnEmptyPathLocation — finding with empty path
// in its first location triggers the guard.
func TestStoreFindingsPerFile_SkipsOnEmptyPathLocation(t *testing.T) {
	section := makeSection("file.go", "package file")
	provider := "mock-provider"
	model := "mock-model"

	rc := makeTestCache(t)

	findings := []Finding{
		{
			Severity:  SeverityLow,
			Title:     "Empty path",
			Locations: []Location{{Path: "", Lines: LineRange{Start: 1}}},
		},
	}

	storeFindingsPerFile(rc, []string{section}, findings, provider, model)

	key := cache.BuildCacheKey(provider, model, section)
	if _, ok := rc.Get(key); ok {
		t.Error("expected NO cache entry when finding has empty primary path")
	}
}
