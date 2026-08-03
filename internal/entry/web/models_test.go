package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/codexauth"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	hostpkg "github.com/voocel/ainovel-cli/internal/host"
)

func TestGlobalModelsAndDefaultSwitch(t *testing.T) {
	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := testWebConfig(t)
	cfg.PersistPath = ""
	cfg.Providers["openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-test",
		Models: []string{"gpt-test", "gpt-next"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var listed struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodGet, "/api/models", "", &listed)
	if len(listed.Models.Providers) != 1 || listed.Models.Providers[0].Name != "openai" {
		t.Fatalf("global providers = %+v", listed.Models.Providers)
	}
	if listed.Models.Roles[0].Provider != "openai" || listed.Models.Roles[0].Model != "gpt-test" {
		t.Fatalf("global default route = %+v", listed.Models.Roles[0])
	}

	var switched struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/default", `{"provider":"openai","model":"gpt-next"}`, &switched)
	if switched.Runtime.Config.Provider != "openai" || switched.Runtime.Config.Model != "gpt-next" {
		t.Fatalf("runtime default = %+v", switched.Runtime.Config)
	}
	if got := server.currentConfig().ModelName; got != "gpt-next" {
		t.Fatalf("server default model = %q, want gpt-next", got)
	}
	for _, role := range []string{"coordinator", "architect", "character", "writer", "editor", "auditor"} {
		if route := findModelRoute(switched.Models.Roles, role); route.Provider != "openai" || route.Model != "gpt-next" || route.Explicit {
			t.Fatalf("%s route after default switch = %+v", role, route)
		}
	}

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if saved.Provider != "openai" || saved.ModelName != "gpt-next" {
		t.Fatalf("saved default = %s/%s", saved.Provider, saved.ModelName)
	}
	if len(saved.Roles) != 0 {
		t.Fatalf("saved agent routes should inherit default: %+v", saved.Roles)
	}

	manifest, err := server.store.CreateProject("Global Default")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	session, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatalf("Open session: %v", err)
	}
	if snap := session.Snapshot(); snap.ModelName != "gpt-next" {
		t.Fatalf("new project model = %q, want gpt-next", snap.ModelName)
	}
	stages := session.ModelConfig().Stages
	if len(stages) != len(bootstrap.KnownModelStages) {
		t.Fatalf("project stage routes = %d, want %d: %+v", len(stages), len(bootstrap.KnownModelStages), stages)
	}
	characterAnalysis := findModelRoute(stages, bootstrap.StageRouteKey(bootstrap.StageCharacterAnalysis))
	if characterAnalysis.Label != "角色分析" || characterAnalysis.FallbackRole != "character" {
		t.Fatalf("character analysis stage route = %+v", characterAnalysis)
	}
	for _, route := range stages {
		if route.Provider != "openai" || route.Model != "gpt-next" || route.Explicit {
			t.Fatalf("inherited project stage route = %+v", route)
		}
	}
	if _, err := os.Stat(ProjectConfigPath(manifest)); !os.IsNotExist(err) {
		t.Fatalf("new project should inherit global default without project overlay, stat err=%v", err)
	}
}

func TestGlobalRetrySettingsKeepsAdaptationOutlineAuditRetryIndependent(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var updated struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/retry-settings", `{"model_call_max_attempts":9,"structure_repair_max_attempts":4,"budget_quality_max_attempts":3,"adaptation_outline_audit_retry_max_attempts":5}`, &updated)
	if updated.Models.StructureRepairMaxAttempts != 4 {
		t.Fatalf("structure repair attempts = %d, want 4", updated.Models.StructureRepairMaxAttempts)
	}
	if updated.Models.AdaptationOutlineAuditRetryMaxAttempts != 5 {
		t.Fatalf("adaptation outline audit attempts = %d, want 5", updated.Models.AdaptationOutlineAuditRetryMaxAttempts)
	}
	if got := server.currentConfig().AdaptationOutlineAuditRetryMaxAttempts; got != 5 {
		t.Fatalf("saved adaptation outline audit attempts = %d, want 5", got)
	}
}

