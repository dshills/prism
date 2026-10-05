package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
)

const f1 = `{"severity":"high","category":"bug","title":"One","message":"m","suggestion":"s","confidence":0.9,"path":"a.go","startLine":1,"endLine":1}`
const f2 = `{"severity":"low","category":"style","title":"Two, with [brackets] and \"quotes\" }","message":"m","suggestion":"s","confidence":0.5,"path":"a.go","startLine":2,"endLine":2}`

func TestSalvageFindings(t *testing.T) {
	withTags := strings.Replace(f1, `"endLine":1`, `"endLine":1,"tags":[]`, 1)
	for _, c := range []struct {
		name     string
		content  string
		titles   []string
		lost     int
		salvaged bool
	}{
		{"prose around the array", "Here are the findings [see below]:\n[" + f1 + "]\nLet me know.", []string{"One"}, 0, true},
		{"wrapper with prose", "Result:\n{\"findings\": [" + f1 + "," + f2 + "]}\nDone", []string{"One", "Two, with [brackets] and \"quotes\" }"}, 0, true},
		{"trailing commas", "[" + strings.TrimSuffix(f1, "}") + ",}," + "]", []string{"One"}, 0, true},
		{"cut off in the last element", "[" + f1 + ", " + f2[:40], []string{"One"}, 1, true},
		{"cut off after an element", "[" + f1 + ",", []string{"One"}, 1, true}, // more may have followed
		{"a nested array beside a finding", "[" + f1 + ", [" + f2 + "]]", []string{"One"}, 1, true},
		{"scalars beside a finding", `[` + f1 + `, "stray", 42]`, []string{"One"}, 0, true},
		{"a finding and a non-finding object", `[` + f1 + `, {"error": "x"}]`, []string{"One"}, 1, true},
		{"scalar first, empty tags inside", `["stray", ` + withTags + `,]`, []string{"One"}, 0, true},
		{"example before the findings", "Example: []\nActual findings: [" + f1 + "]", []string{"One"}, 0, true},
		{"tags arrays inside findings", "Findings:\n[" + withTags + "]", []string{"One"}, 0, true},
		{"empty array in prose", "No issues found: []", nil, 0, false}, // never salvaged as clean
		{"no array", "I could not review this.", nil, 0, false},
		{"cut off in the first element", "[" + f2[:40], nil, 0, false},
		{"two arrays of findings", "[" + f1 + "]\nor maybe\n[" + f2 + "]", nil, 0, false},
		{"objects that are not findings", `[{}, {"error": "review failed"}]`, nil, 0, false},
		{"complete array then a cut-off one", "[" + f1 + "]\nand\n[" + f2[:40], nil, 0, false},
		{"unclosed bracket in prose", "Ranges like [1, 2) are fine.\n[" + f1 + "]", nil, 0, false},
		{"brackets inside an error string", `{"error":"Review failed; expected []"}`, nil, 0, false},
		{"array named inside a string", `{"note": "[` + strings.ReplaceAll(f1, `"`, `\"`) + `]"}`, nil, 0, false},
		{"stray brace between findings", "[" + f1 + ", }, " + f2 + "]", nil, 0, false},
		{"quote left open after the array", "[" + f1 + "]\n\"and then the rest was cut", nil, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw, s, ok := salvageFindings(c.content)
			if ok != c.salvaged {
				t.Fatalf("ok = %v, want %v", ok, c.salvaged)
			}
			if !ok {
				return
			}
			var titles []string
			for _, r := range raw {
				titles = append(titles, r.Title)
			}
			if strings.Join(titles, "|") != strings.Join(c.titles, "|") || s.lost != c.lost || s.repaired != 1 {
				t.Errorf("titles %q, lost %d, repaired %d; want %q, %d, 1", titles, s.lost, s.repaired, c.titles, c.lost)
			}
		})
	}
}

