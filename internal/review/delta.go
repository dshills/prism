package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Delta labels on a finding compared with an earlier review.
const (
	DeltaNew        = "new"        // not in the earlier review
	DeltaPersisting = "persisting" // in the earlier review too
)

// DeltaSummary compares a review with an earlier one, by finding ID (the
// stable fingerprint, fingerprint.go). In a fix loop it tells an agent
// whether it is making progress: what its fixes resolved, what is left, and
// what they introduced.
type DeltaSummary struct {
	// Since names the earlier review: a report file, or "last".
	Since      string `json:"since"`
	New        int    `json:"new"`
	Persisting int    `json:"persisting"`
	// Resolved lists the earlier review's findings that are gone from this
	// one, in files this one reviewed. A finding accepted in the meantime
	// (baseline or prism:ignore) is not resolved and not listed.
	Resolved []Finding `json:"resolved"`
	// OutOfScope counts earlier findings in files this review did not cover:
	// not resolved, just not looked at.
	OutOfScope int `json:"outOfScope"`
	// OnlyNew is set when the report's findings were limited to new ones.
	OnlyNew bool `json:"onlyNew,omitempty"`
	// Incomplete is set when this review did not cover all its input (a
	// failed chunk, a truncated diff, a skipped commit). Which files were
	// missed is not known exactly, so nothing is counted as resolved: every
	// earlier finding not seen again is out of scope instead.
	Incomplete bool `json:"incomplete,omitempty"`
}

// PriorReview is what an earlier review found: the findings it reported and
// the ones it left out as accepted.
type PriorReview struct {
	Findings []Finding
	Accepted []Finding
}

