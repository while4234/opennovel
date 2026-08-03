package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// TestEditChapterAppliesEdit 正常路径：drafts 已有内容，唯一匹配替换成功。
func TestEditChapterAppliesEdit(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "他握紧了拳头，指节发白。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "指节发白",
		"new_string": "指节泛起青白",
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, err := s.Drafts.LoadDraft(2)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if !strings.Contains(got, "指节泛起青白") {
		t.Fatalf("expected draft to contain new text, got %q", got)
	}
	if strings.Contains(got, "指节发白") {
		t.Fatalf("old text should be replaced, got %q", got)
	}
}

func TestEditChapterRejectsRepairThatCreatesRunawayDraft(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName: "test", Phase: domain.PhaseWriting, Flow: domain.FlowWriting,
		TotalChapters: 1, InProgressChapter: 1,
	}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	current := strings.Repeat("甲", 100)
	if err := s.Drafts.SaveDraft(1, current); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	raw, err := NewEditChapterTool(s).Execute(context.Background(), mustJSON(t, map[string]any{
		"chapter": 1, "old_string": "甲", "new_string": strings.Repeat("乙", 90),
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["runaway_edit_rejected"] != true || payload["changed"] != false {
		t.Fatalf("runaway edit was not rejected: %+v", payload)
	}
	if got, loadErr := s.Drafts.LoadDraft(1); loadErr != nil || got != current {
		t.Fatalf("rejected edit changed draft: err=%v got=%q", loadErr, got)
	}
}

func TestEditChapterBatchAppliesMultipleEditsAndReportsWordBudget(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	first := " REMOVE-ONE-1234567890 "
	second := " REMOVE-TWO-1234567890 "
	content := strings.Repeat("a", 65) + first + strings.Repeat("b", 65) + second
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter":        1,
		"budget_segment": 0,
		"edits": []map[string]string{
			{"old_string": first, "new_string": "x"},
			{"old_string": second, "new_string": "y"},
		},
	})
	raw, err := NewEditChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Changed             bool `json:"changed"`
		EditCount           int  `json:"edit_count"`
		WordCount           int  `json:"word_count"`
		RunawaySafetyPassed bool `json:"runaway_safety_passed"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.Changed || payload.EditCount != 2 || payload.WordCount != 132 || !payload.RunawaySafetyPassed {
		t.Fatalf("unexpected batch result: %+v raw=%s", payload, raw)
	}
	got, err := s.Drafts.LoadDraft(1)
	if err != nil || got != strings.Repeat("a", 65)+"x"+strings.Repeat("b", 65)+"y" {
		t.Fatalf("batch edit mismatch: got=%q err=%v", got, err)
	}
}

func TestEditChapterBudgetSegmentPersistsRecoveryCheckpoint(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, strings.Repeat("x", 170)+" keep verbose detail"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	args := json.RawMessage(`{"chapter":1,"budget_segment":0,"edits":[{"old_string":" verbose","new_string":""}]}`)
	raw, err := NewEditChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		BudgetSegment int    `json:"budget_segment"`
		NextStep      string `json:"next_step"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.BudgetSegment != 0 {
		t.Fatalf("segment result=%+v err=%v raw=%s", payload, err, raw)
	}
	latest := s.Checkpoints.Latest(domain.ChapterScope(1))
	if latest == nil || latest.Step != "word_budget_edit_segment_0" {
		t.Fatalf("segment checkpoint not persisted: %+v", latest)
	}
	if !strings.Contains(payload.NextStep, "立即结束本轮") || !strings.Contains(payload.NextStep, "Host 将派发下一行段") {
		t.Fatalf("segment must return control to Host: %+v", payload)
	}
}

