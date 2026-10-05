package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// effortOf is the reasoning effort a recorded request carried, as each
// provider sends it, or "" when it carried none.
func effortOf(provider string, body map[string]any) string {
	var v any
	switch provider {
	case "anthropic":
		if oc, ok := body["output_config"].(map[string]any); ok {
			v = oc["effort"]
		}
	case "gemini":
		if gc, ok := body["generationConfig"].(map[string]any); ok {
			if tc, ok := gc["thinkingConfig"].(map[string]any); ok {
				v = tc["thinkingLevel"]
			}
		}
	default:
		v = body["reasoning_effort"]
	}
	s, _ := v.(string)
	return s
}

// answers is each provider's minimal successful response body.
var answers = map[string]string{
	"openai":    `{"choices":[{"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`,
	"ollama":    `{"choices":[{"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`,
	"anthropic": `{"content":[{"type":"text","text":"[]"}],"stop_reason":"end_turn"}`,
	"gemini":    `{"candidates":[{"content":{"parts":[{"text":"[]"}]},"finishReason":"STOP"}]}`,
}

func newTestProvider(name string, srv *httptest.Server) Reviewer {
	rewrite := &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, baseURL: srv.URL}}
	switch name {
	case "openai":
		return &OpenAI{apiKey: "k", model: "gpt", baseURL: srv.URL, client: srv.Client()}
	case "ollama":
		return &Ollama{model: "m", baseURL: srv.URL, client: srv.Client()}
	case "anthropic":
		return &Anthropic{apiKey: "k", model: "c", client: rewrite}
	default:
		return &Gemini{apiKey: "k", model: "g", client: rewrite}
	}
}

var providerNames = []string{"openai", "ollama", "anthropic", "gemini"}

// Each provider sends the effort as its own setting, next to structured
// output, and sends none when none is asked for.
func TestEffort_SentAsEachProvidersSetting(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			rs := &recordingServer{}
			srv := rs.start(t, func(_ map[string]any, w http.ResponseWriter) { _, _ = io.WriteString(w, answers[name]) })
			p := newTestProvider(name, srv)
			for _, effort := range []string{"low", ""} {
				if _, err := p.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput, Effort: effort}); err != nil {
					t.Fatal(err)
				}
			}
			reqs := rs.requests()
			if got := effortOf(name, reqs[0]); got != "low" {
				t.Errorf("effort sent = %q, want low (request %v)", got, reqs[0])
			}
			if got := effortOf(name, reqs[1]); got != "" {
				t.Errorf("no effort asked, sent %q", got)
			}
			if name == "anthropic" {
				if oc := reqs[0]["output_config"].(map[string]any); oc["format"] == nil {
					t.Errorf("effort replaced the structured output format: %v", oc)
				}
			}
		})
	}
}

// A model that refuses the effort is asked again without it, structured
// output kept, and is not sent it again.
func TestEffort_RefusedIsDroppedAndRemembered(t *testing.T) {
	for _, name := range providerNames {
		t.Run(name, func(t *testing.T) {
			word := map[string]string{"anthropic": "effort", "gemini": "thinking_level"}[name]
			if word == "" {
				word = "reasoning_effort"
			}
			rs := &recordingServer{}
			srv := rs.start(t, func(body map[string]any, w http.ResponseWriter) {
				if effortOf(name, body) != "" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: '`+word+`' is not supported with this model."}}`)
					return
				}
				_, _ = io.WriteString(w, answers[name])
			})
			p := newTestProvider(name, srv)
			for range 2 {
				if _, err := p.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput, Effort: "high"}); err != nil {
					t.Fatal(err)
				}
			}
			var sent []bool
			for _, b := range rs.requests() {
				sent = append(sent, effortOf(name, b) != "")
			}
			if !slices.Equal(sent, []bool{true, false, false}) {
				t.Errorf("requests with effort: %v, want only the first", sent)
			}
			for i, b := range rs.requests() {
				if !hasSchema(name, b) {
					t.Errorf("request %d dropped structured output for an effort refusal", i)
				}
			}
		})
	}
}

