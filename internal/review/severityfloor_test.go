package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
)

func TestWriteSeverityLines(t *testing.T) {
	const floorLine = "Report only findings with severity"
	const focusLine = "Focus especially on findings with severity"
	cases := []struct {
		failOn, min string
		floor       bool
		focus       string // "" for no focus line
	}{
		{"high", "", false, "high"},
		{"none", "", false, ""},
		{"low", "none", false, "low"},
		{"high", "low", false, "high"},   // low is every severity: no floor line
		{"high", "medium", true, "high"}, // gate above the floor still gets focus
		{"medium", "medium", true, ""},   // the floor already says it
		{"medium", "high", true, ""},     // a floor above the gate
		{"none", "high", true, ""},
	}
	for _, c := range cases {
		p := buildUserPrompt("diff", nil, 0, c.failOn, c.min, nil)
		if got := strings.Contains(p, floorLine+" "+c.min+" or above"); got != c.floor {
			t.Errorf("failOn=%s min=%s: floor line %v, want %v\n%s", c.failOn, c.min, got, c.floor, p)
		}
		hasFocus := strings.Contains(p, focusLine)
		if c.focus == "" && hasFocus || c.focus != "" && !strings.Contains(p, focusLine+" "+c.focus+" ") {
			t.Errorf("failOn=%s min=%s: focus line wrong, want %q\n%s", c.failOn, c.min, c.focus, p)
		}
	}
	if p := BuildCodebaseUserPrompt("src", nil, 0, 0, "high", "medium", nil); !strings.Contains(p, floorLine+" medium") {
		t.Errorf("codebase prompt has no floor line:\n%s", p)
	}
}

// The floor is part of the prompt, so it is part of every cache key: a
// review cached without it is not replayed as one that had it.
func TestMinSeverity_ChangesPromptFingerprint(t *testing.T) {
	cfg := config.Default()
	base := promptFingerprint(defaultPromptBuilder, cfg, nil)
	cfg.MinSeverity = "high"
	if promptFingerprint(defaultPromptBuilder, cfg, nil) == base {
		t.Error("minSeverity high left the prompt fingerprint unchanged")
	}
	cfg.MinSeverity = "low" // every severity: the same prompt as none
	if promptFingerprint(defaultPromptBuilder, cfg, nil) != base {
		t.Error("minSeverity low changed the prompt fingerprint")
	}
}

// Findings below the floor are dropped whatever the model wrote, before the
// limit, so they never use it up.
func TestFinalizeFindings_DropsBelowMinSeverity(t *testing.T) {
	cfg := config.Default()
	off := false
	cfg.VerifyFindings = &off
	cfg.BaselineFile = "none"
	cfg.MinSeverity = "medium"
	cfg.MaxFindings = 2
	findings := []Finding{
		{ID: "l1", Severity: SeverityLow, Title: "l1"},
		{ID: "h1", Severity: SeverityHigh, Title: "h1"},
		{ID: "l2", Severity: SeverityLow, Title: "l2"},
		{ID: "m1", Severity: SeverityMedium, Title: "m1"},
	}
	got, _, suppressed, err := FinalizeFindings(context.Background(), findings, gitctx.DiffResult{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "h1" || got[1].ID != "m1" {
		t.Errorf("kept %+v, want h1 and m1", got)
	}
	if len(suppressed) != 0 {
		t.Errorf("below-floor findings listed as suppressed: %+v", suppressed)
	}
	if findings[0].ID != "l1" {
		t.Error("the caller's slice was rewritten")
	}
}

func TestChunkFindingLimit(t *testing.T) {
	cases := []struct{ total, n, want int }{
		{50, 1, 50},
		{50, 2, 50},
		{50, 3, 34},
		{50, 4, 25},
		{50, 10, 10},
		{50, 40, 10}, // the floor
		{5, 10, 5},   // never above the total
		{0, 10, 0},   // no limit stays no limit
	}
	for _, c := range cases {
		if got := chunkFindingLimit(c.total, c.n); got != c.want {
			t.Errorf("chunkFindingLimit(%d, %d) = %d, want %d", c.total, c.n, got, c.want)
		}
	}
}

// Each chunk is asked for its share of maxFindings, and the share is not in
// the cache key: the same chunks in a review of a different size replay.
func TestRun_ChunksAskForTheirShare(t *testing.T) {
	rev := &fileReviewer{}
	useProvider(t, rev)
	cfg := chunkTestConfig(t)
	cfg.MaxFindings = 50
	ctx := context.Background()

	if _, err := Run(ctx, threeChunkDiff("beta"), cfg); err != nil {
		t.Fatal(err)
	}
	prompts := rev.calls()
	if len(prompts) != 3 {
		t.Fatalf("%d calls, want 3", len(prompts))
	}
	for _, p := range prompts {
		if !strings.Contains(p, "Return at most 34 findings.") {
			t.Errorf("chunk prompt does not ask for its share:\n%s", p)
		}
	}

	two := threeChunkDiff("beta")
	two.Diff = fileDiff("d1/a.go", "alpha") + fileDiff("d2/b.go", "beta")
	two.Files = two.Files[:2]
	report, err := Run(ctx, two, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rev.calls()); n != 0 || report.Coverage.CachedChunks != 2 {
		t.Errorf("two of the same chunks: %d calls, %d from cache; want 0 and 2", n, report.Coverage.CachedChunks)
	}
}

// countReviewer answers every prompt with n findings on the file it names.
type countReviewer struct {
	mu    sync.Mutex
	n     int
	calls int
}

func (r *countReviewer) Review(_ context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	path := promptFile.FindStringSubmatch(req.UserPrompt)[1]
	items := make([]string, r.n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"severity":"medium","category":"bug","title":"Issue %d","message":"m","suggestion":"s","confidence":0.9,"path":%q,"startLine":1,"endLine":1,"tags":[]}`, i, path)
	}
	return providers.ReviewResponse{Content: "[" + strings.Join(items, ",") + "]", Provider: "mock", Model: "m"}, nil
}

func (r *countReviewer) Name() string { return "mock" }

// A chunk whose answer fills its lowered share may have left findings out,
// so it is not cached; one with room to spare is.
func TestRun_ChunkThatFillsItsShareIsNotCached(t *testing.T) {
	for _, c := range []struct {
		n, wantCalls int
	}{
		{n: 10, wantCalls: 3}, // the share of 15 over 3 chunks is 10: full
		{n: 9, wantCalls: 0},
	} {
		rev := &countReviewer{n: c.n}
		useProvider(t, rev)
		cfg := chunkTestConfig(t)
		cfg.MaxFindings = 15
		ctx := context.Background()
		if _, err := Run(ctx, threeChunkDiff("beta"), cfg); err != nil {
			t.Fatal(err)
		}
		rev.calls = 0
		if _, err := Run(ctx, threeChunkDiff("beta"), cfg); err != nil {
			t.Fatal(err)
		}
		if rev.calls != c.wantCalls {
			t.Errorf("%d findings a chunk: rerun made %d calls, want %d", c.n, rev.calls, c.wantCalls)
		}
	}
}
