package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	continuationflow "github.com/voocel/ainovel-cli/internal/host/continuation"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func (h *Host) BeginContinuationDraft(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if h == nil || h.store == nil || h.store.Continuation == nil {
		return nil, fmt.Errorf("continuation store is unavailable")
	}
	snapshot, err := h.store.Continuation.Update(expectedRevision, func(next *domain.ContinuationSnapshot) error {
		if next.Workflow.Stage != domain.ContinuationStageSourceReady {
			return fmt.Errorf("continuation source is not ready for Draft collection")
		}
		next.Workflow.Stage = domain.ContinuationStageDraftCollecting
		return nil
	})
	if err == nil {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "已进入续写 Draft 共创", Level: "info"})
	}
	return snapshot, err
}

func (h *Host) CommitContinuationDraft(draft string, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return nil, fmt.Errorf("continuation Draft is required")
	}
	snapshot, err := h.store.Continuation.Update(expectedRevision, func(next *domain.ContinuationSnapshot) error {
		if next.Workflow.Stage != domain.ContinuationStageDraftCollecting {
			return fmt.Errorf("continuation Draft is not being collected")
		}
		next.Workflow.Draft = draft
		next.Workflow.DraftRevision++
		next.Workflow.Stage = domain.ContinuationStageProposalGenerating
		next.Proposal = nil
		next.Volumes = nil
		next.Outlines = nil
		next.Plan = nil
		return nil
	})
	if err == nil {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "续写 Draft 已确认，等待生成提案", Level: "info"})
	}
	return snapshot, err
}

