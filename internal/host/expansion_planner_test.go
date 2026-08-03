package host

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type scriptedExpansionRecommender struct {
	recommendation domain.ExpansionRecommendation
	calls          int
}

func (planner *scriptedExpansionRecommender) RecommendExpansion(context.Context, ExpansionContext, domain.ExpansionRequest) (domain.ExpansionRecommendation, error) {
	planner.calls++
	return planner.recommendation, nil
}

func runExpansionAuditCommand(t *testing.T, planner *ExpansionPlanner, session *domain.RevisionSession, key string) (*domain.RevisionSession, error) {
	t.Helper()
	pending, err := planner.RevisionCommand("request_audit", "", session.Revision, key)
	if err != nil {
		return nil, err
	}
	if pending.Stage != domain.RevisionStageCandidateAudit {
		t.Fatalf("request_audit bypassed pending worker boundary: %s", pending.Stage)
	}
	runner, err := NewExpansionAuditRunner(planner.store)
	if err != nil {
		return nil, err
	}
	updated, artifact, err := processExpansionAuditForTest(planner, runner, session.ID)
	if err == nil && artifact.Decision != "pass" {
		t.Logf("independent audit findings: %v", artifact.Findings)
	}
	return updated, err
}

func planExpansion(t *testing.T, planner *ExpansionPlanner, request domain.ExpansionRequest) (*domain.ExpansionPreview, error) {
	t.Helper()
	runner, err := NewExpansionAuditRunner(planner.store)
	if err != nil {
		return nil, err
	}
	for attempts := 0; attempts < 512; attempts++ {
		preview, planErr := planner.Plan(context.Background(), request)
		var pending *ExpansionDependencyAuditPendingError
		if !errors.As(planErr, &pending) {
			return preview, planErr
		}
		review, reviewErr := runner.ProcessDependencyTask(context.Background(), pending.TaskID)
		if reviewErr != nil {
			if strings.Contains(reviewErr.Error(), "not found") {
				continue
			}
			return nil, reviewErr
		}
		if acceptErr := planner.AcceptDependencyReview(pending.TaskID, review); acceptErr != nil {
			if strings.Contains(acceptErr.Error(), "not found") {
				continue
			}
			return nil, acceptErr
		}
	}
	return nil, fmt.Errorf("test dependency graph exceeded bound")
}

func processExpansionAuditForTest(planner *ExpansionPlanner, runner *ExpansionAuditRunner, revisionID string) (*domain.RevisionSession, ExpansionAuditArtifact, error) {
	artifact, err := runner.ProcessRevisionTask(context.Background(), revisionID)
	if err != nil {
		return nil, ExpansionAuditArtifact{}, err
	}
	updated, err := planner.AcceptAuditArtifact(revisionID, artifact)
	return updated, artifact, err
}

func TestExpansionPlannerRunsEveryBundleStepThroughKernelAndKeepsFormalAtomic(t *testing.T) {
	st, current := expansionTestStore(t)
	targetID := current[0].Arcs[0].Chapters[1].ID
	first := domain.CloneStructureSnapshot(current)
	first[0].Arcs[0].Chapters = append(first[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "new-1"), "Bridge")}, first[0].Arcs[0].Chapters[1:]...)...)
	second := domain.CloneStructureSnapshot(first)
	second[0].Arcs[0].Chapters = append(second[0].Arcs[0].Chapters[:2], append([]domain.OutlineEntry{testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "new-2"), "Cost")}, second[0].Arcs[0].Chapters[2:]...)...)
	recommender := &scriptedExpansionRecommender{recommendation: expansionRecommendation(t, domain.ExpansionFormInsertMany, []domain.ExpansionOperation{
		{Operation: domain.StructureRevisionInsertChapter, Intent: "insert bridge", TargetID: targetID, Proposal: testStructureProposal(t, first, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "independent bridge"})},
		{Operation: domain.StructureRevisionInsertChapter, Intent: "insert cost", TargetID: targetID, Proposal: testStructureProposal(t, second, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "independent cost"})},
	})}
	planner := NewExpansionPlanner(st, recommender)
	request := expansionRequest(current, "bundle-plan")
	preview, err := planExpansion(t, planner, request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(preview.KernelPreviews) != 2 || len(preview.Candidate[0].Arcs[0].Chapters) != 5 {
		t.Fatalf("bundle was not fully kernel-sealed: %+v", preview)
	}
	formal, _ := st.Outline.LoadLayeredOutline()
	if domain.StructureSignature(formal) != domain.StructureSignature(current) {
		t.Fatal("planning wrote formal structure")
	}
	replay, err := planExpansion(t, planner, request)
	if err != nil || replay.ID != preview.ID || recommender.calls != 1 {
		t.Fatalf("idempotent plan replay=%+v calls=%d err=%v", replay, recommender.calls, err)
	}
}

func TestExpansionPlannerPreviewStaleCancelExpiryAndSingleConfirm(t *testing.T) {
	st, current := expansionTestStore(t)
	targetID := current[0].Arcs[0].Chapters[1].ID
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "new"), "Bridge")}, candidate[0].Arcs[0].Chapters[1:]...)...)
	recommender := &scriptedExpansionRecommender{recommendation: expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionInsertChapter, Intent: "bridge", TargetID: targetID, Proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "independent bridge"})}})}
	planner := NewExpansionPlanner(st, recommender)
	if _, err := planExpansion(t, planner, domain.ExpansionRequest{Location: domain.ExpansionBefore, ReferenceIDs: []string{targetID}, Sentence: "bridge", Adjustment: domain.ExpansionAdjustmentDefault, ExpectedStructureRevision: 1, ExpectedStructureSignature: "stale", IdempotencyKey: "stale"}); !errors.Is(err, ErrExpansionPreviewStale) {
		t.Fatalf("stale plan error=%v", err)
	}
	cancelled, err := planExpansion(t, planner, expansionRequest(current, "cancel-plan"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Cancel(cancelled.ID, cancelled.BaseRevision, "cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Confirm(context.Background(), cancelled.ID, cancelled.BaseRevision, "confirm-cancelled"); !errors.Is(err, ErrExpansionPreviewCancelled) {
		t.Fatalf("cancelled confirm error=%v", err)
	}

	preview, err := planExpansion(t, planner, expansionRequest(current, "confirm-plan"))
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := planner.Confirm(context.Background(), preview.ID, preview.BaseRevision, "confirm")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if confirmed.Revision.Stage != domain.RevisionStageCandidateAudit || len(confirmed.Revision.CandidateVersionIDs) == 0 {
		t.Fatalf("confirm did not enter the PR-01 audit gate with a persisted aggregate candidate: %+v", confirmed.Revision)
	}
	pending, err := planner.RevisionCommand("request_audit", "", confirmed.Revision.Revision, "expansion-structure-audit")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Stage != domain.RevisionStageCandidateAudit {
		t.Fatalf("request_audit advanced without an independent worker: %s", pending.Stage)
	}
	task := planner.AuditTask(confirmed.Revision.ID)
	if task == nil || task.Status != "pending" {
		t.Fatalf("durable pending audit task missing: %+v", task)
	}
	runner, err := NewExpansionAuditRunner(st)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := runner.ReviewRevision(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	forgedArtifact := artifact
	forgedArtifact.ScopeID = "rev_forged"
	if err := planner.validateExpansionAuditArtifact(forgedArtifact, *task, confirmed.Revision); err == nil {
		t.Fatal("audit artifact with forged scope was accepted")
	}
	forgedArtifact = artifact
	forgedArtifact.ExpectationSet[0].ContentSignature = "forged-content"
	if err := planner.validateExpansionAuditArtifact(forgedArtifact, *task, confirmed.Revision); err == nil {
		t.Fatal("audit artifact with forged expectation signature was accepted")
	}
	// Simulate a process dying after the durable revision command committed but
	// before the in-memory expansion receipt was updated.
	planner.mu.Lock()
	planner.previews[preview.ID].ConfirmedRevisionID = ""
	planner.mu.Unlock()
	replay, err := planner.Confirm(context.Background(), preview.ID, preview.BaseRevision, "confirm-other-tab")
	if err != nil || !replay.Replay || replay.Revision.ID != confirmed.Revision.ID {
		t.Fatalf("two-tab replay=%+v err=%v", replay, err)
	}
	if _, err := planner.RevisionCommand("audit", "client claims pass", replay.Revision.Revision, "forged-client-audit"); err == nil {
		t.Fatal("ordinary client action=audit forged a passing audit")
	}
	session, _, err := processExpansionAuditForTest(planner, runner, replay.Revision.ID)
	if err != nil || session.Stage != domain.RevisionStageApprovalPending {
		t.Fatalf("independent structure audit=%+v err=%v", session, err)
	}
	exactReplay, err := planner.Confirm(context.Background(), preview.ID, preview.BaseRevision, "confirm")
	if err != nil || exactReplay.Revision.Stage != domain.RevisionStageCandidateAudit || exactReplay.Revision.Revision != confirmed.Revision.Revision {
		t.Fatalf("confirm replay drifted with active session progress: %+v err=%v", exactReplay, err)
	}
	session, err = planner.RevisionCommand("approve", "", session.Revision, "expansion-structure-human")
	if err != nil {
		t.Fatal(err)
	}
	if session.Stage == domain.RevisionStageCandidateGenerating {
		session, err = planner.RevisionCommand("outline", "", session.Revision, "expansion-outline-candidate")
		if err != nil {
			t.Fatal(err)
		}
		session, err = runExpansionAuditCommand(t, planner, session, "expansion-outline-audit")
		if err != nil {
			t.Fatal(err)
		}
		session, err = planner.RevisionCommand("approve", "", session.Revision, "expansion-outline-human")
		if err != nil {
			t.Fatal(err)
		}
	}
	if session.Stage == domain.RevisionStageCandidateGenerating {
		session, err = planner.RevisionCommand("prose", "", session.Revision, "expansion-prose-candidate")
		if err != nil {
			t.Fatal(err)
		}
		session, err = runExpansionAuditCommand(t, planner, session, "expansion-prose-audit")
		if err != nil {
			t.Fatal(err)
		}
		session, err = planner.RevisionCommand("approve", "", session.Revision, "expansion-prose-human")
		if err != nil {
			t.Fatal(err)
		}
	}
	if session.Stage != domain.RevisionStageReadyToPublish {
		t.Fatalf("expansion fixed flow stopped at %s", session.Stage)
	}
	published, err := planner.RevisionCommand("publish", "", session.Revision, "expansion-publish")
	if err != nil || published.Stage != domain.RevisionStageCompleted {
		t.Fatalf("publish sealed expansion=%+v err=%v", published, err)
	}
	formal, err := st.Outline.LoadLayeredOutline()
	if err != nil || domain.StructureSignature(formal) != preview.CandidateSignature {
		t.Fatalf("formal expansion candidate was not atomically published: %v", err)
	}
	assertExpansionPublicationAuthorityFiles(t, st.Dir())

	otherStore, otherCurrent := expansionTestStore(t)
	otherTargetID := otherCurrent[0].Arcs[0].Chapters[1].ID
	otherCandidate := domain.CloneStructureSnapshot(otherCurrent)
	otherCandidate[0].Arcs[0].Chapters = append(otherCandidate[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter(domain.LegacyStructureID(otherStore.Dir(), domain.StructureKindChapter, "new"), "Bridge")}, otherCandidate[0].Arcs[0].Chapters[1:]...)...)
	otherRecommender := &scriptedExpansionRecommender{recommendation: expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionInsertChapter, Intent: "bridge", TargetID: otherTargetID, Proposal: testStructureProposal(t, otherCandidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "independent bridge"})}})}
	expiring := NewExpansionPlanner(otherStore, otherRecommender)
	expiring.now = func() time.Time { return time.Unix(100, 0) }
	expired, err := planExpansion(t, expiring, expansionRequest(otherCurrent, "expire-plan"))
	if err != nil {
		t.Fatal(err)
	}
	expiring.now = func() time.Time { return time.Unix(100, 0).Add(31 * time.Minute) }
	if _, err := expiring.Confirm(context.Background(), expired.ID, expired.BaseRevision, "expire-confirm"); !errors.Is(err, ErrExpansionPreviewExpired) {
		t.Fatalf("expired confirm error=%v", err)
	}
	if exact, exactErr := planner.Confirm(context.Background(), preview.ID, preview.BaseRevision, "confirm"); exactErr != nil || !exact.Replay {
		t.Fatalf("same-key durable confirm replay failed after publish: %+v err=%v", exact, exactErr)
	}
	if _, err := planner.Confirm(context.Background(), preview.ID, preview.BaseRevision, "confirm-after-publish-new-key"); !errors.Is(err, ErrExpansionPreviewStale) {
		t.Fatalf("new key replayed a published historical preview: %v", err)
	}
}

