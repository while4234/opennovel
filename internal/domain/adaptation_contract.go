package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	AdaptationOutlineQualityAuditLegacyVersion = 1
	AdaptationOutlineQualityAuditVersion       = 2
	AdaptationOutlineQualityStatusPassed       = "passed"
	AdaptationBudgetRepairVersion              = 1
)

// AdaptationOutlineQualityAudit is a durable marker for the deterministic
// plan-only gate. Its signature covers semantic contract fields and chapter
// budgets, so changing either the story ownership or output capacity forces a
// fresh audit.
type AdaptationOutlineQualityAudit struct {
	Version            int    `json:"version"`
	Status             string `json:"status"`
	Signature          string `json:"signature"`
	LayeredAuditDigest string `json:"layered_audit_digest,omitempty"`
	CheckedAt          string `json:"checked_at"`
}

func AdaptationPlanOutlineQualitySignature(plan AdaptationPlan) string {
	type sourceEvent struct {
		ID            string                    `json:"id"`
		Description   string                    `json:"description"`
		Importance    AdaptationEventImportance `json:"importance"`
		SourceChapter int                       `json:"source_chapter"`
	}
	type volume struct {
		Index            int      `json:"index"`
		TargetFrom       int      `json:"target_from"`
		TargetTo         int      `json:"target_to"`
		SourceFrom       int      `json:"source_from"`
		SourceTo         int      `json:"source_to"`
		MainlineEventIDs []string `json:"mainline_event_ids,omitempty"`
	}
	type chapter struct {
		Chapter         int      `json:"chapter"`
		Title           string   `json:"title"`
		CoreEvent       string   `json:"core_event"`
		Hook            string   `json:"hook"`
		Scenes          []string `json:"scenes,omitempty"`
		TargetRunes     int      `json:"target_runes,omitempty"`
		TargetMinRunes  int      `json:"target_min_runes,omitempty"`
		TargetMaxRunes  int      `json:"target_max_runes,omitempty"`
		EventIDs        []string `json:"event_ids,omitempty"`
		AddedEventIDs   []string `json:"added_event_ids,omitempty"`
		PreserveEvents  []string `json:"preserve_events,omitempty"`
		RequiredChanges []string `json:"required_changes,omitempty"`
		ForbiddenMoves  []string `json:"forbidden_moves,omitempty"`
	}
	contract := struct {
		Granularity  string        `json:"granularity"`
		SourceEvents []sourceEvent `json:"source_events,omitempty"`
		Volumes      []volume      `json:"volumes,omitempty"`
		Chapters     []chapter     `json:"chapters"`
	}{Granularity: NormalizeAdaptationGranularity(plan.Granularity)}
	for _, event := range plan.SourceEvents {
		contract.SourceEvents = append(contract.SourceEvents, sourceEvent{
			ID: strings.TrimSpace(event.ID), Description: strings.TrimSpace(event.Description),
			Importance: event.Importance, SourceChapter: event.SourceChapter,
		})
	}
	for _, item := range plan.Volumes {
		contract.Volumes = append(contract.Volumes, volume{
			Index: item.Index, TargetFrom: item.TargetFrom, TargetTo: item.TargetTo,
			SourceFrom: item.SourceFrom, SourceTo: item.SourceTo,
			MainlineEventIDs: append([]string(nil), item.MainlineEventIDs...),
		})
	}
	for _, item := range plan.Chapters {
		contract.Chapters = append(contract.Chapters, chapter{
			Chapter: item.Chapter, Title: strings.TrimSpace(item.Title),
			CoreEvent: strings.TrimSpace(item.CoreEvent), Hook: strings.TrimSpace(item.Hook),
			Scenes: append([]string(nil), item.Scenes...), EventIDs: append([]string(nil), item.EventIDs...),
			TargetRunes: item.TargetRunes, TargetMinRunes: item.TargetMinRunes, TargetMaxRunes: item.TargetMaxRunes,
			AddedEventIDs: append([]string(nil), item.AddedEventIDs...), PreserveEvents: append([]string(nil), item.PreserveEvents...),
			RequiredChanges: append([]string(nil), item.RequiredChanges...), ForbiddenMoves: append([]string(nil), item.ForbiddenMoves...),
		})
	}
	data, err := json.Marshal(contract)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// AdaptationPlanStoryContractSignature excludes chapter output budgets while
// retaining event ownership and all outline/story contract fields. It lets a
// legacy-budget migration prove that an old backup is the same plan before
// borrowing only its pre-repair budget values.
func AdaptationPlanStoryContractSignature(plan AdaptationPlan) string {
	copyPlan := plan
	copyPlan.Chapters = append([]AdaptationChapterPlan(nil), plan.Chapters...)
	for index := range copyPlan.Chapters {
		copyPlan.Chapters[index].TargetRunes = 0
		copyPlan.Chapters[index].TargetMinRunes = 0
		copyPlan.Chapters[index].TargetMaxRunes = 0
		copyPlan.Chapters[index].WordBudget = nil
	}
	return AdaptationPlanOutlineQualitySignature(copyPlan)
}

// AdaptationPlanBudgetRepairLineageSignature excludes chapter output budgets
// and event-binding fields while retaining the plot contract. A legacy budget
// backup can legitimately predate a targeted event-binding repair; that repair
// must not make a budget-only migration look like a different story plan.
func AdaptationPlanBudgetRepairLineageSignature(plan AdaptationPlan) string {
	copyPlan := plan
	copyPlan.Chapters = append([]AdaptationChapterPlan(nil), plan.Chapters...)
	for index := range copyPlan.Chapters {
		copyPlan.Chapters[index].TargetRunes = 0
		copyPlan.Chapters[index].TargetMinRunes = 0
		copyPlan.Chapters[index].TargetMaxRunes = 0
		copyPlan.Chapters[index].WordBudget = nil
		copyPlan.Chapters[index].EventIDs = nil
		copyPlan.Chapters[index].PreserveEvents = nil
	}
	return AdaptationPlanOutlineQualitySignature(copyPlan)
}

func AdaptationOutlineQualityPassed(plan AdaptationPlan) bool {
	audit := plan.OutlineQualityAudit
	if audit == nil || audit.Status != AdaptationOutlineQualityStatusPassed || audit.Signature == "" || audit.Signature != AdaptationPlanOutlineQualitySignature(plan) {
		return false
	}
	switch audit.Version {
	case AdaptationOutlineQualityAuditLegacyVersion:
		return true
	case AdaptationOutlineQualityAuditVersion:
		return strings.TrimSpace(audit.LayeredAuditDigest) != ""
	default:
		return false
	}
}

func MarkAdaptationOutlineQualityPassed(plan *AdaptationPlan) {
	if plan == nil {
		return
	}
	plan.OutlineQualityAudit = &AdaptationOutlineQualityAudit{
		Version:   AdaptationOutlineQualityAuditLegacyVersion,
		Status:    AdaptationOutlineQualityStatusPassed,
		Signature: AdaptationPlanOutlineQualitySignature(*plan),
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// MarkAdaptationOutlineQualityPassedWithLayers is the pre-writing gate for new
// proposals. The digest proves that every required batch, parent, volume, and
// global checkpoint passed for this exact plan.
func MarkAdaptationOutlineQualityPassedWithLayers(plan *AdaptationPlan, layeredDigest string) {
	if plan == nil || strings.TrimSpace(layeredDigest) == "" {
		return
	}
	plan.OutlineQualityAudit = &AdaptationOutlineQualityAudit{
		Version:            AdaptationOutlineQualityAuditVersion,
		Status:             AdaptationOutlineQualityStatusPassed,
		Signature:          AdaptationPlanOutlineQualitySignature(*plan),
		LayeredAuditDigest: strings.TrimSpace(layeredDigest),
		CheckedAt:          time.Now().UTC().Format(time.RFC3339),
	}
}

func ClearAdaptationOutlineQualityAudit(plan *AdaptationPlan) {
	if plan != nil {
		plan.OutlineQualityAudit = nil
	}
}

// MarkAdaptationBudgetRepair records a completed legacy-budget migration.
// The record is metadata only; it is intentionally excluded from the outline
// quality signature so the signature continues to describe the story and its
// actual chapter contracts.
func MarkAdaptationBudgetRepair(plan *AdaptationPlan, mode string, attempts int, chapters []int, reason string) {
	if plan == nil {
		return
	}
	copyChapters := append([]int(nil), chapters...)
	sort.Ints(copyChapters)
	plan.BudgetRepair = &AdaptationBudgetRepairRecord{
		Version:       AdaptationBudgetRepairVersion,
		Mode:          strings.TrimSpace(mode),
		Attempts:      attempts,
		Chapters:      copyChapters,
		Reason:        strings.TrimSpace(reason),
		CompletedAt:   time.Now().UTC().Format(time.RFC3339),
		PlanSignature: AdaptationPlanOutlineQualitySignature(*plan),
	}
}

// AdaptationEventBindingIssue is the small, dependency-free subset of the
// adaptation contract that runtime code can validate without importing the
// planner. The planner performs the richer semantic outline audit; this
// helper protects writing and commit paths from unknown or multiply-owned
// source event IDs as well.
type AdaptationEventBindingIssue struct {
	Code     string
	EventID  string
	Chapters []int
	Detail   string
}

// AdaptationEventOutlineMismatchIssue reports a source event whose assigned
// chapter has no matching plot theme while another target chapter does. It is
// intentionally defined in domain so runtime tools can use the same final
// fallback as the planner without importing internal/host/adapt.
type AdaptationEventOutlineMismatchIssue struct {
	EventID             string
	Description         string
	SourceChapter       int
	TargetChapter       int
	MissingThemes       []string
	AlternativeChapters []int
	Detail              string
}

// AdaptationChapterBudgetDensityIssue reports a chapter whose outline contains
// too many scenes for its configured maximum output. The threshold is
// intentionally conservative: it catches impossible compression requests
// without forcing ordinary short chapters to become long chapters.
type AdaptationChapterBudgetDensityIssue struct {
	Chapter             int
	SceneCount          int
	MaxRunes            int
	RecommendedMinRunes int
	Detail              string
}

type adaptationEventTheme struct {
	name         string
	eventTerms   []string
	outlineTerms []string
}

var arcAdaptationEventThemes = []adaptationEventTheme{
	{
		name:         "encounter_conflict",
		eventTerms:   []string{"抢劫", "劫匪", "黑衣人", "交出手机", "手机", "拦截", "夺刀", "放走", "抢钱", "救母", "晨练", "劫持", "跳楼", "钢管", "讨债", "围观", "劝阻", "体操"},
		outlineTerms: []string{"抢劫", "劫匪", "黑衣人", "被迫", "交出", "拦截", "夺刀", "钢管", "围堵", "讨债", "出手", "救下", "救人", "冲突", "劫持", "跳楼", "围观", "劝阻", "体操", "物理", "计算", "瓦解", "死志", "篮球场"},
	},
	{
		name:         "phone_alert",
		eventTerms:   []string{"盖奇", "来上海", "紧急联系", "电话"},
		outlineTerms: []string{"盖奇", "来上海", "紧急", "电话", "驱车", "大哥"},
	},
	{
		name:         "financial_crisis",
		eventTerms:   []string{"飞龙集团", "危机真相", "资金链", "多宝集团", "诈骗", "财务危机"},
		outlineTerms: []string{"飞龙集团", "危机", "资金链", "多宝集团", "诈骗", "财务", "真相", "询问"},
	},
	{
		name:         "first_meeting",
		eventTerms:   []string{"结识", "相识", "初遇", "请客吃饭", "带其进入", "第一次见面"},
		outlineTerms: []string{"首次登场", "直视", "叫什么名字", "四目相对", "相识", "结识", "请客吃饭", "邀请", "姓名", "初次对话", "早餐", "早餐对话"},
	},
}

// ValidateArcSourceEventBindings verifies that every event_id used by an arc
// chapter is a known source event and that one source event has one owner.
// added_event_ids are intentionally excluded: they are target-story events,
// not source-event bindings.
func ValidateArcSourceEventBindings(plan AdaptationPlan) []AdaptationEventBindingIssue {
	if NormalizeAdaptationGranularity(plan.Granularity) != AdaptationGranularityArc {
		return nil
	}
	sourceEvents := make(map[string]AdaptationEvent, len(plan.SourceEvents))
	for _, event := range plan.SourceEvents {
		if eventID := strings.TrimSpace(event.ID); eventID != "" {
			sourceEvents[eventID] = event
		}
	}
	issues := make([]AdaptationEventBindingIssue, 0)
	bindings := make(map[string][]int)
	for index, chapter := range plan.Chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = index + 1
		}
		for _, rawEventID := range chapter.EventIDs {
			eventID := strings.TrimSpace(rawEventID)
			if eventID == "" {
				continue
			}
			if _, known := sourceEvents[eventID]; !known && strings.HasPrefix(eventID, "src-") {
				issues = append(issues, AdaptationEventBindingIssue{
					Code:     "arc_event_unknown",
					EventID:  eventID,
					Chapters: []int{number},
					Detail:   fmt.Sprintf("target chapter %d references unknown source event %s", number, eventID),
				})
				continue
			}
			// Keep repeated occurrences in the slice so the validator also rejects
			// the same event listed twice inside one chapter.
			bindings[eventID] = append(bindings[eventID], number)
		}
	}
	for eventID, chapters := range bindings {
		if len(chapters) <= 1 {
			continue
		}
		issues = append(issues, AdaptationEventBindingIssue{
			Code:     "arc_event_duplicate_binding",
			EventID:  eventID,
			Chapters: append([]int(nil), chapters...),
			Detail:   fmt.Sprintf("source event %s is bound to target chapters %v; it must have exactly one owning chapter", eventID, chapters),
		})
	}
	for index, chapter := range plan.Chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = index + 1
		}
		preserved := sourceEventIDs(chapter.PreserveEvents)
		if len(preserved) == 0 || !allSourceEventIDs(chapter.EventIDs) {
			continue
		}
		assigned := make(map[string]bool, len(chapter.EventIDs))
		for _, rawEventID := range chapter.EventIDs {
			assigned[strings.TrimSpace(rawEventID)] = true
		}
		for _, eventID := range preserved {
			if assigned[eventID] {
				continue
			}
			issues = append(issues, AdaptationEventBindingIssue{
				Code: "arc_event_preserve_unbound", EventID: eventID, Chapters: []int{number},
				Detail: fmt.Sprintf("target chapter %d lists source event %s in preserve_events but not in event_ids; keep the source-event ownership fields aligned", number, eventID),
			})
		}
		for _, rawEventID := range chapter.EventIDs {
			eventID := strings.TrimSpace(rawEventID)
			if containsString(preserved, eventID) {
				continue
			}
			issues = append(issues, AdaptationEventBindingIssue{
				Code: "arc_event_preserve_mismatch", EventID: eventID, Chapters: []int{number},
				Detail: fmt.Sprintf("target chapter %d assigns source event %s in event_ids but preserve_events points to different source events; move the event ownership and matching plot beat together", number, eventID),
			})
		}
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		return issues[left].EventID < issues[right].EventID
	})
	return issues
}

