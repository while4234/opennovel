package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	adaptengine "github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSimulationLibraryUploadSearchAndLoad(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	profile := testWebSimulationProfile("voice.txt", "abc123")
	profileData, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/libraries/simulation/upload", []testMultipartFile{
		{field: "files", filename: "voice.json", body: string(profileData)},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, simulationLibraryDirName, "voice.json")); err != nil {
		t.Fatalf("simulation profile was not saved to library: %v", err)
	}
	libraryData, err := os.ReadFile(filepath.Join(runtimeRoot, simulationLibraryDirName, "voice.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(libraryData, []byte(domain.SimulationPortableProfileVersion)) {
		t.Fatalf("library profile is not portable v2: %s", libraryData)
	}
	portable, err := domain.UnmarshalSimulationPortableProfile(libraryData)
	if err != nil {
		t.Fatal(err)
	}
	if portable.Capabilities.LocalEvidence {
		t.Fatal("portable library profile claims local evidence")
	}
	for _, forbidden := range []string{"source_dir", "source_reports", "voice.txt"} {
		if bytes.Contains(libraryData, []byte(forbidden)) {
			t.Fatalf("portable library profile contains %q", forbidden)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/libraries/simulation?q=voi", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []apiLibraryItem `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "voice" {
		t.Fatalf("search items = %+v, want voice", list.Items)
	}
	if item := list.Items[0]; item.ProfileVersion != domain.SimulationPortableProfileVersion ||
		item.HealthState != "legacy" || !item.Migrated || item.LocalEvidence || item.SourceArchived {
		t.Fatalf("portable simulation metadata = %+v", item)
	}

	req = newMultipartUploadRequest(t, http.MethodPost, "/api/libraries/simulation/upload", []testMultipartFile{
		{field: "files", filename: "voice.json", body: string(profileData)},
	})
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate upload status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}

	manifest, err := server.store.CreateProject("Load Simulation Library")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/library/load", bytes.NewBufferString(`{"name":"voice"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.importCalls != 1 {
		t.Fatalf("import calls = %d, want 1", fake.importCalls)
	}
	wantImportPath := filepath.Join(manifest.RootDir, "profiles", "imported", "voice.json")
	if fake.importPath != wantImportPath {
		t.Fatalf("import path = %q, want %q", fake.importPath, wantImportPath)
	}

	realManifest, err := server.store.CreateProject("Restore Simulation Library")
	if err != nil {
		t.Fatalf("CreateProject real: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+realManifest.ID+"/simulate/library/load", bytes.NewBufferString(`{"name":"voice"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("real load status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(realManifest.OutputDir, "meta", "simulation_profile.json")); err != nil {
		t.Fatalf("simulation profile was not imported into project output: %v", err)
	}
	waitForTestCondition(t, "simulation library import completion", func() bool {
		session := server.sessions.Project(realManifest.ID)
		return session != nil && !session.isActionRunning(projectActionKindSimulationImport)
	})

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+realManifest.ID+"/snapshot", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var snapshot projectSnapshotResponse
	if err := json.NewDecoder(rec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if snapshot.Simulation.ImportStatus != "done" {
		t.Fatalf("simulation import status = %q, want done", snapshot.Simulation.ImportStatus)
	}
	if snapshot.Simulation.ImportedFile == nil || snapshot.Simulation.ImportedFile.Name != "voice.json" {
		t.Fatalf("simulation imported file = %+v, want voice.json", snapshot.Simulation.ImportedFile)
	}
	if !strings.Contains(snapshot.Simulation.Message, "voice") {
		t.Fatalf("simulation message = %q, want profile name", snapshot.Simulation.Message)
	}
}

func TestSimulationLibraryProjectSaveArchivesAndLoadRestoresCorpus(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	sourceProject, err := server.store.CreateProject("Corpus Bundle Source")
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := projectSimulateDir(sourceProject)
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const sourceText = "repository-original synthetic simulation corpus"
	if err := os.WriteFile(filepath.Join(sourceDir, "voice.txt"), []byte(sourceText), 0o644); err != nil {
		t.Fatal(err)
	}
	profileData, err := json.Marshal(testWebSimulationProfile("voice.txt", "bundle-sha"))
	if err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Join(sourceProject.OutputDir, "meta")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "simulation_profile.json"), profileData, 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+sourceProject.ID+"/simulate/library/save",
		bytes.NewBufferString(`{"name":"Corpus Bundle"}`),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d body=%s", rec.Code, rec.Body.String())
	}
	var saveResponse struct {
		Item apiLibraryItem `json:"item"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&saveResponse); err != nil {
		t.Fatal(err)
	}
	if !saveResponse.Item.SourceArchived || saveResponse.Item.ArchivedSourceCount != 1 {
		t.Fatalf("saved bundle metadata = %+v", saveResponse.Item)
	}
	bundleSource := filepath.Join(
		runtimeRoot,
		simulationCorpusLibraryDirName,
		"Corpus Bundle",
		simulationLibraryBundleSourcesDir,
		"voice.txt",
	)
	if data, err := os.ReadFile(bundleSource); err != nil || string(data) != sourceText {
		t.Fatalf("archived source data=%q err=%v", data, err)
	}

	items, err := server.libraries.ListSimulationProfiles("Corpus Bundle")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].SourceArchived || items[0].ArchivedSourceCount != 1 {
		t.Fatalf("listed bundle metadata = %+v", items)
	}

	req = httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+sourceProject.ID+"/simulate/library/save",
		bytes.NewBufferString(`{"name":"Corpus Bundle","replace":true}`),
	)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace status = %d body=%s", rec.Code, rec.Body.String())
	}
	var replaceResponse struct {
		Replaced bool `json:"replaced"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&replaceResponse); err != nil {
		t.Fatal(err)
	}
	if !replaceResponse.Replaced {
		t.Fatal("replace response did not mark replaced=true")
	}

	targetProject, err := server.store.CreateProject("Corpus Bundle Target")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, targetProject)
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+targetProject.ID+"/simulate/library/load",
		bytes.NewBufferString(`{"name":"Corpus Bundle"}`),
	)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.importCalls != 1 {
		t.Fatalf("import calls = %d, want 1", fake.importCalls)
	}
	restored, err := os.ReadFile(filepath.Join(projectSimulateDir(targetProject), "voice.txt"))
	if err != nil || string(restored) != sourceText {
		t.Fatalf("restored source data=%q err=%v", restored, err)
	}
}

