package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dshills/prism/internal/config"
)

func TestMatchGlob(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"internal/auth/**", "internal/auth/token.go", true},
		{"internal/auth/**", "internal/auth/oauth/flow.go", true},
		{"internal/auth/**", "internal/authz/x.go", false},
		{"**/*_test.go", "a_test.go", true},
		{"**/*_test.go", "internal/review/x_test.go", true},
		{"*_test.go", "deep/dir/y_test.go", true}, // no slash: the file name
		{"*_test.go", "deep/dir/y.go", false},
		{"cmd/*/main.go", "cmd/prism/main.go", true},
		{"cmd/*/main.go", "cmd/a/b/main.go", false},
		{"docs/**/*.md", "docs/x.md", true},
		{"**", "any/path/at/all.go", true},
		{"a/**/b/**/c.go", "a/x/b/y/z/c.go", true},
		{"a/**/b/**/c.go", "a/x/y/c.go", false},
	} {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func rulesFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRules_Sets(t *testing.T) {
	r, err := LoadRules(rulesFile(t, `{
		"focus": ["bugs"],
		"sets": [
			{"paths": "internal/auth/**", "focus": ["security"], "severityOverrides": {"security": "high"}},
			{"paths": ["**/*_test.go"], "severityOverrides": {"style": "low"}}
		]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sets) != 2 || r.Sets[0].Paths[0] != "internal/auth/**" || r.Sets[1].Paths[0] != "**/*_test.go" {
		t.Errorf("sets = %+v", r.Sets)
	}

	list, err := LoadRules(rulesFile(t, `[{"paths": "**/*.sql", "focus": ["performance"]}]`))
	if err != nil || len(list.Sets) != 1 || list.Focus != nil {
		t.Errorf("list form = %+v, %v", list, err)
	}

	for name, body := range map[string]string{
		"top-level paths": `{"paths": "x/**"}`,
		"nested sets":     `{"sets": [{"paths": "a/**", "sets": [{"focus": ["x"]}]}]}`,
		"bad glob":        `{"sets": [{"paths": "a/[", "focus": ["x"]}]}`,
		"paths not globs": `{"sets": [{"paths": 3}]}`,
	} {
		if _, err := LoadRules(rulesFile(t, body)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A chunk's prompt has the top-level rules and the sets matching its files;
// a set matching only some of them names those files.
func TestRulesSectionFor(t *testing.T) {
	r := &Rules{
		Focus: []string{"bugs"},
		Sets: []Rules{
			{Paths: Globs{"internal/auth/**"}, Focus: []string{"security"}},
			{Paths: Globs{"*_test.go"}, SeverityOverrides: map[string]string{"style": "low"}},
			{Required: []RequiredCheck{{ID: "R1", Text: "everywhere"}}},
		},
	}
	auth := rulesSectionFor(r, []string{"internal/auth/token.go"})
	if !strings.Contains(auth, "Focus areas: bugs") || !strings.Contains(auth, "Focus areas: security") || strings.Contains(auth, "style findings") {
		t.Errorf("auth chunk:\n%s", auth)
	}
	if strings.Contains(auth, "apply only to") {
		t.Errorf("a set matching every file of the chunk was scoped:\n%s", auth)
	}
	if !strings.Contains(auth, "[R1] everywhere") {
		t.Errorf("a set without paths was left out:\n%s", auth)
	}
	mixed := rulesSectionFor(r, []string{"internal/auth/token.go", "internal/cli/cli.go", "internal/cli/cli_test.go"})
	if !strings.Contains(mixed, "apply only to findings in internal/auth/token.go:\n\nFocus areas: security") ||
		!strings.Contains(mixed, "apply only to findings in internal/cli/cli_test.go:") {
		t.Errorf("mixed chunk:\n%s", mixed)
	}
	if !strings.Contains(mixed, "low severity.\n\nThe rules below apply to every file in this review:\n\nRequired checks") {
		t.Errorf("an unscoped set after a scoped one is not marked as for every file:\n%s", mixed)
	}
	if other := rulesSectionFor(r, []string{"README.md"}); strings.Contains(other, "security") || strings.Contains(other, "style") {
		t.Errorf("unmatched sets in prompt:\n%s", other)
	}
}

// Overrides apply by the finding's own file, later sets winning.
func TestApplySeverityOverrides_Sets(t *testing.T) {
	r := &Rules{
		SeverityOverrides: map[string]string{"security": "medium"},
		Sets: []Rules{
			{Paths: Globs{"internal/auth/**"}, SeverityOverrides: map[string]string{"security": "high"}},
			{Paths: Globs{"internal/auth/testdata/**"}, SeverityOverrides: map[string]string{"security": "low"}},
		},
	}
	at := func(path string) Finding {
		return Finding{Category: CategorySecurity, Severity: SeverityLow, Locations: []Location{{Path: path}}}
	}
	got := ApplySeverityOverrides([]Finding{at("cmd/main.go"), at("internal/auth/a.go"), at("internal/auth/testdata/b.go")}, r)
	want := []Severity{SeverityMedium, SeverityHigh, SeverityLow}
	for i, f := range got {
		if f.Severity != want[i] {
			t.Errorf("finding %d (%s): severity %s, want %s", i, findingPath(f), f.Severity, want[i])
		}
	}
}

// Rule sets are in the prompt fingerprint even though the probe prompt
// matches none of them.
func TestRuleSets_InFingerprint(t *testing.T) {
	cfg := config.Default()
	a := promptFingerprint(defaultPromptBuilder, cfg, &Rules{Sets: []Rules{{Paths: Globs{"x/**"}, Focus: []string{"security"}}}})
	b := promptFingerprint(defaultPromptBuilder, cfg, &Rules{Sets: []Rules{{Paths: Globs{"x/**"}, Focus: []string{"performance"}}}})
	if a == b {
		t.Error("changing a rule set left the fingerprint unchanged")
	}
	if promptFingerprint(defaultPromptBuilder, cfg, &Rules{Focus: []string{"bugs"}}) != promptFingerprint(defaultPromptBuilder, cfg, &Rules{Focus: []string{"bugs"}, Sets: nil}) {
		t.Error("fingerprint is not stable")
	}
}

// Many "**" against a deep path that does not match still returns at once.
func TestMatchGlob_ManyGlobstars(t *testing.T) {
	pattern := strings.Repeat("**/", 30) + "missing.go"
	path := strings.Repeat("d/", 60) + "found.go"
	done := make(chan bool, 1)
	go func() { done <- matchGlob(pattern, path) }()
	select {
	case got := <-done:
		if got {
			t.Error("matched a path without missing.go")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("matchGlob backtracked for over 2s")
	}
}
