package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const revisionStateFile = "meta/revisions/state.json"

var (
	ErrRevisionNotFound                       = errors.New("revision session is not found")
	ErrActiveRevisionExists                   = errors.New("an active revision already exists")
	ErrNoActiveRevision                       = errors.New("no active revision")
	ErrRevisionIdempotencyConflict            = errors.New("revision idempotency key was reused for a different command")
	ErrActiveRevisionBlocksNormalFlow         = errors.New("normal flow is blocked by an active revision")
	ErrCompletionRevalidationBlocksNormalFlow = errors.New("normal flow is blocked by pending completion revalidation")
	ErrRevisionCommandInProgress              = errors.New("a revision service command owns the project")
	revisionLocks                             sync.Map
)

type RevisionConflictError struct {
	Expected int
	Actual   int
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("revision conflict: expected %d, actual %d", e.Expected, e.Actual)
}

func IsRevisionConflict(err error) bool {
	var conflict *RevisionConflictError
	return errors.As(err, &conflict)
}

type revisionReceipt struct {
	Operation          string                 `json:"operation"`
	Fingerprint        string                 `json:"fingerprint"`
	ServiceOperation   string                 `json:"service_operation,omitempty"`
	ServiceFingerprint string                 `json:"service_fingerprint,omitempty"`
	Result             domain.RevisionSession `json:"result"`
}

type revisionState struct {
	Version          int                               `json:"version"`
	Generation       uint64                            `json:"generation"`
	NormalLease      *NormalFlowLease                  `json:"normal_lease,omitempty"`
	CommandFence     *revisionCommandFence             `json:"command_fence,omitempty"`
	Publication      *revisionPublicationAttempt       `json:"publication,omitempty"`
	ActiveSessionID  string                            `json:"active_session_id,omitempty"`
	NextSession      int                               `json:"next_session"`
	NextVersion      int                               `json:"next_version"`
	Sessions         map[string]domain.RevisionSession `json:"sessions"`
	Versions         map[string]domain.ArtifactVersion `json:"versions"`
	CurrentArtifacts map[string]string                 `json:"current_artifacts"`
	Receipts         map[string]revisionReceipt        `json:"receipts"`
}

type revisionCommandFence struct {
	Key                       string `json:"key"`
	Operation                 string `json:"operation"`
	Fingerprint               string `json:"fingerprint"`
	OwnerToken                string `json:"owner_token"`
	RemoveEmptyStateOnRelease bool   `json:"remove_empty_state_on_release,omitempty"`
}

type revisionCommandOwner struct {
	Key         string
	Operation   string
	Fingerprint string
	Token       string
}

const (
	revisionPublicationPrepared = "prepared"
	revisionPublicationApplied  = "formal_applied"
)

type revisionPublicationAttempt struct {
	Token              string                       `json:"token"`
	SessionID          string                       `json:"session_id"`
	ExpectedRevision   int                          `json:"expected_revision"`
	Generation         uint64                       `json:"generation"`
	Mode               domain.RevisionMode          `json:"mode"`
	PolicyID           string                       `json:"policy_id"`
	PolicyVersion      string                       `json:"policy_version"`
	PublishKey         string                       `json:"publish_key"`
	PublishFingerprint string                       `json:"publish_fingerprint"`
	AcceptedVersionIDs []string                     `json:"accepted_version_ids"`
	AcceptedDigest     string                       `json:"accepted_digest"`
	CandidateDigest    string                       `json:"candidate_digest"`
	Status             string                       `json:"status"`
	PrepublishSnapshot normalRevisionFormalSnapshot `json:"prepublish_snapshot"`
	AuthoritySnapshot  publicationAuthoritySnapshot `json:"authority_snapshot"`
}

type normalRevisionFormalSnapshot struct {
	Structure []domain.VolumeOutline `json:"structure"`
	Progress  *domain.Progress       `json:"progress,omitempty"`
	Digest    string                 `json:"digest"`
}

type publicationAuthorityFileSnapshot struct {
	Exists bool        `json:"exists"`
	Data   []byte      `json:"data,omitempty"`
	Mode   os.FileMode `json:"mode,omitempty"`
}

type publicationAuthoritySnapshot struct {
	Version        int                                        `json:"version"`
	Trust          publicationAuthorityFileSnapshot           `json:"trust"`
	Receipt        publicationAuthorityFileSnapshot           `json:"receipt"`
	ExternalRecord publicationAuthorityExternalRecordSnapshot `json:"external_record"`
}

type publicationAuthorityExternalRecordSnapshot struct {
	Exists          bool        `json:"exists"`
	ProjectInstance string      `json:"project_instance,omitempty"`
	Data            []byte      `json:"data,omitempty"`
	Mode            os.FileMode `json:"mode,omitempty"`
	Digest          string      `json:"digest,omitempty"`
}

type RevisionStore struct {
	io               *IO
	mu               *sync.Mutex
	commandOwner     *revisionCommandOwner
	publicationOwner *RevisionPublicationOwner
}

// FoundationPlanningOwner is an opaque, single-session capability derived
// from the router fence carried by a dispatched Foundation repair turn.
type FoundationPlanningOwner struct {
	revisions  *RevisionStore
	sessionID  string
	revision   int
	generation uint64
	artifactID string
}

func (s *RevisionStore) AuthorizeFoundationPlanning(ctx context.Context, artifactID string) (*FoundationPlanningOwner, error) {
	fence, ok := RevisionFenceFromContext(ctx)
	if !ok || strings.TrimSpace(fence.SessionID) == "" || fence.LeaseToken != "" {
		return nil, ErrActiveRevisionBlocksNormalFlow
	}
	artifactID = strings.TrimSpace(artifactID)
	var owner *FoundationPlanningOwner
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		session, exists := state.Sessions[fence.SessionID]
		if !exists || state.ActiveSessionID != session.ID || state.Generation != fence.Generation || session.Revision != fence.Revision ||
			session.Generation != fence.Generation || session.Mode != domain.RevisionModeFoundation || session.PolicyID != domain.FoundationRevisionPolicyID ||
			session.Stage != domain.RevisionStageCandidateGenerating || len(session.Approvals) != 1 {
			return ErrActiveRevisionBlocksNormalFlow
		}
		allowed := false
		hasPlanningImpact := false
		for _, item := range session.Impact.Items {
			if item.ArtifactID == domain.FoundationPlanningArtifactID {
				hasPlanningImpact = true
			}
			if item.ArtifactID == artifactID {
				allowed = true
			}
		}
		// The existing original-planning audit route finishes by aggregating
		// every scoped result into the book audit. The planning-impact marker
		// authorizes that aggregate only; it must not make arbitrary artifact
		// identifiers writable when a full-book impact is present.
		if artifactID == "book" && hasPlanningImpact {
			allowed = true
		}
		if !allowed {
			return fmt.Errorf("Foundation planning artifact %q is outside the approved impact", artifactID)
		}
		owner = &FoundationPlanningOwner{revisions: s, sessionID: session.ID, revision: session.Revision, generation: session.Generation, artifactID: artifactID}
		return nil
	})
	return owner, err
}

func (s *RevisionStore) withFoundationPlanningMutation(owner *FoundationPlanningOwner, operation string, migration *structureMigration, mutation func() error) error {
	if owner == nil || owner.revisions == nil || !revisionStoresShareProject(s, owner.revisions) {
		return ErrActiveRevisionBlocksNormalFlow
	}
	return s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		session, exists := state.Sessions[owner.sessionID]
		if !exists || state.ActiveSessionID != owner.sessionID || state.Generation != owner.generation || session.Revision != owner.revision ||
			session.Generation != owner.generation || session.Stage != domain.RevisionStageCandidateGenerating || len(session.Approvals) != 1 {
			return ErrActiveRevisionBlocksNormalFlow
		}
		if migration != nil {
			if err := migration.recoverWithinRevisionTransaction(); err != nil {
				return fmt.Errorf("recover structure migration before %s: %w", operation, err)
			}
		}
		return mutation()
	})
}

func (s *RevisionStore) withLegacyMutation(operation string, mutation func() error) error {
	return s.withLegacyMigrationMutation(operation, nil, mutation)
}

// withLegacyMigrationMutation owns the complete formal-write transaction. It
// checks revision ownership before recovering a structure journal, then keeps
// the same revision transaction held through the caller's mutation.
func (s *RevisionStore) withLegacyMigrationMutation(operation string, migration *structureMigration, mutation func() error) error {
	if s == nil || s.io == nil {
		return fmt.Errorf("revision store is required before %s", operation)
	}
	return s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return fmt.Errorf("read active revision before %s: %w", operation, err)
		}
		foundationAdaptationOwned := foundationAdaptationCommandAllows(state, operation)
		if state.ActiveSessionID != "" && !foundationAdaptationOwned {
			return fmt.Errorf("legacy adaptation formal write %q is blocked by active revision %s: %w", operation, state.ActiveSessionID, ErrActiveRevisionBlocksNormalFlow)
		}
		if manuscriptID, err := activeManuscriptRevisionID(s.io); err != nil {
			return err
		} else if manuscriptID != "" {
			return fmt.Errorf("legacy formal write %q is blocked by active manuscript revision %s: %w", operation, manuscriptID, ErrActiveRevisionBlocksNormalFlow)
		}
		if state.CommandFence != nil && !foundationAdaptationOwned {
			return fmt.Errorf("legacy adaptation formal write %q is blocked by prepared service command %q: %w", operation, state.CommandFence.Operation, ErrRevisionCommandInProgress)
		}
		if migration != nil {
			if err := migration.recoverWithinRevisionTransaction(); err != nil {
				return fmt.Errorf("recover structure migration before %s: %w", operation, err)
			}
		}
		return mutation()
	})
}

func foundationAdaptationCommandAllows(state *revisionState, operation string) bool {
	if state == nil || state.CommandFence == nil || !strings.HasPrefix(state.CommandFence.Operation, "foundation-adaptation/") {
		return false
	}
	active, ok := state.Sessions[state.ActiveSessionID]
	if !ok || active.Mode != domain.RevisionModeFoundation || active.Stage != domain.RevisionStageCandidateGenerating || len(active.Approvals) != 1 {
		return false
	}
	switch strings.TrimSpace(operation) {
	case "change planning workflow",
		"save proposal", "save volume review", "restore volume review", "clear volume review",
		"save proposal runtime", "clear proposal runtime", "clear proposal workflow",
		"save layered outline", "save flat outline", "save story compass",
		"save adaptation audit", "save adaptation audit run", "mark audit run applied", "save adaptation audit repair",
		"save confirmed plan", "write formal progress":
		return true
	default:
		return false
	}
}

type NormalFlowLease struct {
	Token      string `json:"token"`
	Generation uint64 `json:"generation"`
	Owner      string `json:"owner"`
	PID        int    `json:"pid"`
	AcquiredAt string `json:"acquired_at"`
}

type RevisionFence struct {
	Generation uint64
	SessionID  string
	Revision   int
	LeaseToken string
}

type revisionFenceContextKey struct{}

// ContextWithRevisionFence binds asynchronous work to the ownership epoch
// that dispatched it. Writable tool boundaries must revalidate this fence
// immediately before applying their side effects.
func ContextWithRevisionFence(ctx context.Context, fence RevisionFence) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, revisionFenceContextKey{}, fence)
}

