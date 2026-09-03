// Package backends constructs agent backends from configuration.
//
// It is the single place where configuration meets credentials: API keys are
// read from the environment variables named by the configuration and passed
// directly to a client. They are never stored, logged or persisted.
package backends

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/llm"
)

// MissingCredentialError reports a backend that is configured but unusable
// because its credential environment variable is unset.
type MissingCredentialError struct {
	Backend string
	EnvVar  string
}

func (e *MissingCredentialError) Error() string {
	return fmt.Sprintf("backend %q needs an API key: set %s in your environment", e.Backend, e.EnvVar)
}

// Options controls registry construction.
type Options struct {
	// Observer receives progress events from every backend.
	Observer agent.Observer
	// RequireRoles lists the roles that must resolve to a usable backend.
	// Backends that are configured but lack credentials are skipped rather than
	// failing the whole build, so a user with one provider configured is not
	// blocked by the presence of defaults for others.
	RequireRoles []string
	// ModelOverride replaces the model for every backend, for experiments.
	ModelOverride string
	// BackendOverride forces every role onto one backend.
	BackendOverride string
}

// Build creates a registry of backends from configuration.
func Build(cfg *config.Config, opts Options) (*agent.Registry, error) {
	reg := agent.NewRegistry()
	var skipped []string

	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Providers[name]
		client, err := newClient(name, p, cfg, opts)
		if err != nil {
			var missing *MissingCredentialError
			if asMissing(err, &missing) {
				skipped = append(skipped, fmt.Sprintf("%s (set %s)", name, missing.EnvVar))
				continue
			}
			return nil, err
		}
		reg.Register(name, agent.NewProviderBackend(name, client, cfg.Analysis.MaxSteps, opts.Observer))
	}

	hnames := make([]string, 0, len(cfg.Harnesses))
	for n := range cfg.Harnesses {
		hnames = append(hnames, n)
	}
	sort.Strings(hnames)
	for _, name := range hnames {
		h := cfg.Harnesses[name]
		spec := agent.HarnessSpec{
			Type:    h.Type,
			Command: h.Command,
			Args:    h.Args,
			Model:   h.Model,
			Timeout: time.Duration(h.TimeoutSeconds) * time.Second,
			Env:     h.Env,
		}
		if opts.ModelOverride != "" {
			spec.Model = opts.ModelOverride
		}
		b, err := agent.NewHarnessBackend(name, spec, opts.Observer)
		if err != nil {
			return nil, err
		}
		reg.Register(name, b)
	}

	defaultBackend := cfg.Defaults.Backend
	if opts.BackendOverride != "" {
		defaultBackend = opts.BackendOverride
	}
	if defaultBackend != "" {
		if err := reg.SetDefault(defaultBackend); err != nil {
			return nil, fmt.Errorf("%w%s", err, hint(defaultBackend, cfg, skipped, reg))
		}
	}

	if opts.BackendOverride == "" {
		for role, a := range cfg.Agents {
			if a.Backend == "" {
				continue
			}
			if err := reg.BindRole(role, a.Backend); err != nil {
				return nil, fmt.Errorf("agent role %q: %w", role, err)
			}
		}
	}

	for _, role := range opts.RequireRoles {
		if _, err := reg.For(role); err != nil {
			return nil, fmt.Errorf("%w%s", err, hint(defaultBackend, cfg, skipped, reg))
		}
	}
	return reg, nil
}

func hint(requested string, cfg *config.Config, skipped []string, reg *agent.Registry) string {
	var b strings.Builder
	if len(skipped) > 0 {
		b.WriteString("\n\nConfigured but missing credentials: " + strings.Join(skipped, ", "))
	}
	if names := reg.Names(); len(names) > 0 {
		b.WriteString("\nUsable backends: " + strings.Join(names, ", "))
	} else {
		b.WriteString("\nNo backend is usable yet. Run `codewalk config init` and set an API key, or configure a local agent harness.")
	}
	return b.String()
}

func newClient(name string, p config.ProviderConfig, cfg *config.Config, opts Options) (llm.Client, error) {
	key := ""
	if p.APIKeyEnv != "" {
		key = strings.TrimSpace(os.Getenv(p.APIKeyEnv))
	}
	if key == "" {
		return nil, &MissingCredentialError{Backend: name, EnvVar: p.APIKeyEnv}
	}
	model := p.Model
	if opts.ModelOverride != "" {
		model = opts.ModelOverride
	} else if cfg.Defaults.Model != "" && name == cfg.Defaults.Backend {
		model = cfg.Defaults.Model
	}
	timeout := time.Duration(p.RequestTimeoutSeconds) * time.Second

	switch p.Type {
	case "anthropic":
		return llm.NewAnthropic(llm.AnthropicOptions{
			Name: name, Model: model, APIKey: key, BaseURL: p.BaseURL,
			MaxTokens: p.MaxTokens, ReasoningEffort: p.ReasoningEffort, Timeout: timeout,
		}), nil
	case "openai":
		return llm.NewOpenAI(llm.OpenAIOptions{
			Name: name, Model: model, APIKey: key, BaseURL: p.BaseURL,
			MaxTokens: p.MaxTokens, ReasoningEffort: p.ReasoningEffort, Timeout: timeout,
		}), nil
	case "openai_compatible":
		return llm.NewOpenAI(llm.OpenAIOptions{
			Name: name, Model: model, APIKey: key, BaseURL: p.BaseURL,
			MaxTokens: p.MaxTokens, ReasoningEffort: p.ReasoningEffort, Timeout: timeout,
			Compatible: true,
		}), nil
	default:
		return nil, fmt.Errorf("provider %q: unsupported type %q", name, p.Type)
	}
}

func asMissing(err error, target **MissingCredentialError) bool {
	if m, ok := err.(*MissingCredentialError); ok {
		*target = m
		return true
	}
	return false
}
