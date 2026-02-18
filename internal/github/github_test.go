package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/review"
)

func TestGetPRDiff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), "Bearer test-token")
		}
		if r.Header.Get("Accept") != "application/vnd.github.v3.diff" {
			t.Errorf("Accept = %q, want %q", r.Header.Get("Accept"), "application/vnd.github.v3.diff")
		}
		if r.URL.Path != "/repos/owner/repo/pulls/42" {
			t.Errorf("Path = %q, want %q", r.URL.Path, "/repos/owner/repo/pulls/42")
		}
		w.Write([]byte("diff --git a/file.go b/file.go\n"))
	}))
	defer server.Close()

	c := &Client{
		token:   "test-token",
		apiURL:  server.URL,
		httpCli: server.Client(),
	}

	diff, err := c.GetPRDiff(context.Background(), "owner", "repo", 42)
	if err != nil {
		t.Fatalf("GetPRDiff error: %v", err)
	}
	if diff != "diff --git a/file.go b/file.go\n" {
		t.Errorf("diff = %q", diff)
	}
}

func TestGetPRDiff_404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer server.Close()

	c := &Client{
		token:   "test-token",
		apiURL:  server.URL,
		httpCli: server.Client(),
	}

	_, err := c.GetPRDiff(context.Background(), "owner", "repo", 99)
	if err == nil {
		t.Fatal("Expected error for 404")
	}
	if got := err.Error(); got != "PR #99 not found in owner/repo" {
		t.Errorf("error = %q", got)
	}
}

func TestGetPRDiff_401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	c := &Client{
		token:   "bad-token",
		apiURL:  server.URL,
		httpCli: server.Client(),
	}

	_, err := c.GetPRDiff(context.Background(), "owner", "repo", 1)
	if err == nil {
		t.Fatal("Expected error for 401")
	}
	if got := err.Error(); got != `authentication failed: {"message":"Bad credentials"}` {
		t.Errorf("error = %q", got)
	}
}

func TestGetPRFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/pulls/42/files" {
			t.Errorf("Path = %q", r.URL.Path)
		}
		files := []PRFile{
			{Filename: "main.go"},
			{Filename: "util.go"},
		}
		json.NewEncoder(w).Encode(files)
	}))
	defer server.Close()

	c := &Client{
		token:   "test-token",
		apiURL:  server.URL,
		httpCli: server.Client(),
	}

	files, err := c.GetPRFiles(context.Background(), "owner", "repo", 42)
	if err != nil {
		t.Fatalf("GetPRFiles error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files count = %d, want 2", len(files))
	}
	if files[0] != "main.go" || files[1] != "util.go" {
		t.Errorf("files = %v", files)
	}
}

func TestPostReview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/repos/owner/repo/pulls/42/reviews" {
			t.Errorf("Path = %q", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}

		var rev ReviewRequest
		if err := json.NewDecoder(r.Body).Decode(&rev); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if rev.Event != "COMMENT" {
			t.Errorf("Event = %q, want COMMENT", rev.Event)
		}
		if len(rev.Comments) != 1 {
			t.Errorf("Comments count = %d, want 1", len(rev.Comments))
		}

		w.WriteHeader(200)
		w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()

	c := &Client{
		token:   "test-token",
		apiURL:  server.URL,
		httpCli: server.Client(),
	}

	err := c.PostReview(context.Background(), "owner", "repo", 42, ReviewRequest{
		Body:  "summary",
		Event: "COMMENT",
		Comments: []ReviewComment{
			{Path: "main.go", Line: 10, Body: "issue here"},
		},
	})
	if err != nil {
		t.Fatalf("PostReview error: %v", err)
	}
}

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "HTTPS",
			url:       "https://github.com/dshills/prism.git",
			wantOwner: "dshills",
			wantRepo:  "prism",
		},
		{
			name:      "HTTPS no .git",
			url:       "https://github.com/dshills/prism",
			wantOwner: "dshills",
			wantRepo:  "prism",
		},
		{
			name:      "SSH",
			url:       "git@github.com:dshills/prism.git",
			wantOwner: "dshills",
			wantRepo:  "prism",
		},
		{
			name:      "SSH no .git",
			url:       "git@github.com:dshills/prism",
			wantOwner: "dshills",
			wantRepo:  "prism",
		},
		{
			name:    "invalid",
			url:     "not-a-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := ParseRemoteURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

func TestBuildGitHubReview(t *testing.T) {
	findings := []review.Finding{
		{
			Severity:   review.SeverityHigh,
			Category:   review.CategoryBug,
			Title:      "Null pointer",
			Message:    "Possible nil dereference",
			Suggestion: "Add nil check",
			Confidence: 0.9,
			Locations: []review.Location{
				{Path: "main.go", Lines: review.LineRange{Start: 10, End: 12}},
			},
		},
		{
			Severity:   review.SeverityLow,
			Category:   review.CategoryStyle,
			Title:      "Naming",
			Message:    "Use camelCase",
			Confidence: 0.5,
			Locations:  []review.Location{},
		},
	}

	diffFiles := map[string]bool{"main.go": true}
	rev := BuildGitHubReview(findings, diffFiles)

	if rev.Event != "COMMENT" {
		t.Errorf("Event = %q, want COMMENT", rev.Event)
	}

	// One inline comment for the finding with a location in the diff
	if len(rev.Comments) != 1 {
		t.Fatalf("Comments count = %d, want 1", len(rev.Comments))
	}
	if rev.Comments[0].Path != "main.go" {
		t.Errorf("Comment path = %q", rev.Comments[0].Path)
	}
	if rev.Comments[0].Line != 12 {
		t.Errorf("Comment line = %d, want 12", rev.Comments[0].Line)
	}

	// Summary should include counts
	if !strings.Contains(rev.Body, "High") {
		t.Errorf("Summary should mention severity counts, got: %s", rev.Body)
	}
}
