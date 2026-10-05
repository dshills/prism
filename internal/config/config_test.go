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
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, v)
			}
		}
	}()

	if err := os.Setenv("PRISM_PROVIDER", "openai"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if err := os.Setenv("PRISM_MODEL", "gpt-4o"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if err := os.Setenv("PRISM_FAIL_ON", "high"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if err := os.Setenv("PRISM_FORMAT", "json"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if err := os.Setenv("PRISM_MAX_FINDINGS", "10"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if err := os.Setenv("PRISM_CONTEXT_LINES", "5"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	cfg := Default()
	if err := mergeEnv(&cfg); err != nil {
		t.Fatalf("mergeEnv error: %v", err)
	}

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
			_ = os.Unsetenv("PRISM_PROVIDER")
		} else {
			_ = os.Setenv("PRISM_PROVIDER", orig)
		}
	}()

	if err := os.Setenv("PRISM_PROVIDER", "openai"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	cfg := Default()
	if err := mergeEnv(&cfg); err != nil {
		t.Fatalf("mergeEnv error: %v", err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("After env merge, Provider = %q, want %q", cfg.Provider, "openai")
	}

	mergeOverrides(&cfg, map[string]string{"provider": "gemini"})
	if cfg.Provider != "gemini" {
		t.Errorf("After override, Provider = %q, want %q", cfg.Provider, "gemini")
	}
}

func TestMergeFile_BoolFields(t *testing.T) {
	// When a config file is loaded (has non-zero fields), its booleans should be trusted
	dst := Default()
	src := Config{
		Provider: "openai",
		Cache:    CacheConfig{Enabled: false},
		Privacy:  PrivacyConfig{RedactSecrets: false},
	}
	mergeFile(&dst, src)

	if dst.Cache.Enabled != false {
		t.Error("Cache.Enabled should be false when file explicitly sets it")
	}
	if dst.Privacy.RedactSecrets != false {
		t.Error("RedactSecrets should be false when file explicitly sets it")
	}
}

func TestMergeFile_BoolFields_EmptyFile(t *testing.T) {
	// When file has no non-zero fields, defaults should be preserved
	dst := Default()
	src := Config{} // empty file
	mergeFile(&dst, src)

	if !dst.Cache.Enabled {
		t.Error("Cache.Enabled should remain true when file is empty")
	}
	if !dst.Privacy.RedactSecrets {
		t.Error("RedactSecrets should remain true when file is empty")
	}
}

func TestMergeFile_AllFields(t *testing.T) {
	dst := Default()
	src := Config{
		Provider:     "openai",
		Model:        "gpt-4o",
		Compare:      []string{"anthropic:claude", "openai:gpt-4o"},
		Format:       "json",
		FailOn:       "high",
		MaxFindings:  100,
		ContextLines: 10,
		Include:      []string{"*.go"},
		Exclude:      []string{"test/**"},
		MaxDiffBytes: 1000000,
		RulesFile:    "rules.json",
		Cache: CacheConfig{
			Dir:        "/tmp/cache",
			TTLSeconds: 3600,
		},
		Privacy: PrivacyConfig{
			RedactPaths: []string{"**/.secret"},
		},
	}
	mergeFile(&dst, src)

	if dst.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", dst.Provider, "openai")
	}
	if dst.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", dst.Model, "gpt-4o")
	}
	if len(dst.Compare) != 2 {
		t.Errorf("Compare len = %d, want 2", len(dst.Compare))
	}
	if dst.Format != "json" {
		t.Errorf("Format = %q, want %q", dst.Format, "json")
	}
	if dst.MaxFindings != 100 {
		t.Errorf("MaxFindings = %d, want 100", dst.MaxFindings)
	}
	if dst.ContextLines != 10 {
		t.Errorf("ContextLines = %d, want 10", dst.ContextLines)
	}
	if dst.MaxDiffBytes != 1000000 {
		t.Errorf("MaxDiffBytes = %d, want 1000000", dst.MaxDiffBytes)
	}
	if dst.RulesFile != "rules.json" {
		t.Errorf("RulesFile = %q, want %q", dst.RulesFile, "rules.json")
	}
	if dst.Cache.Dir != "/tmp/cache" {
		t.Errorf("Cache.Dir = %q, want %q", dst.Cache.Dir, "/tmp/cache")
	}
	if dst.Cache.TTLSeconds != 3600 {
		t.Errorf("Cache.TTLSeconds = %d, want 3600", dst.Cache.TTLSeconds)
	}
}

func TestMergeEnv_InvalidMaxFindings(t *testing.T) {
	orig := os.Getenv("PRISM_MAX_FINDINGS")
	defer func() {
		if orig == "" {
			_ = os.Unsetenv("PRISM_MAX_FINDINGS")
		} else {
			_ = os.Setenv("PRISM_MAX_FINDINGS", orig)
		}
	}()

	if err := os.Setenv("PRISM_MAX_FINDINGS", "notanumber"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	cfg := Default()
	err := mergeEnv(&cfg)
	if err == nil {
		t.Error("Expected error for invalid PRISM_MAX_FINDINGS")
	}
}

func TestMergeEnv_InvalidContextLines(t *testing.T) {
	orig := os.Getenv("PRISM_CONTEXT_LINES")
	defer func() {
		if orig == "" {
			_ = os.Unsetenv("PRISM_CONTEXT_LINES")
		} else {
			_ = os.Setenv("PRISM_CONTEXT_LINES", orig)
		}
	}()

	if err := os.Setenv("PRISM_CONTEXT_LINES", "abc"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	cfg := Default()
	err := mergeEnv(&cfg)
	if err == nil {
		t.Error("Expected error for invalid PRISM_CONTEXT_LINES")
	}
}

func TestMergeOverrides_Compare(t *testing.T) {
	cfg := Default()
	mergeOverrides(&cfg, map[string]string{
		"compare": "anthropic:claude,openai:gpt-4o",
	})
	if len(cfg.Compare) != 2 {
		t.Fatalf("Compare len = %d, want 2", len(cfg.Compare))
	}
	if cfg.Compare[0] != "anthropic:claude" {
		t.Errorf("Compare[0] = %q, want %q", cfg.Compare[0], "anthropic:claude")
	}
}

func TestMergeOverrides_AllNumericFields(t *testing.T) {
	cfg := Default()
	mergeOverrides(&cfg, map[string]string{
		"contextLines": "10",
		"maxDiffBytes": "2000000",
		"rulesFile":    "my-rules.json",
	})
	if cfg.ContextLines != 10 {
		t.Errorf("ContextLines = %d, want 10", cfg.ContextLines)
	}
	if cfg.MaxDiffBytes != 2000000 {
		t.Errorf("MaxDiffBytes = %d, want 2000000", cfg.MaxDiffBytes)
	}
	if cfg.RulesFile != "my-rules.json" {
		t.Errorf("RulesFile = %q, want %q", cfg.RulesFile, "my-rules.json")
	}
}

func TestConfigDir_XDG(t *testing.T) {
	orig := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		if orig == "" {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			_ = os.Setenv("XDG_CONFIG_HOME", orig)
		}
	}()

	if err := os.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir error: %v", err)
	}
	if dir != "/tmp/xdg-test/prism" {
		t.Errorf("ConfigDir = %q, want %q", dir, "/tmp/xdg-test/prism")
	}
}

func TestConfigPath(t *testing.T) {
	orig := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		if orig == "" {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			_ = os.Setenv("XDG_CONFIG_HOME", orig)
		}
	}()

	if err := os.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath error: %v", err)
	}
	if path != "/tmp/xdg-test/prism/config.json" {
		t.Errorf("ConfigPath = %q, want %q", path, "/tmp/xdg-test/prism/config.json")
	}
}

func TestSaveAndLoadFile(t *testing.T) {
	tmpDir := t.TempDir()

	orig := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		if orig == "" {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			_ = os.Setenv("XDG_CONFIG_HOME", orig)
		}
	}()
	if err := os.Setenv("XDG_CONFIG_HOME", tmpDir); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	cfg := Default()
	cfg.Provider = "openai"
	cfg.Model = "gpt-4o"
	cfg.MaxFindings = 25

	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := LoadFile()
	if err != nil {
		t.Fatalf("LoadFile error: %v", err)
	}
	if loaded.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", loaded.Provider, "openai")
	}
	if loaded.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", loaded.Model, "gpt-4o")
	}
	if loaded.MaxFindings != 25 {
		t.Errorf("MaxFindings = %d, want 25", loaded.MaxFindings)
	}
}

