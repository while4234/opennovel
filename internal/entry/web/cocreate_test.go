package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	adaptpkg "github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type webNormalStructurePlanner struct {
	proposal domain.StructureRevisionProposal
}

func (p webNormalStructurePlanner) PlanStructure(context.Context, domain.StructureRevisionRequest) (domain.StructureRevisionProposal, error) {
	return p.proposal, nil
}

func TestAdaptCoCreateOpenerUsesCurrentModeContractOnly(t *testing.T) {
	opener := adaptCoCreateOpener(domain.AdaptationGranularityFree, domain.AdaptationRewritePreserveDetails, 0.2)
	for _, want := range []string{
		"granularity=free",
		"rewrite_policy=full_rewrite",
		"word_tolerance=disabled",
		"mode_contract=free/full_rewrite",
		"source_reference_policy=optional_background_anchor",
		"不表示目标章对应原著某章",
	} {
		if !strings.Contains(opener, want) {
			t.Fatalf("opener missing %q:\n%s", want, opener)
		}
	}
	for _, bad := range []string{"rewrite_policy_rule=", "required_one_to_one"} {
		if strings.Contains(opener, bad) {
			t.Fatalf("opener should not contain %q:\n%s", bad, opener)
		}
	}
}

func TestProjectCoCreateSuggestionsAndCommitUseDraftPrompt(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("可以开始。", "## 主题\n- 月城追凶", true, "增加双主角", "改成赛博城市")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写一个月城悬疑"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode begin response: %v", err)
	}
	if !response.CoCreate.Ready || response.CoCreate.DraftPrompt != "## 主题\n- 月城追凶" {
		t.Fatalf("co-create state = %+v", response.CoCreate)
	}
	if len(response.CoCreate.Suggestions) != 2 || response.CoCreate.Suggestions[0] != "增加双主角" {
		t.Fatalf("suggestions = %v", response.CoCreate.Suggestions)
	}
	gateBinding, err := storepkg.NewStore(manifest.OutputDir).CoreCast.LoadGateBinding()
	if err != nil {
		t.Fatal(err)
	}
	if gateBinding == nil || gateBinding.Mode != domain.CoreCastModeNormal ||
		gateBinding.DraftRevision <= 0 || gateBinding.DraftHash == "" {
		t.Fatalf("normal co-create did not persist the Character brief binding: %+v", gateBinding)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.prepareRulesCalls != 1 || fake.preparedRulesPrompt != "## 主题\n- 月城追凶" {
		t.Fatalf("PrepareUserRules calls=%d prompt=%q", fake.prepareRulesCalls, fake.preparedRulesPrompt)
	}
	if fake.startPreparedCalls != 1 {
		t.Fatalf("commit must start Foundation generation before Character review; StartPrepared calls = %d, want 1", fake.startPreparedCalls)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil || review.Status != domain.PlanningReviewStatusCollecting || review.Kind != domain.PlanningReviewKindFoundation {
		t.Fatalf("planning checkpoint = %+v err=%v", review, err)
	}
	if !strings.Contains(review.StartPrompt, "[创作要求]\n## 主题\n- 月城追凶") {
		t.Fatalf("saved start prompt should wrap draft prompt, got %q", review.StartPrompt)
	}
	if _, err := os.Stat(filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))); err != nil {
		t.Fatalf("co-create checkpoint should remain available for safe downstream rollback: %v", err)
	}
}

func TestProjectCoCreateAdvancesStaleCoreCastGateForNewSemanticDraft(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Gate Advance")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if _, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode: domain.CoreCastModeNormal, DraftRevision: 8, DraftHash: "previous-draft",
	}); err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("ready", "## New semantic draft\n- Keep all confirmed discussion.", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"new direction"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	binding, err := st.CoreCast.LoadGateBinding()
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil || binding.DraftRevision != 9 || binding.DraftHash == "previous-draft" {
		t.Fatalf("advanced gate = %+v", binding)
	}
	checkpoint, err := os.ReadFile(filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(checkpoint, []byte("New semantic draft")) {
		t.Fatalf("checkpoint did not preserve the new draft: %s", checkpoint)
	}
}

func TestInitialFoundationStartFailureIsRetryableInSameSession(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Foundation Start Retry")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("ready", "## Premise\n- Retry startup", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"retry startup"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status=%d body=%s", rec.Code, rec.Body.String())
	}
	fake.startPreparedErr = errors.New("injected initial startup failure")
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("failed startup unexpectedly succeeded: %s", rec.Body.String())
	}
	assertInitialFoundationStartRolledBack(t, manifest.OutputDir)

	fake.startPreparedErr = nil
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-session retry status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.startPreparedCalls != 2 {
		t.Fatalf("StartPrepared calls=%d, want 2", fake.startPreparedCalls)
	}
}

func TestInitialFoundationStartFailureIsRetryableAfterSessionReopen(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Foundation Restart Retry")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("ready", "## Premise\n- Retry after reopen", true)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"retry after reopen"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status=%d body=%s", rec.Code, rec.Body.String())
	}
	fake.startPreparedErr = errors.New("injected initial startup failure")
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("failed startup unexpectedly succeeded: %s", rec.Body.String())
	}
	assertInitialFoundationStartRolledBack(t, manifest.OutputDir)

	reopenedHost := newFakeProjectHost()
	reopened, err := NewProjectSession(manifest, reopenedHost)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.cocreate == nil {
		t.Fatal("reopened session did not restore the co-create checkpoint")
	}
	if _, err := reopened.CommitCoCreate(context.Background()); err != nil {
		t.Fatalf("reopened-session retry: %v", err)
	}
	if reopenedHost.startPreparedCalls != 1 {
		t.Fatalf("reopened StartPrepared calls=%d, want 1", reopenedHost.startPreparedCalls)
	}
}

func assertInitialFoundationStartRolledBack(t *testing.T, outputDir string) {
	t.Helper()
	review, err := storepkg.NewStore(outputDir).RunMeta.PlanningReview()
	if err != nil {
		t.Fatalf("load rolled-back review: review=%+v err=%v", review, err)
	}
	if review != nil {
		t.Fatalf("initial Foundation start was not exactly rolled back: %+v", review)
	}
}

func TestProjectCoCreatePlanningRevisionRegeneratesPendingPlan(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Planning Revision")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("updated plan", "## Revised\n- Make the heroine proactive\n- Shorten the opening", true)
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindChapterOutline
		review.Brief = "## Old\n- Slow opening\n- Passive heroine"
		review.StartPrompt = "old start prompt"
		review.TargetTotalWords = 5000
		review.CreatedAt = "2026-07-05T00:00:00Z"
		review.UpdatedAt = "2026-07-05T00:01:00Z"
	})

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/planning/revise", bytes.NewBufferString(`{"feedback":"make the heroine proactive and shorten the opening"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("planning revise status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.cocreateCalls != 1 || fake.adaptCoCreateCalls != 0 || fake.adaptProposalCalls != 0 {
		t.Fatalf("co-create routing calls normal=%d adapt=%d proposal=%d", fake.cocreateCalls, fake.adaptCoCreateCalls, fake.adaptProposalCalls)
	}
	if !coCreateHistoryContains(fake.lastCoCreateHistory, "make the heroine proactive") ||
		!coCreateHistoryContains(fake.lastCoCreateHistory, "Passive heroine") {
		t.Fatalf("revision history missing baseline or feedback: %+v", fake.lastCoCreateHistory)
	}
	if fake.prepareRulesCalls != 1 || fake.setWordBudgetCalls != 1 || fake.startPreparedCalls != 1 {
		t.Fatalf("planning restart calls prepare=%d budget=%d start=%d", fake.prepareRulesCalls, fake.setWordBudgetCalls, fake.startPreparedCalls)
	}
	if fake.wordBudget == nil || fake.wordBudget.TargetTotalWords != 5000 {
		t.Fatalf("word budget = %+v, want target 5000", fake.wordBudget)
	}
	if !strings.Contains(fake.preparedRulesPrompt, "Make the heroine proactive") ||
		!strings.Contains(fake.startPreparedPrompt, "target_total_words=5000") {
		t.Fatalf("prepared prompts did not use revised plan: rules=%q start=%q", fake.preparedRulesPrompt, fake.startPreparedPrompt)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatalf("load planning review: %v", err)
	}
	if review == nil || review.Status != domain.PlanningReviewStatusCollecting {
		t.Fatalf("planning review status = %+v, want collecting", review)
	}
	if review.CreatedAt != "2026-07-05T00:00:00Z" || review.TargetTotalWords != 5000 {
		t.Fatalf("planning review metadata = %+v", review)
	}
	if !strings.Contains(review.Brief, "Make the heroine proactive") || strings.Contains(review.Brief, "Passive heroine") {
		t.Fatalf("planning review brief = %q", review.Brief)
	}
}

func TestPlanningRevisionDraftCompletenessDoesNotRejectNarrativeReference(t *testing.T) {
	previous := strings.Repeat("完整旧规划内容。", 250)
	candidate := strings.Repeat("完整新规划内容。", 300) +
		"\n本次修订在保留上一版核心人物动机的同时，已经完整重写全部章节提纲。"

	if draftPromptRegressed(previous, candidate) {
		t.Fatal("a complete expanded draft was rejected for merely mentioning the previous version")
	}
	if !containsDraftOmissionPlaceholder("## 其他设定\n- 其余设定同上。") {
		t.Fatal("a standalone omission placeholder was not detected")
	}
}

func TestProjectCoCreatePlanningRevisionRetriesIncompleteRepairsFromLatestRawCandidate(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Planning Revision Repair Retry")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.structureRepairMaxAttempts = 3
	firstCandidate := "## First candidate\n- 其余设定同上"
	secondCandidate := "## Second candidate\n- 其他设定同上"
	thirdCandidate := "## Third candidate\n- 其余同上"
	finalCandidate := "## Final complete outline\n" + strings.Repeat("- 完整、独立且可执行的章节规划。\n", 200)
	fake.cocreateReplies = []host.CoCreateReply{
		webCoCreateReply("first incomplete candidate", firstCandidate, true),
		webCoCreateReply("second incomplete candidate", secondCandidate, true),
		webCoCreateReply("third incomplete candidate", thirdCandidate, true),
		webCoCreateReply("final repaired candidate", finalCandidate, true),
	}
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindChapterOutline
		review.Brief = "## Existing complete outline\n" + strings.Repeat("- Existing planning detail.\n", 120)
		review.TargetTotalWords = 5000
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+manifest.ID+"/cocreate/planning/revise",
		bytes.NewBufferString(`{"feedback":"repair the final outline without discarding valid details"}`),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("planning revision status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.cocreateCalls != 4 {
		t.Fatalf("co-create calls=%d, want initial generation plus three configured repairs", fake.cocreateCalls)
	}
	if !coCreateHistoryContains(fake.lastCoCreateHistory, thirdCandidate) {
		t.Fatalf("last repair did not use the latest raw candidate: %+v", fake.lastCoCreateHistory)
	}
	if !coCreateHistoryContains(fake.lastCoCreateHistory, "repair the final outline") {
		t.Fatalf("last repair lost the user's review feedback: %+v", fake.lastCoCreateHistory)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if review == nil || review.Status != domain.PlanningReviewStatusCollecting ||
		!strings.Contains(review.Brief, "Final complete outline") {
		t.Fatalf("final repaired outline was not accepted: %+v", review)
	}
}

func TestProjectCoCreatePlanningRevisionAsyncOutlivesHTTPContext(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Async CoCreate Planning Revision")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply(
		"updated plan",
		"## Revised\n- Make the heroine proactive\n- Shorten the opening",
		true,
	)
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindChapterOutline
		review.Brief = "## Old\n- Slow opening\n- Passive heroine"
		review.TargetTotalWords = 5000
	})

	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+manifest.ID+"/cocreate/planning/revise",
		bytes.NewBufferString(`{"feedback":"make the heroine proactive","async":true,"idempotency_key":"planning-http-canceled"}`),
	).WithContext(requestContext)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("async planning revise status = %d body=%s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		ActionID string `json:"action_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	action := waitForActionStatus(t, server.sessions.sessions[manifest.ID].actions, accepted.ActionID, ActionStatusCompleted)
	if action.Error != "" || fake.cocreateCalls != 1 {
		t.Fatalf("completed action=%+v cocreateCalls=%d", action, fake.cocreateCalls)
	}
}

