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
| 3 | Keep partial results when a chunk fails | Speed, Tokens | High | Partial — succeeded chunks are cached |
| 4 | Stable finding fingerprints | Accuracy, Workflow | High | Not started |
| 5 | Finding baseline / suppression | Workflow | High | Not started |
| 6 | Native structured output | Accuracy, Tokens | High | Not started |
| 7 | Structured fix format | Workflow | High | Not started |
| 8 | Fallback provider | Workflow | High | Not started |
| 9 | Delta mode: net-new findings | Workflow | High | Not started |
| 10 | Skip content not worth reviewing | Tokens, Speed | High | Not started |
| 11 | Detect truncated responses | Accuracy, Tokens | High | Not started |
| 12 | Token usage reporting | Tokens | Medium | Not started |
| 13 | Function context around hunks | Accuracy | Medium | Not started |
| 14 | Compare mode on the full pipeline | Accuracy, Speed, Tokens | Medium | Not started |
| 15 | Severity floor in the prompt | Tokens, Speed | Medium | Not started |
| 16 | Low-temperature sampling | Accuracy | Medium | Not started |
| 17 | Language-specific system prompts | Accuracy | Medium | Not started |
| 18 | Per-directory / per-language rules | Accuracy | Medium | Not started |
| 19 | Reasoning effort control | Speed, Tokens | Medium | Not started |
| 20 | Local JSON salvage before the repair call | Tokens, Speed | Medium | Not started |
| 21 | Second-opinion check for blocking findings | Accuracy | Low | Not started |
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

### 3. Keep partial results when a chunk fails (PARTIAL)

**Status:** Since #2, the chunks that succeeded are cached before the error is returned, so a rerun sends only the failed chunk. The review itself still fails, and `Retry-After` is still ignored.

**Problem:** `runChunkedCounted` returns an error as soon as any one chunk fails. That throws away the findings of every chunk that succeeded, whose tokens have already been paid for. Chunks often fail on 429s: `retryWithBackoff` retries 3 times at roughly 1s, 2s and 4s and ignores `Retry-After`, which a burst of 8 concurrent chunks easily outlasts. One rate-limited chunk fails the whole review, and the agent reruns it from scratch.

**Solution:**
- On 429, honor `Retry-After` when the response sends one, and back off longer than on 5xx errors.
- When a chunk still fails, keep the other chunks' findings and record the failed chunk in `coverage.skipped`. Exit code 5 (`ExitIncomplete`) already tells the agent the review was partial.
- ~~With #2, the successful chunks are cached, so a rerun only sends the failed chunk.~~ Done.
- Auth errors still fail fast, since no retry will fix them.

---

### 4. Stable finding fingerprints

**Problem:** A finding's ID is `sha256(path:title:startLine)`. The title is model-written text that varies from run to run, and the start line moves whenever lines are added above it. The same issue gets a new ID on the next run, so the cross-chunk deduplication, the baseline (#5) and delta mode (#9) all fail to recognize it.

**Solution:** Fingerprint on the parts that stay stable: the path, the category and the normalized `evidence`. Evidence is already checked to exist in the code, and it moves along with the code. Add an occurrence index for repeated quotes. Fall back to the current scheme when there's no evidence. Emit the fingerprint as SARIF `partialFingerprints` so GitHub Code Scanning tracks findings across runs too. This is a prerequisite for #5 and #9.

---

### 5. Finding baseline / suppression

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