func TestExpansionAuditWorkerFailPersistsFindingsAndRetryRequiresNewIndependentArtifact(t *testing.T) {
	st, current := expansionTestStore(t)
	targetID := current[0].Arcs[0].Chapters[1].ID
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "audit-retry"), "Cost")}, candidate[0].Arcs[0].Chapters[1:]...)...)
	recommender := &scriptedExpansionRecommender{recommendation: expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionInsertChapter, Intent: "audit retry", TargetID: targetID, Proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "independent consequence"})}})}
	planner := NewExpansionPlanner(st, recommender)
	preview, err := planExpansion(t, planner, expansionRequest(current, "audit-fail-plan"))
	if err != nil {
		t.Fatal(err)
	}
	confirmation, err := planner.Confirm(context.Background(), preview.ID, preview.BaseRevision, "audit-fail-confirm")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := planner.RevisionCommand("request_audit", "", confirmation.Revision.Revision, "audit-fail-request")
	if err != nil || pending.Stage != domain.RevisionStageCandidateAudit {
		t.Fatalf("enqueue fail audit: %+v %v", pending, err)
	}
	// The candidate artifacts remain signature-consistent and field-complete;
	// the independent auditor rejects the structured dramatic contract itself.
	// No marker, task decision flag, or test endpoint participates in the result.
	taskID := fmt.Sprintf("%s:%d", pending.ID, pending.Revision)
	planner.mu.Lock()
	malformed := planner.pendingAudits[taskID]
	malformed.DramaticAssessment.Result = ""
	planner.pendingAudits[taskID] = malformed
	if err := planner.persistLocked(); err != nil {
		planner.mu.Unlock()
		t.Fatal(err)
	}
	planner.mu.Unlock()
	runner, err := NewExpansionAuditRunner(st)
	if err != nil {
		t.Fatal(err)
	}
	semanticBaseline := malformed
	semanticBaseline.DramaticAssessment = preview.Recommendation.Assessment
	semanticCases := []struct {
		name   string
		mutate func(*ExpansionAuditTask)
	}{
		{name: "causality", mutate: func(task *ExpansionAuditTask) { task.DramaticContract.ChoiceChapterID = "missing-chapter" }},
		{name: "character", mutate: func(task *ExpansionAuditTask) {
			task.DramaticContract.CharacterAfter = task.DramaticContract.CharacterBefore
		}},
		{name: "climax", mutate: func(task *ExpansionAuditTask) { task.DramaticContract.ClimaxTrigger = "" }},
		{name: "exit", mutate: func(task *ExpansionAuditTask) { task.DramaticContract.ExitChapterID = "missing-chapter" }},
		{name: "impact", mutate: func(task *ExpansionAuditTask) {
			task.DramaticContract.RequiredDependencyIDs = []string{"missing-impact"}
		}},
	}
	for _, test := range semanticCases {
		t.Run("semantic-"+test.name, func(t *testing.T) {
			candidateTask := semanticBaseline
			test.mutate(&candidateTask)
			artifact, reviewErr := runner.ReviewRevision(context.Background(), candidateTask)
			if reviewErr != nil || artifact.Decision != "needs_fix" || len(artifact.Findings) == 0 {
				t.Fatalf("signed self-consistent semantic contradiction passed: decision=%s findings=%v err=%v", artifact.Decision, artifact.Findings, reviewErr)
			}
		})
	}
	failed, artifact, err := processExpansionAuditForTest(planner, runner, pending.ID)
	if err != nil || failed.Stage != domain.RevisionStageCandidateAudit || artifact.Decision != "needs_fix" || len(artifact.Findings) == 0 {
		t.Fatalf("failed audit did not stop with findings: session=%+v artifact=%+v err=%v", failed, artifact, err)
	}
	storedTask := planner.AuditTask(failed.ID)
	if storedTask == nil || storedTask.Status != "needs_fix" || len(storedTask.Findings) == 0 {
		t.Fatalf("failed audit was not durable: %+v", storedTask)
	}
	forged := artifact
	_, attackerKey, _ := ed25519.GenerateKey(rand.Reader)
	forged.Decision, forged.Findings = "pass", nil
	forged.Signature = testSignExpansionArtifact(attackerKey, forged)
	if err := planner.validateExpansionAuditArtifact(forged, *storedTask, failed); err == nil {
		t.Fatal("wrong-signer audit artifact was accepted")
	}
	pending, err = planner.RevisionCommand("request_audit", "", failed.Revision, "audit-pass-request")
	if err != nil {
		t.Fatal(err)
	}
	passed, artifact, err := processExpansionAuditForTest(planner, runner, pending.ID)
	if err != nil || artifact.Decision != "pass" || passed.Stage != domain.RevisionStageApprovalPending {
		t.Fatalf("independent retry did not advance after pass: session=%+v artifact=%+v err=%v", passed, artifact, err)
	}
}

func TestExpansionAuditRejectsSelfConsistentCandidateContradictionsWithoutTaskMutation(t *testing.T) {
	tests := []struct {
		name, core, hook, scene string
		mutateTypedClaim        func(*domain.ExpansionDramaticFactSet)
	}{
		{name: "causality", core: "the rescue attempt reaches its final step", hook: "the witnesses record the outcome", scene: "the choice is recorded", mutateTypedClaim: func(value *domain.ExpansionDramaticFactSet) { value.ResultState = "failed" }},
		{name: "character", core: "the protagonist studies the evidence", hook: "the character chooses a response", scene: "the transition is recorded", mutateTypedClaim: func(value *domain.ExpansionDramaticFactSet) { value.CharacterAfter = "proactive" }},
		{name: "climax", core: "the confrontation reaches its turning point", hook: "the parties face the result", scene: "the climax resolves", mutateTypedClaim: func(value *domain.ExpansionDramaticFactSet) { value.ClimaxState = "absent" }},
		{name: "exit", core: "the ally reaches the doorway", hook: "the relationship changes permanently", scene: "the exit is recorded", mutateTypedClaim: func(value *domain.ExpansionDramaticFactSet) { value.ExitState = "reversible" }},
		{name: "impact", core: "the new event changes downstream structure", hook: "dependent artifacts require review", scene: "the impact is recorded", mutateTypedClaim: func(value *domain.ExpansionDramaticFactSet) { value.ImpactState = "recommended" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, current := expansionTestStore(t)
			targetID := current[0].Arcs[0].Chapters[1].ID
			candidate := domain.CloneStructureSnapshot(current)
			contradictory := testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "typed-contradiction-"+test.name), "Contradiction")
			contradictory.CoreEvent, contradictory.Hook, contradictory.Scenes = test.core, test.hook, []string{test.scene}
			candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{contradictory}, candidate[0].Arcs[0].Chapters[1:]...)...)
			recommendation := expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionInsertChapter, Intent: "public semantic contradiction", TargetID: targetID, Proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "typed semantic contradiction"})}})
			test.mutateTypedClaim(recommendation.Assessment.TypedClaims)
			recommender := &scriptedExpansionRecommender{recommendation: recommendation}
			planner := NewExpansionPlanner(st, recommender)
			preview, err := planExpansion(t, planner, expansionRequest(current, "semantic-public-plan-"+test.name))
			if err != nil {
				t.Fatal(err)
			}
			confirmation, err := planner.Confirm(context.Background(), preview.ID, preview.BaseRevision, "semantic-public-confirm-"+test.name)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := planner.RevisionCommand("request_audit", "", confirmation.Revision.Revision, "semantic-public-audit-"+test.name)
			if err != nil {
				t.Fatal(err)
			}
			taskID := fmt.Sprintf("%s:%d", pending.ID, pending.Revision)
			before, err := st.LoadExpansionRuntime()
			if err != nil {
				t.Fatal(err)
			}
			beforePayload := append([]byte(nil), before.PendingAudits[taskID]...)
			runner, err := NewExpansionAuditRunner(st)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := runner.ProcessRevisionTask(context.Background(), pending.ID)
			if err != nil || artifact.Decision != "needs_fix" {
				t.Fatalf("public signed candidate contradiction passed: decision=%s findings=%v err=%v", artifact.Decision, artifact.Findings, err)
			}
			after, err := st.LoadExpansionRuntime()
			if err != nil {
				t.Fatal(err)
			}
			if string(beforePayload) != string(after.PendingAudits[taskID]) {
				t.Fatal("independent semantic negative mutated the persisted pending task")
			}
		})
	}
}

