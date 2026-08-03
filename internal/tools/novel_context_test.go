package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestContextToolInjectsStyleStats(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	progress := &domain.Progress{TotalChapters: 10}
	body := "# 第N章\n他不是迟疑，而是恐惧。沉默了几息。像一道光。\n夜色落下。\n他走了。"
	for ch := 1; ch <= 6; ch++ {
		if err := st.Drafts.SaveFinalChapter(ch, body); err != nil {
			t.Fatalf("SaveFinalChapter: %v", err)
		}
		progress.CompletedChapters = append(progress.CompletedChapters, ch)
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	tool := NewContextTool(st, References{}, "default")
	args, _ := json.Marshal(map[string]any{"chapter": 7})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Episodic map[string]json.RawMessage `json:"episodic_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	statsRaw, ok := payload.Episodic["style_stats"]
	if !ok {
		t.Fatalf("expected episodic_memory.style_stats, got keys %v", keysOf(payload.Episodic))
	}
	var stats struct {
		Chapters int `json:"chapters"`
		Patterns []struct {
			Name  string `json:"name"`
			Total int    `json:"total"`
		} `json:"patterns"`
	}
	if err := json.Unmarshal(statsRaw, &stats); err != nil {
		t.Fatalf("Unmarshal stats: %v", err)
	}
	if stats.Chapters != 6 || len(stats.Patterns) == 0 {
		t.Errorf("stats content: %+v", stats)
	}
	if usage, ok := payload.Episodic["_usage"]; !ok || len(usage) == 0 {
		t.Error("expected episodic_memory._usage annotation")
	}
}

func TestContextToolInjectsAntiAIToneForEveryWritingChapter(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	tool := NewContextTool(st, References{AntiAITone: "去AI化：不要用破折号解释；不要章内小标题。"}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":12}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		ReferencePack struct {
			References map[string]string `json:"references"`
		} `json:"reference_pack"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := payload.ReferencePack.References["anti_ai_tone"]; !strings.Contains(got, "章内小标题") {
		t.Fatalf("anti_ai_tone missing from chapter context: %+v", payload.ReferencePack.References)
	}
}

func TestContextToolInjectsCanonicalCoCreateBriefForPlanning(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	brief := "## 主题\n- 书名：《重生后，我被太子爷宠上天》\n- 地点：A市\n\n## 人物设定\n- 女主 林舒然：20岁\n- 男主 墨子曜：28岁\n- 关系关键词：救命之恩、出租屋同居、失忆依赖"
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusCollecting,
		Kind:   domain.PlanningReviewKindBlueprint,
		Brief:  brief,
	}); err != nil {
		t.Fatalf("SetPlanningReview: %v", err)
	}

	raw, err := NewContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(`{"scope":"planning"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	planning, ok := result["planning_memory"].(map[string]any)
	if !ok {
		t.Fatalf("planning_memory missing: %+v", result)
	}
	contract, ok := planning["creative_brief"].(map[string]any)
	if !ok {
		t.Fatalf("creative_brief missing: %+v", planning)
	}
	if contract["authority"] != "canonical_user_confirmed" || contract["content"] != brief {
		t.Fatalf("unexpected creative brief contract: %+v", contract)
	}
	locks, ok := contract["identity_locks"].(map[string]any)
	if !ok || locks["novel_name"] != "重生后，我被太子爷宠上天" || locks["primary_setting"] != "A市" {
		t.Fatalf("unexpected identity locks: %+v", contract["identity_locks"])
	}
	protagonists, ok := locks["protagonists"].(map[string]any)
	if !ok || protagonists["女主"] != "林舒然" || protagonists["男主"] != "墨子曜" {
		t.Fatalf("unexpected protagonist locks: %+v", locks["protagonists"])
	}
}

func TestContextToolInjectsWordBudget(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	budget, _ := domain.NewWordBudgetFromTarget(10000, domain.WordBudgetSourcePrompt)
	planned := budget.WithPlannedChapters(2)
	if err := st.RunMeta.SetWordBudget(&planned); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	tool := NewContextTool(st, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		t.Fatalf("working_memory missing: %+v", result)
	}
	wordBudget, ok := working["word_budget"].(map[string]any)
	if !ok {
		t.Fatalf("word_budget missing: %+v", working)
	}
	target := wordBudget["target"].(map[string]any)
	current := wordBudget["current_chapter"].(map[string]any)
	if got := int(target["target_total_words"].(float64)); got != 10000 {
		t.Fatalf("target_total_words = %d, want 10000", got)
	}
	if got := int(current["recommended_min_words"].(float64)); got != 4500 {
		t.Fatalf("recommended_min_words = %d, want 4500", got)
	}
	if got := int(current["recommended_max_words"].(float64)); got != 5500 {
		t.Fatalf("recommended_max_words = %d, want 5500", got)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestContextToolReportsWarningsForCorruptedState(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "outline.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write outline.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "progress.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write progress.json: %v", err)
	}

	tool := NewContextTool(store, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Warnings []string `json:"_warnings"`
		Summary  string   `json:"_loading_summary"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Warnings) == 0 {
		t.Fatal("expected context warnings for corrupted files")
	}
	if !containsWarning(payload.Warnings, "outline") {
		t.Fatalf("expected outline warning, got %v", payload.Warnings)
	}
	if !containsWarning(payload.Warnings, "progress") {
		t.Fatalf("expected progress warning, got %v", payload.Warnings)
	}
	if !strings.Contains(payload.Summary, "告警:") {
		t.Fatalf("expected loading summary to contain warning count, got %q", payload.Summary)
	}
}

func containsWarning(warnings []string, key string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, key) {
			return true
		}
	}
	return false
}