func TestLoadFile_NoFile(t *testing.T) {
	tmpDir := t.TempDir()

	orig := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		if orig == "" {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			_ = os.Setenv("XDG_CONFIG_HOME", orig)
		}
	}()
	if err := os.Setenv("XDG_CONFIG_HOME", tmpDir); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	cfg, err := LoadFile()
	if err != nil {
		t.Fatalf("LoadFile error: %v", err)
	}
	// Should return zero config, not defaults
	if cfg.Provider != "" {
		t.Errorf("Provider should be empty for missing file, got %q", cfg.Provider)
	}
}

func TestLoad_Integration(t *testing.T) {
	tmpDir := t.TempDir()

	orig := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		if orig == "" {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			_ = os.Setenv("XDG_CONFIG_HOME", orig)
		}
	}()
	if err := os.Setenv("XDG_CONFIG_HOME", tmpDir); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	// No config file — should get defaults + overrides
	cfg, err := Load(map[string]string{"provider": "openai"})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "openai")
	}
	// Defaults should be preserved for unset fields
	if cfg.MaxFindings != 50 {
		t.Errorf("MaxFindings = %d, want 50 (default)", cfg.MaxFindings)
	}
}

// chunkBytes: default, environment, config key and CLI-style override.
func TestChunkBytes(t *testing.T) {
	if got := Default().ChunkBytes; got != 24000 {
		t.Errorf("Default ChunkBytes = %d, want 24000", got)
	}

	t.Setenv("PRISM_CHUNK_BYTES", "12000")
	cfg := Default()
	if err := mergeEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ChunkBytes != 12000 {
		t.Errorf("PRISM_CHUNK_BYTES: ChunkBytes = %d, want 12000", cfg.ChunkBytes)
	}

	t.Setenv("PRISM_CHUNK_BYTES", "lots")
	if err := mergeEnv(&cfg); err == nil {
		t.Error("PRISM_CHUNK_BYTES=lots should be an error")
	}

	if err := SetField(&cfg, "chunkBytes", "30000"); err != nil || cfg.ChunkBytes != 30000 {
		t.Errorf("SetField chunkBytes: %v, ChunkBytes = %d", err, cfg.ChunkBytes)
	}
	if err := SetField(&cfg, "chunkBytes", "x"); err == nil {
		t.Error("SetField chunkBytes=x should be an error")
	}

	mergeOverrides(&cfg, map[string]string{"chunkBytes": "8000"})
	if cfg.ChunkBytes != 8000 {
		t.Errorf("override: ChunkBytes = %d, want 8000", cfg.ChunkBytes)
	}

	dst := Default()
	mergeFile(&dst, Config{ChunkBytes: 16000})
	if dst.ChunkBytes != 16000 {
		t.Errorf("config file: ChunkBytes = %d, want 16000", dst.ChunkBytes)
	}
}

