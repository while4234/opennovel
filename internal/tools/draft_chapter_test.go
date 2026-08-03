package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestDraftChapterRejectsUnfinishedPendingRewrite(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 80); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	for ch := 1; ch <= 58; ch++ {
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}

	p, _ := s.Progress.Load()
	p.Flow = domain.FlowPolishing
	p.PendingRewrites = []int{65}
	if err := s.Progress.Save(p); err != nil {
		t.Fatalf("Save corrupt progress: %v", err)
	}

	tool := NewDraftChapterTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 65,
		"content": "错误写入未来章节。",
		"mode":    "write",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "pending_rewrites 只能包含已完成章节") {
		t.Fatalf("expected invalid pending_rewrites rejection, got %v", err)
	}
	progress, _ := s.Progress.Load()
	if progress.InProgressChapter == 65 {
		t.Fatalf("future chapter should not become in progress")
	}
}

func TestDraftChapterAdaptationNextStepRequiresCurrentDraftCheck(t *testing.T) {
	s := newAdaptationToolStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "目标章", CoreEvent: "主线事件"}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	raw, err := NewDraftChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1,"content":"改编后的完整正文。","mode":"write"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	next, _ := payload["next_step"].(string)
	for _, want := range []string{"check_consistency", "check_adaptation", "任何后续修改"} {
		if !strings.Contains(next, want) {
			t.Fatalf("next_step missing %q: %q", want, next)
		}
	}
}

func TestDraftChapterRejectsUnexpandedLayeredChapter(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 5); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "第一卷",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "第一弧",
			Chapters: []domain.OutlineEntry{
				{Chapter: 1, Title: "一"},
				{Chapter: 2, Title: "二"},
			},
		}, {
			Index:             2,
			Title:             "第二弧",
			EstimatedChapters: 3,
		}},
	}}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatalf("SetLayered: %v", err)
	}

	tool := NewDraftChapterTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"content": "越界正文。",
		"mode":    "write",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "expand_arc") {
		t.Fatalf("expected unexpanded chapter rejection, got %v", err)
	}
	progress, _ := s.Progress.Load()
	if progress.InProgressChapter == 3 {
		t.Fatalf("unexpanded chapter should not become in progress")
	}
}

