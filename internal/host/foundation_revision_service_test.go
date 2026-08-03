package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestFoundationStateExposesReadonlyCoreCastContractForUnifiedUI(t *testing.T) {
	st, _ := newConfirmedFoundationRevisionStore(t)
	state, err := NewFoundationRevisionService(st).State()
	if err != nil {
		t.Fatal(err)
	}
	if state.CoreCast == nil || state.CoreCast.ContentSignature == "" {
		t.Fatalf("CoreCast was not exposed: %+v", state)
	}
	if !state.CoreCastConfirmed || state.CoreCast.ConfirmedSignature != state.CoreCast.ContentSignature {
		t.Fatalf("CoreCast confirmation was not exposed: %+v", state.CoreCast)
	}
	if state.CoreCastCompletion == nil || !state.CoreCastCompletion.Complete {
		t.Fatalf("CoreCast completion was not exposed: %+v", state.CoreCastCompletion)
	}
}

func TestFoundationCharacterChangeRequiresCurrentPassingCharacterReview(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Characters[0].Description = "changed without a Character Agent review"
	audit, err := domain.FoundationAuditSignature(base)
	if err != nil {
		t.Fatal(err)
	}

	preview, err := service.Preview(FoundationPreviewRequest{
		ExpectedBaseRevision:       base.Revision,
		ExpectedBaseAuditSignature: audit,
		Candidate:                  candidate,
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if preview.CanApply {
		t.Fatalf("character-changing preview unexpectedly applyable: %+v", preview.Validation)
	}
	if _, err := service.Apply(FoundationApplyRequest{
		PreviewID:      preview.ID,
		IdempotencyKey: "apply-without-character-review",
	}); err == nil {
		t.Fatal("Apply accepted a character change without a current passing review")
	} else {
		var classified *FoundationRevisionError
		if !errors.As(err, &classified) || classified.Code != FoundationErrorCharacterReview {
			t.Fatalf("Apply error = %T %v", err, err)
		}
	}
}

func TestFoundationStateRecognizesPreparedAdaptationBeforePlanExists(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters: []domain.AdaptationSource{{
			Chapter: 1,
			Title:   "Source",
			SHA256:  domain.ContentSignature([]byte("source chapter")),
		}},
	}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	source := domain.AdaptationSourceFoundation{
		Version:            1,
		SourceSignature:    storepkg.AdaptationSourceSignature(manifest),
		SourceChapterCount: 1,
		Premise:            "source premise",
		Characters:         []domain.Character{{ID: "source-hero", Name: "Source Hero", Role: "lead"}},
	}
	if err := st.Adaptation.SaveSourceFoundation(source); err != nil {
		t.Fatal(err)
	}

	state, err := NewFoundationRevisionService(st).State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != "adaptation" {
		t.Fatalf("mode = %q, want adaptation", state.Mode)
	}
	got, ok := state.SourceFoundation.(*domain.AdaptationSourceFoundation)
	if !ok || got == nil || len(got.Characters) != 1 || got.Characters[0].Name != "Source Hero" {
		t.Fatalf("source foundation was not exposed before plan creation: %#v", state.SourceFoundation)
	}
	if state.Editable {
		t.Fatal("prepared source without a confirmed target Foundation must remain readonly")
	}
}

func TestAdaptationFoundationStateDoesNotReportCompletionWithoutPersistedSourceDossier(t *testing.T) {
	st, _ := newConfirmedAdaptationFoundationRevisionStore(t)
	state, err := NewFoundationRevisionService(st).State()
	if err != nil {
		t.Fatal(err)
	}
	if state.CoreCastCompletion == nil || state.CoreCastCompletion.Complete || state.CoreCastConfirmed {
		t.Fatalf("adaptation CoreCast completion ignored missing source evidence: completion=%+v confirmed=%v", state.CoreCastCompletion, state.CoreCastConfirmed)
	}
	found := false
	for _, missing := range state.CoreCastCompletion.Missing {
		found = found || missing.Code == "source_dossier_unavailable"
	}
	if !found {
		t.Fatalf("missing source dossier was not explained: %+v", state.CoreCastCompletion)
	}
}

func TestFoundationRevisionServiceFailsClosedForIncompleteAdaptationBaseline(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{Premise: "immutable source"}); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(st.Dir(), "meta", "adaptation", "plan.json")
	if err := os.WriteFile(planPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewFoundationRevisionService(st)
	_, err := service.Preview(FoundationPreviewRequest{})
	var classified *FoundationRevisionError
	if !errors.As(err, &classified) || classified.Code != FoundationErrorStale {
		t.Fatalf("preview error = %T %v", err, err)
	}
}

func TestAdaptationFoundationPreviewApplyAndRetryPreserveSource(t *testing.T) {
	st, base := newConfirmedAdaptationFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Characters = append(candidate.Characters, domain.Character{ID: "support", Name: "Support", Role: "guide", Description: "helps the lead"})
	stagePassingFoundationCharacterReview(t, st, candidate)
	baseAudit, _ := domain.FoundationAuditSignature(base)
	sourcePath := filepath.Join(st.Dir(), "meta", "adaptation", "source_foundation.json")
	sourceBefore, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if preview.ProjectMode != "adaptation" || preview.AdaptationBaseline == nil || preview.Impact.Adaptation == nil || !preview.CanApply {
		t.Fatalf("adaptation preview=%+v", preview)
	}
	service.SetApplyHookForTesting(func(stage string) error {
		if stage == "after_publication" {
			return errors.New("injected adaptation rebuild failure")
		}
		return nil
	})
	if _, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "adaptation-apply"}); err == nil {
		t.Fatal("injected failure was ignored")
	}
	published, err := st.Foundation.Load()
	if err != nil || published.Revision != base.Revision+1 {
		t.Fatalf("published=%+v err=%v", published, err)
	}
	service.SetApplyHookForTesting(nil)
	retried, err := service.Retry("adaptation-retry")
	if err != nil {
		t.Fatal(err)
	}
	if retried.ProjectMode != "adaptation" || retried.Stage != "regenerating" || retried.SessionID == "" {
		t.Fatalf("retry=%+v", retried)
	}
	if err := st.WithFoundationAdaptationRevisionCommand(retried.SessionID, "partial-regeneration", func() error {
		plan, loadErr := st.Adaptation.LoadPlan()
		if loadErr != nil || plan == nil {
			return errors.Join(loadErr, fmt.Errorf("plan is required"))
		}
		binding, loadErr := st.CurrentAdaptationArtifactBinding()
		if loadErr != nil {
			return loadErr
		}
		plan.FoundationBinding = &binding
		plan.Brief = "partially regenerated proposal"
		return st.Adaptation.SaveProposal(*plan)
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkRegenerationFailure(errors.New("injected failure after partial proposal")); err != nil {
		t.Fatal(err)
	}
	partialRetried, err := service.Retry("adaptation-partial-retry")
	if err != nil {
		t.Fatal(err)
	}
	if partialRetried.SessionID != retried.SessionID || partialRetried.Stage != "regenerating" {
		t.Fatalf("partial retry created a new session or lost its resume boundary: before=%+v after=%+v", retried, partialRetried)
	}
	after, err := st.Foundation.Load()
	if err != nil || after.Revision != published.Revision {
		t.Fatalf("retry republished target: %+v err=%v", after, err)
	}
	sourceAfter, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(sourceBefore, sourceAfter) {
		t.Fatalf("SourceFoundation changed during apply/retry: err=%v", err)
	}
}

func TestAdaptationFoundationPreviewStalesOnBoundContextChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *storepkg.Store)
		code   string
	}{
		{name: "plan", mutate: func(t *testing.T, st *storepkg.Store) {
			plan, err := st.Adaptation.LoadPlan()
			if err != nil || plan == nil {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			plan.Brief += " changed"
			if err := st.Adaptation.SavePlan(*plan); err != nil {
				t.Fatal(err)
			}
		}, code: FoundationErrorStale},
		{name: "intent", mutate: func(t *testing.T, st *storepkg.Store) {
			intent, err := st.Adaptation.LoadCoCreateIntent()
			if err != nil || intent == nil {
				t.Fatalf("intent=%+v err=%v", intent, err)
			}
			intent.RawRequest = "changed adaptation intent"
			intent.IntentHash = domain.ContentSignature([]byte(intent.RawRequest))
			if err := st.Adaptation.SaveCoCreateIntent(*intent); err != nil {
				t.Fatal(err)
			}
		}, code: FoundationErrorStale},
		{name: "workflow", mutate: func(t *testing.T, st *storepkg.Store) {
			workflow, err := st.Adaptation.LoadPlanningWorkflow()
			if err != nil || workflow == nil {
				t.Fatalf("workflow=%+v err=%v", workflow, err)
			}
			if _, err := st.Adaptation.SetPlanningWorkflowStage(workflow.Stage, workflow.Revision); err != nil {
				t.Fatal(err)
			}
		}, code: FoundationErrorStale},
		{name: "target_foundation", mutate: func(t *testing.T, st *storepkg.Store) {
			target, err := st.Foundation.Load()
			if err != nil {
				t.Fatal(err)
			}
			target.Premise += " changed outside preview"
			if _, err := st.Foundation.SaveRevisionCAS(target, target.Revision); err != nil {
				t.Fatal(err)
			}
		}, code: FoundationErrorStale},
		{name: "core_cast", mutate: func(t *testing.T, st *storepkg.Store) {
			contract, err := st.CoreCast.Load()
			if err != nil || contract == nil {
				t.Fatalf("contract=%+v err=%v", contract, err)
			}
			contract.Members[0].MainlineFunction = "changed outside preview"
			saved, err := st.CoreCast.SaveCAS(*contract, contract.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
		}, code: FoundationErrorStale},
		{name: "source_manifest", mutate: func(t *testing.T, st *storepkg.Store) {
			manifest, err := st.Adaptation.LoadSourceManifest()
			if err != nil || manifest == nil {
				t.Fatalf("manifest=%+v err=%v", manifest, err)
			}
			manifest.Chapters[0].SHA256 = domain.ContentSignature([]byte("changed source chapter"))
			if err := st.Adaptation.SaveSourceManifest(*manifest); err != nil {
				t.Fatal(err)
			}
		}, code: FoundationErrorSourceStale},
		{name: "source", mutate: func(t *testing.T, st *storepkg.Store) {
			path := filepath.Join(st.Dir(), "meta", "adaptation", "source_foundation.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte("source premise"), []byte("source changed"), 1)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}, code: FoundationErrorSourceStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, base := newConfirmedAdaptationFoundationRevisionStore(t)
			service := NewFoundationRevisionService(st)
			candidate := domain.CloneStoryFoundation(base)
			candidate.Characters = append(candidate.Characters, domain.Character{ID: "support", Name: "Support", Role: "guide", Description: "helps"})
			stagePassingFoundationCharacterReview(t, st, candidate)
			audit, _ := domain.FoundationAuditSignature(base)
			preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: audit, Candidate: candidate})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, st)
			_, err = service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "stale-" + test.name})
			var classified *FoundationRevisionError
			if !errors.As(err, &classified) || classified.Code != test.code {
				t.Fatalf("apply error=%T %v", err, err)
			}
		})
	}
}

