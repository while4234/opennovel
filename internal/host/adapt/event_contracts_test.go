package adapt

import (
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestFinalizeArcRejectsHighLevelMainlineMissingFromChapters(t *testing.T) {
	reports := []domain.AdaptationSourceReport{{
		Chapter: 13,
		KeyEvents: []string{
			"百里冰遇劫，林逸飞出手相救并相识",
			"二人共同救助皮二并形成债务关系",
		},
	}}
	proposal := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityArc, Chapters: []domain.AdaptationChapterPlan{{
		Chapter:       1,
		OutlineEntry:  domain.OutlineEntry{CoreEvent: "新增绑架支线"},
		AddedEventIDs: []string{"added-kidnap"},
	}}}
	err := finalizePlannerEventContracts(&proposal, ProposalOptions{Brief: "保留主线", Granularity: domain.AdaptationGranularityArc}, reports)
	if err == nil || !strings.Contains(err.Error(), "added_event_displaces_mainline") {
		t.Fatalf("expected mainline displacement error, got %v", err)
	}
}

func TestFinalizeArcAcceptsEachMainlineEventExactlyOnce(t *testing.T) {
	reports := []domain.AdaptationSourceReport{{Chapter: 1, KeyEvents: []string{"两人初遇", "案件真相揭晓"}}}
	events := sourceEventsFromReports(reports)
	proposal := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityArc, Chapters: []domain.AdaptationChapterPlan{
		{Chapter: 1, EventIDs: []string{events[0].ID}},
		{Chapter: 2, EventIDs: []string{events[1].ID}},
	}}
	if err := finalizePlannerEventContracts(&proposal, ProposalOptions{Brief: "保留主线", Granularity: domain.AdaptationGranularityArc}, reports); err != nil {
		t.Fatalf("finalize: %v", err)
	}
}

func TestValidateArcBatchEventCoverageRejectsForeignMainlineID(t *testing.T) {
	batch := plannerSkeletonBatch{
		TargetFrom:       5,
		TargetTo:         8,
		MainlineEventIDs: []string{"event-current"},
	}
	chapters := []domain.AdaptationChapterPlan{
		{Chapter: 5, EventIDs: []string{"event-current"}},
		{Chapter: 6, EventIDs: []string{"event-from-previous-batch"}},
	}

	err := validateArcBatchEventCoverage(chapters, batch)
	if err == nil || !strings.Contains(err.Error(), "is not assigned to detail batch 5-8") {
		t.Fatalf("expected foreign mainline ownership error, got %v", err)
	}
}

func TestFinalizeFreeBuildsIndependentTargetLedger(t *testing.T) {
	proposal := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityFree, Chapters: []domain.AdaptationChapterPlan{{Chapter: 1, Title: "新开端", OutlineEntry: domain.OutlineEntry{CoreEvent: "陌生人收到一封信"}}}}
	if err := finalizePlannerEventContracts(&proposal, ProposalOptions{Brief: "自由重构", Granularity: domain.AdaptationGranularityFree}, nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if len(proposal.TargetEventLedger) != 1 || !strings.HasPrefix(proposal.TargetEventLedger[0].ID, "tgt-") {
		t.Fatalf("ledger=%+v", proposal.TargetEventLedger)
	}
}

func TestPlannerSkeletonFromVolumeReviewPreservesMainlineContracts(t *testing.T) {
	review := domain.AdaptationVolumeReview{
		Granularity: domain.AdaptationGranularityArc, RewritePolicy: domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 2,
		Volumes: []domain.AdaptationVolumePlan{{
			Index: 1, TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 3,
			MainlineEventIDs: []string{"meet", "case"},
		}},
	}
	skeleton := plannerSkeletonFromVolumeReview(review)
	if len(skeleton.Batches) != 1 || !slices.Equal(skeleton.Batches[0].MainlineEventIDs, []string{"meet", "case"}) {
		t.Fatalf("mainline event contract was lost: %+v", skeleton.Batches)
	}
}

