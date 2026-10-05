package review

import (
	"slices"
	"strings"
	"testing"
)

func TestParseIgnoreDirective(t *testing.T) {
	for _, tc := range []struct {
		text       string
		ok         bool
		ownLine    bool
		categories []Category
		reason     string
	}{
		{`secret := loadFromVault() // prism:ignore security "loaded from vault"`, true, false, []Category{CategorySecurity}, "loaded from vault"},
		{`// prism:ignore`, true, true, nil, ""},
		{`	# prism:ignore style,docs "generated"`, true, true, []Category{CategoryStyle, CategoryDocs}, "generated"},
		{`/* prism:ignore "false positive" */`, true, true, nil, "false positive"},
		// Code after a block comment: not alone on its line.
		{`/* prism:ignore security */ risky()`, true, false, []Category{CategorySecurity}, ""},
		// Spaces around the commas of a category list.
		{`// prism:ignore style, docs`, true, true, []Category{CategoryStyle, CategoryDocs}, ""},
		// Anything but categories and a quoted reason is no directive, so a
		// typo never widens to every category.
		{`// prism:ignore securty`, false, false, nil, ""},
		{`// prism:ignore style, nope`, false, false, nil, ""},
		{`/* prism:ignore false positive */`, false, false, nil, ""},
		{`-- prism:ignore "SQL is static"`, true, true, nil, "SQL is static"},
		{`x := 1 // prism:ignore Bug`, true, false, []Category{CategoryBug}, ""},
		{`// prism:ignored by design`, false, false, nil, ""},
		{`x := 1 // nothing to see`, false, false, nil, ""},
		// In a string literal the token is data, not a directive.
		{`value := "prism:ignore"`, false, false, nil, ""},
		{`value := "// prism:ignore"`, false, false, nil, ""},
		{"value := `# prism:ignore`", false, false, nil, ""},
		{`msg := 'x -- prism:ignore'`, false, false, nil, ""},
		{`log("a \" quote") // prism:ignore`, true, false, nil, ""},
		// A string elsewhere on the line, then a real directive.
		{`s := "prism:ignore" // prism:ignore docs`, true, false, []Category{CategoryDocs}, ""},
		// Not the start of the comment.
		{`// TODO prism:ignore`, false, false, nil, ""},
		// A tab between the categories and the reason.
		{"// prism:ignore security\t\"accepted\"", true, true, []Category{CategorySecurity}, "accepted"},
	} {
		d, ok := parseIgnoreDirective(tc.text)
		if ok != tc.ok {
			t.Errorf("%q: ok = %v, want %v", tc.text, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if d.ownLine != tc.ownLine || !slices.Equal(d.categories, tc.categories) || d.reason != tc.reason {
			t.Errorf("%q: got ownLine=%v categories=%v reason=%q, want %v %v %q",
				tc.text, d.ownLine, d.categories, d.reason, tc.ownLine, tc.categories, tc.reason)
		}
	}
}

func TestApplyIgnores(t *testing.T) {
	diff := addedSection("a.go", 1,
		"func f() {",
		"	// prism:ignore",                    // 2: alone on its line, covers line 3
		"	a := risky()",                       // 3
		"	b := other() // prism:ignore style", // 4: style only, its own line
		"	c := more()",                        // 5: not covered by line 4's trailing comment
		"}",
	) + "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,2 +1,1 @@\n-x := 1 // prism:ignore\n+x := 2\n"

	for _, tc := range []struct {
		name string
		f    Finding
		want bool // suppressed
	}{
		{"line below an own-line comment", evidenceFinding("a.go", CategoryBug, "t", 3, ""), true},
		{"the comment's own line", evidenceFinding("a.go", CategoryBug, "t", 2, ""), true},
		{"matching category on the line", evidenceFinding("a.go", CategoryStyle, "t", 4, ""), true},
		{"other category on the line", evidenceFinding("a.go", CategoryBug, "t", 4, ""), false},
		{"below a trailing comment", evidenceFinding("a.go", CategoryStyle, "t", 5, ""), false},
		{"multi-line finding spanning the directive", Finding{Category: CategoryStyle, Locations: []Location{{Path: "a.go", Lines: LineRange{Start: 3, End: 5}}}}, true},
		{"another file", evidenceFinding("c.go", CategoryBug, "t", 3, ""), false},
		{"directive on a removed line", evidenceFinding("b.go", CategoryBug, "t", 1, ""), false},
		{"no location", Finding{Category: CategoryBug, Locations: []Location{{Path: "a.go"}}}, false},
	} {
		kept, suppressed := applyIgnores([]Finding{tc.f}, diff, nil)
		if got := len(suppressed) == 1; got != tc.want || len(kept)+len(suppressed) != 1 {
			t.Errorf("%s: suppressed = %v, want %v", tc.name, got, tc.want)
		}
		if tc.want && len(suppressed) == 1 && suppressed[0].Source != SuppressedInline {
			t.Errorf("%s: source = %q, want inline", tc.name, suppressed[0].Source)
		}
	}
}

// Directives are found by lexing the file's language, so string data never
// counts as one, while real comments after strings, apostrophes or operators
// do.
func TestIgnoreDirectives_Lexing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		start int
		lines []string
		want  []int // lines with a directive honoured
	}{
		{"apostrophe in a string, then a directive", "a.go", 1,
			[]string{`fmt.Println("don't") // prism:ignore bug`}, []int{1}},
		{"backslash ending a raw string, then a directive", "a.go", 1,
			[]string{"path := `C:\\` // prism:ignore"}, []int{1}},
		{"decrement before a Go comment", "a.go", 1,
			[]string{`i-- // prism:ignore`}, []int{1}},
		{"inside a multi-line raw string, from the top", "a.go", 1,
			[]string{"const t = `", "// prism:ignore security", "`", "x := 1 // prism:ignore"}, []int{4}},
		{"Go below the top of the file, without the file", "a.go", 20,
			[]string{"x := 1 // prism:ignore"}, nil},
		{"python string with a hash", "a.py", 1,
			[]string{`x = "a # prism:ignore"`, `y = 1  # prism:ignore`}, []int{2}},
		{"python docstring from the top", "a.py", 1,
			[]string{`"""`, `# prism:ignore`, `"""`, `z = 2  # prism:ignore`}, []int{4}},
		{"SQL comment", "q.sql", 1,
			[]string{`SELECT 1 -- prism:ignore performance`}, []int{1}},
		{"SQL string spanning lines", "q.sql", 1,
			[]string{`SELECT 'one`, `-- prism:ignore security`, `', x -- prism:ignore`}, []int{3}},
		{"SQL doubled quote inside a string", "q.sql", 1,
			[]string{`SELECT 'it''s -- prism:ignore', y -- prism:ignore docs`}, []int{1}},
		{"SQL below the top, without the file", "q.sql", 7,
			[]string{`SELECT 1 -- prism:ignore`}, nil},
		{"JS escaped backtick inside a template", "a.js", 1,
			[]string{"const t = `a \\` b", "// prism:ignore security", "`", "f() // prism:ignore"}, []int{4}},
		{"Python escaped triple quote", "a.py", 1,
			[]string{`s = """a \""" b`, `# prism:ignore security`, `"""`, `t = 1  # prism:ignore`}, []int{4}},
		{"inside a C block comment, from the top", "a.c", 1,
			[]string{"/* notes", "// prism:ignore security", "*/", "f(); // prism:ignore"}, []int{4}},
		{"a C hunk that may start inside a block comment", "a.c", 12,
			[]string{"// prism:ignore security", "*/"}, nil},
		{"Go has no # comments", "a.go", 1,
			[]string{`x := y # prism:ignore`}, nil},
		{"Rust lifetime before a comment", "a.rs", 1,
			[]string{`fn f<'a>(x: &'a str) // prism:ignore`}, []int{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ignoreDirectives(addedSection(tc.path, tc.start, tc.lines...), nil)[tc.path]
			if lines := directiveLines(got); !slices.Equal(lines, tc.want) {
				t.Errorf("directives on lines %v, want %v", lines, tc.want)
			}
		})
	}
}

