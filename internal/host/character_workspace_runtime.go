package host

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func (h *Host) CharacterWorkspaceService() *CharacterWorkspaceService {
	if h == nil {
		return nil
	}
	return h.characterWorkspace
}

func (h *Host) ExecuteCharacterAgent(ctx context.Context, task agents.CharacterTask) error {
	if h == nil || h.characterAgent == nil {
		return fmt.Errorf("Character Agent runtime is unavailable")
	}
	prompt, err := task.Prompt()
	if err != nil {
		return err
	}
	args, err := json.Marshal(map[string]any{
		"agent": "character",
		"task":  prompt,
	})
	if err != nil {
		return fmt.Errorf("encode Character Agent dispatch: %w", err)
	}
	if _, err := h.characterAgent.Execute(ctx, args); err != nil {
		return fmt.Errorf("execute Character Agent: %w", err)
	}
	return nil
}

func (h *Host) CharacterAgentModelRoute(mode domain.CharacterWorkspaceRunMode) string {
	if h == nil || h.models == nil {
		return ""
	}
	stage := bootstrap.StageCharacterAnalysis
	if mode == domain.CharacterWorkspaceReview {
		stage = bootstrap.StageCharacterReview
	}
	provider, model, ok := h.models.CurrentStageSelection(stage)
	if !ok {
		return ""
	}
	return provider + "/" + model
}
