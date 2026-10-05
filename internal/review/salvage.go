package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// salvage counts local repairs of responses that were not valid JSON.
type salvage struct {
	repaired int // responses repaired
	lost     int // elements meant as findings that could not be read
}

func (s *salvage) add(o salvage) {
	s.repaired += o.repaired
	s.lost += o.lost
}

// record adds the repairs to a review's coverage. Lost findings make the
// review incomplete: one of them may have been the blocking one.
func (s salvage) record(c *Coverage) {
	c.Salvaged += s.repaired
	if s.lost > 0 {
		c.Skipped = append(c.Skipped, Skip{
			Target: "model response",
			Reason: fmt.Sprintf("a malformed response lost findings (%d unreadable or cut off) and the model could not repair it", s.lost),
		})
	}
}

// parseReviewedSalvaged is parseReviewedFindings, with a local repair of a
// response that does not parse before the caller asks the model to fix it.
func parseReviewedSalvaged(content, reviewed string) ([]Finding, salvage, error) {
	findings, err := parseReviewedFindings(content, reviewed)
	if err == nil {
		return findings, salvage{}, nil
	}
	raw, s, ok := salvageFindings(content)
	if !ok {
		return nil, salvage{}, err
	}
	findings = rawToFindings(raw)
	identifyFindings(findings, reviewed)
	return findings, s, nil
}

// candidate is one array in a response, as salvageFindings read it.
type candidate struct {
	raw  []rawFinding
	lost int // elements meant as findings that could not be read: objects that are not findings, or cut off
}

// salvageFindings repairs a response that is not valid JSON without asking
// the model again, for the mechanical failures: prose before or after the
// array, a {"findings": [...]} wrapper with something around it, trailing
// commas, and an answer cut off inside its last element.
//
// Every array in the response outside a quoted string is read (one inside
// an array already read is part of it), each element decoded on its own. An
// element that is not a finding (an object with a title, path and severity)
// is dropped; one that is an object or is cut off was meant as a finding, so
// it is lost. The response is repaired only when exactly one array holds
// findings and nothing was lost in any other. An answer with no findings is
// never salvaged: a "[]" somewhere in a broken response is no evidence of a
// clean review. Otherwise ok is false, and the caller falls back to the
// model's repair pass, as it does when prose has an unpaired quote.
func salvageFindings(content string) (raw []rawFinding, s salvage, ok bool) {
	var found []candidate
	inString, escaped := false, false
	for i := 0; i < len(content); i++ {
		c := content[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c != '[' {
			continue
		}
		elems, closed, n, ok := splitArray(content[i+1:])
		if !ok {
			return nil, salvage{}, false // mismatched brackets
		}
		found = append(found, readCandidate(elems, closed))
		i += n // past this array, nested ones included
	}
	if inString {
		return nil, salvage{}, false // a quote left open: something was cut
	}

	pick, lost := -1, 0
	for i, c := range found {
		if len(c.raw) > 0 {
			if pick >= 0 {
				return nil, salvage{}, false // two arrays of findings
			}
			pick = i
		}
		lost += c.lost
	}
	if pick < 0 || lost != found[pick].lost {
		return nil, salvage{}, false
	}
	c := found[pick]
	return c.raw, salvage{repaired: 1, lost: c.lost}, true
}

// readCandidate decodes an array's elements, keeping the findings.
func readCandidate(elems []string, closed bool) candidate {
	c := candidate{raw: []rawFinding{}}
	for i, e := range elems {
		e = strings.TrimSpace(e)
		if e == "" {
			continue // a trailing comma before ] or the end
		}
		if !closed && i == len(elems)-1 && !balanced(e) {
			c.lost++ // cut off mid-element
			continue
		}
		if strings.HasPrefix(e, "[") {
			c.lost++ // a nested array may hold findings, which are not read
			continue
		}
		if !strings.HasPrefix(e, "{") {
			continue // prose or a scalar, not meant as a finding
		}
		var f rawFinding
		if json.Unmarshal([]byte(stripTrailingCommas(e)), &f) != nil || f.Title == "" || f.Path == "" || f.Severity == "" {
			c.lost++
			continue
		}
		c.raw = append(c.raw, f)
	}
	if !closed && c.lost == 0 {
		c.lost++ // never closed: the answer was cut, and more may have followed
	}
	return c
}

// splitArray splits the text after an array's "[" into its top-level
// elements, up to the matching "]" (closed) or the end of the text, and
// returns how much of text it read. Strings are skipped whole, so brackets
// and commas inside them do not count. ok is false when a closing bracket
// does not match the one it closes (a "}" for the array's "]").
func splitArray(text string) (elems []string, closed bool, n int, ok bool) {
	var open []byte // the closers expected, innermost last
	from := 0
	inString, escaped := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			open = append(open, '}')
		case '[':
			open = append(open, ']')
		case '}', ']':
			if len(open) == 0 {
				if c != ']' {
					return nil, false, i, false
				}
				return append(elems, text[from:i]), true, i + 1, true // the array's own "]"
			}
			if open[len(open)-1] != c {
				return nil, false, i, false
			}
			open = open[:len(open)-1]
		case ',':
			if len(open) == 0 {
				elems = append(elems, text[from:i])
				from = i + 1
			}
		}
	}
	return append(elems, text[from:]), false, len(text), true
}

// balanced reports whether an element's brackets and strings all close.
func balanced(e string) bool {
	elems, closed, _, ok := splitArray(e + "]")
	return ok && closed && len(elems) == 1
}

// stripTrailingCommas removes commas that directly precede a closing "}" or
// "]" outside strings, which JSON does not allow but models write.
func stripTrailingCommas(e string) string {
	var b strings.Builder
	inString, escaped := false, false
	for i := 0; i < len(e); i++ {
		c := e[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			b.WriteByte(c)
			continue
		}
		if c == '"' {
			inString = true
		}
		if c == ',' {
			j := i + 1
			for j < len(e) && strings.IndexByte(" \t\r\n", e[j]) >= 0 {
				j++
			}
			if j < len(e) && (e[j] == '}' || e[j] == ']') {
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}
