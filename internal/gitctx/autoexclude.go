package gitctx

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/dshills/prism/internal/diffutil"
)

// Excluded is a file left out of a review by prism's own rules (not the
// user's exclude patterns), with the reason. Reviewing it would spend tokens
// on content a model has nothing useful to say about, or, for a deleted
// file, content no finding could be verified against.
type Excluded struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Why a file is excluded.
const (
	ReasonLockfile  = "lockfile"
	ReasonMinified  = "minified or source-map asset"
	ReasonSnapshot  = "test snapshot"
	ReasonGenerated = "generated code"
	ReasonDeleted   = "deleted"
	ReasonNoText    = "no text changes (rename, mode change or binary)"
)

// lockfiles are dependency lock files, matched by base name.
var lockfiles = map[string]bool{
	"go.sum": true, "go.work.sum": true,
	"package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"Cargo.lock": true, "poetry.lock": true, "Pipfile.lock": true, "uv.lock": true, "pdm.lock": true,
	"Gemfile.lock": true, "composer.lock": true, "mix.lock": true, "pubspec.lock": true,
	"Podfile.lock": true, "Package.resolved": true, "flake.lock": true, "gradle.lockfile": true,
	"packages.lock.json": true, "conan.lock": true,
}

// pathReason is why path is excluded by name alone, or "".
func pathReason(path string) string {
	base := filepath.Base(path)
	switch {
	case lockfiles[base]:
		return ReasonLockfile
	case strings.HasSuffix(base, ".min.js"), strings.HasSuffix(base, ".min.css"),
		strings.HasSuffix(base, ".js.map"), strings.HasSuffix(base, ".css.map"):
		return ReasonMinified
	case strings.HasSuffix(base, ".snap"), strings.Contains("/"+path, "/__snapshots__/"):
		return ReasonSnapshot
	}
	return ""
}

// generatedMarker matches the comment a code generator leaves in a file's
// first lines: Go's "Code generated ... DO NOT EDIT." convention (also used
// by many other generators), or "@generated".
var generatedMarker = regexp.MustCompile(`(?i)\bcode generated\b.*\bdo not edit\b|@generated\b`)

// headerLines is how many lines from the top of a file are searched for the
// generated marker.
const headerLines = 10

// isGenerated reports whether a file's first lines (head) mark it as
// generated. Only the leading comment block counts, as in Go's convention
// (the marker comes before the first line of code): searching stops at the
// first line that is neither blank nor a comment, so a banner inside a
// string literal further down is not mistaken for the file's own.
func isGenerated(head string) bool {
	closer := "" // the end of the block comment the scan is inside, if any
	for i, line := range strings.SplitN(head, "\n", headerLines+1) {
		if i == headerLines {
			break
		}
		t := strings.TrimSpace(line)
		if closer == "" {
			switch {
			case t == "":
				continue
			case strings.HasPrefix(t, "/*"):
				closer, t = "*/", t[len("/*"):]
			case strings.HasPrefix(t, "<!--"):
				closer, t = "-->", t[len("<!--"):]
			case commentLine(t):
				if generatedMarker.MatchString(t) {
					return true
				}
				continue
			default:
				return false // code begins: the leading comments are over
			}
		}
		// Inside a block comment, which runs to its closer: only that text
		// is searched (its lines need no comment marker of their own).
		comment, rest, closed := strings.Cut(t, closer)
		if generatedMarker.MatchString(comment) {
			return true
		}
		if closed {
			closer = ""
			if strings.TrimSpace(rest) != "" {
				return false // code follows the comment on this line
			}
		}
	}
	return false
}

