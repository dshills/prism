# Prism

[![Go Reference](https://pkg.go.dev/badge/github.com/dshills/prism.svg)](https://pkg.go.dev/github.com/dshills/prism)

Local-first CLI that reviews code changes using LLM providers and emits findings with deterministic exit codes for CI gating.

Prism is **diff-centric** — it reviews only what changed, not your entire repo. It sends redacted diff hunks to one or more LLMs and returns structured findings with file paths, line numbers, and actionable suggestions. For full-repository audits, Prism also supports a **codebase** mode that reviews all tracked source files.

## Features

- **6 review modes**: unstaged, staged, commit, range, snippet, and full codebase
- **4 LLM providers**: Anthropic, OpenAI, Google Gemini, and Ollama/LMStudio (local)
- **4 output formats**: text, JSON, markdown (PR-comment-ready), and SARIF v2.1.0 — all carry AI provenance (provider/model per finding plus a top-level `_provenance` list) so downstream dashboards can distinguish AI findings from deterministic-analyzer output
- **Multi-model compare mode**: run multiple models in parallel and see consensus vs. unique findings
- **Secret redaction**: API keys, JWTs, private keys, and database credentials are automatically replaced with `[REDACTED]` before being sent to any provider
- **Rules packs**: customize severity overrides, focus areas, and required checks
- **Deterministic exit codes**: designed for CI pipelines and git hooks
- **Pre-commit hook**: install/uninstall with `prism hook install`
- **GitHub PR integration**: post review findings as PR comments
- **Caching**: file-based cache with SHA-256 keys and configurable TTL
- **Large diff handling**: diffs over `chunkBytes` (24 KB) are split into chunks of whole files, directories kept together, reviewed with bounded parallel LLM calls; each chunk's prompt lists the files in the other chunks so the model does not report them as missing

## Installation

### From source

```bash
go install github.com/dshills/prism/cmd/prism@latest
```

### Build locally

```bash
git clone https://github.com/dshills/prism.git
cd prism
go build -o prism ./cmd/prism
```

## Quick Start

1. Set your provider API key:

```bash
export ANTHROPIC_API_KEY="your-key-here"
```

2. Review your unstaged changes:

```bash
prism review unstaged
```

3. Verify your credentials work:

```bash
prism models doctor
```

## Usage

### Review Modes

**Unstaged changes** (working tree vs index):
```bash
prism review unstaged
```

**Staged changes** (index vs HEAD):
```bash
prism review staged
```

**A specific commit** (diff vs its parent):
```bash
prism review commit HEAD~1
prism review commit abc123 --parent def456  # for merge commits
```

**A revision range** (feature branch vs main):
```bash
prism review range origin/main..HEAD
prism review range origin/main..HEAD --merge-base=false
```

**Code from stdin** (snippet mode):
```bash
cat foo.go | prism review snippet --path foo.go --lang go
cat foo.go | prism review snippet --path foo.go --base foo.go.orig
```

**Full codebase** (all tracked files):
```bash
prism review codebase
prism review codebase --paths "**/*.go" --max-findings-per-file 5
prism review codebase --exclude "**/*_test.go" --fail-on high
```

Codebase mode reads all git-tracked, non-binary source files and reviews them as complete files rather than diffs. It always uses chunked review with bounded concurrency. Use `--paths` and `--exclude` to scope the review, and `--max-findings-per-file` to cap findings per file (default: 10).

### Multi-Model Compare

Run the same review across multiple models and see which findings they agree on:

```bash
prism review unstaged --compare anthropic:claude-sonnet-4-6,openai:gpt-5.2
```

Compare mode reports consensus findings (flagged by 2+ models) and unique findings per model.

Each model runs through the same pipeline as a single-model review, concurrently: chunking, the per-chunk cache, repair, splitting after a cut-off response, its provider's rate limits, and token reporting. Coverage combines them, with chunks, calls and cache use summed and tokens listed per model. A model that fails is left out as a coverage skip (exit 5) while the others' findings stand; an auth failure, a malformed spec, or every model failing fails the run. The `fallback` setting doesn't apply, since each model is named.

### Output Formats

```bash
prism review staged --format text       # Human-readable (default)
prism review staged --format json       # Full JSON report
prism review staged --format markdown   # PR-comment-friendly with collapsible sections
prism review staged --format sarif      # SARIF v2.1.0 for CI tooling
```

Write output to a file:
```bash
prism review staged --format sarif --out prism.sarif
```

### CI Integration

Use `--fail-on` to gate CI pipelines:

```bash
# Fail if any high-severity findings exist
prism review range origin/main..HEAD --fail-on high

# Full CI example: SARIF output + fail on high
prism review range origin/main..HEAD --format sarif --out prism.sarif --fail-on high
```

### Pre-Commit Hook

Install a git pre-commit hook that runs prism on staged changes:

```bash
prism hook install          # installs .git/hooks/pre-commit
prism hook uninstall        # removes the hook
```

Or manually:

```bash
# .git/hooks/pre-commit
#!/bin/sh
prism review staged --fail-on high
```

## CLI Reference

### Commands

| Command | Description |
|---------|-------------|
| `prism review unstaged` | Review working tree changes |
| `prism review staged` | Review staged changes |
| `prism review commit <sha>` | Review a specific commit |
| `prism review range <A..B>` | Review a revision range |
| `prism review snippet` | Review code from stdin |
| `prism review codebase` | Review all tracked files in the repository |
| `prism config init` | Create default config file |
| `prism config set <key> <value>` | Set a config value |
| `prism config show` | Show effective configuration |
| `prism models list` | List known providers and models |
| `prism models doctor` | Validate provider credentials |
| `prism cache show` | Show cache statistics |
| `prism cache clear` | Clear cached results |
| `prism baseline add <id>...` | Accept findings so reviews stop reporting them (`--reason`, `--force`) |
| `prism baseline remove <id>...` | Stop accepting findings |
| `prism baseline show` | List accepted findings (`--json`) |
| `prism hook install` | Install git pre-commit hook |
| `prism hook uninstall` | Remove git pre-commit hook |
| `prism version` | Print version |

### Review Flags

All review subcommands accept these flags:

| Flag | Description | Default |
|------|-------------|---------|
| `--provider` | LLM provider (`anthropic`, `openai`, `gemini`, `ollama`) | `anthropic` |
| `--model` | Model name | `claude-sonnet-4-6` |
| `--compare` | Compare mode: comma-separated `provider:model` pairs | |
| `--format` | Output format (`text`, `json`, `markdown`, `sarif`) | `text` |
| `--out` | Output file path | stdout |
| `--fail-on` | Fail threshold (`none`, `low`, `medium`, `high`) | `none` |
| `--min-severity` | Lowest severity to report (`none`, `low`, `medium`, `high`). The model is told not to write anything below it, which saves output tokens, and anything below it is dropped. Agents usually set it to their `--fail-on` | `none` |
| `--confirm-blocking` | `provider:model` asked for a second opinion on each finding at or above `--fail-on`. Refuted findings move to `discarded` with the checker's reason, and a failed check keeps its finding. Up to 20 checks a review, verdicts cached; `coverage.confirm` reports them | |
| `--reasoning-effort` | How hard a reasoning model thinks before it answers (`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`), sent as Anthropic `output_config.effort`, OpenAI/Ollama `reasoning_effort` or Gemini `thinkingLevel`. A level the model refuses is dropped for that review. Lower effort is faster and uses fewer tokens | model's default |
| `--max-findings` | Maximum number of findings. A chunked review asks each chunk for twice its even share (at least 10), not the whole limit | `50` |
| `--context-lines` | Context lines in diff | `3` |
| `--max-diff-bytes` | Maximum diff size in bytes | `500000` |
| `--chunk-bytes` | Target size of each review chunk a larger diff is split into | `24000` |
| `--paths` | Include file path globs (comma-separated) | `**/*` |
| `--exclude` | Exclude file path globs (comma-separated) | `vendor/**`, `**/*.gen.go`, `**/dist/**` |
| `--rules` | Rules file path | |
| `--no-function-context` | Keep plain hunks instead of widening them to their enclosing functions | `false` |
| `--no-auto-exclude` | Review lockfiles, generated code, minified assets, snapshots and deletions too | `false` |
| `--since` | Compare with an earlier review: a prism JSON report, a SARIF log, or `last` (this repo's last review) | |
| `--only-new` | With `--since`, report only new findings; they alone decide the exit code | `false` |
| `--fallback` | `provider:model` to review with when the primary provider fails (auth, retries exhausted, unreachable) | |
| `--baseline` | Baseline of accepted findings; `none` reports them all | `.prism-baseline.json` at the repo root |
| `--no-redact` | Disable secret redaction (prints warning) | `false` |

**Commit-specific:**

| Flag | Description | Default |
|------|-------------|---------|
| `--parent` | Override parent SHA (for merge commits) | |

**Range-specific:**

| Flag | Description | Default |
|------|-------------|---------|
| `--merge-base` | Use merge base for branch comparisons | `true` |

**Snippet-specific:**

| Flag | Description | Default |
|------|-------------|---------|
| `--path` | File path for language detection | |
| `--lang` | Language hint | |
| `--base` | Base file to diff against | |

**Codebase-specific:**

| Flag | Description | Default |
|------|-------------|---------|
| `--max-findings-per-file` | Maximum findings per file | `10` |

## Configuration

### Precedence

1. CLI flags (highest)
2. Environment variables
3. Config file
4. Defaults (lowest)

### Config File

Location: `$XDG_CONFIG_HOME/prism/config.json` (or OS-appropriate equivalent)

Create a default config:
```bash
prism config init
```

Example `config.json`:
```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-4-6",
  "compare": [],
  "format": "text",
  "failOn": "none",
  "minSeverity": "none",
  "reasoningEffort": "",
  "confirmBlocking": "",
  "maxFindings": 50,
  "contextLines": 3,
  "include": ["**/*"],
  "exclude": ["vendor/**", "**/*.gen.go", "**/dist/**"],
  "maxDiffBytes": 500000,
  "chunkBytes": 24000,
  "maxConcurrency": 0,
  "rateLimitRpm": 0,
  "rulesFile": "",
  "baselineFile": "",
  "fallback": "",
  "autoExclude": true,
  "functionContext": true,
  "prices": {},
  "cache": {
    "enabled": true,
    "dir": "",
    "ttlSeconds": 86400
  },
  "privacy": {
    "redactSecrets": true,
    "redactPaths": ["**/.env", "**/*secrets*"]
  }
}
```

### Environment Variables

| Variable | Maps to |
|----------|---------|
| `PRISM_PROVIDER` | `provider` |
| `PRISM_MODEL` | `model` |
| `PRISM_FAIL_ON` | `failOn` |
| `PRISM_MIN_SEVERITY` | `minSeverity` |
| `PRISM_REASONING_EFFORT` | `reasoningEffort` |
| `PRISM_CONFIRM_BLOCKING` | `confirmBlocking` |
| `PRISM_FORMAT` | `format` |
| `PRISM_MAX_FINDINGS` | `maxFindings` |
| `PRISM_CONTEXT_LINES` | `contextLines` |
| `PRISM_CHUNK_BYTES` | `chunkBytes` — target size of each review chunk (default 24000). One prompt carrying a large diff gets a shallow review, so keep this small |
| `PRISM_MAX_CONCURRENCY` | `maxConcurrency` — parallel LLM calls per review (0 = provider default) |
| `PRISM_RATE_LIMIT_RPM` | `rateLimitRpm` — requests per minute cap (0 = provider default) |
| `PRISM_FUNCTION_CONTEXT` | `functionContext` — widen hunks to their enclosing functions (default true) |
| `PRISM_AUTO_EXCLUDE` | `autoExclude` — leave out files not worth reviewing (default true) |
| `PRISM_FALLBACK` | `fallback` — `provider:model` used when the primary provider fails |
| `PRISM_BASELINE_FILE` | `baselineFile` — baseline path, relative to the repo root (`none` turns it off) |
| `ANTHROPIC_API_KEY` | Anthropic provider |
| `OPENAI_API_KEY` | OpenAI provider |
| `GEMINI_API_KEY` | Gemini provider |

## Accepting Findings

Findings that have been reviewed and accepted can be left out of every later review, so an agent never re-fixes or re-asks about them. A suppressed finding doesn't count toward `--fail-on`. It is still listed under "Suppressed" in text and markdown output, under `suppressed` in JSON, and as a suppressed result in SARIF, so nothing is hidden.

**Baseline file.** `.prism-baseline.json` at the repository root is meant to be committed, like `.golangci.yml`. Every agent session and CI run on the repo then respects it:

```bash
prism review staged                     # each finding shows its ID
prism baseline add 3f9c0e1a2b4d5e6f --reason "test fixture, not a real key"
prism baseline show
prism baseline remove 3f9c0e1a2b4d5e6f
```

`add` looks each ID up in the repository's last review and records its path, category and title; `--force` adds an ID that isn't there. Findings match by ID, the fingerprint of the code they're about. An ID covers one quoted piece of code, in one declaration, in one category, so accepting a finding also accepts any other finding of that category on the same code. A baseline file that can't be parsed fails the review rather than being ignored.

**Inline comments.** For a finding tied to one line, add a `prism:ignore` comment. It works in any comment syntax:

```go
secret := loadFromVault() // prism:ignore security "loaded from vault, not hardcoded"

// prism:ignore
legacyHash := md5.Sum(data)
```

The comment must start with `prism:ignore`, right after a comment opener of the file's language (`//` for Go, `#` for Python, `--` for SQL, and so on). Prism lexes the code, so the token inside a string literal, or later in a comment, is not a directive. After the token come, optionally, the categories it applies to (comma-separated; all categories when none are given) and a quoted reason. Anything else makes it no directive, so a misspelled category never widens to every category. The directive covers findings on its own line. A comment alone on its line (nothing before it, nothing after it closes) also covers the line below.

In a language with multi-line strings or block comments (most of them), a hunk below the top of a file might begin inside a string or comment. Prism then lexes the whole file instead: the working tree, the index or the reviewed revision, whichever the review mode used. Where that file can't be read or doesn't match the diff (a GitHub PR review, for example), directives in such hunks are not honored. The finding is reported, and the baseline still works.

Finding directives is best-effort. The lexer knows each language's comments and common string forms, but not every one. Rust raw and multi-line strings, YAML block scalars, and heredocs (shell, Ruby, Perl, PHP) aren't modeled, so text shaped like a directive inside them is taken as one. Anyone who can write such a string can also add a real comment, so this isn't a new way to suppress a finding, only a rare accident. When suppression has to be exact, use the baseline.

## Recording Verdicts

A model's `confidence` is its own claim. A finding's `calibration` is its track record: how often findings like it turned out to be real. After deciding what to do with a finding, record the verdict:

```bash
prism findings confirm <id>...   # a real problem, acted on (alias: accept)
prism findings dismiss <id>...   # a false positive
prism findings stats             # confirm rate by category
```

- **Where verdicts go:** each ID is looked up in the repository's last review, and the verdict is appended to `feedback.jsonl` in the cache directory, readable only by you. The log covers every repository, since a model's record in a category holds wherever it reviews.
- **What findings get:** once there are 5 verdicts, each finding in JSON output carries `calibration: {confirmRate, samples, scope}`.
  - The scope is `model` when its provider, model and category have 5 verdicts, and `category` (every model) otherwise.
  - A finding judged twice counts once, by its latest verdict.
- **Not the baseline:** `prism baseline add` stops a finding being reported. A verdict only records whether it was right.

## Function Context

A plain diff shows three lines around each change, not the function it's in. The model can't see the signature, the receiver or the variables in scope, and that's where many false positives ("err is not checked", "x may be nil") and missed bugs come from. Prism widens each hunk to its whole enclosing function (`git diff --function-context`). It does this file by file, keeping a file's plain hunks when widening would grow it more than 3× or 4 KB beyond the plain diff, whichever allows more, so one change inside a very long function can't bloat the review. `coverage.widenedFiles` counts the widened files.

It costs input tokens: on this repository's recent history the diffs grow about 1.4× (unlimited widening would be 1.6×). Turn it off with `functionContext: false` or `--no-function-context`. It applies to `unstaged`, `staged`, `commit` and `range`; snippets and codebase reviews already show whole files.

## Files Left Out

Besides your own `exclude` patterns, prism leaves out content a model has nothing useful to say about:

- **Lockfiles:** `go.sum`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `Cargo.lock`, `poetry.lock` and the like.
- **Generated code:** recognized by its header comment (Go's `// Code generated … DO NOT EDIT.`, or `@generated`). For an edit deep inside a file, prism reads the header from the working tree, the index or the reviewed commit, with one `git cat-file` for all files.
- **Minified assets and source maps** (`*.min.js`, `*.min.css`, `*.js.map`), and **test snapshots** (`*.snap`, `__snapshots__/`).
- **Sections with nothing to review:** deleted files, whose findings couldn't be verified against any new code anyway, plus renames and mode changes without edits, and binary files.

Each file left out is listed in `coverage.excluded` with its reason, and in the text footer (`Excluded 3 files not worth reviewing: go.sum (lockfile), …`). Leaving them out is policy, not a gap, so it doesn't make the review incomplete. To review them anyway, set `autoExclude: false` or pass `--no-auto-exclude`.

## Comparing with an Earlier Review

In a fix loop, the agent fixes some findings and reviews again. The new report mixes leftovers, regressions and rewordings, so it's hard to tell whether the loop is making progress. `--since` compares the review with an earlier one by finding ID:

```bash
prism review staged --since last              # this repo's previous review
prism review range origin/main..HEAD --since base.sarif --only-new
```

Each finding is marked `new` or `persisting` (`delta` in JSON, `[new]` in text, SARIF `baselineState`). The report's `delta` summary counts them and lists what was **resolved**. An earlier finding in a file this review didn't cover is counted as out of scope, not resolved, and a finding accepted in the meantime is neither. An incomplete review (a failed chunk, a truncated diff) counts nothing as resolved, because a missing finding may only have gone unreviewed. With `--only-new`, only new findings are reported and decide the exit code. `last` with no previous review counts every finding as new, so a loop can use it from the first run, and the remembered last review is always the full one.

## Rules Packs

Create a rules file to customize review behavior:

```json
{
  "focus": ["security", "correctness"],
  "severityOverrides": {
    "style": "low",
    "security": "high"
  },
  "required": [
    { "id": "go-errors", "text": "Ensure errors are wrapped with context" }
  ]
}
```

```bash
prism review staged --rules rules.json
```

- **focus**: categories the reviewer should prioritize
- **severityOverrides**: override default severity for specific categories
- **required**: checks that must be mentioned in the review

### Rule sets for parts of the repository

`sets` applies rules to some paths only, on top of the top-level rules. Later sets win where their overrides overlap.

```json
{
  "focus": ["correctness"],
  "sets": [
    { "paths": "internal/auth/**", "focus": ["security"], "severityOverrides": { "security": "high" } },
    { "paths": ["**/*_test.go", "testdata/**"], "severityOverrides": { "style": "low" } }
  ]
}
```

- **paths**: one glob or a list. `**` matches any number of directories, and a pattern without a slash (`*_test.go`) matches the file name in any directory. A set without `paths` applies everywhere.
- **Prompt:** each review chunk is given the top-level rules plus the sets that match its files. A set that matches only some of the chunk's files names them.
- **Overrides:** severity overrides are applied by each finding's own file.
- **List form:** the file can also be a list of sets, with no top-level rules.

## Providers

### Supported Providers

| Provider | Env Variable | Models |
|----------|-------------|--------|
| Anthropic | `ANTHROPIC_API_KEY` | claude-sonnet-4-6, claude-opus-4-6, claude-haiku-4-5 |
| OpenAI | `OPENAI_API_KEY` | gpt-5.3-codex, gpt-5.3-codex-spark, gpt-5.2-codex, gpt-5.2, gpt-4.1-mini, o3-mini |
| Gemini | `GEMINI_API_KEY` | gemini-3-flash-preview, gemini-3-pro-preview, gemini-2.5-flash, gemini-2.5-pro |
| Ollama | — | llama3.3, llama3.2, llama3.1, codellama, qwen2.5-coder, deepseek-coder-v2 |

### Structured Output

Prism asks every provider for its findings in the provider's structured-output mode, constrained to a JSON schema: `response_format` for OpenAI (and OpenAI-compatible servers through the `ollama` provider), `output_config.format` for Anthropic, and `responseSchema` for Gemini. Responses are therefore always valid JSON with valid severities and categories, and the repair call for malformed output is rarely needed. An endpoint that doesn't support it is detected on its first refusal and asked in plain JSON from then on. The live check is `go test -tags integration -run TestStructuredOutputLive ./internal/review`.

### Token Usage and Cost

Every report includes what the review cost in tokens, by model, under `coverage.tokens`: input (including any read from a prompt cache), output (including hidden reasoning), and the cached and reasoning parts where the provider reports them. Text and markdown add a footer line such as `Tokens: 12,400 in (8,000 cached) / 1,830 out (900 reasoning) — ~$0.06`.

The cost is an estimate, shown only when every model's price is known. Cached input is counted at the full input price, so it's an upper bound. Claude models have built-in prices (Anthropic's API rates as of 2026-09-25), and Ollama is free. Set others, or override a built-in price, in US dollars per million tokens:

```json
{ "prices": { "openai:gpt-6.1-sol": { "input": 3.0, "output": 12.0 } } }
```

Cut-off responses and repair calls count too. A run replayed entirely from cache used no tokens.

### Cut-off Responses

Every request has an output limit of 8192 tokens, and on reasoning models hidden reasoning counts toward it too. Prism checks why each response stopped: Anthropic `stop_reason: max_tokens`, OpenAI and Ollama `finish_reason: length`, Gemini `finishReason: MAX_TOKENS`. A cut-off answer is never parsed. Instead the part is split in half, by file or (for one file) by hunk, and each half is reviewed, up to four levels deep. A part that can't be split is asked once more with twice the limit. If it's still cut off, it's listed under `coverage.skipped` and the review is incomplete (exit 5), rather than reporting a partial list. `coverage.splits` counts the halvings.

A response that isn't valid JSON is repaired locally before prism asks the model to fix it.
- **What it repairs:** prose before or after the array, a `{"findings": [...]}` wrapper with text around it, trailing commas, and an answer that ends inside its last element.
- **Lost findings:** an element that's cut off, an object that isn't a finding, or an array that never closes counts as lost findings. A local repair that loses findings still asks the model to fix the response.
  - If the model's answer does no better, prism keeps the local repair, and the loss is listed under `coverage.skipped` (the review is incomplete).
  - A repaired answer that lost findings isn't cached.
- **Second call:** besides losses, prism asks the model again when the result would be ambiguous: no usable finding, or two arrays of findings.
- **Coverage:** `coverage.salvaged` counts the responses kept from a local repair.

### Fallback Provider

An agent can't fix a provider outage, so with only one provider an expired key or a rate-limit storm loses the review gate. Set a fallback:

```json
{ "fallback": "ollama:llama3.3" }
```

Prism switches to it when the primary returns an auth error, runs out of retries on rate limits or server errors, can't be reached, or doesn't have the model. It also switches when the primary can't even be created, for example because its API key isn't set. It doesn't switch for a request the endpoint refused for its content (400, 413, 422), since the fallback would refuse it too, or for a cancelled run.

Once switched, the rest of the review uses the fallback. Coverage records it (`coverage.fallback` in JSON, `fell back to …` in the text line), and findings carry the fallback's provider and model. The fallback's results aren't cached, so a later run never replays them as the primary's review. If the fallback fails too, the error names both failures; an auth failure still exits 3. Compare mode is unaffected, since each model there is named explicitly.

### Switching Providers

```bash
# Via CLI flag
prism review unstaged --provider openai --model gpt-5.2

# Via environment
export PRISM_PROVIDER=gemini
export PRISM_MODEL=gemini-3-flash-preview

# Via config
prism config set provider openai
prism config set model gpt-5.2
```

### Local Models with Ollama

Prism supports local models via [Ollama](https://ollama.com/):

```bash
ollama pull llama3
prism review unstaged --provider ollama --model llama3
```

Set `OLLAMA_HOST` to use a custom Ollama endpoint (default: `http://localhost:11434`).

## Privacy & Security

- **Secret redaction is on by default.** API keys, JWTs, private keys, bearer tokens, database connection strings, and other credentials are detected via regex patterns and replaced with `[REDACTED]` before being sent to any LLM provider.
- **Path-based redaction**: files matching `privacy.redactPaths` globs (e.g., `.env`, `*secrets*`) have their entire content redacted.
- **Cache stores only redacted payloads** with SHA-256 hashed keys.
- Use `--no-redact` to disable redaction (prints a warning to stderr).

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success — no findings at or above the `--fail-on` threshold |
| `1` | Findings exist at or above the `--fail-on` severity |
| `2` | Usage error or invalid arguments |
| `3` | Provider authentication or configuration error |
| `4` | Runtime error (git failure, IO error, schema validation failure) |

## Finding Categories

Reviews categorize findings as: `bug`, `security`, `performance`, `correctness`, `style`, `maintainability`, `testing`, `docs`.

Each finding includes:
- **Severity**: `high`, `medium`, or `low`
- **Confidence**: 0.0 to 1.0 estimate
- **Locations**: file path, line range, and optional code snippet
- **Suggestion**: actionable fix, often with code
- **Fix** (optional): `{"before", "after"}`, a replacement an agent can apply as an exact string edit. `before` is checked to occur exactly once in the whole file (whitespace included, against the real code). A fix that doesn't, or that can't be checked because the file isn't available (a GitHub PR review), is removed and the finding is tagged `fix-dropped`.
- **Stable ID**: SHA-256 fingerprint of path + category + quoted evidence + the declaration (for Go, the `func` or `type` line) it sits in. The same issue keeps its ID when the model rewords the title or the code moves, and the same code in two functions gets two IDs. Also emitted in SARIF as `partialFingerprints`. Findings without evidence fall back to path + title + start line.

## AI Development Workflows

Prism pairs well with AI coding assistants like Claude Code. Use Prism as a second-opinion reviewer on AI-generated code, with compare mode to get consensus across multiple LLMs. See [WORKFLOWS.md](WORKFLOWS.md) for detailed integration patterns.

## Dependencies

Prism has a single external dependency: [cobra](https://github.com/spf13/cobra) for CLI parsing. Everything else uses the Go standard library.

## License

MIT — see [LICENSE](LICENSE) for details.
