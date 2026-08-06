package tools

import (
	"sort"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
)

func compactCharacterContractsForPlanningDetail(characters []domain.Character) []map[string]any {
	out := make([]map[string]any, 0, len(characters))
	for _, character := range characters {
		item := map[string]any{
			"id":          character.ID,
			"name":        character.Name,
			"aliases":     compactStringList(character.Aliases, 4, 25),
			"role":        truncateRunes(character.Role, 35),
			"tier":        character.Tier,
			"faction":     truncateRunes(character.Faction, 30),
			"description": truncateRunes(character.Description, 60),
			"traits":      compactStringList(character.Traits, 4, 35),
			"goal":        truncateRunes(character.Goal, 50),
			"motivation":  truncateRunes(character.Motivation, 50),
			"conflict":    truncateRunes(character.Conflict, 50),
			"arc":         truncateRunes(character.Arc, 70),
			"voice":       truncateRunes(character.Voice, 60),
			"constraints": compactStringList(character.Constraints, 3, 45),
		}
		if len(character.ContrastDetails) > 0 {
			contrasts := make([]map[string]string, 0, 1)
			for _, detail := range character.ContrastDetails[:1] {
				contrasts = append(contrasts, map[string]string{
					"surface": truncateRunes(detail.Surface, 60),
					"depth":   truncateRunes(detail.Depth, 70),
				})
			}
			item["contrast_details"] = contrasts
		}
		if len(character.KeyBackstory) > 0 {
			backstory := make([]map[string]string, 0, 1)
			for _, detail := range character.KeyBackstory[:1] {
				backstory = append(backstory, map[string]string{
					"event":  truncateRunes(detail.Event, 60),
					"impact": truncateRunes(detail.Impact, 70),
				})
			}
			item["key_backstory"] = backstory
		}
		if character.InitialState != nil {
			item["initial_state"] = map[string]any{
				"identity":      truncateRunes(character.InitialState.Identity, 40),
				"situation":     truncateRunes(character.InitialState.Situation, 50),
				"emotion":       truncateRunes(character.InitialState.Emotion, 40),
				"resources":     compactStringList(character.InitialState.Resources, 2, 40),
				"relationships": truncateRunes(character.InitialState.Relationships, 50),
			}
		}
		if character.KnowledgeBoundary != nil {
			item["knowledge_boundary"] = map[string]any{
				"known":          compactStringList(character.KnowledgeBoundary.Known, 1, 45),
				"unknown":        compactStringList(character.KnowledgeBoundary.Unknown, 1, 45),
				"misconceptions": compactStringList(character.KnowledgeBoundary.Misconceptions, 1, 45),
				"forbidden":      compactStringList(character.KnowledgeBoundary.Forbidden, 1, 45),
			}
		}
		out = append(out, item)
	}
	return out
}

func compactRelationshipsForPlanningDetail(
	relationships []domain.CharacterRelationship,
) []map[string]any {
	out := make([]map[string]any, 0, len(relationships))
	for _, relationship := range relationships {
		out = append(out, map[string]any{
			"id":                  relationship.ID,
			"source_character_id": relationship.SourceCharacterID,
			"target_character_id": relationship.TargetCharacterID,
			"type":                relationship.Type,
			"label":               truncateRunes(relationship.Label, 60),
			"direction":           relationship.Direction,
			"status":              relationship.Status,
			"description":         truncateRunes(relationship.Description, 70),
			"since":               truncateRunes(relationship.Since, 40),
			"tags":                compactStringList(relationship.Tags, 5, 35),
			"constraints":         compactStringList(relationship.Constraints, 3, 40),
		})
	}
	return out
}

func compactWorldRulesForPlanningDetail(rules []domain.WorldRule) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		out = append(out, map[string]any{
			"id":       rule.ID,
			"category": rule.Category,
			"strength": rule.Strength,
			"rule":     truncateRunes(rule.Rule, 30),
			"boundary": truncateRunes(rule.Boundary, 8),
			"tags":     compactStringList(rule.Tags, 4, 30),
		})
	}
	return out
}

func compactCharactersForPlanningReview(characters []domain.Character) []map[string]any {
	return compactCharacterContractsForPlanningReview(characters, nil)
}

func compactCharacterIndexForPlanningReview(characters []domain.Character) [][]any {
	out := make([][]any, 0, len(characters))
	for _, character := range characters {
		out = append(out, []any{
			character.ID,
			character.Name,
			truncateRunes(character.Role, 20),
			character.Tier,
			truncateRunes(character.Faction, 16),
		})
	}
	return out
}

