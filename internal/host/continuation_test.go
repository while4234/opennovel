package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestContinuationImportFinalizationCreatesReviewGate(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    4,
		TotalChapters:     3,
		CompletedChapters: []int{1, 2, 3},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	h := &Host{store: st}
	source := make(chan imp.Event, 1)
	source <- imp.Event{Time: time.Now(), Stage: imp.StageDone, Current: 3, Total: 3, Message: "done"}
	close(source)

	events := h.withContinuationImportFinalization(context.Background(), "source-sha", source)
	event := <-events
	if event.Stage != imp.StageDone || event.Err != nil {
		t.Fatalf("event = %+v", event)
	}
	snapshot, err := st.Continuation.LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if snapshot.Workflow.Stage != domain.ContinuationStageSourceReady || snapshot.Workflow.BaseChapterCount != 3 {
		t.Fatalf("workflow = %+v", snapshot.Workflow)
	}
	if !strings.Contains(event.Message, "等待确认 Draft") {
		t.Fatalf("message = %q", event.Message)
	}
}

func TestContinuationReviewGateBlocksResumeAndStoppedContinue(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	snapshot, err := st.Continuation.InitializeSource("source-sha", 12)
	if err != nil {
		t.Fatalf("InitializeSource: %v", err)
	}
	if _, err := st.Continuation.Update(snapshot.Workflow.Revision, func(next *domain.ContinuationSnapshot) error {
		next.Workflow.Stage = domain.ContinuationStageDraftCollecting
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	h := &Host{store: st}

	if _, err := h.Resume(); err == nil || !strings.Contains(err.Error(), "Draft") {
		t.Fatalf("Resume error = %v, want Draft gate", err)
	}
	if err := h.Continue("继续"); err == nil || !strings.Contains(err.Error(), "Draft") {
		t.Fatalf("Continue error = %v, want Draft gate", err)
	}
}
