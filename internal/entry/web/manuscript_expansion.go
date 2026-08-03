package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/expansionauditorclient"
	"github.com/voocel/ainovel-cli/internal/host"
)

type expansionPreviewDTO struct {
	PreviewID           string                             `json:"preview_id"`
	BaseRevision        int                                `json:"base_revision"`
	Mode                domain.RevisionMode                `json:"mode"`
	Form                domain.ExpansionForm               `json:"form"`
	Reason              string                             `json:"reason"`
	Location            domain.ExpansionLocationKind       `json:"location"`
	ChapterCount        int                                `json:"chapter_count"`
	ChapterMinWords     int                                `json:"chapter_min_words"`
	ChapterMaxWords     int                                `json:"chapter_max_words"`
	TotalMinWords       int                                `json:"total_min_words"`
	TotalMaxWords       int                                `json:"total_max_words"`
	NewArc              bool                               `json:"new_arc"`
	NewVolume           bool                               `json:"new_volume"`
	OldSummary          string                             `json:"old_summary"`
	NewSummary          string                             `json:"new_summary"`
	Assessment          domain.ExpansionDramaticAssessment `json:"assessment"`
	Impacts             []expansionImpactDTO               `json:"impacts"`
	AuditChain          []string                           `json:"audit_chain"`
	ModeConstraints     []string                           `json:"mode_constraints"`
	ExpiresAt           string                             `json:"expires_at"`
	Obsolete            bool                               `json:"obsolete"`
	Cancelled           bool                               `json:"cancelled"`
	ConfirmedRevisionID string                             `json:"confirmed_revision_id,omitempty"`
	DisplayMappings     []expansionDisplayMappingDTO       `json:"display_mappings,omitempty"`
}

type expansionDisplayMappingDTO struct {
	TargetDisplay string `json:"target_display"`
	SourceDisplay string `json:"source_display,omitempty"`
	AdditionLabel string `json:"addition_label,omitempty"`
}

type expansionImpactDTO struct {
	Change   string                      `json:"change"`
	Level    domain.StructureImpactLevel `json:"level"`
	Cause    domain.StructureImpactCause `json:"cause"`
	Evidence []string                    `json:"evidence"`
}

var expansionInternalIDPattern = regexp.MustCompile(`(?i)\b(?:ch|arc|vol|rev|exp)_[0-9a-f]{16,64}\b`)

func publicExpansionText(value string) string {
	return expansionInternalIDPattern.ReplaceAllString(strings.TrimSpace(value), "对应稿件节点")
}

