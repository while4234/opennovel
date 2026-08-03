package domain

import "testing"

func TestAdaptationOutlineQualityAuditInvalidatesWhenContractChanges(t *testing.T) {
	plan := AdaptationPlan{
		Granularity:  AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{{ID: "event-1", Description: "黑衣人拦路抢劫", Importance: AdaptationEventSupporting}},
		Chapters: []AdaptationChapterPlan{{
			Chapter:      1,
			OutlineEntry: OutlineEntry{Title: "冲突", CoreEvent: "黑衣人拦路抢劫。"},
			EventIDs:     []string{"event-1"},
		}},
	}
	MarkAdaptationOutlineQualityPassed(&plan)
	if !AdaptationOutlineQualityPassed(plan) {
		t.Fatal("fresh outline audit should be reusable")
	}
	plan.Chapters[0].EventIDs = nil
	if AdaptationOutlineQualityPassed(plan) {
		t.Fatal("changing event ownership must invalidate outline audit")
	}
}

func TestAdaptationOutlineQualityAuditInvalidatesWhenBudgetChanges(t *testing.T) {
	plan := AdaptationPlan{
		Granularity: AdaptationGranularityArc,
		Chapters: []AdaptationChapterPlan{{
			Chapter: 1, TargetRunes: 1400, TargetMinRunes: 1200, TargetMaxRunes: 1600,
		}},
	}
	MarkAdaptationOutlineQualityPassed(&plan)
	plan.Chapters[0].TargetMaxRunes = 4600
	if AdaptationOutlineQualityPassed(plan) {
		t.Fatal("changing chapter budget must invalidate outline audit")
	}
}

func TestLayeredAdaptationOutlineAuditRequiresDigestAndInvalidatesWithPlan(t *testing.T) {
	plan := AdaptationPlan{Granularity: AdaptationGranularityArc, Chapters: []AdaptationChapterPlan{{
		Chapter: 1, Title: "一", OutlineEntry: OutlineEntry{CoreEvent: "事件", Hook: "钩子"},
	}}}
	MarkAdaptationOutlineQualityPassedWithLayers(&plan, "layered-digest")
	if !AdaptationOutlineQualityPassed(plan) || plan.OutlineQualityAudit.Version != AdaptationOutlineQualityAuditVersion {
		t.Fatalf("layered audit should pass: %+v", plan.OutlineQualityAudit)
	}
	plan.OutlineQualityAudit.LayeredAuditDigest = ""
	if AdaptationOutlineQualityPassed(plan) {
		t.Fatal("version 2 audit without layered digest must fail")
	}
	MarkAdaptationOutlineQualityPassedWithLayers(&plan, "layered-digest")
	plan.Chapters[0].CoreEvent = "改变"
	if AdaptationOutlineQualityPassed(plan) {
		t.Fatal("changing the plan must invalidate the layered audit")
	}
}

func TestValidateArcEventOutlineThemesFindsLaterOwner(t *testing.T) {
	plan := AdaptationPlan{
		Granularity:  AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{{ID: "event-1", Description: "黑衣人拦路抢劫", Importance: AdaptationEventSupporting}},
		Chapters: []AdaptationChapterPlan{
			{Chapter: 1, OutlineEntry: OutlineEntry{CoreEvent: "主角返校与室友重逢。"}, EventIDs: []string{"event-1"}},
			{Chapter: 2, OutlineEntry: OutlineEntry{CoreEvent: "黑衣人拦路抢劫，主角出手制止。"}},
		},
	}
	issues := ValidateArcEventOutlineThemes(plan)
	if len(issues) != 1 || issues[0].TargetChapter != 1 || issues[0].AlternativeChapters[0] != 2 {
		t.Fatalf("unexpected theme mismatch: %+v", issues)
	}
}

func TestValidateArcSourceEventBindingsRequiresPreserveEventAlignment(t *testing.T) {
	plan := AdaptationPlan{
		Granularity: AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{
			{ID: "src-1", Description: "事件一"},
			{ID: "src-2", Description: "事件二"},
		},
		Chapters: []AdaptationChapterPlan{{
			Chapter: 1, EventIDs: []string{"src-1"}, PreserveEvents: []string{"src-2"},
		}},
	}
	issues := ValidateArcSourceEventBindings(plan)
	if len(issues) != 2 {
		t.Fatalf("expected preserve alignment issues, got %+v", issues)
	}
	for _, issue := range issues {
		if issue.Code != "arc_event_preserve_mismatch" && issue.Code != "arc_event_preserve_unbound" {
			t.Fatalf("unexpected preserve alignment issue: %+v", issue)
		}
	}
}

