package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// helper：构造一个处于 Writing 阶段、分层模式的 Progress。
func writingProgress(completed []int, flow domain.FlowState) *domain.Progress {
	return &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              flow,
		Layered:           true,
		CompletedChapters: completed,
	}
}

func TestRoute_NilProgress(t *testing.T) {
	if got := Route(State{Progress: nil}); got != nil {
		t.Fatalf("expected nil for nil progress, got %+v", got)
	}
}

func TestRoute_CastPromotionUsesCharacterAnalyzeThenIndependentReview(t *testing.T) {
	entry := &domain.CastEntry{Name: "Keeper", PromotionStatus: "pending"}
	analyze := Route(State{CastPromotionEntry: entry, Progress: writingProgress(nil, domain.FlowWriting)})
	if analyze == nil || analyze.Agent != "character" || !strings.Contains(analyze.Task, "save_cast_promotion_candidate") {
		t.Fatalf("analyze route = %+v", analyze)
	}
	review := Route(State{
		CastPromotionEntry: entry,
		CastPromotion: &domain.CastPromotionWorkflow{
			LedgerName: "Keeper", Status: domain.CastPromotionCandidateReady,
			CandidateDigest: "1234567890abcdef",
		},
		Progress: writingProgress(nil, domain.FlowWriting),
	})
	if review == nil || review.Agent != "character" || !strings.Contains(review.Task, "save_cast_promotion_review") {
		t.Fatalf("review route = %+v", review)
	}
	wait := Route(State{
		CastPromotionEntry: entry,
		CastPromotion: &domain.CastPromotionWorkflow{
			LedgerName: "Keeper", Status: domain.CastPromotionReviewPassed,
			CandidateDigest: "1234567890abcdef",
		},
		Progress: writingProgress(nil, domain.FlowWriting),
	})
	if wait != nil {
		t.Fatalf("review-passed promotion should await user confirmation, got %+v", wait)
	}
}

func TestRoute_AdaptationCharactersUseSharedAnalyzeAndReviewRuns(t *testing.T) {
	state := State{
		AdaptationCharacterPending: true,
		CharacterBinding: domain.CharacterCardBinding{
			InputDigest: strings.Repeat("a", 64),
		},
	}
	analyze := Route(state)
	if analyze == nil || analyze.Agent != "character" ||
		!strings.Contains(analyze.Task, `"mode":"analyze"`) ||
		!strings.Contains(analyze.Task, `"project_mode":"adaptation"`) ||
		!strings.Contains(analyze.Task, "新男主（主视角）") ||
		!strings.Contains(analyze.Task, "target_original mapping") {
		t.Fatalf("adaptation analyze route = %+v", analyze)
	}

	state.CharacterCandidate = &domain.CharacterCardCandidate{}
	state.CharacterLifecycle = &domain.CharacterCardLifecycle{
		AnalysisStatus: domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:   domain.CharacterCardReviewNotReviewed,
	}
	state.CharacterBinding.Candidate.CharacterContentDigest = strings.Repeat("b", 64)
	review := Route(state)
	if review == nil || review.Agent != "character" ||
		!strings.Contains(review.Task, `"mode":"review"`) ||
		!strings.Contains(review.Task, `"project_mode":"adaptation"`) ||
		!strings.Contains(review.Task, "source/target protagonist conflation") {
		t.Fatalf("adaptation review route = %+v", review)
	}

	state.CharacterLifecycle.ReviewStatus = domain.CharacterCardReviewPassed
	if got := Route(state); got != nil {
		t.Fatalf("passing adaptation review should wait for user confirmation, got %+v", got)
	}
}

func TestRoute_PhaseComplete(t *testing.T) {
	s := State{Progress: &domain.Progress{Phase: domain.PhaseComplete}}
	if got := Route(s); got != nil {
		t.Fatalf("expected nil at PhaseComplete, got %+v", got)
	}
}

func TestRoute_NonWritingPhasesDelegateToLLM(t *testing.T) {
	for _, phase := range []domain.Phase{domain.PhaseInit, domain.PhasePremise, domain.PhaseOutline} {
		s := State{Progress: &domain.Progress{Phase: phase}, FoundationMissing: []string{"premise"}}
		if got := Route(s); got != nil {
			t.Fatalf("phase %s should return nil, got %+v", phase, got)
		}
	}
}

func TestRoute_PendingRewritesFirst(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowRewriting)
	p.PendingRewrites = []int{3, 5}
	got := Route(State{Progress: p})
	if got == nil || got.Agent != "writer" {
		t.Fatalf("expected writer for rewrites, got %+v", got)
	}
	if got.Task != "重写第 3 章" {
		t.Errorf("expected '重写第 3 章', got %q", got.Task)
	}
	if got.Chapter != 3 {
		t.Errorf("expected Chapter=3, got %d", got.Chapter)
	}
}

func TestRoute_OutlineRepairPrecedesPendingRewrites(t *testing.T) {
	p := writingProgress([]int{1, 2, 3}, domain.FlowRewriting)
	p.PendingRewrites = []int{3}
	got := Route(State{
		Progress: p,
		OutlineRepair: &storepkg.OutlineRepairBatch{
			Volume:       1,
			Arc:          2,
			FromChapter:  3,
			ToChapter:    4,
			ChapterCount: 2,
			Duplicate: domain.OutlineDuplicate{
				Chapter:         3,
				ExistingChapter: 1,
				Title:           "鹰符潜入",
			},
		},
	})
	if got == nil || got.Agent != "architect_long" {
		t.Fatalf("expected architect_long repair before writer, got %+v", got)
	}
	if got.Chapter != 0 {
		t.Fatalf("repair dispatch should not target a writer chapter, got %d", got.Chapter)
	}
	for _, want := range []string{"repair_arc", "volume=1", "arc=2", "from_chapter=3", "to_chapter=4", "exactly 2"} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("repair task missing %q: %s", want, got.Task)
		}
	}
}

