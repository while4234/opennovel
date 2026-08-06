package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
)

const maxGlobalPromptRequestBytes = 512 << 10

type globalPromptDTO struct {
	Family     string   `json:"family"`
	Label      string   `json:"label"`
	Aliases    []string `json:"aliases"`
	Content    string   `json:"content"`
	Overridden bool     `json:"overridden"`
	Fallback   bool     `json:"fallback"`
}

type globalPromptRequest struct {
	Content string `json:"content"`
}

func (s *Server) handleGlobalPrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"prompts": globalPromptPayload(s.currentConfig()),
	})
}

func (s *Server) handleGlobalPrompt(w http.ResponseWriter, r *http.Request) {
	family := strings.TrimPrefix(r.URL.Path, "/api/models/global-prompts/")
	if family == "" || strings.Contains(family, "/") {
		writeError(w, http.StatusNotFound, "global prompt family not found")
		return
	}
	if _, ok := globalprompt.BuiltIn(family); !ok {
		writeError(w, http.StatusNotFound, "global prompt family not found")
		return
	}

	switch r.Method {
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, maxGlobalPromptRequestBytes)
		var request globalPromptRequest
		if err := decodeJSONBody(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := globalprompt.ValidateOverride(family, request.Content); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.saveGlobalPrompt(family, request.Content, false); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case http.MethodDelete:
		if err := s.saveGlobalPrompt(family, "", true); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"prompts": globalPromptPayload(s.currentConfig()),
	})
}

func (s *Server) saveGlobalPrompt(family, content string, reset bool) error {
	s.cfgMu.Lock()
	next := cloneWebConfig(s.cfg)
	if next.GlobalPrompts == nil {
		next.GlobalPrompts = make(map[string]string)
	}
	if reset {
		delete(next.GlobalPrompts, family)
	} else {
		next.GlobalPrompts[family] = content
	}
	if len(next.GlobalPrompts) == 0 {
		next.GlobalPrompts = nil
	}
	if err := globalprompt.ValidateOverrides(next.GlobalPrompts); err != nil {
		s.cfgMu.Unlock()
		return err
	}
	saver := s.configSaver
	if saver == nil {
		saver = saveWebConfig
	}
	if err := saver(next); err != nil {
		s.cfgMu.Unlock()
		return fmt.Errorf("save global prompts: %w", err)
	}
	s.cfg = cloneWebConfig(next)
	s.cfgMu.Unlock()

	if s.sessions != nil {
		s.sessions.SetConfig(next)
	}
	// The candidate was validated before persistence; this swap cannot fail.
	return globalprompt.ReplaceOverrides(next.GlobalPrompts)
}

func globalPromptPayload(cfg bootstrap.Config) []globalPromptDTO {
	prompts := make([]globalPromptDTO, 0, len(globalprompt.Families()))
	for _, definition := range globalprompt.Families() {
		content, overridden := cfg.GlobalPrompts[definition.Family]
		if !overridden {
			content, _ = globalprompt.BuiltIn(definition.Family)
		}
		prompts = append(prompts, globalPromptDTO{
			Family:     definition.Family,
			Label:      definition.Label,
			Aliases:    definition.Aliases,
			Content:    content,
			Overridden: overridden,
			Fallback:   definition.Fallback,
		})
	}
	return prompts
}
