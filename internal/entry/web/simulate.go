package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/host/sim"
)

const (
	unlimitedUploadBytes    int64 = 0
	maxMultipartUploadBytes       = 64 << 20
	maxTextUploadBytes            = unlimitedUploadBytes
	maxProfileUploadBytes         = 5 << 20
	maxMultipartMemory            = 8 << 20
)

var (
	textUploadExtensions    = map[string]struct{}{".txt": {}, ".md": {}, ".markdown": {}}
	profileUploadExtensions = map[string]struct{}{".json": {}}
)

type apiUploadedFile struct {
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	Size         int64  `json:"size"`
	RelativePath string `json:"relative_path"`
}

type apiSimulationEvent struct {
	Time    time.Time `json:"time"`
	Stage   string    `json:"stage"`
	Current int       `json:"current"`
	Total   int       `json:"total"`
	Message string    `json:"message"`
	Error   string    `json:"error,omitempty"`
}

type apiSimulationStatus struct {
	ImportedFile   *apiUploadedFile     `json:"imported_file,omitempty"`
	Files          []apiUploadedFile    `json:"files,omitempty"`
	AnalysisStatus string               `json:"analysis_status"`
	AnalysisEvents []apiSimulationEvent `json:"analysis_events,omitempty"`
	ImportStatus   string               `json:"import_status"`
	ImportEvents   []apiSimulationEvent `json:"import_events,omitempty"`
	Message        string               `json:"message,omitempty"`
}

type pendingUpload struct {
	apiUploadedFile
	data []byte
}

type simulationRunError struct {
	message string
}

func (e simulationRunError) Error() string {
	return e.message
}

func (s *Server) handleProjectSimulateFiles(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	finishAction, err := session.beginActionKind(projectActionKindSimulationUpload)
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
	if len(headers) == 0 {
		writeError(w, http.StatusBadRequest, "at least one .txt or .md file is required")
		return
	}

	simulateDir := projectSimulateDir(manifest)
	uploads, err := readPendingUploads(headers, textUploadExtensions, maxTextUploadBytes, simulateDir)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	if err := writePendingUploads(uploads, simulateDir); err != nil {
		writeUploadValidationError(w, err)
		return
	}
	splitReport, err := prepareSimulationSourcesForAnalysis(simulateDir)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	files, err := projectSimulationSourceFiles(simulateDir)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	message := fmt.Sprintf("uploaded %d simulation source file(s)", len(uploads))
	if splitReport.SplitFiles > 0 {
		message = fmt.Sprintf("%s; split %d long file(s) into %d ordered part(s)", message, splitReport.SplitFiles, splitReport.Parts)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"files":   files,
		"message": message,
	})
}

