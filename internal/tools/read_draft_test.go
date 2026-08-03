package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestReadChapterFinal(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Drafts.SaveFinalChapter(1, "第一章的终稿正文。"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"chapter": 1, "source": "final"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Chapter   int    `json:"chapter"`
		Content   string `json:"content"`
		WordCount int    `json:"word_count"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Content == "" {
		t.Fatal("expected non-empty content")
	}
	if payload.WordCount == 0 {
		t.Fatal("expected non-zero word count")
	}
}

func TestReadChapterDraft(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Drafts.SaveDraft(3, "第三章的草稿内容。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"chapter": 3, "source": "draft"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Content == "" {
		t.Fatal("expected draft content")
	}
}

func TestReadChapterDraftLineSegmentKeepsFullDraftIdentity(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const content = "line one\nline two\nline three\nline four"
	if err := s.Drafts.SaveDraft(3, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 3, InProgressChapter: 3}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(300, "test").WithPlannedChapters(3)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(
		`{"chapter":3,"source":"draft","from_line":1,"to_line":4}`,
	))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Content         string `json:"content"`
		WordCount       int    `json:"word_count"`
		ContentSHA256   string `json:"content_sha256"`
		SegmentFromLine int    `json:"segment_from_line"`
		SegmentToLine   int    `json:"segment_to_line"`
		SegmentRunes    int    `json:"segment_runes"`
		SegmentComplete bool   `json:"segment_complete"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Content != content || payload.SegmentFromLine != 1 ||
		payload.SegmentToLine != 4 || payload.SegmentRunes != len([]rune(payload.Content)) ||
		!payload.SegmentComplete {
		t.Fatalf("unexpected line segment: %+v", payload)
	}
	if payload.WordCount != len([]rune(content)) || payload.ContentSHA256 != store.TextSHA256(content) {
		t.Fatalf("segment lost full-draft identity: %+v", payload)
	}
}

func TestReadChapterDraftDefersRangeOutsideCurrentHostBudgetSegment(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	lines := make([]string, 130)
	for index := range lines {
		lines[index] = strings.Repeat("x", 4)
	}
	content := strings.Join(lines, "\n")
	if err := s.Drafts.SaveDraft(3, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 3, InProgressChapter: 3}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(300, "test").WithPlannedChapters(3)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(
		`{"chapter":3,"source":"draft","from_line":1,"to_line":100,"max_runes":3000}`,
	))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Content               string `json:"content"`
		DeferredToHost        bool   `json:"deferred_to_host"`
		ExpectedBudgetSegment int    `json:"expected_budget_segment"`
		ExpectedFromLine      int    `json:"expected_from_line"`
		ExpectedToLine        int    `json:"expected_to_line"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.DeferredToHost || payload.Content != "" || payload.ExpectedBudgetSegment != 2 ||
		payload.ExpectedFromLine != 97 || payload.ExpectedToLine != 130 {
		t.Fatalf("non-Host range leaked draft content: %+v", payload)
	}
}

func TestReadChapterDraftUsesLatestBudgetCursorAfterValidationCheckpoint(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	lines := make([]string, 295)
	for index := range lines {
		lines[index] = "over budget prose"
	}
	content := strings.Join(lines, "\n")
	if err := s.Drafts.SaveDraft(47, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 96, InProgressChapter: 47}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(300000, "test").WithPlannedChapters(96)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	if _, err := s.Checkpoints.Append(domain.ChapterScope(47), "word_budget_edit_segment_6", "", ""); err != nil {
		t.Fatalf("append budget checkpoint: %v", err)
	}
	if _, err := s.Checkpoints.Append(domain.ChapterScope(47), "de_ai_check", "", ""); err != nil {
		t.Fatalf("append validation checkpoint: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(
		`{"chapter":47,"source":"draft","from_line":1,"to_line":100}`,
	))
	if err != nil {
		t.Fatalf("Execute wrong range: %v", err)
	}
	var deferred struct {
		DeferredToHost        bool `json:"deferred_to_host"`
		ExpectedBudgetSegment int  `json:"expected_budget_segment"`
		ExpectedFromLine      int  `json:"expected_from_line"`
		ExpectedToLine        int  `json:"expected_to_line"`
	}
	if err := json.Unmarshal(raw, &deferred); err != nil || !deferred.DeferredToHost ||
		deferred.ExpectedBudgetSegment != 5 || deferred.ExpectedFromLine != 241 || deferred.ExpectedToLine != 288 {
		t.Fatalf("validation checkpoint reset tool cursor: %+v err=%v raw=%s", deferred, err, raw)
	}
}

func TestReadChapterFinalFallbackStillDefersOutOfBudgetDraft(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	content := strings.Repeat("x", 400)
	if err := s.Drafts.SaveDraft(3, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 3, InProgressChapter: 3}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(300, "test").WithPlannedChapters(3)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":3,"source":"final"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Content        string `json:"content"`
		DeferredToHost bool   `json:"deferred_to_host"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || !payload.DeferredToHost || payload.Content != "" {
		t.Fatalf("final fallback leaked out-of-budget draft: %+v err=%v raw=%s", payload, err, raw)
	}
}

