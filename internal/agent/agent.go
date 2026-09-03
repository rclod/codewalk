// Package agent provides the backend abstraction that pipeline stages run on.
//
// A backend is either a direct model provider (codewalk supplies the repository
// tools and drives the tool-use loop) or a local agent harness such as Claude
// Code, Codex or OpenCode (the harness already has filesystem, shell, git and
// search access, so codewalk hands it a task and reads the answer).
//
// The pipeline is written against capabilities, not vendors: a stage asks
// whether structured output or repository tools are available and adapts, so
// the same stage runs unchanged on an HTTP provider or on a local harness.
package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/tools"
)

// Capabilities describes what a backend can do.
type Capabilities struct {
	// Inference is always true; it is listed so capability checks read uniformly.
	Inference bool
	// RepositoryTools means codewalk supplies read-only repository tools.
	RepositoryTools bool
	// OwnTools means the backend brings its own filesystem, search and git
	// access, so codewalk should not duplicate them.
	OwnTools bool
	// StructuredOutput means the backend can be constrained to emit JSON.
	StructuredOutput bool
	// Reasoning means a reasoning-effort setting is honoured.
	Reasoning bool
	// Subagents means the backend can parallelise work internally.
	Subagents bool
	// ContextCaching means repeated prefixes are cached by the provider.
	ContextCaching bool
}

// Task is one unit of work handed to a backend.
type Task struct {
	// Role is the pipeline stage requesting the work, used for logging and
	// per-role configuration.
	Role string
	// System is the role instruction.
	System string
	// Prompt is the concrete request, including any pre-assembled context.
	Prompt string
	// ExpectJSON asks for a single JSON object as the entire response.
	ExpectJSON bool
	// SchemaHint describes the expected JSON shape in prose. It is included in
	// the prompt for every backend, because harnesses cannot be constrained by
	// a response format.
	SchemaHint string
	// MaxSteps bounds tool-use iterations.
	MaxSteps int
	// Model and ReasoningEffort override backend defaults for this task.
	Model           string
	ReasoningEffort string
	// MaxTokens overrides the backend's per-response cap.
	MaxTokens int
}

// Result is the outcome of a task.
type Result struct {
	Text       string    `json:"-"`
	Backend    string    `json:"backend"`
	Model      string    `json:"model,omitempty"`
	Usage      llm.Usage `json:"usage"`
	Steps      int       `json:"steps"`
	ToolCalls  int       `json:"tool_calls"`
	DurationMS int64     `json:"duration_ms"`
	// Truncated marks a task that hit its step budget before the backend
	// finished, which downstream stages should treat as partial evidence.
	Truncated bool `json:"truncated,omitempty"`
}

// Backend executes tasks.
type Backend interface {
	// Name is the configured backend name, for example "anthropic".
	Name() string
	// Kind is "provider" or "harness".
	Kind() string
	// Descriptor identifies the backend and model for run provenance, for
	// example "anthropic:claude-sonnet-5". It never contains credentials.
	Descriptor() string
	Capabilities() Capabilities
	// Execute runs a task. repo may be nil when the task needs no repository
	// access; backends with OwnTools use it only to locate the working
	// directory.
	Execute(ctx context.Context, task Task, repo *tools.Repo) (*Result, error)
}

// Registry resolves pipeline roles to backends.
type Registry struct {
	backends map[string]Backend
	roles    map[string]string
	fallback string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{backends: map[string]Backend{}, roles: map[string]string{}}
}

// Register adds a backend under a name.
func (r *Registry) Register(name string, b Backend) {
	r.backends[name] = b
	if r.fallback == "" {
		r.fallback = name
	}
}

// SetDefault sets the backend used by roles with no explicit binding.
func (r *Registry) SetDefault(name string) error {
	if _, ok := r.backends[name]; !ok {
		return fmt.Errorf("backend %q is not configured", name)
	}
	r.fallback = name
	return nil
}

// BindRole binds a pipeline role to a backend name.
func (r *Registry) BindRole(role, backend string) error {
	if _, ok := r.backends[backend]; !ok {
		return fmt.Errorf("backend %q is not configured", backend)
	}
	r.roles[role] = backend
	return nil
}

// For returns the backend bound to a role.
func (r *Registry) For(role string) (Backend, error) {
	name := r.roles[role]
	if name == "" {
		name = r.fallback
	}
	b, ok := r.backends[name]
	if !ok {
		return nil, fmt.Errorf("no backend configured for role %q", role)
	}
	return b, nil
}

// Get returns a backend by name.
func (r *Registry) Get(name string) (Backend, bool) {
	b, ok := r.backends[name]
	return b, ok
}

// Names lists configured backend names.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.backends))
	for n := range r.backends {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Descriptors maps each bound role to its backend descriptor, for run metadata.
func (r *Registry) Descriptors(roles []string) map[string]string {
	out := map[string]string{}
	for _, role := range roles {
		if b, err := r.For(role); err == nil {
			out[role] = b.Descriptor()
		}
	}
	return out
}

// jsonInstruction is appended to prompts that require structured output. It is
// used for every backend, because a harness cannot be constrained by a
// provider-side response format.
func jsonInstruction(schemaHint string) string {
	var b strings.Builder
	b.WriteString("\n\nRespond with a single JSON object and nothing else. ")
	b.WriteString("Do not wrap it in Markdown fences, and do not add commentary before or after it.")
	if schemaHint != "" {
		b.WriteString("\n\nExpected shape:\n")
		b.WriteString(schemaHint)
	}
	return b.String()
}

func elapsed(start time.Time) int64 { return time.Since(start).Milliseconds() }

// LLMUsage is an alias for llm.Usage, used by backends that report token
// accounting from a harness envelope rather than from a provider response.
type LLMUsage = llm.Usage
