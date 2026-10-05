package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/output"
	"github.com/dshills/prism/internal/review"
)

func postTestReport(t *testing.T, format string) (string, []review.Finding) {
	t.Helper()
	at := func(id, path string, line int) review.Finding {
		return review.Finding{ID: id, Title: "t " + id, Message: "m", Severity: review.SeverityHigh, Category: review.CategoryBug,
			Locations: []review.Location{{Path: path, Lines: review.LineRange{Start: line, End: line}}}}
	}
	findings := []review.Finding{at("aaaaaaaaaaaaaaaa", "a.go", 3), at("bbbbbbbbbbbbbbbb", "b.go", 7), at("cccccccccccccccc", "other.go", 1), at("eeeeeeeeeeeeeeee", "a.go", 50)}
	report := review.BuildReport(gitctx.DiffResult{Mode: "staged"}, findings, 0, 0)
	report.Suppressed = []review.Suppression{{Finding: at("dddddddddddddddd", "a.go", 9), Reason: "baseline"}}
	path := filepath.Join(t.TempDir(), "report."+format)
	if err := output.WriteReport(report, format, path); err != nil {
		t.Fatal(err)
	}
	return path, findings
}

// fakeGitHub serves a pull request's files and records posted reviews.
func fakeGitHub(t *testing.T) (posted *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var reviews []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/42"):
			// a.go shows lines 2-4, b.go lines 6-8.
			_, _ = io.WriteString(w, "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -2,2 +2,3 @@\n x\n+y\n z\n"+
				"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -6,3 +6,3 @@\n x\n-y\n+y2\n z\n")
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pulls/42/reviews"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			reviews = append(reviews, body)
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_TOKEN", "test")
	t.Setenv("GITHUB_API_URL", srv.URL)
	return &reviews
}

func runPost(t *testing.T, report, ids, skip string, dryRun bool) int {
	t.Helper()
	resetFlags()
	exitCode = 0
	flagPostPR, flagPostReport, flagPostIDs, flagPostSkip = 42, report, ids, skip
	flagGHOwner, flagGHRepo, flagGHDryRun = "o", "r", dryRun
	githubPostCmd.SetContext(context.Background())
	if err := githubPostCmd.RunE(githubPostCmd, nil); err != nil {
		t.Fatal(err)
	}
	return exitCode
}

func inlinePaths(review map[string]any) []string {
	var paths []string
	for _, c := range review["comments"].([]any) {
		paths = append(paths, c.(map[string]any)["path"].(string))
	}
	return paths
}

// Only the chosen findings are posted, inline on the pull request's files
// and in the summary otherwise; suppressed findings never.
func TestGitHubPostComments(t *testing.T) {
	for _, format := range []string{"json", "sarif"} {
		t.Run(format, func(t *testing.T) {
			posted := fakeGitHub(t)
			report, _ := postTestReport(t, format)

			if code := runPost(t, report, "", "bbbbbbbbbbbbbbbb", false); code != 0 {
				t.Fatalf("exit %d", code)
			}
			if len(*posted) != 1 {
				t.Fatalf("%d reviews posted", len(*posted))
			}
			r := (*posted)[0]
			if got := inlinePaths(r); len(got) != 1 || got[0] != "a.go" {
				t.Errorf("inline comments on %v, want a.go only", got)
			}
			body := r["body"].(string)
			// other.go is not in the PR, and a.go line 50 is not in its diff.
			if !strings.Contains(body, "t cccccccccccccccc") || !strings.Contains(body, "t eeeeeeeeeeeeeeee") ||
				strings.Contains(body, "dddddddddddddddd") || strings.Contains(body, "t bbbbbbbbbbbbbbbb") {
				t.Errorf("summary:\n%s", body)
			}
		})
	}
}

func TestGitHubPostComments_IDsAndDryRun(t *testing.T) {
	posted := fakeGitHub(t)
	report, _ := postTestReport(t, "json")

	if code := runPost(t, report, "aaaaaaaaaaaaaaaa,ffffffffffffffff", "", false); code != ExitUsageError || len(*posted) != 0 {
		t.Errorf("unknown ID: exit %d, %d posted", code, len(*posted))
	}
	if code := runPost(t, report, "aaaaaaaaaaaaaaaa", "", true); code != 0 || len(*posted) != 0 {
		t.Errorf("dry run: exit %d, %d posted", code, len(*posted))
	}
	if code := runPost(t, report, "bbbbbbbbbbbbbbbb", "", false); code != 0 || len(*posted) != 1 || inlinePaths((*posted)[0])[0] != "b.go" {
		t.Errorf("--ids: exit %d, posted %v", code, *posted)
	}
	if code := runPost(t, filepath.Join(t.TempDir(), "missing.json"), "", "", false); code != ExitRuntimeError {
		t.Errorf("missing report: exit %d", code)
	}
}
