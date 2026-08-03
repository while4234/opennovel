package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
)

func TestToolCallRepairModelRepairsTrailingGarbageGenerate(t *testing.T) {
	model := NewToolCallRepairModel(&scriptedRepairModel{
		response: toolCallResponse(agentcore.ToolCall{
			ID:             "call-1",
			Name:           "novel_context",
			Args:           json.RawMessage(`{}`),
			ArgsInvalid:    true,
			ArgsRawText:    `{}""`,
			ArgsParseError: `invalid character '"' after top-level value`,
		}),
	})

	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	call := resp.Message.ToolCalls()[0]
	if call.ArgsInvalid {
		t.Fatalf("ArgsInvalid = true, call = %+v", call)
	}
	if string(call.Args) != `{}` {
		t.Fatalf("Args = %s, want {}", call.Args)
	}
}

func TestToolCallRepairModelRepairsTrailingGarbageStream(t *testing.T) {
	bad := agentcore.ToolCall{
		ID:             "call-2",
		Name:           "read_chapter",
		Args:           json.RawMessage(`{}`),
		ArgsInvalid:    true,
		ArgsRawText:    `{"chapter":1}""`,
		ArgsParseError: `invalid character '"' after top-level value`,
	}
	model := NewToolCallRepairModel(&scriptedRepairModel{
		events: []agentcore.StreamEvent{
			{
				Type:              agentcore.StreamEventToolCallEnd,
				Message:           toolCallResponse(bad).Message,
				CompletedToolCall: &bad,
			},
			{
				Type:    agentcore.StreamEventDone,
				Message: toolCallResponse(bad).Message,
			},
		},
	})

	stream, err := model.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	end := <-stream
	if end.CompletedToolCall == nil {
		t.Fatal("CompletedToolCall is nil")
	}
	if end.CompletedToolCall.ArgsInvalid {
		t.Fatalf("completed tool call remained invalid: %+v", end.CompletedToolCall)
	}
	if string(end.CompletedToolCall.Args) != `{"chapter":1}` {
		t.Fatalf("completed args = %s", end.CompletedToolCall.Args)
	}
	done := <-stream
	call := done.Message.ToolCalls()[0]
	if call.ArgsInvalid || string(call.Args) != `{"chapter":1}` {
		t.Fatalf("done message call = %+v", call)
	}
}

func TestToolCallRepairModelDoesNotRepairTruncatedJSON(t *testing.T) {
	model := NewToolCallRepairModel(&scriptedRepairModel{
		response: toolCallResponse(agentcore.ToolCall{
			ID:             "call-3",
			Name:           "save_review",
			Args:           json.RawMessage(`{}`),
			ArgsInvalid:    true,
			ArgsRawText:    `{"chapter":1,"summary":"unfinished`,
			ArgsParseError: `unexpected end of JSON input`,
		}),
	})

	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	call := resp.Message.ToolCalls()[0]
	if !call.ArgsInvalid {
		t.Fatalf("truncated args should remain invalid: %+v", call)
	}
}

func TestExtractLosslessJSONArgumentsRejectsSemanticTail(t *testing.T) {
	if _, ok := extractLosslessJSONArguments(`{"chapter":1} more`); ok {
		t.Fatal("semantic tail should not be repaired")
	}
}

func TestExtractLosslessJSONArgumentsRequiresObject(t *testing.T) {
	if _, ok := extractLosslessJSONArguments(`["chapter",1]""`); ok {
		t.Fatal("non-object tool args should not be repaired")
	}
}