func TestRoute_PendingPolishingVerb(t *testing.T) {
	p := writingProgress([]int{1}, domain.FlowPolishing)
	p.PendingRewrites = []int{2}
	got := Route(State{Progress: p})
	if got == nil {
		t.Fatalf("expected polish verb, got %+v", got)
	}
	for _, want := range []string{"打磨第 2 章", "rewrite_brief", "edit_chapter", "局部实质改动", "草稿已包含打磨改动", "不要为了满足次数重复修改", "check_consistency", "check_de_ai", "commit_chapter"} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("polish task missing %q: %s", want, got.Task)
		}
	}
}

func TestRouteResume_PendingPolishWithChangedDraftResumesPersistedGates(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowPolishing)
	p.PendingRewrites = []int{2}
	p.InProgressChapter = 2
	got := RouteResume(State{
		Progress:                    p,
		InProgressDraftExists:       true,
		InProgressDraftDiffersFinal: true,
		InProgressConsistencyValid:  true,
		InProgressDeAIState:         writerDeAIStatePassed,
		InProgressWordCount:         3200,
	})
	if got == nil || !got.ResumeRecovery {
		t.Fatalf("expected changed polish draft to resume persisted gates, got %+v", got)
	}
	if !strings.Contains(got.Task, "check_simulation") || strings.Contains(got.Task, "edit_chapter") {
		t.Fatalf("unexpected polish recovery task: %s", got.Task)
	}
}

func TestRouteResume_PendingPolishWithoutChangedDraftRequiresPolishEdit(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowPolishing)
	p.PendingRewrites = []int{2}
	p.InProgressChapter = 2
	got := RouteResume(State{
		Progress:                   p,
		InProgressDraftExists:      true,
		InProgressConsistencyValid: true,
		InProgressDeAIState:        writerDeAIStatePassed,
	})
	if got == nil || got.ResumeRecovery {
		t.Fatalf("expected unchanged polish draft to keep full polish route, got %+v", got)
	}
	if !strings.Contains(got.Task, "edit_chapter") {
		t.Fatalf("unchanged polish draft must still require a durable edit: %s", got.Task)
	}
}

func TestRoute_ReviewingDelegatesToLLM(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowReviewing)
	if got := Route(State{Progress: p}); got != nil {
		t.Fatalf("expected nil during reviewing, got %+v", got)
	}
}

func TestRoute_SteeringDelegatesToLLM(t *testing.T) {
	p := writingProgress([]int{1}, domain.FlowSteering)
	if got := Route(State{Progress: p}); got != nil {
		t.Fatalf("expected nil during steering, got %+v", got)
	}
}

func TestRoute_ArcEndNeedsReview(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:     true,
			Volume:       1,
			Arc:          2,
			FirstChapter: 7,
			LastChapter:  10,
		},
	}
	got := Route(s)
	if got == nil || got.Agent != "editor" {
		t.Fatalf("expected editor for arc review, got %+v", got)
	}
	if got.Reason != "弧末评审未完成" {
		t.Errorf("reason mismatch: %q", got.Reason)
	}
	for _, want := range []string{"第 7-10 章", "按完整章节分批审阅", "max_total_runes=5000", "next_from", "不要把一个章节从中间切开", "单章批次", `save_review(chapter=10, scope="arc")`} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("arc review task missing %q: %s", want, got.Task)
		}
	}
}

func TestRoute_ArcEndNeedsReviewDispatchesNextBatch(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	got := Route(State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:     true,
			Volume:       1,
			Arc:          2,
			FirstChapter: 7,
			LastChapter:  10,
		},
		ArcReviewBatch: &storepkg.ArcReviewBatch{
			Volume: 1,
			Arc:    2,
			From:   7,
			To:     8,
			Index:  1,
			Total:  2,
		},
	})
	if got == nil || got.Agent != "editor" {
		t.Fatalf("expected editor for arc review batch, got %+v", got)
	}
	for _, want := range []string{"第 1/2 批", "完整章节第 7-8 章", `scope="arc_batch"`, "batch_from=7", "batch_to=8", "禁止调用 novel_context", "读取后立即调用 save_review", "不要在本轮调用 scope=\"arc\""} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("arc review batch task missing %q: %s", want, got.Task)
		}
	}
}

func TestRoute_ArcEndHasReviewNeedsSummary(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd: true,
			Volume:   1,
			Arc:      2,
		},
		HasArcReview: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "editor" || got.Reason != "弧摘要未完成" {
		t.Fatalf("expected arc summary editor call, got %+v", got)
	}
	for _, want := range []string{"save_arc_summary", `novel_context(scope="summary"`, "complete=true", "定向 read_chapter", "禁止无差别重读整弧"} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("arc summary task missing %q: %s", want, got.Task)
		}
	}
}

func TestRoute_VolumeEndNeedsVolumeSummary(t *testing.T) {
	p := writingProgress([]int{20}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 20,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:    true,
			IsVolumeEnd: true,
			Volume:      1,
			Arc:         3,
		},
		HasArcReview:  true,
		HasArcSummary: true,
	}
	got := Route(s)
	if got == nil || got.Reason != "卷摘要未完成" {
		t.Fatalf("expected volume summary request, got %+v", got)
	}
	for _, want := range []string{"save_volume_summary", `novel_context(scope="summary", volume=1)`, "complete=true", "missing_arcs", "禁止无差别逐章重读整卷"} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("volume summary task missing %q: %s", want, got.Task)
		}
	}
}

func TestRoute_NeedsArcExpansion(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			Volume:         1,
			Arc:            2,
			NextVolume:     1,
			NextArc:        3,
			NeedsExpansion: true,
		},
		HasArcReview:  true,
		HasArcSummary: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "architect_long" {
		t.Fatalf("expected architect_long for expansion, got %+v", got)
	}
	if got.Reason != "下一弧骨架待展开" {
		t.Errorf("reason mismatch: %q", got.Reason)
	}
}

func TestRoute_NeedsNewVolume(t *testing.T) {
	p := writingProgress([]int{30}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 30,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			IsVolumeEnd:    true,
			Volume:         2,
			Arc:            4,
			NeedsNewVolume: true,
		},
		HasArcReview:     true,
		HasArcSummary:    true,
		HasVolumeSummary: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "architect_long" || got.Reason != "卷末需决定追加新卷或结束全书" {
		t.Fatalf("expected append_volume/complete_book dispatch, got %+v", got)
	}
}

