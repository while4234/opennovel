package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSaveReviewPersistsContractAssessment(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	for ch := 1; ch <= 3; ch++ {
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}
	if err := s.Drafts.SaveDraft(3, "用于绑定 Editor 审阅的当前合成草稿。"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter":           3,
		"scope":             "chapter",
		"dimensions":        []map[string]any{{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "基本一致"}, {"dimension": "character", "score": 82, "verdict": "pass", "comment": "人设稳定"}, {"dimension": "pacing", "score": 78, "verdict": "warning", "comment": "略慢"}, {"dimension": "continuity", "score": 84, "verdict": "pass", "comment": "连贯"}, {"dimension": "foreshadow", "score": 80, "verdict": "pass", "comment": "正常"}, {"dimension": "hook", "score": 76, "verdict": "warning", "comment": "钩子一般"}, {"dimension": "aesthetic", "score": 81, "verdict": "pass", "comment": "语言基本成立"}},
		"issues":            []map[string]any{},
		"contract_status":   "partial",
		"contract_misses":   []string{"未明确埋下内门试炼邀请"},
		"contract_notes":    "主线推进达成，但 contract 中的第二个推进项没有落地。",
		"verdict":           "polish",
		"summary":           "本章基本完成目标，但 contract 仍有漏项。",
		"affected_chapters": []int{3},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	review, err := s.World.LoadReview(3)
	if err != nil {
		t.Fatalf("LoadReview: %v", err)
	}
	if review == nil {
		t.Fatal("expected review saved, got nil")
	}
	if review.ContractStatus != "partial" {
		t.Fatalf("unexpected contract status: %q", review.ContractStatus)
	}
	if len(review.ContractMisses) != 1 || review.ContractMisses[0] != "未明确埋下内门试炼邀请" {
		t.Fatalf("unexpected contract misses: %+v", review.ContractMisses)
	}
	if review.Dimension("aesthetic") == nil {
		t.Fatalf("expected aesthetic dimension persisted, got %+v", review.Dimensions)
	}
	if review.DraftSHA256 != store.TextSHA256("用于绑定 Editor 审阅的当前合成草稿。") {
		t.Fatalf("review draft digest = %q", review.DraftSHA256)
	}
}

func TestSaveReviewSchemaIsStrictCompatible(t *testing.T) {
	tool := NewSaveReviewTool(nil)
	if !tool.StrictSchema() {
		t.Fatal("save_review should opt into strict schema")
	}

	root := tool.Schema()
	requireRequiredFields(t, root,
		"chapter",
		"scope",
		"volume",
		"arc",
		"batch_from",
		"batch_to",
		"dimensions",
		"issues",
		"contract_status",
		"contract_misses",
		"contract_notes",
		"verdict",
		"summary",
		"affected_chapters",
	)
	requireAllPropertiesRequired(t, root)

	props, ok := root["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has type %T", root["properties"])
	}
	dimensions, ok := props["dimensions"].(map[string]any)
	if !ok {
		t.Fatalf("dimensions schema has type %T", props["dimensions"])
	}
	dimensionItem, ok := dimensions["items"].(map[string]any)
	if !ok {
		t.Fatalf("dimensions.items has type %T", dimensions["items"])
	}
	requireRequiredFields(t, dimensionItem, "dimension", "score", "verdict", "comment")
	requireAllPropertiesRequired(t, dimensionItem)

	issues, ok := props["issues"].(map[string]any)
	if !ok {
		t.Fatalf("issues schema has type %T", props["issues"])
	}
	issueItem, ok := issues["items"].(map[string]any)
	if !ok {
		t.Fatalf("issues.items has type %T", issues["items"])
	}
	requireRequiredFields(t, issueItem, "type", "severity", "description", "evidence", "suggestion")
	requireAllPropertiesRequired(t, issueItem)
}

func requireAllPropertiesRequired(t *testing.T, schema map[string]any) {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has type %T", schema["properties"])
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required has type %T", schema["required"])
	}
	seen := make(map[string]struct{}, len(required))
	for _, field := range required {
		seen[field] = struct{}{}
	}
	for field := range props {
		if _, ok := seen[field]; !ok {
			t.Fatalf("strict schema property %q is not required; required=%v", field, required)
		}
	}
}

