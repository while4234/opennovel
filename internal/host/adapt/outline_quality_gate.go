package adapt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	outlineQualityIssueChapterMissingSegment = "chapter_source_segment_missing"
	outlineQualityIssueChapterInvalidSegment = "chapter_source_segment_invalid"
	outlineQualityIssueArcMissingMainline    = "arc_mainline_missing"
	outlineQualityIssueArcDuplicateMainline  = "arc_mainline_duplicate"
	outlineQualityIssueArcWrongVolume        = "arc_mainline_wrong_volume"
	outlineQualityIssueArcAmbiguousVolume    = "arc_mainline_ambiguous_volume"
	outlineQualityIssueArcUnknownEvent       = "arc_event_unknown"
	outlineQualityIssueArcDuplicateEvent     = "arc_event_duplicate_binding"
	outlineQualityIssueArcPreserveUnbound    = "arc_event_preserve_unbound"
	outlineQualityIssueArcPreserveMismatch   = "arc_event_preserve_mismatch"
	outlineQualityIssueArcEventMismatch      = "arc_event_outline_mismatch"
	outlineQualityIssueArcBudgetDensity      = "arc_chapter_budget_scene_density"
	outlineQualityIssueFreeMissingLedger     = "free_target_ledger_missing"
	outlineQualityIssueFreeUnknownEvent      = "free_target_event_unknown"
	outlineQualityIssueFreeDuplicateBinding  = "free_target_event_duplicate_binding"
	outlineQualityIssueFreeMissingBinding    = "free_target_event_missing_binding"
	outlineQualityIssueFreeDependency        = "free_target_event_dependency"
	outlineQualityIssueFreeRelationship      = "free_relationship_transition"
	outlineQualityIssueFreeSetting           = "free_setting_consistency"
)

// AdaptationOutlineQualityIssue identifies one deterministic, plan-only
// contract violation. It deliberately contains no draft/body evidence: this
// gate runs before a Writer has created prose.
type AdaptationOutlineQualityIssue struct {
	Code                string
	Detail              string
	SourceChapter       int
	TargetChapter       int
	AlternativeChapters []int
	Volume              int
	EventID             string
}

// outlineIssueTargetChapters returns every target chapter that participates in
// an issue. Most issues have a single owner. Duplicate event ownership is an
// intentional exception: all owners must be retained so a later detail batch
// can repair the copy it is actually able to change.
func outlineIssueTargetChapters(issue AdaptationOutlineQualityIssue) []int {
	values := append([]int{issue.TargetChapter}, issue.AlternativeChapters...)
	seen := make(map[int]bool, len(values))
	out := make([]int, 0, len(values))
	for _, chapter := range values {
		if chapter <= 0 || seen[chapter] {
			continue
		}
		seen[chapter] = true
		out = append(out, chapter)
	}
	sort.Ints(out)
	return out
}

// AdaptationOutlineQualityError keeps all quality-gate failures available to a
// caller that wants to request a structural retry. The gate itself never
// invokes a model or retries work.
type AdaptationOutlineQualityError struct {
	Issues []AdaptationOutlineQualityIssue
}

func (e *AdaptationOutlineQualityError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "adaptation outline quality gate failed"
	}
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, "["+issue.Code+"] "+issue.Detail)
	}
	return "adaptation outline quality gate failed: " + strings.Join(parts, "; ")
}

// ValidateAdaptationOutlineQuality validates only durable planning contracts.
// It is intentionally independent from adaptaudit.Audit, whose checks require
// generated chapter bodies and therefore belong to the post-writing audit.
// A nil manifest is supported for legacy/simple proposals; source-rune segment
// coverage becomes enforceable as soon as a source manifest is available.
func ValidateAdaptationOutlineQuality(plan *domain.AdaptationPlan, manifest *domain.AdaptationSourceManifest) error {
	if plan == nil {
		return &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{
			Code: "outline_plan_missing", Detail: "adaptation plan is required",
		}}}
	}
	mode := domain.NormalizeAdaptationGranularity(plan.Granularity)
	var issues []AdaptationOutlineQualityIssue
	switch mode {
	case domain.AdaptationGranularityChapter:
		issues = append(issues, validateChapterOutlineSegments(*plan, manifest)...)
	case domain.AdaptationGranularityArc:
		issues = append(issues, validateArcOutlineMainline(*plan)...)
		issues = append(issues, validateArcOutlineSourceEvents(*plan)...)
		issues = append(issues, validateArcChapterBudgetDensity(*plan)...)
	case domain.AdaptationGranularityFree:
		issues = append(issues, validateFreeOutlineLedger(*plan)...)
	}
	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		if issues[left].Volume != issues[right].Volume {
			return issues[left].Volume < issues[right].Volume
		}
		if issues[left].SourceChapter != issues[right].SourceChapter {
			return issues[left].SourceChapter < issues[right].SourceChapter
		}
		if issues[left].TargetChapter != issues[right].TargetChapter {
			return issues[left].TargetChapter < issues[right].TargetChapter
		}
		if issues[left].EventID != issues[right].EventID {
			return issues[left].EventID < issues[right].EventID
		}
		return issues[left].Detail < issues[right].Detail
	})
	return &AdaptationOutlineQualityError{Issues: issues}
}