func compactCharacterContractsForPlanningReview(
	characters []domain.Character,
	focused map[string]bool,
) []map[string]any {
	out := make([]map[string]any, 0, len(characters))
	for _, character := range characters {
		if focused != nil && !focused[character.ID] {
			continue
		}
		item := map[string]any{
			"id":          character.ID,
			"name":        character.Name,
			"role":        truncateRunes(character.Role, 35),
			"tier":        character.Tier,
			"faction":     truncateRunes(character.Faction, 25),
			"goal":        truncateRunes(character.Goal, 35),
			"motivation":  truncateRunes(character.Motivation, 35),
			"conflict":    truncateRunes(character.Conflict, 35),
			"arc":         truncateRunes(character.Arc, 40),
			"constraints": compactStringList(character.Constraints, 2, 28),
		}
		if character.KnowledgeBoundary != nil {
			item["knowledge_locks"] = map[string]any{
				"unknown":   compactStringList(character.KnowledgeBoundary.Unknown, 1, 30),
				"forbidden": compactStringList(character.KnowledgeBoundary.Forbidden, 1, 30),
			}
		}
		out = append(out, item)
	}
	return out
}

func compactWorldRulesForPlanningReview(rules []domain.WorldRule) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		out = append(out, map[string]any{
			"id":       rule.ID,
			"category": rule.Category,
			"strength": rule.Strength,
			"rule":     truncateRunes(rule.Rule, 30),
			"boundary": truncateRunes(rule.Boundary, 8),
		})
	}
	return out
}

func compactRelationshipsForPlanningAudit(
	relationships []domain.CharacterRelationship,
) [][]any {
	out := make([][]any, 0, len(relationships))
	for _, relationship := range relationships {
		out = append(out, []any{
			relationship.ID,
			relationship.SourceCharacterID,
			relationship.TargetCharacterID,
			relationship.Type,
			relationship.Direction,
			relationship.Status,
			truncateRunes(relationship.Label, 55),
			truncateRunes(relationship.Description, 60),
			truncateRunes(relationship.Since, 30),
			compactStringList(relationship.Tags, 4, 25),
			compactStringList(relationship.Constraints, 3, 35),
		})
	}
	return out
}

func compactWorldRulesForPlanningAudit(worldRules []domain.WorldRule) [][]any {
	out := make([][]any, 0, len(worldRules))
	for _, rule := range worldRules {
		out = append(out, []any{
			rule.ID,
			rule.Category,
			rule.Strength,
			truncateRunes(rule.Rule, 30),
			truncateRunes(rule.Boundary, 8),
		})
	}
	return out
}

const (
	maxContextOutlineTextRunes         = 120
	maxContextOutlineScenes            = 3
	maxContextChapterPlanTextRunes     = 300
	maxContextContractItems            = 10
	maxContextContractItemRunes        = 180
	maxContextCharacters               = 24
	maxContextCharacterTextRunes       = 220
	maxContextCharacterSnapshots       = 24
	maxContextRelationships            = 30
	maxContextStateChanges             = 30
	maxContextForeshadowEntries        = 30
	maxContextTimelineEvents           = 12
	maxContextSummaryItems             = 6
	maxContextSummaryRunes             = 180
	maxContextSummaryEventItems        = 6
	maxContextHistoryItems             = 12
	maxContextUserPreferencesRunes     = 4000
	maxContextUserRuleListItems        = 80
	maxContextAdaptationBriefRunes     = 1000
	maxContextAdaptationRuleItems      = 6
	maxContextSourceReports            = 6
	maxContextSourceReportItemRunes    = 90
	maxContextSourceReportSummaryRunes = 140
	maxContextPlanningVolumeSummaries  = 6
)

func compactOutlineEntry(entry domain.OutlineEntry) domain.OutlineEntry {
	entry.Title = truncateRunes(entry.Title, 80)
	entry.CoreEvent = truncateRunes(entry.CoreEvent, maxContextOutlineTextRunes)
	entry.Hook = truncateRunes(entry.Hook, maxContextOutlineTextRunes)
	entry.Scenes = compactStringList(entry.Scenes, maxContextOutlineScenes, maxContextOutlineTextRunes)
	return entry
}

func compactOutlineEntries(entries []domain.OutlineEntry) []domain.OutlineEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]domain.OutlineEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, compactOutlineEntry(entry))
	}
	return out
}

