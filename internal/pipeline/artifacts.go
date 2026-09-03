package pipeline

import (
	"encoding/json"

	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// The artifacts below are the typed hand-offs between pipeline stages. They are
// persisted with the run so a walkthrough can be explained, reproduced and
// evaluated stage by stage — which is what makes it possible to attribute a
// quality change to one stage rather than to the pipeline as a whole.

// EvidenceReport is the Investigator's output: what was established, and where.
type EvidenceReport struct {
	Purpose           string                        `json:"purpose"`
	PurposeConfidence string                        `json:"purpose_confidence"`
	Findings          []Finding                     `json:"findings"`
	Components        []walkthrough.Component       `json:"components"`
	Relationships     []walkthrough.Relationship    `json:"relationships"`
	Flows             []walkthrough.Flow            `json:"flows"`
	BeforeAfter       []walkthrough.BeforeAfterItem `json:"before_after"`
	StateChanges      []string                      `json:"state_changes"`
	Incidental        []walkthrough.Ignorable       `json:"incidental"`
	Unresolved        []walkthrough.Uncertainty     `json:"unresolved"`
	Complexity        walkthrough.Complexity        `json:"complexity"`

	// FilesInspected is filled by the pipeline from tool provenance rather than
	// by the model, so it reflects what was actually read.
	FilesInspected []string `json:"files_inspected,omitempty"`
}

// Finding is one established fact with its supporting references.
type Finding struct {
	Statement string                      `json:"statement"`
	Evidence  []walkthrough.CodeReference `json:"evidence"`
	// Importance is essential, supporting or incidental. It feeds selectivity:
	// incidental findings should not consume walkthrough attention.
	Importance string `json:"importance"`
}

// MentalModel is what a reader needs to understand, before any decision about
// how to teach it.
type MentalModel struct {
	Purpose        string                      `json:"purpose"`
	Headline       string                      `json:"headline"`
	Complexity     walkthrough.Complexity      `json:"complexity"`
	Concepts       []walkthrough.Concept       `json:"concepts"`
	Components     []walkthrough.Component     `json:"components"`
	Relationships  []walkthrough.Relationship  `json:"relationships"`
	Flows          []walkthrough.Flow          `json:"flows"`
	BeforeAfter    *walkthrough.BeforeAfter    `json:"before_after"`
	Architecture   *walkthrough.Architecture   `json:"architecture"`
	MustUnderstand []string                    `json:"must_understand"`
	CanIgnore      []walkthrough.Ignorable     `json:"can_ignore"`
	Glossary       []walkthrough.GlossaryEntry `json:"glossary"`
	Uncertainties  []walkthrough.Uncertainty   `json:"uncertainties"`
	StartHere      []walkthrough.CodeReference `json:"start_here"`
}

// Plan is the teaching plan: order, shape and where diagrams earn their place.
type Plan struct {
	ComplexityLevel     int           `json:"complexity_level"`
	TargetShape         string        `json:"target_shape"`
	IncludeArchitecture bool          `json:"include_architecture"`
	IncludeBeforeAfter  bool          `json:"include_before_after"`
	Steps               []PlannedStep `json:"steps"`
	Omit                []string      `json:"omit"`
	OrderRationale      string        `json:"order_rationale"`
}

// PlannedStep is one planned unit of teaching.
type PlannedStep struct {
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Goal       string   `json:"goal"`
	Concepts   []string `json:"concepts"`
	Components []string `json:"components"`
	FlowID     string   `json:"flow_id"`
	Diagram    struct {
		Needed  bool   `json:"needed"`
		Type    string `json:"type"`
		Purpose string `json:"purpose"`
	} `json:"diagram"`
	DeepDive struct {
		Needed bool   `json:"needed"`
		About  string `json:"about"`
	} `json:"deep_dive"`
	CodeRefs []struct {
		Path   string `json:"path"`
		Symbol string `json:"symbol"`
		Why    string `json:"why"`
	} `json:"code_refs"`
}

// EditorResult is the clarity editor's revised walkthrough plus its rationale.
type EditorResult struct {
	Walkthrough walkthrough.Walkthrough `json:"walkthrough"`
	Edits       []string                `json:"edits"`
}

// GroundingReport records verification of the walkthrough against the source.
type GroundingReport struct {
	Verdict      string       `json:"verdict"`
	Contradicted []ClaimIssue `json:"contradicted"`
	Unsupported  []ClaimIssue `json:"unsupported"`
	InvalidRefs  []struct {
		Path   string `json:"path"`
		Symbol string `json:"symbol"`
		Why    string `json:"why"`
	} `json:"invalid_references"`
	MissingEssential []struct {
		What     string `json:"what"`
		Evidence string `json:"evidence"`
	} `json:"missing_essential"`
	Confirmed []string `json:"confirmed"`

	// PrunedRefs records references removed by deterministic checking.
	PrunedRefs []string `json:"pruned_refs,omitempty"`
}

// ClaimIssue is one problematic explanatory claim.
type ClaimIssue struct {
	StepID     string `json:"step_id"`
	Claim      string `json:"claim"`
	Evidence   string `json:"evidence,omitempty"`
	Why        string `json:"why,omitempty"`
	Correction string `json:"correction,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// CorrectionResult carries narrow, evidence-driven fixes to specific steps.
type CorrectionResult struct {
	Steps []struct {
		ID          string `json:"id"`
		Explanation string `json:"explanation"`
		Summary     string `json:"summary"`
	} `json:"steps"`
	Uncertainties []walkthrough.Uncertainty `json:"uncertainties"`
	Notes         []string                  `json:"notes"`
}

// StageRecord is the execution record of one pipeline stage.
type StageRecord struct {
	Name          string    `json:"name"`
	Backend       string    `json:"backend"`
	BackendKind   string    `json:"backend_kind,omitempty"`
	Model         string    `json:"model,omitempty"`
	PromptVersion string    `json:"prompt_version,omitempty"`
	DurationMS    int64     `json:"duration_ms"`
	Usage         llm.Usage `json:"usage"`
	Steps         int       `json:"steps,omitempty"`
	ToolCalls     int       `json:"tool_calls,omitempty"`
	Truncated     bool      `json:"truncated,omitempty"`
	Skipped       string    `json:"skipped,omitempty"`
	Error         string    `json:"error,omitempty"`
	// Repairs counts JSON repair retries, a useful signal when comparing
	// backends: a model that needs repairs is costing more than it appears to.
	Repairs int `json:"repairs,omitempty"`
}

// Artifacts holds the serialised stage outputs kept with a run.
type Artifacts map[string]json.RawMessage

func (a Artifacts) set(name string, v any) {
	if a == nil {
		return
	}
	if data, err := json.MarshalIndent(v, "", "  "); err == nil {
		a[name] = data
	}
}
