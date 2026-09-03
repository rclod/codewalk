// Package cli implements the codewalk command line interface.
//
// Commands are dispatched by hand rather than through a framework: the command
// surface is small, and keeping it dependency-free makes `go install` fast and
// the binary easy to audit.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/run"
	"github.com/rclod/codewalk/internal/service"
)

// Version is set at build time with -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

// Env carries the process environment so commands stay testable.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	// Workdir is the directory commands resolve relative paths against.
	Workdir string
}

// DefaultEnv returns the real process environment.
func DefaultEnv() Env {
	wd, _ := os.Getwd()
	return Env{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Workdir: wd}
}

type command struct {
	name    string
	aliases []string
	summary string
	run     func(ctx context.Context, env Env, args []string) error
}

func commands() []command {
	return []command{
		{
			name:    "pr",
			aliases: []string{"change", "diff"},
			summary: "Explain a change: the working tree, a branch, a commit or a range",
			run:     runChange,
		},
		{
			name:    "codebase",
			aliases: []string{"repo"},
			summary: "Explain a repository's architecture and important behaviour",
			run:     runCodebase,
		},
		{
			name:    "ask",
			summary: "Ask a follow-up question about a walkthrough",
			run:     runAsk,
		},
		{
			name:    "runs",
			aliases: []string{"list"},
			summary: "List previous walkthrough runs",
			run:     runRuns,
		},
		{
			name:    "show",
			summary: "Show a stored walkthrough",
			run:     runShow,
		},
		{
			name:    "serve",
			summary: "Run the local HTTP API and web UI",
			run:     runServe,
		},
		{
			name:    "config",
			summary: "Inspect and initialise configuration",
			run:     runConfig,
		},
		{
			name:    "eval",
			summary: "Evaluate walkthrough quality",
			run:     runEval,
		},
		{
			name:    "version",
			summary: "Print the codewalk version",
			run: func(_ context.Context, env Env, _ []string) error {
				fmt.Fprintf(env.Stdout, "codewalk %s\n", Version)
				return nil
			},
		},
	}
}

// Main runs the CLI and returns a process exit code.
func Main(args []string) int {
	env := DefaultEnv()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if len(args) == 0 {
		usage(env.Stdout)
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		usage(env.Stdout)
		return 0
	case "-v", "--version":
		fmt.Fprintf(env.Stdout, "codewalk %s\n", Version)
		return 0
	}

	for _, c := range commands() {
		if c.name == args[0] || contains(c.aliases, args[0]) {
			if err := c.run(ctx, env, args[1:]); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return 2
				}
				if errors.Is(err, context.Canceled) {
					fmt.Fprintln(env.Stderr, "cancelled")
					return 130
				}
				fmt.Fprintf(env.Stderr, "codewalk: %s\n", err)
				return 1
			}
			return 0
		}
	}
	fmt.Fprintf(env.Stderr, "codewalk: unknown command %q\n\n", args[0])
	usage(env.Stderr)
	return 2
}

func usage(out io.Writer) {
	fmt.Fprint(out, `codewalk — guided code walkthroughs

codewalk explains code so you can understand it quickly: what a change does,
how a system fits together, and where to look next. It explains code; it does
not review or grade it.

Usage:
  codewalk <command> [flags]

Commands:
`)
	for _, c := range commands() {
		fmt.Fprintf(out, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprint(out, `
Common usage:
  codewalk pr                      Explain the current branch or working tree
  codewalk pr --base main          Compare against a specific base branch
  codewalk pr main..feature        Explain an explicit revision range
  codewalk pr --format json        Emit the canonical walkthrough as JSON
  codewalk codebase                Explain the whole repository
  codewalk ask latest "why is the worker involved?"
  codewalk serve                   Open the local web UI

Run 'codewalk <command> --help' for command flags.
`)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// newFlagSet creates a flag set that reports errors through the returned error
// rather than exiting the process.
func newFlagSet(env Env, name, usageText string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprint(env.Stderr, usageText)
		fmt.Fprintln(env.Stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	return fs
}

// parseArgs parses flags that appear before, between or after positional
// arguments. Go's flag package stops at the first non-flag argument, which
// would silently ignore `codewalk show latest --format markdown` — a surprise
// worth removing rather than documenting.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// openStore opens the run store, creating it on first use.
func openStore() (*run.Store, error) {
	return run.NewStore(RunsDir())
}

// RunsDir returns the directory where runs are persisted.
func RunsDir() string {
	if d := os.Getenv("CODEWALK_RUNS_DIR"); d != "" {
		return d
	}
	return filepath.Join(config.UserDir(), "runs")
}

func newService() (*service.Service, error) {
	store, err := openStore()
	if err != nil {
		return nil, err
	}
	return service.New(store, Version), nil
}

// resolveRepoPath turns a --repo flag into an absolute path.
func resolveRepoPath(env Env, repoFlag string) string {
	if repoFlag == "" {
		return env.Workdir
	}
	if filepath.IsAbs(repoFlag) {
		return repoFlag
	}
	return filepath.Join(env.Workdir, repoFlag)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