// verifyFindings: on by default; off via env, override, config key or file.
func TestVerifyFindings(t *testing.T) {
	if !Default().ShouldVerifyFindings() {
		t.Error("verification should be on by default")
	}
	t.Setenv("PRISM_VERIFY_FINDINGS", "false")
	cfg := Default()
	if err := mergeEnv(&cfg); err != nil || cfg.ShouldVerifyFindings() {
		t.Errorf("PRISM_VERIFY_FINDINGS=false: err %v, verify %v", err, cfg.ShouldVerifyFindings())
	}
	t.Setenv("PRISM_VERIFY_FINDINGS", "maybe")
	if err := mergeEnv(&cfg); err == nil {
		t.Error("PRISM_VERIFY_FINDINGS=maybe should be an error")
	}
	cfg = Default()
	mergeOverrides(&cfg, map[string]string{"verifyFindings": "false"})
	if cfg.ShouldVerifyFindings() {
		t.Error("override verifyFindings=false ignored")
	}
	if err := SetField(&cfg, "verifyFindings", "true"); err != nil || !cfg.ShouldVerifyFindings() {
		t.Errorf("SetField: %v", err)
	}
	off := false
	dst := Default()
	mergeFile(&dst, Config{VerifyFindings: &off})
	if dst.ShouldVerifyFindings() {
		t.Error("config file verifyFindings=false ignored")
	}
	dst2 := Default()
	mergeFile(&dst2, Config{})
	if !dst2.ShouldVerifyFindings() {
		t.Error("an unset file value must not turn verification off")
	}
}

// minSeverity: config file, environment, CLI-style override and config key,
// each rejected when it is not a severity level.
func TestMinSeverity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PRISM_MIN_SEVERITY", "")

	cfg, err := Load(nil)
	if err != nil || cfg.MinSeverity != "" {
		t.Fatalf("default: MinSeverity = %q, %v; want unset", cfg.MinSeverity, err)
	}
	if err := Save(Config{Provider: "openai", MinSeverity: "low"}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = Load(nil); cfg.MinSeverity != "low" {
		t.Errorf("config file: MinSeverity = %q, want low", cfg.MinSeverity)
	}
	t.Setenv("PRISM_MIN_SEVERITY", "medium")
	if cfg, _ = Load(nil); cfg.MinSeverity != "medium" {
		t.Errorf("environment: MinSeverity = %q, want medium", cfg.MinSeverity)
	}
	if cfg, _ = Load(map[string]string{"minSeverity": "high"}); cfg.MinSeverity != "high" {
		t.Errorf("override: MinSeverity = %q, want high", cfg.MinSeverity)
	}
	if _, err := Load(map[string]string{"minSeverity": "critical"}); err == nil {
		t.Error("override critical: no error")
	}
	t.Setenv("PRISM_MIN_SEVERITY", "Medium")
	if _, err := Load(nil); err == nil {
		t.Error("environment Medium: no error")
	}

	var c Config
	if err := SetField(&c, "minSeverity", "high"); err != nil || c.MinSeverity != "high" {
		t.Errorf("SetField high: %q, %v", c.MinSeverity, err)
	}
	if err := SetField(&c, "minSeverity", "urgent"); err == nil || c.MinSeverity != "high" {
		t.Errorf("SetField urgent: %q, %v; want an error and no change", c.MinSeverity, err)
	}
}
