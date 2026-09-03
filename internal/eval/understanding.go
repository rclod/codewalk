package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/tools"
)

// UnderstandingModel describes what a human needs to know about a change or a
// system. It is the benchmark's notion of truth.
//
// Benchmarking against a single canonical prose walkthrough would be wrong:
// many different explanations can be equally good. An understanding model
// instead states the understanding that must be conveyed, leaving the wording
// entirely free.
type UnderstandingModel struct {
	CaseID  string `json:"case_id,omitempty"`
	Purpose string `json:"purpose"`

	EssentialConcepts        []UnderstandingItem `json:"essential_concepts,omitempty"`
	EssentialBehaviorChanges []UnderstandingItem `json:"essential_behavior_changes,omitempty"`
	ImportantComponents      []UnderstandingItem `json:"important_components,omitempty"`
	ImportantRelationships   []UnderstandingItem `json:"important_relationships,omitempty"`
	ImportantFlows           []UnderstandingItem `json:"important_flows,omitempty"`

	BeforeState string `json:"before_state,omitempty"`
	AfterState  string `json:"after_state,omitempty"`

	SupportingContext []UnderstandingItem `json:"supporting_context,omitempty"`
	// Incidental lists changes a good walkthrough may reasonably ignore.
	// Spending significant attention on these counts against selectivity.
	Incidental []UnderstandingItem `json:"incidental,omitempty"`

	// Source is "curated" for a human-authored model or "extracted" for one
	// derived independently from the repository.
	Source string `json:"source,omitempty"`
}

// UnderstandingItem is one thing a reader must come away knowing.
type UnderstandingItem struct {
	// ID is a stable identifier such as "F1". Coverage is reported by ID so a
	// judge never has to match wording.
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Essential marks items whose absence is a coverage failure rather than a
	// missed opportunity.
	Essential bool `json:"essential,omitempty"`
}

// AllItems returns every item with a category label, which is what the coverage
// judge is asked about.
func (u *UnderstandingModel) AllItems() []LabelledItem {
	var out []LabelledItem
	add := func(category string, items []UnderstandingItem, essentialByDefault bool) {
		for _, it := range items {
			essential := it.Essential || essentialByDefault
			out = append(out, LabelledItem{Category: category, Item: it, Essential: essential})
		}
	}
	add("concept", u.EssentialConcepts, true)
	add("behaviour change", u.EssentialBehaviorChanges, true)
	add("component", u.ImportantComponents, true)
	add("relationship", u.ImportantRelationships, false)
	add("flow", u.ImportantFlows, false)
	add("supporting context", u.SupportingContext, false)
	return out
}

// LabelledItem is an understanding item with its category and weight.
type LabelledItem struct {
	Category  string
	Item      UnderstandingItem
	Essential bool
}

// LoadUnderstandingModel reads a curated understanding model from disk.
func LoadUnderstandingModel(path string) (*UnderstandingModel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var u UnderstandingModel
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("parse understanding model %s: %w", path, err)
	}
	if u.Source == "" {
		u.Source = "curated"
	}
	return &u, nil
}

// ExtractUnderstanding derives an understanding model directly from the
// repository, without seeing the walkthrough.
//
// This is what makes evaluation independent: the extractor and the walkthrough
// pipeline both look at the same repository, and coverage is measured by
// comparing them. A pipeline cannot pass by grading its own work.
func ExtractUnderstanding(ctx context.Context, backend agent.Backend, repo *tools.Repo, contextBrief string) (*UnderstandingModel, error) {
	prompt := contextBrief + "\n\nInvestigate the repository and produce the understanding model. Do not assume anything you have not verified in the code."

	var model UnderstandingModel
	task := agent.Task{
		Role:       "extractor",
		System:     extractorSystem,
		Prompt:     prompt,
		ExpectJSON: true,
		SchemaHint: understandingSchema,
		MaxSteps:   30,
	}
	res, err := backend.Execute(ctx, task, repo)
	if err != nil {
		return nil, err
	}
	if err := unmarshalJSON(res.Text, &model); err != nil {
		return nil, fmt.Errorf("understanding extractor returned unparseable output: %w", err)
	}
	model.Source = "extracted"
	return &model, nil
}
