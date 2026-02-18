package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// Config represents the prism configuration.
type Config struct {
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	Compare      []string      `json:"compare,omitempty"`
	Format       string        `json:"format"`
	FailOn       string        `json:"failOn"`
	MaxFindings  int           `json:"maxFindings"`
	ContextLines int           `json:"contextLines"`
	Include      []string      `json:"include"`
	Exclude      []string      `json:"exclude"`
	MaxDiffBytes int           `json:"maxDiffBytes"`
	RulesFile    string        `json:"rulesFile,omitempty"`
	Cache        CacheConfig   `json:"cache"`
	Privacy      PrivacyConfig `json:"privacy"`
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
		Model:        "claude-sonnet-4-20250514",
		Format:       "text",
		FailOn:       "none",
		MaxFindings:  50,
		ContextLines: 3,
		Include:      []string{"**/*"},
		Exclude:      []string{"vendor/**", "**/*.gen.go", "**/dist/**"},
		MaxDiffBytes: 500000,
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
	mergeEnv(&cfg)
	mergeOverrides(&cfg, overrides)

	return cfg, nil
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
	if src.RulesFile != "" {
		dst.RulesFile = src.RulesFile
	}
	if src.Cache.Dir != "" {
		dst.Cache.Dir = src.Cache.Dir
	}
	if src.Cache.TTLSeconds > 0 {
		dst.Cache.TTLSeconds = src.Cache.TTLSeconds
	}
	// Bool fields from file: only override if the file explicitly set them
	// Since JSON zero value for bool is false, we can't distinguish unset from false
	// in a simple merge. We'll trust the file value if the whole struct was loaded.
	dst.Cache.Enabled = src.Cache.Enabled || dst.Cache.Enabled
	if len(src.Privacy.RedactPaths) > 0 {
		dst.Privacy.RedactPaths = src.Privacy.RedactPaths
	}
}

func mergeEnv(cfg *Config) {
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
	if v := os.Getenv("PRISM_MAX_FINDINGS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxFindings = n
		}
	}
	if v := os.Getenv("PRISM_CONTEXT_LINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ContextLines = n
		}
	}
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
	if v, ok := overrides["rulesFile"]; ok && v != "" {
		cfg.RulesFile = v
	}
	if v, ok := overrides["out"]; ok && v != "" {
		// out is handled by CLI, not stored in config
		_ = v
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
	case "rulesFile":
		cfg.RulesFile = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}