// A refusal is answered by dropping the effort, then the schema alone,
// then both. Only what the request that got
// through left out is not sent again.
func TestEffort_UnnamedRefusal(t *testing.T) {
	for _, c := range []struct {
		name       string
		refuse     func(schema, effort bool) bool
		want       [][2]bool // (schema, effort) of each request
		keepSchema bool
		keepEffort bool
	}{
		{"the effort was the problem", func(_, e bool) bool { return e },
			[][2]bool{{true, true}, {true, false}}, true, false},
		{"the schema was the problem", func(s, _ bool) bool { return s },
			[][2]bool{{true, true}, {true, false}, {false, true}}, false, true},
		{"both were", func(s, e bool) bool { return s || e },
			[][2]bool{{true, true}, {true, false}, {false, true}, {false, false}}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rs := &recordingServer{}
			srv := rs.start(t, func(body map[string]any, w http.ResponseWriter) {
				if c.refuse(body["response_format"] != nil, body["reasoning_effort"] != nil) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":"bad request"}`)
					return
				}
				openaiAnswer(w, "[]")
			})
			o := &Ollama{model: "m", baseURL: srv.URL, client: srv.Client()}
			if _, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput, Effort: "low"}); err != nil {
				t.Fatal(err)
			}
			var got [][2]bool
			for _, b := range rs.requests() {
				got = append(got, [2]bool{b["response_format"] != nil, b["reasoning_effort"] != nil})
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("requests (schema, effort) = %v, want %v", got, c.want)
			}
			if o.schema.use(testOutput) != c.keepSchema || o.effort.use("low") != c.keepEffort {
				t.Errorf("after: schema %v, effort %v; want %v, %v", o.schema.use(testOutput), o.effort.use("low"), c.keepSchema, c.keepEffort)
			}
		})
	}
}

// A refusal that dropping both fields does not fix is the error, and
// switches neither off.
func TestEffort_UnrelatedRefusalKeepsBoth(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(_ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"context too long"}`)
	})
	o := &OpenAI{apiKey: "k", model: "gpt", baseURL: srv.URL, client: srv.Client()}
	if _, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput, Effort: "low"}); err == nil {
		t.Fatal("no error")
	}
	if n := len(rs.requests()); n != 4 {
		t.Errorf("%d requests, want 4 (both, without effort, without schema, without either)", n)
	}
	if !o.schema.use(testOutput) || !o.effort.use("low") {
		t.Error("an unrelated refusal switched a field off")
	}
}

func hasSchema(provider string, body map[string]any) bool {
	switch provider {
	case "anthropic":
		oc, _ := body["output_config"].(map[string]any)
		return oc != nil && oc["format"] != nil
	case "gemini":
		gc, _ := body["generationConfig"].(map[string]any)
		return gc != nil && gc["responseSchema"] != nil
	default:
		return body["response_format"] != nil
	}
}

// A refused level is remembered as that level: another one is still sent.
func TestEffort_RefusalIsPerLevel(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(body map[string]any, w http.ResponseWriter) {
		if body["reasoning_effort"] == "minimal" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"unsupported value: minimal"}`)
			return
		}
		openaiAnswer(w, "[]")
	})
	o := &OpenAI{apiKey: "k", model: "gpt", baseURL: srv.URL, client: srv.Client()}
	for _, effort := range []string{"minimal", "minimal", "low"} {
		if _, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x", Effort: effort}); err != nil {
			t.Fatal(err)
		}
	}
	var sent []string
	for _, b := range rs.requests() {
		s, _ := b["reasoning_effort"].(string)
		sent = append(sent, s)
	}
	if !slices.Equal(sent, []string{"minimal", "", "", "low"}) {
		t.Errorf("efforts sent = %q, want minimal, then none, then low", sent)
	}
}

// A long system prompt is sent as a block marked for prompt caching; a
// short one as a plain string.
func TestAnthropic_SystemPromptCaching(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(_ map[string]any, w http.ResponseWriter) { _, _ = io.WriteString(w, answers["anthropic"]) })
	p := newTestProvider("anthropic", srv)
	long := strings.Repeat("review rule. ", anthropicCacheMinChars/10)
	for _, sys := range []string{"short", long} {
		if _, err := p.Review(context.Background(), ReviewRequest{SystemPrompt: sys, UserPrompt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	reqs := rs.requests()
	if reqs[0]["system"] != "short" {
		t.Errorf("short system = %v", reqs[0]["system"])
	}
	blocks, _ := reqs[1]["system"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("long system = %v", reqs[1]["system"])
	}
	b := blocks[0].(map[string]any)
	if b["text"] != long || b["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Errorf("block = %v", b)
	}
}
