package host

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/host/flow"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestConfirmAdaptationProposalCommitsHostBeforeCoordinatorExecution(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	seedAdaptationConfirmationGate(t, st)
	if err := st.Adaptation.SaveProposal(adaptationConfirmationProposalFixture(t, st)); err != nil {
		t.Fatal(err)
	}

	model := newStartupProbeModel()
	coordinator := agentcore.NewAgent(agentcore.WithModel(agents.WithExecutionBarrierModel(model)))
	h := &Host{
		store: st, coordinator: coordinator, events: make(chan Event, 32), streamCh: make(chan string, 32),
		done: make(chan struct{}, 2), lifecycle: lifecycleIdle,
	}
	h.router = flow.NewDispatcher(coordinator, st)
	h.observer = newObserver(coordinator, st, h.emitEvent, h.emitDelta, h.emitClear)
	model.host = h

	type confirmationResult struct {
		plan *domain.AdaptationPlan
		err  error
	}
	result := make(chan confirmationResult, 1)
	go func() {
		plan, err := h.ConfirmAdaptationProposal()
		result <- confirmationResult{plan: plan, err: err}
	}()

	var probe startupProbe
	select {
	case probe = <-model.probed:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not reach Generate")
	}
	if probe.lifecycle != lifecycleRunning || !probe.runOwned || probe.starting || probe.aborting || !probe.routerEnabled {
		t.Fatalf("Generate observed incoherent Host startup: %+v", probe)
	}
	if probe.firstEvent.Summary != "start adaptation" {
		t.Fatalf("Generate preceded start event: %+v", probe.firstEvent)
	}
	if _, err := h.ConfirmAdaptationProposal(); err == nil {
		t.Fatal("concurrent confirmation passed running admission")
	}
	if err := h.StartPrepared("competing start"); err == nil {
		t.Fatal("concurrent start passed running admission")
	}
	if _, err := h.Resume(); err == nil {
		t.Fatal("concurrent resume passed running admission")
	}
	if !h.Abort() {
		t.Fatal("Abort did not recognize the committed startup run")
	}

	select {
	case confirmed := <-result:
		if confirmed.err != nil || confirmed.plan == nil {
			t.Fatalf("confirmation result = plan=%+v err=%v", confirmed.plan, confirmed.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("confirmation did not return")
	}
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("waitDone did not finish after startup-window Abort")
	}
	waitForConfirmationOwnershipRelease(t, h)
	h.mu.Lock()
	finalLifecycle := h.lifecycle
	h.mu.Unlock()
	if finalLifecycle != lifecyclePaused || !h.observer.aborting.Load() {
		t.Fatalf("Abort final state = lifecycle=%q aborting=%v", finalLifecycle, h.observer.aborting.Load())
	}
}

func TestConfirmAdaptationProposalFastTerminalOrdering(t *testing.T) {
	for _, tc := range []struct {
		name     string
		modelErr error
	}{
		{name: "success"},
		{name: "failure", modelErr: errors.New("immediate model failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storepkg.NewStore(t.TempDir())
			if err := st.Init(); err != nil {
				t.Fatal(err)
			}
			seedAdaptationConfirmationGate(t, st)
			if err := st.Adaptation.SaveProposal(adaptationConfirmationProposalFixture(t, st)); err != nil {
				t.Fatal(err)
			}
			model := &terminalStartupModel{err: tc.modelErr}
			coordinator := agentcore.NewAgent(agentcore.WithModel(agents.WithExecutionBarrierModel(model)))
			h := &Host{
				store: st, coordinator: coordinator, events: make(chan Event, 32), streamCh: make(chan string, 32),
				done: make(chan struct{}, 2), lifecycle: lifecycleIdle,
			}
			h.router = flow.NewDispatcher(coordinator, st)
			h.observer = newObserver(coordinator, st, h.emitEvent, h.emitDelta, h.emitClear)

			if plan, err := h.ConfirmAdaptationProposal(); err != nil || plan == nil {
				t.Fatalf("confirmation = plan=%+v err=%v", plan, err)
			}
			select {
			case <-h.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("waitDone did not observe immediate terminal model")
			}
			waitForConfirmationOwnershipRelease(t, h)
			h.coordinator.WaitForIdle()
			h.mu.Lock()
			finalLifecycle := h.lifecycle
			h.mu.Unlock()
			if finalLifecycle != lifecycleIdle || !h.router.IsEnabled() {
				t.Fatalf("terminal state = lifecycle=%q router_enabled=%v", finalLifecycle, h.router.IsEnabled())
			}
			first := <-h.events
			if first.Summary != "start adaptation" {
				t.Fatalf("first event = %+v, want start adaptation", first)
			}
			if tc.modelErr != nil {
				foundError := false
				for len(h.events) > 0 {
					if event := <-h.events; event.Category == "ERROR" && strings.Contains(event.Detail, tc.modelErr.Error()) {
						foundError = true
					}
				}
				if !foundError {
					t.Fatal("immediate model error was not emitted after the start event")
				}
			}
		})
	}
}

