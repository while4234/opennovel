package domain

import (
	"fmt"
	"strings"
)

// AdaptationChapterOutlineDuplicate identifies a target chapter whose story
// promise repeats an earlier or already-known chapter plan.
type AdaptationChapterOutlineDuplicate struct {
	Chapter         int
	ExistingChapter int
	Title           string
}

func (d AdaptationChapterOutlineDuplicate) Error() string {
	return fmt.Sprintf(
		"chapter %d duplicates outline beats from chapter %d: title/core_event/hook are too similar (%q)",
		d.Chapter,
		d.ExistingChapter,
		d.Title,
	)
}

// FindDuplicateAdaptationChapterOutline returns the first chapter in chapters
// whose title/core_event/hook signature duplicates a chapter in previousGroups
// or an earlier item in chapters. Reused source ranges are allowed; this only
// catches repeated story promises.
func FindDuplicateAdaptationChapterOutline(
	chapters []AdaptationChapterPlan,
	previousGroups ...[]AdaptationChapterPlan,
) (AdaptationChapterOutlineDuplicate, bool) {
	previousEntries := make([][]OutlineEntry, 0, len(previousGroups))
	for _, group := range previousGroups {
		previousEntries = append(previousEntries, adaptationOutlineEntries(group))
	}

	duplicate, ok := FindDuplicateOutlineEntries(adaptationOutlineEntries(chapters), previousEntries...)
	if !ok {
		return AdaptationChapterOutlineDuplicate{}, false
	}
	return AdaptationChapterOutlineDuplicate{
		Chapter:         duplicate.Chapter,
		ExistingChapter: duplicate.ExistingChapter,
		Title:           duplicate.Title,
	}, true
}

func FindAdaptationChapterOutlineReviewCandidates(
	chapters []AdaptationChapterPlan,
	previousGroups ...[]AdaptationChapterPlan,
) []OutlineSimilarityCandidate {
	previousEntries := make([][]OutlineEntry, 0, len(previousGroups))
	for _, group := range previousGroups {
		previousEntries = append(previousEntries, adaptationOutlineEntries(group))
	}
	return FindOutlineSimilarityReviewCandidates(adaptationOutlineEntries(chapters), previousEntries...)
}

func adaptationOutlineEntries(chapters []AdaptationChapterPlan) []OutlineEntry {
	entries := make([]OutlineEntry, 0, len(chapters))
	for _, chapter := range chapters {
		scenes := append([]string(nil), chapter.OutlineEntry.Scenes...)
		if len(scenes) == 0 {
			scenes = append([]string(nil), chapter.Scenes...)
		}
		entries = append(entries, OutlineEntry{
			Chapter:   chapter.Chapter,
			Title:     adaptationChapterTitle(chapter),
			CoreEvent: firstNonEmptyAdaptationOutlineText(chapter.OutlineEntry.CoreEvent, chapter.CoreEvent),
			Hook:      firstNonEmptyAdaptationOutlineText(chapter.OutlineEntry.Hook, chapter.Hook),
			Scenes:    scenes,
		})
	}
	return entries
}

func firstNonEmptyAdaptationOutlineText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func adaptationChapterTitle(chapter AdaptationChapterPlan) string {
	if title := strings.TrimSpace(chapter.Title); title != "" {
		return title
	}
	return strings.TrimSpace(chapter.OutlineEntry.Title)
}
