package flow

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/tools"
)

// PlanningReviewTaskPreparer binds routed review tasks to Host-owned
// authorization. New dispatches replace an older run for the same selector;
// reminders reuse the active run so an in-flight cursor remains valid.
type PlanningReviewTaskPreparer struct {
	registry *tools.PlanningReviewRunRegistry
}

func NewPlanningReviewTaskPreparer(registry *tools.PlanningReviewRunRegistry) *PlanningReviewTaskPreparer {
	return &PlanningReviewTaskPreparer{registry: registry}
}

func (p *PlanningReviewTaskPreparer) PrepareNew(instruction *Instruction) (*Instruction, error) {
	if instruction == nil || instruction.PlanningReview == nil {
		return instruction, nil
	}
	if p == nil || p.registry == nil {
		return nil, fmt.Errorf("planning review registry is not configured")
	}
	reviewID, err := newPlanningReviewDispatchID()
	if err != nil {
		return nil, err
	}
	if err := p.registry.Authorize(reviewID, *instruction.PlanningReview); err != nil {
		return nil, err
	}
	return attachPlanningReviewID(instruction, reviewID), nil
}

func (p *PlanningReviewTaskPreparer) PrepareActive(instruction *Instruction) (*Instruction, error) {
	if instruction == nil || instruction.PlanningReview == nil {
		return instruction, nil
	}
	if p == nil || p.registry == nil {
		return nil, fmt.Errorf("planning review registry is not configured")
	}
	reviewID, err := p.registry.ResolveActive(*instruction.PlanningReview)
	if err != nil {
		return nil, err
	}
	return attachPlanningReviewID(instruction, reviewID), nil
}

func attachPlanningReviewID(instruction *Instruction, reviewID string) *Instruction {
	copyInstruction := *instruction
	copyInstruction.Task += fmt.Sprintf(" Host has authorized review_id=%q for this dispatch. The first planning_review call may omit review_id; use the canonical review_id returned on page zero for save_original_planning_audit. Follow each signed next_cursor in order until complete=true; never restart page zero or save early.", reviewID)
	return &copyInstruction
}
