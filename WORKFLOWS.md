# Using Prism with Claude Code

The core idea: **Prism gives you a second LLM opinion on AI-generated code.** When Claude Code writes code, it's optimizing for completing your request. Prism reviews that same code with a reviewer mindset — looking for bugs, security issues, and things the author (human or AI) missed.

## The basic loop

```
You give instructions -> Claude Code writes code -> Prism reviews it -> Claude Code fixes findings
```

In practice:

```bash
# 1. Claude Code writes your feature (staged but not committed)
# 2. Review what it wrote
prism review staged

# 3. If findings exist, paste them back to Claude Code:
#    "Fix these prism findings: [paste output]"

# 4. Commit when clean
```

## The compare mode advantage

This is where Prism really shines with Claude Code. If Claude Code (Anthropic) wrote the code, review it with a **different model** to avoid blind spots:

```bash
# Get OpenAI and Gemini to review code that Claude wrote
prism review staged --compare openai:gpt-5.2,gemini:gemini-3-flash-preview
```

Or use all three cloud providers for maximum coverage:

```bash
prism review staged --compare anthropic:claude-sonnet-4-6,openai:gpt-5.2,gemini:gemini-3-flash-preview
```

Consensus findings (flagged by 2+ models) are almost always real issues.

## Pre-commit hook (automatic)

The most seamless integration — every commit gets reviewed, whether you or Claude Code made the changes:

```bash
prism hook install
```

This runs `prism review staged --fail-on high` before each commit. If Prism finds high-severity issues, the commit is blocked. Tell Claude Code to fix them and try again.

## Add Prism to your CLAUDE.md

This is the key integration point. Add something like this to your project's `CLAUDE.md`:

```markdown
## Code Quality

After writing or modifying code, run `prism review staged` before committing.
If findings are severity high, fix them before proceeding.
For security-sensitive changes, use compare mode:
  prism review staged --compare openai:gpt-5.2,gemini:gemini-3-flash-preview
```

This way Claude Code knows to self-check its work using Prism as part of its workflow.

## CI gating for PRs

When Claude Code creates a PR, gate it with Prism in your CI pipeline:

```yaml
# GitHub Actions example
- name: Prism review
  run: prism review range origin/main..HEAD --format sarif --out prism.sarif --fail-on high
- uses: github/codeql-action/upload-sarif@v3
  with:
    sarif_file: prism.sarif
```

The SARIF output integrates with GitHub's code scanning, so findings appear as inline annotations on the PR.

## Recommended workflow by task type

| Task | Approach |
|------|----------|
| Quick bug fix | `prism review staged` before commit |
| New feature | Compare mode with 2-3 providers |
| Security-sensitive code | Compare mode + `--fail-on medium` + rules file with `"focus": ["security"]` |
| Large refactor | Let the pre-commit hook catch issues incrementally |
| PR ready for merge | `prism review range origin/main..HEAD --format markdown` and paste into PR description |

## Why this works

- **Different reviewer than author**: The LLM reviewing isn't the same one that wrote the code, so it catches different things
- **Structured output**: Prism returns machine-parseable findings, not prose — easy for Claude Code to act on
- **Fast feedback**: A review takes 2-20 seconds depending on diff size and provider
- **Cheap**: Using fast models (gemini-3-flash-preview, gpt-4.1-mini) keeps costs negligible
- **No context window pollution**: Prism runs as a separate process, so it doesn't consume Claude Code's conversation context
