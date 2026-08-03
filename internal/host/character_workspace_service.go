package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

const (
	CharacterWorkspaceErrorInvalid  = "character_workspace_invalid"
	CharacterWorkspaceErrorStale    = "character_workspace_stale"
	CharacterWorkspaceErrorBusy     = "character_workspace_busy"
	CharacterWorkspaceErrorConflict = "character_workspace_conflict"
	CharacterWorkspaceErrorNotFound = "character_workspace_not_found"
	CharacterWorkspaceErrorReadonly = "character_workspace_readonly"
	CharacterWorkspaceErrorRecovery = "character_workspace_recovery_failed"
	CharacterWorkspaceErrorAgent    = "character_agent_failed"
	characterInstructionLimitBytes  = 4 << 10
	characterCandidateLimitBytes    = 512 << 10
)

type CharacterWorkspaceError struct {
	Code string
	Err  error
}

func (e *CharacterWorkspaceError) Error() string {
	if e == nil || e.Err == nil {
		return "character workspace operation failed"
	}
	return e.Err.Error()
}

func (e *CharacterWorkspaceError) Unwrap() error { return e.Err }

type CharacterAnalysisScope struct {
	CharacterIDs []string `json:"character_ids,omitempty"`
}

type CharacterAnalyzeRequest struct {
	ExpectedBaseRevision       int64                   `json:"expected_base_revision"`
	ExpectedBaseAuditSignature string                  `json:"expected_base_audit_signature"`
	IdempotencyKey             string                  `json:"idempotency_key"`
	Scope                      CharacterAnalysisScope  `json:"scope"`
	Instruction                string                  `json:"instruction,omitempty"`
	AllowSupportingCharacters  bool                    `json:"allow_supporting_characters"`
	Candidate                  *domain.StoryFoundation `json:"candidate,omitempty"`
	CandidateRevision          int64                   `json:"candidate_revision,omitempty"`
	CandidateDigest            string                  `json:"candidate_digest"`
}

type CharacterReviewRequest struct {
	ExpectedBaseRevision       int64                           `json:"expected_base_revision"`
	ExpectedBaseAuditSignature string                          `json:"expected_base_audit_signature"`
	IdempotencyKey             string                          `json:"idempotency_key"`
	Candidate                  *domain.StoryFoundation         `json:"candidate,omitempty"`
	CandidateRevision          int64                           `json:"candidate_revision,omitempty"`
	CandidateDigest            string                          `json:"candidate_digest"`
	SourceMappings             []domain.CharacterSourceMapping `json:"source_mappings,omitempty"`
}

type CharacterRetryRequest struct {
	ExpectedBaseRevision       int64  `json:"expected_base_revision"`
	ExpectedBaseAuditSignature string `json:"expected_base_audit_signature"`
	RunID                      string `json:"run_id"`
	CandidateDigest            string `json:"candidate_digest"`
	IdempotencyKey             string `json:"idempotency_key"`
}

type CharacterDiscardRequest struct {
	ExpectedBaseRevision       int64  `json:"expected_base_revision"`
	ExpectedBaseAuditSignature string `json:"expected_base_audit_signature"`
	RunID                      string `json:"run_id,omitempty"`
	CandidateDigest            string `json:"candidate_digest"`
	IdempotencyKey             string `json:"idempotency_key"`
}

type CharacterCandidateState struct {
	Revision   int64                  `json:"revision"`
	Digest     string                 `json:"digest"`
	Foundation domain.StoryFoundation `json:"foundation"`
}

type CharacterWorkspaceState struct {
	Mode               string                                   `json:"mode"`
	BaseRevision       int64                                    `json:"base_revision"`
	BaseAuditSignature string                                   `json:"base_audit_signature"`
	CurrentDigest      string                                   `json:"current_digest"`
	Current            CharacterCandidateState                  `json:"current"`
	Candidate          *CharacterCandidateState                 `json:"candidate,omitempty"`
	Run                *domain.CharacterWorkspaceRun            `json:"run,omitempty"`
	Completeness       []domain.CharacterCardCompletenessResult `json:"completeness"`
	Coverage           *domain.AdaptationCharacterCoverage      `json:"source_coverage,omitempty"`
	SourceMappings     []domain.CharacterSourceMapping          `json:"source_mappings"`
	Findings           []domain.CharacterCardReviewFinding      `json:"findings"`
	Diff               *domain.FoundationDiff                   `json:"diff,omitempty"`
	AllowedOperations  []string                                 `json:"allowed_operations"`
	ConfirmationStatus domain.CharacterCardConfirmationStatus   `json:"confirmation_status,omitempty"`
	StaleReason        string                                   `json:"stale_reason,omitempty"`
	ReadonlyReason     string                                   `json:"readonly_reason,omitempty"`
	BusyReason         string                                   `json:"busy_reason,omitempty"`
	Error              *domain.CharacterCardError               `json:"error,omitempty"`
}

type CharacterAgentRuntime interface {
	ExecuteCharacterAgent(context.Context, agents.CharacterTask) error
	CharacterAgentModelRoute(domain.CharacterWorkspaceRunMode) string
}

type CharacterWorkspaceService struct {
	store     *storepkg.Store
	runtime   CharacterAgentRuntime
	prepareMu sync.Mutex
}

func NewCharacterWorkspaceService(
	st *storepkg.Store,
	runtime CharacterAgentRuntime,
) *CharacterWorkspaceService {
	return &CharacterWorkspaceService{store: st, runtime: runtime}
}

func (s *CharacterWorkspaceService) RecoverInterrupted() error {
	if s == nil || s.store == nil || s.store.CharacterWorkspace == nil {
		return fmt.Errorf("character workspace store is unavailable")
	}
	return s.store.CharacterWorkspace.RecoverInterrupted()
}

