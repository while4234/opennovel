package adapt

import (
	"context"
	"reflect"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestReanalyzeLegacyArcChapterBudgetsRetriesBudgetOnlyUntilDensityPasses(t *testing.T) {
	model := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{"chapters":[{"chapter":1,"target_runes":900,"min_runes":800,"max_runes":1000}]}`},
		{text: `{"chapters":[{"chapter":1,"target_runes":2100,"min_runes":1900,"max_runes":2300}]}`},
	}}
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Brief:       "keep the story unchanged",
		Chapters: []domain.AdaptationChapterPlan{{
			OutlineEntry: domain.OutlineEntry{
				Title: "opening", CoreEvent: "the clue is found",
				Scenes: []string{"one", "two", "three", "four", "five", "six"},
			},
			Chapter:        1,
			TargetRunes:    900,
			TargetMinRunes: 800,
			TargetMaxRunes: 1000,
			WordBudget:     &domain.AdaptationChapterWordBudget{TargetRunes: 900, MinRunes: 800, MaxRunes: 1000},
		}},
	}
	before := plan.Chapters[0]
	result, err := ReanalyzeLegacyArcChapterBudgets(context.Background(), Deps{
		LLM:                                    model,
		AdaptationOutlineAuditRetryMaxAttempts: 1,
		ModelCallMaxAttempts:                   1,
	}, plan)
	if err != nil {
		t.Fatalf("ReanalyzeLegacyArcChapterBudgets: %v", err)
	}
	if model.calls != 2 || result.Attempts != 2 {
		t.Fatalf("model calls=%d attempts=%d, want 2/2", model.calls, result.Attempts)
	}
	if !reflect.DeepEqual(result.Chapters, []int{1}) {
		t.Fatalf("affected chapters=%v", result.Chapters)
	}
	after := result.Plan.Chapters[0]
	if after.Title != before.Title || after.CoreEvent != before.CoreEvent || !reflect.DeepEqual(after.Scenes, before.Scenes) {
		t.Fatalf("budget repair changed story fields: before=%+v after=%+v", before, after)
	}
	if after.TargetRunes != 2100 || after.TargetMinRunes != 1900 || after.TargetMaxRunes != 2300 {
		t.Fatalf("budget=%d/%d/%d", after.TargetMinRunes, after.TargetRunes, after.TargetMaxRunes)
	}
	if issues := domain.ValidateArcChapterBudgetDensity(result.Plan); len(issues) != 0 {
		t.Fatalf("density issues remain: %+v", issues)
	}
}

func TestReconcileLegacyArcEventBindingsChangesOnlyOwnershipFields(t *testing.T) {
	model := &scriptedAdaptLLM{responses: []adaptLLMResponse{{
		text: `{"chapters":[{"chapter":1,"event_ids":["src-e1","src-e2"],"preserve_events":["src-e1","src-e2"]}]}`,
	}}}
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		SourceEvents: []domain.AdaptationEvent{
			{ID: "src-e1", Description: "the first clue", Importance: domain.AdaptationEventSupporting},
			{ID: "src-e2", Description: "the second clue", Importance: domain.AdaptationEventSupporting},
		},
		Chapters: []domain.AdaptationChapterPlan{{
			OutlineEntry: domain.OutlineEntry{
				Title: "opening", CoreEvent: "the clue is found", Scenes: []string{"one"},
			},
			Chapter:        1,
			EventIDs:       []string{"src-e1"},
			PreserveEvents: []string{"src-e1", "src-e2"},
		}},
	}
	before := plan.Chapters[0]
	result, err := ReconcileLegacyArcEventBindings(context.Background(), Deps{
		LLM:                                    model,
		AdaptationOutlineAuditRetryMaxAttempts: 0,
		ModelCallMaxAttempts:                   1,
	}, plan)
	if err != nil {
		t.Fatalf("ReconcileLegacyArcEventBindings: %v", err)
	}
	after := result.Plan.Chapters[0]
	if result.Attempts != 1 || model.calls != 1 {
		t.Fatalf("model calls=%d attempts=%d, want 1/1", model.calls, result.Attempts)
	}
	if after.Title != before.Title || after.CoreEvent != before.CoreEvent || !reflect.DeepEqual(after.Scenes, before.Scenes) {
		t.Fatalf("event repair changed story fields: before=%+v after=%+v", before, after)
	}
	if !reflect.DeepEqual(after.EventIDs, []string{"src-e1", "src-e2"}) ||
		!reflect.DeepEqual(after.PreserveEvents, []string{"src-e1", "src-e2"}) {
		t.Fatalf("ownership fields=%v/%v", after.EventIDs, after.PreserveEvents)
	}
	if issues := domain.ValidateArcSourceEventBindings(result.Plan); len(issues) != 0 {
		t.Fatalf("binding issues remain: %+v", issues)
	}
}

func TestReconcileLegacyArcEventBindingsForChapterMovesMismatchWithItsSourceLineage(t *testing.T) {
	model := &scriptedAdaptLLM{responses: []adaptLLMResponse{{
		text: `{"chapters":[{"chapter":1,"event_ids":[],"preserve_events":[]},{"chapter":2,"event_ids":["src-0062-e01"],"preserve_events":["src-0062-e01"]}]}`,
	}}}
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		SourceEvents: []domain.AdaptationEvent{{
			ID: "src-0062-e01", Description: "龙过海询问飞龙集团危机真相", SourceChapter: 62,
		}},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, SourceRange: domain.SourceRange{From: 50, To: 55}, OutlineEntry: domain.OutlineEntry{CoreEvent: "书城劫持事件"}, EventIDs: []string{"src-0062-e01"}, PreserveEvents: []string{"src-0062-e01"}},
			{Chapter: 2, SourceRange: domain.SourceRange{From: 62, To: 65}, OutlineEntry: domain.OutlineEntry{CoreEvent: "调查飞龙集团资金链危机"}},
		},
	}
	result, err := ReconcileLegacyArcEventBindingsForChapter(context.Background(), Deps{
		LLM: model, AdaptationOutlineAuditRetryMaxAttempts: 0, ModelCallMaxAttempts: 1,
	}, plan, 1)
	if err != nil {
		t.Fatalf("ReconcileLegacyArcEventBindingsForChapter: %v", err)
	}
	if result.Attempts != 1 || model.calls != 1 || !reflect.DeepEqual(result.Chapters, []int{1, 2}) {
		t.Fatalf("attempts=%d calls=%d chapters=%v", result.Attempts, model.calls, result.Chapters)
	}
	if len(result.Plan.Chapters[0].EventIDs) != 0 || !reflect.DeepEqual(result.Plan.Chapters[1].EventIDs, []string{"src-0062-e01"}) {
		t.Fatalf("event ownership was not moved with source lineage: %+v", result.Plan.Chapters)
	}
}
