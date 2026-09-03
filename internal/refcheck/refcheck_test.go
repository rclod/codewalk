package refcheck_test

import (
	"context"
	"testing"

	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/refcheck"
	"github.com/rclod/codewalk/internal/testutil"
	"github.com/rclod/codewalk/internal/walkthrough"
)

func newChecker(t *testing.T) (*refcheck.Checker, string) {
	t.Helper()
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	base := fixture.Commit("baseline")
	fixture.Write("internal/orders/service.go", `package orders

// Service coordinates order creation.
type Service struct{}

// Create enqueues the order for background processing.
func (s *Service) Create(customer string, total int) (string, error) {
	return "", nil
}
`)
	fixture.Commit("move creation to a worker")

	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return refcheck.New(repo, "HEAD", base), fixture.Dir
}

func TestValidReferencesPass(t *testing.T) {
	checker, _ := newChecker(t)
	refs := []walkthrough.CodeReference{
		{Path: "internal/orders/service.go"},
		{Path: "internal/orders/service.go", Symbol: "Service.Create"},
		{Path: "internal/orders/service.go", Symbol: "Create", StartLine: 6, EndLine: 9},
	}
	if issues := checker.CheckAll(context.Background(), refs); len(issues) > 0 {
		t.Errorf("valid references rejected: %v", issues)
	}
}

func TestBrokenReferencesAreDetected(t *testing.T) {
	checker, _ := newChecker(t)
	cases := map[string]walkthrough.CodeReference{
		"missing file":   {Path: "internal/cache/session_cache.go"},
		"missing symbol": {Path: "internal/orders/service.go", Symbol: "SessionCache.Get"},
		"line past end":  {Path: "internal/orders/service.go", StartLine: 5000},
		"no path":        {Symbol: "Something"},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			issue := checker.Check(context.Background(), ref)
			if issue == nil || issue.Severity != refcheck.SeverityBroken {
				t.Errorf("expected a broken reference, got %v", issue)
			}
		})
	}
}

func TestReferenceToThePreviousStateResolvesAgainstTheBase(t *testing.T) {
	checker, _ := newChecker(t)
	ctx := context.Background()
	// The old implementation returned via the store; that text only exists in
	// the base revision.
	before := walkthrough.CodeReference{Path: "internal/orders/store.go", Symbol: "Store", Side: "before"}
	if issue := checker.Check(ctx, before); issue != nil {
		t.Errorf("a before-side reference should resolve against the base revision: %v", issue)
	}
}

func TestSymbolOutsideStatedRangeIsWeakNotBroken(t *testing.T) {
	checker, _ := newChecker(t)
	issue := checker.Check(context.Background(), walkthrough.CodeReference{
		Path: "internal/orders/service.go", Symbol: "Create", StartLine: 1, EndLine: 2,
	})
	if issue == nil || issue.Severity != refcheck.SeverityWeak {
		t.Errorf("an imprecise line range should be weak, not broken: %v", issue)
	}
}

func TestPruneRemovesOnlyBrokenReferences(t *testing.T) {
	checker, _ := newChecker(t)
	w := &walkthrough.Walkthrough{
		Kind:  walkthrough.KindChange,
		Title: "t", Headline: "h",
		Complexity: walkthrough.Complexity{Level: 2},
		Steps: []walkthrough.Step{{
			ID: "s1", Title: "Step", Explanation: "text",
			CodeRefs: []walkthrough.CodeReference{
				{Path: "internal/orders/service.go", Symbol: "Create"},
				{Path: "internal/cache/session_cache.go", Symbol: "SessionCache"},
			},
		}},
		StartHere: []walkthrough.CodeReference{{Path: "internal/orders/service.go"}},
	}
	removed := checker.Prune(context.Background(), w)
	if len(removed) != 1 {
		t.Fatalf("removed %d references, want 1: %v", len(removed), removed)
	}
	if len(w.Steps[0].CodeRefs) != 1 || w.Steps[0].CodeRefs[0].Path != "internal/orders/service.go" {
		t.Errorf("pruning should keep resolvable references: %+v", w.Steps[0].CodeRefs)
	}
	if len(w.StartHere) != 1 {
		t.Error("valid start-here references should survive")
	}
}

func TestCheckFilesAllowsFilesRemovedByTheChange(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	base := fixture.Commit("baseline")
	fixture.Remove("internal/orders/store.go")
	fixture.Commit("remove the store")

	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	checker := refcheck.New(repo, "HEAD", base)
	missing := checker.CheckFiles(context.Background(), []string{"internal/orders/store.go", "internal/nope.go"})
	if len(missing) != 1 || missing[0] != "internal/nope.go" {
		t.Errorf("missing = %v; a file deleted by the change still exists in the base", missing)
	}
}
