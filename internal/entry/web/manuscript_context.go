package web

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const manuscriptDiscussionBudgetBytes = 60 * 1024
const manuscriptDiscussionBudgetUnits = 10000

var manuscriptChapterStableIDPattern = regexp.MustCompile(`^ch_[0-9a-f]{32}$`)
var manuscriptRevisionStableIDPattern = regexp.MustCompile(`^(?:msr|rev)_[0-9a-f]{32}$`)

type manuscriptDiscussRequest struct {
	StableID         string `json:"stable_id"`
	ArtifactKind     string `json:"artifact_kind"`
	View             string `json:"view"`
	VersionID        string `json:"version_id,omitempty"`
	SelectionStart   int    `json:"selection_start,omitempty"`
	SelectionEnd     int    `json:"selection_end,omitempty"`
	ContentSignature string `json:"content_signature"`
	Intent           string `json:"intent"`
}

func (s *Server) handleManuscriptDiscuss(w http.ResponseWriter, r *http.Request, _ ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService) {
	if r.Method != http.MethodPost {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request manuscriptDiscussRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, "invalid manuscript discussion request")
		return
	}
	request.View = defaultString(strings.TrimSpace(request.View), "current")
	request.ArtifactKind = defaultString(strings.TrimSpace(request.ArtifactKind), "prose")
	request.Intent = strings.TrimSpace(request.Intent)
	if strings.TrimSpace(request.StableID) == "" || strings.TrimSpace(request.ContentSignature) == "" || request.Intent == "" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "stable_id, content_signature, and intent are required")
		return
	}
	if !manuscriptChapterStableIDPattern.MatchString(strings.TrimSpace(request.StableID)) {
		writeManuscriptRequestError(w, http.StatusBadRequest, "stable_id must be a chapter stable ID")
		return
	}
	if request.ArtifactKind != "prose" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "artifact_kind must be prose")
		return
	}
	if request.View != "current" && request.View != "candidate" && request.View != "history" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "view must be current, candidate, or history")
		return
	}
	if request.View == "current" && request.VersionID != "" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "current view must not carry version_id")
		return
	}
	if request.View != "current" && strings.TrimSpace(request.VersionID) == "" {
		writeManuscriptRequestError(w, http.StatusBadRequest, "candidate and history views require version_id")
		return
	}
	baseline, prose, err := service.CurrentChapter(request.StableID)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	var provenance *storepkg.ManuscriptContentProvenance
	if request.View == "current" {
		provenance, err = st.ManuscriptRevisions.ContentProvenance(request.StableID, baseline.CurrentProseSHA256)
		if err != nil {
			if baseline.Mode == domain.RevisionModeAdaptation {
				writeManuscriptError(w, fmt.Errorf("published manuscript provenance is unavailable: %w", err))
				return
			}
			provenance = &storepkg.ManuscriptContentProvenance{ChapterID: baseline.ChapterID, ContentSHA256: baseline.CurrentProseSHA256, ApprovedOutlineSHA256: baseline.ApprovedOutlineSHA256, Mode: domain.RevisionModeNormal}
		}
	}
	if request.View == "candidate" {
		active, loadErr := st.ManuscriptRevisions.Active()
		if loadErr != nil {
			writeManuscriptError(w, loadErr)
			return
		}
		if active == nil || active.RevisionID != request.VersionID {
			writeManuscriptError(w, storepkg.ErrManuscriptRevisionNotFound)
			return
		}
		found := false
		for _, candidate := range active.Candidates {
			if candidate.ChapterID == request.StableID {
				payload, readErr := st.ManuscriptRevisions.Content().Read(candidate.Prose)
				if readErr != nil {
					writeManuscriptError(w, readErr)
					return
				}
				prose, baseline.CurrentProseSHA256, found = string(payload), candidate.Prose.SHA256, true
				provenance = manuscriptRuntimeContentProvenance(*active, candidate)
				break
			}
		}
		if !found {
			writeManuscriptError(w, storepkg.ErrManuscriptRevisionNotFound)
			return
		}
	}
	if request.View == "history" {
		runtime, loadErr := st.ManuscriptRevisions.Load(request.VersionID)
		if loadErr != nil {
			writeManuscriptError(w, loadErr)
			return
		}
		found := false
		for _, candidate := range runtime.Candidates {
			if candidate.ChapterID != request.StableID {
				continue
			}
			payload, readErr := st.ManuscriptRevisions.Content().Read(candidate.Prose)
			if readErr != nil {
				writeManuscriptError(w, readErr)
				return
			}
			prose, baseline.CurrentProseSHA256, found = string(payload), candidate.Prose.SHA256, true
			provenance = manuscriptRuntimeContentProvenance(*runtime, candidate)
			break
		}
		if !found {
			writeManuscriptError(w, storepkg.ErrManuscriptRevisionNotFound)
			return
		}
	}
	if provenance == nil || provenance.Mode != baseline.Mode || provenance.ApprovedOutlineSHA256 != baseline.ApprovedOutlineSHA256 {
		writeManuscriptError(w, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("content provenance drift: manuscript mode or outline changed")})
		return
	}
	baseline.Mode = provenance.Mode
	baseline.AdaptationPlanSHA256 = provenance.AdaptationPlanSHA256
	baseline.SourceManifestSHA256 = provenance.SourceManifestSHA256
	if request.ContentSignature != baseline.CurrentProseSHA256 {
		writeManuscriptError(w, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("discussion source changed")})
		return
	}
	selected, err := manuscriptSelection(prose, request.SelectionStart, request.SelectionEnd)
	if err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, err.Error())
		return
	}
	chips := []string{"当前章", "章节提纲", "所属分卷", "修订摘要"}
	context := map[string]any{"mode": baseline.Mode, "artifact_kind": request.ArtifactKind, "display_chapter": baseline.DisplayChapter, "view": request.View, "selected_prose": selected, "intent": request.Intent, "chips": chips}
	budgetUnits := len([]rune(selected))
	if baseline.Mode == domain.RevisionModeAdaptation {
		context["source_display"] = adaptationSourceDisplay(st, baseline.ChapterID)
		evidence, evidenceUnits, evidenceErr := adaptationDiscussionEvidence(st, baseline.ChapterID, manuscriptDiscussionBudgetUnits-budgetUnits, baseline.AdaptationPlanSHA256, baseline.SourceManifestSHA256)
		if evidenceErr != nil {
			writeManuscriptError(w, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: evidenceErr})
			return
		}
		context["source_evidence"] = evidence
		budgetUnits += evidenceUnits
	}
	payload := mustJSON(context)
	if len(payload) > manuscriptDiscussionBudgetBytes {
		writeManuscriptRequestError(w, http.StatusRequestEntityTooLarge, "discussion context exceeds 60 KiB budget")
		return
	}
	discussionMessage := renderManuscriptDiscussionMessage(request.Intent, selected, context["source_display"], context["source_evidence"])
	writeJSON(w, http.StatusOK, map[string]any{
		"chips": chips,
		"discussion": map[string]any{
			"accepted":                  true,
			"target":                    "cocreate",
			"message":                   discussionMessage,
			"context_is_server_cropped": true,
		},
		"budget_bytes": len(payload),
		"budget_units": budgetUnits,
	})
}

