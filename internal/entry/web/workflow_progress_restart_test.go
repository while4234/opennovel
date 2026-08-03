package web

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestAdaptationWorkflowProgressOffersDetailResumeAfterServerRestart(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationVolumeReview: &domain.AdaptationVolumeReview{
			Status: domain.AdaptationPlanStatusVolumeReview,
		},
		AdaptationPlanningWorkflow: &domain.AdaptationPlanningWorkflow{
			Version:  domain.AdaptationPlanningWorkflowVersion,
			Stage:    domain.AdaptationPlanningStageDetailsGenerating,
			Revision: 8,
		},
	}

	progress := adaptationWorkflowProgress("project-adaptation", snapshot, nil, nil, nil)

	if progress.Status != WorkflowStatusPaused || progress.CurrentStep != "chapter_outline" {
		t.Fatalf("status/step = %q/%q, want paused/chapter_outline", progress.Status, progress.CurrentStep)
	}
	if !progress.Recoverable {
		t.Fatal("interrupted detail generation is not recoverable")
	}
	if progress.NextAction == nil || progress.NextAction.ID != "resume_adaptation_proposal_details" {
		t.Fatalf("next action = %+v, want detail resume", progress.NextAction)
	}
}

func TestAdaptationWorkflowProgressKeepsActiveDetailGenerationRunning(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationVolumeReview: &domain.AdaptationVolumeReview{
			Status: domain.AdaptationPlanStatusVolumeReview,
		},
		AdaptationPlanningWorkflow: &domain.AdaptationPlanningWorkflow{
			Version:  domain.AdaptationPlanningWorkflowVersion,
			Stage:    domain.AdaptationPlanningStageDetailsGenerating,
			Revision: 8,
		},
	}

	progress := adaptationWorkflowProgress(
		"project-adaptation",
		snapshot,
		nil,
		nil,
		[]string{projectActionKindAdaptationProposal},
	)

	if progress.Status != WorkflowStatusRunning || progress.CurrentStep != "chapter_outline" {
		t.Fatalf("status/step = %q/%q, want running/chapter_outline", progress.Status, progress.CurrentStep)
	}
	if progress.NextAction != nil {
		t.Fatalf("active detail generation unexpectedly offers resume: %+v", progress.NextAction)
	}
}