func TestReadChapterDraftDefersFullOutOfBudgetReadToHost(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const content = "line one\nline two\nline three\nline four"
	if err := s.Drafts.SaveDraft(3, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 3, InProgressChapter: 3}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(300, "test").WithPlannedChapters(3)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":3,"source":"draft"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Content        string `json:"content"`
		DeferredToHost bool   `json:"deferred_to_host"`
		NextStep       string `json:"next_step"`
		WordCount      int    `json:"word_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.DeferredToHost || payload.Content != "" || payload.WordCount != len([]rune(content)) ||
		!strings.Contains(payload.NextStep, "Host 将指定唯一行段") {
		t.Fatalf("full out-of-budget read was not deferred: %+v", payload)
	}
}

func TestReadChapterDraftAllowsFullReadDuringPendingPolish(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const content = "line one\nline two\nline three\nline four"
	if err := s.Drafts.SaveDraft(3, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "test",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowPolishing,
		TotalChapters:     3,
		InProgressChapter: 3,
		PendingRewrites:   []int{3},
	}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(300, "test").WithPlannedChapters(3)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":3,"source":"draft"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Content        string `json:"content"`
		DeferredToHost bool   `json:"deferred_to_host"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.DeferredToHost || payload.Content != content {
		t.Fatalf("pending polish was incorrectly budget-segmented: %+v", payload)
	}
}

func TestReadChapterDraftAllowsSoftRecommendationOverage(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		NovelName:         "test",
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		CurrentChapter:    1,
		InProgressChapter: 1,
		TotalChapters:     55,
	}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(200000, "test").WithPlannedChapters(55)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}
	saveChapterWordRange(t, s, 3000, 6000)
	content := strings.Repeat("正", 4165)
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1,"source":"draft"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Content        string `json:"content"`
		DeferredToHost bool   `json:"deferred_to_host"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.DeferredToHost || payload.Content != content {
		t.Fatalf("soft recommendation overage must allow full quality read: %+v", payload)
	}
}

func TestReadChapterDraftLineSegmentRejectsInBudgetDraft(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const content = "line one\nline two\nline three\nline four"
	if err := s.Drafts.SaveDraft(3, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 3, InProgressChapter: 3}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	budget := domain.NewWordBudget(120, "test").WithPlannedChapters(3)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("SetWordBudget: %v", err)
	}

	_, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(
		`{"chapter":3,"source":"draft","from_line":2,"to_line":3}`,
	))
	if err == nil || !strings.Contains(err.Error(), "only available for the current out-of-budget draft") {
		t.Fatalf("in-budget draft must reject segmented read, err=%v", err)
	}
}

func TestReadChapterDraftReportsPolishDifference(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(3, "终稿含有校对残留。加粗去掉正文。"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Drafts.SaveDraft(3, "终稿含有校对残留。正文。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":3,"source":"draft"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		ContentSHA256    string `json:"content_sha256"`
		FinalSHA256      string `json:"final_sha256"`
		DiffersFromFinal bool   `json:"differs_from_final"`
		PolishState      string `json:"polish_state"`
		PolishHint       string `json:"polish_hint"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.DiffersFromFinal || payload.PolishState != "modified" {
		t.Fatalf("draft difference was not exposed: %+v", payload)
	}
	if payload.ContentSHA256 == "" || payload.FinalSHA256 == "" || payload.ContentSHA256 == payload.FinalSHA256 {
		t.Fatalf("expected distinct draft/final hashes: %+v", payload)
	}
	if !strings.Contains(payload.PolishHint, "不要为满足修改次数重复改稿") {
		t.Fatalf("missing recovery guidance: %q", payload.PolishHint)
	}
}