func manuscriptRuntimeContentProvenance(runtime domain.ManuscriptRevisionRuntime, candidate domain.ManuscriptCandidate) *storepkg.ManuscriptContentProvenance {
	return &storepkg.ManuscriptContentProvenance{
		ChapterID: candidate.ChapterID, ContentSHA256: candidate.Prose.SHA256,
		ApprovedOutlineSHA256: candidate.OutlineSignature, Mode: runtime.Mode,
		AdaptationPlanSHA256: runtime.Baseline.AdaptationPlanSHA256,
		SourceManifestSHA256: runtime.Baseline.SourceManifestSHA256,
	}
}

func renderManuscriptDiscussionMessage(intent, selected string, sourceDisplay, sourceEvidence any) string {
	var builder strings.Builder
	builder.WriteString("讨论目标：")
	builder.WriteString(strings.TrimSpace(intent))
	builder.WriteString("\n\n已选择的稿件内容：\n")
	builder.WriteString(strings.TrimSpace(selected))
	if display, ok := sourceDisplay.(string); ok && strings.TrimSpace(display) != "" {
		builder.WriteString("\n\n参考原著范围：")
		builder.WriteString(strings.TrimSpace(display))
	}
	if evidence, ok := sourceEvidence.([]map[string]any); ok {
		for _, item := range evidence {
			label, _ := item["source_label"].(string)
			title, _ := item["title"].(string)
			prose, _ := item["prose"].(string)
			note, _ := item["note"].(string)
			builder.WriteString("\n\n参考内容")
			if strings.TrimSpace(label) != "" {
				builder.WriteString("（")
				builder.WriteString(strings.TrimSpace(label))
				builder.WriteString("）")
			}
			if strings.TrimSpace(title) != "" {
				builder.WriteString("：")
				builder.WriteString(strings.TrimSpace(title))
			}
			builder.WriteString("\n")
			if strings.TrimSpace(prose) != "" {
				builder.WriteString(strings.TrimSpace(prose))
			} else {
				builder.WriteString(strings.TrimSpace(note))
			}
		}
	}
	return builder.String()
}

