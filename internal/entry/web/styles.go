package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
)

type apiStylesResponse struct {
	Styles       []assets.StyleDescriptor `json:"styles"`
	DefaultStyle string                   `json:"default_style"`
}

func (s *Server) handleStyles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, apiStylesResponse{
		Styles:       assets.StyleCatalog(),
		DefaultStyle: s.defaultStyleID(),
	})
}

func (s *Server) handleProjectStyle(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Style string `json:"style"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	style, err := s.resolveStyleID(req.Style)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.SetProjectStyle(id, style)
	if err != nil {
		if errors.Is(err, ErrSessionActionInProgress) || errors.Is(err, ErrProjectStyleLocked) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeProjectSessionError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"style":    style,
		"running":  snapshot.IsRunning,
	})
}

func (s *Server) defaultStyleID() string {
	style := assets.NormalizeStyleID(s.currentConfig().Style)
	if assets.HasStyle(style) {
		return style
	}
	if assets.HasStyle("default") {
		return "default"
	}
	catalog := assets.StyleCatalog()
	if len(catalog) == 0 {
		return ""
	}
	return catalog[0].ID
}

func (s *Server) resolveStyleID(style string) (string, error) {
	style = strings.TrimSpace(style)
	if style == "" {
		style = s.defaultStyleID()
	}
	style = assets.NormalizeStyleID(style)
	if style == "" || !assets.HasStyle(style) {
		return "", fmt.Errorf("unknown style %q", style)
	}
	return style, nil
}
