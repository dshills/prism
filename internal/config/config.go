package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Config represents the prism configuration.
type Config struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Compare  []string `json:"compare,omitempty"`
	Format   string   `json:"format"`
	FailOn   string   `json:"failOn"`
	// MinSeverity is the lowest severity reported: the model is told not to
	// write anything below it, and anything below it is dropped. Empty or
	// "none" reports every severity. Agents usually set it to FailOn, since
	// findings below the gate cost output tokens and decide nothing.
	MinSeverity  string   `json:"minSeverity,omitempty"`
	MaxFindings  int      `json:"maxFindings"`
	ContextLines int      `json:"contextLines"`
	Include      []string `json:"include"`
	Exclude      []string `json:"exclude"`
	MaxDiffBytes int      `json:"maxDiffBytes"`
	ChunkBytes   int      `json:"chunkBytes,omitempty"`
	// VerifyFindings checks findings against the code before reporting them
	// (evidence and Go compile claims). nil means the default, true; a pointer
	// so an explicit false in a config file is distinguishable from unset.
	VerifyFindings *bool `json:"verifyFindings,omitempty"`
	// AutoExclude leaves out files not worth reviewing (lockfiles, generated
	// code, minified assets, snapshots, deletions). nil means the default,
	// true.
	AutoExclude *bool `json:"autoExclude,omitempty"`
	// FunctionContext widens each hunk to its whole enclosing function, file
	// by file within a growth limit. nil means the default, true.
	FunctionContext *bool  `json:"functionContext,omitempty"`
	MaxConcurrency  int    `json:"maxConcurrency,omitempty"`
	RateLimitRPM    int    `json:"rateLimitRpm,omitempty"`
	RulesFile       string `json:"rulesFile,omitempty"`
	// BaselineFile is the baseline of accepted findings, relative to the
	// repository root unless absolute. Empty means .prism-baseline.json;
	// "none" turns the baseline off.
	BaselineFile string `json:"baselineFile,omitempty"`
	// Prices sets models' prices for the cost estimate, keyed by
	// "provider:model", in US dollars per million tokens. They add to and
	// override prism's built-in prices.
	Prices map[string]Price `json:"prices,omitempty"`
	// Fallback is a "provider:model" to review with when the primary
	// provider fails (auth, retries exhausted, unreachable). Empty: none.
	Fallback string        `json:"fallback,omitempty"`
	Cache    CacheConfig   `json:"cache"`
	Privacy  PrivacyConfig `json:"privacy"`
}

// Price is a model's price in US dollars per million tokens.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// CacheConfig controls caching behavior.
type CacheConfig struct {
	Enabled    bool   `json:"enabled"`
	Dir        string `json:"dir,omitempty"`
	TTLSeconds int    `json:"ttlSeconds"`
}

// PrivacyConfig controls privacy/redaction behavior.
type PrivacyConfig struct {
	RedactSecrets bool     `json:"redactSecrets"`
	RedactPaths   []string `json:"redactPaths,omitempty"`
}

// Default returns a Config with all defaults applied.
func Default() Config {
	return Config{
		Provider:     "anthropic",
		Model:        "claude-sonnet-4-6",
		Format:       "text",
		FailOn:       "none",
		MaxFindings:  50,
		ContextLines: 3,
		Include:      []string{"**/*"},
		Exclude:      []string{"vendor/**", "**/*.gen.go", "**/dist/**"},
		MaxDiffBytes: 500000,
		ChunkBytes:   24000,
		Cache: CacheConfig{
			Enabled:    true,
			TTLSeconds: 86400,
		},
		Privacy: PrivacyConfig{
			RedactSecrets: true,
			RedactPaths:   []string{"**/.env", "**/*secrets*"},
		},
	}
}

// ConfigDir returns the platform-appropriate config directory for prism.
func ConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "prism"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "prism"), nil
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "prism"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "prism"), nil
	default:
		return filepath.Join(home, ".config", "prism"), nil
	}
}

// ConfigPath returns the full path to the config file.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// LoadFile loads config from the config file. Returns zero Config and nil error if file doesn't exist.
func LoadFile() (Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("reading config file: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config file: %w", err)
	}
	return cfg, nil
}

