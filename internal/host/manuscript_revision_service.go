package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	hostadapt "github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type ManuscriptPreviewRequest struct {
	ChapterID   string                           `json:"chapter_id"`
	Instruction string                           `json:"instruction"`
	Kind        domain.ManuscriptInstructionKind `json:"kind"`
}

type ManuscriptPreview struct {
	Runtime   *domain.ManuscriptRevisionRuntime `json:"runtime"`
	Escalated bool                              `json:"escalated"`
	Reason    string                            `json:"reason,omitempty"`
}

type ManuscriptCandidateInput struct {
	ChapterID                string                     `json:"chapter_id"`
	Prose                    string                     `json:"prose"`
	Sidecars                 map[string]json.RawMessage `json:"sidecars"`
	AuditSignature           string                     `json:"audit_signature,omitempty"`
	DeferContractAudit       bool                       `json:"-"`
	PreserveSemanticSidecars bool                       `json:"-"`
}

type ManualManuscriptCandidateRequest struct {
	ChapterID        string `json:"chapter_id"`
	ExpectedProseSHA string `json:"expected_prose_sha256"`
	Prose            string `json:"prose"`
}

type ManuscriptRevisionService struct {
	store                  *storepkg.Store
	writer                 ManuscriptWriter
	auditor                ManuscriptAuditor
	beforeRestoreOwnership func()
	beforeRestoreEvidence  func() error
	beforeRestoreCommit    func() error
}

func (s *ManuscriptRevisionService) requireWriteReady() error {
	if s == nil || s.store == nil {
		return &domain.ManuscriptRevisionError{Class: "publication_recovery_required", Err: fmt.Errorf("manuscript store is unavailable")}
	}
	if err := s.store.RequireManuscriptWriteReady(); err != nil {
		return &domain.ManuscriptRevisionError{Class: "publication_recovery_required", Err: err}
	}
	return nil
}

type ManuscriptPlan struct {
	StoryChanged       bool
	Outline            domain.OutlineEntry
	Contract           domain.NarrativeContract
	ImpactedChapterIDs []string
}

type ManuscriptGeneratedSegment struct {
	ChapterID          string
	Attempt            int
	Segment            int
	Prose              string
	Sidecars           map[string]json.RawMessage
	Complete           bool
	Truncated          bool
	DependencyEvidence string
}

type ManuscriptWriter interface {
	PlanManuscriptRevision(context.Context, domain.ManuscriptBaseline, string, domain.ManuscriptInstructionKind) (ManuscriptPlan, error)
	GenerateManuscriptSegment(context.Context, domain.ManuscriptRevisionRuntime, domain.ManuscriptReworkItem, ManuscriptGenerationContext, int, int, string) (ManuscriptGeneratedSegment, error)
}

type ManuscriptAuditor interface {
	AuditManuscriptCandidate(context.Context, domain.ManuscriptRevisionRuntime, domain.ManuscriptCandidate) (passed bool, report string, err error)
}

type ManuscriptEvidenceLocation struct {
	Field     string `json:"field"`
	StartRune int    `json:"start_rune"`
	EndRune   int    `json:"end_rune"`
	Quote     string `json:"quote"`
}

type ManuscriptContractAuditTask struct {
	RevisionID                   string              `json:"revision_id"`
	ChapterID                    string              `json:"chapter_id"`
	CandidateSHA256              string              `json:"candidate_sha256"`
	OutlineSHA256                string              `json:"outline_sha256"`
	Outline                      domain.OutlineEntry `json:"outline"`
	ProtectedState               map[string]string   `json:"protected_state"`
	ProtectedStateSHA256         string              `json:"protected_state_sha256"`
	AuthoritativeOutlineSHA256   string              `json:"authoritative_outline_sha256"`
	AuthoritativeStructureSHA256 string              `json:"authoritative_structure_sha256"`
	Role                         string              `json:"role"`
	Signature                    string              `json:"signature"`
}

type ManuscriptContractAuditDecision struct {
	Role            string                       `json:"role"`
	TaskSignature   string                       `json:"task_signature"`
	CandidateSHA256 string                       `json:"candidate_sha256"`
	Contract        domain.NarrativeContract     `json:"contract"`
	Evidence        []ManuscriptEvidenceLocation `json:"evidence"`
}

type ManuscriptContractVerificationTask struct {
	RevisionID            string `json:"revision_id"`
	ChapterID             string `json:"chapter_id"`
	CandidateSHA256       string `json:"candidate_sha256"`
	LocatorTaskSignature  string `json:"locator_task_signature"`
	LocatorDecisionSHA256 string `json:"locator_decision_sha256"`
	Role                  string `json:"role"`
	Signature             string `json:"signature"`
}

type ManuscriptContractVerificationReceipt struct {
	Field           string `json:"field"`
	Value           string `json:"value"`
	ApprovedValue   string `json:"approved_value"`
	StartRune       int    `json:"start_rune"`
	EndRune         int    `json:"end_rune"`
	Quote           string `json:"quote"`
	Verdict         string `json:"verdict"`
	TaskSignature   string `json:"task_signature"`
	CandidateSHA256 string `json:"candidate_sha256"`
}

type ManuscriptContractVerificationDecision struct {
	Role            string                                  `json:"role"`
	TaskSignature   string                                  `json:"task_signature"`
	CandidateSHA256 string                                  `json:"candidate_sha256"`
	Receipts        []ManuscriptContractVerificationReceipt `json:"receipts"`
}

// ManuscriptStructuredContractAuditor is deliberately separate from the
// writer. It must derive the protected contract from the complete candidate,
// approved outline and server-reread state; writer sidecars are never input.
type ManuscriptStructuredContractAuditor interface {
	AuditCandidateContract(context.Context, ManuscriptContractAuditTask, string) (ManuscriptContractAuditDecision, error)
	VerifyCandidateContract(context.Context, ManuscriptContractVerificationTask, ManuscriptContractAuditDecision, domain.NarrativeContract, string) (ManuscriptContractVerificationDecision, error)
}

type ManuscriptAdaptationFinding struct {
	Kind              string `json:"kind"`
	ID                string `json:"id"`
	Verdict           string `json:"verdict"`
	Evidence          string `json:"evidence"`
	SourceDescription string `json:"source_description,omitempty"`
	StartRune         int    `json:"start_rune,omitempty"`
	EndRune           int    `json:"end_rune,omitempty"`
}

type ManuscriptAdaptationVerificationReceipt struct {
	Kind              string `json:"kind"`
	ID                string `json:"id"`
	SourceDescription string `json:"source_description,omitempty"`
	StartRune         int    `json:"start_rune"`
	EndRune           int    `json:"end_rune"`
	Quote             string `json:"quote"`
	Verdict           string `json:"verdict"`
	TaskSignature     string `json:"task_signature"`
	CandidateSHA256   string `json:"candidate_sha256"`
}

type ManuscriptAdaptationVerificationTask struct {
	CandidateSHA256       string `json:"candidate_sha256"`
	LocatorTaskSignature  string `json:"locator_task_signature"`
	LocatorDecisionSHA256 string `json:"locator_decision_sha256"`
	Role                  string `json:"role"`
	Signature             string `json:"signature"`
}

type ManuscriptAdaptationVerificationDecision struct {
	Role            string                                    `json:"role"`
	TaskSignature   string                                    `json:"task_signature"`
	CandidateSHA256 string                                    `json:"candidate_sha256"`
	Receipts        []ManuscriptAdaptationVerificationReceipt `json:"receipts"`
}

type ManuscriptWholeDocumentAbsenceTask struct {
	CandidateSHA256         string   `json:"candidate_sha256"`
	AdaptationTaskSignature string   `json:"adaptation_task_signature"`
	ForbiddenIDs            []string `json:"forbidden_ids"`
	ProseRunes              int      `json:"prose_runes"`
	Role                    string   `json:"role"`
	Signature               string   `json:"signature"`
}

type ManuscriptWholeDocumentAbsenceReceipt struct {
	TaskSignature   string   `json:"task_signature"`
	CandidateSHA256 string   `json:"candidate_sha256"`
	ProseRunes      int      `json:"prose_runes"`
	ForbiddenIDs    []string `json:"forbidden_ids"`
	Signature       string   `json:"signature"`
}

type ManuscriptAdaptationAuditTask struct {
	RevisionID           string            `json:"revision_id"`
	ChapterID            string            `json:"chapter_id"`
	CandidateSHA256      string            `json:"candidate_sha256"`
	SourceManifestSHA256 string            `json:"source_manifest_sha256"`
	AdaptationPlanSHA256 string            `json:"adaptation_plan_sha256"`
	Events               map[string]string `json:"events,omitempty"`
	RequiredChanges      []string          `json:"required_changes,omitempty"`
	ForbiddenMoves       []string          `json:"forbidden_moves,omitempty"`
	Role                 string            `json:"role"`
	Signature            string            `json:"signature"`
}

type ManuscriptAdaptationAuditDecision struct {
	Role                 string                                 `json:"role"`
	Passed               bool                                   `json:"passed"`
	Report               string                                 `json:"report"`
	TaskSignature        string                                 `json:"task_signature"`
	CandidateSHA256      string                                 `json:"candidate_sha256"`
	SourceManifestSHA256 string                                 `json:"source_manifest_sha256"`
	AdaptationPlanSHA256 string                                 `json:"adaptation_plan_sha256"`
	Findings             []ManuscriptAdaptationFinding          `json:"findings"`
	AbsenceReceipt       *ManuscriptWholeDocumentAbsenceReceipt `json:"absence_receipt,omitempty"`
}

type ManuscriptStructuredAdaptationAuditor interface {
	AuditAdaptationCandidate(context.Context, domain.ManuscriptRevisionRuntime, domain.ManuscriptCandidate, ManuscriptAdaptationAuditTask) (ManuscriptAdaptationAuditDecision, error)
	VerifyAdaptationCandidate(context.Context, ManuscriptAdaptationVerificationTask, ManuscriptAdaptationAuditDecision, string) (ManuscriptAdaptationVerificationDecision, error)
	VerifyWholeDocumentAbsence(context.Context, ManuscriptWholeDocumentAbsenceTask, string) (ManuscriptWholeDocumentAbsenceReceipt, error)
}

func NewManuscriptRevisionService(st *storepkg.Store) *ManuscriptRevisionService {
	return &ManuscriptRevisionService{store: st}
}

func NewManuscriptRevisionServiceWithAuditor(st *storepkg.Store, auditor ManuscriptAuditor) *ManuscriptRevisionService {
	return &ManuscriptRevisionService{store: st, auditor: auditor}
}

func NewManuscriptRevisionServiceWithRuntime(st *storepkg.Store, writer ManuscriptWriter, auditor ManuscriptAuditor) *ManuscriptRevisionService {
	return &ManuscriptRevisionService{store: st, writer: writer, auditor: auditor}
}

func (s *ManuscriptRevisionService) CurrentChapter(stableID string) (domain.ManuscriptBaseline, string, error) {
	progress, err := s.store.Progress.Load()
	if err != nil {
		return domain.ManuscriptBaseline{}, "", err
	}
	if progress == nil || (progress.Phase != domain.PhaseWriting && progress.Phase != domain.PhaseComplete) {
		return domain.ManuscriptBaseline{}, "", fmt.Errorf("current manuscript prose is only readable in writing or complete phase")
	}
	entry, chapter, structure, err := s.resolveChapter(stableID)
	if err != nil {
		return domain.ManuscriptBaseline{}, "", err
	}
	prose, err := s.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return domain.ManuscriptBaseline{}, "", err
	}
	if strings.TrimSpace(prose) == "" {
		return domain.ManuscriptBaseline{}, "", fmt.Errorf("chapter %q has no current formal prose", stableID)
	}
	outlinePayload, _ := json.Marshal(entry)
	contract := narrativeContractFromEntry(entry, structure)
	baseline := domain.ManuscriptBaseline{
		ChapterID: stableID, DisplayChapter: chapter, CurrentProseSHA256: domain.ContentSignature([]byte(prose)),
		ApprovedOutlineSHA256: domain.ContentSignature(outlinePayload), StructureSignature: domain.StructureSignature(structure),
		NarrativeContract: contract, Mode: domain.RevisionModeNormal,
	}
	if manifest, loadErr := s.store.Adaptation.LoadSourceManifest(); loadErr != nil {
		return domain.ManuscriptBaseline{}, "", loadErr
	} else if manifest != nil {
		plan, planErr := s.store.Adaptation.LoadPlan()
		if planErr != nil || plan == nil {
			return domain.ManuscriptBaseline{}, "", fmt.Errorf("adaptation manuscript requires a confirmed plan: %w", planErr)
		}
		manifestPayload, _ := json.Marshal(manifest)
		planPayload, _ := json.Marshal(plan)
		baseline.Mode = domain.RevisionModeAdaptation
		baseline.SourceManifestSHA256 = domain.ContentSignature(manifestPayload)
		baseline.AdaptationPlanSHA256 = domain.ContentSignature(planPayload)
	}
	protectedState, err := s.manuscriptProtectedState(entry)
	if err != nil {
		return domain.ManuscriptBaseline{}, "", err
	}
	contract.StateSHA256 = protectedState.aggregate()
	baseline.NarrativeContract = contract
	baseline.ContractArtifact = newNarrativeContractArtifactWithProtectedState(contract, baseline.CurrentProseSHA256, baseline.ApprovedOutlineSHA256, protectedState)
	return baseline, prose, baseline.Validate()
}

