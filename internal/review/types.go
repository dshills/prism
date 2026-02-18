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
