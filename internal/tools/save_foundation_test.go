package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSaveFoundationSchemaAllowsMissingType(t *testing.T) {
	tool := NewSaveFoundationTool(store.NewStore(testStoreDir(t)))
	required := schemaRequiredNames(tool.Schema())
	if required["type"] {
		t.Fatal("type must not be schema-required; missing type should reach Execute for recovery")
	}
	if !required["content"] {
		t.Fatal("content should remain schema-required")
	}
}

func TestDecodeWorldRulesAcceptsGroupedHardAndSoftObject(t *testing.T) {
	rules, err := decodeWorldRules(`{
		"hard_rules":[{"id":"identity","category":"identity","rule":"Confirmed identities never drift.","boundary":"No identity replacement."}],
		"soft_rules":[{"id":"tone","category":"tone","rule":"Prefer restrained narration.","boundary":"No melodramatic omniscience."}],
		"setting_summary":{"era":"modern"},
		"scale":"long"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 ||
		rules[0].Strength != domain.WorldRuleStrengthHard ||
		rules[1].Strength != domain.WorldRuleStrengthSoft {
		t.Fatalf("grouped rules = %+v", rules)
	}
}

func TestDecodeWorldRulesRejectsCustomWorldBibleObjectWithExactContract(t *testing.T) {
	_, err := decodeWorldRules(`{
		"title":"Custom world bible",
		"setting":{"era":"modern"},
		"reality_rules":["Consequences persist."],
		"narrative_rules":{"pov":"limited"}
	}`)
	if err == nil {
		t.Fatal("custom world-bible object was accepted")
	}
	for _, want := range []string{
		`"hard_rules":[WorldRule...]`,
		`"soft_rules":[WorldRule...]`,
		"id/category/title/rule/boundary/strength/priority/tags",
		"do not send custom setting/reality_rules",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestDecodeWorldRulesRejectsEmptyRequiredFields(t *testing.T) {
	for name, content := range map[string]string{
		"rule":     `[{"id":"hard-1","boundary":"No exceptions."}]`,
		"boundary": `[{"id":"hard-1","rule":"Consequences persist."}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeWorldRules(content)
			if err == nil || !errors.Is(err, errs.ErrToolArgs) {
				t.Fatalf("error = %v", err)
			}
			if !strings.Contains(err.Error(), "non-empty "+name) {
				t.Fatalf("error missing %s contract: %v", name, err)
			}
		})
	}
}

