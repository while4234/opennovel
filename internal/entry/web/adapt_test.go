package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	adaptpkg "github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestProjectAdaptSourceUploadSavesSourceUnderProjectUploads(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	manifest, err := server.store.CreateProject("Adapt Upload")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/source", []testMultipartFile{
		{field: "source", filename: "source-novel.txt", body: "第1章 开始\n原文内容"},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantPath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source-novel.txt")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("uploaded adaptation source was not saved under project uploads: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manifest.RootDir, "simulate", "source-novel.txt")); !os.IsNotExist(err) {
		t.Fatalf("adaptation source must not be saved under simulate, stat err=%v", err)
	}

	var response struct {
		SourceFile apiUploadedFile `json:"source_file"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if response.SourceFile.RelativePath != "source-novel.txt" {
		t.Fatalf("source relative path = %q, want source-novel.txt", response.SourceFile.RelativePath)
	}
}

func TestProjectAdaptSourceUploadDoesNotWriteDuringActiveRevision(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Blocked Adapt Upload")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.sessions.Open(manifest.ID); err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	impact, err := domain.NewRevisionImpact("active revision", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "prose", Change: "rewrite",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeAutoResumeRevisionPolicy{}, storepkg.StartRevisionInput{
		Intent: "revise", Impact: impact, IdempotencyKey: "block-adapt-upload",
	}); err != nil {
		t.Fatal(err)
	}

	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/source", []testMultipartFile{
		{field: "source", filename: "blocked.txt", body: "must not be written"},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(manifest.RootDir, "uploads", "adaptation", "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked upload changed project files: %v", err)
	}
}

func TestProjectAdaptSourceUploadAllowsSourceLargerThanFormerTenMiBLimit(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Large Adaptation Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	body := textBodyLargerThanFormerLimit()
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/source", []testMultipartFile{
		{field: "source", filename: "long-source.txt", body: body},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(filepath.Join(manifest.RootDir, "uploads", "adaptation", "long-source.txt"))
	if err != nil {
		t.Fatalf("stat uploaded source: %v", err)
	}
	if info.Size() <= formerTextUploadLimit {
		t.Fatalf("uploaded source size = %d, want greater than old limit %d", info.Size(), formerTextUploadLimit)
	}
}

func TestProjectAdaptSourceRejectsUnsafeAndMultipleFiles(t *testing.T) {
	cases := []struct {
		name   string
		files  []testMultipartFile
		status int
		want   string
	}{
		{
			name:   "path traversal",
			files:  []testMultipartFile{{field: "source", filename: "../source.txt", body: "source"}},
			status: http.StatusBadRequest,
			want:   "path separators",
		},
		{
			name:   "absolute path",
			files:  []testMultipartFile{{field: "source", filename: "C:\\temp\\source.txt", body: "source"}},
			status: http.StatusBadRequest,
			want:   "absolute path",
		},
		{
			name:   "unsupported extension",
			files:  []testMultipartFile{{field: "source", filename: "source.json", body: "{}"}},
			status: http.StatusBadRequest,
			want:   "unsupported extension",
		},
		{
			name: "multiple files",
			files: []testMultipartFile{
				{field: "source", filename: "one.txt", body: "one"},
				{field: "source", filename: "two.txt", body: "two"},
			},
			status: http.StatusBadRequest,
			want:   "exactly one",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
			defer server.Close()
			manifest, err := server.store.CreateProject(c.name)
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/source", c.files)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)

			if rec.Code != c.status {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), c.status)
			}
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("body %q does not contain %q", rec.Body.String(), c.want)
			}
		})
	}
}

func TestProjectAdaptAnalyzeUsesProjectAdaptationUploadPath(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Analyze")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	sourceDir := filepath.Join(manifest.RootDir, "uploads", "adaptation")
	if err := os.WriteFile(filepath.Join(sourceDir, "source.txt"), []byte("第1章 开始\n内容"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"source.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	want := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
	waitForTestCondition(t, "adaptation host call", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.adaptSourcePath == want
	})
	if fake.adaptSourcePath != want {
		t.Fatalf("adapt source path = %q, want %q", fake.adaptSourcePath, want)
	}
	if strings.Contains(filepath.Clean(fake.adaptSourcePath), filepath.Clean("D:\\ainovel\\uploads\\adaptation")) {
		t.Fatalf("adapt source path should not point at repository uploads: %q", fake.adaptSourcePath)
	}
}

func TestProjectSnapshotRestoresUploadedAdaptationSource(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Restore")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	uploadAdaptationSourceForTest(t, server, manifest, "source.txt", "Chapter 1\nsource body")

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response projectSnapshotResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if response.Adaptation.SourceFile == nil {
		t.Fatalf("snapshot did not restore adaptation source: %+v", response.Adaptation)
	}
	if response.Adaptation.SourceFile.RelativePath != "source.txt" {
		t.Fatalf("restored source relative path = %q, want source.txt", response.Adaptation.SourceFile.RelativePath)
	}
	if response.Adaptation.AnalysisStatus != "idle" {
		t.Fatalf("analysis status = %q, want idle", response.Adaptation.AnalysisStatus)
	}
}

func TestProjectSnapshotRequiresCurrentCoCreateDossierForDoneAnalysis(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Dossier Status")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init store: %v", err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "One", "source body")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	sourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
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

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response projectSnapshotResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if response.Adaptation.AnalysisStatus != "paused" {
		t.Fatalf("analysis status without dossier = %q, want paused", response.Adaptation.AnalysisStatus)
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
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status after dossier = %d body=%s", rec.Code, rec.Body.String())
	}
	response = projectSnapshotResponse{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode second snapshot response: %v", err)
	}
	if response.Adaptation.AnalysisStatus != "done" {
		t.Fatalf("analysis status with dossier = %q, want done", response.Adaptation.AnalysisStatus)
	}
}

func TestProjectSnapshotReportsActiveAdaptationAnalysis(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Active Snapshot")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	uploadAdaptationSourceForTest(t, server, manifest, "source.txt", "Chapter 1\nsource body")
	fake := installFakeSession(t, server, manifest)
	fake.adaptAnalyzeStarted = make(chan struct{})
	fake.blockAdaptAnalyze = true
	session := server.sessions.Project(manifest.ID)

	analysisErr := make(chan error, 1)
	go func() {
		_, err := session.PrepareAdaptationSource(context.Background(), filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt"))
		analysisErr <- err
	}()

	select {
	case <-fake.adaptAnalyzeStarted:
	case <-time.After(time.Second):
		t.Fatal("adaptation analysis did not start")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response projectSnapshotResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if response.Adaptation.AnalysisStatus != "running" {
		t.Fatalf("analysis status = %q, want running", response.Adaptation.AnalysisStatus)
	}
	if len(response.Adaptation.AnalysisEvents) == 0 || response.Adaptation.AnalysisEvents[0].Stage != "chapter" {
		t.Fatalf("analysis events = %+v, want running chapter event", response.Adaptation.AnalysisEvents)
	}

	if !session.Pause() {
		t.Fatal("Pause should stop the active adaptation analysis")
	}
	select {
	case <-analysisErr:
	case <-time.After(time.Second):
		t.Fatal("adaptation analysis did not stop")
	}
}

func TestProjectAdaptAnalyzeRejectsUnsafeSourceFile(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Unsafe Analyze")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"../evil.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("analyze status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.adaptAnalyzeCalls != 0 {
		t.Fatalf("unsafe source file should not call host, calls=%d", fake.adaptAnalyzeCalls)
	}
}

func TestProjectAdaptAnalyzeSkipsCompletedPreparedSource(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Completed Analyze")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writePreparedAdaptationFixture(t, manifest, "source.txt")
	fake := installFakeSession(t, server, manifest)
	if _, err := server.libraries.SaveNovelFromProject(manifest, "Existing Library Name", "source.txt"); err != nil {
		t.Fatalf("seed existing novel library entry: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"source.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Analyzed     bool                `json:"analyzed"`
		Running      bool                `json:"running"`
		Accepted     bool                `json:"accepted"`
		LibrarySaved bool                `json:"library_saved"`
		LibraryItem  apiLibraryItem      `json:"library_item"`
		Adaptation   apiAdaptationStatus `json:"adaptation"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode analyze response: %v", err)
	}
	if !response.Analyzed || response.Running || response.Accepted {
		t.Fatalf("completed source analyze response = analyzed:%v running:%v accepted:%v", response.Analyzed, response.Running, response.Accepted)
	}
	if response.Adaptation.AnalysisStatus != "done" {
		t.Fatalf("analysis status = %q, want done", response.Adaptation.AnalysisStatus)
	}
	if !response.LibrarySaved || response.LibraryItem.Name != "Existing Library Name" {
		t.Fatalf("completed source library save = saved:%v item:%+v, want existing entry", response.LibrarySaved, response.LibraryItem)
	}
	if _, err := os.Stat(filepath.Join(server.libraries.NovelDir(), "Existing Library Name", novelLibraryManifestName)); err != nil {
		t.Fatalf("auto-saved novel library manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(server.libraries.NovelDir(), "source")); !os.IsNotExist(err) {
		t.Fatalf("auto-save should update the source-file match instead of creating a duplicate, stat err=%v", err)
	}
	if fake.adaptAnalyzeCalls != 0 {
		t.Fatalf("completed source should not call host analyze, calls=%d", fake.adaptAnalyzeCalls)
	}
}

