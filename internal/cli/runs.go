package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/rclod/codewalk/internal/render"
)

const runsUsage = `codewalk runs — list previous walkthrough runs

Usage:
  codewalk runs [flags]
`

func runRuns(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "runs", runsUsage)
	limit := fs.Int("limit", 20, "Maximum runs to list")
	jsonOut := fs.Bool("json", false, "Emit JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	summaries, err := store.List(*limit)
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summaries)
	}
	if len(summaries) == 0 {
		fmt.Fprintln(env.Stdout, "No walkthroughs yet. Try: codewalk pr")
		return nil
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tWHEN\tREPOSITORY\tSCOPE\tTITLE")
	for _, s := range summaries {
		title := s.Title
		if s.Status != "complete" {
			title = "(" + s.Status + ") " + title
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ID, humanTime(s.CreatedAt), s.Repository, s.Scope, title)
	}
	return tw.Flush()
}

const showUsage = `codewalk show — display a stored walkthrough

Usage:
  codewalk show <run-id|latest> [flags]
`

func runShow(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "show", showUsage)
	format := fs.String("format", "text", "Output format: text, markdown or json")
	deepDives := fs.Bool("deep-dives", false, "Include deep dives")
	diagrams := fs.Bool("diagrams", false, "Include diagram sources")
	artifact := fs.String("artifact", "", "Print a stage artifact instead (evidence, mental_model, plan, grounding, ...)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	target := "latest"
	if len(rest) > 0 {
		target = rest[0]
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	if *artifact != "" {
		id, err := store.Resolve(target)
		if err != nil {
			return err
		}
		data, err := store.Artifact(id, *artifact)
		if err != nil {
			return fmt.Errorf("run %s has no %q artifact (available: %v)", id, *artifact, store.ArtifactNames(id))
		}
		_, err = env.Stdout.Write(data)
		return err
	}
	w, err := store.Walkthrough(target)
	if err != nil {
		return err
	}
	if err := validFormat(*format); err != nil {
		return err
	}
	switch *format {
	case "json":
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(w)
	case "markdown", "md":
		return render.Markdown(env.Stdout, w)
	default:
		opts := render.DefaultTerminalOptions()
		opts.DeepDives = *deepDives
		opts.Diagrams = *diagrams
		return render.Terminal(env.Stdout, w, opts)
	}
}

func humanTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}
