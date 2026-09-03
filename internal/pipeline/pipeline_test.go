package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/pipeline"
	"github.com/rclod/codewalk/internal/testutil"
	"github.com/rclod/codewalk/internal/tools"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// fakeBackend answers each pipeline stage with canned JSON. It lets the whole
// pipeline be exercised end to end without a model, which is where the
// structural behaviour (stage sequencing, artifact capture, scope filling,
// reference pruning) actually lives.
type fakeBackend struct {
	responses map[string]string
	calls     []string
	prompts   map[string]string
	// failFirst makes a role return unparseable output once, to exercise the
	// JSON repair path.
	failFirst map[string]bool
}

func (f *fakeBackend) Name() string       { return "fake" }
func (f *fakeBackend) Kind() string       { return "provider" }
func (f *fakeBackend) Descriptor() string { return "fake:test-model" }
func (f *fakeBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{Inference: true, RepositoryTools: true, StructuredOutput: true}
}

func (f *fakeBackend) Execute(_ context.Context, task agent.Task, _ *tools.Repo) (*agent.Result, error) {
	f.calls = append(f.calls, task.Role)
	if f.prompts == nil {
		f.prompts = map[string]string{}
	}
	f.prompts[task.Role] = task.Prompt

	if f.failFirst[task.Role] {
		f.failFirst[task.Role] = false
		return &agent.Result{Text: "Sure! Here is the analysis, but as prose.", Backend: "fake", Model: "test-model"}, nil
	}
	body, ok := f.responses[task.Role]
	if !ok {
		body = "{}"
	}
	return &agent.Result{Text: body, Backend: "fake", Model: "test-model", Steps: 1}, nil
}

func newFake(level int) *fakeBackend {
	return &fakeBackend{
		failFirst: map[string]bool{},
		responses: map[string]string{
			"investigator": `{
				"purpose": "Move order completion into a background worker.",
				"purpose_confidence": "evidenced",
				"findings": [{"statement": "Create no longer authorises payment inline.",
					"evidence": [{"path": "internal/orders/service.go", "start_line": 1, "end_line": 4}],
					"importance": "essential"}],
				"complexity": {"level": ` + itoa(level) + `, "rationale": "A synchronous path becomes asynchronous."}
			}`,
			"mental_model": `{
				"purpose": "Order completion becomes asynchronous.",
				"headline": "Order creation now finishes in a background worker.",
				"complexity": {"level": ` + itoa(level) + `, "rationale": "Crosses an execution boundary."},
				"components": [{"id": "worker", "name": "OrderWorker", "kind": "worker", "responsibility": "Completes orders.", "status": "new"}],
				"must_understand": ["A successful response no longer means the order is complete."],
				"start_here": [{"path": "internal/orders/service.go", "note": "Entry point."}]
			}`,
			"planner": `{
				"complexity_level": ` + itoa(level) + `,
				"target_shape": "Two steps: the old behaviour, then the new one.",
				"steps": [{"title": "How it worked before", "kind": "before"}, {"title": "The worker", "kind": "component"}],
				"order_rationale": "Establish the previous behaviour first."
			}`,
			"author": `{
				"title": "Order completion moves to a worker",
				"headline": "Order creation now finishes in a background worker.",
				"summary": "The handler stores the order and returns; a worker completes it.",
				"components": [{"id": "worker", "name": "OrderWorker", "responsibility": "Completes orders.", "status": "new"}],
				"steps": [
					{"id": "before", "title": "How it worked before", "kind": "before",
					 "summary": "Everything happened in the request.", "explanation": "Create did the work inline.",
					 "code_refs": [{"path": "internal/orders/service.go", "symbol": "Service.Create"}]},
					{"id": "after", "title": "The worker", "kind": "component",
					 "summary": "A worker finishes the job.", "explanation": "The worker completes the order.",
					 "components": ["worker"],
					 "code_refs": [
						{"path": "internal/orders/service.go", "start_line": 1, "end_line": 3},
						{"path": "internal/cache/does_not_exist.go", "symbol": "Phantom"}
					 ]}
				],
				"uncertainties": []
			}`,
			"editor": `{
				"walkthrough": {
					"title": "Order completion moves to a worker",
					"headline": "Order creation now finishes in a background worker.",
					"summary": "The handler stores the order and returns; a worker completes it.",
					"components": [{"id": "worker", "name": "OrderWorker", "responsibility": "Completes orders.", "status": "new"}],
					"steps": [
						{"id": "before", "title": "How it worked before", "kind": "before",
						 "summary": "Everything happened in the request.", "explanation": "Create did the work inline.",
						 "code_refs": [{"path": "internal/orders/service.go", "symbol": "Service.Create"}]},
						{"id": "after", "title": "The worker", "kind": "component",
						 "summary": "A worker finishes the job.", "explanation": "The worker completes the order.",
						 "components": ["worker"],
						 "code_refs": [
							{"path": "internal/orders/service.go", "start_line": 1, "end_line": 3},
							{"path": "internal/cache/does_not_exist.go", "symbol": "Phantom"}
						 ]}
					]
				},
				"edits": ["Tightened the summary."]
			}`,
			"grounding": `{
				"verdict": "minor_issues",
				"unsupported": [{"step_id": "after", "claim": "The worker retries failures.",
					"why": "No retry logic is present in the repository.", "suggestion": "State that retry behaviour is not visible here."}]
			}`,
		},
	}
}

func itoa(n int) string { return string(rune('0' + n)) }

