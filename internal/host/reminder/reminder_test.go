package reminder

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/deai"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
}

func TestStopGuard_AllowsStopOnlyWhenComplete(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	guard := NewStopGuard(s, nil)

	// 尚未 Complete：必须阻拦 + 注入
	decision := guard(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if decision.Allow {
		t.Fatal("stop must be blocked before Phase=Complete")
	}
	if decision.InjectMessage == "" {
		t.Fatal("inject message required when blocking")
	}

	// 转 Complete：放行
	if err := s.Progress.UpdatePhase(domain.PhaseComplete); err != nil {
		t.Fatalf("update phase: %v", err)
	}
	decision = guard(context.Background(), agentcore.StopInfo{TurnIndex: 2})
	if !decision.Allow {
		t.Fatal("stop must be allowed when Phase=Complete")
	}
}

func TestStopGuard_EscalatesAfterTooManyConsecutiveBlocks(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	var blocks []string
	guard := NewStopGuard(s, func(reason string, _ int32) {
		blocks = append(blocks, reason)
	})

	for i := 0; i < maxConsecutiveBlocks; i++ {
		decision := guard(context.Background(), agentcore.StopInfo{TurnIndex: i})
		if decision.Escalate {
			t.Fatalf("escalated too early at iteration %d", i)
		}
	}
	decision := guard(context.Background(), agentcore.StopInfo{TurnIndex: maxConsecutiveBlocks})
	if !decision.Escalate {
		t.Fatalf("expected escalate after %d consecutive blocks", maxConsecutiveBlocks+1)
	}
	if len(blocks) != maxConsecutiveBlocks+1 {
		t.Fatalf("audit callback called %d times, want %d", len(blocks), maxConsecutiveBlocks+1)
	}
	if blocks[len(blocks)-1] != "escalated" {
		t.Fatalf("last audit reason should be 'escalated', got %q", blocks[len(blocks)-1])
	}
}

func TestStopGuard_DefaultBlockMessageReissuesCurrentHostRoute(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("update phase: %v", err)
	}

	decision := NewStopGuard(s, nil)(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	for _, want := range []string{"[Host 下达指令]", "subagent(writer", "不要只回复等待"} {
		if !strings.Contains(decision.InjectMessage, want) {
			t.Fatalf("inject message should reissue %q, got %q", want, decision.InjectMessage)
		}
	}
	if strings.Contains(decision.InjectMessage, "等待并执行") {
		t.Fatalf("inject message must not ask the coordinator to wait again: %q", decision.InjectMessage)
	}
}

func TestStopGuard_DefaultBlockMessageAllowsCoordinatorJudgmentWhenNoRoute(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	decision := NewStopGuard(s, nil)(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if strings.Contains(decision.InjectMessage, "[Host 下达指令]") {
		t.Fatalf("no-route inject should not tell coordinator to wait for Host, got %q", decision.InjectMessage)
	}
	if !strings.Contains(decision.InjectMessage, "裁定场景") {
		t.Fatalf("no-route inject should mention coordinator judgment, got %q", decision.InjectMessage)
	}
}

// TestSubAgentGuard_HardStopReasonEscalatesImmediately 验证：模型返回
// safety / content_filter 这类不可恢复的 provider 端拒答时，子代理 StopGuard
// 必须立即 Escalate 而不是注入催促消息。
//
// 历史背景：实测 hy3-preview:free 写第 2 章时连续 8 次 stop_reason='safety'
// 拒答；旧逻辑反复注入"必须 commit"，模型继续 safety，攒到 3 次 block 才 escalate，
// 之后 coordinator 又重派 writer 总共 3 次。每次重派都是新的 SubAgent → 缓存
// 前缀全部冷启动。修复后第一次 safety 立即 escalate，coordinator 从 LLM
// 错误消息看到不可恢复，倾向于换路径而不是重派。
//
// 注意只测 safety / content_filter：StopReasonError / StopReasonAborted 走
// agentcore loop.go 直接终止 run 的分支，根本不会调用 StopGuard，列进来反而
// 引入死代码。
func TestSubAgentGuard_HardStopReasonEscalatesImmediately(t *testing.T) {
	cases := []agentcore.StopReason{
		agentcore.StopReason("safety"),
		agentcore.StopReason("content_filter"),
	}
	for _, sr := range cases {
		t.Run(string(sr), func(t *testing.T) {
			s := newTestStore(t)
			guard := NewWriterStopGuard(s)
			info := agentcore.StopInfo{
				TurnIndex: 1,
				Message:   agentcore.Message{StopReason: sr},
			}
			d := guard(context.Background(), info)
			if !d.Escalate {
				t.Fatalf("stop_reason=%q must escalate immediately, got %#v", sr, d)
			}
			if d.InjectMessage != "" {
				t.Fatalf("stop_reason=%q must not inject any message, got %q", sr, d.InjectMessage)
			}
		})
	}
}

// TestSubAgentGuard_NormalStopStillBlocks 确保对正常 stop_reason 的拦截行为
// 不受硬错误旁路的影响——LLM 自停且没 commit 时仍然要催。
func TestSubAgentGuard_NormalStopStillBlocks(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s)
	info := agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	}
	d := guard(context.Background(), info)
	if d.Escalate {
		t.Fatal("normal stop must not escalate on first block")
	}
	if d.Allow {
		t.Fatal("normal stop must be blocked when no commit checkpoint exists")
	}
	if d.InjectMessage == "" {
		t.Fatal("normal stop must inject a follow-up message")
	}
}

