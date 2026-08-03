package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestContinuationStorePersistsSnapshotAndRejectsStaleRevision(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	snapshot, err := st.Continuation.InitializeSource("sha256:source", 20)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if snapshot.Workflow.Revision != 1 || snapshot.Workflow.Stage != domain.ContinuationStageSourceReady {
		t.Fatalf("unexpected initial workflow: %+v", snapshot.Workflow)
	}

	snapshot, err = st.Continuation.Update(1, func(snapshot *domain.ContinuationSnapshot) error {
		return snapshot.Workflow.Transition(domain.ContinuationStageDraftCollecting)
	})
	if err != nil {
		t.Fatalf("begin draft: %v", err)
	}
	if snapshot.Workflow.Revision != 2 {
		t.Fatalf("revision = %d, want 2", snapshot.Workflow.Revision)
	}

	_, err = st.Continuation.Update(1, func(snapshot *domain.ContinuationSnapshot) error { return nil })
	var conflict *ContinuationRevisionConflictError
	if !errors.As(err, &conflict) || conflict.Actual != 2 {
		t.Fatalf("expected revision conflict at 2, got %v", err)
	}

	reopened := NewStore(root)
	loaded, err := reopened.Continuation.LoadSnapshot()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.Workflow.SourceSignature != "sha256:source" || loaded.Workflow.BaseChapterCount != 20 || loaded.Workflow.Revision != 2 {
		t.Fatalf("unexpected reloaded workflow: %+v", loaded.Workflow)
	}
}

func TestCommitContinuationPlanPreservesImportedBaselineAndAppendsNPlusOne(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	baseOutline := []domain.OutlineEntry{
		{Chapter: 1, Title: "one", CoreEvent: "source one"},
		{Chapter: 2, Title: "two", CoreEvent: "source two"},
		{Chapter: 3, Title: "three", CoreEvent: "source three"},
	}
	baseVolumes := []domain.VolumeOutline{{
		Index: 1, Title: "source", Theme: "source theme",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "source arc", Chapters: baseOutline}},
	}}
	if err := st.Outline.SaveOutline(baseOutline); err != nil {
		t.Fatalf("save outline: %v", err)
	}
	if err := st.Outline.SaveLayeredOutline(baseVolumes); err != nil {
		t.Fatalf("save layered outline: %v", err)
	}
	baseOutline, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("load identity-aware baseline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName: "imported", TotalChapters: 3, CurrentChapter: 4,
		CompletedChapters: []int{1, 2, 3}, Layered: true,
	}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	if _, err := st.Continuation.InitializeSource("source-signature", 3); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	snapshot := advanceContinuationToOutlineReview(t, st.Continuation)
	canonicalBefore, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("load pre-approval outline: %v", err)
	}
	if !reflect.DeepEqual(canonicalBefore, baseOutline) {
		t.Fatalf("candidate planning changed canonical outline before approval: %+v", canonicalBefore)
	}

	snapshot, err = st.CommitContinuationPlan(snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("commit continuation: %v", err)
	}
	if snapshot.Workflow.Stage != domain.ContinuationStageReadyToWrite || snapshot.Plan == nil {
		t.Fatalf("unexpected committed snapshot: %+v", snapshot)
	}
	canonicalAfter, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("load committed outline: %v", err)
	}
	if !reflect.DeepEqual(canonicalAfter[:3], baseOutline) {
		t.Fatalf("imported baseline changed: got %+v want %+v", canonicalAfter[:3], baseOutline)
	}
	if len(canonicalAfter) != 5 || canonicalAfter[3].Chapter != 4 || canonicalAfter[4].Chapter != 5 {
		t.Fatalf("continuation chapters were not appended as N+1: %+v", canonicalAfter)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("load progress: %v", err)
	}
	if progress.TotalChapters != 5 || !progress.Layered {
		t.Fatalf("unexpected committed progress: %+v", progress)
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("load layered outline: %v", err)
	}
	if len(volumes) != 2 || volumes[1].Index != 2 || volumes[1].Arcs[0].Chapters[0].Chapter != 4 {
		t.Fatalf("continuation volume not appended: %+v", volumes)
	}
}

