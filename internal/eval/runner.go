package eval

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/tools"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// Options configures an evaluation.
type Options struct {
	Walkthrough *walkthrough.Walkthrough
	// Repo enables deterministic reference checking and gives judges
	// independent repository access. It may be nil for schema-only checks.
	Repo *gitrepo.Repo
	// Change scopes repository tools to the change being explained.
	Change *gitrepo.ChangeSet

	Mode Mode
	// Judges are the semantic evaluators. Using more than one model family
	// reduces single-model bias; disagreements are recorded rather than hidden.
	Judges []agent.Backend
	// Understanding is the reference model for coverage. When it is nil and
	// Extractor is set, one is derived independently from the repository.
	Understanding *UnderstandingModel
	Extractor     agent.Backend

	RunID  string
	CaseID string

	// Generation metrics carried through so quality, cost and latency appear in
	// one report.
	GenerationDurationMS int64
	GenerationTokens     int
	GenerationModelCalls int
}

// Evaluate scores a walkthrough.
func Evaluate(ctx context.Context, opts Options) (*Result, error) {
	if opts.Walkthrough == nil {
		return nil, fmt.Errorf("evaluate: no walkthrough")
	}
	started := time.Now()
	mode := opts.Mode
	if mode == "" {
		mode = ModeSmoke
	}

	result := &Result{
		WalkthroughID:        opts.Walkthrough.ID,
		RunID:                opts.RunID,
		CaseID:               opts.CaseID,
		EvaluatedAt:          time.Now().UTC(),
		Mode:                 string(mode),
		Dimensions:           map[Dimension]DimensionResult{},
		GenerationDurationMS: opts.GenerationDurationMS,
		GenerationTokens:     opts.GenerationTokens,
		GenerationModelCalls: opts.GenerationModelCalls,
	}

	result.Deterministic = CheckDeterministic(ctx, opts.Walkthrough, opts.Repo)
	for dim, d := range scoreDeterministic(result.Deterministic, opts.Walkthrough) {
		result.Dimensions[dim] = d
	}

	if mode == ModeSmoke || len(opts.Judges) == 0 {
		result.Gates = computeGates(result)
		result.EvaluationDurationMS = time.Since(started).Milliseconds()
		return result, nil
	}

	var repoTools *tools.Repo
	if opts.Repo != nil {
		repoTools = tools.NewRepo(opts.Repo, opts.Change, nil, tools.Options{AllowGitHistory: true})
	}

	var usage llm.Usage
	understanding := opts.Understanding
	if understanding == nil && opts.Extractor != nil && repoTools != nil {
		brief := understandingBrief(opts.Walkthrough)
		extracted, err := ExtractUnderstanding(ctx, opts.Extractor, repoTools, brief)
		if err == nil {
			understanding = extracted
		} else {
			result.Disagreements = append(result.Disagreements,
				fmt.Sprintf("independent understanding extraction failed: %v", err))
		}
	}

	judges := opts.Judges
	if mode == ModeStandard && len(judges) > 1 {
		// Standard mode uses one judge; multiple judges are a full-mode cost.
		judges = judges[:1]
	}

	perJudge := map[Dimension][]DimensionResult{}
	for _, backend := range judges {
		j := &Judge{Backend: backend}
		scores, u, err := j.Evaluate(ctx, JudgeInput{
			Walkthrough:   opts.Walkthrough,
			Repo:          repoTools,
			Understanding: understanding,
		})
		usage.Add(u)
		if err != nil {
			result.Disagreements = append(result.Disagreements, fmt.Sprintf("judge %s failed: %v", backend.Name(), err))
			continue
		}
		result.Judges = append(result.Judges, backend.Descriptor())
		for dim, d := range scores {
			perJudge[dim] = append(perJudge[dim], d)
		}
	}

	for dim, results := range perJudge {
		merged, disagreement := mergeJudgeResults(dim, results)
		if disagreement != "" {
			result.Disagreements = append(result.Disagreements, disagreement)
		}
		result.Dimensions[dim] = combineWithDeterministic(dim, result.Dimensions[dim], merged)
	}

	result.Gates = computeGates(result)
	result.EvaluationDurationMS = time.Since(started).Milliseconds()
	result.EvaluationTokens = usage.InputTokens + usage.OutputTokens
	return result, nil
}