func TestConfirmAdaptationProposalRollsBackRejectedLaunchAndRetries(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	beforeFoundation := seedAdaptationConfirmationGate(t, st)
	proposal := adaptationConfirmationProposalFixture(t, st)
	if err := st.Adaptation.SaveProposal(proposal); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("before", 9); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.GlobalScope(), "before", "before.txt", "sha256:before"); err != nil {
		t.Fatal(err)
	}
	beforeProposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatal(err)
	}
	beforeProgress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeCheckpoints := st.Checkpoints.All()
	beforeFoundationBytes, err := os.ReadFile(filepath.Join(dir, "story_foundation.json"))
	if err != nil {
		t.Fatal(err)
	}
	beforeDurable := captureAdaptationConfirmationState(t, dir)

	model := newConfirmationLaunchModel()
	coordinator := agentcore.NewAgent(agentcore.WithModel(model))
	if err := coordinator.Prompt(context.Background(), "occupy coordinator"); err != nil {
		t.Fatal(err)
	}
	waitForConfirmationLaunchSignal(t, model.started, "occupying coordinator")
	h := &Host{
		store: st, coordinator: coordinator, events: make(chan Event, 32), streamCh: make(chan string, 32),
		done: make(chan struct{}, 2), lifecycle: lifecycleIdle,
	}
	h.router = flow.NewDispatcher(coordinator, st)
	h.observer = newObserver(coordinator, st, h.emitEvent, h.emitDelta, h.emitClear)
	h.observer.setAborting(true)

	if _, err := h.ConfirmAdaptationProposal(); !errors.Is(err, agentcore.ErrAlreadyRunning) {
		t.Fatalf("rejected launch error = %v", err)
	}
	afterFailureProposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatal(err)
	}
	afterFailurePlan, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	afterFailureProgress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	afterFailureFoundationBytes, err := os.ReadFile(filepath.Join(dir, "story_foundation.json"))
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	lifecycleAfterFailure := h.lifecycle
	leaseAfterFailure := h.normalFlowLease
	runOwnedAfterFailure := h.normalFlowRunOwned
	scopedRefsAfterFailure := h.normalFlowScopedRefs
	h.mu.Unlock()
	if !bytes.Equal(afterFailureFoundationBytes, beforeFoundationBytes) ||
		!reflect.DeepEqual(afterFailureProposal, beforeProposal) || afterFailurePlan != nil ||
		!reflect.DeepEqual(afterFailureProgress, beforeProgress) ||
		!sameCheckpointState(st.Checkpoints.All(), beforeCheckpoints) ||
		!reflect.DeepEqual(captureAdaptationConfirmationState(t, dir), beforeDurable) {
		t.Fatal("public launch failure did not restore every durable confirmation before-image")
	}
	if lifecycleAfterFailure != lifecycleIdle || leaseAfterFailure != nil || runOwnedAfterFailure || scopedRefsAfterFailure != 0 ||
		!h.observer.aborting.Load() || coordinator.HasQueuedMessages() {
		t.Fatalf("launch failure changed runtime ownership: lifecycle=%q lease=%+v run_owned=%v refs=%d aborting=%v queued=%v",
			lifecycleAfterFailure, leaseAfterFailure, runOwnedAfterFailure, scopedRefsAfterFailure,
			h.observer.aborting.Load(), coordinator.HasQueuedMessages())
	}
	select {
	case event := <-h.events:
		t.Fatalf("launch failure emitted a successful transition event: %+v", event)
	default:
	}

	close(model.releaseFirst)
	coordinator.WaitForIdle()
	confirmed, err := h.ConfirmAdaptationProposal()
	if err != nil {
		t.Fatalf("retry confirmation: %v", err)
	}
	if confirmed.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("retry status = %q", confirmed.Status)
	}
	waitForConfirmationLaunchSignal(t, model.retryStarted, "retried coordinator")
	afterRetry, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRetry.Characters, beforeFoundation.Characters) ||
		!reflect.DeepEqual(afterRetry.Relationships, beforeFoundation.Relationships) || !afterRetry.RelationshipsReviewed {
		t.Fatalf("retry replaced authoritative target cast: %+v", afterRetry)
	}
	if afterRetry.Revision != beforeFoundation.Revision {
		t.Fatalf("failed launch plus retry changed confirmed target Foundation revision %d -> %d",
			beforeFoundation.Revision, afterRetry.Revision)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseComplete); err != nil {
		t.Fatal(err)
	}
	close(model.releaseRetry)
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("retried confirmation run did not finish")
	}
	waitForConfirmationOwnershipRelease(t, h)
}