// RestoreVersion opens a new revision around an immutable historical
// candidate. It deliberately reuses the normal audit/approval/publication
// state machine: restoring never overwrites current prose directly.
func (s *ManuscriptRevisionService) RestoreVersion(sourceRevisionID, chapterID, expectedSignature, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	source, err := s.store.ManuscriptRevisions.Load(strings.TrimSpace(sourceRevisionID))
	if err != nil {
		return nil, err
	}
	var historical *domain.ManuscriptCandidate
	for i := range source.Candidates {
		if source.Candidates[i].ChapterID == strings.TrimSpace(chapterID) && source.Candidates[i].Prose.SHA256 == strings.TrimSpace(expectedSignature) {
			copy := source.Candidates[i]
			historical = &copy
			break
		}
	}
	if historical == nil {
		return nil, fmt.Errorf("historical manuscript version does not match chapter and signature")
	}
	if _, err := s.store.ManuscriptRevisions.Content().Read(historical.Prose); err != nil {
		return nil, fmt.Errorf("read historical manuscript version: %w", err)
	}
	baseline, _, err := s.CurrentChapter(chapterID)
	if err != nil {
		return nil, err
	}
	if historical.OutlineSignature != baseline.ApprovedOutlineSHA256 || historical.ModeSignature != manuscriptModeSignature(baseline) {
		return nil, fmt.Errorf("historical manuscript version is stale against the current outline or mode")
	}
	historical.AuditSignature = ""
	historical.AuditArtifact = nil
	baselinePayload, _ := json.Marshal(baseline)
	historical.BaselineSignature = domain.ContentSignature(baselinePayload)
	revisionID, err := newManuscriptRevisionID()
	if err != nil {
		return nil, err
	}
	rebound, evidenceEnvelope, err := s.prepareHistoricalCandidate(revisionID, baseline, *historical)
	if err != nil {
		return nil, fmt.Errorf("rebind historical manuscript candidate: %w", err)
	}
	historical = &rebound
	policyID := domain.NormalManuscriptRevisionPolicyID
	if baseline.Mode == domain.RevisionModeAdaptation {
		policyID = domain.AdaptationManuscriptRevisionPolicyID
	}
	runtime := domain.ManuscriptRevisionRuntime{
		Version: 1, RevisionID: revisionID, Revision: 1, Mode: baseline.Mode,
		PolicyID: policyID, PolicyVersion: domain.ManuscriptRevisionPolicyVersion,
		Instruction:     "restore signed historical version " + sourceRevisionID,
		InstructionKind: domain.ManuscriptInstructionRewrite, Stage: "audit_pending",
		Baseline: baseline, Queue: []domain.ManuscriptReworkItem{{
			ChapterID: baseline.ChapterID, DisplayChapter: baseline.DisplayChapter,
			Requirement:        domain.StructureImpactRequired,
			ExpectedSignatures: []string{baseline.CurrentProseSHA256, baseline.ApprovedOutlineSHA256, baseline.StructureSignature},
			Status:             "generated", Attempt: 1, IdempotencyKey: idempotencyKey + ":" + baseline.ChapterID,
		}}, Candidates: []domain.ManuscriptCandidate{*historical},
		PublicationStatus: domain.ManuscriptPublicationNone, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if progress, progressErr := s.store.Progress.Load(); progressErr != nil {
		return nil, progressErr
	} else if progress != nil && progress.Phase == domain.PhaseComplete {
		runtime.RequiresCompletionRevalidation = true
		runtime.CompletionRevalidationStatus = "pending"
	}
	if s.beforeRestoreOwnership != nil {
		s.beforeRestoreOwnership()
	}
	var createdEvidence domain.ManuscriptContentRef
	var evidenceWasCreated bool
	created, startErr := s.store.ManuscriptRevisions.StartAtomic(runtime, idempotencyKey, func(current *domain.ManuscriptRevisionRuntime) error {
		fresh, _, refreshErr := s.CurrentChapter(chapterID)
		if refreshErr != nil {
			return refreshErr
		}
		if manuscriptRestoreOwnershipDrift(baseline, fresh) {
			return &domain.ManuscriptRevisionError{Class: "preview_stale", Err: fmt.Errorf("manuscript mode or signed baseline changed before restore ownership")}
		}
		if s.beforeRestoreEvidence != nil {
			if evidenceErr := s.beforeRestoreEvidence(); evidenceErr != nil {
				return evidenceErr
			}
		}
		evidence, wasCreated, evidenceErr := s.store.ManuscriptRevisions.Content().PutJSONTracked(evidenceEnvelope)
		if evidenceErr != nil {
			return evidenceErr
		}
		createdEvidence, evidenceWasCreated = evidence, wasCreated
		rebound.ContractEvidence = evidence
		if validateErr := rebound.Validate(); validateErr != nil {
			return validateErr
		}
		if s.beforeRestoreCommit != nil {
			if commitErr := s.beforeRestoreCommit(); commitErr != nil {
				return commitErr
			}
		}
		current.Candidates = []domain.ManuscriptCandidate{rebound}
		return nil
	})
	if startErr != nil && evidenceWasCreated {
		if cleanupErr := s.store.ManuscriptRevisions.Content().RemoveCreated(createdEvidence); cleanupErr != nil {
			return nil, errors.Join(startErr, fmt.Errorf("rollback restore candidate evidence: %w", cleanupErr))
		}
	}
	return created, startErr
}

func manuscriptRestoreOwnershipDrift(expected, fresh domain.ManuscriptBaseline) bool {
	return fresh.CurrentProseSHA256 != expected.CurrentProseSHA256 ||
		fresh.ApprovedOutlineSHA256 != expected.ApprovedOutlineSHA256 ||
		fresh.StructureSignature != expected.StructureSignature ||
		fresh.Mode != expected.Mode ||
		fresh.AdaptationPlanSHA256 != expected.AdaptationPlanSHA256 ||
		fresh.SourceManifestSHA256 != expected.SourceManifestSHA256
}

func (s *ManuscriptRevisionService) prepareHistoricalCandidate(revisionID string, baseline domain.ManuscriptBaseline, historical domain.ManuscriptCandidate) (domain.ManuscriptCandidate, manuscriptContractEvidenceEnvelope, error) {
	prosePayload, err := s.store.ManuscriptRevisions.Content().Read(historical.Prose)
	if err != nil {
		return domain.ManuscriptCandidate{}, manuscriptContractEvidenceEnvelope{}, err
	}
	entry, _, structure, err := s.resolveChapter(historical.ChapterID)
	if err != nil {
		return domain.ManuscriptCandidate{}, manuscriptContractEvidenceEnvelope{}, err
	}
	protectedState, err := s.manuscriptProtectedState(entry)
	if err != nil {
		return domain.ManuscriptCandidate{}, manuscriptContractEvidenceEnvelope{}, err
	}
	contract := narrativeContractFromEntry(entry, structure)
	contract.ChapterID = historical.ChapterID
	contract.OutlineSHA256 = baseline.ApprovedOutlineSHA256
	contract.StateSHA256 = protectedState.aggregate()
	outlinePayload, _ := json.Marshal(entry)
	task := newManuscriptContractAuditTask(revisionID, historical.ChapterID, historical.Prose.SHA256, baseline.ApprovedOutlineSHA256, entry, protectedState)
	task.AuthoritativeOutlineSHA256 = domain.JSONContentSignature(outlinePayload)
	task.AuthoritativeStructureSHA256 = domain.StructureSignature(structure)
	task = signManuscriptContractAuditTask(task)
	auditor, ok := s.auditor.(ManuscriptStructuredContractAuditor)
	if !ok {
		return domain.ManuscriptCandidate{}, manuscriptContractEvidenceEnvelope{}, fmt.Errorf("independent structured candidate contract auditor is unavailable")
	}
	decision, err := auditor.AuditCandidateContract(context.Background(), task, string(prosePayload))
	if err != nil {
		return domain.ManuscriptCandidate{}, manuscriptContractEvidenceEnvelope{}, err
	}
	if err = validateManuscriptContractAuditDecision(task, decision, string(prosePayload), contract); err != nil {
		return domain.ManuscriptCandidate{}, manuscriptContractEvidenceEnvelope{}, err
	}
	verificationTask := newManuscriptContractVerificationTask(task, decision)
	verification, err := auditor.VerifyCandidateContract(context.Background(), verificationTask, decision, contract, string(prosePayload))
	if err != nil {
		return domain.ManuscriptCandidate{}, manuscriptContractEvidenceEnvelope{}, err
	}
	if err = validateManuscriptContractVerification(task, decision, verificationTask, verification, string(prosePayload), contract); err != nil {
		return domain.ManuscriptCandidate{}, manuscriptContractEvidenceEnvelope{}, err
	}
	contract = decision.Contract
	artifact := newNarrativeContractArtifactWithProtectedState(contract, historical.Prose.SHA256, baseline.ApprovedOutlineSHA256, protectedState)
	evidence := manuscriptContractEvidenceEnvelope{Version: 1, Contract: contract, Prose: historical.Prose, Sidecar: historical.Sidecar, ProtectedFields: artifact.ProtectedFields, ArtifactSignature: artifact.Signature, AuditTask: task, AuditDecision: decision, VerificationTask: verificationTask, VerificationDecision: verification}
	baselinePayload, _ := json.Marshal(baseline)
	contractPayload, _ := json.Marshal(contract)
	historical.BaselineSignature = domain.ContentSignature(baselinePayload)
	historical.ContractSignature = domain.ContentSignature(contractPayload)
	historical.ContractArtifact = artifact
	historical.ContractEvidence = domain.ManuscriptContentRef{}
	historical.OutlineSignature = baseline.ApprovedOutlineSHA256
	historical.ModeSignature = manuscriptModeSignature(baseline)
	historical.AuditSignature = ""
	historical.AuditArtifact = nil
	return historical, evidence, nil
}

type manuscriptProtectedState struct {
	Character    string `json:"character_state"`
	Relationship string `json:"relationship_state"`
	Timeline     string `json:"timeline_state"`
	Foreshadow   string `json:"foreshadow_state"`
}

func (s manuscriptProtectedState) fields() map[string]string {
	return map[string]string{
		"character_state": s.Character, "relationship_state": s.Relationship,
		"timeline_state": s.Timeline, "foreshadow_state": s.Foreshadow,
	}
}

func (s manuscriptProtectedState) aggregate() string {
	payload, _ := json.Marshal(s)
	return domain.ContentSignature(payload)
}

func (s *ManuscriptRevisionService) manuscriptProtectedState(entry domain.OutlineEntry) (manuscriptProtectedState, error) {
	characters, err := s.store.Characters.Load()
	if err != nil {
		return manuscriptProtectedState{}, err
	}
	snapshots, err := s.store.Characters.LoadLatestSnapshots()
	if err != nil {
		return manuscriptProtectedState{}, err
	}
	cast, err := s.store.Cast.Load()
	if err != nil {
		return manuscriptProtectedState{}, err
	}
	timeline, err := s.store.World.LoadTimeline()
	if err != nil {
		return manuscriptProtectedState{}, err
	}
	relationships, err := s.store.World.LoadRelationships()
	if err != nil {
		return manuscriptProtectedState{}, err
	}
	foreshadow, err := s.store.World.LoadForeshadowLedger()
	if err != nil {
		return manuscriptProtectedState{}, err
	}
	characterPayload, _ := json.Marshal(struct{ Characters, Snapshots, Cast any }{characters, snapshots, cast})
	relationshipPayload, _ := json.Marshal(relationships)
	timelinePayload, _ := json.Marshal(timeline)
	foreshadowPayload, _ := json.Marshal(struct {
		Ledger           any
		FutureCommitment []string
	}{foreshadow, append([]string(nil), entry.Scenes...)})
	return manuscriptProtectedState{
		Character: domain.ContentSignature(characterPayload), Relationship: domain.ContentSignature(relationshipPayload),
		Timeline: domain.ContentSignature(timelinePayload), Foreshadow: domain.ContentSignature(foreshadowPayload),
	}, nil
}

func (s *ManuscriptRevisionService) Preview(request ManuscriptPreviewRequest, idempotencyKey string) (*ManuscriptPreview, error) {
	return s.PreviewContext(context.Background(), request, idempotencyKey)
}

// SubmitManualCandidate saves an author's exact prose through the signed
// publication transaction without asking an AI to approve the author's own
// edit. The server still verifies the loaded formal signature, binds the
// candidate bytes and existing semantic sidecars, records an explicit
// author-save audit receipt, and keeps the normal rollback/history trail.
func (s *ManuscriptRevisionService) SubmitManualCandidate(ctx context.Context, request ManualManuscriptCandidateRequest, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	request.ChapterID = strings.TrimSpace(request.ChapterID)
	request.ExpectedProseSHA = strings.TrimSpace(request.ExpectedProseSHA)
	request.Prose = strings.TrimSpace(request.Prose)
	if request.ChapterID == "" || request.ExpectedProseSHA == "" || request.Prose == "" {
		return nil, fmt.Errorf("chapter_id, expected_prose_sha256 and prose are required")
	}
	baseline, _, err := s.CurrentChapter(request.ChapterID)
	if err != nil {
		return nil, err
	}
	if baseline.CurrentProseSHA256 != request.ExpectedProseSHA {
		return nil, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("formal prose changed after the manual editor loaded")}
	}
	manual := *s
	manual.writer = nil
	preview, err := manual.PreviewContext(ctx, ManuscriptPreviewRequest{
		ChapterID:   request.ChapterID,
		Instruction: "作者手动修改正文；保持已批准的章节契约和现有语义侧车，保存为候选稿等待独立审核与人工批准",
		Kind:        domain.ManuscriptInstructionPolish,
	}, idempotencyKey+":preview")
	if err != nil {
		return nil, err
	}
	sidecars, err := s.currentChapterSidecars(request.ChapterID)
	if err != nil {
		return nil, err
	}
	candidate, err := s.SubmitCandidate(preview.Runtime.RevisionID, preview.Runtime.Revision, idempotencyKey+":candidate", ManuscriptCandidateInput{
		ChapterID:                request.ChapterID,
		Prose:                    request.Prose,
		Sidecars:                 sidecars,
		DeferContractAudit:       true,
		PreserveSemanticSidecars: true,
	})
	if err != nil {
		return nil, err
	}
	return s.publishExactAuthorSave(candidate, idempotencyKey)
}

func (s *ManuscriptRevisionService) publishExactAuthorSave(runtime *domain.ManuscriptRevisionRuntime, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if runtime == nil {
		return nil, fmt.Errorf("manual author-save runtime is unavailable")
	}
	if runtime.Stage == "completed" {
		return runtime, nil
	}
	if runtime.Stage == "audit_pending" {
		if len(runtime.Candidates) != 1 {
			return nil, fmt.Errorf("manual author save requires exactly one candidate")
		}
		candidate := runtime.Candidates[0]
		candidateSignature := manuscriptCandidateSignature(candidate)
		artifact, err := s.persistAuditArtifact(
			*runtime,
			candidate,
			candidateSignature,
			true,
			"作者手动精确保存：服务端已核对正式稿基线签名、候选正文哈希和现有语义侧车；本次不使用 AI 决定作者文本能否发布。",
			candidate.AdaptationCheck,
		)
		if err != nil {
			return nil, err
		}
		candidate.AuditSignature = candidateSignature
		candidate.AuditArtifact = artifact
		runtime, err = s.store.ManuscriptRevisions.Mutate(
			runtime.RevisionID,
			runtime.Revision,
			idempotencyKey+":author-approve",
			"author_exact_save",
			candidateSignature,
			func(current *domain.ManuscriptRevisionRuntime) error {
				if current.Stage != "audit_pending" || len(current.Candidates) != 1 {
					return fmt.Errorf("manual author save is not expected at stage %q", current.Stage)
				}
				current.Candidates[0] = candidate
				current.Stage = "ready_to_publish"
				return nil
			},
		)
		if err != nil {
			return nil, err
		}
	}
	if runtime.Stage != "ready_to_publish" {
		return nil, fmt.Errorf("manual author save cannot publish from stage %q", runtime.Stage)
	}
	return s.Publish(runtime.RevisionID, runtime.Revision, idempotencyKey+":publish")
}

