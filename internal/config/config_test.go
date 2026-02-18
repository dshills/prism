package config

import (
	"os"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Provider != "anthropic" {
		t.Errorf("Default provider = %q, want %q", cfg.Provider, "anthropic")
	}
	if cfg.Format != "text" {
		t.Errorf("Default format = %q, want %q", cfg.Format, "text")
	}
	if cfg.FailOn != "none" {
		t.Errorf("Default failOn = %q, want %q", cfg.FailOn, "none")
	}
	if cfg.MaxFindings != 50 {
		t.Errorf("Default maxFindings = %d, want 50", cfg.MaxFindings)
	}
	if cfg.ContextLines != 3 {
		t.Errorf("Default contextLines = %d, want 3", cfg.ContextLines)
	}
	if cfg.MaxDiffBytes != 500000 {
		t.Errorf("Default maxDiffBytes = %d, want 500000", cfg.MaxDiffBytes)
	}
	if !cfg.Privacy.RedactSecrets {
		t.Error("Default redactSecrets should be true")
	}
}

func TestMergeEnv(t *testing.T) {
	// Save and restore env
	orig := map[string]string{}
	envKeys := []string{"PRISM_PROVIDER", "PRISM_MODEL", "PRISM_FAIL_ON", "PRISM_FORMAT", "PRISM_MAX_FINDINGS", "PRISM_CONTEXT_LINES"}
	for _, k := range envKeys {
		orig[k] = os.Getenv(k)
	}
	defer func() {
		for k, v := range orig {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()

	os.Setenv("PRISM_PROVIDER", "openai")
	os.Setenv("PRISM_MODEL", "gpt-4o")
	os.Setenv("PRISM_FAIL_ON", "high")
	os.Setenv("PRISM_FORMAT", "json")
	os.Setenv("PRISM_MAX_FINDINGS", "10")
	os.Setenv("PRISM_CONTEXT_LINES", "5")

	cfg := Default()
	mergeEnv(&cfg)

	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "openai")
	}
	if cfg.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", cfg.Model, "gpt-4o")
	}
	if cfg.FailOn != "high" {
		t.Errorf("FailOn = %q, want %q", cfg.FailOn, "high")
	}
	if cfg.Format != "json" {
		t.Errorf("Format = %q, want %q", cfg.Format, "json")
	}
	if cfg.MaxFindings != 10 {
		t.Errorf("MaxFindings = %d, want 10", cfg.MaxFindings)
	}
	if cfg.ContextLines != 5 {
		t.Errorf("ContextLines = %d, want 5", cfg.ContextLines)
	}
}

func TestMergeOverrides(t *testing.T) {
	cfg := Default()
	overrides := map[string]string{
		"provider":    "gemini",
		"model":       "gemini-2.0-flash",
		"format":      "json",
		"failOn":      "medium",
		"maxFindings": "25",
	}
	mergeOverrides(&cfg, overrides)

	if cfg.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "gemini")
	}
	if cfg.Model != "gemini-2.0-flash" {
		t.Errorf("Model = %q, want %q", cfg.Model, "gemini-2.0-flash")
	}
	if cfg.Format != "json" {
		t.Errorf("Format = %q, want %q", cfg.Format, "json")
	}
	if cfg.FailOn != "medium" {
		t.Errorf("FailOn = %q, want %q", cfg.FailOn, "medium")
	}
	if cfg.MaxFindings != 25 {
		t.Errorf("MaxFindings = %d, want 25", cfg.MaxFindings)
	}
}

func TestMergeOverrides_Nil(t *testing.T) {
	cfg := Default()
	mergeOverrides(&cfg, nil)
	if cfg.Provider != "anthropic" {
		t.Errorf("Provider changed with nil overrides")
	}
}

func TestSetField(t *testing.T) {
	cfg := Default()

	tests := []struct {
		key   string
		value string
	}{
		{"provider", "openai"},
		{"model", "gpt-4o"},
		{"format", "json"},
		{"failOn", "high"},
		{"maxFindings", "100"},
		{"contextLines", "10"},
		{"maxDiffBytes", "1000000"},
		{"rulesFile", "rules.json"},
	}

	for _, tt := range tests {
		if err := SetField(&cfg, tt.key, tt.value); err != nil {
			t.Errorf("SetField(%q, %q) error: %v", tt.key, tt.value, err)
		}
	}

	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "openai")
	}
	if cfg.MaxFindings != 100 {
		t.Errorf("MaxFindings = %d, want 100", cfg.MaxFindings)
	}
}

func TestSetField_UnknownKey(t *testing.T) {
	cfg := Default()
	err := SetField(&cfg, "nonexistent", "value")
	if err == nil {
		t.Error("Expected error for unknown key")
	}
}

func TestSetField_InvalidInt(t *testing.T) {
	cfg := Default()
	err := SetField(&cfg, "maxFindings", "notanumber")
	if err == nil {
		t.Error("Expected error for non-integer value")
	}
}

func TestConfigPrecedence(t *testing.T) {
	// Test that overrides > env > defaults
	orig := os.Getenv("PRISM_PROVIDER")
	defer func() {
		if orig == "" {
			os.Unsetenv("PRISM_PROVIDER")
		} else {
			os.Setenv("PRISM_PROVIDER", orig)
		}
	}()

	os.Setenv("PRISM_PROVIDER", "openai")

	cfg := Default()
	mergeEnv(&cfg)
	if cfg.Provider != "openai" {
		t.Errorf("After env merge, Provider = %q, want %q", cfg.Provider, "openai")
	}

	mergeOverrides(&cfg, map[string]string{"provider": "gemini"})
	if cfg.Provider != "gemini" {
		t.Errorf("After override, Provider = %q, want %q", cfg.Provider, "gemini")
	}
}