func advanceContinuationToOutlineReview(t *testing.T, continuation *ContinuationStore) *domain.ContinuationSnapshot {
	t.Helper()
	snapshot, err := continuation.Update(1, func(snapshot *domain.ContinuationSnapshot) error {
		return snapshot.Workflow.Transition(domain.ContinuationStageDraftCollecting)
	})
	if err != nil {
		t.Fatalf("begin draft: %v", err)
	}
	snapshot, err = continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		snapshot.Workflow.Draft = "approved draft"
		return snapshot.Workflow.Transition(domain.ContinuationStageProposalGenerating)
	})
	if err != nil {
		t.Fatalf("commit draft: %v", err)
	}
	snapshot, err = continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		snapshot.Proposal = &domain.ContinuationProposal{
			Summary: "finish", Direction: "resolve", TargetChapterCount: 2, Structure: domain.ContinuationStructureSingle,
		}
		return snapshot.Workflow.Transition(domain.ContinuationStageProposalReviewPending)
	})
	if err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	snapshot, err = continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		return snapshot.Workflow.Transition(domain.ContinuationStageOutlineGenerating)
	})
	if err != nil {
		t.Fatalf("begin outlines: %v", err)
	}
	snapshot, err = continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		snapshot.Outlines = &domain.ContinuationOutline{
			Structure: domain.ContinuationStructureSingle,
			Chapters: []domain.OutlineEntry{
				{Chapter: 4, Title: "four", CoreEvent: "continuation four"},
				{Chapter: 5, Title: "five", CoreEvent: "continuation five"},
			},
		}
		return snapshot.Workflow.Transition(domain.ContinuationStageOutlineReviewPending)
	})
	if err != nil {
		t.Fatalf("save outlines: %v", err)
	}
	return snapshot
}

func TestContinuationStoreProposalRevisionCanInvalidateDownstreamCandidates(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := st.Continuation.InitializeSource("source", 3); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	snapshot, err := st.Continuation.Update(1, func(snapshot *domain.ContinuationSnapshot) error {
		return snapshot.Workflow.Transition(domain.ContinuationStageDraftCollecting)
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	snapshot, err = st.Continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		snapshot.Workflow.Draft = "draft"
		return snapshot.Workflow.Transition(domain.ContinuationStageProposalGenerating)
	})
	if err != nil {
		t.Fatalf("commit draft: %v", err)
	}
	snapshot, err = st.Continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		snapshot.Proposal = &domain.ContinuationProposal{
			Summary: "proposal", Direction: "direction", TargetChapterCount: 1, Structure: domain.ContinuationStructureSingle,
		}
		return snapshot.Workflow.Transition(domain.ContinuationStageProposalReviewPending)
	})
	if err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	snapshot, err = st.Continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		return snapshot.Workflow.Transition(domain.ContinuationStageOutlineGenerating)
	})
	if err != nil {
		t.Fatalf("begin outline: %v", err)
	}
	snapshot, err = st.Continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		snapshot.Outlines = &domain.ContinuationOutline{
			Structure: domain.ContinuationStructureSingle,
			Chapters:  []domain.OutlineEntry{{Chapter: 4, Title: "next", CoreEvent: "continue"}},
		}
		return snapshot.Workflow.Transition(domain.ContinuationStageOutlineReviewPending)
	})
	if err != nil {
		t.Fatalf("save outline: %v", err)
	}

	reopened := NewStore(st.Dir())
	snapshot, err = reopened.Continuation.LoadSnapshot()
	if err != nil || snapshot.Proposal == nil || snapshot.Outlines == nil {
		t.Fatalf("candidate artifacts did not persist: snapshot=%+v err=%v", snapshot, err)
	}
	snapshot, err = reopened.Continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		return snapshot.Workflow.Transition(domain.ContinuationStageProposalGenerating)
	})
	if err != nil {
		t.Fatalf("begin proposal revision: %v", err)
	}
	if _, err := reopened.Continuation.Update(snapshot.Workflow.Revision, func(snapshot *domain.ContinuationSnapshot) error {
		snapshot.Proposal = &domain.ContinuationProposal{
			Summary: "revised", Direction: "new direction", TargetChapterCount: 1, Structure: domain.ContinuationStructureSingle,
		}
		snapshot.Volumes = nil
		snapshot.Outlines = nil
		snapshot.Plan = nil
		return snapshot.Workflow.Transition(domain.ContinuationStageProposalReviewPending)
	}); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	loaded, err := reopened.Continuation.LoadSnapshot()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Proposal == nil || loaded.Proposal.Summary != "revised" {
		t.Fatalf("revised proposal was not persisted: %+v", loaded.Proposal)
	}
	if len(loaded.Volumes) != 0 || loaded.Outlines != nil || loaded.Plan != nil {
		t.Fatalf("downstream candidates were not cleared: %+v", loaded)
	}
}
