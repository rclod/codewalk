// Package gitrepo provides read-only access to a local Git repository.
//
// Everything in this package is observational: no command here mutates the
// repository under analysis. That is a product-level guarantee, so the
// allowlist of git subcommands is enforced in one place (runGit).
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotARepository is returned when no Git repository contains the given path.
var ErrNotARepository = errors.New("not a git repository")

// Repo is a local Git repository opened for read-only inspection.
type Repo struct {
	// Root is the absolute path of the repository working tree.
	Root string
	// Name is the working tree's directory name, used for display and cache
	// identity.
	Name string
	// RemoteHost is the host of the "origin" remote when one is configured
	// (for example "github.com"). The full URL is deliberately not retained.
	RemoteHost string
}

// readOnlyGitCommands is the complete set of git subcommands this package is
// permitted to run. Anything that could write to the repository is absent by
// construction.
var readOnlyGitCommands = map[string]bool{
	"rev-parse": true, "diff": true, "diff-tree": true, "status": true,
	"log": true, "show": true, "cat-file": true, "ls-files": true,
	"ls-tree": true, "merge-base": true, "symbolic-ref": true, "branch": true,
	"rev-list": true, "blame": true, "shortlog": true, "config": true,
	"describe": true, "for-each-ref": true, "name-rev": true,
}

// Discover locates the repository containing dir.
func Discover(dir string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", dir)
	}
	out, err := runGitIn(context.Background(), abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotARepository, abs)
	}
	root := strings.TrimSpace(out)
	r := &Repo{Root: root, Name: filepath.Base(root)}
	if url, err := runGitIn(context.Background(), root, "config", "--get", "remote.origin.url"); err == nil {
		r.RemoteHost = remoteHost(strings.TrimSpace(url))
	}
	return r, nil
}

// remoteHost extracts only the host from a remote URL. Paths, usernames and
// credentials embedded in the URL are discarded: codewalk has no use for them
// and they must not end up in persisted artifacts.
func remoteHost(url string) string {
	switch {
	case url == "":
		return ""
	case strings.Contains(url, "://"):
		rest := url[strings.Index(url, "://")+3:]
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		if i := strings.IndexAny(rest, "/:"); i >= 0 {
			rest = rest[:i]
		}
		return rest
	case strings.Contains(url, "@") && strings.Contains(url, ":"):
		// scp-style: user@host:path
		rest := url[strings.Index(url, "@")+1:]
		if i := strings.Index(rest, ":"); i >= 0 {
			rest = rest[:i]
		}
		return rest
	default:
		return ""
	}
}

// Git runs a read-only git command in the repository and returns stdout.
func (r *Repo) Git(ctx context.Context, args ...string) (string, error) {
	return runGitIn(ctx, r.Root, args...)
}

func runGitIn(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("no git subcommand given")
	}
	if !readOnlyGitCommands[args[0]] {
		return "", fmt.Errorf("git subcommand %q is not permitted: codewalk only reads repositories", args[0])
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Keep git deterministic and non-interactive regardless of user config.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"LC_ALL=C",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// `git diff` exits 1 when it finds differences, which is a normal
		// result rather than a failure. This matters for --no-index, used to
		// show files that are not tracked yet.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && args[0] == "diff" {
			return stdout.String(), nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

// CurrentBranch returns the checked-out branch name, or "" when the repository
// has a detached HEAD.
func (r *Repo) CurrentBranch(ctx context.Context) string {
	out, err := r.Git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	if b := strings.TrimSpace(out); b != "HEAD" {
		return b
	}
	return ""
}

// HasCommits reports whether HEAD resolves, which is false in a repository that
// has never been committed to.
func (r *Repo) HasCommits(ctx context.Context) bool {
	_, err := r.Git(ctx, "rev-parse", "--verify", "HEAD")
	return err == nil
}

// DefaultBranch determines the repository's base branch, preferring the remote
// HEAD symbolic ref and falling back to conventional names that actually exist.
func (r *Repo) DefaultBranch(ctx context.Context) string {
	if out, err := r.Git(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimSpace(out); b != "" {
			return b
		}
	}
	for _, candidate := range []string{"main", "master", "develop", "trunk"} {
		for _, ref := range []string{"refs/remotes/origin/" + candidate, "refs/heads/" + candidate} {
			if _, err := r.Git(ctx, "rev-parse", "--verify", "--quiet", ref); err == nil {
				return strings.TrimPrefix(ref, "refs/heads/")
			}
		}
	}
	return ""
}

// Resolve turns a revision expression into a full commit hash.
func (r *Repo) Resolve(ctx context.Context, rev string) (string, error) {
	out, err := r.Git(ctx, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve revision %q: %w", rev, err)
	}
	return strings.TrimSpace(out), nil
}

// MergeBase returns the best common ancestor of two revisions.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := r.Git(ctx, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// FileAt returns the contents of a repository-relative path at a revision. An
// empty revision reads the working tree.
func (r *Repo) FileAt(ctx context.Context, rev, path string) (string, error) {
	if rev == "" || rev == WorkingTree {
		data, err := os.ReadFile(filepath.Join(r.Root, filepath.FromSlash(path)))
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return r.Git(ctx, "show", rev+":"+path)
}

// WorkingTree is the pseudo-revision naming the uncommitted working tree.
const WorkingTree = "WORKTREE"

// ShortStat returns a one-line description of a commit, used when explaining
// what an implementation replaced.
func (r *Repo) CommitSubject(ctx context.Context, rev string) string {
	out, err := r.Git(ctx, "log", "-1", "--format=%s", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
