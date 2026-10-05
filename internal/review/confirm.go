package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/dshills/prism/internal/cache"
	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/diffutil"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/redact"
)

const (
	// maxConfirmations caps the second-opinion calls of one review; the
	// most severe blocking findings are checked first, the rest kept as
	// they are.
	maxConfirmations = 20
	// confirmConcurrency is how many checks run at once.
	confirmConcurrency = 4
	// maxConfirmContext is the most code sent with a finding, around its
	// evidence.
	maxConfirmContext = 12000
	// confirmVersion is part of a cached verdict's key: a change to the
	// prompt or its parsing must not replay verdicts given to the old one.
	confirmVersion = 1
)

const confirmSystemPrompt = `You are a senior engineer checking one finding from an automated code review before it blocks a change.

Read the code and decide whether the finding is correct: the problem it describes exists in this code and matters.
- Refute it when the code does not do what the finding claims, the problem is handled in the code shown, or the claim rests on an assumption the code contradicts.
- Confirm it when it holds, or when the code shown is not enough to tell. Refute only what the code shows to be wrong.

Respond with only a JSON object: {"verdict": "confirm" or "refute", "reason": "one or two sentences"}.`

// confirmOutput is the verdict's schema, for providers with structured
// output.
var confirmOutput = &providers.Output{
	Name:        "verdict",
	Description: "Whether the review finding is correct.",
	Schema: &providers.Schema{Type: "object", Properties: []providers.Property{
		{Name: "verdict", Schema: &providers.Schema{Type: "string", Enum: []string{"confirm", "refute"}}},
		{Name: "reason", Schema: &providers.Schema{Type: "string"}},
	}},
}

// ConfirmUse records the second-opinion check of blocking findings.
type ConfirmUse struct {
	Reviewer Reviewer `json:"reviewer"`
	// Checked is how many blocking findings were checked; Refuted how many
	// of them were discarded. Failed checks keep their finding.
	Checked int `json:"checked"`
	Refuted int `json:"refuted"`
	Failed  int `json:"failed,omitempty"`
	// Unchecked is the blocking findings not checked, and kept as they
	// are: over maxConfirmations, or in a file too large to send whole
	// whose code at the finding could not be located.
	Unchecked int `json:"unchecked,omitempty"`
}

type verdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// confirmBlocking asks cfg.ConfirmBlocking's model for a second opinion on
// each finding at or above failOn, the ones that block the agent. A refuted
// finding is discarded with the model's reason; one whose check fails is
// kept, since a broken check must not wave a real problem through. Verdicts
// are cached by the finding and the code shown with it, so a re-review of
// the same code does not pay again. The calls and tokens are added to cov.
func confirmBlocking(ctx context.Context, findings []Finding, diff gitctx.DiffResult, cfg config.Config, cov *Coverage) ([]Finding, []Discard, error) {
	if cfg.ConfirmBlocking == "" || cfg.FailOn == "" || cfg.FailOn == "none" {
		return findings, nil, nil
	}
	var blocking []int
	for i, f := range findings {
		if MeetsThreshold(f.Severity, cfg.FailOn) {
			blocking = append(blocking, i)
		}
	}
	if len(blocking) == 0 {
		return findings, nil, nil
	}
	providerName, model, err := parseModelSpec(cfg.ConfirmBlocking)
	if err != nil {
		return nil, nil, fmt.Errorf("confirmBlocking: %w", err)
	}
	checker, err := newProvider(providerName, model)
	if err != nil {
		return nil, nil, fmt.Errorf("confirmBlocking: creating provider: %w", err)
	}
	rc, cerr := cache.New(cfg.Cache.Enabled, cfg.Cache.Dir, cfg.Cache.TTLSeconds)
	if cerr != nil {
		rc, _ = cache.New(false, "", 0)
	}

	// The model is shown what the reviewer was: the redacted diff.
	text := diff.Diff
	if cfg.Privacy.RedactSecrets {
		text = redact.Secrets(text)
	}
	sections := map[string]string{}
	for _, s := range diffutil.SplitSections(text) {
		sections[diffutil.PathFromSection(s)] += s
	}

	use := &ConfirmUse{Reviewer: Reviewer{Provider: providerName, Model: model}}
	if len(blocking) > maxConfirmations {
		use.Unchecked = len(blocking) - maxConfirmations
		blocking = blocking[:maxConfirmations] // findings come most severe first
	}

	type result struct {
		v         verdict
		ok        bool
		calls     int
		resp      providers.ReviewResponse
		unlocated bool // the code could not be located; not checked
	}
	results := make([]result, len(blocking))
	var wg sync.WaitGroup
	sem := make(chan struct{}, confirmConcurrency)
	for n, i := range blocking {
		wg.Add(1)
		go func(n int, f Finding) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			user, located := confirmPrompt(f, sections[findingPath(f)])
			if !located {
				results[n] = result{unlocated: true}
				return
			}
			if cfg.Privacy.RedactSecrets {
				user = redact.Secrets(user) // the finding's own text too
			}
			key := cache.BuildCacheKey(providerName, model,
				fmt.Sprintf("confirm=%d,effort=%s\n%s\x00%s", confirmVersion, cfg.ReasoningEffort, confirmSystemPrompt, user))
			if cached, hit := rc.Get(key); hit {
				if v, ok := parseVerdict(cached); ok {
					results[n] = result{v: v, ok: true}
					return
				}
			}
			resp, err := checker.Review(ctx, providers.ReviewRequest{
				SystemPrompt: confirmSystemPrompt,
				UserPrompt:   user,
				MaxTokens:    4096,
				Effort:       cfg.ReasoningEffort,
				Output:       confirmOutput,
			})
			r := result{calls: providers.CallsOf(resp), resp: resp}
			if err == nil {
				if r.v, r.ok = parseVerdict(resp.Content); r.ok {
					_ = rc.Put(key, resp.Content)
				}
			}
			results[n] = r
		}(n, findings[i])
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	var usage usageLedger
	refuted := map[int]string{}
	for n, i := range blocking {
		r := results[n]
		if r.unlocated {
			use.Unchecked++
			continue
		}
		cov.LLMCalls += r.calls
		usage.add(r.resp.Provider, r.resp.Model, r.resp.Usage)
		if !r.ok {
			use.Failed++
			continue
		}
		use.Checked++
		if r.v.Verdict == "refute" {
			use.Refuted++
			refuted[i] = r.v.Reason
		}
	}
	tokens := usage.list()
	priceTokens(tokens, cfg.Prices)
	cov.Tokens = mergeTokens(cov.Tokens, tokens)
	cov.Confirm = use

	kept := make([]Finding, 0, len(findings))
	var discarded []Discard
	for i, f := range findings {
		if reason, ok := refuted[i]; ok {
			discarded = append(discarded, Discard{Finding: f, Reason: fmt.Sprintf("refuted by %s: %s", cfg.ConfirmBlocking, reason)})
			continue
		}
		kept = append(kept, f)
	}
	return kept, discarded, nil
}

