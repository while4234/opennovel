package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/codexauth"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	"github.com/voocel/ainovel-cli/internal/host"
)

var modelConfigRoles = []string{"default", "coordinator", "architect", "character", "writer", "editor", "auditor"}

var globalModelStageLabels = map[string]string{
	bootstrap.StageCoCreate:          "首次/阶段共创",
	bootstrap.StageSourceAnalysis:    "资料分析",
	bootstrap.StageSkeleton:          "骨架规划",
	bootstrap.StageDetailOutline:     "详细提纲",
	bootstrap.StageWriting:           "正文创作",
	bootstrap.StageReview:            "审校与摘要",
	bootstrap.StageCharacterAnalysis: "角色分析",
	bootstrap.StageCharacterReview:   "角色审核",
}

const (
	maxCodexAuthUploadBytes    int64 = 1 << 20
	maxCodexAuthMultipartBytes       = maxCodexAuthUploadBytes + 64<<10
)

var openAuthBrowser = openBrowser
var startGrokAuthLogin = grokauth.StartLogin

type apiModelConfig struct {
	Providers                              []apiModelProvider `json:"providers"`
	Roles                                  []apiModelRoute    `json:"roles"`
	Stages                                 []apiModelRoute    `json:"stages,omitempty"`
	ThinkingLevels                         []string           `json:"thinking_levels"`
	ThinkingRule                           string             `json:"thinking_rule"`
	CoCreateTimeoutSeconds                 int                `json:"cocreate_timeout_seconds"`
	CoCreateMaxTokens                      int                `json:"cocreate_max_tokens"`
	StructureRepairMaxAttempts             int                `json:"structure_repair_max_attempts"`
	BudgetQualityMaxAttempts               int                `json:"budget_quality_max_attempts"`
	AdaptationOutlineAuditRetryMaxAttempts int                `json:"adaptation_outline_audit_retry_max_attempts"`
	ModelAutoSwitch                        apiModelAutoSwitch `json:"model_auto_switch"`
}

type apiModelAutoSwitch struct {
	Enabled              bool     `json:"enabled"`
	FallbackBackends     []string `json:"fallback_backends"`
	NetworkMaxAttempts   int      `json:"network_max_attempts"`
	ModelCallMaxAttempts int      `json:"model_call_max_attempts"`
}

type apiModelProvider struct {
	Name                         string            `json:"name"`
	Label                        string            `json:"label,omitempty"`
	TemplateProvider             string            `json:"template_provider,omitempty"`
	Disabled                     bool              `json:"disabled,omitempty"`
	Type                         string            `json:"type,omitempty"`
	Auth                         string            `json:"auth,omitempty"`
	AccountID                    string            `json:"account_id,omitempty"`
	AuthFileConfigured           bool              `json:"auth_file_configured,omitempty"`
	API                          string            `json:"api,omitempty"`
	BaseURL                      string            `json:"base_url,omitempty"`
	UseProxy                     *bool             `json:"use_proxy,omitempty"`
	RequestTimeoutSeconds        int               `json:"request_timeout_seconds,omitempty"`
	ConnectivityTimeoutSeconds   int               `json:"connectivity_timeout_seconds,omitempty"`
	APIKeyConfigured             bool              `json:"key_configured,omitempty"`
	Models                       []string          `json:"models"`
	ModelReasoningEfforts        map[string]string `json:"model_reasoning_efforts,omitempty"`
	NetworkDisconnectMaxAttempts int               `json:"network_disconnect_max_attempts,omitempty"`
	AutoSwitchCandidatePool      bool              `json:"auto_switch_candidate_pool,omitempty"`
}

