package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rclod/codewalk/internal/gitrepo"
)

// readRepoFile reads a repository file for the UI, enforcing the same
// containment rule the agent tools use: nothing outside the repository root.
func readRepoFile(ctx context.Context, repo *gitrepo.Repo, rev, rel string) (string, error) {
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "/")
	clean := filepath.Clean(filepath.Join(repo.Root, filepath.FromSlash(rel)))
	if clean != repo.Root && !strings.HasPrefix(clean, repo.Root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the repository", rel)
	}
	content, err := repo.FileAt(ctx, rev, filepath.ToSlash(rel))
	if err != nil {
		return "", fmt.Errorf("could not read %s: %w", rel, err)
	}
	const maxBytes = 400_000
	if len(content) > maxBytes {
		content = content[:maxBytes] + "\n... [file truncated] ...\n"
	}
	return content, nil
}
