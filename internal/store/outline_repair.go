package store

import (
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const outlineRepairWindowChapterCount = 3

// OutlineRepairBatch describes the smallest durable outline window that can be
// repaired before writing resumes.
type OutlineRepairBatch struct {
	Volume            int
	Arc               int
	FromChapter       int
	ToChapter         int
	ChapterCount      int
	Duplicate         domain.OutlineDuplicate
	CompletedChapters []int
}

func (b *OutlineRepairBatch) Repairable() bool {
	return b != nil && b.Volume > 0 && b.Arc > 0 && b.FromChapter > 0 && b.ToChapter >= b.FromChapter
}

// FindDuplicateOutlineRepairBatch locates the first duplicated outline promise
// within a generated batch and maps the later duplicate chapter to a small
// repair window inside the expanded layered arc.
func (s *Store) FindDuplicateOutlineRepairBatch(progress *domain.Progress) (*OutlineRepairBatch, error) {
	var batch *OutlineRepairBatch
	err := s.Revisions.withLegacyMigrationMutation("recover or checkpoint outline repair", s.Outline.migration, func() error {
		recoveredProgress, recoverErr := s.completePendingOutlineRepairFinalization(progress)
		if recoverErr != nil {
			return recoverErr
		}
		progress = recoveredProgress
		if s.outlineDuplicateScanCurrent(progress) {
			return nil
		}
		var scanErr error
		batch, scanErr = s.scanDuplicateOutlineRepairBatch(progress)
		if scanErr != nil || batch != nil {
			return scanErr
		}
		if err := s.saveCleanOutlineDuplicateScan(progress, 0, 0); err != nil {
			return err
		}
		return nil
	})
	return batch, err
}

func (s *Store) scanDuplicateOutlineRepairBatch(progress *domain.Progress) (*OutlineRepairBatch, error) {
	if progress != nil && progress.Layered {
		volumes, err := s.Outline.LoadLayeredOutline()
		if err != nil {
			return nil, err
		}
		if len(volumes) > 0 {
			return s.duplicateOutlineRepairBatchInLayered(progress, volumes), nil
		}
	}

	entries, err := s.Outline.LoadOutline()
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	duplicate, ok := domain.FindDuplicateOutlineEntries(entries)
	if !ok {
		return nil, nil
	}
	return &OutlineRepairBatch{Duplicate: duplicate}, nil
}

func (s *Store) duplicateOutlineRepairBatchInLayered(progress *domain.Progress, volumes []domain.VolumeOutline) *OutlineRepairBatch {
	if s.Adaptation.Active() {
		return s.duplicateOutlineRepairBatchInAdaptationVolumes(progress, volumes)
	}
	return duplicateOutlineRepairBatchInLayeredArcs(progress, volumes)
}

func duplicateOutlineRepairBatchInLayeredArcs(progress *domain.Progress, volumes []domain.VolumeOutline) *OutlineRepairBatch {
	globalChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			arcLen := arcOutlineChapterCount(arc)
			from := globalChapter
			to := globalChapter + arcLen - 1
			if len(arc.Chapters) > 0 {
				entries := numberedOutlineEntries(arc.Chapters, from)
				duplicate, ok := domain.FindDuplicateOutlineEntries(entries)
				if ok {
					windowFrom, windowTo := outlineRepairWindow(from, to, duplicate.Chapter)
					var completed []int
					if progress != nil {
						completed = completedChaptersInRange(progress.CompletedChapters, windowFrom, windowTo)
					}
					return &OutlineRepairBatch{
						Volume:            volume.Index,
						Arc:               arc.Index,
						FromChapter:       windowFrom,
						ToChapter:         windowTo,
						ChapterCount:      windowTo - windowFrom + 1,
						Duplicate:         duplicate,
						CompletedChapters: completed,
					}
				}
			}
			globalChapter += arcLen
		}
	}
	return nil
}