// ValidateAdaptationDetailPrefixQuality checks every semantic contract that is
// decidable before future detail batches exist. It deliberately omits global
// missing-event checks, which would misclassify not-yet-generated chapters.
func ValidateAdaptationDetailPrefixQuality(
	plan *domain.AdaptationPlan,
	manifest *domain.AdaptationSourceManifest,
	sourceFrom int,
	sourceTo int,
) error {
	if plan == nil {
		return &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{
			Code: "outline_plan_missing", Detail: "adaptation plan is required",
		}}}
	}
	var issues []AdaptationOutlineQualityIssue
	switch domain.NormalizeAdaptationGranularity(plan.Granularity) {
	case domain.AdaptationGranularityChapter:
		issues = append(issues, validateChapterOutlineSegmentsRange(*plan, manifest, sourceFrom, sourceTo)...)
	case domain.AdaptationGranularityArc:
		issues = append(issues, validateArcOutlineSourceEvents(*plan)...)
		issues = append(issues, validateArcChapterBudgetDensity(*plan)...)
	case domain.AdaptationGranularityFree:
		issues = append(issues, validateFreeOutlineLedger(*plan)...)
	}
	if len(issues) == 0 {
		return nil
	}
	sortAdaptationOutlineQualityIssues(issues)
	return &AdaptationOutlineQualityError{Issues: issues}
}

func validateChapterOutlineSegmentsRange(
	plan domain.AdaptationPlan,
	manifest *domain.AdaptationSourceManifest,
	from int,
	to int,
) []AdaptationOutlineQualityIssue {
	if manifest == nil || from <= 0 || to < from {
		return nil
	}
	sourceRunes := sourceRunesByChapter(manifest)
	segmentsBySource := make(map[int][]domain.AdaptationSourceSegment)
	for _, chapter := range plan.Chapters {
		for _, segment := range chapter.SourceSegments {
			segmentsBySource[segment.SourceChapter] = append(segmentsBySource[segment.SourceChapter], segment)
		}
	}
	var issues []AdaptationOutlineQualityIssue
	for sourceChapter := from; sourceChapter <= to; sourceChapter++ {
		runes := sourceRunes[sourceChapter]
		if runes <= 0 {
			continue
		}
		segments := segmentsBySource[sourceChapter]
		if len(segments) == 0 {
			issues = append(issues, AdaptationOutlineQualityIssue{
				Code: outlineQualityIssueChapterMissingSegment, SourceChapter: sourceChapter,
				Detail: fmt.Sprintf("source chapter %d has %d runes but no SourceSegment ownership", sourceChapter, runes),
			})
			continue
		}
		for _, segmentIssue := range domain.CheckAdaptationSourceSegments(sourceChapter, runes, segments) {
			issues = append(issues, AdaptationOutlineQualityIssue{
				Code: outlineQualityIssueChapterInvalidSegment, SourceChapter: sourceChapter,
				Detail: segmentIssue.Error(),
			})
		}
	}
	return issues
}

func sortAdaptationOutlineQualityIssues(issues []AdaptationOutlineQualityIssue) {
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		if issues[left].Volume != issues[right].Volume {
			return issues[left].Volume < issues[right].Volume
		}
		if issues[left].SourceChapter != issues[right].SourceChapter {
			return issues[left].SourceChapter < issues[right].SourceChapter
		}
		if issues[left].TargetChapter != issues[right].TargetChapter {
			return issues[left].TargetChapter < issues[right].TargetChapter
		}
		return issues[left].EventID < issues[right].EventID
	})
}

