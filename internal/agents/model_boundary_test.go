package agents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestProductionAgentBoundaryPersistsOnePrivateTerminalDiagnostic(t *testing.T) {
	st := initializedDiagnosticStore(t)
	provider := &boundaryTestModel{response: "private generated prose"}
	model := withProductionAgentBoundary(provider, st, "agent_writer")
	messages := []agentcore.Message{agentcore.UserMsg("private source and chapter prompt")}
	tools := []agentcore.ToolSpec{{Name: "private_tool", Description: "private schema", Parameters: map[string]any{"type": "object"}}}
	response, err := model.Generate(t.Context(), messages, tools)
	if err != nil || response == nil {
		t.Fatalf("Generate response=%v err=%v", response, err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d, want 1", provider.calls)
	}
	records, raw := readAgentDiagnostics(t, st.Dir())
	if len(records) != 1 || records[0].Task != "agent_writer" || records[0].Status != "completed" || !records[0].UsagePresent {
		t.Fatalf("diagnostics=%+v", records)
	}
	for _, private := range []string{"private source", "private generated prose", "private_tool", "private schema"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("diagnostic leaked private content %q", private)
		}
	}
}

func TestProductionAgentBoundaryChecksExactSixtyKiBBoundaryBeforeProvider(t *testing.T) {
	for _, delta := range []int{-1, 1} {
		t.Run(map[int]string{-1: "under", 1: "over"}[delta], func(t *testing.T) {
			st := initializedDiagnosticStore(t)
			provider := &boundaryTestModel{response: "ok"}
			model := withProductionAgentBoundary(provider, st, "agent_coordinator")
			messages := exactCompiledAgentMessages(t, productionAgentInputLimitBytes+delta)
			_, err := model.Generate(t.Context(), messages, nil)
			if delta < 0 {
				if err != nil || provider.calls != 1 {
					t.Fatalf("under-limit err=%v calls=%d", err, provider.calls)
				}
				return
			}
			if err == nil || provider.calls != 0 {
				t.Fatalf("over-limit err=%v calls=%d", err, provider.calls)
			}
			if !agentcore.IsContextOverflow(err) {
				t.Fatalf("over-limit error = %v, want recoverable context overflow", err)
			}
			records, _ := readAgentDiagnostics(t, st.Dir())
			if len(records) != 1 || records[0].Status != "rejected_budget" || records[0].InputBytes != productionAgentInputLimitBytes+1 {
				t.Fatalf("over-limit diagnostics=%+v", records)
			}
		})
	}
}

func TestProductionWriterBoundaryAllowsOneChapterValidationPackage(t *testing.T) {
	for _, delta := range []int{-1, 1} {
		t.Run(map[int]string{-1: "under", 1: "over"}[delta], func(t *testing.T) {
			st := initializedDiagnosticStore(t)
			provider := &boundaryTestModel{response: "ok"}
			model := withProductionAgentBoundary(provider, st, "agent_writer")
			messages := exactCompiledAgentMessages(t, writerAgentInputLimitBytes+delta)
			_, err := model.Generate(t.Context(), messages, nil)
			if delta < 0 {
				if err != nil || provider.calls != 1 {
					t.Fatalf("under-limit err=%v calls=%d", err, provider.calls)
				}
				return
			}
			if err == nil || provider.calls != 0 || !agentcore.IsContextOverflow(err) {
				t.Fatalf("over-limit err=%v calls=%d", err, provider.calls)
			}
			records, _ := readAgentDiagnostics(t, st.Dir())
			if len(records) != 1 || records[0].Status != "rejected_budget" ||
				records[0].InputBytes != writerAgentInputLimitBytes+1 {
				t.Fatalf("over-limit diagnostics=%+v", records)
			}
		})
	}
}

func TestProductionLongArchitectBoundaryAllowsOneFocalVolumeContext(t *testing.T) {
	for _, delta := range []int{-1, 1} {
		t.Run(map[int]string{-1: "under", 1: "over"}[delta], func(t *testing.T) {
			st := initializedDiagnosticStore(t)
			provider := &boundaryTestModel{response: "ok"}
			model := withProductionAgentBoundary(provider, st, "agent_architect_long")
			messages := exactCompiledAgentMessages(t, architectLongInputLimitBytes+delta)
			_, err := model.Generate(t.Context(), messages, nil)
			if delta < 0 {
				if err != nil || provider.calls != 1 {
					t.Fatalf("under-limit err=%v calls=%d", err, provider.calls)
				}
				return
			}
			if err == nil || provider.calls != 0 || !agentcore.IsContextOverflow(err) {
				t.Fatalf("over-limit err=%v calls=%d", err, provider.calls)
			}
			records, _ := readAgentDiagnostics(t, st.Dir())
			if len(records) != 1 || records[0].Status != "rejected_budget" ||
				records[0].InputBytes != architectLongInputLimitBytes+1 {
				t.Fatalf("over-limit diagnostics=%+v", records)
			}
		})
	}
}

