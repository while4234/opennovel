package store

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// ReviseChapterOutline replaces one formal chapter outline without changing
// the story structure. Existing chapter artifacts are invalidated so Resume
// can rebuild the chapter and all downstream reviews from the new promise.
func (s *Store) ReviseChapterOutline(chapter int, revised domain.OutlineEntry) error {
	if chapter <= 0 {
		return fmt.Errorf("chapter must be > 0")
	}
	revised = normalizeRevisedOutlineEntry(chapter, revised)
	if err := validateRevisedOutlineEntry(revised); err != nil {
		return err
	}

	current, err := s.Outline.GetChapterOutline(chapter)
	if err != nil {
		return err
	}
	revised.ID = current.ID
	if equalOutlineEntry(*current, revised) {
		return fmt.Errorf("chapter %d outline is unchanged", chapter)
	}

	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return err
	}
	if len(volumes) > 0 {
		volume, arc, locateErr := s.Outline.LocateChapter(chapter)
		if locateErr != nil {
			return locateErr
		}
		return s.RepairArcOutlineRange(volume, arc, chapter, chapter, []domain.OutlineEntry{revised})
	}

	return s.Revisions.withLegacyMigrationMutation("revise flat adaptation chapter outline", s.Outline.migration, func() error {
		return s.reviseFlatChapterOutline(chapter, revised)
	})
}

func (s *Store) reviseFlatChapterOutline(chapter int, revised domain.OutlineEntry) error {
	s.Foundation.lifecycle.reviewMu.Lock()
	defer s.Foundation.lifecycle.reviewMu.Unlock()
	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	if err := s.requireAuthoritativeFormalMutationLocked("revise flat chapter outline"); err != nil {
		return err
	}

	entries, err := s.Outline.LoadOutline()
	if err != nil {
		return err
	}
	index := -1
	for i := range entries {
		if entries[i].Chapter == chapter {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("chapter %d not found in outline", chapter)
	}
	entries[index] = revised
	if err := validateOutlineBatchEntries("revise chapter outline", entries); err != nil {
		return err
	}
	if err := s.Outline.saveOutline(entries); err != nil {
		return err
	}

	if s.Adaptation.Active() {
		plan, err := s.Adaptation.LoadPlan()
		if err != nil {
			return fmt.Errorf("load adaptation plan: %w", err)
		}
		if plan != nil {
			planToSave := *plan
			planToSave.Chapters = cloneAdaptationPlans(plan.Chapters)
			if err := updateAdaptationPlanOutlineEntries(&planToSave, []domain.OutlineEntry{revised}); err != nil {
				return err
			}
			if err := s.Adaptation.savePlan(planToSave); err != nil {
				return fmt.Errorf("save revised adaptation plan: %w", err)
			}
		}
	}

	if err := s.deleteRevisedChapterArtifacts([]int{chapter}); err != nil {
		return err
	}
	return s.finalizeFlatChapterOutlineRevision(entries, chapter)
}

func (s *Store) finalizeFlatChapterOutlineRevision(entries []domain.OutlineEntry, chapter int) error {
	return s.Progress.io.WithWriteLock(func() error {
		progress, err := s.Progress.loadUnlocked()
		if err != nil {
			return err
		}
		if progress == nil {
			progress = &domain.Progress{}
		}
		progress.TotalChapters = len(entries)
		if progress.InProgressChapter == chapter {
			progress.InProgressChapter = 0
			progress.CompletedScenes = nil
		}
		if slices.Contains(progress.CompletedChapters, chapter) {
			clearChapterWordCounts(progress, []int{chapter})
			progress.PendingRewrites = mergePendingRewriteChapters(progress.PendingRewrites, []int{chapter})
			reason := fmt.Sprintf("chapter %d outline revised; rewrite and review required", chapter)
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
		return s.Progress.saveUnlocked(progress)
	})
}

func (s *Store) deleteRevisedChapterArtifacts(chapters []int) error {
	revisedChapters := uniquePositiveChapters(chapters)
	if err := s.World.DeleteChapterFacts(revisedChapters); err != nil {
		return fmt.Errorf("delete revised chapter world facts: %w", err)
	}
	if err := s.Cast.DeleteChapterAppearances(revisedChapters); err != nil {
		return fmt.Errorf("delete revised chapter cast appearances: %w", err)
	}
	for _, chapter := range revisedChapters {
		if err := s.Drafts.deleteChapterArtifactsOwned(chapter); err != nil {
			return fmt.Errorf("delete chapter %d draft artifacts: %w", chapter, err)
		}
		if err := s.Summaries.deleteChapterSummaryOwned(chapter); err != nil {
			return fmt.Errorf("delete chapter %d summary: %w", chapter, err)
		}
		if err := s.World.deleteReviewOwned(chapter); err != nil {
			return fmt.Errorf("delete chapter %d review: %w", chapter, err)
		}
		if err := s.Adaptation.deleteCheck(chapter); err != nil {
			return fmt.Errorf("delete chapter %d adaptation check: %w", chapter, err)
		}
	}
	return nil
}

func normalizeRevisedOutlineEntry(chapter int, entry domain.OutlineEntry) domain.OutlineEntry {
	entry.Chapter = chapter
	entry.Title = strings.TrimSpace(entry.Title)
	entry.CoreEvent = strings.TrimSpace(entry.CoreEvent)
	entry.Hook = strings.TrimSpace(entry.Hook)
	scenes := make([]string, 0, len(entry.Scenes))
	for _, scene := range entry.Scenes {
		if scene = strings.TrimSpace(scene); scene != "" {
			scenes = append(scenes, scene)
		}
	}
	entry.Scenes = scenes
	return entry
}

func validateRevisedOutlineEntry(entry domain.OutlineEntry) error {
	if entry.Title == "" {
		return fmt.Errorf("chapter %d title is required", entry.Chapter)
	}
	if entry.CoreEvent == "" {
		return fmt.Errorf("chapter %d core_event is required", entry.Chapter)
	}
	if entry.Hook == "" {
		return fmt.Errorf("chapter %d hook is required", entry.Chapter)
	}
	if len(entry.Scenes) == 0 {
		return fmt.Errorf("chapter %d scenes are required", entry.Chapter)
	}
	return nil
}

func equalOutlineEntry(a, b domain.OutlineEntry) bool {
	a = normalizeRevisedOutlineEntry(a.Chapter, a)
	b = normalizeRevisedOutlineEntry(a.Chapter, b)
	return reflect.DeepEqual(a, b)
}
