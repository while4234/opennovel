package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestStylesEndpointReturnsMarkdownHeadingLabels(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/styles", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("styles status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body apiStylesResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode styles: %v", err)
	}
	labels := make(map[string]string, len(body.Styles))
	for _, item := range body.Styles {
		labels[item.ID] = item.Label
	}
	if labels["fantasy"] != "奇幻冒险风格" || labels["default"] != "通用写作风格" {
		t.Fatalf("style labels = %+v", labels)
	}
}

func TestStylesEndpointRefreshDiscoversRuntimeMarkdown(t *testing.T) {
	stylesDir := t.TempDir()
	server := newServer(
		testWebConfig(t),
		assets.Load("default"),
		filepath.Join(testTempDir(t), "runtime"),
		assets.NewStyleSource(stylesDir),
	)
	defer server.Close()

	requestCatalog := func() apiStylesResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/styles", nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("styles status = %d body=%s", rec.Code, rec.Body.String())
		}
		var body apiStylesResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode styles: %v", err)
		}
		return body
	}
	hasStyle := func(catalog apiStylesResponse, id string) bool {
		for _, item := range catalog.Styles {
			if item.ID == id {
				return true
			}
		}
		return false
	}

	if hasStyle(requestCatalog(), "runtime-style") {
		t.Fatal("runtime style should not exist before refresh input is created")
	}
	if err := os.WriteFile(
		filepath.Join(stylesDir, "runtime-style.md"),
		[]byte("## Runtime Style\nruntime body"),
		0o644,
	); err != nil {
		t.Fatalf("write runtime style: %v", err)
	}
	if !hasStyle(requestCatalog(), "runtime-style") {
		t.Fatal("refreshed catalog should discover runtime style")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Runtime Style Project","style":"runtime-style"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create runtime style project status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateProjectWithStyleWritesProjectOverlay(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Styled Novel","style":"fantasy"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "fantasy" {
		t.Fatalf("project style = %q, want fantasy", overlay.Style)
	}
}

func TestCreateProjectUsesGrokXHighForEveryStageByDefault(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.Providers = map[string]bootstrap.ProviderConfig{
		"deepseek-backend": {Type: "openai", APIKey: "sk-test", Models: []string{"deepseek-v4-pro"}},
		"grok-backend":     {Type: "openai", APIKey: "sk-test", Models: []string{"grok-4.5"}},
	}
	cfg.Provider = "deepseek-backend"
	cfg.ModelName = "deepseek-v4-pro"
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Recommended Models"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	overlay := readProjectOverlay(t, manifest)
	for _, stage := range bootstrap.KnownModelStages {
		route := overlay.Roles[bootstrap.StageRouteKey(stage)]
		if route.Provider != "grok-backend" || route.Model != "grok-4.5" || route.ReasoningEffort != "xhigh" {
			t.Fatalf("%s route = %+v, want grok-backend/grok-4.5@xhigh", stage, route)
		}
	}
}

func TestCreateProjectStageDefaultsFallBackToAvailableRecommendedModel(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.Providers = map[string]bootstrap.ProviderConfig{
		"grok-backend": {Type: "openai", APIKey: "sk-test", Models: []string{"grok-4.5"}},
	}
	cfg.Provider = "grok-backend"
	cfg.ModelName = "grok-4.5"
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Grok Only"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	overlay := readProjectOverlay(t, manifest)
	for _, stage := range bootstrap.KnownModelStages {
		route := overlay.Roles[bootstrap.StageRouteKey(stage)]
		if route.Provider != "grok-backend" || route.Model != "grok-4.5" || route.ReasoningEffort != "xhigh" {
			t.Fatalf("%s fallback route = %+v, want grok-backend/grok-4.5@xhigh", stage, route)
		}
	}
}

func TestCreateProjectPrefersConfiguredGlobalStageDefaults(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.Providers = map[string]bootstrap.ProviderConfig{
		"deepseek-backend": {Type: "openai", APIKey: "sk-test", Models: []string{"deepseek-v4-pro"}},
		"grok-backend":     {Type: "openai", APIKey: "sk-test", Models: []string{"grok-4.5"}},
	}
	cfg.Provider = "grok-backend"
	cfg.ModelName = "grok-4.5"
	cfg.Roles = map[string]bootstrap.RoleConfig{
		bootstrap.StageRouteKey(bootstrap.StageWriting): {
			Provider:        "deepseek-backend",
			Model:           "deepseek-v4-pro",
			ReasoningEffort: "high",
		},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Configured Defaults"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var manifest ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	writing := readProjectOverlay(t, manifest).Roles[bootstrap.StageRouteKey(bootstrap.StageWriting)]
	if writing.Provider != "deepseek-backend" || writing.Model != "deepseek-v4-pro" || writing.ReasoningEffort != "high" {
		t.Fatalf("writing route = %+v, want configured deepseek-v4-pro@high", writing)
	}
}

func TestCreateProjectRejectsUnknownStyle(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"name":"Bad Style","style":"missing-style"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create unknown style status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown style") {
		t.Fatalf("unknown style response should explain validation: %s", rec.Body.String())
	}
}

func TestOpenProjectHostUsesProjectStyleOverride(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProjectWithStyle("Project Style", "fantasy")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}
	base := testWebConfig(t)
	base.Style = "default"

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer h.Close()
	if snap := h.Snapshot(); snap.Style != "fantasy" {
		t.Fatalf("snapshot style = %q, want fantasy", snap.Style)
	}
}

func TestProjectStyleCanChangeBeforeWritingStarts(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProjectWithStyle("Fresh Style", "default")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/style", bytes.NewBufferString(`{"style":"romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("style switch status = %d body=%s", rec.Code, rec.Body.String())
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "romance" {
		t.Fatalf("project style = %q, want romance", overlay.Style)
	}
}

func TestProjectStyleCanChangeAfterProposalBeforeWritingStarts(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProjectWithStyle("Proposal Style", "default")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{
		NovelName:      "半熟恋人",
		Phase:          "ready",
		TotalChapters:  36,
		CompletedCount: 0,
		TotalWordCount: 0,
		RuntimeState:   "idle",
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/style", bytes.NewBufferString(`{"style":"romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("proposal style switch status = %d body=%s", rec.Code, rec.Body.String())
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "romance" {
		t.Fatalf("project style = %q, want romance", overlay.Style)
	}
}

func TestProjectStyleChangeReportsRunningNonWritingTaskAsBusy(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProjectWithStyle("Busy Style", "default")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{
		NovelName:      "半熟恋人",
		CompletedCount: 0,
		TotalWordCount: 0,
		RuntimeState:   "running",
		IsRunning:      true,
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/style", bytes.NewBufferString(`{"style":"romance"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("busy style status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), ErrSessionActionInProgress.Error()) {
		t.Fatalf("busy style response = %s, want action-in-progress error", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), ErrProjectStyleLocked.Error()) {
		t.Fatalf("busy non-writing task was reported as a permanent style lock: %s", rec.Body.String())
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "default" {
		t.Fatalf("busy project style changed to %q", overlay.Style)
	}
}

func TestProjectStyleChangeRejectsStartedProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProjectWithStyle("Locked Style", "default")
	if err != nil {
		t.Fatalf("CreateProjectWithStyle: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{NovelName: "已开书", TotalChapters: 12, CompletedCount: 1, TotalWordCount: 3200}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/style", bytes.NewBufferString(`{"style":"suspense"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("locked style status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.Style != "default" {
		t.Fatalf("locked project style changed to %q", overlay.Style)
	}
}