func validateChapterOutlineSegments(plan domain.AdaptationPlan, manifest *domain.AdaptationSourceManifest) []AdaptationOutlineQualityIssue {
	if manifest == nil {
		return nil
	}
	sourceRunes := sourceRunesByChapter(manifest)
	if len(sourceRunes) == 0 {
		return nil
	}
	segmentsBySource := make(map[int][]domain.AdaptationSourceSegment, len(sourceRunes))
	for _, chapter := range plan.Chapters {
		for _, segment := range chapter.SourceSegments {
			segmentsBySource[segment.SourceChapter] = append(segmentsBySource[segment.SourceChapter], segment)
		}
	}
	issues := make([]AdaptationOutlineQualityIssue, 0)
	for sourceChapter := 1; sourceChapter <= manifest.ChapterCount; sourceChapter++ {
		runes := sourceRunes[sourceChapter]
		if runes <= 0 {
			continue
		}
		segments := segmentsBySource[sourceChapter]
		if len(segments) == 0 {
			issues = append(issues, AdaptationOutlineQualityIssue{
				Code: outlineQualityIssueChapterMissingSegment, SourceChapter: sourceChapter,
				Detail: fmt.Sprintf("source chapter %d has %d runes but no SourceSegment ownership", sourceChapter, runes),
			})
			continue
		}
		for _, segmentIssue := range domain.CheckAdaptationSourceSegments(sourceChapter, runes, segments) {
			issues = append(issues, AdaptationOutlineQualityIssue{
				Code:          outlineQualityIssueChapterInvalidSegment,
				SourceChapter: sourceChapter,
				Detail:        segmentIssue.Error(),
			})
		}
	}
	for sourceChapter := range segmentsBySource {
		if _, known := sourceRunes[sourceChapter]; known {
			continue
		}
		issues = append(issues, AdaptationOutlineQualityIssue{
			Code: outlineQualityIssueChapterInvalidSegment, SourceChapter: sourceChapter,
			Detail: fmt.Sprintf("SourceSegment references source chapter %d which is absent from the source manifest", sourceChapter),
		})
	}
	return issues
}

func validateArcOutlineMainline(plan domain.AdaptationPlan) []AdaptationOutlineQualityIssue {
	bindings := chapterEventBindings(plan.Chapters)
	mainlineByID := make(map[string]domain.AdaptationEvent)
	for _, event := range plan.SourceEvents {
		if strings.TrimSpace(event.ID) == "" || event.Importance != domain.AdaptationEventMainline {
			continue
		}
		mainlineByID[event.ID] = event
	}

	issues := make([]AdaptationOutlineQualityIssue, 0)
	claimedByVolume := make(map[string][]int)
	for _, volume := range plan.Volumes {
		expected := make(map[string]bool)
		for _, eventID := range volume.MainlineEventIDs {
			if eventID = strings.TrimSpace(eventID); eventID != "" {
				expected[eventID] = true
			}
		}
		if volume.SourceFrom > 0 && volume.SourceTo >= volume.SourceFrom {
			for eventID, event := range mainlineByID {
				if event.SourceChapter >= volume.SourceFrom && event.SourceChapter <= volume.SourceTo {
					expected[eventID] = true
				}
			}
		}
		for eventID := range expected {
			claimedByVolume[eventID] = append(claimedByVolume[eventID], volume.Index)
			validateArcMainlineBinding(&issues, eventID, volume, bindings[eventID])
		}
	}

	for eventID, event := range mainlineByID {
		if len(claimedByVolume[eventID]) == 0 {
			validateArcMainlineBinding(&issues, eventID, domain.AdaptationVolumePlan{}, bindings[eventID])
			continue
		}
		if len(claimedByVolume[eventID]) > 1 {
			issues = append(issues, AdaptationOutlineQualityIssue{
				Code: outlineQualityIssueArcAmbiguousVolume, EventID: eventID, SourceChapter: event.SourceChapter,
				Detail: fmt.Sprintf("source mainline event %s belongs to multiple volume source ranges: %v", eventID, claimedByVolume[eventID]),
			})
		}
	}
	return issues
}

