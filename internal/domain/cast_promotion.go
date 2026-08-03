package domain

// CastPromotionStatus is a durable, restart-safe incremental Character Agent
// workflow. The same agent performs analyze and an independent review run.
type CastPromotionStatus string

const (
	CastPromotionPending           CastPromotionStatus = "pending"
	CastPromotionCandidateReady    CastPromotionStatus = "candidate_ready"
	CastPromotionReviewPassed      CastPromotionStatus = "review_passed"
	CastPromotionReviewNeedsChange CastPromotionStatus = "review_needs_revision"
	CastPromotionConfirmed         CastPromotionStatus = "confirmed"
)

type CastPromotionWorkflow struct {
	Version                int                          `json:"version"`
	LedgerName             string                       `json:"ledger_name"`
	Status                 CastPromotionStatus          `json:"status"`
	BaseFoundationRevision int64                        `json:"base_foundation_revision"`
	AnalyzeRunID           string                       `json:"analyze_run_id,omitempty"`
	AnalyzeIdempotencyKey  string                       `json:"analyze_idempotency_key,omitempty"`
	Candidate              *Character                   `json:"candidate,omitempty"`
	Relationships          []CharacterRelationship      `json:"relationships,omitempty"`
	CandidateDigest        string                       `json:"candidate_digest,omitempty"`
	ReviewRunID            string                       `json:"review_run_id,omitempty"`
	ReviewIdempotencyKey   string                       `json:"review_idempotency_key,omitempty"`
	Findings               []CharacterCardReviewFinding `json:"findings,omitempty"`
	ConfirmationKey        string                       `json:"confirmation_key,omitempty"`
}
