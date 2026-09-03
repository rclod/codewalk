package eval

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rclod/codewalk/internal/walkthrough"
)

// Degradations exist to test the evaluators.
//
// An evaluation system is only trustworthy if it notices when a walkthrough
// gets worse. Each degradation below introduces one specific, known defect, and
// the test suite asserts that the expected dimensions actually move. Without
// this, a scoring regression would look exactly like a quality improvement.

// Degradation is a named way to make a walkthrough worse.
type Degradation struct {
	Name string
	// Description explains what defect is introduced.
	Description string
	// ExpectedDrops lists the dimensions that should score lower afterwards.
	ExpectedDrops []Dimension
	Apply         func(*walkthrough.Walkthrough)
}

// Degradations returns the standard degraded-variant set.
func Degradations() []Degradation {
	return []Degradation{
		{
			Name:          "missing_essential",
			Description:   "Removes the component and the step that explain the most important behaviour",
			ExpectedDrops: []Dimension{DimCoverage, DimMentalModel},
			Apply:         removeEssential,
		},
		{
			Name:          "hallucination",
			Description:   "Invents a datastore component and cites a file that does not exist",
			ExpectedDrops: []Dimension{DimGrounding},
			Apply:         hallucinate,
		},
		{
			Name:          "incorrect_sequence",
			Description:   "Reverses an important flow so the described ordering contradicts the code",
			ExpectedDrops: []Dimension{DimGrounding, DimMentalModel},
			Apply:         reverseFlows,
		},
		{
			Name:          "verbosity",
			Description:   "Adds technically true but irrelevant detail to every step",
			ExpectedDrops: []Dimension{DimSelectivity, DimConcision},
			Apply:         addVerbosity,
		},
		{
			Name:          "bad_teaching_order",
			Description:   "Moves scene-setting context to the end, after the implementation detail",
			ExpectedDrops: []Dimension{DimTeachingOrder},
			Apply:         scrambleOrder,
		},
		{
			Name:          "review_drift",
			Description:   "Adds unsolicited recommendations and critique",
			ExpectedDrops: []Dimension{DimNeutrality},
			Apply:         addReviewDrift,
		},
		{
			Name:          "broken_diagram",
			Description:   "Corrupts a diagram so it no longer parses",
			ExpectedDrops: []Dimension{DimDiagrams},
			Apply:         breakDiagram,
		},
	}
}

// Clone deep-copies a walkthrough so a degradation cannot affect the original.
func Clone(w *walkthrough.Walkthrough) *walkthrough.Walkthrough {
	data, err := json.Marshal(w)
	if err != nil {
		return w
	}
	var out walkthrough.Walkthrough
	if err := json.Unmarshal(data, &out); err != nil {
		return w
	}
	return &out
}

// Degrade applies a named degradation to a copy of the walkthrough.
func Degrade(w *walkthrough.Walkthrough, name string) (*walkthrough.Walkthrough, error) {
	for _, d := range Degradations() {
		if d.Name == name {
			c := Clone(w)
			d.Apply(c)
			c.Normalize()
			return c, nil
		}
	}
	return nil, fmt.Errorf("unknown degradation %q", name)
}

func removeEssential(w *walkthrough.Walkthrough) {
	if len(w.Steps) > 1 {
		// Drop the middle step, which is usually where the substance lives.
		i := len(w.Steps) / 2
		w.Steps = append(w.Steps[:i], w.Steps[i+1:]...)
	}
	if len(w.Components) > 0 {
		removed := w.Components[0].ID
		w.Components = w.Components[1:]
		kept := w.Relationships[:0]
		for _, r := range w.Relationships {
			if r.From != removed && r.To != removed {
				kept = append(kept, r)
			}
		}
		w.Relationships = kept
		for i := range w.Steps {
			var comps []string
			for _, id := range w.Steps[i].Components {
				if id != removed {
					comps = append(comps, id)
				}
			}
			w.Steps[i].Components = comps
		}
	}
}