func (s *ManuscriptRevisionService) currentChapterSidecars(chapterID string) (map[string]json.RawMessage, error) {
	entry, chapter, _, err := s.resolveChapter(chapterID)
	if err != nil {
		return nil, err
	}
	summary, err := s.store.Summaries.LoadSummary(chapter)
	if err != nil {
		return nil, err
	}
	if summary == nil || strings.TrimSpace(summary.Summary) == "" {
		summary = &domain.ChapterSummary{Chapter: chapter, Summary: entry.CoreEvent, KeyEvents: []string{entry.CoreEvent}}
	}
	events := append([]string(nil), summary.KeyEvents...)
	if len(events) == 0 {
		events = []string{entry.CoreEvent}
	}
	timeline, err := s.store.World.LoadTimeline()
	if err != nil {
		return nil, err
	}
	timeline = filterChapterValues(timeline, chapter, func(value domain.TimelineEvent) int { return value.Chapter })
	if len(timeline) == 0 {
		timeline = []domain.TimelineEvent{{Chapter: chapter, Event: entry.CoreEvent, Characters: append([]string(nil), summary.Characters...)}}
	}
	states, err := s.store.World.LoadStateChanges()
	if err != nil {
		return nil, err
	}
	states = filterChapterValues(states, chapter, func(value domain.StateChange) int { return value.Chapter })
	relationships, err := s.store.World.LoadRelationships()
	if err != nil {
		return nil, err
	}
	relationships = filterChapterValues(relationships, chapter, func(value domain.RelationshipEntry) int { return value.Chapter })
	foreshadow, err := s.store.World.LoadForeshadowLedger()
	if err != nil {
		return nil, err
	}
	foreshadow = filterValues(foreshadow, func(value domain.ForeshadowEntry) bool {
		return value.PlantedAt == chapter || value.ResolvedAt == chapter
	})
	worldFacts, err := s.store.World.LoadWorldRules()
	if err != nil {
		return nil, err
	}
	snapshots, err := s.store.Characters.LoadLatestSnapshots()
	if err != nil {
		return nil, err
	}
	carry := struct {
		CharacterSnapshots []domain.CharacterSnapshot `json:"character_snapshots"`
	}{CharacterSnapshots: snapshots}
	values := map[string]any{
		"summary": summary, "events": events, "timeline": timeline, "cast_state": states,
		"relationships": relationships, "foreshadow": foreshadow, "world_facts": worldFacts, "carry_forward": carry,
	}
	result := make(map[string]json.RawMessage, len(values))
	for name, value := range values {
		payload, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal current %s sidecar: %w", name, marshalErr)
		}
		result[name] = payload
	}
	return result, nil
}

func filterChapterValues[T any](values []T, chapter int, chapterOf func(T) int) []T {
	return filterValues(values, func(value T) bool { return chapterOf(value) == chapter })
}

func filterValues[T any](values []T, keep func(T) bool) []T {
	filtered := make([]T, 0, len(values))
	for _, value := range values {
		if keep(value) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func (s *ManuscriptRevisionService) PreviewContext(ctx context.Context, request ManuscriptPreviewRequest, idempotencyKey string) (*ManuscriptPreview, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	request.ChapterID = strings.TrimSpace(request.ChapterID)
	request.Instruction = strings.TrimSpace(request.Instruction)
	if request.ChapterID == "" || request.Instruction == "" {
		return nil, fmt.Errorf("chapter_id and instruction are required")
	}
	if request.Kind == "" {
		request.Kind = domain.ManuscriptInstructionRewrite
	}
	baseline, _, err := s.CurrentChapter(request.ChapterID)
	if err != nil {
		return nil, err
	}
	escalated, reason := false, ""
	var outlinePreview *domain.ManuscriptOutlinePreview
	var additionalImpacts []domain.ManuscriptReworkItem
	if s.writer != nil {
		plan, planErr := s.writer.PlanManuscriptRevision(ctx, baseline, request.Instruction, request.Kind)
		if planErr != nil {
			return nil, fmt.Errorf("plan manuscript revision: %w", planErr)
		}
		if plan.StoryChanged {
			if baseline.Mode == domain.RevisionModeAdaptation {
				return nil, fmt.Errorf("adaptation story changes require the signed adaptation structure-revision workflow")
			}
			plan.Contract = narrativeContractFromProposedEntry(plan.Outline, baseline.NarrativeContract)
			if err := plan.Contract.Validate(); err != nil {
				return nil, fmt.Errorf("planned narrative contract: %w", err)
			}
			outlinePayload, _ := json.Marshal(plan.Outline)
			contractPayload, _ := json.Marshal(plan.Contract)
			outlinePreview = &domain.ManuscriptOutlinePreview{ChapterID: baseline.ChapterID, Outline: plan.Outline, Contract: plan.Contract, OutlineSignature: domain.JSONContentSignature(outlinePayload), ContractSignature: domain.JSONContentSignature(contractPayload)}
			request.Kind = domain.ManuscriptInstructionOutlineRevision
			escalated, reason = true, "instruction changes the server-built signed narrative contract"
		}
		deltas := narrativeContractDeltas(baseline.NarrativeContract, plan.Contract)
		for _, impactedID := range plan.ImpactedChapterIDs {
			impactedID = strings.TrimSpace(impactedID)
			if impactedID == "" || impactedID == baseline.ChapterID {
				continue
			}
			impactedBaseline, _, loadErr := s.CurrentChapter(impactedID)
			if loadErr != nil {
				return nil, fmt.Errorf("resolve impacted stable chapter %q: %w", impactedID, loadErr)
			}
			if len(deltas) == 0 {
				return nil, fmt.Errorf("impacted chapter %q has no server-derived fact or contract delta", impactedID)
			}
			sourcePayload, _ := json.Marshal(baseline)
			targetPayload, _ := json.Marshal(impactedBaseline)
			evidence := append([]string(nil), deltas...)
			artifact := &domain.ManuscriptDependencyArtifact{SourceChapterID: baseline.ChapterID, TargetChapterID: impactedID, SourceBaselineSignature: domain.ContentSignature(sourcePayload), TargetBaselineSignature: domain.ContentSignature(targetPayload), ContractDeltas: append([]string(nil), deltas...), Evidence: evidence}
			unsignedArtifact := *artifact
			unsignedArtifact.Signature = ""
			artifactPayload, _ := json.Marshal(unsignedArtifact)
			artifact.Signature = domain.ContentSignature(artifactPayload)
			additionalImpacts = append(additionalImpacts, domain.ManuscriptReworkItem{
				ChapterID: impactedID, DisplayChapter: impactedBaseline.DisplayChapter, Requirement: domain.StructureImpactRequired,
				Evidence: evidence, DependencySourceIDs: []string{baseline.ChapterID}, DependencyArtifact: artifact,
				ExpectedSignatures: []string{impactedBaseline.CurrentProseSHA256, impactedBaseline.ApprovedOutlineSHA256, impactedBaseline.StructureSignature},
				Status:             "pending", IdempotencyKey: idempotencyKey + ":" + impactedID,
			})
		}
	}
	baselinePayload, _ := json.Marshal(baseline)
	queue := []domain.ManuscriptReworkItem{{
		ChapterID: baseline.ChapterID, DisplayChapter: baseline.DisplayChapter, Requirement: domain.StructureImpactRequired,
		ExpectedSignatures: []string{baseline.CurrentProseSHA256, baseline.ApprovedOutlineSHA256, baseline.StructureSignature}, Status: "pending", Attempt: 0,
		IdempotencyKey: idempotencyKey + ":" + baseline.ChapterID,
	}}
	for _, item := range additionalImpacts {
		if err := item.Validate(baseline.ChapterID); err != nil {
			return nil, err
		}
		queue = append(queue, item)
	}
	revisionID, err := newManuscriptRevisionID()
	if err != nil {
		return nil, err
	}
	policyID := domain.NormalManuscriptRevisionPolicyID
	if baseline.Mode == domain.RevisionModeAdaptation {
		policyID = domain.AdaptationManuscriptRevisionPolicyID
	}
	runtime := domain.ManuscriptRevisionRuntime{
		Version: 1, RevisionID: revisionID, Revision: 1, Mode: baseline.Mode, PolicyID: policyID,
		PolicyVersion: domain.ManuscriptRevisionPolicyVersion, Instruction: request.Instruction, InstructionKind: request.Kind,
		Stage: "approval_pending", Baseline: baseline, Queue: queue, OutlinePreview: outlinePreview, PublicationStatus: domain.ManuscriptPublicationNone,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if progress, progressErr := s.store.Progress.Load(); progressErr != nil {
		return nil, progressErr
	} else if progress != nil && progress.Phase == domain.PhaseComplete {
		runtime.RequiresCompletionRevalidation = true
		runtime.CompletionRevalidationStatus = "pending"
	}
	_ = baselinePayload
	created, err := s.store.ManuscriptRevisions.StartAtomic(runtime, idempotencyKey, func(owned *domain.ManuscriptRevisionRuntime) error {
		fresh, _, loadErr := s.CurrentChapter(owned.Baseline.ChapterID)
		if loadErr != nil {
			return loadErr
		}
		before, _ := json.Marshal(owned.Baseline)
		after, _ := json.Marshal(fresh)
		if domain.ContentSignature(before) != domain.ContentSignature(after) {
			return &domain.ManuscriptRevisionError{Class: "preview_stale", Err: fmt.Errorf("immutable manuscript evidence changed before ownership")}
		}
		owned.Baseline = fresh
		for index := range owned.Queue {
			item := &owned.Queue[index]
			itemBaseline, _, itemErr := s.CurrentChapter(item.ChapterID)
			if itemErr != nil {
				return itemErr
			}
			item.ExpectedSignatures = []string{itemBaseline.CurrentProseSHA256, itemBaseline.ApprovedOutlineSHA256, itemBaseline.StructureSignature}
			if item.ChapterID == owned.Baseline.ChapterID {
				continue
			}
			targetPayload, _ := json.Marshal(itemBaseline)
			if item.DependencyArtifact == nil || item.DependencyArtifact.TargetBaselineSignature != domain.ContentSignature(targetPayload) {
				return &domain.ManuscriptRevisionError{Class: "preview_stale", Err: fmt.Errorf("impacted chapter %q changed before ownership", item.ChapterID)}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ManuscriptPreview{Runtime: created, Escalated: escalated, Reason: reason}, nil
}

func (s *ManuscriptRevisionService) ConfirmAdditionalImpacts(revisionID string, expectedRevision int, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	runtime, err := s.store.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		return nil, err
	}
	sourcePayload, _ := json.Marshal(runtime.Baseline)
	for _, item := range runtime.Queue {
		if item.ChapterID == runtime.Baseline.ChapterID {
			continue
		}
		if item.DependencyArtifact == nil || item.DependencyArtifact.Validate() != nil || item.DependencyArtifact.SourceBaselineSignature != domain.ContentSignature(sourcePayload) {
			return nil, fmt.Errorf("additional impact %q has no current source dependency baseline", item.ChapterID)
		}
		target, _, loadErr := s.CurrentChapter(item.ChapterID)
		if loadErr != nil {
			return nil, loadErr
		}
		targetPayload, _ := json.Marshal(target)
		if item.DependencyArtifact.TargetBaselineSignature != domain.ContentSignature(targetPayload) {
			return nil, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("additional impact %q baseline drifted", item.ChapterID)}
		}
	}
	return s.store.ManuscriptRevisions.Mutate(revisionID, expectedRevision, idempotencyKey, "confirm_impacts", revisionID, func(current *domain.ManuscriptRevisionRuntime) error {
		if current.Stage != "approval_pending" {
			return fmt.Errorf("additional impacts can only be confirmed before generation")
		}
		for index := range current.Queue {
			if current.Queue[index].ChapterID == current.Baseline.ChapterID {
				continue
			}
			if current.Queue[index].DependencyArtifact == nil || current.Queue[index].DependencyArtifact.Validate() != nil {
				return fmt.Errorf("additional impact %q has no valid signed dependency artifact", current.Queue[index].ChapterID)
			}
			current.Queue[index].ImpactConfirmed = true
		}
		return nil
	})
}

func (s *ManuscriptRevisionService) SubmitCandidate(revisionID string, expectedRevision int, idempotencyKey string, input ManuscriptCandidateInput) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	runtime, err := s.store.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		return nil, err
	}
	if runtime.Stage != "approval_pending" && runtime.Stage != "candidate_generating" && runtime.Stage != "failed" {
		return nil, fmt.Errorf("candidate generation is not allowed at stage %q", runtime.Stage)
	}
	var queueItem *domain.ManuscriptReworkItem
	for i := range runtime.Queue {
		if runtime.Queue[i].ChapterID == input.ChapterID {
			queueItem = &runtime.Queue[i]
			break
		}
	}
	if queueItem == nil || strings.TrimSpace(input.Prose) == "" {
		return nil, fmt.Errorf("candidate must contain prose for a queued stable ID")
	}
	if raw, exists := input.Sidecars["narrative_contract"]; exists && len(bytes.TrimSpace(raw)) > 0 {
		return nil, fmt.Errorf("writer-supplied narrative_contract is forbidden; the server derives protected contract evidence")
	}
	if runtime.Mode != domain.RevisionModeAdaptation {
		if err := validateNormalManuscriptSidecars(input.Sidecars, true); err != nil {
			return nil, err
		}
	}
	candidateBaseline := runtime.Baseline
	if input.ChapterID != runtime.Baseline.ChapterID {
		candidateBaseline, _, err = s.CurrentChapter(input.ChapterID)
		if err != nil {
			return nil, err
		}
	}
	prose, err := s.store.ManuscriptRevisions.Content().PutMarkdown(input.Prose)
	if err != nil {
		return nil, err
	}
	sidecar, err := s.storeManuscriptSidecars(input.Sidecars)
	if err != nil {
		return nil, err
	}
	baselinePayload, _ := json.Marshal(candidateBaseline)
	outlineSignature := candidateBaseline.ApprovedOutlineSHA256
	entry, _, structure, err := s.resolveChapter(input.ChapterID)
	if err != nil {
		return nil, err
	}
	authoritativeOutlinePayload, _ := json.Marshal(entry)
	authoritativeOutlineSHA := domain.JSONContentSignature(authoritativeOutlinePayload)
	authoritativeStructureSHA := domain.StructureSignature(structure)
	contract := narrativeContractFromEntry(entry, structure)
	if input.ChapterID == runtime.Baseline.ChapterID && runtime.OutlinePreview != nil {
		outlineSignature = runtime.OutlinePreview.OutlineSignature
		entry = runtime.OutlinePreview.Outline
		contract = narrativeContractFromProposedEntry(entry, candidateBaseline.NarrativeContract)
	}
	contract.ChapterID = input.ChapterID
	contract.OutlineSHA256 = outlineSignature
	protectedState, err := s.manuscriptProtectedState(entry)
	if err != nil {
		return nil, err
	}
	contract.StateSHA256 = protectedState.aggregate()
	contractTask := newManuscriptContractAuditTask(runtime.RevisionID, input.ChapterID, prose.SHA256, outlineSignature, entry, protectedState)
	contractTask.AuthoritativeOutlineSHA256 = authoritativeOutlineSHA
	contractTask.AuthoritativeStructureSHA256 = authoritativeStructureSHA
	contractTask = signManuscriptContractAuditTask(contractTask)
	expectedArtifact := candidateBaseline.ContractArtifact
	if input.ChapterID == runtime.Baseline.ChapterID && runtime.OutlinePreview != nil {
		expectedArtifact = newNarrativeContractArtifactWithProtectedState(contract, candidateBaseline.CurrentProseSHA256, outlineSignature, protectedState)
	}
	var contractEvidence domain.ManuscriptContentRef
	var contractArtifact domain.NarrativeContractArtifact
	if input.DeferContractAudit {
		contractArtifact = newNarrativeContractArtifactWithProtectedState(contract, prose.SHA256, outlineSignature, protectedState)
		if err := compareNarrativeContractArtifacts(expectedArtifact, contractArtifact); err != nil {
			return nil, err
		}
		contractEvidence, err = s.store.ManuscriptRevisions.Content().PutJSON(pendingManuscriptContractEvidence{
			Version: 1, Status: "pending_independent_audit", Task: contractTask,
		})
		if err != nil {
			return nil, err
		}
	} else {
		structuredContractAuditor, ok := s.auditor.(ManuscriptStructuredContractAuditor)
		if !ok {
			return nil, fmt.Errorf("independent structured candidate contract auditor is unavailable")
		}
		contractDecision, auditErr := structuredContractAuditor.AuditCandidateContract(context.Background(), contractTask, input.Prose)
		if auditErr != nil {
			return nil, fmt.Errorf("independent candidate contract audit: %w", auditErr)
		}
		if err := validateManuscriptContractAuditDecision(contractTask, contractDecision, input.Prose, contract); err != nil {
			return nil, err
		}
		verificationTask := newManuscriptContractVerificationTask(contractTask, contractDecision)
		verification, verifyErr := structuredContractAuditor.VerifyCandidateContract(context.Background(), verificationTask, contractDecision, contract, input.Prose)
		if verifyErr != nil {
			return nil, fmt.Errorf("independent candidate contract verifier: %w", verifyErr)
		}
		if err := validateManuscriptContractVerification(contractTask, contractDecision, verificationTask, verification, input.Prose, contract); err != nil {
			return nil, err
		}
		contract = contractDecision.Contract
		contractArtifact = newNarrativeContractArtifactWithProtectedState(contract, prose.SHA256, outlineSignature, protectedState)
		if err := compareNarrativeContractArtifacts(expectedArtifact, contractArtifact); err != nil {
			return nil, err
		}
		contractEvidence, err = s.storeServerCandidateContractEvidence(contract, prose, sidecar, contractArtifact, contractTask, contractDecision, verificationTask, verification)
		if err != nil {
			return nil, err
		}
	}
	contractPayload, _ := json.Marshal(contract)
	candidate := domain.ManuscriptCandidate{
		ChapterID: input.ChapterID, DisplayChapter: candidateBaseline.DisplayChapter, Prose: prose, Sidecar: sidecar,
		BaselineSignature: domain.ContentSignature(baselinePayload), ContractSignature: domain.ContentSignature(contractPayload), ContractArtifact: contractArtifact,
		ContractEvidence: contractEvidence, OutlineSignature: outlineSignature, ModeSignature: manuscriptModeSignature(candidateBaseline),
		PreserveSemanticSidecars: input.PreserveSemanticSidecars,
	}
	if runtime.Mode == domain.RevisionModeAdaptation {
		err = (domain.AdaptationManuscriptPolicy{}).ValidateCandidate(candidate)
	} else {
		err = (domain.NormalManuscriptPolicy{}).ValidateCandidate(candidate)
	}
	if err != nil {
		return nil, err
	}
	return s.store.ManuscriptRevisions.Mutate(revisionID, expectedRevision, idempotencyKey, "submit_candidate", input, func(current *domain.ManuscriptRevisionRuntime) error {
		replaced := false
		for i := range current.Candidates {
			if current.Candidates[i].ChapterID == candidate.ChapterID {
				current.Candidates[i] = candidate
				replaced = true
			}
		}
		if !replaced {
			current.Candidates = append(current.Candidates, candidate)
		}
		current.Stage = "candidate_generating"
		for index := range current.Queue {
			if current.Queue[index].ChapterID == input.ChapterID {
				current.Queue[index].Status = "generated"
				current.Queue[index].Attempt++
				current.Queue[index].ErrorClass = ""
			}
		}
		allGenerated := true
		for _, queued := range current.Queue {
			if queued.Status != "generated" {
				allGenerated = false
				break
			}
		}
		if allGenerated {
			current.Stage = "audit_pending"
		}
		current.Batches = append(current.Batches, domain.ManuscriptBatch{
			ID: "batch_" + domain.ContentSignature([]byte(idempotencyKey))[:16], Revision: current.Revision,
			Attempt: 1, Status: "audit_pending", Items: append([]domain.ManuscriptReworkItem(nil), current.Queue...),
		})
		return nil
	})
}

func protectedStateFromArtifact(artifact domain.NarrativeContractArtifact) manuscriptProtectedState {
	return manuscriptProtectedState{
		Character: artifact.ProtectedFields["character_state"], Relationship: artifact.ProtectedFields["relationship_state"],
		Timeline: artifact.ProtectedFields["timeline_state"], Foreshadow: artifact.ProtectedFields["foreshadow_state"],
	}
}

type manuscriptContractEvidenceEnvelope struct {
	Version              int                                    `json:"version"`
	Contract             domain.NarrativeContract               `json:"contract"`
	Prose                domain.ManuscriptContentRef            `json:"prose"`
	Sidecar              domain.ManuscriptSidecar               `json:"sidecar"`
	ProtectedFields      map[string]string                      `json:"protected_fields"`
	ArtifactSignature    string                                 `json:"artifact_signature"`
	AuditTask            ManuscriptContractAuditTask            `json:"audit_task"`
	AuditDecision        ManuscriptContractAuditDecision        `json:"audit_decision"`
	VerificationTask     ManuscriptContractVerificationTask     `json:"verification_task"`
	VerificationDecision ManuscriptContractVerificationDecision `json:"verification_decision"`
}

type pendingManuscriptContractEvidence struct {
	Version int                         `json:"version"`
	Status  string                      `json:"status"`
	Task    ManuscriptContractAuditTask `json:"task"`
}

func (s *ManuscriptRevisionService) storeServerCandidateContractEvidence(contract domain.NarrativeContract, prose domain.ManuscriptContentRef, sidecar domain.ManuscriptSidecar, artifact domain.NarrativeContractArtifact, task ManuscriptContractAuditTask, decision ManuscriptContractAuditDecision, verificationTask ManuscriptContractVerificationTask, verification ManuscriptContractVerificationDecision) (domain.ManuscriptContentRef, error) {
	return s.store.ManuscriptRevisions.Content().PutJSON(manuscriptContractEvidenceEnvelope{Version: 1, Contract: contract, Prose: prose, Sidecar: sidecar, ProtectedFields: artifact.ProtectedFields, ArtifactSignature: artifact.Signature, AuditTask: task, AuditDecision: decision, VerificationTask: verificationTask, VerificationDecision: verification})
}

func newManuscriptContractAuditTask(revisionID, chapterID, proseSHA, outlineSHA string, outline domain.OutlineEntry, state manuscriptProtectedState) ManuscriptContractAuditTask {
	task := ManuscriptContractAuditTask{RevisionID: revisionID, ChapterID: chapterID, CandidateSHA256: proseSHA, OutlineSHA256: outlineSHA, Outline: outline, ProtectedState: state.fields(), ProtectedStateSHA256: state.aggregate(), Role: "contract_locator"}
	return signManuscriptContractAuditTask(task)
}

func signManuscriptContractAuditTask(task ManuscriptContractAuditTask) ManuscriptContractAuditTask {
	unsigned := task
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	task.Signature = domain.ContentSignature(payload)
	return task
}

func validateManuscriptContractAuditDecision(task ManuscriptContractAuditTask, decision ManuscriptContractAuditDecision, prose string, expected domain.NarrativeContract) error {
	unsignedTask := task
	unsignedTask.Signature = ""
	taskPayload, _ := json.Marshal(unsignedTask)
	if task.Role != "contract_locator" || decision.Role != "contract_locator" || task.Signature != domain.ContentSignature(taskPayload) || decision.TaskSignature != task.Signature || decision.CandidateSHA256 != task.CandidateSHA256 || domain.ContentSignature([]byte(prose)) != task.CandidateSHA256 {
		return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("candidate contract decision is not bound to the complete candidate")}
	}
	if err := decision.Contract.Validate(); err != nil {
		return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("derived candidate contract: %w", err)}
	}
	left, _ := json.Marshal(expected)
	right, _ := json.Marshal(decision.Contract)
	if domain.ContentSignature(left) != domain.ContentSignature(right) {
		return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("candidate prose contradicts the approved narrative contract")}
	}
	required := map[string]bool{"desire": false, "obstacle": false, "choice": false, "cost": false, "result": false, "exit_state": false, "future_commitments": false}
	if len(decision.Evidence) != len(required) {
		return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("contract locator must return exactly seven evidence ranges")}
	}
	ranges := make([]ManuscriptEvidenceLocation, 0, len(required))
	for _, evidence := range decision.Evidence {
		if present, ok := required[evidence.Field]; !ok || present {
			return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("unknown contract evidence field %q", evidence.Field)}
		}
		located, err := rereadCandidateEvidence(prose, evidence.StartRune, evidence.EndRune, evidence.Quote)
		if err != nil {
			return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("%s evidence: %w", evidence.Field, err)}
		}
		_ = located
		required[evidence.Field] = true
		ranges = append(ranges, evidence)
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].StartRune < ranges[j].StartRune })
	for index := 1; index < len(ranges); index++ {
		if ranges[index].StartRune < ranges[index-1].EndRune {
			return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("contract locator evidence ranges overlap")}
		}
	}
	for field, present := range required {
		if !present {
			return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("candidate prose lacks independently located %s evidence", field)}
		}
	}
	return nil
}