func (s *CharacterWorkspaceService) State(runID string) (*CharacterWorkspaceState, error) {
	current, binding, inputs, coreCast, mode, err := s.current()
	if err != nil {
		return nil, err
	}
	currentDigest := binding.Candidate.CharacterContentDigest
	state := &CharacterWorkspaceState{
		Mode:               string(mode),
		BaseRevision:       binding.Candidate.FoundationRevision,
		BaseAuditSignature: binding.Candidate.FoundationAuditSignature,
		CurrentDigest:      currentDigest,
		Current: CharacterCandidateState{
			Revision:   current.Revision,
			Digest:     currentDigest,
			Foundation: current,
		},
		Completeness:      []domain.CharacterCardCompletenessResult{},
		SourceMappings:    []domain.CharacterSourceMapping{},
		Findings:          []domain.CharacterCardReviewFinding{},
		AllowedOperations: []string{"analyze"},
	}
	state.Completeness, err = domain.EvaluateCharacterCardCompleteness(current, coreCast)
	if err != nil {
		return nil, fmt.Errorf("evaluate current character migration state: %w", err)
	}
	candidate, err := s.store.CharacterCards.LoadCandidate()
	if err != nil {
		return nil, err
	}
	if candidate != nil {
		candidateBinding, bindErr := domain.CharacterCardBindingFromFoundation(candidate.Foundation, inputs)
		if bindErr != nil {
			return nil, bindErr
		}
		state.Candidate = &CharacterCandidateState{
			Revision:   candidate.Revision,
			Digest:     candidateBinding.Candidate.CharacterContentDigest,
			Foundation: candidate.Foundation,
		}
		diff, diffErr := domain.ComputeFoundationDiff(current, candidate.Foundation, coreCast)
		if diffErr == nil {
			state.Diff = &diff
		}
		lifecycle, lifecycleErr := s.store.CharacterCards.Load(candidateBinding)
		if lifecycleErr != nil {
			return nil, lifecycleErr
		}
		if lifecycle != nil {
			state.Completeness = append(state.Completeness, lifecycle.Completeness...)
			state.Coverage = lifecycle.Coverage
			state.SourceMappings = append(state.SourceMappings, lifecycle.SourceMappings...)
			state.Findings = append(state.Findings, lifecycle.Findings...)
			state.Error = cloneCharacterError(lifecycle.Error)
			state.ConfirmationStatus = lifecycle.ConfirmationStatus
			if lifecycle.AnalysisStatus == domain.CharacterCardAnalysisStale ||
				lifecycle.ReviewStatus == domain.CharacterCardReviewStale ||
				candidate.Base.Candidate != binding.Candidate ||
				candidate.Base.InputDigest != binding.InputDigest {
				state.StaleReason = "candidate or review is stale for the current Foundation inputs"
			}
			if currentCharacterReviewPassed(*lifecycle, candidateBinding) {
				state.AllowedOperations = append(state.AllowedOperations, "preview")
				if lifecycle.ConfirmationStatus != domain.CharacterCardConfirmed {
					state.AllowedOperations = append(state.AllowedOperations, "confirm")
				}
			}
		}
		state.AllowedOperations = append(state.AllowedOperations, "review", "discard")
	}
	if strings.TrimSpace(runID) == "" {
		state.Run, err = s.store.CharacterWorkspace.Latest()
	} else {
		state.Run, err = s.store.CharacterWorkspace.Load(runID)
		if err == nil && state.Run == nil {
			return nil, characterWorkspaceError(CharacterWorkspaceErrorNotFound, "character workspace run was not found")
		}
	}
	if err != nil {
		return nil, err
	}
	if state.Run != nil {
		if state.Run.Error != nil {
			state.Error = cloneCharacterError(state.Run.Error)
		}
		if state.Run.Active() {
			state.BusyReason = "a Character Agent run is active"
		}
		if state.Run.Status == domain.CharacterWorkspaceFailed ||
			state.Run.Status == domain.CharacterWorkspaceInterrupted {
			state.AllowedOperations = append(state.AllowedOperations, "retry")
		}
		if state.Run.Status == domain.CharacterWorkspaceStale && state.StaleReason == "" {
			state.StaleReason = "the run is stale for the current Foundation inputs"
		}
	}
	activeRevision, activeErr := s.store.Revisions.Active()
	if activeErr != nil {
		return nil, activeErr
	}
	if activeRevision != nil {
		state.BusyReason = "Foundation or manuscript revision " + activeRevision.ID + " is active"
	}
	foundationState, foundationErr := NewFoundationRevisionService(s.store).State()
	if foundationErr == nil && !characterWorkspaceFoundationEditable(foundationState) {
		state.ReadonlyReason = foundationState.ReadonlyReason
	}
	if state.BusyReason != "" || state.ReadonlyReason != "" {
		state.AllowedOperations = filterCharacterOperations(state.AllowedOperations, "preview")
	}
	state.AllowedOperations = uniqueSortedStrings(state.AllowedOperations)
	return state, nil
}

