package domain

import (
	"fmt"
	"strings"
)

const AdaptationRevisionRuntimeVersion = 1

// AdaptationRevisionRuntime is the adaptation-only durable command checkpoint.
// It binds every resumed command to the formal plan, immutable source manifest,
// sealed preview, and server-derived batch topology that existed at preview.
type AdaptationRevisionRuntime struct {
	Version                 int             `json:"version"`
	SessionID               string          `json:"session_id"`
	Stage                   ManuscriptStage `json:"stage"`
	BasePlanSignature       string          `json:"base_plan_signature"`
	SourceManifestSignature string          `json:"source_manifest_signature"`
	PreviewSignature        string          `json:"preview_signature"`
	BatchPlan               BatchPlan       `json:"batch_plan"`
	Paused                  bool            `json:"paused,omitempty"`
}

func (r AdaptationRevisionRuntime) Validate() error {
	if r.Version != AdaptationRevisionRuntimeVersion || strings.TrimSpace(r.SessionID) == "" ||
		!r.Stage.Valid() || strings.TrimSpace(r.BasePlanSignature) == "" ||
		strings.TrimSpace(r.SourceManifestSignature) == "" || strings.TrimSpace(r.PreviewSignature) == "" {
		return fmt.Errorf("adaptation revision runtime binding is incomplete")
	}
	if len(r.BatchPlan.Batches) == 0 {
		return fmt.Errorf("adaptation revision runtime BatchPlan is required")
	}
	return nil
}

type AdaptationRevisionBatchCommand string

const (
	AdaptationRevisionBatchStart         AdaptationRevisionBatchCommand = "start"
	AdaptationRevisionBatchGenerated     AdaptationRevisionBatchCommand = "generated"
	AdaptationRevisionBatchAuditPass     AdaptationRevisionBatchCommand = "audit_pass"
	AdaptationRevisionBatchAuditFail     AdaptationRevisionBatchCommand = "audit_fail"
	AdaptationRevisionBatchFail          AdaptationRevisionBatchCommand = "fail"
	AdaptationRevisionBatchResume        AdaptationRevisionBatchCommand = "resume"
	AdaptationRevisionVolumeReviewStart  AdaptationRevisionBatchCommand = "volume_review_start"
	AdaptationRevisionVolumeReviewPass   AdaptationRevisionBatchCommand = "volume_review_pass"
	AdaptationRevisionVolumeReviewFail   AdaptationRevisionBatchCommand = "volume_review_fail"
	AdaptationRevisionVolumeReviewResume AdaptationRevisionBatchCommand = "volume_review_resume"
	AdaptationRevisionGlobalReviewStart  AdaptationRevisionBatchCommand = "global_review_start"
	AdaptationRevisionGlobalReviewPass   AdaptationRevisionBatchCommand = "global_review_pass"
	AdaptationRevisionGlobalReviewFail   AdaptationRevisionBatchCommand = "global_review_fail"
	AdaptationRevisionGlobalReviewResume AdaptationRevisionBatchCommand = "global_review_resume"
)
