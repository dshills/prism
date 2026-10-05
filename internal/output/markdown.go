package output

import (
	"io"
	"strings"

	"github.com/dshills/prism/internal/review"
)

// MarkdownWriter outputs a PR-comment-friendly markdown report.
type MarkdownWriter struct{}

func (m *MarkdownWriter) Write(w io.Writer, report *review.Report) error {
	ew := &errWriter{w: w}
	total := report.Summary.Counts.High + report.Summary.Counts.Medium + report.Summary.Counts.Low

	// Heading — surface AI provenance so PR readers can distinguish these
	// findings from deterministic-analyzer output at a glance.
	ew.printf("## Prism Code Review%s\n\n", provenanceSuffix(report.Provenance))

	// Summary table
	ew.printf("| Severity | Count |\n")
	ew.printf("|----------|-------|\n")
	ew.printf("| High     | %d    |\n", report.Summary.Counts.High)
	ew.printf("| Medium   | %d    |\n", report.Summary.Counts.Medium)
	ew.printf("| Low      | %d    |\n", report.Summary.Counts.Low)
	ew.printf("| **Total** | **%d** |\n\n", total)

	// FR-2: what was reviewed, always.
	if line := report.Coverage.Describe(report.Timing.LLMMs); line != "" {
		ew.printf("_%s_\n\n", line)
	}
	if line := report.Coverage.ExcludedLine(); line != "" {
		ew.printf("_%s_\n\n", line)
	}
	if line := report.Coverage.IncompleteLine(); line != "" {
		ew.printf("**%s**\n\n", line)
	}

	if total == 0 {
		if report.Coverage.Incomplete() {
			ew.println("No issues found in what was reviewed. :warning:")
		} else {
			ew.println("No issues found. :white_check_mark:")
		}
		writeMarkdownDiscarded(ew, report)
		writeMarkdownDelta(ew, report)
		writeMarkdownSuppressed(ew, report)
		return ew.err
	}

	// Collapsible sections by severity
	grouped := groupFindingsBySeverity(report.Findings)
	for _, sev := range []review.Severity{review.SeverityHigh, review.SeverityMedium, review.SeverityLow} {
		findings := grouped[sev]
		if len(findings) == 0 {
			continue
		}

		icon := mdSeverityIcon(sev)
		label := strings.ToUpper(string(sev))

		ew.printf("<details>\n<summary>%s %s (%d)</summary>\n\n", icon, label, len(findings))

		for _, f := range findings {
			loc := mdPrimaryLocation(f)
			ew.printf("### %s%s\n\n", f.Title, deltaMark(f))
			if loc.Commit != "" {
				ew.printf("**`%s:%d-%d`** | %s | Confidence: %.0f%% | Commit: `%s` | ID: `%s`\n\n",
					loc.Path, loc.Lines.Start, loc.Lines.End, f.Category, f.Confidence*100, loc.Commit, f.ID)
			} else {
				ew.printf("**`%s:%d-%d`** | %s | Confidence: %.0f%% | ID: `%s`\n\n",
					loc.Path, loc.Lines.Start, loc.Lines.End, f.Category, f.Confidence*100, f.ID)
			}
			ew.printf("%s\n\n", f.Message)

			if f.Suggestion != "" {
				ew.printf("**Suggestion:**\n\n")
				// Wrap suggestion in code fence if it looks like code
				if looksLikeCode(f.Suggestion) {
					lang := inferLang(loc.Path)
					ew.printf("```%s\n%s\n```\n\n", lang, f.Suggestion)
				} else {
					ew.printf("> %s\n\n", strings.ReplaceAll(f.Suggestion, "\n", "\n> "))
				}
			}

			if f.Fix != nil {
				ew.printf("**Fix** (exact replacement):\n\n```diff\n%s\n%s\n```\n\n",
					prefixLines(f.Fix.Before, "-"), prefixLines(f.Fix.After, "+"))
			}

			ew.printf("---\n\n")
		}

		ew.printf("</details>\n\n")
	}

	// Timing footer
	ew.printf("*Reviewed in %dms (git: %dms, LLM: %dms)*\n",
		report.Timing.TotalMs, report.Timing.GitMs, report.Timing.LLMMs)

	writeMarkdownDiscarded(ew, report)
	writeMarkdownDelta(ew, report)
	writeMarkdownSuppressed(ew, report)
	return ew.err
}

