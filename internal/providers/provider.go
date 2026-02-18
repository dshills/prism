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
}

// ReviewResponse contains the raw response from an LLM.
type ReviewResponse struct {
	Content    string
	TokensUsed int
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
