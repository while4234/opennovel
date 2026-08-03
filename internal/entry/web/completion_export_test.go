package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/adaptaudit"
)

func TestDecodeProjectExportRequestDefaultsToPreview(t *testing.T) {
	req := httptest.NewRequest("POST", "/export", strings.NewReader(`{"path":"book.txt","format":"txt"}`))
	decoded, err := decodeProjectExportRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Purpose != "preview" {
		t.Fatalf("purpose=%q, want preview", decoded.Purpose)
	}
}

func TestPublishExportRoutesRejectConcurrentProjectAction(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Locked Publish")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	session := server.sessions.Project(manifest.ID)
	unlock, err := session.beginAction()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	for _, route := range []string{"export", "export/download"} {
		t.Run(route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/"+route, bytes.NewBufferString(`{"format":"txt","purpose":"publish"}`))
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, req)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if fake.exportCalls != 0 {
		t.Fatalf("concurrent publish must not write export, calls=%d", fake.exportCalls)
	}
}

func TestWritePublishExportErrorIncludesExactAuditConfirmation(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePublishExportError(recorder, errPublishTest("blocked"), &adaptaudit.Report{Status: "fail", Digest: "report-digest"})
	if recorder.Code != 409 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Code         string `json:"code"`
		ForceAllowed bool   `json:"force_allowed"`
		Report       struct {
			Digest string `json:"digest"`
			Status string `json:"status"`
		} `json:"report"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "completion_audit_blocked" || !body.ForceAllowed || body.Report.Digest != "report-digest" || body.Report.Status != "fail" {
		t.Fatalf("body=%+v", body)
	}
}

type errPublishTest string

func (e errPublishTest) Error() string { return string(e) }
