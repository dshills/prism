package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
)

// Anthropic implements the Reviewer interface for Anthropic's API.
type Anthropic struct {
	apiKey string
	model  string
	client *http.Client
	schema schemaSupport
	effort effortSupport
}

// NewAnthropic creates a new Anthropic provider.
func NewAnthropic(model string) (*Anthropic, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable is not set")
	}
	return &Anthropic{
		apiKey: key,
		model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func (a *Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	// A model without structured outputs or effort refuses them, and is
	// asked again without.
	return sendOptional(req, &a.schema, &a.effort, func(structured, withEffort bool) (ReviewResponse, error) {
		return a.review(ctx, req, structured, withEffort)
	})
}

// review sends one review request, asking for structured output when
// structured is true and setting the effort when withEffort is.
func (a *Anthropic) review(ctx context.Context, req ReviewRequest, structured, withEffort bool) (ReviewResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := anthropicRequest{
		Model:     a.model,
		MaxTokens: maxTokens,
		System:    req.SystemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: req.UserPrompt},
		},
	}
	if structured {
		// Structured outputs (JSON outputs): the response text is JSON that
		// matches the schema. Every object needs additionalProperties false,
		// as in OpenAI's strict mode. Models without the feature refuse the
		// request, and Review asks again without it.
		body.OutputConfig = &anthropicOutputConfig{
			Format: &anthropicFormat{Type: "json_schema", Schema: req.Output.Schema.jsonSchema(true)},
		}
	}
	if withEffort {
		// Effort sets how much the model thinks, on the models that think
		// by default and the ones that do not alike; thinking itself is left
		// to the model's default.
		if body.OutputConfig == nil {
			body.OutputConfig = &anthropicOutputConfig{}
		}
		body.OutputConfig.Effort = req.Effort
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return ReviewResponse{}, fmt.Errorf("marshaling request: %w", err)
	}

	var resp ReviewResponse
	err = retryWithBackoff(ctx, 3, func() error {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-api-key", a.apiKey)
		httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

		httpResp, err := a.client.Do(httpReq)
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

		var result anthropicResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		// Recorded before any check, so a cut-off answer reports its tokens.
		resp = ReviewResponse{Provider: a.Name(), Model: a.model, Usage: result.Usage.usage()}
		if result.StopReason == "max_tokens" {
			return &truncatedError{maxTokens: maxTokens}
		}
		var content string
		for _, block := range result.Content {
			if block.Type == "text" {
				content += block.Text
			}
		}
		if content == "" {
			return fmt.Errorf("empty text content in API response")
		}

		resp.Content = content
		resp.TokensUsed = resp.Usage.InputTokens + resp.Usage.OutputTokens
		return nil
	})

	return resp, err
}

type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       string                 `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicOutputConfig struct {
	Format *anthropicFormat `json:"format,omitempty"`
	Effort string           `json:"effort,omitempty"`
}

// anthropicFormat is a structured-output response format.
type anthropicFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []anthropicBlock `json:"content"`
	Usage   anthropicUsage   `json:"usage"`
	// StopReason is "max_tokens" when the output limit cut the response off.
	StopReason string `json:"stop_reason"`
}

type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"` // uncached input only
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// usage counts all input, cached or not, as InputTokens.
func (u anthropicUsage) usage() Usage {
	return Usage{
		InputTokens:       u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		OutputTokens:      u.OutputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
	}
}
