package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/store"
)

type apiExportResult struct {
	Path     string `json:"path"`
	Name     string `json:"name,omitempty"`
	Chapters int    `json:"chapters"`
	Bytes    int    `json:"bytes"`
	Skipped  []int  `json:"skipped"`
}

type projectExportRequest struct {
	Path                     string `json:"path"`
	Format                   string `json:"format"`
	From                     int    `json:"from"`
	To                       int    `json:"to"`
	Overwrite                bool   `json:"overwrite"`
	Purpose                  string `json:"purpose,omitempty"`
	ForceExport              bool   `json:"force_export,omitempty"`
	AcknowledgedReportDigest string `json:"acknowledged_report_digest,omitempty"`
	OverrideReason           string `json:"override_reason,omitempty"`
}

func (s *Server) handleProjectStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeProjectStartRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if snapshotHasExistingBook(session.Snapshot()) {
		writeError(w, http.StatusConflict, "project already has writing state; use continue/resume or create a new project")
		return
	}
	if err := session.StartQuick(req.Text, req.TargetTotalWords); err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, projectActionResponse{
		Project:  manifest,
		Snapshot: snapshot,
		Running:  snapshot.IsRunning,
	})
}

type projectStartRequest struct {
	Text             string `json:"text"`
	TargetTotalWords int    `json:"target_total_words"`
}

func decodeProjectStartRequest(r *http.Request) (projectStartRequest, error) {
	var req projectStartRequest
	if err := decodeJSONBody(r, &req); err != nil {
		return req, fmt.Errorf("invalid request body: %w", err)
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return req, fmt.Errorf("text is required")
	}
	if req.TargetTotalWords < 0 {
		return req, fmt.Errorf("target_total_words must be a non-negative integer")
	}
	return req, nil
}

func (s *Server) handleProjectPause(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	stopped := session.Pause()
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"stopped":  stopped,
		"running":  snapshot.IsRunning,
	})
}

func (s *Server) handleProjectRollbackPreview(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	preview, err := session.RollbackPreview()
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"rollback": preview,
	})
}

func (s *Server) handleProjectRollback(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req domain.RollbackRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid rollback request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	result, err := session.Rollback(req)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":       manifest,
		"snapshot":      snapshot,
		"cocreate":      session.CoCreateState(),
		"running":       snapshot.IsRunning,
		"rollback":      result.Preview,
		"deleted_paths": result.DeletedPaths,
	})
}