// validateArcOutlineSourceEvents closes the gap left by the mainline-only
// check above. Supporting and texture events are optional in an arc plan, but
// once a planner puts an event_id into a chapter contract it must be known,
// owned exactly once, and describe the same plot beat as that chapter. This
// keeps a later chapter's event from being "repaired" in Writer prose.
func validateArcOutlineSourceEvents(plan domain.AdaptationPlan) []AdaptationOutlineQualityIssue {
	issues := make([]AdaptationOutlineQualityIssue, 0)
	sourceEvents := make(map[string]domain.AdaptationEvent, len(plan.SourceEvents))
	for _, event := range plan.SourceEvents {
		if eventID := strings.TrimSpace(event.ID); eventID != "" {
			sourceEvents[eventID] = event
		}
	}
	for _, bindingIssue := range domain.ValidateArcSourceEventBindings(plan) {
		// Mainline duplicates already have a more specific, volume-aware issue
		// from validateArcOutlineMainline. Keep one canonical issue for them.
		if bindingIssue.Code == "arc_event_duplicate_binding" && sourceEvents[bindingIssue.EventID].Importance == domain.AdaptationEventMainline {
			continue
		}
		issue := AdaptationOutlineQualityIssue{
			Code:    bindingIssue.Code,
			EventID: bindingIssue.EventID,
			Detail:  bindingIssue.Detail,
		}
		if len(bindingIssue.Chapters) > 0 {
			issue.TargetChapter = bindingIssue.Chapters[0]
			issue.AlternativeChapters = append([]int(nil), bindingIssue.Chapters[1:]...)
		}
		if event, ok := sourceEvents[bindingIssue.EventID]; ok {
			issue.SourceChapter = event.SourceChapter
		}
		issues = append(issues, issue)
	}

	for _, mismatch := range domain.ValidateArcEventOutlineThemes(plan) {
		issues = append(issues, AdaptationOutlineQualityIssue{
			Code:                outlineQualityIssueArcEventMismatch,
			EventID:             mismatch.EventID,
			SourceChapter:       mismatch.SourceChapter,
			TargetChapter:       mismatch.TargetChapter,
			AlternativeChapters: append([]int(nil), mismatch.AlternativeChapters...),
			Detail:              mismatch.Detail,
		})
	}
	return issues
}

func validateArcChapterBudgetDensity(plan domain.AdaptationPlan) []AdaptationOutlineQualityIssue {
	issues := make([]AdaptationOutlineQualityIssue, 0)
	for _, budgetIssue := range domain.ValidateArcChapterBudgetDensity(plan) {
		issues = append(issues, AdaptationOutlineQualityIssue{
			Code: outlineQualityIssueArcBudgetDensity, TargetChapter: budgetIssue.Chapter,
			Detail: budgetIssue.Detail,
		})
	}
	return issues
}

// ValidateAdaptationChapterOutlineQuality is the migration-safe runtime
// boundary. New proposals use ValidateAdaptationOutlineQuality for the whole
// plan and receive a durable pass marker. Older confirmed plans may contain
// unrelated legacy issues in future chapters, so a resumed run validates only
// the chapter that is about to be written; it still checks that chapter's
// source-event ownership against the whole plan.
func ValidateAdaptationChapterOutlineQuality(plan *domain.AdaptationPlan, targetChapter int) error {
	if plan == nil {
		return &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{
			Code: "outline_plan_missing", Detail: "adaptation plan is required",
		}}}
	}
	if domain.NormalizeAdaptationGranularity(plan.Granularity) != domain.AdaptationGranularityArc {
		return nil
	}
	if targetChapter <= 0 {
		return fmt.Errorf("target chapter must be > 0")
	}
	knownChapter := false
	for _, chapter := range plan.Chapters {
		if chapter.Chapter == targetChapter {
			knownChapter = true
			break
		}
	}
	if !knownChapter {
		return &AdaptationOutlineQualityError{Issues: []AdaptationOutlineQualityIssue{{
			Code: "arc_target_chapter_missing", TargetChapter: targetChapter,
			Detail: fmt.Sprintf("confirmed arc plan has no target chapter %d", targetChapter),
		}}}
	}
	issues := make([]AdaptationOutlineQualityIssue, 0)
	for _, bindingIssue := range domain.ValidateArcSourceEventBindings(*plan) {
		ownsTarget := false
		for _, chapter := range bindingIssue.Chapters {
			if chapter == targetChapter {
				ownsTarget = true
				break
			}
		}
		if !ownsTarget {
			continue
		}
		issue := AdaptationOutlineQualityIssue{
			Code: bindingIssue.Code, EventID: bindingIssue.EventID,
			TargetChapter: targetChapter, Detail: bindingIssue.Detail,
		}
		for _, event := range plan.SourceEvents {
			if strings.TrimSpace(event.ID) == bindingIssue.EventID {
				issue.SourceChapter = event.SourceChapter
				break
			}
		}
		issues = append(issues, issue)
	}
	for _, mismatch := range domain.ValidateArcEventOutlineThemes(*plan) {
		if mismatch.TargetChapter != targetChapter {
			continue
		}
		issues = append(issues, AdaptationOutlineQualityIssue{
			Code: outlineQualityIssueArcEventMismatch, EventID: mismatch.EventID,
			SourceChapter: mismatch.SourceChapter, TargetChapter: targetChapter,
			AlternativeChapters: append([]int(nil), mismatch.AlternativeChapters...),
			Detail:              mismatch.Detail,
		})
	}
	for _, budgetIssue := range validateArcChapterBudgetDensity(*plan) {
		if budgetIssue.TargetChapter != targetChapter {
			continue
		}
		issues = append(issues, budgetIssue)
	}
	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		return issues[left].EventID < issues[right].EventID
	})
	return &AdaptationOutlineQualityError{Issues: issues}
}