func TestWriterStopGuardResetsConsecutiveBlocksAfterDurableProgress(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s)
	stop := agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	}

	for i := 0; i < subagentMaxConsecutiveBlocks; i++ {
		if decision := guard(context.Background(), stop); decision.Escalate {
			t.Fatalf("escalated before the no-progress limit at attempt %d", i+1)
		}
	}
	if _, err := s.Checkpoints.Append(
		domain.ChapterScope(1),
		"de_ai_batch_repair",
		"drafts/1.draft.md",
		"sha256:repaired",
	); err != nil {
		t.Fatalf("append repair checkpoint: %v", err)
	}

	decision := guard(context.Background(), stop)
	if decision.Escalate || decision.Allow || decision.InjectMessage == "" {
		t.Fatalf("durable Writer progress must restart blocking from one: %#v", decision)
	}
	for i := 1; i < subagentMaxConsecutiveBlocks; i++ {
		if decision = guard(context.Background(), stop); decision.Escalate {
			t.Fatalf("escalated too early after durable progress at attempt %d", i+1)
		}
	}
	if decision = guard(context.Background(), stop); !decision.Escalate {
		t.Fatalf("expected escalation after %d new no-progress blocks: %#v", subagentMaxConsecutiveBlocks+1, decision)
	}
}

func TestArchitectStopGuardAllowsRepairArcCheckpoint(t *testing.T) {
	s := newTestStore(t)
	guard := NewArchitectStopGuard(s)

	if _, err := s.Checkpoints.Append(domain.ArcScope(1, 1), "repair_arc", "layered_outline.json", "d1"); err != nil {
		t.Fatalf("append repair_arc checkpoint: %v", err)
	}

	d := guard(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	})
	if !d.Allow {
		t.Fatalf("architect repair_arc checkpoint should allow stop, got %#v", d)
	}
}

func TestArchitectStopGuardAllowsFreshFoundationRetryBoundary(t *testing.T) {
	s := newTestStore(t)
	guard := NewArchitectStopGuard(
		s,
		"call save_foundation(type=world_rules) with foundation_generation=2 and foundation_base_revision=7",
	)

	decision := guard(t.Context(), agentcore.StopInfo{
		TurnIndex: 1,
		Trigger:   agentcore.StopTriggerAfterTool,
		Message: agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.TextBlock("retry_in_fresh_context")},
		},
	})
	if !decision.Allow {
		t.Fatalf("fresh-context boundary was blocked: %+v", decision)
	}
}

func TestArchitectStopGuardStillBlocksNaturalStopDuringFoundationGeneration(t *testing.T) {
	s := newTestStore(t)
	guard := NewArchitectStopGuard(
		s,
		"call save_foundation(type=world_rules) with foundation_generation=2 and foundation_base_revision=7",
	)

	decision := guard(t.Context(), agentcore.StopInfo{
		TurnIndex: 1,
		Trigger:   agentcore.StopTriggerEndTurn,
		Message: agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.TextBlock("done without saving")},
		},
	})
	if decision.Allow || decision.InjectMessage == "" {
		t.Fatalf("natural stop was not blocked: %+v", decision)
	}
}

func TestSubAgentGuard_PreserveDetailsShortDraftRepairsBeforeCommit(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.StartChapter(1); err != nil {
		t.Fatalf("start chapter: %v", err)
	}
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityChapter,
		RewritePolicy:    domain.AdaptationRewritePreserveDetails,
		WordTolerance:    0.15,
		SourceTotalRunes: 100,
		TargetTotalRunes: 100,
		TargetMinRunes:   85,
		TargetMaxRunes:   115,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "目标章",
			SourceChapters: []int{1},
			SourceRunes:    100,
			TargetRunes:    100,
			TargetMinRunes: 85,
			TargetMaxRunes: 115,
		}},
	}); err != nil {
		t.Fatalf("save adaptation plan: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, strings.Repeat("短", 20)); err != nil {
		t.Fatalf("save draft: %v", err)
	}

	d := NewWriterStopGuard(s)(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	})
	if d.Allow || d.Escalate {
		t.Fatalf("short draft should be blocked with repair message, got %#v", d)
	}
	for _, want := range []string{"低于 preserve_details 硬区间", "不要调用 commit_chapter", `read_chapter(source="source"`, "完整章节"} {
		if !strings.Contains(d.InjectMessage, want) {
			t.Fatalf("inject message missing %q: %q", want, d.InjectMessage)
		}
	}
}

