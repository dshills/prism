package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/review"
)

// FR-3: findings at failOn win, then an incomplete review, then success.
func TestFinishExit(t *testing.T) {
	high := review.Finding{Severity: review.SeverityHigh}
	incomplete := review.Coverage{Skipped: []review.Skip{{Target: "diff", Reason: "truncated"}}}
	for _, tc := range []struct {
		name     string
		findings []review.Finding
		cov      review.Coverage
		failOn   string
		allow    bool
		want     int
	}{
		{"clean and complete", nil, review.Coverage{}, "high", false, ExitSuccess},
		{"findings and incomplete: findings win", []review.Finding{high}, incomplete, "high", false, ExitFindings},
		{"incomplete only", nil, incomplete, "high", false, ExitIncomplete},
		{"incomplete, failOn none", nil, incomplete, "none", false, ExitIncomplete},
		{"incomplete, allowed", nil, incomplete, "high", true, ExitSuccess},
		{"findings below failOn, complete", []review.Finding{{Severity: review.SeverityLow}}, review.Coverage{}, "high", false, ExitSuccess},
	} {
		r := &review.Report{Findings: tc.findings, Coverage: tc.cov}
		if got := finishExit(r, tc.failOn, tc.allow); got != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, got, tc.want)
		}
	}
}

// commitRepo makes a repo with a base commit, then one commit that reviews
// cleanly, an empty commit, and a commit whose review the fake server fails.
func commitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("README.md", "base\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	write("a.go", "package a\n\nfunc A() int { return 1 }\n")
	git("add", "-A")
	git("commit", "-q", "-m", "clean")
	git("commit", "-q", "--allow-empty", "-m", "empty")
	write("b.go", "package a\n\n// FAILME\nfunc B() int { return 2 }\n")
	git("add", "-A")
	git("commit", "-q", "-m", "fails")
	return dir
}

// AC 4: a commit whose review fails makes the report incomplete (exit 5) and
// is listed; the empty commit does not count against it.
func TestPerCommit_FailedCommitIsIncomplete(t *testing.T) {
	resetFlags()
	savedExit := exitCode
	t.Cleanup(func() { exitCode = savedExit })
	exitCode = ExitSuccess

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "FAILME") {
			w.WriteHeader(http.StatusBadRequest) // not retried
			_, _ = w.Write([]byte(`{"error":"scripted"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "[]"}}},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("PRISM_OPENAI_BASE_URL", srv.URL)

	t.Chdir(commitRepo(t))
	out := filepath.Join(t.TempDir(), "report.json")
	flagOut = out

	cfg := config.Default()
	cfg.Provider, cfg.Model, cfg.Format, cfg.FailOn = "openai", "test-model", "json", "none"
	cfg.Cache.Enabled = false
	runPerCommitReview(context.Background(), "HEAD~3..HEAD", cfg)

	if exitCode != ExitIncomplete {
		t.Fatalf("exit %d, want %d (ExitIncomplete)", exitCode, ExitIncomplete)
	}
	var rep review.Report
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	c := rep.Coverage
	if c.Complete || len(c.Skipped) != 1 || !strings.HasPrefix(c.Skipped[0].Reason, "review failed:") {
		t.Fatalf("coverage = %+v, want exactly the failed commit skipped", c)
	}
	if c.Chunks != 1 || c.LLMCalls != 1 {
		t.Errorf("coverage should count only the clean commit's review: %+v", c)
	}

	// --allow-incomplete keeps the same report but exits 0.
	exitCode = ExitSuccess
	flagAllowIncomplete = true
	runPerCommitReview(context.Background(), "HEAD~3..HEAD", cfg)
	if exitCode != ExitSuccess {
		t.Errorf("with --allow-incomplete: exit %d, want 0", exitCode)
	}
}