func adaptationDiscussionEvidence(st *storepkg.Store, stableID string, remainingUnits int, expectedPlanSignature, expectedManifestSignature string) ([]map[string]any, int, error) {
	if remainingUnits <= 0 {
		return nil, 0, fmt.Errorf("discussion context exceeds %d unit budget", manuscriptDiscussionBudgetUnits)
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return nil, 0, fmt.Errorf("adaptation task plan is unavailable")
	}
	if plan.Status != domain.AdaptationPlanStatusConfirmed {
		return nil, 0, fmt.Errorf("adaptation task plan is not confirmed")
	}
	planPayload, _ := json.Marshal(plan)
	if actual := domain.ContentSignature(planPayload); expectedPlanSignature == "" || actual != expectedPlanSignature {
		return nil, 0, fmt.Errorf("adaptation plan binding drift")
	}
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return nil, 0, fmt.Errorf("adaptation source manifest is unavailable")
	}
	manifestSignature := domain.AdaptationSourceManifestContractSignature(*manifest)
	if strings.TrimSpace(manifestSignature) == "" {
		return nil, 0, fmt.Errorf("adaptation source manifest binding is invalid")
	}
	manifestPayload, _ := json.Marshal(manifest)
	if actual := domain.ContentSignature(manifestPayload); expectedManifestSignature == "" || actual != expectedManifestSignature {
		return nil, 0, fmt.Errorf("adaptation source manifest binding drift")
	}
	var task *domain.AdaptationChapterPlan
	for _, chapter := range plan.Chapters {
		if chapter.ID == stableID {
			matched := chapter
			task = &matched
			break
		}
	}
	if task == nil {
		return nil, 0, fmt.Errorf("adaptation task stable ID is not in the confirmed plan")
	}
	if task.IsAdded {
		if strings.TrimSpace(task.CoverageNote) == "" || len(task.SourceChapters) > 0 || len(task.SourceSegments) > 0 || task.SourceRange.From > 0 || task.SourceRange.To > 0 {
			return nil, 0, fmt.Errorf("added adaptation task has an invalid coverage contract")
		}
		note := "新增目标章节，无原著范围：" + strings.TrimSpace(task.CoverageNote)
		return []map[string]any{{"scope": "新增目标章节，无原著范围", "note": note}}, len([]rune(note)), nil
	}
	sourceChapters, coverageErr := adaptationDiscussionSourceChapters(*task)
	if coverageErr != nil {
		return nil, 0, coverageErr
	}
	manifestByChapter := make(map[int]domain.AdaptationSource, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		manifestByChapter[source.Chapter] = source
	}
	if err := validateAdaptationDiscussionSegments(plan.Chapters, sourceChapters, manifestByChapter); err != nil {
		return nil, 0, err
	}
	result := make([]map[string]any, 0, len(sourceChapters))
	usedUnits := 0
	for _, chapter := range sourceChapters {
		if remainingUnits <= 0 {
			break
		}
		prose, metadata, loadErr := st.Adaptation.LoadSourceChapter(chapter)
		if loadErr != nil {
			return nil, 0, loadErr
		}
		if metadata == nil || strings.TrimSpace(prose) == "" {
			return nil, 0, fmt.Errorf("adaptation source chapter %d is unavailable", chapter)
		}
		expected, exists := manifestByChapter[chapter]
		if !exists || expected.SHA256 != metadata.SHA256 || expected.Path != metadata.Path || expected.Title != metadata.Title || expected.Runes != metadata.Runes {
			return nil, 0, fmt.Errorf("adaptation source manifest binding drift for chapter %d", chapter)
		}
		if actual := storepkg.TextSHA256(prose); actual != metadata.SHA256 {
			return nil, 0, fmt.Errorf("adaptation source signature drift for chapter %d", chapter)
		}
		runes := []rune(prose)
		if len(runes) > remainingUnits {
			runes = runes[:remainingUnits]
		}
		result = append(result, map[string]any{"source_label": fmt.Sprintf("原著第 %d 章", chapter), "title": metadata.Title, "prose": string(runes)})
		remainingUnits -= len(runes)
		usedUnits += len(runes)
	}
	return result, usedUnits, nil
}

