package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
)

func TestSplitDiff(t *testing.T) {
	two := fileDiff("a.go", "x") + fileDiff("b.go", "y")
	a, b, ok := splitDiff(two)
	if !ok || a != fileDiff("a.go", "x") || b != fileDiff("b.go", "y") {
		t.Errorf("by file: %q | %q", a, b)
	}

	hunks := "diff --git a/c.go b/c.go\n--- a/c.go\n+++ b/c.go\n@@ -1 +1 @@\n-a\n+b\n@@ -9 +9 @@\n-c\n+d\n@@ -20 +20 @@\n-e\n+f\n"
	a, b, ok = splitDiff(hunks)
	if !ok || strings.Count(a, "\n@@ -") != 1 || strings.Count(b, "\n@@ -") != 2 || !strings.HasPrefix(b, "diff --git a/c.go") || !strings.HasSuffix(a, "+b\n") {
		t.Errorf("by hunk: %q | %q", a, b)
	}
	if got := filesOf(a); len(got) != 1 || got[0] != "c.go" {
		t.Errorf("each half keeps the file header: %v", got)
	}

	if _, _, ok := splitDiff(fileDiff("d.go", "z")); ok {
		t.Error("a single hunk cannot be split")
	}
}

// truncatingServer is an OpenAI-compatible endpoint that cuts off any
// answer to a prompt covering more than maxFiles files, or more than
// maxHunks hunks, unless the request allows at least bigLimit tokens, and
// any prompt containing alwaysCut; it answers others with one finding per
// file.
type truncatingServer struct {
	maxFiles, maxHunks, bigLimit int
	alwaysCut                    string // a diff containing this is always cut off
	mu                           sync.Mutex
	limits                       []int
}

var promptFiles = regexp.MustCompile(`(?m)^\+\+\+ b/(\S+)$`)

func (ts *truncatingServer) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var req struct {
			Messages            []struct{ Content string } `json:"messages"`
			MaxTokens           int                        `json:"max_tokens"`
			MaxCompletionTokens int                        `json:"max_completion_tokens"`
		}
		_ = json.Unmarshal(data, &req)
		limit := max(req.MaxTokens, req.MaxCompletionTokens)
		ts.mu.Lock()
		ts.limits = append(ts.limits, limit)
		ts.mu.Unlock()

		user := req.Messages[len(req.Messages)-1].Content
		diff, _, _ := strings.Cut(user, "\n--- END ")
		files := promptFiles.FindAllStringSubmatch(diff, -1)
		hunks := strings.Count(diff, "\n@@ -")
		endless := ts.alwaysCut != "" && strings.Contains(diff, ts.alwaysCut)
		if endless || (len(files) > ts.maxFiles || hunks > ts.maxHunks) && limit < ts.bigLimit {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"findings\":[{\"sev"},"finish_reason":"length"}]}`)
			return
		}
		var items []string
		for _, m := range files {
			items = append(items, fmt.Sprintf(`{"severity":"low","category":"bug","title":"Issue in %s","message":"m","suggestion":"s","confidence":0.5,"path":%q,"startLine":1,"endLine":1,"evidence":"","fix":{"before":"","after":""},"tags":[]}`, m[1], m[1]))
		}
		content, _ := json.Marshal(`{"findings":[` + strings.Join(items, ",") + `]}`)
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}]}`, content)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("PRISM_OPENAI_BASE_URL", srv.URL)
}

func (ts *truncatingServer) calls() []int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]int(nil), ts.limits...)
}

