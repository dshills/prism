# Improvement Backlog

Potential improvements organized by impact. Items within each tier are ordered by estimated value.

---

## 🟥 High Impact

### 1. Progress feedback during LLM calls

**Problem:** The terminal hangs silently for 40–60 seconds during LLM calls. No spinner, no chunk counter, nothing.

**Solution:** Write a lightweight progress reporter to stderr, gated on TTY detection so AI coding agents and CI pipelines are unaffected.

```go
import "golang.org/x/term"

func showProgress() bool {
    if !term.IsTerminal(int(os.Stderr.Fd())) {
        return false // captured by agent or pipe — stay silent
    }
    if os.Getenv("TERM") == "dumb" || os.Getenv("CI") != "" {
        return false // dumb terminal or CI pseudo-TTY
    }
    return true
}
```

When `showProgress()` is true, a goroutine writes `\rReviewing chunk 3/8…` to stderr with a ticker. When false (agent, pipe, CI), nothing is emitted — the agent sees only the structured stdout report and the exit code. Never write progress to stdout; that carries the JSON/SARIF report.

This is the single biggest UX pain point for interactive use.

---

### 2. Finding baseline / suppression

**Problem:** Every run reports every finding. There is no way to mark a finding as accepted so it stops appearing. This makes the pre-commit hook noisy — the same accepted findings surface on every commit.

For AI coding agents (Claude Code, Codex) this is worse than it is for humans: agents have no cross-session memory. Without suppression, an agent will see the same known finding on every run, fix-loop trying to resolve it, or escalate to the user repeatedly about something already accepted. The agent needs to know *before it starts* that certain findings are resolved at the policy level.

**Solution: two complementary mechanisms**

**1. Repo-committed baseline file (for persistent, cross-session suppression)**

Treat `.prism-baseline.json` like `.golangci.yml` — a human-maintained, source-controlled file that all agents on the repo automatically respect. The agent runs `prism review staged --fail-on medium` unchanged; prism silently filters baseline findings before setting the exit code. Agents only ever act on findings not already approved.

```bash
prism baseline add <finding-id>   # add a finding ID to .prism-baseline.json
prism baseline remove <finding-id>
prism baseline show               # list suppressed findings with their titles
```

No change to any agent workflow. No per-session decisions. The baseline is established once by a human and inherited by every future agent session automatically.

**2. Inline `// prism:ignore` comments (for line-specific suppression)**

For findings tied to a specific line of code, an inline directive is more appropriate — it survives refactors, is visible in code review, and can be added by an agent when explicitly instructed:

```go
secret := loadFromVault() // prism:ignore security "loaded from vault, not hardcoded"
```

When a user tells an agent "ignore this finding going forward," the agent adds the comment once. It persists across all future sessions because it lives in the code itself, not in agent memory.

Finding IDs are already stable SHA-256 hashes of `path + title + startLine`, so the baseline infrastructure is already implicit in the data model.

---

### 3. Per-file incremental cache invalidation (DONE)

**Problem:** The cache key is `hash(provider, model, fullDiff)`. One changed file invalidates the entire cache even when 99% of the diff is unchanged. In codebase mode this discards findings for hundreds of unmodified files.

**Solution:** Cache at per-file granularity using `hash(provider, model, fileContent)` as the key per file. The codebase review pipeline already processes files individually (via chunker). Merge per-file cached results with fresh results for changed files only. This would make repeated codebase reviews nearly instant for small iterative changes.

---

### 4. Code snippet in text output

**Problem:** The text formatter shows `path:start-end  Title` but never shows the actual code. Users have to open the file, navigate to the line, and reconstruct context manually.

**Solution:** Pull 3–5 lines of surrounding context from the diff (already in memory during review) and include them in the text output under the finding header, formatted like:

```
  src/auth/token.go:42-44  Hardcoded secret
  │ 42  secret := "hardcoded-value"
  │ 43  return sign(secret)
```

The diff content is available at finding-generation time; it just needs to be threaded through to the output layer or stored in `Location.Snippet` (the field already exists).

---

## 🟧 Medium Impact

