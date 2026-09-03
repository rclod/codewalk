package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/rclod/codewalk/internal/tools"
)

// HarnessSpec describes how to invoke a local agent harness. It mirrors the
// harness section of the configuration file, but this package stays
// configuration-free so it can be reused and tested independently.
type HarnessSpec struct {
	// Type is claude_code, codex, opencode, acp or command.
	Type    string
	Command string
	Args    []string
	Model   string
	Timeout time.Duration
	// Env lists environment variable names forwarded from the current process.
	// Values are never stored in configuration.
	Env []string
	// PromptAsArgument appends the prompt as the final argument instead of
	// writing it to stdin. Some headless CLIs take the prompt as a flag value
	// and never read stdin.
	PromptAsArgument bool
}

// HarnessBackend runs a task by invoking an existing local coding agent.
//
// The harness already has filesystem, search and git access, so codewalk does
// not supply its own tools here; it supplies the task and the working
// directory. Harnesses are always invoked in a read-only posture where the
// harness supports one.
type HarnessBackend struct {
	name     string
	spec     HarnessSpec
	observer Observer
}

// NewHarnessBackend creates a harness-backed agent.
func NewHarnessBackend(name string, spec HarnessSpec, obs Observer) (*HarnessBackend, error) {
	if spec.Command == "" {
		spec.Command = defaultHarnessCommand(spec.Type)
	}
	if spec.Command == "" {
		return nil, fmt.Errorf("harness %q: no command configured", name)
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 15 * time.Minute
	}
	return &HarnessBackend{name: name, spec: spec, observer: obs}, nil
}

func defaultHarnessCommand(kind string) string {
	switch kind {
	case "claude_code":
		return "claude"
	case "codex":
		return "codex"
	case "opencode":
		return "opencode"
	default:
		return ""
	}
}

func (h *HarnessBackend) Name() string { return h.name }
func (h *HarnessBackend) Kind() string { return "harness" }

func (h *HarnessBackend) Descriptor() string {
	if h.spec.Model != "" {
		return h.name + ":" + h.spec.Model
	}
	return h.name
}

func (h *HarnessBackend) Capabilities() Capabilities {
	return Capabilities{
		Inference: true,
		// The harness brings its own tools; codewalk deliberately does not
		// duplicate them, which is the point of harness support.
		OwnTools:  true,
		Subagents: h.spec.Type == "claude_code",
	}
}

// Available reports whether the harness executable can be found. It is used by
// `codewalk config check` so a misconfiguration surfaces before a run starts.
func (h *HarnessBackend) Available() error {
	if _, err := exec.LookPath(h.spec.Command); err != nil {
		return fmt.Errorf("harness %q: executable %q not found in PATH", h.name, h.spec.Command)
	}
	return nil
}

// Execute runs the task through the harness process.
func (h *HarnessBackend) Execute(ctx context.Context, task Task, repo *tools.Repo) (*Result, error) {
	if h.spec.Type == "acp" {
		return h.executeACP(ctx, task, repo)
	}

	start := time.Now()
	workdir := "."
	if repo != nil {
		workdir = repo.Root()
	}

	prompt := task.Prompt
	if task.ExpectJSON {
		prompt += jsonInstruction(task.SchemaHint)
	}

	args, stdin := h.buildInvocation(task, prompt)
	ctx, cancel := context.WithTimeout(ctx, h.spec.Timeout)
	defer cancel()

	h.emit(Event{Kind: EventModelCall, Role: task.Role, Detail: h.spec.Command})

	cmd := exec.CommandContext(ctx, h.spec.Command, args...)
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = h.environment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[:2000] + "…"
		}
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("harness %q timed out after %s (role %s)", h.name, h.spec.Timeout, task.Role)
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("harness %q failed (role %s): %s", h.name, task.Role, msg)
	}

	text, usage := parseHarnessOutput(h.spec.Type, stdout.String())
	text = strings.TrimSpace(stripANSI(text))
	if text == "" {
		return nil, fmt.Errorf("harness %q produced no output (role %s)", h.name, task.Role)
	}
	return &Result{
		Text:       text,
		Backend:    h.name,
		Model:      h.spec.Model,
		Usage:      usage,
		Steps:      1,
		DurationMS: elapsed(start),
	}, nil
}

