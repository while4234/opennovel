package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

type SaveCastPromotionCandidateTool struct{ store *store.Store }

func NewSaveCastPromotionCandidateTool(st *store.Store) *SaveCastPromotionCandidateTool {
	return &SaveCastPromotionCandidateTool{store: st}
}
func (t *SaveCastPromotionCandidateTool) Name() string { return "save_cast_promotion_candidate" }
func (t *SaveCastPromotionCandidateTool) Description() string {
	return "Character analyze only: stage one full reviewed-card-shaped candidate for the current pending CastEntry. Never publish StoryFoundation."
}
func (t *SaveCastPromotionCandidateTool) Label() string                        { return "stage cast promotion" }
func (t *SaveCastPromotionCandidateTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *SaveCastPromotionCandidateTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *SaveCastPromotionCandidateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("run_id", schema.String("unique analyze run ID")).Required(),
		schema.Property("idempotency_key", schema.String("exact retry key")).Required(),
		schema.Property("candidate", map[string]any{"type": "object", "description": "one complete canonical Character object"}).Required(),
		schema.Property("relationships", map[string]any{"type": "array", "description": "canonical relationships involving the promoted candidate", "items": map[string]any{"type": "object"}}).Required(),
	)
}
func (t *SaveCastPromotionCandidateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request struct {
		RunID          string                         `json:"run_id"`
		IdempotencyKey string                         `json:"idempotency_key"`
		Candidate      domain.Character               `json:"candidate"`
		Relationships  []domain.CharacterRelationship `json:"relationships"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return nil, err
	}
	workflow, err := t.store.Cast.SavePromotionCandidate(
		request.RunID, request.IdempotencyKey, request.Candidate, request.Relationships, foundation,
	)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"saved": true, "status": workflow.Status,
		"ledger_name": workflow.LedgerName, "candidate_digest": workflow.CandidateDigest,
		"next_mode": "review", "review_must_use_independent_run": true,
	})
}

type SaveCastPromotionReviewTool struct{ store *store.Store }

func NewSaveCastPromotionReviewTool(st *store.Store) *SaveCastPromotionReviewTool {
	return &SaveCastPromotionReviewTool{store: st}
}
func (t *SaveCastPromotionReviewTool) Name() string { return "save_cast_promotion_review" }
func (t *SaveCastPromotionReviewTool) Description() string {
	return "Character review only: independently review the staged cast promotion and save structured findings. Never modify or publish the candidate."
}
func (t *SaveCastPromotionReviewTool) Label() string                        { return "review cast promotion" }
func (t *SaveCastPromotionReviewTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *SaveCastPromotionReviewTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *SaveCastPromotionReviewTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("run_id", schema.String("unique review run ID")).Required(),
		schema.Property("idempotency_key", schema.String("exact retry key")).Required(),
		schema.Property("candidate_digest", schema.String("digest from analyze receipt")).Required(),
		schema.Property("findings", map[string]any{"type": "array", "description": "CharacterCardReviewFinding array; [] means pass", "items": map[string]any{"type": "object"}}).Required(),
	)
}
func (t *SaveCastPromotionReviewTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request struct {
		RunID           string                              `json:"run_id"`
		IdempotencyKey  string                              `json:"idempotency_key"`
		CandidateDigest string                              `json:"candidate_digest"`
		Findings        []domain.CharacterCardReviewFinding `json:"findings"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	workflow, err := t.store.Cast.SavePromotionReview(
		request.RunID, request.IdempotencyKey, request.CandidateDigest, request.Findings,
	)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"saved": true, "status": workflow.Status,
		"candidate_digest":           workflow.CandidateDigest,
		"requires_user_confirmation": workflow.Status == domain.CastPromotionReviewPassed,
	})
}
