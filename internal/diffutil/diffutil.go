package diffutil

import "strings"

// SplitSections splits a unified diff into per-file sections.
// Each section starts with a "diff --git" line. Whitespace-only
// trailing content is dropped.
func SplitSections(diff string) []string {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	var sections []string
	var current strings.Builder
	// TrimSuffix drops the trailing newline so SplitSeq does not yield a
	// spurious empty final element, preserving the original behaviour.
	for line := range strings.SplitSeq(strings.TrimSuffix(diff, "\n"), "\n") {
		if strings.HasPrefix(line, "diff --git") && current.Len() > 0 {
			sections = append(sections, current.String())
			current.Reset()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	if current.Len() > 0 {
		s := current.String()
		if strings.TrimSpace(s) != "" {
			sections = append(sections, s)
		}
	}
	return sections
}

// PathFromSection extracts the file path from a diff section by reading the
// "+++ b/<path>" header line. Returns empty string if not found.
func PathFromSection(section string) string {
	for line := range strings.SplitSeq(section, "\n") {
		if rest, ok := strings.CutPrefix(line, "+++ b/"); ok {
			return rest
		}
	}
	return ""
}
