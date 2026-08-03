package bootstrap

import (
	"context"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

type reasoningCaptureModel struct {
	level agentcore.ThinkingLevel
	caps  llm.Capabilities
}

func (m *reasoningCaptureModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.level = agentcore.ResolveCallConfig(opts).ThinkingLevel
	return &agentcore.LLMResponse{}, nil
}

func (m *reasoningCaptureModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, nil
}

func (m *reasoningCaptureModel) SupportsTools() bool { return true }

func (m *reasoningCaptureModel) Capabilities() llm.Capabilities { return m.caps }

func TestSwappableModelAppliesConfiguredThinkingLast(t *testing.T) {
	base := &reasoningCaptureModel{}
	model := NewSwappableModel("openai", "gpt-5.6-sol", base)
	model.SetThinking("high")
	if _, err := model.Generate(context.Background(), nil, nil, agentcore.WithThinking(agentcore.ThinkingLow)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if base.level != agentcore.ThinkingHigh {
		t.Fatalf("thinking level = %q, want high", base.level)
	}
}

func TestSwappableModelOmitsThinkingForUnsupportedModel(t *testing.T) {
	base := &reasoningCaptureModel{
		caps: llm.Capabilities{
			Thinking: llm.ThinkingCapabilities{Supported: llm.SupportNo},
		},
	}
	model := NewSwappableModel("openai-compatible", "deepseek-chat", base)
	model.SetThinking("high")

	if _, err := model.Generate(context.Background(), nil, nil, agentcore.WithThinking(agentcore.ThinkingLow)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if base.level != agentcore.ThinkingAuto {
		t.Fatalf("thinking level = %q, want provider-default auto", base.level)
	}
}
