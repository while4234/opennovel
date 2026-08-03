package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestManuscriptActionDialogueReadyExecuteAndIdempotency(t *testing.T) {
	server, manifest, st, chapterID, baseline, fake := seedManuscriptActionDialogueProject(t)
	defer server.Close()
	fake.manuscriptActionClarifications = []host.ManuscriptActionClarification{{
		Status: "ready", AssistantMessage: "要求明确", ResolvedInstruction: "保留事实，只压缩重复表达",
	}}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	createPayload := map[string]any{
		"chapter_id": chapterID, "content_signature": baseline.CurrentProseSHA256, "type": "polish",
		"initial_input": "压缩重复表达", "structure_revision": domain.StructureRevision(volumes),
		"structure_signature": domain.StructureSignature(volumes), "idempotency_key": "create-ready",
	}
	created := requestManuscriptAction(t, server, manifest.ID, "", createPayload)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	dialogue := decodeManuscriptActionDialogue(t, created.Body.Bytes())
	if dialogue.Status != "ready" || dialogue.ResolvedInstruction == "" || len(fake.manuscriptActionRequests) != 1 {
		t.Fatalf("created dialogue=%+v calls=%d", dialogue, len(fake.manuscriptActionRequests))
	}
	if fake.manuscriptActionRequests[0].Context.Prose != "正式正文" {
		t.Fatalf("clarifier did not receive authoritative prose: %+v", fake.manuscriptActionRequests[0].Context)
	}
	replayed := requestManuscriptAction(t, server, manifest.ID, "", createPayload)
	if replayed.Code != http.StatusOK || len(fake.manuscriptActionRequests) != 1 || !bytes.Contains(replayed.Body.Bytes(), []byte(`"replayed":true`)) {
		t.Fatalf("create replay status=%d body=%s calls=%d", replayed.Code, replayed.Body.String(), len(fake.manuscriptActionRequests))
	}
	executePayload := map[string]any{"expected_version": dialogue.Version, "idempotency_key": "execute-ready"}
	executed := requestManuscriptAction(t, server, manifest.ID, "/"+dialogue.ID+"/execute", executePayload)
	if executed.Code != http.StatusAccepted {
		t.Fatalf("execute status=%d body=%s", executed.Code, executed.Body.String())
	}
	completed := decodeManuscriptActionDialogue(t, executed.Body.Bytes())
	if completed.Status != "completed" || !bytes.Contains(completed.Result, []byte(`"kind":"revision"`)) {
		t.Fatalf("completed dialogue=%+v result=%s", completed, completed.Result)
	}
	active, err := st.ManuscriptRevisions.Active()
	if err != nil || active == nil || active.Baseline.ChapterID != chapterID {
		t.Fatalf("safe preview active=%+v err=%v", active, err)
	}
	executeReplay := requestManuscriptAction(t, server, manifest.ID, "/"+dialogue.ID+"/execute", executePayload)
	if executeReplay.Code != http.StatusOK || !bytes.Contains(executeReplay.Body.Bytes(), []byte(`"replayed":true`)) {
		t.Fatalf("execute replay status=%d body=%s", executeReplay.Code, executeReplay.Body.String())
	}
	// Simulate a process crash after the underlying idempotent preview committed
	// but before the dialogue completion receipt was durable.
	document, err := loadManuscriptActionDialogueDocument(manifest.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	document.Receipts = nil
	document.Dialogue.Status = "executing"
	document.Dialogue.Result = nil
	if err := saveManuscriptActionDialogueDocument(manifest.OutputDir, document); err != nil {
		t.Fatal(err)
	}
	crashRetry := requestManuscriptAction(t, server, manifest.ID, "/"+dialogue.ID+"/execute", executePayload)
	if crashRetry.Code != http.StatusAccepted || decodeManuscriptActionDialogue(t, crashRetry.Body.Bytes()).Status != "completed" {
		t.Fatalf("crash-safe execute retry status=%d body=%s", crashRetry.Code, crashRetry.Body.String())
	}
}

func TestManuscriptActionDialogueQuestionsVersionAndInputBoundary(t *testing.T) {
	server, manifest, st, chapterID, baseline, fake := seedManuscriptActionDialogueProject(t)
	defer server.Close()
	fake.manuscriptActionClarifications = []host.ManuscriptActionClarification{
		{Status: "needs_input", AssistantMessage: "范围会影响剧情", Questions: []host.ManuscriptActionQuestion{{ID: "scope", Prompt: "只改冲突场景，还是整章？"}}},
		{Status: "ready", AssistantMessage: "范围已确认", ResolvedInstruction: "只改冲突场景，保留其他段落"},
	}
	volumes, _ := st.Outline.LoadLayeredOutline()
	createPayload := map[string]any{
		"chapter_id": chapterID, "content_signature": baseline.CurrentProseSHA256, "type": "rewrite", "initial_input": "让冲突更强",
		"structure_revision": domain.StructureRevision(volumes), "structure_signature": domain.StructureSignature(volumes), "idempotency_key": "create-question",
	}
	created := requestManuscriptAction(t, server, manifest.ID, "", createPayload)
	dialogue := decodeManuscriptActionDialogue(t, created.Body.Bytes())
	if created.Code != http.StatusCreated || dialogue.Status != "needs_input" || len(dialogue.Questions) != 1 {
		t.Fatalf("question create status=%d dialogue=%+v", created.Code, dialogue)
	}
	questionID := dialogue.Questions[0].ID
	wrongVersion := requestManuscriptAction(t, server, manifest.ID, "/"+dialogue.ID+"/reply", map[string]any{"question_id": questionID, "answer": "只改冲突场景", "expected_version": dialogue.Version + 1, "idempotency_key": "reply-wrong"})
	if wrongVersion.Code != http.StatusConflict {
		t.Fatalf("wrong version status=%d body=%s", wrongVersion.Code, wrongVersion.Body.String())
	}
	replyPayload := map[string]any{"question_id": questionID, "answer": "只改冲突场景", "expected_version": dialogue.Version, "idempotency_key": "reply-one"}
	replied := requestManuscriptAction(t, server, manifest.ID, "/"+dialogue.ID+"/reply", replyPayload)
	ready := decodeManuscriptActionDialogue(t, replied.Body.Bytes())
	if replied.Code != http.StatusOK || ready.Status != "ready" || ready.ResolvedInstruction == "" || len(fake.manuscriptActionRequests) != 2 {
		t.Fatalf("reply status=%d dialogue=%+v calls=%d", replied.Code, ready, len(fake.manuscriptActionRequests))
	}
	replyReplay := requestManuscriptAction(t, server, manifest.ID, "/"+dialogue.ID+"/reply", replyPayload)
	if replyReplay.Code != http.StatusOK || len(fake.manuscriptActionRequests) != 2 || !bytes.Contains(replyReplay.Body.Bytes(), []byte(`"replayed":true`)) {
		t.Fatalf("reply replay status=%d body=%s", replyReplay.Code, replyReplay.Body.String())
	}
	reusedKey := map[string]any{"question_id": questionID, "answer": "整章", "expected_version": dialogue.Version, "idempotency_key": "reply-one"}
	conflict := requestManuscriptAction(t, server, manifest.ID, "/"+dialogue.ID+"/reply", reusedKey)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("reused key status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	unknownProse := map[string]any{
		"chapter_id": chapterID, "content_signature": baseline.CurrentProseSHA256, "type": "polish", "initial_input": "test",
		"structure_revision": domain.StructureRevision(volumes), "structure_signature": domain.StructureSignature(volumes), "idempotency_key": "client-prose", "prose": "forbidden client prose",
	}
	rejected := requestManuscriptAction(t, server, manifest.ID, "", unknownProse)
	if rejected.Code != http.StatusBadRequest || strings.Contains(rejected.Body.String(), "forbidden client prose") {
		t.Fatalf("client prose boundary status=%d body=%s", rejected.Code, rejected.Body.String())
	}
}

func TestManuscriptActionDialogueRejectsSignatureDriftAndCropsDeterministically(t *testing.T) {
	server, manifest, st, chapterID, baseline, fake := seedManuscriptActionDialogueProject(t)
	defer server.Close()
	longProse := strings.Repeat("长正文", 4000)
	if err := st.Drafts.SaveFinalChapter(1, longProse); err != nil {
		t.Fatal(err)
	}
	baseline, _, _ = fake.manuscriptService.CurrentChapter(chapterID)
	fake.manuscriptActionClarifications = []host.ManuscriptActionClarification{{Status: "ready", ResolvedInstruction: "润色"}}
	volumes, _ := st.Outline.LoadLayeredOutline()
	created := requestManuscriptAction(t, server, manifest.ID, "", map[string]any{
		"chapter_id": chapterID, "content_signature": baseline.CurrentProseSHA256, "type": "polish", "initial_input": "润色",
		"structure_revision": domain.StructureRevision(volumes), "structure_signature": domain.StructureSignature(volumes), "idempotency_key": "crop-create",
	})
	dialogue := decodeManuscriptActionDialogue(t, created.Body.Bytes())
	if created.Code != http.StatusCreated || len(fake.manuscriptActionRequests) != 1 || !fake.manuscriptActionRequests[0].Context.ProseCropped || !strings.Contains(fake.manuscriptActionRequests[0].Context.Prose, "确定性预算裁剪") {
		t.Fatalf("cropped context status=%d request=%+v", created.Code, fake.manuscriptActionRequests)
	}
	if err := st.Drafts.SaveFinalChapter(1, "正文已被外部修改"); err != nil {
		t.Fatal(err)
	}
	drift := requestManuscriptAction(t, server, manifest.ID, "/"+dialogue.ID+"/execute", map[string]any{"expected_version": dialogue.Version, "idempotency_key": "crop-execute"})
	if drift.Code != http.StatusConflict || !bytes.Contains(drift.Body.Bytes(), []byte(`"code":"preview_stale"`)) {
		t.Fatalf("signature drift status=%d body=%s", drift.Code, drift.Body.String())
	}
}

func TestBuildManuscriptActionContextIncludesRecentSummaries(t *testing.T) {
	server, _, st, chapterID, _, fake := seedManuscriptActionDialogueProject(t)
	defer server.Close()
	secondID := "ch_22222222222222222222222222222222"
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{ID: chapterID, Chapter: 1, Title: "第一章", CoreEvent: "确认订婚", Hook: "地址暴露", Scenes: []string{"确认事实"}},
		{ID: secondID, Chapter: 2, Title: "第二章", CoreEvent: "继续接近", Hook: "家宴", Scenes: []string{"延续事实"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(2, "第二章正文"); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:   1,
		Summary:   "苏瑾琛已明确得知刘子昊订婚并与未婚妻同住。",
		KeyEvents: []string{"刘子昊亲口说明订婚三个月"},
	}); err != nil {
		t.Fatal(err)
	}
	contextBundle, _, err := buildManuscriptActionContext(st, fake.manuscriptService, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if len(contextBundle.RecentSummaries) != 1 || !strings.Contains(contextBundle.RecentSummaries[0].Summary, "已明确得知") {
		t.Fatalf("recent summaries missing from action context: %+v", contextBundle.RecentSummaries)
	}
}

func seedManuscriptActionDialogueProject(t *testing.T) (*Server, ProjectManifest, *storepkg.Store, string, domain.ManuscriptBaseline, *fakeProjectHost) {
	t.Helper()
	server := NewServer(testWebConfig(t), assets.Load("default"), testTempDir(t))
	manifest, err := server.store.CreateProject("manuscript action dialogue")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	chapterID := "ch_abcdef0123456789abcdef0123456789"
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "第一章", CoreEvent: "选择", Hook: "代价", Scenes: []string{"冲突"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("测试书", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "正式正文"); err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.manuscriptService = host.NewManuscriptRevisionServiceWithRuntime(st, noChangeManuscriptWriter{}, nil)
	baseline, _, err := fake.manuscriptService.CurrentChapter(chapterID)
	if err != nil {
		t.Fatal(err)
	}
	return server, manifest, st, chapterID, baseline, fake
}

func requestManuscriptAction(t *testing.T, server *Server, projectID, suffix string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return requestWorkspace(t, server, http.MethodPost, "/api/projects/"+projectID+"/manuscript/actions/dialogues"+suffix, body, "")
}

func decodeManuscriptActionDialogue(t *testing.T, payload []byte) manuscriptActionDialogue {
	t.Helper()
	var response struct {
		Dialogue manuscriptActionDialogue `json:"dialogue"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode manuscript action dialogue: %v payload=%s", err, payload)
	}
	return response.Dialogue
}