func TestProjectCoCreatePlanningRevisionFailureReleasesActionForFreshFeedback(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Retry CoCreate Planning Revision")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateErr = context.Canceled
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindChapterOutline
		review.Brief = "## Original review draft\n- Keep this until a revision succeeds"
		review.TargetTotalWords = 5000
	})
	session := server.sessions.sessions[manifest.ID]

	firstID := startAsyncPlanningRevisionTestRequest(
		t,
		server,
		manifest.ID,
		`{"feedback":"first feedback","async":true,"idempotency_key":"planning-failure-1"}`,
	)
	failed := waitForActionStatus(t, session.actions, firstID, ActionStatusFailed)
	if !session.waitForActionsIdle(2 * time.Second) {
		t.Fatal("failed planning revision did not release project action ownership")
	}
	if !failed.Recoverable || session.hasActiveAction() {
		t.Fatalf("failed action did not release ownership: action=%+v active=%v", failed, session.hasActiveAction())
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil || review.Status != domain.PlanningReviewStatusPending ||
		!strings.Contains(review.Brief, "Keep this until a revision succeeds") {
		t.Fatalf("failed revision changed review: review=%+v err=%v", review, err)
	}

	fake.cocreateErr = nil
	fake.cocreateReply = webCoCreateReply(
		"updated plan",
		"## Revised\n- Apply the new feedback cleanly",
		true,
	)
	secondID := startAsyncPlanningRevisionTestRequest(
		t,
		server,
		manifest.ID,
		`{"feedback":"new replacement feedback","async":true,"idempotency_key":"planning-retry-2"}`,
	)
	waitForActionStatus(t, session.actions, secondID, ActionStatusCompleted)
	if !session.waitForActionsIdle(2 * time.Second) {
		t.Fatal("successful retry did not release project action ownership")
	}
	if session.hasActiveAction() {
		t.Fatal("successful retry left project action ownership active")
	}
}

func startAsyncPlanningRevisionTestRequest(
	t *testing.T,
	server *Server,
	projectID string,
	body string,
) string {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+projectID+"/cocreate/planning/revise",
		bytes.NewBufferString(body),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("async planning revision status=%d body=%s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		ActionID string `json:"action_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accepted action: %v", err)
	}
	if accepted.ActionID == "" {
		t.Fatal("accepted action has no action_id")
	}
	return accepted.ActionID
}

func TestRolledBackCoCreateDraftCanBeRevisedAndRegenerateFoundation(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Rolled Back CoCreate Draft")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply(
		"updated draft",
		"## Revised premise\n- Remove the ensemble routes\n- Keep one main relationship",
		true,
	)
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindBlueprint
		review.Brief = "## Old draft\n- Multiple ensemble routes"
		review.StartPrompt = host.BuildStartPrompt(review.Brief)
		review.TargetTotalWords = 100_000
	})
	preview, err := st.RollbackPreview()
	if err != nil || preview.TargetStage != domain.RollbackStageDraft {
		t.Fatalf("rollback preview = %+v, %v", preview, err)
	}
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash}); err != nil {
		t.Fatal(err)
	}
	// Simulate projects rolled back by older builds: Foundation files were
	// deleted, but the planning review still claimed the deleted binding was
	// approved. The live v2 project reported by the user has this exact shape.
	runMetaPath := filepath.Join(manifest.OutputDir, "meta", "run.json")
	runMetaJSON, err := os.ReadFile(runMetaPath)
	if err != nil {
		t.Fatal(err)
	}
	var runMeta domain.RunMeta
	if err := json.Unmarshal(runMetaJSON, &runMeta); err != nil {
		t.Fatal(err)
	}
	runMeta.PlanningReview.FoundationStatus = domain.FoundationReviewStatusApproved
	runMeta.PlanningReview.FoundationRevision = 4
	runMeta.PlanningReview.FoundationAuditSignature = "deleted-foundation-audit"
	runMeta.PlanningReview.CoreCastSignature = "preserved-core-cast"
	runMeta.PlanningReview.FoundationGeneration = 1
	runMeta.PlanningReview.FoundationSections = []string{"premise", "characters", "world_rules", "planned_relationships"}
	runMeta.PlanningReview.FoundationConfirmedAt = "2026-07-27T11:14:16Z"
	runMetaJSON, err = json.MarshalIndent(runMeta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runMetaPath, append(runMetaJSON, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+manifest.ID+"/cocreate/planning/revise",
		bytes.NewBufferString(`{"feedback":"remove ensemble routes and keep only one main relationship"}`),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rolled-back draft revise status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.cocreateCalls != 1 || fake.startPreparedCalls != 0 {
		t.Fatalf("draft revision calls co-create=%d start=%d", fake.cocreateCalls, fake.startPreparedCalls)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil || review.Kind != domain.PlanningReviewKindBlueprint ||
		review.Status != domain.PlanningReviewStatusPending {
		t.Fatalf("revised draft review=%+v err=%v", review, err)
	}
	if !strings.Contains(review.Brief, "Remove the ensemble routes") {
		t.Fatalf("revised draft was not saved: %q", review.Brief)
	}
	if review.FoundationStatus != "" || review.FoundationRevision != 0 ||
		review.FoundationAuditSignature != "" || review.FoundationConfirmedAt != "" {
		t.Fatalf("revised draft restored a deleted Foundation binding: %+v", review)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/confirm", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rolled-back draft confirm status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.startPreparedCalls != 1 || !strings.Contains(fake.startPreparedPrompt, "Remove the ensemble routes") {
		t.Fatalf("Foundation regeneration calls=%d prompt=%q", fake.startPreparedCalls, fake.startPreparedPrompt)
	}
	review, err = st.RunMeta.PlanningReview()
	if err != nil || review == nil || review.Kind != domain.PlanningReviewKindFoundation ||
		review.Status != domain.PlanningReviewStatusCollecting {
		t.Fatalf("Foundation regeneration review=%+v err=%v", review, err)
	}
}

func TestProjectCoCreatePlanningRevisionTargetsSingleChapter(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Planning Revision Target")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("updated chapter plan", "## Revised\n- Give chapter two a sharper reversal", true)
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	seedChapters := []domain.OutlineEntry{
		{Chapter: 1, Title: "Opening", CoreEvent: "A slow start", Hook: "a choice", Scenes: []string{"arrival"}},
		{Chapter: 2, Title: "Reversal", CoreEvent: "The heroine takes charge", Hook: "a consequence", Scenes: []string{"confrontation"}},
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1, Title: "Opening volume", Theme: "Agency", Arcs: []domain.ArcOutline{{Index: 1, Title: "First choice", Goal: "the heroine takes agency", EstimatedChapters: 2, Chapters: seedChapters}},
	}}); err != nil {
		t.Fatalf("seed layered outline: %v", err)
	}
	if err := st.Outline.SaveOutline(seedChapters); err != nil {
		t.Fatalf("seed flat outline: %v", err)
	}
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindChapterOutline
		review.Brief = "## Old\n- Chapter 2 lacks agency"
		review.StartPrompt = "old start prompt"
		review.TargetTotalWords = 8000
		review.CreatedAt = "2026-07-05T00:00:00Z"
		review.UpdatedAt = "2026-07-05T00:01:00Z"
	})

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/planning/revise", bytes.NewBufferString(`{"instruction":"tighten chapter two around the reversal","scope":"chapter","chapter":2}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("planning revise status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.cocreateCalls != 0 {
		t.Fatalf("targeted user instruction must not be rewritten by another co-create model call, got %d", fake.cocreateCalls)
	}
	if fake.resumeCalls != 1 || fake.startPreparedCalls != 0 {
		t.Fatalf("targeted revision should resume durable repair router: resume=%d start=%d", fake.resumeCalls, fake.startPreparedCalls)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil || review.Status != domain.PlanningReviewStatusCollecting || review.Kind != domain.PlanningReviewKindVolumeSplit {
		t.Fatalf("targeted revision review=%+v err=%v", review, err)
	}
	work, err := st.OriginalPlanningAudits.NextWork(st.Outline)
	if err != nil || work == nil || work.Kind != "repair_arc" || work.Volume != 1 || work.Arc != 1 {
		t.Fatalf("targeted revision work=%+v err=%v", work, err)
	}
	if work.Audit == nil || len(work.Audit.Issues) == 0 || work.Audit.Issues[0].RepairInstruction != "tighten chapter two around the reversal" {
		t.Fatalf("raw user instruction was not preserved: %+v", work.Audit)
	}
}

func TestProjectCoCreatePlanningConfirmStartsBlueprintGeneration(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Blueprint Review")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	if err := st.RunMeta.SetWordBudget(domain.NewWordBudget(100_000, domain.WordBudgetSourceAPI)); err != nil {
		t.Fatalf("seed word budget: %v", err)
	}
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindBlueprint
		review.Brief = "## Draft plan before structured planning"
		review.StartPrompt = "start prompt"
		review.TargetTotalWords = 0
		review.CreatedAt = "2026-07-05T00:00:00Z"
		review.UpdatedAt = "2026-07-05T00:01:00Z"
	})

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/confirm", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("planning confirm status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if fake.startPreparedCalls != 1 || !strings.Contains(fake.startPreparedPrompt, "target_total_words=100000") || fake.resumeCalls != 0 {
		t.Fatalf("blueprint generation calls=%d prompt=%q resume=%d", fake.startPreparedCalls, fake.startPreparedPrompt, fake.resumeCalls)
	}
}

func TestProjectCoCreatePlanningConfirmVolumeReviewResumesAuditedBatchRouter(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Volume Review")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	_ = st.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "earned independence"})
	_ = st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1, Title: "Choice", Theme: "Agency", Arcs: []domain.ArcOutline{
			{Index: 1, Title: "Refusal", Goal: "reject the imposed role", EstimatedChapters: 3},
			{Index: 2, Title: "Countermove", Goal: "build an alternative", EstimatedChapters: 3},
		},
	}})
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindVolumeSplit
		review.Brief = "audited original"
		review.TargetTotalWords = 30_000
	})

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/confirm", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.resumeCalls != 1 || fake.continueCalls != 0 {
		t.Fatalf("volume approval must resume durable router: resume=%d continue=%d", fake.resumeCalls, fake.continueCalls)
	}
	review, _ := st.RunMeta.PlanningReview()
	if review == nil || review.Status != domain.PlanningReviewStatusCollecting || review.Kind != domain.PlanningReviewKindVolumeSplit {
		t.Fatalf("review=%+v", review)
	}
}

