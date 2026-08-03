package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

type WorkflowStatus string

const (
	WorkflowStatusIdle                WorkflowStatus = "idle"
	WorkflowStatusRunning             WorkflowStatus = "running"
	WorkflowStatusWaitingConfirmation WorkflowStatus = "waiting_confirmation"
	WorkflowStatusPaused              WorkflowStatus = "paused"
	WorkflowStatusFailed              WorkflowStatus = "failed"
	WorkflowStatusCompleted           WorkflowStatus = "completed"
)

const (
	workflowNormal       = "normal"
	workflowAdaptation   = "adaptation"
	workflowContinuation = "continuation"
)

// WorkflowStep is one user-visible stage in a workflow. Status uses the same
// lifecycle vocabulary as WorkflowProgress so clients need only one state
// machine for project and step rendering.
type WorkflowStep struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Status  WorkflowStatus `json:"status"`
	Current int            `json:"current,omitempty"`
	Total   int            `json:"total,omitempty"`
	Message string         `json:"message,omitempty"`
}

// WorkflowNextAction carries the concurrency token a client should send with
// its next mutation. The key is deterministic for a workflow revision, making
// retries safe to identify while the persistent job layer is being introduced.
type WorkflowNextAction struct {
	ID                   string `json:"id"`
	Label                string `json:"label"`
	ExpectedRevision     int    `json:"expected_revision"`
	IdempotencyKey       string `json:"idempotency_key"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

type WorkflowProgress struct {
	Workflow        string              `json:"workflow"`
	RunID           string              `json:"run_id"`
	Revision        int                 `json:"revision"`
	Status          WorkflowStatus      `json:"status"`
	CurrentStep     string              `json:"current_step"`
	Steps           []WorkflowStep      `json:"steps"`
	NextAction      *WorkflowNextAction `json:"next_action,omitempty"`
	Recoverable     bool                `json:"recoverable"`
	Error           string              `json:"error,omitempty"`
	CurrentAgent    string              `json:"current_agent,omitempty"`
	CurrentProvider string              `json:"current_provider,omitempty"`
	CurrentModel    string              `json:"current_model,omitempty"`
}

// WebSnapshot preserves the legacy flat host snapshot while adding the
// unified workflow contract used by Web and SSE clients.
type WebSnapshot struct {
	host.UISnapshot
	WorkflowProgress WorkflowProgress `json:"workflow_progress"`
	CurrentAction    *ActionRecord    `json:"current_action,omitempty"`
}

func (s *ProjectSession) WebSnapshot() WebSnapshot {
	snapshot := s.Snapshot()
	return WebSnapshot{
		UISnapshot:       snapshot,
		WorkflowProgress: s.workflowProgress(snapshot),
		CurrentAction:    s.LatestBackgroundAction(),
	}
}

func (s *ProjectSession) WorkflowProgress() WorkflowProgress {
	return s.workflowProgress(s.Snapshot())
}

func (s *ProjectSession) workflowProgress(snapshot host.UISnapshot) WorkflowProgress {
	coCreate := s.CoCreateState()
	continuation, _ := s.ContinuationSnapshot()
	adaptationEvent := s.latestAdaptationProgressEvent()

	workflow := selectWorkflow(snapshot, coCreate, continuation, adaptationEvent, s.currentActionKinds())
	var progress WorkflowProgress
	switch workflow {
	case workflowContinuation:
		progress = continuationWorkflowProgress(s.manifest.ID, snapshot, coCreate, continuation)
	case workflowAdaptation:
		progress = adaptationWorkflowProgress(s.manifest.ID, snapshot, coCreate, adaptationEvent, s.currentActionKinds())
	default:
		progress = normalWorkflowProgress(s.manifest.ID, snapshot, coCreate)
	}
	s.attachCurrentWorkflowModel(&progress, snapshot)
	return progress
}

func (s *ProjectSession) attachCurrentWorkflowModel(progress *WorkflowProgress, snapshot host.UISnapshot) {
	if progress == nil || progress.Status != WorkflowStatusRunning || s.host == nil {
		return
	}
	route, agent := currentWorkflowModelRoute(progress.CurrentStep, snapshot.Agents)
	if route == "" {
		return
	}
	provider, model, _ := s.host.CurrentModelSelection(route)
	progress.CurrentAgent = agent
	progress.CurrentProvider = strings.TrimSpace(provider)
	progress.CurrentModel = strings.TrimSpace(model)
}

func currentWorkflowModelRoute(step string, agents []host.AgentSnapshot) (route, agent string) {
	if active := latestWorkingAgent(agents); active != "" {
		return workflowModelRouteForAgent(active, step), active
	}
	stage := currentWorkflowModelStage(step)
	if stage == "" {
		return "", ""
	}
	return bootstrap.StageRouteKey(stage), ""
}

func latestWorkingAgent(agents []host.AgentSnapshot) string {
	var latest *host.AgentSnapshot
	for i := range agents {
		candidate := &agents[i]
		if !strings.EqualFold(strings.TrimSpace(candidate.State), "working") || strings.TrimSpace(candidate.Name) == "" {
			continue
		}
		if latest == nil || candidate.UpdatedAt.After(latest.UpdatedAt) {
			latest = candidate
		}
	}
	if latest == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(latest.Name))
}

func workflowModelRouteForAgent(agent, step string) string {
	agent = strings.ToLower(strings.TrimSpace(agent))
	switch {
	case strings.Contains(agent, "coordinator"):
		return "coordinator"
	case strings.Contains(agent, "writer"):
		return bootstrap.StageRouteKey(bootstrap.StageWriting)
	case strings.Contains(agent, "editor"), strings.Contains(agent, "auditor"):
		return bootstrap.StageRouteKey(bootstrap.StageReview)
	case strings.Contains(agent, "architect"):
		if stage := currentWorkflowModelStage(step); stage != "" {
			return bootstrap.StageRouteKey(stage)
		}
	}
	return agent
}

func currentWorkflowModelStage(step string) string {
	switch step {
	case "creative_intent", "clarification", "contract", "draft":
		return bootstrap.StageCoCreate
	case "source", "source_baseline", "analysis":
		return bootstrap.StageSourceAnalysis
	case "foundation", "structure", "volume_plan", "proposal", "volumes":
		return bootstrap.StageSkeleton
	case "planning_review", "chapter_outline", "proposal_review", "outlines":
		return bootstrap.StageDetailOutline
	case "writing":
		return bootstrap.StageWriting
	case "quality_audit":
		return bootstrap.StageReview
	default:
		return ""
	}
}

func selectWorkflow(
	snapshot host.UISnapshot,
	coCreate *webCoCreateState,
	continuation *domain.ContinuationSnapshot,
	adaptationEvent *APIHostEvent,
	actionKinds []string,
) string {
	if coCreate != nil {
		switch coCreate.Kind {
		case webCoCreateKindContinuation:
			return workflowContinuation
		case webCoCreateKindAdapt:
			return workflowAdaptation
		case webCoCreateKindNormal, webCoCreateKindStage:
			return workflowNormal
		}
	}
	if continuation != nil {
		return workflowContinuation
	}
	if snapshot.AdaptationPlan != nil || snapshot.AdaptationProposal != nil ||
		snapshot.AdaptationVolumeReview != nil || adaptationEvent != nil ||
		containsAdaptationAction(actionKinds) {
		return workflowAdaptation
	}
	return workflowNormal
}

func containsAdaptationAction(kinds []string) bool {
	for _, kind := range kinds {
		switch kind {
		case projectActionKindAdaptationAnalysis, projectActionKindAdaptationProposal, projectActionKindAdaptationRevision:
			return true
		}
	}
	return false
}

const (
	normalLayeredPlanningWordThreshold = 50_000
	adaptationVolumeMinTargetChapters  = 18
	adaptationVolumeMinSourceChapters  = 8
	adaptationVolumeMinSourceRunes     = 40_000
)

func normalWorkflowSteps(snapshot host.UISnapshot, coCreate *webCoCreateState) []WorkflowStep {
	steps := []WorkflowStep{
		{ID: "creative_intent", Label: "创意输入", Status: WorkflowStatusIdle},
		{ID: "structure", Label: "篇幅与结构", Status: WorkflowStatusIdle},
		{ID: "clarification", Label: "澄清决策", Status: WorkflowStatusIdle},
		{ID: "foundation", Label: "完整设定确认", Status: WorkflowStatusIdle},
	}
	if normalWorkflowUsesVolumePlan(snapshot, coCreate) {
		steps = append(steps, WorkflowStep{ID: "volume_plan", Label: "分卷规划与审核", Status: WorkflowStatusIdle})
	}
	return append(steps,
		WorkflowStep{ID: "chapter_outline", Label: "章节细纲与审核", Status: WorkflowStatusIdle},
		WorkflowStep{ID: "writing", Label: "正文创作", Status: WorkflowStatusIdle, Current: snapshot.CompletedCount, Total: snapshot.TotalChapters},
	)
}

func normalWorkflowUsesVolumePlan(snapshot host.UISnapshot, coCreate *webCoCreateState) bool {
	if snapshot.Layered || len(snapshot.LayeredOutline) > 0 {
		return true
	}
	if review := snapshot.PlanningReview; review != nil {
		if review.Kind == domain.PlanningReviewKindVolumeSplit {
			return true
		}
		if review.TargetTotalWords > 0 {
			return review.TargetTotalWords >= normalLayeredPlanningWordThreshold
		}
		if review.Kind == domain.PlanningReviewKindChapterOutline {
			return false
		}
	}
	if coCreate != nil && coCreate.TargetTotalWords > 0 {
		return coCreate.TargetTotalWords >= normalLayeredPlanningWordThreshold
	}
	// Until scale is known, retain the possible long-form gate. Once the user
	// chooses a short/mid target, the step disappears instead of being shown as
	// a stage the backend will never execute.
	return true
}

func normalWorkflowProgress(projectID string, snapshot host.UISnapshot, coCreate *webCoCreateState) WorkflowProgress {
	usesVolumePlan := normalWorkflowUsesVolumePlan(snapshot, coCreate)
	steps := normalWorkflowSteps(snapshot, coCreate)
	revision := normalWorkflowRevision(snapshot, coCreate)
	progress := WorkflowProgress{
		Workflow: workflowNormal,
		RunID:    workflowRunID(projectID, workflowNormal),
		Revision: revision,
		Status:   WorkflowStatusIdle,
		Steps:    steps,
	}

	if characterConfirmationRequired(snapshot.CharacterWorkflow) {
		progress.CurrentStep = "foundation"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.NextAction = nextWorkflowAction(
			progress,
			"confirm_character_candidate",
			"确认角色卡并继续",
			true,
		)
		progress.Steps = completeStepsBefore(steps, progress.CurrentStep)
		progress.Steps = setStep(
			progress.Steps,
			progress.CurrentStep,
			progress.Status,
			0,
			0,
			"角色卡已审核通过，确认后才会发布完整设定并进入下一阶段",
		)
		return progress
	}

	// The durable planning checkpoint supersedes stale co-create transport
	// state. A previous co-create request may have failed or been cancelled
	// after its accepted brief already advanced into volume/detail planning;
	// showing that old error here would send the user back to clarification
	// even though a reviewed proposal is waiting for confirmation.
	if review := snapshot.PlanningReview; review != nil &&
		(review.Status == domain.PlanningReviewStatusPending || review.Status == domain.PlanningReviewStatusCollecting) {
		progress.CurrentStep, progress.Status, progress.NextAction = normalPlanningReviewProgress(progress, review, usesVolumePlan)
		if review.Status == domain.PlanningReviewStatusCollecting && !snapshot.IsRunning {
			progress.Status = WorkflowStatusPaused
			progress.Recoverable = true
			progress.Error = "规划任务已中断，可从检查点恢复"
			progress.NextAction = nextWorkflowAction(progress, "resume_project", "恢复规划", false)
		}
		progress.Steps = completeStepsBefore(steps, progress.CurrentStep)
		message := "正在生成规划"
		if progress.Error != "" {
			message = progress.Error
		} else if progress.Status == WorkflowStatusWaitingConfirmation {
			message = "规划已生成，等待审核"
		} else if review.Kind == domain.PlanningReviewKindVolumeSplit {
			message = "正在逐弧生成细纲并进行原创质量审核"
		}
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, message)
		return progress
	}

	if coCreate != nil && (coCreate.Kind == webCoCreateKindNormal || coCreate.Kind == webCoCreateKindStage) {
		progress.CurrentStep = "clarification"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(steps, "clarification")
		progress.Steps = setStep(progress.Steps, "clarification", progress.Status, 0, 0, coCreate.BlockedReason)
		switch {
		case coCreate.Failed:
			progress.Status = WorkflowStatusFailed
			progress.Recoverable = true
			progress.Error = "普通共创生成失败，可重试后继续"
			progress.Steps = setStep(progress.Steps, "clarification", WorkflowStatusFailed, 0, 0, progress.Error)
			progress.NextAction = nextWorkflowAction(progress, "retry_cocreate", "重试共创", false)
		case coCreate.CanStart:
			progress.CurrentStep = "chapter_outline"
			if usesVolumePlan {
				progress.CurrentStep = "volume_plan"
			}
			progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusWaitingConfirmation, 0, 0, "共创方向已就绪，确认后保存并进入正式规划")
			progress.NextAction = nextWorkflowAction(progress, "commit_cocreate", "完成共创", true)
		default:
			progress.NextAction = nextWorkflowAction(progress, "continue_cocreate", "继续共创", true)
		}
		return progress
	}

	applyWritingState(&progress, snapshot)
	if progress.Status == WorkflowStatusIdle {
		progress.CurrentStep = "creative_intent"
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusIdle, 0, 0, "输入创意后开始共创")
		progress.NextAction = nextWorkflowAction(progress, "begin_cocreate", "开始普通共创", false)
	}
	return progress
}

func characterConfirmationRequired(workflow *host.CharacterWorkflowSummary) bool {
	return workflow != nil &&
		workflow.AnalysisStatus == domain.CharacterCardAnalysisCandidateReady &&
		workflow.ReviewStatus == domain.CharacterCardReviewPassed &&
		workflow.ConfirmationStatus == domain.CharacterCardUnconfirmed
}

func normalPlanningReviewProgress(progress WorkflowProgress, review *host.PlanningReviewSummary, usesVolumePlan bool) (string, WorkflowStatus, *WorkflowNextAction) {
	status := WorkflowStatusRunning
	if review.Status == domain.PlanningReviewStatusPending {
		status = WorkflowStatusWaitingConfirmation
	}
	switch review.Kind {
	case domain.PlanningReviewKindFoundation:
		var action *WorkflowNextAction
		if status == WorkflowStatusWaitingConfirmation {
			action = nextWorkflowAction(progress, "confirm_foundation", "确认完整设定", true)
		}
		return "foundation", status, action
	case domain.PlanningReviewKindBlueprint:
		var action *WorkflowNextAction
		if status == WorkflowStatusWaitingConfirmation {
			action = nextWorkflowAction(progress, "confirm_planning", "生成详细提案", true)
		}
		if !usesVolumePlan {
			return "chapter_outline", status, action
		}
		return "volume_plan", status, action
	case domain.PlanningReviewKindVolumeSplit:
		if status == WorkflowStatusWaitingConfirmation {
			return "volume_plan", status, nextWorkflowAction(progress, "confirm_planning", "审核通过并生成章节细纲", true)
		}
		return "chapter_outline", status, nil
	default:
		var action *WorkflowNextAction
		if status == WorkflowStatusWaitingConfirmation {
			action = nextWorkflowAction(progress, "confirm_planning", "审核通过并开始创作", true)
		}
		return "chapter_outline", status, action
	}
}

func adaptationWorkflowSteps(snapshot host.UISnapshot, coCreate *webCoCreateState) []WorkflowStep {
	steps := []WorkflowStep{
		{ID: "source", Label: "原文导入", Status: WorkflowStatusIdle},
		{ID: "analysis", Label: "原文分析", Status: WorkflowStatusIdle},
		{ID: "contract", Label: "改编契约", Status: WorkflowStatusIdle},
		{ID: "target_foundation", Label: "目标设定", Status: WorkflowStatusIdle},
	}
	if adaptationWorkflowUsesVolumePlan(snapshot, coCreate) {
		steps = append(steps, WorkflowStep{ID: "volume_plan", Label: "分卷规划与审核", Status: WorkflowStatusIdle})
	}
	return append(steps,
		WorkflowStep{ID: "chapter_outline", Label: "章节细纲与审核", Status: WorkflowStatusIdle},
		WorkflowStep{ID: "writing", Label: "正文创作", Status: WorkflowStatusIdle, Current: snapshot.CompletedCount, Total: snapshot.TotalChapters},
		WorkflowStep{ID: "quality_audit", Label: "质量审计", Status: WorkflowStatusIdle},
	)
}

func adaptationWorkflowUsesVolumePlan(snapshot host.UISnapshot, coCreate *webCoCreateState) bool {
	if snapshot.AdaptationVolumeReview != nil {
		return true
	}
	if workflow := snapshot.AdaptationPlanningWorkflow; workflow != nil {
		switch workflow.Stage {
		case domain.AdaptationPlanningStageVolumeReviewPending, domain.AdaptationPlanningStageDetailsGenerating:
			return true
		}
	}

	plan := snapshot.AdaptationProposal
	if plan == nil {
		plan = snapshot.AdaptationPlan
	}
	granularity := ""
	if plan != nil {
		granularity = strings.TrimSpace(plan.Granularity)
	}
	if granularity == "" && coCreate != nil {
		granularity = strings.TrimSpace(coCreate.AdaptMode)
	}
	if granularity != "" && domain.NormalizeAdaptationGranularity(granularity) == domain.AdaptationGranularityChapter {
		return false
	}

	sourceChapterCount := 0
	if snapshot.AdaptationSourceFoundation != nil {
		sourceChapterCount = snapshot.AdaptationSourceFoundation.SourceChapterCount
	}
	if plan != nil {
		if len(plan.Chapters) >= adaptationVolumeMinTargetChapters ||
			plan.SourceTotalRunes >= adaptationVolumeMinSourceRunes ||
			adaptationPlanSourceChapterCount(plan) >= adaptationVolumeMinSourceChapters {
			return true
		}
	}
	if sourceChapterCount >= adaptationVolumeMinSourceChapters {
		return true
	}
	if coCreate != nil && coCreate.TargetTotalWords >= normalLayeredPlanningWordThreshold {
		return true
	}
	if plan != nil || sourceChapterCount > 0 || (coCreate != nil && coCreate.TargetTotalWords > 0) {
		return false
	}
	// Legacy or not-yet-sized projects keep the potential checkpoint visible
	// until source scale and granularity make the direct path explicit.
	return true
}

func adaptationPlanSourceChapterCount(plan *domain.AdaptationPlan) int {
	if plan == nil {
		return 0
	}
	maxChapter := 0
	for _, event := range plan.SourceEvents {
		maxChapter = max(maxChapter, event.SourceChapter)
	}
	for _, chapter := range plan.Chapters {
		maxChapter = max(maxChapter, chapter.SourceRange.To)
		for _, sourceChapter := range chapter.SourceChapters {
			maxChapter = max(maxChapter, sourceChapter)
		}
	}
	return maxChapter
}

func adaptationWorkflowProgress(
	projectID string,
	snapshot host.UISnapshot,
	coCreate *webCoCreateState,
	latest *APIHostEvent,
	actionKinds []string,
) WorkflowProgress {
	steps := adaptationWorkflowSteps(snapshot, coCreate)
	revision := adaptationWorkflowRevision(snapshot, coCreate, latest)
	progress := WorkflowProgress{
		Workflow: workflowAdaptation,
		RunID:    workflowRunID(projectID, workflowAdaptation),
		Revision: revision,
		Status:   WorkflowStatusIdle,
		Steps:    steps,
	}

	if currentStep, message, running := adaptationRunningPresentation(actionKinds, snapshot, coCreate); running {
		progress.Status = WorkflowStatusRunning
		progress.CurrentStep = currentStep
		progress.Steps = completeStepsBefore(progress.Steps, currentStep)
		current, total := 0, 0
		if latest != nil && latest.Total > 0 {
			current, total = latest.Current, latest.Total
			if strings.TrimSpace(latest.Summary) != "" {
				message = latest.Summary
			}
		}
		progress.Steps = setStep(progress.Steps, currentStep, progress.Status, current, total, message)
		return progress
	}

	if coCreate != nil && coCreate.Kind == webCoCreateKindAdapt {
		progress.CurrentStep = "contract"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, coCreate.BlockedReason)
		switch {
		case coCreate.Failed:
			progress.Status = WorkflowStatusFailed
			progress.Recoverable = true
			progress.Error = "改编共创生成失败，可重试后继续"
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusFailed, 0, 0, progress.Error)
			progress.NextAction = nextWorkflowAction(progress, "retry_cocreate", "重试改编共创", false)
		case coCreate.CanStart:
			progress.NextAction = nextWorkflowAction(progress, "commit_adaptation_contract", "确认契约并生成目标设定", true)
		default:
			progress.NextAction = nextWorkflowAction(progress, "continue_adaptation_cocreate", "继续改编共创", true)
		}
		return progress
	}
	if review := snapshot.AdaptationFoundationReview; review != nil &&
		(review.State == domain.AdaptationFoundationReviewPending || review.State == domain.AdaptationFoundationReviewGenerating || review.State == domain.AdaptationFoundationReviewReadonly) {
		progress.CurrentStep = "target_foundation"
		progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
		progress.Status = WorkflowStatusWaitingConfirmation
		message := "目标 StoryFoundation 等待审核"
		switch review.State {
		case domain.AdaptationFoundationReviewGenerating:
			progress.Status, message = WorkflowStatusRunning, "正在生成目标 StoryFoundation"
		case domain.AdaptationFoundationReviewReadonly:
			message = "目标 StoryFoundation 已因正文存在而只读：" + review.ReadonlyReason
		}
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, message)
		if review.State == domain.AdaptationFoundationReviewPending {
			progress.NextAction = nextWorkflowAction(progress, "confirm_foundation", "确认目标 StoryFoundation", true)
		}
		return progress
	}

	proposal := snapshot.AdaptationProposal
	if proposal == nil {
		proposal = snapshot.AdaptationPlan
	}
	planningStep := adaptationPlanningStep(snapshot, coCreate)
	if latest != nil && planningStep != "" {
		stepStatus := workflowStatusFromAdaptationEvent(*latest)
		if stepStatus == WorkflowStatusFailed || stepStatus == WorkflowStatusPaused {
			progress.CurrentStep = planningStep
			progress.Status = stepStatus
			progress.Steps = completeStepsBefore(steps, progress.CurrentStep)
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, stepStatus, latest.Current, latest.Total, latest.Summary)
			progress.Recoverable = true
			progress.Error = strings.TrimSpace(latest.Detail)
			actionID, actionLabel := "resume_adaptation_proposal_details", "继续章节详细提案"
			if planningStep == "volume_plan" {
				actionID, actionLabel = "resume_adaptation_proposal", "继续分卷规划"
			}
			progress.NextAction = nextWorkflowAction(progress, actionID, actionLabel, false)
			return progress
		}
	}

	if workflow := snapshot.AdaptationPlanningWorkflow; workflow != nil {
		switch workflow.Stage {
		case domain.AdaptationPlanningStageSkeletonGenerating:
			progress.CurrentStep = planningStep
			if !workflowContainsString(actionKinds, projectActionKindAdaptationProposal) {
				progress.Status = WorkflowStatusPaused
				progress.Recoverable = true
				progress.Error = "adaptation proposal generation was interrupted"
				progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
				progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "分卷规划生成已中断，可从持久化进度继续")
				progress.NextAction = nextWorkflowAction(progress, "resume_adaptation_proposal", "继续分卷规划", false)
				return progress
			}
			progress.Status = WorkflowStatusRunning
			progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
			message := "正在生成分卷规划"
			if progress.CurrentStep == "chapter_outline" {
				message = "当前规模无需分卷，正在生成章节细纲"
			}
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, message)
			return progress
		case domain.AdaptationPlanningStageVolumeReviewPending:
			progress.CurrentStep = "volume_plan"
			progress.Status = WorkflowStatusWaitingConfirmation
			progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "分卷规划已生成，等待审核")
			progress.NextAction = nextWorkflowAction(progress, "confirm_adaptation_proposal", "审核分卷并生成章节细纲", true)
			return progress
		case domain.AdaptationPlanningStageDetailsGenerating:
			progress.CurrentStep = "chapter_outline"
			if !workflowContainsString(actionKinds, projectActionKindAdaptationProposal) {
				progress.Status = WorkflowStatusPaused
				progress.Recoverable = true
				progress.Error = "adaptation proposal detail generation was interrupted"
				progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
				progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "章节细纲生成已中断，可从持久化批次继续")
				progress.NextAction = nextWorkflowAction(progress, "resume_adaptation_proposal_details", "继续章节详细提案", false)
				return progress
			}
			progress.Status = WorkflowStatusRunning
			progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "分卷已通过，正在生成并审核章节细纲")
			return progress
		case domain.AdaptationPlanningStageProposalReviewPending:
			progress.CurrentStep = "chapter_outline"
			progress.Status = WorkflowStatusWaitingConfirmation
			progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "章节细纲已生成，等待审核")
			progress.NextAction = nextWorkflowAction(progress, "confirm_adaptation_proposal", "确认章节细纲", true)
			return progress
		}
	}

	if latest != nil {
		progress.CurrentStep = "analysis"
		progress.Steps = completeStepsBefore(steps, "analysis")
		stepStatus := workflowStatusFromAdaptationEvent(*latest)
		progress.Status = stepStatus
		progress.Steps = setStep(progress.Steps, "analysis", stepStatus, latest.Current, latest.Total, latest.Summary)
		if stepStatus == WorkflowStatusFailed || stepStatus == WorkflowStatusPaused {
			progress.Recoverable = true
			progress.Error = strings.TrimSpace(latest.Detail)
			progress.NextAction = nextWorkflowAction(progress, "resume_analysis", "继续原文分析", false)
			return progress
		}
	}

	if snapshot.AdaptationVolumeReview != nil {
		progress.CurrentStep = "volume_plan"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "分卷规划已生成，等待审核")
		progress.NextAction = nextWorkflowAction(progress, "confirm_adaptation_proposal", "审核分卷并生成章节细纲", true)
		return progress
	}

	if proposal != nil && proposal.Status != domain.AdaptationPlanStatusConfirmed {
		progress.CurrentStep = "chapter_outline"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "章节细纲已生成，等待审核")
		progress.NextAction = nextWorkflowAction(progress, "confirm_adaptation_proposal", "确认章节细纲", true)
		return progress
	}

	if proposal != nil && proposal.Status == domain.AdaptationPlanStatusConfirmed {
		progress.Steps = completeStepsBefore(progress.Steps, "writing")
		applyWritingState(&progress, snapshot)
		if progress.Status == WorkflowStatusCompleted {
			progress.CurrentStep = "quality_audit"
			progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusCompleted, 1, 1, "改编创作与质量检查已完成")
		}
		return progress
	}

	if latest != nil && latest.Kind == "done" {
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.CurrentStep = "contract"
		progress.Steps = completeStepsBefore(progress.Steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, "原文分析完成，等待确定改编契约")
		progress.NextAction = nextWorkflowAction(progress, "begin_adaptation_cocreate", "开始改编共创", false)
		return progress
	}

	progress.CurrentStep = "source"
	progress.NextAction = nextWorkflowAction(progress, "upload_adaptation_source", "上传原文", false)
	return progress
}

func adaptationPlanningStep(snapshot host.UISnapshot, coCreate *webCoCreateState) string {
	if workflow := snapshot.AdaptationPlanningWorkflow; workflow != nil {
		switch workflow.Stage {
		case domain.AdaptationPlanningStageVolumeReviewPending:
			return "volume_plan"
		case domain.AdaptationPlanningStageDetailsGenerating, domain.AdaptationPlanningStageProposalReviewPending:
			return "chapter_outline"
		case domain.AdaptationPlanningStageSkeletonGenerating:
			if adaptationWorkflowUsesVolumePlan(snapshot, coCreate) {
				return "volume_plan"
			}
			return "chapter_outline"
		}
	}
	if snapshot.AdaptationVolumeReview != nil {
		return "volume_plan"
	}
	if snapshot.AdaptationProposal != nil {
		return "chapter_outline"
	}
	return ""
}

func adaptationRunningPresentation(actionKinds []string, snapshot host.UISnapshot, coCreate *webCoCreateState) (string, string, bool) {
	switch {
	case workflowContainsString(actionKinds, projectActionKindAdaptationRevision):
		if adaptationPlanningStep(snapshot, coCreate) == "volume_plan" {
			return "volume_plan", "正在修订分卷规划", true
		}
		return "chapter_outline", "正在修订章节细纲", true
	case workflowContainsString(actionKinds, projectActionKindAdaptationProposal):
		if adaptationPlanningStep(snapshot, coCreate) == "chapter_outline" {
			return "chapter_outline", "正在生成并逐批审核章节细纲", true
		}
		if adaptationWorkflowUsesVolumePlan(snapshot, coCreate) {
			return "volume_plan", "正在生成分卷规划", true
		}
		return "chapter_outline", "当前规模无需分卷，正在生成章节细纲", true
	case workflowContainsString(actionKinds, projectActionKindAdaptationAnalysis):
		return "analysis", "正在分析原文", true
	default:
		return "", "", false
	}
}

func continuationWorkflowProgress(
	projectID string,
	snapshot host.UISnapshot,
	coCreate *webCoCreateState,
	continuation *domain.ContinuationSnapshot,
) WorkflowProgress {
	steps := []WorkflowStep{
		{ID: "source_baseline", Label: "原作基线", Status: WorkflowStatusIdle},
		{ID: "draft", Label: "续写方向", Status: WorkflowStatusIdle},
		{ID: "proposal", Label: "续写提案", Status: WorkflowStatusIdle},
		{ID: "volumes", Label: "分卷规划", Status: WorkflowStatusIdle},
		{ID: "outlines", Label: "章节细纲", Status: WorkflowStatusIdle},
		{ID: "writing", Label: "续写正文", Status: WorkflowStatusIdle, Current: snapshot.CompletedCount, Total: snapshot.TotalChapters},
	}
	if continuation == nil {
		progress := WorkflowProgress{
			Workflow:    workflowContinuation,
			RunID:       workflowRunID(projectID, workflowContinuation, "uninitialized"),
			Status:      WorkflowStatusIdle,
			CurrentStep: "source_baseline",
			Steps:       steps,
		}
		progress.NextAction = nextWorkflowAction(progress, "upload_continuation_source", "导入原作", false)
		return progress
	}

	workflow := continuation.Workflow
	progress := WorkflowProgress{
		Workflow: workflowContinuation,
		RunID:    workflowRunID(projectID, workflowContinuation, workflow.SourceSignature),
		Revision: workflow.Revision,
		Status:   WorkflowStatusIdle,
		Steps:    steps,
	}
	if coCreate != nil && coCreate.Kind == webCoCreateKindContinuation {
		progress.CurrentStep = "draft"
		progress.Status = WorkflowStatusWaitingConfirmation
		progress.Steps = completeStepsBefore(steps, progress.CurrentStep)
		progress.Steps = setStep(progress.Steps, progress.CurrentStep, progress.Status, 0, 0, coCreate.BlockedReason)
		switch {
		case coCreate.Failed:
			progress.Status = WorkflowStatusFailed
			progress.Recoverable = true
			progress.Error = "续写方向生成失败，可重试后继续"
			progress.Steps = setStep(progress.Steps, progress.CurrentStep, WorkflowStatusFailed, 0, 0, progress.Error)
			progress.NextAction = nextWorkflowAction(progress, "retry_cocreate", "重试续写共创", false)
		case coCreate.CanStart:
			progress.NextAction = nextWorkflowAction(progress, "commit_continuation_draft", "确认续写方向", true)
		default:
			progress.NextAction = nextWorkflowAction(progress, "continue_continuation_cocreate", "继续续写共创", true)
		}
		return progress
	}

	currentStep, status, message, nextID, nextLabel, confirmation := continuationStagePresentation(workflow)
	progress.CurrentStep = currentStep
	progress.Status = status
	progress.Steps = completeStepsBefore(steps, currentStep)
	progress.Steps = setStep(progress.Steps, currentStep, status, continuationWritingCurrent(snapshot, workflow), continuationWritingTotal(snapshot, workflow), message)
	progress.Error = strings.TrimSpace(workflow.LastError)
	progress.Recoverable = status == WorkflowStatusPaused || status == WorkflowStatusFailed
	if nextID != "" {
		progress.NextAction = nextWorkflowAction(progress, nextID, nextLabel, confirmation)
	}
	return progress
}

func continuationStagePresentation(workflow domain.ContinuationWorkflow) (string, WorkflowStatus, string, string, string, bool) {
	switch workflow.Stage {
	case domain.ContinuationStageSourceReady:
		return "source_baseline", WorkflowStatusWaitingConfirmation, "原作已导入，等待确定续写方向", "begin_continuation_draft", "开始续写共创", false
	case domain.ContinuationStageDraftCollecting:
		return "draft", WorkflowStatusWaitingConfirmation, "正在确定续写方向", "continue_continuation_cocreate", "继续续写共创", true
	case domain.ContinuationStageProposalGenerating:
		return "proposal", WorkflowStatusRunning, "正在生成续写提案", "", "", false
	case domain.ContinuationStageProposalReviewPending:
		return "proposal", WorkflowStatusWaitingConfirmation, "续写提案等待确认", "approve_continuation_proposal", "确认续写提案", true
	case domain.ContinuationStageVolumeReviewPending:
		return "volumes", WorkflowStatusWaitingConfirmation, "分卷规划等待确认", "approve_continuation_volumes", "确认分卷规划", true
	case domain.ContinuationStageOutlineGenerating:
		return "outlines", WorkflowStatusRunning, "正在生成章节细纲", "", "", false
	case domain.ContinuationStageOutlineReviewPending:
		return "outlines", WorkflowStatusWaitingConfirmation, "章节细纲等待确认", "approve_continuation_outlines", "确认章节细纲", true
	case domain.ContinuationStageReadyToWrite:
		return "writing", WorkflowStatusWaitingConfirmation, "续写规划已就绪，等待开始创作", "start_continuation", "开始续写", true
	case domain.ContinuationStageWriting:
		return "writing", WorkflowStatusRunning, "正在创作续写正文", "", "", false
	case domain.ContinuationStagePaused:
		return continuationResumeStep(workflow.ResumeStage), WorkflowStatusPaused, "续写流程已暂停", "retry_continuation", "继续续写流程", false
	case domain.ContinuationStageFailed:
		return continuationResumeStep(workflow.ResumeStage), WorkflowStatusFailed, "续写流程失败，可从检查点重试", "retry_continuation", "重试续写流程", false
	default:
		return "source_baseline", WorkflowStatusIdle, "", "upload_continuation_source", "导入原作", false
	}
}

func continuationResumeStep(stage domain.ContinuationStage) string {
	switch stage {
	case domain.ContinuationStageDraftCollecting:
		return "draft"
	case domain.ContinuationStageVolumeReviewPending:
		return "volumes"
	case domain.ContinuationStageOutlineGenerating, domain.ContinuationStageOutlineReviewPending:
		return "outlines"
	case domain.ContinuationStageReadyToWrite, domain.ContinuationStageWriting:
		return "writing"
	default:
		return "proposal"
	}
}

func applyWritingState(progress *WorkflowProgress, snapshot host.UISnapshot) {
	if progress == nil {
		return
	}
	progress.CurrentStep = "writing"
	status := WorkflowStatusIdle
	message := ""
	if !snapshotHasWritingEvidence(snapshot) {
		progress.Status = status
		progress.Steps = setStep(progress.Steps, "writing", status, 0, 0, message)
		return
	}
	switch {
	case snapshot.RuntimeState == "completed" || snapshot.Phase == string(domain.PhaseComplete):
		status = WorkflowStatusCompleted
		message = "创作已完成"
	case snapshot.RuntimeState == "paused" || snapshot.RuntimeState == "pausing":
		status = WorkflowStatusPaused
		message = "创作已暂停，可从检查点继续"
		progress.Recoverable = true
	case snapshot.IsRunning || snapshot.RuntimeState == "running":
		status = WorkflowStatusRunning
		message = "正在创作正文"
	case snapshot.Phase == string(domain.PhaseWriting):
		status = WorkflowStatusPaused
		message = "创作可继续"
		progress.Recoverable = true
	}
	progress.Status = status
	if status != WorkflowStatusIdle {
		progress.Steps = completeStepsBefore(progress.Steps, "writing")
	}
	progress.Steps = setStep(progress.Steps, "writing", status, snapshot.CompletedCount, snapshot.TotalChapters, message)
	if status == WorkflowStatusPaused {
		progress.NextAction = nextWorkflowAction(*progress, "resume_writing", "继续创作", false)
	}
}

func snapshotHasWritingEvidence(snapshot host.UISnapshot) bool {
	return snapshot.Phase == string(domain.PhaseWriting) ||
		snapshot.Phase == string(domain.PhaseComplete) ||
		snapshot.CurrentChapter > 0 ||
		snapshot.TotalChapters > 0 ||
		snapshot.CompletedCount > 0 ||
		snapshot.TotalWordCount > 0 ||
		snapshot.InProgressChapter > 0
}

func nextWorkflowAction(progress WorkflowProgress, id, label string, confirmation bool) *WorkflowNextAction {
	action := &WorkflowNextAction{
		ID:                   id,
		Label:                label,
		ExpectedRevision:     progress.Revision,
		RequiresConfirmation: confirmation,
	}
	action.IdempotencyKey = workflowIdempotencyKey(progress.RunID, progress.Revision, id)
	return action
}

func completeStepsBefore(steps []WorkflowStep, current string) []WorkflowStep {
	out := append([]WorkflowStep(nil), steps...)
	for i := range out {
		if out[i].ID == current {
			break
		}
		out[i].Status = WorkflowStatusCompleted
	}
	return out
}

func setStep(steps []WorkflowStep, id string, status WorkflowStatus, current, total int, message string) []WorkflowStep {
	out := append([]WorkflowStep(nil), steps...)
	for i := range out {
		if out[i].ID != id {
			continue
		}
		out[i].Status = status
		out[i].Current = current
		out[i].Total = total
		out[i].Message = strings.TrimSpace(message)
		break
	}
	return out
}

func normalWorkflowRevision(snapshot host.UISnapshot, coCreate *webCoCreateState) int {
	revision := snapshot.CompletedCount
	if snapshot.PlanningReview != nil {
		revision++
	}
	if coCreate != nil {
		revision += len(coCreate.Messages) + 1
	}
	return revision
}

func adaptationWorkflowRevision(snapshot host.UISnapshot, coCreate *webCoCreateState, event *APIHostEvent) int {
	revision := snapshot.CompletedCount
	if coCreate != nil {
		revision += len(coCreate.Messages) + 1
	}
	if event != nil {
		revision += event.Current
		if event.Total > 0 {
			revision++
		}
	}
	if snapshot.AdaptationProposal != nil || snapshot.AdaptationPlan != nil {
		revision++
	}
	return revision
}

func workflowRunID(projectID, workflow string, identity ...string) string {
	parts := append([]string{projectID, workflow}, identity...)
	return "wf_" + shortWorkflowHash(parts...)
}

func workflowIdempotencyKey(runID string, revision int, action string) string {
	return "idem_" + shortWorkflowHash(runID, fmt.Sprintf("%d", revision), action)
}

func shortWorkflowHash(parts ...string) string {
	canonical := make([]string, 0, len(parts))
	for _, part := range parts {
		canonical = append(canonical, strings.TrimSpace(part))
	}
	sum := sha256.Sum256([]byte(strings.Join(canonical, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func workflowStatusFromAdaptationEvent(event APIHostEvent) WorkflowStatus {
	if event.Failed || event.Level == "error" || event.Kind == "error" {
		return WorkflowStatusFailed
	}
	switch event.Kind {
	case "paused":
		return WorkflowStatusPaused
	case "done":
		return WorkflowStatusCompleted
	default:
		return WorkflowStatusRunning
	}
}

func continuationWritingCurrent(snapshot host.UISnapshot, workflow domain.ContinuationWorkflow) int {
	if workflow.Stage != domain.ContinuationStageWriting {
		return 0
	}
	completed := snapshot.CompletedCount - workflow.BaseChapterCount
	if completed < 0 {
		return 0
	}
	return completed
}

func continuationWritingTotal(snapshot host.UISnapshot, workflow domain.ContinuationWorkflow) int {
	if workflow.Stage != domain.ContinuationStageWriting || snapshot.TotalChapters <= workflow.BaseChapterCount {
		return 0
	}
	return snapshot.TotalChapters - workflow.BaseChapterCount
}

func workflowContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *ProjectSession) latestAdaptationProgressEvent() *APIHostEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.history) - 1; i >= 0; i-- {
		event := s.history[i].Event
		if event == nil || event.Category != "ADAPT" {
			continue
		}
		copy := *event
		return &copy
	}
	return nil
}
