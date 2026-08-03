package adapt

import (
	"errors"
	"fmt"
	"sort"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

var ErrNoAuditableScope = errors.New("no fully written adaptation scope is available")

type auditBatch struct {
	SourceFrom int
	SourceTo   int
	TargetFrom int
	TargetTo   int
}

// ResolveProjectAuditScope converts the user-selected source endpoint into a
// safe, fully written audit range. Target ranges are always derived here.
func ResolveProjectAuditScope(st *store.Store, options AuditOptions) (adaptaudit.Scope, error) {
	if st == nil {
		return adaptaudit.Scope{}, fmt.Errorf("store is required")
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil {
		return adaptaudit.Scope{}, fmt.Errorf("load adaptation plan: %w", err)
	}
	if plan == nil {
		return adaptaudit.Scope{}, fmt.Errorf("confirmed adaptation plan is required")
	}
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		return adaptaudit.Scope{}, fmt.Errorf("load source manifest: %w", err)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return adaptaudit.Scope{}, fmt.Errorf("load progress: %w", err)
	}
	if progress == nil {
		return adaptaudit.Scope{}, fmt.Errorf("project progress is required")
	}
	return resolveAuditScope(*plan, manifest, progress, options.SourceTo)
}

func resolveAuditScope(
	plan domain.AdaptationPlan,
	manifest *domain.AdaptationSourceManifest,
	progress *domain.Progress,
	requestedSourceTo int,
) (adaptaudit.Scope, error) {
	completed := make(map[int]bool, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		if chapter > 0 {
			completed[chapter] = true
		}
	}
	if len(completed) == 0 {
		return adaptaudit.Scope{}, fmt.Errorf("%w: no completed target chapters", ErrNoAuditableScope)
	}

	mode := domain.NormalizeAdaptationGranularity(plan.Granularity)
	sourceLimit := sourceChapterLimit(plan, manifest)
	if sourceLimit <= 0 && mode == domain.AdaptationGranularityFree {
		sourceLimit = max(requestedSourceTo, 1)
	}
	if requestedSourceTo <= 0 || requestedSourceTo > sourceLimit {
		requestedSourceTo = sourceLimit
	}
	if requestedSourceTo <= 0 {
		return adaptaudit.Scope{}, fmt.Errorf("%w: no source chapters", ErrNoAuditableScope)
	}

	var scope adaptaudit.Scope
	var ok bool
	switch mode {
	case domain.AdaptationGranularityArc:
		scope, ok = resolveArcAuditScope(plan, completed, requestedSourceTo)
	case domain.AdaptationGranularityChapter:
		scope, ok = resolveChapterAuditScope(plan, manifest, completed, requestedSourceTo)
	case domain.AdaptationGranularityFree:
		scope, ok = resolveFreeAuditScope(plan, completed, requestedSourceTo)
	default:
		return adaptaudit.Scope{}, fmt.Errorf("unsupported adaptation mode %q", plan.Granularity)
	}
	if !ok {
		return adaptaudit.Scope{}, fmt.Errorf("%w for the selected source endpoint", ErrNoAuditableScope)
	}
	return scope, nil
}

func resolveArcAuditScope(plan domain.AdaptationPlan, completed map[int]bool, requestedSourceTo int) (adaptaudit.Scope, bool) {
	batches := auditBatches(plan)
	if len(batches) == 0 {
		return adaptaudit.Scope{}, false
	}
	firstSource, firstTarget := 0, 0
	lastSource, lastTarget := 0, 0
	expectedTargetFrom := 1
	for _, batch := range batches {
		if batch.SourceFrom > requestedSourceTo {
			break
		}
		if (lastSource == 0 && batch.SourceFrom != 1) || (lastSource > 0 && batch.SourceFrom > lastSource+1) || batch.TargetFrom != expectedTargetFrom {
			break
		}
		if !completedRange(completed, batch.TargetFrom, batch.TargetTo) {
			break
		}
		if firstSource == 0 {
			firstSource, firstTarget = batch.SourceFrom, batch.TargetFrom
		}
		lastSource, lastTarget = batch.SourceTo, batch.TargetTo
		expectedTargetFrom = batch.TargetTo + 1
		if requestedSourceTo <= batch.SourceTo {
			break
		}
	}
	if lastSource == 0 || lastTarget == 0 {
		return adaptaudit.Scope{}, false
	}
	return adaptaudit.Scope{SourceFrom: firstSource, SourceTo: lastSource, TargetFrom: firstTarget, TargetTo: lastTarget}, true
}

func resolveChapterAuditScope(plan domain.AdaptationPlan, manifest *domain.AdaptationSourceManifest, completed map[int]bool, requestedSourceTo int) (adaptaudit.Scope, bool) {
	targetsBySource := make(map[int]map[int]bool)
	segmentsBySource := make(map[int][]domain.AdaptationSourceSegment)
	for _, chapter := range plan.Chapters {
		sources := chapterSourceChapters(chapter)
		for _, source := range sources {
			if targetsBySource[source] == nil {
				targetsBySource[source] = make(map[int]bool)
			}
			targetsBySource[source][chapter.Chapter] = true
		}
		for _, segment := range chapter.SourceSegments {
			segmentsBySource[segment.SourceChapter] = append(segmentsBySource[segment.SourceChapter], segment)
		}
	}
	if len(targetsBySource) == 0 {
		return adaptaudit.Scope{}, false
	}
	sources := make([]int, 0, len(targetsBySource))
	for source := range targetsBySource {
		if source > 0 && source <= requestedSourceTo {
			sources = append(sources, source)
		}
	}
	sort.Ints(sources)
	if len(sources) == 0 || sources[0] != 1 {
		return adaptaudit.Scope{}, false
	}
	sourceRunes := sourceRunesByChapter(manifest)
	firstSource, firstTarget := 0, 0
	lastSource, lastTarget := 0, 0
	previousSource := 0
	for _, source := range sources {
		if source != previousSource+1 {
			break
		}
		targets := targetsBySource[source]
		unitComplete := len(targets) > 0
		if segments := segmentsBySource[source]; len(segments) > 0 {
			sort.Slice(segments, func(i, j int) bool { return segments[i].Sequence < segments[j].Sequence })
			unitComplete = len(domain.CheckAdaptationSourceSegments(source, sourceRunes[source], segments)) == 0
		}
		unitFirst, unitLast := 0, 0
		for target := range targets {
			if !completed[target] {
				unitComplete = false
				break
			}
			if unitFirst == 0 || target < unitFirst {
				unitFirst = target
			}
			if target > unitLast {
				unitLast = target
			}
		}
		if !unitComplete {
			break
		}
		if firstTarget == 0 && unitFirst != 1 {
			break
		}
		if firstSource == 0 {
			firstSource, firstTarget = source, unitFirst
		}
		lastSource = source
		if unitLast > lastTarget {
			lastTarget = unitLast
		}
		previousSource = source
	}
	if lastSource == 0 || lastTarget == 0 {
		return adaptaudit.Scope{}, false
	}
	return adaptaudit.Scope{SourceFrom: firstSource, SourceTo: lastSource, TargetFrom: firstTarget, TargetTo: lastTarget}, true
}

func resolveFreeAuditScope(plan domain.AdaptationPlan, completed map[int]bool, requestedSourceTo int) (adaptaudit.Scope, bool) {
	chapters := append([]domain.AdaptationChapterPlan(nil), plan.Chapters...)
	sort.Slice(chapters, func(i, j int) bool { return chapters[i].Chapter < chapters[j].Chapter })
	firstTarget, lastTarget := 0, 0
	for _, chapter := range chapters {
		if firstTarget == 0 {
			if chapter.Chapter != 1 {
				return adaptaudit.Scope{}, false
			}
			firstTarget = chapter.Chapter
		}
		if chapter.Chapter != lastTarget+1 && lastTarget != 0 {
			break
		}
		if !completed[chapter.Chapter] {
			break
		}
		lastTarget = chapter.Chapter
	}
	if lastTarget == 0 {
		return adaptaudit.Scope{}, false
	}
	return adaptaudit.Scope{SourceFrom: 1, SourceTo: requestedSourceTo, TargetFrom: firstTarget, TargetTo: lastTarget}, true
}

func auditBatches(plan domain.AdaptationPlan) []auditBatch {
	batches := make([]auditBatch, 0, len(plan.Volumes))
	for _, volume := range plan.Volumes {
		if volume.SourceFrom <= 0 || volume.SourceTo < volume.SourceFrom || volume.TargetFrom <= 0 || volume.TargetTo < volume.TargetFrom {
			continue
		}
		batches = append(batches, auditBatch{
			SourceFrom: volume.SourceFrom, SourceTo: volume.SourceTo,
			TargetFrom: volume.TargetFrom, TargetTo: volume.TargetTo,
		})
	}
	if len(batches) == 0 {
		grouped := make(map[[2]int]*auditBatch)
		for _, chapter := range plan.Chapters {
			from, to := chapter.SourceRange.From, chapter.SourceRange.To
			if from <= 0 || to < from || chapter.Chapter <= 0 {
				continue
			}
			key := [2]int{from, to}
			batch := grouped[key]
			if batch == nil {
				batch = &auditBatch{SourceFrom: from, SourceTo: to, TargetFrom: chapter.Chapter, TargetTo: chapter.Chapter}
				grouped[key] = batch
			}
			if chapter.Chapter < batch.TargetFrom {
				batch.TargetFrom = chapter.Chapter
			}
			if chapter.Chapter > batch.TargetTo {
				batch.TargetTo = chapter.Chapter
			}
		}
		for _, batch := range grouped {
			batches = append(batches, *batch)
		}
	}
	sort.Slice(batches, func(i, j int) bool {
		if batches[i].SourceFrom != batches[j].SourceFrom {
			return batches[i].SourceFrom < batches[j].SourceFrom
		}
		return batches[i].TargetFrom < batches[j].TargetFrom
	})
	return batches
}

func chapterSourceChapters(chapter domain.AdaptationChapterPlan) []int {
	seen := make(map[int]bool)
	var sources []int
	appendSource := func(source int) {
		if source > 0 && !seen[source] {
			seen[source] = true
			sources = append(sources, source)
		}
	}
	if len(chapter.SourceSegments) > 0 {
		for _, segment := range chapter.SourceSegments {
			appendSource(segment.SourceChapter)
		}
	} else if len(chapter.SourceChapters) > 0 {
		for _, source := range chapter.SourceChapters {
			appendSource(source)
		}
	} else {
		for source := chapter.SourceRange.From; source > 0 && source <= chapter.SourceRange.To; source++ {
			appendSource(source)
		}
	}
	sort.Ints(sources)
	return sources
}

func completedRange(completed map[int]bool, from, to int) bool {
	if from <= 0 || to < from {
		return false
	}
	for chapter := from; chapter <= to; chapter++ {
		if !completed[chapter] {
			return false
		}
	}
	return true
}

func sourceChapterLimit(plan domain.AdaptationPlan, manifest *domain.AdaptationSourceManifest) int {
	if manifest != nil && manifest.ChapterCount > 0 {
		return manifest.ChapterCount
	}
	limit := 0
	for _, batch := range auditBatches(plan) {
		if batch.SourceTo > limit {
			limit = batch.SourceTo
		}
	}
	for _, chapter := range plan.Chapters {
		for _, source := range chapterSourceChapters(chapter) {
			if source > limit {
				limit = source
			}
		}
	}
	return limit
}