func TestDraftChapterRejectsRepeatedLongSentences(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}

	repeated := "雨水敲打废弃通风管，远处霓虹在积水里闪烁，空气里混着金属和臭氧的气味。"
	content := repeated + repeated + repeated + repeated + "他拔出接口线，强迫自己继续向节点深处移动。"
	args, err := json.Marshal(map[string]any{
		"chapter": 1,
		"content": content,
		"mode":    "write",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	raw, err := NewDraftChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result["repeated_draft_rejected"] != true || result["written"] != false {
		t.Fatalf("expected repeated prose rejection result, got %+v", result)
	}
	if draft, _ := s.Drafts.LoadDraft(1); draft != "" {
		t.Fatalf("repeated draft should not be saved, got %q", draft)
	}
}

func TestDraftChapterReportsNormalWordBudget(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	budget := domain.NewWordBudget(5000, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	raw, err := NewDraftChapterTool(s).Execute(context.Background(), mustJSON(t, map[string]any{
		"chapter": 1,
		"content": "太短的正文。",
		"mode":    "write",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result["runaway_safety_passed"] != false {
		t.Fatalf("expected word budget failure, got %+v", result)
	}
	if result["deferred_to_host"] != true {
		t.Fatalf("out-of-budget draft must return to Host before validation, got %+v", result)
	}
	if _, ok := result["word_budget"]; !ok {
		t.Fatalf("expected word_budget payload, got %+v", result)
	}
	next := result["next_step"].(string)
	for _, want := range []string{"当前草稿已经保存", "立即结束本轮", "Host 会按行段逐段派发", "不要调用 read_chapter"} {
		if !strings.Contains(next, want) {
			t.Fatalf("next_step missing %q: %q", want, next)
		}
	}
	if strings.Contains(next, "整章重写到") {
		t.Fatalf("next_step must not force another whole rewrite: %q", next)
	}
	if strings.Contains(next, "edit_chapter(edits=[...])") {
		t.Fatalf("new draft must return to Host before segmented edits: %q", next)
	}
}

func TestDraftChapterKeepsQualityDraftAboveSoftRecommendation(t *testing.T) {
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

	result := map[string]any{}
	NewDraftChapterTool(s).addNormalWordBudgetStatus(result, 1, 4165)

	if result["runaway_safety_passed"] != true || result["word_budget_recommended"] != false {
		t.Fatalf("soft recommendation became a hard rejection: %+v", result)
	}
	if result["deferred_to_host"] == true {
		t.Fatalf("quality draft within 3000-6000 must not enter trimming recovery: %+v", result)
	}
	budgetPayload := result["word_budget"].(map[string]any)
	if budgetPayload["safety_min_words"] != 2000 || budgetPayload["safety_max_words"] != 7500 ||
		budgetPayload["recommended_min_words"] != 3273 || budgetPayload["recommended_max_words"] != 4000 {
		t.Fatalf("unexpected hard/soft ranges: %+v", budgetPayload)
	}
}

func TestDraftChapterDefersOverwriteOfCurrentOutOfBudgetDraft(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	existing := strings.Repeat("原", 180)
	if err := s.Drafts.SaveDraft(1, existing); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	raw, err := NewDraftChapterTool(s).Execute(context.Background(), mustJSON(t, map[string]any{
		"chapter": 1,
		"content": "模型试图覆盖现有草稿。",
		"mode":    "write",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Written        bool `json:"written"`
		DeferredToHost bool `json:"deferred_to_host"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Written || !payload.DeferredToHost {
		t.Fatalf("out-of-budget overwrite was not deferred: %+v err=%v raw=%s", payload, err, raw)
	}
	if got, loadErr := s.Drafts.LoadDraft(1); loadErr != nil || got != existing {
		t.Fatalf("deferred draft write changed content: err=%v", loadErr)
	}
}

func TestDraftChapterReplacesSeverelyOversizedDraftOnlyWithValidCandidate(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	existing := strings.Repeat("原", 180)
	if err := s.Drafts.SaveDraft(1, existing); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	rejected, err := NewDraftChapterTool(s).Execute(context.Background(), mustJSON(t, map[string]any{
		"chapter":               1,
		"content":               strings.Repeat("短", 10),
		"mode":                  "write",
		"replace_out_of_budget": true,
	}))
	if err != nil {
		t.Fatalf("reject invalid candidate: %v", err)
	}
	var rejection struct {
		CandidateRejected bool `json:"candidate_rejected"`
	}
	if err := json.Unmarshal(rejected, &rejection); err != nil || !rejection.CandidateRejected {
		t.Fatalf("invalid regeneration candidate was not rejected: %s err=%v", rejected, err)
	}
	if got, _ := s.Drafts.LoadDraft(1); got != existing {
		t.Fatalf("invalid candidate replaced the durable draft: %q", got)
	}

	candidate := strings.Repeat("新", 100)
	raw, err := NewDraftChapterTool(s).Execute(context.Background(), mustJSON(t, map[string]any{
		"chapter":               1,
		"content":               candidate,
		"mode":                  "write",
		"replace_out_of_budget": true,
	}))
	if err != nil {
		t.Fatalf("replace with valid candidate: %v", err)
	}
	var result struct {
		Written             bool   `json:"written"`
		ReplacedOutOfBudget bool   `json:"replaced_out_of_budget"`
		RecoveryBackup      string `json:"recovery_backup"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.Written || !result.ReplacedOutOfBudget || result.RecoveryBackup == "" {
		t.Fatalf("valid candidate did not safely replace draft: %+v err=%v raw=%s", result, err, raw)
	}
	if got, _ := s.Drafts.LoadDraft(1); got != candidate {
		t.Fatalf("valid candidate was not saved: %q", got)
	}
	backup, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(result.RecoveryBackup)))
	if err != nil || string(backup) != existing {
		t.Fatalf("recovery backup mismatch: err=%v content=%q", err, backup)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return raw
}
