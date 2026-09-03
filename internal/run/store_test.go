package run_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rclod/codewalk/internal/run"
	"github.com/rclod/codewalk/internal/testutil"
	"github.com/rclod/codewalk/internal/walkthrough"
)

func newStore(t *testing.T) *run.Store {
	t.Helper()
	store, err := run.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func saveRun(t *testing.T, store *run.Store, id string, created time.Time) *run.Run {
	t.Helper()
	r := &run.Run{
		ID:         id,
		CreatedAt:  created,
		Kind:       walkthrough.KindChange,
		Status:     "complete",
		Repository: run.Repository{Name: "orders", Path: "/home/user/projects/example-app"},
		Scope:      walkthrough.Scope{Selector: "branch", Base: "main", Head: "feature"},
	}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	w := testutil.SampleWalkthrough()
	w.ID = id
	if err := store.SaveWalkthrough(id, w); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	store := newStore(t)
	saveRun(t, store, "20260101-120000-aaaaaa", time.Now())

	loaded, err := store.Load("20260101-120000-aaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Repository.Name != "orders" || loaded.Scope.Base != "main" {
		t.Errorf("run did not round trip: %+v", loaded)
	}
	w, err := store.Walkthrough(loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Steps) == 0 {
		t.Error("walkthrough did not round trip")
	}
}

func TestResolveAcceptsPrefixesAndLatest(t *testing.T) {
	store := newStore(t)
	saveRun(t, store, "20260101-120000-aaaaaa", time.Now().Add(-time.Hour))
	saveRun(t, store, "20260102-120000-bbbbbb", time.Now())

	id, err := store.Resolve("20260102")
	if err != nil || id != "20260102-120000-bbbbbb" {
		t.Errorf("prefix resolution = %q, %v", id, err)
	}
	latest, err := store.Resolve("latest")
	if err != nil || latest != "20260102-120000-bbbbbb" {
		t.Errorf("latest = %q, %v", latest, err)
	}
	if _, err := store.Resolve("2026010"); err == nil {
		t.Error("an ambiguous prefix should be rejected rather than guessed")
	}
	if _, err := store.Resolve("nope"); err == nil {
		t.Error("an unknown run should be an error")
	}
}

func TestListIsNewestFirst(t *testing.T) {
	store := newStore(t)
	saveRun(t, store, "20260101-120000-aaaaaa", time.Now().Add(-time.Hour))
	saveRun(t, store, "20260103-120000-cccccc", time.Now())
	saveRun(t, store, "20260102-120000-bbbbbb", time.Now().Add(-time.Minute))

	summaries, err := store.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 3 {
		t.Fatalf("got %d summaries", len(summaries))
	}
	if summaries[0].ID != "20260103-120000-cccccc" {
		t.Errorf("first summary = %q, want the newest run", summaries[0].ID)
	}
	if summaries[0].Title == "" {
		t.Error("summaries should include the walkthrough title for navigation")
	}
	if summaries[0].Scope != "main..feature" {
		t.Errorf("scope label = %q", summaries[0].Scope)
	}

	limited, err := store.List(2)
	if err != nil || len(limited) != 2 {
		t.Errorf("limit not honoured: %d, %v", len(limited), err)
	}
}

func TestConversationAccumulates(t *testing.T) {
	store := newStore(t)
	saveRun(t, store, "20260101-120000-aaaaaa", time.Now())

	if err := store.AppendTurn("20260101-120000-aaaaaa",
		run.Turn{Role: "user", Content: "why is the worker involved?"},
		run.Turn{Role: "assistant", Content: "Because completion moved out of the request."},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn("20260101", run.Turn{Role: "user", Content: "and retries?"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("20260101")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Conversation) != 3 {
		t.Errorf("conversation length = %d, want the thread to accumulate", len(loaded.Conversation))
	}
}

func TestEvalAndFeedbackArtifacts(t *testing.T) {
	store := newStore(t)
	saveRun(t, store, "20260101-120000-aaaaaa", time.Now())

	if err := store.SaveEval("latest", "eval", map[string]any{"score": 4.5}); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := store.LoadEval("latest", "eval", &result); err != nil {
		t.Fatal(err)
	}
	if result["score"] != 4.5 {
		t.Errorf("eval result = %v", result)
	}
	if err := store.SaveFeedback("latest", map[string]any{"answer": "mostly"}); err != nil {
		t.Fatal(err)
	}
	var feedback map[string]any
	if err := store.Feedback("latest", &feedback); err != nil {
		t.Fatal(err)
	}
	if feedback["answer"] != "mostly" {
		t.Errorf("feedback = %v", feedback)
	}
}

func TestSanitizedDropsLocalPaths(t *testing.T) {
	r := &run.Run{
		ID:         "x",
		Repository: run.Repository{Name: "orders", Path: "/home/user/projects/example-app"},
		Scope:      walkthrough.Scope{RepositoryPath: "/home/user/projects/example-app"},
	}
	clean := r.Sanitized()
	if clean.Repository.Path != "" || clean.Scope.RepositoryPath != "" {
		t.Error("a shared run record must not carry a local filesystem path")
	}
	if r.Repository.Path == "" {
		t.Error("sanitising should not mutate the original record")
	}
}

func TestNewIDIsTimeSortable(t *testing.T) {
	earlier := run.NewID(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	later := run.NewID(time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC))
	if !(earlier < later) {
		t.Errorf("run ids should sort chronologically: %q !< %q", earlier, later)
	}
	if strings.ContainsAny(earlier, "/\\ ") {
		t.Errorf("run ids must be safe as directory names: %q", earlier)
	}
}