func TestRoute_AdaptationCompleteNeedsCompleteBook(t *testing.T) {
	p := writingProgress([]int{1, 2, 3}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 3,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			IsVolumeEnd:    true,
			Volume:         1,
			Arc:            1,
			NeedsNewVolume: true,
		},
		HasArcReview:       true,
		HasArcSummary:      true,
		HasVolumeSummary:   true,
		AdaptationActive:   true,
		AdaptationComplete: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "architect_long" {
		t.Fatalf("expected architect_long complete_book dispatch, got %+v", got)
	}
	if got.Reason != "改编计划已完成，进入完结收尾" {
		t.Fatalf("unexpected reason: %+v", got)
	}
	if !strings.Contains(got.Task, "complete_book") || !strings.Contains(got.Task, "不要 append_volume") {
		t.Fatalf("task should force complete_book only, got %q", got.Task)
	}
}

func TestRoute_AdaptationCompletionAuditBlockedStopsRedispatch(t *testing.T) {
	state := State{
		Progress:               &domain.Progress{Phase: domain.PhaseWriting, Layered: true},
		AdaptationActive:       true,
		AdaptationComplete:     true,
		CompletionAuditBlocked: true,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd: true, IsVolumeEnd: true, NeedsNewVolume: true,
			Volume: 1, Arc: 1,
		},
		HasArcReview: true, HasArcSummary: true, HasVolumeSummary: true,
	}
	if got := Route(state); got != nil {
		t.Fatalf("blocked completion audit must stop redispatch, got %+v", got)
	}
}

func TestRoute_AdaptationOutlineBlockedStopsWriterRedispatch(t *testing.T) {
	state := State{
		Progress:                  &domain.Progress{Phase: domain.PhaseWriting},
		AdaptationActive:          true,
		AdaptationOutlineBlocked:  true,
		AdaptationPlannedChapters: map[int]struct{}{13: {}},
	}
	if got := Route(state); got != nil {
		t.Fatalf("invalid adaptation outline must stop writer redispatch, got %+v", got)
	}
}

func TestRoute_AdaptationNeedsNewVolumeContinuesPlannedChapter(t *testing.T) {
	p := writingProgress([]int{99}, domain.FlowWriting)
	p.TotalChapters = 120
	s := State{
		Progress:      p,
		LastCompleted: 99,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			IsVolumeEnd:    true,
			Volume:         15,
			Arc:            1,
			NeedsNewVolume: true,
		},
		HasArcReview:     true,
		HasArcSummary:    true,
		HasVolumeSummary: true,
		AdaptationActive: true,
		AdaptationPlannedChapters: map[int]struct{}{
			100: {},
		},
	}
	got := Route(s)
	if got == nil || got.Agent != "writer" || got.Chapter != 100 {
		t.Fatalf("expected writer for next confirmed adaptation chapter, got %+v", got)
	}
	if strings.Contains(got.Task, "append_volume") || strings.Contains(got.Task, "expand_arc") {
		t.Fatalf("adaptation continuation must not request structural expansion, got %q", got.Task)
	}
}

func TestRoute_AdaptationNeedsExpansionContinuesPlannedChapter(t *testing.T) {
	p := writingProgress([]int{9}, domain.FlowWriting)
	p.TotalChapters = 20
	s := State{
		Progress:      p,
		LastCompleted: 9,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			Volume:         1,
			Arc:            2,
			NextVolume:     1,
			NextArc:        3,
			NeedsExpansion: true,
		},
		HasArcReview:     true,
		HasArcSummary:    true,
		AdaptationActive: true,
		AdaptationPlannedChapters: map[int]struct{}{
			10: {},
		},
	}
	got := Route(s)
	if got == nil || got.Agent != "writer" || got.Chapter != 10 {
		t.Fatalf("expected writer for next confirmed adaptation chapter, got %+v", got)
	}
	if strings.Contains(got.Task, "append_volume") || strings.Contains(got.Task, "expand_arc") {
		t.Fatalf("adaptation continuation must not request structural expansion, got %q", got.Task)
	}
}

func TestRoute_NormalContinue(t *testing.T) {
	p := writingProgress([]int{1, 2, 3}, domain.FlowWriting)
	p.TotalChapters = 20
	got := Route(State{Progress: p, LastCompleted: 3})
	if got == nil || got.Agent != "writer" {
		t.Fatalf("expected writer for next chapter, got %+v", got)
	}
	if got.Task != "写第 4 章" {
		t.Errorf("expected '写第 4 章', got %q", got.Task)
	}
	if got.Chapter != 4 {
		t.Errorf("expected Chapter=4, got %d", got.Chapter)
	}
}

func TestRouteResume_UsesExistingDraftValidationStage(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	state := State{
		Progress:                   p,
		LastCompleted:              4,
		InProgressDraftExists:      true,
		InProgressCheckpoint:       "plan",
		InProgressDeAIState:        writerDeAIStateStale,
		InProgressConsistencyValid: false,
		InProgressWordCount:        3300,
		InProgressWordMin:          2550,
		InProgressWordMax:          3703,
		InProgressWordBudgetValid:  true,
	}

	if normal := Route(state); normal == nil || normal.Task != "写第 5 章" {
		t.Fatalf("normal routing changed: %+v", normal)
	}
	got := RouteResume(state)
	if got == nil || got.Agent != "writer" || got.Chapter != 5 {
		t.Fatalf("expected Writer draft recovery, got %+v", got)
	}
	if !got.ResumeRecovery {
		t.Fatalf("resume instruction must retain recovery identity: %+v", got)
	}
	for _, want := range []string{
		"恢复第 5 章现有草稿",
		"checkpoint=plan",
		"de_ai=stale",
		"word_count=3300",
		"word_budget=2550-3703",
		"禁止调用 plan_chapter 或 draft_chapter",
		"禁止读取其他章节",
		`read_chapter(chapter=5, source="draft")`,
		`novel_context(chapter=5)`,
		"pending_consistency_repair",
		"禁止在修改前重复调用 check_consistency",
		"repair_de_ai_batch",
		"check_consistency",
		"只有当前草稿的一致性检查通过后才能调用 check_de_ai",
		"依次重新执行 check_consistency、check_de_ai",
		"check_de_ai.commit_context",
		"commit_chapter",
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("resume task missing %q: %s", want, got.Task)
		}
	}
	if strings.Index(got.Task, "check_consistency") > strings.Index(got.Task, "check_de_ai") {
		t.Fatalf("resume task must run consistency before de-AI: %s", got.Task)
	}
}

