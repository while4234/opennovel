package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
)

func TestFoundationGETReturnsReadableReadonlyState(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("Foundation API")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/foundation", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Foundation struct {
			Mode           string `json:"mode"`
			Editable       bool   `json:"editable"`
			ReadonlyReason string `json:"readonly_reason"`
		} `json:"foundation"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Foundation.Mode != "normal" || response.Foundation.Editable || response.Foundation.ReadonlyReason == "" {
		t.Fatalf("foundation state = %+v", response.Foundation)
	}
}

func TestFoundationApplyRejectsClientImpact(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("Foundation Strict Apply")
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"preview_id":"preview","idempotency_key":"key","impact":{"full_book":false}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/foundation/apply", body)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFoundationPreviewRejectsSourcePatchModeAndUnknownFields(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("Foundation Target Only")
	if err != nil {
		t.Fatal(err)
	}
	requests := map[string]string{
		"source":   `{"expected_base_revision":0,"expected_base_audit_signature":"audit","candidate":{},"source_foundation":{"premise":"attack"}}`,
		"patch":    `{"expected_base_revision":0,"expected_base_audit_signature":"audit","candidate":{},"patch":[{"op":"replace","path":"/source/premise","value":"attack"}]}`,
		"mode":     `{"expected_base_revision":0,"expected_base_audit_signature":"audit","candidate":{},"mode":"normal"}`,
		"unknown":  `{"expected_base_revision":0,"expected_base_audit_signature":"audit","candidate":{"premise":"target","source":"attack"}}`,
		"trailing": `{"expected_base_revision":0,"expected_base_audit_signature":"audit","candidate":{}} {"source":"attack"}`,
	}
	for name, body := range requests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/foundation/preview", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestFoundationRetryRejectsClientControlledSourceOrMode(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("Foundation Retry Target Only")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"idempotency_key":"retry","source_foundation":{"premise":"attack"}}`,
		`{"idempotency_key":"retry","mode":"normal"}`,
		`{"idempotency_key":"retry","patch":[]}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/foundation/retry", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

func TestCharacterWorkspaceHTTPRejectsSourceMutationAndUnknownFields(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	manifest, err := server.store.CreateProject("Character Workspace Strict JSON")
	if err != nil {
		t.Fatal(err)
	}
	installFakeSession(t, server, manifest)

	for name, body := range map[string]string{
		"source": `{
			"expected_base_revision":0,
			"expected_base_audit_signature":"audit",
			"idempotency_key":"analyze-source",
			"scope":{"character_ids":[]},
			"candidate_digest":"digest",
			"source_foundation":{"premise":"attack"}
		}`,
		"unknown": `{
			"expected_base_revision":0,
			"expected_base_audit_signature":"audit",
			"idempotency_key":"analyze-unknown",
			"scope":{"character_ids":[]},
			"candidate_digest":"digest",
			"unexpected":true
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/projects/"+manifest.ID+"/foundation/characters/analyze",
				bytes.NewBufferString(body),
			)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
