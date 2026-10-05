package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dshills/prism/internal/gitctx"
)

// reviewedFiles reads the file the diff was taken from: the working tree for
// unstaged changes, the index for staged ones.
func TestReviewedFiles(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("staged\n")
	git("add", "a.go")
	write("working tree\n")

	ctx := context.Background()
	for mode, want := range map[string]string{"unstaged": "working tree\n", "staged": "staged\n"} {
		files := reviewedFiles(ctx, gitctx.DiffResult{Mode: mode, Repo: gitctx.RepoMeta{Root: root}})
		if got, ok := files("a.go"); !ok || got != want {
			t.Errorf("%s: got %q, %v; want %q", mode, got, ok, want)
		}
	}
	if reviewedFiles(ctx, gitctx.DiffResult{Mode: "github-pr"}) != nil {
		t.Error("a GitHub PR review has no local files")
	}
}
