package review

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
)

// fakeOpenAI stands in for the OpenAI chat-completions endpoint and counts
// requests. reply decides each response from the request number (1-based) and
// the prompt body: a status code and the message content.
func fakeOpenAI(t *testing.T, reply func(n int32, body string) (int, string)) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		status, content := reply(n, string(b))
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"scripted failure"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("PRISM_OPENAI_BASE_URL", srv.URL)
	return &calls
}

func coverageConfig(t *testing.T, cacheOn bool) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Provider, cfg.Model = "openai", "test-model"
	cfg.Cache.Enabled = cacheOn
	cfg.Cache.Dir = t.TempDir()
	return cfg
}

func diffResult(diff string, files ...string) gitctx.DiffResult {
	return gitctx.DiffResult{Diff: diff, Files: files, Mode: "staged"}
}

func always(content string) func(int32, string) (int, string) {
	return func(int32, string) (int, string) { return http.StatusOK, content }
}

func TestRunCoverage_Unchunked(t *testing.T) {
	calls := fakeOpenAI(t, always("[]"))
	diff := section("a/x.go", 400)
	r, err := Run(context.Background(), diffResult(diff, "a/x.go"), coverageConfig(t, false))
	if err != nil {
		t.Fatal(err)
	}
	c := r.Coverage
	if c.Chunks != 1 || c.LLMCalls != 1 || c.CacheHit || !c.Complete || c.Files != 1 || c.Bytes != len(diff) {
		t.Errorf("coverage = %+v", c)
	}
	if len(c.Reviewer) != 1 || c.Reviewer[0] != (Reviewer{"openai", "test-model"}) {
		t.Errorf("reviewer = %+v, want openai/test-model even with no findings", c.Reviewer)
	}
	if calls.Load() != 1 {
		t.Errorf("server saw %d calls, want 1", calls.Load())
	}
}

