package store

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const castPromotionWorkflowPath = "meta/cast_promotion.json"

func (s *CastStore) LoadPromotionWorkflow() (*domain.CastPromotionWorkflow, error) {
	var workflow domain.CastPromotionWorkflow
	if err := s.io.ReadJSON(castPromotionWorkflowPath, &workflow); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &workflow, nil
}

func (s *CastStore) SavePromotionCandidate(
	runID, idempotencyKey string,
	candidate domain.Character,
	relationships []domain.CharacterRelationship,
	foundation domain.StoryFoundation,
) (*domain.CastPromotionWorkflow, error) {
	runID = strings.TrimSpace(runID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if runID == "" || idempotencyKey == "" {
		return nil, fmt.Errorf("cast promotion analyze run_id and idempotency_key are required")
	}
	pending, err := s.PendingPromotions()
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, fmt.Errorf("no pending cast promotion")
	}
	candidate.ID = strings.TrimSpace(candidate.ID)
	candidate.Name = strings.TrimSpace(candidate.Name)
	if candidate.ID == "" || candidate.Name == "" {
		return nil, fmt.Errorf("cast promotion candidate requires stable ID and name")
	}
	for _, existing := range foundation.Characters {
		if existing.ID == candidate.ID {
			return nil, fmt.Errorf("cast promotion candidate ID %q already exists", candidate.ID)
		}
	}
	merged := foundation
	merged.Characters = append(append([]domain.Character(nil), foundation.Characters...), candidate)
	merged.Relationships = append(append([]domain.CharacterRelationship(nil), foundation.Relationships...), relationships...)
	normalized, err := domain.NormalizeStoryFoundation(merged)
	if err != nil {
		return nil, fmt.Errorf("normalize cast promotion candidate: %w", err)
	}
	if err := domain.ValidateStoryFoundation(normalized); err != nil {
		return nil, fmt.Errorf("validate cast promotion candidate: %w", err)
	}
	digest := castPromotionDigest(candidate, relationships)
	existingWorkflow, err := s.LoadPromotionWorkflow()
	if err != nil {
		return nil, err
	}
	if existingWorkflow != nil &&
		existingWorkflow.AnalyzeIdempotencyKey == idempotencyKey &&
		existingWorkflow.CandidateDigest == digest {
		return existingWorkflow, nil
	}
	workflow := domain.CastPromotionWorkflow{
		Version: 1, LedgerName: pending[0].Name,
		Status:                 domain.CastPromotionCandidateReady,
		BaseFoundationRevision: foundation.Revision,
		AnalyzeRunID:           runID, AnalyzeIdempotencyKey: idempotencyKey,
		Candidate: &candidate, Relationships: relationships,
		CandidateDigest: digest,
	}
	if err := s.io.WriteJSON(castPromotionWorkflowPath, workflow); err != nil {
		return nil, err
	}
	return &workflow, nil
}

func (s *CastStore) SavePromotionReview(
	runID, idempotencyKey, candidateDigest string,
	findings []domain.CharacterCardReviewFinding,
) (*domain.CastPromotionWorkflow, error) {
	workflow, err := s.LoadPromotionWorkflow()
	if err != nil {
		return nil, err
	}
	if workflow == nil || workflow.Status == domain.CastPromotionPending {
		return nil, fmt.Errorf("cast promotion review requires a candidate")
	}
	runID = strings.TrimSpace(runID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if runID == "" || idempotencyKey == "" || candidateDigest != workflow.CandidateDigest {
		return nil, fmt.Errorf("cast promotion review binding is stale or incomplete")
	}
	if runID == workflow.AnalyzeRunID {
		return nil, fmt.Errorf("cast promotion review must use an independent run")
	}
	if workflow.ReviewIdempotencyKey == idempotencyKey &&
		workflow.ReviewRunID == runID &&
		workflow.CandidateDigest == candidateDigest {
		return workflow, nil
	}
	workflow.ReviewRunID = runID
	workflow.ReviewIdempotencyKey = idempotencyKey
	workflow.Findings = append([]domain.CharacterCardReviewFinding(nil), findings...)
	workflow.Status = domain.CastPromotionReviewPassed
	for _, finding := range findings {
		if finding.Blocking || finding.Severity == domain.CharacterCardSeverityBlocking {
			workflow.Status = domain.CastPromotionReviewNeedsChange
			break
		}
	}
	if err := s.io.WriteJSON(castPromotionWorkflowPath, workflow); err != nil {
		return nil, err
	}
	return workflow, nil
}

func (s *Store) ConfirmCastPromotion(idempotencyKey, candidateDigest string) (*domain.CastPromotionWorkflow, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	workflow, err := s.Cast.LoadPromotionWorkflow()
	if err != nil {
		return nil, err
	}
	if workflow == nil || workflow.Candidate == nil {
		return nil, fmt.Errorf("cast promotion candidate is missing")
	}
	if workflow.Status == domain.CastPromotionConfirmed &&
		workflow.ConfirmationKey == idempotencyKey &&
		workflow.CandidateDigest == candidateDigest {
		return workflow, nil
	}
	if workflow.Status != domain.CastPromotionReviewPassed || idempotencyKey == "" ||
		candidateDigest != workflow.CandidateDigest {
		return nil, fmt.Errorf("cast promotion requires a current passing review and explicit confirmation")
	}
	foundation, err := s.Foundation.Load()
	if err != nil {
		return nil, err
	}
	if foundation.Revision != workflow.BaseFoundationRevision {
		for _, character := range foundation.Characters {
			if character.ID != workflow.Candidate.ID {
				continue
			}
			if !reflect.DeepEqual(character, *workflow.Candidate) {
				return nil, fmt.Errorf("promoted character ID %q now refers to different canonical content", character.ID)
			}
			workflow.BaseFoundationRevision = foundation.Revision
			workflow.Status = domain.CastPromotionConfirmed
			workflow.ConfirmationKey = idempotencyKey
			if err := s.Cast.MarkPromoted(workflow.LedgerName, workflow.Candidate.ID); err != nil {
				return nil, fmt.Errorf("recover promoted cast link: %w", err)
			}
			if err := s.Cast.io.WriteJSON(castPromotionWorkflowPath, workflow); err != nil {
				return nil, err
			}
			return workflow, nil
		}
		return nil, fmt.Errorf("StoryFoundation changed before cast promotion confirmation")
	}
	foundation.Characters = append(foundation.Characters, *workflow.Candidate)
	foundation.Relationships = append(foundation.Relationships, workflow.Relationships...)
	saved, err := s.Foundation.SaveCAS(foundation, foundation.Revision)
	if err != nil {
		return nil, fmt.Errorf("publish cast promotion: %w", err)
	}
	workflow.BaseFoundationRevision = saved.Revision
	workflow.Status = domain.CastPromotionConfirmed
	workflow.ConfirmationKey = idempotencyKey
	if err := s.Cast.MarkPromoted(workflow.LedgerName, workflow.Candidate.ID); err != nil {
		return nil, fmt.Errorf("link promoted cast entry: %w", err)
	}
	if err := s.Cast.io.WriteJSON(castPromotionWorkflowPath, workflow); err != nil {
		return nil, err
	}
	return workflow, nil
}

func castPromotionDigest(candidate domain.Character, relationships []domain.CharacterRelationship) string {
	data, _ := json.Marshal(struct {
		Candidate     domain.Character               `json:"candidate"`
		Relationships []domain.CharacterRelationship `json:"relationships"`
	}{candidate, relationships})
	return domain.ContentSignature(data)
}
