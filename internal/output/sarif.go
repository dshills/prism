package output

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dshills/prism/internal/review"
)

// SARIFWriter outputs findings in SARIF v2.1.0 format.
type SARIFWriter struct{}

func (s *SARIFWriter) Write(w io.Writer, report *review.Report) error {
	sarif := buildSARIF(report)
	data, err := json.MarshalIndent(sarif, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling SARIF: %w", err)
	}
	_, err = w.Write(data)
	if err != nil {
		return fmt.Errorf("writing SARIF: %w", err)
	}
	_, err = fmt.Fprintln(w)
	return err
}

// SARIF schema types (v2.1.0)

type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	ShortDescription sarifMessage        `json:"shortDescription"`
	DefaultConfig    sarifDefaultConfig   `json:"defaultConfiguration"`
	Properties       sarifRuleProperties  `json:"properties,omitempty"`
}

type sarifDefaultConfig struct {
	Level string `json:"level"`
}

type sarifRuleProperties struct {
	Tags []string `json:"tags,omitempty"`
}

type sarifResult struct {
	RuleID    string           `json:"ruleId"`
	Level     string           `json:"level"`
	Message   sarifMessage     `json:"message"`
	Locations []sarifLocation  `json:"locations,omitempty"`
	Fixes     []sarifFix       `json:"fixes,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine"`
}

type sarifFix struct {
	Description sarifMessage `json:"description"`
}

func buildSARIF(report *review.Report) sarifLog {
	rulesMap := make(map[string]sarifRule)
	var results []sarifResult

	for _, f := range report.Findings {
		ruleID := generateRuleID(f)

		// Register rule if not seen
		if _, ok := rulesMap[ruleID]; !ok {
			rulesMap[ruleID] = sarifRule{
				ID:               ruleID,
				Name:             string(f.Category),
				ShortDescription: sarifMessage{Text: f.Title},
				DefaultConfig:    sarifDefaultConfig{Level: severityToLevel(f.Severity)},
				Properties:       sarifRuleProperties{Tags: f.Tags},
			}
		}

		result := sarifResult{
			RuleID:  ruleID,
			Level:   severityToLevel(f.Severity),
			Message: sarifMessage{Text: f.Message},
		}

		for _, loc := range f.Locations {
			result.Locations = append(result.Locations, sarifLocation{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifactLocation{URI: loc.Path},
					Region: sarifRegion{
						StartLine: loc.Lines.Start,
						EndLine:   loc.Lines.End,
					},
				},
			})
		}

		if f.Suggestion != "" {
			result.Fixes = append(result.Fixes, sarifFix{
				Description: sarifMessage{Text: f.Suggestion},
			})
		}

		results = append(results, result)
	}

	// Collect rules in stable order
	var rules []sarifRule
	seen := make(map[string]bool)
	for _, f := range report.Findings {
		rid := generateRuleID(f)
		if !seen[rid] {
			seen[rid] = true
			rules = append(rules, rulesMap[rid])
		}
	}

	return sarifLog{
		Version: "2.1.0",
		Schema:  "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json",
		Runs: []sarifRun{
			{
				Tool: sarifTool{
					Driver: sarifDriver{
						Name:           "prism",
						Version:        report.Version,
						InformationURI: "https://github.com/dshills/prism",
						Rules:          rules,
					},
				},
				Results: results,
			},
		},
	}
}

// severityToLevel maps prism severity to SARIF level.
func severityToLevel(s review.Severity) string {
	switch s {
	case review.SeverityHigh:
		return "error"
	case review.SeverityMedium:
		return "warning"
	case review.SeverityLow:
		return "note"
	default:
		return "note"
	}
}

// generateRuleID creates a stable rule ID from category + title.
func generateRuleID(f review.Finding) string {
	data := fmt.Sprintf("%s/%s", f.Category, f.Title)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("prism/%s/%x", f.Category, h[:4])
}