func directiveLines(ds []ignoreDirective) []int {
	var lines []int
	for _, d := range ds {
		lines = append(lines, d.line)
	}
	return lines
}

// A hunk below the top of a Go file is checked against the whole file, lexed
// from its first line: a hunk entirely inside a raw string, with neither
// backtick in it, honours nothing, while a real comment is honoured.
func TestIgnoreDirectives_WholeFile(t *testing.T) {
	file := strings.Join([]string{
		"package a",                     // 1
		"",                              // 2
		"const tmpl = `",                // 3
		"line one",                      // 4
		"// prism:ignore security",      // 5: string data
		"line three",                    // 6
		"`",                             // 7
		"",                              // 8
		"func f() {",                    // 9
		"	x := risky() // prism:ignore", // 10: a real comment
		"}",                             // 11
	}, "\n") + "\n"
	files := func(path string) (string, bool) { return file, path == "a.go" }
	lines := strings.Split(file, "\n")

	inString := addedSection("a.go", 4, lines[3:6]...) // lines 4-6, no backtick in sight
	if got := directiveLines(ignoreDirectives(inString, files)["a.go"]); slices.Contains(got, 5) {
		t.Errorf("hunk inside a raw string: directives on %v, want the string's line 5 not among them", got)
	}
	if got := ignoreDirectives(inString, nil)["a.go"]; len(got) != 0 {
		t.Errorf("without the file: directives on %v, want none", directiveLines(got))
	}
	inCode := addedSection("a.go", 9, lines[8:11]...)
	if got := directiveLines(ignoreDirectives(inCode, files)["a.go"]); !slices.Contains(got, 10) || slices.Contains(got, 5) {
		t.Errorf("hunk in code: directives on %v, want line 10 and not the string's line 5", got)
	}

	// A file that does not match the diff is not trusted.
	stale := func(string) (string, bool) { return strings.Replace(file, "risky()", "safe()", 1), true }
	if got := ignoreDirectives(inCode, stale)["a.go"]; len(got) != 0 {
		t.Errorf("file not matching the diff: directives on %v, want none", directiveLines(got))
	}
}

// A block comment followed by code covers only its own line.
func TestApplyIgnores_BlockCommentBeforeCode(t *testing.T) {
	diff := addedSection("a.go", 1, "/* prism:ignore security */ risky()", "next()")
	for line, want := range map[int]bool{1: true, 2: false} {
		_, suppressed := applyIgnores([]Finding{evidenceFinding("a.go", CategorySecurity, "t", line, "")}, diff, nil)
		if got := len(suppressed) == 1; got != want {
			t.Errorf("line %d: suppressed = %v, want %v", line, got, want)
		}
	}
}