### 5. `--watch` mode

**Problem:** Developers running tight review loops have to re-invoke prism manually after every save.

**Solution:** `prism review unstaged --watch` — use `fsnotify` (or polling as a zero-dependency fallback) to re-run the review whenever a tracked file changes. Debounce by 500ms to avoid thrashing during rapid edits. Print a separator line between runs to distinguish them.

---

### 6. Rate limiting and concurrency cap in compare mode

**Problem:** `RunCompare` launches all providers simultaneously with no rate limit or concurrency cap. Comparing 4 models fires 4 concurrent API calls immediately. The token-bucket limiter and semaphore built for chunked review are not applied here.

**Solution:** Apply the same `ratelimit.New(rpm)` + semaphore pattern from `RunChunkedWithOptions` to `RunCompareWithOptions`. Each provider has its own rate limit; the semaphore should cap total concurrent LLM calls (defaulting to `cfg.MaxConcurrency`).

---

### 7. Fallback provider

**Problem:** If the primary provider returns an auth error or exhausts retries, the review fails completely. CI blocks.

**Solution:** Add a `fallback` config field:
```json
{ "fallback": "ollama:llama3.3" }
```
When the primary provider returns an `authError` or exhausts retries, transparently retry against the fallback. Local Ollama is the natural backstop — always available, no quota.

---

### 8. Delta mode: surface only net-new findings

**Problem:** In CI, you often only want to see findings that are *new* relative to a previous run (e.g., the merge-base commit). Currently prism reviews the full diff with no notion of "already known."

