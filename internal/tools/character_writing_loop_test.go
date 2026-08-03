package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func characterLoopStore(t *testing.T, characters []domain.Character, outline []domain.OutlineEntry) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Characters: characters, RelationshipsReviewed: true,
	}, 0); err != nil {
		t.Fatalf("SaveCAS Foundation: %v", err)
	}
	if err := st.Outline.SaveOutline(outline); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	return st
}

func loopCharacter(id, name, tier string) domain.Character {
	return domain.Character{
		ID: id, Name: name, Tier: tier, Role: "role", Description: "description",
		Arc: "arc", Traits: []string{"trait"}, Goal: "long goal",
		Motivation: "long motivation", Voice: "distinct voice",
		Constraints: []string{"must preserve"},
		KnowledgeBoundary: &domain.CharacterKnowledgeBoundary{
			Known: []string{"public fact"}, Unknown: []string{"secret"},
		},
	}
}

func TestCharacterWorksetPrefersIDsAndStaysBounded(t *testing.T) {
	characters := []domain.Character{loopCharacter("hero", "Hero", "core")}
	for index := 0; index < 24; index++ {
		character := loopCharacter("support-"+string(rune('a'+index)), "Support "+string(rune('a'+index)), "important")
		character.Description = strings.Repeat("long card ", 5000)
		characters = append(characters, character)
	}
	st := characterLoopStore(t, characters, []domain.OutlineEntry{{
		Chapter: 1, CharacterIDs: []string{"support-x"},
	}})
	if err := st.World.AppendStateChanges([]domain.StateChange{{
		Chapter: 1, CharacterID: "support-x", Entity: "Support x",
		Field: "voice", NewValue: "runtime voice override",
	}}); err != nil {
		t.Fatalf("AppendStateChanges: %v", err)
	}
	workset, err := NewContextTool(st, References{}, "").buildCharacterWorkset(1)
	if err != nil {
		t.Fatalf("buildCharacterWorkset: %v", err)
	}
	if workset.Diagnostics.SelectionMode != "stable_id" ||
		len(workset.Full) == 0 || workset.Full[0].ID != "support-x" {
		t.Fatalf("ID-first workset = %+v", workset)
	}
	if len(workset.Full) > characterWorksetMaxFullCards ||
		len(workset.Compressed) > characterWorksetMaxCompressed ||
		workset.Diagnostics.EncodedBytes > characterWorksetBudgetBytes {
		t.Fatalf("unbounded workset diagnostics = %+v", workset.Diagnostics)
	}
	if len(workset.Diagnostics.CompactedIDs) != 1 || workset.Diagnostics.CompactedIDs[0] != "support-x" {
		t.Fatalf("oversized full-card compaction = %+v", workset.Diagnostics)
	}
	if len(workset.Conflicts) != 1 ||
		workset.Conflicts[0].CharacterID != "support-x" ||
		workset.Conflicts[0].Field != "voice" {
		t.Fatalf("static/dynamic conflicts = %+v", workset.Conflicts)
	}
}

func TestPlanCharacterContractRejectsKnowledgeLeak(t *testing.T) {
	character := loopCharacter("hero", "Hero", "core")
	st := characterLoopStore(t, []domain.Character{character}, []domain.OutlineEntry{{
		Chapter: 1, CharacterIDs: []string{"hero"},
	}})
	plan := domain.ChapterPlan{Chapter: 1, Contract: domain.ChapterContract{
		Characters: []domain.ChapterCharacterContract{{
			CharacterID: "hero", Goal: "act", ImmediateMotivation: "now",
			StartState: "ready", VoiceBehavior: []string{"terse"},
			MustPreserve: []string{"identity"}, Known: []string{"secret"},
		}},
	}}
	if err := NewPlanChapterTool(st).validateCharacterContracts(&plan); err == nil {
		t.Fatal("knowledge leak was accepted")
	}
	plan.Contract.Characters[0].Known = []string{"public fact"}
	if err := NewPlanChapterTool(st).validateCharacterContracts(&plan); err != nil {
		t.Fatalf("valid contract rejected: %v", err)
	}
}

func TestCheckConsistencyReturnsBlockingCharacterFindingsWithoutCheckpoint(t *testing.T) {
	st := characterLoopStore(t, []domain.Character{loopCharacter("hero", "Hero", "core")}, nil)
	if err := st.Drafts.SaveDraft(1, "# chapter\nprose"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"findings": []domain.ConsistencyIssue{
			{
				Type: "ooc", Severity: "error", CharacterID: "hero",
				Scene: "scene 1", Evidence: "abandons the established constraint",
				ViolatedField: "constraints", Description: "out of character",
				Suggestion: "restore the constraint",
			},
			{
				Type: "voice_drift", Severity: "error", CharacterID: "hero",
				Scene: "scene 1", Evidence: "uses ornate monologues",
				ViolatedField: "voice", Description: "voice drift",
				Suggestion: "restore terse diction",
			},
			{
				Type: "motivation_break", Severity: "error", CharacterID: "hero",
				Scene: "scene 1", Evidence: "acts without immediate motive",
				ViolatedField: "immediate_motivation", Description: "motivation break",
				Suggestion: "supply a causal trigger",
			},
			{
				Type: "knowledge_leak", Severity: "error", CharacterID: "hero",
				Scene: "scene 1", Evidence: "states the secret",
				ViolatedField: "knowledge_boundary.unknown",
				Description:   "premature knowledge", Suggestion: "show acquisition first",
			},
			{
				Type: "relationship_jump", Severity: "error", CharacterID: "hero",
				Scene: "scene 1", Evidence: "trust jumps without an event",
				ViolatedField: "relationship_state", Description: "relationship jump",
				Suggestion: "add a relationship beat",
			},
		},
	})
	raw, err := NewCheckConsistencyTool(st).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	if findings, ok := result["findings"].([]any); !ok || len(findings) != 5 {
		t.Fatalf("structured findings = %s", raw)
	}
	if result["passed"] != false || st.Checkpoints.LatestByStep(domain.ChapterScope(1), "consistency_check") != nil {
		t.Fatalf("blocking result/checkpoint = %s", raw)
	}
}

func TestCommitDynamicStateRejectsStaticCharacterMutation(t *testing.T) {
	st := characterLoopStore(t, []domain.Character{loopCharacter("hero", "Hero", "core")}, nil)
	tool := NewCommitChapterTool(st)
	err := tool.validateDynamicCharacterUpdates(nil, nil, []domain.StateChange{{
		CharacterID: "hero", Entity: "Hero", Field: "voice", NewValue: "new identity voice",
	}})
	if err == nil {
		t.Fatal("static character mutation was accepted")
	}
}

func TestCommitCharacterIDsMustMatchChapterContract(t *testing.T) {
	st := characterLoopStore(t, []domain.Character{
		loopCharacter("lin_shuran", "林舒然", "core"),
		loopCharacter("su_jinchen", "苏瑾琛", "core"),
	}, []domain.OutlineEntry{{
		Chapter:      1,
		CharacterIDs: []string{"lin_shuran", "su_jinchen"},
	}})
	tool := NewCommitChapterTool(st)
	err := tool.validateChapterCommitCharacterIDs(1, []string{"lengqiuyan", "linyuan"})
	if err == nil || !strings.Contains(err.Error(), "check_de_ai.commit_context") {
		t.Fatalf("expected cross-project character IDs to fail with recovery context, got %v", err)
	}
	if err := tool.validateChapterCommitCharacterIDs(1, []string{"su_jinchen", "lin_shuran"}); err != nil {
		t.Fatalf("canonical character IDs should pass regardless of order: %v", err)
	}
}
