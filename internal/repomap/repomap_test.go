package repomap

import "testing"

func TestFromFileListClassifiesATree(t *testing.T) {
	m := FromFileList("checkout", "", []string{
		"go.mod",
		"README.md",
		"docs/architecture.md",
		"cmd/api/main.go",
		"cmd/worker/main.go",
		"internal/orders/service.go",
		"internal/orders/service_test.go",
		"internal/orders/handler.go",
		"internal/payments/client.go",
		"migrations/001_orders.sql",
		".github/workflows/ci.yml",
		"web/static/app.js",
		"node_modules/left-pad/index.js",
	})

	if m.FileCount != 13 {
		t.Errorf("file count = %d", m.FileCount)
	}
	if m.Languages[0].Language != "Go" {
		t.Errorf("dominant language = %v, want Go", m.Languages[0])
	}
	if !contains(m.Manifests, "go.mod") {
		t.Errorf("manifests = %v", m.Manifests)
	}
	if !contains(m.EntryPointCandidates, "cmd/api/main.go") || !contains(m.EntryPointCandidates, "cmd/worker/main.go") {
		t.Errorf("entry points = %v", m.EntryPointCandidates)
	}
	if !contains(m.Docs, "docs/architecture.md") || !contains(m.Docs, "README.md") {
		t.Errorf("docs = %v", m.Docs)
	}
	if !contains(m.ConfigFiles, ".github/workflows/ci.yml") {
		t.Errorf("config files = %v", m.ConfigFiles)
	}
	if !contains(m.TestDirs, "internal/orders") {
		t.Errorf("test dirs = %v", m.TestDirs)
	}

	var vendored bool
	for _, d := range m.Directories {
		if d.Path == "node_modules/left-pad" && d.Generated {
			vendored = true
		}
	}
	if !vendored {
		t.Error("vendored directories should be marked so agents can skip them")
	}
}

func TestTextBriefingIsCompact(t *testing.T) {
	files := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		files = append(files, "internal/pkg"+string(rune('a'+i%26))+"/file.go")
	}
	m := FromFileList("large", "", files)
	text := m.Text()
	if len(text) > 6000 {
		t.Errorf("briefing is %d bytes; it must stay small enough to prepend to every stage", len(text))
	}
	if len(m.Directories) > 60 {
		t.Errorf("directory list = %d entries, want a bounded summary", len(m.Directories))
	}
}

func TestLanguageDetection(t *testing.T) {
	cases := map[string]string{
		"main.go": "Go", "app.tsx": "TypeScript", "index.js": "JavaScript",
		"model.py": "Python", "lib.rs": "Rust", "schema.sql": "SQL",
		"LICENSE": "", "data.bin": "",
	}
	for path, want := range cases {
		if got := Language(path); got != want {
			t.Errorf("Language(%q) = %q, want %q", path, got, want)
		}
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
