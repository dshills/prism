package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
)

// CompareResult holds results from multi-model comparison.
type CompareResult struct {
	Consensus []Finding            // Findings that appeared in >=2 models
	Unique    map[string][]Finding // Unique findings per model (key: "provider:model")
	All       []Finding            // All merged findings for the report
	LLMMs     int64
	Calls     int // model calls made, across every model
	// Coverage combines what each model's review covered: chunks, calls,
	// cache, tokens, and a skip for each model that failed. It is finalized.
	Coverage Coverage
}

// compareModelResult holds the output from a single model's review.
type compareModelResult struct {
	label    string
	findings []Finding
	err      error
	llmMs    int64
	cov      Coverage
}

// CompareOptions controls how compare mode constructs prompts.
type CompareOptions struct {
	Builder PromptBuilder // nil = use default diff prompts
	// AlwaysChunk chunks the diff even when it is small (codebase reviews).
	AlwaysChunk bool
}

// RunCompare runs reviews independently across multiple provider:model pairs
// and merges findings.
func RunCompare(ctx context.Context, diff gitctx.DiffResult, models []string, cfg config.Config) (*CompareResult, error) {
	return RunCompareWithOptions(ctx, diff, models, cfg, CompareOptions{})
}

// RunCompareWithOptions reviews diff with every model in models
// ("provider:model"), concurrently, each through the same pipeline as a
// single-model review up to its findings (collectFindings): chunking,
// per-chunk cache, repair, cut-off splitting, its provider's rate limits,
// and partial results. It then merges them by fuzzy matching. The merged
// findings are not yet verified, suppressed or limited; the caller
// finalizes them. cfg's fallback does not apply, as each model is named.
//
// A malformed spec or an auth failure fails the comparison. A model that
// fails otherwise is left out, as a coverage skip that makes the report
// incomplete, unless every model failed.
func RunCompareWithOptions(ctx context.Context, diff gitctx.DiffResult, models []string, cfg config.Config, opts CompareOptions) (*CompareResult, error) {
	// Every spec is checked before any review starts, so a bad one cannot
	// leave the others running (and paying) after the comparison failed.
	if len(models) == 0 {
		return nil, errors.New("compare needs at least one model")
	}
	type spec struct{ label, provider, model string }
	specs := make([]spec, len(models))
	for i, m := range models {
		providerName, modelName, err := parseModelSpec(m)
		if err != nil {
			return nil, err
		}
		specs[i] = spec{m, providerName, modelName}
	}

	results := make([]compareModelResult, len(models))
	var wg sync.WaitGroup
	for i, sp := range specs {
		wg.Add(1)
		go func(i int, spec string, providerName, modelName string) {
			defer wg.Done()
			mcfg := cfg
			mcfg.Provider, mcfg.Model, mcfg.Fallback = providerName, modelName, ""
			findings, cov, llmMs, _, err := collectFindings(ctx, diff, mcfg, reviewOpts{builder: opts.Builder, alwaysChunk: opts.AlwaysChunk})
			if err != nil {
				err = fmt.Errorf("%s: %w", spec, err)
			}
			results[i] = compareModelResult{label: spec, findings: findings, err: err, llmMs: llmMs, cov: cov}
		}(i, sp.label, sp.provider, sp.model)
	}
	wg.Wait()

	var ok []compareModelResult
	var skips []Skip
	var firstErr error
	var totalLLMMs int64
	for _, r := range results {
		totalLLMMs += r.llmMs
		if r.err == nil {
			ok = append(ok, r)
			continue
		}
		if providers.IsAuthError(r.err) || ctx.Err() != nil {
			return nil, r.err
		}
		if firstErr == nil {
			firstErr = r.err
		}
		skips = append(skips, Skip{Target: r.label, Reason: "review failed: " + r.err.Error()})
	}
	if len(ok) == 0 {
		return nil, firstErr
	}

	cr := mergeResults(ok, totalLLMMs)
	cr.Coverage = compareCoverage(diff, ReviewersFromSpecs(models), results, skips)
	cr.Calls = cr.Coverage.LLMCalls
	return cr, nil
}

// compareCoverage combines the models' coverage into the comparison's: the
// diff's size and exclusions once, and every model's chunks, calls, cache
// use, splits and tokens summed, a failed model's included, since its calls
// were made and paid for before it failed. A model's own skips are named for
// it, and failed models are skipped as a whole. It is a cache hit only if
// every model's review was.
func compareCoverage(diff gitctx.DiffResult, reviewers []Reviewer, results []compareModelResult, failed []Skip) Coverage {
	c := NewCoverage(reviewers, len(diff.Files), diff.ReviewedBytes(), diff.TruncatedBytes)
	c.Excluded = diff.Excluded
	c.WidenedFiles = diff.WidenedFiles
	allHit := len(results) > 0
	for _, r := range results {
		m := r.cov
		c.Chunks += m.Chunks
		c.LLMCalls += m.LLMCalls
		c.CachedChunks += m.CachedChunks
		c.Splits += m.Splits
		c.Tokens = mergeTokens(c.Tokens, m.Tokens)
		allHit = allHit && r.err == nil && m.CacheHit
		if r.err != nil {
			continue // its findings are out; the failure is a skip of its own
		}
		for _, s := range m.Skipped {
			if s.Target == "diff" {
				continue // the truncation skip, recorded once above
			}
			c.Skipped = append(c.Skipped, Skip{Target: r.label + " " + s.Target, Reason: s.Reason})
		}
	}
	c.Skipped = append(c.Skipped, failed...)
	c.CacheHit = allHit // only when every model's review was replayed
	c.Finalize()
	return c
}