func newManuscriptContractVerificationTask(locatorTask ManuscriptContractAuditTask, decision ManuscriptContractAuditDecision) ManuscriptContractVerificationTask {
	payload, _ := json.Marshal(decision)
	task := ManuscriptContractVerificationTask{RevisionID: locatorTask.RevisionID, ChapterID: locatorTask.ChapterID, CandidateSHA256: locatorTask.CandidateSHA256, LocatorTaskSignature: locatorTask.Signature, LocatorDecisionSHA256: domain.ContentSignature(payload), Role: "contract_verifier"}
	unsigned := task
	unsigned.Signature = ""
	encoded, _ := json.Marshal(unsigned)
	task.Signature = domain.ContentSignature(encoded)
	return task
}

func narrativeContractReceiptValue(contract domain.NarrativeContract, field string) string {
	switch field {
	case "desire":
		return contract.Desire
	case "obstacle":
		return contract.Obstacle
	case "choice":
		return contract.Choice
	case "cost":
		return contract.Cost
	case "result":
		return contract.Result
	case "exit_state":
		return contract.ExitState
	case "future_commitments":
		return strings.Join(contract.FutureCommitments, "\n")
	default:
		return ""
	}
}

func validateManuscriptContractVerification(locatorTask ManuscriptContractAuditTask, locator ManuscriptContractAuditDecision, task ManuscriptContractVerificationTask, decision ManuscriptContractVerificationDecision, prose string, approved domain.NarrativeContract) error {
	expectedTask := newManuscriptContractVerificationTask(locatorTask, locator)
	if task != expectedTask || locator.Role == decision.Role || task.Role != "contract_verifier" || decision.Role != "contract_verifier" || decision.TaskSignature != task.Signature || decision.CandidateSHA256 != task.CandidateSHA256 || len(decision.Receipts) != 7 {
		return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("contract verifier task, role, or receipt count is invalid")}
	}
	locations := make(map[string]ManuscriptEvidenceLocation, 7)
	for _, location := range locator.Evidence {
		locations[location.Field] = location
	}
	seen := make(map[string]bool, 7)
	for _, receipt := range decision.Receipts {
		location, ok := locations[receipt.Field]
		if !ok || seen[receipt.Field] || receipt.TaskSignature != task.Signature || receipt.CandidateSHA256 != task.CandidateSHA256 || receipt.Verdict != "entailed" || receipt.StartRune != location.StartRune || receipt.EndRune != location.EndRune || receipt.Quote != location.Quote || receipt.Value != narrativeContractReceiptValue(locator.Contract, receipt.Field) || receipt.ApprovedValue != narrativeContractReceiptValue(approved, receipt.Field) {
			return &domain.ManuscriptRevisionError{Class: "contract_upgrade_required", Err: fmt.Errorf("contract verifier receipt %q is not meaning-bound", receipt.Field)}
		}
		if _, err := rereadCandidateEvidence(prose, receipt.StartRune, receipt.EndRune, receipt.Quote); err != nil {
			return err
		}
		seen[receipt.Field] = true
	}
	return nil
}

