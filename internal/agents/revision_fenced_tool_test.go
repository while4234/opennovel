package agents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestRevisionFencedToolRejectsQueuedWriteAfterOwnershipRelease(t *testing.T) {
	revisions := store.NewRevisionStore(t.TempDir())
	lease, err := revisions.AcquireNormalFlow("queued-write-test")
	if err != nil {
		t.Fatal(err)
	}
	fence, err := revisions.FenceForNormalFlow(lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	inner := agentcore.NewFuncTool("write", "write", map[string]any{
		"type": "object",
	}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{"ok":true}`), nil
	})
	tool := revisionFenceWrites(revisions, inner)
	ctx := store.ContextWithRevisionFence(context.Background(), fence)
	if err := revisions.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("queued write crossed a released ownership fence")
	}
	if called {
		t.Fatal("stale queued write reached the writable tool")
	}
}

func TestRevisionFencedToolPreservesToolCapabilities(t *testing.T) {
	inner := capabilityTool{}
	wrapped := revisionFenceWrites(store.NewRevisionStore(t.TempDir()), inner)

	strict, ok := wrapped.(interface{ StrictSchema() bool })
	if !ok || !strict.StrictSchema() {
		t.Fatal("strict schema capability was not preserved")
	}
	readOnly, ok := wrapped.(agentcore.ReadOnlyTool)
	if !ok || !readOnly.ReadOnly(nil) {
		t.Fatal("read-only capability was not preserved")
	}
	safe, ok := wrapped.(agentcore.ConcurrencySafeTool)
	if !ok || !safe.ConcurrencySafe(nil) {
		t.Fatal("concurrency-safe capability was not preserved")
	}
}

type capabilityTool struct{}

func (capabilityTool) Name() string                         { return "capability" }
func (capabilityTool) Description() string                  { return "capability" }
func (capabilityTool) Schema() map[string]any               { return map[string]any{"type": "object"} }
func (capabilityTool) StrictSchema() bool                   { return true }
func (capabilityTool) ReadOnly(json.RawMessage) bool        { return true }
func (capabilityTool) ConcurrencySafe(json.RawMessage) bool { return true }
func (capabilityTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}
