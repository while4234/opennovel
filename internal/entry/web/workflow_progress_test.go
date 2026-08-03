package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

func TestWorkflowStatusesUseUnifiedWireValues(t *testing.T) {
	got := []WorkflowStatus{
		WorkflowStatusIdle,
		WorkflowStatusRunning,
		WorkflowStatusWaitingConfirmation,
		WorkflowStatusPaused,
		WorkflowStatusFailed,
		WorkflowStatusCompleted,
	}
	want := []WorkflowStatus{
		"idle",
		"running",
		"waiting_confirmation",
		"paused",
		"failed",
		"completed",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalWorkflowProgressWaitsForPlanningConfirmation(t *testing.T) {
	snapshot := host.UISnapshot{
		NovelName: "test novel",
		PlanningReview: &host.PlanningReviewSummary{
			Status: domain.PlanningReviewStatusPending,
			Kind:   domain.PlanningReviewKindVolumeSplit,
		},
	}
	progress := normalWorkflowProgress("project-normal", snapshot, nil)

	if progress.Workflow != workflowNormal || progress.Status != WorkflowStatusWaitingConfirmation {
		t.Fatalf("progress = %+v", progress)
	}
	if progress.CurrentStep != "volume_plan" {
		t.Fatalf("current step = %q", progress.CurrentStep)
	}
	if progress.NextAction == nil || progress.NextAction.ID != "confirm_planning" {
		t.Fatalf("next action = %+v", progress.NextAction)
	}
	if progress.NextAction.IdempotencyKey == "" {
		t.Fatal("idempotency key is empty")
	}
}

func TestNormalWorkflowProgressPresentsFoundationCheckpointBeforeFormalOutline(t *testing.T) {
	progress := normalWorkflowProgress("project-foundation", host.UISnapshot{
		PlanningReview: &host.PlanningReviewSummary{
			Status: domain.PlanningReviewStatusPending,
			Kind:   domain.PlanningReviewKindFoundation,
		},
	}, nil)
	if progress.CurrentStep != "foundation" || progress.Status != WorkflowStatusWaitingConfirmation {
		t.Fatalf("foundation progress = step %q status %q", progress.CurrentStep, progress.Status)
	}
	if progress.NextAction == nil || progress.NextAction.ID != "confirm_foundation" || !progress.NextAction.RequiresConfirmation {
		t.Fatalf("foundation next action = %+v", progress.NextAction)
	}
	if got := currentWorkflowModelStage("foundation"); got != bootstrap.StageSkeleton {
		t.Fatalf("foundation model stage = %q, want %q", got, bootstrap.StageSkeleton)
	}
}

func TestNormalWorkflowProgressPlanningReviewSupersedesStaleCoCreateFailure(t *testing.T) {
	snapshot := host.UISnapshot{PlanningReview: &host.PlanningReviewSummary{
		Status:           domain.PlanningReviewStatusPending,
		Kind:             domain.PlanningReviewKindChapterOutline,
		TargetTotalWords: 8_000,
	}}
	coCreate := &webCoCreateState{
		Kind:   webCoCreateKindNormal,
		Failed: true,
	}

	progress := normalWorkflowProgress("project-normal", snapshot, coCreate)

	if progress.Status != WorkflowStatusWaitingConfirmation || progress.CurrentStep != "chapter_outline" {
		t.Fatalf("status/step = %q/%q", progress.Status, progress.CurrentStep)
	}
	if progress.NextAction == nil || progress.NextAction.ID != "confirm_planning" {
		t.Fatalf("next action = %+v", progress.NextAction)
	}
	if progress.Error != "" || progress.Recoverable {
		t.Fatalf("stale co-create failure leaked into planning progress: %+v", progress)
	}
	if workflowStepByID(progress.Steps, "volume_plan") != nil {
		t.Fatalf("short original workflow unexpectedly includes volume planning: %+v", progress.Steps)
	}
}

func TestNormalWorkflowProgressKeepsVolumeAndChapterReviewsSeparateForLongForm(t *testing.T) {
	progress := normalWorkflowProgress("project-long", host.UISnapshot{
		PlanningReview: &host.PlanningReviewSummary{
			Status:           domain.PlanningReviewStatusPending,
			Kind:             domain.PlanningReviewKindVolumeSplit,
			TargetTotalWords: 100_000,
		},
	}, nil)

	volume := workflowStepByID(progress.Steps, "volume_plan")
	chapter := workflowStepByID(progress.Steps, "chapter_outline")
	if progress.CurrentStep != "volume_plan" || volume == nil || chapter == nil {
		t.Fatalf("long workflow does not expose both planning gates: %+v", progress)
	}
	if volume.Status != WorkflowStatusWaitingConfirmation || chapter.Status != WorkflowStatusIdle {
		t.Fatalf("volume/chapter status = %q/%q, want waiting/idle", volume.Status, chapter.Status)
	}
}

func TestWorkflowProgressReportsTheRunningStageModel(t *testing.T) {
	session, err := NewProjectSession(ProjectManifest{ID: "project-model-progress"}, newFakeProjectHost())
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	progress := session.workflowProgress(host.UISnapshot{
		IsRunning:      true,
		RuntimeState:   "running",
		TotalChapters:  10,
		CompletedCount: 2,
	})
	if progress.CurrentStep != "writing" || progress.CurrentProvider != "openrouter" || progress.CurrentModel != "model-a" {
		t.Fatalf("running progress = step %q route %q/%q, want writing/openrouter/model-a", progress.CurrentStep, progress.CurrentProvider, progress.CurrentModel)
	}
}

func TestWorkflowProgressReportsTheMostRecentlyWorkingAgentModel(t *testing.T) {
	fake := newFakeProjectHost()
	fake.currentModelSelections = map[string][2]string{
		"coordinator": {"deepseek-yuanyu-0", "deepseek-v4-pro"},
		bootstrap.StageRouteKey(bootstrap.StageWriting): {"deepseek-suifeng", "deepseek-v4-pro"},
	}
	session, err := NewProjectSession(ProjectManifest{ID: "project-active-agent-model"}, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	now := time.Now()
	progress := session.workflowProgress(host.UISnapshot{
		IsRunning:     true,
		RuntimeState:  "running",
		TotalChapters: 10,
		Agents: []host.AgentSnapshot{
			{Name: "coordinator", State: "working", UpdatedAt: now},
			{Name: "writer", State: "working", UpdatedAt: now.Add(time.Second)},
		},
	})

	if progress.CurrentAgent != "writer" || progress.CurrentProvider != "deepseek-suifeng" || progress.CurrentModel != "deepseek-v4-pro" {
		t.Fatalf("active route = agent %q route %q/%q, want writer/deepseek-suifeng/deepseek-v4-pro", progress.CurrentAgent, progress.CurrentProvider, progress.CurrentModel)
	}
}

func TestWorkflowProgressReportsCoordinatorModelWhenCoordinatorIsWorking(t *testing.T) {
	fake := newFakeProjectHost()
	fake.currentModelSelections = map[string][2]string{
		"coordinator": {"deepseek-yuanyu-0", "deepseek-v4-pro"},
		bootstrap.StageRouteKey(bootstrap.StageWriting): {"deepseek-suifeng", "deepseek-v4-pro"},
	}
	session, err := NewProjectSession(ProjectManifest{ID: "project-active-coordinator-model"}, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	progress := session.workflowProgress(host.UISnapshot{
		IsRunning:     true,
		RuntimeState:  "running",
		TotalChapters: 10,
		Agents: []host.AgentSnapshot{
			{Name: "writer", State: "idle", UpdatedAt: time.Now()},
			{Name: "coordinator", State: "working", UpdatedAt: time.Now().Add(time.Second)},
		},
	})

	if progress.CurrentAgent != "coordinator" || progress.CurrentProvider != "deepseek-yuanyu-0" || progress.CurrentModel != "deepseek-v4-pro" {
		t.Fatalf("active route = agent %q route %q/%q, want coordinator/deepseek-yuanyu-0/deepseek-v4-pro", progress.CurrentAgent, progress.CurrentProvider, progress.CurrentModel)
	}
}

func TestCurrentWorkflowModelStageCoversNormalAndAdaptationPlanning(t *testing.T) {
	tests := []struct {
		step string
		want string
	}{
		{step: "volume_plan", want: bootstrap.StageSkeleton},
		{step: "chapter_outline", want: bootstrap.StageDetailOutline},
		{step: "analysis", want: bootstrap.StageSourceAnalysis},
		{step: "contract", want: bootstrap.StageCoCreate},
		{step: "proposal_review", want: bootstrap.StageDetailOutline},
	}

	for _, test := range tests {
		t.Run(test.step, func(t *testing.T) {
			if got := currentWorkflowModelStage(test.step); got != test.want {
				t.Fatalf("currentWorkflowModelStage(%q) = %q, want %q", test.step, got, test.want)
			}
		})
	}
}

func TestWorkflowProgressReportsNormalPlanningModel(t *testing.T) {
	session, err := NewProjectSession(ProjectManifest{ID: "project-planning-model-progress"}, newFakeProjectHost())
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()

	progress := session.workflowProgress(host.UISnapshot{
		IsRunning:    true,
		RuntimeState: "running",
		PlanningReview: &host.PlanningReviewSummary{
			Status: domain.PlanningReviewStatusCollecting,
			Kind:   domain.PlanningReviewKindBlueprint,
		},
	})
	if progress.CurrentStep != "volume_plan" || progress.Status != WorkflowStatusRunning || progress.CurrentProvider != "openrouter" || progress.CurrentModel != "model-a" {
		t.Fatalf("planning progress = step %q status %q route %q/%q, want volume_plan/running/openrouter/model-a", progress.CurrentStep, progress.Status, progress.CurrentProvider, progress.CurrentModel)
	}
}

func TestNormalWorkflowProgressMarksInterruptedPlanningRecoverable(t *testing.T) {
	progress := normalWorkflowProgress("project-interrupted-planning", host.UISnapshot{
		RuntimeState: "idle",
		PlanningReview: &host.PlanningReviewSummary{
			Status: domain.PlanningReviewStatusCollecting,
			Kind:   domain.PlanningReviewKindBlueprint,
		},
	}, nil)

	if progress.Status != WorkflowStatusPaused || !progress.Recoverable {
		t.Fatalf("planning progress status/recoverable = %q/%v, want paused/true", progress.Status, progress.Recoverable)
	}
	if progress.Error == "" || progress.NextAction == nil || progress.NextAction.ID != "resume_project" {
		t.Fatalf("planning recovery metadata = error %q action %+v", progress.Error, progress.NextAction)
	}
	step := workflowStepByID(progress.Steps, "volume_plan")
	if step == nil || step.Status != WorkflowStatusPaused || step.Message != progress.Error {
		t.Fatalf("interrupted planning step = %+v, want paused with recovery message", step)
	}
}

func TestNormalWorkflowProgressCompletesPrerequisitesAfterWritingStarts(t *testing.T) {
	tests := []struct {
		name           string
		snapshot       host.UISnapshot
		expectedStatus WorkflowStatus
	}{
		{
			name: "running",
			snapshot: host.UISnapshot{
				IsRunning:     true,
				RuntimeState:  "running",
				Phase:         string(domain.PhaseWriting),
				TotalChapters: 12,
			},
			expectedStatus: WorkflowStatusRunning,
		},
		{
			name: "paused",
			snapshot: host.UISnapshot{
				RuntimeState:  "paused",
				Phase:         string(domain.PhaseWriting),
				TotalChapters: 12,
			},
			expectedStatus: WorkflowStatusPaused,
		},
		{
			name: "completed",
			snapshot: host.UISnapshot{
				RuntimeState:   "completed",
				Phase:          string(domain.PhaseComplete),
				CompletedCount: 12,
				TotalChapters:  12,
			},
			expectedStatus: WorkflowStatusCompleted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			progress := normalWorkflowProgress("project-writing", test.snapshot, nil)
			if progress.CurrentStep != "writing" || progress.Status != test.expectedStatus {
				t.Fatalf("progress step/status = %q/%q, want writing/%q", progress.CurrentStep, progress.Status, test.expectedStatus)
			}
			for _, stepID := range []string{"creative_intent", "structure", "clarification", "volume_plan", "chapter_outline"} {
				step := workflowStepByID(progress.Steps, stepID)
				if step == nil || step.Status != WorkflowStatusCompleted {
					t.Fatalf("prerequisite step %q = %+v, want completed", stepID, step)
				}
			}
			writing := workflowStepByID(progress.Steps, "writing")
			if writing == nil || writing.Status != test.expectedStatus {
				t.Fatalf("writing step = %+v, want status %q", writing, test.expectedStatus)
			}
		})
	}
}

func TestNormalWorkflowProgressKeepsNewProjectStepsIdle(t *testing.T) {
	progress := normalWorkflowProgress("project-new", host.UISnapshot{}, nil)
	if progress.CurrentStep != "creative_intent" || progress.Status != WorkflowStatusIdle {
		t.Fatalf("new project progress = %+v", progress)
	}
	for _, step := range progress.Steps {
		if step.Status != WorkflowStatusIdle {
			t.Fatalf("new project step %q status = %q, want idle", step.ID, step.Status)
		}
	}
}

func TestNormalWorkflowProgressDoesNotTreatBackgroundActionAsWriting(t *testing.T) {
	snapshot := host.UISnapshot{
		IsRunning:    true,
		RuntimeState: "running",
		Agents: []host.AgentSnapshot{{
			TaskKind: projectActionKindSimulationAnalysis,
			State:    "running",
		}},
	}

	progress := normalWorkflowProgress("project-simulation-analysis", snapshot, nil)

	if progress.CurrentStep != "creative_intent" || progress.Status != WorkflowStatusIdle {
		t.Fatalf("background analysis progress = %+v, want idle creative intent", progress)
	}
	for _, step := range progress.Steps {
		if step.Status != WorkflowStatusIdle {
			t.Fatalf("background analysis step %q status = %q, want idle", step.ID, step.Status)
		}
	}
}

func TestContinuationWorkflowProgressUsesDurableRevision(t *testing.T) {
	continuation := &domain.ContinuationSnapshot{Workflow: domain.ContinuationWorkflow{
		Stage:           domain.ContinuationStageProposalReviewPending,
		SourceSignature: "source-signature",
		Revision:        7,
	}}
	progress := continuationWorkflowProgress("project-continuation", host.UISnapshot{}, nil, continuation)

	if progress.Workflow != workflowContinuation || progress.Revision != 7 {
		t.Fatalf("progress = %+v", progress)
	}
	if progress.Status != WorkflowStatusWaitingConfirmation || progress.CurrentStep != "proposal" {
		t.Fatalf("status/step = %q/%q", progress.Status, progress.CurrentStep)
	}
	if progress.NextAction == nil || progress.NextAction.ExpectedRevision != 7 {
		t.Fatalf("next action = %+v", progress.NextAction)
	}
	wantKey := progress.NextAction.IdempotencyKey
	again := continuationWorkflowProgress("project-continuation", host.UISnapshot{}, nil, continuation)
	if again.NextAction == nil || again.NextAction.IdempotencyKey != wantKey {
		t.Fatalf("idempotency key changed: %q -> %+v", wantKey, again.NextAction)
	}
}

func TestAdaptationEventPreservesCurrentAndTotalInAPIEvent(t *testing.T) {
	session := &ProjectSession{
		manifest:    ProjectManifest{ID: "project-adaptation"},
		hostEventAt: make(map[string]int),
		subscribers: make(map[chan WebEvent]struct{}),
	}
	event := session.appendAdaptationEvent(apiAdaptationEvent{
		Time:    time.Now().UTC(),
		Stage:   string(adapt.StageChapter),
		Current: 3,
		Total:   12,
		Message: "analyzing source chapter",
	})

	if event.Event == nil {
		t.Fatal("API host event is nil")
	}
	if event.Event.Current != 3 || event.Event.Total != 12 {
		t.Fatalf("event progress = %d/%d, want 3/12", event.Event.Current, event.Event.Total)
	}
}

func TestAdaptationWorkflowProgressPrioritizesRunningProposalOverReviewArtifacts(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationProposal: &domain.AdaptationPlan{Status: domain.AdaptationPlanStatusProposal},
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
		t.Fatalf("running proposal unexpectedly requires confirmation: %+v", progress.NextAction)
	}
	step := workflowStepByID(progress.Steps, "chapter_outline")
	if step == nil || step.Status != WorkflowStatusRunning || step.Message != "正在生成并逐批审核章节细纲" {
		t.Fatalf("proposal step = %+v", step)
	}
}

func TestAdaptationWorkflowProgressKeepsDetailAuditFailureInProposalStage(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationVolumeReview: &domain.AdaptationVolumeReview{
			Status: domain.AdaptationPlanStatusVolumeReview,
		},
		AdaptationPlanningWorkflow: &domain.AdaptationPlanningWorkflow{
			Version:  domain.AdaptationPlanningWorkflowVersion,
			Stage:    domain.AdaptationPlanningStageDetailsGenerating,
			Revision: 4,
		},
	}
	latest := &APIHostEvent{
		Category: "ADAPT",
		Kind:     string(adapt.StageAudit),
		Level:    "error",
		Current:  123,
		Total:    370,
		Summary:  "章节详情审核失败",
		Detail:   "duplicate source event owner",
	}
	progress := adaptationWorkflowProgress("project-adaptation", snapshot, nil, latest, nil)

	if progress.Status != WorkflowStatusFailed || progress.CurrentStep != "chapter_outline" {
		t.Fatalf("status/step=%q/%q, want failed/chapter_outline", progress.Status, progress.CurrentStep)
	}
	step := workflowStepByID(progress.Steps, "chapter_outline")
	if step == nil || step.Status != WorkflowStatusFailed || step.Current != 123 || step.Total != 370 {
		t.Fatalf("proposal step=%+v", step)
	}
	if progress.NextAction == nil || progress.NextAction.ID != "resume_adaptation_proposal_details" || progress.NextAction.Label != "继续章节详细提案" {
		t.Fatalf("next action=%+v", progress.NextAction)
	}
	if progress.Error != latest.Detail {
		t.Fatalf("error=%q, want %q", progress.Error, latest.Detail)
	}
}

func TestAdaptationWorkflowProgressSeparatesVolumeReviewFromChapterOutlineReview(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationVolumeReview: &domain.AdaptationVolumeReview{
			Status:             domain.AdaptationPlanStatusVolumeReview,
			Granularity:        domain.AdaptationGranularityFree,
			TargetChapterCount: 24,
		},
		AdaptationPlanningWorkflow: &domain.AdaptationPlanningWorkflow{
			Version:  domain.AdaptationPlanningWorkflowVersion,
			Stage:    domain.AdaptationPlanningStageVolumeReviewPending,
			Revision: 3,
		},
	}
	progress := adaptationWorkflowProgress("project-adaptation", snapshot, nil, nil, nil)

	volume := workflowStepByID(progress.Steps, "volume_plan")
	chapter := workflowStepByID(progress.Steps, "chapter_outline")
	if progress.CurrentStep != "volume_plan" || progress.Status != WorkflowStatusWaitingConfirmation {
		t.Fatalf("volume review progress = %+v", progress)
	}
	if volume == nil || volume.Status != WorkflowStatusWaitingConfirmation || chapter == nil || chapter.Status != WorkflowStatusIdle {
		t.Fatalf("volume/chapter status = %+v/%+v", volume, chapter)
	}
	if progress.NextAction == nil || progress.NextAction.Label != "审核分卷并生成章节细纲" {
		t.Fatalf("volume next action = %+v", progress.NextAction)
	}
}

