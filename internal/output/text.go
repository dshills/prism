package output

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dshills/prism/internal/review"
)

// TextWriter outputs a human-readable text report.
type TextWriter struct{}

func (t *TextWriter) Write(w io.Writer, report *review.Report) error {
	// Summary header
	total := report.Summary.Counts.High + report.Summary.Counts.Medium + report.Summary.Counts.Low
	fmt.Fprintf(w, "Prism Code Review — %s mode\n", report.Inputs.Mode)
	if report.Inputs.Range != "" {
		fmt.Fprintf(w, "Range: %s\n", report.Inputs.Range)
	}
	fmt.Fprintf(w, "Repository: %s (branch: %s)\n", report.Repo.Root, report.Repo.Branch)
	fmt.Fprintln(w, strings.Repeat("─", 60))
	fmt.Fprintf(w, "Findings: %d total", total)
	if total > 0 {
		fmt.Fprintf(w, " (%d high, %d medium, %d low)",
			report.Summary.Counts.High,
			report.Summary.Counts.Medium,
			report.Summary.Counts.Low,
		)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, strings.Repeat("─", 60))

	if total == 0 {
		fmt.Fprintln(w, "\nNo issues found. Looks good!")
		return nil
	}

	// Group by severity (high first), then by file
	grouped := groupBySeverity(report.Findings)
	for _, sev := range []review.Severity{review.SeverityHigh, review.SeverityMedium, review.SeverityLow} {
		findings := grouped[sev]
		if len(findings) == 0 {
			continue
		}

		label := strings.ToUpper(string(sev))
		fmt.Fprintf(w, "\n%s %s\n", severityIcon(sev), label)
		fmt.Fprintln(w, strings.Repeat("─", 40))

		// Sort by file path within severity
		sort.Slice(findings, func(i, j int) bool {
			pi := filePath(findings[i])
			pj := filePath(findings[j])
			return pi < pj
		})

		for _, f := range findings {
			loc := primaryLocation(f)
			fmt.Fprintf(w, "\n  %s:%d-%d  %s\n",
				loc.Path, loc.Lines.Start, loc.Lines.End, f.Title)
			fmt.Fprintf(w, "  Category: %s | Confidence: %.0f%%\n",
				f.Category, f.Confidence*100)

			// Message (indented, wrapped)
			for _, line := range wrapText(f.Message, 70) {
				fmt.Fprintf(w, "    %s\n", line)
			}

			// Suggestion
			if f.Suggestion != "" {
				fmt.Fprintln(w, "  Suggestion:")
				for _, line := range wrapText(f.Suggestion, 70) {
					fmt.Fprintf(w, "    %s\n", line)
				}
			}
		}
	}

	fmt.Fprintf(w, "\n%s\n", strings.Repeat("─", 60))
	fmt.Fprintf(w, "Completed in %dms (git: %dms, LLM: %dms)\n",
		report.Timing.TotalMs, report.Timing.GitMs, report.Timing.LLMMs)

	return nil
}

func groupBySeverity(findings []review.Finding) map[review.Severity][]review.Finding {
	m := make(map[review.Severity][]review.Finding)
	for _, f := range findings {
		m[f.Severity] = append(m[f.Severity], f)
	}
	return m
}

func primaryLocation(f review.Finding) review.Location {
	if len(f.Locations) > 0 {
		return f.Locations[0]
	}
	return review.Location{Path: "unknown"}
}

func filePath(f review.Finding) string {
	if len(f.Locations) > 0 {
		return f.Locations[0].Path
	}
	return ""
}

func severityIcon(s review.Severity) string {
	switch s {
	case review.SeverityHigh:
		return "[!!]"
	case review.SeverityMedium:
		return "[!]"
	case review.SeverityLow:
		return "[-]"
	default:
		return "[?]"
	}
}

func wrapText(text string, width int) []string {
	if len(text) <= width {
		return []string{text}
	}
	var lines []string
	words := strings.Fields(text)
	var current strings.Builder
	for _, word := range words {
		if current.Len()+len(word)+1 > width && current.Len() > 0 {
			lines = append(lines, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(word)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}