func TestContextToolReadsRevisedFormalChapterOutline(t *testing.T) {
	dir := testStoreDir(t)
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "Harbor Ledger", CoreEvent: "A ferry ledger exposes a tide schedule.", Hook: "A locked bell rings.", Scenes: []string{"Inspect the ferry", "Decode the ledger"}},
		{Chapter: 2, Title: "Old Observatory", CoreEvent: "The cast follows an obsolete signal.", Hook: "The lens goes dark.", Scenes: []string{"Climb the dome", "Test the old lens"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, CurrentChapter: 2, TotalChapters: 2}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	revised := domain.OutlineEntry{
		Chapter:   2,
		Title:     "Revised Observatory",
		CoreEvent: "A repaired telescope proves the signal was forged before sunrise.",
		Hook:      "The lens reveals a second moon.",
		Scenes:    []string{"Repair the telescope", "Expose the forged signal"},
	}
	if err := st.ReviseChapterOutline(2, revised); err != nil {
		t.Fatalf("ReviseChapterOutline: %v", err)
	}

	tool := NewContextTool(st, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":2}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := string(raw)
	for _, expected := range []string{"Revised Observatory", "forged before sunrise", "Expose the forged signal"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("revised writer context missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "Old Observatory") || strings.Contains(text, "obsolete signal") {
		t.Fatalf("writer context still contains stale formal outline: %s", text)
	}
}

func TestContextToolChapterModeIncludesWorkingAndReferenceFields(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(`## 题材和基调
少年成长，偏紧张压迫。

## 题材定位
少年升级流

## 核心冲突
主角必须在宗门竞争中活下来。

## 主角目标
进入内门。

## 终局方向
成为真正的执棋者。

## 写作禁区
不提前揭露师尊真相。

## 差异化卖点
弱者逆袭。

## 差异化钩子
每阶段都要用更高代价换成长。

## 核心兑现承诺
持续兑现危机与突破。

## 故事引擎
试炼、资源争夺与身份升级共同推进。

## 中段转折
主角被迫转向另一条修行路线。
`); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "入门", CoreEvent: "主角进入宗门", Scenes: []string{"拜师", "立誓"}},
		{Chapter: 2, Title: "试炼", CoreEvent: "参加外门试炼", Scenes: []string{"集合", "出发"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{
		{Name: "林砚", Role: "主角", Description: "少年修士", Arc: "成长", Traits: []string{"冷静"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{
		{Category: "magic", Rule: "灵气可以炼化", Boundary: "凡人不可直接驾驭"},
	}); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}
	if err := s.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    1,
		Summary:    "主角拜入宗门，确立目标。",
		Characters: []string{"林砚"},
		KeyEvents:  []string{"拜师"},
	}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(1, "第一章正文结尾，留下试炼悬念。"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 2,
		Title:   "试炼",
		Goal:    "通过第一关",
		Contract: domain.ChapterContract{
			RequiredBeats:    []string{"必须让主角通过第一关", "必须埋下内门试炼邀请"},
			ForbiddenMoves:   []string{"不能提前揭露师尊真实身份"},
			ContinuityChecks: []string{"主角左臂旧伤仍未痊愈"},
			EvaluationFocus:  []string{"重点检查试炼节奏是否拖沓"},
		},
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{
		Volume: 1,
		Arc:    1,
		Prose:  []string{"叙述保持克制"},
	}); err != nil {
		t.Fatalf("SaveStyleRules: %v", err)
	}
	if err := s.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}

	tool := NewContextTool(s, References{
		Consistency:      "一致性检查",
		HookTechniques:   "钩子技巧",
		QualityChecklist: "质量清单",
	}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{
		"memory_policy",
		"working_memory",
		"episodic_memory",
		"reference_pack",
		"context_profile",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in chapter context", key)
		}
	}
	working, ok := payload["working_memory"].(map[string]any)
	if !ok {
		t.Fatal("expected working_memory object")
	}
	for _, key := range []string{"recent_summaries", "chapter_plan", "previous_tail", "current_chapter_outline", "chapter_contract"} {
		if _, ok := working[key]; !ok {
			t.Fatalf("expected working_memory.%s in chapter context", key)
		}
	}
	if _, duplicated := payload["references"]; duplicated {
		t.Fatal("chapter context must not duplicate canonical reference_pack at top level")
	}
}

func TestContextToolExposesOnlyDraftRecommendationBeforeWriting(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Init("test", 55); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	budget := domain.NewWordBudget(200000, "test").WithPlannedChapters(55)
	if err := st.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	saveChapterWordRange(t, st, 3000, 6000)

	raw, err := NewContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	working := payload["working_memory"].(map[string]any)
	wordBudget := working["word_budget"].(map[string]any)
	current := wordBudget["current_chapter"].(map[string]any)
	if current["recommended_min_words"] != float64(3273) || current["recommended_max_words"] != float64(4000) {
		t.Fatalf("unexpected context ranges: %+v", current)
	}
	if _, exists := current["hard_min_words"]; exists {
		t.Fatalf("pre-draft context leaked hard_min_words: %+v", current)
	}
	if _, exists := current["hard_max_words"]; exists {
		t.Fatalf("pre-draft context leaked hard_max_words: %+v", current)
	}
}

func TestContextToolInjectsWordBudgetForArchitectAndWriter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("budget", 5); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, 800, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	budget := domain.NewWordBudget(5000, "test").WithPlannedChapters(5)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	for name, chapter := range map[string]int{"architect": 0, "writer": 2} {
		args, err := json.Marshal(map[string]any{"chapter": chapter})
		if err != nil {
			t.Fatalf("[%s] Marshal: %v", name, err)
		}
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("[%s] Execute: %v", name, err)
		}
		var payload struct {
			Working map[string]json.RawMessage `json:"working_memory"`
		}
		if err := json.Unmarshal(result, &payload); err != nil {
			t.Fatalf("[%s] Unmarshal: %v", name, err)
		}
		raw, ok := payload.Working["word_budget"]
		if !ok {
			t.Fatalf("[%s] expected working_memory.word_budget", name)
		}
		var got struct {
			Target struct {
				TargetTotalWords int `json:"target_total_words"`
				PlannedChapters  int `json:"planned_chapters"`
			} `json:"target"`
			CurrentChapter *struct {
				Chapter             int `json:"chapter"`
				RecommendedMinWords int `json:"recommended_min_words"`
				RecommendedMaxWords int `json:"recommended_max_words"`
			} `json:"current_chapter"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("[%s] Unmarshal word budget: %v", name, err)
		}
		if got.Target.TargetTotalWords != 5000 || got.Target.PlannedChapters != 5 {
			t.Fatalf("[%s] unexpected word budget: %+v", name, got)
		}
		if chapter > 0 && (got.CurrentChapter == nil || got.CurrentChapter.Chapter != chapter || got.CurrentChapter.RecommendedMinWords <= 0 || got.CurrentChapter.RecommendedMaxWords <= got.CurrentChapter.RecommendedMinWords) {
			t.Fatalf("[%s] unexpected current chapter budget: %+v", name, got.CurrentChapter)
		}
	}
}

func TestContextToolArchitectModeIncludesPlanningAndFoundation(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(`## 题材和基调
群像冒险，偏冷峻史诗。

## 题材定位
群像长篇冒险

## 核心冲突
众人必须在不断失控的旧秩序中寻找新秩序。

## 主角目标
抵达真相核心。

## 终局方向
揭开古老真相并重建秩序。

## 写作禁区
不靠天降设定收尾。

## 差异化卖点
群像关系推进。

## 差异化钩子
每卷都改变队伍关系结构。

## 核心兑现承诺
持续提供发现、牺牲与选择。

## 故事引擎
旅途推进、真相调查与队伍关系共同驱动。

## 关系/成长主线
队伍从互不信任走向分裂再重组。

## 升级路径
从地方事件走向世界级危机。

## 中期转向
真相并非敌人，而是秩序本身有问题。

## 终局命题
秩序应由谁定义。
`); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "起点", CoreEvent: "旅途开始"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{
		{Name: "沈曜", Role: "主角", Description: "流浪剑客", Arc: "寻找真相", Traits: []string{"敏锐"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{
		{Category: "society", Rule: "城邦林立", Boundary: "皇权不可直辖边地"},
	}); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{
		{
			Index: 1, Title: "第一卷", Theme: "踏上旅途",
			Arcs: []domain.ArcOutline{
				{Index: 1, Title: "启程", Goal: "建立队伍", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "起点"}}},
				{Index: 2, Title: "迷雾", Goal: "逼近秘密", EstimatedChapters: 5},
			},
		},
	}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: "揭开古老真相",
		EstimatedScale:  "预计 3 卷",
	}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{
		Volume: 1,
		Arc:    1,
		Prose:  []string{"保持冷峻节制"},
	}); err != nil {
		t.Fatalf("SaveStyleRules: %v", err)
	}
	if err := s.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}

	tool := NewContextTool(s, References{
		OutlineTemplate:   "大纲模板",
		CharacterTemplate: "角色模板",
		LongformPlanning:  "长篇规划",
	}, "default")
	args, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{
		"memory_policy",
		"planning_memory",
		"foundation_memory",
		"reference_pack",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in architect context", key)
		}
	}
	for _, duplicate := range []string{"planning_tier", "premise_sections", "characters", "layered_outline", "compass", "style_rules", "references"} {
		if _, ok := payload[duplicate]; ok {
			t.Fatalf("architect context must not mirror canonical field %q at top level", duplicate)
		}
	}
	planning := payload["planning_memory"].(map[string]any)
	foundation := payload["foundation_memory"].(map[string]any)
	references := payload["reference_pack"].(map[string]any)
	for key, section := range map[string]map[string]any{
		"planning_tier":     planning,
		"layered_outline":   planning,
		"compass":           planning,
		"premise_sections":  foundation,
		"characters":        foundation,
		"foundation_status": foundation,
		"style_rules":       references,
		"references":        references,
	} {
		if _, ok := section[key]; !ok {
			t.Fatalf("expected canonical architect field %q", key)
		}
	}
}

func TestContextToolMaturePlanningContextIsSourceBounded(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("mature", 96); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	progress, _ := s.Progress.Load()
	progress.Layered = true
	progress.CurrentChapter = 39
	progress.InProgressChapter = 39
	progress.CurrentVolume = 4
	progress.CurrentArc = 2
	if err := s.Progress.Save(progress); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	volumes := make([]domain.VolumeOutline, 0, 8)
	for volume := 1; volume <= 8; volume++ {
		arcs := make([]domain.ArcOutline, 0, 4)
		for arc := 1; arc <= 4; arc++ {
			arcs = append(arcs, domain.ArcOutline{
				Index: arc, Title: strings.Repeat("arc title ", 20), Goal: strings.Repeat("arc goal ", 80), EstimatedChapters: 3,
			})
		}
		volumes = append(volumes, domain.VolumeOutline{Index: volume, Title: strings.Repeat("volume ", 20), Theme: strings.Repeat("theme ", 80), Arcs: arcs})
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Outline.SavePremise(strings.Repeat("premise section ", 800)); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.World.SaveWorldRules(testWorldRules(120)); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}
	refs := strings.Repeat("reference guidance ", 2000)
	raw, err := NewContextTool(s, References{
		OutlineTemplate: refs, CharacterTemplate: refs, LongformPlanning: refs,
		Differentiation: refs, StyleReference: refs, ArcTemplates: refs,
	}, "default").Execute(context.Background(), json.RawMessage(`{"scope":"planning"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > planningContextSourceBytes {
		t.Fatalf("mature planning context = %d bytes, want <= %d", len(raw), planningContextSourceBytes)
	}
	if strings.Contains(string(raw), `"_trimmed"`) {
		t.Fatal("planning context must be source-bounded without a post-build truncation marker")
	}
}

func TestContextToolRichFoundationPlanningContextFitsModelRequestHeadroom(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	characters := make([]domain.Character, 0, 4)
	for idx := 0; idx < 4; idx++ {
		characters = append(characters, domain.Character{
			ID:          fmt.Sprintf("character-%d", idx),
			Name:        fmt.Sprintf("角色%d", idx),
			Role:        strings.Repeat("承担主线职责", 30),
			Description: strings.Repeat("完整人物描述与关键设定", 100),
			Arc:         strings.Repeat("人物弧线逐步变化", 80),
			Traits:      []string{strings.Repeat("关键特质", 30), strings.Repeat("反差特质", 30)},
			Goal:        strings.Repeat("长期目标", 80),
			Motivation:  strings.Repeat("核心动机", 80),
			Conflict:    strings.Repeat("内外冲突", 80),
			Voice:       strings.Repeat("语言风格", 80),
			Constraints: []string{strings.Repeat("不可违反约束", 80), strings.Repeat("持续行为约束", 80)},
			ContrastDetails: []domain.CharacterContrastDetail{{
				Surface: strings.Repeat("表面表现", 80),
				Depth:   strings.Repeat("深层事实", 80),
			}},
			KeyBackstory: []domain.CharacterBackstory{{
				Event:  strings.Repeat("关键往事", 80),
				Impact: strings.Repeat("当下影响", 80),
			}},
			InitialState: &domain.CharacterInitialState{
				Identity:      strings.Repeat("初始身份", 80),
				Situation:     strings.Repeat("初始处境", 80),
				Emotion:       strings.Repeat("初始情绪", 80),
				Resources:     []string{strings.Repeat("初始资源", 80)},
				Relationships: strings.Repeat("初始关系", 80),
			},
			KnowledgeBoundary: &domain.CharacterKnowledgeBoundary{
				Known:          []string{strings.Repeat("已知事实", 80)},
				Unknown:        []string{strings.Repeat("未知事实", 80)},
				Misconceptions: []string{strings.Repeat("错误认知", 80)},
			},
			Notes: strings.Repeat("额外备注", 100),
		})
	}
	relationships := make([]domain.CharacterRelationship, 0, 6)
	pairs := [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 0}, {0, 2}, {1, 3}}
	for idx := 0; idx < 6; idx++ {
		relationships = append(relationships, domain.CharacterRelationship{
			ID:                fmt.Sprintf("relationship-%d", idx),
			SourceCharacterID: characters[pairs[idx][0]].ID,
			TargetCharacterID: characters[pairs[idx][1]].ID,
			Type:              domain.RelationshipTypeOther,
			Direction:         domain.RelationshipDirectionDirected,
			Status:            domain.RelationshipStatusPlanned,
			Label:             strings.Repeat("关系标签", 50),
			Description:       strings.Repeat("关系说明", 100),
			Constraints:       []string{strings.Repeat("关系约束", 80)},
		})
	}
	worldRules := make([]domain.WorldRule, 0, 25)
	for idx := 0; idx < 25; idx++ {
		worldRules = append(worldRules, domain.WorldRule{
			ID:       fmt.Sprintf("hr_rule_%d", idx),
			Category: "structure",
			Rule:     strings.Repeat("世界规则正文", 80),
			Boundary: strings.Repeat("规则边界", 60),
			Strength: domain.WorldRuleStrengthHard,
		})
	}
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:               strings.Repeat("完整长篇故事前提", 500),
		Characters:            characters,
		Relationships:         relationships,
		RelationshipsReviewed: true,
		WorldRules:            worldRules,
	}, 0); err != nil {
		t.Fatal(err)
	}

	raw, err := NewContextTool(st, References{}, "default").Execute(
		context.Background(),
		json.RawMessage(`{"scope":"planning"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > planningContextSourceBytes {
		t.Fatalf("rich Foundation planning context = %d bytes, want <= %d", len(raw), planningContextSourceBytes)
	}
	text := string(raw)
	for _, required := range []string{
		`"characters"`, `"planned_relationships"`, `"world_rules"`,
		`"hard_world_rule_constraints"`, `"hr_rule_24"`, `"character-3"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("rich Foundation planning context is missing %q", required)
		}
	}
}

