package agents

import (
	"context"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// characterModeModel keeps one registered Character Agent while routing each
// independent run to its analyze or review stage model.
type characterModeModel struct {
	analysis agentcore.ChatModel
	review   agentcore.ChatModel

	mu       sync.RWMutex
	lastMode tools.CharacterRunMode
}

func newCharacterModeModel(analysis, review agentcore.ChatModel) *characterModeModel {
	return &characterModeModel{
		analysis: analysis,
		review:   review,
		lastMode: tools.CharacterRunAnalyze,
	}
}

func (m *characterModeModel) Generate(
	ctx context.Context,
	messages []agentcore.Message,
	toolSpecs []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return m.modelForMessages(messages).Generate(ctx, messages, toolSpecs, opts...)
}

func (m *characterModeModel) GenerateStream(
	ctx context.Context,
	messages []agentcore.Message,
	toolSpecs []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	return m.modelForMessages(messages).GenerateStream(ctx, messages, toolSpecs, opts...)
}

func (m *characterModeModel) SupportsTools() bool {
	return m.analysis != nil && m.review != nil &&
		m.analysis.SupportsTools() && m.review.SupportsTools()
}

func (m *characterModeModel) Info() llm.ModelInfo {
	model := m.currentModel()
	if info, ok := model.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info()
	}
	return llm.ModelInfo{}
}

func (m *characterModeModel) currentModel() agentcore.ChatModel {
	m.mu.RLock()
	mode := m.lastMode
	m.mu.RUnlock()
	if mode == tools.CharacterRunReview {
		return m.review
	}
	return m.analysis
}

func (m *characterModeModel) Mode() tools.CharacterRunMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastMode
}

func (m *characterModeModel) modelForMessages(messages []agentcore.Message) agentcore.ChatModel {
	mode := characterModeFromMessages(messages)
	m.mu.Lock()
	m.lastMode = mode
	m.mu.Unlock()
	if mode == tools.CharacterRunReview {
		return m.review
	}
	return m.analysis
}

func characterModeFromMessages(messages []agentcore.Message) tools.CharacterRunMode {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != agentcore.RoleUser {
			continue
		}
		text := strings.ToLower(messages[i].TextContent())
		if strings.Contains(text, `"mode":"review"`) ||
			strings.Contains(text, `"mode": "review"`) ||
			strings.Contains(text, "mode=review") {
			return tools.CharacterRunReview
		}
		if strings.Contains(text, `"mode":"analyze"`) ||
			strings.Contains(text, `"mode": "analyze"`) ||
			strings.Contains(text, "mode=analyze") {
			return tools.CharacterRunAnalyze
		}
	}
	return tools.CharacterRunAnalyze
}
