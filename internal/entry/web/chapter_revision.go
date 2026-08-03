package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type apiChapterRevision struct {
	Chapter         int    `json:"chapter"`
	Instruction     string `json:"instruction"`
	Mode            string `json:"mode"`
	Label           string `json:"label,omitempty"`
	PendingRewrites []int  `json:"pending_rewrites,omitempty"`
	StaleNotice     string `json:"stale_notice,omitempty"`
}

type apiChapterContent struct {
	Chapter   int    `json:"chapter"`
	Content   string `json:"content"`
	WordCount int    `json:"word_count"`
	Source    string `json:"source,omitempty"`
}

func (s *Server) handleProjectChapter(w http.ResponseWriter, r *http.Request, id, rawChapter string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	chapter, err := strconv.Atoi(strings.TrimSpace(rawChapter))
	if err != nil || chapter <= 0 {
		writeError(w, http.StatusBadRequest, "chapter must be > 0")
		return
	}
	manifest, err := s.store.OpenProject(id)
	if err != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, err))
		return
	}
	st := storepkg.NewStore(manifest.OutputDir)
	content, err := st.Drafts.LoadChapterText(chapter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	source := "final"
	if content == "" {
		content, err = st.Drafts.LoadDraft(chapter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if content != "" {
			source = "draft"
		} else {
			source = ""
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest,
		"chapter": apiChapterContent{
			Chapter:   chapter,
			Content:   content,
			WordCount: utf8.RuneCountInString(content),
			Source:    source,
		},
	})
}

func (s *Server) handleProjectChapterRevise(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeChapterRevisionRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	manifest, openErr := s.store.OpenProject(id)
	if openErr != nil {
		writeProjectSessionError(w, fmt.Errorf("%w: %v", ErrProjectNotFound, openErr))
		return
	}
	st := storepkg.NewStore(manifest.OutputDir)
	stableID, stableErr := stableChapterIDForNumber(st, req.Chapter)
	if stableErr != nil {
		writeManuscriptError(w, stableErr)
		return
	}
	{
		kind := domain.ManuscriptInstructionRewrite
		if req.Mode == host.ChapterRevisionModePolish {
			kind = domain.ManuscriptInstructionPolish
		}
		keyPayload, _ := json.Marshal(struct {
			ChapterID   string
			Instruction string
			Kind        domain.ManuscriptInstructionKind
		}{stableID, req.Instruction, kind})
		session, _, sessionErr := s.sessions.Open(id)
		if sessionErr != nil {
			writeProjectSessionError(w, sessionErr)
			return
		}
		service := session.ManuscriptRevisionService()
		if service == nil {
			writeManuscriptError(w, fmt.Errorf("production manuscript writer and auditor are unavailable"))
			return
		}
		preview, previewErr := service.PreviewContext(r.Context(), host.ManuscriptPreviewRequest{
			ChapterID: stableID, Instruction: req.Instruction, Kind: kind,
		}, "legacy-chapter-revise:"+domain.ContentSignature(keyPayload))
		if previewErr != nil {
			writeManuscriptError(w, previewErr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"project": manifest, "preview": preview, "awaiting_confirmation": true, "running": false,
		})
		return
	}
}

func decodeChapterRevisionRequest(r *http.Request) (host.ChapterRevisionRequest, error) {
	var req struct {
		Chapter     int    `json:"chapter"`
		Instruction string `json:"instruction"`
		Mode        string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return host.ChapterRevisionRequest{}, fmt.Errorf("invalid chapter revision request: %w", err)
	}
	out := host.ChapterRevisionRequest{
		Chapter:     req.Chapter,
		Instruction: strings.TrimSpace(req.Instruction),
		Mode:        strings.TrimSpace(req.Mode),
	}
	if out.Chapter <= 0 {
		return out, fmt.Errorf("chapter must be > 0")
	}
	if out.Instruction == "" {
		return out, fmt.Errorf("instruction is required")
	}
	if out.Mode != "" && out.Mode != host.ChapterRevisionModeRewrite && out.Mode != host.ChapterRevisionModePolish {
		return out, fmt.Errorf("mode must be %q or %q", host.ChapterRevisionModeRewrite, host.ChapterRevisionModePolish)
	}
	return out, nil
}

func apiChapterRevisionFromHost(result host.ChapterRevisionResult) apiChapterRevision {
	return apiChapterRevision{
		Chapter:         result.Chapter,
		Instruction:     result.Instruction,
		Mode:            result.Mode,
		Label:           result.Label,
		PendingRewrites: append([]int(nil), result.PendingRewrites...),
		StaleNotice:     result.StaleNotice,
	}
}

func writeChapterRevisionError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, ErrSessionActionInProgress) {
		status = http.StatusConflict
	} else if errors.Is(err, errs.ErrToolArgs) {
		status = http.StatusBadRequest
	} else if errors.Is(err, errs.ErrToolPrecondition) || errors.Is(err, errs.ErrToolConflict) {
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}
