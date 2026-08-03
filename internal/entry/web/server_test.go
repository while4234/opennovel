package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestStaticFSIncludesBuiltWebDist(t *testing.T) {
	static := StaticFS()
	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatalf("embedded index.html missing; run npm --prefix web run build: %v", err)
	}
	if !bytes.Contains(index, []byte(`<div id="root"`)) {
		t.Fatalf("embedded index.html does not look like the Web UI shell: %s", string(index))
	}
	assets, err := fs.ReadDir(static, "assets")
	if err != nil {
		t.Fatalf("embedded assets directory missing: %v", err)
	}
	if len(assets) == 0 {
		t.Fatal("embedded assets directory is empty")
	}
}

func TestStaticIndexDisablesBrowserCache(t *testing.T) {
	handler := NewHandler(bootstrap.Config{}, assets.Bundle{}, filepath.Join(testTempDir(t), "runtime"))

	for _, path := range []string{"/", "/workspace/route"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
		cacheControl := rec.Header().Get("cache-control")
		if !strings.Contains(cacheControl, "no-store") || !strings.Contains(cacheControl, "max-age=0") {
			t.Fatalf("%s cache-control = %q, want no-store and max-age=0", path, cacheControl)
		}
		if rec.Header().Get("pragma") != "no-cache" {
			t.Fatalf("%s pragma = %q, want no-cache", path, rec.Header().Get("pragma"))
		}
		if rec.Header().Get("expires") != "0" {
			t.Fatalf("%s expires = %q, want 0", path, rec.Header().Get("expires"))
		}
	}
}

func TestStaticAssetsCanUseImmutableCache(t *testing.T) {
	static := StaticFS()
	entries, err := fs.ReadDir(static, "assets")
	if err != nil {
		t.Fatalf("read embedded assets: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded assets directory is empty")
	}
	handler := NewHandler(bootstrap.Config{}, assets.Bundle{}, filepath.Join(testTempDir(t), "runtime"))

	req := httptest.NewRequest(http.MethodGet, "/assets/"+entries[0].Name(), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d body=%s", rec.Code, rec.Body.String())
	}
	cacheControl := rec.Header().Get("cache-control")
	if !strings.Contains(cacheControl, "immutable") || !strings.Contains(cacheControl, "max-age=31536000") {
		t.Fatalf("asset cache-control = %q, want immutable hashed asset cache", cacheControl)
	}
}

