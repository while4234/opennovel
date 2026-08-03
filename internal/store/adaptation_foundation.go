package store

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type AdaptationFoundationReviewError struct {
	Code   string
	Err    error
	Review *domain.AdaptationFoundationReview
}

func (e *AdaptationFoundationReviewError) Error() string {
	if e == nil || e.Err == nil {
		return "adaptation target foundation review failed"
	}
	return e.Err.Error()
}

func (e *AdaptationFoundationReviewError) Unwrap() error { return e.Err }

const (
	AdaptationFoundationErrorStale      = "adaptation_foundation_stale"
	AdaptationFoundationErrorStage      = "adaptation_foundation_invalid_stage"
	AdaptationFoundationErrorValidation = "adaptation_foundation_validation_failed"
	AdaptationFoundationErrorReadonly   = "adaptation_foundation_readonly"
)

func (s *AdaptationStore) LoadTargetFoundationReview() (*domain.AdaptationFoundationReview, error) {
	var review domain.AdaptationFoundationReview
	if err := s.io.ReadJSON(adaptationTargetFoundationReviewFile, &review); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := review.Validate(); err != nil {
		return nil, err
	}
	review.BlockingReasons = append([]string(nil), review.BlockingReasons...)
	return &review, nil
}

func (s *AdaptationStore) saveTargetFoundationReview(review domain.AdaptationFoundationReview) error {
	if err := review.Validate(); err != nil {
		return err
	}
	return s.io.WriteJSON(adaptationTargetFoundationReviewFile, review)
}

func (s *Store) CurrentAdaptationFoundationBinding(workflowRevision int) (domain.AdaptationFoundationBinding, domain.StoryFoundation, domain.CoreCastContract, error) {
	if s == nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("store is required")
	}
	manifest, err := s.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("load adaptation source manifest: %w", err)
	}
	sourceFoundation, err := s.Adaptation.LoadSourceFoundation()
	if err != nil || sourceFoundation == nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("load adaptation source foundation: %w", err)
	}
	intent, err := s.Adaptation.LoadCoCreateIntent()
	if err != nil || intent == nil || strings.TrimSpace(intent.IntentHash) == "" {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("current adaptation intent is required")
	}
	gate, err := s.CoreCast.LoadGateBinding()
	if err != nil || gate == nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("load adaptation core cast gate: %w", err)
	}
	if gate.Mode != domain.CoreCastModeAdaptation {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("adaptation target foundation requires adaptation core cast")
	}
	contract, err := s.CoreCast.RequireConfirmedGate(*gate, nil, nil, nil)
	if err != nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("adaptation core cast confirmation is stale: %w", err)
	}
	sourceSignature := AdaptationSourceSignature(*manifest)
	if gate.SourceSignature != sourceSignature || contract.SourceSignature != sourceSignature {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("adaptation source signature changed")
	}
	if gate.AdaptationIntentHash != intent.IntentHash || contract.AdaptationIntentHash != intent.IntentHash {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, fmt.Errorf("adaptation intent changed")
	}
	target, err := s.Foundation.Load()
	if err != nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, err
	}
	if err := domain.ValidateFoundationComplete(target, contract); err != nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, err
	}
	targetSignature, err := domain.FoundationAuditSignature(target)
	if err != nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, err
	}
	binding := domain.AdaptationFoundationBinding{
		SourceSignature: sourceSignature, TargetFoundationAuditSignature: targetSignature,
		CoreCastSignature: contract.ContentSignature, AdaptationIntentHash: intent.IntentHash,
		WorkflowRevision: workflowRevision,
	}
	if err := binding.Validate(); err != nil {
		return domain.AdaptationFoundationBinding{}, domain.StoryFoundation{}, domain.CoreCastContract{}, err
	}
	return binding, target, contract, nil
}

