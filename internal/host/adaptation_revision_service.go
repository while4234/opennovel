package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type AdaptationRevisionService struct {
	store                *storepkg.Store
	revisions            *storepkg.RevisionStore
	afterCommandPrepared func()
	beforeRevisionCommit func()
	saveRevisionRuntime  func(domain.AdaptationRevisionRuntime) error
	clearRevisionRuntime func(string) error
	saveRevisionReceipt  func(string, string, string, any) error
}

type AdaptationRevisionPreviewRequest struct {
	Intent            string                `json:"intent"`
	Candidate         domain.AdaptationPlan `json:"candidate"`
	RequireAddedProse bool                  `json:"require_added_prose,omitempty"`
}

type AdaptationStructureRevisionPreview struct {
	Stage                   domain.ManuscriptStage `json:"stage"`
	BasePlanSignature       string                 `json:"base_plan_signature"`
	SourceManifestSignature string                 `json:"source_manifest_signature"`
	Candidate               domain.AdaptationPlan  `json:"candidate"`
	Impact                  domain.RevisionImpact  `json:"impact"`
	Signature               string                 `json:"signature"`
}

type AdaptationRevisionPreview struct {
	Preview *AdaptationStructureRevisionPreview `json:"preview"`
	Session *domain.RevisionSession             `json:"session"`
}

type AdaptationRevisionCommandReceiptRequest struct {
	Action           string
	ExpectedRevision int
	Preview          *AdaptationStructureRevisionPreview
	Candidate        domain.AdaptationPlan
	Evidence         []domain.RevisionAuditEvidence
	Message          string
	ImpactSignature  string
}

type committedAdaptationPublicationError struct {
	cause error
}

func (e *committedAdaptationPublicationError) Error() string { return e.cause.Error() }
func (e *committedAdaptationPublicationError) Unwrap() error { return e.cause }

type revisionRuntimeRestoreError struct {
	cause error
}

func (e *revisionRuntimeRestoreError) Error() string { return e.cause.Error() }
func (e *revisionRuntimeRestoreError) Unwrap() error { return e.cause }

func NewAdaptationRevisionService(st *storepkg.Store) *AdaptationRevisionService {
	return &AdaptationRevisionService{store: st}
}

// SetCommandPreparedHookForTesting installs deterministic synchronization at
// the durable prepared-command boundary. Production callers must leave it nil.
func (s *AdaptationRevisionService) SetCommandPreparedHookForTesting(hook func()) {
	if s != nil {
		s.afterCommandPrepared = hook
	}
}

func withAdaptationRevisionCommand[T any](s *AdaptationRevisionService, command func() (T, error)) (T, error) {
	var result T
	if s == nil || s.store == nil {
		return result, fmt.Errorf("adaptation revision store is required")
	}
	err := s.store.WithAdaptationRevisionCommand(func() error {
		var commandErr error
		result, commandErr = command()
		return commandErr
	})
	return result, err
}

