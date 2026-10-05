package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dshills/prism/internal/cache"
	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/diffutil"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/redact"
)

// rawFinding is the JSON structure returned by the LLM and also used for
// cache storage. Provider/Model are set only on the cache-storage path so
// cached findings round-trip their provenance; LLM responses won't populate
// them (the model doesn't know its own identity) — the engine stamps those
// from the provider after parsing.
type rawFinding struct {
	Severity   string   `json:"severity"`
	Category   string   `json:"category"`
	Title      string   `json:"title"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion"`
	Confidence float64  `json:"confidence"`
	Path       string   `json:"path"`
	StartLine  int      `json:"startLine"`
	EndLine    int      `json:"endLine"`
	Tags       []string `json:"tags"`
	Evidence   string   `json:"evidence,omitempty"`
	Fix        *Fix     `json:"fix,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Model      string   `json:"model,omitempty"`
}

// newProvider creates the provider a review sends its prompts to. Tests
// replace it to review without a real provider.
var newProvider = providers.New

// reviewOpts controls differences between Run() and RunCodebase() pipelines.
type reviewOpts struct {
	builder     PromptBuilder // nil = default diff prompts
	alwaysChunk bool          // true = skip NeedsChunking() check
}

// Run executes a review using the given diff result and configuration.
func Run(ctx context.Context, diff gitctx.DiffResult, cfg config.Config) (*Report, error) {
	return reviewPipeline(ctx, diff, cfg, reviewOpts{})
}

// promptFingerprint hashes everything builder puts into a prompt besides the
// reviewed text: the system prompt, and the user prompt rendered around an
// empty diff. That covers maxFindings, failOn, the rules section and any
// input a builder adds later. Every cache key includes it, so changing what
// the model is asked is a cache miss, never a replay of a review that was
// asked something else.
func promptFingerprint(builder PromptBuilder, cfg config.Config, rules *Rules) string {
	sys, user := builder("", nil, cfg, rules)
	h := sha256.Sum256([]byte(sys + "\x00" + user))
	return fmt.Sprintf("%x", h[:16])
}

// reviewCacheKey keys a cached review of content, shared by diff and
// codebase reviews. Besides the prompt it carries chunkerVersion, which
// covers what the fingerprint cannot see: how a diff is split, and the
// per-part note that lists the other parts' files.
func reviewCacheKey(provider, model, prompt, content string) string {
	return cache.BuildCacheKey(provider, model,
		fmt.Sprintf("chunker=%d,prompt=%s\n%s", chunkerVersion, prompt, content))
}

// diffCacheKey keys a diff review's cached result. How the diff is chunked is
// part of the key, both the size and the algorithm (chunkerVersion): the same
// diff reviewed in different chunks is a different review, and results cached
// under older chunking must not be replayed as current.
func diffCacheKey(cfg config.Config, prompt, diff string) string {
	return reviewCacheKey(cfg.Provider, cfg.Model, prompt,
		fmt.Sprintf("chunkBytes=%d\n%s", effectiveChunkBytes(cfg.ChunkBytes), diff))
}

// chunkCacheKey keys one chunk's cached findings in a chunked diff review.
// It is the chunk's own text, not the whole diff, so an edit misses only the
// chunks it touches. The note listing the other parts' files is left out:
// otherwise adding or removing any file would miss every chunk, and the
// prompt tells the model not to report on code it can only see named there.
func chunkCacheKey(cfg config.Config, prompt, chunkDiff string) string {
	return reviewCacheKey(cfg.Provider, cfg.Model, prompt, "chunk\n"+chunkDiff)
}

// fileCacheKey keys one file's cached findings in a codebase review.
func fileCacheKey(provider, model, prompt, section string) string {
	return reviewCacheKey(provider, model, prompt, section)
}

// reviewPipeline is the shared review flow: redact → cache → rules → LLM → cache write → overrides → limit → report.
func reviewPipeline(ctx context.Context, diff gitctx.DiffResult, cfg config.Config, opts reviewOpts) (*Report, error) {
	startTime := time.Now()

	// Redact secrets from diff before sending to provider
	redactedDiff := diff.Diff
	if cfg.Privacy.RedactSecrets {
		redactedDiff = redact.Secrets(redactedDiff)
	}

	// FR-1: coverage is recorded for every outcome, including an empty diff.
	cov := NewCoverage(ConfigReviewer(cfg.Provider, cfg.Model), len(diff.Files), diff.ReviewedBytes(), diff.TruncatedBytes)

	if strings.TrimSpace(redactedDiff) == "" {
		return emptyReport(diff, startTime, cov), nil
	}

	// Decide the chunking up front so a cache hit can report it too.
	var chunks []Chunk
	if opts.alwaysChunk || NeedsChunking(redactedDiff, cfg.ChunkBytes) {
		chunks = SplitIntoChunks(redactedDiff, cfg.ChunkBytes)
		cov.Chunks = len(chunks)
	} else {
		cov.Chunks = 1
	}

	// Initialize cache
	reviewCache, err := cache.New(cfg.Cache.Enabled, cfg.Cache.Dir, cfg.Cache.TTLSeconds)
	if err != nil {
		// Cache failure is non-fatal, just disable it
		reviewCache, _ = cache.New(false, "", 0)
	}

	// Rules and the prompt builder come before the cache: both shape the
	// prompt, so both are part of the key.
	rules, err := LoadRules(cfg.RulesFile)
	if err != nil {
		return nil, fmt.Errorf("loading rules: %w", err)
	}
	builder := opts.builder
	if builder == nil {
		builder = defaultPromptBuilder
	}

	prompt := promptFingerprint(builder, cfg, rules)

	var findings []Finding
	var llmMs int64
	if chunks != nil {
		// Chunked: one cache entry per chunk, so a re-review after an edit
		// sends only the chunks that changed.
		findings, llmMs, err = reviewChunksCached(ctx, chunks, cfg, rules, builder, prompt, reviewCache, &cov)
	} else {
		findings, llmMs, err = reviewWholeCached(ctx, redactedDiff, diff.Files, cfg, rules, builder, prompt, reviewCache, &cov)
	}
	if err != nil {
		return nil, err
	}

	// Apply rules severity overrides
	findings = ApplySeverityOverrides(findings, rules)

	// Verify, suppress and limit after the cache, which holds unverified
	// findings so a changed tree re-verifies (FR-8).
	findings, discarded, suppressed, err := FinalizeFindings(ctx, findings, diff, cfg)
	if err != nil {
		return nil, err
	}

	report := BuildReport(diff, findings, llmMs, time.Since(startTime).Milliseconds())
	cov.Finalize()
	report.Coverage = cov
	report.Discarded = discarded
	report.Suppressed = suppressed
	return report, nil
}

// reviewWholeCached reviews a diff that fits in one prompt, with one cache
// entry for the whole diff.
func reviewWholeCached(ctx context.Context, redactedDiff string, files []string, cfg config.Config, rules *Rules, builder PromptBuilder, prompt string, rc *cache.Cache, cov *Coverage) ([]Finding, int64, error) {
	cacheKey := diffCacheKey(cfg, prompt, redactedDiff)
	if cached, ok := rc.Get(cacheKey); ok {
		// A corrupt entry falls through to the LLM.
		if findings, err := parseReviewedFindings(cached, redactedDiff); err == nil {
			// Legacy cache entries may lack provenance; stamp from the cache
			// key's (provider, model) since the key itself fixes them.
			cov.CacheHit = true
			cov.CachedChunks = 1
			return stampProvenance(findings, cfg.Provider, cfg.Model), 0, nil
		}
	}

	provider, err := newProvider(cfg.Provider, cfg.Model)
	if err != nil {
		return nil, 0, fmt.Errorf("creating provider: %w", err)
	}
	sysPr, userPr := builder(redactedDiff, files, cfg, rules)

	llmStart := time.Now()
	req := providers.ReviewRequest{
		SystemPrompt: sysPr,
		UserPrompt:   userPr,
		MaxTokens:    8192,
		Output:       findingsOutput,
	}

	resp, err := provider.Review(ctx, req)
	cov.LLMCalls++
	if err != nil {
		return nil, 0, fmt.Errorf("provider review: %w", err)
	}
	llmMs := time.Since(llmStart).Milliseconds()

	findings, err := parseReviewedFindings(resp.Content, redactedDiff)
	if err != nil {
		// Attempt one repair pass
		repairPrompt := fmt.Sprintf(
			"Your previous response was not valid JSON. The error was: %s\n\nPlease fix it and respond with ONLY a valid JSON array of findings.\n\nYour previous response was:\n%s",
			err.Error(), resp.Content,
		)
		repairReq := providers.ReviewRequest{
			SystemPrompt: sysPr,
			UserPrompt:   repairPrompt,
			MaxTokens:    8192,
			Output:       findingsOutput,
		}
		resp2, err2 := provider.Review(ctx, repairReq)
		cov.LLMCalls++
		if err2 != nil {
			return nil, llmMs, fmt.Errorf("repair pass failed: %w (original error: %w)", err2, err)
		}
		findings, err = parseReviewedFindings(resp2.Content, redactedDiff)
		if err != nil {
			return nil, llmMs, fmt.Errorf("response validation failed after repair: %w", err)
		}
		resp = resp2
	}
	findings = stampProvenance(findings, resp.Provider, resp.Model)
	SortFindings(findings)

	putFindings(rc, cacheKey, findings)
	return findings, llmMs, nil
}

// reviewChunksCached reviews a chunked diff with one cache entry per chunk. A
// chunk whose entry hits is replayed rather than sent; the rest are reviewed
// together, their prompts still listing every other part's files. Each chunk
// reviewed is stored even when another one failed, so a rerun sends only the
// chunks that did not succeed. A chunk that fails is a coverage skip, unless
// chunkFailures finds the failure fatal.
func reviewChunksCached(ctx context.Context, chunks []Chunk, cfg config.Config, rules *Rules, builder PromptBuilder, prompt string, rc *cache.Cache, cov *Coverage) ([]Finding, int64, error) {
	keys := make([]string, len(chunks))
	perChunk := make([][]Finding, len(chunks))
	var todo []int
	for i, c := range chunks {
		keys[i] = chunkCacheKey(cfg, prompt, c.Diff)
		if cached, ok := rc.Get(keys[i]); ok {
			// A corrupt entry is a miss.
			if fs, err := parseReviewedFindings(cached, c.Diff); err == nil {
				perChunk[i] = stampProvenance(fs, cfg.Provider, cfg.Model)
				continue
			}
		}
		todo = append(todo, i)
	}
	cov.CachedChunks = len(chunks) - len(todo)
	if len(todo) == 0 {
		cov.CacheHit = true
		return mergeChunkFindings(perChunk), 0, nil
	}

	provider, err := newProvider(cfg.Provider, cfg.Model)
	if err != nil {
		return nil, 0, fmt.Errorf("creating provider: %w", err)
	}
	fresh, errs, llmMs, calls := reviewChunks(ctx, chunks, todo, provider, cfg, rules, builder)
	cov.LLMCalls = calls
	haveResults := cov.CachedChunks > 0
	for _, i := range todo {
		if errs[i] == nil {
			putFindings(rc, keys[i], fresh[i])
			perChunk[i] = fresh[i]
			haveResults = true
		}
	}
	skips, err := chunkFailures(ctx, chunks, errs, haveResults)
	if err != nil {
		return nil, llmMs, fmt.Errorf("chunked review: %w", err)
	}
	cov.Skipped = append(cov.Skipped, skips...)
	return mergeChunkFindings(perChunk), llmMs, nil
}

// putFindings stores findings under key in the rawFinding form parseFindings
// reads back. Cache write errors are ignored: the cache is an optimisation.
func putFindings(rc *cache.Cache, key string, findings []Finding) {
	if rawJSON, err := json.Marshal(findingsToRaw(findings)); err == nil {
		_ = rc.Put(key, string(rawJSON))
	}
}

func parseFindings(content string) ([]Finding, error) {
	content = strings.TrimSpace(content)

	// Strip markdown code fences if present
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		if len(lines) >= 2 {
			// Remove first line (```json) and last line (```)
			start := 1
			end := len(lines)
			if strings.TrimSpace(lines[end-1]) == "```" {
				end = end - 1
			}
			if start < end {
				content = strings.Join(lines[start:end], "\n")
			} else {
				// Empty code fence (e.g., "```\n```") — treat as empty array
				content = "[]"
			}
		}
	}

	raw, err := decodeRawFindings(content)
	if err != nil {
		return nil, err
	}

	findings := make([]Finding, 0, len(raw))
	for _, r := range raw {
		f := Finding{
			Severity:   Severity(r.Severity),
			Category:   Category(r.Category),
			Title:      r.Title,
			Message:    r.Message,
			Suggestion: r.Suggestion,
			Confidence: r.Confidence,
			Tags:       r.Tags,
			Evidence:   r.Evidence,
			Fix:        usableFix(r.Fix),
			Provider:   r.Provider,
			Model:      r.Model,
			Locations: []Location{
				{
					Path: r.Path,
					Lines: LineRange{
						Start: r.StartLine,
						End:   r.EndLine,
					},
				},
			},
		}
		f.ID = generateFindingID(f)
		findings = append(findings, f)
	}

	return findings, nil
}

