package ctxpack

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAppendJSONSectionNeverEmitsTruncatedJSON(t *testing.T) {
	var parts []string
	remaining := 120
	if stopped := appendJSONSection(&parts, "large", map[string]any{"events": strings.Repeat("evidence", 500)}, &remaining); !stopped {
		t.Fatal("oversized section should stop lower-priority packing")
	}
	if len(parts) != 1 {
		t.Fatalf("parts=%v", parts)
	}
	jsonText := strings.TrimPrefix(parts[0], "## large\n")
	var marker map[string]any
	if err := json.Unmarshal([]byte(jsonText), &marker); err != nil {
		t.Fatalf("budget marker is invalid JSON: %v: %s", err, jsonText)
	}
	if marker["reason"] != "token_budget" {
		t.Fatalf("marker=%v", marker)
	}
}
