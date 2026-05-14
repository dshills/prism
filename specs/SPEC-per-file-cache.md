# SPEC: Per-File Incremental Cache Invalidation

## Problem

The current cache uses a single key derived from `hash(provider, model, fullDiff)`. In codebase mode, which reviews all tracked source files, changing one file invalidates the entire cache entry — every file is re-reviewed on the next run even if 99% of them are unchanged. For a 500-file codebase this means minutes of LLM calls when only a handful of files changed.

## Goal

In **codebase mode only**, cache findings at per-file granularity. Codebase mode reviews all git-tracked, non-binary source files in the repository as complete files (not diffs). With per-file caching, only files whose content has changed since the last cached review are sent to the LLM; unchanged files return their findings from the cache instantly.

Diff modes (`staged`, `unstaged`, `commit`, `range`, `snippet`) are out of scope and must not change behavior.

---

## Functional Requirements

### FR-1: Per-file cache key

Each file's cache entry must be keyed on `hash(provider, model, redactedSectionContent)` where `redactedSectionContent` is derived as follows:

1. Extract the file's section from the codebase pseudo-diff using `diffutil.SplitSections()`, which splits on `diff --git` boundaries and returns the raw section text as produced by `gitctx.Codebase()`.
2. Apply `redact.Secrets()` to that section text.
3. Pass the result as the third argument to `cache.BuildCacheKey(provider, model, redactedSectionContent)`.

The canonical key formula is:
```
key(file) = SHA256( provider + ":" + model + ":" + redact(sectionText) )
```
where SHA256 denotes the 256-bit Secure Hash Algorithm 2 digest, hex-encoded.

This ensures:
- A file whose content is identical across runs produces the same key and gets a cache hit.
- A file whose content changes produces a different key and gets a cache miss.
- Switching provider or model invalidates all file entries (different key space).
- Redaction is always applied before hashing, consistent with the existing whole-diff approach.

### FR-2: Cache entries store per-file findings

Each cache entry stores a JSON array of finding objects for that one file, using the same serialisation format as the existing whole-diff cache (see **Data Types** below). An empty JSON array `[]` is a valid and required entry, meaning "reviewed, no findings."

### FR-3: Partial cache hits

On any given codebase run the following steps execute in order:

1. All file sections are extracted from the diff using `diffutil.SplitSections()`.
2. Each section is redacted and its cache key computed.
3. Files with a valid (non-expired) cache entry contribute their cached findings directly.
4. Files with a cache miss (expired, corrupt, absent, or I/O error) are collected into a filtered diff.
5. If all files are cached, no LLM call is made and the review proceeds directly to step 8.
6. The filtered diff (uncached files only) is sent to the LLM via the existing chunked review path.
7. After the LLM returns, each file's fresh findings are stored to cache individually before the report is built.
8. Cached and fresh findings are merged, deduplicated (by finding ID), sorted (high → medium → low, then by path, then by line), and limited to `cfg.MaxFindings`, in that exact order.

### FR-4: Cache miss storage

After LLM review of uncached files, findings must be grouped by primary file path (the `path` field of a finding's first location entry; findings with no locations or an empty path are stored under a special `"_unknown"` key and are not associated with any file's per-file cache entry) and each file's findings stored individually under that file's cache key. Files for which the LLM returned no findings must be stored with an empty JSON array `[]` so they produce cache hits on subsequent runs.

### FR-5: Redaction before cache key

Secret redaction (`redact.Secrets()`) must be applied to each section's text before computing its cache key. The same redacted text is also what gets sent to the LLM. Redaction is performed once per section and the result reused for both key computation and LLM input.

### FR-6: Backward compatibility

Existing whole-diff cache entries are not used for codebase runs. They will not match per-file keys and will be treated as misses. No migration is required. Stale whole-diff entries expire naturally via TTL.

### FR-7: Cache failure handling