func commentLine(t string) bool {
	for _, p := range []string{"//", "#", "/*", "*", "--", ";", "<!--", "%"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// headReader reads the first bytes of each file as reviewed, for the
// generated marker when the diff does not show the top of the file.
type headReader func(paths []string) map[string]string

// autoExclude removes the sections not worth reviewing from diff and reports
// them: deletions, sections with no text changes, files excluded by name
// (pathReason), and generated files. A generated file is recognised from its
// first lines, taken from the diff when it shows them and from heads
// otherwise (heads may be nil).
func autoExclude(diff string, heads headReader) (string, []Excluded) {
	sections := diffutil.SplitSections(diff)
	var (
		kept     = make([]string, 0, len(sections))
		excluded []Excluded
		pending  []int    // sections whose head must be read
		names    []string // their paths
	)
	keep := make([]bool, len(sections))
	for i, sec := range sections {
		path := diffutil.PathFromSection(sec)
		switch {
		case isDeletion(sec):
			excluded = append(excluded, Excluded{Path: headerPath(sec, false), Reason: ReasonDeleted})
		case !strings.Contains(sec, "\n@@"):
			// No hunks, so no "+++" line either: name it from the header.
			if path == "" {
				path = headerPath(sec, true)
			}
			excluded = append(excluded, Excluded{Path: path, Reason: ReasonNoText})
		case path == "":
			keep[i] = true // nothing to name it by: leave it alone
		case pathReason(path) != "":
			excluded = append(excluded, Excluded{Path: path, Reason: pathReason(path)})
		default:
			if head, complete, ok := headFromDiff(sec); ok && (complete || isGenerated(head)) {
				if isGenerated(head) {
					excluded = append(excluded, Excluded{Path: path, Reason: ReasonGenerated})
				} else {
					keep[i] = true
				}
				continue
			}
			keep[i] = true // unless its head says otherwise
			pending = append(pending, i)
			names = append(names, path)
		}
	}
	if len(pending) > 0 && heads != nil {
		got := heads(names)
		for j, i := range pending {
			if isGenerated(got[names[j]]) {
				keep[i] = false
				excluded = append(excluded, Excluded{Path: names[j], Reason: ReasonGenerated})
			}
		}
	}
	for i, sec := range sections {
		if keep[i] {
			kept = append(kept, sec)
		}
	}
	return strings.Join(kept, ""), excluded
}

func isDeletion(sec string) bool {
	meta := diffutil.SectionMeta(sec)
	return strings.Contains(meta, "\ndeleted file mode ") || strings.Contains(meta, "\n+++ /dev/null")
}

// headerPath is a section's path, the new name when newName is set, else
// the old one. Explicit metadata comes first ("--- a/X", "+++ b/Y",
// "rename from X", "rename to Y", and the same for copies), because the
// "diff --git a/X b/Y" header is ambiguous when a path contains " b/". Only a
// section with none of them (a binary or mode-only change) falls back to the
// header, whose two paths are then the same. Git quotes a path with unusual
// characters ("a/caf\303\251.go"), in Go's escape syntax, so it is unquoted.
func headerPath(sec string, newName bool) string {
	meta := diffutil.SectionMeta(sec)
	prefixes := []string{"--- a/", "rename from ", "copy from "}
	if newName {
		prefixes = []string{"+++ b/", "rename to ", "copy to "}
	}
	for line := range strings.SplitSeq(meta, "\n") {
		for _, p := range prefixes {
			if path, ok := metadataPath(line, p); ok {
				return path
			}
		}
	}

	first, _, _ := strings.Cut(sec, "\n")
	rest, ok := strings.CutPrefix(first, "diff --git ")
	if !ok {
		return ""
	}
	var a, b string
	switch {
	case strings.HasPrefix(rest, `"`) || strings.HasSuffix(rest, `"`):
		a, b = quotedHeaderPaths(rest)
	case len(rest)%2 == 1 && rest[len(rest)/2] == ' ' &&
		strings.HasPrefix(rest, "a/") && rest[len(rest)/2+1:] == "b/"+rest[2:len(rest)/2]:
		// The same path twice: split in the middle, whatever it contains.
		a, b = rest[:len(rest)/2], rest[len(rest)/2+1:]
	default:
		if x, y, ok := strings.Cut(rest, " b/"); ok {
			a, b = x, "b/"+y
		}
	}
	if newName {
		return strings.TrimPrefix(b, "b/")
	}
	return strings.TrimPrefix(a, "a/")
}

// metadataPath reads the path on a metadata line starting with prefix p
// ("--- a/", "+++ b/", "rename from ", ...), unquoting a quoted one. Only
// "---"/"+++" paths carry git's a/ or b/ (a rename or copy path is bare, and a
// real directory named b/ must survive), and only they get the tab git
// appends to an unquoted path that has spaces in it.
func metadataPath(line, p string) (string, bool) {
	rest, ok := strings.CutPrefix(line, p)
	if !ok {
		// A quoted "---"/"+++" path: `--- "a/caf\303\251.go"`.
		bare := strings.TrimSuffix(strings.TrimSuffix(p, "a/"), "b/")
		if bare == p {
			return "", false
		}
		if rest, ok = strings.CutPrefix(line, bare+`"`); !ok {
			return "", false
		}
		rest = `"` + rest
	}
	if !strings.HasPrefix(rest, `"`) {
		if p == "--- a/" || p == "+++ b/" {
			rest = strings.TrimSuffix(rest, "\t")
		}
		return rest, true
	}
	u, err := strconv.Unquote(rest)
	if err != nil {
		return "", false
	}
	switch p {
	case "--- a/":
		return strings.TrimPrefix(u, "a/"), true
	case "+++ b/":
		return strings.TrimPrefix(u, "b/"), true
	}
	return u, true
}

// quotedHeaderPaths splits a header's two paths when git quoted either. Git
// quotes only paths with unusual characters, so the other may be unquoted
// and contain spaces: the quoted token is found, never a space split.
func quotedHeaderPaths(rest string) (a, b string) {
	if strings.HasPrefix(rest, `"`) {
		a, after := readQuoted(rest)
		if strings.HasPrefix(after, `"`) {
			b, _ = readQuoted(after)
		} else {
			b = after // unquoted, spaces and all
		}
		return a, b
	}
	if i := strings.LastIndex(rest, ` "b/`); i >= 0 {
		b, _ = readQuoted(rest[i+1:])
		return rest[:i], b
	}
	return "", ""
}

// readQuoted unquotes the quoted string s starts with, returning it and what
// follows its closing quote and separating space; "" when it does not parse.
func readQuoted(s string) (tok, after string) {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			u, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return "", ""
			}
			return u, strings.TrimPrefix(s[i+1:], " ")
		}
	}
	return "", ""
}

