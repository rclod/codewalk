// Package walkthrough defines the canonical representation of a guided code
// walkthrough.
//
// A Walkthrough is the product's central domain object. The CLI, the HTTP API,
// the embedded web UI, persistence and the evaluation system all operate on
// this single representation rather than inventing their own output models.
//
// The schema is deliberately shaped around *concepts* rather than files: files
// appear as supporting navigation (code references and evidence), never as the
// primary organising principle. It is also deliberately loose in places —
// explanations are prose, and most structured sections are optional — so that a
// generated walkthrough reads like an explanation rather than a filled-in form.
package walkthrough

import "time"

// SchemaVersion identifies the canonical walkthrough schema. It is bumped when
// a change would break consumers that were written against an earlier version.
const SchemaVersion = "1"

// Kind distinguishes the two first-class walkthrough types.
type Kind string

const (
	// KindChange explains the behavioural and architectural meaning of a change
	// (working tree, staged changes, a branch, a commit or a commit range).
	KindChange Kind = "change"
	// KindCodebase explains the architecture and important behaviour of a whole
	// repository.
	KindCodebase Kind = "codebase"
)

// Walkthrough is the canonical, serialisable result of a walkthrough run.
type Walkthrough struct {
	SchemaVersion string `json:"schema_version"`
	ID            string `json:"id"`
	Kind          Kind   `json:"kind"`

	// Title is a short human-facing name for what is being explained.
	Title string `json:"title"`
	// Headline answers "what is this?" in one or two sentences, before any
	// detail. It is the first thing a reader sees in every surface.
	Headline string `json:"headline"`
	// Summary is a short orienting paragraph: the smallest sufficient
	// description of the change or system.
	Summary string `json:"summary"`

	Scope      Scope      `json:"scope"`
	Complexity Complexity `json:"complexity"`

	// Concepts are the ideas a reader needs in order to understand the rest.
	Concepts []Concept `json:"concepts,omitempty"`
	// Components are the participating parts of the system. One component may
	// span many files; one file may participate in several components.
	Components []Component `json:"components,omitempty"`
	// Relationships describe how components interact.
	Relationships []Relationship `json:"relationships,omitempty"`

	// Steps are the ordered teaching sequence. Order is chosen to minimise
	// cognitive load, which frequently differs from diff, file or call order.
	Steps []Step `json:"steps"`

	Architecture *Architecture `json:"architecture,omitempty"`
	Flows        []Flow        `json:"flows,omitempty"`
	BeforeAfter  *BeforeAfter  `json:"before_after,omitempty"`

	// Diagrams holds walkthrough-level diagrams. Steps may also carry their own.
	Diagrams []Diagram `json:"diagrams,omitempty"`

	// StartHere points at the code a reader should open first.
	StartHere []CodeReference `json:"start_here,omitempty"`
	// Ignorable records what can safely be skipped for comprehension purposes.
	Ignorable []Ignorable `json:"ignorable,omitempty"`

	Glossary      []GlossaryEntry `json:"glossary,omitempty"`
	Uncertainties []Uncertainty   `json:"uncertainties,omitempty"`
	Evidence      []Evidence      `json:"evidence,omitempty"`

	Meta Meta `json:"meta"`
}

