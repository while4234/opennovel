package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCommitChapterSchemaDescribesFeedbackAsObject(t *testing.T) {
	tool := NewCommitChapterTool(store.NewStore(testStoreDir(t)))
	schema := tool.Schema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema["properties"])
	}
	feedback, ok := props["feedback"].(map[string]any)
	if !ok {
		t.Fatalf("feedback schema missing: %#v", props["feedback"])
	}
	desc, _ := feedback["description"].(string)
	if !strings.Contains(desc, "JSON object") || !strings.Contains(desc, "字符串化 JSON") {
		t.Fatalf("feedback description should warn against stringified JSON, got %q", desc)
	}
	if got := feedback["type"]; got != "object" {
		t.Fatalf("feedback type = %v, want object", got)
	}
}

func TestCommitChapterRejectsWordBudgetOutOfRange(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 5); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	budget := domain.NewWordBudget(5000, "test").WithPlannedChapters(5)
	if err := store.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	if err := store.Drafts.SaveDraft(1, strings.Repeat("短", 100)); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewCommitChapterTool(store)
	args := commitChapterArgs(t, 1)
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute should return structured word budget rejection, got error %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["word_budget_rejected"] != true || result["committed"] != false {
		t.Fatalf("expected word budget rejection result, got %v", result)
	}
	next, _ := result["next_step"].(string)
	for _, want := range []string{"当前完整草稿已经保留", "立即结束本轮", "Host 按行段逐段派发", "完整质量校验"} {
		if !strings.Contains(next, want) {
			t.Fatalf("normal creation next_step missing %q: %q", want, next)
		}
	}
	if strings.Contains(next, `请先调用 draft_chapter`) || strings.Contains(next, `draft_chapter(mode="write"`) {
		t.Fatalf("normal creation next_step must not force a whole rewrite: %q", next)
	}
	if next, _ := result["next_step"].(string); !strings.Contains(next, "不要再次调用 commit_chapter") {
		t.Fatalf("next_step should steer rewrite, got %q", next)
	}
	if _, statErr := os.Stat(dir + "/chapters/01.md"); !os.IsNotExist(statErr) {
		t.Fatalf("chapter should not be persisted, stat err=%v", statErr)
	}
}

func TestCommitChapterAllowsWordBudgetInRange(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 5); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	budget := domain.NewWordBudget(5000, "test").WithPlannedChapters(5)
	if err := store.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	if err := store.Drafts.SaveDraft(1, strings.Repeat("正", 900)); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewCommitChapterTool(store)
	if _, err := tool.Execute(context.Background(), commitChapterArgs(t, 1)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress == nil || progress.TotalWordCount != 900 || len(progress.CompletedChapters) != 1 {
		t.Fatalf("unexpected progress: %+v", progress)
	}
}

func TestCommitChapterGateAllowsSoftRecommendationOverage(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 55); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	budget := domain.NewWordBudget(200000, "test").WithPlannedChapters(55)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	saveChapterWordRange(t, s, 3000, 6000)

	rejection, err := NewCommitChapterTool(s).checkWordBudgetGate(1, 4843)
	if err != nil {
		t.Fatalf("checkWordBudgetGate: %v", err)
	}
	if rejection != nil {
		t.Fatalf("4843 words are above the recommendation but within the runaway safety range: %+v", rejection)
	}
	rejection, err = NewCommitChapterTool(s).checkWordBudgetGate(1, 7501)
	if err != nil {
		t.Fatalf("checkWordBudgetGate hard overflow: %v", err)
	}
	if rejection == nil || rejection.minWords != 2000 || rejection.maxWords != 7500 {
		t.Fatalf("runaway overflow must still be rejected: %+v", rejection)
	}
}

func commitChapterArgs(t *testing.T, chapter int) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"chapter":         chapter,
		"summary":         "测试提交",
		"characters":      []string{"主角"},
		"key_events":      []string{"推进"},
		"timeline_events": []any{},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return args
}

