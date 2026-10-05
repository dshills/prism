package review

import (
	"context"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
)

func TestPriceOf(t *testing.T) {
	configured := map[string]config.Price{
		"openai:gpt-6.1-sol":          {Input: 3, Output: 12},
		"anthropic:claude-sonnet-4-6": {Input: 9, Output: 9}, // overrides the built-in
	}
	for _, tc := range []struct {
		provider, model string
		want            config.Price
		known           bool
	}{
		{"openai", "gpt-6.1-sol", config.Price{Input: 3, Output: 12}, true},
		{"anthropic", "claude-sonnet-4-6", config.Price{Input: 9, Output: 9}, true},
		{"anthropic", "claude-opus-5-5", config.Price{Input: 4, Output: 20}, true},
		{"anthropic", "claude-haiku-4-5-20251001", config.Price{Input: 1, Output: 5}, true},
		{"ollama", "llama3.3", config.Price{}, true},
		{"gemini", "gemini-3-flash-preview", config.Price{}, false},
	} {
		got, ok := priceOf(tc.provider, tc.model, configured)
		if ok != tc.known || got != tc.want {
			t.Errorf("%s:%s = %+v, %v; want %+v, %v", tc.provider, tc.model, got, ok, tc.want, tc.known)
		}
	}
}

func TestTokensLine(t *testing.T) {
	tokens := []TokenUsage{
		{Provider: "anthropic", Model: "claude-opus-5-5", Usage: providers.Usage{InputTokens: 1_000_000, OutputTokens: 100_000, CachedInputTokens: 400_000}},
		{Provider: "ollama", Model: "llama", Usage: providers.Usage{InputTokens: 2_000, OutputTokens: 500, ReasoningTokens: 300}},
	}
	priceTokens(tokens, nil)
	c := Coverage{Tokens: tokens}
	if got, want := c.TokensLine(), "Tokens: 1,002,000 in (400,000 cached) / 100,500 out (300 reasoning) — ~$6.00"; got != want {
		t.Errorf("TokensLine = %q, want %q", got, want)
	}

	// One model without a known price: the tokens, but no cost.
	c.Tokens = append(c.Tokens, TokenUsage{Provider: "gemini", Model: "g", Usage: providers.Usage{InputTokens: 1, OutputTokens: 1}})
	if got := c.TokensLine(); strings.Contains(got, "$") {
		t.Errorf("TokensLine = %q, want no cost while a price is unknown", got)
	}
	if (Coverage{}).TokensLine() != "" {
		t.Error("no tokens, no line")
	}
}

// Per-commit sums merge by model and add up the costs.
func TestMergeTokens(t *testing.T) {
	one, two := 1.0, 2.0
	a := []TokenUsage{{Provider: "p", Model: "m", Usage: providers.Usage{InputTokens: 10}, CostUSD: &one}}
	b := []TokenUsage{
		{Provider: "p", Model: "m", Usage: providers.Usage{InputTokens: 5}, CostUSD: &two},
		{Provider: "q", Model: "n", Usage: providers.Usage{OutputTokens: 7}},
	}
	got := mergeTokens(a, b)
	if len(got) != 2 || got[0].InputTokens != 15 || *got[0].CostUSD != 3 || got[1].OutputTokens != 7 {
		t.Errorf("merged = %+v", got)
	}
}

// usageReviewer answers like fileReviewer and reports fixed usage per call.
type usageReviewer struct{ fileReviewer }

func (u *usageReviewer) Review(ctx context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	resp, err := u.fileReviewer.Review(ctx, req)
	resp.Usage = providers.Usage{InputTokens: 100, OutputTokens: 10}
	return resp, err
}

// A chunked review's usage is summed over its calls and priced from config;
// a cache replay used none.
func TestRun_CoverageTokens(t *testing.T) {
	useProvider(t, &usageReviewer{})
	cfg := chunkTestConfig(t)
	cfg.Prices = map[string]config.Price{"mock:m": {Input: 1, Output: 2}}

	report, err := Run(context.Background(), threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tok := report.Coverage.Tokens
	if len(tok) != 1 || tok[0].InputTokens != 300 || tok[0].OutputTokens != 30 || tok[0].CostUSD == nil || *tok[0].CostUSD != (300*1+30*2)/1e6 {
		t.Fatalf("coverage.tokens = %+v, want 300 in, 30 out, priced", tok)
	}
	again, err := Run(context.Background(), threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Coverage.Tokens) != 0 || again.Coverage.TokensLine() != "" {
		t.Errorf("cache replay: tokens = %+v, want none", again.Coverage.Tokens)
	}
}