func TestContextToolFiveMillionWordPlanningUsesCompleteHierarchicalVolumeIndex(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := make([]domain.VolumeOutline, 0, 160)
	for volume := 1; volume <= 160; volume++ {
		arcs := make([]domain.ArcOutline, 0, 3)
		for arc := 1; arc <= 3; arc++ {
			arcs = append(arcs, domain.ArcOutline{
				Index: arc, Title: fmt.Sprintf("第%d卷第%d弧", volume, arc),
				Goal:              strings.Repeat("完整因果推进、角色阶段变化与伏笔传递。", 30),
				EstimatedChapters: 4,
			})
		}
		volumes = append(volumes, domain.VolumeOutline{
			Index: volume, Title: fmt.Sprintf("第%03d卷", volume),
			Theme: strings.Repeat("本卷主题冲突与不可逆结果。", 20), Arcs: arcs,
		})
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("five-million", 1920); err != nil {
		t.Fatal(err)
	}
	progress, _ := st.Progress.Load()
	progress.Layered = true
	progress.CurrentVolume = 160
	progress.CurrentArc = 3
	progress.CurrentChapter = 1917
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: strings.Repeat("终局承诺与所有主线闭环。", 40),
		OpenThreads:     []string{"主线承诺", "角色成长", "核心伏笔"},
		EstimatedScale:  "500万字 / 160卷",
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := NewContextTool(st, References{}, "default").Execute(
		context.Background(),
		json.RawMessage(`{"scope":"planning"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > planningContextSourceBytes {
		t.Fatalf("five-million planning context = %d bytes, want <= %d", len(raw), planningContextSourceBytes)
	}
	var payload struct {
		Planning struct {
			Layered []map[string]any `json:"layered_outline"`
			History [][]any          `json:"volume_history_index"`
		} `json:"planning_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Planning.History) != len(volumes) {
		t.Fatalf("hierarchical index covered %d/%d volumes", len(payload.Planning.History), len(volumes))
	}
	if payload.Planning.History[1][0].(float64) != 2 {
		t.Fatalf("middle historical volume must be indexed, got %+v", payload.Planning.History[1])
	}
	last := payload.Planning.Layered[len(payload.Planning.Layered)-1]
	if _, ok := last["arcs"]; !ok || last["index"].(float64) != 160 {
		t.Fatalf("current volume lost full arc detail: %+v", last)
	}
}

func TestContextToolPlanningDetailKeepsCanonicalFactsAndScalesToFiveMillionWords(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	characters := make([]domain.Character, 0, 4)
	for index := 1; index <= 4; index++ {
		characters = append(characters, domain.Character{
			ID: fmt.Sprintf("detail-character-%d", index), Name: fmt.Sprintf("核心角色%d", index),
			Role: strings.Repeat("主线职责", 18), Description: strings.Repeat("人物身份与不可替换的行为逻辑。", 25),
			Arc: strings.Repeat("阶段选择、代价与成长结果。", 22), Traits: []string{"表面冷静", "压力下坚持证据"},
			Tier: "core", Goal: strings.Repeat("长期目标与本卷目标。", 18),
			Motivation: strings.Repeat("核心动机及其来源。", 18), Conflict: strings.Repeat("内外冲突与不可回避的代价。", 18),
			Voice: "克制、明确、不会使用另一角色的口头禅", Constraints: []string{"不得无理由改变阵营", "不得提前知道尚未揭示的信息"},
			InitialState: &domain.CharacterInitialState{
				Identity: "确认身份", Situation: "故事开始时的稳定处境", Emotion: "警惕",
				Resources: []string{"合法资源", "秘密证据"}, Relationships: "依照已确认关系契约",
			},
			KnowledgeBoundary: &domain.CharacterKnowledgeBoundary{
				Known: []string{"已知事实"}, Unknown: []string{"未知真相"},
				Misconceptions: []string{"阶段误判"}, Forbidden: []string{"终局秘密"},
			},
		})
	}
	relationships := make([]domain.CharacterRelationship, 0, 6)
	pairs := [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 0}, {0, 2}, {1, 3}}
	for index := 0; index < 6; index++ {
		relationships = append(relationships, domain.CharacterRelationship{
			ID:                fmt.Sprintf("detail-relationship-%d", index+1),
			SourceCharacterID: characters[pairs[index][0]].ID, TargetCharacterID: characters[pairs[index][1]].ID,
			Type: domain.RelationshipTypeOther, Direction: domain.RelationshipDirectionDirected,
			Status: domain.RelationshipStatusPlanned, Label: "会改变主线选择的关系",
			Description: strings.Repeat("关系从当前状态向目标状态递进，不得跳变。", 12),
			Constraints: []string{"必须通过可见选择改变关系"},
		})
	}
	rules := make([]domain.WorldRule, 0, 25)
	for index := 1; index <= 25; index++ {
		rules = append(rules, domain.WorldRule{
			ID: fmt.Sprintf("detail-rule-%02d", index), Category: "continuity",
			Rule: strings.Repeat("规则正文及因果约束。", 18), Boundary: "适用于全部规划阶段",
			Strength: domain.WorldRuleStrengthHard,
		})
	}
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    strings.Repeat("确认的故事前提、人物关系和结局承诺。", 80),
		Characters: characters, Relationships: relationships,
		RelationshipsReviewed: true, WorldRules: rules,
	}, 0); err != nil {
		t.Fatal(err)
	}
	volumes := make([]domain.VolumeOutline, 0, 160)
	for volume := 1; volume <= 160; volume++ {
		arcs := []domain.ArcOutline{
			{Index: 1, Title: "进入", Goal: strings.Repeat("目标、阻力、选择、代价和结果。", 20), EstimatedChapters: 4},
			{Index: 2, Title: "转折", Goal: strings.Repeat("人物、关系和信息状态递进。", 20), EstimatedChapters: 4},
			{Index: 3, Title: "兑现", Goal: strings.Repeat("高潮兑现并生成下一卷行动问题。", 20), EstimatedChapters: 4},
		}
		if volume == 80 {
			chapters := make([]domain.OutlineEntry, 0, 4)
			for chapter := 1; chapter <= 4; chapter++ {
				chapters = append(chapters, domain.OutlineEntry{
					ID: fmt.Sprintf("ch_%032x", chapter), Chapter: 949 + chapter - 1,
					Title:     fmt.Sprintf("前弧证据章%d", chapter),
					CoreEvent: strings.Repeat("前弧目标、阻力、选择、代价、结果及信息变化。", 20),
					Hook:      strings.Repeat("前弧结果生成下一章行动问题。", 12),
					Scenes:    []string{strings.Repeat("完整递进场景。", 30)},
					CharacterIDs: []string{
						characters[0].ID, characters[1].ID,
					},
					CharacterBeats: []domain.OutlineCharacterBeat{{
						CharacterID: characters[0].ID,
						Goal:        strings.Repeat("前弧目标。", 20),
						Obstacle:    strings.Repeat("前弧阻力。", 20),
						ChoiceCost:  strings.Repeat("前弧选择代价。", 20),
						Advance:     strings.Repeat("前弧状态推进。", 20),
					}},
					RelationshipBeats: []domain.OutlineRelationshipBeat{{
						RelationshipID:    relationships[0].ID,
						SourceCharacterID: relationships[0].SourceCharacterID,
						TargetCharacterID: relationships[0].TargetCharacterID,
						ExpectedAdvance:   strings.Repeat("前弧关系推进。", 20),
					}},
				})
			}
			arcs[0].EstimatedChapters = 0
			arcs[0].Chapters = chapters
		}
		volumes = append(volumes, domain.VolumeOutline{
			Index: volume, Title: fmt.Sprintf("第%03d卷", volume),
			Theme: strings.Repeat("本卷核心冲突、不可逆结果与下卷交棒。", 15),
			Arcs:  arcs,
		})
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusCollecting, Kind: domain.PlanningReviewKindVolumeSplit,
		Brief: strings.Repeat("用户确认的共创事实必须持续落实。", 20),
	}); err != nil {
		t.Fatal(err)
	}
	approveFoundationToolFixture(t, st)
	if err := st.OriginalPlanningAudits.Save(domain.OriginalPlanningAudit{
		Scope: "skeleton_volume", Volume: 80, Verdict: "pass",
		Summary:    "第80卷审核通过，但细纲必须让助理执行落地动作，并呈现对手侧的具体商业异常。",
		Dimensions: []domain.OriginalPlanningAuditDimension{{Name: "volume_function", Score: 8.5, Comment: "卷功能清晰"}},
		Issues: []domain.OriginalPlanningAuditIssue{{
			Severity: "minor", Arc: 2, Description: "执行动作需要落到场景。",
			RepairInstruction: "细纲中明确助理执行与商业异常的证据链。",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.Save(domain.OriginalPlanningAudit{
		Scope: "arc", Volume: 80, Arc: 1, FromChapter: 949, ToChapter: 952, Verdict: "pass",
		Summary:    "prior arc passed with verified causal and relationship handoff",
		Dimensions: []domain.OriginalPlanningAuditDimension{{Name: "continuity", Score: 8.5, Comment: "handoff verified"}},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := NewContextTool(st, References{}, "default").Execute(
		context.Background(), json.RawMessage(`{"scope":"planning_detail","volume":80,"arc":2}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > planningDetailContextSourceBytes {
		var diagnostic map[string]any
		_ = json.Unmarshal(raw, &diagnostic)
		planningBytes, _ := json.Marshal(diagnostic["planning_memory"])
		foundationBytes, _ := json.Marshal(diagnostic["foundation_memory"])
		foundation, _ := diagnostic["foundation_memory"].(map[string]any)
		characterBytes, _ := json.Marshal(foundation["characters"])
		relationshipBytes, _ := json.Marshal(foundation["planned_relationships"])
		ruleBytes, _ := json.Marshal(foundation["world_rules"])
		statusBytes, _ := json.Marshal(foundation["foundation_status"])
		referenceBytes, _ := json.Marshal(diagnostic["reference_pack"])
		workingBytes, _ := json.Marshal(diagnostic["working_memory"])
		t.Fatalf(
			"planning detail context = %d bytes, want <= %d (planning=%d foundation=%d chars=%d relationships=%d rules=%d status=%d references=%d working=%d)",
			len(raw), planningDetailContextSourceBytes, len(planningBytes), len(foundationBytes), len(characterBytes), len(relationshipBytes), len(ruleBytes), len(statusBytes), len(referenceBytes), len(workingBytes),
		)
	}
	text := string(raw)
	for _, required := range []string{
		`"context_profile":"planning_detail"`, `"detail-character-4"`,
		`"detail-relationship-6"`, `"detail-rule-25"`, `"approved_volume_audit"`,
		`"approved_prior_arc_audits"`, `"chapter_evidence_schema"`,
		"prior arc passed", "助理执行落地动作", "商业异常",
		`"volume":80`, `"arc":2`, `[160,`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("planning detail context is missing %q", required)
		}
	}
	if strings.Contains(text, `"character_beats"`) {
		t.Fatal("planning detail repeated completed-arc character beats instead of its passed handoff evidence")
	}
	var payload struct {
		Planning struct {
			Layered []map[string]any `json:"layered_outline"`
			History [][]any          `json:"volume_history_index"`
		} `json:"planning_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Planning.History) != 160 {
		t.Fatalf("history covered %d/160 volumes", len(payload.Planning.History))
	}
	if len(payload.Planning.Layered) != 3 {
		t.Fatalf("detail body covered %d volumes, want previous/current/next", len(payload.Planning.Layered))
	}
}

func TestCompactPlanningDetailKeepsFullEvidenceOnlyForImmediatePriorArc(t *testing.T) {
	chapter := func(number int, title string) domain.OutlineEntry {
		return domain.OutlineEntry{
			ID: fmt.Sprintf("ch_%032x", number), Chapter: number, Title: title,
			CoreEvent: "目标、阻力、选择、代价、结果与信息变化",
			Hook:      "由结果生成下一章行动问题",
			CharacterBeats: []domain.OutlineCharacterBeat{{
				CharacterID: "hero", Goal: "目标", Advance: "推进",
			}},
		}
	}
	payload := compactLayeredOutlineForPlanningDetail([]domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{chapter(1, "已封存前史")}},
			{Index: 2, Chapters: []domain.OutlineEntry{chapter(2, "直接承接弧")}},
			{Index: 3, EstimatedChapters: 4, Title: "当前目标弧"},
		},
	}}, 1, 3)
	arcs := payload[0]["arcs"].([]map[string]any)
	if _, ok := arcs[0]["chapter_index"]; !ok {
		t.Fatalf("older arc did not collapse to its audited chapter index: %+v", arcs[0])
	}
	if _, ok := arcs[0]["chapter_evidence"]; ok {
		t.Fatalf("older arc repeated detailed handoff evidence: %+v", arcs[0])
	}
	if _, ok := arcs[1]["chapter_evidence"]; !ok {
		t.Fatalf("immediate prior arc lost direct handoff evidence: %+v", arcs[1])
	}
}

func TestContextToolPlanningReviewIsEditorBoundedWithoutDroppingCanonicalFacts(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	characters := make([]domain.Character, 0, 24)
	for index := 1; index <= 24; index++ {
		characters = append(characters, domain.Character{
			ID: fmt.Sprintf("character-%02d", index), Name: fmt.Sprintf("角色%02d", index),
			Role: strings.Repeat("结构职责", 20), Tier: "important",
			Goal: strings.Repeat("长期目标", 30), Motivation: strings.Repeat("核心动机", 30),
			Conflict: strings.Repeat("内外冲突", 30), Arc: strings.Repeat("阶段成长", 40),
			Constraints: []string{strings.Repeat("不可违反约束", 20)},
		})
	}
	rules := make([]domain.WorldRule, 0, 25)
	for index := 1; index <= 25; index++ {
		rules = append(rules, domain.WorldRule{
			ID: fmt.Sprintf("rule-%02d", index), Category: "continuity",
			Rule:     strings.Repeat("世界规则与因果边界。", 30),
			Boundary: strings.Repeat("适用边界。", 20), Strength: domain.WorldRuleStrengthHard,
		})
	}
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    strings.Repeat("用户确认的完整故事前提。", 100),
		Characters: characters, WorldRules: rules,
	}, 0); err != nil {
		t.Fatal(err)
	}
	volumes := make([]domain.VolumeOutline, 0, 160)
	for volume := 1; volume <= 160; volume++ {
		volumes = append(volumes, domain.VolumeOutline{
			Index: volume, Title: fmt.Sprintf("第%03d卷", volume),
			Theme: strings.Repeat("主题冲突。", 20),
			Arcs: []domain.ArcOutline{
				{Index: 1, Title: "上升", Goal: strings.Repeat("因果推进。", 40), EstimatedChapters: 4},
				{Index: 2, Title: "转折", Goal: strings.Repeat("人物选择。", 40), EstimatedChapters: 4},
				{Index: 3, Title: "兑现", Goal: strings.Repeat("高潮兑现。", 40), EstimatedChapters: 4},
			},
		})
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusCollecting,
		Kind:   domain.PlanningReviewKindBlueprint,
		Brief:  strings.Repeat("用户已确认且不能丢失的共创约束。", 160),
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := NewContextTool(st, References{}, "default").Execute(
		context.Background(),
		json.RawMessage(`{"scope":"planning_review","volume":80}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > planningReviewContextSourceBytes {
		t.Fatalf("planning review context = %d bytes, want <= %d", len(raw), planningReviewContextSourceBytes)
	}
	text := string(raw)
	for _, required := range []string{`"character-24"`, `"rule-25"`, `[160,`, `"index":80`, `"arcs"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("planning review context is missing %s", required)
		}
	}
	if strings.Contains(text, `"reference_pack"`) {
		t.Fatal("planning review must not carry Architect-only templates")
	}
}

func TestContextToolPlanningAuditCombinesFourChaptersAndCanonicalFactsAtBookScale(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	characters := make([]domain.Character, 0, 4)
	for index := 1; index <= 4; index++ {
		characters = append(characters, domain.Character{
			ID: fmt.Sprintf("audit-character-%d", index), Name: fmt.Sprintf("审核角色%d", index),
			Role: strings.Repeat("不可替代职责", 10), Tier: "core",
			Goal: strings.Repeat("长期目标", 15), Motivation: strings.Repeat("核心动机", 15),
			Conflict: strings.Repeat("因果冲突", 15), Arc: strings.Repeat("阶段成长", 20),
			Constraints: []string{"不得越过已确认的信息边界"},
		})
	}
	relationships := make([]domain.CharacterRelationship, 0, 6)
	pairs := [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 0}, {0, 2}, {1, 3}}
	for index, pair := range pairs {
		relationships = append(relationships, domain.CharacterRelationship{
			ID:                fmt.Sprintf("audit-relationship-%d", index+1),
			SourceCharacterID: characters[pair[0]].ID,
			TargetCharacterID: characters[pair[1]].ID,
			Type:              domain.RelationshipTypeOther, Direction: domain.RelationshipDirectionDirected,
			Status: domain.RelationshipStatusPlanned, Label: "改变主线选择的关系",
			Constraints: []string{"关系变化必须来自可见选择"},
		})
	}
	worldRules := make([]domain.WorldRule, 0, 25)
	for index := 1; index <= 25; index++ {
		worldRules = append(worldRules, domain.WorldRule{
			ID: fmt.Sprintf("audit-rule-%02d", index), Category: "continuity",
			Rule:     "时间线、证据来源与人物知识必须可验证",
			Boundary: "全书", Strength: domain.WorldRuleStrengthHard,
		})
	}
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    strings.Repeat("用户确认的故事前提与终局承诺。", 40),
		Characters: characters, Relationships: relationships,
		RelationshipsReviewed: true, WorldRules: worldRules,
	}, 0); err != nil {
		t.Fatal(err)
	}

	volumes := make([]domain.VolumeOutline, 0, 160)
	for volume := 1; volume <= 160; volume++ {
		arcs := []domain.ArcOutline{
			{Index: 1, Title: "进入", Goal: "建立目标、阻力与行动代价", EstimatedChapters: 4},
			{Index: 2, Title: "转折", Goal: "人物选择改变关系和信息状态", EstimatedChapters: 4},
			{Index: 3, Title: "兑现", Goal: "兑现高潮并交棒下一阶段", EstimatedChapters: 4},
		}
		if volume == 80 {
			chapters := make([]domain.OutlineEntry, 0, 4)
			for chapter := 0; chapter < 4; chapter++ {
				characterBeats := make([]domain.OutlineCharacterBeat, 0, 4)
				relationshipBeats := make([]domain.OutlineRelationshipBeat, 0, 4)
				for beat := 0; beat < 4; beat++ {
					characterBeats = append(characterBeats, domain.OutlineCharacterBeat{
						CharacterID: characters[beat].ID,
						Scene:       strings.Repeat("对应场景不可丢失。", 20),
						Goal:        strings.Repeat("本章可验证目标。", 20),
						Obstacle:    strings.Repeat("主动阻力。", 20),
						ChoiceCost:  strings.Repeat("选择与代价。", 20),
						Advance:     strings.Repeat("状态不可逆推进。", 20),
					})
					relationship := relationships[beat]
					relationshipBeats = append(relationshipBeats, domain.OutlineRelationshipBeat{
						RelationshipID:    relationship.ID,
						SourceCharacterID: relationship.SourceCharacterID,
						TargetCharacterID: relationship.TargetCharacterID,
						Scene:             strings.Repeat("关系所在场景。", 20),
						Start:             strings.Repeat("关系起始状态。", 20),
						ExpectedAdvance:   "关系变化影响下一章选择",
						ForbiddenJump:     strings.Repeat("禁止关系跨级。", 20),
					})
				}
				chapters = append(chapters, domain.OutlineEntry{
					ID:      fmt.Sprintf("ch_%032x", chapter+1),
					Chapter: 949 + chapter, Title: fmt.Sprintf("审核章%d", chapter+1),
					CoreEvent: strings.Repeat("目标、主动阻力、关键选择与代价、不可逆结果。", 20),
					Hook:      strings.Repeat("由本章结果生成下一章行动问题。", 12),
					Scenes: []string{
						strings.Repeat("场景一递进动作与信息变化。", 15),
						strings.Repeat("场景二选择及关系后果。", 15),
						strings.Repeat("场景三不可逆收束。", 15),
						"scene-four-mandatory-aftercare",
						"scene-five-mandatory-handoff",
					},
					CharacterIDs:      []string{characters[0].ID, characters[1].ID},
					CharacterBeats:    characterBeats,
					RelationshipBeats: relationshipBeats,
				})
			}
			arcs[0].EstimatedChapters = 0
			arcs[0].Chapters = chapters
		}
		volumes = append(volumes, domain.VolumeOutline{
			Index: volume, Title: fmt.Sprintf("第%03d卷", volume),
			Theme: "入卷状态、核心冲突、不可逆成果与出卷交棒", Arcs: arcs,
		})
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	progress, _ := st.Progress.Load()
	if progress == nil {
		progress = &domain.Progress{Phase: domain.PhaseOutline, TotalChapters: 1920}
	}
	progress.Layered = true
	progress.CurrentVolume = 80
	progress.CurrentArc = 1
	progress.CurrentChapter = 949
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}

	raw, err := NewContextTool(st, References{}, "default").Execute(
		context.Background(),
		json.RawMessage(`{"scope":"planning_audit","volume":80,"arc":1,"from":949,"to":952}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > planningAuditContextSourceBytes {
		t.Fatalf("planning audit context = %d bytes, want <= %d", len(raw), planningAuditContextSourceBytes)
	}
	text := string(raw)
	for _, required := range []string{
		`"audit-character-4"`,
		`"audit-relationship-6"`,
		`"audit-rule-25"`,
		`"relationship_schema"`,
		`"world_rule_schema"`,
		`"ch_00000000000000000000000000000004"`,
		`"scene-five-mandatory-handoff"`,
		`"scene_count":5`,
		`"scene_counts":{"949":5,"950":5,"951":5,"952":5}`,
		`"outline_character_beat_schema"`,
		`"outline_relationship_beat_schema"`,
		`"audit-character-1"`,
		`"audit-relationship-1"`,
		`"关系变化影响下一章选择"`,
		`"from_chapter":949`,
		`"to_chapter":952`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("planning audit context is missing %s", required)
		}
	}
	if strings.Contains(text, `"nearby_chapters"`) || strings.Contains(text, `"reference_pack"`) {
		t.Fatal("planning audit duplicated chapters or Architect-only references")
	}
	if strings.Contains(text, `"user_rules"`) || strings.Contains(text, `"simulation_profile"`) {
		t.Fatal("planning audit must not duplicate prose-only rules or generic simulation guidance")
	}
}

func TestCompactSkeletonAuditIndexScalesWithoutDroppingBatchCoverage(t *testing.T) {
	audits := make([]domain.OriginalPlanningAudit, 0, 240)
	for volume := 1; volume <= 160; volume++ {
		audits = append(audits, domain.OriginalPlanningAudit{
			Scope: "skeleton_volume", Volume: volume, Verdict: "pass",
			Summary: strings.Repeat("逐卷审核结论完整落盘。", 30),
			Dimensions: []domain.OriginalPlanningAuditDimension{
				{Name: "volume_function", Score: 8.5},
				{Name: "arc_causality", Score: 8},
			},
		})
	}
	for from := 1; from <= 160; from += 2 {
		audits = append(audits, domain.OriginalPlanningAudit{
			Scope: "skeleton_book_batch", FromVolume: from, ToVolume: min(from+1, 160), Verdict: "pass",
			Summary: strings.Repeat("跨卷因果、人物成长与伏笔传递审核通过。", 30),
			Dimensions: []domain.OriginalPlanningAuditDimension{
				{Name: "cross_volume_continuity", Score: 8.5},
				{Name: "setup_payoff", Score: 7.5},
			},
		})
	}

	batch := compactSkeletonAuditIndex(audits, 79, 80)
	if len(batch) != 2 || batch[0]["volume"] != 79 || batch[1]["volume"] != 80 {
		t.Fatalf("batch audit coverage = %+v", batch)
	}
	global := compactSkeletonAuditIndex(audits, 0, 0)
	if len(global) != 80 || global[0]["from_volume"] != 1 || global[79]["to_volume"] != 160 {
		t.Fatalf("global audit coverage = %d entries, first=%+v last=%+v", len(global), global[0], global[len(global)-1])
	}
	raw, err := json.Marshal(global)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 20_000 {
		t.Fatalf("global audit index = %d bytes, want <= 20000", len(raw))
	}
}

func TestCompactSkeletonAuditIndexCarriesDetailedAggregateEvidence(t *testing.T) {
	audits := []domain.OriginalPlanningAudit{
		{
			Scope: "arc", Volume: 7, Arc: 1, Verdict: "pass", Summary: "弧一完成因果推进",
			Dimensions: []domain.OriginalPlanningAuditDimension{{Name: "causal_progression", Score: 8}},
		},
		{
			Scope: "arc", Volume: 7, Arc: 2, Verdict: "pass", Summary: "弧二兑现人物选择",
			Dimensions: []domain.OriginalPlanningAuditDimension{{Name: "character_logic", Score: 8.5}},
		},
		{
			Scope: "volume", Volume: 7, Verdict: "pass", Summary: "第七卷结构与高潮通过",
			Dimensions: []domain.OriginalPlanningAuditDimension{{Name: "climax_payoff", Score: 8}},
		},
		{
			Scope: "book_batch", FromVolume: 7, ToVolume: 8, Verdict: "pass",
			Summary:    "第七至八卷因果承接与伏笔传递通过",
			Dimensions: []domain.OriginalPlanningAuditDimension{{Name: "cross_volume_continuity", Score: 8}},
		},
	}

	volume := compactSkeletonAuditIndex(audits, 7, 7)
	if len(volume) != 3 ||
		volume[0]["arc"] != 1 ||
		volume[1]["arc"] != 2 ||
		volume[2]["scope"] != "volume" {
		t.Fatalf("detailed volume audit evidence = %+v", volume)
	}
	global := compactSkeletonAuditIndex(audits, 0, 0)
	if len(global) != 1 ||
		global[0]["scope"] != "book_batch" ||
		global[0]["from_volume"] != 7 ||
		global[0]["to_volume"] != 8 {
		t.Fatalf("detailed global audit evidence = %+v", global)
	}
}

func TestContextToolSelectedMemoryRecallsStoryThreadsAndReviewLessons(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "邀约", CoreEvent: "长老暗中给出内门试炼邀请", Scenes: []string{"密谈", "留下试炼令"}},
		{Chapter: 2, Title: "试炼前夜", CoreEvent: "林砚准备回应内门试炼邀请", Hook: "谁在背后推动这场试炼", Scenes: []string{"整理线索", "决定赴约"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 8); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "trial_invite", Description: "内门试炼邀请的真实目的", PlantedAt: 1, Status: "planted"},
		{ID: "trial_mastermind", Description: "谁在背后推动这场试炼", PlantedAt: 1, Status: "planted"},
		{ID: "trial_rules", Description: "试炼规则碑文残卷", PlantedAt: 1, Status: "planted"},
		{ID: "outer_disciple", Description: "外门弟子的旧债纠纷", PlantedAt: 1, Status: "planted"},
		{ID: "elder_token", Description: "长老手中令牌的来历", PlantedAt: 1, Status: "planted"},
		{ID: "hidden_gate", Description: "山门背后的隐藏通道", PlantedAt: 1, Status: "planted"},
		{ID: "trial_bet", Description: "试炼盘口的幕后操盘人", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 2,
		Title:   "试炼前夜",
		Goal:    "决定是否回应邀请",
		Contract: domain.ChapterContract{
			PayoffPoints: []string{"回应内门试炼邀请"},
			HookGoal:     "抛出谁在背后推动试炼",
		},
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter:        1,
		Scope:          "chapter",
		Verdict:        "polish",
		Summary:        "主线启动完成，但伏笔不够明确。",
		ContractStatus: "partial",
		ContractMisses: []string{"未明确埋下内门试炼邀请"},
		Issues: []domain.ConsistencyIssue{
			{Type: "hook", Severity: "warning", Description: "章末钩子不够具体"},
		},
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			StoryThreads  []domain.RecallItem `json:"story_threads"`
			ReviewLessons []domain.RecallItem `json:"review_lessons"`
		} `json:"selected_memory"`
		Summary string `json:"_loading_summary"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Selected.StoryThreads) == 0 {
		t.Fatal("expected story thread recall items")
	}
	if len(payload.Selected.ReviewLessons) == 0 {
		t.Fatal("expected review lesson recall items")
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "内门试炼邀请") {
		t.Fatalf("expected story thread recall to mention invite, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "推动这场试炼") {
		t.Fatalf("expected story thread recall to mention trial mastermind, got %+v", payload.Selected.StoryThreads)
	}
	if containsRecallSummary(payload.Selected.StoryThreads, "试炼规则碑文残卷") {
		t.Fatalf("expected weak-overlap foreshadow to stay out, got %+v", payload.Selected.StoryThreads)
	}
	if containsRecallSummary(payload.Selected.StoryThreads, "建议回看第") {
		t.Fatalf("expected related_chapters not to be duplicated into story_threads, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.ReviewLessons, "contract 漏项") {
		t.Fatalf("expected review lesson recall to mention contract miss, got %+v", payload.Selected.ReviewLessons)
	}
	if !strings.Contains(payload.Summary, "线索召回:") || !strings.Contains(payload.Summary, "评审召回:") {
		t.Fatalf("expected loading summary to report selected memory, got %q", payload.Summary)
	}
}

// 久挂未回收的伏笔即使与当前章关键词无关，也应被账龄回填进 story_threads——
// 这正是相关性召回的盲区（独自悬挂太久、却没在本章撞上关键词的那根线）。
// 近期埋下的伏笔（账龄 < 阈值）不应被误标为"未回收"。
func TestContextToolSelectedMemorySurfacesAgingForeshadow(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 当前章主题与所有伏笔都不沾边，确保相关性召回为空，只剩账龄回填生效。
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 50, Title: "瘟疫", CoreEvent: "林砚在城南医馆救治瘟疫病患", Scenes: []string{"熬药", "封锁街巷"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 60); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	// 6 条满足召回阈值；前两条账龄 ≥30（久挂），后四条账龄 <30（近期）。
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "ancient_seal", Description: "上古封印的裂隙", PlantedAt: 3, Status: "planted"},
		{ID: "lost_bloodline", Description: "主角失落的血脉来历", PlantedAt: 5, Status: "advanced"},
		{ID: "market_feud", Description: "昨夜集市的口角", PlantedAt: 47, Status: "planted"},
		{ID: "rumor_a", Description: "近日传闻甲", PlantedAt: 48, Status: "planted"},
		{ID: "rumor_b", Description: "近日传闻乙", PlantedAt: 48, Status: "planted"},
		{ID: "rumor_c", Description: "近日传闻丙", PlantedAt: 49, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 50})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			StoryThreads []domain.RecallItem `json:"story_threads"`
		} `json:"selected_memory"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// 两条久挂伏笔应被回填，且带"未回收"账龄标注。
	if !containsRecallSummary(payload.Selected.StoryThreads, "上古封印的裂隙") {
		t.Fatalf("expected aging foreshadow to surface despite no relevance, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "失落的血脉") {
		t.Fatalf("expected second aging foreshadow to surface, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "未回收") {
		t.Fatalf("expected aging item to carry overdue annotation, got %+v", payload.Selected.StoryThreads)
	}
	// 近期伏笔（账龄 <30 且不相关）不应被回填。
	if containsRecallSummary(payload.Selected.StoryThreads, "昨夜集市的口角") {
		t.Fatalf("recent foreshadow must not be labeled overdue, got %+v", payload.Selected.StoryThreads)
	}
}

func TestContextToolSelectedMemoryIncludesGlobalReviewLessons(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "开端", CoreEvent: "故事开始"},
		{Chapter: 2, Title: "推进", CoreEvent: "主线继续推进"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 6); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 1,
		Scope:   "global",
		Verdict: "polish",
		Summary: "全局推进合格，但角色目标表达还不够稳定。",
		Issues: []domain.ConsistencyIssue{
			{Type: "character", Severity: "warning", Description: "主角目标表达不够稳定"},
		},
	}); err != nil {
		t.Fatalf("SaveReview(global): %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			ReviewLessons []domain.RecallItem `json:"review_lessons"`
		} `json:"selected_memory"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !containsRecallSummary(payload.Selected.ReviewLessons, "主角目标表达不够稳定") {
		t.Fatalf("expected global review lesson to be recalled, got %+v", payload.Selected.ReviewLessons)
	}
}