func TestProjectCoCreatePlanningRevisionTargetsOneVolumeAndReentersAudit(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Volume Revision")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("updated final volume", "## Revised\n- Close every promised plot in volume three", true)
	st := storepkg.NewStore(manifest.OutputDir)
	establishWebApprovedNormalFoundationFixture(t, st)
	volumes := []domain.VolumeOutline{
		{Index: 1, Title: "Opening", Theme: "survival", Arcs: []domain.ArcOutline{{Index: 1, Title: "A", Goal: "escape", EstimatedChapters: 3}, {Index: 2, Title: "B", Goal: "counter", EstimatedChapters: 3}}},
		{Index: 2, Title: "Middle", Theme: "power", Arcs: []domain.ArcOutline{{Index: 1, Title: "C", Goal: "return", EstimatedChapters: 3}, {Index: 2, Title: "D", Goal: "challenge", EstimatedChapters: 3}}},
		{Index: 3, Title: "End", Theme: "closure", Arcs: []domain.ArcOutline{{Index: 1, Title: "E", Goal: "reveal", EstimatedChapters: 3}, {Index: 2, Title: "F", Goal: "finish", EstimatedChapters: 3}}},
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
		review.Status = domain.PlanningReviewStatusPending
		review.Kind = domain.PlanningReviewKindVolumeSplit
		review.Brief = "audited original"
		review.TargetTotalWords = 100_000
	})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/planning/revise", bytes.NewBufferString(`{"scope":"volume","volume_index":3,"instruction":"make the final volume close every main plot"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.resumeCalls != 1 || fake.startPreparedCalls != 0 {
		t.Fatalf("targeted volume revision must resume audit router: resume=%d start=%d", fake.resumeCalls, fake.startPreparedCalls)
	}
	if fake.cocreateCalls != 0 {
		t.Fatalf("targeted user instruction must not be rewritten by another co-create model call, got %d", fake.cocreateCalls)
	}
	review, _ := st.RunMeta.PlanningReview()
	if review == nil || review.Status != domain.PlanningReviewStatusCollecting || review.Kind != domain.PlanningReviewKindBlueprint {
		t.Fatalf("review=%+v", review)
	}
	work, err := st.OriginalPlanningAudits.NextSkeletonWork(st.Outline)
	if err != nil || work == nil || work.Kind != "repair_skeleton_volume" || work.Volume != 3 {
		t.Fatalf("queued work=%+v err=%v", work, err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/planning/revise", bytes.NewBufferString(`{"scope":"volume","volume_index":3,"instruction":"also make the heroine pay the promised personal cost"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second feedback status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.resumeCalls != 1 {
		t.Fatalf("additional feedback should join the active candidate without starting a second run: resume=%d", fake.resumeCalls)
	}
	work, err = st.OriginalPlanningAudits.NextSkeletonWork(st.Outline)
	if err != nil || work == nil || work.Audit == nil ||
		!strings.Contains(work.Audit.Issues[0].RepairInstruction, "close every main plot") ||
		!strings.Contains(work.Audit.Issues[0].RepairInstruction, "promised personal cost") {
		t.Fatalf("multi-round revision feedback=%+v err=%v", work, err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/planning/revise", bytes.NewBufferString(`{"scope":"volume","volume_index":2,"instruction":"redirect this feedback"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "no active original-fiction revision candidate") {
		t.Fatalf("stable target drift was accepted: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectCoCreatePlanningRevisionRejectsInvalidState(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Planning Revision Invalid")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/planning/revise", bytes.NewBufferString(`{"instruction":"   "}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank feedback status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/planning/revise", bytes.NewBufferString(`{"instruction":"revise the plan"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("missing review status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if fake.cocreateCalls != 0 || fake.startPreparedCalls != 0 {
		t.Fatalf("invalid request should not touch model or planning start: co-create=%d start=%d", fake.cocreateCalls, fake.startPreparedCalls)
	}
}

func TestNormalCoCreateTransportRejectsSourceAndAdaptationFieldsBeforeDecoding(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Normal Firewall")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"kind":"normal","initial":"original story","originalNovel":{"title":"forbidden"}}`,
		`{"feedback":"revise","DependencySourceIDs":["source-1"]}`,
		`{"feedback":"revise","adaptationContract":{"event":"forbidden"}}`,
	} {
		path := "/api/projects/" + manifest.ID + "/cocreate/planning/revise"
		if strings.Contains(body, `"kind"`) {
			path = "/api/projects/" + manifest.ID + "/cocreate/begin"
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
			t.Fatalf("normal firewall status=%d body=%s input=%s", rec.Code, rec.Body.String(), body)
		}
	}
}

func TestProjectSessionNormalRevisionPreviewPersistsAndTamperedCandidateIsIsolated(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Normal Revision Service")
	if err != nil {
		t.Fatal(err)
	}
	installFakeSession(t, server, manifest)
	session, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	vol := domain.LegacyStructureID("web-normal", domain.StructureKindVolume, "volume")
	arc := domain.LegacyStructureID("web-normal", domain.StructureKindArc, "arc")
	ch1 := domain.LegacyStructureID("web-normal", domain.StructureKindChapter, "chapter-1")
	ch2 := domain.LegacyStructureID("web-normal", domain.StructureKindChapter, "chapter-2")
	current := []domain.VolumeOutline{{ID: vol, Index: 1, Title: "Volume", Theme: "choice", Arcs: []domain.ArcOutline{{ID: arc, Index: 1, Title: "Arc", Goal: "choose", Chapters: []domain.OutlineEntry{{ID: ch1, Chapter: 1, Title: "One", CoreEvent: "choice", Hook: "cost", Scenes: []string{"choose"}}}}}}}
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, domain.OutlineEntry{ID: ch2, Chapter: 2, Title: "Two", CoreEvent: "cost lands", Hook: "aftermath", Scenes: []string{"pay cost"}})
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("web normal", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	budget, _ := domain.NewDynamicSoftBudget(2, 3000, 5000)
	previewed, err := session.PreviewNormalStructureRevision(context.Background(), webNormalStructurePlanner{proposal: domain.StructureRevisionProposal{
		Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "the consequence needs a chapter"}, Candidate: candidate, SoftBudget: budget,
	}}, domain.StructureRevisionRequest{
		Operation: domain.StructureRevisionAppendChapter, Intent: "add consequence", Stage: domain.ManuscriptStageWriting,
		BaseRevision: 1, Current: current, CurrentSoftBudget: &budget,
	}, "web-preview")
	if err != nil {
		t.Fatal(err)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.ID != previewed.Session.ID {
		t.Fatalf("web persistent session=%+v err=%v", active, err)
	}
	approved, err := session.NormalRevisionService().ApproveImpact(active.ID, active.Revision, "web-impact")
	if err != nil {
		t.Fatal(err)
	}
	tampered := *previewed.Preview
	tampered.Proposal.Candidate[0].Arcs[0].Chapters[1].Title = "tampered after preview"
	if _, err := session.NormalRevisionService().SubmitStructureCandidate(tampered, approved.ID, approved.Revision, "web-tampered"); err == nil {
		t.Fatal("tampered web candidate was accepted")
	}
	formal, _ := st.Outline.LoadLayeredOutline()
	active, _ = st.Revisions.Active()
	if len(formal[0].Arcs[0].Chapters) != 1 || active == nil || active.Stage != domain.RevisionStageCandidateGenerating {
		t.Fatalf("failed candidate escaped isolation: formal=%+v active=%+v", formal, active)
	}
}

func TestNormalRevisionProductionPreviewHandlerUsesRealStageAcrossLifecycle(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	for _, stage := range []domain.ManuscriptStage{
		domain.ManuscriptStageProposalComplete,
		domain.ManuscriptStageOutlineComplete,
		domain.ManuscriptStageWriting,
		domain.ManuscriptStageComplete,
	} {
		t.Run(string(stage), func(t *testing.T) {
			manifest, err := server.store.CreateProject("handler " + string(stage))
			if err != nil {
				t.Fatal(err)
			}
			installFakeSession(t, server, manifest)
			st := storepkg.NewStore(manifest.OutputDir)
			establishWebApprovedNormalFoundationFixture(t, st)
			vol := domain.LegacyStructureID("handler-"+string(stage), domain.StructureKindVolume, "vol")
			arc := domain.LegacyStructureID("handler-"+string(stage), domain.StructureKindArc, "arc")
			ch1 := domain.LegacyStructureID("handler-"+string(stage), domain.StructureKindChapter, "ch-1")
			ch2 := domain.LegacyStructureID("handler-"+string(stage), domain.StructureKindChapter, "ch-2")
			current := []domain.VolumeOutline{{ID: vol, Index: 1, Title: "Volume", Theme: "change", Arcs: []domain.ArcOutline{{ID: arc, Index: 1, Title: "Arc", Goal: "choose", Chapters: []domain.OutlineEntry{{ID: ch1, Chapter: 1, Title: "One", CoreEvent: "choice", Hook: "cost", Scenes: []string{"choose"}}}}}}}
			candidate := domain.CloneStructureSnapshot(current)
			candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, domain.OutlineEntry{ID: ch2, Chapter: 2, Title: "Two", CoreEvent: "cost lands", Hook: "aftermath", Scenes: []string{"pay"}})
			if err := st.Outline.SaveLayeredOutline(current); err != nil {
				t.Fatal(err)
			}
			if err := st.Progress.Init("handler", 1); err != nil {
				t.Fatal(err)
			}
			if err := st.Progress.UpdatePhase(domain.PhaseOutline); err != nil {
				t.Fatal(err)
			}
			switch stage {
			case domain.ManuscriptStageProposalComplete:
				setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
					review.Status = domain.PlanningReviewStatusPending
					review.Kind = domain.PlanningReviewKindVolumeSplit
					review.Brief = "proposal"
				})
			case domain.ManuscriptStageOutlineComplete:
				setWebPlanningReviewPreservingFoundation(t, st, func(review *domain.PlanningReview) {
					review.Status = domain.PlanningReviewStatusPending
					review.Kind = domain.PlanningReviewKindChapterOutline
					review.Brief = "outline"
				})
			default:
				_ = st.Progress.UpdatePhase(domain.PhaseWriting)
				if stage == domain.ManuscriptStageComplete {
					_ = st.Progress.UpdatePhase(domain.PhaseComplete)
				}
			}
			budget, _ := domain.NewDynamicSoftBudget(2, 3000, 5000)
			body, _ := json.Marshal(webNormalRevisionPreviewRequest{
				Operation: domain.StructureRevisionAppendChapter, Intent: "add consequence", IdempotencyKey: "handler-" + string(stage),
				Proposal: domain.StructureRevisionProposal{
					Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "consequence"},
					Candidate:  candidate, SoftBudget: budget,
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/revision/preview", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("handler status=%d body=%s", rec.Code, rec.Body.String())
			}
			var response struct {
				Revision host.NormalStructureRevisionPreview `json:"revision"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Revision.Preview == nil || response.Revision.Preview.Stage != stage || response.Revision.Session == nil {
				t.Fatalf("real handler stage=%+v want=%s", response.Revision, stage)
			}
		})
	}
}

func TestProjectCoCreateCheckpointRestoresAfterSessionRestart(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Restore")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("继续确认。", "## 方向\n- 保留慢热纯爱", false, "加强日常互动")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写一个纯爱故事"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
	if _, err := os.Stat(checkpointPath); err != nil {
		t.Fatalf("co-create checkpoint should exist at %s: %v", checkpointPath, err)
	}

	server.sessions.CloseProject(manifest.ID)
	installFakeSession(t, server, manifest)

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var restored struct {
		CoCreate *webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&restored); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if restored.CoCreate == nil || !restored.CoCreate.Active {
		t.Fatalf("restored co-create state = %+v, want active", restored.CoCreate)
	}
	if restored.CoCreate.DraftPrompt != "## 方向\n- 保留慢热纯爱" {
		t.Fatalf("restored draft = %q", restored.CoCreate.DraftPrompt)
	}
	if len(restored.CoCreate.Messages) != 2 || restored.CoCreate.Messages[0].Role != "user" || restored.CoCreate.Messages[1].Role != "assistant" {
		t.Fatalf("restored messages = %+v", restored.CoCreate.Messages)
	}
}

func TestNormalRevisionHTTPRecoversCrashedPublicationAndRejectsReplay(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Normal Revision Publication")
	if err != nil {
		t.Fatal(err)
	}
	installFakeSession(t, server, manifest)
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	establishWebApprovedNormalFoundationFixture(t, st)
	volumeID := domain.LegacyStructureID("http-publication", domain.StructureKindVolume, "volume")
	arcID := domain.LegacyStructureID("http-publication", domain.StructureKindArc, "arc")
	chapter1 := domain.LegacyStructureID("http-publication", domain.StructureKindChapter, "chapter-1")
	chapter2 := domain.LegacyStructureID("http-publication", domain.StructureKindChapter, "chapter-2")
	current := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "choice",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "choose",
			Chapters: []domain.OutlineEntry{{ID: chapter1, Chapter: 1, Title: "One", CoreEvent: "choice begins", Hook: "cost", Scenes: []string{"choose"}}},
		}},
	}}
	candidate := domain.CloneStructureSnapshot(current)
	candidate[0].Arcs[0].Chapters = append(candidate[0].Arcs[0].Chapters, domain.OutlineEntry{
		ID: chapter2, Chapter: 2, Title: "Two", CoreEvent: "cost lands", Hook: "aftermath", Scenes: []string{"pay"},
	})
	if err := st.Outline.SaveLayeredOutline(current); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{NovelName: "http", Phase: domain.PhaseWriting, Flow: domain.FlowWriting, Layered: true, TotalChapters: 1}); err != nil {
		t.Fatal(err)
	}
	budget, _ := domain.NewDynamicSoftBudget(2, 3000, 5000)
	previewBody, _ := json.Marshal(webNormalRevisionPreviewRequest{
		Operation: domain.StructureRevisionAppendChapter, Intent: "add the consequence", BaseRevision: 1,
		CurrentSoftBudget: &budget, IdempotencyKey: "http-preview",
		Proposal: domain.StructureRevisionProposal{
			Assessment: domain.ContentAdditionAssessment{NeedsAdditionalChapters: true, Reason: "the consequence needs its own chapter"},
			Candidate:  candidate, SoftBudget: budget,
		},
	})
	previewRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/revision/preview", bytes.NewReader(previewBody))
	previewResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var previewed struct {
		Revision host.NormalStructureRevisionPreview `json:"revision"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &previewed); err != nil {
		t.Fatal(err)
	}
	if previewed.Revision.Session == nil || previewed.Revision.Preview == nil {
		t.Fatalf("preview response=%s", previewResponse.Body.String())
	}

	postCommand := func(command webNormalRevisionCommandRequest) (*domain.RevisionSession, int, string) {
		t.Helper()
		body, _ := json.Marshal(command)
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/revision/command", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return nil, rec.Code, rec.Body.String()
		}
		var response struct {
			Revision *domain.RevisionSession `json:"revision"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Revision, rec.Code, rec.Body.String()
	}

	session := previewed.Revision.Session
	session, status, body := postCommand(webNormalRevisionCommandRequest{
		Action: "approve_impact", ExpectedRevision: session.Revision, IdempotencyKey: "http-impact",
	})
	if status != http.StatusOK {
		t.Fatalf("approve impact status=%d body=%s", status, body)
	}
	for session.Stage != domain.RevisionStageReadyToPublish {
		if session.Stage != domain.RevisionStageCandidateGenerating || len(session.Approvals) >= len(session.ApprovalStages) {
			t.Fatalf("unexpected staged session=%+v", session)
		}
		stage := session.ApprovalStages[len(session.Approvals)].ID
		command := webNormalRevisionCommandRequest{ExpectedRevision: session.Revision, IdempotencyKey: "http-candidate-" + stage}
		switch stage {
		case domain.NormalApprovalStructure:
			command.Action, command.Preview = "submit_structure", previewed.Revision.Preview
		case domain.NormalApprovalOutline:
			command.Action, command.Candidate = "submit_details", candidate
		case domain.NormalApprovalProse:
			command.Action = "submit_prose_intents"
		default:
			t.Fatalf("unexpected approval stage %q", stage)
		}
		session, status, body = postCommand(command)
		if status != http.StatusOK {
			t.Fatalf("submit %s status=%d body=%s", stage, status, body)
		}
		evidence := make([]domain.RevisionAuditEvidence, 0, len(session.AuditExpectations))
		for _, expected := range session.AuditExpectations {
			evidence = append(evidence, domain.RevisionAuditEvidence{
				Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter,
				ToChapter: expected.ToChapter, ContentSignature: expected.ContentSignature, Passed: true,
			})
		}
		session, status, body = postCommand(webNormalRevisionCommandRequest{
			Action: "record_audit", ExpectedRevision: session.Revision, IdempotencyKey: "http-audit-" + stage, Evidence: evidence,
		})
		if status != http.StatusOK {
			t.Fatalf("audit %s status=%d body=%s", stage, status, body)
		}
		session, status, body = postCommand(webNormalRevisionCommandRequest{
			Action: "approve_stage", ExpectedRevision: session.Revision, IdempotencyKey: "http-approve-" + stage,
		})
		if status != http.StatusOK {
			t.Fatalf("approve %s status=%d body=%s", stage, status, body)
		}
	}

	publishInput := storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "http-publish"}
	if _, err := st.Revisions.Publish(domain.NormalRevisionPolicy{}, publishInput); !errors.Is(err, storepkg.ErrRevisionCommandInProgress) {
		t.Fatalf("HTTP-ready normal session accepted plain Publish: %v", err)
	}
	active, err := st.Revisions.Active()
	if err != nil || active == nil || active.ID != session.ID || active.Stage != domain.RevisionStageReadyToPublish {
		t.Fatalf("plain Publish consumed HTTP lifecycle: active=%+v err=%v", active, err)
	}
	formalBeforeOwner, err := st.Outline.LoadLayeredOutline()
	if err != nil || len(formalBeforeOwner) != 1 || len(formalBeforeOwner[0].Arcs[0].Chapters) != 1 {
		t.Fatalf("plain Publish changed HTTP formal structure: structure=%+v err=%v", formalBeforeOwner, err)
	}
	_, owner, err := st.Revisions.ValidatePublishWithOwner(domain.NormalRevisionPolicy{}, publishInput)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PublishLayeredStructureForRevision(owner, candidate, publishInput.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	// Closing and recreating the real project session simulates a process crash
	// between formal replacement and the RevisionStore commit.
	server.sessions.CloseProject(manifest.ID)
	installFakeSession(t, server, manifest)
	recovered := storepkg.NewStore(manifest.OutputDir)
	recoveredStructure, err := recovered.Outline.LoadLayeredOutline()
	if err != nil || len(recoveredStructure) != 1 {
		t.Fatalf("restart did not restore bound prepublish snapshot: volumes=%d err=%v", len(recoveredStructure), err)
	}

	publishCommand := webNormalRevisionCommandRequest{
		Action: "publish", ExpectedRevision: session.Revision, IdempotencyKey: publishInput.IdempotencyKey, Preview: previewed.Revision.Preview,
	}
	published, status, body := postCommand(publishCommand)
	if status != http.StatusOK || published == nil || published.Stage != domain.RevisionStageCompleted {
		t.Fatalf("publish status=%d revision=%+v body=%s", status, published, body)
	}
	formal, _ := storepkg.NewStore(manifest.OutputDir).Outline.LoadLayeredOutline()
	if len(formal) != 1 || len(formal[0].Arcs[0].Chapters) != 2 {
		t.Fatalf("HTTP publication formal structure=%+v", formal)
	}
	if _, replayStatus, _ := postCommand(publishCommand); replayStatus == http.StatusOK {
		t.Fatal("real HTTP replay reused completed normal publication")
	}
	formalAfterReplay, _ := storepkg.NewStore(manifest.OutputDir).Outline.LoadLayeredOutline()
	if len(formalAfterReplay) != 1 || len(formalAfterReplay[0].Arcs[0].Chapters) != 2 {
		t.Fatalf("rejected HTTP replay changed formal structure=%+v", formalAfterReplay)
	}
}

func TestProjectCoCreateRecoveryIgnoredAfterWritingStarts(t *testing.T) {
	tests := []struct {
		name       string
		legacyOnly bool
	}{
		{name: "checkpoint"},
		{name: "legacy log", legacyOnly: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
			defer server.Close()
			manifest, err := server.store.CreateProject("Writing Must Win")
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			st := storepkg.NewStore(manifest.OutputDir)
			if err := st.Progress.Save(&domain.Progress{
				NovelName:         "Writing Must Win",
				Phase:             domain.PhaseWriting,
				CurrentChapter:    3,
				TotalChapters:     33,
				CompletedChapters: []int{1, 2},
			}); err != nil {
				t.Fatalf("save writing progress: %v", err)
			}

			sessionsDir := filepath.Join(manifest.OutputDir, "meta", "sessions")
			if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
				t.Fatalf("mkdir sessions dir: %v", err)
			}
			if test.legacyOnly {
				entry, err := json.Marshal(webCoCreateLogEntry{
					InputHistory: []host.CoCreateMessage{{Role: "user", Content: "stale normal novel direction"}},
					ParsedDraft:  "## stale direction",
					ParsedReady:  true,
				})
				if err != nil {
					t.Fatalf("marshal legacy log: %v", err)
				}
				logPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateLogRelPath))
				if err := os.WriteFile(logPath, append(entry, '\n'), 0o644); err != nil {
					t.Fatalf("write legacy log: %v", err)
				}
			} else {
				checkpoint, err := json.Marshal(webCoCreateCheckpoint{
					Version: webCoCreateCheckpointVersion,
					Kind:    webCoCreateKindNormal,
					Session: startup.CoCreateSnapshot{
						History:     []host.CoCreateMessage{{Role: "user", Content: "stale normal novel direction"}},
						DraftPrompt: "## stale direction",
						Ready:       true,
					},
				})
				if err != nil {
					t.Fatalf("marshal checkpoint: %v", err)
				}
				checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
				if err := os.WriteFile(checkpointPath, checkpoint, 0o644); err != nil {
					t.Fatalf("write checkpoint: %v", err)
				}
			}

			installFakeSession(t, server, manifest)

			req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
			}
			var restored struct {
				CoCreate *webCoCreateState `json:"cocreate"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&restored); err != nil {
				t.Fatalf("decode snapshot response: %v", err)
			}
			if restored.CoCreate != nil {
				t.Fatalf("stale co-create should not override writing progress: %+v", restored.CoCreate)
			}
			checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
			if _, err := os.Stat(checkpointPath); !os.IsNotExist(err) {
				t.Fatalf("stale checkpoint should be absent, stat err=%v", err)
			}
		})
	}
}

