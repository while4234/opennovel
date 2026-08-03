package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestCharacterWorkspaceServiceAnalyzeReviewAndRetry(t *testing.T) {
	st, staged, _ := stagedOriginalCharacterWorkflow(t)
	if err := st.CharacterCards.DiscardCandidate(
		stagedCandidateDigest(t, staged),
	); err != nil {
		t.Fatalf("DiscardCandidate fixture: %v", err)
	}
	_, canonicalBinding, _, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		t.Fatalf("CurrentCharacterCanonicalBinding: %v", err)
	}
	runtime := &fakeCharacterAgentRuntime{store: st}
	service := NewCharacterWorkspaceService(st, runtime)

	analyzeRequest := CharacterAnalyzeRequest{
		ExpectedBaseRevision:       canonicalBinding.Candidate.FoundationRevision,
		ExpectedBaseAuditSignature: canonicalBinding.Candidate.FoundationAuditSignature,
		IdempotencyKey:             "workspace-analyze-1",
		Scope:                      CharacterAnalysisScope{CharacterIDs: []string{"char-investigator"}},
		Candidate:                  &staged.Foundation,
		CandidateDigest:            stagedCandidateDigest(t, staged),
	}
	analyzeRun, fresh, err := service.PrepareAnalyze(analyzeRequest)
	if err != nil || !fresh {
		t.Fatalf("PrepareAnalyze: fresh=%t err=%v", fresh, err)
	}
	replayed, fresh, err := service.PrepareAnalyze(analyzeRequest)
	if err != nil || fresh || replayed.RunID != analyzeRun.RunID {
		t.Fatalf("PrepareAnalyze replay: run=%q fresh=%t err=%v", replayed.RunID, fresh, err)
	}
	if err := service.Execute(context.Background(), analyzeRun.RunID); err != nil {
		t.Fatalf("Execute analyze: %v", err)
	}
	canonicalAfterAnalyze, err := st.Foundation.Load()
	if err != nil {
		t.Fatalf("Load canonical Foundation after analyze: %v", err)
	}
	canonicalDigest, err := domain.CharacterCardContentDigest(canonicalAfterAnalyze)
	if err != nil {
		t.Fatalf("canonical CharacterCardContentDigest: %v", err)
	}
	if canonicalAfterAnalyze.Revision != canonicalBinding.Candidate.FoundationRevision ||
		canonicalDigest != canonicalBinding.Candidate.CharacterContentDigest {
		t.Fatal("Character analyze mutated canonical Foundation before apply")
	}
	state, err := service.State(analyzeRun.RunID)
	if err != nil {
		t.Fatalf("State analyze: %v", err)
	}
	if state.Run == nil || state.Run.Status != domain.CharacterWorkspaceCompleted ||
		state.Candidate == nil || state.Candidate.Digest != analyzeRequest.CandidateDigest {
		t.Fatalf("analyze state = %#v", state)
	}

	reviewRequest := CharacterReviewRequest{
		ExpectedBaseRevision:       canonicalBinding.Candidate.FoundationRevision,
		ExpectedBaseAuditSignature: canonicalBinding.Candidate.FoundationAuditSignature,
		IdempotencyKey:             "workspace-review-1",
		CandidateRevision:          state.Candidate.Revision,
		CandidateDigest:            state.Candidate.Digest,
	}
	reviewRun, fresh, err := service.PrepareReview(reviewRequest)
	if err != nil || !fresh {
		t.Fatalf("PrepareReview: fresh=%t err=%v", fresh, err)
	}
	if err := service.Execute(context.Background(), reviewRun.RunID); err != nil {
		_, lifecycle, reviewBinding, inspectErr := tools.CurrentCharacterWorkflow(st)
		t.Fatalf(
			"Execute review: %v; inspect=%v run_base=%+v run_input=%s review_binding=%+v lifecycle=%+v",
			err,
			inspectErr,
			reviewRun.Base,
			reviewRun.InputCandidateDigest,
			reviewBinding,
			lifecycle,
		)
	}
	reviewState, err := service.State(reviewRun.RunID)
	if err != nil {
		t.Fatalf("State review: %v", err)
	}
	if reviewState.Run == nil || reviewState.Run.Status != domain.CharacterWorkspaceCompleted ||
		!containsString(reviewState.AllowedOperations, "preview") ||
		!containsString(reviewState.AllowedOperations, "confirm") ||
		reviewState.ConfirmationStatus != domain.CharacterCardUnconfirmed {
		t.Fatalf("review state = %#v", reviewState)
	}

	runtime.failNext = errors.New("provider token=secret request failed")
	failedRequest := analyzeRequest
	failedRequest.IdempotencyKey = "workspace-analyze-failure"
	failedRun, _, err := service.PrepareAnalyze(failedRequest)
	if err != nil {
		t.Fatalf("PrepareAnalyze failure run: %v", err)
	}
	if err := service.Execute(context.Background(), failedRun.RunID); err == nil {
		t.Fatal("Execute analyze failure returned nil")
	}
	failedState, err := service.State(failedRun.RunID)
	if err != nil {
		t.Fatalf("State failed: %v", err)
	}
	if failedState.Run == nil || failedState.Run.Status != domain.CharacterWorkspaceFailed ||
		failedState.Run.Error == nil {
		t.Fatalf("failed state = %#v", failedState)
	}
	retryRun, fresh, err := service.PrepareRetry(CharacterRetryRequest{
		ExpectedBaseRevision:       canonicalBinding.Candidate.FoundationRevision,
		ExpectedBaseAuditSignature: canonicalBinding.Candidate.FoundationAuditSignature,
		RunID:                      failedRun.RunID,
		CandidateDigest:            failedRun.InputCandidateDigest,
		IdempotencyKey:             "workspace-retry-1",
	})
	if err != nil || !fresh || retryRun.Attempt != 2 {
		t.Fatalf("PrepareRetry: run=%#v fresh=%t err=%v", retryRun, fresh, err)
	}
	if err := service.Execute(context.Background(), retryRun.RunID); err != nil {
		t.Fatalf("Execute retry: %v", err)
	}
}

