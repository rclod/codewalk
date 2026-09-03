package eval

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/rclod/codewalk/internal/diagram"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/refcheck"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// DeterministicResult holds checks that need no model judgement. These are
// cheap enough to run on every walkthrough, and they catch the failures that
// damage trust fastest: invented files, dead line references, diagrams that do
// not render, and code-review language.
type DeterministicResult struct {
	SchemaValid  bool                `json:"schema_valid"`
	SchemaIssues []walkthrough.Issue `json:"schema_issues,omitempty"`

	// RefsVerified is false when no repository was available, in which case
	// references were counted but not resolved. Reporting them as valid would
	// be worse than reporting nothing.
	RefsVerified bool     `json:"refs_verified"`
	RefsChecked  int      `json:"refs_checked"`
	RefsBroken   int      `json:"refs_broken"`
	RefsWeak     int      `json:"refs_weak"`
	RefProblems  []string `json:"ref_problems,omitempty"`
	MissingFiles []string `json:"missing_files,omitempty"`

	DiagramsChecked int      `json:"diagrams_checked"`
	DiagramsInvalid int      `json:"diagrams_invalid"`
	DiagramProblems []string `json:"diagram_problems,omitempty"`

	// ChangedFileClaims verifies that files the walkthrough presents as changed
	// really are part of the change.
	UnchangedFilesClaimed []string `json:"unchanged_files_claimed,omitempty"`

	// ReviewDrift lists phrases that read as code review rather than
	// explanation. This is the product's core boundary, so it is checked
	// lexically as well as semantically.
	ReviewDrift []DriftMatch `json:"review_drift,omitempty"`

	// StepsWithoutRefs counts steps that never point at code.
	StepsWithoutRefs int `json:"steps_without_refs"`
	StepCount        int `json:"step_count"`
	WordCount        int `json:"word_count"`

	DanglingLinks []string `json:"dangling_links,omitempty"`
}

// DriftMatch is one phrase that reads as evaluation rather than explanation.
type DriftMatch struct {
	Where   string `json:"where"`
	Phrase  string `json:"phrase"`
	Excerpt string `json:"excerpt"`
}

// CheckDeterministic runs every check that does not require a model. repo may
// be nil, in which case reference existence is not verified.
func CheckDeterministic(ctx context.Context, w *walkthrough.Walkthrough, repo *gitrepo.Repo) DeterministicResult {
	res := DeterministicResult{
		SchemaValid: true,
		StepCount:   len(w.Steps),
		WordCount:   w.WordCount(),
	}

	res.SchemaIssues = w.Validate()
	if walkthrough.HasErrors(res.SchemaIssues) {
		res.SchemaValid = false
	}

	refs := w.AllCodeRefs()
	res.RefsChecked = len(refs)
	res.RefsVerified = repo != nil
	if repo != nil {
		checker := refcheck.New(repo, headRev(w), w.Scope.BaseCommit)
		for _, issue := range checker.CheckAll(ctx, refs) {
			switch issue.Severity {
			case refcheck.SeverityBroken:
				res.RefsBroken++
			default:
				res.RefsWeak++
			}
			res.RefProblems = append(res.RefProblems, issue.String())
		}
		var componentFiles []string
		for _, c := range w.Components {
			componentFiles = append(componentFiles, c.Files...)
		}
		res.MissingFiles = checker.CheckFiles(ctx, componentFiles)
	}

	for _, d := range w.AllDiagrams() {
		res.DiagramsChecked++
		issues := diagram.ValidateMermaid(d.Source)
		if diagram.Fatal(issues) {
			res.DiagramsInvalid++
		}
		for _, i := range issues {
			res.DiagramProblems = append(res.DiagramProblems, fmt.Sprintf("%s: %s", diagramLabel(d), i.String()))
		}
	}

	if w.Kind == walkthrough.KindChange && len(w.Scope.ChangedFiles) > 0 {
		changed := map[string]bool{}
		for _, f := range w.Scope.ChangedFiles {
			changed[f.Path] = true
			if f.OldPath != "" {
				changed[f.OldPath] = true
			}
		}
		for _, c := range w.Components {
			if c.Status != walkthrough.StatusNew && c.Status != walkthrough.StatusChanged {
				continue
			}
			for _, f := range c.Files {
				if !changed[f] {
					res.UnchangedFilesClaimed = append(res.UnchangedFilesClaimed, fmt.Sprintf("%s (component %q is marked %s)", f, c.Name, c.Status))
				}
			}
		}
	}

	for _, s := range w.Steps {
		if len(s.CodeRefs) == 0 && (s.DeepDive == nil || len(s.DeepDive.CodeRefs) == 0) {
			res.StepsWithoutRefs++
		}
	}
	res.ReviewDrift = DetectReviewDrift(w)
	res.DanglingLinks = danglingLinks(w)
	return res
}

