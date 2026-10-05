package review

import (
	"context"
	"sync"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/ratelimit"
)

// Throttle bounds the model calls of several reviews run at once: one rate
// limit and one cap on calls in flight, shared, so reviewing commits side by
// side asks the provider no faster than one review would.
type Throttle struct {
	limiter *ratelimit.Limiter
	sem     chan struct{}
}

// NewThrottle is the throttle for cfg's provider: its maxConcurrency and
// rateLimitRpm, or the provider's defaults.
func NewThrottle(cfg config.Config) *Throttle {
	concurrency := cfg.MaxConcurrency
	if concurrency <= 0 {
		concurrency = providers.DefaultMaxConcurrency(cfg.Provider)
	}
	rpm := cfg.RateLimitRPM
	if rpm <= 0 {
		rpm = providers.DefaultRPM(cfg.Provider)
	}
	return &Throttle{limiter: ratelimit.New(rpm), sem: make(chan struct{}, max(concurrency, 1))}
}

type throttleKey struct{}

// WithThrottle makes every review run with ctx share t.
func WithThrottle(ctx context.Context, t *Throttle) context.Context {
	return context.WithValue(ctx, throttleKey{}, t)
}

func throttleFrom(ctx context.Context) *Throttle {
	t, _ := ctx.Value(throttleKey{}).(*Throttle)
	return t
}

// acquire takes a slot for one part's calls, or returns false when ctx is
// done first.
func (t *Throttle) acquire(ctx context.Context) bool {
	select {
	case t.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (t *Throttle) release() { <-t.sem }

// CommitReview is one commit's part of a per-commit range review.
type CommitReview struct {
	Commit gitctx.CommitInfo
	// Files is the files the commit's diff covered. The diff itself is not
	// kept: a long range would hold every commit's diff at once.
	Files []string
	// Report is the commit's review; nil when the diff was empty or there
	// was an error.
	Report *Report
	// DiffErr is set when the commit's diff could not be read, Err when it
	// was read and its review failed.
	DiffErr error
	Err     error
}

// commitConcurrency caps the commits reviewed at once. Their model calls
// are bounded by the shared Throttle; this bounds the git work and memory.
const commitConcurrency = 4

// ReviewCommits reviews each commit on its own, several at once, sharing one
// Throttle so the provider sees no more traffic than one review would make.
// The results are in commit order. diffOf reads a commit's diff; started,
// if set, is called as each commit's review begins. An authentication error
// stops the commits not yet started, since every one would fail the same
// way; their results carry the context's error.
func ReviewCommits(ctx context.Context, commits []gitctx.CommitInfo, cfg config.Config,
	diffOf func(context.Context, gitctx.CommitInfo) (gitctx.DiffResult, error), started func(i int)) []CommitReview {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if throttleFrom(ctx) == nil {
		ctx = WithThrottle(ctx, NewThrottle(cfg))
	}

	results := make([]CommitReview, len(commits))
	var wg sync.WaitGroup
	slots := make(chan struct{}, commitConcurrency)
	var startMu sync.Mutex
	for i, c := range commits {
		results[i].Commit = c
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			results[i].Err = ctx.Err()
			continue
		}
		wg.Add(1)
		go func(i int, c gitctx.CommitInfo) {
			defer wg.Done()
			defer func() { <-slots }()
			if ctx.Err() != nil {
				results[i].Err = ctx.Err()
				return
			}
			if started != nil {
				startMu.Lock()
				started(i)
				startMu.Unlock()
			}
			r := &results[i]
			diff, err := diffOf(ctx, c)
			r.DiffErr, r.Files = err, diff.Files
			if err != nil || emptyDiff(diff) {
				return
			}
			r.Report, r.Err = Run(ctx, diff, cfg)
			if providers.IsAuthError(r.Err) {
				cancel()
			}
		}(i, c)
	}
	wg.Wait()
	return results
}

func emptyDiff(d gitctx.DiffResult) bool {
	for _, c := range d.Diff {
		if c != ' ' && c != '\n' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}
