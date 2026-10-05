package review

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dshills/prism/internal/providers"
)

// The schema asks for exactly the fields rawFinding reads, so the two cannot
// drift apart (provider and model are stamped by prism, not the model).
func TestFindingSchema_MatchesRawFinding(t *testing.T) {
	var want []string
	rt := reflect.TypeOf(rawFinding{})
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "provider" && name != "model" {
			want = append(want, name)
		}
	}
	var got []string
	for _, p := range findingSchema().Properties {
		got = append(got, p.Name)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("schema fields %v, rawFinding fields %v", got, want)
	}

	for _, p := range findingSchema().Properties {
		if p.Name == "category" && len(p.Schema.Enum) != len(knownCategories) {
			t.Errorf("category enum = %v, want every known category", p.Schema.Enum)
		}
	}
}

func TestParseFindings_StructuredObject(t *testing.T) {
	got, err := parseFindings(`{"findings":[{"severity":"high","category":"bug","title":"T","message":"m","suggestion":"s","confidence":0.9,"path":"a.go","startLine":1,"endLine":1,"evidence":"x","tags":[]}]}`)
	if err != nil || len(got) != 1 || got[0].Title != "T" {
		t.Fatalf("parseFindings = %v, %v", got, err)
	}
	if got, err := parseFindings(`{"findings":[]}`); err != nil || len(got) != 0 {
		t.Errorf("empty findings object: %v, %v", got, err)
	}
	if _, err := parseFindings(`{"results":[]}`); err == nil {
		t.Error("an object without findings should be an error")
	}
}

// outputRecorder answers with the structured object and records what each
// request asked for.
type outputRecorder struct {
	mu      sync.Mutex
	outputs []*providers.Output
}

func (r *outputRecorder) Review(_ context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	r.mu.Lock()
	r.outputs = append(r.outputs, req.Output)
	r.mu.Unlock()
	return providers.ReviewResponse{Content: `{"findings":[]}`}, nil
}

func (r *outputRecorder) Name() string { return "mock" }

// Every review request asks for structured output, single and chunked.
func TestRun_RequestsStructuredOutput(t *testing.T) {
	for name, chunkBytes := range map[string]int{"single": 0, "chunked": 120} {
		t.Run(name, func(t *testing.T) {
			rec := &outputRecorder{}
			useProvider(t, rec)
			cfg := chunkTestConfig(t)
			cfg.ChunkBytes = chunkBytes
			if _, err := Run(context.Background(), threeChunkDiff("beta"), cfg); err != nil {
				t.Fatal(err)
			}
			if len(rec.outputs) == 0 {
				t.Fatal("no requests")
			}
			for i, o := range rec.outputs {
				if o != findingsOutput {
					t.Errorf("request %d: Output = %v, want findingsOutput", i, o)
				}
			}
		})
	}
}