func TestProjectSnapshotSummaryKeepsNavigationAndDropsHeavyArtifacts(t *testing.T) {
	long := strings.Repeat("内容", projectSnapshotSummaryRunes+20)
	snapshot := WebSnapshot{UISnapshot: host.UISnapshot{
		Premise:                "保留给项目卡片的简介",
		PremiseFull:            long,
		CharacterDetails:       []domain.Character{{Name: "角色"}},
		WorldRules:             []domain.WorldRule{{Rule: "规则"}},
		SimulationProfile:      &domain.SimulationCompactProfile{},
		AdaptationVolumeReview: &domain.AdaptationVolumeReview{},
		AdaptationProposal:     &domain.AdaptationPlan{},
		AdaptationPlan:         &domain.AdaptationPlan{},
		Outline: []host.OutlineSnapshot{{
			Chapter:         55,
			Title:           long,
			CoreEvent:       long,
			Hook:            long,
			Scenes:          []string{long, long, long},
			PreserveEvents:  []string{long, long, long},
			RequiredChanges: []string{long, long, long},
			ForbiddenMoves:  []string{long, long, long},
			CoverageNote:    long,
			SourceCoverage: &host.SourceCoverageSnapshot{
				Chapters: []int{11, 12},
				From:     11,
				To:       12,
				Runes:    2000,
				Note:     long,
			},
		}},
	}}

	compact := compactProjectSnapshot(snapshot)
	if compact.Premise != snapshot.Premise || compact.PremiseFull != "" {
		t.Fatalf("premise summary was not preserved/trimmed: %+v", compact.UISnapshot)
	}
	if compact.CharacterDetails != nil || compact.WorldRules != nil || compact.SimulationProfile != nil ||
		compact.AdaptationVolumeReview != nil || compact.AdaptationProposal != nil || compact.AdaptationPlan != nil {
		t.Fatalf("summary snapshot retained a heavyweight artifact: %+v", compact.UISnapshot)
	}
	if len(compact.Outline) != 1 || compact.Outline[0].Chapter != 55 {
		t.Fatalf("outline navigation was lost: %+v", compact.Outline)
	}
	row := compact.Outline[0]
	if len([]rune(row.Title)) != projectSnapshotSummaryRunes+1 || !strings.HasSuffix(row.Title, "…") {
		t.Fatalf("title was not rune-truncated: %q", row.Title)
	}
	for label, values := range map[string][]string{
		"scenes":           row.Scenes,
		"preserve_events":  row.PreserveEvents,
		"required_changes": row.RequiredChanges,
		"forbidden_moves":  row.ForbiddenMoves,
	} {
		if len(values) != 0 {
			t.Fatalf("%s should not be included in a project-opening summary: %+v", label, values)
		}
	}
	if row.CoreEvent != "" || row.Hook != "" || row.CoverageNote != "" {
		t.Fatalf("summary retained detailed prose: %+v", row)
	}
	if row.SourceCoverage == nil || row.SourceCoverage.From != 11 || row.SourceCoverage.To != 12 ||
		row.SourceCoverage.Runes != 2000 || row.SourceCoverage.IsAdded || len(row.SourceCoverage.Chapters) != 0 || row.SourceCoverage.Note != "" {
		t.Fatalf("summary coverage should retain only range metadata: %+v", row.SourceCoverage)
	}

	history := WebEventHistory{LatestSeq: 500, Events: make([]WebEvent, projectSnapshotSummaryEvents+3)}
	for index := range history.Events {
		history.Events[index].Seq = int64(index + 1)
	}
	compactHistory := compactProjectSnapshotHistory(history)
	if len(compactHistory.Events) != projectSnapshotSummaryEvents || compactHistory.OldestSeq != 4 || compactHistory.Events[0].Seq != 4 {
		t.Fatalf("history was not reduced to its newest events: %+v", compactHistory)
	}
}

func TestProjectSnapshotDetailLevelDefaultsToFull(t *testing.T) {
	for rawURL, want := range map[string]string{
		"/api/projects/demo/snapshot":                projectSnapshotDetailFull,
		"/api/projects/demo/snapshot?detail=summary": projectSnapshotDetailSummary,
		"/api/projects/demo/snapshot?detail=FULL":    projectSnapshotDetailFull,
	} {
		if got := projectSnapshotDetailLevel(httptest.NewRequest(http.MethodGet, rawURL, nil)); got != want {
			t.Fatalf("%s detail=%q, want %q", rawURL, got, want)
		}
	}
}

