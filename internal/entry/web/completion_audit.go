package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func (s *Server) handleProjectCompletionAudit(w http.ResponseWriter, r *http.Request, id string) {
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeManuscriptOperationError(w, fmt.Errorf("%w: project is unavailable", ErrProjectNotFound))
		return
	}
	st := storepkg.NewStore(manifest.OutputDir)
	switch r.Method {
	case http.MethodGet:
		report, loadErr := st.Adaptation.LoadAuditReport()
		if loadErr != nil {
			writeManuscriptEnvelope(w, http.StatusInternalServerError, "completion_audit_load_failed", "completion audit report is unavailable", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"report": report})
	case http.MethodPost:
		session, _, openErr := s.sessions.Open(id)
		if openErr != nil {
			writeManuscriptOperationError(w, openErr)
			return
		}
		if session.Snapshot().IsRunning {
			writeManuscriptRequestError(w, http.StatusConflict, "pause the project before running a completion audit")
			return
		}
		if state := session.CoCreateState(); state != nil && state.Active {
			writeManuscriptRequestError(w, http.StatusConflict, "finish or cancel co-create before running a completion audit")
			return
		}
		if !st.Adaptation.Active() {
			writeJSON(w, http.StatusOK, map[string]any{"status": "not_applicable", "allowed": true})
			return
		}
		gateResult, auditErr := adapt.NewCompletionGate(st).EvaluateCompletion()
		if auditErr != nil {
			writeManuscriptOperationError(w, auditErr)
			return
		}
		report, loadErr := st.Adaptation.LoadAuditReport()
		if loadErr != nil {
			writeManuscriptEnvelope(w, http.StatusInternalServerError, "completion_audit_load_failed", "completion audit report is unavailable", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"report":         report,
			"allowed":        gateResult.Allowed,
			"legacy_warning": gateResult.Warning != "",
		})
	default:
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func validatePublishExport(session *ProjectSession, st *storepkg.Store, req projectExportRequest) (*adaptaudit.Report, error) {
	if session == nil || st == nil {
		return nil, fmt.Errorf("publish export project state is unavailable")
	}
	snapshot := session.Snapshot()
	if snapshot.IsRunning {
		return nil, fmt.Errorf("pause the project before a publish export")
	}
	if state := session.CoCreateState(); state != nil && state.Active {
		return nil, fmt.Errorf("finish or cancel co-create before a publish export")
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return nil, fmt.Errorf("load progress for publish export: %w", err)
	}
	if progress.InProgressChapter > 0 {
		return nil, fmt.Errorf("chapter %d is still in progress", progress.InProgressChapter)
	}
	if len(progress.PendingRewrites) > 0 {
		return nil, fmt.Errorf("publish export is blocked by %d pending rewrites", len(progress.PendingRewrites))
	}
	expected := expectedPublishChapterCount(st, progress)
	if expected <= 0 {
		return nil, fmt.Errorf("publish export requires a complete chapter plan")
	}
	if req.From > 1 || (req.To > 0 && req.To != expected) {
		return nil, fmt.Errorf("publish export must cover the full range 1..%d", expected)
	}
	completed := make(map[int]bool, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		if chapter < 1 || chapter > expected {
			return nil, fmt.Errorf("publish export contains chapter %d outside the complete plan 1..%d", chapter, expected)
		}
		completed[chapter] = true
	}
	for chapter := 1; chapter <= expected; chapter++ {
		if !completed[chapter] {
			return nil, fmt.Errorf("publish export is missing completed chapter %d", chapter)
		}
		body, bodyErr := st.Drafts.LoadChapterText(chapter)
		if bodyErr != nil || strings.TrimSpace(body) == "" {
			return nil, fmt.Errorf("publish export chapter %d is missing or empty", chapter)
		}
	}
	if !st.Adaptation.Active() {
		return nil, nil
	}
	report, err := adapt.AuditProject(st, adapt.AuditOptions{Trigger: adaptaudit.AuditTriggerExport})
	if err != nil {
		return nil, fmt.Errorf("run publish audit: %w", err)
	}
	if err := adapt.ProtectAuditReport(st, report, "pre_export"); err != nil {
		return nil, fmt.Errorf("protect publish audit: %w", err)
	}
	if report.Scope.TargetFrom != 1 || report.Scope.TargetTo != expected {
		return nil, fmt.Errorf("publish audit covers %d..%d instead of the full target range 1..%d", report.Scope.TargetFrom, report.Scope.TargetTo, expected)
	}
	if report.Status == "pass" {
		return report, nil
	}
	if !req.ForceExport {
		return report, fmt.Errorf("publish audit status is %s; explicit force_export confirmation is required for report %s", report.Status, report.Digest)
	}
	if strings.TrimSpace(req.AcknowledgedReportDigest) != report.Digest {
		return report, fmt.Errorf("acknowledged_report_digest does not match the current publish audit")
	}
	if strings.TrimSpace(req.OverrideReason) == "" {
		return report, fmt.Errorf("override_reason is required when force_export is true")
	}
	return report, nil
}

func expectedPublishChapterCount(st *storepkg.Store, progress *domain.Progress) int {
	if st.Adaptation.Active() {
		if plan, err := st.Adaptation.LoadPlan(); err == nil && plan != nil && plan.Status == domain.AdaptationPlanStatusConfirmed {
			maxChapter := 0
			for _, chapter := range plan.Chapters {
				if chapter.Chapter > maxChapter {
					maxChapter = chapter.Chapter
				}
			}
			return maxChapter
		}
	}
	return progress.TotalChapters
}
