package host

import "github.com/voocel/ainovel-cli/internal/domain"

func creativeBlueprintSummary(snap *UISnapshot) *CreativeBlueprintSummary {
	if snap == nil {
		return nil
	}
	loaded := snap.PremiseFull != "" ||
		len(snap.Outline) > 0 ||
		len(snap.CharacterDetails) > 0 ||
		len(snap.WorldRules) > 0 ||
		snap.CompassDirection != "" ||
		snap.CompassScale != ""
	if !loaded {
		return nil
	}
	return &CreativeBlueprintSummary{
		Loaded:           true,
		NovelName:        snap.NovelName,
		Premise:          snap.Premise,
		OutlineChapters:  len(snap.Outline),
		CharacterCount:   len(snap.CharacterDetails),
		WorldRuleCount:   len(snap.WorldRules),
		Layered:          snap.Layered,
		CompassDirection: snap.CompassDirection,
		CompassScale:     snap.CompassScale,
	}
}

func planningReviewSummary(review *domain.PlanningReview) *PlanningReviewSummary {
	if review == nil || review.Status == "" {
		return nil
	}
	return &PlanningReviewSummary{
		Loaded:                   true,
		Status:                   review.Status,
		Kind:                     review.Kind,
		Brief:                    review.Brief,
		TargetTotalWords:         review.TargetTotalWords,
		CreatedAt:                review.CreatedAt,
		UpdatedAt:                review.UpdatedAt,
		FoundationStatus:         review.FoundationStatus,
		FoundationRevision:       review.FoundationRevision,
		FoundationAuditSignature: review.FoundationAuditSignature,
		CoreCastSignature:        review.CoreCastSignature,
		FoundationGeneration:     review.FoundationGeneration,
		FoundationBaseRevision:   review.FoundationBaseRevision,
		FoundationFeedback:       review.FoundationFeedback,
		FoundationConfirmedAt:    review.FoundationConfirmedAt,
	}
}

func adaptationPlanSummary(plan *domain.AdaptationPlan) *AdaptationPlanSummary {
	if plan == nil {
		return nil
	}
	return &AdaptationPlanSummary{
		Loaded:            true,
		Status:            plan.Status,
		Granularity:       plan.Granularity,
		RewritePolicy:     plan.RewritePolicy,
		Brief:             plan.Brief,
		Volumes:           append([]domain.AdaptationVolumePlan(nil), plan.Volumes...),
		ChapterCount:      len(plan.Chapters),
		SourceTotalRunes:  plan.SourceTotalRunes,
		TargetTotalRunes:  plan.TargetTotalRunes,
		TargetMinRunes:    plan.TargetMinRunes,
		TargetMaxRunes:    plan.TargetMaxRunes,
		WordTolerance:     plan.WordTolerance,
		MainlineRules:     append([]string(nil), plan.MainlineRules...),
		RelationshipGoals: append([]string(nil), plan.RelationshipGoals...),
	}
}

func adaptationVolumeReviewSummary(review *domain.AdaptationVolumeReview) *AdaptationVolumeReviewSummary {
	if review == nil {
		return nil
	}
	return &AdaptationVolumeReviewSummary{
		Loaded:             true,
		Status:             review.Status,
		Granularity:        review.Granularity,
		RewritePolicy:      review.RewritePolicy,
		Brief:              review.Brief,
		Volumes:            append([]domain.AdaptationVolumePlan(nil), review.Volumes...),
		TargetChapterCount: review.TargetChapterCount,
		WordTolerance:      review.WordTolerance,
		MainlineRules:      append([]string(nil), review.MainlineRules...),
		RelationshipGoals:  append([]string(nil), review.RelationshipGoals...),
	}
}

