package output

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dshills/prism/internal/review"
)

// MarkdownWriter outputs a PR-comment-friendly markdown report.
type MarkdownWriter struct{}

func (m *MarkdownWriter) Write(w io.Writer, report *review.Report) error {
	total := report.Summary.Counts.High + report.Summary.Counts.Medium + report.Summary.Counts.Low

	// Heading
	fmt.Fprintf(w, "## Prism Code Review\n\n")

	// Summary table
	fmt.Fprintf(w, "| Severity | Count |\n")
	fmt.Fprintf(w, "|----------|-------|\n")
	fmt.Fprintf(w, "| High     | %d    |\n", report.Summary.Counts.High)
	fmt.Fprintf(w, "| Medium   | %d    |\n", report.Summary.Counts.Medium)
	fmt.Fprintf(w, "| Low      | %d    |\n", report.Summary.Counts.Low)
	fmt.Fprintf(w, "| **Total** | **%d** |\n\n", total)

	if total == 0 {
		fmt.Fprintln(w, "No issues found. :white_check_mark:")
		return nil
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

		fmt.Fprintf(w, "<details>\n<summary>%s %s (%d)</summary>\n\n", icon, label, len(findings))

		// Sort by file path within severity
		sort.Slice(findings, func(i, j int) bool {
			return mdFilePath(findings[i]) < mdFilePath(findings[j])
		})

		for _, f := range findings {
			loc := mdPrimaryLocation(f)
			fmt.Fprintf(w, "### %s\n\n", f.Title)
			fmt.Fprintf(w, "**`%s:%d-%d`** | %s | Confidence: %.0f%%\n\n",
				loc.Path, loc.Lines.Start, loc.Lines.End, f.Category, f.Confidence*100)
			fmt.Fprintf(w, "%s\n\n", f.Message)

			if f.Suggestion != "" {
				fmt.Fprintf(w, "**Suggestion:**\n\n")
				// Wrap suggestion in code fence if it looks like code
				if looksLikeCode(f.Suggestion) {
					lang := inferLang(loc.Path)
					fmt.Fprintf(w, "```%s\n%s\n```\n\n", lang, f.Suggestion)
				} else {
					fmt.Fprintf(w, "> %s\n\n", strings.ReplaceAll(f.Suggestion, "\n", "\n> "))
				}
			}

			fmt.Fprintf(w, "---\n\n")
		}

		fmt.Fprintf(w, "</details>\n\n")
	}

	// Timing footer
	fmt.Fprintf(w, "*Reviewed in %dms (git: %dms, LLM: %dms)*\n",
		report.Timing.TotalMs, report.Timing.GitMs, report.Timing.LLMMs)

	return nil
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

func mdFilePath(f review.Finding) string {
	if len(f.Locations) > 0 {
		return f.Locations[0].Path
	}
	return ""
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
