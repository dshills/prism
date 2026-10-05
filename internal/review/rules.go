package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Rules represents a rules pack loaded from --rules.
type Rules struct {
	Focus             []string          `json:"focus,omitempty"`
	SeverityOverrides map[string]string `json:"severityOverrides,omitempty"`
	Required          []RequiredCheck   `json:"required,omitempty"`
	// Paths limits a rule set in Sets to the files matching one of these
	// globs ("internal/auth/**", "**/*_test.go"; a pattern without a slash
	// matches the file name in any directory). A set without paths applies
	// to every file, as the top-level rules do.
	Paths Globs `json:"paths,omitempty"`
	// Sets are rule sets for parts of the repository, each applied on top
	// of the top-level rules where its paths match, later sets winning.
	Sets []Rules `json:"sets,omitempty"`
}

// Globs is a list of path globs, written in JSON as a list or one string.
type Globs []string

func (g *Globs) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*g = Globs{one}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("paths must be a glob or a list of globs: %w", err)
	}
	*g = list
	return nil
}

// RequiredCheck is a policy check that should always be enforced.
type RequiredCheck struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// LoadRules loads a rules file from disk. Returns nil Rules and nil error if path is empty.
func LoadRules(path string) (*Rules, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading rules file: %w", err)
	}
	var rules Rules
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		// A list of rule sets, with nothing at the top level.
		err = json.Unmarshal(data, &rules.Sets)
	} else {
		err = json.Unmarshal(data, &rules)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing rules file: %w", err)
	}
	if len(rules.Paths) > 0 {
		return nil, errors.New("rules file: paths belongs on a rule set in sets, not at the top level")
	}
	for i, set := range rules.Sets {
		if len(set.Sets) > 0 {
			return nil, fmt.Errorf("rules file: set %d has sets of its own; rule sets do not nest", i)
		}
		for _, p := range set.Paths {
			if err := checkGlob(p); err != nil {
				return nil, fmt.Errorf("rules file: set %d: %w", i, err)
			}
		}
	}
	return &rules, nil
}

// BuildRulesPromptSection returns additional prompt instructions derived from
// the top-level rules.
func BuildRulesPromptSection(rules *Rules) string {
	if rules == nil {
		return ""
	}
	var b strings.Builder
	writeRules(&b, rules)
	return b.String()
}

// maxRuleFiles is how many matching files a scoped rule set names.
const maxRuleFiles = 20

// rulesSectionFor is the rules section of a prompt for files: the top-level
// rules, then each rule set that matches one of the files. A set that
// matches only some of them says which, so the model applies it to those
// alone.
func rulesSectionFor(rules *Rules, files []string) string {
	if rules == nil {
		return ""
	}
	var b strings.Builder
	writeRules(&b, rules)
	b.WriteString(setsSectionFor(rules, files))
	return b.String()
}

// setsSectionFor is the rule sets that match files, without the top-level
// rules, which the system prompt carries (SystemPromptWithRules).
func setsSectionFor(rules *Rules, files []string) string {
	if rules == nil {
		return ""
	}
	var b strings.Builder
	scoped := false // the last heading limited the rules to some files
	for _, set := range rules.Sets {
		var matched []string
		for _, f := range files {
			if set.appliesTo(f) {
				matched = append(matched, f)
			}
		}
		if len(matched) == 0 {
			continue
		}
		var inner strings.Builder
		writeRules(&inner, &set)
		if inner.Len() == 0 {
			continue
		}
		if len(set.Paths) == 0 || len(matched) == len(files) {
			if scoped { // end the scoped block before it
				b.WriteString("\nThe rules below apply to every file in this review:\n")
				scoped = false
			}
			b.WriteString(inner.String())
			continue
		}
		scoped = true
		shown := matched
		if len(shown) > maxRuleFiles {
			shown = shown[:maxRuleFiles]
		}
		list := strings.Join(shown, ", ")
		if extra := len(matched) - len(shown); extra > 0 {
			list += fmt.Sprintf(" and %d more matching %s", extra, strings.Join(set.Paths, ", "))
		}
		fmt.Fprintf(&b, "\nThe rules below apply only to findings in %s:\n", list)
		b.WriteString(inner.String())
	}
	return b.String()
}

// appliesTo reports whether a rule set applies to path.
func (r *Rules) appliesTo(path string) bool {
	if len(r.Paths) == 0 {
		return true
	}
	for _, p := range r.Paths {
		if matchGlob(p, path) {
			return true
		}
	}
	return false
}

// writeRules writes rules' own focus, severity policy and required checks,
// not its sets.
func writeRules(b *strings.Builder, rules *Rules) {

	if len(rules.Focus) > 0 {
		fmt.Fprintf(b, "\nFocus areas: %s. Prioritize findings in these areas.\n",
			strings.Join(rules.Focus, ", "))
	}

	if len(rules.SeverityOverrides) > 0 {
		// Sorted, so the same rules always give the same prompt: it is part
		// of the cache key (promptFingerprint).
		b.WriteString("\nSeverity policy:\n")
		for _, cat := range slices.Sorted(maps.Keys(rules.SeverityOverrides)) {
			fmt.Fprintf(b, "- %s findings should be rated as %s severity.\n", cat, rules.SeverityOverrides[cat])
		}
	}

	if len(rules.Required) > 0 {
		b.WriteString("\nRequired checks (always evaluate these):\n")
		for _, req := range rules.Required {
			fmt.Fprintf(b, "- [%s] %s\n", req.ID, req.Text)
		}
	}
}

// ApplySeverityOverrides post-processes findings to enforce severity
// overrides from rules: the top-level ones, then those of each rule set that
// applies to the finding's file, later sets winning.
func ApplySeverityOverrides(findings []Finding, rules *Rules) []Finding {
	if rules == nil {
		return findings
	}
	for i := range findings {
		cat := string(findings[i].Category)
		if override, ok := rules.SeverityOverrides[cat]; ok {
			// The ID is kept: it does not depend on severity.
			findings[i].Severity = Severity(override)
		}
		path := findingPath(findings[i])
		for _, set := range rules.Sets {
			if override, ok := set.SeverityOverrides[cat]; ok && set.appliesTo(path) {
				findings[i].Severity = Severity(override)
			}
		}
	}
	return findings
}

// matchGlob reports whether path matches pattern, segment by segment: "**"
// matches any number of directories, and other segments match as
// filepath.Match does. A pattern without a slash matches the file name, in
// any directory.
func matchGlob(pattern, path string) bool {
	if !strings.Contains(pattern, "/") {
		ok, _ := filepath.Match(pattern, filepath.Base(path))
		return ok
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
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
				ok, _ := filepath.Match(pat[i], segs[j])
				m[i][j] = ok && m[i+1][j+1]
			}
		}
	}
	return m[0][0]
}

// checkGlob rejects a pattern filepath.Match cannot parse.
func checkGlob(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return errors.New("empty path glob")
	}
	for _, seg := range strings.Split(pattern, "/") {
		if _, err := filepath.Match(seg, ""); err != nil {
			return fmt.Errorf("bad path glob %q: %w", pattern, err)
		}
	}
	return nil
}