func TestDecodeFoundationGenerationWorldRulesRequiresCompleteGroups(t *testing.T) {
	valid := `{
		"hard_rules":[{"id":"hard-1","rule":"Consequences persist.","boundary":"No reset."}],
		"soft_rules":[{"id":"soft-1","rule":"Prefer restraint.","boundary":"Avoid melodrama."}]
	}`
	rules, err := decodeFoundationGenerationWorldRules(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 ||
		rules[0].Strength != domain.WorldRuleStrengthHard ||
		rules[1].Strength != domain.WorldRuleStrengthSoft {
		t.Fatalf("rules = %+v", rules)
	}

	for name, content := range map[string]string{
		"array":        `[{"id":"hard-1","rule":"Consequences persist.","boundary":"No reset."}]`,
		"missing_soft": `{"hard_rules":[{"id":"hard-1","rule":"Consequences persist.","boundary":"No reset."}]}`,
		"custom_field": `{"hard_rules":[{"id":"hard-1","rule":"Consequences persist.","boundary":"No reset."}],"soft_rules":[{"id":"soft-1","rule":"Prefer restraint.","boundary":"Avoid melodrama."}],"setting":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeFoundationGenerationWorldRules(content)
			if err == nil || !errors.Is(err, errs.ErrToolArgs) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeWorldRulesInfersLegacyArrayStrengthFromStableID(t *testing.T) {
	rules, err := decodeWorldRules(`[
		{"id":"hr_identity","rule":"Identity is stable.","boundary":"No identity replacement."},
		{"id":"sr_tone","rule":"Prefer restrained narration.","boundary":"No melodramatic omniscience."},
		{"id":"custom","rule":"Historical unknown IDs remain compatible.","boundary":"No silent reinterpretation."},
		{"id":"sr_explicit","rule":"Explicit values win.","boundary":"No implicit override.","strength":"hard"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if rules[0].Strength != domain.WorldRuleStrengthHard ||
		rules[1].Strength != domain.WorldRuleStrengthSoft ||
		rules[2].Strength != "" ||
		rules[3].Strength != domain.WorldRuleStrengthHard {
		t.Fatalf("array rules = %+v", rules)
	}
}

func TestDecodeLayeredOutlineAcceptsSingleAndGroupedObjects(t *testing.T) {
	for name, content := range map[string]string{
		"single":  `{"index":1,"title":"Opening","theme":"Pressure"}`,
		"grouped": `{"volumes":[{"index":2,"title":"Escalation","theme":"Cost"}]}`,
		"wrapped": `{"volume":{"index":3,"title":"Payoff","theme":"Choice"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			volumes, err := decodeLayeredOutline(content)
			if err != nil {
				t.Fatal(err)
			}
			if len(volumes) != 1 || volumes[0].Index == 0 || volumes[0].Title == "" {
				t.Fatalf("volumes = %+v", volumes)
			}
		})
	}
}

func TestSaveFoundationPersistsPlannedRelationshipsByStableIDOnly(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Characters.Save([]domain.Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}}); err != nil {
		t.Fatal(err)
	}
	runtime := []domain.RelationshipEntry{{CharacterA: "Lin", CharacterB: "Mara", Relation: "met", Chapter: 3}}
	if err := st.World.SaveRelationships(runtime); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"type": "planned_relationships",
		"content": []map[string]any{{
			"source_character_id": "lin",
			"target_character_id": "mara",
			"type":                "ally",
			"direction":           "mutual",
			"status":              "planned",
		}},
	})
	if _, err := NewSaveFoundationTool(st).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	foundation, err := st.Foundation.Load()
	if err != nil || len(foundation.Relationships) != 1 {
		t.Fatalf("planned relationships = %+v, %v", foundation.Relationships, err)
	}
	gotRuntime, err := st.World.LoadRelationships()
	if err != nil || len(gotRuntime) != 1 || gotRuntime[0].Chapter != 3 {
		t.Fatalf("runtime relationship state changed: %+v, %v", gotRuntime, err)
	}

	invalid, _ := json.Marshal(map[string]any{
		"type": "planned_relationships",
		"content": []map[string]any{{
			"source_character_id": "Lin",
			"target_character_id": "Mara",
			"type":                "ally",
		}},
	})
	if _, err := NewSaveFoundationTool(st).Execute(context.Background(), invalid); err == nil {
		t.Fatal("character names were accepted in place of stable IDs")
	}
}

func TestSaveFoundationBlocksDirectFormalOutlineWritesDuringActiveRevision(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	impact, err := domain.NewRevisionImpact("direct write gate", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: domain.StructureKindChapter, Change: "revise outline",
		DependencyEvidence: []string{"active revision owns the formal write"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(toolRevisionPolicy{}, store.StartRevisionInput{Intent: "gate", Impact: impact, IdempotencyKey: "tool-gate"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"outline", "layered_outline", "expand_arc", "repair_arc", "repair_volume", "append_volume", "update_compass", "complete_book"} {
		args, _ := json.Marshal(map[string]any{"type": kind, "content": map[string]any{}})
		if _, err := NewSaveFoundationTool(st).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
			t.Fatalf("direct formal write %s was not blocked: %v", kind, err)
		}
	}
}

func TestSaveFoundationRejectsStoryIdentityDriftFromCoCreateBrief(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("novel", 0); err != nil {
		t.Fatal(err)
	}
	brief := "## 主题\n- 书名：《重生后，我被太子爷宠上天》\n- 地点：A市\n\n## 人物设定\n- 女主 林舒然：20岁\n- 男主 墨子曜：28岁"
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusCollecting,
		Kind:   domain.PlanningReviewKindBlueprint,
		Brief:  brief,
	}); err != nil {
		t.Fatal(err)
	}
	tool := NewSaveFoundationTool(st)

	wrongPremise, _ := json.Marshal(map[string]any{
		"type":    "premise",
		"content": "# 《声线入骨》\n沈知夏在H市为影帝谢承晏配乐。",
	})
	if _, err := tool.Execute(context.Background(), wrongPremise); err == nil || !strings.Contains(err.Error(), "canonical co-create brief") {
		t.Fatalf("wrong premise should be rejected by the creative brief guard: %v", err)
	}
	if saved, _ := st.Outline.LoadPremise(); saved != "" {
		t.Fatalf("rejected premise was persisted: %q", saved)
	}

	correctPremise, _ := json.Marshal(map[string]any{
		"type":    "premise",
		"content": "# 《重生后，我被太子爷宠上天》\n林舒然在A市救下失忆的墨子曜，重生后守住两人的命运。",
	})
	if _, err := tool.Execute(context.Background(), correctPremise); err != nil {
		t.Fatalf("canonical premise should pass: %v", err)
	}

	wrongCharacters, _ := json.Marshal(map[string]any{
		"type": "characters",
		"content": []map[string]any{
			{"name": "沈知夏", "role": "女主"},
			{"name": "谢承晏", "role": "男主"},
		},
	})
	if _, err := tool.Execute(context.Background(), wrongCharacters); err == nil || !strings.Contains(err.Error(), "missing canonical protagonists") {
		t.Fatalf("wrong character identities should be rejected: %v", err)
	}
	if characters, _ := st.Characters.Load(); len(characters) != 0 {
		t.Fatalf("rejected characters were persisted: %+v", characters)
	}

	correctCharacters, _ := json.Marshal(map[string]any{
		"type": "characters",
		"content": []map[string]any{
			{"name": "林舒然", "role": "女主"},
			{"name": "墨子曜", "role": "男主"},
		},
	})
	if _, err := tool.Execute(context.Background(), correctCharacters); err != nil {
		t.Fatalf("canonical characters should pass: %v", err)
	}
}

type toolRevisionPolicy struct{}

func (toolRevisionPolicy) Mode() domain.RevisionMode                  { return domain.RevisionModeNormal }
func (toolRevisionPolicy) Identity() (string, string)                 { return "tools.test", "1" }
func (toolRevisionPolicy) ValidateImpact(domain.RevisionImpact) error { return nil }
func (toolRevisionPolicy) ApprovalStages(domain.RevisionImpact) ([]domain.RevisionApprovalStage, error) {
	return []domain.RevisionApprovalStage{{ID: "outline", Label: "outline"}}, nil
}
func (toolRevisionPolicy) ValidateCandidate(domain.RevisionSession, []domain.ArtifactVersion) error {
	return nil
}
func (toolRevisionPolicy) Route(domain.RevisionSession) (*domain.RevisionRoute, error) {
	return nil, nil
}

func TestSaveFoundationInfersPremiseFromMarkdownWhenTypeMissing(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("novel", 0); err != nil {
		t.Fatalf("Init progress: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"content": "# 夜审暗潮\n\n## 题材和基调\n悬疑、审讯、权力反转。",
		"scale":   "short",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(res, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["type"] != "premise" {
		t.Fatalf("type = %v, want premise", result["type"])
	}
	premise, err := store.Outline.LoadPremise()
	if err != nil {
		t.Fatalf("LoadPremise: %v", err)
	}
	if !strings.Contains(premise, "夜审暗潮") {
		t.Fatalf("premise was not saved: %q", premise)
	}
}

func TestSaveFoundationMissingTypeCompletesFoundationWhenOnlyPremiseMissing(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)
	if err := store.Progress.Init("novel", 0); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	tool := NewSaveFoundationTool(store)

	steps := []map[string]any{
		{
			"type": "outline",
			"content": []map[string]any{
				{"chapter": 1, "title": "初次对峙", "core_event": "侦探第一次审问嫌疑人", "hook": "嫌疑人反问她为何害怕真相"},
			},
			"scale": "short",
		},
		{
			"type": "characters",
			"content": []map[string]any{
				confirmedCoreCharacterToolFixture(),
				{"name": "林岚", "role": "私家侦探", "description": "冷静但有伤痕", "arc": "从控制到承认失控", "traits": []string{"敏锐"}},
			},
		},
		{
			"type": "world_rules",
			"content": []map[string]any{
				{"category": "society", "rule": "私人调查必须避开警方监听", "boundary": "不能凭空获得证据"},
			},
		},
	}
	for _, step := range steps {
		args, _ := json.Marshal(step)
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatalf("Execute setup %v: %v", step["type"], err)
		}
	}

	args, err := json.Marshal(map[string]any{
		"content": "# 夜审暗潮\n\n## 题材和基调\n短篇悬疑。",
	})
	if err != nil {
		t.Fatalf("Marshal final premise: %v", err)
	}
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute final premise: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(res, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["foundation_ready"] != true {
		t.Fatalf("foundation_ready = %v, want true", result["foundation_ready"])
	}
	if result["phase"] != string(domain.PhaseWriting) {
		t.Fatalf("phase = %v, want %s", result["phase"], domain.PhaseWriting)
	}
}

func TestSaveFoundationInfersTypeWhenFoundationAlreadyComplete(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)
	if err := store.Progress.Init("novel", 0); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	tool := NewSaveFoundationTool(store)

	for _, step := range []map[string]any{
		{"type": "premise", "content": "# 夜审暗潮\n\n## 题材和基调\n短篇悬疑。"},
		{"type": "outline", "content": []map[string]any{{"chapter": 1, "title": "初次对峙", "core_event": "审问", "hook": "反问"}}},
		{"type": "characters", "content": []map[string]any{confirmedCoreCharacterToolFixture(), {"name": "林岚", "role": "侦探", "description": "敏锐", "arc": "转变", "traits": []string{"冷静"}}}},
		{"type": "world_rules", "content": []map[string]any{{"category": "society", "rule": "证据必须可追溯", "boundary": "不能凭空破案"}}},
	} {
		if step["type"] == "outline" {
			approveFoundationToolFixture(t, store)
		}
		args, _ := json.Marshal(step)
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatalf("Execute setup %v: %v", step["type"], err)
		}
	}

	args, err := json.Marshal(map[string]any{
		"content": []map[string]any{
			confirmedCoreCharacterToolFixture(),
			{"name": "林岚", "role": "侦探", "description": "更新后的角色描述", "arc": "继续转变", "traits": []string{"冷静", "执着"}},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute inferred complete foundation update: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(res, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["type"] != "characters" {
		t.Fatalf("type = %v, want characters", result["type"])
	}
}

func schemaRequiredNames(s map[string]any) map[string]bool {
	result := map[string]bool{}
	switch required := s["required"].(type) {
	case []string:
		for _, name := range required {
			result[name] = true
		}
	case []any:
		for _, raw := range required {
			if name, ok := raw.(string); ok {
				result[name] = true
			}
		}
	}
	return result
}

func TestSaveFoundationPersistsPlanningTier(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type":    "premise",
		"content": "# 测试书名\n\n## 题材和基调\n测试",
		"scale":   "long",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("expected run meta to exist")
	}
	if meta.PlanningTier != domain.PlanningTierLong {
		t.Fatalf("expected planning tier %q, got %q", domain.PlanningTierLong, meta.PlanningTier)
	}
}

func TestSaveFoundationPremiseSetsNovelName(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("novel", 0); err != nil {
		t.Fatalf("Init progress: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type": "premise",
		"content": `# 长夜燃灯

## 题材和基调
东方玄幻，冷硬求生。`,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress == nil {
		t.Fatal("expected progress")
	}
	if progress.NovelName != "长夜燃灯" {
		t.Fatalf("expected novel name set, got %q", progress.NovelName)
	}
}

func TestSaveFoundationOutlineComputesWordBudget(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)
	if err := store.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	budget, _ := domain.NewWordBudgetFromTarget(100000, domain.WordBudgetSourcePrompt)
	if err := store.RunMeta.SetWordBudget(budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	markers := []string{
		"alpha", "bravo", "cipher", "delta", "ember",
		"fjord", "galaxy", "harbor", "ivory", "jigsaw",
		"kernel", "lantern", "matrix", "nebula", "onyx",
		"prairie", "quartz", "raven", "saffron", "tundra",
	}
	entries := make([]map[string]any, 0, len(markers))
	for chapter := 1; chapter <= 20; chapter++ {
		marker := markers[chapter-1]
		entries = append(entries, map[string]any{
			"chapter":    chapter,
			"title":      fmt.Sprintf("chapter %02d %s", chapter, marker),
			"core_event": strings.Repeat(marker, 10),
			"hook":       strings.Repeat(marker+"q", 8),
		})
	}
	args, err := json.Marshal(map[string]any{
		"type":    "outline",
		"content": entries,
		"scale":   "short",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil || meta.WordBudget == nil {
		t.Fatal("expected word budget")
	}
	if meta.WordBudget.PlannedChapters != 20 ||
		meta.WordBudget.ChapterMinWords != 4500 ||
		meta.WordBudget.ChapterMaxWords != 5500 {
		t.Fatalf("word budget = %+v", meta.WordBudget)
	}
}

func TestSaveFoundationOutlineRejectsRepeatedLongTitle(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type": "outline",
		"content": []map[string]any{
			{
				"chapter":    1,
				"title":      "Mirror Door Signal",
				"core_event": "The first chapter opens a distinct clue trail.",
				"hook":       "The signal points to a sealed door.",
			},
			{
				"chapter":    2,
				"title":      "Mirror Door Signal",
				"core_event": "The second chapter follows a different suspect path.",
				"hook":       "A witness withholds evidence.",
			},
		},
		"scale": "short",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected duplicate outline to be rejected")
	} else if !strings.Contains(err.Error(), "duplicate chapter outline") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestSaveFoundationOutlineRequiresReviewForBorderlineSimilarity(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)
	entries := []map[string]any{
		{
			"chapter":    1,
			"title":      "North Archive",
			"core_event": "Mira enters the archive after curfew, trades evidence with the librarian, and learns that the sealed ledger names the deputy.",
			"hook":       "The deputy's name is written in fresh ink.",
			"scenes":     []string{"Archive search", "Librarian exchange"},
		},
		{
			"chapter":    2,
			"title":      "South Archive",
			"core_event": "Mira enters the archive before dawn, trades evidence with the librarian, and learns that a sealed ledger names the warden.",
			"hook":       "A fresh name waits on the sealed page.",
			"scenes":     []string{"Archive search", "Librarian bargain"},
		},
	}
	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type":    "outline",
		"content": entries,
		"scale":   "short",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected borderline outline to require model review")
	} else if !strings.Contains(err.Error(), "similarity_review") {
		t.Fatalf("expected similarity_review guidance, got %v", err)
	}

	reviewedArgs, err := json.Marshal(map[string]any{
		"type":    "outline",
		"content": entries,
		"scale":   "short",
		"similarity_review": []map[string]any{{
			"chapter":          2,
			"existing_chapter": 1,
			"duplicate":        false,
			"reason":           "same setting but different outcome",
		}},
	})
	if err != nil {
		t.Fatalf("Marshal reviewed args: %v", err)
	}
	if _, err := tool.Execute(context.Background(), reviewedArgs); err != nil {
		t.Fatalf("reviewed borderline outline should save: %v", err)
	}
}

func TestSaveFoundationLayeredOutlineRejectsRepeatedLongTitle(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1,
			"title": "Volume",
			"theme": "Theme",
			"arcs": []map[string]any{{
				"index": 1,
				"title": "Arc",
				"goal":  "Goal",
				"chapters": []map[string]any{
					{
						"chapter":    1,
						"title":      "Mirror Door Signal",
						"core_event": "The first chapter opens a distinct clue trail.",
						"hook":       "The signal points to a sealed door.",
					},
					{
						"chapter":    2,
						"title":      "Mirror Door Signal",
						"core_event": "The second chapter follows a different suspect path.",
						"hook":       "A witness withholds evidence.",
					},
				},
			}},
		}},
		"scale": "long",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected duplicate layered outline to be rejected")
	} else if !strings.Contains(err.Error(), "duplicate chapter outline") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestSaveFoundationExpandArcRejectsRepeatedLongTitle(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)
	if err := store.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "Volume",
		Theme: "Theme",
		Arcs: []domain.ArcOutline{{
			Index:             1,
			Title:             "Arc",
			Goal:              "Goal",
			EstimatedChapters: 2,
		}},
	}}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type":   "expand_arc",
		"volume": 1,
		"arc":    1,
		"content": []map[string]any{
			{
				"title":      "Mirror Door Signal",
				"core_event": "The first chapter opens a distinct clue trail.",
				"hook":       "The signal points to a sealed door.",
			},
			{
				"title":      "Mirror Door Signal",
				"core_event": "The second chapter follows a different suspect path.",
				"hook":       "A witness withholds evidence.",
			},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected duplicate expand_arc outline to be rejected")
	} else if !strings.Contains(err.Error(), "duplicate chapter outline") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestSaveFoundationOutlineClearsLayeredStateWhenDowngrading(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)
	if err := store.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewSaveFoundationTool(store)

	layeredArgs, err := json.Marshal(map[string]any{
		"type":    "layered_outline",
		"content": `[{"index":1,"title":"第一卷","theme":"主题","arcs":[{"index":1,"title":"第一弧","goal":"目标","chapters":[{"chapter":1,"title":"第一章","core_event":"开局","hook":"继续"}]}]}]`,
		"scale":   "long",
	})
	if err != nil {
		t.Fatalf("Marshal layered args: %v", err)
	}
	if _, err := tool.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered outline: %v", err)
	}

	outlineArgs, err := json.Marshal(map[string]any{
		"type":    "outline",
		"content": `[{"chapter":1,"title":"第一章","core_event":"改为中篇","hook":"继续"}]`,
		"scale":   "mid",
	})
	if err != nil {
		t.Fatalf("Marshal outline args: %v", err)
	}
	if _, err := tool.Execute(context.Background(), outlineArgs); err != nil {
		t.Fatalf("Execute outline: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress == nil {
		t.Fatal("expected progress to exist")
	}
	if progress.Layered {
		t.Fatal("expected layered mode to be disabled")
	}
	if progress.CurrentVolume != 0 || progress.CurrentArc != 0 {
		t.Fatalf("expected volume/arc reset, got volume=%d arc=%d", progress.CurrentVolume, progress.CurrentArc)
	}

	volumes, err := store.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	if len(volumes) != 0 {
		t.Fatalf("expected layered outline cleared, got %d volumes", len(volumes))
	}

	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("expected run meta to exist")
	}
	if meta.PlanningTier != domain.PlanningTierMid {
		t.Fatalf("expected planning tier %q, got %q", domain.PlanningTierMid, meta.PlanningTier)
	}
}

func TestSaveFoundationOutlinePlansWordBudget(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)
	budget := domain.NewWordBudget(5000, "test")
	if err := store.RunMeta.SetWordBudget(budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type":    "outline",
		"content": `[{"chapter":1,"title":"一","core_event":"开端","hook":"钩子"},{"chapter":2,"title":"二","core_event":"推进","hook":"钩子"},{"chapter":3,"title":"三","core_event":"收束","hook":"钩子"}]`,
		"scale":   "short",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	meta, err := store.RunMeta.Load()
	if err != nil {
		t.Fatalf("LoadRunMeta: %v", err)
	}
	if meta == nil || meta.WordBudget == nil {
		t.Fatal("expected word budget")
	}
	if meta.WordBudget.PlannedChapters != 3 {
		t.Fatalf("planned chapters = %d, want 3", meta.WordBudget.PlannedChapters)
	}
	if meta.WordBudget.ChapterMinWords <= 0 || meta.WordBudget.ChapterMaxWords <= meta.WordBudget.ChapterMinWords {
		t.Fatalf("unexpected chapter range: %+v", meta.WordBudget)
	}
}

func TestSaveFoundationAppendVolume(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, s)
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewSaveFoundationTool(s)

	// 先创建初始 layered_outline（卷1）
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "第一卷", "theme": "起步",
			"arcs": []map[string]any{{
				"index": 1, "title": "首弧", "goal": "目标",
				"chapters": []map[string]any{{"title": "第一章", "core_event": "开局", "hook": "继续"}},
			}},
		}},
		"scale": "long",
	})
	if _, err := tool.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}

	// append_volume：追加卷2
	appendArgs, _ := json.Marshal(map[string]any{
		"type": "append_volume",
		"content": map[string]any{
			"index": 2, "title": "第二卷", "theme": "升级",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{{"title": "新章", "core_event": "推进", "hook": "钩子"}},
			}},
		},
	})
	res, err := tool.Execute(context.Background(), appendArgs)
	if err != nil {
		t.Fatalf("Execute append_volume: %v", err)
	}
	var result map[string]any
	json.Unmarshal(res, &result)
	if result["volume"] != float64(2) {
		t.Fatalf("expected volume=2, got %v", result["volume"])
	}

	// 验证大纲有 2 卷
	volumes, _ := s.Outline.LoadLayeredOutline()
	if len(volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(volumes))
	}
	if volumes[1].Title != "第二卷" {
		t.Fatalf("expected title '第二卷', got %q", volumes[1].Title)
	}
}

func TestSaveFoundationRepairArc(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, s)
	if err := s.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowWriting, Layered: true}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "第一卷",
		Theme: "起步",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "首弧",
			Goal:  "目标",
			Chapters: []domain.OutlineEntry{
				{Title: "旧一", CoreEvent: "旧事件一", Hook: "旧钩子一"},
				{Title: "旧二", CoreEvent: "旧事件二", Hook: "旧钩子二"},
			},
		}},
	}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	args, err := json.Marshal(map[string]any{
		"type":   "repair_arc",
		"volume": 1,
		"arc":    1,
		"content": []map[string]any{
			{"title": "新一", "core_event": "良逸改走侧门", "hook": "侧门留下青色符痕"},
			{"title": "新二", "core_event": "苏幼仪设局反制", "hook": "她把钥匙交给敌人"},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute repair_arc: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(res, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["type"] != "repair_arc" || result["chapters"] != float64(2) {
		t.Fatalf("unexpected result: %+v", result)
	}
	outline, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(outline) != 2 || outline[0].Chapter != 1 || outline[0].Title != "新一" || outline[1].Title != "新二" {
		t.Fatalf("outline not repaired: %+v", outline)
	}
}

func TestSaveFoundationRepairArcRange(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, s)
	if err := s.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowWriting, Layered: true}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume",
		Theme: "Theme",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "Arc",
			Goal:  "Goal",
			Chapters: []domain.OutlineEntry{
				{Title: "Harbor Cipher", CoreEvent: "A salt-stained ferry ledger exposes the smuggler's coded tide schedule.", Hook: "The tide bell rings from a locked boathouse."},
				{Title: "Old Two", CoreEvent: "The second clue is stale.", Hook: "The old second hook remains."},
				{Title: "Old Three", CoreEvent: "The third clue is stale.", Hook: "The old third hook remains."},
				{Title: "Glass Verdict", CoreEvent: "A glass report exposes who signed the false order.", Hook: "The signature belongs to a dead official."},
			},
		}},
	}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	args, err := json.Marshal(map[string]any{
		"type":         "repair_arc",
		"volume":       1,
		"arc":          1,
		"from_chapter": 2,
		"to_chapter":   3,
		"content": []map[string]any{
			{"title": "New Two", "core_event": "A brass witness redirects the second clue.", "hook": "The witness names a hidden staircase."},
			{"title": "New Three", "core_event": "A winter ledger redirects the third clue.", "hook": "The ledger opens to a missing page."},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute repair_arc range: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(res, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["from_chapter"] != float64(2) || result["to_chapter"] != float64(3) || result["chapters"] != float64(2) {
		t.Fatalf("unexpected range result: %+v", result)
	}
	outline, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	got := []string{outline[0].Title, outline[1].Title, outline[2].Title, outline[3].Title}
	if strings.Join(got, "|") != "Harbor Cipher|New Two|New Three|Glass Verdict" {
		t.Fatalf("outline range not repaired correctly: %+v", outline)
	}
}

func TestSaveFoundationAppendVolumeValidation(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewSaveFoundationTool(s)

	// 初始卷
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "第一卷", "theme": "起步",
			"arcs": []map[string]any{{
				"index": 1, "title": "首弧", "goal": "目标",
				"chapters": []map[string]any{{"title": "第一章", "core_event": "开局", "hook": "继续"}},
			}},
		}},
		"scale": "long",
	})
	tool.Execute(context.Background(), layeredArgs)

	// Index 不递增 → 应失败（结构性校验）
	appendArgs, _ := json.Marshal(map[string]any{
		"type": "append_volume",
		"content": map[string]any{
			"index": 1, "title": "重复 Index", "theme": "x",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{{"title": "章", "core_event": "事件", "hook": "钩子"}},
			}},
		},
	})
	_, err := tool.Execute(context.Background(), appendArgs)
	if err == nil {
		t.Fatal("expected error when appending volume with non-increasing index")
	}
}

// TestSaveFoundationAppendVolumeRejectsAfterComplete 验证 Phase=Complete 后不允许 append_volume。
// 取代旧的"Final 卷拒绝追加"语义（Final 字段已删除）。
func TestSaveFoundationAppendVolumeRejectsAfterComplete(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	appendArgs, _ := json.Marshal(map[string]any{
		"type": "append_volume",
		"content": map[string]any{
			"index": 1, "title": "尝试续写", "theme": "x",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧", "goal": "g",
				"chapters": []map[string]any{{"title": "章", "core_event": "e", "hook": "h"}},
			}},
		},
	})
	if _, err := tool.Execute(context.Background(), appendArgs); err == nil {
		t.Fatal("expected error when appending after Phase=Complete")
	}
}

func TestSaveFoundationUpdateCompass(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, s)

	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "update_compass",
		"content": map[string]any{
			"ending_direction": "主角面对最终抉择",
			"open_threads":     []string{"线索A", "关系B"},
			"estimated_scale":  "预计 4-6 卷",
		},
	})
	_, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute update_compass: %v", err)
	}

	compass, err := s.Outline.LoadCompass()
	if err != nil {
		t.Fatalf("LoadCompass: %v", err)
	}
	if compass == nil || compass.EndingDirection != "主角面对最终抉择" {
		t.Fatalf("unexpected compass: %+v", compass)
	}
	if len(compass.OpenThreads) != 2 {
		t.Fatalf("expected 2 open threads, got %d", len(compass.OpenThreads))
	}
}

func TestSaveFoundationUpdateCompassOverridesLastUpdated(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, s)
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "光斑",
		Phase:             domain.PhaseWriting,
		CompletedChapters: []int{1, 2, 3, 5, 4}, // 乱序，验证取 max 而非 len
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "update_compass",
		"content": map[string]any{
			"ending_direction": "主角面对最终抉择",
			"open_threads":     []string{"线索A"},
			"last_updated":     0, // LLM 通常忘填或留 0
		},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute update_compass: %v", err)
	}

	compass, err := s.Outline.LoadCompass()
	if err != nil {
		t.Fatalf("LoadCompass: %v", err)
	}
	if compass.LastUpdated != 5 {
		t.Fatalf("expected LastUpdated=5 (max of CompletedChapters), got %d", compass.LastUpdated)
	}
}

func TestSaveFoundationUpdateCompassRequiresDirection(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type":    "update_compass",
		"content": map[string]any{"estimated_scale": "3 卷"},
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error when ending_direction is empty")
	}
}

func TestSaveFoundationAcceptsDirectJSONArrayContent(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type": "outline",
		"content": []map[string]any{
			{
				"chapter":    1,
				"title":      "第一章",
				"core_event": "主角登场",
				"hook":       "继续",
				"scenes":     []string{"场景一", "场景二"},
			},
		},
		"scale": "short",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	outline, err := store.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(outline) != 1 || outline[0].Title != "第一章" {
		t.Fatalf("unexpected outline: %+v", outline)
	}
}

func TestSaveFoundationNormalizesNonPositiveOutlineChapters(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	approveFoundationToolFixture(t, store)

	tool := NewSaveFoundationTool(store)
	args, err := json.Marshal(map[string]any{
		"type": "outline",
		"content": []map[string]any{
			{
				"chapter":    0,
				"title":      "正文",
				"core_event": "短篇一气呵成",
				"hook":       "真相反转",
				"scenes":     []string{"开端", "反转", "收束"},
			},
		},
		"scale": "short",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	outline, err := store.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(outline) != 1 || outline[0].Chapter != 1 {
		t.Fatalf("outline chapter not normalized: %+v", outline)
	}
}

// completeBookSetup 建一份处于 writing 阶段的最小 Store，用于 complete_book 系列测试。
// complete_book 不校验 layered_outline 章节齐全（判定责任在 LLM 的"完结判定清单"），
// 工具层只校验 PendingRewrites 为空、progress 已初始化。
func completeBookSetup(t *testing.T) *store.Store {
	t.Helper()
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)
	return s
}

func TestSaveFoundationCompleteBookPushesPhaseComplete(t *testing.T) {
	s := completeBookSetup(t)
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{},
	})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute complete_book: %v", err)
	}
	var result map[string]any
	_ = json.Unmarshal(res, &result)
	if result["book_complete"] != true {
		t.Fatalf("expected book_complete=true, got %+v", result)
	}
	if result["phase"] != string(domain.PhaseComplete) {
		t.Fatalf("expected phase=complete, got %v", result["phase"])
	}
	progress, _ := s.Progress.Load()
	if progress.Phase != domain.PhaseComplete {
		t.Fatalf("expected progress.Phase=complete, got %s", progress.Phase)
	}
}

func TestSaveFoundationCompleteBookRejectsBeforeWriting(t *testing.T) {
	// 规划阶段误调 complete_book 必须被拒，否则会直接跳过整本写作。
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhasePremise)
	_ = s.Progress.UpdatePhase(domain.PhaseOutline)
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{},
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected error when phase != writing")
	}
	progress, _ := s.Progress.Load()
	if progress.Phase != domain.PhaseOutline {
		t.Fatalf("phase should remain outline, got %s", progress.Phase)
	}
}

func TestSaveFoundationCompleteBookRejectsWithPendingRewrites(t *testing.T) {
	s := completeBookSetup(t)
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "尾章节奏过快"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	tool := NewSaveFoundationTool(s)
	args, _ := json.Marshal(map[string]any{
		"type": "complete_book", "content": map[string]any{},
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected error when PendingRewrites non-empty")
	}
	progress, _ := s.Progress.Load()
	if progress.Phase == domain.PhaseComplete {
		t.Fatalf("phase should not be Complete with PendingRewrites: %s", progress.Phase)
	}
}

func TestSaveFoundationLongNormalPlanningUsesVolumeAndBatchedChapterReviews(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("long original", 0); err != nil {
		t.Fatalf("Init progress: %v", err)
	}
	budget := domain.NewWordBudget(100_000, domain.WordBudgetSourceAPI)
	if err := st.RunMeta.SetWordBudget(budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusCollecting,
		Kind:   domain.PlanningReviewKindBlueprint,
	}); err != nil {
		t.Fatalf("SetPlanningReview: %v", err)
	}
	if err := st.Outline.SavePremise("# Long original"); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := st.Characters.Save([]domain.Character{{Name: "Hero", Role: "lead", Description: "drives the central conflict"}}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "Actions have public consequences", Boundary: "No consequence-free reset"}}); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}
	collectApprovedFoundationBlueprintFixture(t, st)
	alreadySavedPremiseArgs, _ := json.Marshal(map[string]any{"type": "premise", "content": "# Replaced before skeleton"})
	if _, err := NewSaveFoundationTool(st).Execute(context.Background(), alreadySavedPremiseArgs); err == nil || !strings.Contains(err.Error(), "already persisted and locked") {
		t.Fatalf("expected existing premise to be locked during blueprint recovery, got %v", err)
	}

	volumes := make([]domain.VolumeOutline, 3)
	for vi := range volumes {
		volumes[vi] = domain.VolumeOutline{Index: vi + 1, Title: fmt.Sprintf("Volume %d", vi+1), Theme: fmt.Sprintf("Escalation %d", vi+1)}
		for ai := 1; ai <= 3; ai++ {
			volumes[vi].Arcs = append(volumes[vi].Arcs, domain.ArcOutline{
				Index: ai, Title: fmt.Sprintf("V%d arc %d", vi+1, ai), Goal: fmt.Sprintf("advance conflict through milestone %d", vi*3+ai), EstimatedChapters: 3,
			})
		}
	}
	tool := NewSaveFoundationTool(st)
	execute := func(payload map[string]any) map[string]any {
		t.Helper()
		args, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		raw, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("Execute(%v): %v", payload["type"], err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("Unmarshal result: %v", err)
		}
		return result
	}
	auditTool := NewSaveOriginalPlanningAuditTool(st)
	saveAudit := func(payload map[string]any) map[string]any {
		t.Helper()
		observed := []map[string]any{}
		if scope, _ := payload["scope"].(string); scope == "chapter" || scope == "arc" {
			from, _ := payload["from_chapter"].(int)
			to, _ := payload["to_chapter"].(int)
			for chapter := from; chapter <= to; chapter++ {
				entry, err := st.Outline.GetChapterOutline(chapter)
				if err != nil {
					t.Fatalf("load chapter %d scene evidence: %v", chapter, err)
				}
				observed = append(observed, map[string]any{
					"chapter": chapter,
					"count":   len(entry.Scenes),
				})
			}
		}
		payload["observed_scene_counts"] = observed
		args, _ := json.Marshal(payload)
		raw, err := auditTool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("save audit %v: %v", payload["scope"], err)
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	dimensions := func(names ...string) []map[string]any {
		out := make([]map[string]any, 0, len(names))
		for _, name := range names {
			out = append(out, map[string]any{"name": name, "score": 8, "comment": "evidence-backed pass"})
		}
		return out
	}
	execute(map[string]any{"type": "layered_outline", "scale": "long", "content": volumes[:1]})
	lockedPremiseArgs, _ := json.Marshal(map[string]any{"type": "premise", "content": "# Replaced by mistake"})
	if _, err := tool.Execute(context.Background(), lockedPremiseArgs); err == nil || !strings.Contains(err.Error(), "volume skeleton is already in batched generation") {
		t.Fatalf("expected persisted foundation replacement to be locked after first skeleton volume, got %v", err)
	}
	execute(map[string]any{"type": "append_volume", "content": volumes[1]})
	execute(map[string]any{"type": "append_volume", "content": volumes[2]})
	result := execute(map[string]any{"type": "update_compass", "content": domain.StoryCompass{EndingDirection: "The heroine wins without abandoning her chosen family"}})
	if result["continue_planning"] != true || result["planning_review"] == domain.PlanningReviewStatusPending {
		t.Fatalf("numeric skeleton coverage must enter automatic audit before user review: %+v", result)
	}
	for vi := 1; vi <= 3; vi++ {
		saveAudit(map[string]any{
			"scope": "skeleton_volume", "volume": vi, "verdict": "pass", "summary": "volume has causal progression and a paid phase result",
			"dimensions": dimensions("volume_function", "arc_causality", "character_progression", "conflict_escalation", "budget_capacity", "payoff_and_handoff"),
		})
	}
	saveAudit(map[string]any{
		"scope": "skeleton_book_batch", "from_volume": 1, "to_volume": 2, "verdict": "pass", "summary": "first two volumes escalate coherently",
		"dimensions": dimensions("cross_volume_continuity", "escalation", "character_progression", "setup_payoff", "pacing_balance", "plot_diversity"),
	})
	saveAudit(map[string]any{
		"scope": "skeleton_book_batch", "from_volume": 3, "to_volume": 3, "verdict": "pass", "summary": "final volume closes the promised endgame",
		"dimensions": dimensions("cross_volume_continuity", "escalation", "character_progression", "setup_payoff", "pacing_balance", "plot_diversity"),
	})
	result = saveAudit(map[string]any{
		"scope": "skeleton_book", "verdict": "pass", "summary": "whole skeleton carries and closes every promise",
		"dimensions": dimensions("mainline_completeness", "ending_closure", "character_arc_completeness", "setup_payoff", "volume_balance", "budget_capacity", "originality"),
	})
	if result["planning_review"] != domain.PlanningReviewStatusPending || result["planning_review_kind"] != domain.PlanningReviewKindVolumeSplit {
		t.Fatalf("automatic skeleton audit should open volume review: %+v", result)
	}

	review, _ := st.RunMeta.PlanningReview()
	review.Status = domain.PlanningReviewStatusCollecting
	if err := st.RunMeta.SetPlanningReview(review); err != nil {
		t.Fatalf("start detailed outline stage: %v", err)
	}
	chapter := 1
	beats := []string{
		"avalanche rescue exposes the hidden heir", "beacon sabotage strands the convoy", "cipher auction reveals the forged succession",
		"drought council breaks the merchant alliance", "eclipse ritual awakens a disputed oath", "ferry mutiny transfers the royal witness",
		"garden duel overturns the engagement pact", "harbor quarantine conceals a prison exchange", "icehouse fire destroys the blackmail ledger",
		"jewel theft forces a public confession", "kestrel message redirects the border army", "lighthouse siege reunites the separated sisters",
		"masquerade vote removes the corrupt regent", "nursery tunnel uncovers the missing child", "observatory trial disproves the ancient prophecy",
		"pilgrim strike closes the mountain road", "quarry collapse traps the rival commanders", "river tribunal restores the stolen estate",
		"salt riot divides the palace guard", "theater premiere identifies the masked assassin", "underground flood releases the sealed archive",
		"vineyard bargain converts an enemy captain", "winter coronation triggers the final rebellion", "xylophone signal opens the evacuation route",
		"yacht blockade exposes the treasury conspiracy", "zephyr code reunites the rebel councils", "archive verdict completes the succession struggle",
	}
	for vi := 1; vi <= 3; vi++ {
		for ai := 1; ai <= 3; ai++ {
			batchStart := chapter
			entries := make([]domain.OutlineEntry, 3)
			for i := range entries {
				beat := beats[chapter-1]
				entries[i] = domain.OutlineEntry{
					Chapter:   chapter,
					Title:     strings.Title(beat),
					CoreEvent: beat,
					Hook:      "unresolved " + beat,
					Scenes:    []string{beat},
				}
				chapter++
			}
			result = execute(map[string]any{
				"type": "expand_arc", "volume": vi, "arc": ai, "content": entries,
				"similarity_review": []map[string]any{
					{"chapter": batchStart + 1, "existing_chapter": batchStart, "duplicate": false, "reason": "distinct causal beat"},
					{"chapter": batchStart + 2, "existing_chapter": batchStart, "duplicate": false, "reason": "distinct causal beat"},
					{"chapter": batchStart + 2, "existing_chapter": batchStart + 1, "duplicate": false, "reason": "distinct causal beat"},
				},
			})
			if result["continue_planning"] != true || result["audit_required"] != true {
				t.Fatalf("batch V%d A%d should continue: %+v", vi, ai, result)
			}
			for currentChapter := batchStart; currentChapter < chapter; currentChapter++ {
				entry, loadErr := st.Outline.GetChapterOutline(currentChapter)
				if loadErr != nil {
					t.Fatalf("load chapter %d stable identity: %v", currentChapter, loadErr)
				}
				saveAudit(map[string]any{
					"scope": "chapter", "scope_id": entry.ID, "from_chapter": currentChapter, "to_chapter": currentChapter,
					"verdict": "pass", "summary": "chapter promise is independently sound",
					"dimensions": dimensions("causal_value", "character_logic", "continuity", "scene_progression", "hook_and_pacing", "originality"),
				})
			}
			saveAudit(map[string]any{
				"scope": "arc", "volume": vi, "arc": ai, "from_chapter": batchStart, "to_chapter": chapter - 1,
				"verdict": "pass", "summary": "arc advances causally", "dimensions": dimensions("causal_progression", "character_logic", "chapter_value", "continuity", "hook_and_pacing", "originality"),
			})
		}
		saveAudit(map[string]any{
			"scope": "volume", "volume": vi, "verdict": "pass", "summary": "volume carries its budget and climax",
			"dimensions": dimensions("structure_pacing", "theme_conflict", "climax_payoff", "character_arc", "budget_capacity", "next_volume_drive"),
		})
	}
	saveAudit(map[string]any{
		"scope": "book_batch", "from_volume": 1, "to_volume": 2, "verdict": "pass", "summary": "volumes one and two escalate coherently",
		"dimensions": dimensions("cross_volume_continuity", "escalation", "character_progression", "setup_payoff", "pacing_balance", "originality"),
	})
	saveAudit(map[string]any{
		"scope": "book_batch", "from_volume": 3, "to_volume": 3, "verdict": "pass", "summary": "final volume completes the escalation",
		"dimensions": dimensions("cross_volume_continuity", "escalation", "character_progression", "setup_payoff", "pacing_balance", "originality"),
	})
	result = saveAudit(map[string]any{
		"scope": "book", "verdict": "pass", "summary": "whole book closes every major promise",
		"dimensions": dimensions("mainline_closure", "character_closure", "setup_payoff", "escalation_pacing", "world_consistency", "originality", "ending_delivery"),
	})
	if result["planning_review"] != domain.PlanningReviewStatusPending || result["planning_review_kind"] != domain.PlanningReviewKindChapterOutline {
		t.Fatalf("chapter review transition = %+v", result)
	}
	flat, err := st.Outline.LoadOutline()
	if err != nil || len(flat) != 27 {
		t.Fatalf("detailed outline count=%d err=%v", len(flat), err)
	}
}

func TestSaveFoundationRepairVolumeKeepsBudgetAndInvalidatesSkeletonAudit(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	collectApprovedFoundationBlueprintFixture(t, st)
	if err := st.Progress.Init("repair skeleton", 18); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{
		{Index: 1, Title: "Opening", Theme: "survive", Arcs: []domain.ArcOutline{{Index: 1, Title: "A", Goal: "escape", EstimatedChapters: 3}, {Index: 2, Title: "B", Goal: "counter", EstimatedChapters: 3}}},
		{Index: 2, Title: "False ending", Theme: "open another mystery", Arcs: []domain.ArcOutline{{Index: 1, Title: "C", Goal: "find a clue", EstimatedChapters: 3}, {Index: 2, Title: "D", Goal: "start another hunt", EstimatedChapters: 3}}},
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.Save(domain.OriginalPlanningAudit{Scope: "skeleton_volume", Volume: 1, Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.Save(domain.OriginalPlanningAudit{Scope: "skeleton_volume", Volume: 2, Verdict: "revise", Issues: []domain.OriginalPlanningAuditIssue{{Volume: 2, Arc: 1}}}); err != nil {
		t.Fatal(err)
	}
	repaired := domain.VolumeOutline{Index: 2, Title: "Final reckoning", Theme: "close every promise", Arcs: []domain.ArcOutline{
		{Index: 1, Title: "Truth", Goal: "prove and pay off the mystery", EstimatedChapters: 3},
		{Index: 2, Title: "Choice", Goal: "defeat the antagonist and deliver the ending", EstimatedChapters: 3},
	}}
	args, _ := json.Marshal(map[string]any{"type": "repair_volume", "volume": 2, "content": repaired})
	if _, err := NewSaveFoundationTool(st).Execute(context.Background(), args); err != nil {
		t.Fatalf("repair volume: %v", err)
	}
	got, _ := st.Outline.LoadLayeredOutline()
	if got[0].Title != "Opening" || got[1].Title != "Final reckoning" || domain.TotalChapters(got) != 12 {
		t.Fatalf("repaired volumes = %+v", got)
	}
	adjacent, _ := st.OriginalPlanningAudits.Get("skeleton_volume", 1, 0)
	invalidated, _ := st.OriginalPlanningAudits.Get("skeleton_volume", 2, 0)
	if adjacent != nil || invalidated != nil {
		t.Fatalf("adjacent audit=%+v repaired audit=%+v", adjacent, invalidated)
	}
}