func TestCharacterWorkspaceAnalyzeCanReferencePersistedCandidate(t *testing.T) {
	st, staged, _ := stagedOriginalCharacterWorkflow(t)
	_, binding, _, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		t.Fatal(err)
	}
	service := NewCharacterWorkspaceService(st, &fakeCharacterAgentRuntime{store: st})
	run, fresh, err := service.PrepareAnalyze(CharacterAnalyzeRequest{
		ExpectedBaseRevision:       binding.Candidate.FoundationRevision,
		ExpectedBaseAuditSignature: binding.Candidate.FoundationAuditSignature,
		IdempotencyKey:             "analyze-persisted-candidate",
		Instruction:                "仅修正关系标签",
		CandidateRevision:          staged.Revision,
		CandidateDigest:            stagedCandidateDigest(t, staged),
	})
	if err != nil || !fresh {
		t.Fatalf("PrepareAnalyze persisted candidate: fresh=%t err=%v", fresh, err)
	}
	if run.InputCandidateDigest != stagedCandidateDigest(t, staged) ||
		!reflect.DeepEqual(run.InputCandidate, staged.Foundation) {
		t.Fatalf("analyze input = %+v", run.InputCandidate)
	}
}

func TestCharacterWorkspaceStateLazilyReportsLegacyCardCompletenessWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	legacy, err := st.Foundation.SaveRevisionCAS(domain.StoryFoundation{
		SchemaVersion: domain.StoryFoundationSchemaVersion,
		Premise:       "A legacy project remains readable while its character card awaits review.",
		Characters: []domain.Character{{
			ID: "legacy-lead", Name: "Legacy Lead", Role: "protagonist",
			Description: "An older sparse character card.", Traits: []string{"careful"},
			Tier: string(domain.CharacterTierCore),
		}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	state, err := NewCharacterWorkspaceService(st, nil).State("")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Completeness) != 1 ||
		state.Completeness[0].CharacterID != "legacy-lead" ||
		state.Completeness[0].Status != domain.CharacterCardIncomplete ||
		len(state.Completeness[0].Missing) == 0 {
		t.Fatalf("legacy completeness = %+v", state.Completeness)
	}

	restarted := storepkg.NewStore(dir)
	reloadedState, err := NewCharacterWorkspaceService(restarted, nil).State("")
	if err != nil {
		t.Fatal(err)
	}
	reloadedFoundation, err := restarted.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Completeness, reloadedState.Completeness) ||
		reloadedFoundation.Revision != legacy.Revision {
		t.Fatalf(
			"lazy migration changed across reload: before=%+v after=%+v revision=%d want=%d",
			state.Completeness,
			reloadedState.Completeness,
			reloadedFoundation.Revision,
			legacy.Revision,
		)
	}
}

