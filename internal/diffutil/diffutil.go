package diffutil

import (
	"strconv"
	"strings"
)

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

// Line is one line of a diff section's hunks.
type Line struct {
	// Text is the line without its leading '+', '-' or ' ' marker.
	Text string
	// NewLine is the line's number in the new file; 0 for a removed line,
	// which has no position there.
	NewLine int
	// Removed is true for a '-' line.
	Removed bool
	// Hunk is the 1-based index of the hunk the line belongs to.
	Hunk int
}

// PostImageLines returns the hunk lines of one file's diff section, in order,
// with new-file line numbers taken from the "@@ -a,b +c,d @@" headers. The
// file header lines (diff --git, index, ---, +++) and the hunk headers
// themselves are not included, nor are "\ No newline at end of file" markers.
func PostImageLines(section string) []Line {
	var out []Line
	newLine := 0
	hunk := 0
	inHunk := false
	for _, raw := range strings.Split(section, "\n") {
		if strings.HasPrefix(raw, "@@") {
			newLine = hunkNewStart(raw)
			hunk++
			inHunk = true
			continue
		}
		if !inHunk || raw == "" {
			continue
		}
		switch raw[0] {
		case '+', ' ':
			out = append(out, Line{Text: raw[1:], NewLine: newLine, Hunk: hunk})
			newLine++
		case '-':
			out = append(out, Line{Text: raw[1:], Removed: true, Hunk: hunk})
		case '\\':
			// "\ No newline at end of file"
		default:
			// Anything else (a truncation marker, the next file's header in
			// malformed input) ends the hunk.
			inHunk = false
		}
	}
	return out
}

// hunkNewStart parses the new-file start line from a hunk header
// "@@ -a,b +c,d @@ ...". It returns 1 when the header cannot be parsed.
func hunkNewStart(header string) int {
	i := strings.Index(header, " +")
	if i < 0 {
		return 1
	}
	rest := header[i+2:]
	end := strings.IndexAny(rest, ", ")
	if end >= 0 {
		rest = rest[:end]
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 1
	}
	return n
}
