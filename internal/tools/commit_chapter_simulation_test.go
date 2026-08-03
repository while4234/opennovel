package tools

import (
	"context"
	"errors"
	"testing"
)

type recordingSimulationGate struct {
	chapter int
	content string
	err     error
}

func (g *recordingSimulationGate) EnsureCurrent(_ context.Context, chapter int, content string) error {
	g.chapter, g.content = chapter, content
	return g.err
}

func TestCommitChapterSimulationGateReceivesExactDraft(t *testing.T) {
	wantErr := errors.New("simulation blocked")
	gate := &recordingSimulationGate{err: wantErr}
	tool := &CommitChapterTool{simulationGate: gate}
	err := tool.ensureSimulationGate(context.Background(), 7, "exact final draft")
	if !errors.Is(err, wantErr) {
		t.Fatalf("ensureSimulationGate error = %v, want %v", err, wantErr)
	}
	if gate.chapter != 7 || gate.content != "exact final draft" {
		t.Fatalf("gate binding = chapter %d content %q", gate.chapter, gate.content)
	}
}