func setup(t *testing.T) (pipeline.Options, *fakeBackend) {
	t.Helper()
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	fixture.Branch("feature")
	fixture.Write("internal/orders/worker.go", "package orders\n\n// Worker completes orders.\ntype Worker struct{}\n")
	fixture.Commit("add worker")

	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	change, err := repo.BuildChangeSet(context.Background(), gitrepo.Selection{Mode: gitrepo.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}

	backend := newFake(4)
	registry := agent.NewRegistry()
	registry.Register("fake", backend)
	if err := registry.SetDefault("fake"); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Defaults.Backend = "fake"
	return pipeline.Options{
		Repo:     repo,
		Change:   change,
		Kind:     walkthrough.KindChange,
		Config:   cfg,
		Registry: registry,
		RunID:    "test-run",
		Version:  "test",
	}, backend
}

func TestPipelineProducesAGroundedWalkthrough(t *testing.T) {
	opts, backend := setup(t)
	res, err := pipeline.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	w := res.Walkthrough

	if got, want := backend.calls, []string{"investigator", "mental_model", "planner", "author", "editor", "grounding"}; !equal(got, want) {
		t.Errorf("stage order = %v, want %v", got, want)
	}
	if w.Scope.RepositoryName == "" || w.Scope.Base == "" || len(w.Scope.ChangedFiles) == 0 {
		t.Errorf("the pipeline should fill scope from the change, not the model: %+v", w.Scope)
	}
	if w.Meta.RunID != "test-run" || w.Meta.Stages["author"] != "fake:test-model" {
		t.Errorf("run provenance missing: %+v", w.Meta)
	}
	if w.Complexity.Level != 4 {
		t.Errorf("complexity level = %d, want the mental model's assessment", w.Complexity.Level)
	}

	// A reference the repository cannot resolve must never reach the reader.
	for _, ref := range w.AllCodeRefs() {
		if strings.Contains(ref.Path, "does_not_exist") {
			t.Errorf("an unresolvable reference survived grounding: %+v", ref)
		}
	}
	// Claims the grounding check could not support become stated uncertainty.
	if len(w.Uncertainties) == 0 || !strings.Contains(w.Uncertainties[0].Question, "retries") {
		t.Errorf("unsupported claims should become explicit uncertainty: %+v", w.Uncertainties)
	}

	for _, name := range []string{"evidence", "mental_model", "plan", "authored_walkthrough", "editor", "grounding"} {
		if _, ok := res.Artifacts[name]; !ok {
			t.Errorf("artifact %q was not captured; runs must be explainable stage by stage", name)
		}
	}
	if issues := w.Validate(); walkthrough.HasErrors(issues) {
		t.Errorf("pipeline produced a structurally invalid walkthrough: %v", issues)
	}
}

func TestTrivialChangesSkipThePlanner(t *testing.T) {
	opts, backend := setup(t)
	*backend = *newFake(1)
	res, err := pipeline.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range backend.calls {
		if call == "planner" {
			t.Error("a trivial change should not pay for a planning stage")
		}
	}
	var skipped bool
	for _, stage := range res.Stages {
		if stage.Name == "planner" && stage.Skipped != "" {
			skipped = true
		}
	}
	if !skipped {
		t.Error("a skipped stage should still be recorded, with its reason")
	}
}

func TestStagesCanBeDisabled(t *testing.T) {
	opts, backend := setup(t)
	opts.Config.Analysis.Editor = false
	opts.Config.Analysis.Grounding = false

	if _, err := pipeline.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, call := range backend.calls {
		if call == "editor" || call == "grounding" {
			t.Errorf("%s ran despite being disabled", call)
		}
	}
}

func TestUnparseableStageOutputIsRepairedOnce(t *testing.T) {
	opts, backend := setup(t)
	backend.failFirst["mental_model"] = true

	res, err := pipeline.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("a single formatting slip should not fail a run: %v", err)
	}
	var repairs int
	for _, stage := range res.Stages {
		if stage.Name == "mental_model" {
			repairs = stage.Repairs
		}
	}
	if repairs != 1 {
		t.Errorf("repairs = %d, want the retry to be recorded so its cost is visible", repairs)
	}
	if !strings.Contains(backend.prompts["mental_model"], "could not be parsed") {
		t.Error("the repair prompt should tell the model what went wrong")
	}
}

func TestEditorFailureKeepsTheAuthoredWalkthrough(t *testing.T) {
	opts, backend := setup(t)
	backend.responses["editor"] = `{"walkthrough": {"steps": []}, "edits": []}`

	res, err := pipeline.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Walkthrough.Steps) == 0 {
		t.Error("an editor that returns nothing must not discard the authored walkthrough")
	}
}

func TestChangeContextIsGivenToTheInvestigator(t *testing.T) {
	opts, backend := setup(t)
	if _, err := pipeline.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	prompt := backend.prompts["investigator"]
	for _, want := range []string{"internal/orders/worker.go", "<diff>", "<repository_map>"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("investigator prompt is missing %q", want)
		}
	}
}

func TestCodebaseModeNeedsNoChange(t *testing.T) {
	opts, _ := setup(t)
	opts.Kind = walkthrough.KindCodebase
	opts.Change = nil

	res, err := pipeline.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("codebase walkthrough failed: %v", err)
	}
	if res.Walkthrough.Scope.Selector != "repository" {
		t.Errorf("scope selector = %q, want repository", res.Walkthrough.Scope.Selector)
	}
}

func TestMissingChangeIsRejected(t *testing.T) {
	opts, _ := setup(t)
	opts.Change = nil
	if _, err := pipeline.Run(context.Background(), opts); err == nil {
		t.Error("a change walkthrough without a change set should fail fast")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
