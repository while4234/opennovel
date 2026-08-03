package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/host/flow"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func startHostTestRevision(t *testing.T, st *storepkg.Store, key string) {
	t.Helper()
	impact, err := domain.NewRevisionImpact("host mutation gate", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "active", Impact: impact, IdempotencyKey: key,
	}); err != nil {
		t.Fatal(err)
	}
}

func snapshotHostTestFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "meta", "revisions", "transaction.lock")) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

type fakeHostRevisionPolicy struct{}

func (fakeHostRevisionPolicy) Identity() (string, string) { return "test.host-revision", "1" }

func (fakeHostRevisionPolicy) Mode() domain.RevisionMode { return "fake-host" }

func (fakeHostRevisionPolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	return []domain.RevisionApprovalStage{{ID: "prose", Label: "Prose"}}, nil
}

func (fakeHostRevisionPolicy) ValidateImpact(domain.RevisionImpact) error { return nil }

func (fakeHostRevisionPolicy) ValidateCandidate(domain.RevisionSession, []domain.ArtifactVersion) error {
	return nil
}

func (fakeHostRevisionPolicy) Route(domain.RevisionSession) (*domain.RevisionRoute, error) {
	return nil, nil
}

func TestNormalHostFlowIsBlockedByActiveRevision(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	h := &Host{store: st}
	if err := h.refuseNormalFlowDuringRevision(); err != nil {
		t.Fatalf("empty revision store blocked normal flow: %v", err)
	}
	impact, err := domain.NewRevisionImpact("active revision", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "revise", Impact: impact, IdempotencyKey: "start",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.refuseNormalFlowDuringRevision(); !errors.Is(err, storepkg.ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("normal flow gate error = %v", err)
	}
}

func TestHostNormalFlowLeaseFencesRevisionUntilRelease(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	h := &Host{store: st}
	ownership, err := h.acquireNormalFlowOwnership("host:test")
	if err != nil {
		t.Fatalf("acquire host normal-flow ownership = %v", err)
	}
	impact, err := domain.NewRevisionImpact("host running", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "must wait", Impact: impact, IdempotencyKey: "host-running",
	}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
		t.Fatalf("revision crossed host lease: %v", err)
	}
	ownership.Release()
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "after host", Impact: impact, IdempotencyKey: "host-stopped",
	}); err != nil {
		t.Fatalf("revision did not start after host lease release: %v", err)
	}
}

func TestWebOwnershipIsExplicitlyReusedAndTransferredToHostRun(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	h := &Host{store: st}
	releaseAction, err := h.BeginNormalFlowAction("web:test")
	if err != nil {
		t.Fatal(err)
	}
	ownership, err := h.acquireNormalFlowOwnership("host:start-run")
	if err != nil {
		t.Fatalf("reuse Web ownership in Host run: %v", err)
	}
	ownership.TransferToRun()
	releaseAction()

	impact, err := domain.NewRevisionImpact("host run", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "must wait", Impact: impact, IdempotencyKey: "during-transferred-run",
	}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
		t.Fatalf("revision crossed transferred Host ownership: %v", err)
	}
	h.releaseNormalFlowRunOwnership()
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "after run", Impact: impact, IdempotencyKey: "after-transferred-run",
	}); err != nil {
		t.Fatalf("revision did not start after transferred ownership ended: %v", err)
	}
}

func TestWaitDoneRetainsRunOwnershipUntilTerminalEventIsPersisted(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("terminal ownership", 1); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	const terminalSummary = "Coordinator 停止 (已完成 0 章)"
	persistenceStarted := make(chan struct{})
	allowPersistence := make(chan struct{})
	h := &Host{
		store:     st,
		observer:  &observer{agents: make(map[string]*agentState)},
		events:    make(chan Event, 1),
		done:      make(chan struct{}, 1),
		lifecycle: lifecycleRunning,
	}
	h.appendRuntimeQueue = func(item domain.RuntimeQueueItem) (domain.RuntimeQueueItem, error) {
		if item.Summary == terminalSummary {
			close(persistenceStarted)
			<-allowPersistence
		}
		return st.Runtime.AppendQueue(item)
	}
	ownership, err := h.acquireNormalFlowOwnership("host:test-terminal-run")
	if err != nil {
		t.Fatalf("acquire run ownership: %v", err)
	}
	ownership.TransferToRun()

	waitDoneReturned := make(chan struct{})
	go func() {
		h.waitDone()
		close(waitDoneReturned)
	}()
	select {
	case <-persistenceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("waitDone did not reach terminal event persistence")
	}

	items, err := st.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("load queue before terminal persistence: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("terminal event persisted before test released it: %+v", items)
	}
	impact, err := domain.NewRevisionImpact("terminal persistence window", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "must wait for terminal event", Impact: impact, IdempotencyKey: "before-terminal-event",
	}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
		t.Fatalf("revision crossed pending terminal event persistence: %v", err)
	}

	close(allowPersistence)
	select {
	case <-waitDoneReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("waitDone did not finish after terminal event persistence")
	}
	items, err = st.Runtime.LoadQueue()
	if err != nil {
		t.Fatalf("load queue after terminal persistence: %v", err)
	}
	if len(items) != 1 || items[0].Summary != terminalSummary {
		t.Fatalf("persisted terminal events = %+v", items)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "after terminal event", Impact: impact, IdempotencyKey: "after-terminal-event",
	}); err != nil {
		t.Fatalf("revision did not start after waitDone released ownership: %v", err)
	}
}