func TestProjectAdaptAnalyzeForceRechecksCompletedPreparedSource(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Forced Incremental Analyze")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writePreparedAdaptationFixture(t, manifest, "source.txt")
	fake := installFakeSession(t, server, manifest)
	started := make(chan struct{})
	fake.adaptAnalyzeStarted = started

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+manifest.ID+"/adapt/analyze",
		bytes.NewBufferString(`{"source_file":"source.txt","force":true}`),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("forced analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("forced analyze did not start incremental source preparation")
	}
	if fake.adaptAnalyzeCalls != 1 {
		t.Fatalf("forced analyze calls = %d, want 1", fake.adaptAnalyzeCalls)
	}
	var response struct {
		Accepted bool `json:"accepted"`
		Running  bool `json:"running"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode forced analyze response: %v", err)
	}
	if !response.Accepted || !response.Running {
		t.Fatalf("forced analyze response = accepted:%v running:%v, want true/true", response.Accepted, response.Running)
	}
}

func TestProjectAdaptAnalyzeAutoSavesCompletedBackgroundAnalysis(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	donor, err := server.store.CreateProject("Prepared Analysis Donor")
	if err != nil {
		t.Fatalf("CreateProject donor: %v", err)
	}
	writePreparedAdaptationFixture(t, donor, "source.txt")

	manifest, err := server.store.CreateProject("Background Analyze")
	if err != nil {
		t.Fatalf("CreateProject target: %v", err)
	}
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")
	fake := installFakeSession(t, server, manifest)
	fake.adaptAnalyzeBeforeDone = func(sourcePath string) {
		targetAdaptationRoot := filepath.Join(manifest.OutputDir, "meta", "adaptation")
		if err := copyPreparedAdaptationFiles(donor.OutputDir, targetAdaptationRoot); err != nil {
			t.Errorf("copy completed adaptation data: %v", err)
			return
		}
		if err := rewriteAdaptationManifestSource(manifest.OutputDir, sourcePath); err != nil {
			t.Errorf("rewrite target adaptation source: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"source.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("analyze status = %d body=%s", rec.Code, rec.Body.String())
	}

	waitForTestCondition(t, "background analysis auto-save", func() bool {
		_, err := os.Stat(filepath.Join(runtimeRoot, novelLibraryDirName, "source", novelLibraryManifestName))
		if err != nil {
			return false
		}
		for _, event := range server.sessions.Project(manifest.ID).HistoryAfter(0) {
			if event.Type == webEventTypeHostEvent && event.Event != nil && event.Event.Category == "LIBRARY" && event.Event.Kind == "novel_auto_save" {
				return true
			}
		}
		return false
	})
	requireLibraryEvent(t, server.sessions.Project(manifest.ID), "novel_auto_save", "source")
}

func TestProjectAdaptStartStrictModesMapRewritePolicy(t *testing.T) {
	cases := []struct {
		mode       string
		wantPolicy string
	}{
		{mode: domain.AdaptationGranularityChapter, wantPolicy: domain.AdaptationRewritePreserveDetails},
		{mode: domain.AdaptationGranularityArc, wantPolicy: domain.AdaptationRewriteFullRewrite},
		{mode: domain.AdaptationGranularityFree, wantPolicy: domain.AdaptationRewriteFullRewrite},
	}

	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
			defer server.Close()
			manifest, err := server.store.CreateProject("Adapt Start " + c.mode)
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			fake := installFakeSession(t, server, manifest)
			writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")

			body := `{"mode":` + strconvQuote(c.mode) + `,"brief":"改成现代悬疑，保留主线"}`
			body = `{"source_file":"source.txt","mode":` + strconvQuote(c.mode) + `,"brief":"adapt this source"}`
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
			}
			if fake.adaptStartCalls != 1 {
				t.Fatalf("adapt start calls = %d, want 1", fake.adaptStartCalls)
			}
			if fake.adaptOptions.Granularity != c.mode || fake.adaptOptions.RewritePolicy != c.wantPolicy {
				t.Fatalf("adapt options = %+v, want mode %s policy %s", fake.adaptOptions, c.mode, c.wantPolicy)
			}
			wantSourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "source.txt")
			if fake.adaptOptions.SourcePath != wantSourcePath {
				t.Fatalf("adapt source path = %q, want %q", fake.adaptOptions.SourcePath, wantSourcePath)
			}
		})
	}
}

func TestProjectAdaptProposalBuildsWithoutStartingWriter(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Proposal")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	writeAdaptationUpload(t, manifest, "source.txt", "Chapter 1\nsource body")

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/proposal", bytes.NewBufferString(`{"source_file":"source.txt","mode":"free","brief":"adapt as a new mystery"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("proposal status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.prepareExternalRulesCalls != 1 {
		t.Fatalf("PrepareExternalSourceUserRules calls=%d, want 1", fake.prepareExternalRulesCalls)
	}
	if fake.adaptProposalCalls != 1 {
		t.Fatalf("BuildAdaptationProposal calls=%d, want 1", fake.adaptProposalCalls)
	}
	if fake.adaptStartCalls != 0 || fake.adaptConfirmCalls != 0 {
		t.Fatalf("proposal must not start or confirm: start=%d confirm=%d", fake.adaptStartCalls, fake.adaptConfirmCalls)
	}
	if fake.adaptProposalOptions.Granularity != domain.AdaptationGranularityFree ||
		fake.adaptProposalOptions.RewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("proposal options = %+v", fake.adaptProposalOptions)
	}
	var response struct {
		Proposal domain.AdaptationPlan `json:"proposal"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	if response.Proposal.Status != domain.AdaptationPlanStatusProposal {
		t.Fatalf("proposal status = %q, want proposal", response.Proposal.Status)
	}
}

func TestProjectAdaptConfirmStartsFromSavedProposal(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Confirm")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/confirm", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptConfirmCalls != 1 {
		t.Fatalf("ConfirmAdaptationProposal calls=%d, want 1", fake.adaptConfirmCalls)
	}
	if fake.adaptStartCalls != 0 {
		t.Fatalf("legacy adapt start calls=%d, want 0", fake.adaptStartCalls)
	}
	var response struct {
		Plan domain.AdaptationPlan `json:"plan"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode confirm: %v", err)
	}
	if response.Plan.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("plan status = %q, want confirmed", response.Plan.Status)
	}
}