func withAdaptationRevisionReceipt[T any](
	s *AdaptationRevisionService,
	idempotencyKey, operation string,
	payload any,
	publication *storepkg.AdaptationRevisionPublicationCommand,
	command func(*AdaptationRevisionService) (T, error),
) (T, error) {
	var result T
	if s == nil || s.store == nil {
		return result, fmt.Errorf("adaptation revision store is required")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	fingerprint := domain.ContentSignature(encoded)
	err = s.store.WithPreparedAdaptationRevisionCommand(idempotencyKey, operation, fingerprint, func(revisions *storepkg.RevisionStore) error {
		owned := s.withRevisionStore(revisions)
		found, err := owned.store.LoadVerifiedAdaptationRevisionServiceReceipt(idempotencyKey, operation, fingerprint, &result)
		if err != nil {
			return err
		}
		if found {
			if operation != "publish" {
				return nil
			}
			if published, ok := any(result).(*domain.RevisionSession); !ok || published == nil {
				return fmt.Errorf("adaptation publication service receipt result is invalid")
			} else if err := owned.store.VerifyCommittedAdaptationPublication(idempotencyKey, fingerprint, published); err != nil {
				return fmt.Errorf("verify committed adaptation publication replay: %w", err)
			}
			needsFinalize, err := owned.revisionStore().CommittedPublicationNeedsFinalize()
			if err != nil {
				return err
			}
			if needsFinalize {
				expected := result
				result, err = command(owned)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(expected, result) {
					return fmt.Errorf("committed adaptation publication changed during replay")
				}
			}
			return nil
		}
		var prepareErr error
		if publication == nil {
			prepareErr = owned.store.PrepareAdaptationRevisionCommand(revisions, idempotencyKey, operation, fingerprint)
		} else {
			prepareErr = owned.store.PrepareAdaptationRevisionPublicationCommand(
				revisions, idempotencyKey, fingerprint, publication.SessionID, publication.ExpectedRevision,
			)
		}
		if prepareErr != nil {
			return prepareErr
		}
		if owned.afterCommandPrepared != nil {
			owned.afterCommandPrepared()
		}
		result, err = command(owned)
		if err != nil {
			var committedErr *committedAdaptationPublicationError
			if errors.As(err, &committedErr) {
				var restoreErr *revisionRuntimeRestoreError
				if errors.As(err, &restoreErr) {
					// The internal publication is committed, but runtime recovery
					// itself was not durable. Preserve the prepared journal so the
					// next recovery can reconstruct the service receipt and finish.
					return err
				}
				if receiptErr := owned.persistRevisionReceipt(idempotencyKey, operation, fingerprint, result); receiptErr != nil {
					return errors.Join(err, receiptErr)
				}
				if completeErr := owned.store.CompleteAdaptationRevisionCommand(revisions, idempotencyKey, operation, fingerprint); completeErr != nil {
					return errors.Join(err, completeErr)
				}
				return err
			}
			return owned.rollbackReceiptCommand(err)
		}
		if err := owned.persistRevisionReceipt(idempotencyKey, operation, fingerprint, result); err != nil {
			if operation == "publish" {
				if published, ok := any(result).(*domain.RevisionSession); ok && published != nil && published.Stage == domain.RevisionStageCompleted {
					// The internal receipt is already the commit point. Leave the
					// prepared recovery evidence intact so restart can reconstruct
					// the service receipt instead of restoring the old snapshot.
					return err
				}
			}
			return owned.rollbackReceiptCommand(err)
		}
		if err := owned.store.CompleteAdaptationRevisionCommand(revisions, idempotencyKey, operation, fingerprint); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func (s *AdaptationRevisionService) withRevisionStore(revisions *storepkg.RevisionStore) *AdaptationRevisionService {
	ownedService := *s
	ownedService.revisions = revisions
	return &ownedService
}

func (s *AdaptationRevisionService) revisionStore() *storepkg.RevisionStore {
	if s.revisions != nil {
		return s.revisions
	}
	return s.store.Revisions
}

func (s *AdaptationRevisionService) persistRevisionReceipt(key, operation, fingerprint string, result any) error {
	if s.saveRevisionReceipt != nil {
		return s.saveRevisionReceipt(key, operation, fingerprint, result)
	}
	return s.store.SaveAdaptationRevisionServiceReceipt(s.revisionStore(), key, operation, fingerprint, result)
}

func (s *AdaptationRevisionService) rollbackReceiptCommand(cause error) error {
	if rollbackErr := s.store.RollbackAdaptationRevisionCommand(s.revisionStore()); rollbackErr != nil {
		return fmt.Errorf("adaptation revision command: %v; roll back receipt transaction: %w", cause, rollbackErr)
	}
	return cause
}

func (s *AdaptationRevisionService) LoadCommandReceipt(request AdaptationRevisionCommandReceiptRequest, idempotencyKey string) (*domain.RevisionSession, bool, error) {
	operation, payload, err := adaptationRevisionCommandReceiptIdentity(request)
	if err != nil {
		return nil, false, err
	}
	var result *domain.RevisionSession
	err = s.store.WithAdaptationRevisionCommand(func() error {
		encoded, encodeErr := json.Marshal(payload)
		if encodeErr != nil {
			return encodeErr
		}
		var loadErr error
		found := false
		found, loadErr = s.store.LoadVerifiedAdaptationRevisionServiceReceipt(idempotencyKey, operation, domain.ContentSignature(encoded), &result)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			result = nil
		} else if operation == "publish" {
			if verifyErr := s.store.VerifyCommittedAdaptationPublication(idempotencyKey, domain.ContentSignature(encoded), result); verifyErr != nil {
				return fmt.Errorf("verify loaded adaptation publication receipt: %w", verifyErr)
			}
		}
		return nil
	})
	return result, result != nil, err
}

func adaptationRevisionCommandReceiptIdentity(request AdaptationRevisionCommandReceiptRequest) (string, any, error) {
	action := strings.TrimSpace(request.Action)
	base := struct{ ExpectedRevision int }{request.ExpectedRevision}
	switch action {
	case "approve_impact", "approve_stage", "submit_prose_intents", "pause", "resume", "cancel":
		return action, base, nil
	case "submit_structure", "publish":
		return action, struct {
			ExpectedRevision int
			Preview          *AdaptationStructureRevisionPreview
		}{request.ExpectedRevision, request.Preview}, nil
	case "submit_details":
		return action, struct {
			ExpectedRevision int
			Candidate        domain.AdaptationPlan
		}{request.ExpectedRevision, request.Candidate}, nil
	case "record_audit":
		return action, struct {
			ExpectedRevision int
			Evidence         []domain.RevisionAuditEvidence
		}{request.ExpectedRevision, request.Evidence}, nil
	case "feedback":
		return action, struct {
			ExpectedRevision         int
			ImpactSignature, Message string
		}{request.ExpectedRevision, request.ImpactSignature, request.Message}, nil
	case "fail":
		return action, struct {
			ExpectedRevision int
			Message          string
		}{request.ExpectedRevision, request.Message}, nil
	default:
		return "", nil, fmt.Errorf("unsupported adaptation revision receipt command %q", action)
	}
}

func adaptationCommandReceiptRequest(action string, revision int) AdaptationRevisionCommandReceiptRequest {
	return AdaptationRevisionCommandReceiptRequest{Action: action, ExpectedRevision: revision}
}

func adaptationRevisionNumber(session *domain.RevisionSession) int {
	if session == nil {
		return 0
	}
	return session.Revision
}

func withAdaptationRevisionCommandReceipt[T any](s *AdaptationRevisionService, idempotencyKey string, request AdaptationRevisionCommandReceiptRequest, command func(*AdaptationRevisionService) (T, error)) (T, error) {
	operation, payload, err := adaptationRevisionCommandReceiptIdentity(request)
	if err != nil {
		var result T
		return result, err
	}
	return withAdaptationRevisionReceipt(s, idempotencyKey, operation, payload, nil, command)
}

func (s *AdaptationRevisionService) Preview(request AdaptationRevisionPreviewRequest, idempotencyKey string) (*AdaptationRevisionPreview, error) {
	return withAdaptationRevisionReceipt(s, idempotencyKey, "preview", request, nil, func(owned *AdaptationRevisionService) (*AdaptationRevisionPreview, error) {
		return owned.preview(request, idempotencyKey)
	})
}

// SealExpansionCandidate performs the production adaptation contract,
// topology, coverage and source-manifest validation without starting a
// revision. Expansion planning uses it to bind its kernel result to the exact
// PR-01 preview signature before asking for human confirmation.
func (s *AdaptationRevisionService) SealExpansionCandidate(request AdaptationRevisionPreviewRequest) (*AdaptationStructureRevisionPreview, error) {
	if s == nil || s.store == nil || strings.TrimSpace(request.Intent) == "" {
		return nil, fmt.Errorf("adaptation revision intent is required")
	}
	base, manifest, stage, completed, err := s.loadProductionContract()
	if err != nil {
		return nil, err
	}
	candidate, err := cloneAdaptationPlan(request.Candidate)
	if err != nil {
		return nil, err
	}
	if err := validateWrittenAdaptationTopology(*base, candidate, completed); err != nil {
		return nil, err
	}
	if err := domain.ValidateAdaptationRevisionPlan(*base, candidate, manifest); err != nil {
		return nil, err
	}
	impact, err := deriveAdaptationRevisionImpact(*base, candidate, completed, request.RequireAddedProse)
	if err != nil {
		return nil, err
	}
	skeleton := adaptationStructureSkeleton(*base, candidate, impact)
	preview := &AdaptationStructureRevisionPreview{
		Stage: stage, BasePlanSignature: adaptationPlanSignature(*base),
		SourceManifestSignature: domain.AdaptationSourceManifestContractSignature(*manifest),
		Candidate:               skeleton, Impact: impact,
	}
	preview.Signature = adaptationPreviewSignature(*preview)
	return preview, nil
}

func (s *AdaptationRevisionService) ApproveImpact(sessionID string, revision int, idempotencyKey string) (*domain.RevisionSession, error) {
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, adaptationCommandReceiptRequest("approve_impact", revision), func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.approveImpact(sessionID, revision, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) SubmitFeedback(session *domain.RevisionSession, impactSignature, message, idempotencyKey string) (*domain.RevisionSession, error) {
	request := adaptationCommandReceiptRequest("feedback", adaptationRevisionNumber(session))
	request.ImpactSignature, request.Message = impactSignature, message
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, request, func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.submitFeedback(session, impactSignature, message, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) RebindExpansionPreviewAfterFeedback(session *domain.RevisionSession, previousSignature, nextSignature, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("adaptation revision session is required")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	if runtime.PreviewSignature != previousSignature {
		return nil, fmt.Errorf("adaptation feedback preview runtime binding mismatch")
	}
	prior := *runtime
	runtime.PreviewSignature = nextSignature
	if err := s.persistRevisionRuntime(*runtime); err != nil {
		return nil, err
	}
	updated, err := s.revisionStore().RebindPreviewAfterFeedback(policy, storepkg.RebindRevisionPreviewInput{RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey}, PreviousSignature: previousSignature, NextSignature: nextSignature})
	if err != nil {
		return nil, s.restoreRevisionRuntime(prior, err)
	}
	return updated, nil
}

func (s *AdaptationRevisionService) Fail(session *domain.RevisionSession, message, idempotencyKey string) (*domain.RevisionSession, error) {
	request := adaptationCommandReceiptRequest("fail", adaptationRevisionNumber(session))
	request.Message = message
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, request, func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.fail(session, message, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) Cancel(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, adaptationCommandReceiptRequest("cancel", adaptationRevisionNumber(session)), func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.cancel(session, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) SubmitStructureCandidate(preview AdaptationStructureRevisionPreview, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	request := adaptationCommandReceiptRequest("submit_structure", adaptationRevisionNumber(session))
	request.Preview = &preview
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, request, func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.submitStructureCandidate(preview, session, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) SubmitDetailedOutlineCandidate(candidate domain.AdaptationPlan, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	request := adaptationCommandReceiptRequest("submit_details", adaptationRevisionNumber(session))
	request.Candidate = candidate
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, request, func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.submitDetailedOutlineCandidate(candidate, session, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) RunBatchCommand(sessionID string, command domain.AdaptationRevisionBatchCommand, targetID, message string) (*domain.AdaptationRevisionRuntime, error) {
	return withAdaptationRevisionCommand(s, func() (*domain.AdaptationRevisionRuntime, error) {
		return s.runBatchCommand(sessionID, command, targetID, message)
	})
}

func (s *AdaptationRevisionService) Pause(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, adaptationCommandReceiptRequest("pause", adaptationRevisionNumber(session)), func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.pause(session, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) Resume(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, adaptationCommandReceiptRequest("resume", adaptationRevisionNumber(session)), func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.resume(session, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) RecordAuditSet(session *domain.RevisionSession, evidence []domain.RevisionAuditEvidence, idempotencyKey string) (*domain.RevisionSession, error) {
	request := adaptationCommandReceiptRequest("record_audit", adaptationRevisionNumber(session))
	request.Evidence = evidence
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, request, func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.recordAuditSet(session, evidence, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) ApproveStage(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, adaptationCommandReceiptRequest("approve_stage", adaptationRevisionNumber(session)), func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.approveStage(session, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) SubmitProseReworkCandidate(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	return withAdaptationRevisionCommandReceipt(s, idempotencyKey, adaptationCommandReceiptRequest("submit_prose_intents", adaptationRevisionNumber(session)), func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.submitProseReworkCandidate(session, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) Publish(preview AdaptationStructureRevisionPreview, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	request := adaptationCommandReceiptRequest("publish", adaptationRevisionNumber(session))
	request.Preview = &preview
	operation, payload, err := adaptationRevisionCommandReceiptIdentity(request)
	if err != nil {
		return nil, err
	}
	publication := &storepkg.AdaptationRevisionPublicationCommand{ExpectedRevision: adaptationRevisionNumber(session)}
	if session != nil {
		publication.SessionID = session.ID
	}
	return withAdaptationRevisionReceipt(s, idempotencyKey, operation, payload, publication, func(owned *AdaptationRevisionService) (*domain.RevisionSession, error) {
		return owned.publish(preview, session, idempotencyKey)
	})
}

func (s *AdaptationRevisionService) CurrentManuscriptStage() (domain.ManuscriptStage, error) {
	if s == nil || s.store == nil {
		return "", fmt.Errorf("adaptation revision store is required")
	}
	manifest, err := s.store.Adaptation.LoadSourceManifest()
	if err != nil {
		return "", err
	}
	if manifest == nil {
		return "", fmt.Errorf("adaptation revision service requires an adaptation project")
	}
	progress, err := s.store.Progress.Load()
	if err != nil {
		return "", err
	}
	if progress != nil {
		switch progress.Phase {
		case domain.PhaseComplete:
			return domain.ManuscriptStageComplete, nil
		case domain.PhaseWriting:
			return domain.ManuscriptStageWriting, nil
		}
	}
	workflow, err := s.store.Adaptation.LoadPlanningWorkflow()
	if err != nil {
		return "", err
	}
	if workflow != nil {
		switch workflow.Stage {
		case domain.AdaptationPlanningStageVolumeReviewPending, domain.AdaptationPlanningStageDetailsGenerating:
			return domain.ManuscriptStageProposalComplete, nil
		case domain.AdaptationPlanningStageProposalReviewPending, domain.AdaptationPlanningStageConfirmed:
			return domain.ManuscriptStageOutlineComplete, nil
		}
	}
	if proposal, loadErr := s.store.Adaptation.LoadProposal(); loadErr != nil {
		return "", loadErr
	} else if proposal != nil {
		return domain.ManuscriptStageOutlineComplete, nil
	}
	if review, loadErr := s.store.Adaptation.LoadVolumeReview(); loadErr != nil {
		return "", loadErr
	} else if review != nil {
		return domain.ManuscriptStageProposalComplete, nil
	}
	if progress != nil && progress.Phase == domain.PhaseOutline {
		return domain.ManuscriptStageOutlineComplete, nil
	}
	return domain.ManuscriptStageProposalComplete, nil
}

func (s *AdaptationRevisionService) preview(request AdaptationRevisionPreviewRequest, idempotencyKey string) (*AdaptationRevisionPreview, error) {
	if strings.TrimSpace(request.Intent) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return nil, fmt.Errorf("adaptation revision intent and idempotency key are required")
	}
	base, manifest, stage, completed, err := s.loadProductionContract()
	if err != nil {
		return nil, err
	}
	candidate, err := cloneAdaptationPlan(request.Candidate)
	if err != nil {
		return nil, err
	}
	if err := validateWrittenAdaptationTopology(*base, candidate, completed); err != nil {
		return nil, err
	}
	policy := domain.NewAdaptationRevisionPolicy(stage, base, manifest)
	policy.CompletedTarget = append([]string(nil), completed...)
	if err := domain.ValidateAdaptationRevisionPlan(*base, candidate, manifest); err != nil {
		return nil, err
	}
	impact, err := deriveAdaptationRevisionImpact(*base, candidate, completed, request.RequireAddedProse)
	if err != nil {
		return nil, err
	}
	batchPlan, err := deriveAdaptationRevisionBatchPlan(candidate, impact, manifest)
	if err != nil {
		return nil, err
	}
	skeleton := adaptationStructureSkeleton(*base, candidate, impact)
	preview := &AdaptationStructureRevisionPreview{
		Stage: stage, BasePlanSignature: adaptationPlanSignature(*base),
		SourceManifestSignature: domain.AdaptationSourceManifestContractSignature(*manifest),
		Candidate:               skeleton, Impact: impact,
	}
	preview.Signature = adaptationPreviewSignature(*preview)
	session, err := s.revisionStore().Start(policy, storepkg.StartRevisionInput{
		Intent: request.Intent, Impact: impact, PreviewSignature: preview.Signature, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	runtime := domain.AdaptationRevisionRuntime{
		Version: domain.AdaptationRevisionRuntimeVersion, SessionID: session.ID, Stage: stage,
		BasePlanSignature: preview.BasePlanSignature, SourceManifestSignature: preview.SourceManifestSignature,
		PreviewSignature: preview.Signature, BatchPlan: batchPlan,
	}
	if err := s.persistRevisionRuntime(runtime); err != nil {
		_, cancelErr := s.revisionStore().Cancel(policy, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey + ":rollback"})
		if cancelErr != nil {
			return nil, fmt.Errorf("persist adaptation revision runtime: %v; cancel revision: %w", err, cancelErr)
		}
		return nil, err
	}
	return &AdaptationRevisionPreview{Preview: preview, Session: session}, nil
}

func (s *AdaptationRevisionService) approveImpact(sessionID string, revision int, idempotencyKey string) (*domain.RevisionSession, error) {
	policy, _, err := s.boundPolicy(sessionID)
	if err != nil {
		return nil, err
	}
	return s.revisionStore().ApproveImpact(policy, storepkg.RevisionMutationInput{SessionID: sessionID, ExpectedRevision: revision, IdempotencyKey: idempotencyKey})
}

func (s *AdaptationRevisionService) submitFeedback(session *domain.RevisionSession, impactSignature, message, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("adaptation revision session is required")
	}
	policy, _, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	return s.revisionStore().SubmitFeedback(policy, storepkg.RevisionFeedbackInput{
		RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey},
		StageID:               adaptationServiceApprovalStage(*session), ImpactSignature: impactSignature, Message: message,
	})
}

func (s *AdaptationRevisionService) fail(session *domain.RevisionSession, message, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("adaptation revision session is required")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	previousRuntime := *runtime
	runtime.Paused = true
	if err := s.persistRevisionRuntime(*runtime); err != nil {
		return nil, err
	}
	failed, err := s.revisionStore().Fail(policy, storepkg.RevisionFailureInput{
		RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey}, Error: message,
	})
	if err != nil {
		return nil, s.restoreRevisionRuntime(previousRuntime, err)
	}
	return failed, nil
}

func (s *AdaptationRevisionService) cancel(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("adaptation revision session is required")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	previousRuntime := *runtime
	if err := s.removeRevisionRuntime(session.ID); err != nil {
		return nil, err
	}
	cancelled, err := s.revisionStore().Cancel(policy, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey})
	if err != nil {
		return nil, s.restoreRevisionRuntime(previousRuntime, err)
	}
	return cancelled, nil
}

func (s *AdaptationRevisionService) submitStructureCandidate(preview AdaptationStructureRevisionPreview, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || adaptationServiceApprovalStage(*session) != domain.AdaptationApprovalStructure {
		return nil, fmt.Errorf("adaptation structure approval stage is not active")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	if preview.Signature != session.PreviewSignature || preview.Signature != runtime.PreviewSignature || adaptationPreviewSignature(preview) != preview.Signature {
		return nil, fmt.Errorf("adaptation structure preview substitution is not allowed")
	}
	if preview.BasePlanSignature != runtime.BasePlanSignature || preview.SourceManifestSignature != runtime.SourceManifestSignature || preview.Stage != runtime.Stage {
		return nil, fmt.Errorf("adaptation structure preview binding drifted")
	}
	artifacts, err := adaptationStructureArtifacts(preview, runtime.BatchPlan)
	if err != nil {
		return nil, err
	}
	return s.revisionStore().SubmitCandidate(policy, storepkg.SubmitRevisionCandidateInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey, Artifacts: artifacts,
	})
}

func (s *AdaptationRevisionService) submitDetailedOutlineCandidate(candidate domain.AdaptationPlan, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || adaptationServiceApprovalStage(*session) != domain.AdaptationApprovalOutline {
		return nil, fmt.Errorf("adaptation detailed-outline approval stage is not active")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	accepted, err := s.acceptedAdaptationStructure(session, policy)
	if err != nil {
		return nil, err
	}
	if adaptationPlanStructureSignature(accepted) != adaptationPlanStructureSignature(candidate) {
		return nil, fmt.Errorf("adaptation detailed outline does not match the accepted structure skeleton")
	}
	required := adaptationRequiredChapterIDs(session.Impact)
	if err := validateAdaptationDetailIsolation(accepted, candidate, required); err != nil {
		return nil, err
	}
	detailed, err := overlayAdaptationDetailedPlan(accepted, candidate, required)
	if err != nil {
		return nil, err
	}
	batchPlan, err := deriveAdaptationRevisionBatchPlan(detailed, session.Impact, policy.SourceManifest)
	if err != nil {
		return nil, err
	}
	previousRuntime := *runtime
	runtime.BatchPlan = batchPlan
	artifacts, err := adaptationDetailArtifacts(detailed, accepted, *runtime, required)
	if err != nil {
		return nil, err
	}
	if err := s.persistRevisionRuntime(*runtime); err != nil {
		return nil, err
	}
	updated, err := s.revisionStore().SubmitCandidate(policy, storepkg.SubmitRevisionCandidateInput{
		SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey, Artifacts: artifacts,
	})
	if err != nil {
		return nil, s.restoreRevisionRuntime(previousRuntime, err)
	}
	return updated, nil
}

func (s *AdaptationRevisionService) runBatchCommand(sessionID string, command domain.AdaptationRevisionBatchCommand, targetID, message string) (*domain.AdaptationRevisionRuntime, error) {
	_, runtime, err := s.boundPolicy(sessionID)
	if err != nil {
		return nil, err
	}
	if runtime.Paused {
		return nil, fmt.Errorf("adaptation revision batch work is paused")
	}
	plan := &runtime.BatchPlan
	switch command {
	case domain.AdaptationRevisionBatchStart:
		batch, commandErr := plan.StartNext()
		if commandErr != nil {
			return nil, commandErr
		}
		if batch == nil || strings.TrimSpace(targetID) != "" && batch.ID != strings.TrimSpace(targetID) {
			return nil, fmt.Errorf("next server-derived adaptation batch does not match %q", targetID)
		}
	case domain.AdaptationRevisionBatchGenerated:
		err = plan.MarkGenerated(targetID)
	case domain.AdaptationRevisionBatchAuditPass:
		err = plan.MarkLocalAudit(targetID, true, message)
	case domain.AdaptationRevisionBatchAuditFail:
		err = plan.MarkLocalAudit(targetID, false, message)
	case domain.AdaptationRevisionBatchFail:
		err = plan.Fail(targetID, message)
	case domain.AdaptationRevisionBatchResume:
		err = plan.Resume(targetID)
	case domain.AdaptationRevisionVolumeReviewStart:
		err = plan.StartVolumeReview(targetID)
	case domain.AdaptationRevisionVolumeReviewPass:
		err = plan.MarkVolumeReview(targetID, true, message)
	case domain.AdaptationRevisionVolumeReviewFail:
		err = plan.MarkVolumeReview(targetID, false, message)
	case domain.AdaptationRevisionVolumeReviewResume:
		err = plan.ResumeVolumeReview(targetID)
	case domain.AdaptationRevisionGlobalReviewStart:
		err = plan.StartWholeBookReview()
	case domain.AdaptationRevisionGlobalReviewPass:
		err = plan.MarkWholeBookReview(true, message)
	case domain.AdaptationRevisionGlobalReviewFail:
		err = plan.MarkWholeBookReview(false, message)
	case domain.AdaptationRevisionGlobalReviewResume:
		err = plan.ResumeWholeBookReview()
	default:
		return nil, fmt.Errorf("unsupported adaptation batch command %q", command)
	}
	if err != nil {
		return nil, err
	}
	if err := s.persistRevisionRuntime(*runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (s *AdaptationRevisionService) pause(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("adaptation revision session is required")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	previousRuntime := *runtime
	runtime.Paused = true
	if err := s.persistRevisionRuntime(*runtime); err != nil {
		return nil, err
	}
	paused, err := s.revisionStore().Pause(policy, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey})
	if err != nil {
		return nil, s.restoreRevisionRuntime(previousRuntime, err)
	}
	return paused, nil
}

func (s *AdaptationRevisionService) resume(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("adaptation revision session is required")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	previousRuntime := *runtime
	runtime.Paused = false
	if err := s.persistRevisionRuntime(*runtime); err != nil {
		return nil, err
	}
	resumed, err := s.revisionStore().Resume(policy, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey})
	if err != nil {
		return nil, s.restoreRevisionRuntime(previousRuntime, err)
	}
	return resumed, nil
}

func (s *AdaptationRevisionService) persistRevisionRuntime(runtime domain.AdaptationRevisionRuntime) error {
	if s.saveRevisionRuntime != nil {
		return s.saveRevisionRuntime(runtime)
	}
	return s.store.SaveAdaptationRevisionRuntime(s.revisionStore(), runtime)
}

func (s *AdaptationRevisionService) removeRevisionRuntime(sessionID string) error {
	if s.clearRevisionRuntime != nil {
		return s.clearRevisionRuntime(sessionID)
	}
	return s.store.ClearAdaptationRevisionRuntime(s.revisionStore(), sessionID)
}

func (s *AdaptationRevisionService) restoreRevisionRuntime(previous domain.AdaptationRevisionRuntime, cause error) error {
	if err := s.store.SaveAdaptationRevisionRuntime(s.revisionStore(), previous); err != nil {
		return &revisionRuntimeRestoreError{cause: fmt.Errorf("adaptation revision transition: %v; restore runtime checkpoint: %w", cause, err)}
	}
	return cause
}

func (s *AdaptationRevisionService) recordAuditSet(session *domain.RevisionSession, evidence []domain.RevisionAuditEvidence, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("adaptation revision session is required")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	if adaptationServiceApprovalStage(*session) != domain.AdaptationApprovalProse && !adaptationBatchPlanCompleted(runtime.BatchPlan) {
		return nil, fmt.Errorf("adaptation revision audits require every local, volume, and global BatchPlan checkpoint to pass")
	}
	return s.revisionStore().RecordAudit(policy, storepkg.RevisionAuditInput{
		RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey},
		CandidateSignature:    session.CandidateSignature, Evidence: evidence,
	})
}

func (s *AdaptationRevisionService) approveStage(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || session.CurrentApprovalStage() == nil {
		return nil, fmt.Errorf("adaptation revision approval stage is required")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	previousRuntime := *runtime
	updatesOutlineRuntime := adaptationServiceApprovalStage(*session) == domain.AdaptationApprovalStructure
	if updatesOutlineRuntime {
		accepted, loadErr := s.pendingAdaptationStructure(session)
		if loadErr != nil {
			return nil, loadErr
		}
		runtime.BatchPlan, err = deriveAdaptationRevisionBatchPlan(accepted, session.Impact, policy.SourceManifest)
		if err != nil {
			return nil, err
		}
		if err := s.persistRevisionRuntime(*runtime); err != nil {
			return nil, err
		}
	}
	approved, err := s.revisionStore().ApproveStage(policy, storepkg.RevisionApprovalInput{
		RevisionMutationInput: storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey},
		StageID:               session.CurrentApprovalStage().ID,
	})
	if err != nil {
		if updatesOutlineRuntime {
			return nil, s.restoreRevisionRuntime(previousRuntime, err)
		}
		return nil, err
	}
	return approved, nil
}

func (s *AdaptationRevisionService) pendingAdaptationStructure(session *domain.RevisionSession) (domain.AdaptationPlan, error) {
	for index := len(session.CandidateVersionIDs) - 1; index >= 0; index-- {
		version, err := s.revisionStore().LoadVersion(session.CandidateVersionIDs[index])
		if err != nil {
			return domain.AdaptationPlan{}, err
		}
		if version.ArtifactKind != domain.AdaptationRevisionArtifactPlanSnapshot {
			continue
		}
		var envelope domain.AdaptationPlanRevisionCandidate
		if err := json.Unmarshal(version.Payload, &envelope); err != nil {
			return domain.AdaptationPlan{}, err
		}
		return envelope.Plan, nil
	}
	return domain.AdaptationPlan{}, fmt.Errorf("pending adaptation structure snapshot is missing")
}

func (s *AdaptationRevisionService) submitProseReworkCandidate(session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil || adaptationServiceApprovalStage(*session) != domain.AdaptationApprovalProse {
		return nil, fmt.Errorf("adaptation prose rework intent stage is not active")
	}
	policy, _, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	accepted, err := s.acceptedAdaptationPlan(session, policy)
	if err != nil {
		return nil, err
	}
	locations := adaptationChapterLocations(accepted)
	queue := domain.AdaptationProseReworkQueue{}
	artifacts := make([]storepkg.CandidateArtifactInput, 0)
	for _, impact := range session.Impact.Items {
		if impact.ArtifactKind != domain.StructureKindChapter || impact.Requirement != domain.StructureImpactRequired || !impact.RequiresBodyRewrite {
			continue
		}
		location, ok := locations[impact.ArtifactID]
		if !ok {
			return nil, fmt.Errorf("adaptation prose rework target %q is absent from accepted structure", impact.ArtifactID)
		}
		intent := domain.AdaptationProseReworkIntent{ChapterID: impact.ArtifactID, CurrentNumber: location.chapter.Chapter, VolumeID: location.volumeID, ArcID: location.arcID, Reason: strings.Join(impact.DependencyEvidence, "; ")}
		payload, _ := json.Marshal(intent)
		artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: "rework:" + impact.ArtifactID, ArtifactKind: domain.AdaptationRevisionArtifactProseReworkIntent, Payload: payload})
		queue.ChapterIDs = append(queue.ChapterIDs, impact.ArtifactID)
	}
	if len(queue.ChapterIDs) == 0 {
		return nil, fmt.Errorf("adaptation prose stage has no exact rework targets")
	}
	queuePayload, _ := json.Marshal(queue)
	artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: domain.AdaptationRevisionProseQueueID, ArtifactKind: domain.AdaptationRevisionArtifactProseReworkQueue, Payload: queuePayload})
	return s.revisionStore().SubmitCandidate(policy, storepkg.SubmitRevisionCandidateInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey, Artifacts: artifacts})
}

func (s *AdaptationRevisionService) publish(preview AdaptationStructureRevisionPreview, session *domain.RevisionSession, idempotencyKey string) (*domain.RevisionSession, error) {
	if session == nil {
		return nil, fmt.Errorf("adaptation revision session is required")
	}
	policy, runtime, err := s.boundPolicy(session.ID)
	if err != nil {
		return nil, err
	}
	if preview.Signature != session.PreviewSignature || adaptationPreviewSignature(preview) != preview.Signature || preview.Signature != runtime.PreviewSignature {
		return nil, fmt.Errorf("adaptation revision publish preview substitution is not allowed")
	}
	publishInput := storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: idempotencyKey}
	if replayed, found, replayErr := s.revisionStore().FinalizeCommittedPublication(publishInput); found || replayErr != nil {
		if replayErr != nil {
			return replayed, replayErr
		}
		if err := s.removeRevisionRuntime(session.ID); err != nil {
			return nil, err
		}
		return replayed, nil
	}
	versions, err := s.revisionStore().ValidatePublish(policy, publishInput)
	if err != nil {
		return nil, err
	}
	candidate, err := adaptationPlanFromVersions(policy, versions)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateAdaptationRevisionPlan(*policy.BasePlan, candidate, policy.SourceManifest); err != nil {
		return nil, fmt.Errorf("accepted adaptation snapshot is not publishable: %w", err)
	}
	formalSnapshot, err := s.store.CaptureAdaptationFormalSnapshot()
	if err != nil {
		return nil, err
	}
	if err := s.store.SaveAdaptationPlanForRevision(s.revisionStore(), candidate, session.ID); err != nil {
		return nil, err
	}
	if err := s.applyPublishedProgress(*policy.BasePlan, candidate, session.Impact, session.ID); err != nil {
		return nil, s.rollbackAdaptationPublish(policy, session.ID, formalSnapshot, err)
	}
	if err := s.store.ClearAdaptationRevisionAudits(s.revisionStore(), session.ID); err != nil {
		return nil, s.rollbackAdaptationPublish(policy, session.ID, formalSnapshot, err)
	}
	if s.beforeRevisionCommit != nil {
		s.beforeRevisionCommit()
	}
	if err := s.removeRevisionRuntime(session.ID); err != nil {
		return nil, s.rollbackAdaptationPublish(policy, session.ID, formalSnapshot, err)
	}
	published, publishErr := s.revisionStore().Publish(policy, publishInput)
	if publishErr != nil {
		if published != nil && published.Stage == domain.RevisionStageCompleted {
			return published, &committedAdaptationPublicationError{cause: s.restoreRevisionRuntime(*runtime, publishErr)}
		}
		rollbackErr := s.rollbackAdaptationPublish(policy, session.ID, formalSnapshot, publishErr)
		return nil, s.restoreRevisionRuntime(*runtime, rollbackErr)
	}
	return published, nil
}

func (s *AdaptationRevisionService) rollbackAdaptationPublish(policy domain.AdaptationRevisionPolicy, sessionID string, snapshot *storepkg.AdaptationFormalSnapshot, cause error) error {
	if err := s.store.RestoreAdaptationPlanForRevision(s.revisionStore(), *policy.BasePlan, sessionID); err != nil {
		return fmt.Errorf("publish adaptation revision: %v; restore formal plan: %w", cause, err)
	}
	if err := s.store.RestoreAdaptationFormalSnapshot(s.revisionStore(), snapshot, sessionID); err != nil {
		return fmt.Errorf("publish adaptation revision: %v; restore exact proposal/workflow/progress/audits: %w", cause, err)
	}
	return cause
}

func (s *AdaptationRevisionService) applyPublishedProgress(previous, candidate domain.AdaptationPlan, impact domain.RevisionImpact, sessionID string) error {
	progress, err := s.store.Progress.Load()
	if err != nil || progress == nil {
		return err
	}
	wasComplete := progress.Phase == domain.PhaseComplete
	if wasComplete {
		payload, _ := json.Marshal(candidate)
		progress.CompletionRevalidation = storepkg.NewAdaptationCompletionRevalidationCheckpoint(sessionID, domain.JSONContentSignature(payload), adaptationPlanStructureForDramaticBindings(previous), adaptationPlanStructureForDramaticBindings(candidate))
		progress.Phase = domain.PhaseWriting
		progress.Flow = domain.FlowReviewing
		progress.ReopenedFromComplete = true
		progress.CompletionAuditStatus = ""
		progress.CompletionAuditReportDigest = ""
	}
	progress.TotalChapters = len(candidate.Chapters)
	rewrites := make([]int, 0)
	locations := adaptationChapterLocations(candidate)
	for _, item := range impact.Items {
		if item.RequiresBodyRewrite {
			rewrites = append(rewrites, locations[item.ArtifactID].chapter.Chapter)
		}
	}
	slices.Sort(rewrites)
	rewrites = slices.Compact(rewrites)
	if len(rewrites) > 0 {
		progress.PendingRewrites = mergeAdaptationPendingRewrites(progress.PendingRewrites, rewrites)
		if progress.RewriteReason == "" {
			progress.RewriteReason = "approved adaptation revision"
		} else if !strings.Contains(progress.RewriteReason, "approved adaptation revision") {
			progress.RewriteReason += "; approved adaptation revision"
		}
		progress.Flow = domain.FlowRewriting
		// A completed publication already entered the review gate above.
	}
	if wasComplete && len(candidate.Chapters) > len(progress.CompletedChapters) {
		if len(rewrites) == 0 {
			progress.Flow = domain.FlowWriting
		}
	}
	return s.store.SaveAdaptationRevisionProgress(s.revisionStore(), progress, sessionID)
}

func mergeAdaptationPendingRewrites(existing, additional []int) []int {
	seen := make(map[int]struct{}, len(existing)+len(additional))
	merged := make([]int, 0, len(existing)+len(additional))
	for _, chapter := range append(append([]int(nil), existing...), additional...) {
		if chapter <= 0 {
			continue
		}
		if _, duplicate := seen[chapter]; duplicate {
			continue
		}
		seen[chapter] = struct{}{}
		merged = append(merged, chapter)
	}
	return merged
}

func (s *AdaptationRevisionService) boundPolicy(sessionID string) (domain.AdaptationRevisionPolicy, *domain.AdaptationRevisionRuntime, error) {
	base, manifest, stage, completed, err := s.loadProductionContract()
	if err != nil {
		return domain.AdaptationRevisionPolicy{}, nil, err
	}
	runtime, err := s.store.Adaptation.LoadRevisionRuntime()
	if err != nil {
		return domain.AdaptationRevisionPolicy{}, nil, err
	}
	if runtime == nil || runtime.SessionID != strings.TrimSpace(sessionID) || runtime.Stage != stage || runtime.BasePlanSignature != adaptationPlanSignature(*base) || runtime.SourceManifestSignature != domain.AdaptationSourceManifestContractSignature(*manifest) {
		return domain.AdaptationRevisionPolicy{}, nil, fmt.Errorf("adaptation revision restart binding no longer matches formal plan/source manifest/stage")
	}
	active, err := s.revisionStore().Active()
	if err != nil {
		return domain.AdaptationRevisionPolicy{}, nil, err
	}
	if active == nil || active.ID != strings.TrimSpace(sessionID) || active.Mode != domain.RevisionModeAdaptation {
		return domain.AdaptationRevisionPolicy{}, nil, fmt.Errorf("adaptation revision runtime has no matching active session")
	}
	interrupted := active.Stage == domain.RevisionStagePaused || active.Stage == domain.RevisionStageFailed
	if runtime.Paused != interrupted {
		runtime.Paused = interrupted
		if err := s.persistRevisionRuntime(*runtime); err != nil {
			return domain.AdaptationRevisionPolicy{}, nil, fmt.Errorf("reconcile adaptation revision interruption checkpoint: %w", err)
		}
	}
	policy := domain.NewAdaptationRevisionPolicy(stage, base, manifest)
	policy.CompletedTarget = completed
	return policy, runtime, nil
}

func (s *AdaptationRevisionService) loadProductionContract() (*domain.AdaptationPlan, *domain.AdaptationSourceManifest, domain.ManuscriptStage, []string, error) {
	if s == nil || s.store == nil {
		return nil, nil, "", nil, fmt.Errorf("adaptation revision service requires a formal adaptation project")
	}
	manifest, err := s.store.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return nil, nil, "", nil, fmt.Errorf("load immutable adaptation source manifest: %w", err)
	}
	plan, err := s.loadStagePlan(*manifest)
	if err != nil {
		return nil, nil, "", nil, err
	}
	stage, err := s.CurrentManuscriptStage()
	if err != nil {
		return nil, nil, "", nil, err
	}
	completed, err := s.completedStableTargetIDs(*plan)
	if err != nil {
		return nil, nil, "", nil, err
	}
	return plan, manifest, stage, completed, nil
}

// loadStagePlan resolves the server-owned adaptation contract at every real
// planning checkpoint. A confirmed plan is authoritative once present; before
// confirmation, the detailed proposal or high-level volume review is the
// corresponding durable contract. The volume-review conversion derives the
// source event ledger exclusively from persisted source reports, never from a
// revision request.
func (s *AdaptationRevisionService) loadStagePlan(manifest domain.AdaptationSourceManifest) (*domain.AdaptationPlan, error) {
	plan, err := s.store.Adaptation.LoadPlan()
	if err != nil {
		return nil, fmt.Errorf("load formal adaptation plan: %w", err)
	}
	if plan != nil {
		return plan, nil
	}
	proposal, err := s.store.Adaptation.LoadProposal()
	if err != nil {
		return nil, fmt.Errorf("load formal adaptation proposal: %w", err)
	}
	if proposal != nil {
		return proposal, nil
	}
	review, err := s.store.Adaptation.LoadVolumeReview()
	if err != nil {
		return nil, fmt.Errorf("load formal adaptation volume review: %w", err)
	}
	if review == nil {
		return nil, fmt.Errorf("formal adaptation plan, proposal, or volume review is required")
	}
	reports, err := s.store.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return nil, fmt.Errorf("load immutable adaptation source reports: %w", err)
	}
	if len(reports) != manifest.ChapterCount {
		return nil, fmt.Errorf("complete immutable adaptation source reports are required at proposal-complete stage")
	}
	return adaptationPlanFromVolumeReview(*review, manifest, reports)
}

func adaptationPlanFromVolumeReview(review domain.AdaptationVolumeReview, manifest domain.AdaptationSourceManifest, reports []domain.AdaptationSourceReport) (*domain.AdaptationPlan, error) {
	return domain.AdaptationPlanFromVolumeReview(review, manifest, reports)
}

func (s *AdaptationRevisionService) completedStableTargetIDs(plan domain.AdaptationPlan) ([]string, error) {
	progress, err := s.store.Progress.Load()
	if err != nil || progress == nil {
		return nil, err
	}
	writtenNumbers := make(map[int]struct{}, len(progress.CompletedChapters))
	for _, number := range progress.CompletedChapters {
		writtenNumbers[number] = struct{}{}
	}
	for _, chapter := range plan.Chapters {
		finalBody, loadErr := s.store.Drafts.LoadChapterText(chapter.Chapter)
		if loadErr != nil {
			return nil, loadErr
		}
		draftBody, loadErr := s.store.Drafts.LoadDraft(chapter.Chapter)
		if loadErr != nil {
			return nil, loadErr
		}
		if strings.TrimSpace(finalBody) != "" || strings.TrimSpace(draftBody) != "" {
			writtenNumbers[chapter.Chapter] = struct{}{}
		}
	}
	ids := make([]string, 0, len(writtenNumbers))
	for _, chapter := range plan.Chapters {
		if _, ok := writtenNumbers[chapter.Chapter]; ok {
			ids = append(ids, chapter.ID)
		}
	}
	return ids, nil
}

type adaptationChapterLocation struct {
	volumeID string
	arcID    string
	chapter  domain.AdaptationChapterPlan
}

func adaptationChapterLocations(plan domain.AdaptationPlan) map[string]adaptationChapterLocation {
	locations := make(map[string]adaptationChapterLocation, len(plan.Chapters))
	for _, chapter := range plan.Chapters {
		volumeID := "unassigned-volume"
		for _, volume := range plan.Volumes {
			if volume.TargetFrom <= chapter.Chapter && chapter.Chapter <= volume.TargetTo {
				volumeID = volume.ID
				break
			}
		}
		locations[chapter.ID] = adaptationChapterLocation{volumeID: volumeID, arcID: volumeID + ":revision-arc", chapter: chapter}
	}
	return locations
}

func deriveAdaptationRevisionImpact(base, candidate domain.AdaptationPlan, completed []string, requireAddedProse bool) (domain.RevisionImpact, error) {
	items := make([]domain.RevisionImpactItem, 0)
	baseLocations, candidateLocations := adaptationChapterLocations(base), adaptationChapterLocations(candidate)
	completedSet := make(map[string]struct{}, len(completed))
	for _, id := range completed {
		completedSet[id] = struct{}{}
	}
	baseVolumes := make(map[string]domain.AdaptationVolumePlan, len(base.Volumes))
	for _, volume := range base.Volumes {
		baseVolumes[volume.ID] = volume
	}
	for _, volume := range candidate.Volumes {
		prior, existed := baseVolumes[volume.ID]
		if existed && adaptationJSONEqual(prior, volume) {
			continue
		}
		change := "revise server-derived adaptation volume topology"
		if !existed {
			change = "append server-derived adaptation volume"
		}
		items = append(items,
			domain.RevisionImpactItem{ArtifactID: volume.ID, ArtifactKind: domain.StructureKindVolume, Change: change, Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange, DependencyEvidence: []string{"formal adaptation volume topology differs"}},
			domain.RevisionImpactItem{ArtifactID: volume.ID + ":revision-arc", ArtifactKind: domain.StructureKindArc, Change: "re-audit server-derived volume arc", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactStructureChange, DependencyEvidence: []string{"arc scope derives from formal volume range"}},
		)
	}
	baseByID := make(map[string]domain.AdaptationChapterPlan, len(base.Chapters))
	for _, chapter := range base.Chapters {
		baseByID[chapter.ID] = chapter
	}
	for _, chapter := range candidate.Chapters {
		prior, existed := baseByID[chapter.ID]
		contentChanged := !existed || adaptationChapterContentSignature(prior) != adaptationChapterContentSignature(chapter)
		topologyChanged := !existed || baseLocations[chapter.ID].volumeID != candidateLocations[chapter.ID].volumeID || prior.Chapter != chapter.Chapter
		if !contentChanged && !topologyChanged {
			continue
		}
		item := domain.RevisionImpactItem{ArtifactID: chapter.ID, ArtifactKind: domain.StructureKindChapter, Requirement: domain.StructureImpactRequired, DependencyEvidence: []string{"server-derived target topology and source lineage"}, DependencySourceIDs: adaptationChapterSourceEventIDs(chapter)}
		switch {
		case !existed:
			item.Change, item.Cause = "insert adaptation target chapter", domain.StructureImpactStructureChange
		case contentChanged:
			item.Change, item.Cause = "revise exact adaptation chapter outline", domain.StructureImpactContentDependency
		default:
			item.Change, item.Cause, item.Requirement = "display renumber only", domain.StructureImpactDisplayRenumber, domain.StructureImpactRecommended
			item.DependencyEvidence = nil
		}
		_, item.RequiresBodyRewrite = completedSet[chapter.ID]
		// A newly inserted target chapter has no formal prose yet, but it still
		// requires the same audited prose candidate stage as a written chapter
		// rework. Existing unwritten outline-only changes remain outline scoped.
		item.RequiresBodyRewrite = (!existed && requireAddedProse) || (item.RequiresBodyRewrite && contentChanged)
		items = append(items, item)
	}
	items = append(items,
		domain.RevisionImpactItem{ArtifactID: domain.AdaptationRevisionBatchPlanID, ArtifactKind: domain.AdaptationRevisionArtifactBatchPlan, Change: "persist server-derived bounded generation and review", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"batch topology and context derive from affected stable IDs"}},
		domain.RevisionImpactItem{ArtifactID: domain.AdaptationRevisionPlanSnapshotID, ArtifactKind: domain.AdaptationRevisionArtifactPlanSnapshot, Change: "bind accepted adaptation structure and immutable source manifest", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"formal plan and source manifest signatures"}},
	)
	for _, item := range append([]domain.RevisionImpactItem(nil), items...) {
		if item.RequiresBodyRewrite {
			items = append(items, domain.RevisionImpactItem{ArtifactID: "rework:" + item.ArtifactID, ArtifactKind: domain.AdaptationRevisionArtifactProseReworkIntent, Change: "queue exact written adaptation rework intent", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: append([]string(nil), item.DependencyEvidence...)})
		}
	}
	if slices.ContainsFunc(items, func(item domain.RevisionImpactItem) bool {
		return item.ArtifactKind == domain.AdaptationRevisionArtifactProseReworkIntent
	}) {
		items = append(items, domain.RevisionImpactItem{ArtifactID: domain.AdaptationRevisionProseQueueID, ArtifactKind: domain.AdaptationRevisionArtifactProseReworkQueue, Change: "persist exact adaptation prose rework queue", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"queue is derived from completed stable-ID outline changes"}})
	}
	if len(adaptationRequiredChapterIDsFromItems(items)) == 0 {
		return domain.RevisionImpact{}, fmt.Errorf("adaptation revision has no exact affected target chapter")
	}
	return domain.NewRevisionImpact("server-derived adaptation revision impact", items)
}