**Solution:**
- `prism review range origin/main..HEAD --baseline prism-base.sarif` — load a prior SARIF report, compute the set-difference on finding IDs, and only emit findings not present in the baseline.
- Pairs with the planned baseline feature (#2) to give a complete suppression story.

---

### 9. Token usage reporting

**Problem:** `ReviewResponse.TokensUsed` is populated by every provider but never surfaced to the user. There is no visibility into cost or prompt efficiency.

**Solution:**
- Include token counts in `Report.Timing` (add `InputTokens`, `OutputTokens int64`)
- Show them in `--format text` under the timing footer: `Tokens: 4,200 in / 812 out (~$0.03)`
- Include in JSON/SARIF output for downstream cost tracking dashboards
- Provider-specific cost-per-token tables can be hardcoded and updated alongside `KnownModels()`

---

## 🟨 Output Quality

### 10. Language-specific system prompts

**Problem:** The system prompt is generic across all languages. Go has specific idioms (error wrapping, `defer` ordering, interface satisfaction) that a generic prompt misses. Same for Python async pitfalls, TypeScript type narrowing, Rust lifetime issues, etc.

**Solution:** Maintain a map of language → supplemental prompt section (similar to `extLang` in `prompt.go`). When a review covers Go files, append Go-specific guidelines to the system prompt. The language list is already detected and passed to `BuildUserPromptWithRules` — it just needs to drive prompt selection rather than just being a label.

---

### 11. Structured suggestion format

**Problem:** The `suggestion` field is free-form prose. The LLM sometimes includes a code fix, sometimes doesn't. There is no reliable way for consumers (VS Code extensions, auto-fix agents) to extract the code portion programmatically.

**Solution:** Extend the JSON schema to include an optional `fix` sub-object:
```json
{
  "suggestion": "Wrap the error with context using fmt.Errorf.",
  "fix": {
    "before": "return err",
    "after": "return fmt.Errorf(\"loading config: %w\", err)"
  }
}
```
Update the system prompt to request this structure. Falls back gracefully for models that don't comply — the `fix` field is omitted, `suggestion` prose is still present.

---

### 12. Per-directory / per-language rules

**Problem:** Rules packs apply globally. There is no way to enforce strict security rules for `internal/auth/**` while being permissive about style in `**/*_test.go`.

**Solution:** Support an array of rule sets in config, each with an optional `paths` glob:
```json
{
  "rules": [
    { "paths": "internal/auth/**", "focus": ["security"], "severityOverrides": { "security": "high" } },
    { "paths": "**/*_test.go",     "severityOverrides": { "style": "low" } }
  ]
}
```
The glob-matching infrastructure already exists in `diffutil`. The prompt builder selects and merges applicable rule sets per chunk based on chunk file paths.

---

### 13. Confidence calibration via local feedback log

**Problem:** Confidence scores (0.0–1.0) are LLM-assigned and uncalibrated. A "0.9 confidence" finding may be a false positive; a "0.5" may be critical. There is no feedback loop.

**Solution:** A local SQLite log (or JSON append-log to stay dependency-free) that records finding ID → accepted/dismissed over time. `prism findings accept <id>` and `prism findings dismiss <id>` commands. Over time, show empirical accept rates alongside confidence in the text output: `Confidence: 85% (historically 3/4 accepted)`.

---

## 🔵 Ecosystem / Developer Experience

### 14. `prism github post-comments` subcommand

**Problem:** The GitHub client already builds and posts inline review comments, but the GitHub flow bundles fetch + review + post into one command. There is no way to use a two-step CI pipeline: review in one job, post comments in another using a pre-existing SARIF file.

**Solution:** Add `prism github post-comments --pr 42 --sarif prism.sarif` that reads an existing SARIF report and posts it as a GitHub PR review. Decouples review (potentially expensive/slow) from comment posting (fast, auth-gated).

---

### 15. GitHub Actions action

**Problem:** Adoption in GitHub CI requires writing a multi-step workflow manually. No official action exists.

**Solution:** A `dshills/prism-action@v1` composite action that:
1. Installs the prism binary (via `go install` or a release artifact)
2. Runs `prism review range origin/main..HEAD --format sarif --out prism.sarif`
3. Uploads the SARIF to GitHub Code Scanning via `github/codeql-action/upload-sarif`

One `uses:` line to integrate prism into any GitHub workflow.

---

### 16. Shell completions

**Problem:** No tab completion for providers, models, formats, or flags. `prism review --provider <TAB>` does nothing.

**Solution:** Cobra's `GenBashCompletion`, `GenZshCompletion`, `GenFishCompletion` are built-in and require minimal code. Provider names and output formats can be registered as `ValidArgs`. Model names can be dynamic completions sourced from `KnownModels()`. Expose via `prism completion bash|zsh|fish|powershell`.

---

### 17. `pkg/prism` godoc examples

**Problem:** The public `pkg/prism` API has no `Example*` functions. `pkg.go.dev` shows no usage examples, making library adoption harder. Users must read the CLI source to understand how to use the library.

**Solution:** Add `example_test.go` with runnable examples for `Review`, `RenderReport`, `FilterReportBySeverity`, and `FailOnMet`. Examples serve as both documentation and regression tests (Go's testing framework runs them).

---

### 18. OpenTelemetry instrumentation

**Problem:** Each review spans multiple timed operations (git extraction, secret redaction, cache lookup, LLM call, JSON parsing). `Report.Timing` captures git and LLM milliseconds but nothing in between. In CI, there is no way to understand where time is going without modifying prism.

**Solution:** Add optional OTEL tracing behind a build tag or `PRISM_OTEL_ENDPOINT` env var. Instrument the key spans: `gitctx`, `cache.Get`, `provider.Review`, `parseFindings`. Zero overhead when OTEL is not configured; detailed traces when it is.

---

## ⚡ Quick Wins

Items estimated at a few hours each, high return on effort:

| Item | Effort | Notes |
|------|--------|-------|
| Shell completions | ~30 min | One Cobra call per shell |
| Rate limiting in compare mode | ~1 hr | Reuse existing `ratelimit` package |
| Token counts in JSON output + `--verbose` | ~1 hr | `TokensUsed` already populated per provider |
| stderr progress ticker for chunked reviews | ~2 hrs | Ticker goroutine + `\r` line overwrite |
| Code snippet in text output | ~2 hrs | `Location.Snippet` field already exists |
| `pkg/prism` godoc examples | ~2 hrs | `example_test.go` with runnable examples |
| `prism github post-comments` CLI entry point | ~3 hrs | Logic already exists in `github.go` |
