package review

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
)

func TestUsableFix(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *Fix
		want bool
	}{
		{"none", nil, false},
		{"empty, as the schema sends no fix", &Fix{}, false},
		{"whitespace before", &Fix{Before: "  ", After: "x"}, false},
		{"no change", &Fix{Before: "x", After: "x"}, false},
		{"a replacement", &Fix{Before: "return err", After: "return fmt.Errorf(\"load: %w\", err)"}, true},
		{"a deletion", &Fix{Before: "debug()", After: ""}, true},
	} {
		if got := usableFix(tc.in) != nil; got != tc.want {
			t.Errorf("%s: usable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A fix is parsed from the response and kept through the cache.
func TestParseFindings_FixRoundTrip(t *testing.T) {
	fresh, err := parseFindings(`{"findings":[
		{"severity":"low","category":"bug","title":"A","message":"m","suggestion":"s","confidence":0.5,"path":"a.go","startLine":2,"endLine":2,"evidence":"x","fix":{"before":"return err","after":"return wrap(err)"},"tags":[]},
		{"severity":"low","category":"bug","title":"B","message":"m","suggestion":"s","confidence":0.5,"path":"a.go","startLine":3,"endLine":3,"evidence":"y","fix":{"before":"","after":""},"tags":[]}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if fresh[0].Fix == nil || fresh[0].Fix.After != "return wrap(err)" || fresh[1].Fix != nil {
		t.Fatalf("fixes = %+v, %+v; want the first only", fresh[0].Fix, fresh[1].Fix)
	}
	stored, err := json.Marshal(findingsToRaw(fresh))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := parseFindings(string(stored))
	if err != nil {
		t.Fatal(err)
	}
	if replayed[0].Fix == nil || *replayed[0].Fix != *fresh[0].Fix {
		t.Errorf("fix after the cache = %+v, want %+v", replayed[0].Fix, fresh[0].Fix)
	}
}

// newFileSection is a diff section adding path as a new file, which shows
// the whole file.
func newFileSection(path string, lines ...string) string {
	var b strings.Builder
	b.WriteString("diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n")
	b.WriteString("@@ -0,0 +1," + strconv.Itoa(len(lines)) + " @@\n")
	for _, l := range lines {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

func fixFinding(before string) Finding {
	f := evidenceFinding("a.go", CategoryBug, "t", 3, "")
	f.Fix = &Fix{Before: before, After: "REPLACED"}
	return f
}

// A fix survives only when its Before is in the file exactly once.
func TestVerifyFixes(t *testing.T) {
	diff := newFileSection("a.go", append(twoReturns, "x := \"ababa\"")...) // "return err" twice, "func load() error {" once
	for _, tc := range []struct {
		name   string
		before string
		keep   bool
	}{
		{"once", "func load() error {", true},
		{"several lines, once", "if err := read(); err != nil {\n\t\treturn err", true},
		{"twice", "return err", false},
		{"not in the file", "return nil, err", false},
		{"paraphrased whitespace", "if err := read();  err != nil {", false},
		{"overlapping matches", "aba", false},
	} {
		got := verifyFixes([]Finding{fixFinding(tc.before)}, diff, nil)[0]
		if kept := got.Fix != nil; kept != tc.keep {
			t.Errorf("%s: fix kept = %v, want %v", tc.name, kept, tc.keep)
		}
		if dropped := slices.Contains(got.Tags, TagFixDropped); dropped == tc.keep {
			t.Errorf("%s: %s tag = %v, want %v", tc.name, TagFixDropped, dropped, !tc.keep)
		}
	}
}

// A diff that shows the whole file keeps its final newline, unless git marks
// it as missing, so a fix reaching the end of the file is checked correctly.
func TestWholeFilesInDiff_FinalNewline(t *testing.T) {
	withNewline := newFileSection("a.go", "package a", "var x = 1")
	if got := wholeFilesInDiff(withNewline)["a.go"]; got != "package a\nvar x = 1\n" {
		t.Errorf("content = %q, want the final newline kept", got)
	}
	if got := verifyFixes([]Finding{fixFinding("var x = 1\n")}, withNewline, nil)[0]; got.Fix == nil {
		t.Error("a fix that includes the final newline should be kept")
	}
	without := withNewline + "\\ No newline at end of file\n"
	if got := wholeFilesInDiff(without)["a.go"]; got != "package a\nvar x = 1" {
		t.Errorf("content = %q, want no final newline", got)
	}
}

// With the whole file to hand, Before must be unique in all of it, not just
// in the diff: code outside the hunks would make the replacement ambiguous.
func TestVerifyFixes_WholeFile(t *testing.T) {
	diff := addedSection("a.go", 20, "x := compute()")
	whole := "package a\n\nfunc f() {\n\tx := compute()\n}\n\nfunc g() {\n\tx := compute()\n}\n"
	files := func(string) (string, bool) { return whole, true }
	if got := verifyFixes([]Finding{fixFinding("x := compute()")}, diff, nil)[0]; got.Fix != nil {
		t.Error("a partial diff cannot show the fix is unique: without the file it should be dropped")
	}
	if got := verifyFixes([]Finding{fixFinding("x := compute()")}, diff, files)[0]; got.Fix != nil {
		t.Error("twice in the whole file: the fix should be dropped")
	}
	if got := verifyFixes([]Finding{fixFinding("func g() {\n\tx := compute()")}, diff, files)[0]; got.Fix == nil {
		t.Error("once in the whole file: the fix should be kept")
	}
}

// End to end: a fix that cannot be applied is dropped by verification, and
// verification turned off leaves fixes alone.
func TestVerifyFindings_Fixes(t *testing.T) {
	diff := gitctx.DiffResult{Diff: newFileSection("a.go", twoReturns...), Mode: "snippet"}
	good := fixFinding("func load() error {")
	good.Evidence = "func load() error {"
	good.Locations[0].Lines = LineRange{Start: 1, End: 1}
	bad := fixFinding("return err")
	bad.Evidence = "return err"

	cfg := config.Default()
	kept, _ := VerifyFindings(context.Background(), []Finding{good, bad}, diff, cfg)
	if len(kept) != 2 || kept[0].Fix == nil || kept[1].Fix != nil {
		t.Fatalf("kept fixes = %v, %v; want only the unique one", kept[0].Fix, kept[1].Fix)
	}

	off := false
	cfg.VerifyFindings = &off
	kept, _ = VerifyFindings(context.Background(), []Finding{bad}, diff, cfg)
	if kept[0].Fix == nil || strings.Contains(strings.Join(kept[0].Tags, ","), TagFixDropped) {
		t.Error("with verification off, fixes pass through unchanged")
	}
}
