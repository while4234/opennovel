package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestFoundationReviewBindsCurrentRevisionAuditAndCoreCast(t *testing.T) {
	st := foundationReviewTestStore(t)
	pending := completePendingFoundationReviewForTest(t, st)
	if pending.FoundationRevision <= pending.FoundationBaseRevision ||
		pending.FoundationAuditSignature == "" || pending.CoreCastSignature == "" {
		t.Fatalf("pending review is not fully bound: %+v", pending)
	}
	pendingFoundation, err := st.Foundation.Load()
	if err != nil || pendingFoundation.RelationshipsReviewed {
		t.Fatalf("model generation marked planned relationships reviewed: foundation=%+v err=%v", pendingFoundation, err)
	}

	if _, err := st.ConfirmFoundation(pending.FoundationRevision-1, pending.FoundationAuditSignature); foundationReviewCode(err) != FoundationReviewErrorStale {
		t.Fatalf("stale revision confirmation error = %v", err)
	}
	current, err := st.RunMeta.PlanningReview()
	if err != nil || current == nil {
		t.Fatalf("load refreshed review: review=%+v err=%v", current, err)
	}
	approved, err := st.ConfirmFoundation(current.FoundationRevision, current.FoundationAuditSignature)
	if err != nil {
		t.Fatal(err)
	}
	if approved.FoundationStatus != domain.FoundationReviewStatusApproved || approved.FoundationConfirmedAt == "" {
		t.Fatalf("approved review = %+v", approved)
	}
	approvedFoundation, err := st.Foundation.Load()
	if err != nil || !approvedFoundation.RelationshipsReviewed {
		t.Fatalf("explicit confirmation did not mark relationships reviewed: foundation=%+v err=%v", approvedFoundation, err)
	}
	if err := st.RequireConfirmedFoundation(); err != nil {
		t.Fatalf("confirmed gate: %v", err)
	}

	if err := st.Foundation.updatePremise("A changed premise"); err != nil {
		t.Fatal(err)
	}
	if err := st.RequireConfirmedFoundation(); foundationReviewCode(err) != FoundationReviewErrorStale {
		t.Fatalf("changed foundation gate error = %v", err)
	}
	stale, err := st.RunMeta.PlanningReview()
	if err != nil || stale == nil || stale.Kind != domain.PlanningReviewKindFoundation ||
		stale.FoundationStatus != domain.FoundationReviewStatusPending || stale.FoundationConfirmedAt != "" {
		t.Fatalf("stale binding did not restore checkpoint: review=%+v err=%v", stale, err)
	}
}

func TestFoundationGenerationFenceRejectsOldDuplicateAndRestartedResponses(t *testing.T) {
	st := foundationReviewTestStore(t)
	first := completePendingFoundationReviewForTest(t, st)
	second, err := st.ReviseFoundation("make the premise sharper")
	if err != nil {
		t.Fatal(err)
	}
	firstFence := &FoundationGenerationFence{Generation: first.FoundationGeneration, BaseRevision: first.FoundationBaseRevision}
	secondFence := &FoundationGenerationFence{Generation: second.FoundationGeneration, BaseRevision: second.FoundationBaseRevision}

	restarted := NewStore(st.Dir())
	if _, err := restarted.SaveFoundationPremise(firstFence, "stale premise"); foundationReviewCode(err) != FoundationReviewErrorStale {
		t.Fatalf("old generation error = %v", err)
	}
	if _, err := restarted.SaveFoundationPremise(secondFence, "current premise"); err != nil {
		t.Fatalf("current generation failed: %v", err)
	}
	if _, err := restarted.SaveFoundationPremise(secondFence, "duplicate premise"); foundationReviewCode(err) != FoundationReviewErrorStale {
		t.Fatalf("duplicate section error = %v", err)
	}
	current, err := restarted.RunMeta.PlanningReview()
	if err != nil || current == nil || !reflect.DeepEqual(current.FoundationSections, []string{"premise"}) {
		t.Fatalf("durable generation state = %+v err=%v", current, err)
	}
}

func TestFoundationGenerationRejectsUnfencedSemanticMutation(t *testing.T) {
	st := foundationReviewTestStore(t)
	review := &domain.PlanningReview{Brief: "collecting fixture", StartPrompt: "start"}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatal(err)
	}
	candidate, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	candidate.Premise = "unfenced overwrite"
	if _, err := st.Foundation.SaveCAS(candidate, candidate.Revision); foundationReviewCode(err) != FoundationReviewErrorStale {
		t.Fatalf("unfenced SaveCAS error = %v", err)
	}
}

func TestFoundationReviewRejectsCoreCastRewriteAndLegacyApproval(t *testing.T) {
	st := foundationReviewTestStore(t)
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	foundation.Characters[0].Arc = "rewritten"
	if _, err := st.Foundation.SaveCAS(foundation, foundation.Revision); err == nil {
		t.Fatal("confirmed core character rewrite was accepted")
	}
	legacy := &domain.PlanningReview{Status: domain.PlanningReviewStatusPending, Kind: domain.PlanningReviewKindBlueprint}
	if err := st.RunMeta.SetPlanningReview(legacy); err != nil {
		t.Fatal(err)
	}
	if err := st.RequireConfirmedFoundation(); foundationReviewCode(err) != FoundationReviewErrorStage {
		t.Fatalf("legacy review gate error = %v", err)
	}
}