// GenerateCandidates is the only production candidate boundary. It executes
// one bounded model call per segment and persists receipts before allowing an
// independent audit. HTTP clients never provide prose, sidecars, dependency
// evidence, or audit truth.
func (s *ManuscriptRevisionService) GenerateCandidates(ctx context.Context, revisionID string, expectedRevision, expectedAttempt int, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	if s.writer == nil {
		return nil, fmt.Errorf("manuscript writer is unavailable")
	}
	runtime, err := s.store.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		return nil, err
	}
	if runtime.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: expected %d actual %d", storepkg.ErrManuscriptRevisionConflict, expectedRevision, runtime.Revision)
	}
	if err := s.validateModeEvidence(*runtime); err != nil {
		return nil, err
	}
	var item *domain.ManuscriptReworkItem
	for i := range runtime.Queue {
		if runtime.Queue[i].Status == "pending" || runtime.Queue[i].Status == "failed" {
			item = &runtime.Queue[i]
			break
		}
	}
	if item == nil {
		return nil, fmt.Errorf("no failed or pending manuscript segment is available")
	}
	if expectedAttempt != item.Attempt+1 {
		return nil, fmt.Errorf("generation attempt conflict: expected %d actual %d", expectedAttempt, item.Attempt+1)
	}
	if item.ChapterID != runtime.Baseline.ChapterID && !item.ImpactConfirmed {
		return nil, &domain.ManuscriptRevisionError{Class: "human_confirmation_required", Err: fmt.Errorf("additional stable ID %q requires confirmation", item.ChapterID)}
	}
	generationContext, err := s.buildGenerationContext(*runtime, *item)
	if err != nil {
		return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey+":context-failure", nil, "generation_context_invalid", err)
	}

	const maxSegments = 16
	segmentPlan, err := s.manuscriptSegmentPlanForAttempt(*runtime, item.ChapterID, expectedAttempt, generationContext.CurrentProse, maxSegments)
	if err != nil {
		return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey+":plan-failure", nil, classifyManuscriptGenerationError(err), err)
	}
	var prose strings.Builder
	var sidecars map[string]json.RawMessage
	receipts, resumeErr := s.resumableGenerationReceipts(*runtime, item.ChapterID, expectedAttempt, segmentPlan, &prose)
	if resumeErr != nil {
		return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey+":resume-failure", nil, classifyManuscriptGenerationError(resumeErr), resumeErr)
	}
	startSegment := len(receipts) + 1
	runtime, err = s.persistGenerationCheckpoint(runtime, *item, expectedAttempt, idempotencyKey, receipts, segmentPlan)
	if err != nil {
		return nil, err
	}
	for segment := startSegment; segment <= maxSegments; segment++ {
		promptPayload, _ := json.Marshal(struct {
			RevisionID string
			ChapterID  string
			Attempt    int
			Segment    int
			Prior      string
			Context    ManuscriptGenerationContext
		}{runtime.RevisionID, item.ChapterID, expectedAttempt, segment, prose.String(), generationContext})
		generated, generateErr := s.writer.GenerateManuscriptSegment(ctx, *runtime, *item, generationContext, expectedAttempt, segment, prose.String())
		promptSignature := domain.ContentSignature(promptPayload)
		if generateErr == nil {
			generateErr = validateGeneratedManuscriptSegment(runtime.Mode, generated)
		}
		if isNormalManuscriptSchemaError(generateErr) {
			return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey, receipts, "invalid_schema", generateErr)
		}
		receipt := domain.ManuscriptGenerationReceipt{RevisionID: runtime.RevisionID, ChapterID: item.ChapterID, Attempt: expectedAttempt, Segment: segment, SegmentPlanSignature: segmentPlan.Signature, PromptSignature: promptSignature, DependencyEvidence: promptSignature, Status: "completed"}
		if generateErr != nil {
			receipt.Status, receipt.ErrorClass = "failed", classifyManuscriptGenerationError(generateErr)
			receipts = append(receipts, receipt)
			return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey, receipts, receipt.ErrorClass, generateErr)
		}
		if (generated.ChapterID != "" && generated.ChapterID != item.ChapterID) || (generated.Attempt != 0 && generated.Attempt != expectedAttempt) || (generated.Segment != 0 && generated.Segment != segment) {
			receipt.Status, receipt.ErrorClass = "failed", "segment_identity_mismatch"
			receipts = append(receipts, receipt)
			return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey, receipts, receipt.ErrorClass, fmt.Errorf("model returned a segment identity mismatch"))
		}
		if generated.Truncated || strings.TrimSpace(generated.Prose) == "" {
			class := "empty_response"
			if generated.Truncated {
				class = "truncated_response"
			}
			receipt.Status, receipt.ErrorClass = "failed", class
			receipts = append(receipts, receipt)
			return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey, receipts, receipt.ErrorClass, fmt.Errorf("model returned truncated, empty, or unbound segment"))
		}
		prose.WriteString(generated.Prose)
		receipt.ResponseSignature = domain.ContentSignature([]byte(generated.Prose))
		receipt.Content, err = s.store.ManuscriptRevisions.Content().PutMarkdown(generated.Prose)
		if err != nil {
			receipt.Status, receipt.ErrorClass = "failed", "content_store_failure"
			receipts = append(receipts, receipt)
			return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey+":content-failure", receipts, receipt.ErrorClass, err)
		}
		receipts = append(receipts, receipt)
		runtime, err = s.persistGenerationCheckpoint(runtime, *item, expectedAttempt, idempotencyKey, receipts, segmentPlan)
		if err != nil {
			return nil, err
		}
		if generated.Complete {
			sidecars = generated.Sidecars
			for i := range receipts {
				receipts[i].SegmentCount = segment
			}
			break
		}
	}
	if sidecars == nil {
		return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey, receipts, "segment_limit", fmt.Errorf("manuscript generation exceeded %d bounded segments", maxSegments))
	}
	generated, err := s.SubmitCandidate(revisionID, runtime.Revision, idempotencyKey+":candidate", ManuscriptCandidateInput{ChapterID: item.ChapterID, Prose: prose.String(), Sidecars: sidecars})
	if err != nil {
		class := classifyManuscriptGenerationError(err)
		if class == "provider_error" {
			class = "invalid_schema"
		}
		return s.recordGenerationFailure(runtime, *item, expectedAttempt, idempotencyKey+":candidate-failure", receipts, class, err)
	}
	return s.store.ManuscriptRevisions.Mutate(revisionID, generated.Revision, idempotencyKey, "record_generation_receipts", receipts, func(current *domain.ManuscriptRevisionRuntime) error {
		if len(current.Batches) == 0 {
			return fmt.Errorf("generation batch was not persisted")
		}
		batch := &current.Batches[len(current.Batches)-1]
		batch.ExpectedAttempt = expectedAttempt
		batch.Attempt = expectedAttempt
		batch.Receipts = append([]domain.ManuscriptGenerationReceipt(nil), receipts...)
		batch.SegmentPlan = &segmentPlan
		return nil
	})
}

func (s *ManuscriptRevisionService) resumableGenerationReceipts(runtime domain.ManuscriptRevisionRuntime, chapterID string, attempt int, plan domain.ManuscriptSegmentPlan, prose *strings.Builder) ([]domain.ManuscriptGenerationReceipt, error) {
	for batchIndex := len(runtime.Batches) - 1; batchIndex >= 0; batchIndex-- {
		batch := runtime.Batches[batchIndex]
		if (batch.Status != "failed" && batch.Status != "generating") || len(batch.Items) == 0 || batch.Items[0].ChapterID != chapterID || batch.Attempt > attempt {
			continue
		}
		if batch.SegmentPlan == nil || batch.SegmentPlan.Signature != plan.Signature || batch.SegmentPlan.Validate() != nil {
			return nil, &domain.ManuscriptRevisionError{Class: "segment_plan_mismatch", Err: fmt.Errorf("restart cannot mix a different signed segment plan")}
		}
		completed := make([]domain.ManuscriptGenerationReceipt, 0, len(batch.Receipts))
		stopped := false
		for index, receipt := range batch.Receipts {
			if receipt.Status != "completed" {
				stopped = true
				break
			}
			if err := receipt.Validate(plan, index+1); err != nil || receipt.RevisionID != runtime.RevisionID {
				return nil, &domain.ManuscriptRevisionError{Class: "segment_identity_mismatch", Err: fmt.Errorf("persisted segment %d identity cannot be verified: %w", receipt.Segment, err)}
			}
			payload, err := s.store.ManuscriptRevisions.Content().Read(receipt.Content)
			if err != nil || domain.ContentSignature(payload) != receipt.ResponseSignature {
				return nil, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("persisted segment %d cannot be verified", receipt.Segment)}
			}
			prose.Write(payload)
			completed = append(completed, receipt)
		}
		if stopped {
			for _, receipt := range batch.Receipts[len(completed)+1:] {
				if receipt.Status == "completed" {
					return nil, &domain.ManuscriptRevisionError{Class: "segment_identity_mismatch", Err: fmt.Errorf("completed segment appears after a failed segment")}
				}
			}
		}
		return completed, nil
	}
	return nil, nil
}

func (s *ManuscriptRevisionService) manuscriptSegmentPlanForAttempt(runtime domain.ManuscriptRevisionRuntime, chapterID string, attempt int, currentProse string, maxSegments int) (domain.ManuscriptSegmentPlan, error) {
	fresh := newManuscriptSegmentPlan(chapterID, attempt, currentProse, maxSegments)
	for batchIndex := len(runtime.Batches) - 1; batchIndex >= 0; batchIndex-- {
		batch := runtime.Batches[batchIndex]
		if len(batch.Items) == 0 || batch.Items[0].ChapterID != chapterID || (batch.Status != "failed" && batch.Status != "generating") {
			continue
		}
		if batch.SegmentPlan == nil || batch.SegmentPlan.Validate() != nil {
			return domain.ManuscriptSegmentPlan{}, &domain.ManuscriptRevisionError{Class: "segment_plan_mismatch", Err: fmt.Errorf("prior attempt has no valid signed segment plan")}
		}
		prior := *batch.SegmentPlan
		if prior.ChapterID != fresh.ChapterID || prior.TargetRunes != fresh.TargetRunes || prior.MinRunes != fresh.MinRunes || prior.MaxRunes != fresh.MaxRunes || prior.MaxSegments != fresh.MaxSegments {
			return domain.ManuscriptSegmentPlan{}, &domain.ManuscriptRevisionError{Class: "segment_plan_mismatch", Err: fmt.Errorf("retry cannot change chapter, length bounds, or segment ordering")}
		}
		return prior, nil
	}
	return fresh, nil
}

func (s *ManuscriptRevisionService) persistGenerationCheckpoint(runtime *domain.ManuscriptRevisionRuntime, item domain.ManuscriptReworkItem, attempt int, idempotencyKey string, receipts []domain.ManuscriptGenerationReceipt, plan domain.ManuscriptSegmentPlan) (*domain.ManuscriptRevisionRuntime, error) {
	batchID := "batch_" + domain.ContentSignature([]byte(idempotencyKey))[:16]
	return s.store.ManuscriptRevisions.Mutate(runtime.RevisionID, runtime.Revision, fmt.Sprintf("%s:segment:%d", idempotencyKey, len(receipts)), "generation_checkpoint", receipts, func(current *domain.ManuscriptRevisionRuntime) error {
		batch := domain.ManuscriptBatch{ID: batchID, Revision: current.Revision, Attempt: attempt, ExpectedAttempt: attempt, Status: "generating", Items: []domain.ManuscriptReworkItem{item}, Receipts: append([]domain.ManuscriptGenerationReceipt(nil), receipts...), SegmentPlan: &plan}
		for index := range current.Batches {
			if current.Batches[index].ID == batchID {
				current.Batches[index] = batch
				current.Stage = "candidate_generating"
				return nil
			}
		}
		current.Batches = append(current.Batches, batch)
		current.Stage = "candidate_generating"
		return nil
	})
}