func TestProductionEditorBoundaryAllowsOneArcReviewPackage(t *testing.T) {
	if editorAgentInputLimitBytes != 128*1024 {
		t.Fatalf("editor input limit=%d, want %d", editorAgentInputLimitBytes, 128*1024)
	}
	for _, delta := range []int{-1, 1} {
		t.Run(map[int]string{-1: "under", 1: "over"}[delta], func(t *testing.T) {
			st := initializedDiagnosticStore(t)
			provider := &boundaryTestModel{response: "ok"}
			model := withProductionAgentBoundary(provider, st, "agent_editor")
			messages := exactCompiledAgentMessages(t, editorAgentInputLimitBytes+delta)
			_, err := model.Generate(t.Context(), messages, nil)
			if delta < 0 {
				if err != nil || provider.calls != 1 {
					t.Fatalf("under-limit err=%v calls=%d", err, provider.calls)
				}
				return
			}
			if err == nil || provider.calls != 0 || !agentcore.IsContextOverflow(err) {
				t.Fatalf("over-limit err=%v calls=%d", err, provider.calls)
			}
			records, _ := readAgentDiagnostics(t, st.Dir())
			if len(records) != 1 || records[0].Status != "rejected_budget" ||
				records[0].InputBytes != editorAgentInputLimitBytes+1 {
				t.Fatalf("over-limit diagnostics=%+v", records)
			}
		})
	}
}

func TestProductionCharacterBoundaryAllowsOneCompleteCastReview(t *testing.T) {
	for _, delta := range []int{-1, 1} {
		t.Run(map[int]string{-1: "under", 1: "over"}[delta], func(t *testing.T) {
			st := initializedDiagnosticStore(t)
			provider := &boundaryTestModel{response: "ok"}
			model := withProductionAgentBoundary(provider, st, "agent_character")
			messages := exactCompiledAgentMessages(t, characterAgentInputLimitBytes+delta)
			_, err := model.Generate(t.Context(), messages, nil)
			if delta < 0 {
				if err != nil || provider.calls != 1 {
					t.Fatalf("under-limit err=%v calls=%d", err, provider.calls)
				}
				return
			}
			if err == nil || provider.calls != 0 || !agentcore.IsContextOverflow(err) {
				t.Fatalf("over-limit err=%v calls=%d", err, provider.calls)
			}
			records, _ := readAgentDiagnostics(t, st.Dir())
			if len(records) != 1 || records[0].Status != "rejected_budget" ||
				records[0].InputBytes != characterAgentInputLimitBytes+1 {
				t.Fatalf("over-limit diagnostics=%+v", records)
			}
		})
	}
}

func TestProductionAgentBoundaryRecordsEmptyTruncatedAndProviderFailure(t *testing.T) {
	cases := []struct {
		name, response, wantStatus string
		stopReason                 agentcore.StopReason
		err                        error
	}{
		{name: "empty", wantStatus: "empty_response"},
		{name: "truncated", response: "partial", stopReason: agentcore.StopReasonLength, wantStatus: "truncated_response"},
		{name: "provider", err: errors.New("provider unavailable"), wantStatus: "provider_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := initializedDiagnosticStore(t)
			provider := &boundaryTestModel{response: tc.response, stopReason: tc.stopReason, err: tc.err}
			model := withProductionAgentBoundary(provider, st, "agent_editor")
			_, _ = model.Generate(t.Context(), []agentcore.Message{agentcore.UserMsg("request")}, nil)
			records, _ := readAgentDiagnostics(t, st.Dir())
			if len(records) != 1 || records[0].Status != tc.wantStatus {
				t.Fatalf("diagnostics=%+v", records)
			}
		})
	}
}

func TestSummaryModelUsesExplicitDurableStoreWithoutContextInjection(t *testing.T) {
	for _, agent := range []string{"coordinator", "writer", "editor"} {
		t.Run(agent, func(t *testing.T) {
			st := initializedDiagnosticStore(t)
			provider := &boundaryTestModel{response: "durable summary"}
			wrapped := summaryCompatibleModelWithStore(withProductionAgentBoundary(provider, st, "agent_"+agent), st, agent, nil)
			if _, err := wrapped.Generate(context.Background(), []agentcore.Message{agentcore.UserMsg("private history")}, nil); err != nil {
				t.Fatal(err)
			}
			records, _ := readAgentDiagnostics(t, st.Dir())
			if len(records) != 1 || records[0].Task != "agent_context_summary" {
				t.Fatalf("summary diagnostics=%+v; expected one summary owner and no duplicate agent record", records)
			}
		})
	}
}

func exactCompiledAgentMessages(t *testing.T, target int) []agentcore.Message {
	t.Helper()
	base := []agentcore.Message{{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("x")}}}
	empty, err := compileAgentInput(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := target - (len(empty) - 1)
	if count < 0 {
		t.Fatalf("target %d below compiled envelope %d", target, len(empty))
	}
	messages := []agentcore.Message{{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock(strings.Repeat("x", count))}}}
	compiled, err := compileAgentInput(messages, nil)
	if err != nil || len(compiled) != target {
		t.Fatalf("compiled bytes=%d target=%d err=%v", len(compiled), target, err)
	}
	return messages
}

type boundaryTestModel struct {
	calls      int
	response   string
	stopReason agentcore.StopReason
	err        error
}

func (m *boundaryTestModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(m.response)},
		Usage: &agentcore.Usage{Input: 10, Output: 2}, StopReason: m.stopReason,
	}}, nil
}

func (m *boundaryTestModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, errors.New("not used")
}

func (m *boundaryTestModel) SupportsTools() bool { return true }

func initializedDiagnosticStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	return st
}

func readAgentDiagnostics(t *testing.T, root string) ([]store.ManuscriptContextDiagnostic, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "meta", "manuscript", "context-diagnostics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []store.ManuscriptContextDiagnostic
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	return records, raw
}
