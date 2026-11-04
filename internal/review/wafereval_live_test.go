//go:build wafereval

package review_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/review"
)

// TestWaferEval benchmarks Wafer models on the seeded-bug corpus: every
// model at every reasoning effort it takes reviews every case, through the
// same pipeline as `prism review commit`. It reports, per model and effort,
// the bugs caught, false positives, time and cost. The calls are live and
// billed, so it runs only with the wafereval build tag:
//
//	make eval-wafer
//
// Settings, from the environment:
//
//	WAFER_EVAL_MODELS    models, comma-separated (default: evalModels)
//	WAFER_EVAL_EFFORTS   efforts to try, of those each model takes (default: all)
//	WAFER_EVAL_CASES     case IDs to run (default: all)
//	WAFER_EVAL_RUNS      reviews of each case per model and effort (default 1)
//	WAFER_EVAL_PARALLEL  reviews in flight at once (default 4)
//	WAFER_EVAL_OUT       results file (.json; a .md summary goes beside it)
func TestWaferEval(t *testing.T) {
	if os.Getenv("WAFER_API_KEY") == "" {
		t.Skip("WAFER_API_KEY not set")
	}
	catalog := fetchWaferCatalog(t)
	cases := loadEvalCases(t)
	if only := envList("WAFER_EVAL_CASES"); len(only) > 0 {
		cases = slices.DeleteFunc(cases, func(c evalCase) bool { return !slices.Contains(only, c.ID) })
	}
	if len(cases) == 0 {
		t.Fatal("no cases selected")
	}
	runs := envInt(t, "WAFER_EVAL_RUNS", 1)
	parallel := envInt(t, "WAFER_EVAL_PARALLEL", 4)

	cfg := config.Default()
	cfg.Provider = "wafer"
	cfg.Cache.Enabled = false // every review is a real call
	cfg.Cache.Dir = t.TempDir()
	cfg.BaselineFile = "none"

	diffs := make(map[string]gitctx.DiffResult, len(cases))
	for _, c := range cases {
		diffs[c.ID] = buildEvalDiff(t, c, cfg)
	}

	type combo struct {
		model waferModel
		// effort is "" for a model that takes none: its default is used.
		effort string
	}
	var combos []combo
	models := envList("WAFER_EVAL_MODELS")
	if len(models) == 0 {
		models = evalModels
	}
	efforts := envList("WAFER_EVAL_EFFORTS")
	for _, name := range models {
		m, ok := catalog[strings.ToLower(name)]
		if !ok {
			t.Errorf("model %s is not in Wafer's catalog", name)
			continue
		}
		levels := m.Efforts
		switch {
		case len(m.Efforts) == 0:
			levels = []string{""} // takes no effort: its default
		case len(efforts) > 0:
			levels = slices.DeleteFunc(slices.Clone(levels), func(e string) bool { return !slices.Contains(efforts, e) })
			if len(levels) == 0 {
				t.Errorf("model %s takes efforts %v, none of WAFER_EVAL_EFFORTS %v", m.ID, m.Efforts, efforts)
				continue
			}
		}
		for _, e := range levels {
			combos = append(combos, combo{m, e})
		}
	}
	if len(combos) == 0 {
		t.Fatal("no model and effort selected")
	}

	type job struct {
		combo
		c   evalCase
		run int
	}
	var jobs []job
	for _, cb := range combos {
		for _, c := range cases {
			for r := 1; r <= runs; r++ {
				jobs = append(jobs, job{cb, c, r})
			}
		}
	}
	t.Logf("%d reviews: %d model/effort pairs x %d cases x %d runs, %d at a time",
		len(jobs), len(combos), len(cases), runs, parallel)

	out := os.Getenv("WAFER_EVAL_OUT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "prism-wafer-eval-"+time.Now().Format("20060102-150405")+".json")
	}
	if filepath.Ext(out) != ".json" {
		t.Fatalf("WAFER_EVAL_OUT=%s: want a .json path (the summary is written beside it as .md)", out)
	}
	stem := strings.TrimSuffix(out, ".json")
	mdPath := stem + ".md"
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	// Each review is appended here as it finishes, so a run cut short
	// (timeout, interrupt) keeps what it paid for.
	progress, err := os.Create(stem + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = progress.Close() }()
	t.Logf("progress: %s", progress.Name())

	results := make([]evalRun, len(jobs))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)
	sem := make(chan struct{}, max(parallel, 1))
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := runEval(t.Context(), cfg, j.model, j.effort, j.c, j.run, diffs[j.c.ID])
			results[i] = r
			line, err := json.Marshal(r)
			mu.Lock()
			defer mu.Unlock()
			done++
			t.Logf("[%d/%d] %s", done, len(jobs), r.line())
			if err == nil {
				_, err = progress.Write(append(line, '\n'))
			}
			if err != nil {
				t.Errorf("saving %s: %v", r.Case, err)
			}
		}()
	}
	wg.Wait()

	summaries := summarizeEval(results, cases)
	md := evalMarkdown(summaries, results, cases, runs)
	t.Log("\n" + md)

	data, err := json.MarshalIndent(map[string]any{
		"generatedAt": time.Now().UTC(),
		"runs":        runs,
		"summary":     summaries,
		"reviews":     results,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("results: %s\nsummary: %s", out, mdPath)

	if slices.IndexFunc(results, func(r evalRun) bool { return r.Error == "" }) < 0 {
		t.Fatal("every review failed")
	}
}

// evalModels are the models benchmarked by default.
var evalModels = []string{"GLM-5.3", "DeepSeek-V4.1-Flash", "DeepSeek-V4-Pro", "Kimi-K3"}

// waferModel is a model in Wafer's catalog: the efforts it takes, and its
// price in US cents per million tokens.
type waferModel struct {
	ID              string   `json:"id"`
	Efforts         []string `json:"efforts"`
	InputCents      float64  `json:"inputCentsPerMillion"`
	CacheReadCents  float64  `json:"cacheReadCentsPerMillion"`
	OutputCents     float64  `json:"outputCentsPerMillion"`
	ContextLength   int      `json:"contextLength"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
}

// costUSD is what usage cost, cached input at the cache-read price.
func (m waferModel) costUSD(u providers.Usage) float64 {
	cached := min(u.CachedInputTokens, u.InputTokens)
	cents := float64(u.InputTokens-cached)*m.InputCents +
		float64(cached)*m.CacheReadCents +
		float64(u.OutputTokens)*m.OutputCents
	return cents / 1e6 / 100
}

// fetchWaferCatalog reads Wafer's model list, keyed by lowercased ID, for
// each model's efforts and prices. The list is live, so a benchmark uses
// today's prices and never asks a model for an effort it does not take
// (a refused effort is silently dropped, which would mislabel the run).
func fetchWaferCatalog(t *testing.T) map[string]waferModel {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("WAFER_API_URL"))
	if base == "" {
		base = "https://pass.wafer.ai/v1"
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/chat/completions")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("WAFER_API_KEY"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("wafer models: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wafer models: %s", resp.Status)
	}
	var list struct {
		Data []struct {
			ID    string `json:"id"`
			Wafer struct {
				ContextLength   int  `json:"context_length"`
				MaxOutputTokens *int `json:"max_output_tokens"`
				Capabilities    struct {
					ReasoningEffort struct {
						Efforts []string `json:"efforts"`
					} `json:"reasoning_effort"`
				} `json:"capabilities"`
				Pricing struct {
					Input     float64 `json:"input_cents_per_million"`
					CacheRead float64 `json:"cache_read_cents_per_million"`
					Output    float64 `json:"output_cents_per_million"`
				} `json:"pricing"`
			} `json:"wafer"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("wafer models: %v", err)
	}
	catalog := make(map[string]waferModel, len(list.Data))
	for _, d := range list.Data {
		m := waferModel{
			ID:             d.ID,
			Efforts:        d.Wafer.Capabilities.ReasoningEffort.Efforts,
			InputCents:     d.Wafer.Pricing.Input,
			CacheReadCents: d.Wafer.Pricing.CacheRead,
			OutputCents:    d.Wafer.Pricing.Output,
			ContextLength:  d.Wafer.ContextLength,
		}
		if d.Wafer.MaxOutputTokens != nil {
			m.MaxOutputTokens = *d.Wafer.MaxOutputTokens
		}
		catalog[strings.ToLower(d.ID)] = m
	}
	return catalog
}