func TestAuthoritativeOutlineGateBlocksDirectBypassesAndPreservesAdaptationSemantics(t *testing.T) {
	unapproved := foundationReviewTestStore(t)
	calls := map[string]func() error{
		"flat": func() error {
			return unapproved.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "Opening"}})
		},
		"layered": func() error {
			return unapproved.Outline.SaveLayeredOutline([]domain.VolumeOutline{{Index: 1, Title: "Opening"}})
		},
		"compass": func() error {
			return unapproved.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "earned ending"})
		},
		"append": func() error { return unapproved.AppendVolume(domain.VolumeOutline{Index: 1, Title: "Opening"}) },
		"repair": func() error {
			return unapproved.RepairArcOutline(1, 1, []domain.OutlineEntry{{Chapter: 1, Title: "Opening"}})
		},
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("%s bypass succeeded", name)
		}
	}

	approved := approvedFoundationReviewTestStore(t)
	if err := approved.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "Approved"}}); err != nil {
		t.Fatalf("approved outline blocked: %v", err)
	}

	adaptation := NewStore(t.TempDir())
	if err := adaptation.Adaptation.SavePlan(domain.AdaptationPlan{
		Status:   domain.AdaptationPlanStatusConfirmed,
		Chapters: []domain.AdaptationChapterPlan{{Chapter: 1, Title: "Adapted opening"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := adaptation.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "Adapted opening"}}); err != nil {
		t.Fatalf("PR-03 rewrote confirmed adaptation semantics: %v", err)
	}
}

func TestFoundationReviewCannotBeginWithoutConfirmedCoreCast(t *testing.T) {
	st := NewStore(t.TempDir())
	_, err := st.BeginFoundationReview(&domain.PlanningReview{Brief: "brief"})
	if foundationReviewCode(err) != FoundationReviewErrorStage {
		t.Fatalf("begin without core cast error = %v", err)
	}
}

func TestOriginalCharacterReviewPreservesSeededPremiseSection(t *testing.T) {
	st := NewStore(t.TempDir())
	if _, err := st.SaveFoundationPremise(nil, "共创确认后的故事前提"); err != nil {
		t.Fatal(err)
	}
	review := &domain.PlanningReview{Brief: "共创确认后的故事前提"}
	if _, err := st.BeginOriginalCharacterReview(review); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(review.FoundationSections, []string{"premise"}) {
		t.Fatalf("Foundation sections = %+v", review.FoundationSections)
	}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if review.FoundationBaseRevision != foundation.Revision || foundation.Premise != review.Brief {
		t.Fatalf("review=%+v foundation=%+v", review, foundation)
	}
}

func completePendingFoundationReviewForTest(t *testing.T, st *Store) *domain.PlanningReview {
	t.Helper()
	review := &domain.PlanningReview{Brief: "pending fixture", StartPrompt: "start"}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatal(err)
	}
	fence := &FoundationGenerationFence{Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationPremise(fence, "A complete pending premise"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationCharacters(fence, foundation.Characters); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationRelationships(fence, foundation.Relationships); err != nil {
		t.Fatal(err)
	}
	review, err = st.SaveFoundationWorldRules(fence, []domain.WorldRule{{ID: "rule-pending", Rule: "Consequences persist", Strength: domain.WorldRuleStrengthHard}})
	if err != nil {
		t.Fatal(err)
	}
	return review
}

func foundationReviewTestStore(t *testing.T) *Store {
	t.Helper()
	st := NewStore(t.TempDir())
	establishConfirmedNormalCoreCastForStoreTest(t, st)
	return st
}

func establishConfirmedNormalCoreCastForStoreTest(t *testing.T, st *Store) {
	t.Helper()
	binding, err := st.CoreCast.SaveGateBinding(CoreCastGateBinding{Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash"})
	if err != nil {
		t.Fatal(err)
	}
	contract := storeCompleteNormalCoreCast()
	contract.DraftRevision = binding.DraftRevision
	contract.DraftHash = binding.DraftHash
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
}

func approvedFoundationReviewTestStore(t *testing.T) *Store {
	t.Helper()
	st := foundationReviewTestStore(t)
	approveFoundationForStoreTest(t, st)
	return st
}

func establishApprovedNormalFoundationForStoreTest(t *testing.T, st *Store) {
	t.Helper()
	establishConfirmedNormalCoreCastForStoreTest(t, st)
	approveFoundationForStoreTest(t, st)
}

func establishConfirmedAdaptationCoreCastForStoreTest(t *testing.T, st *Store) {
	t.Helper()
	binding, err := st.CoreCast.SaveGateBinding(CoreCastGateBinding{
		Mode: domain.CoreCastModeAdaptation, DraftRevision: 1, DraftHash: "adaptation-draft",
		SourceSignature: "source-signature", AdaptationIntentHash: "adaptation-intent",
	})
	if err != nil {
		t.Fatal(err)
	}
	contract := storeCompleteNormalCoreCast()
	contract.Mode = domain.CoreCastModeAdaptation
	contract.DraftRevision = binding.DraftRevision
	contract.DraftHash = binding.DraftHash
	contract.SourceSignature = binding.SourceSignature
	contract.AdaptationIntentHash = binding.AdaptationIntentHash
	contract.Members[0].InclusionRationale = "required by the adaptation target"
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
}

func approveFoundationForStoreTest(t *testing.T, st *Store) *domain.PlanningReview {
	t.Helper()
	review := completePendingFoundationReviewForTest(t, st)
	review, err := st.ConfirmFoundation(review.FoundationRevision, review.FoundationAuditSignature)
	if err != nil {
		t.Fatal(err)
	}
	return review
}

func foundationReviewCode(err error) string {
	var reviewErr *FoundationReviewError
	if errors.As(err, &reviewErr) {
		return reviewErr.Code
	}
	return ""
}
