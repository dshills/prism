package review

import (
	"fmt"
	"strings"
)

// Reviewer identifies a model that was asked to review, whether or not it
// produced findings.
type Reviewer struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Skip records part of the input that was not reviewed, and why.
type Skip struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// Coverage describes what a review actually covered
// (specs/SPEC-review-integrity.md FR-1).
type Coverage struct {
	Reviewer []Reviewer `json:"reviewer"`
	Files    int        `json:"files"`
	Bytes    int        `json:"bytes"`
	Chunks   int        `json:"chunks"`
	LLMCalls int        `json:"llmCalls"`
	CacheHit bool       `json:"cacheHit"`
	// CachedChunks is how many of Chunks were replayed from cache instead of
	// sent to a model. It equals Chunks on a full cache hit.
	CachedChunks   int    `json:"cachedChunks"`
	TruncatedBytes int    `json:"truncatedBytes"`
	Skipped        []Skip `json:"skipped"`
	Complete       bool   `json:"complete"`
}

// NewCoverage starts a coverage record for one review of diffText by the given
// reviewers. Truncation is carried over from the git layer and recorded as a
// skip, since the cut bytes were never reviewed.
func NewCoverage(reviewers []Reviewer, files, diffBytes, truncatedBytes int) Coverage {
	c := Coverage{
		Reviewer:       reviewers,
		Files:          files,
		Bytes:          diffBytes,
		TruncatedBytes: truncatedBytes,
		Skipped:        []Skip{},
	}
	if truncatedBytes > 0 {
		c.Skipped = append(c.Skipped, Skip{
			Target: "diff",
			Reason: fmt.Sprintf("truncated at maxDiffBytes (%d bytes not reviewed)", truncatedBytes),
		})
	}
	return c
}

// ConfigReviewer is the single reviewer a non-compare run uses.
func ConfigReviewer(provider, model string) []Reviewer {
	return []Reviewer{{Provider: provider, Model: model}}
}

// Finalize sets Complete: the whole input was reviewed when nothing was
// truncated or skipped. It also guarantees non-nil slices for JSON.
func (c *Coverage) Finalize() {
	if c.Skipped == nil {
		c.Skipped = []Skip{}
	}
	if c.Reviewer == nil {
		c.Reviewer = []Reviewer{}
	}
	c.Complete = c.TruncatedBytes == 0 && len(c.Skipped) == 0
}

// Add folds another review's coverage into c, as the per-commit range review
// does for each commit. The result is a cache hit only if every part was.
func (c *Coverage) Add(o Coverage, first bool) {
	for _, r := range o.Reviewer {
		if !containsReviewer(c.Reviewer, r) {
			c.Reviewer = append(c.Reviewer, r)
		}
	}
	c.Files += o.Files
	c.Bytes += o.Bytes
	c.Chunks += o.Chunks
	c.LLMCalls += o.LLMCalls
	c.CachedChunks += o.CachedChunks
	c.TruncatedBytes += o.TruncatedBytes
	c.Skipped = append(c.Skipped, o.Skipped...)
	if first {
		c.CacheHit = o.CacheHit
	} else {
		c.CacheHit = c.CacheHit && o.CacheHit
	}
}

func containsReviewer(rs []Reviewer, r Reviewer) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// Describe is the one-line coverage statement every text and markdown report
// prints (FR-2). llmMs is the report's model time.
func (c Coverage) Describe(llmMs int64) string {
	if len(c.Reviewer) == 0 {
		return "" // coverage was not recorded for this report
	}
	size := fmt.Sprintf("%s (%.1f KB)", plural(c.Files, "file"), float64(c.Bytes)/1000)
	switch {
	case c.Chunks == 0 && !c.CacheHit && len(c.Skipped) == 0:
		return "Nothing to review (empty diff)"
	case c.Chunks == 0 && !c.CacheHit:
		return "Nothing reviewed"
	case c.CacheHit:
		return fmt.Sprintf("Replayed from cache: %s, originally reviewed by %s", size, reviewerLabel(c.Reviewer))
	default:
		chunks := plural(c.Chunks, "chunk")
		if c.CachedChunks > 0 {
			chunks += fmt.Sprintf(" (%d from cache)", c.CachedChunks)
		}
		return fmt.Sprintf("Reviewed %s in %s by %s — %s, %.1fs",
			size, chunks, reviewerLabel(c.Reviewer),
			plural(c.LLMCalls, "LLM call"), float64(llmMs)/1000)
	}
}

// Incomplete reports whether part of the input was positively not reviewed:
// something was truncated or skipped. It is defined by those facts rather
// than by !Complete, so a report whose coverage was never recorded (the zero
// value) is not mistaken for an incomplete review.
func (c Coverage) Incomplete() bool {
	return c.TruncatedBytes > 0 || len(c.Skipped) > 0
}

// IncompleteLine explains what was not reviewed, or is empty when nothing was
// missed.
func (c Coverage) IncompleteLine() string {
	if !c.Incomplete() {
		return ""
	}
	parts := make([]string, 0, len(c.Skipped))
	for _, s := range c.Skipped {
		parts = append(parts, s.Target+": "+s.Reason)
	}
	return "INCOMPLETE: not everything was reviewed — " + strings.Join(parts, "; ")
}

func reviewerLabel(rs []Reviewer) string {
	if len(rs) == 0 {
		return "unknown model"
	}
	labels := make([]string, len(rs))
	for i, r := range rs {
		labels[i] = r.Provider + "/" + r.Model
	}
	return strings.Join(labels, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// CompareCoverage is the coverage of a compare-mode review: every model saw the
// whole diff once.
func CompareCoverage(reviewers []Reviewer, files, diffBytes, truncatedBytes, calls int) Coverage {
	c := NewCoverage(reviewers, files, diffBytes, truncatedBytes)
	c.Chunks = len(reviewers)
	c.LLMCalls = calls
	c.Finalize()
	return c
}

// ReviewersFromSpecs turns "provider:model" specs into reviewers, skipping
// malformed specs (those fail earlier, when the provider is created).
func ReviewersFromSpecs(specs []string) []Reviewer {
	out := make([]Reviewer, 0, len(specs))
	for _, spec := range specs {
		parts := strings.SplitN(spec, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		out = append(out, Reviewer{Provider: parts[0], Model: parts[1]})
	}
	return out
}