func (s *Server) handleProjectSimulateAnalyze(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	finishAction, err := session.beginActionKind(projectActionKindSimulationAnalysis)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	transferred := false
	defer func() {
		if !transferred {
			finishAction()
		}
	}()
	var request struct {
		Action string `json:"action"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSONBody(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid simulation refresh request")
			return
		}
	}
	action := strings.TrimSpace(request.Action)
	if action == "" {
		action = sim.ActionScan
	}
	if action != sim.ActionScan && action != sim.ActionResynthesize && action != sim.ActionReanalyze {
		writeError(w, http.StatusBadRequest, "unsupported simulation refresh action")
		return
	}
	if _, err := prepareSimulationSourcesForAnalysis(projectSimulateDir(manifest)); err != nil {
		writeSimulationActionError(w, err, nil)
		return
	}
	files, err := projectSimulationSourceFiles(projectSimulateDir(manifest))
	if err != nil {
		writeSimulationActionError(w, err, nil)
		return
	}
	if len(files) == 0 {
		writeError(w, http.StatusConflict, "当前项目没有可分析的本地语料")
		return
	}
	if action == sim.ActionResynthesize {
		summary := session.Snapshot().SimulationSummary
		if summary == nil || !summary.Actions.Resynthesize.Enabled {
			reason := "现有逐篇报告不可复用，请选择全量重分析"
			if summary != nil && summary.Actions.Resynthesize.Reason != "" {
				reason = summary.Actions.Resynthesize.Reason
			}
			writeError(w, http.StatusConflict, reason)
			return
		}
	}
	onComplete := func(events []apiSimulationEvent, runErr error) {
		if runErr != nil || !simulationAnalysisChangedProfile(events) {
			return
		}
		item, syncErr := s.libraries.SyncSimulationProfileFromProject(manifest)
		if syncErr != nil {
			session.appendLibraryEvent(
				"simulation_auto_sync",
				"仿写画像分析完成，但自动入库失败",
				syncErr.Error(),
				"warn",
			)
			return
		}
		session.appendLibraryEvent(
			"simulation_auto_sync",
			fmt.Sprintf("已自动同步仿写画像库：%s", item.Name),
			fmt.Sprintf("source_count=%d", item.SourceCount),
			"success",
		)
	}
	if err := session.startSimulateFromDirOwnedActionWithCompletion(
		projectSimulateDir(manifest),
		action,
		finishAction,
		onComplete,
	); err != nil {
		writeSimulationActionError(w, err, nil)
		return
	}
	transferred = true
	status, err := projectSimulationStatus(
		manifest,
		session.isActionRunning(projectActionKindSimulationAnalysis),
		session.isActionRunning(projectActionKindSimulationImport),
	)
	if err != nil {
		writeSimulationActionError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":    manifest,
		"snapshot":   session.Snapshot(),
		"simulation": status,
		"events":     status.AnalysisEvents,
		"running":    true,
		"accepted":   true,
	})
}

func simulationAnalysisChangedProfile(events []apiSimulationEvent) bool {
	for _, event := range events {
		if event.Stage == string(sim.StageAnalyze) || event.Stage == string(sim.StageMerge) {
			return true
		}
	}
	return false
}

func (s *Server) handleProjectSimulateImport(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	finishAction, err := session.beginActionKind(projectActionKindSimulationImport)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	transferred := false
	defer func() {
		if !transferred {
			finishAction()
		}
	}()
	headers, cleanup, err := parseMultipartFiles(w, r, maxMultipartUploadBytes)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(headers) != 1 {
		writeError(w, http.StatusBadRequest, "exactly one simulation profile JSON file is required")
		return
	}

	importedDir := projectImportedProfilesDir(manifest)
	uploads, err := readPendingUploads(headers, profileUploadExtensions, maxProfileUploadBytes, importedDir)
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	profile := uploads[0]
	if !json.Valid(bytes.TrimSpace(profile.data)) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("profile JSON %q is invalid", profile.Name))
		return
	}
	if err := writePendingUploads(uploads, importedDir); err != nil {
		writeUploadValidationError(w, err)
		return
	}

	if err := session.startImportSimulationProfileOwned(filepath.Join(importedDir, profile.Name), finishAction); err != nil {
		writeSimulationActionError(w, err, nil)
		return
	}
	transferred = true
	libraryItem, librarySaved, libraryWarning := s.trySaveImportedSimulationProfile(profile)
	status, err := projectSimulationStatus(
		manifest,
		session.isActionRunning(projectActionKindSimulationAnalysis),
		session.isActionRunning(projectActionKindSimulationImport),
	)
	if err != nil {
		writeSimulationActionError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":       manifest,
		"snapshot":      session.Snapshot(),
		"imported_file": profile.apiUploadedFile,
		"simulation":    status,
		"events":        status.ImportEvents,
		"running":       true,
		"accepted":      true,
		"library_saved": librarySaved,
		"library_item":  libraryItem,
		"warning":       libraryWarning,
	})
}

func parseMultipartFiles(w http.ResponseWriter, r *http.Request, maxBodyBytes int64) ([]*multipart.FileHeader, func(), error) {
	if maxBodyBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	}
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		return nil, nil, fmt.Errorf("parse multipart upload: %w", err)
	}
	if r.MultipartForm == nil {
		return nil, nil, nil
	}
	cleanup := func() {
		_ = r.MultipartForm.RemoveAll()
	}
	return collectMultipartFileHeaders(r.MultipartForm), cleanup, nil
}

func collectMultipartFileHeaders(form *multipart.Form) []*multipart.FileHeader {
	keys := make([]string, 0, len(form.File))
	for key := range form.File {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	headers := make([]*multipart.FileHeader, 0)
	for _, key := range keys {
		headers = append(headers, form.File[key]...)
	}
	return headers
}

func readPendingUploads(headers []*multipart.FileHeader, allowedExts map[string]struct{}, maxBytes int64, targetDir string) ([]pendingUpload, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("create upload dir: %w", err)
	}
	seen := make(map[string]struct{}, len(headers))
	uploads := make([]pendingUpload, 0, len(headers))
	for _, header := range headers {
		original := rawMultipartFilename(header)
		name, err := sanitizeUploadedFilename(original, allowedExts)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate file name %q in this upload", name)
		}
		seen[key] = struct{}{}
		existing, ok, err := existingFilename(targetDir, name)
		if err != nil {
			return nil, err
		}
		if ok {
			return nil, fmt.Errorf("duplicate file name %q already exists", existing)
		}
		data, err := readMultipartFile(header, maxBytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, fmt.Errorf("%s is empty", name)
		}
		if _, err := safeUploadTarget(targetDir, name); err != nil {
			return nil, err
		}
		uploads = append(uploads, pendingUpload{
			apiUploadedFile: apiUploadedFile{
				Name:         name,
				OriginalName: original,
				Size:         int64(len(data)),
				RelativePath: filepath.ToSlash(name),
			},
			data: data,
		})
	}
	return uploads, nil
}

func writePendingUploads(uploads []pendingUpload, targetDir string) error {
	for _, upload := range uploads {
		target, err := safeUploadTarget(targetDir, upload.Name)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("duplicate file name %q already exists", upload.Name)
			}
			return fmt.Errorf("write %s: %w", upload.Name, err)
		}
		if _, err := file.Write(upload.data); err != nil {
			_ = file.Close()
			return fmt.Errorf("write %s: %w", upload.Name, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("write %s: %w", upload.Name, err)
		}
	}
	return nil
}

func readMultipartFile(header *multipart.FileHeader, maxBytes int64) ([]byte, error) {
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if maxBytes <= 0 {
		return io.ReadAll(file)
	}
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, file, maxBytes+1); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if int64(buf.Len()) > maxBytes {
		return nil, fmt.Errorf("file exceeds %s (%d bytes)", formatByteLimit(maxBytes), maxBytes)
	}
	return buf.Bytes(), nil
}

func formatByteLimit(bytes int64) string {
	const mib = 1 << 20
	if bytes > 0 && bytes%mib == 0 {
		return fmt.Sprintf("%d MiB", bytes/mib)
	}
	return fmt.Sprintf("%d bytes", bytes)
}

func rawMultipartFilename(header *multipart.FileHeader) string {
	disposition := header.Header.Get("Content-Disposition")
	if disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			if filename := strings.TrimSpace(params["filename"]); filename != "" {
				return filename
			}
		}
	}
	return strings.TrimSpace(header.Filename)
}

func sanitizeUploadedFilename(raw string, allowedExts map[string]struct{}) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("file name is required")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("file name %q contains a null byte", name)
	}
	if filepath.IsAbs(name) || isWindowsAbsolutePath(name) {
		return "", fmt.Errorf("file name %q must not be an absolute path", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("file name %q must not contain path separators", name)
	}
	if name == "." || name == ".." || strings.Trim(name, ". ") == "" {
		return "", fmt.Errorf("file name %q is not allowed", name)
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "", fmt.Errorf("file name %q must not end with a dot or space", name)
	}
	for _, r := range name {
		if r < 32 || strings.ContainsRune(`<>:"|?*`, r) {
			return "", fmt.Errorf("file name %q contains an unsafe character", name)
		}
	}
	ext := strings.ToLower(filepath.Ext(name))
	if _, ok := allowedExts[ext]; !ok {
		return "", fmt.Errorf("file name %q has unsupported extension", name)
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if isReservedWindowsName(base) {
		return "", fmt.Errorf("file name %q is reserved", name)
	}
	return name, nil
}