// headFromDiff is the first lines of the new file, when the section's first
// hunk starts at its line 1 (a new file, or an edit at the top). complete is
// set when they are all of the header there is to search: headerLines of
// them, or a new file shown whole. A shorter head that lacks the marker
// settles nothing, since the marker may sit on a line the hunk does not show.
func headFromDiff(sec string) (head string, complete, ok bool) {
	lines := diffutil.PostImageLines(sec)
	if len(lines) == 0 {
		return "", false, false
	}
	var b strings.Builder
	n := 0
	for _, l := range lines {
		if l.Removed {
			continue
		}
		if n == 0 && l.NewLine != 1 {
			return "", false, false
		}
		if l.Hunk != lines[0].Hunk || n == headerLines {
			break
		}
		b.WriteString(l.Text)
		b.WriteByte('\n')
		n++
	}
	wholeFile := diffutil.IsNewFile(sec)
	return b.String(), n >= headerLines || wholeFile, n > 0
}

// headBytes is how much of each file headReader keeps: enough for the first
// headerLines of any sane file.
const headBytes = 4096

// workingTreeHeads reads heads from the working tree under root (unstaged
// changes).
func workingTreeHeads(root string) headReader {
	return func(paths []string) map[string]string {
		out := make(map[string]string, len(paths))
		for _, p := range paths {
			// Git diffs a symlink's target path, not the target's contents,
			// so only regular files are read (which also keeps a FIFO from
			// blocking the review).
			name := filepath.Join(root, filepath.FromSlash(p))
			if info, err := os.Lstat(name); err != nil || !info.Mode().IsRegular() {
				continue
			}
			f, err := os.Open(name)
			if err != nil {
				continue
			}
			buf := make([]byte, headBytes)
			n, _ := io.ReadFull(f, buf)
			_ = f.Close()
			out[p] = string(buf[:n])
		}
		return out
	}
}

