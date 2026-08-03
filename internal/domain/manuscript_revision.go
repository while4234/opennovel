package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	NormalManuscriptRevisionPolicyID     = "ainovel.normal-manuscript-revision"
	AdaptationManuscriptRevisionPolicyID = "ainovel.adaptation-manuscript-revision"
	ManuscriptRevisionPolicyVersion      = "v1"
)

type ManuscriptInstructionKind string

const (
	ManuscriptInstructionPolish            ManuscriptInstructionKind = "polish"
	ManuscriptInstructionRewrite           ManuscriptInstructionKind = "rewrite"
	ManuscriptInstructionOutlineRevision   ManuscriptInstructionKind = "outline_revision"
	ManuscriptInstructionStructureRevision ManuscriptInstructionKind = "structure_revision"
)

type ManuscriptPublicationStatus string

const (
	ManuscriptPublicationNone          ManuscriptPublicationStatus = "none"
	ManuscriptPublicationPrepared      ManuscriptPublicationStatus = "prepared"
	ManuscriptPublicationFormalApplied ManuscriptPublicationStatus = "formal_applied"
	ManuscriptPublicationCompleted     ManuscriptPublicationStatus = "completed"
)

type ManuscriptContentRef struct {
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

func (r ManuscriptContentRef) Validate() error {
	if len(r.SHA256) != 64 || r.Size < 0 {
		return fmt.Errorf("invalid manuscript content reference")
	}
	if r.MediaType != "text/markdown" && r.MediaType != "application/json" {
		return fmt.Errorf("unsupported manuscript content media type %q", r.MediaType)
	}
	return nil
}

type NarrativeContract struct {
	ChapterID         string   `json:"chapter_id"`
	OutlineSHA256     string   `json:"outline_sha256"`
	Desire            string   `json:"desire"`
	Obstacle          string   `json:"obstacle"`
	Choice            string   `json:"choice"`
	Cost              string   `json:"cost"`
	Result            string   `json:"result"`
	ExitState         string   `json:"exit_state"`
	FutureCommitments []string `json:"future_commitments,omitempty"`
	StateSHA256       string   `json:"state_sha256"`
}

// NarrativeContractArtifact is server-derived proof binding the approved
// outline contract and protected manuscript state to one complete prose body.
// ProtectedFields is deliberately explicit so every protected field is
// compared by name instead of relying on a single opaque aggregate hash.
type NarrativeContractArtifact struct {
	ChapterID            string            `json:"chapter_id"`
	ProseSHA256          string            `json:"prose_sha256"`
	OutlineSHA256        string            `json:"outline_sha256"`
	ProtectedFields      map[string]string `json:"protected_fields"`
	ProtectedStateSHA256 string            `json:"protected_state_sha256"`
	Signature            string            `json:"signature"`
}

func NewNarrativeContractArtifact(contract NarrativeContract, proseSignature, outlineSignature string) NarrativeContractArtifact {
	fields := map[string]string{
		"desire": contract.Desire, "obstacle": contract.Obstacle, "choice": contract.Choice,
		"cost": contract.Cost, "result": contract.Result, "exit_state": contract.ExitState,
		"character_state": contract.StateSHA256, "relationship_state": contract.StateSHA256,
		"timeline_state": contract.StateSHA256, "foreshadow_state": contract.StateSHA256,
		"future_commitments": strings.Join(contract.FutureCommitments, "\n"),
	}
	artifact := NarrativeContractArtifact{ChapterID: contract.ChapterID, ProseSHA256: proseSignature, OutlineSHA256: outlineSignature, ProtectedFields: fields, ProtectedStateSHA256: contract.StateSHA256}
	unsigned := artifact
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	artifact.Signature = ContentSignature(payload)
	return artifact
}

// NewNarrativeContractArtifactWithProtectedState builds an artifact from
// state signatures computed by the server from separate authoritative stores.
// Callers cannot collapse these fields into one opaque/copyable state hash.
func NewNarrativeContractArtifactWithProtectedState(contract NarrativeContract, proseSignature, outlineSignature string, protectedState map[string]string) NarrativeContractArtifact {
	artifact := NewNarrativeContractArtifact(contract, proseSignature, outlineSignature)
	for _, name := range []string{"character_state", "relationship_state", "timeline_state", "foreshadow_state"} {
		if value := strings.TrimSpace(protectedState[name]); len(value) == 64 {
			artifact.ProtectedFields[name] = value
		} else {
			artifact.ProtectedFields[name] = ""
		}
	}
	unsigned := artifact
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	artifact.Signature = ContentSignature(payload)
	return artifact
}

func (a NarrativeContractArtifact) Validate() error {
	if strings.TrimSpace(a.ChapterID) == "" || len(a.ProseSHA256) != 64 || len(a.OutlineSHA256) != 64 || len(a.ProtectedStateSHA256) != 64 || len(a.ProtectedFields) == 0 {
		return fmt.Errorf("narrative contract artifact is incomplete")
	}
	unsigned := a
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	if ContentSignature(payload) != a.Signature {
		return fmt.Errorf("narrative contract artifact signature is invalid")
	}
	for _, name := range []string{"character_state", "relationship_state", "timeline_state", "foreshadow_state"} {
		if len(a.ProtectedFields[name]) != 64 {
			return fmt.Errorf("narrative contract artifact %s is invalid", name)
		}
	}
	return nil
}

func (c NarrativeContract) Validate() error {
	if strings.TrimSpace(c.ChapterID) == "" || strings.TrimSpace(c.OutlineSHA256) == "" || strings.TrimSpace(c.StateSHA256) == "" {
		return fmt.Errorf("narrative contract identity and signatures are required")
	}
	for name, value := range map[string]string{"desire": c.Desire, "obstacle": c.Obstacle, "choice": c.Choice, "cost": c.Cost, "result": c.Result, "exit_state": c.ExitState} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("narrative contract %s is required", name)
		}
	}
	return nil
}