// ReadPriorReview reads an earlier review from a prism JSON report or a SARIF
// log prism wrote (finding IDs are its partialFingerprints).
func ReadPriorReview(data []byte) (*PriorReview, error) {
	var probe struct {
		Runs json.RawMessage `json:"runs"`
		Tool string          `json:"tool"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("reading earlier review: %w", err)
	}
	switch {
	case probe.Runs != nil:
		return readPriorSARIF(data)
	case probe.Tool == "prism":
		var r Report
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("reading earlier review: %w", err)
		}
		if r.Delta != nil && r.Delta.OnlyNew {
			return nil, errOnlyNewBaseline
		}
		prior := &PriorReview{Findings: r.Findings}
		for _, s := range r.Suppressed {
			prior.Accepted = append(prior.Accepted, s.Finding)
		}
		return prior, nil
	default:
		return nil, errors.New("reading earlier review: not a prism JSON report or SARIF log")
	}
}

// errOnlyNewBaseline refuses a --only-new report as an earlier review: it
// leaves the persisting findings out, so comparing with it would call them
// new again.
var errOnlyNewBaseline = errors.New("reading earlier review: it was written with --only-new, so it lacks the persisting findings; compare with a full report instead")

// SARIFFingerprintKey names a finding's ID among a SARIF result's
// partialFingerprints, as prism writes and reads them. The version changes if
// the fingerprint scheme does.
const SARIFFingerprintKey = "prismFindingId/v1"

func readPriorSARIF(data []byte) (*PriorReview, error) {
	var log struct {
		Runs []struct {
			Properties struct {
				Delta *DeltaSummary `json:"delta"`
			} `json:"properties"`
			Tool struct {
				Driver struct {
					Name  string `json:"name"`
					Rules []struct {
						ID               string `json:"id"`
						Name             string `json:"name"`
						ShortDescription struct {
							Text string `json:"text"`
						} `json:"shortDescription"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID  string `json:"ruleId"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
							EndLine   int `json:"endLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
				PartialFingerprints map[string]string `json:"partialFingerprints"`
				Suppressions        []json.RawMessage `json:"suppressions"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &log); err != nil {
		return nil, fmt.Errorf("reading earlier SARIF: %w", err)
	}
	prior := &PriorReview{}
	fromPrism := false
	for _, run := range log.Runs {
		if run.Tool.Driver.Name != "prism" {
			continue // another tool's run: its results are not prism findings
		}
		fromPrism = true
		if d := run.Properties.Delta; d != nil && d.OnlyNew {
			return nil, errOnlyNewBaseline
		}
		type rule struct{ name, title string }
		rules := map[string]rule{}
		for _, r := range run.Tool.Driver.Rules {
			rules[r.ID] = rule{r.Name, r.ShortDescription.Text}
		}
		for _, res := range run.Results {
			id := res.PartialFingerprints[SARIFFingerprintKey]
			if id == "" {
				continue // not written by prism, or before IDs were fingerprints
			}
			f := Finding{
				ID:       id,
				Severity: severityFromSARIF(res.Level),
				Title:    rules[res.RuleID].title,
				Category: Category(rules[res.RuleID].name),
				Message:  res.Message.Text,
			}
			for _, l := range res.Locations {
				f.Locations = append(f.Locations, Location{
					Path:  l.PhysicalLocation.ArtifactLocation.URI,
					Lines: LineRange{Start: l.PhysicalLocation.Region.StartLine, End: l.PhysicalLocation.Region.EndLine},
				})
			}
			if len(res.Suppressions) > 0 {
				prior.Accepted = append(prior.Accepted, f)
			} else {
				prior.Findings = append(prior.Findings, f)
			}
		}
	}
	if !fromPrism {
		return nil, errors.New("reading earlier SARIF: no run written by prism")
	}
	return prior, nil
}

// severityFromSARIF reverses the SARIF writer's severity-to-level mapping.
func severityFromSARIF(level string) Severity {
	switch level {
	case "error":
		return SeverityHigh
	case "warning":
		return SeverityMedium
	default:
		return SeverityLow
	}
}

// ApplyDelta compares report with an earlier review and returns a copy of
// report whose findings are labelled new or persisting, with a summary that
// lists what was resolved. reviewed is the files this review covered: an
// earlier finding in another file is out of scope rather than resolved. With
// nil, every file counts as covered. A report whose coverage is incomplete
// counts nothing as resolved, since a finding missing from it may only have
// gone unreviewed. onlyNew limits the copy's findings, and so its summary and
// exit code, to the new ones.
func ApplyDelta(report *Report, prior *PriorReview, since string, reviewed []string, onlyNew bool) *Report {
	out := *report
	seen := map[string]bool{} // IDs the earlier review had, reported or accepted
	for _, f := range prior.Findings {
		seen[f.ID] = true
	}
	for _, f := range prior.Accepted {
		seen[f.ID] = true
	}
	present := map[string]bool{} // IDs this review has, reported or accepted
	for _, s := range report.Suppressed {
		present[s.Finding.ID] = true
	}

	delta := &DeltaSummary{Since: since, Resolved: []Finding{}, OnlyNew: onlyNew, Incomplete: report.Coverage.Incomplete()}
	findings := make([]Finding, 0, len(report.Findings))
	for _, f := range report.Findings {
		present[f.ID] = true
		if seen[f.ID] {
			f.Delta = DeltaPersisting
			delta.Persisting++
			if onlyNew {
				continue
			}
		} else {
			f.Delta = DeltaNew
			delta.New++
		}
		findings = append(findings, f)
	}

	var inScope map[string]bool
	if reviewed != nil {
		inScope = make(map[string]bool, len(reviewed))
		for _, p := range reviewed {
			inScope[p] = true
		}
	}
	for _, f := range prior.Findings {
		if present[f.ID] {
			continue
		}
		if delta.Incomplete || (inScope != nil && !inScope[findingPath(f)]) {
			delta.OutOfScope++
			continue
		}
		delta.Resolved = append(delta.Resolved, f)
	}

	out.Findings = findings
	out.Summary = ComputeSummary(findings)
	out.Delta = delta
	return &out
}

// DeltaLine is the one-line delta statement text and markdown reports print.
func (d *DeltaSummary) DeltaLine() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Since %s: %d new, %d persisting, %d resolved", d.Since, d.New, d.Persisting, len(d.Resolved))
	switch {
	case d.Incomplete && d.OutOfScope > 0:
		fmt.Fprintf(&b, " (review incomplete: %d earlier finding(s) not seen again are not counted as resolved)", d.OutOfScope)
	case d.OutOfScope > 0:
		fmt.Fprintf(&b, " (%d in files not reviewed this time)", d.OutOfScope)
	}
	if d.OnlyNew {
		b.WriteString("; showing new findings only")
	}
	return b.String()
}