func (s *Store) duplicateOutlineRepairBatchInAdaptationVolumes(progress *domain.Progress, volumes []domain.VolumeOutline) *OutlineRepairBatch {
	globalChapter := 1
	for _, volume := range volumes {
		var entries []domain.OutlineEntry
		for _, arc := range volume.Arcs {
			arcLen := arcOutlineChapterCount(arc)
			if len(arc.Chapters) > 0 {
				entries = append(entries, numberedOutlineEntries(arc.Chapters, globalChapter)...)
			}
			globalChapter += arcLen
		}
		if len(entries) == 0 {
			continue
		}
		duplicate, ok := domain.FindDuplicateOutlineEntries(entries)
		if !ok {
			continue
		}
		batch, err := s.layeredRepairBatchForDuplicate(progress, duplicate)
		if err == nil && batch != nil {
			return batch
		}
		return &OutlineRepairBatch{Duplicate: duplicate}
	}
	return nil
}

func (s *Store) layeredRepairBatchForDuplicate(progress *domain.Progress, duplicate domain.OutlineDuplicate) (*OutlineRepairBatch, error) {
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}

	globalChapter := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			arcLen := arcOutlineChapterCount(arc)
			if len(arc.Chapters) == 0 {
				globalChapter += arcLen
				continue
			}
			from := globalChapter
			to := globalChapter + arcLen - 1
			if duplicate.Chapter >= from && duplicate.Chapter <= to {
				windowFrom, windowTo := outlineRepairWindow(from, to, duplicate.Chapter)
				return &OutlineRepairBatch{
					Volume:            volume.Index,
					Arc:               arc.Index,
					FromChapter:       windowFrom,
					ToChapter:         windowTo,
					ChapterCount:      windowTo - windowFrom + 1,
					Duplicate:         duplicate,
					CompletedChapters: completedChaptersInRange(progress.CompletedChapters, windowFrom, windowTo),
				}, nil
			}
			globalChapter += arcLen
		}
	}

	return &OutlineRepairBatch{Duplicate: duplicate}, nil
}

func outlineRepairWindow(arcFrom, arcTo, duplicateChapter int) (int, int) {
	if arcFrom <= 0 || arcTo < arcFrom {
		return arcFrom, arcTo
	}
	if duplicateChapter < arcFrom {
		duplicateChapter = arcFrom
	}
	if duplicateChapter > arcTo {
		duplicateChapter = arcTo
	}
	windowTo := min(arcTo, duplicateChapter+outlineRepairWindowChapterCount-1)
	windowFrom := duplicateChapter
	if windowTo-windowFrom+1 < outlineRepairWindowChapterCount {
		windowFrom = max(arcFrom, windowTo-outlineRepairWindowChapterCount+1)
	}
	return windowFrom, windowTo
}

func completedChaptersInRange(completed []int, from, to int) []int {
	out := make([]int, 0, to-from+1)
	for _, chapter := range completed {
		if chapter >= from && chapter <= to && !slices.Contains(out, chapter) {
			out = append(out, chapter)
		}
	}
	slices.Sort(out)
	return out
}

// RepairArcOutline replaces an already-expanded arc without changing its
// chapter count. In adaptation projects it also updates the confirmed plan's
// target outline fields while preserving source anchors and word budgets.
func (s *Store) RepairArcOutline(volumeIdx, arcIdx int, chapters []domain.OutlineEntry) error {
	return s.RepairArcOutlineRange(volumeIdx, arcIdx, 0, 0, chapters)
}

// RepairArcOutlineRange replaces a small global-chapter window inside an
// already-expanded arc. Passing fromChapter/toChapter as zero preserves the
// historical full-arc repair behavior.
func (s *Store) RepairArcOutlineRange(volumeIdx, arcIdx, fromChapter, toChapter int, chapters []domain.OutlineEntry) error {
	return s.Revisions.withLegacyMigrationMutation("repair adaptation outline", s.Outline.migration, func() error {
		return s.repairArcOutlineRange(volumeIdx, arcIdx, fromChapter, toChapter, chapters)
	})
}

func (s *Store) RepairArcOutlineRangeForFoundationRevision(owner *FoundationPlanningOwner, volumeIdx, arcIdx, fromChapter, toChapter int, chapters []domain.OutlineEntry) error {
	return s.Revisions.withFoundationPlanningMutation(owner, "repair Foundation-owned outline", s.Outline.migration, func() error {
		return s.repairArcOutlineRange(volumeIdx, arcIdx, fromChapter, toChapter, chapters)
	})
}

