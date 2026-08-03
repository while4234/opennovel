package domain

import (
	"fmt"
	"strings"
)

const AdaptationFoundationReviewVersion = 1

const (
	AdaptationFoundationReviewGenerating = "generating"
	AdaptationFoundationReviewPending    = "pending"
	AdaptationFoundationReviewApproved   = "approved"
	AdaptationFoundationReviewReadonly   = "readonly"
)

// AdaptationFoundationBinding is the immutable upstream fence shared by
// adaptation skeletons, proposals, detail audits, and confirmed plans.
type AdaptationFoundationBinding struct {
	SourceSignature                string `json:"source_signature"`
	TargetFoundationAuditSignature string `json:"target_foundation_audit_signature"`
	CoreCastSignature              string `json:"core_cast_signature"`
	AdaptationIntentHash           string `json:"adaptation_intent_hash"`
	WorkflowRevision               int    `json:"workflow_revision"`
}

func (b AdaptationFoundationBinding) Validate() error {
	if strings.TrimSpace(b.SourceSignature) == "" {
		return fmt.Errorf("adaptation foundation binding source signature is required")
	}
	if strings.TrimSpace(b.TargetFoundationAuditSignature) == "" {
		return fmt.Errorf("adaptation foundation binding target audit signature is required")
	}
	if strings.TrimSpace(b.CoreCastSignature) == "" {
		return fmt.Errorf("adaptation foundation binding core cast signature is required")
	}
	if strings.TrimSpace(b.AdaptationIntentHash) == "" {
		return fmt.Errorf("adaptation foundation binding intent hash is required")
	}
	if b.WorkflowRevision <= 0 {
		return fmt.Errorf("adaptation foundation binding workflow revision must be > 0")
	}
	return nil
}

// AdaptationFoundationReview is the explicit human checkpoint between the
// immutable source evidence and adaptation planning artifacts.
type AdaptationFoundationReview struct {
	Version            int                         `json:"version"`
	State              string                      `json:"state"`
	FoundationRevision int64                       `json:"foundation_revision"`
	Binding            AdaptationFoundationBinding `json:"binding"`
	Generation         int64                       `json:"generation"`
	Brief              string                      `json:"brief"`
	Feedback           string                      `json:"feedback,omitempty"`
	ConfirmedAt        string                      `json:"confirmed_at,omitempty"`
	ReadonlyReason     string                      `json:"readonly_reason,omitempty"`
	BlockingReasons    []string                    `json:"blocking_reasons,omitempty"`
	UpdatedAt          string                      `json:"updated_at"`
}

func (r AdaptationFoundationReview) Validate() error {
	if r.Version != AdaptationFoundationReviewVersion {
		return fmt.Errorf("unsupported adaptation foundation review version %d", r.Version)
	}
	switch r.State {
	case AdaptationFoundationReviewGenerating, AdaptationFoundationReviewPending,
		AdaptationFoundationReviewApproved, AdaptationFoundationReviewReadonly:
	default:
		return fmt.Errorf("invalid adaptation foundation review state %q", r.State)
	}
	if r.FoundationRevision <= 0 || r.Generation <= 0 {
		return fmt.Errorf("adaptation foundation review revision and generation must be > 0")
	}
	if strings.TrimSpace(r.Brief) == "" {
		return fmt.Errorf("adaptation foundation review brief is required")
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if r.State == AdaptationFoundationReviewApproved && strings.TrimSpace(r.ConfirmedAt) == "" {
		return fmt.Errorf("approved adaptation foundation review requires confirmation time")
	}
	if r.State == AdaptationFoundationReviewReadonly && strings.TrimSpace(r.ReadonlyReason) == "" {
		return fmt.Errorf("readonly adaptation foundation review requires a reason")
	}
	return nil
}
