package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type continuationMutationRequest struct {
	ExpectedRevision int    `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
	Async            bool   `json:"async,omitempty"`
	Instruction      string `json:"instruction,omitempty"`
	Scope            string `json:"scope,omitempty"`
	VolumeIndex      int    `json:"volume_index,omitempty"`
	Chapter          int    `json:"chapter,omitempty"`
	FromChapter      int    `json:"from_chapter,omitempty"`
	ToChapter        int    `json:"to_chapter,omitempty"`
}

func (s *Server) handleProjectContinuation(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	continuation, err := session.ContinuationSnapshot()
	if err != nil {
		writeContinuationActionError(w, err)
		return
	}
	writeContinuationResponse(w, manifest, session, continuation, "")
}

func (s *Server) handleProjectContinuationSource(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
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

	uploadDir := projectContinuationUploadDir(manifest)
	uploads, err := readPendingUploads(headers, textUploadExtensions, maxTextUploadBytes, uploadDir)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	if err := writePendingUploads(uploads, uploadDir); err != nil {
		writeUploadValidationError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	source := uploads[0]
	events, _, err := session.ImportExternalNovel(ctx, filepath.Join(uploadDir, source.Name), resumeFrom)
	if err != nil {
		writeImportActionError(w, err, events)
		return
	}
	continuation, err := session.ContinuationSnapshot()
	if err != nil {
		writeContinuationActionError(w, err)
		return
	}
	if continuation == nil {
		writeContinuationActionError(w, fmt.Errorf("continuation workflow was not initialized after import"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":      manifest,
		"snapshot":     session.WebSnapshot(),
		"continuation": continuation,
		"source_file":  source.apiUploadedFile,
		"files":        []apiUploadedFile{source.apiUploadedFile},
		"events":       events,
		"running":      false,
	})
}

func (s *Server) handleProjectContinuationDraftBegin(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webCoCreateBeginRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid continuation draft request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	req.Kind = webCoCreateKindContinuation
	state, err := session.BeginContinuationCoCreate(r.Context(), req)
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectContinuationDraftSend(w http.ResponseWriter, r *http.Request, id string) {
	s.handleProjectCoCreateSend(w, r, id)
}

func (s *Server) handleProjectContinuationDraftCommit(w http.ResponseWriter, r *http.Request, id string) {
	s.handleProjectCoCreateCommit(w, r, id)
}

func (s *Server) handleProjectContinuationProposalGenerate(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationLongMutation(w, r, id, "continuation_proposal_generate", func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		return session.GenerateContinuationProposal(ctx, req.ExpectedRevision)
	})
}

func (s *Server) handleProjectContinuationProposalRevise(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationMutation(w, r, id, func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		if strings.TrimSpace(req.Instruction) == "" {
			return nil, fmt.Errorf("revision instruction is required")
		}
		return session.ReviseContinuationProposal(ctx, req.ExpectedRevision, strings.TrimSpace(req.Instruction))
	})
}

func (s *Server) handleProjectContinuationProposalApprove(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationMutation(w, r, id, func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		return session.ApproveContinuationProposal(ctx, req.ExpectedRevision)
	})
}

func (s *Server) handleProjectContinuationVolumesRevise(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationMutation(w, r, id, func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		if strings.TrimSpace(req.Instruction) == "" {
			return nil, fmt.Errorf("revision instruction is required")
		}
		if req.VolumeIndex < 0 {
			return nil, fmt.Errorf("volume_index must be non-negative")
		}
		return session.ReviseContinuationVolumes(ctx, req.ExpectedRevision, strings.TrimSpace(req.Instruction), req.VolumeIndex)
	})
}

func (s *Server) handleProjectContinuationVolumesApprove(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationMutation(w, r, id, func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		return session.ApproveContinuationVolumes(ctx, req.ExpectedRevision)
	})
}

func (s *Server) handleProjectContinuationOutlinesGenerate(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationLongMutation(w, r, id, "continuation_outlines_generate", func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		return session.GenerateContinuationOutlines(ctx, req.ExpectedRevision)
	})
}

func (s *Server) handleProjectContinuationOutlinesRevise(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationMutation(w, r, id, func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		if strings.TrimSpace(req.Instruction) == "" {
			return nil, fmt.Errorf("revision instruction is required")
		}
		_, volumeIndex, from, to, err := normalizeContinuationOutlineRevision(req)
		if err != nil {
			return nil, err
		}
		return session.ReviseContinuationOutlines(ctx, req.ExpectedRevision, volumeIndex, from, to, strings.TrimSpace(req.Instruction))
	})
}

func (s *Server) handleProjectContinuationOutlinesApprove(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationMutation(w, r, id, func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		return session.ApproveContinuationOutlines(ctx, req.ExpectedRevision)
	})
}

func (s *Server) handleProjectContinuationRetry(w http.ResponseWriter, r *http.Request, id string) {
	s.handleContinuationMutation(w, r, id, func(ctx context.Context, session *ProjectSession, req continuationMutationRequest) (*domain.ContinuationSnapshot, error) {
		return session.RetryContinuation(ctx, req.ExpectedRevision)
	})
}

func (s *Server) handleProjectContinuationStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeContinuationMutationRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	continuation, label, err := session.StartContinuation(req.ExpectedRevision)
	if err != nil {
		writeContinuationActionError(w, err)
		return
	}
	writeContinuationResponse(w, manifest, session, continuation, label)
}

type continuationMutation func(context.Context, *ProjectSession, continuationMutationRequest) (*domain.ContinuationSnapshot, error)

func (s *Server) handleContinuationLongMutation(
	w http.ResponseWriter,
	r *http.Request,
	id string,
	kind string,
	mutate continuationMutation,
) {
	if r.Method == http.MethodGet {
		s.handleProjectBackgroundAction(w, r, id)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeContinuationMutationRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.Async {
		session, manifest, err := s.sessions.Open(id)
		if err != nil {
			writeProjectSessionError(w, err)
			return
		}
		continuation, err := mutate(r.Context(), session, req)
		if err != nil {
			writeContinuationActionError(w, err)
			return
		}
		writeContinuationResponse(w, manifest, session, continuation, "")
		return
	}

	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	action, created, err := session.StartBackgroundAction(kind, req.IdempotencyKey, func(ctx context.Context) error {
		_, runErr := mutate(ctx, session, req)
		return runErr
	})
	if err != nil {
		writeBackgroundActionStartError(w, err)
		return
	}
	writeBackgroundActionAccepted(w, r, manifest, session, action, created)
}

func (s *Server) handleContinuationMutation(w http.ResponseWriter, r *http.Request, id string, mutate continuationMutation) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeContinuationMutationRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	continuation, err := mutate(r.Context(), session, req)
	if err != nil {
		writeContinuationActionError(w, err)
		return
	}
	writeContinuationResponse(w, manifest, session, continuation, "")
}

func decodeContinuationMutationRequest(r *http.Request) (continuationMutationRequest, error) {
	var req continuationMutationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		return req, fmt.Errorf("invalid continuation request: %w", err)
	}
	if req.ExpectedRevision <= 0 {
		return req, fmt.Errorf("expected_revision must be a positive integer")
	}
	return req, nil
}

func normalizeContinuationOutlineRevision(req continuationMutationRequest) (string, int, int, int, error) {
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	from := req.FromChapter
	to := req.ToChapter
	if from == 0 && req.Chapter > 0 {
		from = req.Chapter
	}
	if to == 0 && from > 0 {
		to = from
	}
	if scope == "" {
		switch {
		case req.VolumeIndex > 0:
			scope = "volume"
		case from > 0 || to > 0:
			scope = "chapter"
		default:
			scope = "all"
		}
	}
	if scope == "range" {
		scope = "chapter"
	}
	switch scope {
	case "all":
		return scope, 0, 0, 0, nil
	case "volume":
		if req.VolumeIndex <= 0 {
			return "", 0, 0, 0, fmt.Errorf("volume_index is required for volume revision")
		}
		return scope, req.VolumeIndex, 0, 0, nil
	case "chapter":
		if from <= 0 || to <= 0 {
			return "", 0, 0, 0, fmt.Errorf("chapter range is required for chapter revision")
		}
		if from > to {
			from, to = to, from
		}
		return scope, 0, from, to, nil
	default:
		return "", 0, 0, 0, fmt.Errorf("scope must be all, volume, chapter, or range")
	}
}

func projectContinuationUploadDir(manifest ProjectManifest) string {
	return filepath.Join(manifest.RootDir, "uploads", "continuation")
}

func writeContinuationResponse(
	w http.ResponseWriter,
	manifest ProjectManifest,
	session *ProjectSession,
	continuation *domain.ContinuationSnapshot,
	label string,
) {
	snapshot := session.WebSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":      manifest,
		"snapshot":     snapshot,
		"continuation": continuation,
		"label":        label,
		"running":      snapshot.IsRunning,
	})
}

func writeContinuationActionError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	switch {
	case errors.Is(err, ErrProjectNotFound), errors.Is(err, storepkg.ErrContinuationNotInitialized):
		status = http.StatusNotFound
	case errors.Is(err, ErrSessionActionInProgress), storepkg.IsContinuationRevisionConflict(err):
		status = http.StatusConflict
	case isBadContinuationRequest(err):
		status = http.StatusBadRequest
	}
	writeError(w, status, err.Error())
}

func isBadContinuationRequest(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "is required") ||
		strings.Contains(message, "must be") ||
		strings.Contains(message, "positive integer") ||
		strings.Contains(message, "non-negative") ||
		strings.Contains(message, "out of range")
}
