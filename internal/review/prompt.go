package review

import (
	"fmt"
	"strings"
)

const systemPrompt = `You are a strict, expert code reviewer. Your job is to review code diffs and produce structured findings in JSON format.

Rules:
1. Only review the changes shown in the diff. Do not comment on unchanged code.
2. Focus on bugs, security issues, performance problems, and correctness. Avoid bikeshedding on style unless it impacts readability significantly.
3. Be concise and actionable. Every finding must include a concrete suggestion.
4. Reference line numbers from the diff hunks.
5. Rate severity as "low", "medium", or "high".
6. Rate your confidence from 0.0 to 1.0.
7. Categorize each finding as one of: bug, security, performance, correctness, style, maintainability, testing, docs.

You MUST respond with ONLY a JSON array of findings. No markdown, no explanation, no preamble. Just the JSON array.

Each finding must have this exact structure:
{
  "severity": "low|medium|high",
  "category": "bug|security|performance|correctness|style|maintainability|testing|docs",
  "title": "Short descriptive title",
  "message": "What is wrong and why it matters",
  "suggestion": "How to fix it, with code if helpful",
  "confidence": 0.0-1.0,
  "path": "relative/file/path",
  "startLine": 1,
  "endLine": 1,
  "tags": ["optional", "tags"]
}

If there are no issues, respond with an empty array: []`

// BuildUserPrompt constructs the user prompt from diff content and options.
func BuildUserPrompt(diff string, files []string, maxFindings int, failOn string) string {
	return BuildUserPromptWithRules(diff, files, maxFindings, failOn, nil)
}

// BuildUserPromptWithRules constructs the user prompt with optional rules.
func BuildUserPromptWithRules(diff string, files []string, maxFindings int, failOn string, rules *Rules) string {
	var b strings.Builder

	b.WriteString("Review the following code diff.\n\n")

	if maxFindings > 0 {
		fmt.Fprintf(&b, "Return at most %d findings.\n", maxFindings)
	}
	if failOn != "" && failOn != "none" {
		fmt.Fprintf(&b, "Focus especially on findings with severity %s or above.\n", failOn)
	}

	// Language hints from file extensions
	langs := detectLanguages(files)
	if len(langs) > 0 {
		fmt.Fprintf(&b, "Languages: %s\n", strings.Join(langs, ", "))
	}

	// Rules-based instructions
	if rulesSection := BuildRulesPromptSection(rules); rulesSection != "" {
		b.WriteString(rulesSection)
	}

	b.WriteString("\n--- BEGIN DIFF ---\n")
	b.WriteString(diff)
	b.WriteString("\n--- END DIFF ---\n")

	return b.String()
}

// SystemPrompt returns the system prompt for the LLM.
func SystemPrompt() string {
	return systemPrompt
}

func detectLanguages(files []string) []string {
	langMap := map[string]string{
		".go":    "Go",
		".py":    "Python",
		".js":    "JavaScript",
		".ts":    "TypeScript",
		".tsx":   "TypeScript/React",
		".jsx":   "JavaScript/React",
		".rs":    "Rust",
		".java":  "Java",
		".rb":    "Ruby",
		".cpp":   "C++",
		".c":     "C",
		".h":     "C/C++",
		".cs":    "C#",
		".php":   "PHP",
		".swift": "Swift",
		".kt":    "Kotlin",
		".sql":   "SQL",
		".sh":    "Shell",
		".yaml":  "YAML",
		".yml":   "YAML",
		".json":  "JSON",
		".tf":    "Terraform",
	}

	seen := make(map[string]bool)
	var langs []string
	for _, f := range files {
		for ext, lang := range langMap {
			if strings.HasSuffix(f, ext) && !seen[lang] {
				seen[lang] = true
				langs = append(langs, lang)
			}
		}
	}
	return langs
}