func TestCommitChapterRejectsNonPendingRewrite(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := store.Progress.SetPendingRewrites([]int{2}, "测试重写"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.Drafts.SaveDraft(3, "这是错误章节的正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewCommitChapterTool(store)
	args, err := json.Marshal(map[string]any{
		"chapter":         3,
		"summary":         "错误提交",
		"characters":      []string{"主角"},
		"key_events":      []string{"误提交"},
		"timeline_events": []any{},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected commit to be rejected during rewrite flow")
	}

	if _, err := os.Stat(dir + "/chapters/03.md"); !os.IsNotExist(err) {
		t.Fatalf("chapter should not be persisted, stat err=%v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if len(progress.CompletedChapters) != 1 || progress.CompletedChapters[0] != 2 {
		t.Fatalf("completed chapters should only contain original chapter 2, got %v", progress.CompletedChapters)
	}
	if progress.CurrentChapter != 3 {
		t.Fatalf("current chapter should not advance beyond original progress, got %d", progress.CurrentChapter)
	}
}

func TestCommitChapterAllowsPendingRewrite(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := store.Progress.SetPendingRewrites([]int{2}, "测试重写"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := store.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := store.Drafts.SaveDraft(2, "这是正确待重写章节的正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewCommitChapterTool(store)
	args, err := json.Marshal(map[string]any{
		"chapter":         2,
		"summary":         "正确提交",
		"characters":      []string{"主角"},
		"key_events":      []string{"完成重写"},
		"timeline_events": []any{},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := os.Stat(dir + "/chapters/02.md"); err != nil {
		t.Fatalf("chapter should be persisted: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if len(progress.CompletedChapters) != 1 || progress.CompletedChapters[0] != 2 {
		t.Fatalf("unexpected completed chapters: %v", progress.CompletedChapters)
	}
	pending, err := store.Signals.LoadPendingCommit()
	if err != nil {
		t.Fatalf("LoadPendingCommit: %v", err)
	}
	if pending != nil {
		t.Fatalf("expected pending commit cleared, got %+v", pending)
	}
}

// TestCommitChapterRejectsNonAdaptationOutsideWordBudget verifies the normal
// creation word budget gate rejects drafts outside the current chapter range.
func TestCommitChapterRejectsNonAdaptationOutsideWordBudget(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	budget, _ := domain.NewWordBudgetFromTarget(10000, domain.WordBudgetSourcePrompt)
	planned := budget.WithPlannedChapters(2)
	if err := s.RunMeta.SetWordBudget(&planned); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, strings.Repeat("a", 1000)); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewCommitChapterTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter":    1,
		"summary":    "too short",
		"characters": []string{"hero"},
		"key_events": []string{"event"},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute should return structured word budget rejection, got error %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["word_budget_rejected"] != true || result["committed"] != false {
		t.Fatalf("expected word budget rejection result, got %v", result)
	}
	if _, err := os.Stat(dir + "/chapters/01.md"); !os.IsNotExist(err) {
		t.Fatalf("chapter should not be persisted, stat err=%v", err)
	}
}

func TestCommitChapterPendingPolishSkipsCreationWordBudgetGate(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 2); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	budget, _ := domain.NewWordBudgetFromTarget(10000, domain.WordBudgetSourcePrompt)
	planned := budget.WithPlannedChapters(2)
	if err := s.RunMeta.SetWordBudget(&planned); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	original := strings.Repeat("原", 1000)
	if err := s.Drafts.SaveFinalChapter(1, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, original+"改"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, len([]rune(original)), "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{1}, "局部打磨"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	raw, err := NewCommitChapterTool(s).Execute(context.Background(), commitChapterArgs(t, 1))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result["rewritten"] != true || result["queue_drained"] != true {
		t.Fatalf("pending polish was not committed: %+v", result)
	}
	if _, exists := result["word_budget_rejected"]; exists {
		t.Fatalf("pending polish returned creation budget rejection: %+v", result)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Progress.Load: %v", err)
	}
	if len(progress.PendingRewrites) != 0 || progress.Flow != domain.FlowWriting {
		t.Fatalf("pending polish queue was not completed: %+v", progress)
	}
	final, err := s.Drafts.LoadChapterText(1)
	if err != nil {
		t.Fatalf("LoadChapterText: %v", err)
	}
	if final != original+"改" {
		t.Fatalf("targeted polish was not preserved: %q", final)
	}
}

// TestCommitChapterUpdatesCastLedger 验证：commit_chapter 把本章 characters 累加进 cast_ledger，
// cast_intros 提供的 brief_role 被采用，且 characters.json 中的核心角色不进入 ledger。
func TestCommitChapterUpdatesCastLedger(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	// 设定核心角色档案（这些不应进 cast_ledger）
	if err := s.Characters.Save([]domain.Character{
		{Name: "林墨", Role: "主角", Tier: "core"},
		{Name: "李清砚", Role: "导师", Tier: "important"},
	}); err != nil {
		t.Fatalf("Save core characters: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "第一章正文，林墨遇到客栈老板老周与小厮阿云。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    1,
		"summary":    "林墨入住客栈",
		"characters": []string{"林墨", "李清砚", "老周", "阿云"},
		"key_events": []string{"入住"},
		"cast_intros": []any{
			map[string]any{"name": "老周", "brief_role": "客栈老板"},
			map[string]any{"name": "阿云", "brief_role": "客栈小厮"},
		},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	entries, err := s.Cast.Load()
	if err != nil {
		t.Fatalf("Cast.Load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 ledger entries (老周/阿云), got %d: %+v", len(entries), entries)
	}
	byName := map[string]domain.CastEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if e, ok := byName["老周"]; !ok || e.BriefRole != "客栈老板" || e.FirstSeenChapter != 1 {
		t.Errorf("老周 entry wrong: %+v", e)
	}
	if e, ok := byName["阿云"]; !ok || e.BriefRole != "客栈小厮" || e.AppearanceCount != 1 {
		t.Errorf("阿云 entry wrong: %+v", e)
	}
	if _, ok := byName["林墨"]; ok {
		t.Errorf("核心角色 林墨 不应进 ledger")
	}
	if _, ok := byName["李清砚"]; ok {
		t.Errorf("核心角色 李清砚 不应进 ledger")
	}
}

// TestCommitChapterRejectsPolishWithoutDraftChange 验证：已完成章节进入打磨/重写队列后，
// 若 writer 尚未落实打磨意见就直接 commit（drafts 与 chapters 内容完全相同），
// commit_chapter 必须结构化拒绝并引导局部 edit_chapter，不能把正常控制流
// 记成 ERROR，也不能诱发无依据的整章重写。
// TestCommitChapterNonLayeredRecompletesAfterRework 验证非分层书完本后经 reopen 返工，
// 改完章节 commit、队列排空时能自动重新回到 complete（补 drain 后判完结的非分层分支）。
func TestCommitChapterNonLayeredRecompletesAfterRework(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// 两章写完并完结。第 2 章备齐 drafts/chapters，供返工提交。
	ch2 := "第二章原始正文，用于模拟已提交终稿。"
	if err := s.Drafts.SaveDraft(2, ch2); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, ch2); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete(1): %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(ch2)), "", ""); err != nil {
		t.Fatalf("MarkChapterComplete(2): %v", err)
	}
	if err := s.Progress.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	// reopen 第 2 章 → phase 回 writing、PendingRewrites=[2]、flow=rewriting
	if err := s.Progress.Reopen([]int{2}, "返工"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}

	// 返工提交（草稿需与终稿不同才放行）
	if err := s.Drafts.SaveDraft(2, ch2+"\n\n返工新增段落。"); err != nil {
		t.Fatalf("SaveDraft (reworked): %v", err)
	}
	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"summary":    "返工后摘要",
		"characters": []string{"主角"},
		"key_events": []string{"清理"},
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute rework commit: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["book_complete"] != true {
		t.Errorf("book_complete = %v, want true", payload["book_complete"])
	}

	p, _ := s.Progress.Load()
	if p.Phase != domain.PhaseComplete {
		t.Errorf("phase = %s, want complete (应自动重新收尾)", p.Phase)
	}
	if len(p.PendingRewrites) != 0 {
		t.Errorf("PendingRewrites = %v, want empty", p.PendingRewrites)
	}
}

func TestCommitChapterRecreatesFactsWhenRepairDeletedFinal(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{1}, "repair rewrite"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "重写后的正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    1,
		"summary":    "重写后摘要",
		"characters": []string{"主角", "新配角"},
		"key_events": []string{"新事件"},
		"timeline_events": []map[string]any{{
			"time":       "深夜",
			"event":      "新时间线事件",
			"characters": []string{"主角"},
		}},
		"foreshadow_updates": []map[string]any{{
			"id":          "new-clue",
			"action":      "plant",
			"description": "新伏笔",
		}},
		"relationship_changes": []map[string]any{{
			"character_a": "主角",
			"character_b": "新配角",
			"relation":    "同盟",
		}},
		"state_changes": []map[string]any{{
			"entity":    "主角",
			"field":     "goal",
			"new_value": "追查新线索",
		}},
		"cast_intros": []map[string]any{{
			"name":       "新配角",
			"brief_role": "新线索提供者",
		}},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	timeline, err := s.World.LoadTimeline()
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}
	if len(timeline) != 1 || timeline[0].Chapter != 1 || timeline[0].Event != "新时间线事件" {
		t.Fatalf("timeline = %+v, want recreated chapter fact", timeline)
	}
	foreshadow, err := s.World.LoadForeshadowLedger()
	if err != nil {
		t.Fatalf("LoadForeshadowLedger: %v", err)
	}
	if len(foreshadow) != 1 || foreshadow[0].ID != "new-clue" || foreshadow[0].PlantedAt != 1 {
		t.Fatalf("foreshadow = %+v, want recreated clue", foreshadow)
	}
	relationships, err := s.World.LoadRelationships()
	if err != nil {
		t.Fatalf("LoadRelationships: %v", err)
	}
	if len(relationships) != 1 || relationships[0].Chapter != 1 || relationships[0].Relation != "同盟" {
		t.Fatalf("relationships = %+v, want recreated relationship", relationships)
	}
	stateChanges, err := s.World.LoadStateChanges()
	if err != nil {
		t.Fatalf("LoadStateChanges: %v", err)
	}
	if len(stateChanges) != 1 || stateChanges[0].Chapter != 1 || stateChanges[0].NewValue != "追查新线索" {
		t.Fatalf("state changes = %+v, want recreated state change", stateChanges)
	}
	castEntries, err := s.Cast.Load()
	if err != nil {
		t.Fatalf("Load cast: %v", err)
	}
	if len(castEntries) != 2 {
		t.Fatalf("cast entries = %+v, want main and side character appearances in non-core test store", castEntries)
	}
}

// TestCommitChapterLayeredReopenRecompletesDespiteOpenThread 验证收口：分层书经 reopen
// 返工后，即便 compass 仍有未收束长线（返工可能扰动），排空后也按"结构完整"重新完结——
// 不卡在 writing，杜绝终卷末越界续写死循环（§6.5 / known_outline_exhaustion 家族）。
// 反证：若 reopen 路径仍用质量级 layeredBookComplete，本例 open thread 会让其返 false、
// book_complete 为假，测试即失败。
func TestCommitChapterLayeredReopenRecompletesDespiteOpenThread(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	approveFoundationToolFixture(t, s)

	// 单卷单弧两章，全部展开
	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{
					{"title": "首章", "core_event": "起", "hook": "续"},
					{"title": "次章", "core_event": "承", "hook": "终"},
				},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}

	// 两章写完落盘并完结
	ch2 := "第二章原始正文，模拟已提交终稿。"
	for ch, body := range map[int]string{1: "第一章正文。", 2: ch2} {
		if err := s.Drafts.SaveDraft(ch, body); err != nil {
			t.Fatalf("SaveDraft %d: %v", ch, err)
		}
		if err := s.Drafts.SaveFinalChapter(ch, body); err != nil {
			t.Fatalf("SaveFinalChapter %d: %v", ch, err)
		}
		if err := s.Progress.MarkChapterComplete(ch, len([]rune(body)), "", ""); err != nil {
			t.Fatalf("MarkChapterComplete %d: %v", ch, err)
		}
	}
	if err := s.Progress.MarkComplete(); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	// 模拟"返工扰动了长线"：compass 仍有未收束的 open thread
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "主角归乡", OpenThreads: []string{"宿敌未除"}}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}

	// reopen 第 2 章 → 返工提交（草稿需与终稿不同才放行）
	if err := s.Progress.Reopen([]int{2}, "返工"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, ch2+"\n\n返工新增段落。"); err != nil {
		t.Fatalf("SaveDraft reworked: %v", err)
	}
	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter": 2, "summary": "返工摘要", "characters": []string{"主角"}, "key_events": []string{"清理"},
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute rework commit: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if bc, _ := out["book_complete"].(bool); !bc {
		t.Error("reopen 返工排空后应按结构完整重新完结（即便长线未收束）")
	}
	p, _ := s.Progress.Load()
	if p.Phase != domain.PhaseComplete {
		t.Errorf("phase = %s, want complete", p.Phase)
	}
	if p.ReopenedFromComplete {
		t.Error("重新完结后 ReopenedFromComplete 应被清除")
	}
}

func TestCommitChapterRejectsPolishWithoutDraftChange(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// 模拟第 2 章已正常完成：drafts 与 chapters 内容相同。
	original := "第二章原始正文内容，用于模拟已提交终稿。"
	if err := s.Drafts.SaveDraft(2, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(original)), "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	// 进入打磨队列：Flow=Polishing, PendingRewrites=[2]
	if err := s.Progress.SetPendingRewrites([]int{2}, "测试打磨"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"summary":    "假装打磨了",
		"characters": []string{"主角"},
		"key_events": []string{"无改动"},
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unchanged polish must be a structured rejection, got %v", err)
	}
	var rejection struct {
		Committed      bool   `json:"committed"`
		UnchangedDraft bool   `json:"unchanged_draft"`
		NextStep       string `json:"next_step"`
	}
	if err := json.Unmarshal(raw, &rejection); err != nil {
		t.Fatalf("Unmarshal rejection: %v", err)
	}
	if rejection.Committed || !rejection.UnchangedDraft {
		t.Fatalf("unexpected rejection payload: %+v", rejection)
	}
	for _, want := range []string{"rewrite_brief", "edit_chapter", "局部实质改动", `read_chapter(source="draft")`, "check_consistency", "check_de_ai"} {
		if !strings.Contains(rejection.NextStep, want) {
			t.Fatalf("polish rejection missing %q: %s", want, rejection.NextStep)
		}
	}
	if strings.Contains(rejection.NextStep, "draft_chapter") {
		t.Fatalf("polish rejection must not force a whole rewrite: %s", rejection.NextStep)
	}

	// 再写一版不同的草稿 → 应该通过
	polished := original + "\n\n打磨后新增段落。"
	if err := s.Drafts.SaveDraft(2, polished); err != nil {
		t.Fatalf("SaveDraft (polished): %v", err)
	}
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute after real polish: %v", err)
	}
}

func TestCommitChapterAdaptationFinalChapterWaitsForReview(t *testing.T) {
	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "第一章", SourceChapters: []int{1}},
			{Chapter: 2, Title: "第二章", SourceChapters: []int{2}},
		},
	}
	s := newAdaptationToolStoreWithPlan(t, plan, []string{"源一", "源二"})
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{
					{"title": "第一章", "core_event": "起", "hook": "续"},
					{"title": "第二章", "core_event": "终", "hook": "终"},
				},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	tool := NewCommitChapterTool(s)
	commit := func(chapter int) map[string]any {
		draft := fmt.Sprintf("第 %d 章改编正文。", chapter)
		if err := s.Drafts.SaveDraft(chapter, draft); err != nil {
			t.Fatalf("SaveDraft %d: %v", chapter, err)
		}
		if err := s.Adaptation.SaveCheck(domain.AdaptationCheck{
			Chapter:     chapter,
			DraftSHA256: store.TextSHA256(draft),
			Passed:      true,
		}); err != nil {
			t.Fatalf("SaveCheck %d: %v", chapter, err)
		}
		args, _ := json.Marshal(map[string]any{
			"chapter":    chapter,
			"summary":    "摘要",
			"characters": []string{"主角"},
			"key_events": []string{"事件"},
		})
		raw, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("Execute ch%d: %v", chapter, err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("Unmarshal ch%d: %v", chapter, err)
		}
		return out
	}

	if bc, _ := commit(1)["book_complete"].(bool); bc {
		t.Fatal("第 1 章不应完结")
	}
	final := commit(2)
	if review, _ := final["review_required"].(bool); !review {
		t.Fatalf("final chapter should require review, got %+v", final)
	}
	if bc, _ := final["book_complete"].(bool); bc {
		t.Fatalf("final adaptation chapter must wait for editor review before completion, got %+v", final)
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseComplete {
		t.Fatal("phase must remain writing so flow router can dispatch editor")
	}
}