func (s *ManuscriptRevisionService) recordGenerationFailure(runtime *domain.ManuscriptRevisionRuntime, item domain.ManuscriptReworkItem, attempt int, idempotencyKey string, receipts []domain.ManuscriptGenerationReceipt, class string, cause error) (*domain.ManuscriptRevisionRuntime, error) {
	failed, err := s.store.ManuscriptRevisions.Mutate(runtime.RevisionID, runtime.Revision, idempotencyKey, "record_generation_failure", receipts, func(current *domain.ManuscriptRevisionRuntime) error {
		current.Stage = "failed"
		current.LastErrorClass = class
		for i := range current.Queue {
			if current.Queue[i].ChapterID == item.ChapterID {
				current.Queue[i].Attempt = attempt
				current.Queue[i].Status = "failed"
				current.Queue[i].ErrorClass = class
			}
		}
		batchID := "batch_" + domain.ContentSignature([]byte(idempotencyKey))[:16]
		batch := domain.ManuscriptBatch{ID: batchID, Revision: current.Revision, Attempt: attempt, ExpectedAttempt: attempt, Status: "failed", Items: []domain.ManuscriptReworkItem{item}, Receipts: append([]domain.ManuscriptGenerationReceipt(nil), receipts...)}
		replaced := false
		for index := range current.Batches {
			if current.Batches[index].ID == batchID {
				batch.SegmentPlan = current.Batches[index].SegmentPlan
				current.Batches[index], replaced = batch, true
			}
		}
		if !replaced {
			current.Batches = append(current.Batches, batch)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return failed, &domain.ManuscriptRevisionError{Class: class, Err: cause}
}

func newManuscriptSegmentPlan(chapterID string, attempt int, currentProse string, maxSegments int) domain.ManuscriptSegmentPlan {
	target := len([]rune(currentProse))
	plan := domain.ManuscriptSegmentPlan{ChapterID: chapterID, Attempt: attempt, TargetRunes: target, MinRunes: max(1, target*8/10), MaxRunes: max(target, target*12/10), MaxSegments: maxSegments}
	unsigned := plan
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	plan.Signature = domain.ContentSignature(payload)
	return plan
}

func classifyManuscriptGenerationError(err error) string {
	var classified *domain.ManuscriptRevisionError
	if errors.As(err, &classified) && strings.TrimSpace(classified.Class) != "" {
		return classified.Class
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "provider_timeout"
	}
	return "provider_error"
}

func (s *ManuscriptRevisionService) persistAuditArtifact(runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate, candidateSignature string, passed bool, report string, check *domain.AdaptationCheck) (*domain.ManuscriptAuditArtifact, error) {
	taskRef, err := s.store.ManuscriptRevisions.Content().PutJSON(map[string]any{
		"revision_id": runtime.RevisionID, "revision": runtime.Revision, "candidate_signature": candidateSignature,
		"contract_artifact_signature": candidate.ContractArtifact.Signature, "mode_signature": candidate.ModeSignature,
	})
	if err != nil {
		return nil, err
	}
	reportRef, err := s.store.ManuscriptRevisions.Content().PutMarkdown(report)
	if err != nil {
		return nil, err
	}
	findings := make([]string, 0)
	for _, line := range strings.Split(report, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			findings = append(findings, line)
		}
	}
	findingsRef, err := s.store.ManuscriptRevisions.Content().PutJSON(findings)
	if err != nil {
		return nil, err
	}
	receiptPayload := map[string]any{"passed": passed, "candidate_signature": candidateSignature, "checked_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if check != nil {
		receiptPayload["adaptation_check"] = check
	}
	receiptRef, err := s.store.ManuscriptRevisions.Content().PutJSON(receiptPayload)
	if err != nil {
		return nil, err
	}
	evidence := []string{candidate.Prose.SHA256, candidate.ContractArtifact.Signature, candidate.ContractEvidence.SHA256, candidate.BaselineSignature, candidate.OutlineSignature, candidate.ModeSignature}
	if check != nil {
		payload, _ := json.Marshal(check)
		evidence = append(evidence, domain.ContentSignature(payload))
	}
	created := domain.NewManuscriptAuditArtifact(candidateSignature, taskRef, reportRef, findingsRef, receiptRef, evidence)
	artifact := &created
	return artifact, artifact.Validate()
}

func (s *ManuscriptRevisionService) validateAuditArtifact(runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate) error {
	artifact := candidate.AuditArtifact
	if artifact == nil || artifact.Validate() != nil {
		return fmt.Errorf("candidate has no valid signed audit artifact")
	}
	candidateSignature := manuscriptCandidateSignature(candidate)
	if artifact.CandidateSignature != candidateSignature || candidate.AuditSignature != candidateSignature {
		return fmt.Errorf("audit candidate binding drift")
	}
	taskPayload, err := s.store.ManuscriptRevisions.Content().Read(artifact.Task)
	if err != nil {
		return fmt.Errorf("reread audit task: %w", err)
	}
	var task struct {
		RevisionID                string `json:"revision_id"`
		CandidateSignature        string `json:"candidate_signature"`
		ContractArtifactSignature string `json:"contract_artifact_signature"`
		ModeSignature             string `json:"mode_signature"`
	}
	if json.Unmarshal(taskPayload, &task) != nil || task.RevisionID != runtime.RevisionID || task.CandidateSignature != candidateSignature || task.ContractArtifactSignature != candidate.ContractArtifact.Signature || task.ModeSignature != candidate.ModeSignature {
		return fmt.Errorf("audit task schema or cross-binding is invalid")
	}
	reportPayload, err := s.store.ManuscriptRevisions.Content().Read(artifact.Report)
	if err != nil {
		return fmt.Errorf("reread audit report: %w", err)
	}
	findingsPayload, err := s.store.ManuscriptRevisions.Content().Read(artifact.Findings)
	if err != nil {
		return fmt.Errorf("reread audit findings: %w", err)
	}
	var findings []string
	if json.Unmarshal(findingsPayload, &findings) != nil {
		return fmt.Errorf("audit findings schema is invalid")
	}
	expectedFindings := make([]string, 0)
	for _, line := range strings.Split(string(reportPayload), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			expectedFindings = append(expectedFindings, line)
		}
	}
	expectedPayload, _ := json.Marshal(expectedFindings)
	actualPayload, _ := json.Marshal(findings)
	if domain.ContentSignature(expectedPayload) != domain.ContentSignature(actualPayload) {
		return fmt.Errorf("audit findings are not derived from the signed report")
	}
	receiptPayload, err := s.store.ManuscriptRevisions.Content().Read(artifact.Receipt)
	if err != nil {
		return fmt.Errorf("reread audit receipt: %w", err)
	}
	var receipt struct {
		Passed             bool                    `json:"passed"`
		CandidateSignature string                  `json:"candidate_signature"`
		CheckedAt          string                  `json:"checked_at"`
		AdaptationCheck    *domain.AdaptationCheck `json:"adaptation_check,omitempty"`
	}
	if json.Unmarshal(receiptPayload, &receipt) != nil || !receipt.Passed || receipt.CandidateSignature != candidateSignature || strings.TrimSpace(receipt.CheckedAt) == "" {
		return fmt.Errorf("audit receipt schema or candidate binding is invalid")
	}
	if runtime.Mode == domain.RevisionModeAdaptation {
		if receipt.AdaptationCheck == nil || candidate.AdaptationCheck == nil {
			return fmt.Errorf("adaptation audit receipt is missing its deterministic check")
		}
		left, _ := json.Marshal(receipt.AdaptationCheck)
		right, _ := json.Marshal(candidate.AdaptationCheck)
		if domain.ContentSignature(left) != domain.ContentSignature(right) {
			return fmt.Errorf("adaptation audit receipt binding drift")
		}
	}
	return nil
}

func (s *ManuscriptRevisionService) RunAudit(ctx context.Context, revisionID string, expectedRevision int, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	if s.auditor == nil {
		return nil, fmt.Errorf("independent manuscript auditor is unavailable")
	}
	runtime, err := s.store.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		return nil, err
	}
	if runtime.Revision != expectedRevision || runtime.Stage != "audit_pending" || len(runtime.Candidates) == 0 {
		return nil, fmt.Errorf("signed audit is not expected for revision %d at stage %q", expectedRevision, runtime.Stage)
	}
	runtime, err = s.resolvePendingManualContractEvidence(ctx, runtime, idempotencyKey+":contract-evidence")
	if err != nil {
		return nil, err
	}
	if err := s.validateModeEvidence(*runtime); err != nil {
		return nil, err
	}
	type auditResult struct {
		CandidateSignature string
		Passed             bool
		Report             string
		AdaptationCheck    *domain.AdaptationCheck
		Artifact           *domain.ManuscriptAuditArtifact
	}
	results := make([]auditResult, 0, len(runtime.Candidates))
	for _, candidate := range runtime.Candidates {
		candidateSignature := manuscriptCandidateSignature(candidate)
		candidateCheck, evidenceErr := s.validateCandidateBoundEvidence(*runtime, candidate)
		if evidenceErr != nil {
			return s.recordAuditFailure(*runtime, candidate, candidateSignature, idempotencyKey+":deterministic-failure", classifyManuscriptGenerationError(evidenceErr), evidenceErr)
		}
		candidate.AdaptationCheck = candidateCheck
		auditBoundSignature := manuscriptCandidateSignature(candidate)
		passed, report := false, ""
		var auditErr error
		if runtime.Mode == domain.RevisionModeAdaptation {
			structured, ok := s.auditor.(ManuscriptStructuredAdaptationAuditor)
			if !ok {
				auditErr = fmt.Errorf("adaptation audit requires server-bound structured semantic findings")
			} else {
				task, taskErr := s.buildAdaptationAuditTask(*runtime, candidate)
				if taskErr != nil {
					auditErr = taskErr
				} else {
					prosePayload, readErr := s.store.ManuscriptRevisions.Content().Read(candidate.Prose)
					if readErr != nil {
						auditErr = fmt.Errorf("reread complete adaptation candidate: %w", readErr)
					} else if domain.ContentSignature(prosePayload) != candidate.Prose.SHA256 {
						auditErr = fmt.Errorf("reread complete adaptation candidate: candidate SHA drift")
					} else {
						decision, decisionErr := structured.AuditAdaptationCandidate(ctx, *runtime, candidate, task)
						if decisionErr != nil {
							auditErr = decisionErr
						} else if decisionErr = validateAdaptationAuditDecision(task, decision, string(prosePayload)); decisionErr != nil {
							auditErr = decisionErr
						} else {
							verificationTask := newAdaptationVerificationTask(task, decision)
							verification, verifyErr := structured.VerifyAdaptationCandidate(ctx, verificationTask, decision, string(prosePayload))
							if verifyErr != nil {
								auditErr = verifyErr
							} else if verifyErr = validateAdaptationVerification(task, decision, verificationTask, verification, string(prosePayload)); verifyErr != nil {
								auditErr = verifyErr
							} else {
								verificationPayload, _ := json.Marshal(verification)
								candidateCheck.SemanticVerificationTaskSHA256 = verificationTask.Signature
								candidateCheck.SemanticVerificationSHA256 = domain.ContentSignature(verificationPayload)
								candidateCheck.SemanticVerificationReceipts = make([]domain.AdaptationSemanticVerificationReceipt, 0, len(verification.Receipts))
								for _, receipt := range verification.Receipts {
									candidateCheck.SemanticVerificationReceipts = append(candidateCheck.SemanticVerificationReceipts, domain.AdaptationSemanticVerificationReceipt{Kind: receipt.Kind, ID: receipt.ID, SourceDescription: receipt.SourceDescription, Quote: receipt.Quote, StartRune: receipt.StartRune, EndRune: receipt.EndRune, Verdict: receipt.Verdict, TaskSignature: receipt.TaskSignature, CandidateSHA256: receipt.CandidateSHA256})
								}
								if len(task.ForbiddenMoves) > 0 {
									absenceTask := newWholeDocumentAbsenceTask(task, string(prosePayload))
									receipt, absenceErr := structured.VerifyWholeDocumentAbsence(ctx, absenceTask, string(prosePayload))
									if absenceErr != nil {
										auditErr = absenceErr
									} else if absenceErr = validateSeparateAbsenceReceipt(absenceTask, receipt, string(prosePayload)); absenceErr != nil {
										auditErr = absenceErr
									} else {
										receipt.Signature = ""
										encoded, _ := json.Marshal(receipt)
										receipt.Signature = domain.ContentSignature(encoded)
										decision.AbsenceReceipt = &receipt
										candidateCheck.AbsenceAuditSHA256, candidateCheck.AbsenceAuditTaskSHA256 = receipt.Signature, receipt.TaskSignature
										candidateCheck.AbsenceAuditProseRunes, candidateCheck.AbsenceAuditForbiddenIDs = receipt.ProseRunes, append([]string(nil), receipt.ForbiddenIDs...)
									}
								}
								if auditErr == nil {
									passed, report = decision.Passed, strings.TrimSpace(decision.Report)
									candidateCheck.BodyEvidence = adaptationBodyEvidenceFromFindings(decision.Findings, candidate.Prose.SHA256)
									candidateCheck.Summary = "server-bound locator and independent semantic verifier receipts verified"
								}
							}
						}
					}
				}
			}
		} else {
			passed, report, auditErr = s.auditor.AuditManuscriptCandidate(ctx, *runtime, candidate)
		}
		if auditErr != nil {
			return s.recordAuditFailure(*runtime, candidate, auditBoundSignature, idempotencyKey+":auditor-failure", "auditor_failure", auditErr)
		}
		if passed && candidateCheck != nil {
			candidateCheck.Passed = true
			candidateCheck.Summary += "; independent candidate semantic audit passed"
		}
		artifact, artifactErr := s.persistAuditArtifact(*runtime, candidate, auditBoundSignature, passed, strings.TrimSpace(report), candidateCheck)
		if artifactErr != nil {
			return nil, artifactErr
		}
		results = append(results, auditResult{candidateSignature, passed, strings.TrimSpace(report), candidateCheck, artifact})
	}
	return s.store.ManuscriptRevisions.Mutate(revisionID, runtime.Revision, idempotencyKey, "record_audit", results, func(current *domain.ManuscriptRevisionRuntime) error {
		if current.Stage != "audit_pending" || len(current.Candidates) != len(results) {
			return fmt.Errorf("signed audit is not expected at stage %q", current.Stage)
		}
		for i := range current.Candidates {
			candidate := &current.Candidates[i]
			actual := manuscriptCandidateSignature(*candidate)
			if results[i].CandidateSignature != actual {
				return fmt.Errorf("candidate signature drift")
			}
			candidate.AdaptationCheck = results[i].AdaptationCheck
			candidate.AuditArtifact = results[i].Artifact
			if !results[i].Passed {
				current.Stage, current.LastErrorClass = "failed", "audit_failed"
				for queueIndex := range current.Queue {
					if current.Queue[queueIndex].ChapterID == candidate.ChapterID {
						current.Queue[queueIndex].Status = "failed"
						current.Queue[queueIndex].ErrorClass = "audit_failed"
					}
				}
				return nil
			}
			candidate.AuditSignature = manuscriptCandidateSignature(*candidate)
		}
		current.Stage = "final_approval_pending"
		return nil
	})
}

func (s *ManuscriptRevisionService) resolvePendingManualContractEvidence(
	ctx context.Context,
	runtime *domain.ManuscriptRevisionRuntime,
	idempotencyKey string,
) (*domain.ManuscriptRevisionRuntime, error) {
	structured, ok := s.auditor.(ManuscriptStructuredContractAuditor)
	if !ok {
		return nil, fmt.Errorf("independent structured candidate contract auditor is unavailable")
	}
	prepared := append([]domain.ManuscriptCandidate(nil), runtime.Candidates...)
	changed := false
	for index := range prepared {
		candidate := &prepared[index]
		payload, err := s.store.ManuscriptRevisions.Content().Read(candidate.ContractEvidence)
		if err != nil {
			return nil, err
		}
		var pending pendingManuscriptContractEvidence
		if json.Unmarshal(payload, &pending) != nil || pending.Status != "pending_independent_audit" {
			continue
		}
		if pending.Version != 1 || pending.Task.RevisionID != runtime.RevisionID ||
			pending.Task.ChapterID != candidate.ChapterID || pending.Task.CandidateSHA256 != candidate.Prose.SHA256 {
			return nil, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("pending manual contract evidence identity drift")}
		}
		prosePayload, err := s.store.ManuscriptRevisions.Content().Read(candidate.Prose)
		if err != nil || domain.ContentSignature(prosePayload) != candidate.Prose.SHA256 {
			return nil, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("pending manual candidate prose drift")}
		}
		prose := string(prosePayload)
		expected := narrativeContractFromEntry(pending.Task.Outline, nil)
		expected.ChapterID = candidate.ChapterID
		expected.OutlineSHA256 = candidate.OutlineSignature
		expected.StateSHA256 = pending.Task.ProtectedStateSHA256
		decision, err := structured.AuditCandidateContract(ctx, pending.Task, prose)
		if err != nil {
			return nil, fmt.Errorf("independent candidate contract audit: %w", err)
		}
		if err := validateManuscriptContractAuditDecision(pending.Task, decision, prose, expected); err != nil {
			return nil, err
		}
		verificationTask := newManuscriptContractVerificationTask(pending.Task, decision)
		verification, err := structured.VerifyCandidateContract(ctx, verificationTask, decision, expected, prose)
		if err != nil {
			return nil, fmt.Errorf("independent candidate contract verifier: %w", err)
		}
		if err := validateManuscriptContractVerification(pending.Task, decision, verificationTask, verification, prose, expected); err != nil {
			return nil, err
		}
		contract := decision.Contract
		state := manuscriptProtectedState{
			Character:    pending.Task.ProtectedState["character_state"],
			Relationship: pending.Task.ProtectedState["relationship_state"],
			Timeline:     pending.Task.ProtectedState["timeline_state"],
			Foreshadow:   pending.Task.ProtectedState["foreshadow_state"],
		}
		artifact := newNarrativeContractArtifactWithProtectedState(contract, candidate.Prose.SHA256, candidate.OutlineSignature, state)
		if err := compareNarrativeContractArtifacts(candidate.ContractArtifact, artifact); err != nil {
			return nil, err
		}
		evidence, err := s.storeServerCandidateContractEvidence(contract, candidate.Prose, candidate.Sidecar, artifact, pending.Task, decision, verificationTask, verification)
		if err != nil {
			return nil, err
		}
		contractPayload, _ := json.Marshal(contract)
		candidate.ContractSignature = domain.ContentSignature(contractPayload)
		candidate.ContractArtifact = artifact
		candidate.ContractEvidence = evidence
		changed = true
	}
	if !changed {
		return runtime, nil
	}
	return s.store.ManuscriptRevisions.Mutate(runtime.RevisionID, runtime.Revision, idempotencyKey, "bind_manual_contract_evidence", prepared, func(current *domain.ManuscriptRevisionRuntime) error {
		if current.Stage != "audit_pending" || len(current.Candidates) != len(prepared) {
			return fmt.Errorf("manual contract evidence is not expected at stage %q", current.Stage)
		}
		current.Candidates = append([]domain.ManuscriptCandidate(nil), prepared...)
		return nil
	})
}

