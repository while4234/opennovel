package globalprompt

import (
	"context"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

// WrapModel injects the currently active model's global prompt at the LLM
// boundary. This keeps prompts correct after runtime /model switches.
func WrapModel(model agentcore.ChatModel) agentcore.ChatModel {
	if model == nil {
		return nil
	}
	if _, ok := model.(*modelPromptWrapper); ok {
		return model
	}
	return &modelPromptWrapper{inner: model}
}

type modelPromptWrapper struct {
	inner agentcore.ChatModel
}

type suppressModelPromptKey struct{}

// WithoutModelPrompt keeps provider-specific creative-writing instructions out
// of narrowly scoped backend calls such as structured source extraction.
func WithoutModelPrompt(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressModelPromptKey{}, true)
}

func modelPromptSuppressed(ctx context.Context) bool {
	suppressed, _ := ctx.Value(suppressModelPromptKey{}).(bool)
	return suppressed
}

func (m *modelPromptWrapper) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if modelPromptSuppressed(ctx) {
		return m.inner.Generate(ctx, messages, tools, opts...)
	}
	return m.inner.Generate(ctx, m.prepare(messages), tools, opts...)
}

func (m *modelPromptWrapper) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if modelPromptSuppressed(ctx) {
		return m.inner.GenerateStream(ctx, messages, tools, opts...)
	}
	return m.inner.GenerateStream(ctx, m.prepare(messages), tools, opts...)
}

func (m *modelPromptWrapper) SupportsTools() bool {
	return m.inner.SupportsTools()
}

func (m *modelPromptWrapper) ProviderName() string {
	if provider, ok := m.inner.(interface{ ProviderName() string }); ok {
		return provider.ProviderName()
	}
	if info := m.Info(); info.Provider != "" {
		return info.Provider
	}
	return ""
}

func (m *modelPromptWrapper) ModelName() string {
	if namer, ok := m.inner.(interface{ ModelName() string }); ok {
		return namer.ModelName()
	}
	return m.Info().Name
}

func (m *modelPromptWrapper) Info() llm.ModelInfo {
	if info, ok := m.inner.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info()
	}
	return llm.ModelInfo{}
}

func (m *modelPromptWrapper) Capabilities() llm.Capabilities {
	if provider, ok := m.inner.(llm.CapabilityProvider); ok {
		return provider.Capabilities()
	}
	return llm.Capabilities{}
}

func (m *modelPromptWrapper) prepare(messages []agentcore.Message) []agentcore.Message {
	if m.shouldNormalizeGrokHistory() {
		// Grok's compat provider can return reasoning blocks, but rejects them
		// when replayed as history on the next request.
		messages = llm.TransformMessages(messages, "grok")
	}
	return m.apply(messages)
}

func (m *modelPromptWrapper) shouldNormalizeGrokHistory() bool {
	model := strings.ToLower(m.promptModelName())
	return strings.Contains(model, "grok") || strings.Contains(model, "xai")
}

func (m *modelPromptWrapper) apply(messages []agentcore.Message) []agentcore.Message {
	if len(messages) == 0 {
		return messages
	}

	out := append([]agentcore.Message(nil), messages...)
	model := m.promptModelName()
	applied := false
	for i := range out {
		if out[i].Role != agentcore.RoleSystem {
			continue
		}
		out[i].Content = applyToContent(model, out[i].Content)
		applied = true
		break
	}
	if applied {
		return out
	}
	return messages
}

func (m *modelPromptWrapper) promptModelName() string {
	info := m.Info()
	parts := []string{
		m.ProviderName(),
		m.ModelName(),
		info.Provider,
		info.Name,
	}
	return strings.Join(parts, "/")
}

func applyToContent(model string, blocks []agentcore.ContentBlock) []agentcore.ContentBlock {
	if len(blocks) == 0 {
		return []agentcore.ContentBlock{agentcore.TextBlock(TextForModel(model))}
	}

	out := append([]agentcore.ContentBlock(nil), blocks...)
	for i := range out {
		if out[i].Type != agentcore.ContentText {
			continue
		}
		out[i].Text = ApplyForModel(model, out[i].Text)
		return out
	}

	return append([]agentcore.ContentBlock{agentcore.TextBlock(TextForModel(model))}, out...)
}
