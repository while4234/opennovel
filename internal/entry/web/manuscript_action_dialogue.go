package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/expansionauditorclient"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const (
	manuscriptActionDialogueRelPath = "meta/sessions/manuscript-action-dialogue.json"
	manuscriptActionMaxInitialRunes = 1000
	manuscriptActionMaxAnswerRunes  = 500
	manuscriptActionMaxTotalRunes   = 8000
	manuscriptActionMaxRounds       = 6
	manuscriptActionProseRunes      = 9000
)

var manuscriptActionDialogueLocks sync.Map

type manuscriptActionDialogue struct {
	ID                   string                          `json:"id"`
	Type                 string                          `json:"type"`
	Status               string                          `json:"status"`
	ChapterID            string                          `json:"chapter_id"`
	DisplayChapter       int                             `json:"display_chapter"`
	ContentSignature     string                          `json:"content_signature"`
	ContextSignature     string                          `json:"context_signature"`
	StructureRevision    int                             `json:"structure_revision"`
	StructureSignature   string                          `json:"structure_signature"`
	Version              int                             `json:"version"`
	Round                int                             `json:"round"`
	InitialInput         string                          `json:"initial_input"`
	ResolvedInstruction  string                          `json:"resolved_instruction,omitempty"`
	Questions            []host.ManuscriptActionQuestion `json:"questions,omitempty"`
	Messages             []host.ManuscriptActionMessage  `json:"messages"`
	Expansion            *manuscriptActionExpansion      `json:"expansion,omitempty"`
	Result               json.RawMessage                 `json:"result,omitempty"`
	Error                string                          `json:"error,omitempty"`
	ExecutionKey         string                          `json:"execution_key,omitempty"`
	ExecutionFingerprint string                          `json:"execution_fingerprint,omitempty"`
	ExecutionVersion     int                             `json:"execution_version,omitempty"`
	OriginalChapterLabel string                          `json:"original_chapter_label"`
	CreatedAt            string                          `json:"created_at"`
	UpdatedAt            string                          `json:"updated_at"`
}

type manuscriptActionExpansion struct {
	Location                   domain.ExpansionLocationKind `json:"location"`
	ReferenceIDs               []string                     `json:"reference_ids,omitempty"`
	Adjustment                 domain.ExpansionAdjustment   `json:"adjustment,omitempty"`
	ExpectedStructureRevision  int                          `json:"expected_structure_revision"`
	ExpectedStructureSignature string                       `json:"expected_structure_signature"`
}

type manuscriptActionDialogueReceipt struct {
	Key         string                   `json:"key"`
	Operation   string                   `json:"operation"`
	Fingerprint string                   `json:"fingerprint"`
	Dialogue    manuscriptActionDialogue `json:"dialogue"`
}

type manuscriptActionDialogueDocument struct {
	Dialogue *manuscriptActionDialogue         `json:"dialogue,omitempty"`
	Receipts []manuscriptActionDialogueReceipt `json:"receipts,omitempty"`
}

type createManuscriptActionDialogueRequest struct {
	ChapterID          string                     `json:"chapter_id"`
	ContentSignature   string                     `json:"content_signature"`
	Type               string                     `json:"type"`
	InitialInput       string                     `json:"initial_input"`
	Expansion          *manuscriptActionExpansion `json:"expansion,omitempty"`
	StructureRevision  int                        `json:"structure_revision"`
	StructureSignature string                     `json:"structure_signature"`
	IdempotencyKey     string                     `json:"idempotency_key"`
}

