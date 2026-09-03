// Package config loads codewalk's layered configuration.
//
// Precedence, highest first:
//
//	CLI flags  ->  environment variables  ->  repository config
//	           ->  user config            ->  built-in defaults
//
// Flags are applied by the CLI after Load returns; everything below flags is
// resolved here. Credentials are never stored in configuration files: a
// provider entry names an *environment variable* that holds its key.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// UserConfigFileName is the file loaded from the user's codewalk directory.
const UserConfigFileName = "config.toml"

// RepoConfigFileNames are the repository-local configuration files, in the
// order they are looked for.
var RepoConfigFileNames = []string{".codewalk.toml", ".codewalk/config.toml"}

// Config is the fully resolved configuration.
type Config struct {
	Defaults  Defaults                  `toml:"defaults"`
	Server    Server                    `toml:"server"`
	Analysis  Analysis                  `toml:"analysis"`
	Cache     Cache                     `toml:"cache"`
	Providers map[string]ProviderConfig `toml:"providers"`
	Harnesses map[string]HarnessConfig  `toml:"harnesses"`
	Agents    map[string]AgentConfig    `toml:"agents"`
	Eval      Eval                      `toml:"eval"`

	// Sources records which files contributed, for `codewalk config show`.
	Sources []string `toml:"-"`
}

// Defaults holds the settings that shape a default invocation.
type Defaults struct {
	// Backend names an entry in Providers or Harnesses, used by any agent role
	// that does not name its own.
	Backend string `toml:"backend"`
	// Model overrides the backend's model for every role. Empty means "use each
	// backend's configured model".
	Model string `toml:"model"`
	// Base overrides the automatically detected base branch.
	Base string `toml:"base"`
	// Depth is auto | brief | standard | deep. "auto" lets the pipeline choose
	// depth from conceptual complexity, which is almost always what you want.
	Depth string `toml:"depth"`
	// Format is the default CLI output format: text | markdown | json.
	Format string `toml:"format"`
}

// Server configures `codewalk serve`.
type Server struct {
	// Host defaults to loopback. Binding to a non-loopback address exposes an
	// unauthenticated API and requires an explicit opt-in flag.
	Host        string `toml:"host"`
	Port        int    `toml:"port"`
	OpenBrowser bool   `toml:"open_browser"`
}

// Analysis bounds how much repository material the pipeline gathers.
type Analysis struct {
	// MaxChangedFiles caps how many changed files are listed to agents.
	MaxChangedFiles int `toml:"max_changed_files"`
	// MaxDiffBytesPerFile truncates very large per-file diffs.
	MaxDiffBytesPerFile int `toml:"max_diff_bytes_per_file"`
	// MaxDiffBytesTotal bounds the whole diff included in a prompt. Per-file
	// truncation alone is not enough: a change touching hundreds of files would
	// still overflow any context window.
	MaxDiffBytesTotal int `toml:"max_diff_bytes_total"`
	// MaxFileBytes caps a single file read by an agent tool.
	MaxFileBytes int `toml:"max_file_bytes"`
	// IncludeGenerated includes machine-generated files in diffs.
	IncludeGenerated bool `toml:"include_generated"`
	// GitHistory allows agents to inspect commit history.
	GitHistory bool `toml:"git_history"`
	// MaxSteps caps tool-use iterations per agent role.
	MaxSteps int `toml:"max_steps"`
	// Editor enables the clarity editor stage.
	Editor bool `toml:"editor"`
	// Grounding enables the grounding check stage.
	Grounding bool `toml:"grounding"`
}

// Cache configures reusable repository understanding.
type Cache struct {
	Enabled bool   `toml:"enabled"`
	Dir     string `toml:"dir"`
	// TTLHours bounds how long a cached repository map stays valid even if the
	// commit has not changed.
	TTLHours int `toml:"ttl_hours"`
}

