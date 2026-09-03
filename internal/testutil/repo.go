// Package testutil builds fixtures for tests.
//
// Everything here is synthetic: fictional services, example.com addresses and
// generic paths. Nothing in a fixture comes from a real repository or a real
// person, so fixtures are safe to publish.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a temporary git repository built for a test.
type Repo struct {
	Dir string
	t   *testing.T
}

// NewRepo creates an empty git repository in a temporary directory.
func NewRepo(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	r := &Repo{Dir: dir, t: t}
	r.Git("init", "--quiet", "--initial-branch=main")
	r.Git("config", "user.name", "codewalk tests")
	r.Git("config", "user.email", "tests@example.com")
	r.Git("config", "commit.gpgsign", "false")
	return r
}

// Git runs a git command in the repository and fails the test on error.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=codewalk tests",
		"GIT_AUTHOR_EMAIL=tests@example.com",
		"GIT_COMMITTER_NAME=codewalk tests",
		"GIT_COMMITTER_EMAIL=tests@example.com",
		"GIT_AUTHOR_DATE=2024-01-01T12:00:00Z",
		"GIT_COMMITTER_DATE=2024-01-01T12:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// Write creates or replaces a file.
func (r *Repo) Write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.Dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Remove deletes a file.
func (r *Repo) Remove(path string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.Dir, filepath.FromSlash(path))); err != nil {
		r.t.Fatal(err)
	}
}

// Commit stages everything and commits.
func (r *Repo) Commit(message string) string {
	r.t.Helper()
	r.Git("add", "-A")
	r.Git("commit", "--quiet", "--allow-empty", "-m", message)
	return strings.TrimSpace(r.Git("rev-parse", "HEAD"))
}

// Branch creates and checks out a branch.
func (r *Repo) Branch(name string) {
	r.t.Helper()
	r.Git("checkout", "--quiet", "-b", name)
}

// Checkout switches to an existing ref.
func (r *Repo) Checkout(ref string) {
	r.t.Helper()
	r.Git("checkout", "--quiet", ref)
}

// SampleService writes a small synthetic service and commits it, giving tests a
// realistic tree to investigate.
func (r *Repo) SampleService() {
	r.Write("go.mod", "module example.com/orders\n\ngo 1.22\n")
	r.Write("README.md", "# Orders\n\nA fictional order service used in tests.\n")
	r.Write("cmd/api/main.go", `package main

// Entry point for the fictional orders API.
func main() {
	Serve()
}
`)
	r.Write("internal/orders/service.go", `package orders

// Service coordinates order creation.
type Service struct {
	store Store
}

// Create persists a new order.
func (s *Service) Create(customer string, total int) (string, error) {
	return s.store.Insert(customer, total)
}
`)
	r.Write("internal/orders/store.go", `package orders

// Store persists orders.
type Store interface {
	Insert(customer string, total int) (string, error)
}
`)
	r.Commit("add orders service")
}
