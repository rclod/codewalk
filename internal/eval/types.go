// Package eval measures walkthrough quality.
//
// Quality is treated as a first-class product capability rather than a test
// suite bolted on afterwards: walkthroughs are measurable, comparable and
// reproducible, and the reasons behind a score are preserved alongside the
// number.
//
// Two principles shape the design:
//
//   - Deterministic checks come first. Whether a file exists, whether a line
//     range is valid, whether a diagram parses — these are answered by tooling,
//     not by a model, because tooling is cheaper and does not hallucinate.
//   - Quality is not one number. A polished walkthrough that invents a
//     component is worse than a plain one that does not, so some dimensions act
//     as gates rather than contributing to an average.
package eval

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Dimension is an independently scored aspect of walkthrough quality.
type Dimension string

const (
	// DimGrounding asks whether important claims are supported by the repository.
	DimGrounding Dimension = "grounding"
	// DimCoverage asks whether the walkthrough explains what a human needs.
	DimCoverage Dimension = "essential_coverage"
	// DimMentalModel asks whether the overall picture is correct, even when
	// individual statements are.
	DimMentalModel Dimension = "mental_model_accuracy"
	// DimSelectivity asks whether low-value content was left out.
	DimSelectivity Dimension = "selectivity"
	// DimTeachingOrder asks whether concepts arrive when the reader needs them.
	DimTeachingOrder Dimension = "teaching_order"
	// DimDepth asks whether detail matches conceptual complexity.
	DimDepth Dimension = "depth_calibration"
	// DimBeforeAfter asks whether the behavioural difference is clear.
	DimBeforeAfter Dimension = "before_after_clarity"
	// DimNavigability asks whether a reader can move between explanation and code.
	DimNavigability Dimension = "navigability"
	// DimConcision asks whether content could be removed without loss.
	DimConcision Dimension = "concision"
	// DimNeutrality asks whether the walkthrough explained rather than graded.
	DimNeutrality Dimension = "neutrality"
	// DimDiagrams asks whether diagrams reduced cognitive load.
	DimDiagrams Dimension = "diagram_utility"
)

// AllDimensions lists every dimension in report order.
var AllDimensions = []Dimension{
	DimGrounding, DimCoverage, DimMentalModel, DimSelectivity, DimTeachingOrder,
	DimDepth, DimBeforeAfter, DimNavigability, DimConcision, DimNeutrality, DimDiagrams,
}

// DimensionDescription explains what a dimension measures, and is shown in
// reports so a score is interpretable without reading this source.
var DimensionDescription = map[Dimension]string{
	DimGrounding:     "Important claims are supported by repository evidence",
	DimCoverage:      "The essential understanding is present",
	DimMentalModel:   "The overall picture of how things fit together is correct",
	DimSelectivity:   "Low-value content was left out",
	DimTeachingOrder: "Concepts are introduced before they are needed",
	DimDepth:         "Detail matches the conceptual complexity of the subject",
	DimBeforeAfter:   "The behavioural difference is clear",
	DimNavigability:  "A reader can move between explanation and code",
	DimConcision:     "Nothing could be removed without reducing understanding",
	DimNeutrality:    "The walkthrough explains rather than grades",
	DimDiagrams:      "Diagrams reduce the effort of understanding",
}

// Score is a 0..5 rating. Half points are meaningful; finer precision is not.
type Score float64

// DimensionResult is one dimension's outcome, with the observations that
// produced it. The observations are the point: a number tells you a result, the
// observations tell you why.
type DimensionResult struct {
	Dimension Dimension `json:"dimension"`
	Score     Score     `json:"score"`
	// Method is "deterministic", "model" or "combined".
	Method string `json:"method"`
	// Applicable is false when a dimension does not apply, for example
	// diagram utility for a walkthrough with no diagrams. Inapplicable
	// dimensions are excluded from averages rather than scored zero.
	Applicable bool   `json:"applicable"`
	Reasoning  string `json:"reasoning,omitempty"`

	// Structured observations. Which fields are populated depends on the
	// dimension; empty ones are omitted.
	UnsupportedClaims  []string `json:"unsupported_claims,omitempty"`
	Contradictions     []string `json:"contradictions,omitempty"`
	CoveredConcepts    []string `json:"covered_concepts,omitempty"`
	MissingConcepts    []string `json:"missing_concepts,omitempty"`
	UnnecessaryContent []string `json:"unnecessary_content,omitempty"`
	OrderProblems      []string `json:"order_problems,omitempty"`
	Notes              []string `json:"notes,omitempty"`
}

