package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const RevisionSchemaVersion = 1

type RevisionMode string

type RevisionStage string

const (
	RevisionStageImpactReviewPending RevisionStage = "impact_review_pending"
	RevisionStageCandidateGenerating RevisionStage = "candidate_generating"
	RevisionStageCandidateAudit      RevisionStage = "candidate_audit_pending"
	RevisionStageApprovalPending     RevisionStage = "approval_pending"
	RevisionStageReadyToPublish      RevisionStage = "ready_to_publish"
	RevisionStagePaused              RevisionStage = "paused"
	RevisionStageFailed              RevisionStage = "failed"
	RevisionStageCancelled           RevisionStage = "cancelled"
	RevisionStageCompleted           RevisionStage = "completed"
)

func (s RevisionStage) Valid() bool {
	switch s {
	case RevisionStageImpactReviewPending,
		RevisionStageCandidateGenerating,
		RevisionStageCandidateAudit,
		RevisionStageApprovalPending,
		RevisionStageReadyToPublish,
		RevisionStagePaused,
		RevisionStageFailed,
		RevisionStageCancelled,
		RevisionStageCompleted:
		return true
	default:
		return false
	}
}

func (s RevisionStage) Terminal() bool {
	return s == RevisionStageCancelled || s == RevisionStageCompleted
}

type RevisionImpactItem struct {
	ArtifactID          string               `json:"artifact_id"`
	ArtifactKind        string               `json:"artifact_kind"`
	Change              string               `json:"change"`
	Requirement         StructureImpactLevel `json:"requirement,omitempty"`
	Cause               StructureImpactCause `json:"cause,omitempty"`
	RequiresBodyRewrite bool                 `json:"requires_body_rewrite,omitempty"`
	DependencyEvidence  []string             `json:"dependency_evidence,omitempty"`
	DependencySourceIDs []string             `json:"dependency_source_ids,omitempty"`
}

type RevisionImpact struct {
	Summary   string               `json:"summary"`
	Items     []RevisionImpactItem `json:"items"`
	Signature string               `json:"signature"`
}

func NewRevisionImpact(summary string, items []RevisionImpactItem) (RevisionImpact, error) {
	impact := RevisionImpact{Summary: strings.TrimSpace(summary), Items: append([]RevisionImpactItem(nil), items...)}
	for index := range impact.Items {
		impact.Items[index].DependencyEvidence = append([]string(nil), impact.Items[index].DependencyEvidence...)
		impact.Items[index].DependencySourceIDs = append([]string(nil), impact.Items[index].DependencySourceIDs...)
		impact.Items[index].ArtifactID = strings.TrimSpace(impact.Items[index].ArtifactID)
		impact.Items[index].ArtifactKind = strings.TrimSpace(impact.Items[index].ArtifactKind)
		impact.Items[index].Change = strings.TrimSpace(impact.Items[index].Change)
		for evidenceIndex := range impact.Items[index].DependencyEvidence {
			impact.Items[index].DependencyEvidence[evidenceIndex] = strings.TrimSpace(impact.Items[index].DependencyEvidence[evidenceIndex])
		}
		for sourceIndex := range impact.Items[index].DependencySourceIDs {
			impact.Items[index].DependencySourceIDs[sourceIndex] = strings.TrimSpace(impact.Items[index].DependencySourceIDs[sourceIndex])
		}
	}
	if err := impact.Validate(); err != nil {
		return RevisionImpact{}, err
	}
	payload, err := json.Marshal(struct {
		Summary string               `json:"summary"`
		Items   []RevisionImpactItem `json:"items"`
	}{Summary: impact.Summary, Items: impact.Items})
	if err != nil {
		return RevisionImpact{}, err
	}
	impact.Signature = ContentSignature(payload)
	return impact, nil
}

