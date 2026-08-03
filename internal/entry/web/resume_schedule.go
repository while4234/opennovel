package web

import (
	"net/http"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

type resumeScheduleRequest struct {
	DailyTimes []string `json:"daily_times"`
	Timezone   string   `json:"timezone"`
}

type projectResumeScheduleRequest struct {
	Enabled *bool `json:"enabled"`
}

func (s *Server) handleResumeSchedule(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.currentConfig()
		normalized, err := bootstrap.NormalizeResumeSchedule(cfg.ResumeSchedule)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		response := map[string]any{"schedule": normalized}
		if s.resumeScheduler != nil {
			status := s.resumeScheduler.Status()
			response["next_trigger_at"] = status.NextTriggerAt
			response["last_batch"] = status.LastBatch
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPut:
		var req resumeScheduleRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		normalized, err := bootstrap.NormalizeResumeSchedule(bootstrap.ResumeScheduleConfig{
			DailyTimes: req.DailyTimes,
			Timezone:   req.Timezone,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.saveResumeSchedule(normalized); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"schedule": normalized})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) saveResumeSchedule(schedule bootstrap.ResumeScheduleConfig) error {
	s.cfgMu.Lock()
	next := cloneWebConfig(s.cfg)
	next.ResumeSchedule = schedule
	if err := saveWebConfig(next); err != nil {
		s.cfgMu.Unlock()
		return err
	}
	s.cfg = cloneWebConfig(next)
	s.cfgMu.Unlock()
	if s.sessions != nil {
		s.sessions.SetConfig(next)
	}
	if s.resumeScheduler != nil {
		s.resumeScheduler.Wake()
	}
	return nil
}

func (s *Server) handleProjectResumeSchedule(w http.ResponseWriter, r *http.Request, id string) {
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		enabled := session.ScheduledResumeEnabled()
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "enabled": enabled})
	case http.MethodPut:
		var req projectResumeScheduleRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled is required")
			return
		}
		if err := session.SetScheduledResumeEnabled(*req.Enabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "enabled": *req.Enabled})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
