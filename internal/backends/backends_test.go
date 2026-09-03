package backends_test

import (
	"strings"
	"testing"

	"github.com/rclod/codewalk/internal/backends"
	"github.com/rclod/codewalk/internal/config"
)

func clearCredentials(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "XAI_API_KEY"} {
		t.Setenv(key, "")
	}
}

func TestProvidersWithoutCredentialsAreSkippedNotFatal(t *testing.T) {
	clearCredentials(t)
	t.Setenv("OPENAI_API_KEY", "test-key")

	cfg := config.Default()
	cfg.Defaults.Backend = "openai"
	reg, err := backends.Build(cfg, backends.Options{RequireRoles: []string{"author"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := reg.Get("openai"); !ok {
		t.Error("the credentialled provider should be registered")
	}
	if _, ok := reg.Get("anthropic"); ok {
		t.Error("a provider without a credential should be skipped, not registered")
	}
	// Harnesses need no credential, so they are always available to configure.
	if _, ok := reg.Get("claude-code"); !ok {
		t.Error("harness backends should be registered regardless of provider keys")
	}
}

func TestMissingDefaultBackendExplainsWhatToDo(t *testing.T) {
	clearCredentials(t)
	cfg := config.Default()
	cfg.Defaults.Backend = "anthropic"

	_, err := backends.Build(cfg, backends.Options{RequireRoles: []string{"author"}})
	if err == nil {
		t.Fatal("expected an error when the default backend has no credential")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should name the missing environment variable: %v", err)
	}
	if !strings.Contains(err.Error(), "Usable backends") && !strings.Contains(err.Error(), "config init") {
		t.Errorf("error should tell the user what they can do instead: %v", err)
	}
}

func TestRoleBindingsAndOverrides(t *testing.T) {
	clearCredentials(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	cfg := config.Default()
	cfg.Defaults.Backend = "anthropic"
	cfg.Agents["author"] = config.AgentConfig{Backend: "openai"}

	reg, err := backends.Build(cfg, backends.Options{})
	if err != nil {
		t.Fatal(err)
	}
	author, err := reg.For("author")
	if err != nil || author.Name() != "openai" {
		t.Errorf("author backend = %v, %v", author, err)
	}
	investigator, _ := reg.For("investigator")
	if investigator.Name() != "anthropic" {
		t.Errorf("investigator backend = %s, want the default", investigator.Name())
	}

	// A backend override applies to every role, which is what makes an
	// experiment a single-variable change.
	overridden, err := backends.Build(cfg, backends.Options{BackendOverride: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"author", "investigator", "judge"} {
		b, err := overridden.For(role)
		if err != nil || b.Name() != "openai" {
			t.Errorf("role %s = %v, %v; the override should apply everywhere", role, b, err)
		}
	}
}

func TestModelOverrideAppearsInDescriptors(t *testing.T) {
	clearCredentials(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	cfg := config.Default()
	reg, err := backends.Build(cfg, backends.Options{ModelOverride: "experimental-model"})
	if err != nil {
		t.Fatal(err)
	}
	// Run provenance has to record what actually ran, or experiments cannot be
	// attributed afterwards.
	if d := reg.Descriptors([]string{"author"}); d["author"] != "anthropic:experimental-model" {
		t.Errorf("descriptor = %q", d["author"])
	}
}
