package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type stubCompletionGate struct {
	result CompletionAuditResult
	err    error
	calls  int
}

func (g *stubCompletionGate) EvaluateCompletion() (CompletionAuditResult, error) {
	g.calls++
	return g.result, g.err
}

func TestSaveFoundationCompleteBookStopsOnBlockedAudit(t *testing.T) {
	st := store.NewStore(testStoreDir(t))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowWriting}); err != nil {
		t.Fatal(err)
	}
	gate := &stubCompletionGate{result: CompletionAuditResult{
		Applicable: true, Allowed: false, Status: "fail", ReportDigest: "report-1",
	}}
	raw, err := NewSaveFoundationTool(st, gate).Execute(context.Background(), json.RawMessage(`{"type":"complete_book","content":{}}`))
	if err != nil {
		t.Fatalf("blocked completion should be a durable tool result: %v", err)
	}
	var result struct {
		BookComplete    bool                  `json:"book_complete"`
		Blocked         bool                  `json:"blocked"`
		CompletionAudit CompletionAuditResult `json:"completion_audit"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Blocked || result.BookComplete || result.CompletionAudit.ReportDigest != "report-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	progress, _ := st.Progress.Load()
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("phase=%s, want writing", progress.Phase)
	}
	if progress.CompletionAuditStatus != "fail" || progress.CompletionAuditReportDigest != "report-1" {
		t.Fatalf("completion audit state not persisted: %+v", progress)
	}
}

func TestCommitChapterApplyCompletionKeepsFinalChapterWhenAuditBlocks(t *testing.T) {
	plan := domain.AdaptationPlan{
		Status:      domain.AdaptationPlanStatusConfirmed,
		Granularity: domain.AdaptationGranularityArc,
		Chapters:    []domain.AdaptationChapterPlan{{Chapter: 1, Title: "Final"}},
	}
	st := newAdaptationToolStoreWithPlan(t, plan, []string{"source"})
	if err := st.Progress.Save(&domain.Progress{
		Phase: domain.PhaseWriting, Flow: domain.FlowWriting,
		TotalChapters: 1, CompletedChapters: []int{1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "committed final prose"); err != nil {
		t.Fatal(err)
	}
	gate := &stubCompletionGate{result: CompletionAuditResult{Applicable: true, Allowed: false, Status: "fail", ReportDigest: "report-2"}}
	tool := NewCommitChapterTool(st, gate)
	completed, audit := tool.applyCompletion(&domain.CommitResult{NextChapter: 2}, &domain.Progress{
		Phase: domain.PhaseWriting, TotalChapters: 1, CompletedChapters: []int{1},
	})
	if completed || audit == nil || audit.ReportDigest != "report-2" || gate.calls != 1 {
		t.Fatalf("completed=%v audit=%+v calls=%d", completed, audit, gate.calls)
	}
	progress, _ := st.Progress.Load()
	if progress.Phase != domain.PhaseWriting {
		t.Fatalf("phase=%s, want writing", progress.Phase)
	}
	if progress.CompletionAuditStatus != "fail" || progress.CompletionAuditReportDigest != "report-2" {
		t.Fatalf("completion audit state not persisted: %+v", progress)
	}
	body, _ := st.Drafts.LoadChapterText(1)
	if body != "committed final prose" {
		t.Fatalf("final body changed: %q", body)
	}
}