func (i RevisionImpact) Validate() error {
	if strings.TrimSpace(i.Summary) == "" {
		return fmt.Errorf("revision impact summary is required")
	}
	if len(i.Items) == 0 {
		return fmt.Errorf("revision impact must contain at least one item")
	}
	seen := make(map[string]struct{}, len(i.Items))
	for index, item := range i.Items {
		item.ArtifactID = strings.TrimSpace(item.ArtifactID)
		item.ArtifactKind = strings.TrimSpace(item.ArtifactKind)
		item.Change = strings.TrimSpace(item.Change)
		if item.ArtifactID == "" || item.ArtifactKind == "" || item.Change == "" {
			return fmt.Errorf("revision impact item %d requires artifact_id, artifact_kind, and change", index)
		}
		if item.Requirement != "" && item.Requirement != StructureImpactRequired && item.Requirement != StructureImpactRecommended {
			return fmt.Errorf("revision impact item %d has invalid requirement %q", index, item.Requirement)
		}
		if item.Cause != "" {
			switch item.Cause {
			case StructureImpactContentDependency, StructureImpactStructureChange, StructureImpactDisplayRenumber:
			default:
				return fmt.Errorf("revision impact item %d has invalid cause %q", index, item.Cause)
			}
		}
		if item.Cause == StructureImpactDisplayRenumber && item.RequiresBodyRewrite {
			return fmt.Errorf("revision impact item %d cannot rewrite body content for display renumbering", index)
		}
		sources := make(map[string]struct{}, len(item.DependencySourceIDs))
		for _, sourceID := range item.DependencySourceIDs {
			sourceID = strings.TrimSpace(sourceID)
			if sourceID == "" {
				return fmt.Errorf("revision impact item %d contains an empty dependency source ID", index)
			}
			if _, duplicate := sources[sourceID]; duplicate {
				return fmt.Errorf("revision impact item %d contains duplicate dependency source ID %q", index, sourceID)
			}
			sources[sourceID] = struct{}{}
		}
		if _, exists := seen[item.ArtifactID]; exists {
			return fmt.Errorf("revision impact contains duplicate artifact %q", item.ArtifactID)
		}
		seen[item.ArtifactID] = struct{}{}
	}
	if strings.TrimSpace(i.Signature) != "" {
		payload, err := json.Marshal(struct {
			Summary string               `json:"summary"`
			Items   []RevisionImpactItem `json:"items"`
		}{Summary: strings.TrimSpace(i.Summary), Items: i.Items})
		if err != nil {
			return err
		}
		if i.Signature != ContentSignature(payload) {
			return fmt.Errorf("revision impact signature mismatch")
		}
	}
	return nil
}

type ArtifactVersion struct {
	ID               string          `json:"id"`
	ArtifactID       string          `json:"artifact_id"`
	ArtifactKind     string          `json:"artifact_kind"`
	RevisionID       string          `json:"revision_id,omitempty"`
	ParentVersionID  string          `json:"parent_version_id,omitempty"`
	Sequence         int             `json:"sequence"`
	Round            int             `json:"round"`
	Payload          json.RawMessage `json:"payload"`
	ContentSignature string          `json:"content_signature"`
	CreatedAt        string          `json:"created_at"`
}

func (v ArtifactVersion) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.ArtifactID) == "" || strings.TrimSpace(v.ArtifactKind) == "" {
		return fmt.Errorf("artifact version requires id, artifact_id, and artifact_kind")
	}
	if v.Sequence <= 0 || v.Round <= 0 {
		return fmt.Errorf("artifact version sequence and round must be positive")
	}
	if len(v.Payload) == 0 || !json.Valid(v.Payload) {
		return fmt.Errorf("artifact version payload must be valid JSON")
	}
	if v.ContentSignature != JSONContentSignature(v.Payload) {
		return fmt.Errorf("artifact version content signature mismatch")
	}
	return nil
}

type RevisionApprovalStage struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type RevisionApproval struct {
	StageID    string `json:"stage_id"`
	ApprovedAt string `json:"approved_at"`
}

