
Spec: prism (working name) — Local AI Code Review CLI

1) Purpose

Build a local-first CLI that reviews code changes (unstaged/staged/commit/range/snippet) using one or more LLM providers, and emits findings in human-readable and machine-readable formats with deterministic exit codes for CI gating.

Non-goals:
	•	Not an MCP server.
	•	Not a GitHub App (at least initially).
	•	Not a formatter/linter replacement.
	•	Not a “rewrite your whole repo” agent.

Primary goals:
	•	Fast to run in a terminal.
	•	Predictable output and exit codes.
	•	Good defaults; configurable when needed.
	•	Provider-agnostic LLM layer.
	•	Minimal context, high signal (diff-centric, not repo dump).

2) Target Users
	•	Solo devs and teams wanting “second-opinion” code review locally.
	•	CI pipelines that want an AI-based review gate or SARIF report.
	•	People who live in git diff and want suggestions with line numbers.

3) Key Use Cases
	1.	Local dev, quick check:

prism review unstaged

	2.	Before commit (staged gate):

prism review staged --fail-on high

	3.	Review a commit:

prism review commit HEAD~1

	4.	Review a range (feature branch vs main):

prism review range origin/main..HEAD

	5.	Snippet from stdin:

cat foo.go | prism review snippet --path foo.go --lang go

	6.	CI integration:

prism review range origin/main..HEAD --format sarif --out prism.sarif --fail-on high

4) CLI Surface Area

4.1 Top-level commands
	•	prism review ... (core)
	•	prism config ... (persist settings)
	•	prism models ... (provider/model discovery + doctor)
	•	prism cache ... (optional: inspect/clear cache)
	•	prism version

4.2 Review subcommands

prism review unstaged
Reviews git diff (working tree vs index).
Flags:
	•	--paths <glob,...> include filters
	•	--exclude <glob,...>
	•	--context-lines <n> (default 3)
	•	--max-diff-bytes <n> (default e.g. 500k)
	•	--provider, --model, --compare
	•	--format text|json|markdown|sarif
	•	--out <file> (default stdout)
	•	--fail-on none|low|medium|high (default none)
	•	--max-findings <n> (default 50)
	•	--rules <file> (custom rules pack; optional)

prism review staged
Reviews git diff --cached.

Same flags as unstaged.

prism review commit <sha>
Reviews the diff for a commit (vs its parent).
Flags:
	•	--parent <sha> (override parent; for merges)
	•	same output/model flags

prism review range <revRange>
Reviews combined diff for a range (A..B).
Flags:
	•	--per-commit (review each commit separately and aggregate)
	•	--merge-base (default true; uses merge base for branch comparisons)
	•	same output/model flags

prism review snippet
Reads code from stdin.
Flags:
	•	--path <file> (required for good messages)
	•	--lang <lang> (optional; inferred from path)
	•	--base <file> (optional: provide “before” version to compute diff)
	•	model/output flags

4.3 Config commands

prism config init
Creates config file in:
	•	$XDG_CONFIG_HOME/prism/config.json (or OS equivalent)

prism config set key value
Example:

prism config set provider anthropic
prism config set failOn high

prism config show
Print effective config (after merging env/file/defaults).

4.4 Models commands

prism models list
Lists known providers + common models (best-effort; static list is OK).

prism models doctor
Validates credentials and runs a tiny “ping” request.

5) Configuration

5.1 Precedence
	1.	CLI flags
	2.	Environment variables
	3.	Config file
	4.	Defaults

5.2 Config file schema (v1)

config.json example:

