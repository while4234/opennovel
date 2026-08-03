package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestProjectSnapshotDefaultsSimulationModeNormal(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Simulation Default")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Snapshot host.UISnapshot `json:"snapshot"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if response.Snapshot.SimulationMode != bootstrap.SimulationModeNormal {
		t.Fatalf("snapshot simulation mode = %q, want %q", response.Snapshot.SimulationMode, bootstrap.SimulationModeNormal)
	}
}

func TestProjectSimulationModePutPersistsOverlayWithoutLosingFields(t *testing.T) {
	base := testWebConfig(t)
	base.ModelName = "global-model"
	base.ModelAutoSwitch.NetworkMaxAttempts = 7
	base.StructureRepairMaxAttempts = 7
	base.BudgetQualityMaxAttempts = 7
	base.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-global-secret",
		Models: []string{"global-model", "project-model"},
	}
	server := NewServer(base, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Simulation Reinforced")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	original := bootstrap.Config{
		Provider:                   "openai",
		ModelName:                  "project-model",
		ReasoningEffort:            "high",
		Style:                      "romance",
		CoCreateTimeoutSeconds:     45,
		CoCreateMaxTokens:          8192,
		StructureRepairMaxAttempts: 4,
		BudgetQualityMaxAttempts:   5,
		ModelAutoSwitch: bootstrap.ModelAutoSwitchConfig{
			NetworkMaxAttempts: 6,
		},
		Providers: map[string]bootstrap.ProviderConfig{
			"openai": {Models: []string{"project-model"}},
		},
	}
	if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), original); err != nil {
		t.Fatalf("write original overlay: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/simulation-mode", bytes.NewBufferString(`{"simulation_mode":"reinforced"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("simulation mode status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Project        ProjectManifest `json:"project"`
		Snapshot       host.UISnapshot `json:"snapshot"`
		SimulationMode string          `json:"simulation_mode"`
		Running        bool            `json:"running"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Project.ID != manifest.ID || response.Running {
		t.Fatalf("response project/running mismatch: %+v", response)
	}
	if response.SimulationMode != bootstrap.SimulationModeReinforced ||
		response.Snapshot.SimulationMode != bootstrap.SimulationModeReinforced {
		t.Fatalf("response simulation mode mismatch: %+v", response)
	}

	overlay := readProjectOverlay(t, manifest)
	if overlay.SimulationMode != bootstrap.SimulationModeReinforced {
		t.Fatalf("overlay simulation_mode = %q, want reinforced", overlay.SimulationMode)
	}
	if overlay.Provider != original.Provider ||
		overlay.ModelName != original.ModelName ||
		overlay.ReasoningEffort != original.ReasoningEffort ||
		overlay.Style != original.Style ||
		overlay.CoCreateTimeoutSeconds != original.CoCreateTimeoutSeconds ||
		overlay.CoCreateMaxTokens != original.CoCreateMaxTokens ||
		overlay.StructureRepairMaxAttempts != original.StructureRepairMaxAttempts ||
		overlay.BudgetQualityMaxAttempts != original.BudgetQualityMaxAttempts ||
		overlay.ModelAutoSwitch.NetworkMaxAttempts != original.ModelAutoSwitch.NetworkMaxAttempts {
		t.Fatalf("overlay lost existing scalar fields: %+v", overlay)
	}
	if got := overlay.Providers["openai"].Models; len(got) != 1 || got[0] != "project-model" {
		t.Fatalf("overlay provider models = %#v, want [project-model]", got)
	}
}

func TestProjectSimulationModePersistsAcrossProjectReopen(t *testing.T) {
	runtimeRoot := filepath.Join(testTempDir(t), "runtime")
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	manifest, err := server.store.CreateProject("Simulation Reopen")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/simulation-mode", bytes.NewBufferString(`{"simulation_mode":"reinforced"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		server.Close()
		t.Fatalf("simulation mode status = %d body=%s", rec.Code, rec.Body.String())
	}
	server.Close()

	reopened := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer reopened.Close()
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+manifest.ID+"/snapshot", nil)
	rec = httptest.NewRecorder()
	reopened.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reopened snapshot status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Snapshot host.UISnapshot `json:"snapshot"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if response.Snapshot.SimulationMode != bootstrap.SimulationModeReinforced {
		t.Fatalf("reopened snapshot simulation mode = %q, want %q", response.Snapshot.SimulationMode, bootstrap.SimulationModeReinforced)
	}
}

func TestProjectSimulationModePutAcceptsModeAliasAndCompletedIdleProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Completed Idle Simulation")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.snapshot = host.UISnapshot{
		RuntimeState:   "idle",
		CompletedCount: 1,
		TotalWordCount: 3200,
		IsRunning:      false,
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/simulation-mode", bytes.NewBufferString(`{"mode":"reinforced"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("completed idle simulation mode status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.closeCalls != 1 {
		t.Fatalf("old host close calls = %d, want 1", fake.closeCalls)
	}
	overlay := readProjectOverlay(t, manifest)
	if overlay.SimulationMode != bootstrap.SimulationModeReinforced {
		t.Fatalf("overlay simulation_mode = %q, want reinforced", overlay.SimulationMode)
	}
}

func TestProjectSimulationModePutRejectsInvalidMode(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Bad Simulation Mode")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/simulation-mode", bytes.NewBufferString(`{"simulation_mode":"experimental"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestProjectSimulationModePutRejectsBusyStates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *Server, ProjectManifest, *fakeProjectHost)
	}{
		{
			name: "running",
			setup: func(_ *testing.T, _ *Server, _ ProjectManifest, fake *fakeProjectHost) {
				fake.snapshot = host.UISnapshot{RuntimeState: "running", IsRunning: true}
			},
		},
		{
			name: "action",
			setup: func(t *testing.T, server *Server, manifest ProjectManifest, _ *fakeProjectHost) {
				t.Helper()
				session := server.sessions.Project(manifest.ID)
				unlock, err := session.beginActionKind(projectActionKindSimulationAnalysis)
				if err != nil {
					t.Fatalf("beginActionKind: %v", err)
				}
				t.Cleanup(unlock)
			},
		},
		{
			name: "cocreate",
			setup: func(_ *testing.T, server *Server, manifest ProjectManifest, _ *fakeProjectHost) {
				session := server.sessions.Project(manifest.ID)
				session.cocreate = &webCoCreateSession{kind: webCoCreateKindNormal}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
			defer server.Close()
			manifest, err := server.store.CreateProject("Busy Simulation " + tc.name)
			if err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			fake := installFakeSession(t, server, manifest)
			tc.setup(t, server, manifest, fake)

			req := httptest.NewRequest(http.MethodPut, "/api/projects/"+manifest.ID+"/simulation-mode", bytes.NewBufferString(`{"simulation_mode":"reinforced"}`))
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("%s status = %d body=%s, want 409", tc.name, rec.Code, rec.Body.String())
			}
			if fake.closeCalls != 0 {
				t.Fatalf("%s should not close busy host, close calls=%d", tc.name, fake.closeCalls)
			}
		})
	}
}
