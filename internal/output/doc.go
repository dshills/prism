// Package output formats review reports for display or machine consumption.
//
// Four formats are supported:
//   - text     — human-readable terminal output (default)
//   - json     — full structured JSON report
//   - markdown — PR-comment-friendly with collapsible sections per finding
//   - sarif    — SARIF v2.1.0 for upload to GitHub Advanced Security and other CI tools
//
// Use [GetWriter] to obtain a [Writer] for a given format string, then call
// [Writer.Write] with an [io.Writer] and a [*review.Report].  [WriteToFile]
// and [WriteToStdout] are convenience helpers that handle destination selection.
package output