func TestValidateArcSourceEventBindingsAcceptsAnnotatedPreserveEventID(t *testing.T) {
	plan := AdaptationPlan{
		Granularity:  AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{{ID: "src-0298-e01-ad111f23", Description: "事件一"}},
		Chapters: []AdaptationChapterPlan{{
			Chapter: 1, EventIDs: []string{"src-0298-e01-ad111f23"},
			PreserveEvents: []string{"src-0298-e01-ad111f23：事件一的完整描述"},
		}},
	}
	if issues := ValidateArcSourceEventBindings(plan); len(issues) != 0 {
		t.Fatalf("annotated stable preserve ID should align with event_ids: %+v", issues)
	}
}

func TestNormalizeSourceEventReferencesCanonicalizesAnnotationsAndKeepsProse(t *testing.T) {
	got := NormalizeSourceEventReferences([]string{
		"src-0298-e01-ad111f23：事件一的完整描述",
		"保留普通剧情描述",
		"src-0298-e01-ad111f23: duplicate annotation",
	})
	want := []string{"src-0298-e01-ad111f23", "保留普通剧情描述"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("normalized references=%v, want %v", got, want)
	}
}

func TestValidateArcEventOutlineThemesTrustsExplicitSourceLineage(t *testing.T) {
	plan := AdaptationPlan{
		Granularity: AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{{
			ID: "src-0076-e01", Description: "凌雪伤打电话邀请段天狼参加父亲生日宴会", SourceChapter: 76,
		}},
		Chapters: []AdaptationChapterPlan{
			{Chapter: 68, SourceRange: SourceRange{From: 71, To: 81}, OutlineEntry: OutlineEntry{CoreEvent: "凌雪伤安排段天狼参加父亲生日宴会"}, EventIDs: []string{"src-0076-e01"}},
			{Chapter: 69, SourceRange: SourceRange{From: 82, To: 90}, OutlineEntry: OutlineEntry{CoreEvent: "主角接到紧急电话后驱车离开"}},
		},
	}
	if issues := ValidateArcEventOutlineThemes(plan); len(issues) != 0 {
		t.Fatalf("source-compatible owner should not be displaced by fuzzy keywords: %+v", issues)
	}
}

func TestValidateArcChapterBudgetDensityRejectsImpossibleScenePacking(t *testing.T) {
	plan := AdaptationPlan{
		Granularity: AdaptationGranularityArc,
		Chapters: []AdaptationChapterPlan{{
			Chapter:      39,
			OutlineEntry: OutlineEntry{Scenes: make([]string, 9)},
			TargetRunes:  1400, TargetMinRunes: 1200, TargetMaxRunes: 1600,
		}},
	}
	issues := ValidateArcChapterBudgetDensity(plan)
	if len(issues) != 1 || issues[0].Chapter != 39 || issues[0].RecommendedMinRunes != 2700 {
		t.Fatalf("unexpected budget density issues: %+v", issues)
	}
	plan.Chapters[0].TargetMaxRunes = 4600
	if issues := ValidateArcChapterBudgetDensity(plan); len(issues) != 0 {
		t.Fatalf("scene-capable budget should pass: %+v", issues)
	}
}

func TestValidateArcChapterBudgetDensityRejectsTinyPositiveLegacyBudget(t *testing.T) {
	plan := AdaptationPlan{
		Granularity: AdaptationGranularityArc,
		Chapters: []AdaptationChapterPlan{{
			Chapter:      51,
			OutlineEntry: OutlineEntry{Scenes: make([]string, 6)},
			TargetRunes:  792, TargetMinRunes: 673, TargetMaxRunes: 911,
		}},
	}

	issues := ValidateArcChapterBudgetDensity(plan)
	if len(issues) != 1 || issues[0].Chapter != 51 || issues[0].RecommendedMinRunes != 1800 {
		t.Fatalf("tiny positive legacy budget should be rejected: %+v", issues)
	}
}