func (s *Store) repairArcOutlineRange(volumeIdx, arcIdx, fromChapter, toChapter int, chapters []domain.OutlineEntry) error {
	s.Foundation.lifecycle.reviewMu.Lock()
	defer s.Foundation.lifecycle.reviewMu.Unlock()
	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	if err := s.requireAuthoritativeFormalMutationLocked("repair arc outline"); err != nil {
		return err
	}

	prepared, err := s.previewRepairedArcEntries(volumeIdx, arcIdx, fromChapter, toChapter, chapters)
	if err != nil {
		return err
	}
	if err := s.validateRepairParentBatch(volumeIdx, arcIdx, fromChapter, toChapter, chapters); err != nil {
		return err
	}
	if err := s.saveOutlineRepairFinalizationRange(volumeIdx, arcIdx, fromChapter, toChapter, prepared, outlineRepairFinalizationStagePrepared); err != nil {
		return err
	}

	s.Outline.io.mu.Lock()
	_, repaired, err := s.Outline.replaceArcChapterRangeUnlocked(volumeIdx, arcIdx, fromChapter, toChapter, chapters)
	s.Outline.io.mu.Unlock()
	if err != nil {
		_ = s.clearOutlineRepairFinalization()
		return err
	}
	if err := s.saveOutlineRepairFinalizationRange(volumeIdx, arcIdx, fromChapter, toChapter, repaired, outlineRepairFinalizationStageOutlineReplaced); err != nil {
		return err
	}

	progress, err := s.finalizeOutlineRepair(volumeIdx, arcIdx, repaired)
	if err != nil {
		return err
	}

	nextBatch, err := s.scanDuplicateOutlineRepairBatch(progress)
	if err != nil {
		return err
	}
	if nextBatch == nil {
		if err := s.saveCleanOutlineDuplicateScan(progress, volumeIdx, arcIdx); err != nil {
			return err
		}
	} else if err := s.clearOutlineDuplicateScan(); err != nil {
		return err
	}
	return nil
}

func (s *Store) previewRepairedArcEntries(volumeIdx, arcIdx, fromChapter, toChapter int, chapters []domain.OutlineEntry) ([]domain.OutlineEntry, error) {
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	location := locateOutlineArc(volumes, volumeIdx, arcIdx)
	if !location.found {
		return nil, fmt.Errorf("arc not found: volume=%d arc=%d", volumeIdx, arcIdx)
	}
	if location.chapterCount == 0 {
		return nil, fmt.Errorf("arc V%d A%d is not expanded", volumeIdx, arcIdx)
	}
	startChapter, expectedChapters, err := repairArcRangeBounds(volumeIdx, arcIdx, location, fromChapter, toChapter)
	if err != nil {
		return nil, err
	}
	if len(chapters) != expectedChapters {
		if fromChapter > 0 || toChapter > 0 {
			return nil, fmt.Errorf("repair_arc V%d A%d chapters %d-%d must keep %d chapters, got %d", volumeIdx, arcIdx, fromChapter, toChapter, expectedChapters, len(chapters))
		}
		return nil, fmt.Errorf("repair_arc V%d A%d must keep %d chapters, got %d", volumeIdx, arcIdx, expectedChapters, len(chapters))
	}
	return numberedOutlineEntries(chapters, startChapter), nil
}

func (s *Store) validateRepairParentBatch(volumeIdx, arcIdx, fromChapter, toChapter int, chapters []domain.OutlineEntry) error {
	if !s.Adaptation.Active() {
		return nil
	}
	entries, err := s.previewRepairedVolumeEntries(volumeIdx, arcIdx, fromChapter, toChapter, chapters)
	if err != nil {
		return err
	}
	return validateOutlineBatchEntries(fmt.Sprintf("repair_arc V%d parent batch", volumeIdx), entries)
}