func TestRouteResume_UsesPersistedDeAIRepairWithoutRepeatingConsistency(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	state := State{
		Progress:                   p,
		LastCompleted:              4,
		InProgressDraftExists:      true,
		InProgressCheckpoint:       "de_ai_check",
		InProgressDeAIState:        writerDeAIStateFailed,
		InProgressConsistencyValid: true,
		InProgressWordCount:        4793,
		InProgressWordMin:          3000,
		InProgressWordMax:          6000,
		InProgressWordBudgetValid:  true,
	}

	got := RouteResume(state)
	if got == nil || got.Agent != "writer" || got.Chapter != 5 || !got.ResumeRecovery {
		t.Fatalf("expected persisted de-AI repair recovery, got %+v", got)
	}
	for _, want := range []string{
		"去AI化修订",
		"consistency_current=true",
		`read_chapter(chapter=5, source="draft")`,
		"pending_de_ai_repair",
		"若 pending_de_ai_repair 缺失",
		"只调用一次 check_de_ai(chapter=5)",
		"repair_de_ai_batch",
		"每批修订后只复检 check_de_ai",
		"去AI通过后再统一运行一次最终 check_consistency",
		"不要调用 novel_context、check_consistency、plan_chapter、draft_chapter",
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("de-AI recovery task missing %q: %s", want, got.Task)
		}
	}
}

func TestRouteResume_RechecksOnlyDeAIAfterBoundedRepairBatch(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	got := RouteResume(State{
		Progress:                   p,
		LastCompleted:              4,
		InProgressDraftExists:      true,
		InProgressCheckpoint:       "de_ai_batch_repair",
		InProgressDeAIState:        writerDeAIStateStale,
		InProgressConsistencyValid: false,
		InProgressWordCount:        3982,
		InProgressWordMin:          2000,
		InProgressWordMax:          6250,
		InProgressWordBudgetValid:  true,
	})
	if got == nil || got.Agent != "writer" || got.Chapter != 5 || !got.ResumeRecovery {
		t.Fatalf("expected bounded de-AI recheck, got %+v", got)
	}
	if !strings.Contains(got.Task, "check_de_ai(chapter=5) exactly once") {
		t.Fatalf("de-AI recheck task missing exact gate: %s", got.Task)
	}
	for _, forbidden := range []string{"Run check_consistency", "Call novel_context", "Call read_chapter"} {
		if strings.Contains(got.Task, forbidden) {
			t.Fatalf("de-AI recheck mixed forbidden work %q: %s", forbidden, got.Task)
		}
	}
}

func TestRouteResume_RunsFinalConsistencyOnceAfterDeAIPasses(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	got := RouteResume(State{
		Progress:                   p,
		LastCompleted:              4,
		InProgressDraftExists:      true,
		InProgressCheckpoint:       "de_ai_check",
		InProgressDeAIState:        writerDeAIStatePassed,
		InProgressConsistencyValid: false,
		InProgressWordCount:        3982,
		InProgressWordMin:          2000,
		InProgressWordMax:          6250,
		InProgressWordBudgetValid:  true,
	})
	if got == nil || got.Agent != "writer" || got.Chapter != 5 || !got.ResumeRecovery {
		t.Fatalf("expected final consistency recovery, got %+v", got)
	}
	for _, want := range []string{
		`read_chapter(chapter=5, source="draft") exactly once`,
		"Then run check_consistency(chapter=5) once",
		"never use MISSING_FROM_DRAFT merely because prose was absent",
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("final consistency task missing %q: %s", want, got.Task)
		}
	}
}

func TestRouteResume_UsesPassedReceiptsWithoutRepeatingExpensiveGates(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	got := RouteResume(State{
		Progress:                   p,
		LastCompleted:              4,
		InProgressDraftExists:      true,
		InProgressCheckpoint:       "de_ai_check",
		InProgressDeAIState:        writerDeAIStatePassed,
		InProgressConsistencyValid: true,
		InProgressWordCount:        3982,
		InProgressWordMin:          2000,
		InProgressWordMax:          6250,
		InProgressWordBudgetValid:  true,
	})
	if got == nil || got.Agent != "writer" || got.Chapter != 5 || !got.ResumeRecovery {
		t.Fatalf("expected passed-gate recovery, got %+v", got)
	}
	for _, want := range []string{
		"check_de_ai(chapter=5)",
		"check_simulation(chapter=5)",
		"commit_chapter",
		"禁止重复调用 novel_context、read_chapter、check_consistency",
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("passed-gate recovery task missing %q: %s", want, got.Task)
		}
	}
}

func TestRouteResume_UsesCurrentConsistencyWithoutRepeatingItForStaleDeAI(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	got := RouteResume(State{
		Progress:                   p,
		LastCompleted:              4,
		InProgressDraftExists:      true,
		InProgressCheckpoint:       "consistency_check",
		InProgressDeAIState:        writerDeAIStateStale,
		InProgressConsistencyValid: true,
		InProgressWordCount:        3982,
		InProgressWordMin:          2000,
		InProgressWordMax:          6250,
		InProgressWordBudgetValid:  true,
	})
	if got == nil || got.Agent != "writer" || got.Chapter != 5 || !got.ResumeRecovery {
		t.Fatalf("expected stale-deAI recovery, got %+v", got)
	}
	for _, want := range []string{
		"check_de_ai(chapter=5)",
		"check_simulation(chapter=5)",
		"禁止重复调用 novel_context、read_chapter、check_consistency",
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("stale-deAI recovery task missing %q: %s", want, got.Task)
		}
	}
}