func (s *CharacterWorkspaceService) PrepareAnalyze(
	request CharacterAnalyzeRequest,
) (domain.CharacterWorkspaceRun, bool, error) {
	current, binding, _, _, mode, err := s.current()
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if err := validateCharacterExpectedBase(request.ExpectedBaseRevision, request.ExpectedBaseAuditSignature, binding); err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if err := validateCharacterInstruction(request.Instruction); err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	inputCandidate, digest, err := s.analyzeRequestCandidate(current, binding, request)
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if strings.TrimSpace(request.CandidateDigest) == "" || strings.TrimSpace(request.CandidateDigest) != digest {
		return domain.CharacterWorkspaceRun{}, false,
			characterWorkspaceError(CharacterWorkspaceErrorStale, "candidate_digest does not match the server-normalized draft")
	}
	scope, err := validateCharacterScope(request.Scope, inputCandidate)
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	key := strings.TrimSpace(request.IdempotencyKey)
	if key == "" {
		return domain.CharacterWorkspaceRun{}, false,
			characterWorkspaceError(CharacterWorkspaceErrorInvalid, "idempotency_key is required")
	}
	fingerprint := storepkg.FoundationRevisionFingerprint(struct {
		IdempotencyKey  string
		BaseRevision    int64
		BaseAudit       string
		CandidateDigest string
		Scope           []string
		Instruction     string
		AllowSupporting bool
	}{
		key,
		binding.Candidate.FoundationRevision,
		binding.Candidate.FoundationAuditSignature,
		digest,
		scope,
		strings.TrimSpace(request.Instruction),
		request.AllowSupportingCharacters,
	})
	existingRun, err := s.store.CharacterWorkspace.FindByIdempotency(domain.CharacterWorkspaceAnalyze, key)
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if existingRun != nil {
		if existingRun.RequestFingerprint != fingerprint {
			return domain.CharacterWorkspaceRun{}, false,
				characterWorkspaceError(CharacterWorkspaceErrorConflict, "analyze idempotency key was reused with different input")
		}
		return *existingRun, false, nil
	}
	now := domain.RevisionTimestamp()
	run := domain.CharacterWorkspaceRun{
		Version:                   domain.CharacterWorkspaceRunVersion,
		RunID:                     "character-analyze-" + fingerprint[:20],
		Mode:                      domain.CharacterWorkspaceAnalyze,
		Status:                    domain.CharacterWorkspaceQueued,
		Stage:                     "queued",
		ProjectMode:               mode,
		Base:                      binding,
		IdempotencyKey:            key,
		RequestFingerprint:        fingerprint,
		RequestedCharacterIDs:     scope,
		Instruction:               strings.TrimSpace(request.Instruction),
		AllowSupportingCharacters: request.AllowSupportingCharacters,
		InputCandidate:            inputCandidate,
		InputCandidateDigest:      digest,
		Attempt:                   1,
		ModelRoute:                s.modelRoute(domain.CharacterWorkspaceAnalyze),
		CharacterCount:            len(inputCandidate.Characters),
		RelationshipCount:         len(inputCandidate.Relationships),
		RetryReceipts:             []domain.CharacterWorkspaceReceipt{},
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	return s.createRun(run)
}

func (s *CharacterWorkspaceService) analyzeRequestCandidate(
	current domain.StoryFoundation,
	canonicalBinding domain.CharacterCardBinding,
	request CharacterAnalyzeRequest,
) (domain.StoryFoundation, string, error) {
	if request.Candidate != nil || request.CandidateRevision <= 0 {
		return s.normalizeInputCandidate(current, request.Candidate)
	}
	candidate, err := s.store.CharacterCards.LoadCandidate()
	if err != nil {
		return domain.StoryFoundation{}, "", err
	}
	if candidate == nil || candidate.Revision != request.CandidateRevision {
		return domain.StoryFoundation{}, "", characterWorkspaceError(
			CharacterWorkspaceErrorStale,
			"candidate_revision is stale for character analysis",
		)
	}
	if candidate.Base.Candidate != canonicalBinding.Candidate ||
		candidate.Base.InputDigest != canonicalBinding.InputDigest {
		return domain.StoryFoundation{}, "", characterWorkspaceError(
			CharacterWorkspaceErrorStale,
			"persisted character candidate no longer matches the current Foundation inputs",
		)
	}
	digest, err := domain.CharacterCardContentDigest(candidate.Foundation)
	if err != nil {
		return domain.StoryFoundation{}, "", err
	}
	return domain.CloneStoryFoundation(candidate.Foundation), digest, nil
}

func (s *CharacterWorkspaceService) PrepareReview(
	request CharacterReviewRequest,
) (domain.CharacterWorkspaceRun, bool, error) {
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()

	current, binding, inputs, coreCast, mode, err := s.current()
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if err := validateCharacterExpectedBase(request.ExpectedBaseRevision, request.ExpectedBaseAuditSignature, binding); err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	key := strings.TrimSpace(request.IdempotencyKey)
	if key == "" {
		return domain.CharacterWorkspaceRun{}, false,
			characterWorkspaceError(CharacterWorkspaceErrorInvalid, "idempotency_key is required")
	}
	requestedDigest, err := s.reviewRequestDigest(current, request)
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	fingerprint := storepkg.FoundationRevisionFingerprint(struct {
		IdempotencyKey  string
		BaseRevision    int64
		BaseAudit       string
		CandidateDigest string
		InputDigest     string
	}{
		key,
		binding.Candidate.FoundationRevision,
		binding.Candidate.FoundationAuditSignature,
		requestedDigest,
		binding.InputDigest,
	})
	existingRun, err := s.store.CharacterWorkspace.FindByIdempotency(domain.CharacterWorkspaceReview, key)
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if existingRun != nil {
		if existingRun.RequestFingerprint != fingerprint {
			return domain.CharacterWorkspaceRun{}, false,
				characterWorkspaceError(CharacterWorkspaceErrorConflict, "review idempotency key was reused with different input")
		}
		return *existingRun, false, nil
	}
	if err := s.ensureAvailable(); err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	candidate, candidateBinding, lifecycle, err := s.stageReviewCandidate(
		current,
		binding,
		inputs,
		coreCast,
		mode,
		request,
	)
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	now := domain.RevisionTimestamp()
	run := domain.CharacterWorkspaceRun{
		Version:              domain.CharacterWorkspaceRunVersion,
		RunID:                "character-review-" + fingerprint[:20],
		Mode:                 domain.CharacterWorkspaceReview,
		Status:               domain.CharacterWorkspaceQueued,
		Stage:                "queued",
		ProjectMode:          mode,
		Base:                 binding,
		IdempotencyKey:       key,
		RequestFingerprint:   fingerprint,
		InputCandidate:       candidate.Foundation,
		InputCandidateDigest: candidateBinding.Candidate.CharacterContentDigest,
		CandidateRevision:    candidate.Revision,
		LifecycleRevision:    lifecycle.Revision,
		Attempt:              1,
		ModelRoute:           s.modelRoute(domain.CharacterWorkspaceReview),
		CharacterCount:       len(candidate.Foundation.Characters),
		RelationshipCount:    len(candidate.Foundation.Relationships),
		RetryReceipts:        []domain.CharacterWorkspaceReceipt{},
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	return s.createRun(run)
}

func (s *CharacterWorkspaceService) reviewRequestDigest(
	current domain.StoryFoundation,
	request CharacterReviewRequest,
) (string, error) {
	if request.Candidate != nil {
		_, digest, err := s.normalizeInputCandidate(current, request.Candidate)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(request.CandidateDigest) != digest {
			return "", characterWorkspaceError(CharacterWorkspaceErrorStale, "candidate_digest does not match the review candidate")
		}
		return digest, nil
	}
	candidate, err := s.store.CharacterCards.LoadCandidate()
	if err != nil {
		return "", err
	}
	if candidate == nil || request.CandidateRevision <= 0 || candidate.Revision != request.CandidateRevision {
		return "", characterWorkspaceError(
			CharacterWorkspaceErrorInvalid,
			"review requires a complete candidate or the current persisted candidate_revision",
		)
	}
	digest, err := domain.CharacterCardContentDigest(candidate.Foundation)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(request.CandidateDigest) != digest {
		return "", characterWorkspaceError(CharacterWorkspaceErrorStale, "candidate_digest does not match the review candidate")
	}
	return digest, nil
}

func (s *CharacterWorkspaceService) PrepareRetry(
	request CharacterRetryRequest,
) (domain.CharacterWorkspaceRun, bool, error) {
	run, err := s.store.CharacterWorkspace.Load(request.RunID)
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if run == nil {
		return domain.CharacterWorkspaceRun{}, false,
			characterWorkspaceError(CharacterWorkspaceErrorNotFound, "character workspace run was not found")
	}
	current, binding, _, _, _, err := s.current()
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if err := validateCharacterExpectedBase(request.ExpectedBaseRevision, request.ExpectedBaseAuditSignature, binding); err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	if run.Base.Candidate != binding.Candidate || run.Base.InputDigest != binding.InputDigest {
		_, _ = s.store.CharacterWorkspace.Update(run.RunID, run.Revision, func(next *domain.CharacterWorkspaceRun) error {
			next.Status = domain.CharacterWorkspaceStale
			next.Stage = "stale"
			next.UpdatedAt = domain.RevisionTimestamp()
			return nil
		})
		return domain.CharacterWorkspaceRun{}, false,
			characterWorkspaceError(CharacterWorkspaceErrorStale, "Foundation inputs changed before retry")
	}
	digest := run.InputCandidateDigest
	if run.ResultCandidate.CharacterContentDigest != "" {
		digest = run.ResultCandidate.CharacterContentDigest
	}
	if strings.TrimSpace(request.CandidateDigest) != digest {
		return domain.CharacterWorkspaceRun{}, false,
			characterWorkspaceError(CharacterWorkspaceErrorStale, "candidate_digest is stale for this run")
	}
	key := strings.TrimSpace(request.IdempotencyKey)
	if key == "" {
		return domain.CharacterWorkspaceRun{}, false,
			characterWorkspaceError(CharacterWorkspaceErrorInvalid, "idempotency_key is required")
	}
	fingerprint := storepkg.FoundationRevisionFingerprint(struct {
		RunID, CandidateDigest string
		BaseRevision           int64
		BaseAudit              string
	}{run.RunID, digest, current.Revision, binding.Candidate.FoundationAuditSignature})
	for _, receipt := range run.RetryReceipts {
		if receipt.IdempotencyKey != key {
			continue
		}
		if receipt.Fingerprint != fingerprint {
			return domain.CharacterWorkspaceRun{}, false,
				characterWorkspaceError(CharacterWorkspaceErrorConflict, "retry idempotency key was reused with different input")
		}
		return *run, false, nil
	}
	if run.Status != domain.CharacterWorkspaceFailed &&
		run.Status != domain.CharacterWorkspaceInterrupted {
		return domain.CharacterWorkspaceRun{}, false,
			characterWorkspaceError(CharacterWorkspaceErrorConflict, "only failed or interrupted Character Agent runs can be retried")
	}
	updated, err := s.store.CharacterWorkspace.Update(run.RunID, run.Revision, func(next *domain.CharacterWorkspaceRun) error {
		next.Attempt++
		next.Status = domain.CharacterWorkspaceQueued
		next.Stage = "queued"
		next.Error = nil
		next.StartedAt = ""
		next.FinishedAt = ""
		next.DurationMS = 0
		next.UpdatedAt = domain.RevisionTimestamp()
		next.RetryReceipts = append(next.RetryReceipts, domain.CharacterWorkspaceReceipt{
			IdempotencyKey: key,
			Fingerprint:    fingerprint,
			Attempt:        next.Attempt,
		})
		return nil
	})
	return updated, err == nil, err
}

func (s *CharacterWorkspaceService) Execute(ctx context.Context, runID string) error {
	if s == nil || s.runtime == nil {
		return characterWorkspaceError(CharacterWorkspaceErrorAgent, "Character Agent runtime is unavailable")
	}
	run, err := s.store.CharacterWorkspace.Load(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return characterWorkspaceError(CharacterWorkspaceErrorNotFound, "character workspace run was not found")
	}
	if run.Status == domain.CharacterWorkspaceCompleted {
		return nil
	}
	if run.Status != domain.CharacterWorkspaceQueued {
		return characterWorkspaceError(CharacterWorkspaceErrorConflict, "character workspace run is not queued")
	}
	if completed, inspectErr := s.reconcileCompletedRun(*run); inspectErr != nil {
		return inspectErr
	} else if completed {
		return nil
	}
	current, binding, _, _, _, err := s.current()
	if err != nil {
		return s.failRun(*run, CharacterWorkspaceErrorRecovery, err)
	}
	if run.Base.Candidate != binding.Candidate || run.Base.InputDigest != binding.InputDigest ||
		current.Revision != run.Base.Candidate.FoundationRevision {
		return s.staleRun(*run, "Foundation or Character Agent inputs changed before execution")
	}
	if run.Mode == domain.CharacterWorkspaceReview {
		if err := s.markReviewInProgress(*run); err != nil {
			return s.failRun(*run, CharacterWorkspaceErrorRecovery, err)
		}
	}
	startedAt := time.Now().UTC()
	running, err := s.store.CharacterWorkspace.Update(run.RunID, run.Revision, func(next *domain.CharacterWorkspaceRun) error {
		next.Status = domain.CharacterWorkspaceRunning
		next.Stage = "agent"
		next.StartedAt = startedAt.Format(time.RFC3339Nano)
		next.UpdatedAt = next.StartedAt
		return nil
	})
	if err != nil {
		return err
	}
	taskMode := tools.CharacterRunAnalyze
	if running.Mode == domain.CharacterWorkspaceReview {
		taskMode = tools.CharacterRunReview
	}
	task, err := agents.NewCharacterTask(
		running.RunID,
		taskMode,
		running.ProjectMode,
		running.Base,
		s.characterTaskInstruction(running),
	)
	if err != nil {
		return s.failRun(running, CharacterWorkspaceErrorInvalid, err)
	}
	if err := s.runtime.ExecuteCharacterAgent(ctx, task); err != nil {
		if completed, inspectErr := s.reconcileCompletedRun(running); inspectErr == nil && completed {
			return nil
		}
		return s.failRun(running, CharacterWorkspaceErrorAgent, err)
	}
	completed, err := s.reconcileCompletedRun(running)
	if err != nil {
		return s.failRun(running, CharacterWorkspaceErrorRecovery, err)
	}
	if !completed {
		return s.failRun(running, CharacterWorkspaceErrorAgent,
			errors.New("Character Agent returned without a matching persisted result"))
	}
	return nil
}

// FailQueued ensures a run prepared by an HTTP request cannot remain queued
// forever if the background action itself could not be started.
func (s *CharacterWorkspaceService) FailQueued(runID string, cause error) error {
	run, err := s.store.CharacterWorkspace.Load(runID)
	if err != nil || run == nil {
		return err
	}
	if run.Status != domain.CharacterWorkspaceQueued {
		return nil
	}
	message := retrypolicy.SanitizeProviderError(cause)
	if message == "" {
		message = "Character Agent action could not be started"
	}
	_, err = s.store.CharacterWorkspace.Update(run.RunID, run.Revision, func(next *domain.CharacterWorkspaceRun) error {
		if next.Status != domain.CharacterWorkspaceQueued {
			return nil
		}
		next.Status = domain.CharacterWorkspaceFailed
		next.Stage = "action_start"
		next.Error = &domain.CharacterCardError{
			Class:   CharacterWorkspaceErrorRecovery,
			Message: message,
		}
		next.UpdatedAt = domain.RevisionTimestamp()
		next.FinishedAt = next.UpdatedAt
		return nil
	})
	return err
}

func (s *CharacterWorkspaceService) Discard(
	request CharacterDiscardRequest,
) (*CharacterWorkspaceState, error) {
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()

	_, binding, _, _, _, err := s.current()
	if err != nil {
		return nil, err
	}
	if err := validateCharacterExpectedBase(request.ExpectedBaseRevision, request.ExpectedBaseAuditSignature, binding); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.CandidateDigest) == "" {
		return nil, characterWorkspaceError(
			CharacterWorkspaceErrorInvalid,
			"idempotency_key and candidate_digest are required",
		)
	}
	latest, err := s.store.CharacterWorkspace.Latest()
	if err != nil {
		return nil, err
	}
	run := latest
	if strings.TrimSpace(request.RunID) != "" {
		run, err = s.store.CharacterWorkspace.Load(request.RunID)
		if err != nil {
			return nil, err
		}
	}
	if run != nil && run.Active() {
		return nil, characterWorkspaceError(CharacterWorkspaceErrorBusy, "active Character Agent run cannot be discarded")
	}
	fingerprint := storepkg.FoundationRevisionFingerprint(struct {
		RunID, CandidateDigest string
		BaseRevision           int64
	}{strings.TrimSpace(request.RunID), strings.TrimSpace(request.CandidateDigest), binding.Candidate.FoundationRevision})
	if run != nil && run.DiscardReceipt != nil {
		if run.DiscardReceipt.IdempotencyKey == strings.TrimSpace(request.IdempotencyKey) &&
			run.DiscardReceipt.Fingerprint == fingerprint {
			return s.State(run.RunID)
		}
		return nil, characterWorkspaceError(CharacterWorkspaceErrorConflict, "discard idempotency key was reused")
	}
	if err := s.store.CharacterCards.DiscardCandidate(strings.TrimSpace(request.CandidateDigest)); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, characterWorkspaceError(CharacterWorkspaceErrorStale, err.Error())
		}
	}
	if run != nil {
		updated, updateErr := s.store.CharacterWorkspace.Update(run.RunID, run.Revision, func(next *domain.CharacterWorkspaceRun) error {
			next.Status = domain.CharacterWorkspaceDiscarded
			next.Stage = "discarded"
			next.Error = nil
			next.UpdatedAt = domain.RevisionTimestamp()
			next.FinishedAt = next.UpdatedAt
			next.DiscardReceipt = &domain.CharacterWorkspaceReceipt{
				IdempotencyKey: strings.TrimSpace(request.IdempotencyKey),
				Fingerprint:    fingerprint,
				Attempt:        next.Attempt,
			}
			return nil
		})
		if updateErr != nil {
			return nil, updateErr
		}
		return s.State(updated.RunID)
	}
	return s.State("")
}

