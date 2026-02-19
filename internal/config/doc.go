// Package config loads and merges prism configuration from multiple sources.
//
// Precedence (highest to lowest):
//  1. CLI flags
//  2. Environment variables (PRISM_PROVIDER, PRISM_MODEL, PRISM_FAIL_ON, etc.)
//  3. Config file ($XDG_CONFIG_HOME/prism/config.json)
//  4. Built-in defaults
//
// Use [Load] to obtain a merged [Config], [Init] to write a default config
// file, and [Set] to update a single key in the config file.
package config
