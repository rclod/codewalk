package gitrepo_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/testutil"
)

func TestDiscoverFindsRepositoryRoot(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()

	repo, err := gitrepo.Discover(fixture.Dir + "/internal/orders")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if repo.Root != resolved(t, fixture.Dir) {
		t.Errorf("root = %q, want %q", repo.Root, fixture.Dir)
	}
}

func TestDiscoverRejectsNonRepository(t *testing.T) {
	dir := t.TempDir()
	if _, err := gitrepo.Discover(dir); err == nil {
		t.Fatal("expected an error outside a repository")
	}
}

func TestWriteCommandsAreRefused(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	// The observational guarantee is enforced by an allowlist, not by
	// convention, so a write command must fail even though git would accept it.
	for _, cmd := range []string{"commit", "checkout", "reset", "clean", "push"} {
		if _, err := repo.Git(context.Background(), cmd); err == nil {
			t.Errorf("git %s was permitted; codewalk must never write to a repository", cmd)
		}
	}
}

func TestBranchChangeSetUsesMergeBase(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	fixture.Branch("feature")
	fixture.Write("internal/orders/worker.go", "package orders\n\n// Worker processes orders asynchronously.\ntype Worker struct{}\n")
	fixture.Commit("add worker")

	// A commit on main after branching must not appear in the change.
	fixture.Checkout("main")
	fixture.Write("docs/notes.md", "unrelated\n")
	fixture.Commit("unrelated main commit")
	fixture.Checkout("feature")

	repo := open(t, fixture.Dir)
	cs, err := repo.BuildChangeSet(context.Background(), gitrepo.Selection{Mode: gitrepo.ModeAuto})
	if err != nil {
		t.Fatalf("build change set: %v", err)
	}
	if cs.Mode != gitrepo.ModeBranch {
		t.Errorf("mode = %s, want branch", cs.Mode)
	}
	if got := paths(cs); len(got) != 1 || got[0] != "internal/orders/worker.go" {
		t.Errorf("changed files = %v, want only the worker file", got)
	}
}

func TestWorkingTreeAndStagedSelections(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	fixture.Write("internal/orders/service.go", "package orders\n\n// Service was edited but not staged.\n")
	fixture.Write("internal/orders/queue.go", "package orders\n\n// Queue is staged.\n")
	fixture.Git("add", "internal/orders/queue.go")

	repo := open(t, fixture.Dir)
	ctx := context.Background()

	staged, err := repo.BuildChangeSet(ctx, gitrepo.Selection{Mode: gitrepo.ModeStaged})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(staged); len(got) != 1 || got[0] != "internal/orders/queue.go" {
		t.Errorf("staged files = %v, want the staged file only", got)
	}

	working, err := repo.BuildChangeSet(ctx, gitrepo.Selection{Mode: gitrepo.ModeWorkingTree})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths(working)) != 2 {
		t.Errorf("working tree files = %v, want both files", paths(working))
	}
	if working.HeadRev() != gitrepo.WorkingTree {
		t.Errorf("head rev = %q, want the working tree pseudo-revision", working.HeadRev())
	}
}

func TestExplicitRangeAndSingleCommit(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	first := fixture.Commit("empty follow-up")
	fixture.Write("internal/orders/events.go", "package orders\n\n// OrderCreated is published on creation.\ntype OrderCreated struct{}\n")
	second := fixture.Commit("publish order created event")

	repo := open(t, fixture.Dir)
	ctx := context.Background()

	rangeSet, err := repo.BuildChangeSet(ctx, gitrepo.Selection{Spec: first + ".." + second})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(rangeSet); len(got) != 1 || got[0] != "internal/orders/events.go" {
		t.Errorf("range files = %v", got)
	}

	commitSet, err := repo.BuildChangeSet(ctx, gitrepo.Selection{Spec: second})
	if err != nil {
		t.Fatal(err)
	}
	if commitSet.Mode != gitrepo.ModeCommit {
		t.Errorf("mode = %s, want commit", commitSet.Mode)
	}
	if len(commitSet.Commits) != 1 || commitSet.Commits[0].Subject != "publish order created event" {
		t.Errorf("commits = %+v", commitSet.Commits)
	}
}

