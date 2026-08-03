package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/host"
)

type apiChapterOutlineRevision struct {
	Chapter         int                 `json:"chapter"`
	Instruction     string              `json:"instruction"`
	Label           string              `json:"label,omitempty"`
	Outline         domain.OutlineEntry `json:"outline"`
	RewriteQueued   bool                `json:"rewrite_queued"`
	DraftReset      bool                `json:"draft_reset"`
	PendingRewrites []int               `json:"pending_rewrites,omitempty"`
	StaleNotice     string              `json:"stale_notice,omitempty"`
}

func (s *Server) handleProjectChapterOutlineRevise(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeChapterOutlineRevisionRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	result, err := session.ReviseChapterOutline(r.Context(), req)
	if err != nil {
		writeChapterOutlineRevisionError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, projectChapterOutlineRevisionResponse{
		Project:  manifest,
		Snapshot: snapshot,
		Running:  snapshot.IsRunning,
		Revision: apiChapterOutlineRevisionFromHost(result),
	})
}

func writeChapterOutlineRevisionError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, errs.ErrToolArgs):
		status = http.StatusBadRequest
	case errors.Is(err, ErrSessionActionInProgress),
		errors.Is(err, errs.ErrToolPrecondition),
		errors.Is(err, errs.ErrToolConflict):
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func decodeChapterOutlineRevisionRequest(r *http.Request) (host.ChapterOutlineRevisionRequest, error) {
	var req struct {
		Chapter     int    `json:"chapter"`
		Instruction string `json:"instruction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return host.ChapterOutlineRevisionRequest{}, fmt.Errorf("invalid chapter outline revision request: %w", err)
	}
	out := host.ChapterOutlineRevisionRequest{
		Chapter:     req.Chapter,
		Instruction: strings.TrimSpace(req.Instruction),
	}
	if out.Chapter <= 0 {
		return out, fmt.Errorf("chapter must be > 0")
	}
	if out.Instruction == "" {
		return out, fmt.Errorf("instruction is required")
	}
	return out, nil
}

func apiChapterOutlineRevisionFromHost(result host.ChapterOutlineRevisionResult) apiChapterOutlineRevision {
	return apiChapterOutlineRevision{
		Chapter:         result.Chapter,
		Instruction:     result.Instruction,
		Label:           result.Label,
		Outline:         result.Outline,
		RewriteQueued:   result.RewriteQueued,
		DraftReset:      result.DraftReset,
		PendingRewrites: append([]int(nil), result.PendingRewrites...),
		StaleNotice:     result.StaleNotice,
	}
}
