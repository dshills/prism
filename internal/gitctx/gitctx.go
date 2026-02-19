package gitctx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DiffOptions controls how diffs are gathered.
type DiffOptions struct {
	ContextLines int
	MaxDiffBytes int
	Include      []string
	Exclude      []string
}

// DiffResult holds the collected diff and metadata.
type DiffResult struct {
	Diff  string
	Files []string
	Mode  string
	Range string
	Repo  RepoMeta
}

// RepoMeta contains git repository metadata.
type RepoMeta struct {
	Root   string
	Head   string
	Branch string
}

// GetRepoMeta collects repository metadata from git.
func GetRepoMeta() (RepoMeta, error) {
	root, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return RepoMeta{}, fmt.Errorf("not a git repository: %w", err)
	}
	head, err := gitOutput("rev-parse", "HEAD")
	if err != nil {
		head = "" // new repo with no commits
	}
	branch, err := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		branch = ""
	}
	return RepoMeta{
		Root:   strings.TrimSpace(root),
		Head:   strings.TrimSpace(head),
		Branch: strings.TrimSpace(branch),
	}, nil
}

// Unstaged returns the diff of working tree vs index.
func Unstaged(opts DiffOptions) (DiffResult, error) {
	args := buildDiffArgs(opts)
	diff, err := gitOutput(append([]string{"diff"}, args...)...)
	if err != nil {
		return DiffResult{}, fmt.Errorf("git diff: %w", err)
	}
	return buildResult(diff, "unstaged", "", opts)
}

// Staged returns the diff of index vs HEAD.
func Staged(opts DiffOptions) (DiffResult, error) {
	args := buildDiffArgs(opts)
	diff, err := gitOutput(append([]string{"diff", "--cached"}, args...)...)
	if err != nil {
		return DiffResult{}, fmt.Errorf("git diff --cached: %w", err)
	}
	return buildResult(diff, "staged", "", opts)
}

// Commit returns the diff for a specific commit vs its parent.
func Commit(sha string, parent string, opts DiffOptions) (DiffResult, error) {
	args := buildDiffArgs(opts)
	if parent != "" {
		cmdArgs := append([]string{"diff", parent, sha}, args...)
		diff, err := gitOutput(cmdArgs...)
		if err != nil {
			return DiffResult{}, fmt.Errorf("git diff %s %s: %w", parent, sha, err)
		}
		return buildResult(diff, "commit", sha, opts)
	}
	cmdArgs := append([]string{"diff", sha + "~1", sha}, args...)
	diff, err := gitOutput(cmdArgs...)
	if err != nil {
		// Might be initial commit, try show
		showArgs := append([]string{"show", "--format=", sha, "--"}, args[1:]...) // skip -U flag reuse
		diff, err = gitOutput(showArgs...)
		if err != nil {
			return DiffResult{}, fmt.Errorf("git show %s: %w", sha, err)
		}
	}
	return buildResult(diff, "commit", sha, opts)
}

// Range returns the combined diff for a revision range.
func Range(revRange string, mergeBase bool, opts DiffOptions) (DiffResult, error) {
	args := buildDiffArgs(opts)
	diffRange := revRange
	if mergeBase && strings.Contains(revRange, "..") && !strings.Contains(revRange, "...") {
		diffRange = strings.Replace(revRange, "..", "...", 1)
	}
	cmdArgs := append([]string{"diff", diffRange}, args...)
	diff, err := gitOutput(cmdArgs...)
	if err != nil {
		return DiffResult{}, fmt.Errorf("git diff %s: %w", revRange, err)
	}
	return buildResult(diff, "range", revRange, opts)
}