func TestAdaptationWorkflowProgressKeepsVolumeGateForLongSourceBeforePlanning(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationSourceFoundation: &domain.AdaptationSourceFoundation{SourceChapterCount: 17},
		AdaptationFoundationReview: &domain.AdaptationFoundationReview{State: domain.AdaptationFoundationReviewPending},
		AdaptationPlanningWorkflow: &domain.AdaptationPlanningWorkflow{
			Version:  domain.AdaptationPlanningWorkflowVersion,
			Stage:    domain.AdaptationPlanningStageFoundationReviewPending,
			Revision: 2,
		},
	}
	progress := adaptationWorkflowProgress("project-long-adaptation", snapshot, nil, nil, nil)

	if workflowStepByID(progress.Steps, "volume_plan") == nil || workflowStepByID(progress.Steps, "chapter_outline") == nil {
		t.Fatalf("long adaptation does not expose both planning gates: %+v", progress.Steps)
	}
	if progress.CurrentStep != "target_foundation" {
		t.Fatalf("current step = %q, want target_foundation", progress.CurrentStep)
	}
}

func TestAdaptationWorkflowProgressOmitsVolumeForDirectShortProposal(t *testing.T) {
	snapshot := host.UISnapshot{
		AdaptationProposal: &domain.AdaptationPlan{
			Status:      domain.AdaptationPlanStatusProposal,
			Granularity: domain.AdaptationGranularityFree,
			Chapters:    []domain.AdaptationChapterPlan{{Chapter: 1}, {Chapter: 2}},
		},
		AdaptationSourceFoundation: &domain.AdaptationSourceFoundation{SourceChapterCount: 2},
		AdaptationPlanningWorkflow: &domain.AdaptationPlanningWorkflow{
			Version:  domain.AdaptationPlanningWorkflowVersion,
			Stage:    domain.AdaptationPlanningStageProposalReviewPending,
			Revision: 2,
		},
	}
	progress := adaptationWorkflowProgress("project-short-adaptation", snapshot, nil, nil, nil)

	if workflowStepByID(progress.Steps, "volume_plan") != nil {
		t.Fatalf("direct short adaptation unexpectedly includes volume planning: %+v", progress.Steps)
	}
	if progress.CurrentStep != "chapter_outline" || progress.Status != WorkflowStatusWaitingConfirmation {
		t.Fatalf("direct short adaptation progress = %+v", progress)
	}
}

