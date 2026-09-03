package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrecedenceRepoOverridesUserOverridesDefaults(t *testing.T) {
	home := t.TempDir()
	repoDir := t.TempDir()
	t.Setenv("CODEWALK_HOME", home)
	t.Setenv("CODEWALK_CONFIG", filepath.Join(home, "config.toml"))

	write(t, filepath.Join(home, "config.toml"), `
[defaults]
backend = "openai"
depth = "deep"
format = "markdown"

[providers.openai]
model = "user-model"
`)
	write(t, filepath.Join(repoDir, ".codewalk.toml"), `
[defaults]
depth = "brief"

[providers.openai]
model = "repo-model"
`)

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Repository config wins over user config, which wins over defaults.
	if cfg.Defaults.Depth != "brief" {
		t.Errorf("depth = %q, want brief from the repository config", cfg.Defaults.Depth)
	}
	if cfg.Defaults.Backend != "openai" {
		t.Errorf("backend = %q, want openai from the user config", cfg.Defaults.Backend)
	}
	if cfg.Defaults.Format != "markdown" {
		t.Errorf("format = %q, want markdown from the user config", cfg.Defaults.Format)
	}
	// A partial provider override must not discard the rest of the entry.
	p := cfg.Providers["openai"]
	if p.Model != "repo-model" {
		t.Errorf("model = %q, want repo-model", p.Model)
	}
	if p.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("api_key_env = %q, want the default to survive a partial override", p.APIKeyEnv)
	}
	if p.Type != "openai" {
		t.Errorf("type = %q, want the default to survive a partial override", p.Type)
	}
}

func TestEnvironmentOverridesFiles(t *testing.T) {
	repoDir := t.TempDir()
	t.Setenv("CODEWALK_HOME", t.TempDir())
	t.Setenv("CODEWALK_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	write(t, filepath.Join(repoDir, ".codewalk.toml"), "[defaults]\nbackend = \"anthropic\"\ndepth = \"brief\"\n")

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Depth != "brief" {
		t.Fatalf("precondition failed: depth = %q", cfg.Defaults.Depth)
	}
	cfg.applyEnv(func(key string) string {
		switch key {
		case "CODEWALK_DEPTH":
			return "deep"
		case "CODEWALK_SERVER_PORT":
			return "9999"
		case "CODEWALK_EDITOR":
			return "false"
		}
		return ""
	})
	if cfg.Defaults.Depth != "deep" {
		t.Errorf("depth = %q, want the environment to win over the repository config", cfg.Defaults.Depth)
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("port = %d, want 9999", cfg.Server.Port)
	}
	if cfg.Analysis.Editor {
		t.Error("editor should be disabled by CODEWALK_EDITOR=false")
	}
}

func TestValidateRejectsUnknownEntries(t *testing.T) {
	cases := map[string]string{
		"unknown provider type": "[providers.custom]\ntype = \"telepathy\"\n",
		"unknown harness type":  "[harnesses.custom]\ntype = \"telepathy\"\n",
		"unknown agent role":    "[agents.pontificator]\nbackend = \"anthropic\"\n",
		"unknown backend":       "[agents.author]\nbackend = \"nonexistent\"\n",
		"unknown depth":         "[defaults]\ndepth = \"exhaustive\"\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			if err := cfg.Merge(doc); err != nil {
				t.Fatalf("merge: %v", err)
			}
			if err := cfg.Validate(); err == nil {
				t.Error("expected validation to fail")
			}
		})
	}
}

func TestForRoleFallsBackToDefaults(t *testing.T) {
	cfg := Default()
	cfg.Defaults.Backend = "anthropic"
	cfg.Defaults.Model = "default-model"
	cfg.Agents["author"] = AgentConfig{Backend: "openai"}

	author := cfg.ForRole("author")
	if author.Backend != "openai" {
		t.Errorf("author backend = %q, want the explicit binding", author.Backend)
	}
	if author.Model != "default-model" {
		t.Errorf("author model = %q, want the default model to apply", author.Model)
	}
	if author.MaxSteps != cfg.Analysis.MaxSteps {
		t.Errorf("author max steps = %d, want the analysis default", author.MaxSteps)
	}

	editor := cfg.ForRole("editor")
	if editor.Backend != "anthropic" {
		t.Errorf("editor backend = %q, want the default backend", editor.Backend)
	}
}

func TestRoleEnabled(t *testing.T) {
	cfg := Default()
	if !cfg.RoleEnabled("editor") {
		t.Error("editor should be enabled by default")
	}
	cfg.Analysis.Editor = false
	if cfg.RoleEnabled("editor") {
		t.Error("analysis.editor = false should disable the editor stage")
	}
	enabled := true
	cfg.Agents["editor"] = AgentConfig{Enabled: &enabled}
	if !cfg.RoleEnabled("editor") {
		t.Error("an explicit agent-level enable should win over the analysis default")
	}
}

func TestStarterConfigIsValid(t *testing.T) {
	// The file `codewalk config init` writes must itself load cleanly.
	cfg := Default()
	if err := cfg.Merge(StarterConfig); err != nil {
		t.Fatalf("starter config does not parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("starter config does not validate: %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
