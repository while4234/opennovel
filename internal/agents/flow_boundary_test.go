package agents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/voocel/agentcore"
)

func TestFlowBoundaryMiddlewareRecomputesRouteAfterSubagentFailure(t *testing.T) {
	calls := 0
	middleware := flowBoundaryMiddleware(func(toolName string) {
		if toolName != "subagent" {
			t.Fatalf("boundary tool = %q", toolName)
		}
		calls++
	})
	wantErr := errors.New("writer stopped after durable checkpoint")
	_, err := middleware(
		context.Background(),
		agentcore.ToolCall{Name: "subagent"},
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, wantErr
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("middleware error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("boundary hook calls = %d, want 1", calls)
	}
}

func TestFlowBoundaryMiddlewareDoesNotAdvanceFailedNonSubagentTool(t *testing.T) {
	calls := 0
	middleware := flowBoundaryMiddleware(func(string) { calls++ })
	_, _ = middleware(
		context.Background(),
		agentcore.ToolCall{Name: "reopen_book"},
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("reopen failed")
		},
	)
	if calls != 0 {
		t.Fatalf("failed non-subagent tool advanced boundary %d times", calls)
	}
}