func RevisionFenceFromContext(ctx context.Context) (RevisionFence, bool) {
	if ctx == nil {
		return RevisionFence{}, false
	}
	fence, ok := ctx.Value(revisionFenceContextKey{}).(RevisionFence)
	return fence, ok
}

func (s *RevisionStore) AcquireNormalFlow(owner string) (*NormalFlowLease, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, fmt.Errorf("normal flow owner is required")
	}
	var lease *NormalFlowLease
	err := s.withRevisionTransaction(func() error {
		if pending, err := completionRevalidationPendingUnlocked(s.io); err != nil {
			return err
		} else if pending {
			return ErrCompletionRevalidationBlocksNormalFlow
		}
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.ActiveSessionID != "" {
			return ErrActiveRevisionBlocksNormalFlow
		}
		if manuscriptID, err := activeManuscriptRevisionID(s.io); err != nil {
			return err
		} else if manuscriptID != "" {
			return ErrActiveRevisionBlocksNormalFlow
		}
		if state.CommandFence != nil {
			return ErrRevisionCommandInProgress
		}
		if state.NormalLease != nil {
			return fmt.Errorf("normal flow is already leased by %q", state.NormalLease.Owner)
		}
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			return err
		}
		state.Generation++
		lease = &NormalFlowLease{
			Token: fmt.Sprintf("%x", tokenBytes), Generation: state.Generation,
			Owner: owner, PID: os.Getpid(), AcquiredAt: domain.RevisionTimestamp(),
		}
		state.NormalLease = lease
		return s.io.WriteJSON(revisionStateFile, state)
	})
	if err != nil {
		return nil, err
	}
	copy := *lease
	return &copy, nil
}

func (s *RevisionStore) ReleaseNormalFlow(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.NormalLease == nil {
			return nil
		}
		if state.NormalLease.Token != token {
			return fmt.Errorf("normal flow lease token is stale")
		}
		state.NormalLease = nil
		state.Generation++
		return s.io.WriteJSON(revisionStateFile, state)
	})
}

// DetachClonedNormalFlowLease removes process-local ownership copied from a
// source project while preserving the clone's durable revision history.
func (s *RevisionStore) DetachClonedNormalFlowLease() error {
	if s == nil || s.io == nil {
		return fmt.Errorf("revision store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if state.NormalLease == nil {
		return nil
	}
	state.NormalLease = nil
	state.Generation++
	if err := validateRevisionState(state); err != nil {
		return err
	}
	return s.io.WriteJSON(revisionStateFile, state)
}

func (s *RevisionStore) FenceForNormalFlow(token string) (RevisionFence, error) {
	var fence RevisionFence
	err := s.withRevisionTransaction(func() error {
		if pending, err := completionRevalidationPendingUnlocked(s.io); err != nil {
			return err
		} else if pending {
			return ErrCompletionRevalidationBlocksNormalFlow
		}
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.ActiveSessionID != "" || state.NormalLease == nil || state.NormalLease.Token != strings.TrimSpace(token) {
			return ErrActiveRevisionBlocksNormalFlow
		}
		fence = RevisionFence{Generation: state.Generation, LeaseToken: state.NormalLease.Token}
		return nil
	})
	return fence, err
}

func completionRevalidationPendingUnlocked(io *IO) (bool, error) {
	var progress domain.Progress
	if err := io.ReadJSONUnlocked("meta/progress.json", &progress); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	checkpoint := progress.CompletionRevalidation
	return checkpoint != nil && checkpoint.Status != "completed", nil
}

func (s *RevisionStore) SnapshotFence() (RevisionFence, error) {
	var fence RevisionFence
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		fence.Generation = state.Generation
		if state.ActiveSessionID != "" {
			session := state.Sessions[state.ActiveSessionID]
			fence.SessionID, fence.Revision = session.ID, session.Revision
		}
		if state.NormalLease != nil {
			fence.LeaseToken = state.NormalLease.Token
		}
		return nil
	})
	return fence, err
}

func (s *RevisionStore) WithFence(fence RevisionFence, fn func() error) error {
	// A normal-flow lease is itself the durable exclusion boundary: active
	// revision commands cannot begin while the lease remains current. Validate
	// it transactionally, then let each lowest-level formal writer acquire its
	// own revision transaction immediately before the side effect. Holding this
	// transaction across fn would make those guarded writers re-enter the same
	// non-reentrant project lock.
	if fence.LeaseToken != "" {
		if err := s.withRevisionTransaction(func() error {
			state, err := s.loadUnlocked()
			if err != nil {
				return err
			}
			if state.Generation != fence.Generation {
				return fmt.Errorf("revision generation fence is stale")
			}
			if state.ActiveSessionID != "" || state.NormalLease == nil || state.NormalLease.Token != fence.LeaseToken {
				return ErrActiveRevisionBlocksNormalFlow
			}
			return nil
		}); err != nil {
			return err
		}
		return fn()
	}

	return s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.Generation != fence.Generation {
			return fmt.Errorf("revision generation fence is stale")
		}
		session, exists := state.Sessions[fence.SessionID]
		if !exists || state.ActiveSessionID != fence.SessionID || session.Revision != fence.Revision {
			return ErrRevisionNotFound
		}
		return fn()
	})
}

func NewRevisionStore(dir string) *RevisionStore {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	lock, _ := revisionLocks.LoadOrStore(filepath.Clean(abs), &sync.Mutex{})
	return &RevisionStore{io: newIO(dir), mu: lock.(*sync.Mutex)}
}

func newRevisionCommandFence(key, operation, fingerprint, ownerToken string) (*revisionCommandFence, error) {
	fence := &revisionCommandFence{
		Key: strings.TrimSpace(key), Operation: strings.TrimSpace(operation), Fingerprint: strings.TrimSpace(fingerprint), OwnerToken: strings.TrimSpace(ownerToken),
	}
	if fence.Key == "" || fence.Operation == "" || fence.Fingerprint == "" {
		return nil, fmt.Errorf("revision command fence identity is required")
	}
	decodedToken, err := hex.DecodeString(fence.OwnerToken)
	if err != nil || len(decodedToken) != 32 {
		return nil, fmt.Errorf("revision command owner capability must be 256 bits")
	}
	return fence, nil
}

func revisionCommandMatches(fence *revisionCommandFence, key, operation, fingerprint string) bool {
	return fence != nil && fence.Key == strings.TrimSpace(key) && fence.Operation == strings.TrimSpace(operation) && fence.Fingerprint == strings.TrimSpace(fingerprint)
}

func revisionCommandOwnerMatches(fence *revisionCommandFence, owner *revisionCommandOwner) bool {
	return fence != nil && owner != nil && fence.Key == owner.Key && fence.Operation == owner.Operation &&
		fence.Fingerprint == owner.Fingerprint && fence.OwnerToken == owner.Token
}

func allowRevisionCommandMutation(state *revisionState, commandOwner *revisionCommandOwner, publicationOwner ...*RevisionPublicationOwner) error {
	if state == nil {
		return nil
	}
	if state.Publication != nil {
		if len(publicationOwner) == 1 && revisionPublicationOwnerMatches(state.Publication, publicationOwner[0]) {
			return nil
		}
		return ErrRevisionCommandInProgress
	}
	if len(publicationOwner) == 1 && publicationOwner[0] != nil {
		return ErrRevisionCommandInProgress
	}
	if state.CommandFence == nil {
		if commandOwner == nil {
			return nil
		}
		return ErrRevisionCommandInProgress
	}
	if revisionCommandOwnerMatches(state.CommandFence, commandOwner) {
		return nil
	}
	return ErrRevisionCommandInProgress
}

func (s *RevisionStore) claimCommandFence(key, operation, fingerprint string) (*RevisionStore, error) {
	key, operation, fingerprint = strings.TrimSpace(key), strings.TrimSpace(operation), strings.TrimSpace(fingerprint)
	if key == "" || operation == "" || fingerprint == "" {
		return nil, fmt.Errorf("revision command fence identity is required")
	}
	var owned *RevisionStore
	err := s.withRevisionTransaction(func() error {
		_, statErr := os.Stat(s.io.path(revisionStateFile))
		stateMissing := os.IsNotExist(statErr)
		if statErr != nil && !stateMissing {
			return statErr
		}
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.CommandFence != nil {
			if revisionCommandMatches(state.CommandFence, key, operation, fingerprint) {
				owned = s.withCommandOwner(state.CommandFence)
				return nil
			}
			return ErrRevisionCommandInProgress
		}
		if state.NormalLease != nil {
			return fmt.Errorf("%w: normal flow is still running", ErrRevisionCommandInProgress)
		}
		if manuscriptID, err := activeManuscriptRevisionID(s.io); err != nil {
			return err
		} else if manuscriptID != "" {
			return ErrRevisionCommandInProgress
		}
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err != nil {
			return err
		}
		fence, err := newRevisionCommandFence(key, operation, fingerprint, fmt.Sprintf("%x", tokenBytes))
		if err != nil {
			return err
		}
		if stateMissing {
			fence.RemoveEmptyStateOnRelease = true
		}
		state.CommandFence = fence
		if err := s.io.WriteJSON(revisionStateFile, state); err != nil {
			return err
		}
		owned = s.withCommandOwner(fence)
		return nil
	})
	return owned, err
}

func (s *RevisionStore) withCommandOwner(fence *revisionCommandFence) *RevisionStore {
	owned := *s
	owned.commandOwner = &revisionCommandOwner{Key: fence.Key, Operation: fence.Operation, Fingerprint: fence.Fingerprint, Token: fence.OwnerToken}
	return &owned
}

func (s *RevisionStore) requireCommandFence() error {
	return s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if !revisionCommandOwnerMatches(state.CommandFence, s.commandOwner) {
			return ErrRevisionCommandInProgress
		}
		return nil
	})
}

func (s *RevisionStore) requireCommandFenceFor(key, operation, fingerprint string) error {
	if s.commandOwner == nil || s.commandOwner.Key != strings.TrimSpace(key) ||
		s.commandOwner.Operation != strings.TrimSpace(operation) || s.commandOwner.Fingerprint != strings.TrimSpace(fingerprint) {
		return ErrRevisionCommandInProgress
	}
	return s.requireCommandFence()
}

func (s *RevisionStore) releaseCommandFence() error {
	return s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.CommandFence == nil {
			return nil
		}
		if !revisionCommandOwnerMatches(state.CommandFence, s.commandOwner) {
			return ErrRevisionCommandInProgress
		}
		removeEmptyState := state.CommandFence.RemoveEmptyStateOnRelease
		state.CommandFence = nil
		if removeEmptyState && revisionStateIsEmpty(state) {
			return s.io.RemoveFile(revisionStateFile)
		}
		return s.io.WriteJSON(revisionStateFile, state)
	})
}

func revisionStateIsEmpty(state *revisionState) bool {
	return state != nil && state.Version == domain.RevisionSchemaVersion && state.Generation == 1 &&
		state.NormalLease == nil && state.CommandFence == nil && state.Publication == nil && state.ActiveSessionID == "" &&
		state.NextSession == 0 && state.NextVersion == 0 && len(state.Sessions) == 0 &&
		len(state.Versions) == 0 && len(state.CurrentArtifacts) == 0 && len(state.Receipts) == 0
}

