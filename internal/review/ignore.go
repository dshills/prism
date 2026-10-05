package review

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/dshills/prism/internal/diffutil"
)

// ignoreToken marks an inline suppression in a code comment:
//
//	secret := loadFromVault() // prism:ignore security "loaded from vault"
//
// The directive must start a real comment: the token comes right after a
// comment opener of the file's language, found by lexing the code (strings
// skipped, so "// prism:ignore" in a string literal is data, not a
// directive). Lexing carries multi-line strings and block comments from line
// to line. A hunk below the top of a file may begin inside a multi-line
// string or a block comment, which the diff cannot show, so for a language
// that has either the whole file is lexed from its first line instead, when
// it can be read and matches the diff. When it cannot, such hunks honour no directives: prism
// reports the finding rather than risk string data suppressing it, and the
// baseline still applies.
//
// The directive covers findings on its own line. A comment alone on its line
// also covers the line below it. After the token come, optionally, the
// categories it applies to (comma-separated; all when there are none) and a
// quoted reason. Anything else after the token makes it no directive, so a
// misspelt category never widens to every category.
//
// Finding directives is best-effort. The lexer knows each language's
// comments and its common string forms, but not all of them: Rust raw and
// multi-line strings, YAML block scalars, and heredocs (shell, Ruby, Perl,
// PHP) are not modelled, so directive-shaped text inside them is taken as a
// comment. That is accepted rather than fixed language by language: whoever
// can write a string into the code can write a real comment there too, so
// such text is no new way to suppress a finding, only an unlikely accident.
// Where suppression must be exact, the baseline is.
const ignoreToken = "prism:ignore"

// knownCategories are the categories a directive may name.
var knownCategories = []Category{
	CategoryBug, CategorySecurity, CategoryPerformance, CategoryCorrectness,
	CategoryStyle, CategoryMaintainability, CategoryTesting, CategoryDocs,
}

type ignoreDirective struct {
	line int // new-file line the directive is on
	// ownLine is true for a comment alone on its line, which also covers the
	// line below it.
	ownLine    bool
	categories []Category // empty: every category
	reason     string
}

func (d ignoreDirective) covers(f Finding) bool {
	r := primaryLoc(f).Lines
	if r.Start <= 0 {
		return false
	}
	end := max(r.End, r.Start)
	onLines := d.line >= r.Start && d.line <= end
	above := d.ownLine && d.line == r.Start-1
	if !onLines && !above {
		return false
	}
	return len(d.categories) == 0 || slices.Contains(d.categories, Category(strings.ToLower(string(f.Category))))
}

// syntax is how comments and strings look in a language: enough to find real
// comments without mistaking string data for one.
type syntax struct {
	line   []string    // line comment openers
	block  [][2]string // block comment opener and closer
	quotes string      // single-line string quotes, with backslash escapes
	multi  []delim     // strings that may span lines
	// guess is true for a file type prism does not know. Every opener is
	// tried, and lexing stops at the first one: an opener that is really an
	// operator ("--", "%") then only hides a directive, never makes one.
	guess bool
}

// delim is a string that may span lines, closed by the same text, and how a
// closing delimiter inside it is escaped. Where a language's rule is unclear
// the later-closing one is used: staying in a string too long can only hide
// a directive, while leaving it too early could turn string data into one.
type delim struct {
	quote  string
	escape escapeRule
}

type escapeRule int

const (
	noEscape  escapeRule = iota // Go raw strings
	backslash                   // \` in a JS template, \""" in Python
	doubled                     // '' inside an SQL string
)

var (
	cStyle    = syntax{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: `"'`}
	goStyle   = syntax{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: `"'`, multi: []delim{{"`", noEscape}}}
	jsStyle   = syntax{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: `"'`, multi: []delim{{"`", backslash}}}
	jvmStyle  = syntax{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: `"'`, multi: []delim{{`"""`, backslash}}}
	pyStyle   = syntax{line: []string{"#"}, quotes: `"'`, multi: []delim{{`"""`, backslash}, {`'''`, backslash}}}
	hashStyle = syntax{line: []string{"#"}, multi: []delim{{`"`, backslash}, {`'`, backslash}}} // shell, Ruby, YAML: quotes may span lines
	sqlStyle  = syntax{line: []string{"--"}, block: [][2]string{{"/*", "*/"}}, multi: []delim{{`'`, doubled}, {`"`, doubled}}}
	markup    = syntax{block: [][2]string{{"<!--", "-->"}}}
	guessed   = syntax{
		line:  []string{"//", "#", "--", ";", "%"},
		block: [][2]string{{"/*", "*/"}, {"<!--", "-->"}},
		multi: []delim{{`"""`, backslash}, {`'''`, backslash}, {"`", backslash}, {`"`, backslash}, {`'`, backslash}},
		guess: true,
	}
)