// decodeRawFindings reads a response's findings: a JSON array of them, or
// the {"findings": [...]} object that structured output returns.
func decodeRawFindings(content string) ([]rawFinding, error) {
	if strings.HasPrefix(content, "{") {
		var wrapped struct {
			Findings *[]rawFinding `json:"findings"`
		}
		if err := json.Unmarshal([]byte(content), &wrapped); err != nil {
			return nil, fmt.Errorf("invalid findings object: %w", err)
		}
		if wrapped.Findings == nil {
			return nil, fmt.Errorf(`invalid findings object: no "findings" array`)
		}
		return *wrapped.Findings, nil
	}
	var raw []rawFinding
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON array: %w", err)
	}
	return raw, nil
}

// usableFix is the fix a response gave, or nil when it gave none: the schema
// requires the field, so "no fix" arrives as empty strings. A fix that
// changes nothing is no fix either.
func usableFix(f *Fix) *Fix {
	if f == nil || strings.TrimSpace(f.Before) == "" || f.Before == f.After {
		return nil
	}
	return &Fix{Before: f.Before, After: f.After}
}

// stampProvenance sets Provider and Model on every finding that doesn't
// already have them. Used after parsing fresh LLM responses; cached findings
// retain whatever the cache wrote.
func stampProvenance(findings []Finding, provider, model string) []Finding {
	for i := range findings {
		if findings[i].Provider == "" {
			findings[i].Provider = provider
		}
		if findings[i].Model == "" {
			findings[i].Model = model
		}
	}
	return findings
}

