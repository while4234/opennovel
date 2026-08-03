package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// CompleteMissingCharacterGenders performs a narrow, post-writing metadata
// correction. It may only fill an empty gender; it cannot change an existing
// identity value or any other Foundation field. The existing human approval
// remains valid because the caller is supplying previously omitted structured
// identity metadata, not revising story intent.
func (s *Store) CompleteMissingCharacterGenders(
	expectedRevision int64,
	genders map[string]string,
) (domain.StoryFoundation, error) {
	if s == nil || s.Foundation == nil || s.RunMeta == nil || s.Revisions == nil {
		return domain.StoryFoundation{}, fmt.Errorf("story foundation store is unavailable")
	}
	if len(genders) == 0 {
		return domain.StoryFoundation{}, fmt.Errorf("at least one character gender is required")
	}

	var saved domain.StoryFoundation
	err := s.Revisions.withRevisionTransaction(func() error {
		current, err := s.Foundation.Load()
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return &FoundationConflictError{Expected: expectedRevision, Actual: current.Revision}
		}
		review, err := s.RunMeta.PlanningReview()
		if err != nil {
			return err
		}
		if review == nil || review.FoundationStatus != domain.FoundationReviewStatusApproved ||
			strings.TrimSpace(review.FoundationConfirmedAt) == "" {
			return fmt.Errorf("missing-gender correction requires an approved Foundation")
		}

		candidate := domain.CloneStoryFoundation(current)
		pending := make(map[string]string, len(genders))
		for id, gender := range genders {
			id = strings.TrimSpace(id)
			gender = strings.ToLower(strings.TrimSpace(gender))
			switch gender {
			case "male", "female", "nonbinary", "unspecified":
			default:
				return fmt.Errorf("character %q gender %q is invalid", id, gender)
			}
			pending[id] = gender
		}
		for index := range candidate.Characters {
			character := &candidate.Characters[index]
			gender, ok := pending[character.ID]
			if !ok {
				continue
			}
			if strings.TrimSpace(character.Gender) != "" {
				return fmt.Errorf("character %q already has gender %q; identity correction cannot overwrite it", character.ID, character.Gender)
			}
			character.Gender = gender
			delete(pending, character.ID)
		}
		if len(pending) > 0 {
			for id := range pending {
				return fmt.Errorf("character %q does not exist", id)
			}
		}

		saved, err = s.Foundation.SaveRevisionCAS(candidate, current.Revision)
		if err != nil {
			return err
		}
		auditSignature, err := domain.FoundationAuditSignature(saved)
		if err != nil {
			return err
		}
		rebound := clonePlanningReview(*review)
		rebound.FoundationRevision = saved.Revision
		rebound.FoundationAuditSignature = auditSignature
		rebound.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return s.RunMeta.setPlanningReviewAuthoritative(&rebound)
	})
	return saved, err
}

// CompleteMissingCoreCastGenders re-signs omitted genders in the confirmed
// core contract and rebinds the existing approved planning review.
func (s *Store) CompleteMissingCoreCastGenders(
	expectedRevision int64,
	genders map[string]string,
) (domain.CoreCastContract, error) {
	if s == nil || s.CoreCast == nil || s.Foundation == nil || s.RunMeta == nil || s.Revisions == nil {
		return domain.CoreCastContract{}, fmt.Errorf("core cast store is unavailable")
	}
	var saved domain.CoreCastContract
	err := s.Revisions.withRevisionTransaction(func() error {
		foundation, err := s.Foundation.Load()
		if err != nil {
			return err
		}
		review, err := s.RunMeta.PlanningReview()
		if err != nil {
			return err
		}
		if review == nil || review.FoundationStatus != domain.FoundationReviewStatusApproved ||
			strings.TrimSpace(review.FoundationConfirmedAt) == "" {
			return fmt.Errorf("missing-gender correction requires an approved Foundation")
		}
		saved, err = s.CoreCast.CompleteMissingGenders(expectedRevision, genders, foundation.Revision)
		if err != nil {
			return err
		}
		rebound := clonePlanningReview(*review)
		rebound.CoreCastSignature = saved.ContentSignature
		rebound.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return s.RunMeta.setPlanningReviewAuthoritative(&rebound)
	})
	return saved, err
}