const minimumArcRunesPerScene = 300

// ValidateArcChapterBudgetDensity prevents a planner from packing a multi-
// scene arc chapter into a budget that cannot express its own beats. A caller
// may still choose fewer scenes or a larger budget; this validator never edits
// a plan and never reasons about prose.
func ValidateArcChapterBudgetDensity(plan AdaptationPlan) []AdaptationChapterBudgetDensityIssue {
	if NormalizeAdaptationGranularity(plan.Granularity) != AdaptationGranularityArc {
		return nil
	}
	issues := make([]AdaptationChapterBudgetDensityIssue, 0)
	for index, chapter := range plan.Chapters {
		sceneCount := len(chapter.Scenes)
		// Keep the guard focused on real multi-beat chapters. A missing budget
		// is handled by the broader plan validator; any positive budget is
		// meaningful evidence, including extremely small legacy budgets.
		if sceneCount < 6 {
			continue
		}
		maxRunes := chapter.TargetMaxRunes
		if chapter.WordBudget != nil && chapter.WordBudget.MaxRunes > maxRunes {
			maxRunes = chapter.WordBudget.MaxRunes
		}
		if maxRunes <= 0 {
			continue
		}
		recommendedMin := sceneCount * minimumArcRunesPerScene
		if maxRunes >= recommendedMin {
			continue
		}
		number := chapter.Chapter
		if number <= 0 {
			number = index + 1
		}
		issues = append(issues, AdaptationChapterBudgetDensityIssue{
			Chapter: number, SceneCount: sceneCount, MaxRunes: maxRunes, RecommendedMinRunes: recommendedMin,
			Detail: fmt.Sprintf("target chapter %d has %d scenes but max budget is %d runes; raise the chapter budget to at least %d runes or reduce/split the scenes before writing", number, sceneCount, maxRunes, recommendedMin),
		})
	}
	sort.SliceStable(issues, func(left, right int) bool { return issues[left].Chapter < issues[right].Chapter })
	return issues
}