type replyManuscriptActionDialogueRequest struct {
	QuestionID      string `json:"question_id"`
	Answer          string `json:"answer"`
	ExpectedVersion int    `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type commandManuscriptActionDialogueRequest struct {
	ExpectedVersion int    `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type manuscriptActionClarifier interface {
	ClarifyManuscriptAction(context.Context, host.ManuscriptActionClarificationRequest) (host.ManuscriptActionClarification, error)
}

func (s *Server) handleManuscriptActionDialogueRoute(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService, action string) bool {
	const prefix = "manuscript/actions/dialogues"
	if !strings.HasPrefix(action, prefix) {
		return false
	}
	session, _, err := s.sessions.Open(manifest.ID)
	if err != nil {
		writeManuscriptError(w, err)
		return true
	}
	rest := strings.Trim(strings.TrimPrefix(action, prefix), "/")
	lock := manuscriptActionLock(manifest.OutputDir)
	lock.Lock()
	defer lock.Unlock()
	switch {
	case rest == "active":
		s.handleReadManuscriptActionDialogue(w, r, manifest)
	case rest == "":
		s.handleCreateManuscriptActionDialogue(w, r, manifest, st, service, session)
	case strings.HasSuffix(rest, "/reply"):
		s.handleReplyManuscriptActionDialogue(w, r, manifest, st, service, session, strings.TrimSuffix(rest, "/reply"))
	case strings.HasSuffix(rest, "/execute"):
		s.handleExecuteManuscriptActionDialogue(w, r, manifest, st, service, session, strings.TrimSuffix(rest, "/execute"))
	case strings.HasSuffix(rest, "/cancel"):
		s.handleCancelManuscriptActionDialogue(w, r, manifest, strings.TrimSuffix(rest, "/cancel"))
	default:
		http.NotFound(w, r)
	}
	return true
}

func manuscriptActionLock(outputDir string) *sync.Mutex {
	value, _ := manuscriptActionDialogueLocks.LoadOrStore(filepath.Clean(outputDir), &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Server) handleReadManuscriptActionDialogue(w http.ResponseWriter, r *http.Request, manifest ProjectManifest) {
	if r.Method != http.MethodGet {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	document, err := loadManuscriptActionDialogueDocument(manifest.OutputDir)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "dialogue": document.Dialogue})
}

func (s *Server) handleCreateManuscriptActionDialogue(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService, clarifier manuscriptActionClarifier) {
	if r.Method != http.MethodPost {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request createManuscriptActionDialogueRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, "invalid manuscript action dialogue request")
		return
	}
	request.ChapterID = strings.TrimSpace(request.ChapterID)
	request.ContentSignature = strings.TrimSpace(request.ContentSignature)
	request.Type = strings.TrimSpace(request.Type)
	request.InitialInput = strings.TrimSpace(request.InitialInput)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if !manuscriptChapterStableIDPattern.MatchString(request.ChapterID) || request.ContentSignature == "" || request.IdempotencyKey == "" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "chapter_id, content_signature, and idempotency_key are required")
		return
	}
	if request.Type != "polish" && request.Type != "rewrite" && request.Type != "expand" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "type must be polish, rewrite, or expand")
		return
	}
	if request.InitialInput == "" || utf8.RuneCountInString(request.InitialInput) > manuscriptActionMaxInitialRunes {
		writeManuscriptRequestError(w, http.StatusBadRequest, "initial_input must contain 1 to 1000 characters")
		return
	}
	if request.Type == "expand" && request.Expansion == nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, "expansion parameters are required")
		return
	}
	document, err := loadManuscriptActionDialogueDocument(manifest.OutputDir)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	fingerprint := manuscriptActionFingerprint(request)
	if receipt, replayErr := replayManuscriptActionReceipt(document, request.IdempotencyKey, "create", fingerprint); receipt != nil || replayErr != nil {
		if replayErr != nil {
			writeManuscriptRequestError(w, http.StatusConflict, replayErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "dialogue": receipt.Dialogue, "replayed": true})
		return
	}
	if document.Dialogue != nil && !manuscriptActionDialogueTerminal(document.Dialogue.Status) {
		writeManuscriptRequestError(w, http.StatusConflict, "another manuscript action dialogue is still active")
		return
	}
	contextBundle, baseline, err := buildManuscriptActionContext(st, service, request.ChapterID)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if baseline.CurrentProseSHA256 != request.ContentSignature {
		writeManuscriptError(w, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("chapter prose changed; refresh before starting the action")})
		return
	}
	if request.StructureRevision > 0 && (request.StructureRevision != contextBundleStructureRevision(st) || request.StructureSignature != contextBundleStructureSignature(st)) {
		writeManuscriptError(w, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("project structure changed; refresh before starting the action")})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	dialogue := manuscriptActionDialogue{
		ID: manuscriptActionDialogueID(), Type: request.Type, Status: "executing", ChapterID: request.ChapterID,
		DisplayChapter: baseline.DisplayChapter, ContentSignature: baseline.CurrentProseSHA256, ContextSignature: contextBundle.ContextSignature,
		StructureRevision: contextBundleStructureRevision(st), StructureSignature: contextBundleStructureSignature(st), Version: 1, Round: 1,
		InitialInput: request.InitialInput, Expansion: request.Expansion, OriginalChapterLabel: fmt.Sprintf("第 %d 章", baseline.DisplayChapter),
		Messages: []host.ManuscriptActionMessage{{Role: "user", Content: request.InitialInput}}, CreatedAt: now, UpdatedAt: now,
	}
	clarification, err := clarifier.ClarifyManuscriptAction(r.Context(), host.ManuscriptActionClarificationRequest{Action: request.Type, InitialInput: request.InitialInput, Context: contextBundle, Messages: dialogue.Messages})
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	applyManuscriptActionClarification(&dialogue, clarification)
	document.Dialogue = &dialogue
	recordManuscriptActionReceipt(document, request.IdempotencyKey, "create", fingerprint, dialogue)
	if err := saveManuscriptActionDialogueDocument(manifest.OutputDir, document); err != nil {
		writeManuscriptError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": manifest, "dialogue": dialogue})
}