func (s *ManuscriptRevisionService) recordAuditFailure(runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate, candidateSignature, idempotencyKey, class string, cause error) (*domain.ManuscriptRevisionRuntime, error) {
	report := class + ": " + strings.TrimSpace(cause.Error())
	artifact, err := s.persistAuditArtifact(runtime, candidate, candidateSignature, false, report, candidate.AdaptationCheck)
	if err != nil {
		return nil, errors.Join(cause, err)
	}
	failed, err := s.store.ManuscriptRevisions.Mutate(runtime.RevisionID, runtime.Revision, idempotencyKey, "record_audit_failure", artifact.Signature, func(current *domain.ManuscriptRevisionRuntime) error {
		current.Stage = "failed"
		current.LastErrorClass = class
		for index := range current.Candidates {
			if current.Candidates[index].ChapterID == candidate.ChapterID {
				current.Candidates[index].AuditArtifact = artifact
			}
		}
		for index := range current.Queue {
			if current.Queue[index].ChapterID == candidate.ChapterID {
				current.Queue[index].Status = "failed"
				current.Queue[index].ErrorClass = class
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.Join(cause, err)
	}
	return failed, &domain.ManuscriptRevisionError{Class: class, Err: cause}
}

func (s *ManuscriptRevisionService) buildAdaptationAuditTask(runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate) (ManuscriptAdaptationAuditTask, error) {
	plan, err := s.store.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return ManuscriptAdaptationAuditTask{}, fmt.Errorf("adaptation plan is unavailable: %w", err)
	}
	var planned *domain.AdaptationChapterPlan
	for index := range plan.Chapters {
		if plan.Chapters[index].ID == candidate.ChapterID && plan.Chapters[index].Chapter == candidate.DisplayChapter {
			planned = &plan.Chapters[index]
			break
		}
	}
	if planned == nil {
		return ManuscriptAdaptationAuditTask{}, fmt.Errorf("candidate stable ID and display number do not identify one adaptation plan chapter")
	}
	reports, err := s.store.Adaptation.LoadSourceReports()
	if err != nil {
		return ManuscriptAdaptationAuditTask{}, err
	}
	events := make(map[string]string)
	owned := append(append([]string(nil), planned.EventIDs...), planned.PreserveEvents...)
	for _, report := range reports {
		for _, event := range report.SourceEvents {
			if containsManuscriptString(owned, event.ID) {
				events[event.ID] = strings.TrimSpace(event.Description)
			}
		}
	}
	for _, eventID := range owned {
		if strings.TrimSpace(events[eventID]) == "" {
			return ManuscriptAdaptationAuditTask{}, fmt.Errorf("owned event %q has no manifest-bound source description", eventID)
		}
	}
	task := ManuscriptAdaptationAuditTask{
		RevisionID: runtime.RevisionID, ChapterID: candidate.ChapterID, CandidateSHA256: candidate.Prose.SHA256,
		SourceManifestSHA256: runtime.Baseline.SourceManifestSHA256, AdaptationPlanSHA256: runtime.Baseline.AdaptationPlanSHA256,
		Events: events, RequiredChanges: append([]string(nil), planned.RequiredChanges...), ForbiddenMoves: append([]string(nil), planned.ForbiddenMoves...),
		Role: "adaptation_locator",
	}
	unsigned := task
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	task.Signature = domain.ContentSignature(payload)
	return task, nil
}

func validateAdaptationAuditDecision(task ManuscriptAdaptationAuditTask, decision ManuscriptAdaptationAuditDecision, prose string) error {
	if task.Role != "adaptation_locator" || decision.Role != "adaptation_locator" || domain.ContentSignature([]byte(prose)) != task.CandidateSHA256 || decision.TaskSignature != task.Signature || decision.CandidateSHA256 != task.CandidateSHA256 || decision.SourceManifestSHA256 != task.SourceManifestSHA256 || decision.AdaptationPlanSHA256 != task.AdaptationPlanSHA256 || strings.TrimSpace(decision.Report) == "" {
		return fmt.Errorf("adaptation semantic findings are not cross-bound to task, candidate, source manifest, plan, and report")
	}
	if !decision.Passed {
		return nil
	}
	find := func(kind, id, verdict string) *ManuscriptAdaptationFinding {
		for index := range decision.Findings {
			finding := &decision.Findings[index]
			if finding.Kind == kind && strings.TrimSpace(finding.ID) == strings.TrimSpace(id) && finding.Verdict == verdict && strings.TrimSpace(finding.Evidence) != "" {
				return finding
			}
		}
		return nil
	}
	seenLocations := make(map[string]struct{})
	validateLocated := func(finding *ManuscriptAdaptationFinding) error {
		located, err := rereadCandidateEvidence(prose, finding.StartRune, finding.EndRune, finding.Evidence)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%d:%d:%s", finding.StartRune, finding.EndRune, normalizedEvidenceSignature(located))
		if _, duplicate := seenLocations[key]; duplicate {
			return fmt.Errorf("adaptation finding reuses a copied evidence location")
		}
		seenLocations[key] = struct{}{}
		return nil
	}
	for eventID, sourceDescription := range task.Events {
		finding := find("event", eventID, "affirmed")
		if finding == nil || strings.TrimSpace(finding.SourceDescription) != strings.TrimSpace(sourceDescription) {
			return fmt.Errorf("adaptation event %q lacks a meaning-bound source-description finding", eventID)
		}
		if err := validateLocated(finding); err != nil {
			return fmt.Errorf("adaptation event %q evidence: %w", eventID, err)
		}
	}
	for _, change := range task.RequiredChanges {
		finding := find("change", change, "affirmed")
		if finding == nil {
			return fmt.Errorf("required adaptation change %q lacks an affirmed semantic finding", change)
		}
		if err := validateLocated(finding); err != nil {
			return fmt.Errorf("required adaptation change %q evidence: %w", change, err)
		}
	}
	return nil
}

func newAdaptationVerificationTask(locatorTask ManuscriptAdaptationAuditTask, locator ManuscriptAdaptationAuditDecision) ManuscriptAdaptationVerificationTask {
	payload, _ := json.Marshal(locator)
	task := ManuscriptAdaptationVerificationTask{CandidateSHA256: locatorTask.CandidateSHA256, LocatorTaskSignature: locatorTask.Signature, LocatorDecisionSHA256: domain.ContentSignature(payload), Role: "adaptation_semantic_verifier"}
	unsigned := task
	unsigned.Signature = ""
	encoded, _ := json.Marshal(unsigned)
	task.Signature = domain.ContentSignature(encoded)
	return task
}

func validateAdaptationVerification(locatorTask ManuscriptAdaptationAuditTask, locator ManuscriptAdaptationAuditDecision, task ManuscriptAdaptationVerificationTask, decision ManuscriptAdaptationVerificationDecision, prose string) error {
	expected := newAdaptationVerificationTask(locatorTask, locator)
	if task != expected || locator.Role == decision.Role || decision.Role != "adaptation_semantic_verifier" || decision.TaskSignature != task.Signature || decision.CandidateSHA256 != task.CandidateSHA256 {
		return fmt.Errorf("adaptation semantic verifier task or role separation is invalid")
	}
	required := make(map[string]ManuscriptAdaptationFinding)
	for _, finding := range locator.Findings {
		if finding.Verdict == "affirmed" {
			required[finding.Kind+"\n"+finding.ID] = finding
		}
	}
	if len(decision.Receipts) != len(required) {
		return fmt.Errorf("adaptation verifier receipt count is invalid")
	}
	seen := make(map[string]bool)
	for _, receipt := range decision.Receipts {
		key := receipt.Kind + "\n" + receipt.ID
		finding, ok := required[key]
		if !ok || seen[key] || receipt.TaskSignature != task.Signature || receipt.CandidateSHA256 != task.CandidateSHA256 || receipt.Verdict != "entailed" || receipt.StartRune != finding.StartRune || receipt.EndRune != finding.EndRune || receipt.Quote != finding.Evidence || receipt.SourceDescription != finding.SourceDescription {
			return fmt.Errorf("adaptation verifier receipt %q is not meaning-bound", receipt.ID)
		}
		if _, err := rereadCandidateEvidence(prose, receipt.StartRune, receipt.EndRune, receipt.Quote); err != nil {
			return err
		}
		seen[key] = true
	}
	return nil
}

func newWholeDocumentAbsenceTask(task ManuscriptAdaptationAuditTask, prose string) ManuscriptWholeDocumentAbsenceTask {
	ids := append([]string(nil), task.ForbiddenMoves...)
	sort.Strings(ids)
	result := ManuscriptWholeDocumentAbsenceTask{CandidateSHA256: task.CandidateSHA256, AdaptationTaskSignature: task.Signature, ForbiddenIDs: ids, ProseRunes: len([]rune(prose)), Role: "whole_document_absence_verifier"}
	unsigned := result
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	result.Signature = domain.ContentSignature(payload)
	return result
}

func validateSeparateAbsenceReceipt(task ManuscriptWholeDocumentAbsenceTask, receipt ManuscriptWholeDocumentAbsenceReceipt, prose string) error {
	ids := append([]string(nil), receipt.ForbiddenIDs...)
	sort.Strings(ids)
	if receipt.TaskSignature != task.Signature || receipt.CandidateSHA256 != task.CandidateSHA256 || receipt.ProseRunes != task.ProseRunes || strings.Join(ids, "\n") != strings.Join(task.ForbiddenIDs, "\n") {
		return fmt.Errorf("whole-document verifier receipt is not bound to its separate task")
	}
	for _, forbidden := range task.ForbiddenIDs {
		if normalized := strings.TrimSpace(forbidden); normalized != "" && strings.Contains(prose, normalized) {
			return fmt.Errorf("whole-document absence receipt contradicts candidate prose for forbidden move %q", forbidden)
		}
	}
	return nil
}

func adaptationBodyEvidenceFromFindings(findings []ManuscriptAdaptationFinding, candidateSHA string) []domain.AdaptationBodyEvidence {
	result := make([]domain.AdaptationBodyEvidence, 0)
	for _, finding := range findings {
		if finding.Kind == "event" && finding.Verdict == "affirmed" {
			result = append(result, domain.AdaptationBodyEvidence{EventID: finding.ID, Quote: finding.Evidence, StartRune: finding.StartRune, EndRune: finding.EndRune, EvidenceSHA256: normalizedEvidenceSignature(finding.Evidence), CandidateSHA256: candidateSHA})
		}
	}
	return result
}

func (s *ManuscriptRevisionService) validateCandidateBoundEvidence(runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate) (*domain.AdaptationCheck, error) {
	prose, err := s.store.ManuscriptRevisions.Content().Read(candidate.Prose)
	if err != nil || domain.ContentSignature(prose) != candidate.Prose.SHA256 {
		return nil, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("candidate prose cannot be verified")}
	}
	if err := s.validateStoredCandidateContractEvidence(candidate, string(prose)); err != nil {
		return nil, err
	}
	if runtime.Mode != domain.RevisionModeAdaptation {
		return nil, nil
	}
	manifest, err := s.store.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return nil, fmt.Errorf("adaptation source manifest is unavailable: %w", err)
	}
	plan, err := s.store.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return nil, fmt.Errorf("adaptation plan is unavailable: %w", err)
	}
	var planned *domain.AdaptationChapterPlan
	for index := range plan.Chapters {
		if plan.Chapters[index].ID == candidate.ChapterID || plan.Chapters[index].Chapter == candidate.DisplayChapter {
			planned = &plan.Chapters[index]
			break
		}
	}
	if planned == nil {
		return nil, fmt.Errorf("candidate stable ID is outside the adaptation plan")
	}
	if err := hostadapt.ValidateAdaptationChapterOutlineQuality(plan, candidate.DisplayChapter); err != nil {
		return nil, fmt.Errorf("candidate adaptation coverage/ownership/granularity contract: %w", err)
	}
	proseText := string(prose)
	runeCount := len([]rune(proseText))
	if planned.TargetMinRunes > 0 && runeCount < planned.TargetMinRunes {
		return nil, &domain.ManuscriptRevisionError{Class: "adaptation_contract_violation", Err: fmt.Errorf("candidate has %d runes, below required %d", runeCount, planned.TargetMinRunes)}
	}
	if planned.TargetMaxRunes > 0 && runeCount > planned.TargetMaxRunes {
		return nil, &domain.ManuscriptRevisionError{Class: "adaptation_contract_violation", Err: fmt.Errorf("candidate has %d runes, above allowed %d", runeCount, planned.TargetMaxRunes)}
	}
	for _, eventID := range append(append([]string(nil), planned.EventIDs...), planned.PreserveEvents...) {
		for _, other := range plan.Chapters {
			if other.Chapter != planned.Chapter && (containsManuscriptString(other.EventIDs, eventID) || containsManuscriptString(other.PreserveEvents, eventID)) {
				return nil, &domain.ManuscriptRevisionError{Class: "adaptation_ownership_violation", Err: fmt.Errorf("event %q is owned by multiple target chapters", eventID)}
			}
		}
	}
	reports, err := s.store.Adaptation.LoadSourceReports()
	if err != nil {
		return nil, err
	}
	eventDescriptions := make(map[string]string)
	manifestSources := make(map[int]string, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		manifestSources[source.Chapter] = source.SHA256
	}
	for _, report := range reports {
		if expected := manifestSources[report.Chapter]; expected == "" || report.SourceSHA256 != expected {
			return nil, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("source report %d is not bound to the signed source manifest", report.Chapter)}
		}
		for _, event := range report.SourceEvents {
			eventDescriptions[event.ID] = strings.TrimSpace(event.Description)
		}
	}
	for _, eventID := range append(append([]string(nil), planned.EventIDs...), planned.PreserveEvents...) {
		if expected := eventDescriptions[eventID]; expected == "" {
			return nil, &domain.ManuscriptRevisionError{Class: "adaptation_coverage_violation", Err: fmt.Errorf("event %q is absent from manifest-bound source reports", eventID)}
		}
	}
	for _, sourceChapter := range planned.SourceChapters {
		text, source, loadErr := s.store.Adaptation.LoadSourceChapter(sourceChapter)
		if loadErr != nil || source == nil || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("adaptation source chapter %d is unavailable", sourceChapter)
		}
		if domain.ContentSignature([]byte(text)) != source.SHA256 {
			return nil, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("adaptation source chapter %d drifted", sourceChapter)}
		}
	}
	return &domain.AdaptationCheck{
		Chapter: candidate.DisplayChapter, DraftSHA256: candidate.Prose.SHA256, Passed: false,
		Summary:   "server deterministic ownership, granularity, and source-signature checks passed; semantic findings pending",
		CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func (s *ManuscriptRevisionService) validateStoredCandidateContractEvidence(candidate domain.ManuscriptCandidate, prose string) error {
	payload, err := s.store.ManuscriptRevisions.Content().Read(candidate.ContractEvidence)
	if err != nil {
		return &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("reread candidate contract evidence: %w", err)}
	}
	var evidence manuscriptContractEvidenceEnvelope
	if json.Unmarshal(payload, &evidence) != nil || evidence.Version != 1 || evidence.Prose != candidate.Prose || evidence.ArtifactSignature != candidate.ContractArtifact.Signature {
		return &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("candidate contract evidence envelope is not bound to the candidate")}
	}
	contractPayload, _ := json.Marshal(evidence.Contract)
	if domain.ContentSignature(contractPayload) != candidate.ContractSignature || evidence.AuditTask.OutlineSHA256 != candidate.OutlineSignature {
		return &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("candidate contract signature or outline binding drift")}
	}
	currentOutline, _, currentStructure, err := s.resolveChapter(candidate.ChapterID)
	if err != nil {
		return &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("reread authoritative outline by stable ID: %w", err)}
	}
	currentPayload, _ := json.Marshal(currentOutline)
	if domain.JSONContentSignature(currentPayload) != evidence.AuditTask.AuthoritativeOutlineSHA256 || domain.StructureSignature(currentStructure) != evidence.AuditTask.AuthoritativeStructureSHA256 || currentOutline.ID != candidate.ChapterID {
		return &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("current authoritative outline identity or structure drift")}
	}
	state, err := s.manuscriptProtectedState(evidence.AuditTask.Outline)
	if err != nil {
		return err
	}
	if evidence.AuditTask.ProtectedStateSHA256 != state.aggregate() {
		return &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("candidate contract authoritative state drift")}
	}
	for name, value := range state.fields() {
		if evidence.AuditTask.ProtectedState[name] != value || candidate.ContractArtifact.ProtectedFields[name] != value {
			return &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("candidate contract %s state drift", name)}
		}
	}
	expected := narrativeContractFromEntry(evidence.AuditTask.Outline, nil)
	expected.ChapterID = candidate.ChapterID
	expected.OutlineSHA256 = candidate.OutlineSignature
	expected.StateSHA256 = state.aggregate()
	if err := validateManuscriptContractAuditDecision(evidence.AuditTask, evidence.AuditDecision, prose, expected); err != nil {
		return err
	}
	if err := validateManuscriptContractVerification(evidence.AuditTask, evidence.AuditDecision, evidence.VerificationTask, evidence.VerificationDecision, prose, expected); err != nil {
		return err
	}
	return nil
}

func containsManuscriptString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(target) {
			return true
		}
	}
	return false
}