func TestContextToolInjectsOnlyStructuredActiveAdaptationRules(t *testing.T) {
	plainStore := store.NewStore(testStoreDir(t))
	if err := plainStore.Init(); err != nil {
		t.Fatalf("Init plain store: %v", err)
	}
	if err := plainStore.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "开端", CoreEvent: "故事开始"}}); err != nil {
		t.Fatalf("SaveOutline plain: %v", err)
	}
	if err := plainStore.Progress.Init("plain", 1); err != nil {
		t.Fatalf("Init plain progress: %v", err)
	}
	refs := References{
		AdaptationWriter:                "禁止使用（某某内心独白：...）这类补丁标签。",
		AdaptationEditorPreserveDetails: "preserve_details 审阅：禁止内心独白仅为示意。",
		AdaptationEditorFullRewrite:     "full_rewrite 审阅：禁止搬运原文。",
	}
	plainTool := NewContextTool(plainStore, refs, "default")
	plainArgs, _ := json.Marshal(map[string]any{"chapter": 1})
	plainRaw, err := plainTool.Execute(context.Background(), plainArgs)
	if err != nil {
		t.Fatalf("Execute plain: %v", err)
	}
	var plainPayload struct {
		Working map[string]json.RawMessage `json:"working_memory"`
	}
	if err := json.Unmarshal(plainRaw, &plainPayload); err != nil {
		t.Fatalf("Unmarshal plain: %v", err)
	}
	if _, ok := plainPayload.Working["adaptation_writing_guidance"]; ok {
		t.Fatal("plain writing context must not include adaptation writer guidance")
	}
	if _, ok := plainPayload.Working["adaptation_editor_guidance"]; ok {
		t.Fatal("plain writing context must not include adaptation editor guidance")
	}

	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		ModePolicy:    domain.AdaptationPolicyDetailPreservationWithSplit,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Status:        domain.AdaptationPlanStatusConfirmed,
		Brief:         "禁止使用补丁标签",
		Rules: []domain.AdaptationRule{{
			ID:   "brief-label",
			Kind: domain.AdaptationRuleForbidden,
			Text: "禁止使用补丁标签",
			Mode: domain.AdaptationGranularityChapter,
		}},
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "目标章",
			SourceChapters: []int{1},
			SourceRunes:    100,
			TargetRunes:    100,
			TargetMinRunes: 85,
			TargetMaxRunes: 115,
			RuleIDs:        []string{"brief-label"},
		}},
	}
	adaptStore := newAdaptationToolStoreWithPlan(t, plan, []string{"原文主线事件。"})
	if err := adaptStore.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "目标章", CoreEvent: "改编事件"}}); err != nil {
		t.Fatalf("SaveOutline adapt: %v", err)
	}
	if err := adaptStore.Progress.Init("adapt", 1); err != nil {
		t.Fatalf("Init adapt progress: %v", err)
	}
	adaptTool := NewContextTool(adaptStore, refs, "default")
	adaptRaw, err := adaptTool.Execute(context.Background(), plainArgs)
	if err != nil {
		t.Fatalf("Execute adapt: %v", err)
	}
	var adaptPayload struct {
		AdaptationMode bool                       `json:"adaptation_mode"`
		Working        map[string]json.RawMessage `json:"working_memory"`
	}
	if err := json.Unmarshal(adaptRaw, &adaptPayload); err != nil {
		t.Fatalf("Unmarshal adapt: %v", err)
	}
	if !adaptPayload.AdaptationMode {
		t.Fatal("expected adaptation_mode=true")
	}
	if _, ok := adaptPayload.Working["adaptation_writing_guidance"]; ok {
		t.Fatal("adaptation context must not inject the all-mode writer markdown")
	}
	if _, ok := adaptPayload.Working["adaptation_editor_guidance"]; ok {
		t.Fatal("writer context must not inject editor markdown")
	}
	rawRules, ok := adaptPayload.Working["adaptation_active_rules"]
	if !ok {
		t.Fatal("adaptation context should include task-scoped structured rules")
	}
	var activeRules []domain.AdaptationRule
	if err := json.Unmarshal(rawRules, &activeRules); err != nil {
		t.Fatalf("Unmarshal active rules: %v", err)
	}
	if len(activeRules) != 1 || activeRules[0].ID != "brief-label" {
		t.Fatalf("active rules mismatch: %+v", activeRules)
	}
}