func isWindowsAbsolutePath(path string) bool {
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	if len(path) >= 2 && path[1] == ':' {
		c := path[0]
		return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	}
	return false
}

func isReservedWindowsName(base string) bool {
	candidate := strings.ToUpper(strings.Trim(base, " ."))
	if idx := strings.Index(candidate, "."); idx >= 0 {
		candidate = candidate[:idx]
	}
	switch candidate {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(candidate) == 4 {
		prefix := candidate[:3]
		suffix := candidate[3]
		return (prefix == "COM" || prefix == "LPT") && suffix >= '1' && suffix <= '9'
	}
	return false
}

func safeUploadTarget(dir, name string) (string, error) {
	target := filepath.Join(dir, name)
	if !isSameOrChild(dir, target) || filepath.Clean(target) == filepath.Clean(dir) {
		return "", fmt.Errorf("unsafe upload target %q", name)
	}
	return target, nil
}

func existingFilename(dir, name string) (string, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	want := strings.ToLower(name)
	for _, entry := range entries {
		if strings.ToLower(entry.Name()) == want {
			return entry.Name(), true, nil
		}
	}
	return "", false, nil
}

func apiSimulationEventFromSim(ev sim.Event) apiSimulationEvent {
	api := apiSimulationEvent{
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

func projectSimulateDir(manifest ProjectManifest) string {
	return filepath.Join(manifest.RootDir, "simulate")
}

func projectImportedProfilesDir(manifest ProjectManifest) string {
	return filepath.Join(manifest.RootDir, "profiles", "imported")
}

func projectSimulationStatus(manifest ProjectManifest, analysisRunning bool, importRunning bool) (apiSimulationStatus, error) {
	status := apiSimulationStatus{AnalysisStatus: "idle", ImportStatus: "idle"}
	files, err := projectSimulationSourceFiles(projectSimulateDir(manifest))
	if err != nil {
		return status, err
	}
	status.Files = files
	profilePath, profileErr := findProjectSimulationProfile(manifest)
	if profileErr == nil {
		profile, err := readSimulationProfileFile(profilePath)
		if err != nil {
			return status, err
		}
		sourceCount := len(profile.Corpus.Sources)
		total := sourceCount
		if len(files) > total {
			total = len(files)
		}
		if sourceCount > 0 && len(files) > sourceCount {
			status.AnalysisStatus = "paused"
			status.Message = "已恢复部分仿写画像，可继续分析"
			status.AnalysisEvents = []apiSimulationEvent{{
				Time:    time.Now().UTC(),
				Stage:   string(sim.StageAnalyze),
				Current: sourceCount,
				Total:   total,
				Message: status.Message,
			}}
		} else {
			status.AnalysisStatus = "done"
			status.Message = "已恢复仿写画像"
			status.AnalysisEvents = []apiSimulationEvent{{
				Time:    time.Now().UTC(),
				Stage:   string(sim.StageDone),
				Current: sourceCount,
				Total:   total,
				Message: status.Message,
			}}
		}
	} else if analysisRunning {
		status.Message = "仿写画像分析进行中"
	}
	if analysisRunning {
		status.AnalysisStatus = "running"
		status.Message = "仿写画像分析进行中"
		status.AnalysisEvents = []apiSimulationEvent{{
			Time:    time.Now().UTC(),
			Stage:   string(sim.StageAnalyze),
			Current: 0,
			Total:   len(files),
			Message: status.Message,
		}}
	}
	importedFile, err := latestImportedSimulationProfile(projectImportedProfilesDir(manifest))
	if err != nil {
		return status, err
	}
	if importRunning {
		status.ImportStatus = "running"
		status.ImportEvents = []apiSimulationEvent{{
			Time:    time.Now().UTC(),
			Stage:   string(sim.StageImport),
			Current: 0,
			Total:   1,
			Message: "仿写画像导入进行中",
		}}
		return status, nil
	}
	if importedFile != nil {
		status.ImportedFile = importedFile
		status.ImportStatus = "done"
		message := fmt.Sprintf("已恢复画像：%s", simulationProfileDisplayName(importedFile.Name))
		status.ImportEvents = []apiSimulationEvent{{
			Time:    time.Now().UTC(),
			Stage:   string(sim.StageDone),
			Current: 1,
			Total:   1,
			Message: message,
		}}
		status.Message = message
	}
	return status, nil
}

func projectSimulationSourceFiles(sourceDir string) ([]apiUploadedFile, error) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]apiUploadedFile, 0, len(entries))
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
		files = append(files, apiUploadedFile{
			Name:         entry.Name(),
			OriginalName: entry.Name(),
			Size:         info.Size(),
			RelativePath: filepath.ToSlash(entry.Name()),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return files, nil
}

func latestImportedSimulationProfile(sourceDir string) (*apiUploadedFile, error) {
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
		if _, ok := profileUploadExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
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

func simulationProfileDisplayName(name string) string {
	display := strings.TrimSuffix(name, filepath.Ext(name))
	if strings.TrimSpace(display) == "" {
		return name
	}
	return display
}

func writeUploadValidationError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if strings.Contains(strings.ToLower(err.Error()), "duplicate file name") {
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func writeSimulationActionError(w http.ResponseWriter, err error, events []apiSimulationEvent) {
	status := http.StatusInternalServerError
	var runErr simulationRunError
	switch {
	case errors.Is(err, ErrSessionActionInProgress):
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
