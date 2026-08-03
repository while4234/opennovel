package store

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const expansionRuntimePath = ".ai/revisions/expansion-runtime.json"
const expansionAuditorTrustPath = ".ai/revisions/expansion-auditor-trust.json"

// ExpansionCommandReceipt is the durable idempotency boundary for every
// expansion mutation. Result IDs are intentionally small; authoritative
// previews and revisions remain in their own journals.
type ExpansionCommandReceipt struct {
	Operation   string `json:"operation"`
	Fingerprint string `json:"fingerprint"`
	PreviewID   string `json:"preview_id,omitempty"`
	RevisionID  string `json:"revision_id,omitempty"`
	// Result is the exact response observed when the durable mutation first
	// completed. Replays return this snapshot rather than today's active state.
	Result          json.RawMessage `json:"result,omitempty"`
	ResultSignature string          `json:"result_signature,omitempty"`
}

// ExpansionRuntime survives a host restart. The seal is persisted with the
// signed previews so a restart can revalidate, re-plan, or explicitly reject
// an obsolete preview instead of silently accepting it.
type ExpansionRuntime struct {
	Version                 int                                         `json:"version"`
	SealHex                 string                                      `json:"seal_hex"`
	KernelSealHex           string                                      `json:"kernel_seal_hex"`
	Previews                map[string]*domain.ExpansionPreview         `json:"previews"`
	Receipts                map[string]ExpansionCommandReceipt          `json:"receipts"`
	PendingAudits           map[string]json.RawMessage                  `json:"pending_audits,omitempty"`
	PendingDependencyAudits map[string]json.RawMessage                  `json:"pending_dependency_audits,omitempty"`
	AuditArtifacts          map[string]json.RawMessage                  `json:"audit_artifacts,omitempty"`
	DependencyReviews       map[string]domain.ExpansionDependencyReview `json:"dependency_reviews,omitempty"`
	DependencyReviewIndex   map[string]string                           `json:"dependency_review_index,omitempty"`
	PendingAdjustments      map[string]ExpansionAdjustmentTransaction   `json:"pending_adjustments,omitempty"`
}

// ExpansionAdjustmentTransaction is the durable prepare record that binds an
// expansion preview successor to the active revision rebind. The expansion
// planner can deterministically finish or roll back this record after a crash.
type ExpansionAdjustmentTransaction struct {
	OperationKey              string `json:"operation_key"`
	Fingerprint               string `json:"fingerprint"`
	SourcePreviewID           string `json:"source_preview_id"`
	NextPreviewID             string `json:"next_preview_id"`
	RevisionID                string `json:"revision_id"`
	PreviousRevisionSignature string `json:"previous_revision_signature"`
	NextRevisionSignature     string `json:"next_revision_signature"`
	RebindIdempotencyKey      string `json:"rebind_idempotency_key"`
}

func (s *Store) LoadExpansionRuntime() (ExpansionRuntime, error) {
	result := ExpansionRuntime{Version: 1, Previews: map[string]*domain.ExpansionPreview{}, Receipts: map[string]ExpansionCommandReceipt{}, PendingAudits: map[string]json.RawMessage{}, PendingDependencyAudits: map[string]json.RawMessage{}, AuditArtifacts: map[string]json.RawMessage{}, DependencyReviews: map[string]domain.ExpansionDependencyReview{}, DependencyReviewIndex: map[string]string{}, PendingAdjustments: map[string]ExpansionAdjustmentTransaction{}}
	if s == nil || s.Revisions == nil || s.Revisions.io == nil {
		return result, errors.New("expansion runtime store is unavailable")
	}
	err := s.Revisions.io.ReadJSON(expansionRuntimePath, &result)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if result.Version != 1 {
		return result, errors.New("unsupported expansion runtime version")
	}
	if result.Previews == nil {
		result.Previews = map[string]*domain.ExpansionPreview{}
	}
	if result.Receipts == nil {
		result.Receipts = map[string]ExpansionCommandReceipt{}
	}
	if result.PendingAudits == nil {
		result.PendingAudits = map[string]json.RawMessage{}
	}
	if result.PendingDependencyAudits == nil {
		result.PendingDependencyAudits = map[string]json.RawMessage{}
	}
	if result.AuditArtifacts == nil {
		result.AuditArtifacts = map[string]json.RawMessage{}
	}
	if result.DependencyReviews == nil {
		result.DependencyReviews = map[string]domain.ExpansionDependencyReview{}
	}
	if result.DependencyReviewIndex == nil {
		result.DependencyReviewIndex = map[string]string{}
	}
	// Migrate the pre-v2 cache shape in memory. Reviews are now retained by
	// immutable artifact signature; stage/scope is only a latest-result index.
	for key, review := range result.DependencyReviews {
		if review.ArtifactSignature == "" || key == review.ArtifactSignature {
			continue
		}
		result.DependencyReviews[review.ArtifactSignature] = review
		result.DependencyReviewIndex[key] = review.ArtifactSignature
		delete(result.DependencyReviews, key)
	}
	if result.PendingAdjustments == nil {
		result.PendingAdjustments = map[string]ExpansionAdjustmentTransaction{}
	}
	return result, nil
}

// SetExpansionWriteFaultForTesting injects failures at the real atomic store
// seam used by expansion runtime and revision state. It is intentionally a
// storage test seam, not a product or HTTP control.
func (s *Store) SetExpansionWriteFaultForTesting(fault func(rel, stage string) error) func() {
	if s == nil || s.Revisions == nil || s.Revisions.io == nil {
		return func() {}
	}
	s.Revisions.io.writeFault = fault
	if s.Adaptation != nil && s.Adaptation.io != nil {
		s.Adaptation.io.writeFault = fault
	}
	return func() {
		s.Revisions.io.writeFault = nil
		if s.Adaptation != nil && s.Adaptation.io != nil {
			s.Adaptation.io.writeFault = nil
		}
	}
}

// ExpansionAuditorTrust is the only auditor identity visible to the product
// process. The private key remains in the runner-owned identity file.
type ExpansionAuditorTrust struct {
	PublicKeyHex string `json:"public_key_hex"`
}

func (s *Store) LoadExpansionAuditorTrust() (ExpansionAuditorTrust, error) {
	var result ExpansionAuditorTrust
	if s == nil || s.Revisions == nil || s.Revisions.io == nil {
		return result, errors.New("expansion auditor trust store is unavailable")
	}
	return result, s.Revisions.io.ReadJSON(expansionAuditorTrustPath, &result)
}

func (s *Store) SaveExpansionAuditorTrust(trust ExpansionAuditorTrust) error {
	if s == nil || s.Revisions == nil || s.Revisions.io == nil {
		return errors.New("expansion auditor trust store is unavailable")
	}
	return s.Revisions.withRevisionTransaction(func() error {
		return s.Revisions.io.WriteJSON(expansionAuditorTrustPath, trust)
	})
}

func (s *Store) SaveExpansionRuntime(runtime ExpansionRuntime) error {
	if s == nil || s.Revisions == nil || s.Revisions.io == nil {
		return errors.New("expansion runtime store is unavailable")
	}
	runtime.Version = 1
	return s.Revisions.withRevisionTransaction(func() error {
		return s.Revisions.io.WriteJSON(expansionRuntimePath, runtime)
	})
}
