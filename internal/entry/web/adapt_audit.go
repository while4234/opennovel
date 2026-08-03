package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type apiAuditSourceChapter struct {
	Chapter int    `json:"chapter"`
	Title   string `json:"title"`
}

func (s *Server) handleProjectAdaptAudit(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		manifest, err := s.store.OpenProject(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		st := storepkg.NewStore(manifest.OutputDir)
		report, err := st.Adaptation.LoadAuditReport()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		chapters, err := auditSourceChapters(st)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		availableScope, scopeErr := adapt.ResolveProjectAuditScope(st, adapt.AuditOptions{})
		if scopeErr != nil && !errors.Is(scopeErr, adapt.ErrNoAuditableScope) {
			writeError(w, http.StatusInternalServerError, scopeErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"report":          report,
			"source_chapters": chapters,
			"auditable_scope": availableScope,
		})
	case http.MethodPost:
		var options adapt.AuditOptions
		if r.Body != nil {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil && !errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, "invalid adaptation audit request: "+err.Error())
				return
			}
		}
		session, manifest, err := s.sessions.Open(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		if session.Snapshot().IsRunning {
			writeError(w, http.StatusConflict, "pause the project before running an adaptation audit")
			return
		}
		if state := session.CoCreateState(); state != nil && state.Active {
			writeError(w, http.StatusConflict, "finish or cancel co-create before running an adaptation audit")
			return
		}
		unlock, err := session.beginAction()
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		defer unlock()
		report, err := adapt.AuditProject(storepkg.NewStore(manifest.OutputDir), options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		latestRun, runErr := storepkg.NewStore(manifest.OutputDir).Adaptation.LatestAuditRun()
		if runErr != nil {
			writeError(w, http.StatusInternalServerError, runErr.Error())
			return
		}
		response := map[string]any{
			"report":          report,
			"snapshot":        session.Snapshot(),
			"applied":         false,
			"auditable_scope": report.Scope,
			"run":             latestRun,
		}
		if latestRun != nil {
			response["run_id"] = latestRun.RunID
		}
		writeJSON(w, http.StatusOK, response)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func auditSourceChapters(st *storepkg.Store) ([]apiAuditSourceChapter, error) {
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return nil, err
	}
	chapters := make([]apiAuditSourceChapter, 0)
	if manifest == nil {
		return chapters, nil
	}
	chapters = make([]apiAuditSourceChapter, 0, len(manifest.Chapters))
	for _, chapter := range manifest.Chapters {
		if chapter.Chapter <= 0 {
			continue
		}
		chapters = append(chapters, apiAuditSourceChapter{Chapter: chapter.Chapter, Title: chapter.Title})
	}
	return chapters, nil
}

func (s *Server) handleProjectAdaptAuditApply(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request adaptaudit.ConfirmationRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid adaptation audit confirmation: "+err.Error())
			return
		}
	}
	if strings.TrimSpace(request.RunID) == "" {
		writeError(w, http.StatusBadRequest, "audit run id is required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if session.Snapshot().IsRunning {
		writeError(w, http.StatusConflict, "pause the project before applying an adaptation repair")
		return
	}
	if state := session.CoCreateState(); state != nil && state.Active {
		writeError(w, http.StatusConflict, "finish or cancel co-create before applying an adaptation repair")
		return
	}
	unlock, err := session.beginAction()
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	defer unlock()
	application, err := adapt.ApplyProjectAuditRepair(storepkg.NewStore(manifest.OutputDir), request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session.AppendSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"application": application,
		"snapshot":    session.Snapshot(),
		"applied":     true,
		"running":     false,
	})
}