// Scope records exactly what was explained, so a walkthrough can be reproduced,
// re-evaluated and cached.
type Scope struct {
	// RepositoryName is the repository directory name. It is used for display
	// and for cache identity; it is not a remote identifier.
	RepositoryName string `json:"repository_name"`
	// RepositoryPath is the absolute path that was analysed. It is local
	// machine state: it is written into run metadata but is never part of a
	// distributable artifact such as a benchmark case.
	RepositoryPath string `json:"repository_path,omitempty"`
	// RemoteHost is the host portion of the origin remote (for example
	// "github.com") when one exists. The full remote URL is intentionally not
	// stored.
	RemoteHost string `json:"remote_host,omitempty"`

	// Selector describes how the change was selected.
	Selector string `json:"selector,omitempty"` // working-tree | staged | branch | range | commit | repository

	Base       string `json:"base,omitempty"`
	Head       string `json:"head,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	HeadCommit string `json:"head_commit,omitempty"`
	Branch     string `json:"branch,omitempty"`

	ChangedFiles []ChangedFile `json:"changed_files,omitempty"`
	Stats        ChangeStats   `json:"stats,omitempty"`

	// Subtree restricts a codebase walkthrough to part of a repository.
	Subtree string `json:"subtree,omitempty"`
}

// ChangeStats summarises the size of a change.
type ChangeStats struct {
	FilesChanged int `json:"files_changed"`
	Insertions   int `json:"insertions"`
	Deletions    int `json:"deletions"`
}

// ChangedFile is one path touched by the change under explanation.
type ChangedFile struct {
	Path       string `json:"path"`
	OldPath    string `json:"old_path,omitempty"`
	Status     string `json:"status"` // added | modified | deleted | renamed | copied
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	Binary     bool   `json:"binary,omitempty"`
	// Generated marks files that appear machine-generated (lockfiles, vendored
	// code, compiled protobuf output). They are usually incidental.
	Generated bool `json:"generated,omitempty"`
}

// Complexity is the internal conceptual complexity classification that
// calibrates walkthrough depth. It answers "how much context does a human need
// to confidently understand this?" and says nothing about defect risk.
type Complexity struct {
	Level     int      `json:"level"` // 1 trivial .. 5 systemic
	Label     string   `json:"label"`
	Rationale string   `json:"rationale,omitempty"`
	Factors   []string `json:"factors,omitempty"`
}

// ComplexityLabel maps a level to its canonical label.
func ComplexityLabel(level int) string {
	switch level {
	case 1:
		return "trivial"
	case 2:
		return "local"
	case 3:
		return "multi-component"
	case 4:
		return "architectural"
	case 5:
		return "systemic"
	default:
		return "unknown"
	}
}

// Concept is an idea the reader needs in order to follow the explanation.
type Concept struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Summary      string `json:"summary"`
	WhyItMatters string `json:"why_it_matters,omitempty"`
	// Preexisting marks concepts that already existed in the system, as opposed
	// to concepts introduced by the change.
	Preexisting bool            `json:"preexisting,omitempty"`
	CodeRefs    []CodeReference `json:"code_refs,omitempty"`
}

// ComponentStatus describes how a component relates to the change.
type ComponentStatus string

const (
	StatusExisting ComponentStatus = "existing"
	StatusNew      ComponentStatus = "new"
	StatusChanged  ComponentStatus = "changed"
	StatusRemoved  ComponentStatus = "removed"
	StatusExternal ComponentStatus = "external"
)

// Component is a conceptual part of the system: a subsystem, service, module,
// worker, boundary, datastore or external integration.
type Component struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Kind           string          `json:"kind,omitempty"` // subsystem, service, module, worker, queue, datastore, api, ui, external, ...
	Responsibility string          `json:"responsibility"`
	Status         ComponentStatus `json:"status,omitempty"`
	Files          []string        `json:"files,omitempty"`
	CodeRefs       []CodeReference `json:"code_refs,omitempty"`
	Notes          string          `json:"notes,omitempty"`
}

// Relationship is a directed edge between two components.
type Relationship struct {
	From        string `json:"from"` // component ID
	To          string `json:"to"`   // component ID
	Kind        string `json:"kind"` // calls, publishes, consumes, persists, reads, depends_on, implements, http, ...
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	// Status marks edges introduced or removed by the change.
	Status ComponentStatus `json:"status,omitempty"`
}

// StepKind is a hint about a step's teaching role. It drives presentation (for
// example, before/after steps render as a comparison) and helps the evaluation
// system reason about teaching order.
type StepKind string

const (
	StepContext     StepKind = "context"
	StepBefore      StepKind = "before"
	StepAfter       StepKind = "after"
	StepComponent   StepKind = "component"
	StepFlow        StepKind = "flow"
	StepData        StepKind = "data"
	StepState       StepKind = "state"
	StepIntegration StepKind = "integration"
	StepOutcome     StepKind = "outcome"
)

// Step is one unit of the ordered teaching sequence.
type Step struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Kind  StepKind `json:"kind,omitempty"`
	// Summary is a single sentence: what the reader will understand after this
	// step. Surfaces use it for navigation and progressive disclosure.
	Summary string `json:"summary"`
	// Explanation is the human-facing prose, in Markdown.
	Explanation string `json:"explanation"`

	Concepts   []string `json:"concepts,omitempty"`   // concept IDs
	Components []string `json:"components,omitempty"` // component IDs

	CodeRefs []CodeReference `json:"code_refs,omitempty"`
	Diagrams []Diagram       `json:"diagrams,omitempty"`
	FlowID   string          `json:"flow_id,omitempty"`

	// DeepDive is optional detail that is hidden until requested. It exists so
	// the initial read stays small without discarding useful depth.
	DeepDive *DeepDive `json:"deep_dive,omitempty"`

	// Evidence lists IDs of supporting evidence entries.
	Evidence []string `json:"evidence,omitempty"`
}

// DeepDive is progressively disclosed detail attached to a step.
type DeepDive struct {
	Title       string          `json:"title,omitempty"`
	Explanation string          `json:"explanation"`
	CodeRefs    []CodeReference `json:"code_refs,omitempty"`
	Diagrams    []Diagram       `json:"diagrams,omitempty"`
}

// Architecture positions the explained work inside the wider system.
type Architecture struct {
	Overview  string              `json:"overview"`
	Groups    []ArchitectureGroup `json:"groups,omitempty"`
	DiagramID string              `json:"diagram_id,omitempty"`
}

// ArchitectureGroup is a named cluster of components, used for hierarchical
// navigation ("System > Billing > Payment Processing").
type ArchitectureGroup struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Components  []string `json:"components,omitempty"` // component IDs
	// Children allows nesting so large systems can be explored top-down.
	Children []ArchitectureGroup `json:"children,omitempty"`
}

// Flow is an ordered path of control or data through the system.
type Flow struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Trigger   string     `json:"trigger,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Steps     []FlowStep `json:"steps,omitempty"`
	Outcome   string     `json:"outcome,omitempty"`
	DiagramID string     `json:"diagram_id,omitempty"`
	// Status marks flows introduced or removed by the change.
	Status ComponentStatus `json:"status,omitempty"`
}

