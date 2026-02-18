package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew_UnknownProvider(t *testing.T) {
	_, err := New("unknown", "model")
	if err == nil {
		t.Error("Expected error for unknown provider")
	}
}

func TestNew_GoogleAlias(t *testing.T) {
	// "google" should map to Gemini but requires API key
	_, err := New("google", "gemini-2.0-flash")
	if err == nil {
		t.Skip("GEMINI_API_KEY is set, skipping missing key test")
	}
	// Error should be about missing key, not unknown provider
	if err.Error() == "unknown provider: google" {
		t.Error("'google' should be a valid provider alias for gemini")
	}
}

func TestAnthropic_Name(t *testing.T) {
	a := &Anthropic{model: "test"}
	if a.Name() != "anthropic" {
		t.Errorf("Name() = %q, want %q", a.Name(), "anthropic")
	}
}

func TestOpenAI_Name(t *testing.T) {
	o := &OpenAI{model: "test"}
	if o.Name() != "openai" {
		t.Errorf("Name() = %q, want %q", o.Name(), "openai")
	}
}

func TestGemini_Name(t *testing.T) {
	g := &Gemini{model: "test"}
	if g.Name() != "gemini" {
		t.Errorf("Name() = %q, want %q", g.Name(), "gemini")
	}
}

func TestAnthropic_ServerError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":"internal server error"}`))
			return
		}
		resp := anthropicResponse{
			Content: []anthropicBlock{{Type: "text", Text: "[]"}},
			Usage:   anthropicUsage{InputTokens: 10, OutputTokens: 5},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	a := &Anthropic{
		apiKey: "test-key",
		model:  "claude-sonnet-4-20250514",
		client: &http.Client{
			Transport: &rewriteTransport{
				base:    server.Client().Transport,
				baseURL: server.URL,
			},
		},
	}

	resp, err := a.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err != nil {
		t.Fatalf("Review should succeed after retries: %v", err)
	}
	if resp.Content != "[]" {
		t.Errorf("Content = %q, want %q", resp.Content, "[]")
	}
	if attempts != 3 {
		t.Errorf("Expected 3 attempts (2 retries on 5xx), got %d", attempts)
	}
}

func TestAnthropic_EmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := anthropicResponse{
			Content: []anthropicBlock{}, // no text blocks
			Usage:   anthropicUsage{InputTokens: 10, OutputTokens: 0},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	a := &Anthropic{
		apiKey: "test-key",
		model:  "claude-sonnet-4-20250514",
		client: &http.Client{
			Transport: &rewriteTransport{
				base:    server.Client().Transport,
				baseURL: server.URL,
			},
		},
	}

	_, err := a.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err == nil {
		t.Error("Expected error for empty content")
	}
}

