package store

import (
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestCompleteMissingCharacterGendersFillsOnlyEmptyValues(t *testing.T) {
	st := NewStore(t.TempDir())
	foundation, err := st.Foundation.SaveRevisionCAS(domain.StoryFoundation{
		Premise: "premise",
		Characters: []domain.Character{
			{ID: "shen", Name: "Shen"},
			{ID: "lin", Name: "Lin", Gender: "female"},
		},
		WorldRules: []domain.WorldRule{{ID: "rule", Rule: "rule"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := st.RunMeta.setPlanningReviewAuthoritative(&domain.PlanningReview{
		Kind:                  domain.PlanningReviewKindChapterOutline,
		Status:                domain.PlanningReviewStatusApproved,
		FoundationStatus:      domain.FoundationReviewStatusApproved,
		FoundationRevision:    foundation.Revision,
		FoundationConfirmedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	saved, err := st.CompleteMissingCharacterGenders(foundation.Revision, map[string]string{"shen": "male"})
	if err != nil {
		t.Fatal(err)
	}
	genders := map[string]string{}
	for _, character := range saved.Characters {
		genders[character.ID] = character.Gender
	}
	if genders["shen"] != "male" || genders["lin"] != "female" {
		t.Fatalf("unexpected genders: %+v", genders)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := domain.FoundationAuditSignature(saved)
	if err != nil {
		t.Fatal(err)
	}
	if review.FoundationRevision != saved.Revision || review.FoundationAuditSignature != signature {
		t.Fatalf("planning review was not rebound: %+v", review)
	}
}

func TestCompleteMissingCharacterGendersRejectsOverwrite(t *testing.T) {
	st := NewStore(t.TempDir())
	foundation, err := st.Foundation.SaveRevisionCAS(domain.StoryFoundation{
		Premise:    "premise",
		Characters: []domain.Character{{ID: "shen", Name: "Shen", Gender: "male"}},
		WorldRules: []domain.WorldRule{{ID: "rule", Rule: "rule"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := st.RunMeta.setPlanningReviewAuthoritative(&domain.PlanningReview{
		Kind:                  domain.PlanningReviewKindChapterOutline,
		Status:                domain.PlanningReviewStatusApproved,
		FoundationStatus:      domain.FoundationReviewStatusApproved,
		FoundationRevision:    foundation.Revision,
		FoundationConfirmedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteMissingCharacterGenders(foundation.Revision, map[string]string{"shen": "female"}); err == nil {
		t.Fatal("expected overwrite rejection")
	}
}

func TestCompleteMissingCoreCastGendersRebindsPublishedContract(t *testing.T) {
	st := NewStore(t.TempDir())
	candidate := storeCompleteNormalCoreCast()
	candidate.Members[0].Character.Gender = ""
	saved, err := st.CoreCast.SaveCAS(candidate, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	published, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := st.RunMeta.setPlanningReviewAuthoritative(&domain.PlanningReview{
		Kind:                  domain.PlanningReviewKindChapterOutline,
		Status:                domain.PlanningReviewStatusApproved,
		FoundationStatus:      domain.FoundationReviewStatusApproved,
		FoundationRevision:    foundation.Revision,
		FoundationConfirmedAt: now,
		CoreCastSignature:     published.ContentSignature,
	}); err != nil {
		t.Fatal(err)
	}

	corrected, err := st.CompleteMissingCoreCastGenders(
		published.Revision,
		map[string]string{"lin": "female"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if corrected.Members[0].Character.Gender != "female" {
		t.Fatalf("gender = %q", corrected.Members[0].Character.Gender)
	}
	if corrected.ConfirmedSignature != corrected.ContentSignature ||
		corrected.PublishReceipt.ContentSignature != corrected.ContentSignature ||
		corrected.PublishReceipt.FoundationRevision != foundation.Revision {
		t.Fatalf("corrected contract is not confirmed and published: %+v", corrected)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if review.CoreCastSignature != corrected.ContentSignature {
		t.Fatalf("planning review core signature = %q, want %q", review.CoreCastSignature, corrected.ContentSignature)
	}
	if _, err := st.CompleteMissingCoreCastGenders(
		corrected.Revision,
		map[string]string{"lin": "male"},
	); err == nil {
		t.Fatal("expected core gender overwrite rejection")
	}
}
