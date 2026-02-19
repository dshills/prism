// Prism is a local-first CLI for reviewing code changes with LLM providers.
//
// It reviews unstaged, staged, commit, range, snippet, and full-codebase diffs,
// emitting structured findings with deterministic exit codes suitable for CI
// gating and git hooks.
//
// Usage:
//
//	prism review unstaged             # review working tree changes
//	prism review staged               # review staged changes
//	prism review commit <sha>         # review a specific commit
//	prism review range origin/main..HEAD  # review a revision range
//	prism review snippet              # review code from stdin
//	prism review codebase             # review all tracked files
//
// See https://github.com/dshills/prism for full documentation.
package main
