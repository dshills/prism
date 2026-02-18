package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllama_Review(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no Authorization header when no API key is set
		if r.Header.Get("Authorization") != "" {
			t.Error("Expected no Authorization header for keyless Ollama")
		}

		resp := openaiResponse{
			Choices: []openaiChoice{
				{Message: openaiMessage{Role: "assistant", Content: "[]"}},
			},
			Usage: openaiUsage{TotalTokens: 100},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	o := &Ollama{
		model:   "llama3",
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
	if resp.TokensUsed != 100 {
		t.Errorf("TokensUsed = %d, want 100", resp.TokensUsed)
	}
}

func TestOllama_ReviewWithAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-ollama-key" {
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

	o := &Ollama{
		apiKey:  "test-ollama-key",
		model:   "llama3",
		baseURL: server.URL,
		client:  server.Client(),
	}

	resp, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err != nil {
		t.Fatalf("Review error: %v", err)
	}
	if resp.Content != "[]" {
		t.Errorf("Content = %q, want %q", resp.Content, "[]")
	}
}

func TestOllama_ServerError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer server.Close()

	o := &Ollama{
		model:   "llama3",
		baseURL: server.URL,
		client:  server.Client(),
	}

	_, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err == nil {
		t.Fatal("Expected error for server error response")
	}
	// Should retry: 1 initial + 3 retries = 4 attempts
	if attempts != 4 {
		t.Errorf("Expected 4 attempts, got %d", attempts)
	}
}

func TestOllama_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaiResponse{
			Choices: []openaiChoice{},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	o := &Ollama{
		model:   "llama3",
		baseURL: server.URL,
		client:  server.Client(),
	}

	_, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "test",
		UserPrompt:   "test",
	})
	if err == nil {
		t.Fatal("Expected error for empty response")
	}
}

func TestOllama_Name(t *testing.T) {
	o := &Ollama{}
	if o.Name() != "ollama" {
		t.Errorf("Name() = %q, want %q", o.Name(), "ollama")
	}
}

func TestNewOllama_URLNormalization(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantURL string
	}{
		{
			name:    "default",
			host:    "",
			wantURL: "http://localhost:11434/v1/chat/completions",
		},
		{
			name:    "trailing slash",
			host:    "http://localhost:11434/",
			wantURL: "http://localhost:11434/v1/chat/completions",
		},
		{
			name:    "with v1",
			host:    "http://localhost:11434/v1",
			wantURL: "http://localhost:11434/v1/chat/completions",
		},
		{
			name:    "with full path",
			host:    "http://localhost:11434/v1/chat/completions",
			wantURL: "http://localhost:11434/v1/chat/completions",
		},
		{
			name:    "custom host",
			host:    "http://192.168.1.100:11434",
			wantURL: "http://192.168.1.100:11434/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OLLAMA_HOST", tt.host)
			t.Setenv("PRISM_OLLAMA_API_KEY", "")

			o, err := NewOllama("llama3")
			if err != nil {
				t.Fatalf("NewOllama error: %v", err)
			}
			if o.baseURL != tt.wantURL {
				t.Errorf("baseURL = %q, want %q", o.baseURL, tt.wantURL)
			}
		})
	}
}

func TestFactory_OllamaAliases(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "http://localhost:11434")

	for _, name := range []string{"ollama", "lmstudio"} {
		r, err := New(name, "llama3")
		if err != nil {
			t.Fatalf("New(%q) error: %v", name, err)
		}
		if r.Name() != "ollama" {
			t.Errorf("New(%q).Name() = %q, want %q", name, r.Name(), "ollama")
		}
	}
}
