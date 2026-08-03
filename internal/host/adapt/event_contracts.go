package adapt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// plannerDetailEventContractVersionPartitioned makes every stable source
// event belong to exactly one detail sub-batch inside a parent planner batch.
// The old shared-whitelist contract let neighboring sub-batches independently
// select the same supporting event, which delayed ownership failures until a
// later cross-batch audit.
const plannerDetailEventContractVersionPartitioned = 1

func sourceEventsFromReports(reports []domain.AdaptationSourceReport) []domain.AdaptationEvent {
	var events []domain.AdaptationEvent
	for index := range reports {
		report := reports[index]
		events = append(events, domain.EnsureAdaptationSourceEvents(&report)...)
	}
	return events
}

func mainlineSourceEventsInRange(reports []domain.AdaptationSourceReport, from, to int) []domain.AdaptationEvent {
	var events []domain.AdaptationEvent
	for _, event := range sourceEventsFromReports(reports) {
		if event.SourceChapter < from || event.SourceChapter > to || event.Importance != domain.AdaptationEventMainline {
			continue
		}
		event.Required = true
		events = append(events, event)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].SourceChapter != events[j].SourceChapter {
			return events[i].SourceChapter < events[j].SourceChapter
		}
		return events[i].ID < events[j].ID
	})
	return events
}

func compactPlannerSourceEvents(events []domain.AdaptationEvent, maxItems int) []domain.AdaptationEvent {
	if maxItems <= 0 || len(events) == 0 {
		return nil
	}
	if len(events) > maxItems {
		events = events[:maxItems]
	}
	out := make([]domain.AdaptationEvent, 0, len(events))
	for _, event := range events {
		event.Description = clipText(event.Description, 140)
		event.Evidence = clipText(event.Evidence, 140)
		event.DependsOn = append([]string(nil), event.DependsOn...)
		out = append(out, event)
	}
	return out
}

func attachSkeletonMainlineEvents(skeleton *plannerSkeleton, reports []domain.AdaptationSourceReport) {
	if skeleton == nil || domain.NormalizeAdaptationGranularity(skeleton.Granularity) != domain.AdaptationGranularityArc {
		return
	}
	for index := range skeleton.Batches {
		batch := &skeleton.Batches[index]
		batch.MainlineEventIDs = adaptationEventIDs(mainlineSourceEventsInRange(reports, batch.SourceFrom, batch.SourceTo))
		batch.AllowedEventIDs = adaptationEventIDs(sourceEventsInRange(reports, batch.SourceFrom, batch.SourceTo))
	}
}

func sourceEventsInRange(reports []domain.AdaptationSourceReport, from, to int) []domain.AdaptationEvent {
	var events []domain.AdaptationEvent
	for _, event := range sourceEventsFromReports(reports) {
		if event.SourceChapter < from || event.SourceChapter > to {
			continue
		}
		events = append(events, event)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].SourceChapter != events[j].SourceChapter {
			return events[i].SourceChapter < events[j].SourceChapter
		}
		return events[i].ID < events[j].ID
	})
	return events
}

func splitEventIDsForBatch(ids []string, partCount, partIndex int) []string {
	if len(ids) == 0 || partCount <= 0 || partIndex < 0 || partIndex >= partCount {
		return nil
	}
	start, end := splitSourceRuneShare(len(ids), partCount, partIndex)
	return append([]string(nil), ids[start:end]...)
}

func inheritPlannerRuntimeDetailEventContracts(skeleton *plannerSkeleton, outline *domain.AdaptationProposalRuntimeOutline) {
	if skeleton == nil || outline == nil {
		return
	}
	for index := range skeleton.Batches {
		batch := &skeleton.Batches[index]
		for _, persisted := range outline.Batches {
			if batch.Index != persisted.Index ||
				batch.TargetFrom != persisted.TargetFrom || batch.TargetTo != persisted.TargetTo ||
				batch.SourceFrom != persisted.SourceFrom || batch.SourceTo != persisted.SourceTo {
				continue
			}
			batch.DetailEventContractVersion = persisted.DetailEventContractVersion
			break
		}
	}
}

