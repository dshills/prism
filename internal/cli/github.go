package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/github"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/output"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/review"
	"github.com/spf13/cobra"
)

var (
	flagGHOwner  string
	flagGHRepo   string
	flagGHDryRun bool
)

var githubCmd = &cobra.Command{
	Use:   "github <pr-number>",
	Short: "Review a GitHub pull request",
	Long:  "Fetch a PR diff from GitHub, run review, and optionally post findings as PR review comments.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prNumber, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid PR number %q\n", args[0])
			exitCode = ExitUsageError
			return nil
		}

		cfg, err := config.Load(buildOverrides())
		if err != nil {
			return err
		}

		// Detect owner/repo if not provided
		owner, repo := flagGHOwner, flagGHRepo
		if owner == "" || repo == "" {
			detected, detectedRepo, err := github.DetectRepo()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\nUse --owner and --repo flags to specify manually.\n", err)
				exitCode = ExitRuntimeError
				return nil
			}
			if owner == "" {
				owner = detected
			}
			if repo == "" {
				repo = detectedRepo
			}
		}

		// Create GitHub client
		ghClient, err := github.NewClient()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitAuthError
			return nil
		}

		ctx := context.Background()

		// Fetch PR diff
		fmt.Fprintf(os.Stderr, "Fetching PR #%d from %s/%s...\n", prNumber, owner, repo)
		diff, err := ghClient.GetPRDiff(ctx, owner, repo, prNumber)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		if diff == "" {
			fmt.Fprintln(os.Stdout, "PR has no diff — nothing to review.")
			return nil
		}

		// Fetch PR files
		files, err := ghClient.GetPRFiles(ctx, owner, repo, prNumber)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not fetch file list: %v\n", err)
			files = nil
		}

		// Build DiffResult for the review engine
		diffResult := gitctx.DiffResult{
			Diff:  diff,
			Files: files,
			Mode:  "github-pr",
			Range: fmt.Sprintf("#%d", prNumber),
		}

		// Run review
		report, err := review.Run(ctx, diffResult, cfg)
		if err != nil {
			if providers.IsAuthError(err) {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				exitCode = ExitAuthError
				return nil
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		// Write local output
		if err := output.WriteReport(report, cfg.Format, flagOut); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		// Post review to GitHub (unless dry-run)
		if flagGHDryRun {
			fmt.Fprintf(os.Stderr, "Dry run: %d findings found, not posting to GitHub.\n", len(report.Findings))
		} else {
			diffFileSet := make(map[string]bool, len(files))
			for _, f := range files {
				diffFileSet[f] = true
			}

			ghReview := github.BuildGitHubReview(report.Findings, diffFileSet)
			fmt.Fprintf(os.Stderr, "Posting review (%d inline comments)...\n", len(ghReview.Comments))

			if err := ghClient.PostReview(ctx, owner, repo, prNumber, ghReview); err != nil {
				fmt.Fprintf(os.Stderr, "Error posting review: %v\n", err)
				exitCode = ExitRuntimeError
				return nil
			}

			fmt.Fprintf(os.Stderr, "Review posted to PR #%d.\n", prNumber)
		}

		// Check fail-on threshold
		if cfg.FailOn != "none" && cfg.FailOn != "" {
			for _, f := range report.Findings {
				if review.MeetsThreshold(f.Severity, cfg.FailOn) {
					exitCode = ExitFindings
					return nil
				}
			}
		}

		return nil
	},
}

func init() {
	addReviewFlags(githubCmd)
	githubCmd.Flags().StringVar(&flagGHOwner, "owner", "", "GitHub repository owner (auto-detected if omitted)")
	githubCmd.Flags().StringVar(&flagGHRepo, "repo", "", "GitHub repository name (auto-detected if omitted)")
	githubCmd.Flags().BoolVar(&flagGHDryRun, "dry-run", false, "Run review but don't post to GitHub")
}
