package review

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"
)

// genCompareFixture builds n models with findings/model findings each,
// spread across pathCount distinct paths. Titles are drawn from a small
// vocabulary so some cross-model pairs fuzzy-match by shared words.
func genCompareFixture(models, findingsPerModel, pathCount int, seed uint64) []compareModelResult {
	rng := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	vocab := []string{"null", "pointer", "leak", "sql", "injection", "race", "unchecked", "error", "slow", "query", "bounds", "overflow"}
	cats := []Category{CategoryBug, CategorySecurity, CategoryPerformance, CategoryStyle}

	out := make([]compareModelResult, models)
	for i := range out {
		findings := make([]Finding, findingsPerModel)
		for fi := range findings {
			path := fmt.Sprintf("pkg%d/file%d.go", fi%pathCount, fi%pathCount)
			w1 := vocab[rng.IntN(len(vocab))]
			w2 := vocab[rng.IntN(len(vocab))]
			start := rng.IntN(200) + 1
			findings[fi] = Finding{
				ID:       fmt.Sprintf("m%d-f%d", i, fi),
				Category: cats[rng.IntN(len(cats))],
				Title:    w1 + " " + w2 + " issue",
				Severity: SeverityMedium,
				Locations: []Location{
					{Path: path, Lines: LineRange{Start: start, End: start + 5}},
				},
			}
		}
		out[i] = compareModelResult{
			label:    fmt.Sprintf("provider:model%d", i),
			findings: findings,
		}
	}
	return out
}

var mergeBenchCases = []struct {
	models, findings, paths int
}{
	{2, 50, 10},
	{4, 100, 20},
	{4, 500, 20},
	{6, 200, 5},
}

func BenchmarkMergeResults(b *testing.B) {
	for _, c := range mergeBenchCases {
		name := fmt.Sprintf("m=%d_f=%d_p=%d", c.models, c.findings, c.paths)
		b.Run(name, func(b *testing.B) {
			results := genCompareFixture(c.models, c.findings, c.paths, 42)
			b.ResetTimer()
			for range b.N {
				_ = mergeResults(results, 0)
			}
		})
	}
}

func BenchmarkMergeResultsNaive(b *testing.B) {
	for _, c := range mergeBenchCases {
		name := fmt.Sprintf("m=%d_f=%d_p=%d", c.models, c.findings, c.paths)
		b.Run(name, func(b *testing.B) {
			results := genCompareFixture(c.models, c.findings, c.paths, 42)
			b.ResetTimer()
			for range b.N {
				_ = mergeResultsNaive(results, 0)
			}
		})
	}
}

// mergeResultsNaive is the pre-optimization reference implementation,
// kept solely as a correctness oracle for the property test.
func mergeResultsNaive(results []compareModelResult, totalLLMMs int64) *CompareResult {
	cr := &CompareResult{
		Unique: make(map[string][]Finding),
		LLMMs:  totalLLMMs,
	}
	if len(results) == 0 {
		return cr
	}

	type matchKey struct{ modelIdx, findingIdx int }
	matchCounts := make(map[matchKey]int)
	for i := range results {
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

// TestMergeResults_EquivalentToNaive verifies the optimized path-bucketed
// implementation produces the same consensus/unique classification as the
// original O(n²·m²) version across randomized inputs.
func TestMergeResults_EquivalentToNaive(t *testing.T) {
	configs := []struct{ models, findings, paths int }{
		{2, 10, 3},
		{3, 20, 5},
		{4, 50, 10},
		{5, 30, 1}, // all findings in one path: worst case for bucketing
	}
	for _, c := range configs {
		for seed := uint64(1); seed <= 5; seed++ {
			name := fmt.Sprintf("m=%d_f=%d_p=%d_s=%d", c.models, c.findings, c.paths, seed)
			t.Run(name, func(t *testing.T) {
				results := genCompareFixture(c.models, c.findings, c.paths, seed)
				got := mergeResults(results, 0)
				want := mergeResultsNaive(results, 0)

				if !sameFindingSet(got.Consensus, want.Consensus) {
					t.Errorf("Consensus differs: got %d, want %d", len(got.Consensus), len(want.Consensus))
				}
				if len(got.Unique) != len(want.Unique) {
					t.Errorf("Unique key count differs: got %d, want %d", len(got.Unique), len(want.Unique))
				}
				for label, wantList := range want.Unique {
					if !sameFindingSet(got.Unique[label], wantList) {
						t.Errorf("Unique[%s] differs: got %d, want %d", label, len(got.Unique[label]), len(wantList))
					}
				}
				if !sameFindingSet(got.All, want.All) {
					t.Errorf("All differs: got %d, want %d", len(got.All), len(want.All))
				}
			})
		}
	}
}

// sameFindingSet compares two finding slices as multisets keyed by ID.
func sameFindingSet(a, b []Finding) bool {
	if len(a) != len(b) {
		return false
	}
	ids := func(fs []Finding) []string {
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.ID
		}
		sort.Strings(out)
		return out
	}
	ia, ib := ids(a), ids(b)
	for i := range ia {
		if ia[i] != ib[i] {
			return false
		}
	}
	return true
}