{
  "provider": "anthropic",
  "model": "claude-3-5-sonnet",
  "compare": [],
  "format": "text",
  "failOn": "none",
  "maxFindings": 50,
  "contextLines": 3,
  "include": ["**/*"],
  "exclude": ["vendor/**", "**/*.gen.go", "**/dist/**"],
  "maxDiffBytes": 500000,
  "rulesFile": "",
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

5.3 Environment variables
	•	PRISM_PROVIDER
	•	PRISM_MODEL
	•	PRISM_FAIL_ON
	•	PRISM_FORMAT
	•	PRISM_MAX_FINDINGS
	•	PRISM_CONTEXT_LINES
	•	Provider keys (choose a consistent naming scheme):
	•	OPENAI_API_KEY
	•	ANTHROPIC_API_KEY
	•	GEMINI_API_KEY (or GOOGLE_API_KEY)
	•	optional: PRISM_OPENAI_BASE_URL for compatible endpoints

6) Output Contract

6.1 Common Finding model (canonical internal schema)

{
  "tool": "prism",
  "version": "1.0",
  "runId": "uuid",
  "repo": {
    "root": "/abs/path",
    "head": "sha",
    "branch": "name"
  },
  "inputs": {
    "mode": "unstaged|staged|commit|range|snippet",
    "range": "A..B",
    "pathsIncluded": ["..."],
    "pathsExcluded": ["..."]
  },
  "summary": {
    "counts": { "low": 3, "medium": 2, "high": 1 },
    "highestSeverity": "high"
  },
  "findings": [
    {
      "id": "stable-id-hash",
      "severity": "low|medium|high",
      "category": "bug|security|performance|correctness|style|maintainability|testing|docs",
      "title": "Short human title",
      "message": "What is wrong and why it matters",
      "suggestion": "Actionable fix, possibly with code",
      "confidence": 0.0,
      "locations": [
        {
          "path": "relative/file.go",
          "hunk": "@@ -12,7 +12,9 @@",
          "lines": { "start": 12, "end": 18 },
          "commit": "sha (optional)",
          "snippet": "small excerpt (optional)"
        }
      ],
      "tags": ["go", "sql", "n+1", "race-condition"],
      "references": ["optional urls or docs names"]
    }
  ],
  "timing": {
    "gitMs": 12,
    "llmMs": 1530,
    "totalMs": 1700
  }
}

Notes:
	•	id should be stable-ish (hash of path + title + hunk context) so CI diffs are sane.
	•	confidence is an estimate; used only for sorting/filters.

6.2 Text format

Human friendly:
	•	Summary header
	•	Group by severity, then by file
	•	Each finding shows: file:line range, title, 2–4 lines of message, suggestion.

6.3 Markdown format

For PR comments:
	•	A heading, summary table
	•	Collapsible sections by severity
	•	Code fences for suggestions

6.4 SARIF format

Map findings to SARIF results with:
	•	severity -> level (note/warning/error)
	•	location mapping using artifactLocation.uri + region startLine/endLine
	•	ruleId derived from category/title hash

6.5 Exit codes

Deterministic:
	•	0: success; no findings at/above fail threshold
	•	1: findings at/above --fail-on
	•	2: usage error / invalid args
	•	3: provider auth/config error
	•	4: runtime error (git error, IO error, etc.)

7) Review Engine Behavior

7.1 Input collection rules (diff-centric)

Default behavior:
	•	Collect diff hunks for included files only.
	•	Limit total bytes with maxDiffBytes. If exceeded:
	•	either truncate with warning, or
	•	switch to “per-file review” with caps
	•	Always include file paths and hunk headers.
	•	Include a tiny amount of surrounding context (contextLines).

7.2 Secret redaction

Before sending to LLM:
	•	Detect common secrets via regex heuristics:
	•	API keys, JWTs, private keys, AWS patterns, etc.
	•	Replace with [REDACTED].
	•	Also support path-based redaction (privacy.redactPaths).

7.3 Prompting strategy (high-level)

The LLM should get:
	•	A system instruction: “Be a strict code reviewer; produce JSON findings only…”
	•	A user payload including:
	•	repo language hints
	•	diff hunks
	•	constraints: max findings, severity policy, avoid style bikeshedding unless it matters
	•	request line-aware output referencing hunk lines

Model output must be validated against the schema. If invalid:
	•	Attempt one repair pass (same model).
	•	If still invalid: fail with exit code 4 (or emit best-effort with warning if --best-effort is set).

