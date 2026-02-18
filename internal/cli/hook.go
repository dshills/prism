package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const (
	hookMarkerStart = "# >>> prism pre-commit hook >>>"
	hookMarkerEnd   = "# <<< prism pre-commit hook <<<"
)

var (
	hookFailOn      string
	hookFormat      string
	hookMaxFindings int
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Manage git pre-commit hook",
}

var hookInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install prism as a git pre-commit hook",
	RunE: func(cmd *cobra.Command, args []string) error {
		hookPath, err := getHookPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		section := generateHookScript(hookFailOn, hookFormat, hookMaxFindings)

		existing, err := os.ReadFile(hookPath)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error reading hook file: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		var content string
		if os.IsNotExist(err) || len(existing) == 0 {
			// No existing hook — create new file
			content = "#!/bin/sh\n" + section
		} else {
			content = replacePrismSection(string(existing), section)
		}

		if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating hooks directory: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		if err := os.WriteFile(hookPath, []byte(content), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing hook file: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		fmt.Fprintf(os.Stdout, "Installed prism pre-commit hook at %s\n", hookPath)
		return nil
	},
}

var hookUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove prism pre-commit hook",
	RunE: func(cmd *cobra.Command, args []string) error {
		hookPath, err := getHookPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		existing, err := os.ReadFile(hookPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(os.Stdout, "No pre-commit hook found.")
				return nil
			}
			fmt.Fprintf(os.Stderr, "Error reading hook file: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		content := removePrismSection(string(existing))

		// If only shebang (and whitespace) remains, delete the file entirely
		trimmed := strings.TrimSpace(content)
		if trimmed == "" || trimmed == "#!/bin/sh" || trimmed == "#!/bin/bash" {
			if err := os.Remove(hookPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error removing hook file: %v\n", err)
				exitCode = ExitRuntimeError
				return nil
			}
			fmt.Fprintf(os.Stdout, "Removed prism pre-commit hook at %s\n", hookPath)
			return nil
		}

		if err := os.WriteFile(hookPath, []byte(content), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing hook file: %v\n", err)
			exitCode = ExitRuntimeError
			return nil
		}

		fmt.Fprintf(os.Stdout, "Removed prism section from %s\n", hookPath)
		return nil
	},
}

func getHookPath() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--git-dir").Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository (git rev-parse --git-dir failed)")
	}
	gitDir := strings.TrimSpace(string(out))
	return filepath.Join(gitDir, "hooks", "pre-commit"), nil
}

func generateHookScript(failOn, format string, maxFindings int) string {
	var b strings.Builder
	b.WriteString(hookMarkerStart + "\n")
	b.WriteString(fmt.Sprintf("prism review staged --fail-on %s --format %s --max-findings %d\n", failOn, format, maxFindings))
	b.WriteString("PRISM_EXIT=$?\n")
	b.WriteString("if [ $PRISM_EXIT -eq 1 ]; then\n")
	b.WriteString("  echo \"prism: findings above threshold, commit blocked\"\n")
	b.WriteString("  exit 1\n")
	b.WriteString("elif [ $PRISM_EXIT -ge 2 ]; then\n")
	b.WriteString("  echo \"prism: warning — review encountered an error (exit $PRISM_EXIT), allowing commit\"\n")
	b.WriteString("fi\n")
	b.WriteString(hookMarkerEnd + "\n")
	return b.String()
}

func replacePrismSection(existing, section string) string {
	startIdx := strings.Index(existing, hookMarkerStart)
	endIdx := strings.Index(existing, hookMarkerEnd)

	if startIdx == -1 || endIdx == -1 {
		// No existing prism section — append
		if !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		return existing + section
	}

	// Replace existing section
	before := existing[:startIdx]
	after := existing[endIdx+len(hookMarkerEnd):]
	// Trim leading newline from after to avoid double newlines
	after = strings.TrimPrefix(after, "\n")
	return before + section + after
}

func removePrismSection(existing string) string {
	startIdx := strings.Index(existing, hookMarkerStart)
	endIdx := strings.Index(existing, hookMarkerEnd)

	if startIdx == -1 || endIdx == -1 {
		return existing
	}

	before := existing[:startIdx]
	after := existing[endIdx+len(hookMarkerEnd):]
	after = strings.TrimPrefix(after, "\n")

	return before + after
}

func init() {
	hookCmd.AddCommand(hookInstallCmd)
	hookCmd.AddCommand(hookUninstallCmd)
	hookInstallCmd.Flags().StringVar(&hookFailOn, "fail-on", "high", "Fail on severity threshold (none, low, medium, high)")
	hookInstallCmd.Flags().StringVar(&hookFormat, "format", "text", "Output format (text, json, markdown, sarif)")
	hookInstallCmd.Flags().IntVar(&hookMaxFindings, "max-findings", 10, "Maximum number of findings")
}
