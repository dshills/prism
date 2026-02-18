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
