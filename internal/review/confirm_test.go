package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
)

// checker refutes any finding whose title has "wrong" in it, and fails
// every check while failing is set.
type checker struct {
	mu      sync.Mutex
	prompts []string
	failing bool
}

func (c *checker) Review(_ context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prompts = append(c.prompts, req.UserPrompt)
	if c.failing {
		return providers.ReviewResponse{}, errors.New("unavailable")
	}
	v := `{"verdict":"confirm","reason":"it holds"}`
	if strings.Contains(req.UserPrompt, "Title: wrong") {
		v = "```json\n" + `{"verdict":"refute","reason":"the value is checked on line 3"}` + "\n```"
	}
	return providers.ReviewResponse{Content: v, Provider: "mock", Model: "checker", Usage: providers.Usage{InputTokens: 100, OutputTokens: 10}}, nil
}

func (c *checker) Name() string { return "mock" }

func (c *checker) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.prompts)
	c.prompts = nil
	return n
}

func confirmConfig(t *testing.T) config.Config {
	cfg := chunkTestConfig(t)
	cfg.FailOn = "high"
	cfg.ConfirmBlocking = "mock:checker"
	cfg.BaselineFile = "none"
	return cfg
}

func blockingFindings() []Finding {
	at := func(title string, sev Severity) Finding {
		return Finding{ID: title, Title: title, Severity: sev, Category: CategoryBug, Locations: []Location{{Path: "a.go", Lines: LineRange{Start: 1, End: 1}}}}
	}
	return []Finding{at("real bug", SeverityHigh), at("wrong claim", SeverityHigh), at("minor", SeverityLow)}
}

// Blocking findings are checked; refuted ones are discarded with the
// checker's reason, the rest kept, and a re-review replays the verdicts.
func TestConfirmBlocking(t *testing.T) {
	c := &checker{}
	useProvider(t, c)
	cfg := confirmConfig(t)
	diff := gitctx.DiffResult{Diff: fileDiff("a.go", "x := load()")}

	var cov Coverage
	kept, discarded, _, err := FinalizeFindings(context.Background(), blockingFindings(), diff, cfg, &cov)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 || kept[0].Title != "real bug" || kept[1].Title != "minor" {
		t.Errorf("kept %v", titlesOf(kept))
	}
	if len(discarded) != 1 || discarded[0].Finding.Title != "wrong claim" || discarded[0].Reason != "refuted by mock:checker: the value is checked on line 3" {
		t.Errorf("discarded %+v", discarded)
	}
	if n := c.calls(); n != 2 {
		t.Errorf("%d checks, want 2 (the low finding does not block)", n)
	}
	if u := cov.Confirm; u == nil || u.Checked != 2 || u.Refuted != 1 || u.Failed != 0 || cov.LLMCalls != 2 || len(cov.Tokens) != 1 {
		t.Errorf("coverage confirm %+v, calls %d, tokens %v", cov.Confirm, cov.LLMCalls, cov.Tokens)
	}

	var again Coverage
	if _, discarded, _, _ = FinalizeFindings(context.Background(), blockingFindings(), diff, cfg, &again); len(discarded) != 1 {
		t.Errorf("replayed verdicts discarded %d, want 1", len(discarded))
	}
	if n := c.calls(); n != 0 || again.LLMCalls != 0 {
		t.Errorf("re-review made %d checks, want 0 (cached)", n)
	}
}

// A failed check keeps its finding; no gate, no checks.
func TestConfirmBlocking_FailsOpen(t *testing.T) {
	c := &checker{failing: true}
	useProvider(t, c)
	cfg := confirmConfig(t)
	diff := gitctx.DiffResult{Diff: fileDiff("a.go", "x := load()")}
	var cov Coverage
	kept, discarded, _, err := FinalizeFindings(context.Background(), blockingFindings(), diff, cfg, &cov)
	if err != nil || len(kept) != 3 || len(discarded) != 0 || cov.Confirm.Failed != 2 || c.calls() != 2 {
		t.Errorf("kept %d, discarded %d, confirm %+v, err %v", len(kept), len(discarded), cov.Confirm, err)
	}

	cfg.FailOn = "none"
	if kept, _, _, _ := FinalizeFindings(context.Background(), blockingFindings(), diff, cfg, nil); len(kept) != 3 || c.calls() != 0 {
		t.Error("failOn none: checks were made")
	}
}

// At most maxConfirmations checks, the most severe first; the rest are kept.
func TestConfirmBlocking_Capped(t *testing.T) {
	c := &checker{}
	useProvider(t, c)
	cfg := confirmConfig(t)
	cfg.Cache.Enabled = false
	cfg.MaxFindings = 0
	var findings []Finding
	for i := range maxConfirmations + 5 {
		findings = append(findings, Finding{Title: fmt.Sprintf("f%d", i), Severity: SeverityHigh, Locations: []Location{{Path: "a.go"}}})
	}
	var cov Coverage
	kept, _, _, err := FinalizeFindings(context.Background(), findings, gitctx.DiffResult{Diff: fileDiff("a.go", "x")}, cfg, &cov)
	if err != nil || len(kept) != len(findings) {
		t.Fatalf("kept %d, err %v", len(kept), err)
	}
	if n := c.calls(); n != maxConfirmations || cov.Confirm.Unchecked != 5 {
		t.Errorf("%d checks, unchecked %d; want %d and 5", n, cov.Confirm.Unchecked, maxConfirmations)
	}
}

