package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const simulationSearchResultLimit = 5

var errSimulationSearchUnavailable = errors.New("仿写语料搜索尚未配置")

type simulationSearchResult struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	FileType string `json:"file_type"`
	Size     string `json:"size,omitempty"`
}

type downloadedSimulationSource struct {
	Name string
	Path string
	Size int64
}

type simulationSourceDownloader interface {
	Search(context.Context, string, int) ([]simulationSearchResult, error)
	Download(context.Context, string, string) (downloadedSimulationSource, error)
}

type unavailableSimulationSourceDownloader struct{}

func (unavailableSimulationSourceDownloader) Search(context.Context, string, int) ([]simulationSearchResult, error) {
	return nil, errSimulationSearchUnavailable
}

func (unavailableSimulationSourceDownloader) Download(context.Context, string, string) (downloadedSimulationSource, error) {
	return downloadedSimulationSource{}, errSimulationSearchUnavailable
}

type simulationSearchRequest struct {
	FileName string `json:"file_name"`
}

type simulationSearchDownloadRequest struct {
	ResultID string `json:"result_id"`
}

func (s *Server) handleProjectSimulationSearch(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, _, err := s.sessions.Open(id); err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	var request simulationSearchRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	query, err := simulationSearchQuery(request.FileName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	results, err := s.sourceDownloader.Search(r.Context(), query, simulationSearchResultLimit)
	if err != nil {
		writeSimulationSearchError(w, err)
		return
	}
	txtResults := make([]simulationSearchResult, 0, simulationSearchResultLimit)
	for _, result := range results {
		if strings.EqualFold(result.FileType, "txt") || strings.EqualFold(filepath.Ext(result.Name), ".txt") {
			result.FileType = "txt"
			txtResults = append(txtResults, result)
			if len(txtResults) == simulationSearchResultLimit {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   query,
		"results": txtResults,
	})
}

func (s *Server) handleProjectSimulationSearchDownload(w http.ResponseWriter, r *http.Request, id string) {
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

	var request simulationSearchDownloadRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resultID := strings.TrimSpace(request.ResultID)
	if resultID == "" {
		writeError(w, http.StatusBadRequest, "result_id is required")
		return
	}
	temporaryDir, err := os.MkdirTemp(s.runtimeRoot, ".simulation-download-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create temporary download directory")
		return
	}
	defer os.RemoveAll(temporaryDir)

	downloaded, err := s.sourceDownloader.Download(r.Context(), resultID, temporaryDir)
	if err != nil {
		writeSimulationSearchError(w, err)
		return
	}
	upload, err := pendingDownloadedSimulationSource(downloaded, projectSimulateDir(manifest))
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	if err := writePendingUploads([]pendingUpload{upload}, projectSimulateDir(manifest)); err != nil {
		writeUploadValidationError(w, err)
		return
	}
	splitReport, err := prepareSimulationSourcesForAnalysis(projectSimulateDir(manifest))
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	files, err := projectSimulationSourceFiles(projectSimulateDir(manifest))
	if err != nil {
		writeUploadValidationError(w, err)
		return
	}
	message := fmt.Sprintf("已下载并加入语料：%s", upload.Name)
	if splitReport.SplitFiles > 0 {
		message = fmt.Sprintf("%s；已自动拆分为 %d 个有序分片", message, splitReport.Parts)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"files":   files,
		"message": message,
	})
}

func simulationSearchQuery(value string) (string, error) {
	query := strings.TrimSpace(value)
	if query == "" {
		return "", errors.New("文件名不能为空")
	}
	query = strings.TrimSuffix(query, ".txt")
	query = strings.TrimRight(query, "。.!！?？；;，,")
	query = strings.TrimSpace(query)
	return query, nil
}

func pendingDownloadedSimulationSource(downloaded downloadedSimulationSource, targetDir string) (pendingUpload, error) {
	name, err := sanitizeUploadedFilename(downloaded.Name, map[string]struct{}{`.txt`: {}})
	if err != nil {
		return pendingUpload{}, fmt.Errorf("请选择 TXT 文件进行下载: %w", err)
	}
	if strings.ToLower(filepath.Ext(downloaded.Path)) != ".txt" {
		return pendingUpload{}, errors.New("请选择 TXT 文件进行下载")
	}
	data, err := os.ReadFile(downloaded.Path)
	if err != nil {
		return pendingUpload{}, fmt.Errorf("read downloaded source: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return pendingUpload{}, errors.New("下载的 TXT 文件为空")
	}
	if downloaded.Size > 0 && downloaded.Size != int64(len(data)) {
		return pendingUpload{}, errors.New("下载文件大小校验失败")
	}
	if existing, ok, err := existingFilename(targetDir, name); err != nil {
		return pendingUpload{}, err
	} else if ok {
		return pendingUpload{}, fmt.Errorf("duplicate file name %q already exists", existing)
	}
	if _, err := safeUploadTarget(targetDir, name); err != nil {
		return pendingUpload{}, err
	}
	return pendingUpload{
		apiUploadedFile: apiUploadedFile{
			Name:         name,
			OriginalName: downloaded.Name,
			Size:         int64(len(data)),
			RelativePath: filepath.ToSlash(name),
		},
		data: data,
	}, nil
}

func writeSimulationSearchError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, errSimulationSearchUnavailable) {
		status = http.StatusServiceUnavailable
	}
	writeError(w, status, err.Error())
}