func activeAdaptationChapters(plan, proposal *domain.AdaptationPlan) map[int]domain.AdaptationChapterPlan {
	active := plan
	if active == nil {
		active = proposal
	}
	if active == nil || len(active.Chapters) == 0 {
		return nil
	}
	chapters := make(map[int]domain.AdaptationChapterPlan, len(active.Chapters))
	for _, chapter := range active.Chapters {
		if chapter.Chapter > 0 {
			chapters[chapter.Chapter] = chapter
		}
	}
	return chapters
}

func outlineSnapshotFromEntry(entry domain.OutlineEntry, progress *domain.Progress, budget *domain.WordBudget, adaptation *domain.AdaptationChapterPlan) OutlineSnapshot {
	if adaptation != nil {
		entry = mergeAdaptationOutline(entry, *adaptation)
	}
	snap := OutlineSnapshot{
		Chapter:          entry.Chapter,
		Title:            entry.Title,
		CoreEvent:        entry.CoreEvent,
		Hook:             entry.Hook,
		Scenes:           append([]string(nil), entry.Scenes...),
		WrittenWordCount: writtenWordCount(progress, entry.Chapter),
	}
	if adaptation != nil {
		snap.SourceCoverage = sourceCoverageSnapshot(*adaptation)
		snap.PreserveEvents = append([]string(nil), adaptation.PreserveEvents...)
		snap.RequiredChanges = append([]string(nil), adaptation.RequiredChanges...)
		snap.ForbiddenMoves = append([]string(nil), adaptation.ForbiddenMoves...)
		snap.CoverageNote = adaptation.CoverageNote
	}
	snap.WordBudget = chapterBudgetSnapshot(progress, budget, adaptation)
	return snap
}

func outlineSnapshotsFromAdaptation(plan *domain.AdaptationPlan, progress *domain.Progress, budget *domain.WordBudget) []OutlineSnapshot {
	if plan == nil || len(plan.Chapters) == 0 {
		return nil
	}
	snapshots := make([]OutlineSnapshot, 0, len(plan.Chapters))
	for i := range plan.Chapters {
		chapter := &plan.Chapters[i]
		entry := chapter.OutlineEntry
		entry.Chapter = chapter.Chapter
		if entry.Title == "" {
			entry.Title = chapter.Title
		}
		snapshots = append(snapshots, outlineSnapshotFromEntry(entry, progress, budget, chapter))
	}
	return snapshots
}

func mergeAdaptationOutline(entry domain.OutlineEntry, adaptation domain.AdaptationChapterPlan) domain.OutlineEntry {
	if entry.Chapter == 0 {
		entry.Chapter = adaptation.Chapter
	}
	if entry.Title == "" {
		entry.Title = adaptation.Title
	}
	if entry.CoreEvent == "" {
		entry.CoreEvent = adaptation.CoreEvent
	}
	if entry.Hook == "" {
		entry.Hook = adaptation.Hook
	}
	if len(entry.Scenes) == 0 {
		entry.Scenes = append([]string(nil), adaptation.Scenes...)
	}
	return entry
}

func chapterBudgetSnapshot(progress *domain.Progress, budget *domain.WordBudget, adaptation *domain.AdaptationChapterPlan) *ChapterBudgetSnapshot {
	if adaptation != nil {
		snap := &ChapterBudgetSnapshot{
			SourceRunes: adaptation.SourceRunes,
			TargetRunes: adaptation.TargetRunes,
			MinRunes:    adaptation.TargetMinRunes,
			MaxRunes:    adaptation.TargetMaxRunes,
		}
		if adaptation.WordBudget != nil {
			snap.SourceRunes = firstPositive(snap.SourceRunes, adaptation.WordBudget.SourceRunes)
			snap.TargetRunes = firstPositive(snap.TargetRunes, adaptation.WordBudget.TargetRunes)
			snap.MinRunes = firstPositive(snap.MinRunes, adaptation.WordBudget.MinRunes)
			snap.MaxRunes = firstPositive(snap.MaxRunes, adaptation.WordBudget.MaxRunes)
			snap.Tolerance = adaptation.WordBudget.Tolerance
		}
		if snap.SourceRunes > 0 || snap.TargetRunes > 0 || snap.MinRunes > 0 || snap.MaxRunes > 0 {
			return snap
		}
	}
	if budget == nil {
		return nil
	}
	normalized := *budget
	if progress != nil && normalized.PlannedChapters <= 0 && progress.TotalChapters > 0 {
		normalized = normalized.WithPlannedChapters(progress.TotalChapters)
	}
	minWords, maxWords, ok := normalized.ChapterRange()
	if !ok {
		return nil
	}
	return &ChapterBudgetSnapshot{
		TargetWords: (minWords + maxWords) / 2,
		MinWords:    minWords,
		MaxWords:    maxWords,
	}
}

