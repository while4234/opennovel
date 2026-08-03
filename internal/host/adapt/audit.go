package adapt

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type AuditOptions struct {
	SourceFrom int                     `json:"source_from,omitempty"`
	SourceTo   int                     `json:"source_to,omitempty"`
	TargetFrom int                     `json:"target_from,omitempty"`
	TargetTo   int                     `json:"target_to,omitempty"`
	Trigger    adaptaudit.AuditTrigger `json:"-"`
}

func AuditProject(st *store.Store, options AuditOptions) (*adaptaudit.Report, error) {
	if st == nil {
		return nil, fmt.Errorf("store is required")
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil {
		return nil, fmt.Errorf("load adaptation plan: %w", err)
	}
	if plan == nil {
		return nil, fmt.Errorf("confirmed adaptation plan is required")
	}
	scope, err := ResolveProjectAuditScope(st, options)
	if err != nil {
		return nil, err
	}
	effectiveOptions := AuditOptions{
		SourceFrom: scope.SourceFrom, SourceTo: scope.SourceTo,
		TargetFrom: scope.TargetFrom, TargetTo: scope.TargetTo,
	}
	input, err := buildProjectAuditInput(st, *plan, effectiveOptions)
	if err != nil {
		return nil, err
	}
	report := adaptaudit.Audit(input)
	if reason := auditContractUnavailable(*plan, input); reason != "" {
		report = adaptaudit.AuditEvidenceOnly(input, "audit_contract_unavailable", reason)
	}
	run, err := adaptaudit.NewAuditRun(report, adaptaudit.AuditKindContract, options.Trigger, nil, time.Now())
	if err != nil {
		return nil, fmt.Errorf("create adaptation audit run: %w", err)
	}
	if err := st.Adaptation.SaveAuditRun(run); err != nil {
		return nil, fmt.Errorf("save adaptation audit run: %w", err)
	}
	return &report, nil
}

func ApplyProjectAuditRepair(st *store.Store, request adaptaudit.ConfirmationRequest) (*adaptaudit.RepairApplication, error) {
	if st == nil {
		return nil, fmt.Errorf("store is required")
	}
	var report *adaptaudit.Report
	var err error
	if strings.TrimSpace(request.RunID) != "" {
		run, loadErr := st.Adaptation.LoadAuditRun(request.RunID)
		if loadErr != nil {
			return nil, fmt.Errorf("load adaptation audit run: %w", loadErr)
		}
		if run == nil {
			return nil, fmt.Errorf("adaptation audit run %s not found", request.RunID)
		}
		report = &run.Report
	} else {
		report, err = st.Adaptation.LoadAuditReport()
	}
	if err != nil {
		return nil, fmt.Errorf("load adaptation audit report: %w", err)
	}
	if report == nil {
		return nil, fmt.Errorf("adaptation audit report is required")
	}
	if err := adaptaudit.ValidateConfirmationRequest(*report, request); err != nil {
		return nil, err
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return nil, fmt.Errorf("load confirmed adaptation plan: %w", err)
	}
	currentScope, err := ResolveProjectAuditScope(st, AuditOptions{SourceTo: report.Scope.SourceTo})
	if err != nil || currentScope != report.Scope {
		return nil, fmt.Errorf("adaptation audit is stale; run a new read-only audit before applying repairs")
	}
	currentInput, err := buildProjectAuditInput(st, *plan, AuditOptions{
		SourceFrom: report.Scope.SourceFrom, SourceTo: report.Scope.SourceTo,
		TargetFrom: report.Scope.TargetFrom, TargetTo: report.Scope.TargetTo,
	})
	if err != nil {
		return nil, fmt.Errorf("rebuild adaptation audit input: %w", err)
	}
	if auditContractUnavailable(*plan, currentInput) != "" {
		return nil, fmt.Errorf("adaptation audit is not eligible for automatic repair because its evidence contract is unavailable")
	}
	if currentDigest := adaptaudit.ComputeInputDigest(currentInput); currentDigest != report.InputDigest {
		return nil, fmt.Errorf("adaptation audit is stale; run a new read-only audit before applying repairs")
	}
	backupPath, err := st.Adaptation.Backup("audit-" + report.Digest[:12])
	if err != nil {
		return nil, fmt.Errorf("backup adaptation before repair: %w", err)
	}
	originalPlan := cloneAdaptationPlan(*plan)
	originalProgress, err := st.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress before audit repair: %w", err)
	}
	affected := repairPlanFromAudit(plan, report.Findings)
	if len(affected) == 0 && len(report.Findings) > 0 {
		return nil, fmt.Errorf("audit findings could not be mapped to target chapters")
	}
	if err := st.Adaptation.SavePlan(*plan); err != nil {
		return nil, fmt.Errorf("save repaired adaptation plan: %w", err)
	}
	queued, err := queueCompletedAuditRepairs(st, affected, "adaptation audit "+report.Digest[:12])
	if err != nil {
		rollbackAuditRepair(st, originalPlan, originalProgress, nil)
		return nil, err
	}
	originalChecks := make(map[int]*domain.AdaptationCheck, len(affected))
	for _, chapter := range affected {
		check, loadErr := st.Adaptation.LoadCheck(chapter)
		if loadErr != nil {
			rollbackAuditRepair(st, originalPlan, originalProgress, originalChecks)
			return nil, fmt.Errorf("load adaptation check %d before invalidation: %w", chapter, loadErr)
		}
		originalChecks[chapter] = check
		if deleteErr := st.Adaptation.DeleteCheck(chapter); deleteErr != nil {
			rollbackAuditRepair(st, originalPlan, originalProgress, originalChecks)
			return nil, fmt.Errorf("invalidate adaptation check %d: %w", chapter, deleteErr)
		}
	}
	status := "plan_repaired"
	if len(queued) > 0 {
		status = "queued_for_project_repair"
	}
	application := adaptaudit.RepairApplication{
		RunID:            request.RunID,
		ReportDigest:     report.Digest,
		BackupPath:       backupPath,
		AffectedChapters: affected,
		QueuedChapters:   queued,
		Status:           status,
		AppliedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	if err := st.Adaptation.SaveRepairApplication(application); err != nil {
		rollbackAuditRepair(st, originalPlan, originalProgress, originalChecks)
		return nil, fmt.Errorf("save adaptation repair application: %w", err)
	}
	if request.RunID != "" {
		if err := st.Adaptation.MarkAuditRunApplied(request.RunID, application.AppliedAt); err != nil {
			return nil, fmt.Errorf("record applied audit run: %w", err)
		}
	}
	return &application, nil
}

func rollbackAuditRepair(
	st *store.Store,
	plan domain.AdaptationPlan,
	progress *domain.Progress,
	checks map[int]*domain.AdaptationCheck,
) {
	if st == nil {
		return
	}
	_ = st.Adaptation.SavePlan(plan)
	if progress != nil {
		_ = st.Progress.Save(progress)
	}
	for chapter, check := range checks {
		if check == nil {
			_ = st.Adaptation.DeleteCheck(chapter)
			continue
		}
		_ = st.Adaptation.SaveCheck(*check)
	}
}

func buildProjectAuditInput(st *store.Store, plan domain.AdaptationPlan, options AuditOptions) (adaptaudit.Input, error) {
	mode := adaptaudit.Mode(domain.NormalizeAdaptationGranularity(plan.Granularity))
	input := adaptaudit.Input{Mode: mode, Scope: adaptaudit.Scope{
		SourceFrom: options.SourceFrom, SourceTo: options.SourceTo,
		TargetFrom: options.TargetFrom, TargetTo: options.TargetTo,
	}}
	input.RelationshipStates = cloneStringMap(plan.TargetRelationshipStates)
	for _, lock := range plan.TargetSettingLocks {
		input.SettingLocks = append(input.SettingLocks, adaptaudit.SettingLock{Key: lock.Key, Value: lock.Value})
	}
	reports, err := st.Adaptation.LoadSourceReports()
	if err != nil {
		return input, fmt.Errorf("load source reports: %w", err)
	}
	if len(plan.SourceEvents) == 0 {
		plan.SourceEvents = sourceEventsFromReports(reports)
	}
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return input, fmt.Errorf("load source manifest: %w", err)
	}
	sourceRunes := sourceRunesByChapter(manifest)

	selectedEventIDs := make(map[string]bool)
	for _, chapter := range plan.Chapters {
		if !chapterSelected(chapter.Chapter, options) {
			continue
		}
		for _, eventID := range append(append([]string(nil), chapter.EventIDs...), chapter.AddedEventIDs...) {
			selectedEventIDs[eventID] = true
		}
	}
	highArtifactIDs := make(map[string]string)
	for _, volume := range plan.Volumes {
		if !targetRangeSelected(volume.TargetFrom, volume.TargetTo, options) {
			continue
		}
		id := fmt.Sprintf("high-plan-volume-%d", volume.Index)
		raw, _ := json.Marshal(volume)
		input.Artifacts = append(input.Artifacts, adaptaudit.Artifact{ID: id, Kind: adaptaudit.ArtifactHighPlan, Text: string(raw)})
		for _, eventID := range volume.MainlineEventIDs {
			highArtifactIDs[eventID] = id
			selectedEventIDs[eventID] = true
		}
	}

	for _, event := range append(append([]domain.AdaptationEvent(nil), plan.SourceEvents...), plan.TargetEventLedger...) {
		if event.Origin == domain.AdaptationEventOriginSource {
			if event.SourceChapter <= 0 && !selectedEventIDs[event.ID] {
				continue
			}
			if event.SourceChapter > 0 && !sourceChapterSelected(event.SourceChapter, options) {
				continue
			}
		}
		if event.Origin != domain.AdaptationEventOriginSource && !selectedEventIDs[event.ID] {
			continue
		}
		auditEvent := auditEventFromDomain(event)
		if artifactID := highArtifactIDs[event.ID]; artifactID != "" {
			auditEvent.HighPlanEvidence = []adaptaudit.Evidence{{ArtifactID: artifactID, Quote: event.ID}}
		}
		input.Events = append(input.Events, auditEvent)
	}
	eventSeen := make(map[string]bool)
	for _, event := range input.Events {
		eventSeen[event.ID] = true
	}

	for _, chapter := range plan.Chapters {
		if !chapterSelected(chapter.Chapter, options) {
			continue
		}
		planArtifactID := fmt.Sprintf("target-plan-%04d", chapter.Chapter)
		chapterSegments, segmentContractPresent := auditChapterSegments(chapter, sourceRunes, mode)
		segmentIDs := auditSourceSegmentIDs(chapterSegments, chapter.Chapter, segmentContractPresent)
		planPayload := struct {
			Plan       domain.AdaptationChapterPlan `json:"plan"`
			SegmentIDs []string                     `json:"segment_ids,omitempty"`
		}{Plan: chapter, SegmentIDs: segmentIDs}
		raw, _ := json.Marshal(planPayload)
		input.Artifacts = append(input.Artifacts, adaptaudit.Artifact{ID: planArtifactID, Kind: adaptaudit.ArtifactTargetPlan, Chapter: chapter.Chapter, Text: string(raw)})

		body, _ := st.Drafts.LoadChapterText(chapter.Chapter)
		if strings.TrimSpace(body) == "" {
			body, _, _ = st.Drafts.LoadChapterContent(chapter.Chapter)
		}
		bodyArtifactID := fmt.Sprintf("target-body-%04d", chapter.Chapter)
		input.Artifacts = append(input.Artifacts, adaptaudit.Artifact{ID: bodyArtifactID, Kind: adaptaudit.ArtifactTargetChapter, Chapter: chapter.Chapter, Text: body})
		check, _ := st.Adaptation.LoadCheck(chapter.Chapter)
		evidenceByEvent := currentBodyEvidence(check, body, bodyArtifactID)
		attachSettingClaimEvidence(input.Events, evidenceByEvent)
		chapterEventIDs := make(map[string]bool, len(chapter.EventIDs))

		for _, eventID := range chapter.EventIDs {
			chapterEventIDs[eventID] = true
			if !eventSeen[eventID] {
				input.Events = append(input.Events, adaptaudit.Event{ID: eventID, Origin: adaptaudit.OriginTarget, Class: adaptaudit.ClassOrdinary})
				eventSeen[eventID] = true
			}
			input.Bindings = append(input.Bindings, adaptaudit.Binding{
				EventID:        eventID,
				TargetChapters: []int{chapter.Chapter},
				PlanEvidence:   []adaptaudit.Evidence{{ArtifactID: planArtifactID, Quote: eventID}},
				BodyEvidence:   evidenceByEvent[eventID],
			})
		}
		for _, eventID := range chapter.AddedEventIDs {
			if !eventSeen[eventID] {
				input.Events = append(input.Events, adaptaudit.Event{ID: eventID, Origin: adaptaudit.OriginAdded, Class: adaptaudit.ClassOrdinary})
				eventSeen[eventID] = true
			}
		}
		for _, segment := range chapterSegments {
			if !sourceChapterSelected(segment.SourceChapter, options) {
				continue
			}
			segmentID := auditSourceSegmentID(segment, chapter.Chapter, segmentContractPresent)
			input.SourceSegments = append(input.SourceSegments, adaptaudit.SourceSegment{
				ID: segmentID, Chapter: segment.SourceChapter, Sequence: segment.Sequence, TargetChapter: chapter.Chapter,
				FromRune: segment.RuneShare.Start, ToRune: segment.RuneShare.End,
				LongPart: sourceRunes[segment.SourceChapter] > domain.AdaptationModelChapterMaxRunes,
				Required: true, ContractPresent: segmentContractPresent,
				TotalRunes: sourceRunes[segment.SourceChapter], MaxRunes: domain.AdaptationModelChapterMaxRunes,
				EntryState: map[string]string(segment.EntryState), ExitState: map[string]string(segment.ExitState),
			})
			var segmentBodyEvidence []adaptaudit.Evidence
			for _, eventID := range segment.EventIDs {
				segmentBodyEvidence = append(segmentBodyEvidence, evidenceByEvent[eventID]...)
			}
			input.Bindings = append(input.Bindings, adaptaudit.Binding{
				SourceSegmentIDs: []string{segmentID}, TargetChapters: []int{chapter.Chapter},
				PlanEvidence: []adaptaudit.Evidence{{ArtifactID: planArtifactID, Quote: segmentID}},
				BodyEvidence: segmentBodyEvidence,
			})
		}
		for eventID, evidence := range evidenceByEvent {
			if eventID == "" || chapterEventIDs[eventID] {
				continue
			}
			input.Bindings = append(input.Bindings, adaptaudit.Binding{
				EventID: eventID, TargetChapters: []int{chapter.Chapter}, BodyEvidence: evidence,
			})
		}
	}
	return input, nil
}

