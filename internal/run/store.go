package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rclod/codewalk/internal/pipeline"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// Store persists runs on the local filesystem, one directory per run:
//
//	<dir>/<run-id>/run.json
//	<dir>/<run-id>/walkthrough.json
//	<dir>/<run-id>/artifacts/<stage>.json
//	<dir>/<run-id>/eval/<name>.json
//
// Plain files keep runs inspectable with ordinary tools, which matters when
// debugging a pipeline or a benchmark result.
type Store struct {
	dir string
}

// NewStore opens (and creates) a run store.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("run store: no directory configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("run store: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) runDir(id string) string { return filepath.Join(s.dir, id) }

// Save writes the run record.
func (s *Store) Save(r *Run) error {
	if r.ID == "" {
		return fmt.Errorf("run store: run has no id")
	}
	if err := os.MkdirAll(s.runDir(r.ID), 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.runDir(r.ID), "run.json"), r)
}

// SaveWalkthrough writes the canonical walkthrough for a run.
func (s *Store) SaveWalkthrough(id string, w *walkthrough.Walkthrough) error {
	if err := os.MkdirAll(s.runDir(id), 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.runDir(id), "walkthrough.json"), w)
}

// SaveArtifacts writes intermediate stage artifacts.
func (s *Store) SaveArtifacts(id string, artifacts pipeline.Artifacts) error {
	dir := filepath.Join(s.runDir(id), "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, data := range artifacts {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Artifact reads one stage artifact.
func (s *Store) Artifact(id, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.runDir(id), "artifacts", name+".json"))
}

// ArtifactNames lists the stage artifacts stored for a run.
func (s *Store) ArtifactNames(id string) []string {
	entries, err := os.ReadDir(filepath.Join(s.runDir(id), "artifacts"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(names)
	return names
}

// Load reads a run record.
func (s *Store) Load(id string) (*Run, error) {
	id, err := s.Resolve(id)
	if err != nil {
		return nil, err
	}
	var r Run
	if err := readJSON(filepath.Join(s.runDir(id), "run.json"), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Walkthrough reads the canonical walkthrough for a run.
func (s *Store) Walkthrough(id string) (*walkthrough.Walkthrough, error) {
	id, err := s.Resolve(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.runDir(id), "walkthrough.json"))
	if err != nil {
		if os.IsNotExist(err) {
			// Say why it is missing rather than surfacing a bare path error.
			if record, loadErr := s.Load(id); loadErr == nil {
				switch record.Status {
				case "running":
					return nil, fmt.Errorf("run %s is still generating", id)
				case "failed":
					return nil, fmt.Errorf("run %s failed: %s", id, record.Error)
				}
			}
			return nil, fmt.Errorf("run %s has no walkthrough", id)
		}
		return nil, err
	}
	return walkthrough.Decode(data)
}

// Resolve expands a unique run-id prefix, and accepts "latest".
func (s *Store) Resolve(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("no run id given")
	}
	if id == "latest" {
		// "latest" means the most recent run a reader can actually open. A run
		// that is still generating, or that failed, has no walkthrough, and
		// resolving to it would fail with a confusing missing-file error.
		summaries, err := s.List(0)
		if err != nil {
			return "", err
		}
		if len(summaries) == 0 {
			return "", fmt.Errorf("no runs have been recorded yet")
		}
		for _, summary := range summaries {
			if summary.Status == "complete" {
				return summary.ID, nil
			}
		}
		newest := summaries[0]
		return "", fmt.Errorf("no completed run yet: the most recent run %s is %s",
			newest.ID, newest.Status)
	}
	if _, err := os.Stat(filepath.Join(s.runDir(id), "run.json")); err == nil {
		return id, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), id) {
			matches = append(matches, e.Name())
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no run matches %q", id)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("run id %q is ambiguous (%d matches)", id, len(matches))
	}
}

// List returns run summaries, newest first. A limit of zero returns all runs.
func (s *Store) List(limit int) ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	// Run identifiers are time-ordered, so a reverse lexical sort is newest
	// first without reading every record.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	var out []Summary
	for _, name := range names {
		var r Run
		if err := readJSON(filepath.Join(s.runDir(name), "run.json"), &r); err != nil {
			continue
		}
		summary := Summary{
			ID:         r.ID,
			CreatedAt:  r.CreatedAt,
			Kind:       string(r.Kind),
			Repository: r.Repository.Name,
			Status:     r.Status,
			DurationMS: r.Metrics.DurationMS,
			Scope:      scopeLabel(r.Scope),
		}
		if w, err := s.Walkthrough(name); err == nil {
			summary.Title = w.Title
		}
		out = append(out, summary)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func scopeLabel(s walkthrough.Scope) string {
	switch s.Selector {
	case "repository":
		return "whole repository"
	case "working-tree":
		return "working tree"
	case "staged":
		return "staged changes"
	default:
		if s.Base != "" && s.Head != "" {
			return s.Base + ".." + s.Head
		}
		return s.Selector
	}
}

// AppendTurn adds a follow-up conversation turn to a run.
func (s *Store) AppendTurn(id string, turns ...Turn) error {
	r, err := s.Load(id)
	if err != nil {
		return err
	}
	r.Conversation = append(r.Conversation, turns...)
	return s.Save(r)
}

// SaveEval stores an evaluation result alongside a run.
func (s *Store) SaveEval(id, name string, v any) error {
	id, err := s.Resolve(id)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.runDir(id), "eval")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, name+".json"), v)
}

// LoadEval reads an evaluation result stored with a run.
func (s *Store) LoadEval(id, name string, v any) error {
	id, err := s.Resolve(id)
	if err != nil {
		return err
	}
	return readJSON(filepath.Join(s.runDir(id), "eval", name+".json"), v)
}

// Delete removes a run and everything stored with it.
func (s *Store) Delete(id string) error {
	id, err := s.Resolve(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(s.runDir(id))
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Write through a temporary file so a crash cannot leave a truncated record.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// SaveFeedback records human feedback about a walkthrough. Feedback is the
// closest available proxy for the product's real outcome — whether a reader
// actually got the mental model they needed — so it is stored with the run and
// not just counted.
func (s *Store) SaveFeedback(id string, v any) error {
	id, err := s.Resolve(id)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.runDir(id), "feedback.json"), v)
}

// Feedback reads stored feedback for a run.
func (s *Store) Feedback(id string, v any) error {
	id, err := s.Resolve(id)
	if err != nil {
		return err
	}
	return readJSON(filepath.Join(s.runDir(id), "feedback.json"), v)
}