func deriveAdaptationRevisionBatchPlan(candidate domain.AdaptationPlan, impact domain.RevisionImpact, manifest *domain.AdaptationSourceManifest) (domain.BatchPlan, error) {
	if manifest == nil {
		return domain.BatchPlan{}, fmt.Errorf("adaptation revision BatchPlan requires the immutable source manifest")
	}
	want := adaptationRequiredChapterIDs(impact)
	locations := adaptationChapterLocations(candidate)
	byVolume := make(map[string][]string)
	volumeOrder := make([]string, 0)
	for _, chapter := range candidate.Chapters {
		if !want[chapter.ID] {
			continue
		}
		volumeID := locations[chapter.ID].volumeID
		if _, exists := byVolume[volumeID]; !exists {
			volumeOrder = append(volumeOrder, volumeID)
		}
		byVolume[volumeID] = append(byVolume[volumeID], chapter.ID)
	}
	plan := domain.BatchPlan{WholeBookReview: domain.BatchAggregateReview{ScopeID: "whole-book", Status: domain.BatchReviewPending}}
	batchIndex := 0
	for _, volumeID := range volumeOrder {
		plan.VolumeReviews = append(plan.VolumeReviews, domain.BatchAggregateReview{ScopeID: volumeID, Status: domain.BatchReviewPending})
		ids := byVolume[volumeID]
		for offset := 0; offset < len(ids); {
			chunkSize := min(4, len(ids)-offset)
			initialChunkSize := chunkSize
			var chapterIDs []string
			var context []domain.BatchContextItem
			var units int
			var contextErr error
			for {
				chapterIDs = append([]string(nil), ids[offset:offset+chunkSize]...)
				context, units, contextErr = adaptationBatchContext(candidate, chapterIDs, *manifest)
				if contextErr != nil {
					return domain.BatchPlan{}, contextErr
				}
				if units <= domain.AdaptationRevisionBatchContextMaxUnits {
					break
				}
				if chunkSize == 1 {
					return domain.BatchPlan{}, fmt.Errorf("adaptation target chapter %q requires %d immutable-source context units; ceiling is %d", chapterIDs[0], units, domain.AdaptationRevisionBatchContextMaxUnits)
				}
				chunkSize--
			}
			batchIndex++
			plan.Batches = append(plan.Batches, domain.BatchWork{ID: fmt.Sprintf("adaptation-batch-%03d", batchIndex), Index: batchIndex, ChapterIDs: chapterIDs, VolumeID: volumeID, ArcID: volumeID + ":revision-arc", EstimatedOutputWords: adaptationBatchOutputWords(candidate, chapterIDs), ContextUnits: units, Context: context, Constrained: chunkSize < initialChunkSize || chunkSize <= 2, Status: domain.BatchStatusPending})
			offset += chunkSize
		}
	}
	if err := domain.ValidateAdaptationRevisionBatchPlan(plan); err != nil {
		return domain.BatchPlan{}, err
	}
	return plan, nil
}

