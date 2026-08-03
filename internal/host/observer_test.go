package host

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestParseSubagentResultError(t *testing.T) {
	cases := []struct {
		name   string
		result string
		want   string
	}{
		{"empty", ``, ""},
		{"object form", `{"error":"unknown agent \"writer2\""}`, `unknown agent "writer2"`},
		{"object empty error field", `{"error":""}`, ""},
		{"bare string - invalid params", `"Invalid parameters: provide exactly one mode (agent+task, tasks, or chain)"`, "Invalid parameters: provide exactly one mode (agent+task, tasks, or chain)"},
		{"bare string - background", `"background mode requires agent + task"`, "background mode requires agent + task"},
		{"bare string - parallel cap", `"Too many parallel tasks (5). Max is 3."`, "Too many parallel tasks (5). Max is 3."},
		{"bare string - normal result not flagged", `"Chapter committed"`, ""},
		{"success object not flagged", `{"chapter":1,"status":"ok"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSubagentResultError(json.RawMessage(c.result))
			if got != c.want {
				t.Fatalf("parseSubagentResultError(%q) = %q, want %q", c.result, got, c.want)
			}
		})
	}
}

func testObserver(events *[]Event) *observer {
	return &observer{
		emitEv: func(ev Event) {
			*events = append(*events, ev)
		},
		emitD:                 func(string) {},
		emitC:                 func() {},
		agents:                make(map[string]*agentState),
		lastThinkingByAgent:   make(map[string]string),
		dispatchStarts:        make(map[string]*activeCall),
		toolStarts:            make(map[string]*activeCall),
		streamExtractors:      make(map[string]*agentExtractor),
		streamArgPrefixes:     make(map[string]string),
		streamArgLabels:       make(map[string]string),
		malformedToolArgFails: make(map[string]int),
		retryEvents:           make(map[string]string),
	}
}

func TestObserverRetryEventsUpdateSameLine(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handle(agentcore.Event{
		Type: agentcore.EventRetry,
		RetryInfo: &agentcore.RetryInfo{
			Attempt:    1,
			MaxRetries: 7,
			Err:        errors.New("server 500"),
		},
	})
	o.handle(agentcore.Event{
		Type: agentcore.EventRetry,
		RetryInfo: &agentcore.RetryInfo{
			Attempt:    2,
			MaxRetries: 7,
			Err:        errors.New("server 500 again"),
		},
	})

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 raw update events", len(events))
	}
	if events[0].ID == "" || events[1].ID != events[0].ID {
		t.Fatalf("retry events should share ID for TUI in-place update: %+v", events)
	}
	if !strings.Contains(events[1].Summary, "重试 (2/7)") {
		t.Fatalf("summary = %q, want updated retry count", events[1].Summary)
	}
}

func TestObserverSubagentRetryEventsUpdateSameLinePerAgent(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	for i := 1; i <= 2; i++ {
		o.handleToolUpdate(agentcore.Event{
			Type: agentcore.EventToolExecUpdate,
			Progress: &agentcore.ProgressPayload{
				Kind:       agentcore.ProgressRetry,
				Agent:      "writer",
				Attempt:    i,
				MaxRetries: 7,
				Message:    "stream failed",
			},
		})
	}

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 raw update events", len(events))
	}
	if events[0].ID == "" || events[1].ID != events[0].ID {
		t.Fatalf("writer retry events should share ID: %+v", events)
	}
	if events[1].Agent != "writer" || !strings.Contains(events[1].Summary, "重试 (2/7)") {
		t.Fatalf("event = %+v, want writer retry 2/7", events[1])
	}
}

func TestObserverSubagentToolDeltaUpdatesSaveFoundationType(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handleSubagentDelta(&agentcore.ProgressPayload{
		Kind:      agentcore.ProgressToolDelta,
		Agent:     "architect_long",
		Tool:      "save_foundation",
		DeltaKind: agentcore.DeltaToolCall,
		Delta:     `{"type":"premise","content":"# 书名`,
	})

	if len(events) < 2 {
		t.Fatalf("events = %d, want start + summary update", len(events))
	}
	if events[0].Category != "TOOL" || events[0].Summary != "save_foundation" || events[0].Depth != 1 {
		t.Fatalf("start event = %+v", events[0])
	}
	if events[1].ID != events[0].ID || events[1].Summary != "save_foundation[premise]" {
		t.Fatalf("summary update = %+v, start = %+v", events[1], events[0])
	}
}

func TestObserverMarksSubagentWorkingDuringModelStream(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handleSubagentDelta(&agentcore.ProgressPayload{
		Kind:      agentcore.ProgressToolDelta,
		Agent:     "writer",
		DeltaKind: agentcore.DeltaText,
		Delta:     "正在分析本章",
	})

	snapshots := o.agentSnapshots()
	if len(snapshots) != 1 || snapshots[0].Name != "writer" || snapshots[0].State != "working" {
		t.Fatalf("snapshots = %+v, want streaming writer working", snapshots)
	}
	if snapshots[0].Tool != "" {
		t.Fatalf("streaming model must not report a tool before tool call, got %q", snapshots[0].Tool)
	}
}

func TestObserverMarksCoordinatorWorkingDuringModelStream(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handleMessageUpdate(agentcore.Event{
		Type:      agentcore.EventMessageUpdate,
		DeltaKind: agentcore.DeltaText,
		Delta:     "正在选择下一步",
	})

	snapshots := o.agentSnapshots()
	if len(snapshots) != 1 || snapshots[0].Name != "coordinator" || snapshots[0].State != "working" {
		t.Fatalf("snapshots = %+v, want streaming coordinator working", snapshots)
	}
}

func TestObserverMarksDispatchTargetWorkingBeforeFirstModelDelta(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handleToolStart(agentcore.Event{
		Type: agentcore.EventToolExecStart,
		Tool: "subagent",
		Args: json.RawMessage(`{"agent":"writer","task":"打磨第 39 章"}`),
	})

	states := map[string]string{}
	for _, snapshot := range o.agentSnapshots() {
		states[snapshot.Name] = snapshot.State
	}
	if states["writer"] != "working" {
		t.Fatalf("states = %+v, want dispatch target writer working before first delta", states)
	}
}

func TestObserverSubagentToolDeltaUpdatesSaveFoundationTypeAcrossChunks(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	for _, delta := range []string{`{"ty`, `pe":"premise","content":"# 书名`} {
		o.handleSubagentDelta(&agentcore.ProgressPayload{
			Kind:      agentcore.ProgressToolDelta,
			Agent:     "architect_long",
			Tool:      "save_foundation",
			DeltaKind: agentcore.DeltaToolCall,
			Delta:     delta,
		})
	}

	var summaries []string
	for _, ev := range events {
		summaries = append(summaries, ev.Summary)
	}
	if !strings.Contains(strings.Join(summaries, "\n"), "save_foundation[premise]") {
		t.Fatalf("summaries = %v, want save_foundation[premise]", summaries)
	}
}

func TestObserverCoordinatorToolDeltaStartsToolLoading(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "call_1",
				Name: "novel_context",
			}),
		},
	}

	o.handleMessageUpdate(agentcore.Event{
		Type:      agentcore.EventMessageUpdate,
		Message:   msg,
		Delta:     `{"chapter":`,
		DeltaKind: agentcore.DeltaToolCall,
	})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Category != "TOOL" || events[0].Agent != "coordinator" || events[0].Summary != "novel_context" {
		t.Fatalf("event = %+v", events[0])
	}
	if !events[0].Running() {
		t.Fatalf("event should be running: %+v", events[0])
	}
}