func auditSourceSegmentIDs(segments []domain.AdaptationSourceSegment, targetChapter int, contractPresent bool) []string {
	ids := make([]string, 0, len(segments))
	for _, segment := range segments {
		ids = append(ids, auditSourceSegmentID(segment, targetChapter, contractPresent))
	}
	return ids
}

func auditSourceSegmentID(segment domain.AdaptationSourceSegment, targetChapter int, contractPresent bool) string {
	id := sourceSegmentID(segment)
	if !contractPresent {
		id += fmt.Sprintf("-legacy-target-%04d", targetChapter)
	}
	return id
}

func auditChapterSegments(
	chapter domain.AdaptationChapterPlan,
	sourceRunes map[int]int,
	mode adaptaudit.Mode,
) ([]domain.AdaptationSourceSegment, bool) {
	if len(chapter.SourceSegments) > 0 || mode != adaptaudit.ModeChapter {
		return append([]domain.AdaptationSourceSegment(nil), chapter.SourceSegments...), len(chapter.SourceSegments) > 0
	}
	sources := append([]int(nil), chapter.SourceChapters...)
	if len(sources) == 0 && chapter.SourceRange.From > 0 {
		for sourceChapter := chapter.SourceRange.From; sourceChapter <= chapter.SourceRange.To; sourceChapter++ {
			sources = append(sources, sourceChapter)
		}
	}
	segments := make([]domain.AdaptationSourceSegment, 0, len(sources))
	for _, sourceChapter := range sources {
		segments = append(segments, domain.AdaptationSourceSegment{
			SourceChapter: sourceChapter,
			Sequence:      1,
			RuneShare: domain.AdaptationSourceRuneShare{
				Start: 0,
				End:   sourceRunes[sourceChapter],
			},
		})
	}
	return segments, false
}

