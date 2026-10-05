package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultOpenAIURL = "https://api.openai.com/v1/chat/completions"

// OpenAI implements the Reviewer interface for OpenAI's API.
type OpenAI struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
	schema  schemaSupport
}

// NewOpenAI creates a new OpenAI provider.
func NewOpenAI(model string) (*OpenAI, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable is not set")
	}
	baseURL := os.Getenv("PRISM_OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = defaultOpenAIURL
	}
	return &OpenAI{
		apiKey:  key,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func (o *OpenAI) Name() string { return "openai" }

func (o *OpenAI) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	structured := o.schema.use(req.Output)
	resp, err := o.review(ctx, req, structured)
	if structured && refusedSchema(err) {
		// The endpoint does not take response_format: ask again without it,
		// and stop asking once that works.
		if resp, err = o.review(ctx, req, false); err == nil {
			o.schema.rejected.Store(true)
		}
	}
	return resp, err
}

// review sends one review request, asking for structured output when
// structured is true.
func (o *OpenAI) review(ctx context.Context, req ReviewRequest, structured bool) (ReviewResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	messages := []openaiMessage{
		{Role: "system", Content: req.SystemPrompt},
		{Role: "user", Content: req.UserPrompt},
	}

	body := openaiRequest{
		Model:    o.model,
		Messages: messages,
	}
	// Newer OpenAI models reject max_tokens and require max_completion_tokens.
	if usesMaxCompletionTokens(o.model, isOfficialOpenAI(o.baseURL)) {
		body.MaxCompletionTokens = maxTokens
	} else {
		body.MaxTokens = maxTokens
	}
	if req.Temperature > 0 {
		body.Temperature = &req.Temperature
	}
	if structured {
		body.ResponseFormat = openaiFormat(req.Output)
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
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

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

type openaiRequest struct {
	Model               string                `json:"model"`
	Messages            []openaiMessage       `json:"messages"`
	MaxTokens           int                   `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                   `json:"max_completion_tokens,omitempty"`
	Temperature         *float64              `json:"temperature,omitempty"`
	ResponseFormat      *openaiResponseFormat `json:"response_format,omitempty"`
}

// openaiResponseFormat asks for structured output: a response that matches
// a JSON schema. OpenAI-compatible servers (Ollama, LM Studio) accept the
// same field.
type openaiResponseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *openaiJSONSchema `json:"json_schema,omitempty"`
}

type openaiJSONSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Strict      bool           `json:"strict"`
	Schema      map[string]any `json:"schema"`
}

// openaiFormat is the response_format for out.
func openaiFormat(out *Output) *openaiResponseFormat {
	return &openaiResponseFormat{
		Type: "json_schema",
		JSONSchema: &openaiJSONSchema{
			Name:        out.Name,
			Description: out.Description,
			Strict:      true,
			Schema:      out.Schema.jsonSchema(true),
		},
	}
}

// legacyMaxTokensFamilies are the OpenAI model families that still take
// max_tokens. Every family released after them (gpt-5, gpt-6, the o-series, ...)
// requires max_completion_tokens, so naming the old families rather than the
// new ones means a new model works without an edit here.
var legacyMaxTokensFamilies = []string{"gpt-4", "gpt-3.5", "chatgpt-"}

// modernOpenAIFamilies are the families known to require
// max_completion_tokens. They are only consulted for a custom
// PRISM_OPENAI_BASE_URL, where the model may be anything an OpenAI-compatible
// server hosts.
var modernOpenAIFamilies = []string{"gpt-5", "gpt-6", "o1", "o3", "o4"}

// isOfficialOpenAI reports whether baseURL is OpenAI's own API, judged by host
// so that an explicit PRISM_OPENAI_BASE_URL with a different path or a trailing
// slash still counts. An empty baseURL is the default endpoint.
func isOfficialOpenAI(baseURL string) bool {
	if baseURL == "" {
		return true
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com")
}

// usesMaxCompletionTokens reports whether model takes max_completion_tokens
// rather than max_tokens.
//
// On OpenAI's own endpoint (official), only the legacy families take
// max_tokens, and everything else, including models released after this was
// written, gets max_completion_tokens. On a custom OpenAI-compatible endpoint
// the model may be a Llama or Mistral served by a server that only knows
// max_tokens, so there only the known modern OpenAI families switch over.
// Fine-tuned ids ("ft:gpt-4o-mini:org::id") are judged by their base model.
func usesMaxCompletionTokens(model string, official bool) bool {
	m := strings.TrimPrefix(strings.ToLower(model), "ft:")
	if official {
		for _, legacy := range legacyMaxTokensFamilies {
			if strings.HasPrefix(m, legacy) {
				return false
			}
		}
		return true
	}
	for _, modern := range modernOpenAIFamilies {
		if strings.HasPrefix(m, modern) {
			return true
		}
	}
	return false
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiResponse struct {
	Choices []openaiChoice `json:"choices"`
	Usage   openaiUsage    `json:"usage"`
}

type openaiChoice struct {
	Message openaiMessage `json:"message"`
	// FinishReason is "length" when the output limit cut the response off.
	FinishReason string `json:"finish_reason"`
}

type openaiUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"` // reasoning included
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u openaiUsage) usage() Usage {
	return Usage{
		InputTokens:       u.PromptTokens,
		OutputTokens:      u.CompletionTokens,
		ReasoningTokens:   u.CompletionTokensDetails.ReasoningTokens,
		CachedInputTokens: u.PromptTokensDetails.CachedTokens,
	}
}
