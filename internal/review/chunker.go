package review

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
)

const (
	// maxConcurrency limits parallel LLM calls.
	maxConcurrency = 4
	// ChunkThreshold is the byte size above which we switch to chunked review.
	ChunkThreshold = 100000 // 100KB
)

// Chunk represents a portion of a diff to be reviewed independently.
type Chunk struct {
	Index int
	Diff  string
	Files []string
}

// SplitIntoChunks splits a diff into per-file chunks.
// Each chunk contains the diff sections for one or more files,
// staying under maxBytes per chunk.
func SplitIntoChunks(diff string, maxBytes int) []Chunk {
	sections := splitSections(diff)
	if len(sections) == 0 {
		return nil
	}

	if maxBytes <= 0 {
		maxBytes = ChunkThreshold
	}

	var chunks []Chunk
	var currentDiff strings.Builder
	var currentFiles []string
	idx := 0

	for _, sec := range sections {
		path := pathFromSection(sec)

		// If adding this section would exceed maxBytes, flush the current chunk
		if currentDiff.Len() > 0 && currentDiff.Len()+len(sec) > maxBytes {
			chunks = append(chunks, Chunk{
				Index: idx,
				Diff:  currentDiff.String(),
				Files: currentFiles,
			})
			idx++
			currentDiff.Reset()
			currentFiles = nil
		}

		currentDiff.WriteString(sec)
		if path != "" {
			currentFiles = append(currentFiles, path)
		}
	}

	// Flush remaining
	if currentDiff.Len() > 0 {
		chunks = append(chunks, Chunk{
			Index: idx,
			Diff:  currentDiff.String(),
			Files: currentFiles,
		})
	}

	return chunks
}

// NeedsChunking returns true if the diff is large enough to benefit from chunked review.
func NeedsChunking(diff string) bool {
	return len(diff) > ChunkThreshold
}

// RunChunked reviews diff chunks in parallel and merges findings.
func RunChunked(ctx context.Context, chunks []Chunk, provider providers.Reviewer, cfg config.Config) ([]Finding, int64, error) {
	return RunChunkedWithRules(ctx, chunks, provider, cfg, nil)
}

// RunChunkedWithRules reviews diff chunks in parallel with optional rules.
func RunChunkedWithRules(ctx context.Context, chunks []Chunk, provider providers.Reviewer, cfg config.Config, rules *Rules) ([]Finding, int64, error) {
	type result struct {
		index    int
		findings []Finding
		err      error
	}

	results := make([]result, len(chunks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrency)
	var totalLLMMs int64
	var mu sync.Mutex

	for i, chunk := range chunks {
		wg.Add(1)
		go func(i int, chunk Chunk) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			userPrompt := BuildUserPromptWithRules(chunk.Diff, chunk.Files, cfg.MaxFindings, cfg.FailOn, rules)
			req := providers.ReviewRequest{
				SystemPrompt: SystemPrompt(),
				UserPrompt:   userPrompt,
				MaxTokens:    8192,
			}

			llmStart := time.Now()
			resp, err := provider.Review(ctx, req)
			elapsed := time.Since(llmStart).Milliseconds()

			mu.Lock()
			totalLLMMs += elapsed
			mu.Unlock()

			if err != nil {
				results[i] = result{index: i, err: fmt.Errorf("chunk %d: %w", i, err)}
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
					SystemPrompt: SystemPrompt(),
					UserPrompt:   repairPrompt,
					MaxTokens:    8192,
				})
				if err2 != nil {
					results[i] = result{index: i, err: fmt.Errorf("chunk %d repair: %w", i, err2)}
					return
				}
				findings, err = parseFindings(resp2.Content)
				if err != nil {
					results[i] = result{index: i, err: fmt.Errorf("chunk %d validation after repair: %w", i, err)}
					return
				}
			}

			results[i] = result{index: i, findings: findings}
		}(i, chunk)
	}

	wg.Wait()

	// Merge findings in stable order (by chunk index)
	var allFindings []Finding
	for _, r := range results {
		if r.err != nil {
			return nil, totalLLMMs, r.err
		}
		allFindings = append(allFindings, r.findings...)
	}

	// Deduplicate by finding ID
	allFindings = deduplicateFindings(allFindings)

	// Sort by severity (high first), then by file path, then by line
	sort.Slice(allFindings, func(i, j int) bool {
		ri := SeverityRank(allFindings[i].Severity)
		rj := SeverityRank(allFindings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		pi := findingPath(allFindings[i])
		pj := findingPath(allFindings[j])
		if pi != pj {
			return pi < pj
		}
		li := findingStartLine(allFindings[i])
		lj := findingStartLine(allFindings[j])
		return li < lj
	})

	return allFindings, totalLLMMs, nil
}

func deduplicateFindings(findings []Finding) []Finding {
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

func splitSections(diff string) []string {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	var sections []string
	lines := strings.Split(diff, "\n")
	var current strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") && current.Len() > 0 {
			sections = append(sections, current.String())
			current.Reset()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	if current.Len() > 0 {
		s := current.String()
		if strings.TrimSpace(s) != "" {
			sections = append(sections, s)
		}
	}
	return sections
}

func pathFromSection(section string) string {
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			return strings.TrimPrefix(line, "+++ b/")
		}
	}
	return ""
}
