package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	hostpkg "github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestActionRegistryDeduplicatesIdempotencyKey(t *testing.T) {
	registry, err := NewActionRegistry("project-actions", filepath.Join(t.TempDir(), "meta", "actions.json"))
	if err != nil {
		t.Fatalf("NewActionRegistry: %v", err)
	}
	var runs atomic.Int32
	finished := make(chan struct{})
	runner := func(context.Context) error {
		runs.Add(1)
		close(finished)
		return nil
	}

	first, created, err := registry.Start("proposal", "request-1", runner, actionLifecycle{})
	if err != nil {
		t.Fatalf("Start first: %v", err)
	}
	if !created {
		t.Fatal("first action was not created")
	}
	second, created, err := registry.Start("proposal", "request-1", runner, actionLifecycle{})
	if err != nil {
		t.Fatalf("Start duplicate: %v", err)
	}
	if created {
		t.Fatal("duplicate action was reported as created")
	}
	if second.ActionID != first.ActionID {
		t.Fatalf("duplicate action ID = %q, want %q", second.ActionID, first.ActionID)
	}

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("background action did not finish")
	}
	waitForActionStatus(t, registry, first.ActionID, ActionStatusCompleted)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runner executions = %d, want 1", got)
	}
}

func TestActionRegistryMarksRunningActionInterruptedAfterReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "actions.json")
	registry, err := NewActionRegistry("project-restart", path)
	if err != nil {
		t.Fatalf("NewActionRegistry: %v", err)
	}
	release := make(chan struct{})
	action, _, err := registry.Start("outlines", "request-2", func(context.Context) error {
		<-release
		return nil
	}, actionLifecycle{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	reloaded, err := NewActionRegistry("project-restart", path)
	if err != nil {
		close(release)
		t.Fatalf("reload registry: %v", err)
	}
	interrupted, err := reloaded.Get(action.ActionID)
	if err != nil {
		close(release)
		t.Fatalf("Get interrupted action: %v", err)
	}
	if interrupted.Status != ActionStatusInterrupted || !interrupted.Recoverable {
		close(release)
		t.Fatalf("interrupted action = %+v", interrupted)
	}
	if interrupted.FinishedAt == nil {
		close(release)
		t.Fatal("interrupted action has no finished_at")
	}
	close(release)
	waitForActionStatus(t, registry, action.ActionID, ActionStatusCompleted)
}

func TestActionRegistryRequiresIdempotencyKey(t *testing.T) {
	registry, err := NewActionRegistry("project-key", "")
	if err != nil {
		t.Fatalf("NewActionRegistry: %v", err)
	}
	_, _, err = registry.Start("proposal", "", func(context.Context) error { return nil }, actionLifecycle{})
	if !errors.Is(err, ErrActionKeyRequired) {
		t.Fatalf("Start error = %v, want ErrActionKeyRequired", err)
	}
}

func TestActionRegistryReleasesLifecycleAfterRunnerPanic(t *testing.T) {
	registry, err := NewActionRegistry("project-panic", "")
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	action, created, err := registry.Start(
		"planning_revision",
		"panic-request",
		func(context.Context) error {
			panic("unexpected model adapter panic")
		},
		actionLifecycle{finished: func() { close(finished) }},
	)
	if err != nil || !created {
		t.Fatalf("Start = created=%v err=%v", created, err)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("panic did not finish action lifecycle")
	}
	failed := waitForActionStatus(t, registry, action.ActionID, ActionStatusFailed)
	if !failed.Recoverable || !strings.Contains(failed.Error, "panicked") {
		t.Fatalf("panic action = %+v", failed)
	}
}

func TestCancellableBackgroundActionStopsFromProjectPause(t *testing.T) {
	dir := t.TempDir()
	host := newFakeProjectHost()
	session, err := NewProjectSession(
		ProjectManifest{ID: "cancellable-planning-revision", RootDir: dir, OutputDir: dir},
		host,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	started := make(chan struct{})
	action, created, err := session.StartCancellableBackgroundAction(
		projectActionKindPlanningRevision,
		"planning-pause",
		func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	)
	if err != nil || !created {
		t.Fatalf("StartCancellableBackgroundAction = created=%v err=%v", created, err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellable background action did not start")
	}
	if !session.Pause() {
		t.Fatal("project pause did not cancel the background action")
	}
	failed := waitForActionStatus(t, session.actions, action.ActionID, ActionStatusFailed)
	if !failed.Recoverable || !strings.Contains(failed.Error, "context canceled") {
		t.Fatalf("paused action = %+v", failed)
	}
	if !session.waitForActionsIdle(2 * time.Second) {
		t.Fatal("paused background action did not release ownership")
	}
}

func TestActionRegistryLatestSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "actions.json")
	registry, err := NewActionRegistry("project-latest", path)
	if err != nil {
		t.Fatalf("NewActionRegistry: %v", err)
	}
	action, _, err := registry.Start("proposal", "request-latest", func(context.Context) error { return nil }, actionLifecycle{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForActionStatus(t, registry, action.ActionID, ActionStatusCompleted)

	reloaded, err := NewActionRegistry("project-latest", path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	latest := reloaded.Latest()
	if latest == nil || latest.ActionID != action.ActionID || latest.Status != ActionStatusCompleted {
		t.Fatalf("latest action = %+v, want completed %s", latest, action.ActionID)
	}
}

func TestContinuationLongMutationAsyncReturns202AndSupportsStatusQuery(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	store := NewProjectStore(runtimeRoot)
	manifest, err := store.CreateProject("async continuation")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := newFakeProjectHost()
	session, err := NewProjectSession(manifest, fake)
	if err != nil {
		t.Fatalf("NewProjectSession: %v", err)
	}
	defer session.Close()
	manager := NewSessionManager(testWebConfig(t), assets.Load("default"), store)
	manager.sessions[manifest.ID] = session
	server := &Server{store: store, sessions: manager}

	started := make(chan struct{})
	release := make(chan struct{})
	mutation := func(ctx context.Context, _ *ProjectSession, _ continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		close(started)
		<-release
		return nil, ctx.Err()
	}
	body := `{"expected_revision":1,"async":true,"idempotency_key":"continuation-request"}`
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/continuation/proposal/generate", strings.NewReader(body))
	response := httptest.NewRecorder()
	server.handleContinuationLongMutation(response, request, manifest.ID, "continuation_proposal_generate", mutation)
	if response.Code != http.StatusAccepted {
		close(release)
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		ActionID string `json:"action_id"`
		Created  bool   `json:"created"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		close(release)
		t.Fatalf("decode accepted response: %v", err)
	}
	if accepted.ActionID == "" || !accepted.Created {
		close(release)
		t.Fatalf("accepted response = %+v", accepted)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("async mutation did not start")
	}

	duplicateRequest := httptest.NewRequest(http.MethodPost, request.URL.Path, strings.NewReader(body))
	duplicateResponse := httptest.NewRecorder()
	server.handleContinuationLongMutation(duplicateResponse, duplicateRequest, manifest.ID, "continuation_proposal_generate", mutation)
	if duplicateResponse.Code != http.StatusAccepted {
		close(release)
		t.Fatalf("duplicate status = %d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
	var duplicate struct {
		ActionID string `json:"action_id"`
		Created  bool   `json:"created"`
	}
	if err := json.Unmarshal(duplicateResponse.Body.Bytes(), &duplicate); err != nil {
		close(release)
		t.Fatalf("decode duplicate response: %v", err)
	}
	if duplicate.ActionID != accepted.ActionID || duplicate.Created {
		close(release)
		t.Fatalf("duplicate response = %+v, accepted = %+v", duplicate, accepted)
	}

	query := httptest.NewRequest(http.MethodGet, request.URL.Path+"?action_id="+accepted.ActionID, nil)
	queryResponse := httptest.NewRecorder()
	server.handleContinuationLongMutation(queryResponse, query, manifest.ID, "continuation_proposal_generate", mutation)
	if queryResponse.Code != http.StatusOK {
		close(release)
		t.Fatalf("query status = %d body=%s", queryResponse.Code, queryResponse.Body.String())
	}
	close(release)
	waitForActionStatus(t, session.actions, accepted.ActionID, ActionStatusCompleted)
}

func waitForActionStatus(t *testing.T, registry *ActionRegistry, actionID string, want ActionStatus) ActionRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		action, err := registry.Get(actionID)
		if err != nil {
			t.Fatalf("Get action: %v", err)
		}
		if action.Status == want {
			return action
		}
		time.Sleep(10 * time.Millisecond)
	}
	action, _ := registry.Get(actionID)
	t.Fatalf("action status = %q, want %q", action.Status, want)
	return ActionRecord{}
}

type blockingSnapshotProjectHost struct {
	*fakeProjectHost
	block       atomic.Bool
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func newBlockingSnapshotProjectHost() *blockingSnapshotProjectHost {
	return &blockingSnapshotProjectHost{
		fakeProjectHost: newFakeProjectHost(),
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
}

func (h *blockingSnapshotProjectHost) Snapshot() hostpkg.UISnapshot {
	if h.block.Load() {
		h.enteredOnce.Do(func() { close(h.entered) })
		<-h.release
	}
	return h.fakeProjectHost.Snapshot()
}

func (h *blockingSnapshotProjectHost) arm() {
	h.block.Store(true)
}

type blockingAdaptationPostProcessHost struct {
	*blockingSnapshotProjectHost
}

func (h *blockingAdaptationPostProcessHost) BuildAdaptationProposalVolumesContext(ctx context.Context, options adapt.ProposalOptions) (*adapt.ProposalStageResult, error) {
	result, err := h.fakeProjectHost.BuildAdaptationProposalVolumesContext(ctx, options)
	h.arm()
	return result, err
}

func (h *blockingAdaptationPostProcessHost) BuildAdaptationProposalDetailsContext(ctx context.Context, options adapt.ProposalDetailsOptions) (*domain.AdaptationPlan, error) {
	result, err := h.fakeProjectHost.BuildAdaptationProposalDetailsContext(ctx, options)
	h.arm()
	return result, err
}

func TestActiveRevisionBlocksBackgroundActionCreationWithoutSideEffects(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	impact, err := domain.NewRevisionImpact("background action gate", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "active", Impact: impact, IdempotencyKey: "active-background-action",
	}); err != nil {
		t.Fatal(err)
	}

	host := newFakeProjectHost()
	session, err := NewProjectSession(ProjectManifest{ID: "blocked-background", RootDir: dir, OutputDir: dir}, host)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	session.mu.Lock()
	historyBefore := len(session.history)
	session.mu.Unlock()
	var runs atomic.Int32
	_, _, err = session.StartBackgroundAction("continuation", "blocked-request", func(context.Context) error {
		runs.Add(1)
		return nil
	})
	if !errors.Is(err, storepkg.ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("StartBackgroundAction error = %v", err)
	}
	if runs.Load() != 0 {
		t.Fatalf("blocked background runner executed %d times", runs.Load())
	}
	if latest := session.LatestBackgroundAction(); latest != nil {
		t.Fatalf("blocked background action created registry record: %+v", latest)
	}
	session.mu.Lock()
	historyAfter := len(session.history)
	session.mu.Unlock()
	if historyAfter != historyBefore {
		t.Fatalf("blocked background action appended snapshots: before=%d after=%d", historyBefore, historyAfter)
	}
	if _, err := os.Stat(projectActionRegistryPath(session.Manifest())); !os.IsNotExist(err) {
		t.Fatalf("blocked background action persisted registry: %v", err)
	}
}

func TestBackgroundActionOwnershipIncludesFinalRegistryAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	host := newBlockingSnapshotProjectHost()
	session, err := NewProjectSession(ProjectManifest{ID: "background-post-process", RootDir: dir, OutputDir: dir}, host)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	action, created, err := session.StartBackgroundAction("continuation", "post-process", func(context.Context) error {
		host.arm()
		return nil
	})
	if err != nil || !created {
		t.Fatalf("StartBackgroundAction = created=%v err=%v", created, err)
	}
	select {
	case <-host.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("final background snapshot did not start")
	}
	completed := waitForActionStatus(t, session.actions, action.ActionID, ActionStatusCompleted)
	if completed.FinishedAt == nil {
		t.Fatal("completed registry state was not persisted before final snapshot")
	}

	impact, err := domain.NewRevisionImpact("background post-processing", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "must wait", Impact: impact, IdempotencyKey: "during-background-final-snapshot",
	}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
		t.Fatalf("revision crossed background final snapshot: %v", err)
	}
	close(host.release)
	if !session.waitForActionsIdle(2 * time.Second) {
		t.Fatal("background ownership was not released after final snapshot")
	}
	if _, err := st.Revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "after action", Impact: impact, IdempotencyKey: "after-background-final-snapshot",
	}); err != nil {
		t.Fatalf("revision did not start after background post-processing: %v", err)
	}
}

func TestBackgroundActionRunnerReceivesNormalFlowRevisionFence(t *testing.T) {
	dir := t.TempDir()
	st := storepkg.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	session, err := NewProjectSession(
		ProjectManifest{ID: "background-revision-fence", RootDir: dir, OutputDir: dir},
		newFakeProjectHost(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	fenced := make(chan bool, 1)
	action, created, err := session.StartBackgroundAction(
		"character_review",
		"revision-fence",
		func(ctx context.Context) error {
			_, ok := storepkg.RevisionFenceFromContext(ctx)
			fenced <- ok
			return nil
		},
	)
	if err != nil || !created {
		t.Fatalf("StartBackgroundAction = created=%v err=%v", created, err)
	}
	select {
	case ok := <-fenced:
		if !ok {
			t.Fatal("background action runner did not receive a revision fence")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background action runner did not execute")
	}
	waitForActionStatus(t, session.actions, action.ActionID, ActionStatusCompleted)
}

func TestAdaptationWorkflowFinalWritesRemainOwned(t *testing.T) {
	tests := []struct {
		name string
		seed func(*testing.T, *storepkg.Store)
		run  func(context.Context, *ProjectSession) error
	}{
		{
			name: "volumes",
			run: func(ctx context.Context, session *ProjectSession) error {
				_, err := session.BuildAdaptationProposalVolumesContext(ctx, adapt.ProposalOptions{
					Brief: "adapt", Granularity: domain.AdaptationGranularityFree,
				})
				return err
			},
		},
		{
			name: "details",
			seed: func(t *testing.T, st *storepkg.Store) {
				t.Helper()
				started, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageVolumeReviewPending, started.Revision); err != nil {
					t.Fatal(err)
				}
			},
			run: func(ctx context.Context, session *ProjectSession) error {
				_, err := session.BuildAdaptationProposalDetailsContext(ctx)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			st := storepkg.NewStore(dir)
			if err := st.Init(); err != nil {
				t.Fatal(err)
			}
			if test.seed != nil {
				test.seed(t, st)
			}
			host := &blockingAdaptationPostProcessHost{newBlockingSnapshotProjectHost()}
			session, err := NewProjectSession(ProjectManifest{ID: "adaptation-" + test.name, RootDir: dir, OutputDir: dir}, host)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()

			done := make(chan error, 1)
			go func() { done <- test.run(context.Background(), session) }()
			select {
			case <-host.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("adaptation final snapshot did not start")
			}
			workflow, err := st.Adaptation.LoadPlanningWorkflow()
			if err != nil {
				t.Fatal(err)
			}
			if workflow == nil || workflow.Stage != domain.AdaptationPlanningStageProposalReviewPending {
				t.Fatalf("workflow before final snapshot = %+v, want proposal review pending", workflow)
			}
			impact, err := domain.NewRevisionImpact("adaptation final write", []domain.RevisionImpactItem{{
				ArtifactID: "adaptation-plan", ArtifactKind: "outline", Change: "revise",
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
				Intent: "must wait", Impact: impact, IdempotencyKey: "during-adaptation-" + test.name,
			}); !errors.Is(err, storepkg.ErrActiveRevisionExists) {
				t.Fatalf("revision crossed adaptation %s post-processing: %v", test.name, err)
			}
			close(host.release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("adaptation %s: %v", test.name, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("adaptation %s did not finish", test.name)
			}
			if _, err := st.Revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
				Intent: "after action", Impact: impact, IdempotencyKey: "after-adaptation-" + test.name,
			}); err != nil {
				t.Fatalf("revision did not start after adaptation %s: %v", test.name, err)
			}
		})
	}
}