// TestCommitChapterLayeredRejectsOutOfRangeChapter 验证分层模式下，
// 章号越出 layered_outline 的 commit 必须硬失败，而不是 slog.Warn 放行。
// 这是阻止"裁定误判后 writer 一路裸跑"的物理刹车（《凡骨》ch204..347 案例）。
func TestCommitChapterLayeredRejectsOutOfRangeChapter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	approveFoundationToolFixture(t, s)

	// 建一份 layered_outline，只有 1 卷 1 弧 1 章
	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{
					{"title": "首章", "core_event": "起", "hook": "续"},
				},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	// 越界章节 2 的 commit 必须硬失败
	if err := s.Drafts.SaveDraft(2, "越界章节正文，必须被拦下。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"summary":    "越界章节",
		"characters": []string{"主角"},
		"key_events": []string{"不该被允许"},
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected commit to fail when chapter out of layered outline range")
	}

	// 章节文件不应落盘、Progress 不应推进
	if _, statErr := os.Stat(dir + "/chapters/02.md"); !os.IsNotExist(statErr) {
		t.Fatalf("chapter 2 should not be persisted, stat err=%v", statErr)
	}
	progress, _ := s.Progress.Load()
	if len(progress.CompletedChapters) != 0 {
		t.Fatalf("CompletedChapters should stay empty, got %v", progress.CompletedChapters)
	}
}

