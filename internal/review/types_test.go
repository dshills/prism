package review

import "testing"

func TestSeverityRank(t *testing.T) {
	tests := []struct {
		severity Severity
		want     int
	}{
		{SeverityLow, 1},
		{SeverityMedium, 2},
		{SeverityHigh, 3},
		{Severity("unknown"), 0},
	}
	for _, tt := range tests {
		got := SeverityRank(tt.severity)
		if got != tt.want {
			t.Errorf("SeverityRank(%q) = %d, want %d", tt.severity, got, tt.want)
		}
	}
}

func TestMeetsThreshold(t *testing.T) {
	tests := []struct {
		severity  Severity
		threshold string
		want      bool
	}{
		{SeverityHigh, "none", false},
		{SeverityHigh, "", false},
		{SeverityHigh, "high", true},
		{SeverityHigh, "medium", true},
		{SeverityHigh, "low", true},
		{SeverityMedium, "high", false},
		{SeverityMedium, "medium", true},
		{SeverityMedium, "low", true},
		{SeverityLow, "high", false},
		{SeverityLow, "medium", false},
		{SeverityLow, "low", true},
	}
	for _, tt := range tests {
		got := MeetsThreshold(tt.severity, tt.threshold)
		if got != tt.want {
			t.Errorf("MeetsThreshold(%q, %q) = %v, want %v", tt.severity, tt.threshold, got, tt.want)
		}
	}
}

func TestComputeSummary(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityHigh},
		{Severity: SeverityMedium},
		{Severity: SeverityMedium},
		{Severity: SeverityLow},
		{Severity: SeverityLow},
		{Severity: SeverityLow},
	}

	s := ComputeSummary(findings)

	if s.Counts.High != 1 {
		t.Errorf("High count = %d, want 1", s.Counts.High)
	}
	if s.Counts.Medium != 2 {
		t.Errorf("Medium count = %d, want 2", s.Counts.Medium)
	}
	if s.Counts.Low != 3 {
		t.Errorf("Low count = %d, want 3", s.Counts.Low)
	}
	if s.HighestSeverity != SeverityHigh {
		t.Errorf("HighestSeverity = %q, want %q", s.HighestSeverity, SeverityHigh)
	}
}

func TestComputeSummary_Empty(t *testing.T) {
	s := ComputeSummary(nil)
	if s.Counts.High != 0 || s.Counts.Medium != 0 || s.Counts.Low != 0 {
		t.Errorf("Expected all zero counts for empty findings")
	}
	if s.HighestSeverity != "" {
		t.Errorf("HighestSeverity = %q, want empty", s.HighestSeverity)
	}
}

func TestCollectProvenance(t *testing.T) {
	findings := []Finding{
		{Provider: "anthropic", Model: "claude-opus-4-5"},
		{Provider: "anthropic", Model: "claude-opus-4-5"},
		{Provider: "openai", Model: "gpt-5.2"},
		{}, // no provenance — must be skipped
	}
	got := CollectProvenance(findings)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Provider != "anthropic" || got[0].Model != "claude-opus-4-5" || !got[0].AIGenerated {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Provider != "openai" || got[1].Model != "gpt-5.2" || !got[1].AIGenerated {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestCollectProvenance_Empty(t *testing.T) {
	if got := CollectProvenance(nil); got != nil {
		t.Errorf("Expected nil for empty findings, got %+v", got)
	}
	if got := CollectProvenance([]Finding{{}}); got != nil {
		t.Errorf("Expected nil when no finding has provenance, got %+v", got)
	}
}

func TestStampProvenance(t *testing.T) {
	findings := []Finding{
		{}, // empty
		{Provider: "existing", Model: "preserved"}, // must not be overwritten
	}
	stamped := stampProvenance(findings, "new-provider", "new-model")
	if stamped[0].Provider != "new-provider" || stamped[0].Model != "new-model" {
		t.Errorf("stamped[0] = %+v; expected new-provider/new-model", stamped[0])
	}
	if stamped[1].Provider != "existing" || stamped[1].Model != "preserved" {
		t.Errorf("stamped[1] = %+v; existing provenance should be preserved", stamped[1])
	}
}