// Valid JSON is never counted as salvaged.
func TestParseReviewedSalvaged_ValidIsNotSalvaged(t *testing.T) {
	fs, s, err := parseReviewedSalvaged("["+f1+"]", "")
	if err != nil || len(fs) != 1 || s != (salvage{}) {
		t.Errorf("= %d findings, %+v, %v", len(fs), s, err)
	}
}

// scriptedReviewer answers each call with the next of its replies.
type scriptedReviewer struct {
	mu      sync.Mutex
	replies []string
	calls   int
}

func (r *scriptedReviewer) Review(context.Context, providers.ReviewRequest) (providers.ReviewResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reply := r.replies[min(r.calls, len(r.replies)-1)]
	r.calls++
	return providers.ReviewResponse{Content: reply, Provider: "mock", Model: "m"}, nil
}

func (r *scriptedReviewer) Name() string { return "mock" }

// A response repaired locally with nothing lost needs no second call. One
// that lost findings asks the model to repair it; if that does no better,
// the usable findings are kept and the loss makes the review incomplete,
// and it is not cached.
func TestRun_SalvageInsteadOfRepairCall(t *testing.T) {
	finding := strings.Replace(f1, `"path":"a.go"`, `"path":"d1/a.go"`, 1)
	cutOff := "[" + finding + ", {\"severity\":\"lo"
	for _, c := range []struct {
		name            string
		replies         []string
		calls           int
		salvaged        int
		complete        bool
		callsAfterRerun int
	}{
		{"prose", []string{"Sure!\n[" + finding + "]"}, 1, 1, true, 0},
		{"cut off, repaired by the model", []string{cutOff, "[" + finding + "]"}, 2, 0, true, 0},
		{"cut off, repair no better", []string{cutOff, cutOff}, 2, 1, false, 2}, // the kept answer is the one counted
	} {
		t.Run(c.name, func(t *testing.T) {
			rev := &scriptedReviewer{replies: c.replies}
			useProvider(t, rev)
			cfg := chunkTestConfig(t)
			diff := threeChunkDiff("beta")
			diff.Diff, diff.Files = fileDiff("d1/a.go", "alpha"), diff.Files[:1]
			report, err := Run(context.Background(), diff, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if rev.calls != c.calls {
				t.Errorf("%d model calls, want %d", rev.calls, c.calls)
			}
			if cov := report.Coverage; cov.Salvaged != c.salvaged || cov.Complete != c.complete {
				t.Errorf("coverage salvaged %d, complete %v; want %d, %v (skipped %v)", cov.Salvaged, cov.Complete, c.salvaged, c.complete, cov.Skipped)
			}
			if len(report.Findings) != 1 {
				t.Errorf("%d findings, want 1", len(report.Findings))
			}
			rev.calls = 0
			if _, err := Run(context.Background(), diff, cfg); err != nil {
				t.Fatal(err)
			}
			if rev.calls != c.callsAfterRerun {
				t.Errorf("rerun made %d calls, want %d", rev.calls, c.callsAfterRerun)
			}
		})
	}
}

// A repair answer cut off at its output limit is passed on as a cut-off, so
// the part is split or asked again with a larger limit, rather than
// settling for a lossy local repair.
func TestAsk_TruncatedRepairIsPassedOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "not valid JSON") {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"[{\"sev"},"finish_reason":"length"}]}`)
			return
		}
		content, _ := json.Marshal("[" + f1 + ", {\"severity\":\"lo")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}]}`, content)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)
	p, err := providers.NewOllama("m")
	if err != nil {
		t.Fatal(err)
	}
	pr := &partReviewer{provider: p, cfg: config.Default(), builder: defaultPromptBuilder}
	_, err = pr.ask(context.Background(), part{diff: fileDiff("a.go", "x"), files: []string{"a.go"}}, 100)
	if !providers.IsTruncated(err) {
		t.Errorf("err = %v, want the repair's cut-off", err)
	}
}