func (s *Store) previewRepairedVolumeEntries(volumeIdx, arcIdx, fromChapter, toChapter int, chapters []domain.OutlineEntry) ([]domain.OutlineEntry, error) {
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	globalChapter := 1
	for _, volume := range volumes {
		var volumeEntries []domain.OutlineEntry
		for _, arc := range volume.Arcs {
			arcLen := arcOutlineChapterCount(arc)
			if len(arc.Chapters) == 0 {
				globalChapter += arcLen
				continue
			}
			arcEntries := numberedOutlineEntries(arc.Chapters, globalChapter)
			if volume.Index == volumeIdx && arc.Index == arcIdx {
				location := outlineArcLocation{found: true, startChapter: globalChapter, chapterCount: len(arc.Chapters)}
				startChapter, expectedChapters, err := repairArcRangeBounds(volumeIdx, arcIdx, location, fromChapter, toChapter)
				if err != nil {
					return nil, err
				}
				if len(chapters) != expectedChapters {
					return nil, fmt.Errorf("repair_arc V%d A%d chapters %d-%d must keep %d chapters, got %d", volumeIdx, arcIdx, fromChapter, toChapter, expectedChapters, len(chapters))
				}
				repaired := numberedOutlineEntries(chapters, startChapter)
				if fromChapter > 0 || toChapter > 0 {
					offset := fromChapter - globalChapter
					copy(arcEntries[offset:offset+len(repaired)], repaired)
				} else {
					arcEntries = repaired
				}
			}
			if volume.Index == volumeIdx {
				volumeEntries = append(volumeEntries, arcEntries...)
			}
			globalChapter += arcLen
		}
		if volume.Index == volumeIdx {
			return volumeEntries, nil
		}
	}
	return nil, fmt.Errorf("volume not found: volume=%d", volumeIdx)
}

func (s *Store) finalizeOutlineRepair(volumeIdx, arcIdx int, repaired []domain.OutlineEntry) (*domain.Progress, error) {
	if s.Adaptation.Active() {
		plan, err := s.Adaptation.LoadPlan()
		if err != nil {
			return nil, fmt.Errorf("load adaptation plan: %w", err)
		}
		if plan != nil {
			planToSave := *plan
			planToSave.Chapters = cloneAdaptationPlans(plan.Chapters)
			if err := updateAdaptationPlanOutlineEntries(&planToSave, repaired); err != nil {
				return nil, err
			}
			if err := s.Adaptation.savePlan(planToSave); err != nil {
				return nil, fmt.Errorf("save repaired adaptation plan: %w", err)
			}
		}
	}

	repairedChapters := outlineEntryChapters(repaired)
	if err := s.deleteRepairedArcArtifacts(volumeIdx, arcIdx, repairedChapters); err != nil {
		return nil, err
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}

	var progress *domain.Progress
	err = s.Progress.io.WithWriteLock(func() error {
		var loadErr error
		progress, loadErr = s.Progress.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		if progress == nil {
			progress = &domain.Progress{}
		}
		progress.TotalChapters = domain.TotalChapters(volumes)
		if slices.Contains(repairedChapters, progress.InProgressChapter) {
			progress.InProgressChapter = 0
			progress.CompletedScenes = nil
		}
		rewriteChapters := completedChaptersInSet(progress.CompletedChapters, repairedChapters)
		clearChapterWordCounts(progress, rewriteChapters)
		if len(rewriteChapters) > 0 {
			if err := domain.ValidateFlowTransition(progress.Flow, domain.FlowRewriting); err != nil {
				return err
			}
			progress.PendingRewrites = mergePendingRewriteChapters(progress.PendingRewrites, rewriteChapters)
			reason := outlineRepairRewriteReason(volumeIdx, arcIdx, repaired, rewriteChapters)
			if progress.RewriteReason == "" {
				progress.RewriteReason = reason
			} else if !strings.Contains(progress.RewriteReason, reason) {
				progress.RewriteReason += "; " + reason
			}
			progress.Flow = domain.FlowRewriting
			if progress.Phase == domain.PhaseComplete {
				progress.Phase = domain.PhaseWriting
				progress.ReopenedFromComplete = true
			}
		}
		for _, target := range repairedArcPostprocessTargets(volumes, volumeIdx, arcIdx, progress.CompletedChapters) {
			enqueueArcPostprocessTarget(progress, target)
		}
		return s.Progress.saveUnlocked(progress)
	})
	if err != nil {
		return nil, err
	}
	if err := s.clearOutlineRepairFinalization(); err != nil {
		return nil, err
	}
	return progress, nil
}

