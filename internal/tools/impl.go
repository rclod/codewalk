package tools

import (
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/rclod/codewalk/internal/gitrepo"
)

func (r *Repo) listFiles(ctx context.Context, dir, glob string, limit int) (string, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	if dir != "" {
		if _, err := r.safePath(dir); err != nil {
			return "", err
		}
	}
	args := []string{"ls-files", "-z"}
	if dir != "" {
		args = append(args, "--", dir)
	}
	out, err := r.repo.Git(ctx, args...)
	if err != nil {
		return "", err
	}
	var matches []string
	total := 0
	for _, f := range strings.Split(out, "\x00") {
		if f == "" {
			continue
		}
		if glob != "" && !matchGlob(glob, f) {
			continue
		}
		total++
		if len(matches) < limit {
			matches = append(matches, f)
		}
	}
	if total == 0 {
		return wrap("file_list", attrs("dir", dir, "glob", glob), "(no tracked files match)"), nil
	}
	body := strings.Join(matches, "\n")
	if total > len(matches) {
		body += fmt.Sprintf("\n... %d more paths not shown ...", total-len(matches))
	}
	return wrap("file_list", attrs("dir", dir, "glob", glob, "count", strconv.Itoa(total)), body), nil
}

func (r *Repo) readFile(ctx context.Context, rel, rev string, start, end int) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("read_file requires a path")
	}
	if _, err := r.safePath(rel); err != nil {
		return "", err
	}
	rel = filepathToSlash(rel)

	content, err := r.repo.FileAt(ctx, rev, rel)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file %q does not exist at %s", rel, revLabel(rev))
		}
		return "", fmt.Errorf("could not read %q at %s: %v", rel, revLabel(rev), err)
	}
	r.noteRead(rel)

	lines := strings.Split(content, "\n")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return "", fmt.Errorf("file %q has %d lines; start_line %d is past the end", rel, len(lines), start)
	}
	var b strings.Builder
	for i := start - 1; i < end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
	}
	body := truncate(b.String(), r.opts.MaxFileBytes, "file content")
	return wrap("file", attrs("path", rel, "rev", revLabel(rev),
		"lines", fmt.Sprintf("%d-%d of %d", start, end, len(lines))), body), nil
}

func revLabel(rev string) string {
	if rev == "" || rev == gitrepo.WorkingTree {
		return "working tree"
	}
	return rev
}

func (r *Repo) search(ctx context.Context, pattern, glob string, literal bool, limit int) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("search requires a pattern")
	}
	if limit <= 0 || limit > 500 {
		limit = r.opts.MaxSearchResults
	}
	if literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regular expression: %v", err)
	}

	matches, truncatedAt, err := r.grep(ctx, re, glob, limit)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return wrap("search_results", attrs("pattern", pattern, "glob", glob),
			"(no matches — the pattern may be wrong, or the behaviour may live elsewhere)"), nil
	}
	body := strings.Join(matches, "\n")
	if truncatedAt {
		body += "\n... more matches exist; narrow the pattern or add a glob filter ..."
	}
	return wrap("search_results", attrs("pattern", pattern, "glob", glob, "shown", strconv.Itoa(len(matches))), body), nil
}

// grep searches tracked files. ripgrep is used when present purely for speed.
func (r *Repo) grep(ctx context.Context, re *regexp.Regexp, glob string, limit int) ([]string, bool, error) {
	out, err := r.repo.Git(ctx, "ls-files", "-z")
	if err != nil {
		return nil, false, err
	}
	var results []string
	for _, f := range strings.Split(out, "\x00") {
		if f == "" || gitrepo.LooksGenerated(f) {
			continue
		}
		if glob != "" && !matchGlob(glob, f) {
			continue
		}
		abs, err := r.safePath(f)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() || info.Size() > int64(4<<20) {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil || isBinary(data) {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				if len(line) > 300 {
					line = line[:300] + "…"
				}
				results = append(results, fmt.Sprintf("%s:%d: %s", f, i+1, strings.TrimRight(line, "\r")))
				if len(results) >= limit {
					return results, true, nil
				}
			}
		}
	}
	return results, false, nil
}

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

// definitionPatterns are language-aware heuristics for "where is this defined".
// They are deliberately approximate: results are evidence for an agent to
// confirm, not an authoritative index.
func definitionPatterns(symbol string) string {
	s := regexp.QuoteMeta(symbol)
	alternatives := []string{
		`func\s+(\([^)]*\)\s*)?` + s + `\b`,                                       // Go
		`(type|interface|struct)\s+` + s + `\b`,                                   // Go types
		`(class|interface|enum|trait|struct|record)\s+` + s + `\b`,                // OO languages
		`def\s+` + s + `\b`,                                                       // Python, Ruby
		`(function|const|let|var)\s+` + s + `\b`,                                  // JavaScript, TypeScript
		s + `\s*[:=]\s*(async\s*)?(function|\()`,                                  // JS assignment forms
		`(fn|impl)\s+` + s + `\b`,                                                 // Rust
		`(public|private|protected|static|final|\s)+[\w<>\[\]]+\s+` + s + `\s*\(`, // Java, C#
	}
	return "(?m)^\\s*(" + strings.Join(alternatives, "|") + ")"
}

