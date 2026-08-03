package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestProjectAdaptAuditHistoryListGetAndCompare(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Audit History")
	if err != nil {
		t.Fatal(err)
	}
	st := storepkg.NewStore(manifest.OutputDir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	report := adaptaudit.Audit(adaptaudit.Input{Mode: adaptaudit.ModeFree, Scope: adaptaudit.Scope{TargetFrom: 1, TargetTo: 1}})
	first, _ := adaptaudit.NewAuditRun(report, adaptaudit.AuditKindContract, adaptaudit.AuditTriggerManual, nil, time.Unix(1, 0))
	second, _ := adaptaudit.NewAuditRun(report, adaptaudit.AuditKindModelSecondPass, adaptaudit.AuditTriggerManual, &adaptaudit.ModelSnapshot{Model: "strong"}, time.Unix(2, 0))
	if err := st.Adaptation.SaveAuditRun(first); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveAuditRun(second); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/projects/" + manifest.ID + "/adapt/audits",
		"/api/projects/" + manifest.ID + "/adapt/audits/" + first.RunID,
		"/api/projects/" + manifest.ID + "/adapt/audits/compare?base_run_id=" + first.RunID + "&candidate_run_id=" + second.RunID,
	} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
}
