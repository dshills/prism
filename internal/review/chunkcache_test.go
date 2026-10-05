package review

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
)

// fileReviewer reports one finding per file in each prompt it is sent, and
// fails any prompt whose diff contains failOn (while failOn is set).
type fileReviewer struct {
	mu      sync.Mutex
	prompts []string
	failOn  string
}

var promptFile = regexp.MustCompile(`(?m)^\+\+\+ b/(\S+)$`)

func (r *fileReviewer) Review(_ context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, req.UserPrompt)
	failOn := r.failOn
	r.mu.Unlock()

	diff, _, _ := strings.Cut(req.UserPrompt, "--- END DIFF ---")
	if failOn != "" && strings.Contains(diff, failOn) {
		return providers.ReviewResponse{}, errors.New("provider unavailable")
	}
	var items []string
	for _, m := range promptFile.FindAllStringSubmatch(diff, -1) {
		items = append(items, fmt.Sprintf(
			`{"severity":"medium","category":"bug","title":"Issue in %s","message":"m","suggestion":"s","confidence":0.9,"path":%q,"startLine":1,"endLine":1,"tags":[]}`,
			m[1], m[1]))
	}
	return providers.ReviewResponse{Content: "[" + strings.Join(items, ",") + "]", Provider: "mock", Model: "m"}, nil
}

func (r *fileReviewer) Name() string { return "mock" }

func (r *fileReviewer) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.prompts
	r.prompts = nil
	return out
}

// useProvider makes the pipeline review with r for the rest of the test.
func useProvider(t *testing.T, r providers.Reviewer) {
	t.Helper()
	old := newProvider
	newProvider = func(_, _ string) (providers.Reviewer, error) { return r, nil }
	t.Cleanup(func() { newProvider = old })
}

// fileDiff is one file's section of a unified diff.
func fileDiff(path, line string) string {
	return fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-old\n+%s\n", path, path, path, path, line)
}

// threeChunkDiff is a diff of three files in three directories, each in a
// chunk of its own under chunkTestConfig's chunk size.
func threeChunkDiff(second string) gitctx.DiffResult {
	files := []string{"d1/a.go", "d2/b.go", "d3/c.go"}
	diff := fileDiff(files[0], "alpha") + fileDiff(files[1], second) + fileDiff(files[2], "gamma")
	return gitctx.DiffResult{Diff: diff, Files: files, Mode: "staged"}
}

func chunkTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Provider, cfg.Model = "mock", "m"
	cfg.Cache.Enabled = true
	cfg.Cache.Dir = t.TempDir()
	cfg.Cache.TTLSeconds = 3600
	cfg.ChunkBytes = 120
	off := false
	cfg.VerifyFindings = &off
	return cfg
}

func findingPaths(r *Report) []string {
	var paths []string
	for _, f := range r.Findings {
		paths = append(paths, findingPath(f))
	}
	return paths
}