// RepairArcChapterBudgetDensity migrates legacy arc plans whose scene count
// clearly exceeds their chapter capacity. It only expands budgets; event
// ownership and scene content remain unchanged. Callers should persist the
// plan through AdaptationStore so the pre-repair snapshot can be backed up.
func RepairArcChapterBudgetDensity(plan *AdaptationPlan) []int {
	if plan == nil || NormalizeAdaptationGranularity(plan.Granularity) != AdaptationGranularityArc {
		return nil
	}
	issues := ValidateArcChapterBudgetDensity(*plan)
	if len(issues) == 0 {
		return nil
	}
	repaired := make([]int, 0, len(issues))
	for _, issue := range issues {
		for index := range plan.Chapters {
			chapter := &plan.Chapters[index]
			number := chapter.Chapter
			if number <= 0 {
				number = index + 1
			}
			if number != issue.Chapter {
				continue
			}
			targetFloor, minFloor, maxFloor := arcChapterBudgetFloor(issue.SceneCount)
			chapter.TargetRunes = maxAdaptationInt(chapter.TargetRunes, targetFloor)
			chapter.TargetMinRunes = maxAdaptationInt(chapter.TargetMinRunes, minFloor)
			chapter.TargetMaxRunes = maxAdaptationInt(chapter.TargetMaxRunes, maxFloor)
			if chapter.TargetMinRunes > chapter.TargetRunes {
				chapter.TargetRunes = chapter.TargetMinRunes
			}
			if chapter.TargetRunes > chapter.TargetMaxRunes {
				chapter.TargetMaxRunes = chapter.TargetRunes
			}
			if chapter.WordBudget == nil {
				chapter.WordBudget = &AdaptationChapterWordBudget{SourceRunes: chapter.SourceRunes}
			}
			chapter.WordBudget.TargetRunes = chapter.TargetRunes
			chapter.WordBudget.MinRunes = chapter.TargetMinRunes
			chapter.WordBudget.MaxRunes = chapter.TargetMaxRunes
			if chapter.WordBudget.SourceRunes <= 0 {
				chapter.WordBudget.SourceRunes = chapter.SourceRunes
			}
			if chapter.WordBudget.Tolerance <= 0 {
				chapter.WordBudget.Tolerance = plan.WordTolerance
			}
			repaired = append(repaired, number)
			break
		}
	}
	if len(repaired) == 0 {
		return nil
	}
	plan.TargetTotalRunes = 0
	plan.TargetMinRunes = 0
	plan.TargetMaxRunes = 0
	for _, chapter := range plan.Chapters {
		plan.TargetTotalRunes += chapter.TargetRunes
		plan.TargetMinRunes += chapter.TargetMinRunes
		plan.TargetMaxRunes += chapter.TargetMaxRunes
	}
	ClearAdaptationOutlineQualityAudit(plan)
	return repaired
}

