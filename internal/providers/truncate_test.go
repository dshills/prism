package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Each provider reports a response cut off at its output limit as a
// truncation, never as content to parse.
func TestTruncationDetected(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		make func(srv *httptest.Server) Reviewer
	}{
		{"openai", `{"choices":[{"message":{"role":"assistant","content":"[{\"sev"},"finish_reason":"length"}]}`,
			func(srv *httptest.Server) Reviewer {
				return &OpenAI{apiKey: "k", model: "gpt-4o", baseURL: srv.URL, client: srv.Client()}
			}},
		{"ollama", `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}]}`,
			func(srv *httptest.Server) Reviewer {
				return &Ollama{model: "m", baseURL: srv.URL, client: srv.Client()}
			}},
		{"anthropic", `{"content":[{"type":"text","text":"[{\"sev"}],"stop_reason":"max_tokens","usage":{}}`,
			func(srv *httptest.Server) Reviewer {
				return &Anthropic{apiKey: "k", model: "c", client: &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, baseURL: srv.URL}}}
			}},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"[{"}]},"finishReason":"MAX_TOKENS"}]}`,
			func(srv *httptest.Server) Reviewer {
				return &Gemini{apiKey: "k", model: "g", client: &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, baseURL: srv.URL}}}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := tc.make(srv).Review(context.Background(), ReviewRequest{UserPrompt: "x", MaxTokens: 100})
			if !IsTruncated(err) {
				t.Fatalf("err = %v, want a truncation", err)
			}
			if calls != 1 {
				t.Errorf("%d calls, want 1: a truncation is not retried", calls)
			}
		})
	}
	if !IsTruncated(fmt.Errorf("chunk 2: %w", &truncatedError{maxTokens: 8})) || IsTruncated(errors.New("other")) {
		t.Error("IsTruncated should see through wrapping, and only truncations")
	}
}

// A different model would not fix an answer too long for one response, so a
// truncation does not switch to the fallback.
func TestFallback_NotForTruncation(t *testing.T) {
	f := NewFallback(&stubReviewer{name: "openai", err: &truncatedError{maxTokens: 8192}}, nil, "openai:gpt", "ollama:llama",
		func() (Reviewer, error) { return &stubReviewer{name: "ollama", content: "[]"}, nil })
	if _, err := f.Review(context.Background(), ReviewRequest{}); !IsTruncated(err) {
		t.Errorf("err = %v, want the truncation", err)
	}
	if _, _, used := f.FellBack(); used {
		t.Error("fell back on a truncation")
	}
}
