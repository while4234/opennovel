package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestProjectAdaptAuditGetReturnsSavedReadOnlyReport(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Adapt Audit")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeFree})
	if err := st.Adaptation.SaveAuditReport(report); err != nil {
		t.Fatalf("SaveAuditReport: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		ChapterCount: 2,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Title: "第一章", Path: "private/1.txt", SHA256: "secret-hash", Runes: 100},
			{Chapter: 2, Title: "第二章", Path: "private/2.txt", SHA256: "secret-hash-2", Runes: 200},
		},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityFree,
		Status:      domain.AdaptationPlanStatusConfirmed,
		Chapters:    []domain.AdaptationChapterPlan{{Chapter: 1}},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName: "audit", Phase: domain.PhaseWriting, Flow: domain.FlowWriting,
		TotalChapters: 1, CurrentChapter: 2, CompletedChapters: []int{1},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/adapt/audit", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Report         *adaptaudit.Report      `json:"report"`
		SourceChapters []apiAuditSourceChapter `json:"source_chapters"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if response.Report == nil || response.Report.Digest != report.Digest || !response.Report.ReadOnly {
		t.Fatalf("report=%+v", response.Report)
	}
	if len(response.SourceChapters) != 2 || response.SourceChapters[0].Title != "第一章" || response.SourceChapters[1].Chapter != 2 {
		t.Fatalf("source chapters=%+v", response.SourceChapters)
	}
	if body := rec.Body.String(); strings.Contains(body, "private/1.txt") || strings.Contains(body, "secret-hash") || strings.Contains(body, "runes") {
		t.Fatalf("audit source chapter response exposed private manifest metadata: %s", body)
	}
}