type ManuscriptBaseline struct {
	ChapterID             string                    `json:"chapter_id"`
	DisplayChapter        int                       `json:"display_chapter"`
	CurrentProseSHA256    string                    `json:"current_prose_sha256"`
	ApprovedOutlineSHA256 string                    `json:"approved_outline_sha256"`
	StructureSignature    string                    `json:"structure_signature"`
	NarrativeContract     NarrativeContract         `json:"narrative_contract"`
	ContractArtifact      NarrativeContractArtifact `json:"contract_artifact"`
	Mode                  RevisionMode              `json:"mode"`
	AdaptationPlanSHA256  string                    `json:"adaptation_plan_sha256,omitempty"`
	SourceManifestSHA256  string                    `json:"source_manifest_sha256,omitempty"`
}

func (b ManuscriptBaseline) Validate() error {
	if strings.TrimSpace(b.ChapterID) == "" || b.DisplayChapter <= 0 || strings.TrimSpace(b.CurrentProseSHA256) == "" || strings.TrimSpace(b.ApprovedOutlineSHA256) == "" || strings.TrimSpace(b.StructureSignature) == "" {
		return fmt.Errorf("manuscript baseline is incomplete")
	}
	if err := b.NarrativeContract.Validate(); err != nil {
		return err
	}
	if err := b.ContractArtifact.Validate(); err != nil {
		return err
	}
	if b.ContractArtifact.ChapterID != b.ChapterID || b.ContractArtifact.ProseSHA256 != b.CurrentProseSHA256 || b.ContractArtifact.OutlineSHA256 != b.ApprovedOutlineSHA256 {
		return fmt.Errorf("baseline narrative artifact is not bound to the current chapter")
	}
	if b.Mode == RevisionModeAdaptation {
		if strings.TrimSpace(b.AdaptationPlanSHA256) == "" || strings.TrimSpace(b.SourceManifestSHA256) == "" {
			return fmt.Errorf("adaptation manuscript baseline requires plan and source manifest signatures")
		}
	} else if b.AdaptationPlanSHA256 != "" || b.SourceManifestSHA256 != "" {
		return fmt.Errorf("normal manuscript baseline must not contain source or adaptation signatures")
	}
	return nil
}

type ManuscriptReworkItem struct {
	ChapterID           string                        `json:"chapter_id"`
	DisplayChapter      int                           `json:"display_chapter"`
	Requirement         StructureImpactLevel          `json:"requirement"`
	Evidence            []string                      `json:"evidence,omitempty"`
	DependencySourceIDs []string                      `json:"dependency_source_ids,omitempty"`
	ExpectedSignatures  []string                      `json:"expected_signatures"`
	Status              string                        `json:"status"`
	Attempt             int                           `json:"attempt"`
	ErrorClass          string                        `json:"error_class,omitempty"`
	IdempotencyKey      string                        `json:"idempotency_key"`
	DependencyArtifact  *ManuscriptDependencyArtifact `json:"dependency_artifact,omitempty"`
	ImpactConfirmed     bool                          `json:"impact_confirmed,omitempty"`
}