func TestEditChapterBudgetSegmentDefersWrongOrOutOfRangeSegment(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(300, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	lines := make([]string, 100)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%03d-verbose", index+1)
	}
	content := strings.Join(lines, "\n")
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	for _, args := range []json.RawMessage{
		json.RawMessage(`{"chapter":1,"budget_segment":0,"edits":[{"old_string":"line-001-verbose","new_string":"line-001"}]}`),
		json.RawMessage(`{"chapter":1,"budget_segment":2,"edits":[{"old_string":"line-001-verbose","new_string":"line-001"}]}`),
	} {
		raw, err := NewEditChapterTool(s).Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		var payload struct {
			Changed        bool `json:"changed"`
			DeferredToHost bool `json:"deferred_to_host"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil || payload.Changed || !payload.DeferredToHost {
			t.Fatalf("out-of-scope segment edit was not deferred: %+v err=%v raw=%s", payload, err, raw)
		}
	}
	got, err := s.Drafts.LoadDraft(1)
	if err != nil || got != content {
		t.Fatalf("deferred segment edit changed draft: err=%v", err)
	}
}

func TestEditChapterBudgetSegmentAllowsParagraphMergeInsideHostSegment(t *testing.T) {
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
	content := strings.Repeat("x", 170) + "\n\n重复解释。\n\n继续动作。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	args := json.RawMessage(`{"chapter":1,"budget_segment":0,"edits":[{"old_string":"重复解释。\n\n继续动作。","new_string":"继续动作。"}]}`)
	raw, err := NewEditChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Changed        bool `json:"changed"`
		DeferredToHost bool `json:"deferred_to_host"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || !payload.Changed || payload.DeferredToHost {
		t.Fatalf("quality-preserving paragraph merge was rejected: %+v err=%v raw=%s", payload, err, raw)
	}
}

func TestEditChapterBudgetSegmentSkipsInvalidOldStringAndAppliesValidSubset(t *testing.T) {
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
	content := strings.Repeat("x", 170) + " 林舒然哼出一声笑。倒计时还在。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	args := json.RawMessage(`{"chapter":1,"budget_segment":0,"edits":[{"old_string":"她哼出一声笑。","new_string":"她笑了。"},{"old_string":"倒计时还在。","new_string":"计时还在。"}]}`)
	raw, err := NewEditChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Changed                bool  `json:"changed"`
		EditCount              int   `json:"edit_count"`
		SkippedBudgetEditCount int   `json:"skipped_budget_edit_count"`
		SkippedIndices         []int `json:"skipped_budget_edit_indices"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || !payload.Changed || payload.EditCount != 1 ||
		payload.SkippedBudgetEditCount != 1 || len(payload.SkippedIndices) != 1 || payload.SkippedIndices[0] != 0 {
		t.Fatalf("valid subset was not applied atomically: %+v err=%v raw=%s", payload, err, raw)
	}
	got, loadErr := s.Drafts.LoadDraft(1)
	if loadErr != nil || !strings.Contains(got, "计时还在。") || strings.Contains(got, "倒计时还在。") {
		t.Fatalf("valid subset missing from draft: got=%q err=%v", got, loadErr)
	}
}

func TestEditChapterOutOfBudgetDefersUnsegmentedEditToHost(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	content := strings.Repeat("a", 180) + "tail"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"chapter": 1, "old_string": "tail", "new_string": "x",
	})
	raw, err := NewEditChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Changed        bool   `json:"changed"`
		DeferredToHost bool   `json:"deferred_to_host"`
		WordCount      int    `json:"word_count"`
		NextStep       string `json:"next_step"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Changed || !payload.DeferredToHost || payload.WordCount != len([]rune(content)) ||
		!strings.Contains(payload.NextStep, "Host 将指定唯一行段") {
		t.Fatalf("unsegmented out-of-budget edit was not deferred: %+v", payload)
	}
	got, err := s.Drafts.LoadDraft(1)
	if err != nil || got != content {
		t.Fatalf("deferred edit changed draft: got=%q err=%v", got, err)
	}
}

func TestEditChapterBatchAllowsQualityPreservingSmallEdits(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	var content strings.Builder
	edits := make([]map[string]string, 20)
	for index := range edits {
		oldText := fmt.Sprintf("verbose-detail-%02d", index)
		newText := fmt.Sprintf("detail-%02d", index)
		content.WriteString(oldText)
		content.WriteByte('\n')
		edits[index] = map[string]string{"old_string": oldText, "new_string": newText}
	}
	if err := s.Drafts.SaveDraft(1, content.String()); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	args, _ := json.Marshal(map[string]any{"chapter": 1, "edits": edits})
	raw, err := NewEditChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		EditCount int `json:"edit_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.EditCount != 20 {
		t.Fatalf("twenty-edit batch result=%+v err=%v raw=%s", payload, err, raw)
	}
}

func TestEditChapterPendingPolishAppliesValidSubsetAndSkipsStaleItems(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const original = "第一处重复表达。第二处准确原文。"
	if err := s.Drafts.SaveDraft(3, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "test",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowPolishing,
		TotalChapters:     3,
		CompletedChapters: []int{1, 2, 3},
		InProgressChapter: 3,
		PendingRewrites:   []int{3},
	}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}

	raw, err := NewEditChapterTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":3,
		"edits":[
			{"old_string":"模型记错的旧文本","new_string":"不会落盘"},
			{"old_string":"第二处准确原文","new_string":"第二处精确改写"}
		]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Changed        bool  `json:"changed"`
		EditCount      int   `json:"edit_count"`
		SkippedCount   int   `json:"skipped_stale_edit_count"`
		SkippedIndices []int `json:"skipped_stale_edit_indices"`
		DeferredToHost bool  `json:"deferred_to_host"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.Changed || payload.EditCount != 1 || payload.SkippedCount != 1 ||
		len(payload.SkippedIndices) != 1 || payload.SkippedIndices[0] != 0 || payload.DeferredToHost {
		t.Fatalf("unexpected partial polish result: %+v raw=%s", payload, raw)
	}
	got, err := s.Drafts.LoadDraft(3)
	if err != nil || got != "第一处重复表达。第二处精确改写。" {
		t.Fatalf("valid polish subset was not applied: got=%q err=%v", got, err)
	}
}

func TestEditChapterPendingPolishDefersWhenEveryItemIsStale(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const original = "当前草稿保持不变。"
	if err := s.Drafts.SaveDraft(3, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "test",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowPolishing,
		TotalChapters:     3,
		CompletedChapters: []int{1, 2, 3},
		InProgressChapter: 3,
		PendingRewrites:   []int{3},
	}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}

	raw, err := NewEditChapterTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":3,
		"edits":[{"old_string":"不存在的旧文本","new_string":"不会落盘"}]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Changed        bool `json:"changed"`
		DeferredToHost bool `json:"deferred_to_host"`
		SkippedCount   int  `json:"skipped_stale_edit_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Changed || !payload.DeferredToHost || payload.SkippedCount != 1 {
		t.Fatalf("unexpected stale polish result: %+v raw=%s", payload, raw)
	}
	got, err := s.Drafts.LoadDraft(3)
	if err != nil || got != original {
		t.Fatalf("stale polish batch changed the draft: got=%q err=%v", got, err)
	}
}

func TestEditChapterRepeatedPatchIsIdempotent(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	const content = "订单备注里已经写明：按设计师署名，不按品牌。"
	if err := s.Drafts.SaveDraft(2, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	args := json.RawMessage(`{"chapter":2,"old_string":"加粗去掉订单备注","new_string":"订单备注里已经写明：按设计师署名，不按品牌。"}`)
	raw, err := NewEditChapterTool(s).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("repeated patch must be an idempotent no-op: %v", err)
	}
	var payload struct {
		AlreadyApplied bool   `json:"already_applied"`
		Changed        bool   `json:"changed"`
		NextStep       string `json:"next_step"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.AlreadyApplied || payload.Changed {
		t.Fatalf("unexpected idempotent payload: %+v", payload)
	}
	if !strings.Contains(payload.NextStep, "check_consistency") {
		t.Fatalf("idempotent retry lost validation guidance: %q", payload.NextStep)
	}
	got, err := s.Drafts.LoadDraft(2)
	if err != nil || got != content {
		t.Fatalf("idempotent retry changed draft: got=%q err=%v", got, err)
	}
}

func TestEditChapterMissingOldAndNewTextStillFails(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "当前草稿没有目标句段。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	args := json.RawMessage(`{"chapter":2,"old_string":"模型抄错的原文","new_string":"并不存在的目标新文"}`)
	if _, err := NewEditChapterTool(s).Execute(context.Background(), args); err == nil {
		t.Fatal("unrelated missing text must remain a hard matching error")
	}
}

func TestEditChapterUpdatesCanonicalDraftAfterStructureMigration(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "第一卷",
		Arcs: []domain.ArcOutline{{
			Index:    1,
			Title:    "第一弧",
			Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "第一章"}},
		}},
	}}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "她记得一句不该出现的旧台词。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	args := json.RawMessage(`{"chapter":1,"old_string":"不该出现的旧台词","new_string":"露台上真实发生的问答"}`)
	if _, err := NewEditChapterTool(s).Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if draft != "她记得一句露台上真实发生的问答。" {
		t.Fatalf("canonical draft was not updated: %q", draft)
	}
	legacy, err := os.ReadFile(filepath.Join(dir, "drafts", "01.draft.md"))
	if err != nil {
		t.Fatalf("ReadFile legacy draft: %v", err)
	}
	if string(legacy) != draft {
		t.Fatalf("legacy/canonical draft diverged: legacy=%q canonical=%q", legacy, draft)
	}
	legacyPath := filepath.Join(dir, "drafts", "01.draft.md")
	if err := os.WriteFile(legacyPath, []byte("stale legacy draft"), 0o644); err != nil {
		t.Fatalf("WriteFile stale legacy draft: %v", err)
	}
	reopened := store.NewStore(dir)
	canonical, err := reopened.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatalf("LoadDraft after reopen: %v", err)
	}
	if canonical != draft {
		t.Fatalf("canonical draft did not retain edit: got=%q want=%q", canonical, draft)
	}
}

