package flow

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/deai"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestCompletionReportAllowsOnlyPassOrLegacyInconclusive(t *testing.T) {
	cases := []struct {
		name   string
		report *adaptaudit.Report
		want   bool
	}{
		{name: "pass", report: &adaptaudit.Report{Status: "pass"}, want: true},
		{name: "fail", report: &adaptaudit.Report{Status: "fail"}, want: false},
		{name: "new inconclusive", report: &adaptaudit.Report{Status: "inconclusive"}, want: false},
		{name: "legacy inconclusive", report: &adaptaudit.Report{Status: "inconclusive", Findings: []adaptaudit.Finding{{Code: "audit_contract_unavailable"}}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := completionReportAllows(tc.report); got != tc.want {
				t.Fatalf("completionReportAllows=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadStateRoutesNewAdaptationThroughCharacterBeforeCoreCastExists(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	signature := strings.Repeat("a", 64)
	if _, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode:                 domain.CoreCastModeAdaptation,
		DraftRevision:        1,
		DraftHash:            "adaptation-draft",
		SourceSignature:      signature,
		AdaptationIntentHash: signature,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(
		domain.AdaptationPlanningStageTargetFoundationGenerating,
		-1,
	); err != nil {
		t.Fatal(err)
	}

	state := LoadState(st)
	if !state.AdaptationCharacterPending {
		t.Fatalf("adaptation character workflow was not loaded before CoreCast creation: %+v", state)
	}
	instruction := Route(state)
	if instruction == nil || instruction.Agent != "character" ||
		!strings.Contains(instruction.Task, `"project_mode":"adaptation"`) ||
		!strings.Contains(instruction.Task, `"mode":"analyze"`) {
		t.Fatalf("new adaptation routed past Character Agent: %+v", instruction)
	}
}

func TestLoadStateIncludesInProgressWriterRecoveryFacts(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	const draft = "# 第五章\n\n当前草稿已经存在，需要继续验证。"
	if err := st.Drafts.SaveDraft(5, draft); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.ChapterScope(5), "consistency_check", "drafts/05.draft.md"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeAI.SaveAudit(deai.Audit{
		Version:     deai.PolicyVersion,
		Chapter:     5,
		DraftSHA256: storepkg.TextSHA256(draft),
		Passed:      false,
	}); err != nil {
		t.Fatal(err)
	}
	budget := domain.NewWordBudget(1000, "test").WithPlannedChapters(20)
	if err := st.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.ChapterScope(5), "plan", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.ChapterScope(5), "word_budget_edit_segment_2", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.ChapterScope(5), "de_ai_check", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		CompletedChapters: []int{1, 2, 3, 4},
		InProgressChapter: 5,
		TotalChapters:     20,
	}); err != nil {
		t.Fatal(err)
	}

	state := LoadState(st)
	if !state.InProgressDraftExists || state.InProgressCheckpoint != "de_ai_check" ||
		state.InProgressBudgetCheckpoint != "word_budget_edit_segment_2" ||
		state.InProgressDeAIState != writerDeAIStateFailed || !state.InProgressConsistencyValid {
		t.Fatalf("in-progress recovery facts were not loaded: %+v", state)
	}
	if state.InProgressWordCount != len([]rune(draft)) ||
		state.InProgressLineCount != len(strings.Split(draft, "\n")) ||
		state.InProgressRecommendedMin != 45 || state.InProgressRecommendedMax != 55 ||
		state.InProgressWordMin != 30 || state.InProgressWordMax != 68 ||
		state.InProgressWordBudgetValid {
		t.Fatalf("in-progress word budget facts were not loaded: %+v", state)
	}
}

func TestLoadStateExcludesPendingPolishFromWriterBudgetRecovery(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	const draft = "completed chapter awaiting a small targeted polish"
	if err := st.Drafts.SaveDraft(5, draft); err != nil {
		t.Fatal(err)
	}
	budget := domain.NewWordBudget(1000, "test").WithPlannedChapters(20)
	if err := st.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowPolishing,
		CompletedChapters: []int{1, 2, 3, 4, 5},
		InProgressChapter: 5,
		PendingRewrites:   []int{5},
		TotalChapters:     20,
	}); err != nil {
		t.Fatal(err)
	}

	state := LoadState(st)
	if !state.InProgressDraftExists || state.InProgressWordCount != len([]rune(draft)) {
		t.Fatalf("in-progress polish draft facts were not loaded: %+v", state)
	}
	if state.InProgressWordMin != 0 || state.InProgressWordMax != 0 || state.InProgressWordBudgetValid {
		t.Fatalf("pending polish was incorrectly assigned creation budget facts: %+v", state)
	}
	got := Route(state)
	if got == nil || got.Agent != "writer" || got.Chapter != 5 || !strings.Contains(got.Task, "edit_chapter") {
		t.Fatalf("pending polish did not route directly to writer: %+v", got)
	}
}

func TestLoadStateKeepsModestDeclaredRangeOverrunOutOfTrimRecovery(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	draft := strings.Repeat("正", 6573)
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatal(err)
	}
	budget := domain.NewWordBudget(200000, "test").WithPlannedChapters(55)
	if err := st.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatal(err)
	}
	if err := st.UserRules.Save(&rules.Snapshot{
		Version: rules.SnapshotVersion,
		Status:  rules.StatusReady,
		Structured: rules.Structured{
			ChapterWords: &rules.WordRange{Min: 3000, Max: 6000},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		InProgressChapter: 1,
		TotalChapters:     55,
	}); err != nil {
		t.Fatal(err)
	}

	state := LoadState(st)
	if state.InProgressWordMin != 2000 || state.InProgressWordMax != 7500 || !state.InProgressWordBudgetValid {
		t.Fatalf("soft recommendation incorrectly triggered recovery: %+v", state)
	}
	if got := Route(state); got == nil || got.Agent != "writer" || got.ResumeRecovery {
		t.Fatalf("modest overrun should continue normal validation, got %+v", got)
	}
}