// FlowStep is one hop in a flow.
type FlowStep struct {
	From    string         `json:"from,omitempty"` // component ID or free text actor
	To      string         `json:"to,omitempty"`
	Action  string         `json:"action"`
	Detail  string         `json:"detail,omitempty"`
	Async   bool           `json:"async,omitempty"`
	CodeRef *CodeReference `json:"code_ref,omitempty"`
}

// BeforeAfter makes the behavioural or architectural difference explicit.
type BeforeAfter struct {
	Summary string            `json:"summary,omitempty"`
	Aspects []BeforeAfterItem `json:"aspects,omitempty"`
}

// BeforeAfterItem contrasts one aspect of the system across the change.
type BeforeAfterItem struct {
	Aspect string `json:"aspect"`
	Before string `json:"before"`
	After  string `json:"after"`
	// Significance explains why the difference matters to comprehension.
	Significance string `json:"significance,omitempty"`
}

// DiagramFormat identifies the serialisation of a diagram. Mermaid is the
// canonical format because it is text-serialisable, diffable and widely
// renderable.
type DiagramFormat string

const (
	DiagramMermaid DiagramFormat = "mermaid"
)

// Diagram is a visual aid. Diagrams exist to reduce cognitive load; they are
// not decoration and are omitted when prose is clearer.
type Diagram struct {
	ID      string        `json:"id"`
	Title   string        `json:"title,omitempty"`
	Format  DiagramFormat `json:"format"`
	Source  string        `json:"source"`
	Caption string        `json:"caption,omitempty"`
	// Purpose states what the diagram is meant to make easier to understand.
	Purpose string `json:"purpose,omitempty"`
}

// CodeReference connects an explanation to actual source. References support
// exploration; they are not a mechanism for dumping code into the walkthrough.
type CodeReference struct {
	// Path is repository-relative and always uses forward slashes.
	Path      string `json:"path"`
	Symbol    string `json:"symbol,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	// Rev is the git revision the reference resolves against. Empty means the
	// head state of the analysed scope.
	Rev string `json:"rev,omitempty"`
	// Side distinguishes references into the pre-change and post-change states.
	Side string `json:"side,omitempty"` // before | after | unchanged
	Note string `json:"note,omitempty"`
	// Snippet is an optional short excerpt. Authors are instructed to include
	// one only when the code itself is the clearest explanation.
	Snippet string `json:"snippet,omitempty"`
}

// Ignorable records parts of the change a reader can skip initially, and why.
type Ignorable struct {
	Path   string `json:"path,omitempty"`
	Area   string `json:"area,omitempty"`
	Reason string `json:"reason"`
}

// GlossaryEntry defines a term as this repository actually uses it.
type GlossaryEntry struct {
	Term       string `json:"term"`
	Definition string `json:"definition"`
	// UsedHere connects the term to concrete places in this repository, which
	// is what keeps architecture vocabulary from becoming jargon.
	UsedHere []string `json:"used_here,omitempty"`
}

// Uncertainty is an explicit statement of what could not be established.
// Recording uncertainty protects mental model efficiency: false confidence is
// worse than an acknowledged gap.
type Uncertainty struct {
	Question  string `json:"question"`
	Known     string `json:"known,omitempty"`
	Unknown   string `json:"unknown,omitempty"`
	WhereNext string `json:"where_next,omitempty"`
}

// EvidenceKind classifies where a piece of supporting evidence came from.
type EvidenceKind string

const (
	EvidenceFile    EvidenceKind = "file"
	EvidenceDiff    EvidenceKind = "diff"
	EvidenceGitLog  EvidenceKind = "git_history"
	EvidenceSearch  EvidenceKind = "search"
	EvidenceCommand EvidenceKind = "command"
	EvidenceDoc     EvidenceKind = "doc"
)

// Evidence records provenance for explanatory claims.
type Evidence struct {
	ID      string       `json:"id"`
	Kind    EvidenceKind `json:"kind"`
	Ref     string       `json:"ref"` // path, command, revision range, query
	Summary string       `json:"summary"`
	// Excerpt is a short supporting quotation from the source, when useful.
	Excerpt string `json:"excerpt,omitempty"`
}

// Meta carries generation metadata that is safe to serialise anywhere. It never
// contains credentials, prompts, or provider request payloads.
type Meta struct {
	GeneratedAt time.Time `json:"generated_at"`
	RunID       string    `json:"run_id,omitempty"`
	Generator   string    `json:"generator,omitempty"` // "codewalk/<version>"
	// Stages maps a pipeline stage name to the backend/model that produced it,
	// for example {"author": "anthropic:claude-sonnet-5"}.
	Stages map[string]string `json:"stages,omitempty"`
	// Notes records pipeline-level remarks such as skipped stages.
	Notes      []string `json:"notes,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
}