// compactOutlineEntriesForPlanningAudit keeps every structured fact needed by
// an editorial verdict while removing prose repetition between core_event,
// scenes and per-character beats. Unlike the nearby-outline projection it
// retains every scene, character beat and relationship beat in the requested
// window; only individual descriptions are bounded.
func compactOutlineEntriesForPlanningAudit(entries []domain.OutlineEntry) []map[string]any {
	if len(entries) == 0 {
		return nil
	}
	coreEventLimit, hookLimit, sceneLimit := 300, 160, 160
	temporaryRoleLimit, temporarySceneLimit, temporaryPurposeLimit := 50, 60, 80
	denseBatch := len(entries) >= 4
	if denseBatch {
		// Four fully structured chapters are the largest permitted audit
		// window. Keep every scene and beat, but use a tighter semantic
		// projection for prose that is already represented in those fields.
		// This reserves request headroom for the complete canonical
		// character/relationship/rule evidence rather than splitting an arc.
		coreEventLimit, hookLimit, sceneLimit = 120, 60, 75
		temporaryRoleLimit, temporarySceneLimit, temporaryPurposeLimit = 35, 40, 55
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id":          entry.ID,
			"chapter":     entry.Chapter,
			"title":       truncateRunes(entry.Title, 80),
			"core_event":  truncateRunes(entry.CoreEvent, coreEventLimit),
			"hook":        truncateRunes(entry.Hook, hookLimit),
			"scene_count": len(entry.Scenes),
			"scenes":      compactStringList(entry.Scenes, len(entry.Scenes), sceneLimit),
		}
		if len(entry.CharacterIDs) > 0 {
			item["character_ids"] = entry.CharacterIDs
		}
		if len(entry.CharacterBeats) > 0 {
			beats := make([][]any, 0, len(entry.CharacterBeats))
			for _, beat := range entry.CharacterBeats {
				beats = append(beats, compactPlanningAuditCharacterBeat(beat, denseBatch))
			}
			item["character_beats"] = beats
		}
		if len(entry.RelationshipBeats) > 0 {
			beats := make([][]any, 0, len(entry.RelationshipBeats))
			for _, beat := range entry.RelationshipBeats {
				beats = append(beats, compactPlanningAuditRelationshipBeat(beat, denseBatch))
			}
			item["relationship_beats"] = beats
		}
		if len(entry.TemporaryRoles) > 0 {
			roles := make([][]any, 0, len(entry.TemporaryRoles))
			for _, role := range entry.TemporaryRoles {
				roles = append(roles, []any{
					truncateRunes(role.Role, temporaryRoleLimit),
					truncateRunes(role.Scene, temporarySceneLimit),
					truncateRunes(role.Purpose, temporaryPurposeLimit),
					role.Important,
				})
			}
			item["temporary_roles"] = roles
		}
		if entry.DramaticFacts != nil {
			item["dramatic_facts"] = entry.DramaticFacts
		}
		out = append(out, item)
	}
	return out
}

func compactPlanningAuditCharacterBeat(beat domain.OutlineCharacterBeat, denseBatch bool) []any {
	sceneLimit, goalLimit, obstacleLimit, choiceLimit, advanceLimit := 60, 75, 75, 90, 90
	if denseBatch {
		sceneLimit, goalLimit, obstacleLimit, choiceLimit, advanceLimit = 25, 30, 30, 40, 40
	}
	return []any{
		beat.CharacterID,
		truncateRunes(beat.Scene, sceneLimit),
		truncateRunes(beat.Goal, goalLimit),
		truncateRunes(beat.Obstacle, obstacleLimit),
		truncateRunes(beat.ChoiceCost, choiceLimit),
		truncateRunes(beat.Advance, advanceLimit),
	}
}

func compactPlanningAuditRelationshipBeat(beat domain.OutlineRelationshipBeat, denseBatch bool) []any {
	sceneLimit, startLimit, advanceLimit, forbiddenLimit := 60, 60, 90, 80
	if denseBatch {
		sceneLimit, startLimit, advanceLimit, forbiddenLimit = 25, 25, 40, 35
	}
	return []any{
		beat.RelationshipID,
		beat.SourceCharacterID,
		beat.TargetCharacterID,
		truncateRunes(beat.Scene, sceneLimit),
		truncateRunes(beat.Start, startLimit),
		truncateRunes(beat.ExpectedAdvance, advanceLimit),
		truncateRunes(beat.ForbiddenJump, forbiddenLimit),
	}
}

func compactChapterPlan(plan domain.ChapterPlan) domain.ChapterPlan {
	plan.Title = truncateRunes(plan.Title, 80)
	plan.Goal = truncateRunes(plan.Goal, maxContextChapterPlanTextRunes)
	plan.Conflict = truncateRunes(plan.Conflict, maxContextChapterPlanTextRunes)
	plan.Hook = truncateRunes(plan.Hook, maxContextChapterPlanTextRunes)
	plan.EmotionArc = truncateRunes(plan.EmotionArc, maxContextChapterPlanTextRunes)
	plan.Notes = truncateRunes(plan.Notes, maxContextChapterPlanTextRunes)
	plan.Contract = compactChapterContract(plan.Contract)
	return plan
}

func compactChapterContract(contract domain.ChapterContract) domain.ChapterContract {
	contract.RequiredBeats = compactStringList(contract.RequiredBeats, maxContextContractItems, maxContextContractItemRunes)
	contract.ForbiddenMoves = compactStringList(contract.ForbiddenMoves, maxContextContractItems, maxContextContractItemRunes)
	contract.ContinuityChecks = compactStringList(contract.ContinuityChecks, maxContextContractItems, maxContextContractItemRunes)
	contract.EvaluationFocus = compactStringList(contract.EvaluationFocus, maxContextContractItems, maxContextContractItemRunes)
	contract.PayoffPoints = compactStringList(contract.PayoffPoints, maxContextContractItems, maxContextContractItemRunes)
	contract.EmotionTarget = truncateRunes(contract.EmotionTarget, maxContextContractItemRunes)
	contract.HookGoal = truncateRunes(contract.HookGoal, maxContextContractItemRunes)
	return contract
}