// mergeJudgeResults averages several judges and reports material disagreement.
// Disagreement is surfaced rather than smoothed away: two judges a point and a
// half apart is a signal about the walkthrough or about the judges, and either
// one is worth knowing.
func mergeJudgeResults(dim Dimension, results []DimensionResult) (DimensionResult, string) {
	if len(results) == 1 {
		return results[0], ""
	}
	merged := results[0]
	var sum Score
	var n int
	minScore, maxScore := Score(5), Score(0)
	for _, r := range results {
		if !r.Applicable {
			continue
		}
		sum += r.Score
		n++
		minScore = minScoreOf(minScore, r.Score)
		maxScore = maxScoreOf(maxScore, r.Score)
		merged.UnsupportedClaims = appendUnique(merged.UnsupportedClaims, r.UnsupportedClaims)
		merged.Contradictions = appendUnique(merged.Contradictions, r.Contradictions)
		merged.MissingConcepts = appendUnique(merged.MissingConcepts, r.MissingConcepts)
		merged.CoveredConcepts = appendUnique(merged.CoveredConcepts, r.CoveredConcepts)
		merged.UnnecessaryContent = appendUnique(merged.UnnecessaryContent, r.UnnecessaryContent)
		merged.OrderProblems = appendUnique(merged.OrderProblems, r.OrderProblems)
		merged.Notes = appendUnique(merged.Notes, r.Notes)
	}
	if n == 0 {
		merged.Applicable = false
		return merged, ""
	}
	merged.Score = Score(math.Round(float64(sum/Score(n))*2) / 2)
	merged.Method = "model"
	disagreement := ""
	if maxScore-minScore >= 1.5 {
		disagreement = fmt.Sprintf("judges disagreed on %s: scores ranged %.1f to %.1f", dim, float64(minScore), float64(maxScore))
	}
	return merged, disagreement
}

// combineWithDeterministic decides how a deterministic score and a model score
// interact. For dimensions where tooling produces objective evidence of a
// failure (a reference that does not resolve, a diagram that does not parse,
// review language in the prose), the worse score wins: a judge's good opinion
// does not make a broken reference work.
func combineWithDeterministic(dim Dimension, det, model DimensionResult) DimensionResult {
	if det.Dimension == "" || !det.Applicable {
		return model
	}
	if !model.Applicable {
		return det
	}
	switch dim {
	case DimGrounding, DimNeutrality, DimDiagrams:
		out := model
		out.Method = "combined"
		if det.Score < model.Score {
			out.Score = det.Score
		}
		out.Notes = appendUnique(out.Notes, det.Notes)
		out.UnsupportedClaims = appendUnique(out.UnsupportedClaims, det.UnsupportedClaims)
		if det.Reasoning != "" {
			out.Reasoning = model.Reasoning + " Deterministic checks: " + det.Reasoning
		}
		return out
	default:
		return model
	}
}

func understandingBrief(w *walkthrough.Walkthrough) string {
	var b []byte
	b = append(b, "Repository: "...)
	b = append(b, w.Scope.RepositoryName...)
	b = append(b, '\n')
	if w.Kind == walkthrough.KindChange {
		b = append(b, "Determine what a human needs to understand about this change: "...)
		b = append(b, scopeLabel(w.Scope)...)
		b = append(b, ".\nUse the change tools to see exactly what changed.\n"...)
	} else {
		b = append(b, "Determine what a human needs to understand about this repository's architecture and important behaviour.\n"...)
	}
	return string(b)
}

func appendUnique(dst []string, src []string) []string {
	seen := map[string]bool{}
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range src {
		if s != "" && !seen[s] {
			dst = append(dst, s)
			seen[s] = true
		}
	}
	return dst
}

func minScoreOf(a, b Score) Score {
	if a < b {
		return a
	}
	return b
}

func maxScoreOf(a, b Score) Score {
	if a > b {
		return a
	}
	return b
}