func TestProjectAdaptStartFailsAfterNewUploadUntilAnalyzeCompletes(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Stale Adapt Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.requireAnalyzedAdaptSource = true

	uploadAdaptationSourceForTest(t, server, manifest, "old.txt", "Chapter 1\nold source")
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"old.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("old analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	oldPath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "old.txt")
	waitForTestCondition(t, "old adaptation analysis", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.adaptSourcePath == oldPath
	})

	uploadAdaptationSourceForTest(t, server, manifest, "new.txt", "Chapter 1\nnew source")
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(`{"source_file":"new.txt","mode":"chapter","brief":"adapt the new source"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale start status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "has not completed analysis") {
		t.Fatalf("stale start body %q does not explain analysis requirement", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/analyze", bytes.NewBufferString(`{"source_file":"new.txt"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new analyze status = %d body=%s", rec.Code, rec.Body.String())
	}
	newPath := filepath.Join(manifest.RootDir, "uploads", "adaptation", "new.txt")
	waitForTestCondition(t, "new adaptation analysis", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.adaptSourcePath == newPath
	})
	waitForTestCondition(t, "new adaptation analysis completion", func() bool {
		session := server.sessions.Project(manifest.ID)
		return session != nil && !session.isActionRunning(projectActionKindAdaptationAnalysis)
	})
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(`{"source_file":"new.txt","mode":"chapter","brief":"adapt the new source"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh start status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if fake.adaptStartCalls != 2 {
		t.Fatalf("adapt start calls = %d, want stale attempt plus fresh attempt", fake.adaptStartCalls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/continue", bytes.NewBufferString(`{"text":"ordinary path still works"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ordinary continue status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if fake.continueCalls != 1 {
		t.Fatalf("continue calls = %d, want 1", fake.continueCalls)
	}
}

func TestProjectAdaptStartRequiresSourceFile(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Missing Adapt Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(`{"mode":"chapter","brief":"adapt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if fake.adaptStartCalls != 0 {
		t.Fatalf("missing source_file should not call host, calls=%d", fake.adaptStartCalls)
	}
}

func TestProjectAdaptStartRejectsUnsupportedMode(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Bad Adapt Mode")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/start", bytes.NewBufferString(`{"mode":"summary","brief":"改编"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chapter, arc, free") {
		t.Fatalf("body %q does not explain strict modes", rec.Body.String())
	}
	if fake.adaptStartCalls != 0 {
		t.Fatalf("unsupported mode should not call host, calls=%d", fake.adaptStartCalls)
	}
}

func uploadAdaptationSourceForTest(t *testing.T, server *Server, manifest ProjectManifest, filename, body string) {
	t.Helper()
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/source", []testMultipartFile{
		{field: "source", filename: filename, body: body},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload %s status = %d body=%s", filename, rec.Code, rec.Body.String())
	}
}

func writeAdaptationUpload(t *testing.T, manifest ProjectManifest, filename, body string) {
	t.Helper()
	sourceDir := filepath.Join(manifest.RootDir, "uploads", "adaptation")
	if err := os.WriteFile(filepath.Join(sourceDir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("write adaptation upload %s: %v", filename, err)
	}
}
