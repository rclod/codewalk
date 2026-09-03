// Package render presents a walkthrough for humans.
//
// Every renderer consumes the same canonical walkthrough, so the terminal, the
// Markdown export and the web UI cannot drift apart in what they consider a
// walkthrough to be.
package render

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rclod/codewalk/internal/walkthrough"
)

// TerminalOptions controls terminal rendering.
type TerminalOptions struct {
	Width int
	Color bool
	// DeepDives includes optional deep-dive content. It is off by default:
	// progressive disclosure is the point.
	DeepDives bool
	// Diagrams includes diagram sources, which are verbose in a terminal.
	Diagrams bool
}

// DefaultTerminalOptions derives sensible options from the environment.
func DefaultTerminalOptions() TerminalOptions {
	return TerminalOptions{Width: 88, Color: colorEnabled()}
}

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

type style struct{ enabled bool }

func (s style) bold(v string) string   { return s.wrap("1", v) }
func (s style) dim(v string) string    { return s.wrap("2", v) }
func (s style) cyan(v string) string   { return s.wrap("36", v) }
func (s style) yellow(v string) string { return s.wrap("33", v) }
func (s style) green(v string) string  { return s.wrap("32", v) }

func (s style) wrap(code, v string) string {
	if !s.enabled || v == "" {
		return v
	}
	return "\x1b[" + code + "m" + v + "\x1b[0m"
}

// Terminal writes a walkthrough as readable terminal output.
func Terminal(out io.Writer, w *walkthrough.Walkthrough, opts TerminalOptions) error {
	if opts.Width <= 0 {
		opts.Width = 88
	}
	s := style{enabled: opts.Color}

	fmt.Fprintln(out, s.bold(w.Title))
	fmt.Fprintln(out, s.dim(strings.Repeat("─", minInt(len(w.Title), opts.Width))))
	fmt.Fprintln(out)
	fmt.Fprintln(out, wrapText(w.Headline, opts.Width))
	if w.Summary != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, wrapText(w.Summary, opts.Width))
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s %s\n", s.dim("Scope:"), scopeLine(w))
	fmt.Fprintf(out, "%s %s (level %d)", s.dim("Depth:"), w.Complexity.Label, w.Complexity.Level)
	if w.Complexity.Rationale != "" {
		fmt.Fprintf(out, " — %s", w.Complexity.Rationale)
	}
	fmt.Fprintln(out)

	if len(w.Concepts) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, s.bold("Concepts you need"))
		for _, c := range w.Concepts {
			fmt.Fprintf(out, "  %s %s\n", s.cyan("•"), s.bold(c.Name))
			if c.Summary != "" {
				fmt.Fprintln(out, indent(wrapText(c.Summary, opts.Width-4), "    "))
			}
		}
	}

	if w.BeforeAfter != nil && len(w.BeforeAfter.Aspects) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, s.bold("Before and after"))
		if w.BeforeAfter.Summary != "" {
			fmt.Fprintln(out, indent(wrapText(w.BeforeAfter.Summary, opts.Width-2), "  "))
		}
		for _, a := range w.BeforeAfter.Aspects {
			fmt.Fprintf(out, "\n  %s\n", s.bold(a.Aspect))
			fmt.Fprintf(out, "    %s %s\n", s.yellow("before:"), strings.TrimSpace(oneLine(a.Before)))
			fmt.Fprintf(out, "    %s  %s\n", s.green("after:"), strings.TrimSpace(oneLine(a.After)))
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, s.bold("Walkthrough"))
	for i, step := range w.Steps {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%s %s\n", s.cyan(fmt.Sprintf("%d.", i+1)), s.bold(step.Title))
		if step.Summary != "" {
			fmt.Fprintln(out, indent(s.dim(wrapText(step.Summary, opts.Width-3)), "   "))
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, indent(wrapProse(markdownToText(step.Explanation), opts.Width-3), "   "))

		for _, ref := range step.CodeRefs {
			fmt.Fprintf(out, "   %s %s", s.dim("→"), refLabel(ref))
			if ref.Note != "" {
				fmt.Fprintf(out, " %s", s.dim("— "+ref.Note))
			}
			fmt.Fprintln(out)
		}
		if opts.Diagrams {
			for _, d := range step.Diagrams {
				fmt.Fprintln(out)
				fmt.Fprintf(out, "   %s\n", s.dim("diagram: "+d.Title))
				fmt.Fprintln(out, indent(d.Source, "   "))
			}
		} else if len(step.Diagrams) > 0 {
			fmt.Fprintf(out, "   %s\n", s.dim(fmt.Sprintf("(%d diagram(s) — view with `codewalk serve` or --format markdown)", len(step.Diagrams))))
		}
		if step.DeepDive != nil {
			if opts.DeepDives {
				fmt.Fprintln(out)
				title := step.DeepDive.Title
				if title == "" {
					title = "Deep dive"
				}
				fmt.Fprintf(out, "   %s\n", s.bold(title))
				fmt.Fprintln(out, indent(wrapProse(markdownToText(step.DeepDive.Explanation), opts.Width-5), "     "))
			} else {
				fmt.Fprintf(out, "   %s\n", s.dim("(deep dive available — rerun with --deep-dives)"))
			}
		}
	}

	if len(w.StartHere) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, s.bold("Where to start reading"))
		for _, ref := range w.StartHere {
			fmt.Fprintf(out, "  %s %s", s.cyan("→"), refLabel(ref))
			if ref.Note != "" {
				fmt.Fprintf(out, " %s", s.dim("— "+ref.Note))
			}
			fmt.Fprintln(out)
		}
	}

	if len(w.Ignorable) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, s.bold("Safe to skip for now"))
		for _, ig := range w.Ignorable {
			label := ig.Path
			if label == "" {
				label = ig.Area
			}
			fmt.Fprintf(out, "  %s %s %s\n", s.dim("·"), label, s.dim("— "+ig.Reason))
		}
	}

	if len(w.Uncertainties) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, s.bold("Not established"))
		for _, u := range w.Uncertainties {
			fmt.Fprintf(out, "  %s %s\n", s.yellow("?"), wrapText(u.Question, opts.Width-4))
			if u.Known != "" {
				fmt.Fprintln(out, indent(s.dim("known: "+u.Known), "    "))
			}
			if u.WhereNext != "" {
				fmt.Fprintln(out, indent(s.dim("look at: "+u.WhereNext), "    "))
			}
		}
	}
	return nil
}