// Result is a complete evaluation of one walkthrough.
type Result struct {
	WalkthroughID string    `json:"walkthrough_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	CaseID        string    `json:"case_id,omitempty"`
	EvaluatedAt   time.Time `json:"evaluated_at"`
	Mode          string    `json:"mode"`

	Deterministic DeterministicResult           `json:"deterministic"`
	Dimensions    map[Dimension]DimensionResult `json:"dimensions"`
	Gates         []GateResult                  `json:"gates"`

	// Judges records which backends produced the semantic scores, so a result
	// can be attributed and disagreement can be investigated.
	Judges []string `json:"judges,omitempty"`
	// Disagreements records dimensions where judges differed materially.
	Disagreements []string `json:"disagreements,omitempty"`

	// Cost and latency of the generation being evaluated, when known. They are
	// evaluation dimensions in their own right, just not quality ones.
	GenerationDurationMS int64 `json:"generation_duration_ms,omitempty"`
	GenerationTokens     int   `json:"generation_tokens,omitempty"`
	GenerationModelCalls int   `json:"generation_model_calls,omitempty"`

	// EvaluationDurationMS and EvaluationTokens are the cost of evaluating.
	EvaluationDurationMS int64 `json:"evaluation_duration_ms,omitempty"`
	EvaluationTokens     int   `json:"evaluation_tokens,omitempty"`
}

// Passed reports whether every gate passed.
func (r *Result) Passed() bool {
	for _, g := range r.Gates {
		if !g.Passed {
			return false
		}
	}
	return true
}

// Average returns the mean of applicable dimension scores. It is a summary, not
// a verdict: gates decide whether a walkthrough is acceptable at all.
func (r *Result) Average() Score {
	var sum Score
	var n int
	for _, d := range r.Dimensions {
		if d.Applicable {
			sum += d.Score
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / Score(n)
}

// SortedDimensions returns dimension results in report order.
func (r *Result) SortedDimensions() []DimensionResult {
	out := make([]DimensionResult, 0, len(r.Dimensions))
	for _, dim := range AllDimensions {
		if d, ok := r.Dimensions[dim]; ok {
			out = append(out, d)
		}
	}
	// Include any dimension not in the canonical list, so nothing is hidden.
	for dim, d := range r.Dimensions {
		if !knownDimension(dim) {
			out = append(out, d)
		}
	}
	return out
}

func knownDimension(d Dimension) bool {
	for _, k := range AllDimensions {
		if k == d {
			return true
		}
	}
	return false
}

// Weakest returns the lowest-scoring applicable dimensions.
func (r *Result) Weakest(n int) []DimensionResult {
	all := r.SortedDimensions()
	applicable := all[:0]
	for _, d := range all {
		if d.Applicable {
			applicable = append(applicable, d)
		}
	}
	sort.SliceStable(applicable, func(i, j int) bool { return applicable[i].Score < applicable[j].Score })
	if len(applicable) > n {
		applicable = applicable[:n]
	}
	return applicable
}

// GateResult is the outcome of a hard quality gate. Gates exist because some
// failures cannot be offset by polish elsewhere.
type GateResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
	// Blocking gates fail the evaluation; non-blocking gates are warnings.
	Blocking bool `json:"blocking"`
}

func (g GateResult) String() string {
	status := "pass"
	if !g.Passed {
		status = "FAIL"
	}
	if g.Detail == "" {
		return fmt.Sprintf("%s: %s", g.Name, status)
	}
	return fmt.Sprintf("%s: %s (%s)", g.Name, status, g.Detail)
}

// Mode selects how much evaluation to perform.
type Mode string

const (
	// ModeSmoke runs only deterministic checks. It is cheap enough to run on
	// every generated walkthrough.
	ModeSmoke Mode = "smoke"
	// ModeStandard adds independent semantic judging.
	ModeStandard Mode = "standard"
	// ModeFull adds multiple judges and adjudication.
	ModeFull Mode = "full"
)

// ParseMode validates an evaluation mode.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case "", ModeSmoke:
		return ModeSmoke, nil
	case ModeStandard:
		return ModeStandard, nil
	case ModeFull:
		return ModeFull, nil
	default:
		return "", fmt.Errorf("unknown evaluation mode %q (want smoke, standard or full)", s)
	}
}
