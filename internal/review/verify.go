package review

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/diffutil"
	"github.com/dshills/prism/internal/gitctx"
	"github.com/dshills/prism/internal/redact"
)

// Tags that verification adds to a finding it keeps.
const (
	TagUnverified        = "unverified"
	TagLocationCorrected = "location-corrected"
	// TagVetFailed: go vet failed for the claim's package. The claim may be
	// right, but vet also fails on analyzer warnings or missing dependencies,
	// so this records what happened rather than asserting the claim.
	TagVetFailed = "vet-failed"
	// TagFixDropped: the finding's fix was removed because its "before" text
	// is not in the file exactly once, so it could not be applied as an exact
	// replacement. The finding and its suggestion are kept.
	TagFixDropped = "fix-dropped"
)

// VerifyFindings checks findings against the reviewed input before they are
// reported (specs/SPEC-review-integrity.md FR-5, FR-6). Evidence runs first;
// only its survivors go on to the Go compile check, then their fixes are
// checked (verifyFixes). Every finding removed is returned in discarded with
// its reason, never dropped silently. With verification turned off, findings
// pass through unchanged, fixes included.
func VerifyFindings(ctx context.Context, findings []Finding, diff gitctx.DiffResult, cfg config.Config) (kept []Finding, discarded []Discard) {
	if !cfg.ShouldVerifyFindings() {
		return findings, []Discard{}
	}
	// Check against what the model saw: the redacted diff.
	text := diff.Diff
	if cfg.Privacy.RedactSecrets {
		text = redact.Secrets(text)
	}
	kept, discarded = verifyEvidence(findings, text)
	kept, compileDiscarded := verifyCompileClaims(ctx, kept, treeEnvFor(diff))
	// Fixes are checked against the code itself, not the redacted text: an
	// agent applies them to the real file.
	kept = verifyFixes(kept, diff.Diff, reviewedFiles(ctx, diff))
	return kept, append(discarded, compileDiscarded...)
}

// verifyFixes keeps each finding's fix only if its Before text occurs exactly
// once in the finding's file, so an agent can apply it as an exact string
// replacement. That is checked in the whole file as reviewed, when files can
// read it. Otherwise only a file the diff shows whole (a new file, or a
// codebase review's section) can be checked: being unique in the hunks shown
// says nothing about the code around them. A fix that fails, or cannot be
// checked, is removed and the finding tagged TagFixDropped.
func verifyFixes(findings []Finding, diff string, files fileSource) []Finding {
	var whole map[string]string // files the diff shows whole, by path; built on first use
	for i, f := range findings {
		if f.Fix == nil {
			continue
		}
		path := findingPath(f)
		content, ok := "", false
		if files != nil {
			content, ok = files(path)
		}
		if !ok {
			if whole == nil {
				whole = wholeFilesInDiff(diff)
			}
			content, ok = whole[path]
		}
		if !ok || !occursOnce(content, f.Fix.Before) {
			f.Fix = nil
			f.Tags = addTag(f.Tags, TagFixDropped)
			findings[i] = f
		}
	}
	return findings
}

// occursOnce reports whether sub starts at exactly one position in s,
// counting overlapping matches ("aba" occurs twice in "ababa").
func occursOnce(s, sub string) bool {
	i := strings.Index(s, sub)
	return i >= 0 && !strings.Contains(s[i+1:], sub)
}

// wholeFilesInDiff is the content of each file the diff shows in full: a
// section whose old side is /dev/null (a new file, or a codebase review's
// section) has every line of the file in its hunks.
func wholeFilesInDiff(diff string) map[string]string {
	out := map[string]string{}
	for _, sec := range diffutil.SplitSections(diff) {
		path := diffutil.PathFromSection(sec)
		if path == "" || !strings.Contains(sec, "\n--- /dev/null\n") {
			continue
		}
		var b strings.Builder
		for _, l := range diffutil.PostImageLines(sec) {
			if !l.Removed {
				b.WriteString(l.Text)
				b.WriteByte('\n')
			}
		}
		content := b.String()
		// A new file has no old side, so git's marker can only be about the
		// new one: the file does not end in a newline.
		if strings.Contains(sec, "\n\\ No newline at end of file") {
			content = strings.TrimSuffix(content, "\n")
		}
		out[path] = content
	}
	return out
}


// ---------------------------------------------------------------------------
// FR-5: evidence
// ---------------------------------------------------------------------------