func adaptationBatchContext(plan domain.AdaptationPlan, chapterIDs []string, manifest domain.AdaptationSourceManifest) ([]domain.BatchContextItem, int, error) {
	manifestRunes := make(map[int]int, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		if source.Chapter <= 0 || source.Runes <= 0 {
			return nil, 0, fmt.Errorf("immutable source manifest contains invalid chapter metadata")
		}
		manifestRunes[source.Chapter] = source.Runes
	}
	seen := make(map[string]struct{})
	context := make([]domain.BatchContextItem, 0)
	for _, id := range chapterIDs {
		chapter := adaptationChapterByStableID(plan, id)
		segmentedSources := make(map[int]struct{}, len(chapter.SourceSegments))
		for _, segment := range chapter.SourceSegments {
			sourceRunes, exists := manifestRunes[segment.SourceChapter]
			if !exists || segment.RuneShare.Start < 0 || segment.RuneShare.End > sourceRunes || segment.RuneShare.Runes() <= 0 {
				return nil, 0, fmt.Errorf("target chapter %q has a source segment outside immutable manifest bounds", id)
			}
			segmentedSources[segment.SourceChapter] = struct{}{}
			key := fmt.Sprintf("source:%d:%d-%d", segment.SourceChapter, segment.RuneShare.Start, segment.RuneShare.End)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			context = append(context, domain.BatchContextItem{ID: key, Kind: domain.BatchContextSourceAnchor, Units: segment.RuneShare.Runes(), Necessary: true})
		}
		sources := append([]int(nil), chapter.SourceChapters...)
		for source := chapter.SourceRange.From; source > 0 && source <= chapter.SourceRange.To; source++ {
			sources = append(sources, source)
		}
		sort.Ints(sources)
		for _, source := range slices.Compact(sources) {
			if _, segmented := segmentedSources[source]; segmented {
				continue
			}
			sourceRunes, exists := manifestRunes[source]
			if !exists {
				return nil, 0, fmt.Errorf("target chapter %q references unknown immutable source chapter %d", id, source)
			}
			key := fmt.Sprintf("source:%d", source)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			context = append(context, domain.BatchContextItem{ID: key, Kind: domain.BatchContextSourceAnchor, Units: sourceRunes, Necessary: true})
		}
		for _, eventID := range append(append([]string(nil), chapter.EventIDs...), chapter.AddedEventIDs...) {
			key := "event:" + eventID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			context = append(context, domain.BatchContextItem{ID: key, Kind: domain.BatchContextFact, Units: 1, Necessary: true})
		}
	}
	units := 0
	for _, item := range context {
		units += item.Units
	}
	return context, units, nil
}