func (h *Host) GenerateContinuationProposal(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("生成续写提案"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().GenerateProposal(h.continuationActionContext(ctx), expectedRevision)
	h.emitContinuationAction("续写提案已生成，等待审核", err)
	return snapshot, err
}

func (h *Host) ReviseContinuationProposal(ctx context.Context, instruction string, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("修改续写提案"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().ReviseProposal(h.continuationActionContext(ctx), expectedRevision, instruction)
	h.emitContinuationAction("续写提案已修改，等待审核", err)
	return snapshot, err
}

func (h *Host) ApproveContinuationProposal(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("审核续写提案"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().ApproveProposal(h.continuationActionContext(ctx), expectedRevision)
	h.emitContinuationAction("续写提案已通过，后续规划已生成并等待审核", err)
	return snapshot, err
}

func (h *Host) ReviseContinuationVolumes(ctx context.Context, instruction string, volumeIndex, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("修改续写分卷"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().ReviseVolumes(h.continuationActionContext(ctx), expectedRevision, instruction, volumeIndex)
	h.emitContinuationAction("续写分卷已修改，等待审核", err)
	return snapshot, err
}

func (h *Host) ApproveContinuationVolumes(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("审核续写分卷"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().ApproveVolumes(h.continuationActionContext(ctx), expectedRevision)
	h.emitContinuationAction("续写分卷已通过，章节细纲已生成并等待审核", err)
	return snapshot, err
}

func (h *Host) GenerateContinuationOutlines(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("生成续写章节细纲"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().GenerateOutlines(h.continuationActionContext(ctx), expectedRevision)
	h.emitContinuationAction("续写章节细纲已生成，等待审核", err)
	return snapshot, err
}

func (h *Host) ReviseContinuationOutlines(ctx context.Context, revision continuationflow.OutlineRevisionInput, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("修改续写章节细纲"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().ReviseOutlines(h.continuationActionContext(ctx), expectedRevision, revision)
	h.emitContinuationAction("续写章节细纲已修改，等待审核", err)
	return snapshot, err
}

func (h *Host) ApproveContinuationOutlines(expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("审核续写章节细纲"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().ApproveOutlines(expectedRevision)
	h.emitContinuationAction("续写章节细纲已通过，可以确认开写", err)
	return snapshot, err
}

// StartContinuation is the only path that projects the approved candidate
// outline into canonical story state and opens the Writer gate.
func (h *Host) StartContinuation(expectedRevision int) (string, *domain.ContinuationSnapshot, error) {
	if err := h.guardContinuationPlanningAction("开始小说续写"); err != nil {
		return "", nil, err
	}
	if h.coordinator != nil {
		h.coordinator.WaitForIdle()
	}
	h.releaseNormalFlowRunOwnership()
	ownership, err := h.acquireNormalFlowOwnership("host:start-continuation")
	if err != nil {
		return "", nil, err
	}
	defer ownership.Release()
	committed, err := h.store.CommitContinuationPlan(expectedRevision)
	if err != nil {
		return "", nil, err
	}
	writing, err := h.continuationPlanner().MarkWriting(committed.Workflow.Revision)
	if err != nil {
		return "", nil, err
	}
	h.refreshWriterRestore()
	label, err := h.resume(true)
	if err != nil {
		return "", writing, err
	}
	h.emitContinuationAction("续写规划已确认，开始从下一章写作", nil)
	return label, writing, nil
}

func (h *Host) RetryContinuation(ctx context.Context, expectedRevision int) (*domain.ContinuationSnapshot, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := h.guardContinuationPlanningAction("重试续写规划"); err != nil {
		return nil, err
	}
	snapshot, err := h.continuationPlanner().Retry(expectedRevision)
	if err != nil {
		return nil, err
	}
	switch snapshot.Workflow.Stage {
	case domain.ContinuationStageProposalGenerating:
		if snapshot.Proposal != nil && snapshot.Proposal.Structure == domain.ContinuationStructureVolumes {
			return h.continuationPlanner().GenerateVolumes(h.continuationActionContext(ctx), snapshot.Workflow.Revision)
		}
		return h.GenerateContinuationProposal(ctx, snapshot.Workflow.Revision)
	case domain.ContinuationStageOutlineGenerating:
		return h.GenerateContinuationOutlines(ctx, snapshot.Workflow.Revision)
	default:
		return snapshot, nil
	}
}

func (h *Host) guardContinuationPlanningAction(action string) error {
	if err := h.guardExclusive(action); err != nil {
		return err
	}
	return h.budget.Refuse()
}

func (h *Host) emitContinuationAction(summary string, err error) {
	level := "info"
	detail := ""
	if err != nil {
		level = "error"
		detail = err.Error()
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Detail: detail, Level: level})
}

// GenerateContinuationProposal is implemented by the continuation planner
// facade below once the Draft has been committed. The context is kept on the
// public boundary because proposal and outline generation may be long-running.
func (h *Host) continuationActionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// ContinuationSnapshot returns the durable continuation planning read model.
// A nil snapshot means this project was not created from an imported novel.
func (h *Host) ContinuationSnapshot() (*domain.ContinuationSnapshot, error) {
	if h == nil || h.store == nil || h.store.Continuation == nil {
		return nil, nil
	}
	snapshot, err := h.store.Continuation.LoadSnapshot()
	if errors.Is(err, storepkg.ErrContinuationNotInitialized) {
		return nil, nil
	}
	return snapshot, err
}

// ensureContinuationWritingAllowed is a Host-level safety gate. UI controls
// are advisory; Resume and stopped-state Continue must also refuse to start a
// coordinator while a continuation Draft or planning artifact awaits review.
func (h *Host) ensureContinuationWritingAllowed() error {
	snapshot, err := h.ContinuationSnapshot()
	if err != nil || snapshot == nil {
		return err
	}
	if snapshot.Workflow.Stage == domain.ContinuationStageWriting {
		return nil
	}
	return continuationPlanningGateError(snapshot.Workflow.Stage)
}

func continuationPlanningGateError(stage domain.ContinuationStage) error {
	switch stage {
	case domain.ContinuationStageSourceReady, domain.ContinuationStageDraftCollecting:
		return fmt.Errorf("续写尚未确认 Draft，请先完成续写共创")
	case domain.ContinuationStageProposalGenerating:
		return fmt.Errorf("续写提案正在生成，完成后需先审核")
	case domain.ContinuationStageProposalReviewPending:
		return fmt.Errorf("续写提案待审核，不能直接恢复写作")
	case domain.ContinuationStageVolumeReviewPending:
		return fmt.Errorf("续写分卷规划待审核，不能直接恢复写作")
	case domain.ContinuationStageOutlineGenerating:
		return fmt.Errorf("续写章节细纲正在生成，完成后需先审核")
	case domain.ContinuationStageOutlineReviewPending:
		return fmt.Errorf("续写章节细纲待审核，不能直接恢复写作")
	case domain.ContinuationStageReadyToWrite:
		return fmt.Errorf("续写规划已通过，请使用“确认并开始续写”启动")
	case domain.ContinuationStagePaused:
		return fmt.Errorf("续写规划已暂停，请先恢复当前规划阶段")
	case domain.ContinuationStageFailed:
		return fmt.Errorf("续写规划失败，请先重试当前规划阶段")
	default:
		return fmt.Errorf("续写工作流状态 %q 不允许开始写作", stage)
	}
}