func compactAdaptationChapterPlan(plan domain.AdaptationChapterPlan) domain.AdaptationChapterPlan {
	plan.OutlineEntry = compactOutlineEntry(plan.OutlineEntry)
	plan.Title = truncateRunes(plan.Title, 80)
	plan.CoverageNote = truncateRunes(plan.CoverageNote, maxContextChapterPlanTextRunes)
	plan.PreserveEvents = compactStringList(plan.PreserveEvents, maxContextContractItems, maxContextContractItemRunes)
	plan.RequiredChanges = compactStringList(plan.RequiredChanges, maxContextContractItems, maxContextContractItemRunes)
	plan.ForbiddenMoves = compactStringList(plan.ForbiddenMoves, maxContextContractItems, maxContextContractItemRunes)
	return plan
}

func compactCharacters(chars []domain.Character, maxItems int) []domain.Character {
	if len(chars) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(chars), maxItems)
	out := make([]domain.Character, 0, limit)
	for _, c := range chars[:limit] {
		c.Role = truncateRunes(c.Role, 60)
		c.Description = truncateRunes(c.Description, 100)
		c.Arc = truncateRunes(c.Arc, 100)
		c.Traits = compactStringList(c.Traits, 4, 35)
		c.Aliases = compactStringList(c.Aliases, 5, 30)
		c.Goal = truncateRunes(c.Goal, 80)
		c.Motivation = truncateRunes(c.Motivation, 80)
		c.Conflict = truncateRunes(c.Conflict, 80)
		c.Voice = truncateRunes(c.Voice, 60)
		c.Constraints = compactStringList(c.Constraints, 3, 60)
		c.Notes = ""
		if len(c.ContrastDetails) > 1 {
			c.ContrastDetails = c.ContrastDetails[:1]
		}
		for idx := range c.ContrastDetails {
			c.ContrastDetails[idx].Surface = truncateRunes(c.ContrastDetails[idx].Surface, 60)
			c.ContrastDetails[idx].Depth = truncateRunes(c.ContrastDetails[idx].Depth, 70)
		}
		if len(c.KeyBackstory) > 1 {
			c.KeyBackstory = c.KeyBackstory[:1]
		}
		for idx := range c.KeyBackstory {
			c.KeyBackstory[idx].Event = truncateRunes(c.KeyBackstory[idx].Event, 60)
			c.KeyBackstory[idx].Impact = truncateRunes(c.KeyBackstory[idx].Impact, 70)
		}
		// Chapter-zero state is writer-owned context. The Architect receives
		// the role/goal/arc/constraints above plus compact knowledge locks.
		c.InitialState = nil
		if c.KnowledgeBoundary != nil {
			knowledge := *c.KnowledgeBoundary
			knowledge.Known = compactStringList(knowledge.Known, 1, 50)
			knowledge.Unknown = compactStringList(knowledge.Unknown, 1, 50)
			knowledge.Misconceptions = compactStringList(knowledge.Misconceptions, 1, 50)
			knowledge.Forbidden = compactStringList(knowledge.Forbidden, 1, 50)
			c.KnowledgeBoundary = &knowledge
		}
		out = append(out, c)
	}
	return out
}

func compactCharacterRelationships(
	relationships []domain.CharacterRelationship,
	maxItems int,
) []domain.CharacterRelationship {
	if len(relationships) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(relationships), maxItems)
	out := make([]domain.CharacterRelationship, 0, limit)
	for _, relationship := range relationships[:limit] {
		relationship.Label = truncateRunes(relationship.Label, 70)
		relationship.Description = truncateRunes(relationship.Description, 100)
		relationship.Since = truncateRunes(relationship.Since, 70)
		relationship.Tags = compactStringList(relationship.Tags, 5, 40)
		relationship.Constraints = compactStringList(relationship.Constraints, 3, 60)
		out = append(out, relationship)
	}
	return out
}

func compactCharacterSnapshots(snapshots []domain.CharacterSnapshot, maxItems int) []domain.CharacterSnapshot {
	if len(snapshots) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(snapshots)-maxItems, 0)
	out := make([]domain.CharacterSnapshot, 0, len(snapshots)-start)
	for _, snap := range snapshots[start:] {
		snap.Status = truncateRunes(snap.Status, maxContextCharacterTextRunes)
		snap.Power = truncateRunes(snap.Power, 120)
		snap.Motivation = truncateRunes(snap.Motivation, maxContextCharacterTextRunes)
		snap.Relations = truncateRunes(snap.Relations, maxContextCharacterTextRunes)
		out = append(out, snap)
	}
	return out
}

func compactRelationshipEntries(entries []domain.RelationshipEntry, currentChapter, maxItems int) []domain.RelationshipEntry {
	if len(entries) == 0 || maxItems <= 0 {
		return nil
	}
	var picked []domain.RelationshipEntry
	for i := len(entries) - 1; i >= 0 && len(picked) < maxItems; i-- {
		entry := entries[i]
		if currentChapter > 0 && entry.Chapter > currentChapter {
			continue
		}
		entry.Relation = truncateRunes(entry.Relation, maxContextContractItemRunes)
		picked = append(picked, entry)
	}
	reverseRelationshipEntries(picked)
	return picked
}