func TestExpansionCancelReceiptRejectsKeyReuseForAnotherPreview(t *testing.T) {
	st, current := expansionTestStore(t)
	targetID := current[0].Arcs[0].Chapters[1].ID
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "cancel-receipt"), "After"))
	recommender := &scriptedExpansionRecommender{recommendation: expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Intent: "after", Proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "after"})}})}
	planner := NewExpansionPlanner(st, recommender)
	first, err := planExpansion(t, planner, expansionRequest(current, "cancel-first"))
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := expansionRequest(current, "cancel-second")
	secondRequest.Sentence = "another sentence"
	second, err := planExpansion(t, planner, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Cancel(first.ID, first.BaseRevision, "cancel-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Cancel(second.ID, second.BaseRevision, "cancel-key"); !errors.Is(err, storepkg.ErrManuscriptIdempotencyConflict) {
		t.Fatalf("cancel key conflict=%v", err)
	}
	_ = targetID
}

func TestExpansionRuntimeSurvivesRestartAndRejectsFingerprintReuse(t *testing.T) {
	st, current := expansionTestStore(t)
	targetID := current[0].Arcs[0].Chapters[1].ID
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "restart"), "Restart"))
	recommendation := expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Intent: "restart", TargetID: targetID, Proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "restart"})}})
	firstRecommender := &scriptedExpansionRecommender{recommendation: recommendation}
	first := NewExpansionPlanner(st, firstRecommender)
	request := expansionRequest(current, "durable-plan")
	preview, err := planExpansion(t, first, request)
	if err != nil {
		t.Fatal(err)
	}
	restartedRecommender := &scriptedExpansionRecommender{recommendation: recommendation}
	restarted := NewExpansionPlanner(storepkg.NewStore(st.Dir()), restartedRecommender)
	replayed, err := planExpansion(t, restarted, request)
	if err != nil || replayed.ID != preview.ID || restartedRecommender.calls != 0 {
		t.Fatalf("restart replay=%+v calls=%d err=%v", replayed, restartedRecommender.calls, err)
	}
	changed := request
	changed.Sentence = "different payload"
	if _, err := planExpansion(t, restarted, changed); !errors.Is(err, storepkg.ErrManuscriptIdempotencyConflict) {
		t.Fatalf("same key accepted another fingerprint: %v", err)
	}
	confirmation, err := restarted.Confirm(context.Background(), preview.ID, preview.BaseRevision, "durable-confirm")
	if err != nil || confirmation.Revision.Stage != domain.RevisionStageCandidateAudit {
		t.Fatalf("restart confirmation=%+v err=%v", confirmation, err)
	}
	if _, err := restarted.Confirm(context.Background(), preview.ID, preview.BaseRevision+1, "durable-confirm"); !errors.Is(err, ErrExpansionPreviewStale) {
		t.Fatalf("confirm key accepted another expected revision: %v", err)
	}
	if _, err := restarted.Confirm(context.Background(), preview.ID, preview.BaseRevision+1, "new-stale-confirm-key"); !errors.Is(err, ErrExpansionPreviewStale) {
		t.Fatalf("confirmed preview accepted a new key with stale expected revision: %v", err)
	}
}

func TestExpansionPlannerConcurrentPlanAndConfirmAreLinearizable(t *testing.T) {
	st, current := expansionTestStore(t)
	targetID := current[0].Arcs[0].Chapters[1].ID
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters[:1], append([]domain.OutlineEntry{testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "concurrent"), "Bridge")}, candidate[0].Arcs[0].Chapters[1:]...)...)
	recommender := &scriptedExpansionRecommender{recommendation: expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionInsertChapter, Intent: "bridge", TargetID: targetID, Proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "independent bridge"})}})}
	planner := NewExpansionPlanner(st, recommender)
	request := expansionRequest(current, "concurrent-plan")
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			preview, err := planExpansion(t, planner, request)
			if err != nil {
				errs <- err
				return
			}
			ids <- preview.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Plan: %v", err)
	}
	var previewID string
	for id := range ids {
		if previewID == "" {
			previewID = id
		}
		if id != previewID {
			t.Fatalf("concurrent plan produced multiple previews: %s != %s", id, previewID)
		}
	}
	if recommender.calls != 1 {
		t.Fatalf("recommender calls=%d want 1", recommender.calls)
	}
	preview, _ := planner.Get(previewID)
	revisions := make(chan string, 2)
	errs = make(chan error, 2)
	for _, key := range []string{"tab-a", "tab-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			result, err := planner.Confirm(context.Background(), previewID, preview.BaseRevision, key)
			if err != nil {
				errs <- err
				return
			}
			revisions <- result.Revision.ID
		}(key)
	}
	wg.Wait()
	close(revisions)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Confirm: %v", err)
	}
	var revisionID string
	for id := range revisions {
		if revisionID == "" {
			revisionID = id
		}
		if id != revisionID {
			t.Fatalf("concurrent confirm produced multiple revisions")
		}
	}
}

func TestExpansionAdjustIsSourceBoundAtomicAndDurable(t *testing.T) {
	st, current := expansionTestStore(t)
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "adjusted"), "Adjusted"))
	recommender := &scriptedExpansionRecommender{recommendation: expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Intent: "adjust", Proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "adjust"})}})}
	planner := NewExpansionPlanner(st, recommender)
	source, err := planExpansion(t, planner, expansionRequest(current, "adjust-source"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := planner.Adjust(context.Background(), source.ID, source.BaseRevision, domain.ExpansionAdjustmentFull, "make the turn explicit", "adjust-atomic")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := planner.Get(source.ID)
	if !old.Obsolete || next.Obsolete || next.ID == source.ID {
		t.Fatalf("adjust did not atomically tombstone source and create successor: old=%+v next=%+v", old, next)
	}
	restarted := NewExpansionPlanner(storepkg.NewStore(st.Dir()), recommender)
	replay, err := restarted.Adjust(context.Background(), source.ID, source.BaseRevision, domain.ExpansionAdjustmentFull, "make the turn explicit", "adjust-atomic")
	if err != nil || replay.ID != next.ID || replay.Signature != next.Signature {
		t.Fatalf("durable adjust replay=%+v err=%v", replay, err)
	}
	other, err := planExpansion(t, restarted, expansionRequest(current, "other-source"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Adjust(context.Background(), other.ID, other.BaseRevision, domain.ExpansionAdjustmentFull, "make the turn explicit", "adjust-atomic"); !errors.Is(err, storepkg.ErrManuscriptIdempotencyConflict) {
		t.Fatalf("adjust key was reusable across source previews: %v", err)
	}
}

func TestConfirmedNormalExpansionAdjustRecoversCommitWriteFailureAndReplaysOneResult(t *testing.T) {
	st, current := expansionTestStore(t)
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, testChapter(domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "adjust-confirmed"), "Adjusted"))
	recommender := &scriptedExpansionRecommender{recommendation: expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Intent: "adjust", Proposal: testStructureProposal(t, candidate, domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "adjust"})}})}
	planner := NewExpansionPlanner(st, recommender)
	source, err := planExpansion(t, planner, expansionRequest(current, "confirmed-adjust-source"))
	if err != nil {
		t.Fatal(err)
	}
	confirmation, err := planner.Confirm(context.Background(), source.ID, source.BaseRevision, "confirmed-adjust-confirm")
	if err != nil {
		t.Fatal(err)
	}
	feedback, err := planner.RevisionCommand("feedback", "repair the causal bridge", confirmation.Revision.Revision, "confirmed-adjust-feedback")
	if err != nil || feedback.Stage != domain.RevisionStageCandidateGenerating {
		t.Fatalf("feedback=%+v err=%v", feedback, err)
	}
	rebindInjected := false
	restoreFault := st.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
		if rel == "meta/revisions/state.json" && stage == "after_replace" && !rebindInjected {
			rebindInjected = true
			return errors.New("injected committed normal revision rebind error")
		}
		return nil
	})
	_, err = planner.Adjust(context.Background(), source.ID, source.BaseRevision, domain.ExpansionAdjustmentFull, "make the causal turn explicit", "confirmed-adjust")
	if err == nil || !rebindInjected {
		t.Fatalf("adjust did not expose the committed-but-error rebind: injected=%v err=%v", rebindInjected, err)
	}
	restoreFault()
	restarted := NewExpansionPlanner(storepkg.NewStore(st.Dir()), recommender)
	if restarted.initErr != nil {
		t.Fatal(restarted.initErr)
	}
	replay, err := restarted.Adjust(context.Background(), source.ID, source.BaseRevision, domain.ExpansionAdjustmentFull, "make the causal turn explicit", "confirmed-adjust")
	if err != nil {
		t.Fatal(err)
	}
	active, err := restarted.ActiveRevision()
	if err != nil || active.PreviewSignature != replay.Signature {
		t.Fatalf("active revision was not rebound to recovered preview: active=%+v replay=%+v err=%v", active, replay, err)
	}
	old, err := restarted.Get(source.ID)
	if err != nil || !old.Obsolete || replay.Obsolete {
		t.Fatalf("recovery did not leave exactly the successor live: old=%+v replay=%+v err=%v", old, replay, err)
	}
	runtime, err := st.LoadExpansionRuntime()
	if err != nil || len(runtime.PendingAdjustments) != 0 || len(runtime.Receipts) == 0 {
		t.Fatalf("adjust recovery journal/receipt mismatch: pending=%d receipts=%d err=%v", len(runtime.PendingAdjustments), len(runtime.Receipts), err)
	}
}

