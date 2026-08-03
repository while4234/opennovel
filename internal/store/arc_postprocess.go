package store

import (
	"slices"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type ArcReviewBatch struct {
	Volume int
	Arc    int
	From   int
	To     int
	Index  int
	Total  int
}

func (s *Store) FindPendingArcPostprocess(progress *domain.Progress) (*ArcBoundary, error) {
	if progress == nil || !progress.Layered || len(progress.PendingArcPost) == 0 {
		return nil, nil
	}

	current, err := s.Progress.Load()
	if err != nil || current == nil || len(current.PendingArcPost) == 0 {
		return nil, err
	}

	pending := uniqueArcPostprocessTargets(current.PendingArcPost)
	kept := make([]domain.ArcPostprocessTarget, 0, len(pending))
	var selected *ArcBoundary

	for _, target := range pending {
		boundary, err := s.Outline.CheckArcBoundary(target.LastChapter)
		if err != nil {
			return nil, err
		}
		if !arcPostprocessTargetMatchesBoundary(target, boundary) {
			continue
		}
		if s.arcPostprocessComplete(boundary) {
			continue
		}
		if selected == nil {
			copyBoundary := *boundary
			selected = &copyBoundary
		}
		kept = append(kept, target)
	}

	if selected != nil {
		current.CurrentVolume = selected.Volume
		current.CurrentArc = selected.Arc
	}
	if !sameArcPostprocessTargets(current.PendingArcPost, kept) || selected != nil {
		current.PendingArcPost = kept
		if err := s.Progress.Save(current); err != nil {
			return nil, err
		}
	}
	return selected, nil
}

func (s *Store) NextArcReviewBatch(boundary *ArcBoundary, runeBudget int) (*ArcReviewBatch, error) {
	if boundary == nil || !boundary.IsArcEnd || boundary.FirstChapter <= 0 || boundary.LastChapter < boundary.FirstChapter {
		return nil, nil
	}
	ranges, err := s.ArcReviewBatchRanges(boundary.FirstChapter, boundary.LastChapter, runeBudget)
	if err != nil {
		return nil, err
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	reviews, err := s.World.LoadArcBatchReviews(boundary.Volume, boundary.Arc)
	if err != nil {
		return nil, err
	}
	done := make(map[[2]int]struct{}, len(reviews))
	for _, review := range reviews {
		done[[2]int{review.BatchFrom, review.BatchTo}] = struct{}{}
	}
	for index, r := range ranges {
		if _, ok := done[[2]int{r.From, r.To}]; ok {
			continue
		}
		return &ArcReviewBatch{
			Volume: boundary.Volume,
			Arc:    boundary.Arc,
			From:   r.From,
			To:     r.To,
			Index:  index + 1,
			Total:  len(ranges),
		}, nil
	}
	return nil, nil
}

func (s *Store) ArcReviewBatchRanges(from, to, runeBudget int) ([]ChapterRange, error) {
	if from <= 0 || to < from {
		return nil, nil
	}
	var ranges []ChapterRange
	currentFrom := 0
	currentRunes := 0
	for chapter := from; chapter <= to; chapter++ {
		text, err := s.Drafts.LoadChapterText(chapter)
		if err != nil {
			return nil, err
		}
		chapterRunes := utf8.RuneCountInString(text)
		if currentFrom == 0 {
			currentFrom = chapter
		}
		if runeBudget > 0 && currentRunes > 0 && currentRunes+chapterRunes > runeBudget {
			ranges = append(ranges, ChapterRange{From: currentFrom, To: chapter - 1})
			currentFrom = chapter
			currentRunes = 0
		}
		currentRunes += chapterRunes
		if runeBudget > 0 && chapterRunes > runeBudget {
			ranges = append(ranges, ChapterRange{From: chapter, To: chapter})
			currentFrom = 0
			currentRunes = 0
		}
	}
	if currentFrom > 0 {
		ranges = append(ranges, ChapterRange{From: currentFrom, To: to})
	}
	return ranges, nil
}

type ChapterRange struct {
	From int
	To   int
}

func (s *Store) arcPostprocessComplete(boundary *ArcBoundary) bool {
	if boundary == nil || !boundary.IsArcEnd || boundary.LastChapter <= 0 {
		return true
	}
	hasReview := s.World.HasArcReview(boundary.LastChapter) ||
		s.Checkpoints.LatestByStep(domain.ArcScope(boundary.Volume, boundary.Arc), "review") != nil
	if !hasReview {
		return false
	}
	if !s.Summaries.HasArcSummary(boundary.Volume, boundary.Arc) {
		return false
	}
	return !boundary.IsVolumeEnd || s.Summaries.HasVolumeSummary(boundary.Volume)
}

func enqueueArcPostprocessTarget(progress *domain.Progress, target domain.ArcPostprocessTarget) {
	if progress == nil || target.Volume <= 0 || target.Arc <= 0 || target.LastChapter <= 0 {
		return
	}
	for _, existing := range progress.PendingArcPost {
		if existing.Volume == target.Volume && existing.Arc == target.Arc && existing.LastChapter == target.LastChapter {
			return
		}
	}
	progress.PendingArcPost = append(progress.PendingArcPost, target)
	slices.SortFunc(progress.PendingArcPost, compareArcPostprocessTargets)
}

func repairedArcPostprocessTargets(volumes []domain.VolumeOutline, volumeIdx, arcIdx int, completed []int) []domain.ArcPostprocessTarget {
	completedSet := positiveIntSet(completed)
	globalChapter := 1
	var repaired *domain.ArcPostprocessTarget
	var volumeEnd *domain.ArcPostprocessTarget

	for _, volume := range volumes {
		for arcIndex, arc := range volume.Arcs {
			arcLen := arcOutlineChapterCount(arc)
			from := globalChapter
			to := globalChapter + arcLen - 1
			arcComplete := chapterRangeComplete(completedSet, from, to)
			if volume.Index == volumeIdx && arc.Index == arcIdx && arcComplete {
				target := domain.ArcPostprocessTarget{Volume: volume.Index, Arc: arc.Index, LastChapter: to}
				repaired = &target
			}
			if volume.Index == volumeIdx && arcIndex == len(volume.Arcs)-1 && arcComplete {
				target := domain.ArcPostprocessTarget{Volume: volume.Index, Arc: arc.Index, LastChapter: to}
				volumeEnd = &target
			}
			globalChapter += arcLen
		}
	}

	var targets []domain.ArcPostprocessTarget
	if repaired != nil {
		targets = append(targets, *repaired)
	}
	if volumeEnd != nil && !containsArcPostprocessTarget(targets, *volumeEnd) {
		targets = append(targets, *volumeEnd)
	}
	return targets
}

func chapterRangeComplete(completed map[int]struct{}, from, to int) bool {
	if from <= 0 || to < from {
		return false
	}
	for chapter := from; chapter <= to; chapter++ {
		if _, ok := completed[chapter]; !ok {
			return false
		}
	}
	return true
}

func uniqueArcPostprocessTargets(targets []domain.ArcPostprocessTarget) []domain.ArcPostprocessTarget {
	out := make([]domain.ArcPostprocessTarget, 0, len(targets))
	for _, target := range targets {
		if target.Volume <= 0 || target.Arc <= 0 || target.LastChapter <= 0 {
			continue
		}
		if containsArcPostprocessTarget(out, target) {
			continue
		}
		out = append(out, target)
	}
	slices.SortFunc(out, compareArcPostprocessTargets)
	return out
}

func containsArcPostprocessTarget(targets []domain.ArcPostprocessTarget, target domain.ArcPostprocessTarget) bool {
	return slices.ContainsFunc(targets, func(existing domain.ArcPostprocessTarget) bool {
		return existing.Volume == target.Volume && existing.Arc == target.Arc && existing.LastChapter == target.LastChapter
	})
}

func compareArcPostprocessTargets(a, b domain.ArcPostprocessTarget) int {
	if a.LastChapter != b.LastChapter {
		return a.LastChapter - b.LastChapter
	}
	if a.Volume != b.Volume {
		return a.Volume - b.Volume
	}
	return a.Arc - b.Arc
}

func sameArcPostprocessTargets(a, b []domain.ArcPostprocessTarget) bool {
	a = uniqueArcPostprocessTargets(a)
	b = uniqueArcPostprocessTargets(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func arcPostprocessTargetMatchesBoundary(target domain.ArcPostprocessTarget, boundary *ArcBoundary) bool {
	return boundary != nil &&
		boundary.IsArcEnd &&
		boundary.Volume == target.Volume &&
		boundary.Arc == target.Arc &&
		boundary.LastChapter == target.LastChapter
}