func requireRequiredFields(t *testing.T, schema map[string]any, fields ...string) {
	t.Helper()
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required has type %T", schema["required"])
	}
	seen := make(map[string]struct{}, len(required))
	for _, field := range required {
		seen[field] = struct{}{}
	}
	for _, field := range fields {
		if _, ok := seen[field]; !ok {
			t.Fatalf("required missing %q; got %v", field, required)
		}
	}
}

func TestSaveReviewRejectsMissingDimensions(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	for ch := 1; ch <= 3; ch++ {
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter":    3,
		"scope":      "chapter",
		"dimensions": []map[string]any{{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "基本一致"}},
		"issues":     []map[string]any{},
		"verdict":    "accept",
		"summary":    "ok",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "dimensions must contain exactly") {
		t.Fatalf("expected dimensions validation error, got %v", err)
	}
}

func TestSaveReviewRejectsDimensionWithoutComment(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "comment": "基本一致"},
			{"dimension": "character", "score": 82, "comment": "人设稳定"},
			{"dimension": "pacing", "score": 78},
			{"dimension": "continuity", "score": 84, "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "comment": "正常"},
			{"dimension": "hook", "score": 76, "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "comment": "语言基本成立"},
		},
		"issues":  []map[string]any{},
		"verdict": "accept",
		"summary": "ok",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "dimension comment is required: pacing") {
		t.Fatalf("expected dimension comment validation error, got %v", err)
	}
}

func TestSaveReviewRejectsUnfinishedAffectedChapter(t *testing.T) {
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

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 58,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "comment": "基本一致"},
			{"dimension": "character", "score": 82, "comment": "人设稳定"},
			{"dimension": "pacing", "score": 58, "comment": "节奏需要重写"},
			{"dimension": "continuity", "score": 84, "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "comment": "正常"},
			{"dimension": "hook", "score": 76, "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "comment": "语言基本成立"},
		},
		"issues":            []map[string]any{},
		"contract_status":   "partial",
		"verdict":           "polish",
		"summary":           "需要打磨第 58 章，不能把未完成章节入队。",
		"affected_chapters": []int{65},
		"contract_misses":   []string{"节奏超出本章职责"},
		"contract_notes":    "应只处理已完成章节。",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "pending_rewrites 只能包含已完成章节") {
		t.Fatalf("expected unfinished affected chapter rejection, got %v", err)
	}
	review, err := s.World.LoadReview(58)
	if err != nil {
		t.Fatalf("LoadReview: %v", err)
	}
	if review != nil {
		t.Fatalf("review should not be saved when pending rewrite validation fails: %+v", review)
	}
	p, _ := s.Progress.Load()
	if p.Flow != domain.FlowWriting && p.Flow != "" {
		t.Fatalf("flow should not enter rewrite/polish, got %s", p.Flow)
	}
	if len(p.PendingRewrites) != 0 {
		t.Fatalf("pending_rewrites should remain empty, got %v", p.PendingRewrites)
	}
}

// TestSaveReviewDerivesVerdictFromScore 验证：verdict 由 score 确定性推导，模型给的
// 不一致 verdict（如 score=85 却填 warning）不再报错，而是被覆写成正确值（pass）。
// 防回归 issue：弱模型 score/verdict 打架曾导致 save_review 反复失败。
func TestSaveReviewDerivesVerdictFromScore(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "一致"},
			{"dimension": "character", "score": 82, "comment": "稳定"}, // 省略 verdict
			{"dimension": "pacing", "score": 78, "verdict": "warning", "comment": "略慢"},
			{"dimension": "continuity", "score": 84, "verdict": "pass", "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "verdict": "pass", "comment": "正常"},
			{"dimension": "hook", "score": 76, "verdict": "warning", "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 85, "verdict": "warning", "comment": "语言成立"}, // 不一致：85 却填 warning
		},
		"issues":  []map[string]any{},
		"verdict": "accept",
		"summary": "ok",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute should succeed (verdict auto-derived), got %v", err)
	}

	review, err := s.World.LoadReview(3)
	if err != nil || review == nil {
		t.Fatalf("LoadReview: %v", err)
	}
	// 85 → pass（覆写模型给的 warning）；82 省略 → pass。
	if d := review.Dimension("aesthetic"); d == nil || d.Verdict != "pass" {
		t.Fatalf("aesthetic verdict should be derived to pass, got %+v", d)
	}
	if d := review.Dimension("character"); d == nil || d.Verdict != "pass" {
		t.Fatalf("character verdict should be derived to pass, got %+v", d)
	}
}