func (s *ManuscriptRevisionService) validateModeEvidence(runtime domain.ManuscriptRevisionRuntime) error {
	if runtime.Mode == domain.RevisionModeNormal {
		if runtime.Baseline.AdaptationPlanSHA256 != "" || runtime.Baseline.SourceManifestSHA256 != "" {
			return fmt.Errorf("normal manuscript runtime contains adaptation evidence")
		}
		return nil
	}
	manifest, err := s.store.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return fmt.Errorf("adaptation source manifest is unavailable: %w", err)
	}
	plan, err := s.store.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return fmt.Errorf("adaptation plan is unavailable: %w", err)
	}
	manifestPayload, _ := json.Marshal(manifest)
	planPayload, _ := json.Marshal(plan)
	if domain.ContentSignature(manifestPayload) != runtime.Baseline.SourceManifestSHA256 || domain.ContentSignature(planPayload) != runtime.Baseline.AdaptationPlanSHA256 {
		return fmt.Errorf("adaptation source or plan signature drift")
	}
	check, err := s.store.Adaptation.LoadCheck(runtime.Baseline.DisplayChapter)
	if err != nil {
		return err
	}
	if check == nil || !check.Passed || check.DraftSHA256 != runtime.Baseline.CurrentProseSHA256 {
		return fmt.Errorf("adaptation manuscript requires a fresh server-read AdaptationCheck")
	}
	return nil
}

func (s *ManuscriptRevisionService) Approve(revisionID string, expectedRevision int, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	return s.store.ManuscriptRevisions.Mutate(revisionID, expectedRevision, idempotencyKey, "approve", revisionID, func(current *domain.ManuscriptRevisionRuntime) error {
		if current.Stage != "final_approval_pending" || len(current.Candidates) == 0 {
			return fmt.Errorf("final approval requires the current signed audit")
		}
		for _, candidate := range current.Candidates {
			if err := s.validateAuditArtifact(*current, candidate); err != nil {
				return fmt.Errorf("final approval requires every current signed audit")
			}
		}
		current.Stage = "ready_to_publish"
		return nil
	})
}

func (s *ManuscriptRevisionService) Publish(revisionID string, expectedRevision int, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	runtime, err := s.store.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		return nil, err
	}
	signatures := make([]string, 0, len(runtime.Candidates))
	for _, candidate := range runtime.Candidates {
		if err := s.validateAuditArtifact(*runtime, candidate); err != nil {
			return nil, fmt.Errorf("publication requires verified audit content: %w", err)
		}
		signatures = append(signatures, candidate.AuditSignature)
	}
	if replay, found, replayErr := s.store.ManuscriptRevisions.Replay(idempotencyKey, "publish", signatures); found || replayErr != nil {
		return replay, replayErr
	}
	if runtime.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: expected %d actual %d", storepkg.ErrManuscriptRevisionConflict, expectedRevision, runtime.Revision)
	}
	for _, candidate := range runtime.Candidates {
		if err := s.store.ManuscriptRevisions.BindContentProvenance(storepkg.ManuscriptContentProvenance{
			ChapterID: candidate.ChapterID, ContentSHA256: candidate.Prose.SHA256,
			ApprovedOutlineSHA256: candidate.OutlineSignature, Mode: runtime.Mode,
			AdaptationPlanSHA256: runtime.Baseline.AdaptationPlanSHA256,
			SourceManifestSHA256: runtime.Baseline.SourceManifestSHA256,
		}); err != nil {
			return nil, fmt.Errorf("freeze publication provenance: %w", err)
		}
	}
	completed, err := s.store.PublishManuscriptCandidate(runtime, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if completed.Stage == "completion_revalidation_pending" {
		return s.RevalidateCompletion(revisionID, completed.Revision, idempotencyKey+":complete-revalidation")
	}
	return completed, nil
}

func (s *ManuscriptRevisionService) RevalidateCompletion(revisionID string, expectedRevision int, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	runtime, err := s.store.ManuscriptRevisions.Load(revisionID)
	if err != nil {
		return nil, err
	}
	if runtime.Stage != "completion_revalidation_pending" {
		return nil, fmt.Errorf("completion revalidation is not pending")
	}
	progress, err := s.store.Progress.Load()
	if err != nil {
		return nil, err
	}
	if progress == nil || progress.Phase != domain.PhaseComplete {
		return nil, fmt.Errorf("complete manuscript phase changed during revalidation")
	}
	structure, err := s.store.Outline.LoadOutline()
	if err != nil {
		return nil, err
	}
	digests := make([]string, 0, len(structure))
	for _, entry := range structure {
		prose, loadErr := s.store.Drafts.LoadChapterText(entry.Chapter)
		if loadErr != nil || strings.TrimSpace(prose) == "" {
			return nil, fmt.Errorf("complete manuscript chapter %q is missing formal prose: %w", entry.ID, loadErr)
		}
		digests = append(digests, entry.ID+":"+domain.ContentSignature([]byte(prose)))
	}
	signature := domain.ContentSignature([]byte(strings.Join(digests, "\n")))
	return s.store.ManuscriptRevisions.Mutate(revisionID, expectedRevision, idempotencyKey, "complete_revalidation", signature, func(current *domain.ManuscriptRevisionRuntime) error {
		if current.Stage != "completion_revalidation_pending" {
			return fmt.Errorf("completion revalidation is no longer pending")
		}
		current.CompletionRevalidationStatus = "completed"
		current.CompletionRevalidationSignature = signature
		current.Stage = "completed"
		return nil
	})
}

func (s *ManuscriptRevisionService) Cancel(revisionID string, expectedRevision int, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	if err := s.requireWriteReady(); err != nil {
		return nil, err
	}
	return s.store.ManuscriptRevisions.Mutate(revisionID, expectedRevision, idempotencyKey, "cancel", revisionID, func(current *domain.ManuscriptRevisionRuntime) error {
		if current.PublicationStatus != domain.ManuscriptPublicationNone {
			return fmt.Errorf("publication recovery required")
		}
		current.Stage = "cancelled"
		return nil
	})
}

func (s *ManuscriptRevisionService) storeManuscriptSidecars(payloads map[string]json.RawMessage) (domain.ManuscriptSidecar, error) {
	required := normalManuscriptSidecarNames
	refs := make(map[string]domain.ManuscriptContentRef, len(required))
	for _, name := range required {
		payload := payloads[name]
		if len(payload) == 0 || !json.Valid(payload) {
			return domain.ManuscriptSidecar{}, fmt.Errorf("candidate sidecar %s is missing or invalid", name)
		}
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			return domain.ManuscriptSidecar{}, err
		}
		if manuscriptSemanticSidecarEmpty(name, value) {
			return domain.ManuscriptSidecar{}, fmt.Errorf("candidate semantic sidecar %s is empty", name)
		}
		ref, err := s.store.ManuscriptRevisions.Content().PutJSON(value)
		if err != nil {
			return domain.ManuscriptSidecar{}, err
		}
		refs[name] = ref
	}
	return domain.ManuscriptSidecar{Summary: refs["summary"], Events: refs["events"], Timeline: refs["timeline"], CastState: refs["cast_state"], Relationships: refs["relationships"], Foreshadow: refs["foreshadow"], WorldFacts: refs["world_facts"], CarryForward: refs["carry_forward"]}, nil
}

func manuscriptSemanticSidecarEmpty(name string, value any) bool {
	if name == "summary" {
		if object, ok := value.(map[string]any); ok {
			return strings.TrimSpace(fmt.Sprint(object["summary"])) == ""
		}
	}
	if name != "events" && name != "timeline" {
		return false
	}
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		if len(typed) == 0 {
			return true
		}
		for _, item := range typed {
			if !manuscriptSemanticSidecarEmpty("", item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (s *ManuscriptRevisionService) resolveChapter(stableID string) (domain.OutlineEntry, int, []domain.VolumeOutline, error) {
	structure, err := s.store.Outline.LoadLayeredOutline()
	if err != nil {
		return domain.OutlineEntry{}, 0, nil, err
	}
	if len(structure) == 0 {
		outline, loadErr := s.store.Outline.LoadOutline()
		if loadErr != nil {
			return domain.OutlineEntry{}, 0, nil, loadErr
		}
		structure = []domain.VolumeOutline{{ID: domain.LegacyStructureID(s.store.Dir(), domain.StructureKindVolume, "flat"), Index: 1, Arcs: []domain.ArcOutline{{ID: domain.LegacyStructureID(s.store.Dir(), domain.StructureKindArc, "flat"), Index: 1, Chapters: outline}}}}
	}
	for _, entry := range domain.FlattenOutline(structure) {
		if entry.ID == stableID {
			return entry, entry.Chapter, structure, nil
		}
	}
	return domain.OutlineEntry{}, 0, nil, fmt.Errorf("stable chapter ID %q not found", stableID)
}

func narrativeContractFromEntry(entry domain.OutlineEntry, structure []domain.VolumeOutline) domain.NarrativeContract {
	state := domain.StructureSignature(structure)
	title := nonEmptyContractField(entry.Title, "chapter "+fmt.Sprint(entry.Chapter))
	event := nonEmptyContractField(entry.CoreEvent, "resolve "+title)
	hook := nonEmptyContractField(entry.Hook, "carry forward "+event)
	scenes := append([]string(nil), entry.Scenes...)
	choice := nonEmptyContractField(contractScene(scenes, 0), "choose how to "+event)
	cost := nonEmptyContractField(contractScene(scenes, 1), "pay the consequence of "+choice)
	result := nonEmptyContractField(contractScene(scenes, len(scenes)-1), "complete "+event)
	payload, _ := json.Marshal(entry)
	return domain.NarrativeContract{
		ChapterID: entry.ID, OutlineSHA256: domain.JSONContentSignature(payload), Desire: title,
		Obstacle: event, Choice: choice, Cost: cost, Result: result, ExitState: hook,
		FutureCommitments: scenes, StateSHA256: state,
	}
}

func contractScene(scenes []string, index int) string {
	if index < 0 || index >= len(scenes) {
		return ""
	}
	return strings.TrimSpace(scenes[index])
}

func nonEmptyContractField(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func narrativeContractFromProposedEntry(entry domain.OutlineEntry, baseline domain.NarrativeContract) domain.NarrativeContract {
	contract := narrativeContractFromEntry(entry, nil)
	contract.StateSHA256 = baseline.StateSHA256
	payload, _ := json.Marshal(entry)
	contract.OutlineSHA256 = domain.JSONContentSignature(payload)
	return contract
}

func manuscriptContractSignature(contract domain.NarrativeContract) string {
	payload, _ := json.Marshal(contract)
	return domain.ContentSignature(payload)
}

func manuscriptModeSignature(baseline domain.ManuscriptBaseline) string {
	payload, _ := json.Marshal(struct {
		Mode   domain.RevisionMode
		Plan   string
		Source string
	}{baseline.Mode, baseline.AdaptationPlanSHA256, baseline.SourceManifestSHA256})
	return domain.ContentSignature(payload)
}

func manuscriptCandidateSignature(candidate domain.ManuscriptCandidate) string {
	candidate.AuditSignature = ""
	candidate.AuditArtifact = nil
	payload, _ := json.Marshal(candidate)
	return domain.ContentSignature(payload)
}

func newNarrativeContractArtifact(contract domain.NarrativeContract, proseSignature, outlineSignature string) domain.NarrativeContractArtifact {
	return domain.NewNarrativeContractArtifact(contract, proseSignature, outlineSignature)
}

func newNarrativeContractArtifactWithProtectedState(contract domain.NarrativeContract, proseSignature, outlineSignature string, state manuscriptProtectedState) domain.NarrativeContractArtifact {
	return domain.NewNarrativeContractArtifactWithProtectedState(contract, proseSignature, outlineSignature, state.fields())
}

func compareNarrativeContractArtifacts(expected, candidate domain.NarrativeContractArtifact) error {
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("expected narrative contract artifact: %w", err)
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("candidate narrative contract artifact: %w", err)
	}
	if expected.ChapterID != candidate.ChapterID || expected.OutlineSHA256 != candidate.OutlineSHA256 {
		return &domain.ManuscriptRevisionError{Class: "protected_contract_violation", Err: fmt.Errorf("candidate narrative contract identity drift")}
	}
	for _, name := range []string{"desire", "obstacle", "choice", "cost", "result", "exit_state", "future_commitments"} {
		value := expected.ProtectedFields[name]
		if candidate.ProtectedFields[name] != value {
			return &domain.ManuscriptRevisionError{Class: "protected_contract_violation", Err: fmt.Errorf("protected field %q drifted", name)}
		}
	}
	for _, name := range []string{"character_state", "relationship_state", "timeline_state", "foreshadow_state"} {
		if len(candidate.ProtectedFields[name]) != 64 {
			return &domain.ManuscriptRevisionError{Class: "protected_contract_violation", Err: fmt.Errorf("candidate independently derived field %q is invalid", name)}
		}
	}
	return nil
}

func rereadCandidateEvidence(prose string, start, end int, quote string) (string, error) {
	runes := []rune(prose)
	if start < 0 || end <= start || end > len(runes) || end-start > 240 {
		return "", fmt.Errorf("evidence range is invalid or too broad")
	}
	located := string(runes[start:end])
	if located != quote || strings.TrimSpace(located) == "" {
		return "", fmt.Errorf("evidence quote does not match its candidate rune range")
	}
	lineStart := start
	for lineStart > 0 && runes[lineStart-1] != '\n' {
		lineStart--
	}
	linePrefix := strings.TrimSpace(string(runes[lineStart:start]))
	metadataPrefix := strings.ToLower(strings.TrimSpace(linePrefix))
	metadata := false
	for _, key := range []string{"metadata:", "summary:", "event_id:", "source:", "contract:", "yaml:"} {
		metadata = metadata || strings.HasPrefix(metadataPrefix, key)
	}
	if strings.HasPrefix(linePrefix, ">") || strings.HasPrefix(linePrefix, "```") || strings.HasPrefix(linePrefix, "---") || metadata && lineStart < 512 {
		return "", fmt.Errorf("evidence is quoted, code, or metadata rather than narrative prose")
	}
	contextStart, contextEnd := start-16, end+16
	if contextStart < 0 {
		contextStart = 0
	}
	if contextEnd > len(runes) {
		contextEnd = len(runes)
	}
	context := strings.ToLower(string(runes[contextStart:contextEnd]))
	for _, marker := range []string{"并非", "不是", "没有", "从未", "未曾", "否认", "不代表", "did not", "never ", "not "} {
		if strings.Contains(context, marker) {
			return "", fmt.Errorf("evidence is inside a negated context")
		}
	}
	return located, nil
}

func normalizedEvidenceSignature(value string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(value), " "))
	return domain.ContentSignature([]byte(normalized))
}

func narrativeContractDeltas(before, after domain.NarrativeContract) []string {
	left := newNarrativeContractArtifact(before, strings.Repeat("0", 64), before.OutlineSHA256)
	right := newNarrativeContractArtifact(after, strings.Repeat("0", 64), after.OutlineSHA256)
	var deltas []string
	for name, oldValue := range left.ProtectedFields {
		if newValue := right.ProtectedFields[name]; newValue != oldValue {
			deltas = append(deltas, name+":"+domain.ContentSignature([]byte(oldValue))+"->"+domain.ContentSignature([]byte(newValue)))
		}
	}
	return deltas
}

func newManuscriptRevisionID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "msr_" + hex.EncodeToString(random), nil
}
