package globalprompt

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

func TestApplyPrefixesSystemPrompt(t *testing.T) {
	prefix := Text()
	if prefix == "" {
		t.Fatal("global prompt template must not be empty")
	}

	got := Apply("role prompt")

	if !strings.HasPrefix(got, prefix+"\n\n") {
		t.Fatalf("global prompt was not prepended:\n%s", got)
	}
	if !strings.HasSuffix(got, "role prompt") {
		t.Fatalf("role prompt should remain after the prefix:\n%s", got)
	}
}

func TestDeepSeekGlobalPromptExcludesWritingOnlyContracts(t *testing.T) {
	prefix := TextForModel("deepseek/deepseek-v4-pro")
	for _, writingOnly := range []string{
		"正文生成的反模板约束",
		"单独完成去AI化复检",
		"破折号仅用于真实语气中断",
	} {
		if strings.Contains(prefix, writingOnly) {
			t.Fatalf("writing-only contract %q leaked into the shared DeepSeek prefix", writingOnly)
		}
	}
}

func TestEveryModelGlobalPromptRequiresSimplifiedChineseUserFacingOutput(t *testing.T) {
	for _, model := range []string{
		"deepseek/deepseek-v4-pro",
		"openai/gpt-5.5",
		"xai/grok-4.3-latest",
	} {
		prefix := TextForModel(model)
		for _, contract := range []string{
			"全局输出语言契约",
			"必须使用简体中文",
			"finding 描述",
		} {
			if !strings.Contains(prefix, contract) {
				t.Fatalf("%s global prompt is missing %q", model, contract)
			}
		}
	}
}

func TestApplyForModelSelectsGPTPrompt(t *testing.T) {
	gptPrefix := TextForModel("openai/gpt-5.5")
	deepSeekPrefix := TextForModel("deepseek/deepseek-v4-pro")
	if gptPrefix == "" || deepSeekPrefix == "" {
		t.Fatal("model-specific global prompts must not be empty")
	}

	got := ApplyForModel("openai/gpt-5.5", "role prompt")

	if !strings.HasPrefix(got, gptPrefix+"\n\n") {
		t.Fatalf("GPT prompt was not selected:\n%s", got)
	}
	if body := Strip(got); body != "role prompt" {
		t.Fatalf("global prompt should strip back to the role prompt, got %q", body)
	}
}

func TestApplyForModelSelectsGrokPrompt(t *testing.T) {
	grokPrefix := TextForModel("xai/grok-4.3-latest")
	deepSeekPrefix := TextForModel("deepseek/deepseek-v4-pro")
	gptPrefix := TextForModel("openai/gpt-5.5")
	if grokPrefix == "" {
		t.Fatal("Grok global prompt must not be empty")
	}
	if grokPrefix == deepSeekPrefix || grokPrefix == gptPrefix {
		t.Fatal("Grok prompt should be distinct from DeepSeek/GPT prompts")
	}

	got := ApplyForModel("grok-oauth/grok-4.3-latest", "role prompt")

	if !strings.HasPrefix(got, grokPrefix+"\n\n") {
		t.Fatalf("Grok prompt was not selected:\n%s", got)
	}
	if body := Strip(got); body != "role prompt" {
		t.Fatalf("global prompt should strip back to the role prompt, got %q", body)
	}
}

func TestApplyForModelReplacesExistingGlobalPrompt(t *testing.T) {
	deepSeekPrompt := ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")

	got := ApplyForModel("openai/gpt-5.5", deepSeekPrompt)
	wantPrefix := TextForModel("openai/gpt-5.5")

	if !strings.HasPrefix(got, wantPrefix+"\n\n") {
		t.Fatalf("model switch should replace the existing prefix:\n%s", got)
	}
	if strings.Count(got, "role prompt") != 1 {
		t.Fatalf("role prompt should remain exactly once:\n%s", got)
	}
}