// evalRun is one review of one case.
type evalRun struct {
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Case     string `json:"case"`
	Run      int    `json:"run"`
	WallMs   int64  `json:"wallMs"`
	LLMCalls int    `json:"llmCalls"`
	providers.Usage
	CostUSD float64 `json:"costUSD"`
	// Caught and Missed are bug IDs; CaughtBlocking are the caught bugs
	// whose finding was medium or high.
	Caught         []string `json:"caught"`
	CaughtBlocking []string `json:"caughtBlocking"`
	Missed         []string `json:"missed"`
	// Extra counts the findings that caught no seeded bug; FalsePositives
	// the medium and high ones on a control case.
	Extra          int              `json:"extra"`
	FalsePositives int              `json:"falsePositives"`
	Discarded      []review.Discard `json:"discarded"`
	Complete       bool             `json:"complete"`
	Skipped        []review.Skip    `json:"skipped,omitempty"`
	Error          string           `json:"error,omitempty"`
	Findings       []review.Finding `json:"findings"`
}

func (r evalRun) failed() bool { return r.Error != "" || !r.Complete }

func (r evalRun) line() string {
	who := r.Model + " " + effortLabel(r.Effort) + " " + r.Case
	if r.Error != "" {
		return fmt.Sprintf("%s: error after %.1fs: %s", who, float64(r.WallMs)/1000, r.Error)
	}
	s := fmt.Sprintf("%s: caught %d/%d, %d extra, %.1fs, %d out tokens, $%.4f",
		who, len(r.Caught), len(r.Caught)+len(r.Missed), r.Extra, float64(r.WallMs)/1000, r.OutputTokens, r.CostUSD)
	if r.FalsePositives > 0 {
		s += fmt.Sprintf(", %d false positive(s)", r.FalsePositives)
	}
	if !r.Complete {
		s += " (incomplete)"
	}
	return s
}

