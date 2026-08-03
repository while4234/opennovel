package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	productionAgentInputLimitBytes = 60 * 1024
	architectLongInputLimitBytes   = 96 * 1024
	editorAgentInputLimitBytes     = 128 * 1024
	characterAgentInputLimitBytes  = 128 * 1024
	writerAgentInputLimitBytes     = 96 * 1024
	continuityAuditInputLimitBytes = 128 * 1024
)

// diagnosticModel owns the production boundary for one agent role. It checks
// the exact messages and tool schema handed to agentcore immediately before
// the provider call and writes one terminal, metadata-only diagnostic.
type diagnosticModel struct {
	agentcore.ChatModel
	store *store.Store
	task  string
}

type executionBarrierContextKey struct{}

// WithExecutionBarrier delays the coordinator's provider call until Host has
// committed the matching lifecycle, event, router, and ownership state.
func WithExecutionBarrier(ctx context.Context, ready <-chan struct{}) context.Context {
	if ready == nil {
		return ctx
	}
	return context.WithValue(ctx, executionBarrierContextKey{}, ready)
}

type executionBarrierModel struct {
	agentcore.ChatModel
}

func WithExecutionBarrierModel(model agentcore.ChatModel) agentcore.ChatModel {
	if model == nil {
		return nil
	}
	return &executionBarrierModel{ChatModel: model}
}

func (m *executionBarrierModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if err := waitForExecutionBarrier(ctx); err != nil {
		return nil, err
	}
	return m.ChatModel.Generate(ctx, messages, tools, opts...)
}

func (m *executionBarrierModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if err := waitForExecutionBarrier(ctx); err != nil {
		return nil, err
	}
	return m.ChatModel.GenerateStream(ctx, messages, tools, opts...)
}

func waitForExecutionBarrier(ctx context.Context) error {
	ready, _ := ctx.Value(executionBarrierContextKey{}).(<-chan struct{})
	if ready == nil {
		return nil
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func withProductionAgentBoundary(model agentcore.ChatModel, st *store.Store, task string) agentcore.ChatModel {
	if model == nil {
		return nil
	}
	return &diagnosticModel{ChatModel: model, store: st, task: task}
}

func (m *diagnosticModel) ProviderName() string {
	if named, ok := m.ChatModel.(agentcore.ProviderNamer); ok {
		return named.ProviderName()
	}
	return ""
}

func (m *diagnosticModel) ModelName() string {
	if named, ok := m.ChatModel.(agentcore.ModelNamer); ok {
		return named.ModelName()
	}
	return ""
}

func (m *diagnosticModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	recorder, err := m.begin(messages, tools)
	if err != nil {
		return nil, err
	}
	response, err := m.ChatModel.Generate(modeldiag.WithStore(ctx, m.store), messages, tools, opts...)
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return nil, err
	}
	if response != nil && response.Message.StopReason == agentcore.StopReasonLength {
		_ = recorder.Finish(modeldiag.StatusTruncated, response.Message.TextContent(), response.Message.Usage)
		return response, nil
	}
	if response == nil || strings.TrimSpace(response.Message.TextContent()) == "" {
		var usage *agentcore.Usage
		if response != nil {
			usage = response.Message.Usage
		}
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", usage)
		return response, nil
	}
	if err := recorder.Finish(modeldiag.StatusCompleted, response.Message.TextContent(), response.Message.Usage); err != nil {
		return nil, err
	}
	return response, nil
}

func (m *diagnosticModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	recorder, err := m.begin(messages, tools)
	if err != nil {
		return nil, err
	}
	source, err := m.ChatModel.GenerateStream(modeldiag.WithStore(ctx, m.store), messages, tools, opts...)
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return nil, err
	}
	output := make(chan agentcore.StreamEvent)
	go func() {
		defer close(output)
		var text strings.Builder
		terminal := false
		for event := range source {
			if event.Type == agentcore.StreamEventTextDelta {
				text.WriteString(event.Delta)
			}
			if event.Type == agentcore.StreamEventDone {
				terminal = true
				final := text.String()
				if final == "" {
					final = event.Message.TextContent()
				}
				status := modeldiag.StatusCompleted
				if event.StopReason == agentcore.StopReasonLength || event.Message.StopReason == agentcore.StopReasonLength {
					status = modeldiag.StatusTruncated
				} else if strings.TrimSpace(final) == "" {
					status = modeldiag.StatusEmptyResponse
				}
				_ = recorder.Finish(status, final, event.Message.Usage)
			}
			if event.Type == agentcore.StreamEventError {
				terminal = true
				_ = recorder.Finish(modeldiag.StatusProviderError, text.String(), nil)
			}
			select {
			case output <- event:
			case <-ctx.Done():
				_ = recorder.Finish(modeldiag.StatusProviderError, text.String(), nil)
				return
			}
		}
		if !terminal {
			_ = recorder.Finish(modeldiag.StatusProviderError, text.String(), nil)
		}
	}()
	return output, nil
}