func TestLoadStateUsesArcReviewCheckpointWhenReviewChapterDiffers(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{
		{
			Index: 1,
			Arcs: []domain.ArcOutline{
				{
					Index: 1,
					Chapters: []domain.OutlineEntry{
						{Title: "one"},
						{Title: "two"},
						{Title: "three"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("save layered outline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1, 2, 3},
	}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	if _, err := st.Checkpoints.Append(domain.ArcScope(1, 1), "review", "reviews/01.json", "sha256:review"); err != nil {
		t.Fatalf("append review checkpoint: %v", err)
	}

	state := LoadState(st)

	if state.ArcBoundary == nil || !state.ArcBoundary.IsArcEnd {
		t.Fatalf("expected arc-end boundary, got %+v", state.ArcBoundary)
	}
	if !state.HasArcReview {
		t.Fatal("expected arc review to be recognized from arc-scope checkpoint")
	}
}

func TestLoadStateIncludesOutlineRepairBatch(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Chapters: []domain.OutlineEntry{
					{Title: "Shared Promise", CoreEvent: "The team enters the archive and finds the sealed ledger before dawn.", Hook: "The ledger names the missing witness."},
					{Title: "Shared Promise", CoreEvent: "The team enters the archive and finds the sealed ledger before dawn.", Hook: "The ledger names the missing witness."},
					{Title: "同题", CoreEvent: "同事件", Hook: "同钩子"},
				},
			},
			{
				Index: 2,
				Chapters: []domain.OutlineEntry{
					{Title: "同题", CoreEvent: "同事件", Hook: "同钩子"},
				},
			},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("save layered outline: %v", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("save outline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1, 2},
	}); err != nil {
		t.Fatalf("save progress: %v", err)
	}

	state := LoadState(st)

	if state.OutlineRepair == nil || !state.OutlineRepair.Repairable() {
		t.Fatalf("expected outline repair batch, got %+v", state.OutlineRepair)
	}
	if state.OutlineRepair.Volume != 1 || state.OutlineRepair.Arc != 1 {
		t.Fatalf("expected V1 A1, got %+v", state.OutlineRepair)
	}
}

func TestLoadStatePrefersPendingRepairedArcPostprocessOverLastCompleted(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Chapters: []domain.OutlineEntry{
					{Title: "一", CoreEvent: "不同事件一", Hook: "不同钩子一"},
					{Title: "二", CoreEvent: "不同事件二", Hook: "不同钩子二"},
				},
			},
			{
				Index: 2,
				Chapters: []domain.OutlineEntry{
					{Title: "三", CoreEvent: "不同事件三", Hook: "不同钩子三"},
					{Title: "四", CoreEvent: "不同事件四", Hook: "不同钩子四"},
				},
			},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("save layered outline: %v", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("save outline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1, 2, 3, 4},
		PendingArcPost: []domain.ArcPostprocessTarget{
			{Volume: 1, Arc: 1, LastChapter: 2},
		},
	}); err != nil {
		t.Fatalf("save progress: %v", err)
	}

	state := LoadState(st)

	if state.ArcBoundary == nil || state.ArcBoundary.Volume != 1 || state.ArcBoundary.Arc != 1 {
		t.Fatalf("expected pending V1 A1 boundary, got %+v", state.ArcBoundary)
	}
	if state.ArcBoundary.LastChapter != 2 {
		t.Fatalf("last chapter = %d, want 2", state.ArcBoundary.LastChapter)
	}
	got := Route(state)
	if got == nil || got.Agent != "editor" || got.Reason != "弧末评审未完成" {
		t.Fatalf("expected editor arc review for repaired arc, got %+v", got)
	}
}

func TestLoadStateIncludesAdaptationStateWithPendingArcPostprocess(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Chapters: []domain.OutlineEntry{
				{Title: "一", CoreEvent: "不同事件一", Hook: "不同钩子一"},
				{Title: "二", CoreEvent: "不同事件二", Hook: "不同钩子二"},
			},
		}},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("save layered outline: %v", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("save outline: %v", err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "一"},
			{Chapter: 2, Title: "二"},
		},
	}); err != nil {
		t.Fatalf("save adaptation plan: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1, 2},
		PendingArcPost: []domain.ArcPostprocessTarget{
			{Volume: 1, Arc: 1, LastChapter: 2},
		},
	}); err != nil {
		t.Fatalf("save progress: %v", err)
	}

	state := LoadState(st)

	if state.ArcBoundary == nil || state.ArcBoundary.LastChapter != 2 {
		t.Fatalf("expected pending arc boundary, got %+v", state.ArcBoundary)
	}
	if !state.AdaptationActive {
		t.Fatal("expected adaptation state to survive pending arc early return")
	}
	if !state.AdaptationComplete {
		t.Fatal("expected completed confirmed adaptation plan")
	}
	if state.AdaptationMaxChapter != 2 {
		t.Fatalf("adaptation max chapter = %d, want 2", state.AdaptationMaxChapter)
	}
}
