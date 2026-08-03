package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestAdaptationPlanningWorkflowLegacyVolumeReviewIsConservative(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Adaptation.io.WriteJSON(adaptationVolumeReviewFile, domain.AdaptationVolumeReview{
		Brief: "review first", Granularity: domain.AdaptationGranularityArc,
		Volumes: []domain.AdaptationVolumePlan{{Index: 1, Title: "One", TargetFrom: 1, TargetTo: 4}},
	}); err != nil {
		t.Fatalf("write legacy volume review: %v", err)
	}

	workflow, err := st.Adaptation.LoadPlanningWorkflow()
	if err != nil {
		t.Fatalf("LoadPlanningWorkflow: %v", err)
	}
	if workflow == nil || workflow.Stage != domain.AdaptationPlanningStageVolumeReviewPending {
		t.Fatalf("workflow = %+v, want volume review pending", workflow)
	}
}

func TestAdaptationPlanningWorkflowCASAndPersistence(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	started, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, 0)
	if err != nil {
		t.Fatalf("start workflow: %v", err)
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageDetailsGenerating, started.Revision+1); err == nil {
		t.Fatal("expected stale revision rejection")
	}
	pending, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageVolumeReviewPending, started.Revision)
	if err != nil {
		t.Fatalf("advance workflow: %v", err)
	}

	reloaded, err := NewStore(root).Adaptation.LoadPlanningWorkflow()
	if err != nil {
		t.Fatalf("reload workflow: %v", err)
	}
	if reloaded == nil || reloaded.Stage != domain.AdaptationPlanningStageVolumeReviewPending || reloaded.Revision != pending.Revision {
		t.Fatalf("reloaded = %+v, want %+v", reloaded, pending)
	}
}