var syntaxByExt = map[string]syntax{
	".go": goStyle,
	".js": jsStyle, ".jsx": jsStyle, ".mjs": jsStyle, ".cjs": jsStyle, ".ts": jsStyle, ".tsx": jsStyle,
	".c": cStyle, ".h": cStyle, ".cc": cStyle, ".cpp": cStyle, ".hpp": cStyle, ".cs": cStyle,
	".rs": cStyle, ".proto": cStyle, ".dart": cStyle, ".m": cStyle, ".mm": cStyle,
	".java": jvmStyle, ".kt": jvmStyle, ".kts": jvmStyle, ".scala": jvmStyle, ".swift": jvmStyle, ".groovy": jvmStyle,
	".php": {line: []string{"//", "#"}, block: [][2]string{{"/*", "*/"}}, multi: []delim{{`"`, backslash}, {`'`, backslash}}},
	".py":  pyStyle, ".pyi": pyStyle,
	".rb": hashStyle, ".sh": hashStyle, ".bash": hashStyle, ".zsh": hashStyle, ".yaml": hashStyle, ".yml": hashStyle,
	".toml": hashStyle, ".r": hashStyle, ".pl": hashStyle, ".pm": hashStyle, ".ex": hashStyle, ".exs": hashStyle,
	".mk": hashStyle, ".dockerfile": hashStyle, ".ps1": hashStyle, ".conf": hashStyle, ".cfg": hashStyle,
	".tf":  {line: []string{"#", "//"}, block: [][2]string{{"/*", "*/"}}, multi: []delim{{`"`, backslash}}},
	".sql": sqlStyle, ".lua": {line: []string{"--"}, quotes: `"'`}, ".hs": {line: []string{"--"}, block: [][2]string{{"{-", "-}"}}, quotes: `"`},
	".html": markup, ".htm": markup, ".xml": markup, ".svg": markup, ".md": markup,
	".lisp": {line: []string{";"}, multi: []delim{{`"`, backslash}}}, ".el": {line: []string{";"}, multi: []delim{{`"`, backslash}}},
	".clj": {line: []string{";"}, multi: []delim{{`"`, backslash}}},
	".asm": {line: []string{";"}, quotes: `"`}, ".ini": {line: []string{";", "#"}, quotes: `"`},
	".tex": {line: []string{"%"}}, ".erl": {line: []string{"%"}, multi: []delim{{`"`, backslash}}},
}

// crossesLines reports whether the syntax has state that carries from one
// line to the next, so a hunk lexed from its first line may start mid-string
// or mid-comment.
func (s syntax) crossesLines() bool {
	return len(s.multi) > 0 || len(s.block) > 0
}

func syntaxFor(path string) syntax {
	if s, ok := syntaxByExt[strings.ToLower(filepath.Ext(path))]; ok {
		return s
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "makefile", "dockerfile", "gemfile", "rakefile", "justfile":
		return hashStyle
	}
	return guessed
}

// lexState is what a line starts inside: a multi-line string, a block
// comment (the delimiter that ends it), or code.
type lexState struct {
	multi *delim
	block string
}

