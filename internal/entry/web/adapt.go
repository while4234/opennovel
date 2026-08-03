package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type apiAdaptationEvent struct {
	Time    time.Time `json:"time"`
	Stage   string    `json:"stage"`
	Current int       `json:"current"`
	Total   int       `json:"total"`
	Message string    `json:"message"`
	Error   string    `json:"error,omitempty"`
}

type adaptationRunError struct {
	message string
}

func (e adaptationRunError) Error() string {
	return e.message
}

type adaptationPausedError struct {
	message string
}

func (e adaptationPausedError) Error() string {
	return e.message
}

type apiAdaptationStatus struct {
	SourceFile     *apiUploadedFile     `json:"source_file,omitempty"`
	AnalysisStatus string               `json:"analysis_status"`
	AnalysisEvents []apiAdaptationEvent `json:"analysis_events,omitempty"`
	Message        string               `json:"message,omitempty"`
}

type adaptationProposalRequestMeta struct {
	Async          bool
	IdempotencyKey string
}

func (s *Server) handleProjectAdaptSource(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	finishAction, err := session.beginActionKind(projectActionKindAdaptationUpload)
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

	sourceDir := projectAdaptationUploadDir(manifest)
	uploads, err := readPendingUploads(headers, textUploadExtensions, maxTextUploadBytes, sourceDir)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	if err := writePendingUploads(uploads, sourceDir); err != nil {
		writeUploadValidationError(w, err)
		return
	}
	sourceFile := uploads[0].apiUploadedFile
	writeJSON(w, http.StatusOK, map[string]any{
		"project":     manifest,
		"source_file": sourceFile,
		"files":       []apiUploadedFile{sourceFile},
		"message":     "uploaded adaptation source file",
	})
}

func (s *Server) handleProjectAdaptAnalyze(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sourcePath, force, err := adaptationAnalyzeRequest(r, manifest)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	status, err := projectAdaptationStatus(manifest, false)
	if err != nil {
		writeAdaptationActionError(w, err, nil)
		return
	}
	if status.AnalysisStatus == "done" && !force {
		matches, err := preparedAdaptationSourceMatches(manifest, sourcePath)
		if err != nil {
			writeAdaptationActionError(w, err, status.AnalysisEvents)
			return
		}
		if matches {
			finishAction, beginErr := session.beginAction()
			if beginErr != nil {
				writeAdaptationActionError(w, beginErr, status.AnalysisEvents)
				return
			}
			item, err := s.autoSaveAnalyzedNovel(session, manifest, sourcePath)
			finishAction()
			if err != nil {
				writeAdaptationActionError(w, err, status.AnalysisEvents)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"project":       manifest,
				"snapshot":      session.WebSnapshot(),
				"adaptation":    status,
				"events":        status.AnalysisEvents,
				"running":       false,
				"accepted":      false,
				"analyzed":      true,
				"library_saved": true,
				"library_item":  item,
			})
			return
		}
	}
	if err := session.StartPrepareAdaptationSourceWithCompletion(sourcePath, func() error {
		_, err := s.autoSaveAnalyzedNovel(session, manifest, sourcePath)
		return err
	}); err != nil {
		writeAdaptationActionError(w, err, nil)
		return
	}
	status, err = projectAdaptationStatus(manifest, session.isActionRunning(projectActionKindAdaptationAnalysis))
	if err != nil {
		writeAdaptationActionError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":    manifest,
		"snapshot":   session.WebSnapshot(),
		"adaptation": status,
		"events":     status.AnalysisEvents,
		"running":    true,
		"accepted":   true,
	})
}

func (s *Server) autoSaveAnalyzedNovel(session *ProjectSession, manifest ProjectManifest, sourcePath string) (apiLibraryItem, error) {
	item, err := s.libraries.UpsertAnalyzedNovelFromProject(manifest, filepath.Base(sourcePath))
	if err != nil {
		return apiLibraryItem{}, fmt.Errorf("auto-save analyzed novel %q: %w", filepath.Base(sourcePath), err)
	}
	session.appendLibraryEvent(
		"novel_auto_save",
		fmt.Sprintf("已自动保存小说库：%s（%d 章）", item.Name, item.ChapterCount),
		fmt.Sprintf("source_file=%s", item.SourceFile),
		"success",
	)
	return item, nil
}