func scopeLine(w *walkthrough.Walkthrough) string {
	s := w.Scope
	switch s.Selector {
	case "repository":
		if s.Subtree != "" {
			return fmt.Sprintf("%s (%s)", s.RepositoryName, s.Subtree)
		}
		return s.RepositoryName + " (whole repository)"
	case "working-tree":
		return fmt.Sprintf("%s — uncommitted changes, %d files, +%d/-%d",
			s.RepositoryName, s.Stats.FilesChanged, s.Stats.Insertions, s.Stats.Deletions)
	case "staged":
		return fmt.Sprintf("%s — staged changes, %d files, +%d/-%d",
			s.RepositoryName, s.Stats.FilesChanged, s.Stats.Insertions, s.Stats.Deletions)
	default:
		return fmt.Sprintf("%s — %s..%s, %d files, +%d/-%d",
			s.RepositoryName, s.Base, s.Head, s.Stats.FilesChanged, s.Stats.Insertions, s.Stats.Deletions)
	}
}

func refLabel(ref walkthrough.CodeReference) string {
	label := ref.Path
	if ref.StartLine > 0 {
		label += fmt.Sprintf(":%d", ref.StartLine)
		if ref.EndLine > ref.StartLine {
			label += fmt.Sprintf("-%d", ref.EndLine)
		}
	}
	if ref.Symbol != "" {
		label += "  " + ref.Symbol
	}
	if ref.Side == "before" {
		label += " (before)"
	}
	return label
}

// markdownToText flattens the small Markdown subset used in explanations so it
// reads well in a terminal without a Markdown renderer. Fenced code blocks are
// kept verbatim and indented; the fence lines themselves are dropped, since a
// bare "go" line reads as noise.
func markdownToText(md string) string {
	var out []string
	inCode := false
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimRight(line, " ")
		if strings.HasPrefix(strings.TrimSpace(t), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, "  "+t)
			continue
		}
		t = strings.ReplaceAll(t, "**", "")
		t = strings.ReplaceAll(t, "`", "")
		out = append(out, t)
	}
	return strings.Join(out, "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// wrapProse wraps prose but leaves indented lines alone, so code kept from a
// fenced block is not reflowed into nonsense.
func wrapProse(s string, width int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "  ") {
			out = append(out, line)
			continue
		}
		out = append(out, wrapText(line, width))
	}
	return strings.Join(out, "\n")
}

// wrapText wraps prose to a width while preserving blank lines and list items.
func wrapText(s string, width int) string {
	if width <= 20 {
		width = 20
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(para)
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		prefix := ""
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			prefix = "  "
		}
		words := strings.Fields(trimmed)
		line := ""
		for _, word := range words {
			switch {
			case line == "":
				line = word
			case len(line)+1+len(word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = prefix + word
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