// findingsToRaw converts parsed Findings back to rawFinding format for cache storage.
func findingsToRaw(findings []Finding) []rawFinding {
	raw := make([]rawFinding, len(findings))
	for i, f := range findings {
		r := rawFinding{
			Severity:   string(f.Severity),
			Category:   string(f.Category),
			Title:      f.Title,
			Message:    f.Message,
			Suggestion: f.Suggestion,
			Confidence: f.Confidence,
			Tags:       f.Tags,
			Evidence:   f.Evidence,
			Fix:        f.Fix,
			Provider:   f.Provider,
			Model:      f.Model,
		}
		if len(f.Locations) > 0 {
			r.Path = f.Locations[0].Path
			r.StartLine = f.Locations[0].Lines.Start
			r.EndLine = f.Locations[0].Lines.End
		}
		raw[i] = r
	}
	return raw
}

// GenerateRunID creates a unique run identifier.
func GenerateRunID() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return fmt.Sprintf("%x", h[:16])
}

// CodebaseConfig extends config with codebase-specific options.
type CodebaseConfig struct {
	config.Config
	MaxFindingsPerFile int
}

// RunCodebase executes a full-codebase review.
func RunCodebase(ctx context.Context, diff gitctx.DiffResult, cfg CodebaseConfig) (*Report, error) {
	startTime := time.Now()

	reviewCache, err := cache.New(cfg.Cache.Enabled, cfg.Cache.Dir, cfg.Cache.TTLSeconds)
	if err != nil {
		reviewCache, _ = cache.New(false, "", 0)
	}

	rules, err := LoadRules(cfg.RulesFile)
	if err != nil {
		return nil, fmt.Errorf("loading rules: %w", err)
	}

	return runCodebaseWithFileCache(ctx, diff, cfg, reviewCache, rules, startTime)
}