// Snippet wraps raw content as a "diff" for review. If base is provided, computes a real diff.
func Snippet(content, path, lang, base string) (DiffResult, error) {
	var diff string
	if base != "" {
		tmpDir, err := os.MkdirTemp("", "prism-snippet-*")
		if err != nil {
			return DiffResult{}, fmt.Errorf("creating temp dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		aDir := filepath.Join(tmpDir, "a")
		bDir := filepath.Join(tmpDir, "b")
		baseName := filepath.Base(path)

		if err := os.MkdirAll(aDir, 0o755); err != nil {
			return DiffResult{}, err
		}
		if err := os.MkdirAll(bDir, 0o755); err != nil {
			return DiffResult{}, err
		}
		if err := os.WriteFile(filepath.Join(aDir, baseName), []byte(base), 0o644); err != nil {
			return DiffResult{}, err
		}
		if err := os.WriteFile(filepath.Join(bDir, baseName), []byte(content), 0o644); err != nil {
			return DiffResult{}, err
		}

		// git diff --no-index returns exit code 1 when files differ (expected).
		// Only treat it as an error if the output is empty AND there's an error.
		diff, err = gitOutput("diff", "--no-index",
			filepath.Join(aDir, baseName),
			filepath.Join(bDir, baseName))
		if err != nil && diff == "" {
			return DiffResult{}, fmt.Errorf("git diff --no-index: %w", err)
		}
	} else {
		lines := strings.Split(content, "\n")
		var b strings.Builder
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
		fmt.Fprintf(&b, "new file mode 100644\n")
		fmt.Fprintf(&b, "--- /dev/null\n")
		fmt.Fprintf(&b, "+++ b/%s\n", path)
		fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
		for _, line := range lines {
			fmt.Fprintf(&b, "+%s\n", line)
		}
		diff = b.String()
	}

	return DiffResult{
		Diff:  diff,
		Files: []string{path},
		Mode:  "snippet",
	}, nil
}

func buildDiffArgs(opts DiffOptions) []string {
	var args []string
	if opts.ContextLines > 0 {
		args = append(args, fmt.Sprintf("-U%d", opts.ContextLines))
	}
	args = append(args, "--")
	if len(opts.Include) > 0 {
		for _, p := range opts.Include {
			if p != "**/*" {
				args = append(args, p)
			}
		}
	}
	return args
}

func buildResult(diff, mode, rangeStr string, opts DiffOptions) (DiffResult, error) {
	meta, err := GetRepoMeta()
	if err != nil {
		meta = RepoMeta{}
	}

	files := extractFiles(diff)

	// Filter excludes before truncating so excluded files don't consume the byte budget
	if len(opts.Exclude) > 0 {
		diff = filterExcluded(diff, opts.Exclude)
		files = filterFileList(files, opts.Exclude)
	}

	if opts.MaxDiffBytes > 0 && len(diff) > opts.MaxDiffBytes {
		diff = diff[:opts.MaxDiffBytes] + "\n... (diff truncated at max-diff-bytes limit)\n"
	}

	return DiffResult{
		Diff:  diff,
		Files: files,
		Mode:  mode,
		Range: rangeStr,
		Repo:  meta,
	}, nil
}

func extractFiles(diff string) []string {
	var files []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			f := strings.TrimPrefix(line, "+++ b/")
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files
}

func filterExcluded(diff string, excludes []string) string {
	sections := splitDiffSections(diff)
	var kept []string
	for _, section := range sections {
		path := extractPathFromSection(section)
		if path == "" || !MatchesAny(path, excludes) {
			kept = append(kept, section)
		}
	}
	return strings.Join(kept, "")
}

func splitDiffSections(diff string) []string {
	var sections []string
	lines := strings.Split(diff, "\n")
	var current strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") && current.Len() > 0 {
			sections = append(sections, current.String())
			current.Reset()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	if current.Len() > 0 {
		sections = append(sections, current.String())
	}
	return sections
}

func extractPathFromSection(section string) string {
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			return strings.TrimPrefix(line, "+++ b/")
		}
	}
	return ""
}

func filterFileList(files []string, excludes []string) []string {
	var result []string
	for _, f := range files {
		if !MatchesAny(f, excludes) {
			result = append(result, f)
		}
	}
	return result
}

// MatchesAny returns true if the path matches any of the given glob patterns.
func MatchesAny(path string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
		clean := strings.TrimPrefix(pattern, "**/")
		if clean != pattern {
			matched, err = filepath.Match(clean, filepath.Base(path))
			if err == nil && matched {
				return true
			}
			matched, err = filepath.Match(clean, path)
			if err == nil && matched {
				return true
			}
		}
	}
	return false
}

// maxFileBytes is the per-file size limit for codebase review.
const maxFileBytes = 1 << 20 // 1MB

