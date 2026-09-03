package walkthrough

import (
	"strings"
	"testing"
)

func TestNormalizeAssignsIdentifiersAndCleansInput(t *testing.T) {
	w := &Walkthrough{
		Kind:  KindChange,
		Title: "Example",
		Steps: []Step{
			{Title: "One", Explanation: "..."},
			{Title: "Two", Explanation: "...", CodeRefs: []CodeReference{
				{Path: "./internal/orders/service.go", StartLine: 40, EndLine: 12},
			}},
			// A duplicate identifier must not collapse two distinct steps.
			{ID: "step1", Title: "Three", Explanation: "..."},
		},
		Diagrams: []Diagram{
			{Source: "```mermaid\nflowchart LR\n  A --> B\n```"},
		},
		Complexity: Complexity{Level: 9},
	}
	w.Normalize()

	ids := map[string]bool{}
	for _, s := range w.Steps {
		if s.ID == "" {
			t.Fatal("every step should receive an id")
		}
		if ids[s.ID] {
			t.Fatalf("duplicate step id %q survived normalisation", s.ID)
		}
		ids[s.ID] = true
	}
	ref := w.Steps[1].CodeRefs[0]
	if ref.Path != "internal/orders/service.go" {
		t.Errorf("path = %q, want a repository-relative path", ref.Path)
	}
	if ref.StartLine != 12 || ref.EndLine != 40 {
		t.Errorf("reversed line range not corrected: %d-%d", ref.StartLine, ref.EndLine)
	}
	if strings.Contains(w.Diagrams[0].Source, "```") {
		t.Errorf("markdown fence should be stripped from diagram source: %q", w.Diagrams[0].Source)
	}
	if w.Complexity.Level != 5 || w.Complexity.Label != "systemic" {
		t.Errorf("complexity = %d/%q, want clamping to 5/systemic", w.Complexity.Level, w.Complexity.Label)
	}
	if w.SchemaVersion != SchemaVersion {
		t.Error("normalisation should stamp the schema version")
	}
}

func TestValidateAcceptsAWellFormedWalkthrough(t *testing.T) {
	w := minimal()
	if issues := w.Validate(); HasErrors(issues) {
		t.Errorf("valid walkthrough rejected: %v", issues)
	}
}

func TestValidateFindsStructuralProblems(t *testing.T) {
	tests := map[string]func(*Walkthrough){
		"unknown component reference": func(w *Walkthrough) {
			w.Steps[0].Components = []string{"does-not-exist"}
		},
		"unknown concept reference": func(w *Walkthrough) {
			w.Steps[0].Concepts = []string{"does-not-exist"}
		},
		"relationship to unknown component": func(w *Walkthrough) {
			w.Relationships = append(w.Relationships, Relationship{From: "component1", To: "ghost", Kind: "calls"})
		},
		"unparseable diagram": func(w *Walkthrough) {
			w.Diagrams = append(w.Diagrams, Diagram{ID: "d1", Format: DiagramMermaid, Source: "sequenceDiagram\n    Browser->>API"})
		},
		"code reference without a path": func(w *Walkthrough) {
			w.Steps[0].CodeRefs = append(w.Steps[0].CodeRefs, CodeReference{Symbol: "Thing"})
		},
		"missing explanation": func(w *Walkthrough) {
			w.Steps[0].Explanation = ""
		},
		"no steps": func(w *Walkthrough) {
			w.Steps = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			w := minimal()
			mutate(w)
			if !HasErrors(w.Validate()) {
				t.Errorf("expected a validation error")
			}
		})
	}
}

func TestWordCountExcludesDeepDives(t *testing.T) {
	w := minimal()
	before := w.WordCount()
	w.Steps[0].DeepDive = &DeepDive{Explanation: strings.Repeat("detail ", 500)}
	if w.WordCount() != before {
		t.Error("deep dives are optional disclosure and must not count against the default reading length")
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	w := minimal()
	data, err := w.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != w.Title || len(got.Steps) != len(w.Steps) {
		t.Errorf("round trip lost content: %+v", got)
	}
}

func minimal() *Walkthrough {
	w := &Walkthrough{
		Kind:       KindChange,
		Title:      "Example change",
		Headline:   "Something changed.",
		Complexity: Complexity{Level: 2},
		Components: []Component{{ID: "component1", Name: "Service", Responsibility: "Does the thing."}},
		Steps: []Step{{
			ID: "step1", Title: "What changed", Summary: "The gist.", Explanation: "Details.",
			Components: []string{"component1"},
		}},
	}
	w.Normalize()
	return w
}