type apiModelRoute struct {
	Role            string `json:"role"`
	Label           string `json:"label,omitempty"`
	FallbackRole    string `json:"fallback_role,omitempty"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Explicit        bool   `json:"explicit"`
	ReasoningEffort string `json:"reasoning_effort"`
}

type grokLoginStartRequest struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	OpenBrowser bool   `json:"open_browser"`
}

type grokLoginCompleteRequest struct {
	Callback string `json:"callback"`
}

type grokLoginStatusRequest struct {
	AccountID string `json:"account_id"`
}

type codexAuthStatusRequest struct {
	AuthFile string `json:"auth_file"`
}

type coCreateTimeoutRequest struct {
	Seconds        int `json:"seconds"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

type coCreateMaxTokensRequest struct {
	Tokens    int `json:"tokens"`
	MaxTokens int `json:"max_tokens"`
}

type retrySettingsRequest struct {
	ModelCallMaxAttempts                   int `json:"model_call_max_attempts"`
	NetworkDisconnectMaxAttempts           int `json:"network_disconnect_max_attempts"`
	StructureRepairMaxAttempts             int `json:"structure_repair_max_attempts"`
	BudgetQualityMaxAttempts               int `json:"budget_quality_max_attempts"`
	AdaptationOutlineAuditRetryMaxAttempts int `json:"adaptation_outline_audit_retry_max_attempts"`
}

func (r retrySettingsRequest) modelCallAttempts() int {
	if r.ModelCallMaxAttempts != 0 {
		return r.ModelCallMaxAttempts
	}
	return r.NetworkDisconnectMaxAttempts
}

type modelDeleteRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type modelProviderRequest struct {
	Role                         string  `json:"role"`
	OriginalProvider             string  `json:"original_provider"`
	Provider                     string  `json:"provider"`
	Model                        string  `json:"model"`
	ModelReasoningEffort         *string `json:"model_reasoning_effort"`
	Label                        string  `json:"label"`
	TemplateProvider             string  `json:"template_provider"`
	Disabled                     bool    `json:"disabled"`
	Type                         string  `json:"type"`
	Auth                         string  `json:"auth"`
	AccountID                    string  `json:"account_id"`
	AuthFile                     string  `json:"auth_file"`
	APIKey                       string  `json:"api_key"`
	BaseURL                      string  `json:"base_url"`
	API                          string  `json:"api"`
	UseProxy                     *bool   `json:"use_proxy"`
	RequestTimeoutSeconds        int     `json:"request_timeout_seconds"`
	ConnectivityTimeoutSeconds   int     `json:"connectivity_timeout_seconds"`
	NetworkDisconnectMaxAttempts int     `json:"network_disconnect_max_attempts"`
	AutoSwitchCandidatePool      bool    `json:"auto_switch_candidate_pool"`
	SelectAfterSave              *bool   `json:"select_after_save"`
}

func (r modelProviderRequest) providerConfig() bootstrap.ProviderConfig {
	pc := bootstrap.ProviderConfig{
		Label:                      strings.TrimSpace(r.Label),
		TemplateProvider:           strings.TrimSpace(r.TemplateProvider),
		Disabled:                   r.Disabled,
		Type:                       strings.TrimSpace(r.Type),
		Auth:                       strings.TrimSpace(r.Auth),
		AccountID:                  strings.TrimSpace(r.AccountID),
		AuthFile:                   strings.TrimSpace(r.AuthFile),
		APIKey:                     strings.TrimSpace(r.APIKey),
		BaseURL:                    strings.TrimSpace(r.BaseURL),
		API:                        strings.TrimSpace(r.API),
		RequestTimeoutSeconds:      r.RequestTimeoutSeconds,
		ConnectivityTimeoutSeconds: r.ConnectivityTimeoutSeconds,
	}
	if r.ModelReasoningEffort != nil {
		pc.ModelReasoningEfforts = map[string]string{
			strings.TrimSpace(r.Model): strings.ToLower(strings.TrimSpace(*r.ModelReasoningEffort)),
		}
	}
	if pc.UsesGrokOAuth() {
		pc.API = ""
		pc.APIKey = ""
	}
	if pc.UsesCodexAuth() {
		pc.AccountID = ""
		pc.API = "responses"
		pc.APIKey = ""
		if pc.BaseURL == "" {
			pc.BaseURL = codexauth.DefaultBaseURL
		}
	}
	if r.UseProxy != nil {
		useProxy := *r.UseProxy
		pc.UseProxy = &useProxy
	}
	return pc
}

func (r modelProviderRequest) providerModelUpdate() host.ProviderModelUpdate {
	return host.ProviderModelUpdate{
		Role:                    normalizeModelRole(r.Role),
		OriginalProvider:        strings.TrimSpace(r.OriginalProvider),
		Provider:                strings.TrimSpace(r.Provider),
		Model:                   strings.TrimSpace(r.Model),
		ProviderConfig:          r.providerConfig(),
		NetworkMaxAttempts:      r.NetworkDisconnectMaxAttempts,
		AutoSwitchCandidatePool: r.AutoSwitchCandidatePool,
		SelectAfterSave:         r.SelectAfterSave,
	}
}

func (r modelProviderRequest) providerModelSaveUpdate() host.ProviderModelUpdate {
	update := r.providerModelUpdate()
	selectAfterSave := false
	update.SelectAfterSave = &selectAfterSave
	return update
}

func (r coCreateTimeoutRequest) value() int {
	if r.TimeoutSeconds != 0 {
		return r.TimeoutSeconds
	}
	return r.Seconds
}

func (r coCreateMaxTokensRequest) value() int {
	if r.MaxTokens != 0 {
		return r.MaxTokens
	}
	return r.Tokens
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.currentConfig()
		writeJSON(w, http.StatusOK, map[string]any{
			"models": s.globalModelConfig(cfg),
		})
	case http.MethodDelete:
		s.handleModelDelete(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleDefaultModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	if provider == "" || model == "" {
		writeError(w, http.StatusBadRequest, "provider and model are required")
		return
	}

	cfg := s.currentConfig()
	if _, ok := cfg.Providers[provider]; !ok {
		writeError(w, http.StatusBadRequest, "provider is not configured")
		return
	}
	cfg, err := host.SelectProviderModelInConfig(cfg, "default", provider, model)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	if err := cfg.ValidateBase(); err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	if err := saveWebConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setCurrentConfig(cfg)
	s.refreshProjectsAfterGlobalModelSettings(cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  s.globalModelConfig(cfg),
		"runtime": s.runtimePayload(cfg),
	})
}

func (s *Server) handleModelSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Inherit  bool   `json:"inherit"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var models apiModelConfig
	var runtime map[string]any
	var err error
	if req.Inherit {
		models, runtime, err = s.clearGlobalModelRoute(req.Role)
	} else {
		models, runtime, err = s.addGlobalProviderModel(req.Role, req.Provider, req.Model, bootstrap.ProviderConfig{})
	}
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  models,
		"runtime": runtime,
	})
}

func (s *Server) handleGlobalModelThinking(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role  string `json:"role"`
		Level string `json:"level"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	models, runtime, err := s.setGlobalModelThinking(req.Role, req.Level)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "runtime": runtime})
}

