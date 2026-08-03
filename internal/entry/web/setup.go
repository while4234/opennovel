package web

import (
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

type setupProviderOption struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Type           string `json:"type,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	APIKeyOptional bool   `json:"api_key_optional,omitempty"`
}

var webSetupProviderOptions = []setupProviderOption{
	{ID: "openrouter", Label: "OpenRouter", Type: "openai", BaseURL: "https://openrouter.ai/api/v1"},
	{ID: "anthropic", Label: "Anthropic", Type: "anthropic"},
	{ID: "gemini", Label: "Gemini", Type: "gemini"},
	{ID: "openai", Label: "OpenAI", Type: "openai"},
	{ID: "deepseek", Label: "DeepSeek", Type: "openai"},
	{ID: "qwen", Label: "Qwen", Type: "openai"},
	{ID: "glm", Label: "GLM", Type: "openai"},
	{ID: "grok", Label: "Grok", Type: "grok"},
	{ID: "ollama", Label: "Ollama", Type: "openai", BaseURL: "http://localhost:11434/v1", APIKeyOptional: true},
	{ID: "bedrock", Label: "Bedrock", Type: "bedrock", APIKeyOptional: true},
	{ID: "custom", Label: "自定义兼容服务", Type: "openai", APIKeyOptional: true},
}

func webSetupRequired(cfg bootstrap.Config) bool {
	if strings.TrimSpace(cfg.Provider) == "" || strings.TrimSpace(cfg.ModelName) == "" {
		return true
	}
	return cfg.ValidateBase() != nil
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := s.currentConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required": webSetupRequired(cfg),
		"providers":      webSetupProviderOptions,
		"config": map[string]any{
			"provider": cfg.Provider,
			"model":    cfg.ModelName,
			"style":    cfg.Style,
		},
	})
}

func (s *Server) handleSetupTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req modelProviderRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Role = "default"
	result, _ := host.TestProviderModelInConfig(r.Context(), s.currentConfig(), req.Role, req.Provider, req.providerConfig(), req.Model)
	result.Message = redactModelProviderMessage(result.Message, s.currentConfig(), req.providerConfig())
	writeJSON(w, http.StatusOK, map[string]any{"test": result})
}

func (s *Server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req modelProviderRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	selectAfterSave := true
	req.Role = "default"
	req.SelectAfterSave = &selectAfterSave
	models, runtime, err := s.configureGlobalProviderModel(r.Context(), req)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required": false,
		"models":         models,
		"runtime":        runtime,
	})
}
