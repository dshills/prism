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
			// Git appends a tab to a path that has spaces in it.
			return strings.TrimSuffix(rest, "\t")
		}
	}
	return ""
}

// SectionMeta is a section's metadata: its header lines, before the first
// hunk. Markers such as "deleted file mode" or "--- /dev/null" are looked for
// here only, since a hunk's content lines can spell them too (a removed line
// "-- /dev/null" is "--- /dev/null" in the diff).
func SectionMeta(section string) string {
	meta, _, _ := strings.Cut(section, "\n@@")
	return meta
}

// IsNewFile reports whether a section adds a file, so its hunk holds the
// whole file: its old side is /dev/null (a new file, a snippet, or a
// codebase review's section).
func IsNewFile(section string) bool {
	return strings.Contains(SectionMeta(section)+"\n", "\n--- /dev/null\n")
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
	// Heading is the text git puts after its hunk's header,
	// "@@ -a,b +c,d @@ heading": the nearest line above the hunk that starts
	// a declaration (for Go, a func or type line), or "" when there is none.
	Heading string
}

// PostImageLines returns the hunk lines of one file's diff section, in order,
// with new-file line numbers taken from the "@@ -a,b +c,d @@" headers. The
// file header lines (diff --git, index, ---, +++) and the hunk headers
// themselves are not included, nor are "\ No newline at end of file" markers.
func PostImageLines(section string) []Line {
	var out []Line
	newLine := 0
	hunk := 0
	heading := ""
	inHunk := false
	for _, raw := range strings.Split(section, "\n") {
		if strings.HasPrefix(raw, "@@") {
			newLine = hunkNewStart(raw)
			heading = hunkHeading(raw)
			hunk++
			inHunk = true
			continue
		}
		if !inHunk || raw == "" {
			continue
		}
		switch raw[0] {
		case '+', ' ':
			out = append(out, Line{Text: raw[1:], NewLine: newLine, Hunk: hunk, Heading: heading})
			newLine++
		case '-':
			out = append(out, Line{Text: raw[1:], Removed: true, Hunk: hunk, Heading: heading})
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

// hunkHeading returns the text after a hunk header's closing "@@", trimmed.
func hunkHeading(header string) string {
	rest, ok := strings.CutPrefix(header, "@@")
	if !ok {
		return ""
	}
	_, heading, ok := strings.Cut(rest, "@@")
	if !ok {
		return ""
	}
	return strings.TrimSpace(heading)
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
