// Package refcheck validates walkthrough code references against a repository.
//
// This is deterministic verification: a reference either resolves or it does
// not. Running it inside the pipeline keeps fabricated paths out of a
// walkthrough, and running it inside the evaluator keeps grounding scores
// honest without spending a model call on something a file system can answer.
package refcheck

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// Severity classifies a reference problem.
type Severity string

const (
	// SeverityBroken means the reference cannot be followed at all.
	SeverityBroken Severity = "broken"
	// SeverityWeak means the reference resolves but is imprecise, for example a
	// symbol that does not appear in the file it points at.
	SeverityWeak Severity = "weak"
)

// Issue is one invalid or imprecise reference.
type Issue struct {
	Ref      walkthrough.CodeReference `json:"ref"`
	Problem  string                    `json:"problem"`
	Severity Severity                  `json:"severity"`
}

func (i Issue) String() string {
	loc := i.Ref.Path
	if i.Ref.Symbol != "" {
		loc += " (" + i.Ref.Symbol + ")"
	}
	return loc + ": " + i.Problem
}

// Checker resolves references against specific revisions of a repository.
type Checker struct {
	repo    *gitrepo.Repo
	headRev string
	baseRev string

	mu    sync.Mutex
	cache map[string][]string // "rev\x00path" -> lines
}

// New creates a checker. headRev is the post-change state (which may be
// gitrepo.WorkingTree) and baseRev the pre-change state; baseRev may be empty
// for codebase walkthroughs.
func New(repo *gitrepo.Repo, headRev, baseRev string) *Checker {
	return &Checker{repo: repo, headRev: headRev, baseRev: baseRev, cache: map[string][]string{}}
}

func (c *Checker) revFor(ref walkthrough.CodeReference) string {
	if ref.Rev != "" {
		return ref.Rev
	}
	if ref.Side == "before" && c.baseRev != "" {
		return c.baseRev
	}
	return c.headRev
}

func (c *Checker) lines(ctx context.Context, rev, path string) ([]string, error) {
	key := rev + "\x00" + path
	c.mu.Lock()
	if lines, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return lines, nil
	}
	c.mu.Unlock()

	content, err := c.repo.FileAt(ctx, rev, path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(content, "\n")
	c.mu.Lock()
	c.cache[key] = lines
	c.mu.Unlock()
	return lines, nil
}

// Check validates one reference, returning nil when it resolves cleanly.
func (c *Checker) Check(ctx context.Context, ref walkthrough.CodeReference) *Issue {
	if strings.TrimSpace(ref.Path) == "" {
		return &Issue{Ref: ref, Problem: "reference has no path", Severity: SeverityBroken}
	}
	rev := c.revFor(ref)
	lines, err := c.lines(ctx, rev, ref.Path)
	if err != nil {
		return &Issue{
			Ref:      ref,
			Problem:  fmt.Sprintf("file does not exist at %s", revLabel(rev)),
			Severity: SeverityBroken,
		}
	}
	if ref.StartLine > len(lines) {
		return &Issue{
			Ref:      ref,
			Problem:  fmt.Sprintf("start line %d is past the end of the file (%d lines)", ref.StartLine, len(lines)),
			Severity: SeverityBroken,
		}
	}
	if ref.EndLine > len(lines) {
		return &Issue{
			Ref:      ref,
			Problem:  fmt.Sprintf("end line %d is past the end of the file (%d lines)", ref.EndLine, len(lines)),
			Severity: SeverityWeak,
		}
	}
	if sym := shortSymbol(ref.Symbol); sym != "" {
		if !containsSymbol(lines, sym) {
			return &Issue{
				Ref:      ref,
				Problem:  fmt.Sprintf("symbol %q does not appear in the file at %s", ref.Symbol, revLabel(rev)),
				Severity: SeverityBroken,
			}
		}
		if ref.StartLine > 0 && ref.EndLine >= ref.StartLine {
			if !containsSymbol(lines[ref.StartLine-1:min(ref.EndLine, len(lines))], sym) {
				return &Issue{
					Ref:      ref,
					Problem:  fmt.Sprintf("symbol %q exists in the file but not within lines %d-%d", ref.Symbol, ref.StartLine, ref.EndLine),
					Severity: SeverityWeak,
				}
			}
		}
	}
	return nil
}

// CheckAll validates a set of references, de-duplicating identical problems.
func (c *Checker) CheckAll(ctx context.Context, refs []walkthrough.CodeReference) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	for _, ref := range refs {
		issue := c.Check(ctx, ref)
		if issue == nil {
			continue
		}
		key := issue.Ref.Path + "|" + issue.Ref.Symbol + "|" + issue.Problem
		if seen[key] {
			continue
		}
		seen[key] = true
		issues = append(issues, *issue)
	}
	return issues
}

// Prune removes broken references from a walkthrough in place and returns the
// issues that were removed. A reference a reader cannot follow is worse than no
// reference, so the pipeline drops them rather than shipping them.
func (c *Checker) Prune(ctx context.Context, w *walkthrough.Walkthrough) []Issue {
	var removed []Issue
	filter := func(refs []walkthrough.CodeReference) []walkthrough.CodeReference {
		kept := refs[:0]
		for _, ref := range refs {
			if issue := c.Check(ctx, ref); issue != nil && issue.Severity == SeverityBroken {
				removed = append(removed, *issue)
				continue
			}
			kept = append(kept, ref)
		}
		return kept
	}
	w.StartHere = filter(w.StartHere)
	for i := range w.Concepts {
		w.Concepts[i].CodeRefs = filter(w.Concepts[i].CodeRefs)
	}
	for i := range w.Components {
		w.Components[i].CodeRefs = filter(w.Components[i].CodeRefs)
	}
	for i := range w.Steps {
		w.Steps[i].CodeRefs = filter(w.Steps[i].CodeRefs)
		if dd := w.Steps[i].DeepDive; dd != nil {
			dd.CodeRefs = filter(dd.CodeRefs)
		}
	}
	for i := range w.Flows {
		for j := range w.Flows[i].Steps {
			if ref := w.Flows[i].Steps[j].CodeRef; ref != nil {
				if issue := c.Check(ctx, *ref); issue != nil && issue.Severity == SeverityBroken {
					removed = append(removed, *issue)
					w.Flows[i].Steps[j].CodeRef = nil
				}
			}
		}
	}
	return removed
}

// CheckFiles verifies that a set of repository-relative paths exist at the head
// revision. It is used to validate component file lists.
func (c *Checker) CheckFiles(ctx context.Context, paths []string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := c.lines(ctx, c.headRev, p); err != nil {
			// A file removed by the change legitimately exists only in the base.
			if c.baseRev == "" {
				missing = append(missing, p)
				continue
			}
			if _, err := c.lines(ctx, c.baseRev, p); err != nil {
				missing = append(missing, p)
			}
		}
	}
	return missing
}

// shortSymbol reduces a qualified symbol such as "OrderService.Create" or
// "orders::create" to the part that is expected to appear literally in source.
func shortSymbol(sym string) string {
	sym = strings.TrimSpace(sym)
	if sym == "" {
		return ""
	}
	sym = strings.TrimSuffix(sym, "()")
	if i := strings.LastIndexAny(sym, ".:#/"); i >= 0 && i < len(sym)-1 {
		sym = sym[i+1:]
	}
	return sym
}

func containsSymbol(lines []string, sym string) bool {
	for _, l := range lines {
		if strings.Contains(l, sym) {
			return true
		}
	}
	return false
}

func revLabel(rev string) string {
	if rev == "" || rev == gitrepo.WorkingTree {
		return "the working tree"
	}
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
