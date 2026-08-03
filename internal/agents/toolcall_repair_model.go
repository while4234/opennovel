package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

// ToolCallRepairModel fixes only lossless tool-call argument framing mistakes.
// It never invents missing semantic fields: truncated or otherwise incomplete
// JSON remains invalid so the originating model must retry with real content.
type ToolCallRepairModel struct {
	inner agentcore.ChatModel
}

func NewToolCallRepairModel(inner agentcore.ChatModel) agentcore.ChatModel {
	if inner == nil {
		return nil
	}
	if _, ok := inner.(*ToolCallRepairModel); ok {
		return inner
	}
	return &ToolCallRepairModel{inner: inner}
}

func (m *ToolCallRepairModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	resp, err := m.inner.Generate(ctx, messages, tools, opts...)
	if err != nil || resp == nil {
		return resp, err
	}
	resp.Message = repairToolCallsInMessage(resp.Message)
	return resp, nil
}

func (m *ToolCallRepairModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	stream, err := m.inner.GenerateStream(ctx, messages, tools, opts...)
	if err != nil {
		return nil, err
	}
	out := make(chan agentcore.StreamEvent)
	go func() {
		defer close(out)
		for event := range stream {
			event.Message = repairToolCallsInMessage(event.Message)
			if event.CompletedToolCall != nil {
				repaired := repairToolCall(*event.CompletedToolCall)
				event.CompletedToolCall = &repaired
			}
			out <- event
		}
	}()
	return out, nil
}

func (m *ToolCallRepairModel) SupportsTools() bool {
	return m.inner != nil && m.inner.SupportsTools()
}

func (m *ToolCallRepairModel) ProviderName() string {
	if provider, ok := m.inner.(interface{ ProviderName() string }); ok {
		return provider.ProviderName()
	}
	if info := m.Info(); info.Provider != "" {
		return info.Provider
	}
	return ""
}

func (m *ToolCallRepairModel) ModelName() string {
	if namer, ok := m.inner.(interface{ ModelName() string }); ok {
		return namer.ModelName()
	}
	return m.Info().Name
}

func (m *ToolCallRepairModel) Info() llm.ModelInfo {
	if info, ok := m.inner.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info()
	}
	return llm.ModelInfo{}
}

func (m *ToolCallRepairModel) Capabilities() llm.Capabilities {
	if provider, ok := m.inner.(llm.CapabilityProvider); ok {
		return provider.Capabilities()
	}
	return llm.Capabilities{}
}

func repairToolCallsInMessage(message agentcore.Message) agentcore.Message {
	for i := range message.Content {
		if message.Content[i].ToolCall == nil {
			continue
		}
		repaired := repairToolCall(*message.Content[i].ToolCall)
		message.Content[i].ToolCall = &repaired
	}
	return message
}

func repairToolCall(call agentcore.ToolCall) agentcore.ToolCall {
	if !call.ArgsInvalid {
		return call
	}
	repaired, ok := extractLosslessJSONArguments(call.ArgsRawText)
	if !ok {
		return call
	}
	call.Args = repaired
	call.ArgsInvalid = false
	call.ArgsRawText = ""
	call.ArgsParseError = ""
	return call
}

func extractLosslessJSONArguments(raw string) (json.RawMessage, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	tail := strings.TrimSpace(raw[int(decoder.InputOffset()):])
	if !isIgnorableToolArgsTail(tail) {
		return nil, false
	}
	encoded, err := json.Marshal(value)
	if err != nil || !json.Valid(encoded) {
		return nil, false
	}
	return bytes.TrimSpace(encoded), true
}

func isIgnorableToolArgsTail(tail string) bool {
	if tail == "" {
		return true
	}
	for _, r := range tail {
		switch r {
		case '"', '\'', '`':
			continue
		default:
			return false
		}
	}
	return true
}