func TestObserverEventErrorClosesEarlyToolLoading(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "call_1",
				Name: "novel_context",
			}),
		},
	}

	o.handleMessageUpdate(agentcore.Event{
		Type:      agentcore.EventMessageUpdate,
		Message:   msg,
		Delta:     `{"chapter":`,
		DeltaKind: agentcore.DeltaToolCall,
	})
	o.handle(agentcore.Event{Type: agentcore.EventError, Err: errors.New("stream failed")})

	if len(events) != 3 {
		t.Fatalf("events = %d, want start + failed finish + error: %+v", len(events), events)
	}
	if events[1].ID != events[0].ID || events[1].FinishedAt.IsZero() || !events[1].Failed {
		t.Fatalf("finish event = %+v, start = %+v", events[1], events[0])
	}
	if events[2].Category != "ERROR" {
		t.Fatalf("error event = %+v", events[2])
	}
}

func TestObserverMalformedToolArgsFirstFailureWarnsWithoutErrorRow(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handleToolStart(agentcore.Event{
		Type: agentcore.EventToolExecStart,
		Tool: "novel_context",
		Args: json.RawMessage(`{}`),
	})
	o.handleToolEnd(agentcore.Event{
		Type:    agentcore.EventToolExecEnd,
		Tool:    "novel_context",
		Result:  mustJSONRaw(t, malformedToolArgMessage("novel_context")),
		IsError: true,
	})

	if len(events) != 2 {
		t.Fatalf("events = %d, want start + warn finish: %+v", len(events), events)
	}
	if events[1].ID != events[0].ID || events[1].Failed || events[1].Level != "warn" {
		t.Fatalf("finish event = %+v, start = %+v", events[1], events[0])
	}
	if events[1].Category != "TOOL" || strings.Contains(events[1].Category, "ERROR") {
		t.Fatalf("warning should finish the tool row only: %+v", events[1])
	}
}