func TestConfirmedAdaptationExpansionAdjustRecoversCommitWriteFailureAndReplaysOneResult(t *testing.T) {
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityChapter, false)
	if _, err := NewExpansionAuditRunner(st); err != nil {
		t.Fatal(err)
	}
	baseStructure := adaptationExpansionStructure(base)
	if err := st.Outline.SaveLayeredOutline(baseStructure); err != nil {
		t.Fatal(err)
	}
	delta, _ := domain.NewDynamicSoftBudget(1, 2500, 4500)
	proposalBudget, _ := domain.NewDynamicSoftBudget(len(candidate.Chapters), 2500, 4500)
	causalImpacts := make([]domain.StructureImpactItem, 0, len(base.Chapters))
	for _, chapter := range base.Chapters {
		causalImpacts = append(causalImpacts, domain.StructureImpactItem{ArtifactID: chapter.ID, ArtifactKind: domain.StructureKindChapter, Change: "verify adjacent protected contract", Level: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"added bridge changes adjacent contract review"}, DependencySourceIDs: []string{adaptationTestAddedID}})
	}
	recommendation := expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Intent: "add bridge", Proposal: domain.StructureRevisionProposal{Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "coverage-safe bridge"}, Candidate: adaptationExpansionStructure(candidate), Impacts: causalImpacts, SoftBudget: proposalBudget}}})
	recommendation.Assessment.AdaptationEffect = "coverage and protected contracts remain complete"
	recommendation.AdaptationCandidate = &candidate
	recommendation.ChapterCount, recommendation.ChapterMinWords, recommendation.ChapterMaxWords = 1, 2500, 4500
	recommendation.TotalMinWords, recommendation.TotalMaxWords, recommendation.SoftBudgetDelta = delta.TotalMinWords, delta.TotalMaxWords, delta
	recommender := &scriptedExpansionRecommender{recommendation: recommendation}
	planner := NewExpansionPlanner(st, recommender)
	request := domain.ExpansionRequest{Location: domain.ExpansionAfter, ReferenceIDs: []string{base.Chapters[len(base.Chapters)-1].ID}, Sentence: "add a coverage-safe bridge", Adjustment: domain.ExpansionAdjustmentDefault, ExpectedStructureRevision: domain.StructureRevision(baseStructure), ExpectedStructureSignature: domain.StructureSignature(baseStructure), IdempotencyKey: "adaptation-confirmed-adjust-source"}
	source, err := planExpansion(t, planner, request)
	if err != nil {
		t.Fatal(err)
	}
	confirmation, err := planner.Confirm(context.Background(), source.ID, source.BaseRevision, "adaptation-confirmed-adjust-confirm")
	if err != nil {
		t.Fatal(err)
	}
	feedback, err := planner.RevisionCommand("feedback", "repair protected contract evidence", confirmation.Revision.Revision, "adaptation-confirmed-adjust-feedback")
	if err != nil || feedback.Stage != domain.RevisionStageCandidateGenerating {
		t.Fatalf("feedback=%+v err=%v", feedback, err)
	}
	adjustedCandidate, err := cloneAdaptationPlan(candidate)
	if err != nil {
		t.Fatal(err)
	}
	adjustedCandidate.Chapters[len(adjustedCandidate.Chapters)-1].CoverageNote += "; feedback-bound ownership evidence"
	adjustedRecommendation := recommendation
	adjustedRecommendation.AdaptationCandidate = &adjustedCandidate
	recommender.recommendation = adjustedRecommendation
	rebindInjected := false
	restoreRebindFault := st.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
		if rel == "meta/revisions/state.json" && stage == "after_replace" && !rebindInjected {
			var prepared storepkg.ExpansionRuntime
			payload, readErr := os.ReadFile(filepath.Join(st.Dir(), filepath.FromSlash(".ai/revisions/expansion-runtime.json")))
			if readErr == nil && json.Unmarshal(payload, &prepared) == nil && len(prepared.PendingAdjustments) > 0 {
				rebindInjected = true
				return errors.New("injected adaptation revision rebind write failure")
			}
		}
		return nil
	})
	_, err = planner.Adjust(context.Background(), source.ID, source.BaseRevision, domain.ExpansionAdjustmentFull, "make protected ownership explicit", "adaptation-rebind-failure")
	if err == nil || !rebindInjected {
		t.Fatalf("adaptation rebind fault was not observed: injected=%v err=%v", rebindInjected, err)
	}
	restoreRebindFault()
	recoveredStore := storepkg.NewStore(st.Dir())
	planner = NewExpansionPlanner(recoveredStore, recommender)
	if planner.initErr != nil {
		t.Fatal(planner.initErr)
	}
	replay, err := planner.Adjust(context.Background(), source.ID, source.BaseRevision, domain.ExpansionAdjustmentFull, "make protected ownership explicit", "adaptation-rebind-failure")
	if err != nil {
		t.Fatal(err)
	}
	active, err := planner.ActiveRevision()
	if err != nil || active.PreviewSignature != replay.RevisionPreviewSignature {
		t.Fatalf("adaptation revision was not rebound to recovered preview: active=%+v replay=%+v err=%v", active, replay, err)
	}
	old, err := planner.Get(source.ID)
	if err != nil || !old.Obsolete || replay.Obsolete {
		t.Fatalf("adaptation recovery did not leave exactly one live successor: old=%+v replay=%+v err=%v", old, replay, err)
	}
	runtime, err := recoveredStore.LoadExpansionRuntime()
	if err != nil || len(runtime.PendingAdjustments) != 0 || len(runtime.Receipts) == 0 {
		t.Fatalf("adaptation adjust recovery journal/receipt mismatch: pending=%d receipts=%d err=%v", len(runtime.PendingAdjustments), len(runtime.Receipts), err)
	}
}

func TestValidateExpansionFormDeterministicMatrix(t *testing.T) {
	op := func(kind domain.StructureRevisionOperation) domain.ExpansionOperation {
		return domain.ExpansionOperation{Operation: kind}
	}
	cases := []struct {
		name       string
		form       domain.ExpansionForm
		operations []domain.ExpansionOperation
		chapters   int
	}{
		{"expand", domain.ExpansionFormExpandCurrent, []domain.ExpansionOperation{op(domain.StructureRevisionExpandChapter)}, 1},
		{"one", domain.ExpansionFormInsertOne, []domain.ExpansionOperation{op(domain.StructureRevisionInsertChapter)}, 1},
		{"many", domain.ExpansionFormInsertMany, []domain.ExpansionOperation{op(domain.StructureRevisionInsertChapter), op(domain.StructureRevisionAppendChapter)}, 2},
		{"arc", domain.ExpansionFormNewArc, []domain.ExpansionOperation{op(domain.StructureRevisionAppendArc)}, 3},
		{"volume", domain.ExpansionFormNewVolume, []domain.ExpansionOperation{op(domain.StructureRevisionAppendVolume)}, 6},
		{"epilogue", domain.ExpansionFormEpilogue, []domain.ExpansionOperation{op(domain.StructureRevisionAppendVolume)}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget, _ := domain.NewDynamicSoftBudget(tc.chapters, 2000, 3000)
			recommendation := domain.ExpansionRecommendation{Form: tc.form, OrderedOperations: tc.operations, ChapterCount: tc.chapters, ChapterMinWords: 2000, ChapterMaxWords: 3000, TotalMinWords: budget.TotalMinWords, TotalMaxWords: budget.TotalMaxWords, SoftBudgetDelta: budget}
			if err := validateExpansionForm(recommendation); err != nil {
				t.Fatal(err)
			}
		})
	}
	budget, _ := domain.NewDynamicSoftBudget(2, 2000, 3000)
	invalid := domain.ExpansionRecommendation{Form: domain.ExpansionFormInsertOne, OrderedOperations: []domain.ExpansionOperation{op(domain.StructureRevisionInsertChapter), op(domain.StructureRevisionAppendChapter)}, ChapterCount: 2, ChapterMinWords: 2000, ChapterMaxWords: 3000, TotalMinWords: budget.TotalMinWords, TotalMaxWords: budget.TotalMaxWords, SoftBudgetDelta: budget}
	if err := validateExpansionForm(invalid); err == nil {
		t.Fatal("insert_one accepted multiple operations")
	}
	invalid.Form = domain.ExpansionFormInsertMany
	invalid.ChapterCount = 3
	if err := validateExpansionForm(invalid); err == nil {
		t.Fatal("insert_multiple accepted a mismatched chapter count")
	}
}

func TestExpansionSixFormsUseRealStableIDProposalDeltas(t *testing.T) {
	_, before := expansionTestStore(t)
	chapter := func(seed string, number int) domain.OutlineEntry {
		item := testChapter(domain.LegacyStructureID("six-form", domain.StructureKindChapter, seed), seed)
		item.Chapter = number
		return item
	}
	tests := []struct {
		name  string
		form  domain.ExpansionForm
		build func() ([]domain.ExpansionOperation, []domain.VolumeOutline, int)
	}{
		{"expand-current", domain.ExpansionFormExpandCurrent, func() ([]domain.ExpansionOperation, []domain.VolumeOutline, int) {
			after := domain.CloneStructureSnapshot(before)
			after[0].Arcs[0].Chapters[0].CoreEvent += " with irreversible consequence"
			return []domain.ExpansionOperation{{Operation: domain.StructureRevisionExpandChapter, Proposal: domain.StructureRevisionProposal{Candidate: after}}}, after, 1
		}},
		{"insert-one", domain.ExpansionFormInsertOne, func() ([]domain.ExpansionOperation, []domain.VolumeOutline, int) {
			after := domain.CloneStructureSnapshot(before)
			after[0].Arcs[0].Chapters = append(after[0].Arcs[0].Chapters, chapter("insert-one", 3))
			return []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Proposal: domain.StructureRevisionProposal{Candidate: after}}}, after, 1
		}},
		{"insert-many", domain.ExpansionFormInsertMany, func() ([]domain.ExpansionOperation, []domain.VolumeOutline, int) {
			middle := domain.CloneStructureSnapshot(before)
			middle[0].Arcs[0].Chapters = append(middle[0].Arcs[0].Chapters, chapter("insert-many-a", 3))
			after := domain.CloneStructureSnapshot(middle)
			after[0].Arcs[0].Chapters = append(after[0].Arcs[0].Chapters, chapter("insert-many-b", 4))
			return []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendChapter, Proposal: domain.StructureRevisionProposal{Candidate: middle}}, {Operation: domain.StructureRevisionAppendChapter, Proposal: domain.StructureRevisionProposal{Candidate: after}}}, after, 2
		}},
		{"new-arc", domain.ExpansionFormNewArc, func() ([]domain.ExpansionOperation, []domain.VolumeOutline, int) {
			after := domain.CloneStructureSnapshot(before)
			after[0].Arcs = append(after[0].Arcs, domain.ArcOutline{ID: domain.LegacyStructureID("six-form", domain.StructureKindArc, "new-arc"), Index: 2, Title: "new arc", Goal: "pay the cost", Chapters: []domain.OutlineEntry{chapter("arc-a", 3), chapter("arc-b", 4)}})
			return []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendArc, Proposal: domain.StructureRevisionProposal{Candidate: after}}}, after, 2
		}},
		{"new-volume", domain.ExpansionFormNewVolume, func() ([]domain.ExpansionOperation, []domain.VolumeOutline, int) {
			after := domain.CloneStructureSnapshot(before)
			after = append(after, domain.VolumeOutline{ID: domain.LegacyStructureID("six-form", domain.StructureKindVolume, "new-volume"), Index: 2, Title: "new volume", Theme: "aftermath", Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID("six-form", domain.StructureKindArc, "volume-arc"), Index: 1, Chapters: []domain.OutlineEntry{chapter("volume-a", 3), chapter("volume-b", 4)}}}})
			return []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendVolume, Proposal: domain.StructureRevisionProposal{Candidate: after}}}, after, 2
		}},
		{"epilogue", domain.ExpansionFormEpilogue, func() ([]domain.ExpansionOperation, []domain.VolumeOutline, int) {
			after := domain.CloneStructureSnapshot(before)
			after = append(after, domain.VolumeOutline{ID: domain.LegacyStructureID("six-form", domain.StructureKindVolume, "epilogue"), Index: 2, Title: "epilogue", Theme: "closure", Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID("six-form", domain.StructureKindArc, "epilogue-arc"), Index: 1, Chapters: []domain.OutlineEntry{chapter("epilogue", 3)}}}})
			return []domain.ExpansionOperation{{Operation: domain.StructureRevisionAppendVolume, Proposal: domain.StructureRevisionProposal{Candidate: after}}}, after, 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations, after, count := test.build()
			budget, _ := domain.NewDynamicSoftBudget(count, 1800, 9000) // deliberately broad: advisory, not a hard generation gate
			recommendation := domain.ExpansionRecommendation{Form: test.form, OrderedOperations: operations, ChapterCount: count, ChapterMinWords: 1800, ChapterMaxWords: 9000, TotalMinWords: budget.TotalMinWords, TotalMaxWords: budget.TotalMaxWords, SoftBudgetDelta: budget, NewArc: test.form == domain.ExpansionFormNewArc, NewVolume: test.form == domain.ExpansionFormNewVolume || test.form == domain.ExpansionFormEpilogue}
			if err := validateExpansionForm(recommendation); err != nil {
				t.Fatal(err)
			}
			if err := validateExpansionActualDelta(recommendation, before, after); err != nil {
				t.Fatal(err)
			}
		})
	}
	deleted := domain.CloneStructureSnapshot(before)
	deleted[0].Arcs[0].Chapters = deleted[0].Arcs[0].Chapters[1:]
	budget, _ := domain.NewDynamicSoftBudget(1, 1800, 3000)
	bad := domain.ExpansionRecommendation{Form: domain.ExpansionFormExpandCurrent, OrderedOperations: []domain.ExpansionOperation{{Operation: domain.StructureRevisionExpandChapter, Proposal: domain.StructureRevisionProposal{Candidate: deleted}}}, ChapterCount: 1, ChapterMinWords: 1800, ChapterMaxWords: 3000, TotalMinWords: budget.TotalMinWords, TotalMaxWords: budget.TotalMaxWords, SoftBudgetDelta: budget}
	if err := validateExpansionActualDelta(bad, before, deleted); err == nil {
		t.Fatal("written stable chapter deletion was accepted")
	}
}

