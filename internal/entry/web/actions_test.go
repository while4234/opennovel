package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestProjectStartQuickUsesPreparedStartupPrompt(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Quick Start")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/start", bytes.NewBufferString(`{"text":"写一个月城悬疑故事"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.prepareRulesCalls != 1 {
		t.Fatalf("PrepareUserRules calls = %d, want 1", fake.prepareRulesCalls)
	}
	if fake.startPreparedCalls != 1 {
		t.Fatalf("StartPrepared calls = %d, want 1", fake.startPreparedCalls)
	}
	if !strings.Contains(fake.preparedRulesPrompt, "写一个月城悬疑故事") {
		t.Fatalf("prepared rules prompt lost raw user prompt: %q", fake.preparedRulesPrompt)
	}
	if !strings.Contains(fake.startPreparedPrompt, "写一个月城悬疑故事") {
		t.Fatalf("start prompt lost user prompt: %q", fake.startPreparedPrompt)
	}
}

func TestProjectStartQuickPersistsTargetTotalWords(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Quick Budget")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/start", bytes.NewBufferString(`{"text":"写一部短篇小说","target_total_words":5000}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.setWordBudgetCalls != 1 || fake.wordBudget == nil || fake.wordBudget.TargetTotalWords != 5000 {
		t.Fatalf("SetWordBudget calls=%d budget=%+v", fake.setWordBudgetCalls, fake.wordBudget)
	}
	if !strings.Contains(fake.startPreparedPrompt, "target_total_words=5000") {
		t.Fatalf("start prompt missing target_total_words: %q", fake.startPreparedPrompt)
	}
}

func TestProjectStartQuickRejectsProjectWithExistingBookState(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Existing State")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{NovelName: "旧书", TotalChapters: 10}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/start", bytes.NewBufferString(`{"text":"新故事"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("start status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if fake.startPreparedCalls != 0 {
		t.Fatalf("existing book state should not call StartPrepared, calls=%d", fake.startPreparedCalls)
	}
}

func TestProjectPauseCallsAbort(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Pause")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/pause", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("pause status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.abortCalls != 1 {
		t.Fatalf("Abort calls = %d, want 1", fake.abortCalls)
	}
	if !strings.Contains(rec.Body.String(), `"stopped":true`) {
		t.Fatalf("pause response should report stopped=true: %s", rec.Body.String())
	}
}

func TestProjectRollbackPreviewAndConfirmRoutes(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Rollback")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	preview := domain.RollbackPreviewWithHash(domain.RollbackPreview{
		CanRollback:   true,
		Mode:          "normal",
		CurrentStage:  "writing",
		TargetStage:   domain.RollbackStageChapterOutline,
		TargetLabel:   "详细章节提纲完成待审核",
		DeletePaths:   []string{"chapters/"},
		PreservePaths: []string{"outline.json"},
	})
	fake.rollbackPreview = preview
	fake.rollbackResult = domain.RollbackResult{
		Preview:      preview,
		DeletedPaths: []string{"chapters"},
	}
	fake.snapshot = host.UISnapshot{Phase: string(domain.PhaseOutline)}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/rollback/preview", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", rec.Code, rec.Body.String())
	}
	var previewResponse struct {
		Rollback domain.RollbackPreview `json:"rollback"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&previewResponse); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if previewResponse.Rollback.PreviewHash == "" || previewResponse.Rollback.TargetStage != domain.RollbackStageChapterOutline {
		t.Fatalf("preview response = %+v", previewResponse.Rollback)
	}

	body := `{"confirm":true,"preview_hash":"` + previewResponse.Rollback.PreviewHash + `"}`
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/rollback", bytes.NewBufferString(body))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.rollbackPreviewCalls != 1 || fake.rollbackCalls != 1 {
		t.Fatalf("rollback calls preview=%d execute=%d", fake.rollbackPreviewCalls, fake.rollbackCalls)
	}
	if !strings.Contains(rec.Body.String(), `"deleted_paths":["chapters"]`) {
		t.Fatalf("rollback response missing deleted paths: %s", rec.Body.String())
	}
}