func enablePlannerDetailEventContractsForFreshRuntime(skeleton *plannerSkeleton, runtime *domain.AdaptationProposalRuntime) {
	if skeleton == nil || (runtime != nil && len(runtime.CompletedBatches) > 0) {
		return
	}
	for index := range skeleton.Batches {
		if skeleton.Batches[index].DetailEventContractVersion < plannerDetailEventContractVersionPartitioned {
			skeleton.Batches[index].DetailEventContractVersion = plannerDetailEventContractVersionPartitioned
		}
	}
}

func plannerDetailBatchEventContract(parent plannerSkeletonBatch, partCount, partIndex int) ([]string, []string) {
	mainline := splitEventIDsForBatch(parent.MainlineEventIDs, partCount, partIndex)
	mainlineSet := make(map[string]struct{}, len(parent.MainlineEventIDs))
	for _, eventID := range parent.MainlineEventIDs {
		if eventID = strings.TrimSpace(eventID); eventID != "" {
			mainlineSet[eventID] = struct{}{}
		}
	}
	optional := make([]string, 0, len(parent.AllowedEventIDs))
	for _, eventID := range parent.AllowedEventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID == "" {
			continue
		}
		if _, isMainline := mainlineSet[eventID]; !isMainline {
			optional = append(optional, eventID)
		}
	}
	optional = uniquePlannerEventIDs(optional)
	allowedSet := make(map[string]struct{}, len(mainline)+len(optional))
	for _, eventID := range mainline {
		if eventID = strings.TrimSpace(eventID); eventID != "" {
			allowedSet[eventID] = struct{}{}
		}
	}
	for _, eventID := range splitEventIDsForBatch(optional, partCount, partIndex) {
		if eventID = strings.TrimSpace(eventID); eventID != "" {
			allowedSet[eventID] = struct{}{}
		}
	}
	allowed := make([]string, 0, len(allowedSet))
	seen := make(map[string]struct{}, len(allowedSet))
	for _, eventID := range append(append([]string(nil), parent.AllowedEventIDs...), mainline...) {
		eventID = strings.TrimSpace(eventID)
		if eventID == "" {
			continue
		}
		if _, assigned := allowedSet[eventID]; !assigned {
			continue
		}
		if _, duplicate := seen[eventID]; duplicate {
			continue
		}
		seen[eventID] = struct{}{}
		allowed = append(allowed, eventID)
	}
	return uniquePlannerEventIDs(mainline), allowed
}

func uniquePlannerEventIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// plannerDetailBatchWithPriorEventOwnership keeps a legacy in-progress
// proposal consistent without silently accepting a duplicate. If an older
// completed detail batch already owns a stable event, the current batch sees
// that event as explicitly forbidden and any stale current audit receives a
// new contract signature. New proposals avoid this path through partitioned
// contracts, while this migration keeps existing projects resumable.
func plannerDetailBatchWithPriorEventOwnership(batch plannerSkeletonBatch, previous []domain.AdaptationChapterPlan) plannerSkeletonBatch {
	if len(previous) == 0 || (len(batch.AllowedEventIDs) == 0 && len(batch.MainlineEventIDs) == 0) {
		return batch
	}
	owned := make(map[string]int)
	for _, chapter := range previous {
		for _, eventID := range chapter.EventIDs {
			eventID = strings.TrimSpace(eventID)
			if eventID != "" {
				owned[eventID]++
			}
		}
	}
	forbidden := make(map[string]struct{})
	for _, eventID := range append(append([]string(nil), batch.AllowedEventIDs...), batch.MainlineEventIDs...) {
		eventID = strings.TrimSpace(eventID)
		if eventID != "" && owned[eventID] == 1 {
			forbidden[eventID] = struct{}{}
		}
	}
	if len(forbidden) == 0 {
		return batch
	}
	batch.PriorOwnedEventIDs = plannerEventIDsInOriginalOrder(append(batch.AllowedEventIDs, batch.MainlineEventIDs...), forbidden)
	batch.AllowedEventIDs = plannerEventIDsWithout(batch.AllowedEventIDs, forbidden)
	batch.MainlineEventIDs = plannerEventIDsWithout(batch.MainlineEventIDs, forbidden)
	return batch
}

