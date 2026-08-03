package adapt

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
)

const validDossierBatchJSON = `{
  "plot_phase": "opening",
  "key_causality": [],
  "plot_threads": [],
  "character_arcs": [],
  "world_constraints": [],
  "major_characters": [],
  "relationship_signals": [],
  "heroine_signals": [],
  "ambiguity_risks": [],
  "couple_milestones": []
}`

func TestGrok45DossierUsesCleanPromptHighReasoningAndStrictSchema(t *testing.T) {
	capture := &dossierContractModel{provider: "grok-oauth", model: "grok-4.5"}
	wrapped := globalprompt.WrapModel(capture)

	_, err := generatePlannerTextForStage(
		context.Background(),
		StageDossier,
		wrapped,
		"structured extraction prompt",
		"source reports",
		6144,
		nil,
		1,
		1,
		"test dossier",
		1,
	)
	if err != nil {
		t.Fatalf("generatePlannerTextForStage: %v", err)
	}

	systemPrompt := capture.messages[0].TextContent()
	if systemPrompt != "structured extraction prompt" {
		t.Fatalf("Grok dossier should preserve the extraction prompt, got %q", systemPrompt)
	}
	if strings.Contains(systemPrompt, globalprompt.TextForModel("grok-oauth/grok-4.5")) {
		t.Fatalf("Grok dossier should not receive the creative global prompt, got %q", systemPrompt)
	}
	if capture.callConfig.ThinkingLevel != agentcore.ThinkingHigh {
		t.Fatalf("thinking level = %q, want high", capture.callConfig.ThinkingLevel)
	}
	format := capture.callConfig.ResponseFormat
	if format == nil || format.Type != agentcore.ResponseFormatJSONSchema || format.JSONSchema == nil {
		t.Fatalf("response format = %#v, want JSON schema", format)
	}
	if format.JSONSchema.Strict == nil || !*format.JSONSchema.Strict {
		t.Fatalf("JSON schema should be strict: %#v", format.JSONSchema)
	}
	if format.JSONSchema.Schema == nil {
		t.Fatal("JSON schema body must not be nil")
	}
}

func TestDeepSeekDossierKeepsExistingGlobalPromptAndJSONMode(t *testing.T) {
	capture := &dossierContractModel{provider: "deepseek-yuanyuAI", model: "deepseek-v4-pro"}
	wrapped := globalprompt.WrapModel(capture)

	_, err := generatePlannerTextForStage(
		context.Background(),
		StageDossier,
		wrapped,
		"structured extraction prompt",
		"source reports",
		6144,
		nil,
		1,
		1,
		"test dossier",
		1,
	)
	if err != nil {
		t.Fatalf("generatePlannerTextForStage: %v", err)
	}

	systemPrompt := capture.messages[0].TextContent()
	if !strings.HasPrefix(systemPrompt, globalprompt.TextForModel("deepseek-yuanyuAI/deepseek-v4-pro")+"\n\n") {
		t.Fatalf("DeepSeek dossier should keep its existing global prompt:\n%s", systemPrompt)
	}
	if body := globalprompt.Strip(systemPrompt); body != "structured extraction prompt" {
		t.Fatalf("DeepSeek role prompt body = %q", body)
	}
	if capture.callConfig.ThinkingLevel != agentcore.ThinkingAuto {
		t.Fatalf("DeepSeek thinking level changed to %q", capture.callConfig.ThinkingLevel)
	}
	format := capture.callConfig.ResponseFormat
	if format == nil || format.Type != agentcore.ResponseFormatJSONObject {
		t.Fatalf("DeepSeek response format = %#v, want existing JSON object mode", format)
	}
}

func TestGrok45DossierDetectionIsModelSpecific(t *testing.T) {
	if !isGrok45ModelIdentity("grok-oauth/grok-4.5") {
		t.Fatal("Grok 4.5 identity should use the dossier compatibility contract")
	}
	if isGrok45ModelIdentity("grok-oauth/grok-4.3-latest") {
		t.Fatal("other Grok models should keep their existing dossier contract")
	}
}

type dossierContractModel struct {
	provider   string
	model      string
	messages   []agentcore.Message
	callConfig agentcore.CallConfig
}

func (m *dossierContractModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.messages = messages
	m.callConfig = agentcore.ResolveCallConfig(opts)
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(validDossierBatchJSON)},
	}}, nil
}

func (m *dossierContractModel) GenerateStream(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	stream := make(chan agentcore.StreamEvent)
	close(stream)
	return stream, nil
}

func (m *dossierContractModel) SupportsTools() bool { return true }

func (m *dossierContractModel) ProviderName() string { return m.provider }

func (m *dossierContractModel) ModelName() string { return m.model }

func (m *dossierContractModel) Info() llm.ModelInfo {
	return llm.ModelInfo{Provider: m.provider, Name: m.model}
}
