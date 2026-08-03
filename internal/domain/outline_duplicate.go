package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// OutlineDuplicate identifies a chapter whose outline promise repeats an
// earlier or already-known chapter.
type OutlineDuplicate struct {
	Chapter         int
	ExistingChapter int
	Title           string
	Reason          string
}

// OutlineSimilarityCandidate identifies a pair that is too similar to trust
// deterministically, but not similar enough to auto-reject without model review.
type OutlineSimilarityCandidate struct {
	Chapter          int
	ExistingChapter  int
	Title            string
	Reason           string
	DetailSimilarity float64
	FullSimilarity   float64
}

func (d OutlineDuplicate) Error() string {
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "title/core_event/hook are too similar"
	}
	return fmt.Sprintf(
		"chapter %d duplicates outline beats from chapter %d: %s (%q)",
		d.Chapter,
		d.ExistingChapter,
		reason,
		d.Title,
	)
}

// FindDuplicateOutlineEntries returns the first chapter in entries whose
// title/core_event/hook signature duplicates a chapter in previousGroups or an
// earlier item in entries.
func FindDuplicateOutlineEntries(
	entries []OutlineEntry,
	previousGroups ...[]OutlineEntry,
) (OutlineDuplicate, bool) {
	seen := make([]OutlineEntry, 0, len(entries))
	for _, group := range previousGroups {
		for _, entry := range group {
			if outlineComparable(entry) {
				seen = append(seen, entry)
			}
		}
	}

	for _, entry := range entries {
		if !outlineComparable(entry) {
			continue
		}
		for _, existing := range seen {
			if existing.Chapter == entry.Chapter {
				continue
			}
			reason, duplicate := outlineEntriesDuplicate(existing, entry)
			if !duplicate {
				continue
			}
			return OutlineDuplicate{
				Chapter:         entry.Chapter,
				ExistingChapter: existing.Chapter,
				Title:           strings.TrimSpace(entry.Title),
				Reason:          reason,
			}, true
		}
		seen = append(seen, entry)
	}
	return OutlineDuplicate{}, false
}

// FindOutlineSimilarityReviewCandidates returns batch-local outline pairs that
// should be reviewed by a model before accepting the batch.
func FindOutlineSimilarityReviewCandidates(
	entries []OutlineEntry,
	previousGroups ...[]OutlineEntry,
) []OutlineSimilarityCandidate {
	seen := make([]OutlineEntry, 0, len(entries))
	for _, group := range previousGroups {
		for _, entry := range group {
			if outlineComparable(entry) {
				seen = append(seen, entry)
			}
		}
	}

	var candidates []OutlineSimilarityCandidate
	for _, entry := range entries {
		if !outlineComparable(entry) {
			continue
		}
		for _, existing := range seen {
			if existing.Chapter == entry.Chapter {
				continue
			}
			assessment := assessOutlineSimilarity(existing, entry)
			if assessment.duplicate || !assessment.review {
				continue
			}
			candidates = append(candidates, OutlineSimilarityCandidate{
				Chapter:          entry.Chapter,
				ExistingChapter:  existing.Chapter,
				Title:            strings.TrimSpace(entry.Title),
				Reason:           assessment.reason,
				DetailSimilarity: assessment.detailSimilarity,
				FullSimilarity:   assessment.fullSimilarity,
			})
		}
		seen = append(seen, entry)
	}
	return candidates
}

const (
	duplicateTitleMinRunes      = 4
	outlineSimilarityMinRunes   = 24
	outlineSameTitleSimilarity  = 0.58
	outlineFullTextSimilarity   = 0.78
	outlineDetailTextSimilarity = 0.82
	outlineReviewSimilarity     = 0.70
)

func outlineSignature(entry OutlineEntry) string {
	title := strings.TrimSpace(entry.Title)
	coreEvent := strings.TrimSpace(entry.CoreEvent)
	hook := strings.TrimSpace(entry.Hook)
	if title == "" || coreEvent == "" || hook == "" {
		return ""
	}
	titleKey := outlineSignaturePart(title)
	coreEventKey := outlineSignaturePart(coreEvent)
	hookKey := outlineSignaturePart(hook)
	if titleKey == "" || coreEventKey == "" || hookKey == "" {
		return ""
	}
	return strings.Join([]string{titleKey, coreEventKey, hookKey}, "\x00")
}