func (s *Store) deleteRepairedArcArtifacts(volumeIdx, arcIdx int, chapters []int) error {
	if err := s.deleteRevisedChapterArtifacts(chapters); err != nil {
		return err
	}
	if err := s.Summaries.deleteArcSummaryOwned(volumeIdx, arcIdx); err != nil {
		return fmt.Errorf("delete arc summary V%d A%d: %w", volumeIdx, arcIdx, err)
	}
	if err := s.Summaries.deleteVolumeSummaryOwned(volumeIdx); err != nil {
		return fmt.Errorf("delete volume summary V%d: %w", volumeIdx, err)
	}
	return nil
}

func outlineEntryChapters(entries []domain.OutlineEntry) []int {
	chapters := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.Chapter > 0 {
			chapters = append(chapters, entry.Chapter)
		}
	}
	return uniquePositiveChapters(chapters)
}

func completedChaptersInSet(completed []int, allowed []int) []int {
	allowedSet := make(map[int]struct{}, len(allowed))
	for _, chapter := range allowed {
		if chapter > 0 {
			allowedSet[chapter] = struct{}{}
		}
	}
	out := make([]int, 0, len(allowedSet))
	for _, chapter := range allowed {
		if _, ok := allowedSet[chapter]; !ok {
			continue
		}
		if slices.Contains(completed, chapter) && !slices.Contains(out, chapter) {
			out = append(out, chapter)
		}
	}
	return out
}

func uniquePositiveChapters(chapters []int) []int {
	out := make([]int, 0, len(chapters))
	for _, chapter := range chapters {
		if chapter <= 0 || slices.Contains(out, chapter) {
			continue
		}
		out = append(out, chapter)
	}
	slices.Sort(out)
	return out
}

func outlineRepairRewriteReason(volumeIdx, arcIdx int, repaired []domain.OutlineEntry, rewriteChapters []int) string {
	from, to := 0, 0
	if len(repaired) > 0 {
		from = repaired[0].Chapter
		to = repaired[len(repaired)-1].Chapter
	}
	return fmt.Sprintf(
		"outline repair V%d A%d regenerated chapters %d-%d; rewrite completed chapters %v",
		volumeIdx,
		arcIdx,
		from,
		to,
		rewriteChapters,
	)
}

func cloneAdaptationPlans(chapters []domain.AdaptationChapterPlan) []domain.AdaptationChapterPlan {
	out := make([]domain.AdaptationChapterPlan, len(chapters))
	for i := range chapters {
		out[i] = chapters[i]
		out[i].SourceChapters = append([]int(nil), chapters[i].SourceChapters...)
		out[i].PreserveEvents = append([]string(nil), chapters[i].PreserveEvents...)
		out[i].RequiredChanges = append([]string(nil), chapters[i].RequiredChanges...)
		out[i].ForbiddenMoves = append([]string(nil), chapters[i].ForbiddenMoves...)
		out[i].OutlineEntry.Scenes = append([]string(nil), chapters[i].OutlineEntry.Scenes...)
	}
	return out
}

func updateAdaptationPlanOutlineEntries(plan *domain.AdaptationPlan, entries []domain.OutlineEntry) error {
	if plan == nil {
		return nil
	}
	byChapter := make(map[int]domain.OutlineEntry, len(entries))
	for _, entry := range entries {
		byChapter[entry.Chapter] = entry
	}

	updated := 0
	for i := range plan.Chapters {
		entry, ok := byChapter[plan.Chapters[i].Chapter]
		if !ok {
			continue
		}
		plan.Chapters[i].Title = entry.Title
		plan.Chapters[i].OutlineEntry.ID = entry.ID
		plan.Chapters[i].OutlineEntry.Title = entry.Title
		plan.Chapters[i].OutlineEntry.CoreEvent = entry.CoreEvent
		plan.Chapters[i].OutlineEntry.Hook = entry.Hook
		plan.Chapters[i].OutlineEntry.Scenes = append([]string(nil), entry.Scenes...)
		updated++
	}
	if updated != len(entries) {
		return fmt.Errorf("adaptation plan missing %d repaired outline chapters", len(entries)-updated)
	}
	return nil
}
