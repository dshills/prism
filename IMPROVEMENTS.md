# Improvement Backlog

Prism is run by AI coding agents (Claude Code, Codex), not by people at a terminal. Each run is a step in an agent's loop: review, read the findings, fix, review again. Items are ranked by how much they help that loop:

- **Accuracy:** each false positive costs the agent a fix cycle, and each missed bug gets past the gate.
- **Speed / throughput:** the agent waits on every run, often several times per change.
- **Token usage:** agents run prism on every commit and every pass of a fix loop, so cost adds up without anyone noticing.
- **Workflow:** the agent can act on findings without guessing, and a review never fails outright.

## Status

| # | Item | Improves | Priority | Status |
|---|------|----------|----------|--------|
| 1 | Cache keys that cover every prompt input | Accuracy | High | **Done** |
| 2 | Per-chunk caching for diff modes | Speed, Tokens | High | **Done** |
| 3 | Keep partial results when a chunk fails | Speed, Tokens | High | **Done** |
| 4 | Stable finding fingerprints | Accuracy, Workflow | High | **Done** |
| 5 | Finding baseline / suppression | Workflow | High | **Done** |
| 6 | Native structured output | Accuracy, Tokens | High | **Done** |
| 7 | Structured fix format | Workflow | High | **Done** |
| 8 | Fallback provider | Workflow | High | **Done** |
| 9 | Delta mode: net-new findings | Workflow | High | **Done** |
| 10 | Skip content not worth reviewing | Tokens, Speed | High | **Done** |
| 11 | Detect truncated responses | Accuracy, Tokens | High | **Done** |
| 12 | Token usage reporting | Tokens | Medium | **Done** |
| 13 | Function context around hunks | Accuracy | Medium | **Done** |
| 14 | Compare mode on the full pipeline | Accuracy, Speed, Tokens | Medium | **Done** |
| 15 | Severity floor in the prompt | Tokens, Speed | Medium | **Done** |
| 16 | ~~Low-temperature sampling~~ | Accuracy | — | **Dropped** (see #16) |
| 17 | Language-specific system prompts | Accuracy | Medium | **Done** |
| 18 | Per-directory / per-language rules | Accuracy | Medium | **Done** |
| 19 | Reasoning effort control | Speed, Tokens | Medium | **Done** |
| 20 | Local JSON salvage before the repair call | Tokens, Speed | Medium | **Done** |
| 21 | Second-opinion check for blocking findings | Accuracy | Low | **Done** |
| 22 | Concurrent per-commit review | Speed | Low | Not started |
| 23 | Trim repeated per-chunk prompt overhead | Tokens | Low | Not started |
| 24 | Confidence calibration | Accuracy | Low | Not started |
| 25 | `prism github post-comments` | Workflow | Low | Not started |
| — | Per-file incremental cache invalidation | Speed, Tokens | — | **Done** (f844f11) |

---

## 🟥 High Impact

### 1. Cache keys that cover every prompt input (DONE)

**Status:** Done. `promptFingerprint` hashes the system prompt and the user prompt rendered around an empty diff, so every builder input is covered without being listed by hand. `reviewCacheKey` adds that fingerprint and `chunkerVersion` to both the diff key and the per-file key. The severity policy in the rules section is now sorted, so the same rules always give the same prompt and the same key.

**Problem:** A cached review is replayed whenever its key matches, but the keys leave out inputs that change what the model is asked:

- `diffCacheKey` (`internal/review/engine.go`) covers the provider, model, `chunkerVersion`, chunk size and diff. It leaves out the rules file, `failOn` and `maxFindings`, and both of the last two are written into the user prompt. If an agent changes `--fail-on` or edits the rules, it still gets the old answer.
- The codebase per-file key is `BuildCacheKey(provider, model, section)`, which doesn't even include `chunkerVersion`. After the evidence prompt change (version 4), codebase mode still replays findings from the older prompt. Those findings have no `evidence`, so they're tagged `unverified` and kept instead of being checked.

**Solution:** Derive every cache key from a hash of the prompt inputs, plus the content: the system prompt text, the rules section, `failOn`, `maxFindings`, `maxFindingsPerFile` and `chunkerVersion`. One helper used by both paths keeps them in step. Any new prompt input then goes through the same helper, so it can't be left out of the key.

---

### 2. Per-chunk caching for diff modes (DONE)

**Status:** Done, per chunk rather than per file. A chunked diff caches each chunk under `chunkCacheKey` (the chunk's own text plus the prompt fingerprint from #1), sends only the misses, and merges the results in chunk order. Prompts for the chunks that are sent still list every other part's files. Coverage reports the replayed chunks in `cachedChunks`, and the text line reads `in 5 chunks (4 from cache)`.

Per-file caching was rejected for diff modes because it replays stale findings. Say a finding on `a.go` comes from how it uses `b.go`, and the agent fixes it by editing only `b.go`. `a.go`'s cached entry would still replay the finding. A chunk is reviewed as one unit, so any edit inside it re-reviews the whole chunk.

The limit: a diff that fits in one chunk (24 KB by default) is still cached as a whole, so a re-review after any edit re-reviews all of it. Chunk boundaries depend on file sizes, so an edit that grows a file enough can move later chunks' boundaries and invalidate them as well.

**Problem:** Diff modes (`unstaged`, `staged`, `commit`, `range`) cache the whole diff under a single key. In a fix loop the agent edits one file and reviews again. The diff has changed, so every chunk goes back to the model, including the ones covering files that didn't change. This is the most common prism call, and it pays full price every time.

**Solution:** Use the per-file cache from codebase mode (`storeFindingsPerFile`) for diff modes as well. Key each file section with the prompt inputs from #1, send only the misses to the model, and merge the results. Chunks are grouped by directory, so an edit usually invalidates a single chunk. The `otherPartsNote` file list should not be part of the key, or every new file would invalidate everything. A fix-loop re-review then costs about as much as the files the agent actually touched.

---

### 3. Keep partial results when a chunk fails (DONE)

**Status:** Done.
- **Retries:** providers honor `Retry-After` (seconds or an HTTP date) and `retry-after-ms` on 429 and 5xx, with up to 25% jitter so chunks given the same wait don't all return at once. A wait over 60s fails at once rather than stalling the review. Without a header, a 429 backs off from 2s and a 5xx from 1s.
- **Partial results:** in diff and codebase modes, a chunk that still fails becomes a `coverage.skipped` entry (`chunk 2/5 (a.go, b.go)`). The other chunks' findings are reported and the run exits 5. An auth error, a cancelled context, or no result at all still fails the review.
- **No false clean:** in codebase mode, a failed chunk's files are not cached as clean.
- **Related fixes:** `IsAuthError` now sees through wrapped errors (an auth failure used to exit 4 instead of 3), and `prism github` now exits 5 for an incomplete review, as `review` does.

**Problem:** `runChunkedCounted` returns an error as soon as any one chunk fails. That throws away the findings of every chunk that succeeded, whose tokens have already been paid for. Chunks often fail on 429s: `retryWithBackoff` retries 3 times at roughly 1s, 2s and 4s and ignores `Retry-After`, which a burst of 8 concurrent chunks easily outlasts. One rate-limited chunk fails the whole review, and the agent reruns it from scratch.

**Solution:**
- On 429, honor `Retry-After` when the response sends one, and back off longer than on 5xx errors.
- When a chunk still fails, keep the other chunks' findings and record the failed chunk in `coverage.skipped`. Exit code 5 (`ExitIncomplete`) already tells the agent the review was partial.
- ~~With #2, the successful chunks are cached, so a rerun only sends the failed chunk.~~ Done.
- Auth errors still fail fast, since no retry will fix them.

---

### 4. Stable finding fingerprints (DONE)

**Status:** Done (`internal/review/fingerprint.go`).
- **What the ID hashes:** the path, the category, the evidence (normalized the way verification matches it), and the enclosing declaration. The declaration is the nearest line at or above the evidence that starts one, by git's hunk-header rule (for Go, the `func` or `type` line). It is read from the hunk, or from the hunk header that git computes from the whole file, so it doesn't depend on the diff's context size. Rewording the title or moving the lines no longer changes the ID. The same code in two functions gets two IDs.
- **Same code, same declaration and category:** two findings like this share an ID. Numbering them was rejected because the order comes from titles or list order, both of which shift, so one finding could inherit the other's ID. Neighboring-line anchors were also tried; they changed the ID whenever the hunk's context changed. `DeduplicateFindings` matches ID plus title plus start line, so both findings are kept.
- **Other cases:** the ID is computed from the reviewed text, so it doesn't depend on chunking. Findings without evidence keep the earlier `path:title:startLine` ID.
- **SARIF:** the ID is emitted as `partialFingerprints["prismFindingId/v1"]`. Severity overrides no longer regenerate it.
- **Limits:** renaming the enclosing function changes the ID, so the finding shows up as new. Git takes the hunk header from the old file, so an uncommitted rename above a hunk shows the old name until it's committed. Declarations are cut to the 80 bytes git keeps in a header, so long signatures anchor the same either way. A repo whose `.gitattributes` sets a `diff=` driver (such as `golang`) gets header text from that driver's pattern, which may not match the in-hunk rule. Such a finding can change ID when the hunk grows to include its declaration.

**Problem:** A finding's ID is `sha256(path:title:startLine)`. The title is model-written text that varies from run to run, and the start line moves whenever lines are added above it. The same issue gets a new ID on the next run, so the cross-chunk deduplication, the baseline (#5) and delta mode (#9) all fail to recognize it.

**Solution:** Fingerprint on the parts that stay stable: the path, the category and the normalized `evidence`. Evidence is already checked to exist in the code, and it moves along with the code. Add an occurrence index for repeated quotes. Fall back to the current scheme when there's no evidence. Emit the fingerprint as SARIF `partialFingerprints` so GitHub Code Scanning tracks findings across runs too. This is a prerequisite for #5 and #9.

---

### 5. Finding baseline / suppression (DONE)

**Status:** Done.
- **Baseline file:** `.prism-baseline.json` at the repo root (`baselineFile`, `--baseline`, `PRISM_BASELINE_FILE`; `none` turns it off) is managed by `prism baseline add/remove/show`. `add` looks the ID up in the repo's last review, which the CLI keeps in the cache dir, and records the finding's path, category, title, reason and date. `--force` adds an ID that isn't in that review.
- **Inline:** `prism:ignore [categories] ["reason"]` must start a real comment, and any other text after the token rejects the directive.
  - **How it's found:** a small per-language lexer (comment and string syntax by file extension, including each multi-line string's escape rule: none for Go raw strings, backslash, or doubled quotes for SQL) carries strings and block comments across lines, so string data can't suppress a finding. Where a language's rule is unclear, it keeps a string open longer, which can only hide a directive.
  - **Mid-file hunks:** for a language with multi-line strings or block comments, a hunk below line 1 is lexed as part of the whole file (working tree, index or reviewed revision), after checking the file matches the diff. Where it can't be read, those hunks honor no directives; the finding is reported and the baseline still applies.
  - **Coverage:** a directive covers its own line, and the line below when the comment is alone on its line.
  - **Best-effort, by decision:** the lexer doesn't model Rust raw and multi-line strings, YAML block scalars, or heredocs (shell, Ruby, Perl, PHP), so directive-shaped text inside them counts as a directive. Prism review flagged these as high. They were accepted because whoever can write such a string can write a real comment, so it isn't an escalation; exact suppression is the baseline's job. A stricter follow-up would honor directives only where an exact lexer exists (Go's `go/scanner`), language by language.
