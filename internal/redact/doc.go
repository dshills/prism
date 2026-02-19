// Package redact removes secrets from diff content before it is sent to any
// LLM provider.
//
// Detection uses regex heuristics covering common secret shapes: API keys,
// JWTs, private keys, AWS access key IDs and secret access keys, bearer
// tokens, database connection strings, and provider-specific tokens
// (Anthropic, OpenAI, GitHub, Slack).
//
// Path-based redaction is also supported: files whose paths match configured
// glob patterns have their entire content replaced with [REDACTED] rather than
// being scanned line by line.
package redact
