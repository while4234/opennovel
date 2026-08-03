package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type AutoResumeDisposition string

const (
	AutoResumeActionable AutoResumeDisposition = "actionable"
	AutoResumeWaitUser   AutoResumeDisposition = "wait_user"
	AutoResumeBusy       AutoResumeDisposition = "busy"
	AutoResumeNoWork     AutoResumeDisposition = "no_work"
	AutoResumeBlocked    AutoResumeDisposition = "blocked"
)

const (
	AutoResumeActionHost                 = "host_resume"
	AutoResumeActionCoCreate             = "cocreate_resume"
	AutoResumeActionAdaptationAnalysis   = "adaptation_analysis"
	AutoResumeActionAdaptationSkeleton   = "adaptation_skeleton"
	AutoResumeActionAdaptationDetails    = "adaptation_details"
	AutoResumeActionContinuationPlanning = "continuation_retry"
)

type AutoResumeDecision struct {
	Disposition      AutoResumeDisposition              `json:"disposition"`
	Action           string                             `json:"action,omitempty"`
	ReasonCode       string                             `json:"reason_code"`
	Label            string                             `json:"label"`
	StateFingerprint string                             `json:"state_fingerprint"`
	WorkflowRevision int                                `json:"workflow_revision,omitempty"`
	Recovery         *storepkg.ManuscriptRecoveryStatus `json:"recovery,omitempty"`
}

type autoResumeState struct {
	Disposition AutoResumeDisposition `json:"disposition"`
	Action      string                `json:"action,omitempty"`
	Reason      string                `json:"reason"`
	Revision    int                   `json:"revision,omitempty"`
	Phase       string                `json:"phase,omitempty"`
	Flow        string                `json:"flow,omitempty"`
	Chapter     int                   `json:"chapter,omitempty"`
}

