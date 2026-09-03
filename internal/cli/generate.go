package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/render"
	"github.com/rclod/codewalk/internal/service"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// generateFlags are shared by `codewalk pr` and `codewalk codebase`.
type generateFlags struct {
	repo      string
	format    string
	output    string
	depth     string
	focus     string
	backend   string
	model     string
	quiet     bool
	deepDives bool
	diagrams  bool

	// Pipeline overrides, primarily for experiments.
	noEditor         bool
	noGrounding      bool
	noHistory        bool
	includeGenerated bool
}

func (g *generateFlags) register(fs flagSet) {
	fs.StringVar(&g.repo, "repo", "", "Repository to analyse (default: current directory)")
	fs.StringVar(&g.format, "format", "", "Output format: text, markdown or json (default: from config)")
	fs.StringVar(&g.output, "output", "", "Write output to a file instead of stdout")
	fs.StringVar(&g.depth, "depth", "", "Walkthrough depth: auto, brief, standard or deep")
	fs.StringVar(&g.focus, "focus", "", "Focus the walkthrough, e.g. \"just the database changes\"")
	fs.StringVar(&g.backend, "backend", "", "Backend for every stage (a configured provider or harness)")
	fs.StringVar(&g.model, "model", "", "Model for every stage")
	fs.BoolVar(&g.quiet, "quiet", false, "Suppress progress output")
	fs.BoolVar(&g.deepDives, "deep-dives", false, "Include deep dives in terminal output")
	fs.BoolVar(&g.diagrams, "diagrams", false, "Include diagram sources in terminal output")
	fs.BoolVar(&g.noEditor, "no-editor", false, "Skip the clarity editor stage")
	fs.BoolVar(&g.noGrounding, "no-grounding", false, "Skip the semantic grounding stage")
	fs.BoolVar(&g.noHistory, "no-git-history", false, "Do not let agents inspect commit history")
	fs.BoolVar(&g.includeGenerated, "include-generated", false, "Include generated files in the diff shown to agents")
}

// flagSet is the subset of *flag.FlagSet used above, so both commands can share
// registration without importing flag here.
type flagSet interface {
	StringVar(p *string, name string, value string, usage string)
	BoolVar(p *bool, name string, value bool, usage string)
}

const changeUsage = `codewalk pr — explain a change

Usage:
  codewalk pr [revision-range] [flags]

Examples:
  codewalk pr                        Explain the current branch, or uncommitted work
  codewalk pr --base main            Compare the current branch against main
  codewalk pr main..feature          Explain an explicit range
  codewalk pr --staged               Explain staged changes only
  codewalk pr abc1234                Explain a single commit
  codewalk pr --format json          Emit the canonical walkthrough as JSON
`

func runChange(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "pr", changeUsage)
	var g generateFlags
	g.register(fs)
	base := fs.String("base", "", "Base branch or revision to compare against")
	head := fs.String("head", "", "Head revision (default: HEAD)")
	staged := fs.Bool("staged", false, "Explain staged changes only")
	workingTree := fs.Bool("working-tree", false, "Explain all uncommitted changes")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}

	sel := gitrepo.Selection{Mode: gitrepo.ModeAuto, Base: *base, Head: *head}
	switch {
	case *staged && *workingTree:
		return fmt.Errorf("--staged and --working-tree cannot be combined")
	case *staged:
		sel.Mode = gitrepo.ModeStaged
	case *workingTree:
		sel.Mode = gitrepo.ModeWorkingTree
	}
	if len(rest) > 0 {
		sel.Spec = rest[0]
	}
	return generate(ctx, env, g, walkthrough.KindChange, sel, "")
}

const codebaseUsage = `codewalk codebase — explain a repository

Usage:
  codewalk codebase [subtree] [flags]

Examples:
  codewalk codebase                  Explain the whole repository
  codewalk codebase internal/billing Explain one part of it
  codewalk codebase --focus "how requests are authenticated"
`

func runCodebase(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "codebase", codebaseUsage)
	var g generateFlags
	g.register(fs)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	subtree := ""
	if len(rest) > 0 {
		subtree = rest[0]
	}
	return generate(ctx, env, g, walkthrough.KindCodebase, gitrepo.Selection{}, subtree)
}