func TestReadChapterDraftReportsUnchangedPolishState(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const content = "草稿与终稿完全相同。"
	if err := s.Drafts.SaveFinalChapter(3, content); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Drafts.SaveDraft(3, content); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	raw, err := NewReadChapterTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":3,"source":"draft"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		ContentSHA256    string `json:"content_sha256"`
		FinalSHA256      string `json:"final_sha256"`
		DiffersFromFinal bool   `json:"differs_from_final"`
		PolishState      string `json:"polish_state"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.DiffersFromFinal || payload.PolishState != "unchanged" || payload.ContentSHA256 != payload.FinalSHA256 {
		t.Fatalf("unchanged draft state was not exposed: %+v", payload)
	}
}

func TestReadChapterDialogue(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Characters.Save([]domain.Character{
		{Name: "张三", Aliases: []string{"老张"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := store.Drafts.SaveFinalChapter(1, "张三站起身来。\u201c我不同意这个方案，\u201d张三冷冷地说。"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"source": "final", "character": "张三"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Character string   `json:"character"`
		Samples   []string `json:"samples"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Character != "张三" {
		t.Fatalf("expected character 张三, got %s", payload.Character)
	}
}

func TestReadChapterRange(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := store.Drafts.SaveFinalChapter(i, "这是一段正文内容。"); err != nil {
			t.Fatalf("SaveFinalChapter(%d): %v", i, err)
		}
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"from": 1, "to": 3, "source": "final"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Chapters map[string]string `json:"chapters"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Chapters) != 3 {
		t.Fatalf("expected 3 chapters, got %d", len(payload.Chapters))
	}
}

func TestReadChapterRangeBudgetStopsAtChapterBoundary(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := store.Drafts.SaveFinalChapter(i, strings.Repeat("章", 8)); err != nil {
			t.Fatalf("SaveFinalChapter(%d): %v", i, err)
		}
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{
		"from":            1,
		"to":              3,
		"source":          "final",
		"max_total_runes": 12,
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Chapters         map[string]string `json:"chapters"`
		ReturnedChapters []int             `json:"returned_chapters"`
		OmittedChapters  []int             `json:"omitted_chapters"`
		NextFrom         int               `json:"next_from"`
		Complete         bool              `json:"complete"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Chapters) != 1 || payload.Chapters["1"] != strings.Repeat("章", 8) {
		t.Fatalf("expected only full chapter 1, got %+v", payload.Chapters)
	}
	if payload.NextFrom != 2 || payload.Complete {
		t.Fatalf("expected next_from=2 and incomplete payload, got next_from=%d complete=%v", payload.NextFrom, payload.Complete)
	}
	if len(payload.OmittedChapters) != 2 || payload.OmittedChapters[0] != 2 || payload.OmittedChapters[1] != 3 {
		t.Fatalf("omitted chapters should start at the next whole chapter, got %+v", payload.OmittedChapters)
	}
}