func TestEditChapterAdaptationInvalidatesBothChecks(t *testing.T) {
	s := newAdaptationToolStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, "他握紧了拳头。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	raw, err := NewEditChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1,"old_string":"握紧","new_string":"松开"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	next, _ := payload["next_step"].(string)
	for _, want := range []string{"旧 check_consistency/check_adaptation 已失效", "重新调用", "同一版草稿", "commit_chapter"} {
		if !strings.Contains(next, want) {
			t.Fatalf("next_step missing %q: %q", want, next)
		}
	}
}

// TestEditChapterSeedsFromFinalChapter drafts 不存在但 chapters 有 → 自动从 chapters 播种。
func TestEditChapterSeedsFromFinalChapter(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	// 模拟第 3 章已提交且进入打磨队列
	original := "风从窗缝里钻进来，带着潮湿的泥土气味。"
	if err := s.Drafts.SaveFinalChapter(3, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, len([]rune(original)), "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{3}, "测试打磨"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    3,
		"old_string": "潮湿的泥土气味",
		"new_string": "泥土和铁锈混杂的气味",
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// drafts 应被播种且包含新文本
	draft, err := s.Drafts.LoadDraft(3)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if !strings.Contains(draft, "泥土和铁锈混杂的气味") {
		t.Fatalf("expected draft seeded + edited, got %q", draft)
	}

	// chapters 保持原样（edit_chapter 不碰终稿）
	final, err := s.Drafts.LoadChapterText(3)
	if err != nil {
		t.Fatalf("LoadChapterText: %v", err)
	}
	if final != original {
		t.Fatalf("final chapter must stay untouched, got %q", final)
	}
}

// TestEditChapterRejectsCompletedWithoutQueue 已完成且不在重写队列中 → 拒绝。
func TestEditChapterPendingPolishDoesNotReturnCreationBudgetRecovery(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const original = "completed chapter awaiting a targeted polish"
	if err := s.Drafts.SaveDraft(3, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "test",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowPolishing,
		TotalChapters:     3,
		CompletedChapters: []int{1, 2, 3},
		InProgressChapter: 3,
		PendingRewrites:   []int{3},
	}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(300, "test").WithPlannedChapters(3)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	raw, err := NewEditChapterTool(s).Execute(context.Background(), json.RawMessage(
		`{"chapter":3,"old_string":"targeted","new_string":"focused"}`,
	))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, exists := payload["runaway_safety_passed"]; exists {
		t.Fatalf("pending polish returned creation budget status: %+v", payload)
	}
	next, _ := payload["next_step"].(string)
	if !strings.Contains(next, "check_consistency") || strings.Contains(next, "Host") {
		t.Fatalf("pending polish returned the wrong next step: %q", next)
	}
}

func TestEditChapterRejectsCompletedWithoutQueue(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	original := "第二章原始正文。"
	if err := s.Drafts.SaveDraft(2, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(original)), "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "原始正文",
		"new_string": "篡改内容",
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected rejection for completed chapter not in PendingRewrites")
	}
	if !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("expected ErrToolPrecondition, got %v", err)
	}
}

