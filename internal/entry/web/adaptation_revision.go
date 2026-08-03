package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type webAdaptationRevisionPreviewRequest struct {
	Intent         string                `json:"intent"`
	Candidate      domain.AdaptationPlan `json:"candidate"`
	IdempotencyKey string                `json:"idempotency_key"`
}

type webAdaptationRevisionCommandRequest struct {
	Action           string                                   `json:"action"`
	ExpectedRevision int                                      `json:"expected_revision"`
	IdempotencyKey   string                                   `json:"idempotency_key"`
	Preview          *host.AdaptationStructureRevisionPreview `json:"preview,omitempty"`
	Candidate        domain.AdaptationPlan                    `json:"candidate,omitempty"`
	Evidence         []domain.RevisionAuditEvidence           `json:"evidence,omitempty"`
	BatchCommand     domain.AdaptationRevisionBatchCommand    `json:"batch_command,omitempty"`
	TargetID         string                                   `json:"target_id,omitempty"`
	Message          string                                   `json:"message,omitempty"`
	ImpactSignature  string                                   `json:"impact_signature,omitempty"`
}

func (s *Server) handleProjectAdaptationRevisionPreview(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request webAdaptationRevisionPreviewRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid adaptation revision preview: "+err.Error())
		return
	}
	if strings.TrimSpace(request.Intent) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "intent and idempotency_key are required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	result, err := session.AdaptationRevisionService().Preview(host.AdaptationRevisionPreviewRequest{Intent: request.Intent, Candidate: request.Candidate}, request.IdempotencyKey)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "preview": result.Preview, "revision": result.Session})
}

func (s *Server) handleProjectAdaptationRevisionCommand(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request webAdaptationRevisionCommandRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid adaptation revision command: "+err.Error())
		return
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" || request.ExpectedRevision <= 0 {
		writeError(w, http.StatusBadRequest, "idempotency_key and expected_revision are required")
		return
	}
	action := strings.TrimSpace(request.Action)
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectManifestError(w, err)
		return
	}
	if action != "batch" {
		receiptRequest := host.AdaptationRevisionCommandReceiptRequest{
			Action: action, ExpectedRevision: request.ExpectedRevision, Preview: request.Preview,
			Candidate: request.Candidate, Evidence: request.Evidence, Message: request.Message, ImpactSignature: request.ImpactSignature,
		}
		receiptService := host.NewAdaptationRevisionService(storepkg.NewStore(manifest.OutputDir))
		if replay, found, replayErr := receiptService.LoadCommandReceipt(receiptRequest, request.IdempotencyKey); replayErr != nil {
			writeProjectLifecycleError(w, replayErr)
			return
		} else if found {
			writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": replay, "runtime": nil})
			return
		}
	}
	projectSession, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	service := projectSession.AdaptationRevisionService()
	active, err := storepkg.NewStore(manifest.OutputDir).Revisions.Active()
	if err != nil || active == nil {
		if err == nil {
			err = fmt.Errorf("no active adaptation revision")
		}
		writeProjectLifecycleError(w, fmt.Errorf("load active adaptation revision: %w", err))
		return
	}
	if active.Mode != domain.RevisionModeAdaptation || active.Revision != request.ExpectedRevision {
		writeProjectLifecycleError(w, fmt.Errorf("adaptation revision conflict: expected %d, actual %d mode=%s", request.ExpectedRevision, active.Revision, active.Mode))
		return
	}
	var revision *domain.RevisionSession
	var runtime *domain.AdaptationRevisionRuntime
	switch action {
	case "approve_impact":
		revision, err = service.ApproveImpact(active.ID, active.Revision, request.IdempotencyKey)
	case "submit_structure":
		if request.Preview == nil {
			err = fmt.Errorf("sealed adaptation preview is required")
		} else {
			revision, err = service.SubmitStructureCandidate(*request.Preview, active, request.IdempotencyKey)
		}
	case "submit_details":
		revision, err = service.SubmitDetailedOutlineCandidate(request.Candidate, active, request.IdempotencyKey)
	case "batch":
		runtime, err = service.RunBatchCommand(active.ID, request.BatchCommand, request.TargetID, request.Message)
	case "record_audit":
		revision, err = service.RecordAuditSet(active, request.Evidence, request.IdempotencyKey)
	case "approve_stage":
		revision, err = service.ApproveStage(active, request.IdempotencyKey)
	case "submit_prose_intents":
		revision, err = service.SubmitProseReworkCandidate(active, request.IdempotencyKey)
	case "feedback":
		revision, err = service.SubmitFeedback(active, request.ImpactSignature, request.Message, request.IdempotencyKey)
	case "pause":
		revision, err = service.Pause(active, request.IdempotencyKey)
	case "resume":
		revision, err = service.Resume(active, request.IdempotencyKey)
	case "fail":
		revision, err = service.Fail(active, request.Message, request.IdempotencyKey)
	case "cancel":
		revision, err = service.Cancel(active, request.IdempotencyKey)
	case "publish":
		if request.Preview == nil {
			err = fmt.Errorf("sealed adaptation preview is required")
		} else {
			revision, err = service.Publish(*request.Preview, active, request.IdempotencyKey)
		}
	default:
		err = fmt.Errorf("unsupported adaptation revision command %q", request.Action)
	}
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": revision, "runtime": runtime})
}
