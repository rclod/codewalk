package eval_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclod/codewalk/internal/eval"
	"github.com/rclod/codewalk/internal/gitrepo"
)

// corpusDir points at the shipped benchmark corpus, which is part of the
// repository rather than a test fixture: if a case stops loading or stops
// materialising, the benchmark suite is broken.
func corpusDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "benchmarks", "cases")
}

func TestShippedCorpusLoads(t *testing.T) {
	cases, err := eval.LoadCorpus(corpusDir(t))
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(cases) < 2 {
		t.Fatalf("expected several benchmark cases, got %d", len(cases))
	}
	for _, c := range cases {
		if c.Description == "" {
			t.Errorf("case %s has no description", c.ID)
		}
		if c.WalkthroughKind() == "change" && (c.Base == "" || c.Head == "") {
			t.Errorf("change case %s must declare base and head", c.ID)
		}
		if c.Understanding == nil {
			continue
		}
		// A curated understanding model is only useful if its items are
		// identifiable and described.
		for _, item := range c.Understanding.AllItems() {
			if item.Item.ID == "" || item.Item.Description == "" {
				t.Errorf("case %s has an understanding item without an id or description: %+v", c.ID, item.Item)
			}
		}
	}
}

func TestCaseMaterialisesIntoAGitRepository(t *testing.T) {
	c, err := eval.LoadCase(filepath.Join(corpusDir(t), "02-async-order-completion"))
	if err != nil {
		t.Fatal(err)
	}
	repoPath, err := c.Materialize(t.TempDir())
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}

	repo, err := gitrepo.Discover(repoPath)
	if err != nil {
		t.Fatalf("materialised fixture is not a repository: %v", err)
	}
	ctx := context.Background()
	change, err := repo.BuildChangeSet(ctx, gitrepo.Selection{Spec: c.Base + ".." + c.Head})
	if err != nil {
		t.Fatalf("build change set from the case's declared range: %v", err)
	}
	if len(change.Files) == 0 {
		t.Fatal("the declared range contains no changes")
	}

	var sawWorker, sawOutbox bool
	for _, f := range change.Files {
		if strings.HasSuffix(f.Path, "orders/worker.go") {
			sawWorker = true
		}
		if strings.Contains(f.Path, "outbox") {
			sawOutbox = true
		}
	}
	if !sawWorker || !sawOutbox {
		t.Errorf("the change should introduce the worker and the outbox: %v", change.Files)
	}
}

func TestFixturesContainNoIdentifyingInformation(t *testing.T) {
	// The corpus is published. This check is deliberately blunt: it is cheaper
	// to keep it true than to audit it later.
	cases, err := eval.LoadCorpus(corpusDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		repoPath, err := c.Materialize(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		repo, err := gitrepo.Discover(repoPath)
		if err != nil {
			t.Fatal(err)
		}
		log, err := repo.Git(context.Background(), "log", "--format=%an <%ae>")
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
			if !strings.Contains(line, "example.com") {
				t.Errorf("case %s commit identity %q is not a fictional address", c.ID, line)
			}
		}
	}
}