// formatAdaptationOutlineQualityFeedback turns deterministic gate output into
// a compact next-attempt instruction. It is intentionally attached to the
// planner input rather than the Writer prompt, so the root contract is fixed
// before any chapter body is generated.
func formatAdaptationOutlineQualityFeedback(err *AdaptationOutlineQualityError) string {
	if err == nil || len(err.Issues) == 0 {
		return ""
	}
	type issueGroup struct {
		code    string
		count   int
		samples []string
	}
	groupsByCode := make(map[string]*issueGroup)
	for _, issue := range err.Issues {
		group := groupsByCode[issue.Code]
		if group == nil {
			group = &issueGroup{code: issue.Code}
			groupsByCode[issue.Code] = group
		}
		group.count++
		if len(group.samples) < 3 {
			group.samples = append(group.samples, issue.Detail)
		}
	}
	groups := make([]*issueGroup, 0, len(groupsByCode))
	for _, group := range groupsByCode {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].code < groups[j].code })
	var builder strings.Builder
	builder.WriteString("The previous detail-outline attempt failed the plan-only adaptation quality gate. Repair the outline contract before generating any prose.\n")
	for _, group := range groups {
		fmt.Fprintf(&builder, "- [%s] %d issue(s). Examples: %s\n", group.code, group.count, strings.Join(group.samples, " | "))
	}
	builder.WriteString("The runtime retains every individual issue and routes the complete per-batch set to its targeted repair; this summary only describes global issue classes.\n")
	builder.WriteString("Do not silence the error by deleting an event_id while keeping its plot beat in another chapter. Reassign the event_id, preserve_events, required_changes, and matching outline beat together; each source event_id may have only one owner.")
	return builder.String()
}

func validateArcMainlineBinding(
	issues *[]AdaptationOutlineQualityIssue,
	eventID string,
	volume domain.AdaptationVolumePlan,
	chapters []int,
) {
	if len(chapters) == 0 {
		*issues = append(*issues, AdaptationOutlineQualityIssue{
			Code: outlineQualityIssueArcMissingMainline, EventID: eventID, Volume: volume.Index,
			Detail: fmt.Sprintf("mainline event %s has no target chapter binding", eventID),
		})
		return
	}
	if len(chapters) != 1 {
		*issues = append(*issues, AdaptationOutlineQualityIssue{
			Code: outlineQualityIssueArcDuplicateMainline, EventID: eventID, Volume: volume.Index,
			Detail: fmt.Sprintf("mainline event %s is bound to target chapters %v; it must be assigned exactly once", eventID, chapters),
		})
		return
	}
	if volume.TargetFrom <= 0 || volume.TargetTo < volume.TargetFrom {
		return
	}
	if chapters[0] < volume.TargetFrom || chapters[0] > volume.TargetTo {
		*issues = append(*issues, AdaptationOutlineQualityIssue{
			Code: outlineQualityIssueArcWrongVolume, EventID: eventID, Volume: volume.Index, TargetChapter: chapters[0],
			Detail: fmt.Sprintf("mainline event %s is bound to target chapter %d, outside volume %d target range %d-%d", eventID, chapters[0], volume.Index, volume.TargetFrom, volume.TargetTo),
		})
	}
}

