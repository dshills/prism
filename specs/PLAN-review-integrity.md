# PLAN: Review integrity

Spec: `SPEC-review-integrity.md`. Two phases, each passing tests on its own.

## Phase 1 — Coverage and the incomplete exit code (FR-1, FR-2, FR-3)

**Types (`internal/review/types.go`)**

- `Coverage{Reviewer []Reviewer; Files, Bytes, Chunks, LLMCalls, TruncatedBytes int; CacheHit, Complete bool; Skipped []Skip}`, where `Reviewer{Provider, Model}` and `Skip{Target, Reason}`.
- `Report.Coverage Coverage` with the tag `json:"coverage"`, so it is always
  present.
- A helper, `(*Coverage).Finalize()`, sets
  `Complete = TruncatedBytes == 0 && len(Skipped) == 0`.

**Truncation source (`internal/gitctx/gitctx.go`)**

- `DiffResult` gains `TruncatedBytes int`, which `buildResult` sets to
  `len(diff) - MaxDiffBytes` at the point where it truncates.
- The marker line it appends stays as it is.

**Filling coverage**

- **Single and chunked review (`internal/review/engine.go`, `reviewPipeline`).**
  - `Files = len(diff.Files)`.
  - `Bytes = len(redactedDiff)`, excluding the truncation marker.
  - `Chunks` is 1, or `len(chunks)` when chunked.
  - `LLMCalls` counts provider calls, including repair passes. For chunked
    review it is returned by `RunChunkedWithOptions`: its signature gains a
    calls count, and every in-repo caller is updated.
  - `CacheHit` is set when the result came from cache.
  - `Reviewer` comes from `cfg.Provider` and `cfg.Model`.
  - `TruncatedBytes` comes from `diff.TruncatedBytes`, and a positive value
    appends `Skip{"diff", "truncated at maxDiffBytes"}`.
  - The empty-diff early return (`emptyReport`) sets `Chunks: 0` and
    `Complete: true`.
- **Codebase review (`RunCodebase`).** The same fields.
  - `CacheHit` is true only when every file came from the per-file cache.
  - `Chunks` counts the chunks of cache misses only.
- **Compare mode (`internal/review/compare.go`, `internal/cli/review.go`
  `runCompareMode`).**
  - `Reviewer` lists every model.
  - `LLMCalls` and `Chunks` are summed across models.
- **Per-commit range (`internal/cli/review.go`, `runPerCommitReview`).**
  - Coverage is summed over reviewed commits.
  - A commit skipped for a diff error, or a commit whose review failed, adds a
    `Skip` (target: short SHA; reason: the error).
  - A commit skipped because its diff is empty adds nothing.
  - `Finalize()` runs at the end.
- **Library (`pkg/prism/prism.go`).** The per-commit loop there changes the
  same way. Its `continue` on a diff error becomes a `Skip`.

**Output**

- **Text (`internal/output/text.go`).**
  - Remove the early return for zero findings, so the coverage line and the
    timing footer always print.
  - Add `coverageLine(report)`, which produces the three FR-2 forms.
  - Add `incompleteLine(report)`.
  - Byte sizes are shown as `%.1f KB`.
- **Markdown (`markdown.go`).** The same coverage sentence goes under the
  summary.
- **JSON.** Gets `coverage` automatically through the struct.
- **SARIF.** Coverage goes into `runs[0].properties.coverage`.

**Exit code (`internal/cli/root.go`, `review.go`)**

- Add `ExitIncomplete = 5`.
- A shared `finishExit(report, cfg)` sets `ExitFindings` when `failOn` is met,
  otherwise `ExitIncomplete` when `!Coverage.Complete && !flagAllowIncomplete`.
  It is used by `runReview`, per-commit review and codebase review.
- Add the `--allow-incomplete` flag to the review commands' flag set, and reset
  it in `cli_test.go`.

**Tests**

- **Engine** (with a mock provider):
  - an unchunked review: `Chunks 1`, `LLMCalls 1`;
  - a chunked review: `Chunks N`, `LLMCalls N`;
  - a repair pass counts 2 calls;
  - a second run hits the cache: `CacheHit`, `LLMCalls 0`;
  - an empty diff: complete, `Chunks 0`.
- **gitctx:** `TruncatedBytes` is set by the existing truncation test fixture.
- **Text output:** a zero-finding report prints the coverage and timing lines;
  the three coverage forms; the `INCOMPLETE` line.
- **CLI:** `finishExit` precedence, as a table: findings plus incomplete gives
  1; incomplete alone gives 5; incomplete with `--allow-incomplete` gives 0;
  auth errors are unchanged.
- **Per-commit:** a stub makes one commit's review fail. The report is
  incomplete, lists that SHA and exits 5. An empty commit does not make it
  incomplete.

## Phase 2 — Verified findings (FR-4 … FR-8)

**Prompt (`internal/review/prompt.go`)**

- Both system prompts add `"evidence"` to the finding structure, with the
  instruction to copy the exact line or lines verbatim from the diff.