func effortLabel(e string) string {
	if e == "" {
		return "default"
	}
	return e
}

func runEval(ctx context.Context, cfg config.Config, m waferModel, effort string, c evalCase, run int, diff gitctx.DiffResult) evalRun {
	cfg.Model = m.ID
	cfg.ReasoningEffort = effort
	r := evalRun{Model: m.ID, Effort: effort, Case: c.ID, Run: run, Caught: []string{}, CaughtBlocking: []string{}, Missed: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	start := time.Now()
	report, err := review.Run(ctx, diff, cfg)
	r.WallMs = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = err.Error()
		r.Missed = bugIDs(c)
		return r
	}
	for _, tok := range report.Coverage.Tokens {
		r.Add(tok.Usage)
	}
	r.CostUSD = m.costUSD(r.Usage)
	r.LLMCalls = report.Coverage.LLMCalls
	r.Complete = report.Coverage.Complete
	r.Skipped = report.Coverage.Skipped
	r.Discarded = report.Discarded
	r.Findings = report.Findings

	match := matchEval(c, report.Findings)
	for _, b := range c.Bugs {
		f, ok := match.Caught[b.ID]
		if !ok {
			r.Missed = append(r.Missed, b.ID)
			continue
		}
		r.Caught = append(r.Caught, b.ID)
		if review.SeverityRank(f.Severity) >= review.SeverityRank(review.SeverityMedium) {
			r.CaughtBlocking = append(r.CaughtBlocking, b.ID)
		}
	}
	r.Extra = len(match.Extra)
	r.FalsePositives = match.falsePositives(c)
	return r
}