func (s *Server) handleReplyManuscriptActionDialogue(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService, clarifier manuscriptActionClarifier, dialogueID string) {
	if r.Method != http.MethodPost {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request replyManuscriptActionDialogueRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, "invalid manuscript dialogue reply")
		return
	}
	request.QuestionID, request.Answer, request.IdempotencyKey = strings.TrimSpace(request.QuestionID), strings.TrimSpace(request.Answer), strings.TrimSpace(request.IdempotencyKey)
	if request.QuestionID == "" || request.Answer == "" || utf8.RuneCountInString(request.Answer) > manuscriptActionMaxAnswerRunes || request.ExpectedVersion <= 0 || request.IdempotencyKey == "" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "question_id, answer (up to 500 characters), expected_version, and idempotency_key are required")
		return
	}
	document, err := loadManuscriptActionDialogueDocument(manifest.OutputDir)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	fingerprint := manuscriptActionFingerprint(request)
	if receipt, replayErr := replayManuscriptActionReceipt(document, request.IdempotencyKey, "reply", fingerprint); receipt != nil || replayErr != nil {
		if replayErr != nil {
			writeManuscriptRequestError(w, http.StatusConflict, replayErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "dialogue": receipt.Dialogue, "replayed": true})
		return
	}
	dialogue := document.Dialogue
	if dialogue == nil || dialogue.ID != strings.TrimSpace(dialogueID) {
		writeManuscriptRequestError(w, http.StatusNotFound, "manuscript action dialogue not found")
		return
	}
	if dialogue.Status != "needs_input" || dialogue.Version != request.ExpectedVersion {
		writeManuscriptRequestError(w, http.StatusConflict, "dialogue state or version changed; refresh before replying")
		return
	}
	questionIndex := -1
	for index, question := range dialogue.Questions {
		if question.ID == request.QuestionID {
			questionIndex = index
			break
		}
	}
	if questionIndex < 0 {
		writeManuscriptRequestError(w, http.StatusBadRequest, "question is no longer pending")
		return
	}
	if manuscriptActionDialogueRunes(*dialogue)+utf8.RuneCountInString(request.Answer) > manuscriptActionMaxTotalRunes {
		writeManuscriptRequestError(w, http.StatusRequestEntityTooLarge, "dialogue exceeds 8000 characters; cancel and submit a consolidated instruction")
		return
	}
	dialogue.Messages = append(dialogue.Messages, host.ManuscriptActionMessage{Role: "user", QuestionID: request.QuestionID, Content: request.Answer})
	dialogue.Questions = append(dialogue.Questions[:questionIndex], dialogue.Questions[questionIndex+1:]...)
	dialogue.Version++
	dialogue.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if len(dialogue.Questions) == 0 {
		if dialogue.Round >= manuscriptActionMaxRounds {
			dialogue.Questions = []host.ManuscriptActionQuestion{{ID: fmt.Sprintf("limit-%d", dialogue.Round), Prompt: "已达到 6 轮澄清上限。请取消本次对话，整理为一条完整意见后重新提交。"}}
		} else {
			contextBundle, _, buildErr := buildManuscriptActionContext(st, service, dialogue.ChapterID)
			if buildErr != nil {
				writeManuscriptError(w, buildErr)
				return
			}
			if contextBundle.ContextSignature != dialogue.ContextSignature {
				writeManuscriptError(w, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("manuscript context changed during clarification")})
				return
			}
			clarification, clarifyErr := clarifier.ClarifyManuscriptAction(r.Context(), host.ManuscriptActionClarificationRequest{Action: dialogue.Type, InitialInput: dialogue.InitialInput, Context: contextBundle, Messages: dialogue.Messages})
			if clarifyErr != nil {
				writeManuscriptError(w, clarifyErr)
				return
			}
			dialogue.Round++
			applyManuscriptActionClarification(dialogue, clarification)
		}
	}
	recordManuscriptActionReceipt(document, request.IdempotencyKey, "reply", fingerprint, *dialogue)
	if err := saveManuscriptActionDialogueDocument(manifest.OutputDir, document); err != nil {
		writeManuscriptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "dialogue": dialogue})
}

