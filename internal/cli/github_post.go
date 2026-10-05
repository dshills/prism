package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dshills/prism/internal/github"
	"github.com/dshills/prism/internal/review"
	"github.com/spf13/cobra"
)

var (
	flagPostPR     int
	flagPostReport string
	flagPostIDs    string
	flagPostSkip   string
)

var githubPostCmd = &cobra.Command{
	Use:   "post-comments",
	Short: "Post findings from an existing report as a pull request review",
	Long: `Post the findings of a report prism already wrote (a JSON report or a SARIF
log) as a GitHub pull request review, without reviewing again. An agent can
review with "prism github <pr> --dry-run --format json --out prism.json",
check the findings, and post only the ones it confirmed:

  prism github post-comments --pr 42 --report prism.json --ids 1a2b3c4d5e6f7a8b,...

Suppressed findings are never posted. A finding is posted inline when its
line is in the pull request's current diff, and in the review's summary
otherwise.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagPostPR <= 0 || flagPostReport == "" {
			fmt.Fprintln(os.Stderr, "Error: --pr and --report are required")
			exitCode = ExitUsageError
			return nil
		}
		data, err := os.ReadFile(flagPostReport)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		findings, err := review.ReportFindings(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		findings, err = selectFindings(findings, splitComma(flagPostIDs), splitComma(flagPostSkip))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitUsageError
			return nil
		}
		if len(findings) == 0 {
			fmt.Fprintln(os.Stderr, "No findings to post.")
			return nil
		}

		owner, repo, err := resolveGitHubRepo()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\nUse --owner and --repo flags to specify manually.\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		ghClient, err := github.NewClient()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitAuthError
			return nil
		}

		// Inline comments only on lines the pull request's diff shows, which
		// may have moved on since the report was written: GitHub refuses a
		// whole review over one comment elsewhere.
		diff, err := ghClient.GetPRDiff(ctx, owner, repo, flagPostPR)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not fetch the diff, posting every finding in the summary: %v\n", err)
		}
		ghReview := github.BuildGitHubReviewForDiff(findings, diff)

		if flagGHDryRun {
			out, _ := json.MarshalIndent(ghReview, "", "  ")
			_, _ = fmt.Fprintln(os.Stdout, string(out))
			fmt.Fprintf(os.Stderr, "Dry run: %d findings (%d inline), not posting to GitHub.\n", len(findings), len(ghReview.Comments))
			return nil
		}
		fmt.Fprintf(os.Stderr, "Posting review to %s/%s#%d (%d findings, %d inline)...\n", owner, repo, flagPostPR, len(findings), len(ghReview.Comments))
		if err := ghClient.PostReview(ctx, owner, repo, flagPostPR, ghReview); err != nil {
			fmt.Fprintf(os.Stderr, "Error posting review: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		fmt.Fprintf(os.Stderr, "Review posted to PR #%d.\n", flagPostPR)
		return nil
	},
}

// selectFindings keeps the findings named in only (all when it is empty)
// and drops those in skip. An ID in either list that the report does not
// have is an error: a mistyped ID must not post or hide the wrong finding.
func selectFindings(findings []review.Finding, only, skip []string) ([]review.Finding, error) {
	have := make(map[string]bool, len(findings))
	for _, f := range findings {
		have[f.ID] = true
	}
	for _, id := range append(append([]string{}, only...), skip...) {
		if !have[id] {
			return nil, fmt.Errorf("finding %s is not in the report", id)
		}
	}
	keep, drop := map[string]bool{}, map[string]bool{}
	for _, id := range only {
		keep[id] = true
	}
	for _, id := range skip {
		drop[id] = true
	}
	var out []review.Finding
	for _, f := range findings {
		if (len(only) == 0 || keep[f.ID]) && !drop[f.ID] {
			out = append(out, f)
		}
	}
	return out, nil
}

// resolveGitHubRepo is --owner and --repo, each detected from the git
// remote when not given.
func resolveGitHubRepo() (owner, repo string, err error) {
	owner, repo = flagGHOwner, flagGHRepo
	if owner != "" && repo != "" {
		return owner, repo, nil
	}
	detected, detectedRepo, err := github.DetectRepo()
	if err != nil {
		return "", "", err
	}
	if owner == "" {
		owner = detected
	}
	if repo == "" {
		repo = detectedRepo
	}
	return owner, repo, nil
}

func init() {
	githubPostCmd.Flags().IntVar(&flagPostPR, "pr", 0, "Pull request number")
	githubPostCmd.Flags().StringVar(&flagPostReport, "report", "", "Report to post: a prism JSON report or a SARIF log prism wrote")
	githubPostCmd.Flags().StringVar(&flagPostIDs, "ids", "", "Post only these finding IDs (comma-separated)")
	githubPostCmd.Flags().StringVar(&flagPostSkip, "skip-ids", "", "Leave these finding IDs out (comma-separated)")
	githubPostCmd.Flags().StringVar(&flagGHOwner, "owner", "", "GitHub repository owner (auto-detected if omitted)")
	githubPostCmd.Flags().StringVar(&flagGHRepo, "repo", "", "GitHub repository name (auto-detected if omitted)")
	githubPostCmd.Flags().BoolVar(&flagGHDryRun, "dry-run", false, "Print the review that would be posted instead of posting it")
	githubCmd.AddCommand(githubPostCmd)
}
