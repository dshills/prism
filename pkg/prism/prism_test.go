package prism

import (
	"encoding/json"
	"strings"
	"testing"
)

// makeReport constructs a *Report from zero or more Finding values.
// The Summary is computed automatically from the provided findings so
// that test cases can rely on accurate counts without repetitive setup.
func makeReport(findings ...Finding) *Report {
	r := &Report{
		Tool:    "prism",
		Version: Version,
	}
	r.Findings = append([]Finding(nil), findings...)
	r.Summary = Summary{} // computed below
	low, medium, high := 0, 0, 0
	var highest Severity
	for _, f := range findings {
		switch f.Severity {
		case "low":
			low++
		case "medium":
			medium++
		case "high":
			high++
		}
		if severityRank(f.Severity) > severityRank(highest) {
			highest = f.Severity
		}
	}
	r.Summary.Counts.Low = low
	r.Summary.Counts.Medium = medium
	r.Summary.Counts.High = high
	r.Summary.HighestSeverity = highest
	return r
}

// severityRank mirrors the internal ranking without importing review directly.
func severityRank(s Severity) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// -- helpers for building Finding values --------------------------------------

func lowFinding() Finding    { return Finding{ID: "f-low", Severity: "low", Title: "low issue"} }
func mediumFinding() Finding { return Finding{ID: "f-med", Severity: "medium", Title: "medium issue"} }
func highFinding() Finding   { return Finding{ID: "f-high", Severity: "high", Title: "high issue"} }

// =============================================================================
// 1. IsSupportedProvider
// =============================================================================

func TestIsSupportedProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		// exact lower-case matches
		{"anthropic", true},
		{"openai", true},
		{"gemini", true},
		{"google", true},
		{"ollama", true},
		{"lmstudio", true},
		// case-insensitive
		{"ANTHROPIC", true},
		{"OpenAI", true},
		{"Gemini", true},
		{"OLLAMA", true},
		// whitespace trimmed
		{" anthropic ", true},
		{"  openai  ", true},
		// unknown
		{"cohere", false},
		{"unknown-provider", false},
		// empty
		{"", false},
		{"  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := IsSupportedProvider(tt.provider)
			if got != tt.want {
				t.Errorf("IsSupportedProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

// =============================================================================
// 2. ProviderForModel
// =============================================================================

func TestProviderForModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		// anthropic
		{"claude-opus-4-5", "anthropic"},
		{"claude-sonnet-4-6", "anthropic"},
		{"claude-haiku-4-5", "anthropic"},
		// openai
		{"gpt-4o", "openai"},
		{"gpt-5.2", "openai"},
		{"gpt-4.1-mini", "openai"},
		{"o3-mini", "openai"},
		// gemini
		{"gemini-pro", "gemini"},
		{"gemini-2.5-flash", "gemini"},
		{"gemini-3-flash-preview", "gemini"},
		// ollama
		{"llama3.3", "ollama"},
		{"qwen2.5-coder", "ollama"},
		{"codellama", "ollama"},
		{"deepseek-coder-v2", "ollama"},
		// unknown
		{"unknown-model", ""},
		{"", ""},
		{"bert-base", ""},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := ProviderForModel(tt.model)
			if got != tt.want {
				t.Errorf("ProviderForModel(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// =============================================================================
// 3. FilterReportBySeverity
// =============================================================================

func TestFilterReportBySeverity(t *testing.T) {
	t.Run("nil report returns nil", func(t *testing.T) {
		got := FilterReportBySeverity(nil, "high")
		if got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("empty threshold returns same report", func(t *testing.T) {
		r := makeReport(highFinding())
		got := FilterReportBySeverity(r, "")
		if got != r {
			t.Errorf("expected same pointer returned for empty threshold")
		}
	})

	t.Run("threshold 'none' returns same report", func(t *testing.T) {
		r := makeReport(highFinding())
		got := FilterReportBySeverity(r, "none")
		if got != r {
			t.Errorf("expected same pointer returned for threshold 'none'")
		}
	})

	t.Run("threshold 'high' keeps only high findings", func(t *testing.T) {
		r := makeReport(lowFinding(), mediumFinding(), highFinding())
		got := FilterReportBySeverity(r, "high")
		if len(got.Findings) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(got.Findings))
		}
		if got.Findings[0].Severity != "high" {
			t.Errorf("expected high severity, got %q", got.Findings[0].Severity)
		}
		if got.Summary.Counts.High != 1 {
			t.Errorf("expected summary High=1, got %d", got.Summary.Counts.High)
		}
		if got.Summary.Counts.Low != 0 || got.Summary.Counts.Medium != 0 {
			t.Errorf("unexpected low/medium counts in summary: %+v", got.Summary.Counts)
		}
	})

	t.Run("threshold 'medium' keeps high and medium", func(t *testing.T) {
		r := makeReport(lowFinding(), mediumFinding(), highFinding())
		got := FilterReportBySeverity(r, "medium")
		if len(got.Findings) != 2 {
			t.Fatalf("expected 2 findings, got %d", len(got.Findings))
		}
		for _, f := range got.Findings {
			if f.Severity == "low" {
				t.Errorf("low finding should have been filtered, got %+v", f)
			}
		}
		if got.Summary.Counts.Low != 0 {
			t.Errorf("expected Low count 0, got %d", got.Summary.Counts.Low)
		}
	})

	t.Run("original report Findings slice not mutated", func(t *testing.T) {
		r := makeReport(lowFinding(), mediumFinding(), highFinding())
		origLen := len(r.Findings)
		got := FilterReportBySeverity(r, "high")
		if len(r.Findings) != origLen {
			t.Errorf("original report Findings mutated: was %d, now %d", origLen, len(r.Findings))
		}
		// confirm clone is a different pointer
		if got == r {
			t.Errorf("FilterReportBySeverity returned same pointer, expected clone")
		}
	})

	t.Run("all findings below threshold returns empty findings", func(t *testing.T) {
		r := makeReport(lowFinding(), lowFinding())
		got := FilterReportBySeverity(r, "high")
		if len(got.Findings) != 0 {
			t.Errorf("expected 0 findings, got %d", len(got.Findings))
		}
	})
}

// =============================================================================
// 4. FailOnMet
// =============================================================================

func TestFailOnMet(t *testing.T) {
	t.Run("nil report returns false", func(t *testing.T) {
		if FailOnMet(nil, "high") {
			t.Error("expected false for nil report")
		}
	})

	t.Run("empty threshold returns false", func(t *testing.T) {
		r := makeReport(highFinding())
		if FailOnMet(r, "") {
			t.Error("expected false for empty threshold")
		}
	})

	t.Run("threshold 'none' returns false", func(t *testing.T) {
		r := makeReport(highFinding())
		if FailOnMet(r, "none") {
			t.Error("expected false for threshold 'none'")
		}
	})

	t.Run("high finding with threshold 'high' returns true", func(t *testing.T) {
		r := makeReport(highFinding())
		if !FailOnMet(r, "high") {
			t.Error("expected true: high finding meets 'high' threshold")
		}
	})

	t.Run("low finding with threshold 'high' returns false", func(t *testing.T) {
		r := makeReport(lowFinding())
		if FailOnMet(r, "high") {
			t.Error("expected false: low finding does not meet 'high' threshold")
		}
	})

	t.Run("medium finding with threshold 'medium' returns true", func(t *testing.T) {
		r := makeReport(mediumFinding())
		if !FailOnMet(r, "medium") {
			t.Error("expected true: medium finding meets 'medium' threshold")
		}
	})

	t.Run("mixed findings: low only, threshold 'medium' returns false", func(t *testing.T) {
		r := makeReport(lowFinding(), lowFinding())
		if FailOnMet(r, "medium") {
			t.Error("expected false: no medium/high findings")
		}
	})

	t.Run("no findings returns false", func(t *testing.T) {
		r := makeReport()
		if FailOnMet(r, "low") {
			t.Error("expected false for empty findings")
		}
	})
}

// =============================================================================
// 5. RenderReport
// =============================================================================

func TestRenderReport(t *testing.T) {
	r := makeReport(highFinding(), lowFinding())

	t.Run("empty format defaults to JSON", func(t *testing.T) {
		b, err := RenderReport(r, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !json.Valid(b) {
			t.Errorf("output is not valid JSON: %s", b)
		}
	})

	t.Run("format json produces valid JSON", func(t *testing.T) {
		b, err := RenderReport(r, "json")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !json.Valid(b) {
			t.Errorf("output is not valid JSON: %s", b)
		}
		if len(b) == 0 {
			t.Error("expected non-empty output for json format")
		}
	})

	t.Run("format text contains finding title", func(t *testing.T) {
		b, err := RenderReport(r, "text")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(b), "high issue") {
			t.Errorf("text output missing finding title; got: %s", b)
		}
	})

	t.Run("format markdown contains review header", func(t *testing.T) {
		b, err := RenderReport(r, "markdown")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(b), "## Prism Code Review") {
			t.Errorf("markdown output missing expected header; got: %s", b)
		}
	})

	t.Run("format sarif contains schema and runs keys", func(t *testing.T) {
		b, err := RenderReport(r, "sarif")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		s := string(b)
		if !strings.Contains(s, `"$schema"`) {
			t.Errorf("sarif output missing $schema key; got: %s", s)
		}
		if !strings.Contains(s, `"runs"`) {
			t.Errorf("sarif output missing runs key; got: %s", s)
		}
	})

	t.Run("unknown format returns error", func(t *testing.T) {
		_, err := RenderReport(r, "invalid")
		if err == nil {
			t.Error("expected error for unknown format")
		}
		if !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("error message should mention the invalid format, got: %v", err)
		}
	})
}

// =============================================================================
// 6. KnownModels
// =============================================================================

func TestKnownModels(t *testing.T) {
	infos := KnownModels()

	if len(infos) < 1 {
		t.Fatal("KnownModels returned empty slice")
	}

	// collect providers present in the result
	byProvider := make(map[string][]string)
	for _, info := range infos {
		if len(info.Models) == 0 {
			t.Errorf("provider %q has empty Models slice", info.Provider)
		}
		byProvider[info.Provider] = append(byProvider[info.Provider], info.Models...)
	}

	for _, required := range []string{"anthropic", "openai", "gemini", "ollama"} {
		if _, ok := byProvider[required]; !ok {
			t.Errorf("expected provider %q in KnownModels result", required)
		}
	}
}

// =============================================================================
// 7. DefaultReviewOptions
// =============================================================================

func TestDefaultReviewOptions(t *testing.T) {
	opts := DefaultReviewOptions()

	if !opts.MergeBase {
		t.Error("expected MergeBase to be true in DefaultReviewOptions")
	}

	if opts.Provider == "" {
		t.Error("expected non-empty Provider in DefaultReviewOptions")
	}

	if opts.FailOn == "" {
		t.Error("expected non-empty FailOn in DefaultReviewOptions")
	}
}

// =============================================================================
// 8. compareProvenance (unexported)
// =============================================================================

func TestCompareProvenance(t *testing.T) {
	t.Run("valid entries produce Provenance with AIGenerated=true", func(t *testing.T) {
		got := compareProvenance([]string{"anthropic:claude-sonnet-4-6", "openai:gpt-5.2"})
		if len(got) != 2 {
			t.Fatalf("expected 2 provenance entries, got %d", len(got))
		}
		for _, p := range got {
			if !p.AIGenerated {
				t.Errorf("expected AIGenerated=true for %+v", p)
			}
		}
		if got[0].Provider != "anthropic" || got[0].Model != "claude-sonnet-4-6" {
			t.Errorf("unexpected first entry: %+v", got[0])
		}
		if got[1].Provider != "openai" || got[1].Model != "gpt-5.2" {
			t.Errorf("unexpected second entry: %+v", got[1])
		}
	})

	t.Run("malformed entry without colon is dropped", func(t *testing.T) {
		got := compareProvenance([]string{"nocolon", "anthropic:claude-sonnet-4-6"})
		if len(got) != 1 {
			t.Fatalf("expected 1 provenance entry, got %d: %+v", len(got), got)
		}
		if got[0].Provider != "anthropic" {
			t.Errorf("expected anthropic provider, got %q", got[0].Provider)
		}
	})

	t.Run("empty string entry is dropped", func(t *testing.T) {
		got := compareProvenance([]string{"", "openai:gpt-5.2"})
		if len(got) != 1 {
			t.Fatalf("expected 1 provenance entry, got %d", len(got))
		}
	})

	t.Run("entry with empty provider is dropped", func(t *testing.T) {
		got := compareProvenance([]string{":gpt-5.2"})
		if len(got) != 0 {
			t.Fatalf("expected 0 entries for ':gpt-5.2', got %d", len(got))
		}
	})

	t.Run("entry with empty model is dropped", func(t *testing.T) {
		got := compareProvenance([]string{"openai:"})
		if len(got) != 0 {
			t.Fatalf("expected 0 entries for 'openai:', got %d", len(got))
		}
	})

	t.Run("order is preserved", func(t *testing.T) {
		models := []string{"gemini:gemini-2.5-flash", "anthropic:claude-sonnet-4-6", "openai:gpt-5.2"}
		got := compareProvenance(models)
		if len(got) != 3 {
			t.Fatalf("expected 3 entries, got %d", len(got))
		}
		if got[0].Provider != "gemini" || got[1].Provider != "anthropic" || got[2].Provider != "openai" {
			t.Errorf("order not preserved: got %v", got)
		}
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		got := compareProvenance(nil)
		if len(got) != 0 {
			t.Errorf("expected empty slice for nil input, got %d entries", len(got))
		}
	})
}

// =============================================================================
// 9. cloneReport (unexported)
// =============================================================================

func TestCloneReport(t *testing.T) {
	original := makeReport(highFinding(), lowFinding())
	original.Provenance = []Provenance{
		{AIGenerated: true, Provider: "anthropic", Model: "claude-sonnet-4-6"},
	}

	clone := cloneReport(original)

	t.Run("returned pointer differs from input", func(t *testing.T) {
		if clone == original {
			t.Error("cloneReport returned the same pointer")
		}
	})

	t.Run("scalar fields are equal", func(t *testing.T) {
		if clone.Tool != original.Tool {
			t.Errorf("Tool mismatch: %q vs %q", clone.Tool, original.Tool)
		}
		if clone.Version != original.Version {
			t.Errorf("Version mismatch: %q vs %q", clone.Version, original.Version)
		}
	})

	t.Run("Findings slice is a copy — appending to clone does not affect original", func(t *testing.T) {
		origLen := len(original.Findings)
		clone.Findings = append(clone.Findings, mediumFinding())
		if len(original.Findings) != origLen {
			t.Errorf("appending to clone mutated original: original len %d, after append %d", origLen, len(original.Findings))
		}
	})

	t.Run("Findings elements are independent — mutating clone title does not affect original", func(t *testing.T) {
		clone2 := cloneReport(original)
		origTitle := original.Findings[0].Title
		clone2.Findings[0].Title = "mutated"
		if original.Findings[0].Title != origTitle {
			t.Errorf("mutating clone Finding.Title changed original: got %q, want %q", original.Findings[0].Title, origTitle)
		}
	})

	t.Run("Location fields in clone are independent of original", func(t *testing.T) {
		withLoc := makeReport(Finding{
			ID: "f-loc", Severity: "high", Title: "located",
			Locations: []Location{{Path: "original.go"}},
		})
		clone3 := cloneReport(withLoc)
		clone3.Findings[0].Locations[0].Path = "mutated.go"
		if withLoc.Findings[0].Locations[0].Path == "mutated.go" {
			t.Error("mutating clone Location.Path affected original")
		}
	})

	t.Run("Provenance slice is a copy — appending to clone does not affect original", func(t *testing.T) {
		origLen := len(original.Provenance)
		clone.Provenance = append(clone.Provenance, Provenance{AIGenerated: true, Provider: "openai", Model: "gpt-5.2"})
		if len(original.Provenance) != origLen {
			t.Errorf("appending to clone mutated original provenance: was %d, now %d", origLen, len(original.Provenance))
		}
	})

	t.Run("cloneReport of nil-findings report produces non-nil empty Findings", func(t *testing.T) {
		r := &Report{Tool: "prism"}
		c := cloneReport(r)
		if c.Findings == nil {
			t.Error("expected non-nil Findings slice for cloned report with nil findings")
		}
		if len(c.Findings) != 0 {
			t.Errorf("expected empty Findings, got %d", len(c.Findings))
		}
	})
}