// WalkFiles returns all git-tracked, non-binary files matching the
// include/exclude filters. Uses `git ls-files` for the file list and
// detects binaries via `git diff --no-index --numstat /dev/null <file>`.
func WalkFiles(opts DiffOptions) ([]string, error) {
	out, err := gitOutput("ls-files")
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Apply include filter
		if len(opts.Include) > 0 {
			if !MatchesAny(line, opts.Include) {
				continue
			}
		}
		// Apply exclude filter
		if len(opts.Exclude) > 0 {
			if MatchesAny(line, opts.Exclude) {
				continue
			}
		}
		// Skip binary files
		if isBinary(line) {
			continue
		}
		files = append(files, line)
	}

	sort.Strings(files)
	return files, nil
}

// isBinary detects whether a file is binary using git diff --numstat.
// Binary files show "-\t-\t" for added/removed lines.
func isBinary(path string) bool {
	out, _ := gitOutput("diff", "--no-index", "--numstat", "/dev/null", path)
	return strings.HasPrefix(strings.TrimSpace(out), "-\t-\t")
}

// Codebase reads all tracked source files and assembles them as
// synthetic unified diffs. Returns a DiffResult with Mode="codebase".
func Codebase(opts DiffOptions) (DiffResult, error) {
	meta, err := GetRepoMeta()
	if err != nil {
		return DiffResult{}, err
	}

	files, err := WalkFiles(opts)
	if err != nil {
		return DiffResult{}, err
	}

	var combined strings.Builder
	var includedFiles []string
	totalBytes := 0

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // skip unreadable files
		}
		if len(data) > maxFileBytes {
			continue // skip oversized files
		}

		content := string(data)
		lines := strings.Split(content, "\n")

		var section strings.Builder
		fmt.Fprintf(&section, "diff --git a/%s b/%s\n", path, path)
		fmt.Fprintf(&section, "new file mode 100644\n")
		fmt.Fprintf(&section, "--- /dev/null\n")
		fmt.Fprintf(&section, "+++ b/%s\n", path)
		fmt.Fprintf(&section, "@@ -0,0 +1,%d @@\n", len(lines))
		for _, line := range lines {
			fmt.Fprintf(&section, "+%s\n", line)
		}

		sectionStr := section.String()

		// Respect MaxDiffBytes as total budget
		if opts.MaxDiffBytes > 0 && totalBytes+len(sectionStr) > opts.MaxDiffBytes {
			break
		}

		combined.WriteString(sectionStr)
		includedFiles = append(includedFiles, path)
		totalBytes += len(sectionStr)
	}

	return DiffResult{
		Diff:  combined.String(),
		Files: includedFiles,
		Mode:  "codebase",
		Repo:  meta,
	}, nil
}

// CommitInfo holds a commit SHA and its subject line.
type CommitInfo struct {
	SHA     string
	Subject string
}

// ListCommits returns commits in a revision range, oldest first.
// If mergeBase is true, ".." is converted to "..." for merge-base comparison.
func ListCommits(revRange string, mergeBase bool) ([]CommitInfo, error) {
	listRange := revRange
	if mergeBase && strings.Contains(revRange, "..") && !strings.Contains(revRange, "...") {
		listRange = strings.Replace(revRange, "..", "...", 1)
	}

	// Use --format to get SHA and subject in a single git call.
	// Output format: "commit <sha>\n<subject>\n" per commit.
	out, err := gitOutput("rev-list", "--reverse", "--format=%s", listRange)
	if err != nil {
		return nil, fmt.Errorf("git rev-list %s: %w", revRange, err)
	}

	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}

	lines := strings.Split(out, "\n")
	var commits []CommitInfo
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "commit ") {
			continue
		}
		sha := strings.TrimPrefix(line, "commit ")
		var subject string
		if i+1 < len(lines) {
			subject = strings.TrimSpace(lines[i+1])
			i++ // skip the subject line
		}
		commits = append(commits, CommitInfo{
			SHA:     sha,
			Subject: subject,
		})
	}
	return commits, nil
}

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(out), fmt.Errorf("%s: %s", err, string(exitErr.Stderr))
		}
		return "", err
	}
	return string(out), nil
}
