package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rclod/codewalk/internal/tools"
)

// This file implements a minimal client for the Agent Client Protocol (ACP), a
// JSON-RPC 2.0 conversation over a subprocess's stdio. ACP is the preferred
// integration contract for harnesses that speak it, because it gives codewalk
// structured session control and an explicit permission handshake instead of
// scraping a CLI's output.
//
// The client is deliberately small: codewalk needs to start a session, send one
// prompt, answer permission requests according to its read-only policy, and
// collect the agent's message. Support is marked experimental in the docs
// because harness implementations of ACP are still converging.

const acpProtocolVersion = 1

// acpPermissionPolicy decides how to answer a permission request. codewalk is
// observational, so tool calls that would modify the repository are refused
// even when the harness would allow them.
var acpMutatingKinds = map[string]bool{
	"edit": true, "delete": true, "move": true,
}

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// acpConn is a JSON-RPC connection to an ACP agent subprocess.
type acpConn struct {
	stdin  io.WriteCloser
	reader *bufio.Reader

	mu      sync.Mutex
	nextID  int
	pending map[string]chan acpMessage

	// text accumulates streamed assistant message chunks.
	textMu sync.Mutex
	text   strings.Builder

	// allowExecute controls whether shell execution requests are permitted.
	allowExecute bool
	observer     Observer
	role         string

	tools int

	closed chan struct{}
}

