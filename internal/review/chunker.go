package review

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

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
	return SystemPromptFor(files), buildUserPrompt(chunkDiff, files, cfg.MaxFindings, cfg.FailOn, cfg.MinSeverity, rules)
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
	run := reviewChunks(ctx, chunks, nil, provider, cfg, rules, builder)
	if err := firstError(run.errs); err != nil {
		return nil, run.llmMs, run.calls, err
	}
	return mergeChunkFindings(run.findings), run.llmMs, run.calls, nil
}

// chunkRun is the result of reviewChunks, by chunk index.
type chunkRun struct {
	findings [][]Finding // each reviewed chunk's findings; nil when not reviewed or failed
	errs     []error     // each failed chunk's error
	// fallback marks the chunks a fallback provider answered (see
	// providers.Fallback). Their results are not cached: a later run must not
	// replay a fallback's review as the configured model's.
	fallback []bool
	// capped marks the chunks whose answer filled a limit lowered to their
	// share of maxFindings (chunkFindingLimit). It may have left findings
	// out, and the share is not in the cache key, so they are not cached
	// either: a later review asking a chunk for more must not replay it.
	capped []bool
	llmMs  int64
	calls  int
	splits int // parts halved after a cut-off response
	usage  usageLedger
}

// reviewChunks reviews chunks in parallel and returns each chunk's findings
// by chunk index. Only the chunks whose indexes are in todo are sent to the
// provider (all of them when todo is nil); the rest are left nil, as is a
// chunk that failed. A chunk that succeeded is never nil (parseFindings
// returns an empty slice for "[]"), so nil means "no result". Every
// prompt is still built against the whole of chunks, so a chunk reviewed on
// its own is told about the parts that were not.
//
// Each failed chunk's error is kept by chunk index, so the caller can keep
// the chunks that succeeded and report the ones that did not.
func reviewChunks(ctx context.Context, chunks []Chunk, todo []int, provider providers.Reviewer, cfg config.Config, rules *Rules, builder PromptBuilder) chunkRun {
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
	// Each chunk is asked for its share of maxFindings, not all of it. The
	// fingerprint in the cache key is the full limit's, like the note on the
	// other parts: the share moves with the number of chunks, and a change
	// in that must not miss every chunk. A chunk whose answer fills its
	// share is not cached (chunkRun.capped).
	partCfg := cfg
	partCfg.MaxFindings = chunkFindingLimit(cfg.MaxFindings, len(chunks))
	pr := &partReviewer{provider: provider, cfg: partCfg, rules: rules, builder: builder, limiter: limiter}

	run := chunkRun{
		findings: make([][]Finding, len(chunks)),
		errs:     make([]error, len(chunks)),
		fallback: make([]bool, len(chunks)),
		capped:   make([]bool, len(chunks)),
	}
	lowered := partCfg.MaxFindings < cfg.MaxFindings
	errs := run.errs
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex

	for _, i := range todo {
		wg.Add(1)
		go func(i int, chunk Chunk) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			res, err := pr.review(ctx, part{diff: chunk.Diff, files: chunk.Files, note: otherPartsNote(chunks, i)}, 0)
			mu.Lock()
			run.llmMs += res.llmMs
			run.calls += res.calls
			run.splits += res.splits
			run.usage.merge(res.usage)
			mu.Unlock()
			if err != nil {
				errs[i] = err
				return
			}
			run.findings[i] = res.findings
			run.fallback[i] = res.fallback
			run.capped[i] = lowered && res.full
		}(i, chunks[i])
	}

	wg.Wait()
	return run
}

// minChunkFindings is the lowest per-chunk findings limit.
const minChunkFindings = 10

// chunkFindingLimit is the findings limit each of n chunks is asked for when
// the review keeps total. Asking every chunk for total would allow n times
// the output tokens of findings that are cut anyway; each is asked for twice
// its even share instead, so a chunk holding most of the problems still has
// room, and never fewer than minChunkFindings (or more than total).
func chunkFindingLimit(total, n int) int {
	if total <= 0 || n <= 1 {
		return total
	}
	share := (2*total + n - 1) / n
	return min(total, max(share, minChunkFindings))
}

// firstError is the first non-nil error in chunk order, labelled with its
// chunk index.
func firstError(errs []error) error {
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("chunk %d: %w", i, err)
		}
	}
	return nil
}

// maxSkipFiles caps how many file names a failed chunk's skip entry lists.
const maxSkipFiles = 5

// chunkFailures turns the chunks that failed into coverage skips, so the
// chunks that succeeded are kept and the report says what was not reviewed
// (exit 5). It returns an error instead when there is nothing worth keeping:
// a provider auth error, which fails every chunk alike, a cancelled context,
// or no result at all (haveResults false: no chunk was cached or succeeded).
func chunkFailures(ctx context.Context, chunks []Chunk, errs []error, haveResults bool) ([]Skip, error) {
	first := firstError(errs)
	if first == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i, err := range errs {
		if providers.IsAuthError(err) {
			return nil, fmt.Errorf("chunk %d: %w", i, err)
		}
	}
	if !haveResults {
		return nil, first
	}
	var skips []Skip
	for i, err := range errs {
		if err != nil {
			skips = append(skips, Skip{Target: chunkTarget(chunks, i), Reason: "review failed: " + err.Error()})
		}
	}
	return skips, nil
}

// chunkTarget names a chunk in a skip entry by its position and files, which
// are what an agent needs to know were not reviewed.
func chunkTarget(chunks []Chunk, i int) string {
	files := chunks[i].Files
	shown := files
	if len(shown) > maxSkipFiles {
		shown = shown[:maxSkipFiles]
	}
	list := strings.Join(shown, ", ")
	if extra := len(files) - len(shown); extra > 0 {
		list += fmt.Sprintf(", and %d more", extra)
	}
	return fmt.Sprintf("chunk %d/%d (%s)", i+1, len(chunks), list)
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

// DeduplicateFindings removes findings reported twice: the same ID (the same
// code, declaration and category, see fingerprint.go), title and start line.
// Findings that share an ID but differ in title or line are kept, since they
// may be two issues on the same code, or the same code twice in a function.
func DeduplicateFindings(findings []Finding) []Finding {
	seen := make(map[string]bool)
	var result []Finding
	for _, f := range findings {
		key := fmt.Sprintf("%s\x00%s\x00%d", f.ID, f.Title, findingStartLine(f))
		if !seen[key] {
			seen[key] = true
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
