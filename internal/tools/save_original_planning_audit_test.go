package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSaveOriginalPlanningAuditSchemaIsStrictCompatible(t *testing.T) {
	tool := NewSaveOriginalPlanningAuditTool(nil)
	if !tool.StrictSchema() {
		t.Fatal("save_original_planning_audit should use strict tool calling")
	}
	toolSchema := tool.Schema()
	requireRequiredFields(t, toolSchema,
		"scope", "scope_id", "volume", "arc", "from_volume", "to_volume",
		"from_chapter", "to_chapter", "verdict", "summary", "dimensions", "issues",
		"observed_scene_counts",
	)
	requireAllPropertiesRequired(t, toolSchema)

	properties, ok := toolSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", toolSchema["properties"])
	}
	scopeID, ok := properties["scope_id"].(map[string]any)
	if !ok {
		t.Fatalf("scope_id schema missing: %#v", properties["scope_id"])
	}
	if got := scopeID["type"]; got != "string" {
		t.Fatalf("scope_id type = %v, want string", got)
	}
	for _, field := range []string{"dimensions", "issues", "observed_scene_counts"} {
		arraySchema, ok := properties[field].(map[string]any)
		if !ok {
			t.Fatalf("%s schema missing: %#v", field, properties[field])
		}
		itemSchema, ok := arraySchema["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s.items schema missing: %#v", field, arraySchema["items"])
		}
		requireAllPropertiesRequired(t, itemSchema)
		if field == "dimensions" {
			requireRequiredFields(t, itemSchema, "name", "score", "comment")
		} else if field == "observed_scene_counts" {
			requireRequiredFields(t, itemSchema, "chapter", "count")
		} else {
			requireRequiredFields(t, itemSchema, "severity", "volume", "arc", "from_chapter", "to_chapter", "description", "repair_instruction")
		}
	}
}