func TestReadChapterRangeBudgetReturnsOversizedChapterWhole(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Drafts.SaveFinalChapter(1, strings.Repeat("长", 20)); err != nil {
		t.Fatalf("SaveFinalChapter(1): %v", err)
	}
	if err := store.Drafts.SaveFinalChapter(2, "第二章"); err != nil {
		t.Fatalf("SaveFinalChapter(2): %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{
		"from":            1,
		"to":              2,
		"source":          "final",
		"max_runes":       5,
		"max_total_runes": 10,
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Chapters          map[string]string `json:"chapters"`
		OversizedChapters []int             `json:"oversized_chapters"`
		OmittedChapters   []int             `json:"omitted_chapters"`
		NextFrom          int               `json:"next_from"`
		TotalRunes        int               `json:"total_runes"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Chapters["1"] != strings.Repeat("长", 20) {
		t.Fatalf("oversized chapter should not be split or truncated, got %q", payload.Chapters["1"])
	}
	if len(payload.OversizedChapters) != 1 || payload.OversizedChapters[0] != 1 {
		t.Fatalf("expected chapter 1 marked oversized, got %+v", payload.OversizedChapters)
	}
	if payload.NextFrom != 2 || len(payload.OmittedChapters) != 1 || payload.OmittedChapters[0] != 2 {
		t.Fatalf("expected next whole chapter queued, next_from=%d omitted=%+v", payload.NextFrom, payload.OmittedChapters)
	}
	if payload.TotalRunes < 20 {
		t.Fatalf("expected full oversized rune count, got %d", payload.TotalRunes)
	}
}

func TestDraftChapterWrite(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewDraftChapterTool(store)
	args, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"content": "这是整章的正文内容，一次写完。",
		"mode":    "write",
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Written   bool `json:"written"`
		WordCount int  `json:"word_count"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !payload.Written {
		t.Fatal("expected written=true")
	}
	if payload.WordCount == 0 {
		t.Fatal("expected non-zero word count")
	}

	// 验证能读回来
	content, err := store.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatalf("LoadDraft: %v", err)
	}
	if content == "" {
		t.Fatal("expected non-empty draft")
	}
	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.InProgressChapter != 1 {
		t.Fatalf("expected in-progress chapter 1, got %d", progress.InProgressChapter)
	}
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("expected phase writing, got %s", progress.Phase)
	}
}

func TestDraftChapterAppend(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := store.Drafts.SaveDraft(2, "前半部分。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewDraftChapterTool(store)
	args, _ := json.Marshal(map[string]any{
		"chapter": 2,
		"content": "后半部分。",
		"mode":    "append",
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Mode      string `json:"mode"`
		WordCount int    `json:"word_count"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Mode != "append" {
		t.Fatalf("expected mode=append, got %s", payload.Mode)
	}

	content, _ := store.Drafts.LoadDraft(2)
	if content == "" || content == "前半部分。" {
		t.Fatal("expected appended content")
	}
}

func TestReadChapterMissingReturnsJSON(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewReadChapterTool(store)
	args, _ := json.Marshal(map[string]any{"chapter": 1, "source": "final"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("expected no error for missing chapter, got: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed["exists"] != false {
		t.Fatal("expected exists=false")
	}
}

func TestPlanChapterMarksInProgress(t *testing.T) {
	dir := testStoreDir(t)
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := NewPlanChapterTool(store)
	args, _ := json.Marshal(map[string]any{
		"chapter":  1,
		"title":    "起头",
		"goal":     "建立处境",
		"conflict": "债务逼近",
		"hook":     "发现线索",
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	progress, err := store.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("expected phase writing, got %s", progress.Phase)
	}
	if progress.InProgressChapter != 1 {
		t.Fatalf("expected in-progress chapter 1, got %d", progress.InProgressChapter)
	}
}

func TestDraftChapterRejectsCompleted(t *testing.T) {
	dir := testStoreDir(t)
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	_ = s.Drafts.SaveDraft(1, "第一章正文")
	_ = s.Progress.StartChapter(1)
	_ = s.Progress.MarkChapterComplete(1, 3000, "", "")

	tool := NewDraftChapterTool(s)
	args, _ := json.Marshal(map[string]any{
		"chapter": 1,
		"content": "试图覆盖已提交的章节",
	})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("expected soft rejection, got error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload["skipped"] != true {
		t.Fatalf("expected skipped=true, got %+v", payload)
	}
}
