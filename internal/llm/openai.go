package llm

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// OpenAIClient talks to the OpenAI Chat Completions API and to any
// OpenAI-compatible endpoint (xAI, local servers, gateways), which is why the
// base URL and the "compatible" flag are configurable.
type OpenAIClient struct {
	name       string
	model      string
	baseURL    string
	maxTok     int
	effort     string
	compatible bool
	tr         *transport
}

// OpenAIOptions configures an OpenAI-style client.
type OpenAIOptions struct {
	Name            string
	Model           string
	APIKey          string
	BaseURL         string
	MaxTokens       int
	ReasoningEffort string
	Timeout         time.Duration
	// Compatible marks a third-party OpenAI-compatible endpoint. Parameters
	// that such services commonly reject are omitted.
	Compatible bool
}

// NewOpenAI constructs an OpenAI-style client.
func NewOpenAI(o OpenAIOptions) *OpenAIClient {
	base := o.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	maxTok := o.MaxTokens
	if maxTok <= 0 {
		maxTok = 16000
	}
	name := o.Name
	if name == "" {
		name = "openai"
	}
	return &OpenAIClient{
		name:       name,
		model:      o.Model,
		baseURL:    strings.TrimSuffix(base, "/"),
		maxTok:     maxTok,
		effort:     o.ReasoningEffort,
		compatible: o.Compatible,
		tr:         newTransport(name, o.APIKey, o.Timeout),
	}
}

func (c *OpenAIClient) Name() string  { return c.name }
func (c *OpenAIClient) Model() string { return c.model }

func (c *OpenAIClient) Capabilities() Capabilities {
	return Capabilities{Tools: true, JSONMode: true, Reasoning: !c.compatible, Streaming: true}
}

type openaiRequest struct {
	Model               string          `json:"model"`
	Messages            []openaiMessage `json:"messages"`
	Tools               []openaiTool    `json:"tools,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	ResponseFormat      *openaiFormat   `json:"response_format,omitempty"`
}

type openaiFormat struct {
	Type string `json:"type"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type openaiResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string        `json:"finish_reason"`
		Message      openaiMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// Complete implements Client.
func (c *OpenAIClient) Complete(ctx context.Context, req Request) (*Response, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = c.maxTok
	}

	body := openaiRequest{Model: model, Temperature: req.Temperature}
	if c.compatible {
		// Older-style compatible endpoints expect max_tokens.
		body.MaxTokens = maxTok
	} else {
		body.MaxCompletionTokens = maxTok
		effort := req.ReasoningEffort
		if effort == "" {
			effort = c.effort
		}
		body.ReasoningEffort = effort
		if effort != "" {
			// Reasoning models reject an explicit temperature.
			body.Temperature = nil
		}
	}
	if req.JSONMode {
		body.ResponseFormat = &openaiFormat{Type: "json_object"}
	}
	if req.System != "" {
		body.Messages = append(body.Messages, openaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toOpenAIMessages(m)...)
	}
	for _, t := range req.Tools {
		var ot openaiTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Schema
		body.Tools = append(body.Tools, ot)
	}

	var out openaiResponse
	headers := map[string]string{"authorization": "Bearer " + c.tr.apiKey}
	if err := c.tr.postJSON(ctx, c.baseURL+"/chat/completions", headers, body, &out); err != nil {
		return nil, err
	}
	res := &Response{
		Model: out.Model,
		Usage: Usage{
			InputTokens:       out.Usage.PromptTokens,
			OutputTokens:      out.Usage.CompletionTokens,
			CachedInputTokens: out.Usage.PromptTokensDetails.CachedTokens,
			Calls:             1,
		},
	}
	if len(out.Choices) == 0 {
		return res, nil
	}
	choice := out.Choices[0]
	res.StopReason = choice.FinishReason
	res.Text = choice.Message.Content
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		res.ToolCalls = append(res.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(args)})
	}
	return res, nil
}

func toOpenAIMessages(m Message) []openaiMessage {
	switch m.Role {
	case RoleAssistant:
		msg := openaiMessage{Role: "assistant", Content: m.Text}
		for _, tc := range m.ToolCalls {
			var otc openaiToolCall
			otc.ID = tc.ID
			otc.Type = "function"
			otc.Function.Name = tc.Name
			otc.Function.Arguments = string(tc.Input)
			if otc.Function.Arguments == "" {
				otc.Function.Arguments = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, otc)
		}
		return []openaiMessage{msg}
	case RoleTool:
		var msgs []openaiMessage
		for _, tr := range m.ToolResults {
			msgs = append(msgs, openaiMessage{Role: "tool", ToolCallID: tr.CallID, Content: tr.Content})
		}
		if m.Text != "" {
			msgs = append(msgs, openaiMessage{Role: "user", Content: m.Text})
		}
		return msgs
	default:
		return []openaiMessage{{Role: "user", Content: m.Text}}
	}
}