func TestOpenAI_EmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaiResponse{
			Choices: []openaiChoice{
				{Message: openaiMessage{Role: "assistant", Content: ""}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	o := &OpenAI{
		apiKey:  "test-key",
		model:   "gpt-4o",
		baseURL: server.URL,
		client:  server.Client(),
	}

	_, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err == nil {
		t.Error("Expected error for empty content")
	}
}

func TestOpenAI_NoChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaiResponse{Choices: []openaiChoice{}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	o := &OpenAI{
		apiKey:  "test-key",
		model:   "gpt-4o",
		baseURL: server.URL,
		client:  server.Client(),
	}

	_, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err == nil {
		t.Error("Expected error for no choices")
	}
}

func TestOpenAI_AuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	o := &OpenAI{
		apiKey:  "bad-key",
		model:   "gpt-4o",
		baseURL: server.URL,
		client:  server.Client(),
	}

	_, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err == nil {
		t.Fatal("Expected auth error")
	}
	if !IsAuthError(err) {
		t.Errorf("Expected auth error, got: %v", err)
	}
}

func TestOpenAI_ServerError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"service unavailable"}`))
			return
		}
		resp := openaiResponse{
			Choices: []openaiChoice{
				{Message: openaiMessage{Role: "assistant", Content: "[]"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
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
		t.Fatalf("Review should succeed after retry: %v", err)
	}
	if resp.Content != "[]" {
		t.Errorf("Content = %q, want %q", resp.Content, "[]")
	}
	if attempts != 2 {
		t.Errorf("Expected 2 attempts, got %d", attempts)
	}
}

func TestGemini_AuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer server.Close()

	g := &Gemini{
		apiKey: "bad-key",
		model:  "gemini-2.0-flash",
		client: &http.Client{
			Transport: &rewriteTransport{
				base:    server.Client().Transport,
				baseURL: server.URL,
			},
		},
	}

	_, err := g.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err == nil {
		t.Fatal("Expected auth error")
	}
	if !IsAuthError(err) {
		t.Errorf("Expected auth error, got: %v", err)
	}
}

func TestGemini_NoCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := geminiResponse{Candidates: []geminiCandidate{}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	g := &Gemini{
		apiKey: "test-key",
		model:  "gemini-2.0-flash",
		client: &http.Client{
			Transport: &rewriteTransport{
				base:    server.Client().Transport,
				baseURL: server.URL,
			},
		},
	}

	_, err := g.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err == nil {
		t.Error("Expected error for no candidates")
	}
}

func TestAnthropic_DefaultMaxTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body anthropicRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.MaxTokens != 4096 {
			t.Errorf("Default MaxTokens = %d, want 4096", body.MaxTokens)
		}
		resp := anthropicResponse{
			Content: []anthropicBlock{{Type: "text", Text: "[]"}},
			Usage:   anthropicUsage{InputTokens: 10, OutputTokens: 5},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	a := &Anthropic{
		apiKey: "test-key",
		model:  "claude-sonnet-4-20250514",
		client: &http.Client{
			Transport: &rewriteTransport{
				base:    server.Client().Transport,
				baseURL: server.URL,
			},
		},
	}

	_, err := a.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
		MaxTokens:    0, // should default to 4096
	})
	if err != nil {
		t.Fatalf("Review error: %v", err)
	}
}

func TestIsAuthError(t *testing.T) {
	if IsAuthError(nil) {
		t.Error("nil should not be auth error")
	}
	if IsAuthError(&rateLimitError{}) {
		t.Error("rateLimitError should not be auth error")
	}
	if !IsAuthError(&authError{message: "test"}) {
		t.Error("authError should be auth error")
	}
}

func TestIsRetryable(t *testing.T) {
	if isRetryable(&authError{message: "test"}) {
		t.Error("authError should not be retryable")
	}
	if !isRetryable(&rateLimitError{}) {
		t.Error("rateLimitError should be retryable")
	}
	if !isRetryable(&serverError{statusCode: 500}) {
		t.Error("serverError should be retryable")
	}
	if isRetryable(context.Canceled) {
		t.Error("context.Canceled should not be retryable")
	}
}

func TestErrorMessages(t *testing.T) {
	rl := &rateLimitError{}
	if rl.Error() != "rate limited" {
		t.Errorf("rateLimitError.Error() = %q", rl.Error())
	}

	se := &serverError{statusCode: 500, body: "oops"}
	if se.Error() != "server error: oops" {
		t.Errorf("serverError.Error() = %q", se.Error())
	}

	ae := &authError{message: "bad key"}
	if ae.Error() != "authentication error: bad key" {
		t.Errorf("authError.Error() = %q", ae.Error())
	}
}

func TestRetryWithBackoff_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := retryWithBackoff(ctx, 3, func() error {
		return &rateLimitError{}
	})
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got: %v", err)
	}
}

func TestRetryWithBackoff_NonRetryable(t *testing.T) {
	attempts := 0
	err := retryWithBackoff(context.Background(), 3, func() error {
		attempts++
		return &authError{message: "bad"}
	})
	if attempts != 1 {
		t.Errorf("Expected 1 attempt for auth error, got %d", attempts)
	}
	if !IsAuthError(err) {
		t.Errorf("Expected auth error, got: %v", err)
	}
}

func TestRetryWithBackoff_Success(t *testing.T) {
	err := retryWithBackoff(context.Background(), 3, func() error {
		return nil
	})
	if err != nil {
		t.Errorf("Expected nil error, got: %v", err)
	}
}