type fakeCharacterAgentRuntime struct {
	store    *storepkg.Store
	failNext error
}

func (f *fakeCharacterAgentRuntime) CharacterAgentModelRoute(
	mode domain.CharacterWorkspaceRunMode,
) string {
	return "fake/" + string(mode)
}

func (f *fakeCharacterAgentRuntime) ExecuteCharacterAgent(
	ctx context.Context,
	task agents.CharacterTask,
) error {
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	run, err := f.store.CharacterWorkspace.Load(task.RunID)
	if err != nil || run == nil {
		return err
	}
	registry := tools.NewCharacterRunRegistry()
	if _, err := tools.NewCharacterContextTool(f.store, registry).Execute(
		ctx,
		mustWorkspaceJSON(map[string]any{"run_id": task.RunID, "mode": task.Mode}),
	); err != nil {
		return err
	}
	switch task.Mode {
	case tools.CharacterRunAnalyze:
		_, err = tools.NewSaveCharacterCandidateTool(f.store, registry).Execute(ctx, mustWorkspaceJSON(map[string]any{
			"run_id":                 task.RunID,
			"mode":                   task.Mode,
			"idempotency_key":        run.IdempotencyKey,
			"base_revision":          task.Baseline.Candidate.FoundationRevision,
			"base_audit_signature":   task.Baseline.Candidate.FoundationAuditSignature,
			"candidate_digest":       task.Baseline.Candidate.CharacterContentDigest,
			"input_digest":           task.Baseline.InputDigest,
			"analysis_summary":       "fake Character Agent completed bounded analysis",
			"characters":             run.InputCandidate.Characters,
			"relationships":          run.InputCandidate.Relationships,
			"relationships_reviewed": run.InputCandidate.RelationshipsReviewed,
			"source_mappings":        []domain.CharacterSourceMapping{},
		}))
	case tools.CharacterRunReview:
		_, _, binding, bindingErr := tools.CurrentCharacterWorkflow(f.store)
		if bindingErr != nil {
			return bindingErr
		}
		_, err = tools.NewSaveCharacterReviewTool(f.store, registry).Execute(ctx, mustWorkspaceJSON(map[string]any{
			"run_id":               task.RunID,
			"mode":                 task.Mode,
			"idempotency_key":      run.IdempotencyKey,
			"base_revision":        binding.Candidate.FoundationRevision,
			"base_audit_signature": binding.Candidate.FoundationAuditSignature,
			"candidate_digest":     binding.Candidate.CharacterContentDigest,
			"input_digest":         binding.InputDigest,
			"verdict":              "pass",
			"summary":              "fake independent review passed",
			"findings":             []domain.CharacterCardReviewFinding{},
		}))
	default:
		return errors.New("unsupported fake Character Agent mode")
	}
	return err
}

func stagedCandidateDigest(t *testing.T, candidate domain.CharacterCardCandidate) string {
	t.Helper()
	digest, err := domain.CharacterCardContentDigest(candidate.Foundation)
	if err != nil {
		t.Fatalf("CharacterCardContentDigest: %v", err)
	}
	return digest
}

func mustWorkspaceJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
