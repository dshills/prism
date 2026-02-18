package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/redact"
)

// CompareResult holds results from multi-model comparison.
type CompareResult struct {
	Consensus []Finding // Findings that appeared in >=2 models
	Unique    map[string][]Finding // Unique findings per model (key: "provider:model")
	All       []Finding // All merged findings for the report
	LLMMs     int64
}

// compareModelResult holds the output from a single model's review.
type compareModelResult struct {
	label    string
	findings []Finding
	err      error
}

// RunCompare runs reviews independently across multiple provider:model pairs
// and merges findings.
func RunCompare(ctx context.Context, diff string, files []string, models []string, cfg config.Config, rules *Rules) (*CompareResult, error) {
	results := make([]compareModelResult, len(models))
	var wg sync.WaitGroup
	var totalLLMMs int64
	var mu sync.Mutex

	for i, modelSpec := range models {
		wg.Add(1)
		go func(i int, spec string) {
			defer wg.Done()

			providerName, modelName, err := parseModelSpec(spec)
			if err != nil {
				results[i] = compareModelResult{label: spec, err: err}
				return
			}

			provider, err := providers.New(providerName, modelName)
			if err != nil {
				results[i] = compareModelResult{label: spec, err: fmt.Errorf("%s: %w", spec, err)}
				return
			}

			redactedDiff := diff
			if cfg.Privacy.RedactSecrets {
				redactedDiff = redact.Secrets(redactedDiff)
			}

			userPrompt := BuildUserPromptWithRules(redactedDiff, files, cfg.MaxFindings, cfg.FailOn, rules)

			llmStart := time.Now()
			resp, err := provider.Review(ctx, providers.ReviewRequest{
				SystemPrompt: SystemPrompt(),
				UserPrompt:   userPrompt,
				MaxTokens:    8192,
			})
			elapsed := time.Since(llmStart).Milliseconds()

			mu.Lock()
			totalLLMMs += elapsed
			mu.Unlock()

			if err != nil {
				results[i] = compareModelResult{label: spec, err: fmt.Errorf("%s: %w", spec, err)}
				return
			}

			findings, err := parseFindings(resp.Content)
			if err != nil {
				results[i] = compareModelResult{label: spec, err: fmt.Errorf("%s: invalid response: %w", spec, err)}
				return
			}

			results[i] = compareModelResult{label: spec, findings: findings}
		}(i, modelSpec)
	}

	wg.Wait()

	// Check for errors
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
	}

	// Merge findings
	return mergeResults(results, totalLLMMs), nil
}

func mergeResults(results []compareModelResult, totalLLMMs int64) *CompareResult {
	cr := &CompareResult{
		Unique: make(map[string][]Finding),
		LLMMs:  totalLLMMs,
	}

	if len(results) == 0 {
		return cr
	}

	// Track which findings from each model match findings from other models
	// A finding is "consensus" if it appears in >=2 models (by fuzzy match)
	type matchKey struct {
		modelIdx   int
		findingIdx int
	}
	matchCounts := make(map[matchKey]int)

	for i := 0; i < len(results); i++ {
		for fi, f := range results[i].findings {
			key := matchKey{i, fi}
			for j := i + 1; j < len(results); j++ {
				for gj, g := range results[j].findings {
					if fuzzyMatch(f, g) {
						matchCounts[key]++
						matchCounts[matchKey{j, gj}]++
						break
					}
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
