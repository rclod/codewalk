package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/tools"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// Candidate is one walkthrough entering a comparison, together with the
// configuration that produced it. The label is what an experiment is actually
// comparing: a model, a prompt version, a harness, a pipeline shape.
type Candidate struct {
	Label       string                   `json:"label"`
	RunID       string                   `json:"run_id,omitempty"`
	Walkthrough *walkthrough.Walkthrough `json:"-"`
	// Config describes what varied, for example {"backend": "openai", "editor": "off"}.
	Config map[string]string `json:"config,omitempty"`
}

// Comparison is the outcome of a blind pairwise evaluation.
type Comparison struct {
	CaseID string `json:"case_id,omitempty"`
	// A and B are the candidate labels, in presentation order.
	A string `json:"a"`
	B string `json:"b"`
	// Dimensions maps a dimension to the winning label, or "tie".
	Dimensions      map[Dimension]string `json:"dimensions"`
	Overall         string               `json:"overall"`
	Reasons         map[Dimension]string `json:"reasons,omitempty"`
	OverallReason   string               `json:"overall_reason,omitempty"`
	DecisiveFactors []string             `json:"decisive_factors,omitempty"`
	Judge           string               `json:"judge,omitempty"`
	// Swapped records that candidate A was presented second, which is how
	// position bias is controlled.
	Swapped bool      `json:"swapped"`
	Usage   llm.Usage `json:"usage,omitempty"`
}

// ComparePair runs one blind comparison.
//
// Blinding is real: the judge sees "Candidate A" and "Candidate B" with no
// provider, model or configuration information, and the presentation order is
// randomised so a judge that favours the first candidate cannot systematically
// favour one arm of an experiment.
func ComparePair(ctx context.Context, backend agent.Backend, repo *tools.Repo, a, b Candidate, rng *rand.Rand) (*Comparison, error) {
	if a.Walkthrough == nil || b.Walkthrough == nil {
		return nil, fmt.Errorf("pairwise comparison needs two walkthroughs")
	}
	swapped := rng != nil && rng.Intn(2) == 1
	first, second := a, b
	if swapped {
		first, second = b, a
	}

	firstJSON, _ := json.MarshalIndent(blind(first.Walkthrough), "", "  ")
	secondJSON, _ := json.MarshalIndent(blind(second.Walkthrough), "", "  ")

	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Both candidates explain the same subject: %s in repository %s.\n\n",
		scopeLabel(a.Walkthrough.Scope), a.Walkthrough.Scope.RepositoryName)
	fmt.Fprintf(&prompt, "<candidate_a>\n%s\n</candidate_a>\n\n<candidate_b>\n%s\n</candidate_b>\n\n", firstJSON, secondJSON)
	prompt.WriteString("Compare them.")
	if repo != nil {
		prompt.WriteString(" Use the tools to check the repository where a difference in accuracy is what separates them.")
	}

	task := agent.Task{
		Role:       "judge",
		System:     pairwiseSystem,
		Prompt:     prompt.String(),
		ExpectJSON: true,
		SchemaHint: pairwiseSchema,
		MaxSteps:   15,
	}
	res, err := backend.Execute(ctx, task, repo)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Dimensions []struct {
			Dimension string `json:"dimension"`
			Winner    string `json:"winner"`
			Reason    string `json:"reason"`
		} `json:"dimensions"`
		Overall struct {
			Winner string `json:"winner"`
			Reason string `json:"reason"`
		} `json:"overall"`
		DecisiveFactors []string `json:"decisive_factors"`
	}
	if err := unmarshalJSON(res.Text, &parsed); err != nil {
		return nil, fmt.Errorf("pairwise judge returned unparseable output: %w", err)
	}

	// Translate the judge's positional verdicts back to candidate labels.
	resolve := func(winner string) string {
		switch strings.ToUpper(strings.TrimSpace(winner)) {
		case "A":
			return first.Label
		case "B":
			return second.Label
		default:
			return "tie"
		}
	}

	cmp := &Comparison{
		A:               a.Label,
		B:               b.Label,
		Dimensions:      map[Dimension]string{},
		Reasons:         map[Dimension]string{},
		Overall:         resolve(parsed.Overall.Winner),
		OverallReason:   parsed.Overall.Reason,
		DecisiveFactors: parsed.DecisiveFactors,
		Judge:           backend.Descriptor(),
		Swapped:         swapped,
		Usage:           res.Usage,
	}
	for _, d := range parsed.Dimensions {
		dim := Dimension(d.Dimension)
		if !knownDimension(dim) {
			continue
		}
		cmp.Dimensions[dim] = resolve(d.Winner)
		if d.Reason != "" {
			cmp.Reasons[dim] = d.Reason
		}
	}
	return cmp, nil
}

// blind strips metadata that would reveal which system produced a candidate.
func blind(w *walkthrough.Walkthrough) *walkthrough.Walkthrough {
	c := *w
	c.Meta = walkthrough.Meta{}
	c.ID = ""
	c.Evidence = nil
	c.Scope.RepositoryPath = ""
	return &c
}

// Tally accumulates comparison outcomes across a corpus, which is what turns
// individual verdicts into a usable experiment result.
type Tally struct {
	Wins  map[string]int               `json:"wins"`
	Ties  int                          `json:"ties"`
	Total int                          `json:"total"`
	ByDim map[Dimension]map[string]int `json:"by_dimension"`
}

// NewTally creates an empty tally.
func NewTally() *Tally {
	return &Tally{Wins: map[string]int{}, ByDim: map[Dimension]map[string]int{}}
}

// Add records one comparison.
func (t *Tally) Add(c *Comparison) {
	t.Total++
	if c.Overall == "tie" {
		t.Ties++
	} else {
		t.Wins[c.Overall]++
	}
	for dim, winner := range c.Dimensions {
		if t.ByDim[dim] == nil {
			t.ByDim[dim] = map[string]int{}
		}
		t.ByDim[dim][winner]++
	}
}

// WinRate returns a label's share of decided comparisons.
func (t *Tally) WinRate(label string) float64 {
	decided := t.Total - t.Ties
	if decided == 0 {
		return 0
	}
	return float64(t.Wins[label]) / float64(decided)
}