// revisionHeads reads heads from rev ("" for the index) with one
// `git cat-file --batch` for all paths.
func revisionHeads(ctx context.Context, root, rev string) headReader {
	return func(paths []string) map[string]string {
		out := make(map[string]string, len(paths))
		// The batch protocol is one request per line, so a path with a line
		// break in it would shift every later answer onto the wrong file:
		// such paths are not asked for (their files are kept for review).
		asked := paths[:0:0]
		var in bytes.Buffer
		for _, p := range paths {
			if strings.ContainsAny(p, "\r\n") {
				continue
			}
			asked = append(asked, p)
			fmt.Fprintf(&in, "%s:%s\n", rev, p)
		}
		paths = asked
		cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
		cmd.Dir = root
		cmd.Stdin = &in
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return out
		}
		if err := cmd.Start(); err != nil {
			return out
		}
		// Stream the output: keep headBytes of each object and discard the
		// rest as it arrives, so a large file never sits in memory whole.
		r := bufio.NewReader(stdout)
		for _, p := range paths {
			header, err := r.ReadString('\n')
			if err != nil {
				break
			}
			// "<request> missing" or "<request> ambiguous" has no content
			// (and the request, echoed, may itself contain spaces).
			if strings.HasSuffix(header, " missing\n") || strings.HasSuffix(header, " ambiguous\n") {
				continue
			}
			fields := strings.Fields(header) // "<sha> <type> <size>"
			if len(fields) != 3 {
				break // unknown framing: stop rather than misread later answers
			}
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				break
			}
			head := make([]byte, min(size, headBytes))
			if _, err := io.ReadFull(r, head); err != nil {
				break
			}
			// The rest of the object, and its trailing newline.
			if _, err := io.CopyN(io.Discard, r, size-int64(len(head))+1); err != nil {
				break
			}
			if fields[1] == "blob" {
				out[p] = string(head)
			}
		}
		_, _ = io.Copy(io.Discard, r) // let git finish writing before Wait
		_ = cmd.Wait()
		return out
	}
}

// headsFor is how to read the heads of the files a diff in mode shows: the
// working tree for unstaged changes, the index for staged ones, the tip
// revision for a commit or range. nil when there is no repository to read.
func headsFor(ctx context.Context, root, mode, rangeStr string) headReader {
	if root == "" {
		return nil
	}
	switch mode {
	case "unstaged":
		return workingTreeHeads(root)
	case "staged":
		return revisionHeads(ctx, root, "")
	case "commit":
		return revisionHeads(ctx, root, rangeStr)
	case "range":
		if strings.Contains(rangeStr, "..") {
			return revisionHeads(ctx, root, rangeTip(rangeStr))
		}
		// "rev^!" and "rev^-" (or "rev^-n") compare rev with a parent;
		// any other single revision is compared with the working tree.
		if tip, ok := parentRangeTip(rangeStr); ok {
			return revisionHeads(ctx, root, tip)
		}
		return workingTreeHeads(root)
	}
	return nil
}

// parentRangeTip is rev for the parent-range forms "rev^!" and "rev^-[n]".
func parentRangeTip(r string) (string, bool) {
	if tip, ok := strings.CutSuffix(r, "^!"); ok && tip != "" {
		return tip, true
	}
	if i := strings.LastIndex(r, "^-"); i > 0 {
		if n := r[i+2:]; n == "" || strings.Trim(n, "0123456789") == "" {
			return r[:i], true
		}
	}
	return "", false
}

// rangeTip is the revision whose files a range's diff shows: what follows
// "..." or "..", or HEAD when that is empty.
func rangeTip(r string) string {
	for _, sep := range []string{"...", ".."} {
		if i := strings.Index(r, sep); i >= 0 {
			if tip := r[i+len(sep):]; tip != "" {
				return tip
			}
			return "HEAD"
		}
	}
	if r == "" {
		return "HEAD"
	}
	return r
}
