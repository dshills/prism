package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/review"
	"github.com/spf13/cobra"
)

var (
	flagSince   string
	flagOnlyNew bool
	// priorReview is the --since review, loaded and checked before the
	// review runs so a bad file fails before anything is spent on a model.
	priorReview *review.PriorReview
)

// sinceLast is the --since value for this repository's last review.
const sinceLast = "last"

// addDeltaFlags adds --since and --only-new to a review command. Both are
// checked, and the --since review loaded, before the command spends
// anything on a model.
func addDeltaFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&flagSince, "since", "", `Compare with an earlier review: a prism JSON report, a SARIF log, or "last" for this repository's last review`)
	cmd.Flags().BoolVar(&flagOnlyNew, "only-new", false, "Report only findings that are not in the --since review (they alone decide the exit code)")
	prev := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if flagOnlyNew && flagSince == "" {
			return errors.New("--only-new needs --since")
		}
		priorReview = nil
		if flagSince != "" {
			cfg, err := config.Load(buildOverrides())
			if err != nil {
				return err
			}
			ctx := c.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if priorReview, err = loadPriorReview(ctx, cfg, flagSince); err != nil {
				return err
			}
		}
		if prev != nil {
			return prev(c, args)
		}
		return nil
	}
}

// withDelta compares report with the --since review, if one was asked for,
// and returns the report to write. reviewed is the files this review
// covered (nil: not known). The report given is left as it is: that full
// report, not a --only-new view of it, is the one to remember as this
// repository's last review, or the next run would see persisting findings
// as new.
func withDelta(ctx context.Context, report *review.Report, cfg config.Config, reviewed []string) (*review.Report, error) {
	if flagSince == "" {
		return report, nil
	}
	prior := priorReview // loaded by PreRunE, before the review
	if prior == nil {
		var err error
		if prior, err = loadPriorReview(ctx, cfg, flagSince); err != nil {
			return nil, err
		}
	}
	return review.ApplyDelta(report, prior, flagSince, reviewed, flagOnlyNew), nil
}

// loadPriorReview reads the earlier review --since names. "last" with no
// last review yet (the first run of a fix loop) is an empty one, so every
// finding is new.
func loadPriorReview(ctx context.Context, cfg config.Config, since string) (*review.PriorReview, error) {
	path := since
	if since == sinceLast {
		root := repoRoot(ctx)
		if root == "" {
			return nil, errors.New(`--since last needs a git repository`)
		}
		p, err := lastReportPath(cfg, root)
		if err != nil {
			return nil, err
		}
		path = p
	}
	data, err := os.ReadFile(path)
	if since == sinceLast && errors.Is(err, fs.ErrNotExist) {
		return &review.PriorReview{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("--since: %w", err)
	}
	return review.ReadPriorReview(data)
}