func adaptationBatchOutputWords(plan domain.AdaptationPlan, chapterIDs []string) int {
	total := 0
	for _, id := range chapterIDs {
		total += adaptationChapterByStableID(plan, id).TargetRunes
	}
	return max(total, len(chapterIDs))
}

func adaptationStructureSkeleton(base, candidate domain.AdaptationPlan, impact domain.RevisionImpact) domain.AdaptationPlan {
	result, _ := cloneAdaptationPlan(candidate)
	for index := range result.Chapters {
		if !adaptationRequiredChapterIDs(impact)[result.Chapters[index].ID] {
			continue
		}
		prior, existed := adaptationChapterByID(base, result.Chapters[index].ID)
		if existed && adaptationChapterContentSignature(prior) == adaptationChapterContentSignature(result.Chapters[index]) {
			continue
		}
		result.Chapters[index].OutlineEntry.Title = ""
		result.Chapters[index].OutlineEntry.CoreEvent = ""
		result.Chapters[index].OutlineEntry.Hook = ""
		result.Chapters[index].OutlineEntry.Scenes = nil
		result.Chapters[index].Title = ""
	}
	return result
}

func adaptationStructureArtifacts(preview AdaptationStructureRevisionPreview, batchPlan domain.BatchPlan) ([]storepkg.CandidateArtifactInput, error) {
	artifacts := make([]storepkg.CandidateArtifactInput, 0)
	for _, item := range preview.Impact.Items {
		if item.Requirement != domain.StructureImpactRequired {
			continue
		}
		switch item.ArtifactKind {
		case domain.StructureKindVolume:
			volume, ok := adaptationVolumeByID(preview.Candidate, item.ArtifactID)
			if !ok {
				return nil, fmt.Errorf("server-derived volume %q is absent from preview", item.ArtifactID)
			}
			payload, _ := json.Marshal(volume)
			artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: item.ArtifactID, ArtifactKind: item.ArtifactKind, Payload: payload})
		case domain.StructureKindArc:
			volumeID := strings.TrimSuffix(item.ArtifactID, ":revision-arc")
			volume, ok := adaptationVolumeByID(preview.Candidate, volumeID)
			if !ok {
				return nil, fmt.Errorf("server-derived arc %q has no volume", item.ArtifactID)
			}
			arc := domain.AdaptationRevisionArcCandidate{ID: item.ArtifactID, VolumeID: volume.ID, TargetFrom: volume.TargetFrom, TargetTo: volume.TargetTo, SourceChapters: adaptationVolumeSourceChapters(preview.Candidate, volume), MainlineEventIDs: append([]string(nil), volume.MainlineEventIDs...)}
			payload, _ := json.Marshal(arc)
			artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: item.ArtifactID, ArtifactKind: item.ArtifactKind, Payload: payload})
		}
	}
	planPayload, _ := json.Marshal(domain.AdaptationPlanRevisionCandidate{Stage: preview.Stage, SourceSignature: preview.SourceManifestSignature, Plan: preview.Candidate})
	batchPayload, _ := json.Marshal(batchPlan)
	artifacts = append(artifacts,
		storepkg.CandidateArtifactInput{ArtifactID: domain.AdaptationRevisionPlanSnapshotID, ArtifactKind: domain.AdaptationRevisionArtifactPlanSnapshot, Payload: planPayload},
		storepkg.CandidateArtifactInput{ArtifactID: domain.AdaptationRevisionBatchPlanID, ArtifactKind: domain.AdaptationRevisionArtifactBatchPlan, Payload: batchPayload},
	)
	return artifacts, nil
}