func adaptationDiscussionSourceChapters(task domain.AdaptationChapterPlan) ([]int, error) {
	chapters := make(map[int]struct{})
	for _, chapter := range task.SourceChapters {
		if chapter <= 0 {
			return nil, fmt.Errorf("adaptation task has invalid source coverage")
		}
		chapters[chapter] = struct{}{}
	}
	if task.SourceRange.From > 0 || task.SourceRange.To > 0 {
		if task.SourceRange.From <= 0 || task.SourceRange.To < task.SourceRange.From {
			return nil, fmt.Errorf("adaptation task has invalid source range")
		}
		for chapter := task.SourceRange.From; chapter <= task.SourceRange.To; chapter++ {
			if len(task.SourceChapters) > 0 {
				if _, exists := chapters[chapter]; !exists {
					return nil, fmt.Errorf("adaptation source chapters and range coverage drift")
				}
			}
			chapters[chapter] = struct{}{}
		}
		if len(task.SourceChapters) > 0 && len(chapters) != task.SourceRange.To-task.SourceRange.From+1 {
			return nil, fmt.Errorf("adaptation source chapters and range coverage drift")
		}
	}
	for _, segment := range task.SourceSegments {
		if _, exists := chapters[segment.SourceChapter]; !exists {
			return nil, fmt.Errorf("adaptation source segment coverage drift")
		}
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("non-added adaptation task has zero source coverage")
	}
	result := make([]int, 0, len(chapters))
	for chapter := range chapters {
		result = append(result, chapter)
	}
	slices.Sort(result)
	return result, nil
}

func validateAdaptationDiscussionSegments(tasks []domain.AdaptationChapterPlan, sources []int, manifest map[int]domain.AdaptationSource) error {
	segmentsBySource := make(map[int][]domain.AdaptationSourceSegment)
	for _, task := range tasks {
		for _, segment := range task.SourceSegments {
			segmentsBySource[segment.SourceChapter] = append(segmentsBySource[segment.SourceChapter], segment)
		}
	}
	for _, source := range sources {
		segments := segmentsBySource[source]
		if len(segments) == 0 {
			continue
		}
		metadata, exists := manifest[source]
		if !exists || metadata.Runes <= 0 {
			return fmt.Errorf("adaptation source segment manifest coverage drift")
		}
		slices.SortFunc(segments, func(left, right domain.AdaptationSourceSegment) int {
			return cmp.Compare(left.Sequence, right.Sequence)
		})
		if err := domain.ValidateAdaptationSourceSegments(source, metadata.Runes, segments); err != nil {
			return fmt.Errorf("adaptation source segment coverage drift: %w", err)
		}
	}
	return nil
}

func manuscriptSelection(prose string, start, end int) (string, error) {
	runes := []rune(prose)
	if start == 0 && end == 0 {
		if len(runes) > 6000 {
			runes = runes[:6000]
		}
		return string(runes), nil
	}
	if start < 0 || end <= start || end > len(runes) {
		return "", fmt.Errorf("selection range is outside current content")
	}
	if end-start > 6000 {
		return "", fmt.Errorf("selection exceeds 6000 characters")
	}
	return string(runes[start:end]), nil
}