func mergeResults(results []compareModelResult, totalLLMMs int64) *CompareResult {
	cr := &CompareResult{
		Unique: make(map[string][]Finding),
		LLMMs:  totalLLMMs,
	}

	if len(results) == 0 {
		return cr
	}

	// Track which findings from each model match findings from other models.
	// A finding is "consensus" if it appears in >=2 models (by fuzzy match).
	type matchKey struct {
		modelIdx   int
		findingIdx int
	}
	matchCounts := make(map[matchKey]int)

	// Bucket findings by path so fuzzyMatch only runs on candidates that
	// can possibly match (fuzzyMatch rejects cross-path pairs immediately).
	// This turns the worst case from O(total²) into O(sum(k²)) per path.
	// Entries hold only indices to avoid copying Finding structs.
	type bucketEntry struct {
		modelIdx   int
		findingIdx int
	}
	byPath := make(map[string][]bucketEntry)
	for i, r := range results {
		for fi, f := range r.findings {
			p := findingPath(f)
			byPath[p] = append(byPath[p], bucketEntry{i, fi})
		}
	}

	// For each ea = bucket[a], scan subsequent entries eb (which are ordered by
	// modelIdx then findingIdx ascending). Match at most one eb per higher
	// model — this mirrors the inner `break` in the original nested loop and
	// preserves its exact counting semantics.
	matchedModel := make([]bool, len(results))
	for _, bucket := range byPath {
		for a := range bucket {
			ea := bucket[a]
			clear(matchedModel)
			for b := a + 1; b < len(bucket); b++ {
				eb := bucket[b]
				if eb.modelIdx <= ea.modelIdx {
					continue
				}
				if matchedModel[eb.modelIdx] {
					continue
				}
				if fuzzyMatch(results[ea.modelIdx].findings[ea.findingIdx], results[eb.modelIdx].findings[eb.findingIdx]) {
					matchCounts[matchKey(ea)]++
					matchCounts[matchKey(eb)]++
					matchedModel[eb.modelIdx] = true
				}
			}
		}
	}

	// Classify findings. Use a dedup key based on path+startLine+category to
	// prevent near-duplicate consensus entries from different models.
	type dedupKey struct {
		path      string
		startLine int
		category  Category
	}
	consensusSeen := make(map[dedupKey]bool)
	for i, r := range results {
		for fi, f := range r.findings {
			key := matchKey{i, fi}
			if matchCounts[key] > 0 {
				dk := dedupKey{findingPath(f), findingStartLine(f), f.Category}
				if !consensusSeen[dk] {
					consensusSeen[dk] = true
					cr.Consensus = append(cr.Consensus, f)
					cr.All = append(cr.All, f)
				}
			} else {
				cr.Unique[r.label] = append(cr.Unique[r.label], f)
				cr.All = append(cr.All, f)
			}
		}
	}

	SortFindings(cr.Consensus)
	SortFindings(cr.All)

	return cr
}

// fuzzyMatch determines if two findings are similar enough to be considered the same.
func fuzzyMatch(a, b Finding) bool {
	// Must be same file
	pathA := findingPath(a)
	pathB := findingPath(b)
	if pathA != pathB {
		return false
	}

	// Lines must overlap
	if !linesOverlap(a, b) {
		return false
	}

	// Title similarity (case-insensitive substring or >50% word overlap)
	if titleSimilar(a.Title, b.Title) {
		return true
	}

	// Same category + overlapping lines + some title word overlap (>0 shared words)
	if a.Category == b.Category && anyTitleWordOverlap(a.Title, b.Title) {
		return true
	}

	return false
}

// anyTitleWordOverlap returns true if titles share at least one meaningful word.
func anyTitleWordOverlap(a, b string) bool {
	wordsA := strings.Fields(strings.ToLower(a))
	wordsB := strings.Fields(strings.ToLower(b))
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return false
	}
	setB := make(map[string]bool, len(wordsB))
	for _, w := range wordsB {
		setB[w] = true
	}
	for _, w := range wordsA {
		if setB[w] {
			return true
		}
	}
	return false
}

func linesOverlap(a, b Finding) bool {
	la := findingLines(a)
	lb := findingLines(b)
	// Overlap if one range contains the start of the other
	return la.Start <= lb.End && lb.Start <= la.End
}

func findingLines(f Finding) LineRange {
	if len(f.Locations) > 0 {
		return f.Locations[0].Lines
	}
	return LineRange{}
}

func titleSimilar(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))

	// Exact match
	if a == b {
		return true
	}

	// Substring
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}

	// Word overlap: >50% of words in common
	wordsA := strings.Fields(a)
	wordsB := strings.Fields(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return false
	}

	setB := make(map[string]bool)
	for _, w := range wordsB {
		setB[w] = true
	}

	overlap := 0
	for _, w := range wordsA {
		if setB[w] {
			overlap++
		}
	}

	minLen := len(wordsA)
	if len(wordsB) < minLen {
		minLen = len(wordsB)
	}

	return float64(overlap)/float64(minLen) > 0.5
}

func parseModelSpec(spec string) (string, string, error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid model spec %q: expected provider:model", spec)
	}
	return parts[0], parts[1], nil
}