func adaptationDetailArtifacts(candidate, accepted domain.AdaptationPlan, runtime domain.AdaptationRevisionRuntime, required map[string]bool) ([]storepkg.CandidateArtifactInput, error) {
	artifacts := make([]storepkg.CandidateArtifactInput, 0, len(required)+2)
	locations := adaptationChapterLocations(candidate)
	for _, chapter := range candidate.Chapters {
		if !required[chapter.ID] {
			continue
		}
		location := locations[chapter.ID]
		detail := domain.AdaptationDetailedOutlineCandidate{ChapterID: chapter.ID, CurrentNumber: chapter.Chapter, VolumeID: location.volumeID, ArcID: location.arcID, Outline: chapter}
		payload, _ := json.Marshal(detail)
		artifacts = append(artifacts, storepkg.CandidateArtifactInput{ArtifactID: chapter.ID, ArtifactKind: domain.StructureKindChapter, Payload: payload})
	}
	planPayload, _ := json.Marshal(domain.AdaptationPlanRevisionCandidate{Stage: runtime.Stage, SourceSignature: runtime.SourceManifestSignature, Plan: accepted})
	batchPayload, _ := json.Marshal(runtime.BatchPlan)
	artifacts = append(artifacts,
		storepkg.CandidateArtifactInput{ArtifactID: domain.AdaptationRevisionPlanSnapshotID, ArtifactKind: domain.AdaptationRevisionArtifactPlanSnapshot, Payload: planPayload},
		storepkg.CandidateArtifactInput{ArtifactID: domain.AdaptationRevisionBatchPlanID, ArtifactKind: domain.AdaptationRevisionArtifactBatchPlan, Payload: batchPayload},
	)
	return artifacts, nil
}