func (s *ProjectSession) AutoResumeDecision() (AutoResumeDecision, error) {
	if !s.ScheduledResumeEnabled() {
		return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeNoWork, Reason: "scheduled_resume_disabled"}, "项目已关闭定时恢复"), nil
	}
	if s.hasActiveAction() {
		return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeBusy, Reason: "active_action"}, "项目已有任务运行中"), nil
	}
	snapshot := s.Snapshot()
	if snapshot.IsRunning {
		return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeBusy, Reason: "host_running"}, "项目正在运行"), nil
	}

	s.mu.Lock()
	manifest := s.manifest
	cocreate := s.cocreate
	s.mu.Unlock()
	if cocreate != nil {
		if !cocreate.coreCastResumeExempt() {
			if strings.TrimSpace(manifest.OutputDir) == "" {
				return blockedAutoResume("core_cast_gate_blocked", fmt.Errorf("project output directory is required for core cast gate")), nil
			}
			if err := host.RequireResumeCoreCastGate(storepkg.NewStore(manifest.OutputDir), false); err != nil {
				return blockedAutoResume("core_cast_gate_blocked", err), nil
			}
		}
		state := cocreate.apiState()
		if state.Failed && len(state.Suggestions) == 0 && len(state.PendingDecisions) == 0 && !state.Ready {
			return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeActionable, Action: AutoResumeActionCoCreate, Reason: "cocreate_failed"}, "恢复共创模型调用"), nil
		}
		return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeWaitUser, Reason: "cocreate_wait_user"}, "共创建议或决策等待用户"), nil
	}

	if strings.TrimSpace(manifest.OutputDir) == "" {
		return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeNoWork, Reason: "missing_output_dir"}, "项目没有可恢复目录"), nil
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if active, err := st.Revisions.Active(); err != nil {
		return blockedAutoResume("revision_read_failed", err), nil
	} else if active != nil {
		return makeAutoResumeDecision(
			autoResumeState{Disposition: AutoResumeBlocked, Reason: "active_revision", Revision: active.Revision},
			"活动修订等待修订任务或人工确认",
		), nil
	}
	if active, err := st.ManuscriptRevisions.Active(); err != nil {
		return blockedAutoResume("manuscript_revision_read_failed", err), nil
	} else if active != nil {
		return makeAutoResumeDecision(
			autoResumeState{Disposition: AutoResumeBlocked, Reason: "active_manuscript_revision", Revision: active.Revision},
			"正文修订等待签名审核或人工确认",
		), nil
	}
	if required, err := host.CharacterConfirmationRequired(st); err != nil {
		return blockedAutoResume("character_workflow_read_failed", err), nil
	} else if required {
		return makeAutoResumeDecision(
			autoResumeState{Disposition: AutoResumeWaitUser, Reason: "character_confirmation_required"},
			"角色卡已审核通过，请确认后继续生成完整设定",
		), nil
	}
	if err := host.RequireResumeCoreCastGate(st, false); err != nil {
		return blockedAutoResume("core_cast_gate_blocked", err), nil
	}
	if pending, pendingErr := host.AdaptationCharacterWorkflowPending(st); pendingErr != nil {
		return blockedAutoResume("character_workflow_read_failed", pendingErr), nil
	} else if pending {
		return makeAutoResumeDecision(
			autoResumeState{Disposition: AutoResumeActionable, Action: AutoResumeActionHost, Reason: "adaptation_character_pending"},
			"恢复改编角色卡分析与独立审核",
		), nil
	}
	if err := st.RequireManuscriptWriteReady(); err != nil {
		recovery := st.ManuscriptRecoveryState()
		return makeRecoveryAutoResumeDecision(recovery), nil
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return blockedAutoResume("progress_read_failed", err), nil
	}
	if progress != nil && progress.CompletionRevalidation != nil && progress.CompletionRevalidation.Status != "completed" {
		checkpoint := progress.CompletionRevalidation
		if checkpoint.Mode == domain.RevisionModeNormal &&
			strings.HasPrefix(checkpoint.AcceptedRevisionID, "normal-completion-baseline-") &&
			progress.Phase == domain.PhaseWriting && progress.TotalChapters > 0 &&
			len(progress.CompletedChapters) == progress.TotalChapters && len(progress.PendingRewrites) == 0 {
			return makeAutoResumeDecision(
				autoResumeState{Disposition: AutoResumeActionable, Action: AutoResumeActionHost, Reason: "legacy_completion_revalidation", Phase: string(progress.Phase)},
				"修复旧项目完结证据并重新执行独立审计",
			), nil
		}
		return makeAutoResumeDecision(
			autoResumeState{Disposition: AutoResumeWaitUser, Reason: "completion_revalidation_required", Phase: string(progress.Phase)},
			"完结作品结构已变更，等待正文、后处理与分层审核重新验证",
		), nil
	}
	if review, err := st.RunMeta.PlanningReview(); err != nil {
		return blockedAutoResume("planning_review_read_failed", err), nil
	} else if review != nil && review.Status == domain.PlanningReviewStatusPending {
		return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeWaitUser, Reason: "planning_review_pending"}, "规划等待用户审核"), nil
	} else if review != nil && review.FoundationStatus != "" &&
		!(review.Kind == domain.PlanningReviewKindFoundation && review.FoundationStatus == domain.FoundationReviewStatusCollecting) {
		if err := st.RequireConfirmedFoundation(); err != nil {
			return blockedAutoResume("foundation_gate_blocked", err), nil
		}
	}

	workflow, err := st.Adaptation.LoadPlanningWorkflow()
	if err != nil {
		return blockedAutoResume("adaptation_workflow_read_failed", err), nil
	}
	if workflow != nil {
		state := autoResumeState{Revision: workflow.Revision}
		switch workflow.Stage {
		case domain.AdaptationPlanningStageTargetFoundationGenerating:
			state.Disposition, state.Reason = AutoResumeWaitUser, "adaptation_target_foundation_generation_interrupted"
			return makeAutoResumeDecision(state, "目标 StoryFoundation 生成中断，请重试当前共创提交或修订"), nil
		case domain.AdaptationPlanningStageFoundationReviewPending:
			state.Disposition, state.Reason = AutoResumeWaitUser, "adaptation_foundation_review_pending"
			return makeAutoResumeDecision(state, "目标 StoryFoundation 等待用户审核"), nil
		case domain.AdaptationPlanningStageSkeletonGenerating:
			action, actionErr := pendingAdaptationProposalResumeAction(st)
			if actionErr != nil {
				return blockedAutoResume("adaptation_checkpoint_read_failed", actionErr), nil
			}
			if action == nil {
				return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeBlocked, Reason: "adaptation_skeleton_checkpoint_missing", Revision: workflow.Revision}, "改编骨架缺少可恢复检查点"), nil
			}
			state.Disposition, state.Action, state.Reason = AutoResumeActionable, AutoResumeActionAdaptationSkeleton, "adaptation_skeleton_interrupted"
			return makeAutoResumeDecision(state, "恢复改编分卷骨架生成"), nil
		case domain.AdaptationPlanningStageDetailsGenerating:
			action, actionErr := pendingAdaptationProposalResumeAction(st)
			if actionErr != nil {
				return blockedAutoResume("adaptation_checkpoint_read_failed", actionErr), nil
			}
			if action == nil {
				return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeBlocked, Reason: "adaptation_details_checkpoint_missing", Revision: workflow.Revision}, "改编细纲缺少可恢复检查点"), nil
			}
			state.Disposition, state.Action, state.Reason = AutoResumeActionable, AutoResumeActionAdaptationDetails, "adaptation_details_interrupted"
			return makeAutoResumeDecision(state, "恢复改编章节细纲生成"), nil
		case domain.AdaptationPlanningStageVolumeReviewPending:
			state.Disposition, state.Reason = AutoResumeWaitUser, "adaptation_volume_review_pending"
			return makeAutoResumeDecision(state, "改编分卷骨架等待用户审核"), nil
		case domain.AdaptationPlanningStageProposalReviewPending:
			state.Disposition, state.Reason = AutoResumeWaitUser, "adaptation_proposal_review_pending"
			return makeAutoResumeDecision(state, "改编详细提案等待用户确认"), nil
		}
	}

	if action, err := pendingAdaptationAnalysisResumeAction(manifest); err != nil {
		return blockedAutoResume("adaptation_analysis_read_failed", err), nil
	} else if action != nil {
		return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeActionable, Action: AutoResumeActionAdaptationAnalysis, Reason: "adaptation_analysis_paused"}, action.Label), nil
	}

	continuation, err := s.host.ContinuationSnapshot()
	if err != nil {
		return blockedAutoResume("continuation_read_failed", err), nil
	}
	if continuation != nil {
		state := autoResumeState{Revision: continuation.Workflow.Revision}
		stage := continuation.Workflow.Stage
		resumeStage := continuation.Workflow.ResumeStage
		switch stage {
		case domain.ContinuationStageProposalGenerating, domain.ContinuationStageOutlineGenerating:
			state.Disposition, state.Action, state.Reason = AutoResumeActionable, AutoResumeActionContinuationPlanning, "continuation_generating"
			return makeAutoResumeDecision(state, "恢复续写规划生成"), nil
		case domain.ContinuationStagePaused, domain.ContinuationStageFailed:
			if resumeStage == domain.ContinuationStageProposalGenerating || resumeStage == domain.ContinuationStageOutlineGenerating || resumeStage == domain.ContinuationStageWriting {
				state.Disposition, state.Action, state.Reason = AutoResumeActionable, AutoResumeActionContinuationPlanning, "continuation_interrupted"
				return makeAutoResumeDecision(state, "从检查点恢复续写"), nil
			}
		case domain.ContinuationStageProposalReviewPending, domain.ContinuationStageVolumeReviewPending, domain.ContinuationStageOutlineReviewPending, domain.ContinuationStageReadyToWrite, domain.ContinuationStageDraftCollecting, domain.ContinuationStageSourceReady:
			state.Disposition, state.Reason = AutoResumeWaitUser, "continuation_wait_user"
			return makeAutoResumeDecision(state, "续写流程等待用户操作"), nil
		case domain.ContinuationStageWriting:
			state.Disposition, state.Action, state.Reason = AutoResumeActionable, AutoResumeActionHost, "continuation_writing_interrupted"
			return makeAutoResumeDecision(state, "恢复续写正文"), nil
		}
	}

	progress, err = st.Progress.Load()
	if progress == nil {
		return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeNoWork, Reason: "empty_project"}, "项目尚未开始"), nil
	}
	state := autoResumeState{Phase: string(progress.Phase), Flow: string(progress.Flow), Chapter: progress.CurrentChapter}
	if progress.Phase == domain.PhaseComplete {
		state.Disposition, state.Reason = AutoResumeNoWork, "phase_complete"
		return makeAutoResumeDecision(state, "项目已完成"), nil
	}
	state.Disposition, state.Action, state.Reason = AutoResumeActionable, AutoResumeActionHost, "host_work_interrupted"
	return makeAutoResumeDecision(state, "恢复未完成创作"), nil
}