func TestWebPipelineSmokeCoversStartupIsolationSnapshotAndSSE(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := httpServer.Client()
	client.Timeout = 3 * time.Second

	createResp, err := client.Post(httpServer.URL+"/api/projects", "application/json", bytes.NewBufferString(`{"name":"Smoke Novel"}`))
	if err != nil {
		t.Fatalf("create project request: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", createResp.StatusCode)
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(createResp.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode project manifest: %v", err)
	}
	if !isSameOrChild(runtimeRoot, manifest.RootDir) {
		t.Fatalf("project root %q is not under runtime root %q", manifest.RootDir, runtimeRoot)
	}

	uploadReq := newClientMultipartUploadRequest(t, httpServer.URL+"/api/projects/"+manifest.ID+"/simulate/files", []testMultipartFile{
		{field: "files", filename: "style-sample.txt", body: "voice and pacing sample"},
	})
	uploadResp, err := client.Do(uploadReq)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", uploadResp.StatusCode)
	}
	uploadedPath := filepath.Join(manifest.RootDir, "simulate", "style-sample.txt")
	if _, err := os.Stat(uploadedPath); err != nil {
		t.Fatalf("uploaded source should stay in project simulate dir: %v", err)
	}

	snapshotResp, err := client.Get(httpServer.URL + "/api/projects/" + manifest.ID + "/snapshot")
	if err != nil {
		t.Fatalf("snapshot request: %v", err)
	}
	defer snapshotResp.Body.Close()
	if snapshotResp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d", snapshotResp.StatusCode)
	}
	var snapshotBody struct {
		Project  ProjectManifest `json:"project"`
		Snapshot struct {
			WorkflowProgress WorkflowProgress `json:"workflow_progress"`
		} `json:"snapshot"`
		Events         []WebEvent `json:"events"`
		LatestEventSeq int64      `json:"latest_event_seq"`
	}
	if err := json.NewDecoder(snapshotResp.Body).Decode(&snapshotBody); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if snapshotBody.Project.ID != manifest.ID || snapshotBody.Snapshot.WorkflowProgress.Workflow == "" {
		t.Fatalf("snapshot response missing project or snapshot: %+v", snapshotBody)
	}
	for _, ev := range snapshotBody.Events {
		if !isWorkbenchReplayEvent(ev) {
			t.Fatalf("snapshot response included state replay event: %+v", ev)
		}
	}
	session, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatalf("open production project session: %v", err)
	}
	chapterID := "ch_0123456789abcdef0123456789abcdef"
	session.appendManuscriptMutation("prose_publish", chapterID)

	eventsResp, err := client.Get(httpServer.URL + "/api/projects/" + manifest.ID + "/events?after=0")
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer eventsResp.Body.Close()
	if eventsResp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", eventsResp.StatusCode)
	}
	if ctype := eventsResp.Header.Get("content-type"); !strings.HasPrefix(ctype, "text/event-stream") {
		t.Fatalf("events content-type = %q", ctype)
	}
	reader := bufio.NewReader(eventsResp.Body)
	var sawID, sawSnapshotEvent, sawSnapshotData, sawMutationData bool
	for i := 0; i < 30 && !(sawID && sawSnapshotEvent && sawSnapshotData && sawMutationData); i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE line: %v", err)
		}
		line = strings.TrimSpace(line)
		sawID = sawID || strings.HasPrefix(line, "id:")
		sawSnapshotEvent = sawSnapshotEvent || line == "event: snapshot"
		sawSnapshotData = sawSnapshotData || (strings.HasPrefix(line, "data:") && strings.Contains(line, `"type":"snapshot"`))
		sawMutationData = sawMutationData || (strings.HasPrefix(line, "data:") && strings.Contains(line, `"manuscript_mutation":{"scope":"prose_publish","stable_id":"`+chapterID+`"}`))
	}
	if !sawID || !sawSnapshotEvent || !sawSnapshotData || !sawMutationData {
		t.Fatalf("SSE stream missing production envelope: id=%v snapshot_event=%v snapshot_data=%v mutation=%v", sawID, sawSnapshotEvent, sawSnapshotData, sawMutationData)
	}
	eventsResp.Body.Close()

	historyResp, err := client.Get(httpServer.URL + "/api/projects/" + manifest.ID + "/events/history?after=0")
	if err != nil {
		t.Fatalf("event history request: %v", err)
	}
	defer historyResp.Body.Close()
	if historyResp.StatusCode != http.StatusOK {
		t.Fatalf("event history status = %d", historyResp.StatusCode)
	}
	if ctype := historyResp.Header.Get("content-type"); !strings.HasPrefix(ctype, "application/json") {
		t.Fatalf("event history content-type = %q", ctype)
	}
	var historyBody WebEventHistory
	if err := json.NewDecoder(historyResp.Body).Decode(&historyBody); err != nil {
		t.Fatalf("decode event history response: %v", err)
	}
	if historyBody.ProjectID != manifest.ID {
		t.Fatalf("event history project id = %q, want %q", historyBody.ProjectID, manifest.ID)
	}
	if historyBody.LatestSeq == 0 || historyBody.HistoryLimit == 0 || len(historyBody.Events) == 0 {
		t.Fatalf("event history missing replay metadata/events: %+v", historyBody)
	}
	if historyBody.Events[len(historyBody.Events)-1].Type != webEventTypeSnapshot {
		t.Fatalf("event history should include the replayed snapshot from SSE setup: %+v", historyBody.Events)
	}
	foundMutation := false
	for _, event := range historyBody.Events {
		if event.ManuscriptMutation != nil && event.ManuscriptMutation.Scope == "prose_publish" && event.ManuscriptMutation.StableID == chapterID {
			foundMutation = true
		}
	}
	if !foundMutation {
		t.Fatalf("event history omitted manuscript mutation projection: %+v", historyBody.Events)
	}
}