func arcChapterBudgetFloor(sceneCount int) (target, min, max int) {
	if sceneCount >= 7 {
		return 4000, 3400, 4600
	}
	return 3500, 3000, 4000
}

func maxAdaptationInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// ValidateArcEventOutlineThemes is the shared semantic safety net for arc
// plans. It is deliberately conservative: it only reports a mismatch when a
// recognized event theme is absent from its owner but is present in another
// target chapter. Supporting/texture events remain optional when the planner
// does not bind them at all.
func ValidateArcEventOutlineThemes(plan AdaptationPlan) []AdaptationEventOutlineMismatchIssue {
	if NormalizeAdaptationGranularity(plan.Granularity) != AdaptationGranularityArc {
		return nil
	}
	sourceEvents := make(map[string]AdaptationEvent, len(plan.SourceEvents))
	for _, event := range plan.SourceEvents {
		if eventID := strings.TrimSpace(event.ID); eventID != "" {
			sourceEvents[eventID] = event
		}
	}
	bindings := make(map[string][]int)
	for index, chapter := range plan.Chapters {
		number := chapter.Chapter
		if number <= 0 {
			number = index + 1
		}
		for _, rawEventID := range chapter.EventIDs {
			eventID := strings.TrimSpace(rawEventID)
			if eventID != "" {
				if !containsInt(bindings[eventID], number) {
					bindings[eventID] = append(bindings[eventID], number)
				}
			}
		}
	}
	issues := make([]AdaptationEventOutlineMismatchIssue, 0)
	for eventID, chapters := range bindings {
		event, known := sourceEvents[eventID]
		if !known {
			continue
		}
		themes := adaptationThemeNames(event.Description, true)
		if len(themes) == 0 {
			continue
		}
		for _, owner := range chapters {
			ownerChapter, ownerKnown := adaptationChapterByNumber(plan.Chapters, owner)
			// Explicit source lineage is stronger evidence than a fuzzy keyword
			// match. A chapter that actually covers the event's source chapter is
			// allowed to paraphrase the beat without repeating one trigger word.
			if ownerKnown && adaptationChapterCoversSource(ownerChapter, event.SourceChapter) {
				continue
			}
			ownerThemes := adaptationChapterThemeSet(plan.Chapters, owner)
			missing := differenceThemeNames(themes, ownerThemes)
			if len(missing) == 0 {
				continue
			}
			alternatives := adaptationSourceLineageAlternatives(plan.Chapters, owner, event.SourceChapter)
			if len(alternatives) == 0 {
				for _, candidate := range plan.Chapters {
					candidateNumber := candidate.Chapter
					if candidateNumber == owner {
						continue
					}
					if len(intersectThemeNames(missing, adaptationChapterThemeSet([]AdaptationChapterPlan{candidate}, candidateNumber))) > 0 {
						alternatives = append(alternatives, candidateNumber)
					}
				}
			}
			if len(alternatives) == 0 {
				continue
			}
			issues = append(issues, AdaptationEventOutlineMismatchIssue{
				EventID:             eventID,
				Description:         event.Description,
				SourceChapter:       event.SourceChapter,
				TargetChapter:       owner,
				MissingThemes:       missing,
				AlternativeChapters: alternatives,
				Detail: fmt.Sprintf(
					"source event %s (%s) is bound to target chapter %d, but its plot theme(s) %s are absent from that chapter and appear in target chapter(s) %v; move event_ids, preserve_events, required_changes, and the matching story beat together before writing",
					eventID, clipAdaptationText(event.Description, 120), owner, strings.Join(missing, ", "), alternatives,
				),
			})
		}
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].TargetChapter != issues[right].TargetChapter {
			return issues[left].TargetChapter < issues[right].TargetChapter
		}
		return issues[left].EventID < issues[right].EventID
	})
	return issues
}

