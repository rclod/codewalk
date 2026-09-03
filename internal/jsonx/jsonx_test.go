package jsonx

import "testing"

func TestExtractHandlesModelFormatting(t *testing.T) {
	cases := map[string]string{
		"bare object":      `{"a":1}`,
		"fenced":           "```json\n{\"a\":1}\n```",
		"fenced no lang":   "```\n{\"a\":1}\n```",
		"with preamble":    "Here is the result:\n{\"a\":1}",
		"with trailer":     "{\"a\":1}\nLet me know if you need more.",
		"braces in string": `{"a":"} not the end {"}`,
		"escaped quote":    `{"a":"say \"hi\""}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := Extract(input)
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			var v map[string]any
			if err := Unmarshal(out, &v); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
		})
	}
}

func TestExtractArrays(t *testing.T) {
	out, err := Extract("Result:\n[1, 2, 3]")
	if err != nil {
		t.Fatal(err)
	}
	if out != "[1, 2, 3]" {
		t.Errorf("extract = %q", out)
	}
}

func TestExtractReportsUsefulErrors(t *testing.T) {
	if _, err := Extract(""); err == nil {
		t.Error("empty input should error")
	}
	if _, err := Extract("no json at all"); err == nil {
		t.Error("input without JSON should error")
	}
	if _, err := Extract(`{"a": 1`); err == nil {
		t.Error("unclosed JSON should error")
	}
}
