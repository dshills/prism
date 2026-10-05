# SPEC: Review integrity — honest coverage and verified findings

## Problem

Two failures make prism unreliable as an automated gate, the way AI coding
agents use it. Both were observed on 2026-09-30 while reviewing a 2,200-line
change in AnimalHealthRecord (AHR-414).

1. **A clean result does not show that a review happened.**
   - Text output omits "Reviewed by" and the timing line whenever there are no
     findings.
   - A cache replay reports `llmMs: 0`, the same as no model call.
   - A diff truncated at `maxDiffBytes` gets a marker string appended but no
     flag, so the unreviewed remainder is invisible.
   - `review range --per-commit` skips a commit whose diff errors, and continues
     past a commit whose review fails, then exits 0.

   An agent reading "No issues found" cannot tell a full review from a partial
   one or from none.
2. **Confident findings that the code refutes are reported as-is.** Three
   gemini-3.8-flash findings, each at 90–100% confidence, were false:
   - "`p.f[0]` must be set to −1": that exact line was in the diff.
   - "`wg.Go` does not exist; compilation failure": the package built and its
     tests passed.
   - A finding placed at a file's header comment, far from the code it
     described.

## Goals

- **G1.** Every report states what was reviewed, how, and by which model, even
  with zero findings. A report that did not cover its whole input exits with a
  distinct code.
- **G2.** Findings are checked against the diff, and for Go against the
  compiler, before they are reported. A finding that fails a check is removed
  from the findings and listed separately with the reason, never silently
  deleted.

## Non-goals

