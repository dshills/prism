package review

// Severity represents the severity level of a finding.
type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

// SeverityRank returns a numeric rank for sorting (higher = more severe).
func SeverityRank(s Severity) int {
	switch s {
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	default:
		return 0
	}
}

// MeetsThreshold returns true if severity is at or above the threshold.
func MeetsThreshold(s Severity, threshold string) bool {
	if threshold == "none" || threshold == "" {
		return false
	}
	return SeverityRank(s) >= SeverityRank(Severity(threshold))
}

// Category represents the type of finding.
type Category string

const (
	CategoryBug             Category = "bug"
	CategorySecurity        Category = "security"
	CategoryPerformance     Category = "performance"
	CategoryCorrectness     Category = "correctness"
	CategoryStyle           Category = "style"
	CategoryMaintainability Category = "maintainability"
	CategoryTesting         Category = "testing"
	CategoryDocs            Category = "docs"
)

// Fix replaces Before, copied verbatim from the file, with After.
type Fix struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// Location represents where a finding was detected.
type Location struct {
	Path    string    `json:"path"`
	Hunk    string    `json:"hunk,omitempty"`
	Lines   LineRange `json:"lines"`
	Commit  string    `json:"commit,omitempty"`
	Snippet string    `json:"snippet,omitempty"`
}

// LineRange represents a range of line numbers.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Finding represents a single code review finding.
type Finding struct {
	ID         string     `json:"id"`
	Severity   Severity   `json:"severity"`
	Category   Category   `json:"category"`
	Title      string     `json:"title"`
	Message    string     `json:"message"`
	Suggestion string     `json:"suggestion,omitempty"`
	Confidence float64    `json:"confidence"`
	Locations  []Location `json:"locations"`
	Tags       []string   `json:"tags,omitempty"`
	References []string   `json:"references,omitempty"`
	// Evidence is the code the model quoted as showing the problem. It is
	// checked against the reviewed input (specs/SPEC-review-integrity.md FR-5).
	Evidence string `json:"evidence,omitempty"`
	// Fix is a code change an agent can apply as an exact string
	// replacement: Before occurs exactly once in the file. A fix that does not
	// is removed during verification (TagFixDropped). nil when the model gave
	// none.
	Fix *Fix `json:"fix,omitempty"`
	// Provider is the LLM vendor that produced this finding
	// (e.g. "anthropic", "openai"). Empty only for legacy/test data.
	Provider string `json:"provider,omitempty"`
	// Model is the concrete model that produced this finding.
	Model string `json:"model,omitempty"`
}

// Provenance records that a finding was produced by an LLM and which one.
// Downstream consumers (SARIF ingest, dashboards, auto-fix agents) use this
// to distinguish AI-generated findings from deterministic-analyzer findings.
type Provenance struct {
	AIGenerated bool   `json:"ai_generated"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
}

// RepoInfo contains repository metadata.
type RepoInfo struct {
	Root   string `json:"root"`
	Head   string `json:"head"`
	Branch string `json:"branch"`
}

// InputInfo describes what was reviewed.
type InputInfo struct {
	Mode          string   `json:"mode"`
	Range         string   `json:"range,omitempty"`
	PathsIncluded []string `json:"pathsIncluded,omitempty"`
	PathsExcluded []string `json:"pathsExcluded,omitempty"`
}

// SeverityCounts holds counts by severity level.
type SeverityCounts struct {
	Low    int `json:"low"`
	Medium int `json:"medium"`
	High   int `json:"high"`
}

// Summary provides an overview of findings.
type Summary struct {
	Counts          SeverityCounts `json:"counts"`
	HighestSeverity Severity       `json:"highestSeverity"`
}

// Timing contains performance metrics.
type Timing struct {
	GitMs   int64 `json:"gitMs"`
	LLMMs   int64 `json:"llmMs"`
	TotalMs int64 `json:"totalMs"`
}

// Report is the top-level output structure.
type Report struct {
	Tool     string    `json:"tool"`
	Version  string    `json:"version"`
	RunID    string    `json:"runId"`
	Repo     RepoInfo  `json:"repo"`
	Inputs   InputInfo `json:"inputs"`
	Summary  Summary   `json:"summary"`
	Findings []Finding `json:"findings"`
	Timing   Timing    `json:"timing"`
	// Provenance lists every (provider, model) pair that contributed findings
	// to this report. A single-provider run has one entry; compare mode lists
	// each model. The underscore prefix marks it as tool-level metadata.
	Provenance []Provenance `json:"_provenance,omitempty"`
	// Coverage states what was reviewed, how, and by which model. It is
	// always present, including when there are no findings, so a clean
	// result can be told apart from a partial review or no review at all.
	Coverage Coverage `json:"coverage"`
	// Discarded lists findings that failed verification against the code,
	// each with the reason. They are not in Findings or Summary (FR-7).
	Discarded []Discard `json:"discarded"`
	// Suppressed lists findings accepted by the baseline or an inline
	// prism:ignore, with where the acceptance came from. They are not in
	// Findings or Summary, so they never decide the exit code.
	Suppressed []Suppression `json:"suppressed"`
}

// Discard is a finding removed by verification, and why.
type Discard struct {
	Finding Finding `json:"finding"`
	Reason  string  `json:"reason"`
}

// ComputeSummary calculates the summary from findings.
func ComputeSummary(findings []Finding) Summary {
	var s Summary
	for _, f := range findings {
		switch f.Severity {
		case SeverityLow:
			s.Counts.Low++
		case SeverityMedium:
			s.Counts.Medium++
		case SeverityHigh:
			s.Counts.High++
		}
		if SeverityRank(f.Severity) > SeverityRank(s.HighestSeverity) {
			s.HighestSeverity = f.Severity
		}
	}
	return s
}