func TestRouteResume_RepairsOnlyOneWordBudgetSegment(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	state := State{
		Progress:                  p,
		LastCompleted:             4,
		InProgressDraftExists:     true,
		InProgressCheckpoint:      "edit",
		InProgressWordCount:       4000,
		InProgressWordMin:         2550,
		InProgressWordMax:         3703,
		InProgressWordBudgetValid: false,
		InProgressLineCount:       130,
	}

	got := RouteResume(state)
	if got == nil || !got.ResumeRecovery || got.Agent != "writer" || got.Chapter != 5 {
		t.Fatalf("expected segmented Writer recovery, got %+v", got)
	}
	for _, want := range []string{
		`novel_context(chapter=5)`,
		`read_chapter(chapter=5, source="draft", from_line=97, to_line=130)`,
		`edit_chapter(chapter=5, budget_segment=2, edits=[...])`,
		`必须净删减至少 99 字`,
		`段落级 old_string/new_string`,
		`合计净变化已达到本段目标`,
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("segment task missing %q: %s", want, got.Task)
		}
	}
	for _, forbidden := range []string{"check_de_ai", "check_consistency"} {
		if strings.Contains(got.Task, forbidden) {
			t.Fatalf("segment task must not mix later validation %q: %s", forbidden, got.Task)
		}
	}
}

func TestRouteResume_RegeneratesSeverelyOversizedDraftFromCleanContext(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	got := RouteResume(State{
		Progress:                  p,
		LastCompleted:             4,
		InProgressDraftExists:     true,
		InProgressCheckpoint:      "word_budget_edit_segment_3",
		InProgressWordCount:       7913,
		InProgressRecommendedMin:  3273,
		InProgressRecommendedMax:  4000,
		InProgressWordMin:         3000,
		InProgressWordMax:         6000,
		InProgressWordBudgetValid: false,
		InProgressLineCount:       520,
	})
	if got == nil || !got.ResumeRecovery || got.Agent != "writer" || got.Chapter != 5 {
		t.Fatalf("expected clean regeneration recovery, got %+v", got)
	}
	for _, want := range []string{
		`超过审核安全上限 6000 字逾 10%`,
		`禁止读取、摘抄、压缩或改写旧草稿`,
		`novel_context(chapter=5)`,
		`推荐范围 3273-4000 字`,
		`明确目标为 3636 字`,
		`3000-6000 字只是写完后工具审核使用`,
		`不要调用 plan_chapter 或 read_chapter`,
		`replace_out_of_budget=true`,
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("regeneration task missing %q: %s", want, got.Task)
		}
	}
	for _, forbidden := range []string{"from_line=", "budget_segment=", "edit_chapter"} {
		if strings.Contains(got.Task, forbidden) {
			t.Fatalf("regeneration task retained segment repair %q: %s", forbidden, got.Task)
		}
	}
}

func TestRoute_RepairsNewlyPersistedOutOfBudgetDraftBySegment(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	got := Route(State{
		Progress:                  p,
		LastCompleted:             4,
		InProgressDraftExists:     true,
		InProgressCheckpoint:      "draft",
		InProgressWordCount:       4000,
		InProgressWordMin:         2545,
		InProgressWordMax:         3721,
		InProgressWordBudgetValid: false,
		InProgressLineCount:       220,
	})
	if got == nil || !got.ResumeRecovery || got.Agent != "writer" || got.Chapter != 5 {
		t.Fatalf("normal routing must segment a newly persisted oversized draft: %+v", got)
	}
	for _, want := range []string{
		`read_chapter(chapter=5, source="draft", from_line=193, to_line=220)`,
		`edit_chapter(chapter=5, budget_segment=4, edits=[...])`,
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("normal segment task missing %q: %s", want, got.Task)
		}
	}
}

func TestWriterBudgetSegmentProgressesFromEndToStart(t *testing.T) {
	cases := []struct {
		checkpoint  string
		wantSegment int
		wantFrom    int
		wantTo      int
	}{
		{checkpoint: "word_budget_edit_segment_2", wantSegment: 1, wantFrom: 49, wantTo: 96},
		{checkpoint: "word_budget_edit_segment_1", wantSegment: 0, wantFrom: 1, wantTo: 48},
		{checkpoint: "word_budget_edit_segment_0", wantSegment: 2, wantFrom: 97, wantTo: 130},
	}
	for _, tc := range cases {
		t.Run(tc.checkpoint, func(t *testing.T) {
			got := routeWriterBudgetSegment(State{
				InProgressCheckpoint: tc.checkpoint,
				InProgressWordCount:  4292,
				InProgressWordMin:    2550,
				InProgressWordMax:    3703,
				InProgressLineCount:  130,
			}, 5)
			for _, want := range []string{
				fmt.Sprintf("budget_segment=%d", tc.wantSegment),
				fmt.Sprintf("from_line=%d", tc.wantFrom),
				fmt.Sprintf("to_line=%d", tc.wantTo),
			} {
				if !strings.Contains(got.Task, want) {
					t.Fatalf("task missing %q: %s", want, got.Task)
				}
			}
		})
	}
}

func TestWriterBudgetSegmentKeepsCursorAfterValidationCheckpoint(t *testing.T) {
	got := routeWriterBudgetSegment(State{
		InProgressCheckpoint:       "de_ai_check",
		InProgressBudgetCheckpoint: "word_budget_edit_segment_6",
		InProgressWordCount:        3973,
		InProgressWordMin:          2544,
		InProgressWordMax:          3743,
		InProgressLineCount:        295,
	}, 47)
	for _, want := range []string{
		"budget_segment=5",
		"from_line=241",
		"to_line=288",
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("validation checkpoint reset recovery cursor; missing %q: %s", want, got.Task)
		}
	}
}

func TestRouteResume_DoesNotOverrideNewChapterWithoutDraft(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	got := RouteResume(State{Progress: p, LastCompleted: 4})
	if got == nil || got.Task != "写第 5 章" {
		t.Fatalf("draftless chapter must keep normal route, got %+v", got)
	}
}

