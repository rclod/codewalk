// Package diagram provides lightweight, dependency-free validation of the
// diagram sources embedded in a walkthrough.
//
// The goal is not to reimplement Mermaid. It is to catch the failure modes that
// actually damage a walkthrough — an empty diagram, an unknown diagram type, an
// unbalanced bracket, a sequence diagram whose arrows do not parse — cheaply
// enough that the check can run on every generated walkthrough.
package diagram

import (
	"fmt"
	"strings"
)

// Issue is a single problem found in a diagram source.
type Issue struct {
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
	// Fatal marks problems that would prevent the diagram from rendering at all.
	Fatal bool `json:"fatal"`
}

func (i Issue) String() string {
	if i.Line > 0 {
		return fmt.Sprintf("line %d: %s", i.Line, i.Message)
	}
	return i.Message
}

// knownTypes lists the Mermaid diagram headers codewalk generates or accepts.
// Unknown headers are reported as non-fatal, because Mermaid gains diagram
// types faster than this validator does.
var knownTypes = []string{
	"sequenceDiagram",
	"flowchart",
	"graph",
	"classDiagram",
	"stateDiagram-v2",
	"stateDiagram",
	"erDiagram",
	"journey",
	"gitGraph",
	"timeline",
	"mindmap",
	"C4Context",
	"block-beta",
}

// ValidateMermaid checks a Mermaid source for structural problems.
func ValidateMermaid(src string) []Issue {
	var issues []Issue
	lines := splitMeaningful(src)
	if len(lines) == 0 {
		return []Issue{{Message: "diagram source is empty", Fatal: true}}
	}

	header := lines[0]
	kind := diagramType(header.text)
	if kind == "" {
		issues = append(issues, Issue{
			Line:    header.n,
			Message: fmt.Sprintf("unrecognised diagram type %q", firstWord(header.text)),
		})
	}
	if len(lines) == 1 {
		issues = append(issues, Issue{Line: header.n, Message: "diagram declares a type but contains no content", Fatal: true})
		return issues
	}

	issues = append(issues, checkBalanced(src)...)

	switch kind {
	case "sequenceDiagram":
		issues = append(issues, validateSequence(lines[1:])...)
	case "flowchart", "graph":
		issues = append(issues, validateFlowchart(lines[1:])...)
	}
	return issues
}

// Fatal reports whether any issue would prevent rendering.
func Fatal(issues []Issue) bool {
	for _, i := range issues {
		if i.Fatal {
			return true
		}
	}
	return false
}

type line struct {
	n    int
	text string
}

func splitMeaningful(src string) []line {
	var out []line
	for i, raw := range strings.Split(src, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "%%") {
			continue
		}
		out = append(out, line{n: i + 1, text: t})
	}
	return out
}

func diagramType(header string) string {
	w := firstWord(header)
	for _, k := range knownTypes {
		if w == k || strings.HasPrefix(header, k) {
			switch k {
			case "graph":
				return "graph"
			case "flowchart":
				return "flowchart"
			}
			return k
		}
	}
	return ""
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// sequenceKeywords are statements that are valid without an arrow.
var sequenceKeywords = []string{
	"participant", "actor", "activate", "deactivate", "note", "loop", "alt",
	"else", "opt", "par", "and", "critical", "option", "rect", "end", "autonumber",
	"box", "break", "link", "links", "create", "destroy", "title", "accTitle", "accDescr",
}

var sequenceArrows = []string{"-->>", "->>", "-->", "->", "--x", "-x", "--)", "-)", "<<-->>", "<<->>"}

func validateSequence(lines []line) []Issue {
	var issues []Issue
	arrows := 0
	for _, l := range lines {
		lower := strings.ToLower(l.text)
		if hasKeyword(lower, sequenceKeywords) {
			continue
		}
		if a := containsAny(l.text, sequenceArrows); a != "" {
			arrows++
			idx := strings.Index(l.text, a)
			left := strings.TrimSpace(l.text[:idx])
			right := strings.TrimSpace(l.text[idx+len(a):])
			if left == "" {
				issues = append(issues, Issue{Line: l.n, Message: "message has no sender", Fatal: true})
			}
			if !strings.Contains(right, ":") {
				issues = append(issues, Issue{Line: l.n, Message: "message is missing a ':' label", Fatal: true})
			} else if strings.TrimSpace(strings.SplitN(right, ":", 2)[0]) == "" {
				issues = append(issues, Issue{Line: l.n, Message: "message has no recipient", Fatal: true})
			}
			continue
		}
		issues = append(issues, Issue{Line: l.n, Message: "line is neither a sequence statement nor a message", Fatal: true})
	}
	if arrows == 0 {
		issues = append(issues, Issue{Message: "sequence diagram contains no messages", Fatal: true})
	}
	return issues
}

var flowArrows = []string{"-->", "---", "-.->", "==>", "--x", "--o", "<-->"}

var flowKeywords = []string{
	"subgraph", "end", "direction", "click", "class", "classdef", "style",
	"linkstyle", "title", "acctitle", "accdescr",
}

func validateFlowchart(lines []line) []Issue {
	var issues []Issue
	edges, nodes := 0, 0
	depth := 0
	for _, l := range lines {
		lower := strings.ToLower(l.text)
		if hasKeyword(lower, flowKeywords) {
			if strings.HasPrefix(lower, "subgraph") {
				depth++
			}
			if lower == "end" {
				depth--
				if depth < 0 {
					issues = append(issues, Issue{Line: l.n, Message: "'end' without a matching 'subgraph'", Fatal: true})
					depth = 0
				}
			}
			continue
		}
		if containsAny(l.text, flowArrows) != "" {
			edges++
			continue
		}
		nodes++
	}
	if depth > 0 {
		issues = append(issues, Issue{Message: fmt.Sprintf("%d unclosed 'subgraph' block(s)", depth), Fatal: true})
	}
	if edges == 0 && nodes <= 1 {
		issues = append(issues, Issue{Message: "flowchart contains no edges", Fatal: true})
	}
	return issues
}

func hasKeyword(lowerLine string, keywords []string) bool {
	for _, k := range keywords {
		if lowerLine == k || strings.HasPrefix(lowerLine, k+" ") {
			return true
		}
	}
	return false
}

func containsAny(s string, subs []string) string {
	best := ""
	bestIdx := -1
	for _, sub := range subs {
		if i := strings.Index(s, sub); i >= 0 {
			// Prefer the earliest match, and the longest arrow at that position.
			if bestIdx < 0 || i < bestIdx || (i == bestIdx && len(sub) > len(best)) {
				best, bestIdx = sub, i
			}
		}
	}
	return best
}

// checkBalanced reports unbalanced bracket pairs, the most common way a
// generated diagram fails to render.
func checkBalanced(src string) []Issue {
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
	open := map[rune]int{'(': 0, '[': 0, '{': 0}
	inQuote := false
	for _, r := range src {
		switch {
		case r == '"':
			inQuote = !inQuote
		case inQuote:
			// Brackets inside labels are literal text.
		case r == '(' || r == '[' || r == '{':
			open[r]++
		case r == ')' || r == ']' || r == '}':
			open[pairs[r]]--
		}
	}
	var issues []Issue
	for o, n := range open {
		if n != 0 {
			issues = append(issues, Issue{
				Message: fmt.Sprintf("unbalanced %q brackets (%+d)", string(o), n),
				Fatal:   true,
			})
		}
	}
	if inQuote {
		issues = append(issues, Issue{Message: "unterminated quoted label", Fatal: true})
	}
	return issues
}