// TestCommitChapterLayeredWaitsForReviewWhenDone 验证分层终章不绕过弧/卷级评审：
// 即使大纲全部展开并写完、活跃伏笔为零、指南针长线收束，commit_chapter 也只暴露
// review_required 事实；后续由 Router 派 Editor 审阅/摘要，再让 Architect 调 complete_book。
func TestCommitChapterLayeredWaitsForReviewWhenDone(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	approveFoundationToolFixture(t, s)

	// 单卷单弧两章，全部展开（无骨架弧）
	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{
					{"title": "首章", "core_event": "起", "hook": "续"},
					{"title": "次章", "core_event": "承", "hook": "终"},
				},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}
	// 指南针长线已收束（OpenThreads 空）
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "主角归乡"}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	tool := NewCommitChapterTool(s)
	commit := func(ch int) map[string]any {
		if err := s.Drafts.SaveDraft(ch, fmt.Sprintf("第 %d 章正文内容，用于测试确定性完结。", ch)); err != nil {
			t.Fatalf("SaveDraft %d: %v", ch, err)
		}
		args, _ := json.Marshal(map[string]any{
			"chapter": ch, "summary": "摘要", "characters": []string{"主角"}, "key_events": []string{"事件"},
		})
		raw, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("Execute ch%d: %v", ch, err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("Unmarshal ch%d: %v", ch, err)
		}
		return out
	}

	// 第 1 章：未写完，不应完结
	if bc, _ := commit(1)["book_complete"].(bool); bc {
		t.Fatal("写完第 1 章不应触发完结")
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseComplete {
		t.Fatal("写完第 1 章 phase 不应为 complete")
	}

	// 第 2 章（最后一章）：应等待弧/卷评审，不能直接完结。
	final := commit(2)
	if review, _ := final["review_required"].(bool); !review {
		t.Fatalf("写完最后一章应触发评审，got %+v", final)
	}
	if bc, _ := final["book_complete"].(bool); bc {
		t.Fatalf("写完最后一章不应绕过评审自动完结，got %+v", final)
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseComplete {
		t.Fatal("phase 应保持 writing，等待 Router 派 Editor")
	}
}

// TestCommitChapterLayeredNoAutoCompleteWithOpenThreads 验证保守性：仍有活跃长线时
// 即使章节写满也不自动完结，把"是否继续"的裁定权留给架构师。
func TestCommitChapterLayeredNoAutoCompleteWithOpenThreads(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	approveFoundationToolFixture(t, s)

	foundation := NewSaveFoundationTool(s)
	layeredArgs, _ := json.Marshal(map[string]any{
		"type": "layered_outline",
		"content": []map[string]any{{
			"index": 1, "title": "卷一", "theme": "主题",
			"arcs": []map[string]any{{
				"index": 1, "title": "弧一", "goal": "目标",
				"chapters": []map[string]any{{"title": "首章", "core_event": "起", "hook": "续"}},
			}},
		}},
		"scale": "long",
	})
	if _, err := foundation.Execute(context.Background(), layeredArgs); err != nil {
		t.Fatalf("Execute layered: %v", err)
	}
	// 仍有未收束的活跃长线
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "主角归乡", OpenThreads: []string{"宿敌未除"}}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	_ = s.Progress.UpdatePhase(domain.PhaseWriting)

	if err := s.Drafts.SaveDraft(1, "唯一一章的正文，但长线未收束。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	tool := NewCommitChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter": 1, "summary": "摘要", "characters": []string{"主角"}, "key_events": []string{"事件"},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if p, _ := s.Progress.Load(); p.Phase == domain.PhaseComplete {
		t.Fatal("活跃长线未收束时不应自动完结")
	}
}