func adaptationSourceDisplay(st *storepkg.Store, stableID string) string {
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return "当前改编任务原著范围"
	}
	for _, chapter := range plan.Chapters {
		if chapter.ID != stableID {
			continue
		}
		if len(chapter.SourceChapters) == 1 {
			return fmt.Sprintf("原著第 %d 章", chapter.SourceChapters[0])
		}
		if len(chapter.SourceChapters) > 1 {
			return fmt.Sprintf("原著第 %d–%d 章", chapter.SourceChapters[0], chapter.SourceChapters[len(chapter.SourceChapters)-1])
		}
		return "新增目标章节"
	}
	return "当前改编任务原著范围"
}

type manuscriptRestoreRequest struct {
	RevisionID               string `json:"revision_id"`
	ChapterID                string `json:"chapter_id"`
	ExpectedContentSignature string `json:"expected_content_signature"`
	IdempotencyKey           string `json:"idempotency_key"`
	PreviewSignature         string `json:"preview_signature,omitempty"`
}

func (s *Server) handleManuscriptRestorePreview(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService) {
	if r.Method != http.MethodPost {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request manuscriptRestoreRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, "invalid restore preview request")
		return
	}
	if err := validateRestoreRequest(request, false); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, err.Error())
		return
	}
	runtime, candidate, err := loadRestoreCandidate(st, request)
	if err != nil {
		if errors.Is(err, storepkg.ErrManuscriptRevisionNotFound) {
			writeManuscriptEnvelope(w, http.StatusGone, "version_gone", "historical version is no longer available", nil)
			return
		}
		writeManuscriptError(w, err)
		return
	}
	baseline, _, err := service.CurrentChapter(request.ChapterID)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if candidate.OutlineSignature != baseline.ApprovedOutlineSHA256 || candidate.ModeSignature != manuscriptModeSignatureForPreview(baseline) {
		writeManuscriptEnvelope(w, http.StatusConflict, "preview_stale", "historical version no longer matches the current outline or mode", map[string]any{"action": "refresh_preview"})
		return
	}
	if active, activeErr := st.ManuscriptRevisions.Active(); activeErr != nil {
		writeManuscriptError(w, activeErr)
		return
	} else if active != nil {
		writeManuscriptEnvelope(w, http.StatusConflict, "active_revision", "finish or cancel the active revision before restoring", map[string]any{"active_revision_id": active.RevisionID, "stage": active.Stage})
		return
	}
	previewSignature := manuscriptRestorePreviewSignature(runtime.RevisionID, request.ChapterID, baseline, *candidate)
	writeJSON(w, http.StatusOK, map[string]any{"project": manifest, "preview": map[string]any{"source_revision_id": runtime.RevisionID, "chapter_id": request.ChapterID, "historical_signature": candidate.Prose.SHA256, "current_signature": baseline.CurrentProseSHA256, "mode": baseline.Mode, "impact": "creates a new audit_pending revision; current formal prose is not overwritten", "requires_confirmation": true, "preview_signature": previewSignature}})
}

func validateRestoreRequest(request manuscriptRestoreRequest, requireIdempotency bool) error {
	if strings.TrimSpace(request.RevisionID) == "" || strings.TrimSpace(request.ChapterID) == "" || strings.TrimSpace(request.ExpectedContentSignature) == "" {
		return fmt.Errorf("revision_id, chapter_id, and expected_content_signature are required")
	}
	if !manuscriptRevisionStableIDPattern.MatchString(strings.TrimSpace(request.RevisionID)) || !manuscriptChapterStableIDPattern.MatchString(strings.TrimSpace(request.ChapterID)) {
		return fmt.Errorf("revision_id and chapter_id must be stable IDs, not paths")
	}
	if requireIdempotency && strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("idempotency_key is required")
	}
	if requireIdempotency && strings.TrimSpace(request.PreviewSignature) == "" {
		return fmt.Errorf("preview_signature is required")
	}
	return nil
}

