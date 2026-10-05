package review

import (
	"testing"

	"github.com/dshills/prism/internal/config"
)

// codebaseBuilderFor mirrors the builder runCodebaseWithFileCache uses.
func codebaseBuilderFor(maxPerFile int) PromptBuilder {
	return func(chunkDiff string, files []string, c config.Config, r *Rules) (string, string) {
		return CodebaseSystemPrompt(), BuildCodebaseUserPrompt(chunkDiff, files, c.MaxFindings, maxPerFile, c.FailOn, c.MinSeverity, r)
	}
}

// Every input that changes what the model is asked must change the prompt
// fingerprint, or a review asked something else is replayed from the cache.
func TestPromptFingerprint_CoversPromptInputs(t *testing.T) {
	base := config.Default()
	base.FailOn = "medium"
	rules := &Rules{Focus: []string{"security"}}
	want := promptFingerprint(defaultPromptBuilder, base, rules)

	failOn := base
	failOn.FailOn = "high"
	maxFindings := base
	maxFindings.MaxFindings = base.MaxFindings + 1

	cases := map[string]string{
		"failOn":      promptFingerprint(defaultPromptBuilder, failOn, rules),
		"maxFindings": promptFingerprint(defaultPromptBuilder, maxFindings, rules),
		"rules":       promptFingerprint(defaultPromptBuilder, base, &Rules{Focus: []string{"performance"}}),
		"no rules":    promptFingerprint(defaultPromptBuilder, base, nil),
		"builder":     promptFingerprint(codebaseBuilderFor(0), base, rules),
	}
	for name, got := range cases {
		if got == want {
			t.Errorf("fingerprint ignores %s", name)
		}
	}

	if got := promptFingerprint(defaultPromptBuilder, base, rules); got != want {
		t.Error("same inputs gave a different fingerprint")
	}
}

// Codebase mode's per-file limit is in its prompt, so it is in its key.
func TestPromptFingerprint_CodebaseMaxFindingsPerFile(t *testing.T) {
	cfg := config.Default()
	if promptFingerprint(codebaseBuilderFor(3), cfg, nil) == promptFingerprint(codebaseBuilderFor(5), cfg, nil) {
		t.Error("fingerprint ignores maxFindingsPerFile")
	}
}

// Inputs the prompt does not show must not split the cache: failOn "none"
// and "" both leave the severity line out.
func TestPromptFingerprint_IgnoresInputsOutsidePrompt(t *testing.T) {
	a, b := config.Default(), config.Default()
	a.FailOn, b.FailOn = "", "none"
	if promptFingerprint(defaultPromptBuilder, a, nil) != promptFingerprint(defaultPromptBuilder, b, nil) {
		t.Error("failOn \"\" and \"none\" give the same prompt but different fingerprints")
	}
}

// Severity overrides live in a map; the rendered policy, and so the key, must
// not depend on map iteration order.
func TestPromptFingerprint_StableRulesOrder(t *testing.T) {
	cfg := config.Default()
	rules := &Rules{SeverityOverrides: map[string]string{
		"security": "high", "style": "low", "performance": "medium", "docs": "low", "bug": "high",
	}}
	want := promptFingerprint(defaultPromptBuilder, cfg, rules)
	for range 50 {
		if promptFingerprint(defaultPromptBuilder, cfg, rules) != want {
			t.Fatal("fingerprint changes between calls with the same rules")
		}
	}
}

func TestDiffCacheKey_IncludesPrompt(t *testing.T) {
	cfg := config.Default()
	if diffCacheKey(cfg, "p1", "diff") == diffCacheKey(cfg, "p2", "diff") {
		t.Error("diff cache key ignores the prompt fingerprint")
	}
}

// The per-file key used to be (provider, model, section) only, so codebase
// reviews cached under an older prompt or chunker were replayed. It now goes
// through reviewCacheKey, which also carries chunkerVersion.
func TestFileCacheKey_IncludesPrompt(t *testing.T) {
	section := makeSection("a.go", "package a")
	if fileCacheKey("p", "m", "p1", section) == fileCacheKey("p", "m", "p2", section) {
		t.Error("file cache key ignores the prompt fingerprint")
	}
	if fileCacheKey("p", "m", "p1", section) != reviewCacheKey("p", "m", "p1", section) {
		t.Error("file cache key should be the shared review key of the section")
	}
	if fileCacheKey("p", "m", "p1", section) == fileCacheKey("p", "m2", "p1", section) {
		t.Error("file cache key ignores the model")
	}
}