type RevisionFeedback struct {
	Round     int    `json:"round"`
	StageID   string `json:"stage_id,omitempty"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

type RevisionAudit struct {
	Round              int    `json:"round"`
	CandidateSignature string `json:"candidate_signature"`
	Scope              string `json:"scope,omitempty"`
	ScopeID            string `json:"scope_id,omitempty"`
	FromChapter        int    `json:"from_chapter,omitempty"`
	ToChapter          int    `json:"to_chapter,omitempty"`
	ContentSignature   string `json:"content_signature,omitempty"`
	Passed             bool   `json:"passed"`
	Report             string `json:"report,omitempty"`
	CreatedAt          string `json:"created_at"`
}

// RevisionAuditEvidence is one independently signed audit in the complete
// quality set required for a candidate. Chapter identity deliberately carries
// both its stable ID and current display range so renumbering cannot make an
// old chapter audit appear current.
type RevisionAuditEvidence struct {
	Scope            string `json:"scope"`
	ScopeID          string `json:"scope_id"`
	FromChapter      int    `json:"from_chapter,omitempty"`
	ToChapter        int    `json:"to_chapter,omitempty"`
	ContentSignature string `json:"content_signature"`
	Passed           bool   `json:"passed"`
	Report           string `json:"report,omitempty"`
}

func (e RevisionAuditEvidence) Validate() error {
	if strings.TrimSpace(e.Scope) == "" || strings.TrimSpace(e.ScopeID) == "" ||
		strings.TrimSpace(e.ContentSignature) == "" {
		return fmt.Errorf("revision audit evidence requires scope, scope_id, and content signature")
	}
	if e.Scope == "chapter" && (e.FromChapter <= 0 || e.ToChapter != e.FromChapter) {
		return fmt.Errorf("chapter audit %q must identify exactly one current chapter number", e.ScopeID)
	}
	return nil
}

// RevisionAuditExpectation is the exact scope-local signature that must be
// returned by an auditor. Persisting the complete set with the candidate makes
// missing, invented, or whole-candidate-substituted scopes impossible.
type RevisionAuditExpectation struct {
	Scope            string `json:"scope"`
	ScopeID          string `json:"scope_id"`
	FromChapter      int    `json:"from_chapter,omitempty"`
	ToChapter        int    `json:"to_chapter,omitempty"`
	ContentSignature string `json:"content_signature"`
}

func (e RevisionAuditExpectation) Validate() error {
	return (RevisionAuditEvidence{
		Scope: e.Scope, ScopeID: e.ScopeID, FromChapter: e.FromChapter,
		ToChapter: e.ToChapter, ContentSignature: e.ContentSignature, Passed: true,
	}).Validate()
}

type RevisionRoute struct {
	Agent      string `json:"agent"`
	Task       string `json:"task"`
	Reason     string `json:"reason,omitempty"`
	SessionID  string `json:"session_id"`
	Revision   int    `json:"revision"`
	Generation uint64 `json:"generation"`
}

func (r RevisionRoute) Validate() error {
	if strings.TrimSpace(r.Agent) == "" || strings.TrimSpace(r.Task) == "" {
		return fmt.Errorf("revision route requires agent and task")
	}
	if strings.TrimSpace(r.SessionID) == "" || r.Revision <= 0 || r.Generation == 0 {
		return fmt.Errorf("revision route requires a session, revision, and generation fence")
	}
	return nil
}

type RevisionSession struct {
	Version             int                        `json:"version"`
	ID                  string                     `json:"id"`
	Mode                RevisionMode               `json:"mode"`
	Stage               RevisionStage              `json:"stage"`
	ResumeStage         RevisionStage              `json:"resume_stage,omitempty"`
	Revision            int                        `json:"revision"`
	Generation          uint64                     `json:"generation"`
	PolicyID            string                     `json:"policy_id"`
	PolicyVersion       string                     `json:"policy_version"`
	Intent              string                     `json:"intent"`
	Impact              RevisionImpact             `json:"impact"`
	PreviewSignature    string                     `json:"preview_signature,omitempty"`
	ApprovalStages      []RevisionApprovalStage    `json:"approval_stages"`
	Approvals           []RevisionApproval         `json:"approvals,omitempty"`
	AcceptedVersionIDs  []string                   `json:"accepted_version_ids,omitempty"`
	CandidateVersionIDs []string                   `json:"candidate_version_ids,omitempty"`
	CandidateSignature  string                     `json:"candidate_signature,omitempty"`
	AuditExpectations   []RevisionAuditExpectation `json:"audit_expectations,omitempty"`
	Round               int                        `json:"round"`
	Audits              []RevisionAudit            `json:"audits,omitempty"`
	Feedback            []RevisionFeedback         `json:"feedback,omitempty"`
	Route               *RevisionRoute             `json:"route,omitempty"`
	RestoresVersionID   string                     `json:"restores_version_id,omitempty"`
	LastError           string                     `json:"last_error,omitempty"`
	CreatedAt           string                     `json:"created_at"`
	UpdatedAt           string                     `json:"updated_at"`
	CompletedAt         string                     `json:"completed_at,omitempty"`
}

func (s RevisionSession) Active() bool {
	return !s.Stage.Terminal()
}

func (s RevisionSession) CurrentApprovalStage() *RevisionApprovalStage {
	if s.Stage != RevisionStageApprovalPending || len(s.Approvals) >= len(s.ApprovalStages) {
		return nil
	}
	stage := s.ApprovalStages[len(s.Approvals)]
	return &stage
}

func (s RevisionSession) CurrentApprovalStageID() string {
	stage := s.CurrentApprovalStage()
	if stage == nil {
		return ""
	}
	return stage.ID
}

func (s RevisionSession) LatestAuditPassed() bool {
	if len(s.Audits) == 0 {
		return false
	}
	audit := s.Audits[len(s.Audits)-1]
	return audit.Round == s.Round && audit.Passed && audit.CandidateSignature == s.CandidateSignature
}

func (s RevisionSession) Validate() error {
	if s.Version != RevisionSchemaVersion || strings.TrimSpace(s.ID) == "" || strings.TrimSpace(string(s.Mode)) == "" {
		return fmt.Errorf("invalid revision session identity")
	}
	if !s.Stage.Valid() || s.Revision <= 0 || s.Generation == 0 || s.Round <= 0 || strings.TrimSpace(s.Intent) == "" {
		return fmt.Errorf("invalid revision session state")
	}
	if strings.TrimSpace(s.PolicyID) == "" || strings.TrimSpace(s.PolicyVersion) == "" {
		return fmt.Errorf("revision policy identity and version are required")
	}
	if err := s.Impact.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.Impact.Signature) == "" {
		return fmt.Errorf("revision impact signature is required")
	}
	if len(s.ApprovalStages) == 0 {
		return fmt.Errorf("revision approval stages must not be empty")
	}
	seen := make(map[string]struct{}, len(s.ApprovalStages))
	for _, stage := range s.ApprovalStages {
		stage.ID = strings.TrimSpace(stage.ID)
		if stage.ID == "" || strings.TrimSpace(stage.Label) == "" {
			return fmt.Errorf("revision approval stage requires id and label")
		}
		if _, exists := seen[stage.ID]; exists {
			return fmt.Errorf("duplicate revision approval stage %q", stage.ID)
		}
		seen[stage.ID] = struct{}{}
	}
	if len(s.Approvals) > len(s.ApprovalStages) {
		return fmt.Errorf("revision approvals exceed configured stages")
	}
	expectations := make(map[string]struct{}, len(s.AuditExpectations))
	for _, expectation := range s.AuditExpectations {
		if err := expectation.Validate(); err != nil {
			return err
		}
		key := expectation.Scope + "\x00" + expectation.ScopeID
		if _, duplicate := expectations[key]; duplicate {
			return fmt.Errorf("duplicate revision audit expectation %s/%s", expectation.Scope, expectation.ScopeID)
		}
		expectations[key] = struct{}{}
	}
	accepted := make(map[string]struct{}, len(s.AcceptedVersionIDs))
	for _, versionID := range s.AcceptedVersionIDs {
		if strings.TrimSpace(versionID) == "" {
			return fmt.Errorf("accepted revision version ID is required")
		}
		if _, duplicate := accepted[versionID]; duplicate {
			return fmt.Errorf("duplicate accepted revision version %q", versionID)
		}
		accepted[versionID] = struct{}{}
	}
	for index, approval := range s.Approvals {
		if approval.StageID != s.ApprovalStages[index].ID {
			return fmt.Errorf("revision approvals are out of order")
		}
		if strings.TrimSpace(approval.ApprovedAt) == "" {
			return fmt.Errorf("revision approval timestamp is required")
		}
	}
	for _, audit := range s.Audits {
		if audit.Round <= 0 || strings.TrimSpace(audit.CandidateSignature) == "" || strings.TrimSpace(audit.CreatedAt) == "" {
			return fmt.Errorf("revision audit is incomplete")
		}
		if audit.Round > s.Round {
			return fmt.Errorf("revision audit round is ahead of the session")
		}
	}
	for _, feedback := range s.Feedback {
		if feedback.Round <= 0 || feedback.Round >= s.Round || strings.TrimSpace(feedback.Message) == "" || strings.TrimSpace(feedback.CreatedAt) == "" {
			return fmt.Errorf("revision feedback is incomplete or out of round order")
		}
	}
	effectiveStage := s.Stage
	if s.Stage == RevisionStagePaused || s.Stage == RevisionStageFailed {
		if !s.ResumeStage.Valid() || s.ResumeStage.Terminal() || s.ResumeStage == RevisionStagePaused || s.ResumeStage == RevisionStageFailed {
			return fmt.Errorf("revision resume stage is invalid")
		}
		effectiveStage = s.ResumeStage
	} else if s.ResumeStage != "" {
		return fmt.Errorf("revision resume stage is only valid while interrupted")
	}
	hasCandidates := len(s.CandidateVersionIDs) > 0 && strings.TrimSpace(s.CandidateSignature) != ""
	switch effectiveStage {
	case RevisionStageCandidateAudit, RevisionStageApprovalPending, RevisionStageReadyToPublish, RevisionStageCompleted:
		if !hasCandidates {
			return fmt.Errorf("revision stage %q requires a non-empty candidate", effectiveStage)
		}
	}
	if effectiveStage == RevisionStageApprovalPending || effectiveStage == RevisionStageReadyToPublish || effectiveStage == RevisionStageCompleted {
		if !s.LatestAuditPassed() {
			return fmt.Errorf("revision stage %q requires a passing current-round audit", effectiveStage)
		}
	}
	if effectiveStage == RevisionStageReadyToPublish || effectiveStage == RevisionStageCompleted {
		if len(s.Approvals) != len(s.ApprovalStages) {
			return fmt.Errorf("revision stage %q requires all ordered approvals", effectiveStage)
		}
	}
	if s.Route != nil {
		if effectiveStage != RevisionStageCandidateGenerating && effectiveStage != RevisionStageCandidateAudit {
			return fmt.Errorf("revision route is not allowed at stage %q", effectiveStage)
		}
		if err := s.Route.Validate(); err != nil {
			return err
		}
		if s.Route.SessionID != s.ID || s.Route.Revision != s.Revision || s.Route.Generation != s.Generation {
			return fmt.Errorf("revision route fence does not match its session")
		}
	}
	return nil
}

// RevisionPolicy supplies mode-specific validation and routing without putting
// original-fiction or adaptation semantics into the shared revision engine.
type RevisionPolicy interface {
	Mode() RevisionMode
	Identity() (id string, version string)
	ApprovalStages(RevisionImpact) ([]RevisionApprovalStage, error)
	ValidateImpact(RevisionImpact) error
	ValidateCandidate(RevisionSession, []ArtifactVersion) error
	Route(RevisionSession) (*RevisionRoute, error)
}

// StagedRevisionPolicy is an optional extension for policies whose later
// candidates must not be generated before an earlier human approval. Policies
// that do not implement it retain the single-candidate RevisionPolicy flow.
type StagedRevisionPolicy interface {
	RevisionPolicy
	ContinueAfterApproval(RevisionSession, RevisionApprovalStage) bool
}

// SignedAuditSetPolicy is implemented by policies that require more than one
// boolean audit decision before a human approval can open.
type SignedAuditSetPolicy interface {
	RevisionPolicy
	ValidateAuditSet(RevisionSession, []RevisionAuditEvidence) error
}

// ScopedAuditPolicy derives the exact scope-local audit contract from the
// immutable candidate versions accepted by RevisionStore.
type ScopedAuditPolicy interface {
	RevisionPolicy
	AuditExpectations(RevisionSession, []ArtifactVersion) ([]RevisionAuditExpectation, error)
}

func ContentSignature(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func JSONContentSignature(content []byte) string {
	var canonical bytes.Buffer
	if err := json.Compact(&canonical, content); err != nil {
		return ContentSignature(content)
	}
	return ContentSignature(canonical.Bytes())
}

func CandidateSignature(versions []ArtifactVersion) string {
	type signedVersion struct {
		ArtifactID string `json:"artifact_id"`
		VersionID  string `json:"version_id"`
		Signature  string `json:"signature"`
	}
	signed := make([]signedVersion, 0, len(versions))
	for _, version := range versions {
		signed = append(signed, signedVersion{version.ArtifactID, version.ID, version.ContentSignature})
	}
	payload, _ := json.Marshal(signed)
	return ContentSignature(payload)
}

func RevisionTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
