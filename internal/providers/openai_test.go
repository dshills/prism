package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAI_Review(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("Missing or wrong Authorization header")
		}

		resp := openaiResponse{
			Choices: []openaiChoice{
				{Message: openaiMessage{Role: "assistant", Content: "[]"}},
			},
			Usage: openaiUsage{TotalTokens: 50},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	o := &OpenAI{
		apiKey:  "test-key",
		model:   "gpt-4o",
		baseURL: server.URL,
		client:  server.Client(),
	}

	resp, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
		MaxTokens:    10,
	})
	if err != nil {
		t.Fatalf("Review error: %v", err)
	}
	if resp.Content != "[]" {
		t.Errorf("Content = %q, want %q", resp.Content, "[]")
	}
	if resp.TokensUsed != 50 {
		t.Errorf("TokensUsed = %d, want 50", resp.TokensUsed)
	}
}

func TestOpenAI_RateLimit(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		resp := openaiResponse{
			Choices: []openaiChoice{
				{Message: openaiMessage{Role: "assistant", Content: "[]"}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	o := &OpenAI{
		apiKey:  "test-key",
		model:   "gpt-4o",
		baseURL: server.URL,
		client:  server.Client(),
	}

	resp, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err != nil {
		t.Fatalf("Review error after retries: %v", err)
	}
	if resp.Content != "[]" {
		t.Errorf("Content = %q, want %q", resp.Content, "[]")
	}
	if attempts != 3 {
		t.Errorf("Expected 3 attempts (2 retries), got %d", attempts)
	}
}

func TestUsesMaxCompletionTokens(t *testing.T) {
	for _, tc := range []struct {
		model    string
		official bool
		want     bool
	}{
		// OpenAI's own endpoint: only the legacy families keep max_tokens.
		{"gpt-4o", true, false},
		{"gpt-4.1-mini", true, false},
		{"gpt-4-turbo", true, false},
		{"gpt-3.5-turbo", true, false},
		{"chatgpt-4o-latest", true, false},
		{"ft:gpt-4o-mini:acme::abc123", true, false},
		{"gpt-5.3-codex", true, true},
		{"gpt-6-luna", true, true},
		{"o3-mini", true, true},
		{"o4-mini", true, true},
		{"GPT-6-Luna", true, true},
		{"some-future-model", true, true}, // new families work without an edit
		// A custom compatible endpoint: only known modern OpenAI families switch.
		{"llama3.1:70b", false, false},
		{"mistral-large", false, false},
		{"gpt-4o", false, false},
		{"gpt-5.2", false, true},
		{"gpt-6-luna", false, true},
		{"o3-mini", false, true},
	} {
		if got := usesMaxCompletionTokens(tc.model, tc.official); got != tc.want {
			t.Errorf("usesMaxCompletionTokens(%q, official=%v) = %v, want %v", tc.model, tc.official, got, tc.want)
		}
	}
}

// The request body carries exactly one of the two token fields, chosen by
// model: gpt-6-luna must not send max_tokens (OpenAI rejects it with a 400).
func TestOpenAI_TokenFieldInRequest(t *testing.T) {
	for _, tc := range []struct {
		model, want, notWant string
	}{
		{"gpt-6-luna", "max_completion_tokens", "max_tokens"},
		{"gpt-4o", "max_tokens", "max_completion_tokens"},
	} {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(openaiResponse{
				Choices: []openaiChoice{{Message: openaiMessage{Role: "assistant", Content: "[]"}}},
			})
		}))
		// The official-endpoint rule applies whenever baseURL is the default, so
		// point the client at the test server by swapping its transport instead.
		o := &OpenAI{apiKey: "k", model: tc.model, baseURL: defaultOpenAIURL, client: &http.Client{
			Transport: rewriteTo(server.URL),
		}}
		if _, err := o.Review(context.Background(), ReviewRequest{SystemPrompt: "s", UserPrompt: "u", MaxTokens: 10}); err != nil {
			server.Close()
			t.Fatalf("%s: Review: %v", tc.model, err)
		}
		server.Close()
		if _, ok := body[tc.want]; !ok {
			t.Errorf("%s: request body lacks %s: %v", tc.model, tc.want, body)
		}
		if _, ok := body[tc.notWant]; ok {
			t.Errorf("%s: request body must not carry %s: %v", tc.model, tc.notWant, body)
		}
	}
}

// rewriteTo sends every request to target, keeping the path, so a client
// configured for the real OpenAI URL can be exercised against a test server.
type rewriteTo string

func (target rewriteTo) RoundTrip(r *http.Request) (*http.Response, error) {
	u, err := r.URL.Parse(string(target) + r.URL.Path)
	if err != nil {
		return nil, err
	}
	r2 := r.Clone(r.Context())
	r2.URL = u
	r2.Host = u.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func TestIsOfficialOpenAI(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"", true},
		{defaultOpenAIURL, true},
		{"https://api.openai.com/v1/chat/completions/", true},
		{"https://API.OpenAI.com/v1/chat/completions", true},
		{"http://localhost:11434/v1/chat/completions", false},
		{"https://my-proxy.example.com/v1/chat/completions", false},
		{"://not a url", false},
	} {
		if got := isOfficialOpenAI(tc.baseURL); got != tc.want {
			t.Errorf("isOfficialOpenAI(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}