// scanLine lexes one line starting in st. It returns the directive that
// starts a real comment on the line, if any, and the state at the end of the
// line.
func (s syntax) scanLine(text string, st lexState) (d ignoreDirective, found bool, end lexState) {
	i := 0
	for i < len(text) {
		if st.block != "" {
			j := strings.Index(text[i:], st.block)
			if j < 0 {
				return d, found, st
			}
			i += j + len(st.block)
			st.block = ""
			continue
		}
		if st.multi != nil {
			j := closingQuote(text, i, *st.multi)
			if j < 0 {
				return d, found, st
			}
			i = j + len(st.multi.quote)
			st.multi = nil
			continue
		}

		rest := text[i:]
		if m := opensMulti(rest, s.multi); m != nil {
			st.multi = m
			i += len(m.quote)
			continue
		}
		if q := rest[0]; strings.IndexByte(s.quotes, q) >= 0 {
			i = skipQuoted(text, i)
			continue
		}
		if o := prefixIn(rest, s.line); o != "" {
			if dd, ok := directiveAfter(rest[len(o):]); ok && !found {
				dd.ownLine = strings.TrimSpace(text[:i]) == ""
				return dd, true, st
			}
			return d, found, st // the rest of the line is a comment
		}
		if b, ok := blockIn(rest, s.block); ok {
			body := rest[len(b[0]):]
			closeAt := strings.Index(body, b[1])
			comment := body
			if closeAt >= 0 {
				comment = body[:closeAt]
			}
			if dd, ok := directiveAfter(comment); ok && !found {
				// Alone on its line only with no code before the comment
				// and none after it closes.
				after := ""
				if closeAt >= 0 {
					after = body[closeAt+len(b[1]):]
				}
				dd.ownLine = strings.TrimSpace(text[:i]) == "" && strings.TrimSpace(after) == ""
				d, found = dd, true
			}
			if s.guess && !found {
				return d, found, st
			}
			st.block = b[1]
			i += len(b[0])
			continue
		}
		i++
	}
	return d, found, st
}

// skipQuoted returns the index just past the single-line string that starts
// at text[i], or len(text) when it runs to the end of the line.
func skipQuoted(text string, i int) int {
	q := text[i]
	for j := i + 1; j < len(text); j++ {
		switch text[j] {
		case '\\':
			j++
		case q:
			return j + 1
		}
	}
	return len(text)
}

// opensMulti is the longest multi-line delimiter s starts with, or nil.
func opensMulti(s string, delims []delim) *delim {
	var best *delim
	for i := range delims {
		if strings.HasPrefix(s, delims[i].quote) && (best == nil || len(delims[i].quote) > len(best.quote)) {
			best = &delims[i]
		}
	}
	return best
}

// closingQuote is the index in text, from i, of the delimiter that closes d,
// skipping escaped ones; -1 when the string goes on past this line.
func closingQuote(text string, i int, d delim) int {
	for j := i; j < len(text); j++ {
		switch {
		case d.escape == backslash && text[j] == '\\':
			j++ // the next character is escaped
		case strings.HasPrefix(text[j:], d.quote):
			if d.escape == doubled && strings.HasPrefix(text[j+len(d.quote):], d.quote) {
				j += 2*len(d.quote) - 1 // a doubled quote is a quote, not the end
				continue
			}
			return j
		}
	}
	return -1
}

// prefixIn is the longest of candidates that s starts with, or "".
func prefixIn(s string, candidates []string) string {
	best := ""
	for _, c := range candidates {
		if len(c) > len(best) && strings.HasPrefix(s, c) {
			best = c
		}
	}
	return best
}

func blockIn(s string, blocks [][2]string) ([2]string, bool) {
	for _, b := range blocks {
		if strings.HasPrefix(s, b[0]) {
			return b, true
		}
	}
	return [2]string{}, false
}

// directiveArgs is what may follow the token: a category list (commas, with
// optional spaces around them) and then a quoted reason, each optional.
var directiveArgs = regexp.MustCompile(`^(?:([A-Za-z]+(?:\s*,\s*[A-Za-z]+)*))?\s*(?:"([^"]*)")?\s*$`)

// directiveAfter reads a directive from a comment's text after its opener:
// optional whitespace, the token, then optionally categories and a quoted
// reason. Anything else is no directive.
func directiveAfter(comment string) (ignoreDirective, bool) {
	rest, ok := strings.CutPrefix(strings.TrimLeftFunc(comment, unicode.IsSpace), ignoreToken)
	if !ok || (rest != "" && !unicode.IsSpace(rune(rest[0]))) {
		return ignoreDirective{}, false // not the token, or a longer word such as prism:ignored
	}
	m := directiveArgs.FindStringSubmatch(strings.TrimSpace(rest))
	if m == nil {
		return ignoreDirective{}, false
	}
	d := ignoreDirective{reason: m[2]}
	if m[1] != "" {
		cats, ok := parseCategories(m[1])
		if !ok {
			return ignoreDirective{}, false
		}
		d.categories = cats
	}
	return d, true
}

