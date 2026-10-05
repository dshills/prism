package review

import (
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
)

// A review's system prompt carries the guidance for its files' languages
// only, in a fixed order whatever the order of the files.
func TestSystemPromptFor(t *testing.T) {
	if got := SystemPromptFor([]string{"README.md", "data.json"}); got != SystemPrompt() {
		t.Error("files without guidance changed the system prompt")
	}
	goOnly := SystemPromptFor([]string{"main.go"})
	if !strings.HasPrefix(goOnly, SystemPrompt()) || !strings.Contains(goOnly, "\nGo:\n") || strings.Contains(goOnly, "\nPython:\n") {
		t.Errorf("Go review prompt:\n%s", goOnly[len(SystemPrompt()):])
	}
	a := SystemPromptFor([]string{"app.py", "web/App.TSX", "cmd/main.go"})
	b := SystemPromptFor([]string{"cmd/main.go", "app.py", "web/App.TSX"})
	if a != b {
		t.Error("the order of the files changed the prompt")
	}
	for _, name := range []string{"Go", "Python", "JavaScript/TypeScript"} {
		if !strings.Contains(a, "\n"+name+":\n") {
			t.Errorf("no %s guidance in %q", name, a[len(SystemPrompt()):])
		}
	}
	if strings.Index(a, "\nGo:\n") > strings.Index(a, "\nPython:\n") {
		t.Error("guidance not in langGuides order")
	}
	if !strings.Contains(CodebaseSystemPromptFor([]string{"lib.rs"}), "\nRust:\n") {
		t.Error("codebase prompt has no Rust guidance")
	}
}

// The guidance is in the prompt fingerprint, so changing it is a cache miss.
func TestLanguageGuidance_InFingerprint(t *testing.T) {
	cfg := config.Default()
	before := promptFingerprint(defaultPromptBuilder, cfg, nil)
	old := langGuides[0].text
	langGuides[0].text += "\n- one more rule"
	defer func() { langGuides[0].text = old }()
	if promptFingerprint(defaultPromptBuilder, cfg, nil) == before {
		t.Error("changing the Go guidance left the fingerprint unchanged")
	}
}

// Every guide is reachable and none shares an extension with another.
func TestLangGuides_Extensions(t *testing.T) {
	seen := map[string]string{}
	for _, g := range langGuides {
		if len(g.exts) == 0 || strings.TrimSpace(g.text) == "" {
			t.Errorf("%s: no extensions or no text", g.name)
		}
		for _, e := range g.exts {
			if prev, ok := seen[e]; ok {
				t.Errorf("%s is in both %s and %s", e, prev, g.name)
			}
			seen[e] = g.name
		}
	}
}