func publicExpansionPreview(preview *domain.ExpansionPreview) expansionPreviewDTO {
	recommendation := preview.Recommendation
	assessment := recommendation.Assessment
	assessment.Goal = publicExpansionText(assessment.Goal)
	assessment.Conflict = publicExpansionText(assessment.Conflict)
	assessment.Choice = publicExpansionText(assessment.Choice)
	assessment.Cost = publicExpansionText(assessment.Cost)
	assessment.Result = publicExpansionText(assessment.Result)
	assessment.CharacterStageChange = publicExpansionText(assessment.CharacterStageChange)
	assessment.IndependentClimax = publicExpansionText(assessment.IndependentClimax)
	assessment.IrreversibleExit = publicExpansionText(assessment.IrreversibleExit)
	assessment.CurrentFit = publicExpansionText(assessment.CurrentFit)
	assessment.VolumePacingEffect = publicExpansionText(assessment.VolumePacingEffect)
	assessment.AdaptationEffect = publicExpansionText(assessment.AdaptationEffect)
	result := expansionPreviewDTO{
		PreviewID: preview.ID, BaseRevision: preview.BaseRevision, Mode: preview.Mode, Form: recommendation.Form, Reason: publicExpansionText(recommendation.Reason),
		Location: recommendation.Location, ChapterCount: recommendation.ChapterCount,
		ChapterMinWords: recommendation.ChapterMinWords, ChapterMaxWords: recommendation.ChapterMaxWords,
		TotalMinWords: recommendation.TotalMinWords, TotalMaxWords: recommendation.TotalMaxWords,
		NewArc: recommendation.NewArc, NewVolume: recommendation.NewVolume,
		OldSummary: publicExpansionText(recommendation.OldSummary), NewSummary: publicExpansionText(recommendation.NewSummary), Assessment: assessment,
		ExpiresAt: preview.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"), Obsolete: preview.Obsolete, Cancelled: preview.Cancelled,
		ConfirmedRevisionID: preview.ConfirmedRevisionID,
	}
	for _, item := range recommendation.AuditChain {
		result.AuditChain = append(result.AuditChain, publicExpansionText(item))
	}
	for _, item := range recommendation.ModeConstraints {
		result.ModeConstraints = append(result.ModeConstraints, publicExpansionText(item))
	}
	for _, impact := range recommendation.Impacts {
		item := expansionImpactDTO{Change: publicExpansionText(impact.Change), Level: impact.Level, Cause: impact.Cause}
		for _, evidence := range impact.DependencyEvidence {
			item.Evidence = append(item.Evidence, publicExpansionText(evidence))
		}
		result.Impacts = append(result.Impacts, item)
	}
	contractByID := make(map[string]domain.AdaptationChapterPlan)
	if recommendation.AdaptationCandidate != nil {
		for _, chapter := range recommendation.AdaptationCandidate.Chapters {
			contractByID[chapter.ID] = chapter
		}
	}
	for _, chapter := range domain.FlattenOutline(preview.Candidate) {
		mapping := expansionDisplayMappingDTO{TargetDisplay: fmt.Sprintf("目标第 %d 章", chapter.Chapter)}
		if contract, ok := contractByID[chapter.ID]; ok {
			mapping.SourceDisplay = publicAdaptationSourceRange(contract)
			if contract.IsAdded {
				mapping.AdditionLabel = "新增剧情"
			}
		}
		result.DisplayMappings = append(result.DisplayMappings, mapping)
	}
	return result
}

func publicAdaptationSourceRange(chapter domain.AdaptationChapterPlan) string {
	if chapter.IsAdded {
		return "无原著章映射"
	}
	from, to := chapter.SourceRange.From, chapter.SourceRange.To
	if from <= 0 && len(chapter.SourceChapters) > 0 {
		from, to = chapter.SourceChapters[0], chapter.SourceChapters[len(chapter.SourceChapters)-1]
	}
	if from <= 0 {
		return "原著覆盖待审核"
	}
	if to <= from {
		return fmt.Sprintf("原著第 %d 章", from)
	}
	return fmt.Sprintf("原著第 %d–%d 章", from, to)
}