func TestRepairArcChapterBudgetDensityExpandsLegacyBudgets(t *testing.T) {
	plan := AdaptationPlan{
		Granularity: AdaptationGranularityArc,
		Chapters: []AdaptationChapterPlan{
			{Chapter: 1, OutlineEntry: OutlineEntry{Scenes: make([]string, 6)}, TargetRunes: 1200, TargetMinRunes: 1000, TargetMaxRunes: 1400,
				WordBudget: &AdaptationChapterWordBudget{SourceRunes: 800, TargetRunes: 1200, MinRunes: 1000, MaxRunes: 1400}},
			{Chapter: 2, OutlineEntry: OutlineEntry{Scenes: make([]string, 7)}, TargetRunes: 1400, TargetMinRunes: 1200, TargetMaxRunes: 1600,
				WordBudget: &AdaptationChapterWordBudget{SourceRunes: 1000, TargetRunes: 1400, MinRunes: 1200, MaxRunes: 1600}},
			{Chapter: 3, OutlineEntry: OutlineEntry{Scenes: make([]string, 2)}, TargetRunes: 1000, TargetMinRunes: 800, TargetMaxRunes: 2000},
		},
	}

	repaired := RepairArcChapterBudgetDensity(&plan)
	if len(repaired) != 2 || repaired[0] != 1 || repaired[1] != 2 {
		t.Fatalf("unexpected repaired chapters: %v", repaired)
	}
	if got := plan.Chapters[0]; got.TargetRunes != 3500 || got.TargetMinRunes != 3000 || got.TargetMaxRunes != 4000 || got.WordBudget.MaxRunes != 4000 {
		t.Fatalf("six-scene budget was not repaired: %+v", got)
	}
	if got := plan.Chapters[1]; got.TargetRunes != 4000 || got.TargetMinRunes != 3400 || got.TargetMaxRunes != 4600 || got.WordBudget.MaxRunes != 4600 {
		t.Fatalf("seven-scene budget was not repaired: %+v", got)
	}
	if plan.TargetTotalRunes != 8500 || plan.TargetMinRunes != 7200 || plan.TargetMaxRunes != 10600 {
		t.Fatalf("plan totals were not rebuilt: %d/%d/%d", plan.TargetTotalRunes, plan.TargetMinRunes, plan.TargetMaxRunes)
	}
	if len(ValidateArcChapterBudgetDensity(plan)) != 0 {
		t.Fatalf("repaired plan still violates density: %+v", ValidateArcChapterBudgetDensity(plan))
	}
}

func TestValidateArcEventOutlineThemesFindsFinancialEventMovedToLaterChapter(t *testing.T) {
	plan := AdaptationPlan{
		Granularity: AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{{
			ID: "src-0062-e01", Description: "龙过海询问飞龙集团危机真相", Importance: AdaptationEventMainline, SourceChapter: 62,
		}},
		Chapters: []AdaptationChapterPlan{
			{Chapter: 39, SourceRange: SourceRange{From: 50, To: 55}, OutlineEntry: OutlineEntry{CoreEvent: "龙过海接到电话后前往龙天翔办公室，随后发生书城劫持事件。"}, EventIDs: []string{"src-0062-e01"}},
			{Chapter: 44, SourceRange: SourceRange{From: 62, To: 65}, OutlineEntry: OutlineEntry{CoreEvent: "龙天翔邀请段天狼加入飞龙集团，双方讨论资金链危机。"}},
		},
	}
	issues := ValidateArcEventOutlineThemes(plan)
	if len(issues) != 1 || issues[0].TargetChapter != 39 || len(issues[0].AlternativeChapters) != 1 || issues[0].AlternativeChapters[0] != 44 {
		t.Fatalf("unexpected financial theme mismatch: %+v", issues)
	}
}

func TestAdaptationPlanBudgetRepairLineageSignatureIgnoresEventBindingRepair(t *testing.T) {
	base := AdaptationPlan{
		Granularity: AdaptationGranularityArc,
		SourceEvents: []AdaptationEvent{{
			ID: "src-0001-e01", Description: "the source event", SourceChapter: 1,
		}},
		Chapters: []AdaptationChapterPlan{{
			Chapter: 1,
			Title:   "The contract",
			OutlineEntry: OutlineEntry{
				CoreEvent: "the core event",
				Hook:      "the hook",
				Scenes:    []string{"beat one", "beat two"},
			},
			TargetRunes:    2000,
			TargetMinRunes: 1600,
			TargetMaxRunes: 2400,
			EventIDs:       []string{"src-0001-e01"},
			PreserveEvents: []string{"src-0001-e01"},
		}},
	}
	repaired := base
	repaired.Chapters = append([]AdaptationChapterPlan(nil), base.Chapters...)
	repaired.Chapters[0].EventIDs = []string{"src-0001-e02"}
	repaired.Chapters[0].PreserveEvents = []string{"src-0001-e02"}

	if AdaptationPlanStoryContractSignature(base) == AdaptationPlanStoryContractSignature(repaired) {
		t.Fatal("story signature should detect event-binding changes")
	}
	if AdaptationPlanBudgetRepairLineageSignature(base) != AdaptationPlanBudgetRepairLineageSignature(repaired) {
		t.Fatal("budget lineage should ignore targeted event-binding changes")
	}

	repaired.Chapters[0].CoreEvent = "a different story"
	if AdaptationPlanBudgetRepairLineageSignature(base) == AdaptationPlanBudgetRepairLineageSignature(repaired) {
		t.Fatal("budget lineage should detect plot changes")
	}
}
