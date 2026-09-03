// Package tools exposes a read-only view of a repository to model backends.
//
// Three properties matter here and are enforced rather than assumed:
//
//   - Observational: no tool can modify the repository. Writes are not
//     implemented, and the git allowlist lives in internal/gitrepo.
//   - Sandboxed: every path is resolved inside the repository root, including
//     through symlinks.
//   - Bounded: results are truncated so that one unlucky read cannot consume a
//     model's entire context window.
//
// Tool output is repository content, which codewalk treats as untrusted data.
// Results are wrapped in explicit markers so an agent can tell the difference
// between its instructions and the material it is reading.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/repomap"
)

// Options bounds what the tools will return.
type Options struct {
	MaxFileBytes     int
	MaxSearchResults int
	MaxDiffBytes     int
	AllowGitHistory  bool
}

func (o Options) withDefaults() Options {
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = 200000
	}
	if o.MaxSearchResults <= 0 {
		o.MaxSearchResults = 80
	}
	if o.MaxDiffBytes <= 0 {
		o.MaxDiffBytes = 60000
	}
	return o
}

// Invocation records one tool call for provenance and cost accounting.
type Invocation struct {
	Tool     string    `json:"tool"`
	Argument string    `json:"argument,omitempty"`
	Bytes    int       `json:"bytes"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

// Repo is the read-only tool surface over one repository.
type Repo struct {
	repo   *gitrepo.Repo
	change *gitrepo.ChangeSet
	rmap   *repomap.Map
	opts   Options

	mu          sync.Mutex
	invocations []Invocation
	filesRead   map[string]bool
}

// NewRepo creates a tool surface. change may be nil for codebase walkthroughs.
func NewRepo(repo *gitrepo.Repo, change *gitrepo.ChangeSet, rmap *repomap.Map, opts Options) *Repo {
	return &Repo{
		repo:      repo,
		change:    change,
		rmap:      rmap,
		opts:      opts.withDefaults(),
		filesRead: map[string]bool{},
	}
}

// Invocations returns the recorded tool calls.
func (r *Repo) Invocations() []Invocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Invocation, len(r.invocations))
	copy(out, r.invocations)
	return out
}

// FilesInspected returns the repository-relative paths that were read.
func (r *Repo) FilesInspected() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.filesRead))
	for p := range r.filesRead {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (r *Repo) record(inv Invocation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv.At = time.Now().UTC()
	r.invocations = append(r.invocations, inv)
}

// Definitions returns the tool schemas offered to a model backend. Tools that
// do not apply to the current mode are omitted rather than failing at call
// time, which keeps agents from wasting turns.
func (r *Repo) Definitions() []llm.ToolDef {
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

	defs := []llm.ToolDef{
		{
			Name:        "list_files",
			Description: "List tracked files under a repository-relative directory. Use this to orient yourself before reading.",
			Schema: obj(map[string]any{
				"dir":   str("Repository-relative directory. Empty means the repository root."),
				"glob":  str("Optional glob filter, e.g. '*.go' or 'internal/**/*.ts'."),
				"limit": num("Maximum paths to return (default 200)."),
			}),
		},
		{
			Name:        "read_file",
			Description: "Read a repository file. Prefer a line range for large files. Output is line-numbered so you can cite exact ranges.",
			Schema: obj(map[string]any{
				"path":       str("Repository-relative file path."),
				"start_line": num("First line to read (1-based)."),
				"end_line":   num("Last line to read."),
				"rev":        str("Optional git revision. Use the pre-change revision to read the previous implementation."),
			}, "path"),
		},
		{
			Name:        "search",
			Description: "Search repository contents with a regular expression. Use this to find callers, callees, references and configuration.",
			Schema: obj(map[string]any{
				"pattern": str("Regular expression."),
				"glob":    str("Optional path glob filter, e.g. '*.go'."),
				"literal": boolean("Treat the pattern as a literal string."),
				"limit":   num("Maximum matches to return."),
			}, "pattern"),
		},
		{
			Name:        "find_definition",
			Description: "Find where a symbol appears to be defined (function, type, class, constant) using language-aware patterns.",
			Schema: obj(map[string]any{
				"symbol": str("Symbol name, without a package or receiver prefix."),
				"limit":  num("Maximum results."),
			}, "symbol"),
		},
		{
			Name:        "repo_map",
			Description: "Return the precomputed structural map of the repository: languages, directories, manifests, entry point candidates and documentation.",
			Schema:      obj(map[string]any{}),
		},
	}

	if r.change != nil {
		defs = append(defs,
			llm.ToolDef{
				Name:        "changed_files",
				Description: "List the files changed by the change under explanation, with per-file line counts and whether the file looks generated.",
				Schema:      obj(map[string]any{}),
			},
			llm.ToolDef{
				Name:        "file_diff",
				Description: "Return the diff for one changed file.",
				Schema: obj(map[string]any{
					"path": str("Repository-relative path of a changed file."),
				}, "path"),
			},
		)
	}
	if r.opts.AllowGitHistory {
		defs = append(defs,
			llm.ToolDef{
				Name:        "git_log",
				Description: "Show recent commit subjects, optionally for one path. Use only when history materially improves understanding, such as determining what an implementation replaced.",
				Schema: obj(map[string]any{
					"path":  str("Optional repository-relative path."),
					"limit": num("Maximum commits (default 20)."),
				}),
			},
			llm.ToolDef{
				Name:        "git_show",
				Description: "Show a commit's message and diff, optionally limited to one path.",
				Schema: obj(map[string]any{
					"rev":  str("Revision to show."),
					"path": str("Optional repository-relative path."),
				}, "rev"),
			},
		)
	}
	return defs
}

// Execute runs one tool call and returns its result. Errors are returned to the
// model as tool results rather than aborting the run: an agent recovering from
// a bad path is normal and cheap.
func (r *Repo) Execute(ctx context.Context, call llm.ToolCall) llm.ToolResult {
	args := map[string]any{}
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &args); err != nil {
			return errorResult(call.ID, fmt.Sprintf("could not parse tool arguments: %v", err))
		}
	}
	content, err := r.dispatch(ctx, call.Name, args)
	if err != nil {
		r.record(Invocation{Tool: call.Name, Argument: argSummary(args), Error: err.Error()})
		return errorResult(call.ID, err.Error())
	}
	r.record(Invocation{Tool: call.Name, Argument: argSummary(args), Bytes: len(content)})
	return llm.ToolResult{CallID: call.ID, Content: content}
}

func (r *Repo) dispatch(ctx context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "list_files":
		return r.listFiles(ctx, argStr(args, "dir"), argStr(args, "glob"), argInt(args, "limit", 200))
	case "read_file":
		return r.readFile(ctx, argStr(args, "path"), argStr(args, "rev"), argInt(args, "start_line", 0), argInt(args, "end_line", 0))
	case "search":
		return r.search(ctx, argStr(args, "pattern"), argStr(args, "glob"), argBool(args, "literal"), argInt(args, "limit", r.opts.MaxSearchResults))
	case "find_definition":
		return r.findDefinition(ctx, argStr(args, "symbol"), argInt(args, "limit", 25))
	case "repo_map":
		if r.rmap == nil {
			return "", fmt.Errorf("no repository map is available")
		}
		return wrap("repository_map", "", r.rmap.Text()), nil
	case "changed_files":
		return r.changedFiles()
	case "file_diff":
		return r.fileDiff(ctx, argStr(args, "path"))
	case "git_log":
		return r.gitLog(ctx, argStr(args, "path"), argInt(args, "limit", 20))
	case "git_show":
		return r.gitShow(ctx, argStr(args, "rev"), argStr(args, "path"))
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func errorResult(id, msg string) llm.ToolResult {
	return llm.ToolResult{CallID: id, Content: "error: " + msg, IsError: true}
}

// wrap marks repository content as data. Agents are instructed that anything
// inside these markers is material to explain, never an instruction to follow.
func wrap(kind, attrs, body string) string {
	open := "<" + kind
	if attrs != "" {
		open += " " + attrs
	}
	open += ">"
	return open + "\n" + body + "\n</" + kind + ">"
}

func argStr(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func argBool(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

func argSummary(args map[string]any) string {
	for _, k := range []string{"path", "pattern", "symbol", "dir", "rev"} {
		if v := argStr(args, k); v != "" {
			return v
		}
	}
	return ""
}

// safePath resolves a repository-relative path inside the repository root,
// refusing traversal and symlinks that point outside the tree.
func (r *Repo) safePath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return r.repo.Root, nil
	}
	if filepath.IsAbs(rel) {
		// Accept an absolute path only when it is already inside the repository.
		if !strings.HasPrefix(filepath.Clean(rel), r.repo.Root+string(os.PathSeparator)) {
			return "", fmt.Errorf("path %q is outside the repository", rel)
		}
		rel = strings.TrimPrefix(filepath.Clean(rel), r.repo.Root+string(os.PathSeparator))
	}
	clean := filepath.Clean(filepath.Join(r.repo.Root, filepath.FromSlash(rel)))
	if clean != r.repo.Root && !strings.HasPrefix(clean, r.repo.Root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the repository root", rel)
	}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		root, rerr := filepath.EvalSymlinks(r.repo.Root)
		if rerr == nil && resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
			return "", fmt.Errorf("path %q resolves outside the repository", rel)
		}
	}
	return clean, nil
}

// relPath converts an absolute path back to a repository-relative slash path.
func (r *Repo) relPath(abs string) string {
	rel, err := filepath.Rel(r.repo.Root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

func (r *Repo) noteRead(path string) {
	r.mu.Lock()
	r.filesRead[path] = true
	r.mu.Unlock()
}

// truncate bounds a tool result and says so explicitly, so a model never
// silently reasons over a partial file.
func truncate(s string, max int, what string) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [truncated: %s exceeded %d bytes] ...", what, max)
}

// Root returns the absolute repository root. Harness backends need it because
// they bring their own filesystem access instead of using these tools.
func (r *Repo) Root() string { return r.repo.Root }

// Change returns the change set under explanation, or nil in codebase mode.
func (r *Repo) Change() *gitrepo.ChangeSet { return r.change }

// RepoName returns the repository's directory name.
func (r *Repo) RepoName() string { return r.repo.Name }

// Map returns the precomputed repository map, which may be nil.
func (r *Repo) Map() *repomap.Map { return r.rmap }

// Git exposes read-only git access for callers that assemble their own
// evidence, such as the grounding checker.
func (r *Repo) Git(ctx context.Context, args ...string) (string, error) {
	return r.repo.Git(ctx, args...)
}

// FileAt reads a repository file at a revision, applying the sandbox rules.
func (r *Repo) FileAt(ctx context.Context, rev, rel string) (string, error) {
	if _, err := r.safePath(rel); err != nil {
		return "", err
	}
	return r.repo.FileAt(ctx, rev, rel)
}