func TestAttachSkeletonEventsPublishesSupportingWhitelist(t *testing.T) {
	skeleton := plannerSkeleton{Granularity: domain.AdaptationGranularityArc, Batches: []plannerSkeletonBatch{{SourceFrom: 1, SourceTo: 1}}}
	reports := []domain.AdaptationSourceReport{{Chapter: 1, SourceEvents: []domain.AdaptationEvent{
		{ID: "src-main", SourceChapter: 1, Importance: domain.AdaptationEventMainline},
		{ID: "src-support", SourceChapter: 1, Importance: domain.AdaptationEventSupporting},
	}}}
	attachSkeletonMainlineEvents(&skeleton, reports)
	if !detailAuditContainsString(skeleton.Batches[0].MainlineEventIDs, "src-main") {
		t.Fatalf("mainline whitelist=%v", skeleton.Batches[0].MainlineEventIDs)
	}
	if !detailAuditContainsString(skeleton.Batches[0].AllowedEventIDs, "src-support") {
		t.Fatalf("allowed whitelist=%v", skeleton.Batches[0].AllowedEventIDs)
	}
}

func TestPlannerDetailEventContractPartitionsEveryStableEvent(t *testing.T) {
	parent := plannerSkeletonBatch{
		TargetFrom:                 1,
		TargetTo:                   8,
		DetailEventContractVersion: plannerDetailEventContractVersionPartitioned,
		MainlineEventIDs:           []string{"main-1", "main-2", "main-3"},
		AllowedEventIDs:            []string{"main-1", "support-1", "main-2", "texture-1", "main-3", "support-2", "texture-2"},
	}
	details := plannerDetailBatches([]plannerSkeletonBatch{parent}, 4)
	if len(details) != 2 {
		t.Fatalf("detail batches=%d, want 2", len(details))
	}
	counts := make(map[string]int)
	for _, detail := range details {
		allowed := make(map[string]bool)
		for _, eventID := range detail.AllowedEventIDs {
			counts[eventID]++
			allowed[eventID] = true
		}
		for _, eventID := range detail.MainlineEventIDs {
			if !allowed[eventID] {
				t.Fatalf("mainline %q is missing from its detail whitelist: %+v", eventID, detail)
			}
		}
	}
	for _, eventID := range parent.AllowedEventIDs {
		if counts[eventID] != 1 {
			t.Fatalf("stable event %q is assigned %d times, want once", eventID, counts[eventID])
		}
	}
}

func TestPlannerDetailBatchTreatsAcceptedPriorEventAsForbidden(t *testing.T) {
	legacy := plannerSkeletonBatch{
		TargetFrom:       5,
		TargetTo:         8,
		MainlineEventIDs: []string{"already-owned", "current-mainline"},
		AllowedEventIDs:  []string{"already-owned", "current-mainline", "current-support"},
	}
	previous := []domain.AdaptationChapterPlan{{Chapter: 4, EventIDs: []string{"already-owned"}}}
	batch := plannerDetailBatchWithPriorEventOwnership(legacy, previous)
	if !slices.Equal(batch.PriorOwnedEventIDs, []string{"already-owned"}) ||
		slices.Contains(batch.AllowedEventIDs, "already-owned") ||
		slices.Contains(batch.MainlineEventIDs, "already-owned") {
		t.Fatalf("legacy ownership contract=%+v", batch)
	}
	if err := validateArcBatchEventCoverage([]domain.AdaptationChapterPlan{{
		Chapter: 5, EventIDs: []string{"already-owned"},
	}}, batch); err == nil || !strings.Contains(err.Error(), "already owned by an earlier accepted") {
		t.Fatalf("event ownership error=%v", err)
	}
	if err := validateArcBatchEventCoverage([]domain.AdaptationChapterPlan{{
		Chapter: 5, EventIDs: []string{"current-mainline"}, PreserveEvents: []string{"already-owned"},
	}}, batch); err == nil || !strings.Contains(err.Error(), "preserve_events") {
		t.Fatalf("preserve ownership error=%v", err)
	}
}
