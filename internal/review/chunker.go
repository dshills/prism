package review

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/diffutil"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/ratelimit"
)

const (
	// DefaultChunkBytes is the target size of one chunk of a diff under review,
	// used when the config does not set ChunkBytes. It is deliberately small:
	// one prompt carrying a whole large diff gets a shallow review. On an
	// 80 KB diff several models returned no findings at all, while the same
	// diff split per package gave real ones.
	DefaultChunkBytes = 24000

	// maxContextFiles caps how many other-part file names a chunk's prompt
	// lists, so codebase reviews of thousands of files stay bounded.
	maxContextFiles = 200

	// chunkerVersion is part of every cache key (reviewCacheKey). What the
	// prompt builders write is keyed by promptFingerprint, so a change to the
	// system or user prompt needs no bump; bump it when SplitIntoChunks or
	// otherPartsNote change, which the fingerprint cannot see. 4: findings
	// quote their evidence (specs/SPEC-review-integrity.md FR-4).
	chunkerVersion = 4
)

// effectiveChunkBytes returns chunkBytes, or DefaultChunkBytes when unset.
func effectiveChunkBytes(chunkBytes int) int {
	if chunkBytes <= 0 {
		return DefaultChunkBytes
	}
	return chunkBytes
}

// Chunk represents a portion of a diff to be reviewed independently.
type Chunk struct {
	Index int
	Diff  string
	Files []string
}

// SplitIntoChunks splits a diff into chunks of whole files, each targeting at
// most chunkBytes (DefaultChunkBytes when chunkBytes <= 0). A single file
// larger than that is a chunk of its own; a file is never split.
//
// A directory (usually a package, which often changes together) is kept in
// one chunk whenever it fits in one: at each directory boundary, if the whole
// next directory would not fit in the space left, it starts a fresh chunk.
// Small directories still share a chunk. A directory larger than a chunk is
// split by size alone.
func SplitIntoChunks(diff string, chunkBytes int) []Chunk {
	sections := diffutil.SplitSections(diff)
	if len(sections) == 0 {
		return nil
	}
	maxBytes := effectiveChunkBytes(chunkBytes)

	paths := make([]string, len(sections))
	dirs := make([]string, len(sections))
	for i, sec := range sections {
		paths[i] = diffutil.PathFromSection(sec)
		dirs[i] = sectionDir(paths[i])
	}
	// runBytes[i] is the size of the run of consecutive sections sharing
	// sections[i]'s directory, starting at i. Git orders a diff by path, so a
	// directory's files are consecutive.
	runBytes := make([]int, len(sections))
	for i := len(sections) - 1; i >= 0; i-- {
		runBytes[i] = len(sections[i])
		if i+1 < len(sections) && dirs[i+1] == dirs[i] {
			runBytes[i] += runBytes[i+1]
		}
	}

	var chunks []Chunk
	var currentDiff strings.Builder
	var currentFiles []string

	flush := func() {
		chunks = append(chunks, Chunk{
			Index: len(chunks),
			Diff:  currentDiff.String(),
			Files: currentFiles,
		})
		currentDiff.Reset()
		currentFiles = nil
	}

	for i, sec := range sections {
		if currentDiff.Len() > 0 {
			// Only a directory that fits in one chunk is worth a fresh one; a
			// larger directory is split by size wherever it starts, so it must
			// not cut short the small directories before it.
			newDir := i > 0 && dirs[i] != dirs[i-1]
			dirWouldSplit := newDir && runBytes[i] <= maxBytes && currentDiff.Len()+runBytes[i] > maxBytes
			overflow := currentDiff.Len()+len(sec) > maxBytes
			if dirWouldSplit || overflow {
				flush()
			}
		}
		currentDiff.WriteString(sec)
		if paths[i] != "" {
			currentFiles = append(currentFiles, paths[i])
		}
	}

	if currentDiff.Len() > 0 {
		flush()
	}
	return chunks
}

// sectionDir is the directory of a diff section's path ("" for a file at the
// root or a section with no path).
func sectionDir(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return ""
}

// NeedsChunking returns true if the diff is larger than one chunk.
func NeedsChunking(diff string, chunkBytes int) bool {
	return len(diff) > effectiveChunkBytes(chunkBytes)
}