func (s *Server) handleCoCreateTimeout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req coCreateTimeoutRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	seconds, err := bootstrap.NormalizeCoCreateTimeoutSeconds(req.value())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.currentConfig()
	cfg.CoCreateTimeoutSeconds = seconds
	if err := cfg.ValidateBase(); err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	if err := saveWebConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setCurrentConfig(cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  s.globalModelConfig(cfg),
		"runtime": s.runtimePayload(cfg),
	})
}

func (s *Server) handleCoCreateMaxTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req coCreateMaxTokensRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tokens, err := bootstrap.NormalizeCoCreateMaxTokens(req.value())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.currentConfig()
	cfg.CoCreateMaxTokens = tokens
	if err := cfg.ValidateBase(); err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	if err := saveWebConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setCurrentConfig(cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  s.globalModelConfig(cfg),
		"runtime": s.runtimePayload(cfg),
	})
}

func (s *Server) handleRetrySettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req retrySettingsRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	modelCallAttempts, err := bootstrap.NormalizeRuntimeNetworkMaxAttempts(req.modelCallAttempts())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repairAttempts, err := bootstrap.NormalizeStructureRepairMaxAttempts(req.StructureRepairMaxAttempts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	budgetAttempts, err := bootstrap.NormalizeBudgetQualityMaxAttempts(req.BudgetQualityMaxAttempts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	auditAttempts, err := bootstrap.NormalizeAdaptationOutlineAuditRetryMaxAttempts(req.AdaptationOutlineAuditRetryMaxAttempts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.currentConfig()
	cfg.ModelAutoSwitch.NetworkMaxAttempts = modelCallAttempts
	cfg.StructureRepairMaxAttempts = repairAttempts
	cfg.BudgetQualityMaxAttempts = budgetAttempts
	cfg.AdaptationOutlineAuditRetryMaxAttempts = auditAttempts
	if err := cfg.ValidateBase(); err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	if err := saveWebConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setCurrentConfig(cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  s.globalModelConfig(cfg),
		"runtime": s.runtimePayload(cfg),
	})
}

func (s *Server) handleModelAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req modelProviderRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	models, runtime, err := s.configureGlobalProviderModel(r.Context(), req)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  models,
		"runtime": runtime,
	})
}

func (s *Server) handleModelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req modelProviderRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.currentConfig()
	pc := req.providerConfig()
	var result host.ProviderModelTestResult
	if strings.TrimSpace(req.OriginalProvider) != "" {
		result, _ = host.TestConfiguredProviderModelInConfig(r.Context(), cfg, req.providerModelUpdate())
	} else {
		result, _ = host.TestProviderModelInConfig(r.Context(), cfg, req.Role, req.Provider, pc, req.Model)
	}
	result.Message = redactModelProviderMessage(result.Message, cfg, pc)
	writeJSON(w, http.StatusOK, map[string]any{
		"test": result,
	})
}

func (s *Server) handleModelDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req modelProviderRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.currentConfig()
	pc := req.providerConfig()
	var result host.ProviderModelDiscoveryResult
	if strings.TrimSpace(req.OriginalProvider) != "" {
		result, _ = host.DiscoverConfiguredProviderModelsInConfig(r.Context(), cfg, req.providerModelUpdate())
	} else {
		result, _ = host.DiscoverProviderModelsInConfig(r.Context(), cfg, req.Provider, pc, req.Model)
	}
	result.Message = redactModelProviderMessage(result.Message, cfg, pc)
	writeJSON(w, http.StatusOK, map[string]any{
		"discovery": result,
	})
}

func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	var req modelDeleteRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.currentConfig()
	next, err := host.RemoveProviderModelFromConfig(cfg, req.Provider, req.Model)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	if err := saveWebConfig(next); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setCurrentConfig(next)
	s.refreshProjectsAfterGlobalModelDelete(cfg, next, req.Provider, req.Model)
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  s.globalModelConfig(next),
		"runtime": s.runtimePayload(next),
	})
}

func (s *Server) refreshProjectsAfterGlobalModelDelete(previous, next bootstrap.Config, provider, model string) {
	if s.store != nil {
		updated, err := s.store.RemoveInheritedProjectProviderModel(previous, next, provider, model)
		if err != nil {
			slog.Warn("remove inherited model from project configs failed", "module", "web", "provider", provider, "model", model, "err", err)
		} else if updated > 0 {
			slog.Info("removed inherited model from project configs", "module", "web", "provider", provider, "model", model, "projects", updated)
		}
	}
	if s.sessions != nil {
		if err := s.sessions.SyncInheritedProviderModelRemovalFromGlobal(next, provider, model); err != nil {
			slog.Warn("remove inherited model from active projects failed", "module", "web", "provider", provider, "model", model, "err", err)
		}
	}
}

