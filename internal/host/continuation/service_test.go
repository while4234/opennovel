package continuation

import (
	"context"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type fakeGenerator struct {
	proposal        domain.ContinuationProposal
	revisedProposal domain.ContinuationProposal
	volumes         []domain.VolumeOutline
	revisedVolumes  []domain.VolumeOutline
	outline         domain.ContinuationOutline
	revisedOutline  domain.ContinuationOutline
}

func (g *fakeGenerator) GenerateProposal(context.Context, ProposalInput) (domain.ContinuationProposal, error) {
	return g.proposal, nil
}

func (g *fakeGenerator) ReviseProposal(context.Context, ProposalRevisionInput) (domain.ContinuationProposal, error) {
	if g.revisedProposal.Summary != "" {
		return g.revisedProposal, nil
	}
	return g.proposal, nil
}

func (g *fakeGenerator) GenerateVolumes(context.Context, VolumeInput) ([]domain.VolumeOutline, error) {
	return g.volumes, nil
}

func (g *fakeGenerator) ReviseVolumes(context.Context, VolumeRevisionInput) ([]domain.VolumeOutline, error) {
	if len(g.revisedVolumes) > 0 {
		return g.revisedVolumes, nil
	}
	return g.volumes, nil
}

func (g *fakeGenerator) GenerateOutlines(context.Context, OutlineInput) (domain.ContinuationOutline, error) {
	return g.outline, nil
}

func (g *fakeGenerator) ReviseOutlines(context.Context, OutlineRevisionInput) (domain.ContinuationOutline, error) {
	if len(g.revisedOutline.Chapters) > 0 || len(g.revisedOutline.Volumes) > 0 {
		return g.revisedOutline, nil
	}
	return g.outline, nil
}

func TestServiceShortContinuationSkipsVolumeReviewAndStartsAtNPlusOne(t *testing.T) {
	generator := &fakeGenerator{
		proposal: domain.ContinuationProposal{
			Summary:            "short ending",
			Direction:          "resolve the final mystery",
			TargetChapterCount: 2,
			Structure:          domain.ContinuationStructureSingle,
		},
		outline: domain.ContinuationOutline{
			Structure: domain.ContinuationStructureSingle,
			Chapters: []domain.OutlineEntry{
				{Chapter: 11, Title: "answer", CoreEvent: "the clue is decoded"},
				{Chapter: 12, Title: "return", CoreEvent: "the cast returns home"},
			},
		},
	}
	service := newTestService(t, 10, generator)
	snapshot := prepareProposal(t, service)
	if snapshot.Workflow.Stage != domain.ContinuationStageProposalReviewPending {
		t.Fatalf("stage = %s, want proposal review", snapshot.Workflow.Stage)
	}

	snapshot, err := service.ApproveProposal(context.Background(), snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("approve proposal: %v", err)
	}
	if snapshot.Workflow.Stage != domain.ContinuationStageOutlineReviewPending {
		t.Fatalf("short proposal stage = %s, want outline review", snapshot.Workflow.Stage)
	}
	if len(snapshot.Volumes) != 0 {
		t.Fatalf("short proposal unexpectedly generated volumes: %+v", snapshot.Volumes)
	}
	if snapshot.Outlines.Chapters[0].Chapter != 11 {
		t.Fatalf("first continuation chapter = %d, want 11", snapshot.Outlines.Chapters[0].Chapter)
	}

	snapshot, err = service.ApproveOutlines(snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("approve outlines: %v", err)
	}
	if snapshot.Workflow.Stage != domain.ContinuationStageReadyToWrite || snapshot.Plan == nil {
		t.Fatalf("unexpected approved snapshot: %+v", snapshot)
	}
	if snapshot.Plan.Chapters[0].Chapter != 11 {
		t.Fatalf("approved plan starts at %d, want 11", snapshot.Plan.Chapters[0].Chapter)
	}
}

func TestServiceLongContinuationReviewsVolumesAndRevisionInvalidatesOutlines(t *testing.T) {
	volumes := testVolumeSkeleton("first direction")
	revisedVolumes := testVolumeSkeleton("revised direction")
	detailed := detailedVolumeOutline(6)
	generator := &fakeGenerator{
		proposal: domain.ContinuationProposal{
			Summary:            "long continuation",
			Direction:          "open a second campaign",
			TargetChapterCount: 4,
			Structure:          domain.ContinuationStructureVolumes,
		},
		volumes:        volumes,
		revisedVolumes: revisedVolumes,
		outline:        detailed,
	}
	service := newTestService(t, 5, generator)
	snapshot := prepareProposal(t, service)

	snapshot, err := service.ApproveProposal(context.Background(), snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("approve proposal: %v", err)
	}
	if snapshot.Workflow.Stage != domain.ContinuationStageVolumeReviewPending || len(snapshot.Volumes) != 2 {
		t.Fatalf("unexpected volume-review snapshot: %+v", snapshot)
	}

	snapshot, err = service.ApproveVolumes(context.Background(), snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("approve volumes: %v", err)
	}
	if snapshot.Workflow.Stage != domain.ContinuationStageOutlineReviewPending || snapshot.Outlines == nil {
		t.Fatalf("unexpected outline-review snapshot: %+v", snapshot)
	}

	snapshot, err = service.ReviseVolumes(context.Background(), snapshot.Workflow.Revision, "change volume one", 1)
	if err != nil {
		t.Fatalf("revise volumes: %v", err)
	}
	if snapshot.Workflow.Stage != domain.ContinuationStageVolumeReviewPending {
		t.Fatalf("stage = %s, want volume review", snapshot.Workflow.Stage)
	}
	if snapshot.Outlines != nil || snapshot.Plan != nil {
		t.Fatalf("volume revision did not invalidate downstream: %+v", snapshot)
	}
	if snapshot.Volumes[0].Theme != "revised direction" {
		t.Fatalf("revised volume was not persisted: %+v", snapshot.Volumes[0])
	}
}

func TestServiceProposalRevisionInvalidatesVolumesAndOutlines(t *testing.T) {
	generator := &fakeGenerator{
		proposal: domain.ContinuationProposal{
			Summary: "long", Direction: "long direction", TargetChapterCount: 4, Structure: domain.ContinuationStructureVolumes,
		},
		revisedProposal: domain.ContinuationProposal{
			Summary: "shorter", Direction: "finish now", TargetChapterCount: 2, Structure: domain.ContinuationStructureSingle,
		},
		volumes: testVolumeSkeleton("original"),
		outline: detailedVolumeOutline(6),
	}
	service := newTestService(t, 5, generator)
	snapshot := prepareProposal(t, service)
	var err error
	snapshot, err = service.ApproveProposal(context.Background(), snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("approve proposal: %v", err)
	}
	snapshot, err = service.ApproveVolumes(context.Background(), snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("approve volumes: %v", err)
	}

	snapshot, err = service.ReviseProposal(context.Background(), snapshot.Workflow.Revision, "make it shorter")
	if err != nil {
		t.Fatalf("revise proposal: %v", err)
	}
	if snapshot.Proposal.Structure != domain.ContinuationStructureSingle {
		t.Fatalf("proposal was not revised: %+v", snapshot.Proposal)
	}
	if len(snapshot.Volumes) != 0 || snapshot.Outlines != nil || snapshot.Plan != nil {
		t.Fatalf("proposal revision did not invalidate downstream: %+v", snapshot)
	}
}

func newTestService(t *testing.T, base int, generator Generator) *Service {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if _, err := st.Continuation.InitializeSource("source-signature", base); err != nil {
		t.Fatalf("initialize continuation: %v", err)
	}
	return NewService(st.Continuation, generator)
}

func prepareProposal(t *testing.T, service *Service) *domain.ContinuationSnapshot {
	t.Helper()
	snapshot, err := service.BeginDraft(1)
	if err != nil {
		t.Fatalf("begin draft: %v", err)
	}
	snapshot, err = service.CommitDraft("continue from the unresolved ending", snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("commit draft: %v", err)
	}
	snapshot, err = service.GenerateProposal(context.Background(), snapshot.Workflow.Revision)
	if err != nil {
		t.Fatalf("generate proposal: %v", err)
	}
	return snapshot
}

func testVolumeSkeleton(theme string) []domain.VolumeOutline {
	return []domain.VolumeOutline{
		{Index: 1, Title: "campaign", Theme: theme, Arcs: []domain.ArcOutline{{Index: 1, Title: "departure", EstimatedChapters: 2}}},
		{Index: 2, Title: "homecoming", Theme: "closure", Arcs: []domain.ArcOutline{{Index: 1, Title: "return", EstimatedChapters: 2}}},
	}
}

func detailedVolumeOutline(firstChapter int) domain.ContinuationOutline {
	chapter := firstChapter
	volumes := testVolumeSkeleton("first direction")
	for volumeIndex := range volumes {
		arc := &volumes[volumeIndex].Arcs[0]
		arc.EstimatedChapters = 0
		for range 2 {
			arc.Chapters = append(arc.Chapters, domain.OutlineEntry{
				Chapter: chapter, Title: "chapter", CoreEvent: "event",
			})
			chapter++
		}
	}
	return domain.ContinuationOutline{Structure: domain.ContinuationStructureVolumes, Volumes: volumes}
}