func TestWaitDoneRetainsRunOwnershipAcrossSuccessfulAdaptationRepair(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedAdaptationConfirmationGate(t, st)
	if err := st.Progress.Init("adaptation repair ownership", 1); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("set writing phase: %v", err)
	}
	if err := st.Progress.StartChapter(1); err != nil {
		t.Fatalf("start chapter: %v", err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Status:      domain.AdaptationPlanStatusConfirmed,
		Granularity: domain.AdaptationGranularityArc,
		Brief:       "repair the legacy chapter budget without changing the story",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 1,
			OutlineEntry: domain.OutlineEntry{
				Title:     "Opening",
				CoreEvent: "The protagonist accepts the first challenge",
				Scenes:    []string{"one", "two", "three", "four", "five", "six"},
			},
			TargetRunes:    900,
			TargetMinRunes: 800,
			TargetMaxRunes: 1000,
			WordBudget: &domain.AdaptationChapterWordBudget{
				TargetRunes: 900,
				MinRunes:    800,
				MaxRunes:    1000,
			},
		}},
	}); err != nil {
		t.Fatalf("save blocked adaptation plan: %v", err)
	}

	model := newAdaptationRepairOwnershipModel()
	coordinator := agentcore.NewAgent(agentcore.WithModel(model))
	h := &Host{
		store:       st,
		models:      &bootstrap.ModelSet{Default: bootstrap.NewSwappableModel("test", "adaptation-repair", model)},
		coordinator: coordinator,
		usage:       NewUsageTracker(nil, nil),
		events:      make(chan Event, 32),
		streamCh:    make(chan string, 32),
		done:        make(chan struct{}, 2),
		lifecycle:   lifecycleRunning,
	}
	h.router = flow.NewDispatcher(coordinator, st)
	h.observer = newObserver(coordinator, st, h.emitEvent, h.emitDelta, h.emitClear)
	repairPersisting := make(chan struct{})
	allowRepairPersistence := make(chan struct{})
	var repairPersistenceOnce sync.Once
	h.appendRuntimeQueue = func(item domain.RuntimeQueueItem) (domain.RuntimeQueueItem, error) {
		if strings.HasPrefix(item.Summary, "预算专项模型重分析通过") {
			repairPersistenceOnce.Do(func() { close(repairPersisting) })
			<-allowRepairPersistence
		}
		return st.Runtime.AppendQueue(item)
	}
	ownership, err := h.acquireNormalFlowOwnership("host:test-adaptation-repair-run")
	if err != nil {
		t.Fatalf("acquire run ownership: %v", err)
	}
	ownership.TransferToRun()
	h.mu.Lock()
	originalLeaseToken := h.normalFlowLease.Token
	h.mu.Unlock()

	waitDoneReturned := make(chan struct{})
	go func() {
		h.waitDone()
		close(waitDoneReturned)
	}()
	waitForOwnershipSignal(t, repairPersisting, "successful adaptation repair persistence")
	assertRevisionBlockedByNormalFlow(t, st, "during-successful-adaptation-repair")

	close(allowRepairPersistence)
	waitForOwnershipSignal(t, model.replacementStarted, "replacement adaptation run")
	waitForOwnershipSignal(t, waitDoneReturned, "repairing waitDone handoff")
	h.mu.Lock()
	replacementLeaseToken := h.normalFlowLease.Token
	replacementRunOwned := h.normalFlowRunOwned
	h.mu.Unlock()
	if replacementLeaseToken != originalLeaseToken {
		t.Fatalf("adaptation repair replaced normal-flow lease: before=%q after=%q", originalLeaseToken, replacementLeaseToken)
	}
	if !replacementRunOwned {
		t.Fatal("replacement adaptation run did not inherit normal-flow ownership")
	}
	assertRevisionBlockedByNormalFlow(t, st, "during-replacement-adaptation-run")

	if err := st.Progress.UpdatePhase(domain.PhaseComplete); err != nil {
		t.Fatalf("complete repaired run: %v", err)
	}
	close(model.releaseReplacement)
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("replacement adaptation run did not finalize")
	}
	startHostTestRevision(t, st, "after-replacement-adaptation-run")
}