func (s *RevisionStore) currentCommandFence() (*revisionCommandFence, error) {
	var result *revisionCommandFence
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.CommandFence != nil {
			copy := *state.CommandFence
			result = &copy
		}
		return nil
	})
	return result, err
}

func (s *RevisionStore) currentCommandOwner() (*RevisionStore, error) {
	fence, err := s.currentCommandFence()
	if err != nil || fence == nil {
		return nil, err
	}
	return s.withCommandOwner(fence), nil
}

type StartRevisionInput struct {
	Intent           string
	Impact           domain.RevisionImpact
	PreviewSignature string
	IdempotencyKey   string
}

type CandidateArtifactInput struct {
	ArtifactID   string
	ArtifactKind string
	Payload      json.RawMessage
}

type SubmitRevisionCandidateInput struct {
	SessionID        string
	ExpectedRevision int
	IdempotencyKey   string
	Artifacts        []CandidateArtifactInput
}

type RevisionMutationInput struct {
	SessionID        string
	ExpectedRevision int
	IdempotencyKey   string
}

type RebindRevisionPreviewInput struct {
	RevisionMutationInput
	PreviousSignature string
	NextSignature     string
}

// RebindPreviewAfterFeedback replaces only the sealed generator input after a
// signed audit failure and explicit user feedback. It cannot change impact,
// approvals, candidate versions, or any non-generating revision.
func (s *RevisionStore) RebindPreviewAfterFeedback(policy domain.RevisionPolicy, input RebindRevisionPreviewInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input.RevisionMutationInput, "rebind_preview_after_feedback", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageCandidateGenerating || len(session.Feedback) == 0 {
			return fmt.Errorf("revision preview can only be rebound after audit feedback")
		}
		if strings.TrimSpace(input.PreviousSignature) == "" || session.PreviewSignature != strings.TrimSpace(input.PreviousSignature) || strings.TrimSpace(input.NextSignature) == "" || input.NextSignature == input.PreviousSignature {
			return fmt.Errorf("revision preview feedback rebind signature mismatch")
		}
		session.PreviewSignature = strings.TrimSpace(input.NextSignature)
		return nil
	})
}

type RevisionAuditInput struct {
	RevisionMutationInput
	CandidateSignature string
	Passed             bool
	Report             string
	Evidence           []domain.RevisionAuditEvidence
}

type RevisionFeedbackInput struct {
	RevisionMutationInput
	StageID         string
	ImpactSignature string
	Message         string
}

type RevisionApprovalInput struct {
	RevisionMutationInput
	StageID string
}

type RevisionFailureInput struct {
	RevisionMutationInput
	Error string
}

// RevisionPublicationOwner is an opaque capability minted only after the
// RevisionStore has validated the exact active revision and its publish gates.
// Formal Store writers revalidate it while holding the revision transaction.
type RevisionPublicationOwner struct {
	revisions          *RevisionStore
	policy             domain.RevisionPolicy
	sessionID          string
	expectedRevision   int
	mode               domain.RevisionMode
	policyID           string
	policyVersion      string
	generation         uint64
	token              string
	publishKey         string
	publishFingerprint string
	acceptedVersionIDs []string
	acceptedDigest     string
	candidateDigest    string
}

func revisionPublicationOwnerMatches(attempt *revisionPublicationAttempt, owner *RevisionPublicationOwner) bool {
	return attempt != nil && owner != nil && attempt.Token == owner.token &&
		attempt.SessionID == owner.sessionID && attempt.ExpectedRevision == owner.expectedRevision &&
		attempt.Generation == owner.generation && attempt.Mode == owner.mode &&
		attempt.PolicyID == owner.policyID && attempt.PolicyVersion == owner.policyVersion &&
		attempt.PublishKey == owner.publishKey &&
		attempt.PublishFingerprint == owner.publishFingerprint &&
		attempt.AcceptedDigest == owner.acceptedDigest && attempt.CandidateDigest == owner.candidateDigest &&
		slices.Equal(attempt.AcceptedVersionIDs, owner.acceptedVersionIDs)
}

func revisionPublishFingerprint(input RevisionMutationInput) (string, error) {
	_, fingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, "publish", input)
	return fingerprint, err
}

func publicationAcceptedVersions(state *revisionState, session *domain.RevisionSession) ([]domain.ArtifactVersion, error) {
	versionIDs := append(append([]string(nil), session.AcceptedVersionIDs...), session.CandidateVersionIDs...)
	versions := make([]domain.ArtifactVersion, 0, len(versionIDs))
	for _, versionID := range versionIDs {
		version, exists := state.Versions[versionID]
		if !exists || version.ParentVersionID != state.CurrentArtifacts[version.ArtifactID] {
			return nil, fmt.Errorf("accepted candidate version %q is stale or missing", versionID)
		}
		if err := version.Validate(); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, nil
}

func canonicalNormalCandidateDigest(versions []domain.ArtifactVersion) (string, error) {
	var candidate []domain.VolumeOutline
	found := false
	for _, version := range versions {
		if version.ArtifactID != domain.NormalStructureSnapshotID || version.ArtifactKind != domain.NormalArtifactStructureSnapshot {
			continue
		}
		if found {
			return "", fmt.Errorf("normal publication contains multiple canonical structure snapshots")
		}
		if err := json.Unmarshal(version.Payload, &candidate); err != nil {
			return "", fmt.Errorf("decode canonical normal structure snapshot: %w", err)
		}
		found = true
	}
	if !found {
		return "", fmt.Errorf("normal publication is missing its canonical structure snapshot")
	}
	return normalStructureDigest(candidate)
}

func latestNormalStructureDigest(versions []domain.ArtifactVersion) (string, error) {
	for index := len(versions) - 1; index >= 0; index-- {
		version := versions[index]
		if version.ArtifactID != domain.NormalStructureSnapshotID || version.ArtifactKind != domain.NormalArtifactStructureSnapshot {
			continue
		}
		var candidate []domain.VolumeOutline
		if err := json.Unmarshal(version.Payload, &candidate); err != nil {
			return "", err
		}
		return normalStructureDigest(candidate)
	}
	return "", fmt.Errorf("normal publication is missing its canonical structure snapshot")
}

func normalStructureDigest(candidate []domain.VolumeOutline) (string, error) {
	if err := domain.ValidateStructureSnapshot(candidate); err != nil {
		return "", err
	}
	payload, err := json.Marshal(domain.CloneStructureSnapshot(candidate))
	if err != nil {
		return "", err
	}
	return domain.JSONContentSignature(payload), nil
}

func publicationBinding(state *revisionState, session *domain.RevisionSession, canonical []domain.ArtifactVersion) ([]string, string, string, error) {
	accepted, err := publicationAcceptedVersions(state, session)
	if err != nil {
		return nil, "", "", err
	}
	ids := make([]string, 0, len(accepted))
	for _, version := range accepted {
		ids = append(ids, version.ID)
	}
	candidateDigest := ""
	if session.Mode == domain.RevisionModeNormal {
		candidateDigest, err = canonicalNormalCandidateDigest(canonical)
		if err != nil {
			return nil, "", "", err
		}
	}
	return ids, domain.CandidateSignature(accepted), candidateDigest, nil
}

func newRevisionPublicationToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

type RestoreArtifactVersionInput struct {
	VersionID      string
	Intent         string
	IdempotencyKey string
}

func (s *RevisionStore) Start(policy domain.RevisionPolicy, input StartRevisionInput) (*domain.RevisionSession, error) {
	intent := strings.TrimSpace(input.Intent)
	if intent == "" {
		return nil, fmt.Errorf("revision intent is required")
	}
	impact, err := normalizeRevisionImpact(input.Impact)
	if err != nil {
		return nil, err
	}
	payload := struct {
		Intent           string
		Impact           domain.RevisionImpact
		PreviewSignature string
	}{intent, impact, strings.TrimSpace(input.PreviewSignature)}
	operation, fingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, "start", payload)
	if err != nil {
		return nil, err
	}
	if receipt, err := s.lookupRevisionReceipt(input.IdempotencyKey, operation, fingerprint); receipt != nil || err != nil {
		return receipt, err
	}
	policyMode, policyID, policyVersion, err := describeRevisionPolicy(policy)
	if err != nil {
		return nil, err
	}
	var working *revisionState
	var generation uint64
	var receipt *domain.RevisionSession
	err = s.withRevisionTransaction(func() error {
		state, loadErr := s.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		if err := allowRevisionCommandMutation(state, s.commandOwner, s.publicationOwner); err != nil {
			return err
		}
		if found, receiptErr := matchingRevisionReceipt(state, s.commandOwner, input.IdempotencyKey, operation, fingerprint); found != nil || receiptErr != nil {
			receipt, err = found, receiptErr
			return err
		}
		if state.ActiveSessionID != "" {
			return ErrActiveRevisionExists
		}
		if manuscriptID, loadErr := activeManuscriptRevisionID(s.io); loadErr != nil {
			return loadErr
		} else if manuscriptID != "" {
			return ErrActiveRevisionExists
		}
		if state.NormalLease != nil {
			return fmt.Errorf("%w: normal flow is still running", ErrActiveRevisionExists)
		}
		generation = state.Generation
		working, err = cloneRevisionState(state)
		return err
	})
	if err != nil || receipt != nil {
		return receipt, err
	}
	// Policy code is deliberately outside both the process and filesystem locks.
	if err := policy.ValidateImpact(cloneRevisionImpact(impact)); err != nil {
		return nil, fmt.Errorf("validate revision impact: %w", err)
	}
	stages, err := validateApprovalStages(policy, cloneRevisionImpact(impact))
	if err != nil {
		return nil, err
	}
	now := domain.RevisionTimestamp()
	working.NextSession++
	working.Generation++
	session := domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: fmt.Sprintf("rev-%06d", working.NextSession), Mode: policyMode,
		Stage: domain.RevisionStageImpactReviewPending, Revision: 1, Generation: working.Generation,
		PolicyID: policyID, PolicyVersion: policyVersion, Intent: intent, Impact: impact,
		PreviewSignature: strings.TrimSpace(input.PreviewSignature),
		ApprovalStages:   stages, Round: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := applyRevisionRoute(policy, &session); err != nil {
		return nil, err
	}
	working.Sessions[session.ID] = session
	working.ActiveSessionID = session.ID
	return s.commitOptimistic(input.IdempotencyKey, operation, fingerprint, generation, working, session)
}

func (s *RevisionStore) ApproveImpact(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "approve_impact", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageImpactReviewPending {
			return fmt.Errorf("revision impact can only be approved from %q", domain.RevisionStageImpactReviewPending)
		}
		if len(session.CandidateVersionIDs) > 0 {
			session.Stage = domain.RevisionStageCandidateAudit
		} else {
			session.Stage = domain.RevisionStageCandidateGenerating
		}
		return nil
	})
}