- **Where it runs:** both mechanisms run in `review.FinalizeFindings`, the step every path ends in, after verification and before `maxFindings`. They therefore apply to cache replays, chunked, codebase, compare, per-commit, GitHub PR and `pkg/prism` reviews alike.
- **Reporting:** suppressed findings leave `findings` and the exit code but are listed under `suppressed` (JSON), "Suppressed" (text and markdown), and as SARIF results with `suppressions` (`external` for the baseline, `inSource` inline).
- **IDs visible:** text and markdown output now show each finding's ID, so an agent can baseline it.
- **Failure modes:** a baseline that can't be parsed fails the review rather than silently reporting every accepted finding again. Saves are atomic (temp file plus rename). Last reports are kept only when the repository is known, so a GitHub PR review isn't filed under the working directory's repo; baselining from one needs `--force`. Reports are written 0600 in a 0700 directory, since they hold evidence.

**Problem:** Every run reports every finding, and agents remember nothing between sessions. An agent sees the same accepted finding on every run. It either keeps trying to fix it or asks the user about it again. The agent needs to know, *before it starts*, which findings have already been settled.

**Solution: two complementary mechanisms**

**1. Repo-committed baseline file (persistent, cross-session suppression)**

Treat `.prism-baseline.json` like `.golangci.yml`: a source-controlled file that every agent on the repo respects automatically. The agent runs `prism review staged --fail-on medium` as usual. Prism drops baseline findings before setting the exit code, so agents only act on findings that haven't been accepted.

