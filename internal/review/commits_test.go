package review

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
)

// inflightReviewer answers after a pause, recording the most calls it had
// in flight at once.
type inflightReviewer struct {
	now, peak atomic.Int32
	mu        sync.Mutex
	files     []string
}

func (r *inflightReviewer) Review(ctx context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	n := r.now.Add(1)
	defer r.now.Add(-1)
	for {
		p := r.peak.Load()
		if n <= p || r.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	m := promptFile.FindStringSubmatch(req.UserPrompt)
	r.mu.Lock()
	r.files = append(r.files, m[1])
	r.mu.Unlock()
	return providers.ReviewResponse{Content: fmt.Sprintf(`[{"severity":"low","category":"bug","title":"t","message":"m","suggestion":"s","confidence":0.5,"path":%q,"startLine":1,"endLine":1}]`, m[1]), Provider: "mock", Model: "m"}, nil
}

func (r *inflightReviewer) Name() string { return "mock" }

func someCommits(n int) []gitctx.CommitInfo {
	var cs []gitctx.CommitInfo
	for i := range n {
		cs = append(cs, gitctx.CommitInfo{SHA: fmt.Sprintf("%040d", i), Subject: fmt.Sprintf("commit %d", i)})
	}
	return cs
}

func diffForCommit(_ context.Context, c gitctx.CommitInfo) (gitctx.DiffResult, error) {
	path := "c" + strings.TrimLeft(c.SHA, "0") + ".go"
	if path == "c.go" {
		path = "c0.go"
	}
	return gitctx.DiffResult{Diff: fileDiff(path, "x"), Files: []string{path}, Mode: "commit"}, nil
}

// Commits are reviewed side by side, never more calls in flight than the
// shared throttle allows, and the results come back in commit order.
func TestReviewCommits_ConcurrentInOrder(t *testing.T) {
	rev := &inflightReviewer{}
	useProvider(t, rev)
	cfg := chunkTestConfig(t)
	cfg.ChunkBytes = 0
	cfg.MaxConcurrency = 2
	cfg.RateLimitRPM = 6000

	var startedN atomic.Int32
	results := ReviewCommits(context.Background(), someCommits(6), cfg, diffForCommit, func(int) { startedN.Add(1) })
	if len(results) != 6 || startedN.Load() != 6 {
		t.Fatalf("%d results, %d started", len(results), startedN.Load())
	}
	for i, r := range results {
		if r.Err != nil || r.DiffErr != nil || r.Report == nil {
			t.Fatalf("commit %d: %+v", i, r)
		}
		want := fmt.Sprintf("c%d.go", i)
		if got := findingPath(r.Report.Findings[0]); got != want {
			t.Errorf("result %d is for %s, want %s", i, got, want)
		}
	}
	if p := rev.peak.Load(); p != 2 {
		t.Errorf("peak calls in flight = %d, want 2 (shared throttle, and side by side)", p)
	}
}

// An empty diff gives no report; a diff error is reported for its commit.
func TestReviewCommits_EmptyAndDiffError(t *testing.T) {
	useProvider(t, &inflightReviewer{})
	cfg := chunkTestConfig(t)
	results := ReviewCommits(context.Background(), someCommits(3), cfg, func(ctx context.Context, c gitctx.CommitInfo) (gitctx.DiffResult, error) {
		switch c.SHA[39] {
		case '0':
			return gitctx.DiffResult{Diff: "  \n"}, nil
		case '1':
			return gitctx.DiffResult{}, fmt.Errorf("bad object")
		}
		return diffForCommit(ctx, c)
	}, nil)
	if results[0].Report != nil || results[0].Err != nil || results[1].DiffErr == nil || results[2].Report == nil {
		t.Errorf("results: %+v", results)
	}
}

// An authentication error stops the commits not yet started.
func TestReviewCommits_AuthErrorStops(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_API_KEY", "bad")
	t.Setenv("PRISM_OPENAI_BASE_URL", srv.URL)
	cfg := chunkTestConfig(t)
	cfg.Provider, cfg.Model, cfg.ChunkBytes = "openai", "gpt-4o", 0

	results := ReviewCommits(context.Background(), someCommits(20), cfg, diffForCommit, nil)
	auth := 0
	for _, r := range results {
		if providers.IsAuthError(r.Err) {
			auth++
		}
	}
	if auth == 0 {
		t.Fatal("no authentication error reported")
	}
	if n := requests.Load(); n >= 20 {
		t.Errorf("%d requests: the commits after the auth error were still reviewed", n)
	}
}