// confirmPrompt is the user prompt for checking f: the finding, then the
// code it is about. ok is false when that code cannot be located (excerpt).
func confirmPrompt(f Finding, section string) (string, bool) {
	code, ok := excerpt(section, f.Evidence, findingStartLine(f))
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString("The finding:\n")
	fmt.Fprintf(&b, "- Severity: %s\n- Category: %s\n- Title: %s\n- Message: %s\n", f.Severity, f.Category, f.Title, f.Message)
	if len(f.Locations) > 0 {
		l := f.Locations[0]
		fmt.Fprintf(&b, "- Location: %s lines %d-%d\n", l.Path, l.Lines.Start, l.Lines.End)
	}
	if f.Evidence != "" {
		fmt.Fprintf(&b, "- Evidence:\n%s\n", f.Evidence)
	}
	b.WriteString("\n--- BEGIN CODE ---\n")
	b.WriteString(code)
	b.WriteString("\n--- END CODE ---\n")
	return b.String(), true
}

// excerpt is up to maxConfirmContext bytes of section around the finding's
// line, cut at line ends. The line is found by the hunk headers' line
// numbers, or else by the evidence when it occurs exactly once. ok is false
// when a section too large to send whole has neither: an excerpt of the
// wrong place could refute a real finding, so the finding is not checked.
func excerpt(section, evidence string, line int) (string, bool) {
	if strings.TrimSpace(section) == "" {
		return "", false // no code to show: a verdict would be a guess
	}
	if len(section) <= maxConfirmContext {
		return section, true
	}
	at := lineOffset(section, line)
	if ev := strings.TrimSpace(evidence); at < 0 && ev != "" && strings.Count(section, ev) == 1 {
		at = strings.Index(section, ev)
		at = strings.LastIndexByte(section[:at], '\n') + 1 // its line's start
	}
	if at < 0 {
		return "", false
	}
	lineEnd := len(section)
	if i := strings.IndexByte(section[at:], '\n'); i >= 0 {
		lineEnd = at + i + 1
	}
	if lineEnd-at > maxConfirmContext {
		return "", false // the line alone is too long to show
	}
	// A window around the line, then trimmed to whole lines outside it.
	from := max(0, at-(maxConfirmContext-(lineEnd-at))/2)
	to := min(len(section), from+maxConfirmContext)
	from = max(0, to-maxConfirmContext)
	if from > 0 {
		if i := strings.IndexByte(section[from:at], '\n'); i >= 0 {
			from += i + 1
		} else {
			from = at
		}
	}
	if to < len(section) {
		if i := strings.LastIndexByte(section[lineEnd:to], '\n'); i >= 0 {
			to = lineEnd + i + 1
		} else {
			to = lineEnd
		}
	}
	return section[from:to], true
}

// lineOffset is the offset in a diff section of the line numbered line in
// the new file, or -1 when no hunk shows it.
func lineOffset(section string, line int) int {
	if line <= 0 {
		return -1
	}
	cur, off := 0, 0
	for _, l := range strings.SplitAfter(section, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			cur = 0 // a file's header lines, "+++ b/..." among them, are not code
		case strings.HasPrefix(l, "@@ "):
			cur = 0
			if _, after, ok := strings.Cut(l, " +"); ok {
				n, _, _ := strings.Cut(after, ",")
				n, _, _ = strings.Cut(n, " ")
				cur, _ = strconv.Atoi(n)
			}
		case cur > 0 && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, " ")):
			if cur == line {
				return off
			}
			cur++
		}
		off += len(l)
	}
	return -1
}

// parseVerdict reads a verdict object, fenced or not. ok is false for
// anything else, including a verdict that is neither confirm nor refute.
func parseVerdict(content string) (verdict, bool) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	var v verdict
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &v) != nil {
		return verdict{}, false
	}
	v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
	if v.Verdict != "confirm" && v.Verdict != "refute" {
		return verdict{}, false
	}
	return v, true
}
