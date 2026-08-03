package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/expansionauditorclient"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type manuscriptOperationErrorEnvelope struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeManuscriptOperationError(w http.ResponseWriter, err error) {
	status, envelope := classifyManuscriptOperationError(err)
	writeJSON(w, status, map[string]any{"error": envelope})
}

func writeManuscriptRequestError(w http.ResponseWriter, status int, message string) {
	code := "invalid_request"
	switch status {
	case http.StatusMethodNotAllowed:
		code = "method_not_allowed"
	case http.StatusRequestEntityTooLarge:
		code = "request_budget_exceeded"
	case http.StatusServiceUnavailable:
		code = "service_unavailable"
	}
	writeManuscriptEnvelope(w, status, code, message, nil)
}

func writeManuscriptEnvelope(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, map[string]any{"error": manuscriptOperationErrorEnvelope{Code: code, Message: message, Details: details}})
}

func classifyManuscriptOperationError(err error) (int, manuscriptOperationErrorEnvelope) {
	envelope := manuscriptOperationErrorEnvelope{Code: "invalid_request", Message: "request could not be processed"}
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, ErrProjectStartupRecovery):
		status, envelope.Code, envelope.Message = http.StatusServiceUnavailable, "startup_recovery_required", "project startup recovery must complete before requests can be served"
	case errors.Is(err, ErrProjectNotFound), errors.Is(err, storepkg.ErrManuscriptRevisionNotFound), errors.Is(err, host.ErrExpansionPreviewNotFound):
		status, envelope.Code = http.StatusNotFound, "not_found"
	case errors.Is(err, expansionauditorclient.ErrUnavailable), errors.Is(err, expansionauditorclient.ErrProcess), errors.Is(err, expansionauditorclient.ErrDecode):
		status, envelope.Code, envelope.Message = http.StatusServiceUnavailable, "expansion_auditor_unavailable", "independent expansion auditor is unavailable"
	case errors.Is(err, host.ErrExpansionPreviewStale), errors.Is(err, host.ErrExpansionPreviewExpired), errors.Is(err, host.ErrExpansionPreviewSealInvalidated):
		status, envelope.Code = http.StatusConflict, "preview_stale"
	case errors.Is(err, host.ErrExpansionPreviewCancelled):
		status, envelope.Code, envelope.Message = http.StatusConflict, "preview_cancelled", "expansion preview was cancelled"
	case errors.Is(err, storepkg.ErrActiveRevisionExists), errors.Is(err, storepkg.ErrRevisionCommandInProgress):
		status, envelope.Code = http.StatusConflict, "active_revision"
	case errors.Is(err, storepkg.ErrManuscriptIdempotencyConflict):
		status, envelope.Code = http.StatusConflict, "idempotency_conflict"
	case storepkg.IsPublicationRecoveryRequired(err):
		status, envelope.Code = http.StatusConflict, "publication_recovery_required"
	case storepkg.IsRevisionConflict(err), errors.Is(err, storepkg.ErrManuscriptRevisionConflict):
		status, envelope.Code = http.StatusConflict, "revision_conflict"
	}
	var classified *domain.ManuscriptRevisionError
	if !errors.As(err, &classified) || strings.TrimSpace(classified.Class) == "" {
		return status, envelope
	}
	errorClass := strings.TrimSpace(classified.Class)
	envelope.Details = map[string]any{"error_class": errorClass}
	envelope.Message = "manuscript operation failed: " + errorClass
	switch errorClass {
	case "revision_conflict":
		status, envelope.Code = http.StatusConflict, "revision_conflict"
	case "signature_drift", "preview_stale":
		status, envelope.Code = http.StatusConflict, "preview_stale"
	case "idempotency_conflict":
		status, envelope.Code = http.StatusConflict, "idempotency_conflict"
	case "active_revision":
		status, envelope.Code = http.StatusConflict, "active_revision"
	case "publication_recovery_required":
		status, envelope.Code = http.StatusConflict, "publication_recovery_required"
	case "human_confirmation_required", "approval_required", "dependency_audit_needs_fix":
		status, envelope.Code = http.StatusConflict, "human_confirmation_required"
	case "provider_error", "provider_timeout", "provider_auth", "provider_quota", "request_budget_exceeded",
		"invalid_json", "invalid_schema", "missing_segment", "empty_response", "truncated_response", "segment_limit",
		"segment_identity_mismatch", "segment_plan_mismatch", "content_store_failure", "dependency_audit_failed", "auditor_error":
		status, envelope.Code = http.StatusUnprocessableEntity, "batch_failed"
	default:
		status, envelope.Code = http.StatusBadRequest, "invalid_request"
	}
	return status, envelope
}
