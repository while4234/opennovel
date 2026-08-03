package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestFoundationRevisionStorePersistsAndDetectsTamperedPreview(t *testing.T) {
	st := NewStore(t.TempDir())
	base := domain.StoryFoundation{SchemaVersion: 1, Revision: 1, Premise: "base", Characters: []domain.Character{{ID: "char-1", Name: "Hero"}}, WorldRules: []domain.WorldRule{{ID: "rule-1", Category: "other", Rule: "rule", Strength: domain.WorldRuleStrengthSoft}}}
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "candidate"
	diff, err := domain.ComputeFoundationDiff(base, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := domain.AnalyzeFoundationImpact(diff, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidateSignature, _ := domain.FoundationContentSignature(candidate)
	now := time.Now().UTC()
	preview := domain.FoundationRevisionPreview{Version: 1, ID: "preview-1", ProjectMode: "normal", BaseRevision: 1, BaseAuditSignature: domain.ContentSignature([]byte("audit")), BaseCoreCastSignature: domain.ContentSignature([]byte("cast")), BasePlanningSignature: domain.ContentSignature([]byte("planning")), Generation: 1, Base: base, Candidate: candidate, CandidateSignature: candidateSignature, Diff: diff, Impact: impact, Validation: domain.FoundationPreviewValidation{Valid: true}, CanApply: true, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano)}
	preview = domain.SignFoundationRevisionPreview(preview)
	if err := st.FoundationRevisions.SavePreview(preview); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.FoundationRevisions.LoadPreview(preview.ID)
	if err != nil || loaded.Signature != preview.Signature {
		t.Fatalf("load preview: %+v, %v", loaded, err)
	}

	path := st.FoundationRevisions.io.path(foundationPreviewPath(preview.ID))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for index := range data {
		if data[index] == 'c' {
			data[index] = 'x'
			break
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FoundationRevisions.LoadPreview(preview.ID); err == nil {
		t.Fatal("tampered preview was accepted")
	}
}

func TestFoundationPlanningOwnerRequiresExactActiveFenceAndImpact(t *testing.T) {
	st := NewStore(t.TempDir())
	impact, err := domain.FoundationRevisionImpact(signedFoundationImpactForStoreTest(t))
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.FoundationRevisionPolicy{}
	session, err := st.Revisions.Start(policy, StartRevisionInput{Intent: "revise Foundation", Impact: impact, PreviewSignature: domain.ContentSignature([]byte("preview")), IdempotencyKey: "start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(policy, StartRevisionInput{Intent: "second", Impact: impact, IdempotencyKey: "second"}); err == nil {
		t.Fatal("second active Foundation revision was accepted")
	}
	session, err = st.Revisions.ApproveImpact(policy, RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "impact"})
	if err != nil {
		t.Fatal(err)
	}
	foundation := domain.StoryFoundation{SchemaVersion: 1, Premise: "changed", Characters: []domain.Character{{ID: "char-1", Name: "Hero"}}, WorldRules: []domain.WorldRule{{ID: "rule-1", Category: "other", Rule: "rule", Strength: domain.WorldRuleStrengthSoft}}}
	payload, _ := json.Marshal(foundation)
	session, err = st.Revisions.SubmitCandidate(policy, SubmitRevisionCandidateInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "candidate", Artifacts: []CandidateArtifactInput{{ArtifactID: domain.FoundationArtifactID, ArtifactKind: domain.FoundationArtifactKind, Payload: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	expected := session.AuditExpectations[0]
	session, err = st.Revisions.RecordAudit(policy, RevisionAuditInput{RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "audit"}, CandidateSignature: session.CandidateSignature, Evidence: []domain.RevisionAuditEvidence{{Scope: expected.Scope, ScopeID: expected.ScopeID, ContentSignature: expected.ContentSignature, Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.ApproveStage(policy, RevisionApprovalInput{RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "approve"}, StageID: "foundation_apply"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.AuthorizeFoundationPlanning(context.Background(), "book"); err == nil {
		t.Fatal("unfenced Foundation planning owner was issued")
	}
	wrong := ContextWithRevisionFence(context.Background(), RevisionFence{Generation: session.Generation, SessionID: session.ID, Revision: session.Revision - 1})
	if _, err := st.Revisions.AuthorizeFoundationPlanning(wrong, "book"); err == nil {
		t.Fatal("stale Foundation planning fence was accepted")
	}
	ctx := ContextWithRevisionFence(context.Background(), RevisionFence{Generation: session.Generation, SessionID: session.ID, Revision: session.Revision})
	owner, err := st.Revisions.AuthorizeFoundationPlanning(ctx, "book")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.AuthorizeFoundationPlanning(ctx, "outside-approved-impact"); err == nil {
		t.Fatal("out-of-impact Foundation planning owner was issued")
	}
	if err := st.Revisions.withFoundationPlanningMutation(owner, "test", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestFoundationRevisionReceiptRejectsKeyReuse(t *testing.T) {
	st := NewStore(t.TempDir())
	runtime := domain.FoundationRevisionRuntime{Version: 1, RevisionID: "rev-1", SessionID: "rev-1", PreviewID: "preview-1", ProjectMode: "normal", Stage: "failed", Attempt: 1, Generation: 1, Impact: signedFoundationImpactForStoreTest(t), CreatedAt: domain.RevisionTimestamp(), UpdatedAt: domain.RevisionTimestamp()}
	if err := st.FoundationRevisions.SaveReceipt("key", "fingerprint-a", runtime); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.FoundationRevisions.LoadReceipt("key", "fingerprint-b"); err == nil {
		t.Fatal("idempotency key reuse was accepted")
	}
}

func TestFoundationAdaptationOwnerAllowsTargetPlanningButRejectsSourceWrites(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	impact, err := domain.FoundationRevisionImpact(signedFoundationImpactForStoreTest(t))
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.FoundationRevisionPolicy{}
	session, err := st.Revisions.Start(policy, StartRevisionInput{Intent: "adapt target Foundation", Impact: impact, PreviewSignature: domain.ContentSignature([]byte("preview")), IdempotencyKey: "owner-start"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.ApproveImpact(policy, RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "owner-impact"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(domain.StoryFoundation{SchemaVersion: 1, Premise: "target", Characters: []domain.Character{{ID: "hero", Name: "Hero"}}, WorldRules: []domain.WorldRule{{ID: "rule", Rule: "rule", Strength: domain.WorldRuleStrengthSoft}}})
	session, err = st.Revisions.SubmitCandidate(policy, SubmitRevisionCandidateInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "owner-candidate", Artifacts: []CandidateArtifactInput{{ArtifactID: domain.FoundationArtifactID, ArtifactKind: domain.FoundationArtifactKind, Payload: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	expected := session.AuditExpectations[0]
	session, err = st.Revisions.RecordAudit(policy, RevisionAuditInput{RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "owner-audit"}, CandidateSignature: session.CandidateSignature, Evidence: []domain.RevisionAuditEvidence{{Scope: expected.Scope, ScopeID: expected.ScopeID, ContentSignature: expected.ContentSignature, Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	session, err = st.Revisions.ApproveStage(policy, RevisionApprovalInput{RevisionMutationInput: RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "owner-approve"}, StageID: "foundation_apply"})
	if err != nil {
		t.Fatal(err)
	}
	err = st.WithFoundationAdaptationRevisionCommand(session.ID, "test-target-owner", func() error {
		if _, workflowErr := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, -1); workflowErr != nil {
			return workflowErr
		}
		if sourceErr := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{Premise: "forbidden"}); sourceErr == nil {
			return errors.New("SourceFoundation write was accepted")
		}
		if manifestErr := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{}); manifestErr == nil {
			return errors.New("source manifest write was accepted")
		}
		if _, chapterErr := st.Adaptation.SaveSourceChapter(1, "forbidden", "forbidden"); chapterErr == nil {
			return errors.New("source chapter write was accepted")
		}
		if reportsErr := st.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{{Chapter: 1, Summary: "forbidden"}}); reportsErr == nil {
			return errors.New("source report write was accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if source, err := st.Adaptation.LoadSourceFoundation(); err != nil || source != nil {
		t.Fatalf("source=%+v err=%v", source, err)
	}
}

func TestFoundationAdaptationCommandFenceRecoversWithoutParallelSession(t *testing.T) {
	st := NewStore(t.TempDir())
	owner, err := st.Revisions.claimCommandFence("revision", "foundation-adaptation/regenerate", domain.ContentSignature([]byte("revision")))
	if err != nil || owner == nil {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	reopened := NewStore(st.Dir())
	if current, err := reopened.Revisions.currentCommandOwner(); err != nil || current != nil {
		t.Fatalf("recovered owner=%+v err=%v", current, err)
	}
}

func signedFoundationImpactForStoreTest(t *testing.T) domain.FoundationImpact {
	t.Helper()
	base := domain.StoryFoundation{SchemaVersion: 1, Premise: "base", Characters: []domain.Character{{ID: "char-1", Name: "Hero"}}, WorldRules: []domain.WorldRule{{ID: "rule-1", Category: "other", Rule: "rule", Strength: domain.WorldRuleStrengthSoft}}}
	candidate := domain.CloneStoryFoundation(base)
	candidate.Premise = "changed"
	diff, err := domain.ComputeFoundationDiff(base, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := domain.AnalyzeFoundationImpact(diff, nil)
	if err != nil {
		t.Fatal(err)
	}
	return impact
}
