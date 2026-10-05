package review

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/providers"
)

// usageNamed is a namedReviewer that also reports usage.
type usageNamed struct{ namedReviewer }

func (u usageNamed) Review(ctx context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	resp, err := u.namedReviewer.Review(ctx, req)
	resp.Usage = providers.Usage{InputTokens: 100, OutputTokens: 10}
	return resp, err
}

// Each model goes through the full pipeline: the diff is chunked for each,
// both agree on every finding, and the coverage combines them, with tokens
// kept per model. A rerun replays every model's chunks from cache.
func TestRunCompare_FullPipeline(t *testing.T) {
	a, b := &switchableReviewer{}, &switchableReviewer{}
	useProviders(t, map[string]providers.Reviewer{
		"openai":    usageNamed{namedReviewer{a, "openai"}},
		"anthropic": usageNamed{namedReviewer{b, "anthropic"}},
	})
	cfg := chunkTestConfig(t)
	models := []string{"openai:gpt", "anthropic:claude"}
	diff := threeChunkDiff("beta")

	cr, err := RunCompare(context.Background(), diff, models, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(cr.Consensus) != 3 || len(cr.All) != 3 {
		t.Errorf("consensus %d, all %d; want both models agreeing on 3", len(cr.Consensus), len(cr.All))
	}
	c := cr.Coverage
	if c.Chunks != 6 || c.LLMCalls != 6 || cr.Calls != 6 || len(c.Reviewer) != 2 || len(c.Tokens) != 2 || !c.Complete {
		t.Errorf("coverage = %+v, want 3 chunks and calls per model, 2 reviewers, tokens per model", c)
	}

	again, err := RunCompare(context.Background(), diff, models, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c := again.Coverage; !c.CacheHit || c.LLMCalls != 0 || c.CachedChunks != 6 {
		t.Errorf("rerun coverage = %+v, want every model's chunks from cache", c)
	}
}

// A model that fails is left out as a skip, and the others' findings stand.
func TestRunCompare_FailedModelIsSkipped(t *testing.T) {
	useProviders(t, map[string]providers.Reviewer{
		"openai":    namedReviewer{&switchableReviewer{}, "openai"},
		"anthropic": namedReviewer{&switchableReviewer{down: errors.New("sending request: connection refused")}, "anthropic"},
	})
	cfg := chunkTestConfig(t)
	cfg.ChunkBytes = 0
	cr, err := RunCompare(context.Background(), threeChunkDiff("beta"), []string{"openai:gpt", "anthropic:claude"}, cfg)
	if err != nil {
		t.Fatalf("one failed model should not fail the comparison: %v", err)
	}
	c := cr.Coverage
	if len(cr.All) != 3 || c.Complete || len(c.Skipped) != 1 || c.Skipped[0].Target != "anthropic:claude" || !strings.Contains(c.Skipped[0].Reason, "connection refused") {
		t.Errorf("findings %d, coverage %+v; want the other model's 3 and anthropic:claude skipped", len(cr.All), c)
	}
	// The failed model's call was made, so it counts.
	if c.LLMCalls != 2 || c.CacheHit {
		t.Errorf("llmCalls = %d, cacheHit %v; want both models' calls counted, no cache hit", c.LLMCalls, c.CacheHit)
	}

	useProviders(t, map[string]providers.Reviewer{}) // every model unavailable
	fresh := chunkTestConfig(t)                      // and nothing in the cache to replay
	fresh.ChunkBytes = 0
	if _, err := RunCompare(context.Background(), threeChunkDiff("beta"), []string{"openai:gpt", "anthropic:claude"}, fresh); err == nil {
		t.Error("with every model failed, the comparison should fail")
	}
}

// An auth failure, or a malformed spec, fails the comparison outright.
func TestRunCompare_AuthFailureAndBadSpec(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("PRISM_OPENAI_BASE_URL", srv.URL)
	old := newProvider
	newProvider = func(name, model string) (providers.Reviewer, error) {
		if name == "openai" {
			return providers.New(name, model)
		}
		return namedReviewer{&switchableReviewer{}, name}, nil
	}
	t.Cleanup(func() { newProvider = old })

	cfg := chunkTestConfig(t)
	cfg.ChunkBytes, cfg.RateLimitRPM = 0, 6000
	_, err := RunCompare(context.Background(), threeChunkDiff("beta"), []string{"openai:gpt-4o", "anthropic:claude"}, cfg)
	if err == nil || !providers.IsAuthError(err) || !strings.Contains(err.Error(), "openai:gpt-4o") {
		t.Errorf("err = %v, want the auth failure, naming the model", err)
	}

	// A bad spec anywhere fails before any model is called.
	calls := 0
	newProvider = func(name, _ string) (providers.Reviewer, error) {
		calls++
		return namedReviewer{&switchableReviewer{}, name}, nil
	}
	if _, err := RunCompare(context.Background(), threeChunkDiff("beta"), []string{"anthropic:claude", "openai"}, chunkTestConfig(t)); err == nil || calls != 0 {
		t.Errorf("err = %v after %d provider(s) created; want the spec rejected before any review", err, calls)
	}
}

// No models is an error, never a nil result.
func TestRunCompare_NoModels(t *testing.T) {
	if cr, err := RunCompare(context.Background(), threeChunkDiff("beta"), nil, chunkTestConfig(t)); err == nil || cr != nil {
		t.Errorf("RunCompare(no models) = %v, %v; want an error", cr, err)
	}
}

// A failed model means no cache hit, whatever order the models are in.
func TestCompareCoverage_CacheHitNeedsEveryModel(t *testing.T) {
	failed := compareModelResult{label: "a:x", err: errors.New("down")}
	hit := compareModelResult{label: "b:y", cov: Coverage{CacheHit: true, Chunks: 1, CachedChunks: 1}}
	for _, order := range [][]compareModelResult{{failed, hit}, {hit, failed}} {
		c := compareCoverage(threeChunkDiff("beta"), nil, order, []Skip{{Target: "a:x"}})
		if c.CacheHit {
			t.Errorf("order %s,%s: cache hit with a failed model", order[0].label, order[1].label)
		}
	}
	if c := compareCoverage(threeChunkDiff("beta"), nil, []compareModelResult{hit, hit}, nil); !c.CacheHit {
		t.Error("every model replayed: a cache hit")
	}
}
