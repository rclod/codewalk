// Package llm defines a provider-neutral interface for language-model
// inference.
//
// The interface is expressed in capabilities rather than vendor concepts:
// callers ask whether a client supports tool use, JSON output or reasoning
// effort, and never branch on the provider's name. Provider-specific wire
// formats stay inside this package.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Role identifies the author of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool carries tool results back to the model. Providers that model
	// this differently (Anthropic puts results in a user turn) adapt internally.
	RoleTool Role = "tool"
)

// Message is one turn of a conversation.
type Message struct {
	Role Role
	Text string
	// ToolCalls are emitted by the assistant.
	ToolCalls []ToolCall
	// ToolResults answer previous tool calls.
	ToolResults []ToolResult
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult is the outcome of running a tool.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// ToolDef describes a tool the model may call. Schema is a JSON Schema object.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Request is a single completion request.
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolDef

	MaxTokens   int
	Temperature *float64
	// ReasoningEffort is passed through where supported and ignored elsewhere.
	ReasoningEffort string
	// JSONMode asks the provider to constrain output to a JSON object where it
	// supports doing so. Callers must still tolerate non-JSON responses.
	JSONMode bool
}

// Usage reports token accounting when the provider supplies it.
type Usage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	Calls             int `json:"calls"`
}

// Add accumulates usage across calls.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CachedInputTokens += o.CachedInputTokens
	u.Calls += o.Calls
}

// Response is a single completion result.
type Response struct {
	Text       string
	ToolCalls  []ToolCall
	Usage      Usage
	StopReason string
	Model      string
}

// Capabilities describes what a client can do, so the pipeline can pick the
// best available approach without knowing the vendor.
type Capabilities struct {
	Tools          bool
	JSONMode       bool
	Reasoning      bool
	Streaming      bool
	ContextCaching bool
}

// Client is a language-model backend.
type Client interface {
	// Name identifies the configured backend (for example "anthropic").
	Name() string
	// Model returns the default model for this client.
	Model() string
	Capabilities() Capabilities
	Complete(ctx context.Context, req Request) (*Response, error)
}

// UserMessage is a convenience constructor.
func UserMessage(text string) Message { return Message{Role: RoleUser, Text: text} }

// ErrorKind classifies provider failures so callers can decide whether to
// retry, fall back, or surface a configuration problem to the user.
type ErrorKind string

const (
	ErrAuth      ErrorKind = "auth"
	ErrRateLimit ErrorKind = "rate_limit"
	ErrOverload  ErrorKind = "overloaded"
	ErrBadInput  ErrorKind = "bad_input"
	ErrTransport ErrorKind = "transport"
	ErrOther     ErrorKind = "other"
)

// Error is a provider error with its sensitive details already removed.
type Error struct {
	Kind     ErrorKind
	Provider string
	Status   int
	Message  string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: %s (HTTP %d): %s", e.Provider, e.Kind, e.Status, e.Message)
	}
	return fmt.Sprintf("%s: %s: %s", e.Provider, e.Kind, e.Message)
}

// Retryable reports whether retrying the same request could succeed.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case ErrRateLimit, ErrOverload, ErrTransport:
		return true
	default:
		return false
	}
}

// Redact removes anything that looks like a credential from text destined for
// logs, errors or persisted run metadata.
func Redact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) >= 8 {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
