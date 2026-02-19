// Package providers implements the Reviewer interface for each supported LLM
// provider.
//
// Supported providers: Anthropic (Claude), OpenAI (GPT), Google (Gemini), and
// Ollama / LMStudio for local models.
//
// All providers share a common retry helper with exponential back-off and
// rate-limit handling. HTTP clients are injected via a transport field so that
// tests can redirect calls to local httptest servers without making live API
// requests.
//
// Use [New] to obtain a Reviewer by provider name and model string.
package providers