func (s *AdaptationRevisionService) acceptedAdaptationStructure(session *domain.RevisionSession, policy domain.AdaptationRevisionPolicy) (domain.AdaptationPlan, error) {
	if !slices.ContainsFunc(session.ApprovalStages[:min(len(session.Approvals), len(session.ApprovalStages))], func(stage domain.RevisionApprovalStage) bool { return stage.ID == domain.AdaptationApprovalStructure }) {
		return cloneAdaptationPlan(*policy.BasePlan)
	}
	for index := len(session.AcceptedVersionIDs) - 1; index >= 0; index-- {
		version, err := s.revisionStore().LoadVersion(session.AcceptedVersionIDs[index])
		if err != nil {
			return domain.AdaptationPlan{}, err
		}
		if version.ArtifactKind != domain.AdaptationRevisionArtifactPlanSnapshot {
			continue
		}
		var envelope domain.AdaptationPlanRevisionCandidate
		if err := json.Unmarshal(version.Payload, &envelope); err != nil {
			return domain.AdaptationPlan{}, err
		}
		return envelope.Plan, nil
	}
	return domain.AdaptationPlan{}, fmt.Errorf("accepted adaptation structure snapshot is missing")
}

func (s *AdaptationRevisionService) acceptedAdaptationPlan(session *domain.RevisionSession, policy domain.AdaptationRevisionPolicy) (domain.AdaptationPlan, error) {
	versions := make([]domain.ArtifactVersion, 0, len(session.AcceptedVersionIDs))
	for _, id := range session.AcceptedVersionIDs {
		version, err := s.revisionStore().LoadVersion(id)
		if err != nil {
			return domain.AdaptationPlan{}, err
		}
		versions = append(versions, *version)
	}
	return adaptationPlanFromVersions(policy, versions)
}