func (s *Server) handleProjectAdaptStart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Mode       string `json:"mode"`
		Brief      string `json:"brief"`
		SourceFile string `json:"source_file"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid adaptation start request: "+err.Error())
			return
		}
	}
	mode := strings.TrimSpace(req.Mode)
	rewritePolicy, err := adaptationRewritePolicyForMode(mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	brief := strings.TrimSpace(req.Brief)
	if brief == "" {
		writeError(w, http.StatusBadRequest, "adaptation brief is required")
		return
	}

	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sourcePath, err := adaptationSourcePathFromName(req.SourceFile, manifest, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := session.StartAdaptationPrepared(adapt.ProposalOptions{
		Brief:         brief,
		SourcePath:    sourcePath,
		Granularity:   mode,
		RewritePolicy: rewritePolicy,
		WordTolerance: adapt.DefaultWordTolerance,
	}); err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.WebSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":        manifest,
		"snapshot":       snapshot,
		"mode":           mode,
		"rewrite_policy": rewritePolicy,
		"running":        snapshot.IsRunning,
	})
}

func (s *Server) handleProjectAdaptProposal(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		s.handleProjectBackgroundAction(w, r, id)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	options, mode, rewritePolicy, meta, err := decodeAdaptationProposalRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sourcePath, err := adaptationSourcePathFromName(options.SourcePath, manifest, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	options.SourcePath = sourcePath
	if meta.Async {
		action, created, err := session.StartBackgroundAction("adaptation_proposal_generate", meta.IdempotencyKey, func(ctx context.Context) error {
			if readyErr := ensureAdaptationCoCreateBriefingReady(ctx, session, sourcePath, options); readyErr != nil {
				return readyErr
			}
			_, buildErr := session.BuildAdaptationProposalContext(ctx, options)
			return buildErr
		})
		if err != nil {
			writeBackgroundActionStartError(w, err)
			return
		}
		writeBackgroundActionAccepted(w, r, manifest, session, action, created)
		return
	}
	if err := ensureAdaptationCoCreateBriefingReady(r.Context(), session, sourcePath, options); err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	proposal, err := session.BuildAdaptationProposalContext(r.Context(), options)
	if err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.WebSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":        manifest,
		"snapshot":       snapshot,
		"proposal":       proposal,
		"mode":           mode,
		"rewrite_policy": rewritePolicy,
		"running":        snapshot.IsRunning,
	})
}

func (s *Server) handleProjectAdaptProposalVolumes(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		s.handleProjectBackgroundAction(w, r, id)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	options, mode, rewritePolicy, meta, err := decodeAdaptationProposalRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	sourcePath, err := adaptationSourcePathFromName(options.SourcePath, manifest, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	options.SourcePath = sourcePath
	if meta.Async {
		action, created, err := session.StartBackgroundAction("adaptation_volume_proposal_generate", meta.IdempotencyKey, func(ctx context.Context) error {
			if readyErr := ensureAdaptationCoCreateBriefingReady(ctx, session, sourcePath, options); readyErr != nil {
				return readyErr
			}
			_, buildErr := session.BuildAdaptationProposalVolumesContext(ctx, options)
			return buildErr
		})
		if err != nil {
			writeBackgroundActionStartError(w, err)
			return
		}
		writeBackgroundActionAccepted(w, r, manifest, session, action, created)
		return
	}
	if err := ensureAdaptationCoCreateBriefingReady(r.Context(), session, sourcePath, options); err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	result, err := session.BuildAdaptationProposalVolumesContext(r.Context(), options)
	if err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.WebSnapshot()
	response := map[string]any{
		"project":        manifest,
		"snapshot":       snapshot,
		"mode":           mode,
		"rewrite_policy": rewritePolicy,
		"running":        snapshot.IsRunning,
	}
	if result != nil {
		if result.Proposal != nil {
			response["proposal"] = result.Proposal
		}
		if result.VolumeReview != nil {
			response["volume_review"] = result.VolumeReview
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleProjectAdaptProposalVolumesRevise(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		VolumeIndex int    `json:"volume_index"`
		Instruction string `json:"instruction"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid adaptation volume review revision request: "+err.Error())
			return
		}
	}
	options := adapt.ProposalRevisionOptions{
		VolumeIndex: req.VolumeIndex,
		Instruction: strings.TrimSpace(req.Instruction),
	}
	if options.VolumeIndex <= 0 {
		writeError(w, http.StatusBadRequest, "volume_index is required")
		return
	}
	if options.Instruction == "" {
		writeError(w, http.StatusBadRequest, "revision instruction is required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	review, err := session.ReviseAdaptationVolumeReviewContext(r.Context(), options)
	if err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.WebSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":       manifest,
		"snapshot":      snapshot,
		"volume_review": review,
		"running":       snapshot.IsRunning,
	})
}