func TestObserverMalformedToolArgsRepeatedFailureEscalatesToError(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := malformedToolArgMessage("novel_context")

	for i := 0; i < 2; i++ {
		o.handleToolStart(agentcore.Event{
			Type: agentcore.EventToolExecStart,
			Tool: "novel_context",
			Args: json.RawMessage(`{}`),
		})
		o.handleToolEnd(agentcore.Event{
			Type:    agentcore.EventToolExecEnd,
			Tool:    "novel_context",
			Result:  mustJSONRaw(t, msg),
			IsError: true,
		})
	}

	if len(events) != 5 {
		t.Fatalf("events = %d, want first start+warn then second start+error+ERROR: %+v", len(events), events)
	}
	if events[1].Level != "warn" || events[1].Failed {
		t.Fatalf("first finish = %+v, want warn without failed", events[1])
	}
	if events[3].Level != "error" || !events[3].Failed {
		t.Fatalf("second finish = %+v, want failed error", events[3])
	}
	if events[4].Category != "ERROR" || events[4].Level != "error" {
		t.Fatalf("second failure should emit ERROR row: %+v", events[4])
	}
}

func TestObserverProgressMalformedToolArgsFirstFailureWarns(t *testing.T) {
	var events []Event
	o := testObserver(&events)

	o.handleToolUpdate(agentcore.Event{
		Type: agentcore.EventToolExecUpdate,
		Progress: &agentcore.ProgressPayload{
			Kind:  agentcore.ProgressToolStart,
			Agent: "editor",
			Tool:  "save_review",
			Args:  json.RawMessage(`{}`),
		},
	})
	o.handleToolUpdate(agentcore.Event{
		Type: agentcore.EventToolExecUpdate,
		Progress: &agentcore.ProgressPayload{
			Kind:    agentcore.ProgressToolError,
			Agent:   "editor",
			Tool:    "save_review",
			Message: malformedToolArgMessage("save_review"),
		},
	})

	if len(events) != 2 {
		t.Fatalf("events = %d, want start + warn finish: %+v", len(events), events)
	}
	if events[1].ID != events[0].ID || events[1].Failed || events[1].Level != "warn" || events[1].Depth != 1 {
		t.Fatalf("progress finish = %+v, start = %+v", events[1], events[0])
	}
}

func TestDisplayToolNameDistinguishesSourceChapter(t *testing.T) {
	args := json.RawMessage(`{"chapter":17,"source":"source"}`)
	if got := displayToolName("read_chapter", args); got != "read_chapter(第17章·原文)" {
		t.Fatalf("displayToolName = %q", got)
	}
}

func malformedToolArgMessage(tool string) string {
	return "tool argument validation failed: " + tool + ` received malformed JSON arguments: invalid character '"' after top-level value
raw args: {}""`
}

func mustJSONRaw(t *testing.T, value string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestObserverCoordinatorSubagentDeltaMergesWithExecStart(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "call_1",
				Name: "subagent",
			}),
		},
	}

	o.handleMessageUpdate(agentcore.Event{
		Type:      agentcore.EventMessageUpdate,
		Message:   msg,
		Delta:     `{"agent":"writer","task":"继续"}`,
		DeltaKind: agentcore.DeltaToolCall,
	})
	args, err := json.Marshal(map[string]any{"agent": "writer", "task": "继续"})
	if err != nil {
		t.Fatal(err)
	}
	o.handleToolStart(agentcore.Event{
		Type: agentcore.EventToolExecStart,
		Tool: "subagent",
		Args: args,
	})

	if len(events) != 2 {
		t.Fatalf("events = %d, want start + summary update: %+v", len(events), events)
	}
	if events[0].Category != "DISPATCH" || events[0].Summary != "subagent" {
		t.Fatalf("dispatch start = %+v", events[0])
	}
	if events[1].ID != events[0].ID || events[1].Summary != "writer（继续）" {
		t.Fatalf("dispatch update = %+v, start = %+v", events[1], events[0])
	}
}

func TestObserverCoordinatorSubagentDeltaUpdatesDispatchSummary(t *testing.T) {
	var events []Event
	o := testObserver(&events)
	msg := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "call_1",
				Name: "subagent",
			}),
		},
	}

	for _, delta := range []string{`{"agent":"wr`, `iter","task":"继续"}`} {
		o.handleMessageUpdate(agentcore.Event{
			Type:      agentcore.EventMessageUpdate,
			Message:   msg,
			Delta:     delta,
			DeltaKind: agentcore.DeltaToolCall,
		})
	}

	if len(events) != 2 {
		t.Fatalf("events = %d, want start + summary update: %+v", len(events), events)
	}
	if events[0].Category != "DISPATCH" || events[0].Summary != "subagent" {
		t.Fatalf("dispatch start = %+v", events[0])
	}
	if events[1].ID != events[0].ID || events[1].Summary != "writer（继续）" {
		t.Fatalf("dispatch update = %+v, start = %+v", events[1], events[0])
	}
}