// Save writes the config to the config file.
func Save(cfg Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// Load builds the effective config by merging: defaults <- file <- env <- overrides.
// The overrides map comes from CLI flags (only non-zero values should be set).
func Load(overrides map[string]string) (Config, error) {
	cfg := Default()

	fileCfg, err := LoadFile()
	if err != nil {
		return Config{}, err
	}
	mergeFile(&cfg, fileCfg)
	if err := mergeEnv(&cfg); err != nil {
		return Config{}, err
	}
	mergeOverrides(&cfg, overrides)
	if err := checkSeverityLevel("minSeverity", cfg.MinSeverity); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// checkSeverityLevel rejects a severity level that is not none, low, medium
// or high (empty is unset).
func checkSeverityLevel(key, v string) error {
	switch v {
	case "", "none", "low", "medium", "high":
		return nil
	}
	return fmt.Errorf("%s must be none, low, medium or high, got %q", key, v)
}

func mergeFile(dst *Config, src Config) {
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if len(src.Compare) > 0 {
		dst.Compare = src.Compare
	}
	if src.Format != "" {
		dst.Format = src.Format
	}
	if src.FailOn != "" {
		dst.FailOn = src.FailOn
	}
	if src.MinSeverity != "" {
		dst.MinSeverity = src.MinSeverity
	}
	if src.MaxFindings > 0 {
		dst.MaxFindings = src.MaxFindings
	}
	if src.ContextLines > 0 {
		dst.ContextLines = src.ContextLines
	}
	if len(src.Include) > 0 {
		dst.Include = src.Include
	}
	if len(src.Exclude) > 0 {
		dst.Exclude = src.Exclude
	}
	if src.MaxDiffBytes > 0 {
		dst.MaxDiffBytes = src.MaxDiffBytes
	}
	if src.ChunkBytes > 0 {
		dst.ChunkBytes = src.ChunkBytes
	}
	if src.VerifyFindings != nil {
		v := *src.VerifyFindings
		dst.VerifyFindings = &v
	}
	if src.AutoExclude != nil {
		v := *src.AutoExclude
		dst.AutoExclude = &v
	}
	if src.FunctionContext != nil {
		v := *src.FunctionContext
		dst.FunctionContext = &v
	}
	if src.MaxConcurrency > 0 {
		dst.MaxConcurrency = src.MaxConcurrency
	}
	if src.RateLimitRPM > 0 {
		dst.RateLimitRPM = src.RateLimitRPM
	}
	if src.RulesFile != "" {
		dst.RulesFile = src.RulesFile
	}
	if src.BaselineFile != "" {
		dst.BaselineFile = src.BaselineFile
	}
	if src.Fallback != "" {
		dst.Fallback = src.Fallback
	}
	if len(src.Prices) > 0 {
		dst.Prices = src.Prices
	}
	if src.Cache.Dir != "" {
		dst.Cache.Dir = src.Cache.Dir
	}
	if src.Cache.TTLSeconds > 0 {
		dst.Cache.TTLSeconds = src.Cache.TTLSeconds
	}
	// Bool fields: JSON zero value for bool is false, so we can't distinguish
	// "unset" from "explicitly false" without custom unmarshaling. Use a heuristic:
	// if the file had any non-zero field, it was loaded and we trust its booleans.
	fileLoaded := src.Provider != "" || src.Model != "" || src.Format != "" || len(src.Compare) > 0 ||
		src.MaxFindings > 0 || src.ContextLines > 0 || src.MaxDiffBytes > 0 || src.Cache.Dir != "" || src.Cache.TTLSeconds > 0
	if fileLoaded {
		dst.Cache.Enabled = src.Cache.Enabled
		dst.Privacy.RedactSecrets = src.Privacy.RedactSecrets
	}
	if len(src.Privacy.RedactPaths) > 0 {
		dst.Privacy.RedactPaths = src.Privacy.RedactPaths
	}
}

func mergeEnv(cfg *Config) error {
	if v := os.Getenv("PRISM_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("PRISM_MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("PRISM_FAIL_ON"); v != "" {
		cfg.FailOn = v
	}
	if v := os.Getenv("PRISM_FORMAT"); v != "" {
		cfg.Format = v
	}
	if v := os.Getenv("PRISM_MIN_SEVERITY"); v != "" {
		cfg.MinSeverity = v
	}
	if v := os.Getenv("PRISM_MAX_FINDINGS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("PRISM_MAX_FINDINGS must be an integer, got %q", v)
		}
		cfg.MaxFindings = n
	}
	if v := os.Getenv("PRISM_CONTEXT_LINES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("PRISM_CONTEXT_LINES must be an integer, got %q", v)
		}
		cfg.ContextLines = n
	}
	if v := os.Getenv("PRISM_BASELINE_FILE"); v != "" {
		cfg.BaselineFile = v
	}
	if v := os.Getenv("PRISM_FALLBACK"); v != "" {
		cfg.Fallback = v
	}
	if v := os.Getenv("PRISM_VERIFY_FINDINGS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("PRISM_VERIFY_FINDINGS must be true or false, got %q", v)
		}
		cfg.VerifyFindings = &b
	}
	if v := os.Getenv("PRISM_FUNCTION_CONTEXT"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("PRISM_FUNCTION_CONTEXT must be true or false, got %q", v)
		}
		cfg.FunctionContext = &b
	}
	if v := os.Getenv("PRISM_AUTO_EXCLUDE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("PRISM_AUTO_EXCLUDE must be true or false, got %q", v)
		}
		cfg.AutoExclude = &b
	}
	if v := os.Getenv("PRISM_CHUNK_BYTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("PRISM_CHUNK_BYTES must be an integer, got %q", v)
		}
		cfg.ChunkBytes = n
	}
	if v := os.Getenv("PRISM_MAX_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("PRISM_MAX_CONCURRENCY must be an integer, got %q", v)
		}
		cfg.MaxConcurrency = n
	}
	if v := os.Getenv("PRISM_RATE_LIMIT_RPM"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("PRISM_RATE_LIMIT_RPM must be an integer, got %q", v)
		}
		cfg.RateLimitRPM = n
	}
	return nil
}