func headRev(w *walkthrough.Walkthrough) string {
	if w.Scope.HeadCommit != "" {
		return w.Scope.HeadCommit
	}
	return gitrepo.WorkingTree
}

func diagramLabel(d walkthrough.Diagram) string {
	if d.Title != "" {
		return d.Title
	}
	return d.ID
}

// driftPatterns are phrases that indicate the walkthrough has drifted from
// explanation into evaluation. They are matched with surrounding context so
// that ordinary technical prose ("the request should arrive within 30s" as a
// description of behaviour) is less likely to be flagged; the semantic
// neutrality judge is the authority, and this check is the cheap first pass.
var driftPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(should|ought to) (be |have been )?(refactor|simplif|extract|renam|split|remov|replac|avoid|fix|improv)`),
	regexp.MustCompile(`(?i)\b(this|that|it) is (a |an )?(problem|issue|bug|anti-?pattern|code smell|vulnerability)`),
	regexp.MustCompile(`(?i)\b(i |we )?(would |'d )?recommend\b`),
	regexp.MustCompile(`(?i)\bconsider (refactoring|extracting|simplifying|adding a test|using)\b`),
	regexp.MustCompile(`(?i)\b(could|would) be (better|cleaner|simpler|more efficient)\b`),
	regexp.MustCompile(`(?i)\b(unnecessarily|needlessly) (complex|complicated|verbose)\b`),
	regexp.MustCompile(`(?i)\bmissing (a )?(test|tests|validation|error handling)\b`),
	regexp.MustCompile(`(?i)\b(security|performance) (issue|problem|concern|risk)\b`),
	regexp.MustCompile(`(?i)\bN\+1\b.*\b(problem|issue|should)\b`),
	regexp.MustCompile(`(?i)\bshould (be )?(optimi[sz]ed|cached|validated|sanitised|sanitized)\b`),
}

// DetectReviewDrift finds code-review language in walkthrough prose.
func DetectReviewDrift(w *walkthrough.Walkthrough) []DriftMatch {
	var matches []DriftMatch
	check := func(where, text string) {
		if text == "" {
			return
		}
		for _, re := range driftPatterns {
			for _, loc := range re.FindAllStringIndex(text, -1) {
				matches = append(matches, DriftMatch{
					Where:   where,
					Phrase:  text[loc[0]:loc[1]],
					Excerpt: excerpt(text, loc[0], loc[1]),
				})
			}
		}
	}
	check("headline", w.Headline)
	check("summary", w.Summary)
	for i, s := range w.Steps {
		where := fmt.Sprintf("steps[%d] %q", i, s.Title)
		check(where, s.Summary)
		check(where, s.Explanation)
		if s.DeepDive != nil {
			check(where+" deep dive", s.DeepDive.Explanation)
		}
	}
	if w.Architecture != nil {
		check("architecture", w.Architecture.Overview)
	}
	for _, c := range w.Concepts {
		check("concept "+c.Name, c.Summary+" "+c.WhyItMatters)
	}
	return matches
}

func excerpt(text string, start, end int) string {
	const pad = 60
	from := start - pad
	if from < 0 {
		from = 0
	}
	to := end + pad
	if to > len(text) {
		to = len(text)
	}
	return strings.TrimSpace(strings.ReplaceAll(text[from:to], "\n", " "))
}

// danglingLinks finds internal references in prose that do not resolve, for
// example "see step 7" in a five-step walkthrough.
var stepReference = regexp.MustCompile(`(?i)\bstep (\d+)\b`)

func danglingLinks(w *walkthrough.Walkthrough) []string {
	var out []string
	total := len(w.Steps)
	for i, s := range w.Steps {
		for _, m := range stepReference.FindAllStringSubmatch(s.Explanation, -1) {
			var n int
			if _, err := fmt.Sscanf(m[1], "%d", &n); err == nil && (n < 1 || n > total) {
				out = append(out, fmt.Sprintf("steps[%d] refers to %q but the walkthrough has %d steps", i, m[0], total))
			}
		}
	}
	return out
}

