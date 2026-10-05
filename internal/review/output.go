package review

import "github.com/dshills/prism/internal/providers"

// findingsOutput is the structured output every review asks for: an object
// holding the findings, in each provider's own structured-output mode. The
// schema fixes the shape and the enumerated values (severity, category), so a
// model that supports it cannot answer with malformed JSON, and the repair
// pass is needed only where structured output is not available.
//
// The root is an object, not the bare array the prompt describes, because
// structured-output modes require one. parseFindings reads both.
var findingsOutput = &providers.Output{
	Name:        "report_findings",
	Description: "Report the code review's findings. Use an empty list when there are none.",
	Schema: &providers.Schema{
		Type: "object",
		Properties: []providers.Property{
			{Name: "findings", Schema: &providers.Schema{Type: "array", Items: findingSchema()}},
		},
	},
}

func findingSchema() *providers.Schema {
	str := func(desc string) *providers.Schema { return &providers.Schema{Type: "string", Description: desc} }
	categories := make([]string, len(knownCategories))
	for i, c := range knownCategories {
		categories[i] = string(c)
	}
	return &providers.Schema{
		Type: "object",
		Properties: []providers.Property{
			{Name: "severity", Schema: &providers.Schema{Type: "string", Enum: []string{"low", "medium", "high"}}},
			{Name: "category", Schema: &providers.Schema{Type: "string", Enum: categories}},
			{Name: "title", Schema: str("Short descriptive title")},
			{Name: "message", Schema: str("What is wrong and why it matters")},
			{Name: "suggestion", Schema: str("How to fix it, with code if helpful")},
			{Name: "confidence", Schema: &providers.Schema{Type: "number", Description: "Confidence from 0.0 to 1.0"}},
			{Name: "path", Schema: str("Relative file path")},
			{Name: "startLine", Schema: &providers.Schema{Type: "integer"}},
			{Name: "endLine", Schema: &providers.Schema{Type: "integer"}},
			{Name: "evidence", Schema: str("The exact line or lines of code that show the problem, copied verbatim from the input")},
			{Name: "fix", Schema: &providers.Schema{
				Type:        "object",
				Description: "A code change that fixes the problem by replacing code shown in the input; both empty when the fix is not such a replacement",
				Properties: []providers.Property{
					{Name: "before", Schema: str("The code to replace, copied exactly (whitespace included) from the file; it must occur only once there")},
					{Name: "after", Schema: str("The code to put in its place")},
				},
			}},
			{Name: "tags", Schema: &providers.Schema{Type: "array", Items: &providers.Schema{Type: "string"}}},
		},
	}
}