func TestProjectChapterReviseFailsClosedWhenStableIDCannotResolve(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Chapter Revise")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{RuntimeState: "running", IsRunning: true, Phase: "writing", PendingRewrites: []int{2}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/chapters/revise", bytes.NewBufferString(`{"chapter":2,"instruction":"加强悬念","mode":"polish"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("chapter revise status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.reviseChapterCalls != 0 {
		t.Fatalf("legacy ReviseChapter calls = %d, want 0", fake.reviseChapterCalls)
	}
}

func TestProjectChapterReviseRejectsInvalidRequest(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Bad Chapter Revise")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/chapters/revise", bytes.NewBufferString(`{"chapter":2,"instruction":" ","mode":"rewrite"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("chapter revise status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.reviseChapterCalls != 0 {
		t.Fatalf("invalid request should not call host, calls=%d", fake.reviseChapterCalls)
	}
}

func TestProjectChapterOutlineReviseCallsHostFlow(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Chapter Outline Revise")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{RuntimeState: "idle", Phase: "writing"}
	fake.reviseChapterOutlineResult = host.ChapterOutlineRevisionResult{
		Chapter:       100,
		Instruction:   "加强本章反转，并保持卷弧目标不变",
		Label:         "第100章详细提纲已更新",
		Outline:       domain.OutlineEntry{Chapter: 100, Title: "新的反转"},
		RewriteQueued: false,
		DraftReset:    false,
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+manifest.ID+"/outline/chapters/revise",
		bytes.NewBufferString(`{"chapter":100,"instruction":"加强本章反转，并保持卷弧目标不变"}`),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("chapter outline revise status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.reviseChapterOutlineCalls != 1 {
		t.Fatalf("ReviseChapterOutline calls = %d, want 1", fake.reviseChapterOutlineCalls)
	}
	if fake.reviseChapterOutlineRequest.Chapter != 100 ||
		fake.reviseChapterOutlineRequest.Instruction != "加强本章反转，并保持卷弧目标不变" {
		t.Fatalf("ReviseChapterOutline request = %+v", fake.reviseChapterOutlineRequest)
	}
	var body struct {
		Project  ProjectManifest           `json:"project"`
		Snapshot host.UISnapshot           `json:"snapshot"`
		Running  bool                      `json:"running"`
		Revision apiChapterOutlineRevision `json:"revision"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode chapter outline revise response: %v", err)
	}
	if body.Project.ID != manifest.ID || body.Running || body.Revision.Chapter != 100 || body.Revision.Outline.Title != "新的反转" {
		t.Fatalf("unexpected chapter outline revise response: %+v", body)
	}
}

func TestProjectChapterOutlineReviseRejectsInvalidRequest(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Bad Chapter Outline Revise")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+manifest.ID+"/outline/chapters/revise",
		bytes.NewBufferString(`{"chapter":100,"instruction":" "}`),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("chapter outline revise status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.reviseChapterOutlineCalls != 0 {
		t.Fatalf("invalid request should not call host, calls=%d", fake.reviseChapterOutlineCalls)
	}
}

func TestProjectChapterOutlineReviseMapsHostErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "precondition", err: fmt.Errorf("chapter already in progress: %w", errs.ErrToolPrecondition), status: http.StatusConflict},
		{name: "model failure", err: errors.New("model unavailable"), status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
			defer server.Close()
			manifest, err := server.store.CreateProject("Outline Error " + tt.name)
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			fake := installFakeSession(t, server, manifest)
			fake.reviseChapterOutlineErr = tt.err

			req := httptest.NewRequest(
				http.MethodPost,
				"/api/projects/"+manifest.ID+"/outline/chapters/revise",
				bytes.NewBufferString(`{"chapter":12,"instruction":"increase tension"}`),
			)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)

			if rec.Code != tt.status {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), tt.status)
			}
			if fake.reviseChapterOutlineCalls != 1 {
				t.Fatalf("ReviseChapterOutline calls = %d, want 1", fake.reviseChapterOutlineCalls)
			}
		})
	}
}

func TestProjectChapterContentReadsFinalChapter(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Chapter Content")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Drafts.SaveFinalChapter(3, "chapter body"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/chapters/3", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("chapter content status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Chapter apiChapterContent `json:"chapter"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode chapter content response: %v", err)
	}
	if body.Chapter.Chapter != 3 || body.Chapter.Content != "chapter body" || body.Chapter.WordCount != len("chapter body") || body.Chapter.Source != "final" {
		t.Fatalf("unexpected chapter content: %+v", body.Chapter)
	}
}

func TestProjectImportSavesSourceUnderProjectWithoutStartingWriting(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("External Import")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := newImportRequest(t, "/api/projects/"+manifest.ID+"/import", "source.txt", "第一章\n第二章", "2")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "uploads", "import", "source.txt")
	if fake.importNovelPath != wantPath {
		t.Fatalf("import path = %q, want %q", fake.importNovelPath, wantPath)
	}
	if fake.importNovelResumeFrom != 2 {
		t.Fatalf("resumeFrom = %d, want 2", fake.importNovelResumeFrom)
	}
	if fake.resumeCalls != 0 {
		t.Fatalf("import must wait for continuation approval before writing, calls=%d", fake.resumeCalls)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("import source was not saved: %v", err)
	}
}

func TestProjectContinuationSourceImportsWithoutStartingWriting(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Continuation Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.continuationSnapshot = testContinuationSnapshot(domain.ContinuationStageSourceReady, 1)

	req := newImportRequest(t, "/api/projects/"+manifest.ID+"/continuation/source", "source.txt", "第一章\n第二章", "")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("continuation source status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "uploads", "continuation", "source.txt")
	if fake.importNovelPath != wantPath {
		t.Fatalf("continuation import path = %q, want %q", fake.importNovelPath, wantPath)
	}
	if fake.resumeCalls != 0 {
		t.Fatalf("continuation source import must not resume writing, calls=%d", fake.resumeCalls)
	}
	var body struct {
		Continuation *domain.ContinuationSnapshot `json:"continuation"`
		Running      bool                         `json:"running"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode continuation source response: %v", err)
	}
	if body.Continuation == nil || body.Continuation.Workflow.Stage != domain.ContinuationStageSourceReady || body.Running {
		t.Fatalf("unexpected continuation source response: %+v", body)
	}
}

func TestProjectContinuationStartResumesOnlyOnce(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Continuation Start")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.continuationSnapshot = testContinuationSnapshot(domain.ContinuationStageReadyToWrite, 7)

	for attempt, wantStatus := range []int{http.StatusOK, http.StatusConflict} {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/projects/"+manifest.ID+"/continuation/start",
			bytes.NewBufferString(`{"expected_revision":7}`),
		)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("attempt %d status = %d body=%s, want %d", attempt+1, rec.Code, rec.Body.String(), wantStatus)
		}
	}
	if fake.resumeCalls != 1 {
		t.Fatalf("continuation start resumed writing %d times, want exactly 1", fake.resumeCalls)
	}
	if fake.continuationSnapshot.Workflow.Stage != domain.ContinuationStageWriting {
		t.Fatalf("continuation stage = %q, want writing", fake.continuationSnapshot.Workflow.Stage)
	}
}

func TestProjectContinuationMutationRejectsStaleRevision(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Continuation Revision")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.continuationSnapshot = testContinuationSnapshot(domain.ContinuationStageProposalGenerating, 4)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+manifest.ID+"/continuation/proposal/generate",
		bytes.NewBufferString(`{"expected_revision":3}`),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("stale continuation mutation status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
}

func testContinuationSnapshot(stage domain.ContinuationStage, revision int) *domain.ContinuationSnapshot {
	return &domain.ContinuationSnapshot{Workflow: domain.ContinuationWorkflow{
		Version:          domain.ContinuationSchemaVersion,
		Stage:            stage,
		SourceSignature:  "source-signature",
		BaseChapterCount: 2,
		Revision:         revision,
	}}
}

func TestProjectExportUsesProjectExportsDir(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Export")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.exportResult = &exp.Result{Path: filepath.Join(manifest.RootDir, "exports", "book.epub"), Chapters: 3, Bytes: 1024, Skipped: []int{2}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/export", bytes.NewBufferString(`{"path":"book.epub","format":"epub","from":1,"to":3,"overwrite":true}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "exports", "book.epub")
	if fake.exportOptions.OutPath != wantPath {
		t.Fatalf("export path = %q, want %q", fake.exportOptions.OutPath, wantPath)
	}
	if fake.exportOptions.Format != exp.FormatEPUB || fake.exportOptions.From != 1 || fake.exportOptions.To != 3 || !fake.exportOptions.Overwrite {
		t.Fatalf("export options = %+v", fake.exportOptions)
	}
	var body struct {
		Export apiExportResult `json:"export"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if body.Export.Chapters != 3 || body.Export.Bytes != 1024 || len(body.Export.Skipped) != 1 {
		t.Fatalf("export result = %+v", body.Export)
	}
}

func TestProjectExportDownloadReturnsBytesForBrowserSave(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Browser Export")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{IsRunning: true, RuntimeState: "running"}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/export/download", bytes.NewBufferString(`{"path":"browser-book","format":"txt","from":1,"to":2}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("export download status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.exportCalls != 1 {
		t.Fatalf("Export calls = %d, want 1", fake.exportCalls)
	}
	if fake.exportOptions.OutPath == "" || filepath.Dir(fake.exportOptions.OutPath) == filepath.Join(manifest.RootDir, "exports") {
		t.Fatalf("download export should use a temp out path, got %q", fake.exportOptions.OutPath)
	}
	if fake.exportOptions.Format != exp.FormatTXT || !fake.exportOptions.Overwrite {
		t.Fatalf("export options = %+v", fake.exportOptions)
	}
	if got := rec.Header().Get("X-AINovel-Export-Name"); got != "browser-book.txt" {
		t.Fatalf("export name header = %q, want browser-book.txt", got)
	}
	if got := rec.Header().Get("X-AINovel-Export-Chapters"); got != "1" {
		t.Fatalf("export chapters header = %q, want 1", got)
	}
	if body := rec.Body.String(); body != "fake export" {
		t.Fatalf("export body = %q, want fake export", body)
	}
	if fake.abortCalls != 0 {
		t.Fatalf("preview export must not interrupt active writing, abort calls = %d", fake.abortCalls)
	}
}

func TestProjectExportAppendsSelectedFormatExtension(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Export Extension")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/export", bytes.NewBufferString(`{"path":"plain-title","format":"txt","overwrite":true}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "exports", "plain-title.txt")
	if fake.exportOptions.OutPath != wantPath {
		t.Fatalf("export path = %q, want %q", fake.exportOptions.OutPath, wantPath)
	}
	if fake.exportOptions.Format != exp.FormatTXT {
		t.Fatalf("export format = %q, want %q", fake.exportOptions.Format, exp.FormatTXT)
	}
	var body struct {
		Export apiExportResult `json:"export"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if body.Export.Path != wantPath {
		t.Fatalf("response export path = %q, want %q", body.Export.Path, wantPath)
	}
}

func TestProjectExportRejectsFormatExtensionMismatch(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Export Mismatch")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/export", bytes.NewBufferString(`{"path":"book.epub","format":"txt","overwrite":true}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("export status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.exportCalls != 0 {
		t.Fatalf("mismatched export path should not call host, calls=%d", fake.exportCalls)
	}
}

func TestProjectExportRejectsAbsolutePath(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Unsafe Export")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/export", bytes.NewBufferString(`{"path":"C:\\temp\\book.txt","format":"txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("export status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.exportCalls != 0 {
		t.Fatalf("unsafe export path should not call host, calls=%d", fake.exportCalls)
	}
}

func TestProjectDiagReturnsReportAndWritesExport(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Diagnostics")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/diag", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("diag status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Project    ProjectManifest `json:"project"`
		Report     map[string]any  `json:"report"`
		ExportPath string          `json:"export_path"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode diag: %v", err)
	}
	if body.Project.ID != manifest.ID || body.Report == nil {
		t.Fatalf("diag response missing project/report: %+v", body)
	}
	if body.ExportPath != "" {
		if _, err := os.Stat(body.ExportPath); err != nil {
			t.Fatalf("diag export was not written: %v", err)
		}
	}
}

func newImportRequest(t *testing.T, path, filename, body, from string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if from != "" {
		if err := writer.WriteField("from", from); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	part, err := writer.CreateFormFile("source", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
