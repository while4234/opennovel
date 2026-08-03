package agents

import (
	"context"
	"encoding/json"
	"testing"
)

type workflowSubagentRecorder struct {
	args json.RawMessage
}

func (t *workflowSubagentRecorder) Name() string        { return "subagent" }
func (t *workflowSubagentRecorder) Description() string { return "generic" }
func (t *workflowSubagentRecorder) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent":      map[string]any{"type": "string"},
			"task":       map[string]any{"type": "string"},
			"background": map[string]any{"type": "boolean"},
			"tasks":      map[string]any{"type": "array"},
			"team_name":  map[string]any{"type": "string"},
		},
	}
}
func (t *workflowSubagentRecorder) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	t.args = append(t.args[:0], args...)
	return json.RawMessage(`{"ok":true}`), nil
}

func TestWorkflowSubagentToolForcesHostDirectedSynchronousMode(t *testing.T) {
	recorder := &workflowSubagentRecorder{}
	tool := newWorkflowSubagentTool(recorder)
	properties := tool.Schema()["properties"].(map[string]any)
	for _, forbidden := range []string{"background", "tasks", "team_name"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("workflow schema exposed %q", forbidden)
		}
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(
		`{"agent":"editor","task":"audit volume 1","background":true,"team_name":"wrong"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(recorder.args, &got); err != nil {
		t.Fatal(err)
	}
	if got["agent"] != "editor" || got["task"] != "audit volume 1" {
		t.Fatalf("canonical workflow request = %+v", got)
	}
	if _, ok := got["background"]; ok {
		t.Fatalf("background mode leaked to generic subagent: %+v", got)
	}
	if _, ok := got["team_name"]; ok {
		t.Fatalf("team mode leaked to generic subagent: %+v", got)
	}
	if len(result) > 256 || !json.Valid(result) {
		t.Fatalf("workflow receipt must stay bounded and valid: %q", result)
	}
}
