package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropic_Review(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if r.Header.Get("x-api-key") != "test-key" {
			t.Error("Missing API key header")
		}
		if r.Header.Get("anthropic-version") != anthropicAPIVersion {
			t.Error("Missing anthropic-version header")
		}

		resp := anthropicResponse{
			Content: []anthropicBlock{
				{Type: "text", Text: "[]"},
			},
			Usage: anthropicUsage{InputTokens: 100, OutputTokens: 10},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	a := &Anthropic{
		apiKey: "test-key",
		model:  "claude-sonnet-4-20250514",
		client: server.Client(),
	}
	// Override URL by using a custom transport
	origURL := anthropicAPIURL
	// We'll use a wrapper approach: create a client that redirects to our server
	a.client = &http.Client{
		Transport: &rewriteTransport{
			base:    server.Client().Transport,
			baseURL: server.URL,
		},
	}
	_ = origURL

	resp, err := a.Review(context.Background(), ReviewRequest{
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
	if resp.TokensUsed != 110 {
		t.Errorf("TokensUsed = %d, want 110", resp.TokensUsed)
	}
}

func TestAnthropic_AuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	a := &Anthropic{
		apiKey: "bad-key",
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
		t.Fatal("Expected auth error")
	}
	if !IsAuthError(err) {
		t.Errorf("Expected auth error, got: %v", err)
	}
}

// rewriteTransport rewrites all request URLs to point at the test server.
type rewriteTransport struct {
	base    http.RoundTripper
	baseURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = t.baseURL[len("http://"):]
	if t.base != nil {
		return t.base.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}
