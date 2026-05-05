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
	lines := strings.Split(diff, "\n")
	// strings.Split on a newline-terminated string produces a trailing empty
	// element; drop it so we don't emit a spurious extra newline per section.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var current strings.Builder
	for _, line := range lines {
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
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			return strings.TrimPrefix(line, "+++ b/")
		}
	}
	return ""
}
