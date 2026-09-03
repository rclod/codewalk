package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/testutil"
	"github.com/rclod/codewalk/internal/tools"
)

// scriptedClient returns queued responses, so the tool-use loop can be tested
// deterministically.
type scriptedClient struct {
	responses []llm.Response
	requests  []llm.Request
	caps      llm.Capabilities
}

func (c *scriptedClient) Name() string  { return "scripted" }
func (c *scriptedClient) Model() string { return "scripted-model" }
func (c *scriptedClient) Capabilities() llm.Capabilities {
	return c.caps
}

func (c *scriptedClient) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	c.requests = append(c.requests, req)
	if len(c.responses) == 0 {
		return nil, errors.New("no scripted response left")
	}
	res := c.responses[0]
	if len(c.responses) > 1 {
		c.responses = c.responses[1:]
	}
	return &res, nil
}

func repoTools(t *testing.T) *tools.Repo {
	t.Helper()
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return tools.NewRepo(repo, nil, nil, tools.Options{})
}

func TestProviderBackendRunsToolLoopUntilAnswer(t *testing.T) {
	client := &scriptedClient{
		caps: llm.Capabilities{Tools: true},
		responses: []llm.Response{
			{
				ToolCalls: []llm.ToolCall{{ID: "t1", Name: "read_file", Input: json.RawMessage(`{"path":"internal/orders/service.go"}`)}},
				Usage:     llm.Usage{InputTokens: 10, OutputTokens: 2, Calls: 1},
			},
			{Text: "The service creates orders.", Usage: llm.Usage{InputTokens: 30, OutputTokens: 8, Calls: 1}},
		},
	}
	backend := agent.NewProviderBackend("scripted", client, 10, nil)

	res, err := backend.Execute(context.Background(), agent.Task{Role: "investigator", Prompt: "investigate"}, repoTools(t))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Text != "The service creates orders." {
		t.Errorf("text = %q", res.Text)
	}
	if res.ToolCalls != 1 || res.Steps != 2 {
		t.Errorf("tool calls = %d, steps = %d", res.ToolCalls, res.Steps)
	}
	if res.Usage.InputTokens != 40 || res.Usage.Calls != 2 {
		t.Errorf("usage should accumulate across the loop: %+v", res.Usage)
	}

	// The second request must carry the assistant turn and the tool result.
	second := client.requests[1]
	if len(second.Messages) != 3 {
		t.Fatalf("second request messages = %d, want prompt, assistant, tool result", len(second.Messages))
	}
	if second.Messages[2].Role != llm.RoleTool || len(second.Messages[2].ToolResults) != 1 {
		t.Errorf("tool results not fed back: %+v", second.Messages[2])
	}
}

// loopingClient always asks for another tool call while tools are offered, and
// answers once they are withdrawn. It models a model that would otherwise
// investigate forever.
type loopingClient struct {
	requests []llm.Request
}

func (c *loopingClient) Name() string                   { return "looping" }
func (c *loopingClient) Model() string                  { return "looping-model" }
func (c *loopingClient) Capabilities() llm.Capabilities { return llm.Capabilities{Tools: true} }

func (c *loopingClient) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	c.requests = append(c.requests, req)
	if len(req.Tools) > 0 {
		return &llm.Response{
			ToolCalls: []llm.ToolCall{{ID: "t", Name: "search", Input: json.RawMessage(`{"pattern":"Service"}`)}},
			Usage:     llm.Usage{Calls: 1},
		}, nil
	}
	return &llm.Response{Text: "Partial answer.", Usage: llm.Usage{Calls: 1}}, nil
}