func TestExpansionMapReduceBuildsBoundedDependencyBatches(t *testing.T) {
	var structure []domain.VolumeOutline
	for volume := 1; volume <= 9; volume++ {
		chapterID := domain.LegacyStructureID("map-reduce", domain.StructureKindChapter, fmt.Sprintf("chapter-%d", volume))
		structure = append(structure, domain.VolumeOutline{ID: domain.LegacyStructureID("map-reduce", domain.StructureKindVolume, fmt.Sprintf("volume-%d", volume)), Index: volume, Title: fmt.Sprintf("V%d", volume), Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID("map-reduce", domain.StructureKindArc, fmt.Sprintf("arc-%d", volume)), Index: 1, Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: volume, Title: "C"}}}}})
	}
	selected := structure[0].Arcs[0].Chapters[0].ID
	local, volumes, batches, skeleton := buildExpansionMapReduce(structure, []string{selected}, nil)
	if len(local) != 1 || len(volumes) != 9 || len(batches) < 3 || skeleton == "" {
		t.Fatalf("map/reduce output incomplete: local=%d volumes=%d batches=%d", len(local), len(volumes), len(batches))
	}
	for _, batch := range batches {
		limit := 4
		if batch.HighRisk {
			limit = 2
		}
		if len(batch.VolumeIDs) == 0 || len(batch.VolumeIDs) > limit {
			t.Fatalf("batch exceeds bound: %+v", batch)
		}
		if batch.InputSignature == "" || batch.ReductionSignature == "" || len(batch.ArcBatches) == 0 {
			t.Fatalf("batch lacks executable signed review artifacts: %+v", batch)
		}
		for _, arcBatch := range batch.ArcBatches {
			if len(arcBatch.ChapterIDs) == 0 || len(arcBatch.ChapterIDs) > 3 || arcBatch.InputSignature == "" {
				t.Fatalf("arc dependency batch is not bounded and signed: %+v", arcBatch)
			}
		}
	}
	st, _ := expansionTestStore(t)
	if err := st.Outline.SaveLayeredOutline(structure); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildExpansionContext(st, domain.ExpansionRequest{Location: domain.ExpansionAfter, ReferenceIDs: []string{selected}, Sentence: "review dependencies"})
	if err != nil {
		t.Fatal(err)
	}
	planner := NewExpansionPlanner(st, nil)
	firstReviewCalls := reviewDependencySnapshot(t, planner, st, &snapshot)
	restartedStore := storepkg.NewStore(st.Dir())
	restarted := NewExpansionPlanner(restartedStore, nil)
	unchanged, err := BuildExpansionContext(storepkg.NewStore(st.Dir()), domain.ExpansionRequest{Location: domain.ExpansionAfter, ReferenceIDs: []string{selected}, Sentence: "review dependencies"})
	if err != nil {
		t.Fatal(err)
	}
	if calls := reviewDependencySnapshot(t, restarted, restartedStore, &unchanged); calls != 0 {
		t.Fatalf("restart reran unchanged dependency reviews: before=%d after=%d", firstReviewCalls, calls)
	}
	runtime, err := restartedStore.LoadExpansionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	for key, review := range runtime.DependencyReviews {
		review.ArtifactSignature = "tampered"
		runtime.DependencyReviews[key] = review
		break
	}
	if err := restartedStore.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	tamperedPlanner := NewExpansionPlanner(storepkg.NewStore(st.Dir()), nil)
	tamperedSnapshot, err := BuildExpansionContext(storepkg.NewStore(st.Dir()), domain.ExpansionRequest{Location: domain.ExpansionAfter, ReferenceIDs: []string{selected}, Sentence: "review dependencies"})
	if err != nil {
		t.Fatal(err)
	}
	if calls := reviewDependencySnapshot(t, tamperedPlanner, storepkg.NewStore(st.Dir()), &tamperedSnapshot); calls == 0 {
		t.Fatal("tampered persisted dependency receipt was reused")
	}
	restarted = tamperedPlanner
	changedStructure := domain.CloneStructureSnapshot(structure)
	changedStructure[0].Arcs[0].Chapters[0].CoreEvent = "changed upstream event"
	if err := st.Outline.SaveLayeredOutline(changedStructure); err != nil {
		t.Fatal(err)
	}
	changedSnapshot, err := BuildExpansionContext(st, domain.ExpansionRequest{Location: domain.ExpansionAfter, ReferenceIDs: []string{selected}, Sentence: "review dependencies"})
	if err != nil {
		t.Fatal(err)
	}
	rerunCalls := reviewDependencySnapshot(t, restarted, restartedStore, &changedSnapshot)
	if rerunCalls <= 0 || rerunCalls >= firstReviewCalls {
		t.Fatalf("selective invalidation reran wrong graph scope: initial=%d rerun=%d", firstReviewCalls, rerunCalls)
	}
	var previous []domain.ExpansionDependencyReview
	for _, batch := range snapshot.DependencyBatches {
		if len(batch.ReviewReceipts) < len(batch.ArcBatches)+2 {
			t.Fatalf("signed dependency artifacts missing: %+v", batch)
		}
		for _, review := range batch.ReviewReceipts {
			if err := verifyExpansionDependencyReview(review, planner.auditorPublicKey); err != nil {
				t.Fatalf("dependency reviewer did not produce a verifiable artifact: %v", err)
			}
			previous = append(previous, review)
		}
	}
	current := append([]domain.ExpansionDependencyReview(nil), previous...)
	changedScope := current[0].ScopeID
	current[0].InputSignature = "changed-input"
	affected := ExpansionAffectedDependencyScopes(previous, current)
	if !slices.Contains(affected, changedScope) {
		t.Fatalf("selective stale omitted changed local scope: %v", affected)
	}
	if len(affected) >= len(previous) {
		t.Fatalf("one upstream change invalidated unrelated dependency batches: %v", affected)
	}
}

func TestExpansionDependencyAuditorPersistsSemanticFailureAndRejectsWrongSigner(t *testing.T) {
	st, _ := expansionTestStore(t)
	planner := NewExpansionPlanner(st, nil)
	runner, err := NewExpansionAuditRunner(st)
	if err != nil {
		t.Fatal(err)
	}
	task := ExpansionDependencyAuditTask{ID: "dep-semantic-fail", Stage: "local", ScopeID: "arc-stable", InputSignature: "input-signature", Output: "[needs_fix] unresolved local continuity", DependencyIDs: []string{"chapter-stable"}, PolicyVersion: expansionDependencyPolicyVersion, Status: "pending", CreatedAt: time.Now().UTC()}
	planner.mu.Lock()
	planner.pendingDependencyAudits[task.ID] = task
	if err := planner.persistLocked(); err != nil {
		planner.mu.Unlock()
		t.Fatal(err)
	}
	planner.mu.Unlock()
	review, err := runner.ProcessDependencyTask(context.Background(), task.ID)
	if err != nil || review.Decision != "needs_fix" || len(review.Findings) == 0 {
		t.Fatalf("semantic dependency audit=%+v err=%v", review, err)
	}
	if err := planner.AcceptDependencyReview(task.ID, review); err == nil {
		t.Fatal("needs_fix dependency review did not block planning")
	}
	planner.mu.Lock()
	stored := planner.dependencyReviews[planner.dependencyReviewIndex[task.Stage+"\x00"+task.ScopeID]]
	planner.mu.Unlock()
	if stored.Decision != "needs_fix" || len(stored.Findings) == 0 {
		t.Fatalf("dependency failure was not durable: %+v", stored)
	}

	attackerStore := storepkg.NewStore(t.TempDir())
	if err := attackerStore.Init(); err != nil {
		t.Fatal(err)
	}
	attacker, err := NewExpansionAuditRunner(attackerStore)
	if err != nil {
		t.Fatal(err)
	}
	forgedTask := task
	forgedTask.ID = "dep-wrong-signer"
	forgedTask.Output = "valid repaired dependency continuity summary"
	planner.mu.Lock()
	planner.pendingDependencyAudits[forgedTask.ID] = forgedTask
	if err := planner.persistLocked(); err != nil {
		planner.mu.Unlock()
		t.Fatal(err)
	}
	planner.mu.Unlock()
	forged, err := attacker.ReviewDependency(context.Background(), forgedTask)
	if err != nil {
		t.Fatal(err)
	}
	if err := planner.AcceptDependencyReview(forgedTask.ID, forged); err == nil {
		t.Fatal("wrong-signer dependency receipt was accepted")
	}
}

