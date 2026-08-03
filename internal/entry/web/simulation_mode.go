package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

type projectSimulationModeRequest struct {
	SimulationMode string `json:"simulation_mode"`
	Mode           string `json:"mode"`
}

func (r projectSimulationModeRequest) value() string {
	if strings.TrimSpace(r.SimulationMode) != "" {
		return r.SimulationMode
	}
	return r.Mode
}

func (s *Server) handleProjectSimulationMode(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req projectSimulationModeRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode, err := bootstrap.NormalizeSimulationMode(req.value())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.SetProjectSimulationMode(id, mode)
	if err != nil {
		if errors.Is(err, ErrSessionActionInProgress) || errors.Is(err, ErrProjectSimulationLocked) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeProjectSessionError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":         manifest,
		"snapshot":        snapshot,
		"simulation_mode": mode,
		"running":         snapshot.IsRunning,
	})
}