func TestPersistAdaptationConfirmationRollsBackLateFailureAndPreservesPublishedTargetCastOnRetry(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	beforeFoundation := seedAdaptationConfirmationGate(t, st)
	proposal := adaptationConfirmationProposalFixture(t, st)
	if err := st.Adaptation.SaveProposal(proposal); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("before", 9); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.GlobalScope(), "before", "before.txt", "sha256:before"); err != nil {
		t.Fatal(err)
	}
	beforeProposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatal(err)
	}
	beforeProgress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeCheckpoints := st.Checkpoints.All()
	beforeFoundationBytes, err := os.ReadFile(filepath.Join(dir, "story_foundation.json"))
	if err != nil {
		t.Fatal(err)
	}

	lateFailure := errors.New("forced late confirmation failure")
	h := &Host{store: st, adaptationConfirmationFailpoint: func(stage string) error {
		if stage != "after_foundation" {
			t.Fatalf("unexpected failpoint stage %q", stage)
		}
		return lateFailure
	}}
	if _, err := h.persistAdaptationConfirmation(proposal); !errors.Is(err, lateFailure) {
		t.Fatalf("late failure = %v", err)
	}
	afterFailureProposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatal(err)
	}
	afterFailurePlan, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	afterFailureProgress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	afterFailureFoundationBytes, err := os.ReadFile(filepath.Join(dir, "story_foundation.json"))
	if err != nil {
		t.Fatal(err)
	}
	foundationSame := bytes.Equal(afterFailureFoundationBytes, beforeFoundationBytes)
	proposalSame := reflect.DeepEqual(afterFailureProposal, beforeProposal)
	progressSame := reflect.DeepEqual(afterFailureProgress, beforeProgress)
	checkpointsSame := sameCheckpointState(st.Checkpoints.All(), beforeCheckpoints)
	if !foundationSame || !proposalSame || afterFailurePlan != nil || !progressSame || !checkpointsSame {
		t.Fatalf("late failure partially advanced state: foundation_same=%v proposal_same=%v plan=%+v progress_same=%v checkpoints_same=%v",
			foundationSame, proposalSame, afterFailurePlan, progressSame, checkpointsSame)
	}

	h.adaptationConfirmationFailpoint = nil
	confirmed, err := h.persistAdaptationConfirmation(proposal)
	if err != nil {
		t.Fatalf("retry confirmation: %v", err)
	}
	if confirmed.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("confirmed status = %q", confirmed.Status)
	}
	afterRetry, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRetry.Characters, beforeFoundation.Characters) ||
		!reflect.DeepEqual(afterRetry.Relationships, beforeFoundation.Relationships) || !afterRetry.RelationshipsReviewed {
		t.Fatalf("target cast was replaced by source cast: %+v", afterRetry)
	}
	if afterRetry.Revision != beforeFoundation.Revision {
		t.Fatalf("proposal confirmation changed target foundation revision: got %d, want %d", afterRetry.Revision, beforeFoundation.Revision)
	}
	if _, err := h.persistAdaptationConfirmation(proposal); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	afterIdempotentRetry, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if afterIdempotentRetry.Revision != afterRetry.Revision {
		t.Fatalf("unchanged cast/foundation advanced revision: %d -> %d", afterRetry.Revision, afterIdempotentRetry.Revision)
	}
}

func sameCheckpointState(left, right []domain.Checkpoint) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Seq != right[index].Seq || left[index].Step != right[index].Step ||
			left[index].Artifact != right[index].Artifact || left[index].Digest != right[index].Digest ||
			!left[index].Scope.Matches(right[index].Scope) {
			return false
		}
	}
	return true
}

func TestHostResumeFailsClosedWithoutDurableCoreCastBinding(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("legacy", 1); err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st}
	if _, err := h.Resume(); err == nil {
		t.Fatal("Host.Resume accepted a legacy project without a durable core cast binding")
	}
}

