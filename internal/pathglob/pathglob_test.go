package pathglob

import (
	"strings"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
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
		{"**/*", "main.go", true},
		{"**/*", "a/b/c.go", true},
		{"a/**/b/**/c.go", "a/x/b/y/z/c.go", true},
		{"a/**/b/**/c.go", "a/x/y/c.go", false},
		{"vendor/**", "vendor/github.com/x/y/z.go", true},
		{"**/dist/**", "web/dist/js/app.js", true},
		{"**/dist/**", "dist/app.js", true},
		{"**/dist/**", "distro/app.js", false},
		// A directory's contents match only MatchOrUnder.
		{"internal/review", "internal/review/engine.go", false},
	} {
		if got := Match(c.pattern, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestMatchOrUnder(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"internal/review", "internal/review/engine.go", true},
		{"internal/review", "internal/review/testdata/a/b.go", true},
		{"internal/review", "internal/reviewer/x.go", false},
		{"internal/review/", "internal/review/engine.go", true},
		{"./internal/review", "internal/review/engine.go", true},
		{"vendor/", "vendor/a/b.go", true},
		{"internal/review/testdata/**", "internal/review/testdata/wafereval/x/after/y.ts", true},
		{"internal/*/testdata", "internal/review/testdata/x.go", true},
		{"*.md", "docs/guide/intro.md", true},
		{"main.go", "main.go", true},
		{"main.go", "cmd/main.go", true}, // no slash: the file name
		{"", "main.go", false},
		{"/", "main.go", false},
	} {
		if got := MatchOrUnder(c.pattern, c.path); got != c.want {
			t.Errorf("MatchOrUnder(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// Many "**" against a deep path that does not match still returns at once.
func TestMatch_ManyGlobstars(t *testing.T) {
	pattern := strings.Repeat("**/", 30) + "missing.go"
	path := strings.Repeat("d/", 60) + "found.go"
	done := make(chan bool, 1)
	go func() { done <- Match(pattern, path) }()
	select {
	case got := <-done:
		if got {
			t.Error("matched a path without missing.go")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Match backtracked for over 2s")
	}
}

func TestCheck(t *testing.T) {
	for _, p := range []string{"**/*.go", "internal/[ab]*/x.go", "*", "vendor/"} {
		if err := Check(p); err != nil {
			t.Errorf("Check(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"", "  ", "internal/[/x.go", "/secrets.yaml", "a//b"} {
		if err := Check(p); err == nil {
			t.Errorf("Check(%q) accepted a bad glob", p)
		}
	}
}
