package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

func (s *Server) handleProjectFoundationCharacters(
	w http.ResponseWriter,
	r *http.Request,
	id string,
	action string,
) {
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	service := session.CharacterWorkspaceService()
	if service == nil {
		writeCharacterWorkspaceError(w, &host.CharacterWorkspaceError{
			Code: host.CharacterWorkspaceErrorRecovery,
			Err:  errors.New("Character workspace service is unavailable"),
		})
		return
	}
	switch action {
	case "foundation/characters":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		state, err := service.State(strings.TrimSpace(r.URL.Query().Get("run_id")))
		if err != nil {
			writeCharacterWorkspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"project":             manifest,
			"character_workspace": state,
		})
	case "foundation/characters/analyze":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request host.CharacterAnalyzeRequest
		if err := decodeJSONBody(r, &request); err != nil {
			writeCharacterWorkspaceDecodeError(w, id, "analyze", err)
			return
		}
		run, _, err := service.PrepareAnalyze(request)
		if err != nil {
			writeCharacterWorkspaceError(w, err)
			return
		}
		s.startCharacterWorkspaceAction(
			w,
			r,
			manifest,
			session,
			service,
			run,
			projectActionKindCharacterAnalyze,
			request.IdempotencyKey,
		)
	case "foundation/characters/review":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request host.CharacterReviewRequest
		if err := decodeJSONBody(r, &request); err != nil {
			writeCharacterWorkspaceDecodeError(w, id, "review", err)
			return
		}
		run, _, err := service.PrepareReview(request)
		if err != nil {
			writeCharacterWorkspaceError(w, err)
			return
		}
		s.startCharacterWorkspaceAction(
			w,
			r,
			manifest,
			session,
			service,
			run,
			projectActionKindCharacterReview,
			request.IdempotencyKey,
		)
	case "foundation/characters/retry":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request host.CharacterRetryRequest
		if err := decodeJSONBody(r, &request); err != nil {
			writeCharacterWorkspaceDecodeError(w, id, "retry", err)
			return
		}
		run, _, err := service.PrepareRetry(request)
		if err != nil {
			writeCharacterWorkspaceError(w, err)
			return
		}
		s.startCharacterWorkspaceAction(
			w,
			r,
			manifest,
			session,
			service,
			run,
			projectActionKindCharacterRetry,
			request.IdempotencyKey,
		)
	case "foundation/characters/discard":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request host.CharacterDiscardRequest
		if err := decodeJSONBody(r, &request); err != nil {
			writeCharacterWorkspaceDecodeError(w, id, "discard", err)
			return
		}
		state, err := service.Discard(request)
		if err != nil {
			writeCharacterWorkspaceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"project":             manifest,
			"character_workspace": state,
		})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) startCharacterWorkspaceAction(
	w http.ResponseWriter,
	r *http.Request,
	manifest ProjectManifest,
	session *ProjectSession,
	service *host.CharacterWorkspaceService,
	run domain.CharacterWorkspaceRun,
	kind string,
	idempotencyKey string,
) {
	action, actionCreated, err := session.StartBackgroundAction(
		kind,
		strings.TrimSpace(idempotencyKey),
		func(ctx context.Context) error {
			return service.Execute(ctx, run.RunID)
		},
	)
	if err != nil {
		_ = service.FailQueued(run.RunID, err)
		writeCharacterWorkspaceError(w, &host.CharacterWorkspaceError{
			Code: host.CharacterWorkspaceErrorBusy,
			Err:  err,
		})
		return
	}
	state, err := service.State(run.RunID)
	if err != nil {
		writeCharacterWorkspaceError(w, err)
		return
	}
	status := http.StatusAccepted
	if run.Status == domain.CharacterWorkspaceCompleted ||
		run.Status == domain.CharacterWorkspaceFailed ||
		run.Status == domain.CharacterWorkspaceInterrupted ||
		run.Status == domain.CharacterWorkspaceStale {
		status = http.StatusOK
	}
	w.Header().Set("location", r.URL.Path[:strings.LastIndex(r.URL.Path, "/")+1]+"?run_id="+run.RunID)
	writeJSON(w, status, map[string]any{
		"project":             manifest,
		"run_id":              run.RunID,
		"action_id":           action.ActionID,
		"action":              action,
		"created":             actionCreated,
		"character_workspace": state,
	})
}

func writeCharacterWorkspaceDecodeError(
	w http.ResponseWriter,
	projectID string,
	endpoint string,
	err error,
) {
	if foundationSourceMutationAttempt(err) {
		log.Printf(
			"character workspace source mutation attempt rejected project=%s endpoint=%s",
			projectID,
			endpoint,
		)
		writeCharacterWorkspaceError(w, &host.CharacterWorkspaceError{
			Code: host.FoundationErrorSourceMutation,
			Err:  errors.New("Character workspace requests accept target-story candidates only"),
		})
		return
	}
	writeCharacterWorkspaceError(w, &host.CharacterWorkspaceError{
		Code: host.CharacterWorkspaceErrorInvalid,
		Err:  errors.New("invalid Character workspace request: " + err.Error()),
	})
}

func writeCharacterWorkspaceError(w http.ResponseWriter, err error) {
	var classified *host.CharacterWorkspaceError
	if !errors.As(err, &classified) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{
				"code":    "character_workspace_error",
				"message": "Character workspace operation failed",
			},
		})
		return
	}
	status := http.StatusConflict
	switch classified.Code {
	case host.CharacterWorkspaceErrorInvalid, host.FoundationErrorSourceMutation:
		status = http.StatusBadRequest
	case host.CharacterWorkspaceErrorNotFound:
		status = http.StatusNotFound
	case host.CharacterWorkspaceErrorAgent, host.CharacterWorkspaceErrorRecovery:
		status = http.StatusServiceUnavailable
	case host.CharacterWorkspaceErrorBusy, host.CharacterWorkspaceErrorConflict,
		host.CharacterWorkspaceErrorReadonly, host.CharacterWorkspaceErrorStale:
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    classified.Code,
			"message": classified.Error(),
		},
	})
}