func TestProjectSimulationImportAlsoAddsLibraryEntry(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()
	manifest, err := server.store.CreateProject("Import Simulation Library")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	installFakeSession(t, server, manifest)

	profile := testWebSimulationProfile("imported.txt", "def456")
	profileData, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	req := newMultipartUploadRequest(t, http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/import", []testMultipartFile{
		{field: "profile", filename: "auto.json", body: string(profileData)},
	})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		LibrarySaved bool `json:"library_saved"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if !response.LibrarySaved {
		t.Fatalf("library_saved = false, want true")
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, simulationLibraryDirName, "auto.json")); err != nil {
		t.Fatalf("imported profile was not copied to library: %v", err)
	}
}

func TestNovelLibrarySaveLoadRewritesManifestAndSkipsAnalyze(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	sourceProject, err := server.store.CreateProject("Prepared Novel Source")
	if err != nil {
		t.Fatalf("CreateProject source: %v", err)
	}
	sourcePath := writePreparedAdaptationFixture(t, sourceProject, "source.txt")
	writeContaminatedCoCreateProgress(t, sourceProject.OutputDir)
	installFakeSession(t, server, sourceProject)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+sourceProject.ID+"/adapt/library/save", bytes.NewBufferString(`{"name":"Fixture Novel","source_file":"source.txt"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d body=%s", rec.Code, rec.Body.String())
	}
	requireLibraryEvent(t, server.sessions.Project(sourceProject.ID), "novel_save", "Fixture Novel")

	entryRoot := filepath.Join(runtimeRoot, novelLibraryDirName, "Fixture Novel")
	if _, err := os.Stat(filepath.Join(entryRoot, "source", novelLibrarySourceName)); err != nil {
		t.Fatalf("library source copy missing: %v", err)
	}
	libraryManifest := readAdaptationManifestForTest(t, filepath.Join(entryRoot, "meta", "adaptation", "source_manifest.json"))
	wantLibrarySource := filepath.Join(entryRoot, "source", novelLibrarySourceName)
	if filepath.Clean(libraryManifest.SourcePath) != filepath.Clean(wantLibrarySource) {
		t.Fatalf("library source_path = %q, want %q", libraryManifest.SourcePath, wantLibrarySource)
	}
	if filepath.Clean(libraryManifest.SourcePath) == filepath.Clean(sourcePath) {
		t.Fatalf("library source_path still points at original project source: %q", sourcePath)
	}
	assertPathMissing(t, filepath.Join(entryRoot, "meta", "adaptation", "cocreate_intent.json"))
	assertPathMissing(t, filepath.Join(entryRoot, "meta", "adaptation", "cocreate_briefing.json"))
	assertPathMissing(t, filepath.Join(entryRoot, "meta", "adaptation", "cocreate_briefing_batches"))
	assertPathMissing(t, filepath.Join(entryRoot, "meta", "adaptation", "proposal.json"))

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+sourceProject.ID+"/adapt/library/save", bytes.NewBufferString(`{"name":"Fixture Novel","source_file":"source.txt"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate save status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}

	writeContaminatedCoCreateProgress(t, entryRoot)
	targetProject, err := server.store.CreateProject("Loaded Novel Target")
	if err != nil {
		t.Fatalf("CreateProject target: %v", err)
	}
	fake := installFakeSession(t, server, targetProject)
	writeContaminatedWebCoCreateProgress(t, targetProject.OutputDir)
	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+targetProject.ID+"/adapt/library/load", bytes.NewBufferString(`{"name":"Fixture Novel"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", rec.Code, rec.Body.String())
	}
	requireLibraryEvent(t, server.sessions.Project(targetProject.ID), "novel_load", "Fixture Novel")
	var loadResponse struct {
		Analyzed   bool            `json:"analyzed"`
		SourceFile apiUploadedFile `json:"source_file"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&loadResponse); err != nil {
		t.Fatalf("decode load response: %v", err)
	}
	if !loadResponse.Analyzed {
		t.Fatalf("loaded novel should be marked analyzed")
	}
	if loadResponse.SourceFile.RelativePath != "Fixture Novel.txt" {
		t.Fatalf("source relative path = %q, want Fixture Novel.txt", loadResponse.SourceFile.RelativePath)
	}
	if fake.adaptAnalyzeCalls != 0 {
		t.Fatalf("library load should not call analyze, calls=%d", fake.adaptAnalyzeCalls)
	}

	projectSourcePath := filepath.Join(targetProject.RootDir, "uploads", "adaptation", "Fixture Novel.txt")
	projectManifest := readAdaptationManifestForTest(t, filepath.Join(targetProject.OutputDir, "meta", "adaptation", "source_manifest.json"))
	if filepath.Clean(projectManifest.SourcePath) != filepath.Clean(projectSourcePath) {
		t.Fatalf("project source_path = %q, want %q", projectManifest.SourcePath, projectSourcePath)
	}
	if _, _, err := adaptengine.ValidatePreparedSource(store.NewStore(targetProject.OutputDir), projectSourcePath); err != nil {
		t.Fatalf("loaded prepared source does not validate: %v", err)
	}
	targetAdaptationRoot := filepath.Join(targetProject.OutputDir, "meta", "adaptation")
	assertPathMissing(t, filepath.Join(targetAdaptationRoot, "cocreate_intent.json"))
	assertPathMissing(t, filepath.Join(targetAdaptationRoot, "cocreate_briefing.json"))
	assertPathMissing(t, filepath.Join(targetAdaptationRoot, "cocreate_briefing_batches"))
	assertPathMissing(t, filepath.Join(targetAdaptationRoot, "proposal.json"))
	assertPathMissing(t, filepath.Join(targetProject.OutputDir, filepath.FromSlash(webCoCreateCheckpointRelPath)))
	assertPathMissing(t, filepath.Join(targetProject.OutputDir, filepath.FromSlash(webCoCreateLogRelPath)))

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+targetProject.ID+"/adapt/start", bytes.NewBufferString(`{"source_file":"Fixture Novel.txt","mode":"chapter","brief":"adapt this source"}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.adaptAnalyzeCalls != 0 {
		t.Fatalf("start after library load should not call analyze, calls=%d", fake.adaptAnalyzeCalls)
	}
	if fake.adaptStartCalls != 1 {
		t.Fatalf("adapt start calls = %d, want 1", fake.adaptStartCalls)
	}
}

func TestNovelLibraryLoadLegacyEntryStartsDossierBackfill(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	sourceProject, err := server.store.CreateProject("Legacy Novel Source")
	if err != nil {
		t.Fatalf("CreateProject source: %v", err)
	}
	sourcePath := writePreparedAdaptationFixture(t, sourceProject, "source.txt")
	entryRoot := filepath.Join(runtimeRoot, novelLibraryDirName, "Legacy Novel")
	if err := os.MkdirAll(filepath.Join(entryRoot, "source"), 0o755); err != nil {
		t.Fatalf("create legacy source dir: %v", err)
	}
	if err := copyFileOverwrite(sourcePath, filepath.Join(entryRoot, "source", novelLibrarySourceName)); err != nil {
		t.Fatalf("copy legacy source: %v", err)
	}
	if err := copyDir(filepath.Join(sourceProject.OutputDir, "meta", "adaptation"), filepath.Join(entryRoot, "meta", "adaptation")); err != nil {
		t.Fatalf("copy legacy adaptation data: %v", err)
	}
	if err := os.Remove(filepath.Join(entryRoot, "meta", "adaptation", "cocreate_dossier.json")); err != nil {
		t.Fatalf("remove legacy dossier: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(entryRoot, "meta", "adaptation", "cocreate_dossier_batches")); err != nil {
		t.Fatalf("remove legacy dossier batches: %v", err)
	}
	now := time.Now().UTC()
	if err := writeJSONFile(filepath.Join(entryRoot, novelLibraryManifestName), novelLibraryManifest{
		Version:      1,
		Name:         "Legacy Novel",
		SourceFile:   "source.txt",
		ChapterCount: 2,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("write legacy library manifest: %v", err)
	}

	targetProject, err := server.store.CreateProject("Legacy Novel Target")
	if err != nil {
		t.Fatalf("CreateProject target: %v", err)
	}
	fake := installFakeSession(t, server, targetProject)
	started := make(chan struct{})
	fake.adaptAnalyzeStarted = started
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+targetProject.ID+"/adapt/library/load", bytes.NewBufferString(`{"name":"Legacy Novel"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("legacy library load did not start adaptation backfill")
	}
	if fake.adaptAnalyzeCalls != 1 {
		t.Fatalf("adapt analyze calls = %d, want 1", fake.adaptAnalyzeCalls)
	}

	var loadResponse struct {
		Analyzed   bool                 `json:"analyzed"`
		Running    bool                 `json:"running"`
		Accepted   bool                 `json:"accepted"`
		Adaptation apiAdaptationStatus  `json:"adaptation"`
		Events     []apiAdaptationEvent `json:"events"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&loadResponse); err != nil {
		t.Fatalf("decode load response: %v", err)
	}
	if loadResponse.Analyzed {
		t.Fatal("legacy library load should not be marked analyzed until dossier backfill finishes")
	}
	if !loadResponse.Running || !loadResponse.Accepted {
		t.Fatalf("legacy library load running=%v accepted=%v, want true/true", loadResponse.Running, loadResponse.Accepted)
	}
	if loadResponse.Adaptation.AnalysisStatus != "running" {
		t.Fatalf("analysis status = %q, want running", loadResponse.Adaptation.AnalysisStatus)
	}
	if len(loadResponse.Events) == 0 {
		t.Fatal("legacy library load should return running analysis event")
	}
}

func TestNovelLibraryLoadLegacyFoundationStartsIncrementalUpgrade(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	sourceProject, err := server.store.CreateProject("Legacy Foundation Source")
	if err != nil {
		t.Fatalf("CreateProject source: %v", err)
	}
	writePreparedAdaptationFixture(t, sourceProject, "source.txt")
	if _, err := server.libraries.SaveNovelFromProject(sourceProject, "Legacy Foundation", "source.txt"); err != nil {
		t.Fatalf("SaveNovelFromProject: %v", err)
	}
	libraryFoundationPath := filepath.Join(
		server.libraries.NovelDir(),
		"Legacy Foundation",
		"meta",
		"adaptation",
		"source_foundation.json",
	)
	var legacy domain.AdaptationSourceFoundation
	if err := readJSONFile(libraryFoundationPath, &legacy); err != nil {
		t.Fatalf("read library source foundation: %v", err)
	}
	legacy.Version = 0
	legacy.SourceChapterCount = 0
	legacy.SourceSignature = ""
	legacy.ReportSignature = ""
	legacy.PromptVersion = ""
	legacy.BatchRuneLimit = 0
	if err := writeJSONFile(libraryFoundationPath, legacy); err != nil {
		t.Fatalf("write legacy library source foundation: %v", err)
	}

	targetProject, err := server.store.CreateProject("Legacy Foundation Target")
	if err != nil {
		t.Fatalf("CreateProject target: %v", err)
	}
	fake := installFakeSession(t, server, targetProject)
	started := make(chan struct{})
	fake.adaptAnalyzeStarted = started
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+targetProject.ID+"/adapt/library/load",
		bytes.NewBufferString(`{"name":"Legacy Foundation"}`),
	)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("legacy source foundation did not start incremental upgrade")
	}
	if fake.adaptAnalyzeCalls != 1 {
		t.Fatalf("adapt analyze calls = %d, want 1", fake.adaptAnalyzeCalls)
	}
}

func TestNovelLibraryLoadLegacyEntrySyncsBackfillToLibrary(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	sourceProject, err := server.store.CreateProject("Legacy Sync Source")
	if err != nil {
		t.Fatalf("CreateProject source: %v", err)
	}
	sourcePath := writePreparedAdaptationFixture(t, sourceProject, "source.txt")
	entryRoot := filepath.Join(runtimeRoot, novelLibraryDirName, "Legacy Sync")
	if err := os.MkdirAll(filepath.Join(entryRoot, "source"), 0o755); err != nil {
		t.Fatalf("create legacy source dir: %v", err)
	}
	if err := copyFileOverwrite(sourcePath, filepath.Join(entryRoot, "source", novelLibrarySourceName)); err != nil {
		t.Fatalf("copy legacy source: %v", err)
	}
	if err := copyDir(filepath.Join(sourceProject.OutputDir, "meta", "adaptation"), filepath.Join(entryRoot, "meta", "adaptation")); err != nil {
		t.Fatalf("copy legacy adaptation data: %v", err)
	}
	if err := os.Remove(filepath.Join(entryRoot, "meta", "adaptation", "cocreate_dossier.json")); err != nil {
		t.Fatalf("remove legacy dossier: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(entryRoot, "meta", "adaptation", "cocreate_dossier_batches")); err != nil {
		t.Fatalf("remove legacy dossier batches: %v", err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	if err := writeJSONFile(filepath.Join(entryRoot, novelLibraryManifestName), novelLibraryManifest{
		Version:      1,
		Name:         "Legacy Sync",
		SourceFile:   "source.txt",
		ChapterCount: 2,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("write legacy library manifest: %v", err)
	}

	targetProject, err := server.store.CreateProject("Legacy Sync Target")
	if err != nil {
		t.Fatalf("CreateProject target: %v", err)
	}
	fake := installFakeSession(t, server, targetProject)
	fake.adaptAnalyzePrefixEvents = []adaptengine.Event{{
		Stage:   adaptengine.StageDossier,
		Message: "recovered dossier batch",
		Err:     errors.New("temporary dossier parse repair"),
	}}
	fake.adaptAnalyzeBeforeDone = func(string) {
		targetAdaptationRoot := filepath.Join(targetProject.OutputDir, "meta", "adaptation")
		if err := copyPreparedAdaptationFiles(sourceProject.OutputDir, targetAdaptationRoot); err != nil {
			t.Errorf("copy completed adaptation data: %v", err)
			return
		}
		projectSourcePath := filepath.Join(targetProject.RootDir, "uploads", "adaptation", "Legacy Sync.txt")
		if err := rewriteAdaptationManifestSource(targetProject.OutputDir, projectSourcePath); err != nil {
			t.Errorf("rewrite project adaptation source: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+targetProject.ID+"/adapt/library/load", bytes.NewBufferString(`{"name":"Legacy Sync"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load status = %d body=%s", rec.Code, rec.Body.String())
	}

	waitForTestCondition(t, "legacy library backfill sync", func() bool {
		if _, err := os.Stat(filepath.Join(entryRoot, "meta", "adaptation", "cocreate_dossier.json")); err != nil {
			return false
		}
		if _, err := os.Stat(filepath.Join(entryRoot, "meta", "adaptation", "cocreate_dossier_batches")); err != nil {
			return false
		}
		for _, ev := range server.sessions.Project(targetProject.ID).HistoryAfter(0) {
			if ev.Event != nil && ev.Event.Category == "LIBRARY" && ev.Event.Kind == "novel_sync" {
				return true
			}
		}
		return false
	})

	libraryManifest := readAdaptationManifestForTest(t, filepath.Join(entryRoot, "meta", "adaptation", "source_manifest.json"))
	wantLibrarySource := filepath.Join(entryRoot, "source", novelLibrarySourceName)
	if filepath.Clean(libraryManifest.SourcePath) != filepath.Clean(wantLibrarySource) {
		t.Fatalf("synced library source_path = %q, want %q", libraryManifest.SourcePath, wantLibrarySource)
	}
	updatedManifest, err := readNovelLibraryManifest(entryRoot)
	if err != nil {
		t.Fatalf("read synced library manifest: %v", err)
	}
	if !updatedManifest.UpdatedAt.After(now) {
		t.Fatalf("library updated_at was not refreshed: got %s, old %s", updatedManifest.UpdatedAt, now)
	}
}

func TestNovelLibrarySaveReplaceUpdatesExistingEntry(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	manifest, err := server.store.CreateProject("Replace Novel Source")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	writePreparedAdaptationFixture(t, manifest, "source.txt")
	installFakeSession(t, server, manifest)

	saveBody := `{"name":"Replace Novel","source_file":"source.txt"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/library/save", bytes.NewBufferString(saveBody))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initial save status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/library/save", bytes.NewBufferString(saveBody))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate save status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/adapt/library/save", bytes.NewBufferString(`{"name":"Replace Novel","source_file":"source.txt","replace":true}`))
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace save status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Replaced bool `json:"replaced"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode replace response: %v", err)
	}
	if !response.Replaced {
		t.Fatal("replace response did not mark replaced=true")
	}
}

func requireLibraryEvent(t *testing.T, session *ProjectSession, kind, name string) WebEvent {
	t.Helper()
	if session == nil {
		t.Fatalf("project session is nil")
	}
	for _, ev := range session.HistoryAfter(0) {
		if ev.Type != webEventTypeHostEvent || ev.Event == nil {
			continue
		}
		if ev.Event.Category == "LIBRARY" && ev.Event.Kind == kind && strings.Contains(ev.Event.Summary, name) {
			return ev
		}
	}
	t.Fatalf("library event kind=%q name=%q not found: %+v", kind, name, session.HistoryAfter(0))
	return WebEvent{}
}

func writePreparedAdaptationFixture(t *testing.T, manifest ProjectManifest, sourceName string) string {
	t.Helper()
	sourceText := "第一章 开端\nalpha source chapter one\n\n第二章 转折\nbeta source chapter two\n"
	sourcePath := filepath.Join(manifest.RootDir, "uploads", "adaptation", sourceName)
	if err := os.WriteFile(sourcePath, []byte(sourceText), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	st := store.NewStore(manifest.OutputDir)
	sourceOne, err := st.Adaptation.SaveSourceChapter(1, "开端", "alpha source chapter one")
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	sourceTwo, err := st.Adaptation.SaveSourceChapter(2, "转折", "beta source chapter two")
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	sourceManifest := domain.AdaptationSourceManifest{
		SourcePath:   sourcePath,
		ChapterCount: 2,
		Chapters:     []domain.AdaptationSource{sourceOne, sourceTwo},
	}
	if err := st.Adaptation.SaveSourceManifest(sourceManifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	reports := []domain.AdaptationSourceReport{
		{Chapter: 1, Title: "开端", SourceSHA256: sourceOne.SHA256, Summary: "source chapter one summary", KeyEvents: []string{"event one"}},
		{Chapter: 2, Title: "转折", SourceSHA256: sourceTwo.SHA256, Summary: "source chapter two summary", KeyEvents: []string{"event two"}},
	}
	for _, report := range reports {
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatalf("SaveSourceReport %d: %v", report.Chapter, err)
		}
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{
		Version:            1,
		SourcePath:         sourcePath,
		SourceChapterCount: sourceManifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(sourceManifest),
		ReportSignature:    "fixture-report-signature",
		PromptVersion:      "source-foundation-merge-v1:fixture",
		BatchRuneLimit:     70_000,
		Premise:            "source premise",
		Characters: []domain.Character{{
			Name:        "Ari",
			Role:        "lead",
			Description: "lead character",
			Arc:         "changes under pressure",
			Traits:      []string{"decisive"},
		}},
		WorldRules: []domain.WorldRule{{Category: "world", Rule: "rule", Boundary: "boundary"}},
		Volumes: []domain.VolumeOutline{{
			Index: 1,
			Title: "Volume One",
			Theme: "pressure",
			Arcs: []domain.ArcOutline{{
				Index: 1,
				Title: "Arc One",
				Goal:  "survive",
				Chapters: []domain.OutlineEntry{{
					Chapter:   1,
					Title:     "开端",
					CoreEvent: "event one",
					Hook:      "hook one",
					Scenes:    []string{"scene one"},
				}, {
					Chapter:   2,
					Title:     "转折",
					CoreEvent: "event two",
					Hook:      "hook two",
					Scenes:    []string{"scene two"},
				}},
			}},
		}},
	}); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	batch := domain.AdaptationCoCreateDossierBatch{
		Index:           1,
		SourceFrom:      1,
		SourceTo:        2,
		SourceSignature: store.AdaptationDossierBatchSpecs(sourceManifest, adaptengine.CoCreateDossierBatchSize, adaptengine.CoCreateDossierBatchRuneLimit)[0].SourceSignature,
		PromptVersion:   adaptengine.CoCreateDossierPromptVersion,
		PlotPhase:       "fixture source plot",
	}
	if err := st.Adaptation.SaveCoCreateDossierBatch(batch); err != nil {
		t.Fatalf("SaveCoCreateDossierBatch: %v", err)
	}
	if err := st.Adaptation.SaveCoCreateDossier(domain.AdaptationCoCreateDossier{
		Version:            1,
		PromptVersion:      adaptengine.CoCreateDossierPromptVersion,
		SourcePath:         sourcePath,
		SourceChapterCount: sourceManifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(sourceManifest),
		BatchSize:          adaptengine.CoCreateDossierBatchSize,
		BatchRuneLimit:     adaptengine.CoCreateDossierBatchRuneLimit,
		Batches:            []domain.AdaptationCoCreateDossierBatch{batch},
	}); err != nil {
		t.Fatalf("SaveCoCreateDossier: %v", err)
	}
	return sourcePath
}

func writeContaminatedCoCreateProgress(t *testing.T, root string) {
	t.Helper()
	adaptationRoot := filepath.Join(root, "meta", "adaptation")
	if err := writeJSONFile(filepath.Join(adaptationRoot, "cocreate_intent.json"), map[string]any{
		"raw_request": "old co-create request",
	}); err != nil {
		t.Fatalf("write contaminated intent: %v", err)
	}
	if err := writeJSONFile(filepath.Join(adaptationRoot, "cocreate_briefing.json"), map[string]any{
		"resolved_decisions": []string{"old answer"},
	}); err != nil {
		t.Fatalf("write contaminated briefing: %v", err)
	}
	if err := writeJSONFile(filepath.Join(adaptationRoot, "cocreate_briefing_batches", "0001.json"), map[string]any{
		"decision_questions": []string{"old question"},
	}); err != nil {
		t.Fatalf("write contaminated briefing batch: %v", err)
	}
	if err := writeJSONFile(filepath.Join(adaptationRoot, "proposal.json"), map[string]any{
		"brief": "old generated proposal",
	}); err != nil {
		t.Fatalf("write contaminated proposal: %v", err)
	}
}

func writeContaminatedWebCoCreateProgress(t *testing.T, outputDir string) {
	t.Helper()
	sessionRoot := filepath.Join(outputDir, "meta", "sessions")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("create contaminated session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, filepath.FromSlash(webCoCreateCheckpointRelPath)), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write contaminated co-create checkpoint: %v", err)
	}
	logLine := `{"input_history":[{"role":"user","content":"old co-create"}],"parsed_reply":"old","parsed_draft":"old"}` + "\n"
	if err := os.WriteFile(filepath.Join(outputDir, filepath.FromSlash(webCoCreateLogRelPath)), []byte(logLine), 0o644); err != nil {
		t.Fatalf("write contaminated co-create log: %v", err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("path %s should not exist", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func readAdaptationManifestForTest(t *testing.T, path string) domain.AdaptationSourceManifest {
	t.Helper()
	var manifest domain.AdaptationSourceManifest
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read adaptation manifest %s: %v", path, err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode adaptation manifest %s: %v", path, err)
	}
	return manifest
}