func compactStateChanges(changes []domain.StateChange, maxItems int) []domain.StateChange {
	if len(changes) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(changes), maxItems)
	out := make([]domain.StateChange, 0, limit)
	for _, change := range changes[:limit] {
		change.OldValue = truncateRunes(change.OldValue, 100)
		change.NewValue = truncateRunes(change.NewValue, maxContextContractItemRunes)
		change.Reason = truncateRunes(change.Reason, maxContextContractItemRunes)
		out = append(out, change)
	}
	return out
}

func compactForeshadowEntries(entries []domain.ForeshadowEntry, maxItems int) []domain.ForeshadowEntry {
	if len(entries) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(entries), maxItems)
	out := make([]domain.ForeshadowEntry, 0, limit)
	for _, entry := range entries[:limit] {
		entry.Description = truncateRunes(entry.Description, maxContextContractItemRunes)
		out = append(out, entry)
	}
	return out
}

func compactTimelineEvents(events []domain.TimelineEvent, maxItems int) []domain.TimelineEvent {
	if len(events) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(events)-maxItems, 0)
	out := make([]domain.TimelineEvent, 0, len(events)-start)
	for _, event := range events[start:] {
		event.Time = truncateRunes(event.Time, 80)
		event.Event = truncateRunes(event.Event, maxContextContractItemRunes)
		event.Characters = compactStringList(event.Characters, 8, 40)
		out = append(out, event)
	}
	return out
}

func compactChapterSummaries(summaries []domain.ChapterSummary) []domain.ChapterSummary {
	if len(summaries) == 0 {
		return nil
	}
	start := max(len(summaries)-maxContextSummaryItems, 0)
	out := make([]domain.ChapterSummary, 0, len(summaries)-start)
	for _, summary := range summaries[start:] {
		summary.Summary = truncateRunes(summary.Summary, maxContextSummaryRunes)
		summary.Characters = compactStringList(summary.Characters, 12, 40)
		summary.KeyEvents = compactStringList(summary.KeyEvents, maxContextSummaryEventItems, maxContextContractItemRunes)
		out = append(out, summary)
	}
	return out
}

func compactArcSummaries(summaries []domain.ArcSummary, maxItems int) []domain.ArcSummary {
	if len(summaries) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(summaries)-maxItems, 0)
	out := make([]domain.ArcSummary, 0, len(summaries)-start)
	for _, summary := range summaries[start:] {
		summary.Summary = truncateRunes(summary.Summary, maxContextSummaryRunes)
		summary.KeyEvents = compactStringList(summary.KeyEvents, maxContextSummaryEventItems, maxContextContractItemRunes)
		out = append(out, summary)
	}
	return out
}

func compactVolumeSummaries(summaries []domain.VolumeSummary, maxItems int) []domain.VolumeSummary {
	if len(summaries) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(summaries)-maxItems, 0)
	out := make([]domain.VolumeSummary, 0, len(summaries)-start)
	for _, summary := range summaries[start:] {
		summary.Summary = truncateRunes(summary.Summary, maxContextSummaryRunes)
		summary.KeyEvents = compactStringList(summary.KeyEvents, maxContextSummaryEventItems, maxContextContractItemRunes)
		out = append(out, summary)
	}
	return out
}

func compactWritingStyleRules(style *domain.WritingStyleRules) *domain.WritingStyleRules {
	if style == nil {
		return nil
	}
	out := *style
	out.Prose = compactStringList(out.Prose, 6, 90)
	out.Taboos = compactStringList(out.Taboos, 8, 90)
	out.Dialogue = compactCharacterVoices(out.Dialogue, 8)
	return &out
}

func compactCharacterVoices(voices []domain.CharacterVoice, maxItems int) []domain.CharacterVoice {
	if len(voices) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(voices), maxItems)
	out := make([]domain.CharacterVoice, 0, limit)
	for _, voice := range voices[:limit] {
		voice.Rules = compactStringList(voice.Rules, 4, 80)
		out = append(out, voice)
	}
	return out
}

func compactAdaptationPlanSummary(plan *domain.AdaptationPlan) map[string]any {
	if plan == nil {
		return nil
	}
	return map[string]any{
		"granularity":        plan.Granularity,
		"mode_policy":        plan.ModePolicy,
		"status":             plan.Status,
		"rewrite_policy":     plan.RewritePolicy,
		"word_tolerance":     adaptationWordToleranceForContext(plan),
		"mainline_rules":     compactStringList(plan.MainlineRules, maxContextAdaptationRuleItems, 120),
		"relationship_goals": compactStringList(plan.RelationshipGoals, maxContextAdaptationRuleItems, 120),
		"source_total_runes": plan.SourceTotalRunes,
		"target_total_runes": plan.TargetTotalRunes,
		"target_min_runes":   plan.TargetMinRunes,
		"target_max_runes":   plan.TargetMaxRunes,
	}
}