func mergeOverrides(cfg *Config, overrides map[string]string) {
	if overrides == nil {
		return
	}
	if v, ok := overrides["provider"]; ok && v != "" {
		cfg.Provider = v
	}
	if v, ok := overrides["model"]; ok && v != "" {
		cfg.Model = v
	}
	if v, ok := overrides["format"]; ok && v != "" {
		cfg.Format = v
	}
	if v, ok := overrides["failOn"]; ok && v != "" {
		cfg.FailOn = v
	}
	if v, ok := overrides["minSeverity"]; ok && v != "" {
		cfg.MinSeverity = v
	}
	if v, ok := overrides["maxFindings"]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxFindings = n
		}
	}
	if v, ok := overrides["contextLines"]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ContextLines = n
		}
	}
	if v, ok := overrides["maxDiffBytes"]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxDiffBytes = n
		}
	}
	if v, ok := overrides["functionContext"]; ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.FunctionContext = &b
		}
	}
	if v, ok := overrides["autoExclude"]; ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.AutoExclude = &b
		}
	}
	if v, ok := overrides["verifyFindings"]; ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.VerifyFindings = &b
		}
	}
	if v, ok := overrides["chunkBytes"]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ChunkBytes = n
		}
	}
	if v, ok := overrides["rulesFile"]; ok && v != "" {
		cfg.RulesFile = v
	}
	if v, ok := overrides["baselineFile"]; ok && v != "" {
		cfg.BaselineFile = v
	}
	if v, ok := overrides["fallback"]; ok && v != "" {
		cfg.Fallback = v
	}
	if v, ok := overrides["compare"]; ok && v != "" {
		cfg.Compare = strings.Split(v, ",")
	}
}

// SetField sets a single config field by key name. Returns error if key is unknown.
func SetField(cfg *Config, key, value string) error {
	switch key {
	case "provider":
		cfg.Provider = value
	case "model":
		cfg.Model = value
	case "format":
		cfg.Format = value
	case "failOn":
		cfg.FailOn = value
	case "minSeverity":
		if err := checkSeverityLevel("minSeverity", value); err != nil {
			return err
		}
		cfg.MinSeverity = value
	case "maxFindings":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("maxFindings must be an integer: %w", err)
		}
		cfg.MaxFindings = n
	case "contextLines":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("contextLines must be an integer: %w", err)
		}
		cfg.ContextLines = n
	case "maxDiffBytes":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("maxDiffBytes must be an integer: %w", err)
		}
		cfg.MaxDiffBytes = n
	case "functionContext":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("functionContext must be true or false: %w", err)
		}
		cfg.FunctionContext = &b
	case "autoExclude":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("autoExclude must be true or false: %w", err)
		}
		cfg.AutoExclude = &b
	case "verifyFindings":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("verifyFindings must be true or false: %w", err)
		}
		cfg.VerifyFindings = &b
	case "chunkBytes":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("chunkBytes must be an integer: %w", err)
		}
		cfg.ChunkBytes = n
	case "rulesFile":
		cfg.RulesFile = value
	case "baselineFile":
		cfg.BaselineFile = value
	case "fallback":
		cfg.Fallback = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// ShouldVerifyFindings reports whether findings are verified against the code
// before being reported; true unless explicitly turned off.
func (c Config) ShouldVerifyFindings() bool {
	return c.VerifyFindings == nil || *c.VerifyFindings
}

// ShouldFunctionContext reports whether hunks are widened to their
// enclosing functions; true unless explicitly turned off.
func (c Config) ShouldFunctionContext() bool {
	return c.FunctionContext == nil || *c.FunctionContext
}

// ShouldAutoExclude reports whether files not worth reviewing are left out;
// true unless explicitly turned off.
func (c Config) ShouldAutoExclude() bool {
	return c.AutoExclude == nil || *c.AutoExclude
}