func plannerEventIDsInOriginalOrder(values []string, wanted map[string]struct{}) []string {
	out := make([]string, 0, len(wanted))
	seen := make(map[string]struct{}, len(wanted))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, wanted := wanted[value]; !wanted {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func plannerEventIDsWithout(values []string, forbidden map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, blocked := forbidden[value]; blocked {
			continue
		}
		out = append(out, value)
	}
	return uniquePlannerEventIDs(out)
}

func validateArcBatchEventCoverage(chapters []domain.AdaptationChapterPlan, batch plannerSkeletonBatch) error {
	allowed := make(map[string]struct{}, len(batch.AllowedEventIDs)+len(batch.MainlineEventIDs))
	priorOwned := make(map[string]struct{}, len(batch.PriorOwnedEventIDs))
	for _, eventID := range batch.PriorOwnedEventIDs {
		if eventID = strings.TrimSpace(eventID); eventID != "" {
			priorOwned[eventID] = struct{}{}
		}
	}
	counts := make(map[string]int, len(batch.MainlineEventIDs))
	for _, eventID := range batch.MainlineEventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID != "" {
			allowed[eventID] = struct{}{}
		}
	}
	for _, eventID := range batch.AllowedEventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID != "" {
			allowed[eventID] = struct{}{}
		}
	}
	for _, chapter := range chapters {
		for _, rawEventID := range chapter.EventIDs {
			eventID := strings.TrimSpace(rawEventID)
			if eventID == "" {
				continue
			}
			if _, alreadyOwned := priorOwned[eventID]; alreadyOwned {
				return fmt.Errorf("arc source event %s is already owned by an earlier accepted detail batch; remove it from event_ids and preserve_events in target chapters %d-%d", eventID, batch.TargetFrom, batch.TargetTo)
			}
			if _, ok := allowed[eventID]; !ok {
				return fmt.Errorf("arc source event %s is not assigned to detail batch %d-%d; remove it from event_ids or use added_event_ids for a genuinely new target event", eventID, batch.TargetFrom, batch.TargetTo)
			}
			counts[eventID]++
		}
		for _, rawEventID := range chapter.PreserveEvents {
			eventID := strings.TrimSpace(rawEventID)
			if _, alreadyOwned := priorOwned[eventID]; alreadyOwned {
				return fmt.Errorf("arc source event %s is already owned by an earlier accepted detail batch; remove it from preserve_events and the matching target beat in chapters %d-%d", eventID, batch.TargetFrom, batch.TargetTo)
			}
		}
	}
	for _, eventID := range batch.MainlineEventIDs {
		switch counts[eventID] {
		case 1:
			continue
		case 0:
			return fmt.Errorf("arc mainline event %s is promised by the parent plan but missing from chapter event_ids", eventID)
		default:
			return fmt.Errorf("arc mainline event %s is assigned to %d detail chapters; assign it exactly once", eventID, counts[eventID])
		}
	}
	return nil
}

func finalizePlannerEventContracts(proposal *domain.AdaptationPlan, opts ProposalOptions, reports []domain.AdaptationSourceReport) error {
	if proposal == nil {
		return fmt.Errorf("planner proposal is nil")
	}
	proposal.ModePolicy = domain.AdaptationModePolicyForGranularity(opts.Granularity)
	proposal.Rules = domain.CompileAdaptationRules(opts.Brief, opts.Granularity)
	proposal.SourceEvents = sourceEventsFromReports(reports)
	switch domain.NormalizeAdaptationGranularity(opts.Granularity) {
	case domain.AdaptationGranularityArc:
		return validateArcProposalMainlineCoverage(proposal)
	case domain.AdaptationGranularityFree:
		buildFreeTargetEventLedger(proposal)
	}
	return nil
}