func compactLayeredOutlineForPlanning(volumes []domain.VolumeOutline, progress *domain.Progress) []map[string]any {
	if len(volumes) == 0 {
		return nil
	}
	focus := make(map[int]bool, 4)
	focus[volumes[0].Index] = true
	if progress != nil && progress.CurrentVolume > 0 {
		focus[progress.CurrentVolume-1] = true
		focus[progress.CurrentVolume] = true
		focus[progress.CurrentVolume+1] = true
	} else {
		focus[volumes[max(len(volumes)-2, 0)].Index] = true
		focus[volumes[len(volumes)-1].Index] = true
	}
	return compactLayeredOutlineWithFocus(volumes, progress, focus)
}

func compactLayeredOutlineForPlanningDetail(
	volumes []domain.VolumeOutline,
	targetVolume, targetArc int,
) []map[string]any {
	out := make([]map[string]any, 0, 3)
	globalChapter := 1
	for _, volume := range volumes {
		volumeCount := 0
		for _, arc := range volume.Arcs {
			count := len(arc.Chapters)
			if count == 0 {
				count = arc.EstimatedChapters
			}
			volumeCount += count
		}
		if volume.Index != targetVolume {
			if volume.Index == targetVolume-1 || volume.Index == targetVolume+1 {
				handoff := map[string]any{
					"index":         volume.Index,
					"title":         truncateRunes(volume.Title, 80),
					"theme":         truncateRunes(volume.Theme, 80),
					"chapter_count": volumeCount,
					"handoff_only":  true,
				}
				if len(volume.Arcs) > 0 {
					edge := volume.Arcs[0]
					if volume.Index < targetVolume {
						edge = volume.Arcs[len(volume.Arcs)-1]
					}
					handoff["edge_arc"] = map[string]any{
						"index": edge.Index,
						"title": truncateRunes(edge.Title, 60),
						"goal":  truncateRunes(edge.Goal, 80),
					}
				}
				out = append(out, handoff)
			}
			globalChapter += volumeCount
			continue
		}
		payload := map[string]any{
			"index": volume.Index,
			"title": truncateRunes(volume.Title, 100),
			"theme": truncateRunes(volume.Theme, 140),
		}
		arcs := make([]map[string]any, 0, len(volume.Arcs))
		for _, arc := range volume.Arcs {
			count := len(arc.Chapters)
			if count == 0 {
				count = arc.EstimatedChapters
			}
			from := globalChapter
			to := globalChapter + max(count-1, 0)
			goalLimit := 50
			if arc.Index == targetArc {
				goalLimit = 180
			}
			arcPayload := map[string]any{
				"index":         arc.Index,
				"title":         truncateRunes(arc.Title, 80),
				"goal":          truncateRunes(arc.Goal, goalLimit),
				"from":          from,
				"to":            to,
				"chapter_count": count,
				"expanded":      arc.IsExpanded(),
				"target":        arc.Index == targetArc,
			}
			if arc.IsExpanded() {
				if arc.Index == targetArc {
					arcPayload["chapters"] = compactOutlineEntries(arc.Chapters)
				} else if arc.Index == targetArc-1 {
					arcPayload["chapter_evidence"] = compactCompletedArcForPlanningDetail(arc.Chapters)
					arcPayload["chapter_evidence_schema"] = []string{
						"id", "chapter", "title", "core_event", "hook",
						"character_ids", "relationship_advances",
					}
				} else {
					arcPayload["chapter_index"] = compactCompletedArcChapterIndex(arc.Chapters)
					arcPayload["chapter_index_schema"] = []string{"id", "chapter", "title"}
				}
			}
			arcs = append(arcs, arcPayload)
			globalChapter += count
		}
		payload["arcs"] = arcs
		out = append(out, payload)
	}
	return out
}

func compactCompletedArcChapterIndex(entries []domain.OutlineEntry) [][]any {
	out := make([][]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, []any{entry.ID, entry.Chapter, truncateRunes(entry.Title, 60)})
	}
	return out
}

func compactCompletedArcForPlanningDetail(entries []domain.OutlineEntry) [][]any {
	out := make([][]any, 0, len(entries))
	for _, entry := range entries {
		relationshipAdvances := make([][]string, 0, len(entry.RelationshipBeats))
		for _, beat := range entry.RelationshipBeats {
			relationshipAdvances = append(relationshipAdvances, []string{
				beat.RelationshipID,
				truncateRunes(beat.ExpectedAdvance, 55),
			})
		}
		out = append(out, []any{
			entry.ID,
			entry.Chapter,
			truncateRunes(entry.Title, 60),
			truncateRunes(entry.CoreEvent, 220),
			truncateRunes(entry.Hook, 110),
			entry.CharacterIDs,
			relationshipAdvances,
		})
	}
	return out
}

func compactVolumeHistoryIndex(volumes []domain.VolumeOutline) [][]any {
	out := make([][]any, 0, len(volumes))
	for _, volume := range volumes {
		chapterCount := 0
		for _, arc := range volume.Arcs {
			if arc.IsExpanded() {
				chapterCount += len(arc.Chapters)
			} else {
				chapterCount += arc.EstimatedChapters
			}
		}
		out = append(out, []any{
			volume.Index,
			truncateRunes(volume.Title, 12),
			chapterCount,
			len(volume.Arcs),
		})
	}
	return out
}