// scoreDeterministic converts deterministic findings into the dimension scores
// they can support on their own. Semantic judging refines these; without a
// judge they are still meaningful.
func scoreDeterministic(res DeterministicResult, w *walkthrough.Walkthrough) map[Dimension]DimensionResult {
	out := map[Dimension]DimensionResult{}

	// Grounding: broken references are objective evidence of ungrounded claims.
	grounding := DimensionResult{Dimension: DimGrounding, Method: "deterministic", Applicable: true, Score: 5}
	switch {
	case !res.RefsVerified:
		// Without a repository the deterministic layer has nothing to say about
		// grounding, so it declines to score rather than assuming the best.
		grounding.Applicable = false
		grounding.Reasoning = "references were not verified: no repository was available"
	case res.RefsChecked > 0:
		brokenRatio := float64(res.RefsBroken) / float64(res.RefsChecked)
		switch {
		case res.RefsBroken == 0:
			grounding.Reasoning = fmt.Sprintf("all %d code references resolve", res.RefsChecked)
		case brokenRatio < 0.1:
			grounding.Score = 3.5
		case brokenRatio < 0.25:
			grounding.Score = 2.5
		default:
			grounding.Score = 1
		}
		if res.RefsBroken > 0 {
			grounding.Reasoning = fmt.Sprintf("%d of %d code references do not resolve", res.RefsBroken, res.RefsChecked)
			grounding.UnsupportedClaims = res.RefProblems
		}
	default:
		grounding.Score = 2.5
		grounding.Reasoning = "the walkthrough contains no code references to verify"
	}
	if len(res.MissingFiles) > 0 {
		grounding.Score -= 1
		grounding.Notes = append(grounding.Notes, fmt.Sprintf("component file lists name %d path(s) that do not exist", len(res.MissingFiles)))
	}
	out[DimGrounding] = clampResult(grounding)

	// Navigability: can a reader get from explanation to code?
	nav := DimensionResult{Dimension: DimNavigability, Method: "deterministic", Applicable: true, Score: 5}
	if res.StepCount > 0 {
		withoutRefs := float64(res.StepsWithoutRefs) / float64(res.StepCount)
		switch {
		case withoutRefs > 0.6:
			nav.Score = 2
		case withoutRefs > 0.3:
			nav.Score = 3.5
		}
		nav.Reasoning = fmt.Sprintf("%d of %d steps carry no code reference", res.StepsWithoutRefs, res.StepCount)
	}
	if len(w.StartHere) == 0 {
		nav.Score -= 0.5
		nav.Notes = append(nav.Notes, "no starting point is suggested")
	}
	if res.RefsBroken > 0 {
		nav.Score -= 1
	}
	if len(res.DanglingLinks) > 0 {
		nav.Score -= 0.5
		nav.Notes = append(nav.Notes, res.DanglingLinks...)
	}
	out[DimNavigability] = clampResult(nav)

	// Neutrality: explanation, not evaluation.
	neutrality := DimensionResult{Dimension: DimNeutrality, Method: "deterministic", Applicable: true, Score: 5}
	switch n := len(res.ReviewDrift); {
	case n == 0:
		neutrality.Reasoning = "no code-review language detected"
	case n <= 2:
		neutrality.Score = 3
	case n <= 5:
		neutrality.Score = 2
	default:
		neutrality.Score = 1
	}
	for _, d := range res.ReviewDrift {
		neutrality.Notes = append(neutrality.Notes, fmt.Sprintf("%s: %q", d.Where, d.Phrase))
	}
	if len(res.ReviewDrift) > 0 {
		neutrality.Reasoning = fmt.Sprintf("%d phrase(s) read as code review rather than explanation", len(res.ReviewDrift))
	}
	out[DimNeutrality] = clampResult(neutrality)

	// Diagram utility: a diagram that does not render cannot reduce effort.
	diagrams := DimensionResult{Dimension: DimDiagrams, Method: "deterministic", Applicable: res.DiagramsChecked > 0}
	if diagrams.Applicable {
		diagrams.Score = 5
		if res.DiagramsInvalid > 0 {
			diagrams.Score = 1
			diagrams.Reasoning = fmt.Sprintf("%d of %d diagrams do not parse", res.DiagramsInvalid, res.DiagramsChecked)
			diagrams.Notes = res.DiagramProblems
		} else {
			diagrams.Reasoning = fmt.Sprintf("all %d diagrams parse", res.DiagramsChecked)
		}
	} else {
		diagrams.Reasoning = "no diagrams to assess"
	}
	out[DimDiagrams] = clampResult(diagrams)

	return out
}

func clampResult(d DimensionResult) DimensionResult {
	if d.Score < 0 {
		d.Score = 0
	}
	if d.Score > 5 {
		d.Score = 5
	}
	return d
}
