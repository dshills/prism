package gitctx

import (
	"context"
	"strings"

	"github.com/dshills/prism/internal/diffutil"
)

// widenSlack is how much a file's diff may grow, at least, when its hunks
// are widened to their enclosing functions; beyond it, growth is allowed up
// to widenFactor times the plain diff. A file whose widened diff would grow
// more (one change inside a very long function) keeps its plain hunks, so
// one file cannot make the whole review much larger.
const (
	widenSlack  = 4096
	widenFactor = 3
)

// gitDiff runs `git <prefix> <args>` and, with opts.FunctionContext, the same
// with --function-context, then takes each file's widened section when it
// grew within limits. It returns the diff and how many files were widened.
// The widened run is a bonus: if it fails, the plain diff stands.
func gitDiff(ctx context.Context, prefix []string, opts DiffOptions) (string, int, error) {
	args := buildDiffArgs(opts)
	plain, err := gitOutputCtx(ctx, append(append([]string{}, prefix...), args...)...)
	if err != nil || !opts.FunctionContext || plain == "" {
		return plain, 0, err
	}
	wideArgs := append(append(append([]string{}, prefix...), "--function-context"), args...)
	wide, werr := gitOutputCtx(ctx, wideArgs...)
	if werr != nil {
		return plain, 0, nil
	}
	merged, widened := widenSections(plain, wide)
	return merged, widened, nil
}

// widenSections replaces each section of plain with the same file's section
// of wide when its growth is within limits (widenSlack, widenFactor).
// Sections are matched by their "diff --git" line, which the two diffs
// share.
func widenSections(plain, wide string) (string, int) {
	byHeader := map[string]string{}
	for _, sec := range diffutil.SplitSections(wide) {
		first, _, _ := strings.Cut(sec, "\n")
		byHeader[first] = sec
	}
	var b strings.Builder
	widened := 0
	for _, sec := range diffutil.SplitSections(plain) {
		first, _, _ := strings.Cut(sec, "\n")
		w, ok := byHeader[first]
		if ok && w != sec && len(w) <= len(sec)+max(widenSlack, (widenFactor-1)*len(sec)) {
			b.WriteString(w)
			widened++
			continue
		}
		b.WriteString(sec)
	}
	return b.String(), widened
}