func TestRunCoverage_Chunked(t *testing.T) {
	fakeOpenAI(t, always("[]"))
	cfg := coverageConfig(t, false)
	cfg.ChunkBytes = 1000
	diff := section("a/x.go", 900) + section("b/y.go", 900) + section("c/z.go", 900)
	r, err := Run(context.Background(), diffResult(diff, "a/x.go", "b/y.go", "c/z.go"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Coverage.Chunks != 3 || r.Coverage.LLMCalls != 3 {
		t.Errorf("chunked coverage = %+v, want 3 chunks, 3 calls", r.Coverage)
	}
}

// A repair pass is a second model call and is counted.
func TestRunCoverage_RepairCounted(t *testing.T) {
	fakeOpenAI(t, func(n int32, _ string) (int, string) {
		if n == 1 {
			return http.StatusOK, "not json"
		}
		return http.StatusOK, "[]"
	})
	r, err := Run(context.Background(), diffResult(section("a/x.go", 300), "a/x.go"), coverageConfig(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if r.Coverage.LLMCalls != 2 {
		t.Errorf("LLMCalls = %d, want 2 (review + repair)", r.Coverage.LLMCalls)
	}
}

// A replay says so: no model call, but the chunk count and reviewer remain.
func TestRunCoverage_CacheHit(t *testing.T) {
	calls := fakeOpenAI(t, always("[]"))
	cfg := coverageConfig(t, true)
	d := diffResult(section("a/x.go", 300), "a/x.go")
	if _, err := Run(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Coverage
	if !c.CacheHit || c.LLMCalls != 0 || c.Chunks != 1 || len(c.Reviewer) != 1 {
		t.Errorf("replayed coverage = %+v", c)
	}
	if calls.Load() != 1 {
		t.Errorf("server saw %d calls, want 1 (second run replayed)", calls.Load())
	}
	if !strings.HasPrefix(c.Describe(r.Timing.LLMMs), "Replayed from cache") {
		t.Errorf("Describe = %q", c.Describe(r.Timing.LLMMs))
	}
}

// Bytes cut at maxDiffBytes were not reviewed: incomplete, with the reason.
func TestRunCoverage_Truncated(t *testing.T) {
	fakeOpenAI(t, always("[]"))
	body := section("a/x.go", 300)
	d := diffResult(body+gitctx.TruncationMarker, "a/x.go")
	d.TruncatedBytes = 1234
	r, err := Run(context.Background(), d, coverageConfig(t, false))
	if err != nil {
		t.Fatal(err)
	}
	c := r.Coverage
	if c.Complete || !c.Incomplete() || c.TruncatedBytes != 1234 || c.Bytes != len(body) {
		t.Errorf("truncated coverage = %+v", c)
	}
	if len(c.Skipped) != 1 || c.Skipped[0].Target != "diff" || !strings.Contains(c.Skipped[0].Reason, "1234 bytes not reviewed") {
		t.Errorf("skipped = %+v", c.Skipped)
	}
}

func TestCoverageDescribe(t *testing.T) {
	who := ConfigReviewer("openai", "gpt-6-sol")
	reviewed := Coverage{Reviewer: who, Files: 41, Bytes: 79400, Chunks: 5, LLMCalls: 5}
	if got, want := reviewed.Describe(57300), "Reviewed 41 files (79.4 KB) in 5 chunks by openai/gpt-6-sol — 5 LLM calls, 57.3s"; got != want {
		t.Errorf("reviewed:\n got %q\nwant %q", got, want)
	}
	one := Coverage{Reviewer: who, Files: 1, Bytes: 500, Chunks: 1, LLMCalls: 1}
	if got := one.Describe(1000); !strings.Contains(got, "1 file (0.5 KB) in 1 chunk") || !strings.Contains(got, "1 LLM call,") {
		t.Errorf("singular forms: %q", got)
	}
	cached := Coverage{Reviewer: who, Files: 41, Bytes: 79400, Chunks: 5, CacheHit: true}
	if got, want := cached.Describe(0), "Replayed from cache: 41 files (79.4 KB), originally reviewed by openai/gpt-6-sol"; got != want {
		t.Errorf("cached:\n got %q\nwant %q", got, want)
	}
	if got := (Coverage{Reviewer: who}).Describe(0); got != "Nothing to review (empty diff)" {
		t.Errorf("empty: %q", got)
	}
	if got := (Coverage{Reviewer: who, Skipped: []Skip{{"abc1234", "review failed"}}}).Describe(0); got != "Nothing reviewed" {
		t.Errorf("all skipped: %q", got)
	}
	if got := (Coverage{}).Describe(0); got != "" {
		t.Errorf("unrecorded coverage should print nothing, got %q", got)
	}
	cmp := Coverage{Reviewer: ReviewersFromSpecs([]string{"openai:a", "gemini:b", "bad"}), Files: 2, Bytes: 2000, Chunks: 2, LLMCalls: 2}
	if got := cmp.Describe(0); !strings.Contains(got, "by openai/a, gemini/b") {
		t.Errorf("compare reviewers: %q", got)
	}
}

// Incomplete is defined by what was missed, so a zero-value (never recorded)
// coverage is not mistaken for an incomplete review.
func TestCoverageIncomplete(t *testing.T) {
	if (Coverage{}).Incomplete() || (Coverage{}).IncompleteLine() != "" {
		t.Error("zero-value coverage reported as incomplete")
	}
	c := NewCoverage(ConfigReviewer("p", "m"), 1, 10, 0)
	c.Skipped = append(c.Skipped, Skip{"abc1234", "review failed: boom"})
	c.Finalize()
	if c.Complete || !c.Incomplete() || !strings.Contains(c.IncompleteLine(), "abc1234: review failed: boom") {
		t.Errorf("coverage %+v, line %q", c, c.IncompleteLine())
	}
}

// Per-commit sums: a cache hit only if every part was one.
func TestCoverageAdd(t *testing.T) {
	var sum Coverage
	sum.Add(Coverage{Reviewer: ConfigReviewer("p", "m"), Files: 2, Bytes: 100, Chunks: 1, CacheHit: true}, true)
	sum.Add(Coverage{Reviewer: ConfigReviewer("p", "m"), Files: 3, Bytes: 200, Chunks: 2, LLMCalls: 2}, false)
	sum.Finalize()
	if sum.Files != 5 || sum.Bytes != 300 || sum.Chunks != 3 || sum.LLMCalls != 2 || sum.CacheHit || len(sum.Reviewer) != 1 || !sum.Complete {
		t.Errorf("sum = %+v", sum)
	}
}