func (s *RevisionStore) SubmitCandidate(policy domain.RevisionPolicy, input SubmitRevisionCandidateInput) (*domain.RevisionSession, error) {
	if len(input.Artifacts) == 0 {
		return nil, fmt.Errorf("revision candidate must contain at least one artifact")
	}
	payload := input
	return s.mutate(policy, input.RevisionMutationInput(), "submit_candidate", payload, func(state *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageCandidateGenerating {
			return fmt.Errorf("revision candidate can only be submitted from %q", domain.RevisionStageCandidateGenerating)
		}
		impactKinds := make(map[string]string, len(session.Impact.Items))
		for _, item := range session.Impact.Items {
			impactKinds[item.ArtifactID] = item.ArtifactKind
		}
		seen := make(map[string]struct{}, len(input.Artifacts))
		versions := make([]domain.ArtifactVersion, 0, len(input.Artifacts))
		for _, artifact := range input.Artifacts {
			artifact.ArtifactID = strings.TrimSpace(artifact.ArtifactID)
			artifact.ArtifactKind = strings.TrimSpace(artifact.ArtifactKind)
			kind, inImpact := impactKinds[artifact.ArtifactID]
			if !inImpact || kind != artifact.ArtifactKind {
				return fmt.Errorf("candidate artifact %q is outside the approved impact", artifact.ArtifactID)
			}
			if _, duplicate := seen[artifact.ArtifactID]; duplicate {
				return fmt.Errorf("duplicate candidate artifact %q", artifact.ArtifactID)
			}
			seen[artifact.ArtifactID] = struct{}{}
			if len(artifact.Payload) == 0 || !json.Valid(artifact.Payload) {
				return fmt.Errorf("candidate artifact %q payload must be valid JSON", artifact.ArtifactID)
			}
			state.NextVersion++
			version := domain.ArtifactVersion{
				ID:         fmt.Sprintf("artifact-version-%06d", state.NextVersion),
				ArtifactID: artifact.ArtifactID, ArtifactKind: artifact.ArtifactKind,
				RevisionID: session.ID, ParentVersionID: state.CurrentArtifacts[artifact.ArtifactID],
				Sequence: nextArtifactSequence(state, artifact.ArtifactID), Round: session.Round,
				Payload:          append(json.RawMessage(nil), artifact.Payload...),
				ContentSignature: domain.JSONContentSignature(artifact.Payload), CreatedAt: domain.RevisionTimestamp(),
			}
			versions = append(versions, version)
		}
		policySession, err := cloneRevisionSession(*session)
		if err != nil {
			return err
		}
		canonicalVersions := cloneArtifactVersions(versions)
		policyVersions := cloneArtifactVersions(canonicalVersions)
		if err := policy.ValidateCandidate(*policySession, policyVersions); err != nil {
			return fmt.Errorf("validate revision candidate: %w", err)
		}
		var expectations []domain.RevisionAuditExpectation
		if scoped, ok := policy.(domain.ScopedAuditPolicy); ok {
			expectations, err = scoped.AuditExpectations(*policySession, cloneArtifactVersions(canonicalVersions))
			if err != nil {
				return fmt.Errorf("derive revision audit expectations: %w", err)
			}
			for _, expectation := range expectations {
				if err := expectation.Validate(); err != nil {
					return err
				}
			}
		}
		for _, version := range canonicalVersions {
			state.Versions[version.ID] = version
			session.CandidateVersionIDs = append(session.CandidateVersionIDs, version.ID)
		}
		session.CandidateSignature = domain.CandidateSignature(canonicalVersions)
		session.AuditExpectations = append([]domain.RevisionAuditExpectation(nil), expectations...)
		session.Stage = domain.RevisionStageCandidateAudit
		return nil
	})
}

func (input SubmitRevisionCandidateInput) RevisionMutationInput() RevisionMutationInput {
	return RevisionMutationInput{input.SessionID, input.ExpectedRevision, input.IdempotencyKey}
}

func (s *RevisionStore) RecordAudit(policy domain.RevisionPolicy, input RevisionAuditInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input.RevisionMutationInput, "record_audit", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageCandidateAudit {
			return fmt.Errorf("revision audit can only be recorded from %q", domain.RevisionStageCandidateAudit)
		}
		if strings.TrimSpace(input.CandidateSignature) == "" || input.CandidateSignature != session.CandidateSignature {
			return fmt.Errorf("revision audit candidate signature mismatch")
		}
		if signed, ok := policy.(domain.SignedAuditSetPolicy); ok {
			if err := signed.ValidateAuditSet(*session, append([]domain.RevisionAuditEvidence(nil), input.Evidence...)); err != nil {
				return fmt.Errorf("validate signed revision audit set: %w", err)
			}
			input.Passed = true
			for _, evidence := range input.Evidence {
				if !evidence.Passed {
					input.Passed = false
					break
				}
			}
		}
		now := domain.RevisionTimestamp()
		if len(input.Evidence) == 0 {
			session.Audits = append(session.Audits, domain.RevisionAudit{
				Round: session.Round, CandidateSignature: session.CandidateSignature,
				Passed: input.Passed, Report: strings.TrimSpace(input.Report), CreatedAt: now,
			})
		} else {
			for _, evidence := range input.Evidence {
				session.Audits = append(session.Audits, domain.RevisionAudit{
					Round: session.Round, CandidateSignature: session.CandidateSignature,
					Scope: evidence.Scope, ScopeID: evidence.ScopeID, FromChapter: evidence.FromChapter,
					ToChapter: evidence.ToChapter, ContentSignature: evidence.ContentSignature,
					Passed: evidence.Passed, Report: strings.TrimSpace(evidence.Report), CreatedAt: now,
				})
			}
		}
		if input.Passed {
			session.Stage = domain.RevisionStageApprovalPending
			return nil
		}
		session.Round++
		session.Stage = domain.RevisionStageCandidateGenerating
		session.CandidateVersionIDs = nil
		session.CandidateSignature = ""
		session.AuditExpectations = nil
		if _, staged := policy.(domain.StagedRevisionPolicy); !staged {
			session.Approvals = nil
		}
		return nil
	})
}

func (s *RevisionStore) SubmitFeedback(policy domain.RevisionPolicy, input RevisionFeedbackInput) (*domain.RevisionSession, error) {
	message := strings.TrimSpace(input.Message)
	if message == "" {
		return nil, fmt.Errorf("revision feedback is required")
	}
	return s.mutate(policy, input.RevisionMutationInput, "submit_feedback", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if _, signed := policy.(domain.SignedAuditSetPolicy); signed &&
			(strings.TrimSpace(input.ImpactSignature) == "" || input.ImpactSignature != session.Impact.Signature) {
			return fmt.Errorf("revision feedback target drift requires a new impact preview")
		}
		if session.Stage != domain.RevisionStageCandidateGenerating && session.Stage != domain.RevisionStageCandidateAudit && session.Stage != domain.RevisionStageApprovalPending {
			return fmt.Errorf("revision feedback is not allowed from stage %q", session.Stage)
		}
		if session.Stage == domain.RevisionStageApprovalPending {
			current := session.CurrentApprovalStage()
			if current == nil || (strings.TrimSpace(input.StageID) != "" && input.StageID != current.ID) {
				return fmt.Errorf("revision feedback stage does not match the pending approval")
			}
		}
		session.Feedback = append(session.Feedback, domain.RevisionFeedback{
			Round: session.Round, StageID: strings.TrimSpace(input.StageID), Message: message, CreatedAt: domain.RevisionTimestamp(),
		})
		session.Round++
		session.Stage = domain.RevisionStageCandidateGenerating
		session.CandidateVersionIDs = nil
		session.CandidateSignature = ""
		session.AuditExpectations = nil
		if _, staged := policy.(domain.StagedRevisionPolicy); !staged {
			session.Approvals = nil
		}
		return nil
	})
}

func (s *RevisionStore) ApproveStage(policy domain.RevisionPolicy, input RevisionApprovalInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input.RevisionMutationInput, "approve_stage", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStageApprovalPending || !session.LatestAuditPassed() {
			return fmt.Errorf("revision candidate must pass audit before approval")
		}
		stage := session.CurrentApprovalStage()
		if stage == nil || input.StageID != stage.ID {
			return fmt.Errorf("revision approval must follow the configured stage order")
		}
		session.Approvals = append(session.Approvals, domain.RevisionApproval{StageID: stage.ID, ApprovedAt: domain.RevisionTimestamp()})
		if len(session.Approvals) == len(session.ApprovalStages) {
			session.Stage = domain.RevisionStageReadyToPublish
			return nil
		}
		if staged, ok := policy.(domain.StagedRevisionPolicy); ok && staged.ContinueAfterApproval(*session, *stage) {
			session.AcceptedVersionIDs = append(session.AcceptedVersionIDs, session.CandidateVersionIDs...)
			session.Round++
			session.Stage = domain.RevisionStageCandidateGenerating
			session.CandidateVersionIDs = nil
			session.CandidateSignature = ""
			session.AuditExpectations = nil
		}
		return nil
	})
}

func (s *RevisionStore) Publish(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.publish(policy, input)
}

// PublishWithOwner completes the exact durable normal-publication attempt.
// Other revision modes keep using Publish because their formal transaction is
// fenced by the adaptation command owner instead.
func (s *RevisionStore) PublishWithOwner(policy domain.RevisionPolicy, input RevisionMutationInput, owner *RevisionPublicationOwner) (*domain.RevisionSession, error) {
	if s == nil || owner == nil || owner.revisions == nil || !revisionStoresShareProject(s, owner.revisions) {
		return nil, ErrRevisionCommandInProgress
	}
	owned := *s
	owned.publicationOwner = owner
	return owned.publish(policy, input)
}

// FinalizeCommittedPublication replays only an already committed publish
// receipt. It never starts a new publication, so service layers may safely call
// it before their ordinary active-session validation after a finalize fault.
func (s *RevisionStore) FinalizeCommittedPublication(input RevisionMutationInput) (*domain.RevisionSession, bool, error) {
	command, fingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, "publish", input)
	if err != nil {
		return nil, false, err
	}
	receipt, err := s.lookupRevisionReceipt(input.IdempotencyKey, command, fingerprint)
	if err != nil || receipt == nil {
		return receipt, false, err
	}
	if validExpansionPublicationPolicy(*receipt) {
		if err := s.withRevisionTransaction(func() error {
			return finalizeExpansionAuthorityCreationForOutput(s.io.dir)
		}); err != nil {
			return receipt, true, fmt.Errorf("finalize committed expansion authority creation: %w", err)
		}
	}
	return receipt, true, nil
}

// CommittedPublicationNeedsFinalize reports whether the current project still
// has a durable authority-creation journal after its revision receipt commit.
func (s *RevisionStore) CommittedPublicationNeedsFinalize() (bool, error) {
	if s == nil || s.io == nil {
		return false, fmt.Errorf("revision store is required")
	}
	return expansionAuthorityCreationNeedsFinalize(s.io.dir)
}