// TestEditChapterRejectsAmbiguousMatch 多处匹配且未开 replace_all → 报错。
func TestEditChapterRejectsAmbiguousMatch(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "他笑了。她也笑了。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "笑了",
		"new_string": "沉默了",
	})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected rejection for ambiguous match")
	}
}

// TestEditChapterReplaceAll replace_all=true 时所有匹配均被替换。
func TestEditChapterReplaceAll(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "他笑了。她也笑了。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":     2,
		"old_string":  "笑了",
		"new_string":  "沉默了",
		"replace_all": true,
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, _ := s.Drafts.LoadDraft(2)
	if strings.Contains(got, "笑了") {
		t.Fatalf("all occurrences should be replaced, got %q", got)
	}
	if strings.Count(got, "沉默了") != 2 {
		t.Fatalf("expected 2 replacements, got %q", got)
	}
}

// TestEditChapterRejectsEmptyOldString 空 old_string → 参数非法。
func TestEditChapterRejectsEmptyOldString(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "",
		"new_string": "xxx",
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected rejection for empty old_string")
	}
	if !errors.Is(err, errs.ErrToolArgs) {
		t.Fatalf("expected ErrToolArgs, got %v", err)
	}
}

// TestEditChapterRejectsNoDraftNoFinal drafts 与 chapters 都不存在 → 报错提示先 draft_chapter。
func TestEditChapterRejectsNoDraftNoFinal(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewEditChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter":    5,
		"old_string": "任何",
		"new_string": "替换",
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected rejection when neither draft nor chapter exists")
	}
	if !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("expected ErrToolPrecondition, got %v", err)
	}
}