func (s *Server) addGlobalProviderModel(role, provider, model string, pc bootstrap.ProviderConfig) (apiModelConfig, map[string]any, error) {
	role = normalizeModelRole(role)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return apiModelConfig{}, nil, fmt.Errorf("provider and model are required")
	}
	if !globalModelRoleAllowed(role) {
		return apiModelConfig{}, nil, fmt.Errorf("unknown role %q", role)
	}

	cfg := s.currentConfig()
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]bootstrap.ProviderConfig)
	}
	if providerConfigRequestIsEmpty(pc) {
		if _, ok := cfg.Providers[provider]; !ok {
			return apiModelConfig{}, nil, fmt.Errorf("provider %q is not configured", provider)
		}
	} else {
		pc.Models = []string{model}
		cfg.Providers[provider] = pc
	}
	cfg, err := host.SelectProviderModelInConfig(cfg, role, provider, model)
	if err != nil {
		return apiModelConfig{}, nil, err
	}
	if err := cfg.ValidateBase(); err != nil {
		return apiModelConfig{}, nil, err
	}
	if err := saveWebConfig(cfg); err != nil {
		return apiModelConfig{}, nil, err
	}
	s.setCurrentConfig(cfg)
	s.refreshProjectsAfterGlobalModelSettings(cfg)
	return s.globalModelConfig(cfg), s.runtimePayload(cfg), nil
}

func (s *Server) clearGlobalModelRoute(role string) (apiModelConfig, map[string]any, error) {
	role = normalizeModelRole(role)
	if role == "" || role == "default" {
		return apiModelConfig{}, nil, fmt.Errorf("default model route cannot inherit")
	}
	if !globalModelRoleAllowed(role) {
		return apiModelConfig{}, nil, fmt.Errorf("unknown role %q", role)
	}
	cfg := s.currentConfig()
	if cfg.Roles != nil {
		rc := cfg.Roles[role]
		rc.Provider = ""
		rc.Model = ""
		if globalRoleConfigEmpty(rc) {
			delete(cfg.Roles, role)
		} else {
			cfg.Roles[role] = rc
		}
	}
	if err := cfg.ValidateBase(); err != nil {
		return apiModelConfig{}, nil, err
	}
	if err := saveWebConfig(cfg); err != nil {
		return apiModelConfig{}, nil, err
	}
	s.setCurrentConfig(cfg)
	s.refreshProjectsAfterGlobalModelSettings(cfg)
	return s.globalModelConfig(cfg), s.runtimePayload(cfg), nil
}

func (s *Server) setGlobalModelThinking(role, level string) (apiModelConfig, map[string]any, error) {
	role = normalizeModelRole(role)
	if !globalModelRoleAllowed(role) {
		return apiModelConfig{}, nil, fmt.Errorf("unknown role %q", role)
	}
	parsed, err := agents.ParseThinkingLevel(level)
	if err != nil {
		return apiModelConfig{}, nil, err
	}
	cfg := s.currentConfig()
	if role == "default" {
		cfg.ReasoningEffort = string(parsed)
	} else {
		if cfg.Roles == nil {
			cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := cfg.Roles[role]
		rc.ReasoningEffort = string(parsed)
		if globalRoleConfigEmpty(rc) {
			delete(cfg.Roles, role)
		} else {
			cfg.Roles[role] = rc
		}
	}
	if err := cfg.ValidateBase(); err != nil {
		return apiModelConfig{}, nil, err
	}
	if err := saveWebConfig(cfg); err != nil {
		return apiModelConfig{}, nil, err
	}
	s.setCurrentConfig(cfg)
	s.refreshProjectsAfterGlobalModelSettings(cfg)
	return s.globalModelConfig(cfg), s.runtimePayload(cfg), nil
}

func globalRoleConfigEmpty(rc bootstrap.RoleConfig) bool {
	return rc.Provider == "" && rc.Model == "" && rc.ReasoningEffort == "" && len(rc.Fallbacks) == 0
}

func (s *Server) addGlobalProviderModelWithProbe(ctx context.Context, role, provider, model string, pc bootstrap.ProviderConfig) (apiModelConfig, map[string]any, error) {
	role = normalizeModelRole(role)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return apiModelConfig{}, nil, fmt.Errorf("provider and model are required")
	}
	if !globalModelRoleAllowed(role) {
		return apiModelConfig{}, nil, fmt.Errorf("unknown role %q", role)
	}
	next, err := host.AddProviderModelToConfig(ctx, s.currentConfig(), role, provider, pc, model)
	if err != nil {
		return apiModelConfig{}, nil, err
	}
	if err := saveWebConfig(next); err != nil {
		return apiModelConfig{}, nil, err
	}
	s.setCurrentConfig(next)
	return s.globalModelConfig(next), s.runtimePayload(next), nil
}

