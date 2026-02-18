package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/output"
	"github.com/dshills/prism/internal/providers"
	"github.com/dshills/prism/internal/review"
	"github.com/spf13/cobra"
)

// Shared review flags
var (
	flagPaths        string
	flagExclude      string
	flagContextLines int
	flagMaxDiffBytes int
	flagProvider     string
	flagModel        string
	flagCompare      string
	flagFormat       string
	flagOut          string
	flagFailOn       string
	flagMaxFindings  int
	flagRules        string
	flagNoRedact     bool
)

func addReviewFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&flagPaths, "paths", "", "Include file path globs (comma-separated)")
	cmd.Flags().StringVar(&flagExclude, "exclude", "", "Exclude file path globs (comma-separated)")
	cmd.Flags().IntVar(&flagContextLines, "context-lines", 0, "Number of context lines in diff")
	cmd.Flags().IntVar(&flagMaxDiffBytes, "max-diff-bytes", 0, "Maximum diff size in bytes")
	cmd.Flags().StringVar(&flagProvider, "provider", "", "LLM provider (anthropic, openai, gemini)")
	cmd.Flags().StringVar(&flagModel, "model", "", "Model name")
	cmd.Flags().StringVar(&flagCompare, "compare", "", "Compare mode: comma-separated provider:model pairs")
	cmd.Flags().StringVar(&flagFormat, "format", "", "Output format (text, json, markdown, sarif)")
	cmd.Flags().StringVar(&flagOut, "out", "", "Output file path (default: stdout)")
	cmd.Flags().StringVar(&flagFailOn, "fail-on", "", "Fail on severity threshold (none, low, medium, high)")
	cmd.Flags().IntVar(&flagMaxFindings, "max-findings", 0, "Maximum number of findings")
	cmd.Flags().StringVar(&flagRules, "rules", "", "Rules file path")
	cmd.Flags().BoolVar(&flagNoRedact, "no-redact", false, "Disable secret redaction (use with caution)")
}

func buildOverrides() map[string]string {
	m := make(map[string]string)
	if flagProvider != "" {
		m["provider"] = flagProvider
	}
	if flagModel != "" {
		m["model"] = flagModel
	}
	if flagFormat != "" {
		m["format"] = flagFormat
	}
	if flagFailOn != "" {
		m["failOn"] = flagFailOn
	}
	if flagMaxFindings > 0 {
		m["maxFindings"] = fmt.Sprintf("%d", flagMaxFindings)
	}
	if flagContextLines > 0 {
		m["contextLines"] = fmt.Sprintf("%d", flagContextLines)
	}
	if flagMaxDiffBytes > 0 {
		m["maxDiffBytes"] = fmt.Sprintf("%d", flagMaxDiffBytes)
	}
	if flagRules != "" {
		m["rulesFile"] = flagRules
	}
	if flagCompare != "" {
		m["compare"] = flagCompare
	}
	return m
}

