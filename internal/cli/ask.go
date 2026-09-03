package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rclod/codewalk/internal/backends"
	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/pipeline"
	"github.com/rclod/codewalk/internal/run"
	"github.com/rclod/codewalk/internal/service"
	"github.com/rclod/codewalk/internal/walkthrough"
)

const askUsage = `codewalk ask — ask a follow-up question about a walkthrough

Usage:
  codewalk ask <run-id|latest|walkthrough.json> "your question" [flags]

Examples:
  codewalk ask latest "why is the worker involved?"
  codewalk ask latest "go deeper on step 4"
  codewalk ask 20260902-142530-a1b2c3 "where is this state persisted?"
  codewalk ask walkthrough.json "explain only the database changes"

Follow-ups reuse the walkthrough, the evidence and the repository understanding
from the original run instead of investigating from scratch.
`

func runAsk(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "ask", askUsage)
	repoFlag := fs.String("repo", "", "Repository the walkthrough describes (default: the one recorded with the run)")
	quiet := fs.Bool("quiet", false, "Suppress progress output")
	jsonOut := fs.Bool("json", false, "Emit the answer as JSON")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		fmt.Fprint(env.Stderr, askUsage)
		return fmt.Errorf("ask needs a walkthrough and a question")
	}
	target := rest[0]
	question := strings.Join(rest[1:], " ")

	var progress *progressReporter
	if !*quiet {
		progress = newProgressReporter(env.Stderr)
		defer progress.stop()
	}

	if strings.HasSuffix(target, ".json") {
		if _, err := os.Stat(target); err == nil {
			return askFile(ctx, env, target, question, *repoFlag, *jsonOut, progress)
		}
	}

	svc, err := newService()
	if err != nil {
		return err
	}
	res, err := svc.Ask(ctx, service.AskRequest{
		RunID:          target,
		Question:       question,
		RepositoryPath: resolveRepoPathOptional(env, *repoFlag),
		Observer:       progress,
	})
	if progress != nil {
		progress.stop()
	}
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Fprintln(env.Stdout)
	fmt.Fprintln(env.Stdout, strings.TrimSpace(res.Answer))
	return nil
}

// askFile answers a question about a walkthrough JSON file, which is the
// one-shot workflow: generate to a file, then interrogate it.
func askFile(ctx context.Context, env Env, path, question, repoFlag string, jsonOut bool, progress *progressReporter) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	w, err := walkthrough.Decode(data)
	if err != nil {
		return err
	}
	repoPath := firstNonEmpty(resolveRepoPathOptional(env, repoFlag), w.Scope.RepositoryPath, env.Workdir)
	repo, err := gitrepo.Discover(repoPath)
	if err != nil {
		return fmt.Errorf("could not open the repository this walkthrough describes: %w", err)
	}
	cfg, err := config.Load(repo.Root)
	if err != nil {
		return err
	}
	registry, err := backends.Build(cfg, backends.Options{Observer: progress, RequireRoles: []string{"followup"}})
	if err != nil {
		return err
	}
	res, err := pipeline.Ask(ctx, pipeline.FollowUpOptions{
		Repo:        repo,
		Change:      changeSetFromScope(w.Scope),
		Walkthrough: w,
		Question:    question,
		Config:      cfg,
		Registry:    registry,
		Observer:    progress,
	})
	if progress != nil {
		progress.stop()
	}
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Fprintln(env.Stdout)
	fmt.Fprintln(env.Stdout, strings.TrimSpace(res.Answer))
	return nil
}

func changeSetFromScope(s walkthrough.Scope) *gitrepo.ChangeSet {
	if s.Selector == "" || s.Selector == "repository" {
		return nil
	}
	return &gitrepo.ChangeSet{
		Mode:       gitrepo.SelectorMode(s.Selector),
		Base:       s.Base,
		Head:       s.Head,
		BaseCommit: s.BaseCommit,
		HeadCommit: s.HeadCommit,
		Branch:     s.Branch,
		Files:      s.ChangedFiles,
		Stats:      s.Stats,
	}
}

func resolveRepoPathOptional(env Env, repoFlag string) string {
	if repoFlag == "" {
		return ""
	}
	return resolveRepoPath(env, repoFlag)
}

func formatUsage(m run.Metrics) string {
	if m.Usage.Calls == 0 {
		return "no model calls"
	}
	summary := fmt.Sprintf("%d model calls, %s in / %s out tokens",
		m.Usage.Calls, humanCount(m.Usage.InputTokens), humanCount(m.Usage.OutputTokens))
	if m.Usage.CachedInputTokens > 0 {
		summary += fmt.Sprintf(" (%s cached)", humanCount(m.Usage.CachedInputTokens))
	}
	if m.ToolCalls > 0 {
		summary += fmt.Sprintf(", %d tool calls", m.ToolCalls)
	}
	return summary
}

func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
