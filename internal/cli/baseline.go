package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dshills/prism/internal/cache"
	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/review"
	"github.com/spf13/cobra"
)

var (
	flagBaselineReason string
	flagBaselineForce  bool
	flagBaselineJSON   bool
)

var baselineCmd = &cobra.Command{
	Use:   "baseline",
	Short: "Manage accepted findings (.prism-baseline.json)",
	Long: `The baseline lists findings that were reviewed and accepted. Every review
leaves them out of its findings and exit code, and lists them as suppressed.
The file lives at the repository root and is meant to be committed, so every
agent and CI run on the repository respects it.

Findings are matched by ID, the fingerprint of the code they are about. An ID
covers one quoted piece of code, in one declaration, in one category.`,
}

var baselineAddCmd = &cobra.Command{
	Use:   "add <finding-id>...",
	Short: "Accept findings so reviews stop reporting them",
	Long: `Accept findings by ID. Each ID is looked up in this repository's last review
so the baseline records its path, category and title. Use --force to add an
ID that is not in the last review.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, b, err := openBaseline(cmd.Context())
		if err != nil {
			return err
		}
		last := loadLastReport(cmd.Context())
		today := time.Now().Format("2006-01-02")

		// Check every ID before changing anything.
		entries := make([]review.BaselineEntry, 0, len(args))
		for _, id := range args {
			if f, ok := findInReport(last, id); ok {
				entries = append(entries, review.EntryFor(f, flagBaselineReason, today))
				continue
			}
			if !flagBaselineForce {
				return fmt.Errorf("finding %s is not in this repository's last review; run a review first, or pass --force to add the ID alone", id)
			}
			entries = append(entries, review.BaselineEntry{ID: id, Reason: flagBaselineReason, Added: today})
		}

		added := 0
		for _, e := range entries {
			if b.Add(e) {
				added++
			}
		}
		if err := b.Save(path); err != nil {
			return fmt.Errorf("writing baseline: %w", err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "Added %d finding(s) to %s", added, path)
		if updated := len(entries) - added; updated > 0 {
			_, _ = fmt.Fprintf(os.Stdout, " (%d already there, updated)", updated)
		}
		_, _ = fmt.Fprintln(os.Stdout)
		return nil
	},
}

var baselineRemoveCmd = &cobra.Command{
	Use:   "remove <finding-id>...",
	Short: "Stop accepting findings, so reviews report them again",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, b, err := openBaseline(cmd.Context())
		if err != nil {
			return err
		}
		removed := 0
		for _, id := range args {
			if b.Remove(id) {
				removed++
			} else {
				_, _ = fmt.Fprintf(os.Stderr, "Not in the baseline: %s\n", id)
			}
		}
		if removed == 0 {
			return fmt.Errorf("none of the IDs are in %s", path)
		}
		if err := b.Save(path); err != nil {
			return fmt.Errorf("writing baseline: %w", err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "Removed %d finding(s) from %s\n", removed, path)
		return nil
	},
}

var baselineShowCmd = &cobra.Command{
	Use:   "show",
	Short: "List the accepted findings",
	RunE: func(cmd *cobra.Command, args []string) error {
		path, b, err := openBaseline(cmd.Context())
		if err != nil {
			return err
		}
		if flagBaselineJSON {
			data, err := json.MarshalIndent(b, "", "  ")
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(os.Stdout, string(data))
			return nil
		}
		if len(b.Findings) == 0 {
			_, _ = fmt.Fprintf(os.Stdout, "No accepted findings in %s\n", path)
			return nil
		}
		_, _ = fmt.Fprintf(os.Stdout, "%d accepted finding(s) in %s:\n", len(b.Findings), path)
		for _, e := range b.Findings {
			_, _ = fmt.Fprintf(os.Stdout, "  %s  %s  [%s]  %s", e.ID, orDash(e.Path), orDash(e.Category), orDash(e.Title))
			if e.Reason != "" {
				_, _ = fmt.Fprintf(os.Stdout, " — %s", e.Reason)
			}
			if e.Added != "" {
				_, _ = fmt.Fprintf(os.Stdout, " (%s)", e.Added)
			}
			_, _ = fmt.Fprintln(os.Stdout)
		}
		return nil
	},
}

func init() {
	baselineCmd.PersistentFlags().StringVar(&flagBaseline, "baseline", "", "Baseline file (default .prism-baseline.json at the repo root)")
	baselineAddCmd.Flags().StringVar(&flagBaselineReason, "reason", "", "Why the findings are accepted, recorded in the baseline")
	baselineAddCmd.Flags().BoolVar(&flagBaselineForce, "force", false, "Add IDs that are not in the last review")
	baselineShowCmd.Flags().BoolVar(&flagBaselineJSON, "json", false, "Print the baseline as JSON")
	baselineCmd.AddCommand(baselineAddCmd, baselineRemoveCmd, baselineShowCmd)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// openBaseline loads the configured baseline of the repository in the
// working directory.
func openBaseline(ctx context.Context) (string, *review.Baseline, error) {
	cfg, err := baselineConfig()
	if err != nil {
		return "", nil, err
	}
	path := review.BaselinePath(cfg, repoRoot(ctx))
	if path == "" {
		return "", nil, fmt.Errorf("the baseline is turned off (baselineFile is \"none\")")
	}
	b, err := review.LoadBaseline(path)
	if err != nil {
		return "", nil, err
	}
	return path, b, nil
}

func baselineConfig() (config.Config, error) {
	overrides := map[string]string{}
	if flagBaseline != "" {
		overrides["baselineFile"] = flagBaseline
	}
	return config.Load(overrides)
}

// repoRoot is the root of the repository in the working directory, or ""
// outside one.
func repoRoot(ctx context.Context) string {
	meta, err := gitctx.GetRepoMeta(ctx)
	if err != nil {
		return ""
	}
	return meta.Root
}

// lastReportPath is where the last review of the repository at root is kept,
// so `prism baseline add` can record what an ID was.
func lastReportPath(cfg config.Config, root string) (string, error) {
	dir, err := cache.ResolveDir(cfg.Cache.Dir)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(root))
	return filepath.Join(dir, "reports", fmt.Sprintf("%x.json", h[:8])), nil
}

// rememberReport keeps report as its repository's last review. Only a report
// whose repository is known is kept: a GitHub PR review (no local repository
// in its report) could be of another repository than the working directory,
// and an empty root would make every such review share one entry. It is best
// effort: failing to keep a report only means `baseline add` needs --force.
func rememberReport(report *review.Report, cfg config.Config) {
	if report.Repo.Root == "" {
		return
	}
	path, err := lastReportPath(cfg, report.Repo.Root)
	if err != nil {
		return
	}
	data, err := json.Marshal(report)
	if err != nil {
		return
	}
	_ = writePrivate(path, data)
}

// writePrivate replaces path with data, readable only by the user: a report
// holds findings' evidence, which is unredacted under --no-redact. It writes
// a temporary file (os.CreateTemp makes it 0600) and renames it into place,
// so an existing report never keeps wider permissions or ends half written.
func writePrivate(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".report-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// loadLastReport is the last review of the repository in the working
// directory, or nil when there is none (or no repository).
func loadLastReport(ctx context.Context) *review.Report {
	root := repoRoot(ctx)
	if root == "" {
		return nil
	}
	cfg, err := baselineConfig()
	if err != nil {
		return nil
	}
	path, err := lastReportPath(cfg, root)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r review.Report
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	return &r
}

// findInReport finds the finding with this ID among a report's findings,
// including ones already suppressed.
func findInReport(r *review.Report, id string) (review.Finding, bool) {
	if r == nil {
		return review.Finding{}, false
	}
	for _, f := range r.Findings {
		if f.ID == id {
			return f, true
		}
	}
	for _, s := range r.Suppressed {
		if s.Finding.ID == id {
			return s.Finding, true
		}
	}
	return review.Finding{}, false
}
