# Prism

Local-first CLI that reviews code changes using LLM providers and emits findings with deterministic exit codes for CI gating.

Prism is **diff-centric** — it reviews only what changed, not your entire repo. It sends redacted diff hunks to one or more LLMs and returns structured findings with file paths, line numbers, and actionable suggestions.

## Features

- **5 review modes**: unstaged, staged, commit, range, and stdin snippet
- **4 LLM providers**: Anthropic, OpenAI, Google Gemini, and Ollama/LMStudio (local)
- **4 output formats**: text, JSON, markdown (PR-comment-ready), and SARIF v2.1.0
- **Multi-model compare mode**: run multiple models in parallel and see consensus vs. unique findings
- **Secret redaction**: API keys, JWTs, private keys, and database credentials are automatically replaced with `[REDACTED]` before being sent to any provider
- **Rules packs**: customize severity overrides, focus areas, and required checks
- **Deterministic exit codes**: designed for CI pipelines and git hooks
- **Pre-commit hook**: install/uninstall with `prism hook install`
- **GitHub PR integration**: post review findings as PR comments
- **Caching**: file-based cache with SHA-256 keys and configurable TTL
- **Large diff handling**: automatic chunking with bounded parallel LLM calls

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

### Multi-Model Compare

Run the same review across multiple models and see which findings they agree on:

```bash
prism review unstaged --compare anthropic:claude-sonnet-4-6,openai:gpt-5.2
```

Compare mode reports consensus findings (flagged by 2+ models) and unique findings per model.

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
| `prism config init` | Create default config file |
| `prism config set <key> <value>` | Set a config value |
| `prism config show` | Show effective configuration |
| `prism models list` | List known providers and models |
| `prism models doctor` | Validate provider credentials |
| `prism cache show` | Show cache statistics |
| `prism cache clear` | Clear cached results |
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
| `--max-findings` | Maximum number of findings | `50` |
| `--context-lines` | Context lines in diff | `3` |
| `--max-diff-bytes` | Maximum diff size in bytes | `500000` |
| `--paths` | Include file path globs (comma-separated) | `**/*` |
| `--exclude` | Exclude file path globs (comma-separated) | `vendor/**`, `**/*.gen.go`, `**/dist/**` |
| `--rules` | Rules file path | |
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
```

### Environment Variables

| Variable | Maps to |
|----------|---------|
| `PRISM_PROVIDER` | `provider` |
| `PRISM_MODEL` | `model` |
| `PRISM_FAIL_ON` | `failOn` |
| `PRISM_FORMAT` | `format` |
| `PRISM_MAX_FINDINGS` | `maxFindings` |
| `PRISM_CONTEXT_LINES` | `contextLines` |
| `ANTHROPIC_API_KEY` | Anthropic provider |
| `OPENAI_API_KEY` | OpenAI provider |
| `GEMINI_API_KEY` | Gemini provider |

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

## Providers

### Supported Providers

| Provider | Env Variable | Models |
|----------|-------------|--------|
| Anthropic | `ANTHROPIC_API_KEY` | claude-sonnet-4-6, claude-opus-4-6, claude-haiku-4-5 |
| OpenAI | `OPENAI_API_KEY` | gpt-5.3-codex, gpt-5.2-codex, gpt-5.2, gpt-4.1-mini, o3-mini |
| Gemini | `GEMINI_API_KEY` | gemini-3-flash-preview, gemini-3-pro-preview, gemini-2.5-flash, gemini-2.5-pro |
| Ollama | — | llama3.3, llama3.2, llama3.1, codellama, qwen2.5-coder |

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
- **Stable ID**: SHA-256 hash of path + title + start line, consistent across runs

## AI Development Workflows

Prism pairs well with AI coding assistants like Claude Code. Use Prism as a second-opinion reviewer on AI-generated code, with compare mode to get consensus across multiple LLMs. See [WORKFLOWS.md](WORKFLOWS.md) for detailed integration patterns.

## Dependencies

Prism has a single external dependency: [cobra](https://github.com/spf13/cobra) for CLI parsing. Everything else uses the Go standard library.

## License

MIT — see [LICENSE](LICENSE) for details.