func TestSaveOriginalPlanningAuditAcceptsChapterScopeID(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	approveFoundationToolFixture(t, st)
	chapterID := domain.LegacyStructureID("audit-tool-test", domain.StructureKindChapter, "volume-1/arc-1/chapter-1")
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		ID: domain.LegacyStructureID("audit-tool-test", domain.StructureKindVolume, "volume-1"), Index: 1, Title: "Opening", Theme: "survival",
		Arcs: []domain.ArcOutline{{
			ID: domain.LegacyStructureID("audit-tool-test", domain.StructureKindArc, "volume-1/arc-1"), Index: 1, Title: "Wake", Goal: "survive the deadline",
			Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "Evidence", CoreEvent: "the heroine verifies changed evidence", Hook: "someone arrives", Scenes: []string{"verify the evidence"}}},
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	args, err := json.Marshal(map[string]any{
		"scope": "chapter", "scope_id": chapterID, "from_chapter": 1, "to_chapter": 1,
		"volume": 0, "arc": 0, "from_volume": 0, "to_volume": 0,
		"verdict": "pass", "summary": "the current chapter is causally complete",
		"dimensions":            originalAuditTestDimensions("causal_value", "character_logic", "continuity", "scene_progression", "hook_and_pacing", "originality"),
		"issues":                []map[string]any{},
		"observed_scene_counts": []map[string]any{{"chapter": 1, "count": 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSaveOriginalPlanningAuditTool(st).Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute with schema-advertised scope_id: %v", err)
	}

	audits, err := st.OriginalPlanningAudits.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].ScopeID != chapterID {
		t.Fatalf("saved chapter audit = %+v, want scope_id %q", audits, chapterID)
	}
}

func TestSaveOriginalPlanningAuditRejectsContradictoryObservedSceneCount(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	approveFoundationToolFixture(t, st)
	chapterID := domain.LegacyStructureID("audit-scene-count", domain.StructureKindChapter, "volume-1/arc-1/chapter-1")
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		ID: domain.LegacyStructureID("audit-scene-count", domain.StructureKindVolume, "volume-1"), Index: 1,
		Arcs: []domain.ArcOutline{{
			ID: domain.LegacyStructureID("audit-scene-count", domain.StructureKindArc, "volume-1/arc-1"), Index: 1,
			Chapters: []domain.OutlineEntry{{
				ID: chapterID, Chapter: 1, Title: "Evidence", CoreEvent: "complete chain",
				Hook: "handoff", Scenes: []string{"one", "two", "three", "four", "five"},
			}},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{
		"scope": "chapter", "scope_id": chapterID, "from_chapter": 1, "to_chapter": 1,
		"volume": 0, "arc": 0, "from_volume": 0, "to_volume": 0,
		"verdict": "revise", "summary": "claims the evidence is incomplete",
		"dimensions": originalAuditTestDimensions(
			"causal_value", "character_logic", "continuity",
			"scene_progression", "hook_and_pacing", "originality",
		),
		"issues": []map[string]any{{
			"severity": "blocking", "volume": 1, "arc": 1,
			"from_chapter": 1, "to_chapter": 1,
			"description": "claims scenes are missing", "repair_instruction": "add scenes",
		}},
		"observed_scene_counts": []map[string]any{{"chapter": 1, "count": 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSaveOriginalPlanningAuditTool(st).Execute(context.Background(), args); err == nil {
		t.Fatal("expected contradictory scene count to be rejected")
	}
}

func TestSaveOriginalPlanningAuditRejectsMoreThanFourRawChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	tool := NewSaveOriginalPlanningAuditTool(st)
	args, _ := json.Marshal(map[string]any{
		"scope": "arc", "volume": 1, "arc": 1, "from_chapter": 1, "to_chapter": 5,
		"verdict": "pass", "summary": "too large",
		"dimensions": originalAuditTestDimensions("causal_progression", "character_logic", "chapter_value", "continuity", "hook_and_pacing", "originality"),
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected >4 chapter audit batch to be rejected")
	}
}

func TestSaveOriginalPlanningAuditReviseRequiresPreciseRepairTarget(t *testing.T) {
	audit := domain.OriginalPlanningAudit{
		Scope: "book", Verdict: "revise", Summary: "ending is not earned",
		Dimensions: []domain.OriginalPlanningAuditDimension{
			{Name: "mainline_closure", Score: 6, Comment: "missing decision"},
			{Name: "character_closure", Score: 8, Comment: "closed"},
			{Name: "setup_payoff", Score: 8, Comment: "closed"},
			{Name: "escalation_pacing", Score: 8, Comment: "balanced"},
			{Name: "world_consistency", Score: 8, Comment: "consistent"},
			{Name: "originality", Score: 8, Comment: "distinct"},
			{Name: "ending_delivery", Score: 6, Comment: "missing decision"},
		},
		Issues: []domain.OriginalPlanningAuditIssue{{Severity: "major", Description: "ending skips the heroine's choice", RepairInstruction: "add the decisive choice"}},
	}
	if err := validateOriginalPlanningAudit(audit); err == nil {
		t.Fatal("expected revise issue without volume/arc to be rejected")
	}
}

func TestSaveOriginalPlanningSkeletonAuditCanRepairAWholeVolume(t *testing.T) {
	audit := domain.OriginalPlanningAudit{
		Scope: "skeleton_volume", Volume: 3, Verdict: "revise", Summary: "the final volume does not close the book",
		Dimensions: []domain.OriginalPlanningAuditDimension{
			{Name: "volume_function", Score: 6, Comment: "opens another investigation"},
			{Name: "arc_causality", Score: 8, Comment: "causal"},
			{Name: "character_progression", Score: 7, Comment: "partial"},
			{Name: "conflict_escalation", Score: 8, Comment: "escalates"},
			{Name: "budget_capacity", Score: 8, Comment: "fits"},
			{Name: "payoff_and_handoff", Score: 4, Comment: "no ending"},
		},
		Issues: []domain.OriginalPlanningAuditIssue{{
			Severity: "major", Volume: 3, Description: "the last volume only opens the old case", RepairInstruction: "replace the whole volume with an ending volume",
		}},
	}
	if err := validateOriginalPlanningAudit(audit); err != nil {
		t.Fatalf("whole-volume skeleton repair should be valid: %v", err)
	}
}

func originalAuditTestDimensions(names ...string) []map[string]any {
	result := make([]map[string]any, 0, len(names))
	for _, name := range names {
		result = append(result, map[string]any{"name": name, "score": 8, "comment": "supported by the bounded batch"})
	}
	return result
}
