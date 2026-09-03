// Package jsonx extracts JSON from model output.
//
// Models emit JSON with a preamble, inside a Markdown fence, or with a trailing
// explanation, regardless of instructions. Rather than failing a whole
// walkthrough run over presentation, codewalk extracts the first balanced JSON
// value and reports a precise error when that is impossible, so the caller can
// ask for a repair.
package jsonx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Extract returns the first balanced JSON object or array in s.
func Extract(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("response was empty")
	}
	if fenced, ok := extractFence(s); ok {
		s = fenced
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return "", fmt.Errorf("response contained no JSON value")
	}
	open := s[start]
	close := byte('}')
	if open == '[' {
		close = ']'
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case escaped:
			escaped = false
		case ch == '\\' && inString:
			escaped = true
		case ch == '"':
			inString = !inString
		case inString:
			// Braces inside strings are literal.
		case ch == open:
			depth++
		case ch == close:
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("JSON value starting at offset %d is not closed", start)
}

// extractFence returns the contents of the first Markdown code fence.
func extractFence(s string) (string, bool) {
	i := strings.Index(s, "```")
	if i < 0 {
		return "", false
	}
	rest := s[i+3:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	} else {
		return "", false
	}
	j := strings.Index(rest, "```")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// Unmarshal extracts JSON from model output and decodes it into v.
func Unmarshal(s string, v any) error {
	raw, err := Extract(s)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return fmt.Errorf("%w", err)
	}
	return nil
}
