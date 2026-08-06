package web

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestCharacterResumeRepairAgainstProjectClone(t *testing.T) {
	sourceDir := os.Getenv("OPENNOVEL_CHARACTER_RESUME_SOURCE")
	if sourceDir == "" {
		t.Skip("OPENNOVEL_CHARACTER_RESUME_SOURCE is not set")
	}
	productionRevision, productionDigest, err := readCharacterCandidateIdentity(sourceDir)
	if err != nil {
		t.Fatalf("read production Character candidate before clone: %v", err)
	}
	cloneDir := filepath.Join(t.TempDir(), "output")
	if err := copyTestDirectory(sourceDir, cloneDir); err != nil {
		t.Fatalf("copy production output for state-level test: %v", err)
	}

	st := storepkg.NewStore(cloneDir)
	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil {
		t.Fatalf("load cloned Character candidate: %v", err)
	}
	if candidate == nil {
		t.Fatal("cloned project has no staged Character candidate")
	}
	candidateBinding, err := domain.CharacterCardBindingFromFoundation(candidate.Foundation, candidate.Base.Inputs)
	if err != nil {
		t.Fatalf("bind cloned Character candidate: %v", err)
	}
	lifecycle, err := st.CharacterCards.Load(candidateBinding)
	if err != nil {
		t.Fatalf("load cloned Character lifecycle: %v", err)
	}
	if lifecycle == nil || lifecycle.ConfirmationStatus != domain.CharacterCardUnconfirmed {
		t.Fatalf("clone candidate confirmation status = %+v, want unconfirmed", lifecycle)
	}
	_, currentBinding, _, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		t.Fatalf("read cloned canonical Character binding: %v", err)
	}
	if candidate.Base.Candidate == currentBinding.Candidate && candidate.Base.InputDigest == currentBinding.InputDigest {
		staleCandidate := *candidate
		staleCandidate.Base.Candidate.FoundationAuditSignature = differentSignature(currentBinding.Candidate.FoundationAuditSignature)
		staleCandidate, err = st.CharacterCards.SaveCandidateCAS(staleCandidate, candidate.Revision)
		if err != nil {
			t.Fatalf("stage stale Character binding in clone: %v", err)
		}
		candidate = &staleCandidate
	}
	if candidate.Base.Candidate == currentBinding.Candidate && candidate.Base.InputDigest == currentBinding.InputDigest {
		t.Fatal("clone candidate is current; the fixture does not reproduce the stale resume conflict")
	}
	if _, _, _, workflowErr := tools.CurrentCharacterWorkflow(st); workflowErr == nil || !strings.Contains(workflowErr.Error(), "staged character candidate is stale") {
		t.Fatalf("cloned stale candidate did not reproduce the target tool conflict: %v", workflowErr)
	}

	required, err := host.CharacterConfirmationRequired(st)
	if err != nil {
		t.Fatalf("repair Character confirmation state: %v", err)
	}
	if required {
		t.Fatal("stale unconfirmed candidate should not remain confirmation-ready")
	}

	pending, err := host.OriginalCharacterWorkflowPending(st)
	if err != nil {
		t.Fatalf("read repaired Character workflow state: %v", err)
	}
	if !pending {
		t.Fatal("discarding a stale candidate should return the workflow to pending analysis")
	}
	candidate, err = st.CharacterCards.LoadCandidate()
	if err != nil {
		t.Fatalf("load repaired Character candidate: %v", err)
	}
	if candidate != nil {
		t.Fatal("stale unconfirmed candidate was not discarded")
	}

	lifecycleCloneDir := filepath.Join(t.TempDir(), "output")
	if err := copyTestDirectory(sourceDir, lifecycleCloneDir); err != nil {
		t.Fatalf("copy production output for lifecycle state test: %v", err)
	}
	lifecycleStore := storepkg.NewStore(lifecycleCloneDir)
	lifecycleCandidate, err := lifecycleStore.CharacterCards.LoadCandidate()
	if err != nil || lifecycleCandidate == nil {
		t.Fatalf("load lifecycle-clone candidate: candidate=%+v err=%v", lifecycleCandidate, err)
	}
	rawLifecycle, err := lifecycleStore.CharacterCards.LoadPersistedLifecycle()
	if err != nil || rawLifecycle == nil {
		t.Fatalf("load lifecycle-clone sidecar: lifecycle=%+v err=%v", rawLifecycle, err)
	}
	if rawLifecycle.ConfirmationStatus != domain.CharacterCardUnconfirmed {
		t.Fatalf("lifecycle-clone confirmation status = %q, want unconfirmed", rawLifecycle.ConfirmationStatus)
	}
	_, lifecycleCanonicalBinding, _, _, err := tools.CurrentCharacterCanonicalBinding(lifecycleStore)
	if err != nil {
		t.Fatalf("read lifecycle-clone canonical binding: %v", err)
	}
	if lifecycleCandidate.Base.Candidate != lifecycleCanonicalBinding.Candidate || lifecycleCandidate.Base.InputDigest != lifecycleCanonicalBinding.InputDigest {
		reboundCandidate := *lifecycleCandidate
		reboundCandidate.Base = lifecycleCanonicalBinding
		reboundCandidate, err = lifecycleStore.CharacterCards.SaveCandidateCAS(reboundCandidate, lifecycleCandidate.Revision)
		if err != nil {
			t.Fatalf("make lifecycle-clone candidate Base canonical: %v", err)
		}
		lifecycleCandidate = &reboundCandidate
	}
	lifecycleCandidateBinding, err := domain.CharacterCardBindingFromFoundation(
		lifecycleCandidate.Foundation,
		lifecycleCanonicalBinding.Inputs,
	)
	if err != nil {
		t.Fatalf("bind lifecycle-clone candidate: %v", err)
	}
	divergedBinding := lifecycleCandidateBinding
	divergedBinding.Candidate.FoundationAuditSignature = differentSignature(lifecycleCandidateBinding.Candidate.FoundationAuditSignature)
	divergedLifecycle := *rawLifecycle
	divergedLifecycle.Candidate = divergedBinding.Candidate
	divergedLifecycle.Inputs = divergedBinding.Inputs
	divergedLifecycle.InputDigest = divergedBinding.InputDigest
	divergedLifecycle.ReviewedCandidate = divergedBinding.Candidate
	divergedLifecycle.ReviewedInputDigest = divergedBinding.InputDigest
	if _, err := lifecycleStore.CharacterCards.SaveCAS(divergedLifecycle, rawLifecycle.Revision, divergedBinding); err != nil {
		t.Fatalf("stage diverged lifecycle in clone: %v", err)
	}
	_, staleLifecycle, _, workflowErr := tools.CurrentCharacterWorkflow(lifecycleStore)
	if workflowErr != nil {
		t.Fatalf("lifecycle divergence should reconcile through CurrentCharacterWorkflow: %v", workflowErr)
	}
	if staleLifecycle == nil ||
		staleLifecycle.AnalysisStatus != domain.CharacterCardAnalysisStale ||
		(staleLifecycle.ReviewStatus != domain.CharacterCardReviewNotReviewed &&
			staleLifecycle.ReviewStatus != domain.CharacterCardReviewStale) ||
		staleLifecycle.ConfirmationStatus != domain.CharacterCardUnconfirmed ||
		staleLifecycle.Candidate == lifecycleCandidateBinding.Candidate {
		t.Fatalf("lifecycle clone did not reproduce the stale run binding: %+v", staleLifecycle)
	}
	if required, err := host.CharacterConfirmationRequired(lifecycleStore); err != nil {
		t.Fatalf("repair lifecycle-clone Character state: %v", err)
	} else if required {
		t.Fatal("diverged unconfirmed lifecycle should not remain confirmation-ready")
	}
	if pending, err := host.OriginalCharacterWorkflowPending(lifecycleStore); err != nil {
		t.Fatalf("read lifecycle-clone repaired workflow state: %v", err)
	} else if !pending {
		t.Fatal("discarding a diverged lifecycle should return the workflow to pending analysis")
	}
	if remaining, err := lifecycleStore.CharacterCards.LoadCandidate(); err != nil || remaining != nil {
		t.Fatalf("diverged lifecycle-clone candidate remains: candidate=%+v err=%v", remaining, err)
	}

	afterRevision, afterDigest, err := readCharacterCandidateIdentity(sourceDir)
	if err != nil {
		t.Fatalf("read production Character candidate after clone repair: %v", err)
	}
	if afterRevision != productionRevision || afterDigest != productionDigest {
		t.Fatalf(
			"production Character candidate changed during clone repair: revision %d/%d digest %q/%q",
			productionRevision,
			afterRevision,
			productionDigest,
			afterDigest,
		)
	}
}