func (s *Store) SaveAdaptationTargetFoundationCandidate(candidate domain.StoryFoundation, expectedWorkflowRevision int, brief, feedback string) (*domain.AdaptationFoundationReview, error) {
	if s == nil {
		return nil, fmt.Errorf("store is required")
	}
	var savedReview *domain.AdaptationFoundationReview
	err := s.WithAdaptationConfirmationTransaction(func() error {
		workflow, err := s.Adaptation.LoadPlanningWorkflow()
		if err != nil || workflow == nil || workflow.Stage != domain.AdaptationPlanningStageTargetFoundationGenerating || workflow.Revision != expectedWorkflowRevision {
			return &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStage, Err: fmt.Errorf("target foundation generation workflow is not current")}
		}
		current, err := s.Foundation.Load()
		if err != nil {
			return err
		}
		gate, err := s.CoreCast.LoadGateBinding()
		if err != nil || gate == nil {
			return fmt.Errorf("load core cast gate: %w", err)
		}
		contract, err := s.CoreCast.RequireConfirmedGate(*gate, nil, nil, nil)
		if err != nil {
			return err
		}
		candidate.Revision = current.Revision
		if err := domain.ValidateFoundationComplete(candidate, contract); err != nil {
			return &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorValidation, Err: err}
		}
		saved, err := s.Foundation.SaveCAS(candidate, current.Revision)
		if err != nil {
			return err
		}
		pendingWorkflow, err := s.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageFoundationReviewPending, expectedWorkflowRevision)
		if err != nil {
			return err
		}
		binding, _, _, err := s.CurrentAdaptationFoundationBinding(pendingWorkflow.Revision)
		if err != nil {
			return err
		}
		previous, err := s.Adaptation.LoadTargetFoundationReview()
		if err != nil {
			return err
		}
		generation := int64(1)
		if previous != nil {
			generation = previous.Generation + 1
		}
		review := domain.AdaptationFoundationReview{
			Version: domain.AdaptationFoundationReviewVersion, State: domain.AdaptationFoundationReviewPending,
			FoundationRevision: saved.Revision, Binding: binding, Generation: generation,
			Brief:    strings.TrimSpace(brief),
			Feedback: strings.TrimSpace(feedback), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := s.Adaptation.saveTargetFoundationReview(review); err != nil {
			return err
		}
		savedReview = &review
		return nil
	})
	return savedReview, err
}

func (s *Store) ConfirmAdaptationTargetFoundation(expectedRevision int64, expectedAuditSignature string) (*domain.AdaptationFoundationReview, error) {
	review, err := s.Adaptation.LoadTargetFoundationReview()
	if err != nil || review == nil {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStage, Err: fmt.Errorf("adaptation target foundation review is required"), Review: review}
	}
	if review.State == domain.AdaptationFoundationReviewReadonly {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorReadonly, Err: fmt.Errorf("adaptation target foundation is readonly: %s", review.ReadonlyReason), Review: review}
	}
	if review.State != domain.AdaptationFoundationReviewPending {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStage, Err: fmt.Errorf("adaptation target foundation is not pending review"), Review: review}
	}
	workflow, err := s.Adaptation.LoadPlanningWorkflow()
	if err != nil || workflow == nil || workflow.Stage != domain.AdaptationPlanningStageFoundationReviewPending || workflow.Revision != review.Binding.WorkflowRevision {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStale, Err: fmt.Errorf("adaptation target foundation workflow revision is stale"), Review: review}
	}
	current, target, _, err := s.CurrentAdaptationFoundationBinding(workflow.Revision)
	if err != nil {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStale, Err: err, Review: review}
	}
	if expectedRevision != target.Revision {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStale, Err: fmt.Errorf("adaptation target foundation revision changed"), Review: review}
	}
	if strings.TrimSpace(expectedAuditSignature) != current.TargetFoundationAuditSignature {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStale, Err: fmt.Errorf("adaptation target foundation signature changed"), Review: review}
	}
	if err := adaptationFoundationBindingMismatch(review.Binding, current); err != nil {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStale, Err: err, Review: review}
	}
	if !target.RelationshipsReviewed {
		target.RelationshipsReviewed = true
		saved, saveErr := s.Foundation.SaveCAS(target, target.Revision)
		if saveErr != nil {
			return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorValidation, Err: fmt.Errorf("mark target relationships reviewed: %w", saveErr), Review: review}
		}
		review.FoundationRevision = saved.Revision
	}
	review.State = domain.AdaptationFoundationReviewApproved
	review.ConfirmedAt = time.Now().UTC().Format(time.RFC3339Nano)
	review.UpdatedAt = review.ConfirmedAt
	if err := s.Adaptation.saveTargetFoundationReview(*review); err != nil {
		return review, err
	}
	return review, nil
}

