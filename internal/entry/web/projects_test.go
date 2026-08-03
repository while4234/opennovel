package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func TestProjectManifestCreateListOpenTouches(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))

	created, err := store.CreateProject("My Test Novel")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if created.ID == "" || created.Name != "My Test Novel" {
		t.Fatalf("created manifest mismatch: %+v", created)
	}
	for _, dir := range []string{
		created.RootDir,
		filepath.Join(created.RootDir, "simulate"),
		filepath.Join(created.RootDir, "uploads"),
		filepath.Join(created.RootDir, "uploads", "adaptation"),
		filepath.Join(created.RootDir, "profiles"),
		created.OutputDir,
	} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("expected project dir %s: info=%v err=%v", dir, info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(created.RootDir, "project.json")); err != nil {
		t.Fatalf("project manifest not written: %v", err)
	}

	projects, err := store.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != created.ID {
		t.Fatalf("projects = %+v, want created project", projects)
	}

	time.Sleep(10 * time.Millisecond)
	opened, err := store.OpenProject(created.ID)
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	if !opened.LastAccessedAt.After(created.LastAccessedAt) {
		t.Fatalf("LastAccessedAt was not updated: before=%s after=%s", created.LastAccessedAt, opened.LastAccessedAt)
	}
}

func TestProjectOpenSerializesConcurrentManifestRefresh(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	created, err := store.CreateProject("Concurrent Open")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, openErr := store.OpenProject(created.ID)
			errs <- openErr
		}()
	}
	wg.Wait()
	close(errs)
	for openErr := range errs {
		if openErr != nil {
			t.Fatalf("concurrent OpenProject: %v", openErr)
		}
	}
}

func TestProjectRenameUpdatesNameWithoutMovingRoot(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	created, err := store.CreateProject("Original")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	renamed, err := store.RenameProject(created.ID, "Renamed Novel")
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if renamed.ID != created.ID || renamed.RootDir != created.RootDir || renamed.OutputDir != created.OutputDir {
		t.Fatalf("rename moved identity/path: before=%+v after=%+v", created, renamed)
	}
	if renamed.Name != "Renamed Novel" {
		t.Fatalf("renamed name = %q", renamed.Name)
	}
	if !renamed.UpdatedAt.After(created.UpdatedAt) && !renamed.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("updated_at not preserved sensibly: before=%s after=%s", created.UpdatedAt, renamed.UpdatedAt)
	}

	if _, err := store.RenameProject(created.ID, "  "); err == nil {
		t.Fatal("RenameProject accepted empty name")
	}
}

func TestProjectTrashMovesProjectOutOfActiveList(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	created, err := store.CreateProject("Trash Me")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	trashed, trashPath, err := store.TrashProject(created.ID)
	if err != nil {
		t.Fatalf("TrashProject: %v", err)
	}
	if trashed.ID != created.ID || trashed.DeletedAt == nil {
		t.Fatalf("trashed manifest = %+v", trashed)
	}
	if _, err := os.Stat(created.RootDir); !os.IsNotExist(err) {
		t.Fatalf("active project root still exists or unexpected error: %v", err)
	}
	if info, err := os.Stat(trashPath); err != nil || !info.IsDir() {
		t.Fatalf("trash path = %q info=%v err=%v", trashPath, info, err)
	}
	projects, err := store.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("active projects after trash = %+v, want none", projects)
	}
	if _, _, err := store.TrashProject("missing-project"); err == nil {
		t.Fatal("TrashProject accepted missing project")
	}
}

