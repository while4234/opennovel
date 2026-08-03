package web

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestNormalWorkflowProgressRoutesToCharacterConfirmation(t *testing.T) {
	progress := normalWorkflowProgress("project-character-confirmation", host.UISnapshot{
		PlanningReview: &host.PlanningReviewSummary{
			Status:           domain.PlanningReviewStatusCollecting,
			Kind:             domain.PlanningReviewKindFoundation,
			FoundationStatus: domain.FoundationReviewStatusCollecting,
		},
		CharacterWorkflow: &host.CharacterWorkflowSummary{
			AnalysisStatus:     domain.CharacterCardAnalysisCandidateReady,
			ReviewStatus:       domain.CharacterCardReviewPassed,
			ConfirmationStatus: domain.CharacterCardUnconfirmed,
		},
	}, nil)

	if progress.Status != WorkflowStatusWaitingConfirmation ||
		progress.CurrentStep != "foundation" ||
		progress.Recoverable ||
		progress.Error != "" {
		t.Fatalf("character confirmation progress = %+v", progress)
	}
	if progress.NextAction == nil ||
		progress.NextAction.ID != "confirm_character_candidate" ||
		!progress.NextAction.RequiresConfirmation {
		t.Fatalf("character confirmation next action = %+v", progress.NextAction)
	}
}

func TestProjectSessionResumeReturnsCharacterConfirmationGate(t *testing.T) {
	outputDir := t.TempDir()
	st := storepkg.NewStore(outputDir)
	stageOriginalCharacterConfirmation(t, st)

	fake := newFakeProjectHost()
	session, err := NewProjectSession(
		ProjectManifest{ID: "project-character-confirmation", OutputDir: outputDir},
		fake,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	label, err := session.Resume()
	if err != nil {
		t.Fatalf("Resume returned technical gate error: %v", err)
	}
	if !strings.Contains(label, "确认") || !strings.Contains(label, "角色卡") {
		t.Fatalf("Resume label = %q", label)
	}
	if fake.resumeCalls != 0 {
		t.Fatalf("Host resume calls = %d, want 0 before confirmation", fake.resumeCalls)
	}

	decision, err := session.AutoResumeDecision()
	if err != nil {
		t.Fatal(err)
	}
	if decision.Disposition != AutoResumeWaitUser ||
		decision.ReasonCode != "character_confirmation_required" ||
		decision.Action != "" {
		t.Fatalf("AutoResumeDecision = %+v", decision)
	}
}

func stageOriginalCharacterConfirmation(t *testing.T, st *storepkg.Store) {
	t.Helper()
	base, err := st.Foundation.SaveRevisionCAS(domain.StoryFoundation{
		SchemaVersion: domain.StoryFoundationSchemaVersion,
		Premise:       "调查员必须在保护家人的同时揭露阴谋。",
		WorldRules: []domain.WorldRule{{
			ID:       "rule-evidence",
			Category: "悬疑",
			Rule:     "所有指控都必须有两条独立线索。",
			Strength: domain.WorldRuleStrengthHard,
		}},
		Characters:    []domain.Character{},
		Relationships: []domain.CharacterRelationship{},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginOriginalCharacterReview(&domain.PlanningReview{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "reviewed-original-brief",
	}); err != nil {
		t.Fatal(err)
	}
	_, canonicalBinding, inputs, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		t.Fatal(err)
	}
	candidateFoundation := domain.CloneStoryFoundation(base)
	candidateFoundation.Characters = []domain.Character{completeWebCharacter()}
	candidateFoundation.RelationshipsReviewed = true
	projected, findings, err := domain.ProjectCharacterCandidateCoreCast(candidateFoundation, nil)
	if err != nil || len(findings) != 0 {
		t.Fatalf("project CoreCast findings=%+v err=%v", findings, err)
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(candidateFoundation, &projected)
	if err != nil {
		t.Fatal(err)
	}
	candidateBinding, err := domain.CharacterCardBindingFromFoundation(candidateFoundation, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CharacterCards.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:       domain.CharacterCardCandidateVersion,
		Base:          canonicalBinding,
		Foundation:    candidateFoundation,
		ProjectedCast: projected,
	}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CharacterCards.SaveCAS(domain.CharacterCardLifecycle{
		Version:             domain.CharacterCardLifecycleVersion,
		Mode:                domain.CharacterCardProjectOriginal,
		Candidate:           candidateBinding.Candidate,
		Inputs:              candidateBinding.Inputs,
		InputDigest:         candidateBinding.InputDigest,
		Completeness:        completeness,
		AnalysisStatus:      domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:        domain.CharacterCardReviewPassed,
		ReviewedCandidate:   candidateBinding.Candidate,
		ReviewedInputDigest: candidateBinding.InputDigest,
		ConfirmationStatus:  domain.CharacterCardUnconfirmed,
	}, 0, candidateBinding); err != nil {
		t.Fatal(err)
	}
}

func completeWebCharacter() domain.Character {
	return domain.Character{
		ID:          "char-investigator",
		Name:        "林澈",
		Gender:      "female",
		Role:        "主角调查员",
		Description: "一名在公共真相和家庭安全之间挣扎的纪律严明的调查员。",
		Arc:         "她从控制信息转向接受公开真相的代价。",
		Traits:      []string{"自律", "敏锐"},
		Tier:        string(domain.CharacterTierCore),
		Goal:        "揭露阴谋。",
		Motivation:  "保护妹妹免受相同的制度伤害。",
		Conflict:    "公开真相可能摧毁她的家庭。",
		Voice:       "简短、以证据为先。",
		Constraints: []string{"没有交叉证据时绝不指控。"},
		ContrastDetails: []domain.CharacterContrastDetail{{
			Surface: "冷静", Depth: "妹妹受到威胁时会失去判断",
		}},
		KeyBackstory: []domain.CharacterBackstory{{
			Event: "曾经误认一名证人。", Impact: "此后会复核每条线索。",
		}},
		InitialState: &domain.CharacterInitialState{
			Identity: "调查员", Situation: "收到旧案的新线索", Emotion: "戒备",
			Resources: []string{"封存档案"}, Relationships: "与妹妹疏远",
		},
		KnowledgeBoundary: &domain.CharacterKnowledgeBoundary{
			Known: []string{"官方卷宗"}, Unknown: []string{"阴谋首脑"},
			Misconceptions: []string{"父亲已经离城"}, Forbidden: []string{"首脑身份"},
		},
	}
}
