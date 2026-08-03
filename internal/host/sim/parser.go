package sim

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseJSONPayload(text string, out any) error {
	body := strings.TrimSpace(text)
	body = stripMarkdownFence(body)
	start := strings.Index(body, "{")
	end := strings.LastIndex(body, "}")
	if start < 0 || end < start {
		return fmt.Errorf("no JSON object in response")
	}
	segment := body[start : end+1]
	if err := json.Unmarshal([]byte(segment), out); err != nil {
		repaired, repairErr := repairJSONStringControlChars(segment)
		if repairErr != nil {
			return fmt.Errorf("parse JSON response: %w", err)
		}
		if retryErr := json.Unmarshal([]byte(repaired), out); retryErr == nil {
			return nil
		}
		return fmt.Errorf("parse JSON response: %w", err)
	}
	return nil
}

func stripMarkdownFence(body string) string {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "```") {
		return body
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 2 {
		return body
	}
	lines = lines[1:]
	if n := len(lines); n > 0 && strings.HasPrefix(strings.TrimSpace(lines[n-1]), "```") {
		lines = lines[:n-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func repairJSONStringControlChars(segment string) (string, error) {
	var b strings.Builder
	b.Grow(len(segment))
	inString := false
	escaped := false
	changed := false
	for _, r := range segment {
		if inString {
			if escaped {
				b.WriteRune(r)
				escaped = false
				continue
			}
			switch r {
			case '\\':
				b.WriteRune(r)
				escaped = true
			case '"':
				b.WriteRune(r)
				inString = false
			case '\n':
				b.WriteString(`\n`)
				changed = true
			case '\r':
				b.WriteString(`\r`)
				changed = true
			case '\t':
				b.WriteString(`\t`)
				changed = true
			default:
				b.WriteRune(r)
			}
			continue
		}
		b.WriteRune(r)
		if r == '"' {
			inString = true
		}
	}
	if !changed {
		return "", fmt.Errorf("no repairable JSON string control characters")
	}
	return b.String(), nil
}