func TestProviderBackendStopsAtStepBudgetAndStillAnswers(t *testing.T) {
	// A model that keeps calling tools forever must still produce something
	// useful rather than failing the run.
	client := &loopingClient{}
	backend := agent.NewProviderBackend("looping", client, 3, nil)

	res, err := backend.Execute(context.Background(), agent.Task{Role: "investigator", Prompt: "go", MaxSteps: 2}, repoTools(t))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.Truncated {
		t.Error("hitting the step budget should be recorded as truncation")
	}
	if res.Text != "Partial answer." {
		t.Errorf("text = %q, want the model's concluding answer", res.Text)
	}
	last := client.requests[len(client.requests)-1]
	if len(last.Tools) != 0 {
		t.Error("the final request should withdraw tools so the model must answer")
	}
	if !strings.Contains(last.Messages[len(last.Messages)-1].Text, "budget") {
		t.Error("the model should be told why it is being asked to conclude")
	}
}

func TestJSONModeIsNotCombinedWithTools(t *testing.T) {
	// Providers that support JSON mode cannot narrate a tool call while
	// constrained to a JSON object, so structured output is requested through
	// the prompt whenever tools are available.
	client := &scriptedClient{
		caps:      llm.Capabilities{Tools: true, JSONMode: true},
		responses: []llm.Response{{Text: `{"ok":true}`}},
	}
	backend := agent.NewProviderBackend("scripted", client, 5, nil)
	if _, err := backend.Execute(context.Background(), agent.Task{
		Role: "author", Prompt: "write", ExpectJSON: true, SchemaHint: `{"ok": true}`,
	}, repoTools(t)); err != nil {
		t.Fatal(err)
	}
	req := client.requests[0]
	if req.JSONMode {
		t.Error("JSON mode should not be set while tools are offered")
	}
	if !strings.Contains(req.Messages[0].Text, "single JSON object") {
		t.Error("the prompt should still require a JSON object")
	}
}

func TestEmptyResponseIsAnError(t *testing.T) {
	client := &scriptedClient{caps: llm.Capabilities{Tools: true}, responses: []llm.Response{{Text: "   "}}}
	backend := agent.NewProviderBackend("scripted", client, 5, nil)
	if _, err := backend.Execute(context.Background(), agent.Task{Role: "author", Prompt: "write"}, nil); err == nil {
		t.Error("an empty model response should surface as an error, not an empty walkthrough")
	}
}

func TestObserverSeesProgress(t *testing.T) {
	client := &scriptedClient{
		caps: llm.Capabilities{Tools: true},
		responses: []llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "t", Name: "repo_map", Input: json.RawMessage(`{}`)}}},
			{Text: "done"},
		},
	}
	var events []agent.Event
	backend := agent.NewProviderBackend("scripted", client, 5, agent.ObserverFunc(func(e agent.Event) {
		events = append(events, e)
	}))
	if _, err := backend.Execute(context.Background(), agent.Task{Role: "investigator", Prompt: "go"}, repoTools(t)); err != nil {
		t.Fatal(err)
	}
	var sawTool bool
	for _, e := range events {
		if e.Kind == agent.EventToolCall && e.Tool == "repo_map" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Errorf("observer should see tool calls: %+v", events)
	}
}

func TestRegistryBindsRolesAndFallsBack(t *testing.T) {
	reg := agent.NewRegistry()
	primary := agent.NewProviderBackend("primary", &scriptedClient{}, 5, nil)
	secondary := agent.NewProviderBackend("secondary", &scriptedClient{}, 5, nil)
	reg.Register("primary", primary)
	reg.Register("secondary", secondary)

	if err := reg.SetDefault("primary"); err != nil {
		t.Fatal(err)
	}
	if err := reg.BindRole("author", "secondary"); err != nil {
		t.Fatal(err)
	}
	if err := reg.BindRole("author", "missing"); err == nil {
		t.Error("binding to an unknown backend should fail")
	}

	author, err := reg.For("author")
	if err != nil || author.Name() != "secondary" {
		t.Errorf("author backend = %v, %v", author, err)
	}
	investigator, err := reg.For("investigator")
	if err != nil || investigator.Name() != "primary" {
		t.Errorf("unbound roles should fall back to the default: %v, %v", investigator, err)
	}
	if d := reg.Descriptors([]string{"author"}); d["author"] != "secondary:scripted-model" {
		t.Errorf("descriptors = %v", d)
	}
}