func validateFreeOutlineLedger(plan domain.AdaptationPlan) []AdaptationOutlineQualityIssue {
	if len(plan.TargetEventLedger) == 0 {
		return []AdaptationOutlineQualityIssue{{
			Code:   outlineQualityIssueFreeMissingLedger,
			Detail: "free adaptation requires a target_event_ledger before detailed outlines can be accepted",
		}}
	}
	issues := make([]AdaptationOutlineQualityIssue, 0)
	ledger := make(map[string]domain.AdaptationEvent, len(plan.TargetEventLedger))
	for _, event := range plan.TargetEventLedger {
		event.ID = strings.TrimSpace(event.ID)
		if event.ID == "" {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeUnknownEvent, Detail: "target_event_ledger contains a blank event id"})
			continue
		}
		if _, duplicate := ledger[event.ID]; duplicate {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeDuplicateBinding, EventID: event.ID, Detail: fmt.Sprintf("target_event_ledger contains duplicate event %s", event.ID)})
			continue
		}
		ledger[event.ID] = event
	}
	bindings := chapterEventBindings(plan.Chapters)
	for _, chapter := range plan.Chapters {
		if len(nonEmptyEventIDs(chapter.EventIDs)) == 0 {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeMissingBinding, TargetChapter: chapter.Chapter, Detail: fmt.Sprintf("target chapter %d has no event_ids for the target ledger", chapter.Chapter)})
		}
	}
	for eventID, chapters := range bindings {
		if _, exists := ledger[eventID]; !exists {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeUnknownEvent, EventID: eventID, Detail: fmt.Sprintf("target chapter binding references %s, which is missing from target_event_ledger", eventID)})
			continue
		}
		if len(chapters) != 1 {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeDuplicateBinding, EventID: eventID, Detail: fmt.Sprintf("target event %s is bound to chapters %v; ledger events need one owning chapter", eventID, chapters)})
		}
	}
	for eventID := range ledger {
		if len(bindings[eventID]) == 0 {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeMissingBinding, EventID: eventID, Detail: fmt.Sprintf("target ledger event %s has no chapter binding", eventID)})
		}
	}

	for eventID, event := range ledger {
		validateFreeEventDependencies(&issues, eventID, event.DependsOn, bindings, ledger)
	}
	issues = append(issues, validateFreeRelationshipTransitions(plan, ledger, bindings)...)
	issues = append(issues, validateFreeSettingClaims(plan, ledger)...)
	return issues
}

func validateFreeEventDependencies(
	issues *[]AdaptationOutlineQualityIssue,
	eventID string,
	dependsOn []string,
	bindings map[string][]int,
	ledger map[string]domain.AdaptationEvent,
) {
	chapters := bindings[eventID]
	if len(chapters) != 1 {
		return
	}
	for _, dependencyID := range nonEmptyEventIDs(dependsOn) {
		if _, exists := ledger[dependencyID]; !exists {
			*issues = append(*issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeDependency, EventID: eventID, TargetChapter: chapters[0], Detail: fmt.Sprintf("target event %s depends on missing ledger event %s", eventID, dependencyID)})
			continue
		}
		dependencyChapters := bindings[dependencyID]
		if len(dependencyChapters) != 1 || dependencyChapters[0] >= chapters[0] {
			*issues = append(*issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeDependency, EventID: eventID, TargetChapter: chapters[0], Detail: fmt.Sprintf("target event %s depends on %s, which is not established in an earlier target chapter", eventID, dependencyID)})
		}
	}
}