func adaptationChapterByNumber(chapters []AdaptationChapterPlan, number int) (AdaptationChapterPlan, bool) {
	for _, chapter := range chapters {
		if chapter.Chapter == number {
			return chapter, true
		}
	}
	return AdaptationChapterPlan{}, false
}

func adaptationChapterCoversSource(chapter AdaptationChapterPlan, sourceChapter int) bool {
	if sourceChapter <= 0 {
		return false
	}
	if containsInt(chapter.SourceChapters, sourceChapter) {
		return true
	}
	return chapter.SourceRange.From > 0 && chapter.SourceRange.To >= chapter.SourceRange.From &&
		sourceChapter >= chapter.SourceRange.From && sourceChapter <= chapter.SourceRange.To
}

func adaptationSourceLineageAlternatives(chapters []AdaptationChapterPlan, owner, sourceChapter int) []int {
	if sourceChapter <= 0 {
		return nil
	}
	var alternatives []int
	for _, chapter := range chapters {
		if chapter.Chapter == owner || !adaptationChapterCoversSource(chapter, sourceChapter) {
			continue
		}
		alternatives = append(alternatives, chapter.Chapter)
	}
	return alternatives
}

func adaptationThemeNames(text string, event bool) []string {
	set := make(map[string]bool)
	for _, theme := range arcAdaptationEventThemes {
		terms := theme.outlineTerms
		if event {
			terms = theme.eventTerms
		}
		for _, term := range terms {
			if strings.Contains(text, term) {
				set[theme.name] = true
				break
			}
		}
	}
	out := make([]string, 0, len(set))
	for _, theme := range arcAdaptationEventThemes {
		if set[theme.name] {
			out = append(out, theme.name)
		}
	}
	return out
}