func buildDiffOpts(cfg config.Config) gitctx.DiffOptions {
	opts := gitctx.DiffOptions{
		ContextLines: cfg.ContextLines,
		MaxDiffBytes: cfg.MaxDiffBytes,
		Include:      cfg.Include,
		Exclude:      cfg.Exclude,
	}
	if flagPaths != "" {
		opts.Include = splitComma(flagPaths)
	}
	if flagExclude != "" {
		opts.Exclude = append(opts.Exclude, splitComma(flagExclude)...)
	}
	return opts
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func runReview(diff gitctx.DiffResult, cfg config.Config) {
	if flagNoRedact {
		cfg.Privacy.RedactSecrets = false
		fmt.Fprintln(os.Stderr, "WARNING: secret redaction is disabled")
	}

	// Determine compare models from flag or config
	var compareModels []string
	if flagCompare != "" {
		compareModels = splitComma(flagCompare)
	} else if len(cfg.Compare) > 0 {
		compareModels = cfg.Compare
	}

	ctx := context.Background()

	var report *review.Report
	var err error

	if len(compareModels) >= 2 {
		report, err = runCompareMode(ctx, diff, cfg, compareModels, nil)
	} else {
		report, err = review.Run(ctx, diff, cfg)
	}

	if err != nil {
		if providers.IsAuthError(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitAuthError
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCode = ExitRuntimeError
		return
	}

	if err := output.WriteReport(report, cfg.Format, flagOut); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		exitCode = ExitRuntimeError
		return
	}

	// Check fail-on threshold
	if cfg.FailOn != "none" && cfg.FailOn != "" {
		for _, f := range report.Findings {
			if review.MeetsThreshold(f.Severity, cfg.FailOn) {
				exitCode = ExitFindings
				return
			}
		}
	}
}

func runCompareMode(ctx context.Context, diff gitctx.DiffResult, cfg config.Config, models []string, builder review.PromptBuilder) (*review.Report, error) {
	startTime := time.Now()

	rules, err := review.LoadRules(cfg.RulesFile)
	if err != nil {
		return nil, fmt.Errorf("loading rules: %w", err)
	}

	cr, err := review.RunCompareWithOptions(ctx, diff.Diff, diff.Files, models, cfg, rules, review.CompareOptions{
		Builder: builder,
	})
	if err != nil {
		return nil, err
	}

	findings := cr.All
	if cfg.MaxFindings > 0 && len(findings) > cfg.MaxFindings {
		findings = findings[:cfg.MaxFindings]
	}

	report := review.BuildReport(diff, findings, cr.LLMMs, time.Since(startTime).Milliseconds())

	// Print compare summary to stderr
	fmt.Fprintf(os.Stderr, "Compare mode: %d models, %d consensus findings, %d total\n",
		len(models), len(cr.Consensus), len(cr.All))
	for label, unique := range cr.Unique {
		if len(unique) > 0 {
			fmt.Fprintf(os.Stderr, "  %s: %d unique findings\n", label, len(unique))
		}
	}

	return report, nil
}

var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Review code changes",
	Long:  "Review code changes using an LLM provider. Use subcommands to specify what to review.",
}

var reviewUnstagedCmd = &cobra.Command{
	Use:   "unstaged",
	Short: "Review unstaged changes (working tree vs index)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(buildOverrides())
		if err != nil {
			return err
		}
		diff, err := gitctx.Unstaged(buildDiffOpts(cfg))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		runReview(diff, cfg)
		return nil
	},
}

var reviewStagedCmd = &cobra.Command{
	Use:   "staged",
	Short: "Review staged changes (index vs HEAD)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(buildOverrides())
		if err != nil {
			return err
		}
		diff, err := gitctx.Staged(buildDiffOpts(cfg))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		runReview(diff, cfg)
		return nil
	},
}

var (
	flagParent string
)

var reviewCommitCmd = &cobra.Command{
	Use:   "commit <sha>",
	Short: "Review a specific commit",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(buildOverrides())
		if err != nil {
			return err
		}
		diff, err := gitctx.Commit(args[0], flagParent, buildDiffOpts(cfg))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		runReview(diff, cfg)
		return nil
	},
}

var (
	flagMergeBase bool
)

var reviewRangeCmd = &cobra.Command{
	Use:   "range <revRange>",
	Short: "Review a revision range (e.g., origin/main..HEAD)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(buildOverrides())
		if err != nil {
			return err
		}
		diff, err := gitctx.Range(args[0], flagMergeBase, buildDiffOpts(cfg))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		runReview(diff, cfg)
		return nil
	},
}

var (
	flagSnippetPath        string
	flagSnippetLang        string
	flagSnippetBase        string
	flagMaxFindingsPerFile int
)

var reviewSnippetCmd = &cobra.Command{
	Use:   "snippet",
	Short: "Review code from stdin",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(buildOverrides())
		if err != nil {
			return err
		}

		content, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		var base string
		if flagSnippetBase != "" {
			baseData, err := os.ReadFile(flagSnippetBase)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading base file: %v\n", err)
				exitCode = ExitRuntimeError
				return nil
			}
			base = string(baseData)
		}

		path := flagSnippetPath
		if path == "" {
			path = "stdin"
		}

		diff, err := gitctx.Snippet(string(content), path, flagSnippetLang, base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		runReview(diff, cfg)
		return nil
	},
}

