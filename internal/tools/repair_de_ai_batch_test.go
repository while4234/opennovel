package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestRepairDeAIBatchAppliesBoundedExactRevisions(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n林逸飞没有回答——门外有人敲了一下。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	result, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"},
			{"old_string":"林逸飞没有回答——桌上的杯子晃了晃。","new_string":"桌上的杯子被他无意碰得轻响一声，他仍盯着那份文件。"}
		]
	}`))
	if err != nil {
		t.Fatalf("repair batch: %v", err)
	}
	if !strings.Contains(string(result), `"repaired_count":2`) {
		t.Fatalf("result = %s", result)
	}
	if !strings.Contains(string(result), "最终提交版本") {
		t.Fatalf("result must require both gates on the final commit version: %s", result)
	}
	var next struct {
		NextStep string `json:"next_step"`
	}
	if err := json.Unmarshal(result, &next); err != nil {
		t.Fatal(err)
	}
	if deAI, consistency := strings.Index(next.NextStep, "check_de_ai"), strings.Index(next.NextStep, "check_consistency"); deAI < 0 || consistency < 0 || deAI >= consistency {
		t.Fatalf("repair must defer consistency until de-AI passes: %q", next.NextStep)
	}
	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(draft, "林逸飞没有回答——门外有人敲了一下。") || !strings.Contains(draft, "门外传来两下轻敲") {
		t.Fatalf("unexpected repaired draft: %s", draft)
	}
	if checkpoint := s.Checkpoints.LatestByStep(domain.ChapterScope(1), "de_ai_batch_repair"); checkpoint == nil {
		t.Fatal("expected de_ai_batch_repair checkpoint")
	}
}

func TestRepairDeAIBatchDefersCurrentOutOfBudgetDraft(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Save(&domain.Progress{NovelName: "test", Phase: domain.PhaseWriting, TotalChapters: 1, InProgressChapter: 1}); err != nil {
		t.Fatal(err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatal(err)
	}
	problem := "林逸飞没有回答——门外有人敲了一下。"
	content := strings.Repeat("前文。", 50) + problem +
		"林逸飞没有回答——桌上的杯子晃了晃。" +
		"林逸飞没有回答——吴宇申把文件推过来。" +
		"林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	raw, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞看向门缝。"}]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		Changed        bool `json:"changed"`
		DeferredToHost bool `json:"deferred_to_host"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Changed || !payload.DeferredToHost {
		t.Fatalf("out-of-budget de-AI repair was not deferred: %+v err=%v raw=%s", payload, err, raw)
	}
	if got, loadErr := s.Drafts.LoadDraft(1); loadErr != nil || got != content {
		t.Fatalf("deferred repair changed draft: err=%v", loadErr)
	}
}

func TestRepairDeAIBatchSkipsStaleEntryAndAppliesCurrentMatches(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n门外传来两下轻敲，林逸飞把视线移向门缝。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。\n\n林逸飞没有回答——走廊的灯灭了。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	result, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"},
			{"old_string":"林逸飞没有回答——桌上的杯子晃了晃。","new_string":"杯子被他碰得轻响一声，他仍盯着那份文件。"}
		]
	}`))
	if err != nil {
		t.Fatalf("repair batch with one stale entry: %v", err)
	}
	var payload struct {
		Changed             bool   `json:"changed"`
		RepairedCount       int    `json:"repaired_count"`
		SkippedStaleCount   int    `json:"skipped_stale_count"`
		SkippedStaleIndices []int  `json:"skipped_stale_indices"`
		NextStep            string `json:"next_step"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Changed || payload.RepairedCount != 1 || payload.SkippedStaleCount != 1 ||
		len(payload.SkippedStaleIndices) != 1 || payload.SkippedStaleIndices[0] != 0 {
		t.Fatalf("unexpected stale-entry result: %+v", payload)
	}
	if !strings.Contains(payload.NextStep, "不要重放旧批次") {
		t.Fatalf("missing safe recovery guidance: %q", payload.NextStep)
	}
	if deAI, consistency := strings.Index(payload.NextStep, "check_de_ai"), strings.Index(payload.NextStep, "check_consistency"); deAI < 0 || consistency < 0 || deAI >= consistency {
		t.Fatalf("stale recovery must defer consistency until de-AI passes: %q", payload.NextStep)
	}
	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draft, "门外传来两下轻敲") || !strings.Contains(draft, "杯子被他碰得轻响一声") {
		t.Fatalf("stale entry prevented valid repair: %s", draft)
	}
}

func TestRepairDeAIBatchStaleAuditRefreshesDeAIBeforeFinalConsistency(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n" + strings.Repeat("他停住——又向前走了一步。\n", 20)
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, content+"\n草稿已经变化。"); err != nil {
		t.Fatal(err)
	}

	_, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[{"old_string":"他停住——又向前走了一步。","new_string":"他停住，片刻后才向前走。"}]
	}`))
	if err == nil || !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("expected stale-audit precondition, got %v", err)
	}
	message := err.Error()
	deAI := strings.Index(message, "check_de_ai")
	consistency := strings.Index(message, "check_consistency")
	if deAI < 0 || consistency < 0 || deAI >= consistency {
		t.Fatalf("stale-audit recovery order is unclear: %q", message)
	}
	for _, want := range []string{"禁止重试修订", "只有新的 check_de_ai 返回 failed 报告后"} {
		if !strings.Contains(message, want) {
			t.Fatalf("stale-audit recovery missing %q: %q", want, message)
		}
	}
}

func TestRepairDeAIBatchTreatsFullyStaleBatchAsNoOp(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n门外传来两下轻敲，林逸飞把视线移向门缝。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。\n\n林逸飞没有回答——走廊的灯灭了。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	result, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"}
		]
	}`))
	if err != nil {
		t.Fatalf("fully stale batch must be a recoverable no-op: %v", err)
	}
	var payload struct {
		Changed           bool `json:"changed"`
		RepairedCount     int  `json:"repaired_count"`
		SkippedStaleCount int  `json:"skipped_stale_count"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Changed || payload.RepairedCount != 0 || payload.SkippedStaleCount != 1 {
		t.Fatalf("unexpected fully stale result: %+v", payload)
	}
	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if draft != content {
		t.Fatalf("fully stale batch changed draft: %s", draft)
	}
	if checkpoint := s.Checkpoints.LatestByStep(domain.ChapterScope(1), "de_ai_batch_repair"); checkpoint != nil {
		t.Fatal("fully stale batch must not create a repair checkpoint")
	}
}

func TestRepairDeAIBatchRejectsAmbiguousText(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n林逸飞没有回答。\n\n林逸飞没有回答。\n\n林逸飞没有回答。\n\n林逸飞没有回答。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}
	_, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1,"repairs":[{"old_string":"没有回答","new_string":"抬起头"}]}`))
	if err == nil || !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("expected exact-match precondition, got %v", err)
	}
}

func TestRepairDeAIBatchRejectsOverlappingPatchesWithoutSaving(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n林逸飞没有回答——门外有人敲了一下。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	_, err := NewRepairDeAIBatchTool(s).Execute(context.Background(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲。"},
			{"old_string":"没有回答——门外有人敲了一下","new_string":"抬眼看向门缝"}
		]
	}`))
	if err == nil || !errors.Is(err, errs.ErrToolArgs) {
		t.Fatalf("expected overlapping patch error, got %v", err)
	}
	draft, err := s.Drafts.LoadDraft(1)
	if err != nil {
		t.Fatal(err)
	}
	if draft != content {
		t.Fatalf("overlapping repairs must not persist a partial draft: %s", draft)
	}
}