func adaptationConfirmationProposalFixture(t *testing.T, st *storepkg.Store) domain.AdaptationPlan {
	t.Helper()
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc, Status: domain.AdaptationPlanStatusProposal,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "recast the source story around the published target characters",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "Target Opening", SourceChapters: []int{1}, OutlineEntry: domain.OutlineEntry{CoreEvent: "Target Lin opens the conflict.", Hook: "Target Mara reveals a clue.", Scenes: []string{"station"}}},
			{Chapter: 2, Title: "Target Turn", SourceChapters: []int{2}, OutlineEntry: domain.OutlineEntry{CoreEvent: "The target alliance changes the plan.", Hook: "A new threat appears.", Scenes: []string{"archive"}}},
		},
	}
	if binding, err := st.CurrentAdaptationArtifactBinding(); err == nil {
		plan.FoundationBinding = &binding
	}
	return plan
}

func seedAdaptationConfirmationGate(t *testing.T, st *storepkg.Store) domain.StoryFoundation {
	t.Helper()
	source, err := st.Adaptation.SaveSourceChapter(1, "One", "source body")
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.AdaptationSourceManifest{SourcePath: "source.txt", ChapterCount: 1, Chapters: []domain.AdaptationSource{source}}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{Premise: "source premise"}); err != nil {
		t.Fatal(err)
	}
	specs := storepkg.AdaptationDossierBatchSpecs(manifest, adapt.CoCreateDossierBatchSize, adapt.CoCreateDossierBatchRuneLimit)
	if len(specs) != 1 {
		t.Fatalf("dossier batch specs = %d", len(specs))
	}
	if err := st.Adaptation.SaveCoCreateDossier(domain.AdaptationCoCreateDossier{
		Version: 1, PromptVersion: adapt.CoCreateDossierPromptVersion, SourcePath: manifest.SourcePath,
		SourceChapterCount: manifest.ChapterCount, SourceSignature: storepkg.AdaptationSourceSignature(manifest),
		BatchSize: adapt.CoCreateDossierBatchSize, BatchRuneLimit: adapt.CoCreateDossierBatchRuneLimit,
		Batches: []domain.AdaptationCoCreateDossierBatch{{
			Index: 1, SourceFrom: 1, SourceTo: 1, SourceSignature: specs[0].SourceSignature,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	intent := adapt.BuildCoCreateIntent("recast around target characters", domain.AdaptationGranularityArc, domain.AdaptationRewriteFullRewrite, 0)
	if err := st.Adaptation.SaveCoCreateIntent(intent); err != nil {
		t.Fatal(err)
	}
	binding, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode: domain.CoreCastModeAdaptation, DraftRevision: 1, DraftHash: "draft-hash",
		SourceSignature: storepkg.AdaptationSourceSignature(manifest), AdaptationIntentHash: intent.IntentHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeAdaptation,
		DraftRevision: binding.DraftRevision, DraftHash: binding.DraftHash,
		SourceSignature: binding.SourceSignature, AdaptationIntentHash: binding.AdaptationIntentHash,
		Members: []domain.CoreCastMember{
			{
				Character:  domain.Character{ID: "target-lin", Name: "Target Lin", Role: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "accept leadership", Traits: []string{"brave"}, Constraints: []string{"protect allies"}},
				Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal,
				MainlineFunction: "drives the central conflict", InclusionRationale: "new adaptation lead",
			},
			{
				Character:  domain.Character{ID: "target-mara", Name: "Target Mara", Role: "ally", Goal: "expose the threat", Motivation: "justice", Conflict: "distrust", Arc: "learns to rely on others", Traits: []string{"observant"}, Constraints: []string{"will not hide evidence"}},
				Importance: domain.CoreCastImportanceMajorSupport, Origin: domain.CoreCastOriginOriginal,
				MainlineFunction: "changes the protagonist's plan", InclusionRationale: "new adaptation ally",
			},
		},
		PlannedRelationships: []domain.CharacterRelationship{{
			ID: "target-alliance", SourceCharacterID: "target-lin", TargetCharacterID: "target-mara",
			Type: domain.RelationshipTypeAlly, Direction: domain.RelationshipDirectionDirected,
			Status: domain.RelationshipStatusPlanned, Description: "an uneasy alliance drives the mainline",
		}},
	}
	saved, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	workflow, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageTargetFoundationGenerating, -1)
	if err != nil {
		t.Fatal(err)
	}
	review, err := adapt.GenerateTargetFoundation(context.Background(), adapt.Deps{Store: st}, adapt.TargetFoundationOptions{
		Brief: "recast around target characters", ExpectedWorkflowRevision: workflow.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConfirmAdaptationTargetFoundation(review.FoundationRevision, review.Binding.TargetFoundationAuditSignature); err != nil {
		t.Fatal(err)
	}
	currentWorkflow, err := st.Adaptation.LoadPlanningWorkflow()
	if err != nil || currentWorkflow == nil {
		t.Fatal(err)
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, currentWorkflow.Revision); err != nil {
		t.Fatal(err)
	}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	return foundation
}

type confirmationLaunchModel struct {
	started      chan struct{}
	releaseFirst chan struct{}
	retryStarted chan struct{}
	releaseRetry chan struct{}
	mu           sync.Mutex
	calls        int
}

type startupProbe struct {
	lifecycle     lifecycle
	runOwned      bool
	starting      bool
	aborting      bool
	routerEnabled bool
	firstEvent    Event
}

type startupProbeModel struct {
	host   *Host
	probed chan startupProbe
}

func newStartupProbeModel() *startupProbeModel {
	return &startupProbeModel{probed: make(chan startupProbe, 1)}
}

func (m *startupProbeModel) Generate(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	h := m.host
	h.mu.Lock()
	probe := startupProbe{
		lifecycle: h.lifecycle, runOwned: h.normalFlowRunOwned, starting: h.startingRun != nil,
		aborting: h.observer.aborting.Load(), routerEnabled: h.router.IsEnabled(),
	}
	h.mu.Unlock()
	select {
	case probe.firstEvent = <-h.events:
	default:
	}
	m.probed <- probe
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *startupProbeModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, options ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	stream := make(chan agentcore.StreamEvent, 1)
	go func() {
		defer close(stream)
		response, err := m.Generate(ctx, messages, tools, options...)
		if err != nil {
			stream <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
			return
		}
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: response.Message, StopReason: response.Message.StopReason}
	}()
	return stream, nil
}

func (m *startupProbeModel) SupportsTools() bool { return false }

type terminalStartupModel struct {
	err error
}

func (m *terminalStartupModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("done")}, StopReason: agentcore.StopReasonStop,
	}}, nil
}