func (m *diagnosticModel) begin(messages []agentcore.Message, tools []agentcore.ToolSpec) (*modeldiag.Recorder, error) {
	compiled, err := compileAgentInput(messages, tools)
	if err != nil {
		return nil, fmt.Errorf("compile %s model input: %w", m.task, err)
	}
	inputLimitBytes := m.inputLimitBytes()
	recorder, err := modeldiag.Begin(modeldiag.Request{
		Store:           m.store,
		Task:            m.task,
		User:            compiled,
		InputLimitBytes: inputLimitBytes,
	})
	if err != nil && len(compiled) > inputLimitBytes {
		// This is the same recoverable condition as a provider context overflow.
		// Mapping the exact byte boundary onto agentcore's sentinel activates its
		// forced ContextManager rewrite and one retry before the subagent fails.
		return nil, &agentcore.ContextOverflowError{Cause: err}
	}
	return recorder, err
}

func (m *diagnosticModel) inputLimitBytes() int {
	if m.task == "continuity_auditor" {
		// The independent comparison needs the current draft plus the adjacent
		// committed chapter. Two maximum-sized Chinese chapters can exceed the
		// generic planning boundary while remaining comfortably within review
		// model context windows.
		return continuityAuditInputLimitBytes
	}
	if m.task == "agent_writer" {
		// Writer receives exactly one active chapter work package plus the
		// current draft and validation tool schemas. That valid chapter-scoped
		// request can exceed the shared 60 KiB planning boundary even though it
		// remains far below Writer's 128K-token runtime window. Keep planning
		// agents at 60 KiB so long books still cannot inject whole-book context.
		return writerAgentInputLimitBytes
	}
	if m.task == "agent_architect_long" {
		// Long-form Architect must retain one bounded novel_context result for
		// the active volume. Confirmed Foundation data plus that focal planning
		// window can legitimately cross the shared 60 KiB planning boundary
		// while remaining far below Architect's 96K-token runtime window.
		return architectLongInputLimitBytes
	}
	if m.task == "agent_editor" {
		// Editor reviews one bounded arc or chapter batch after novel_context
		// expands its confirmed outlines and audit evidence. Four-chapter arc
		// reviews can cross 60 KiB while remaining below the 128K-token Editor
		// runtime window.
		return editorAgentInputLimitBytes
	}
	if m.task == "agent_character" {
		// Character review must compare one complete staged cast and its
		// planned relationships. A normal eight-to-twelve-character candidate
		// can exceed the generic planning boundary while remaining far below
		// the Character model's runtime context window.
		return characterAgentInputLimitBytes
	}
	return productionAgentInputLimitBytes
}

func compileAgentInput(messages []agentcore.Message, tools []agentcore.ToolSpec) ([]byte, error) {
	return json.Marshal(struct {
		Messages []agentcore.Message  `json:"messages"`
		Tools    []agentcore.ToolSpec `json:"tools"`
	}{Messages: messages, Tools: tools})
}

func unwrapProductionAgentBoundary(model agentcore.ChatModel) agentcore.ChatModel {
	if wrapped, ok := model.(*diagnosticModel); ok {
		return wrapped.ChatModel
	}
	return model
}
