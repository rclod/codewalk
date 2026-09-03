package eval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// A benchmark case is a self-contained, publishable evaluation fixture:
//
//	benchmarks/cases/<case-id>/
//	  case.toml           what to explain, and what to expect
//	  understanding.json  the curated understanding model (optional)
//	  repo/001-<name>/    a snapshot of the repository, committed in order
//	  repo/002-<name>/
//
// Snapshots are plain directories rather than a packed git repository so that a
// case is reviewable in a pull request. The corpus ships synthetic repositories
// only: no private code, and no proprietary excerpts.

// Case is one benchmark case.
type Case struct {
	ID          string `toml:"-"`
	Dir         string `toml:"-"`
	Name        string `toml:"name"`
	Description string `toml:"description"`
	// Kind is "change" or "codebase".
	Kind     string   `toml:"kind"`
	Tags     []string `toml:"tags"`
	Selector string   `toml:"selector"`
	Base     string   `toml:"base"`
	Head     string   `toml:"head"`
	Focus    string   `toml:"focus"`
	Subtree  string   `toml:"subtree"`
	// ExpectedComplexity is the complexity level a good walkthrough should
	// assign. It is checked as an observation, not as a gate: reasonable
	// walkthroughs can differ by a level.
	ExpectedComplexity int `toml:"expected_complexity"`

	Understanding *UnderstandingModel `toml:"-"`
}

// WalkthroughKind maps the case kind onto the canonical kind.
func (c *Case) WalkthroughKind() walkthrough.Kind {
	if strings.EqualFold(c.Kind, "codebase") {
		return walkthrough.KindCodebase
	}
	return walkthrough.KindChange
}

// LoadCase reads a benchmark case directory.
func LoadCase(dir string) (*Case, error) {
	var c Case
	path := filepath.Join(dir, "case.toml")
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c.Dir = dir
	c.ID = filepath.Base(dir)
	if c.Name == "" {
		c.Name = c.ID
	}
	understandingPath := filepath.Join(dir, "understanding.json")
	if _, err := os.Stat(understandingPath); err == nil {
		u, err := LoadUnderstandingModel(understandingPath)
		if err != nil {
			return nil, err
		}
		u.CaseID = c.ID
		c.Understanding = u
	}
	return &c, nil
}

// LoadCorpus reads every case in a corpus directory.
func LoadCorpus(dir string) ([]*Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read corpus %s: %w", dir, err)
	}
	var cases []*Case
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		caseDir := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(caseDir, "case.toml")); err != nil {
			continue
		}
		c, err := LoadCase(caseDir)
		if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	if len(cases) == 0 {
		return nil, fmt.Errorf("no benchmark cases found in %s", dir)
	}
	return cases, nil
}

// Materialize builds the case's fixture repository inside destDir and returns
// the repository path.
//
// This is the one place in codewalk that creates git history. It writes only
// into a directory the caller owns — never into a repository under analysis.
func (c *Case) Materialize(destDir string) (string, error) {
	snapshotsDir := filepath.Join(c.Dir, "repo")
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return "", fmt.Errorf("case %s: %w", c.ID, err)
	}
	var snapshots []string
	for _, e := range entries {
		if e.IsDir() {
			snapshots = append(snapshots, e.Name())
		}
	}
	sort.Strings(snapshots)
	if len(snapshots) == 0 {
		return "", fmt.Errorf("case %s: no snapshots in %s", c.ID, snapshotsDir)
	}

	repoPath := filepath.Join(destDir, c.ID)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return "", err
	}
	if err := gitInit(repoPath); err != nil {
		return "", err
	}

	for i, name := range snapshots {
		if err := clearWorktree(repoPath); err != nil {
			return "", err
		}
		if err := copyTree(filepath.Join(snapshotsDir, name), repoPath); err != nil {
			return "", err
		}
		message := snapshotMessage(name)
		if err := gitCommitAll(repoPath, message, i); err != nil {
			return "", err
		}
		tag := fmt.Sprintf("snapshot-%03d", i+1)
		if err := runGit(repoPath, "tag", "-f", tag); err != nil {
			return "", err
		}
	}
	return repoPath, nil
}

// snapshotMessage derives a commit message from a snapshot directory name such
// as "002-add-outbox-worker".
func snapshotMessage(name string) string {
	parts := strings.SplitN(name, "-", 2)
	if len(parts) == 2 {
		return strings.ReplaceAll(parts[1], "-", " ")
	}
	return name
}

// benchmarkIdentity is a deliberately fictional committer identity. Benchmark
// fixtures must not carry anyone's real name or address.
var benchmarkIdentity = []string{
	"GIT_AUTHOR_NAME=codewalk benchmarks",
	"GIT_AUTHOR_EMAIL=benchmarks@example.com",
	"GIT_COMMITTER_NAME=codewalk benchmarks",
	"GIT_COMMITTER_EMAIL=benchmarks@example.com",
}

func gitInit(dir string) error {
	if err := runGit(dir, "init", "--quiet", "--initial-branch=main"); err != nil {
		return err
	}
	if err := runGit(dir, "config", "user.name", "codewalk benchmarks"); err != nil {
		return err
	}
	return runGit(dir, "config", "user.email", "benchmarks@example.com")
}

func gitCommitAll(dir, message string, index int) error {
	if err := runGit(dir, "add", "-A"); err != nil {
		return err
	}
	// Fixed timestamps keep fixture repositories byte-identical across machines.
	date := fmt.Sprintf("2024-01-%02d 12:00:00 +0000", index+1)
	cmd := exec.Command("git", "commit", "--quiet", "--allow-empty", "-m", message)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), benchmarkIdentity...),
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), benchmarkIdentity...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return nil
}

// clearWorktree removes tracked content so the next snapshot replaces rather
// than merges with the previous one.
func clearWorktree(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