func (s *Store) RequireConfirmedAdaptationFoundation() (*domain.AdaptationFoundationReview, error) {
	review, err := s.Adaptation.LoadTargetFoundationReview()
	if err != nil || review == nil || review.State != domain.AdaptationFoundationReviewApproved || strings.TrimSpace(review.ConfirmedAt) == "" {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStage, Err: fmt.Errorf("adaptation target foundation confirmation gate is not satisfied"), Review: review}
	}
	workflow, err := s.Adaptation.LoadPlanningWorkflow()
	if err != nil || workflow == nil {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStale, Err: fmt.Errorf("adaptation target foundation workflow is missing"), Review: review}
	}
	current, target, _, err := s.CurrentAdaptationFoundationBinding(review.Binding.WorkflowRevision)
	if err == nil && target.Revision != review.FoundationRevision {
		err = fmt.Errorf("adaptation target foundation revision changed")
	}
	if err == nil {
		err = adaptationFoundationBindingMismatch(review.Binding, current)
	}
	if err != nil {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStale, Err: err, Review: review}
	}
	return review, nil
}

func (s *Store) MarkAdaptationTargetFoundationPending(feedback string) (*domain.AdaptationFoundationReview, error) {
	review, err := s.Adaptation.LoadTargetFoundationReview()
	if err != nil || review == nil {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorStage, Err: fmt.Errorf("adaptation target foundation review is required"), Review: review}
	}
	if review.State == domain.AdaptationFoundationReviewReadonly {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorReadonly, Err: fmt.Errorf("adaptation target foundation is readonly: %s", review.ReadonlyReason), Review: review}
	}
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return review, &AdaptationFoundationReviewError{Code: AdaptationFoundationErrorValidation, Err: fmt.Errorf("adaptation target foundation feedback is required"), Review: review}
	}
	workflow, err := s.Adaptation.LoadPlanningWorkflow()
	if err != nil || workflow == nil {
		return review, err
	}
	if _, err := s.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageTargetFoundationGenerating, workflow.Revision); err != nil {
		return review, err
	}
	review.State = domain.AdaptationFoundationReviewGenerating
	review.Feedback = feedback
	review.ConfirmedAt = ""
	review.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.Adaptation.saveTargetFoundationReview(*review); err != nil {
		return review, err
	}
	return review, nil
}

func (s *Store) CurrentAdaptationArtifactBinding() (domain.AdaptationFoundationBinding, error) {
	review, err := s.RequireConfirmedAdaptationFoundation()
	if err != nil {
		return domain.AdaptationFoundationBinding{}, err
	}
	workflow, err := s.Adaptation.LoadPlanningWorkflow()
	if err != nil || workflow == nil {
		return domain.AdaptationFoundationBinding{}, fmt.Errorf("adaptation planning workflow is required")
	}
	binding := review.Binding
	binding.WorkflowRevision = workflow.Revision
	return binding, binding.Validate()
}