func (s *RevisionStore) publish(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "publish", input, func(state *revisionState, session *domain.RevisionSession) error {
		attempt, err := s.requireNormalPublicationAttempt(input, state, session)
		if err != nil {
			return err
		}
		versions, err := prepareRevisionPublish(policy, state, session)
		if err != nil {
			return err
		}
		if err := s.revalidateNormalPublicationBinding(attempt, state, session, versions); err != nil {
			return err
		}
		for _, version := range versions {
			state.CurrentArtifacts[version.ArtifactID] = version.ID
		}
		now := domain.RevisionTimestamp()
		session.Stage = domain.RevisionStageCompleted
		session.CompletedAt = now
		state.ActiveSessionID = ""
		state.Publication = nil
		return nil
	})
}

func (s *RevisionStore) requireNormalPublicationAttempt(input RevisionMutationInput, state *revisionState, session *domain.RevisionSession) (*revisionPublicationAttempt, error) {
	if session.Mode != domain.RevisionModeNormal {
		return nil, nil
	}
	attempt := state.Publication
	if attempt == nil || attempt.Status != revisionPublicationApplied ||
		!revisionPublicationOwnerMatches(attempt, s.publicationOwner) {
		return nil, ErrRevisionCommandInProgress
	}
	publishFingerprint, err := revisionPublishFingerprint(input)
	if err != nil {
		return nil, err
	}
	if attempt.SessionID != strings.TrimSpace(input.SessionID) ||
		attempt.ExpectedRevision != input.ExpectedRevision ||
		attempt.PublishKey != strings.TrimSpace(input.IdempotencyKey) ||
		attempt.PublishFingerprint != publishFingerprint {
		return nil, ErrRevisionCommandInProgress
	}
	return attempt, nil
}

func (s *RevisionStore) revalidateNormalPublicationBinding(
	attempt *revisionPublicationAttempt,
	state *revisionState,
	session *domain.RevisionSession,
	versions []domain.ArtifactVersion,
) error {
	if session.Mode != domain.RevisionModeNormal {
		return nil
	}
	if attempt == nil {
		return ErrRevisionCommandInProgress
	}
	ids, acceptedDigest, candidateDigest, err := publicationBinding(state, session, versions)
	if err != nil {
		return err
	}
	if !slices.Equal(ids, attempt.AcceptedVersionIDs) ||
		acceptedDigest != attempt.AcceptedDigest || candidateDigest != attempt.CandidateDigest {
		return fmt.Errorf("normal publication accepted artifacts changed")
	}
	var formalStructure []domain.VolumeOutline
	if err := s.io.ReadJSON("layered_outline.json", &formalStructure); err != nil {
		return fmt.Errorf("read normal publication formal structure: %w", err)
	}
	formalDigest, err := normalStructureDigest(formalStructure)
	if err != nil {
		return fmt.Errorf("validate normal publication formal structure: %w", err)
	}
	if formalDigest != candidateDigest {
		return fmt.Errorf("normal publication formal structure changed")
	}
	return nil
}

// ValidatePublish performs every RevisionStore policy/version/generation check
// without mutating either formal artifacts or revision state. Production
// publication must call this before any formal structure write.
func (s *RevisionStore) ValidatePublish(policy domain.RevisionPolicy, input RevisionMutationInput) ([]domain.ArtifactVersion, error) {
	versions, _, err := s.ValidatePublishWithOwner(policy, input)
	return versions, err
}

// ValidatePublishWithOwner returns the accepted versions together with the
// opaque capability required by formal Store publication and rollback APIs.
func (s *RevisionStore) ValidatePublishWithOwner(policy domain.RevisionPolicy, input RevisionMutationInput) ([]domain.ArtifactVersion, *RevisionPublicationOwner, error) {
	var versions []domain.ArtifactVersion
	var owner *RevisionPublicationOwner
	publishFingerprint, err := revisionPublishFingerprint(input)
	if err != nil {
		return nil, nil, err
	}
	err = s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.ActiveSessionID != strings.TrimSpace(input.SessionID) {
			return ErrRevisionNotFound
		}
		if state.Publication == nil {
			if err := allowRevisionCommandMutation(state, s.commandOwner, s.publicationOwner); err != nil {
				return err
			}
		}
		session := state.Sessions[state.ActiveSessionID]
		if input.ExpectedRevision != session.Revision {
			return &RevisionConflictError{Expected: input.ExpectedRevision, Actual: session.Revision}
		}
		mode, id, version, err := describeRevisionPolicy(policy)
		if err != nil {
			return err
		}
		if session.Mode != mode || session.PolicyID != id || session.PolicyVersion != version {
			return fmt.Errorf("revision publish policy drift")
		}
		versions, err = prepareRevisionPublish(policy, state, &session)
		if err != nil {
			return err
		}
		ids, acceptedDigest, candidateDigest, err := publicationBinding(state, &session, versions)
		if err != nil {
			return err
		}
		token := ""
		generation := state.Generation
		if state.Publication != nil {
			attempt := state.Publication
			if attempt.SessionID != session.ID || attempt.ExpectedRevision != session.Revision ||
				attempt.Mode != mode || attempt.PolicyID != id || attempt.PolicyVersion != version ||
				attempt.PublishKey != strings.TrimSpace(input.IdempotencyKey) ||
				attempt.PublishFingerprint != publishFingerprint || !slices.Equal(attempt.AcceptedVersionIDs, ids) ||
				attempt.AcceptedDigest != acceptedDigest || attempt.CandidateDigest != candidateDigest {
				return ErrRevisionCommandInProgress
			}
			token, generation = attempt.Token, attempt.Generation
		} else {
			token, err = newRevisionPublicationToken()
			if err != nil {
				return err
			}
		}
		owner = &RevisionPublicationOwner{
			revisions: s, policy: policy, sessionID: session.ID, expectedRevision: session.Revision,
			mode: mode, policyID: id, policyVersion: version, generation: generation, token: token,
			publishKey:         strings.TrimSpace(input.IdempotencyKey),
			publishFingerprint: publishFingerprint, acceptedVersionIDs: ids,
			acceptedDigest: acceptedDigest, candidateDigest: candidateDigest,
		}
		return nil
	})
	return cloneArtifactVersions(versions), owner, err
}

func prepareRevisionPublish(policy domain.RevisionPolicy, state *revisionState, session *domain.RevisionSession) ([]domain.ArtifactVersion, error) {
	if session.Stage != domain.RevisionStageReadyToPublish || !session.LatestAuditPassed() || len(session.CandidateVersionIDs) == 0 {
		return nil, fmt.Errorf("revision is not ready to publish")
	}
	if err := session.Impact.Validate(); err != nil {
		return nil, fmt.Errorf("revalidate revision impact: %w", err)
	}
	if err := policy.ValidateImpact(cloneRevisionImpact(session.Impact)); err != nil {
		return nil, fmt.Errorf("revalidate revision impact policy: %w", err)
	}
	currentStages, err := validateApprovalStages(policy, cloneRevisionImpact(session.Impact))
	if err != nil {
		return nil, err
	}
	if !approvalStagesEqual(currentStages, session.ApprovalStages) {
		return nil, fmt.Errorf("revision approval stages changed before publish")
	}
	if len(session.Approvals) != len(session.ApprovalStages) {
		return nil, fmt.Errorf("revision is missing ordered approvals")
	}
	currentVersions := make([]domain.ArtifactVersion, 0, len(session.CandidateVersionIDs))
	for _, versionID := range session.CandidateVersionIDs {
		version, exists := state.Versions[versionID]
		if !exists || version.ParentVersionID != state.CurrentArtifacts[version.ArtifactID] || version.Round != session.Round {
			return nil, fmt.Errorf("current candidate version %q is stale or missing", versionID)
		}
		currentVersions = append(currentVersions, version)
	}
	if domain.CandidateSignature(currentVersions) != session.CandidateSignature {
		return nil, fmt.Errorf("revision candidate signature changed before publish")
	}
	versionIDs := append(append([]string(nil), session.AcceptedVersionIDs...), session.CandidateVersionIDs...)
	versionsByArtifact := make(map[string]domain.ArtifactVersion, len(versionIDs))
	artifactOrder := make([]string, 0, len(versionIDs))
	for _, versionID := range versionIDs {
		version, exists := state.Versions[versionID]
		if !exists || version.ParentVersionID != state.CurrentArtifacts[version.ArtifactID] {
			return nil, fmt.Errorf("accepted candidate version %q is stale or missing", versionID)
		}
		if err := version.Validate(); err != nil {
			return nil, err
		}
		if _, exists := versionsByArtifact[version.ArtifactID]; !exists {
			artifactOrder = append(artifactOrder, version.ArtifactID)
		}
		versionsByArtifact[version.ArtifactID] = version
	}
	versions := make([]domain.ArtifactVersion, 0, len(artifactOrder))
	for _, artifactID := range artifactOrder {
		versions = append(versions, versionsByArtifact[artifactID])
	}
	policySession, err := cloneRevisionSession(*session)
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateCandidate(*policySession, cloneArtifactVersions(versions)); err != nil {
		return nil, fmt.Errorf("revalidate revision candidate: %w", err)
	}
	return versions, nil
}

func (s *RevisionStore) Pause(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "pause", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage == domain.RevisionStagePaused || session.Stage == domain.RevisionStageFailed {
			return fmt.Errorf("revision is already interrupted")
		}
		session.ResumeStage = session.Stage
		session.Stage = domain.RevisionStagePaused
		return nil
	})
}

func (s *RevisionStore) Fail(policy domain.RevisionPolicy, input RevisionFailureInput) (*domain.RevisionSession, error) {
	message := strings.TrimSpace(input.Error)
	if message == "" {
		return nil, fmt.Errorf("revision failure error is required")
	}
	return s.mutate(policy, input.RevisionMutationInput, "fail", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage == domain.RevisionStagePaused || session.Stage == domain.RevisionStageFailed {
			return fmt.Errorf("revision is already interrupted")
		}
		session.ResumeStage = session.Stage
		session.Stage = domain.RevisionStageFailed
		session.LastError = message
		return nil
	})
}

func (s *RevisionStore) Resume(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "resume", input, func(_ *revisionState, session *domain.RevisionSession) error {
		if session.Stage != domain.RevisionStagePaused && session.Stage != domain.RevisionStageFailed {
			return fmt.Errorf("revision is not paused or failed")
		}
		if !session.ResumeStage.Valid() || session.ResumeStage.Terminal() {
			return fmt.Errorf("revision resume stage is invalid")
		}
		session.Stage = session.ResumeStage
		session.ResumeStage = ""
		session.LastError = ""
		return nil
	})
}

func (s *RevisionStore) Cancel(policy domain.RevisionPolicy, input RevisionMutationInput) (*domain.RevisionSession, error) {
	return s.mutate(policy, input, "cancel", input, func(state *revisionState, session *domain.RevisionSession) error {
		session.Stage = domain.RevisionStageCancelled
		session.ResumeStage = ""
		session.Route = nil
		state.ActiveSessionID = ""
		return nil
	})
}

func (s *RevisionStore) RestoreVersion(policy domain.RevisionPolicy, input RestoreArtifactVersionInput) (*domain.RevisionSession, error) {
	intent := strings.TrimSpace(input.Intent)
	if intent == "" {
		return nil, fmt.Errorf("restore intent is required")
	}
	versionID := strings.TrimSpace(input.VersionID)
	payload := struct {
		VersionID string
		Intent    string
	}{versionID, intent}
	operation, fingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, "restore_version", payload)
	if err != nil {
		return nil, err
	}
	if receipt, err := s.lookupRevisionReceipt(input.IdempotencyKey, operation, fingerprint); receipt != nil || err != nil {
		return receipt, err
	}
	policyMode, policyID, policyVersion, err := describeRevisionPolicy(policy)
	if err != nil {
		return nil, err
	}
	var working *revisionState
	var source domain.ArtifactVersion
	var generation uint64
	var receipt *domain.RevisionSession
	err = s.withRevisionTransaction(func() error {
		state, loadErr := s.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		if err := allowRevisionCommandMutation(state, s.commandOwner, s.publicationOwner); err != nil {
			return err
		}
		if found, receiptErr := matchingRevisionReceipt(state, s.commandOwner, input.IdempotencyKey, operation, fingerprint); found != nil || receiptErr != nil {
			receipt, err = found, receiptErr
			return err
		}
		if state.ActiveSessionID != "" {
			return ErrActiveRevisionExists
		}
		if state.NormalLease != nil {
			return fmt.Errorf("%w: normal flow is still running", ErrActiveRevisionExists)
		}
		var exists bool
		source, exists = state.Versions[versionID]
		if !exists {
			return fmt.Errorf("artifact version %q is not found", versionID)
		}
		source.Payload = append(json.RawMessage(nil), source.Payload...)
		generation = state.Generation
		working, err = cloneRevisionState(state)
		return err
	})
	if err != nil || receipt != nil {
		return receipt, err
	}
	impact, err := domain.NewRevisionImpact("Restore a historical artifact version through a new revision", []domain.RevisionImpactItem{{
		ArtifactID: source.ArtifactID, ArtifactKind: source.ArtifactKind,
		Change: "restore historical version " + source.ID, DependencyEvidence: []string{source.ContentSignature},
	}})
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateImpact(cloneRevisionImpact(impact)); err != nil {
		return nil, fmt.Errorf("validate restore impact: %w", err)
	}
	stages, err := validateApprovalStages(policy, impact)
	if err != nil {
		return nil, err
	}
	working.NextSession++
	working.NextVersion++
	working.Generation++
	now := domain.RevisionTimestamp()
	session := domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: fmt.Sprintf("rev-%06d", working.NextSession), Mode: policyMode,
		Stage: domain.RevisionStageImpactReviewPending, Revision: 1, Generation: working.Generation,
		PolicyID: policyID, PolicyVersion: policyVersion, Intent: intent, Impact: impact,
		ApprovalStages: stages, Round: 1, RestoresVersionID: source.ID, CreatedAt: now, UpdatedAt: now,
	}
	candidate := source
	candidate.ID = fmt.Sprintf("artifact-version-%06d", working.NextVersion)
	candidate.RevisionID = session.ID
	candidate.ParentVersionID = working.CurrentArtifacts[source.ArtifactID]
	candidate.Sequence = nextArtifactSequence(working, source.ArtifactID)
	candidate.Round, candidate.CreatedAt = 1, now
	policySession, err := cloneRevisionSession(session)
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateCandidate(*policySession, cloneArtifactVersions([]domain.ArtifactVersion{candidate})); err != nil {
		return nil, fmt.Errorf("validate restore candidate: %w", err)
	}
	working.Versions[candidate.ID] = candidate
	session.CandidateVersionIDs = []string{candidate.ID}
	session.CandidateSignature = domain.CandidateSignature([]domain.ArtifactVersion{candidate})
	if err := applyRevisionRoute(policy, &session); err != nil {
		return nil, err
	}
	working.Sessions[session.ID] = session
	working.ActiveSessionID = session.ID
	return s.commitOptimistic(input.IdempotencyKey, operation, fingerprint, generation, working, session)
}

func (s *RevisionStore) Active() (*domain.RevisionSession, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	if state.ActiveSessionID == "" {
		return nil, nil
	}
	session, exists := state.Sessions[state.ActiveSessionID]
	if !exists || !session.Active() {
		return nil, fmt.Errorf("revision active-session index is invalid")
	}
	return cloneRevisionSession(session)
}

func (s *RevisionStore) LoadSession(sessionID string) (*domain.RevisionSession, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	session, exists := state.Sessions[strings.TrimSpace(sessionID)]
	if !exists {
		return nil, ErrRevisionNotFound
	}
	return cloneRevisionSession(session)
}

func (s *RevisionStore) LoadVersion(versionID string) (*domain.ArtifactVersion, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	version, exists := state.Versions[strings.TrimSpace(versionID)]
	if !exists {
		return nil, fmt.Errorf("artifact version %q is not found", versionID)
	}
	copy := version
	copy.Payload = append(json.RawMessage(nil), version.Payload...)
	return &copy, nil
}

func (s *RevisionStore) CurrentVersion(artifactID string) (*domain.ArtifactVersion, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	versionID := state.CurrentArtifacts[strings.TrimSpace(artifactID)]
	if versionID == "" {
		return nil, nil
	}
	version := state.Versions[versionID]
	copy := version
	copy.Payload = append(json.RawMessage(nil), version.Payload...)
	return &copy, nil
}

func (s *RevisionStore) ListVersions(artifactID string) ([]domain.ArtifactVersion, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	artifactID = strings.TrimSpace(artifactID)
	versions := make([]domain.ArtifactVersion, 0)
	for _, version := range state.Versions {
		if version.ArtifactID == artifactID {
			version.Payload = append(json.RawMessage(nil), version.Payload...)
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Sequence < versions[j].Sequence })
	return versions, nil
}

func (s *RevisionStore) mutate(
	policy domain.RevisionPolicy,
	input RevisionMutationInput,
	operation string,
	payload any,
	apply func(*revisionState, *domain.RevisionSession) error,
) (*domain.RevisionSession, error) {
	command, fingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, operation, payload)
	if err != nil {
		return nil, err
	}
	if receipt, err := s.lookupRevisionReceipt(input.IdempotencyKey, command, fingerprint); receipt != nil || err != nil {
		if receipt != nil && err == nil && operation == "publish" && validExpansionPublicationPolicy(*receipt) {
			err = s.withRevisionTransaction(func() error {
				return finalizeExpansionAuthorityCreationForOutput(s.io.dir)
			})
			if err != nil {
				return nil, fmt.Errorf("finalize expansion authority creation retry: %w", err)
			}
		}
		return receipt, err
	}
	policyMode, policyID, policyVersion, err := describeRevisionPolicy(policy)
	if err != nil {
		return nil, err
	}
	if operation == "publish" {
		return s.mutatePublicationAtomically(
			policy,
			input,
			command,
			fingerprint,
			policyMode,
			policyID,
			policyVersion,
			apply,
		)
	}
	var working *revisionState
	var generation uint64
	var receipt *domain.RevisionSession
	err = s.withRevisionTransaction(func() error {
		state, loadErr := s.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		if err := allowRevisionCommandMutation(state, s.commandOwner, s.publicationOwner); err != nil {
			return err
		}
		if found, receiptErr := matchingRevisionReceipt(state, s.commandOwner, input.IdempotencyKey, command, fingerprint); found != nil || receiptErr != nil {
			receipt, err = found, receiptErr
			return err
		}
		if state.ActiveSessionID == "" {
			return ErrNoActiveRevision
		}
		if strings.TrimSpace(input.SessionID) != state.ActiveSessionID {
			return ErrRevisionNotFound
		}
		session := state.Sessions[state.ActiveSessionID]
		if session.Mode != policyMode || session.PolicyID != policyID || session.PolicyVersion != policyVersion {
			return fmt.Errorf("revision policy %q@%q does not match persisted policy %q@%q", policyID, policyVersion, session.PolicyID, session.PolicyVersion)
		}
		if input.ExpectedRevision != session.Revision {
			return &RevisionConflictError{Expected: input.ExpectedRevision, Actual: session.Revision}
		}
		generation = state.Generation
		working, err = cloneRevisionState(state)
		return err
	})
	if err != nil || receipt != nil {
		return receipt, err
	}
	session := working.Sessions[working.ActiveSessionID]
	var normalAuthoritySnapshot *publicationAuthoritySnapshot
	if operation == "publish" && session.Mode == domain.RevisionModeNormal && working.Publication != nil {
		snapshot := working.Publication.AuthoritySnapshot
		normalAuthoritySnapshot = &snapshot
	}
	if err := apply(working, &session); err != nil {
		return nil, err
	}
	session.Revision++
	working.Generation++
	session.Generation = working.Generation
	session.UpdatedAt = domain.RevisionTimestamp()
	if session.Stage.Terminal() {
		session.Route = nil
	} else if err := applyRevisionRoute(policy, &session); err != nil {
		return nil, err
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	working.Sessions[session.ID] = session
	if operation == "publish" && validExpansionPublicationPolicy(session) {
		if err := s.writeExpansionPublicationReceipt(working, session); err != nil {
			if normalAuthoritySnapshot != nil {
				if restoreErr := restorePublicationAuthoritySnapshot(s.io, *normalAuthoritySnapshot); restoreErr != nil {
					return nil, errors.Join(fmt.Errorf("seal expansion publication receipt: %w", err), fmt.Errorf("restore failed normal publication authority: %w", restoreErr))
				}
			}
			return nil, fmt.Errorf("seal expansion publication receipt: %w", err)
		}
	}
	committed, commitErr := s.commitOptimistic(input.IdempotencyKey, command, fingerprint, generation, working, session)
	if commitErr != nil && normalAuthoritySnapshot != nil {
		if restoreErr := restorePublicationAuthoritySnapshot(s.io, *normalAuthoritySnapshot); restoreErr != nil {
			return nil, errors.Join(commitErr, fmt.Errorf("restore failed normal publication authority: %w", restoreErr))
		}
	}
	return committed, commitErr
}

// mutatePublicationAtomically keeps the final formal-artifact verification,
// signed expansion receipt, and revision generation commit under one project
// transaction. The surrounding normal/adaptation publication journals remain
// responsible for crash recovery of formal files; this boundary removes the
// live race in which another formal writer could previously enter after the
// receipt was sealed but before state.json advanced.
func (s *RevisionStore) mutatePublicationAtomically(
	policy domain.RevisionPolicy,
	input RevisionMutationInput,
	operation, fingerprint string,
	policyMode domain.RevisionMode,
	policyID, policyVersion string,
	apply func(*revisionState, *domain.RevisionSession) error,
) (*domain.RevisionSession, error) {
	var committed *domain.RevisionSession
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if err := allowRevisionCommandMutation(state, s.commandOwner, s.publicationOwner); err != nil {
			return err
		}
		if found, receiptErr := matchingRevisionReceipt(state, s.commandOwner, input.IdempotencyKey, operation, fingerprint); found != nil || receiptErr != nil {
			if found != nil && receiptErr == nil && validExpansionPublicationPolicy(*found) {
				if err := finalizeExpansionAuthorityCreationForOutput(s.io.dir); err != nil {
					return fmt.Errorf("finalize expansion authority creation retry: %w", err)
				}
			}
			committed = found
			return receiptErr
		}
		if state.ActiveSessionID == "" {
			return ErrNoActiveRevision
		}
		if strings.TrimSpace(input.SessionID) != state.ActiveSessionID {
			return ErrRevisionNotFound
		}
		session := state.Sessions[state.ActiveSessionID]
		if session.Mode != policyMode || session.PolicyID != policyID || session.PolicyVersion != policyVersion {
			return fmt.Errorf("revision policy %q@%q does not match persisted policy %q@%q", policyID, policyVersion, session.PolicyID, session.PolicyVersion)
		}
		if input.ExpectedRevision != session.Revision {
			return &RevisionConflictError{Expected: input.ExpectedRevision, Actual: session.Revision}
		}

		expectedGeneration := state.Generation
		working, err := cloneRevisionState(state)
		if err != nil {
			return err
		}
		session = working.Sessions[working.ActiveSessionID]
		if err := apply(working, &session); err != nil {
			return err
		}
		session.Revision++
		working.Generation++
		session.Generation = working.Generation
		session.UpdatedAt = domain.RevisionTimestamp()
		if session.Stage.Terminal() {
			session.Route = nil
		} else if err := applyRevisionRoute(policy, &session); err != nil {
			return err
		}
		if err := session.Validate(); err != nil {
			return err
		}
		if working.Generation != expectedGeneration+1 || session.Generation != working.Generation {
			return fmt.Errorf("revision generation fence did not advance exactly once")
		}
		working.Sessions[session.ID] = session
		receipt, err := s.revisionReceiptForCommit(input.IdempotencyKey, operation, fingerprint, session)
		if err != nil {
			return err
		}
		working.Receipts[input.IdempotencyKey] = receipt
		if err := validateRevisionState(working); err != nil {
			return err
		}

		if validExpansionPublicationPolicy(session) {
			authoritySnapshot, err := capturePublicationAuthoritySnapshot(s.io)
			if err != nil {
				return fmt.Errorf("snapshot expansion publication authority: %w", err)
			}
			if err := s.writeExpansionPublicationReceiptOwned(working, session); err != nil {
				if restoreErr := restorePublicationAuthoritySnapshot(s.io, authoritySnapshot); restoreErr != nil {
					return errors.Join(fmt.Errorf("seal expansion publication receipt: %w", err), fmt.Errorf("restore publication authority: %w", restoreErr))
				}
				return fmt.Errorf("seal expansion publication receipt: %w", err)
			}
			if err := s.io.WriteJSON(revisionStateFile, working); err != nil {
				persisted, loadErr := s.loadUnlocked()
				if loadErr == nil && revisionPublicationCommitMatches(persisted, input.IdempotencyKey, operation, fingerprint, session) {
					committed, err = cloneRevisionSession(session)
					return err
				}
				if restoreErr := restorePublicationAuthoritySnapshot(s.io, authoritySnapshot); restoreErr != nil {
					return errors.Join(err, fmt.Errorf("restore publication authority: %w", restoreErr))
				}
				return err
			}
			committed, err = cloneRevisionSession(session)
			if err != nil {
				return err
			}
			if err := finalizeExpansionAuthorityCreationForOutput(s.io.dir); err != nil {
				return fmt.Errorf("finalize expansion authority creation: %w", err)
			}
		} else if err := s.io.WriteJSON(revisionStateFile, working); err != nil {
			return err
		}
		committed, err = cloneRevisionSession(session)
		return err
	})
	return committed, err
}

func revisionPublicationCommitMatches(
	state *revisionState,
	idempotencyKey, operation, fingerprint string,
	result domain.RevisionSession,
) bool {
	if state == nil || state.Generation != result.Generation {
		return false
	}
	receipt, ok := state.Receipts[strings.TrimSpace(idempotencyKey)]
	return ok && receipt.Operation == operation && receipt.Fingerprint == fingerprint &&
		receipt.Result.ID == result.ID && receipt.Result.Revision == result.Revision &&
		receipt.Result.Generation == result.Generation && receipt.Result.Stage == result.Stage
}

func revisionCommandFingerprint(idempotencyKey, operation string, payload any) (string, string, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return "", "", fmt.Errorf("revision idempotency key is required")
	}
	fingerprintPayload, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	return operation, domain.ContentSignature(fingerprintPayload), nil
}

func matchingRevisionReceipt(state *revisionState, owner *revisionCommandOwner, key, operation, fingerprint string) (*domain.RevisionSession, error) {
	receipt, exists := state.Receipts[strings.TrimSpace(key)]
	if !exists {
		return nil, nil
	}
	expectedFingerprint := fingerprint
	if owner != nil && strings.TrimSpace(key) == owner.Key {
		internalOperation, ok := adaptationRevisionInternalReceiptOperation(owner.Operation)
		if !ok || internalOperation != operation {
			return nil, ErrRevisionIdempotencyConflict
		}
		expectedFingerprint = adaptationRevisionServiceBoundInternalFingerprint(
			key, operation, fingerprint, owner.Operation, owner.Fingerprint,
		)
	}
	if receipt.Operation != operation || receipt.Fingerprint != expectedFingerprint {
		return nil, ErrRevisionIdempotencyConflict
	}
	return cloneRevisionSession(receipt.Result)
}

func (s *RevisionStore) revisionReceiptForCommit(
	key, operation, fingerprint string,
	result domain.RevisionSession,
) (revisionReceipt, error) {
	receipt := revisionReceipt{Operation: operation, Fingerprint: fingerprint, Result: result}
	if s == nil || s.commandOwner == nil {
		return receipt, nil
	}
	owner := s.commandOwner
	if strings.TrimSpace(key) != owner.Key {
		// Compensating/rollback mutations may use their own idempotency key while
		// the outer command still owns the project. They are not service receipts
		// and therefore deliberately remain unbound.
		return receipt, nil
	}
	internalOperation, ok := adaptationRevisionInternalReceiptOperation(owner.Operation)
	if !ok || internalOperation != operation || strings.TrimSpace(owner.Fingerprint) == "" {
		return revisionReceipt{}, fmt.Errorf("revision receipt does not match its service command identity")
	}
	receipt.ServiceOperation = owner.Operation
	receipt.ServiceFingerprint = owner.Fingerprint
	receipt.Fingerprint = adaptationRevisionServiceBoundInternalFingerprint(
		key, operation, fingerprint, owner.Operation, owner.Fingerprint,
	)
	return receipt, nil
}

func (s *RevisionStore) lookupRevisionReceipt(key, operation, fingerprint string) (*domain.RevisionSession, error) {
	var receipt *domain.RevisionSession
	err := s.withRevisionTransaction(func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		found, receiptErr := matchingRevisionReceipt(state, s.commandOwner, key, operation, fingerprint)
		if receiptErr == nil && found != nil {
			receipt = found
			if operation == "publish" && validExpansionPublicationPolicy(*found) {
				needsFinalize, finalizeErr := expansionAuthorityCreationNeedsFinalize(s.io.dir)
				if finalizeErr != nil {
					return finalizeErr
				}
				if needsFinalize {
					return nil
				}
			}
		}
		if err := allowRevisionCommandMutation(state, s.commandOwner, s.publicationOwner); err != nil {
			return err
		}
		receipt = found
		return receiptErr
	})
	return receipt, err
}

