package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func recordCurrentConsistency(t testing.TB, s *store.Store, chapter int) {
	t.Helper()
	if _, err := s.Checkpoints.AppendArtifact(
		domain.ChapterScope(chapter), "consistency_check",
		fmt.Sprintf("drafts/%02d.draft.md", chapter),
	); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDeAIRecordsDraftDigestAndRejectsUncleanProse(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, "# 第一章\n\n## 一\n然后他没有说话——不是因为不想说，而是不能说——像一盏灯。\n"); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	tool := NewCheckDeAITool(s)
	data, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Passed     bool `json:"passed"`
		RepairPlan struct {
			Mode    string `json:"mode"`
			Batches []struct {
				ID string `json:"id"`
			} `json:"batches"`
		} `json:"repair_plan"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Fatalf("expected unclean prose to require repair: %s", data)
	}
	if result.RepairPlan.Mode != "batched" || len(result.RepairPlan.Batches) == 0 {
		t.Fatalf("expected a batched repair plan: %s", data)
	}
	audit, err := s.DeAI.LoadAudit(1)
	if err != nil || audit == nil || audit.DraftSHA256 == "" {
		t.Fatalf("audit = %+v, err=%v", audit, err)
	}
	if cp := s.Checkpoints.LatestByStep(domain.ChapterScope(1), "de_ai_check"); cp == nil {
		t.Fatal("expected de_ai_check checkpoint")
	}
}

func TestCheckDeAIRequiresCurrentConsistencyReceipt(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, "# 第一章\n\n机场初见。\n"); err != nil {
		t.Fatal(err)
	}
	tool := NewCheckDeAITool(s)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err == nil {
		t.Fatal("expected missing consistency receipt to block de-AI")
	}
	if _, err := s.Checkpoints.AppendArtifact(domain.ChapterScope(1), "consistency_check", "drafts/01.draft.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, "# 第一章\n\n商业晚宴初见。\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`)); err == nil {
		t.Fatal("expected stale consistency receipt to block de-AI")
	}
}

func TestCheckDeAIAllowsDirectRecheckAfterBoundedRepairBatch(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	const original = "# 第一章\n\n林逸飞没有回答——门外有人敲了一下。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, original); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(t.Context(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepairDeAIBatchTool(s).Execute(t.Context(), json.RawMessage(`{
		"chapter":1,
		"repairs":[
			{"old_string":"林逸飞没有回答——门外有人敲了一下。","new_string":"门外传来两下轻敲，林逸飞把视线移向门缝。"}
		]
	}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCheckDeAITool(s).Execute(t.Context(), json.RawMessage(`{"chapter":1}`)); err != nil {
		t.Fatalf("bounded de-AI repair should permit direct de-AI recheck: %v", err)
	}
}

func TestCheckDeAIReturnsCanonicalCommitContext(t *testing.T) {
	characters := []domain.Character{
		{ID: "lin_shuran", Name: "林舒然"},
		{ID: "su_jinchen", Name: "苏瑾琛"},
	}
	s := characterLoopStore(t, characters, []domain.OutlineEntry{{
		Chapter:      1,
		Title:        "机场初见",
		CoreEvent:    "苏瑾琛在机场第一次看见林舒然。",
		Hook:         "执念开始。",
		CharacterIDs: []string{"lin_shuran", "su_jinchen"},
		Scenes:       []string{"机场到达厅初见"},
	}})
	if err := s.Drafts.SaveDraft(1, "# 第一章\n\n机场到达厅里，林舒然从苏瑾琛面前经过。"); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	data, err := NewCheckDeAITool(s).Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		CommitContext chapterCommitContext `json:"commit_context"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.CommitContext.Title != "机场初见" ||
		len(result.CommitContext.CharacterIDs) != 2 ||
		result.CommitContext.CharacterIDs[0] != "lin_shuran" ||
		result.CommitContext.Characters[1] != "苏瑾琛" ||
		!slices.Contains(result.CommitContext.AllowedCharacterStateFields, "injury") ||
		result.CommitContext.DraftSHA256 == "" {
		t.Fatalf("unexpected commit context: %+v", result.CommitContext)
	}
}
