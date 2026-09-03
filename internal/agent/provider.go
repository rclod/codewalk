package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/tools"
)

// ProviderBackend runs tasks against a model provider, supplying codewalk's own
// read-only repository tools and driving the tool-use loop.
type ProviderBackend struct {
	name   string
	client llm.Client
	// defaultMaxSteps bounds tool iterations when a task does not set its own.
	defaultMaxSteps int
	// observer receives progress events; it may be nil.
	observer Observer
}

// Observer receives pipeline progress. It exists so the CLI can show what is
// happening and the HTTP API can stream it, without either one reaching into
// the agent internals.
type Observer interface {
	OnEvent(Event)
}

// EventKind classifies a progress event.
type EventKind string

const (
	EventStageStart EventKind = "stage_start"
	EventStageEnd   EventKind = "stage_end"
	EventToolCall   EventKind = "tool_call"
	EventModelCall  EventKind = "model_call"
	EventNote       EventKind = "note"
)

// Event is a progress notification.
type Event struct {
	Kind    EventKind `json:"kind"`
	Role    string    `json:"role,omitempty"`
	Tool    string    `json:"tool,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Step    int       `json:"step,omitempty"`
	Elapsed int64     `json:"elapsed_ms,omitempty"`
}

// ObserverFunc adapts a function to the Observer interface.
type ObserverFunc func(Event)

// OnEvent implements Observer.
func (f ObserverFunc) OnEvent(e Event) { f(e) }

// NewProviderBackend creates a provider-backed agent.
func NewProviderBackend(name string, client llm.Client, maxSteps int, obs Observer) *ProviderBackend {
	if maxSteps <= 0 {
		maxSteps = 40
	}
	return &ProviderBackend{name: name, client: client, defaultMaxSteps: maxSteps, observer: obs}
}

func (p *ProviderBackend) Name() string { return p.name }
func (p *ProviderBackend) Kind() string { return "provider" }

func (p *ProviderBackend) Descriptor() string {
	return p.name + ":" + p.client.Model()
}

func (p *ProviderBackend) Capabilities() Capabilities {
	c := p.client.Capabilities()
	return Capabilities{
		Inference:        true,
		RepositoryTools:  c.Tools,
		StructuredOutput: c.JSONMode,
		Reasoning:        c.Reasoning,
		ContextCaching:   c.ContextCaching,
	}
}

// Execute runs the task, looping over tool calls until the model produces a
// final answer or the step budget is exhausted.
func (p *ProviderBackend) Execute(ctx context.Context, task Task, repo *tools.Repo) (*Result, error) {
	start := time.Now()
	maxSteps := task.MaxSteps
	if maxSteps <= 0 {
		maxSteps = p.defaultMaxSteps
	}

	prompt := task.Prompt
	if task.ExpectJSON {
		prompt += jsonInstruction(task.SchemaHint)
	}

	req := llm.Request{
		Model:           task.Model,
		System:          task.System,
		Messages:        []llm.Message{llm.UserMessage(prompt)},
		ReasoningEffort: task.ReasoningEffort,
		MaxTokens:       task.MaxTokens,
	}
	caps := p.client.Capabilities()
	if repo != nil && caps.Tools {
		req.Tools = repo.Definitions()
	}
	// JSON mode and tool use are mutually awkward across providers: a model in
	// JSON mode cannot narrate a tool call. Structured output is therefore
	// requested through the prompt whenever tools are in play, and enforced by
	// the response format only on the final, tool-free turn.
	jsonModeAvailable := task.ExpectJSON && caps.JSONMode && len(req.Tools) == 0
	req.JSONMode = jsonModeAvailable

	result := &Result{Backend: p.name, Model: p.client.Model()}
	if task.Model != "" {
		result.Model = task.Model
	}

	for step := 1; step <= maxSteps; step++ {
		p.emit(Event{Kind: EventModelCall, Role: task.Role, Step: step, Elapsed: elapsed(start)})
		resp, err := p.client.Complete(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("%s (role %s): %w", p.name, task.Role, err)
		}
		result.Usage.Add(resp.Usage)
		result.Steps = step

		if len(resp.ToolCalls) == 0 {
			result.Text = strings.TrimSpace(resp.Text)
			result.DurationMS = elapsed(start)
			if result.Text == "" {
				return nil, fmt.Errorf("%s (role %s): model returned an empty response", p.name, task.Role)
			}
			return result, nil
		}
		if repo == nil {
			return nil, fmt.Errorf("%s (role %s): model requested tools but no repository is available", p.name, task.Role)
		}

		req.Messages = append(req.Messages, llm.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			ToolCalls: resp.ToolCalls,
		})
		results := make([]llm.ToolResult, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			p.emit(Event{Kind: EventToolCall, Role: task.Role, Tool: call.Name, Detail: toolDetail(call), Step: step, Elapsed: elapsed(start)})
			results = append(results, repo.Execute(ctx, call))
			result.ToolCalls++
		}
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, ToolResults: results})
	}

	// The step budget ran out. Ask once more without tools so the work done so
	// far still produces an answer rather than an error.
	req.Tools = nil
	req.JSONMode = task.ExpectJSON && caps.JSONMode
	req.Messages = append(req.Messages, llm.UserMessage(
		"You have reached the investigation budget for this stage. Produce your final answer now using what you have already established. Where something remains unverified, say so explicitly rather than guessing."))
	resp, err := p.client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s (role %s): %w", p.name, task.Role, err)
	}
	result.Usage.Add(resp.Usage)
	result.Text = strings.TrimSpace(resp.Text)
	result.Truncated = true
	result.DurationMS = elapsed(start)
	if result.Text == "" {
		return nil, fmt.Errorf("%s (role %s): model returned an empty response after exhausting its step budget", p.name, task.Role)
	}
	return result, nil
}

func (p *ProviderBackend) emit(e Event) {
	if p.observer != nil {
		p.observer.OnEvent(e)
	}
}

func toolDetail(call llm.ToolCall) string {
	s := string(call.Input)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