func sourceCoverageSnapshot(chapter domain.AdaptationChapterPlan) *SourceCoverageSnapshot {
	coverage := &SourceCoverageSnapshot{
		Chapters: append([]int(nil), chapter.SourceChapters...),
		From:     chapter.SourceRange.From,
		To:       chapter.SourceRange.To,
		Runes:    chapter.SourceRunes,
		IsAdded:  chapter.IsAdded,
		Note:     chapter.CoverageNote,
	}
	if coverage.From <= 0 || coverage.To <= 0 {
		coverage.From, coverage.To = chapterRange(coverage.Chapters)
	}
	if coverage.Runes <= 0 && chapter.WordBudget != nil {
		coverage.Runes = chapter.WordBudget.SourceRunes
	}
	if len(coverage.Chapters) == 0 && coverage.From <= 0 && coverage.To <= 0 && coverage.Runes <= 0 && !coverage.IsAdded && coverage.Note == "" {
		return nil
	}
	return coverage
}

func layeredVolumeSnapshots(volumes []domain.VolumeOutline) []LayeredVolumeSnapshot {
	if len(volumes) == 0 {
		return nil
	}
	out := make([]LayeredVolumeSnapshot, 0, len(volumes))
	for _, volume := range volumes {
		from, to, count := layeredVolumeChapterStats(volume)
		out = append(out, LayeredVolumeSnapshot{
			Index:        volume.Index,
			Title:        volume.Title,
			Theme:        volume.Theme,
			TargetFrom:   from,
			TargetTo:     to,
			ChapterCount: count,
		})
	}
	return out
}

func layeredVolumeChapterStats(volume domain.VolumeOutline) (int, int, int) {
	from, to, count := 0, 0, 0
	for _, arc := range volume.Arcs {
		if len(arc.Chapters) == 0 {
			count += arc.EstimatedChapters
			continue
		}
		for _, chapter := range arc.Chapters {
			if chapter.Chapter <= 0 {
				continue
			}
			count++
			if from == 0 || chapter.Chapter < from {
				from = chapter.Chapter
			}
			if chapter.Chapter > to {
				to = chapter.Chapter
			}
		}
	}
	return from, to, count
}

func writtenWordCount(progress *domain.Progress, chapter int) int {
	if progress == nil || progress.ChapterWordCounts == nil || chapter <= 0 {
		return 0
	}
	return progress.ChapterWordCounts[chapter]
}

func firstSignals(groups ...[]string) []string {
	const limit = 3
	signals := make([]string, 0, limit)
	for _, group := range groups {
		for _, value := range group {
			if value == "" {
				continue
			}
			signals = append(signals, value)
			if len(signals) == limit {
				return signals
			}
		}
	}
	return signals
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func chapterRange(chapters []int) (int, int) {
	if len(chapters) == 0 {
		return 0, 0
	}
	from, to := 0, 0
	for _, chapter := range chapters {
		if chapter <= 0 {
			continue
		}
		if from == 0 || chapter < from {
			from = chapter
		}
		if chapter > to {
			to = chapter
		}
	}
	return from, to
}