// ProviderConfig describes a direct model-provider integration.
type ProviderConfig struct {
	// Type selects the wire protocol: anthropic | openai | openai_compatible.
	// "xai" and other OpenAI-compatible services use openai_compatible.
	Type string `toml:"type"`
	// Model is the default model for this provider.
	Model string `toml:"model"`
	// APIKeyEnv names the environment variable holding the credential. The key
	// itself is never read from or written to configuration files.
	APIKeyEnv string `toml:"api_key_env"`
	// BaseURL overrides the provider endpoint.
	BaseURL string `toml:"base_url"`
	// MaxTokens caps a single completion.
	MaxTokens int `toml:"max_tokens"`
	// ReasoningEffort is passed through where the provider supports it.
	ReasoningEffort string `toml:"reasoning_effort"`
	// RequestTimeoutSeconds bounds a single model call.
	RequestTimeoutSeconds int `toml:"request_timeout_seconds"`
}

// HarnessConfig describes a local agent harness (an existing coding agent that
// already has filesystem, shell, git and search capabilities).
type HarnessConfig struct {
	// Type selects the adapter: claude_code | codex | opencode | acp | command.
	Type string `toml:"type"`
	// Command is the executable to run. Empty uses the adapter's default.
	Command string `toml:"command"`
	// Args are extra arguments inserted before the adapter's own arguments.
	Args []string `toml:"args"`
	// Model is passed to the harness when it accepts one.
	Model string `toml:"model"`
	// TimeoutSeconds bounds a single harness invocation.
	TimeoutSeconds int `toml:"timeout_seconds"`
	// Env lists environment variable names to forward to the harness process.
	// Values are taken from the current environment; no values are stored here.
	Env []string `toml:"env"`
}

// AgentConfig binds a pipeline role to a backend.
type AgentConfig struct {
	// Backend names a provider or harness entry.
	Backend string `toml:"backend"`
	Model   string `toml:"model"`
	// ReasoningEffort overrides the backend default for this role.
	ReasoningEffort string `toml:"reasoning_effort"`
	// MaxSteps overrides the tool-use iteration cap for this role.
	MaxSteps int `toml:"max_steps"`
	// Enabled allows a role to be switched off (used for pipeline experiments,
	// for example running without the clarity editor).
	Enabled *bool `toml:"enabled"`
}

// Eval configures the evaluation system.
type Eval struct {
	// Judges lists backends used for semantic evaluation. Using more than one
	// family reduces single-model bias.
	Judges []string `toml:"judges"`
	// Mode is smoke | standard | full.
	Mode string `toml:"mode"`
	// CorpusDir is the default benchmark corpus location.
	CorpusDir string `toml:"corpus_dir"`
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Defaults: Defaults{
			Backend: "anthropic",
			Depth:   "auto",
			Format:  "text",
		},
		Server: Server{Host: "127.0.0.1", Port: 7457, OpenBrowser: true},
		Analysis: Analysis{
			MaxChangedFiles:     400,
			MaxDiffBytesPerFile: 60000,
			MaxDiffBytesTotal:   400000,
			MaxFileBytes:        200000,
			IncludeGenerated:    false,
			GitHistory:          true,
			MaxSteps:            40,
			Editor:              true,
			Grounding:           true,
		},
		Cache: Cache{Enabled: true, TTLHours: 24 * 14},
		Providers: map[string]ProviderConfig{
			"anthropic": {
				Type:      "anthropic",
				Model:     "claude-sonnet-5",
				APIKeyEnv: "ANTHROPIC_API_KEY",
				MaxTokens: 16000,
			},
			"openai": {
				Type:      "openai",
				Model:     "gpt-5.2",
				APIKeyEnv: "OPENAI_API_KEY",
				MaxTokens: 16000,
			},
			"xai": {
				Type:      "openai_compatible",
				Model:     "grok-4",
				APIKeyEnv: "XAI_API_KEY",
				BaseURL:   "https://api.x.ai/v1",
				MaxTokens: 16000,
			},
		},
		Harnesses: map[string]HarnessConfig{
			"claude-code": {Type: "claude_code", Command: "claude", TimeoutSeconds: 900},
			"codex":       {Type: "codex", Command: "codex", TimeoutSeconds: 900},
			"opencode":    {Type: "opencode", Command: "opencode", TimeoutSeconds: 900},
		},
		Agents: map[string]AgentConfig{},
		Eval:   Eval{Mode: "smoke", CorpusDir: "benchmarks/cases"},
	}
}