func TestProjectTrashListRestoreAndEmpty(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	created, err := store.CreateProject("Trash Lifecycle")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, _, err := store.TrashProject(created.ID); err != nil {
		t.Fatalf("TrashProject: %v", err)
	}

	trashed, err := store.ListTrashProjects()
	if err != nil {
		t.Fatalf("ListTrashProjects: %v", err)
	}
	if len(trashed) != 1 || trashed[0].ID != created.ID || trashed[0].DeletedAt == nil {
		t.Fatalf("trash projects = %+v", trashed)
	}

	restored, err := store.RestoreTrashProject(created.ID)
	if err != nil {
		t.Fatalf("RestoreTrashProject: %v", err)
	}
	if restored.ID != created.ID || restored.DeletedAt != nil || restored.RootDir != created.RootDir {
		t.Fatalf("restored = %+v, created=%+v", restored, created)
	}
	if _, err := os.Stat(restored.RootDir); err != nil {
		t.Fatalf("restored root missing: %v", err)
	}
	if active, err := store.ListProjects(); err != nil || len(active) != 1 {
		t.Fatalf("active projects after restore = %+v err=%v", active, err)
	}

	if _, _, err := store.TrashProject(created.ID); err != nil {
		t.Fatalf("TrashProject again: %v", err)
	}
	removed, err := store.EmptyTrashProjects()
	if err != nil {
		t.Fatalf("EmptyTrashProjects: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if trashed, err := store.ListTrashProjects(); err != nil || len(trashed) != 0 {
		t.Fatalf("trash after empty = %+v err=%v", trashed, err)
	}
}

func TestDeletedProjectManifestIsHiddenAndCannotOpen(t *testing.T) {
	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	created, err := store.CreateProject("Stranded Deleted")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	deletedAt := time.Now().UTC()
	created.DeletedAt = &deletedAt
	if err := writeProjectManifest(created); err != nil {
		t.Fatalf("write deleted manifest: %v", err)
	}

	projects, err := store.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("deleted project listed as active: %+v", projects)
	}
	if _, err := store.OpenProject(created.ID); !os.IsNotExist(err) {
		t.Fatalf("OpenProject deleted err = %v, want os.IsNotExist", err)
	}
}

func TestProjectResourceHandlersRenameAndTrashActiveProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Handler Project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+manifest.ID, bytes.NewBufferString(`{"name":"Handler Renamed"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d body=%s", rec.Code, rec.Body.String())
	}
	var renamed ProjectManifest
	if err := json.NewDecoder(rec.Body).Decode(&renamed); err != nil {
		t.Fatalf("decode rename response: %v", err)
	}
	if renamed.Name != "Handler Renamed" || renamed.ID != manifest.ID || renamed.RootDir != manifest.RootDir {
		t.Fatalf("renamed response = %+v", renamed)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/projects/"+manifest.ID, nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.closeCalls != 1 {
		t.Fatalf("active session close calls = %d, want 1", fake.closeCalls)
	}
	var deleted struct {
		Project   ProjectManifest `json:"project"`
		TrashPath string          `json:"trash_path"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&deleted); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleted.Project.DeletedAt == nil || deleted.TrashPath == "" {
		t.Fatalf("delete response = %+v", deleted)
	}
	if _, err := os.Stat(deleted.TrashPath); err != nil {
		t.Fatalf("trash path missing: %v", err)
	}
}

func TestTrashProjectHandlersListRestoreAndEmpty(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Trash Handler")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, _, err := server.store.TrashProject(manifest.ID); err != nil {
		t.Fatalf("TrashProject: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/trash/projects", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list trash status = %d body=%s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Projects []ProjectManifest `json:"projects"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed.Projects) != 1 || listed.Projects[0].ID != manifest.ID {
		t.Fatalf("listed trash = %+v", listed.Projects)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/trash/projects/"+manifest.ID+"/restore", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d body=%s", rec.Code, rec.Body.String())
	}
	if active, err := server.store.ListProjects(); err != nil || len(active) != 1 {
		t.Fatalf("active after restore = %+v err=%v", active, err)
	}

	if _, _, err := server.store.TrashProject(manifest.ID); err != nil {
		t.Fatalf("TrashProject again: %v", err)
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/trash/projects", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty status = %d body=%s", rec.Code, rec.Body.String())
	}
	var emptied struct {
		Removed int `json:"removed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&emptied); err != nil {
		t.Fatalf("decode empty response: %v", err)
	}
	if emptied.Removed != 1 {
		t.Fatalf("removed = %d, want 1", emptied.Removed)
	}
}

func TestLegacyProjectTrashHandlersListAndClear(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Legacy Trash Handler")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, _, err := server.store.TrashProject(manifest.ID); err != nil {
		t.Fatalf("TrashProject: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/trash", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Projects []ProjectManifest `json:"projects"`
		TrashDir string            `json:"trash_dir"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode legacy list response: %v", err)
	}
	if len(listed.Projects) != 1 || listed.Projects[0].ID != manifest.ID || listed.TrashDir == "" {
		t.Fatalf("legacy trash list = %+v", listed)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/projects/trash", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy clear status = %d body=%s", rec.Code, rec.Body.String())
	}
	var cleared struct {
		DeletedCount int `json:"deleted_count"`
		Removed      int `json:"removed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&cleared); err != nil {
		t.Fatalf("decode legacy clear response: %v", err)
	}
	if cleared.DeletedCount != 1 || cleared.Removed != 1 {
		t.Fatalf("legacy clear response = %+v", cleared)
	}
}

