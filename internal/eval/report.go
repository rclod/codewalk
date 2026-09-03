package eval

import (
	"fmt"
	"io"
	"strings"
)

// Report writes a readable quality report. The report is a view over the
// structured result: everything in it is available as JSON for dashboards and
// regression tracking.
func Report(out io.Writer, r *Result) error {
	p := func(format string, args ...any) { fmt.Fprintf(out, format, args...) }

	title := "Walkthrough quality report"
	if r.CaseID != "" {
		title += " — " + r.CaseID
	}
	p("%s\n%s\n\n", title, strings.Repeat("─", len(title)))

	verdict := "PASSED"
	if !r.Passed() {
		verdict = "FAILED"
	}
	p("Result: %s   ·   average score %.2f / 5   ·   mode %s\n", verdict, float64(r.Average()), r.Mode)
	if r.RunID != "" {
		p("Run: %s\n", r.RunID)
	}
	if len(r.Judges) > 0 {
		p("Judges: %s\n", strings.Join(r.Judges, ", "))
	}
	p("\n")

	p("Quality gates\n")
	for _, g := range r.Gates {
		mark := "✓"
		if !g.Passed {
			mark = "✗"
		}
		line := fmt.Sprintf("  %s %-24s", mark, g.Name)
		if g.Detail != "" && !g.Passed {
			line += " " + g.Detail
		}
		p("%s\n", line)
	}
	p("\n")

	p("Dimensions\n")
	for _, d := range r.SortedDimensions() {
		if !d.Applicable {
			p("  %-24s   n/a   %s\n", d.Dimension, d.Reasoning)
			continue
		}
		p("  %-24s %5.1f   %s\n", d.Dimension, float64(d.Score), truncate(d.Reasoning, 90))
	}
	p("\n")

	det := r.Deterministic
	p("Deterministic checks\n")
	p("  schema:      %s\n", passFail(det.SchemaValid))
	if det.RefsVerified {
		p("  references:  %d checked, %d broken, %d imprecise\n", det.RefsChecked, det.RefsBroken, det.RefsWeak)
	} else {
		p("  references:  %d found, none verified (no repository available)\n", det.RefsChecked)
	}
	if det.DiagramsChecked > 0 {
		p("  diagrams:    %d checked, %d invalid\n", det.DiagramsChecked, det.DiagramsInvalid)
	}
	p("  structure:   %d steps, %d without code references, ~%d words\n", det.StepCount, det.StepsWithoutRefs, det.WordCount)
	if len(det.ReviewDrift) > 0 {
		p("  review drift: %d phrase(s)\n", len(det.ReviewDrift))
		for i, d := range det.ReviewDrift {
			if i >= 5 {
				p("      … %d more\n", len(det.ReviewDrift)-i)
				break
			}
			p("      %s: %q\n", d.Where, d.Phrase)
		}
	}
	p("\n")

	if weak := r.Weakest(3); len(weak) > 0 {
		p("Weakest dimensions\n")
		for _, d := range weak {
			p("  %s (%.1f): %s\n", d.Dimension, float64(d.Score), d.Reasoning)
			printList(out, "missing", d.MissingConcepts)
			printList(out, "unsupported", d.UnsupportedClaims)
			printList(out, "contradicted", d.Contradictions)
			printList(out, "unnecessary", d.UnnecessaryContent)
			printList(out, "order", d.OrderProblems)
			printList(out, "note", d.Notes)
		}
		p("\n")
	}

	if len(r.Disagreements) > 0 {
		p("Judge disagreements\n")
		for _, d := range r.Disagreements {
			p("  · %s\n", d)
		}
		p("\n")
	}

	if r.GenerationDurationMS > 0 || r.GenerationTokens > 0 {
		p("Cost and latency\n")
		p("  generation: %.1fs, %d model calls, %d tokens\n",
			float64(r.GenerationDurationMS)/1000, r.GenerationModelCalls, r.GenerationTokens)
		p("  evaluation: %.1fs, %d tokens\n", float64(r.EvaluationDurationMS)/1000, r.EvaluationTokens)
	}
	return nil
}

func printList(out io.Writer, label string, items []string) {
	for i, item := range items {
		if i >= 4 {
			fmt.Fprintf(out, "      … %d more %s\n", len(items)-i, label)
			return
		}
		fmt.Fprintf(out, "      %s: %s\n", label, truncate(item, 110))
	}
}

func passFail(ok bool) string {
	if ok {
		return "valid"
	}
	return "INVALID"
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// SuiteReport summarises a whole benchmark corpus run.
type SuiteReport struct {
	Cases []CaseResult `json:"cases"`
}

// CaseResult is one benchmark case's outcome.
type CaseResult struct {
	CaseID string  `json:"case_id"`
	RunID  string  `json:"run_id,omitempty"`
	Passed bool    `json:"passed"`
	Score  Score   `json:"score"`
	Error  string  `json:"error,omitempty"`
	Result *Result `json:"result,omitempty"`
}

// WriteSuite renders a corpus summary.
func WriteSuite(out io.Writer, s *SuiteReport) error {
	fmt.Fprintf(out, "Benchmark suite: %d case(s)\n\n", len(s.Cases))
	passed := 0
	var total Score
	scored := 0
	for _, c := range s.Cases {
		switch {
		case c.Error != "":
			fmt.Fprintf(out, "  ✗ %-28s error: %s\n", c.CaseID, truncate(c.Error, 90))
		case c.Passed:
			passed++
			total += c.Score
			scored++
			fmt.Fprintf(out, "  ✓ %-28s %.2f\n", c.CaseID, float64(c.Score))
		default:
			total += c.Score
			scored++
			fmt.Fprintf(out, "  ✗ %-28s %.2f  gates failed: %s\n", c.CaseID, float64(c.Score), failedGates(c.Result))
		}
	}
	fmt.Fprintf(out, "\n%d/%d passed", passed, len(s.Cases))
	if scored > 0 {
		fmt.Fprintf(out, ", mean score %.2f", float64(total)/float64(scored))
	}
	fmt.Fprintln(out)
	return nil
}

func failedGates(r *Result) string {
	if r == nil {
		return ""
	}
	var names []string
	for _, g := range r.Gates {
		if !g.Passed {
			names = append(names, g.Name)
		}
	}
	return strings.Join(names, ", ")
}
