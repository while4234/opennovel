package domain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	FoundationRevisionSchemaVersion                       = 1
	FoundationRevisionPolicyID                            = "ainovel.foundation-revision"
	FoundationRevisionPolicyVersion                       = "1"
	RevisionModeFoundation                   RevisionMode = "foundation"
	FoundationArtifactID                                  = "story-foundation"
	FoundationArtifactKind                                = "foundation"
	FoundationPlanningArtifactID                          = "foundation-planning-snapshot"
	FoundationPlanningArtifactKind                        = "foundation-planning"
	FoundationAdaptationPlanningArtifactKind              = "foundation-adaptation-planning"
)

type FoundationEntityType string

const (
	FoundationEntityPremise      FoundationEntityType = "premise"
	FoundationEntityCharacter    FoundationEntityType = "character"
	FoundationEntityRelationship FoundationEntityType = "relationship"
	FoundationEntityWorldRule    FoundationEntityType = "world_rule"
)

type FoundationChangeKind string

const (
	FoundationChangeAdded    FoundationChangeKind = "added"
	FoundationChangeRemoved  FoundationChangeKind = "removed"
	FoundationChangeModified FoundationChangeKind = "modified"
)

type FoundationDiffChange struct {
	EntityType       FoundationEntityType `json:"entity_type"`
	EntityID         string               `json:"entity_id"`
	Kind             FoundationChangeKind `json:"kind"`
	ChangedFields    []string             `json:"changed_fields,omitempty"`
	HighRisk         bool                 `json:"high_risk,omitempty"`
	CoreCastAffected bool                 `json:"core_cast_affected,omitempty"`
	HardRuleAffected bool                 `json:"hard_rule_affected,omitempty"`
}

type FoundationDiff struct {
	Version                  int                    `json:"version"`
	Changes                  []FoundationDiffChange `json:"changes"`
	CoreCastReconfirmation   bool                   `json:"core_cast_reconfirmation"`
	FoundationReconfirmation bool                   `json:"foundation_reconfirmation"`
	Signature                string                 `json:"signature"`
}

func ComputeFoundationDiff(base, candidate StoryFoundation, coreCast *CoreCastContract) (FoundationDiff, error) {
	left, err := NormalizeStoryFoundation(base)
	if err != nil {
		return FoundationDiff{}, fmt.Errorf("normalize base foundation: %w", err)
	}
	right, err := NormalizeStoryFoundation(candidate)
	if err != nil {
		return FoundationDiff{}, fmt.Errorf("normalize candidate foundation: %w", err)
	}
	coreCharacters, coreRelationships := foundationCoreIDs(coreCast)
	changes := make([]FoundationDiffChange, 0)
	if left.Premise != right.Premise {
		changes = append(changes, FoundationDiffChange{EntityType: FoundationEntityPremise, EntityID: "premise", Kind: FoundationChangeModified, ChangedFields: []string{"premise"}, HighRisk: true})
	}
	changes = append(changes, diffFoundationCharacters(left.Characters, right.Characters, coreCharacters)...)
	changes = append(changes, diffFoundationRelationships(left.Relationships, right.Relationships, coreRelationships)...)
	changes = append(changes, diffFoundationRules(left.WorldRules, right.WorldRules)...)
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].EntityType != changes[j].EntityType {
			return changes[i].EntityType < changes[j].EntityType
		}
		if changes[i].EntityID != changes[j].EntityID {
			return changes[i].EntityID < changes[j].EntityID
		}
		return changes[i].Kind < changes[j].Kind
	})
	diff := FoundationDiff{Version: FoundationRevisionSchemaVersion, Changes: changes, FoundationReconfirmation: len(changes) > 0}
	for _, change := range changes {
		diff.CoreCastReconfirmation = diff.CoreCastReconfirmation || change.CoreCastAffected
	}
	unsigned := diff
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	diff.Signature = ContentSignature(payload)
	return diff, nil
}

func (d FoundationDiff) Validate() error {
	if d.Version != FoundationRevisionSchemaVersion || strings.TrimSpace(d.Signature) == "" {
		return fmt.Errorf("foundation diff identity is incomplete")
	}
	unsigned := d
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	if d.Signature != ContentSignature(payload) {
		return fmt.Errorf("foundation diff signature mismatch")
	}
	for index, change := range d.Changes {
		if strings.TrimSpace(change.EntityID) == "" || !validFoundationEntityType(change.EntityType) || !validFoundationChangeKind(change.Kind) {
			return fmt.Errorf("foundation diff change %d is invalid", index)
		}
	}
	return nil
}