var reviewCodebaseCmd = &cobra.Command{
	Use:   "codebase",
	Short: "Review all tracked files in the repository",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(buildOverrides())
		if err != nil {
			return err
		}
		diff, err := gitctx.Codebase(buildDiffOpts(cfg))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}
		runCodebaseReview(diff, cfg)
		return nil
	},
}

func runCodebaseReview(diff gitctx.DiffResult, cfg config.Config) {
	if flagNoRedact {
		cfg.Privacy.RedactSecrets = false
		fmt.Fprintln(os.Stderr, "WARNING: secret redaction is disabled")
	}

	var compareModels []string
	if flagCompare != "" {
		compareModels = splitComma(flagCompare)
	} else if len(cfg.Compare) > 0 {
		compareModels = cfg.Compare
	}

	ctx := context.Background()

	var report *review.Report
	var err error

	if len(compareModels) >= 2 {
		maxPerFile := flagMaxFindingsPerFile
		codebaseBuilder := func(chunkDiff string, files []string, c config.Config, r *review.Rules) (string, string) {
			return review.CodebaseSystemPrompt(), review.BuildCodebaseUserPrompt(chunkDiff, files, c.MaxFindings, maxPerFile, c.FailOn, r)
		}
		report, err = runCompareMode(ctx, diff, cfg, compareModels, codebaseBuilder)
	} else {
		cbCfg := review.CodebaseConfig{
			Config:             cfg,
			MaxFindingsPerFile: flagMaxFindingsPerFile,
		}
		report, err = review.RunCodebase(ctx, diff, cbCfg)
	}

	if err != nil {
		if providers.IsAuthError(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitAuthError
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCode = ExitRuntimeError
		return
	}

	if err := output.WriteReport(report, cfg.Format, flagOut); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		exitCode = ExitRuntimeError
		return
	}

	if cfg.FailOn != "none" && cfg.FailOn != "" {
		for _, f := range report.Findings {
			if review.MeetsThreshold(f.Severity, cfg.FailOn) {
				exitCode = ExitFindings
				return
			}
		}
	}
}

func init() {
	// Add review subcommands
	reviewCmd.AddCommand(reviewUnstagedCmd)
	reviewCmd.AddCommand(reviewStagedCmd)
	reviewCmd.AddCommand(reviewCommitCmd)
	reviewCmd.AddCommand(reviewRangeCmd)
	reviewCmd.AddCommand(reviewSnippetCmd)
	reviewCmd.AddCommand(reviewCodebaseCmd)

	// Add shared flags to all review subcommands
	for _, cmd := range []*cobra.Command{
		reviewUnstagedCmd,
		reviewStagedCmd,
		reviewCommitCmd,
		reviewRangeCmd,
		reviewSnippetCmd,
		reviewCodebaseCmd,
	} {
		addReviewFlags(cmd)
	}

	// Codebase-specific flags
	reviewCodebaseCmd.Flags().IntVar(&flagMaxFindingsPerFile, "max-findings-per-file", 10, "Maximum findings per file")

	// Commit-specific flags
	reviewCommitCmd.Flags().StringVar(&flagParent, "parent", "", "Override parent SHA (for merge commits)")

	// Range-specific flags
	reviewRangeCmd.Flags().BoolVar(&flagMergeBase, "merge-base", true, "Use merge base for branch comparisons")

	// Snippet-specific flags
	reviewSnippetCmd.Flags().StringVar(&flagSnippetPath, "path", "", "File path (for language detection and messages)")
	reviewSnippetCmd.Flags().StringVar(&flagSnippetLang, "lang", "", "Language hint")
	reviewSnippetCmd.Flags().StringVar(&flagSnippetBase, "base", "", "Base file to diff against")
}