// Load resolves configuration for a repository directory. repoDir may be empty
// when no repository is involved.
func Load(repoDir string) (*Config, error) {
	cfg := Default()

	if path := UserConfigPath(); path != "" {
		if err := cfg.mergeFile(path); err != nil {
			return nil, err
		}
	}
	if repoDir != "" {
		for _, name := range RepoConfigFileNames {
			p := filepath.Join(repoDir, filepath.FromSlash(name))
			if _, err := os.Stat(p); err == nil {
				if err := cfg.mergeFile(p); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	cfg.applyEnv(os.Getenv)
	return cfg, cfg.Validate()
}

// UserConfigPath returns the user-level configuration file path, honouring
// CODEWALK_CONFIG and CODEWALK_HOME.
func UserConfigPath() string {
	if p := os.Getenv("CODEWALK_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(UserDir(), UserConfigFileName)
}

// UserDir returns codewalk's user directory (~/.codewalk by default).
func UserDir() string {
	if d := os.Getenv("CODEWALK_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codewalk"
	}
	return filepath.Join(home, ".codewalk")
}

func (c *Config) mergeFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if err := c.Merge(string(data)); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	c.Sources = append(c.Sources, path)
	return nil
}

// Merge decodes TOML over the current configuration. Keys absent from the
// document keep their existing value, which is what produces layered
// precedence; map entries are merged field by field rather than replaced, so a
// repository config can override one provider setting without restating the
// rest.
func (c *Config) Merge(doc string) error {
	var layer Config
	if _, err := toml.Decode(doc, &layer); err != nil {
		return err
	}
	// Scalars: decode again over the live struct so unset keys are preserved.
	saveProviders, saveHarnesses, saveAgents := c.Providers, c.Harnesses, c.Agents
	c.Providers, c.Harnesses, c.Agents = nil, nil, nil
	if _, err := toml.Decode(doc, c); err != nil {
		return err
	}
	c.Providers, c.Harnesses, c.Agents = saveProviders, saveHarnesses, saveAgents

	for name, p := range layer.Providers {
		base := c.Providers[name]
		c.Providers[name] = mergeProvider(base, p)
	}
	for name, h := range layer.Harnesses {
		base := c.Harnesses[name]
		c.Harnesses[name] = mergeHarness(base, h)
	}
	if c.Agents == nil {
		c.Agents = map[string]AgentConfig{}
	}
	for name, a := range layer.Agents {
		base := c.Agents[name]
		c.Agents[name] = mergeAgent(base, a)
	}
	return nil
}

func mergeProvider(base, over ProviderConfig) ProviderConfig {
	if over.Type != "" {
		base.Type = over.Type
	}
	if over.Model != "" {
		base.Model = over.Model
	}
	if over.APIKeyEnv != "" {
		base.APIKeyEnv = over.APIKeyEnv
	}
	if over.BaseURL != "" {
		base.BaseURL = over.BaseURL
	}
	if over.MaxTokens != 0 {
		base.MaxTokens = over.MaxTokens
	}
	if over.ReasoningEffort != "" {
		base.ReasoningEffort = over.ReasoningEffort
	}
	if over.RequestTimeoutSeconds != 0 {
		base.RequestTimeoutSeconds = over.RequestTimeoutSeconds
	}
	return base
}

func mergeHarness(base, over HarnessConfig) HarnessConfig {
	if over.Type != "" {
		base.Type = over.Type
	}
	if over.Command != "" {
		base.Command = over.Command
	}
	if len(over.Args) > 0 {
		base.Args = over.Args
	}
	if over.Model != "" {
		base.Model = over.Model
	}
	if over.TimeoutSeconds != 0 {
		base.TimeoutSeconds = over.TimeoutSeconds
	}
	if len(over.Env) > 0 {
		base.Env = over.Env
	}
	return base
}

func mergeAgent(base, over AgentConfig) AgentConfig {
	if over.Backend != "" {
		base.Backend = over.Backend
	}
	if over.Model != "" {
		base.Model = over.Model
	}
	if over.ReasoningEffort != "" {
		base.ReasoningEffort = over.ReasoningEffort
	}
	if over.MaxSteps != 0 {
		base.MaxSteps = over.MaxSteps
	}
	if over.Enabled != nil {
		base.Enabled = over.Enabled
	}
	return base
}

// applyEnv layers CODEWALK_* environment variables over file configuration.
// getenv is injectable so the precedence rules are testable without touching
// the process environment.
func (c *Config) applyEnv(getenv func(string) string) {
	set := func(key string, apply func(string)) {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			apply(v)
		}
	}
	setInt := func(key string, apply func(int)) {
		set(key, func(v string) {
			if n, err := strconv.Atoi(v); err == nil {
				apply(n)
			}
		})
	}
	setBool := func(key string, apply func(bool)) {
		set(key, func(v string) {
			if b, err := strconv.ParseBool(v); err == nil {
				apply(b)
			}
		})
	}

	set("CODEWALK_BACKEND", func(v string) { c.Defaults.Backend = v })
	set("CODEWALK_MODEL", func(v string) { c.Defaults.Model = v })
	set("CODEWALK_BASE", func(v string) { c.Defaults.Base = v })
	set("CODEWALK_DEPTH", func(v string) { c.Defaults.Depth = v })
	set("CODEWALK_FORMAT", func(v string) { c.Defaults.Format = v })
	set("CODEWALK_SERVER_HOST", func(v string) { c.Server.Host = v })
	setInt("CODEWALK_SERVER_PORT", func(v int) { c.Server.Port = v })
	setBool("CODEWALK_OPEN_BROWSER", func(v bool) { c.Server.OpenBrowser = v })
	setBool("CODEWALK_CACHE", func(v bool) { c.Cache.Enabled = v })
	set("CODEWALK_CACHE_DIR", func(v string) { c.Cache.Dir = v })
	setBool("CODEWALK_EDITOR", func(v bool) { c.Analysis.Editor = v })
	setBool("CODEWALK_GROUNDING", func(v bool) { c.Analysis.Grounding = v })
	setBool("CODEWALK_GIT_HISTORY", func(v bool) { c.Analysis.GitHistory = v })
	setInt("CODEWALK_MAX_STEPS", func(v int) { c.Analysis.MaxSteps = v })
}

// Validate checks internal consistency without contacting any provider.
func (c *Config) Validate() error {
	for name, p := range c.Providers {
		switch p.Type {
		case "anthropic", "openai", "openai_compatible":
		case "":
			return fmt.Errorf("provider %q: missing type", name)
		default:
			return fmt.Errorf("provider %q: unknown type %q (want anthropic, openai or openai_compatible)", name, p.Type)
		}
	}
	for name, h := range c.Harnesses {
		switch h.Type {
		case "claude_code", "codex", "opencode", "acp", "command":
		case "":
			return fmt.Errorf("harness %q: missing type", name)
		default:
			return fmt.Errorf("harness %q: unknown type %q", name, h.Type)
		}
	}
	for role, a := range c.Agents {
		if !ValidRole(role) {
			return fmt.Errorf("unknown agent role %q (valid roles: %s)", role, strings.Join(Roles, ", "))
		}
		if a.Backend != "" {
			if _, ok := c.Providers[a.Backend]; !ok {
				if _, ok := c.Harnesses[a.Backend]; !ok {
					return fmt.Errorf("agent %q references unknown backend %q", role, a.Backend)
				}
			}
		}
	}
	if c.Defaults.Backend != "" {
		if _, ok := c.Providers[c.Defaults.Backend]; !ok {
			if _, ok := c.Harnesses[c.Defaults.Backend]; !ok {
				return fmt.Errorf("defaults.backend %q is neither a configured provider nor a harness", c.Defaults.Backend)
			}
		}
	}
	switch c.Defaults.Depth {
	case "", "auto", "brief", "standard", "deep":
	default:
		return fmt.Errorf("defaults.depth %q is not auto, brief, standard or deep", c.Defaults.Depth)
	}
	return nil
}

// Roles are the configurable pipeline roles.
var Roles = []string{
	"investigator", "mental_model", "planner", "author", "editor",
	"grounding", "followup", "judge", "extractor",
}

// ValidRole reports whether name is a known pipeline role.
func ValidRole(name string) bool {
	for _, r := range Roles {
		if r == name {
			return true
		}
	}
	return false
}

// ForRole resolves the effective backend configuration for a pipeline role,
// applying defaults where the role does not override them.
func (c *Config) ForRole(role string) AgentConfig {
	a := c.Agents[role]
	if a.Backend == "" {
		a.Backend = c.Defaults.Backend
	}
	if a.Model == "" {
		a.Model = c.Defaults.Model
	}
	if a.MaxSteps == 0 {
		a.MaxSteps = c.Analysis.MaxSteps
	}
	return a
}

// RoleEnabled reports whether a stage should run. Stages default to enabled
// unless configuration explicitly disables them.
func (c *Config) RoleEnabled(role string) bool {
	if a, ok := c.Agents[role]; ok && a.Enabled != nil {
		return *a.Enabled
	}
	switch role {
	case "editor":
		return c.Analysis.Editor
	case "grounding":
		return c.Analysis.Grounding
	}
	return true
}

// StarterConfig is the file `codewalk config init` writes. It is intentionally
// mostly commented out: good defaults should mean an empty file works. It lives
// here, next to the schema it configures, so a test can prove the two agree.
const StarterConfig = `# codewalk configuration
#
# Precedence, highest first:
#   CLI flags -> environment variables -> repository .codewalk.toml
#             -> this file -> built-in defaults
#
# API keys are never stored here. Each provider names the environment variable
# that holds its key.

[defaults]
# Backend used by every pipeline stage that does not name its own.
# Any provider or harness defined below.
backend = "anthropic"

# Walkthrough depth: "auto" lets conceptual complexity decide, which is
# usually what you want. Other values: "brief", "standard", "deep".
depth = "auto"

# Default CLI output: "text", "markdown" or "json".
format = "text"

# Base branch for change walkthroughs. Empty means detect it.
# base = "main"

[analysis]
# The clarity editor and grounding check cost extra model calls and materially
# improve accuracy and concision. Turn them off for the fastest possible run.
editor = true
grounding = true
git_history = true
max_steps = 40

[server]
host = "127.0.0.1"
port = 7457
open_browser = true

[providers.anthropic]
type = "anthropic"
model = "claude-sonnet-5"
api_key_env = "ANTHROPIC_API_KEY"

[providers.openai]
type = "openai"
model = "gpt-5.2"
api_key_env = "OPENAI_API_KEY"

[providers.xai]
type = "openai_compatible"
model = "grok-4"
api_key_env = "XAI_API_KEY"
base_url = "https://api.x.ai/v1"

# Local agent harnesses already have filesystem, search and git access.
# codewalk runs them in a read-only posture.
[harnesses.claude-code]
type = "claude_code"
command = "claude"

[harnesses.codex]
type = "codex"
command = "codex"

[harnesses.opencode]
type = "opencode"
command = "opencode"

# Different stages can run on different backends. This is optional: strong
# defaults mean you never have to touch it. It is most useful for experiments,
# and for pairing a thorough investigator with a strong writer.
#
# [agents.investigator]
# backend = "openai"
# reasoning_effort = "high"
#
# [agents.author]
# backend = "anthropic"
`
