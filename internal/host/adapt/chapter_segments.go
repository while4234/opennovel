package adapt

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func buildChapterSegmentPlans(
	report domain.AdaptationSourceReport,
	events []domain.AdaptationEvent,
	opts ProposalOptions,
	sourceRunesByChapter map[int]int,
	rules []domain.AdaptationRule,
	firstTargetChapter int,
) []domain.AdaptationChapterPlan {
	sourceRunes := sourceRunesForReport(report, sourceRunesByChapter)
	segmentCount := domain.MinimumAdaptationSourceSegmentCount(sourceRunes)
	if segmentCount <= 0 {
		segmentCount = 1
	}
	segments := make([]domain.AdaptationSourceSegment, 0, segmentCount)
	plans := make([]domain.AdaptationChapterPlan, 0, segmentCount)
	entryStates, exitStates := sourceSegmentBoundaryStates(report, segmentCount)
	for index := 0; index < segmentCount; index++ {
		start, end := splitSourceRuneShare(sourceRunes, segmentCount, index)
		ownedEvents, preserveEvents := sourceEventsForSegment(report, events, segmentCount, index)
		segment := domain.AdaptationSourceSegment{
			SourceChapter: report.Chapter,
			Sequence:      index + 1,
			EventIDs:      ownedEvents,
			RuneShare: domain.AdaptationSourceRuneShare{
				Start: start,
				End:   end,
			},
			EntryState:     entryStates[index],
			ExitState:      exitStates[index],
			BoundaryReason: sourceSegmentBoundaryReason(segmentCount, index),
		}
		segments = append(segments, segment)

		targetChapter := firstTargetChapter + index
		plan := buildChapterPlan(report, opts, sourceRunesByChapter)
		plan.Chapter = targetChapter
		plan.OutlineEntry.Chapter = targetChapter
		plan.Title = sourceSegmentTitle(report.Title, segmentCount, index)
		plan.OutlineEntry.Title = plan.Title
		plan.SourceRunes = end - start
		plan.TargetRunes = plan.SourceRunes
		plan.TargetMinRunes, plan.TargetMaxRunes = runeRange(plan.TargetRunes, opts.WordTolerance)
		if plan.TargetMaxRunes > domain.AdaptationModelChapterMaxRunes {
			plan.TargetMaxRunes = domain.AdaptationModelChapterMaxRunes
		}
		plan.WordBudget = &domain.AdaptationChapterWordBudget{
			SourceRunes: plan.SourceRunes,
			TargetRunes: plan.TargetRunes,
			MinRunes:    plan.TargetMinRunes,
			MaxRunes:    plan.TargetMaxRunes,
			Tolerance:   opts.WordTolerance,
		}
		plan.SourceSegments = []domain.AdaptationSourceSegment{segment}
		plan.EventIDs = append([]string(nil), ownedEvents...)
		plan.PreserveEvents = preserveEvents
		plan.RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(rules, opts.Granularity, targetChapter))
		plan.CoverageNote = fmt.Sprintf(
			"source chapter %d segment %d/%d owns rune-accounting share [%d,%d); use complete scene boundaries, not a character-level hard cut",
			report.Chapter,
			index+1,
			segmentCount,
			start,
			end,
		)
		plans = append(plans, plan)
	}
	// Keep one writing responsibility per target chapter. Auditors reconstruct
	// the full ordered list across plans, while Writer sees only its own segment.
	for index := range plans {
		plans[index].SourceSegments = []domain.AdaptationSourceSegment{segments[index]}
	}
	return plans
}

func sourceSegmentBoundaryStates(
	report domain.AdaptationSourceReport,
	segmentCount int,
) ([]domain.AdaptationSegmentState, []domain.AdaptationSegmentState) {
	entries := make([]domain.AdaptationSegmentState, segmentCount)
	exits := make([]domain.AdaptationSegmentState, segmentCount)
	current := domain.AdaptationSegmentState{}
	for index := 0; index < segmentCount; index++ {
		entries[index] = cloneAdaptationSegmentState(current)
		for changeIndex, change := range report.StateChanges {
			if changeIndex*segmentCount/max(1, len(report.StateChanges)) != index {
				continue
			}
			key := "state:" + strings.TrimSpace(change.Entity) + ":" + strings.TrimSpace(change.Field)
			if strings.Trim(key, ":") != "state" {
				current[key] = strings.TrimSpace(change.NewValue)
			}
		}
		for relationIndex, relation := range report.Relationships {
			if relationIndex*segmentCount/max(1, len(report.Relationships)) != index {
				continue
			}
			left, right := strings.TrimSpace(relation.CharacterA), strings.TrimSpace(relation.CharacterB)
			if left == "" || right == "" {
				continue
			}
			if left > right {
				left, right = right, left
			}
			current["relationship:"+left+"|"+right] = strings.TrimSpace(relation.Relation)
		}
		exits[index] = cloneAdaptationSegmentState(current)
	}
	return entries, exits
}

func cloneAdaptationSegmentState(state domain.AdaptationSegmentState) domain.AdaptationSegmentState {
	out := make(domain.AdaptationSegmentState, len(state))
	for key, value := range state {
		out[key] = value
	}
	return out
}

func splitSourceRuneShare(total, count, index int) (int, int) {
	if total <= 0 || count <= 0 || index < 0 || index >= count {
		return 0, 0
	}
	base := total / count
	remainder := total % count
	start := index*base + min(index, remainder)
	length := base
	if index < remainder {
		length++
	}
	return start, start + length
}

func sourceEventsForSegment(
	report domain.AdaptationSourceReport,
	events []domain.AdaptationEvent,
	segmentCount int,
	segmentIndex int,
) ([]string, []string) {
	if segmentCount <= 0 || segmentIndex < 0 || segmentIndex >= segmentCount {
		return nil, nil
	}
	var ids []string
	var descriptions []string
	for eventIndex, event := range events {
		owner := eventIndex * segmentCount / len(events)
		if owner != segmentIndex {
			continue
		}
		ids = append(ids, event.ID)
		descriptions = append(descriptions, event.Description)
	}
	if len(ids) > 0 {
		return ids, descriptions
	}
	// The analyzer may emit fewer event labels than a very long chapter needs
	// target chapters. A stable coverage responsibility keeps segment ownership
	// explicit without pretending the rune boundary is a prose cut point.
	id := fmt.Sprintf("src-%04d-segment-%02d", report.Chapter, segmentIndex+1)
	description := fmt.Sprintf("完整承接来源章第 %d/%d 个场景分段的细节与状态变化", segmentIndex+1, segmentCount)
	return []string{id}, []string{description}
}

func sourceSegmentBoundaryReason(segmentCount, segmentIndex int) string {
	if segmentCount <= 1 {
		return "single source chapter responsibility"
	}
	if segmentIndex == segmentCount-1 {
		return "final complete scene and chapter exit state"
	}
	return "complete scene transition or conflict-stage boundary"
}

func sourceSegmentTitle(title string, segmentCount, segmentIndex int) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "来源章节"
	}
	if segmentCount <= 1 {
		return title
	}
	return fmt.Sprintf("%s（%d）", title, segmentIndex+1)
}

func adaptationEventIDs(events []domain.AdaptationEvent) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if strings.TrimSpace(event.ID) != "" {
			ids = append(ids, event.ID)
		}
	}
	return ids
}