func validateArcProposalMainlineCoverage(proposal *domain.AdaptationPlan) error {
	counts := make(map[string]int)
	chaptersByEvent := make(map[string][]int)
	addedCount := 0
	for index := range proposal.Chapters {
		chapter := &proposal.Chapters[index]
		chapter.RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(proposal.Rules, proposal.Granularity, chapter.Chapter))
		for _, eventID := range chapter.EventIDs {
			eventID = strings.TrimSpace(eventID)
			counts[eventID]++
			chaptersByEvent[eventID] = append(chaptersByEvent[eventID], chapter.Chapter)
		}
		addedCount += len(chapter.AddedEventIDs)
	}
	var missing []string
	for _, event := range proposal.SourceEvents {
		if event.Importance != domain.AdaptationEventMainline || !event.Required {
			continue
		}
		switch counts[event.ID] {
		case 1:
			continue
		case 0:
			missing = append(missing, event.ID)
		default:
			return &arcMainlineBindingError{
				EventID:  event.ID,
				Count:    counts[event.ID],
				Chapters: append([]int(nil), chaptersByEvent[event.ID]...),
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		if addedCount > 0 {
			return fmt.Errorf("added_event_displaces_mainline: added events are planned while required mainline events are unassigned: %s", strings.Join(missing, ", "))
		}
		return fmt.Errorf("missing_mainline_plan_binding: volume/source promises are absent from chapter event_ids: %s", strings.Join(missing, ", "))
	}
	return nil
}

type arcMainlineBindingError struct {
	EventID  string
	Count    int
	Chapters []int
}

func (e *arcMainlineBindingError) Error() string {
	if e == nil {
		return "arc mainline event binding is invalid"
	}
	return fmt.Sprintf("arc mainline event %s is assigned %d times; mainline events must be bound exactly once", e.EventID, e.Count)
}

func buildFreeTargetEventLedger(proposal *domain.AdaptationPlan) {
	proposal.TargetEventLedger = nil
	seen := make(map[string]bool)
	for index := range proposal.Chapters {
		chapter := &proposal.Chapters[index]
		chapter.RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(proposal.Rules, proposal.Granularity, chapter.Chapter))
		if len(chapter.EventIDs) == 0 {
			chapter.EventIDs = []string{stableTargetEventID(chapter.Chapter, chapter.CoreEvent)}
		}
		added := make(map[string]bool, len(chapter.AddedEventIDs))
		for _, eventID := range chapter.AddedEventIDs {
			added[eventID] = true
		}
		for _, eventID := range chapter.EventIDs {
			if eventID == "" || seen[eventID] {
				continue
			}
			seen[eventID] = true
			origin := domain.AdaptationEventOriginTarget
			if added[eventID] {
				origin = domain.AdaptationEventOriginAdded
			}
			targetEvent := domain.AdaptationEvent{
				ID:          eventID,
				Description: firstNonEmptyString(chapter.CoreEvent, chapter.Title),
				Origin:      origin,
				Importance:  domain.AdaptationEventSupporting,
				DependsOn:   append([]string(nil), chapter.DependsOnEventIDs...),
			}
			if len(proposal.TargetEventLedger) == 0 || eventID == chapter.EventIDs[0] {
				targetEvent.Relationship = chapter.Relationship
				targetEvent.SettingClaims = append([]domain.AdaptationSettingClaim(nil), chapter.SettingClaims...)
			}
			proposal.TargetEventLedger = append(proposal.TargetEventLedger, targetEvent)
		}
	}
}

func stableTargetEventID(chapter int, description string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(description)))
	return fmt.Sprintf("tgt-%04d-%s", chapter, hex.EncodeToString(sum[:4]))
}