// otherPartsNote tells the model, when a review is split, which files the
// other parts contain. Without it a model reviewing one part reports code in
// another part as missing or not updated (for example, a constructor whose
// callers are in a different chunk). Empty when there is only one chunk.
func otherPartsNote(chunks []Chunk, i int) string {
	if len(chunks) <= 1 {
		return ""
	}
	var others []string
	for j, c := range chunks {
		if j != i {
			others = append(others, c.Files...)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n## Other parts of this review\n\nThis review is split into %d parts and this is part %d. ", len(chunks), i+1)
	b.WriteString("The files below are in the other parts and are reviewed there. Code you cannot see here may be defined, changed or called in one of them, so do not report something as missing or not updated only because it is absent from this part.\n\n")
	shown := others
	if len(shown) > maxContextFiles {
		shown = shown[:maxContextFiles]
	}
	for _, f := range shown {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	if extra := len(others) - len(shown); extra > 0 {
		fmt.Fprintf(&b, "- ...and %d more\n", extra)
	}
	return b.String()
}

// PromptBuilder constructs system and user prompts for a chunk.
type PromptBuilder func(chunkDiff string, files []string, cfg config.Config, rules *Rules) (systemPrompt, userPrompt string)

// ChunkOptions controls how chunked review is performed.
type ChunkOptions struct {
	Builder PromptBuilder
}

// defaultPromptBuilder uses the standard diff-review prompts.
func defaultPromptBuilder(chunkDiff string, files []string, cfg config.Config, rules *Rules) (string, string) {
	return SystemPrompt(), BuildUserPromptWithRules(chunkDiff, files, cfg.MaxFindings, cfg.FailOn, rules)
}

// RunChunked reviews diff chunks in parallel and merges findings.
func RunChunked(ctx context.Context, chunks []Chunk, provider providers.Reviewer, cfg config.Config) ([]Finding, int64, error) {
	return RunChunkedWithRules(ctx, chunks, provider, cfg, nil)
}

// RunChunkedWithRules reviews diff chunks in parallel with optional rules.
func RunChunkedWithRules(ctx context.Context, chunks []Chunk, provider providers.Reviewer, cfg config.Config, rules *Rules) ([]Finding, int64, error) {
	return RunChunkedWithOptions(ctx, chunks, provider, cfg, rules, ChunkOptions{})
}

// RunChunkedWithOptions reviews diff chunks in parallel with custom prompt construction.
func RunChunkedWithOptions(ctx context.Context, chunks []Chunk, provider providers.Reviewer, cfg config.Config, rules *Rules, opts ChunkOptions) ([]Finding, int64, error) {
	findings, llmMs, _, err := runChunkedCounted(ctx, chunks, provider, cfg, rules, opts)
	return findings, llmMs, err
}

// runChunkedCounted is RunChunkedWithOptions that also returns how many model
// calls were made, repair passes included, for the report's coverage.
func runChunkedCounted(ctx context.Context, chunks []Chunk, provider providers.Reviewer, cfg config.Config, rules *Rules, opts ChunkOptions) ([]Finding, int64, int, error) {
	builder := opts.Builder
	if builder == nil {
		builder = defaultPromptBuilder
	}
	perChunk, llmMs, calls, err := reviewChunks(ctx, chunks, nil, provider, cfg, rules, builder)
	if err != nil {
		return nil, llmMs, calls, err
	}
	return mergeChunkFindings(perChunk), llmMs, calls, nil
}

// reviewChunks reviews chunks in parallel and returns each chunk's findings
// by chunk index. Only the chunks whose indexes are in todo are sent to the
// provider (all of them when todo is nil); the rest are left nil, as is a
// chunk that failed. A chunk that succeeded is never nil (parseFindings
// returns an empty slice for "[]"), so nil means "no result". Every
// prompt is still built against the whole of chunks, so a chunk reviewed on
// its own is told about the parts that were not.
//
// When a chunk fails, the first error in chunk order is returned together
// with the findings of every chunk that succeeded, so the caller can keep
// them.
func reviewChunks(ctx context.Context, chunks []Chunk, todo []int, provider providers.Reviewer, cfg config.Config, rules *Rules, builder PromptBuilder) ([][]Finding, int64, int, error) {
	if todo == nil {
		todo = make([]int, len(chunks))
		for i := range chunks {
			todo[i] = i
		}
	}

	// Compute effective concurrency and rate limit from config + provider defaults.
	concurrency := cfg.MaxConcurrency
	if concurrency <= 0 {
		concurrency = providers.DefaultMaxConcurrency(provider.Name())
	}
	rpm := cfg.RateLimitRPM
	if rpm <= 0 {
		rpm = providers.DefaultRPM(provider.Name())
	}
	limiter := ratelimit.New(rpm)

	perChunk := make([][]Finding, len(chunks))
	errs := make([]error, len(chunks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	var totalLLMMs int64
	var calls int
	var mu sync.Mutex

	for _, i := range todo {
		wg.Add(1)
		go func(i int, chunk Chunk) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			if err := limiter.Wait(ctx); err != nil {
				errs[i] = fmt.Errorf("chunk %d: rate limiter: %w", i, err)
				return
			}

			sysPr, userPr := builder(chunk.Diff, chunk.Files, cfg, rules)
			userPr += otherPartsNote(chunks, i)
			req := providers.ReviewRequest{
				SystemPrompt: sysPr,
				UserPrompt:   userPr,
				MaxTokens:    8192,
			}

			llmStart := time.Now()
			resp, err := provider.Review(ctx, req)
			elapsed := time.Since(llmStart).Milliseconds()

			mu.Lock()
			totalLLMMs += elapsed
			calls++
			mu.Unlock()

			if err != nil {
				errs[i] = fmt.Errorf("chunk %d: %w", i, err)
				return
			}

			findings, err := parseFindings(resp.Content)
			if err != nil {
				// Try repair
				repairPrompt := fmt.Sprintf(
					"Your previous response was not valid JSON. The error was: %s\n\nPlease fix and respond with ONLY a valid JSON array of findings.\n\nPrevious response:\n%s",
					err.Error(), resp.Content,
				)
				resp2, err2 := provider.Review(ctx, providers.ReviewRequest{
					SystemPrompt: sysPr,
					UserPrompt:   repairPrompt,
					MaxTokens:    8192,
				})
				mu.Lock()
				calls++
				mu.Unlock()
				if err2 != nil {
					errs[i] = fmt.Errorf("chunk %d repair: %w", i, err2)
					return
				}
				findings, err = parseFindings(resp2.Content)
				if err != nil {
					errs[i] = fmt.Errorf("chunk %d validation after repair: %w", i, err)
					return
				}
				resp = resp2
			}

			perChunk[i] = stampProvenance(findings, resp.Provider, resp.Model)
		}(i, chunks[i])
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return perChunk, totalLLMMs, calls, err
		}
	}
	return perChunk, totalLLMMs, calls, nil
}

// mergeChunkFindings joins per-chunk findings in chunk order, then
// deduplicates and sorts them (by severity, high first, then path and line).
func mergeChunkFindings(perChunk [][]Finding) []Finding {
	var all []Finding
	for _, fs := range perChunk {
		all = append(all, fs...)
	}
	all = DeduplicateFindings(all)
	SortFindings(all)
	return all
}

// DeduplicateFindings removes duplicate findings by ID.
func DeduplicateFindings(findings []Finding) []Finding {
	seen := make(map[string]bool)
	var result []Finding
	for _, f := range findings {
		if !seen[f.ID] {
			seen[f.ID] = true
			result = append(result, f)
		}
	}
	return result
}

// SortFindings sorts findings by severity (high first), then path, then line.
func SortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		ri := SeverityRank(findings[i].Severity)
		rj := SeverityRank(findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		pi := findingPath(findings[i])
		pj := findingPath(findings[j])
		if pi != pj {
			return pi < pj
		}
		li := findingStartLine(findings[i])
		lj := findingStartLine(findings[j])
		return li < lj
	})
}

func findingPath(f Finding) string {
	if len(f.Locations) > 0 {
		return f.Locations[0].Path
	}
	return ""
}

func findingStartLine(f Finding) int {
	if len(f.Locations) > 0 {
		return f.Locations[0].Lines.Start
	}
	return 0
}