func TestHandlerCreatesProjectsUnderRuntimeRoot(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	handler := NewHandler(bootstrap.Config{}, assets.Bundle{}, runtimeRoot)

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Web Novel"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(manifest.RootDir), filepath.Clean(runtimeRoot)) {
		t.Fatalf("project root %q should be under runtime root %q", manifest.RootDir, runtimeRoot)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), manifest.ID) {
		t.Fatalf("project list should include %q: %s", manifest.ID, rec.Body.String())
	}
}

func TestModelConfigResponseRedactsProviderSecrets(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	handler := NewHandler(testWebConfig(t), assets.Load("default"), runtimeRoot)

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Secret Safety"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/models", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-test") || strings.Contains(body, "api_key") {
		t.Fatalf("model config response exposed provider secret: %s", body)
	}
	if !strings.Contains(body, `"providers"`) || !strings.Contains(body, `"roles"`) {
		t.Fatalf("model config response missing expected sections: %s", body)
	}
}

func TestBackendManualTestEndpointIsNoTokenCall(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	handler := NewHandler(testWebConfig(t), assets.Load("default"), runtimeRoot)

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Backend Test"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/backend/test", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("backend test status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Backend apiBackendStatus `json:"backend"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode backend test: %v", err)
	}
	if body.Backend.ManualTest == nil {
		t.Fatalf("manual_test missing from backend response: %+v", body.Backend)
	}
	if !body.Backend.ManualTest.NoTokenCall {
		t.Fatalf("manual_test.no_token_call = false, want true: %+v", body.Backend.ManualTest)
	}
}

func TestAPIUsageFromSnapshotProjectsCacheFields(t *testing.T) {
	summary := apiUsageFromSnapshot(host.UISnapshot{
		TotalInputTokens:       100,
		TotalOutputTokens:      40,
		TotalCacheReadTokens:   70,
		TotalCacheWriteTokens:  15,
		TotalCostUSD:           0.0123,
		TotalSavedUSD:          0.0456,
		OverallCacheCapable:    true,
		OverallRecentCacheRead: 30,
		OverallRecentInput:     50,
		OverallRecentSamples:   2,
		MissingAssistantUsage:  1,
		CachePerAgent: []host.AgentCacheStat{{
			Role:      "writer",
			Input:     100,
			CacheRead: 70,
		}},
		CachePerModel: []host.AgentCacheStat{{
			Model:     "gpt-test",
			Input:     100,
			CacheRead: 70,
		}},
	})
	if summary.Overall.InputTokens != 100 || summary.Overall.CacheReadTokens != 70 || !summary.Overall.CacheCapable {
		t.Fatalf("overall usage projection lost cache fields: %+v", summary.Overall)
	}
	if len(summary.ByRole) != 1 || summary.ByRole[0].Role != "writer" {
		t.Fatalf("by-role usage projection = %+v", summary.ByRole)
	}
	if len(summary.ByModel) != 1 || summary.ByModel[0].Model != "gpt-test" {
		t.Fatalf("by-model usage projection = %+v", summary.ByModel)
	}
	if summary.MissingAssistantUsage != 1 {
		t.Fatalf("missing usage = %d, want 1", summary.MissingAssistantUsage)
	}
}

func newClientMultipartUploadRequest(t *testing.T, url string, files []testMultipartFile) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		field := file.field
		if field == "" {
			field = "files"
		}
		part, err := writer.CreateFormFile(field, file.filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write([]byte(file.body)); err != nil {
			t.Fatalf("write multipart part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