func TestApplyForModelReplacesDeepSeekWithGrokPrompt(t *testing.T) {
	deepSeekPrompt := ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")

	got := ApplyForModel("xai/grok-4.3-latest", deepSeekPrompt)
	wantPrefix := TextForModel("xai/grok-4.3-latest")

	if !strings.HasPrefix(got, wantPrefix+"\n\n") {
		t.Fatalf("model switch should replace the existing prefix:\n%s", got)
	}
	if strings.Count(got, "role prompt") != 1 {
		t.Fatalf("role prompt should remain exactly once:\n%s", got)
	}
}

func TestWrapModelAppliesPromptForCurrentModel(t *testing.T) {
	capture := &captureModel{provider: "openai", model: "gpt-5.5"}
	wrapped := WrapModel(capture)

	_, err := wrapped.Generate(context.Background(), []agentcore.Message{
		agentcore.SystemMsg(ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")),
		agentcore.UserMsg("hello"),
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	systemPrompt := capture.messages[0].TextContent()
	if !strings.HasPrefix(systemPrompt, TextForModel("openai/gpt-5.5")+"\n\n") {
		t.Fatalf("wrapped model did not apply GPT prompt:\n%s", systemPrompt)
	}
	if body := Strip(systemPrompt); body != "role prompt" {
		t.Fatalf("wrapped model should preserve only the role prompt body, got %q", body)
	}
}

func TestWrapModelCanSuppressProviderPromptForScopedCall(t *testing.T) {
	capture := &captureModel{provider: "grok", model: "grok-4.5"}
	wrapped := WrapModel(capture)

	_, err := wrapped.Generate(WithoutModelPrompt(context.Background()), []agentcore.Message{
		agentcore.SystemMsg("structured extraction prompt"),
		agentcore.UserMsg("hello"),
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got := capture.messages[0].TextContent(); got != "structured extraction prompt" {
		t.Fatalf("scoped call should bypass the provider prompt, got %q", got)
	}
}

func TestWrapModelKeepsThinkingHistoryForNonGrokModels(t *testing.T) {
	capture := &captureModel{provider: "openai", model: "gpt-5.5"}
	wrapped := WrapModel(capture)

	_, err := wrapped.Generate(context.Background(), []agentcore.Message{
		agentcore.SystemMsg("role prompt"),
		{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock("private reasoning"),
				agentcore.TextBlock("answer"),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got := capture.messages[1].ThinkingContent(); got != "private reasoning" {
		t.Fatalf("non-Grok history should keep thinking blocks, got %q", got)
	}
}

func TestWrapModelNormalizesGrokThinkingHistoryForStream(t *testing.T) {
	capture := &captureModel{provider: "grok", model: "grok-4.3-latest"}
	wrapped := WrapModel(capture)

	stream, err := wrapped.GenerateStream(context.Background(), []agentcore.Message{
		agentcore.SystemMsg("role prompt"),
		{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock("private reasoning"),
				agentcore.TextBlock("answer"),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	for range stream {
	}

	if got := capture.messages[1].ThinkingContent(); got != "" {
		t.Fatalf("Grok history should not replay provider-native thinking blocks, got %q", got)
	}
	text := capture.messages[1].TextContent()
	if !strings.Contains(text, "<thinking>\nprivate reasoning\n</thinking>") {
		t.Fatalf("Grok history should preserve reasoning as plain text context, got %q", text)
	}
	if !strings.Contains(text, "answer") {
		t.Fatalf("Grok history should preserve assistant text, got %q", text)
	}
}

type captureModel struct {
	provider string
	model    string
	messages []agentcore.Message
}

func (m *captureModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.messages = messages
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock("ok")},
	}}, nil
}

func (m *captureModel) GenerateStream(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.messages = messages
	ch := make(chan agentcore.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *captureModel) SupportsTools() bool { return true }

func (m *captureModel) ProviderName() string { return m.provider }

func (m *captureModel) ModelName() string { return m.model }

func (m *captureModel) Info() llm.ModelInfo {
	return llm.ModelInfo{Provider: m.provider, Name: m.model}
}

func TestApplyIsIdempotent(t *testing.T) {
	first := Apply("role prompt")
	second := Apply(first)

	if second != first {
		t.Fatalf("Apply should not duplicate the global prompt:\nfirst=%q\nsecond=%q", first, second)
	}
}
