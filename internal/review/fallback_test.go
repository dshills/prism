package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/providers"
)

// switchableReviewer fails while down is set, else reports one finding per
// file like fileReviewer.
type switchableReviewer struct {
	fileReviewer
	down error
}

func (s *switchableReviewer) Review(ctx context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	if s.down != nil {
		s.mu.Lock()
		s.prompts = append(s.prompts, req.UserPrompt)
		s.mu.Unlock()
		return providers.ReviewResponse{}, s.down
	}
	resp, err := s.fileReviewer.Review(ctx, req)
	resp.Provider, resp.Model = s.Name(), "m"
	return resp, err
}

// useProviders makes newProvider return byName[provider], or fail when it is
// nil, for the rest of the test.
func useProviders(t *testing.T, byName map[string]providers.Reviewer) {
	t.Helper()
	old := newProvider
	newProvider = func(name, _ string) (providers.Reviewer, error) {
		if r := byName[name]; r != nil {
			return r, nil
		}
		return nil, errors.New(strings.ToUpper(name) + "_API_KEY environment variable is not set")
	}
	t.Cleanup(func() { newProvider = old })
}

type namedReviewer struct {
	*switchableReviewer
	name string
}

func (n namedReviewer) Name() string { return n.name }

func (n namedReviewer) Review(ctx context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	resp, err := n.switchableReviewer.Review(ctx, req)
	if err == nil {
		resp.Provider = n.name
	}
	return resp, err
}

// When the primary fails, the fallback reviews; coverage says so, findings
// carry the fallback's provenance, and none of it is cached as the primary's.
func TestRun_FallbackReviewsAndIsNotCached(t *testing.T) {
	primary := &switchableReviewer{down: errors.New("sending request: connection refused")}
	fallback := &switchableReviewer{}
	useProviders(t, map[string]providers.Reviewer{
		"openai": namedReviewer{primary, "openai"},
		"ollama": namedReviewer{fallback, "ollama"},
	})
	cfg := chunkTestConfig(t)
	cfg.Provider, cfg.Model, cfg.ChunkBytes = "openai", "gpt", 0
	cfg.Fallback = "ollama:llama3.3"
	ctx := context.Background()

	report, err := Run(ctx, threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 3 || report.Findings[0].Provider != "ollama" {
		t.Fatalf("findings = %d (provider %q), want 3 from the fallback", len(report.Findings), report.Findings[0].Provider)
	}
	fb := report.Coverage.Fallback
	if fb == nil || fb.Reviewer.Provider != "ollama" || fb.Reviewer.Model != "llama3.3" || !strings.Contains(fb.Reason, "connection refused") {
		t.Fatalf("coverage.fallback = %+v, want ollama/llama3.3 with the reason", fb)
	}
	if report.Coverage.LLMCalls != 2 {
		t.Errorf("llmCalls = %d, want 2: the primary's failed call and the fallback's", report.Coverage.LLMCalls)
	}
	if line := report.Coverage.Describe(0); !strings.Contains(line, "fell back to ollama/llama3.3") {
		t.Errorf("coverage line = %q, want it to name the fallback", line)
	}

	// The primary is back: its review is not replayed from the fallback's.
	primary.down = nil
	primary.calls()
	again, err := Run(ctx, threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again.Coverage.CacheHit || len(primary.calls()) != 1 || again.Coverage.Fallback != nil {
		t.Errorf("second run: cache hit %v, %d primary calls, fallback %+v; want a fresh primary review",
			again.Coverage.CacheHit, len(primary.calls()), again.Coverage.Fallback)
	}
}

// In a chunked review, chunks the fallback answered are not cached either.
func TestRun_FallbackChunksNotCached(t *testing.T) {
	primary := &switchableReviewer{down: errors.New("sending request: timeout")}
	useProviders(t, map[string]providers.Reviewer{
		"openai": namedReviewer{primary, "openai"},
		"ollama": namedReviewer{&switchableReviewer{}, "ollama"},
	})
	cfg := chunkTestConfig(t)
	cfg.Provider, cfg.Model, cfg.Fallback = "openai", "gpt", "ollama:llama3.3"
	ctx := context.Background()

	if _, err := Run(ctx, threeChunkDiff("beta"), cfg); err != nil {
		t.Fatal(err)
	}
	primary.down = nil
	primary.calls()
	again, err := Run(ctx, threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again.Coverage.CachedChunks != 0 || len(primary.calls()) != 3 {
		t.Errorf("second run: %d chunks from cache, %d primary calls; want 0 and 3", again.Coverage.CachedChunks, len(primary.calls()))
	}
}

// A primary that cannot be created (no API key) is not an error when a
// fallback is set: the fallback reviews.
func TestRun_FallbackWhenPrimaryUnavailable(t *testing.T) {
	useProviders(t, map[string]providers.Reviewer{"ollama": namedReviewer{&switchableReviewer{}, "ollama"}})
	cfg := chunkTestConfig(t)
	cfg.Provider, cfg.Model, cfg.ChunkBytes = "anthropic", "claude", 0

	if _, err := Run(context.Background(), threeChunkDiff("beta"), cfg); err == nil {
		t.Fatal("without a fallback, a missing key is an error")
	}
	cfg.Fallback = "ollama:llama3.3"
	report, err := Run(context.Background(), threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fb := report.Coverage.Fallback; fb == nil || !strings.Contains(fb.Reason, "ANTHROPIC_API_KEY") {
		t.Errorf("coverage.fallback = %+v, want the missing key as the reason", fb)
	}
}

func TestNewReviewer_BadFallbackSpec(t *testing.T) {
	cfg := chunkTestConfig(t)
	cfg.Fallback = "llama3.3" // no provider
	if _, err := newReviewer(cfg); err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Errorf("err = %v, want the fallback spec rejected", err)
	}
}
