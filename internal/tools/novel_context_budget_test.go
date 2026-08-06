package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestPlanningVolumeRequiresPositiveSelector(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	tool := NewContextTool(st, References{}, "default")
	for _, args := range []string{`{"scope":"planning_volume"}`, `{"scope":"planning_volume","volume":-1}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), "requires a positive volume") {
			t.Fatalf("Execute(%s) error=%v, want required positive volume", args, err)
		}
	}
}

func TestPlanningFinalJSONBudgetFailsClosedWithSectionSizes(t *testing.T) {
	result := map[string]any{
		"planning_memory": map[string]any{"unexpected_unbounded_source": strings.Repeat("界", planningContextSourceBytes)},
	}
	_, err := marshalBoundedContext("planning", result)
	if err == nil {
		t.Fatal("oversized final planning JSON must fail closed")
	}
	for _, required := range []string{"planning context", "final JSON budget", "planning_memory="} {
		if !strings.Contains(err.Error(), required) {
			t.Fatalf("error %q missing %q", err, required)
		}
	}
}

func TestPlanningReviewSelectorKeepsTargetExactAndBoundsAdjacentVolumes(t *testing.T) {
	volumes := make([]domain.VolumeOutline, 0, 3)
	for index := 1; index <= 3; index++ {
		volumes = append(volumes, domain.VolumeOutline{
			Index: index,
			Title: strings.Repeat(string(rune('A'+index)), 90),
			Theme: strings.Repeat("target-theme-", 40) + string(rune('0'+index)),
			Arcs: []domain.ArcOutline{{
				Index:             1,
				Title:             strings.Repeat("arc-title-", 10),
				Goal:              strings.Repeat("causal-goal-", 50) + string(rune('0'+index)),
				EstimatedChapters: 4,
			}},
		})
	}
	projection := compactLayeredOutlineForPlanningReviewVolumes(
		volumes,
		&domain.Progress{CurrentVolume: 1, CurrentArc: 1},
		3,
		3,
	)
	if len(projection) != 2 {
		t.Fatalf("review projection volumes=%d, want target plus one adjacent volume", len(projection))
	}
	adjacent, target := projection[0], projection[1]
	if target["theme"] != volumes[2].Theme {
		t.Fatalf("target theme changed: got %q want %q", target["theme"], volumes[2].Theme)
	}
	targetArcs := target["arcs"].([]map[string]any)
	if targetArcs[0]["goal"] != volumes[2].Arcs[0].Goal {
		t.Fatalf("target arc goal changed: got %q want %q", targetArcs[0]["goal"], volumes[2].Arcs[0].Goal)
	}
	if adjacent["theme"] == volumes[1].Theme {
		t.Fatal("adjacent volume theme must remain bounded")
	}
	adjacentArcs := adjacent["arcs"].([]map[string]any)
	if adjacentArcs[0]["goal"] == volumes[1].Arcs[0].Goal {
		t.Fatal("adjacent volume arc goal must remain bounded")
	}
}

func TestPrePremisePlanningScopesRetainUniquePreferences(t *testing.T) {
	tests := []struct {
		name string
		args string
	}{
		{name: "planning", args: `{"scope":"planning"}`},
		{name: "planning volume", args: `{"scope":"planning_volume","volume":1}`},
		{name: "planning detail", args: `{"scope":"planning_detail","volume":1,"arc":1}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := planningPreferenceStore(t, "canonical brief before premise", "unique preference for "+tc.name, false, true)
			raw, err := NewContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if !strings.Contains(text, "unique preference for "+tc.name) {
				t.Fatalf("%s removed the pre-premise preference: %s", tc.name, text)
			}
			if strings.Contains(text, `"preferences_source"`) {
				t.Fatalf("%s emitted a false preferences_source before the premise was published", tc.name)
			}
			if tc.name == "planning volume" && !strings.Contains(text, `"memory_policy"`) {
				t.Fatal("planning_volume must retain the bounded Architect memory policy")
			}
		})
	}
}

func TestPublishedFoundationWithoutCreativeBriefRetainsLegacyPreferences(t *testing.T) {
	st := planningPreferenceStore(t, "", "legacy preference without creative brief", true, false)
	raw, err := NewContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(`{"scope":"planning"}`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "legacy preference without creative brief") || strings.Contains(text, `"preferences_source"`) {
		t.Fatalf("legacy preference contract was not preserved: %s", text)
	}
}

func TestPublishedFoundationWithCreativeBriefUsesDigestPreferenceSource(t *testing.T) {
	st := planningPreferenceStore(t, "published canonical brief", "duplicate published preference", true, true)
	raw, err := NewContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(`{"scope":"planning"}`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "duplicate published preference") {
		t.Fatalf("published duplicate preference was retained: %s", text)
	}
	for _, required := range []string{`"content_digest"`, `"content_source"`, `"preferences_source":"planning_memory.creative_brief"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("published brief projection missing %s: %s", required, text)
		}
	}
}

func planningPreferenceStore(t *testing.T, brief, preference string, publishedPremise, includeBrief bool) *store.Store {
	t.Helper()
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	premise := ""
	if publishedPremise {
		premise = "published canonical premise"
	}
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    premise,
		Characters: []domain.Character{{ID: "lead", Name: "Lead", Role: "protagonist", Tier: "core"}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	if includeBrief {
		if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
			Status: domain.PlanningReviewStatusCollecting,
			Kind:   domain.PlanningReviewKindBlueprint,
			Brief:  brief,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UserRules.Save(&rules.Snapshot{
		Version:     rules.SnapshotVersion,
		Status:      rules.StatusReady,
		Preferences: preference,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "Volume One",
		Arcs:  []domain.ArcOutline{{Index: 1, Title: "Opening", Goal: "establish the conflict", EstimatedChapters: 3}},
	}}); err != nil {
		t.Fatal(err)
	}
	return st
}
