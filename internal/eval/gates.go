package eval

import "fmt"

// Gate thresholds. A walkthrough that fails one of these is not made acceptable
// by scoring well elsewhere: a beautifully written explanation of a component
// that does not exist is worse than a plain one that is correct.
const (
	gateMinGrounding   = 3.0
	gateMinCoverage    = 3.0
	gateMinMentalModel = 3.0
	gateMinNeutrality  = 3.0
)

// computeGates evaluates the hard quality gates.
func computeGates(r *Result) []GateResult {
	var gates []GateResult

	gates = append(gates, GateResult{
		Name:     "schema_valid",
		Passed:   r.Deterministic.SchemaValid,
		Blocking: true,
		Detail:   schemaDetail(r),
	})

	if r.Deterministic.RefsVerified {
		gates = append(gates, GateResult{
			Name:     "references_resolve",
			Passed:   r.Deterministic.RefsBroken == 0,
			Blocking: true,
			Detail:   fmt.Sprintf("%d of %d references do not resolve", r.Deterministic.RefsBroken, r.Deterministic.RefsChecked),
		})
	}

	if r.Deterministic.DiagramsChecked > 0 {
		gates = append(gates, GateResult{
			Name:     "diagrams_render",
			Passed:   r.Deterministic.DiagramsInvalid == 0,
			Blocking: true,
			Detail:   fmt.Sprintf("%d of %d diagrams fail to parse", r.Deterministic.DiagramsInvalid, r.Deterministic.DiagramsChecked),
		})
	}

	gates = append(gates, dimensionGate(r, "grounding", DimGrounding, gateMinGrounding))
	gates = append(gates, dimensionGate(r, "essential_coverage", DimCoverage, gateMinCoverage))
	gates = append(gates, dimensionGate(r, "mental_model_accuracy", DimMentalModel, gateMinMentalModel))
	gates = append(gates, dimensionGate(r, "no_review_drift", DimNeutrality, gateMinNeutrality))

	out := gates[:0]
	for _, g := range gates {
		if g.Name != "" {
			out = append(out, g)
		}
	}
	return out
}

// dimensionGate builds a gate for a scored dimension. Dimensions that were not
// scored (for example when running in smoke mode) produce no gate rather than a
// failure: an unmeasured dimension is unknown, not bad.
func dimensionGate(r *Result, name string, dim Dimension, threshold float64) GateResult {
	d, ok := r.Dimensions[dim]
	if !ok || !d.Applicable {
		return GateResult{}
	}
	return GateResult{
		Name:     name,
		Passed:   float64(d.Score) >= threshold,
		Blocking: true,
		Detail:   fmt.Sprintf("scored %.1f, minimum %.1f", float64(d.Score), threshold),
	}
}

func schemaDetail(r *Result) string {
	if r.Deterministic.SchemaValid {
		return ""
	}
	for _, issue := range r.Deterministic.SchemaIssues {
		if issue.Severity == "error" {
			return issue.String()
		}
	}
	return "walkthrough is structurally invalid"
}