func hallucinate(w *walkthrough.Walkthrough) {
	w.Components = append(w.Components, walkthrough.Component{
		ID:             "phantom-cache",
		Name:           "SessionCache",
		Kind:           "datastore",
		Responsibility: "Caches session state in Redis between requests.",
		Status:         walkthrough.StatusNew,
		Files:          []string{"internal/cache/session_cache.go"},
	})
	if len(w.Steps) > 0 {
		w.Steps[0].Explanation += "\n\nRequests are served from `SessionCache`, a Redis-backed cache that stores session state between requests."
		w.Steps[0].CodeRefs = append(w.Steps[0].CodeRefs, walkthrough.CodeReference{
			Path:      "internal/cache/session_cache.go",
			Symbol:    "SessionCache.Get",
			StartLine: 42,
			EndLine:   88,
		})
	}
}

func reverseFlows(w *walkthrough.Walkthrough) {
	for i := range w.Flows {
		steps := w.Flows[i].Steps
		for a, b := 0, len(steps)-1; a < b; a, b = a+1, b-1 {
			steps[a], steps[b] = steps[b], steps[a]
		}
		for j := range steps {
			steps[j].From, steps[j].To = steps[j].To, steps[j].From
		}
	}
	for i := range w.Diagrams {
		w.Diagrams[i].Source = reverseSequenceLines(w.Diagrams[i].Source)
	}
	for i := range w.Steps {
		for j := range w.Steps[i].Diagrams {
			w.Steps[i].Diagrams[j].Source = reverseSequenceLines(w.Steps[i].Diagrams[j].Source)
		}
	}
}

// reverseSequenceLines flips the order of message lines in a sequence diagram,
// leaving it syntactically valid but factually wrong.
func reverseSequenceLines(src string) string {
	lines := strings.Split(src, "\n")
	if len(lines) < 3 {
		return src
	}
	body := lines[1:]
	for a, b := 0, len(body)-1; a < b; a, b = a+1, b-1 {
		body[a], body[b] = body[b], body[a]
	}
	return strings.Join(append(lines[:1], body...), "\n")
}

const filler = "The handler is registered through the router's standard registration path, which is shared with every other route in the service. " +
	"The struct fields are assigned in declaration order, and the context is threaded through each call as the first argument, matching the convention used throughout the package. " +
	"The imports are grouped into standard library and module blocks, and the file ends with a newline as required by the formatter."

func addVerbosity(w *walkthrough.Walkthrough) {
	for i := range w.Steps {
		w.Steps[i].Explanation += "\n\n" + filler
	}
	w.Summary += " " + filler
}

func scrambleOrder(w *walkthrough.Walkthrough) {
	if len(w.Steps) < 2 {
		return
	}
	first := w.Steps[0]
	w.Steps = append(w.Steps[1:], first)
}

func addReviewDrift(w *walkthrough.Walkthrough) {
	if len(w.Steps) == 0 {
		return
	}
	w.Steps[0].Explanation += "\n\nThis is a problem: the loop should be refactored to avoid the N+1 issue, and I would recommend extracting a service. " +
		"The error handling is missing tests and could be better."
	if len(w.Steps) > 1 {
		w.Steps[1].Explanation += "\n\nThis approach is unnecessarily complex and should be simplified."
	}
}

func breakDiagram(w *walkthrough.Walkthrough) {
	broken := "sequenceDiagram\n    Browser->>API POST /checkout\n    API-->Service"
	if len(w.Diagrams) > 0 {
		w.Diagrams[0].Source = broken
		return
	}
	for i := range w.Steps {
		if len(w.Steps[i].Diagrams) > 0 {
			w.Steps[i].Diagrams[0].Source = broken
			return
		}
	}
	w.Diagrams = append(w.Diagrams, walkthrough.Diagram{
		ID:     "broken",
		Format: walkthrough.DiagramMermaid,
		Source: broken,
	})
}