// indexedLine is a non-blank, normalised hunk line with its diff position.
type indexedLine struct {
	norm string
	line diffutil.Line
}

// verifyEvidence checks each finding's path and quoted evidence against the
// file's section of diff, and corrects its lines when the evidence sits
// elsewhere.
func verifyEvidence(findings []Finding, diff string) (kept []Finding, discarded []Discard) {
	files := indexFiles(diff)

	kept = make([]Finding, 0, len(findings))
	discarded = []Discard{}
	for _, f := range findings {
		loc := primaryLoc(f)
		lines, ok := files[loc.Path]
		if !ok {
			discarded = append(discarded, Discard{Finding: f, Reason: "path not in diff"})
			continue
		}
		if strings.TrimSpace(f.Evidence) == "" {
			f.Tags = addTag(f.Tags, TagUnverified)
			kept = append(kept, f)
			continue
		}
		start, end, found := locateEvidence(lines, f.Evidence, loc.Lines.Start)
		if !found {
			discarded = append(discarded, Discard{Finding: f, Reason: "quoted evidence not found in " + loc.Path})
			continue
		}
		if start > 0 && !overlaps(loc.Lines, start, end) {
			f.Locations = slices.Clone(f.Locations)
			f.Locations[0].Lines = LineRange{Start: start, End: end}
			f.Tags = addTag(f.Tags, TagLocationCorrected)
		}
		kept = append(kept, f)
	}
	return kept, discarded
}

// indexFiles indexes each file section of diff by path: its non-blank hunk
// lines, normalised for matching quoted evidence.
func indexFiles(diff string) map[string][]indexedLine {
	files := map[string][]indexedLine{}
	for _, sec := range diffutil.SplitSections(diff) {
		p := diffutil.PathFromSection(sec)
		if p == "" {
			continue
		}
		var lines []indexedLine
		for _, l := range diffutil.PostImageLines(sec) {
			if n := normalizeCode(l.Text); n != "" {
				lines = append(lines, indexedLine{norm: n, line: l})
			}
		}
		files[p] = lines
	}
	return files
}

// locateEvidence finds the evidence as a contiguous run of the file's
// normalised lines. When it occurs more than once, the match nearest the
// finding's stated start line wins. It returns the new-file range of the
// matched (non-removed) lines, or 0, 0 when the match is on removed lines only.
func locateEvidence(lines []indexedLine, evidence string, statedStart int) (start, end int, found bool) {
	i, n, found := locateEvidenceRun(lines, evidence, statedStart)
	if !found {
		return 0, 0, false
	}
	s, e := runRange(lines[i : i+n])
	return s, e, true
}

// locateEvidenceRun is locateEvidence's match as a position in lines: the
// index of its first line and its length.
func locateEvidenceRun(lines []indexedLine, evidence string, statedStart int) (i, n int, found bool) {
	ev := evidenceLines(evidence, false)
	matches := findRuns(lines, ev)
	if len(matches) == 0 {
		// A model may copy the diff's own '+' / '-' markers into the quote.
		if stripped := evidenceLines(evidence, true); !slices.Equal(stripped, ev) {
			ev = stripped
			matches = findRuns(lines, ev)
		}
	}
	if len(matches) == 0 {
		return 0, 0, false
	}
	best, bestDist := 0, math.MaxInt
	for _, m := range matches {
		s, _ := runRange(lines[m : m+len(ev)])
		d := bestDistance(s, statedStart)
		if d < bestDist {
			best, bestDist = m, d
		}
	}
	return best, len(ev), true
}

func findRuns(lines []indexedLine, ev []string) []int {
	if len(ev) == 0 {
		return nil
	}
	var out []int
	for i := 0; i+len(ev) <= len(lines); i++ {
		match := true
		for j := range ev {
			// A quote lies within one hunk: the lines between two hunks are
			// not in the diff, so a run across them is not contiguous code.
			if lines[i+j].norm != ev[j] || lines[i+j].line.Hunk != lines[i].line.Hunk {
				match = false
				break
			}
		}
		if match {
			out = append(out, i)
		}
	}
	return out
}

// runRange is the new-file line range of a matched run, ignoring removed
// lines; 0, 0 when every matched line was removed.
func runRange(run []indexedLine) (start, end int) {
	for _, l := range run {
		if l.line.Removed {
			continue
		}
		if start == 0 || l.line.NewLine < start {
			start = l.line.NewLine
		}
		if l.line.NewLine > end {
			end = l.line.NewLine
		}
	}
	return start, end
}

