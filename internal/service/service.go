// Package service is the application layer shared by the CLI and the HTTP API.
//
// Both surfaces do the same thing — resolve a repository and a change, build
// backends, run the pipeline, persist the run — so that logic lives here once.
// A new surface (an editor extension, a CI integration) should be able to reuse
// this package without reimplementing any of it.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/backends"
	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/pipeline"
	"github.com/rclod/codewalk/internal/run"
	"github.com/rclod/codewalk/internal/tools"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// Service generates and stores walkthroughs.
type Service struct {
	store   *run.Store
	version string
}

// New creates a service.
func New(store *run.Store, version string) *Service {
	return &Service{store: store, version: version}
}

// Store exposes the run store for surfaces that list or read runs.
func (s *Service) Store() *run.Store { return s.store }

// GenerateRequest describes a walkthrough to produce.
type GenerateRequest struct {
	// RepositoryPath is the directory to analyse; the repository root is
	// discovered from it.
	RepositoryPath string
	Kind           walkthrough.Kind
	Selection      gitrepo.Selection
	Depth          string
	Focus          string
	Subtree        string

	// BackendOverride and ModelOverride apply to every pipeline role. They exist
	// for experiments and for `--backend` / `--model` on the command line.
	BackendOverride string
	ModelOverride   string

	// Config overrides the layered configuration. When nil it is loaded from the
	// repository and user configuration.
	Config *config.Config

	Observer agent.Observer
}

// GenerateResult is a completed generation.
type GenerateResult struct {
	Run         *run.Run
	Walkthrough *walkthrough.Walkthrough
	Pipeline    *pipeline.Result
}

// Generate produces a walkthrough and persists the run.
func (s *Service) Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	repo, err := gitrepo.Discover(req.RepositoryPath)
	if err != nil {
		return nil, err
	}

	cfg := req.Config
	if cfg == nil {
		cfg, err = config.Load(repo.Root)
		if err != nil {
			return nil, err
		}
	}
	if req.Depth == "" {
		req.Depth = cfg.Defaults.Depth
	}

	kind := req.Kind
	if kind == "" {
		kind = walkthrough.KindChange
	}

	var change *gitrepo.ChangeSet
	if kind == walkthrough.KindChange {
		sel := req.Selection
		if sel.Base == "" {
			sel.Base = cfg.Defaults.Base
		}
		change, err = repo.BuildChangeSet(ctx, sel)
		if err != nil {
			return nil, err
		}
		if change.Empty() {
			return nil, fmt.Errorf("there are no changes to explain in %s", change.Describe())
		}
	}

	registry, err := backends.Build(cfg, backends.Options{
		Observer:        req.Observer,
		RequireRoles:    []string{"investigator", "mental_model", "author"},
		BackendOverride: req.BackendOverride,
		ModelOverride:   req.ModelOverride,
	})
	if err != nil {
		return nil, err
	}

	runID := run.NewID(time.Now())
	record := &run.Run{
		ID:        runID,
		CreatedAt: time.Now().UTC(),
		Version:   s.version,
		Kind:      kind,
		Status:    "running",
		Repository: run.Repository{
			Name:       repo.Name,
			Path:       repo.Root,
			RemoteHost: repo.RemoteHost,
		},
		Pipeline: run.Pipeline{
			Depth:        req.Depth,
			Focus:        req.Focus,
			Backends:     registry.Descriptors(config.Roles),
			ConfigDigest: ConfigDigest(cfg),
		},
	}
	if change != nil {
		record.Repository.HeadCommit = change.HeadCommit
		// Record what is being explained before generation starts, so an
		// in-flight run is identifiable in `codewalk runs` and in the UI.
		record.Scope = walkthrough.Scope{
			RepositoryName: repo.Name,
			RepositoryPath: repo.Root,
			RemoteHost:     repo.RemoteHost,
			Selector:       string(change.Mode),
			Base:           change.Base,
			Head:           change.Head,
			BaseCommit:     change.BaseCommit,
			HeadCommit:     change.HeadCommit,
			Branch:         change.Branch,
			ChangedFiles:   change.Files,
			Stats:          change.Stats,
		}
	} else {
		record.Scope = walkthrough.Scope{
			RepositoryName: repo.Name,
			RepositoryPath: repo.Root,
			RemoteHost:     repo.RemoteHost,
			Selector:       "repository",
			Subtree:        req.Subtree,
		}
	}
	if s.store != nil {
		if err := s.store.Save(record); err != nil {
			return nil, err
		}
	}

	result, runErr := pipeline.Run(ctx, pipeline.Options{
		Repo:     repo,
		Change:   change,
		Kind:     kind,
		Config:   cfg,
		Registry: registry,
		Observer: req.Observer,
		Depth:    req.Depth,
		Focus:    req.Focus,
		Subtree:  req.Subtree,
		RunID:    runID,
		Version:  s.version,
	})

	if result != nil {
		record.Pipeline.Stages = result.Stages
		record.Metrics = metricsFrom(result)
	}
	if runErr != nil {
		record.Status = "failed"
		record.Error = runErr.Error()
		if s.store != nil {
			_ = s.store.Save(record)
			if result != nil {
				_ = s.store.SaveArtifacts(runID, result.Artifacts)
			}
		}
		return nil, runErr
	}

	w := result.Walkthrough
	record.Status = "complete"
	record.Scope = w.Scope
	record.Complexity = w.Complexity

	if s.store != nil {
		if err := s.store.Save(record); err != nil {
			return nil, err
		}
		if err := s.store.SaveWalkthrough(runID, w); err != nil {
			return nil, err
		}
		if err := s.store.SaveArtifacts(runID, result.Artifacts); err != nil {
			return nil, err
		}
	}
	return &GenerateResult{Run: record, Walkthrough: w, Pipeline: result}, nil
}