func workflowStepByID(steps []WorkflowStep, id string) *WorkflowStep {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
	}
	return nil
}

func TestWebSnapshotSerializesWorkflowProgressWithoutDroppingLegacyFields(t *testing.T) {
	snapshot := WebSnapshot{
		UISnapshot: host.UISnapshot{NovelName: "legacy novel"},
		WorkflowProgress: WorkflowProgress{
			Workflow: workflowNormal,
			RunID:    "wf_test",
			Status:   WorkflowStatusIdle,
			Steps:    []WorkflowStep{},
		},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := body["NovelName"]; !ok {
		t.Fatalf("legacy NovelName field missing: %s", data)
	}
	if _, ok := body["workflow_progress"]; !ok {
		t.Fatalf("workflow_progress missing: %s", data)
	}
}

func TestSnapshotSSECarriesWorkflowProgressAlongsideLegacySnapshot(t *testing.T) {
	fake := newFakeProjectHost()
	fake.snapshot = host.UISnapshot{
		NovelName:        "sse novel",
		PremiseFull:      strings.Repeat("正文", projectSnapshotSummaryRunes+1),
		CharacterDetails: []domain.Character{{Name: "角色"}},
		Outline: []host.OutlineSnapshot{{
			Chapter:   1,
			CoreEvent: strings.Repeat("事件", projectSnapshotSummaryRunes+1),
			Scenes:    []string{"场景一", "场景二", "场景三"},
		}},
	}
	session := &ProjectSession{
		manifest:    ProjectManifest{ID: "project-sse-progress"},
		host:        fake,
		actionKinds: make(map[string]int),
		hostEventAt: make(map[string]int),
		subscribers: make(map[chan WebEvent]struct{}),
	}

	event := session.AppendSnapshot()
	snapshot, ok := event.Snapshot.(host.UISnapshot)
	if !ok {
		t.Fatalf("legacy snapshot type = %T", event.Snapshot)
	}
	if snapshot.PremiseFull != "" || len(snapshot.CharacterDetails) != 0 {
		t.Fatalf("SSE snapshot retained heavyweight fields: %+v", snapshot)
	}
	if len(snapshot.Outline) != 1 || len(snapshot.Outline[0].Scenes) != 0 ||
		snapshot.Outline[0].CoreEvent != "" {
		t.Fatalf("SSE outline was not compacted: %+v", snapshot.Outline)
	}
	if event.WorkflowProgress == nil || event.WorkflowProgress.Workflow != workflowNormal {
		t.Fatalf("workflow progress = %+v", event.WorkflowProgress)
	}
}
