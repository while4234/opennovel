package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
)

func TestCompletionAuditErrorsUseTypedPrivateEnvelope(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/api/projects/missing-project/completion/audit", nil))
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"not_found"`) || strings.Contains(missing.Body.String(), server.store.RuntimeRoot) {
		t.Fatalf("missing completion audit envelope status=%d body=%s", missing.Code, missing.Body.String())
	}

	project, err := server.store.CreateProject("Private Completion Project")
	if err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(project.OutputDir, "meta", "adaptation", "audits", "latest.json")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auditPath, []byte("private prose and invalid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	load := httptest.NewRecorder()
	server.Handler().ServeHTTP(load, httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/completion/audit", nil))
	if load.Code != http.StatusInternalServerError || !strings.Contains(load.Body.String(), `"code":"completion_audit_load_failed"`) || strings.Contains(load.Body.String(), "private prose") || strings.Contains(load.Body.String(), auditPath) {
		t.Fatalf("completion load envelope status=%d body=%s", load.Code, load.Body.String())
	}

	method := httptest.NewRecorder()
	server.Handler().ServeHTTP(method, httptest.NewRequest(http.MethodDelete, "/api/projects/"+project.ID+"/completion/audit", nil))
	if method.Code != http.StatusMethodNotAllowed || !strings.Contains(method.Body.String(), `"code":"method_not_allowed"`) {
		t.Fatalf("method envelope status=%d body=%s", method.Code, method.Body.String())
	}
}