func TestContextToolShowsDisabledWordToleranceForFullRewrite(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:    domain.AdaptationGranularityArc,
		RewritePolicy:  domain.AdaptationRewritePreserveDetails,
		Status:         domain.AdaptationPlanStatusConfirmed,
		WordTolerance:  0.15,
		TargetMinRunes: 85,
		TargetMaxRunes: 115,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "目标章",
			SourceChapters: []int{1},
			SourceRunes:    100,
			TargetRunes:    100,
			TargetMinRunes: 85,
			TargetMaxRunes: 115,
		}},
	}
	adaptStore := newAdaptationToolStoreWithPlan(t, plan, []string{"原文主线事件。"})
	if err := adaptStore.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "目标章", CoreEvent: "改编事件"}}); err != nil {
		t.Fatalf("SaveOutline adapt: %v", err)
	}
	if err := adaptStore.Progress.Init("adapt", 1); err != nil {
		t.Fatalf("Init adapt progress: %v", err)
	}

	tool := NewContextTool(adaptStore, References{}, "default")
	args, _ := json.Marshal(map[string]any{"chapter": 1})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Working map[string]json.RawMessage `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	var adaptation map[string]any
	if err := json.Unmarshal(payload.Working["adaptation"], &adaptation); err != nil {
		t.Fatalf("Unmarshal adaptation: %v", err)
	}
	if adaptation["word_tolerance"] != "disabled" {
		t.Fatalf("word_tolerance=%v, want disabled", adaptation["word_tolerance"])
	}
}

func TestContextToolClarifiesFreeFullRewriteSourceRefsAreAnchors(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityFree,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Status:        domain.AdaptationPlanStatusConfirmed,
		Brief:         "rewrite_policy_rule=chapter=>preserve_details;arc/free=>full_rewrite\n自由重构结局。",
		WordTolerance: 0.15,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        53,
			Title:          "目标章",
			SourceChapters: []int{17},
			SourceRunes:    100,
			TargetRunes:    2000,
			TargetMinRunes: 1800,
			TargetMaxRunes: 2200,
			SourceRange:    domain.SourceRange{From: 17, To: 17},
		}},
	}
	sourceTexts := make([]string, 17)
	for i := range sourceTexts {
		sourceTexts[i] = "原文主线事件。"
	}
	adaptStore := newAdaptationToolStoreWithPlan(t, plan, sourceTexts)
	if err := adaptStore.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 53, Title: "目标章", CoreEvent: "新剧情推进"}}); err != nil {
		t.Fatalf("SaveOutline adapt: %v", err)
	}
	if err := adaptStore.Progress.Init("adapt", 59); err != nil {
		t.Fatalf("Init adapt progress: %v", err)
	}

	tool := NewContextTool(adaptStore, References{}, "default")
	args, _ := json.Marshal(map[string]any{"chapter": 53})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Working map[string]json.RawMessage `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	var mode struct {
		Granularity               string   `json:"granularity"`
		RewritePolicy             string   `json:"rewrite_policy"`
		SourceReferencePolicy     string   `json:"source_reference_policy"`
		SourceMappingMeaning      string   `json:"source_mapping_meaning"`
		SourceReadInstruction     string   `json:"source_read_instruction"`
		BudgetInstruction         string   `json:"budget_instruction"`
		LegacyRewritePolicyNotice string   `json:"legacy_rewrite_policy_notice"`
		PreserveDetailsApplicable bool     `json:"preserve_details_applicable"`
		MustNot                   []string `json:"must_not"`
	}
	if err := json.Unmarshal(payload.Working["adaptation_effective_mode"], &mode); err != nil {
		t.Fatalf("Unmarshal adaptation_effective_mode: %v", err)
	}
	if mode.Granularity != domain.AdaptationGranularityFree ||
		mode.RewritePolicy != domain.AdaptationRewriteFullRewrite ||
		mode.SourceReferencePolicy != "optional_background_anchor" ||
		mode.PreserveDetailsApplicable {
		t.Fatalf("free effective mode mismatch: %+v", mode)
	}
	for _, want := range []string{"不表示目标章对应原著章节", "不要因为 source_chapters/source_range 存在就读取原文", "rewrite_policy_rule", "word_budget 是提案规划参考而非正文硬上限", "适度超过 max_runes 可以保留并提交"} {
		joined := strings.Join([]string{mode.SourceMappingMeaning, mode.SourceReadInstruction, mode.BudgetInstruction, mode.LegacyRewritePolicyNotice, strings.Join(mode.MustNot, "\n")}, "\n")
		if !strings.Contains(joined, want) {
			t.Fatalf("free effective mode missing %q:\n%+v", want, mode)
		}
	}
}