func generate(ctx context.Context, env Env, g generateFlags, kind walkthrough.Kind, sel gitrepo.Selection, subtree string) error {
	repoPath := resolveRepoPath(env, g.repo)

	cfg, err := config.Load(repoPath)
	if err != nil {
		return err
	}
	if g.noEditor {
		cfg.Analysis.Editor = false
	}
	if g.noGrounding {
		cfg.Analysis.Grounding = false
	}
	if g.noHistory {
		cfg.Analysis.GitHistory = false
	}
	if g.includeGenerated {
		cfg.Analysis.IncludeGenerated = true
	}
	format := firstNonEmpty(g.format, cfg.Defaults.Format, "text")
	if err := validFormat(format); err != nil {
		return err
	}

	svc, err := newService()
	if err != nil {
		return err
	}

	var progress *progressReporter
	if !g.quiet {
		progress = newProgressReporter(env.Stderr)
		defer progress.stop()
	}

	res, err := svc.Generate(ctx, service.GenerateRequest{
		RepositoryPath:  repoPath,
		Kind:            kind,
		Selection:       sel,
		Depth:           firstNonEmpty(g.depth, cfg.Defaults.Depth),
		Focus:           g.focus,
		Subtree:         subtree,
		BackendOverride: g.backend,
		ModelOverride:   g.model,
		Config:          cfg,
		Observer:        progress,
	})
	if progress != nil {
		progress.stop()
	}
	if err != nil {
		return err
	}

	out := env.Stdout
	if g.output != "" {
		f, err := os.Create(g.output)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	if err := writeWalkthrough(out, res.Walkthrough, format, g); err != nil {
		return err
	}

	if !g.quiet && format != "json" {
		fmt.Fprintf(env.Stderr, "\nRun %s · %s · %s\n",
			res.Run.ID, formatDuration(res.Run.Metrics.DurationMS), formatUsage(res.Run.Metrics))
		fmt.Fprintf(env.Stderr, "Ask a follow-up:  codewalk ask latest \"...\"\n")
		fmt.Fprintf(env.Stderr, "Open in browser:  codewalk serve\n")
	}
	return nil
}

func writeWalkthrough(out io.Writer, w *walkthrough.Walkthrough, format string, g generateFlags) error {
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(w)
	case "markdown", "md":
		return render.Markdown(out, w)
	default:
		opts := render.DefaultTerminalOptions()
		opts.DeepDives = g.deepDives
		opts.Diagrams = g.diagrams
		return render.Terminal(out, w, opts)
	}
}

func validFormat(f string) error {
	switch f {
	case "text", "markdown", "md", "json":
		return nil
	default:
		return fmt.Errorf("unknown format %q (want text, markdown or json)", f)
	}
}

// progressReporter prints pipeline progress to stderr, keeping stdout clean so
// `codewalk pr --format json > walkthrough.json` works as expected.
type progressReporter struct {
	mu      sync.Mutex
	out     io.Writer
	started time.Time
	stage   string
	stopped bool
	tools   int
}

func newProgressReporter(out io.Writer) *progressReporter {
	return &progressReporter{out: out, started: time.Now()}
}

var stageLabels = map[string]string{
	"investigator": "Investigating the repository",
	"mental_model": "Working out what matters",
	"planner":      "Planning the walkthrough",
	"author":       "Writing the walkthrough",
	"editor":       "Editing for clarity",
	"grounding":    "Checking against the code",
	"correction":   "Applying corrections",
	"followup":     "Answering",
}

func (p *progressReporter) OnEvent(e agent.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	switch e.Kind {
	case agent.EventStageStart:
		p.stage = e.Role
		p.tools = 0
		label := stageLabels[e.Role]
		if label == "" {
			label = e.Role
		}
		fmt.Fprintf(p.out, "· %s (%s)\n", label, e.Detail)
	case agent.EventToolCall:
		p.tools++
		if p.tools <= 40 {
			fmt.Fprintf(p.out, "    %s %s\n", e.Tool, truncateDetail(e.Detail))
		} else if p.tools == 41 {
			fmt.Fprintln(p.out, "    …")
		}
	case agent.EventNote:
		fmt.Fprintf(p.out, "· %s: %s\n", e.Role, e.Detail)
	case agent.EventStageEnd:
		if e.Detail != "" {
			fmt.Fprintf(p.out, "    (%s)\n", e.Detail)
		}
	}
}

func (p *progressReporter) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
}

func truncateDetail(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	if len(s) > 90 {
		s = s[:90] + "…"
	}
	return s
}

func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}
