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
			w.Write([]byte(`{"error":"rate limited"}`))
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
		t.Fatalf("Review error after retries: %v", err)
	}
	if resp.Content != "[]" {
		t.Errorf("Content = %q, want %q", resp.Content, "[]")
	}
	if attempts != 3 {
		t.Errorf("Expected 3 attempts (2 retries), got %d", attempts)
	}
}
