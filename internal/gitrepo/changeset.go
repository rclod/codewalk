package gitrepo

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rclod/codewalk/internal/walkthrough"
)

// SelectorMode names the ways a change can be selected for explanation.
type SelectorMode string

const (
	// ModeAuto asks the repository to pick the most useful selection.
	ModeAuto SelectorMode = "auto"
	// ModeWorkingTree explains every uncommitted change (staged and unstaged).
	ModeWorkingTree SelectorMode = "working-tree"
	// ModeStaged explains only staged changes.
	ModeStaged SelectorMode = "staged"
	// ModeBranch explains the current branch against its base, using merge-base
	// semantics so unrelated base commits do not appear in the change.
	ModeBranch SelectorMode = "branch"
	// ModeRange explains an explicit revision range.
	ModeRange SelectorMode = "range"
	// ModeCommit explains a single commit.
	ModeCommit SelectorMode = "commit"
)

// Selection is the user's request for what to explain, before it is resolved
// against the repository.
type Selection struct {
	Mode SelectorMode
	// Spec is a raw revision expression such as "main..feature", "HEAD~3" or a
	// single commit hash. It is optional.
	Spec string
	// Base overrides the base branch used by ModeBranch.
	Base string
	// Head overrides the head revision.
	Head string
}

// ChangeSet is a resolved, inspectable change.
type ChangeSet struct {
	Mode       SelectorMode
	Base       string // display form, e.g. "main"
	Head       string // display form, e.g. "feature-branch" or WorkingTree
	BaseCommit string
	HeadCommit string // empty when Head is the working tree
	Branch     string
	Files      []walkthrough.ChangedFile
	Stats      walkthrough.ChangeStats
	// Commits lists the commits introduced by the change, newest first. It is
	// empty for working-tree and staged selections.
	Commits []Commit
}

// Commit is a minimal commit record used to explain what a change replaced.
type Commit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
	// Author information is deliberately omitted: it is personal data and it is
	// not needed to understand what code does.
	Date string `json:"date"`
}

// Empty reports whether the change contains no files.
func (c *ChangeSet) Empty() bool { return len(c.Files) == 0 }

// Describe renders a short human-readable description of the selection.
func (c *ChangeSet) Describe() string {
	switch c.Mode {
	case ModeWorkingTree:
		return "uncommitted changes in the working tree"
	case ModeStaged:
		return "staged changes"
	case ModeCommit:
		return "commit " + shorten(c.HeadCommit)
	case ModeBranch:
		return fmt.Sprintf("%s compared with %s", c.Head, c.Base)
	default:
		return fmt.Sprintf("%s..%s", c.Base, c.Head)
	}
}

