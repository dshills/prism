package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/review"
)

func suppressedReport() *review.Report {
	finding := func(id, title, path string, line int) review.Finding {
		return review.Finding{
			ID: id, Title: title, Message: "m", Severity: review.SeverityHigh, Category: review.CategorySecurity,
			Locations: []review.Location{{Path: path, Lines: review.LineRange{Start: line, End: line}}},
		}
	}
	findings := []review.Finding{finding("aaaa1111bbbb2222", "SQL injection", "db.go", 10)}
	return &review.Report{
		Tool:     "prism",
		Version:  "1.0",
		Inputs:   review.InputInfo{Mode: "staged"},
		Summary:  review.ComputeSummary(findings),
		Findings: findings,
		Suppressed: []review.Suppression{
			{Finding: finding("cccc3333dddd4444", "Hardcoded secret", "auth.go", 5), Source: review.SuppressedByBaseline, Reason: "test fixture"},
			{Finding: finding("eeee5555ffff6666", "Weak hash", "hash.go", 7), Source: review.SuppressedInline},
		},
	}
}

// Agents read IDs from the default text output to baseline them, and see
// what the baseline left out.
func TestTextWriter_IDsAndSuppressed(t *testing.T) {
	var buf bytes.Buffer
	if err := (&TextWriter{}).Write(&buf, suppressedReport()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"ID: aaaa1111bbbb2222",
		"Suppressed 2 accepted finding(s):",
		"auth.go:5  Hardcoded secret — baseline: test fixture",
		"hash.go:7  Weak hash — prism:ignore",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q:\n%s", want, out)
		}
	}
}

// With every finding suppressed, markdown still lists what it left out.
func TestMarkdownWriter_SuppressedWithNoFindings(t *testing.T) {
	r := suppressedReport()
	r.Findings = nil
	var buf bytes.Buffer
	if err := (&MarkdownWriter{}).Write(&buf, r); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "Suppressed 2 accepted finding(s)") || !strings.Contains(out, "`auth.go:5` Hardcoded secret — baseline: test fixture") {
		t.Errorf("markdown lacks the suppressed findings:\n%s", out)
	}
}

// SARIF carries suppressed findings marked as such, so code scanning shows
// them as dismissed rather than open or gone.
func TestSARIFWriter_Suppressions(t *testing.T) {
	var buf bytes.Buffer
	if err := (&SARIFWriter{}).Write(&buf, suppressedReport()); err != nil {
		t.Fatal(err)
	}
	var log sarifLog
	if err := json.Unmarshal(buf.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	results := log.Runs[0].Results
	if len(results) != 3 {
		t.Fatalf("%d results, want the finding and both suppressed ones", len(results))
	}
	kinds := map[string]string{}
	for _, r := range results {
		id := r.PartialFingerprints[sarifFingerprintKey]
		switch len(r.Suppressions) {
		case 0:
			kinds[id] = ""
		case 1:
			kinds[id] = r.Suppressions[0].Kind + ":" + r.Suppressions[0].Justification
		default:
			t.Errorf("%s has %d suppressions", id, len(r.Suppressions))
		}
	}
	want := map[string]string{
		"aaaa1111bbbb2222": "",
		"cccc3333dddd4444": "external:test fixture",
		"eeee5555ffff6666": "inSource:",
	}
	for id, k := range want {
		if kinds[id] != k {
			t.Errorf("%s: suppression %q, want %q", id, kinds[id], k)
		}
	}
}

// A fix is shown verbatim, as a replacement, in text and markdown.
func TestWriters_ShowFix(t *testing.T) {
	r := suppressedReport()
	r.Findings[0].Fix = &review.Fix{Before: "db.Query(\"SELECT \" + id)", After: "db.Query(\"SELECT ?\", id)"}
	var text bytes.Buffer
	if err := (&TextWriter{}).Write(&text, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Fix (exact replacement):", "    - db.Query(\"SELECT \" + id)", "    + db.Query(\"SELECT ?\", id)"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, text.String())
		}
	}
	var md bytes.Buffer
	if err := (&MarkdownWriter{}).Write(&md, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md.String(), "```diff\n-db.Query(\"SELECT \" + id)\n+db.Query(\"SELECT ?\", id)\n```") {
		t.Errorf("markdown lacks the fix diff:\n%s", md.String())
	}
}