func TestDispatcher_ResumeRecoverySurvivesSubagentBoundaries(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	state := State{
		Progress:                   p,
		LastCompleted:              4,
		InProgressDraftExists:      true,
		InProgressCheckpoint:       "edit",
		InProgressDeAIState:        writerDeAIStateStale,
		InProgressConsistencyValid: false,
		InProgressWordCount:        4263,
		InProgressWordMin:          2550,
		InProgressWordMax:          3703,
	}
	dispatcher := NewDispatcher(nil, nil)
	dispatcher.BeginResumeRecovery()
	first := dispatcher.route(state)
	second := dispatcher.route(state)
	for index, got := range []*Instruction{first, second} {
		if got == nil || !got.ResumeRecovery ||
			(!strings.Contains(got.Task, "恢复第 5 章现有草稿") &&
				!strings.Contains(got.Task, "干净上下文重新生成完整正文")) {
			t.Fatalf("recovery route %d lost durable constraints: %+v", index+1, got)
		}
	}
	repeated := formatDispatchMessage(second, 2)
	if strings.Contains(repeated, "允许先调 novel_context") ||
		!strings.Contains(repeated, "不得退回普通写章") {
		t.Fatalf("repeat note loosened recovery constraints: %s", repeated)
	}

	state.InProgressDraftExists = false
	normal := dispatcher.route(state)
	if normal == nil || normal.ResumeRecovery || dispatcher.resumeRecovery.Load() {
		t.Fatalf("recovery lease did not clear after draft state ended: %+v", normal)
	}
}

func TestDispatcher_DefaultRouteRecoversDurableDraft(t *testing.T) {
	p := writingProgress([]int{1, 2, 3, 4}, domain.FlowWriting)
	p.TotalChapters = 20
	p.InProgressChapter = 5
	state := State{
		Progress:                   p,
		LastCompleted:              4,
		InProgressDraftExists:      true,
		InProgressCheckpoint:       "de_ai",
		InProgressDeAIState:        writerDeAIStatePassed,
		InProgressConsistencyValid: true,
		InProgressWordCount:        4093,
		InProgressWordMin:          2000,
		InProgressWordMax:          6250,
		InProgressWordBudgetValid:  true,
	}

	got := NewDispatcher(nil, nil).route(state)
	if got == nil || !got.ResumeRecovery || got.Chapter != 5 {
		t.Fatalf("ordinary subagent boundary lost durable draft recovery: %+v", got)
	}
	if !strings.Contains(got.Task, "check_simulation") {
		t.Fatalf("ordinary boundary returned fresh writing instead of final validation recovery: %s", got.Task)
	}
}

func TestRoute_ContinuationStartsAtFirstNewChapterWithoutBaselinePostprocess(t *testing.T) {
	p := writingProgress([]int{1, 2, 3}, domain.FlowWriting)
	p.TotalChapters = 8
	got := Route(State{
		Progress:                p,
		LastCompleted:           3,
		ContinuationActive:      true,
		ContinuationBaseChapter: 3,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:    true,
			IsVolumeEnd: true,
			Volume:      1,
			Arc:         1,
		},
	})
	if got == nil || got.Agent != "writer" || got.Chapter != 4 {
		t.Fatalf("expected continuation to start at chapter 4, got %+v", got)
	}
}

func TestRoute_AdaptationStopsAtConfirmedPlanBoundary(t *testing.T) {
	p := writingProgress([]int{1, 2, 3}, domain.FlowWriting)
	p.TotalChapters = 4
	got := Route(State{
		Progress:         p,
		AdaptationActive: true,
		AdaptationPlannedChapters: map[int]struct{}{
			1: {},
			2: {},
			3: {},
		},
		AdaptationMaxChapter: 3,
	})
	if got != nil {
		t.Fatalf("adaptation router should not dispatch chapter outside confirmed plan, got %+v", got)
	}
}

func TestRoute_ArcEndNonLayeredSkipsBoundary(t *testing.T) {
	// 非 Layered 模式即使 ArcBoundary 非 nil 也不走弧末分支
	p := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           false,
		CompletedChapters: []int{10},
		TotalChapters:     20,
	}
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary:   &storepkg.ArcBoundary{IsArcEnd: true, Volume: 1, Arc: 2},
	}
	got := Route(s)
	if got == nil || got.Agent != "writer" {
		t.Fatalf("non-layered should fall through to writer, got %+v", got)
	}
}