func (d FoundationDiff) ChangedEntityIDs() []string {
	ids := make([]string, 0, len(d.Changes))
	for _, change := range d.Changes {
		ids = append(ids, string(change.EntityType)+":"+change.EntityID)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

type FoundationDependency struct {
	ID                        string               `json:"id"`
	SourceEntityType          FoundationEntityType `json:"source_entity_type"`
	SourceEntityID            string               `json:"source_entity_id"`
	DependentArtifactType     string               `json:"dependent_artifact_type"`
	DependentArtifactID       string               `json:"dependent_artifact_id"`
	VolumeID                  string               `json:"volume_id,omitempty"`
	ArcID                     string               `json:"arc_id,omitempty"`
	ChapterID                 string               `json:"chapter_id,omitempty"`
	FromChapter               int                  `json:"from_chapter,omitempty"`
	ToChapter                 int                  `json:"to_chapter,omitempty"`
	DependencyKind            string               `json:"dependency_kind"`
	FoundationSignature       string               `json:"foundation_signature"`
	DependentContentSignature string               `json:"dependent_content_signature"`
	EvidenceSource            string               `json:"evidence_source"`
	SourceAnchorIDs           []string             `json:"source_anchor_ids,omitempty"`
	ContractIDs               []string             `json:"contract_ids,omitempty"`
}

type FoundationDependencyManifest struct {
	Version             int                    `json:"version"`
	FoundationSignature string                 `json:"foundation_signature"`
	Entries             []FoundationDependency `json:"entries"`
	UpdatedAt           string                 `json:"updated_at"`
	Signature           string                 `json:"signature"`
}

func NewFoundationDependencyManifest(foundationSignature string, entries []FoundationDependency) (FoundationDependencyManifest, error) {
	manifest := FoundationDependencyManifest{Version: FoundationRevisionSchemaVersion, FoundationSignature: strings.TrimSpace(foundationSignature), Entries: append([]FoundationDependency(nil), entries...), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].ID < manifest.Entries[j].ID })
	if err := manifest.validateEntries(); err != nil {
		return FoundationDependencyManifest{}, err
	}
	unsigned := manifest
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	manifest.Signature = ContentSignature(payload)
	return manifest, nil
}

func (m FoundationDependencyManifest) Validate() error {
	if m.Version != FoundationRevisionSchemaVersion || len(m.FoundationSignature) != 64 || strings.TrimSpace(m.Signature) == "" {
		return fmt.Errorf("foundation dependency manifest identity is incomplete")
	}
	if err := m.validateEntries(); err != nil {
		return err
	}
	unsigned := m
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	if m.Signature != ContentSignature(payload) {
		return fmt.Errorf("foundation dependency manifest signature mismatch")
	}
	return nil
}

