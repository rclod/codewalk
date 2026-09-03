package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/run"
	"github.com/rclod/codewalk/internal/server"
	"github.com/rclod/codewalk/internal/service"
	"github.com/rclod/codewalk/internal/testutil"
	"github.com/rclod/codewalk/internal/walkthrough"
)

func newServer(t *testing.T) (http.Handler, *run.Store, string) {
	t.Helper()
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := run.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Options{
		Service:           service.New(store, "test"),
		Config:            config.Default(),
		Version:           "test",
		DefaultRepository: repo.Root,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler(), store, repo.Root
}

func seedRun(t *testing.T, store *run.Store, repoRoot string) string {
	t.Helper()
	id := run.NewID(time.Now())
	w := testutil.SampleWalkthrough()
	w.ID = id
	record := &run.Run{
		ID: id, CreatedAt: time.Now().UTC(), Kind: walkthrough.KindChange, Status: "complete",
		Repository: run.Repository{Name: "orders", Path: repoRoot},
		Scope:      w.Scope,
	}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWalkthrough(id, w); err != nil {
		t.Fatal(err)
	}
	return id
}

func do(t *testing.T, handler http.Handler, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHealthReportsSchemaVersion(t *testing.T) {
	handler, _, _ := newServer(t)
	rec := do(t, handler, "GET", "/api/v1/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["schema_version"] != walkthrough.SchemaVersion {
		t.Errorf("health should advertise the walkthrough schema version: %v", body)
	}
}

func TestRepositoryEndpointDescribesTheDefaultRepository(t *testing.T) {
	handler, _, _ := newServer(t)
	rec := do(t, handler, "GET", "/api/v1/repository", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["branch"] != "main" {
		t.Errorf("branch = %v", body["branch"])
	}
}

func TestWalkthroughLifecycleEndpoints(t *testing.T) {
	handler, store, repoRoot := newServer(t)
	id := seedRun(t, store, repoRoot)

	list := do(t, handler, "GET", "/api/v1/walkthroughs", "", nil)
	if !strings.Contains(list.Body.String(), id) {
		t.Errorf("listing should include the stored run:\n%s", list.Body)
	}

	get := do(t, handler, "GET", "/api/v1/walkthroughs/"+id, "", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("status = %d", get.Code)
	}
	var payload struct {
		Run         run.Run                 `json:"run"`
		Walkthrough walkthrough.Walkthrough `json:"walkthrough"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Walkthrough.Steps) == 0 || payload.Run.ID != id {
		t.Errorf("response should carry both the run and the canonical walkthrough")
	}

	missing := do(t, handler, "GET", "/api/v1/walkthroughs/nope", "", nil)
	if missing.Code != http.StatusNotFound {
		t.Errorf("unknown run status = %d, want 404", missing.Code)
	}
}

func TestSourceEndpointServesRepositoryFilesAndRefusesEscapes(t *testing.T) {
	handler, store, repoRoot := newServer(t)
	id := seedRun(t, store, repoRoot)

	ok := do(t, handler, "GET", "/api/v1/walkthroughs/"+id+"/source?path=internal/orders/service.go", "", nil)
	if ok.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", ok.Code, ok.Body)
	}
	if !strings.Contains(ok.Body.String(), "package orders") {
		t.Errorf("source body = %s", ok.Body)
	}

	escape := do(t, handler, "GET", "/api/v1/walkthroughs/"+id+"/source?path=../../../etc/passwd", "", nil)
	if escape.Code == http.StatusOK {
		t.Error("the source endpoint must not serve files outside the repository")
	}
}

func TestFeedbackIsRecordedAndValidated(t *testing.T) {
	handler, store, repoRoot := newServer(t)
	id := seedRun(t, store, repoRoot)

	rec := do(t, handler, "POST", "/api/v1/walkthroughs/"+id+"/feedback", `{"answer":"mostly"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var stored map[string]any
	if err := store.Feedback(id, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["answer"] != "mostly" {
		t.Errorf("stored feedback = %v", stored)
	}

	bad := do(t, handler, "POST", "/api/v1/walkthroughs/"+id+"/feedback", `{"answer":"maybe"}`, nil)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("invalid feedback status = %d, want 400", bad.Code)
	}
}

func TestCrossOriginRequestsAreRejected(t *testing.T) {
	handler, _, _ := newServer(t)
	// The API is unauthenticated by design, so a page on another origin must
	// not be able to drive it.
	rec := do(t, handler, "GET", "/api/v1/health", "", map[string]string{"Origin": "https://attacker.example.com"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want 403", rec.Code)
	}
	local := do(t, handler, "GET", "/api/v1/health", "", map[string]string{"Origin": "http://localhost:7457"})
	if local.Code != http.StatusOK {
		t.Errorf("local origin status = %d, want 200", local.Code)
	}
}

func TestUnknownJobIsNotFound(t *testing.T) {
	handler, _, _ := newServer(t)
	if rec := do(t, handler, "GET", "/api/v1/jobs/missing", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestInvalidCreateRequestIsRejected(t *testing.T) {
	handler, _, _ := newServer(t)
	rec := do(t, handler, "POST", "/api/v1/walkthroughs", `{"type":"telepathy"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestWebApplicationIsServed(t *testing.T) {
	handler, _, _ := newServer(t)
	rec := do(t, handler, "GET", "/", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "codewalk") {
		t.Errorf("the embedded web application should be served at the root: %d", rec.Code)
	}
	// Client-side routes fall back to the application shell.
	if fallback := do(t, handler, "GET", "/some/deep/route", "", nil); fallback.Code != http.StatusOK {
		t.Errorf("unknown route status = %d, want the app shell", fallback.Code)
	}
}