// runCodebaseWithFileCache implements per-file cache invalidation for codebase mode.
// Each file section is checked individually against the cache; only cache misses
// are sent to the LLM. Fresh findings are stored per-file so subsequent runs on
// unchanged files return cached results without any LLM call.
func runCodebaseWithFileCache(
	ctx context.Context,
	diff gitctx.DiffResult,
	cfg CodebaseConfig,
	reviewCache *cache.Cache,
	rules *Rules,
	startTime time.Time,
) (*Report, error) {
	var (
		cachedFindings   []Finding
		uncachedSections []string
		freshFindings    []Finding
		llmMs            int64
	)

	// Step 1: Apply redaction once; the same redacted text is used for both
	// cache key derivation and LLM input (FR-5).
	redactedDiff := diff.Diff
	if cfg.Privacy.RedactSecrets {
		redactedDiff = redact.Secrets(diff.Diff)
	}

	cov := NewCoverage(ConfigReviewer(cfg.Provider, cfg.Model), len(diff.Files), diff.ReviewedBytes(), diff.TruncatedBytes)

	// Step 2: Nothing to review.
	if strings.TrimSpace(redactedDiff) == "" {
		return emptyReport(diff, startTime, cov), nil
	}

	// Step 3: Split into per-file sections.
	sections := diffutil.SplitSections(redactedDiff)

	maxPerFile := cfg.MaxFindingsPerFile
	codebaseBuilder := func(chunkDiff string, files []string, c config.Config, r *Rules) (string, string) {
		return CodebaseSystemPrompt(), BuildCodebaseUserPrompt(chunkDiff, files, c.MaxFindings, maxPerFile, c.FailOn, r)
	}
	prompt := promptFingerprint(codebaseBuilder, cfg.Config, rules)

	// Step 4: Check each section against the per-file cache.
	for _, section := range sections {
		key := fileCacheKey(cfg.Provider, cfg.Model, prompt, section)
		if cached, ok := reviewCache.Get(key); ok {
			parsed, err := parseReviewedFindings(cached, section)
			if err == nil {
				// Valid cache hit — collect findings and skip LLM for this file.
				cachedFindings = append(cachedFindings, parsed...)
				continue
			}
			// Corrupt entry: fall through and treat as a miss (FR-7).
		}
		uncachedSections = append(uncachedSections, section)
	}

	// Step 5: All files cached — skip LLM entirely (AC-1).
	if len(uncachedSections) > 0 {
		// Step 6: Build a filtered diff containing only cache-miss sections.
		filteredDiff := strings.Join(uncachedSections, "")

		// Step 7: Run the chunked review on uncached sections only.
		provider, err := newProvider(cfg.Provider, cfg.Model)
		if err != nil {
			return nil, fmt.Errorf("creating provider: %w", err)
		}

		chunks := SplitIntoChunks(filteredDiff, cfg.ChunkBytes)
		cov.Chunks = len(chunks)
		perChunk, errs, ms, calls := reviewChunks(ctx, chunks, nil, provider, cfg.Config, rules, codebaseBuilder)
		llmMs, cov.LLMCalls = ms, calls

		// Step 8: Store fresh findings per file for future cache hits (FR-4),
		// only for the chunks that succeeded: a failed chunk's files must not
		// be cached as clean.
		haveResults := len(uncachedSections) < len(sections) // some files came from cache
		for i, c := range chunks {
			if errs[i] == nil {
				storeFindingsPerFile(reviewCache, diffutil.SplitSections(c.Diff), perChunk[i], cfg.Provider, cfg.Model, prompt)
				haveResults = true
			}
		}
		skips, err := chunkFailures(ctx, chunks, errs, haveResults)
		if err != nil {
			return nil, fmt.Errorf("chunked review: %w", err)
		}
		cov.Skipped = append(cov.Skipped, skips...)
		freshFindings = mergeChunkFindings(perChunk)
	}

	// Step 9: Merge cached and fresh findings.
	allFindings := append(cachedFindings, freshFindings...)

	// Step 10: Apply rules severity overrides.
	allFindings = ApplySeverityOverrides(allFindings, rules)

	// Step 11: Deduplicate (safety net for any cross-chunk duplicates).
	allFindings = DeduplicateFindings(allFindings)

	// Step 12: Sort high → medium → low, then by path, then by line.
	SortFindings(allFindings)

	// Step 13: Verify against the code (FR-5/FR-6; cached findings re-verify
	// too), suppress accepted findings, and enforce MaxFindings on the merged
	// set (FR-9).
	allFindings, discarded, suppressed, err := FinalizeFindings(ctx, allFindings, diff, cfg.Config)
	if err != nil {
		return nil, err
	}

	// Every file came from the per-file cache: nothing was sent to a model.
	if len(uncachedSections) == 0 {
		cov.CacheHit = true
		cov.Chunks = len(SplitIntoChunks(redactedDiff, cfg.ChunkBytes))
		cov.CachedChunks = cov.Chunks
	}
	report := BuildReport(diff, allFindings, llmMs, time.Since(startTime).Milliseconds())
	cov.Finalize()
	report.Coverage = cov
	report.Discarded = discarded
	report.Suppressed = suppressed
	return report, nil
}

