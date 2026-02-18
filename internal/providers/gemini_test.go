package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGemini_Review(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the API key is passed as a header
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Error("Missing API key in x-goog-api-key header")
		}

		resp := geminiResponse{
			Candidates: []geminiCandidate{
				{
					Content: geminiContent{
						Parts: []geminiPart{{Text: "[]"}},
					},
				},
			},
			UsageMetadata: geminiUsage{TotalTokenCount: 75},
		}
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

	resp, err := g.Review(context.Background(), ReviewRequest{
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
	if resp.TokensUsed != 75 {
		t.Errorf("TokensUsed = %d, want 75", resp.TokensUsed)
	}
}