func (s *Server) handleExecuteManuscriptActionDialogue(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService, session *ProjectSession, dialogueID string) {
	if r.Method != http.MethodPost {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request commandManuscriptActionDialogueRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, "invalid manuscript dialogue execute request")
		return
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.ExpectedVersion <= 0 || request.IdempotencyKey == "" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "expected_version and idempotency_key are required")
		return
	}
	document, err := loadManuscriptActionDialogueDocument(manifest.OutputDir)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	fingerprint := manuscriptActionFingerprint(request)
	if receipt, replayErr := replayManuscriptActionReceipt(document, request.IdempotencyKey, "execute", fingerprint); receipt != nil || replayErr != nil {
		if replayErr != nil {
			writeManuscriptRequestError(w, http.StatusConflict, replayErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "dialogue": receipt.Dialogue, "replayed": true})
		return
	}
	dialogue := document.Dialogue
	if dialogue == nil || dialogue.ID != strings.TrimSpace(dialogueID) {
		writeManuscriptRequestError(w, http.StatusNotFound, "manuscript action dialogue not found")
		return
	}
	executionRetry := dialogue.Status == "executing" && dialogue.ExecutionKey == request.IdempotencyKey && dialogue.ExecutionFingerprint == fingerprint && dialogue.ExecutionVersion == request.ExpectedVersion
	if !executionRetry && (dialogue.Status != "ready" || dialogue.Version != request.ExpectedVersion) {
		writeManuscriptRequestError(w, http.StatusConflict, "dialogue is not ready or its version changed")
		return
	}
	contextBundle, baseline, err := buildManuscriptActionContext(st, service, dialogue.ChapterID)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if baseline.CurrentProseSHA256 != dialogue.ContentSignature || (!executionRetry && contextBundle.ContextSignature != dialogue.ContextSignature) || contextBundleStructureRevision(st) != dialogue.StructureRevision || contextBundleStructureSignature(st) != dialogue.StructureSignature {
		writeManuscriptError(w, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("manuscript or structure changed; start a new action from the refreshed chapter")})
		return
	}
	if !executionRetry {
		dialogue.Status = "executing"
		dialogue.ExecutionKey = request.IdempotencyKey
		dialogue.ExecutionFingerprint = fingerprint
		dialogue.ExecutionVersion = request.ExpectedVersion
		dialogue.Version++
		dialogue.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := saveManuscriptActionDialogueDocument(manifest.OutputDir, document); err != nil {
			writeManuscriptError(w, err)
			return
		}
	}
	var result any
	if dialogue.Type == "expand" {
		if dialogue.Expansion == nil {
			markManuscriptActionFailed(manifest.OutputDir, document, dialogue, "expansion parameters are missing")
			writeManuscriptRequestError(w, http.StatusConflict, "expansion parameters are missing")
			return
		}
		planner := session.ExpansionPlanner()
		if planner == nil {
			markManuscriptActionFailed(manifest.OutputDir, document, dialogue, "expansion planner is unavailable")
			writeManuscriptRequestError(w, http.StatusServiceUnavailable, "expansion planner is unavailable")
			return
		}
		if auditorErr := session.ExpansionAuditorError(); auditorErr != nil {
			markManuscriptActionFailed(manifest.OutputDir, document, dialogue, auditorErr.Error())
			writeExpansionError(w, auditorErr)
			return
		}
		client, clientErr := expansionauditorclient.New()
		if clientErr != nil {
			markManuscriptActionFailed(manifest.OutputDir, document, dialogue, clientErr.Error())
			writeExpansionError(w, clientErr)
			return
		}
		expansionRequest := domain.ExpansionRequest{
			Location: dialogue.Expansion.Location, ReferenceIDs: append([]string(nil), dialogue.Expansion.ReferenceIDs...), Sentence: dialogue.ResolvedInstruction,
			Adjustment: dialogue.Expansion.Adjustment, ExpectedStructureRevision: dialogue.StructureRevision, ExpectedStructureSignature: dialogue.StructureSignature,
			IdempotencyKey: request.IdempotencyKey, ClientRequestID: dialogue.ID,
		}
		preview, planErr := planWithExpansionAuditorProcess(r.Context(), planner, client, manifest.OutputDir, expansionRequest)
		if planErr != nil {
			markManuscriptActionFailed(manifest.OutputDir, document, dialogue, planErr.Error())
			writeExpansionError(w, planErr)
			return
		}
		result = map[string]any{"kind": "expansion", "preview": publicExpansionPreview(preview), "awaiting_human_confirmation": true}
	} else {
		kind := domain.ManuscriptInstructionKind(dialogue.Type)
		preview, previewErr := service.PreviewContext(r.Context(), host.ManuscriptPreviewRequest{ChapterID: dialogue.ChapterID, Instruction: dialogue.ResolvedInstruction, Kind: kind}, request.IdempotencyKey)
		if previewErr != nil {
			markManuscriptActionFailed(manifest.OutputDir, document, dialogue, previewErr.Error())
			writeManuscriptError(w, previewErr)
			return
		}
		result = map[string]any{"kind": "revision", "preview": preview, "awaiting_confirmation": true}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		markManuscriptActionFailed(manifest.OutputDir, document, dialogue, err.Error())
		writeManuscriptError(w, err)
		return
	}
	dialogue.Result = encoded
	dialogue.Status = "completed"
	dialogue.Error = ""
	dialogue.Version++
	dialogue.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	recordManuscriptActionReceipt(document, request.IdempotencyKey, "execute", fingerprint, *dialogue)
	if err := saveManuscriptActionDialogueDocument(manifest.OutputDir, document); err != nil {
		writeManuscriptError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"project": manifest, "dialogue": dialogue})
}