func loadRestoreCandidate(st *storepkg.Store, request manuscriptRestoreRequest) (*domain.ManuscriptRevisionRuntime, *domain.ManuscriptCandidate, error) {
	runtime, err := st.ManuscriptRevisions.Load(strings.TrimSpace(request.RevisionID))
	if err != nil {
		return nil, nil, err
	}
	for i := range runtime.Candidates {
		candidate := &runtime.Candidates[i]
		if candidate.ChapterID == strings.TrimSpace(request.ChapterID) && candidate.Prose.SHA256 == strings.TrimSpace(request.ExpectedContentSignature) {
			return runtime, candidate, nil
		}
	}
	return nil, nil, storepkg.ErrManuscriptRevisionNotFound
}

func manuscriptModeSignatureForPreview(baseline domain.ManuscriptBaseline) string {
	payload, _ := json.Marshal(struct {
		Mode   domain.RevisionMode
		Plan   string
		Source string
	}{baseline.Mode, baseline.AdaptationPlanSHA256, baseline.SourceManifestSHA256})
	return domain.ContentSignature(payload)
}

func manuscriptRestorePreviewSignature(sourceRevisionID, chapterID string, baseline domain.ManuscriptBaseline, candidate domain.ManuscriptCandidate) string {
	return domain.ContentSignature(mustJSON(struct {
		SourceRevision string              `json:"source_revision_id"`
		ChapterID      string              `json:"chapter_id"`
		Current        string              `json:"current_prose_signature"`
		Historical     string              `json:"historical_prose_signature"`
		Outline        string              `json:"outline_signature"`
		Structure      string              `json:"structure_signature"`
		Mode           domain.RevisionMode `json:"mode"`
		ModeSignature  string              `json:"mode_signature"`
	}{sourceRevisionID, chapterID, baseline.CurrentProseSHA256, candidate.Prose.SHA256, baseline.ApprovedOutlineSHA256, baseline.StructureSignature, baseline.Mode, manuscriptModeSignatureForPreview(baseline)}))
}

func (s *Server) handleManuscriptRestore(w http.ResponseWriter, r *http.Request, manifest ProjectManifest, st *storepkg.Store, service *host.ManuscriptRevisionService) {
	if r.Method != http.MethodPost {
		writeManuscriptRequestError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request manuscriptRestoreRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, "invalid restore request")
		return
	}
	if err := validateRestoreRequest(request, true); err != nil {
		writeManuscriptRequestError(w, http.StatusBadRequest, err.Error())
		return
	}
	runtime, candidate, err := loadRestoreCandidate(st, request)
	if err != nil {
		if errors.Is(err, storepkg.ErrManuscriptRevisionNotFound) {
			writeManuscriptEnvelope(w, http.StatusGone, "version_gone", "historical version is no longer available", nil)
			return
		}
		writeManuscriptError(w, err)
		return
	}
	if candidate.Prose.SHA256 != request.ExpectedContentSignature {
		writeManuscriptError(w, &domain.ManuscriptRevisionError{Class: "signature_drift", Err: fmt.Errorf("historical version changed")})
		return
	}
	baseline, _, err := service.CurrentChapter(request.ChapterID)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if candidate.OutlineSignature != baseline.ApprovedOutlineSHA256 || candidate.ModeSignature != manuscriptModeSignatureForPreview(baseline) {
		writeManuscriptEnvelope(w, http.StatusConflict, "preview_stale", "historical version no longer matches the current outline or mode", map[string]any{"action": "refresh_preview"})
		return
	}
	expectedPreview := manuscriptRestorePreviewSignature(runtime.RevisionID, request.ChapterID, baseline, *candidate)
	if request.PreviewSignature != expectedPreview {
		writeManuscriptEnvelope(w, http.StatusConflict, "preview_stale", "restore confirmation does not match the signed preview", map[string]any{"action": "refresh_preview"})
		return
	}
	revision, err := service.RestoreVersion(request.RevisionID, request.ChapterID, request.ExpectedContentSignature, request.IdempotencyKey)
	if err != nil {
		writeManuscriptError(w, err)
		return
	}
	if session, _, openErr := s.sessions.Open(manifest.ID); openErr == nil {
		session.appendManuscriptMutation("generation", request.ChapterID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"project": manifest, "revision": revision, "restored_from": request.RevisionID, "awaiting_audit": true})
}
