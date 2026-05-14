# PLAN: Per-File Incremental Cache Invalidation

Spec: `specs/SPEC-per-file-cache.md`

---

## Overview

Replace the single whole-diff cache entry in codebase mode with per-file cache entries keyed on `hash(provider, model, redactedSectionContent)`. Only files that are cache misses are sent to the LLM. The shared `reviewPipeline` used by diff modes is untouched.

### Affected packages

| Package | Change |
|---|---|
| `internal/review/engine.go` | New `runCodebaseWithFileCache` function; `RunCodebase` delegates to it |
| `internal/review/engine_test.go` | Tests for new per-file cache logic |
| No other packages change | `cache`, `diffutil`, `gitctx`, `output`, `cli` are all unchanged |

---

## Phase 1 — `runCodebaseWithFileCache` skeleton

**Goal:** Extract a new `RunCodebase`-specific function that owns the per-file cache loop, without breaking any existing behaviour.

### 1.1 Add `runCodebaseWithFileCache` to `internal/review/engine.go`

```go
func runCodebaseWithFileCache(
    ctx context.Context,
    diff gitctx.DiffResult,
    cfg CodebaseConfig,
    reviewCache *cache.Cache,
    rules *Rules,
    startTime time.Time,
) (*Report, error)
```

The function signature accepts a pre-constructed `reviewCache` (already created in `RunCodebase`) and pre-loaded `rules` so both are only initialised once.

**Step-by-step logic:**

```go
// Declare accumulators up-front so all exit paths are valid.
var (
    cachedFindings  []Finding
    uncachedSections []string
    freshFindings   []Finding
    llmMs           int64
)
```

1. Apply redaction: `redactedDiff := diff.Diff` → `if cfg.Privacy.RedactSecrets { redactedDiff = redact.Secrets(diff.Diff) }`
2. If `strings.TrimSpace(redactedDiff) == ""`: return `emptyReport(diff, startTime), nil`
3. Split into sections: `sections := diffutil.SplitSections(redactedDiff)` — in codebase mode this produces exactly one section per file.
4. For each section:
   - Compute `key := cache.BuildCacheKey(cfg.Provider, cfg.Model, section)` — `parseFindings` is an existing unexported helper in `engine.go`
   - Check `reviewCache.Get(key)` → on hit, call `parseFindings(cached)` → on success append to `cachedFindings`; on parse error treat as miss
   - On miss (or any error): append section to `uncachedSections`
5. If `len(uncachedSections) == 0`: skip to step 9 (`freshFindings` and `llmMs` remain zero-valued, which is correct)
6. Build `filteredDiff := strings.Join(uncachedSections, "")` — `SplitIntoChunks` will re-derive per-chunk file lists from section headers internally, so no separate file list is needed here
7. Construct the LLM provider and run chunked review on uncached sections only:
   ```go
   provider, err := providers.New(cfg.Provider, cfg.Model)
   if err != nil {
       return nil, fmt.Errorf("creating provider: %w", err)
   }
   maxPerFile := cfg.MaxFindingsPerFile
   codebaseBuilder := func(chunkDiff string, files []string, c config.Config, r *Rules) (string, string) {
       return CodebaseSystemPrompt(), BuildCodebaseUserPrompt(chunkDiff, files, c.MaxFindings, maxPerFile, c.FailOn, r)
   }
   chunks := SplitIntoChunks(filteredDiff, cfg.MaxDiffBytes)
   freshFindings, llmMs, err = RunChunkedWithOptions(ctx, chunks, provider, cfg.Config, rules, ChunkOptions{Builder: codebaseBuilder})
   if err != nil {
       return nil, fmt.Errorf("chunked review: %w", err)
   }
   ```
   `codebaseBuilder`, `RunChunkedWithOptions`, `SplitIntoChunks`, `findingsToRaw` are all existing functions in `engine.go` / `chunker.go`.
8. Store fresh findings per-file (see Phase 2); if any finding lacks a path the entire batch stays uncached (see Design Decisions)
9. Merge: `allFindings := append(cachedFindings, freshFindings...)`
10. Apply `ApplySeverityOverrides(allFindings, rules)`
11. Deduplicate: `allFindings = DeduplicateFindings(allFindings)`
12. Sort: `SortFindings(allFindings)`
13. Limit: `if cfg.MaxFindings > 0 && len(allFindings) > cfg.MaxFindings { allFindings = allFindings[:cfg.MaxFindings] }`
14. Return `BuildReport(diff, allFindings, llmMs, time.Since(startTime).Milliseconds()), nil`

### 1.2 Update `RunCodebase` to call the new function

```go
func RunCodebase(ctx context.Context, diff gitctx.DiffResult, cfg CodebaseConfig) (*Report, error) {
    startTime := time.Now()

    reviewCache, err := cache.New(cfg.Cache.Enabled, cfg.Cache.Dir, cfg.Cache.TTLSeconds)
    if err != nil {
        reviewCache, _ = cache.New(false, "", 0)
    }

    rules, err := LoadRules(cfg.RulesFile)
    if err != nil {
        return nil, fmt.Errorf("loading rules: %w", err)
    }

    return runCodebaseWithFileCache(ctx, diff, cfg, reviewCache, rules, startTime)
}
```

**Deliverable:** `go build ./...` passes; existing tests pass; `RunCodebase` behaviour is unchanged (no cache is populated yet in this phase — the per-file store is added in Phase 2).

---

## Phase 2 — Per-file cache store after LLM review

**Goal:** After LLM review of uncached files, store each file's findings individually under its per-file cache key.