// evalSummary is one model and effort over every case and run.
type evalSummary struct {
	Model          string  `json:"model"`
	Effort         string  `json:"effort"`
	Reviews        int     `json:"reviews"`
	Failed         int     `json:"failed"`
	Expected       int     `json:"expected"`
	Caught         int     `json:"caught"`
	CaughtBlocking int     `json:"caughtBlocking"`
	Recall         float64 `json:"recall"`
	Extra          int     `json:"extra"`
	FalsePositives int     `json:"falsePositives"`
	MedianMs       int64   `json:"medianMs"`
	MaxMs          int64   `json:"maxMs"`
	TotalMs        int64   `json:"totalMs"`
	providers.Usage
	CostUSD float64 `json:"costUSD"`
	// Missed counts each missed bug's misses over the runs.
	Missed map[string]int `json:"missed"`
}

func (s evalSummary) name() string { return s.Model + " / " + effortLabel(s.Effort) }

func summarizeEval(results []evalRun, cases []evalCase) []evalSummary {
	byKey := map[string]*evalSummary{}
	var order []string
	walls := map[string][]int64{}
	for _, r := range results {
		key := r.Model + "\x00" + r.Effort
		s := byKey[key]
		if s == nil {
			s = &evalSummary{Model: r.Model, Effort: r.Effort, Missed: map[string]int{}}
			byKey[key] = s
			order = append(order, key)
		}
		s.Reviews++
		if r.failed() {
			s.Failed++
		}
		s.Expected += len(r.Caught) + len(r.Missed)
		s.Caught += len(r.Caught)
		s.CaughtBlocking += len(r.CaughtBlocking)
		s.Extra += r.Extra
		s.FalsePositives += r.FalsePositives
		s.TotalMs += r.WallMs
		s.MaxMs = max(s.MaxMs, r.WallMs)
		s.Add(r.Usage)
		s.CostUSD += r.CostUSD
		for _, id := range r.Missed {
			s.Missed[id]++
		}
		walls[key] = append(walls[key], r.WallMs)
	}
	out := make([]evalSummary, 0, len(order))
	for _, key := range order {
		s := byKey[key]
		if s.Expected > 0 {
			s.Recall = float64(s.Caught) / float64(s.Expected)
		}
		w := walls[key]
		slices.Sort(w)
		s.MedianMs = w[len(w)/2]
		out = append(out, *s)
	}
	// Most bugs caught first; then fewer false alarms, then cheaper.
	slices.SortStableFunc(out, func(a, b evalSummary) int {
		return cmp.Or(
			cmp.Compare(b.Recall, a.Recall),
			cmp.Compare(a.FalsePositives, b.FalsePositives),
			cmp.Compare(a.Failed, b.Failed),
			cmp.Compare(a.CostUSD, b.CostUSD),
		)
	})
	return out
}