func compactVolumeThemeMilestones(volumes []domain.VolumeOutline) [][]any {
	out := make([][]any, 0, len(volumes)/10+2)
	for index, volume := range volumes {
		if index != 0 && index != len(volumes)-1 && (index+1)%10 != 0 {
			continue
		}
		out = append(out, []any{volume.Index, truncateRunes(volume.Theme, 25)})
	}
	return out
}

func compactLayeredOutlineForPlanningReviewVolumes(
	volumes []domain.VolumeOutline,
	progress *domain.Progress,
	fromVolume int,
	toVolume int,
) []map[string]any {
	selected := make(map[int]bool, max(toVolume-fromVolume+1, 1))
	if fromVolume == 0 && toVolume == 0 {
		// Whole-book review consumes the already-passed batch reports supplied
		// by Host. Keep the opening and ending books detailed and represent the
		// complete middle as a stable index.
		if len(volumes) > 0 {
			selected[volumes[0].Index] = true
			selected[volumes[len(volumes)-1].Index] = true
		}
	} else {
		for index := fromVolume; index <= toVolume; index++ {
			selected[index] = true
		}
	}
	included := includeAdjacentVolumes(volumes, selected)
	return compactLayeredOutlineWithSelection(volumes, progress, included, selected)
}

func compactLayeredOutlineForPlanningVolume(
	volumes []domain.VolumeOutline,
	progress *domain.Progress,
	targetVolume int,
) []map[string]any {
	selected := map[int]bool{targetVolume: true}
	included := includeAdjacentVolumes(volumes, selected)
	return compactLayeredOutlineWithSelection(volumes, progress, included, selected)
}

func includeAdjacentVolumes(volumes []domain.VolumeOutline, selected map[int]bool) map[int]bool {
	included := make(map[int]bool, len(selected)+2)
	available := make(map[int]bool, len(volumes))
	for _, volume := range volumes {
		available[volume.Index] = true
	}
	for volume := range selected {
		if available[volume] {
			included[volume] = true
		}
		if available[volume-1] {
			included[volume-1] = true
		}
		if available[volume+1] {
			included[volume+1] = true
		}
	}
	return included
}

func compactLayeredOutlineWithFocus(
	volumes []domain.VolumeOutline,
	progress *domain.Progress,
	focus map[int]bool,
) []map[string]any {
	return compactLayeredOutlineWithSelection(volumes, progress, focus, nil)
}

func compactLayeredOutlineWithSelection(
	volumes []domain.VolumeOutline,
	progress *domain.Progress,
	included map[int]bool,
	selected map[int]bool,
) []map[string]any {
	out := make([]map[string]any, 0, len(included))
	globalChapter := 1
	currentChapter := 0
	currentVolume := 0
	currentArc := 0
	if progress != nil {
		currentChapter = progress.CurrentChapter
		if progress.InProgressChapter > 0 {
			currentChapter = progress.InProgressChapter
		}
		currentVolume = progress.CurrentVolume
		currentArc = progress.CurrentArc
	}

	for _, volume := range volumes {
		volumeChapterCount := 0
		for _, arc := range volume.Arcs {
			if arc.IsExpanded() {
				volumeChapterCount += len(arc.Chapters)
			} else {
				volumeChapterCount += arc.EstimatedChapters
			}
		}
		if !included[volume.Index] {
			globalChapter += volumeChapterCount
			continue
		}
		title := truncateRunes(volume.Title, 80)
		theme := truncateRunes(volume.Theme, maxContextSummaryRunes)
		if selected[volume.Index] {
			title = volume.Title
			theme = volume.Theme
		}
		volumePayload := map[string]any{
			"index": volume.Index,
			"title": title,
			"theme": theme,
		}
		arcs := make([]map[string]any, 0, len(volume.Arcs))
		for _, arc := range volume.Arcs {
			chapterCount := len(arc.Chapters)
			if chapterCount == 0 {
				chapterCount = arc.EstimatedChapters
			}
			arcStart := globalChapter
			arcEnd := globalChapter + max(chapterCount-1, 0)
			arcTitle := truncateRunes(arc.Title, 40)
			arcGoal := truncateRunes(arc.Goal, 30)
			if selected[volume.Index] {
				arcTitle = arc.Title
				arcGoal = arc.Goal
			} else if volume.Index == currentVolume && arc.Index == currentArc {
				arcGoal = truncateRunes(arc.Goal, maxContextSummaryRunes)
			}
			arcPayload := map[string]any{
				"index":         arc.Index,
				"title":         arcTitle,
				"goal":          arcGoal,
				"from":          arcStart,
				"to":            arcEnd,
				"chapter_count": chapterCount,
				"expanded":      arc.IsExpanded(),
			}
			if arc.EstimatedChapters > 0 && !arc.IsExpanded() {
				arcPayload["estimated_chapters"] = arc.EstimatedChapters
			}
			if arc.IsExpanded() && volume.Index == currentVolume && arc.Index == currentArc {
				from := max(currentChapter-1, arcStart)
				to := min(currentChapter+1, arcEnd)
				chapters := make([]domain.OutlineEntry, 0, len(arc.Chapters))
				chapterNo := arcStart
				for _, entry := range arc.Chapters {
					entry.Chapter = chapterNo
					chapters = append(chapters, entry)
					chapterNo++
				}
				arcPayload["nearby_chapters"] = compactOutlineEntries(outlineEntriesInRange(chapters, from, to))
			}
			arcs = append(arcs, arcPayload)
			globalChapter += chapterCount
		}
		volumePayload["arcs"] = arcs
		out = append(out, volumePayload)
	}
	return out
}