func TestSubAgentGuard_PreserveDetailsMissingEvidenceRepairsBeforeCommit(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.StartChapter(1); err != nil {
		t.Fatalf("start chapter: %v", err)
	}
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityChapter,
		RewritePolicy:    domain.AdaptationRewritePreserveDetails,
		Status:           domain.AdaptationPlanStatusConfirmed,
		WordTolerance:    0.15,
		SourceTotalRunes: 100,
		TargetTotalRunes: 100,
		TargetMinRunes:   85,
		TargetMaxRunes:   115,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:         1,
			Title:           "目标章",
			SourceChapters:  []int{1},
			SourceRunes:     100,
			TargetRunes:     100,
			TargetMinRunes:  85,
			TargetMaxRunes:  115,
			RequiredChanges: []string{"改写关系线"},
		}},
	}); err != nil {
		t.Fatalf("save adaptation plan: %v", err)
	}
	draft := strings.Repeat("正", 100)
	if err := s.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if err := s.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: store.TextSHA256(draft),
		Passed:      false,
		Issues:      []string{"adaptation_change_evidence: preserve_details with required_changes must provide change_evidence"},
	}); err != nil {
		t.Fatalf("save adaptation check: %v", err)
	}

	d := NewWriterStopGuard(s)(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	})
	if d.Allow || d.Escalate {
		t.Fatalf("missing evidence should be blocked with repair message, got %#v", d)
	}
	for _, want := range []string{"check_adaptation 未通过", "不要调用 commit_chapter", "change_evidence", "不要只写在 summary", "check_adaptation({"} {
		if !strings.Contains(d.InjectMessage, want) {
			t.Fatalf("inject message missing %q: %q", want, d.InjectMessage)
		}
	}
}

func TestWriterStopGuardRechecksConsistencyBeforeStaleDeAIBatch(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.StartChapter(1); err != nil {
		t.Fatalf("start chapter: %v", err)
	}
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityChapter,
		RewritePolicy:    domain.AdaptationRewritePreserveDetails,
		Status:           domain.AdaptationPlanStatusConfirmed,
		WordTolerance:    0.15,
		SourceTotalRunes: 100,
		TargetTotalRunes: 100,
		TargetMinRunes:   85,
		TargetMaxRunes:   115,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "目标章",
			SourceChapters: []int{1},
			SourceRunes:    100,
			TargetRunes:    100,
			TargetMinRunes: 85,
			TargetMaxRunes: 115,
		}},
	}); err != nil {
		t.Fatalf("save adaptation plan: %v", err)
	}
	if err := s.DeAI.Enable(); err != nil {
		t.Fatalf("enable de-AI: %v", err)
	}
	draft := strings.Repeat("正", 100)
	if err := s.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if err := s.DeAI.SaveAudit(deai.Audit{Chapter: 1, DraftSHA256: store.TextSHA256("旧草稿"), Passed: true}); err != nil {
		t.Fatalf("save stale de-AI audit: %v", err)
	}
	if err := s.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: store.TextSHA256(draft),
		Passed:      true,
	}); err != nil {
		t.Fatalf("save adaptation check: %v", err)
	}

	message := writerStopBlockMessage(s)
	if !strings.Contains(message, "check_consistency") || strings.Contains(message, "下一次响应必须直接调用一次 check_de_ai") {
		t.Fatalf("stale draft should recheck consistency before de-AI, got %q", message)
	}
}

func TestWriterStopGuardRequiresCurrentConsistencyBeforeDeAI(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.StartChapter(1); err != nil {
		t.Fatalf("start chapter: %v", err)
	}
	draft := "两年前机场初见，今天以同事身份入局。"
	if err := s.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}

	message := writerStopBlockMessage(s)
	for _, want := range []string{
		"check_consistency",
		`read_chapter(chapter=1, source="draft")`,
		"不得因为新 Writer 上下文未携带正文就使用 MISSING_FROM_DRAFT",
		"时间、地点、POV",
		"不要调用 novel_context",
		"arc_beat_miss",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("consistency reminder missing %q: %q", want, message)
		}
	}
}

