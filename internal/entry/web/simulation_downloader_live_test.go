package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveSimulationSourceSearch(t *testing.T) {
	if os.Getenv("AINOVEL_LIVE_DALIPAN") != "1" {
		t.Skip("set AINOVEL_LIVE_DALIPAN=1 to run authenticated DaliPan search")
	}
	runtimeRoot := t.TempDir()
	downloader := newSimulationSourceDownloader(runtimeRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, testCase := range []struct {
		title     string
		wantFound bool
	}{
		{title: "甜欲！被病娇影帝撩哄得脸红心跳", wantFound: true},
		{title: "重生后，我把疯批老公撩到失控", wantFound: true},
		{title: "豪门甜宠：傅先生的团宠小娇妻", wantFound: true},
		{title: "异兽迷城", wantFound: true},
	} {
		results, err := downloader.Search(ctx, testCase.title, simulationSearchResultLimit)
		if err != nil {
			t.Fatalf("search %q: %v", testCase.title, err)
		}
		if testCase.wantFound && len(results) == 0 {
			t.Fatalf("search %q returned no TXT results", testCase.title)
		}
		if !testCase.wantFound && len(results) != 0 {
			t.Fatalf("search %q unexpectedly returned results: %+v", testCase.title, results)
		}
		for _, result := range results {
			if result.FileType != "txt" {
				t.Fatalf("search %q returned non-TXT result: %+v", testCase.title, result)
			}
		}
	}
}

func TestLiveSimulationSourceDownload(t *testing.T) {
	if os.Getenv("AINOVEL_LIVE_DOWNLOAD") != "1" {
		t.Skip("set AINOVEL_LIVE_DOWNLOAD=1 to run authenticated BaiduPCS-Go download")
	}
	runtimeRoot := t.TempDir()
	if configured := os.Getenv("AINOVEL_LIVE_BAIDUPCS_RUNTIME"); configured != "" {
		runtimeRoot = configured
	}
	downloader := newSimulationSourceDownloader(runtimeRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	title := strings.TrimSpace(os.Getenv("AINOVEL_LIVE_DOWNLOAD_TITLE"))
	if title == "" {
		title = "甜欲！被病娇影帝撩哄得脸红心跳"
	}
	results, err := downloader.Search(ctx, title, simulationSearchResultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no TXT search result")
	}
	downloaded, err := downloader.Download(ctx, results[0].ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if downloaded.Size == 0 || filepath.Ext(downloaded.Path) != ".txt" {
		t.Fatalf("invalid download: %+v", downloaded)
	}
}

func TestLiveBaiduSmartSearchFallback(t *testing.T) {
	if os.Getenv("AINOVEL_LIVE_BAIDU_SMART") != "1" {
		t.Skip("set AINOVEL_LIVE_BAIDU_SMART=1 to run the background Baidu smart-agent fallback")
	}
	credentials, err := loadSimulationDownloadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	downloader := &dalipanBaiduDownloader{
		runtimeRoot: t.TempDir(),
		credentials: credentials,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	title := strings.TrimSpace(os.Getenv("AINOVEL_LIVE_BAIDU_SMART_TITLE"))
	if title == "" {
		title = "异兽迷城"
	}
	result, err := downloader.searchBaiduSmartApp(ctx, title)
	if err != nil {
		t.Logf("smart result context: %q", result.Context)
		t.Fatal(err)
	}
	if result.ShareURL == "" || !smartResultMatchesTXT(title, result.Context) {
		t.Fatal("background fallback did not return the matching TXT link")
	}
}