// A cut-off answer is not parsed: the part is split, and its halves'
// findings make up the review.
func TestRun_TruncatedResponseIsSplit(t *testing.T) {
	ts := &truncatingServer{maxFiles: 1, maxHunks: 99, bigLimit: 1 << 30}
	ts.start(t)
	cfg := chunkTestConfig(t)
	cfg.Provider, cfg.Model, cfg.ChunkBytes, cfg.RateLimitRPM = "openai", "gpt-4o", 0, 6000

	report, err := Run(context.Background(), threeChunkDiff("beta"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 3 {
		t.Errorf("%d findings, want one per file from the halves", len(report.Findings))
	}
	// 3 files: cut off; halves of 1 and 2 files; the 2 is cut off and halved.
	if c := report.Coverage; c.Splits != 2 || c.LLMCalls != 5 || !c.Complete {
		t.Errorf("coverage = %+v, want 2 splits, 5 calls, complete", c)
	}
	if !strings.Contains(report.Coverage.Describe(0), "2 parts split after a cut-off response") {
		t.Errorf("coverage line = %q", report.Coverage.Describe(0))
	}
}

// One file with several hunks is split by hunk.
func TestRun_TruncatedSingleFileSplitsByHunk(t *testing.T) {
	ts := &truncatingServer{maxFiles: 9, maxHunks: 1, bigLimit: 1 << 30}
	ts.start(t)
	cfg := chunkTestConfig(t)
	cfg.Provider, cfg.Model, cfg.ChunkBytes, cfg.RateLimitRPM = "openai", "gpt-4o", 0, 6000
	diff := threeChunkDiff("beta")
	diff.Diff = "diff --git a/c.go b/c.go\n--- a/c.go\n+++ b/c.go\n@@ -1 +1 @@\n-a\n+b\n@@ -9 +9 @@\n-c\n+d\n"

	report, err := Run(context.Background(), diff, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if report.Coverage.Splits != 1 || len(report.Findings) != 1 {
		t.Errorf("splits %d, findings %d; want 1 split, the halves' findings merged", report.Coverage.Splits, len(report.Findings))
	}
}

// A part that cannot be split is asked once more with twice the limit;
// still cut off, the review says so rather than reporting a partial list.
func TestRun_TruncatedUnsplittable(t *testing.T) {
	ts := &truncatingServer{maxFiles: 0, maxHunks: 0, bigLimit: 1 << 30}
	ts.start(t)
	cfg := chunkTestConfig(t)
	cfg.Provider, cfg.Model, cfg.ChunkBytes, cfg.RateLimitRPM = "openai", "gpt-4o", 0, 6000
	diff := threeChunkDiff("beta")
	diff.Diff = fileDiff("a.go", "x")

	_, err := Run(context.Background(), diff, cfg)
	if err == nil || !providers.IsTruncated(err) || !strings.Contains(err.Error(), "cannot be split further") {
		t.Fatalf("err = %v, want the truncation explained", err)
	}
	if got := ts.calls(); len(got) != 2 || got[1] != 2*got[0] {
		t.Errorf("limits asked for = %v, want the default, then twice it", got)
	}

	// A larger limit that is enough rescues it.
	ts2 := &truncatingServer{maxFiles: 0, maxHunks: 0, bigLimit: 2 * defaultMaxTokens}
	ts2.start(t)
	if report, err := Run(context.Background(), diff, chunkTestConfig2(t)); err != nil || len(report.Findings) != 1 {
		t.Errorf("with a big enough second limit: %v, %v", report, err)
	}
}

func chunkTestConfig2(t *testing.T) config.Config {
	t.Helper()
	c := chunkTestConfig(t)
	c.Provider, c.Model, c.ChunkBytes, c.RateLimitRPM = "openai", "gpt-4o", 0, 6000
	return c
}

// In a chunked review, a chunk that stays cut off is a skip: the other
// chunks' findings are kept and the review is incomplete.
func TestRun_TruncatedChunkIsSkipped(t *testing.T) {
	ts := &truncatingServer{maxFiles: 99, maxHunks: 99, bigLimit: 1 << 30, alwaysCut: "ENDLESS"}
	ts.start(t)
	cfg := chunkTestConfig(t)
	cfg.Provider, cfg.Model, cfg.RateLimitRPM = "openai", "gpt-4o", 6000

	report, err := Run(context.Background(), threeChunkDiff("ENDLESS"), cfg)
	if err != nil {
		t.Fatalf("one hopeless chunk should not fail the review: %v", err)
	}
	if got := strings.Join(findingPaths(report), ","); got != "d1/a.go,d3/c.go" {
		t.Errorf("findings = %s, want the other two chunks'", got)
	}
	c := report.Coverage
	if c.Complete || len(c.Skipped) != 1 || !strings.Contains(c.Skipped[0].Reason, "cut off") {
		t.Errorf("coverage = %+v, want d2's chunk skipped as cut off", c)
	}
}