func TestExpansionDependencyAuditorRecursivelyBlocksFailedAndTamperedChildren(t *testing.T) {
	st, _ := expansionTestStore(t)
	runner, err := NewExpansionAuditRunner(st)
	if err != nil {
		t.Fatal(err)
	}
	childTask := ExpansionDependencyAuditTask{Stage: "local", ScopeID: "arc-stable", InputSignature: "local-input", Output: "causal chapter continuity is preserved", DependencyIDs: []string{"chapter-stable"}, PolicyVersion: expansionDependencyPolicyVersion}
	child, err := runner.ReviewDependency(context.Background(), childTask)
	if err != nil || child.Decision != "pass" {
		t.Fatalf("child=%+v err=%v", child, err)
	}
	parentTask := ExpansionDependencyAuditTask{Stage: "volume", ScopeID: "volume-stable", InputSignature: "volume-input", Output: "volume consequence and handoff remain coherent", DependencyIDs: []string{child.ScopeID}, ChildReviews: []domain.ExpansionDependencyReview{child}, PolicyVersion: expansionDependencyPolicyVersion}
	parent, err := runner.ReviewDependency(context.Background(), parentTask)
	if err != nil || parent.Decision != "pass" {
		t.Fatalf("parent=%+v err=%v", parent, err)
	}

	tampered := child
	tampered.OutputSignature = "tampered-output"
	parentTask.ChildReviews = []domain.ExpansionDependencyReview{tampered}
	parent, err = runner.ReviewDependency(context.Background(), parentTask)
	if err != nil || parent.Decision != "needs_fix" || !slices.ContainsFunc(parent.Findings, func(value string) bool { return strings.Contains(value, "child dependency artifact is invalid") }) {
		t.Fatalf("tampered child was not blocked: %+v err=%v", parent, err)
	}

	failedTask := childTask
	failedTask.Output = "[needs_fix] causal consequence is unresolved"
	failed, err := runner.ReviewDependency(context.Background(), failedTask)
	if err != nil || failed.Decision != "needs_fix" {
		t.Fatalf("failed child=%+v err=%v", failed, err)
	}
	parentTask.ChildReviews = []domain.ExpansionDependencyReview{failed}
	parent, err = runner.ReviewDependency(context.Background(), parentTask)
	if err != nil || parent.Decision != "needs_fix" || !slices.ContainsFunc(parent.Findings, func(value string) bool { return strings.Contains(value, "did not pass") }) {
		t.Fatalf("failed child was not propagated: %+v err=%v", parent, err)
	}
}

func TestExpansionDependencyGraphReloadsOnlyDurableSignedChildArtifacts(t *testing.T) {
	st, _ := expansionTestStore(t)
	runner, err := NewExpansionAuditRunner(st)
	if err != nil {
		t.Fatal(err)
	}
	rootID := "revision-task:1"
	childTask := ExpansionDependencyAuditTask{ID: rootID + ":checkpoint:chapter-stable", RootAuditTaskID: rootID, Stage: "chapter", ScopeID: "chapter-stable", InputSignature: "chapter-input", Output: "chapter causal contract is complete", PolicyVersion: expansionDependencyPolicyVersion, Status: "pending"}
	parentTask := ExpansionDependencyAuditTask{ID: rootID + ":checkpoint:volume-stable", RootAuditTaskID: rootID, Stage: "volume", ScopeID: "volume-stable", InputSignature: "volume-input", Output: "volume consequence is complete", DependencyIDs: []string{childTask.ScopeID}, ChildTaskIDs: []string{childTask.ID}, PolicyVersion: expansionDependencyPolicyVersion, Status: "pending"}
	runtime, err := st.LoadExpansionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	childPayload, _ := json.Marshal(childTask)
	parentPayload, _ := json.Marshal(parentTask)
	runtime.PendingDependencyAudits[childTask.ID] = childPayload
	runtime.PendingDependencyAudits[parentTask.ID] = parentPayload
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	first, err := runner.processDependencyGraph(context.Background(), parentTask.ID, map[string]bool{})
	if err != nil || first.Decision != "pass" {
		t.Fatalf("durable graph review=%+v err=%v", first, err)
	}
	runtime, err = st.LoadExpansionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(runtime.PendingDependencyAudits[parentTask.ID], &parentTask); err != nil {
		t.Fatal(err)
	}
	if len(parentTask.ChildReviews) != 0 || len(parentTask.ChildArtifacts) != 1 {
		t.Fatalf("durable parent embedded child summaries instead of refs: %+v", parentTask)
	}
	restarted, err := NewExpansionAuditRunner(storepkg.NewStore(st.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.ProcessDependencyTask(context.Background(), parentTask.ID)
	if err != nil || replayed.ArtifactSignature != first.ArtifactSignature {
		t.Fatalf("restart did not replay the same signed graph artifact: replay=%+v first=%+v err=%v", replayed, first, err)
	}
	var persistedChild ExpansionDependencyAuditTask
	if err := json.Unmarshal(runtime.PendingDependencyAudits[childTask.ID], &persistedChild); err != nil {
		t.Fatal(err)
	}
	delete(runtime.DependencyReviews, persistedChild.ArtifactID)
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ProcessDependencyTask(context.Background(), parentTask.ID); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing durable child was trusted: %v", err)
	}
	runtime.DependencyReviews[first.ArtifactSignature] = first
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ProcessDependencyTask(context.Background(), parentTask.ID); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("replaced durable child was trusted: %v", err)
	}
}