// buildInvocation returns the argv and stdin for a harness call. Defaults keep
// each harness non-interactive and read-only; configured args are inserted
// first so a user can override them.
func (h *HarnessBackend) buildInvocation(task Task, prompt string) (args []string, stdin string) {
	args = append(args, h.spec.Args...)
	switch h.spec.Type {
	case "claude_code":
		args = append(args, "-p", "--output-format", "json", "--permission-mode", "plan")
		if h.spec.Model != "" {
			args = append(args, "--model", h.spec.Model)
		}
		if task.System != "" {
			args = append(args, "--append-system-prompt", task.System)
		}
		return args, prompt
	case "codex":
		args = append(args, "exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check")
		if h.spec.Model != "" {
			args = append(args, "--model", h.spec.Model)
		}
		args = append(args, "-")
		return args, combineSystemPrompt(task.System, prompt)
	case "opencode":
		args = append(args, "run")
		if h.spec.Model != "" {
			args = append(args, "-m", h.spec.Model)
		}
		return args, combineSystemPrompt(task.System, prompt)
	default: // "command": a user-defined executable
		combined := combineSystemPrompt(task.System, prompt)
		if h.spec.PromptAsArgument {
			return append(args, combined), ""
		}
		return args, combined
	}
}

func combineSystemPrompt(system, prompt string) string {
	if system == "" {
		return prompt
	}
	return system + "\n\n---\n\n" + prompt
}

// environment builds the child process environment. Only variables the user
// explicitly listed are forwarded beyond the inherited environment, and no
// values are ever read from configuration files.
func (h *HarnessBackend) environment() []string {
	env := os.Environ()
	for _, name := range h.spec.Env {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

func (h *HarnessBackend) emit(e Event) {
	if h.observer != nil {
		h.observer.OnEvent(e)
	}
}

// parseHarnessOutput extracts the final assistant message from a harness's
// output. Each harness has its own envelope; when parsing fails the raw output
// is used, which is almost always still the answer.
func parseHarnessOutput(kind, out string) (string, LLMUsage) {
	switch kind {
	case "claude_code":
		var envelope struct {
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
			Usage   struct {
				InputTokens          int `json:"input_tokens"`
				OutputTokens         int `json:"output_tokens"`
				CacheReadInputTokens int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &envelope); err == nil && envelope.Result != "" {
			return envelope.Result, LLMUsage{
				InputTokens:       envelope.Usage.InputTokens,
				OutputTokens:      envelope.Usage.OutputTokens,
				CachedInputTokens: envelope.Usage.CacheReadInputTokens,
				Calls:             1,
			}
		}
	case "codex":
		// codex exec --json emits one JSON event per line; the final assistant
		// message is the last event carrying text.
		var last string
		usage := LLMUsage{Calls: 1}
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var ev map[string]any
			if json.Unmarshal([]byte(line), &ev) != nil {
				continue
			}
			if t := extractCodexText(ev); t != "" {
				last = t
			}
			if u, ok := ev["usage"].(map[string]any); ok {
				usage.InputTokens = intFrom(u, "input_tokens")
				usage.OutputTokens = intFrom(u, "output_tokens")
			}
		}
		if last != "" {
			return last, usage
		}
	}
	return out, LLMUsage{Calls: 1}
}

// extractCodexText pulls assistant text out of a codex event without depending
// on a specific event schema version.
func extractCodexText(ev map[string]any) string {
	for _, key := range []string{"text", "message", "last_agent_message"} {
		if s, ok := ev[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	for _, key := range []string{"item", "msg", "data"} {
		if nested, ok := ev[key].(map[string]any); ok {
			if s := extractCodexText(nested); s != "" {
				return s
			}
		}
	}
	return ""
}

func intFrom(m map[string]any, key string) int {
	if f, ok := m[key].(float64); ok {
		return int(f)
	}
	return 0
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// stripANSI removes terminal escape sequences that interactive harnesses emit.
func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }
