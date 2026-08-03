package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const adaptationRevisionRuntimeFile = adaptationRootDir + "/revision_runtime.json"
const adaptationRevisionServiceReceiptsFile = adaptationRootDir + "/revision_service_receipts.json"
const adaptationRevisionCommandLockFile = "meta/revisions/adaptation-command.lock"
const adaptationRevisionCommandJournalFile = "meta/revisions/adaptation-command-journal.json"
const adaptationRevisionCommandSnapshotDir = "meta/revisions/adaptation-command-snapshot"

var adaptationRevisionCommandLocks sync.Map

var adaptationRevisionCommandCleanupFault func(string, string) error
var adaptationPublicationLegacyBindingForTesting bool

type adaptationRevisionFileSnapshot struct {
	exists bool
	data   []byte
}

type adaptationRevisionServiceReceipt struct {
	Operation   string          `json:"operation"`
	Fingerprint string          `json:"fingerprint"`
	Result      json.RawMessage `json:"result"`
}

type adaptationRevisionServiceReceiptState struct {
	Version  int                                         `json:"version"`
	Receipts map[string]adaptationRevisionServiceReceipt `json:"receipts"`
}

type adaptationStructureRevisionPreviewReceipt struct {
	Stage                   domain.ManuscriptStage `json:"stage"`
	BasePlanSignature       string                 `json:"base_plan_signature"`
	SourceManifestSignature string                 `json:"source_manifest_signature"`
	Candidate               *domain.AdaptationPlan `json:"candidate"`
	Impact                  *domain.RevisionImpact `json:"impact"`
	Signature               string                 `json:"signature"`
}

type adaptationRevisionPreviewReceipt struct {
	Preview *adaptationStructureRevisionPreviewReceipt `json:"preview"`
	Session *domain.RevisionSession                    `json:"session"`
}

type adaptationRevisionCommandJournal struct {
	Version           int                                   `json:"version"`
	Key               string                                `json:"key"`
	Operation         string                                `json:"operation"`
	Fingerprint       string                                `json:"fingerprint"`
	Files             []string                              `json:"files"`
	AuthoritySnapshot publicationAuthoritySnapshot          `json:"authority_snapshot"`
	Publication       *AdaptationRevisionPublicationCommand `json:"publication,omitempty"`
}

type adaptationRevisionCommandJournalV1 struct {
	Version     int      `json:"version"`
	Key         string   `json:"key"`
	Operation   string   `json:"operation"`
	Fingerprint string   `json:"fingerprint"`
	Files       []string `json:"files"`
}

type adaptationRevisionCommandJournalV2 struct {
	Version           int                                   `json:"version"`
	Key               string                                `json:"key"`
	Operation         string                                `json:"operation"`
	Fingerprint       string                                `json:"fingerprint"`
	Files             []string                              `json:"files"`
	AuthoritySnapshot publicationAuthoritySnapshot          `json:"authority_snapshot"`
	Publication       *AdaptationRevisionPublicationCommand `json:"publication,omitempty"`
}

// AdaptationRevisionPublicationCommand binds a prepared service publish to the
// one internal revision receipt that is allowed to prove it committed.
type AdaptationRevisionPublicationCommand struct {
	SessionID                  string `json:"session_id"`
	ExpectedRevision           int    `json:"expected_revision"`
	InternalReceiptFingerprint string `json:"internal_receipt_fingerprint"`
}

type adaptationPublicationServiceBindingPayload struct {
	Version                    int                         `json:"version"`
	Key                        string                      `json:"key"`
	Operation                  string                      `json:"operation"`
	ServiceFingerprint         string                      `json:"service_fingerprint"`
	InternalReceiptFingerprint string                      `json:"internal_receipt_fingerprint"`
	Session                    domain.RevisionSession      `json:"session"`
	Publication                ExpansionPublicationReceipt `json:"publication"`
}

func adaptationPublicationServiceBindingDigest(
	key, operation, serviceFingerprint, internalReceiptFingerprint string,
	session domain.RevisionSession,
	receipt ExpansionPublicationReceipt,
) (string, error) {
	key = strings.TrimSpace(key)
	operation = strings.TrimSpace(operation)
	serviceFingerprint = strings.TrimSpace(serviceFingerprint)
	internalReceiptFingerprint = strings.TrimSpace(internalReceiptFingerprint)
	if key == "" || operation != "publish" || !isLowerHex64(serviceFingerprint) || !isLowerHex64(internalReceiptFingerprint) {
		return "", fmt.Errorf("adaptation publication service binding identity is invalid")
	}
	if err := validateCommittedAdaptationServiceResult(&session); err != nil {
		return "", err
	}
	receipt.Signature = ""
	receipt.AdaptationServiceBinding = ""
	payload, err := json.Marshal(adaptationPublicationServiceBindingPayload{
		Version: 1, Key: key, Operation: operation, ServiceFingerprint: serviceFingerprint,
		InternalReceiptFingerprint: internalReceiptFingerprint, Session: session, Publication: receipt,
	})
	if err != nil {
		return "", err
	}
	return domain.ContentSignature(payload), nil
}

func adaptationPublicationServiceBindingForCommit(
	state *revisionState,
	session domain.RevisionSession,
	receipt ExpansionPublicationReceipt,
) (string, error) {
	if state == nil || state.CommandFence == nil {
		// Direct low-level callers can still read legacy adaptation receipts. The
		// production service always owns a publish fence and therefore always emits
		// the durable binding below.
		return "", nil
	}
	if adaptationPublicationLegacyBindingForTesting {
		return "", nil
	}
	fence := state.CommandFence
	if fence.Operation != "publish" {
		return "", fmt.Errorf("adaptation publication requires an exact publish service fence")
	}
	internal, exists := state.Receipts[fence.Key]
	if !exists || internal.Operation != "publish" || internal.ServiceOperation != fence.Operation ||
		internal.ServiceFingerprint != fence.Fingerprint || !reflect.DeepEqual(internal.Result, session) {
		return "", fmt.Errorf("adaptation publication service binding requires its internal receipt")
	}
	rawInternalFingerprint, err := adaptationRevisionInternalReceiptFingerprint(state, fence.Key, "publish", session)
	if err != nil {
		return "", fmt.Errorf("reconstruct adaptation publication internal receipt fingerprint: %w", err)
	}
	return adaptationPublicationServiceBindingDigest(
		fence.Key, fence.Operation, fence.Fingerprint, rawInternalFingerprint, session, receipt,
	)
}

// SetAdaptationRevisionCommandCleanupFaultForTesting installs deterministic
// failure points around prepared journal/snapshot deletion.
func SetAdaptationRevisionCommandCleanupFaultForTesting(fault func(path, stage string) error) func() {
	previous := adaptationRevisionCommandCleanupFault
	adaptationRevisionCommandCleanupFault = fault
	return func() { adaptationRevisionCommandCleanupFault = previous }
}

// SetAdaptationPublicationLegacyBindingForTesting emits the pre-binding signed
// adaptation receipt schema so upgrade recovery can be exercised end to end.
func SetAdaptationPublicationLegacyBindingForTesting(enabled bool) (func(), error) {
	if !testing.Testing() {
		return nil, fmt.Errorf("legacy adaptation publication binding fixture is unavailable outside a Go test process")
	}
	previous := adaptationPublicationLegacyBindingForTesting
	adaptationPublicationLegacyBindingForTesting = enabled
	return func() { adaptationPublicationLegacyBindingForTesting = previous }, nil
}

type AdaptationFormalSnapshot struct {
	files          map[string]adaptationRevisionFileSnapshot
	structureFiles map[string][]byte
}

var adaptationRevisionFormalFiles = []string{
	adaptationPlanFile,
	adaptationProposalFile,
	adaptationVolumeReviewFile,
	adaptationProposalRuntimeFile,
	adaptationPlanningWorkflowFile,
	adaptationAuditReportFile,
	adaptationRepairApplicationFile,
	"meta/progress.json",
}

func (s *AdaptationStore) withLegacyFormalMutation(operation string, mutation func() error) error {
	if s == nil {
		return fmt.Errorf("adaptation store is required before %s", operation)
	}
	if s.withLegacyMutation == nil {
		return mutation()
	}
	return s.withLegacyMutation(operation, s.migration, mutation)
}

func (s *AdaptationStore) saveRevisionRuntimeRaw(runtime domain.AdaptationRevisionRuntime) error {
	if err := runtime.Validate(); err != nil {
		return err
	}
	return s.io.WriteJSON(adaptationRevisionRuntimeFile, runtime)
}

func (s *AdaptationStore) LoadRevisionServiceReceipt(key, operation, fingerprint string, result any) (bool, error) {
	receipt, found, err := s.loadRevisionServiceReceipt(key)
	if err != nil || !found {
		return false, err
	}
	operation, fingerprint = strings.TrimSpace(operation), strings.TrimSpace(fingerprint)
	if !found {
		return false, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return false, ErrRevisionIdempotencyConflict
	}
	if result == nil {
		return false, fmt.Errorf("adaptation revision service receipt result target is required")
	}
	if err := decodeStrictUniqueJSON(receipt.Result, result); err != nil {
		return false, fmt.Errorf("decode adaptation revision service receipt %q: %w", key, err)
	}
	return true, nil
}

func (s *AdaptationStore) loadRevisionServiceReceipt(key string) (adaptationRevisionServiceReceipt, bool, error) {
	key = strings.TrimSpace(key)
	data, err := s.io.ReadFile(adaptationRevisionServiceReceiptsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return adaptationRevisionServiceReceipt{}, false, nil
		}
		return adaptationRevisionServiceReceipt{}, false, err
	}
	state, err := decodeAdaptationRevisionServiceReceiptState(data)
	if err != nil {
		return adaptationRevisionServiceReceipt{}, false, err
	}
	receipt, found := state.Receipts[key]
	return receipt, found, nil
}

func (s *AdaptationStore) HasRevisionServiceReceipt(key, operation, fingerprint string) (bool, error) {
	receipt, found, err := s.loadRevisionServiceReceipt(key)
	if err != nil || !found {
		return false, err
	}
	operation, fingerprint = strings.TrimSpace(operation), strings.TrimSpace(fingerprint)
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return false, ErrRevisionIdempotencyConflict
	}
	return true, nil
}

func (s *AdaptationStore) saveRevisionServiceReceiptRaw(key, operation, fingerprint string, result any) error {
	key, operation, fingerprint = strings.TrimSpace(key), strings.TrimSpace(operation), strings.TrimSpace(fingerprint)
	if key == "" || operation == "" || fingerprint == "" {
		return fmt.Errorf("adaptation revision service receipt identity is required")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err := decodeAdaptationRevisionServiceResult(operation, encoded); err != nil {
		return fmt.Errorf("encode adaptation revision service receipt %q: %w", key, err)
	}
	return s.io.WithWriteLock(func() error {
		state := adaptationRevisionServiceReceiptState{Version: 1, Receipts: make(map[string]adaptationRevisionServiceReceipt)}
		data, readErr := s.io.ReadFileUnlocked(adaptationRevisionServiceReceiptsFile)
		if readErr == nil {
			decoded, decodeErr := decodeAdaptationRevisionServiceReceiptState(data)
			if decodeErr != nil {
				return decodeErr
			}
			state = decoded
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
		if state.Receipts == nil {
			state.Receipts = make(map[string]adaptationRevisionServiceReceipt)
		}
		if current, found := state.Receipts[key]; found {
			if current.Operation != operation || current.Fingerprint != fingerprint {
				return ErrRevisionIdempotencyConflict
			}
			if !sameJSONDocument(current.Result, encoded) {
				return fmt.Errorf("adaptation revision service receipt result conflicts with its durable value")
			}
			return nil
		}
		state.Receipts[key] = adaptationRevisionServiceReceipt{Operation: operation, Fingerprint: fingerprint, Result: encoded}
		return s.io.WriteJSONUnlocked(adaptationRevisionServiceReceiptsFile, state)
	})
}

func decodeAdaptationRevisionServiceReceiptState(data []byte) (adaptationRevisionServiceReceiptState, error) {
	var state adaptationRevisionServiceReceiptState
	if err := decodeStrictUniqueJSON(data, &state); err != nil {
		return state, fmt.Errorf("decode adaptation revision service receipts: %w", err)
	}
	if state.Version != 1 || state.Receipts == nil {
		return state, fmt.Errorf("adaptation revision service receipt state is invalid")
	}
	for key, receipt := range state.Receipts {
		if key == "" || key != strings.TrimSpace(key) || receipt.Operation == "" || receipt.Operation != strings.TrimSpace(receipt.Operation) ||
			receipt.Fingerprint == "" || receipt.Fingerprint != strings.TrimSpace(receipt.Fingerprint) {
			return state, fmt.Errorf("adaptation revision service receipt %q identity is invalid", key)
		}
		if _, err := decodeAdaptationRevisionServiceResult(receipt.Operation, receipt.Result); err != nil {
			return state, fmt.Errorf("adaptation revision service receipt %q result is invalid: %w", key, err)
		}
	}
	return state, nil
}

func decodeAdaptationRevisionServiceResult(operation string, data []byte) (any, error) {
	operation = strings.TrimSpace(operation)
	if operation == "preview" {
		var result adaptationRevisionPreviewReceipt
		if err := decodeStrictUniqueJSON(data, &result); err != nil {
			return nil, err
		}
		if err := validateAdaptationRevisionPreviewReceipt(result); err != nil {
			return nil, err
		}
		return &result, nil
	}
	if !isAdaptationRevisionSessionServiceOperation(operation) {
		return nil, fmt.Errorf("unsupported adaptation revision service operation %q", operation)
	}
	var result *domain.RevisionSession
	if err := decodeStrictUniqueJSON(data, &result); err != nil {
		return nil, err
	}
	if result == nil || result.Mode != domain.RevisionModeAdaptation {
		return nil, fmt.Errorf("adaptation revision service result is invalid")
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("adaptation revision service result is invalid: %w", err)
	}
	return result, nil
}

func isAdaptationRevisionSessionServiceOperation(operation string) bool {
	switch operation {
	case "approve_impact", "approve_stage", "submit_prose_intents", "pause", "resume", "cancel",
		"submit_structure", "submit_details", "record_audit", "feedback", "fail", "publish":
		return true
	default:
		return false
	}
}

func validateAdaptationRevisionPreviewReceipt(result adaptationRevisionPreviewReceipt) error {
	if result.Preview == nil || result.Session == nil || result.Session.Mode != domain.RevisionModeAdaptation ||
		result.Preview.Candidate == nil || result.Preview.Impact == nil || !result.Session.Active() ||
		!result.Preview.Stage.Valid() || !isLowerHex64(result.Preview.BasePlanSignature) ||
		!isLowerHex64(result.Preview.SourceManifestSignature) || !isLowerHex64(result.Preview.Signature) {
		return fmt.Errorf("adaptation revision preview receipt is incomplete")
	}
	if err := result.Session.Validate(); err != nil {
		return fmt.Errorf("adaptation revision preview session is invalid: %w", err)
	}
	if err := result.Preview.Impact.Validate(); err != nil || len(result.Preview.Candidate.Chapters) == 0 ||
		len(result.Preview.Candidate.Volumes) == 0 {
		return fmt.Errorf("adaptation revision preview candidate is incomplete")
	}
	copy := *result.Preview
	copy.Signature = ""
	payload, err := json.Marshal(copy)
	if err != nil || domain.JSONContentSignature(payload) != result.Preview.Signature ||
		result.Session.PreviewSignature != result.Preview.Signature || !reflect.DeepEqual(result.Session.Impact, *result.Preview.Impact) {
		return fmt.Errorf("adaptation revision preview receipt binding is invalid")
	}
	return nil
}

func validateCompleteJSONResult(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("receipt result is incomplete")
	}
	if err := validateUniqueJSONDocument(data); err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == nil {
		return fmt.Errorf("receipt result is incomplete")
	}
	return nil
}

func decodeStrictUniqueJSON(data []byte, target any) error {
	if err := validateUniqueJSONDocument(data); err != nil {
		return err
	}
	return decodeExactJSON(data, target)
}

func sameJSONDocument(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if decodeStrictUniqueJSON(left, &leftValue) != nil || decodeStrictUniqueJSON(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

// SaveAdaptationRevisionServiceReceipt durably records a prepared service
// command only for the exact owner capability that currently fences it.
func (s *Store) SaveAdaptationRevisionServiceReceipt(owner *RevisionStore, key, operation, fingerprint string, result any) error {
	key, operation, fingerprint = strings.TrimSpace(key), strings.TrimSpace(operation), strings.TrimSpace(fingerprint)
	if key == "" || operation == "" || fingerprint == "" {
		return fmt.Errorf("adaptation revision service receipt identity is required")
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if !revisionCommandMatches(state.CommandFence, key, operation, fingerprint) ||
			!revisionCommandOwnerMatches(state.CommandFence, owner.commandOwner) {
			return ErrRevisionCommandInProgress
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		decoded, err := decodeAdaptationRevisionServiceResult(operation, encoded)
		if err != nil {
			return err
		}
		if operation != "publish" {
			if err := verifyAdaptationRevisionServiceResultAgainstState(state, key, operation, fingerprint, decoded); err != nil {
				return err
			}
			if err := s.verifyAdaptationRevisionPreviewDurableFacts(operation, decoded); err != nil {
				return err
			}
		}
		return s.Adaptation.saveRevisionServiceReceiptRaw(key, operation, fingerprint, result)
	})
}

// LoadVerifiedAdaptationRevisionServiceReceipt proves a service receipt against
// the immutable shared revision receipt before exposing it to replay, cleanup,
// or startup recovery. Plain service JSON is never a commit fact on its own.
func (s *Store) LoadVerifiedAdaptationRevisionServiceReceipt(key, operation, fingerprint string, result any) (bool, error) {
	key, operation, fingerprint = strings.TrimSpace(key), strings.TrimSpace(operation), strings.TrimSpace(fingerprint)
	receipt, found, err := s.Adaptation.loadRevisionServiceReceipt(key)
	if err != nil {
		return false, err
	}
	if !found {
		if operation == "publish" {
			// A legacy committed publication without a protected service binding
			// must fail at read/replay entry points too. Modern bound commits still
			// return not-found here so startup recovery can reconstruct the missing
			// outer receipt from their signed durable facts.
			_, _, verifyErr := s.committedAdaptationPublicationResult(key, fingerprint, nil, false)
			if verifyErr != nil {
				return false, verifyErr
			}
		}
		return false, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return false, ErrRevisionIdempotencyConflict
	}
	decoded, err := decodeAdaptationRevisionServiceResult(operation, receipt.Result)
	if err != nil {
		return false, err
	}
	if operation != "publish" {
		err = s.Revisions.withRevisionTransaction(func() error {
			state, loadErr := s.Revisions.loadUnlocked()
			if loadErr != nil {
				return loadErr
			}
			return verifyAdaptationRevisionServiceResultAgainstState(state, key, operation, fingerprint, decoded)
		})
		if err != nil {
			return false, err
		}
		if err := s.verifyAdaptationRevisionPreviewDurableFacts(operation, decoded); err != nil {
			return false, err
		}
	}
	if result != nil {
		if err := decodeStrictUniqueJSON(receipt.Result, result); err != nil {
			return false, fmt.Errorf("decode adaptation revision service receipt %q: %w", key, err)
		}
	}
	return true, nil
}

func (s *Store) verifyAdaptationRevisionPreviewDurableFacts(operation string, decoded any) error {
	if operation != "preview" {
		return nil
	}
	result, ok := decoded.(*adaptationRevisionPreviewReceipt)
	if !ok || result == nil || result.Preview == nil || result.Preview.Candidate == nil || result.Preview.Impact == nil {
		return fmt.Errorf("adaptation revision preview durable facts are incomplete")
	}
	base, err := s.loadAdaptationRevisionPreviewBase()
	if err != nil {
		return fmt.Errorf("load adaptation revision preview base: %w", err)
	}
	if base == nil {
		return fmt.Errorf("adaptation revision preview base is missing")
	}
	manifest, err := s.Adaptation.LoadSourceManifest()
	if err != nil {
		return fmt.Errorf("load adaptation revision source manifest: %w", err)
	}
	if manifest == nil {
		return fmt.Errorf("adaptation revision source manifest is missing")
	}
	basePayload, err := json.Marshal(base)
	if err != nil {
		return fmt.Errorf("encode adaptation revision preview base: %w", err)
	}
	if result.Preview.BasePlanSignature != domain.JSONContentSignature(basePayload) ||
		result.Preview.SourceManifestSignature != domain.AdaptationSourceManifestContractSignature(*manifest) {
		return fmt.Errorf("adaptation revision preview base or source manifest binding is invalid")
	}
	if err := domain.ValidateAdaptationRevisionPreviewCandidate(
		*base, *result.Preview.Candidate, manifest, *result.Preview.Impact,
	); err != nil {
		return fmt.Errorf("adaptation revision preview candidate is invalid: %w", err)
	}
	return nil
}

func (s *Store) loadAdaptationRevisionPreviewBase() (*domain.AdaptationPlan, error) {
	plan, err := s.Adaptation.LoadPlan()
	if err != nil || plan != nil {
		return plan, err
	}
	proposal, err := s.Adaptation.LoadProposal()
	if err != nil || proposal != nil {
		return proposal, err
	}
	review, err := s.Adaptation.LoadVolumeReview()
	if err != nil || review == nil {
		return nil, err
	}
	manifest, err := s.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return nil, err
	}
	reports, err := s.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return nil, err
	}
	if len(reports) != manifest.ChapterCount {
		return nil, fmt.Errorf("complete immutable adaptation source reports are required at proposal-complete stage")
	}
	return domain.AdaptationPlanFromVolumeReview(*review, *manifest, reports)
}

func verifyAdaptationRevisionServiceResultAgainstState(state *revisionState, key, operation, serviceFingerprint string, decoded any) error {
	if state == nil {
		return fmt.Errorf("adaptation revision service result has no durable revision state")
	}
	var session *domain.RevisionSession
	if operation == "preview" {
		preview, ok := decoded.(*adaptationRevisionPreviewReceipt)
		if !ok || preview == nil {
			return fmt.Errorf("adaptation revision preview result is invalid")
		}
		session = preview.Session
	} else {
		var ok bool
		session, ok = decoded.(*domain.RevisionSession)
		if !ok || session == nil {
			return fmt.Errorf("adaptation revision transition result is invalid")
		}
	}
	internalOperation, ok := adaptationRevisionInternalReceiptOperation(operation)
	if !ok {
		return fmt.Errorf("unsupported adaptation revision service operation %q", operation)
	}
	internal, exists := state.Receipts[strings.TrimSpace(key)]
	if !exists || internal.Operation != internalOperation || internal.ServiceOperation != operation ||
		internal.ServiceFingerprint != strings.TrimSpace(serviceFingerprint) || !reflect.DeepEqual(internal.Result, *session) {
		return fmt.Errorf("adaptation revision service result does not match its durable revision receipt")
	}
	expectedFingerprint, err := adaptationRevisionInternalReceiptFingerprint(state, strings.TrimSpace(key), internalOperation, *session)
	if err != nil {
		return fmt.Errorf("reconstruct adaptation revision internal receipt fingerprint: %w", err)
	}
	expectedFingerprint = adaptationRevisionServiceBoundInternalFingerprint(
		key, internalOperation, expectedFingerprint, operation, serviceFingerprint,
	)
	if internal.Fingerprint != expectedFingerprint {
		return fmt.Errorf("adaptation revision internal receipt fingerprint is invalid")
	}
	persisted, exists := state.Sessions[session.ID]
	if !exists || persisted.Mode != domain.RevisionModeAdaptation || persisted.Revision < session.Revision ||
		persisted.Generation < session.Generation || state.Generation < session.Generation {
		return fmt.Errorf("adaptation revision service result does not match durable session facts")
	}
	return nil
}

func adaptationRevisionServiceBoundInternalFingerprint(
	key, internalOperation, internalFingerprint, serviceOperation, serviceFingerprint string,
) string {
	payload, _ := json.Marshal(struct {
		Key                 string
		InternalOperation   string
		InternalFingerprint string
		ServiceOperation    string
		ServiceFingerprint  string
	}{
		Key:                 strings.TrimSpace(key),
		InternalOperation:   strings.TrimSpace(internalOperation),
		InternalFingerprint: strings.TrimSpace(internalFingerprint),
		ServiceOperation:    strings.TrimSpace(serviceOperation),
		ServiceFingerprint:  strings.TrimSpace(serviceFingerprint),
	})
	return domain.JSONContentSignature(payload)
}

func adaptationRevisionInternalReceiptFingerprint(
	state *revisionState,
	key, operation string,
	result domain.RevisionSession,
) (string, error) {
	if result.Revision <= 0 {
		return "", fmt.Errorf("adaptation revision result has no predecessor revision")
	}
	base := RevisionMutationInput{SessionID: result.ID, ExpectedRevision: result.Revision - 1, IdempotencyKey: key}
	var payload any
	switch operation {
	case "start":
		payload = struct {
			Intent           string
			Impact           domain.RevisionImpact
			PreviewSignature string
		}{result.Intent, result.Impact, result.PreviewSignature}
	case "approve_impact", "pause", "resume", "cancel", "publish":
		payload = base
	case "submit_candidate":
		artifacts := make([]CandidateArtifactInput, 0, len(result.CandidateVersionIDs))
		for _, versionID := range result.CandidateVersionIDs {
			version, exists := state.Versions[versionID]
			if !exists || version.RevisionID != result.ID || version.Round != result.Round {
				return "", fmt.Errorf("candidate version %q is absent from durable revision facts", versionID)
			}
			artifacts = append(artifacts, CandidateArtifactInput{
				ArtifactID: version.ArtifactID, ArtifactKind: version.ArtifactKind,
				Payload: append(json.RawMessage(nil), version.Payload...),
			})
		}
		if len(artifacts) == 0 {
			return "", fmt.Errorf("adaptation candidate receipt has no durable artifacts")
		}
		payload = SubmitRevisionCandidateInput{
			SessionID: result.ID, ExpectedRevision: result.Revision - 1,
			IdempotencyKey: key, Artifacts: artifacts,
		}
	case "record_audit":
		auditRound := result.Round
		if result.Stage == domain.RevisionStageCandidateGenerating {
			auditRound--
		}
		evidence := make([]domain.RevisionAuditEvidence, 0)
		candidateSignature := ""
		for index := len(result.Audits) - 1; index >= 0; index-- {
			audit := result.Audits[index]
			if audit.Round != auditRound {
				break
			}
			candidateSignature = audit.CandidateSignature
			evidence = append(evidence, domain.RevisionAuditEvidence{
				Scope: audit.Scope, ScopeID: audit.ScopeID, FromChapter: audit.FromChapter,
				ToChapter: audit.ToChapter, ContentSignature: audit.ContentSignature,
				Passed: audit.Passed, Report: audit.Report,
			})
		}
		slices.Reverse(evidence)
		if len(evidence) == 0 || candidateSignature == "" {
			return "", fmt.Errorf("adaptation audit receipt has no durable evidence")
		}
		payload = RevisionAuditInput{RevisionMutationInput: base, CandidateSignature: candidateSignature, Evidence: evidence}
	case "submit_feedback":
		if len(result.Feedback) == 0 {
			return "", fmt.Errorf("adaptation feedback receipt has no durable feedback")
		}
		feedback := result.Feedback[len(result.Feedback)-1]
		payload = RevisionFeedbackInput{
			RevisionMutationInput: base, StageID: feedback.StageID,
			ImpactSignature: result.Impact.Signature, Message: feedback.Message,
		}
	case "approve_stage":
		if len(result.Approvals) == 0 {
			return "", fmt.Errorf("adaptation approval receipt has no durable approval")
		}
		payload = RevisionApprovalInput{RevisionMutationInput: base, StageID: result.Approvals[len(result.Approvals)-1].StageID}
	case "fail":
		if strings.TrimSpace(result.LastError) == "" {
			return "", fmt.Errorf("adaptation failure receipt has no durable error")
		}
		payload = RevisionFailureInput{RevisionMutationInput: base, Error: result.LastError}
	default:
		return "", fmt.Errorf("unsupported adaptation internal receipt operation %q", operation)
	}
	_, fingerprint, err := revisionCommandFingerprint(key, operation, payload)
	return fingerprint, err
}

func adaptationRevisionInternalReceiptOperation(operation string) (string, bool) {
	switch operation {
	case "preview":
		return "start", true
	case "submit_structure", "submit_details", "submit_prose_intents":
		return "submit_candidate", true
	case "feedback":
		return "submit_feedback", true
	case "approve_impact", "approve_stage", "pause", "resume", "cancel", "record_audit", "fail", "publish":
		return operation, true
	default:
		return "", false
	}
}

// SaveAdaptationRevisionRuntime checkpoints the active adaptation revision.
// An unfenced command may use the project's ordinary RevisionStore; a prepared
// command must present its exact owner capability.
func (s *Store) SaveAdaptationRevisionRuntime(owner *RevisionStore, runtime domain.AdaptationRevisionRuntime) error {
	if err := runtime.Validate(); err != nil {
		return err
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := allowRevisionCommandMutation(state, owner.commandOwner); err != nil {
			return err
		}
		active, exists := state.Sessions[state.ActiveSessionID]
		if exists && active.ID == runtime.SessionID && active.Mode == domain.RevisionModeAdaptation {
			return s.Adaptation.saveRevisionRuntimeRaw(runtime)
		}
		committed, committedExists := state.Sessions[runtime.SessionID]
		if !committedExists || committed.Mode != domain.RevisionModeAdaptation || committed.Stage != domain.RevisionStageCompleted ||
			state.CommandFence == nil || state.CommandFence.Operation != "publish" ||
			!revisionCommandOwnerMatches(state.CommandFence, owner.commandOwner) {
			return fmt.Errorf("adaptation revision runtime %q has no matching active or committed publish owner", runtime.SessionID)
		}
		return s.Adaptation.saveRevisionRuntimeRaw(runtime)
	})
}

// ClearAdaptationRevisionRuntime removes the active adaptation checkpoint under
// the same prepared-command ownership rules as SaveAdaptationRevisionRuntime.
func (s *Store) ClearAdaptationRevisionRuntime(owner *RevisionStore, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := allowRevisionCommandMutation(state, owner.commandOwner); err != nil {
			return err
		}
		active, exists := state.Sessions[state.ActiveSessionID]
		if !exists || active.ID != sessionID || active.Mode != domain.RevisionModeAdaptation {
			return fmt.Errorf("adaptation revision %q does not own runtime cleanup", sessionID)
		}
		return s.Adaptation.clearRevisionRuntimeRaw(sessionID)
	})
}

func (s *Store) withAdaptationRevisionStateWrite(owner *RevisionStore, write func(*revisionState) error) error {
	if s == nil || s.Revisions == nil || s.Adaptation == nil || owner == nil || owner.io == nil {
		return ErrRevisionCommandInProgress
	}
	if !revisionStoresShareProject(s.Revisions, owner) {
		return ErrRevisionCommandInProgress
	}
	return s.Revisions.withRevisionTransaction(func() error {
		state, err := s.Revisions.loadUnlocked()
		if err != nil {
			return err
		}
		return write(state)
	})
}

func revisionStoresShareProject(expected, owner *RevisionStore) bool {
	if expected == nil || expected.io == nil || owner == nil || owner.io == nil {
		return false
	}
	expectedDir, expectedErr := filepath.Abs(expected.io.dir)
	ownerDir, ownerErr := filepath.Abs(owner.io.dir)
	return expectedErr == nil && ownerErr == nil && strings.EqualFold(filepath.Clean(expectedDir), filepath.Clean(ownerDir))
}

func requirePreparedAdaptationRevisionOwner(state *revisionState, owner *RevisionStore, sessionID, operation string) error {
	if state == nil || owner == nil || owner.commandOwner == nil ||
		!revisionCommandOwnerMatches(state.CommandFence, owner.commandOwner) {
		return ErrRevisionCommandInProgress
	}
	sessionID = strings.TrimSpace(sessionID)
	active, exists := state.Sessions[state.ActiveSessionID]
	if !exists || state.ActiveSessionID != sessionID || active.ID != sessionID ||
		active.Mode != domain.RevisionModeAdaptation || !active.Active() {
		return fmt.Errorf("adaptation revision %q does not own %s", sessionID, operation)
	}
	return nil
}

// WithAdaptationRevisionCommand serializes the complete adaptation revision
// service transition across Store and service instances, including processes.
// RevisionStore uses a distinct nested transaction lock for its own state.
func (s *Store) WithAdaptationRevisionCommand(fn func() error) error {
	if s == nil || s.Revisions == nil {
		return fmt.Errorf("adaptation revision store is required")
	}
	return s.withAdaptationRevisionCommandLock(func() error {
		if err := s.recoverAdaptationRevisionCommand(); err != nil {
			return err
		}
		return fn()
	})
}

// WithFoundationAdaptationRevisionCommand grants a narrow, durable command
// fence to the existing adaptation proposal/audit pipeline while a Foundation
// RevisionSession remains the sole active session. The legacy mutation gate
// below permits only target planning operations; every immutable source write
// remains blocked even while this command is running.
func (s *Store) WithFoundationAdaptationRevisionCommand(sessionID, operation string, fn func() error) error {
	if s == nil || s.Revisions == nil || fn == nil {
		return fmt.Errorf("Foundation adaptation revision command is required")
	}
	sessionID, operation = strings.TrimSpace(sessionID), strings.TrimSpace(operation)
	if sessionID == "" || operation == "" {
		return fmt.Errorf("Foundation adaptation revision command identity is required")
	}
	return s.withAdaptationRevisionCommandLock(func() error {
		if err := s.recoverAdaptationRevisionCommand(); err != nil {
			return err
		}
		commandOperation := "foundation-adaptation/" + operation
		fingerprint := domain.ContentSignature([]byte(sessionID + "\x00" + commandOperation))
		owner, err := s.Revisions.claimCommandFence(sessionID, commandOperation, fingerprint)
		if err != nil {
			return err
		}
		if err := s.Revisions.withRevisionTransaction(func() error {
			state, loadErr := s.Revisions.loadUnlocked()
			if loadErr != nil {
				return loadErr
			}
			active, ok := state.Sessions[state.ActiveSessionID]
			if !ok || active.ID != sessionID || active.Mode != domain.RevisionModeFoundation ||
				active.Stage != domain.RevisionStageCandidateGenerating || len(active.Approvals) != 1 {
				return fmt.Errorf("Foundation revision %q does not own adaptation regeneration", sessionID)
			}
			return nil
		}); err != nil {
			return errors.Join(err, owner.releaseCommandFence())
		}
		return errors.Join(fn(), owner.releaseCommandFence())
	})
}

// WithPreparedAdaptationRevisionCommand reserves shared revision/normal-flow
// ownership before the service snapshot is captured. The durable fence remains
// until rollback or receipt-backed cleanup has removed every recovery artifact.
func (s *Store) WithPreparedAdaptationRevisionCommand(key, operation, fingerprint string, fn func(*RevisionStore) error) error {
	if s == nil || s.Revisions == nil {
		return fmt.Errorf("adaptation revision store is required")
	}
	return s.withAdaptationRevisionCommandLock(func() error {
		if err := s.recoverAdaptationRevisionCommand(); err != nil {
			return err
		}
		owner, err := s.Revisions.claimCommandFence(key, operation, fingerprint)
		if err != nil {
			return err
		}
		commandErr := fn(owner)
		pending, pendingErr := s.adaptationRevisionCommandFilesPending()
		if pendingErr != nil || pending {
			return errors.Join(commandErr, pendingErr)
		}
		releaseErr := owner.releaseCommandFence()
		return errors.Join(commandErr, releaseErr)
	})
}

func (s *Store) withAdaptationRevisionCommandLock(fn func() error) error {
	abs, err := filepath.Abs(s.dir)
	if err != nil {
		abs = s.dir
	}
	lock, _ := adaptationRevisionCommandLocks.LoadOrStore(filepath.Clean(abs), new(sync.Mutex))
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()
	return withRevisionFileTransaction(newIO(s.dir), adaptationRevisionCommandLockFile, fn)
}

func (s *Store) PrepareAdaptationRevisionCommand(owner *RevisionStore, key, operation, fingerprint string) error {
	return s.prepareAdaptationRevisionCommand(owner, key, operation, fingerprint, nil)
}

// PrepareAdaptationRevisionPublicationCommand persists the exact internal
// publish receipt identity before formal state can change.
func (s *Store) PrepareAdaptationRevisionPublicationCommand(
	owner *RevisionStore,
	key, fingerprint, sessionID string,
	expectedRevision int,
) error {
	input := RevisionMutationInput{
		SessionID: strings.TrimSpace(sessionID), ExpectedRevision: expectedRevision, IdempotencyKey: strings.TrimSpace(key),
	}
	_, internalFingerprint, err := revisionCommandFingerprint(input.IdempotencyKey, "publish", input)
	if err != nil {
		return err
	}
	publication := &AdaptationRevisionPublicationCommand{
		SessionID: input.SessionID, ExpectedRevision: input.ExpectedRevision, InternalReceiptFingerprint: internalFingerprint,
	}
	if publication.SessionID == "" || publication.ExpectedRevision <= 0 {
		return fmt.Errorf("adaptation revision publication identity is required")
	}
	return s.prepareAdaptationRevisionCommand(owner, key, "publish", fingerprint, publication)
}

func (s *Store) prepareAdaptationRevisionCommand(
	owner *RevisionStore,
	key, operation, fingerprint string,
	publication *AdaptationRevisionPublicationCommand,
) error {
	journal := adaptationRevisionCommandJournal{
		Version: 2, Key: strings.TrimSpace(key), Operation: strings.TrimSpace(operation), Fingerprint: strings.TrimSpace(fingerprint),
		Publication: publication,
	}
	if journal.Key == "" || journal.Operation == "" || journal.Fingerprint == "" {
		return fmt.Errorf("adaptation revision command journal identity is required")
	}
	if err := s.requireAdaptationRevisionCommandOwner(owner, journal.Key, journal.Operation, journal.Fingerprint); err != nil {
		return err
	}
	io := newIO(s.dir)
	if _, err := io.RemoveAllRel(adaptationRevisionCommandSnapshotDir); err != nil {
		return err
	}
	files, err := s.captureAdaptationRevisionCommandSnapshot(io)
	if err != nil {
		_, _ = io.RemoveAllRel(adaptationRevisionCommandSnapshotDir)
		return err
	}
	journal.Files = files
	journal.AuthoritySnapshot, err = capturePublicationAuthoritySnapshot(io)
	if err != nil {
		_, _ = io.RemoveAllRel(adaptationRevisionCommandSnapshotDir)
		return err
	}
	if err := validateAdaptationRevisionCommandJournalIdentity(journal); err != nil {
		_, _ = io.RemoveAllRel(adaptationRevisionCommandSnapshotDir)
		return err
	}
	if err := io.WriteJSON(adaptationRevisionCommandJournalFile, journal); err != nil {
		_, _ = io.RemoveAllRel(adaptationRevisionCommandSnapshotDir)
		return err
	}
	return nil
}

func (s *Store) RollbackAdaptationRevisionCommand(owner *RevisionStore) error {
	io := newIO(s.dir)
	journal, err := loadAdaptationRevisionCommandJournal(io)
	if err != nil {
		return err
	}
	if journal == nil {
		return nil
	}
	if err := s.requireAdaptationRevisionCommandOwner(owner, journal.Key, journal.Operation, journal.Fingerprint); err != nil {
		return err
	}
	if err := s.restoreAdaptationRevisionCommandSnapshot(io, *journal, owner); err != nil {
		return err
	}
	return cleanupAdaptationRevisionCommand(io)
}

func (s *Store) CompleteAdaptationRevisionCommand(owner *RevisionStore, key, operation, fingerprint string) error {
	if err := s.requireAdaptationRevisionCommandOwner(owner, key, operation, fingerprint); err != nil {
		return err
	}
	io := newIO(s.dir)
	journal, err := loadAdaptationRevisionCommandJournal(io)
	if err != nil {
		return err
	}
	if journal == nil || journal.Key != strings.TrimSpace(key) || journal.Operation != strings.TrimSpace(operation) || journal.Fingerprint != strings.TrimSpace(fingerprint) {
		return fmt.Errorf("adaptation revision command journal does not match its owner")
	}
	if operation == "publish" {
		var result *domain.RevisionSession
		found, err := s.LoadVerifiedAdaptationRevisionServiceReceipt(key, operation, fingerprint, &result)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("adaptation revision command receipt is not durable")
		}
		internalResult, committed, err := s.committedAdaptationPublicationResult(journal.Key, journal.Fingerprint, journal.Publication, true)
		if err != nil {
			return fmt.Errorf("verify adaptation publication before prepared cleanup: %w", err)
		}
		if !committed || !sameCommittedRevisionResult(internalResult, result) {
			return fmt.Errorf("adaptation service receipt does not match its committed publication")
		}
		if err := clearCommittedAdaptationRevisionRuntime(io, result.ID); err != nil {
			return err
		}
	} else {
		found, err := s.LoadVerifiedAdaptationRevisionServiceReceipt(key, operation, fingerprint, nil)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("adaptation revision command receipt is not durable")
		}
	}
	return cleanupAdaptationRevisionCommand(io)
}

func (s *Store) requireAdaptationRevisionCommandOwner(owner *RevisionStore, key, operation, fingerprint string) error {
	if s == nil || s.Revisions == nil || s.Revisions.io == nil || owner == nil || owner.io == nil {
		return ErrRevisionCommandInProgress
	}
	storeDir, storeErr := filepath.Abs(s.Revisions.io.dir)
	ownerDir, ownerErr := filepath.Abs(owner.io.dir)
	if storeErr != nil || ownerErr != nil || !strings.EqualFold(filepath.Clean(storeDir), filepath.Clean(ownerDir)) {
		return ErrRevisionCommandInProgress
	}
	return owner.requireCommandFenceFor(key, operation, fingerprint)
}

func (s *Store) recoverAdaptationRevisionCommand() error {
	io := newIO(s.dir)
	journal, err := loadAdaptationRevisionCommandJournal(io)
	if err != nil {
		return fmt.Errorf("recover adaptation revision command journal: %w", err)
	}
	if journal == nil {
		if _, err := io.RemoveAllRel(adaptationRevisionCommandSnapshotDir); err != nil {
			return err
		}
		owner, err := s.Revisions.currentCommandOwner()
		if err != nil || owner == nil {
			return err
		}
		return owner.releaseCommandFence()
	}
	owner, err := s.Revisions.claimCommandFence(journal.Key, journal.Operation, journal.Fingerprint)
	if err != nil {
		return fmt.Errorf("recover adaptation revision command ownership: %w", err)
	}
	var serviceResult *domain.RevisionSession
	var committed bool
	var receiptErr error
	if journal.Operation == "publish" {
		committed, receiptErr = s.LoadVerifiedAdaptationRevisionServiceReceipt(journal.Key, journal.Operation, journal.Fingerprint, &serviceResult)
	} else {
		committed, receiptErr = s.LoadVerifiedAdaptationRevisionServiceReceipt(journal.Key, journal.Operation, journal.Fingerprint, nil)
	}
	if receiptErr != nil {
		return fmt.Errorf("read interrupted adaptation revision command receipt: %w", receiptErr)
	}
	if committed {
		if journal.Operation == "publish" {
			internalResult, internalCommitted, err := s.committedAdaptationRevisionCommandResult(*journal)
			if err != nil {
				return fmt.Errorf("verify receipt-backed adaptation publication: %w", err)
			}
			if !internalCommitted || !sameCommittedRevisionResult(internalResult, serviceResult) {
				return fmt.Errorf("adaptation service receipt does not match its internal publication receipt")
			}
			input, err := committedAdaptationPublicationInput(journal.Key, internalResult)
			if err != nil {
				return err
			}
			replayed, found, finalizeErr := owner.FinalizeCommittedPublication(input)
			if finalizeErr != nil {
				return fmt.Errorf("finalize receipt-backed adaptation publication: %w", finalizeErr)
			}
			if !found || !sameCommittedRevisionResult(internalResult, replayed) {
				return fmt.Errorf("receipt-backed adaptation publication changed during finalize")
			}
			if err := clearCommittedAdaptationRevisionRuntime(io, serviceResult.ID); err != nil {
				return err
			}
		}
		if err := cleanupAdaptationRevisionCommand(io); err != nil {
			return err
		}
		return owner.releaseCommandFence()
	}
	internalResult, internalCommitted, internalErr := s.committedAdaptationRevisionCommandResult(*journal)
	if internalErr != nil {
		return fmt.Errorf("verify interrupted adaptation revision publication: %w", internalErr)
	}
	if internalCommitted {
		input, err := committedAdaptationPublicationInput(journal.Key, internalResult)
		if err != nil {
			return err
		}
		replayed, found, finalizeErr := owner.FinalizeCommittedPublication(input)
		if finalizeErr != nil {
			return fmt.Errorf("finalize interrupted adaptation revision publication: %w", finalizeErr)
		}
		if !found || replayed == nil || !sameCommittedRevisionResult(internalResult, replayed) {
			return fmt.Errorf("interrupted adaptation revision publication receipt changed during finalize")
		}
		if err := s.SaveAdaptationRevisionServiceReceipt(owner, journal.Key, journal.Operation, journal.Fingerprint, replayed); err != nil {
			return fmt.Errorf("repair interrupted adaptation revision service receipt: %w", err)
		}
		if err := clearCommittedAdaptationRevisionRuntime(io, replayed.ID); err != nil {
			return err
		}
		if err := cleanupAdaptationRevisionCommand(io); err != nil {
			return err
		}
		return owner.releaseCommandFence()
	}
	if err := s.restoreAdaptationRevisionCommandSnapshot(io, *journal, owner); err != nil {
		return fmt.Errorf("roll back interrupted adaptation revision command: %w", err)
	}
	if err := cleanupAdaptationRevisionCommand(io); err != nil {
		return err
	}
	return owner.releaseCommandFence()
}

func (s *Store) committedAdaptationRevisionCommandResult(journal adaptationRevisionCommandJournal) (*domain.RevisionSession, bool, error) {
	if journal.Operation != "publish" {
		return nil, false, nil
	}
	if err := validateAdaptationRevisionCommandJournalIdentity(journal); err != nil {
		return nil, false, err
	}
	return s.committedAdaptationPublicationResult(journal.Key, journal.Fingerprint, journal.Publication, true)
}

// VerifyCommittedAdaptationPublication binds a terminal service receipt to the
// internal revision receipt and every formal and authority fact on every replay.
func (s *Store) VerifyCommittedAdaptationPublication(key, serviceFingerprint string, serviceResult *domain.RevisionSession) error {
	key, serviceFingerprint = strings.TrimSpace(key), strings.TrimSpace(serviceFingerprint)
	if key == "" || serviceFingerprint == "" {
		return fmt.Errorf("adaptation publication service receipt identity is required")
	}
	internalResult, committed, err := s.committedAdaptationPublicationResult(key, serviceFingerprint, nil, false)
	if err != nil {
		return err
	}
	if !committed || !sameCommittedRevisionResult(internalResult, serviceResult) {
		return fmt.Errorf("adaptation service receipt does not match its committed publication")
	}
	return nil
}

func (s *Store) committedAdaptationPublicationResult(
	key string,
	serviceFingerprint string,
	publication *AdaptationRevisionPublicationCommand,
	allowLegacyMigration bool,
) (*domain.RevisionSession, bool, error) {
	var result *domain.RevisionSession
	err := s.Revisions.withRevisionTransaction(func() error {
		var state revisionState
		if err := readProjectJSONStrict(s.Revisions.io, revisionStateFile, &state); err != nil {
			return err
		}
		if err := validateRevisionState(&state); err != nil {
			return err
		}
		receipt, exists := state.Receipts[strings.TrimSpace(key)]
		if !exists {
			return nil
		}
		if receipt.Operation != "publish" {
			return ErrRevisionIdempotencyConflict
		}
		found, err := cloneRevisionSession(receipt.Result)
		if err != nil {
			return err
		}
		if err := validateCommittedAdaptationServiceResult(found); err != nil {
			return err
		}
		input, err := committedAdaptationPublicationInput(key, found)
		if err != nil {
			return err
		}
		_, rawInternalFingerprint, err := revisionCommandFingerprint(key, "publish", input)
		if err != nil {
			return fmt.Errorf("reconstruct adaptation internal publication receipt fingerprint: %w", err)
		}
		expectedReceiptFingerprint := rawInternalFingerprint
		if receipt.ServiceOperation != "" {
			if receipt.ServiceOperation != "publish" || receipt.ServiceFingerprint != strings.TrimSpace(serviceFingerprint) {
				return fmt.Errorf("adaptation internal publication receipt service identity is invalid")
			}
			expectedReceiptFingerprint = adaptationRevisionServiceBoundInternalFingerprint(
				key,
				"publish",
				rawInternalFingerprint,
				receipt.ServiceOperation,
				receipt.ServiceFingerprint,
			)
		}
		if receipt.Fingerprint != expectedReceiptFingerprint {
			return fmt.Errorf("adaptation internal publication receipt fingerprint is invalid")
		}
		if publication != nil && (found.ID != publication.SessionID || input.ExpectedRevision != publication.ExpectedRevision ||
			rawInternalFingerprint != publication.InternalReceiptFingerprint) {
			return fmt.Errorf("adaptation prepared publication binding is invalid")
		}
		// The publication generation is immutable on the completed session and
		// signed publication receipt, while the revision state's generation may
		// legitimately advance when later normal-flow leases are acquired and
		// released. Reject rewind/substitution without treating those unrelated
		// successors as publication tampering.
		if state.Generation < found.Generation || !reflect.DeepEqual(state.Sessions[found.ID], *found) {
			return fmt.Errorf("adaptation internal publication receipt binding is invalid")
		}
		result = found
		return withExpansionAuthorityRootOperation(func() error {
			legacy, _, err := validateCommittedAdaptationPublicationFiles(
				s.Revisions.io, &state, *found, key, serviceFingerprint, rawInternalFingerprint, allowLegacyMigration,
			)
			if err != nil {
				return err
			}
			if !legacy {
				return nil
			}
			return fmt.Errorf("legacy adaptation publication service binding requires versioned manual recovery")
		})
	})
	if err != nil {
		return nil, false, err
	}
	return result, result != nil, nil
}

func committedAdaptationPublicationInput(key string, result *domain.RevisionSession) (RevisionMutationInput, error) {
	if err := validateCommittedAdaptationServiceResult(result); err != nil {
		return RevisionMutationInput{}, err
	}
	if result.Revision <= 1 {
		return RevisionMutationInput{}, fmt.Errorf("adaptation publication revision cannot identify its expected revision")
	}
	return RevisionMutationInput{
		SessionID: result.ID, ExpectedRevision: result.Revision - 1, IdempotencyKey: strings.TrimSpace(key),
	}, nil
}

func validateCommittedAdaptationPublicationFiles(
	io *IO,
	state *revisionState,
	result domain.RevisionSession,
	key, serviceFingerprint, internalFingerprint string,
	allowLegacy bool,
) (bool, ExpansionPublicationReceipt, error) {
	var trust ExpansionPublicationTrust
	if err := readProjectJSONStrict(io, expansionPublicationTrustFile, &trust); err != nil {
		return false, ExpansionPublicationReceipt{}, fmt.Errorf("load committed adaptation publication trust: %w", err)
	}
	var receipt ExpansionPublicationReceipt
	if err := readProjectJSONStrict(io, expansionPublicationReceiptFile, &receipt); err != nil {
		return false, receipt, fmt.Errorf("load committed adaptation publication receipt: %w", err)
	}
	if err := verifyExpansionPublicationTrustLocked(trust); err != nil {
		return false, receipt, err
	}
	if err := validateExpansionPublicationReceipt(receipt); err != nil {
		return false, receipt, err
	}
	if receipt.Mode != domain.RevisionModeAdaptation || receipt.SessionID != result.ID ||
		receipt.SessionRevision != result.Revision || receipt.PublicationGeneration != result.Generation ||
		receipt.AcceptedRevisionID != result.ID || receipt.ProjectID != trust.ProjectID ||
		receipt.ProjectInstance != trust.ProjectInstance || receipt.KeyID != trust.KeyID ||
		receipt.KeyEpoch != trust.Epoch || !verifyExpansionPublicationSignature(trust, receipt) {
		return false, receipt, fmt.Errorf("committed adaptation publication receipt binding is invalid")
	}
	current, err := publicationArtifactBindings(state)
	if err != nil || !slices.Equal(current, receipt.CurrentArtifacts) {
		return false, receipt, fmt.Errorf("committed adaptation publication artifacts changed")
	}
	volumes, kind, err := loadPublicationFormalStructure(io, domain.RevisionModeAdaptation)
	if err != nil {
		return false, receipt, fmt.Errorf("load committed adaptation formal structure: %w", err)
	}
	if kind != receipt.StructureArtifactKind || domain.StructureSignature(volumes) != receipt.StructureSignature {
		return false, receipt, fmt.Errorf("committed adaptation formal structure changed")
	}
	recordPath, err := authorityProjectRecordPath(trust.ProjectInstance)
	if err != nil {
		return false, receipt, err
	}
	var record expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSONStrict(recordPath, &record); err != nil {
		return false, receipt, fmt.Errorf("load committed adaptation authority record: %w", err)
	}
	if _, err := loadExpansionProjectPrivate(trust); err != nil {
		return false, receipt, err
	}
	if record.Version != expansionAuthorityRootVersion || record.ProjectID != trust.ProjectID || record.ProjectInstance != trust.ProjectInstance ||
		record.KeyID != trust.KeyID || record.Epoch != trust.Epoch || record.PublicKey != trust.PublicKey ||
		record.RevokedBefore > trust.Epoch {
		return false, receipt, fmt.Errorf("committed adaptation authority record binding is invalid")
	}
	expectedBinding, err := adaptationPublicationServiceBindingDigest(
		key, "publish", serviceFingerprint, internalFingerprint, result, receipt,
	)
	if err != nil {
		return false, receipt, err
	}
	if receipt.AdaptationServiceBinding == "" {
		if !allowLegacy {
			return false, receipt, fmt.Errorf("committed adaptation publication service binding is missing")
		}
		return true, receipt, nil
	}
	if receipt.AdaptationServiceBinding != expectedBinding {
		return false, receipt, fmt.Errorf("committed adaptation publication service binding is invalid")
	}
	return false, receipt, nil
}

func readProjectJSONStrict(io *IO, rel string, target any) error {
	data, err := io.ReadFile(rel)
	if err != nil {
		return err
	}
	return decodeStrictUniqueJSON(data, target)
}

func validateCommittedAdaptationServiceResult(result *domain.RevisionSession) error {
	if result == nil || result.ID == "" || result.Mode != domain.RevisionModeAdaptation ||
		result.Stage != domain.RevisionStageCompleted || result.Revision <= 0 || result.Generation == 0 {
		return fmt.Errorf("committed adaptation service result is invalid")
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("committed adaptation service result is invalid: %w", err)
	}
	return nil
}

func sameCommittedRevisionResult(expected, actual *domain.RevisionSession) bool {
	return expected != nil && actual != nil && reflect.DeepEqual(*expected, *actual)
}

func clearCommittedAdaptationRevisionRuntime(io *IO, sessionID string) error {
	var runtime domain.AdaptationRevisionRuntime
	if err := io.ReadJSON(adaptationRevisionRuntimeFile, &runtime); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := runtime.Validate(); err != nil {
		return err
	}
	if runtime.SessionID != strings.TrimSpace(sessionID) {
		return fmt.Errorf("committed adaptation runtime belongs to another session")
	}
	if err := injectAdaptationRevisionCommandCleanupFault(adaptationRevisionRuntimeFile, "before_delete"); err != nil {
		return err
	}
	if err := io.RemoveFile(adaptationRevisionRuntimeFile); err != nil {
		return err
	}
	return injectAdaptationRevisionCommandCleanupFault(adaptationRevisionRuntimeFile, "after_delete")
}

func (s *Store) adaptationRevisionCommandPending() (bool, error) {
	pending, err := s.adaptationRevisionCommandFilesPending()
	if err != nil || pending {
		return pending, err
	}
	if _, err := os.Stat(newIO(s.dir).path(revisionStateFile)); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	fence, err := s.Revisions.currentCommandFence()
	return fence != nil, err
}

func (s *Store) adaptationRevisionCommandFilesPending() (bool, error) {
	io := newIO(s.dir)
	for _, rel := range []string{adaptationRevisionCommandJournalFile, adaptationRevisionCommandSnapshotDir} {
		if _, err := os.Stat(io.path(rel)); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func (s *Store) captureAdaptationRevisionCommandSnapshot(io *IO) ([]string, error) {
	files := make([]string, 0)
	for _, rel := range adaptationRevisionCommandTrackedFiles() {
		data, err := io.ReadFile(rel)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if err := writeAdaptationRevisionCommandFile(io, filepath.ToSlash(filepath.Join(adaptationRevisionCommandSnapshotDir, filepath.FromSlash(rel))), data); err != nil {
			return nil, err
		}
		files = append(files, rel)
	}
	root := io.path(structureRootDir)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeAdaptationRevisionCommandFile(io, filepath.ToSlash(filepath.Join(adaptationRevisionCommandSnapshotDir, filepath.FromSlash(rel))), data); err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

func (s *Store) restoreAdaptationRevisionCommandSnapshot(io *IO, journal adaptationRevisionCommandJournal, owner *RevisionStore) error {
	if err := validateAdaptationRevisionCommandJournalIdentity(journal); err != nil {
		return fmt.Errorf("invalid adaptation revision command journal: %w", err)
	}
	if journal.Version == 2 && journal.AuthoritySnapshot.Version != 2 {
		return fmt.Errorf("invalid adaptation revision command authority snapshot")
	}
	if owner == nil {
		return ErrRevisionCommandInProgress
	}
	if err := owner.requireCommandFence(); err != nil {
		return err
	}
	fence, err := owner.currentCommandFence()
	if err != nil {
		return err
	}
	if !revisionCommandMatches(fence, journal.Key, journal.Operation, journal.Fingerprint) || !revisionCommandOwnerMatches(fence, owner.commandOwner) {
		return ErrRevisionCommandInProgress
	}
	snapshotFiles, err := loadAdaptationRevisionCommandSnapshotFiles(io, journal)
	if err != nil {
		return err
	}
	if journal.Version == 2 {
		if err := restorePublicationAuthoritySnapshot(io, journal.AuthoritySnapshot); err != nil {
			return fmt.Errorf("restore adaptation publication authority: %w", err)
		}
	}
	if _, err := io.RemoveAllRel(structureRootDir); err != nil {
		return err
	}
	existing := make(map[string]bool, len(journal.Files))
	for _, rel := range journal.Files {
		existing[rel] = true
	}
	for _, rel := range adaptationRevisionCommandTrackedFilesForVersion(journal.Version) {
		if existing[rel] {
			continue
		}
		if rel == revisionStateFile {
			state := newRevisionState()
			state.CommandFence = fence
			if err := io.WriteJSON(revisionStateFile, state); err != nil {
				return err
			}
			continue
		}
		if err := io.RemoveFile(rel); err != nil {
			return err
		}
	}
	for _, rel := range journal.Files {
		data := snapshotFiles[rel]
		if rel == revisionStateFile {
			var state revisionState
			if err := json.Unmarshal(data, &state); err != nil {
				return fmt.Errorf("decode adaptation revision command snapshot %s: %w", rel, err)
			}
			state.CommandFence = fence
			data, err = json.MarshalIndent(state, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')
		}
		if err := writeAdaptationRevisionCommandFile(io, rel, data); err != nil {
			return fmt.Errorf("restore adaptation revision command snapshot %s: %w", rel, err)
		}
	}
	return nil
}

func loadAdaptationRevisionCommandSnapshotFiles(io *IO, journal adaptationRevisionCommandJournal) (map[string][]byte, error) {
	result := make(map[string][]byte, len(journal.Files))
	for _, rel := range journal.Files {
		snapshotRel := filepath.ToSlash(filepath.Join(adaptationRevisionCommandSnapshotDir, filepath.FromSlash(rel)))
		path, err := io.safeRelPath(snapshotRel)
		if err != nil {
			return nil, fmt.Errorf("resolve adaptation revision command snapshot %s: %w", rel, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect adaptation revision command snapshot %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("adaptation revision command snapshot %s is not a regular file", rel)
		}
		data, err := io.ReadFile(snapshotRel)
		if err != nil {
			return nil, fmt.Errorf("read adaptation revision command snapshot %s: %w", rel, err)
		}
		if rel == revisionStateFile {
			var state revisionState
			if err := decodeStrictUniqueJSON(data, &state); err != nil {
				return nil, fmt.Errorf("decode adaptation revision command snapshot %s: %w", rel, err)
			}
		}
		if rel == adaptationRevisionServiceReceiptsFile {
			if _, err := decodeAdaptationRevisionServiceReceiptState(data); err != nil {
				return nil, fmt.Errorf("decode adaptation revision command snapshot %s: %w", rel, err)
			}
		}
		if rel == adaptationRevisionRuntimeFile {
			var runtime domain.AdaptationRevisionRuntime
			if err := decodeStrictUniqueJSON(data, &runtime); err != nil {
				return nil, fmt.Errorf("decode adaptation revision command snapshot %s: %w", rel, err)
			}
			if err := runtime.Validate(); err != nil {
				return nil, fmt.Errorf("validate adaptation revision command snapshot %s: %w", rel, err)
			}
		}
		result[rel] = data
	}
	return result, nil
}

func adaptationRevisionCommandTrackedFiles() []string {
	files := append([]string(nil), adaptationRevisionFormalFiles...)
	files = append(files,
		adaptationRevisionRuntimeFile,
		adaptationRevisionServiceReceiptsFile,
		expansionPublicationReceiptFile,
		expansionPublicationTrustFile,
		revisionStateFile,
	)
	slices.Sort(files)
	return slices.Compact(files)
}

func adaptationRevisionCommandTrackedFilesForVersion(version int) []string {
	if version == 1 {
		files := append([]string(nil), adaptationRevisionFormalFiles...)
		files = append(files, adaptationRevisionRuntimeFile, adaptationRevisionServiceReceiptsFile, revisionStateFile)
		slices.Sort(files)
		return slices.Compact(files)
	}
	return adaptationRevisionCommandTrackedFiles()
}

func loadAdaptationRevisionCommandJournal(io *IO) (*adaptationRevisionCommandJournal, error) {
	data, err := io.ReadFile(adaptationRevisionCommandJournalFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := validateUniqueJSONDocument(data); err != nil {
		return nil, err
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	var journal adaptationRevisionCommandJournal
	switch header.Version {
	case 1:
		var legacy adaptationRevisionCommandJournalV1
		if err := decodeExactJSON(data, &legacy); err != nil {
			return nil, err
		}
		journal = adaptationRevisionCommandJournal{
			Version: legacy.Version, Key: legacy.Key, Operation: legacy.Operation,
			Fingerprint: legacy.Fingerprint, Files: legacy.Files,
		}
	case 2:
		var current adaptationRevisionCommandJournalV2
		if err := decodeExactJSON(data, &current); err != nil {
			return nil, err
		}
		journal = adaptationRevisionCommandJournal{
			Version: current.Version, Key: current.Key, Operation: current.Operation,
			Fingerprint: current.Fingerprint, Files: current.Files,
			AuthoritySnapshot: current.AuthoritySnapshot, Publication: current.Publication,
		}
	default:
		return nil, fmt.Errorf("unsupported adaptation revision command journal version %d", header.Version)
	}
	if err := validateAdaptationRevisionCommandJournalIdentity(journal); err != nil {
		return nil, err
	}
	return &journal, nil
}

func validateAdaptationRevisionCommandJournalIdentity(journal adaptationRevisionCommandJournal) error {
	if (journal.Version != 1 && journal.Version != 2) || strings.TrimSpace(journal.Key) == "" || strings.TrimSpace(journal.Operation) == "" || strings.TrimSpace(journal.Fingerprint) == "" ||
		journal.Key != strings.TrimSpace(journal.Key) || journal.Operation != strings.TrimSpace(journal.Operation) || journal.Fingerprint != strings.TrimSpace(journal.Fingerprint) {
		return fmt.Errorf("adaptation revision command identity is invalid")
	}
	if err := validateAdaptationRevisionCommandSnapshotFiles(journal.Version, journal.Files); err != nil {
		return err
	}
	if journal.Version == 1 {
		if journal.Publication != nil || !reflect.DeepEqual(journal.AuthoritySnapshot, publicationAuthoritySnapshot{}) {
			return fmt.Errorf("legacy adaptation revision command contains unsupported current fields")
		}
		return nil
	}
	if journal.AuthoritySnapshot.Version != 2 {
		return fmt.Errorf("adaptation revision command authority snapshot is invalid")
	}
	if journal.Publication == nil {
		if journal.Operation == "publish" {
			return fmt.Errorf("adaptation revision publication binding is required")
		}
		return nil
	}
	publication := journal.Publication
	if journal.Operation != "publish" || strings.TrimSpace(publication.SessionID) == "" || publication.ExpectedRevision <= 0 ||
		!isLowerHex64(publication.InternalReceiptFingerprint) {
		return fmt.Errorf("adaptation revision publication identity is invalid")
	}
	input := RevisionMutationInput{
		SessionID: publication.SessionID, ExpectedRevision: publication.ExpectedRevision, IdempotencyKey: journal.Key,
	}
	_, fingerprint, err := revisionCommandFingerprint(journal.Key, "publish", input)
	if err != nil || fingerprint != publication.InternalReceiptFingerprint {
		return fmt.Errorf("adaptation revision internal receipt fingerprint is invalid")
	}
	return nil
}

func validateAdaptationRevisionCommandSnapshotFiles(version int, files []string) error {
	allowed := make(map[string]struct{})
	for _, rel := range adaptationRevisionCommandTrackedFilesForVersion(version) {
		allowed[rel] = struct{}{}
	}
	previous := ""
	for _, rel := range files {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
		_, tracked := allowed[rel]
		structure := strings.HasPrefix(rel, structureRootDir+"/")
		if rel == "" || rel != clean || filepath.IsAbs(filepath.FromSlash(rel)) || filepath.VolumeName(filepath.FromSlash(rel)) != "" ||
			(!tracked && !structure) || (previous != "" && rel <= previous) {
			return fmt.Errorf("adaptation revision command snapshot path %q is invalid", rel)
		}
		previous = rel
	}
	return nil
}

func validateUniqueJSONDocument(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := validateUniqueJSONValue(decoder); err != nil {
		return err
	}
	return ensureSingleJSONValue(decoder)
}

func validateUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := validateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := validateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("JSON array is not terminated")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func cleanupAdaptationRevisionCommand(io *IO) error {
	if err := injectAdaptationRevisionCommandCleanupFault(adaptationRevisionCommandJournalFile, "before_delete"); err != nil {
		return err
	}
	if err := io.RemoveFile(adaptationRevisionCommandJournalFile); err != nil {
		return err
	}
	if err := injectAdaptationRevisionCommandCleanupFault(adaptationRevisionCommandJournalFile, "after_delete"); err != nil {
		return err
	}
	if err := injectAdaptationRevisionCommandCleanupFault(adaptationRevisionCommandSnapshotDir, "before_delete"); err != nil {
		return err
	}
	_, err := io.RemoveAllRel(adaptationRevisionCommandSnapshotDir)
	if err != nil {
		return err
	}
	return injectAdaptationRevisionCommandCleanupFault(adaptationRevisionCommandSnapshotDir, "after_delete")
}

func injectAdaptationRevisionCommandCleanupFault(path, stage string) error {
	if adaptationRevisionCommandCleanupFault == nil {
		return nil
	}
	return adaptationRevisionCommandCleanupFault(filepath.ToSlash(path), stage)
}

func writeAdaptationRevisionCommandFile(io *IO, rel string, data []byte) error {
	if strings.HasSuffix(filepath.ToSlash(rel), expansionPublicationPrivateKeyFile) {
		return writeFileAtomicWithMode(io, rel, data, 0o600)
	}
	return io.WithWriteLock(func() error { return io.WriteFileUnlocked(rel, data) })
}

func (s *AdaptationStore) LoadRevisionRuntime() (*domain.AdaptationRevisionRuntime, error) {
	var runtime domain.AdaptationRevisionRuntime
	if err := s.io.ReadJSON(adaptationRevisionRuntimeFile, &runtime); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := runtime.Validate(); err != nil {
		return nil, err
	}
	return &runtime, nil
}

func (s *Store) SaveAdaptationPlanForRevision(owner *RevisionStore, plan domain.AdaptationPlan, sessionID string) error {
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "the formal plan write"); err != nil {
			return err
		}
		return s.Adaptation.savePlan(plan)
	})
}

func (s *Store) RestoreAdaptationPlanForRevision(owner *RevisionStore, plan domain.AdaptationPlan, sessionID string) error {
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "the rollback plan"); err != nil {
			return err
		}
		return s.Adaptation.savePlan(plan)
	})
}

func (s *AdaptationStore) clearRevisionRuntimeRaw(sessionID string) error {
	runtime, err := s.LoadRevisionRuntime()
	if err != nil {
		return err
	}
	if runtime != nil && runtime.SessionID != strings.TrimSpace(sessionID) {
		return fmt.Errorf("adaptation revision runtime belongs to %s", runtime.SessionID)
	}
	return s.io.RemoveFile(adaptationRevisionRuntimeFile)
}

func (s *Store) CaptureAdaptationFormalSnapshot() (*AdaptationFormalSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("store is required")
	}
	io := newIO(s.dir)
	snapshot := &AdaptationFormalSnapshot{
		files:          make(map[string]adaptationRevisionFileSnapshot, len(adaptationRevisionFormalFiles)),
		structureFiles: make(map[string][]byte),
	}
	for _, path := range adaptationRevisionFormalFiles {
		data, err := io.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				snapshot.files[path] = adaptationRevisionFileSnapshot{}
				continue
			}
			return nil, err
		}
		snapshot.files[path] = adaptationRevisionFileSnapshot{exists: true, data: append([]byte(nil), data...)}
	}
	structureRoot := io.path(structureRootDir)
	if err := filepath.WalkDir(structureRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot.structureFiles[filepath.ToSlash(rel)] = append([]byte(nil), data...)
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return snapshot, nil
}

func (s *Store) RestoreAdaptationFormalSnapshot(owner *RevisionStore, snapshot *AdaptationFormalSnapshot, sessionID string) error {
	if s == nil || snapshot == nil {
		return fmt.Errorf("adaptation formal snapshot is required")
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "formal snapshot restore"); err != nil {
			return err
		}
		io := newIO(s.dir)
		return io.WithWriteLock(func() error {
			if _, err := io.RemoveAllRelUnlocked(structureRootDir); err != nil {
				return err
			}
			for path, data := range snapshot.structureFiles {
				if err := io.WriteFileUnlocked(path, data); err != nil {
					return err
				}
			}
			for _, path := range adaptationRevisionFormalFiles {
				file := snapshot.files[path]
				if file.exists {
					if err := io.WriteFileUnlocked(path, file.data); err != nil {
						return err
					}
					continue
				}
				if err := io.RemoveFileUnlocked(path); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func (s *Store) ClearAdaptationRevisionAudits(owner *RevisionStore, sessionID string) error {
	if s == nil {
		return fmt.Errorf("store is required")
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "revision audit cleanup"); err != nil {
			return err
		}
		io := newIO(s.dir)
		return io.WithWriteLock(func() error {
			if err := io.RemoveFileUnlocked(adaptationAuditReportFile); err != nil {
				return err
			}
			return io.RemoveFileUnlocked(adaptationRepairApplicationFile)
		})
	})
}

func (s *Store) SaveAdaptationRevisionProgress(owner *RevisionStore, progress *domain.Progress, sessionID string) error {
	if progress == nil {
		return fmt.Errorf("adaptation revision progress is required")
	}
	return s.withAdaptationRevisionStateWrite(owner, func(state *revisionState) error {
		if err := requirePreparedAdaptationRevisionOwner(state, owner, sessionID, "formal progress write"); err != nil {
			return err
		}
		return s.Progress.saveOwned(cloneProgress(progress))
	})
}
