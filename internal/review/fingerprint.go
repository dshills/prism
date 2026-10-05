package review

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
)

// A finding's ID is its fingerprint: a hash of what stays the same when the
// same issue is reported again. That is the file, the category, the quoted
// evidence (normalised as verification matches it) and an anchor: the
// declaration the evidence sits in. The title is model-written prose that
// changes from run to run, and the start line moves whenever lines are added
// above it, so neither is part of the hash. The baseline and delta
// comparisons rely on this, as does deduplication.
//
// The anchor separates the same code in two places, such as `return err` in
// two functions of one file. It is the nearest line at or above the evidence
// that starts a declaration, as git finds for its hunk headers: a line
// beginning with an ASCII letter, '_' or '$' (for Go, a func or type line),
// cut to the 80 bytes git keeps. It does
// not depend on how much context the diff carries: the declaration is either
// in the hunk above the evidence, or it is the line git names in the hunk's
// header, which git takes from the whole file.
//
// Two findings on the same code in the same declaration and category share an
// ID: nothing that tells them apart (their titles, the order they are listed
// in) is stable, and numbering them would let one inherit the other's ID when
// it is reworded or fixed. DeduplicateFindings keeps both.
//
// A finding without evidence has nothing stable to anchor on, so it keeps the
// earlier scheme: path, title and start line.

// fingerprintBase is the text a finding's ID hashes. files is the reviewed
// text indexed by indexFiles; with nil, or when the evidence is not found in
// it, the base has no anchor.
func fingerprintBase(f Finding, files map[string][]indexedLine) string {
	path := findingPath(f)
	ev := evidenceLines(f.Evidence, true)
	if len(ev) == 0 {
		return fmt.Sprintf("%s:%s:%d", path, f.Title, findingStartLine(f))
	}
	category := strings.ToLower(strings.TrimSpace(string(f.Category)))
	base := "evidence\x00" + path + "\x00" + category + "\x00" + strings.Join(ev, "\n")
	if lines := files[path]; lines != nil {
		if i, _, ok := locateEvidenceRun(lines, f.Evidence, findingStartLine(f)); ok {
			base += "\x00" + enclosingDeclaration(lines, i)
		}
	}
	return base
}

// gitHeadingMax is how many bytes of a declaration line git keeps in a hunk
// header (xdiff's func_line buffer). A declaration found in the hunk is cut
// to the same length, so a long signature anchors the same either way.
const gitHeadingMax = 80

// enclosingDeclaration is the nearest new-file line at or above lines[i], in
// its hunk, that starts a declaration, or else the declaration git named in
// the hunk's header. It is "" when there is neither, as at the top of a file.
func enclosingDeclaration(lines []indexedLine, i int) string {
	hunk := lines[i].line.Hunk
	for j := i; j >= 0 && lines[j].line.Hunk == hunk; j-- {
		if text := lines[j].line.Text; !lines[j].line.Removed && startsDeclaration(text) {
			return normalizeCode(asGitHeading(text))
		}
	}
	return normalizeCode(lines[i].line.Heading)
}

// startsDeclaration reports whether a line starts a declaration by git's
// default rule for hunk headers: its first byte is an ASCII letter, '_' or
// '$'.
func startsDeclaration(text string) bool {
	if text == "" {
		return false
	}
	c := text[0]
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_' || c == '$'
}

// asGitHeading is a declaration line as git writes it in a hunk header: at
// most gitHeadingMax bytes, without trailing whitespace.
func asGitHeading(text string) string {
	if len(text) > gitHeadingMax {
		text = text[:gitHeadingMax]
	}
	return strings.TrimRightFunc(text, unicode.IsSpace)
}

func fingerprintID(base string) string {
	h := sha256.Sum256([]byte(base))
	return fmt.Sprintf("%x", h[:8])
}

// generateFindingID is a finding's ID without the anchor, which needs the
// reviewed text. parseFindings sets it; identifyFindings replaces it.
func generateFindingID(f Finding) string {
	return fingerprintID(fingerprintBase(f, nil))
}

// identifyFindings sets each finding's ID from reviewed, the text the model
// was given (one chunk, or the whole diff), so the anchor can be found.
func identifyFindings(findings []Finding, reviewed string) {
	files := indexFiles(reviewed)
	for i := range findings {
		findings[i].ID = fingerprintID(fingerprintBase(findings[i], files))
	}
}

// parseReviewedFindings parses a model response, or a cached one, for the
// reviewed text and gives each finding its full ID.
func parseReviewedFindings(content, reviewed string) ([]Finding, error) {
	findings, err := parseFindings(content)
	if err != nil {
		return nil, err
	}
	identifyFindings(findings, reviewed)
	return findings, nil
}