func (s *Server) configureGlobalProviderModel(ctx context.Context, req modelProviderRequest) (apiModelConfig, map[string]any, error) {
	update := req.providerModelSaveUpdate()
	next, err := host.ConfigureProviderModelInConfig(ctx, s.currentConfig(), update)
	if err != nil {
		return apiModelConfig{}, nil, err
	}
	if err := saveWebConfig(next); err != nil {
		return apiModelConfig{}, nil, err
	}
	s.setCurrentConfig(next)
	s.refreshProjectsAfterGlobalProviderEdit(next, update.OriginalProvider, update.Provider)
	return s.globalModelConfig(next), s.runtimePayload(next), nil
}

func (s *Server) refreshProjectsAfterGlobalModelSettings(cfg bootstrap.Config) {
	if s.store != nil {
		updated, err := s.store.RefreshProjectModelSettings(cfg)
		if err != nil {
			slog.Warn("refresh project model settings failed", "module", "web", "err", err)
		} else if updated > 0 {
			slog.Info("refreshed project model settings", "module", "web", "projects", updated)
		}
	}
	if s.sessions != nil {
		if err := s.sessions.SyncModelSettingsFromGlobal(cfg); err != nil {
			slog.Warn("refresh active project model settings failed", "module", "web", "err", err)
		}
	}
}

func (s *Server) refreshProjectsAfterGlobalProviderEdit(cfg bootstrap.Config, originalProvider, provider string) {
	originalProvider = strings.TrimSpace(originalProvider)
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return
	}
	if originalProvider == "" {
		originalProvider = provider
	}
	if s.store != nil {
		updated, err := s.store.RefreshProjectProviderReferences(cfg, originalProvider, provider)
		if err != nil {
			slog.Warn("refresh project provider references failed", "module", "web", "provider", provider, "err", err)
		} else if updated > 0 {
			slog.Info("refreshed project provider references", "module", "web", "provider", provider, "projects", updated)
		}
	}
	if s.sessions != nil {
		if err := s.sessions.SyncInheritedProviderFromGlobal(cfg, originalProvider, provider); err != nil {
			slog.Warn("refresh active project providers failed", "module", "web", "provider", provider, "err", err)
		}
	}
}

func globalModelRoleAllowed(role string) bool {
	for _, known := range modelConfigRoles {
		if role == known {
			return true
		}
	}
	for _, stage := range bootstrap.KnownModelStages {
		if role == bootstrap.StageRouteKey(stage) {
			return true
		}
	}
	return false
}

func (s *Server) globalModelConfig(cfg bootstrap.Config) apiModelConfig {
	providers := configuredModelProviders(cfg)
	outProviders := make([]apiModelProvider, 0, len(providers))
	for _, provider := range providers {
		outProviders = append(outProviders, apiProviderFromConfig(provider, cfg.Providers[provider], cfg.CandidateModels(provider), cfg.ModelAutoSwitch))
	}
	roles := make([]apiModelRoute, 0, len(modelConfigRoles))
	for _, role := range modelConfigRoles {
		normalized := normalizeModelRole(role)
		provider := cfg.Provider
		model := cfg.ModelName
		explicit := normalized == "default"
		if normalized != "default" {
			if rc, ok := cfg.Roles[normalized]; ok && rc.Provider != "" && rc.Model != "" {
				provider = rc.Provider
				model = rc.Model
				explicit = true
			}
		}
		roles = append(roles, apiModelRoute{
			Role:            normalized,
			Provider:        provider,
			Model:           model,
			Explicit:        explicit,
			ReasoningEffort: cfg.ResolveReasoningEffort(normalized),
		})
	}
	stages := make([]apiModelRoute, 0, len(bootstrap.KnownModelStages))
	for _, stage := range bootstrap.KnownModelStages {
		key := bootstrap.StageRouteKey(stage)
		fallbackRole := bootstrap.StageFallbackRole(stage)
		provider, model, explicit := globalModelSelection(cfg, key, fallbackRole)
		stages = append(stages, apiModelRoute{
			Role:            key,
			Label:           globalModelStageLabels[stage],
			FallbackRole:    fallbackRole,
			Provider:        provider,
			Model:           model,
			Explicit:        explicit,
			ReasoningEffort: cfg.ResolveReasoningEffort(key),
		})
	}
	return apiModelConfig{
		Providers:                              outProviders,
		Roles:                                  roles,
		Stages:                                 stages,
		ThinkingLevels:                         []string{"", "off", "low", "medium", "high", "xhigh", "max"},
		ThinkingRule:                           "default applies to coordinator, architect, character, writer, editor, and auditor unless that agent has its own model or reasoning setting",
		CoCreateTimeoutSeconds:                 cfg.EffectiveCoCreateTimeoutSeconds(),
		CoCreateMaxTokens:                      cfg.EffectiveCoCreateMaxTokens(),
		StructureRepairMaxAttempts:             cfg.EffectiveStructureRepairMaxAttempts(),
		BudgetQualityMaxAttempts:               cfg.EffectiveBudgetQualityMaxAttempts(),
		AdaptationOutlineAuditRetryMaxAttempts: cfg.EffectiveAdaptationOutlineAuditRetryMaxAttempts(),
		ModelAutoSwitch:                        apiModelAutoSwitchFromConfig(cfg.ModelAutoSwitch),
	}
}

