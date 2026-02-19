// Package cli wires together the Cobra command tree for the prism binary.
//
// It defines the root command and all subcommands (review, config, models,
// cache, hook, version), binds flags, reads configuration, invokes the review
// engine, and returns deterministic exit codes for CI gating.
package cli
