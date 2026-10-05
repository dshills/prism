package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func deltaFinding(id, path string) Finding {
	return Finding{
		ID: id, Title: "issue " + id, Severity: SeverityHigh, Category: CategoryBug,
		Locations: []Location{{Path: path, Lines: LineRange{Start: 1, End: 1}}},
	}
}

func idsOf(fs []Finding) string {
	var ids []string
	for _, f := range fs {
		ids = append(ids, f.ID+":"+f.Delta)
	}
	return strings.Join(ids, ",")
}

// A fix loop: the earlier review had A, B and C; the agent fixed A, B is
// still there, D is new, and C's file was not reviewed this time.
func TestApplyDelta(t *testing.T) {
	prior := &PriorReview{Findings: []Finding{
		deltaFinding("A", "a.go"), deltaFinding("B", "a.go"), deltaFinding("C", "c.go"),
	}}
	report := &Report{Findings: []Finding{deltaFinding("B", "a.go"), deltaFinding("D", "a.go")}}
	report.Summary = ComputeSummary(report.Findings)

	got := ApplyDelta(report, prior, "last", []string{"a.go"}, false)
	if ids := idsOf(got.Findings); ids != "B:persisting,D:new" {
		t.Errorf("findings = %s", ids)
	}
	d := got.Delta
	if d.New != 1 || d.Persisting != 1 || len(d.Resolved) != 1 || d.Resolved[0].ID != "A" || d.OutOfScope != 1 {
		t.Errorf("delta = %+v, want 1 new, 1 persisting, A resolved, C out of scope", d)
	}
	if line := d.DeltaLine(); line != "Since last: 1 new, 1 persisting, 1 resolved (1 in files not reviewed this time)" {
		t.Errorf("DeltaLine = %q", line)
	}
	if report.Delta != nil || report.Findings[0].Delta != "" {
		t.Error("ApplyDelta changed the report it was given")
	}
}

// An incomplete review cannot tell resolved from unreviewed, so it counts
// nothing as resolved.
func TestApplyDelta_IncompleteResolvesNothing(t *testing.T) {
	prior := &PriorReview{Findings: []Finding{deltaFinding("A", "a.go")}}
	report := &Report{Coverage: Coverage{Skipped: []Skip{{Target: "chunk 1/2 (a.go)", Reason: "review failed"}}}}
	got := ApplyDelta(report, prior, "last", []string{"a.go"}, false)
	if len(got.Delta.Resolved) != 0 || got.Delta.OutOfScope != 1 || !got.Delta.Incomplete {
		t.Errorf("delta = %+v, want A out of scope, nothing resolved", got.Delta)
	}
	if !strings.Contains(got.Delta.DeltaLine(), "review incomplete") {
		t.Errorf("DeltaLine = %q, want the reason", got.Delta.DeltaLine())
	}
}

// --only-new keeps only the new findings, so they alone set the summary
// (and exit code), while the delta still counts the persisting ones.
func TestApplyDelta_OnlyNew(t *testing.T) {
	prior := &PriorReview{Findings: []Finding{deltaFinding("B", "a.go")}}
	report := &Report{Findings: []Finding{deltaFinding("B", "a.go"), deltaFinding("D", "a.go")}}
	got := ApplyDelta(report, prior, "base.sarif", nil, true)
	if ids := idsOf(got.Findings); ids != "D:new" || got.Summary.Counts.High != 1 {
		t.Errorf("findings = %s, %d high; want only D", ids, got.Summary.Counts.High)
	}
	if got.Delta.Persisting != 1 || !got.Delta.OnlyNew || !strings.HasSuffix(got.Delta.DeltaLine(), "showing new findings only") {
		t.Errorf("delta = %+v", got.Delta)
	}
	if len(report.Findings) != 2 {
		t.Error("the full report must stay whole, to be remembered as the last review")
	}
}

