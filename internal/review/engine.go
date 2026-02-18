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
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/redact"
)

// rawFinding is the JSON structure returned by the LLM.
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
}

// Run executes a review using the given diff result and configuration.
func Run(ctx context.Context, diff gitctx.DiffResult, cfg config.Config) (*Report, error) {
	startTime := time.Now()
	gitMs := time.Since(startTime).Milliseconds()

	// Redact secrets from diff before sending to provider
	redactedDiff := diff.Diff
	if cfg.Privacy.RedactSecrets {
		redactedDiff = redact.Secrets(redactedDiff)
	}

	if strings.TrimSpace(redactedDiff) == "" {
		return emptyReport(diff, gitMs, startTime), nil
	}

	// Initialize cache
	reviewCache, err := cache.New(cfg.Cache.Enabled, cfg.Cache.Dir, cfg.Cache.TTLSeconds)
	if err != nil {
		// Cache failure is non-fatal, just disable it
		reviewCache, _ = cache.New(false, "", 0)
	}

	cacheKey := cache.BuildCacheKey(cfg.Provider, cfg.Model, redactedDiff)

	// Check cache
	var findings []Finding
	var llmMs int64
	if cached, ok := reviewCache.Get(cacheKey); ok {
		findings, err = parseFindings(cached)
		if err != nil {
			// Cache entry is corrupt, fall through to LLM
			findings = nil
		}
	}

	// Load rules
	rules, err := LoadRules(cfg.RulesFile)
	if err != nil {
		return nil, fmt.Errorf("loading rules: %w", err)
	}

	if findings == nil {
		provider, err := providers.New(cfg.Provider, cfg.Model)
		if err != nil {
			return nil, fmt.Errorf("creating provider: %w", err)
		}

		// Use chunked review for large diffs
		if NeedsChunking(redactedDiff) {
			chunks := SplitIntoChunks(redactedDiff, cfg.MaxDiffBytes)
			findings, llmMs, err = RunChunkedWithRules(ctx, chunks, provider, cfg, rules)
			if err != nil {
				return nil, fmt.Errorf("chunked review: %w", err)
			}
		} else {
			userPrompt := BuildUserPromptWithRules(redactedDiff, diff.Files, cfg.MaxFindings, cfg.FailOn, rules)

			llmStart := time.Now()
			req := providers.ReviewRequest{
				SystemPrompt: SystemPrompt(),
				UserPrompt:   userPrompt,
				MaxTokens:    8192,
			}

			resp, err := provider.Review(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("provider review: %w", err)
			}
			llmMs = time.Since(llmStart).Milliseconds()

			findings, err = parseFindings(resp.Content)
			if err != nil {
				// Attempt one repair pass
				repairPrompt := fmt.Sprintf(
					"Your previous response was not valid JSON. The error was: %s\n\nPlease fix it and respond with ONLY a valid JSON array of findings.\n\nYour previous response was:\n%s",
					err.Error(), resp.Content,
				)
				repairReq := providers.ReviewRequest{
					SystemPrompt: SystemPrompt(),
					UserPrompt:   repairPrompt,
					MaxTokens:    8192,
				}
				resp2, err2 := provider.Review(ctx, repairReq)
				if err2 != nil {
					return nil, fmt.Errorf("repair pass failed: %w (original error: %w)", err2, err)
				}
				findings, err = parseFindings(resp2.Content)
				if err != nil {
					return nil, fmt.Errorf("response validation failed after repair: %w", err)
				}
			}
		}

		// Store in cache (the raw JSON response for the findings)
		if findingsJSON, jerr := json.Marshal(findings); jerr == nil {
			_ = reviewCache.Put(cacheKey, string(findingsJSON))
		}
	}

	// Apply rules severity overrides
	findings = ApplySeverityOverrides(findings, rules)

	// Limit findings
	if cfg.MaxFindings > 0 && len(findings) > cfg.MaxFindings {
		findings = findings[:cfg.MaxFindings]
	}

	totalMs := time.Since(startTime).Milliseconds()

	report := &Report{
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
			GitMs:   gitMs,
			LLMMs:   llmMs,
			TotalMs: totalMs,
		},
	}

	return report, nil
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
			content = strings.Join(lines[start:end], "\n")
		}
	}

	var raw []rawFinding
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON array: %w", err)
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

func generateFindingID(f Finding) string {
	var path string
	if len(f.Locations) > 0 {
		path = f.Locations[0].Path
	}
	data := fmt.Sprintf("%s:%s:%d", path, f.Title, func() int {
		if len(f.Locations) > 0 {
			return f.Locations[0].Lines.Start
		}
		return 0
	}())
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:8])
}

// GenerateRunID creates a unique run identifier.
func GenerateRunID() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return fmt.Sprintf("%x", h[:16])
}

func emptyReport(diff gitctx.DiffResult, gitMs int64, startTime time.Time) *Report {
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
		Summary:  Summary{},
		Findings: []Finding{},
		Timing: Timing{
			GitMs:   gitMs,
			TotalMs: time.Since(startTime).Milliseconds(),
		},
	}
}
