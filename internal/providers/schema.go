package providers

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Output asks a provider to constrain its response to a JSON schema, using
// the provider's own structured-output mode: OpenAI's response_format,
// Anthropic's output_config.format, Gemini's responseSchema. The response's
// Content is then the JSON object the schema describes. A provider or
// endpoint without such a mode answers as it would without one, so the
// caller parses either shape.
type Output struct {
	// Name identifies the schema (OpenAI's json_schema name, Anthropic's
	// tool name): letters, digits, '_' and '-'.
	Name        string
	Description string
	Schema      *Schema
}

// Schema is the subset of JSON Schema every provider's structured-output mode
// accepts: objects, arrays, strings (optionally enumerated), numbers and
// integers. Every property of an object is required, as OpenAI's strict mode
// demands, so an optional value is an empty string or array instead.
type Schema struct {
	Type        string // "object", "array", "string", "number" or "integer"
	Description string
	// Properties lists an object's properties, in order.
	Properties []Property
	Items      *Schema  // an array's elements
	Enum       []string // a string's allowed values
}

// Property is one named property of an object schema.
type Property struct {
	Name   string
	Schema *Schema
}

// jsonSchema renders s as standard JSON Schema. strict adds what OpenAI's
// strict mode requires: additionalProperties false on every object.
func (s *Schema) jsonSchema(strict bool) map[string]any {
	out := map[string]any{"type": s.Type}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Items != nil {
		out["items"] = s.Items.jsonSchema(strict)
	}
	if s.Type == "object" {
		props := map[string]any{}
		required := make([]string, 0, len(s.Properties))
		for _, p := range s.Properties {
			props[p.Name] = p.Schema.jsonSchema(strict)
			required = append(required, p.Name)
		}
		out["properties"] = props
		out["required"] = required
		if strict {
			out["additionalProperties"] = false
		}
	}
	return out
}

// geminiSchema renders s as Gemini's responseSchema: OpenAPI-style upper-case
// types, with propertyOrdering so fields come back in the order given.
func (s *Schema) geminiSchema() map[string]any {
	out := map[string]any{"type": strings.ToUpper(s.Type)}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Items != nil {
		out["items"] = s.Items.geminiSchema()
	}
	if s.Type == "object" {
		props := map[string]any{}
		names := make([]string, 0, len(s.Properties))
		for _, p := range s.Properties {
			props[p.Name] = p.Schema.geminiSchema()
			names = append(names, p.Name)
		}
		out["properties"] = props
		out["required"] = names
		out["propertyOrdering"] = names
	}
	return out
}

// schemaSupport remembers, for one provider, that its endpoint rejected a
// structured-output request, so later calls (the other chunks of a review)
// do not each pay for a rejected request first.
type schemaSupport struct {
	rejected atomic.Bool
}

// use reports whether a request with out should ask for structured output.
func (s *schemaSupport) use(out *Output) bool {
	return out != nil && out.Schema != nil && !s.rejected.Load()
}

// effortSupport remembers, for one provider, the reasoning effort its
// endpoint rejected, as schemaSupport does for structured output. It is the
// level that was refused, not effort as a whole: a model can take some
// levels and not others.
type effortSupport struct {
	rejected atomic.Value // string
}

// use reports whether a request should carry effort.
func (s *effortSupport) use(effort string) bool {
	rejected, _ := s.rejected.Load().(string)
	return effort != "" && effort != rejected
}

// sendOptional sends a request with the optional fields its endpoint has not
// refused: structured output and a reasoning effort. A refusal (400 or 422)
// is answered by asking again without them, one at a time: without the
// effort, which costs the review least, then without the schema alone, then
// without both. The fields the request that got through left out are not
// sent to this endpoint again, so the other chunks of a review do not each
// pay for a refused request first; a field is switched off only after every
// attempt that kept it was refused.
func sendOptional(req ReviewRequest, schema *schemaSupport, effort *effortSupport,
	send func(structured, withEffort bool) (ReviewResponse, error)) (ReviewResponse, error) {
	structured, withEffort := schema.use(req.Output), effort.use(req.Effort)
	resp, err := send(structured, withEffort)
	if !refusedSchema(err) {
		return resp, err
	}
	type attempt struct{ structured, effort bool }
	var tries []attempt
	switch {
	case structured && withEffort:
		tries = []attempt{{true, false}, {false, true}, {false, false}}
	case structured || withEffort:
		tries = []attempt{{false, false}}
	}
	for _, a := range tries {
		resp, err = send(a.structured, a.effort)
		if err == nil {
			if structured && !a.structured {
				schema.rejected.Store(true)
			}
			if withEffort && !a.effort {
				effort.rejected.Store(req.Effort)
			}
			return resp, nil
		}
		if !refusedSchema(err) {
			return resp, err
		}
	}
	return resp, err
}

// requestError is a 4xx response other than auth and rate limits: the
// request itself was refused.
type requestError struct {
	status int
	body   string
}

func (e *requestError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.status, e.body)
}

// refusedSchema reports whether err is the kind of refusal an endpoint gives
// a structured-output field it does not support (400 or 422), so the request
// is worth sending once more without it.
func refusedSchema(err error) bool {
	re, ok := err.(*requestError)
	return ok && (re.status == 400 || re.status == 422)
}
