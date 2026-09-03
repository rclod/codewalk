package walkthrough

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rclod/codewalk/internal/diagram"
)

// Severity classifies a validation finding.
type Severity string

const (
	// SeverityError marks a walkthrough that is structurally invalid: consumers
	// (web UI, evaluators, follow-up sessions) cannot rely on it.
	SeverityError Severity = "error"
	// SeverityWarning marks something that degrades the reading experience but
	// still renders.
	SeverityWarning Severity = "warning"
)

// Issue is one structural validation finding. Issues are deliberately
// structural only: nothing here judges explanatory quality, which is the
// evaluation system's job.
type Issue struct {
	Severity Severity `json:"severity"`
	Path     string   `json:"path"` // JSON-ish location, e.g. "steps[2].code_refs[0]"
	Message  string   `json:"message"`
}

func (i Issue) String() string { return fmt.Sprintf("%s: %s: %s", i.Severity, i.Path, i.Message) }

// Validate checks the walkthrough's internal structure: required fields,
// resolvable cross-references and parseable diagrams. It does not touch the
// repository; grounding against source is a separate concern (see
// internal/eval).
func (w *Walkthrough) Validate() []Issue {
	var issues []Issue
	add := func(sev Severity, path, msg string) {
		issues = append(issues, Issue{Severity: sev, Path: path, Message: msg})
	}

	if w.SchemaVersion == "" {
		add(SeverityError, "schema_version", "missing schema version")
	} else if w.SchemaVersion != SchemaVersion {
		add(SeverityWarning, "schema_version",
			fmt.Sprintf("walkthrough uses schema version %q, this build expects %q", w.SchemaVersion, SchemaVersion))
	}
	if w.Kind != KindChange && w.Kind != KindCodebase {
		add(SeverityError, "kind", fmt.Sprintf("unknown walkthrough kind %q", w.Kind))
	}
	if strings.TrimSpace(w.Title) == "" {
		add(SeverityError, "title", "missing title")
	}
	if strings.TrimSpace(w.Headline) == "" {
		add(SeverityError, "headline", "missing headline")
	}
	if len(w.Steps) == 0 {
		add(SeverityError, "steps", "walkthrough has no steps")
	}
	if w.Complexity.Level < 1 || w.Complexity.Level > 5 {
		add(SeverityError, "complexity.level", fmt.Sprintf("complexity level %d is outside 1..5", w.Complexity.Level))
	}

	conceptIDs := map[string]bool{}
	for i, c := range w.Concepts {
		p := fmt.Sprintf("concepts[%d]", i)
		if c.ID == "" {
			add(SeverityError, p+".id", "missing id")
		} else if conceptIDs[c.ID] {
			add(SeverityError, p+".id", fmt.Sprintf("duplicate concept id %q", c.ID))
		}
		conceptIDs[c.ID] = true
		if strings.TrimSpace(c.Name) == "" {
			add(SeverityError, p+".name", "missing name")
		}
		if strings.TrimSpace(c.Summary) == "" {
			add(SeverityWarning, p+".summary", "concept has no summary")
		}
	}

	componentIDs := map[string]bool{}
	for i, c := range w.Components {
		p := fmt.Sprintf("components[%d]", i)
		if c.ID == "" {
			add(SeverityError, p+".id", "missing id")
		} else if componentIDs[c.ID] {
			add(SeverityError, p+".id", fmt.Sprintf("duplicate component id %q", c.ID))
		}
		componentIDs[c.ID] = true
		if strings.TrimSpace(c.Name) == "" {
			add(SeverityError, p+".name", "missing name")
		}
		if strings.TrimSpace(c.Responsibility) == "" {
			add(SeverityWarning, p+".responsibility", "component has no stated responsibility")
		}
	}

	for i, r := range w.Relationships {
		p := fmt.Sprintf("relationships[%d]", i)
		if !componentIDs[r.From] {
			add(SeverityError, p+".from", fmt.Sprintf("unknown component id %q", r.From))
		}
		if !componentIDs[r.To] {
			add(SeverityError, p+".to", fmt.Sprintf("unknown component id %q", r.To))
		}
	}

	// Diagrams can be declared at the walkthrough level, inside a step, or
	// inside a deep dive, and a flow or the architecture section may reference
	// any of them. Collect every id up front so reference checks do not depend
	// on the order this function happens to walk the document in.
	diagramIDs := map[string]bool{}
	for _, d := range w.AllDiagrams() {
		diagramIDs[d.ID] = true
	}
	seenDiagram := map[string]bool{}
	checkDiagrams := func(ds []Diagram, base string) {
		for i, d := range ds {
			p := fmt.Sprintf("%s[%d]", base, i)
			if d.ID == "" {
				add(SeverityError, p+".id", "missing id")
			} else if seenDiagram[d.ID] {
				add(SeverityError, p+".id", fmt.Sprintf("duplicate diagram id %q", d.ID))
			}
			seenDiagram[d.ID] = true
			if d.Format != DiagramMermaid {
				add(SeverityWarning, p+".format", fmt.Sprintf("unsupported diagram format %q", d.Format))
				continue
			}
			for _, di := range diagram.ValidateMermaid(d.Source) {
				sev := SeverityWarning
				if di.Fatal {
					sev = SeverityError
				}
				add(sev, p+".source", di.String())
			}
		}
	}
	checkDiagrams(w.Diagrams, "diagrams")

	flowIDs := map[string]bool{}
	for i, f := range w.Flows {
		p := fmt.Sprintf("flows[%d]", i)
		if f.ID == "" {
			add(SeverityError, p+".id", "missing id")
		} else if flowIDs[f.ID] {
			add(SeverityError, p+".id", fmt.Sprintf("duplicate flow id %q", f.ID))
		}
		flowIDs[f.ID] = true
		if len(f.Steps) == 0 {
			add(SeverityWarning, p+".steps", "flow has no steps")
		}
		if f.DiagramID != "" && !diagramIDs[f.DiagramID] {
			add(SeverityError, p+".diagram_id", fmt.Sprintf("unknown diagram id %q", f.DiagramID))
		}
		for j, fs := range f.Steps {
			if fs.CodeRef != nil {
				issues = append(issues, validateRef(*fs.CodeRef, fmt.Sprintf("%s.steps[%d].code_ref", p, j))...)
			}
		}
	}

	stepIDs := map[string]bool{}
	for i, s := range w.Steps {
		p := fmt.Sprintf("steps[%d]", i)
		if s.ID == "" {
			add(SeverityError, p+".id", "missing id")
		} else if stepIDs[s.ID] {
			add(SeverityError, p+".id", fmt.Sprintf("duplicate step id %q", s.ID))
		}
		stepIDs[s.ID] = true
		if strings.TrimSpace(s.Title) == "" {
			add(SeverityError, p+".title", "missing title")
		}
		if strings.TrimSpace(s.Explanation) == "" {
			add(SeverityError, p+".explanation", "step has no explanation")
		}
		if strings.TrimSpace(s.Summary) == "" {
			add(SeverityWarning, p+".summary", "step has no one-line summary, which navigation surfaces rely on")
		}
		for _, id := range s.Concepts {
			if !conceptIDs[id] {
				add(SeverityError, p+".concepts", fmt.Sprintf("unknown concept id %q", id))
			}
		}
		for _, id := range s.Components {
			if !componentIDs[id] {
				add(SeverityError, p+".components", fmt.Sprintf("unknown component id %q", id))
			}
		}
		if s.FlowID != "" && !flowIDs[s.FlowID] {
			add(SeverityError, p+".flow_id", fmt.Sprintf("unknown flow id %q", s.FlowID))
		}
		for j, r := range s.CodeRefs {
			issues = append(issues, validateRef(r, fmt.Sprintf("%s.code_refs[%d]", p, j))...)
		}
		checkDiagrams(s.Diagrams, p+".diagrams")
		if s.DeepDive != nil {
			if strings.TrimSpace(s.DeepDive.Explanation) == "" {
				add(SeverityWarning, p+".deep_dive.explanation", "deep dive has no content")
			}
			checkDiagrams(s.DeepDive.Diagrams, p+".deep_dive.diagrams")
		}
	}

	if w.Architecture != nil {
		if w.Architecture.DiagramID != "" && !diagramIDs[w.Architecture.DiagramID] {
			add(SeverityError, "architecture.diagram_id", fmt.Sprintf("unknown diagram id %q", w.Architecture.DiagramID))
		}
		var walk func(gs []ArchitectureGroup, base string)
		walk = func(gs []ArchitectureGroup, base string) {
			for i, g := range gs {
				p := fmt.Sprintf("%s[%d]", base, i)
				for _, id := range g.Components {
					if !componentIDs[id] {
						add(SeverityError, p+".components", fmt.Sprintf("unknown component id %q", id))
					}
				}
				walk(g.Children, p+".children")
			}
		}
		walk(w.Architecture.Groups, "architecture.groups")
	}

	evidenceIDs := map[string]bool{}
	for i, e := range w.Evidence {
		if e.ID == "" {
			add(SeverityError, fmt.Sprintf("evidence[%d].id", i), "missing id")
		}
		evidenceIDs[e.ID] = true
	}
	for i, s := range w.Steps {
		for _, id := range s.Evidence {
			if !evidenceIDs[id] {
				add(SeverityWarning, fmt.Sprintf("steps[%d].evidence", i), fmt.Sprintf("unknown evidence id %q", id))
			}
		}
	}
	for i, r := range w.StartHere {
		issues = append(issues, validateRef(r, fmt.Sprintf("start_here[%d]", i))...)
	}
	return issues
}

func validateRef(r CodeReference, path string) []Issue {
	var issues []Issue
	if strings.TrimSpace(r.Path) == "" {
		issues = append(issues, Issue{Severity: SeverityError, Path: path + ".path", Message: "code reference has no path"})
	}
	if r.StartLine < 0 || r.EndLine < 0 {
		issues = append(issues, Issue{Severity: SeverityError, Path: path, Message: "negative line number"})
	}
	if r.EndLine > 0 && r.StartLine == 0 {
		issues = append(issues, Issue{Severity: SeverityWarning, Path: path, Message: "end_line set without start_line"})
	}
	return issues
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Decode parses a walkthrough from JSON and normalises it.
func Decode(data []byte) (*Walkthrough, error) {
	var w Walkthrough
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("decode walkthrough: %w", err)
	}
	w.Normalize()
	return &w, nil
}

// Encode serialises a walkthrough as indented JSON.
func (w *Walkthrough) Encode() ([]byte, error) {
	return json.MarshalIndent(w, "", "  ")
}