func (i ManuscriptReworkItem) Validate(targetChapterID string) error {
	if strings.TrimSpace(i.ChapterID) == "" || i.DisplayChapter <= 0 || strings.TrimSpace(i.IdempotencyKey) == "" || len(i.ExpectedSignatures) == 0 {
		return fmt.Errorf("manuscript rework item is incomplete")
	}
	if i.Requirement != StructureImpactRequired && i.Requirement != StructureImpactRecommended {
		return fmt.Errorf("invalid manuscript rework requirement %q", i.Requirement)
	}
	if i.ChapterID != targetChapterID && (len(i.Evidence) == 0 || len(i.DependencySourceIDs) == 0) {
		return fmt.Errorf("additional chapter %q requires dependency evidence tied to a changed stable ID", i.ChapterID)
	}
	if i.ChapterID != targetChapterID {
		if i.DependencyArtifact == nil || i.DependencyArtifact.Validate() != nil {
			return fmt.Errorf("additional chapter %q requires a signed dependency artifact", i.ChapterID)
		}
	}
	return nil
}

type ManuscriptDependencyArtifact struct {
	SourceChapterID         string   `json:"source_chapter_id"`
	TargetChapterID         string   `json:"target_chapter_id"`
	SourceBaselineSignature string   `json:"source_baseline_signature"`
	TargetBaselineSignature string   `json:"target_baseline_signature"`
	FactDeltas              []string `json:"fact_deltas"`
	ContractDeltas          []string `json:"contract_deltas"`
	Evidence                []string `json:"evidence"`
	Signature               string   `json:"signature"`
}

func (a ManuscriptDependencyArtifact) Validate() error {
	if strings.TrimSpace(a.SourceChapterID) == "" || strings.TrimSpace(a.TargetChapterID) == "" || len(a.SourceBaselineSignature) != 64 || len(a.TargetBaselineSignature) != 64 || (len(a.FactDeltas) == 0 && len(a.ContractDeltas) == 0) || len(a.Evidence) == 0 || strings.TrimSpace(a.Signature) == "" {
		return fmt.Errorf("dependency artifact is incomplete")
	}
	unsigned := a
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	if ContentSignature(payload) != a.Signature {
		return fmt.Errorf("dependency artifact signature is invalid")
	}
	return nil
}

type ManuscriptSidecar struct {
	Summary       ManuscriptContentRef `json:"summary"`
	Events        ManuscriptContentRef `json:"events"`
	Timeline      ManuscriptContentRef `json:"timeline"`
	CastState     ManuscriptContentRef `json:"cast_state"`
	Relationships ManuscriptContentRef `json:"relationships"`
	Foreshadow    ManuscriptContentRef `json:"foreshadow"`
	WorldFacts    ManuscriptContentRef `json:"world_facts"`
	CarryForward  ManuscriptContentRef `json:"carry_forward"`
}

func (s ManuscriptSidecar) Validate() error {
	for _, ref := range []ManuscriptContentRef{s.Summary, s.Events, s.Timeline, s.CastState, s.Relationships, s.Foreshadow, s.WorldFacts, s.CarryForward} {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("incomplete manuscript sidecar: %w", err)
		}
	}
	return nil
}

type ManuscriptCandidate struct {
	ChapterID         string                    `json:"chapter_id"`
	DisplayChapter    int                       `json:"display_chapter"`
	Prose             ManuscriptContentRef      `json:"prose"`
	Sidecar           ManuscriptSidecar         `json:"sidecar"`
	BaselineSignature string                    `json:"baseline_signature"`
	ContractSignature string                    `json:"contract_signature"`
	ContractArtifact  NarrativeContractArtifact `json:"contract_artifact"`
	ContractEvidence  ManuscriptContentRef      `json:"contract_evidence"`
	OutlineSignature  string                    `json:"outline_signature"`
	ModeSignature     string                    `json:"mode_signature"`
	// PreserveSemanticSidecars marks an author-owned prose-only save. The
	// publication transaction updates formal prose while leaving summaries,
	// world facts, character state, and relationship ledgers untouched.
	PreserveSemanticSidecars bool                     `json:"preserve_semantic_sidecars,omitempty"`
	AuditSignature           string                   `json:"audit_signature,omitempty"`
	AuditArtifact            *ManuscriptAuditArtifact `json:"audit_artifact,omitempty"`
	AdaptationCheck          *AdaptationCheck         `json:"adaptation_check,omitempty"`
}

