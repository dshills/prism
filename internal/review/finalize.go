package review

import (
	"context"
	"os"
	"path/filepath"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
)

// FinalizeFindings is the last step before every report, whatever reviewed
// the diff: verify findings against the code (FR-5/FR-6), leave out the ones
// accepted by an inline prism:ignore or the repository's baseline, then apply
// maxFindings. Discarded and suppressed findings are returned for the report
// to list; neither uses up the limit or counts toward the exit code.
//
// It runs after the cache, which holds findings from before all of this, so
// accepting a finding takes effect on the next run even when it is replayed.
// An unreadable baseline is an error, not an empty one.
//
// With confirmBlocking set, the findings left that would block the run are
// then checked by a second model, and the ones it refutes discarded; its
// calls and tokens are added to cov (which may be nil).
func FinalizeFindings(ctx context.Context, findings []Finding, diff gitctx.DiffResult, cfg config.Config, cov *Coverage) ([]Finding, []Discard, []Suppression, error) {
	findings = dropBelow(findings, cfg.MinSeverity)
	findings, discarded := VerifyFindings(ctx, findings, diff, cfg)

	baseline, err := LoadBaseline(BaselinePath(cfg, diff.Repo.Root))
	if err != nil {
		return nil, nil, nil, err
	}
	// Directives are read from the code itself, not the redacted text the
	// model saw: redaction rewrites string contents, which would stop the diff
	// matching the file it is checked against.
	findings, inline := applyIgnores(findings, diff.Diff, reviewedFiles(ctx, diff))
	findings, accepted := applyBaseline(findings, baseline)
	suppressed := append(append([]Suppression{}, inline...), accepted...)

	if cov == nil {
		cov = &Coverage{}
	}
	findings, refuted, err := confirmBlocking(ctx, findings, diff, cfg, cov)
	if err != nil {
		return nil, nil, nil, err
	}
	discarded = append(discarded, refuted...)

	if cfg.MaxFindings > 0 && len(findings) > cfg.MaxFindings {
		findings = findings[:cfg.MaxFindings]
	}
	return findings, discarded, suppressed, nil
}

// dropBelow leaves out findings below minSeverity. The model is told not to
// write them, so this only enforces what it was asked; they are not listed
// anywhere, since setting a floor says they are not wanted.
func dropBelow(findings []Finding, minSeverity string) []Finding {
	floor := SeverityRank(Severity(minSeverity))
	if floor <= SeverityRank(SeverityLow) {
		return findings
	}
	kept := findings[:0:0]
	for _, f := range findings {
		if SeverityRank(f.Severity) >= floor {
			kept = append(kept, f)
		}
	}
	return kept
}

// reviewedFiles reads a file's whole content as it was reviewed: the working
// tree for unstaged changes, the index for staged ones, the tip revision for
// a commit or range. It is nil where that cannot be had (a GitHub PR review,
// a snippet, no repository), and a codebase review already has whole files.
func reviewedFiles(ctx context.Context, diff gitctx.DiffResult) fileSource {
	root := diff.Repo.Root
	if root == "" {
		return nil
	}
	var rev string
	switch diff.Mode {
	case "unstaged":
		return func(path string) (string, bool) {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
			return string(data), err == nil
		}
	case "staged":
		rev = "" // ":path" is the index
	case "commit":
		rev = diff.Range
	case "range":
		rev = rangeTip(diff.Range)
	default:
		return nil
	}
	return func(path string) (string, bool) {
		out, err := execGit{}.Run(ctx, root, "show", rev+":"+path)
		return out, err == nil
	}
}
