package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/repomap"
	"github.com/rclod/codewalk/internal/tools"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// FollowUpOptions configures a follow-up question against an existing
// walkthrough.
//
// Follow-ups reuse the repository understanding that generating the walkthrough
// already paid for: the walkthrough itself, the investigator's evidence and the
// mental model are all handed back to the model, so a question like "go deeper
// on step 4" costs one call rather than a second full investigation.
type FollowUpOptions struct {
	Repo        *gitrepo.Repo
	Change      *gitrepo.ChangeSet
	Walkthrough *walkthrough.Walkthrough
	// Evidence and MentalModel are the persisted artifacts from the original
	// run. Either may be nil.
	Evidence    json.RawMessage
	MentalModel json.RawMessage
	// History is the prior conversation, oldest first.
	History  []Message
	Question string

	Config   *config.Config
	Registry *agent.Registry
	Observer agent.Observer
}

// Message is one prior turn of a follow-up conversation.
type Message struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

// FollowUpResult is the answer to a follow-up question.
type FollowUpResult struct {
	Answer     string    `json:"answer"`
	Backend    string    `json:"backend"`
	Model      string    `json:"model,omitempty"`
	Usage      llm.Usage `json:"usage"`
	ToolCalls  int       `json:"tool_calls"`
	DurationMS int64     `json:"duration_ms"`
	// FilesInspected records any additional files read while answering.
	FilesInspected []string `json:"files_inspected,omitempty"`
}

// Ask answers a follow-up question about an existing walkthrough.
func Ask(ctx context.Context, opts FollowUpOptions) (*FollowUpResult, error) {
	if opts.Walkthrough == nil {
		return nil, fmt.Errorf("follow-up: no walkthrough to ask about")
	}
	if strings.TrimSpace(opts.Question) == "" {
		return nil, fmt.Errorf("follow-up: no question given")
	}
	started := time.Now()

	b, err := opts.Registry.For("followup")
	if err != nil {
		return nil, err
	}
	agentCfg := opts.Config.ForRole("followup")

	var repoTools *tools.Repo
	if opts.Repo != nil {
		rev := ""
		if opts.Change != nil && opts.Change.HeadCommit != "" {
			rev = opts.Change.HeadCommit
		}
		rmap, _ := repomap.Build(ctx, opts.Repo, rev)
		repoTools = tools.NewRepo(opts.Repo, opts.Change, rmap, tools.Options{
			MaxFileBytes:    opts.Config.Analysis.MaxFileBytes,
			MaxDiffBytes:    opts.Config.Analysis.MaxDiffBytesPerFile,
			AllowGitHistory: opts.Config.Analysis.GitHistory,
		})
	}

	wJSON, _ := json.MarshalIndent(opts.Walkthrough, "", "  ")
	prompt := &strings.Builder{}
	fmt.Fprintf(prompt, "Repository: %s\n", opts.Walkthrough.Scope.RepositoryName)
	if opts.Walkthrough.Kind == walkthrough.KindChange {
		fmt.Fprintf(prompt, "The walkthrough explains a change: %s\n", scopeDescription(opts.Walkthrough.Scope))
	} else {
		prompt.WriteString("The walkthrough explains this repository's architecture and behaviour.\n")
	}
	fmt.Fprintf(prompt, "\n<walkthrough>\n%s\n</walkthrough>\n", wJSON)
	if len(opts.MentalModel) > 0 {
		fmt.Fprintf(prompt, "\n<mental_model>\n%s\n</mental_model>\n", opts.MentalModel)
	}
	if len(opts.Evidence) > 0 {
		fmt.Fprintf(prompt, "\n<evidence>\n%s\n</evidence>\n", truncateJSON(opts.Evidence, 24000))
	}
	if len(opts.History) > 0 {
		prompt.WriteString("\n<conversation_so_far>\n")
		for _, m := range opts.History {
			fmt.Fprintf(prompt, "%s: %s\n\n", m.Role, m.Content)
		}
		prompt.WriteString("</conversation_so_far>\n")
	}
	fmt.Fprintf(prompt, "\nThe reader asks:\n\n%s\n", opts.Question)

	task := agent.Task{
		Role:            "followup",
		System:          followUpSystem,
		Prompt:          prompt.String(),
		MaxSteps:        maxInt(6, agentCfg.MaxSteps/2),
		Model:           agentCfg.Model,
		ReasoningEffort: agentCfg.ReasoningEffort,
	}
	res, err := b.Execute(ctx, task, repoTools)
	if err != nil {
		return nil, err
	}
	out := &FollowUpResult{
		Answer:     res.Text,
		Backend:    res.Backend,
		Model:      res.Model,
		Usage:      res.Usage,
		ToolCalls:  res.ToolCalls,
		DurationMS: time.Since(started).Milliseconds(),
	}
	if repoTools != nil {
		out.FilesInspected = repoTools.FilesInspected()
	}
	return out, nil
}

func scopeDescription(s walkthrough.Scope) string {
	switch s.Selector {
	case "working-tree":
		return "uncommitted changes in the working tree"
	case "staged":
		return "staged changes"
	case "commit":
		return "commit " + s.HeadCommit
	default:
		if s.Base != "" {
			return s.Head + " compared with " + s.Base
		}
		return s.Selector
	}
}

// truncateJSON bounds a large artifact so a follow-up prompt stays affordable.
func truncateJSON(data json.RawMessage, max int) string {
	if len(data) <= max {
		return string(data)
	}
	return string(data[:max]) + "\n... [evidence truncated] ..."
}