func (s *CharacterWorkspaceService) current() (
	domain.StoryFoundation,
	domain.CharacterCardBinding,
	domain.CharacterCardInputSignatures,
	*domain.CoreCastContract,
	domain.CharacterCardProjectMode,
	error,
) {
	if s == nil || s.store == nil {
		return domain.StoryFoundation{}, domain.CharacterCardBinding{}, domain.CharacterCardInputSignatures{}, nil, "", fmt.Errorf("character workspace store is unavailable")
	}
	foundation, binding, inputs, coreCast, err := tools.CurrentCharacterCanonicalBinding(s.store)
	if err != nil {
		return foundation, binding, inputs, coreCast, "", err
	}
	mode := domain.CharacterCardProjectOriginal
	if coreCast != nil && coreCast.Mode == domain.CoreCastModeAdaptation {
		mode = domain.CharacterCardProjectAdaptation
	}
	return foundation, binding, inputs, coreCast, mode, nil
}

func (s *CharacterWorkspaceService) normalizeInputCandidate(
	current domain.StoryFoundation,
	requested *domain.StoryFoundation,
) (domain.StoryFoundation, string, error) {
	candidate := domain.CloneStoryFoundation(current)
	if requested != nil {
		candidate = domain.CloneStoryFoundation(*requested)
	}
	normalized, err := domain.NormalizeStoryFoundation(candidate)
	if err != nil {
		return domain.StoryFoundation{}, "", characterWorkspaceError(CharacterWorkspaceErrorInvalid, err.Error())
	}
	if normalized.Revision != current.Revision ||
		normalized.Premise != current.Premise ||
		!reflect.DeepEqual(normalized.WorldRules, current.WorldRules) {
		return domain.StoryFoundation{}, "", characterWorkspaceError(
			CharacterWorkspaceErrorInvalid,
			"Character Agent candidate may change only characters, relationships, and relationships_reviewed",
		)
	}
	digest, err := domain.CharacterCardContentDigest(normalized)
	if err != nil {
		return domain.StoryFoundation{}, "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return domain.StoryFoundation{}, "", characterWorkspaceError(CharacterWorkspaceErrorInvalid, "candidate cannot be encoded")
	}
	if len(encoded) > characterCandidateLimitBytes {
		return domain.StoryFoundation{}, "", characterWorkspaceError(
			CharacterWorkspaceErrorInvalid,
			fmt.Sprintf("candidate exceeds %d bytes", characterCandidateLimitBytes),
		)
	}
	return normalized, digest, nil
}