func (s *Server) handleProjectAdaptProposalDetails(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	proposal, err := session.BuildAdaptationProposalDetailsContext(r.Context())
	if err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.WebSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"proposal": proposal,
		"running":  snapshot.IsRunning,
	})
}

func (s *Server) handleProjectAdaptProposalRevise(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Target      string `json:"target"`
		FromChapter int    `json:"from_chapter"`
		ToChapter   int    `json:"to_chapter"`
		VolumeIndex int    `json:"volume_index"`
		Instruction string `json:"instruction"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid adaptation proposal revision request: "+err.Error())
			return
		}
	}
	options := adapt.ProposalRevisionOptions{
		Target:      strings.TrimSpace(req.Target),
		FromChapter: req.FromChapter,
		ToChapter:   req.ToChapter,
		VolumeIndex: req.VolumeIndex,
		Instruction: strings.TrimSpace(req.Instruction),
	}
	if options.Instruction == "" {
		writeError(w, http.StatusBadRequest, "revision instruction is required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	proposal, err := session.ReviseAdaptationProposalContext(r.Context(), options)
	if err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.WebSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"proposal": proposal,
		"running":  snapshot.IsRunning,
	})
}

func (s *Server) handleProjectAdaptConfirm(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	plan, err := session.ConfirmAdaptationProposal()
	if err != nil {
		writeAdaptationStartError(w, err)
		return
	}
	snapshot := session.WebSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"plan":     plan,
		"running":  snapshot.IsRunning,
	})
}

func decodeAdaptationProposalRequest(r *http.Request) (adapt.ProposalOptions, string, string, adaptationProposalRequestMeta, error) {
	var req struct {
		Mode           string `json:"mode"`
		Brief          string `json:"brief"`
		SourceFile     string `json:"source_file"`
		Async          bool   `json:"async,omitempty"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return adapt.ProposalOptions{}, "", "", adaptationProposalRequestMeta{}, fmt.Errorf("invalid adaptation proposal request: %w", err)
		}
	}
	mode := strings.TrimSpace(req.Mode)
	rewritePolicy, err := adaptationRewritePolicyForMode(mode)
	if err != nil {
		return adapt.ProposalOptions{}, "", "", adaptationProposalRequestMeta{}, err
	}
	brief := strings.TrimSpace(req.Brief)
	if brief == "" {
		return adapt.ProposalOptions{}, "", "", adaptationProposalRequestMeta{}, fmt.Errorf("adaptation brief is required")
	}
	return adapt.ProposalOptions{
			Brief:         brief,
			SourcePath:    strings.TrimSpace(req.SourceFile),
			Granularity:   mode,
			RewritePolicy: rewritePolicy,
			WordTolerance: adapt.DefaultWordTolerance,
		}, mode, rewritePolicy, adaptationProposalRequestMeta{
			Async:          req.Async,
			IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
		}, nil
}

func ensureAdaptationCoCreateBriefingReady(ctx context.Context, session *ProjectSession, sourcePath string, options adapt.ProposalOptions) error {
	if session == nil {
		return fmt.Errorf("project session is required")
	}
	intent := adapt.BuildCoCreateIntent(options.Brief, options.Granularity, options.RewritePolicy, options.WordTolerance)
	briefing, err := session.host.EnsureAdaptationProposalCoCreateBriefing(ctx, sourcePath, intent)
	if err != nil {
		return fmt.Errorf("prepare adaptation co-create briefing: %w", err)
	}
	if pending := adapt.PendingCoCreateBriefingDecisions(briefing); len(pending) > 0 {
		return fmt.Errorf("adaptation co-create briefing has %d pending decisions; resolve them in Adapt co-create first", len(pending))
	}
	return nil
}