func TestGlobalModelSwitchRoutePersistsRole(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["deepseek"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-test",
		Models: []string{"deepseek-chat"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var switched struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/switch", `{"role":"writer","provider":"deepseek","model":"deepseek-chat"}`, &switched)
	if route := findModelRoute(switched.Models.Roles, "writer"); route.Provider != "deepseek" || route.Model != "deepseek-chat" || !route.Explicit {
		t.Fatalf("writer route = %+v", route)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Roles["writer"].Provider != "deepseek" || saved.Roles["writer"].Model != "deepseek-chat" {
		t.Fatalf("saved writer role = %+v", saved.Roles["writer"])
	}
}

func TestGlobalStageModelAndThinkingPersist(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["grok-oauth"] = bootstrap.ProviderConfig{
		Type: "openai", APIKey: "sk-test", Models: []string{"grok-4.5"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var initial struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodGet, "/api/models", "", &initial)
	if len(initial.Models.Stages) != len(bootstrap.KnownModelStages) {
		t.Fatalf("global stages = %d, want %d", len(initial.Models.Stages), len(bootstrap.KnownModelStages))
	}

	stageRole := bootstrap.StageRouteKey(bootstrap.StageWriting)
	var switched struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/switch", `{"role":"stage:writing","provider":"grok-oauth","model":"grok-4.5"}`, &switched)
	route := findModelRoute(switched.Models.Stages, stageRole)
	if route.Provider != "grok-oauth" || route.Model != "grok-4.5" || !route.Explicit {
		t.Fatalf("writing stage route = %+v", route)
	}

	var thinking struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/thinking", `{"role":"stage:writing","level":"xhigh"}`, &thinking)
	route = findModelRoute(thinking.Models.Stages, stageRole)
	if route.ReasoningEffort != "xhigh" {
		t.Fatalf("writing stage reasoning = %q, want xhigh", route.ReasoningEffort)
	}

	var inherited struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/switch", `{"role":"stage:writing","inherit":true}`, &inherited)
	route = findModelRoute(inherited.Models.Stages, stageRole)
	if route.Explicit || route.ReasoningEffort != "xhigh" {
		t.Fatalf("inherited writing stage route = %+v, want inherited model with xhigh override", route)
	}

	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	savedRoute := saved.Roles[stageRole]
	if savedRoute.Provider != "" || savedRoute.Model != "" || savedRoute.ReasoningEffort != "xhigh" {
		t.Fatalf("saved writing stage = %+v", savedRoute)
	}
}

func TestGlobalCoCreateTimeoutPersists(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var response struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				CoCreateTimeoutSeconds int `json:"cocreate_timeout_seconds"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/cocreate-timeout", `{"seconds":45}`, &response)
	if response.Models.CoCreateTimeoutSeconds != 45 || response.Runtime.Config.CoCreateTimeoutSeconds != 45 {
		t.Fatalf("timeout response models=%d runtime=%d", response.Models.CoCreateTimeoutSeconds, response.Runtime.Config.CoCreateTimeoutSeconds)
	}
	if got := server.currentConfig().CoCreateTimeoutSeconds; got != 45 {
		t.Fatalf("server timeout = %d, want 45", got)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.CoCreateTimeoutSeconds != 45 {
		t.Fatalf("saved timeout = %d, want 45", saved.CoCreateTimeoutSeconds)
	}
}

func TestGlobalCoCreateMaxTokensPersists(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var response struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				CoCreateMaxTokens int `json:"cocreate_max_tokens"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/cocreate-max-tokens", `{"tokens":8192}`, &response)
	if response.Models.CoCreateMaxTokens != 8192 || response.Runtime.Config.CoCreateMaxTokens != 8192 {
		t.Fatalf("max tokens response models=%d runtime=%d", response.Models.CoCreateMaxTokens, response.Runtime.Config.CoCreateMaxTokens)
	}
	if got := server.currentConfig().CoCreateMaxTokens; got != 8192 {
		t.Fatalf("server max tokens = %d, want 8192", got)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.CoCreateMaxTokens != 8192 {
		t.Fatalf("saved max tokens = %d, want 8192", saved.CoCreateMaxTokens)
	}
}

func TestProjectCoCreateTimeoutUsesProjectHost(t *testing.T) {
	base := testWebConfig(t)
	server := NewServer(base, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Project Timeout")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	var response struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/cocreate-timeout", `{"seconds":30}`, &response)
	if fake.setCoCreateTimeoutCalls != 1 || fake.coCreateTimeoutSeconds != 30 {
		t.Fatalf("host timeout calls=%d seconds=%d", fake.setCoCreateTimeoutCalls, fake.coCreateTimeoutSeconds)
	}
	if response.Models.CoCreateTimeoutSeconds != 30 {
		t.Fatalf("response timeout = %d, want 30", response.Models.CoCreateTimeoutSeconds)
	}
}

func TestProjectCoCreateMaxTokensUsesProjectHost(t *testing.T) {
	base := testWebConfig(t)
	server := NewServer(base, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Project Max Tokens")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	var response struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/cocreate-max-tokens", `{"tokens":12288}`, &response)
	if fake.setCoCreateMaxTokensCalls != 1 || fake.coCreateMaxTokens != 12288 {
		t.Fatalf("host max token calls=%d tokens=%d", fake.setCoCreateMaxTokensCalls, fake.coCreateMaxTokens)
	}
	if response.Models.CoCreateMaxTokens != 12288 {
		t.Fatalf("response max tokens = %d, want 12288", response.Models.CoCreateMaxTokens)
	}
}

func TestProjectModelDeleteUsesProjectHost(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Project Model Delete")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	var response struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodDelete, "/api/projects/"+manifest.ID+"/models", `{"provider":"openrouter","model":"model-b"}`, &response)
	if fake.removeProviderCalls != 1 || fake.removeProviderName != "openrouter" || fake.removeProviderModel != "model-b" {
		t.Fatalf("remove model args calls=%d provider=%q model=%q", fake.removeProviderCalls, fake.removeProviderName, fake.removeProviderModel)
	}
	if response.Models.Roles[0].Provider != "openrouter" {
		t.Fatalf("models response = %+v", response.Models)
	}
}

func TestGlobalModelAddGrokOAuthProvider(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	base := testWebConfig(t)
	base.PersistPath = ""
	server := NewServer(base, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var added struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", `{"role":"default","provider":"grok-oauth","model":"grok-4.3-latest","type":"grok","auth":"grok_oauth","account_id":"default","api":"chat","api_key":"should-not-save"}`, &added)
	if added.Runtime.Config.Provider != base.Provider || added.Runtime.Config.Model != base.ModelName {
		t.Fatalf("runtime default = %+v", added.Runtime.Config)
	}
	if !modelConfigHasProvider(added.Models, "grok-oauth", "grok-4.3-latest") {
		t.Fatalf("models missing grok provider: %+v", added.Models.Providers)
	}
	cfg := server.currentConfig()
	pc := cfg.Providers["grok-oauth"]
	if pc.Type != "grok" || pc.Auth != bootstrap.ProviderAuthGrokOAuth || pc.AccountID != "default" {
		t.Fatalf("grok provider config = %+v", pc)
	}
	if pc.API != "" || pc.APIKey != "" {
		t.Fatalf("grok_oauth should not persist OpenAI api or api_key fields: %+v", pc)
	}

	saved, err := bootstrap.LoadConfigFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if saved.Provider != base.Provider || saved.ModelName != base.ModelName {
		t.Fatalf("saved default = %s/%s", saved.Provider, saved.ModelName)
	}
}

func TestGlobalModelAddCodexAuthProvider(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	home := testTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var added struct {
		Models apiModelConfig `json:"models"`
	}
	body := `{"select_after_save":false,"provider":"codex-login","model":"gpt-5.5","label":"Codex","template_provider":"codex","type":"openai","auth":"codex","api":"responses","api_key":"should-not-save","base_url":"https://chatgpt.com/backend-api/codex","auth_file":"D:/codex/auth.json"}`
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", body, &added)
	if !modelConfigHasProvider(added.Models, "codex-login", "gpt-5.5") {
		t.Fatalf("models missing codex provider: %+v", added.Models.Providers)
	}
	provider := findModelProvider(added.Models.Providers, "codex-login")
	if provider.APIKeyConfigured || !provider.AuthFileConfigured {
		t.Fatalf("codex provider response = %+v", provider)
	}
	pc := server.currentConfig().Providers["codex-login"]
	if pc.Type != "openai" || pc.Auth != bootstrap.ProviderAuthCodex || pc.API != "responses" {
		t.Fatalf("codex provider config = %+v", pc)
	}
	if pc.APIKey != "" || pc.AuthFile != "D:/codex/auth.json" {
		t.Fatalf("codex auth should persist auth_file but not api_key: %+v", pc)
	}
}

func TestGlobalModelAddCanSaveWithoutSwitchingDefault(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var added struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"runtime"`
	}
	body := `{"select_after_save":false,"provider":"deepseek2","model":"deepseek-v4-pro","label":"DeepSeek Relay","type":"openai","api":"chat","api_key":"sk-test","base_url":"https://api.example/v1"}`
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", body, &added)
	if !modelConfigHasProvider(added.Models, "deepseek2", "deepseek-v4-pro") {
		t.Fatalf("models missing saved provider: %+v", added.Models.Providers)
	}
	if added.Runtime.Config.Provider != cfg.Provider || added.Runtime.Config.Model != cfg.ModelName {
		t.Fatalf("runtime default changed to %+v, want %s/%s", added.Runtime.Config, cfg.Provider, cfg.ModelName)
	}
	if server.currentConfig().Provider != cfg.Provider || server.currentConfig().ModelName != cfg.ModelName {
		t.Fatalf("server default changed to %s/%s", server.currentConfig().Provider, server.currentConfig().ModelName)
	}
}

func TestGlobalModelEditRenamesProviderAndPreservesBlankAPIKey(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["custom-openai"] = bootstrap.ProviderConfig{
		Label:   "Wrong",
		Type:    "openai",
		API:     "chat",
		APIKey:  "sk-secret",
		BaseURL: "https://old.example/v1",
		Models:  []string{"old-model"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var edited struct {
		Models apiModelConfig `json:"models"`
	}
	body := `{"role":"default","original_provider":"custom-openai","provider":"fixed-openai","model":"new-model","label":"Fixed","type":"openai","api":"responses","base_url":"https://new.example/v1","network_disconnect_max_attempts":4,"auto_switch_candidate_pool":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/add", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("model edit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("model edit response leaked api key: %s", rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &edited); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	provider := findModelProvider(edited.Models.Providers, "fixed-openai")
	if provider.Name == "" || !provider.APIKeyConfigured || provider.NetworkDisconnectMaxAttempts != 4 || !provider.AutoSwitchCandidatePool {
		t.Fatalf("edited provider response = %+v", provider)
	}
	if route := findModelRoute(edited.Models.Roles, "default"); route.Provider != cfg.Provider || route.Model != cfg.ModelName {
		t.Fatalf("default route changed after provider edit = %+v", route)
	}
	if route := findModelRoute(edited.Models.Roles, "writer"); route.Provider != cfg.Provider || route.Model != cfg.ModelName || route.Explicit {
		t.Fatalf("writer route after provider edit = %+v", route)
	}
	next := server.currentConfig()
	if _, ok := next.Providers["custom-openai"]; ok {
		t.Fatal("old provider key still configured")
	}
	pc := next.Providers["fixed-openai"]
	if pc.APIKey != "sk-secret" || pc.Label != "Fixed" || pc.API != "responses" || pc.BaseURL != "https://new.example/v1" {
		t.Fatalf("edited provider config = %+v", pc)
	}
	if next.ModelAutoSwitch.EffectiveNetworkMaxAttempts() != 4 || !modelAutoSwitchHasProvider(next.ModelAutoSwitch, "fixed-openai") {
		t.Fatalf("auto switch config = %+v", next.ModelAutoSwitch)
	}
	if next.Provider != cfg.Provider || next.ModelName != cfg.ModelName {
		t.Fatalf("server default changed to %s/%s", next.Provider, next.ModelName)
	}
	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if saved.Providers["fixed-openai"].APIKey != "sk-secret" {
		t.Fatalf("saved provider = %+v", saved.Providers["fixed-openai"])
	}
	if saved.Provider != cfg.Provider || saved.ModelName != cfg.ModelName {
		t.Fatalf("saved default changed to %s/%s", saved.Provider, saved.ModelName)
	}
}

func TestGlobalModelEditRefreshesProjectProviderReferences(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["custom-openai"] = bootstrap.ProviderConfig{
		Label:  "Old Label",
		Type:   "openai",
		API:    "chat",
		APIKey: "sk-secret",
		Models: []string{"old-model", "new-model"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	closedProject, err := server.store.CreateProject("Closed Project")
	if err != nil {
		t.Fatalf("CreateProject closed: %v", err)
	}
	writeProjectModelOverlay(t, closedProject, bootstrap.Config{
		Provider:  "custom-openai",
		ModelName: "old-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"custom-openai": {Label: "Stale Label", Models: []string{"old-model"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"writer": {Provider: "custom-openai", Model: "old-model"},
		},
		ModelAutoSwitch: bootstrap.ModelAutoSwitchConfig{
			FallbackBackends: []string{"custom-openai"},
		},
	})

	activeProject, err := server.store.CreateProject("Active Project")
	if err != nil {
		t.Fatalf("CreateProject active: %v", err)
	}
	writeProjectModelOverlay(t, activeProject, bootstrap.Config{
		Provider:  "custom-openai",
		ModelName: "old-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"custom-openai": {Label: "Stale Label", Models: []string{"old-model"}},
		},
		Roles: map[string]bootstrap.RoleConfig{
			"editor": {Provider: "custom-openai", Model: "old-model"},
		},
	})
	activeSession, _, err := server.sessions.Open(activeProject.ID)
	if err != nil {
		t.Fatalf("Open active session: %v", err)
	}
	if route := findModelRoute(activeSession.ModelConfig().Roles, "default"); route.Provider != "custom-openai" {
		t.Fatalf("precondition active default route = %+v", route)
	}

	var edited struct {
		Models apiModelConfig `json:"models"`
	}
	body := `{"role":"default","original_provider":"custom-openai","provider":"fixed-openai","model":"new-model","label":"Fixed Label","type":"openai","api":"chat","network_disconnect_max_attempts":9,"auto_switch_candidate_pool":true}`
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/add", body, &edited)
	if route := findModelRoute(edited.Models.Roles, "default"); route.Provider != cfg.Provider || route.Model != cfg.ModelName {
		t.Fatalf("global default route = %+v", route)
	}

	closedOverlay := readProjectOverlay(t, closedProject)
	if _, ok := closedOverlay.Providers["custom-openai"]; ok {
		t.Fatalf("closed overlay still has old provider: %+v", closedOverlay.Providers)
	}
	if closedOverlay.Provider != "fixed-openai" || closedOverlay.ModelName != "old-model" {
		t.Fatalf("closed overlay default = %s/%s", closedOverlay.Provider, closedOverlay.ModelName)
	}
	if rc := closedOverlay.Roles["writer"]; rc.Provider != "fixed-openai" || rc.Model != "old-model" {
		t.Fatalf("closed writer route = %+v", rc)
	}
	if !reflect.DeepEqual(closedOverlay.ModelAutoSwitch.FallbackBackends, []string{"fixed-openai"}) || closedOverlay.ModelAutoSwitch.EffectiveNetworkMaxAttempts() != 9 {
		t.Fatalf("closed auto switch = %+v", closedOverlay.ModelAutoSwitch)
	}
	if pc := closedOverlay.Providers["fixed-openai"]; pc.Label != "Fixed Label" || pc.Type != "" || pc.APIKey != "" || !containsString(pc.Models, "old-model") || !containsString(pc.Models, "new-model") {
		t.Fatalf("closed inherited provider metadata = %+v", pc)
	}

	activeModels := activeSession.ModelConfig()
	if route := findModelRoute(activeModels.Roles, "default"); route.Provider != "fixed-openai" || route.Model != "old-model" {
		t.Fatalf("active default route = %+v", route)
	}
	if route := findModelRoute(activeModels.Roles, "editor"); route.Provider != "fixed-openai" || route.Model != "old-model" {
		t.Fatalf("active editor route = %+v", route)
	}
	activeProvider := findModelProvider(activeModels.Providers, "fixed-openai")
	if activeProvider.Name == "" || activeProvider.Label != "Fixed Label" || !activeProvider.AutoSwitchCandidatePool || activeProvider.NetworkDisconnectMaxAttempts != 9 {
		t.Fatalf("active provider = %+v", activeProvider)
	}
	if !reflect.DeepEqual(activeModels.ModelAutoSwitch.FallbackBackends, []string{"fixed-openai"}) || activeModels.ModelAutoSwitch.NetworkMaxAttempts != 9 {
		t.Fatalf("active auto switch = %+v", activeModels.ModelAutoSwitch)
	}

	var reopened struct {
		Models apiModelConfig `json:"models"`
	}
	serveJSON(t, server.Handler(), http.MethodGet, "/api/projects/"+closedProject.ID+"/models", "", &reopened)
	if modelConfigHasProvider(reopened.Models, "custom-openai", "old-model") {
		t.Fatalf("reopened project still exposes old provider: %+v", reopened.Models.Providers)
	}
	reopenedProvider := findModelProvider(reopened.Models.Providers, "fixed-openai")
	if reopenedProvider.Name == "" || reopenedProvider.Label != "Fixed Label" {
		t.Fatalf("reopened provider = %+v", reopenedProvider)
	}
	if route := findModelRoute(reopened.Models.Roles, "default"); route.Provider != "fixed-openai" {
		t.Fatalf("reopened default route = %+v", route)
	}
	if !reflect.DeepEqual(reopened.Models.ModelAutoSwitch.FallbackBackends, []string{"fixed-openai"}) || reopened.Models.ModelAutoSwitch.NetworkMaxAttempts != 9 {
		t.Fatalf("reopened auto switch = %+v", reopened.Models.ModelAutoSwitch)
	}
}

func TestGlobalModelEditProbeFailureDoesNotPersist(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return errors.New("probe rejected")
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["custom-openai"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-secret",
		Models: []string{"old-model"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/models/add", bytes.NewBufferString(`{"role":"default","original_provider":"custom-openai","provider":"fixed-openai","model":"new-model","type":"openai"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("model edit status = %d body=%s, want failure", rec.Code, rec.Body.String())
	}
	if _, ok := server.currentConfig().Providers["fixed-openai"]; ok {
		t.Fatal("failed probe persisted renamed provider")
	}
	if server.currentConfig().Providers["custom-openai"].APIKey != "sk-secret" {
		t.Fatalf("original provider changed: %+v", server.currentConfig().Providers["custom-openai"])
	}
}

func writeProjectModelOverlay(t *testing.T, manifest ProjectManifest, cfg bootstrap.Config) {
	t.Helper()
	if err := bootstrap.SaveConfig(ProjectConfigPath(manifest), cfg); err != nil {
		t.Fatalf("write project overlay: %v", err)
	}
}

func TestGlobalModelTestDoesNotPersistOrLeakAPIKey(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return errors.New("probe failed for sk-secret")
	})
	t.Cleanup(restore)

	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/models/test", bytes.NewBufferString(`{"role":"default","provider":"probe-openai","model":"probe-model","type":"openai","api_key":"sk-secret","base_url":"https://proxy.example/v1"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model test status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("model test response leaked api key: %s", rec.Body.String())
	}
	if _, ok := server.currentConfig().Providers["probe-openai"]; ok {
		t.Fatal("model test should not persist provider config")
	}
}

func TestModelProviderRequestIncludesPerModelReasoningDefault(t *testing.T) {
	effort := " high "
	req := modelProviderRequest{Model: "gpt-5.6-sol", ModelReasoningEffort: &effort}
	pc := req.providerConfig()
	if got := pc.ModelReasoningEfforts["gpt-5.6-sol"]; got != "high" {
		t.Fatalf("model reasoning effort = %q, want high", got)
	}
}

func TestGlobalModelTestExistingProviderUsesEditFlow(t *testing.T) {
	restore := hostpkg.SetAddedModelConnectivityProbeForTest(func(context.Context, agentcore.ChatModel) error {
		return nil
	})
	t.Cleanup(restore)

	cfg := testWebConfig(t)
	cfg.Providers["deepseek"] = bootstrap.ProviderConfig{
		Label:   "DeepSeek",
		Type:    "openai",
		API:     "chat",
		APIKey:  "sk-old",
		BaseURL: "https://api.sfkey.cn/v1",
		Models:  []string{"deepseek-v4-pro"},
	}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	body := `{"role":"default","original_provider":"deepseek","provider":"deepseek","model":"deepseek-v4-pro","label":"DeepSeek","type":"openai","api":"chat","base_url":"https://api.sfkey.cn/v1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/test", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("model test status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("model test used add-provider flow: %s", rec.Body.String())
	}
	var response struct {
		Test hostpkg.ProviderModelTestResult `json:"test"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Test.Status != "ok" || response.Test.Provider != "deepseek" {
		t.Fatalf("model test response = %+v", response.Test)
	}
	if server.currentConfig().Providers["deepseek"].APIKey != "sk-old" {
		t.Fatalf("existing provider changed: %+v", server.currentConfig().Providers["deepseek"])
	}
}

func TestGlobalModelDeleteRemovesProviderAndRoleRoute(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	cfg.Providers["proxy"] = bootstrap.ProviderConfig{
		Type:   "openai",
		APIKey: "sk-proxy",
		Models: []string{"proxy-model"},
	}
	cfg.Roles = map[string]bootstrap.RoleConfig{
		"writer": {Provider: "proxy", Model: "proxy-model"},
	}
	cfg.ModelAutoSwitch.FallbackBackends = []string{"openai", "proxy"}
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	legacyProject, err := server.store.CreateProject("Legacy Inherited Provider")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := bootstrap.SaveConfig(ProjectConfigPath(legacyProject), bootstrap.Config{
		Provider:  "proxy",
		ModelName: "proxy-model",
		Providers: map[string]bootstrap.ProviderConfig{
			"proxy": cfg.Providers["proxy"],
		},
		Roles: map[string]bootstrap.RoleConfig{
			"editor": {Provider: "proxy", Model: "proxy-model"},
		},
		ModelAutoSwitch: bootstrap.ModelAutoSwitchConfig{
			FallbackBackends: []string{"proxy"},
		},
	}); err != nil {
		t.Fatalf("SaveConfig legacy project: %v", err)
	}
	ownedProject, err := server.store.CreateProject("Project Owned Provider")
	if err != nil {
		t.Fatalf("CreateProject owned: %v", err)
	}
	if err := bootstrap.SaveConfig(ProjectConfigPath(ownedProject), bootstrap.Config{
		Providers: map[string]bootstrap.ProviderConfig{
			"proxy": cfg.Providers["proxy"],
		},
		ProjectOwnedProviders: map[string]bool{"proxy": true},
	}); err != nil {
		t.Fatalf("SaveConfig owned project: %v", err)
	}

	var deleted struct {
		Models  apiModelConfig `json:"models"`
		Runtime struct {
			Config struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"config"`
		} `json:"runtime"`
	}
	serveJSON(t, server.Handler(), http.MethodDelete, "/api/models", `{"provider":"proxy","model":"proxy-model"}`, &deleted)
	if modelConfigHasProvider(deleted.Models, "proxy", "proxy-model") {
		t.Fatalf("deleted models still include proxy: %+v", deleted.Models.Providers)
	}
	if route := findModelRoute(deleted.Models.Roles, "writer"); route.Provider != "openai" || route.Model != "gpt-test" || route.Explicit {
		t.Fatalf("writer route after delete = %+v", route)
	}
	if deleted.Runtime.Config.Provider != "openai" || deleted.Runtime.Config.Model != "gpt-test" {
		t.Fatalf("runtime default after delete = %+v", deleted.Runtime.Config)
	}

	saved, err := bootstrap.LoadConfigFile(cfg.PersistPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if _, ok := saved.Providers["proxy"]; ok {
		t.Fatalf("saved providers still include proxy: %+v", saved.Providers["proxy"])
	}
	if _, ok := saved.Roles["writer"]; ok {
		t.Fatalf("saved writer route still exists: %+v", saved.Roles["writer"])
	}
	if containsString(saved.ModelAutoSwitch.FallbackBackends, "proxy") {
		t.Fatalf("saved fallback pool still includes deleted provider: %+v", saved.ModelAutoSwitch.FallbackBackends)
	}
	projectCfg, err := bootstrap.LoadConfigFile(ProjectConfigPath(legacyProject))
	if err != nil {
		t.Fatalf("LoadConfigFile project overlay: %v", err)
	}
	if _, ok := projectCfg.Providers["proxy"]; ok {
		t.Fatalf("project overlay retained deleted inherited provider: %+v", projectCfg.Providers["proxy"])
	}
	if projectCfg.Provider != "" || projectCfg.ModelName != "" {
		t.Fatalf("project overlay retained deleted default route: %s/%s", projectCfg.Provider, projectCfg.ModelName)
	}
	if _, ok := projectCfg.Roles["editor"]; ok {
		t.Fatalf("project overlay retained deleted editor route: %+v", projectCfg.Roles["editor"])
	}
	projectBytes, err := os.ReadFile(ProjectConfigPath(legacyProject))
	if err != nil {
		t.Fatalf("read project overlay: %v", err)
	}
	if strings.Contains(string(projectBytes), "sk-proxy") {
		t.Fatal("project overlay retained deleted inherited credential")
	}
	ownedCfg, err := bootstrap.LoadConfigFile(ProjectConfigPath(ownedProject))
	if err != nil {
		t.Fatalf("LoadConfigFile owned project: %v", err)
	}
	if ownedCfg.Providers["proxy"].APIKey != "sk-proxy" {
		t.Fatal("global deletion changed an explicitly project-owned provider")
	}
}

func TestGlobalModelDeleteRejectsCurrentDefault(t *testing.T) {
	cfg := testWebConfig(t)
	cfg.PersistPath = filepath.Join(testTempDir(t), "config.json")
	server := NewServer(cfg, assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/models", bytes.NewBufferString(`{"provider":"openai","model":"gpt-test"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("delete default status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := server.currentConfig().Provider + "/" + server.currentConfig().ModelName; got != "openai/gpt-test" {
		t.Fatalf("default changed after rejected delete: %s", got)
	}
}

func modelConfigHasProvider(models apiModelConfig, providerName, modelName string) bool {
	for _, provider := range models.Providers {
		if provider.Name != providerName {
			continue
		}
		for _, model := range provider.Models {
			if model == modelName {
				return true
			}
		}
	}
	return false
}

func findModelProvider(providers []apiModelProvider, name string) apiModelProvider {
	for _, provider := range providers {
		if provider.Name == name {
			return provider
		}
	}
	return apiModelProvider{}
}

func findModelRoute(routes []apiModelRoute, role string) apiModelRoute {
	for _, route := range routes {
		if route.Role == role {
			return route
		}
	}
	return apiModelRoute{}
}

func TestProjectModelConfigureExistingPassesRetryAndPoolSettings(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Existing Model Configure")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	body := `{"role":"default","original_provider":"openrouter","provider":"fixed-router","model":"model-b","label":"Fixed Router","type":"openai","base_url":"https://router.example/v1","network_disconnect_max_attempts":5,"auto_switch_candidate_pool":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model configure status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.configureOriginalProvider != "openrouter" || fake.configureProviderName != "fixed-router" || fake.configureProviderModel != "model-b" {
		t.Fatalf("configure args original=%q provider=%q model=%q", fake.configureOriginalProvider, fake.configureProviderName, fake.configureProviderModel)
	}
	if fake.configureProviderConfig.Label != "Fixed Router" || fake.configureProviderConfig.BaseURL != "https://router.example/v1" || fake.configureProviderConfig.Type != "openai" {
		t.Fatalf("configure provider config = %+v", fake.configureProviderConfig)
	}
	if fake.configureNetworkAttempts != 5 || !fake.configureAutoSwitchPool {
		t.Fatalf("configure retry/pool attempts=%d pool=%v", fake.configureNetworkAttempts, fake.configureAutoSwitchPool)
	}
}

func TestProjectModelAddExistingProviderUsesEmptyConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Existing Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"writer","provider":"openrouter","model":"new-model"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.configureProviderRole != "writer" || fake.configureProviderName != "openrouter" || fake.configureProviderModel != "new-model" {
		t.Fatalf("configure model args role=%q provider=%q model=%q", fake.configureProviderRole, fake.configureProviderName, fake.configureProviderModel)
	}
	if fake.configureProviderConfig.Type != "" || fake.configureProviderConfig.APIKey != "" || len(fake.configureProviderConfig.Models) != 0 {
		t.Fatalf("existing provider should use empty config: %+v", fake.configureProviderConfig)
	}
}

func TestProjectModelSwitchCanInheritDefault(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Inherit Agent Model")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/switch", bytes.NewBufferString(`{"role":"writer","inherit":true}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("inherit status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.clearModelRouteCalls != 1 || fake.clearModelRouteRole != "writer" {
		t.Fatalf("clear route calls=%d role=%q", fake.clearModelRouteCalls, fake.clearModelRouteRole)
	}
	if fake.switchCalls != 0 {
		t.Fatalf("inherit should not switch explicit model, switch calls=%d", fake.switchCalls)
	}
}

func TestProjectModelAddPresetPassesProviderConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Preset Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"default","provider":"anthropic","label":"Anthropic","template_provider":"anthropic","type":"anthropic","api_key":"sk-test","model":"claude-sonnet-4-5","use_proxy":false,"request_timeout_seconds":120,"connectivity_timeout_seconds":12}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.configureProviderName != "anthropic" || fake.configureProviderConfig.Type != "anthropic" || fake.configureProviderConfig.APIKey != "sk-test" {
		t.Fatalf("preset provider config = %+v provider=%q", fake.configureProviderConfig, fake.configureProviderName)
	}
	if fake.configureProviderConfig.Label != "Anthropic" || fake.configureProviderConfig.TemplateProvider != "anthropic" {
		t.Fatalf("preset provider metadata = %+v", fake.configureProviderConfig)
	}
	if fake.configureProviderConfig.UseProxy == nil || *fake.configureProviderConfig.UseProxy {
		t.Fatalf("preset use_proxy = %#v, want explicit false", fake.configureProviderConfig.UseProxy)
	}
	if fake.configureProviderConfig.RequestTimeoutSeconds != 120 || fake.configureProviderConfig.ConnectivityTimeoutSeconds != 12 {
		t.Fatalf("preset timeouts = %d/%d", fake.configureProviderConfig.RequestTimeoutSeconds, fake.configureProviderConfig.ConnectivityTimeoutSeconds)
	}
	if len(fake.configureProviderConfig.Models) != 0 || fake.configureProviderModel != "claude-sonnet-4-5" {
		t.Fatalf("preset model list = %+v model=%q", fake.configureProviderConfig.Models, fake.configureProviderModel)
	}
}

func TestProjectModelAddGrokOAuthPassesProviderConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Grok OAuth Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"writer","provider":"grok-oauth","type":"grok","auth":"grok_oauth","account_id":"work","model":"grok-4.3-latest","api":"chat","api_key":"should-not-forward"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.configureProviderName != "grok-oauth" || fake.configureProviderModel != "grok-4.3-latest" {
		t.Fatalf("grok add args provider=%q model=%q", fake.configureProviderName, fake.configureProviderModel)
	}
	if fake.configureProviderConfig.Type != "grok" || fake.configureProviderConfig.Auth != bootstrap.ProviderAuthGrokOAuth || fake.configureProviderConfig.AccountID != "work" {
		t.Fatalf("grok provider config = %+v", fake.configureProviderConfig)
	}
	if len(fake.configureProviderConfig.Models) != 0 {
		t.Fatalf("grok model list = %+v", fake.configureProviderConfig.Models)
	}
	if fake.configureProviderConfig.API != "" || fake.configureProviderConfig.APIKey != "" {
		t.Fatalf("grok_oauth config should not receive OpenAI api or api key: %+v", fake.configureProviderConfig)
	}
}

func TestProjectModelAddCodexAuthPassesProviderConfig(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Codex Auth Model Add")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+manifest.ID+"/models/add", bytes.NewBufferString(`{"role":"writer","provider":"codex-login","type":"openai","auth":"codex","model":"gpt-5.5","api":"chat","api_key":"should-not-forward","base_url":"","auth_file":"D:/codex/auth.json"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model add status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.configureProviderName != "codex-login" || fake.configureProviderModel != "gpt-5.5" {
		t.Fatalf("codex add args provider=%q model=%q", fake.configureProviderName, fake.configureProviderModel)
	}
	if fake.configureProviderConfig.Type != "openai" || fake.configureProviderConfig.Auth != bootstrap.ProviderAuthCodex {
		t.Fatalf("codex provider config = %+v", fake.configureProviderConfig)
	}
	if fake.configureProviderConfig.API != "responses" || fake.configureProviderConfig.APIKey != "" {
		t.Fatalf("codex auth config should force responses and clear api key: %+v", fake.configureProviderConfig)
	}
	if fake.configureProviderConfig.BaseURL != codexauth.DefaultBaseURL || fake.configureProviderConfig.AuthFile != "D:/codex/auth.json" {
		t.Fatalf("codex auth base/auth file = %+v", fake.configureProviderConfig)
	}
}

func TestProjectGrokLoginEndpointsUseHostFlow(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Grok OAuth Login")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.grokLoginStart = grokauth.LoginStart{
		Status:               grokauth.AuthStatus{AccountID: "work", AccountName: "Work", ActiveLogin: "pending"},
		AuthorizeURL:         "https://auth.x.ai/authorize",
		RedirectURI:          "http://127.0.0.1:56121/callback",
		ManualPasteSupported: true,
		LoopbackListening:    true,
	}
	fake.grokLoginPoll = grokauth.LoginPoll{
		Status:  grokauth.AuthStatus{AccountID: "work", AccountName: "Work", ActiveLogin: "pending"},
		State:   "pending",
		Message: "waiting",
	}
	fake.grokCompleteStatus = grokauth.AuthStatus{LoggedIn: true, Provider: grokauth.ProviderID, AccountID: "work", AccountName: "Work"}
	fake.grokStatus = fake.grokCompleteStatus

	var start struct {
		Login grokauth.LoginStart `json:"login"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/start", `{"account_id":"work","account_name":"Work"}`, &start)
	if fake.grokStartAccountID != "work" || fake.grokStartAccountName != "Work" {
		t.Fatalf("start args accountID=%q accountName=%q", fake.grokStartAccountID, fake.grokStartAccountName)
	}
	if start.Login.AuthorizeURL != "https://auth.x.ai/authorize" || !start.Login.ManualPasteSupported {
		t.Fatalf("start login = %+v", start.Login)
	}

	var poll struct {
		Login grokauth.LoginPoll `json:"login"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/poll", `{}`, &poll)
	if poll.Login.State != "pending" || poll.Login.Message != "waiting" {
		t.Fatalf("poll login = %+v", poll.Login)
	}

	var complete struct {
		Status grokauth.AuthStatus `json:"status"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/complete", `{"callback":"?code=abc&state=state"}`, &complete)
	if fake.grokCompleteCallback != "?code=abc&state=state" || !complete.Status.LoggedIn {
		t.Fatalf("complete callback=%q status=%+v", fake.grokCompleteCallback, complete.Status)
	}

	var status struct {
		Status grokauth.AuthStatus `json:"status"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/status", `{"account_id":"work"}`, &status)
	if fake.grokStatusAccountID != "work" || !status.Status.LoggedIn {
		t.Fatalf("status accountID=%q status=%+v", fake.grokStatusAccountID, status.Status)
	}
}

func TestProjectGrokLoginStartCanOpenAuthorizeURL(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Grok OAuth Browser Open")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := installFakeSession(t, server, manifest)
	fake.grokLoginStart = grokauth.LoginStart{
		Status:               grokauth.AuthStatus{AccountID: "work", AccountName: "Work", ActiveLogin: "pending"},
		AuthorizeURL:         "https://auth.x.ai/authorize",
		RedirectURI:          "http://127.0.0.1:56121/callback",
		ManualPasteSupported: true,
		LoopbackListening:    true,
	}

	previousOpenAuthBrowser := openAuthBrowser
	var openedURL string
	openAuthBrowser = func(rawURL string) error {
		openedURL = rawURL
		return nil
	}
	t.Cleanup(func() {
		openAuthBrowser = previousOpenAuthBrowser
	})

	var start struct {
		Login         grokauth.LoginStart `json:"login"`
		BrowserOpened bool                `json:"browser_opened"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/projects/"+manifest.ID+"/models/grok-login/start", `{"account_id":"work","account_name":"Work","open_browser":true}`, &start)
	if openedURL != "https://auth.x.ai/authorize" {
		t.Fatalf("opened URL = %q", openedURL)
	}
	if !start.BrowserOpened {
		t.Fatalf("browser_opened = false, response = %+v", start)
	}
}

func TestGrokLoginStartCanRunWithoutProject(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	previousStartGrokAuthLogin := startGrokAuthLogin
	startGrokAuthLogin = func(accountID, accountName string) (grokauth.LoginStart, error) {
		if accountID != "work" || accountName != "Work" {
			t.Fatalf("start args accountID=%q accountName=%q", accountID, accountName)
		}
		return grokauth.LoginStart{
			Status:               grokauth.AuthStatus{AccountID: accountID, AccountName: accountName, ActiveLogin: "pending"},
			AuthorizeURL:         "https://auth.x.ai/authorize",
			RedirectURI:          "http://127.0.0.1:56121/callback",
			ManualPasteSupported: true,
			LoopbackListening:    true,
		}, nil
	}
	previousOpenAuthBrowser := openAuthBrowser
	var openedURL string
	openAuthBrowser = func(rawURL string) error {
		openedURL = rawURL
		return nil
	}
	t.Cleanup(func() {
		startGrokAuthLogin = previousStartGrokAuthLogin
		openAuthBrowser = previousOpenAuthBrowser
	})

	var start struct {
		Login         grokauth.LoginStart `json:"login"`
		BrowserOpened bool                `json:"browser_opened"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/grok-login/start", `{"account_id":"work","account_name":"Work","open_browser":true}`, &start)
	if openedURL != "https://auth.x.ai/authorize" || !start.BrowserOpened {
		t.Fatalf("openedURL=%q browser_opened=%v", openedURL, start.BrowserOpened)
	}
}

func TestCodexAuthStatusEndpointReadsExistingLogin(t *testing.T) {
	authPath := filepath.Join(testTempDir(t), "auth.json")
	data, err := json.Marshal(map[string]any{
		"tokens": map[string]any{
			"access_token": "codex-access-token",
			"account_id":   "acct-test",
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(authPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()

	var status struct {
		Status codexauth.AuthStatus `json:"status"`
	}
	serveJSON(t, server.Handler(), http.MethodPost, "/api/models/codex-auth/status", fmt.Sprintf(`{"auth_file":%q}`, authPath), &status)
	if !status.Status.LoggedIn || status.Status.AccountID != "acct-test" || status.Status.AuthFileName != "auth.json" {
		t.Fatalf("codex status = %+v", status.Status)
	}
	if strings.Contains(status.Status.Message, authPath) {
		t.Fatalf("status message leaked auth path: %q", status.Status.Message)
	}
}

func TestCodexAuthUploadStoresManagedCredential(t *testing.T) {
	runtimeRoot := testTempDir(t)
	server := NewServer(testWebConfig(t), assets.Load("default"), runtimeRoot)
	defer server.Close()

	credential := []byte(`{"tokens":{"access_token":"uploaded-secret","account_id":"acct-uploaded"}}`)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("auth_file", "auth.json")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(credential); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/models/codex-auth/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "uploaded-secret") {
		t.Fatal("upload response leaked credential")
	}
	var response struct {
		AuthFile string               `json:"auth_file"`
		Status   codexauth.AuthStatus `json:"status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	wantPath := filepath.Join(runtimeRoot, "auth", "codex", "auth.json")
	if response.AuthFile != wantPath || !response.Status.LoggedIn || response.Status.AccountID != "acct-uploaded" {
		t.Fatalf("upload response = %+v, want path %q", response, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("managed auth file: %v", err)
	}
}

func serveJSON(t *testing.T, handler http.Handler, method, path, body string, out any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s status = %d body=%s", method, path, rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(out); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
}