func (s *CharacterWorkspaceService) stageReviewCandidate(
	current domain.StoryFoundation,
	canonicalBinding domain.CharacterCardBinding,
	inputs domain.CharacterCardInputSignatures,
	coreCast *domain.CoreCastContract,
	mode domain.CharacterCardProjectMode,
	request CharacterReviewRequest,
) (domain.CharacterCardCandidate, domain.CharacterCardBinding, domain.CharacterCardLifecycle, error) {
	existingCandidate, err := s.store.CharacterCards.LoadCandidate()
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{}, err
	}
	var foundation domain.StoryFoundation
	if request.Candidate != nil {
		foundation, _, err = s.normalizeInputCandidate(current, request.Candidate)
		if err != nil {
			return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{}, err
		}
	} else {
		if existingCandidate == nil || request.CandidateRevision <= 0 ||
			existingCandidate.Revision != request.CandidateRevision {
			return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{},
				characterWorkspaceError(
					CharacterWorkspaceErrorInvalid,
					"review requires a complete candidate or the current persisted candidate_revision",
				)
		}
		foundation = existingCandidate.Foundation
	}
	digest, err := domain.CharacterCardContentDigest(foundation)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{}, err
	}
	if strings.TrimSpace(request.CandidateDigest) != digest {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{},
			characterWorkspaceError(CharacterWorkspaceErrorStale, "candidate_digest does not match the review candidate")
	}
	if existingCandidate != nil &&
		request.CandidateRevision > 0 &&
		existingCandidate.Revision != request.CandidateRevision {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{},
			characterWorkspaceError(CharacterWorkspaceErrorStale, "candidate_revision is stale")
	}
	projected, projectionFindings, err := domain.ProjectCharacterCandidateCoreCast(foundation, coreCast)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{}, err
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(foundation, &projected)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{}, err
	}
	expectedCandidateRevision := int64(0)
	if existingCandidate != nil {
		expectedCandidateRevision = existingCandidate.Revision
	}
	staged, err := s.store.CharacterCards.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:       domain.CharacterCardCandidateVersion,
		Base:          canonicalBinding,
		Foundation:    foundation,
		ProjectedCast: projected,
	}, expectedCandidateRevision)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{},
			characterWorkspaceError(CharacterWorkspaceErrorConflict, err.Error())
	}
	candidateBinding, err := domain.CharacterCardBindingFromFoundation(staged.Foundation, inputs)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{}, err
	}
	existingLifecycle, err := s.store.CharacterCards.Load(candidateBinding)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{}, err
	}
	expectedLifecycleRevision := int64(0)
	analysisSummary := "user-provided Character workspace draft"
	sourceMappings := append([]domain.CharacterSourceMapping(nil), request.SourceMappings...)
	if existingLifecycle != nil {
		expectedLifecycleRevision = existingLifecycle.Revision
		if existingLifecycle.AnalysisSummary != "" {
			analysisSummary = existingLifecycle.AnalysisSummary
		}
		if request.SourceMappings == nil {
			sourceMappings = append([]domain.CharacterSourceMapping(nil), existingLifecycle.SourceMappings...)
		}
	}
	if mode == domain.CharacterCardProjectOriginal && len(sourceMappings) > 0 {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{},
			characterWorkspaceError(CharacterWorkspaceErrorInvalid, "original projects must not provide source_mappings")
	}
	lifecycle := domain.CharacterCardLifecycle{
		Version:             domain.CharacterCardLifecycleVersion,
		Mode:                mode,
		Candidate:           candidateBinding.Candidate,
		Inputs:              candidateBinding.Inputs,
		InputDigest:         candidateBinding.InputDigest,
		AnalysisSummary:     analysisSummary,
		Completeness:        completeness,
		AnalysisStatus:      domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:        domain.CharacterCardReviewInProgress,
		ReviewedCandidate:   candidateBinding.Candidate,
		ReviewedInputDigest: candidateBinding.InputDigest,
		Findings:            projectionFindings,
		ConfirmationStatus:  domain.CharacterCardUnconfirmed,
		SourceMappings:      sourceMappings,
	}
	savedLifecycle, err := s.store.CharacterCards.SaveCAS(lifecycle, expectedLifecycleRevision, candidateBinding)
	if err != nil {
		return domain.CharacterCardCandidate{}, domain.CharacterCardBinding{}, domain.CharacterCardLifecycle{},
			characterWorkspaceError(CharacterWorkspaceErrorConflict, err.Error())
	}
	return staged, candidateBinding, savedLifecycle, nil
}

