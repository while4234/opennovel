package agents

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// CharacterTask is the Host/Coordinator-facing dispatch contract. It carries
// identities only; the Character Agent must call character_context to retrieve
// and revalidate the actual bounded evidence in its own run.
type CharacterTask struct {
	RunID       string                          `json:"run_id"`
	Mode        tools.CharacterRunMode          `json:"mode"`
	ProjectMode domain.CharacterCardProjectMode `json:"project_mode"`
	Baseline    domain.CharacterCardBinding     `json:"baseline"`
	Instruction string                          `json:"instruction"`
}

func NewCharacterTask(
	runID string,
	mode tools.CharacterRunMode,
	projectMode domain.CharacterCardProjectMode,
	baseline domain.CharacterCardBinding,
	instruction string,
) (CharacterTask, error) {
	task := CharacterTask{
		RunID:       strings.TrimSpace(runID),
		Mode:        mode,
		ProjectMode: projectMode,
		Baseline:    baseline,
		Instruction: strings.TrimSpace(instruction),
	}
	if err := task.Validate(); err != nil {
		return CharacterTask{}, err
	}
	return task, nil
}

func (t CharacterTask) Validate() error {
	if t.RunID == "" {
		return fmt.Errorf("character task run_id is required")
	}
	if t.Mode != tools.CharacterRunAnalyze && t.Mode != tools.CharacterRunReview {
		return fmt.Errorf("character task mode %q is invalid", t.Mode)
	}
	if t.ProjectMode != domain.CharacterCardProjectOriginal &&
		t.ProjectMode != domain.CharacterCardProjectAdaptation {
		return fmt.Errorf("character task project mode %q is invalid", t.ProjectMode)
	}
	if t.Baseline.Candidate.FoundationRevision < 0 ||
		len(t.Baseline.Candidate.FoundationAuditSignature) != 64 ||
		len(t.Baseline.Candidate.CharacterContentDigest) != 64 ||
		len(t.Baseline.InputDigest) != 64 {
		return fmt.Errorf("character task baseline is incomplete")
	}
	if t.Instruction == "" {
		return fmt.Errorf("character task instruction is required")
	}
	return nil
}

// Prompt serializes the task as a stable machine-readable dispatch. The
// system prompt owns methodology; this payload selects exactly one run mode.
func (t CharacterTask) Prompt() (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("marshal character task: %w", err)
	}
	return "Execute exactly one Character Agent run. Call character_context first, then the matching single save tool. Task JSON:\n" +
		string(payload), nil
}
