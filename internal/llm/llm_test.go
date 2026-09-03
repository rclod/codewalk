package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubDoer captures the outgoing request and returns a canned response, so the
// provider adapters can be tested without a network.
type stubDoer struct {
	requests  []map[string]any
	responses []stubResponse
	calls     int
}

type stubResponse struct {
	status int
	body   string
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	parsed["__headers"] = map[string]any{
		"authorization": req.Header.Get("authorization"),
		"x-api-key":     req.Header.Get("x-api-key"),
	}
	parsed["__url"] = req.URL.String()
	s.requests = append(s.requests, parsed)

	res := s.responses[min(s.calls, len(s.responses)-1)]
	s.calls++
	return &http.Response{
		StatusCode: res.status,
		Body:       io.NopCloser(strings.NewReader(res.body)),
		Header:     http.Header{},
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestAnthropicRequestAndToolCallParsing(t *testing.T) {
	doer := &stubDoer{responses: []stubResponse{{status: 200, body: `{
		"model": "test-model",
		"stop_reason": "tool_use",
		"content": [
			{"type": "text", "text": "Let me look."},
			{"type": "tool_use", "id": "tool-1", "name": "read_file", "input": {"path": "main.go"}}
		],
		"usage": {"input_tokens": 120, "output_tokens": 34, "cache_read_input_tokens": 100}
	}`}}}

	client := NewAnthropic(AnthropicOptions{Model: "test-model", APIKey: "test-key", MaxTokens: 1000})
	client.tr.client = doer

	res, err := client.Complete(context.Background(), Request{
		System:   "system instruction",
		Messages: []Message{UserMessage("explain this")},
		Tools:    []ToolDef{{Name: "read_file", Description: "read", Schema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.Text != "Let me look." {
		t.Errorf("text = %q", res.Text)
	}
	if res.Usage.InputTokens != 120 || res.Usage.CachedInputTokens != 100 || res.Usage.Calls != 1 {
		t.Errorf("usage = %+v", res.Usage)
	}

	sent := doer.requests[0]
	if sent["__headers"].(map[string]any)["x-api-key"] != "test-key" {
		t.Error("api key should be sent in the x-api-key header")
	}
	if _, ok := sent["system"]; !ok {
		t.Error("system prompt should be sent")
	}
}

func TestAnthropicToolResultsBecomeUserContent(t *testing.T) {
	doer := &stubDoer{responses: []stubResponse{{status: 200, body: `{"content":[{"type":"text","text":"done"}],"usage":{}}`}}}
	client := NewAnthropic(AnthropicOptions{Model: "m", APIKey: "k"})
	client.tr.client = doer

	_, err := client.Complete(context.Background(), Request{Messages: []Message{
		UserMessage("go"),
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "read_file", Input: json.RawMessage(`{"path":"a"}`)}}},
		{Role: RoleTool, ToolResults: []ToolResult{{CallID: "t1", Content: "contents"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	messages := doer.requests[0]["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	if last["role"] != "user" {
		t.Errorf("tool results should be delivered in a user turn, got role %v", last["role"])
	}
	block := last["content"].([]any)[0].(map[string]any)
	if block["type"] != "tool_result" || block["tool_use_id"] != "t1" {
		t.Errorf("tool result block = %v", block)
	}
}

func TestOpenAIRequestShapeAndToolCalls(t *testing.T) {
	doer := &stubDoer{responses: []stubResponse{{status: 200, body: `{
		"model": "gpt-test",
		"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant", "content": "",
			"tool_calls": [{"id": "call-1", "type": "function", "function": {"name": "search", "arguments": "{\"pattern\":\"x\"}"}}]}}],
		"usage": {"prompt_tokens": 50, "completion_tokens": 8, "prompt_tokens_details": {"cached_tokens": 20}}
	}`}}}
	client := NewOpenAI(OpenAIOptions{Model: "gpt-test", APIKey: "secret-key", ReasoningEffort: "high"})
	client.tr.client = doer

	res, err := client.Complete(context.Background(), Request{
		System:   "system",
		Messages: []Message{UserMessage("go")},
		Tools:    []ToolDef{{Name: "search", Schema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "search" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	sent := doer.requests[0]
	if sent["reasoning_effort"] != "high" {
		t.Error("reasoning effort should be forwarded when supported")
	}
	if _, ok := sent["temperature"]; ok {
		t.Error("temperature must be omitted when reasoning effort is set")
	}
	if sent["__headers"].(map[string]any)["authorization"] != "Bearer secret-key" {
		t.Error("api key should be sent as a bearer token")
	}
}

func TestOpenAICompatibleOmitsProviderSpecificParameters(t *testing.T) {
	doer := &stubDoer{responses: []stubResponse{{status: 200, body: `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`}}}
	client := NewOpenAI(OpenAIOptions{
		Model: "grok-test", APIKey: "k", BaseURL: "https://api.example.com/v1",
		ReasoningEffort: "high", Compatible: true,
	})
	client.tr.client = doer
	if _, err := client.Complete(context.Background(), Request{Messages: []Message{UserMessage("go")}}); err != nil {
		t.Fatal(err)
	}
	sent := doer.requests[0]
	if _, ok := sent["reasoning_effort"]; ok {
		t.Error("compatible endpoints should not receive provider-specific parameters")
	}
	if _, ok := sent["max_tokens"]; !ok {
		t.Error("compatible endpoints expect max_tokens")
	}
	if url, _ := sent["__url"].(string); url != "https://api.example.com/v1/chat/completions" {
		t.Errorf("base url not honoured: %v", url)
	}
}

func TestErrorsAreClassifiedAndCredentialsRedacted(t *testing.T) {
	cases := []struct {
		status int
		kind   ErrorKind
		retry  bool
	}{
		{401, ErrAuth, false},
		{400, ErrBadInput, false},
		{429, ErrRateLimit, true},
		{503, ErrOverload, true},
	}
	for _, tc := range cases {
		doer := &stubDoer{responses: []stubResponse{{
			status: tc.status,
			body:   `{"error": {"message": "request with key sk-secret-value-1234 failed"}}`,
		}}}
		client := NewAnthropic(AnthropicOptions{Model: "m", APIKey: "sk-secret-value-1234"})
		client.tr.client = doer
		client.tr.maxRetries = 1
		client.tr.sleep = func(time.Duration) {}

		_, err := client.Complete(context.Background(), Request{Messages: []Message{UserMessage("go")}})
		if err == nil {
			t.Fatalf("status %d should produce an error", tc.status)
		}
		providerErr, ok := err.(*Error)
		if !ok {
			t.Fatalf("error type = %T", err)
		}
		if providerErr.Kind != tc.kind {
			t.Errorf("status %d classified as %s, want %s", tc.status, providerErr.Kind, tc.kind)
		}
		if providerErr.Retryable() != tc.retry {
			t.Errorf("status %d retryable = %v, want %v", tc.status, providerErr.Retryable(), tc.retry)
		}
		if strings.Contains(err.Error(), "sk-secret-value-1234") {
			t.Fatalf("credential leaked into an error message: %s", err)
		}
	}
}

func TestTransientFailuresAreRetried(t *testing.T) {
	doer := &stubDoer{responses: []stubResponse{
		{status: 429, body: `{"error":{"message":"slow down"}}`},
		{status: 200, body: `{"content":[{"type":"text","text":"recovered"}],"usage":{}}`},
	}}
	client := NewAnthropic(AnthropicOptions{Model: "m", APIKey: "k"})
	client.tr.client = doer
	client.tr.sleep = func(time.Duration) {}

	res, err := client.Complete(context.Background(), Request{Messages: []Message{UserMessage("go")}})
	if err != nil {
		t.Fatalf("retry did not recover: %v", err)
	}
	if res.Text != "recovered" {
		t.Errorf("text = %q", res.Text)
	}
	if doer.calls != 2 {
		t.Errorf("calls = %d, want one retry", doer.calls)
	}
}

func TestRedactLeavesShortStringsAlone(t *testing.T) {
	// Redacting a very short "secret" would mangle unrelated text.
	if got := Redact("the value abc appears", "abc"); got != "the value abc appears" {
		t.Errorf("got %q", got)
	}
	if got := Redact("key sk-abcdefgh here", "sk-abcdefgh"); !strings.Contains(got, "[redacted]") {
		t.Errorf("got %q", got)
	}
}
