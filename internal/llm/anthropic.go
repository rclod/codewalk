package llm

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// AnthropicClient talks to the Anthropic Messages API.
type AnthropicClient struct {
	name    string
	model   string
	baseURL string
	version string
	maxTok  int
	effort  string
	tr      *transport
}

// AnthropicOptions configures an Anthropic client.
type AnthropicOptions struct {
	Name            string
	Model           string
	APIKey          string
	BaseURL         string
	MaxTokens       int
	ReasoningEffort string
	Timeout         time.Duration
}

// NewAnthropic constructs an Anthropic client.
func NewAnthropic(o AnthropicOptions) *AnthropicClient {
	base := o.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	maxTok := o.MaxTokens
	if maxTok <= 0 {
		maxTok = 16000
	}
	name := o.Name
	if name == "" {
		name = "anthropic"
	}
	return &AnthropicClient{
		name:    name,
		model:   o.Model,
		baseURL: strings.TrimSuffix(base, "/"),
		version: "2023-06-01",
		maxTok:  maxTok,
		effort:  o.ReasoningEffort,
		tr:      newTransport(name, o.APIKey, o.Timeout),
	}
}

func (c *AnthropicClient) Name() string  { return c.name }
func (c *AnthropicClient) Model() string { return c.model }

func (c *AnthropicClient) Capabilities() Capabilities {
	return Capabilities{Tools: true, JSONMode: false, Reasoning: true, Streaming: true, ContextCaching: true}
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      []anthropicBlock   `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// CacheControl marks a prefix as cacheable, which materially reduces cost
	// when several pipeline stages share the same repository context.
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResponse struct {
	Model      string           `json:"model"`
	StopReason string           `json:"stop_reason"`
	Content    []anthropicBlock `json:"content"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Complete implements Client.
func (c *AnthropicClient) Complete(ctx context.Context, req Request) (*Response, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = c.maxTok
	}

	body := anthropicRequest{Model: model, MaxTokens: maxTok, Temperature: req.Temperature}
	if req.System != "" {
		body.System = []anthropicBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: &anthropicCacheControl{Type: "ephemeral"},
		}}
	}
	effort := req.ReasoningEffort
	if effort == "" {
		effort = c.effort
	}
	if budget := thinkingBudget(effort, maxTok); budget > 0 {
		body.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
		// Extended thinking requires headroom for the thinking tokens.
		body.MaxTokens = maxTok + budget
		body.Temperature = nil
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toAnthropicMessage(m))
	}

	var out anthropicResponse
	headers := map[string]string{
		"x-api-key":         c.tr.apiKey,
		"anthropic-version": c.version,
	}
	if err := c.tr.postJSON(ctx, c.baseURL+"/v1/messages", headers, body, &out); err != nil {
		return nil, err
	}

	res := &Response{
		Model:      out.Model,
		StopReason: out.StopReason,
		Usage: Usage{
			InputTokens:       out.Usage.InputTokens,
			OutputTokens:      out.Usage.OutputTokens,
			CachedInputTokens: out.Usage.CacheReadInputTokens,
			Calls:             1,
		},
	}
	var text strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			res.ToolCalls = append(res.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	res.Text = text.String()
	return res, nil
}

func toAnthropicMessage(m Message) anthropicMessage {
	switch m.Role {
	case RoleAssistant:
		msg := anthropicMessage{Role: "assistant"}
		if m.Text != "" {
			msg.Content = append(msg.Content, anthropicBlock{Type: "text", Text: m.Text})
		}
		for _, tc := range m.ToolCalls {
			input := tc.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			msg.Content = append(msg.Content, anthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
		}
		return msg
	case RoleTool:
		// Anthropic delivers tool results inside a user turn.
		msg := anthropicMessage{Role: "user"}
		for _, tr := range m.ToolResults {
			msg.Content = append(msg.Content, anthropicBlock{
				Type:      "tool_result",
				ToolUseID: tr.CallID,
				Content:   tr.Content,
				IsError:   tr.IsError,
			})
		}
		if m.Text != "" {
			msg.Content = append(msg.Content, anthropicBlock{Type: "text", Text: m.Text})
		}
		return msg
	default:
		return anthropicMessage{Role: "user", Content: []anthropicBlock{{Type: "text", Text: m.Text}}}
	}
}

// thinkingBudget maps a provider-neutral reasoning effort onto an Anthropic
// thinking budget. Zero disables extended thinking.
func thinkingBudget(effort string, maxTokens int) int {
	switch strings.ToLower(effort) {
	case "low":
		return 2048
	case "medium":
		return 8192
	case "high", "max":
		return 16384
	default:
		return 0
	}
}
