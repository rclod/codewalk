package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/testutil"
	"github.com/rclod/codewalk/internal/tools"
)

func newTools(t *testing.T) (*tools.Repo, *gitrepo.Repo) {
	t.Helper()
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return tools.NewRepo(repo, nil, nil, tools.Options{AllowGitHistory: true}), repo
}

func call(t *testing.T, r *tools.Repo, name string, args map[string]any) llm.ToolResult {
	t.Helper()
	input, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return r.Execute(context.Background(), llm.ToolCall{ID: "call-1", Name: name, Input: input})
}

// canary is content that must never appear in a tool result. It is deliberately
// unusual: a common word would also occur in error messages and in temporary
// directory paths such as macOS's /private/var, turning a passing sandbox into
// a failing test.
const canary = "COD3WALK-CANARY-MUST-NOT-BE-READ"

func TestPathSandboxRejectsEscapes(t *testing.T) {
	repoTools, repo := newTools(t)
	// A file outside the repository that must stay unreachable.
	outside := filepath.Join(filepath.Dir(repo.Root), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	for _, path := range []string{
		"../outside-secret.txt",
		"internal/../../outside-secret.txt",
		"/etc/passwd",
		outside,
	} {
		res := call(t, repoTools, "read_file", map[string]any{"path": path})
		if !res.IsError {
			t.Errorf("read_file(%q) succeeded; paths outside the repository must be refused", path)
		}
		if strings.Contains(res.Content, canary) {
			t.Fatalf("read_file(%q) leaked content from outside the repository", path)
		}
	}
}

func TestSymlinkEscapeIsRefused(t *testing.T) {
	repoTools, repo := newTools(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo.Root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	res := call(t, repoTools, "read_file", map[string]any{"path": "link.txt"})
	if !res.IsError {
		t.Error("a symlink pointing outside the repository must be refused")
	}
	if strings.Contains(res.Content, canary) {
		t.Fatalf("read_file through a symlink leaked content from outside the repository")
	}
}

func TestReadFileIsLineNumberedAndRangeable(t *testing.T) {
	repoTools, _ := newTools(t)
	res := call(t, repoTools, "read_file", map[string]any{
		"path": "internal/orders/service.go", "start_line": 3, "end_line": 5,
	})
	if res.IsError {
		t.Fatalf("read_file failed: %s", res.Content)
	}
	if !strings.Contains(res.Content, "3\t") {
		t.Errorf("output should be line numbered so a model can cite exact ranges:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "1\tpackage orders") {
		t.Error("output should respect the requested range")
	}
	if !strings.Contains(res.Content, `lines="3-5`) {
		t.Errorf("output should state which lines it contains:\n%s", res.Content)
	}
}

func TestReadFileReportsMissingPathsClearly(t *testing.T) {
	repoTools, _ := newTools(t)
	res := call(t, repoTools, "read_file", map[string]any{"path": "internal/orders/nope.go"})
	if !res.IsError {
		t.Fatal("reading a missing file should be an error")
	}
	if !strings.Contains(res.Content, "does not exist") {
		t.Errorf("error should say what went wrong: %q", res.Content)
	}
}

func TestSearchFindsMatchesAndReportsMisses(t *testing.T) {
	repoTools, _ := newTools(t)
	hit := call(t, repoTools, "search", map[string]any{"pattern": "func \\(s \\*Service\\)"})
	if hit.IsError || !strings.Contains(hit.Content, "internal/orders/service.go") {
		t.Errorf("search did not find the service method: %s", hit.Content)
	}
	miss := call(t, repoTools, "search", map[string]any{"pattern": "NoSuchSymbolAnywhere"})
	if miss.IsError {
		t.Fatal("a search with no matches is not an error")
	}
	if !strings.Contains(miss.Content, "no matches") {
		t.Errorf("an empty search should say so plainly: %s", miss.Content)
	}
	bad := call(t, repoTools, "search", map[string]any{"pattern": "([unclosed"})
	if !bad.IsError {
		t.Error("an invalid regular expression should be reported")
	}
}

func TestFindDefinitionLocatesSymbols(t *testing.T) {
	repoTools, _ := newTools(t)
	res := call(t, repoTools, "find_definition", map[string]any{"symbol": "Service.Create"})
	if res.IsError || !strings.Contains(res.Content, "func (s *Service) Create") {
		t.Errorf("qualified symbol lookup failed: %s", res.Content)
	}
}

func TestChangeToolsRequireAChange(t *testing.T) {
	repoTools, _ := newTools(t)
	for _, name := range []string{"changed_files", "file_diff"} {
		res := call(t, repoTools, name, map[string]any{"path": "internal/orders/service.go"})
		if !res.IsError {
			t.Errorf("%s should be unavailable when no change is being explained", name)
		}
	}
	// The definitions offered to a model should not advertise them either.
	for _, def := range repoTools.Definitions() {
		if def.Name == "changed_files" {
			t.Error("changed_files should not be offered in codebase mode")
		}
	}
}

func TestGitHistoryCanBeDisabled(t *testing.T) {
	fixture := testutil.NewRepo(t)
	fixture.SampleService()
	repo, err := gitrepo.Discover(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	restricted := tools.NewRepo(repo, nil, nil, tools.Options{AllowGitHistory: false})
	if res := call(t, restricted, "git_log", nil); !res.IsError {
		t.Error("git history must be refused when the run disables it")
	}
	for _, def := range restricted.Definitions() {
		if def.Name == "git_log" || def.Name == "git_show" {
			t.Errorf("%s should not be offered when history is disabled", def.Name)
		}
	}
}

func TestProvenanceIsRecorded(t *testing.T) {
	repoTools, _ := newTools(t)
	call(t, repoTools, "read_file", map[string]any{"path": "internal/orders/service.go"})
	call(t, repoTools, "search", map[string]any{"pattern": "Store"})

	if files := repoTools.FilesInspected(); len(files) != 1 || files[0] != "internal/orders/service.go" {
		t.Errorf("files inspected = %v", files)
	}
	invocations := repoTools.Invocations()
	if len(invocations) != 2 {
		t.Fatalf("expected two recorded invocations, got %d", len(invocations))
	}
	if summary := tools.SortedInvocationSummary(invocations); summary["read_file"] != 1 || summary["search"] != 1 {
		t.Errorf("invocation summary = %v", summary)
	}
}

func TestToolResultsAreMarkedAsRepositoryContent(t *testing.T) {
	repoTools, _ := newTools(t)
	res := call(t, repoTools, "read_file", map[string]any{"path": "README.md"})
	// Repository content is untrusted data; the markers are how an agent tells
	// its instructions apart from the material it is reading.
	if !strings.HasPrefix(res.Content, "<file ") || !strings.HasSuffix(strings.TrimSpace(res.Content), "</file>") {
		t.Errorf("tool output should be wrapped in explicit content markers:\n%s", res.Content)
	}
}

func TestUnknownToolIsReported(t *testing.T) {
	repoTools, _ := newTools(t)
	if res := call(t, repoTools, "delete_everything", nil); !res.IsError {
		t.Error("unknown tools must be refused")
	}
}
