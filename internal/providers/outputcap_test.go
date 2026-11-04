package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func TestRefusedOutputLimit(t *testing.T) {
	for _, c := range []struct {
		name      string
		err       error
		requested int
		want      int
		ok        bool
	}{
		{"anthropic", &requestError{status: 400, body: `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 65536 > 64000, which is the maximum allowed number of output tokens for claude-sonnet-4-5-20250929"}}`},
			65536, 64000, true},
		{"openai", &requestError{status: 400, body: `{"error":{"message":"max_tokens is too large: 65536. This model supports at most 32768 completion tokens, whereas you provided 65536.","param":"max_tokens"}}`},
			65536, 32768, true},
		{"openai max_completion_tokens", &requestError{status: 400, body: `{"error":{"message":"max_completion_tokens is too large: 32768. This model supports at most 16,384 completion tokens"}}`},
			32768, 16384, true},
		{"gemini exclusive bound", &requestError{status: 400, body: `{"error":{"message":"The specified maxOutputTokens value is out of range: supported range is [1, 8193)"}}`},
			32768, 8192, true},
		// Only a range's end is exclusive, not any number before a ')'.
		{"parenthesized number", &requestError{status: 400, body: `{"error":{"message":"max_output_tokens is limited to 8192 (context window 131072)"}}`},
			65536, 8192, true},
		{"parenthesized cap", &requestError{status: 400, body: `{"error":{"message":"max_tokens too large (at most 16384)"}}`},
			32768, 16384, true},
		{"unrelated 400 with numbers", &requestError{status: 400, body: `{"error":{"message":"prompt is too long: 210000 tokens > 200000 maximum"}}`},
			32768, 0, false},
		{"names the field but no smaller number", &requestError{status: 400, body: `{"error":{"message":"max_tokens must be an integer"}}`},
			32768, 0, false},
		{"server error", &requestError{status: 500, body: `max_tokens: 65536 > 64000`}, 65536, 0, false},
		{"not a request error", errors.New("max_tokens: 65536 > 64000"), 65536, 0, false},
		{"nil", nil, 65536, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := refusedOutputLimit(c.err, c.requested)
			if got != c.want || ok != c.ok {
				t.Errorf("refusedOutputLimit = %d, %v; want %d, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestOutputCap_Limit(t *testing.T) {
	var c outputCap
	if got := c.limit(65536); got != 65536 {
		t.Errorf("no cap learned: %d, want 65536", got)
	}
	c.max.Store(32768)
	for _, tc := range []struct{ in, want int }{{65536, 32768}, {32768, 32768}, {4096, 4096}, {0, 0}} {
		if got := c.limit(tc.in); got != tc.want {
			t.Errorf("limit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// A model that refuses the output limit is asked again at the cap it names,
// with structured output and effort still on, and later requests start at
// that cap.
func TestOutputCap_LearnedFromRefusal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		cap   int
		make  func(rs *recordingServer, t *testing.T) Reviewer
	}{
		{"openai", "max_tokens", 32768, func(rs *recordingServer, t *testing.T) Reviewer {
			srv := rs.start(t, func(body map[string]any, w http.ResponseWriter) {
				if n := int(body["max_tokens"].(float64)); n > 32768 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprintf(w, `{"error":{"message":"max_tokens is too large: %d. This model supports at most 32768 completion tokens"}}`, n)
					return
				}
				openaiAnswer(w, `{"findings":[]}`)
			})
			return &OpenAI{apiKey: "k", model: "gpt-4.1-mini", baseURL: srv.URL, client: srv.Client()}
		}},
		{"anthropic", "max_tokens", 64000, func(rs *recordingServer, t *testing.T) Reviewer {
			srv := rs.start(t, func(body map[string]any, w http.ResponseWriter) {
				if n := int(body["max_tokens"].(float64)); n > 64000 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: %d > 64000, which is the maximum allowed number of output tokens for claude-test"}}`, n)
					return
				}
				_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"findings\":[]}"}],"usage":{"input_tokens":5,"output_tokens":5}}`)
			})
			return &Anthropic{apiKey: "k", model: "claude-test", client: &http.Client{
				Transport: &rewriteTransport{base: srv.Client().Transport, baseURL: srv.URL},
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := &recordingServer{}
			r := tc.make(rs, t)
			req := ReviewRequest{UserPrompt: "x", MaxTokens: 65536, Output: testOutput, Effort: "high"}
			if resp, err := r.Review(context.Background(), req); err != nil || resp.Content != `{"findings":[]}` {
				t.Fatalf("Review = %q, %v", resp.Content, err)
			}
			reqs := rs.requests()
			if len(reqs) != 2 {
				t.Fatalf("%d requests, want 2 (refused, then at the cap)", len(reqs))
			}
			if got := int(reqs[1][tc.field].(float64)); got != tc.cap {
				t.Errorf("retry asked for %d, want %d", got, tc.cap)
			}
			if !hasStructuredOutput(reqs[1]) {
				t.Error("the retry dropped structured output: a refused limit is not a refused schema")
			}

			if _, err := r.Review(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			reqs = rs.requests()
			if len(reqs) != 3 || int(reqs[2][tc.field].(float64)) != tc.cap {
				t.Errorf("the next review should start at the learned cap in one request; got %d requests", len(reqs))
			}
		})
	}
}

// An endpoint that refuses the effort, then the limit of the request sent
// without it, keeps structured output: the limit refusal ends sendOptional's
// attempts, sendCapped asks again within the cap, and only the effort is
// switched off.
func TestOutputCap_RefusalAfterEffortRefusal(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(body map[string]any, w http.ResponseWriter) {
		switch {
		case body["reasoning_effort"] != nil:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unrecognized request argument: reasoning_effort"}}`)
		case int(body["max_tokens"].(float64)) > 32768:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"max_tokens is too large. This model supports at most 32768 completion tokens"}}`)
		default:
			openaiAnswer(w, `{"findings":[]}`)
		}
	})
	o := &OpenAI{apiKey: "k", model: "m", baseURL: srv.URL, client: srv.Client()}
	req := ReviewRequest{UserPrompt: "x", MaxTokens: 65536, Output: testOutput, Effort: "high"}
	if _, err := o.Review(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for i, body := range rs.requests() {
		if !hasStructuredOutput(body) {
			t.Errorf("request %d dropped structured output", i+1)
		}
	}
	if !o.schema.use(testOutput) || o.effort.use("high") {
		t.Errorf("want the schema kept and the effort switched off; schema %v, effort %v", o.schema.use(testOutput), o.effort.use("high"))
	}
}

// hasStructuredOutput reports whether a request body asks for structured
// output in either provider's form.
func hasStructuredOutput(body map[string]any) bool {
	if _, ok := body["response_format"]; ok {
		return true
	}
	oc, ok := body["output_config"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = oc["format"]
	return ok
}