func auditEventFromDomain(event domain.AdaptationEvent) adaptaudit.Event {
	class := adaptaudit.ClassOrdinary
	if event.Importance == domain.AdaptationEventMainline {
		class = adaptaudit.ClassMainline
	}
	if strings.TrimSpace(event.RelationshipChange) != "" {
		class = adaptaudit.ClassRelationship
	}
	origin := adaptaudit.EventOrigin(event.Origin)
	auditEvent := adaptaudit.Event{
		ID: event.ID, Origin: origin, Class: class, Required: event.Required,
		Importance: string(event.Importance), DependsOn: append([]string(nil), event.DependsOn...),
	}
	if event.Relationship != nil {
		auditEvent.Class = adaptaudit.ClassRelationship
		auditEvent.Relationship = &adaptaudit.RelationshipChange{
			Pair: event.Relationship.Pair, From: event.Relationship.From, To: event.Relationship.To,
			AllowedFrom:      append([]string(nil), event.Relationship.AllowedFrom...),
			RequiresEventIDs: append([]string(nil), event.Relationship.RequiresEventIDs...),
		}
	}
	for _, claim := range event.SettingClaims {
		auditEvent.SettingClaims = append(auditEvent.SettingClaims, adaptaudit.SettingClaim{Key: claim.Key, Value: claim.Value})
	}
	return auditEvent
}