func markManuscriptActionFailed(outputDir string, document *manuscriptActionDialogueDocument, dialogue *manuscriptActionDialogue, message string) {
	dialogue.Status = "failed"
	dialogue.Error = strings.TrimSpace(message)
	dialogue.Version++
	dialogue.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_ = saveManuscriptActionDialogueDocument(outputDir, document)
}

func (s *Server) handleCancelManuscriptActionDialogue(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, dialogueID string) {
	if r.Method != http.MethodPost {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request commandManuscriptActionDialogueRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, "invalid manuscript dialogue cancel request")
		return
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	document, err := loadManuscriptActionDialogueDocument(manifest.OutputDir)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	fingerprint := manuscriptActionFingerprint(request)
	if receipt, replayErr := replayManuscriptActionReceipt(document, request.IdempotencyKey, "cancel", fingerprint); receipt != nil || replayErr != nil {
		if replayErr != nil {
			writeManuscriptRequestError(w, http.StatusConflict, replayErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "dialogue": receipt.Dialogue, "replayed": true})
		return
	}
	dialogue := document.Dialogue
	if dialogue == nil || dialogue.ID != strings.TrimSpace(dialogueID) {
		writeManuscriptRequestError(w, http.StatusNotFound, "manuscript action dialogue not found")
		return
	}
	if dialogue.Version != request.ExpectedVersion || manuscriptActionDialogueTerminal(dialogue.Status) || dialogue.Status == "executing" {
		writeManuscriptRequestError(w, http.StatusConflict, "dialogue cannot be cancelled in its current state")
		return
	}
	dialogue.Status = "cancelled"
	dialogue.Version++
	dialogue.Questions = nil
	dialogue.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	recordManuscriptActionReceipt(document, request.IdempotencyKey, "cancel", fingerprint, *dialogue)
	if err := saveManuscriptActionDialogueDocument(manifest.OutputDir, document); err != nil {
		writeManuscriptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "dialogue": dialogue})
}