func (s *Server) handleManuscriptExpansionRoute(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, projectID, action string) {
	session, _, err := s.sessions.Open(projectID)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	planner := session.ExpansionPlanner()
	if planner == nil {
		writeManuscriptRequestError(w, http.StatusServiceUnavailable, "expansion planner is unavailable")
		return
	}
	if auditorErr := session.ExpansionAuditorError(); auditorErr != nil {
		writeExpansionError(w, fmt.Errorf("%w: required independent component failed", expansionauditorclient.ErrUnavailable))
		return
	}
	rest := strings.TrimPrefix(action, "manuscript/expansion/")
	switch {
	case rest == "revision/command":
		if r.Method != http.MethodPost {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			Action           string `json:"action"`
			Message          string `json:"message,omitempty"`
			ExpectedRevision int    `json:"expected_revision"`
			IdempotencyKey   string `json:"idempotency_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeManuscriptRequestError(w, http.StatusBadRequest, "invalid expansion revision command")
			return
		}
		revision, commandErr := planner.RevisionCommand(request.Action, request.Message, request.ExpectedRevision, request.IdempotencyKey)
		if commandErr != nil {
			writeExpansionError(w, commandErr)
			return
		}
		session.appendManuscriptMutation("generation", "expansion")
		publicRevision := publicExpansionRevision(revision)
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": publicRevision})
	case rest == "revision/auditor/process":
		if r.Method != http.MethodPost {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		active, loadErr := planner.ActiveRevision()
		if loadErr != nil {
			writeExpansionError(w, loadErr)
			return
		}
		client, clientErr := expansionauditorclient.New()
		if clientErr != nil {
			writeExpansionError(w, clientErr)
			return
		}
		artifact, auditErr := client.ReviewRevision(r.Context(), manifest.OutputDir, active.ID)
		if auditErr != nil {
			writeExpansionError(w, auditErr)
			return
		}
		processed, auditErr := planner.AcceptAuditArtifact(active.ID, artifact)
		if auditErr != nil {
			writeExpansionError(w, auditErr)
			return
		}
		publicRevision := publicExpansionRevision(processed)
		if value, ok := publicRevision.(map[string]any); ok {
			value["findings"] = append([]string(nil), artifact.Findings...)
			value["audit_decision"] = artifact.Decision
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": publicRevision})
	case rest == "revision":
		if r.Method != http.MethodGet {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		active, loadErr := planner.ActiveRevision()
		if loadErr != nil {
			writeExpansionError(w, loadErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "revision": publicExpansionRevision(active)})
	case rest == "plan":
		if r.Method != http.MethodPost {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request domain.ExpansionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeManuscriptRequestError(w, http.StatusBadRequest, "invalid expansion plan request")
			return
		}
		client, clientErr := expansionauditorclient.New()
		if clientErr != nil {
			writeExpansionError(w, clientErr)
			return
		}
		preview, err := planWithExpansionAuditorProcess(r.Context(), planner, client, manifest.OutputDir, request)
		if err != nil {
			writeExpansionError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"project": manifest, "preview": publicExpansionPreview(preview), "awaiting_human_confirmation": true})
	case rest == "adjust":
		if r.Method != http.MethodPost {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			PreviewID        string                     `json:"preview_id"`
			ExpectedRevision int                        `json:"expected_revision"`
			Adjustment       domain.ExpansionAdjustment `json:"adjustment"`
			Sentence         string                     `json:"sentence,omitempty"`
			IdempotencyKey   string                     `json:"idempotency_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeManuscriptRequestError(w, http.StatusBadRequest, "invalid expansion adjustment request")
			return
		}
		client, clientErr := expansionauditorclient.New()
		if clientErr != nil {
			writeExpansionError(w, clientErr)
			return
		}
		preview, err := adjustWithExpansionAuditorProcess(r.Context(), planner, client, manifest.OutputDir, request.PreviewID, request.ExpectedRevision, request.Adjustment, request.Sentence, request.IdempotencyKey)
		if err != nil {
			writeExpansionError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"project": manifest, "preview": publicExpansionPreview(preview), "awaiting_human_confirmation": true})
	case rest == "confirm":
		if r.Method != http.MethodPost {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var request struct {
			PreviewID        string `json:"preview_id"`
			ExpectedRevision int    `json:"expected_revision"`
			IdempotencyKey   string `json:"idempotency_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeManuscriptRequestError(w, http.StatusBadRequest, "invalid expansion confirmation request")
			return
		}
		confirmation, err := planner.Confirm(r.Context(), request.PreviewID, request.ExpectedRevision, request.IdempotencyKey)
		if err != nil {
			writeExpansionError(w, err)
			return
		}
		session.appendManuscriptMutation("structure_publish", "expansion")
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "confirmation": confirmation, "human_confirmed": true})
	case strings.HasSuffix(rest, "/cancel"):
		if r.Method != http.MethodPost {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		previewID := strings.TrimSuffix(rest, "/cancel")
		var request struct {
			ExpectedRevision int    `json:"expected_revision"`
			IdempotencyKey   string `json:"idempotency_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeManuscriptRequestError(w, http.StatusBadRequest, "invalid expansion cancel request")
			return
		}
		preview, err := planner.Cancel(previewID, request.ExpectedRevision, request.IdempotencyKey)
		if err != nil {
			writeExpansionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "preview": publicExpansionPreview(preview)})
	default:
		if r.Method != http.MethodGet {
			writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		preview, err := planner.Get(rest)
		if err != nil {
			writeExpansionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "preview": publicExpansionPreview(preview)})
	}
}