7.4 Multi-model compare mode

--compare provider:model,provider:model
	•	Run reviews independently.
	•	Merge findings by fuzzy match on (path + overlapping line region + title similarity).
	•	Show:
	•	consensus findings (appeared in >=2 models)
	•	unique findings per model
	•	Fail threshold applies to merged set (or configurable).

8) Provider Abstraction

8.1 Interface

A single Go interface (example):

type Reviewer interface {
  Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
  Name() string
}

ReviewRequest includes:
	•	prompt strings
	•	diff payload
	•	options (max tokens, temp, json schema hint)

8.2 Providers (v1)
	•	Anthropic
	•	OpenAI
	•	Google/Gemini

Implementation requirements:
	•	Timeouts and retries (bounded).
	•	Rate limit handling with backoff.
	•	Clear error classification for exit codes.

9) Project Structure (Go)

Recommended layout:

prism/
  cmd/prism/
    main.go
  internal/
    cli/              # cobra/urfavecli parsing, flags -> options
    gitctx/           # gather diffs, apply filters, compute ranges
    review/           # prompt assembly, response validation, merging
    providers/        # anthropic/openai/google implementations
    output/           # text/json/md/sarif writers
    config/           # load/merge config
    redact/           # secret detection/redaction
    schema/           # JSON schema + validator
  pkg/
    types/            # exported structs for JSON output (optional)
  testdata/
  README.md
  LICENSE

Dependency philosophy:
	•	Keep it lean. If you want dependency-free-ish, pick:
	•	stdlib + one CLI library (or even pure flag parsing)
	•	avoid giant frameworks
	•	JSON schema validation: either lightweight validator or custom strict decoding + required-field checks.

10) Testing Strategy

Minimum:
	•	Unit tests:
	•	diff parsing and path filtering
	•	config precedence
	•	redaction correctness
	•	output rendering (golden files)
	•	schema validation and “repair pass”
	•	Integration tests:
	•	create temp git repo, make commits, run review range, assert output
	•	Provider tests:
	•	mocked HTTP clients (no live calls in CI)

11) Security & Privacy
	•	Never log raw diff by default.
	•	--verbose still redacts secrets.
	•	Optional --no-redact flag for power users (prints warning).
	•	Cache stores only hashed keys + redacted payloads (if cache enabled).
	•	Document exactly what is sent to providers.

12) Performance
	•	Fast path: one diff, one request.
	•	For big diffs:
	•	chunk by file or by hunks with size caps
	•	parallelize LLM calls with bounded concurrency
	•	stable ordering in output even if parallel

13) Release & Distribution
	•	Single static binary builds for:
	•	linux/amd64, linux/arm64
	•	darwin/amd64, darwin/arm64
	•	windows/amd64
	•	GitHub Releases + checksum file.
	•	Optional Homebrew tap later.

14) MVP Milestones

v0.1 (local-only)
	•	review unstaged|staged|commit|range|snippet
	•	text + json output
	•	config file + env
	•	one provider (pick your favorite)
	•	fail-on exit codes

v0.2 (CI-friendly)
	•	SARIF output
	•	models doctor
	•	caching
	•	chunking for large diffs

v0.3 (power features)
	•	compare mode
	•	markdown output polished for PR comments
	•	rules file (lightweight “priorities”)

15) Optional: Rules Pack (not a linter, more like policy hints)

--rules rules.json can supply:
	•	severity overrides for categories
	•	“focus areas” (security, perf, concurrency, db)
	•	banned patterns (soft guidance)
	•	required checks (e.g., “new endpoints need auth middleware mention”)

Example:

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


⸻

Naming / positioning (because names matter)

You can name it something like:
	•	prism (multi-angle review)
	•	second-opinion
	•	diffwise
	•	reviewd
	•	lintellect (terrible, but funny)

Keep it distinct from MCP so users don’t assume it’s “Claude Desktop glue.”