func (c ManuscriptCandidate) Validate() error {
	if strings.TrimSpace(c.ChapterID) == "" || c.DisplayChapter <= 0 || strings.TrimSpace(c.BaselineSignature) == "" || strings.TrimSpace(c.ContractSignature) == "" || strings.TrimSpace(c.OutlineSignature) == "" || strings.TrimSpace(c.ModeSignature) == "" {
		return fmt.Errorf("manuscript candidate signatures are incomplete")
	}
	if err := c.Prose.Validate(); err != nil {
		return err
	}
	if err := c.ContractArtifact.Validate(); err != nil {
		return err
	}
	if err := c.ContractEvidence.Validate(); err != nil || c.ContractEvidence.MediaType != "application/json" {
		return fmt.Errorf("candidate narrative contract evidence is invalid")
	}
	if c.ContractArtifact.ChapterID != c.ChapterID || c.ContractArtifact.ProseSHA256 != c.Prose.SHA256 || c.ContractArtifact.OutlineSHA256 != c.OutlineSignature {
		return fmt.Errorf("candidate narrative artifact is not bound to candidate prose")
	}
	if c.AdaptationCheck != nil && (c.AdaptationCheck.Chapter != c.DisplayChapter || c.AdaptationCheck.DraftSHA256 != c.Prose.SHA256) {
		return fmt.Errorf("candidate adaptation check is not bound to candidate prose")
	}
	return c.Sidecar.Validate()
}

type ManuscriptAuditArtifact struct {
	CandidateSignature string               `json:"candidate_signature"`
	Task               ManuscriptContentRef `json:"task"`
	Report             ManuscriptContentRef `json:"report"`
	Findings           ManuscriptContentRef `json:"findings"`
	Receipt            ManuscriptContentRef `json:"receipt"`
	EvidenceSignatures []string             `json:"evidence_signatures"`
	Signature          string               `json:"signature"`
}

func NewManuscriptAuditArtifact(candidateSignature string, task, report, findings, receipt ManuscriptContentRef, evidenceSignatures []string) ManuscriptAuditArtifact {
	artifact := ManuscriptAuditArtifact{CandidateSignature: candidateSignature, Task: task, Report: report, Findings: findings, Receipt: receipt, EvidenceSignatures: append([]string(nil), evidenceSignatures...)}
	unsigned := artifact
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	artifact.Signature = ContentSignature(payload)
	return artifact
}

func (a ManuscriptAuditArtifact) Validate() error {
	if len(a.CandidateSignature) != 64 || len(a.EvidenceSignatures) == 0 {
		return fmt.Errorf("audit artifact identity is incomplete")
	}
	for _, ref := range []ManuscriptContentRef{a.Task, a.Report, a.Findings, a.Receipt} {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("invalid audit artifact content: %w", err)
		}
	}
	unsigned := a
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	if ContentSignature(payload) != a.Signature {
		return fmt.Errorf("audit artifact signature is invalid")
	}
	return nil
}

type ManuscriptBatch struct {
	ID              string                        `json:"id"`
	Revision        int                           `json:"revision"`
	Attempt         int                           `json:"attempt"`
	ExpectedAttempt int                           `json:"expected_attempt"`
	Status          string                        `json:"status"`
	Items           []ManuscriptReworkItem        `json:"items"`
	Receipts        []ManuscriptGenerationReceipt `json:"receipts,omitempty"`
	SegmentPlan     *ManuscriptSegmentPlan        `json:"segment_plan,omitempty"`
}

type ManuscriptSegmentPlan struct {
	ChapterID   string `json:"chapter_id"`
	Attempt     int    `json:"attempt"`
	TargetRunes int    `json:"target_runes"`
	MinRunes    int    `json:"min_runes"`
	MaxRunes    int    `json:"max_runes"`
	MaxSegments int    `json:"max_segments"`
	Signature   string `json:"signature"`
}

func (p ManuscriptSegmentPlan) Validate() error {
	if strings.TrimSpace(p.ChapterID) == "" || p.Attempt <= 0 || p.TargetRunes <= 0 || p.MinRunes <= 0 || p.MaxRunes < p.MinRunes || p.MaxSegments <= 0 {
		return fmt.Errorf("manuscript segment plan is incomplete")
	}
	unsigned := p
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	if ContentSignature(payload) != p.Signature {
		return fmt.Errorf("manuscript segment plan signature is invalid")
	}
	return nil
}

