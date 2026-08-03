package agents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

type foundationRetryProbeTool struct {
	err error
}

func (t foundationRetryProbeTool) Name() string           { return "save_foundation" }
func (t foundationRetryProbeTool) Description() string    { return "probe" }
func (t foundationRetryProbeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t foundationRetryProbeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, t.err
}

func TestFoundationRetryBoundaryEndsMalformedGenerationRun(t *testing.T) {
	tool := newFoundationRetryBoundaryTool(foundationRetryProbeTool{
		err: errors.Join(errors.New("invalid world_rules shape"), errs.ErrToolArgs),
	})
	result, err := tool.Execute(t.Context(), json.RawMessage(
		`{"type":"world_rules","foundation_generation":2,"foundation_base_revision":7}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	var payload saveFoundationResult
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.RetryInFreshContext || payload.Type != "world_rules" {
		t.Fatalf("retry payload = %+v", payload)
	}
	if !architectLongShouldStopAfterToolResult("save_foundation", result) {
		t.Fatal("fresh-context retry did not end the current Architect run")
	}
}

func TestFoundationRetryBoundaryPreservesNonGenerationError(t *testing.T) {
	want := errors.Join(errors.New("invalid outline"), errs.ErrToolArgs)
	tool := newFoundationRetryBoundaryTool(foundationRetryProbeTool{err: want})
	_, err := tool.Execute(t.Context(), json.RawMessage(`{"type":"outline"}`))
	if !errors.Is(err, errs.ErrToolArgs) {
		t.Fatalf("non-generation error = %v", err)
	}
}

func TestFoundationRetryBoundaryPreservesCancellation(t *testing.T) {
	tool := newFoundationRetryBoundaryTool(foundationRetryProbeTool{err: context.Canceled})
	_, err := tool.Execute(t.Context(), json.RawMessage(
		`{"type":"world_rules","foundation_generation":2,"foundation_base_revision":7}`,
	))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestFoundationRetryBoundaryEndsStaleGenerationRun(t *testing.T) {
	tool := newFoundationRetryBoundaryTool(foundationRetryProbeTool{
		err: &store.FoundationReviewError{Code: store.FoundationReviewErrorStale},
	})
	result, err := tool.Execute(t.Context(), json.RawMessage(
		`{"type":"premise","foundation_generation":2,"foundation_base_revision":7}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	var payload saveFoundationResult
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.RetryInFreshContext {
		t.Fatalf("retry payload = %+v", payload)
	}
}

var _ agentcore.Tool = foundationRetryProbeTool{}