func (s *CharacterWorkspaceService) createRun(
	run domain.CharacterWorkspaceRun,
) (domain.CharacterWorkspaceRun, bool, error) {
	if err := s.ensureAvailable(); err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	created, fresh, err := s.store.CharacterWorkspace.Create(run)
	if err == nil {
		return created, fresh, nil
	}
	var conflict *storepkg.CharacterWorkspaceConflictError
	if errors.As(err, &conflict) {
		code := CharacterWorkspaceErrorConflict
		if strings.Contains(strings.ToLower(conflict.Error()), "active") {
			code = CharacterWorkspaceErrorBusy
		}
		return domain.CharacterWorkspaceRun{}, false, characterWorkspaceError(code, conflict.Error())
	}
	return domain.CharacterWorkspaceRun{}, false, err
}

func (s *CharacterWorkspaceService) ensureAvailable() error {
	if foundationState, err := NewFoundationRevisionService(s.store).State(); err != nil {
		return err
	} else if !characterWorkspaceFoundationEditable(foundationState) {
		return characterWorkspaceError(CharacterWorkspaceErrorReadonly, foundationState.ReadonlyReason)
	}
	activeRevision, err := s.store.Revisions.Active()
	if err != nil {
		return err
	}
	if activeRevision != nil {
		return characterWorkspaceError(CharacterWorkspaceErrorBusy, "a Foundation revision session is active")
	}
	latest, err := s.store.CharacterWorkspace.Latest()
	if err != nil {
		return err
	}
	if latest != nil && latest.Active() {
		return characterWorkspaceError(CharacterWorkspaceErrorBusy, "a Character Agent run is active")
	}
	return nil
}

func characterWorkspaceFoundationEditable(state *FoundationState) bool {
	if state == nil {
		return false
	}
	if state.Editable {
		return true
	}
	return state.ReadonlyReason == "planning_stage_not_editable" &&
		state.Mode == "normal" &&
		state.PlanningReview != nil &&
		state.PlanningReview.Kind == domain.PlanningReviewKindFoundation &&
		state.PlanningReview.Status == domain.PlanningReviewStatusCollecting &&
		state.PlanningReview.FoundationStatus == domain.FoundationReviewStatusCollecting
}

func (s *CharacterWorkspaceService) markReviewInProgress(run domain.CharacterWorkspaceRun) error {
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(s.store)
	if err != nil {
		return err
	}
	if candidate == nil || lifecycle == nil ||
		binding.Candidate.CharacterContentDigest != run.InputCandidateDigest {
		return characterWorkspaceError(CharacterWorkspaceErrorStale, "persisted review candidate changed")
	}
	next := *lifecycle
	next.ReviewStatus = domain.CharacterCardReviewInProgress
	next.ReviewedCandidate = binding.Candidate
	next.ReviewedInputDigest = binding.InputDigest
	next.ConfirmationStatus = domain.CharacterCardUnconfirmed
	next.Error = nil
	next.RunID = ""
	next.IdempotencyKey = ""
	next.SubmissionDigest = ""
	_, err = s.store.CharacterCards.SaveCAS(next, lifecycle.Revision, binding)
	return err
}

func (s *CharacterWorkspaceService) reconcileCompletedRun(
	run domain.CharacterWorkspaceRun,
) (bool, error) {
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(s.store)
	if err != nil || candidate == nil || lifecycle == nil {
		return false, err
	}
	if lifecycle.RunID != run.RunID || lifecycle.IdempotencyKey != run.IdempotencyKey {
		return false, nil
	}
	switch run.Mode {
	case domain.CharacterWorkspaceAnalyze:
		if lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady {
			return false, nil
		}
		if err := validateScopedCharacterResult(run, candidate.Foundation); err != nil {
			return false, err
		}
	case domain.CharacterWorkspaceReview:
		if lifecycle.ReviewStatus != domain.CharacterCardReviewPassed &&
			lifecycle.ReviewStatus != domain.CharacterCardReviewNeedsRevision {
			return false, nil
		}
	default:
		return false, characterWorkspaceError(CharacterWorkspaceErrorInvalid, "unsupported Character Agent run mode")
	}
	current, err := s.store.CharacterWorkspace.Load(run.RunID)
	if err != nil || current == nil {
		return false, err
	}
	if current.Status == domain.CharacterWorkspaceCompleted {
		return true, nil
	}
	finishedAt := time.Now().UTC()
	startedAt, _ := time.Parse(time.RFC3339Nano, current.StartedAt)
	duration := int64(0)
	if !startedAt.IsZero() {
		duration = finishedAt.Sub(startedAt).Milliseconds()
	}
	_, err = s.store.CharacterWorkspace.Update(current.RunID, current.Revision, func(next *domain.CharacterWorkspaceRun) error {
		next.Status = domain.CharacterWorkspaceCompleted
		next.Stage = "completed"
		next.ResultCandidate = binding.Candidate
		next.CandidateRevision = candidate.Revision
		next.LifecycleRevision = lifecycle.Revision
		next.CharacterCount = len(candidate.Foundation.Characters)
		next.RelationshipCount = len(candidate.Foundation.Relationships)
		next.Error = nil
		next.FinishedAt = finishedAt.Format(time.RFC3339Nano)
		next.UpdatedAt = next.FinishedAt
		next.DurationMS = duration
		return nil
	})
	return err == nil, err
}

func (s *CharacterWorkspaceService) failRun(
	run domain.CharacterWorkspaceRun,
	class string,
	cause error,
) error {
	message := strings.TrimSpace(retrypolicy.SanitizeProviderError(cause))
	if message == "" {
		message = "Character Agent run failed"
	}
	current, loadErr := s.store.CharacterWorkspace.Load(run.RunID)
	if loadErr == nil && current != nil && current.Status != domain.CharacterWorkspaceCompleted {
		finishedAt := time.Now().UTC()
		startedAt, _ := time.Parse(time.RFC3339Nano, current.StartedAt)
		duration := int64(0)
		if !startedAt.IsZero() {
			duration = finishedAt.Sub(startedAt).Milliseconds()
		}
		_, _ = s.store.CharacterWorkspace.Update(current.RunID, current.Revision, func(next *domain.CharacterWorkspaceRun) error {
			next.Status = domain.CharacterWorkspaceFailed
			next.Stage = "failed"
			next.Error = &domain.CharacterCardError{Class: class, Message: message}
			next.FinishedAt = finishedAt.Format(time.RFC3339Nano)
			next.UpdatedAt = next.FinishedAt
			next.DurationMS = duration
			return nil
		})
	}
	if run.Mode == domain.CharacterWorkspaceReview {
		s.markReviewFailed(run, class, message)
	}
	return characterWorkspaceError(class, message)
}

func (s *CharacterWorkspaceService) staleRun(
	run domain.CharacterWorkspaceRun,
	message string,
) error {
	current, err := s.store.CharacterWorkspace.Load(run.RunID)
	if err == nil && current != nil {
		_, _ = s.store.CharacterWorkspace.Update(current.RunID, current.Revision, func(next *domain.CharacterWorkspaceRun) error {
			next.Status = domain.CharacterWorkspaceStale
			next.Stage = "stale"
			next.Error = &domain.CharacterCardError{Class: CharacterWorkspaceErrorStale, Message: message}
			next.FinishedAt = domain.RevisionTimestamp()
			next.UpdatedAt = next.FinishedAt
			return nil
		})
	}
	return characterWorkspaceError(CharacterWorkspaceErrorStale, message)
}

