package host

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/flow"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestRequireResumeCoreCastGateAllowsPendingOriginalCharacterWorkflow(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := st.SaveFoundationPremise(nil, "confirmed premise"); err != nil {
		t.Fatalf("SaveFoundationPremise: %v", err)
	}
	if _, err := st.BeginOriginalCharacterReview(&domain.PlanningReview{}); err != nil {
		t.Fatalf("BeginOriginalCharacterReview: %v", err)
	}

	if err := RequireResumeCoreCastGate(st, true); err != nil {
		t.Fatalf("pending original Character workflow was blocked: %v", err)
	}
}

func TestRequireResumeCoreCastGateStillBlocksWithoutManagedWorkflow(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	err := RequireResumeCoreCastGate(st, true)
	if err == nil || !strings.Contains(err.Error(), "core cast gate binding does not exist") {
		t.Fatalf("error = %v, want missing managed CoreCast gate", err)
	}
}

func TestInitialResumePromptRoutesPendingOriginalWorkflowToCharacter(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := st.SaveFoundationPremise(nil, "confirmed premise"); err != nil {
		t.Fatalf("SaveFoundationPremise: %v", err)
	}
	if _, err := st.BeginOriginalCharacterReview(&domain.PlanningReview{}); err != nil {
		t.Fatalf("BeginOriginalCharacterReview: %v", err)
	}
	if err := st.Progress.Init("", 0); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}

	h := &Host{store: st}
	prompt := h.initialRoutePrompt("resume", true)
	if !strings.Contains(prompt, "subagent(character") {
		t.Fatalf("resume prompt did not bind Character route: %q", prompt)
	}
	if strings.Contains(prompt, "subagent(architect") {
		t.Fatalf("resume prompt incorrectly routed Architect: %q", prompt)
	}
}

func TestAbortDisablesRoutingAndClearsQueuedMessages(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	coordinator := agentcore.NewAgent()
	h := &Host{
		store:       st,
		coordinator: coordinator,
		events:      make(chan Event, 4),
		streamCh:    make(chan string, 4),
		done:        make(chan struct{}, 1),
		lifecycle:   lifecycleRunning,
	}
	h.router = flow.NewDispatcher(coordinator, st)
	h.router.Enable()
	h.observer = newObserver(coordinator, st, h.emitEvent, h.emitDelta, h.emitClear)
	t.Cleanup(h.observer.finalize)
	coordinator.FollowUp(agentcore.UserMsg("stale route"))

	if !h.abortWithEvent("manual pause", "warn") {
		t.Fatal("abort did not recognize running Host")
	}
	if h.router.IsEnabled() {
		t.Fatal("router remained enabled after abort")
	}
	if coordinator.HasQueuedMessages() {
		t.Fatal("queued messages survived abort")
	}
}
