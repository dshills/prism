package providers

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestWaferChatURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "https://pass.wafer.ai/v1/chat/completions",
		"https://pass.wafer.ai/v1":  "https://pass.wafer.ai/v1/chat/completions",
		"https://pass.wafer.ai/v1/": "https://pass.wafer.ai/v1/chat/completions",
		"https://proxy.example/v1/chat/completions": "https://proxy.example/v1/chat/completions",
		" http://localhost:8080/v1 ":                "http://localhost:8080/v1/chat/completions",
	} {
		if got := waferChatURL(in); got != want {
			t.Errorf("waferChatURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewWafer(t *testing.T) {
	t.Setenv("WAFER_API_KEY", "")
	if _, err := New("wafer", "GLM-5.3"); err == nil {
		t.Error("no key: no error")
	}
	t.Setenv("WAFER_API_KEY", "wk")
	t.Setenv("WAFER_API_URL", "https://pass.wafer.ai/v1")
	p, err := New("wafer", "GLM-5.3")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "wafer" {
		t.Errorf("Name = %q", p.Name())
	}
	if o, _ := p.(*OpenAI); o == nil || o.baseURL != "https://pass.wafer.ai/v1/chat/completions" {
		t.Errorf("provider = %+v", p)
	}
}

// A review goes to WAFER_API_URL's chat completions with the key, the model,
// max_tokens, structured output and the reasoning effort, and its response
// and usage are labelled wafer.
func TestWafer_Review(t *testing.T) {
	rs := &recordingServer{}
	var auth, path string
	srv := rs.start(t, func(_ map[string]any, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"findings\":[]}","reasoning_content":"thinking"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":900,"completion_tokens":120,"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":100}}}`)
	})
	srv.Config.Handler = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth, path = r.Header.Get("Authorization"), r.URL.Path
			next.ServeHTTP(w, r)
		})
	}(srv.Config.Handler)
	t.Setenv("WAFER_API_KEY", "wk-test")
	t.Setenv("WAFER_API_URL", srv.URL+"/v1")
	p, err := NewWafer("DeepSeek-V4.1-Flash")
	if err != nil {
		t.Fatal(err)
	}
	p.client = srv.Client()

	resp, err := p.Review(context.Background(), ReviewRequest{SystemPrompt: "s", UserPrompt: "u", MaxTokens: 8192, Output: testOutput, Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer wk-test" || path != "/v1/chat/completions" {
		t.Errorf("auth %q, path %q", auth, path)
	}
	body := rs.requests()[0]
	if body["model"] != "DeepSeek-V4.1-Flash" || body["max_tokens"] != float64(8192) || body["max_completion_tokens"] != nil ||
		body["reasoning_effort"] != "high" || body["response_format"] == nil {
		t.Errorf("request = %v", body)
	}
	want := Usage{InputTokens: 900, OutputTokens: 120, ReasoningTokens: 100, CachedInputTokens: 800}
	if resp.Content != `{"findings":[]}` || resp.Provider != "wafer" || resp.Model != "DeepSeek-V4.1-Flash" || resp.Usage != want {
		t.Errorf("response = %+v", resp)
	}
}

// Wafer refuses an effort level a model does not take (minimal, say) with a
// 400 naming it: the review goes on without it.
func TestWafer_RefusedEffortIsDropped(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(body map[string]any, w http.ResponseWriter) {
		if body["reasoning_effort"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"reasoning_effort must be one of none, low, medium, high, xhigh, max","type":"invalid_request_error","param":"reasoning_effort","code":"unsupported_value"}}`)
			return
		}
		openaiAnswer(w, "[]")
	})
	t.Setenv("WAFER_API_KEY", "wk")
	t.Setenv("WAFER_API_URL", srv.URL)
	p, err := NewWafer("GLM-5.3")
	if err != nil {
		t.Fatal(err)
	}
	p.client = srv.Client()
	if _, err := p.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput, Effort: "minimal"}); err != nil {
		t.Fatal(err)
	}
	if reqs := rs.requests(); len(reqs) != 2 || reqs[1]["reasoning_effort"] != nil || reqs[1]["response_format"] == nil {
		t.Errorf("requests = %v", reqs)
	}
}
