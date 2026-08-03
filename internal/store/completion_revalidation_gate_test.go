package store

import (
	"errors"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestCompletionRevalidationBlocksAcquireAndExistingFence(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	progress := &domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowReviewing, CompletionRevalidation: &domain.CompletionRevalidationCheckpoint{Version: completionRevalidationVersion, Status: "pending"}}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.AcquireNormalFlow("manual-continue"); !errors.Is(err, ErrCompletionRevalidationBlocksNormalFlow) {
		t.Fatalf("pending checkpoint acquire error = %v", err)
	}

	progress.CompletionRevalidation.Status = "completed"
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	lease, err := st.Revisions.AcquireNormalFlow("scheduled-resume")
	if err != nil {
		t.Fatal(err)
	}
	progress.CompletionRevalidation.Status = "pending"
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.FenceForNormalFlow(lease.Token); !errors.Is(err, ErrCompletionRevalidationBlocksNormalFlow) {
		t.Fatalf("checkpoint inserted before dispatch fence error = %v", err)
	}
	if err := st.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatal(err)
	}
}