func (r *Repo) findDefinition(ctx context.Context, symbol string, limit int) (string, error) {
	if symbol == "" {
		return "", fmt.Errorf("find_definition requires a symbol")
	}
	// A qualified name such as OrderService.Create is searched by its last part.
	short := symbol
	if i := strings.LastIndexAny(short, ".:#"); i >= 0 && i < len(short)-1 {
		short = short[i+1:]
	}
	re, err := regexp.Compile(definitionPatterns(short))
	if err != nil {
		return "", err
	}
	matches, _, err := r.grep(ctx, re, "", limit)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return wrap("definitions", attrs("symbol", symbol),
			"(no definition found — the symbol may be generated, imported from a dependency, or named differently)"), nil
	}
	return wrap("definitions", attrs("symbol", symbol), strings.Join(matches, "\n")), nil
}

func (r *Repo) changedFiles() (string, error) {
	if r.change == nil {
		return "", fmt.Errorf("changed_files is only available when explaining a change")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Selection: %s\n", r.change.Describe())
	if r.change.BaseCommit != "" {
		fmt.Fprintf(&b, "Base: %s (%s)\nHead: %s\n", r.change.Base, shortHash(r.change.BaseCommit), r.change.Head)
	}
	fmt.Fprintf(&b, "%d files changed, %d insertions(+), %d deletions(-)\n\n",
		r.change.Stats.FilesChanged, r.change.Stats.Insertions, r.change.Stats.Deletions)
	for _, f := range r.change.Files {
		line := fmt.Sprintf("%-9s +%-5d -%-5d %s", f.Status, f.Insertions, f.Deletions, f.Path)
		if f.OldPath != "" {
			line += " (from " + f.OldPath + ")"
		}
		if f.Generated {
			line += "  [likely generated]"
		}
		if f.Binary {
			line += "  [binary]"
		}
		b.WriteString(line + "\n")
	}
	if len(r.change.Commits) > 0 {
		b.WriteString("\nCommits (newest first):\n")
		for i, c := range r.change.Commits {
			if i >= 30 {
				fmt.Fprintf(&b, "... %d more commits ...\n", len(r.change.Commits)-i)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", c.Short, c.Subject)
		}
	}
	return wrap("change_summary", "", b.String()), nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func (r *Repo) fileDiff(ctx context.Context, rel string) (string, error) {
	if r.change == nil {
		return "", fmt.Errorf("file_diff is only available when explaining a change")
	}
	if rel == "" {
		return "", fmt.Errorf("file_diff requires a path")
	}
	if _, err := r.safePath(rel); err != nil {
		return "", err
	}
	d, err := r.repo.FileDiff(ctx, r.change, rel)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(d) == "" {
		return wrap("diff", attrs("path", rel), "(no diff — this path is not part of the change)"), nil
	}
	r.noteRead(rel)
	return wrap("diff", attrs("path", rel), truncate(d, r.opts.MaxDiffBytes, "diff")), nil
}

func (r *Repo) gitLog(ctx context.Context, rel string, limit int) (string, error) {
	if !r.opts.AllowGitHistory {
		return "", fmt.Errorf("git history inspection is disabled for this run")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []string{"log", "--format=%h %cs %s", fmt.Sprintf("--max-count=%d", limit)}
	if rel != "" {
		if _, err := r.safePath(rel); err != nil {
			return "", err
		}
		args = append(args, "--", rel)
	}
	out, err := r.repo.Git(ctx, args...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return wrap("git_log", attrs("path", rel), "(no commits)"), nil
	}
	return wrap("git_log", attrs("path", rel), strings.TrimSpace(out)), nil
}

func (r *Repo) gitShow(ctx context.Context, rev, rel string) (string, error) {
	if !r.opts.AllowGitHistory {
		return "", fmt.Errorf("git history inspection is disabled for this run")
	}
	if rev == "" {
		return "", fmt.Errorf("git_show requires a revision")
	}
	args := []string{"show", "--format=%h %cs%n%s%n%n%b", "--patch", rev}
	if rel != "" {
		if _, err := r.safePath(rel); err != nil {
			return "", err
		}
		args = append(args, "--", rel)
	}
	out, err := r.repo.Git(ctx, args...)
	if err != nil {
		return "", err
	}
	return wrap("commit", attrs("rev", rev, "path", rel), truncate(out, r.opts.MaxDiffBytes, "commit")), nil
}

func attrs(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", kv[i], kv[i+1]))
	}
	return strings.Join(parts, " ")
}

func filepathToSlash(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(path.Clean(strings.ReplaceAll(p, "\\", "/")), "//", "/"), "./")
}

// matchGlob supports the common glob forms used in practice, including the
// "**/" prefix that path.Match does not handle.
func matchGlob(pattern, name string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if strings.HasPrefix(pattern, "**/") {
		suffix := pattern[3:]
		if ok, _ := path.Match(suffix, path.Base(name)); ok {
			return true
		}
		return globMatchSegments(suffix, name)
	}
	if ok, _ := path.Match(pattern, name); ok {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if ok, _ := path.Match(pattern, path.Base(name)); ok {
			return true
		}
	}
	return globMatchSegments(pattern, name)
}

// globMatchSegments expands an embedded "**" into a match against any number of
// intermediate directories.
func globMatchSegments(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		return false
	}
	parts := strings.SplitN(pattern, "**", 2)
	prefix, suffix := parts[0], strings.TrimPrefix(parts[1], "/")
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := strings.TrimPrefix(name, prefix)
	for {
		if ok, _ := path.Match(suffix, rest); ok {
			return true
		}
		i := strings.Index(rest, "/")
		if i < 0 {
			return false
		}
		rest = rest[i+1:]
	}
}

// SortedInvocationSummary aggregates tool usage for run metrics.
func SortedInvocationSummary(invocations []Invocation) map[string]int {
	counts := map[string]int{}
	for _, inv := range invocations {
		counts[inv.Tool]++
	}
	return counts
}