func adaptationAnalyzeRequest(r *http.Request, manifest ProjectManifest) (string, bool, error) {
	var req struct {
		SourceFile string `json:"source_file"`
		Force      bool   `json:"force,omitempty"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return "", false, fmt.Errorf("invalid adaptation analyze request: %w", err)
		}
	}
	sourceDir := projectAdaptationUploadDir(manifest)
	if strings.TrimSpace(req.SourceFile) == "" {
		sourcePath, err := onlyAdaptationSourcePath(sourceDir)
		return sourcePath, req.Force, err
	}
	sourcePath, err := adaptationSourcePathFromName(req.SourceFile, manifest, true)
	return sourcePath, req.Force, err
}

func adaptationSourcePathFromName(sourceFile string, manifest ProjectManifest, allowInfer bool) (string, error) {
	sourceDir := projectAdaptationUploadDir(manifest)
	if strings.TrimSpace(sourceFile) == "" {
		if !allowInfer {
			return "", fmt.Errorf("source_file is required")
		}
		return onlyAdaptationSourcePath(sourceDir)
	}
	name, err := sanitizeUploadedFilename(sourceFile, textUploadExtensions)
	if err != nil {
		return "", err
	}
	sourcePath, err := safeUploadTarget(sourceDir, name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("adaptation source file %q was not uploaded", name)
		}
		return "", fmt.Errorf("stat adaptation source file %q: %w", name, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("adaptation source file %q is a directory", name)
	}
	return sourcePath, nil
}

func onlyAdaptationSourcePath(sourceDir string) (string, error) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("upload an adaptation source file before analysis")
		}
		return "", fmt.Errorf("list adaptation source files: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := textUploadExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; ok {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("upload an adaptation source file before analysis")
	}
	if len(names) > 1 {
		return "", fmt.Errorf("source_file is required when multiple adaptation source files exist")
	}
	return safeUploadTarget(sourceDir, names[0])
}

func apiAdaptationEventFromAdapt(ev adapt.Event) apiAdaptationEvent {
	api := apiAdaptationEvent{
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

func projectAdaptationUploadDir(manifest ProjectManifest) string {
	return filepath.Join(manifest.RootDir, "uploads", "adaptation")
}

func projectAdaptationStatus(manifest ProjectManifest, analysisRunning bool) (apiAdaptationStatus, error) {
	status := apiAdaptationStatus{AnalysisStatus: "idle"}
	sourceDir := projectAdaptationUploadDir(manifest)
	st := storepkg.NewStore(manifest.OutputDir)

	applyRunning := func() {
		if !analysisRunning {
			return
		}
		status.AnalysisStatus = "running"
		status.Message = "原文分析进行中"
		status.AnalysisEvents = adaptationStatusEvents("running", latestAdaptationTotal(status.AnalysisEvents))
	}

	adaptationManifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return status, err
	}
	if adaptationManifest != nil {
		sourceFile, err := uploadedFileFromPath(sourceDir, adaptationManifest.SourcePath)
		if err != nil {
			return status, err
		}
		status.SourceFile = sourceFile
		status.AnalysisStatus = adaptationAnalysisStatus(st, adaptationManifest)
		status.Message = adaptationStatusMessage(status.AnalysisStatus)
		status.AnalysisEvents = adaptationStatusEvents(status.AnalysisStatus, adaptationManifest.ChapterCount)
		applyRunning()
		return status, nil
	}

	sourceFile, err := latestAdaptationUpload(sourceDir)
	if err != nil {
		return status, err
	}
	if sourceFile != nil {
		status.SourceFile = sourceFile
		status.Message = "已恢复上传原文"
	}
	applyRunning()
	return status, nil
}

func adaptationAnalysisStatus(st *storepkg.Store, manifest *domain.AdaptationSourceManifest) string {
	if manifest == nil || manifest.ChapterCount <= 0 {
		return "idle"
	}
	reports, err := st.Adaptation.LoadCompleteSourceReports()
	if err != nil || len(reports) != manifest.ChapterCount {
		return "paused"
	}
	foundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil || foundation == nil {
		return "paused"
	}
	current, err := st.Adaptation.CoCreateDossierCurrent(adapt.CoCreateDossierPromptVersion, adapt.CoCreateDossierBatchSize, adapt.CoCreateDossierBatchRuneLimit)
	if err != nil || !current {
		return "paused"
	}
	return "done"
}

func preparedAdaptationSourceMatches(manifest ProjectManifest, sourcePath string) (bool, error) {
	st := storepkg.NewStore(manifest.OutputDir)
	adaptationManifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || adaptationManifest == nil {
		return false, err
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return false, nil
	}
	absPath, err := filepath.Abs(sourcePath)
	if err == nil {
		sourcePath = absPath
	}
	return sameAdaptationSourcePath(adaptationManifest.SourcePath, sourcePath), nil
}

func sameAdaptationSourcePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func adaptationStatusMessage(status string) string {
	switch status {
	case "running":
		return "原文分析进行中"
	case "done":
		return "已恢复原文分析"
	case "paused":
		return "已恢复部分原文分析，可继续分析"
	default:
		return "已恢复上传原文"
	}
}

func adaptationStatusEvents(status string, total int) []apiAdaptationEvent {
	if status != "done" && status != "paused" && status != "running" {
		return nil
	}
	stage := adapt.StageDone
	current := total
	message := "原文分析已恢复"
	if status == "running" {
		stage = adapt.StageChapter
		current = 0
		message = "原文分析进行中"
	} else if status == "paused" {
		stage = adapt.StagePaused
		current = 0
		message = "原文分析未完成，可再次点击分析继续"
	}
	return []apiAdaptationEvent{{
		Time:    time.Now().UTC(),
		Stage:   string(stage),
		Current: current,
		Total:   total,
		Message: message,
	}}
}

func latestAdaptationTotal(events []apiAdaptationEvent) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Total > 0 {
			return events[i].Total
		}
	}
	return 0
}

func uploadedFileFromPath(sourceDir, sourcePath string) (*apiUploadedFile, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return nil, nil
	}
	if !filepath.IsAbs(sourcePath) {
		sourcePath = filepath.Join(sourceDir, sourcePath)
	}
	sourcePath = filepath.Clean(sourcePath)
	if !isSameOrChild(sourceDir, sourcePath) || filepath.Clean(sourceDir) == sourcePath {
		return nil, nil
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}
	rel, err := filepath.Rel(sourceDir, sourcePath)
	if err != nil {
		return nil, err
	}
	file := apiUploadedFile{
		Name:         filepath.Base(sourcePath),
		OriginalName: filepath.Base(sourcePath),
		Size:         info.Size(),
		RelativePath: filepath.ToSlash(rel),
	}
	return &file, nil
}

func latestAdaptationUpload(sourceDir string) (*apiUploadedFile, error) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var latest *apiUploadedFile
	var latestMod time.Time
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := textUploadExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		file := apiUploadedFile{
			Name:         entry.Name(),
			OriginalName: entry.Name(),
			Size:         info.Size(),
			RelativePath: filepath.ToSlash(entry.Name()),
		}
		if latest == nil || info.ModTime().After(latestMod) || (info.ModTime().Equal(latestMod) && file.Name > latest.Name) {
			latest = &file
			latestMod = info.ModTime()
		}
	}
	return latest, nil
}

func adaptationRewritePolicyForMode(mode string) (string, error) {
	switch mode {
	case domain.AdaptationGranularityChapter:
		return domain.AdaptationRewritePreserveDetails, nil
	case domain.AdaptationGranularityArc, domain.AdaptationGranularityFree:
		return domain.AdaptationRewriteFullRewrite, nil
	default:
		return "", fmt.Errorf("adaptation mode must be one of chapter, arc, free")
	}
}

func writeAdaptationActionError(w http.ResponseWriter, err error, events []apiAdaptationEvent) {
	status := http.StatusInternalServerError
	var runErr adaptationRunError
	var pausedErr adaptationPausedError
	switch {
	case errors.Is(err, ErrSessionActionInProgress):
		status = http.StatusConflict
	case errors.As(err, &pausedErr):
		status = http.StatusConflict
	case errors.As(err, &runErr):
		status = http.StatusBadRequest
	default:
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{
		"error":  err.Error(),
		"events": events,
	})
}

func writeAdaptationStartError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSessionActionInProgress) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusConflict, err.Error())
}
