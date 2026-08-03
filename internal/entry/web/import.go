package web

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/host/imp"
)

type apiImportEvent struct {
	Time    time.Time `json:"time"`
	Stage   string    `json:"stage"`
	Current int       `json:"current"`
	Total   int       `json:"total"`
	Message string    `json:"message"`
	Error   string    `json:"error,omitempty"`
}

type importRunError struct {
	message string
}

func (e importRunError) Error() string {
	return e.message
}

func (s *Server) handleProjectImport(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	finishAction, err := session.beginActionKind(projectActionKindContinuation)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	defer finishAction()
	headers, cleanup, err := parseMultipartFiles(w, r, unlimitedUploadBytes)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(headers) != 1 {
		writeError(w, http.StatusBadRequest, "exactly one source novel file is required")
		return
	}
	resumeFrom, err := parseMultipartResumeFrom(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	importDir := projectImportUploadDir(manifest)
	uploads, err := readPendingUploads(headers, textUploadExtensions, maxTextUploadBytes, importDir)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	if err := writePendingUploads(uploads, importDir); err != nil {
		writeUploadValidationError(w, err)
		return
	}

	source := uploads[0]
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	events, label, err := session.importExternalNovelOwned(ctx, filepath.Join(importDir, source.Name), resumeFrom)
	if err != nil {
		writeImportActionError(w, err, events)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":     manifest,
		"snapshot":    snapshot,
		"source_file": source.apiUploadedFile,
		"events":      events,
		"label":       label,
		"running":     snapshot.IsRunning,
	})
}

func parseMultipartResumeFrom(r *http.Request) (int, error) {
	if r.MultipartForm == nil {
		return 0, nil
	}
	values := r.MultipartForm.Value
	var raw string
	if len(values["from"]) > 0 {
		raw = values["from"][0]
	} else if len(values["resume_from"]) > 0 {
		raw = values["resume_from"][0]
	}
	return parseNonNegativeFormInt(raw, "from")
}

func apiImportEventFromImp(ev imp.Event) apiImportEvent {
	api := apiImportEvent{
		Time:    ev.Time,
		Stage:   string(ev.Stage),
		Current: ev.Current,
		Total:   ev.Total,
		Message: ev.Message,
	}
	if api.Time.IsZero() {
		api.Time = time.Now().UTC()
	}
	if ev.Err != nil {
		api.Error = ev.Err.Error()
	}
	if api.Message == "" && api.Error != "" {
		api.Message = api.Error
	}
	return api
}

func projectImportUploadDir(manifest ProjectManifest) string {
	return filepath.Join(manifest.RootDir, "uploads", "import")
}

func writeImportActionError(w http.ResponseWriter, err error, events []apiImportEvent) {
	status := http.StatusConflict
	var runErr importRunError
	switch {
	case errors.Is(err, ErrSessionActionInProgress):
		status = http.StatusConflict
	case errors.As(err, &runErr):
		status = http.StatusBadRequest
	case strings.Contains(strings.ToLower(err.Error()), "source path is required"):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{
		"error":  err.Error(),
		"events": events,
	})
}