func TestOpenProjectHostUsesProjectOutputDir(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("Output Isolation")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cfg := testWebConfig(t)

	h, err := store.OpenProjectHost(cfg, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer h.Close()
	if h.Dir() != manifest.OutputDir {
		t.Fatalf("host dir = %q, want project output %q", h.Dir(), manifest.OutputDir)
	}
	if info, err := os.Stat(filepath.Join(manifest.OutputDir, "meta")); err != nil || !info.IsDir() {
		t.Fatalf("host store did not initialize project output: info=%v err=%v", info, err)
	}
}

func TestOpenProjectHostUsesProjectModelOverrideAndGlobalFallback(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	projectOverride, err := store.CreateProject("Project Override")
	if err != nil {
		t.Fatalf("CreateProject override: %v", err)
	}
	projectFallback, err := store.CreateProject("Project Fallback")
	if err != nil {
		t.Fatalf("CreateProject fallback: %v", err)
	}
	base := testWebConfig(t)
	base.ModelName = "global-model"
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global",
		Models: []string{"global-model", "project-model"},
	}
	if err := bootstrap.SaveConfig(ProjectConfigPath(projectOverride), bootstrap.Config{
		Provider:  "openai",
		ModelName: "project-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Models: []string{"project-model"}},
		},
	}); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	overrideHost, err := store.OpenProjectHost(base, assets.Load("default"), projectOverride)
	if err != nil {
		t.Fatalf("OpenProjectHost override: %v", err)
	}
	defer overrideHost.Close()
	if snap := overrideHost.Snapshot(); snap.ModelName != "project-model" {
		t.Fatalf("override model = %q, want project-model", snap.ModelName)
	}

	fallbackHost, err := store.OpenProjectHost(base, assets.Load("default"), projectFallback)
	if err != nil {
		t.Fatalf("OpenProjectHost fallback: %v", err)
	}
	defer fallbackHost.Close()
	if snap := fallbackHost.Snapshot(); snap.ModelName != "global-model" {
		t.Fatalf("fallback model = %q, want global-model", snap.ModelName)
	}
}

