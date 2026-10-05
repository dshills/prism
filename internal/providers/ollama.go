package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultOllamaURL = "http://localhost:11434"

// Ollama implements the Reviewer interface for Ollama and LM Studio (OpenAI-compatible API).
type Ollama struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
	schema  schemaSupport
	effort  effortSupport
}

// NewOllama creates a new Ollama provider. No API key is required by default.
func NewOllama(model string) (*Ollama, error) {
	baseURL := os.Getenv("OLLAMA_HOST")
	if baseURL == "" {
		baseURL = defaultOllamaURL
	}

	// Normalize URL: strip trailing /, /v1, /v1/chat/completions
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1/chat/completions")
	baseURL = strings.TrimSuffix(baseURL, "/v1")

	// Optional API key for servers that require it (e.g., LM Studio)
	apiKey := os.Getenv("PRISM_OLLAMA_API_KEY")

	return &Ollama{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL + "/v1/chat/completions",
		client:  &http.Client{Timeout: 300 * time.Second},
	}, nil
}

func (o *Ollama) Name() string { return "ollama" }

func (o *Ollama) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	// A server without response_format or reasoning_effort refuses the
	// field and is asked again without it.
	return sendOptional(req, &o.schema, &o.effort, func(structured, withEffort bool) (ReviewResponse, error) {
		return o.review(ctx, req, structured, withEffort)
	})
}

// review sends one review request, asking for structured output when
// structured is true and setting the reasoning effort when withEffort is.
func (o *Ollama) review(ctx context.Context, req ReviewRequest, structured, withEffort bool) (ReviewResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	messages := []openaiMessage{
		{Role: "system", Content: req.SystemPrompt},
		{Role: "user", Content: req.UserPrompt},
	}

	body := openaiRequest{
		Model:     o.model,
		Messages:  messages,
		MaxTokens: maxTokens,
	}
	if req.Temperature > 0 {
		body.Temperature = &req.Temperature
	}
	if structured {
		body.ResponseFormat = openaiFormat(req.Output)
	}
	if withEffort {
		body.ReasoningEffort = req.Effort
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return ReviewResponse{}, fmt.Errorf("marshaling request: %w", err)
	}

	var resp ReviewResponse
	err = retryWithBackoff(ctx, 3, func() error {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if o.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
		}

		httpResp, err := o.client.Do(httpReq)
		if err != nil {
			return fmt.Errorf("sending request: %w", err)
		}
		defer func() { _ = httpResp.Body.Close() }()

		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}

		if httpResp.StatusCode == 429 {
			return newRateLimitError(httpResp.Header)
		}
		if httpResp.StatusCode == 401 || httpResp.StatusCode == 403 {
			return &authError{message: string(respBody)}
		}
		if httpResp.StatusCode >= 500 {
			return newServerError(httpResp.StatusCode, httpResp.Header, string(respBody))
		}
		if httpResp.StatusCode != 200 {
			return &requestError{status: httpResp.StatusCode, body: string(respBody)}
		}

		var result openaiResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		// Recorded before any check, so a cut-off answer reports its tokens.
		resp = ReviewResponse{Provider: o.Name(), Model: o.model, Usage: result.Usage.usage()}
		if len(result.Choices) == 0 {
			return fmt.Errorf("no choices in response")
		}
		if result.Choices[0].FinishReason == "length" {
			return &truncatedError{maxTokens: maxTokens}
		}
		if result.Choices[0].Message.Content == "" {
			return fmt.Errorf("empty text content in API response")
		}

		resp.Content = result.Choices[0].Message.Content
		resp.TokensUsed = result.Usage.TotalTokens
		return nil
	})

	return resp, err
}
