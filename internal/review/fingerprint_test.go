package review

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func evidenceFinding(path string, category Category, title string, line int, evidence string) Finding {
	return Finding{
		Severity: SeverityMedium,
		Category: category,
		Title:    title,
		Evidence: evidence,
		Locations: []Location{
			{Path: path, Lines: LineRange{Start: line, End: line}},
		},
	}
}

// addedSection is a diff section adding lines to path, starting at new-file
// line start.
func addedSection(path string, start int, lines ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -%d,0 +%d,%d @@\n", path, path, path, path, start, start, len(lines))
	for _, l := range lines {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

// twoReturns has the same line, `return err`, in two functions.
var twoReturns = []string{
	"func load() error {",
	"	if err := read(); err != nil {",
	"		return err",
	"	}",
	"	return nil",
	"}",
	"func save() error {",
	"	if err := write(); err != nil {",
	"		return err",
	"	}",
	"	return nil",
	"}",
}

func identified(reviewed string, fs ...Finding) []Finding {
	identifyFindings(fs, reviewed)
	return fs
}

// The same issue reported again keeps its ID even when the model rewords the
// title, the lines move, or the evidence is quoted with a diff marker or other
// whitespace.
func TestGenerateFindingID_StableAcrossRuns(t *testing.T) {
	want := generateFindingID(evidenceFinding("db/q.go", CategorySecurity, "SQL injection", 42, `rows, err := db.Query("SELECT * FROM t WHERE id = " + id)`))
	for name, f := range map[string]Finding{
		"reworded title": evidenceFinding("db/q.go", CategorySecurity, "Query built by string concatenation", 42, `rows, err := db.Query("SELECT * FROM t WHERE id = " + id)`),
		"lines moved":    evidenceFinding("db/q.go", CategorySecurity, "SQL injection", 57, `rows, err := db.Query("SELECT * FROM t WHERE id = " + id)`),
		"diff marker":    evidenceFinding("db/q.go", CategorySecurity, "SQL injection", 42, `+	rows, err := db.Query("SELECT * FROM t WHERE id = " + id)`),
		"whitespace":     evidenceFinding("db/q.go", CategorySecurity, "SQL injection", 42, "  rows,  err := db.Query(\"SELECT * FROM t WHERE id = \" +   id)\n"),
		"category case":  evidenceFinding("db/q.go", "Security", "SQL injection", 42, `rows, err := db.Query("SELECT * FROM t WHERE id = " + id)`),
	} {
		if got := generateFindingID(f); got != want {
			t.Errorf("%s: ID changed", name)
		}
	}
}

// What the ID does depend on: the file, the category and the quoted code.
func TestGenerateFindingID_DistinguishesIssues(t *testing.T) {
	base := evidenceFinding("a.go", CategoryBug, "t", 1, "x := f()")
	id := generateFindingID(base)
	for name, f := range map[string]Finding{
		"path":     evidenceFinding("b.go", CategoryBug, "t", 1, "x := f()"),
		"category": evidenceFinding("a.go", CategoryPerformance, "t", 1, "x := f()"),
		"evidence": evidenceFinding("a.go", CategoryBug, "t", 1, "x := g()"),
	} {
		if generateFindingID(f) == id {
			t.Errorf("ID ignores the %s", name)
		}
	}
	if len(id) != 16 {
		t.Errorf("ID length = %d, want 16", len(id))
	}
}

// A finding with no evidence keeps the earlier scheme, so its ID is what it
// was before fingerprints: sha256(path:title:startLine).
func TestGenerateFindingID_NoEvidenceKeepsLegacyID(t *testing.T) {
	f := evidenceFinding("main.go", CategoryBug, "Nil map write", 10, "")
	h := sha256.Sum256([]byte("main.go:Nil map write:10"))
	if got, want := generateFindingID(f), fmt.Sprintf("%x", h[:8]); got != want {
		t.Errorf("ID = %s, want legacy %s", got, want)
	}
	if blank := evidenceFinding("main.go", CategoryBug, "Nil map write", 10, " \n\t"); generateFindingID(blank) != generateFindingID(f) {
		t.Error("blank evidence should count as no evidence")
	}
}

// The same code in two places gets two IDs, told apart by the code around
// it, and each keeps its ID when the lines move or the title is reworded.
func TestIdentifyFindings_SameCodeInTwoPlaces(t *testing.T) {
	diff := addedSection("a.go", 1, twoReturns...)
	got := identified(diff,
		evidenceFinding("a.go", CategoryBug, "Error not wrapped", 3, "return err"),
		evidenceFinding("a.go", CategoryBug, "Error not wrapped", 9, "return err"),
	)
	if got[0].ID == got[1].ID {
		t.Fatal("return err in load and in save share an ID")
	}

	// Ten lines added above the function: the same code, lower down.
	moved := addedSection("a.go", 11, twoReturns...)
	again := identified(moved,
		evidenceFinding("a.go", CategoryBug, "save drops the error context", 19, "return err"),
		evidenceFinding("a.go", CategoryBug, "load returns a bare error", 13, "return err"),
	)
	if again[1].ID != got[0].ID || again[0].ID != got[1].ID {
		t.Error("IDs changed when the lines moved and the titles were reworded")
	}
}

// The anchor is the enclosing declaration: edits inside the function leave
// the ID alone, renaming the function changes it.
func TestIdentifyFindings_AnchorIsTheDeclaration(t *testing.T) {
	f := evidenceFinding("a.go", CategoryBug, "t", 3, "return err")
	before := identified(addedSection("a.go", 1, twoReturns...), f)[0].ID

	edited := append([]string(nil), twoReturns...)
	edited[1] = "	if err := readAll(); err != nil {" // the line right above it
	edited[6] = "func saveAll() error {"             // another function
	if after := identified(addedSection("a.go", 1, edited...), f)[0].ID; after != before {
		t.Error("an edit inside the function changed the ID")
	}
	edited[0] = "func loadAll() error {"
	if after := identified(addedSection("a.go", 1, edited...), f)[0].ID; after == before {
		t.Error("renaming the enclosing function kept the ID")
	}
}

// The ID does not depend on how much context the diff carries: the same
// finding gets the same ID whether its hunk holds the whole function or
// starts below the declaration, which git then names in the hunk header.
func TestIdentifyFindings_IndependentOfDiffContext(t *testing.T) {
	f := evidenceFinding("a.go", CategoryBug, "t", 3, "return err")
	whole := identified(addedSection("a.go", 1, twoReturns[:6]...), f)[0].ID

	narrow := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -2,3 +2,3 @@ func load() error {\n" +
		" 	if err := read(); err != nil {\n" +
		"+		return err\n" +
		" 	}\n"
	if got := identified(narrow, f)[0].ID; got != whole {
		t.Error("a narrower hunk gave a different ID")
	}
}

// Git keeps only the first 80 bytes of a declaration in a hunk header, so a
// long signature found in the hunk is cut the same way.
func TestIdentifyFindings_LongSignature(t *testing.T) {
	sig := "func (s *Server) handleRequestWithRetries(ctx context.Context, req *Request, attempts int) (*Response, error) {"
	f := evidenceFinding("a.go", CategoryBug, "t", 3, "return nil, err")
	inHunk := identified(addedSection("a.go", 1, sig, "	if err := s.do(ctx, req); err != nil {", "		return nil, err", "	}"), f)[0].ID

	header := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -2,3 +2,3 @@ " + sig[:gitHeadingMax] + "\n" +
		" 	if err := s.do(ctx, req); err != nil {\n" +
		"+		return nil, err\n" +
		" 	}\n"
	if got := identified(header, f)[0].ID; got != inHunk {
		t.Error("a long signature in the hunk and in the header anchor differently")
	}
}

// The same code twice in one function shares an ID; both findings are kept,
// as they are on different lines.
func TestIdentifyFindings_SameCodeTwiceInAFunction(t *testing.T) {
	diff := addedSection("a.go", 1,
		"func both() error {",
		"	if err := a(); err != nil {",
		"		return err",
		"	}",
		"	if err := b(); err != nil {",
		"		return err",
		"	}",
		"	return nil",
		"}",
	)
	got := identified(diff,
		evidenceFinding("a.go", CategoryBug, "Error not wrapped", 3, "return err"),
		evidenceFinding("a.go", CategoryBug, "Error not wrapped", 6, "return err"),
	)
	if got[0].ID != got[1].ID {
		t.Error("the same code in one function should share an ID")
	}
	if kept := DeduplicateFindings(got); len(kept) != 2 {
		t.Errorf("kept %d findings, want both", len(kept))
	}
}

// A file's IDs do not depend on how the diff was chunked: the file reviewed
// alone and inside a larger diff give the same ID.
func TestIdentifyFindings_SameIDChunkedOrWhole(t *testing.T) {
	section := addedSection("a.go", 1, twoReturns...)
	whole := addedSection("b.go", 1, "package b") + section + addedSection("c.go", 1, "package c")
	f := evidenceFinding("a.go", CategoryBug, "t", 9, "return err")
	if identified(section, f)[0].ID != identified(whole, f)[0].ID {
		t.Error("chunked and whole reviews give different IDs")
	}
}

// Two findings on the same code in the same category share an ID, since
// nothing stable tells them apart. Deduplication keeps both when their titles
// differ, and drops a finding repeated with the same title.
func TestIdentifyFindings_SameCodeShareIDAndBothKept(t *testing.T) {
	diff := addedSection("a.go", 1, twoReturns...)
	got := identified(diff,
		evidenceFinding("a.go", CategoryBug, "Error not wrapped", 3, "return err"),
		evidenceFinding("a.go", CategoryBug, "Error swallowed by caller", 3, "return err"),
		evidenceFinding("a.go", CategoryBug, "Error not wrapped", 3, "return err"),
	)
	if got[0].ID != got[1].ID {
		t.Fatal("two findings on the same code and category got different IDs")
	}
	kept := DeduplicateFindings(got)
	if len(kept) != 2 || kept[0].Title == kept[1].Title {
		t.Errorf("kept %d findings, want the two distinct titles", len(kept))
	}
}

// The same issue reported twice (in two responses, quoted differently) gets
// one ID, so the merge keeps one.
func TestParseReviewedFindings_SameIssueAcrossResponses(t *testing.T) {
	diff := addedSection("a.go", 1, "func name(p *P) string {", "	return p.Name", "}")
	r1 := `[{"severity":"high","category":"bug","title":"Nil dereference","message":"m","suggestion":"s","confidence":0.9,"path":"a.go","startLine":2,"endLine":2,"evidence":"return p.Name","tags":[]}]`
	r2 := `[{"severity":"high","category":"bug","title":"Nil dereference","message":"m","suggestion":"s","confidence":0.8,"path":"a.go","startLine":2,"endLine":2,"evidence":"+\treturn p.Name","tags":[]}]`
	f1, err := parseReviewedFindings(r1, diff)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := parseReviewedFindings(r2, diff)
	if err != nil {
		t.Fatal(err)
	}
	if f1[0].ID != f2[0].ID {
		t.Fatal("the same issue got different IDs in two responses")
	}
	if f1[0].ID == generateFindingID(f1[0]) {
		t.Error("the anchor was not applied")
	}
	if got := mergeChunkFindings([][]Finding{f1, f2}); len(got) != 1 {
		t.Errorf("merged %d findings, want 1", len(got))
	}
}

// Findings replayed from the cache get the IDs they had when fresh.
func TestFindingIDs_SurviveCacheRoundTrip(t *testing.T) {
	diff := addedSection("a.go", 1, twoReturns...)
	fresh, err := parseReviewedFindings(`[
		{"severity":"low","category":"bug","title":"A","message":"m","suggestion":"s","confidence":0.5,"path":"a.go","startLine":3,"endLine":3,"evidence":"return err","tags":[]},
		{"severity":"low","category":"bug","title":"B","message":"m","suggestion":"s","confidence":0.5,"path":"a.go","startLine":9,"endLine":9,"evidence":"return err","tags":[]}
	]`, diff)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(findingsToRaw(fresh))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := parseReviewedFindings(string(stored), diff)
	if err != nil {
		t.Fatal(err)
	}
	for i := range fresh {
		if fresh[i].ID != replayed[i].ID {
			t.Errorf("finding %d: ID %s fresh, %s from cache", i, fresh[i].ID, replayed[i].ID)
		}
	}
}

// A severity override changes the severity, not the identity.
func TestApplySeverityOverrides_KeepsID(t *testing.T) {
	findings := identified(addedSection("a.go", 1, "x"), evidenceFinding("a.go", CategoryStyle, "t", 1, "x"))
	id := findings[0].ID
	got := ApplySeverityOverrides(findings, &Rules{SeverityOverrides: map[string]string{"style": "low"}})
	if got[0].ID != id || got[0].Severity != SeverityLow {
		t.Errorf("after override: ID %s severity %s, want ID %s severity low", got[0].ID, got[0].Severity, id)
	}
}