func TestAdaptationFoundationRecoveryRejectsSourceChangeAfterPublication(t *testing.T) {
	st, base := newConfirmedAdaptationFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Characters = append(candidate.Characters, domain.Character{ID: "support", Name: "Support", Role: "guide", Description: "helps"})
	stagePassingFoundationCharacterReview(t, st, candidate)
	audit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: audit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	service.SetApplyHookForTesting(func(stage string) error {
		if stage == "after_publication" {
			return errors.New("stop after target publication")
		}
		return nil
	})
	if _, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "published-source-stale"}); err == nil {
		t.Fatal("publication failpoint was ignored")
	}
	service.SetApplyHookForTesting(nil)
	sourcePath := filepath.Join(st.Dir(), "meta", "adaptation", "source_foundation.json")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, bytes.Replace(source, []byte("source premise"), []byte("changed premise"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = service.Retry("published-source-stale-retry")
	var classified *FoundationRevisionError
	if !errors.As(err, &classified) || classified.Code != FoundationErrorSourceStale {
		t.Fatalf("retry error=%T %v", err, err)
	}
	published, loadErr := st.Foundation.Load()
	if loadErr != nil || published.Revision != base.Revision+1 {
		t.Fatalf("target publication changed during rejected recovery: foundation=%+v err=%v", published, loadErr)
	}
}

func TestAdaptationFoundationCoreChangeRequiresMatchingConfirmedContract(t *testing.T) {
	st, base := newConfirmedAdaptationFoundationRevisionStore(t)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Characters[0].Goal = "a changed core goal"
	audit, _ := domain.FoundationAuditSignature(base)
	preview, err := NewFoundationRevisionService(st).Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: audit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanApply || preview.Impact.Adaptation == nil || !preview.Impact.Adaptation.RequiresCoreCastReconfirmation {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestAdaptationFoundationCoreChangeAcceptsRealReconfirmation(t *testing.T) {
	st, base := newConfirmedAdaptationFoundationRevisionStore(t)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Characters[0].Goal = "a changed core goal"
	contract, err := st.CoreCast.Load()
	if err != nil || contract == nil {
		t.Fatalf("contract=%+v err=%v", contract, err)
	}
	contract.Members[0].Character.Goal = candidate.Characters[0].Goal
	saved, err := st.CoreCast.SaveCAS(*contract, contract.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, completion, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil || !completion.Complete {
		t.Fatalf("completion=%+v err=%v", completion, err)
	}
	stagePassingFoundationCharacterReview(t, st, candidate)
	audit, _ := domain.FoundationAuditSignature(base)
	preview, err := NewFoundationRevisionService(st).Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: audit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply || preview.AdaptationBaseline == nil || !preview.AdaptationBaseline.CoreCastReconfirmed || preview.Impact.RequiresCoreCastConfirmation {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestAdaptationFoundationReviewCompletionUsesSameSessionAndDoesNotStartBody(t *testing.T) {
	st, base := newConfirmedAdaptationFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Characters = append(candidate.Characters, domain.Character{ID: "support", Name: "Support", Role: "guide", Description: "helps"})
	stagePassingFoundationCharacterReview(t, st, candidate)
	audit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: audit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "complete-adaptation"})
	if err != nil || runtime.Stage != "regenerating" {
		t.Fatalf("runtime=%+v err=%v", runtime, err)
	}
	err = st.WithFoundationAdaptationRevisionCommand(runtime.SessionID, "test-regeneration", func() error {
		workflow, loadErr := st.Adaptation.LoadPlanningWorkflow()
		if loadErr != nil || workflow == nil {
			return errors.Join(loadErr, fmt.Errorf("workflow is required"))
		}
		if _, loadErr = st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, workflow.Revision); loadErr != nil {
			return loadErr
		}
		plan, loadErr := st.Adaptation.LoadPlan()
		if loadErr != nil || plan == nil {
			return errors.Join(loadErr, fmt.Errorf("plan is required"))
		}
		binding, loadErr := st.CurrentAdaptationArtifactBinding()
		if loadErr != nil {
			return loadErr
		}
		plan.FoundationBinding = &binding
		plan.Brief = "revalidated target outline"
		domain.MarkAdaptationOutlineQualityPassedWithLayers(plan, domain.ContentSignature([]byte("layered-audit")))
		if loadErr = st.Adaptation.SaveProposal(*plan); loadErr != nil {
			return loadErr
		}
		_, loadErr = st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageProposalReviewPending, -1)
		return loadErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkAdaptationRegenerationReady(); err != nil {
		t.Fatal(err)
	}
	err = st.WithFoundationAdaptationRevisionCommand(runtime.SessionID, "test-confirmation", func() error {
		proposal, loadErr := st.Adaptation.LoadProposal()
		if loadErr != nil || proposal == nil {
			return errors.Join(loadErr, fmt.Errorf("proposal is required"))
		}
		_, loadErr = adapt.ConfirmAdaptationProposal(context.Background(), adapt.Deps{Store: st}, *proposal)
		return loadErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteAdaptationReview(); err != nil {
		t.Fatal(err)
	}
	if active, err := st.Revisions.Active(); err != nil || active != nil {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	finalRuntime, err := st.FoundationRevisions.LoadRuntime()
	if err != nil || finalRuntime == nil || finalRuntime.SessionID != runtime.SessionID || finalRuntime.Stage != "completed" {
		t.Fatalf("runtime=%+v err=%v", finalRuntime, err)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil || progress.Phase != domain.PhaseOutline || progress.CurrentChapter != 0 || len(progress.CompletedChapters) != 0 {
		t.Fatalf("body started unexpectedly: progress=%+v err=%v", progress, err)
	}
}

func TestFoundationRevisionServiceTreatsWritingAsReadonly(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, CurrentChapter: 1}); err != nil {
		t.Fatal(err)
	}
	state, err := NewFoundationRevisionService(st).State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Editable || state.ReadonlyReason != "body_started" {
		t.Fatalf("state = %+v", state)
	}
}

func TestFoundationRevisionServiceTreatsPersistedDraftAsBodyStarted(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseOutline}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveDraft(1, "persisted prose"); err != nil {
		t.Fatal(err)
	}
	state, err := NewFoundationRevisionService(st).State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Editable || state.ReadonlyReason != "body_started" {
		t.Fatalf("state = %+v", state)
	}
}

func TestFoundationRevisionRequiresCoreCastReconfirmationBeforeApply(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Characters[0].Goal = "a different core goal"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := NewFoundationRevisionService(st).Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanApply || !preview.Impact.RequiresCoreCastConfirmation || len(preview.Validation.Warnings) == 0 {
		t.Fatalf("core-cast preview=%+v", preview)
	}
}

func TestFoundationRevisionApplyRetryDoesNotPublishTwice(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "A revised central premise"
	baseAudit, err := domain.FoundationAuditSignature(base)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply || !preview.Impact.FullBook {
		t.Fatalf("preview = %+v", preview)
	}
	service.SetApplyHookForTesting(func(stage string) error {
		if stage == "after_publication" {
			return errors.New("injected regeneration failure")
		}
		return nil
	})
	if _, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "apply-once"}); err == nil {
		t.Fatal("injected failure was ignored")
	}
	published, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != base.Revision+1 {
		t.Fatalf("published revision=%d want=%d", published.Revision, base.Revision+1)
	}
	service.SetApplyHookForTesting(nil)
	retried, err := service.Retry("retry-once")
	if err != nil {
		t.Fatal(err)
	}
	if retried.Stage != "regenerating" {
		t.Fatalf("retry stage=%s", retried.Stage)
	}
	replayed, err := service.Retry("retry-once")
	if err != nil || replayed.RevisionID != retried.RevisionID {
		t.Fatalf("retry replay=%+v err=%v", replayed, err)
	}
	afterRetry, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if afterRetry.Revision != published.Revision {
		t.Fatalf("retry republished Foundation: %d -> %d", published.Revision, afterRetry.Revision)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.ID != retried.SessionID {
		t.Fatalf("active revision=%+v err=%v", active, err)
	}
	if active.Stage != domain.RevisionStageCandidateGenerating || len(active.Approvals) != 1 || active.Approvals[0].StageID != "foundation_apply" || active.Route == nil {
		t.Fatalf("Foundation planning session did not remain active: %+v", active)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil || review.Status != domain.PlanningReviewStatusCollecting {
		t.Fatalf("planning repair review=%+v err=%v", review, err)
	}
}

func TestFoundationRevisionApplyIdempotencyReturnsSameRuntime(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "A revised central premise"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	if first.RevisionID != second.RevisionID || first.Publication.FoundationRevision != second.Publication.FoundationRevision {
		t.Fatalf("idempotent results differ: %+v %+v", first, second)
	}
}

func TestFoundationRevisionRouteLaunchFailureRetriesSameSession(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "route launch retry premise"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "route-launch"})
	if err != nil {
		t.Fatal(err)
	}
	publishedRevision := runtime.Publication.FoundationRevision
	if err := service.MarkRegenerationFailure(errors.New("router launch failed")); err != nil {
		t.Fatal(err)
	}
	failed, err := st.Revisions.Active()
	if err != nil || failed == nil || failed.ID != runtime.SessionID || failed.Stage != domain.RevisionStageFailed || failed.ResumeStage != domain.RevisionStageCandidateGenerating {
		t.Fatalf("failed session=%+v err=%v", failed, err)
	}
	retried, err := service.Retry("route-launch-retry")
	if err != nil || retried.SessionID != runtime.SessionID || retried.Stage != "regenerating" {
		t.Fatalf("retried=%+v err=%v", retried, err)
	}
	foundation, err := st.Foundation.Load()
	if err != nil || foundation.Revision != publishedRevision {
		t.Fatalf("route retry republished Foundation: revision=%d want=%d err=%v", foundation.Revision, publishedRevision, err)
	}
}

func TestFoundationRevisionAwaitsExistingOutlineApprovalBeforeCompletion(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	volumes := []domain.VolumeOutline{{ID: domain.LegacyStructureID("foundation-route", domain.StructureKindVolume, "v1"), Index: 1, Title: "Volume 1", Theme: "repair", Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID("foundation-route", domain.StructureKindArc, "v1-a1"), Index: 1, Title: "Arc 1", Goal: "repair", EstimatedChapters: 1, Chapters: []domain.OutlineEntry{{ID: domain.LegacyStructureID("foundation-route", domain.StructureKindChapter, "c1"), Chapter: 1, Title: "Chapter 1", CoreEvent: "event", Scenes: []string{"scene"}, Hook: "hook"}}}}}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "A revised premise requiring planning repair"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: "approval-boundary"})
	if err != nil || runtime.Stage != "regenerating" {
		t.Fatalf("apply runtime=%+v err=%v", runtime, err)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil {
		t.Fatalf("planning review=%+v err=%v", review, err)
	}
	review.Status = domain.PlanningReviewStatusPending
	review.Kind = domain.PlanningReviewKindVolumeSplit
	if err := st.RunMeta.SetPlanningReview(review); err != nil {
		t.Fatal(err)
	}
	state, err := service.State()
	if err != nil || state.ActiveRevision.Stage != "awaiting_outline_approval" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.Stage != domain.RevisionStageApprovalPending || len(active.AuditExpectations) != 1 || active.AuditExpectations[0].Scope != "planning" {
		t.Fatalf("active planning approval=%+v err=%v", active, err)
	}
	if err := service.ApproveOutline(); err != nil {
		t.Fatal(err)
	}
	if active, err := st.Revisions.Active(); err != nil || active != nil {
		t.Fatalf("completed revision remained active: %+v err=%v", active, err)
	}
}

func TestFoundationRevisionConcurrentApplyCreatesOneActiveRevision(t *testing.T) {
	st, base := newConfirmedFoundationRevisionStore(t)
	service := NewFoundationRevisionService(st)
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "A revised central premise"
	baseAudit, _ := domain.FoundationAuditSignature(base)
	preview, err := service.Preview(FoundationPreviewRequest{ExpectedBaseRevision: base.Revision, ExpectedBaseAuditSignature: baseAudit, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, key := range []string{"concurrent-a", "concurrent-b"} {
		wait.Add(1)
		go func(key string) {
			defer wait.Done()
			_, applyErr := NewFoundationRevisionService(storepkg.NewStore(st.Dir())).Apply(FoundationApplyRequest{PreviewID: preview.ID, IdempotencyKey: key})
			results <- applyErr
		}(key)
	}
	wait.Wait()
	close(results)
	successes := 0
	for applyErr := range results {
		if applyErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent applies=%d want=1", successes)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}

func newConfirmedFoundationRevisionStore(t *testing.T) (*storepkg.Store, domain.StoryFoundation) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash"}); err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash", Members: []domain.CoreCastMember{{Character: domain.Character{ID: "hero", Name: "Hero", Role: "lead", Description: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "lead", Traits: []string{"brave"}, Constraints: []string{"will not betray friends"}}, Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal, MainlineFunction: "drives the mainline", NoCoreRelationships: true}}}
	saved, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	review := &domain.PlanningReview{Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindBlueprint, Brief: "plan"}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatal(err)
	}
	fence := &storepkg.FoundationGenerationFence{Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision}
	if _, err := st.SaveFoundationPremise(fence, "An original premise"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationCharacters(fence, contractCharactersForFoundationTest(contract)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationRelationships(fence, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationWorldRules(fence, []domain.WorldRule{{ID: "rule-1", Category: "society", Rule: "Promises bind", Boundary: "No free escape", Strength: domain.WorldRuleStrengthHard}}); err != nil {
		t.Fatal(err)
	}
	formal, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := domain.FoundationAuditSignature(formal)
	if _, err := st.ConfirmFoundation(formal.Revision, audit); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseOutline, Layered: true}); err != nil {
		t.Fatal(err)
	}
	formal, err = st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st, formal
}

func contractCharactersForFoundationTest(contract domain.CoreCastContract) []domain.Character {
	return domain.ContractCharacters(contract)
}

func stagePassingFoundationCharacterReview(
	t *testing.T,
	st *storepkg.Store,
	candidate domain.StoryFoundation,
) {
	t.Helper()
	_, canonicalBinding, inputs, coreCast, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		t.Fatal(err)
	}
	projected, _, err := domain.ProjectCharacterCandidateCoreCast(candidate, coreCast)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := st.CharacterCards.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:       domain.CharacterCardCandidateVersion,
		Base:          canonicalBinding,
		Foundation:    candidate,
		ProjectedCast: projected,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domain.CharacterCardBindingFromFoundation(saved.Foundation, inputs)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CharacterCards.SaveCAS(domain.CharacterCardLifecycle{
		Version:             domain.CharacterCardLifecycleVersion,
		Mode:                domain.CharacterCardProjectAdaptation,
		Candidate:           binding.Candidate,
		Inputs:              binding.Inputs,
		InputDigest:         binding.InputDigest,
		AnalysisSummary:     "fixture Character Agent analysis",
		Completeness:        []domain.CharacterCardCompletenessResult{},
		AnalysisStatus:      domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:        domain.CharacterCardReviewPassed,
		ReviewedCandidate:   binding.Candidate,
		ReviewedInputDigest: binding.InputDigest,
		ReviewSummary:       "fixture independent Character Agent review",
		Findings:            []domain.CharacterCardReviewFinding{},
		ConfirmationStatus:  domain.CharacterCardUnconfirmed,
		SourceMappings:      []domain.CharacterSourceMapping{},
	}, 0, binding)
	if err != nil {
		t.Fatal(err)
	}
}

func newConfirmedAdaptationFoundationRevisionStore(t *testing.T) (*storepkg.Store, domain.StoryFoundation) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	manifest := domain.AdaptationSourceManifest{SourcePath: "source.txt", ChapterCount: 1, Chapters: []domain.AdaptationSource{{Chapter: 1, Title: "Source", SHA256: domain.ContentSignature([]byte("source chapter"))}}}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{Version: 1, SourceSignature: storepkg.AdaptationSourceSignature(manifest), Premise: "source premise", Characters: []domain.Character{{ID: "source-hero", Name: "Source Hero"}}, WorldRules: []domain.WorldRule{{ID: "source-rule", Category: "other", Rule: "source rule"}}}); err != nil {
		t.Fatal(err)
	}
	intent := domain.AdaptationCoCreateIntent{Version: 1, RawRequest: "adapt faithfully", IntentHash: domain.ContentSignature([]byte("adaptation intent"))}
	if err := st.Adaptation.SaveCoCreateIntent(intent); err != nil {
		t.Fatal(err)
	}
	workflow, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageTargetFoundationGenerating, -1)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{Mode: domain.CoreCastModeAdaptation, DraftRevision: 1, DraftHash: "adaptation-draft", SourceSignature: storepkg.AdaptationSourceSignature(manifest), AdaptationIntentHash: intent.IntentHash})
	if err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeAdaptation, DraftRevision: binding.DraftRevision, DraftHash: binding.DraftHash, SourceSignature: binding.SourceSignature, AdaptationIntentHash: binding.AdaptationIntentHash, Members: []domain.CoreCastMember{{Character: domain.Character{ID: "hero", Name: "Hero", Role: "lead", Description: "target hero", Goal: "protect home", Motivation: "duty", Conflict: "fear", Arc: "grow", Traits: []string{"brave"}, Constraints: []string{"no betrayal"}}, Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal, MainlineFunction: "lead", InclusionRationale: "new adaptation protagonist", NoCoreRelationships: true}}}
	savedContract, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(savedContract.Revision, savedContract.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	target, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	target.Premise = "target premise"
	target.WorldRules = []domain.WorldRule{{ID: "target-rule", Category: "other", Rule: "target rule", Strength: domain.WorldRuleStrengthSoft}}
	review, err := st.SaveAdaptationTargetFoundationCandidate(target, workflow.Revision, "adaptation brief", "")
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := domain.FoundationAuditSignature(current)
	if _, err := st.ConfirmAdaptationTargetFoundation(current.Revision, audit); err != nil {
		t.Fatal(err)
	}
	planBinding := review.Binding
	planBinding.TargetFoundationAuditSignature = audit
	chapterID := domain.LegacyStructureID("adaptation-foundation-test", domain.StructureKindChapter, "1")
	plan := domain.AdaptationPlan{FoundationBinding: &planBinding, Granularity: domain.AdaptationGranularityChapter, RewritePolicy: "faithful", Brief: "adaptation brief", Chapters: []domain.AdaptationChapterPlan{{OutlineEntry: domain.OutlineEntry{ID: chapterID, CoreEvent: "event", Scenes: []string{"scene"}, Hook: "hook"}, Chapter: 1, Title: "Target", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}}}}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseOutline, Layered: true}); err != nil {
		t.Fatal(err)
	}
	current, err = st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if workflow == nil {
		t.Fatal(fmt.Errorf("workflow is missing"))
	}
	return st, current
}