The baseline depends on IDs that hold steady across runs, which today's IDs don't (#4).

---

### 6. Native structured output

**Problem:** Prism asks for JSON in the prompt, then parses free text. It strips code fences by hand, and on a parse failure it makes a second, full model call to repair the response. Severity and category values aren't constrained either, so a model can return `"critical"` or `"logic"` and the finding is still accepted.

**Solution:** Use each provider's constrained output mode with a JSON schema for findings:
- OpenAI: `response_format: {type: "json_schema", strict: true}`
- Gemini: `responseMimeType: "application/json"` with `responseSchema`
- Anthropic: structured outputs, or a forced tool call with `input_schema`
- Ollama: `format` with a JSON schema

Wrap the array in an object (`{"findings": [...]}`), because some APIs require an object at the root. That removes almost all repair calls and fence handling, and every enum value is guaranteed valid. Keep the repair pass as the fallback for endpoints that don't support schemas (LM Studio, custom OpenAI-compatible servers). This also gives #7 a schema to add `fix` to.

---

### 7. Structured fix format

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

### 8. Fallback provider

**Problem:** If the primary provider returns an auth error or runs out of retries, the review fails with exit 3 or 4. An agent can't fix a provider outage. It either stops and asks the user or skips the review, and the review gate is lost either way.

**Solution:** Add a `fallback` config field:
```json
{ "fallback": "ollama:llama3.3" }
```
When the primary provider returns an `authError` or runs out of retries, prism retries against the fallback without the caller having to do anything. Record the provider that actually ran in `coverage.reviewer` so the agent can tell its report came from the fallback. Local Ollama is the natural backstop: always available, no quota.

---

### 9. Delta mode: surface only net-new findings

**Problem:** In a fix loop, the agent fixes three findings and reviews again. The new report mixes leftover findings, new findings caused by the fix, and different wording of findings it already saw, since LLM output isn't deterministic. The agent can't tell whether it's making progress or going in circles.

**Solution:**
- `prism review staged --since prior.json` (or `.sarif`): load an earlier report and diff it against the new one by finding fingerprint (#4). Report each finding as new, resolved or persisting. With `--only-new`, emit only new findings and use only those to decide the exit code.
- In CI, use the merge-base report: `prism review range origin/main..HEAD --since base.sarif`.
- Together with the baseline (#5), this covers suppression fully. The baseline holds findings accepted for good; delta mode handles what has changed since the last run.

---

### 10. Skip content not worth reviewing

**Problem:** The default excludes are `vendor/**`, `**/*.gen.go` and `**/dist/**`. Everything else goes to the model. That includes:

- lockfiles (`go.sum`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `Cargo.lock`, `poetry.lock`)
- generated code that isn't named `*.gen.go`, such as `*.pb.go` and any file with Go's `// Code generated ... DO NOT EDIT.` header
- minified assets
- test snapshots
- sections for deleted files

A deleted file has no post-image lines, so no finding on it can pass evidence verification. Every token spent on it is wasted.

**Solution:** Add lockfiles, minified assets and snapshots to the default excludes. Detect generated files from their header line rather than their name. Drop pure-deletion and rename-only sections before chunking. List what was left out in a new `coverage.excluded` field rather than in `skipped`, so excluding by policy doesn't set exit code 5.

---

### 11. Detect truncated responses

**Problem:** Every call uses a fixed `MaxTokens: 8192`, and no provider checks why the response stopped. The stop signals are Anthropic `stop_reason: "max_tokens"`, OpenAI `finish_reason: "length"` and Gemini `finishReason: "MAX_TOKENS"`. When a response hits the limit, the cut-off JSON goes to the repair pass, which only sees the truncated text. The review then either fails or quietly reports a partial list. On reasoning models (GPT-5 and the o-series, Gemini thinking models), hidden reasoning tokens count toward the same limit, so a hard chunk can come back empty.

**Solution:** Have each provider return a typed truncation error. On truncation, split the chunk in half and review both halves (or retry with a higher limit for reasoning models), instead of sending it to repair. If a truncation can't be recovered, record it in coverage so the report is marked incomplete rather than presented as whole.

---

## 🟧 Medium Impact

### 12. Token usage reporting

**Problem:** Every provider fills in `ReviewResponse.TokensUsed`, but prism never reports it. It's also a single input-plus-output sum, which loses the split; output tokens cost several times more than input tokens. Without these numbers, none of the token-reduction items here (#2, #10, #15, #19, #20, #23) can be measured.

**Solution:**
- Have providers return `InputTokens` and `OutputTokens` separately, plus reasoning and cached tokens where the API reports them.
- Sum them into the report next to `coverage`, so they appear in JSON and SARIF output.
- Add a one-line footer to text output: `Tokens: 4,200 in / 812 out (~$0.03)`. An agent can pass that on to the user.
- Hardcode cost-per-token tables for each provider and update them alongside `KnownModels()`.

---

### 13. Function context around hunks

**Problem:** The default `contextLines` is 3. The model sees three lines around each change, but not the enclosing function's signature, its receiver or the variables in scope. That's where many false positives ("err is not checked", "x may be nil") and missed bugs come from.

**Solution:** Add a `functionContext` option that passes `--function-context` (`-W`) to `git diff`, which widens each hunk to cover the whole enclosing function. Turn it on by default when the widened diff still fits in a chunk. Fall back to `-U3` when it would push the diff into many more chunks. Evidence verification already works on post-image lines, so it needs no change. Use #12 to check the token cost against the drop in discarded and false-positive findings.

---

### 14. Compare mode on the full pipeline

**Problem:** Agents run compare mode for security-sensitive changes (the project's CLAUDE.md requires it), yet it has the weakest pipeline. `RunCompareWithOptions` sends the whole diff to each model in a single prompt. There's no chunking, even though the chunker exists because an 80 KB diff in one prompt got no findings at all. It also has no cache, no repair pass (one malformed response fails the whole compare), and no rate limit or concurrency cap (every model is called at once).

**Solution:** Run each model through the normal review pipeline concurrently, under a shared semaphore that defaults to `cfg.MaxConcurrency`, then merge the results with the existing fuzzy matching. Each model then gets chunking, its own per-provider rate limiting, per-chunk caching (#2), repair and partial-result handling (#3). Compare mode already verifies findings after merging, so verification needs no change.

---

### 15. Severity floor in the prompt

**Problem:** The prompt says "Focus especially on findings with severity X or above", so the model still writes low-severity findings. An agent gating on `--fail-on medium` ignores them, but their output tokens are already spent. Output tokens cost the most and make up most of the response time.

**Solution:** Add `minSeverity` (and `--min-severity`), which tells the model not to report anything below that level and also filters such findings after parsing. Agents would usually set it equal to `failOn`. The prompt's limit is also applied per chunk ("Return at most 50 findings"); reduce it in proportion to the number of chunks so a 10-chunk review doesn't allow 500 findings.

---

### 16. Low-temperature sampling

**Problem:** The engine never sets `ReviewRequest.Temperature`, so providers leave it out and use their own default, which is usually 1.0. The Anthropic provider doesn't send a temperature at all. The same diff produces different findings on each run. The agent chases noise, fix loops take longer to settle, and baseline and delta matching (#4, #5, #9) get harder.

**Solution:** Add a `temperature` config field defaulting to a low value (0–0.2), and send it on every provider, including Anthropic. Leave it out for models that only accept their default: OpenAI reasoning models, and Anthropic with extended thinking enabled.

---

### 17. Language-specific system prompts

**Problem:** The system prompt is the same for every language. It misses Go-specific idioms (error wrapping, `defer` ordering, interface satisfaction), Python async pitfalls, TypeScript type narrowing, Rust lifetimes and so on. That means more generic findings and more false positives, and an agent spends a fix cycle on each false positive.

**Solution:** Keep a map from language to an extra prompt section (like `extLang` in `prompt.go`). When a review covers Go files, append the Go-specific guidelines to the system prompt. Languages are already detected and passed to `BuildUserPromptWithRules`; today they're only used as a label, and they should choose the prompt sections instead.

---

### 18. Per-directory / per-language rules

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

### 19. Reasoning effort control

**Problem:** Reasoning models (GPT-5 and the o-series, Gemini thinking models, Claude with extended thinking) spend hidden tokens before they answer. Prism doesn't set an effort level, so it gets each provider's default. That can mean tens of seconds and thousands of reasoning tokens for a 20-line docs chunk.

**Solution:** Add a `reasoningEffort` config field (`low`/`medium`/`high`), mapped to OpenAI `reasoning_effort`, Gemini `thinkingConfig` and Anthropic `thinking.budget_tokens`. With #18, a rule set can raise the effort for risky paths and lower it for tests and docs. Use #12 and the discard counts to check that lower effort doesn't cost accuracy.

---

### 20. Local JSON salvage before the repair call

**Problem:** When `parseFindings` fails, prism makes a second full model call. Many failures are mechanical: prose before or after the array, a `{"findings": [...]}` wrapper, trailing commas, or a cut-off final element.

**Solution:** Try local fixes first: extract the outermost `[...]`, unwrap a single-key object, remove trailing commas, and drop an incomplete last element (recording that in coverage). Call the model only if all of these fail. This matters less once #6 is in, but still helps with Ollama, LM Studio and any fallback (#8) that can't take a schema.

---

## 🟨 Low Impact

### 21. Second-opinion check for blocking findings

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
| Honor `Retry-After` and keep partial chunk results (#3) | ~2 hrs | Coverage, exit 5 and per-chunk results already exist |
| `prism github post-comments` CLI entry point (#25) | ~3 hrs | Logic already exists in `github.go` |

---

## Out of scope

These items were dropped because they only help a person running prism at a terminal: a TTY progress ticker, code snippets in text output (agents can read the file, and `evidence` is already in JSON), `--watch` mode, shell completions, a GitHub Actions action, `pkg/prism` godoc examples, and OpenTelemetry tracing.