func (m FoundationDependencyManifest) validateEntries() error {
	seen := make(map[string]struct{}, len(m.Entries))
	for index, entry := range m.Entries {
		if strings.TrimSpace(entry.ID) == "" || !validFoundationEntityType(entry.SourceEntityType) || strings.TrimSpace(entry.SourceEntityID) == "" ||
			strings.TrimSpace(entry.DependentArtifactType) == "" || strings.TrimSpace(entry.DependentArtifactID) == "" || strings.TrimSpace(entry.DependencyKind) == "" ||
			len(entry.FoundationSignature) != 64 || len(entry.DependentContentSignature) != 64 || strings.TrimSpace(entry.EvidenceSource) == "" {
			return fmt.Errorf("foundation dependency %d is incomplete", index)
		}
		if entry.FoundationSignature != m.FoundationSignature {
			return fmt.Errorf("foundation dependency %q is stale", entry.ID)
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return fmt.Errorf("duplicate foundation dependency %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
	}
	return nil
}

type FoundationImpactReason struct {
	Code        string   `json:"code"`
	EntityIDs   []string `json:"entity_ids,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Required    bool     `json:"required"`
}

type FoundationAuditScope struct {
	Scope       string `json:"scope"`
	ScopeID     string `json:"scope_id,omitempty"`
	Volume      int    `json:"volume,omitempty"`
	Arc         int    `json:"arc,omitempty"`
	FromChapter int    `json:"from_chapter,omitempty"`
	ToChapter   int    `json:"to_chapter,omitempty"`
	Required    bool   `json:"required"`
}

type FoundationImpact struct {
	Version                        int                         `json:"version"`
	EvidenceLevel                  string                      `json:"evidence_level"`
	FullBook                       bool                        `json:"full_book"`
	AffectedVolumeIDs              []string                    `json:"affected_volume_ids,omitempty"`
	AffectedArcIDs                 []string                    `json:"affected_arc_ids,omitempty"`
	AffectedChapterIDs             []string                    `json:"affected_chapter_ids,omitempty"`
	Reasons                        []FoundationImpactReason    `json:"reasons"`
	RequiredAudits                 []FoundationAuditScope      `json:"required_audits"`
	RequiresCoreCastConfirmation   bool                        `json:"requires_core_cast_confirmation"`
	RequiresFoundationConfirmation bool                        `json:"requires_foundation_confirmation"`
	Adaptation                     *FoundationAdaptationImpact `json:"adaptation,omitempty"`
	Signature                      string                      `json:"signature"`
}

// FoundationAdaptationImpact augments the common target-Foundation impact.
// It contains only target planning consequences and immutable source/contract
// references; source evidence is never represented as a writable artifact.
type FoundationAdaptationImpact struct {
	EvidenceLevel                      string   `json:"evidence_level"`
	SourceAnchorIDs                    []string `json:"source_anchor_ids,omitempty"`
	ContractIDs                        []string `json:"contract_ids,omitempty"`
	ExpansionReasons                   []string `json:"expansion_reasons,omitempty"`
	RequiresCoreCastReconfirmation     bool     `json:"requires_core_cast_reconfirmation"`
	RequiresAdaptationPlanConfirmation bool     `json:"requires_adaptation_plan_confirmation"`
	SourceFidelityReview               bool     `json:"source_fidelity_review"`
	TargetConsistencyReview            bool     `json:"target_consistency_review"`
	CharacterMappingReview             bool     `json:"character_mapping_review"`
	PlanContractReview                 bool     `json:"plan_contract_review"`
	OutlineQualityReview               bool     `json:"outline_quality_review"`
	AffectedProposal                   bool     `json:"affected_proposal"`
	AffectedOutline                    bool     `json:"affected_outline"`
}

type FoundationAdaptationBaseline struct {
	SourceSignature             string `json:"source_signature"`
	SourceManifestSignature     string `json:"source_manifest_signature"`
	AdaptationIntentHash        string `json:"adaptation_intent_hash"`
	WorkflowRevision            int    `json:"workflow_revision"`
	WorkflowStage               string `json:"workflow_stage"`
	PlanSemanticSignature       string `json:"plan_semantic_signature"`
	PlanStoryContractSignature  string `json:"plan_story_contract_signature"`
	PlanOutlineQualitySignature string `json:"plan_outline_quality_signature"`
	CoreCastReconfirmed         bool   `json:"core_cast_reconfirmed"`
}

func (b FoundationAdaptationBaseline) Validate() error {
	if len(b.SourceSignature) != 64 || len(b.SourceManifestSignature) != 64 ||
		strings.TrimSpace(b.AdaptationIntentHash) == "" || b.WorkflowRevision <= 0 ||
		strings.TrimSpace(b.WorkflowStage) == "" || len(b.PlanSemanticSignature) != 64 ||
		len(b.PlanStoryContractSignature) != 64 || len(b.PlanOutlineQualitySignature) != 64 {
		return fmt.Errorf("adaptation Foundation baseline is incomplete")
	}
	return nil
}

type FoundationPreviewValidation struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type FoundationRevisionPreview struct {
	Version                     int                           `json:"version"`
	ID                          string                        `json:"id"`
	ProjectMode                 string                        `json:"project_mode"`
	BaseRevision                int64                         `json:"base_revision"`
	BaseAuditSignature          string                        `json:"base_audit_signature"`
	BaseCoreCastSignature       string                        `json:"base_core_cast_signature"`
	BasePlanningSignature       string                        `json:"base_planning_signature"`
	AdaptationBaseline          *FoundationAdaptationBaseline `json:"adaptation_baseline,omitempty"`
	Generation                  uint64                        `json:"generation"`
	Base                        StoryFoundation               `json:"base"`
	Candidate                   StoryFoundation               `json:"candidate"`
	CandidateSignature          string                        `json:"candidate_signature"`
	Diff                        FoundationDiff                `json:"diff"`
	Impact                      FoundationImpact              `json:"impact"`
	DependencySnapshotSignature string                        `json:"dependency_snapshot_signature,omitempty"`
	Validation                  FoundationPreviewValidation   `json:"validation"`
	CanApply                    bool                          `json:"can_apply"`
	ReadonlyReason              string                        `json:"readonly_reason,omitempty"`
	CreatedAt                   string                        `json:"created_at"`
	ExpiresAt                   string                        `json:"expires_at"`
	Signature                   string                        `json:"signature"`
}

func (p FoundationRevisionPreview) Validate() error {
	if p.Version != FoundationRevisionSchemaVersion || strings.TrimSpace(p.ID) == "" || p.BaseRevision < 0 || p.Generation == 0 || len(p.CandidateSignature) != 64 || len(p.Signature) != 64 {
		return fmt.Errorf("foundation preview identity is incomplete")
	}
	if _, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err != nil {
		return fmt.Errorf("invalid foundation preview creation time")
	}
	if _, err := time.Parse(time.RFC3339Nano, p.ExpiresAt); err != nil {
		return fmt.Errorf("invalid foundation preview expiry time")
	}
	if err := p.Diff.Validate(); err != nil {
		return err
	}
	if err := p.Impact.Validate(); err != nil {
		return err
	}
	signature, err := FoundationContentSignature(p.Candidate)
	if err != nil || signature != p.CandidateSignature {
		return fmt.Errorf("foundation preview candidate signature mismatch")
	}
	if p.Base.Revision != p.BaseRevision {
		return fmt.Errorf("foundation preview base revision mismatch")
	}
	if p.ProjectMode == "adaptation" {
		if p.AdaptationBaseline == nil {
			return fmt.Errorf("adaptation Foundation preview baseline is missing")
		}
		if err := p.AdaptationBaseline.Validate(); err != nil {
			return err
		}
	} else if p.AdaptationBaseline != nil {
		return fmt.Errorf("normal Foundation preview contains adaptation baseline")
	}
	unsigned := p
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	if p.Signature != ContentSignature(payload) {
		return fmt.Errorf("foundation preview signature mismatch")
	}
	return nil
}

func SignFoundationRevisionPreview(preview FoundationRevisionPreview) FoundationRevisionPreview {
	preview.Signature = ""
	payload, _ := json.Marshal(preview)
	preview.Signature = ContentSignature(payload)
	return preview
}

type FoundationPublicationReceipt struct {
	Status             string `json:"status"`
	CandidateSignature string `json:"candidate_signature"`
	FoundationRevision int64  `json:"foundation_revision"`
	PublishedAt        string `json:"published_at"`
}

type FoundationRevisionRuntime struct {
	Version        int                           `json:"version"`
	RevisionID     string                        `json:"revision_id"`
	SessionID      string                        `json:"session_id"`
	PreviewID      string                        `json:"preview_id"`
	ProjectMode    string                        `json:"project_mode"`
	Stage          string                        `json:"stage"`
	ResumeStage    string                        `json:"resume_stage,omitempty"`
	Attempt        int                           `json:"attempt"`
	Generation     uint64                        `json:"generation"`
	Impact         FoundationImpact              `json:"impact"`
	Publication    *FoundationPublicationReceipt `json:"publication,omitempty"`
	LastErrorClass string                        `json:"last_error_class,omitempty"`
	LastError      string                        `json:"last_error,omitempty"`
	CreatedAt      string                        `json:"created_at"`
	UpdatedAt      string                        `json:"updated_at"`
}

func (r FoundationRevisionRuntime) Active() bool {
	return r.Stage != "completed" && r.Stage != "cancelled"
}

func AnalyzeFoundationImpact(diff FoundationDiff, manifest *FoundationDependencyManifest) (FoundationImpact, error) {
	if err := diff.Validate(); err != nil {
		return FoundationImpact{}, err
	}
	impact := FoundationImpact{Version: FoundationRevisionSchemaVersion, EvidenceLevel: "missing", RequiresCoreCastConfirmation: diff.CoreCastReconfirmation, RequiresFoundationConfirmation: len(diff.Changes) > 0}
	fullReason := "dependency_evidence_missing"
	if manifest != nil && manifest.Validate() == nil {
		impact.EvidenceLevel = "structured"
		bySource := make(map[string][]FoundationDependency)
		for _, entry := range manifest.Entries {
			key := string(entry.SourceEntityType) + ":" + entry.SourceEntityID
			bySource[key] = append(bySource[key], entry)
		}
		local := true
		for _, change := range diff.Changes {
			key := string(change.EntityType) + ":" + change.EntityID
			entries := bySource[key]
			if change.HighRisk || change.HardRuleAffected || change.Kind == FoundationChangeAdded || len(entries) == 0 || widelyReferencedRelationship(change, entries) {
				local = false
				fullReason = foundationExpansionReason(change, len(entries) == 0)
				break
			}
			for _, entry := range entries {
				impact.AffectedVolumeIDs = append(impact.AffectedVolumeIDs, entry.VolumeID)
				impact.AffectedArcIDs = append(impact.AffectedArcIDs, entry.ArcID)
				impact.AffectedChapterIDs = append(impact.AffectedChapterIDs, entry.ChapterID)
				impact.Reasons = append(impact.Reasons, FoundationImpactReason{Code: "structured_dependency", EntityIDs: []string{key}, EvidenceIDs: []string{entry.ID}, Required: true})
			}
		}
		if local {
			impact.AffectedVolumeIDs = compactNonEmpty(impact.AffectedVolumeIDs)
			impact.AffectedArcIDs = compactNonEmpty(impact.AffectedArcIDs)
			impact.AffectedChapterIDs = compactNonEmpty(impact.AffectedChapterIDs)
			impact.RequiredAudits = localFoundationAuditScopes(impact)
		}
	}
	if len(diff.Changes) > 0 && len(impact.RequiredAudits) == 0 {
		impact.FullBook = true
		impact.EvidenceLevel = map[bool]string{true: "conflict", false: impact.EvidenceLevel}[manifest != nil && impact.EvidenceLevel == "structured"]
		impact.Reasons = []FoundationImpactReason{{Code: fullReason, EntityIDs: diff.ChangedEntityIDs(), Required: true}}
		impact.RequiredAudits = []FoundationAuditScope{{Scope: "book", ScopeID: "book", Required: true}}
	}
	unsigned := impact
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	impact.Signature = ContentSignature(payload)
	return impact, nil
}

func widelyReferencedRelationship(change FoundationDiffChange, entries []FoundationDependency) bool {
	if change.EntityType != FoundationEntityRelationship || change.Kind != FoundationChangeRemoved {
		return false
	}
	volumes := make(map[string]struct{})
	for _, entry := range entries {
		if entry.VolumeID != "" {
			volumes[entry.VolumeID] = struct{}{}
		}
	}
	return len(volumes) > 1 || len(entries) > 4
}

func (i FoundationImpact) Validate() error {
	if i.Version != FoundationRevisionSchemaVersion || strings.TrimSpace(i.EvidenceLevel) == "" || strings.TrimSpace(i.Signature) == "" {
		return fmt.Errorf("foundation impact identity is incomplete")
	}
	unsigned := i
	unsigned.Signature = ""
	payload, _ := json.Marshal(unsigned)
	if i.Signature != ContentSignature(payload) {
		return fmt.Errorf("foundation impact signature mismatch")
	}
	if i.Adaptation != nil {
		if strings.TrimSpace(i.Adaptation.EvidenceLevel) == "" {
			return fmt.Errorf("adaptation Foundation impact evidence level is required")
		}
		if i.RequiresFoundationConfirmation && (!i.Adaptation.SourceFidelityReview || !i.Adaptation.TargetConsistencyReview ||
			!i.Adaptation.CharacterMappingReview || !i.Adaptation.PlanContractReview || !i.Adaptation.OutlineQualityReview ||
			!i.Adaptation.AffectedProposal || !i.Adaptation.AffectedOutline) {
			return fmt.Errorf("adaptation Foundation impact requires source-fidelity, target-consistency, character-mapping, plan-contract, proposal, and outline review")
		}
	}
	return nil
}

type FoundationRevisionPolicy struct{}

func (FoundationRevisionPolicy) Mode() RevisionMode { return RevisionModeFoundation }
func (FoundationRevisionPolicy) Identity() (string, string) {
	return FoundationRevisionPolicyID, FoundationRevisionPolicyVersion
}
func (FoundationRevisionPolicy) ApprovalStages(impact RevisionImpact) ([]RevisionApprovalStage, error) {
	if err := (FoundationRevisionPolicy{}).ValidateImpact(impact); err != nil {
		return nil, err
	}
	return []RevisionApprovalStage{
		{ID: "foundation_apply", Label: "Apply reviewed Foundation"},
		{ID: "outline_approval", Label: "Approve repaired outline"},
	}, nil
}
func (FoundationRevisionPolicy) ValidateImpact(impact RevisionImpact) error { return impact.Validate() }
func (FoundationRevisionPolicy) ValidateCandidate(session RevisionSession, versions []ArtifactVersion) error {
	if session.Mode != RevisionModeFoundation {
		return fmt.Errorf("foundation revision mode is required")
	}
	if len(session.Approvals) >= 1 {
		var planning *ArtifactVersion
		for index := range versions {
			version := &versions[index]
			switch version.ArtifactID {
			case FoundationPlanningArtifactID:
				if (version.ArtifactKind != FoundationPlanningArtifactKind && version.ArtifactKind != FoundationAdaptationPlanningArtifactKind) || planning != nil {
					return fmt.Errorf("foundation planning candidate is duplicated or has the wrong kind")
				}
				planning = version
			case FoundationArtifactID:
				if len(session.Approvals) < 2 || version.ArtifactKind != FoundationArtifactKind {
					return fmt.Errorf("foundation artifact is not allowed in this planning stage")
				}
			default:
				return fmt.Errorf("unexpected Foundation revision artifact %q", version.ArtifactID)
			}
		}
		if planning == nil || (len(session.Approvals) == 1 && len(versions) != 1) || (len(session.Approvals) >= 2 && len(versions) != 2) {
			return fmt.Errorf("foundation planning stage requires its canonical planning snapshot")
		}
		if planning.ArtifactKind == FoundationAdaptationPlanningArtifactKind {
			var plan AdaptationPlan
			if err := json.Unmarshal(planning.Payload, &plan); err != nil {
				return fmt.Errorf("decode adaptation Foundation planning candidate: %w", err)
			}
			if plan.Status != AdaptationPlanStatusConfirmed || len(plan.Chapters) == 0 || !AdaptationOutlineQualityPassed(plan) {
				return fmt.Errorf("adaptation Foundation planning candidate must be confirmed and carry a passed outline-quality receipt")
			}
		} else {
			var volumes []VolumeOutline
			if err := json.Unmarshal(planning.Payload, &volumes); err != nil || len(volumes) == 0 {
				return fmt.Errorf("foundation planning candidate is invalid: %w", err)
			}
		}
		return nil
	}
	if len(versions) != 1 {
		return fmt.Errorf("foundation apply stage requires exactly one canonical Foundation")
	}
	if versions[0].ArtifactID != FoundationArtifactID || versions[0].ArtifactKind != FoundationArtifactKind {
		return fmt.Errorf("foundation apply stage requires exactly one canonical foundation artifact")
	}
	var value StoryFoundation
	if err := json.Unmarshal(versions[0].Payload, &value); err != nil {
		return err
	}
	_, err := NormalizeStoryFoundation(value)
	return err
}
func (FoundationRevisionPolicy) AuditExpectations(_ RevisionSession, versions []ArtifactVersion) ([]RevisionAuditExpectation, error) {
	if len(versions) != 1 {
		return nil, fmt.Errorf("foundation audit requires exactly one canonical artifact")
	}
	scope := "foundation"
	if versions[0].ArtifactID == FoundationPlanningArtifactID {
		if versions[0].ArtifactKind == FoundationAdaptationPlanningArtifactKind {
			scope = "adaptation_planning"
		} else {
			scope = "planning"
		}
	}
	return []RevisionAuditExpectation{{
		Scope: scope, ScopeID: versions[0].ArtifactID, ContentSignature: versions[0].ContentSignature,
	}}, nil
}
func (FoundationRevisionPolicy) ValidateAuditSet(session RevisionSession, evidence []RevisionAuditEvidence) error {
	if len(session.AuditExpectations) != 1 || len(evidence) != 1 {
		return fmt.Errorf("foundation revision requires its exact persisted audit expectation")
	}
	expected, actual := session.AuditExpectations[0], evidence[0]
	if err := actual.Validate(); err != nil {
		return err
	}
	if actual.Scope != expected.Scope || actual.ScopeID != expected.ScopeID || actual.ContentSignature != expected.ContentSignature ||
		actual.FromChapter != expected.FromChapter || actual.ToChapter != expected.ToChapter {
		return fmt.Errorf("foundation revision audit does not match the persisted candidate")
	}
	return nil
}
func (FoundationRevisionPolicy) Route(session RevisionSession) (*RevisionRoute, error) {
	if session.Stage != RevisionStageCandidateGenerating || len(session.Approvals) != 1 {
		return nil, nil
	}
	for _, item := range session.Impact.Items {
		if item.ArtifactKind == FoundationAdaptationPlanningArtifactKind {
			// Adaptation regeneration is driven by the existing adaptation proposal
			// pipeline under the Foundation revision's narrow mutation owner.
			return nil, nil
		}
	}
	return &RevisionRoute{Agent: "architect_long", Task: "continue the existing original-planning repair and signed audit route", Reason: "foundation_revision_planning", SessionID: session.ID, Revision: session.Revision, Generation: session.Generation}, nil
}

func (FoundationRevisionPolicy) ContinueAfterApproval(_ RevisionSession, stage RevisionApprovalStage) bool {
	return stage.ID == "foundation_apply"
}

func FoundationRevisionImpact(value FoundationImpact) (RevisionImpact, error) {
	items := []RevisionImpactItem{
		{ArtifactID: FoundationArtifactID, ArtifactKind: FoundationArtifactKind, Change: "publish reviewed canonical Foundation", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency},
		{ArtifactID: FoundationPlanningArtifactID, ArtifactKind: FoundationPlanningArtifactKind, Change: "repair and re-audit affected original planning", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency},
	}
	if value.Adaptation != nil {
		items[1].ArtifactKind = FoundationAdaptationPlanningArtifactKind
		items[1].Change = "regenerate and re-audit affected adaptation proposal and outline"
	}
	if value.FullBook {
		items = append(items, RevisionImpactItem{ArtifactID: "book", ArtifactKind: "book", Change: "rebuild planning from revised foundation", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency})
	} else {
		for _, id := range value.AffectedVolumeIDs {
			items = append(items, RevisionImpactItem{ArtifactID: id, ArtifactKind: StructureKindVolume, Change: "rebuild planning from revised foundation", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency})
		}
		for _, id := range value.AffectedArcIDs {
			items = append(items, RevisionImpactItem{ArtifactID: id, ArtifactKind: StructureKindArc, Change: "rebuild planning from revised foundation", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency})
		}
		for _, id := range value.AffectedChapterIDs {
			items = append(items, RevisionImpactItem{ArtifactID: id, ArtifactKind: StructureKindChapter, Change: "rebuild planning from revised foundation", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency})
		}
	}
	return NewRevisionImpact("Foundation revision planning impact", items)
}

func FoundationProjectionSignature(value StoryFoundation, refs []string) (string, error) {
	normalized, err := NormalizeStoryFoundation(value)
	if err != nil {
		return "", err
	}
	characters := make(map[string]Character, len(normalized.Characters))
	for _, item := range normalized.Characters {
		characters[item.ID] = item
	}
	relationships := make(map[string]CharacterRelationship, len(normalized.Relationships))
	for _, item := range normalized.Relationships {
		relationships[item.ID] = item
	}
	rules := make(map[string]WorldRule, len(normalized.WorldRules))
	for _, item := range normalized.WorldRules {
		rules[item.ID] = item
	}
	refs = append([]string(nil), refs...)
	slices.Sort(refs)
	projection := make(map[string]any, len(refs))
	for _, ref := range slices.Compact(refs) {
		parts := strings.SplitN(ref, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			return "", fmt.Errorf("invalid foundation entity reference %q", ref)
		}
		switch FoundationEntityType(parts[0]) {
		case FoundationEntityPremise:
			projection[ref] = normalized.Premise
		case FoundationEntityCharacter:
			value, ok := characters[parts[1]]
			if !ok {
				return "", fmt.Errorf("foundation character %q is missing", parts[1])
			}
			projection[ref] = value
		case FoundationEntityRelationship:
			value, ok := relationships[parts[1]]
			if !ok {
				return "", fmt.Errorf("foundation relationship %q is missing", parts[1])
			}
			projection[ref] = value
		case FoundationEntityWorldRule:
			value, ok := rules[parts[1]]
			if !ok {
				return "", fmt.Errorf("foundation world rule %q is missing", parts[1])
			}
			projection[ref] = value
		default:
			return "", fmt.Errorf("unknown foundation entity reference %q", ref)
		}
	}
	payload, _ := json.Marshal(projection)
	return ContentSignature(payload), nil
}

func foundationCoreIDs(contract *CoreCastContract) (map[string]bool, map[string]bool) {
	characters, relationships := map[string]bool{}, map[string]bool{}
	if contract == nil {
		return characters, relationships
	}
	for _, member := range contract.Members {
		characters[member.Character.ID] = true
	}
	for _, relation := range contract.PlannedRelationships {
		relationships[relation.ID] = true
	}
	return characters, relationships
}

func diffFoundationCharacters(base, candidate []Character, core map[string]bool) []FoundationDiffChange {
	left, right := map[string]Character{}, map[string]Character{}
	for _, value := range base {
		left[value.ID] = value
	}
	for _, value := range candidate {
		right[value.ID] = value
	}
	ids := unionFoundationIDs(left, right)
	changes := make([]FoundationDiffChange, 0)
	for _, id := range ids {
		before, hadBefore := left[id]
		after, hasAfter := right[id]
		kind := FoundationChangeModified
		switch {
		case !hadBefore:
			kind = FoundationChangeAdded
		case !hasAfter:
			kind = FoundationChangeRemoved
		case reflect.DeepEqual(before, after):
			continue
		}
		changes = append(changes, FoundationDiffChange{EntityType: FoundationEntityCharacter, EntityID: id, Kind: kind, ChangedFields: changedJSONFields(before, after), CoreCastAffected: core[id], HighRisk: core[id] && kind != FoundationChangeAdded})
	}
	return changes
}

func diffFoundationRelationships(base, candidate []CharacterRelationship, core map[string]bool) []FoundationDiffChange {
	left, right := map[string]CharacterRelationship{}, map[string]CharacterRelationship{}
	for _, value := range base {
		left[value.ID] = value
	}
	for _, value := range candidate {
		right[value.ID] = value
	}
	ids := unionFoundationIDs(left, right)
	changes := make([]FoundationDiffChange, 0)
	for _, id := range ids {
		before, hadBefore := left[id]
		after, hasAfter := right[id]
		kind := FoundationChangeModified
		switch {
		case !hadBefore:
			kind = FoundationChangeAdded
		case !hasAfter:
			kind = FoundationChangeRemoved
		case reflect.DeepEqual(before, after):
			continue
		}
		fields := changedJSONFields(before, after)
		highRisk := kind == FoundationChangeRemoved || slices.Contains(fields, "source_character_id") || slices.Contains(fields, "target_character_id") || slices.Contains(fields, "direction")
		changes = append(changes, FoundationDiffChange{EntityType: FoundationEntityRelationship, EntityID: id, Kind: kind, ChangedFields: fields, CoreCastAffected: core[id], HighRisk: highRisk && core[id]})
	}
	return changes
}

func diffFoundationRules(base, candidate []WorldRule) []FoundationDiffChange {
	left, right := map[string]WorldRule{}, map[string]WorldRule{}
	for _, value := range base {
		left[value.ID] = value
	}
	for _, value := range candidate {
		right[value.ID] = value
	}
	ids := unionFoundationIDs(left, right)
	changes := make([]FoundationDiffChange, 0)
	for _, id := range ids {
		before, hadBefore := left[id]
		after, hasAfter := right[id]
		kind := FoundationChangeModified
		switch {
		case !hadBefore:
			kind = FoundationChangeAdded
		case !hasAfter:
			kind = FoundationChangeRemoved
		case reflect.DeepEqual(before, after):
			continue
		}
		hard := (hadBefore && before.Strength == WorldRuleStrengthHard) || (hasAfter && after.Strength == WorldRuleStrengthHard)
		changes = append(changes, FoundationDiffChange{EntityType: FoundationEntityWorldRule, EntityID: id, Kind: kind, ChangedFields: changedJSONFields(before, after), HardRuleAffected: hard, HighRisk: hard})
	}
	return changes
}

func changedJSONFields(left, right any) []string {
	var a, b map[string]any
	la, _ := json.Marshal(left)
	lb, _ := json.Marshal(right)
	_ = json.Unmarshal(la, &a)
	_ = json.Unmarshal(lb, &b)
	keys := make([]string, 0)
	for key, value := range a {
		if !reflect.DeepEqual(value, b[key]) {
			keys = append(keys, key)
		}
	}
	for key, value := range b {
		if _, ok := a[key]; !ok && value != nil {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func unionFoundationIDs[T any](left, right map[string]T) []string {
	ids := make([]string, 0, len(left)+len(right))
	for id := range left {
		ids = append(ids, id)
	}
	for id := range right {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func compactNonEmpty(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func localFoundationAuditScopes(impact FoundationImpact) []FoundationAuditScope {
	result := make([]FoundationAuditScope, 0, len(impact.AffectedChapterIDs)+len(impact.AffectedArcIDs)+len(impact.AffectedVolumeIDs)+1)
	for _, id := range impact.AffectedChapterIDs {
		result = append(result, FoundationAuditScope{Scope: "chapter", ScopeID: id, Required: true})
	}
	for _, id := range impact.AffectedArcIDs {
		result = append(result, FoundationAuditScope{Scope: "arc", ScopeID: id, Required: true})
	}
	for _, id := range impact.AffectedVolumeIDs {
		result = append(result, FoundationAuditScope{Scope: "volume", ScopeID: id, Required: true})
	}
	result = append(result, FoundationAuditScope{Scope: "book", ScopeID: "book", Required: true})
	return result
}

func foundationExpansionReason(change FoundationDiffChange, missing bool) string {
	if missing {
		return "dependency_evidence_missing"
	}
	if change.EntityType == FoundationEntityPremise {
		return "premise_mainline_changed"
	}
	if change.HardRuleAffected {
		return "hard_world_rule_changed"
	}
	if change.CoreCastAffected {
		return "core_cast_changed"
	}
	if change.Kind == FoundationChangeAdded {
		return "new_entity_scope_unproven"
	}
	return "dependency_evidence_conflict"
}

func validFoundationEntityType(value FoundationEntityType) bool {
	switch value {
	case FoundationEntityPremise, FoundationEntityCharacter, FoundationEntityRelationship, FoundationEntityWorldRule:
		return true
	}
	return false
}

func validFoundationChangeKind(value FoundationChangeKind) bool {
	switch value {
	case FoundationChangeAdded, FoundationChangeRemoved, FoundationChangeModified:
		return true
	}
	return false
}
