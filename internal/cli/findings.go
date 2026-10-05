package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/review"
	"github.com/spf13/cobra"
)

var flagFindingsReason string

var findingsCmd = &cobra.Command{
	Use:   "findings",
	Short: "Record what was done with findings, to calibrate later reviews",
	Long: `After deciding what to do with a finding, record the verdict: confirm it when
it was a real problem (and you acted on it), dismiss it when it was a false
positive. Verdicts go to a feedback log next to the review cache. Once a
category has enough of them, each finding in later reviews carries a
calibration: how often findings like it were confirmed, for its model when
that has enough verdicts, else for every model.

This is not the baseline: "prism baseline add" stops a finding being
reported, while a verdict only records whether it was right.`,
}

var findingsConfirmCmd = &cobra.Command{
	Use:     "confirm <finding-id>...",
	Aliases: []string{"accept"},
	Short:   "Record findings as real problems",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return recordVerdicts(cmd, args, review.VerdictConfirmed)
	},
}

var findingsDismissCmd = &cobra.Command{
	Use:   "dismiss <finding-id>...",
	Short: "Record findings as false positives",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return recordVerdicts(cmd, args, review.VerdictDismissed)
	},
}

var findingsStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show how often findings were confirmed, by category",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(nil)
		if err != nil {
			return err
		}
		path, err := review.FeedbackPath(cfg)
		if err != nil {
			return err
		}
		entries, err := review.LoadFeedback(path)
		if err != nil {
			return fmt.Errorf("reading feedback log: %w", err)
		}
		if len(entries) == 0 {
			_, _ = fmt.Fprintln(os.Stdout, "No verdicts recorded yet.")
			return nil
		}
		for _, line := range review.NewFeedbackStats(entries).CategoryLines() {
			_, _ = fmt.Fprintln(os.Stdout, line)
		}
		return nil
	},
}

// recordVerdicts looks each ID up in the repository's last review, for its
// category and model, and appends the verdicts to the feedback log. Every
// ID is checked before anything is written.
func recordVerdicts(cmd *cobra.Command, ids []string, verdict string) error {
	cfg, err := config.Load(nil)
	if err != nil {
		return err
	}
	last := loadLastReport(cmd.Context())
	repo := repoRoot(cmd.Context())
	now := time.Now().UTC().Format(time.RFC3339)
	entries := make([]review.FeedbackEntry, 0, len(ids))
	for _, id := range ids {
		f, ok := findInReport(last, id)
		if !ok {
			return fmt.Errorf("finding %s is not in this repository's last review; a verdict needs the finding's category and model", id)
		}
		entries = append(entries, review.FeedbackFor(f, verdict, flagFindingsReason, repo, now))
	}
	path, err := review.FeedbackPath(cfg)
	if err != nil {
		return err
	}
	if err := review.AppendFeedback(path, entries); err != nil {
		return fmt.Errorf("writing feedback log: %w", err)
	}
	_, _ = fmt.Fprintf(os.Stdout, "Recorded %d finding(s) as %s\n", len(entries), verdict)
	return nil
}

func init() {
	for _, c := range []*cobra.Command{findingsConfirmCmd, findingsDismissCmd} {
		c.Flags().StringVar(&flagFindingsReason, "reason", "", "Why (kept in the feedback log)")
	}
	findingsCmd.AddCommand(findingsConfirmCmd, findingsDismissCmd, findingsStatsCmd)
}