// provenanceSuffix formats provenance for the markdown heading.
// Returns an empty string when no provenance is recorded (legacy reports,
// empty diffs) so the heading stays clean.
func provenanceSuffix(provenance []review.Provenance) string {
	if len(provenance) == 0 {
		return ""
	}
	parts := make([]string, 0, len(provenance))
	for _, p := range provenance {
		switch {
		case p.Provider != "" && p.Model != "":
			parts = append(parts, p.Provider+"/"+p.Model)
		case p.Model != "":
			parts = append(parts, p.Model)
		case p.Provider != "":
			parts = append(parts, p.Provider)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (AI-generated: " + strings.Join(parts, ", ") + ")"
}

func groupFindingsBySeverity(findings []review.Finding) map[review.Severity][]review.Finding {
	m := make(map[review.Severity][]review.Finding)
	for _, f := range findings {
		m[f.Severity] = append(m[f.Severity], f)
	}
	return m
}

func mdPrimaryLocation(f review.Finding) review.Location {
	if len(f.Locations) > 0 {
		return f.Locations[0]
	}
	return review.Location{Path: "unknown"}
}

func mdSeverityIcon(s review.Severity) string {
	switch s {
	case review.SeverityHigh:
		return ":red_circle:"
	case review.SeverityMedium:
		return ":orange_circle:"
	case review.SeverityLow:
		return ":yellow_circle:"
	default:
		return ":white_circle:"
	}
}

func looksLikeCode(s string) bool {
	codeIndicators := []string{
		"func ", "if ", "for ", "return ", "var ", "const ",
		"def ", "class ", "import ", "from ",
		"{", "}", "=>", "->", ":=", "==",
		"()", "[];",
	}
	for _, indicator := range codeIndicators {
		if strings.Contains(s, indicator) {
			return true
		}
	}
	return false
}

func inferLang(path string) string {
	langMap := map[string]string{
		".go":   "go",
		".py":   "python",
		".js":   "javascript",
		".ts":   "typescript",
		".tsx":  "tsx",
		".jsx":  "jsx",
		".rs":   "rust",
		".java": "java",
		".rb":   "ruby",
		".cpp":  "cpp",
		".c":    "c",
		".cs":   "csharp",
		".php":  "php",
		".sh":   "bash",
		".sql":  "sql",
		".yaml": "yaml",
		".yml":  "yaml",
		".json": "json",
		".tf":   "hcl",
	}
	for ext, lang := range langMap {
		if strings.HasSuffix(path, ext) {
			return lang
		}
	}
	return ""
}

// errWriterMd is provided by text.go's errWriter via same package.
// The errWriter type is shared across the output package since
// both text.go and markdown.go are in package output.
// No need to redeclare it here - it's defined in text.go.

// prefixLines puts prefix before every line of s, as a diff does.
func prefixLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// writeMarkdownDelta states the comparison with an earlier review and lists
// what it resolved.
func writeMarkdownDelta(ew *errWriter, report *review.Report) {
	d := report.Delta
	if d == nil {
		return
	}
	ew.printf("\n%s\n", d.DeltaLine())
	if len(d.Resolved) == 0 {
		return
	}
	ew.printf("\n<details><summary>Resolved %d finding(s)</summary>\n\n", len(d.Resolved))
	for _, f := range d.Resolved {
		loc := mdPrimaryLocation(f)
		ew.printf("- `%s:%d` %s\n", loc.Path, loc.Lines.Start, f.Title)
	}
	ew.println("\n</details>")
}

// writeMarkdownSuppressed lists findings accepted by the baseline or an
// inline prism:ignore.
func writeMarkdownSuppressed(ew *errWriter, report *review.Report) {
	if len(report.Suppressed) == 0 {
		return
	}
	ew.printf("\n<details><summary>Suppressed %d accepted finding(s)</summary>\n\n", len(report.Suppressed))
	for _, s := range report.Suppressed {
		loc := mdPrimaryLocation(s.Finding)
		ew.printf("- `%s:%d` %s — %s%s\n", loc.Path, loc.Lines.Start, s.Finding.Title, suppressionSource(s), suppressionReason(s))
	}
	ew.println("\n</details>")
}

// writeMarkdownDiscarded lists findings that failed verification (FR-7).
func writeMarkdownDiscarded(ew *errWriter, report *review.Report) {
	if len(report.Discarded) == 0 {
		return
	}
	ew.printf("\n<details><summary>Discarded %d finding(s) that failed verification</summary>\n\n", len(report.Discarded))
	for _, d := range report.Discarded {
		loc := mdPrimaryLocation(d.Finding)
		ew.printf("- `%s:%d` %s — %s\n", loc.Path, loc.Lines.Start, d.Finding.Title, d.Reason)
	}
	ew.println("\n</details>")
}