func shorten(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// BuildChangeSet resolves a Selection against the repository.
func (r *Repo) BuildChangeSet(ctx context.Context, sel Selection) (*ChangeSet, error) {
	if !r.HasCommits(ctx) {
		return nil, fmt.Errorf("repository %s has no commits yet, so there is no change to explain", r.Name)
	}
	mode := sel.Mode
	if mode == "" {
		mode = ModeAuto
	}
	if sel.Spec != "" && (mode == ModeAuto || mode == ModeRange || mode == ModeCommit) {
		return r.changeSetFromSpec(ctx, sel.Spec)
	}
	if mode == ModeAuto {
		var err error
		mode, err = r.autoMode(ctx, sel.Base)
		if err != nil {
			return nil, err
		}
	}

	switch mode {
	case ModeStaged:
		cs := &ChangeSet{Mode: ModeStaged, Base: "HEAD", Head: WorkingTree, Branch: r.CurrentBranch(ctx)}
		head, err := r.Resolve(ctx, "HEAD")
		if err != nil {
			return nil, err
		}
		cs.BaseCommit = head
		if err := r.fillFiles(ctx, cs, []string{"diff", "-M", "--cached"}); err != nil {
			return nil, err
		}
		return cs, nil

	case ModeWorkingTree:
		cs := &ChangeSet{Mode: ModeWorkingTree, Base: "HEAD", Head: WorkingTree, Branch: r.CurrentBranch(ctx)}
		head, err := r.Resolve(ctx, "HEAD")
		if err != nil {
			return nil, err
		}
		cs.BaseCommit = head
		if err := r.fillFiles(ctx, cs, []string{"diff", "-M", "HEAD"}); err != nil {
			return nil, err
		}
		// A file the reader created but has not staged is part of the work they
		// want explained, so untracked files are included here even though
		// `git diff` does not see them.
		if err := r.addUntracked(ctx, cs); err != nil {
			return nil, err
		}
		return cs, nil

	case ModeBranch:
		base := sel.Base
		if base == "" {
			base = r.DefaultBranch(ctx)
		}
		if base == "" {
			return nil, fmt.Errorf("could not determine a base branch; pass --base explicitly")
		}
		head := sel.Head
		if head == "" {
			head = "HEAD"
		}
		return r.rangeChangeSet(ctx, base, head, true)
	}
	return nil, fmt.Errorf("unsupported selection mode %q", mode)
}

// autoMode implements the default behaviour of `codewalk pr` with no
// arguments: explain the branch when it has commits of its own, otherwise
// explain uncommitted work.
func (r *Repo) autoMode(ctx context.Context, base string) (SelectorMode, error) {
	if base == "" {
		base = r.DefaultBranch(ctx)
	}
	if base != "" {
		if mb, err := r.MergeBase(ctx, base, "HEAD"); err == nil {
			head, err := r.Resolve(ctx, "HEAD")
			if err == nil && mb != head {
				return ModeBranch, nil
			}
		}
	}
	if dirty, _ := r.HasUncommittedChanges(ctx); dirty {
		return ModeWorkingTree, nil
	}
	if base == "" {
		return "", fmt.Errorf("could not determine what to explain: no base branch was found and the working tree is clean; pass a revision range or --base")
	}
	return "", fmt.Errorf("could not determine what to explain: %s matches %s and the working tree is clean; pass a revision range explicitly", r.describeHead(ctx), base)
}

func (r *Repo) describeHead(ctx context.Context) string {
	if b := r.CurrentBranch(ctx); b != "" {
		return b
	}
	return "HEAD"
}

// HasUncommittedChanges reports whether the working tree differs from HEAD,
// ignoring untracked files.
func (r *Repo) HasUncommittedChanges(ctx context.Context) (bool, error) {
	out, err := r.Git(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// changeSetFromSpec handles "a..b", "a...b" and single-commit specs.
func (r *Repo) changeSetFromSpec(ctx context.Context, spec string) (*ChangeSet, error) {
	if i := strings.Index(spec, "..."); i >= 0 {
		return r.rangeChangeSet(ctx, spec[:i], spec[i+3:], true)
	}
	if i := strings.Index(spec, ".."); i >= 0 {
		return r.rangeChangeSet(ctx, spec[:i], spec[i+2:], false)
	}
	commit, err := r.Resolve(ctx, spec)
	if err != nil {
		return nil, err
	}
	// A root commit has no parent, so it is compared against the empty tree.
	baseCommit := emptyTreeHash
	if parent, err := r.Resolve(ctx, commit+"^"); err == nil {
		baseCommit = parent
	}
	cs := &ChangeSet{
		Mode:       ModeCommit,
		Base:       shorten(baseCommit),
		Head:       spec,
		BaseCommit: baseCommit,
		HeadCommit: commit,
		Branch:     r.CurrentBranch(ctx),
	}
	if err := r.fillFiles(ctx, cs, []string{"diff", "-M", baseCommit, commit}); err != nil {
		return nil, err
	}
	cs.Commits = r.commitsBetween(ctx, baseCommit, commit)
	return cs, nil
}

// emptyTreeHash is git's well-known empty tree object, used to diff root commits.
const emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func (r *Repo) rangeChangeSet(ctx context.Context, base, head string, useMergeBase bool) (*ChangeSet, error) {
	base, head = strings.TrimSpace(base), strings.TrimSpace(head)
	if base == "" || head == "" {
		return nil, fmt.Errorf("revision range needs both a base and a head")
	}
	headCommit, err := r.Resolve(ctx, head)
	if err != nil {
		return nil, err
	}
	baseCommit, err := r.Resolve(ctx, base)
	if err != nil {
		return nil, err
	}
	mode := ModeRange
	if useMergeBase {
		if mb, err := r.MergeBase(ctx, baseCommit, headCommit); err == nil {
			baseCommit = mb
			mode = ModeBranch
		}
	}
	displayHead := head
	if head == "HEAD" {
		if b := r.CurrentBranch(ctx); b != "" {
			displayHead = b
		}
	}
	cs := &ChangeSet{
		Mode:       mode,
		Base:       base,
		Head:       displayHead,
		BaseCommit: baseCommit,
		HeadCommit: headCommit,
		Branch:     r.CurrentBranch(ctx),
	}
	if err := r.fillFiles(ctx, cs, []string{"diff", "-M", baseCommit, headCommit}); err != nil {
		return nil, err
	}
	cs.Commits = r.commitsBetween(ctx, baseCommit, headCommit)
	return cs, nil
}

func (r *Repo) commitsBetween(ctx context.Context, base, head string) []Commit {
	out, err := r.Git(ctx, "log", "--format=%H%x1f%h%x1f%s%x1f%cs", "--max-count=100", base+".."+head)
	if err != nil {
		return nil
	}
	var commits []Commit
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		parts := strings.Split(l, "\x1f")
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, Commit{Hash: parts[0], Short: parts[1], Subject: parts[2], Date: parts[3]})
	}
	return commits
}

// fillFiles populates the changed-file list and statistics using the given git
// diff invocation.
func (r *Repo) fillFiles(ctx context.Context, cs *ChangeSet, diffArgs []string) error {
	numstat, err := r.Git(ctx, append(append([]string{}, diffArgs...), "--numstat", "-z")...)
	if err != nil {
		return err
	}
	status, err := r.Git(ctx, append(append([]string{}, diffArgs...), "--name-status", "-z")...)
	if err != nil {
		return err
	}
	statuses := parseNameStatusZ(status)
	files := parseNumstatZ(numstat)
	for i := range files {
		if st, ok := statuses[files[i].Path]; ok {
			files[i].Status = st.status
			files[i].OldPath = st.oldPath
		}
		files[i].Generated = LooksGenerated(files[i].Path)
		cs.Stats.Insertions += files[i].Insertions
		cs.Stats.Deletions += files[i].Deletions
	}
	cs.Files = files
	cs.Stats.FilesChanged = len(files)
	return nil
}

// addUntracked appends untracked, non-ignored files to a working-tree change.
func (r *Repo) addUntracked(ctx context.Context, cs *ChangeSet) error {
	out, err := r.Git(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, p := range splitZ(out) {
		f := walkthrough.ChangedFile{Path: p, Status: "added", Generated: LooksGenerated(p)}
		data, err := os.ReadFile(filepath.Join(r.Root, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		if isBinary(data) {
			f.Binary = true
		} else {
			f.Insertions = strings.Count(string(data), "\n")
			if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
				f.Insertions++
			}
		}
		cs.Files = append(cs.Files, f)
		cs.Stats.FilesChanged++
		cs.Stats.Insertions += f.Insertions
	}
	return nil
}

// isBinary reports whether content looks binary, using the same NUL-byte
// heuristic git itself applies.
func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

type nameStatus struct {
	status  string
	oldPath string
}

func parseNameStatusZ(out string) map[string]nameStatus {
	res := map[string]nameStatus{}
	tokens := splitZ(out)
	for i := 0; i < len(tokens); {
		code := tokens[i]
		i++
		if code == "" {
			continue
		}
		switch code[0] {
		case 'R', 'C':
			if i+1 >= len(tokens) {
				return res
			}
			old, newPath := tokens[i], tokens[i+1]
			i += 2
			kind := "renamed"
			if code[0] == 'C' {
				kind = "copied"
			}
			res[newPath] = nameStatus{status: kind, oldPath: old}
		default:
			if i >= len(tokens) {
				return res
			}
			p := tokens[i]
			i++
			res[p] = nameStatus{status: statusName(code[0])}
		}
	}
	return res
}

func statusName(c byte) string {
	switch c {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'M':
		return "modified"
	case 'T':
		return "type-changed"
	default:
		return "modified"
	}
}

func parseNumstatZ(out string) []walkthrough.ChangedFile {
	var files []walkthrough.ChangedFile
	tokens := splitZ(out)
	for i := 0; i < len(tokens); {
		fields := strings.Split(tokens[i], "\t")
		if len(fields) < 3 {
			i++
			continue
		}
		adds, dels := fields[0], fields[1]
		f := walkthrough.ChangedFile{Status: "modified"}
		if adds == "-" || dels == "-" {
			f.Binary = true
		} else {
			f.Insertions, _ = strconv.Atoi(adds)
			f.Deletions, _ = strconv.Atoi(dels)
		}
		if strings.TrimSpace(fields[2]) == "" {
			// Rename entry: the old and new paths follow as separate tokens.
			if i+2 < len(tokens) {
				f.OldPath = tokens[i+1]
				f.Path = tokens[i+2]
				f.Status = "renamed"
				i += 3
			} else {
				i = len(tokens)
			}
		} else {
			f.Path = fields[2]
			i++
		}
		if f.Path != "" {
			files = append(files, f)
		}
	}
	return files
}

func splitZ(s string) []string {
	parts := strings.Split(s, "\x00")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// generatedDirs and generatedNames capture the paths that are almost always
// machine-produced. Marking them lets the pipeline de-emphasise them instead of
// spending walkthrough attention on them.
var generatedDirs = []string{
	"vendor/", "node_modules/", "third_party/", "dist/", "build/", ".next/",
	"target/debug/", "target/release/", "__pycache__/",
}

var generatedNames = map[string]bool{
	"go.sum": true, "package-lock.json": true, "yarn.lock": true,
	"pnpm-lock.yaml": true, "Cargo.lock": true, "poetry.lock": true,
	"composer.lock": true, "Gemfile.lock": true, "uv.lock": true,
}

var generatedSuffixes = []string{
	".pb.go", "_pb2.py", ".pb.cc", ".pb.h", ".generated.go", ".generated.ts",
	".min.js", ".min.css", ".g.dart", "_generated.go", ".snap",
}

// LooksGenerated reports whether a path is very likely machine-generated.
func LooksGenerated(p string) bool {
	p = strings.TrimPrefix(path.Clean(p), "./")
	if generatedNames[path.Base(p)] {
		return true
	}
	for _, d := range generatedDirs {
		if strings.HasPrefix(p, d) || strings.Contains(p, "/"+d) {
			return true
		}
	}
	for _, s := range generatedSuffixes {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	return false
}

// Diff returns the textual diff for a change set.
//
// Generated files are excluded unless includeGenerated is set, and the output is
// bounded both per file and in total, so no change can exhaust a model's context
// window. Truncation is always announced in the output: a model must never
// reason over a partial diff believing it is complete.
func (r *Repo) Diff(ctx context.Context, cs *ChangeSet, includeGenerated bool, maxBytesPerFile, maxBytesTotal int) (string, error) {
	var b strings.Builder
	for i, f := range cs.Files {
		if maxBytesTotal > 0 && b.Len() >= maxBytesTotal {
			fmt.Fprintf(&b, "\n... %d of %d files omitted: the diff exceeded %d bytes. "+
				"Use the file_diff tool to read any of the remaining files:\n", len(cs.Files)-i, len(cs.Files), maxBytesTotal)
			for _, remaining := range cs.Files[i:] {
				fmt.Fprintf(&b, "  %s %s (+%d/-%d)\n", remaining.Status, remaining.Path, remaining.Insertions, remaining.Deletions)
			}
			break
		}
		if f.Binary {
			fmt.Fprintf(&b, "diff --git a/%s b/%s\n(binary file, %s)\n\n", f.Path, f.Path, f.Status)
			continue
		}
		if f.Generated && !includeGenerated {
			fmt.Fprintf(&b, "diff --git a/%s b/%s\n(generated file, %s, +%d/-%d lines, contents omitted)\n\n",
				f.Path, f.Path, f.Status, f.Insertions, f.Deletions)
			continue
		}
		d, err := r.FileDiff(ctx, cs, f.Path)
		if err != nil {
			continue
		}
		if maxBytesPerFile > 0 && len(d) > maxBytesPerFile {
			d = d[:maxBytesPerFile] + fmt.Sprintf("\n... diff truncated at %d bytes (file has +%d/-%d lines) ...\n", maxBytesPerFile, f.Insertions, f.Deletions)
		}
		b.WriteString(d)
		if !strings.HasSuffix(d, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// FileDiff returns the diff for a single path within a change set.
func (r *Repo) FileDiff(ctx context.Context, cs *ChangeSet, filePath string) (string, error) {
	args := []string{"diff", "-M"}
	switch cs.Mode {
	case ModeStaged:
		args = append(args, "--cached")
	case ModeWorkingTree:
		if r.isUntracked(ctx, filePath) {
			// An untracked file has no committed counterpart, so it is diffed
			// against an empty input rather than against HEAD.
			return r.Git(ctx, "diff", "-M", "--no-index", "--", os.DevNull, filePath)
		}
		args = append(args, "HEAD")
	default:
		args = append(args, cs.BaseCommit, cs.HeadCommit)
	}
	args = append(args, "--", filePath)
	return r.Git(ctx, args...)
}

// isUntracked reports whether a path exists in the working tree but not in the
// index.
func (r *Repo) isUntracked(ctx context.Context, filePath string) bool {
	out, err := r.Git(ctx, "ls-files", "--error-unmatch", "--", filePath)
	return err != nil || strings.TrimSpace(out) == ""
}

// HeadRev returns the revision that reference resolution should use for the
// post-change state.
func (cs *ChangeSet) HeadRev() string {
	if cs.HeadCommit == "" {
		return WorkingTree
	}
	return cs.HeadCommit
}