// ExecuteAutoResume serializes automatic attempts, revalidates the durable
// fingerprint immediately before dispatch, and relies on each action's normal
// project action lock to reject a simultaneous user command.
func (s *ProjectSession) ExecuteAutoResume(ctx context.Context, expectedFingerprint string) (AutoResumeDecision, error) {
	s.autoResumeMu.Lock()
	defer s.autoResumeMu.Unlock()
	decision, err := s.AutoResumeDecision()
	if err != nil {
		return decision, err
	}
	if decision.Disposition != AutoResumeActionable {
		return decision, nil
	}
	if expectedFingerprint != "" && decision.StateFingerprint != expectedFingerprint {
		return decision, fmt.Errorf("auto-resume state changed before execution")
	}
	s.mu.Lock()
	outputDir := s.manifest.OutputDir
	cocreate := s.cocreate
	s.mu.Unlock()
	if !cocreate.coreCastResumeExempt() {
		if strings.TrimSpace(outputDir) != "" {
			st := storepkg.NewStore(outputDir)
			if gateErr := host.RequireResumeCoreCastGate(st, true); gateErr != nil {
				return blockedAutoResume("core_cast_gate_blocked", gateErr), nil
			}
			if review, reviewErr := st.RunMeta.PlanningReview(); reviewErr != nil {
				return blockedAutoResume("planning_review_read_failed", reviewErr), nil
			} else if review != nil && review.FoundationStatus != "" &&
				!(review.Kind == domain.PlanningReviewKindFoundation && review.FoundationStatus == domain.FoundationReviewStatusCollecting) {
				if gateErr := st.RequireConfirmedFoundation(); gateErr != nil {
					return blockedAutoResume("foundation_gate_blocked", gateErr), nil
				}
			}
		}
	}
	switch decision.Action {
	case AutoResumeActionCoCreate:
		_, err = s.ResumeCoCreate(ctx)
	case AutoResumeActionAdaptationAnalysis, AutoResumeActionAdaptationSkeleton, AutoResumeActionAdaptationDetails:
		_, _, err = s.resumePendingWebAction(ctx)
	case AutoResumeActionContinuationPlanning:
		_, err = s.RetryContinuation(ctx, decision.WorkflowRevision)
	case AutoResumeActionHost:
		_, err = s.Resume()
	default:
		err = fmt.Errorf("unsupported auto-resume action %q", decision.Action)
	}
	return decision, err
}

