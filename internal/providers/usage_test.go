package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Each provider reports its usage the same way: all input (cached
// included), all output (reasoning included), and the cached and reasoning
// parts where the API gives them. A cut-off answer reports its tokens too.
func TestProviderUsage(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		make func(srv *httptest.Server) Reviewer
		want Usage
	}{
		{"openai", `{"choices":[{"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1200,"completion_tokens":300,"total_tokens":1500,
			"prompt_tokens_details":{"cached_tokens":1000},"completion_tokens_details":{"reasoning_tokens":250}}}`,
			func(srv *httptest.Server) Reviewer {
				return &OpenAI{apiKey: "k", model: "gpt", baseURL: srv.URL, client: srv.Client()}
			},
			Usage{InputTokens: 1200, OutputTokens: 300, ReasoningTokens: 250, CachedInputTokens: 1000}},
		{"anthropic", `{"content":[{"type":"text","text":"[]"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":200,"output_tokens":80,"cache_read_input_tokens":900,"cache_creation_input_tokens":100}}`,
			func(srv *httptest.Server) Reviewer {
				return &Anthropic{apiKey: "k", model: "c", client: &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, baseURL: srv.URL}}}
			},
			Usage{InputTokens: 1200, OutputTokens: 80, CachedInputTokens: 900}},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"[]"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":500,"candidatesTokenCount":40,"thoughtsTokenCount":160,"cachedContentTokenCount":0,"totalTokenCount":700}}`,
			func(srv *httptest.Server) Reviewer {
				return &Gemini{apiKey: "k", model: "g", client: &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, baseURL: srv.URL}}}
			},
			Usage{InputTokens: 500, OutputTokens: 200, ReasoningTokens: 160}},
		{"cut off", `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}],
			"usage":{"prompt_tokens":900,"completion_tokens":8192,"completion_tokens_details":{"reasoning_tokens":8192}}}`,
			func(srv *httptest.Server) Reviewer {
				return &Ollama{model: "m", baseURL: srv.URL, client: srv.Client()}
			},
			Usage{InputTokens: 900, OutputTokens: 8192, ReasoningTokens: 8192}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			resp, _ := tc.make(srv).Review(context.Background(), ReviewRequest{UserPrompt: "x"})
			if resp.Usage != tc.want {
				t.Errorf("Usage = %+v, want %+v", resp.Usage, tc.want)
			}
		})
	}
}