func TestSaveReviewRejectsMissingAffectedChaptersForRewrite(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "基本一致"},
			{"dimension": "character", "score": 82, "verdict": "pass", "comment": "人设稳定"},
			{"dimension": "pacing", "score": 78, "verdict": "warning", "comment": "略慢"},
			{"dimension": "continuity", "score": 84, "verdict": "pass", "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "verdict": "pass", "comment": "正常"},
			{"dimension": "hook", "score": 76, "verdict": "warning", "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "verdict": "pass", "comment": "语言基本成立"},
		},
		"issues":  []map[string]any{},
		"verdict": "rewrite",
		"summary": "需要重写",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "affected_chapters is required") {
		t.Fatalf("expected affected_chapters validation error, got %v", err)
	}
}

func TestSaveReviewRejectsIssueWithoutEvidence(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "基本一致"},
			{"dimension": "character", "score": 82, "verdict": "pass", "comment": "人设稳定"},
			{"dimension": "pacing", "score": 78, "verdict": "warning", "comment": "略慢"},
			{"dimension": "continuity", "score": 84, "verdict": "pass", "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "verdict": "pass", "comment": "正常"},
			{"dimension": "hook", "score": 76, "verdict": "warning", "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "verdict": "pass", "comment": "语言基本成立"},
		},
		"issues": []map[string]any{
			{"type": "hook", "severity": "warning", "description": "章末钩子偏弱"},
		},
		"verdict":           "polish",
		"summary":           "需要补强钩子。",
		"affected_chapters": []int{3},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "issue evidence is required") {
		t.Fatalf("expected issue evidence validation error, got %v", err)
	}
}

