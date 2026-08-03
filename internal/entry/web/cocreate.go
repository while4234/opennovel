package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const (
	webCoCreateKindNormal       = "normal"
	webCoCreateKindStage        = "stage"
	webCoCreateKindAdapt        = "adapt"
	webCoCreateKindContinuation = "continuation"

	webCoCreateCheckpointVersion = 1
	adaptDecisionDraftBatchSize  = 4
	maxCoCreateJSONBodyBytes     = 1 << 20
	adaptDecisionDraftMarker     = "Internal adaptation decision draft batch"

	stageCoCreateOpener            = "我先暂停一下，想和你一起规划接下来的走向。"
	stageCoCreateSystemLine        = "已暂停创作，进入阶段共创。AI 会结合当前故事进度，和你一起规划接下来的走向。"
	adaptCoCreateSystemLine        = "原书分析和模式选择完成，进入改编共创。AI 会锁定已选模式，帮你确认具体改编目标。"
	continuationCoCreateOpener     = "我想和你一起确定这本小说接下来续写的 Draft。"
	continuationCoCreateSystemLine = "原作已经导入，进入续写 Draft 共创。AI 会基于已写内容协助确定后续方向；确认 Draft 后只进入续写提案，不会直接开始写正文。"
)

type webCoCreateBeginRequest struct {
	Kind             string  `json:"kind"`
	Initial          string  `json:"initial"`
	SourceFile       string  `json:"source_file"`
	Mode             string  `json:"mode"`
	Tolerance        float64 `json:"word_tolerance"`
	TargetTotalWords int     `json:"target_total_words"`
	ExpectedRevision int     `json:"expected_revision,omitempty"`
	IdempotencyKey   string  `json:"idempotency_key,omitempty"`

	sourcePath string
	briefing   *domain.AdaptationCoCreateBriefing
}