func TestProjectAdaptCoCreateCheckpointRestoresModeAndCommits(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Restore")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	opener := strings.Join([]string{
		"Please adapt this novel.",
		"",
		"granularity=free",
		"rewrite_policy=" + domain.AdaptationRewritePolicyForGranularity(domain.AdaptationGranularityFree),
		"word_tolerance=0.15",
	}, "\n")
	checkpoint := webCoCreateCheckpoint{
		Version: webCoCreateCheckpointVersion,
		Kind:    webCoCreateKindAdapt,
		Session: startup.CoCreateSnapshot{
			History: []host.CoCreateMessage{
				{Role: "user", Content: opener},
				{Role: "user", Content: "core adaptation idea"},
			},
			DraftPrompt:     "## restored draft\n- keep current direction",
			DraftHistoryLen: 2,
			Ready:           false,
		},
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
	if err := os.MkdirAll(filepath.Dir(checkpointPath), 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(checkpointPath, data, 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var restored struct {
		CoCreate *webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&restored); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if restored.CoCreate == nil || restored.CoCreate.AdaptMode != domain.AdaptationGranularityFree || restored.CoCreate.WordTolerance != 0 {
		t.Fatalf("restored adapt options = %+v", restored.CoCreate)
	}
	if restored.CoCreate.SourceFile != "source.txt" || !restored.CoCreate.CanStart {
		t.Fatalf("restored source/can_start = %+v", restored.CoCreate)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptStartCalls != 1 {
		t.Fatalf("Character workflow start calls = %d, want 1", fake.adaptStartCalls)
	}
	if fake.adaptProposalCalls != 0 {
		t.Fatalf("adaptation proposal ran before Character confirmation: calls=%d", fake.adaptProposalCalls)
	}
	if !strings.Contains(fake.startPreparedPrompt, "keep current direction") {
		t.Fatalf("Character brief = %q, want restored draft", fake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateCommitRepairsRestoredStaleDraft(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Stale Commit")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	opener := strings.Join([]string{
		"Please adapt this novel.",
		"",
		"granularity=free",
		"rewrite_policy=" + domain.AdaptationRewritePolicyForGranularity(domain.AdaptationGranularityFree),
		"word_tolerance=disabled",
	}, "\n")
	oldDraftRaw := "<reply>old draft</reply><draft>## old draft\n- early direction</draft><ready>true</ready><suggestions></suggestions>"
	checkpoint := webCoCreateCheckpoint{
		Version: webCoCreateCheckpointVersion,
		Kind:    webCoCreateKindAdapt,
		Session: startup.CoCreateSnapshot{
			History: []host.CoCreateMessage{
				{Role: "user", Content: opener},
				{Role: "user", Content: "early direction"},
				{Role: "assistant", Content: oldDraftRaw},
				{Role: "user", Content: "expand to 24 target chapters"},
			},
			DraftPrompt: "## old draft\n- early direction",
			Ready:       true,
		},
		SourceFile: "source.txt",
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
	if err := os.MkdirAll(filepath.Dir(checkpointPath), 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(checkpointPath, data, 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.adaptCoCreateReply = webCoCreateReply("draft repaired", "## repaired draft\n- expand to 24 target chapters", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("repair co-create calls = %d, want 1", fake.adaptCoCreateCalls)
	}
	if fake.adaptStartCalls != 1 || fake.adaptProposalCalls != 0 {
		t.Fatalf("Character/proposal calls = %d/%d, want 1/0", fake.adaptStartCalls, fake.adaptProposalCalls)
	}
	if !strings.Contains(fake.startPreparedPrompt, "24 target chapters") || strings.Contains(fake.startPreparedPrompt, "old draft") {
		t.Fatalf("Character brief = %q, want repaired draft only", fake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateLongFormCommitStartsCharacterWorkflow(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Volume Review")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptCoCreateReply = webCoCreateReply("long-form direction ready", "## Adaptation Draft\n- expand into 20 target chapters", true)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"expand into 20 target chapters"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt commit status = %d body=%s", rec.Code, rec.Body.String())
	}

	if fake.adaptStartCalls != 1 || fake.adaptProposalCalls != 0 || fake.adaptConfirmCalls != 0 {
		t.Fatalf("Character/proposal/writing calls = %d/%d/%d, want 1/0/0",
			fake.adaptStartCalls, fake.adaptProposalCalls, fake.adaptConfirmCalls)
	}
	if !strings.Contains(fake.startPreparedPrompt, "20 target chapters") {
		t.Fatalf("Character brief = %q, want long-form direction", fake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateBeginRequiresCurrentDossier(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Missing Dossier")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"expand into 20 target chapters"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("adapt begin status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 0 {
		t.Fatalf("adapt co-create should not start, calls=%d", fake.adaptCoCreateCalls)
	}
}

func TestProjectAdaptCoCreateBeginUsesLoadedPreparedSnapshot(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Loaded Snapshot")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	sourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
	if err := os.WriteFile(sourcePath, []byte("Chapter 1: Changed\nTHIS WOULD NOT MATCH THE STORED SNAPSHOT\n"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.adaptCoCreateReply = webCoCreateReply("ready", "## Adapt brief\n- use the loaded source analysis", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"keep the loaded novel analysis"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("adapt co-create calls = %d, want 1", fake.adaptCoCreateCalls)
	}
}

func TestProjectAdaptCoCreateCommitRepairsRestoredRegressedDraft(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Fresh Bad Commit")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	opener := strings.Join([]string{
		"Please adapt this novel.",
		"",
		"granularity=free",
		"rewrite_policy=" + domain.AdaptationRewritePolicyForGranularity(domain.AdaptationGranularityFree),
		"word_tolerance=disabled",
	}, "\n")
	stableDraft := strings.Join([]string{
		"## stable draft",
		"- preserve early setup",
		"- keep the family subplot",
		strings.Repeat("- accumulated beat\n", 80),
	}, "\n")
	badDraft := "## bad draft\n- 同上，已完整记录。\n- only final pacing note"
	checkpoint := webCoCreateCheckpoint{
		Version: webCoCreateCheckpointVersion,
		Kind:    webCoCreateKindAdapt,
		Session: startup.CoCreateSnapshot{
			History: []host.CoCreateMessage{
				{Role: "user", Content: opener},
				{Role: "assistant", Content: "<reply>stable</reply><draft>" + stableDraft + "</draft><ready>true</ready><suggestions></suggestions>"},
				{Role: "user", Content: "add final pacing note"},
				{Role: "assistant", Content: "<reply>bad</reply><draft>" + badDraft + "</draft><ready>true</ready><suggestions></suggestions>"},
			},
			DraftPrompt:     badDraft,
			DraftHistoryLen: 4,
			Ready:           true,
		},
		SourceFile: "source.txt",
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
	if err := os.MkdirAll(filepath.Dir(checkpointPath), 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(checkpointPath, data, 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.adaptCoCreateReply = webCoCreateReply("draft repaired", stableDraft+"\n- add final pacing note", true)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var restored struct {
		CoCreate *webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&restored); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if restored.CoCreate == nil || restored.CoCreate.CanStart {
		t.Fatalf("regressed restored draft should not be marked startable: %+v", restored.CoCreate)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("repair co-create calls = %d, want 1", fake.adaptCoCreateCalls)
	}
	if !strings.Contains(fake.startPreparedPrompt, "family subplot") ||
		!strings.Contains(fake.startPreparedPrompt, "final pacing note") ||
		strings.Contains(fake.startPreparedPrompt, "同上") {
		t.Fatalf("Character brief = %q, want repaired cumulative draft", fake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateCommitRepairsPreviousRoundPlaceholderDraft(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Previous Round Placeholder")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	opener := strings.Join([]string{
		"Please adapt this novel.",
		"",
		"granularity=free",
		"rewrite_policy=" + domain.AdaptationRewritePolicyForGranularity(domain.AdaptationGranularityFree),
		"word_tolerance=disabled",
	}, "\n")
	stableDraft := strings.Join([]string{
		"## 改编模式",
		"granularity=free",
		"rewrite_policy=full_rewrite",
		"word_tolerance=disabled",
		"",
		"## 用户目标",
		"- 保留家族压力副线",
		"- 增强女主主动调查线",
	}, "\n")
	badDraft := "## 用户目标\n同前轮（完整保留）"
	checkpoint := webCoCreateCheckpoint{
		Version: webCoCreateCheckpointVersion,
		Kind:    webCoCreateKindAdapt,
		Session: startup.CoCreateSnapshot{
			History: []host.CoCreateMessage{
				{Role: "user", Content: opener},
				{Role: "assistant", Content: "<reply>stable</reply><draft>" + stableDraft + "</draft><ready>true</ready><suggestions></suggestions>"},
				{Role: "user", Content: "把后半段节奏放慢，多留日常铺垫"},
				{Role: "assistant", Content: "<reply>bad</reply><draft>" + badDraft + "</draft><ready>true</ready><suggestions></suggestions>"},
			},
			DraftPrompt:     badDraft,
			DraftHistoryLen: 4,
			Ready:           true,
		},
		SourceFile: "source.txt",
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
	if err := os.MkdirAll(filepath.Dir(checkpointPath), 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(checkpointPath, data, 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.adaptCoCreateReply = webCoCreateReply("draft repaired", stableDraft+"\n- 把后半段节奏放慢，多留日常铺垫", true)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var restored struct {
		CoCreate *webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&restored); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if restored.CoCreate == nil || restored.CoCreate.CanStart {
		t.Fatalf("previous-round placeholder draft should not be startable: %+v", restored.CoCreate)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("repair co-create calls = %d, want 1", fake.adaptCoCreateCalls)
	}
	if !strings.Contains(fake.startPreparedPrompt, "家族压力副线") ||
		!strings.Contains(fake.startPreparedPrompt, "后半段节奏放慢") ||
		strings.Contains(fake.startPreparedPrompt, "同前轮") {
		t.Fatalf("Character brief = %q, want repaired self-contained draft", fake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateCheckpointIgnoredWhenPlanExists(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Done")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Brief:         "confirmed adaptation",
		Granularity:   domain.AdaptationGranularityFree,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Target One",
			SourceChapters: []int{1},
		}},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	checkpoint := webCoCreateCheckpoint{
		Version: webCoCreateCheckpointVersion,
		Kind:    webCoCreateKindAdapt,
		Session: startup.CoCreateSnapshot{
			History:     []host.CoCreateMessage{{Role: "user", Content: "granularity=free\nrewrite_policy=full_rewrite"}},
			DraftPrompt: "## stale draft",
			Ready:       true,
		},
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
	if err := os.MkdirAll(filepath.Dir(checkpointPath), 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(checkpointPath, data, 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var restored struct {
		CoCreate *webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&restored); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if restored.CoCreate != nil {
		t.Fatalf("stale checkpoint should be ignored when plan exists: %+v", restored.CoCreate)
	}
	if _, err := os.Stat(checkpointPath); err != nil {
		t.Fatalf("blocked checkpoint should remain available for a later rollback: %v", err)
	}
}

func TestProjectNormalCoCreateLegacyLogIgnoredWhenPlanningReviewExists(t *testing.T) {
	for _, status := range []string{
		domain.PlanningReviewStatusPending,
		domain.PlanningReviewStatusCollecting,
	} {
		t.Run(status, func(t *testing.T) {
			outputDir := t.TempDir()
			st := storepkg.NewStore(outputDir)
			if err := st.Init(); err != nil {
				t.Fatalf("Init store: %v", err)
			}
			if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
				Status: status,
				Kind:   domain.PlanningReviewKindBlueprint,
				Brief:  "accepted planning brief",
			}); err != nil {
				t.Fatalf("seed planning review: %v", err)
			}

			entry := webCoCreateLogEntry{
				InputHistory: []host.CoCreateMessage{{Role: "user", Content: "write a normal novel"}},
				RawResponse:  "<reply>ready</reply><draft>## stale draft</draft><ready>true</ready>",
				ParsedReply:  "ready",
				ParsedDraft:  "## stale draft",
				ParsedReady:  true,
			}
			data, err := json.Marshal(entry)
			if err != nil {
				t.Fatalf("marshal co-create log: %v", err)
			}
			logPath := filepath.Join(outputDir, filepath.FromSlash(webCoCreateLogRelPath))
			if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
				t.Fatalf("mkdir co-create log dir: %v", err)
			}
			if err := os.WriteFile(logPath, append(data, '\n'), 0o644); err != nil {
				t.Fatalf("write co-create log: %v", err)
			}

			session, err := NewProjectSession(ProjectManifest{
				ID:        "normal-planning-" + status,
				OutputDir: outputDir,
			}, newFakeProjectHost())
			if err != nil {
				t.Fatalf("NewProjectSession: %v", err)
			}
			defer session.Close()

			if restored := session.CoCreateState(); restored != nil {
				t.Fatalf("stale co-create log restored over planning review: %+v", restored)
			}
			checkpointPath := filepath.Join(outputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
			if _, err := os.Stat(checkpointPath); !os.IsNotExist(err) {
				t.Fatalf("stale co-create checkpoint should remain absent, stat err=%v", err)
			}
		})
	}
}

func TestProjectCoCreateDoesNotRestoreBusinessStateFromRedactedLog(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Legacy CoCreate Restore")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	opener := strings.Join([]string{
		"Please adapt this novel.",
		"",
		"granularity=free",
		"rewrite_policy=" + domain.AdaptationRewritePolicyForGranularity(domain.AdaptationGranularityFree),
		"word_tolerance=0.15",
	}, "\n")
	assistantRaw := strings.Join([]string{
		"<reply>ready to start</reply>",
		"<draft>## restored draft\n- keep current direction</draft>",
		"<ready>true</ready>",
		"<suggestions><suggestion>write chapter one</suggestion></suggestions>",
	}, "")
	entry := webCoCreateLogEntry{
		InputHistory: []host.CoCreateMessage{
			{Role: "user", Content: opener},
			{Role: "user", Content: "core adaptation idea"},
			{Role: "assistant", Content: assistantRaw},
			{Role: "user", Content: "move the first period to high school"},
		},
		Error: "context deadline exceeded",
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal legacy log: %v", err)
	}
	logPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateLogRelPath))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy log dir: %v", err)
	}
	if err := os.WriteFile(logPath, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write legacy log: %v", err)
	}

	installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var restored struct {
		CoCreate *webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&restored); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if restored.CoCreate != nil {
		t.Fatalf("redacted diagnostic log restored business state: %+v", restored.CoCreate)
	}
	if _, err := os.Stat(filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))); !os.IsNotExist(err) {
		t.Fatalf("diagnostic log unexpectedly produced a durable checkpoint: %v", err)
	}
}

func TestProjectRollbackToDraftDoesNotRestoreFromDiagnosticLog(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Rollback Restore CoCreate")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	assistantRaw := "<reply>ready to start</reply><draft>## restored draft\n- keep current direction</draft><ready>true</ready>"
	entry := webCoCreateLogEntry{
		InputHistory: []host.CoCreateMessage{
			{Role: "user", Content: "granularity=chapter\nrewrite_policy=preserve_details\nword_tolerance=0.15"},
			{Role: "user", Content: "adapt the whole source"},
		},
		RawResponse: assistantRaw,
		ParsedReply: "ready to start",
		ParsedDraft: "## restored draft\n- keep current direction",
		ParsedReady: true,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal co-create log: %v", err)
	}
	logPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateLogRelPath))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("mkdir co-create log dir: %v", err)
	}
	normalEntry, err := json.Marshal(webCoCreateLogEntry{
		InputHistory: []host.CoCreateMessage{{Role: "user", Content: "write a separate normal novel"}},
		ParsedDraft:  "wrong normal draft",
		ParsedReady:  true,
	})
	if err != nil {
		t.Fatalf("marshal normal co-create log: %v", err)
	}
	logData := append(append(append([]byte(nil), data...), '\n'), normalEntry...)
	logData = append(logData, '\n')
	if err := os.WriteFile(logPath, logData, 0o644); err != nil {
		t.Fatalf("write co-create log: %v", err)
	}

	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Brief:       "adapt the whole source",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:      1,
			Title:        "Target",
			OutlineEntry: domain.OutlineEntry{Chapter: 1, Title: "Target", CoreEvent: "event"},
		}},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	if err := os.Remove(filepath.Join(manifest.OutputDir, "meta", "adaptation", "plan.json")); err != nil {
		t.Fatalf("remove confirmed plan: %v", err)
	}
	fake.rollbackPreview = domain.RollbackPreviewWithHash(domain.RollbackPreview{
		CanRollback: true,
		Mode:        "adaptation",
		TargetStage: domain.RollbackStageDraft,
		TargetLabel: "共创草稿完成",
	})
	fake.rollbackResult = domain.RollbackResult{Preview: fake.rollbackPreview}

	previewReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/rollback/preview", nil)
	previewRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("rollback preview status = %d body=%s", previewRec.Code, previewRec.Body.String())
	}
	rollbackBody := `{"confirm":true,"preview_hash":"` + fake.rollbackPreview.PreviewHash + `"}`
	rollbackReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/rollback", strings.NewReader(rollbackBody))
	rollbackRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rollbackRec, rollbackReq)
	if rollbackRec.Code != http.StatusOK {
		t.Fatalf("rollback status = %d body=%s", rollbackRec.Code, rollbackRec.Body.String())
	}
	var rollbackResponse struct {
		CoCreate *webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rollbackRec.Body).Decode(&rollbackResponse); err != nil {
		t.Fatalf("decode rollback response: %v", err)
	}
	if rollbackResponse.CoCreate != nil {
		t.Fatalf("rollback restored business state from diagnostic log: %+v", rollbackResponse.CoCreate)
	}

	snapshotReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	snapshotRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(snapshotRec, snapshotReq)
	var snapshot struct {
		CoCreate *webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(snapshotRec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.CoCreate != nil {
		t.Fatalf("snapshot restored business state from diagnostic log: %+v", snapshot.CoCreate)
	}
}

func TestProjectCoCreatePersistsTargetTotalWords(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Budget")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("可以开始。", "## 主题\n- 5000 字短篇", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写一部短篇","target_total_words":5000}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.setWordBudgetCalls != 1 || fake.wordBudget == nil || fake.wordBudget.TargetTotalWords != 5000 {
		t.Fatalf("SetWordBudget calls=%d budget=%+v", fake.setWordBudgetCalls, fake.wordBudget)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil || !strings.Contains(review.StartPrompt, "target_total_words=5000") {
		t.Fatalf("saved start prompt missing word budget contract: review=%+v err=%v", review, err)
	}
}

func TestProjectCoCreateWebCopyUsesStartButtonInsteadOfCtrlS(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Copy")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateProgress = []coCreateProgressStep{{kind: "reply", text: "可以按 Ctrl+S 开始创作。"}}
	fake.cocreateReply = webCoCreateReply("可以按 Ctrl+S 开始创作。", "## 主题\n- 月城追凶", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写一个月城悬疑"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode begin response: %v", err)
	}
	got := response.CoCreate.StreamReply + "\n"
	for _, message := range response.CoCreate.Messages {
		got += message.Content + "\n"
	}
	if strings.Contains(got, "Ctrl+S") {
		t.Fatalf("web co-create copy should not mention Ctrl+S: %q", got)
	}
	if !strings.Contains(got, "点击「启动」") {
		t.Fatalf("web co-create copy should mention start button: %q", got)
	}
}

func TestProjectCoCreateSuggestionSendAndReviseTruncatesHistory(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Revise")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReplies = []host.CoCreateReply{
		webCoCreateReply("continue", "## Direction\n- initial", false, "add heroine line"),
		webCoCreateReply("updated", "## Direction\n- initial\n- add heroine line", false),
		webCoCreateReply("revised", "## Direction\n- initial\n- add heroine line with slower pacing", false),
	}
	fake.cocreateReply = webCoCreateReply("继续确认。", "## 方向\n- 初始", false, "加强女主线")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写月城悬疑"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"加强女主线","source":"suggestion"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("send status = %d body=%s", rec.Code, rec.Body.String())
	}
	var sent struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&sent); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	var suggestionMessage webCoCreateMessage
	for _, message := range sent.CoCreate.Messages {
		if message.Role == "user" && message.Content == "加强女主线" {
			suggestionMessage = message
			break
		}
	}
	if suggestionMessage.ID == "" || !suggestionMessage.Editable || suggestionMessage.Source != "suggestion" {
		t.Fatalf("suggestion message metadata = %+v", suggestionMessage)
	}
	if fake.cocreateCalls != 2 {
		t.Fatalf("co-create calls after send = %d, want 2", fake.cocreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/revise", bytes.NewBufferString(`{"message_id":`+strconvQuote(suggestionMessage.ID)+`,"text":"加强女主线，但保留慢热节奏"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revise status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.cocreateCalls != 3 {
		t.Fatalf("co-create calls after revise = %d, want 3", fake.cocreateCalls)
	}
	if len(fake.lastCoCreateHistory) != 3 {
		t.Fatalf("history after revise = %+v, want 3 entries", fake.lastCoCreateHistory)
	}
	if got := fake.lastCoCreateHistory[2].Content; got != "加强女主线，但保留慢热节奏" {
		t.Fatalf("revised history content = %q", got)
	}
	var revised struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&revised); err != nil {
		t.Fatalf("decode revise response: %v", err)
	}
	for _, message := range revised.CoCreate.Messages {
		if message.Content == "加强女主线" {
			t.Fatalf("old suggestion survived after revise: %+v", revised.CoCreate.Messages)
		}
	}
}

func TestProjectCoCreateRejectsInvalidSourceAndNonEditableRevise(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("CoCreate Rejects")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.cocreateReply = webCoCreateReply("先确认。", "## 方向", false)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"normal","initial":"写月城悬疑"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var begun struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&begun); err != nil {
		t.Fatalf("decode begin response: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"随便补充","source":"bad"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid source status = %d body=%s", rec.Code, rec.Body.String())
	}

	assistantID := ""
	for _, message := range begun.CoCreate.Messages {
		if message.Role == "assistant" {
			assistantID = message.ID
			break
		}
	}
	if assistantID == "" {
		t.Fatalf("assistant message id not found: %+v", begun.CoCreate.Messages)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/revise", bytes.NewBufferString(`{"message_id":`+strconvQuote(assistantID)+`,"text":"改掉 AI 消息"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-editable revise status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectStageCoCreatePausesAndResumesWithDraft(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Stage CoCreate")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.stageCoCreateReply = webCoCreateReply("后续方向已清楚。", "## 后续走向\n- 回收旧伏笔", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"stage"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stage begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.pauseCoCreateCalls != 1 {
		t.Fatalf("PauseForCoCreate calls = %d, want 1", fake.pauseCoCreateCalls)
	}
	if fake.stageCoCreateCalls != 1 {
		t.Fatalf("StageCoCreateStream calls = %d, want 1", fake.stageCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stage commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.resumeCoCreateCalls != 1 || fake.resumeCoCreateDraft != "## 后续走向\n- 回收旧伏笔" {
		t.Fatalf("ResumeFromCoCreate calls=%d draft=%q", fake.resumeCoCreateCalls, fake.resumeCoCreateDraft)
	}
}

func TestProjectStageCoCreateFailureKeepsStateCancelable(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Stage CoCreate Failure")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.stageCoCreateErr = errors.New("stream failed")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"stage"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("stage begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var failure struct {
		Error    string           `json:"error"`
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&failure); err != nil {
		t.Fatalf("decode failure response: %v", err)
	}
	if !strings.Contains(failure.Error, "stream failed") {
		t.Fatalf("error = %q, want stream failed", failure.Error)
	}
	if !failure.CoCreate.Active || failure.CoCreate.Kind != webCoCreateKindStage {
		t.Fatalf("failure co-create state = %+v, want active stage", failure.CoCreate)
	}
	if fake.pauseCoCreateCalls != 1 {
		t.Fatalf("PauseForCoCreate calls = %d, want 1", fake.pauseCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/cancel", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.cancelCoCreateCalls != 1 {
		t.Fatalf("CancelCoCreate calls = %d, want 1", fake.cancelCoCreateCalls)
	}
}

func TestProjectAdaptCoCreateLocksSelectedModeOnCommit(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptCoCreateReply = webCoCreateReply(
		"目标明确，可以开始。",
		"## 改编模式\n\ngranularity=chapter\nrewrite_policy=preserve_details\n\n## 用户目标\n- 加强女主线",
		true,
	)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"arc","initial":"Keep the ending, but make the heroine survive."}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("AdaptCoCreateStream calls = %d, want 1", fake.adaptCoCreateCalls)
	}
	if len(fake.lastCoCreateHistory) == 0 || !strings.Contains(fake.lastCoCreateHistory[0].Content, "granularity=arc") {
		t.Fatalf("adapt opener did not lock arc mode: %+v", fake.lastCoCreateHistory)
	}
	if len(fake.lastCoCreateHistory) < 2 || fake.lastCoCreateHistory[1].Content != "Keep the ending, but make the heroine survive." {
		t.Fatalf("adapt initial brief was not sent to model history: %+v", fake.lastCoCreateHistory)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("adapt commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.prepareExternalRulesCalls != 0 || fake.adaptProposalCalls != 0 {
		t.Fatalf("proposal ran before Character confirmation: rules=%d proposal=%d",
			fake.prepareExternalRulesCalls, fake.adaptProposalCalls)
	}
	if fake.adaptStartCalls != 1 {
		t.Fatalf("Character workflow start calls = %d, want 1", fake.adaptStartCalls)
	}
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode commit response: %v", err)
	}
	if response.CoCreate.Active {
		t.Fatalf("adapt commit should leave co-create after start, got %+v", response.CoCreate)
	}
	if response.CoCreate.AdaptMode != domain.AdaptationGranularityArc ||
		response.CoCreate.RewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("locked adaptation mode = %+v", response.CoCreate)
	}
	if response.CoCreate.Proposal != nil {
		t.Fatalf("adapt proposal must wait for Character confirmation, got %+v", response.CoCreate.Proposal)
	}
}

func TestProjectAdaptCoCreateBeginWaitsForBriefingDecision(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Briefing")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptBriefing = testPendingAdaptBriefing()
	fake.adaptCoCreateReply = webCoCreateReply("ready", "## Adapt brief\n- resolved", true)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"strict single heroine, remove side romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var beginResponse struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&beginResponse); err != nil {
		t.Fatalf("decode begin response: %v", err)
	}
	if fake.adaptBriefingCalls != 1 {
		t.Fatalf("EnsureAdaptationCoCreateBriefing calls = %d, want 1", fake.adaptBriefingCalls)
	}
	if fake.adaptCoCreateCalls != 0 {
		t.Fatalf("AdaptCoCreateStream calls before decision = %d, want 0", fake.adaptCoCreateCalls)
	}
	if beginResponse.CoCreate.CanStart || len(beginResponse.CoCreate.PendingDecisions) != 1 {
		t.Fatalf("begin co-create state = %+v, want one pending decision and can_start=false", beginResponse.CoCreate)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/decision", bytes.NewBufferString(`{"decision_id":"q1","option_id":"a"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("decision status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.resolveAdaptDecisionCalls != 1 {
		t.Fatalf("ResolveAdaptationCoCreateDecision calls = %d, want 1", fake.resolveAdaptDecisionCalls)
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("AdaptCoCreateStream calls after decision = %d, want 1", fake.adaptCoCreateCalls)
	}
	var decisionResponse struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&decisionResponse); err != nil {
		t.Fatalf("decode decision response: %v", err)
	}
	if len(decisionResponse.CoCreate.PendingDecisions) != 0 || !decisionResponse.CoCreate.CanStart {
		t.Fatalf("decision co-create state = %+v, want no pending decisions and can_start=true", decisionResponse.CoCreate)
	}
}

func TestProjectAdaptCoCreateBeginDoesNotExposeRecoverableCheckpointAsFailure(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Briefing In Progress")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptBriefing = testPendingAdaptBriefing()
	fake.adaptBriefingStarted = make(chan struct{})
	fake.releaseAdaptBriefing = make(chan struct{})

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"strict single heroine, remove side romance"}`))
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		done <- rec
	}()

	<-fake.adaptBriefingStarted
	session, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatalf("Open session: %v", err)
	}
	state := session.CoCreateState()
	if state == nil || !state.Active || state.Failed {
		t.Fatalf("in-progress co-create state = %+v, want active without failed", state)
	}
	if state.Briefing != nil {
		t.Fatalf("in-progress co-create briefing = %+v, want nil until briefing completes", state.Briefing)
	}

	var checkpoint webCoCreateCheckpoint
	checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
	data, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	if !checkpoint.Failed {
		t.Fatalf("checkpoint should stay crash-resumable while briefing is in progress: %+v", checkpoint)
	}

	close(fake.releaseAdaptBriefing)
	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectAdaptCoCreateSendUsesAIWithoutRefreshingBriefing(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Briefing Refresh")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptBriefing = testPendingAdaptBriefing()
	fake.adaptCoCreateReplies = []host.CoCreateReply{
		webCoCreateReply("ready", "## Adapt brief\n- remove side romance", true),
		webCoCreateReply("updated", "## Adapt brief\n- remove side romance\n- raise Moonlight's emotional weight", true),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"strict single heroine, remove side romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptBriefingCalls != 1 {
		t.Fatalf("EnsureAdaptationCoCreateBriefing calls after begin = %d, want 1", fake.adaptBriefingCalls)
	}
	if fake.adaptCoCreateCalls != 0 {
		t.Fatalf("AdaptCoCreateStream calls before decision = %d, want 0", fake.adaptCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/decision", bytes.NewBufferString(`{"decision_id":"q1","option_id":"a"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("decision status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptBriefingCalls != 1 {
		t.Fatalf("decision should not rebuild briefing calls = %d, want 1", fake.adaptBriefingCalls)
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("AdaptCoCreateStream calls after decision = %d, want 1", fake.adaptCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"also raise Moonlight's emotional weight","source":"custom"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt send status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptBriefingCalls != 1 {
		t.Fatalf("EnsureAdaptationCoCreateBriefing calls after supplement = %d, want 1", fake.adaptBriefingCalls)
	}
	if fake.adaptCoCreateCalls != 2 {
		t.Fatalf("AdaptCoCreateStream calls after supplement = %d, want 2", fake.adaptCoCreateCalls)
	}
	var sendResponse struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&sendResponse); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if !sendResponse.CoCreate.CanStart || !strings.Contains(sendResponse.CoCreate.DraftPrompt, "Moonlight's emotional weight") {
		t.Fatalf("send co-create state = %+v, want AI-updated startable draft", sendResponse.CoCreate)
	}
	if !coCreateHistoryContains(fake.lastCoCreateHistory, "remove side romance") ||
		!coCreateHistoryContains(fake.lastCoCreateHistory, "also raise Moonlight's emotional weight") {
		t.Fatalf("AI history should include prior draft and latest supplement, got %+v", fake.lastCoCreateHistory)
	}
}

func TestProjectAdaptCoCreateBeginReturnsAllPendingBriefingDecisions(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Briefing Queue")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptBriefing = testPendingAdaptBriefingWithDecisionCount(6)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"strict single heroine, remove side romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	var beginResponse struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&beginResponse); err != nil {
		t.Fatalf("decode begin response: %v", err)
	}
	if got := len(beginResponse.CoCreate.PendingDecisions); got != 6 {
		t.Fatalf("pending decisions = %d, want all 6", got)
	}
	if beginResponse.CoCreate.Briefing == nil {
		t.Fatal("briefing state is nil")
	}
	if beginResponse.CoCreate.Briefing.PendingDecisionCount != 6 || beginResponse.CoCreate.Briefing.TotalDecisionCount != 6 {
		t.Fatalf("briefing counts = %+v, want pending=6 total=6", beginResponse.CoCreate.Briefing)
	}
	if beginResponse.CoCreate.CanStart {
		t.Fatalf("can_start = true with pending decisions: %+v", beginResponse.CoCreate)
	}
}

func TestProjectAdaptCoCreateDecisionDraftRunsInBatches(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Decision Batches")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptBriefing = testPendingAdaptBriefingWithDecisionCount(18)
	for index := 1; index <= 5; index++ {
		fake.adaptCoCreateReplies = append(fake.adaptCoCreateReplies, webCoCreateReply(
			fmt.Sprintf("batch %d integrated", index),
			fmt.Sprintf("## Adapt brief\n- decision batch %d integrated", index),
			index == 5,
		))
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"strict single heroine, remove side romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptBriefingCalls != 1 {
		t.Fatalf("EnsureAdaptationCoCreateBriefing calls after begin = %d, want 1", fake.adaptBriefingCalls)
	}

	decisions := make([]webCoCreateDecisionItem, 0, 18)
	for index := 1; index <= 18; index++ {
		decisions = append(decisions, webCoCreateDecisionItem{DecisionID: fmt.Sprintf("q%d", index), OptionID: "a"})
	}
	body, err := json.Marshal(webCoCreateDecisionRequest{Decisions: decisions})
	if err != nil {
		t.Fatalf("marshal decisions: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/decision", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("decision status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptBriefingCalls != 1 {
		t.Fatalf("decisions should not rebuild briefing calls = %d, want 1", fake.adaptBriefingCalls)
	}
	if fake.adaptCoCreateCalls != 5 {
		t.Fatalf("AdaptCoCreateStream decision draft calls = %d, want 5", fake.adaptCoCreateCalls)
	}
	if len(fake.adaptCoCreateHistories) != 5 {
		t.Fatalf("recorded adapt histories = %d, want 5", len(fake.adaptCoCreateHistories))
	}
	for index, history := range fake.adaptCoCreateHistories {
		latestUser := latestUserMessageContent(history)
		wantMarker := fmt.Sprintf("%s %d/5", adaptDecisionDraftMarker, index+1)
		if !strings.Contains(latestUser, wantMarker) {
			t.Fatalf("history %d latest user missing marker %q: %q", index+1, wantMarker, latestUser)
		}
		if got := strings.Count(latestUser, "=>"); got > adaptDecisionDraftBatchSize {
			t.Fatalf("history %d decision count = %d, want <= %d: %q", index+1, got, adaptDecisionDraftBatchSize, latestUser)
		}
	}

	var decisionResponse struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&decisionResponse); err != nil {
		t.Fatalf("decode decision response: %v", err)
	}
	if !decisionResponse.CoCreate.CanStart || !strings.Contains(decisionResponse.CoCreate.DraftPrompt, "decision batch 5 integrated") {
		t.Fatalf("decision co-create state = %+v, want final batched draft", decisionResponse.CoCreate)
	}
	for _, message := range decisionResponse.CoCreate.Messages {
		if strings.Contains(message.Content, adaptDecisionDraftMarker) {
			t.Fatalf("internal decision batch prompt leaked to UI messages: %+v", decisionResponse.CoCreate.Messages)
		}
	}
	session := server.sessions.Project(manifest.ID)
	if session == nil {
		t.Fatal("project session is nil")
	}
	var batchEventDetails []string
	for _, ev := range session.HistoryAfter(0) {
		if ev.Type != webEventTypeHostEvent || ev.Event == nil {
			continue
		}
		if ev.Event.Kind == webCoCreateKindAdapt && strings.Contains(ev.Event.Summary, "分批生成首次 draft") {
			batchEventDetails = append(batchEventDetails, ev.Event.Detail)
		}
	}
	if len(batchEventDetails) != 5 {
		t.Fatalf("decision draft batch event count = %d, want 5: %+v", len(batchEventDetails), session.HistoryAfter(0))
	}
	for index, detail := range batchEventDetails {
		want := fmt.Sprintf("confirmed_decision_batch=%d/5", index+1)
		if detail != want {
			t.Fatalf("batch event %d detail = %q, want %q", index+1, detail, want)
		}
	}
}

func TestProjectAdaptCoCreateResumePreservesFailedDecisionProgress(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Resume")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptBriefing = testPendingAdaptBriefing()
	fake.adaptCoCreateErr = errors.New("<html><head><title>502 Bad Gateway</title></head><body>nginx</body></html>")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"strict single heroine, remove side romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/decision", bytes.NewBufferString(`{"decision_id":"q1","option_id":"a"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("decision status = %d body=%s", rec.Code, rec.Body.String())
	}
	var failedResponse struct {
		Error    string           `json:"error"`
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&failedResponse); err != nil {
		t.Fatalf("decode failed decision response: %v", err)
	}
	if !failedResponse.CoCreate.Active || !failedResponse.CoCreate.Failed {
		t.Fatalf("failed co-create state = %+v, want active failed session", failedResponse.CoCreate)
	}
	if failedResponse.CoCreate.Briefing == nil || failedResponse.CoCreate.Briefing.PendingDecisionCount != 0 || failedResponse.CoCreate.Briefing.ResolvedDecisionCount != 1 {
		t.Fatalf("failed briefing state = %+v, want resolved decision preserved", failedResponse.CoCreate.Briefing)
	}
	if strings.Contains(strings.ToLower(failedResponse.Error), "<html") || strings.Contains(failedResponse.Error, "nginx") {
		t.Fatalf("error leaked raw html: %s", failedResponse.Error)
	}
	failedHistoryLen := len(fake.lastCoCreateHistory)
	if failedHistoryLen == 0 {
		t.Fatal("failed run did not call adapt co-create")
	}

	fake.adaptCoCreateErr = nil
	fake.adaptCoCreateReply = webCoCreateReply("ready after resume", "## Adapt brief\n- resolved and saved", true)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/resume", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resumeResponse struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resumeResponse); err != nil {
		t.Fatalf("decode resume response: %v", err)
	}
	if resumeResponse.CoCreate.Failed || !resumeResponse.CoCreate.CanStart || !strings.Contains(resumeResponse.CoCreate.DraftPrompt, "resolved and saved") {
		t.Fatalf("resume co-create state = %+v, want recovered startable draft", resumeResponse.CoCreate)
	}
	if got := len(fake.lastCoCreateHistory); got != failedHistoryLen {
		t.Fatalf("resume history len = %d, want preserved len %d without new user message", got, failedHistoryLen)
	}
}

func TestProjectAdaptCoCreateResumeRestoresFailedBriefingGeneration(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Briefing Resume")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptBriefingErr = errors.New("<html><head><title>502 Bad Gateway</title></head><body>nginx</body></html>")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"strict single heroine, remove side romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("adapt begin status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	var failedResponse struct {
		Error    string           `json:"error"`
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&failedResponse); err != nil {
		t.Fatalf("decode failed begin response: %v", err)
	}
	if !failedResponse.CoCreate.Active || !failedResponse.CoCreate.Failed || failedResponse.CoCreate.Briefing != nil {
		t.Fatalf("failed briefing state = %+v, want active failed session without briefing", failedResponse.CoCreate)
	}
	if !webCoCreateMessagesContain(failedResponse.CoCreate.Messages, "strict single heroine") {
		t.Fatalf("failed response should retain initial prompt messages: %+v", failedResponse.CoCreate.Messages)
	}
	if strings.Contains(strings.ToLower(failedResponse.Error), "<html") || strings.Contains(failedResponse.Error, "nginx") {
		t.Fatalf("error leaked raw html: %s", failedResponse.Error)
	}
	if _, err := os.Stat(filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))); err != nil {
		t.Fatalf("failed briefing should persist checkpoint: %v", err)
	}
	session, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatalf("Open session: %v", err)
	}
	failedEvent := latestHostEventByKind(session.HistoryAfter(0), "adapt_briefing")
	if failedEvent == nil || !failedEvent.Failed || failedEvent.Level != "error" {
		t.Fatalf("failed briefing event = %+v, want error lifecycle event", failedEvent)
	}
	if strings.Contains(strings.ToLower(failedEvent.Detail), "<html") || strings.Contains(failedEvent.Detail, "nginx") {
		t.Fatalf("failed briefing event leaked raw html: %s", failedEvent.Detail)
	}

	server.sessions.CloseProject(manifest.ID)
	restoredFake := installFakeSession(t, server, manifest)
	restoredFake.adaptBriefing = testReadyAdaptBriefing()
	restoredFake.adaptCoCreateReply = webCoCreateReply("ready after briefing resume", "## Adapt brief\n- resumed from saved prompt", true)

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/resume", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resumeResponse struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resumeResponse); err != nil {
		t.Fatalf("decode resume response: %v", err)
	}
	if restoredFake.adaptBriefingCalls != 1 {
		t.Fatalf("EnsureAdaptationCoCreateBriefing calls = %d, want 1", restoredFake.adaptBriefingCalls)
	}
	if restoredFake.lastAdaptBriefingIntent.RawRequest != "strict single heroine, remove side romance" {
		t.Fatalf("restored briefing intent raw request = %q", restoredFake.lastAdaptBriefingIntent.RawRequest)
	}
	wantSourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
	if restoredFake.lastAdaptBriefingSource != wantSourcePath {
		t.Fatalf("restored briefing source = %q, want %q", restoredFake.lastAdaptBriefingSource, wantSourcePath)
	}
	if restoredFake.adaptCoCreateCalls != 1 {
		t.Fatalf("AdaptCoCreateStream calls after resume = %d, want 1", restoredFake.adaptCoCreateCalls)
	}
	if !coCreateHistoryContains(restoredFake.lastCoCreateHistory, "strict single heroine") {
		t.Fatalf("restored co-create history lost initial prompt: %+v", restoredFake.lastCoCreateHistory)
	}
	if resumeResponse.CoCreate.Failed || !resumeResponse.CoCreate.CanStart || !strings.Contains(resumeResponse.CoCreate.DraftPrompt, "resumed from saved prompt") {
		t.Fatalf("resume co-create state = %+v, want recovered startable draft", resumeResponse.CoCreate)
	}
}

func TestProjectAdaptCoCreateCommitUsesAIUpdatedDraftBeforeCharacterWorkflow(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Final Consolidation")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptCoCreateReplies = []host.CoCreateReply{
		webCoCreateReply("initial draft ready", "## Adaptation Draft\n- preserve early relationship arc", true),
		webCoCreateReply("updated draft ready", "## Adaptation Draft\n- preserve early relationship arc\n- expand the late chapter plan", true),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"preserve early relationship arc"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"expand the late chapter plan","source":"custom"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt send status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 2 {
		t.Fatalf("AdaptCoCreateStream calls before commit = %d, want 2", fake.adaptCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 2 {
		t.Fatalf("co-create calls after commit = %d, want 2", fake.adaptCoCreateCalls)
	}
	if !strings.Contains(fake.startPreparedPrompt, "early relationship arc") ||
		!strings.Contains(fake.startPreparedPrompt, "late chapter plan") {
		t.Fatalf("Character brief should use final consolidated draft: %q", fake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateCommitRetryKeepsFinalDraftAfterCharacterStartFailure(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Final Consolidation Retry")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptStartErr = errors.New("Character workflow timeout")
	fake.adaptCoCreateReplies = []host.CoCreateReply{
		webCoCreateReply("initial draft ready", "## Adaptation Draft\n- preserve early relationship arc", true),
		webCoCreateReply("updated draft ready", "## Adaptation Draft\n- preserve early relationship arc\n- expand the late chapter plan", true),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"preserve early relationship arc"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"expand the late chapter plan","source":"custom"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt send status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("first adapt commit status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 2 {
		t.Fatalf("co-create calls after failed Character start = %d, want 2", fake.adaptCoCreateCalls)
	}
	if fake.adaptStartCalls != 1 || fake.adaptProposalCalls != 0 {
		t.Fatalf("Character/proposal calls after failed start = %d/%d, want 1/0", fake.adaptStartCalls, fake.adaptProposalCalls)
	}
	var checkpoint webCoCreateCheckpoint
	checkpointPath := filepath.Join(manifest.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath))
	data, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	if !checkpoint.DraftConsolidated {
		t.Fatalf("checkpoint should remember final draft consolidation: %+v", checkpoint)
	}

	server.sessions.CloseProject(manifest.ID)
	restoredFake := installFakeSession(t, server, manifest)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry adapt commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if restoredFake.adaptCoCreateCalls != 0 {
		t.Fatalf("retry should not reconsolidate draft, co-create calls=%d", restoredFake.adaptCoCreateCalls)
	}
	if restoredFake.adaptStartCalls != 1 || restoredFake.adaptProposalCalls != 0 {
		t.Fatalf("retry Character/proposal calls = %d/%d, want 1/0", restoredFake.adaptStartCalls, restoredFake.adaptProposalCalls)
	}
	if !strings.Contains(restoredFake.startPreparedPrompt, "early relationship arc") ||
		!strings.Contains(restoredFake.startPreparedPrompt, "late chapter plan") {
		t.Fatalf("retry Character brief should use saved final draft: %q", restoredFake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateUsesAISupplementBeforeCharacterWorkflow(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Repair")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	fake.adaptCoCreateReplies = []host.CoCreateReply{
		webCoCreateReply("initial draft ready", "## Adaptation Draft\n- early goal", true),
		webCoCreateReply("expanded draft ready", "## Adaptation Draft\n- early goal\n- expand the plan to 24 target chapters and add the new subplot", true),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"start from this adaptation direction"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 1 {
		t.Fatalf("AdaptCoCreateStream calls after begin = %d, want 1", fake.adaptCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"expand the plan to 24 target chapters and add the new subplot","source":"custom"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt send status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 2 {
		t.Fatalf("AdaptCoCreateStream calls after supplement = %d, want 2", fake.adaptCoCreateCalls)
	}
	var sendResponse struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&sendResponse); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if !sendResponse.CoCreate.CanStart || !strings.Contains(sendResponse.CoCreate.DraftPrompt, "24 target chapters") {
		t.Fatalf("repaired draft state = %+v", sendResponse.CoCreate)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptStartCalls != 1 || fake.adaptProposalCalls != 0 {
		t.Fatalf("Character/proposal calls = %d/%d, want 1/0", fake.adaptStartCalls, fake.adaptProposalCalls)
	}
	if !strings.Contains(fake.startPreparedPrompt, "24 target chapters") || !strings.Contains(fake.startPreparedPrompt, "new subplot") {
		t.Fatalf("Character brief did not use repaired draft: %q", fake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateAIUpdatePreservesLongDraftBeforeCharacterWorkflow(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Regressed Draft")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	previousDraft := strings.Join([]string{
		"## Adaptation Draft",
		"- preserve the early relationship arc",
		"- include the family pressure subplot",
		"- keep the investigation thread",
		strings.Repeat("- earlier accumulated beat\n", 80),
	}, "\n")
	fake.adaptCoCreateReplies = []host.CoCreateReply{
		webCoCreateReply("initial draft ready", previousDraft, true),
		webCoCreateReply("overwritten draft", "## Adaptation Draft\n- 同上，已完整记录。\n- only discuss the final pacing note", true),
		webCoCreateReply("draft repaired", previousDraft+"\n- add the final pacing note with daily scenes between reveals", true),
		webCoCreateReply("final draft consolidated", previousDraft+"\n- add the final pacing note with daily scenes between reveals", true),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"start from this adaptation direction"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"add daily scenes between reveals so the pacing breathes","source":"custom"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt send status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 3 {
		t.Fatalf("AdaptCoCreateStream calls after supplement repair = %d, want 3", fake.adaptCoCreateCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/commit", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt commit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(fake.startPreparedPrompt, "family pressure subplot") ||
		!strings.Contains(fake.startPreparedPrompt, "daily scenes between reveals") ||
		strings.Contains(fake.startPreparedPrompt, "同上") {
		t.Fatalf("Character brief did not preserve repaired cumulative draft: %q", fake.startPreparedPrompt)
	}
}

func TestProjectAdaptCoCreateAIUpdateKeepsStableDraftWhenRepairFails(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Rollback Draft")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	previousDraft := strings.Join([]string{
		"## 改编模式",
		"granularity=free",
		"rewrite_policy=full_rewrite",
		"word_tolerance=disabled",
		"",
		"## 用户目标",
		"- 保留家族压力副线",
		"- 增强女主主动调查线",
	}, "\n")
	fake.adaptCoCreateReplies = []host.CoCreateReply{
		webCoCreateReply("initial draft ready", previousDraft, true),
		webCoCreateReply("bad draft", "## 用户目标\n同前轮（完整保留）", true),
		{Message: "repair failed to produce draft", Ready: true, Raw: "<reply>repair failed to produce draft</reply><ready>true</ready><suggestions></suggestions>"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"保留家族压力副线，并增强女主主动调查线"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"后半段节奏放慢","source":"custom"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("adapt send status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 3 {
		t.Fatalf("AdaptCoCreateStream calls after failed repair = %d, want 3", fake.adaptCoCreateCalls)
	}
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if !strings.Contains(response.CoCreate.DraftPrompt, previousDraft) {
		t.Fatalf("draft should keep previous stable draft, got %q", response.CoCreate.DraftPrompt)
	}
	if response.CoCreate.CanStart {
		t.Fatalf("failed AI update should not be startable: %+v", response.CoCreate)
	}
	if strings.Contains(response.CoCreate.DraftPrompt, "同前轮") {
		t.Fatalf("rejected placeholder leaked into draft: %q", response.CoCreate.DraftPrompt)
	}
}

func TestProjectAdaptCoCreateIncrementUsesStableDraftContext(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt CoCreate Compact Repair")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")
	previousDraft := "## Stable Draft\n- preserve early setup\n- keep relationship arc"
	fake.adaptCoCreateReplies = []host.CoCreateReply{
		webCoCreateReply("initial draft ready", previousDraft, true),
		webCoCreateReply("updated draft ready", previousDraft+"\n- add final daily-scene pacing", true),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"free","initial":"start from this adaptation direction"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt begin status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/send", bytes.NewBufferString(`{"text":"add final daily-scene pacing","source":"custom"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adapt send status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 2 {
		t.Fatalf("AdaptCoCreateStream calls = %d, want 2", fake.adaptCoCreateCalls)
	}
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if !strings.Contains(response.CoCreate.DraftPrompt, "preserve early setup") ||
		!strings.Contains(response.CoCreate.DraftPrompt, "add final daily-scene pacing") {
		t.Fatalf("merged draft should include stable draft and latest user turn: %q", response.CoCreate.DraftPrompt)
	}
	if !coCreateHistoryContains(fake.lastCoCreateHistory, "preserve early setup") ||
		!coCreateHistoryContains(fake.lastCoCreateHistory, "add final daily-scene pacing") {
		t.Fatalf("AI history should include stable draft and latest user turn: %+v", fake.lastCoCreateHistory)
	}
}

func TestCompactDraftRepairHistoryKeepsAllUserPlanningTurns(t *testing.T) {
	previousDraft := "## Stable Draft\n- preserve early setup"
	history := []host.CoCreateMessage{
		{Role: "user", Content: "granularity=free\nrewrite_policy=full_rewrite"},
		{Role: "user", Content: "early planning turn"},
		{Role: "assistant", Content: "<reply>ok</reply><draft>" + previousDraft + "</draft><ready>true</ready><suggestions></suggestions>"},
		{Role: "user", Content: "middle planning turn"},
		{Role: "assistant", Content: "brief acknowledgement without draft"},
		{Role: "user", Content: "late planning turn"},
		{Role: "assistant", Content: "<reply>bad</reply><draft>## overwritten\n- 同上</draft><ready>true</ready><suggestions></suggestions>"},
	}

	repairHistory := compactDraftRepairHistory(history, previousDraft)
	joined := ""
	for _, message := range repairHistory {
		joined += "\n" + message.Content
	}
	for _, want := range []string{"early planning turn", "middle planning turn", "late planning turn", "preserve early setup"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("repair history missing %q: %q", want, joined)
		}
	}
	if strings.Contains(joined, "## overwritten") {
		t.Fatalf("repair history should omit older assistant draft bodies: %q", joined)
	}
}

func TestProjectAdaptCoCreateRejectsTargetTotalWords(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Budget Reject")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	seedAnalyzedAdaptationForCoCreateTest(t, manifest, "source.txt")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/cocreate/begin", bytes.NewBufferString(`{"kind":"adapt","source_file":"source.txt","mode":"arc","target_total_words":5000}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("adapt begin status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.adaptCoCreateCalls != 0 {
		t.Fatalf("adapt co-create should not start, calls=%d", fake.adaptCoCreateCalls)
	}
}

func seedAnalyzedAdaptationForCoCreateTest(t *testing.T, manifest ProjectManifest, filename string) {
	t.Helper()
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init store: %v", err)
	}
	sourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", filename)
	source, err := st.Adaptation.SaveSourceChapter(1, "One", "source body")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	sourceManifest := domain.AdaptationSourceManifest{
		SourcePath:   sourcePath,
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}
	if err := st.Adaptation.SaveSourceManifest(sourceManifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	report := domain.AdaptationSourceReport{
		Chapter:      1,
		Title:        "One",
		SourceSHA256: source.SHA256,
		Summary:      "source summary",
		KeyEvents:    []string{"source event"},
	}
	if err := st.Adaptation.SaveSourceReport(report); err != nil {
		t.Fatalf("SaveSourceReport: %v", err)
	}
	if err := st.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{report}); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{Premise: "source", Characters: []domain.Character{}, WorldRules: []domain.WorldRule{}}); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	dossier := domain.AdaptationCoCreateDossier{
		Version:            1,
		PromptVersion:      adaptpkg.CoCreateDossierPromptVersion,
		SourcePath:         sourcePath,
		SourceChapterCount: 1,
		SourceSignature:    storepkg.AdaptationSourceSignature(sourceManifest),
		BatchSize:          adaptpkg.CoCreateDossierBatchSize,
		BatchRuneLimit:     adaptpkg.CoCreateDossierBatchRuneLimit,
		Batches: []domain.AdaptationCoCreateDossierBatch{
			{Index: 1, SourceFrom: 1, SourceTo: 1, SourceSignature: storepkg.AdaptationDossierBatchSpecs(sourceManifest, adaptpkg.CoCreateDossierBatchSize, adaptpkg.CoCreateDossierBatchRuneLimit)[0].SourceSignature},
		},
	}
	if err := st.Adaptation.SaveCoCreateDossier(dossier); err != nil {
		t.Fatalf("SaveCoCreateDossier: %v", err)
	}
}

func testPendingAdaptBriefing() *domain.AdaptationCoCreateBriefing {
	return testPendingAdaptBriefingWithDecisionCount(1)
}

func testReadyAdaptBriefing() *domain.AdaptationCoCreateBriefing {
	briefing := testPendingAdaptBriefing()
	briefing.Decisions = nil
	briefing.ResolvedDecisions = nil
	return briefing
}

func testPendingAdaptBriefingWithDecisionCount(count int) *domain.AdaptationCoCreateBriefing {
	if count < 1 {
		count = 1
	}
	decisions := make([]domain.AdaptationBriefingDecision, 0, count)
	for index := 1; index <= count; index++ {
		id := fmt.Sprintf("q%d", index)
		decisions = append(decisions, domain.AdaptationBriefingDecision{
			ID:       id,
			Question: fmt.Sprintf("How should decision %d be handled?", index),
			Evidence: fmt.Sprintf("chapter %d shows a decision risk", index),
			Impact:   "changes relationship cleanup rules for later arcs",
			Required: true,
			Status:   "pending",
			Options: []domain.AdaptationDecisionOption{
				{ID: "a", Label: "Remove ambiguity", Description: "Keep the side character as an ally only"},
				{ID: "b", Label: "Keep as friendship", Description: "Rewrite intimacy into ordinary trust"},
			},
			RecommendedOptionID: "a",
		})
	}
	decisions[0].Question = "How should the side romance be handled?"
	decisions[0].Evidence = "chapter 90 shows a confession risk"
	return &domain.AdaptationCoCreateBriefing{
		Version:           1,
		PromptVersion:     adaptpkg.CoCreateBriefingPromptVersion,
		TriggerReason:     "source_chapter_count>320",
		IntentHash:        "intent",
		ConfirmedFacts:    []string{"source couple milestone is unclear"},
		ResolvedDecisions: nil,
		Decisions:         decisions,
	}
}

func webCoCreateReply(message, draft string, ready bool, suggestions ...string) host.CoCreateReply {
	return host.CoCreateReply{
		Message:     message,
		Prompt:      draft,
		Ready:       ready,
		Suggestions: suggestions,
		Raw:         "<reply>" + message + "</reply><draft>" + draft + "</draft><ready>true</ready><suggestions></suggestions>",
	}
}

func coCreateHistoryContains(history []host.CoCreateMessage, text string) bool {
	for _, message := range history {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

func latestUserMessageContent(history []host.CoCreateMessage) string {
	for idx := len(history) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(history[idx].Role) == "user" {
			return history[idx].Content
		}
	}
	return ""
}

func webCoCreateMessagesContain(messages []webCoCreateMessage, text string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

func latestHostEventByKind(events []WebEvent, kind string) *APIHostEvent {
	for idx := len(events) - 1; idx >= 0; idx-- {
		event := events[idx].Event
		if event != nil && event.Kind == kind {
			return event
		}
	}
	return nil
}
