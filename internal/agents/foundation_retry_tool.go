package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

const foundationRetryFreshContextStatus = "retry_in_fresh_context"

type foundationRetryBoundaryTool struct {
	inner agentcore.Tool
}

func newFoundationRetryBoundaryTool(inner agentcore.Tool) agentcore.Tool {
	return &foundationRetryBoundaryTool{inner: inner}
}

func (t *foundationRetryBoundaryTool) Name() string           { return t.inner.Name() }
func (t *foundationRetryBoundaryTool) Description() string    { return t.inner.Description() }
func (t *foundationRetryBoundaryTool) Schema() map[string]any { return t.inner.Schema() }

func (t *foundationRetryBoundaryTool) StrictSchema() bool {
	strict, ok := t.inner.(interface{ StrictSchema() bool })
	return ok && strict.StrictSchema()
}

func (t *foundationRetryBoundaryTool) ReadOnly(args json.RawMessage) bool {
	if readOnly, ok := t.inner.(agentcore.ReadOnlyTool); ok {
		return readOnly.ReadOnly(args)
	}
	return false
}

func (t *foundationRetryBoundaryTool) ConcurrencySafe(args json.RawMessage) bool {
	if safe, ok := t.inner.(agentcore.ConcurrencySafeTool); ok {
		return safe.ConcurrencySafe(args)
	}
	return false
}

func (t *foundationRetryBoundaryTool) Execute(
	ctx context.Context,
	args json.RawMessage,
) (json.RawMessage, error) {
	result, err := t.inner.Execute(ctx, args)
	if err == nil || !foundationGenerationNeedsFreshContext(args, err) {
		return result, err
	}

	var request struct {
		Type                   string `json:"type"`
		FoundationGeneration   int    `json:"foundation_generation"`
		FoundationBaseRevision int64  `json:"foundation_base_revision"`
	}
	_ = json.Unmarshal(args, &request)
	return json.Marshal(map[string]any{
		"type":                     strings.TrimSpace(request.Type),
		"status":                   foundationRetryFreshContextStatus,
		"saved":                    false,
		"retry_in_fresh_context":   true,
		"foundation_generation":    request.FoundationGeneration,
		"foundation_base_revision": request.FoundationBaseRevision,
		"error":                    err.Error(),
	})
}

func foundationGenerationNeedsFreshContext(args json.RawMessage, err error) bool {
	var request struct {
		FoundationGeneration int `json:"foundation_generation"`
	}
	if json.Unmarshal(args, &request) != nil ||
		request.FoundationGeneration <= 0 {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errs.ErrToolArgs) {
		return true
	}
	var reviewErr *store.FoundationReviewError
	return errors.As(err, &reviewErr)
}