func globalModelSelection(cfg bootstrap.Config, role, fallbackRole string) (string, string, bool) {
	if rc, ok := cfg.Roles[role]; ok && strings.TrimSpace(rc.Provider) != "" && strings.TrimSpace(rc.Model) != "" {
		return strings.TrimSpace(rc.Provider), strings.TrimSpace(rc.Model), true
	}
	if rc, ok := cfg.Roles[fallbackRole]; ok && strings.TrimSpace(rc.Provider) != "" && strings.TrimSpace(rc.Model) != "" {
		return strings.TrimSpace(rc.Provider), strings.TrimSpace(rc.Model), false
	}
	return strings.TrimSpace(cfg.Provider), strings.TrimSpace(cfg.ModelName), false
}

func apiProviderFromConfig(name string, pc bootstrap.ProviderConfig, models []string, autoSwitch bootstrap.ModelAutoSwitchConfig) apiModelProvider {
	useProxy := cloneBoolPtr(pc.UseProxy)
	return apiModelProvider{
		Name:                         name,
		Label:                        pc.Label,
		TemplateProvider:             pc.TemplateProvider,
		Disabled:                     pc.Disabled,
		Type:                         pc.Type,
		Auth:                         pc.Auth,
		AccountID:                    pc.AccountID,
		AuthFileConfigured:           strings.TrimSpace(pc.AuthFile) != "",
		API:                          pc.API,
		BaseURL:                      pc.BaseURL,
		UseProxy:                     useProxy,
		RequestTimeoutSeconds:        pc.RequestTimeoutSeconds,
		ConnectivityTimeoutSeconds:   pc.ConnectivityTimeoutSeconds,
		APIKeyConfigured:             strings.TrimSpace(pc.APIKey) != "",
		Models:                       append([]string(nil), models...),
		ModelReasoningEfforts:        cloneStringMap(pc.ModelReasoningEfforts),
		NetworkDisconnectMaxAttempts: autoSwitch.EffectiveNetworkMaxAttempts(),
		AutoSwitchCandidatePool:      modelAutoSwitchHasProvider(autoSwitch, name),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func apiModelAutoSwitchFromConfig(cfg bootstrap.ModelAutoSwitchConfig) apiModelAutoSwitch {
	return apiModelAutoSwitch{
		Enabled:              cfg.IsEnabled(),
		FallbackBackends:     append([]string(nil), cfg.FallbackBackends...),
		NetworkMaxAttempts:   cfg.EffectiveNetworkMaxAttempts(),
		ModelCallMaxAttempts: cfg.EffectiveNetworkMaxAttempts(),
	}
}

func modelAutoSwitchHasProvider(cfg bootstrap.ModelAutoSwitchConfig, provider string) bool {
	provider = strings.TrimSpace(provider)
	for _, candidate := range cfg.FallbackBackends {
		if strings.TrimSpace(candidate) == provider {
			return true
		}
	}
	return false
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func configuredModelProviders(cfg bootstrap.Config) []string {
	providers := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers
}

type configuredModelRoute struct {
	provider string
	model    string
}

func recommendedNewProjectStageRoutes(cfg bootstrap.Config) map[string]bootstrap.RoleConfig {
	grok := findConfiguredModelRoute(cfg, isGrok45Model, bootstrap.StageRouteKey(bootstrap.StageSkeleton), "architect", "editor")
	if grok.model == "" {
		return nil
	}

	routes := make(map[string]bootstrap.RoleConfig, len(bootstrap.KnownModelStages))
	for _, stage := range bootstrap.KnownModelStages {
		if configured, ok := cfg.Roles[bootstrap.StageRouteKey(stage)]; ok &&
			providerCanUseModel(cfg, configured.Provider, configured.Model) {
			routes[stage] = configured
			continue
		}
		routes[stage] = bootstrap.RoleConfig{
			Provider:        grok.provider,
			Model:           grok.model,
			ReasoningEffort: "xhigh",
		}
	}
	return routes
}

func findConfiguredModelRoute(cfg bootstrap.Config, matches func(string) bool, preferredRoles ...string) configuredModelRoute {
	for _, role := range preferredRoles {
		route, ok := cfg.Roles[role]
		if ok && matches(route.Model) && providerCanUseModel(cfg, route.Provider, route.Model) {
			return configuredModelRoute{provider: route.Provider, model: route.Model}
		}
	}
	if matches(cfg.ModelName) && providerCanUseModel(cfg, cfg.Provider, cfg.ModelName) {
		return configuredModelRoute{provider: cfg.Provider, model: cfg.ModelName}
	}
	for _, provider := range configuredModelProviders(cfg) {
		for _, model := range cfg.CandidateModels(provider) {
			if matches(model) && providerCanUseModel(cfg, provider, model) {
				return configuredModelRoute{provider: provider, model: model}
			}
		}
	}
	return configuredModelRoute{}
}

func providerCanUseModel(cfg bootstrap.Config, provider, model string) bool {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	pc, ok := cfg.Providers[provider]
	return ok && !pc.Disabled && provider != "" && model != ""
}

func isGrok45Model(model string) bool {
	normalized := compactModelName(model)
	return strings.Contains(normalized, "grok45")
}

func compactModelName(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.NewReplacer("-", "", "_", "", ".", "", "/", "").Replace(model)
}

func saveWebConfig(cfg bootstrap.Config) error {
	path := strings.TrimSpace(cfg.PersistPath)
	if path == "" {
		path = bootstrap.DefaultConfigPath()
	}
	if path == "" {
		return nil
	}
	return bootstrap.SaveConfig(path, cfg)
}

func (s *Server) handleProjectModels(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		session, manifest, err := s.sessions.Open(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"project": manifest,
			"models":  session.ModelConfig(),
		})
	case http.MethodDelete:
		s.handleProjectModelDelete(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleProjectModelSwitch(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Inherit  bool   `json:"inherit"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	var models apiModelConfig
	if req.Inherit {
		models, err = session.ClearModelRoute(req.Role)
	} else {
		models, err = session.SwitchModel(req.Role, req.Provider, req.Model)
	}
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectModelThinking(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role  string `json:"role"`
		Level string `json:"level"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.SetRoleThinking(req.Role, req.Level)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectCoCreateTimeout(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req coCreateTimeoutRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.SetCoCreateTimeoutSeconds(req.value())
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectCoCreateMaxTokens(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req coCreateMaxTokensRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.SetCoCreateMaxTokens(req.value())
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectRetrySettings(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req retrySettingsRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.SetRetrySettings(req.modelCallAttempts(), req.StructureRepairMaxAttempts, req.BudgetQualityMaxAttempts, req.AdaptationOutlineAuditRetryMaxAttempts)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectModelAdd(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req modelProviderRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.ConfigureProviderModel(r.Context(), req)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectModelTest(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req modelProviderRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	result, _ := session.TestProviderModel(r.Context(), req)
	result.Message = redactModelProviderMessage(result.Message, s.currentConfig(), req.providerConfig())
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"test":    result,
	})
}

func (s *Server) handleProjectModelDiscover(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req modelProviderRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	result, _ := session.DiscoverProviderModels(r.Context(), req)
	result.Message = redactModelProviderMessage(result.Message, s.currentConfig(), req.providerConfig())
	writeJSON(w, http.StatusOK, map[string]any{
		"project":   manifest,
		"discovery": result,
	})
}

func (s *Server) handleProjectModelDelete(w http.ResponseWriter, r *http.Request, id string) {
	var req modelDeleteRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.RemoveProviderModel(req.Provider, req.Model)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func providerConfigRequestIsEmpty(pc bootstrap.ProviderConfig) bool {
	return pc.Type == "" &&
		pc.Label == "" &&
		pc.TemplateProvider == "" &&
		!pc.Disabled &&
		pc.UseProxy == nil &&
		pc.RequestTimeoutSeconds == 0 &&
		pc.ConnectivityTimeoutSeconds == 0 &&
		pc.Auth == "" &&
		pc.AccountID == "" &&
		pc.AuthFile == "" &&
		pc.API == "" &&
		pc.APIKey == "" &&
		pc.BaseURL == "" &&
		len(pc.Models) == 0 &&
		len(pc.ExtraBody) == 0 &&
		len(pc.Extra) == 0
}

func redactModelProviderMessage(message string, cfg bootstrap.Config, pc bootstrap.ProviderConfig) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return message
	}
	secrets := []string{
		strings.TrimSpace(pc.APIKey),
		strings.TrimSpace(pc.AuthFile),
		strings.TrimSpace(cfg.Proxy),
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "<redacted>")
	}
	return message
}

func (s *Server) handleProjectModelAddOpenAICompatible(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
		API      string `json:"api"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	models, err := session.AddOpenAICompatibleModel(req.Role, req.Provider, req.Model, req.BaseURL, req.APIKey, req.API)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"models":   models,
		"snapshot": session.Snapshot(),
	})
}

func (s *Server) handleProjectGrokLoginStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req grokLoginStartRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	login, err := session.StartGrokLogin(defaultGrokAccountID(req.AccountID), req.AccountName)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	response := map[string]any{
		"project": manifest,
		"login":   login,
	}
	addGrokBrowserOpenResult(response, login.AuthorizeURL, req.OpenBrowser)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleProjectGrokLoginPoll(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	login, err := session.PollGrokLogin()
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"login":   login,
	})
}

func (s *Server) handleProjectGrokLoginComplete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req grokLoginCompleteRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Callback) == "" {
		writeError(w, http.StatusBadRequest, "callback is required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	status, err := session.CompleteGrokLogin(req.Callback)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"status":  status,
	})
}

func (s *Server) handleProjectGrokLoginStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	accountID := r.URL.Query().Get("account_id")
	if r.Method == http.MethodPost {
		var req grokLoginStatusRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		accountID = req.AccountID
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"status":  session.GrokLoginStatus(defaultGrokAccountID(accountID)),
	})
}

func (s *Server) handleProjectCodexAuthStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authFile, err := codexAuthFileFromStatusRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"status":  codexauth.GetStatus(authFile),
	})
}

func (s *Server) handleGrokLogin(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/api/models/grok-login/")
	switch action {
	case "start":
		s.handleGrokLoginStart(w, r)
	case "poll":
		s.handleGrokLoginPoll(w, r)
	case "complete":
		s.handleGrokLoginComplete(w, r)
	case "status":
		s.handleGrokLoginStatus(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleGrokLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req grokLoginStartRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	login, err := startGrokAuthLogin(defaultGrokAccountID(req.AccountID), req.AccountName)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	response := map[string]any{"login": login}
	addGrokBrowserOpenResult(response, login.AuthorizeURL, req.OpenBrowser)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleGrokLoginPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	login, err := grokauth.PollLogin(r.Context())
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": login})
}

func (s *Server) handleGrokLoginComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req grokLoginCompleteRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Callback) == "" {
		writeError(w, http.StatusBadRequest, "callback is required")
		return
	}
	status, err := grokauth.CompleteLogin(r.Context(), req.Callback)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (s *Server) handleGrokLoginStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	accountID := r.URL.Query().Get("account_id")
	if r.Method == http.MethodPost {
		var req grokLoginStatusRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		accountID = req.AccountID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": grokauth.GetStatus(defaultGrokAccountID(accountID)),
	})
}

func (s *Server) handleCodexAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authFile, err := codexAuthFileFromStatusRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": codexauth.GetStatus(authFile),
	})
}

func (s *Server) handleCodexAuthUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	headers, cleanup, err := parseMultipartFiles(w, r, maxCodexAuthMultipartBytes)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(headers) != 1 {
		writeError(w, http.StatusBadRequest, "exactly one auth.json file is required")
		return
	}
	name, err := sanitizeUploadedFilename(rawMultipartFilename(headers[0]), profileUploadExtensions)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	data, err := readMultipartFile(headers[0], maxCodexAuthUploadBytes)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	target := filepath.Join(s.runtimeRoot, "auth", "codex", "auth.json")
	status, err := codexauth.InstallAuthFile(data, target)
	if err != nil {
		var authErr *codexauth.AuthError
		if errors.As(err, &authErr) {
			writeError(w, http.StatusBadRequest, authErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "save Codex auth file failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_file": target,
		"file": map[string]any{
			"name": name,
			"size": len(data),
		},
		"status": status,
	})
}

func codexAuthFileFromStatusRequest(r *http.Request) (string, error) {
	authFile := ""
	if r.Method == http.MethodPost {
		var req codexAuthStatusRequest
		if err := decodeJSONBody(r, &req); err != nil {
			return "", err
		}
		authFile = req.AuthFile
	}
	return strings.TrimSpace(authFile), nil
}

func addGrokBrowserOpenResult(response map[string]any, authorizeURL string, openBrowser bool) {
	if !openBrowser {
		return
	}
	authorizeURL = strings.TrimSpace(authorizeURL)
	if authorizeURL == "" {
		response["browser_opened"] = false
		response["browser_open_error"] = "Grok authorize URL is empty"
		return
	}
	if err := openAuthBrowser(authorizeURL); err != nil {
		response["browser_opened"] = false
		response["browser_open_error"] = err.Error()
		return
	}
	response["browser_opened"] = true
}

func defaultGrokAccountID(value string) string {
	if strings.TrimSpace(value) == "" {
		return grokauth.DefaultAccountID
	}
	return value
}

func (s *Server) handleProjectUsage(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"usage":    apiUsageFromSnapshot(snapshot),
	})
}

func (s *Server) handleProjectBackendStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"backend": session.BackendStatus(false),
	})
}

func (s *Server) handleProjectBackendTest(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"backend": session.BackendStatus(true),
	})
}

func normalizeModelRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return "default"
	}
	return role
}

type apiUsageSummary struct {
	Overall               apiUsageTotals        `json:"overall"`
	ByRole                []host.AgentCacheStat `json:"by_role"`
	ByModel               []host.AgentCacheStat `json:"by_model"`
	MissingAssistantUsage int                   `json:"missing_assistant_usage"`
	UpdatedAt             time.Time             `json:"updated_at"`
}

type apiUsageTotals struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	SavedUSD         float64 `json:"saved_usd"`
	CacheCapable     bool    `json:"cache_capable"`
	RecentCacheRead  int     `json:"recent_cache_read_tokens"`
	RecentInput      int     `json:"recent_input_tokens"`
	RecentSamples    int     `json:"recent_samples"`
}

func apiUsageFromSnapshot(snapshot host.UISnapshot) apiUsageSummary {
	return apiUsageSummary{
		Overall: apiUsageTotals{
			InputTokens:      snapshot.TotalInputTokens,
			OutputTokens:     snapshot.TotalOutputTokens,
			CacheReadTokens:  snapshot.TotalCacheReadTokens,
			CacheWriteTokens: snapshot.TotalCacheWriteTokens,
			CostUSD:          snapshot.TotalCostUSD,
			SavedUSD:         snapshot.TotalSavedUSD,
			CacheCapable:     snapshot.OverallCacheCapable,
			RecentCacheRead:  snapshot.OverallRecentCacheRead,
			RecentInput:      snapshot.OverallRecentInput,
			RecentSamples:    snapshot.OverallRecentSamples,
		},
		ByRole:                snapshot.CachePerAgent,
		ByModel:               snapshot.CachePerModel,
		MissingAssistantUsage: snapshot.MissingAssistantUsage,
		UpdatedAt:             time.Now().UTC(),
	}
}