func planWithExpansionAuditorProcess(ctx context.Context, planner *host.ExpansionPlanner, client *expansionauditorclient.Client, projectRoot string, request domain.ExpansionRequest) (*domain.ExpansionPreview, error) {
	for attempts := 0; attempts < 512; attempts++ {
		preview, err := planner.Plan(ctx, request)
		var pending *host.ExpansionDependencyAuditPendingError
		if !errors.As(err, &pending) {
			return preview, err
		}
		review, err := client.ReviewDependency(ctx, projectRoot, pending.TaskID)
		if err != nil {
			if errors.Is(err, expansionauditorclient.ErrTaskNotFound) {
				continue
			}
			return nil, err
		}
		if err := planner.AcceptDependencyReview(pending.TaskID, review); err != nil {
			if errors.Is(err, host.ErrExpansionDependencyTaskNotFound) {
				continue
			}
			return nil, err
		}
	}
	return nil, &domain.ManuscriptRevisionError{Class: "batch_failed", Err: fmt.Errorf("expansion dependency audit graph exceeded bounded task count")}
}

func adjustWithExpansionAuditorProcess(ctx context.Context, planner *host.ExpansionPlanner, client *expansionauditorclient.Client, projectRoot, previewID string, expectedRevision int, adjustment domain.ExpansionAdjustment, sentence, idempotencyKey string) (*domain.ExpansionPreview, error) {
	for attempts := 0; attempts < 512; attempts++ {
		preview, err := planner.Adjust(ctx, previewID, expectedRevision, adjustment, sentence, idempotencyKey)
		var pending *host.ExpansionDependencyAuditPendingError
		if !errors.As(err, &pending) {
			return preview, err
		}
		review, reviewErr := client.ReviewDependency(ctx, projectRoot, pending.TaskID)
		if reviewErr != nil {
			if errors.Is(reviewErr, expansionauditorclient.ErrTaskNotFound) {
				continue
			}
			return nil, reviewErr
		}
		if acceptErr := planner.AcceptDependencyReview(pending.TaskID, review); acceptErr != nil {
			if errors.Is(acceptErr, host.ErrExpansionDependencyTaskNotFound) {
				continue
			}
			return nil, acceptErr
		}
	}
	return nil, &domain.ManuscriptRevisionError{Class: "batch_failed", Err: fmt.Errorf("expansion adjustment dependency audit graph exceeded bounded task count")}
}

func publicExpansionRevision(session *domain.RevisionSession) any {
	if session == nil {
		return nil
	}
	findings := make([]string, 0)
	if strings.TrimSpace(session.LastError) != "" {
		findings = append(findings, publicExpansionText(session.LastError))
	}
	for _, audit := range session.Audits {
		if !audit.Passed && strings.TrimSpace(audit.Report) != "" {
			findings = append(findings, publicExpansionText(audit.Report))
		}
	}
	approvalStage := session.CurrentApprovalStageID()
	if approvalStage == "" && len(session.Approvals) < len(session.ApprovalStages) &&
		(session.Stage == domain.RevisionStageCandidateGenerating || session.Stage == domain.RevisionStageCandidateAudit) {
		approvalStage = session.ApprovalStages[len(session.Approvals)].ID
	}
	return map[string]any{
		"revision_id": session.ID, "revision": session.Revision, "stage": session.Stage,
		"approval_stage": approvalStage, "findings": findings,
		"updated_at": session.UpdatedAt, "terminal": session.Stage.Terminal(),
	}
}

func writeExpansionError(w http.ResponseWriter, err error) {
	writeManuscriptOperationError(w, err)
}
