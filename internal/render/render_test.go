package render_test

import (
	"strings"
	"testing"

	"github.com/rclod/codewalk/internal/render"
	"github.com/rclod/codewalk/internal/testutil"
)

func TestTerminalOutputContainsTheEssentials(t *testing.T) {
	w := testutil.SampleWalkthrough()
	var out strings.Builder
	if err := render.Terminal(&out, w, render.TerminalOptions{Width: 80}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		w.Title,
		"Order creation now persists the order",
		"How order creation worked before",
		"internal/orders/service.go",
		"Where to start reading",
		"Safe to skip for now",
		"Not established",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("terminal output is missing %q", want)
		}
	}
}

func TestTerminalHidesOptionalDetailByDefault(t *testing.T) {
	w := testutil.SampleWalkthrough()
	var out strings.Builder
	if err := render.Terminal(&out, w, render.TerminalOptions{Width: 80}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "Failed work is retried") {
		t.Error("deep dives should stay closed unless asked for")
	}
	if strings.Contains(text, "sequenceDiagram") {
		t.Error("diagram sources should not be dumped into the terminal by default")
	}
	if !strings.Contains(text, "deep dive available") {
		t.Error("the reader should be told that a deep dive exists")
	}

	var expanded strings.Builder
	if err := render.Terminal(&expanded, w, render.TerminalOptions{Width: 80, DeepDives: true, Diagrams: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expanded.String(), "Failed work is retried") {
		t.Error("--deep-dives should reveal deep dives")
	}
	if !strings.Contains(expanded.String(), "sequenceDiagram") {
		t.Error("--diagrams should reveal diagram sources")
	}
}

func TestTerminalWrapsToWidth(t *testing.T) {
	w := testutil.SampleWalkthrough()
	var out strings.Builder
	if err := render.Terminal(&out, w, render.TerminalOptions{Width: 60}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if width := len([]rune(line)); width > 80 {
			t.Errorf("line exceeds a reasonable wrap width (%d): %q", width, line)
		}
	}
}

func TestMarkdownOutputIsComplete(t *testing.T) {
	w := testutil.SampleWalkthrough()
	var out strings.Builder
	if err := render.Markdown(&out, w); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"# " + w.Title,
		"## Walkthrough",
		"### 1. How order creation worked before",
		"```mermaid",
		"<details>",
		"| Aspect | Before | After |",
		"## Where to start reading",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("markdown output is missing %q", want)
		}
	}
	if !strings.Contains(text, "not a code review") {
		t.Error("markdown export should state the product boundary")
	}
}

func TestMarkdownEscapesTableCells(t *testing.T) {
	w := testutil.SampleWalkthrough()
	w.BeforeAfter.Aspects[0].Before = "a | b"
	var out strings.Builder
	if err := render.Markdown(&out, w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `a \| b`) {
		t.Error("pipes in table cells should be escaped so the table still renders")
	}
}