- Rule 4 becomes: reference line numbers from the diff hunks, and quote them in
  `evidence`.

**Schema**

- `rawFinding` and `Finding` gain `Evidence string` (`json:"evidence,omitempty"`).
- `parseFindings` and `findingsToRaw` carry it through.

**Verification code: `internal/review/verify.go`, in package `review`**

A separate package would need `review.Finding`, and `review` would need to call
it, which is an import cycle. So both passes live in `review`. `diffutil`
gains only a line parser.

- `verifyEvidence(findings []Finding, diff string) (kept []Finding, discarded
  []Discard)` implements FR-5:
  - per-file section index via `diffutil.SplitSections`;
  - a new `diffutil.PostImageLines(section) []Line{Text string; NewLine int;
    Removed bool}`, which parses `@@ -a,b +c,d @@` headers and skips the file
    header lines;
  - normalisation, contiguous match, nearest-match selection, and location
    correction.
- `verifyCompileClaims(ctx, findings, env TreeEnv) (kept, discarded)` implements
  FR-6.
  - `TreeEnv{RepoRoot, Mode, TipRev string; Git GitRunner; Go GoRunner}`.
  - The `GitRunner` and `GoRunner` interfaces wrap `exec.CommandContext`, and
    are faked in tests.
  - The tree-match check follows the FR-6 table.
  - Memoisation is per directory, with the 60-second per-package and
    120-second total budgets.
- `Discard{Finding Finding; Reason string}` goes in `types.go`, and
  `Report.Discarded []Discard` has the tag `json:"discarded"`, always present.

**Integration (in this order, per FR-5's ordering rule)**

- **`reviewPipeline`:** after the cache load or fresh parse, and after
  `ApplySeverityOverrides` but **before** the `MaxFindings` limit, run
  `verifyEvidence` and then `verifyCompileClaims`. Discarded findings do not
  consume the limit. The cache stores the pre-verification findings (FR-8).
  No move is needed: today the cache `Put` runs inside the `findings == nil`
  branch, straight after a fresh parse and before `ApplySeverityOverrides`, so
  inserting verification after the overrides leaves the cached content
  unverified. A test asserts this.
- **`RunCodebase`, compare mode, per-commit:** the same two calls on their
  merged findings. Per-commit uses each commit's SHA as `TipRev`, per FR-6's
  per-commit row.
- **`BuildReport`** takes the discards, so `Summary` is computed from the kept
  findings only.
- **`--no-verify-findings`, config `verifyFindings` (default true) and env
  `PRISM_VERIFY_FINDINGS`** skip both passes.

**Cache version**

- Bump `chunkerVersion` from 3 to 4 (the FR-4 prompt change), with the comment
  updated to cover prompt changes.
- **Effect on existing caches.** The version is part of the key hash, so every
  existing entry becomes a cache miss. The first run of each diff after
  upgrading calls the model again. Nothing is corrupted or read incorrectly,
  and entries still expire by their TTL (24 h by default), so no migration or
  rollback step is needed. A rollback to the previous binary reads its own old
  keys, which are still in the cache until they expire. This is noted in the
  commit message.

**Output**

- **Text:** the "Discarded N finding(s)…" section after the findings, before
  the footer.
- **Markdown:** the same section.
- **SARIF:** discarded findings are excluded.
- **JSON:** carries `discarded` through the struct.

**Tests**

- **`diffutil.PostImageLines`:** multi-hunk line numbers; removed lines carry
  no new-line number.
- **`verify.go`:**
  - evidence found in place: kept, lines unchanged;
  - found 30 lines away: lines corrected, tagged `location-corrected`;
  - absent: discarded with the reason;
  - path not in the diff: discarded;
  - no evidence: kept and tagged `unverified`;
  - evidence spanning two hunks: found;
  - whitespace-only differences: found;
  - a match only on removed lines: lines unchanged;
  - two matches: the nearest to `startLine` is used.
- **`CompileClaims`,** with faked git and go runners:
  - claim detection, by tag and by the regex, and a non-claim;
  - tree-match per mode, including snippet (never);
  - vet passes: discarded; vet fails: kept, tagged `vet-failed`; a file excluded by build constraints: `unverified`;
  - `go` missing: `unverified`; a timeout: `unverified`;
  - memoisation: one vet per directory;
  - the total budget.
- **Engine:**
  - discarded findings are excluded from `Summary` and `failOn`;
  - the cache stores unverified findings;
  - `--no-verify-findings` passes findings through.
- **An integration-style test for AC 7:** a temp git repo holding a Go package
  that vets cleanly, plus an unstaged diff and a faked provider response
  claiming a compile error. The finding is discarded. The same finding in
  snippet mode is kept.

## Gates (each phase)

- `go test -race ./...` and `golangci-lint run ./...` pass.
- prism reviews its own staged diff with the user's default config, and every
  finding is either fixed or answered.
- Commit and push only on the user's instruction.