func adaptationChapterThemeSet(chapters []AdaptationChapterPlan, number int) map[string]bool {
	set := make(map[string]bool)
	for _, chapter := range chapters {
		if chapter.Chapter != number {
			continue
		}
		text := strings.Join([]string{chapter.Title, chapter.CoreEvent, chapter.Hook, strings.Join(chapter.Scenes, " ")}, "\n")
		for _, theme := range adaptationThemeNames(text, false) {
			set[theme] = true
		}
		return set
	}
	return set
}

func differenceThemeNames(themes []string, present map[string]bool) []string {
	out := make([]string, 0, len(themes))
	for _, theme := range themes {
		if !present[theme] {
			out = append(out, theme)
		}
	}
	return out
}

func intersectThemeNames(themes []string, present map[string]bool) []string {
	out := make([]string, 0, len(themes))
	for _, theme := range themes {
		if present[theme] {
			out = append(out, theme)
		}
	}
	return out
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sourceEventIDs(values []string) []string {
	ids := make([]string, 0, len(values))
	for _, raw := range values {
		if eventID := leadingSourceEventID(raw); eventID != "" && !containsString(ids, eventID) {
			ids = append(ids, eventID)
		}
	}
	return ids
}

// NormalizeSourceEventReferences removes model-added descriptions from stable
// src-* references while preserving ordinary prose entries. The canonical
// form prevents a harmless "src-id: description" decoration from consuming
// content-repair attempts in later ownership audits.
func NormalizeSourceEventReferences(values []string) []string {
	if values == nil {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if eventID := leadingSourceEventID(value); eventID != "" {
			value = eventID
		}
		if value != "" && !containsString(normalized, value) {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func leadingSourceEventID(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "src-") {
		return ""
	}
	for index, char := range value {
		if char == '-' || char == '_' || char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' {
			continue
		}
		return strings.TrimSpace(value[:index])
	}
	return value
}

func allSourceEventIDs(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, raw := range values {
		if !strings.HasPrefix(strings.TrimSpace(raw), "src-") {
			return false
		}
	}
	return true
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func clipAdaptationText(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}