func applyManuscriptActionClarification(dialogue *manuscriptActionDialogue, clarification host.ManuscriptActionClarification) {
	dialogue.Status = clarification.Status
	dialogue.ResolvedInstruction = clarification.ResolvedInstruction
	dialogue.Questions = append([]host.ManuscriptActionQuestion(nil), clarification.Questions...)
	if clarification.AssistantMessage != "" {
		dialogue.Messages = append(dialogue.Messages, host.ManuscriptActionMessage{Role: "assistant", Content: clarification.AssistantMessage})
	}
	for index := range dialogue.Questions {
		dialogue.Questions[index].ID = fmt.Sprintf("r%d-%s", dialogue.Round, dialogue.Questions[index].ID)
		dialogue.Messages = append(dialogue.Messages, host.ManuscriptActionMessage{Role: "assistant", QuestionID: dialogue.Questions[index].ID, Content: dialogue.Questions[index].Prompt})
	}
	dialogue.Version++
	dialogue.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func buildManuscriptActionContext(st *storepkg.Store, service *host.ManuscriptRevisionService, chapterID string) (host.ManuscriptActionContext, domain.ManuscriptBaseline, error) {
	baseline, prose, err := service.CurrentChapter(chapterID)
	if err != nil {
		return host.ManuscriptActionContext{}, domain.ManuscriptBaseline{}, err
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return host.ManuscriptActionContext{}, domain.ManuscriptBaseline{}, err
	}
	var chapterOutline any
	var volumeTitle, arcTitle string
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			for _, chapter := range arc.Chapters {
				if chapter.ID == chapterID {
					chapterOutline, volumeTitle, arcTitle = chapter, volume.Title, arc.Title
				}
			}
		}
	}
	croppedProse, cropped := cropManuscriptActionProse(prose)
	active, err := st.ManuscriptRevisions.Active()
	if err != nil {
		return host.ManuscriptActionContext{}, domain.ManuscriptBaseline{}, err
	}
	var revisionSummary any
	if active != nil {
		revisionSummary = manuscriptRevisionMetadata{RevisionID: active.RevisionID, Revision: active.Revision, Stage: active.Stage, UpdatedAt: active.UpdatedAt}
	}
	recentSummaries, err := st.Summaries.LoadRecentSummaries(baseline.DisplayChapter, 4)
	if err != nil {
		return host.ManuscriptActionContext{}, domain.ManuscriptBaseline{}, err
	}
	contextBundle := host.ManuscriptActionContext{Mode: string(baseline.Mode), ChapterID: chapterID, DisplayChapter: baseline.DisplayChapter, Prose: croppedProse, ProseCropped: cropped, ChapterOutline: chapterOutline, VolumeTitle: volumeTitle, ArcTitle: arcTitle, RevisionSummary: revisionSummary, RecentSummaries: recentSummaries}
	if baseline.Mode == domain.RevisionModeAdaptation {
		evidence, _, evidenceErr := adaptationDiscussionEvidence(st, chapterID, manuscriptDiscussionBudgetUnits-len([]rune(croppedProse)), baseline.AdaptationPlanSHA256, baseline.SourceManifestSHA256)
		if evidenceErr != nil {
			return host.ManuscriptActionContext{}, domain.ManuscriptBaseline{}, evidenceErr
		}
		contextBundle.AdaptationEvidence = evidence
	}
	contextBundle.ContextSignature = manuscriptActionFingerprint(struct {
		Context            host.ManuscriptActionContext `json:"context"`
		ContentSignature   string                       `json:"content_signature"`
		StructureSignature string                       `json:"structure_signature"`
	}{contextBundle, baseline.CurrentProseSHA256, domain.StructureSignature(volumes)})
	return contextBundle, baseline, nil
}

