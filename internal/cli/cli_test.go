package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dshills/prism/internal/config"
)

// resetFlags resets all package-level flag variables to their zero values.
func resetFlags() {
	flagPaths = ""
	flagExclude = ""
	flagContextLines = 0
	flagMaxDiffBytes = 0
	flagProvider = ""
	flagModel = ""
	flagCompare = ""
	flagFormat = ""
	flagOut = ""
	flagFailOn = ""
	flagMaxFindings = 0
	flagRules = ""
	flagNoRedact = false
	flagParent = ""
	flagMergeBase = false
	flagSnippetPath = ""
	flagSnippetLang = ""
	flagSnippetBase = ""
	flagGHOwner = ""
	flagGHRepo = ""
	flagGHDryRun = false
}

// --- splitComma tests ---

func TestSplitComma(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty string", "", nil},
		{"single value", "foo", []string{"foo"}},
		{"multiple values", "a,b,c", []string{"a", "b", "c"}},
		{"whitespace trimmed", " a , b , c ", []string{"a", "b", "c"}},
		{"empty parts skipped", "a,,b", []string{"a", "b"}},
		{"all empty", ",,,", nil},
		{"trailing comma", "a,b,", []string{"a", "b"}},
		{"leading comma", ",a,b", []string{"a", "b"}},
		{"glob patterns", "*.go,src/**/*.ts", []string{"*.go", "src/**/*.ts"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitComma(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("splitComma(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitComma(%q)[%d] = %q, want %q",
						tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- buildOverrides tests ---

func TestBuildOverrides_NoFlags(t *testing.T) {
	resetFlags()
	m := buildOverrides()
	if len(m) != 0 {
		t.Errorf("buildOverrides() with no flags = %v, want empty map", m)
	}
}

func TestBuildOverrides_AllFlags(t *testing.T) {
	resetFlags()
	flagProvider = "openai"
	flagModel = "gpt-4o"
	flagFormat = "json"
	flagFailOn = "high"
	flagMaxFindings = 10
	flagContextLines = 5
	flagMaxDiffBytes = 1000
	flagRules = "rules.yaml"
	flagCompare = "anthropic:claude,openai:gpt-4o"

	m := buildOverrides()

	expected := map[string]string{
		"provider":     "openai",
		"model":        "gpt-4o",
		"format":       "json",
		"failOn":       "high",
		"maxFindings":  "10",
		"contextLines": "5",
		"maxDiffBytes": "1000",
		"rulesFile":    "rules.yaml",
		"compare":      "anthropic:claude,openai:gpt-4o",
	}

	if len(m) != len(expected) {
		t.Fatalf("buildOverrides() returned %d entries, want %d", len(m), len(expected))
	}
	for k, v := range expected {
		if m[k] != v {
			t.Errorf("buildOverrides()[%q] = %q, want %q", k, m[k], v)
		}
	}
}

func TestBuildOverrides_PartialFlags(t *testing.T) {
	resetFlags()
	flagProvider = "gemini"
	flagFormat = "sarif"

	m := buildOverrides()

	if len(m) != 2 {
		t.Fatalf("buildOverrides() returned %d entries, want 2", len(m))
	}
	if m["provider"] != "gemini" {
		t.Errorf("provider = %q, want %q", m["provider"], "gemini")
	}
	if m["format"] != "sarif" {
		t.Errorf("format = %q, want %q", m["format"], "sarif")
	}
}

func TestBuildOverrides_ZeroIntsExcluded(t *testing.T) {
	resetFlags()
	flagProvider = "anthropic"
	flagMaxFindings = 0
	flagContextLines = 0
	flagMaxDiffBytes = 0

	m := buildOverrides()

	if _, ok := m["maxFindings"]; ok {
		t.Error("maxFindings=0 should not be in overrides")
	}
	if _, ok := m["contextLines"]; ok {
		t.Error("contextLines=0 should not be in overrides")
	}
	if _, ok := m["maxDiffBytes"]; ok {
		t.Error("maxDiffBytes=0 should not be in overrides")
	}
}

// --- buildDiffOpts tests ---

func TestBuildDiffOpts_FromConfig(t *testing.T) {
	resetFlags()
	cfg := config.Config{
		ContextLines: 5,
		MaxDiffBytes: 100000,
		Include:      []string{"*.go"},
		Exclude:      []string{"vendor/**"},
	}

	opts := buildDiffOpts(cfg)

	if opts.ContextLines != 5 {
		t.Errorf("ContextLines = %d, want 5", opts.ContextLines)
	}
	if opts.MaxDiffBytes != 100000 {
		t.Errorf("MaxDiffBytes = %d, want 100000", opts.MaxDiffBytes)
	}
	if len(opts.Include) != 1 || opts.Include[0] != "*.go" {
		t.Errorf("Include = %v, want [*.go]", opts.Include)
	}
	if len(opts.Exclude) != 1 || opts.Exclude[0] != "vendor/**" {
		t.Errorf("Exclude = %v, want [vendor/**]", opts.Exclude)
	}
}

func TestBuildDiffOpts_PathsFlagOverridesInclude(t *testing.T) {
	resetFlags()
	flagPaths = "src/**/*.go,lib/**/*.go"

	cfg := config.Config{
		ContextLines: 3,
		Include:      []string{"**/*"},
		Exclude:      []string{"vendor/**"},
	}

	opts := buildDiffOpts(cfg)

	if len(opts.Include) != 2 {
		t.Fatalf("Include has %d entries, want 2", len(opts.Include))
	}
	if opts.Include[0] != "src/**/*.go" || opts.Include[1] != "lib/**/*.go" {
		t.Errorf("Include = %v, want [src/**/*.go lib/**/*.go]", opts.Include)
	}
}

func TestBuildDiffOpts_ExcludeFlagAppends(t *testing.T) {
	resetFlags()
	flagExclude = "test/**,docs/**"

	cfg := config.Config{
		Exclude: []string{"vendor/**"},
	}

	opts := buildDiffOpts(cfg)

	if len(opts.Exclude) != 3 {
		t.Fatalf("Exclude has %d entries, want 3", len(opts.Exclude))
	}
	if opts.Exclude[0] != "vendor/**" {
		t.Errorf("Exclude[0] = %q, want %q", opts.Exclude[0], "vendor/**")
	}
	if opts.Exclude[1] != "test/**" {
		t.Errorf("Exclude[1] = %q, want %q", opts.Exclude[1], "test/**")
	}
	if opts.Exclude[2] != "docs/**" {
		t.Errorf("Exclude[2] = %q, want %q", opts.Exclude[2], "docs/**")
	}
}

func TestBuildDiffOpts_NoFlagOverrides(t *testing.T) {
	resetFlags()
	cfg := config.Config{
		ContextLines: 7,
		Include:      []string{"*.ts"},
		Exclude:      []string{"node_modules/**"},
	}

	opts := buildDiffOpts(cfg)

	if opts.ContextLines != 7 {
		t.Errorf("ContextLines = %d, want 7", opts.ContextLines)
	}
	if len(opts.Include) != 1 || opts.Include[0] != "*.ts" {
		t.Errorf("Include = %v, want [*.ts]", opts.Include)
	}
	if len(opts.Exclude) != 1 || opts.Exclude[0] != "node_modules/**" {
		t.Errorf("Exclude = %v, want [node_modules/**]", opts.Exclude)
	}
}

// --- version command tests ---

func TestVersionCmd_Execute(t *testing.T) {
	// versionCmd writes to os.Stdout directly, but we can verify it runs without error.
	err := versionCmd.Execute()
	if err != nil {
		t.Errorf("version command returned error: %v", err)
	}
}

// --- models list command tests ---

func TestModelsListCmd_Execute(t *testing.T) {
	// modelsListCmd writes to os.Stdout directly, but we can verify it runs without error.
	modelsCmd.SetArgs([]string{"list"})
	err := modelsCmd.Execute()
	if err != nil {
		t.Errorf("models list command returned error: %v", err)
	}
}

func TestKnownModels_AllProviders(t *testing.T) {
	providers := map[string]bool{
		"anthropic": false,
		"openai":    false,
		"gemini":    false,
		"ollama":    false,
	}

	for _, info := range knownModels {
		if _, ok := providers[info.Provider]; ok {
			providers[info.Provider] = true
		}
		if len(info.Models) == 0 {
			t.Errorf("provider %s has no models", info.Provider)
		}
	}

	for provider, found := range providers {
		if !found {
			t.Errorf("expected provider %q not found in knownModels", provider)
		}
	}
}

// --- config command tests ---

func TestConfigInit_CreatesFile(t *testing.T) {
	resetFlags()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configCmd.SetArgs([]string{"init"})
	err := configCmd.Execute()
	if err != nil {
		t.Fatalf("config init returned error: %v", err)
	}

	configPath := filepath.Join(tmpDir, "prism", "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("config init did not create config.json")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("cannot read config file: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config file is not valid JSON: %v", err)
	}
	if cfg.Provider == "" {
		t.Error("config file has empty provider")
	}
}

func TestConfigInit_AlreadyExists(t *testing.T) {
	resetFlags()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfgDir := filepath.Join(tmpDir, "prism")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"provider":"openai"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	configCmd.SetArgs([]string{"init"})
	err := configCmd.Execute()
	if err != nil {
		t.Fatalf("config init with existing file returned error: %v", err)
	}

	// Verify original content is preserved (not overwritten)
	data, err := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		t.Fatalf("cannot read config file: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("config init overwrote existing file: provider = %q, want %q", cfg.Provider, "openai")
	}
}

func TestConfigSet_UpdatesFile(t *testing.T) {
	resetFlags()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configCmd.SetArgs([]string{"set", "provider", "openai"})
	err := configCmd.Execute()
	if err != nil {
		t.Fatalf("config set returned error: %v", err)
	}

	configPath := filepath.Join(tmpDir, "prism", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("cannot read config file: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config file is not valid JSON: %v", err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("provider = %q, want %q", cfg.Provider, "openai")
	}
}

func TestConfigSet_InvalidKey(t *testing.T) {
	resetFlags()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configCmd.SetArgs([]string{"set", "unknownKey", "value"})
	err := configCmd.Execute()
	if err == nil {
		t.Error("config set with invalid key should return error")
	}
}

func TestConfigSet_MissingArgs(t *testing.T) {
	resetFlags()

	configCmd.SetArgs([]string{"set", "provider"})
	err := configCmd.Execute()
	if err == nil {
		t.Error("config set with 1 arg should return error (requires 2)")
	}
}

func TestConfigShow_Execute(t *testing.T) {
	resetFlags()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configCmd.SetArgs([]string{"show"})
	err := configCmd.Execute()
	if err != nil {
		t.Errorf("config show returned error: %v", err)
	}
}

// --- cache command tests ---

func TestCacheShow_Execute(t *testing.T) {
	resetFlags()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cacheCmd.SetArgs([]string{"show"})
	err := cacheCmd.Execute()
	if err != nil {
		t.Errorf("cache show returned error: %v", err)
	}
}

func TestCacheClear_Execute(t *testing.T) {
	resetFlags()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	// Create a fake cache entry
	cacheDir := filepath.Join(tmpDir, "prism")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "abc123.json"), []byte(`{"key":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cacheCmd.SetArgs([]string{"clear"})
	err := cacheCmd.Execute()
	if err != nil {
		t.Errorf("cache clear returned error: %v", err)
	}

	// Verify cache entry was removed
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("cannot read cache dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			t.Errorf("cache clear did not remove %s", e.Name())
		}
	}
}

// --- github command tests ---

func TestGithubCmd_InvalidPRNumber(t *testing.T) {
	resetFlags()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	savedExitCode := exitCode
	t.Cleanup(func() { exitCode = savedExitCode })
	exitCode = ExitSuccess

	githubCmd.SetArgs([]string{"abc"})
	err := githubCmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != ExitUsageError {
		t.Errorf("exitCode = %d, want %d (ExitUsageError)", exitCode, ExitUsageError)
	}
}

func TestGithubCmd_MissingArg(t *testing.T) {
	resetFlags()

	githubCmd.SetArgs([]string{})
	err := githubCmd.Execute()
	if err == nil {
		t.Error("github command without args should return error")
	}
}

// --- review command structure tests ---

func TestReviewCmd_HasSubcommands(t *testing.T) {
	expected := map[string]bool{
		"unstaged": false,
		"staged":   false,
		"commit":   false,
		"range":    false,
		"snippet":  false,
	}

	for _, sub := range reviewCmd.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("review subcommand %q not found", name)
		}
	}
}

func TestReviewCommitCmd_MissingArg(t *testing.T) {
	resetFlags()

	reviewCmd.SetArgs([]string{"commit"})
	err := reviewCmd.Execute()
	if err == nil {
		t.Error("review commit without SHA arg should return error")
	}
}

func TestReviewRangeCmd_MissingArg(t *testing.T) {
	resetFlags()

	reviewCmd.SetArgs([]string{"range"})
	err := reviewCmd.Execute()
	if err == nil {
		t.Error("review range without arg should return error")
	}
}

// --- exit code constants tests ---

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name string
		code int
		want int
	}{
		{"ExitSuccess", ExitSuccess, 0},
		{"ExitFindings", ExitFindings, 1},
		{"ExitUsageError", ExitUsageError, 2},
		{"ExitAuthError", ExitAuthError, 3},
		{"ExitRuntimeError", ExitRuntimeError, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.code != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.code, tt.want)
			}
		})
	}
}

// --- version constant test ---

func TestVersionConstant(t *testing.T) {
	if version == "" {
		t.Error("version constant is empty")
	}
}