func cloneRevisionState(state *revisionState) (*revisionState, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var clone revisionState
	if err := json.Unmarshal(payload, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func (s *RevisionStore) commitOptimistic(
	idempotencyKey, operation, fingerprint string,
	expectedGeneration uint64,
	working *revisionState,
	result domain.RevisionSession,
) (*domain.RevisionSession, error) {
	var committed *domain.RevisionSession
	err := s.withRevisionTransaction(func() error {
		current, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if err := allowRevisionCommandMutation(current, s.commandOwner, s.publicationOwner); err != nil {
			return err
		}
		if found, receiptErr := matchingRevisionReceipt(current, s.commandOwner, idempotencyKey, operation, fingerprint); found != nil || receiptErr != nil {
			committed = found
			return receiptErr
		}
		if operation == "start" {
			if manuscriptID, loadErr := activeManuscriptRevisionID(s.io); loadErr != nil {
				return loadErr
			} else if manuscriptID != "" {
				return ErrActiveRevisionExists
			}
		}
		if current.Generation != expectedGeneration {
			if operation == "start" && current.ActiveSessionID != "" {
				return ErrActiveRevisionExists
			}
			return &RevisionConflictError{Expected: int(expectedGeneration), Actual: int(current.Generation)}
		}
		if working.Generation != expectedGeneration+1 || result.Generation != working.Generation {
			return fmt.Errorf("revision generation fence did not advance exactly once")
		}
		receipt, err := s.revisionReceiptForCommit(idempotencyKey, operation, fingerprint, result)
		if err != nil {
			return err
		}
		working.Receipts[idempotencyKey] = receipt
		if err := validateRevisionState(working); err != nil {
			return err
		}
		if err := s.io.WriteJSON(revisionStateFile, working); err != nil {
			return err
		}
		committed, err = cloneRevisionSession(result)
		return err
	})
	return committed, err
}

func (s *RevisionStore) load() (*revisionState, error) {
	var state *revisionState
	err := s.withRevisionTransaction(func() error {
		var loadErr error
		state, loadErr = s.loadUnlocked()
		return loadErr
	})
	return state, err
}

func (s *RevisionStore) loadUnlocked() (*revisionState, error) {
	var state revisionState
	if err := s.io.ReadJSON(revisionStateFile, &state); err != nil {
		if os.IsNotExist(err) {
			return newRevisionState(), nil
		}
		return nil, err
	}
	if state.NormalLease != nil && !revisionProcessAlive(state.NormalLease.PID) {
		state.NormalLease = nil
		state.Generation++
		if err := s.io.WriteJSON(revisionStateFile, &state); err != nil {
			return nil, fmt.Errorf("recover stale normal-flow lease: %w", err)
		}
	}
	if err := validateRevisionState(&state); err != nil {
		return nil, err
	}
	return &state, nil
}

func newRevisionState() *revisionState {
	return &revisionState{
		Version:          domain.RevisionSchemaVersion,
		Generation:       1,
		Sessions:         make(map[string]domain.RevisionSession),
		Versions:         make(map[string]domain.ArtifactVersion),
		CurrentArtifacts: make(map[string]string),
		Receipts:         make(map[string]revisionReceipt),
	}
}

func validateRevisionState(state *revisionState) error {
	if state == nil || state.Version != domain.RevisionSchemaVersion || state.Generation == 0 {
		return fmt.Errorf("unsupported revision store version")
	}
	if state.Sessions == nil || state.Versions == nil || state.CurrentArtifacts == nil || state.Receipts == nil {
		return fmt.Errorf("revision store maps are required")
	}
	if state.NormalLease != nil {
		if strings.TrimSpace(state.NormalLease.Token) == "" || strings.TrimSpace(state.NormalLease.Owner) == "" || state.NormalLease.PID <= 0 || state.NormalLease.Generation != state.Generation {
			return fmt.Errorf("normal flow lease fence is invalid")
		}
		if state.ActiveSessionID != "" {
			return fmt.Errorf("normal flow lease and revision cannot both be active")
		}
	}
	if state.CommandFence != nil {
		if _, err := newRevisionCommandFence(state.CommandFence.Key, state.CommandFence.Operation, state.CommandFence.Fingerprint, state.CommandFence.OwnerToken); err != nil {
			return err
		}
		if state.NormalLease != nil {
			return fmt.Errorf("normal flow lease and revision command fence cannot both be active")
		}
	}
	if state.Publication != nil {
		attempt := state.Publication
		decodedToken, err := hex.DecodeString(attempt.Token)
		if err != nil || len(decodedToken) != 32 || attempt.SessionID == "" || attempt.ExpectedRevision <= 0 ||
			attempt.Generation != state.Generation || attempt.Mode != domain.RevisionModeNormal ||
			attempt.PolicyID == "" || attempt.PolicyVersion == "" || attempt.PublishKey == "" ||
			attempt.PublishFingerprint == "" || len(attempt.AcceptedVersionIDs) == 0 ||
			attempt.AcceptedDigest == "" || attempt.CandidateDigest == "" ||
			(attempt.Status != revisionPublicationPrepared && attempt.Status != revisionPublicationApplied) {
			return fmt.Errorf("normal publication attempt is invalid")
		}
		if state.NormalLease != nil || state.CommandFence != nil || state.ActiveSessionID != attempt.SessionID {
			return fmt.Errorf("normal publication attempt has conflicting ownership")
		}
		session, exists := state.Sessions[attempt.SessionID]
		if !exists || !session.Active() || session.Mode != domain.RevisionModeNormal ||
			session.Revision != attempt.ExpectedRevision || session.Generation != attempt.Generation ||
			session.PolicyID != attempt.PolicyID || session.PolicyVersion != attempt.PolicyVersion {
			return fmt.Errorf("normal publication attempt session binding is invalid")
		}
		expectedIDs := append(append([]string(nil), session.AcceptedVersionIDs...), session.CandidateVersionIDs...)
		if !slices.Equal(expectedIDs, attempt.AcceptedVersionIDs) {
			return fmt.Errorf("normal publication accepted version binding is invalid")
		}
		accepted := make([]domain.ArtifactVersion, 0, len(expectedIDs))
		for _, versionID := range expectedIDs {
			version, exists := state.Versions[versionID]
			if !exists {
				return fmt.Errorf("normal publication version %q is missing", versionID)
			}
			accepted = append(accepted, version)
		}
		if domain.CandidateSignature(accepted) != attempt.AcceptedDigest {
			return fmt.Errorf("normal publication accepted artifact digest is invalid")
		}
		candidateDigest, err := latestNormalStructureDigest(accepted)
		if err != nil || candidateDigest != attempt.CandidateDigest {
			return fmt.Errorf("normal publication candidate digest is invalid: %w", err)
		}
		snapshotDigest, err := normalRevisionFormalSnapshotDigest(attempt.PrepublishSnapshot.Structure, attempt.PrepublishSnapshot.Progress)
		if err != nil || snapshotDigest != attempt.PrepublishSnapshot.Digest {
			return fmt.Errorf("normal publication prepublish snapshot is invalid: %w", err)
		}
	}
	activeCount := 0
	for id, session := range state.Sessions {
		if id != session.ID {
			return fmt.Errorf("revision session index mismatch")
		}
		if err := session.Validate(); err != nil {
			return fmt.Errorf("validate revision session %q: %w", id, err)
		}
		if session.Active() {
			activeCount++
		}
		candidateVersions := make([]domain.ArtifactVersion, 0, len(session.CandidateVersionIDs))
		for _, versionID := range session.AcceptedVersionIDs {
			version, exists := state.Versions[versionID]
			if !exists || version.RevisionID != session.ID || version.Round >= session.Round {
				return fmt.Errorf("revision session %q references invalid accepted version %q", id, versionID)
			}
		}
		for _, versionID := range session.CandidateVersionIDs {
			version, exists := state.Versions[versionID]
			if !exists || version.RevisionID != session.ID {
				return fmt.Errorf("revision session %q references invalid candidate version %q", id, versionID)
			}
			candidateVersions = append(candidateVersions, version)
			if version.Round != session.Round {
				return fmt.Errorf("revision session %q references candidate %q from stale round %d", id, versionID, version.Round)
			}
		}
		if len(candidateVersions) == 0 && session.CandidateSignature != "" {
			return fmt.Errorf("revision session %q has a candidate signature without versions", id)
		}
		if len(candidateVersions) > 0 && session.CandidateSignature != domain.CandidateSignature(candidateVersions) {
			return fmt.Errorf("revision session %q candidate signature mismatch", id)
		}
		if session.Generation > state.Generation {
			return fmt.Errorf("revision session %q generation is ahead of the store", id)
		}
	}
	if activeCount > 1 {
		return fmt.Errorf("revision store contains multiple active sessions")
	}
	if activeCount == 0 && state.ActiveSessionID != "" {
		return fmt.Errorf("revision active-session index is stale")
	}
	if activeCount == 1 {
		active, exists := state.Sessions[state.ActiveSessionID]
		if !exists || !active.Active() {
			return fmt.Errorf("revision active-session index is invalid")
		}
	}
	for id, version := range state.Versions {
		if id != version.ID {
			return fmt.Errorf("artifact version index mismatch")
		}
		if err := version.Validate(); err != nil {
			return fmt.Errorf("validate artifact version %q: %w", id, err)
		}
	}
	for artifactID, versionID := range state.CurrentArtifacts {
		version, exists := state.Versions[versionID]
		if !exists || version.ArtifactID != artifactID {
			return fmt.Errorf("current artifact %q references invalid version %q", artifactID, versionID)
		}
	}
	for key, receipt := range state.Receipts {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(receipt.Operation) == "" || strings.TrimSpace(receipt.Fingerprint) == "" {
			return fmt.Errorf("revision receipt identity is invalid")
		}
		if (receipt.ServiceOperation == "") != (receipt.ServiceFingerprint == "") {
			return fmt.Errorf("revision receipt %q has a partial service identity", key)
		}
		if receipt.ServiceOperation != "" {
			internalOperation, ok := adaptationRevisionInternalReceiptOperation(receipt.ServiceOperation)
			if !ok || internalOperation != receipt.Operation || strings.TrimSpace(receipt.ServiceFingerprint) == "" ||
				receipt.ServiceFingerprint != strings.TrimSpace(receipt.ServiceFingerprint) {
				return fmt.Errorf("revision receipt %q has an invalid service identity", key)
			}
			internalFingerprint, err := adaptationRevisionInternalReceiptFingerprint(state, key, receipt.Operation, receipt.Result)
			if err != nil {
				return fmt.Errorf("revision receipt %q cannot reconstruct its internal fingerprint: %w", key, err)
			}
			expectedFingerprint := adaptationRevisionServiceBoundInternalFingerprint(
				key, receipt.Operation, internalFingerprint, receipt.ServiceOperation, receipt.ServiceFingerprint,
			)
			if receipt.Fingerprint != expectedFingerprint {
				return fmt.Errorf("revision receipt %q has an invalid service-bound fingerprint", key)
			}
		}
		if err := receipt.Result.Validate(); err != nil {
			return fmt.Errorf("validate revision receipt %q: %w", key, err)
		}
		if receipt.Result.Generation > state.Generation {
			return fmt.Errorf("revision receipt %q generation is ahead of the store", key)
		}
	}
	return nil
}

func normalizeRevisionImpact(impact domain.RevisionImpact) (domain.RevisionImpact, error) {
	normalized, err := domain.NewRevisionImpact(impact.Summary, impact.Items)
	if err != nil {
		return domain.RevisionImpact{}, err
	}
	if strings.TrimSpace(impact.Signature) != "" && impact.Signature != normalized.Signature {
		return domain.RevisionImpact{}, fmt.Errorf("revision impact signature mismatch")
	}
	return normalized, nil
}

func describeRevisionPolicy(policy domain.RevisionPolicy) (domain.RevisionMode, string, string, error) {
	if policy == nil {
		return "", "", "", fmt.Errorf("revision policy with a mode is required")
	}
	mode := policy.Mode()
	if strings.TrimSpace(string(mode)) == "" {
		return "", "", "", fmt.Errorf("revision policy with a mode is required")
	}
	id, version := policy.Identity()
	if strings.TrimSpace(id) == "" || strings.TrimSpace(version) == "" {
		return "", "", "", fmt.Errorf("revision policy identity and version are required")
	}
	return mode, id, version, nil
}

func validateApprovalStages(policy domain.RevisionPolicy, impact domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	stages, err := policy.ApprovalStages(cloneRevisionImpact(impact))
	if err != nil {
		return nil, fmt.Errorf("load revision approval stages: %w", err)
	}
	if len(stages) == 0 {
		return nil, fmt.Errorf("revision policy must define at least one approval stage")
	}
	probe := domain.RevisionSession{
		Version: domain.RevisionSchemaVersion, ID: "probe", Mode: policy.Mode(),
		Stage: domain.RevisionStageImpactReviewPending, Revision: 1, Generation: 1, Intent: "probe", Impact: impact,
		ApprovalStages: stages, Round: 1, CreatedAt: domain.RevisionTimestamp(), UpdatedAt: domain.RevisionTimestamp(),
	}
	probe.PolicyID, probe.PolicyVersion = policy.Identity()
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	return append([]domain.RevisionApprovalStage(nil), stages...), nil
}

func approvalStagesEqual(left, right []domain.RevisionApprovalStage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func applyRevisionRoute(policy domain.RevisionPolicy, session *domain.RevisionSession) error {
	if session.Stage != domain.RevisionStageCandidateGenerating && session.Stage != domain.RevisionStageCandidateAudit {
		session.Route = nil
		return nil
	}
	policySession, err := cloneRevisionSession(*session)
	if err != nil {
		return err
	}
	route, err := policy.Route(*policySession)
	if err != nil {
		return fmt.Errorf("route revision: %w", err)
	}
	if route != nil {
		copy := *route
		copy.SessionID = session.ID
		copy.Revision = session.Revision
		copy.Generation = session.Generation
		if err := copy.Validate(); err != nil {
			return err
		}
		session.Route = &copy
	} else {
		session.Route = nil
	}
	return nil
}

func cloneRevisionImpact(impact domain.RevisionImpact) domain.RevisionImpact {
	clone := impact
	clone.Items = append([]domain.RevisionImpactItem(nil), impact.Items...)
	for index := range clone.Items {
		clone.Items[index].DependencyEvidence = append([]string(nil), impact.Items[index].DependencyEvidence...)
	}
	return clone
}

func cloneArtifactVersions(versions []domain.ArtifactVersion) []domain.ArtifactVersion {
	clone := append([]domain.ArtifactVersion(nil), versions...)
	for index := range clone {
		clone[index].Payload = append(json.RawMessage(nil), versions[index].Payload...)
	}
	return clone
}

func nextArtifactSequence(state *revisionState, artifactID string) int {
	sequence := 1
	for _, version := range state.Versions {
		if version.ArtifactID == artifactID && version.Sequence >= sequence {
			sequence = version.Sequence + 1
		}
	}
	return sequence
}

func cloneRevisionSession(session domain.RevisionSession) (*domain.RevisionSession, error) {
	payload, err := json.Marshal(session)
	if err != nil {
		return nil, err
	}
	var clone domain.RevisionSession
	if err := json.Unmarshal(payload, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}
