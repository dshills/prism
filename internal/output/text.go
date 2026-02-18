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
	ew := &errWriter{w: w}

	// Summary header
	total := report.Summary.Counts.High + report.Summary.Counts.Medium + report.Summary.Counts.Low
	ew.printf("Prism Code Review — %s mode\n", report.Inputs.Mode)
	if report.Inputs.Range != "" {
		ew.printf("Range: %s\n", report.Inputs.Range)
	}
	ew.printf("Repository: %s (branch: %s)\n", report.Repo.Root, report.Repo.Branch)
	ew.println(strings.Repeat("─", 60))
	ew.printf("Findings: %d total", total)
	if total > 0 {
		ew.printf(" (%d high, %d medium, %d low)",
			report.Summary.Counts.High,
			report.Summary.Counts.Medium,
			report.Summary.Counts.Low,
		)
	}
	ew.println("")
	ew.println(strings.Repeat("─", 60))

	if total == 0 {
		ew.println("\nNo issues found. Looks good!")
		return ew.err
	}

	// Group by severity (high first), then by file
	grouped := groupBySeverity(report.Findings)
	for _, sev := range []review.Severity{review.SeverityHigh, review.SeverityMedium, review.SeverityLow} {
		findings := grouped[sev]
		if len(findings) == 0 {
			continue
		}

		label := strings.ToUpper(string(sev))
		ew.printf("\n%s %s\n", severityIcon(sev), label)
		ew.println(strings.Repeat("─", 40))

		// Sort by file path within severity
		sort.Slice(findings, func(i, j int) bool {
			pi := filePath(findings[i])
			pj := filePath(findings[j])
			return pi < pj
		})

		for _, f := range findings {
			loc := primaryLocation(f)
			ew.printf("\n  %s:%d-%d  %s\n",
				loc.Path, loc.Lines.Start, loc.Lines.End, f.Title)
			ew.printf("  Category: %s | Confidence: %.0f%%\n",
				f.Category, f.Confidence*100)

			// Message (indented, wrapped)
			for _, line := range wrapText(f.Message, 70) {
				ew.printf("    %s\n", line)
			}

			// Suggestion
			if f.Suggestion != "" {
				ew.println("  Suggestion:")
				for _, line := range wrapText(f.Suggestion, 70) {
					ew.printf("    %s\n", line)
				}
			}
		}
	}

	ew.printf("\n%s\n", strings.Repeat("─", 60))
	ew.printf("Completed in %dms (git: %dms, LLM: %dms)\n",
		report.Timing.TotalMs, report.Timing.GitMs, report.Timing.LLMMs)

	return ew.err
}

// errWriter wraps an io.Writer and captures the first error.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, args ...interface{}) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}

func (ew *errWriter) println(s string) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintln(ew.w, s)
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