func bestDistance(start, stated int) int {
	if start == 0 {
		return math.MaxInt - 1 // removed-only runs rank after any positioned run
	}
	d := start - stated
	if d < 0 {
		return -d
	}
	return d
}

func overlaps(r LineRange, start, end int) bool {
	rEnd := r.End
	if rEnd < r.Start {
		rEnd = r.Start
	}
	return r.Start <= end && start <= rEnd
}

// evidenceLines normalises quoted evidence into its non-blank lines. With
// stripMarkers, a leading diff marker ('+' or '-') is removed from each line.
func evidenceLines(evidence string, stripMarkers bool) []string {
	var out []string
	for _, l := range strings.Split(evidence, "\n") {
		t := strings.TrimSpace(l)
		if stripMarkers && (strings.HasPrefix(t, "+") || strings.HasPrefix(t, "-")) {
			t = t[1:]
		}
		if n := normalizeCode(t); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// normalizeCode trims a line and collapses internal whitespace runs, so
// indentation and alignment differences do not defeat a match.
func normalizeCode(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func primaryLoc(f Finding) Location {
	if len(f.Locations) == 0 {
		return Location{}
	}
	return f.Locations[0]
}

func addTag(tags []string, tag string) []string {
	if slices.Contains(tags, tag) {
		return tags
	}
	return append(slices.Clone(tags), tag)
}

// ---------------------------------------------------------------------------
// FR-6: Go compile claims
// ---------------------------------------------------------------------------

var compileClaimTags = []string{"compiler-error", "compile-error", "compilation"}

var compileClaimRE = regexp.MustCompile(`(?i)\b(does not|doesn't|won't|will not|fails? to) compile\b|\bcompil(e|ation) (error|failure)\b|\bundefined: |\bhas no (field or )?method\b|\bnot declared\b`)

// isCompileClaim reports whether a finding asserts that Go code does not
// compile.
func isCompileClaim(f Finding) bool {
	if !strings.HasSuffix(primaryLoc(f).Path, ".go") {
		return false
	}
	for _, t := range f.Tags {
		if slices.Contains(compileClaimTags, strings.ToLower(t)) {
			return true
		}
	}
	// The title states the claim. A message may merely discuss compilation
	// (a finding about a build check, say), and treating that as a claim
	// would let vet discard a finding that asserts nothing about compiling.
	return compileClaimRE.MatchString(f.Title)
}

// GitRunner runs git in dir and reports whether it exited 0, with stdout.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (stdout string, err error)
}

// GoRunner runs the go tool in a package directory.
type GoRunner interface {
	Available() bool
	Vet(ctx context.Context, dir string) error
	// PackageFiles lists the file names go vet compiles in dir under the
	// current platform and build tags (library, cgo and both kinds of test
	// file).
	PackageFiles(ctx context.Context, dir string) ([]string, error)
}

// TreeEnv is what the compile check needs to know about the working tree.
type TreeEnv struct {
	RepoRoot          string
	Mode              string // unstaged, staged, commit, range, codebase, snippet
	TipRev            string // the reviewed end revision (commit and range modes)
	Git               GitRunner
	Go                GoRunner
	PerPackageTimeout time.Duration
	TotalBudget       time.Duration
	// ModuleRoot returns the repo-relative directory of the Go module that
	// holds dir ("." for the repo root). nil means ".".
	ModuleRoot func(dir string) string
	// LocalDeps reports whether the module at mod builds against code outside
	// itself: a go.work workspace, or a replace directive to a local path.
	// nil means no.
	LocalDeps func(mod string) bool
}

// treeEnvFor builds the compile check's environment; a variable so tests can
// substitute fakes.
var treeEnvFor = newTreeEnv

// newTreeEnv derives the environment for a real review.
func newTreeEnv(diff gitctx.DiffResult) TreeEnv {
	env := TreeEnv{
		RepoRoot:          diff.Repo.Root,
		Mode:              diff.Mode,
		Git:               execGit{},
		Go:                execGo{},
		PerPackageTimeout: 60 * time.Second,
		TotalBudget:       120 * time.Second,
		ModuleRoot:        moduleRootOnDisk(diff.Repo.Root),
		LocalDeps:         localDepsOnDisk(diff.Repo.Root),
	}
	switch diff.Mode {
	case "commit":
		env.TipRev = diff.Range
	case "range":
		env.TipRev = rangeTip(diff.Range)
	}
	return env
}

// rangeTip is the right-hand side of "A..B" or "A...B", or HEAD when omitted.
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

// verifyCompileClaims discards Go compile-claim findings whose package vets
// cleanly, when the working tree holds exactly the reviewed code (FR-6).
func verifyCompileClaims(ctx context.Context, findings []Finding, env TreeEnv) (kept []Finding, discarded []Discard) {
	kept = make([]Finding, 0, len(findings))
	discarded = []Discard{}
	vetResult := map[string]error{}     // per directory, memoised
	treeMatch := map[string]bool{}      // per module, memoised
	buildFiles := map[string][]string{} // per directory, memoised
	var spent time.Duration
	for _, f := range findings {
		if !isCompileClaim(f) {
			kept = append(kept, f)
			continue
		}
		file := primaryLoc(f).Path
		dir := path.Dir(file)
		mod := "."
		if env.ModuleRoot != nil {
			mod = env.ModuleRoot(dir)
		}
		matches, known := treeMatch[mod]
		if !known && env.RepoRoot != "" && env.Go != nil && env.Go.Available() {
			matches = treeMatches(ctx, env, mod)
			treeMatch[mod] = matches
		}
		if matches {
			// A file excluded by build constraints is not compiled by vet, so a
			// clean vet says nothing about it. go list runs under the same
			// per-package timeout and total budget as vet, once per directory.
			files, listed := buildFiles[dir]
			if !listed {
				if env.TotalBudget > 0 && spent >= env.TotalBudget {
					files = nil
				} else {
					lctx, cancel := context.WithTimeout(ctx, stepTimeout(env, spent))
					start := time.Now()
					files, _ = env.Go.PackageFiles(lctx, filepath.Join(env.RepoRoot, filepath.FromSlash(dir)))
					spent += time.Since(start)
					cancel()
				}
				buildFiles[dir] = files
			}
			matches = slices.Contains(files, path.Base(file))
		}
		if !matches {
			f.Tags = addTag(f.Tags, TagUnverified)
			kept = append(kept, f)
			continue
		}
		err, memo := vetResult[dir]
		if !memo {
			if env.TotalBudget > 0 && spent >= env.TotalBudget {
				f.Tags = addTag(f.Tags, TagUnverified)
				kept = append(kept, f)
				continue
			}
			vctx, cancel := context.WithTimeout(ctx, stepTimeout(env, spent))
			start := time.Now()
			err = env.Go.Vet(vctx, filepath.Join(env.RepoRoot, filepath.FromSlash(dir)))
			spent += time.Since(start)
			if vctx.Err() != nil {
				err = vctx.Err()
			}
			cancel()
			vetResult[dir] = err
		}
		switch {
		case err == nil:
			discarded = append(discarded, Discard{Finding: f, Reason: "package compiles: go vet passed in " + dir})
		case errors.Is(err, context.DeadlineExceeded):
			f.Tags = addTag(f.Tags, TagUnverified)
			kept = append(kept, f)
		default:
			f.Tags = addTag(f.Tags, TagVetFailed)
			kept = append(kept, f)
		}
	}
	return kept, discarded
}

// treeMatches reports whether the working tree holds exactly the reviewed
// version of the Go module rooted at mod (repo-relative), per the FR-6 table.
// go vet compiles the package and everything it imports, and the build can
// read cgo and assembly sources and go:embed assets, so every tracked file in
// the module must match the reviewed version. No untracked compile input may
// be present either, ignored ones included: vet compiles an ignored generated
// file just the same.
func treeMatches(ctx context.Context, env TreeEnv, mod string) bool {
	tracked := trackedInputPathspecs(mod)
	clean := func(args ...string) bool {
		_, err := env.Git.Run(ctx, env.RepoRoot, append(args, tracked...)...)
		return err == nil
	}
	noUntracked := func() bool {
		// Any untracked file the repo does not ignore, anywhere in the module,
		// could be an embed asset or source: no match.
		out, err := env.Git.Run(ctx, env.RepoRoot, "ls-files", "--others", "--exclude-standard", "--", trackedInputPathspecs(mod)[0])
		if err != nil || strings.TrimSpace(out) != "" {
			return false
		}
		// Ignored files count only when they are compile inputs (an ignored
		// generated .go file); build outputs such as binaries do not.
		args := append([]string{"ls-files", "--others", "--ignored", "--exclude-standard", "--"}, untrackedInputPathspecs(mod)...)
		out, err = env.Git.Run(ctx, env.RepoRoot, args...)
		return err == nil && strings.TrimSpace(out) == ""
	}
	// A workspace, or a replace directive to a local directory, pulls in code
	// outside this module that the checks above do not cover.
	if env.LocalDeps != nil && env.LocalDeps(mod) {
		return false
	}
	switch env.Mode {
	case "unstaged", "codebase":
		// Both review the working tree itself.
		return noUntracked()
	case "staged":
		return noUntracked() && clean("diff", "--quiet", "--")
	case "commit", "range":
		// The working tree matches the reviewed tip for this module, whether
		// or not the tip is HEAD.
		return env.TipRev != "" && noUntracked() && clean("diff", "--quiet", env.TipRev, "--")
	default: // snippet, or anything unknown
		return false
	}
}

// modPrefix is mod as a pathspec prefix ("" for the repo root).
func modPrefix(mod string) string {
	if mod == "." || mod == "" {
		return ""
	}
	return mod + "/"
}

// trackedInputPathspecs covers every file in the module, plus a workspace
// file at the repo root.
func trackedInputPathspecs(mod string) []string {
	p := modPrefix(mod)
	whole := ":(glob)" + p + "**"
	if p == "" {
		whole = ":(glob)**"
	}
	return []string{whole, ":(glob)go.work", ":(glob)go.work.sum"}
}

// untrackedInputPathspecs covers the untracked files that can change whether
// the module compiles. Other untracked files (notes, scratch output) do not.
func untrackedInputPathspecs(mod string) []string {
	p := modPrefix(mod)
	var out []string
	for _, ext := range []string{"go", "c", "h", "s", "S", "syso"} {
		out = append(out, ":(glob)"+p+"**/*."+ext)
	}
	return append(out, ":(glob)"+p+"**/go.mod", ":(glob)go.work")
}

// localDepsOnDisk reports a go.work at the repo root, or a go.mod replace
// directive whose target is a local path ("./" or "../").
func localDepsOnDisk(repoRoot string) func(mod string) bool {
	return func(mod string) bool {
		if _, err := os.Stat(filepath.Join(repoRoot, "go.work")); err == nil {
			return true
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(mod), "go.mod"))
		if err != nil {
			return false
		}
		return localReplaceRE.Match(data)
	}
}

var localReplaceRE = regexp.MustCompile(`(?m)=>\s*\.\.?/`)

// moduleRootOnDisk finds the nearest directory at or above dir holding a
// go.mod, within the repository; "." when none is found below the root.
func moduleRootOnDisk(repoRoot string) func(dir string) string {
	return func(dir string) string {
		for d := dir; ; d = path.Dir(d) {
			if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(d), "go.mod")); err == nil {
				return d
			}
			if d == "." || d == "/" || d == "" {
				return "."
			}
		}
	}
}

type execGit struct{}

func (execGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

type execGo struct{}

func (execGo) Available() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

func (execGo) PackageFiles(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-f",
		`{{join .GoFiles " "}} {{join .CgoFiles " "}} {{join .TestGoFiles " "}} {{join .XTestGoFiles " "}}`, ".")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func (execGo) Vet(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "go", "vet", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go vet: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StampCommit sets the commit on each discarded finding's locations, as the
// per-commit range review does for kept findings, so a discard in an
// aggregate report can be traced to the revision its lines refer to.
func StampCommit(ds []Discard, commit string) []Discard {
	out := make([]Discard, len(ds))
	for i, d := range ds {
		d.Finding.Locations = slices.Clone(d.Finding.Locations)
		for j := range d.Finding.Locations {
			d.Finding.Locations[j].Commit = commit
		}
		out[i] = d
	}
	return out
}

// stepTimeout is the per-package timeout, cut to what is left of the total
// budget so a late command cannot overrun it.
func stepTimeout(env TreeEnv, spent time.Duration) time.Duration {
	if env.TotalBudget <= 0 {
		return env.PerPackageTimeout
	}
	return min(env.PerPackageTimeout, env.TotalBudget-spent)
}