func evalMarkdown(summaries []evalSummary, results []evalRun, cases []evalCase, runs int) string {
	var b strings.Builder
	nBugs := 0
	for _, c := range cases {
		nBugs += len(c.Bugs)
	}
	fmt.Fprintf(&b, "# Wafer review benchmark\n\n%s. %d cases, %d seeded bugs, %d run(s) of each.\n\n",
		time.Now().Format("2006-01-02 15:04"), len(cases), nBugs, runs)

	b.WriteString("| Model / effort | Caught | Recall | Caught ≥ medium | Extra findings | Control FPs | Failed | Median time | Slowest | Output tokens (reasoning) | Cost | Cost per bug |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range summaries {
		perBug := "—"
		if s.Caught > 0 {
			perBug = fmt.Sprintf("$%.4f", s.CostUSD/float64(s.Caught))
		}
		fmt.Fprintf(&b, "| %s | %d/%d | %.0f%% | %d | %d | %d | %d/%d | %.1fs | %.1fs | %d (%d) | $%.4f | %s |\n",
			s.name(), s.Caught, s.Expected, 100*s.Recall, s.CaughtBlocking, s.Extra, s.FalsePositives,
			s.Failed, s.Reviews, float64(s.MedianMs)/1000, float64(s.MaxMs)/1000,
			s.OutputTokens, s.ReasoningTokens, s.CostUSD, perBug)
	}

	if len(summaries) > 0 {
		best := summaries[0]
		near := slices.DeleteFunc(slices.Clone(summaries), func(s evalSummary) bool {
			return s.Recall < best.Recall-0.10 || s.Failed > 0
		})
		b.WriteString("\n## Picks\n\n")
		fmt.Fprintf(&b, "- **Most bugs caught:** %s (%.0f%%, %d control false positives, $%.4f)\n",
			best.name(), 100*best.Recall, best.FalsePositives, best.CostUSD)
		if len(near) > 0 {
			fast := slices.MinFunc(near, func(a, b evalSummary) int { return cmp.Compare(a.MedianMs, b.MedianMs) })
			cheap := slices.MinFunc(near, func(a, b evalSummary) int { return cmp.Compare(a.CostUSD, b.CostUSD) })
			fmt.Fprintf(&b, "- **Fastest within 10 points of the best recall:** %s (median %.1fs, %.0f%%)\n",
				fast.name(), float64(fast.MedianMs)/1000, 100*fast.Recall)
			fmt.Fprintf(&b, "- **Cheapest within 10 points of the best recall:** %s ($%.4f, %.0f%%)\n",
				cheap.name(), cheap.CostUSD, 100*cheap.Recall)
		}
		fast := slices.MinFunc(summaries, func(a, b evalSummary) int { return cmp.Compare(a.MedianMs, b.MedianMs) })
		cheap := slices.MinFunc(summaries, func(a, b evalSummary) int { return cmp.Compare(a.CostUSD, b.CostUSD) })
		fmt.Fprintf(&b, "- **Fastest overall:** %s (median %.1fs, %.0f%%)\n", fast.name(), float64(fast.MedianMs)/1000, 100*fast.Recall)
		fmt.Fprintf(&b, "- **Cheapest overall:** %s ($%.4f, %.0f%%)\n", cheap.name(), cheap.CostUSD, 100*cheap.Recall)
	}

	b.WriteString("\n## By bug\n\n| Bug | Caught by | Missed by |\n|---|---|---|\n")
	for _, c := range cases {
		for _, bug := range c.Bugs {
			caught, total := 0, 0
			var missedBy []string
			for _, s := range summaries {
				n := s.Missed[bug.ID]
				total += s.Reviews / len(cases)
				caught += s.Reviews/len(cases) - n
				if n > 0 {
					label := s.name()
					if runs > 1 {
						label += " ×" + strconv.Itoa(n)
					}
					missedBy = append(missedBy, label)
				}
			}
			fmt.Fprintf(&b, "| `%s` | %d/%d | %s |\n", bug.ID, caught, total, strings.Join(missedBy, ", "))
		}
	}

	var errs []string
	for _, r := range results {
		if r.failed() {
			reason := r.Error
			if reason == "" {
				var parts []string
				for _, s := range r.Skipped {
					parts = append(parts, s.Target+": "+s.Reason)
				}
				reason = "incomplete: " + strings.Join(parts, "; ")
			}
			errs = append(errs, fmt.Sprintf("- %s / %s, %s run %d: %s", r.Model, effortLabel(r.Effort), r.Case, r.Run, reason))
		}
	}
	if len(errs) > 0 {
		b.WriteString("\n## Failed or incomplete reviews\n\n" + strings.Join(errs, "\n") + "\n")
	}
	b.WriteString("\nCost uses Wafer's catalog prices at run time, cached input at the cache-read price. " +
		"Time is wall time per review, with the other reviews in flight alongside.\n")
	return b.String()
}

func envList(name string) []string {
	var out []string
	for f := range strings.SplitSeq(os.Getenv(name), ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func envInt(t *testing.T, name string, def int) int {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("%s=%q: want a positive integer", name, v)
	}
	return n
}