func TestFormatMessage(t *testing.T) {
	msg := FormatMessage(&Instruction{Agent: "writer", Task: "写第 5 章", Reason: "续写"})
	for _, want := range []string{"[Host 下达指令]", "subagent(writer, \"写第 5 章\")", "agent: writer", "task: \"写第 5 章\"", "续写", "必须原样使用", "不要改写 task", "不要先调 novel_context"} {
		if !contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDispatcher_TrackRepeat(t *testing.T) {
	// 不需要真实 coordinator / store；trackRepeat 只读自己的缓存。
	d := &Dispatcher{}
	inst := &Instruction{Agent: "writer", Task: "写第 5 章", Reason: "续写"}
	if got := d.trackRepeat(inst); got != 1 {
		t.Fatalf("首次下达应计 1，got %d", got)
	}
	if got := d.trackRepeat(inst); got != 2 {
		t.Fatalf("同 Agent+Task 重复下达应计 2，got %d", got)
	}
	// Reason 不同、Agent+Task 相同时视为同一指令继续累计
	sameTaskDiffReason := &Instruction{Agent: "writer", Task: "写第 5 章", Reason: "弧末后继续"}
	if got := d.trackRepeat(sameTaskDiffReason); got != 3 {
		t.Fatalf("仅 Reason 不同应视为重复累计到 3，got %d", got)
	}
	other := &Instruction{Agent: "writer", Task: "写第 6 章", Reason: "续写"}
	if got := d.trackRepeat(other); got != 1 {
		t.Fatalf("Task 变更后应重置为 1，got %d", got)
	}
	d.ResetRepeat()
	if got := d.trackRepeat(other); got != 1 {
		t.Fatalf("ResetRepeat 后首次应计 1，got %d", got)
	}
}

func TestDispatcherAssignsFreshPlanningReviewIDPerDispatchAttempt(t *testing.T) {
	registry := tools.NewPlanningReviewRunRegistry()
	dispatcher := NewDispatcher(nil, nil, registry)
	original := &Instruction{
		Agent: "editor", Task: "audit volume three",
		PlanningReview: &tools.PlanningReviewSelector{Volume: 3},
	}
	first, err := dispatcher.authorizePlanningReviewInstruction(original)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dispatcher.authorizePlanningReviewInstruction(original)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Task, "review_id=\"planning-review-") ||
		!strings.Contains(first.Task, "next_cursor") || !strings.Contains(first.Task, "complete=true") {
		t.Fatalf("first dispatch omitted paging contract: %s", first.Task)
	}
	if first.Task == second.Task {
		t.Fatal("new Host dispatch reused the prior review_id")
	}
	if registry.ActiveRunCount() != 1 {
		t.Fatalf("new dispatch did not revoke the older selector run: active=%d", registry.ActiveRunCount())
	}
	if original.Task != "audit volume three" {
		t.Fatalf("dispatch mutated pure route instruction: %s", original.Task)
	}
	dispatcher.ResetRepeat()
	if registry.ActiveRunCount() != 0 {
		t.Fatalf("ResetRepeat retained planning reviews: active=%d", registry.ActiveRunCount())
	}
	if _, err := dispatcher.authorizePlanningReviewInstruction(original); err != nil {
		t.Fatal(err)
	}
	dispatcher.Disable()
	if registry.ActiveRunCount() != 0 {
		t.Fatalf("Disable retained planning reviews: active=%d", registry.ActiveRunCount())
	}
}

func TestDispatcherPlanningReviewMessageCarriesAuthorizedTask(t *testing.T) {
	registry := tools.NewPlanningReviewRunRegistry()
	var request *agentcore.LLMRequest
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator := agentcore.NewAgent(agentcore.WithModel(sequentialFlowTestModel(
		func(index int, captured *agentcore.LLMRequest) (*agentcore.LLMResponse, error) {
			if index == 0 {
				close(started)
				<-release
				return &agentcore.LLMResponse{Message: flowTestAssistantMsg("ready", agentcore.StopReasonStop)}, nil
			}
			request = captured
			return &agentcore.LLMResponse{Message: flowTestAssistantMsg("done", agentcore.StopReasonStop)}, nil
		},
	)))
	dispatcher := NewDispatcher(coordinator, nil, registry)
	instruction := &Instruction{
		Agent: "editor", Task: "audit volume three",
		PlanningReview: &tools.PlanningReviewSelector{Volume: 3},
	}
	if err := coordinator.Prompt(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := dispatcher.dispatchFenced(instruction, storepkg.RevisionFence{}, true); err != nil {
		t.Fatal(err)
	}
	close(release)
	coordinator.WaitForIdle()
	if request == nil || len(request.Messages) == 0 {
		t.Fatal("Dispatcher did not deliver a Coordinator user message")
	}
	reviewID, err := registry.ResolveActive(tools.PlanningReviewSelector{Volume: 3})
	if err != nil {
		t.Fatal(err)
	}
	actualMessage := request.Messages[len(request.Messages)-1].TextContent()
	for _, want := range []string{
		"subagent(editor", "audit volume three", "Host has authorized review_id", reviewID,
		"first planning_review call may omit review_id", "signed next_cursor", "complete=true",
	} {
		if !strings.Contains(actualMessage, want) {
			t.Fatalf("actual Dispatcher message omitted %q: %s", want, actualMessage)
		}
	}
}

func TestFormatDispatchMessage_RepeatNotice(t *testing.T) {
	inst := &Instruction{Agent: "writer", Task: "写第 5 章", Reason: "续写"}
	first := formatDispatchMessage(inst, 1)
	if first != FormatMessage(inst) {
		t.Fatalf("首次下达不应附加重复注记: %s", first)
	}
	third := formatDispatchMessage(inst, 3)
	for _, want := range []string{"第 3 次下达", "路由事实未变化", "novel_context", "改派"} {
		if !contains(third, want) {
			t.Errorf("重复注记缺少 %q: %s", want, third)
		}
	}
}

func TestDispatcher_OnRepeatFiresOnceAtThreshold(t *testing.T) {
	d := &Dispatcher{}
	var fired []string
	d.SetOnRepeat(func(agent, task string, n int) {
		fired = append(fired, fmt.Sprintf("%s|%s|%d", agent, task, n))
	})

	inst := &Instruction{Agent: "writer", Task: "写第 5 章"}
	for range 6 {
		d.trackRepeat(inst) // n=1..6：只在 n==3 时回调一次
	}
	if len(fired) != 1 || fired[0] != fmt.Sprintf("writer|写第 5 章|%d", repeatNotifyAt) {
		t.Fatalf("应恰好在第 %d 次触发一次，got %v", repeatNotifyAt, fired)
	}

	// 键变更后重新武装：换任务再连续 3 次 → 再触发一次
	other := &Instruction{Agent: "writer", Task: "写第 6 章"}
	for range 3 {
		d.trackRepeat(other)
	}
	if len(fired) != 2 {
		t.Fatalf("键变更后应重新武装，got %v", fired)
	}
}

func TestDispatcher_SteersAfterSuccessfulBoundaryToolBeforeNextModelCall(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := st.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	var secondReq *agentcore.LLMRequest
	var dispatcher *Dispatcher
	coordinator := agentcore.NewAgent(
		agentcore.WithModel(sequentialFlowTestModel(func(i int, req *agentcore.LLMRequest) (*agentcore.LLMResponse, error) {
			if i == 0 {
				return &agentcore.LLMResponse{Message: flowTestToolCallMsg(agentcore.ToolCall{
					ID:   "tc-subagent",
					Name: "subagent",
					Args: json.RawMessage(`{"agent":"architect_long","task":"plan"}`),
				})}, nil
			}
			secondReq = req
			return &agentcore.LLMResponse{Message: flowTestAssistantMsg("done", agentcore.StopReasonStop)}, nil
		})),
		agentcore.WithTools(agentcore.NewFuncTool("subagent", "fake subagent", map[string]any{
			"type": "object",
		}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
				return nil, err
			}
			return json.RawMessage(`"foundation_ready=true"`), nil
		})),
		agentcore.WithMiddlewares(func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
			out, err := next(ctx, call.Args)
			if err == nil && call.Name == "subagent" {
				dispatcher.Dispatch()
			}
			return out, err
		}),
	)

	dispatcher = NewDispatcher(coordinator, st)
	lease, err := st.Revisions.AcquireNormalFlow("dispatcher-test")
	if err != nil {
		t.Fatalf("acquire normal flow lease: %v", err)
	}
	defer st.Revisions.ReleaseNormalFlow(lease.Token)
	dispatcher.SetNormalFlowLease(lease)
	dispatcher.Enable()

	if err := coordinator.Prompt(context.Background(), "start"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	coordinator.WaitForIdle()

	if secondReq == nil {
		t.Fatal("expected second model request")
	}
	if len(secondReq.Messages) < 4 {
		t.Fatalf("expected tool result and Host instruction in second request, got %d messages", len(secondReq.Messages))
	}
	if result := secondReq.Messages[len(secondReq.Messages)-2]; result.Role != agentcore.RoleTool {
		t.Fatalf("expected tool result immediately before Host instruction, got %q", result.Role)
	}
	got := secondReq.Messages[len(secondReq.Messages)-1].TextContent()
	for _, want := range []string{"[Host 下达指令]", "subagent(writer", "写第 1 章"} {
		if !contains(got, want) {
			t.Fatalf("Host instruction missing %q: %s", want, got)
		}
	}
}

