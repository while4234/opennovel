package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
)

type fakeSimulationSourceDownloader struct {
	searchQuery string
	searchLimit int
	results     []simulationSearchResult
	downloaded  downloadedSimulationSource
}

func (f *fakeSimulationSourceDownloader) Search(_ context.Context, query string, limit int) ([]simulationSearchResult, error) {
	f.searchQuery = query
	f.searchLimit = limit
	return f.results, nil
}

func (f *fakeSimulationSourceDownloader) Download(_ context.Context, _ string, _ string) (downloadedSimulationSource, error) {
	return f.downloaded, nil
}

func TestProjectSimulationSearchUsesRealNameAndReturnsFirstFiveTxt(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("search corpus")
	if err != nil {
		t.Fatal(err)
	}
	downloader := &fakeSimulationSourceDownloader{}
	for i := 0; i < 7; i++ {
		downloader.results = append(downloader.results, simulationSearchResult{ID: string(rune('a' + i)), Name: "result.txt", FileType: "txt"})
	}
	downloader.results = append([]simulationSearchResult{{ID: "zip", Name: "archive.zip", FileType: "zip"}}, downloader.results...)
	server.sourceDownloader = downloader

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/search", strings.NewReader(`{"file_name":"测试小说"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if downloader.searchQuery != "测试小说" || downloader.searchLimit != simulationSearchResultLimit {
		t.Fatalf("search=%q/%d", downloader.searchQuery, downloader.searchLimit)
	}
	var response struct {
		Results []simulationSearchResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 5 {
		t.Fatalf("results=%d, want 5", len(response.Results))
	}
}

func TestProjectSimulationSearchDownloadIngestsAndSplitsTxt(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("download corpus")
	if err != nil {
		t.Fatal(err)
	}
	downloadPath := filepath.Join(testTempDir(t), "long.txt")
	content := simulationTestChapter(1, "one", strings.Repeat("一", 5000)) + simulationTestChapter(2, "two", strings.Repeat("二", 5000))
	if err := os.WriteFile(downloadPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	server.sourceDownloader = &fakeSimulationSourceDownloader{downloaded: downloadedSimulationSource{Name: "long.txt", Path: downloadPath, Size: int64(len([]byte(content)))}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/search/download", strings.NewReader(`{"result_id":"result-1"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	files, err := projectSimulationSourceFiles(projectSimulateDir(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files=%+v, want 2 split parts", files)
	}
}

func TestProjectSimulationSearchDownloadRejectsNonTxt(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("reject corpus")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(testTempDir(t), "archive.zip")
	if err := os.WriteFile(path, []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	server.sourceDownloader = &fakeSimulationSourceDownloader{downloaded: downloadedSimulationSource{Name: "archive.zip", Path: path, Size: 3}}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/simulate/search/download", strings.NewReader(`{"result_id":"result-2"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "请选择 TXT 文件") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSmartResultMatchesRequestedTxt(t *testing.T) {
	contextText := "异兽迷城.txt 全集下载 提取码：a1B2"
	if !smartResultMatchesTXT("《异兽迷城》", contextText) {
		t.Fatal("expected matching TXT result")
	}
	if smartResultMatchesTXT("别的小说", contextText) {
		t.Fatal("must reject a TXT result for another title")
	}
	if smartResultMatchesTXT("异兽迷城", "异兽迷城 EPUB 下载") {
		t.Fatal("must reject a non-TXT result")
	}
	if got := extractBaiduSharePassword(contextText); got != "a1B2" {
		t.Fatalf("password=%q", got)
	}
}

func TestSimulationTitleMatchesRejectsLooselyRelatedBooks(t *testing.T) {
	query := "重生后，我把疯批老公撩到失控"
	if !simulationTitleMatches(query, "重生后，我把疯批老公撩到失控（超甜！偏执大佬的小青梅重生了）.txt") {
		t.Fatal("expected the alias suffix to remain a match")
	}
	if simulationTitleMatches(query, "重生后，撩翻了疯批摄政王.txt") {
		t.Fatal("must reject a loosely related title")
	}
}
