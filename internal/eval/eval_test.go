package eval_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rclod/codewalk/internal/eval"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/testutil"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// baseline builds a walkthrough whose references actually resolve against a
// fixture repository, so degradations can be measured against a clean start.
func baseline(t *testing.T) (*walkthrough.Walkthrough, *gitrepo.Repo) {
	t.Helper()
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	fixture.Write("internal/orders/worker.go", "package orders\n\n// Worker completes orders.\ntype Worker struct{}\n\nfunc (w *Worker) Run() {}\n")
	fixture.Commit("add worker")

	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	w := testutil.SampleWalkthrough()
	w.Scope.RepositoryName = repo.Name
	w.Scope.HeadCommit = ""
	w.Steps[0].CodeRefs = []walkthrough.CodeReference{{Path: "internal/orders/service.go", Symbol: "Create"}}
	w.Steps[2].CodeRefs = []walkthrough.CodeReference{{Path: "internal/orders/worker.go", Symbol: "Worker"}}
	w.StartHere = []walkthrough.CodeReference{{Path: "internal/orders/service.go", Note: "Start here."}}
	w.Components[0].Files = []string{"internal/orders/service.go"}
	w.Components[1].Files = []string{"internal/orders/worker.go"}
	w.Scope.ChangedFiles = []walkthrough.ChangedFile{
		{Path: "internal/orders/service.go", Status: "modified"},
		{Path: "internal/orders/worker.go", Status: "added"},
	}
	w.Normalize()
	return w, repo
}