// Accepted findings are neither new nor resolved: one accepted since the
// earlier review is not listed as resolved, and one the earlier review had
// accepted is not new when it is reported again.
func TestApplyDelta_Accepted(t *testing.T) {
	prior := &PriorReview{
		Findings: []Finding{deltaFinding("A", "a.go")},
		Accepted: []Finding{deltaFinding("B", "a.go")},
	}
	report := &Report{
		Findings:   []Finding{deltaFinding("B", "a.go")},
		Suppressed: []Suppression{{Finding: deltaFinding("A", "a.go"), Source: SuppressedByBaseline}},
	}
	got := ApplyDelta(report, prior, "last", nil, false)
	if ids := idsOf(got.Findings); ids != "B:persisting" || len(got.Delta.Resolved) != 0 {
		t.Errorf("findings = %s, resolved = %v; want B persisting, nothing resolved", ids, got.Delta.Resolved)
	}
}

func TestReadPriorReview(t *testing.T) {
	report := Report{
		Tool:       "prism",
		Findings:   []Finding{deltaFinding("A", "a.go")},
		Suppressed: []Suppression{{Finding: deltaFinding("B", "b.go")}},
	}
	data, _ := json.Marshal(report)
	prior, err := ReadPriorReview(data)
	if err != nil || len(prior.Findings) != 1 || prior.Findings[0].ID != "A" || len(prior.Accepted) != 1 {
		t.Fatalf("JSON: %+v, %v", prior, err)
	}

	sarif := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"prism","rules":[{"id":"r1","name":"bug","shortDescription":{"text":"Nil map write"}}]}},
		"results":[
			{"ruleId":"r1","level":"error","message":{"text":"m"},"partialFingerprints":{"prismFindingId/v1":"A"},
			 "locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":3,"endLine":4}}}]},
			{"ruleId":"r1","message":{"text":"m"},"partialFingerprints":{"prismFindingId/v1":"B"},"suppressions":[{"kind":"external"}]},
			{"ruleId":"r1","message":{"text":"from another tool"}}
		]}]}`
	prior, err = ReadPriorReview([]byte(sarif))
	if err != nil {
		t.Fatal(err)
	}
	if len(prior.Findings) != 1 || prior.Findings[0].ID != "A" || prior.Findings[0].Title != "Nil map write" || findingPath(prior.Findings[0]) != "a.go" || prior.Findings[0].Severity != SeverityHigh {
		t.Errorf("SARIF findings = %+v", prior.Findings)
	}
	if len(prior.Accepted) != 1 || prior.Accepted[0].ID != "B" {
		t.Errorf("SARIF accepted = %+v, want B", prior.Accepted)
	}

	for name, data := range map[string]string{
		"neither":              `{"something":"else"}`,
		"another tool's SARIF": `{"runs":[{"tool":{"driver":{"name":"semgrep"}},"results":[]}]}`,
		"null runs":            `{"runs":null}`,
	} {
		if _, err := ReadPriorReview([]byte(data)); err == nil {
			t.Errorf("%s: should be an error", name)
		}
	}
	if prior, err := ReadPriorReview([]byte(`{"runs":[{"tool":{"driver":{"name":"prism"}},"results":[]}]}`)); err != nil || len(prior.Findings) != 0 {
		t.Errorf("an empty prism SARIF log is a valid, empty review: %v", err)
	}
}

// A --only-new report lacks the persisting findings, so it is refused as an
// earlier review, from JSON and from SARIF alike.
func TestReadPriorReview_RefusesOnlyNew(t *testing.T) {
	data, _ := json.Marshal(Report{Tool: "prism", Delta: &DeltaSummary{OnlyNew: true}})
	if _, err := ReadPriorReview(data); err == nil || !strings.Contains(err.Error(), "--only-new") {
		t.Errorf("JSON: err = %v", err)
	}
	sarif := `{"runs":[{"properties":{"delta":{"since":"x","onlyNew":true}},"tool":{"driver":{"name":"prism"}},"results":[]}]}`
	if _, err := ReadPriorReview([]byte(sarif)); err == nil || !strings.Contains(err.Error(), "--only-new") {
		t.Errorf("SARIF: err = %v", err)
	}
}