type webCoCreateSendRequest struct {
	Text           string `json:"text"`
	Source         string `json:"source"`
	ForceRebrief   bool   `json:"force_rebrief,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type webCoCreateReviseRequest struct {
	MessageID      string `json:"message_id"`
	Text           string `json:"text"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type webCoCreatePlanningRevisionRequest struct {
	Feedback       string `json:"feedback"`
	Instruction    string `json:"instruction"`
	Target         string `json:"target,omitempty"`
	Scope          string `json:"scope,omitempty"`
	VolumeIndex    int    `json:"volume_index,omitempty"`
	Chapter        int    `json:"chapter,omitempty"`
	FromChapter    int    `json:"from_chapter,omitempty"`
	ToChapter      int    `json:"to_chapter,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Async          bool   `json:"async,omitempty"`
}

type webNormalRevisionPreviewRequest struct {
	Operation         domain.StructureRevisionOperation `json:"operation"`
	Intent            string                            `json:"intent"`
	TargetID          string                            `json:"target_id,omitempty"`
	DestinationID     string                            `json:"destination_id,omitempty"`
	BaseRevision      int                               `json:"base_revision,omitempty"`
	CurrentSoftBudget *domain.DynamicSoftBudget         `json:"current_soft_budget,omitempty"`
	Proposal          domain.StructureRevisionProposal  `json:"proposal"`
	IdempotencyKey    string                            `json:"idempotency_key"`
}

type webNormalRevisionCommandRequest struct {
	Action           string                           `json:"action"`
	ExpectedRevision int                              `json:"expected_revision,omitempty"`
	IdempotencyKey   string                           `json:"idempotency_key"`
	Preview          *domain.StructureRevisionPreview `json:"preview,omitempty"`
	Candidate        []domain.VolumeOutline           `json:"candidate,omitempty"`
	Evidence         []domain.RevisionAuditEvidence   `json:"evidence,omitempty"`
	ImpactSignature  string                           `json:"impact_signature,omitempty"`
	Feedback         string                           `json:"feedback,omitempty"`
}

type webNormalRevisionPlanner struct {
	proposal domain.StructureRevisionProposal
}

func (p webNormalRevisionPlanner) PlanStructure(context.Context, domain.StructureRevisionRequest) (domain.StructureRevisionProposal, error) {
	return p.proposal, nil
}

type webCoCreateDecisionItem struct {
	DecisionID   string `json:"decision_id"`
	OptionID     string `json:"option_id"`
	CustomAnswer string `json:"custom_answer"`
}

type webCoCreateDecisionRequest struct {
	webCoCreateDecisionItem
	Decisions      []webCoCreateDecisionItem `json:"decisions,omitempty"`
	IdempotencyKey string                    `json:"idempotency_key,omitempty"`
}

type webCoCreateMessage struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	Editable     bool   `json:"editable,omitempty"`
	Source       string `json:"source,omitempty"`
	historyIndex int
}

type webCoCreateMessageCheckpoint struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	Editable     bool   `json:"editable,omitempty"`
	Source       string `json:"source,omitempty"`
	HistoryIndex int    `json:"history_index"`
}

type webCoCreateCheckpoint struct {
	Version                int                                `json:"version"`
	UpdatedAt              time.Time                          `json:"updated_at"`
	Kind                   string                             `json:"kind"`
	Session                startup.CoCreateSnapshot           `json:"session"`
	Messages               []webCoCreateMessageCheckpoint     `json:"messages"`
	NextMessageSeq         int                                `json:"next_message_seq"`
	Failed                 bool                               `json:"failed,omitempty"`
	SourceFile             string                             `json:"source_file,omitempty"`
	SourcePath             string                             `json:"source_path,omitempty"`
	AdaptGranularity       string                             `json:"adapt_granularity,omitempty"`
	AdaptRewritePolicy     string                             `json:"adapt_rewrite_policy,omitempty"`
	AdaptWordTolerance     float64                            `json:"adapt_word_tolerance,omitempty"`
	TargetTotalWords       int                                `json:"target_total_words,omitempty"`
	AdaptationProposal     *domain.AdaptationPlan             `json:"adaptation_proposal,omitempty"`
	AdaptationVolumeReview *domain.AdaptationVolumeReview     `json:"adaptation_volume_review,omitempty"`
	DraftConsolidated      bool                               `json:"draft_consolidated_for_commit,omitempty"`
	AdaptationBriefing     *domain.AdaptationCoCreateBriefing `json:"adaptation_briefing,omitempty"`
	ExpectedRevision       int                                `json:"expected_revision,omitempty"`
}

type webCoCreateLogEntry struct {
	InputHistory      []host.CoCreateMessage `json:"input_history"`
	RawResponse       string                 `json:"raw_response"`
	Thinking          string                 `json:"thinking"`
	ParsedReply       string                 `json:"parsed_reply"`
	ParsedDraft       string                 `json:"parsed_draft"`
	ParsedReady       bool                   `json:"parsed_ready"`
	ParsedSuggestions []string               `json:"parsed_sugs"`
	Error             string                 `json:"error"`
}

type webCoCreateState struct {
	Kind                  string                              `json:"kind"`
	Active                bool                                `json:"active"`
	Messages              []webCoCreateMessage                `json:"messages"`
	DraftPrompt           string                              `json:"draft_prompt"`
	Ready                 bool                                `json:"ready"`
	Suggestions           []string                            `json:"suggestions"`
	StreamThinking        string                              `json:"stream_thinking,omitempty"`
	StreamReply           string                              `json:"stream_reply,omitempty"`
	AdaptMode             string                              `json:"adapt_mode,omitempty"`
	RewritePolicy         string                              `json:"rewrite_policy,omitempty"`
	WordTolerance         float64                             `json:"word_tolerance,omitempty"`
	TargetTotalWords      int                                 `json:"target_total_words,omitempty"`
	SourceFile            string                              `json:"source_file,omitempty"`
	Proposal              *domain.AdaptationPlan              `json:"proposal,omitempty"`
	VolumeReview          *domain.AdaptationVolumeReview      `json:"volume_review,omitempty"`
	CanStart              bool                                `json:"can_start"`
	ModeLocked            bool                                `json:"mode_locked,omitempty"`
	Failed                bool                                `json:"failed,omitempty"`
	CommittedLabel        string                              `json:"committed_label,omitempty"`
	Briefing              *webCoCreateBriefingState           `json:"briefing,omitempty"`
	PendingDecisions      []domain.AdaptationBriefingDecision `json:"pending_decisions,omitempty"`
	BlockedReason         string                              `json:"blocked_reason,omitempty"`
	CoreCast              *domain.CoreCastContract            `json:"core_cast,omitempty"`
	SourceMajorCharacters []domain.SourceMajorCharacter       `json:"source_major_characters"`
	CastCompletion        domain.CoreCastCompletionResult     `json:"cast_completion"`
	CastConfirmed         bool                                `json:"cast_confirmed"`
	CastSignature         string                              `json:"cast_signature,omitempty"`
	BlockingReasons       []string                            `json:"blocking_reasons"`
}

type webCoreCastUpdateRequest struct {
	ExpectedRevision int64                   `json:"expected_revision"`
	CoreCast         domain.CoreCastContract `json:"core_cast"`
}

type webCoreCastConfirmRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	ContentSignature string `json:"content_signature"`
}

type webCharacterCandidateConfirmRequest struct {
	ExpectedCandidateRevision int64  `json:"expected_candidate_revision"`
	CandidateDigest           string `json:"candidate_digest"`
	IdempotencyKey            string `json:"idempotency_key"`
}

type webCharacterCandidateEditRequest struct {
	ExpectedCandidateRevision int64                          `json:"expected_candidate_revision"`
	Characters                []domain.Character             `json:"characters"`
	PlannedRelationships      []domain.CharacterRelationship `json:"planned_relationships"`
	RelationshipsReviewed     bool                           `json:"relationships_reviewed"`
}

type webFoundationConfirmRequest struct {
	ExpectedRevision int64           `json:"expected_revision"`
	AuditSignature   string          `json:"audit_signature"`
	SourceFoundation json.RawMessage `json:"source_foundation,omitempty"`
}

type webFoundationReviseRequest struct {
	Feedback         string          `json:"feedback"`
	SourceFoundation json.RawMessage `json:"source_foundation,omitempty"`
}

type webCoCreateSession struct {
	kind                    string
	session                 *startup.CoCreateSession
	messages                []webCoCreateMessage
	nextMessageSeq          int
	failed                  bool
	sourceFile              string
	sourcePath              string
	adaptGranularity        string
	adaptRewritePolicy      string
	adaptWordTolerance      float64
	targetTotalWords        int
	adaptationProposal      *domain.AdaptationPlan
	adaptationVolumeReview  *domain.AdaptationVolumeReview
	draftConsolidated       bool
	adaptationBriefing      *domain.AdaptationCoCreateBriefing
	expectedRevision        int
	coreCast                *domain.CoreCastContract
	sourceCharacters        []domain.SourceMajorCharacter
	sourceMajorCharacters   []domain.SourceMajorCharacter
	sourceResolutionMissing []domain.CoreCastMissingItem
	castError               string
}

type webCoCreateBriefingState struct {
	Active                bool   `json:"active"`
	TriggerReason         string `json:"trigger_reason,omitempty"`
	PendingDecisionCount  int    `json:"pending_decision_count"`
	ResolvedDecisionCount int    `json:"resolved_decision_count"`
	TotalDecisionCount    int    `json:"total_decision_count"`
}

func (s *Server) handleProjectCoCreateBegin(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webCoCreateBeginRequest
	raw, err := decodeJSONBodyRaw(r, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid co-create request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Kind) != webCoCreateKindAdapt {
		if err := domain.ValidateNormalRevisionPayload(raw); err != nil {
			writeError(w, http.StatusBadRequest, "invalid normal co-create request: "+err.Error())
			return
		}
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	if strings.TrimSpace(req.Kind) == webCoCreateKindAdapt {
		if req.TargetTotalWords != 0 {
			writeError(w, http.StatusBadRequest, "target_total_words is only supported for normal co-create")
			return
		}
		mode := strings.TrimSpace(req.Mode)
		rewritePolicy, err := adaptationRewritePolicyForMode(mode)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sourcePath, err := adaptationSourcePathFromName(req.SourceFile, manifest, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Mode = mode
		req.sourcePath = sourcePath
		req.SourceFile = strings.TrimSpace(req.SourceFile)
		req.Tolerance = startup.AdaptationWordToleranceForGranularity(mode, req.Tolerance)
		if rewritePolicy == "" {
			writeError(w, http.StatusBadRequest, "adaptation rewrite policy is required")
			return
		}
		st := storepkg.NewStore(manifest.OutputDir)
		if _, _, err := adapt.ValidatePreparedSource(st, sourcePath); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		current, err := st.Adaptation.CoCreateDossierCurrent(adapt.CoCreateDossierPromptVersion, adapt.CoCreateDossierBatchSize, adapt.CoCreateDossierBatchRuneLimit)
		if err != nil {
			writeError(w, http.StatusConflict, "read adaptation co-create dossier: "+err.Error())
			return
		}
		if !current {
			writeError(w, http.StatusConflict, "adaptation co-create dossier missing or stale; run source analysis first")
			return
		}
		state, err := session.BeginAdaptCoCreate(r.Context(), req)
		if err != nil {
			writeCoCreateActionError(w, err, state)
			return
		}
		writeCoCreateResponse(w, manifest, session, state)
		return
	}
	state, err := session.BeginCoCreate(r.Context(), req)
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateSend(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeCoCreateSendRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.SendCoCreate(r.Context(), req)
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateRevise(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webCoCreateReviseRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid co-create revise request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.ReviseCoCreate(r.Context(), req)
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateDecision(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webCoCreateDecisionRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid co-create decision request: "+err.Error())
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.ResolveCoCreateDecision(r.Context(), req)
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateResume(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.ResumeCoCreate(r.Context())
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateCommit(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.CommitCoCreate(r.Context())
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	state.CommittedLabel = coCreateCommitLabel(state.Kind)
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCoCreateConfirm(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	label, err := session.ConfirmCoCreatePlanning()
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": session.WebSnapshot(),
		"running":  session.Snapshot().IsRunning,
		"label":    label,
	})
}

func (s *Server) handleProjectFoundationReview(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	var label string
	switch action {
	case "confirm":
		var req webFoundationConfirmRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid foundation confirmation: "+err.Error())
			return
		}
		if len(req.SourceFoundation) > 0 && string(req.SourceFoundation) != "null" {
			writeError(w, http.StatusUnprocessableEntity, "source_foundation is read-only and cannot be submitted")
			return
		}
		label, err = session.ConfirmCoCreateFoundation(req.ExpectedRevision, req.AuditSignature)
	case "revise":
		var req webFoundationReviseRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid foundation revision: "+err.Error())
			return
		}
		if len(req.SourceFoundation) > 0 && string(req.SourceFoundation) != "null" {
			writeError(w, http.StatusUnprocessableEntity, "source_foundation is read-only and cannot be submitted")
			return
		}
		label, err = session.ReviseCoCreateFoundation(req.Feedback)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeFoundationReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest, "snapshot": session.WebSnapshot(), "running": session.Snapshot().IsRunning, "label": label,
	})
}

func writeFoundationReviewError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	code := storepkg.FoundationReviewErrorStage
	var adaptationErr *storepkg.AdaptationFoundationReviewError
	if errors.As(err, &adaptationErr) {
		if adaptationErr.Code == storepkg.AdaptationFoundationErrorValidation || adaptationErr.Code == storepkg.AdaptationFoundationErrorReadonly {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"error": map[string]any{
			"code": adaptationErr.Code, "message": err.Error(), "latest": adaptationErr.Review,
		}})
		return
	}
	var reviewErr *storepkg.FoundationReviewError
	if errors.As(err, &reviewErr) {
		code = reviewErr.Code
		if code == storepkg.FoundationReviewErrorValidation {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"error": map[string]any{
			"code": code, "message": err.Error(), "latest": reviewErr.Review,
		}})
		return
	}
	if errors.Is(err, ErrSessionActionInProgress) || strings.Contains(strings.ToLower(err.Error()), "already running") || strings.Contains(strings.ToLower(err.Error()), "busy") {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
			"code": storepkg.FoundationReviewErrorBusy, "message": err.Error(),
		}})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{
		"code": "foundation_storage_error", "message": err.Error(),
	}})
}

func (s *Server) handleProjectCoCreatePlanningRevise(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		s.handleProjectBackgroundAction(w, r, id)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webCoCreatePlanningRevisionRequest
	raw, err := decodeJSONBodyRaw(r, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid co-create planning revision request: "+err.Error())
		return
	}
	if err := domain.ValidateNormalRevisionPayload(raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid normal planning revision request: "+err.Error())
		return
	}
	req.Feedback = strings.TrimSpace(req.Feedback)
	if req.Feedback == "" {
		req.Feedback = strings.TrimSpace(req.Instruction)
	}
	if req.Feedback == "" {
		writeError(w, http.StatusBadRequest, "feedback is required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	req.Instruction = strings.TrimSpace(req.Instruction)
	if req.Instruction == "" {
		req.Instruction = req.Feedback
	}
	if req.Async {
		instruction := req.Feedback
		action, created, startErr := session.StartCancellableBackgroundAction(
			projectActionKindPlanningRevision,
			req.IdempotencyKey,
			func(ctx context.Context) error {
				return session.reviseCoCreatePlanningWithinAction(ctx, req, instruction)
			},
		)
		if startErr != nil {
			writeBackgroundActionStartError(w, startErr)
			return
		}
		writeBackgroundActionAccepted(w, r, manifest, session, action, created)
		return
	}
	if err := session.ReviseCoCreatePlanning(r.Context(), req); err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	snapshot := session.WebSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": snapshot,
		"running":  snapshot.IsRunning,
		"label":    "planning revision started",
	})
}

func (s *Server) handleProjectNormalRevisionPreview(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webNormalRevisionPreviewRequest
	raw, err := decodeJSONBodyRaw(r, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid normal revision preview: "+err.Error())
		return
	}
	if err := domain.ValidateNormalRevisionPayload(raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid normal revision preview: "+err.Error())
		return
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	st := storepkg.NewStore(manifest.OutputDir)
	current, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	stage, err := session.NormalRevisionService().CurrentManuscriptStage()
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	completed := normalCompletedChapterIDs(st, current)
	if req.BaseRevision <= 0 {
		req.BaseRevision = 1
	}
	if req.CurrentSoftBudget == nil {
		budget, budgetErr := domain.NewDynamicSoftBudget(domain.TotalChapters(current), 3000, 5000)
		if budgetErr != nil {
			writeProjectLifecycleError(w, budgetErr)
			return
		}
		req.CurrentSoftBudget = &budget
	}
	previewed, err := session.PreviewNormalStructureRevision(r.Context(), webNormalRevisionPlanner{proposal: req.Proposal}, domain.StructureRevisionRequest{
		Operation: req.Operation, Intent: req.Intent, Stage: stage, TargetID: req.TargetID,
		DestinationID: req.DestinationID, BaseRevision: req.BaseRevision, Current: current,
		CompletedChapterIDs: completed, CurrentSoftBudget: req.CurrentSoftBudget,
	}, req.IdempotencyKey)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": previewed})
}

func normalCompletedChapterIDs(st *storepkg.Store, current []domain.VolumeOutline) []string {
	progress, _ := st.Progress.Load()
	if progress == nil {
		return nil
	}
	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		completed[chapter] = struct{}{}
	}
	ids := make([]string, 0, len(completed))
	for _, chapter := range domain.FlattenOutline(current) {
		if _, ok := completed[chapter.Chapter]; ok {
			ids = append(ids, chapter.ID)
		}
	}
	return ids
}

func (s *Server) handleProjectNormalRevisionCommand(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req webNormalRevisionCommandRequest
	raw, err := decodeJSONBodyRaw(r, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid normal revision command: "+err.Error())
		return
	}
	if err := domain.ValidateNormalRevisionPayload(raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid normal revision command: "+err.Error())
		return
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	projectSession, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	service := projectSession.NormalRevisionService()
	active, err := storepkg.NewStore(manifest.OutputDir).Revisions.Active()
	if err != nil {
		writeProjectLifecycleError(w, fmt.Errorf("load active normal revision: %w", err))
		return
	}
	if active == nil {
		writeProjectLifecycleError(w, fmt.Errorf("no active normal revision"))
		return
	}
	if req.ExpectedRevision <= 0 {
		writeError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}
	if req.ExpectedRevision != active.Revision {
		writeProjectLifecycleError(w, fmt.Errorf("revision conflict: expected %d, actual %d", req.ExpectedRevision, active.Revision))
		return
	}
	var result *domain.RevisionSession
	switch strings.TrimSpace(req.Action) {
	case "approve_impact":
		result, err = service.ApproveImpact(active.ID, active.Revision, req.IdempotencyKey)
	case "submit_structure":
		if req.Preview == nil {
			err = fmt.Errorf("sealed preview is required")
		} else {
			result, err = service.SubmitStructureCandidate(*req.Preview, active.ID, active.Revision, req.IdempotencyKey)
		}
	case "submit_details":
		result, err = service.SubmitDetailedOutlineCandidate(req.Candidate, active, req.IdempotencyKey)
	case "record_audit":
		result, err = service.RecordAuditSet(active, req.Evidence, req.IdempotencyKey)
	case "approve_stage":
		result, err = service.ApproveStage(active, req.IdempotencyKey)
	case "submit_prose_intents":
		result, err = service.SubmitProseReworkCandidate(active, req.IdempotencyKey)
	case "feedback":
		result, err = service.SubmitFeedback(active, req.ImpactSignature, req.Feedback, req.IdempotencyKey)
	case "publish":
		if req.Preview == nil {
			err = fmt.Errorf("sealed preview is required")
		} else {
			result, err = service.PublishStructure(*req.Preview, active, req.IdempotencyKey)
		}
	default:
		err = fmt.Errorf("unsupported normal revision command %q", req.Action)
	}
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": result})
}

func (s *Server) handleProjectCoCreateCancel(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	state, err := session.CancelCoCreate()
	if err != nil {
		writeCoCreateActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func newWebCoCreateSession(req webCoCreateBeginRequest) (*webCoCreateSession, error) {
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = webCoCreateKindNormal
	}
	switch kind {
	case webCoCreateKindNormal:
		initial := strings.TrimSpace(req.Initial)
		if initial == "" {
			return nil, fmt.Errorf("initial idea is required")
		}
		if req.TargetTotalWords < 0 {
			return nil, fmt.Errorf("target_total_words must be a non-negative integer")
		}
		targetTotalWords := req.TargetTotalWords
		state := &webCoCreateSession{
			kind:             kind,
			session:          startup.NewCoCreateSession(initial),
			targetTotalWords: targetTotalWords,
		}
		state.messages = append(state.messages, state.newMessage("user", initial, "custom", 0))
		return state, nil
	case webCoCreateKindStage:
		initial := strings.TrimSpace(req.Initial)
		if initial == "" {
			initial = stageCoCreateOpener
		}
		state := &webCoCreateSession{
			kind:    kind,
			session: startup.NewCoCreateSession(initial),
		}
		messages := []webCoCreateMessage{state.newMessage("system", stageCoCreateSystemLine, "", -1)}
		if initial != stageCoCreateOpener {
			messages = append(messages, state.newMessage("user", initial, "custom", 0))
		}
		state.messages = messages
		return state, nil
	case webCoCreateKindContinuation:
		if req.ExpectedRevision < 0 {
			return nil, fmt.Errorf("expected_revision must be a non-negative integer")
		}
		initial := strings.TrimSpace(req.Initial)
		if initial == "" {
			initial = continuationCoCreateOpener
		}
		state := &webCoCreateSession{
			kind:             kind,
			session:          startup.NewCoCreateSession(initial),
			expectedRevision: req.ExpectedRevision,
		}
		state.messages = []webCoCreateMessage{state.newMessage("system", continuationCoCreateSystemLine, "", -1)}
		if initial != continuationCoCreateOpener {
			state.messages = append(state.messages, state.newMessage("user", initial, "custom", 0))
		}
		return state, nil
	case webCoCreateKindAdapt:
		granularity, ok := domain.StrictAdaptationGranularity(req.Mode)
		if !ok {
			return nil, fmt.Errorf("adaptation mode must be one of chapter, arc, free")
		}
		rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
		tolerance := startup.AdaptationWordToleranceForGranularity(granularity, req.Tolerance)
		opener := adaptCoCreateOpener(granularity, rewritePolicy, tolerance)
		state := &webCoCreateSession{
			kind:               kind,
			session:            startup.NewCoCreateSession(opener),
			sourceFile:         strings.TrimSpace(req.SourceFile),
			sourcePath:         strings.TrimSpace(req.sourcePath),
			adaptGranularity:   granularity,
			adaptRewritePolicy: rewritePolicy,
			adaptWordTolerance: tolerance,
			adaptationBriefing: req.briefing,
		}
		state.messages = []webCoCreateMessage{state.newMessage("system", adaptCoCreateSystemLine, "", -1)}
		if initial := strings.TrimSpace(req.Initial); initial != "" {
			historyIndex := len(state.session.History())
			state.session.AppendUser(initial)
			state.messages = append(state.messages, state.newMessage("user", initial, "custom", historyIndex))
		}
		return state, nil
	default:
		return nil, fmt.Errorf("co-create kind must be one of normal, stage, adapt, continuation")
	}
}

func (s *webCoCreateSession) newMessage(role, content, source string, historyIndex int) webCoCreateMessage {
	s.nextMessageSeq++
	message := webCoCreateMessage{
		ID:           fmt.Sprintf("m%d", s.nextMessageSeq),
		Role:         role,
		Content:      content,
		Source:       coCreateMessageSource(source),
		historyIndex: historyIndex,
	}
	message.Editable = role == "user" && historyIndex >= 0
	return message
}

func (s *webCoCreateSession) checkpoint(now time.Time) webCoCreateCheckpoint {
	if s == nil {
		return webCoCreateCheckpoint{}
	}
	return s.checkpointWithFailed(now, s.failed)
}

func (s *webCoCreateSession) checkpointWithFailed(now time.Time, failed bool) webCoCreateCheckpoint {
	if s == nil {
		return webCoCreateCheckpoint{}
	}
	messages := make([]webCoCreateMessageCheckpoint, 0, len(s.messages))
	for _, message := range s.messages {
		messages = append(messages, webCoCreateMessageCheckpoint{
			ID:           message.ID,
			Role:         message.Role,
			Content:      message.Content,
			Editable:     message.Editable,
			Source:       message.Source,
			HistoryIndex: message.historyIndex,
		})
	}
	return webCoCreateCheckpoint{
		Version:                webCoCreateCheckpointVersion,
		UpdatedAt:              now.UTC(),
		Kind:                   s.kind,
		Session:                s.session.Snapshot(),
		Messages:               messages,
		NextMessageSeq:         s.nextMessageSeq,
		Failed:                 failed,
		SourceFile:             s.sourceFile,
		SourcePath:             s.sourcePath,
		AdaptGranularity:       s.adaptGranularity,
		AdaptRewritePolicy:     s.adaptRewritePolicy,
		AdaptWordTolerance:     s.adaptWordTolerance,
		TargetTotalWords:       s.targetTotalWords,
		AdaptationProposal:     s.adaptationProposal,
		AdaptationVolumeReview: s.adaptationVolumeReview,
		DraftConsolidated:      s.draftConsolidated,
		AdaptationBriefing:     s.adaptationBriefing,
		ExpectedRevision:       s.expectedRevision,
	}
}

func webCoCreateSessionFromCheckpoint(checkpoint webCoCreateCheckpoint) (*webCoCreateSession, error) {
	if checkpoint.Version != webCoCreateCheckpointVersion {
		return nil, fmt.Errorf("unsupported co-create checkpoint version %d", checkpoint.Version)
	}
	kind := strings.TrimSpace(checkpoint.Kind)
	if kind == "" {
		kind = webCoCreateKindNormal
	}
	switch kind {
	case webCoCreateKindNormal, webCoCreateKindStage, webCoCreateKindAdapt, webCoCreateKindContinuation:
	default:
		return nil, fmt.Errorf("unsupported co-create kind %q", checkpoint.Kind)
	}
	if len(checkpoint.Session.History) == 0 {
		return nil, fmt.Errorf("co-create checkpoint history is empty")
	}
	messages := make([]webCoCreateMessage, 0, len(checkpoint.Messages))
	for _, message := range checkpoint.Messages {
		messages = append(messages, webCoCreateMessage{
			ID:           message.ID,
			Role:         message.Role,
			Content:      message.Content,
			Editable:     message.Editable,
			Source:       message.Source,
			historyIndex: message.HistoryIndex,
		})
	}
	nextMessageSeq := checkpoint.NextMessageSeq
	if nextMessageSeq < len(messages) {
		nextMessageSeq = len(messages)
	}
	adaptGranularity, adaptRewritePolicy, adaptWordTolerance := checkpoint.AdaptGranularity, checkpoint.AdaptRewritePolicy, checkpoint.AdaptWordTolerance
	if kind == webCoCreateKindAdapt {
		adaptGranularity, adaptRewritePolicy, adaptWordTolerance = coCreateCheckpointAdaptOptions(checkpoint)
	}
	session := startup.NewCoCreateSessionFromSnapshot(checkpoint.Session)
	return &webCoCreateSession{
		kind:                   kind,
		session:                session,
		messages:               messages,
		nextMessageSeq:         nextMessageSeq,
		failed:                 checkpoint.Failed,
		sourceFile:             strings.TrimSpace(checkpoint.SourceFile),
		sourcePath:             strings.TrimSpace(checkpoint.SourcePath),
		adaptGranularity:       adaptGranularity,
		adaptRewritePolicy:     adaptRewritePolicy,
		adaptWordTolerance:     adaptWordTolerance,
		targetTotalWords:       checkpoint.TargetTotalWords,
		adaptationProposal:     checkpoint.AdaptationProposal,
		adaptationVolumeReview: checkpoint.AdaptationVolumeReview,
		draftConsolidated:      checkpoint.DraftConsolidated,
		adaptationBriefing:     checkpoint.AdaptationBriefing,
		expectedRevision:       checkpoint.ExpectedRevision,
		coreCast:               session.LegacyCoreCast(),
	}, nil
}

func coCreateCheckpointAdaptOptions(checkpoint webCoCreateCheckpoint) (string, string, float64) {
	granularity := strings.TrimSpace(checkpoint.AdaptGranularity)
	rewritePolicy := strings.TrimSpace(checkpoint.AdaptRewritePolicy)
	wordTolerance := checkpoint.AdaptWordTolerance
	if (granularity == "" || rewritePolicy == "") && len(checkpoint.Session.History) > 0 {
		logGranularity, logRewritePolicy, logWordTolerance := coCreateLogAdaptOptions(checkpoint.Session.History[0].Content)
		if granularity == "" {
			granularity = logGranularity
		}
		if rewritePolicy == "" {
			rewritePolicy = logRewritePolicy
		}
		if wordTolerance <= 0 {
			wordTolerance = logWordTolerance
		}
	}
	return normalizeWebAdaptCoCreateOptions(granularity, rewritePolicy, wordTolerance)
}

func webCoCreateSessionFromLogEntry(entry webCoCreateLogEntry) (*webCoCreateSession, error) {
	history := cleanCoCreateLogHistory(entry.InputHistory)
	if len(history) == 0 {
		return nil, fmt.Errorf("co-create log history is empty")
	}
	kind := inferWebCoCreateKindFromLog(history)
	draftPrompt, ready, suggestions := coCreateLogDraftState(entry, history)
	sessionHistory := append([]host.CoCreateMessage(nil), history...)
	assistantMessage := strings.TrimSpace(entry.ParsedReply)
	if assistantMessage == "" {
		assistantMessage = extractCoCreateLogTag(entry.RawResponse, "reply")
	}
	if strings.TrimSpace(entry.Error) == "" {
		raw := strings.TrimSpace(entry.RawResponse)
		if raw == "" {
			raw = assistantMessage
		}
		if raw != "" {
			sessionHistory = append(sessionHistory, host.CoCreateMessage{Role: "assistant", Content: raw})
		}
	}
	state := &webCoCreateSession{
		kind:    kind,
		session: startup.NewCoCreateSessionFromSnapshot(startup.CoCreateSnapshot{History: sessionHistory, DraftPrompt: draftPrompt, Ready: ready, Suggestions: suggestions}),
		failed:  strings.TrimSpace(entry.Error) != "",
	}
	if kind == webCoCreateKindAdapt {
		state.adaptGranularity, state.adaptRewritePolicy, state.adaptWordTolerance = coCreateLogAdaptOptions(history[0].Content)
	}
	state.messages = webCoCreateMessagesFromLog(kind, history, assistantMessage, strings.TrimSpace(entry.Error) == "")
	return state, nil
}

func cleanCoCreateLogHistory(history []host.CoCreateMessage) []host.CoCreateMessage {
	out := make([]host.CoCreateMessage, 0, len(history))
	for _, message := range history {
		role := strings.TrimSpace(message.Role)
		content := strings.TrimSpace(message.Content)
		if role == "" || content == "" {
			continue
		}
		switch role {
		case "user", "assistant", "system":
			out = append(out, host.CoCreateMessage{Role: role, Content: content})
		}
	}
	return out
}

func inferWebCoCreateKindFromLog(history []host.CoCreateMessage) string {
	if len(history) == 0 {
		return webCoCreateKindNormal
	}
	first := history[0].Content
	if strings.Contains(first, "granularity=") && strings.Contains(first, "rewrite_policy=") {
		return webCoCreateKindAdapt
	}
	if strings.TrimSpace(first) == strings.TrimSpace(stageCoCreateOpener) {
		return webCoCreateKindStage
	}
	if strings.TrimSpace(first) == strings.TrimSpace(continuationCoCreateOpener) {
		return webCoCreateKindContinuation
	}
	return webCoCreateKindNormal
}

func coCreateLogAdaptOptions(opener string) (string, string, float64) {
	granularity := strings.TrimSpace(coCreateLogKey(opener, "granularity"))
	if normalized, ok := domain.StrictAdaptationGranularity(granularity); ok {
		granularity = normalized
	} else {
		granularity = domain.AdaptationGranularityChapter
	}
	rewritePolicy := strings.TrimSpace(coCreateLogKey(opener, "rewrite_policy"))
	if rewritePolicy == "" {
		rewritePolicy = domain.AdaptationRewritePolicyForGranularity(granularity)
	}
	tolerance := adapt.DefaultWordTolerance
	if raw := strings.TrimSpace(coCreateLogKey(opener, "word_tolerance")); raw != "" && raw != "disabled" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed > 0 {
			tolerance = parsed
		}
	}
	return normalizeWebAdaptCoCreateOptions(granularity, rewritePolicy, tolerance)
}

func coCreateLogKey(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		left, right, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(left) != key {
			continue
		}
		return strings.TrimSpace(right)
	}
	return ""
}

func coCreateLogDraftState(entry webCoCreateLogEntry, history []host.CoCreateMessage) (string, bool, []string) {
	draft := strings.TrimSpace(entry.ParsedDraft)
	ready := entry.ParsedReady
	suggestions := append([]string(nil), entry.ParsedSuggestions...)
	if draft != "" {
		return draft, ready, suggestions
	}
	for idx := len(history) - 1; idx >= 0; idx-- {
		if history[idx].Role != "assistant" {
			continue
		}
		draft = extractCoCreateLogTag(history[idx].Content, "draft")
		if draft == "" {
			continue
		}
		ready = strings.EqualFold(strings.TrimSpace(extractCoCreateLogTag(history[idx].Content, "ready")), "true")
		if len(suggestions) == 0 && len(history) > 0 && history[len(history)-1].Role == "assistant" {
			suggestions = splitCoCreateLogSuggestions(extractCoCreateLogTag(history[idx].Content, "suggestions"))
		}
		return draft, ready, suggestions
	}
	return draft, ready, suggestions
}

func splitCoCreateLogSuggestions(text string) []string {
	var suggestions []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-* "))
		if line != "" {
			suggestions = append(suggestions, line)
		}
	}
	return suggestions
}

func extractCoCreateLogTag(text, tag string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(text, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(text[start:], close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}

func webCoCreateMessagesFromLog(kind string, history []host.CoCreateMessage, assistantMessage string, appendAssistant bool) []webCoCreateMessage {
	state := &webCoCreateSession{kind: kind}
	if kind == webCoCreateKindAdapt {
		state.messages = append(state.messages, state.newMessage("system", adaptCoCreateSystemLine, "", -1))
	}
	if kind == webCoCreateKindStage {
		state.messages = append(state.messages, state.newMessage("system", stageCoCreateSystemLine, "", -1))
	}
	if kind == webCoCreateKindContinuation {
		state.messages = append(state.messages, state.newMessage("system", continuationCoCreateSystemLine, "", -1))
	}
	for idx, message := range history {
		if coCreateLogMessageHidden(kind, idx, message) {
			continue
		}
		content := message.Content
		source := ""
		if message.Role == "assistant" {
			if reply := extractCoCreateLogTag(content, "reply"); reply != "" {
				content = reply
			}
		} else if message.Role == "user" {
			source = "custom"
		}
		state.messages = append(state.messages, state.newMessage(message.Role, content, source, idx))
	}
	if appendAssistant && strings.TrimSpace(assistantMessage) != "" {
		state.messages = append(state.messages, state.newMessage("assistant", assistantMessage, "", len(history)))
	}
	return state.messages
}

func coCreateLogMessageHidden(kind string, index int, message host.CoCreateMessage) bool {
	if message.Role == "user" && kind == webCoCreateKindAdapt && strings.Contains(message.Content, adaptDecisionDraftMarker) {
		return true
	}
	if index != 0 || message.Role != "user" {
		return false
	}
	switch kind {
	case webCoCreateKindAdapt:
		return strings.Contains(message.Content, "granularity=") && strings.Contains(message.Content, "rewrite_policy=")
	case webCoCreateKindStage:
		return strings.TrimSpace(message.Content) == strings.TrimSpace(stageCoCreateOpener)
	case webCoCreateKindContinuation:
		return strings.TrimSpace(message.Content) == strings.TrimSpace(continuationCoCreateOpener)
	default:
		return false
	}
}

func coCreateMessageSource(source string) string {
	switch strings.TrimSpace(source) {
	case "suggestion":
		return "suggestion"
	case "custom":
		return "custom"
	default:
		return ""
	}
}

func (s *webCoCreateSession) appendUser(text, source string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	historyIndex := len(s.session.History())
	s.session.AppendUser(text)
	s.draftConsolidated = false
	if coCreateMessageSource(source) == "" {
		source = "custom"
	}
	s.messages = append(s.messages, s.newMessage("user", text, source, historyIndex))
	return nil
}

func (s *webCoCreateSession) appendInternalUser(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	s.session.AppendUser(text)
	s.draftConsolidated = false
	return nil
}

func (s *webCoCreateSession) applyReply(reply host.CoCreateReply) {
	historyIndex := len(s.session.History())
	s.session.ApplyReply(reply)
	if reply.CoreCast != nil && (s.kind == webCoCreateKindNormal || s.kind == webCoCreateKindAdapt) {
		cast := *reply.CoreCast
		cast.DraftRevision = s.session.DraftRevision()
		cast.DraftHash = s.session.DraftHash()
		if s.kind == webCoCreateKindAdapt {
			cast.Mode = domain.CoreCastModeAdaptation
			if s.adaptationBriefing != nil {
				cast.SourceSignature = strings.TrimSpace(s.adaptationBriefing.SourceSignature)
				cast.AdaptationIntentHash = strings.TrimSpace(s.adaptationBriefing.IntentHash)
			}
		} else {
			cast.Mode = domain.CoreCastModeNormal
			cast.SourceSignature = ""
			cast.AdaptationIntentHash = ""
		}
		s.coreCast = &cast
		s.castError = ""
	}
	s.draftConsolidated = false
	if text := strings.TrimSpace(reply.Message); text != "" {
		s.messages = append(s.messages, s.newMessage("assistant", text, "", historyIndex))
	}
}

func (s *webCoCreateSession) rollbackDraftAfterRejectedReply(reply host.CoCreateReply) {
	if s == nil || s.session == nil {
		return
	}
	historyIndex := len(s.session.History())
	s.session.ApplyReply(host.CoCreateReply{})
	s.draftConsolidated = false
	if text := strings.TrimSpace(reply.Message); text != "" {
		s.messages = append(s.messages, s.newMessage("assistant", text, "", historyIndex))
	}
}

func (s *webCoCreateSession) draftNeedsRepair(reply host.CoCreateReply, previousDraft string) bool {
	if s == nil || s.session == nil {
		return false
	}
	prompt := strings.TrimSpace(reply.Prompt)
	if prompt == "" {
		return true
	}
	if draftPromptRegressed(previousDraft, prompt) {
		return true
	}
	if strings.TrimSpace(previousDraft) != "" && prompt == strings.TrimSpace(previousDraft) && s.session.DraftStale() {
		return true
	}
	return false
}

func (s *webCoCreateSession) currentDraftNeedsRepair() (bool, string) {
	if s == nil || s.session == nil {
		return false, ""
	}
	currentDraft := s.draftPrompt()
	if strings.TrimSpace(currentDraft) == "" {
		return false, ""
	}
	baseDraft := s.previousStableDraft(currentDraft)
	if s.latestHistoryDraftNeedsRepair(currentDraft) {
		return true, baseDraft
	}
	if containsDraftOmissionPlaceholder(currentDraft) {
		return true, baseDraft
	}
	if baseDraft != "" && draftPromptRegressed(baseDraft, currentDraft) {
		return true, baseDraft
	}
	return false, currentDraft
}

func (s *webCoCreateSession) shouldConsolidateDraftBeforeCommit() bool {
	if s == nil || s.session == nil || s.kind != webCoCreateKindAdapt || !s.session.DraftFresh() {
		return false
	}
	if s.draftConsolidated {
		return false
	}
	return coCreatePlanningUserMessageCount(s.kind, s.session.History()) > 1
}

func coCreatePlanningUserMessageCount(kind string, history []host.CoCreateMessage) int {
	count := 0
	start := 0
	if len(history) > 0 && coCreateLogMessageHidden(kind, 0, history[0]) {
		start = 1
	}
	for idx := start; idx < len(history); idx++ {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "user" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		count++
	}
	return count
}

func (s *webCoCreateSession) latestHistoryDraftNeedsRepair(currentDraft string) bool {
	if s == nil || s.session == nil {
		return false
	}
	history := s.session.History()
	for idx := len(history) - 1; idx >= 0; idx-- {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		draft := extractCoCreateLogTag(message.Content, "draft")
		if draft == "" {
			continue
		}
		if containsDraftOmissionPlaceholder(draft) {
			return true
		}
		baseDraft := previousStableDraftInHistory(history[:idx], currentDraft)
		return baseDraft != "" && draftPromptRegressed(baseDraft, draft)
	}
	return false
}

func (s *webCoCreateSession) previousStableDraft(currentDraft string) string {
	if s == nil || s.session == nil {
		return ""
	}
	currentDraft = strings.TrimSpace(currentDraft)
	return previousStableDraftInHistory(s.session.History(), currentDraft)
}

func previousStableDraftInHistory(history []host.CoCreateMessage, currentDraft string) string {
	currentDraft = strings.TrimSpace(currentDraft)
	skippedCurrent := currentDraft == ""
	for idx := len(history) - 1; idx >= 0; idx-- {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		draft := extractCoCreateLogTag(message.Content, "draft")
		if draft == "" {
			continue
		}
		if !skippedCurrent && strings.TrimSpace(draft) == currentDraft {
			skippedCurrent = true
			continue
		}
		if !containsDraftOmissionPlaceholder(draft) {
			return draft
		}
	}
	if containsDraftOmissionPlaceholder(currentDraft) {
		return ""
	}
	return currentDraft
}

func draftPromptRegressed(previousDraft, nextDraft string) bool {
	previousDraft = strings.TrimSpace(previousDraft)
	nextDraft = strings.TrimSpace(nextDraft)
	if previousDraft == "" || nextDraft == "" {
		return false
	}
	if containsDraftOmissionPlaceholder(nextDraft) {
		return true
	}
	previousLen := len([]rune(previousDraft))
	nextLen := len([]rune(nextDraft))
	return previousLen >= 1200 && nextLen < previousLen*2/3
}

func containsDraftOmissionPlaceholder(draft string) bool {
	draft = strings.ToLower(strings.TrimSpace(draft))
	if draft == "" {
		return false
	}
	placeholders := []string{
		"同上",
		"其余同上",
		"其他同上",
		"其余设定同上",
		"其他设定同上",
		"同前轮",
		"同上一轮",
		"如上",
		"前述",
		"前文",
		"前面",
		"前轮",
		"上一轮",
		"上轮",
		"上一稿",
		"上一版",
		"前一稿",
		"前一版",
		"此前",
		"已完整记录",
		"不再重复",
		"不赘述",
		"见上",
		"沿用上一轮",
		"沿用上轮",
		"沿用上一稿",
		"沿用上一版",
		"保持上一轮",
		"保持上轮",
		"保持上一稿",
		"保持上一版",
		"保留上一轮",
		"保留上轮",
		"保留上一稿",
		"保留上一版",
		"previous round",
		"previous draft",
		"last round",
		"last draft",
		"same as above",
		"as above",
	}
	for _, line := range strings.Split(draft, "\n") {
		line = normalizeDraftOmissionLine(line)
		for _, placeholder := range placeholders {
			if line == placeholder {
				return true
			}
		}
	}
	return false
}

func normalizeDraftOmissionLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "#*-•0123456789.、()（） \t")
	line = strings.TrimSpace(line)
	return strings.Trim(line, "。；;，,：:")
}

func (s *webCoCreateSession) draftRepairHistory(reply host.CoCreateReply, previousDraft string) []host.CoCreateMessage {
	if s == nil || s.session == nil {
		return nil
	}
	history := compactDraftRepairHistory(s.session.History(), previousDraft)
	if raw := strings.TrimSpace(reply.Raw); raw != "" {
		history = append(history, host.CoCreateMessage{Role: "assistant", Content: raw})
	} else if message := strings.TrimSpace(reply.Message); message != "" {
		history = append(history, host.CoCreateMessage{Role: "assistant", Content: message})
	}
	history = append(history, host.CoCreateMessage{Role: "user", Content: coCreateDraftRepairInstruction(s.kind, previousDraft)})
	return history
}

func compactDraftRepairHistory(history []host.CoCreateMessage, previousDraft string) []host.CoCreateMessage {
	out := make([]host.CoCreateMessage, 0, len(history)+2)
	if len(history) > 0 {
		out = append(out, history[0])
	}
	if previousDraft = strings.TrimSpace(previousDraft); previousDraft != "" {
		out = append(out, host.CoCreateMessage{
			Role:    "assistant",
			Content: "<draft>\n" + previousDraft + "\n</draft>",
		})
	}
	recentAssistant := recentNonDraftAssistantIndexes(history, 3)
	for idx := 1; idx < len(history); idx++ {
		message := history[idx]
		role := strings.TrimSpace(message.Role)
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if role == "user" {
			out = append(out, message)
			continue
		}
		if _, ok := recentAssistant[idx]; ok {
			out = append(out, message)
		}
	}
	return out
}

func recentNonDraftAssistantIndexes(history []host.CoCreateMessage, limit int) map[int]struct{} {
	out := make(map[int]struct{})
	if limit <= 0 {
		return out
	}
	for idx := len(history) - 1; idx >= 1 && len(out) < limit; idx-- {
		message := history[idx]
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" || extractCoCreateLogTag(content, "draft") != "" {
			continue
		}
		out[idx] = struct{}{}
	}
	return out
}

func coCreateDraftRepairInstruction(kind string, previousDraft string) string {
	instruction := `Internal draft consolidation request:
The previous response did not produce a stable, complete <draft>, or the draft did not absorb the latest user discussion.
Update the previous stable draft with all confirmed planning decisions from the user messages above, especially turns after the previous stable draft, then return the normal four XML tags: <reply>, <draft>, <ready>, <suggestions>.
Do not paste the chat transcript into <draft>. Distill confirmed decisions into one executable Markdown draft.
The <draft> must be complete, current, and preserve the previous stable draft while adding the latest confirmed decisions.
If required decisions are still missing, write the known decisions plus a "pending decisions" section in <draft> and set <ready>false</ready>.
Never use placeholders such as "same as above", "同上", "同前轮", "上一轮", "已完整记录", "完整保留", "不再重复", or "见上"; the draft must be self-contained.`
	if kind == webCoCreateKindAdapt {
		instruction += `
For adaptation co-create, preserve the selected granularity, rewrite_policy, and word_tolerance exactly as originally provided. Do not ask the user to choose chapter/arc/free again.
Keep the repaired draft at brief level: merge overlapping rules, preserve hard relationship/mainline constraints, and keep only key source chapter anchors. Do not create chapter-by-chapter strategy, volume outline, per-chapter plot beats, or a "## 逐章策略" section. Source chapter numbers are anchors, not target chapter-count requests.`
	}
	if previousDraft = strings.TrimSpace(previousDraft); previousDraft != "" {
		instruction += "\n\nPrevious stable draft to preserve and merge:\n<previous_draft>\n" + previousDraft + "\n</previous_draft>"
	}
	return strings.TrimSpace(instruction)
}

func (s *webCoCreateSession) reviseUser(messageID, text string) error {
	messageID = strings.TrimSpace(messageID)
	text = strings.TrimSpace(text)
	if messageID == "" {
		return fmt.Errorf("message_id is required")
	}
	if text == "" {
		return fmt.Errorf("text is required")
	}
	for idx := range s.messages {
		message := s.messages[idx]
		if message.ID != messageID {
			continue
		}
		if message.Role != "user" || !message.Editable || message.historyIndex < 0 {
			return fmt.Errorf("message is not editable")
		}
		history := s.session.History()
		if message.historyIndex >= len(history) || history[message.historyIndex].Role != "user" {
			return fmt.Errorf("message history is no longer editable")
		}
		history = append([]host.CoCreateMessage(nil), history[:message.historyIndex+1]...)
		history[message.historyIndex] = host.CoCreateMessage{Role: "user", Content: text}
		s.session.ResetHistory(history)
		s.draftConsolidated = false
		s.messages = append([]webCoCreateMessage(nil), s.messages[:idx+1]...)
		s.messages[idx].Content = text
		if s.messages[idx].Source == "" {
			s.messages[idx].Source = "custom"
		}
		return nil
	}
	return fmt.Errorf("message not found")
}

func (s *webCoCreateSession) requireReadyDraft() error {
	if s == nil {
		return fmt.Errorf("co-create has not started")
	}
	if strings.TrimSpace(s.draftPrompt()) == "" {
		return fmt.Errorf("draft prompt is required")
	}
	if s.session == nil || !s.session.DraftFresh() {
		return fmt.Errorf("co-create draft is not up to date; continue co-create until the latest discussion is consolidated")
	}
	return nil
}

func (s *webCoCreateSession) draftPrompt() string {
	if s == nil || s.session == nil {
		return ""
	}
	return strings.TrimSpace(s.session.DraftPrompt())
}

func (s *webCoCreateSession) pendingBriefingDecisions() []domain.AdaptationBriefingDecision {
	if s == nil || s.kind != webCoCreateKindAdapt {
		return nil
	}
	return adapt.PendingCoCreateBriefingDecisions(s.adaptationBriefing)
}

func (s *webCoCreateSession) hasPendingBriefingDecisions() bool {
	return len(s.pendingBriefingDecisions()) > 0
}

func (s *webCoCreateSession) apiState() webCoCreateState {
	if s == nil {
		return webCoCreateState{}
	}
	var legacyCoreCast *domain.CoreCastContract
	if s.supportsLegacyCoreCast() {
		legacyCoreCast = cloneWebCoreCast(s.coreCast)
	}
	canStart := s.session.CanStart()
	completion := s.castCompletion()
	blockingReasons := []string{}
	castConfirmed := false
	castSignature := ""
	if legacyCoreCast != nil {
		castSignature = legacyCoreCast.ContentSignature
		castConfirmed = castSignature != "" && legacyCoreCast.ConfirmedSignature == castSignature
	}
	if needsRepair, _ := s.currentDraftNeedsRepair(); needsRepair {
		canStart = false
	}
	pendingDecisions := s.pendingBriefingDecisions()
	briefingState := coCreateBriefingState(s.adaptationBriefing)
	blockedReason := ""
	if s.needsAdaptBriefingBeforeDraft() {
		canStart = false
		blockedReason = "prepare adaptation co-create briefing before draft generation"
	} else if len(pendingDecisions) > 0 {
		canStart = false
		blockedReason = "resolve adaptation briefing decisions before draft generation"
	}
	if len(blockingReasons) > 0 && blockedReason == "" {
		blockedReason = blockingReasons[0]
	}
	return webCoCreateState{
		Kind:                  s.kind,
		Active:                true,
		Messages:              webCoCreateDisplayMessages(s.kind, s.messages),
		DraftPrompt:           s.draftPrompt(),
		Ready:                 s.session.Ready(),
		Suggestions:           append([]string(nil), s.session.Suggestions()...),
		StreamThinking:        s.session.StreamThinking(),
		StreamReply:           normalizeWebCoCreateText(s.kind, s.session.StreamReply()),
		AdaptMode:             s.adaptGranularity,
		RewritePolicy:         s.adaptRewritePolicy,
		WordTolerance:         s.adaptWordTolerance,
		TargetTotalWords:      s.targetTotalWords,
		SourceFile:            s.sourceFile,
		Proposal:              s.adaptationProposal,
		VolumeReview:          s.adaptationVolumeReview,
		CanStart:              canStart,
		ModeLocked:            s.kind == webCoCreateKindAdapt,
		Failed:                s.failed,
		Briefing:              briefingState,
		PendingDecisions:      pendingDecisions,
		BlockedReason:         blockedReason,
		CoreCast:              legacyCoreCast,
		SourceMajorCharacters: append([]domain.SourceMajorCharacter(nil), s.sourceMajorCharacters...),
		CastCompletion:        completion,
		CastConfirmed:         castConfirmed,
		CastSignature:         castSignature,
		BlockingReasons:       blockingReasons,
	}
}

func (s *Server) handleProjectCoreCast(w http.ResponseWriter, r *http.Request, id, action string) {
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	var state webCoCreateState
	switch action {
	case "update":
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req webCoreCastUpdateRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid core cast request: "+err.Error())
			return
		}
		state, err = session.UpdateCoreCast(req.CoreCast, req.ExpectedRevision)
	case "confirm":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req webCoreCastConfirmRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid core cast confirmation: "+err.Error())
			return
		}
		state, err = session.ConfirmCoreCast(req.ExpectedRevision, req.ContentSignature)
	case "unconfirm":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req webCoreCastConfirmRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid core cast unconfirm request: "+err.Error())
			return
		}
		state, err = session.UnconfirmCoreCast(req.ExpectedRevision)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeCoreCastActionError(w, err, state)
		return
	}
	writeCoCreateResponse(w, manifest, session, state)
}

func (s *Server) handleProjectCharacterCandidateConfirm(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	var request webCharacterCandidateConfirmRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid character confirmation: "+err.Error())
		return
	}
	result, err := session.ConfirmCharacterCandidate(request)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":   manifest,
		"character": result,
		"runtime":   session.Snapshot(),
	})
}

func (s *Server) handleProjectCharacterCandidate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	var request webCharacterCandidateEditRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid character edit: "+err.Error())
		return
	}
	candidate, lifecycle, err := session.EditCharacterCandidate(request)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":   manifest,
		"candidate": candidate,
		"lifecycle": lifecycle,
		"runtime":   session.Snapshot(),
	})
}

func writeCoreCastActionError(w http.ResponseWriter, err error, state webCoCreateState) {
	status := http.StatusInternalServerError
	code := "core_cast_storage_error"
	message := "core cast operation failed"
	details := map[string]any{}
	var revisionConflict *storepkg.CoreCastConflictError
	var signatureConflict *storepkg.CoreCastSignatureConflictError
	var validation *storepkg.CoreCastValidationError
	switch {
	case errors.As(err, &revisionConflict):
		status = http.StatusConflict
		code = "core_cast_revision_conflict"
		message = err.Error()
		details["latest_revision"] = revisionConflict.Actual
		details["latest_signature"] = revisionConflict.Signature
		details["cast_completion"] = state.CastCompletion
	case errors.As(err, &signatureConflict):
		status = http.StatusConflict
		code = "core_cast_signature_conflict"
		message = err.Error()
		details["latest_revision"] = signatureConflict.Revision
		details["latest_signature"] = signatureConflict.Actual
		details["cast_completion"] = state.CastCompletion
	case errors.As(err, &validation):
		status = http.StatusUnprocessableEntity
		code = "core_cast_invalid"
		message = err.Error()
	case strings.Contains(err.Error(), "stale") || strings.Contains(err.Error(), "gate") || strings.Contains(err.Error(), "does not exist"):
		status = http.StatusConflict
		code = "core_cast_gate_conflict"
		message = err.Error()
	}
	details["code"] = code
	details["message"] = message
	writeJSON(w, status, map[string]any{"error": details, "cocreate": state})
}

// supportsLegacyCoreCast keeps old five-section checkpoints editable while new
// co-create responses leave all Character ownership to the Character Agent.
func (s *webCoCreateSession) supportsLegacyCoreCast() bool {
	return s != nil && s.kind == webCoCreateKindAdapt
}

func (s *webCoCreateSession) coreCastResumeExempt() bool {
	return s != nil && (s.kind == webCoCreateKindStage || s.kind == webCoCreateKindContinuation)
}

func (s *webCoCreateSession) castCompletion() domain.CoreCastCompletionResult {
	if s == nil || !s.supportsLegacyCoreCast() || s.coreCast == nil {
		return domain.CoreCastCompletionResult{Complete: true, Missing: []domain.CoreCastMissingItem{}, BlockingReasons: []string{}}
	}
	result := domain.CoreCastCompletion(*s.coreCast, s.sourceCharacters, s.sourceMajorCharacters)
	return webCoreCastCompletionWithExtra(result, s.sourceResolutionMissing)
}

func webCoreCastCompletionWithExtra(base domain.CoreCastCompletionResult, extra []domain.CoreCastMissingItem) domain.CoreCastCompletionResult {
	if len(extra) == 0 {
		if base.Missing == nil {
			base.Missing = []domain.CoreCastMissingItem{}
		}
		if base.BlockingReasons == nil {
			base.BlockingReasons = []string{}
		}
		return base
	}
	base.Missing = append(base.Missing, extra...)
	seen := make(map[string]struct{}, len(base.BlockingReasons))
	for _, reason := range base.BlockingReasons {
		seen[reason] = struct{}{}
	}
	for _, item := range extra {
		if _, ok := seen[item.Description]; !ok {
			base.BlockingReasons = append(base.BlockingReasons, item.Description)
			seen[item.Description] = struct{}{}
		}
	}
	base.Complete = false
	return base
}

func cloneWebCoreCast(value *domain.CoreCastContract) *domain.CoreCastContract {
	if value == nil {
		return nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var clone domain.CoreCastContract
	if json.Unmarshal(payload, &clone) != nil {
		return nil
	}
	return &clone
}

func coCreateBriefingState(briefing *domain.AdaptationCoCreateBriefing) *webCoCreateBriefingState {
	if briefing == nil {
		return nil
	}
	return &webCoCreateBriefingState{
		Active:                true,
		TriggerReason:         briefing.TriggerReason,
		PendingDecisionCount:  len(adapt.PendingCoCreateBriefingDecisions(briefing)),
		ResolvedDecisionCount: len(briefing.ResolvedDecisions),
		TotalDecisionCount:    len(briefing.Decisions),
	}
}

func webCoCreateDisplayMessages(kind string, messages []webCoCreateMessage) []webCoCreateMessage {
	out := append([]webCoCreateMessage(nil), messages...)
	for i := range out {
		if out[i].Role == "assistant" {
			out[i].Content = normalizeWebCoCreateText(kind, out[i].Content)
		}
	}
	return out
}

func normalizeWebCoCreateText(kind string, text string) string {
	if text == "" {
		return ""
	}
	return strings.NewReplacer(
		"可以按 Ctrl+S 把方向交给创作引擎、继续创作", "可以点击「启动」把方向交给创作引擎并继续创作",
		"可以按 Ctrl+S 应用方向并继续创作", "可以点击「启动」应用方向并继续创作",
		"可以按 Ctrl+S 开始改编", "可以点击「启动」开始改编",
		"可以按 Ctrl+S 开始创作", "可以点击「启动」开始创作",
		"可以按 Ctrl+S 开始", "可以点击「启动」开始",
		"按 Ctrl+S 把方向交给创作引擎、继续创作", "点击「启动」把方向交给创作引擎并继续创作",
		"按 Ctrl+S 应用方向并继续创作", "点击「启动」应用方向并继续创作",
		"按 Ctrl+S 开始改编", "点击「启动」开始改编",
		"按 Ctrl+S 开始创作", "点击「启动」开始创作",
		"按 Ctrl+S 开始", "点击「启动」开始",
		"Ctrl+S", "点击「启动」",
	).Replace(text)
}

func adaptCoCreateOpener(granularity, rewritePolicy string, wordTolerance float64) string {
	_ = rewritePolicy
	modeContract := startup.AdaptationModeContract(granularity, wordTolerance)
	return strings.TrimSpace(fmt.Sprintf(`我想基于这本小说做改编，已确认改编模式如下：

%s

请基于原书分析帮我确认具体改编目标。只围绕上面的当前模式整理 brief，不要写入其它模式的规则，也不要再询问或改动 chapter/arc/free 与 full_rewrite/preserve_details 这两个模式选择。`,
		modeContract))
}

func coCreateAdaptIntentRaw(initial string) string {
	initial = strings.TrimSpace(initial)
	if initial != "" {
		return initial
	}
	return "基于原书分析确认具体改编目标。"
}

func (s *webCoCreateSession) adaptBriefingIntent() domain.AdaptationCoCreateIntent {
	if s == nil {
		return adapt.BuildCoCreateIntent(coCreateAdaptIntentRaw(""), "", "", 0)
	}
	return adapt.BuildCoCreateIntent(
		coCreateAdaptIntentRaw(s.currentAdaptCoCreateRequest()),
		s.adaptGranularity,
		s.adaptRewritePolicy,
		s.adaptWordTolerance,
	)
}

func (s *webCoCreateSession) initialAdaptCoCreateRequest() string {
	requests := s.adaptCoCreateUserRequests()
	if len(requests) == 0 {
		return ""
	}
	return requests[0]
}

func (s *webCoCreateSession) currentAdaptCoCreateRequest() string {
	requests := s.adaptCoCreateUserRequests()
	if len(requests) == 0 {
		return ""
	}
	resolved := resolvedAdaptBriefingDecisionLines(s.adaptationBriefing)
	if len(requests) == 1 && len(resolved) == 0 {
		return requests[0]
	}
	var sb strings.Builder
	for idx, request := range requests {
		if idx > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "用户补充 %d:\n%s", idx+1, request)
	}
	if len(resolved) > 0 {
		sb.WriteString("\n\n已确认选项:\n")
		for _, line := range resolved {
			fmt.Fprintf(&sb, "- %s\n", line)
		}
	}
	return strings.TrimSpace(sb.String())
}

func (s *webCoCreateSession) adaptCoCreateUserRequests() []string {
	if s == nil || s.session == nil {
		return nil
	}
	var requests []string
	history := s.session.History()
	for idx, message := range history {
		if coCreateLogMessageHidden(webCoCreateKindAdapt, idx, message) {
			continue
		}
		if strings.TrimSpace(message.Role) != "user" {
			continue
		}
		if content := strings.TrimSpace(message.Content); content != "" {
			requests = append(requests, content)
		}
	}
	return requests
}

func resolvedAdaptBriefingDecisionLines(briefing *domain.AdaptationCoCreateBriefing) []string {
	if briefing == nil || len(briefing.ResolvedDecisions) == 0 {
		return nil
	}
	decisions := make(map[string]domain.AdaptationBriefingDecision, len(briefing.Decisions))
	for _, decision := range briefing.Decisions {
		if id := strings.TrimSpace(decision.ID); id != "" {
			decisions[id] = decision
		}
	}
	lines := make([]string, 0, len(briefing.ResolvedDecisions))
	for _, resolved := range briefing.ResolvedDecisions {
		decisionID := strings.TrimSpace(resolved.DecisionID)
		decision := decisions[decisionID]
		answer := strings.TrimSpace(resolved.CustomAnswer)
		if answer == "" {
			answer = adaptBriefingDecisionOptionLabel(decision.Options, resolved.OptionID)
		}
		question := strings.TrimSpace(decision.Question)
		switch {
		case question != "" && answer != "":
			lines = append(lines, question+" => "+answer)
		case question != "":
			lines = append(lines, question)
		case answer != "":
			lines = append(lines, answer)
		}
	}
	return lines
}

func adaptDecisionDraftBatches(briefing *domain.AdaptationCoCreateBriefing, batchSize int) [][]string {
	lines := resolvedAdaptBriefingDecisionLines(briefing)
	if len(lines) == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = adaptDecisionDraftBatchSize
	}
	batches := make([][]string, 0, (len(lines)+batchSize-1)/batchSize)
	for start := 0; start < len(lines); start += batchSize {
		end := start + batchSize
		if end > len(lines) {
			end = len(lines)
		}
		batches = append(batches, append([]string(nil), lines[start:end]...))
	}
	return batches
}

func adaptDecisionDraftBatchInstruction(index, total int, decisions []string, hasDraft bool) string {
	if index <= 0 {
		index = 1
	}
	if total <= 0 {
		total = 1
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %d/%d\n", adaptDecisionDraftMarker, index, total)
	if hasDraft {
		sb.WriteString("Update the existing <draft> by integrating only the confirmed decisions in this batch. Preserve prior hard constraints, but merge overlapping rules instead of expanding them.\n")
	} else {
		sb.WriteString("Create the initial adaptation <draft> from the mode contract, source briefing, and only the confirmed decisions in this batch.\n")
	}
	sb.WriteString("Return the normal four XML tags: <reply>, <draft>, <ready>, <suggestions>.\n")
	sb.WriteString("The <draft> must be complete and self-contained after this batch; never use placeholders such as same as above, 同上, 同前轮, 上一轮, or 见上.\n")
	sb.WriteString("Keep the <draft> concise and brief-level: do not write chapter-by-chapter strategy, volume outline, single-chapter details, or a ## 逐章策略 section. Source chapter numbers are anchors only, not target chapter-count requests.\n")
	if index < total {
		sb.WriteString("This is not the final decision batch. Mention that more confirmed decisions will be integrated next and set <ready>false</ready>.\n")
	} else {
		sb.WriteString("This is the final decision batch. After integrating it, set <ready>true</ready> if the draft can drive adaptation planning.\n")
	}
	sb.WriteString("\nConfirmed decisions in this batch:\n")
	for _, decision := range decisions {
		if decision = strings.TrimSpace(decision); decision != "" {
			fmt.Fprintf(&sb, "- %s\n", decision)
		}
	}
	return strings.TrimSpace(sb.String())
}

func (req webCoCreateDecisionRequest) resolvedDecisionItems() []domain.AdaptationResolvedDecision {
	items := req.Decisions
	if len(items) == 0 {
		items = []webCoCreateDecisionItem{req.webCoCreateDecisionItem}
	}
	out := make([]domain.AdaptationResolvedDecision, 0, len(items))
	for _, item := range items {
		out = append(out, domain.AdaptationResolvedDecision{
			DecisionID:   strings.TrimSpace(item.DecisionID),
			OptionID:     strings.TrimSpace(item.OptionID),
			CustomAnswer: strings.TrimSpace(item.CustomAnswer),
		})
	}
	return out
}

func clipWebRunes(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func adaptBriefingDecisionOptionLabel(options []domain.AdaptationDecisionOption, optionID string) string {
	optionID = strings.TrimSpace(optionID)
	for _, option := range options {
		if strings.TrimSpace(option.ID) != optionID {
			continue
		}
		label := strings.TrimSpace(option.Label)
		if description := strings.TrimSpace(option.Description); description != "" {
			if label == "" {
				return description
			}
			return label + ": " + description
		}
		return label
	}
	return optionID
}

func (s *webCoCreateSession) needsAdaptBriefingBeforeDraft() bool {
	return s != nil &&
		s.kind == webCoCreateKindAdapt &&
		s.adaptationBriefing == nil &&
		strings.TrimSpace(s.draftPrompt()) == ""
}

func (s *webCoCreateSession) needsAdaptBriefingRefresh(force bool) bool {
	if s == nil || s.kind != webCoCreateKindAdapt {
		return false
	}
	if s.needsAdaptBriefingBeforeDraft() {
		return true
	}
	if !force {
		return false
	}
	if s.adaptationBriefing == nil {
		return true
	}
	intent := s.adaptBriefingIntent()
	return strings.TrimSpace(intent.IntentHash) != "" &&
		strings.TrimSpace(s.adaptationBriefing.IntentHash) != strings.TrimSpace(intent.IntentHash)
}

func normalizeWebAdaptCoCreateOptions(granularity, rewritePolicy string, wordTolerance float64) (string, string, float64) {
	normalizedGranularity, ok := domain.StrictAdaptationGranularity(strings.TrimSpace(granularity))
	if !ok {
		normalizedGranularity = domain.AdaptationGranularityChapter
	}
	normalizedRewritePolicy := strings.TrimSpace(rewritePolicy)
	if normalizedRewritePolicy == "" {
		normalizedRewritePolicy = domain.AdaptationRewritePolicyForGranularity(normalizedGranularity)
	}
	return normalizedGranularity, normalizedRewritePolicy, startup.AdaptationWordToleranceForGranularity(normalizedGranularity, wordTolerance)
}

func decodeCoCreateSendRequest(r *http.Request) (webCoCreateSendRequest, error) {
	var req webCoCreateSendRequest
	if err := decodeJSONBody(r, &req); err != nil {
		return req, fmt.Errorf("invalid request body: %w", err)
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return req, fmt.Errorf("text is required")
	}
	switch strings.TrimSpace(req.Source) {
	case "", "custom", "suggestion":
		req.Source = strings.TrimSpace(req.Source)
	default:
		return req, fmt.Errorf("source must be custom or suggestion")
	}
	return req, nil
}

func decodeJSONBody(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxCoCreateJSONBodyBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxCoCreateJSONBodyBytes {
		return fmt.Errorf("request body exceeds %d bytes", maxCoCreateJSONBodyBytes)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func decodeJSONBodyRaw(r *http.Request, target any) (json.RawMessage, error) {
	if r.Body == nil {
		return json.RawMessage(`{}`), nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxCoCreateJSONBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxCoCreateJSONBodyBytes {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxCoCreateJSONBodyBytes)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("request body must contain exactly one JSON value")
		}
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func writeCoCreateResponse(w http.ResponseWriter, manifest ProjectManifest, session *ProjectSession, state webCoCreateState) {
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  manifest,
		"snapshot": session.WebSnapshot(),
		"cocreate": state,
		"running":  session.Snapshot().IsRunning,
	})
}

func writeCoCreateActionError(w http.ResponseWriter, err error, state webCoCreateState) {
	status := http.StatusConflict
	if errors.Is(err, ErrSessionActionInProgress) {
		status = http.StatusConflict
	} else if isBadCoCreateRequest(err) {
		status = http.StatusBadRequest
	}
	message := err.Error()
	if cleaned := retrypolicy.SanitizeProviderError(err); cleaned != "" {
		message = cleaned
	}
	body := map[string]any{"error": message}
	if state.Kind != "" {
		body["cocreate"] = state
	}
	writeJSON(w, status, body)
}

func isBadCoCreateRequest(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "is required") ||
		strings.Contains(text, "must be one of") ||
		strings.Contains(text, "must be custom or suggestion") ||
		strings.Contains(text, "not found") ||
		strings.Contains(text, "not editable") ||
		strings.Contains(text, "not ready") ||
		strings.Contains(text, "non-negative integer") ||
		strings.Contains(text, "has not started")
}

func coCreateCommitLabel(kind string) string {
	if kind == webCoCreateKindAdapt {
		return "改编提案已生成"
	}
	switch kind {
	case webCoCreateKindStage:
		return "阶段方向已应用"
	case webCoCreateKindContinuation:
		return "续写 Draft 已确认"
	case webCoCreateKindAdapt:
		return "改编已启动"
	default:
		return "创作已启动"
	}
}
