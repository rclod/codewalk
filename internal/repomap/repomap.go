// Package repomap builds a cheap, deterministic overview of a repository.
//
// The map is produced without any model call. It exists so that expensive
// agents start from a compact, accurate picture of the repository instead of
// spending tokens rediscovering the same structural facts on every run.
package repomap

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/rclod/codewalk/internal/gitrepo"
)

// Map is a structural summary of a repository at a specific revision.
type Map struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	FileCount  int    `json:"file_count"`

	// Languages counts tracked files per language, most common first.
	Languages []LanguageCount `json:"languages"`
	// Directories summarises the significant directories of the tree.
	Directories []DirectorySummary `json:"directories"`
	// Manifests are build and dependency descriptors, which identify the
	// project's ecosystem and entry points more reliably than directory names.
	Manifests []string `json:"manifests,omitempty"`
	// EntryPointCandidates are files that commonly start a program. They are
	// candidates only: the investigator confirms them against the source.
	EntryPointCandidates []string `json:"entry_point_candidates,omitempty"`
	// Docs are repository documents worth reading before the source.
	Docs []string `json:"docs,omitempty"`
	// ConfigFiles are runtime, deployment and CI descriptors.
	ConfigFiles []string `json:"config_files,omitempty"`
	// TestDirs are directories that look like test suites.
	TestDirs []string `json:"test_dirs,omitempty"`
}

// LanguageCount counts files of one language.
type LanguageCount struct {
	Language string `json:"language"`
	Files    int    `json:"files"`
}

// DirectorySummary describes one directory of the tree.
type DirectorySummary struct {
	Path      string `json:"path"`
	Files     int    `json:"files"`
	Languages string `json:"languages,omitempty"`
	Generated bool   `json:"generated,omitempty"`
}