func TestProjectModelPersistenceWritesSecretFreeOverlay(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("Secret Free Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cfg := testWebConfig(t)
	cfg.ModelName = "global-model"
	cfg.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global-secret",
		Models: []string{"global-model", "writer-model"},
	}

	h, err := store.OpenProjectHost(cfg, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer h.Close()
	if err := h.SwitchModel("writer", "openai", "writer-model"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	data, err := os.ReadFile(ProjectConfigPath(manifest))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "sk-global-secret") || strings.Contains(text, "api_key") {
		t.Fatalf("project overlay leaked inherited secret: %s", text)
	}
	if !strings.Contains(text, `"writer"`) || !strings.Contains(text, `"writer-model"`) {
		t.Fatalf("project overlay missing writer route: %s", text)
	}
}

func TestProjectRoleSwitchPersistsOnlyExplicitRoleOverlay(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("Role Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	base := testWebConfig(t)
	base.ModelName = "global-a"
	base.ReasoningEffort = "high"
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global-secret",
		Models: []string{"global-a", "global-b", "writer-global", "writer-project", "editor-global"},
	}
	base.Roles = map[string]bootstrap.RoleConfig{
		"writer": {Provider: "openai", Model: "writer-global", ReasoningEffort: "medium"},
		"editor": {Provider: "openai", Model: "editor-global", ReasoningEffort: "low"},
	}

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	if err := h.SwitchModel("writer", "openai", "writer-project"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	h.Close()

	overlay := readProjectOverlay(t, manifest)
	if overlay.Provider != "" || overlay.ModelName != "" {
		t.Fatalf("concrete role switch persisted inherited default route: provider=%q model=%q", overlay.Provider, overlay.ModelName)
	}
	if overlay.ReasoningEffort != "" {
		t.Fatalf("concrete role switch persisted inherited reasoning default: %q", overlay.ReasoningEffort)
	}
	if len(overlay.Roles) != 1 {
		t.Fatalf("overlay roles = %+v, want only writer", overlay.Roles)
	}
	writer := overlay.Roles["writer"]
	if writer.Provider != "openai" || writer.Model != "writer-project" || writer.ReasoningEffort != "" {
		t.Fatalf("writer overlay = %+v, want only explicit route", writer)
	}
	if _, ok := overlay.Roles["editor"]; ok {
		t.Fatalf("overlay persisted unrelated inherited editor role: %+v", overlay.Roles)
	}
	if pc := overlay.Providers["openai"]; pc.APIKey != "" || pc.Type != "" || !containsString(pc.Models, "writer-project") || containsString(pc.Models, "global-a") {
		t.Fatalf("inherited provider overlay = %+v, want safe selected model metadata only", pc)
	}

	changedGlobal := base
	changedGlobal.ModelName = "global-b"
	reopened, err := store.OpenProjectHost(changedGlobal, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("reopen with changed global default: %v", err)
	}
	defer reopened.Close()
	if snap := reopened.Snapshot(); snap.ModelName != "global-b" {
		t.Fatalf("default route after reopen = %q, want changed global default", snap.ModelName)
	}
	provider, model, explicit := reopened.CurrentModelSelection("architect")
	if explicit || provider != "openai" || model != "global-b" {
		t.Fatalf("unset architect route = %s/%s explicit=%v, want inherited changed global default", provider, model, explicit)
	}
}

func TestProjectRoleThinkingPersistsOnlyRoleScopeAndFallsBack(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("Thinking Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	base := testWebConfig(t)
	base.ModelName = "gpt-5"
	base.ReasoningEffort = "high"
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-test",
		Models: []string{"gpt-5"},
	}

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	if err := h.SetRoleThinking("writer", "low"); err != nil {
		t.Fatalf("SetRoleThinking: %v", err)
	}
	h.Close()

	overlay := readProjectOverlay(t, manifest)
	if overlay.Provider != "" || overlay.ModelName != "" || overlay.ReasoningEffort != "" {
		t.Fatalf("role thinking persisted unrelated defaults: %+v", overlay)
	}
	if len(overlay.Roles) != 1 {
		t.Fatalf("roles = %+v, want only writer thinking", overlay.Roles)
	}
	writer := overlay.Roles["writer"]
	if writer.Provider != "" || writer.Model != "" || writer.ReasoningEffort != "low" {
		t.Fatalf("writer thinking overlay = %+v, want only reasoning_effort", writer)
	}

	changedGlobal := base
	changedGlobal.ReasoningEffort = "medium"
	reopened, err := store.OpenProjectHost(changedGlobal, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("reopen with changed global thinking: %v", err)
	}
	defer reopened.Close()
	if got := reopened.CurrentThinking("writer"); got != "low" {
		t.Fatalf("writer thinking = %q, want project override low", got)
	}
	if got := reopened.CurrentThinking("editor"); got != "medium" {
		t.Fatalf("editor thinking = %q, want changed global fallback medium", got)
	}
}

func TestProjectRetrySettingsPersistAcrossReopen(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("Retry Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	base := testWebConfig(t)
	base.ModelAutoSwitch.NetworkMaxAttempts = 7
	base.StructureRepairMaxAttempts = 7
	base.BudgetQualityMaxAttempts = 7
	base.AdaptationOutlineAuditRetryMaxAttempts = 7

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	if err := h.SetRetrySettings(14, 14, 3, 4); err != nil {
		t.Fatalf("SetRetrySettings: %v", err)
	}
	h.Close()

	overlay := readProjectOverlay(t, manifest)
	if got := overlay.ModelAutoSwitch.NetworkMaxAttempts; got != 14 {
		t.Fatalf("overlay model retry attempts = %d, want 14", got)
	}
	if got := overlay.StructureRepairMaxAttempts; got != 14 {
		t.Fatalf("overlay structure repair attempts = %d, want 14", got)
	}
	if got := overlay.BudgetQualityMaxAttempts; got != 3 {
		t.Fatalf("overlay budget quality attempts = %d, want 3", got)
	}
	if got := overlay.AdaptationOutlineAuditRetryMaxAttempts; got != 4 {
		t.Fatalf("overlay adaptation outline audit attempts = %d, want 4", got)
	}

	changedGlobal := base
	changedGlobal.ModelAutoSwitch.NetworkMaxAttempts = 7
	changedGlobal.StructureRepairMaxAttempts = 7
	changedGlobal.BudgetQualityMaxAttempts = 7
	changedGlobal.AdaptationOutlineAuditRetryMaxAttempts = 7
	reopened, err := store.OpenProjectHost(changedGlobal, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("reopen with changed global retry settings: %v", err)
	}
	defer reopened.Close()
	if got := reopened.ModelAutoSwitchConfig().EffectiveNetworkMaxAttempts(); got != 14 {
		t.Fatalf("reopened model retry attempts = %d, want 14", got)
	}
	if got := reopened.CurrentStructureRepairMaxAttempts(); got != 14 {
		t.Fatalf("reopened structure repair attempts = %d, want 14", got)
	}
	if got := reopened.CurrentBudgetQualityMaxAttempts(); got != 3 {
		t.Fatalf("reopened budget quality attempts = %d, want 3", got)
	}
	if got := reopened.CurrentAdaptationOutlineAuditRetryMaxAttempts(); got != 4 {
		t.Fatalf("reopened adaptation outline audit attempts = %d, want 4", got)
	}
}

func TestProjectRetrySettingsReturnsPersistError(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("Retry Persist Error")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	base := testWebConfig(t)
	base.ModelAutoSwitch.NetworkMaxAttempts = 7
	base.StructureRepairMaxAttempts = 7
	base.BudgetQualityMaxAttempts = 7
	base.AdaptationOutlineAuditRetryMaxAttempts = 7

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	defer h.Close()
	if err := os.MkdirAll(ProjectConfigPath(manifest), 0o755); err != nil {
		t.Fatalf("block project config path: %v", err)
	}

	if err := h.SetRetrySettings(14, 14, 3, 4); err == nil {
		t.Fatal("SetRetrySettings succeeded with unwritable project config path")
	}
	if got := h.ModelAutoSwitchConfig().EffectiveNetworkMaxAttempts(); got != 7 {
		t.Fatalf("model retry attempts after failed persist = %d, want rollback to 7", got)
	}
	if got := h.CurrentStructureRepairMaxAttempts(); got != 7 {
		t.Fatalf("structure repair attempts after failed persist = %d, want rollback to 7", got)
	}
	if got := h.CurrentBudgetQualityMaxAttempts(); got != 7 {
		t.Fatalf("budget quality attempts after failed persist = %d, want rollback to 7", got)
	}
	if got := h.CurrentAdaptationOutlineAuditRetryMaxAttempts(); got != 7 {
		t.Fatalf("adaptation outline audit attempts after failed persist = %d, want rollback to 7", got)
	}
}

func TestProjectOwnedProviderSecretPreservedWhileInheritedProviderRedacted(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store := NewProjectStore(filepath.Join(testTempDir(t), "novels"))
	manifest, err := store.CreateProject("Owned Provider Overlay")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), bootstrap.Config{
		Providers: map[string]bootstrap.ProviderConfig{
			"project-openai": {
				Type:    "openai",
				API:     "chat",
				APIKey:  "sk-project-owned",
				BaseURL: "https://project.example/v1",
				Models:  []string{"project-model"},
			},
		},
	}); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	base := testWebConfig(t)
	base.ModelName = "global-model"
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global-secret",
		Models: []string{"global-model", "global-editor"},
	}

	h, err := store.OpenProjectHost(base, assets.Load("default"), manifest)
	if err != nil {
		t.Fatalf("OpenProjectHost: %v", err)
	}
	if err := h.SwitchModel("writer", "project-openai", "project-model"); err != nil {
		t.Fatalf("SwitchModel project provider: %v", err)
	}
	if err := h.SwitchModel("editor", "openai", "global-editor"); err != nil {
		t.Fatalf("SwitchModel inherited provider: %v", err)
	}
	h.Close()

	overlay := readProjectOverlay(t, manifest)
	projectProvider := overlay.Providers["project-openai"]
	if projectProvider.APIKey != "sk-project-owned" || projectProvider.BaseURL != "https://project.example/v1" || projectProvider.Type != "openai" {
		t.Fatalf("project-owned provider was not preserved: %+v", projectProvider)
	}
	inheritedProvider := overlay.Providers["openai"]
	if inheritedProvider.APIKey != "" || inheritedProvider.Type != "" || inheritedProvider.BaseURL != "" {
		t.Fatalf("inherited provider leaked private config: %+v", inheritedProvider)
	}
	if !containsString(inheritedProvider.Models, "global-editor") {
		t.Fatalf("inherited provider missing safe selected model metadata: %+v", inheritedProvider)
	}
	data, err := os.ReadFile(ProjectConfigPath(manifest))
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if strings.Contains(string(data), "sk-global-secret") {
		t.Fatalf("project overlay leaked inherited global secret: %s", string(data))
	}
}

func readProjectOverlay(t *testing.T, manifest ProjectManifest) bootstrap.Config {
	t.Helper()
	cfg, err := bootstrap.LoadConfigFile(ProjectConfigPath(manifest))
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	return cfg
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
