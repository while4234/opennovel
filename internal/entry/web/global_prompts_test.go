package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
)

func TestGlobalPromptsGetReturnsFixedFamilyOrder(t *testing.T) {
	restoreGlobalPromptRegistry(t)
	cfg := testWebConfig(t)
	cfg.GlobalPrompts = map[string]string{globalprompt.FamilyGemini: "custom Gemini"}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var response struct {
		Prompts []globalPromptDTO `json:"prompts"`
	}
	serveJSON(t, server.Handler(), http.MethodGet, "/api/models/global-prompts", "", &response)
	want := []string{"claude", "deepseek", "gemini", "gpt", "grok", "kimi"}
	if len(response.Prompts) != len(want) {
		t.Fatalf("prompt families = %d, want %d", len(response.Prompts), len(want))
	}
	for i, family := range want {
		if response.Prompts[i].Family != family {
			t.Fatalf("prompts[%d].family = %q, want %q", i, response.Prompts[i].Family, family)
		}
	}
	if !response.Prompts[1].Fallback {
		t.Fatal("DeepSeek must be marked as the unknown-model fallback")
	}
	if !response.Prompts[2].Overridden || response.Prompts[2].Content != "custom Gemini" {
		t.Fatalf("Gemini DTO = %+v", response.Prompts[2])
	}
}

func TestGlobalPromptPutPersistsOriginalAndUpdatesOpenRuntime(t *testing.T) {
	restoreGlobalPromptRegistry(t)
	path := filepath.Join(testTempDir(t), "config.json")
	cfg := testWebConfig(t)
	cfg.PersistPath = path
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	content := "\n  custom GPT prompt  \n"
	body, err := json.Marshal(globalPromptRequest{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Prompts []globalPromptDTO `json:"prompts"`
	}
	serveJSON(t, server.Handler(), http.MethodPut, "/api/models/global-prompts/gpt", string(body), &response)

	if got := server.currentConfig().GlobalPrompts[globalprompt.FamilyGPT]; got != content {
		t.Fatalf("runtime config prompt = %q, want original source %q", got, content)
	}
	if got := globalprompt.TextForModel("openai/gpt-5.5"); got != "custom GPT prompt" {
		t.Fatalf("active runtime prompt = %q", got)
	}
	saved, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if got := saved.GlobalPrompts[globalprompt.FamilyGPT]; got != content {
		t.Fatalf("saved prompt = %q, want original source %q", got, content)
	}

	copyOfConfig := server.currentConfig()
	copyOfConfig.GlobalPrompts[globalprompt.FamilyGPT] = "mutated clone"
	if got := server.currentConfig().GlobalPrompts[globalprompt.FamilyGPT]; got != content {
		t.Fatalf("currentConfig leaked map mutation: %q", got)
	}
}

func TestGlobalPromptDeleteRestoresBuiltInAndPersistsRemoval(t *testing.T) {
	restoreGlobalPromptRegistry(t)
	path := filepath.Join(testTempDir(t), "config.json")
	cfg := testWebConfig(t)
	cfg.PersistPath = path
	cfg.GlobalPrompts = map[string]string{globalprompt.FamilyKimi: "custom Kimi"}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var response struct {
		Prompts []globalPromptDTO `json:"prompts"`
	}
	serveJSON(t, server.Handler(), http.MethodDelete, "/api/models/global-prompts/kimi", "", &response)
	if _, ok := server.currentConfig().GlobalPrompts[globalprompt.FamilyKimi]; ok {
		t.Fatal("Kimi override remains in current config")
	}
	builtIn, _ := globalprompt.BuiltIn(globalprompt.FamilyKimi)
	if got := globalprompt.TextForModel("moonshot/k3"); got != builtIn {
		t.Fatal("Kimi runtime prompt was not restored to built-in content")
	}
	saved, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if len(saved.GlobalPrompts) != 0 {
		t.Fatalf("saved overrides = %#v, want empty", saved.GlobalPrompts)
	}
}

func TestGlobalPromptSaveFailureDoesNotChangeRuntime(t *testing.T) {
	restoreGlobalPromptRegistry(t)
	cfg := testWebConfig(t)
	cfg.GlobalPrompts = map[string]string{globalprompt.FamilyGPT: "before"}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	server.configSaver = func(bootstrap.Config) error { return errors.New("disk unavailable") }

	req := httptest.NewRequest(http.MethodPut, "/api/models/global-prompts/gpt", strings.NewReader(`{"content":"after"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := server.currentConfig().GlobalPrompts[globalprompt.FamilyGPT]; got != "before" {
		t.Fatalf("runtime config changed after failed save: %q", got)
	}
	if got := globalprompt.TextForModel("openai/gpt-5.5"); got != "before" {
		t.Fatalf("registry changed after failed save: %q", got)
	}
}

func TestGlobalPromptValidationAndUnknownFamily(t *testing.T) {
	restoreGlobalPromptRegistry(t)
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	tests := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{name: "blank", path: "/api/models/global-prompts/gpt", body: `{"content":"  "}`, status: http.StatusBadRequest},
		{name: "nul", path: "/api/models/global-prompts/gpt", body: `{"content":"before\u0000after"}`, status: http.StatusBadRequest},
		{name: "too large", path: "/api/models/global-prompts/gpt", body: mustGlobalPromptRequest(t, strings.Repeat("a", globalprompt.MaxOverrideBytes+1)), status: http.StatusBadRequest},
		{name: "unknown", path: "/api/models/global-prompts/other", body: `{"content":"prompt"}`, status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPut, test.path, strings.NewReader(test.body)))
			if rec.Code != test.status {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, test.status, rec.Body.String())
			}
		})
	}
}

func TestNewServerLoadsPersistedGlobalPromptOverrides(t *testing.T) {
	restoreGlobalPromptRegistry(t)
	path := filepath.Join(testTempDir(t), "config.json")
	cfg := testWebConfig(t)
	cfg.GlobalPrompts = map[string]string{globalprompt.FamilyClaude: "persisted Claude"}
	if err := bootstrap.SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	loaded, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	server := NewServer(loaded, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	if got := globalprompt.TextForModel("anthropic/claude-opus"); got != "persisted Claude" {
		t.Fatalf("startup prompt = %q", got)
	}
}

func TestProjectConfigLoaderDropsGlobalPromptOverrides(t *testing.T) {
	restoreGlobalPromptRegistry(t)
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Prompt isolation")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), bootstrap.Config{
		GlobalPrompts: map[string]string{globalprompt.FamilyGPT: "project prompt"},
	}); err != nil {
		t.Fatalf("SaveConfig project overlay: %v", err)
	}

	loaded, found, err := server.store.loadProjectConfig(manifest)
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	if !found {
		t.Fatal("project config was not found")
	}
	if len(loaded.GlobalPrompts) != 0 {
		t.Fatalf("project global prompts were not dropped: %#v", loaded.GlobalPrompts)
	}
}

func restoreGlobalPromptRegistry(t *testing.T) {
	t.Helper()
	previous := globalprompt.Overrides()
	t.Cleanup(func() {
		if err := globalprompt.ReplaceOverrides(previous); err != nil {
			t.Errorf("restore global prompts: %v", err)
		}
	})
}

func mustGlobalPromptRequest(t *testing.T, content string) string {
	t.Helper()
	data, err := json.Marshal(globalPromptRequest{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