func (s *CharacterWorkspaceService) markReviewFailed(
	run domain.CharacterWorkspaceRun,
	class string,
	message string,
) {
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(s.store)
	if err != nil || candidate == nil || lifecycle == nil ||
		binding.Candidate.CharacterContentDigest != run.InputCandidateDigest {
		return
	}
	next := *lifecycle
	next.ReviewStatus = domain.CharacterCardReviewFailed
	next.ReviewedCandidate = binding.Candidate
	next.ReviewedInputDigest = binding.InputDigest
	next.ConfirmationStatus = domain.CharacterCardUnconfirmed
	next.Error = &domain.CharacterCardError{Class: class, Message: message}
	next.RunID = ""
	next.IdempotencyKey = ""
	next.SubmissionDigest = ""
	_, _ = s.store.CharacterCards.SaveCAS(next, lifecycle.Revision, binding)
}

func (s *CharacterWorkspaceService) characterTaskInstruction(
	run domain.CharacterWorkspaceRun,
) string {
	scope := "all characters"
	if len(run.RequestedCharacterIDs) > 0 {
		scope = "only character IDs " + strings.Join(run.RequestedCharacterIDs, ", ")
	}
	instruction := strings.TrimSpace(run.Instruction)
	if instruction == "" {
		if run.Mode == domain.CharacterWorkspaceReview {
			instruction = "Independently review the exact persisted Character workspace candidate."
		} else {
			instruction = "Analyze and complete the Character workspace draft."
		}
	}
	return fmt.Sprintf(
		"%s Scope: %s. Idempotency key: %s. Supporting character additions allowed: %t.",
		instruction,
		scope,
		run.IdempotencyKey,
		run.AllowSupportingCharacters,
	)
}

func (s *CharacterWorkspaceService) modelRoute(mode domain.CharacterWorkspaceRunMode) string {
	if s.runtime == nil {
		return ""
	}
	return s.runtime.CharacterAgentModelRoute(mode)
}

func validateCharacterExpectedBase(
	revision int64,
	audit string,
	binding domain.CharacterCardBinding,
) error {
	if revision != binding.Candidate.FoundationRevision ||
		strings.TrimSpace(audit) != binding.Candidate.FoundationAuditSignature {
		return characterWorkspaceError(CharacterWorkspaceErrorStale, "Foundation base revision or audit signature changed")
	}
	return nil
}

func validateCharacterInstruction(value string) error {
	if !utf8.ValidString(value) {
		return characterWorkspaceError(CharacterWorkspaceErrorInvalid, "instruction must be valid UTF-8")
	}
	if len(value) > characterInstructionLimitBytes {
		return characterWorkspaceError(
			CharacterWorkspaceErrorInvalid,
			fmt.Sprintf("instruction exceeds %d bytes", characterInstructionLimitBytes),
		)
	}
	return nil
}

func validateCharacterScope(
	scope CharacterAnalysisScope,
	candidate domain.StoryFoundation,
) ([]string, error) {
	ids := uniqueSortedStrings(scope.CharacterIDs)
	if len(ids) == 0 {
		return []string{}, nil
	}
	known := make(map[string]bool, len(candidate.Characters))
	for _, character := range candidate.Characters {
		known[character.ID] = true
	}
	for _, id := range ids {
		if !known[id] {
			return nil, characterWorkspaceError(
				CharacterWorkspaceErrorInvalid,
				fmt.Sprintf("scope character_id %q does not exist", id),
			)
		}
	}
	return ids, nil
}

func validateScopedCharacterResult(
	run domain.CharacterWorkspaceRun,
	result domain.StoryFoundation,
) error {
	if len(run.RequestedCharacterIDs) == 0 {
		return nil
	}
	scoped := make(map[string]bool, len(run.RequestedCharacterIDs))
	for _, id := range run.RequestedCharacterIDs {
		scoped[id] = true
	}
	before := make(map[string]domain.Character, len(run.InputCandidate.Characters))
	for _, character := range run.InputCandidate.Characters {
		before[character.ID] = character
	}
	for _, character := range result.Characters {
		previous, existed := before[character.ID]
		if existed {
			if !scoped[character.ID] && !reflect.DeepEqual(previous, character) {
				return characterWorkspaceError(
					CharacterWorkspaceErrorAgent,
					fmt.Sprintf("scoped analysis changed unrelated character %q", character.ID),
				)
			}
			delete(before, character.ID)
			continue
		}
		if !run.AllowSupportingCharacters {
			return characterWorkspaceError(
				CharacterWorkspaceErrorAgent,
				fmt.Sprintf("scoped analysis added character %q without permission", character.ID),
			)
		}
		if character.Tier == string(domain.CharacterTierCore) {
			return characterWorkspaceError(
				CharacterWorkspaceErrorAgent,
				fmt.Sprintf("scoped analysis added core character %q as supporting cast", character.ID),
			)
		}
		scoped[character.ID] = true
	}
	for id := range before {
		if !scoped[id] {
			return characterWorkspaceError(
				CharacterWorkspaceErrorAgent,
				fmt.Sprintf("scoped analysis removed unrelated character %q", id),
			)
		}
	}
	return nil
}

func currentCharacterReviewPassed(
	lifecycle domain.CharacterCardLifecycle,
	binding domain.CharacterCardBinding,
) bool {
	if lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.ReviewStatus != domain.CharacterCardReviewPassed ||
		lifecycle.Candidate != binding.Candidate ||
		lifecycle.InputDigest != binding.InputDigest ||
		lifecycle.ReviewedCandidate != binding.Candidate ||
		lifecycle.ReviewedInputDigest != binding.InputDigest {
		return false
	}
	for _, completeness := range lifecycle.Completeness {
		if completeness.Status != domain.CharacterCardComplete {
			return false
		}
	}
	if lifecycle.Coverage != nil && lifecycle.Coverage.BlockingGaps > 0 {
		return false
	}
	for _, finding := range lifecycle.Findings {
		if finding.Blocking || finding.Severity == domain.CharacterCardSeverityBlocking {
			return false
		}
	}
	return true
}

func characterWorkspaceError(code, message string) error {
	return &CharacterWorkspaceError{Code: code, Err: errors.New(strings.TrimSpace(message))}
}

func cloneCharacterError(value *domain.CharacterCardError) *domain.CharacterCardError {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func filterCharacterOperations(values []string, keep ...string) []string {
	allowed := make(map[string]bool, len(keep))
	for _, value := range keep {
		allowed[value] = true
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if allowed[value] {
			out = append(out, value)
		}
	}
	return out
}