func TestDispatcher_FollowUpStartsNextRouteAfterResumePromptStops(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := st.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("set writing phase: %v", err)
	}

	var secondReq *agentcore.LLMRequest
	firstStarted := make(chan struct{})
	coordinator := agentcore.NewAgent(
		agentcore.WithModel(sequentialFlowTestModel(func(i int, req *agentcore.LLMRequest) (*agentcore.LLMResponse, error) {
			if i == 0 {
				close(firstStarted)
				return &agentcore.LLMResponse{Message: flowTestAssistantMsg("resume prompt acknowledged", agentcore.StopReasonStop)}, nil
			}
			secondReq = req
			return &agentcore.LLMResponse{Message: flowTestAssistantMsg("next chapter dispatched", agentcore.StopReasonStop)}, nil
		})),
	)

	dispatcher := NewDispatcher(coordinator, st)
	lease, err := st.Revisions.AcquireNormalFlow("dispatcher-follow-up-test")
	if err != nil {
		t.Fatalf("acquire normal flow lease: %v", err)
	}
	defer st.Revisions.ReleaseNormalFlow(lease.Token)
	dispatcher.SetNormalFlowLease(lease)
	dispatcher.Enable()
	if err := coordinator.Prompt(context.Background(), "resume"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-firstStarted
	if inst := Route(LoadState(st)); inst == nil {
		t.Fatalf("resume route is nil: %+v", LoadState(st))
	}
	dispatcher.DispatchFollowUp()
	coordinator.WaitForIdle()

	if secondReq == nil {
		t.Fatal("expected follow-up model request after resume prompt")
	}
	got := secondReq.Messages[len(secondReq.Messages)-1].TextContent()
	if !contains(got, "[Host") || !contains(got, "writer") || !contains(got, "1") {
		t.Fatalf("follow-up Host instruction missing: %s", got)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("load progress: %v", err)
	}
	if progress.InProgressChapter != 1 {
		t.Fatalf("in-progress chapter = %d, want 1", progress.InProgressChapter)
	}
}

func TestDispatcher_DoesNotCrossPendingNormalPlanningReview(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := st.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("set writing phase: %v", err)
	}
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusPending,
		Kind:   domain.PlanningReviewKindVolumeSplit,
	}); err != nil {
		t.Fatalf("set planning review: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	coordinator := agentcore.NewAgent(agentcore.WithModel(sequentialFlowTestModel(func(_ int, _ *agentcore.LLMRequest) (*agentcore.LLMResponse, error) {
		calls.Add(1)
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return &agentcore.LLMResponse{Message: flowTestAssistantMsg("wait for review", agentcore.StopReasonStop)}, nil
	})))
	dispatcher := NewDispatcher(coordinator, st)
	dispatcher.Enable()
	if err := coordinator.Prompt(context.Background(), "start"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-started
	dispatcher.Dispatch()
	close(release)
	coordinator.WaitForIdle()
	if got := calls.Load(); got != 1 {
		t.Fatalf("model calls = %d, pending review must not enqueue another route", got)
	}
}

type flowTestSequentialModel struct {
	fn  func(i int, req *agentcore.LLMRequest) (*agentcore.LLMResponse, error)
	idx int64
}

func sequentialFlowTestModel(fn func(i int, req *agentcore.LLMRequest) (*agentcore.LLMResponse, error)) *flowTestSequentialModel {
	return &flowTestSequentialModel{fn: fn}
}

func (m *flowTestSequentialModel) take(msgs []agentcore.Message, tools []agentcore.ToolSpec) (*agentcore.LLMResponse, error) {
	i := int(atomic.AddInt64(&m.idx, 1) - 1)
	return m.fn(i, &agentcore.LLMRequest{Messages: msgs, Tools: tools})
}

func (m *flowTestSequentialModel) Generate(_ context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.take(msgs, tools)
}

func (m *flowTestSequentialModel) GenerateStream(_ context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.take(msgs, tools)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message, StopReason: resp.Message.StopReason}
	close(ch)
	return ch, nil
}

func (m *flowTestSequentialModel) SupportsTools() bool { return true }

func flowTestAssistantMsg(text string, stop agentcore.StopReason) agentcore.Message {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: stop,
	}
}

func flowTestToolCallMsg(calls ...agentcore.ToolCall) agentcore.Message {
	blocks := make([]agentcore.ContentBlock, len(calls))
	for i, call := range calls {
		blocks[i] = agentcore.ToolCallBlock(call)
	}
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    blocks,
		StopReason: agentcore.StopReasonToolUse,
	}
}
