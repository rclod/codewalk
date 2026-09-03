package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(t *testing.T) (Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEWALK_HOME", home)
	t.Setenv("CODEWALK_CONFIG", filepath.Join(home, "config.toml"))
	t.Setenv("CODEWALK_RUNS_DIR", filepath.Join(home, "runs"))
	var stdout, stderr bytes.Buffer
	return Env{Stdout: &stdout, Stderr: &stderr, Workdir: t.TempDir()}, &stdout, &stderr
}

func TestUsageListsEveryCommand(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	for _, c := range commands() {
		if !strings.Contains(out.String(), c.name) {
			t.Errorf("usage does not mention %q", c.name)
		}
	}
	if !strings.Contains(out.String(), "not review or grade it") {
		t.Error("usage should state the product boundary: codewalk explains code, it does not grade it")
	}
}

func TestUnknownCommandExitsWithUsageCode(t *testing.T) {
	if code := Main([]string{"definitely-not-a-command"}); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestVersionCommand(t *testing.T) {
	env, stdout, _ := testEnv(t)
	if err := commandByName(t, "version").run(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "codewalk") {
		t.Errorf("version output = %q", stdout.String())
	}
}

func TestConfigInitWritesAUsableFile(t *testing.T) {
	env, stdout, _ := testEnv(t)
	if err := runConfig(context.Background(), env, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("CODEWALK_HOME"), "config.toml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	// The file will hold provider settings; it should not be world-readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config permissions = %o, want 0600", perm)
	}
	if !strings.Contains(stdout.String(), "ANTHROPIC_API_KEY") {
		t.Error("first-run output should explain how to supply a credential")
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "sk-") {
		t.Error("the starter config must not contain anything resembling a real key")
	}

	// A second init must not silently overwrite an edited configuration.
	stdout.Reset()
	if err := runConfig(context.Background(), env, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "already exists") {
		t.Error("a second init should refuse without --force")
	}
}

func TestConfigShowRedactsCredentials(t *testing.T) {
	env, stdout, _ := testEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-super-secret-value")
	if err := runConfig(context.Background(), env, []string{"show"}); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if strings.Contains(out, "sk-super-secret-value") {
		t.Fatal("config show leaked a credential")
	}
	if !strings.Contains(out, "ANTHROPIC_API_KEY (set)") {
		t.Errorf("config show should report whether a credential is present:\n%s", out)
	}
}

func TestConfigCheckFailsWithoutAnyBackend(t *testing.T) {
	env, _, _ := testEnv(t)
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "XAI_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("PATH", t.TempDir()) // no harness executables available
	if err := runConfig(context.Background(), env, []string{"check"}); err == nil {
		t.Error("config check should fail when nothing is usable, so the problem surfaces before a run")
	}
}

func TestPathsHonourEnvironmentOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEWALK_HOME", home)
	t.Setenv("CODEWALK_RUNS_DIR", "")
	if got, want := RunsDir(), filepath.Join(home, "runs"); got != want {
		t.Errorf("runs dir = %q, want %q", got, want)
	}
	t.Setenv("CODEWALK_RUNS_DIR", "/tmp/example-runs")
	if RunsDir() != "/tmp/example-runs" {
		t.Errorf("runs dir override ignored: %q", RunsDir())
	}
}

func TestChangeCommandRejectsConflictingSelectors(t *testing.T) {
	env, _, _ := testEnv(t)
	err := runChange(context.Background(), env, []string{"--staged", "--working-tree"})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("error = %v, want a clear conflict message", err)
	}
}

func TestFormatValidation(t *testing.T) {
	for _, format := range []string{"text", "markdown", "md", "json"} {
		if err := validFormat(format); err != nil {
			t.Errorf("format %q rejected: %v", format, err)
		}
	}
	if err := validFormat("pdf"); err == nil {
		t.Error("unknown formats should be rejected with a clear message")
	}
}

func TestAskRequiresAQuestion(t *testing.T) {
	env, _, _ := testEnv(t)
	if err := runAsk(context.Background(), env, []string{"latest"}); err == nil {
		t.Error("ask should require both a walkthrough and a question")
	}
}

func TestEvalUsageIsShownWithoutArguments(t *testing.T) {
	env, stdout, _ := testEnv(t)
	if err := runEval(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "codewalk eval") {
		t.Error("eval with no subcommand should print usage")
	}
}

func commandByName(t *testing.T, name string) command {
	t.Helper()
	for _, c := range commands() {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no command named %q", name)
	return command{}
}

func TestFlagsAreAcceptedAfterPositionalArguments(t *testing.T) {
	// Go's flag package stops at the first non-flag argument, which would make
	// `codewalk show latest --format markdown` silently ignore the format.
	env, _, _ := testEnv(t)
	fs := newFlagSet(env, "show", "")
	format := fs.String("format", "text", "")
	verbose := fs.Bool("verbose", false, "")

	positional, err := parseArgs(fs, []string{"latest", "--format", "markdown", "extra", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if *format != "markdown" || !*verbose {
		t.Errorf("flags after a positional argument were ignored: format=%q verbose=%v", *format, *verbose)
	}
	if len(positional) != 2 || positional[0] != "latest" || positional[1] != "extra" {
		t.Errorf("positional arguments = %v", positional)
	}
}
