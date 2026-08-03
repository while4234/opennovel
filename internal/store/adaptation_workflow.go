package store

import (
	"fmt"
	"os"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const adaptationPlanningWorkflowFile = adaptationRootDir + "/planning_workflow.json"

// LoadPlanningWorkflow returns an explicit workflow when present. For legacy
// projects it infers only conservative review gates and never persists the
// inference as a side effect of reading.
func (s *AdaptationStore) LoadPlanningWorkflow() (*domain.AdaptationPlanningWorkflow, error) {
	var workflow domain.AdaptationPlanningWorkflow
	if err := s.io.ReadJSON(adaptationPlanningWorkflowFile, &workflow); err == nil {
		if workflow.Version == 1 {
			return s.inferLegacyPlanningWorkflow()
		}
		if err := workflow.Validate(); err != nil {
			return nil, err
		}
		return &workflow, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s.inferLegacyPlanningWorkflow()
}

func (s *AdaptationStore) inferLegacyPlanningWorkflow() (*domain.AdaptationPlanningWorkflow, error) {
	stage := domain.AdaptationPlanningStage("")
	if review, err := s.LoadTargetFoundationReview(); err != nil {
		return nil, err
	} else if review != nil {
		switch review.State {
		case domain.AdaptationFoundationReviewGenerating:
			stage = domain.AdaptationPlanningStageTargetFoundationGenerating
		case domain.AdaptationFoundationReviewPending, domain.AdaptationFoundationReviewApproved, domain.AdaptationFoundationReviewReadonly:
			stage = domain.AdaptationPlanningStageFoundationReviewPending
		}
	} else if plan, err := s.LoadPlan(); err != nil {
		return nil, err
	} else if plan != nil {
		// A legacy plan is not evidence that the target StoryFoundation was
		// explicitly reviewed.  It must pass the new checkpoint first.
		stage = domain.AdaptationPlanningStageTargetFoundationGenerating
	} else if proposal, err := s.LoadProposal(); err != nil {
		return nil, err
	} else if proposal != nil {
		stage = domain.AdaptationPlanningStageProposalReviewPending
	} else if review, err := s.LoadVolumeReview(); err != nil {
		return nil, err
	} else if review != nil {
		stage = domain.AdaptationPlanningStageVolumeReviewPending
	} else if runtime, err := s.LoadProposalRuntime(); err != nil {
		return nil, err
	} else if runtime != nil {
		stage = domain.AdaptationPlanningStageSkeletonGenerating
	}
	if stage == "" {
		return nil, nil
	}
	return &domain.AdaptationPlanningWorkflow{
		Version:  domain.AdaptationPlanningWorkflowVersion,
		Stage:    stage,
		Revision: 1,
	}, nil
}

func (s *AdaptationStore) SetPlanningWorkflowStage(stage domain.AdaptationPlanningStage, expectedRevision int) (*domain.AdaptationPlanningWorkflow, error) {
	if !stage.Valid() {
		return nil, fmt.Errorf("invalid adaptation planning stage %q", stage)
	}
	var saved domain.AdaptationPlanningWorkflow
	err := s.withLegacyFormalMutation("change planning workflow", func() error {
		return s.io.WithWriteLock(func() error {
			current, err := s.loadPlanningWorkflowUnlocked()
			if err != nil {
				return err
			}
			if expectedRevision >= 0 && current.Revision != expectedRevision {
				return fmt.Errorf("adaptation planning workflow revision changed: expected %d, got %d", expectedRevision, current.Revision)
			}
			saved = domain.AdaptationPlanningWorkflow{
				Version:   domain.AdaptationPlanningWorkflowVersion,
				Stage:     stage,
				Revision:  current.Revision + 1,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			}
			return s.io.WriteJSONUnlocked(adaptationPlanningWorkflowFile, saved)
		})
	})
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

func (s *AdaptationStore) loadPlanningWorkflowUnlocked() (domain.AdaptationPlanningWorkflow, error) {
	var workflow domain.AdaptationPlanningWorkflow
	if err := s.io.ReadJSONUnlocked(adaptationPlanningWorkflowFile, &workflow); err != nil {
		if os.IsNotExist(err) {
			return domain.AdaptationPlanningWorkflow{}, nil
		}
		return domain.AdaptationPlanningWorkflow{}, err
	}
	if err := workflow.Validate(); err != nil {
		return domain.AdaptationPlanningWorkflow{}, err
	}
	return workflow, nil
}

func (s *AdaptationStore) writePlanningWorkflowStageUnlocked(stage domain.AdaptationPlanningStage) error {
	current, err := s.loadPlanningWorkflowUnlocked()
	if err != nil {
		return err
	}
	workflow := domain.AdaptationPlanningWorkflow{
		Version:   domain.AdaptationPlanningWorkflowVersion,
		Stage:     stage,
		Revision:  current.Revision + 1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return s.io.WriteJSONUnlocked(adaptationPlanningWorkflowFile, workflow)
}
