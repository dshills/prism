package review

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRules_Empty(t *testing.T) {
	rules, err := LoadRules("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules != nil {
		t.Error("expected nil rules for empty path")
	}
}

func TestLoadRules_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	content := `{
		"focus": ["security", "correctness"],
		"severityOverrides": {
			"style": "low",
			"security": "high"
		},
		"required": [
			{"id": "go-errors", "text": "Ensure errors are wrapped with context"}
		]
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	rules, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules error: %v", err)
	}
	if rules == nil {
		t.Fatal("expected non-nil rules")
	}
	if len(rules.Focus) != 2 {
		t.Errorf("Focus = %d, want 2", len(rules.Focus))
	}
	if rules.Focus[0] != "security" {
		t.Errorf("Focus[0] = %q, want %q", rules.Focus[0], "security")
	}
	if len(rules.SeverityOverrides) != 2 {
		t.Errorf("SeverityOverrides = %d, want 2", len(rules.SeverityOverrides))
	}
	if rules.SeverityOverrides["security"] != "high" {
		t.Errorf("SeverityOverrides[security] = %q, want %q", rules.SeverityOverrides["security"], "high")
	}
	if len(rules.Required) != 1 {
		t.Errorf("Required = %d, want 1", len(rules.Required))
	}
	if rules.Required[0].ID != "go-errors" {
		t.Errorf("Required[0].ID = %q, want %q", rules.Required[0].ID, "go-errors")
	}
}

func TestLoadRules_NotFound(t *testing.T) {
	_, err := LoadRules("/nonexistent/path/rules.json")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadRules_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRules(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestBuildRulesPromptSection_Nil(t *testing.T) {
	if s := BuildRulesPromptSection(nil); s != "" {
		t.Errorf("expected empty string for nil rules, got %q", s)
	}
}

func TestBuildRulesPromptSection_Full(t *testing.T) {
	rules := &Rules{
		Focus: []string{"security", "performance"},
		SeverityOverrides: map[string]string{
			"style": "low",
		},
		Required: []RequiredCheck{
			{ID: "auth", Text: "Check auth middleware"},
		},
	}

	s := BuildRulesPromptSection(rules)

	if s == "" {
		t.Fatal("expected non-empty prompt section")
	}

	// Check focus
	if !contains(s, "security") || !contains(s, "performance") {
		t.Error("Missing focus areas in prompt")
	}

	// Check severity override
	if !contains(s, "style") || !contains(s, "low") {
		t.Error("Missing severity override in prompt")
	}

	// Check required
	if !contains(s, "auth") || !contains(s, "Check auth middleware") {
		t.Error("Missing required check in prompt")
	}
}

func TestApplySeverityOverrides_Nil(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityLow, Category: CategoryStyle},
	}
	result := ApplySeverityOverrides(findings, nil)
	if result[0].Severity != SeverityLow {
		t.Error("Nil rules should not change severity")
	}
}

func TestApplySeverityOverrides_Applied(t *testing.T) {
	rules := &Rules{
		SeverityOverrides: map[string]string{
			"style":    "low",
			"security": "high",
		},
	}
	findings := []Finding{
		{ID: "1", Severity: SeverityMedium, Category: CategoryStyle, Title: "Style issue", Locations: []Location{{Path: "a.go", Lines: LineRange{1, 5}}}},
		{ID: "2", Severity: SeverityLow, Category: CategorySecurity, Title: "Security issue", Locations: []Location{{Path: "b.go", Lines: LineRange{10, 15}}}},
		{ID: "3", Severity: SeverityMedium, Category: CategoryBug, Title: "Bug", Locations: []Location{{Path: "c.go", Lines: LineRange{20, 25}}}},
	}

	result := ApplySeverityOverrides(findings, rules)

	if result[0].Severity != SeverityLow {
		t.Errorf("Style finding severity = %q, want %q", result[0].Severity, SeverityLow)
	}
	if result[1].Severity != SeverityHigh {
		t.Errorf("Security finding severity = %q, want %q", result[1].Severity, SeverityHigh)
	}
	if result[2].Severity != SeverityMedium {
		t.Errorf("Bug finding severity should be unchanged, got %q", result[2].Severity)
	}
}

func TestApplySeverityOverrides_EmptyOverrides(t *testing.T) {
	rules := &Rules{
		SeverityOverrides: map[string]string{},
	}
	findings := []Finding{
		{Severity: SeverityMedium, Category: CategoryBug},
	}
	result := ApplySeverityOverrides(findings, rules)
	if result[0].Severity != SeverityMedium {
		t.Error("Empty overrides should not change severity")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