```bash
prism baseline add <finding-id>   # add a finding ID to .prism-baseline.json
prism baseline remove <finding-id>
prism baseline show               # list suppressed findings with their titles
```

Agents run these commands when the user says "accept that finding". Nothing else in the agent's workflow changes, and every later session picks up the result.

**2. Inline `// prism:ignore` comments (line-specific suppression)**

A finding tied to one line is better handled with a directive in the code. It survives refactors, shows up in code review, and an agent can add it when told to:

```go
secret := loadFromVault() // prism:ignore security "loaded from vault, not hardcoded"
```

The directive lives in the code, not in agent memory, so it holds across all future sessions.

The baseline keys on finding fingerprints, which hold steady across runs (#4). Its granularity is a quoted piece of code, in one declaration, in one category: accepting a finding also accepts any other finding of that category on the same code in the same function, since the two share an ID. A finer split needs a stable issue type from the model (a fixed rule list, which #6's schema could add).

---

### 6. Native structured output (DONE)

**Status:** Done.
- **The schema:** `internal/review/output.go` defines the findings schema: an object holding the findings array, with severity and category as enums. A test keeps it in step with `rawFinding`.
- **Provider modes:** `internal/providers/schema.go` renders it per provider: strict JSON Schema in `response_format` for OpenAI and the OpenAI-compatible Ollama/LM Studio endpoint, `output_config.format` (JSON outputs) for Anthropic, and `responseSchema` with upper-case types for Gemini.
- **Where it's used:** every review request asks for it, including single, chunked, repair and compare calls.
- **Fallback:** when an endpoint refuses the request (400/422) and the same request succeeds without the schema, the provider stops asking for the rest of the run. An unrelated 400 doesn't switch it off.
- **Parsing and prompt:** `parseFindings` reads the object or a bare array, so the repair pass still covers endpoints without the mode. The system prompts tell the model to use the tool or format when one is given.
- **Why not a forced tool call:** a forced tool call (`tool_choice: tool`) was the first version for Anthropic. It was replaced because Claude Opus 5.5, Sonnet 5.5 and Fable 5.1 refuse forced tool use with a 400, which would have silently turned structured output off on the newest models.
- **Verified live:** `TestStructuredOutputLive` (build tag `integration`) confirms structured answers from OpenAI gpt-6.1-sol; Anthropic Claude Haiku 4.5, Sonnet 5.5 and Sonnet 4.6; and Gemini 3 Flash.

**Problem:** Prism asks for JSON in the prompt, then parses free text. It strips code fences by hand, and on a parse failure it makes a second, full model call to repair the response. Severity and category values aren't constrained either, so a model can return `"critical"` or `"logic"` and the finding is still accepted.

**Solution:** Use each provider's constrained output mode with a JSON schema for findings:
- OpenAI: `response_format: {type: "json_schema", strict: true}`
- Gemini: `responseMimeType: "application/json"` with `responseSchema`
- Anthropic: structured outputs, or a forced tool call with `input_schema`
- Ollama: `format` with a JSON schema

Wrap the array in an object (`{"findings": [...]}`), because some APIs require an object at the root. That removes almost all repair calls and fence handling, and every enum value is guaranteed valid. Keep the repair pass as the fallback for endpoints that don't support schemas (LM Studio, custom OpenAI-compatible servers). This also gives #7 a schema to add `fix` to.

---

### 7. Structured fix format (DONE)

**Status:** Done.
- **The field:** findings carry an optional `fix: {before, after}`, requested through the structured-output schema (#6) and the system prompts. The schema requires the field, so "no fix" arrives as empty strings, and a no-op fix is ignored.
- **The check:** verification keeps a fix only if `before` occurs exactly once in the file. It matches whitespace exactly, against the real (unredacted) code an agent edits. It checks the whole reviewed file (the working tree, the index or the tip revision) and counts overlapping matches. Without that file, it accepts only a diff that shows the whole file (a new file or a codebase section), because being unique in the visible hunks says nothing about the code around them. A failed fix is removed and the finding tagged `fix-dropped`; the finding and its prose suggestion stay.
- **Output and cache:** text and markdown show the fix as a replacement, JSON carries it, and the cache keeps it.
- **Verified live:** on a test diff, gpt-6.1-sol, Claude Haiku 4.5 and Gemini 3 Flash each returned fixes, and all of them were applicable as exact replacements.

**Problem:** The `suggestion` field is free-form prose. Sometimes the LLM includes a code fix, sometimes it doesn't. An agent has to read the prose, work out the change, and find where it goes. That's slow and error-prone, and two agents can read the same suggestion differently.

**Solution:** Extend the JSON schema with an optional `fix` sub-object:
```json
{
  "suggestion": "Wrap the error with context using fmt.Errorf.",
  "fix": {
    "before": "return err",
    "after": "return fmt.Errorf(\"loading config: %w\", err)"
  }
}
```
Update the system prompt to ask for this structure. If a model doesn't comply, `fix` is simply left out and the `suggestion` prose remains.

`before` can be checked against the diff the same way `evidence` is today (d6ab437). A `fix` whose `before` doesn't match the code is dropped, so an agent can apply any remaining `fix` as an exact string replacement.

---

### 8. Fallback provider (DONE)

**Status:** Done.
- **Setting it:** `fallback` (`provider:model`; also `--fallback` and `PRISM_FALLBACK`) wraps the provider in `providers.Fallback`.
- **When it switches:** on an auth error, retries exhausted (429/5xx), a transport failure or a 404, and when the primary can't be created (a missing key). It doesn't switch on a cancelled run or on 400/413/422. The switch is sticky for the run, and the fallback is created only if needed.
- **Reporting:** coverage gains `fallback: {reviewer, reason}`, the fallback joins `coverage.reviewer`, and the text line says `fell back to …`. Findings carry the fallback's provenance.
- **Cache:** the fallback's answers aren't cached, whether a single diff, a chunk or a codebase file, so the primary's review is never replaced by the fallback's on a later run.
- **Failure:** if the fallback fails too, the error wraps both, and `IsAuthError` still sees a primary auth failure (exit 3).
- **Scope:** compare mode doesn't use a fallback.

**Problem:** If the primary provider returns an auth error or runs out of retries, the review fails with exit 3 or 4. An agent can't fix a provider outage. It either stops and asks the user or skips the review, and the review gate is lost either way.

**Solution:** Add a `fallback` config field:
```json
{ "fallback": "ollama:llama3.3" }
```
When the primary provider returns an `authError` or runs out of retries, prism retries against the fallback without the caller having to do anything. Record the provider that actually ran in `coverage.reviewer` so the agent can tell its report came from the fallback. Local Ollama is the natural backstop: always available, no quota.

---

### 9. Delta mode: surface only net-new findings (DONE)

**Status:** Done.
- **The flags:** `--since <prism JSON | SARIF | last>` and `--only-new` work on every review command and on `prism github`, where `--only-new` posts only new comments. `last` is the repo's last review, which the CLI already keeps from #5; with no previous review, every finding is new.
- **Comparing:** `review.ApplyDelta` matches by finding ID (#4) and labels each finding `new` or `persisting`. It lists **resolved** earlier findings only for files this review covered (the rest count as `outOfScope`), and never reports an accepted (suppressed) finding as new or resolved. An incomplete review counts nothing as resolved.
- **`--only-new`:** findings and the exit code are limited to new ones, but the remembered last review stays the full report, so the next run doesn't see persisting findings as new.
- **Output:** text marks `[new]`/`[persisting]` and lists resolved findings, markdown does the same, and SARIF sets `baselineState`. Reading a SARIF log back uses the same `partialFingerprints` key the writer uses.
- **Usage error:** `--only-new` without `--since` exits 2 before any review runs.

**Problem:** In a fix loop, the agent fixes three findings and reviews again. The new report mixes leftover findings, new findings caused by the fix, and different wording of findings it already saw, since LLM output isn't deterministic. The agent can't tell whether it's making progress or going in circles.

**Solution:**
- `prism review staged --since prior.json` (or `.sarif`): load an earlier report and diff it against the new one by finding fingerprint (#4). Report each finding as new, resolved or persisting. With `--only-new`, emit only new findings and use only those to decide the exit code.
- In CI, use the merge-base report: `prism review range origin/main..HEAD --since base.sarif`.
- Together with the baseline (#5), this covers suppression fully. The baseline holds findings accepted for good; delta mode handles what has changed since the last run.

---

### 10. Skip content not worth reviewing (DONE)

**Status:** Done (`internal/gitctx/autoexclude.go`). These are built-in rules, separate from the user's `exclude` patterns.
- **What's left out:** lockfiles (by base name), minified assets and source maps, test snapshots, and generated code. Generated code is recognized by a `Code generated … DO NOT EDIT` or `@generated` comment in its first 10 lines, read from the diff when it shows line 1. Otherwise the header comes from the working tree, the index or the tip revision, with one `git cat-file --batch` for all files. Also left out: deletions, and sections with no hunks (renames, mode changes, binaries).
- **Where and how it's reported:** it applies before truncation, and in codebase mode too. Each excluded file is listed in `coverage.excluded` with its reason and named in the text and markdown footer. It doesn't make the review incomplete.
- **Opting out:** `autoExclude` (default true), `--no-auto-exclude` or `PRISM_AUTO_EXCLUDE`.

**Problem:** The default excludes are `vendor/**`, `**/*.gen.go` and `**/dist/**`. Everything else goes to the model. That includes:

- lockfiles (`go.sum`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `Cargo.lock`, `poetry.lock`)
- generated code that isn't named `*.gen.go`, such as `*.pb.go` and any file with Go's `// Code generated ... DO NOT EDIT.` header
- minified assets
- test snapshots
- sections for deleted files

A deleted file has no post-image lines, so no finding on it can pass evidence verification. Every token spent on it is wasted.

**Solution:** Add lockfiles, minified assets and snapshots to the default excludes. Detect generated files from their header line rather than their name. Drop pure-deletion and rename-only sections before chunking. List what was left out in a new `coverage.excluded` field rather than in `skipped`, so excluding by policy doesn't set exit code 5.

---

### 11. Detect truncated responses (DONE)

**Status:** Done.
- **Detection:** each provider returns a typed truncation error (`providers.IsTruncated`) for `stop_reason: max_tokens`, `finish_reason: length` or `finishReason: MAX_TOKENS`. It's checked before the content, so an empty reasoning-model answer is a truncation too. It isn't retried, and the fallback doesn't switch on it.
- **Recovery:** single-diff and chunked reviews now share one `reviewPart`, which also replaced two copies of the request-and-repair code. A cut-off response is never parsed or repaired. The part is halved instead, by file at the byte midpoint, or by hunk with the file header kept, up to four levels deep. Each half's prompt says what the other half holds.
- **When it can't split:** an unsplittable part is asked once more at 16384 tokens. If it's still cut off, it's an error: a chunk becomes a coverage skip (exit 5), and a single-diff review fails with the reason.
- **Reporting:** `coverage.splits` counts the halvings, and the text line says `N parts split after a cut-off response`.

**Problem:** Every call uses a fixed `MaxTokens: 8192`, and no provider checks why the response stopped. The stop signals are Anthropic `stop_reason: "max_tokens"`, OpenAI `finish_reason: "length"` and Gemini `finishReason: "MAX_TOKENS"`. When a response hits the limit, the cut-off JSON goes to the repair pass, which only sees the truncated text. The review then either fails or quietly reports a partial list. On reasoning models (GPT-5 and the o-series, Gemini thinking models), hidden reasoning tokens count toward the same limit, so a hard chunk can come back empty.

**Solution:** Have each provider return a typed truncation error. On truncation, split the chunk in half and review both halves (or retry with a higher limit for reasoning models), instead of sending it to repair. If a truncation can't be recovered, record it in coverage so the report is marked incomplete rather than presented as whole.

---

## 🟧 Medium Impact

### 12. Token usage reporting (DONE)

**Status:** Done.
- **Provider usage:** providers return `Usage{input, output, reasoning, cachedInput}`, normalized across APIs. Anthropic's input adds cache reads and writes, Gemini's output adds thinking tokens, and OpenAI's comes from the `*_details` fields. It's recorded even for cut-off responses.
- **In the report:** usage is summed per model (a fallback can mix models) over every call, including repairs and halves, into `coverage.tokens`. Per-commit reviews merge by model.
- **Cost:** `costUSD` estimates from built-in Claude prices (Anthropic's rates as of 2026-09-25, dated snapshots priced as their model), a free Ollama, and the `prices` config map for everything else, which also overrides built-ins. Text and markdown print `Tokens: N in (… cached) / N out (… reasoning) — ~$X`, with the cost only when every model is priced.

**Problem:** Every provider fills in `ReviewResponse.TokensUsed`, but prism never reports it. It's also a single input-plus-output sum, which loses the split; output tokens cost several times more than input tokens. Without these numbers, none of the token-reduction items here (#2, #10, #15, #19, #20, #23) can be measured.

**Solution:**
- Have providers return `InputTokens` and `OutputTokens` separately, plus reasoning and cached tokens where the API reports them.
- Sum them into the report next to `coverage`, so they appear in JSON and SARIF output.
- Add a one-line footer to text output: `Tokens: 4,200 in / 812 out (~$0.03)`. An agent can pass that on to the user.
- Hardcode cost-per-token tables for each provider and update them alongside `KnownModels()`.

---

### 13. Function context around hunks (DONE)

**Status:** Done (`internal/gitctx/funccontext.go`).
- **How:** diff modes run git twice, plain and with `--function-context`, and take each file's widened section unless it grows more than `max(4 KB, 3×)` over the plain one. That's per file rather than "whole diff fits in a chunk", so one long function can't switch the context off for everything else. If the widened run fails, the plain diff stands.
- **Settings:** on by default (`functionContext`, `--no-function-context`, `PRISM_FUNCTION_CONTEXT`), and `coverage.widenedFiles` counts the widened files.
- **Measured:** on this repo's last 12 commits (146 file diffs), the input went from 436 KB to 632 KB, 1.4×; unlimited widening would be 1.6×. 92 files were widened.
- **Not yet measured:** the accuracy gain, meaning the drop in discarded and false-positive findings, needs a set of live reviews to compare.
- **No changes needed elsewhere:** fingerprints already anchor on the enclosing declaration from the hunk or its header, and evidence checks use post-image lines.

**Problem:** The default `contextLines` is 3. The model sees three lines around each change, but not the enclosing function's signature, its receiver or the variables in scope. That's where many false positives ("err is not checked", "x may be nil") and missed bugs come from.

**Solution:** Add a `functionContext` option that passes `--function-context` (`-W`) to `git diff`, which widens each hunk to cover the whole enclosing function. Turn it on by default when the widened diff still fits in a chunk. Fall back to `-U3` when it would push the diff into many more chunks. Evidence verification already works on post-image lines, so it needs no change. Use #12 to check the token cost against the drop in discarded and false-positive findings.

---

### 14. Compare mode on the full pipeline (DONE)

**Status:** Done.
- **One pipeline:** `reviewPipeline` is now `collectFindings` (redact, chunk, cache, model, severity overrides) plus `FinalizeFindings`. `RunCompare` runs `collectFindings` once per model, concurrently, with `cfg.Fallback` cleared, then merges with the existing fuzzy matching. Every model therefore gets chunking (codebase compare always chunks), the per-chunk cache, repair, cut-off splitting (#11), its own provider's rate limit and concurrency, partial results (#3) and token usage (#12).
- **Coverage:** `compareCoverage` combines the models' coverage: chunks, calls, cached chunks, splits and tokens summed, a cache hit only if every model's was, and each model's skips named for it.
- **Failures:** a failed model is a skip, and the review is incomplete. An auth failure, a malformed spec or a cancelled run fails it, as does every model failing.
- **Concurrency:** it's capped per provider, not across models. Two specs from the same provider each get that provider's limits.

**Problem:** Agents run compare mode for security-sensitive changes (the project's CLAUDE.md requires it), yet it has the weakest pipeline. `RunCompareWithOptions` sends the whole diff to each model in a single prompt. There's no chunking, even though the chunker exists because an 80 KB diff in one prompt got no findings at all. It also has no cache, no repair pass (one malformed response fails the whole compare), and no rate limit or concurrency cap (every model is called at once).

**Solution:** Run each model through the normal review pipeline concurrently, under a shared semaphore that defaults to `cfg.MaxConcurrency`, then merge the results with the existing fuzzy matching. Each model then gets chunking, its own per-provider rate limiting, per-chunk caching (#2), repair and partial-result handling (#3). Compare mode already verifies findings after merging, so verification needs no change.

---

### 15. Severity floor in the prompt (DONE)

**Status:** Done.
- **Floor:** `minSeverity` is set by config, `PRISM_MIN_SEVERITY`, `--min-severity` or `pkg/prism` `MinSeverity`, and must be `none`, `low`, `medium` or `high`.
  - Above `low`, the prompt says to report nothing below it, and `FinalizeFindings` drops anything below it after the rules' severity overrides, before verification and the limit. Dropped findings aren't listed anywhere.
  - The "focus" line for `failOn` is kept only when `failOn` is above the floor.
  - The floor is part of the prompt fingerprint, so a review cached without it isn't replayed as one that had it.
- **Per-chunk limit:** each of n chunks is asked for `min(maxFindings, max(ceil(2·maxFindings/n), 10))`, twice its even share. This applies to diff and codebase reviews.
  - The share isn't in the cache key, just like the other-parts note. A change in the number of chunks still replays the unchanged ones.
  - A chunk whose answer fills a lowered share may have left findings out, so it isn't cached. A later review that asks it for more reviews it again.
  - The final `maxFindings` cut is unchanged and still keeps the most severe findings.

**Problem:** The prompt says "Focus especially on findings with severity X or above", so the model still writes low-severity findings. An agent gating on `--fail-on medium` ignores them, but their output tokens are already spent. Output tokens cost the most and make up most of the response time.

**Solution:** Add `minSeverity` (and `--min-severity`), which tells the model not to report anything below that level and also filters such findings after parsing. Agents would usually set it equal to `failOn`. The prompt's limit is also applied per chunk ("Return at most 50 findings"); reduce it in proportion to the number of chunks so a 10-chunk review doesn't allow 500 findings.

---

### 16. Low-temperature sampling (DROPPED)

**Dropped:** most of the models agents run reject or advise against a lowered temperature.
- **Anthropic:** sampling parameters return a 400 on Fable 5/5.1, Opus 5.5/5/4.8/4.7 and Sonnet 5. Sonnet 5.5 accepts only the default. Older models take a temperature only with thinking off.
- **OpenAI:** reasoning models (the o-series and GPT-5 and later) reject a non-default temperature.
- **Gemini 3:** Google recommends leaving it at 1.0 and warns that lower values can cause looping.
- **Local reasoning models** (Qwen3 thinking, DeepSeek-R1, gpt-oss) recommend 0.6–1.0, and Ollama already applies each model's recommended setting from its Modelfile.

The lever these models offer is reasoning effort (#19). Run-to-run consistency comes from structured output (#6), the caches (#1, #2), stable IDs (#4) and delta mode (#9). The original item follows for the record.

**Problem:** The engine never sets `ReviewRequest.Temperature`, so providers leave it out and use their own default, which is usually 1.0. The Anthropic provider doesn't send a temperature at all. The same diff produces different findings on each run. The agent chases noise, fix loops take longer to settle, and baseline and delta matching (#4, #5, #9) get harder.

**Solution:** Add a `temperature` config field defaulting to a low value (0–0.2), and send it on every provider, including Anthropic. Leave it out for models that only accept their default: OpenAI reasoning models, and Anthropic with extended thinking enabled.

---

### 17. Language-specific system prompts (DONE)

**Status:** Done.
- **Guidance:** `internal/review/langguide.go` has guidance for Go, Python, JavaScript/TypeScript, Rust, Java, C/C++, Shell and SQL. Each covers the mistakes a generic review misses in that language, plus a "do not report" list of that language's common false positives (for example unchecked `bytes.Buffer` writes, and style a linter or formatter enforces). The Go guidance also explains when a captured loop variable is shared (before Go 1.22, or a variable declared outside the loop) and when it isn't.
- **Use:** `SystemPromptFor` / `CodebaseSystemPromptFor` add a section for each language among a chunk's files, in a fixed order, so the same languages always give the same prompt. Diff, codebase and compare reviews all use them.
- **Cache:** the prompt fingerprint renders the prompt for one file of each guided language, so a change to any guidance is a cache miss.

**Problem:** The system prompt is the same for every language. It misses Go-specific idioms (error wrapping, `defer` ordering, interface satisfaction), Python async pitfalls, TypeScript type narrowing, Rust lifetimes and so on. That means more generic findings and more false positives, and an agent spends a fix cycle on each false positive.

**Solution:** Keep a map from language to an extra prompt section (like `extLang` in `prompt.go`). When a review covers Go files, append the Go-specific guidelines to the system prompt. Languages are already detected and passed to `BuildUserPromptWithRules`; today they're only used as a label, and they should choose the prompt sections instead.

---

### 18. Per-directory / per-language rules (DONE)

**Status:** Done. They live in the rules file, not the main config, next to the rules they extend.
- **Format:** `sets` is a list of rule sets, each with `paths` (one glob or a list), `focus`, `severityOverrides` and `required`. The file can also be a bare list of sets.
  - Sets don't nest. `paths` at the top level, or a glob that can't be parsed, is an error.
- **Globs:** a matcher in `rules.go` handles `**` across any number of directories. `gitctx.MatchesAny` matches `**` one level only. A pattern without a slash matches the file name.
- **Prompt:** each chunk gets the top-level rules plus the sets matching its files. A set matching only some of them names those files (up to 20).
- **Overrides:** `ApplySeverityOverrides` applies the top-level overrides, then each matching set's by the finding's own path, later sets winning.
- **Cache:** sets render only for matching files, so the prompt fingerprint also keys the sets' JSON.
- **Not covered:** a per-path reasoning effort (#19). A request has a single effort, and a chunk can mix paths.

**Problem:** Rules packs apply globally. You can't enforce strict security rules for `internal/auth/**` and be lenient about style in `**/*_test.go`. Agents get blocked by low-value findings in test code, or let high-risk code through at a lax threshold.

**Solution:** Support an array of rule sets in config, each with an optional `paths` glob:
```json
{
  "rules": [
    { "paths": "internal/auth/**", "focus": ["security"], "severityOverrides": { "security": "high" } },
    { "paths": "**/*_test.go",     "severityOverrides": { "style": "low" } }
  ]
}
```
The glob matching already exists in `diffutil`. For each chunk, the prompt builder picks and merges the rule sets that match the chunk's file paths.

---

### 19. Reasoning effort control (DONE)

**Status:** Done.
- **Setting:** `reasoningEffort` is set by config, `PRISM_REASONING_EFFORT`, `--reasoning-effort` or `pkg/prism` `ReasoningEffort`. It is one of `none`, `minimal`, `low`, `medium`, `high`, `xhigh` or `max`, and the default is unset (each model's own default).
- **Per provider:** it's sent as Anthropic `output_config.effort` (next to the structured-output format, with thinking left at the model's default), OpenAI and Ollama `reasoning_effort`, and Gemini `thinkingConfig.thinkingLevel`.
- **Refusals:** not every model takes every level, or effort at all.
  - A 400/422 is asked again without the effort (keeping structured output), then without the schema alone, then without both.
  - Only the fields the successful request left out are not sent to that endpoint again (`sendOptional` in `providers/schema.go`), so a field is switched off only after every attempt that kept it was refused.
- **Cache:** effort is part of the prompt fingerprint, so a review at one effort isn't replayed as one at another. Unset keeps the earlier keys.
- **Not covered:** per-path effort waits on #18. Raw temperature is not sent (#16).

**Problem:** Reasoning models (GPT-5 and the o-series, Gemini thinking models, Claude with extended thinking) spend hidden tokens before they answer. Prism doesn't set an effort level, so it gets each provider's default. That can mean tens of seconds and thousands of reasoning tokens for a 20-line docs chunk.

**Solution:** Add a `reasoningEffort` config field (`low`/`medium`/`high`), mapped to OpenAI `reasoning_effort`, Gemini `thinkingConfig` and Anthropic `thinking.budget_tokens`. With #18, a rule set can raise the effort for risky paths and lower it for tests and docs. Use #12 and the discard counts to check that lower effort doesn't cost accuracy.

---

### 20. Local JSON salvage before the repair call (DONE)

**Status:** Done.
- **Parsing:** `salvageFindings` (`internal/review/salvage.go`) reads every array in the response that's outside a quoted string. An array nested inside one already read is part of it.
  - It splits each array into elements, skipping strings whole, and decodes each one on its own after removing trailing commas.
  - An object that isn't a finding (one with a title, path and severity), an element that's cut off, or an array that never closes was meant as findings, so it counts as a loss. Prose and scalars, such as `[see below]`, are not a loss.
  - Brackets are matched by type, so a stray `}` ends nothing. A mismatch, or a quote left open at the end, means salvage gives up.
- **When it repairs:** only when exactly one array holds findings and nothing was lost in any other array.
- **Losses:** a local repair that lost findings still makes the repair call, because a lost finding may have been the blocking one.
  - If the model's answer does no better, prism keeps the local repair.
  - Its losses are a `coverage.skipped` entry, so the review is incomplete (exit 5).
  - An answer with no findings is never salvaged, because a `[]` somewhere in a broken response is no evidence of a clean review.
  - Two arrays of findings, a loss in another array, an answer cut off in its first finding, and prose with an unpaired quote all go to the repair call.
- **Use:** it runs on the first response and on the repair response. Only a response with no array at all makes the repair call.
- **Coverage:** `salvaged` counts the responses kept from a local repair, summed across per-commit and compare reviews.
- **Cache:** a repaired answer that lost findings isn't cached (`chunkRun.partial`, which also covers #15's lowered share).

**Problem:** When `parseFindings` fails, prism makes a second full model call. Many failures are mechanical: prose before or after the array, a `{"findings": [...]}` wrapper, trailing commas, or a cut-off final element.

**Solution:** Try local fixes first: extract the outermost `[...]`, unwrap a single-key object, remove trailing commas, and drop an incomplete last element (recording that in coverage). Call the model only if all of these fail. This matters less once #6 is in, but still helps with Ollama, LM Studio and any fallback (#8) that can't take a schema.

---

## 🟨 Low Impact

### 21. Second-opinion check for blocking findings (DONE)

**Status:** Done.
- **Setting:** `confirmBlocking` is a `provider:model`, set by config, `PRISM_CONFIRM_BLOCKING`, `--confirm-blocking` or `pkg/prism` `ConfirmBlocking`, and validated as such.
- **Placement:** `FinalizeFindings` runs it last, after verification, inline ignores and the baseline, and before `maxFindings`. Every review path (diff, codebase, compare, per-commit) gets it.
- **The check:** each finding at or above `failOn` gets one call. It carries the redacted finding and up to 12 KB of its file's redacted diff, and asks for `{"verdict": "confirm"|"refute", "reason"}` (structured output where supported).
  - **Locating the code:** in a larger file the excerpt is centred on the finding's line, found through the hunk headers, or on its evidence when that occurs only once. A finding whose code can't be located isn't checked and stays.
  - The checker is told to confirm when the code shown isn't enough to tell.
  - Refuted findings go to `discarded` with the checker's reason. A check that fails or can't be parsed keeps its finding.
- **Cost:** up to 20 checks a review (the most severe first), 4 at a time.
  - Verdicts are cached by the checker, the effort and the full prompt, so a fix-loop re-review doesn't pay again.
  - Calls and tokens go into coverage, and `coverage.confirm` reports how many were checked, refuted, failed and left unchecked.

**Problem:** The evidence check and `go vet` catch made-up quotes and false compile claims. They can't catch a wrong argument, such as "this can be nil" when it can't. The findings that hurt most are the ones at or above `failOn`, because they block the agent and start a fix cycle.

**Solution:** Add an optional `confirmBlocking` setting. For each finding at or above `failOn`, send one small call with the finding and the code around it to a cheap model (the fallback, or a configured one), asking it to confirm or refute. Move refuted findings to `discarded` with the model's reason. This costs one short call per blocking finding, and every false positive it removes saves the agent a full fix cycle.

---

### 22. Concurrent per-commit review

**Problem:** `runPerCommitReview` reviews commits one at a time. Each commit's chunks run in parallel, but a 20-commit range takes about 20 times as long as a single review.

**Solution:** Review the commits concurrently, all sharing one provider rate limiter and semaphore. Keep the report in commit order and keep the existing skip and coverage handling.

---

### 23. Trim repeated per-chunk prompt overhead

**Problem:** Every chunk repeats the system prompt, the rules section and an `otherPartsNote` listing up to 200 file names (`maxContextFiles`). In a large codebase review, each of N chunks carries the same list of about 2,000 tokens.

**Solution:**
- Beyond a small threshold, replace the file list with per-directory counts (`internal/review/: 14 files`).
- Order each prompt so the content shared by all chunks comes first (system prompt, rules, language guidance) and the chunk's own diff comes last, so provider prefix caching applies. OpenAI caches automatically above about 1,024 tokens; Anthropic needs `cache_control`. Today's system prompt is under those minimums, but once #17 and #18 add to it, caching becomes worthwhile.

---

### 24. Confidence calibration via local feedback log

**Problem:** Confidence scores (0.0–1.0) come from the LLM and aren't calibrated. A "0.9 confidence" finding may be a false positive, and a "0.5" may be critical. Agents can't use confidence to decide what to act on.

**Solution:** Keep a local JSON append-log that maps finding fingerprint (#4) to accepted or dismissed. Add `prism findings accept <id>` and `prism findings dismiss <id>`, which an agent runs after deciding what to do with a finding. Over time, report each category's historical accept rate in the JSON output next to `confidence`, so agents (and `--fail-on` gating) can weight findings by evidence rather than by what the model claims. This overlaps with #5, so build it after the baseline.

---

### 25. `prism github post-comments` subcommand

**Problem:** `prism github <pr>` fetches, reviews and posts in one step. An agent can't check the findings and drop false positives before they appear on the PR.

**Solution:** Add `prism github post-comments --pr 42 --report prism.json` (or `--sarif`). It reads an existing report and posts it as a GitHub PR review. The agent can then review with `prism github <pr> --dry-run --format json`, filter the findings, and post only the ones it confirmed. The posting logic already exists in `github.go`.

---

## ⚡ Quick Wins

| Item | Effort | Notes |
|------|--------|-------|
| Low-temperature sampling (#16) | ~1 hr | `Temperature` is already plumbed through three of the four providers |
| Severity floor in the prompt (#15) | ~1 hr | One prompt line plus a post-parse filter |
| Function context option (#13) | ~1 hr | Pass `-W` to `git diff` |
| Token counts in JSON output (#12) | ~1 hr | `TokensUsed` already populated per provider |
| Skip lockfiles and generated files (#10) | ~2 hrs | Default excludes plus a header check |
| `prism github post-comments` CLI entry point (#25) | ~3 hrs | Logic already exists in `github.go` |

---

## Out of scope

These items were dropped because they only help a person running prism at a terminal: a TTY progress ticker, code snippets in text output (agents can read the file, and `evidence` is already in JSON), `--watch` mode, shell completions, a GitHub Actions action, `pkg/prism` godoc examples, and OpenTelemetry tracing.
