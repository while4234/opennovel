package tools

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestEnsureDeAIGateRequiresMatchingPassingAudit(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.DeAI.Enable(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n窗外的雨停了。许舟把伞靠在门边。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	commit := NewCommitChapterTool(s)
	if err := commit.ensureDeAIGate(1, content); err == nil {
		t.Fatal("missing audit should block commit")
	}
	if _, err := NewCheckDeAITool(s).Execute(t.Context(), []byte(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := commit.ensureDeAIGate(1, content); err != nil {
		t.Fatalf("passing audit should allow commit: %v", err)
	}
	if err := commit.ensureDeAIGate(1, content+"又下雨了。"); err == nil {
		t.Fatal("changed draft should invalidate audit")
	}
}

func TestEnsureDeAIGateDirectsFailedAuditToBatchRepairBeforeRewrite(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.DeAI.Enable(); err != nil {
		t.Fatal(err)
	}
	content := "# 第一章\n\n林逸飞没有回答——门外有人敲了一下。\n\n林逸飞没有回答——桌上的杯子晃了晃。\n\n林逸飞没有回答——吴宇申把文件推过来。\n\n林逸飞没有回答——窗帘动了一下。"
	if err := s.Drafts.SaveDraft(1, content); err != nil {
		t.Fatal(err)
	}
	recordCurrentConsistency(t, s, 1)
	if _, err := NewCheckDeAITool(s).Execute(t.Context(), []byte(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}

	err := NewCommitChapterTool(s).ensureDeAIGate(1, content)
	if err == nil {
		t.Fatal("failed audit should block commit")
	}
	message := err.Error()
	if !strings.Contains(message, "repair_de_ai_batch") || !strings.Contains(message, "连续两批没有改善") {
		t.Fatalf("failed audit should direct bounded repair before a conditional rewrite: %s", message)
	}
}