func (s *Server) handleProjectExport(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeProjectExportRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid export request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	var auditReport *adaptaudit.Report
	if req.Purpose == "publish" {
		unlock, lockErr := session.beginAction()
		if lockErr != nil {
			writeProjectLifecycleError(w, lockErr)
			return
		}
		defer unlock()
		auditReport, err = validatePublishExport(session, store.NewStore(manifest.OutputDir), req)
		if err != nil {
			writePublishExportError(w, err, auditReport)
			return
		}
	}
	format, err := exportFormat(req.Format)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	outPath, err := projectExportPath(manifest, req.Path, format)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Purpose == "publish" {
		auditReport, err = validatePublishExport(session, store.NewStore(manifest.OutputDir), req)
		if err != nil {
			writePublishExportError(w, err, auditReport)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := session.Export(ctx, exp.Options{
		Format:    format,
		OutPath:   outPath,
		From:      req.From,
		To:        req.To,
		Overwrite: req.Overwrite,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	snapshot := session.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"export":   apiExportResultFromExp(result),
		"running":  snapshot.IsRunning,
		"audit":    auditReport,
	})
}

func (s *Server) handleProjectExportDownload(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeProjectExportRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid export request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	var auditReport *adaptaudit.Report
	if req.Purpose == "publish" {
		unlock, lockErr := session.beginAction()
		if lockErr != nil {
			writeProjectLifecycleError(w, lockErr)
			return
		}
		defer unlock()
		auditReport, err = validatePublishExport(session, store.NewStore(manifest.OutputDir), req)
		if err != nil {
			writePublishExportError(w, err, auditReport)
			return
		}
	}
	format, err := exportFormat(req.Format)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fileName, err := projectExportFileName(manifest, req.Path, format)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tmpDir, err := os.MkdirTemp("", "ainovel-export-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)

	outPath := filepath.Join(tmpDir, fileName)
	if req.Purpose == "publish" {
		auditReport, err = validatePublishExport(session, store.NewStore(manifest.OutputDir), req)
		if err != nil {
			writePublishExportError(w, err, auditReport)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := session.Export(ctx, exp.Options{
		Format:    format,
		OutPath:   outPath,
		From:      req.From,
		To:        req.To,
		Overwrite: true,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", exportContentType(format))
	w.Header().Set("Content-Disposition", contentDispositionAttachment(fileName))
	w.Header().Set("X-AINovel-Export-Name", fileName)
	w.Header().Set("X-AINovel-Export-Chapters", strconv.Itoa(result.Chapters))
	w.Header().Set("X-AINovel-Export-Bytes", strconv.Itoa(len(data)))
	w.Header().Set("X-AINovel-Export-Skipped", intsHeader(result.Skipped))
	if auditReport != nil {
		w.Header().Set("X-AINovel-Audit-Status", auditReport.Status)
		w.Header().Set("X-AINovel-Audit-Digest", auditReport.Digest)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func writePublishExportError(w http.ResponseWriter, err error, report *adaptaudit.Report) {
	response := map[string]any{
		"code":          "publish_precondition_failed",
		"error":         err.Error(),
		"force_allowed": false,
	}
	if report != nil && report.Status != "pass" {
		response["code"] = "completion_audit_blocked"
		response["report"] = map[string]any{
			"digest": report.Digest,
			"status": report.Status,
		}
		response["force_allowed"] = true
	}
	writeJSON(w, http.StatusConflict, response)
}

func decodeProjectExportRequest(r *http.Request) (projectExportRequest, error) {
	var req projectExportRequest
	if err := decodeJSONBody(r, &req); err != nil {
		return req, err
	}
	req.Path = strings.TrimSpace(req.Path)
	req.Format = strings.TrimSpace(req.Format)
	if req.From < 0 || req.To < 0 {
		return req, fmt.Errorf("from and to must be non-negative")
	}
	req.Purpose = strings.ToLower(strings.TrimSpace(req.Purpose))
	if req.Purpose == "" {
		req.Purpose = "preview"
	}
	if req.Purpose != "preview" && req.Purpose != "publish" {
		return req, fmt.Errorf("purpose must be preview or publish")
	}
	req.AcknowledgedReportDigest = strings.TrimSpace(req.AcknowledgedReportDigest)
	req.OverrideReason = strings.TrimSpace(req.OverrideReason)
	return req, nil
}

func (s *Server) handleProjectDiag(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	st := store.NewStore(manifest.OutputDir)
	report, capture := diag.Diagnose(st)
	exportPath, _ := diag.WriteExport(st, report, capture)
	writeJSON(w, http.StatusOK, map[string]any{
		"project":     manifest,
		"report":      report,
		"runtime":     capture,
		"export_path": exportPath,
	})
}

func snapshotHasExistingBook(snapshot host.UISnapshot) bool {
	return strings.TrimSpace(snapshot.NovelName) != "" ||
		strings.TrimSpace(snapshot.Phase) != "" ||
		snapshot.TotalChapters > 0 ||
		snapshot.CompletedCount > 0 ||
		snapshot.TotalWordCount > 0
}

func snapshotHasStartedWriting(snapshot host.UISnapshot) bool {
	return snapshot.CompletedCount > 0 ||
		snapshot.TotalWordCount > 0
}

func exportFormat(value string) (exp.Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case string(exp.FormatTXT):
		return exp.FormatTXT, nil
	case string(exp.FormatEPUB):
		return exp.FormatEPUB, nil
	default:
		return "", fmt.Errorf("export format must be txt or epub")
	}
}

func projectExportPath(manifest ProjectManifest, raw string, format exp.Format) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if filepath.IsAbs(raw) || isWindowsAbsolutePath(raw) {
		return "", fmt.Errorf("export path must be relative to the project exports directory")
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("export path must stay inside the project exports directory")
	}
	if strings.HasSuffix(raw, "/") || strings.HasSuffix(raw, `\`) {
		return "", fmt.Errorf("export path must include a file name")
	}
	if strings.Trim(filepath.Base(clean), ". ") == "" {
		return "", fmt.Errorf("export path must include a valid file name")
	}
	clean, err := ensureExportPathExtension(clean, format)
	if err != nil {
		return "", err
	}
	base := filepath.Join(manifest.RootDir, "exports")
	target := filepath.Join(base, clean)
	if !isSameOrChild(base, target) || filepath.Clean(target) == filepath.Clean(base) {
		return "", fmt.Errorf("export path must stay inside the project exports directory")
	}
	return target, nil
}

func projectExportFileName(manifest ProjectManifest, raw string, format exp.Format) (string, error) {
	if raw = strings.TrimSpace(raw); raw != "" {
		path, err := projectExportPath(manifest, raw, format)
		if err != nil {
			return "", err
		}
		name := sanitizeExportFileName(filepath.Base(path))
		if name == "" {
			return "", fmt.Errorf("export filename is empty")
		}
		return name, nil
	}
	name := sanitizeExportFileName(manifest.Name)
	if name == "" {
		name = "novel"
	}
	ext := selectedExportExtension(format)
	if ext == "" {
		ext = ".txt"
	}
	return name + ext, nil
}

func ensureExportPathExtension(path string, format exp.Format) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	wantExt := selectedExportExtension(format)
	switch ext {
	case ".txt", ".epub":
		if wantExt != "" && ext != wantExt {
			return "", fmt.Errorf("export file extension %s does not match selected format %s", ext, format)
		}
		return path, nil
	case "":
		if wantExt == "" {
			wantExt = ".txt"
		}
		return path + wantExt, nil
	default:
		if wantExt != "" {
			return path + wantExt, nil
		}
		return path, nil
	}
}

func selectedExportExtension(format exp.Format) string {
	switch format {
	case exp.FormatEPUB:
		return ".epub"
	case exp.FormatTXT:
		return ".txt"
	default:
		return ""
	}
}

func exportContentType(format exp.Format) string {
	switch format {
	case exp.FormatEPUB:
		return "application/epub+zip"
	default:
		return "text/plain; charset=utf-8"
	}
}

func contentDispositionAttachment(fileName string) string {
	escaped := strings.ReplaceAll(fileName, `"`, "")
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, escaped, url.PathEscape(fileName))
}

func sanitizeExportFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"/", "_", `\`, "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_",
	)
	name = replacer.Replace(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 {
			return '_'
		}
		return r
	}, name)
	return strings.Trim(name, ". ")
}

func intsHeader(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func apiExportResultFromExp(result *exp.Result) apiExportResult {
	if result == nil {
		return apiExportResult{}
	}
	return apiExportResult{
		Path:     result.Path,
		Name:     filepath.Base(result.Path),
		Chapters: result.Chapters,
		Bytes:    result.Bytes,
		Skipped:  append([]int(nil), result.Skipped...),
	}
}

func parseNonNegativeFormInt(raw, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return n, nil
}
