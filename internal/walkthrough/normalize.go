package walkthrough

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// Normalize fills in derived and structural fields that authors should not have
// to produce by hand: identifiers, schema version, complexity labels and path
// hygiene. It is idempotent and never invents explanatory content.
func (w *Walkthrough) Normalize() {
	if w == nil {
		return
	}
	w.SchemaVersion = SchemaVersion
	if w.Kind == "" {
		w.Kind = KindChange
	}
	if w.Meta.GeneratedAt.IsZero() {
		w.Meta.GeneratedAt = time.Now().UTC()
	}

	if w.Complexity.Level < 1 {
		w.Complexity.Level = 1
	}
	if w.Complexity.Level > 5 {
		w.Complexity.Level = 5
	}
	if w.Complexity.Label == "" {
		w.Complexity.Label = ComplexityLabel(w.Complexity.Level)
	}

	seen := map[string]bool{}
	unique := func(prefix, id string, i int) string {
		id = strings.TrimSpace(id)
		if id == "" {
			id = fmt.Sprintf("%s%d", prefix, i+1)
		}
		base := id
		for n := 2; seen[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		seen[id] = true
		return id
	}

	for i := range w.Concepts {
		w.Concepts[i].ID = unique("concept", w.Concepts[i].ID, i)
		normalizeRefs(w.Concepts[i].CodeRefs)
	}
	for i := range w.Components {
		w.Components[i].ID = unique("component", w.Components[i].ID, i)
		for j := range w.Components[i].Files {
			w.Components[i].Files[j] = normalizePath(w.Components[i].Files[j])
		}
		normalizeRefs(w.Components[i].CodeRefs)
	}
	for i := range w.Flows {
		w.Flows[i].ID = unique("flow", w.Flows[i].ID, i)
		for j := range w.Flows[i].Steps {
			if r := w.Flows[i].Steps[j].CodeRef; r != nil {
				r.Path = normalizePath(r.Path)
			}
		}
	}
	for i := range w.Steps {
		w.Steps[i].ID = unique("step", w.Steps[i].ID, i)
		normalizeRefs(w.Steps[i].CodeRefs)
		for j := range w.Steps[i].Diagrams {
			w.Steps[i].Diagrams[j].ID = unique("diagram", w.Steps[i].Diagrams[j].ID, j)
			normalizeDiagram(&w.Steps[i].Diagrams[j])
		}
		if dd := w.Steps[i].DeepDive; dd != nil {
			normalizeRefs(dd.CodeRefs)
			for j := range dd.Diagrams {
				dd.Diagrams[j].ID = unique("diagram", dd.Diagrams[j].ID, j)
				normalizeDiagram(&dd.Diagrams[j])
			}
		}
	}
	for i := range w.Diagrams {
		w.Diagrams[i].ID = unique("diagram", w.Diagrams[i].ID, i)
		normalizeDiagram(&w.Diagrams[i])
	}
	for i := range w.Evidence {
		w.Evidence[i].ID = unique("evidence", w.Evidence[i].ID, i)
	}
	normalizeRefs(w.StartHere)
	for i := range w.Ignorable {
		w.Ignorable[i].Path = normalizePath(w.Ignorable[i].Path)
	}
	for i := range w.Scope.ChangedFiles {
		w.Scope.ChangedFiles[i].Path = normalizePath(w.Scope.ChangedFiles[i].Path)
		w.Scope.ChangedFiles[i].OldPath = normalizePath(w.Scope.ChangedFiles[i].OldPath)
	}
}

func normalizeDiagram(d *Diagram) {
	if d.Format == "" {
		d.Format = DiagramMermaid
	}
	d.Source = strings.TrimSpace(stripFence(d.Source))
}

// stripFence removes a Markdown code fence that models frequently wrap around
// diagram sources.
func stripFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	}
	if j := strings.LastIndex(t, "```"); j >= 0 {
		t = t[:j]
	}
	return t
}

func normalizeRefs(refs []CodeReference) {
	for i := range refs {
		refs[i].Path = normalizePath(refs[i].Path)
		if refs[i].EndLine > 0 && refs[i].StartLine > refs[i].EndLine {
			refs[i].StartLine, refs[i].EndLine = refs[i].EndLine, refs[i].StartLine
		}
	}
}

// normalizePath makes a reference path repository-relative and slash-separated.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	return path.Clean(p)
}

// AllDiagrams returns every diagram in the walkthrough, including those nested
// in steps and deep dives.
func (w *Walkthrough) AllDiagrams() []Diagram {
	var out []Diagram
	out = append(out, w.Diagrams...)
	for _, s := range w.Steps {
		out = append(out, s.Diagrams...)
		if s.DeepDive != nil {
			out = append(out, s.DeepDive.Diagrams...)
		}
	}
	return out
}

// AllCodeRefs returns every code reference in the walkthrough.
func (w *Walkthrough) AllCodeRefs() []CodeReference {
	var out []CodeReference
	out = append(out, w.StartHere...)
	for _, c := range w.Concepts {
		out = append(out, c.CodeRefs...)
	}
	for _, c := range w.Components {
		out = append(out, c.CodeRefs...)
	}
	for _, s := range w.Steps {
		out = append(out, s.CodeRefs...)
		if s.DeepDive != nil {
			out = append(out, s.DeepDive.CodeRefs...)
		}
	}
	for _, f := range w.Flows {
		for _, st := range f.Steps {
			if st.CodeRef != nil {
				out = append(out, *st.CodeRef)
			}
		}
	}
	return out
}

// ComponentByID looks up a component.
func (w *Walkthrough) ComponentByID(id string) (Component, bool) {
	for _, c := range w.Components {
		if c.ID == id {
			return c, true
		}
	}
	return Component{}, false
}

// WordCount returns the approximate number of prose words a reader is asked to
// consume at the default disclosure level (deep dives excluded). It is used by
// depth-calibration and concision evaluators.
func (w *Walkthrough) WordCount() int {
	n := len(strings.Fields(w.Headline)) + len(strings.Fields(w.Summary))
	for _, c := range w.Concepts {
		n += len(strings.Fields(c.Summary)) + len(strings.Fields(c.WhyItMatters))
	}
	for _, s := range w.Steps {
		n += len(strings.Fields(s.Summary)) + len(strings.Fields(s.Explanation))
	}
	if w.Architecture != nil {
		n += len(strings.Fields(w.Architecture.Overview))
	}
	return n
}
