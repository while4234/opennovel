package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestCastPromotionUsesIndependentReviewAndConfirmsIdempotently(t *testing.T) {
	st := newCastTestStore(t)
	foundation, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Characters:            []domain.Character{{ID: "hero", Name: "Hero", Tier: "core"}},
		RelationshipsReviewed: true,
	}, 0)
	if err != nil {
		t.Fatalf("SaveCAS Foundation: %v", err)
	}
	for _, chapter := range []int{1, 2, 3} {
		if err := st.Cast.MergeAppearances(chapter, []string{"Keeper"}, []domain.CastIntro{{Name: "Keeper", BriefRole: "gate keeper"}}, nil); err != nil {
			t.Fatalf("MergeAppearances %d: %v", chapter, err)
		}
	}
	pending, err := st.Cast.PendingPromotions()
	if err != nil || len(pending) != 1 || pending[0].PromotionReason != "repeated_appearance" {
		t.Fatalf("promotion trigger = %+v, err=%v", pending, err)
	}
	candidate := domain.Character{
		ID: "keeper", Name: "Keeper", Tier: "secondary", Role: "gate keeper",
		Description: "guards the gate", Arc: "chooses a side", Traits: []string{"watchful"},
		Goal: "protect the gate", Motivation: "duty", Voice: "brief",
		Constraints: []string{"does not abandon the post without cause"},
	}
	workflow, err := st.Cast.SavePromotionCandidate("analyze-run", "analyze-key", candidate, nil, foundation)
	if err != nil {
		t.Fatalf("SavePromotionCandidate: %v", err)
	}
	if _, err := st.Cast.SavePromotionReview("analyze-run", "review-key", workflow.CandidateDigest, nil); err == nil {
		t.Fatal("same-run review was accepted")
	}
	workflow, err = st.Cast.SavePromotionReview("review-run", "review-key", workflow.CandidateDigest, nil)
	if err != nil || workflow.Status != domain.CastPromotionReviewPassed {
		t.Fatalf("SavePromotionReview = %+v, err=%v", workflow, err)
	}
	confirmed, err := st.ConfirmCastPromotion("confirm-key", workflow.CandidateDigest)
	if err != nil {
		t.Fatalf("ConfirmCastPromotion: %v", err)
	}
	retry, err := st.ConfirmCastPromotion("confirm-key", workflow.CandidateDigest)
	if err != nil || retry.Status != domain.CastPromotionConfirmed {
		t.Fatalf("idempotent confirmation = %+v, err=%v", retry, err)
	}
	entries, _ := st.Cast.Load()
	if len(entries) != 1 || !entries[0].Promoted || entries[0].TargetCharacterID != "keeper" {
		t.Fatalf("linked ledger = %+v", entries)
	}
	if confirmed.Candidate == nil || confirmed.Candidate.ID != "keeper" {
		t.Fatalf("confirmed workflow = %+v", confirmed)
	}
}