func (h *HarnessBackend) executeACP(ctx context.Context, task Task, repo *tools.Repo) (*Result, error) {
	start := time.Now()
	workdir := "."
	if repo != nil {
		workdir = repo.Root()
	}
	ctx, cancel := context.WithTimeout(ctx, h.spec.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.spec.Command, h.spec.Args...)
	cmd.Dir = workdir
	cmd.Env = h.environment()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("harness %q: could not start ACP agent: %w", h.name, err)
	}
	defer func() {
		stdin.Close()
		_ = cmd.Wait()
	}()

	conn := &acpConn{
		stdin:    stdin,
		reader:   bufio.NewReaderSize(stdout, 1<<20),
		pending:  map[string]chan acpMessage{},
		observer: h.observer,
		role:     task.Role,
		closed:   make(chan struct{}),
	}
	go conn.readLoop()

	if _, err := conn.call(ctx, "initialize", map[string]any{
		"protocolVersion": acpProtocolVersion,
		"clientCapabilities": map[string]any{
			// codewalk does not offer filesystem services to the agent: the
			// harness reads the repository with its own tools.
			"fs": map[string]any{"readTextFile": false, "writeTextFile": false},
		},
	}); err != nil {
		return nil, fmt.Errorf("harness %q: ACP initialize failed: %w", h.name, err)
	}

	sessionRaw, err := conn.call(ctx, "session/new", map[string]any{
		"cwd":        workdir,
		"mcpServers": []any{},
	})
	if err != nil {
		return nil, fmt.Errorf("harness %q: ACP session/new failed: %w", h.name, err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
		Modes     struct {
			AvailableModes []struct {
				ID   string `json:"id"`
				Meta struct {
					Kind string `json:"kind"`
				} `json:"_meta"`
			} `json:"availableModes"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(sessionRaw, &session); err != nil || session.SessionID == "" {
		return nil, fmt.Errorf("harness %q: ACP agent did not return a session id", h.name)
	}

	// Prefer a plan-style mode where the agent offers one. Having the agent
	// refuse to modify anything is a stronger guarantee than codewalk declining
	// each permission request as it arrives, and it is what the CLI adapters
	// already do with --permission-mode plan.
	if mode := planMode(session.Modes.AvailableModes); mode != "" {
		if _, err := conn.call(ctx, "session/set_mode", map[string]any{
			"sessionId": session.SessionID,
			"modeId":    mode,
		}); err != nil {
			// Not fatal: the permission policy below still refuses mutations.
			conn.note("could not select read-only mode %q: %v", mode, err)
		}
	}

	prompt := combineSystemPrompt(task.System, task.Prompt)
	if task.ExpectJSON {
		prompt += jsonInstruction(task.SchemaHint)
	}
	promptResult, err := conn.call(ctx, "session/prompt", map[string]any{
		"sessionId": session.SessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": prompt}},
	})
	if err != nil {
		return nil, fmt.Errorf("harness %q: ACP session/prompt failed: %w", h.name, err)
	}

	text := strings.TrimSpace(conn.collected())
	if text == "" {
		return nil, fmt.Errorf("harness %q: ACP agent returned no message (role %s)", h.name, task.Role)
	}
	return &Result{
		Text:       text,
		Backend:    h.name,
		Model:      h.spec.Model,
		Steps:      1,
		ToolCalls:  conn.toolCalls(),
		DurationMS: elapsed(start),
		Usage:      acpUsage(promptResult),
	}, nil
}

// planMode picks a mode that prevents modification, preferring the agent's own
// declared "plan" kind over a mode that merely happens to be named that way.
func planMode(modes []struct {
	ID   string `json:"id"`
	Meta struct {
		Kind string `json:"kind"`
	} `json:"_meta"`
}) string {
	for _, m := range modes {
		if m.Meta.Kind == "plan" {
			return m.ID
		}
	}
	for _, m := range modes {
		if m.ID == "plan" {
			return m.ID
		}
	}
	return ""
}

// acpUsage reads the token accounting an agent reports with its prompt result.
func acpUsage(result json.RawMessage) LLMUsage {
	usage := LLMUsage{Calls: 1}
	var payload struct {
		Usage struct {
			InputTokens      int `json:"inputTokens"`
			OutputTokens     int `json:"outputTokens"`
			CachedReadTokens int `json:"cachedReadTokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(result, &payload) == nil {
		usage.InputTokens = payload.Usage.InputTokens
		usage.OutputTokens = payload.Usage.OutputTokens
		usage.CachedInputTokens = payload.Usage.CachedReadTokens
	}
	return usage
}

func (c *acpConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprintf("%d", c.nextID)
	ch := make(chan acpMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("ACP agent closed the connection during %q", method)
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (c *acpConn) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *acpConn) readLoop() {
	defer close(c.closed)
	for {
		line, err := c.reader.ReadBytes('\n')
		if len(line) > 0 {
			var msg acpMessage
			if json.Unmarshal(line, &msg) == nil {
				c.handle(msg)
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *acpConn) handle(msg acpMessage) {
	// A response to one of our calls.
	if msg.Method == "" && len(msg.ID) > 0 {
		id := strings.Trim(string(msg.ID), `"`)
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
		return
	}

	switch msg.Method {
	case "session/update":
		c.handleUpdate(msg.Params)
	case "session/request_permission":
		c.handlePermission(msg)
	default:
		// Requests codewalk does not implement are refused explicitly rather
		// than ignored, so the agent does not stall waiting for a reply.
		if len(msg.ID) > 0 {
			_ = c.write(map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(msg.ID),
				"error":   map[string]any{"code": -32601, "message": "method not supported by codewalk"},
			})
		}
	}
}

func (c *acpConn) handleUpdate(params json.RawMessage) {
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	switch p.Update.SessionUpdate {
	case "agent_message_chunk":
		if p.Update.Content.Text != "" {
			c.textMu.Lock()
			c.text.WriteString(p.Update.Content.Text)
			c.textMu.Unlock()
		}
	case "tool_call":
		c.textMu.Lock()
		c.tools++
		c.textMu.Unlock()
		if c.observer != nil {
			c.observer.OnEvent(Event{Kind: EventToolCall, Role: c.role, Tool: p.Update.Kind, Detail: p.Update.Title})
		}
	}
}

// handlePermission answers a permission request using codewalk's read-only
// policy: anything that would modify the repository is refused.
func (c *acpConn) handlePermission(msg acpMessage) {
	var p struct {
		ToolCall struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(msg.Params, &p)

	allowed := !acpMutatingKinds[p.ToolCall.Kind]
	if p.ToolCall.Kind == "execute" && !c.allowExecute {
		allowed = false
	}

	reply := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID)}
	if allowed {
		for _, o := range p.Options {
			if o.Kind == "allow_once" || o.Kind == "allow_always" {
				reply["result"] = map[string]any{
					"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID},
				}
				_ = c.write(reply)
				return
			}
		}
	}
	for _, o := range p.Options {
		if o.Kind == "reject_once" || o.Kind == "reject_always" {
			reply["result"] = map[string]any{
				"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID},
			}
			_ = c.write(reply)
			return
		}
	}
	reply["result"] = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	_ = c.write(reply)
}

// toolCalls reports how many tool calls the agent made.
func (c *acpConn) toolCalls() int {
	c.textMu.Lock()
	defer c.textMu.Unlock()
	return c.tools
}

// note surfaces a diagnostic without failing the run.
func (c *acpConn) note(format string, args ...any) {
	if c.observer != nil {
		c.observer.OnEvent(Event{Kind: EventNote, Role: c.role, Detail: fmt.Sprintf(format, args...)})
	}
}

func (c *acpConn) collected() string {
	c.textMu.Lock()
	defer c.textMu.Unlock()
	return c.text.String()
}
