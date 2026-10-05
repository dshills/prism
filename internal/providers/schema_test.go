package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

var testOutput = &Output{
	Name:        "report_findings",
	Description: "Report findings.",
	Schema: &Schema{Type: "object", Properties: []Property{
		{Name: "findings", Schema: &Schema{Type: "array", Items: &Schema{Type: "object", Properties: []Property{
			{Name: "severity", Schema: &Schema{Type: "string", Enum: []string{"low", "high"}}},
			{Name: "line", Schema: &Schema{Type: "integer"}},
		}}}},
	}},
}

// OpenAI's strict mode needs every property required and no extra ones, on
// every object, nested or not.
func TestSchema_JSONSchemaStrict(t *testing.T) {
	root := testOutput.Schema.jsonSchema(true)
	item := root["properties"].(map[string]any)["findings"].(map[string]any)["items"].(map[string]any)
	for name, obj := range map[string]map[string]any{"root": root, "item": item} {
		if obj["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties = %v, want false", name, obj["additionalProperties"])
		}
	}
	if got := item["required"].([]string); !slices.Equal(got, []string{"severity", "line"}) {
		t.Errorf("required = %v, want every property", got)
	}
	sev := item["properties"].(map[string]any)["severity"].(map[string]any)
	if sev["type"] != "string" || !slices.Equal(sev["enum"].([]string), []string{"low", "high"}) {
		t.Errorf("severity = %v", sev)
	}
	if _, ok := testOutput.Schema.jsonSchema(false)["additionalProperties"]; ok {
		t.Error("non-strict schemas should not set additionalProperties")
	}
}

// Gemini takes OpenAPI-style upper-case types and an explicit order.
func TestSchema_Gemini(t *testing.T) {
	root := testOutput.Schema.geminiSchema()
	item := root["properties"].(map[string]any)["findings"].(map[string]any)["items"].(map[string]any)
	if root["type"] != "OBJECT" || item["type"] != "OBJECT" || item["properties"].(map[string]any)["line"].(map[string]any)["type"] != "INTEGER" {
		t.Errorf("types not upper-case: %v", root)
	}
	if got := item["propertyOrdering"].([]string); !slices.Equal(got, []string{"severity", "line"}) {
		t.Errorf("propertyOrdering = %v", got)
	}
	if _, ok := root["additionalProperties"]; ok {
		t.Error("Gemini's schema has no additionalProperties")
	}
}

// recordingServer answers every request with handle and keeps the decoded
// request bodies.
type recordingServer struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (rs *recordingServer) start(t *testing.T, handle func(body map[string]any, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		rs.mu.Lock()
		rs.bodies = append(rs.bodies, body)
		rs.mu.Unlock()
		handle(body, w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (rs *recordingServer) requests() []map[string]any {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]map[string]any(nil), rs.bodies...)
}

func openaiAnswer(w http.ResponseWriter, content string) {
	_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}]}`, content)
}

func TestOpenAI_StructuredOutput(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(_ map[string]any, w http.ResponseWriter) { openaiAnswer(w, `{"findings":[]}`) })
	o := &OpenAI{apiKey: "k", model: "gpt-4o", baseURL: srv.URL, client: srv.Client()}

	resp, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != `{"findings":[]}` {
		t.Errorf("Content = %q", resp.Content)
	}
	rf := rs.requests()[0]["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "report_findings" || js["strict"] != true || js["schema"].(map[string]any)["type"] != "object" {
		t.Errorf("response_format = %v", rf)
	}

	// No Output, no response_format.
	if _, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rs.requests()[1]["response_format"]; ok {
		t.Error("a request without Output should not ask for structured output")
	}
}

// An endpoint that refuses response_format is asked again without it, and
// not asked again for the rest of the provider's life.
func TestOpenAI_SchemaRefusedFallsBack(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(body map[string]any, w http.ResponseWriter) {
		if _, ok := body["response_format"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"response_format is not supported"}`)
			return
		}
		openaiAnswer(w, "[]")
	})
	o := &Ollama{model: "llama3", baseURL: srv.URL, client: srv.Client()}

	for range 2 {
		resp, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput})
		if err != nil || resp.Content != "[]" {
			t.Fatalf("Review = %q, %v", resp.Content, err)
		}
	}
	var withSchema []bool
	for _, b := range rs.requests() {
		_, ok := b["response_format"]
		withSchema = append(withSchema, ok)
	}
	if !slices.Equal(withSchema, []bool{true, false, false}) {
		t.Errorf("requests with response_format: %v, want only the first", withSchema)
	}
}

// A 400 that dropping the schema does not fix is the error, and does not
// switch structured output off.
func TestOpenAI_UnrelatedRefusalKeepsSchema(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(_ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"context too long"}`)
	})
	o := &OpenAI{apiKey: "k", model: "gpt-4o", baseURL: srv.URL, client: srv.Client()}
	if _, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput}); err == nil || !strings.Contains(err.Error(), "context too long") {
		t.Fatalf("err = %v, want the 400", err)
	}
	if !o.schema.use(testOutput) {
		t.Error("structured output was switched off by an unrelated refusal")
	}
}

// Anthropic structured output is output_config.format, not a forced tool
// call, which the newest models refuse.
func TestAnthropic_StructuredOutput(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(_ map[string]any, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"findings\":[]}"}],"usage":{"input_tokens":5,"output_tokens":5}}`)
	})
	a := &Anthropic{apiKey: "k", model: "claude-test", client: &http.Client{
		Transport: &rewriteTransport{base: srv.Client().Transport, baseURL: srv.URL},
	}}

	resp, err := a.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput})
	if err != nil || resp.Content != `{"findings":[]}` {
		t.Fatalf("Review = %q, %v", resp.Content, err)
	}
	body := rs.requests()[0]
	format := body["output_config"].(map[string]any)["format"].(map[string]any)
	schema := format["schema"].(map[string]any)
	if format["type"] != "json_schema" || schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Errorf("output_config.format = %v", format)
	}
	if _, ok := body["tools"]; ok {
		t.Error("structured output should not send tools")
	}
}

func TestGemini_StructuredOutput(t *testing.T) {
	rs := &recordingServer{}
	srv := rs.start(t, func(_ map[string]any, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"{\"findings\":[]}"}]}}]}`)
	})
	g := &Gemini{apiKey: "k", model: "gemini-test", client: &http.Client{
		Transport: &rewriteTransport{base: srv.Client().Transport, baseURL: srv.URL},
	}}
	resp, err := g.Review(context.Background(), ReviewRequest{UserPrompt: "x", Output: testOutput})
	if err != nil || resp.Content != `{"findings":[]}` {
		t.Fatalf("Review = %q, %v", resp.Content, err)
	}
	gc := rs.requests()[0]["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" || gc["responseSchema"].(map[string]any)["type"] != "OBJECT" {
		t.Errorf("generationConfig = %v", gc)
	}
}
