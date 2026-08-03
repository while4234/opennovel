package domain

import (
	"encoding/json"
	"testing"
)

func TestAdaptationChapterPlanJSONIncludesOutlineAndBudgetFields(t *testing.T) {
	raw := []byte(`{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "reshape arcs",
		"planner": {"prompt": "adaptation-planner", "prompt_version": "v1", "model": "test-model"},
		"chapters": [{
			"chapter": 1,
			"title": "New opening",
			"core_event": "The lead accepts the altered call",
			"hook": "A hidden debt is named",
			"scenes": ["call", "choice"],
			"source_chapters": [1, 2],
			"is_added": true,
			"word_budget": {
				"source_runes": 1200,
				"target_runes": 1500,
				"min_runes": 1300,
				"max_runes": 1700,
				"tolerance": 0.15
			}
		}]
	}`)

	var plan AdaptationPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if plan.Planner == nil || plan.Planner.Prompt != "adaptation-planner" || plan.Planner.Model != "test-model" {
		t.Fatalf("planner metadata mismatch: %+v", plan.Planner)
	}
	chapter := plan.Chapters[0]
	if chapter.CoreEvent == "" || chapter.Hook == "" || len(chapter.Scenes) != 2 {
		t.Fatalf("outline fields not loaded: %+v", chapter.OutlineEntry)
	}
	if len(chapter.SourceChapters) != 2 || !chapter.IsAdded {
		t.Fatalf("source mapping fields not loaded: %+v", chapter)
	}
	if chapter.WordBudget == nil || chapter.WordBudget.TargetRunes != 1500 || chapter.WordBudget.MinRunes != 1300 {
		t.Fatalf("word budget not loaded: %+v", chapter.WordBudget)
	}
}
