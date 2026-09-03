package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rclod/codewalk/internal/config"
)

const configUsage = `codewalk config — inspect and initialise configuration

Usage:
  codewalk config init     Write a starter user configuration file
  codewalk config show     Show the resolved configuration and where it came from
  codewalk config path     Show the paths codewalk uses
  codewalk config check    Check that configured backends are usable

Configuration precedence, highest first:
  CLI flags -> environment variables -> repository config -> user config -> defaults

API keys are never stored in configuration. A provider entry names the
environment variable that holds its key.
`

func runConfig(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(env.Stdout, configUsage)
		return nil
	}
	switch args[0] {
	case "init":
		return configInit(env, args[1:])
	case "show":
		return configShow(env)
	case "path":
		return configPath(env)
	case "check":
		return configCheck(env)
	case "-h", "--help":
		fmt.Fprint(env.Stdout, configUsage)
		return nil
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func configInit(env Env, args []string) error {
	fs := newFlagSet(env, "config init", "codewalk config init — write a starter configuration file\n")
	force := fs.Bool("force", false, "Overwrite an existing configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := config.UserConfigPath()
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(env.Stdout, "Configuration already exists at %s\nUse --force to overwrite it.\n", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(config.StarterConfig), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Wrote %s\n\n", path)
	fmt.Fprint(env.Stdout, `Next: make a model backend available.

  Anthropic:  export ANTHROPIC_API_KEY=your-api-key
  OpenAI:     export OPENAI_API_KEY=your-api-key
  xAI:        export XAI_API_KEY=your-api-key

Or use a local agent harness you already have installed, for example:

  codewalk pr --backend claude-code

Then run 'codewalk config check' to confirm, and 'codewalk pr' in a repository.
`)
	return nil
}

func configShow(env Env) error {
	cfg, err := config.Load(env.Workdir)
	if err != nil {
		return err
	}
	out := env.Stdout
	fmt.Fprintln(out, "Sources (later entries override earlier ones):")
	fmt.Fprintln(out, "  built-in defaults")
	for _, s := range cfg.Sources {
		fmt.Fprintf(out, "  %s\n", s)
	}
	fmt.Fprintln(out, "  environment variables (CODEWALK_*)")
	fmt.Fprintln(out)

	fmt.Fprintln(out, "[defaults]")
	fmt.Fprintf(out, "  backend = %q\n", cfg.Defaults.Backend)
	if cfg.Defaults.Model != "" {
		fmt.Fprintf(out, "  model   = %q\n", cfg.Defaults.Model)
	}
	fmt.Fprintf(out, "  depth   = %q\n", cfg.Defaults.Depth)
	fmt.Fprintf(out, "  format  = %q\n", cfg.Defaults.Format)
	if cfg.Defaults.Base != "" {
		fmt.Fprintf(out, "  base    = %q\n", cfg.Defaults.Base)
	}

	fmt.Fprintln(out, "\n[analysis]")
	fmt.Fprintf(out, "  editor = %v, grounding = %v, git_history = %v, max_steps = %d\n",
		cfg.Analysis.Editor, cfg.Analysis.Grounding, cfg.Analysis.GitHistory, cfg.Analysis.MaxSteps)

	fmt.Fprintln(out, "\n[server]")
	fmt.Fprintf(out, "  %s:%d (open_browser = %v)\n", cfg.Server.Host, cfg.Server.Port, cfg.Server.OpenBrowser)

	fmt.Fprintln(out, "\n[providers]")
	for _, name := range sortedKeys(cfg.Providers) {
		p := cfg.Providers[name]
		fmt.Fprintf(out, "  %-12s type=%s model=%s key=%s\n", name, p.Type, p.Model, credentialState(p.APIKeyEnv))
	}
	fmt.Fprintln(out, "\n[harnesses]")
	for _, name := range sortedKeys(cfg.Harnesses) {
		h := cfg.Harnesses[name]
		fmt.Fprintf(out, "  %-12s type=%s command=%s %s\n", name, h.Type, h.Command, executableState(h.Command))
	}
	if len(cfg.Agents) > 0 {
		fmt.Fprintln(out, "\n[agents]")
		for _, role := range sortedKeys(cfg.Agents) {
			a := cfg.Agents[role]
			fmt.Fprintf(out, "  %-14s backend=%s model=%s\n", role, orNone(a.Backend), orNone(a.Model))
		}
	}
	return nil
}

func configPath(env Env) error {
	fmt.Fprintf(env.Stdout, "config   %s\n", config.UserConfigPath())
	fmt.Fprintf(env.Stdout, "runs     %s\n", RunsDir())
	cacheDir, _ := os.UserCacheDir()
	fmt.Fprintf(env.Stdout, "cache    %s\n", filepath.Join(cacheDir, "codewalk"))
	fmt.Fprintf(env.Stdout, "repo     %s (looked for as %s)\n", env.Workdir, strings.Join(config.RepoConfigFileNames, " or "))
	return nil
}

func configCheck(env Env) error {
	cfg, err := config.Load(env.Workdir)
	if err != nil {
		return err
	}
	usable := 0
	fmt.Fprintln(env.Stdout, "Providers:")
	for _, name := range sortedKeys(cfg.Providers) {
		p := cfg.Providers[name]
		if os.Getenv(p.APIKeyEnv) != "" {
			usable++
			fmt.Fprintf(env.Stdout, "  ✓ %-12s %s (%s is set)\n", name, p.Model, p.APIKeyEnv)
		} else {
			fmt.Fprintf(env.Stdout, "  · %-12s unavailable: set %s\n", name, p.APIKeyEnv)
		}
	}
	fmt.Fprintln(env.Stdout, "Harnesses:")
	for _, name := range sortedKeys(cfg.Harnesses) {
		h := cfg.Harnesses[name]
		if _, err := exec.LookPath(h.Command); err == nil {
			usable++
			fmt.Fprintf(env.Stdout, "  ✓ %-12s %s found in PATH\n", name, h.Command)
		} else {
			fmt.Fprintf(env.Stdout, "  · %-12s unavailable: %s not found in PATH\n", name, h.Command)
		}
	}
	fmt.Fprintf(env.Stdout, "\nDefault backend: %s\n", cfg.Defaults.Backend)
	if usable == 0 {
		return fmt.Errorf("no backend is usable yet: set a provider API key or install a supported agent harness")
	}
	return nil
}

func credentialState(envVar string) string {
	if envVar == "" {
		return "(none configured)"
	}
	if os.Getenv(envVar) != "" {
		return envVar + " (set)"
	}
	return envVar + " (not set)"
}

func executableState(command string) string {
	if command == "" {
		return ""
	}
	if _, err := exec.LookPath(command); err == nil {
		return "(found)"
	}
	return "(not in PATH)"
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func orNone(s string) string {
	if s == "" {
		return "(default)"
	}
	return s
}