func outlineComparable(entry OutlineEntry) bool {
	return strings.TrimSpace(entry.Title) != "" ||
		strings.TrimSpace(entry.CoreEvent) != "" ||
		strings.TrimSpace(entry.Hook) != "" ||
		len(entry.Scenes) > 0
}

func outlineEntriesDuplicate(existing, entry OutlineEntry) (string, bool) {
	assessment := assessOutlineSimilarity(existing, entry)
	return assessment.reason, assessment.duplicate
}

type outlineSimilarityAssessment struct {
	reason           string
	detailSimilarity float64
	fullSimilarity   float64
	duplicate        bool
	review           bool
}

func assessOutlineSimilarity(existing, entry OutlineEntry) outlineSimilarityAssessment {
	if signature := outlineSignature(entry); signature != "" && signature == outlineSignature(existing) {
		return outlineSimilarityAssessment{
			reason:    "same title/core_event/hook signature",
			duplicate: true,
		}
	}

	existingTitle := outlineSignaturePart(existing.Title)
	entryTitle := outlineSignaturePart(entry.Title)
	detailSimilarity := outlineTextSimilarity(outlineDetailText(existing), outlineDetailText(entry))
	fullSimilarity := outlineTextSimilarity(outlineComparableText(existing), outlineComparableText(entry))
	if existingTitle != "" && existingTitle == entryTitle {
		if utf8.RuneCountInString(entryTitle) >= duplicateTitleMinRunes {
			return outlineSimilarityAssessment{
				reason:           "same chapter title",
				detailSimilarity: detailSimilarity,
				fullSimilarity:   fullSimilarity,
				duplicate:        true,
			}
		}
		if detailSimilarity >= outlineSameTitleSimilarity {
			return outlineSimilarityAssessment{
				reason:           "same title with similar detailed outline",
				detailSimilarity: detailSimilarity,
				fullSimilarity:   fullSimilarity,
				duplicate:        true,
			}
		}
	}

	if detailSimilarity >= outlineDetailTextSimilarity {
		return outlineSimilarityAssessment{
			reason:           "similar core_event/hook/scenes",
			detailSimilarity: detailSimilarity,
			fullSimilarity:   fullSimilarity,
			duplicate:        true,
		}
	}
	if fullSimilarity >= outlineFullTextSimilarity {
		return outlineSimilarityAssessment{
			reason:           "similar detailed outline",
			detailSimilarity: detailSimilarity,
			fullSimilarity:   fullSimilarity,
			duplicate:        true,
		}
	}
	if detailSimilarity >= outlineReviewSimilarity || fullSimilarity >= outlineReviewSimilarity {
		return outlineSimilarityAssessment{
			reason:           "borderline similar detailed outline",
			detailSimilarity: detailSimilarity,
			fullSimilarity:   fullSimilarity,
			review:           true,
		}
	}
	return outlineSimilarityAssessment{
		detailSimilarity: detailSimilarity,
		fullSimilarity:   fullSimilarity,
	}
}

func outlineDetailText(entry OutlineEntry) string {
	parts := []string{entry.CoreEvent, entry.Hook}
	parts = append(parts, entry.Scenes...)
	return strings.Join(parts, "\n")
}

func outlineComparableText(entry OutlineEntry) string {
	return strings.TrimSpace(entry.Title) + "\n" + outlineDetailText(entry)
}

func outlineTextSimilarity(left, right string) float64 {
	left = outlineSignaturePart(left)
	right = outlineSignaturePart(right)
	if left == "" || right == "" {
		return 0
	}
	if utf8.RuneCountInString(left) < outlineSimilarityMinRunes ||
		utf8.RuneCountInString(right) < outlineSimilarityMinRunes {
		return 0
	}
	if left == right {
		return 1
	}

	leftGrams := outlineRuneBigrams(left)
	rightGrams := outlineRuneBigrams(right)
	if len(leftGrams) == 0 || len(rightGrams) == 0 {
		return 0
	}
	intersection := 0
	for gram := range leftGrams {
		if _, ok := rightGrams[gram]; ok {
			intersection++
		}
	}
	shorter := min(len(leftGrams), len(rightGrams))
	return float64(intersection) / float64(shorter)
}

func outlineRuneBigrams(text string) map[string]struct{} {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	if len(runes) == 1 {
		return map[string]struct{}{string(runes): {}}
	}
	grams := make(map[string]struct{}, len(runes)-1)
	for i := 0; i+1 < len(runes); i++ {
		grams[string(runes[i:i+2])] = struct{}{}
	}
	return grams
}

func outlineSignaturePart(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
