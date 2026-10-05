package review

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/diffutil"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/ratelimit"
)

const (
	// defaultMaxTokens is the output limit of a review request.
	defaultMaxTokens = 8192
	// maxSplitDepth is how many times a part whose response was cut off can
	// be halved before it is retried with a larger limit instead.
	maxSplitDepth = 4
)

// part is one review request's input: a chunk, the whole diff, or half of
// either after a response was cut off.
type part struct {
	diff  string
	files []string
	note  string // what the other parts hold, appended to the prompt
}

// partResult is what reviewing a part produced.
type partResult struct {
	findings []Finding
	fallback bool  // a fallback provider answered (any of its requests)
	calls    int   // model calls, repairs and halves included
	llmMs    int64 // time spent waiting on the model
	splits   int   // times a part was halved after a cut-off response
	usage    usageLedger
}

func (r *partResult) add(o partResult) {
	r.usage.merge(o.usage)
	r.findings = append(r.findings, o.findings...)
	r.fallback = r.fallback || o.fallback
	r.calls += o.calls
	r.llmMs += o.llmMs
	r.splits += o.splits
}

// partReviewer reviews parts with one provider, prompt builder and limiter.
type partReviewer struct {
	provider providers.Reviewer
	cfg      config.Config
	rules    *Rules
	builder  PromptBuilder
	limiter  *ratelimit.Limiter // nil: no rate limit
}

// review reviews p. A response cut off at its output limit is never parsed:
// the part is halved (by file, or by hunk for a single file) and each half
// reviewed, since a smaller input needs a shorter answer. A part that cannot
// be halved is asked once more with twice the limit; cut off again, it is an
// error, which makes the part a coverage skip rather than a partial list.
func (r *partReviewer) review(ctx context.Context, p part, depth int) (partResult, error) {
	res, err := r.ask(ctx, p, defaultMaxTokens)
	if !providers.IsTruncated(err) {
		return res, err
	}
	if depth < maxSplitDepth {
		if a, b, ok := splitDiff(p.diff); ok {
			res.splits++
			fa, fb := filesOf(a), filesOf(b)
			for _, half := range []part{
				{diff: a, files: fa, note: p.note + splitNote(fb, fa)},
				{diff: b, files: fb, note: p.note + splitNote(fa, fb)},
			} {
				got, err := r.review(ctx, half, depth+1)
				res.add(got)
				if err != nil {
					return res, err
				}
			}
			return res, nil
		}
	}
	again, err := r.ask(ctx, p, 2*defaultMaxTokens)
	res.add(again)
	if providers.IsTruncated(err) {
		return res, fmt.Errorf("response cut off even at %d output tokens, and the part cannot be split further: %w", 2*defaultMaxTokens, err)
	}
	return res, err
}

// ask sends one review request for p, with one repair pass when the answer
// is not valid JSON.
func (r *partReviewer) ask(ctx context.Context, p part, maxTokens int) (partResult, error) {
	var res partResult
	if r.limiter != nil {
		if err := r.limiter.Wait(ctx); err != nil {
			return res, fmt.Errorf("rate limiter: %w", err)
		}
	}
	sysPr, userPr := r.builder(p.diff, p.files, r.cfg, r.rules)
	userPr += p.note

	start := time.Now()
	resp, err := r.provider.Review(ctx, providers.ReviewRequest{
		SystemPrompt: sysPr,
		UserPrompt:   userPr,
		MaxTokens:    maxTokens,
		Output:       findingsOutput,
	})
	res.llmMs += time.Since(start).Milliseconds()
	res.calls += providers.CallsOf(resp)
	res.usage.add(resp.Provider, resp.Model, resp.Usage) // a cut-off answer still cost tokens
	if err != nil {
		return res, err
	}

	findings, err := parseReviewedFindings(resp.Content, p.diff)
	if err != nil {
		repairPrompt := fmt.Sprintf(
			"Your previous response was not valid JSON. The error was: %s\n\nPlease fix it and respond with ONLY a valid JSON array of findings.\n\nYour previous response was:\n%s",
			err.Error(), resp.Content,
		)
		start := time.Now()
		resp2, err2 := r.provider.Review(ctx, providers.ReviewRequest{
			SystemPrompt: sysPr,
			UserPrompt:   repairPrompt,
			MaxTokens:    maxTokens,
			Output:       findingsOutput,
		})
		res.llmMs += time.Since(start).Milliseconds()
		res.calls += providers.CallsOf(resp2)
		res.usage.add(resp2.Provider, resp2.Model, resp2.Usage)
		if err2 != nil {
			return res, fmt.Errorf("repair: %w", err2)
		}
		if findings, err = parseReviewedFindings(resp2.Content, p.diff); err != nil {
			return res, fmt.Errorf("validation after repair: %w", err)
		}
		resp = resp2
	}
	res.findings = stampProvenance(findings, resp.Provider, resp.Model)
	res.fallback = resp.Fallback
	return res, nil
}

// splitDiff halves a diff for a shorter answer: between file sections, at
// the byte midpoint, when it has two or more; else between the hunks of its
// one file, each half keeping the file's header. ok is false for a single
// hunk.
func splitDiff(diff string) (a, b string, ok bool) {
	sections := diffutil.SplitSections(diff)
	if len(sections) >= 2 {
		half, size := 0, 0
		for i, s := range sections {
			if size+len(s) > len(diff)/2 && i > 0 {
				half = i
				break
			}
			size += len(s)
			half = i + 1
		}
		half = min(max(half, 1), len(sections)-1)
		return strings.Join(sections[:half], ""), strings.Join(sections[half:], ""), true
	}
	if len(sections) != 1 {
		return "", "", false
	}
	sec := sections[0]
	meta := diffutil.SectionMeta(sec)
	body := sec[len(meta):] // "\n@@ -a +b @@ ...", one hunk after another
	var starts []int
	for i := 0; ; {
		j := strings.Index(body[i:], "\n@@ -")
		if j < 0 {
			break
		}
		starts = append(starts, i+j)
		i += j + 1
	}
	if len(starts) < 2 {
		return "", "", false
	}
	cut := starts[len(starts)/2]
	return meta + body[:cut] + "\n", meta + body[cut:], true
}

// filesOf lists the files a diff's sections are for.
func filesOf(diff string) []string {
	var files []string
	for _, s := range diffutil.SplitSections(diff) {
		if p := diffutil.PathFromSection(s); p != "" && (len(files) == 0 || files[len(files)-1] != p) {
			files = append(files, p)
		}
	}
	return files
}

// splitNote tells a half of a split part what the other half holds, so the
// model does not report code as missing because it sits in the other half.
func splitNote(other, mine []string) string {
	var b strings.Builder
	b.WriteString("\n\nThis input was split in two because the answer for all of it was too long. ")
	b.WriteString("The other half holds the code below, reviewed separately; do not report something as missing only because it is not shown here.\n\n")
	for _, f := range other {
		if slices.Contains(mine, f) {
			fmt.Fprintf(&b, "- other hunks of %s\n", f)
		} else {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	return b.String()
}