func attachSettingClaimEvidence(events []adaptaudit.Event, evidence map[string][]adaptaudit.Evidence) {
	for index := range events {
		items := evidence[events[index].ID]
		if len(items) == 0 {
			continue
		}
		for claimIndex := range events[index].SettingClaims {
			claim := &events[index].SettingClaims[claimIndex]
			for _, item := range items {
				if strings.Contains(item.Quote, claim.Value) {
					claim.Evidence = append(claim.Evidence, item)
				}
			}
		}
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func currentBodyEvidence(check *domain.AdaptationCheck, body, artifactID string) map[string][]adaptaudit.Evidence {
	out := make(map[string][]adaptaudit.Evidence)
	if check == nil || check.DraftSHA256 != store.TextSHA256(body) {
		return out
	}
	for _, item := range check.BodyEvidence {
		if item.EventID == "" || item.Quote == "" {
			continue
		}
		out[item.EventID] = append(out[item.EventID], adaptaudit.Evidence{ArtifactID: artifactID, Quote: item.Quote})
	}
	return out
}

func auditContractUnavailable(plan domain.AdaptationPlan, input adaptaudit.Input) string {
	selectedChapters := make(map[int]bool)
	for _, artifact := range input.Artifacts {
		if artifact.Kind == adaptaudit.ArtifactTargetPlan && artifact.Chapter > 0 {
			selectedChapters[artifact.Chapter] = true
		}
	}
	switch input.Mode {
	case adaptaudit.ModeArc:
		if len(plan.SourceEvents) == 0 {
			return "该项目由旧版 Arc 计划生成，缺少可验证的主线事件台账；本报告仅校验现有正文引文，不能据此判定正文遗漏，也不会提供自动修复。"
		}
		required := 0
		requiredIDs := make(map[string]bool)
		for _, event := range input.Events {
			if event.Origin == adaptaudit.OriginSource && event.Class == adaptaudit.ClassMainline && event.Required {
				required++
				requiredIDs[event.ID] = true
			}
		}
		bindings := 0
		for _, chapter := range plan.Chapters {
			if !selectedChapters[chapter.Chapter] {
				continue
			}
			for _, eventID := range chapter.EventIDs {
				if requiredIDs[eventID] {
					bindings++
				}
			}
		}
		if required > 0 && bindings == 0 {
			return "该项目由旧版 Arc 计划生成，缺少可验证的主线事件绑定；本报告仅校验现有正文引文，不能据此判定正文遗漏，也不会提供自动修复。"
		}
	case adaptaudit.ModeChapter:
		for _, chapter := range plan.Chapters {
			if selectedChapters[chapter.Chapter] && len(chapter.SourceSegments) == 0 {
				return "该项目的 Chapter 计划缺少持久化原著分段合同；无法安全判断 1:N 拆分覆盖，本报告不会提供自动修复。"
			}
		}
	case adaptaudit.ModeFree:
		selectedEventIDs := make(map[string]bool)
		for _, chapter := range plan.Chapters {
			if selectedChapters[chapter.Chapter] {
				for _, eventID := range chapter.EventIDs {
					selectedEventIDs[eventID] = true
				}
			}
		}
		covered := false
		for _, event := range plan.TargetEventLedger {
			if selectedEventIDs[event.ID] {
				covered = true
				break
			}
		}
		if len(selectedEventIDs) == 0 || !covered {
			return "该项目的 Free 计划缺少目标事件台账；本报告仅校验现有正文引文，不能完整判断因果与关系状态。"
		}
	}
	return ""
}

func sourceSegmentIDs(segments []domain.AdaptationSourceSegment) []string {
	ids := make([]string, 0, len(segments))
	for _, segment := range segments {
		ids = append(ids, sourceSegmentID(segment))
	}
	return ids
}

func sourceSegmentID(segment domain.AdaptationSourceSegment) string {
	return fmt.Sprintf("src-%04d-seg-%02d", segment.SourceChapter, segment.Sequence)
}

func chapterSelected(chapter int, options AuditOptions) bool {
	return (options.TargetFrom <= 0 || chapter >= options.TargetFrom) && (options.TargetTo <= 0 || chapter <= options.TargetTo)
}

func sourceChapterSelected(chapter int, options AuditOptions) bool {
	if chapter <= 0 {
		return true
	}
	return (options.SourceFrom <= 0 || chapter >= options.SourceFrom) && (options.SourceTo <= 0 || chapter <= options.SourceTo)
}

func targetRangeSelected(from, to int, options AuditOptions) bool {
	if options.TargetFrom > 0 && to < options.TargetFrom {
		return false
	}
	return options.TargetTo <= 0 || from <= options.TargetTo
}

func repairPlanFromAudit(plan *domain.AdaptationPlan, findings []adaptaudit.Finding) []int {
	if plan == nil {
		return nil
	}
	events := make(map[string]domain.AdaptationEvent)
	for _, event := range append(append([]domain.AdaptationEvent(nil), plan.SourceEvents...), plan.TargetEventLedger...) {
		events[event.ID] = event
	}
	affected := make(map[int]bool)
	for _, finding := range findings {
		if !finding.Blocking {
			continue
		}
		for _, chapter := range finding.TargetChapters {
			affected[chapter] = true
			appendAuditRepairDuty(plan, chapter, finding)
		}
		if finding.EventID == "" {
			continue
		}
		event, ok := events[finding.EventID]
		if !ok {
			continue
		}
		chapterIndex := bestChapterForSourceEvent(plan.Chapters, event)
		if chapterIndex < 0 {
			continue
		}
		chapter := &plan.Chapters[chapterIndex]
		if !slices.Contains(chapter.EventIDs, event.ID) {
			chapter.EventIDs = append(chapter.EventIDs, event.ID)
		}
		change := "兑现事件 " + event.ID + "：" + event.Description
		if !slices.Contains(chapter.RequiredChanges, change) {
			chapter.RequiredChanges = append(chapter.RequiredChanges, change)
		}
		affected[chapter.Chapter] = true
	}
	chapters := make([]int, 0, len(affected))
	for chapter := range affected {
		if chapter > 0 {
			chapters = append(chapters, chapter)
		}
	}
	sort.Ints(chapters)
	return chapters
}

func appendAuditRepairDuty(plan *domain.AdaptationPlan, targetChapter int, finding adaptaudit.Finding) {
	if plan == nil || targetChapter <= 0 {
		return
	}
	for index := range plan.Chapters {
		chapter := &plan.Chapters[index]
		if chapter.Chapter != targetChapter {
			continue
		}
		duty := fmt.Sprintf("审计修复 %s：%s", finding.Code, finding.Message)
		if !slices.Contains(chapter.RequiredChanges, duty) {
			chapter.RequiredChanges = append(chapter.RequiredChanges, duty)
		}
		return
	}
}

func bestChapterForSourceEvent(chapters []domain.AdaptationChapterPlan, event domain.AdaptationEvent) int {
	bestIndex, bestScore := -1, -1_000_000
	for index, chapter := range chapters {
		score := -1_000
		if slices.Contains(chapter.SourceChapters, event.SourceChapter) {
			score = 100
		}
		if chapter.SourceRange.From > 0 && event.SourceChapter >= chapter.SourceRange.From && event.SourceChapter <= chapter.SourceRange.To {
			width := chapter.SourceRange.To - chapter.SourceRange.From
			score = max(score, 60-min(width, 40))
		}
		if adaptationTextRelated(event.Description, strings.Join(append([]string{chapter.Title, chapter.CoreEvent}, chapter.PreserveEvents...), " ")) {
			score += 80
		}
		score -= len(chapter.EventIDs) * 5
		score -= len(chapter.AddedEventIDs) * 10
		if score > bestScore {
			bestIndex, bestScore = index, score
		}
	}
	if bestScore < 0 {
		return -1
	}
	return bestIndex
}

func adaptationTextRelated(left, right string) bool {
	leftRunes := []rune(strings.Join(strings.Fields(left), ""))
	right = strings.Join(strings.Fields(right), "")
	for width := 4; width >= 2; width-- {
		for index := 0; index+width <= len(leftRunes); index++ {
			if strings.Contains(right, string(leftRunes[index:index+width])) {
				return true
			}
		}
	}
	return false
}

func queueCompletedAuditRepairs(st *store.Store, affected []int, reason string) ([]int, error) {
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return nil, err
	}
	completed := make(map[int]bool, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		completed[chapter] = true
	}
	queued := append([]int(nil), progress.PendingRewrites...)
	for _, chapter := range affected {
		if completed[chapter] && !slices.Contains(queued, chapter) {
			queued = append(queued, chapter)
		}
	}
	sort.Ints(queued)
	if len(queued) == 0 {
		return nil, nil
	}
	reason = mergeRewriteReason(progress.RewriteReason, reason)
	if progress.Phase == domain.PhaseComplete {
		if err := st.Progress.Reopen(queued, reason); err != nil {
			return nil, fmt.Errorf("reopen completed project for audit repair: %w", err)
		}
	} else if err := st.Progress.SetPendingRewrites(queued, reason); err != nil {
		return nil, fmt.Errorf("queue audit repair chapters: %w", err)
	}
	return queued, nil
}

func mergeRewriteReason(existing, added string) string {
	existing = strings.TrimSpace(existing)
	added = strings.TrimSpace(added)
	if existing == "" {
		return added
	}
	if added == "" {
		return existing
	}
	if strings.Contains(existing, added) {
		return existing
	}
	return existing + "; " + added
}
