// Package pathglob matches repository paths, slash-separated and relative to
// the repository root, against globs: the include, exclude and redaction
// filters and the rule sets' paths. It uses package path, not path/filepath,
// so a pattern means the same on every platform.
package pathglob

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Match reports whether name matches pattern, segment by segment: "**"
// matches any number of directories, and other segments match as
// path.Match does. A pattern without a slash matches the file name, in any
// directory.
func Match(pattern, name string) bool {
	if !strings.Contains(pattern, "/") {
		ok, _ := path.Match(pattern, path.Base(name))
		return ok
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// MatchOrUnder is Match, and also true for a path inside a directory the
// pattern matches: "internal/review" and "vendor/" match every file beneath
// them, as a git pathspec does. A pattern with a slash is anchored at the
// repository root (unlike a .gitignore entry): "**/vendor/" matches a vendor
// directory at any depth.
func MatchOrUnder(pattern, name string) bool {
	pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "./"), "/")
	if pattern == "" {
		return false
	}
	return Match(pattern, name) || Match(pattern+"/**", name)
}

// matchSegments matches path segments against pattern segments, bottom up:
// m[i][j] is whether pat[i:] matches segs[j:]. Each pair is computed once,
// so a pattern with many "**" stays linear in pattern times path length.
func matchSegments(pat, segs []string) bool {
	m := make([][]bool, len(pat)+1)
	for i := range m {
		m[i] = make([]bool, len(segs)+1)
	}
	m[len(pat)][len(segs)] = true
	for i := len(pat) - 1; i >= 0; i-- {
		for j := len(segs); j >= 0; j-- {
			switch {
			case pat[i] == "**":
				// Match no segment here, or this one and keep going.
				m[i][j] = m[i+1][j] || (j < len(segs) && m[i][j+1])
			case j < len(segs):
				ok, _ := path.Match(pat[i], segs[j])
				m[i][j] = ok && m[i+1][j+1]
			}
		}
	}
	return m[0][0]
}

// Check rejects a pattern path.Match cannot parse, and one with an empty
// segment ("/x", "a//b"), which no repository path has, so it could never
// match.
func Check(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return errors.New("empty path glob")
	}
	for seg := range strings.SplitSeq(strings.TrimSuffix(pattern, "/"), "/") {
		if seg == "" {
			return fmt.Errorf("bad path glob %q: empty path segment", pattern)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("bad path glob %q: %w", pattern, err)
		}
	}
	return nil
}