func compactSourceReportsForContext(reports []domain.AdaptationSourceReport, refs []int) []domain.AdaptationSourceReport {
	if len(reports) == 0 || len(refs) == 0 {
		return nil
	}
	want := make(map[int]struct{}, len(refs))
	for _, ref := range refs {
		want[ref] = struct{}{}
	}
	out := make([]domain.AdaptationSourceReport, 0, min(len(refs), maxContextSourceReports))
	for _, report := range reports {
		if _, ok := want[report.Chapter]; !ok {
			continue
		}
		out = append(out, compactSourceReport(report))
		if len(out) >= maxContextSourceReports {
			break
		}
	}
	return out
}

func compactSourceReport(report domain.AdaptationSourceReport) domain.AdaptationSourceReport {
	report.Summary = truncateRunes(report.Summary, maxContextSourceReportSummaryRunes)
	report.Characters = compactStringList(report.Characters, 8, 40)
	report.CharacterFacts = compactStringList(report.CharacterFacts, 3, maxContextSourceReportItemRunes)
	report.KeyEvents = compactStringList(report.KeyEvents, 3, maxContextSourceReportItemRunes)
	report.WorldRules = compactStringList(report.WorldRules, 2, maxContextSourceReportItemRunes)
	report.Timeline = nil
	report.Foreshadow = nil
	report.Relationships = nil
	report.StateChanges = nil
	return report
}

func compactReviewIssues(issues []domain.ConsistencyIssue, maxItems int) []domain.ConsistencyIssue {
	if len(issues) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(issues), maxItems)
	out := make([]domain.ConsistencyIssue, 0, limit)
	for _, issue := range issues[:limit] {
		issue.Description = truncateRunes(issue.Description, maxContextContractItemRunes)
		issue.Evidence = truncateRunes(issue.Evidence, maxContextContractItemRunes)
		issue.Suggestion = truncateRunes(issue.Suggestion, maxContextContractItemRunes)
		out = append(out, issue)
	}
	return out
}

func compactForeshadowUpdates(entries []domain.ForeshadowUpdate, maxItems int) []domain.ForeshadowUpdate {
	if len(entries) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(entries), maxItems)
	out := make([]domain.ForeshadowUpdate, 0, limit)
	for _, entry := range entries[:limit] {
		entry.Description = truncateRunes(entry.Description, maxContextSourceReportItemRunes)
		out = append(out, entry)
	}
	return out
}

func compactUserRulesPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	if prefs, ok := out["preferences"].(string); ok {
		out["preferences"] = truncateRunes(prefs, maxContextUserPreferencesRunes)
	}
	if structured, ok := out["structured"].(rules.Structured); ok {
		out["structured"] = compactStructuredRules(structured)
	}
	return out
}

func compactStructuredRules(structured rules.Structured) rules.Structured {
	structured.ForbiddenChars = compactStringList(structured.ForbiddenChars, maxContextUserRuleListItems, 20)
	structured.ForbiddenPhrases = compactStringList(structured.ForbiddenPhrases, maxContextUserRuleListItems, 80)
	structured.FatigueWords = compactFatigueWords(structured.FatigueWords, maxContextUserRuleListItems)
	return structured
}

func compactFatigueWords(words map[string]int, maxItems int) map[string]int {
	if len(words) == 0 || maxItems <= 0 {
		return nil
	}
	keys := make([]string, 0, len(words))
	for key := range words {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maxItems {
		keys = keys[:maxItems]
	}
	out := make(map[string]int, len(keys))
	for _, key := range keys {
		out[truncateRunes(key, 30)] = words[key]
	}
	return out
}

func compactStringList(items []string, maxItems, maxRunes int) []string {
	if len(items) == 0 || maxItems <= 0 {
		return nil
	}
	limit := min(len(items), maxItems)
	out := make([]string, 0, limit)
	for _, item := range items[:limit] {
		out = append(out, truncateRunes(item, maxRunes))
	}
	return out
}

func compactRecentStrings(items []string, maxItems int) []string {
	if len(items) == 0 || maxItems <= 0 {
		return nil
	}
	start := max(len(items)-maxItems, 0)
	out := make([]string, len(items)-start)
	copy(out, items[start:])
	return out
}

func reverseRelationshipEntries(entries []domain.RelationshipEntry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}