// storeFindingsPerFile stores fresh LLM findings into the cache at per-file
// granularity. Each section gets its own cache entry keyed by fileCacheKey
// (provider, model, prompt fingerprint and section text) so only the changed
// file is a miss on the next run.
//
// If ANY finding in the batch has no primary path the entire batch is left
// uncached — we cannot attribute unattributable findings to a specific file,
// and silently dropping them would cause them to disappear from subsequent
// reports (FR-4, plan Design Decisions).
//
// All write errors are silently ignored (FR-7).
func storeFindingsPerFile(reviewCache *cache.Cache, sections []string, findings []Finding, provider, model, prompt string) {
	// Guard: if any finding lacks a primary path, skip the entire batch.
	for _, f := range findings {
		if len(f.Locations) == 0 || f.Locations[0].Path == "" {
			return
		}
	}

	// Group findings by primary file path.
	byPath := make(map[string][]Finding)
	for _, f := range findings {
		path := f.Locations[0].Path
		byPath[path] = append(byPath[path], f)
	}

	// Write one cache entry per section. Sections with no findings are stored
	// as "[]" so they produce cache hits on the next run (FR-4, AC-5).
	for _, section := range sections {
		path := diffutil.PathFromSection(section)
		if path == "" {
			continue
		}
		key := fileCacheKey(provider, model, prompt, section)
		raw := findingsToRaw(byPath[path]) // nil slice marshals as JSON []
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		_ = reviewCache.Put(key, string(data))
	}
}