func (m *terminalStartupModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, options ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	stream := make(chan agentcore.StreamEvent, 1)
	go func() {
		defer close(stream)
		response, err := m.Generate(ctx, messages, tools, options...)
		if err != nil {
			stream <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
			return
		}
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: response.Message, StopReason: response.Message.StopReason}
	}()
	return stream, nil
}

func (m *terminalStartupModel) SupportsTools() bool { return false }

func newConfirmationLaunchModel() *confirmationLaunchModel {
	return &confirmationLaunchModel{
		started: make(chan struct{}), releaseFirst: make(chan struct{}),
		retryStarted: make(chan struct{}), releaseRetry: make(chan struct{}),
	}
}

func (m *confirmationLaunchModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		close(m.started)
		<-m.releaseFirst
	} else {
		close(m.retryStarted)
		<-m.releaseRetry
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("done")}, StopReason: agentcore.StopReasonStop,
	}}, nil
}

func (m *confirmationLaunchModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, options ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	stream := make(chan agentcore.StreamEvent, 1)
	go func() {
		defer close(stream)
		response, err := m.Generate(ctx, messages, tools, options...)
		if err != nil {
			stream <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
			return
		}
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: response.Message, StopReason: response.Message.StopReason}
	}()
	return stream, nil
}

func (m *confirmationLaunchModel) SupportsTools() bool { return false }

func waitForConfirmationLaunchSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForConfirmationOwnershipRelease(t *testing.T, h *Host) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		released := h.normalFlowLease == nil && !h.normalFlowRunOwned
		h.mu.Unlock()
		if released {
			fence, err := h.store.Revisions.SnapshotFence()
			if err == nil && fence.LeaseToken == "" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("normal-flow ownership was not released after completion")
}

func captureAdaptationConfirmationState(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	paths := []string{
		"meta/adaptation/proposal.json", "meta/adaptation/proposal_volume_review.json", "meta/adaptation/proposal_runtime.json",
		"meta/adaptation/plan.json", "meta/adaptation/planning_workflow.json", "meta/adaptation/checks", "meta/structure",
		"meta/run.json", "meta/progress.json", "meta/checkpoints.jsonl", "story_foundation.json", "meta/foundation",
		"premise.md", "characters.json", "characters.md", "world_rules.json", "world_rules.md",
		"planned_relationships.json", "planned_relationships.md", "outline.json", "outline.md",
		"layered_outline.json", "layered_outline.md", "meta/compass.json",
	}
	files := make(map[string][]byte)
	for _, rel := range paths {
		root := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			body, readErr := os.ReadFile(root)
			if readErr != nil {
				t.Fatal(readErr)
			}
			files[filepath.ToSlash(rel)] = body
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			fileRel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				return relErr
			}
			files[filepath.ToSlash(fileRel)] = body
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return files
}