func TestSaveReviewIssueErrorEscalatesAcceptToPolish(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	for ch := 1; ch <= 3; ch++ {
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter":    3,
		"scope":      "arc",
		"dimensions": passingReviewDimensions(),
		"issues": []map[string]any{
			{"type": "aesthetic", "severity": "error", "description": "chapter 2 keeps a meta label in prose", "evidence": "inner monologue label"},
		},
		"verdict": "accept",
		"summary": "accepted by model despite an error issue",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["final_verdict"] != "polish" {
		t.Fatalf("expected final_verdict polish, got %v", result["final_verdict"])
	}
	if !strings.Contains(result["escalation_reason"].(string), "error") {
		t.Fatalf("expected issue severity escalation reason, got %v", result["escalation_reason"])
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Progress.Load: %v", err)
	}
	if p.Flow != domain.FlowPolishing {
		t.Fatalf("expected polishing flow, got %s", p.Flow)
	}
	if len(p.PendingRewrites) != 1 || p.PendingRewrites[0] != 2 {
		t.Fatalf("expected chapter 2 pending polish, got %v", p.PendingRewrites)
	}
}

func TestSaveReviewIssueCriticalEscalatesPolishToRewrite(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter":    3,
		"scope":      "chapter",
		"dimensions": passingReviewDimensions(),
		"issues": []map[string]any{
			{"type": "continuity", "severity": "critical", "description": "contradicts prior chapter", "evidence": "chapter 2 says the opposite"},
		},
		"verdict":           "polish",
		"summary":           "model under-classified a critical issue",
		"affected_chapters": []int{3},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	if result["final_verdict"] != "rewrite" {
		t.Fatalf("expected final_verdict rewrite, got %v", result["final_verdict"])
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Progress.Load: %v", err)
	}
	if p.Flow != domain.FlowRewriting {
		t.Fatalf("expected rewriting flow, got %s", p.Flow)
	}
}

func TestSaveReviewArcBatchMergesAfterAllChapterBatches(t *testing.T) {
	s := store.NewStore(testStoreDir(t))
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CurrentVolume:     1,
		CurrentArc:        1,
		TotalChapters:     4,
		CompletedChapters: []int{1, 2, 3, 4},
	}); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "第一卷",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "第一弧",
			Chapters: []domain.OutlineEntry{
				{Title: "一", CoreEvent: "起"},
				{Title: "二", CoreEvent: "承"},
				{Title: "三", CoreEvent: "转"},
				{Title: "四", CoreEvent: "合"},
			},
		}},
	}}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	for chapter := 1; chapter <= 4; chapter++ {
		if err := s.Drafts.SaveFinalChapter(chapter, strings.Repeat("正文", 1000)); err != nil {
			t.Fatalf("SaveFinalChapter(%d): %v", chapter, err)
		}
	}

	tool := NewSaveReviewTool(s)
	first := arcBatchReviewArgs(4, 1, 1, 1, 2, "第一批无硬伤")
	raw, err := tool.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("Execute first batch: %v", err)
	}
	var firstResult map[string]any
	if err := json.Unmarshal(raw, &firstResult); err != nil {
		t.Fatalf("Unmarshal first result: %v", err)
	}
	if firstResult["arc_review_complete"] != false {
		t.Fatalf("first batch should not complete arc review: %+v", firstResult)
	}
	if s.World.HasArcReview(4) {
		t.Fatal("batch review must not count as final arc review")
	}

	second := arcBatchReviewArgs(4, 1, 1, 3, 4, "第二批无硬伤")
	raw, err = tool.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("Execute second batch: %v", err)
	}
	var secondResult map[string]any
	if err := json.Unmarshal(raw, &secondResult); err != nil {
		t.Fatalf("Unmarshal second result: %v", err)
	}
	if secondResult["merged_from_batches"] != true || secondResult["arc_review_complete"] != true {
		t.Fatalf("second batch should merge final arc review: %+v", secondResult)
	}
	review, err := s.World.LoadReview(4)
	if err != nil {
		t.Fatalf("LoadReview: %v", err)
	}
	if review == nil || review.Scope != "arc" {
		t.Fatalf("expected final arc review, got %+v", review)
	}
	if review.BatchFrom != 1 || review.BatchTo != 4 {
		t.Fatalf("merged batch range = %d-%d, want 1-4", review.BatchFrom, review.BatchTo)
	}
	if !strings.Contains(review.Summary, "共2批") {
		t.Fatalf("merged summary should mention batch count, got %q", review.Summary)
	}
}

func arcBatchReviewArgs(chapter, volume, arc, from, to int, summary string) []byte {
	args, _ := json.Marshal(map[string]any{
		"chapter":           chapter,
		"scope":             "arc_batch",
		"volume":            volume,
		"arc":               arc,
		"batch_from":        from,
		"batch_to":          to,
		"dimensions":        passingReviewDimensions(),
		"issues":            []map[string]any{},
		"contract_status":   "met",
		"contract_misses":   []string{},
		"contract_notes":    summary,
		"verdict":           "accept",
		"summary":           summary,
		"affected_chapters": []int{},
	})
	return args
}

func passingReviewDimensions() []map[string]any {
	return []map[string]any{
		{"dimension": "consistency", "score": 85, "comment": "ok"},
		{"dimension": "character", "score": 85, "comment": "ok"},
		{"dimension": "pacing", "score": 85, "comment": "ok"},
		{"dimension": "continuity", "score": 85, "comment": "ok"},
		{"dimension": "foreshadow", "score": 85, "comment": "ok"},
		{"dimension": "hook", "score": 85, "comment": "ok"},
		{"dimension": "aesthetic", "score": 85, "comment": "ok"},
	}
}

func TestInferAffectedChaptersFromIssuesParsesChineseChapter(t *testing.T) {
	chapters := inferAffectedChaptersFromIssues([]domain.ConsistencyIssue{
		{Description: "第3章中仍保留提示词残留", Evidence: "第3章出现示意文本"},
	})
	if len(chapters) != 1 || chapters[0] != 3 {
		t.Fatalf("expected chapter 3, got %v", chapters)
	}
}