func metricsFrom(r *pipeline.Result) run.Metrics {
	m := run.Metrics{
		DurationMS:        r.DurationMS,
		Usage:             r.Usage,
		CodewalkToolCalls: len(r.ToolCalls),
		ToolBreakdown:     tools.SortedInvocationSummary(r.ToolCalls),
		ModelCalls:        r.Usage.Calls,
	}
	// Harness backends report their own tool activity; without adding it, a
	// harness run appears to have inspected nothing.
	m.ToolCalls = len(r.ToolCalls)
	for _, stage := range r.Stages {
		m.ToolCalls += stage.ToolCalls
	}
	files := map[string]bool{}
	for _, inv := range r.ToolCalls {
		if inv.Tool == "read_file" && inv.Argument != "" {
			files[inv.Argument] = true
		}
	}
	m.FilesInspected = len(files)
	return m
}

// AskRequest is a follow-up question against a stored run.
type AskRequest struct {
	RunID    string
	Question string
	// RepositoryPath overrides the repository recorded with the run, which is
	// needed when the run was produced elsewhere.
	RepositoryPath string
	Config         *config.Config
	Observer       agent.Observer
}

// AskResult is an answer plus the turns appended to the conversation.
type AskResult struct {
	Answer string                   `json:"answer"`
	Result *pipeline.FollowUpResult `json:"result"`
	RunID  string                   `json:"run_id"`
}

// Ask answers a follow-up question about a stored run and appends both turns to
// the run's conversation, so later questions keep the thread.
func (s *Service) Ask(ctx context.Context, req AskRequest) (*AskResult, error) {
	if s.store == nil {
		return nil, fmt.Errorf("no run store is configured")
	}
	record, err := s.store.Load(req.RunID)
	if err != nil {
		return nil, err
	}
	w, err := s.store.Walkthrough(record.ID)
	if err != nil {
		return nil, fmt.Errorf("run %s has no walkthrough: %w", record.ID, err)
	}

	repoPath := req.RepositoryPath
	if repoPath == "" {
		repoPath = record.Repository.Path
	}
	var repo *gitrepo.Repo
	var change *gitrepo.ChangeSet
	if repoPath != "" {
		if repo, err = gitrepo.Discover(repoPath); err == nil && record.Kind == walkthrough.KindChange {
			change = changeSetFromScope(record.Scope)
		}
	}

	cfg := req.Config
	if cfg == nil {
		root := ""
		if repo != nil {
			root = repo.Root
		}
		if cfg, err = config.Load(root); err != nil {
			return nil, err
		}
	}
	registry, err := backends.Build(cfg, backends.Options{
		Observer:     req.Observer,
		RequireRoles: []string{"followup"},
	})
	if err != nil {
		return nil, err
	}

	evidence, _ := s.store.Artifact(record.ID, "evidence")
	mentalModel, _ := s.store.Artifact(record.ID, "mental_model")

	var history []pipeline.Message
	for _, t := range record.Conversation {
		history = append(history, pipeline.Message{Role: t.Role, Content: t.Content})
	}

	res, err := pipeline.Ask(ctx, pipeline.FollowUpOptions{
		Repo:        repo,
		Change:      change,
		Walkthrough: w,
		Evidence:    json.RawMessage(evidence),
		MentalModel: json.RawMessage(mentalModel),
		History:     history,
		Question:    req.Question,
		Config:      cfg,
		Registry:    registry,
		Observer:    req.Observer,
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if err := s.store.AppendTurn(record.ID,
		run.Turn{Role: "user", Content: req.Question, At: now},
		run.Turn{Role: "assistant", Content: res.Answer, At: now, Backend: res.Backend, Usage: res.Usage, ToolCalls: res.ToolCalls},
	); err != nil {
		return nil, err
	}
	return &AskResult{Answer: res.Answer, Result: res, RunID: record.ID}, nil
}

// changeSetFromScope reconstructs enough of a change set for follow-up tools to
// answer questions about the same change the walkthrough explained.
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

// ConfigDigest hashes the settings that affect walkthrough content, so two runs
// can be compared for configuration equality. Credentials are not part of the
// configuration and therefore cannot leak into the digest.
func ConfigDigest(cfg *config.Config) string {
	payload := struct {
		Defaults config.Defaults               `json:"defaults"`
		Analysis config.Analysis               `json:"analysis"`
		Agents   map[string]config.AgentConfig `json:"agents"`
	}{cfg.Defaults, cfg.Analysis, cfg.Agents}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
