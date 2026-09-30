package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/review"
)

func coverageReport(cov review.Coverage, findings ...review.Finding) *review.Report {
	if findings == nil {
		findings = []review.Finding{}
	}
	cov.Finalize()
	return &review.Report{
		Tool:     "prism",
		Inputs:   review.InputInfo{Mode: "staged"},
		Findings: findings,
		Summary:  review.ComputeSummary(findings),
		Timing:   review.Timing{LLMMs: 57300, TotalMs: 58000},
		Coverage: cov,
	}
}

var reviewed = review.Coverage{
	Reviewer: review.ConfigReviewer("openai", "gpt-6-sol"),
	Files:    41, Bytes: 79400, Chunks: 5, LLMCalls: 5,
}

func render(t *testing.T, w Writer, r *review.Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := w.Write(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// FR-2 / AC 1: a clean report still says what was reviewed, and when.
func TestTextWriter_CleanReportShowsCoverageAndTiming(t *testing.T) {
	out := render(t, &TextWriter{}, coverageReport(reviewed))
	for _, want := range []string{
		"No issues found. Looks good!",
		"Reviewed 41 files (79.4 KB) in 5 chunks by openai/gpt-6-sol — 5 LLM calls, 57.3s",
		"Completed in 58000ms",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "INCOMPLETE") {
		t.Errorf("complete review marked incomplete:\n%s", out)
	}
}

func TestTextWriter_IncompleteReport(t *testing.T) {
	cov := reviewed
	cov.TruncatedBytes = 5000
	cov.Skipped = []review.Skip{{Target: "diff", Reason: "truncated at maxDiffBytes (5000 bytes not reviewed)"}}
	out := render(t, &TextWriter{}, coverageReport(cov))
	if !strings.Contains(out, "No issues found in what was reviewed.") || strings.Contains(out, "Looks good") {
		t.Errorf("an incomplete clean review must not say it looks good:\n%s", out)
	}
	if !strings.Contains(out, "INCOMPLETE: not everything was reviewed — diff: truncated at maxDiffBytes (5000 bytes not reviewed)") {
		t.Errorf("missing INCOMPLETE line:\n%s", out)
	}
}

// The footer prints for reports with findings too.
func TestTextWriter_FindingsReportShowsCoverage(t *testing.T) {
	f := review.Finding{Severity: review.SeverityHigh, Title: "Bug", Locations: []review.Location{{Path: "a.go", Lines: review.LineRange{Start: 1, End: 1}}}}
	out := render(t, &TextWriter{}, coverageReport(reviewed, f))
	if !strings.Contains(out, "Reviewed 41 files") || !strings.Contains(out, "Completed in") {
		t.Errorf("footer missing:\n%s", out)
	}
}

func TestTextWriter_CacheReplay(t *testing.T) {
	cov := reviewed
	cov.CacheHit, cov.LLMCalls = true, 0
	out := render(t, &TextWriter{}, coverageReport(cov))
	if !strings.Contains(out, "Replayed from cache: 41 files (79.4 KB), originally reviewed by openai/gpt-6-sol") {
		t.Errorf("replay not stated:\n%s", out)
	}
}

func TestMarkdownWriter_Coverage(t *testing.T) {
	out := render(t, &MarkdownWriter{}, coverageReport(reviewed))
	if !strings.Contains(out, "_Reviewed 41 files (79.4 KB) in 5 chunks") {
		t.Errorf("markdown lacks coverage:\n%s", out)
	}
	cov := reviewed
	cov.Skipped = []review.Skip{{Target: "abc1234", Reason: "review failed: boom"}}
	out = render(t, &MarkdownWriter{}, coverageReport(cov))
	if !strings.Contains(out, "**INCOMPLETE: not everything was reviewed — abc1234: review failed: boom**") || strings.Contains(out, "white_check_mark") {
		t.Errorf("markdown incomplete handling:\n%s", out)
	}
}

func TestJSONAndSARIF_CarryCoverage(t *testing.T) {
	r := coverageReport(reviewed)
	var js map[string]any
	if err := json.Unmarshal([]byte(render(t, &JSONWriter{}, r)), &js); err != nil {
		t.Fatal(err)
	}
	cov, ok := js["coverage"].(map[string]any)
	if !ok || cov["chunks"].(float64) != 5 || cov["complete"] != true {
		t.Errorf("json coverage = %v", js["coverage"])
	}
	if skipped, ok := cov["skipped"].([]any); !ok || len(skipped) != 0 {
		t.Errorf("skipped must be an empty array, got %v", cov["skipped"])
	}

	var sarif struct {
		Runs []struct {
			Properties struct {
				Coverage review.Coverage `json:"coverage"`
			} `json:"properties"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(render(t, &SARIFWriter{}, r)), &sarif); err != nil {
		t.Fatal(err)
	}
	if len(sarif.Runs) != 1 || sarif.Runs[0].Properties.Coverage.Chunks != 5 {
		t.Errorf("sarif coverage = %+v", sarif.Runs)
	}
}

// FR-7: discarded findings are listed with their reasons, and never counted.
func TestWriters_DiscardedFindings(t *testing.T) {
	r := coverageReport(reviewed)
	r.Discarded = []review.Discard{{
		Finding: review.Finding{Title: "wg.Go does not compile", Locations: []review.Location{{Path: "pkg/p_test.go", Lines: review.LineRange{Start: 153}}}},
		Reason:  "package compiles: go vet passed in pkg",
	}}
	text := render(t, &TextWriter{}, r)
	if !strings.Contains(text, "Discarded 1 finding(s) that failed verification:") ||
		!strings.Contains(text, "pkg/p_test.go:153  wg.Go does not compile — package compiles: go vet passed in pkg") {
		t.Errorf("text lacks the discarded section:\n%s", text)
	}
	if !strings.Contains(text, "No issues found. Looks good!") {
		t.Errorf("a discarded finding must not count as a finding:\n%s", text)
	}
	md := render(t, &MarkdownWriter{}, r)
	if !strings.Contains(md, "Discarded 1 finding(s) that failed verification") || !strings.Contains(md, "`pkg/p_test.go:153` wg.Go does not compile") {
		t.Errorf("markdown lacks the discarded section:\n%s", md)
	}
	var sarif struct {
		Runs []struct {
			Results []any `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(render(t, &SARIFWriter{}, r)), &sarif); err != nil {
		t.Fatal(err)
	}
	if len(sarif.Runs[0].Results) != 0 {
		t.Errorf("SARIF must exclude discarded findings, got %d results", len(sarif.Runs[0].Results))
	}
	var js map[string]any
	if err := json.Unmarshal([]byte(render(t, &JSONWriter{}, r)), &js); err != nil {
		t.Fatal(err)
	}
	if d, ok := js["discarded"].([]any); !ok || len(d) != 1 {
		t.Errorf("json discarded = %v", js["discarded"])
	}
}
