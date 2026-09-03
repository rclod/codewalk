package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/jsonx"
	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/tools"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// Judge scores a walkthrough semantically, with repository access so its
// judgements are evidence-based rather than impressionistic.
type Judge struct {
	Backend agent.Backend
}

// JudgeInput is everything a judge sees.
type JudgeInput struct {
	Walkthrough *walkthrough.Walkthrough
	// Repo gives the judge independent repository access. It may be nil, in
	// which case the judge scores what it can from the walkthrough alone.
	Repo *tools.Repo
	// Understanding is the reference understanding model, curated or extracted.
	// Coverage is scored against it when present.
	Understanding *UnderstandingModel
}

// judgeResponse mirrors judgeSchema.
type judgeResponse map[string]struct {
	Score              *float64 `json:"score"`
	Applicable         *bool    `json:"applicable"`
	Reasoning          string   `json:"reasoning"`
	UnsupportedClaims  []string `json:"unsupported_claims"`
	Contradictions     []string `json:"contradictions"`
	CoveredConcepts    []string `json:"covered_concepts"`
	MissingConcepts    []string `json:"missing_concepts"`
	UnnecessaryContent []string `json:"unnecessary_content"`
	OrderProblems      []string `json:"order_problems"`
	Notes              []string `json:"notes"`
}

// Evaluate scores the walkthrough on the semantic dimensions.
func (j *Judge) Evaluate(ctx context.Context, in JudgeInput) (map[Dimension]DimensionResult, llm.Usage, error) {
	w := in.Walkthrough
	wJSON, _ := json.MarshalIndent(w, "", "  ")

	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Repository: %s\n", w.Scope.RepositoryName)
	if w.Kind == walkthrough.KindChange {
		fmt.Fprintf(&prompt, "The walkthrough explains a change: %s (%d files, +%d/-%d lines).\n",
			scopeLabel(w.Scope), w.Scope.Stats.FilesChanged, w.Scope.Stats.Insertions, w.Scope.Stats.Deletions)
		if len(w.Scope.ChangedFiles) > 0 {
			prompt.WriteString("Changed files:\n")
			for i, f := range w.Scope.ChangedFiles {
				if i >= 80 {
					fmt.Fprintf(&prompt, "  ... %d more ...\n", len(w.Scope.ChangedFiles)-i)
					break
				}
				fmt.Fprintf(&prompt, "  %s %s (+%d/-%d)%s\n", f.Status, f.Path, f.Insertions, f.Deletions, generatedNote(f))
			}
		}
	} else {
		prompt.WriteString("The walkthrough explains the repository's architecture and behaviour.\n")
	}
	fmt.Fprintf(&prompt, "The walkthrough declares complexity level %d (%s).\n", w.Complexity.Level, w.Complexity.Label)
	fmt.Fprintf(&prompt, "It contains %d steps and roughly %d words of prose at the default disclosure level.\n\n", len(w.Steps), w.WordCount())

	if in.Understanding != nil {
		uJSON, _ := json.MarshalIndent(in.Understanding, "", "  ")
		fmt.Fprintf(&prompt, "This reference understanding model states what a human needs to know (%s). Score essential coverage against it, reporting item ids. Wording does not need to match: judge whether the understanding is conveyed.\n\n<understanding_model>\n%s\n</understanding_model>\n\n",
			in.Understanding.Source, uJSON)
	}
	fmt.Fprintf(&prompt, "<walkthrough>\n%s\n</walkthrough>\n\nScore this walkthrough.", wJSON)
	if in.Repo != nil {
		prompt.WriteString(" Use the tools to verify important claims against the repository before scoring grounding or mental model accuracy.")
	}

	task := agent.Task{
		Role:       "judge",
		System:     judgeSystem,
		Prompt:     prompt.String(),
		ExpectJSON: true,
		SchemaHint: judgeSchema,
		MaxSteps:   20,
	}
	res, err := j.Backend.Execute(ctx, task, in.Repo)
	if err != nil {
		return nil, llm.Usage{}, err
	}
	var parsed judgeResponse
	if err := jsonx.Unmarshal(res.Text, &parsed); err != nil {
		return nil, res.Usage, fmt.Errorf("judge %s returned unparseable output: %w", j.Backend.Name(), err)
	}

	out := map[Dimension]DimensionResult{}
	for key, v := range parsed {
		dim := Dimension(key)
		if !knownDimension(dim) {
			continue
		}
		d := DimensionResult{
			Dimension:          dim,
			Method:             "model",
			Applicable:         true,
			Reasoning:          v.Reasoning,
			UnsupportedClaims:  v.UnsupportedClaims,
			Contradictions:     v.Contradictions,
			CoveredConcepts:    v.CoveredConcepts,
			MissingConcepts:    v.MissingConcepts,
			UnnecessaryContent: v.UnnecessaryContent,
			OrderProblems:      v.OrderProblems,
			Notes:              v.Notes,
		}
		if v.Applicable != nil {
			d.Applicable = *v.Applicable
		}
		if v.Score != nil {
			d.Score = Score(*v.Score)
		} else if d.Applicable {
			// A judge that omitted a score has not evaluated the dimension;
			// treating that as zero would silently punish the walkthrough.
			d.Applicable = false
			d.Reasoning = "judge returned no score for this dimension"
		}
		out[dim] = clampResult(d)
	}
	return out, res.Usage, nil
}

func generatedNote(f walkthrough.ChangedFile) string {
	if f.Generated {
		return " [likely generated]"
	}
	return ""
}

func scopeLabel(s walkthrough.Scope) string {
	switch s.Selector {
	case "working-tree":
		return "uncommitted changes"
	case "staged":
		return "staged changes"
	case "repository":
		return "whole repository"
	default:
		if s.Base != "" {
			return s.Base + ".." + s.Head
		}
		return s.Selector
	}
}

// unmarshalJSON is a package-local helper so eval code does not repeat the
// extraction dance.
func unmarshalJSON(text string, v any) error { return jsonx.Unmarshal(text, v) }