type ManuscriptGenerationReceipt struct {
	RevisionID           string               `json:"revision_id"`
	ChapterID            string               `json:"chapter_id"`
	Attempt              int                  `json:"attempt"`
	Segment              int                  `json:"segment"`
	SegmentCount         int                  `json:"segment_count"`
	SegmentPlanSignature string               `json:"segment_plan_signature"`
	PromptSignature      string               `json:"prompt_signature"`
	ResponseSignature    string               `json:"response_signature,omitempty"`
	DependencyEvidence   string               `json:"dependency_evidence"`
	Status               string               `json:"status"`
	ErrorClass           string               `json:"error_class,omitempty"`
	Content              ManuscriptContentRef `json:"content,omitempty"`
}

func (r ManuscriptGenerationReceipt) Validate(plan ManuscriptSegmentPlan, expectedSegment int) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.RevisionID) == "" || r.ChapterID != plan.ChapterID || r.Attempt <= 0 || r.Segment != expectedSegment || r.SegmentPlanSignature != plan.Signature {
		return fmt.Errorf("generation receipt identity is not bound to its signed segment plan")
	}
	if len(r.PromptSignature) != 64 || len(r.DependencyEvidence) != 64 {
		return fmt.Errorf("generation receipt signatures are incomplete")
	}
	if r.Status == "completed" && (len(r.ResponseSignature) != 64 || r.Content.Validate() != nil) {
		return fmt.Errorf("completed generation receipt content is invalid")
	}
	return nil
}

type ManuscriptOutlinePreview struct {
	ChapterID         string            `json:"chapter_id"`
	Outline           OutlineEntry      `json:"outline"`
	Contract          NarrativeContract `json:"contract"`
	OutlineSignature  string            `json:"outline_signature"`
	ContractSignature string            `json:"contract_signature"`
}

type ManuscriptRevisionRuntime struct {
	Version                         int                         `json:"version"`
	RevisionID                      string                      `json:"revision_id"`
	Revision                        int                         `json:"revision"`
	Mode                            RevisionMode                `json:"mode"`
	PolicyID                        string                      `json:"policy_id"`
	PolicyVersion                   string                      `json:"policy_version"`
	Instruction                     string                      `json:"instruction"`
	InstructionKind                 ManuscriptInstructionKind   `json:"instruction_kind"`
	Stage                           string                      `json:"stage"`
	Baseline                        ManuscriptBaseline          `json:"baseline"`
	Queue                           []ManuscriptReworkItem      `json:"queue"`
	Batches                         []ManuscriptBatch           `json:"batches,omitempty"`
	Candidates                      []ManuscriptCandidate       `json:"candidates,omitempty"`
	OutlinePreview                  *ManuscriptOutlinePreview   `json:"outline_preview,omitempty"`
	PublicationStatus               ManuscriptPublicationStatus `json:"publication_status"`
	RequiresCompletionRevalidation  bool                        `json:"requires_completion_revalidation,omitempty"`
	CompletionRevalidationStatus    string                      `json:"completion_revalidation_status,omitempty"`
	CompletionRevalidationSignature string                      `json:"completion_revalidation_signature,omitempty"`
	LastErrorClass                  string                      `json:"last_error_class,omitempty"`
	UpdatedAt                       string                      `json:"updated_at"`
}

func (r ManuscriptRevisionRuntime) Validate() error {
	if r.Version != 1 || strings.TrimSpace(r.RevisionID) == "" || r.Revision <= 0 || strings.TrimSpace(r.PolicyID) == "" || r.PolicyVersion != ManuscriptRevisionPolicyVersion || strings.TrimSpace(r.Instruction) == "" {
		return fmt.Errorf("invalid manuscript revision runtime")
	}
	if err := r.Baseline.Validate(); err != nil {
		return err
	}
	if len(r.Queue) == 0 {
		return fmt.Errorf("manuscript revision queue is empty")
	}
	for _, item := range r.Queue {
		if err := item.Validate(r.Baseline.ChapterID); err != nil {
			return err
		}
	}
	for _, candidate := range r.Candidates {
		if err := candidate.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type ManuscriptRevisionError struct {
	Class string
	Err   error
}

func (e *ManuscriptRevisionError) Error() string {
	if e.Err == nil {
		return e.Class
	}
	return e.Class + ": " + e.Err.Error()
}

func (e *ManuscriptRevisionError) Unwrap() error { return e.Err }