func validateFreeRelationshipTransitions(plan domain.AdaptationPlan, ledger map[string]domain.AdaptationEvent, bindings map[string][]int) []AdaptationOutlineQualityIssue {
	type transition struct {
		eventID string
		chapter int
		value   domain.AdaptationRelationshipTransition
	}
	var transitions []transition
	for eventID, event := range ledger {
		if event.Relationship == nil || len(bindings[eventID]) != 1 {
			continue
		}
		transitions = append(transitions, transition{eventID: eventID, chapter: bindings[eventID][0], value: *event.Relationship})
	}
	sort.SliceStable(transitions, func(left, right int) bool {
		if transitions[left].chapter != transitions[right].chapter {
			return transitions[left].chapter < transitions[right].chapter
		}
		return transitions[left].eventID < transitions[right].eventID
	})
	issues := make([]AdaptationOutlineQualityIssue, 0)
	currentState := make(map[string]string)
	for _, item := range transitions {
		relationship := item.value
		relationship.Pair = strings.TrimSpace(relationship.Pair)
		relationship.From = strings.TrimSpace(relationship.From)
		relationship.To = strings.TrimSpace(relationship.To)
		if relationship.Pair == "" || relationship.To == "" {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeRelationship, EventID: item.eventID, TargetChapter: item.chapter, Detail: fmt.Sprintf("relationship transition for event %s needs pair and to state", item.eventID)})
			continue
		}
		if previous, exists := currentState[relationship.Pair]; exists && !relationshipStateAllows(previous, relationship) {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeRelationship, EventID: item.eventID, TargetChapter: item.chapter, Detail: fmt.Sprintf("relationship %s moves from %s after prior state %s", relationship.Pair, relationship.From, previous)})
		}
		validateFreeEventDependencies(&issues, item.eventID, relationship.RequiresEventIDs, bindings, ledger)
		currentState[relationship.Pair] = relationship.To
	}
	for pair, expected := range plan.TargetRelationshipStates {
		pair = strings.TrimSpace(pair)
		expected = strings.TrimSpace(expected)
		if pair == "" || expected == "" {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeRelationship, Detail: "target_relationship_states contains a blank pair or state"})
			continue
		}
		if actual, known := currentState[pair]; known && actual != expected {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeRelationship, Detail: fmt.Sprintf("relationship %s ends at %s but target state is %s", pair, actual, expected)})
		}
	}
	return issues
}

func relationshipStateAllows(previous string, relationship domain.AdaptationRelationshipTransition) bool {
	if relationship.From != "" && relationship.From == previous {
		return true
	}
	for _, allowed := range relationship.AllowedFrom {
		if strings.TrimSpace(allowed) == previous {
			return true
		}
	}
	return relationship.From == "" && len(relationship.AllowedFrom) == 0
}

func validateFreeSettingClaims(plan domain.AdaptationPlan, ledger map[string]domain.AdaptationEvent) []AdaptationOutlineQualityIssue {
	issues := make([]AdaptationOutlineQualityIssue, 0)
	locks := make(map[string]string, len(plan.TargetSettingLocks))
	for _, lock := range plan.TargetSettingLocks {
		key, value := strings.TrimSpace(lock.Key), strings.TrimSpace(lock.Value)
		if key == "" || value == "" {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeSetting, Detail: "target_setting_locks contains a blank key or value"})
			continue
		}
		if existing, duplicate := locks[key]; duplicate && existing != value {
			issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeSetting, Detail: fmt.Sprintf("target setting lock %s has conflicting values %s and %s", key, existing, value)})
			continue
		}
		locks[key] = value
	}
	claims := make(map[string]string)
	for eventID, event := range ledger {
		for _, claim := range event.SettingClaims {
			key, value := strings.TrimSpace(claim.Key), strings.TrimSpace(claim.Value)
			if key == "" || value == "" {
				issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeSetting, EventID: eventID, Detail: fmt.Sprintf("target event %s has a blank setting claim", eventID)})
				continue
			}
			if locked, exists := locks[key]; exists && locked != value {
				issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeSetting, EventID: eventID, Detail: fmt.Sprintf("target event %s claims setting %s=%s but lock requires %s", eventID, key, value, locked)})
			}
			if existing, exists := claims[key]; exists && existing != value {
				issues = append(issues, AdaptationOutlineQualityIssue{Code: outlineQualityIssueFreeSetting, EventID: eventID, Detail: fmt.Sprintf("target setting %s changes from %s to %s without an explicit setting transition", key, existing, value)})
				continue
			}
			claims[key] = value
		}
	}
	return issues
}

func chapterEventBindings(chapters []domain.AdaptationChapterPlan) map[string][]int {
	bindings := make(map[string][]int)
	for index, chapter := range chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = index + 1
		}
		seen := make(map[string]bool)
		for _, eventID := range nonEmptyEventIDs(chapter.EventIDs) {
			if seen[eventID] {
				continue
			}
			seen[eventID] = true
			bindings[eventID] = append(bindings[eventID], number)
		}
	}
	return bindings
}

func nonEmptyEventIDs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
