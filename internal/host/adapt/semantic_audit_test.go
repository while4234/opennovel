package adapt

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestAcceptSemanticOutputRequiresExactQuoteOffsetAndSHA(t *testing.T) {
	artifact := semanticArtifact{ID: "target-body-0001", SHA256: "sha", Text: "甲乙丙丁", TargetChapters: []int{1}}
	out := semanticModelOutput{}
	out.Findings = append(out.Findings, semanticModelFinding{Code: "continuity", Severity: "warning", Message: "test", ArtifactID: artifact.ID, ArtifactSHA256: artifact.SHA256, Quote: "乙丙", FromRune: 1, ToRune: 3, TargetChapters: []int{1}})
	run := &SemanticAuditRun{}
	artifacts := map[string]semanticArtifact{artifact.ID: artifact}
	acceptSemanticOutput(run, out, artifacts)
	if len(run.Findings) != 1 || !run.Findings[0].EvidenceVerified || run.RejectedEvidence != 0 {
		t.Fatalf("valid evidence was not accepted: %+v", run)
	}

	out.Findings[0].Quote = "甲乙"
	acceptSemanticOutput(run, out, artifacts)
	if len(run.Findings) != 1 || run.RejectedEvidence != 1 {
		t.Fatalf("offset-mismatched evidence was not rejected: %+v", run)
	}
	out.Findings[0].Quote = "乙丙"
	out.Findings[0].ArtifactSHA256 = "stale"
	acceptSemanticOutput(run, out, artifacts)
	if run.RejectedEvidence != 2 {
		t.Fatalf("stale artifact evidence was not rejected: %+v", run)
	}
}

func TestSemanticUnitWindowsAlwaysCompareSourcePlanAndBody(t *testing.T) {
	unit := semanticAuditUnit{ID: "chapter-0001", Artifacts: []semanticArtifact{
		{ID: "source-0001", SHA256: "s", Text: strings.Repeat("原", 25_000)},
		{ID: "target-plan-0001", SHA256: "p", Text: "提纲", TargetChapters: []int{1}},
		{ID: "target-body-0001", SHA256: "b", Text: strings.Repeat("改", 18_000), TargetChapters: []int{1}},
	}}
	windows := semanticUnitWindows(unit)
	if len(windows) < 2 {
		t.Fatalf("expected chunked unit, got %d windows", len(windows))
	}
	for index, window := range windows {
		seen := map[string]bool{}
		for _, chunk := range window {
			seen[chunk.ArtifactID] = true
		}
		for _, id := range []string{"source-0001", "target-plan-0001", "target-body-0001"} {
			if !seen[id] {
				t.Fatalf("window %d lacks %s: %+v", index, id, window)
			}
		}
	}
}

type semanticCaptureModel struct{ messages []agentcore.Message }

func (m *semanticCaptureModel) Generate(_ context.Context, msgs []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.messages = msgs
	return &agentcore.LLMResponse{Message: agentcore.Message{Content: []agentcore.ContentBlock{agentcore.TextBlock(`{"summary":"人物状态跨批矛盾","findings":[],"judgments":[]}`)}}}, nil
}
func (*semanticCaptureModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, context.Canceled
}
func (*semanticCaptureModel) SupportsTools() bool { return false }

func TestGlobalSynthesisPromptChecksCrossUnitContradictions(t *testing.T) {
	model := &semanticCaptureModel{}
	out, _, err := callSemanticAuditor(context.Background(), model, "global_synthesis", map[string]any{"unit_summaries": []string{"第一批人物受伤", "第二批人物无故痊愈"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Summary, "矛盾") {
		t.Fatalf("unexpected output: %+v", out)
	}
	prompt := model.messages[0].TextContent()
	if !strings.Contains(prompt, "cross-unit character state") || !strings.Contains(model.messages[1].TextContent(), "无故痊愈") {
		t.Fatalf("global comparison context missing: %s / %s", prompt, model.messages[1].TextContent())
	}
}

func TestSemanticJudgmentsCanNeverClaimVerifiedFact(t *testing.T) {
	artifact := semanticArtifact{ID: "target-body-0001", SHA256: "sha", Text: "正文"}
	out := semanticModelOutput{Judgments: []SemanticAuditJudgment{{Code: "possibly_missing", Message: "not found", VerifiedFact: true}}}
	run := &SemanticAuditRun{}
	acceptSemanticOutput(run, out, map[string]semanticArtifact{artifact.ID: artifact})
	if len(run.Judgments) != 1 || run.Judgments[0].VerifiedFact {
		t.Fatalf("absence judgment became a verified fact: %+v", run.Judgments)
	}
}

func TestPrepareSemanticAuditRequiresUnknownPriceAcknowledgement(t *testing.T) {
	st := semanticAuditTestStore(t)
	options := SemanticAuditOptions{MaxCalls: 10}
	if _, err := PrepareSemanticAudit(st, options, "p", "m"); err == nil || !strings.Contains(err.Error(), "acknowledge_unknown_price") {
		t.Fatalf("expected explicit acknowledgement, got %v", err)
	}
	options.AcknowledgeUnknownPrice = true
	if _, err := PrepareSemanticAudit(st, options, "p", "m"); err != nil {
		t.Fatalf("acknowledged prepare failed: %v", err)
	}
}

func TestResumeSemanticAuditPreservesCheckpointAndRunID(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	run := &SemanticAuditRun{Version: 1, RunID: newSemanticRunID(), Status: "interrupted", ReadOnly: true, CompletedStages: map[string]SemanticAuditStageResult{"chapter-0001/window/0000": {Stage: "unit_window", Summary: "unverified", SummarySource: "model_assessment"}}, Progress: SemanticAuditProgress{CompletedCalls: 1, CoveredRunes: 10, TotalRunes: 20}}
	if err := SaveSemanticAuditRun(st, run); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeSemanticAudit(st, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.RunID != run.RunID || resumed.Status != "queued" || len(resumed.CompletedStages) != 1 || resumed.Progress.CoveredRunes != 10 {
		t.Fatalf("checkpoint not preserved: %+v", resumed)
	}
}

func TestModelAssessmentMakesHistoryInconclusive(t *testing.T) {
	report := adaptaudit.NewModelSecondPassReport(adaptaudit.ModeChapter, adaptaudit.Scope{SourceFrom: 1, SourceTo: 1, TargetFrom: 1, TargetTo: 1}, "input", []adaptaudit.Finding{{ID: "a", Fingerprint: "fp", Code: "possibly_missing", Severity: "warning", Message: "model did not find event", Source: "model_assessment"}})
	if report.Status != "inconclusive" || report.Findings[0].Source != "model_assessment" || report.Confirmation.Required {
		t.Fatalf("assessment report is misleading: %+v", report)
	}
}

func semanticAuditTestStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "source", "source event")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{ChapterCount: 1, Chapters: []domain.AdaptationSource{source}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{Granularity: domain.AdaptationGranularityChapter, Status: domain.AdaptationPlanStatusConfirmed, Chapters: []domain.AdaptationChapterPlan{{Chapter: 1, SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "adapted event"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{TotalChapters: 1, CurrentChapter: 2, CompletedChapters: []int{1}}); err != nil {
		t.Fatal(err)
	}
	return st
}