// TestEditChapterWorksWithCommitValidation 整条链路：edit_chapter → commit_chapter 成功 drain 队列。
// 验证新工具与 commit_chapter 的 drafts≠chapters 硬校验配合良好。
func TestEditChapterWorksWithCommitValidation(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	original := "风从窗缝里钻进来，带着潮湿的泥土气味。"
	if err := s.Drafts.SaveDraft(2, original); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(2, original); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, len([]rune(original)), "mystery", "quest"); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "打磨"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatalf("SetFlow: %v", err)
	}

	editTool := NewEditChapterTool(s)
	editArgs, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"old_string": "潮湿的泥土气味",
		"new_string": "泥土和铁锈混杂的气味",
	})
	if _, err := editTool.Execute(context.Background(), editArgs); err != nil {
		t.Fatalf("edit_chapter: %v", err)
	}

	commitTool := NewCommitChapterTool(s)
	commitArgs, _ := json.Marshal(map[string]any{
		"chapter":    2,
		"summary":    "打磨后摘要",
		"characters": []string{"主角"},
		"key_events": []string{"完成打磨"},
	})
	if _, err := commitTool.Execute(context.Background(), commitArgs); err != nil {
		t.Fatalf("commit_chapter after edit: %v", err)
	}

	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if len(progress.PendingRewrites) != 0 {
		t.Fatalf("expected queue drained, got %v", progress.PendingRewrites)
	}
}

func TestEditChapterAllowsExactRepairWhenDeAIReportHasMultipleKinds(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	content := "# 第一章\n\n林逸飞没有回答——门外有人敲了一下。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatalf("check_de_ai: %v", err)
	}
	_, err := NewEditChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1,"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"}`))
	if err != nil {
		t.Fatalf("expected exact repair to remain available, got %v", err)
	}
}