func cropManuscriptActionProse(prose string) (string, bool) {
	runes := []rune(prose)
	if len(runes) <= manuscriptActionProseRunes {
		return prose, false
	}
	const tail = 3000
	head := manuscriptActionProseRunes - tail
	return string(runes[:head]) + "\n\n……（正文中段按确定性预算裁剪）……\n\n" + string(runes[len(runes)-tail:]), true
}

func contextBundleStructureRevision(st *storepkg.Store) int {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return 0
	}
	return domain.StructureRevision(volumes)
}

func contextBundleStructureSignature(st *storepkg.Store) string {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return ""
	}
	return domain.StructureSignature(volumes)
}

func manuscriptActionDialogueTerminal(status string) bool {
	return status == "completed" || status == "cancelled" || status == "failed"
}

func manuscriptActionDialogueRunes(dialogue manuscriptActionDialogue) int {
	total := 0
	for _, message := range dialogue.Messages {
		total += utf8.RuneCountInString(message.Content)
	}
	return total
}

func manuscriptActionDialogueID() string {
	payload := make([]byte, 16)
	if _, err := rand.Read(payload); err != nil {
		return fmt.Sprintf("mad_%d", time.Now().UnixNano())
	}
	return "mad_" + hex.EncodeToString(payload)
}

func manuscriptActionFingerprint(value any) string {
	payload, _ := json.Marshal(value)
	return domain.ContentSignature(payload)
}

func manuscriptActionDialoguePath(outputDir string) string {
	return filepath.Join(outputDir, filepath.FromSlash(manuscriptActionDialogueRelPath))
}

func loadManuscriptActionDialogueDocument(outputDir string) (*manuscriptActionDialogueDocument, error) {
	payload, err := os.ReadFile(manuscriptActionDialoguePath(outputDir))
	if errors.Is(err, os.ErrNotExist) {
		return &manuscriptActionDialogueDocument{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manuscript action dialogue: %w", err)
	}
	var document manuscriptActionDialogueDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, fmt.Errorf("decode manuscript action dialogue: %w", err)
	}
	return &document, nil
}

func saveManuscriptActionDialogueDocument(outputDir string, document *manuscriptActionDialogueDocument) error {
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	path := manuscriptActionDialoguePath(outputDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomically(path, payload, 0o600)
}

func replayManuscriptActionReceipt(document *manuscriptActionDialogueDocument, key, operation, fingerprint string) (*manuscriptActionDialogueReceipt, error) {
	for index := range document.Receipts {
		receipt := &document.Receipts[index]
		if receipt.Key != key {
			continue
		}
		if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
			return nil, fmt.Errorf("idempotency key was already used with different input")
		}
		return receipt, nil
	}
	return nil, nil
}

func recordManuscriptActionReceipt(document *manuscriptActionDialogueDocument, key, operation, fingerprint string, dialogue manuscriptActionDialogue) {
	document.Receipts = append(document.Receipts, manuscriptActionDialogueReceipt{Key: key, Operation: operation, Fingerprint: fingerprint, Dialogue: dialogue})
	if len(document.Receipts) > 80 {
		document.Receipts = append([]manuscriptActionDialogueReceipt(nil), document.Receipts[len(document.Receipts)-80:]...)
	}
}
