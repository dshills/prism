// Package review contains the core types and engine for LLM-based code review.
//
// It defines the Finding, Report, and Severity types, assembles prompts from
// diff hunks, parses and validates JSON responses from LLM providers, and
// generates stable finding IDs as SHA-256 hashes of path, title, and line
// context.
//
// Large diffs (>100 KB) are automatically split into per-file chunks and
// reviewed in parallel with bounded concurrency; results are deduplicated and
// merged before being returned.
//
// Compare mode (compare.go) runs the same diff against multiple provider/model
// pairs concurrently and classifies findings as consensus or model-unique using
// fuzzy title matching.
//
// Rules packs (rules.go) allow callers to override finding severities, specify
// focus areas, and declare required checks that must appear in every review.
package review