func TestToolCallRepairModelAllowsRepairedArgsThroughAgentValidation(t *testing.T) {
	tests := []struct {
		name    string
		tool    *recordingRepairTool
		rawArgs string
		want    string
	}{
		{
			name:    "novel_context empty object",
			tool:    newRecordingRepairTool("novel_context", schema.Object()),
			rawArgs: `{}""`,
			want:    `{}`,
		},
		{
			name: "read_chapter required chapter",
			tool: newRecordingRepairTool("read_chapter", schema.Object(
				schema.Property("chapter", schema.Int("chapter number")).Required(),
			)),
			rawArgs: `{"chapter":1}""`,
			want:    `{"chapter":1}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := NewToolCallRepairModel(&scriptedRepairModel{responses: []*agentcore.LLMResponse{
				toolCallResponse(agentcore.ToolCall{
					ID:             "call-1",
					Name:           tc.tool.Name(),
					Args:           json.RawMessage(`{}`),
					ArgsInvalid:    true,
					ArgsRawText:    tc.rawArgs,
					ArgsParseError: `invalid character '"' after top-level value`,
				}),
				textResponse("done"),
			}})
			agent := agentcore.NewAgent(
				agentcore.WithModel(model),
				agentcore.WithTools(tc.tool),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := agent.Prompt(ctx, "start"); err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			agent.WaitForIdle()

			if state := agent.State(); state.Error != "" {
				t.Fatalf("agent error = %q", state.Error)
			}
			if got := tc.tool.CallCount(); got != 1 {
				t.Fatalf("tool calls = %d, want 1", got)
			}
			if got := string(tc.tool.LastArgs()); got != tc.want {
				t.Fatalf("tool args = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestToolCallRepairModelLeavesTruncatedArgsRecoverableInAgent(t *testing.T) {
	tool := newRecordingRepairTool("save_review", schema.Object(
		schema.Property("chapter", schema.Int("chapter")).Required(),
	))
	model := NewToolCallRepairModel(&scriptedRepairModel{responses: []*agentcore.LLMResponse{
		toolCallResponse(agentcore.ToolCall{
			ID:             "call-1",
			Name:           tool.Name(),
			Args:           json.RawMessage(`{}`),
			ArgsInvalid:    true,
			ArgsRawText:    `{"chapter":1,"summary":"unfinished`,
			ArgsParseError: `unexpected end of JSON input`,
		}),
		textResponse("done"),
	}})
	agent := agentcore.NewAgent(
		agentcore.WithModel(model),
		agentcore.WithTools(tool),
	)
	var events []agentcore.Event
	unsub := agent.Subscribe(func(ev agentcore.Event) {
		events = append(events, ev)
	})
	defer unsub()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := agent.Prompt(ctx, "start"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	agent.WaitForIdle()

	if state := agent.State(); state.Error != "" {
		t.Fatalf("agent error = %q", state.Error)
	}
	if got := tool.CallCount(); got != 0 {
		t.Fatalf("tool calls = %d, want 0", got)
	}
	foundRecoverableError := false
	for _, ev := range events {
		if ev.Type != agentcore.EventToolExecEnd || ev.Tool != tool.Name() {
			continue
		}
		foundRecoverableError = true
		if !ev.IsError {
			t.Fatalf("tool end should be an error result: %+v", ev)
		}
		if !strings.Contains(string(ev.Result), "received malformed JSON arguments") {
			t.Fatalf("tool error result = %s", ev.Result)
		}
	}
	if !foundRecoverableError {
		t.Fatal("expected recoverable tool validation error event")
	}
}

type scriptedRepairModel struct {
	response      *agentcore.LLMResponse
	responses     []*agentcore.LLMResponse
	responseIndex int
	events        []agentcore.StreamEvent
}

func (m *scriptedRepairModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.nextResponse()
}

func (m *scriptedRepairModel) nextResponse() (*agentcore.LLMResponse, error) {
	if m.response != nil {
		return m.response, nil
	}
	if m.responseIndex >= len(m.responses) {
		return nil, errors.New("missing response")
	}
	resp := m.responses[m.responseIndex]
	m.responseIndex++
	return resp, nil
}

func (m *scriptedRepairModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if len(m.events) > 0 {
		out := make(chan agentcore.StreamEvent, len(m.events))
		for _, event := range m.events {
			out <- event
		}
		close(out)
		return out, nil
	}
	resp, err := m.nextResponse()
	if err != nil {
		return nil, err
	}
	out := make(chan agentcore.StreamEvent, 1)
	out <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    resp.Message,
		StopReason: resp.Message.StopReason,
	}
	close(out)
	return out, nil
}

func (m *scriptedRepairModel) SupportsTools() bool { return true }

func toolCallResponse(call agentcore.ToolCall) *agentcore.LLMResponse {
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.ToolCallBlock(call)},
		StopReason: agentcore.StopReasonToolUse,
		Timestamp:  time.Now(),
	}}
}

func textResponse(text string) *agentcore.LLMResponse {
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: agentcore.StopReasonStop,
		Timestamp:  time.Now(),
	}}
}

type recordingRepairTool struct {
	name   string
	schema map[string]any
	mu     sync.Mutex
	calls  int
	args   json.RawMessage
}

func newRecordingRepairTool(name string, schema map[string]any) *recordingRepairTool {
	return &recordingRepairTool{name: name, schema: schema}
}

func (t *recordingRepairTool) Name() string        { return t.name }
func (t *recordingRepairTool) Description() string { return t.name }
func (t *recordingRepairTool) Schema() map[string]any {
	return t.schema
}

func (t *recordingRepairTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.args = append(json.RawMessage(nil), args...)
	return json.Marshal(map[string]any{"ok": true})
}

func (t *recordingRepairTool) CallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func (t *recordingRepairTool) LastArgs() json.RawMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append(json.RawMessage(nil), t.args...)
}