// The checker is shown the finding and its file's code, redacted.
func TestConfirmPrompt_ShowsRedactedCode(t *testing.T) {
	c := &checker{}
	useProvider(t, c)
	cfg := confirmConfig(t)
	secret := "AKIA" + "ABCDEFGHIJKLMNOP"
	diff := gitctx.DiffResult{Diff: fileDiff("a.go", `key := "`+secret+`"`) + fileDiff("b.go", "other")}
	if _, _, _, err := FinalizeFindings(context.Background(), blockingFindings()[:1], diff, cfg, nil); err != nil {
		t.Fatal(err)
	}
	p := c.prompts[0]
	if !strings.Contains(p, "Title: real bug") || !strings.Contains(p, "+++ b/a.go") || strings.Contains(p, "b/b.go") {
		t.Errorf("prompt:\n%s", p)
	}
	if strings.Contains(p, secret) {
		t.Error("the secret was sent to the checker")
	}

	c.prompts = nil
	leaky := blockingFindings()[:1]
	leaky[0].Message = "the key " + secret + " is hard-coded"
	leaky[0].Evidence = `key := "` + secret + `"`
	if _, _, _, err := FinalizeFindings(context.Background(), leaky, diff, cfg, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.prompts[0], secret) {
		t.Error("the finding's own text sent the secret to the checker")
	}

	// A finding in a file the diff does not show is kept, unchecked.
	c.prompts = nil
	elsewhere := blockingFindings()[:1]
	elsewhere[0].Locations[0].Path = "missing.go"
	var cov Coverage
	kept, _, _, err := FinalizeFindings(context.Background(), elsewhere, diff, cfg, &cov)
	if err != nil || len(kept) != 1 || len(c.prompts) != 0 || cov.Confirm.Unchecked != 1 {
		t.Errorf("no code: kept %d, %d checks, unchecked %d, err %v", len(kept), len(c.prompts), cov.Confirm.Unchecked, err)
	}
}

func TestExcerpt(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -1,2000 +1,2000 @@\n")
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&b, " line %d: return nil\n", i)
	}
	section := b.String()

	// By line number, though the evidence is everywhere.
	got, ok := excerpt(section, "return nil", 1500)
	if !ok || len(got) > maxConfirmContext || !strings.Contains(got, " line 1500: return nil\n") {
		t.Errorf("by line: ok %v, %d bytes", ok, len(got))
	}
	// No line, ambiguous evidence: not checked.
	if _, ok := excerpt(section, "return nil", 0); ok {
		t.Error("ambiguous evidence without a line was located")
	}
	// No line, evidence found once.
	if got, ok := excerpt(section, "line 1234: return nil", 0); !ok || !strings.Contains(got, "line 1234:") {
		t.Errorf("by unique evidence: ok %v", ok)
	}
	// One long line, no newline nearby.
	long := strings.Repeat("x", 3*maxConfirmContext) + "\nend\n"
	if got, ok := excerpt(long, "end", 0); !ok || len(got) > maxConfirmContext {
		t.Errorf("long line: ok %v, %d bytes", ok, len(got))
	}
	if got, ok := excerpt("short", "x", 0); !ok || got != "short" {
		t.Error("a short section is cut")
	}
	if _, ok := excerpt("  \n", "x", 1); ok {
		t.Error("an empty section was located")
	}
	// A long target line is shown whole, with the code around it.
	var lb strings.Builder
	lb.WriteString("@@ -1,3 +1,3 @@\n")
	lb.WriteString(strings.Repeat(" before\n", 500))
	target := " " + strings.Repeat("y", 8000) + " TARGET\n"
	lb.WriteString(target)
	lb.WriteString(strings.Repeat(" after\n", 500))
	if got, ok := excerpt(lb.String(), "", 501); !ok || !strings.Contains(got, target) || len(got) > maxConfirmContext {
		t.Errorf("long target: ok %v, %d bytes, has target %v", ok, len(got), strings.Contains(got, target))
	}
	huge := "@@ -1,1 +1,1 @@\n " + strings.Repeat("z", 2*maxConfirmContext) + "\n"
	if _, ok := excerpt(huge, "", 1); ok {
		t.Error("a line longer than the limit was shown")
	}
}

func TestLineOffset(t *testing.T) {
	section := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -10,3 +20,4 @@ func f() {\n ctx\n-old\n+new\n+added\n ctx2\n"
	for line, want := range map[int]string{20: " ctx", 21: "+new", 22: "+added", 23: " ctx2"} {
		off := lineOffset(section, line)
		if off < 0 || !strings.HasPrefix(section[off:], want+"\n") {
			t.Errorf("line %d at %d, want %q", line, off, want)
		}
	}
	if lineOffset(section, 19) != -1 || lineOffset(section, 24) != -1 {
		t.Error("a line outside the hunk was found")
	}
	// An added line that starts with "++ " is code, not a file header.
	raw := "diff --git a/r.go b/r.go\n--- a/r.go\n+++ b/r.go\n@@ -1,0 +1,3 @@\n+s := `\n+++ not a header\n+target`\n"
	if off := lineOffset(raw, 3); off < 0 || !strings.HasPrefix(raw[off:], "+target`") {
		t.Errorf("line 3 at %d", off)
	}
}

func titlesOf(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Title)
	}
	return out
}