func (s *Store) ValidateAdaptationArtifactBinding(binding *domain.AdaptationFoundationBinding) error {
	if binding == nil {
		return fmt.Errorf("adaptation artifact has no target foundation binding")
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	review, err := s.RequireConfirmedAdaptationFoundation()
	if err != nil {
		return err
	}
	workflow, err := s.Adaptation.LoadPlanningWorkflow()
	if err != nil || workflow == nil {
		return fmt.Errorf("adaptation planning workflow is required")
	}
	comparison := *binding
	comparison.WorkflowRevision = review.Binding.WorkflowRevision
	if err := adaptationFoundationBindingMismatch(review.Binding, comparison); err != nil {
		return err
	}
	if binding.WorkflowRevision <= review.Binding.WorkflowRevision || binding.WorkflowRevision > workflow.Revision {
		return fmt.Errorf("adaptation artifact workflow revision changed")
	}
	return nil
}

func adaptationFoundationBindingMismatch(expected, current domain.AdaptationFoundationBinding) error {
	switch {
	case expected.SourceSignature != current.SourceSignature:
		return fmt.Errorf("adaptation source signature changed")
	case expected.TargetFoundationAuditSignature != current.TargetFoundationAuditSignature:
		return fmt.Errorf("adaptation target foundation signature changed")
	case expected.CoreCastSignature != current.CoreCastSignature:
		return fmt.Errorf("adaptation core cast signature changed")
	case expected.AdaptationIntentHash != current.AdaptationIntentHash:
		return fmt.Errorf("adaptation intent signature changed")
	case expected.WorkflowRevision != current.WorkflowRevision:
		return fmt.Errorf("adaptation workflow revision changed")
	default:
		return nil
	}
}

// RebindAdaptationTargetFoundationForRevision advances only the target-side
// review binding after a Foundation RevisionSession has published its target
// candidate. It is intentionally callable only under the narrow Foundation
// adaptation command fence and never touches source artifacts.
func (s *Store) RebindAdaptationTargetFoundationForRevision(sessionID string, foundationRevision int64, targetAuditSignature string) (*domain.AdaptationFoundationReview, error) {
	if s == nil || s.Revisions == nil {
		return nil, fmt.Errorf("store is required")
	}
	var rebound *domain.AdaptationFoundationReview
	err := s.Revisions.withRevisionTransaction(func() error {
		state, err := s.Revisions.loadUnlocked()
		if err != nil {
			return err
		}
		active, ok := state.Sessions[state.ActiveSessionID]
		if !ok || active.ID != strings.TrimSpace(sessionID) || !foundationAdaptationCommandAllows(state, "change planning workflow") {
			return fmt.Errorf("Foundation revision %q does not own adaptation target rebinding", sessionID)
		}
		review, err := s.Adaptation.LoadTargetFoundationReview()
		if err != nil || review == nil || review.State != domain.AdaptationFoundationReviewApproved {
			return fmt.Errorf("approved adaptation target Foundation review is required: %w", err)
		}
		workflow, err := s.Adaptation.LoadPlanningWorkflow()
		if err != nil || workflow == nil {
			return fmt.Errorf("adaptation workflow is required: %w", err)
		}
		binding, target, _, err := s.CurrentAdaptationFoundationBinding(workflow.Revision)
		if err != nil {
			return err
		}
		if target.Revision != foundationRevision || binding.TargetFoundationAuditSignature != strings.TrimSpace(targetAuditSignature) {
			return fmt.Errorf("published adaptation target Foundation binding is stale")
		}
		if review.FoundationRevision == foundationRevision && adaptationFoundationBindingMismatch(review.Binding, binding) == nil {
			copy := *review
			rebound = &copy
			return nil
		}
		review.FoundationRevision = foundationRevision
		review.Binding = binding
		review.Generation++
		review.ConfirmedAt = time.Now().UTC().Format(time.RFC3339Nano)
		review.UpdatedAt = review.ConfirmedAt
		if err := s.Adaptation.saveTargetFoundationReview(*review); err != nil {
			return err
		}
		copy := *review
		rebound = &copy
		return nil
	})
	return rebound, err
}