func TestWriterStopGuardAllowsCompletedHostWordBudgetSegment(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s)
	if _, err := s.Checkpoints.Append(domain.ChapterScope(47), "word_budget_edit_segment_5", "drafts/47.draft.md", "sha256:segment"); err != nil {
		t.Fatalf("append segment checkpoint: %v", err)
	}

	decision := guard(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	})
	if !decision.Allow || decision.Escalate || decision.InjectMessage != "" {
		t.Fatalf("completed Host segment must return to router without validation reminder: %#v", decision)
	}
}

func TestWriterStopGuardAllowsOutOfBudgetDraftToReturnForHostRepair(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 1); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.StartChapter(1); err != nil {
		t.Fatalf("start chapter: %v", err)
	}
	budget := domain.NewWordBudget(100, "test").WithPlannedChapters(1)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("set word budget: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, strings.Repeat("超", 140)); err != nil {
		t.Fatalf("save over-budget draft: %v", err)
	}

	decision := NewWriterStopGuard(s)(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	})
	if !decision.Allow || decision.Escalate || decision.InjectMessage != "" {
		t.Fatalf("out-of-budget draft must return to Host-owned segment repair: %#v", decision)
	}
}

func TestWriterStopGuardDoesNotTrimSoftRecommendationOverage(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 55); err != nil {
		t.Fatalf("init progress: %v", err)
	}
	if err := s.Progress.StartChapter(1); err != nil {
		t.Fatalf("start chapter: %v", err)
	}
	budget := domain.NewWordBudget(200000, "test").WithPlannedChapters(55)
	if err := s.RunMeta.SetWordBudget(&budget); err != nil {
		t.Fatalf("set word budget: %v", err)
	}
	if err := s.UserRules.Save(&rules.Snapshot{
		Version: rules.SnapshotVersion,
		Status:  rules.StatusReady,
		Structured: rules.Structured{
			ChapterWords: &rules.WordRange{Min: 3000, Max: 6000},
		},
	}); err != nil {
		t.Fatalf("save user rules: %v", err)
	}
	if err := s.Drafts.SaveDraft(1, strings.Repeat("正", 4165)); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if writerDraftNeedsBudgetRepair(s) {
		t.Fatal("4165-word draft must proceed to quality validation instead of Host trimming")
	}
}

// TestStopGuard_NonConsecutiveTurnResetsCounter 验证：两次 block 之间 TurnIndex
// 不相邻（中间 LLM 做了 tool call 或用户 resume）时，consecutive 计数重置。
func TestStopGuard_NonConsecutiveTurnResetsCounter(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	guard := NewStopGuard(s, nil)

	for i := 0; i < maxConsecutiveBlocks; i++ {
		if d := guard(context.Background(), agentcore.StopInfo{TurnIndex: i}); d.Escalate {
			t.Fatalf("escalated too early at iteration %d", i)
		}
	}

	d := guard(context.Background(), agentcore.StopInfo{TurnIndex: maxConsecutiveBlocks + 10})
	if d.Escalate {
		t.Fatal("non-consecutive block must NOT escalate; counter should have been reset")
	}
	if d.Allow {
		t.Fatal("stop must still be blocked when Phase != Complete")
	}

	d = guard(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if d.Escalate {
		t.Fatal("resume (TurnIndex backflow) must NOT escalate")
	}
}

// TestEditorStopGuard_TaskAware 验证任务感知：被派生成弧摘要时，仅 save_review（复核）
// 不算完成，必须产出 arc_summary 才放行——封堵卷中骨架弧死循环的起点 Defect C。
func TestEditorStopGuard_TaskAware(t *testing.T) {
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	// 摘要任务 + 只存了 review → 必须阻拦（review 不满足 arc_summary 要求）。
	t.Run("summary task blocks on review only", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧摘要（save_arc_summary）")
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		if d := guard(context.Background(), normalStop); d.Allow {
			t.Fatal("summary task must NOT be satisfied by a review checkpoint")
		}
	})

	// 摘要任务 + 已存 arc_summary → 放行。
	t.Run("summary task allows on arc_summary", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧摘要（save_arc_summary）")
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "arc_summary", "summaries/arc-v05a01.json", "d1"); err != nil {
			t.Fatalf("append arc_summary: %v", err)
		}
		if d := guard(context.Background(), normalStop); !d.Allow {
			t.Fatal("summary task must be satisfied by an arc_summary checkpoint")
		}
	})

	// 评审任务 + 存了 review → 放行（默认宽松行为不变）。
	t.Run("review task allows on review", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "对第 5 卷第 1 弧做弧级评审（scope=arc）")
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		if d := guard(context.Background(), normalStop); !d.Allow {
			t.Fatal("review task must be satisfied by a review checkpoint")
		}
	})
}