// BuildReport constructs a Report from diff metadata, findings, and timing info.
// Report.Provenance is derived from the per-finding Provider/Model pairs.
// Callers are expected to pass pre-sorted findings (reviewPipeline and
// RunChunkedWithOptions both sort before calling BuildReport).
func BuildReport(diff gitctx.DiffResult, findings []Finding, llmMs, totalMs int64) *Report {
	if findings == nil {
		findings = []Finding{}
	}
	return &Report{
		Tool:    "prism",
		Version: "1.0",
		RunID:   GenerateRunID(),
		Repo: RepoInfo{
			Root:   diff.Repo.Root,
			Head:   diff.Repo.Head,
			Branch: diff.Repo.Branch,
		},
		Inputs: InputInfo{
			Mode:  diff.Mode,
			Range: diff.Range,
		},
		Summary:  ComputeSummary(findings),
		Findings: findings,
		Timing: Timing{
			LLMMs:   llmMs,
			TotalMs: totalMs,
		},
		Provenance: CollectProvenance(findings),
		Discarded:  []Discard{},
		Suppressed: []Suppression{},
	}
}

// CollectProvenance returns the deduplicated, stably ordered list of
// (provider, model) pairs across all findings. Order follows first appearance.
func CollectProvenance(findings []Finding) []Provenance {
	seen := make(map[string]bool)
	var out []Provenance
	for _, f := range findings {
		if f.Provider == "" && f.Model == "" {
			continue
		}
		key := f.Provider + "\x00" + f.Model
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Provenance{
			AIGenerated: true,
			Provider:    f.Provider,
			Model:       f.Model,
		})
	}
	return out
}

// emptyReport is the report for a diff with nothing to review. Nothing was
// sent to a model and nothing was missed, so it is complete with no chunks
// (unless the git layer truncated it, which Finalize still reports).
func emptyReport(diff gitctx.DiffResult, startTime time.Time, cov Coverage) *Report {
	r := BuildReport(diff, []Finding{}, 0, time.Since(startTime).Milliseconds())
	cov.Chunks = 0
	cov.Finalize()
	r.Coverage = cov
	return r
}