- Suppression or baselines (IMPROVEMENTS #2).
- Calibration (IMPROVEMENTS #13).
- Compile checks for any language other than Go.
- Changing which issues a model finds. Verification only removes findings the
  code refutes.

---

## Functional requirements

### FR-1: Coverage in every report

`Report` gains a `coverage` object, always present in JSON and filled by every
review path: single, chunked, compare, codebase, per-commit range, and
`pkg/prism`.

| Field | Type | Meaning |
|---|---|---|
| `reviewer` | `[]{provider, model}` | Every model asked to review, from config, independent of findings. Compare mode lists each model. |
| `files` | int | Distinct files in the reviewed diff. |
| `bytes` | int | Bytes of diff reviewed, after exclusion and truncation. |
| `chunks` | int | Review units sent to a model: 1 for an unchunked diff, N for chunked, 0 when nothing was sent. |
| `llmCalls` | int | Model calls made in this run, including repair passes and a failed call to the primary provider before its fallback answered. Retries of one call inside a provider are not counted. 0 when the result came entirely from cache. |
| `cacheHit` | bool | All the findings were replayed from cache: no chunk was sent. |
| `fallback` | `{reviewer: {provider, model}, reason}` | Present when the configured provider failed and the fallback provider reviewed instead (all or part of the input); `reason` is the primary's failure. The fallback is also listed in `reviewer`. |
| `cachedChunks` | int | Of `chunks`, how many were replayed from cache instead of sent. Equals `chunks` when `cacheHit` is true. |
| `truncatedBytes` | int | Bytes cut by `maxDiffBytes` (0 when not truncated). |
| `skipped` | `[]{target, reason}` | Parts of the input not reviewed. For per-commit range, `target` is the short commit SHA. For truncation, `target` is `"diff"`. For a failed chunk, `target` is `"chunk 2/5 (a.go, b.go)"`: its position and up to five of its files. |
| `complete` | bool | True when the whole input was reviewed: nothing skipped for an error, nothing truncated. |

**Empty input.** An empty diff (nothing staged, a range with no changes after
filtering) is **complete** with `chunks: 0`. There was nothing to review, so
nothing was missed.

**Per-commit range.**
- Coverage is the sum over reviewed commits.
- A commit skipped because its diff is empty is **not** incomplete.
- A commit skipped for a diff error, or a commit whose review failed, is added
  to `skipped` and makes the report incomplete.

**Chunk failures.** In a chunked review (diff or codebase), a chunk whose
review still fails after retries is added to `skipped`, with the provider's
error as the reason. The other chunks' findings are kept and reported, and the
report is incomplete. The chunks that succeeded are cached, so a rerun sends
only the chunks that failed. The whole review still fails, with no report,
when:
- any chunk fails with an auth error (exit 3), since it applies to every chunk;
- the context is cancelled;
- no chunk produced a result, from a model or from cache.

A review that is not chunked has only one part, so its failure fails the
review as before.

**Cache hits.** A cached result reports `cacheHit: true` and `llmCalls: 0`.
`chunks` is the count the review would have used.

**Partial cache hits.** A chunked diff review caches each chunk on its own, so
some chunks can be replayed while the rest are sent. Such a review reports
`cacheHit: false`, the full `chunks` count, the replayed ones in
`cachedChunks`, and only the calls actually made in `llmCalls`. The text
coverage line adds them after the chunk count:
`Reviewed 41 files (79.4 KB) in 5 chunks (4 from cache) by openai/gpt-6-sol — 1 LLM call, 12.1s`.

### FR-2: Coverage line in text and markdown output

- **Text** always prints one coverage line in the footer, including when there
  are no findings. It uses one of three forms, with byte sizes in KB to one
  decimal:
  - `Reviewed 41 files (79.4 KB) in 5 chunks by openai/gpt-6-sol — 5 LLM calls, 57.3s`
  - `Replayed from cache: 41 files (79.4 KB), originally reviewed by openai/gpt-6-sol`
  - `Nothing to review (empty diff)`
- **Incomplete reviews** add a line beginning `INCOMPLETE:` that lists each
  `skipped` entry, and the truncated byte count when there is one.
- **The timing footer** ("Completed in …") is printed for zero-finding reports
  too.
- **Markdown** prints the same coverage sentence under the summary.

### FR-3: Exit code for incomplete reviews

- New exit code `ExitIncomplete = 5`, set when `coverage.complete` is false.
- When `failOn` is also met, `ExitFindings = 1` takes precedence, because it is
  the stronger signal.
- Provider and auth errors keep 4 and 3.
- `--allow-incomplete` restores exit 0 for incomplete reviews. The coverage
  report is unchanged, so the gap is still visible.

### FR-4: Findings quote their evidence

- Both system prompts (diff review and codebase review) require one new field
  per finding: `"evidence": "the exact line or lines of code, copied verbatim
  from the diff, that show the problem"`.
- `rawFinding` and the cache format carry `evidence`.
- `Finding` exposes it as `evidence` in JSON.

### FR-5: Evidence verification

Each finding is checked against the reviewed diff after parsing and after cache
load. Cached findings are re-verified every run.

**Order.** FR-5 runs first. Only findings that survive it go on to FR-6.
**Discards.** Every finding FR-5 or FR-6 discards is appended to `discarded`
(FR-7) with its reason. Nothing is dropped anywhere else.

1. **Path.** The finding's `path` must be one of the diff's files. If not, the
   finding is **discarded** with reason `path not in diff`.
2. **Evidence present.** Normalise both sides: strip the leading
   `+`/`-`/space of diff lines, trim each line, collapse internal whitespace
   runs to a single space, and drop blank lines. Every evidence line must
   appear, in order and contiguously, among the lines of that file's section.
   The check is over the file's section only, never the whole diff.
   - The section's header lines (`diff --git`, `index`, `---`, `+++`) and the
     `@@` hunk headers are removed before matching, so they are never part of
     a match.
   - "Contiguous" means adjacent after that removal and after dropping blank
     lines, **within one hunk**. The lines between two hunks are not in the
     diff, so a quote across a hunk boundary is not contiguous code and does
     not match. (Revised during implementation. The first draft allowed it,
     and the self-review showed it let a non-existent quote pass.)
   - If the evidence is not found, the finding is **discarded** with reason
     `quoted evidence not found in <path>`.
   - *Accepted limitation.* Whitespace is collapsed inside string literals
     too, so a quote of `"a b"` matches code containing `"a  b"`. This can
     only let a finding through; it never discards one wrongly. A precise fix
     would need a tokenizer for each language.
3. **Location correction.** When the evidence is found and its new-file line
   range (computed from `@@` hunk headers) does not overlap the finding's
   `startLine`–`endLine`, the finding's lines are replaced with the evidence's
   range, and the finding gets the tag `location-corrected`.
   - Evidence that matches only removed (`-`) lines has no new-file position,
     so its lines are left unchanged.
   - When evidence matches in more than one place, the match nearest the
     stated `startLine` is used.
4. **No evidence.** A finding without `evidence` is **kept** and tagged
   `unverified`. This covers an older cache entry (FR-8 normally prevents
   those from being replayed) and a model that ignored the instruction. It is
   not discarded, because a missing quote is not evidence against the finding.
   The `unverified` tag lets a reader weigh it accordingly.

### FR-6: Compile verification (Go only)

A finding is a **compile claim** when all of these hold:

- its path ends in `.go`;
- either its tags include one of `compiler-error`, `compile-error`,
  `compilation`, or its **title** matches, case-insensitively (the message is
  not read, because a finding can discuss compilation without claiming that
  its code fails to compile; revised after the self-review discarded such a
  finding):
  `\b(does not|doesn't|won't|will not|fails? to) compile\b|\bcompil(e|ation)
  (error|failure)\b|\bundefined: |\bhas no (field or )?method\b|\bnot declared\b`.

A compile claim is checked only when two conditions hold.

**1. Tree-match.** The working tree holds exactly the reviewed version of the
claim's **Go module**. `go vet` compiles the package and everything it
imports, and uses `go.mod`, `go.sum` and any `go.work`. So a match on the named
file, or even the whole package, is not enough: an unstaged edit to a
dependency, or a new untracked `.go` file, can change whether it compiles.

- The module is the nearest directory at or above the package holding a
  `go.mod`, or the repo root.
- **Tracked inputs** are every file in the module (`:(glob)<mod>/**`) plus
  `go.work` and `go.work.sum` at the repo root. Every file counts, not just
  `.go` files, because cgo and assembly sources and `go:embed` assets also
  affect the build.
- **Untracked inputs** are:
  - any untracked file in the module that git does not ignore, since it could
    be an embed asset or source;
  - ignored files that are compile inputs: `.go`, `.c`, `.h`, `.s`, `.S`,
    `.syso`, a `go.mod`, or a root `go.work`. Vet compiles an ignored
    generated file just the same.

  Other ignored files, such as build outputs, do not count.
- **Local dependencies.** A `go.work` at the repo root, or a `replace`
  directive in the module's `go.mod` pointing to a local path (`./` or `../`),
  pulls in code outside the module. Tree-match then does not hold.
- Tree-match is evaluated once per module per run.

| Mode | Tree-match holds when |
|---|---|
| `unstaged`, `codebase` | there are no untracked inputs in the module. Both modes review the working tree itself. |
| `staged` | the above, and the module's tracked inputs have no unstaged changes (`git diff --quiet -- <tracked specs>`) |
| `commit`, `range`, `range --per-commit` | the above, and the module's tracked inputs in the working tree equal the reviewed tip (`git diff --quiet <tip> -- <tracked specs>`). The tip is the commit, or the right-hand side of `A..B`, or `HEAD` when that is omitted. The tip need not be `HEAD`. |
| `snippet` | never |

**2. The file is in the build.** `go list` in the package directory must list
the named file among its compiled files, under the current platform and build
tags. A file excluded by build constraints is not compiled by vet, so a clean
vet says nothing about it. `go list` runs once per directory, under the same
60-second timeout as vet, and its time counts toward the 120-second total
budget.

*Revised during implementation (2026-09-30).*
- The first draft required the tip to be `HEAD` and checked only the named
  file. Reviewing the merged AHR-414 range therefore left gemini's false
  `wg.Go` claim unverified, although the package was identical.
- The self-review then showed that neither a file-level nor a package-level
  match covers dependency edits or build constraints.

When tree-match holds and `go` is on `PATH`:

- prism runs `go vet .` with the working directory set to the file's
  directory, and a 60-second timeout per package.
- Vet compiles the package's test files as well, so it covers `_test.go`.
- Results are memoised per directory within a run.
- **Vet succeeds (exit 0):** the finding is **discarded** with reason `package
  compiles: go vet passed in <dir>`.
- **Vet fails:** the finding is kept and tagged `vet-failed`. Vet also fails
  on analyzer warnings and missing dependencies, so this records what
  happened; it does not assert that the claim is right.
- **Vet times out, or `go` is missing:** the finding is kept and tagged
  `unverified`.
- **Either condition does not hold:** the finding is kept and tagged
  `unverified`, and vet is not run.

The total compile-verification time per run is capped at 120 seconds.
- Each `go list` and `go vet` command gets the per-package timeout, cut to
  whatever remains of the total, so a late command cannot overrun it.
- Findings still unchecked at the cap are kept and tagged `unverified`.

*Accepted residuals (from the self-review, 2026-09-30):*
- Ignored `go:embed` assets that are not source files are not detected.
- Whitespace inside string literals is collapsed (FR-5).
Neither can wrongly discard a finding except through that rare ignored embed
asset.

### FR-7: Discarded findings are reported, not hidden

- `Report` gains `discarded: [{finding, reason}]`, always present in JSON (an
  empty array when there are none).
- Discarded findings are excluded from `findings`, `summary`, the `failOn`
  exit logic, and SARIF.
- In a per-commit range review, discarded findings carry their commit's short
  SHA on their locations, as kept findings do.
- **Text output** ends with `Discarded N finding(s) that failed verification:`
  and one line per finding: path, line, title, and reason.
- **Markdown** gets the same section.
- `--no-verify-findings` (config `verifyFindings: false`, env
  `PRISM_VERIFY_FINDINGS=false`) turns off FR-5 and FR-6 entirely. `discarded`
  is then empty, and no finding gains a verification tag.

### FR-8: Cache

- Cache entries store findings **before** verification, so a changed
  environment (a file edited since, or `go` installed) re-verifies correctly.
- The prompt change in FR-4 bumps `chunkerVersion`, which is part of the diff
  cache key, so older cached reviews without evidence are not replayed as
  current.

---

## Acceptance criteria

1. A zero-finding text report prints the coverage line and the timing footer.
2. A cache-replayed report shows "Replayed from cache" and `cacheHit: true` in
   JSON.
3. A diff over `maxDiffBytes` sets `truncatedBytes > 0` and `complete: false`,
   and exits 5. With `--allow-incomplete` it exits 0.
4. A per-commit range where one commit's review fails exits 5 and lists that
   commit in `skipped`. Empty-diff commits do not make the report incomplete.
5. A finding whose evidence is absent from its file's section is in
   `discarded` with reason `quoted evidence not found in <path>`, and not in
   `findings` or `summary`.
6. A finding whose evidence is found 30 lines from its stated `startLine` gets
   the evidence's lines and the tag `location-corrected`.
7. A Go compile-claim finding in a package that vets cleanly, reviewed in
   `unstaged` mode, is discarded with the `go vet passed` reason. The same
   finding in `snippet` mode is kept and tagged `unverified`.
8. With `--no-verify-findings`, findings pass through unchanged and
   `discarded` is empty.
9. The tests pass under `-race`, and `golangci-lint` is clean.