func TestExpansionDependencyGraphRetainsAndReplaysTwoStableScopeGenerations(t *testing.T) {
	st, _ := expansionTestStore(t)
	runner, err := NewExpansionAuditRunner(st)
	if err != nil {
		t.Fatal(err)
	}
	type graph struct {
		leaf, root ExpansionDependencyAuditTask
		review     domain.ExpansionDependencyReview
	}
	graphs := make([]graph, 0, 2)
	for generation := 1; generation <= 2; generation++ {
		rootID := fmt.Sprintf("revision-stable:%d", generation)
		chapter := ExpansionDependencyAuditTask{ID: rootID + ":checkpoint:chapter-stable", RootAuditTaskID: rootID, Stage: "chapter", ScopeID: "chapter-stable", InputSignature: fmt.Sprintf("chapter-input-%d", generation), Output: fmt.Sprintf("chapter consequence generation %d", generation), PolicyVersion: expansionDependencyPolicyVersion, Status: "pending"}
		batch := ExpansionDependencyAuditTask{ID: rootID + ":checkpoint:batch-stable", RootAuditTaskID: rootID, Stage: "batch", ScopeID: "batch-stable", InputSignature: fmt.Sprintf("batch-input-%d", generation), Output: fmt.Sprintf("batch consequence generation %d", generation), DependencyIDs: []string{chapter.ScopeID}, ChildTaskIDs: []string{chapter.ID}, PolicyVersion: expansionDependencyPolicyVersion, Status: "pending"}
		volume := ExpansionDependencyAuditTask{ID: rootID + ":checkpoint:volume-stable", RootAuditTaskID: rootID, Stage: "volume", ScopeID: "volume-stable", InputSignature: fmt.Sprintf("volume-input-%d", generation), Output: fmt.Sprintf("volume consequence generation %d", generation), DependencyIDs: []string{batch.ScopeID}, ChildTaskIDs: []string{batch.ID}, PolicyVersion: expansionDependencyPolicyVersion, Status: "pending"}
		wholeBook := ExpansionDependencyAuditTask{ID: rootID + ":checkpoint:whole-book", RootAuditTaskID: rootID, Stage: "skeleton", ScopeID: "whole-book", InputSignature: fmt.Sprintf("book-input-%d", generation), Output: fmt.Sprintf("book consequence generation %d", generation), DependencyIDs: []string{volume.ScopeID}, ChildTaskIDs: []string{volume.ID}, PolicyVersion: expansionDependencyPolicyVersion, Status: "pending"}
		runtime, loadErr := st.LoadExpansionRuntime()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		for _, task := range []ExpansionDependencyAuditTask{chapter, batch, volume, wholeBook} {
			runtime.PendingDependencyAudits[task.ID], _ = json.Marshal(task)
		}
		if saveErr := st.SaveExpansionRuntime(runtime); saveErr != nil {
			t.Fatal(saveErr)
		}
		review, processErr := runner.processDependencyGraph(context.Background(), wholeBook.ID, map[string]bool{})
		if processErr != nil || review.Decision != "pass" {
			t.Fatalf("generation %d process review=%+v err=%v", generation, review, processErr)
		}
		graphs = append(graphs, graph{leaf: chapter, root: wholeBook, review: review})
	}
	runtime, err := st.LoadExpansionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.DependencyReviews) < 8 {
		t.Fatalf("immutable repository overwrote stable-scope history: %d artifacts", len(runtime.DependencyReviews))
	}
	var firstChild ExpansionDependencyAuditTask
	if err := json.Unmarshal(runtime.PendingDependencyAudits[graphs[0].leaf.ID], &firstChild); err != nil {
		t.Fatal(err)
	}
	// Deliberately point the mutable cache index at generation one. Immutable
	// replay of either generation must remain unaffected by this index choice.
	runtime.DependencyReviewIndex["chapter\x00chapter-stable"] = firstChild.ArtifactID
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewExpansionAuditRunner(storepkg.NewStore(st.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	for index, graph := range graphs {
		replayed, replayErr := restarted.ProcessDependencyTask(context.Background(), graph.root.ID)
		if replayErr != nil || replayed.ArtifactSignature != graph.review.ArtifactSignature {
			t.Fatalf("generation %d immutable replay=%+v expected=%+v err=%v", index+1, replayed, graph.review, replayErr)
		}
	}
	// Exercise the top-level production contract: it retains exact root task
	// identities and only caches the matching immutable root artifact.
	runtime, err = st.LoadExpansionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	for _, graph := range graphs {
		top := ExpansionAuditTask{ID: graph.root.RootAuditTaskID, RevisionID: graph.root.RootAuditTaskID, Revision: 1, Stage: domain.RevisionStageCandidateAudit, Scope: "revision_candidate", ScopeID: graph.root.RootAuditTaskID, PolicyVersion: expansionAuditorPolicyVersion, Status: "pending", CheckpointTaskIDs: []string{graph.root.ID}}
		runtime.PendingAudits[top.ID], _ = json.Marshal(top)
	}
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	for index, graph := range graphs {
		if _, err := restarted.ProcessRevisionTask(context.Background(), graph.root.RootAuditTaskID); err != nil {
			t.Fatalf("generation %d top-level first process failed: %v", index+1, err)
		}
	}
	restarted, err = NewExpansionAuditRunner(storepkg.NewStore(st.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	for index, graph := range graphs {
		if _, err := restarted.ProcessRevisionTask(context.Background(), graph.root.RootAuditTaskID); err != nil {
			t.Fatalf("generation %d top-level restart replay failed: %v", index+1, err)
		}
	}
	runtime, err = st.LoadExpansionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	for _, graph := range graphs {
		var top ExpansionAuditTask
		if err := json.Unmarshal(runtime.PendingAudits[graph.root.RootAuditTaskID], &top); err != nil {
			t.Fatal(err)
		}
		if len(top.CheckpointTaskIDs) != 1 || top.CheckpointTaskIDs[0] != graph.root.ID || len(top.CheckpointArtifacts) != 1 || top.CheckpointArtifacts[0].ArtifactID != graph.review.ArtifactSignature {
			t.Fatalf("top-level task flattened or replaced root graph: %+v", top)
		}
	}
	firstArtifact := runtime.DependencyReviews[firstChild.ArtifactID]
	var secondChild ExpansionDependencyAuditTask
	if err := json.Unmarshal(runtime.PendingDependencyAudits[graphs[1].leaf.ID], &secondChild); err != nil {
		t.Fatal(err)
	}
	secondArtifact := runtime.DependencyReviews[secondChild.ArtifactID]
	delete(runtime.DependencyReviews, firstChild.ArtifactID)
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ProcessDependencyTask(context.Background(), graphs[0].root.ID); err == nil {
		t.Fatal("graph with deleted generation-one child artifact did not fail closed")
	}
	if _, err := restarted.ProcessRevisionTask(context.Background(), graphs[0].root.RootAuditTaskID); err == nil {
		t.Fatal("top-level revision trusted deleted generation-one child artifact")
	}
	if _, err := restarted.ProcessRevisionTask(context.Background(), graphs[1].root.RootAuditTaskID); err != nil {
		t.Fatalf("deleted generation-one artifact poisoned generation-two top-level replay: %v", err)
	}
	if replayed, err := restarted.ProcessDependencyTask(context.Background(), graphs[1].root.ID); err != nil || replayed.ArtifactSignature != graphs[1].review.ArtifactSignature {
		t.Fatalf("deleting generation one poisoned independent generation two: replay=%+v err=%v", replayed, err)
	}
	tampered := firstArtifact
	tampered.OutputSignature = "tampered"
	runtime.DependencyReviews[firstChild.ArtifactID] = tampered
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ProcessDependencyTask(context.Background(), graphs[0].root.ID); err == nil {
		t.Fatal("graph with tampered generation-one child artifact did not fail closed")
	}
	if _, err := restarted.ProcessRevisionTask(context.Background(), graphs[0].root.RootAuditTaskID); err == nil {
		t.Fatal("top-level revision trusted tampered generation-one child artifact")
	}
	if _, err := restarted.ProcessRevisionTask(context.Background(), graphs[1].root.RootAuditTaskID); err != nil {
		t.Fatalf("tampered generation-one artifact poisoned generation-two top-level replay: %v", err)
	}
	if replayed, err := restarted.ProcessDependencyTask(context.Background(), graphs[1].root.ID); err != nil || replayed.ArtifactSignature != graphs[1].review.ArtifactSignature {
		t.Fatalf("tampering generation one poisoned independent generation two: replay=%+v err=%v", replayed, err)
	}
	runtime.DependencyReviews[firstChild.ArtifactID] = secondArtifact
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ProcessDependencyTask(context.Background(), graphs[0].root.ID); err == nil {
		t.Fatal("cross-generation child artifact replacement did not fail closed")
	}
	if _, err := restarted.ProcessRevisionTask(context.Background(), graphs[0].root.RootAuditTaskID); err == nil {
		t.Fatal("top-level revision trusted cross-generation child artifact")
	}
	if _, err := restarted.ProcessRevisionTask(context.Background(), graphs[1].root.RootAuditTaskID); err != nil {
		t.Fatalf("cross-generation replacement poisoned generation-two top-level replay: %v", err)
	}
	if replayed, err := restarted.ProcessDependencyTask(context.Background(), graphs[1].root.ID); err != nil || replayed.ArtifactSignature != graphs[1].review.ArtifactSignature {
		t.Fatalf("cross-generation replacement poisoned independent generation two: replay=%+v err=%v", replayed, err)
	}
	runtime.DependencyReviews[firstChild.ArtifactID] = firstArtifact
	rootPayload := append(json.RawMessage(nil), runtime.PendingDependencyAudits[graphs[0].root.ID]...)
	var completedRoot ExpansionDependencyAuditTask
	if err := json.Unmarshal(rootPayload, &completedRoot); err != nil {
		t.Fatal(err)
	}
	rootArtifact := runtime.DependencyReviews[completedRoot.ArtifactID]
	parentID := completedRoot.ChildTaskIDs[0]
	parentPayload := append(json.RawMessage(nil), runtime.PendingDependencyAudits[parentID]...)
	assertTopLevelIsolation := func(label string) {
		t.Helper()
		if _, err := restarted.ProcessRevisionTask(context.Background(), graphs[0].root.RootAuditTaskID); err == nil {
			t.Fatalf("top-level revision trusted %s", label)
		}
		if _, err := restarted.ProcessRevisionTask(context.Background(), graphs[1].root.RootAuditTaskID); err != nil {
			t.Fatalf("%s poisoned generation-two top-level replay: %v", label, err)
		}
	}
	delete(runtime.PendingDependencyAudits, graphs[0].root.ID)
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	assertTopLevelIsolation("missing root task")
	runtime.PendingDependencyAudits[graphs[0].root.ID] = rootPayload
	delete(runtime.PendingDependencyAudits, parentID)
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	assertTopLevelIsolation("missing parent task")
	runtime.PendingDependencyAudits[parentID] = parentPayload
	tamperedRoot := completedRoot
	tamperedRoot.ContractSignature = "tampered"
	runtime.PendingDependencyAudits[graphs[0].root.ID], _ = json.Marshal(tamperedRoot)
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	assertTopLevelIsolation("tampered root contract")
	runtime.PendingDependencyAudits[graphs[0].root.ID] = rootPayload
	tamperedRootArtifact := rootArtifact
	tamperedRootArtifact.OutputSignature = "tampered"
	runtime.DependencyReviews[completedRoot.ArtifactID] = tamperedRootArtifact
	if err := st.SaveExpansionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	assertTopLevelIsolation("tampered root artifact")
}

func TestExpansionPlannerUsesPublicTrustWithoutReadingRunnerPrivateIdentity(t *testing.T) {
	st, _ := expansionTestStore(t)
	planner := NewExpansionPlanner(st, nil)
	if planner.initErr != nil {
		t.Fatalf("planner attempted to load runner private identity: %v", planner.initErr)
	}
}

func TestAdaptationExpansionProjectionBindsStableTargetAndSourceContract(t *testing.T) {
	chapterID := domain.LegacyStructureID("adaptation-expansion", domain.StructureKindChapter, "chapter-1")
	volumeID := domain.LegacyStructureID("adaptation-expansion", domain.StructureKindVolume, "volume-1")
	candidate := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "V", Theme: "T",
		Arcs: []domain.ArcOutline{{
			ID: domain.LegacyStructureID("adaptation-expansion", domain.StructureKindArc, "arc-1"), Index: 1,
			Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "C", CoreEvent: "E", Hook: "H", Scenes: []string{"S"}}},
		}},
	}}
	plan := domain.AdaptationPlan{Volumes: []domain.AdaptationVolumePlan{{ID: volumeID, Index: 1, Title: "V", Theme: "T"}}, Chapters: []domain.AdaptationChapterPlan{{OutlineEntry: domain.OutlineEntry{ID: chapterID, Chapter: 1, Title: "C", CoreEvent: "E", Hook: "H", Scenes: []string{"S"}}, Chapter: 1, Title: "C", SourceRange: domain.SourceRange{From: 7, To: 8}, IsAdded: true}}}
	if err := validateAdaptationExpansionProjection(candidate, plan); err != nil {
		t.Fatal(err)
	}
	plan.Chapters[0].OutlineEntry.ID = domain.LegacyStructureID("adaptation-expansion", domain.StructureKindChapter, "other")
	if err := validateAdaptationExpansionProjection(candidate, plan); err == nil {
		t.Fatal("projection accepted a divergent contract candidate")
	}
	plan.Chapters[0].OutlineEntry.ID = chapterID
	plan.Chapters[0].CoreEvent = "diverged"
	if err := validateAdaptationExpansionProjection(candidate, plan); err == nil {
		t.Fatal("projection accepted divergent adaptation content")
	}
}