func check(t *testing.T, w *walkthrough.Walkthrough, repo *gitrepo.Repo) *eval.Result {
	t.Helper()
	res, err := eval.Evaluate(context.Background(), eval.Options{Walkthrough: w, Repo: repo, Mode: eval.ModeSmoke})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCleanWalkthroughPassesDeterministicChecks(t *testing.T) {
	w, repo := baseline(t)
	res := check(t, w, repo)
	if !res.Passed() {
		t.Errorf("a clean walkthrough should pass its gates: %+v", res.Gates)
	}
	if res.Deterministic.RefsBroken != 0 {
		t.Errorf("broken references = %d: %v", res.Deterministic.RefsBroken, res.Deterministic.RefProblems)
	}
	if res.Deterministic.DiagramsChecked == 0 {
		t.Error("the fixture contains a diagram; it should have been checked")
	}
}

// TestDegradationsAreDetected is the "evaluate the evaluators" test: an
// evaluation system is only useful if introducing a known defect actually moves
// the dimensions it is supposed to move.
func TestDegradationsAreDetected(t *testing.T) {
	w, repo := baseline(t)
	clean := check(t, w, repo)

	deterministic := map[string][]eval.Dimension{
		"hallucination":  {eval.DimGrounding},
		"review_drift":   {eval.DimNeutrality},
		"broken_diagram": {eval.DimDiagrams},
	}
	for name, dims := range deterministic {
		t.Run(name, func(t *testing.T) {
			degraded, err := eval.Degrade(w, name)
			if err != nil {
				t.Fatal(err)
			}
			res := check(t, degraded, repo)
			for _, dim := range dims {
				before := clean.Dimensions[dim].Score
				after := res.Dimensions[dim].Score
				if after >= before {
					t.Errorf("%s: %s scored %.1f, expected a drop from %.1f", name, dim, float64(after), float64(before))
				}
			}
			if res.Passed() {
				t.Errorf("%s should fail a quality gate", name)
			}
		})
	}
}

func TestVerbosityIsVisibleInTheStructuralSignals(t *testing.T) {
	// Selectivity and concision need a judge to score, but the deterministic
	// layer must at least make the change measurable.
	w, repo := baseline(t)
	clean := check(t, w, repo)
	degraded, err := eval.Degrade(w, "verbosity")
	if err != nil {
		t.Fatal(err)
	}
	res := check(t, degraded, repo)
	if res.Deterministic.WordCount <= clean.Deterministic.WordCount {
		t.Error("added filler should increase the measured reading length")
	}
}

func TestMissingEssentialContentRemovesAStep(t *testing.T) {
	w, _ := baseline(t)
	degraded, err := eval.Degrade(w, "missing_essential")
	if err != nil {
		t.Fatal(err)
	}
	if len(degraded.Steps) >= len(w.Steps) {
		t.Error("the degradation should remove a step")
	}
	if len(degraded.Components) >= len(w.Components) {
		t.Error("the degradation should remove a component")
	}
	if issues := degraded.Validate(); walkthrough.HasErrors(issues) {
		// Degraded variants must stay structurally valid, otherwise a schema
		// failure would mask the quality signal being tested.
		t.Errorf("degraded walkthrough should remain structurally valid: %v", issues)
	}
}

func TestDegradationDoesNotMutateTheOriginal(t *testing.T) {
	w, _ := baseline(t)
	steps := len(w.Steps)
	if _, err := eval.Degrade(w, "missing_essential"); err != nil {
		t.Fatal(err)
	}
	if len(w.Steps) != steps {
		t.Error("degradations must operate on a copy")
	}
}

func TestReviewDriftDetection(t *testing.T) {
	w, _ := baseline(t)
	if drift := eval.DetectReviewDrift(w); len(drift) != 0 {
		t.Errorf("neutral prose flagged as review drift: %+v", drift)
	}
	// Descriptive statements about behaviour must not be mistaken for critique.
	w.Steps[0].Explanation = "The loop loads each record separately, so one database lookup happens per item. " +
		"Requests should arrive within the configured timeout, otherwise the worker retries."
	if drift := eval.DetectReviewDrift(w); len(drift) != 0 {
		t.Errorf("behavioural description flagged as review drift: %+v", drift)
	}

	w.Steps[0].Explanation = "This is a problem: the loop should be refactored, and I would recommend extracting a service."
	drift := eval.DetectReviewDrift(w)
	if len(drift) < 2 {
		t.Errorf("code-review language not detected: %+v", drift)
	}
}

func TestGatesBlockOnFabricationEvenWhenPolished(t *testing.T) {
	w, repo := baseline(t)
	degraded, err := eval.Degrade(w, "hallucination")
	if err != nil {
		t.Fatal(err)
	}
	res := check(t, degraded, repo)
	var refGate bool
	for _, g := range res.Gates {
		if g.Name == "references_resolve" && !g.Passed {
			refGate = true
		}
	}
	if !refGate {
		t.Errorf("a fabricated reference should fail the reference gate: %+v", res.Gates)
	}
}

func TestInapplicableDimensionsAreExcludedFromTheAverage(t *testing.T) {
	w, repo := baseline(t)
	// Remove every diagram: diagram utility becomes inapplicable rather than zero.
	w.Steps[1].Diagrams = nil
	w.Diagrams = nil
	res := check(t, w, repo)
	if res.Dimensions[eval.DimDiagrams].Applicable {
		t.Error("diagram utility should be inapplicable when there are no diagrams")
	}
	if res.Average() < 3 {
		t.Errorf("an inapplicable dimension must not drag the average down: %.2f", float64(res.Average()))
	}
}

func TestReportIsReadable(t *testing.T) {
	w, repo := baseline(t)
	res := check(t, w, repo)
	var out strings.Builder
	if err := eval.Report(&out, res); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Quality gates", "Dimensions", "Deterministic checks"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report is missing the %q section", want)
		}
	}
}

func TestTallyTracksWinsAndTies(t *testing.T) {
	tally := eval.NewTally()
	tally.Add(&eval.Comparison{Overall: "with-editor", Dimensions: map[eval.Dimension]string{eval.DimConcision: "with-editor"}})
	tally.Add(&eval.Comparison{Overall: "tie"})
	tally.Add(&eval.Comparison{Overall: "with-editor"})
	tally.Add(&eval.Comparison{Overall: "without-editor"})

	if tally.Total != 4 || tally.Ties != 1 {
		t.Errorf("tally = %+v", tally)
	}
	if got := tally.WinRate("with-editor"); got < 0.66 || got > 0.67 {
		t.Errorf("win rate = %.3f, want two of three decided comparisons", got)
	}
}
