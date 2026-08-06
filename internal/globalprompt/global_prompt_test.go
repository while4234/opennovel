package globalprompt

import (
	"context"
	"strings"
	"sync"
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
		"kimi/Kimi-k3",
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

func TestApplyForModelSelectsClaudePromptForOfficialAndCompatibleModels(t *testing.T) {
	claudePrefix := TextForModel("anthropic/claude-opus-5.1")
	deepSeekPrefix := TextForModel("deepseek/deepseek-v4-pro")
	if claudePrefix == "" || claudePrefix == deepSeekPrefix {
		t.Fatal("Claude prompt must be non-empty and distinct from the DeepSeek prompt")
	}
	if !strings.Contains(claudePrefix, "<writing_hardening>") {
		t.Fatal("Claude prompt does not contain the uploaded protocol marker")
	}

	for _, identity := range []string{
		"anthropic/claude-opus-5.1",
		"claude-opus/agy-claude-opus-4-6",
		"custom-openai/opus-5.5",
	} {
		got := ApplyForModel(identity, "role prompt")
		if !strings.HasPrefix(got, claudePrefix+"\n\n") {
			t.Fatalf("Claude prompt was not selected for %q:\n%s", identity, got)
		}
		if body := Strip(got); body != "role prompt" {
			t.Fatalf("Claude prompt should strip back to the role prompt for %q, got %q", identity, body)
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

func TestApplyForModelSelectsGeminiPrompt(t *testing.T) {
	geminiPrefix := TextForModel("google/gemini-3.1-pro")
	deepSeekPrefix := TextForModel("deepseek/deepseek-v4-pro")
	if geminiPrefix == "" {
		t.Fatal("Gemini global prompt must not be empty")
	}
	if geminiPrefix == deepSeekPrefix {
		t.Fatal("Gemini prompt should be distinct from the DeepSeek prompt")
	}
	if !strings.Contains(geminiPrefix, "GEMINI 3.1 PRO - HUMAN-CENTRIC NARRATIVE ENGINE") {
		t.Fatal("Gemini prompt does not contain the uploaded protocol marker")
	}

	got := ApplyForModel("openrouter/google/gemini-3.1-pro", "role prompt")

	if !strings.HasPrefix(got, geminiPrefix+"\n\n") {
		t.Fatalf("Gemini prompt was not selected:\n%s", got)
	}
	if body := Strip(got); body != "role prompt" {
		t.Fatalf("global prompt should strip back to the role prompt, got %q", body)
	}
}

func TestApplyForModelSelectsGeminiPromptForCompatibleAndFutureModels(t *testing.T) {
	wantPrefix := TextForModel("google/gemini-3.1-pro")
	for _, identity := range []string{
		"gemini-3-pro/假流式-gemini-3.1-pro-preview",
		"google/gemini-3.4-pro",
		"custom-openai/gemini-4.0-flash",
	} {
		got := ApplyForModel(identity, "role prompt")
		if !strings.HasPrefix(got, wantPrefix+"\n\n") {
			t.Fatalf("Gemini prompt was not selected for %q:\n%s", identity, got)
		}
	}
}

func TestGeminiGlobalPromptUsesProjectVoiceWithoutCannedHumanity(t *testing.T) {
	prefix := TextForModel("google/gemini-3.1-pro")
	for _, required := range []string{
		"VOICE ALIGNMENT",
		"SCENE FUNCTION",
		"active project style and current role prompt",
		"POV knowledge boundaries",
	} {
		if !strings.Contains(prefix, required) {
			t.Fatalf("Gemini global prompt is missing %q", required)
		}
	}
	for _, canned := range []string{
		"{{char}}",
		"{{user}}",
		"{{worldinfo}}",
		"FORBIDDEN WORD",
		"Human Friction",
		"INTERACTION HOOKS",
		"PHYSIOLOGICAL DETAIL",
		"Internal Thoughts/心理",
	} {
		if strings.Contains(prefix, canned) {
			t.Fatalf("Gemini global prompt still contains canned guidance %q", canned)
		}
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

func TestApplyForModelSelectsKimiPrompt(t *testing.T) {
	kimiPrefix := TextForModel("kimi/Kimi-k3")
	deepSeekPrefix := TextForModel("deepseek/deepseek-v4-pro")
	if kimiPrefix == "" {
		t.Fatal("Kimi global prompt must not be empty")
	}
	if kimiPrefix == deepSeekPrefix {
		t.Fatal("Kimi prompt should be distinct from the DeepSeek prompt")
	}

	got := ApplyForModel("kimi/Kimi-k3", "role prompt")

	if !strings.HasPrefix(got, kimiPrefix+"\n\n") {
		t.Fatalf("Kimi prompt was not selected:\n%s", got)
	}
	if body := Strip(got); body != "role prompt" {
		t.Fatalf("global prompt should strip back to the role prompt, got %q", body)
	}
}

func TestApplyForModelSelectsKimiPromptForMoonshotProvider(t *testing.T) {
	wantPrefix := TextForModel("kimi/Kimi-k3")
	got := ApplyForModel("moonshot/k3", "role prompt")

	if !strings.HasPrefix(got, wantPrefix+"\n\n") {
		t.Fatalf("Moonshot provider should select the Kimi prompt:\n%s", got)
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

func TestWrapModelAppliesGeminiPromptForCurrentModel(t *testing.T) {
	capture := &captureModel{provider: "openrouter", model: "google/gemini-3.1-pro"}
	wrapped := WrapModel(capture)

	_, err := wrapped.Generate(context.Background(), []agentcore.Message{
		agentcore.SystemMsg(ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")),
		agentcore.UserMsg("hello"),
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	systemPrompt := capture.messages[0].TextContent()
	if !strings.HasPrefix(systemPrompt, TextForModel("google/gemini-3.1-pro")+"\n\n") {
		t.Fatalf("wrapped model did not apply Gemini prompt:\n%s", systemPrompt)
	}
	if body := Strip(systemPrompt); body != "role prompt" {
		t.Fatalf("wrapped model should preserve only the role prompt body, got %q", body)
	}
}

func TestWrapModelAppliesClaudePromptForCurrentBackend(t *testing.T) {
	capture := &captureModel{provider: "claude-opus", model: "agy-claude-opus-4-6"}
	wrapped := WrapModel(capture)

	_, err := wrapped.Generate(context.Background(), []agentcore.Message{
		agentcore.SystemMsg(ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")),
		agentcore.UserMsg("hello"),
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	systemPrompt := capture.messages[0].TextContent()
	if !strings.HasPrefix(systemPrompt, TextForModel("claude-opus/agy-claude-opus-4-6")+"\n\n") {
		t.Fatalf("wrapped model did not apply Claude prompt:\n%s", systemPrompt)
	}
	if body := Strip(systemPrompt); body != "role prompt" {
		t.Fatalf("wrapped model should preserve only the role prompt body, got %q", body)
	}
}

func TestWrapModelAppliesKimiPromptForCurrentModel(t *testing.T) {
	capture := &captureModel{provider: "custom-openai", model: "kimi-k3"}
	wrapped := WrapModel(capture)

	_, err := wrapped.Generate(context.Background(), []agentcore.Message{
		agentcore.SystemMsg(ApplyForModel("deepseek/deepseek-v4-pro", "role prompt")),
		agentcore.UserMsg("hello"),
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	systemPrompt := capture.messages[0].TextContent()
	if !strings.HasPrefix(systemPrompt, TextForModel("custom-openai/kimi-k3")+"\n\n") {
		t.Fatalf("wrapped model did not apply Kimi prompt:\n%s", systemPrompt)
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

func TestRegistryOverrideIsAtomicAndPreservesConfiguredSource(t *testing.T) {
	previous := Overrides()
	t.Cleanup(func() { _ = ReplaceOverrides(previous) })

	override := "\n  custom GPT prompt  \n"
	if err := ReplaceOverrides(map[string]string{FamilyGPT: override}); err != nil {
		t.Fatalf("ReplaceOverrides: %v", err)
	}
	if got := Overrides()[FamilyGPT]; got != override {
		t.Fatalf("stored override = %q, want original source %q", got, override)
	}
	if got := TextForModel("openai/gpt-5.5"); got != "custom GPT prompt" {
		t.Fatalf("effective prompt = %q", got)
	}
	applied := ApplyForModel("openai/gpt-5.5", "role prompt")
	if got := Strip(applied); got != "role prompt" {
		t.Fatalf("Strip(override) = %q", got)
	}
}

func TestRegistryOverrideValidation(t *testing.T) {
	tests := []struct {
		name    string
		family  string
		content string
	}{
		{name: "unknown family", family: "other", content: "prompt"},
		{name: "non canonical family", family: "GPT", content: "prompt"},
		{name: "blank", family: FamilyGPT, content: " \r\n\t "},
		{name: "nul", family: FamilyGPT, content: "prompt\x00tail"},
		{name: "too large", family: FamilyGPT, content: strings.Repeat("a", MaxOverrideBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateOverride(test.family, test.content); err == nil {
				t.Fatal("ValidateOverride unexpectedly succeeded")
			}
		})
	}
	if err := ValidateOverride(FamilyGPT, strings.Repeat("a", MaxOverrideBytes)); err != nil {
		t.Fatalf("exact byte limit should be accepted: %v", err)
	}
}

func TestRegistryConcurrentReadersObserveCompleteSnapshots(t *testing.T) {
	registry := NewRegistry()
	first := strings.Repeat("A", 1024)
	second := strings.Repeat("B", 2048)
	if err := registry.ReplaceOverrides(map[string]string{FamilyGPT: first}); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	errors := make(chan string, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 1000 {
				got := registry.textForModel("openai/gpt-5.5")
				if got != first && got != second {
					errors <- got
					return
				}
			}
		}()
	}
	if err := registry.ReplaceOverrides(map[string]string{FamilyGPT: second}); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	close(errors)
	for got := range errors {
		t.Fatalf("reader observed partial snapshot of length %d", len(got))
	}
}

func TestExistingWrappedModelGenerateReplacesRetiredOverride(t *testing.T) {
	previous := Overrides()
	t.Cleanup(func() { _ = ReplaceOverrides(previous) })
	if err := ReplaceOverrides(map[string]string{FamilyGPT: "before GPT prompt"}); err != nil {
		t.Fatal(err)
	}
	capture := &captureModel{provider: "openai", model: "gpt-5.5"}
	wrapped := WrapModel(capture)
	preparedBeforeUpdate := ApplyForModel("openai/gpt-5.5", "role prompt")

	if err := ReplaceOverrides(map[string]string{FamilyGPT: "after GPT prompt"}); err != nil {
		t.Fatal(err)
	}
	_, err := wrapped.Generate(context.Background(), []agentcore.Message{
		agentcore.SystemMsg(preparedBeforeUpdate),
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := capture.messages[0].TextContent(); got != "after GPT prompt\n\nrole prompt" {
		t.Fatalf("generated system prompt = %q", got)
	}
}

func TestExistingWrappedModelGenerateStreamReplacesRetiredOverride(t *testing.T) {
	previous := Overrides()
	t.Cleanup(func() { _ = ReplaceOverrides(previous) })
	if err := ReplaceOverrides(map[string]string{FamilyGPT: "before stream prompt"}); err != nil {
		t.Fatal(err)
	}
	capture := &captureModel{provider: "openai", model: "gpt-5.5"}
	wrapped := WrapModel(capture)
	preparedBeforeUpdate := ApplyForModel("openai/gpt-5.5", "role prompt")

	if err := ReplaceOverrides(map[string]string{FamilyGPT: "after stream prompt"}); err != nil {
		t.Fatal(err)
	}
	stream, err := wrapped.GenerateStream(context.Background(), []agentcore.Message{
		agentcore.SystemMsg(preparedBeforeUpdate),
	}, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	for range stream {
	}
	if got := capture.messages[0].TextContent(); got != "after stream prompt\n\nrole prompt" {
		t.Fatalf("streamed system prompt = %q", got)
	}
}
