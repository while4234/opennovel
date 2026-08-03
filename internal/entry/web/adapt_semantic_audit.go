package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type semanticAuditJob struct {
	cancel context.CancelFunc
}

var semanticAuditJobs sync.Map

type semanticAuditHost interface {
	EstimateSemanticAudit(adapt.SemanticAuditOptions) (*adapt.SemanticAuditEstimate, error)
	PrepareSemanticAudit(adapt.SemanticAuditOptions) (*adapt.SemanticAuditRun, error)
	ExecuteSemanticAudit(context.Context, string) error
	LoadSemanticAudit(string) (*adapt.SemanticAuditRun, error)
	ResumeSemanticAudit(string) (*adapt.SemanticAuditRun, error)
}

func projectSemanticAuditHost(session *ProjectSession) (semanticAuditHost, error) {
	h, ok := session.host.(semanticAuditHost)
	if !ok {
		return nil, errors.New("semantic audit is unavailable")
	}
	return h, nil
}

func (s *Server) handleProjectSemanticAuditEstimate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	options, ok := decodeSemanticAuditOptions(w, r)
	if !ok {
		return
	}
	session, _, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	h, err := projectSemanticAuditHost(session)
	if err != nil {
		writeError(w, http.StatusNotImplemented, err.Error())
		return
	}
	estimate, err := h.EstimateSemanticAudit(options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"estimate": estimate})
}

func (s *Server) handleProjectSemanticAuditStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	options, ok := decodeSemanticAuditOptions(w, r)
	if !ok {
		return
	}
	session, _, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	h, err := projectSemanticAuditHost(session)
	if err != nil {
		writeError(w, http.StatusNotImplemented, err.Error())
		return
	}
	if session.Snapshot().IsRunning {
		writeError(w, http.StatusConflict, "pause the project before starting a semantic audit")
		return
	}
	if state := session.CoCreateState(); state != nil && state.Active {
		writeError(w, http.StatusConflict, "finish or cancel co-create before starting a semantic audit")
		return
	}
	actionCtx, finishAction, err := session.beginCancellableAction(context.Background(), projectActionKindSemanticAudit)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	run, err := h.PrepareSemanticAudit(options)
	if err != nil {
		finishAction()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(actionCtx)
	semanticAuditJobs.Store(run.RunID, semanticAuditJob{cancel: cancel})
	go func() {
		defer semanticAuditJobs.Delete(run.RunID)
		defer cancel()
		defer finishAction()
		_ = h.ExecuteSemanticAudit(ctx, run.RunID)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

func (s *Server) handleProjectSemanticAuditRun(w http.ResponseWriter, r *http.Request, id, tail string) {
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		http.NotFound(w, r)
		return
	}
	runID := parts[0]
	session, _, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	h, err := projectSemanticAuditHost(session)
	if err != nil {
		writeError(w, http.StatusNotImplemented, err.Error())
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		run, err := h.LoadSemanticAudit(runID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if run.Status == "running" || run.Status == "queued" {
			if _, live := semanticAuditJobs.Load(runID); !live {
				finish, beginErr := session.beginActionKind(projectActionKindSemanticAudit)
				if beginErr != nil {
					writeProjectSessionError(w, beginErr)
					return
				}
				run, err = adapt.MarkSemanticAuditInterrupted(storepkg.NewStore(session.manifest.OutputDir), runID)
				finish()
			}
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"run": run})
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if job, ok := semanticAuditJobs.Load(runID); ok {
			job.(semanticAuditJob).cancel()
		}
		run, err := h.LoadSemanticAudit(runID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"run": run, "cancel_requested": true})
		return
	}
	if len(parts) == 2 && parts[1] == "report" && r.Method == http.MethodGet {
		semanticRun, loadErr := h.LoadSemanticAudit(runID)
		if loadErr != nil {
			writeError(w, http.StatusNotFound, loadErr.Error())
			return
		}
		if semanticRun.PublishedRunID == "" {
			writeError(w, http.StatusConflict, "semantic audit has not published a completed report")
			return
		}
		publishedRun, loadErr := storepkg.NewStore(session.manifest.OutputDir).Adaptation.LoadAuditRun(semanticRun.PublishedRunID)
		if loadErr != nil {
			writeError(w, http.StatusInternalServerError, loadErr.Error())
			return
		}
		if publishedRun == nil {
			writeError(w, http.StatusNotFound, "published semantic audit report not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"run": publishedRun, "report": publishedRun.Report})
		return
	}
	if len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost {
		previous, err := h.LoadSemanticAudit(runID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		actionCtx, finishAction, err := session.beginCancellableAction(context.Background(), projectActionKindSemanticAudit)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		run, err := h.ResumeSemanticAudit(previous.RunID)
		if err != nil {
			finishAction()
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := context.WithCancel(actionCtx)
		semanticAuditJobs.Store(run.RunID, semanticAuditJob{cancel: cancel})
		go func() {
			defer semanticAuditJobs.Delete(run.RunID)
			defer cancel()
			defer finishAction()
			_ = h.ExecuteSemanticAudit(ctx, run.RunID)
		}()
		writeJSON(w, http.StatusAccepted, map[string]any{"run": run, "retry_of": runID})
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func decodeSemanticAuditOptions(w http.ResponseWriter, r *http.Request) (adapt.SemanticAuditOptions, bool) {
	var options adapt.SemanticAuditOptions
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&options); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid semantic audit request: "+err.Error())
			return options, false
		}
	}
	return options, true
}