func makeAutoResumeDecision(state autoResumeState, label string) AutoResumeDecision {
	payload, _ := json.Marshal(state)
	sum := sha256.Sum256(payload)
	return AutoResumeDecision{
		Disposition: state.Disposition, Action: state.Action, ReasonCode: state.Reason,
		Label: label, StateFingerprint: hex.EncodeToString(sum[:]), WorkflowRevision: state.Revision,
	}
}

func blockedAutoResume(reason string, err error) AutoResumeDecision {
	return makeAutoResumeDecision(autoResumeState{Disposition: AutoResumeBlocked, Reason: reason}, "无法安全判断恢复状态："+err.Error())
}

func makeRecoveryAutoResumeDecision(recovery storepkg.ManuscriptRecoveryStatus) AutoResumeDecision {
	if !recovery.Required {
		recovery = storepkg.ManuscriptRecoveryStatus{
			Required: true,
			Class:    "publication_recovery_required",
			Owners:   []string{"unknown"},
			Message:  "durable manuscript recovery could not be verified",
		}
	}
	decision := makeAutoResumeDecision(
		autoResumeState{Disposition: AutoResumeBlocked, Reason: "publication_recovery_required"},
		"正式稿件恢复尚未完成，当前只允许读取",
	)
	decision.Recovery = &recovery
	return decision
}
