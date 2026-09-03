// Package run persists walkthrough generation runs.
//
// A run records everything needed to explain, reproduce and evaluate a
// walkthrough: what was analysed, which stages executed on which backends with
// which prompt versions, what each stage produced, and what it cost. Runs are
// the unit the evaluation system operates on.
//
// Runs never contain credentials. They do contain the local repository path,
// because a local tool needs it to re-open the repository; Sanitized strips it
// for anything that leaves the machine.
package run

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/pipeline"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// Run is a persisted walkthrough generation.
type Run struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Version   string    `json:"codewalk_version"`

	Kind       walkthrough.Kind       `json:"kind"`
	Repository Repository             `json:"repository"`
	Scope      walkthrough.Scope      `json:"scope"`
	Complexity walkthrough.Complexity `json:"complexity"`

	Pipeline Pipeline `json:"pipeline"`
	Metrics  Metrics  `json:"metrics"`

	// Status is running, complete or failed.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`

	// Conversation holds follow-up questions and answers about this run.
	Conversation []Turn `json:"conversation,omitempty"`
}

// Repository identifies the analysed repository.
type Repository struct {
	Name string `json:"name"`
	// Path is local machine state and is removed by Sanitized.
	Path       string `json:"path,omitempty"`
	RemoteHost string `json:"remote_host,omitempty"`
	HeadCommit string `json:"head_commit,omitempty"`
}

// Pipeline records the configuration that produced the walkthrough. It stores
// prompt versions and backend descriptors rather than prompt text or
// credentials, which is enough to reproduce a run and safe to share.
type Pipeline struct {
	Depth  string                 `json:"depth,omitempty"`
	Focus  string                 `json:"focus,omitempty"`
	Stages []pipeline.StageRecord `json:"stages"`
	// Backends maps role to backend descriptor, for example
	// {"author": "anthropic:claude-sonnet-5"}.
	Backends map[string]string `json:"backends,omitempty"`
	// ConfigDigest is a stable hash of the effective analysis settings, so two
	// runs can be compared for configuration equality without storing the
	// configuration itself.
	ConfigDigest string `json:"config_digest,omitempty"`
}

// Metrics are the operational costs of a run. Cost and latency are evaluation
// dimensions in their own right.
//
// Tool activity is counted in two places because it happens in two places: a
// provider backend uses codewalk's own read-only tools, while a harness backend
// uses its own. Reporting only the former makes a harness run look like it read
// nothing at all.
type Metrics struct {
	DurationMS int64     `json:"duration_ms"`
	Usage      llm.Usage `json:"usage"`
	// ToolCalls is every tool call made on the run's behalf, whether through
	// codewalk's tools or reported by a harness.
	ToolCalls int `json:"tool_calls"`
	// CodewalkToolCalls counts only calls through codewalk's own tool surface.
	CodewalkToolCalls int            `json:"codewalk_tool_calls"`
	ToolBreakdown     map[string]int `json:"tool_breakdown,omitempty"`
	// FilesInspected counts distinct files read through codewalk's tools. It is
	// zero for harness backends, which read files themselves.
	FilesInspected int `json:"files_inspected"`
	ModelCalls     int `json:"model_calls"`
}

// Turn is one message in a follow-up conversation.
type Turn struct {
	Role      string    `json:"role"` // user | assistant
	Content   string    `json:"content"`
	At        time.Time `json:"at"`
	Backend   string    `json:"backend,omitempty"`
	Usage     llm.Usage `json:"usage,omitempty"`
	ToolCalls int       `json:"tool_calls,omitempty"`
}

// Summary is the listing view of a run.
type Summary struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	Kind       string    `json:"kind"`
	Repository string    `json:"repository"`
	Title      string    `json:"title"`
	Scope      string    `json:"scope"`
	Status     string    `json:"status"`
	DurationMS int64     `json:"duration_ms"`
}

// NewID returns a time-sortable, human-readable run identifier.
func NewID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// Sanitized returns a copy safe to share outside the machine that produced it:
// the local repository path is removed.
func (r *Run) Sanitized() *Run {
	c := *r
	c.Repository.Path = ""
	c.Scope.RepositoryPath = ""
	return &c
}
