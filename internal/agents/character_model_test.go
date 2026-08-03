package agents

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/promptcompile"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestCharacterModeModelRoutesIndependentRuns(t *testing.T) {
	analysisResponse := textResponse("analysis-model")
	reviewResponse := textResponse("review-model")
	analysis := &scriptedRepairModel{response: analysisResponse}
	review := &scriptedRepairModel{response: reviewResponse}
	model := newCharacterModeModel(analysis, review)

	got, err := model.Generate(context.Background(), characterUserMessages(`{"mode":"analyze"}`), nil)
	if err != nil || got != analysisResponse {
		t.Fatalf("analysis route response=%p err=%v", got, err)
	}
	if model.Mode() != tools.CharacterRunAnalyze {
		t.Fatalf("mode=%q, want analyze", model.Mode())
	}

	got, err = model.Generate(context.Background(), characterUserMessages(`{"mode":"review"}`), nil)
	if err != nil || got != reviewResponse {
		t.Fatalf("review route response=%p err=%v", got, err)
	}
	if model.Mode() != tools.CharacterRunReview {
		t.Fatalf("mode=%q, want review", model.Mode())
	}
}

func TestCharacterTaskIsStrictlyModeScoped(t *testing.T) {
	binding := domain.CharacterCardBinding{
		Candidate: domain.CharacterCardCandidateReference{
			FoundationRevision:       1,
			FoundationAuditSignature: strings.Repeat("a", 64),
			CharacterContentDigest:   strings.Repeat("b", 64),
		},
		InputDigest: strings.Repeat("c", 64),
	}
	task, err := NewCharacterTask(
		"review-1",
		tools.CharacterRunReview,
		domain.CharacterCardProjectAdaptation,
		binding,
		"Review the current persisted candidate.",
	)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := task.Prompt()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"run_id":"review-1"`, `"mode":"review"`, `"project_mode":"adaptation"`, "character_context"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("task prompt missing %q: %s", want, prompt)
		}
	}
	if _, err := NewCharacterTask("", tools.CharacterRunAnalyze, domain.CharacterCardProjectOriginal, binding, "analyze"); err == nil {
		t.Fatal("empty run ID was accepted")
	}
}

func TestCharacterRoleMappings(t *testing.T) {
	if got := modelProfileRole(promptcompile.AgentCharacter); got != "character" {
		t.Fatalf("model profile role=%q, want character", got)
	}
}

func characterUserMessages(text string) []agentcore.Message {
	return []agentcore.Message{{
		Role:    agentcore.RoleUser,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(text)},
	}}
}
