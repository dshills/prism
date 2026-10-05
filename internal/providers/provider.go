package providers

import (
	"context"
	"fmt"
)

// ReviewRequest contains the data sent to an LLM for review.
type ReviewRequest struct {
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
	Temperature  float64
	// Output, when set, asks for a response matching its schema in the
	// provider's structured-output mode (see Output).
	Output *Output
}

// ReviewResponse contains the raw response from an LLM.
type ReviewResponse struct {
	Content    string
	TokensUsed int
	// Provider identifies the LLM vendor that produced this response
	// (e.g. "anthropic", "openai", "gemini", "ollama").
	Provider string
	// Model identifies the concrete model used (e.g. "claude-opus-4-5").
	Model string
	// Fallback is true when a Fallback reviewer's fallback provider, not the
	// configured one, produced this response.
	Fallback bool
	// Usage is the tokens the call used, also on a cut-off response.
	Usage Usage
	// Calls is how many model calls the response took, when more than one: a
	// Fallback reviewer's failed call to its primary, then the fallback. Zero
	// means one. Retries of one call inside a provider are not counted.
	Calls int
}

// Usage is the tokens one model call used, as the provider reported them.
type Usage struct {
	// InputTokens is all input, including any read from a prompt cache.
	InputTokens int `json:"input"`
	// OutputTokens is all output, including hidden reasoning.
	OutputTokens int `json:"output"`
	// ReasoningTokens is the part of OutputTokens spent on hidden reasoning,
	// where the provider reports it.
	ReasoningTokens int `json:"reasoning,omitempty"`
	// CachedInputTokens is the part of InputTokens read from a prompt
	// cache, where the provider reports it.
	CachedInputTokens int `json:"cachedInput,omitempty"`
}

// Add adds o to u.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.ReasoningTokens += o.ReasoningTokens
	u.CachedInputTokens += o.CachedInputTokens
}

// CallsOf is how many model calls a Review made, from its response.
func CallsOf(resp ReviewResponse) int {
	return max(resp.Calls, 1)
}

// Reviewer is the provider abstraction interface.
type Reviewer interface {
	Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
	Name() string
}

// New creates a provider by name.
func New(provider, model string) (Reviewer, error) {
	switch provider {
	case "anthropic":
		return NewAnthropic(model)
	case "openai":
		return NewOpenAI(model)
	case "gemini", "google":
		return NewGemini(model)
	case "ollama", "lmstudio":
		return NewOllama(model)
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}