func assertRevisionBlockedByNormalFlow(t *testing.T, st *storepkg.Store, key string) {
	t.Helper()
	impact, err := domain.NewRevisionImpact("adaptation repair ownership", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "must wait for normal flow", Impact: impact, IdempotencyKey: key,
	}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
		t.Fatalf("revision crossed normal-flow ownership: %v", err)
	}
}

type adaptationRepairOwnershipModel struct {
	replacementStarted chan struct{}
	releaseReplacement chan struct{}
	startOnce          sync.Once
}

func newAdaptationRepairOwnershipModel() *adaptationRepairOwnershipModel {
	return &adaptationRepairOwnershipModel{
		replacementStarted: make(chan struct{}),
		releaseReplacement: make(chan struct{}),
	}
}

func (m *adaptationRepairOwnershipModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{Message: adaptationRepairAssistantMessage(`{"chapters":[{"chapter":1,"target_runes":2100,"min_runes":1900,"max_runes":2300}]}`)}, nil
}

func (m *adaptationRepairOwnershipModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.startOnce.Do(func() { close(m.replacementStarted) })
	stream := make(chan agentcore.StreamEvent, 1)
	go func() {
		defer close(stream)
		<-m.releaseReplacement
		message := adaptationRepairAssistantMessage("done")
		stream <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: message, StopReason: agentcore.StopReasonStop}
	}()
	return stream, nil
}

func (m *adaptationRepairOwnershipModel) SupportsTools() bool { return false }

func adaptationRepairAssistantMessage(text string) agentcore.Message {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: agentcore.StopReasonStop,
	}
}

func waitForOwnershipSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestActiveRevisionBlocksContinuationAndChapterRevisionBeforeSideEffects(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Progress.Init("blocked", 1); err != nil {
		t.Fatal(err)
	}
	impact, err := domain.NewRevisionImpact("host mutation gate", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "active", Impact: impact, IdempotencyKey: "active-host-mutation",
	}); err != nil {
		t.Fatal(err)
	}
	migrationLog := filepath.Join(st.Dir(), "meta", "structure", "migration.json")
	if err := os.MkdirAll(filepath.Dir(migrationLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(migrationLog, []byte(`{"version":1,"stage":"planned"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st}

	beforeContinuation := adaptationRevisionProjectBytes(t, st.Dir())
	if _, _, err := h.StartContinuation(1); !errors.Is(err, storepkg.ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("StartContinuation error = %v", err)
	}
	if afterContinuation := adaptationRevisionProjectBytes(t, st.Dir()); !reflect.DeepEqual(beforeContinuation, afterContinuation) {
		t.Fatal("rejected Host continuation recovered or changed pending structure bytes")
	}
	if err := os.Remove(migrationLog); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ReviseChapter(ChapterRevisionRequest{Chapter: 1, Instruction: "rewrite"}); !errors.Is(err, storepkg.ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("ReviseChapter error = %v", err)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(progress.PendingRewrites) != 0 {
		t.Fatalf("blocked chapter revision mutated rewrite queue: %+v", progress.PendingRewrites)
	}
	if _, err := st.Continuation.LoadSnapshot(); !errors.Is(err, storepkg.ErrContinuationNotInitialized) {
		t.Fatalf("blocked continuation start mutated workflow: %v", err)
	}
}

func TestActiveRevisionBlocksCoCreateResumeAndCancelBeforeOwnershipRelease(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	impact, err := domain.NewRevisionImpact("co-create gate", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "active", Impact: impact, IdempotencyKey: "active-cocreate",
	}); err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st, cocreating: true}
	if err := h.ResumeFromCoCreate("continue this direction"); !errors.Is(err, storepkg.ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("ResumeFromCoCreate error = %v", err)
	}
	h.CancelCoCreate()
	h.mu.Lock()
	cocreating := h.cocreating
	h.mu.Unlock()
	if !cocreating {
		t.Fatal("co-create ownership was cleared while an active revision blocked cancellation")
	}
}

func TestActiveRevisionBlocksRepresentativeWritableHostBoundariesWithoutSideEffects(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	startHostTestRevision(t, st, "active-host-boundaries")
	h := &Host{store: st, events: make(chan Event, 16), cocreating: true}

	tests := []struct {
		name string
		run  func() error
	}{
		{"prepare user rules", func() error { return h.PrepareUserRules("write a story") }},
		{"set word budget", func() error { return h.SetWordBudget(&domain.WordBudget{TargetTotalWords: 1000}) }},
		{"continuation draft", func() error { _, err := h.BeginContinuationDraft(1); return err }},
		{"adaptation planning", func() error {
			_, err := h.BuildAdaptationProposalContext(context.Background(), adapt.ProposalOptions{Brief: "adapt"})
			return err
		}},
		{"rollback", func() error { _, err := h.Rollback(domain.RollbackRequest{Confirm: true}); return err }},
		{"idle steer", func() error {
			_, err := h.store.RunMeta.Load()
			if err != nil {
				return err
			}
			h.cocreating = false
			return h.Steer("later")
		}},
		{"normal co-create", func() error { _, err := h.CoCreateStream(context.Background(), nil, nil); return err }},
		{"adaptation analysis", func() error { _, err := h.PrepareAdaptationSource(context.Background(), "source.txt"); return err }},
		{"simulation", func() error { _, err := h.SimulateFromDir(context.Background(), dir); return err }},
		{"simulation import", func() error { _, err := h.ImportSimulationProfile(context.Background(), "profile.json"); return err }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := snapshotHostTestFiles(t, dir)
			err := test.run()
			if !errors.Is(err, storepkg.ErrActiveRevisionBlocksNormalFlow) {
				t.Fatalf("error = %v", err)
			}
			if test.name == "idle steer" && !strings.Contains(err.Error(), "set pending steer") {
				t.Fatalf("idle Steer error = %v, want pending steer context", err)
			}
			after := snapshotHostTestFiles(t, dir)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("blocked boundary changed project files\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestAsyncHostStreamsRetainLeaseUntilProducerCompletionOrCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
	}{
		{"continuation import completion", false},
		{"adaptation analysis completion", false},
		{"simulation completion", false},
		{"simulation import cancellation", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			st := storepkg.NewStore(dir)
			h := &Host{store: st}
			release, err := h.beginNormalFlowMutation()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			source := make(chan int)
			stream := holdNormalFlowStream(ctx, source, release)
			if test.cancel {
				cancel()
			} else {
				defer cancel()
			}
			impact, err := domain.NewRevisionImpact("stream live", []domain.RevisionImpactItem{{
				ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
				Intent: "must wait", Impact: impact, IdempotencyKey: "while-stream-live",
			}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
				t.Fatalf("revision crossed live stream: %v", err)
			}
			close(source)
			for range stream {
			}
			if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
				Intent: "after stream", Impact: impact, IdempotencyKey: "after-stream",
			}); err != nil {
				t.Fatalf("revision did not start after stream ended: %v", err)
			}
		})
	}
}

func TestWritableAsyncHostMethodsHoldOwnershipForTheirReturnedStreams(t *testing.T) {
	t.Run("continuation import", func(t *testing.T) {
		h := newSimulationTestHost(t)
		source := make(chan imp.Event)
		previousRules := prepareContinuationImportUserRules
		prepareContinuationImportUserRules = func(*Host) error { return nil }
		defer func() { prepareContinuationImportUserRules = previousRules }()
		previousRun := runContinuationImport
		runContinuationImport = func(context.Context, imp.Deps, imp.Options) (<-chan imp.Event, error) {
			return source, nil
		}
		defer func() { runContinuationImport = previousRun }()
		sourcePath := filepath.Join(t.TempDir(), "source.txt")
		if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, err := h.ImportFrom(ctx, imp.Options{SourcePath: sourcePath})
		if err != nil {
			t.Fatal(err)
		}
		assertHostStreamLease(t, h.store, source, stream, nil)
	})

	t.Run("adaptation analysis", func(t *testing.T) {
		h := newSimulationTestHost(t)
		source := make(chan adapt.Event)
		previous := runAdaptationSource
		runAdaptationSource = func(context.Context, adapt.Deps, adapt.Options) (<-chan adapt.Event, error) {
			return source, nil
		}
		defer func() { runAdaptationSource = previous }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, err := h.PrepareAdaptationSource(ctx, "source.txt")
		if err != nil {
			t.Fatal(err)
		}
		assertHostStreamLease(t, h.store, source, stream, nil)
	})

	t.Run("simulation analysis", func(t *testing.T) {
		h := newSimulationTestHost(t)
		source := make(chan sim.Event)
		previous := runSimulation
		runSimulation = func(context.Context, sim.Deps, sim.Options) (<-chan sim.Event, error) {
			return source, nil
		}
		defer func() { runSimulation = previous }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, err := h.SimulateFromDir(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		assertHostStreamLease(t, h.store, source, stream, nil)
	})

	t.Run("simulation import cancellation", func(t *testing.T) {
		h := newSimulationTestHost(t)
		source := make(chan sim.Event)
		previous := runSimulationImport
		runSimulationImport = func(context.Context, sim.Deps, string) (<-chan sim.Event, error) {
			return source, nil
		}
		defer func() { runSimulationImport = previous }()
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := h.ImportSimulationProfile(ctx, "profile.json")
		if err != nil {
			t.Fatal(err)
		}
		assertHostStreamLease(t, h.store, source, stream, cancel)
	})
}

func TestConcurrentDirectHostMutationCannotReleaseAnotherCallOwnership(t *testing.T) {
	h := newSimulationTestHost(t)
	source := make(chan adapt.Event)
	previous := runAdaptationSource
	runAdaptationSource = func(context.Context, adapt.Deps, adapt.Options) (<-chan adapt.Event, error) {
		return source, nil
	}
	defer func() { runAdaptationSource = previous }()

	stream, err := h.PrepareAdaptationSource(context.Background(), "source.txt")
	if err != nil {
		t.Fatal(err)
	}
	shortCallDone := make(chan error, 1)
	go func() {
		shortCallDone <- h.SetWordBudget(&domain.WordBudget{TargetTotalWords: 10_000})
	}()
	select {
	case err := <-shortCallDone:
		if err != nil {
			t.Fatalf("concurrent short mutation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent short mutation did not finish")
	}

	impact, err := domain.NewRevisionImpact("long Host mutation", []domain.RevisionImpactItem{{
		ArtifactID: "adaptation-source", ArtifactKind: "source", Change: "replace",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "must wait", Impact: impact, IdempotencyKey: "while-direct-host-call-live",
	}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
		t.Fatalf("short Host call released another call's ownership: %v", err)
	}

	close(source)
	for range stream {
	}
	if _, err := h.store.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "after producer", Impact: impact, IdempotencyKey: "after-direct-host-call",
	}); err != nil {
		t.Fatalf("revision did not start after long Host call ended: %v", err)
	}
}

func TestContinuationImportCancellationDrainsProducerBeforeReleasingOwnership(t *testing.T) {
	h := newSimulationTestHost(t)
	source := make(chan imp.Event)
	previousRules := prepareContinuationImportUserRules
	prepareContinuationImportUserRules = func(*Host) error { return nil }
	defer func() { prepareContinuationImportUserRules = previousRules }()
	previousRun := runContinuationImport
	runContinuationImport = func(context.Context, imp.Deps, imp.Options) (<-chan imp.Event, error) {
		return source, nil
	}
	defer func() { runContinuationImport = previousRun }()
	sourcePath := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := h.ImportFrom(ctx, imp.Options{SourcePath: sourcePath})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	emitted := make(chan struct{})
	go func() {
		source <- imp.Event{Stage: imp.StageChapter, Message: "emitted after cancellation"}
		close(emitted)
	}()
	select {
	case <-emitted:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation finalizer stopped draining the producer after cancellation")
	}

	impact, err := domain.NewRevisionImpact("continuation producer", []domain.RevisionImpactItem{{
		ArtifactID: "continuation-source", ArtifactKind: "source", Change: "replace",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "must wait", Impact: impact, IdempotencyKey: "while-canceled-producer-open",
	}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
		t.Fatalf("revision crossed canceled but open continuation producer: %v", err)
	}

	close(source)
	for range stream {
	}
	if _, err := h.store.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "after producer", Impact: impact, IdempotencyKey: "after-canceled-producer-close",
	}); err != nil {
		t.Fatalf("revision did not start after canceled producer closed: %v", err)
	}
}

func assertHostStreamLease[T any](t *testing.T, st *storepkg.Store, source chan T, stream <-chan T, cancel func()) {
	t.Helper()
	impact, err := domain.NewRevisionImpact("stream live", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cancel != nil {
		cancel()
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "must wait", Impact: impact, IdempotencyKey: "while-method-stream-live",
	}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
		t.Fatalf("revision crossed live Host stream: %v", err)
	}
	close(source)
	for range stream {
	}
	if _, err := st.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "after stream", Impact: impact, IdempotencyKey: "after-method-stream",
	}); err != nil {
		t.Fatalf("revision did not start after Host stream ended: %v", err)
	}
}