func differentSignature(current string) string {
	candidate := strings.Repeat("0", 64)
	if candidate == current {
		return strings.Repeat("1", 64)
	}
	return candidate
}

func readCharacterCandidateIdentity(outputDir string) (int64, string, error) {
	data, err := os.ReadFile(filepath.Join(outputDir, "meta", "character_cards", "candidate.json"))
	if err != nil {
		return 0, "", err
	}
	var candidate domain.CharacterCardCandidate
	if err := json.Unmarshal(data, &candidate); err != nil {
		return 0, "", err
	}
	digest, err := domain.CharacterCardContentDigest(candidate.Foundation)
	if err != nil {
		return 0, "", err
	}
	return candidate.Revision, digest, nil
}

func copyTestDirectory(sourceDir, destinationDir string) error {
	return filepath.WalkDir(sourceDir, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(sourceDir, sourcePath)
		if err != nil {
			return err
		}
		destinationPath := filepath.Join(destinationDir, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(destinationPath, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		return os.WriteFile(destinationPath, data, info.Mode().Perm())
	})
}

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

func TestProjectSessionResumeDiscardsStaleUnconfirmedCharacterCandidate(t *testing.T) {
	outputDir := t.TempDir()
	st := storepkg.NewStore(outputDir)
	stageOriginalCharacterConfirmation(t, st)

	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil {
		t.Fatal("staged candidate is missing")
	}
	candidate.Base.Candidate.FoundationAuditSignature = strings.Repeat("a", 64)
	if _, err := st.CharacterCards.SaveCandidateCAS(*candidate, candidate.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, _, workflowErr := tools.CurrentCharacterWorkflow(st); workflowErr == nil || !strings.Contains(workflowErr.Error(), "staged character candidate is stale") {
		t.Fatalf("injected candidate Base did not reproduce the target tool conflict: %v", workflowErr)
	}

	fake := newFakeProjectHost()
	session, err := NewProjectSession(
		ProjectManifest{ID: "project-stale-character-candidate", OutputDir: outputDir},
		fake,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if _, err := session.Resume(); err != nil {
		t.Fatalf("Resume returned stale candidate conflict: %v", err)
	}
	if fake.resumeCalls != 1 {
		t.Fatalf("Host resume calls = %d, want 1 after stale candidate cleanup", fake.resumeCalls)
	}
	candidate, err = st.CharacterCards.LoadCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if candidate != nil {
		t.Fatalf("stale candidate was not discarded: %+v", candidate.Base)
	}
}

func TestProjectSessionResumeDiscardsDivergedUnconfirmedCharacterLifecycle(t *testing.T) {
	outputDir := t.TempDir()
	st := storepkg.NewStore(outputDir)
	stageOriginalCharacterConfirmation(t, st)

	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil || candidate == nil {
		t.Fatalf("load staged candidate: candidate=%+v err=%v", candidate, err)
	}
	candidateBinding, err := domain.CharacterCardBindingFromFoundation(candidate.Foundation, candidate.Base.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := st.CharacterCards.LoadPersistedLifecycle()
	if err != nil || lifecycle == nil {
		t.Fatalf("load staged lifecycle: lifecycle=%+v err=%v", lifecycle, err)
	}
	divergedBinding := candidateBinding
	divergedBinding.Candidate.FoundationAuditSignature = differentSignature(candidateBinding.Candidate.FoundationAuditSignature)
	divergedLifecycle := *lifecycle
	divergedLifecycle.Candidate = divergedBinding.Candidate
	divergedLifecycle.ReviewedCandidate = divergedBinding.Candidate
	if _, err := st.CharacterCards.SaveCAS(divergedLifecycle, lifecycle.Revision, divergedBinding); err != nil {
		t.Fatal(err)
	}

	_, reconciled, _, workflowErr := tools.CurrentCharacterWorkflow(st)
	if workflowErr != nil {
		t.Fatalf("diverged lifecycle should reconcile to stale instead of returning a Base conflict: %v", workflowErr)
	}
	if reconciled == nil ||
		reconciled.AnalysisStatus != domain.CharacterCardAnalysisStale ||
		reconciled.ReviewStatus != domain.CharacterCardReviewStale ||
		reconciled.ConfirmationStatus != domain.CharacterCardUnconfirmed ||
		reconciled.Candidate == candidateBinding.Candidate {
		t.Fatalf("diverged lifecycle was not detected as stale: %+v", reconciled)
	}

	fake := newFakeProjectHost()
	session, err := NewProjectSession(
		ProjectManifest{ID: "project-stale-character-lifecycle", OutputDir: outputDir},
		fake,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Resume(); err != nil {
		t.Fatalf("Resume returned diverged lifecycle conflict: %v", err)
	}
	if fake.resumeCalls != 1 {
		t.Fatalf("Host resume calls = %d, want 1 after lifecycle cleanup", fake.resumeCalls)
	}
	if current, err := st.CharacterCards.LoadCandidate(); err != nil || current != nil {
		t.Fatalf("diverged unconfirmed candidate remains: candidate=%+v err=%v", current, err)
	}
}

func TestCharacterResumeKeepsStaleConfirmedCandidateStrict(t *testing.T) {
	outputDir := t.TempDir()
	st := storepkg.NewStore(outputDir)
	stageOriginalCharacterConfirmation(t, st)
	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil || candidate == nil {
		t.Fatalf("load staged candidate: candidate=%+v err=%v", candidate, err)
	}
	candidateBinding, err := domain.CharacterCardBindingFromFoundation(candidate.Foundation, candidate.Base.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := st.CharacterCards.LoadPersistedLifecycle()
	if err != nil || lifecycle == nil {
		t.Fatalf("load staged lifecycle: lifecycle=%+v err=%v", lifecycle, err)
	}
	confirmed := *lifecycle
	confirmed.ConfirmationStatus = domain.CharacterCardConfirmed
	if _, err := st.CharacterCards.SaveCAS(confirmed, lifecycle.Revision, candidateBinding); err != nil {
		t.Fatal(err)
	}
	candidate.Base.Candidate.FoundationAuditSignature = differentSignature(candidate.Base.Candidate.FoundationAuditSignature)
	staleCandidate, err := st.CharacterCards.SaveCandidateCAS(*candidate, candidate.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := tools.RepairCharacterWorkflowForResume(st); err == nil {
		t.Fatal("stale confirmed candidate was silently discarded or accepted")
	}
	current, err := st.CharacterCards.LoadCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.Revision != staleCandidate.Revision {
		t.Fatalf("stale confirmed candidate was deleted: %+v", current)
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