### 2.1 Add `storeFindingsPerFile` helper

```go
// storeFindingsPerFile stores findings grouped by primary file path.
// Files reviewed but having no findings are stored as "[]" so subsequent
// runs treat them as cache hits.
//
// If any finding in the batch has no primary path it cannot be attributed
// to a specific file. In that case NO cache entries are written for this
// batch — all sections remain cache misses on the next run and are
// re-reviewed, ensuring unattributable findings are not silently lost.
//
// All write errors are silently ignored (non-fatal per FR-7).
func storeFindingsPerFile(
    reviewCache *cache.Cache,
    sections []string,   // only the uncached sections that were reviewed
    findings []Finding,
    provider, model string,
) {
    // Guard: if any finding lacks a primary path, bail out entirely.
    // The whole batch stays uncached so these findings reappear next run.
    for _, f := range findings {
        if len(f.Locations) == 0 || f.Locations[0].Path == "" {
            return
        }
    }

    byPath := make(map[string][]Finding)
    for _, f := range findings {
        path := f.Locations[0].Path
        byPath[path] = append(byPath[path], f)
    }

    for _, section := range sections {
        path := diffutil.PathFromSection(section)
        if path == "" {
            continue
        }
        key := cache.BuildCacheKey(provider, model, section)
        raw := findingsToRaw(byPath[path]) // nil slice → marshals as []
        data, err := json.Marshal(raw)
        if err != nil {
            continue
        }
        _ = reviewCache.Put(key, string(data))
    }
}
```

### 2.2 Wire into `runCodebaseWithFileCache`

After the `RunChunkedWithOptions` call (step 7 in Phase 1), call:
```go
storeFindingsPerFile(reviewCache, uncachedSections, freshFindings, cfg.Provider, cfg.Model)
```

**Deliverable:** After two sequential `prism review codebase` runs on unchanged files, the second run makes zero LLM calls and returns the same findings.

---

## Phase 3 — Tests

**Goal:** Cover all acceptance criteria from the spec with unit tests in `internal/review/engine_test.go`. All tests use the `mockReviewer` already defined in `chunker_test.go` (same package).

### Test cases

| Test | AC | What it does |
|---|---|---|
| `TestCodebaseFileCache_AllCached` | AC-1 | Pre-populate cache for all sections; assert `mockReviewer.callCount == 0` |
| `TestCodebaseFileCache_AllMiss` | AC-2 | Empty cache; assert provider called, findings returned |
| `TestCodebaseFileCache_PartialHit` | AC-4 | Cache N files, leave 1 uncached; assert provider called once for uncached file only |
| `TestCodebaseFileCache_EmptyFindingsStored` | AC-5 | Provider returns `[]`; assert cache entry written; second call → zero LLM calls |
| `TestCodebaseFileCache_CorruptEntry` | AC-6 | Write corrupt JSON to cache key; assert provider called, no error returned |
| `TestCodebaseFileCache_WriteFailure` | AC-7 | Use a read-only or disabled cache; assert report still correct, no error |
| `TestCodebaseFileCache_MaxFindings` | AC-9 | N cached + M fresh findings with MaxFindings < N+M; assert `len(report.Findings) == MaxFindings` |
| `TestCodebaseFileCache_DiffModesUnchanged` | AC-10 | Call `Run()` (diff mode); assert whole-diff cache path used, not per-file |

### Helper: build a test cache

Tests use `cache.New(true, t.TempDir(), 86400)` to get a real, isolated cache per test. No mocking of the cache layer.

---

## Phase 4 — Prism review & commit

1. Run `go test ./internal/review/... -v` — all tests must pass.
2. Run `golangci-lint run ./...` — zero new lint issues.
3. Stage changes, run `prism review staged --fail-on medium`.
4. Fix any medium/high findings; re-review until exit 0.
5. Commit:
   ```
   perf(cache): per-file cache invalidation for codebase mode
   ```

---

## Design Decisions

### Why not change `reviewPipeline`?

`reviewPipeline` is shared by all six review modes. Injecting per-file logic there would require mode-specific branching throughout, making the shared path harder to reason about. `RunCodebase` already has a dedicated entry point; a sibling function is cleaner.

### One section per file in codebase mode

`gitctx.Codebase()` reads each git-tracked file in full and emits exactly one pseudo-diff section per file (the section begins with `diff --git a/<path> b/<path>` and contains the entire file content). There is a strict 1:1 mapping between files and sections. Therefore `storeFindingsPerFile` will never write the same file's findings under multiple section keys, and loading cached findings on subsequent runs will not produce duplicates. The `DeduplicateFindings` call in the merge step (Phase 1, step 9 after merge) provides a final safety net regardless.

### Why reuse `cache.BuildCacheKey`?

The key builder is `hash(provider + ":" + model + ":" + content)`. Passing per-section content instead of the full diff requires zero API changes to the `cache` package — it's purely a call-site change. This minimises blast radius.

### Why store `[]` for files with no findings?

Without an explicit `[]` entry, a file that was reviewed and found clean would appear as a cache miss on the next run and be re-reviewed unnecessarily. Storing `[]` means "reviewed, clean" while a missing key means "not yet reviewed."

### Why are unattributable findings (no path) skipped in `storeFindingsPerFile`?

There is no file to associate them with. They are still included in the merged findings for the current report (via `freshFindings`); they simply aren't stored in any per-file cache slot. On the next run they will be re-generated if their file is re-reviewed.

### Rollback / migration

No schema change. Existing whole-diff cache entries for codebase runs will not match per-file keys and will be ignored as misses, expiring naturally. No migration tooling is needed.
