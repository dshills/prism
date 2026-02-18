package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/dshills/prism/internal/review"
)

const defaultAPIURL = "https://api.github.com"

// Client provides access to the GitHub REST API.
type Client struct {
	token   string
	apiURL  string
	httpCli *http.Client
}

// NewClient creates a new GitHub client. Requires GITHUB_TOKEN env var.
func NewClient() (*Client, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is not set")
	}

	apiURL := os.Getenv("GITHUB_API_URL")
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	apiURL = strings.TrimRight(apiURL, "/")

	return &Client{
		token:   token,
		apiURL:  apiURL,
		httpCli: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// GetPRDiff fetches the diff for a pull request.
func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, prNumber int) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiURL, owner, repo, prNumber)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3.diff")

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching PR diff: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode == 404 {
		return "", fmt.Errorf("PR #%d not found in %s/%s", prNumber, owner, repo)
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", fmt.Errorf("authentication failed: %s", string(body))
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API error (status %d): %s", resp.StatusCode, string(body))
	}

	return string(body), nil
}

// PRFile represents a file changed in a pull request.
type PRFile struct {
	Filename string `json:"filename"`
}

// GetPRFiles fetches the list of files changed in a pull request.
func (c *Client) GetPRFiles(ctx context.Context, owner, repo string, prNumber int) ([]string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files", c.apiURL, owner, repo, prNumber)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching PR files: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API error (status %d): %s", resp.StatusCode, string(body))
	}

	var files []PRFile
	if err := json.Unmarshal(body, &files); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Filename
	}
	return names, nil
}

// ReviewComment represents an inline comment on a PR review.
type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// ReviewRequest represents a PR review to post.
type ReviewRequest struct {
	Body     string          `json:"body"`
	Event    string          `json:"event"`
	Comments []ReviewComment `json:"comments"`
}

// PostReview posts a pull request review with inline comments.
func (c *Client) PostReview(ctx context.Context, owner, repo string, prNumber int, review ReviewRequest) error {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", c.apiURL, owner, repo, prNumber)

	payload, err := json.Marshal(review)
	if err != nil {
		return fmt.Errorf("marshaling review: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return fmt.Errorf("posting review: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode == 422 {
		return fmt.Errorf("GitHub rejected review (422): %s", string(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// BuildGitHubReview converts review findings into a GitHub PR review request.
// diffFiles is the set of files in the PR diff. Findings for files not in the diff
// are included in the summary body only.
func BuildGitHubReview(findings []review.Finding, diffFiles map[string]bool) ReviewRequest {
	var high, medium, low int
	var bodyComments []string
	var comments []ReviewComment

	for _, f := range findings {
		switch f.Severity {
		case review.SeverityHigh:
			high++
		case review.SeverityMedium:
			medium++
		case review.SeverityLow:
			low++
		}

		// Check if finding has a valid location in the diff
		if len(f.Locations) > 0 && f.Locations[0].Path != "" && diffFiles[f.Locations[0].Path] {
			loc := f.Locations[0]
			line := loc.Lines.End
			if line == 0 {
				line = loc.Lines.Start
			}
			if line == 0 {
				// No line info — include in body
				bodyComments = append(bodyComments, formatFindingBody(f))
				continue
			}

			body := formatInlineComment(f)
			comments = append(comments, ReviewComment{
				Path: loc.Path,
				Line: line,
				Body: body,
			})
		} else {
			bodyComments = append(bodyComments, formatFindingBody(f))
		}
	}

	// Build summary body
	var sb strings.Builder
	sb.WriteString("## Prism Code Review\n\n")
	sb.WriteString(fmt.Sprintf("| Severity | Count |\n|----------|-------|\n"))
	sb.WriteString(fmt.Sprintf("| High | %d |\n", high))
	sb.WriteString(fmt.Sprintf("| Medium | %d |\n", medium))
	sb.WriteString(fmt.Sprintf("| Low | %d |\n\n", low))

	if len(bodyComments) > 0 {
		sb.WriteString("### General Findings\n\n")
		for _, c := range bodyComments {
			sb.WriteString(c)
			sb.WriteString("\n\n")
		}
	}

	return ReviewRequest{
		Body:     sb.String(),
		Event:    "COMMENT",
		Comments: comments,
	}
}

func formatInlineComment(f review.Finding) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**%s** (%s, %s, confidence: %.0f%%)\n\n", f.Title, f.Severity, f.Category, f.Confidence*100))
	sb.WriteString(f.Message)
	if f.Suggestion != "" {
		sb.WriteString(fmt.Sprintf("\n\n**Suggestion:**\n```\n%s\n```", f.Suggestion))
	}
	return sb.String()
}

func formatFindingBody(f review.Finding) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("- **%s** (%s, %s): %s", f.Title, f.Severity, f.Category, f.Message))
	if f.Suggestion != "" {
		sb.WriteString(fmt.Sprintf(" — *Suggestion: %s*", f.Suggestion))
	}
	return sb.String()
}

var (
	httpsRemoteRe = regexp.MustCompile(`https?://[^/]+/([^/]+)/([^/.\s]+)`)
	sshRemoteRe   = regexp.MustCompile(`[^@]+@[^:]+:([^/]+)/([^/.\s]+)`)
)

// DetectRepo parses owner/repo from the git remote origin URL.
func DetectRepo() (owner, repo string, err error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", "", fmt.Errorf("cannot detect repo: git remote get-url origin failed: %w", err)
	}
	url := strings.TrimSpace(string(out))
	return ParseRemoteURL(url)
}

// ParseRemoteURL extracts owner/repo from a git remote URL.
func ParseRemoteURL(url string) (owner, repo string, err error) {
	// Strip .git suffix
	url = strings.TrimSuffix(url, ".git")

	if m := httpsRemoteRe.FindStringSubmatch(url); len(m) == 3 {
		return m[1], m[2], nil
	}
	if m := sshRemoteRe.FindStringSubmatch(url); len(m) == 3 {
		return m[1], m[2], nil
	}
	return "", "", fmt.Errorf("cannot parse owner/repo from remote URL: %s", url)
}