func TestContextToolKeepsFullForeshadowWhenRecallNotTriggered(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "起势", CoreEvent: "故事起势"},
		{Chapter: 2, Title: "推进", CoreEvent: "继续推进"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 4); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "small_1", Description: "第一条小伏笔", PlantedAt: 1, Status: "planted"},
		{ID: "small_2", Description: "第二条小伏笔", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	episodic, _ := payload["episodic_memory"].(map[string]any)
	if _, ok := episodic["foreshadow_ledger"]; !ok {
		t.Fatal("expected episodic_memory.foreshadow_ledger to remain when selected recall is not triggered")
	}
	if _, ok := payload["selected_memory"]; ok {
		t.Fatalf("expected no selected_memory for small foreshadow sets, got %+v", payload["selected_memory"])
	}
}

func TestContextToolFallsBackToFullForeshadowWhenSelectionIsTooSparse(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "邀约", CoreEvent: "长老暗中给出内门试炼邀请"},
		{Chapter: 2, Title: "试炼前夜", CoreEvent: "林砚准备回应内门试炼邀请", Scenes: []string{"整理线索", "决定赴约"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 8); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "trial_invite", Description: "内门试炼邀请的真实目的", PlantedAt: 1, Status: "planted"},
		{ID: "trial_rules", Description: "试炼规则碑文残卷", PlantedAt: 1, Status: "planted"},
		{ID: "outer_disciple", Description: "外门弟子的旧债纠纷", PlantedAt: 1, Status: "planted"},
		{ID: "elder_token", Description: "长老手中令牌的来历", PlantedAt: 1, Status: "planted"},
		{ID: "hidden_gate", Description: "山门背后的隐藏通道", PlantedAt: 1, Status: "planted"},
		{ID: "trial_bet", Description: "试炼盘口的幕后操盘人", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	episodic, _ := payload["episodic_memory"].(map[string]any)
	if _, ok := episodic["foreshadow_ledger"]; !ok {
		t.Fatal("expected episodic_memory.foreshadow_ledger when selection is too sparse")
	}
	if selected, ok := payload["selected_memory"].(map[string]any); ok {
		if _, exists := selected["story_threads"]; exists {
			t.Fatalf("expected sparse story_threads to fall back to full ledger, got %+v", selected["story_threads"])
		}
	}
}

func containsRecallSummary(items []domain.RecallItem, want string) bool {
	for _, item := range items {
		if strings.Contains(item.Summary, want) {
			return true
		}
	}
	return false
}

func TestContextToolInjectsRewriteBriefForPendingRewriteChapter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "节奏拖沓，需要压缩前半段"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 2,
		Scope:   "chapter",
		Verdict: "rewrite",
		Summary: "前半段铺垫过长，冲突迟迟不出现。",
		Issues: []domain.ConsistencyIssue{
			{Type: "pacing", Severity: "error", Description: "前 2000 字无推进"},
		},
		ContractMisses: []string{"未兑现试炼开场"},
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	working, _ := payload["working_memory"].(map[string]any)
	brief, ok := working["rewrite_brief"].(map[string]any)
	if !ok {
		t.Fatalf("expected working_memory.rewrite_brief in chapter context, got %T", working["rewrite_brief"])
	}
	if got := brief["reason"]; got != "节奏拖沓，需要压缩前半段" {
		t.Fatalf("expected rewrite reason, got %v", got)
	}
	if got, _ := brief["review_summary"].(string); !strings.Contains(got, "铺垫过长") {
		t.Fatalf("expected review summary from chapter review, got %v", brief["review_summary"])
	}
	if issues, _ := brief["issues"].([]any); len(issues) == 0 {
		t.Fatalf("expected review issues in rewrite_brief, got %v", brief["issues"])
	}
	if misses, _ := brief["contract_misses"].([]any); len(misses) == 0 {
		t.Fatalf("expected contract misses in rewrite_brief, got %v", brief["contract_misses"])
	}
}

func TestContextToolOmitsRewriteBriefForNormalChapter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	working, _ := payload["working_memory"].(map[string]any)
	if _, ok := working["rewrite_brief"]; ok {
		t.Fatal("expected no rewrite_brief for chapter outside PendingRewrites")
	}
}