func TestRenameAndGeneratedFileDetection(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	fixture.Write("package-lock.json", "{\n  \"lockfileVersion\": 3\n}\n")
	fixture.Commit("add lockfile")

	fixture.Git("mv", "internal/orders/store.go", "internal/orders/repository.go")
	fixture.Write("package-lock.json", "{\n  \"lockfileVersion\": 3,\n  \"changed\": true\n}\n")

	repo := open(t, fixture.Dir)
	cs, err := repo.BuildChangeSet(context.Background(), gitrepo.Selection{Mode: gitrepo.ModeWorkingTree})
	if err != nil {
		t.Fatal(err)
	}
	var sawRename, sawGenerated bool
	for _, f := range cs.Files {
		if f.Path == "internal/orders/repository.go" && f.Status == "renamed" && f.OldPath == "internal/orders/store.go" {
			sawRename = true
		}
		if f.Path == "package-lock.json" && f.Generated {
			sawGenerated = true
		}
	}
	if !sawRename {
		t.Errorf("rename not detected: %+v", cs.Files)
	}
	if !sawGenerated {
		t.Errorf("lockfile not marked generated: %+v", cs.Files)
	}
}

func TestDiffOmitsGeneratedFileContents(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	fixture.Write("go.sum", "example.com/dep v1.0.0 h1:aaaa=\n")
	fixture.Write("internal/orders/service.go", "package orders\n\n// Service changed.\n")

	repo := open(t, fixture.Dir)
	ctx := context.Background()
	cs, err := repo.BuildChangeSet(ctx, gitrepo.Selection{Mode: gitrepo.ModeWorkingTree})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := repo.Diff(ctx, cs, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(diff, "Service changed") {
		t.Error("diff should contain the real source change")
	}
	if contains(diff, "example.com/dep v1.0.0") {
		t.Error("diff should omit generated file contents so they do not consume the context window")
	}
	if !contains(diff, "generated file") {
		t.Error("diff should note that a generated file was omitted rather than hiding it entirely")
	}
}

func TestRemoteHostKeepsOnlyTheHost(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	// A remote URL can carry a username; only the host is ever retained.
	fixture.Git("remote", "add", "origin", "https://someone@git.example.com/team/project.git")

	repo := open(t, fixture.Dir)
	if repo.RemoteHost != "git.example.com" {
		t.Errorf("remote host = %q, want git.example.com", repo.RemoteHost)
	}
}

func open(t *testing.T, dir string) *gitrepo.Repo {
	t.Helper()
	repo, err := gitrepo.Discover(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	return repo
}

func paths(cs *gitrepo.ChangeSet) []string {
	var out []string
	for _, f := range cs.Files {
		out = append(out, f.Path)
	}
	return out
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func resolved(t *testing.T, dir string) string {
	t.Helper()
	repo, err := gitrepo.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo.Root
}

func TestDiffIsBoundedInTotalAndSaysSo(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	// Many files, each individually small: per-file truncation alone would not
	// stop this from overflowing a context window.
	big := strings.Repeat("// a line of changed code\n", 200)
	for i := 0; i < 40; i++ {
		fixture.Write(fmt.Sprintf("internal/generated/file%02d.go", i), "package generated\n\n"+big)
	}

	repo := open(t, fixture.Dir)
	ctx := context.Background()
	cs, err := repo.BuildChangeSet(ctx, gitrepo.Selection{Mode: gitrepo.ModeWorkingTree})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := repo.Diff(ctx, cs, true, 0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) > 60000 {
		t.Errorf("diff is %d bytes despite a 20000 byte budget", len(diff))
	}
	if !contains(diff, "files omitted") {
		t.Error("truncation must be announced, or a model will reason over a partial diff believing it is complete")
	}
	if !contains(diff, "file_diff tool") {
		t.Error("the omission notice should say how to read the rest")
	}
	if !contains(diff, "internal/generated/file39.go") {
		t.Error("omitted files should still be listed by name")
	}
}
