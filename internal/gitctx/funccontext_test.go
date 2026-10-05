package gitctx

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestWidenSections(t *testing.T) {
	plainA := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -5,1 +5,1 @@\n-x\n+y\n"
	wideA := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -3,4 +3,4 @@ func f() {\n func f() {\n-x\n+y\n }\n"
	plainB := "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -5,1 +5,1 @@\n-p\n+q\n"
	wideB := "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,999 +1,999 @@\n" + strings.Repeat(" line\n", widenSlack)
	plainC := "diff --git a/c.go b/c.go\n--- a/c.go\n+++ b/c.go\n@@ -1 +1 @@\n-m\n+n\n"

	got, n := widenSections(plainA+plainB+plainC, wideA+wideB)
	if want := wideA + plainB + plainC; got != want || n != 1 {
		t.Errorf("widened %d:\n%s\nwant a.go widened, b.go too large to widen, c.go missing from the wide diff", n, got)
	}
}

// End to end in a repo: a change inside a short function gets the whole
// function, signature included; inside a very long one, the plain hunk.
func TestUnstaged_FunctionContext(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	var long strings.Builder
	long.WriteString("func long() {\n")
	for i := range 2000 {
		fmt.Fprintf(&long, "\tv%d := %d\n", i, i)
	}
	long.WriteString("}\n")
	short := "package a\n\nfunc load(path string) error {\n\tf, err := open(path)\n\tif err != nil {\n\t\treturn err\n\t}\n\tdefer f.Close()\n\tdata := read(f)\n\tuse(data)\n\tcheck(data)\n\tdone()\n\treturn nil\n}\n"
	writeIn(t, dir, "a.go", short)
	writeIn(t, dir, "b.go", "package a\n\n"+long.String())
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	writeIn(t, dir, "a.go", strings.Replace(short, "\tdone()\n", "\tdone(true)\n", 1))
	writeIn(t, dir, "b.go", "package a\n\n"+strings.Replace(long.String(), "v1000 := 1000", "v1000 := -1", 1))
	t.Chdir(dir)

	got, err := Unstaged(context.Background(), DiffOptions{FunctionContext: true, NoAutoExclude: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Diff, "\n func load(path string) error {\n") {
		t.Errorf("a.go's hunk should hold the whole function, signature included:\n%s", got.Diff)
	}
	if strings.Count(got.Diff, "\n v") > 20 {
		t.Error("b.go's 2000-line function should not be pulled in whole")
	}
	if got.WidenedFiles != 1 {
		t.Errorf("WidenedFiles = %d, want 1 (a.go)", got.WidenedFiles)
	}

	plain, err := Unstaged(context.Background(), DiffOptions{NoAutoExclude: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Diff, "\n func load(path string) error {\n") || plain.WidenedFiles != 0 {
		t.Error("without FunctionContext the hunks stay plain")
	}
}