func TestContextToolPolishingUsesChapterOwnedSourceProfile(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "前章", CoreEvent: strings.Repeat("前情", 500)},
		{Chapter: 2, Title: "当前章", CoreEvent: strings.Repeat("本章既定事件", 500), Hook: strings.Repeat("既定钩子", 300)},
		{Chapter: 3, Title: "后章", CoreEvent: strings.Repeat("未来事件", 500)},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("polish-source-profile", 3); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, strings.Repeat("需要局部打磨的正文。", 300)); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, strings.Repeat("需要局部打磨的正文。", 300)); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "只修正节奏、措辞和 AI 痕迹，不改变剧情合同"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 2,
		Title:   "当前章",
		Goal:    strings.Repeat("不应在打磨上下文重复的计划说明", 200),
		Contract: domain.ChapterContract{
			RequiredBeats:    []string{"保留既定事件"},
			ForbiddenMoves:   []string{"不得改变剧情"},
			ContinuityChecks: []string{"保持人物关系"},
			HookGoal:         "保留既定章末钩子",
		},
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 2,
		Scope:   "chapter",
		Verdict: "polish",
		Summary: "表达和节奏需打磨",
		Issues: []domain.ConsistencyIssue{{
			Type: "aesthetic", Severity: "warning", Description: "解释性复盘偏多", Suggestion: "逐处精确修订",
		}},
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{Prose: []string{"叙述克制", "用动作承载情绪"}}); err != nil {
		t.Fatalf("SaveStyleRules: %v", err)
	}
	if err := s.Characters.SaveSnapshots(1, 1, []domain.CharacterSnapshot{{Name: "林舒然", Status: "仍在犹豫", Relations: "与墨子曜保持信任"}}); err != nil {
		t.Fatalf("SaveSnapshots: %v", err)
	}
	if err := s.World.SaveRelationships([]domain.RelationshipEntry{{Chapter: 1, CharacterA: "林舒然", CharacterB: "墨子曜", Relation: "互相信任"}}); err != nil {
		t.Fatalf("SaveRelationships: %v", err)
	}
	longItems := []string{strings.Repeat("仿写观察", 100), strings.Repeat("节奏观察", 100), strings.Repeat("禁复制提醒", 100)}
	if err := s.Simulation.Save(domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Synthesis: domain.SimulationSynthesis{
			Style:         domain.SimulationStyle{NarrativeVoice: longItems, SentenceRhythm: longItems, ProseTexture: longItems, DoNotCopy: longItems},
			PlotDesign:    domain.SimulationPlotDesign{OpeningPatterns: longItems, EscalationPatterns: longItems},
			HookDesign:    domain.SimulationHookDesign{HookTypes: longItems, PayoffRules: longItems},
			PacingDensity: domain.SimulationPacingDensity{SceneDensity: longItems, InformationRelease: longItems, DialogueActionRatio: longItems},
			RoleGuidance:  domain.SimulationRoleGuidance{Writer: longItems, Architect: longItems, Editor: longItems},
		},
	}); err != nil {
		t.Fatalf("Simulation.Save: %v", err)
	}

	refs := References{
		Consistency:      strings.Repeat("全书一致性资料", 2000),
		HookTechniques:   strings.Repeat("钩子设计资料", 2000),
		QualityChecklist: strings.Repeat("质量检查", 2000),
		AntiAITone:       strings.Repeat("去AI规则", 3000),
	}
	raw, err := NewContextToolWithOptions(s, refs, "romance", ContextToolOptions{SimulationMode: "reinforced"}).Execute(context.Background(), json.RawMessage(`{"chapter":2}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerPolishingContextBytes {
		t.Fatalf("polishing context = %d bytes, want source-bounded <= %d", len(raw), writerPolishingContextBytes)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["context_profile"] != "polishing" {
		t.Fatalf("context_profile = %v, want polishing", payload["context_profile"])
	}
	working, _ := payload["working_memory"].(map[string]any)
	for _, key := range []string{"current_chapter_outline", "chapter_contract", "rewrite_brief", "chapter_draft", "user_rules", "simulation_contract"} {
		if _, ok := working[key]; !ok {
			t.Fatalf("polishing context missing working_memory.%s", key)
		}
	}
	for _, key := range []string{"chapter_plan", "future_chapter_promises", "recent_summaries", "timeline", "checkpoint"} {
		if _, ok := working[key]; ok {
			t.Fatalf("polishing context must not load working_memory.%s", key)
		}
	}
	for _, key := range []string{"premise", "world_rules", "nearby_outline", "arc_outline_compact", "references", "rewrite_brief"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("polishing context must not include top-level planning/duplicate field %s", key)
		}
	}
}

func TestContextToolUsesSourceBoundedRecoveryPackageForStoredDraft(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("recovery", 4); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.StartChapter(2); err != nil {
		t.Fatalf("StartChapter: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "previous", CoreEvent: "establish continuity"},
		{Chapter: 2, Title: "current", CoreEvent: "make the irreversible choice", Hook: "new evidence arrives"},
		{Chapter: 3, Title: "future", CoreEvent: "pay the cost"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 2,
		Title:   "current",
		Goal:    strings.Repeat("drafting rationale ", 300),
		Contract: domain.ChapterContract{
			RequiredBeats:    []string{"make the irreversible choice"},
			ContinuityChecks: []string{"preserve the prior consequence"},
			HookGoal:         "end on new evidence",
		},
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(1, strings.Repeat("previous chapter prose ", 500)); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, strings.Repeat("stored current draft prose ", 700)); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	longItems := []string{strings.Repeat("simulation guidance ", 20)}
	if err := s.Simulation.Save(domain.SimulationProfile{
		Version: domain.SimulationProfileVersion,
		Synthesis: domain.SimulationSynthesis{
			Style:        domain.SimulationStyle{NarrativeVoice: longItems, SentenceRhythm: longItems},
			RoleGuidance: domain.SimulationRoleGuidance{Writer: longItems},
		},
	}); err != nil {
		t.Fatalf("Simulation.Save: %v", err)
	}
	refs := References{
		Consistency:      strings.Repeat("consistency rule ", 2_000),
		QualityChecklist: strings.Repeat("quality rule ", 2_000),
		AntiAITone:       strings.Repeat("anti ai rule ", 3_000),
	}
	raw, err := NewContextToolWithOptions(s, refs, "default", ContextToolOptions{SimulationMode: "reinforced"}).Execute(t.Context(), json.RawMessage(`{"chapter":2}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerRecoveryContextBytes {
		t.Fatalf("recovery context=%d bytes, want <=%d beside the full draft", len(raw), writerRecoveryContextBytes)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["context_profile"] != "recovering" {
		t.Fatalf("context_profile=%v, want recovering", payload["context_profile"])
	}
	working, _ := payload["working_memory"].(map[string]any)
	for _, key := range []string{"current_chapter_outline", "chapter_contract", "chapter_draft", "user_rules"} {
		if _, ok := working[key]; !ok {
			t.Fatalf("recovery context missing working_memory.%s", key)
		}
	}
	for _, key := range []string{"chapter_plan", "future_chapter_promises", "recent_summaries", "previous_tail", "simulation_profile"} {
		if _, ok := working[key]; ok {
			t.Fatalf("recovery context must not reload drafting-only working_memory.%s", key)
		}
	}
	if _, ok := payload["simulation_profile"]; ok {
		t.Fatal("recovery context must not place simulation material beside the stored draft")
	}
}

func TestContextToolDoesNotInjectUserDirectives(t *testing.T) {
	// save_directive 已移除：novel_context 不再注入 working_memory.user_directives，
	// 长期写作要求统一走 user_rules。锁死这条，防止回归。
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	for name, chapter := range map[string]int{"writer": 1, "architect": 0} {
		args, _ := json.Marshal(map[string]any{"chapter": chapter})
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("[%s] Execute: %v", name, err)
		}
		var payload map[string]any
		if err := json.Unmarshal(result, &payload); err != nil {
			t.Fatalf("[%s] Unmarshal: %v", name, err)
		}
		working, ok := payload["working_memory"].(map[string]any)
		if !ok {
			t.Fatalf("[%s] missing working_memory", name)
		}
		if _, exists := working["user_directives"]; exists {
			t.Errorf("[%s] working_memory 不应再有 user_directives（已统一到 user_rules）", name)
		}
		// user_rules 仍应稳定注入
		if _, ok := working["user_rules"].(map[string]any); !ok {
			t.Errorf("[%s] working_memory.user_rules 应稳定注入", name)
		}
	}
}

func TestContextToolLongChapterUsesWindowedOutline(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(strings.Repeat("large premise ", 8000)); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline(testOutlineEntries(1567)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("long", 1567); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger(testForeshadowEntries(160)); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerChapterSourceBudgetBytes {
		t.Fatalf("long chapter context = %d bytes, want <= %d", len(raw), writerChapterSourceBudgetBytes)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := payload["outline"]; ok {
		t.Fatal("long chapter context must not include full outline")
	}
	working, _ := payload["working_memory"].(map[string]any)
	if _, ok := working["current_chapter_outline"]; !ok {
		t.Fatal("expected working_memory.current_chapter_outline")
	}
	for _, planningKey := range []string{"nearby_outline", "arc_outline_compact", "outline_scope", "premise", "world_rules"} {
		if _, ok := payload[planningKey]; ok {
			t.Fatalf("writer chapter context must not include architect planning field %q", planningKey)
		}
	}
	if _, ok := payload["_trimmed"]; ok {
		t.Fatalf("long chapter context should be source-bounded without hard trimming, got %v", payload["_trimmed"])
	}
}

func TestContextToolChapterOneBoundsProductionSizedMultibyteReferences(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(strings.Repeat("婚姻博弈与商业危机。", 300)); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter:      1,
		Title:        "开局",
		CoreEvent:    strings.Repeat("林舒然在监控中醒来并寻找脱身机会。", 80),
		Hook:         "苏瑾琛发现异常。",
		Scenes:       []string{"病房对峙", "第一次试探", "留下后续承诺"},
		CharacterIDs: []string{"lin_shuran", "su_jinchen"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("long", 55); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{
		{ID: "lin_shuran", Name: "林舒然", Role: "主角"},
		{ID: "su_jinchen", Name: "苏瑾琛", Role: "男主"},
	}); err != nil {
		t.Fatalf("Save characters: %v", err)
	}
	large := strings.Repeat("写作方法必须服务于当前章节事实与人物选择。", 700)
	refs := References{
		ChapterGuide:     large,
		HookTechniques:   large,
		QualityChecklist: large,
		ChapterTemplate:  large,
		Consistency:      large,
		ContentExpansion: large,
		DialogueWriting:  large,
		StyleReference:   large,
		AntiAITone:       large,
	}

	raw, err := NewContextTool(s, refs, "default").Execute(t.Context(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerChapterSourceBudgetBytes {
		t.Fatalf("chapter-one production reference context = %d bytes, want <= %d", len(raw), writerChapterSourceBudgetBytes)
	}
	var payload struct {
		Working    map[string]json.RawMessage `json:"working_memory"`
		Episodic   map[string]json.RawMessage `json:"episodic_memory"`
		References map[string]json.RawMessage `json:"reference_pack"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload.Working["current_chapter_outline"]; !ok {
		t.Fatal("bounded context lost the authoritative current chapter outline")
	}
	var userRules map[string]json.RawMessage
	if err := json.Unmarshal(payload.Working["user_rules"], &userRules); err != nil {
		t.Fatalf("decode bounded user rules: %v", err)
	}
	if _, ok := userRules["structured"]; !ok {
		t.Fatal("bounded context lost mechanical user rules")
	}
	if _, ok := userRules["preferences"]; ok {
		t.Fatal("bounded context replayed raw startup preferences already owned by the chapter contract")
	}
	if _, ok := payload.Episodic["character_workset"]; !ok {
		t.Fatal("bounded context lost the canonical character workset")
	}
	if _, ok := payload.Episodic["characters"]; ok {
		t.Fatal("bounded context duplicated full character cards beside character_workset")
	}
	if len(payload.References) == 0 {
		t.Fatal("bounded context removed all quality guidance")
	}
}

func TestContextToolExposesOnlyNearbyFutureChapterPromises(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "已发生", CoreEvent: "主角发现密信"},
		{Chapter: 2, Title: "当前章", CoreEvent: "主角追查落款"},
		{Chapter: 3, Title: "后续揭示", CoreEvent: "盟友公开承认背叛", Hook: "真正主谋现身"},
		{Chapter: 4, Title: "后果", CoreEvent: "队伍因背叛分裂"},
		{Chapter: 5, Title: "追击", CoreEvent: "主角追上主谋"},
		{Chapter: 6, Title: "窗口外", CoreEvent: "不应注入"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init("test", 6); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}

	raw, err := NewContextTool(s, References{}, "default").Execute(context.Background(), json.RawMessage(`{"chapter":2}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	working, ok := payload["working_memory"].(map[string]any)
	if !ok {
		t.Fatalf("working_memory = %#v", payload["working_memory"])
	}
	promises, ok := working["future_chapter_promises"].([]any)
	if !ok || len(promises) != futureChapterPromiseWindow {
		t.Fatalf("future_chapter_promises = %#v, want %d nearby chapters", working["future_chapter_promises"], futureChapterPromiseWindow)
	}
	first := promises[0].(map[string]any)
	if first["chapter"] != float64(3) || first["core_event"] != "盟友公开承认背叛" {
		t.Fatalf("first future promise = %#v", first)
	}
	promiseJSON, _ := json.Marshal(promises)
	if strings.Contains(string(promiseJSON), "不应注入") {
		t.Fatal("future promise window must not include distant outline entries")
	}
}

func TestContextToolLongChapterDoesNotGrowWithProgressHistory(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(strings.Repeat("large premise section ", 5000)); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	outline := testOutlineEntries(117)
	if err := s.Outline.SaveOutline(outline); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "Long Volume",
		Theme: strings.Repeat("volume theme ", 100),
		Arcs: []domain.ArcOutline{{
			Index:    1,
			Title:    "Long Arc",
			Goal:     strings.Repeat("arc goal ", 100),
			Chapters: outline,
		}},
	}}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	for ch := 1; ch <= 100; ch++ {
		body := fmt.Sprintf("# Chapter %d\n%s", ch, strings.Repeat("正文段落。", 80))
		if err := s.Drafts.SaveFinalChapter(ch, body); err != nil {
			t.Fatalf("SaveFinalChapter %d: %v", ch, err)
		}
		if err := s.Summaries.SaveSummary(domain.ChapterSummary{
			Chapter:    ch,
			Summary:    strings.Repeat(fmt.Sprintf("summary %d ", ch), 80),
			Characters: []string{"A", "B", "C"},
			KeyEvents:  []string{strings.Repeat("event ", 60)},
		}); err != nil {
			t.Fatalf("SaveSummary %d: %v", ch, err)
		}
	}
	completed := make([]int, 100)
	strands := make([]string, 100)
	hooks := make([]string, 100)
	for i := 0; i < 100; i++ {
		completed[i] = i + 1
		strands[i] = fmt.Sprintf("strand-%03d", i+1)
		hooks[i] = fmt.Sprintf("hook-%03d", i+1)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "long-progress",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		TotalChapters:     117,
		CurrentChapter:    101,
		InProgressChapter: 101,
		CompletedChapters: completed,
		StrandHistory:     strands,
		HookHistory:       hooks,
		Layered:           true,
		CurrentVolume:     1,
		CurrentArc:        1,
		ChapterWordCounts: map[int]int{100: 3200},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger(testForeshadowEntries(240)); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}
	if err := s.World.SaveRelationships(testRelationshipEntries(160)); err != nil {
		t.Fatalf("SaveRelationships: %v", err)
	}
	if err := s.World.AppendStateChanges(testStateChanges(160)); err != nil {
		t.Fatalf("AppendStateChanges: %v", err)
	}
	if err := s.World.SaveWorldRules(testWorldRules(120)); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":101}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerChapterSourceBudgetBytes {
		t.Fatalf("chapter context after long progress = %d bytes, want <= %d", len(raw), writerChapterSourceBudgetBytes)
	}

	var payload struct {
		Trimmed  any            `json:"_trimmed"`
		Working  map[string]any `json:"working_memory"`
		Episodic struct {
			Foreshadow    []domain.ForeshadowEntry   `json:"foreshadow_ledger"`
			Relationships []domain.RelationshipEntry `json:"relationship_state"`
			StateChanges  []domain.StateChange       `json:"recent_state_changes"`
		} `json:"episodic_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Trimmed != nil {
		t.Fatalf("context should not rely on hard trimming, got %v", payload.Trimmed)
	}
	if _, ok := payload.Working["checkpoint"]; ok {
		t.Fatal("writer chapter context must source progress from the signed chapter contract, not full progress history")
	}
	if len(payload.Episodic.Foreshadow) > 12 {
		t.Fatalf("foreshadow ledger length = %d", len(payload.Episodic.Foreshadow))
	}
	if len(payload.Episodic.Relationships) > 12 {
		t.Fatalf("relationships length = %d", len(payload.Episodic.Relationships))
	}
	if len(payload.Episodic.StateChanges) > 12 {
		t.Fatalf("state changes length = %d", len(payload.Episodic.StateChanges))
	}
}

func TestContextToolOutlineRangeScopeReturnsRequestedChapters(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline(testOutlineEntries(100)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"outline_range","from":20,"to":30}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Outline []domain.OutlineEntry `json:"outline"`
		Scope   struct {
			Mode             string `json:"mode"`
			From             int    `json:"from"`
			To               int    `json:"to"`
			ReturnedChapters int    `json:"returned_chapters"`
			TotalChapters    int    `json:"total_chapters"`
		} `json:"outline_scope"`
		Working map[string]any `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Outline) != 11 {
		t.Fatalf("outline range length = %d, want 11", len(payload.Outline))
	}
	if payload.Outline[0].Chapter != 20 || payload.Outline[len(payload.Outline)-1].Chapter != 30 {
		t.Fatalf("unexpected chapter range: %+v", payload.Outline)
	}
	if payload.Scope.Mode != "outline_range" || payload.Scope.From != 20 || payload.Scope.To != 30 ||
		payload.Scope.ReturnedChapters != 11 || payload.Scope.TotalChapters != 100 {
		t.Fatalf("unexpected outline_scope: %+v", payload.Scope)
	}
	if payload.Working != nil {
		t.Fatalf("outline_range should not include working_memory, got %+v", payload.Working)
	}
}

func TestContextToolSummaryScopeReturnsCompactEvidenceAndMissingChapters(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, summary := range []domain.ChapterSummary{
		{Chapter: 1, Summary: "发现天才", Characters: []string{"甲"}, KeyEvents: []string{"相遇"}},
		{Chapter: 3, Summary: "确认目标", Characters: []string{"甲", "乙"}, KeyEvents: []string{"结盟"}},
	} {
		if err := s.Summaries.SaveSummary(summary); err != nil {
			t.Fatalf("SaveSummary: %v", err)
		}
	}
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 3, Scope: "arc", Verdict: "accept", Summary: "弧评审"}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if err := s.World.SaveTimeline([]domain.TimelineEvent{{Chapter: 2, Event: "转折"}, {Chapter: 8, Event: "范围外"}}); err != nil {
		t.Fatalf("SaveTimeline: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"summary","from":1,"to":3}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Summaries []domain.ChapterSummary `json:"chapter_summaries"`
		Review    *domain.ReviewEntry     `json:"arc_review"`
		Timeline  []domain.TimelineEvent  `json:"timeline"`
		Evidence  struct {
			Complete bool  `json:"complete"`
			Missing  []int `json:"missing_summary_chapters"`
		} `json:"summary_evidence"`
		Working map[string]any `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Summaries) != 2 || payload.Review == nil || payload.Review.Scope != "arc" {
		t.Fatalf("unexpected summary evidence: summaries=%+v review=%+v", payload.Summaries, payload.Review)
	}
	if payload.Evidence.Complete || len(payload.Evidence.Missing) != 1 || payload.Evidence.Missing[0] != 2 {
		t.Fatalf("unexpected completeness: %+v", payload.Evidence)
	}
	if len(payload.Timeline) != 1 || payload.Timeline[0].Chapter != 2 {
		t.Fatalf("timeline was not range-filtered: %+v", payload.Timeline)
	}
	if payload.Working != nil {
		t.Fatalf("summary scope should not include the full writing context: %+v", payload.Working)
	}
}

func TestContextToolSummaryScopeReturnsVolumeArcEvidence(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	volumes := []domain.VolumeOutline{{Index: 1, Arcs: []domain.ArcOutline{{Index: 1}, {Index: 2}}}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "第一弧"}); err != nil {
		t.Fatalf("SaveArcSummary: %v", err)
	}

	tool := NewContextTool(s, References{}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"summary","volume":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		ArcSummaries []domain.ArcSummary `json:"arc_summaries"`
		Evidence     struct {
			Complete    bool  `json:"complete"`
			MissingArcs []int `json:"missing_arcs"`
		} `json:"summary_evidence"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.ArcSummaries) != 1 || payload.Evidence.Complete || len(payload.Evidence.MissingArcs) != 1 || payload.Evidence.MissingArcs[0] != 2 {
		t.Fatalf("unexpected volume evidence: %+v", payload)
	}
}

func TestContextToolAdaptationChapterContextIsSourceBounded(t *testing.T) {
	sourceRefs := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	plan := domain.AdaptationPlan{
		Granularity:       domain.AdaptationGranularityArc,
		RewritePolicy:     domain.AdaptationRewriteFullRewrite,
		Status:            domain.AdaptationPlanStatusConfirmed,
		Brief:             strings.Repeat("adaptation brief ", 4000),
		MainlineRules:     []string{strings.Repeat("mainline rule ", 500)},
		RelationshipGoals: []string{strings.Repeat("relationship goal ", 500)},
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:         7,
			Title:           "目标章",
			SourceChapters:  sourceRefs,
			SourceRunes:     40000,
			TargetRunes:     4200,
			TargetMinRunes:  3600,
			TargetMaxRunes:  5000,
			CoverageNote:    strings.Repeat("coverage ", 500),
			PreserveEvents:  []string{strings.Repeat("preserve ", 500)},
			RequiredChanges: []string{strings.Repeat("required ", 500)},
			ForbiddenMoves:  []string{strings.Repeat("forbidden ", 500)},
		}},
	}
	sourceTexts := make([]string, 12)
	for i := range sourceTexts {
		sourceTexts[i] = strings.Repeat("source prose ", 1000)
	}
	adaptStore := newAdaptationToolStoreWithPlan(t, plan, sourceTexts)
	if err := adaptStore.Outline.SaveOutline(testOutlineEntries(117)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := adaptStore.Progress.Save(&domain.Progress{
		NovelName:         "adapt-long",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		TotalChapters:     117,
		CurrentChapter:    7,
		InProgressChapter: 7,
		CompletedChapters: []int{1, 2, 3, 4, 5, 6},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := adaptStore.Adaptation.SaveSourceReports(testAdaptationSourceReports(24)); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}

	tool := NewContextTool(adaptStore, References{
		AdaptationWriter:            strings.Repeat("writer guidance ", 500),
		AdaptationEditorFullRewrite: strings.Repeat("editor guidance ", 500),
	}, "default")
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":7}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > writerChapterContextBudgetBytes {
		t.Fatalf("adaptation context = %d bytes, want <= %d", len(raw), writerChapterContextBudgetBytes)
	}

	var payload struct {
		Trimmed any `json:"_trimmed"`
		Working struct {
			Adaptation struct {
				Brief string `json:"brief"`
			} `json:"adaptation"`
			Reports  []domain.AdaptationSourceReport `json:"source_ref_reports"`
			Contract domain.AdaptationChapterPlan    `json:"adaptation_contract"`
		} `json:"working_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Trimmed != nil {
		t.Fatalf("adaptation context should not rely on hard trimming, got %v", payload.Trimmed)
	}
	if len([]rune(payload.Working.Adaptation.Brief)) > maxContextAdaptationBriefRunes+3 {
		t.Fatalf("adaptation brief was not compacted")
	}
	if len(payload.Working.Reports) > maxContextSourceReports {
		t.Fatalf("source reports length = %d", len(payload.Working.Reports))
	}
	for _, report := range payload.Working.Reports {
		if len([]rune(report.Summary)) > maxContextSourceReportSummaryRunes+3 {
			t.Fatalf("source report summary not compacted: %d", len([]rune(report.Summary)))
		}
		if len(report.KeyEvents) > 8 || len(report.CharacterFacts) > 8 {
			t.Fatalf("source report lists not compacted: %+v", report)
		}
	}
	if len(payload.Working.Contract.RequiredChanges) > maxContextContractItems ||
		len([]rune(payload.Working.Contract.CoverageNote)) > maxContextChapterPlanTextRunes+3 {
		t.Fatalf("adaptation contract not compacted: %+v", payload.Working.Contract)
	}
}

func testOutlineEntries(count int) []domain.OutlineEntry {
	entries := make([]domain.OutlineEntry, 0, count)
	for chapter := 1; chapter <= count; chapter++ {
		entries = append(entries, domain.OutlineEntry{
			Chapter:   chapter,
			Title:     fmt.Sprintf("Chapter %04d", chapter),
			CoreEvent: fmt.Sprintf("event %04d", chapter),
			Hook:      fmt.Sprintf("hook %04d", chapter),
			Scenes:    []string{fmt.Sprintf("scene %04d", chapter)},
		})
	}
	return entries
}

func testForeshadowEntries(count int) []domain.ForeshadowEntry {
	entries := make([]domain.ForeshadowEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, domain.ForeshadowEntry{
			ID:          fmt.Sprintf("thread_%04d", i),
			Description: strings.Repeat(fmt.Sprintf("unrelated thread %04d ", i), 20),
			PlantedAt:   1,
			Status:      "planted",
		})
	}
	return entries
}

func testRelationshipEntries(count int) []domain.RelationshipEntry {
	entries := make([]domain.RelationshipEntry, 0, count)
	for i := 1; i <= count; i++ {
		entries = append(entries, domain.RelationshipEntry{
			CharacterA: "A",
			CharacterB: fmt.Sprintf("B%03d", i),
			Relation:   strings.Repeat(fmt.Sprintf("relationship %03d ", i), 30),
			Chapter:    i,
		})
	}
	return entries
}

func testStateChanges(count int) []domain.StateChange {
	changes := make([]domain.StateChange, 0, count)
	for i := 1; i <= count; i++ {
		changes = append(changes, domain.StateChange{
			Chapter:  max(1, i-60),
			Entity:   fmt.Sprintf("entity-%03d", i),
			Field:    "status",
			OldValue: strings.Repeat("old ", 40),
			NewValue: strings.Repeat("new ", 40),
			Reason:   strings.Repeat("reason ", 40),
		})
	}
	return changes
}

func testWorldRules(count int) []domain.WorldRule {
	rules := make([]domain.WorldRule, 0, count)
	for i := 1; i <= count; i++ {
		rules = append(rules, domain.WorldRule{
			Category: "rule",
			Rule:     strings.Repeat(fmt.Sprintf("world rule %03d ", i), 30),
			Boundary: strings.Repeat("boundary ", 30),
		})
	}
	return rules
}

func TestPlanningContextIncludesPublishedCoreCastRelationships(t *testing.T) {
	st := store.NewStore(t.TempDir())
	characters := []domain.Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}}
	relationships := []domain.CharacterRelationship{{
		ID: "bond", SourceCharacterID: "lin", TargetCharacterID: "mara",
		Type: domain.RelationshipTypeAlly, Direction: domain.RelationshipDirectionMutual, Status: domain.RelationshipStatusPlanned,
	}}
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{Characters: characters, Relationships: relationships, RelationshipsReviewed: true}, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := NewContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(`{"scope":"planning"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	foundation, ok := result["foundation_memory"].(map[string]any)
	if !ok {
		t.Fatalf("foundation memory missing: %s", raw)
	}
	if got, ok := foundation["characters"].([]any); !ok || len(got) != 2 {
		t.Fatalf("published core characters missing: %#v", foundation["characters"])
	}
	if got, ok := foundation["planned_relationships"].([]any); !ok || len(got) != 1 {
		t.Fatalf("published planned relationships missing: %#v", foundation["planned_relationships"])
	}
}

func testAdaptationSourceReports(count int) []domain.AdaptationSourceReport {
	reports := make([]domain.AdaptationSourceReport, 0, count)
	for i := 1; i <= count; i++ {
		reports = append(reports, domain.AdaptationSourceReport{
			Chapter:        i,
			Title:          fmt.Sprintf("Source %02d", i),
			Summary:        strings.Repeat(fmt.Sprintf("summary %02d ", i), 200),
			Characters:     []string{"A", "B", "C"},
			CharacterFacts: []string{strings.Repeat("fact ", 200)},
			KeyEvents:      []string{strings.Repeat("event ", 200)},
			WorldRules:     []string{strings.Repeat("world ", 200)},
			Timeline: []domain.TimelineEvent{{
				Chapter:    i,
				Time:       strings.Repeat("time ", 50),
				Event:      strings.Repeat("timeline ", 200),
				Characters: []string{"A", "B"},
			}},
			Foreshadow: []domain.ForeshadowUpdate{{
				ID:          fmt.Sprintf("f%02d", i),
				Action:      "plant",
				Description: strings.Repeat("foreshadow ", 200),
			}},
			Relationships: []domain.RelationshipEntry{{
				CharacterA: "A",
				CharacterB: "B",
				Relation:   strings.Repeat("relation ", 200),
				Chapter:    i,
			}},
			StateChanges: []domain.StateChange{{
				Chapter:  i,
				Entity:   "A",
				Field:    "status",
				NewValue: strings.Repeat("new ", 200),
				Reason:   strings.Repeat("reason ", 200),
			}},
		})
	}
	return reports
}