Any I/O or parse failure during cache lookup, TTL evaluation, or cache write for an individual file is non-fatal:
- A failed read or corrupt entry is treated as a cache miss; the file is re-reviewed.
- A failed write is silently ignored; the review result is still used for the current run.

This is consistent with the existing whole-diff cache behavior in `reviewPipeline`.

### FR-8: Timing

`Report.Timing.LLMMs` reflects only the actual LLM time for uncached files in the current run. Cached files contribute 0 ms. `Report.Timing.TotalMs` reflects wall-clock time for the full run including cache lookups. Both fields use the same int64 millisecond unit as the existing `Timing` struct.

### FR-9: MaxFindings

`cfg.MaxFindings` is applied after cached and fresh findings are merged, deduplicated, and sorted — consistent with current behavior.

### FR-10: No behavior change for diff modes

`reviewPipeline` (used by all diff modes) is unchanged. Per-file caching is implemented exclusively within `RunCodebase`, not in the shared pipeline.

---

## Data Types

**Finding** — a structured issue returned by the LLM. Relevant fields:
- `id` (string): stable SHA-256 hash of path + title + start line, used for deduplication.
- `severity` (string): `"high"`, `"medium"`, or `"low"`.
- `locations` (array): one or more location objects, each with `path` (string) and `lines.start`/`lines.end` (int). The primary location is `locations[0]`.

**Cache entry** — a JSON object with fields `key`, `response` (a JSON-serialised array of finding objects), `createdAt`, and `ttl`. Identical format to existing whole-diff entries; only the granularity and key input change.

**Section** — the contiguous block of pseudo-diff text for a single file as returned by `diffutil.SplitSections()`. Sections are delimited by `diff --git` lines. The section content is stable and deterministic for a given file state.

---

## Non-Goals

- Migrating or invalidating existing whole-diff cache entries.
- Per-file caching for diff modes (`staged`, `unstaged`, `commit`, `range`, `snippet`).
- New CLI flags or config options — existing cache settings apply unchanged.
- UI changes — cache behavior is transparent; `prism cache show` reports aggregate stats as before.
- Cross-file deduplication — finding deduplication by ID is unchanged and runs after merge.

---

## Acceptance Criteria

| ID | Criterion | Verifiable by |
|----|-----------|---------------|
| AC-1 | A codebase run where all files are in cache makes zero LLM calls. | Unit test: mock provider assert-not-called when all sections hit cache. |
| AC-2 | A codebase run where no files are in cache reviews all files via LLM. | Unit test: mock provider called once per chunk covering all files. |
| AC-3 | After a run, each reviewed file has an individual cache entry; a second run on the same files makes zero LLM calls. | Integration test: two sequential runs, assert provider not called on second. |
| AC-4 | Changing one file invalidates only that file's cache entry; the second run calls the LLM only for the changed file. | Unit test: pre-populate cache for N files, mutate one section, assert provider called once for that file only. |
| AC-5 | A file with no findings is cached with `[]`; subsequent run does not call LLM for that file. | Unit test: provider returns `[]`, assert cache entry is `[]`, assert no second call. |
| AC-6 | A corrupt cache entry (invalid JSON) causes that file to be re-reviewed; the run does not error. | Unit test: inject corrupt entry, assert provider called for that file, no error returned. |
| AC-7 | A cache write failure for one file is silently ignored; other files' results and the final report are unaffected. | Unit test: make cache dir read-only for one key, assert report contains correct findings. |
| AC-8 | `Report.Timing.LLMMs` equals the sum of LLM call durations for uncached files only; cached files contribute 0. | Unit test: mix of hits and misses, assert LLMMs within expected range. |
| AC-9 | `cfg.MaxFindings` is enforced on the merged cached+fresh finding set. | Unit test: N cached findings + M fresh findings with MaxFindings < N+M, assert len(report.Findings) == MaxFindings. |
| AC-10 | Diff modes (`staged`, `unstaged`, `commit`, `range`) are unaffected by this change. | Existing test suite passes unchanged. |