func adaptationPlanFromVersions(policy domain.AdaptationRevisionPolicy, versions []domain.ArtifactVersion) (domain.AdaptationPlan, error) {
	plan := *policy.BasePlan
	details := make(map[string]domain.AdaptationChapterPlan)
	for _, version := range versions {
		switch version.ArtifactKind {
		case domain.AdaptationRevisionArtifactPlanSnapshot:
			var envelope domain.AdaptationPlanRevisionCandidate
			if err := json.Unmarshal(version.Payload, &envelope); err != nil {
				return domain.AdaptationPlan{}, err
			}
			plan = envelope.Plan
		case domain.StructureKindChapter:
			var detail domain.AdaptationDetailedOutlineCandidate
			if err := json.Unmarshal(version.Payload, &detail); err != nil {
				return domain.AdaptationPlan{}, err
			}
			details[detail.ChapterID] = detail.Outline
		}
	}
	for index := range plan.Chapters {
		if detail, ok := details[plan.Chapters[index].ID]; ok {
			if adaptationChapterStructureSignature(plan.Chapters[index]) != adaptationChapterStructureSignature(detail) {
				return domain.AdaptationPlan{}, fmt.Errorf("adaptation detailed outline %q changes sealed structure/source ownership", detail.ID)
			}
			plan.Chapters[index] = overlayAdaptationChapterNarrative(plan.Chapters[index], detail)
		}
	}
	return plan, nil
}

func validateAdaptationDetailIsolation(accepted, candidate domain.AdaptationPlan, required map[string]bool) error {
	for _, chapter := range candidate.Chapters {
		if required[chapter.ID] {
			if strings.TrimSpace(chapter.Title) == "" || strings.TrimSpace(chapter.CoreEvent) == "" || strings.TrimSpace(chapter.Hook) == "" || len(chapter.Scenes) == 0 {
				return fmt.Errorf("adaptation detailed outline %q is incomplete", chapter.ID)
			}
			continue
		}
		authoritative, exists := adaptationChapterByID(accepted, chapter.ID)
		if !exists || !adaptationJSONEqual(authoritative, chapter) {
			return fmt.Errorf("adaptation detailed outline changes non-impacted target %q", chapter.ID)
		}
	}
	return nil
}

func validateWrittenAdaptationTopology(base, candidate domain.AdaptationPlan, completed []string) error {
	baseLocations, candidateLocations := adaptationChapterLocations(base), adaptationChapterLocations(candidate)
	for _, id := range completed {
		baseLocation, baseExists := baseLocations[id]
		candidateLocation, candidateExists := candidateLocations[id]
		if !baseExists || !candidateExists || baseLocation.chapter.Chapter != candidateLocation.chapter.Chapter || baseLocation.volumeID != candidateLocation.volumeID {
			return fmt.Errorf("written adaptation target %q cannot be moved or renumbered", id)
		}
	}
	return nil
}

func adaptationBatchPlanCompleted(plan domain.BatchPlan) bool {
	for _, batch := range plan.Batches {
		if batch.Status != domain.BatchStatusCompleted {
			return false
		}
	}
	for _, review := range plan.VolumeReviews {
		if review.Status != domain.BatchReviewCompleted {
			return false
		}
	}
	return plan.WholeBookReview.Status == domain.BatchReviewCompleted
}

func adaptationServiceApprovalStage(session domain.RevisionSession) string {
	if len(session.Approvals) >= len(session.ApprovalStages) {
		return ""
	}
	return session.ApprovalStages[len(session.Approvals)].ID
}

func adaptationRequiredChapterIDs(impact domain.RevisionImpact) map[string]bool {
	result := make(map[string]bool)
	for _, item := range impact.Items {
		if item.ArtifactKind == domain.StructureKindChapter && item.Requirement == domain.StructureImpactRequired {
			result[item.ArtifactID] = true
		}
	}
	return result
}

func adaptationRequiredChapterIDsFromItems(items []domain.RevisionImpactItem) map[string]bool {
	impact := domain.RevisionImpact{Items: items}
	return adaptationRequiredChapterIDs(impact)
}

func adaptationChapterSourceEventIDs(chapter domain.AdaptationChapterPlan) []string {
	ids := append(append([]string(nil), chapter.EventIDs...), chapter.AddedEventIDs...)
	for _, segment := range chapter.SourceSegments {
		ids = append(ids, segment.EventIDs...)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func adaptationPreviewSignature(preview AdaptationStructureRevisionPreview) string {
	copy := preview
	copy.Signature = ""
	payload, _ := json.Marshal(copy)
	return domain.JSONContentSignature(payload)
}

func adaptationPlanSignature(plan domain.AdaptationPlan) string {
	payload, _ := json.Marshal(plan)
	return domain.JSONContentSignature(payload)
}

func adaptationPlanStructureSignature(plan domain.AdaptationPlan) string {
	value, _ := cloneAdaptationPlan(plan)
	for index := range value.Chapters {
		clearAdaptationChapterNarrative(&value.Chapters[index])
	}
	payload, _ := json.Marshal(value)
	return domain.JSONContentSignature(payload)
}

func adaptationChapterStructureSignature(chapter domain.AdaptationChapterPlan) string {
	clearAdaptationChapterNarrative(&chapter)
	payload, _ := json.Marshal(chapter)
	return domain.JSONContentSignature(payload)
}

func clearAdaptationChapterNarrative(chapter *domain.AdaptationChapterPlan) {
	chapter.Title = ""
	chapter.OutlineEntry.Title = ""
	chapter.OutlineEntry.CoreEvent = ""
	chapter.OutlineEntry.Hook = ""
	chapter.OutlineEntry.Scenes = nil
}

func overlayAdaptationChapterNarrative(sealed, detail domain.AdaptationChapterPlan) domain.AdaptationChapterPlan {
	sealed.Title = detail.Title
	sealed.OutlineEntry.Title = detail.OutlineEntry.Title
	sealed.OutlineEntry.CoreEvent = detail.OutlineEntry.CoreEvent
	sealed.OutlineEntry.Hook = detail.OutlineEntry.Hook
	sealed.OutlineEntry.Scenes = append([]string(nil), detail.OutlineEntry.Scenes...)
	return sealed
}

func overlayAdaptationDetailedPlan(accepted, candidate domain.AdaptationPlan, required map[string]bool) (domain.AdaptationPlan, error) {
	result, err := cloneAdaptationPlan(accepted)
	if err != nil {
		return domain.AdaptationPlan{}, err
	}
	candidateByID := make(map[string]domain.AdaptationChapterPlan, len(candidate.Chapters))
	for _, chapter := range candidate.Chapters {
		candidateByID[chapter.ID] = chapter
	}
	for index := range result.Chapters {
		if !required[result.Chapters[index].ID] {
			continue
		}
		detail, exists := candidateByID[result.Chapters[index].ID]
		if !exists {
			return domain.AdaptationPlan{}, fmt.Errorf("adaptation detailed outline %q is missing", result.Chapters[index].ID)
		}
		result.Chapters[index] = overlayAdaptationChapterNarrative(result.Chapters[index], detail)
	}
	return result, nil
}

func adaptationChapterContentSignature(chapter domain.AdaptationChapterPlan) string {
	chapter.Chapter = 0
	chapter.OutlineEntry.Chapter = 0
	payload, _ := json.Marshal(chapter)
	return domain.JSONContentSignature(payload)
}

func adaptationJSONEqual(left, right any) bool {
	leftPayload, _ := json.Marshal(left)
	rightPayload, _ := json.Marshal(right)
	return string(leftPayload) == string(rightPayload)
}

func cloneAdaptationPlan(plan domain.AdaptationPlan) (domain.AdaptationPlan, error) {
	payload, err := json.Marshal(plan)
	if err != nil {
		return domain.AdaptationPlan{}, err
	}
	var cloned domain.AdaptationPlan
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return domain.AdaptationPlan{}, err
	}
	return cloned, nil
}

func adaptationChapterByID(plan domain.AdaptationPlan, id string) (domain.AdaptationChapterPlan, bool) {
	for _, chapter := range plan.Chapters {
		if chapter.ID == id {
			return chapter, true
		}
	}
	return domain.AdaptationChapterPlan{}, false
}

func adaptationChapterByStableID(plan domain.AdaptationPlan, id string) domain.AdaptationChapterPlan {
	chapter, _ := adaptationChapterByID(plan, id)
	return chapter
}

func adaptationVolumeByID(plan domain.AdaptationPlan, id string) (domain.AdaptationVolumePlan, bool) {
	for _, volume := range plan.Volumes {
		if volume.ID == id {
			return volume, true
		}
	}
	return domain.AdaptationVolumePlan{}, false
}

func adaptationVolumeSourceChapters(plan domain.AdaptationPlan, volume domain.AdaptationVolumePlan) []int {
	chapters := make([]int, 0)
	for _, target := range plan.Chapters {
		if target.Chapter < volume.TargetFrom || target.Chapter > volume.TargetTo {
			continue
		}
		chapters = append(chapters, target.SourceChapters...)
	}
	sort.Ints(chapters)
	return slices.Compact(chapters)
}