// parseIgnoreDirective reads the directive in a single line of a file type
// prism does not know.
func parseIgnoreDirective(text string) (ignoreDirective, bool) {
	d, ok, _ := guessed.scanLine(text, lexState{})
	return d, ok
}

// parseCategories reads a comma-separated list of categories, reporting
// false when any part is not a category (the word starts the reason instead).
func parseCategories(s string) ([]Category, bool) {
	var cats []Category
	for _, part := range strings.Split(s, ",") {
		c := Category(strings.ToLower(strings.TrimSpace(part)))
		if !slices.Contains(knownCategories, c) {
			return nil, false
		}
		cats = append(cats, c)
	}
	return cats, true
}

// fileSource returns a file's whole new-file content as reviewed, or false
// when it cannot be had.
type fileSource func(path string) (string, bool)

// ignoreDirectives finds the directives in diff's new-file lines, by path.
// Directives on removed lines do not count: they are gone from the code.
// files, which may be nil, gives whole files for the hunks that need them.
func ignoreDirectives(diff string, files fileSource) map[string][]ignoreDirective {
	out := map[string][]ignoreDirective{}
	for _, sec := range diffutil.SplitSections(diff) {
		path := diffutil.PathFromSection(sec)
		if path == "" || !strings.Contains(sec, ignoreToken) {
			continue
		}
		syn := syntaxFor(path)
		var lines []diffutil.Line
		fromTop := true // every hunk starts at line 1
		hunk := -1
		for _, l := range diffutil.PostImageLines(sec) {
			if l.Removed {
				continue
			}
			if l.Hunk != hunk {
				hunk = l.Hunk
				fromTop = fromTop && l.NewLine == 1
			}
			lines = append(lines, l)
		}
		if !syn.crossesLines() || fromTop {
			// No hunk can start inside a string or comment.
			out[path] = directivesInHunks(syn, lines)
			continue
		}
		if files == nil {
			continue
		}
		if content, ok := files(path); ok && matchesFile(content, lines) {
			out[path] = directivesInFile(syn, content)
		}
	}
	return out
}

// directivesInHunks lexes each hunk from its first line, in code.
func directivesInHunks(syn syntax, lines []diffutil.Line) []ignoreDirective {
	var out []ignoreDirective
	var st lexState
	hunk := -1
	for _, l := range lines {
		if l.Hunk != hunk {
			hunk, st = l.Hunk, lexState{}
		}
		var d ignoreDirective
		var ok bool
		d, ok, st = syn.scanLine(l.Text, st)
		if ok {
			d.line = l.NewLine
			out = append(out, d)
		}
	}
	return out
}

// directivesInFile lexes a whole file from its first line.
func directivesInFile(syn syntax, content string) []ignoreDirective {
	var out []ignoreDirective
	var st lexState
	for n, text := range strings.Split(content, "\n") {
		var d ignoreDirective
		var ok bool
		d, ok, st = syn.scanLine(strings.TrimSuffix(text, "\r"), st)
		if ok {
			d.line = n + 1
			out = append(out, d)
		}
	}
	return out
}

// matchesFile reports whether content holds every one of the diff's
// new-file lines at its line number, so it is the file that was reviewed.
func matchesFile(content string, lines []diffutil.Line) bool {
	file := strings.Split(content, "\n")
	for _, l := range lines {
		if l.NewLine < 1 || l.NewLine > len(file) || strings.TrimSuffix(file[l.NewLine-1], "\r") != l.Text {
			return false
		}
	}
	return true
}

// applyIgnores splits findings into those no directive in diff covers and
// those one does. files, which may be nil, gives whole files (see
// ignoreDirectives).
func applyIgnores(findings []Finding, diff string, files fileSource) (kept []Finding, suppressed []Suppression) {
	directives := ignoreDirectives(diff, files)
	if len(directives) == 0 {
		return findings, nil
	}
	kept = make([]Finding, 0, len(findings))
next:
	for _, f := range findings {
		for _, d := range directives[findingPath(f)] {
			if d.covers(f) {
				suppressed = append(suppressed, Suppression{Finding: f, Source: SuppressedInline, Reason: d.reason})
				continue next
			}
		}
		kept = append(kept, f)
	}
	return kept, suppressed
}