func TestAdaptationExpansionProductionFullFlowUsesOneAuditedContract(t *testing.T) {
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityChapter, false)
	if _, err := NewExpansionAuditRunner(st); err != nil {
		t.Fatal(err)
	}
	baseStructure := adaptationExpansionStructure(base)
	candidateStructure := adaptationExpansionStructure(candidate)
	facts := domain.ExpansionDramaticFactSet{SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active", ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "passive", CharacterAfter: "active", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required"}
	for volumeIndex := range candidateStructure {
		for arcIndex := range candidateStructure[volumeIndex].Arcs {
			for chapterIndex := range candidateStructure[volumeIndex].Arcs[arcIndex].Chapters {
				copy := facts
				candidateStructure[volumeIndex].Arcs[arcIndex].Chapters[chapterIndex].DramaticFacts = &copy
			}
		}
	}
	for chapterIndex := range candidate.Chapters {
		copy := facts
		candidate.Chapters[chapterIndex].DramaticFacts = &copy
	}
	if err := st.Outline.SaveLayeredOutline(baseStructure); err != nil {
		t.Fatal(err)
	}
	delta, _ := domain.NewDynamicSoftBudget(1, 2500, 4500)
	proposalBudget, _ := domain.NewDynamicSoftBudget(len(candidate.Chapters), 2500, 4500)
	causalImpacts := make([]domain.StructureImpactItem, 0, len(base.Chapters))
	for _, chapter := range base.Chapters {
		causalImpacts = append(causalImpacts, domain.StructureImpactItem{ArtifactID: chapter.ID, ArtifactKind: domain.StructureKindChapter, Change: "verify existing contract against added bridge", Level: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"added bridge changes adjacent contract review"}, DependencySourceIDs: []string{adaptationTestAddedID}})
	}
	recommendation := expansionRecommendation(t, domain.ExpansionFormInsertOne, []domain.ExpansionOperation{{
		Operation: domain.StructureRevisionAppendChapter, Intent: "add an original bridge",
		Proposal: domain.StructureRevisionProposal{Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "coverage-preserving original bridge"}, Candidate: candidateStructure, Impacts: causalImpacts, SoftBudget: proposalBudget},
	}})
	recommendation.Assessment.AdaptationEffect = "source coverage, ownership, and protected event contracts remain complete"
	recommendation.AdaptationCandidate = &candidate
	recommendation.ChapterCount, recommendation.ChapterMinWords, recommendation.ChapterMaxWords = 1, 2500, 4500
	recommendation.TotalMinWords, recommendation.TotalMaxWords, recommendation.SoftBudgetDelta = delta.TotalMinWords, delta.TotalMaxWords, delta
	recommender := &scriptedExpansionRecommender{recommendation: recommendation}
	planner := NewExpansionPlanner(st, recommender)
	request := domain.ExpansionRequest{Location: domain.ExpansionAfter, ReferenceIDs: []string{base.Chapters[len(base.Chapters)-1].ID}, Sentence: "add a coverage-safe original bridge", Adjustment: domain.ExpansionAdjustmentDefault, ExpectedStructureRevision: domain.StructureRevision(baseStructure), ExpectedStructureSignature: domain.StructureSignature(baseStructure), IdempotencyKey: "adaptation-plan"}
	preview, err := planExpansion(t, planner, request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Mode != domain.RevisionModeAdaptation || preview.Recommendation.AdaptationCandidate == nil || len(preview.DependencyReviews) == 0 {
		t.Fatalf("adaptation preview lost contract or dependency audit chain: %+v", preview)
	}
	planner = NewExpansionPlanner(storepkg.NewStore(st.Dir()), recommender)
	confirmation, err := planner.Confirm(context.Background(), preview.ID, preview.BaseRevision, "adaptation-confirm")
	if err != nil {
		t.Fatal(err)
	}
	session := confirmation.Revision
	stageIDs := make([]string, 0, len(session.ApprovalStages))
	for _, stage := range session.ApprovalStages {
		stageIDs = append(stageIDs, stage.ID)
	}
	for _, required := range []string{domain.AdaptationApprovalStructure, domain.AdaptationApprovalOutline, domain.AdaptationApprovalProse} {
		if !slices.Contains(stageIDs, required) {
			t.Fatalf("adaptation expansion omitted required fixed-flow stage %s: %v", required, stageIDs)
		}
	}
	if _, err := planner.RevisionCommand("audit", "client pass", session.Revision, "adaptation-forged-audit"); err == nil {
		t.Fatal("adaptation client forged an audit pass")
	}
	for session.Stage != domain.RevisionStageReadyToPublish {
		beforeStage := session.Stage
		switch session.Stage {
		case domain.RevisionStageCandidateAudit:
			session, err = runExpansionAuditCommand(t, planner, session, fmt.Sprintf("adaptation-audit-%d", session.Revision))
		case domain.RevisionStageApprovalPending:
			session, err = planner.RevisionCommand("approve", "", session.Revision, fmt.Sprintf("adaptation-human-%d", session.Revision))
		case domain.RevisionStageCandidateGenerating:
			stageID := session.ApprovalStages[len(session.Approvals)].ID
			action := "outline"
			if stageID == domain.AdaptationApprovalStructure {
				action = "structure"
			} else if strings.Contains(stageID, "prose") {
				action = "prose"
			}
			session, err = planner.RevisionCommand(action, "", session.Revision, fmt.Sprintf("adaptation-%s-%d", action, session.Revision))
		default:
			t.Fatalf("adaptation full flow stopped at %s", session.Stage)
		}
		if err != nil {
			t.Fatalf("adaptation full flow stage=%s: %v", beforeStage, err)
		}
	}
	published, err := planner.RevisionCommand("publish", "", session.Revision, "adaptation-publish")
	if err != nil || published.Stage != domain.RevisionStageCompleted {
		t.Fatalf("adaptation publish=%+v err=%v", published, err)
	}
	formal, err := st.Adaptation.LoadPlan()
	if err != nil || formal == nil || len(formal.Chapters) != len(candidate.Chapters) || formal.Chapters[len(formal.Chapters)-1].ID != candidate.Chapters[len(candidate.Chapters)-1].ID {
		t.Fatalf("published adaptation contract is not the audited candidate: plan=%+v err=%v", formal, err)
	}
	assertExpansionPublicationAuthorityFiles(t, st.Dir())
}

func assertExpansionPublicationAuthorityFiles(t *testing.T, outputDir string) {
	t.Helper()
	for _, rel := range []string{
		"meta/revisions/expansion-publication-trust.json",
		"meta/revisions/expansion-publication-receipt.json",
	} {
		info, err := os.Stat(filepath.Join(outputDir, filepath.FromSlash(rel)))
		if err != nil || info.Size() == 0 {
			t.Fatalf("real publish path did not generate %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, "meta", "runtime", "expansion-publication-authority.json")); !os.IsNotExist(err) {
		t.Fatalf("real publish path retained project-local private authority: %v", err)
	}
}

func adaptationExpansionStructure(plan domain.AdaptationPlan) []domain.VolumeOutline {
	volumes := make([]domain.VolumeOutline, 0, len(plan.Volumes))
	chaptersByVolume := make(map[string][]domain.OutlineEntry)
	for _, chapter := range plan.Chapters {
		volumeID := plan.Volumes[0].ID
		for _, volume := range plan.Volumes {
			if chapter.Chapter >= volume.TargetFrom && chapter.Chapter <= volume.TargetTo {
				volumeID = volume.ID
				break
			}
		}
		chaptersByVolume[volumeID] = append(chaptersByVolume[volumeID], chapter.OutlineEntry)
	}
	for _, volume := range plan.Volumes {
		volumes = append(volumes, domain.VolumeOutline{ID: volume.ID, Index: volume.Index, Title: volume.Title, Theme: volume.Theme, Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID("adaptation-expansion-full-flow", domain.StructureKindArc, volume.ID), Index: 1, Title: volume.Title, Goal: "preserve coverage", Chapters: chaptersByVolume[volume.ID]}}})
	}
	return domain.ProjectLayeredOutlineOrder(volumes)
}

func expansionTestStore(t *testing.T) (*storepkg.Store, []domain.VolumeOutline) {
	t.Helper()
	st := newPublicationTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExpansionAuditRunner(st); err != nil {
		t.Fatal(err)
	}
	current := testStructure()
	current[0].ID = domain.LegacyStructureID(st.Dir(), domain.StructureKindVolume, "volume-1")
	current[0].Arcs[0].ID = domain.LegacyStructureID(st.Dir(), domain.StructureKindArc, "volume-1/arc-1")
	for index := range current[0].Arcs[0].Chapters {
		current[0].Arcs[0].Chapters[index].ID = domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, fmt.Sprintf("chapter-%d", index+1))
	}
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Layered: true, TotalChapters: 3, CurrentChapter: 1}); err != nil {
		t.Fatal(err)
	}
	return st, current
}

func reviewDependencySnapshot(t *testing.T, planner *ExpansionPlanner, st *storepkg.Store, snapshot *ExpansionContext) int {
	t.Helper()
	runner, err := NewExpansionAuditRunner(st)
	if err != nil {
		t.Fatal(err)
	}
	processed := 0
	for attempts := 0; attempts < 512; attempts++ {
		err := planner.reviewExpansionContextDependencies(context.Background(), snapshot)
		var pending *ExpansionDependencyAuditPendingError
		if err == nil {
			return processed
		}
		if !errors.As(err, &pending) {
			t.Fatal(err)
		}
		review, err := runner.ProcessDependencyTask(context.Background(), pending.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if err := planner.AcceptDependencyReview(pending.TaskID, review); err != nil {
			t.Fatal(err)
		}
		processed++
	}
	t.Fatal("dependency audit graph did not converge")
	return 0
}

func expansionRequest(current []domain.VolumeOutline, key string) domain.ExpansionRequest {
	return domain.ExpansionRequest{Location: domain.ExpansionBefore, ReferenceIDs: []string{current[0].Arcs[0].Chapters[1].ID}, Sentence: "make the misunderstanding require an irreversible choice", Adjustment: domain.ExpansionAdjustmentDefault, ExpectedStructureRevision: domain.StructureRevision(current), ExpectedStructureSignature: domain.StructureSignature(current), IdempotencyKey: key}
}

func expansionRecommendation(t *testing.T, form domain.ExpansionForm, operations []domain.ExpansionOperation) domain.ExpansionRecommendation {
	t.Helper()
	budget, err := domain.NewDynamicSoftBudget(len(operations), 2200, 3600)
	if err != nil {
		t.Fatal(err)
	}
	facts := domain.ExpansionDramaticFactSet{SchemaVersion: domain.ExpansionDramaticFactsSchemaV1, GoalState: "pursued", ConflictState: "active", ChoiceState: "committed", CostState: "paid", ResultState: "achieved", CharacterBefore: "passive", CharacterAfter: "active", ClimaxState: "occurred", ExitState: "irreversible", ImpactState: "required"}
	return domain.ExpansionRecommendation{Form: form, Reason: "goal-conflict-choice-cost-result is independent", Location: domain.ExpansionBefore, ChapterCount: len(operations), ChapterMinWords: 2200, ChapterMaxWords: 3600, TotalMinWords: 2200 * len(operations), TotalMaxWords: 3600 * len(operations), OldSummary: "misunderstanding", NewSummary: "choice pays a cost", Assessment: domain.ExpansionDramaticAssessment{Goal: "learn truth", Conflict: "trust breaks", Choice: "reveal evidence", Cost: "lose ally", Result: "new alliance", CharacterStageChange: "passive to active", CharacterBeforeStage: "passive", CharacterAfterStage: "active", IndependentClimax: "evidence is revealed", IrreversibleExit: "the former ally leaves", CurrentFit: "needs an independent unit", VolumePacingEffect: "adds a midpoint turn", TypedClaims: &facts}, AuditChain: []string{"impact", "structure", "outline", "prose"}, ModeConstraints: []string{"normal source firewall"}, OrderedOperations: operations, SoftBudgetDelta: budget}
}