// A fix-loop re-review that edits one file sends only that file's chunk, and
// still reports the findings of the chunks replayed from cache.
func TestRun_ChunkCacheReviewsOnlyChangedChunks(t *testing.T) {
	rev := &fileReviewer{}
	useProvider(t, rev)
	cfg := chunkTestConfig(t)
	ctx := context.Background()

	first, err := Run(ctx, threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rev.calls()); n != 3 || first.Coverage.Chunks != 3 {
		t.Fatalf("first run: %d calls over %d chunks, want 3 over 3", n, first.Coverage.Chunks)
	}
	if first.Coverage.CachedChunks != 0 || first.Coverage.CacheHit {
		t.Errorf("first run coverage = %+v, want nothing from cache", first.Coverage)
	}

	again, err := Run(ctx, threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rev.calls()); n != 0 {
		t.Errorf("unchanged diff: %d calls, want 0", n)
	}
	if c := again.Coverage; !c.CacheHit || c.CachedChunks != 3 || c.LLMCalls != 0 {
		t.Errorf("unchanged diff coverage = %+v, want a full cache hit", c)
	}

	edited, err := Run(ctx, threeChunkDiff("beta, fixed"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	prompts := rev.calls()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "+++ b/d2/b.go") {
		t.Fatalf("one edited file: sent %d prompts, want only d2/b.go's chunk", len(prompts))
	}
	// The chunk sent alone is still told about the parts replayed from cache.
	for _, other := range []string{"- d1/a.go", "- d3/c.go"} {
		if !strings.Contains(prompts[0], other) {
			t.Errorf("prompt does not list other part %q:\n%s", other, prompts[0])
		}
	}
	if c := edited.Coverage; c.CacheHit || c.CachedChunks != 2 || c.LLMCalls != 1 || c.Chunks != 3 {
		t.Errorf("edited coverage = %+v, want 3 chunks, 2 cached, 1 call", c)
	}
	if got := strings.Join(findingPaths(edited), ","); got != "d1/a.go,d2/b.go,d3/c.go" {
		t.Errorf("findings = %s, want one per file, cached and fresh", got)
	}
	if line := edited.Coverage.Describe(edited.Timing.LLMMs); !strings.Contains(line, "in 3 chunks (2 from cache)") {
		t.Errorf("coverage line = %q, want it to say 2 chunks came from cache", line)
	}
}

// When one chunk fails, the chunks that succeeded are cached, so a rerun sends
// only the one that failed.
func TestRun_ChunkCacheKeepsSucceededChunksOnFailure(t *testing.T) {
	rev := &fileReviewer{failOn: "d2/b.go"}
	useProvider(t, rev)
	cfg := chunkTestConfig(t)
	ctx := context.Background()

	if _, err := Run(ctx, threeChunkDiff("beta"), cfg); err == nil {
		t.Fatal("expected the failed chunk to fail the review")
	}
	rev.calls()

	rev.mu.Lock()
	rev.failOn = ""
	rev.mu.Unlock()
	report, err := Run(ctx, threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	prompts := rev.calls()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "+++ b/d2/b.go") {
		t.Fatalf("rerun sent %d prompts, want only the chunk that failed", len(prompts))
	}
	if report.Coverage.CachedChunks != 2 || len(report.Findings) != 3 {
		t.Errorf("rerun: %d cached chunks, %d findings; want 2 and 3", report.Coverage.CachedChunks, len(report.Findings))
	}
}

// A diff that fits in one prompt is still cached as a whole.
func TestRun_SingleChunkCacheHit(t *testing.T) {
	rev := &fileReviewer{}
	useProvider(t, rev)
	cfg := chunkTestConfig(t)
	cfg.ChunkBytes = 0 // default: the whole diff is one chunk
	diff := threeChunkDiff("beta")

	if _, err := Run(context.Background(), diff, cfg); err != nil {
		t.Fatal(err)
	}
	if n := len(rev.calls()); n != 1 {
		t.Fatalf("first run: %d calls, want 1", n)
	}
	report, err := Run(context.Background(), diff, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rev.calls()); n != 0 {
		t.Errorf("second run: %d calls, want 0", n)
	}
	if c := report.Coverage; !c.CacheHit || c.Chunks != 1 || c.CachedChunks != 1 {
		t.Errorf("coverage = %+v, want a full hit of 1 chunk", c)
	}
}

// A chunk's key is its own text and the prompt, not where it sits in the diff
// or what the other chunks hold.
func TestChunkCacheKey(t *testing.T) {
	cfg := config.Default()
	a := chunkCacheKey(cfg, "p", fileDiff("d1/a.go", "x"))
	if a != chunkCacheKey(cfg, "p", fileDiff("d1/a.go", "x")) {
		t.Error("same chunk, different key")
	}
	if a == chunkCacheKey(cfg, "p", fileDiff("d1/a.go", "y")) {
		t.Error("chunk key ignores the chunk's content")
	}
	if a == chunkCacheKey(cfg, "q", fileDiff("d1/a.go", "x")) {
		t.Error("chunk key ignores the prompt")
	}
	if a == diffCacheKey(cfg, "p", fileDiff("d1/a.go", "x")) {
		t.Error("a chunk and a whole diff with the same text share a key")
	}
}
