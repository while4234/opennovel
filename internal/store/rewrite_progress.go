package store

import "github.com/voocel/ainovel-cli/internal/domain"

// ReconcilePendingRewriteProgress keeps progress counters aligned with deleted
// final chapters while preserving CompletedChapters for rewrite-queue routing.
func (s *Store) ReconcilePendingRewriteProgress() (*domain.Progress, error) {
	var reconciled *domain.Progress
	err := s.Revisions.withLegacyMigrationMutation("reconcile pending rewrite progress", s.Outline.migration, func() error {
		var err error
		reconciled, err = s.reconcilePendingRewriteProgressOwned()
		return err
	})
	return reconciled, err
}

func (s *Store) reconcilePendingRewriteProgressOwned() (*domain.Progress, error) {
	progress, err := s.Progress.Load()
	if err != nil || progress == nil || len(progress.PendingRewrites) == 0 {
		return progress, err
	}

	var missing []int
	for _, chapter := range uniquePositiveChapters(progress.PendingRewrites) {
		text, err := s.Drafts.LoadChapterText(chapter)
		if err != nil {
			return progress, err
		}
		if text == "" {
			missing = append(missing, chapter)
		}
	}
	if len(missing) == 0 {
		return progress, nil
	}

	var updated *domain.Progress
	err = s.Progress.io.WithWriteLock(func() error {
		var loadErr error
		updated, loadErr = s.Progress.loadUnlocked()
		if loadErr != nil || updated == nil {
			return loadErr
		}
		if !clearChapterWordCounts(updated, missing) {
			return nil
		}
		return s.Progress.saveUnlocked(updated)
	})
	if err != nil {
		return progress, err
	}
	if updated == nil {
		return progress, nil
	}
	return updated, nil
}

func clearChapterWordCounts(progress *domain.Progress, chapters []int) bool {
	if progress == nil || len(progress.ChapterWordCounts) == 0 || len(chapters) == 0 {
		return false
	}
	changed := false
	for _, chapter := range uniquePositiveChapters(chapters) {
		wordCount, ok := progress.ChapterWordCounts[chapter]
		if !ok {
			continue
		}
		progress.TotalWordCount -= wordCount
		delete(progress.ChapterWordCounts, chapter)
		changed = true
	}
	if progress.TotalWordCount < 0 {
		progress.TotalWordCount = 0
		for _, wordCount := range progress.ChapterWordCounts {
			progress.TotalWordCount += wordCount
		}
	}
	return changed
}

func mergePendingRewriteChapters(existing, additional []int) []int {
	if len(existing) == 0 {
		return uniquePositiveChapters(additional)
	}
	if len(additional) == 0 {
		return uniquePositiveChapters(existing)
	}
	merged := make([]int, 0, len(existing)+len(additional))
	merged = append(merged, existing...)
	merged = append(merged, additional...)
	return uniquePositiveChapters(merged)
}