// Build produces a map of the repository at rev. An empty rev maps the working
// tree.
func Build(ctx context.Context, repo *gitrepo.Repo, rev string) (*Map, error) {
	args := []string{"ls-files", "-z"}
	if rev != "" && rev != gitrepo.WorkingTree {
		args = []string{"ls-tree", "-r", "--name-only", "-z", rev}
	}
	out, err := repo.Git(ctx, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return FromFileList(repo.Name, rev, files), nil
}

// FromFileList builds a map from an explicit file list. It is separated from
// Build so the logic can be tested without a repository.
func FromFileList(repoName, rev string, files []string) *Map {
	m := &Map{Repository: repoName, Revision: rev, FileCount: len(files)}

	langCount := map[string]int{}
	dirFiles := map[string]int{}
	dirLangs := map[string]map[string]bool{}

	for _, f := range files {
		lang := Language(f)
		if lang != "" {
			langCount[lang]++
		}
		dir := path.Dir(f)
		if dir == "." {
			dir = "(root)"
		}
		dirFiles[dir]++
		if dirLangs[dir] == nil {
			dirLangs[dir] = map[string]bool{}
		}
		if lang != "" {
			dirLangs[dir][lang] = true
		}

		base := path.Base(f)
		switch {
		case manifestNames[base]:
			m.Manifests = append(m.Manifests, f)
		case isDoc(f):
			m.Docs = append(m.Docs, f)
		case isConfig(f):
			m.ConfigFiles = append(m.ConfigFiles, f)
		}
		if isEntryPoint(f) {
			m.EntryPointCandidates = append(m.EntryPointCandidates, f)
		}
		if isTestPath(f) {
			d := path.Dir(f)
			if !containsString(m.TestDirs, d) {
				m.TestDirs = append(m.TestDirs, d)
			}
		}
	}

	for lang, n := range langCount {
		m.Languages = append(m.Languages, LanguageCount{Language: lang, Files: n})
	}
	sort.Slice(m.Languages, func(i, j int) bool {
		if m.Languages[i].Files != m.Languages[j].Files {
			return m.Languages[i].Files > m.Languages[j].Files
		}
		return m.Languages[i].Language < m.Languages[j].Language
	})

	for dir, n := range dirFiles {
		var langs []string
		for l := range dirLangs[dir] {
			langs = append(langs, l)
		}
		sort.Strings(langs)
		if len(langs) > 3 {
			langs = langs[:3]
		}
		m.Directories = append(m.Directories, DirectorySummary{
			Path:      dir,
			Files:     n,
			Languages: strings.Join(langs, ", "),
			Generated: gitrepo.LooksGenerated(dir + "/"),
		})
	}
	sort.Slice(m.Directories, func(i, j int) bool {
		if m.Directories[i].Files != m.Directories[j].Files {
			return m.Directories[i].Files > m.Directories[j].Files
		}
		return m.Directories[i].Path < m.Directories[j].Path
	})
	if len(m.Directories) > 60 {
		m.Directories = m.Directories[:60]
	}
	sort.Strings(m.Manifests)
	sort.Strings(m.Docs)
	sort.Strings(m.ConfigFiles)
	sort.Strings(m.EntryPointCandidates)
	sort.Strings(m.TestDirs)
	m.Docs = capSlice(m.Docs, 40)
	m.ConfigFiles = capSlice(m.ConfigFiles, 40)
	m.EntryPointCandidates = capSlice(m.EntryPointCandidates, 40)
	m.TestDirs = capSlice(m.TestDirs, 30)
	return m
}

func capSlice(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var extLanguage = map[string]string{
	".go": "Go", ".rs": "Rust", ".py": "Python", ".rb": "Ruby", ".java": "Java",
	".kt": "Kotlin", ".swift": "Swift", ".c": "C", ".h": "C", ".cc": "C++",
	".cpp": "C++", ".hpp": "C++", ".cs": "C#", ".php": "PHP", ".ex": "Elixir",
	".exs": "Elixir", ".erl": "Erlang", ".scala": "Scala", ".clj": "Clojure",
	".ts": "TypeScript", ".tsx": "TypeScript", ".js": "JavaScript",
	".jsx": "JavaScript", ".mjs": "JavaScript", ".vue": "Vue", ".svelte": "Svelte",
	".sql": "SQL", ".sh": "Shell", ".bash": "Shell", ".tf": "Terraform",
	".proto": "Protobuf", ".graphql": "GraphQL", ".css": "CSS", ".scss": "CSS",
	".html": "HTML", ".dart": "Dart", ".lua": "Lua", ".zig": "Zig",
}

// Language maps a path to a language name, or "" when it is not source code.
func Language(p string) string {
	return extLanguage[strings.ToLower(path.Ext(p))]
}

var manifestNames = map[string]bool{
	"go.mod": true, "package.json": true, "Cargo.toml": true, "pyproject.toml": true,
	"setup.py": true, "requirements.txt": true, "Gemfile": true, "pom.xml": true,
	"build.gradle": true, "build.gradle.kts": true, "composer.json": true,
	"mix.exs": true, "Package.swift": true, "pubspec.yaml": true, "deno.json": true,
}

var configHints = []string{
	"dockerfile", "docker-compose", "kubernetes/", "k8s/", "helm/", ".github/workflows/",
	"terraform/", "makefile", "justfile", "procfile", "serverless.yml", "vercel.json",
	"fly.toml", "railway.json", ".env.example", "config/", "nginx.conf",
}

func isConfig(p string) bool {
	l := strings.ToLower(p)
	for _, h := range configHints {
		if strings.Contains(l, h) {
			return true
		}
	}
	ext := strings.ToLower(path.Ext(p))
	return ext == ".yaml" || ext == ".yml" || ext == ".toml" || ext == ".ini"
}

func isDoc(p string) bool {
	l := strings.ToLower(p)
	if strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".rst") || strings.HasSuffix(l, ".adoc") {
		return true
	}
	base := strings.ToLower(path.Base(p))
	return base == "readme" || base == "agents.md" || base == "design.md"
}

var entryPointNames = map[string]bool{
	"main.go": true, "main.py": true, "main.rs": true, "app.py": true,
	"index.js": true, "index.ts": true, "server.js": true, "server.ts": true,
	"main.ts": true, "main.js": true, "application.java": true, "app.rb": true,
	"manage.py": true, "wsgi.py": true, "asgi.py": true, "cli.py": true,
	"__main__.py": true, "mod.rs": false,
}

func isEntryPoint(p string) bool {
	base := strings.ToLower(path.Base(p))
	if entryPointNames[base] {
		return true
	}
	dir := strings.ToLower(path.Dir(p))
	return strings.HasPrefix(dir, "cmd/") || dir == "cmd"
}

func isTestPath(p string) bool {
	l := strings.ToLower(p)
	switch {
	case strings.HasSuffix(l, "_test.go"), strings.HasSuffix(l, ".test.ts"),
		strings.HasSuffix(l, ".test.js"), strings.HasSuffix(l, ".spec.ts"),
		strings.HasSuffix(l, ".spec.js"), strings.HasSuffix(l, "_test.py"),
		strings.HasSuffix(l, "_spec.rb"):
		return true
	}
	return strings.HasPrefix(l, "test/") || strings.HasPrefix(l, "tests/") ||
		strings.Contains(l, "/test/") || strings.Contains(l, "/tests/") ||
		strings.Contains(l, "/__tests__/") || strings.Contains(l, "/spec/")
}

// Text renders the map as a compact briefing for a model prompt. It is
// deliberately terse: the map orients an agent, it does not replace
// investigation.
func (m *Map) Text() string {
	var b strings.Builder
	b.WriteString("Repository: " + m.Repository + "\n")
	b.WriteString("Tracked files: " + itoa(m.FileCount) + "\n")
	if len(m.Languages) > 0 {
		b.WriteString("Languages: ")
		for i, l := range m.Languages {
			if i >= 6 {
				break
			}
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(l.Language + " (" + itoa(l.Files) + ")")
		}
		b.WriteString("\n")
	}
	writeList := func(label string, items []string, limit int) {
		if len(items) == 0 {
			return
		}
		if len(items) > limit {
			items = items[:limit]
		}
		b.WriteString(label + ": " + strings.Join(items, ", ") + "\n")
	}
	writeList("Manifests", m.Manifests, 12)
	writeList("Entry point candidates", m.EntryPointCandidates, 15)
	writeList("Docs", m.Docs, 12)
	writeList("Config", m.ConfigFiles, 12)
	writeList("Test locations", m.TestDirs, 10)
	if len(m.Directories) > 0 {
		b.WriteString("Largest directories:\n")
		for i, d := range m.Directories {
			if i >= 25 {
				break
			}
			line := "  " + d.Path + " (" + itoa(d.Files) + " files"
			if d.Languages != "" {
				line += ", " + d.Languages
			}
			if d.Generated {
				line += ", likely generated"
			}
			b.WriteString(line + ")\n")
		}
	}
	return b.String()
}

func itoa(n int) string { return strconv.Itoa(n) }
