package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CommitChapterTool 提交章节：加载正文 → 保存终稿 → 生成摘要 → 更新状态 → 更新进度。
type CommitChapterTool struct {
	store          *store.Store
	completionGate CompletionGate
	simulationGate ChapterSimulationGate
}

func NewCommitChapterTool(store *store.Store, gates ...CompletionGate) *CommitChapterTool {
	return &CommitChapterTool{store: store, completionGate: completionGateFrom(gates)}
}

// ChapterSimulationGate validates the exact final draft immediately before
// commit. Implementations must not trust model-supplied mode or digests.
type ChapterSimulationGate interface {
	EnsureCurrent(context.Context, int, string) error
}

func NewCommitChapterToolWithSimulation(
	st *store.Store,
	completionGate CompletionGate,
	simulationGate ChapterSimulationGate,
) *CommitChapterTool {
	return &CommitChapterTool{
		store: st, completionGate: completionGate, simulationGate: simulationGate,
	}
}

// commitOutput 在 domain.CommitResult 之上嵌入扩展字段，保持 domain 包不依赖 rules。
// 由于嵌入字段会被 JSON marshaler 提升（promoted），序列化结果等同于扁平结构。
type commitOutput struct {
	domain.CommitResult
	RuleViolations  []rules.Violation      `json:"rule_violations,omitempty"`
	CompletionAudit *CompletionAuditResult `json:"completion_audit,omitempty"`
}

func (t *CommitChapterTool) Name() string { return "commit_chapter" }
func (t *CommitChapterTool) Description() string {
	return "提交章节终稿。加载草稿正文保存为终稿，更新时间线、伏笔、关系、角色状态和进度。" +
		"character_ids 与 characters 必须直接使用同一版 check_de_ai 返回的 commit_context，禁止凭记忆生成或沿用其他项目人物。" +
		"返回结构化事实：next_chapter / review_required / arc_end / volume_end / needs_expansion / book_complete / flow 等"
}
func (t *CommitChapterTool) Label() string { return "提交章节" }

// 写工具（跨域原子操作：草稿→终稿→摘要→进度→checkpoint），禁止并发。
func (t *CommitChapterTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *CommitChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *CommitChapterTool) Schema() map[string]any {
	timelineSchema := schema.Object(
		schema.Property("time", schema.String("故事内时间")).Required(),
		schema.Property("event", schema.String("事件描述")).Required(),
		schema.Property("characters", schema.Array("涉及角色", schema.String(""))),
	)
	foreshadowSchema := schema.Object(
		schema.Property("id", schema.String("伏笔 ID")).Required(),
		schema.Property("action", schema.Enum("操作", "plant", "advance", "resolve")).Required(),
		schema.Property("description", schema.String("伏笔描述（仅 plant 时必需）")),
	)
	relationshipSchema := schema.Object(
		schema.Property("source_character_id", schema.String("stable source character ID; preferred over name")),
		schema.Property("target_character_id", schema.String("stable target character ID; preferred over name")),
		schema.Property("character_a", schema.String("角色 A（旧 name-only 兼容）")),
		schema.Property("character_b", schema.String("角色 B（旧 name-only 兼容）")),
		schema.Property("relation", schema.String("当前关系描述")).Required(),
	)
	stateChangeSchema := schema.Object(
		schema.Property("character_id", schema.String("stable character ID for character state; preferred over entity name")),
		schema.Property("entity", schema.String("角色名或实体名（旧 name-only 兼容）")),
		schema.Property("field", schema.String("变化属性。角色状态只能逐字使用 check_de_ai.commit_context.allowed_character_state_fields 中的英文值；不要翻译、改写或尝试同义词。非角色实体才可使用其他字段。")).Required(),
		schema.Property("old_value", schema.String("变化前的值")),
		schema.Property("new_value", schema.String("变化后的值")).Required(),
		schema.Property("reason", schema.String("变化原因")),
	)
	feedbackSchema := schema.Object(
		schema.Property("deviation", schema.String("偏离大纲的描述")).Required(),
		schema.Property("suggestion", schema.String("对后续大纲的调整建议")).Required(),
	)
	feedbackSchema["description"] = "对后续大纲的建议对象；必须直接传 JSON object，不要传字符串化 JSON"
	return schema.Object(
		schema.Property("chapter", schema.Int("章节号")).Required(),
		schema.Property("summary", schema.String("本章内容摘要（200字以内）")).Required(),
		schema.Property("characters", schema.Array("本章出场角色名（旧 name-only 兼容）", schema.String(""))),
		schema.Property("character_ids", schema.Array("本章出场角色的稳定 StoryFoundation ID", schema.String(""))),
		schema.Property("key_events", schema.Array("本章关键事件", schema.String(""))).Required(),
		schema.Property("timeline_events", schema.Array("本章时间线事件", timelineSchema)),
		schema.Property("foreshadow_updates", schema.Array("伏笔操作", foreshadowSchema)),
		schema.Property("relationship_changes", schema.Array("关系变化", relationshipSchema)),
		schema.Property("state_changes", schema.Array("角色/实体状态变化。没有符合允许字段的变化时传空数组，不要猜测字段名。", stateChangeSchema)),
		schema.Property("cast_intros", schema.Array("本章首次引入且后续可能再出现的次要角色简介（不含主角及 characters.json 已有角色）", schema.Object(
			schema.Property("name", schema.String("角色名")).Required(),
			schema.Property("brief_role", schema.String("一句话定位（如：客栈老板/赌坊打手）")).Required(),
		))),
		schema.Property("hook_type", schema.Enum("章末钩子类型", "crisis", "mystery", "desire", "emotion", "choice")),
		schema.Property("dominant_strand", schema.Enum("本章主导叙事线", "quest", "fire", "constellation")),
		schema.Property("feedback", feedbackSchema),
	)
}

func (t *CommitChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter             int                        `json:"chapter"`
		Summary             string                     `json:"summary"`
		Characters          []string                   `json:"characters"`
		CharacterIDs        []string                   `json:"character_ids"`
		KeyEvents           []string                   `json:"key_events"`
		TimelineEvents      []domain.TimelineEvent     `json:"timeline_events"`
		ForeshadowUpdates   []domain.ForeshadowUpdate  `json:"foreshadow_updates"`
		RelationshipChanges []domain.RelationshipEntry `json:"relationship_changes"`
		StateChanges        []domain.StateChange       `json:"state_changes"`
		CastIntros          []domain.CastIntro         `json:"cast_intros"`
		HookType            string                     `json:"hook_type"`
		DominantStrand      string                     `json:"dominant_strand"`
		Feedback            *domain.OutlineFeedback    `json:"feedback"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if err := t.validateChapterCommitCharacterIDs(a.Chapter, a.CharacterIDs); err != nil {
		return nil, err
	}
	if err := t.validateDynamicCharacterUpdates(a.CharacterIDs, a.RelationshipChanges, a.StateChanges); err != nil {
		return nil, err
	}
	if t.store.Progress.IsChapterCompleted(a.Chapter) {
		// 清理可能残留的 PendingCommit（崩溃发生在 ProgressMarked 之后、ClearPendingCommit 之前）
		if pending, _ := t.store.Signals.LoadPendingCommit(); pending != nil && pending.Chapter == a.Chapter {
			_ = t.store.Signals.ClearPendingCommit()
		}
		// 打磨/重写路径：章节虽已完成，但仍在 pending_rewrites 中，允许覆盖并 drain 队列
		progress, _ := t.store.Progress.Load()
		if progress != nil && slices.Contains(progress.PendingRewrites, a.Chapter) {
			return t.executeRewriteCommit(
				ctx,
				a.Chapter,
				a.Summary,
				a.Characters,
				a.KeyEvents,
				a.TimelineEvents,
				a.ForeshadowUpdates,
				a.RelationshipChanges,
				a.StateChanges,
				a.CastIntros,
				a.HookType,
				a.DominantStrand,
				progress,
			)
		}
		return t.buildSkipResult(a.Chapter, progress)
	}
	existingPending, err := t.store.Signals.LoadPendingCommit()
	if err != nil {
		return nil, fmt.Errorf("load pending commit: %w: %w", errs.ErrStoreRead, err)
	}
	if existingPending != nil && existingPending.Chapter != a.Chapter {
		return nil, fmt.Errorf("存在未恢复的章节提交：第 %d 章（阶段 %s），请先恢复或重新提交该章: %w", existingPending.Chapter, existingPending.Stage, errs.ErrToolConflict)
	}
	if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
		// 队列冲突保持原样（已带 ErrToolConflict 分类）；其他 IO 错误归 Precondition。
		if errors.Is(err, errs.ErrToolConflict) {
			return nil, err
		}
		return nil, fmt.Errorf("章节当前不允许提交: %w: %w", errs.ErrToolPrecondition, err)
	}

	// 分层模式越界拦截：必须先于任何写操作，否则越界 commit 会把章节文件、摘要、
	// Progress 都改坏。boundary 复用给下方第 6b 步算弧/卷信号。
	if err := EnsureAdaptationChapterPlanned(t.store, a.Chapter); err != nil {
		return nil, err
	}

	var boundary *store.ArcBoundary
	if progress, perr := t.store.Progress.Load(); perr == nil && progress != nil && progress.Layered {
		b, bErr := t.store.Outline.CheckArcBoundary(a.Chapter)
		if bErr != nil {
			return nil, fmt.Errorf("弧边界检测失败 chapter=%d: %w: %w", a.Chapter, errs.ErrStoreRead, bErr)
		}
		if b == nil {
			return nil, fmt.Errorf(
				"第 %d 章不在分层大纲范围内：写作必须先 expand_arc 扩展弧或 append_volume 追加卷；若全书已完结请调 save_foundation type=complete_book: %w",
				a.Chapter, errs.ErrToolPrecondition)
		}
		boundary = b
	}

	// 1. 加载章节正文
	content, wordCount, err := t.store.Drafts.LoadChapterContent(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", a.Chapter, errs.ErrToolPrecondition)
	}
	if err := t.ensureDeAIGate(a.Chapter, content); err != nil {
		return nil, err
	}
	if err := t.ensureAdaptationGate(a.Chapter, content); err != nil {
		return nil, err
	}
	if err := t.ensureSimulationGate(ctx, a.Chapter, content); err != nil {
		return nil, err
	}
	if budgetRejection, err := t.checkWordBudgetGate(a.Chapter, wordCount); err != nil {
		return nil, err
	} else if budgetRejection != nil {
		return json.Marshal(budgetRejection.result(domain.FlowWriting))
	}

	now := time.Now().Format(time.RFC3339)
	pending := domain.PendingCommit{
		Chapter:        a.Chapter,
		Stage:          domain.CommitStageStarted,
		Summary:        a.Summary,
		HookType:       a.HookType,
		DominantStrand: a.DominantStrand,
		StartedAt:      now,
		UpdatedAt:      now,
	}
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("save pending commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 2. 保存终稿
	if err := t.store.Drafts.SaveFinalChapter(a.Chapter, content); err != nil {
		return nil, fmt.Errorf("save final chapter: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.CaptureManuscriptContentProvenance(a.Chapter, content); err != nil {
		return nil, fmt.Errorf("freeze manuscript content provenance: %w: %w", errs.ErrStoreWrite, err)
	}

	// 3. 保存摘要
	summary := domain.ChapterSummary{
		Chapter:      a.Chapter,
		Summary:      a.Summary,
		Characters:   a.Characters,
		CharacterIDs: a.CharacterIDs,
		KeyEvents:    a.KeyEvents,
	}
	if err := t.store.Summaries.SaveSummary(summary); err != nil {
		return nil, fmt.Errorf("save summary: %w: %w", errs.ErrStoreWrite, err)
	}

	if err := t.applyChapterFacts(
		a.Chapter,
		a.TimelineEvents,
		a.ForeshadowUpdates,
		a.RelationshipChanges,
		a.StateChanges,
		a.Characters,
		a.CastIntros,
	); err != nil {
		return nil, err
	}

	pending.Stage = domain.CommitStageStateApplied
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("update pending commit stage: %w: %w", errs.ErrStoreWrite, err)
	}

	// 5. 更新进度
	if err := t.store.Progress.MarkChapterComplete(a.Chapter, wordCount, a.HookType, a.DominantStrand); err != nil {
		return nil, fmt.Errorf("mark chapter complete: %w: %w", errs.ErrStoreWrite, err)
	}

	// 6. 判断是否需要审阅
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	completedCount := 0
	if progress != nil {
		completedCount = len(progress.CompletedChapters)
	}

	// 6b. 长篇模式弧/卷信号：boundary 已在入口前置校验，Layered 时保证非 nil
	var arcEnd, volumeEnd, needsExpansion, needsNewVolume bool
	var vol, arc, nextVol, nextArc int
	if progress != nil && progress.Layered && boundary != nil {
		arcEnd = boundary.IsArcEnd
		volumeEnd = boundary.IsVolumeEnd
		vol = boundary.Volume
		arc = boundary.Arc
		needsExpansion = boundary.NeedsExpansion
		needsNewVolume = boundary.NeedsNewVolume
		nextVol = boundary.NextVolume
		nextArc = boundary.NextArc
		_ = t.store.Progress.UpdateVolumeArc(vol, arc)
	}

	var reviewRequired bool
	var reviewReason string
	if progress != nil && progress.Layered {
		reviewRequired, reviewReason = domain.ShouldArcReview(arcEnd, volumeEnd, vol, arc)
	} else {
		reviewRequired, reviewReason = domain.ShouldReview(completedCount)
	}

	// 7. 构造结构化信号
	result := domain.CommitResult{
		Chapter:        a.Chapter,
		Committed:      true,
		WordCount:      wordCount,
		NextChapter:    a.Chapter + 1,
		ReviewRequired: reviewRequired,
		ReviewReason:   reviewReason,
		HookType:       a.HookType,
		DominantStrand: a.DominantStrand,
		Feedback:       a.Feedback,
		ArcEnd:         arcEnd,
		VolumeEnd:      volumeEnd,
		Volume:         vol,
		Arc:            arc,
		NeedsExpansion: needsExpansion,
		NeedsNewVolume: needsNewVolume,
		NextVolume:     nextVol,
		NextArc:        nextArc,
	}

	// 8. 完成态判定：非分层写完最后一章 / 分层最终卷最后一章 → MarkComplete
	completed, completionAudit := t.applyCompletion(&result, progress)
	if completed {
		result.BookComplete = true
	}
	if p, _ := t.store.Progress.Load(); p != nil {
		result.Flow = string(p.Flow)
	}

	pending.Stage = domain.CommitStageProgressMarked
	pending.Result = &result
	pending.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := t.store.Signals.SavePendingCommit(pending); err != nil {
		return nil, fmt.Errorf("update pending commit result: %w: %w", errs.ErrStoreWrite, err)
	}

	// 9. 清除进度中间状态
	if err := t.store.Progress.ClearInProgress(); err != nil {
		return nil, fmt.Errorf("clear in-progress: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Signals.ClearPendingCommit(); err != nil {
		return nil, fmt.Errorf("clear pending commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 10. 追加 checkpoint
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(a.Chapter), "commit",
		fmt.Sprintf("chapters/%02d.md", a.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 11. 机械规则检查（仅返事实，不阻断）
	violations := t.checkRules(content, wordCount)
	return json.Marshal(commitOutput{CommitResult: result, RuleViolations: violations, CompletionAudit: completionAudit})
}

func (t *CommitChapterTool) applyChapterFacts(
	chapter int,
	timelineEvents []domain.TimelineEvent,
	foreshadowUpdates []domain.ForeshadowUpdate,
	relationshipChanges []domain.RelationshipEntry,
	stateChanges []domain.StateChange,
	characters []string,
	castIntros []domain.CastIntro,
) error {
	if len(timelineEvents) > 0 {
		for i := range timelineEvents {
			timelineEvents[i].Chapter = chapter
		}
		if err := t.store.World.AppendTimelineEvents(timelineEvents); err != nil {
			return fmt.Errorf("append timeline: %w: %w", errs.ErrStoreWrite, err)
		}
	}
	if len(foreshadowUpdates) > 0 {
		if err := t.store.World.UpdateForeshadow(chapter, foreshadowUpdates); err != nil {
			return fmt.Errorf("update foreshadow: %w: %w", errs.ErrStoreWrite, err)
		}
	}
	if len(relationshipChanges) > 0 {
		for i := range relationshipChanges {
			relationshipChanges[i].Chapter = chapter
		}
		if err := t.store.World.UpdateRelationships(relationshipChanges); err != nil {
			return fmt.Errorf("update relationships: %w: %w", errs.ErrStoreWrite, err)
		}
	}
	if len(stateChanges) > 0 {
		for i := range stateChanges {
			stateChanges[i].Chapter = chapter
		}
		if err := t.store.World.AppendStateChanges(stateChanges); err != nil {
			return fmt.Errorf("append state changes: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// Cast ledger is secondary recall data. A write failure should not block a
	// chapter commit because later commits can refresh recent appearances.
	if len(characters) > 0 {
		coreNames := loadCoreCharacterNameSet(t.store)
		if err := t.store.Cast.MergeAppearances(chapter, characters, castIntros, coreNames); err != nil {
			slog.Warn("配角名册累加失败，跳过", "module", "commit", "chapter", chapter, "err", err)
		}
	}
	return nil
}

func (t *CommitChapterTool) ensureAdaptationGate(chapter int, content string) error {
	if !t.store.Adaptation.Active() {
		return nil
	}
	plan, err := t.store.Adaptation.LoadPlan()
	if err != nil {
		return fmt.Errorf("load adaptation plan: %w: %w", errs.ErrStoreRead, err)
	}
	if plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return fmt.Errorf("改编项目提交被拒：改编计划尚未确认: %w", errs.ErrToolPrecondition)
	}
	if _, repairErr := t.store.Adaptation.RepairLegacyArcChapterBudgetDensity(plan); repairErr != nil {
		return fmt.Errorf("改编项目提交被拒：创作前预算专项修复未完成，最终确定性兜底也失败：%w: %w: %w", errs.ErrStoreWrite, repairErr, errs.ErrToolPrecondition)
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, chapter)
	if !ok {
		return fmt.Errorf("改编项目提交被拒：confirmed plan 中没有第 %d 章: %w", chapter, errs.ErrToolPrecondition)
	}
	if issues := adaptationPlanContractIssues(plan, chapter); len(issues) > 0 {
		return fmt.Errorf("改编项目提交被拒：上游大纲契约无效，禁止把问题留给正文修复：%s: %w", issues[0], errs.ErrToolPrecondition)
	}
	wordCount := len([]rune(content))
	if issues := adaptationWordContractIssues(t.store, plan, chapterPlan, chapter, wordCount); len(issues) > 0 {
		contract := buildAdaptationWordContract(t.store, plan, chapterPlan, chapter, wordCount)
		repair := adaptationWordContractRepairStep(contract, issues, chapter)
		if repair != "" {
			return fmt.Errorf("改编项目提交被拒：%s。%s: %w", issues[0], repair, errs.ErrToolPrecondition)
		}
		return fmt.Errorf("改编项目提交被拒：%s: %w", issues[0], errs.ErrToolPrecondition)
	}
	if issues := adaptationDraftQualityIssues(t.store, plan, chapterPlan, chapter, content); len(issues) > 0 {
		if repair := adaptationQualityRepairStep(issues, chapter); repair != "" {
			return fmt.Errorf("改编项目提交被拒：%s。%s: %w", issues[0], repair, errs.ErrToolPrecondition)
		}
		return fmt.Errorf("改编项目提交被拒：%s: %w", issues[0], errs.ErrToolPrecondition)
	}
	digest := store.TextSHA256(content)
	passed, check, err := t.store.Adaptation.HasPassingCheck(chapter, digest)
	if err != nil {
		return fmt.Errorf("load adaptation check: %w: %w", errs.ErrStoreRead, err)
	}
	if passed {
		if issues := adaptationChangeEvidenceIssues(plan, chapterPlan, check.ChangeEvidence); len(issues) > 0 {
			if repair := adaptationQualityRepairStep(issues, chapter); repair != "" {
				return fmt.Errorf("adaptation commit rejected: %s. %s: %w", issues[0], repair, errs.ErrToolPrecondition)
			}
			return fmt.Errorf("adaptation commit rejected: %s: %w", issues[0], errs.ErrToolPrecondition)
		}
		return nil
	}
	switch {
	case check == nil:
		return fmt.Errorf("改编项目提交被拒：第 %d 章尚未通过 check_adaptation，请先对照原文 source refs 和改编契约校验草稿: %w", chapter, errs.ErrToolPrecondition)
	case check.DraftSHA256 != digest:
		return fmt.Errorf("改编项目提交被拒：第 %d 章通过校验的草稿 digest=%s，但当前草稿 digest=%s。请重新调用 check_adaptation: %w", chapter, check.DraftSHA256, digest, errs.ErrToolPrecondition)
	default:
		return fmt.Errorf("改编项目提交被拒：第 %d 章最近一次 check_adaptation 未通过，issues=%v: %w", chapter, check.Issues, errs.ErrToolPrecondition)
	}
}

// ensureDeAIGate binds commit to the exact draft version that passed the
// dedicated post-writing pass. Unlike generic review, this gate is local to a
// chapter and can be rerun immediately after a targeted paragraph rewrite.
func (t *CommitChapterTool) ensureDeAIGate(chapter int, content string) error {
	if t == nil || t.store == nil || t.store.DeAI == nil {
		return nil
	}
	enabled, err := t.store.DeAI.Enabled()
	if err != nil {
		return fmt.Errorf("load de-AI policy: %w: %w", errs.ErrStoreRead, err)
	}
	if !enabled {
		return nil
	}
	consistency := t.store.Checkpoints.LatestByStep(domain.ChapterScope(chapter), "consistency_check")
	digest := store.TextSHA256(content)
	if consistency == nil {
		return fmt.Errorf("第 %d 章尚未完成一致性审核。先调用 novel_context、read_chapter 和 check_consistency，逐场景核对时间、地点、视角、人物、事件顺序与不可逆结果，再执行去AI化和提交: %w", chapter, errs.ErrToolPrecondition)
	}
	if consistency.Digest != "sha256:"+digest {
		return fmt.Errorf("第 %d 章在一致性审核后已修改，旧审核不再适用。请对当前草稿重新调用 check_consistency；通过后再执行 check_de_ai 和 commit_chapter: %w", chapter, errs.ErrToolPrecondition)
	}
	audit, err := t.store.DeAI.LoadAudit(chapter)
	if err != nil {
		return fmt.Errorf("load de-AI audit: %w: %w", errs.ErrStoreRead, err)
	}
	if audit == nil {
		return fmt.Errorf("第 %d 章尚未完成独立去AI化审校。先调用 check_de_ai；如有问题，按报告做段落级修复并在最终草稿上重新检查: %w", chapter, errs.ErrToolPrecondition)
	}
	if audit.DraftSHA256 != digest {
		return fmt.Errorf("第 %d 章在去AI化审校后已修改，旧审校不再适用。先重新调用 check_de_ai，再提交: %w", chapter, errs.ErrToolPrecondition)
	}
	if !audit.Passed {
		repair := audit.Report.RepairSummary()
		if repair == "" {
			repair = "按去AI化报告进行段落级修复"
		}
		next := "先调用 check_de_ai 查看 repair_plan，再按报告中的首个类别处理 1-8 处精确原文。"
		if batches := audit.Report.RepairPlan().Batches; len(batches) > 0 {
			first := batches[0]
			next = fmt.Sprintf("先处理“%s”批次：用 repair_de_ai_batch 修订 %d 处以内的 examples 原文。", first.Label, first.SuggestedEdits)
		}
		return fmt.Errorf("第 %d 章未通过独立去AI化审校：%s。%s 每批落盘后立即重跑 check_de_ai；仍有 repair finding 才进入下一类别。只有连续两批没有改善，或剧情因果、人物、篇幅结构也需要调整时，才回读全文并用 draft_chapter(mode=write)。通过后重跑 check_consistency、改编项目的 check_adaptation（如适用），再 commit_chapter: %w", chapter, repair, next, errs.ErrToolPrecondition)
	}
	return nil
}

func (t *CommitChapterTool) ensureSimulationGate(ctx context.Context, chapter int, content string) error {
	if t == nil || t.simulationGate == nil {
		return nil
	}
	return t.simulationGate.EnsureCurrent(ctx, chapter, content)
}

// checkRules 对章节正文做机械检查：内置产品底线 Lint（机制残留，始终执行）
// + 用户规则 Check（读本书快照的 structured；快照缺失退到内置默认，保证机械底线始终在）。
type wordBudgetGateRejection struct {
	chapter              int
	wordCount            int
	direction            string
	minWords             int
	maxWords             int
	targetTotalWords     int
	completedWords       int
	remainingTargetWords int
	remainingChapters    int
}

func (r wordBudgetGateRejection) message() string {
	return fmt.Sprintf(
		"普通创作字数预算拒绝提交：第 %d 章当前 %d 字，%s预算区间 %d-%d 字。全书目标 %d 字，已完成 %d 字，剩余目标 %d 字，剩余章节 %d。",
		r.chapter, r.wordCount, r.direction, r.minWords, r.maxWords,
		r.targetTotalWords, r.completedWords, r.remainingTargetWords, r.remainingChapters,
	)
}

func (r wordBudgetGateRejection) result(flow domain.FlowState) map[string]any {
	_ = flow
	nextStep := fmt.Sprintf(
		"不要再次调用 commit_chapter、read_chapter、edit_chapter 或 draft_chapter，也不要整章重写。当前完整草稿已经保留，但不在 %d-%d 字预算内；立即结束本轮，由 Host 按行段逐段派发局部修复，保留关键情节、人物选择、情感落点和章末钩子。进入区间后，Host 会在同一草稿上重新派发完整质量校验。",
		r.minWords, r.maxWords,
	)
	return map[string]any{
		"committed":            false,
		"chapter":              r.chapter,
		"word_count":           r.wordCount,
		"word_budget_rejected": true,
		"reason":               r.message(),
		"word_budget": map[string]any{
			"min_words":              r.minWords,
			"max_words":              r.maxWords,
			"target_total_words":     r.targetTotalWords,
			"completed_words":        r.completedWords,
			"remaining_target_words": r.remainingTargetWords,
			"remaining_chapters":     r.remainingChapters,
		},
		"next_step": nextStep,
	}
}

func (t *CommitChapterTool) checkWordBudgetGate(chapter int, wordCount int) (*wordBudgetGateRejection, error) {
	meta, err := t.store.RunMeta.Load()
	if err != nil {
		return nil, fmt.Errorf("load word budget: %w: %w", errs.ErrStoreRead, err)
	}
	if meta == nil || meta.WordBudget == nil || meta.WordBudget.TargetTotalWords <= 0 {
		return nil, nil
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress for word budget: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil || progress.Flow == domain.FlowPolishing || len(progress.PendingRewrites) > 0 {
		return nil, nil
	}
	runtime, policy, policyOK, policyErr := t.store.ChapterWordBudgetPolicy(progress, chapter)
	if policyErr != nil {
		return nil, fmt.Errorf("resolve chapter word budget: %w: %w", errs.ErrStoreRead, policyErr)
	}
	if !policyOK {
		return nil, nil
	}
	minWords := policy.HardMinWords
	maxWords := policy.HardMaxWords
	if policy.WithinHardRange(wordCount) {
		return nil, nil
	}
	direction := "低于"
	if wordCount > maxWords {
		direction = "超过"
	}
	return &wordBudgetGateRejection{
		chapter:              chapter,
		wordCount:            wordCount,
		direction:            direction,
		minWords:             minWords,
		maxWords:             maxWords,
		targetTotalWords:     runtime.Target.TargetTotalWords,
		completedWords:       runtime.Progress.CompletedWords,
		remainingTargetWords: runtime.Remaining.TargetWords,
		remainingChapters:    runtime.Remaining.Chapters,
	}, nil
}

func (t *CommitChapterTool) checkRules(text string, wordCount int) []rules.Violation {
	violations := rules.Lint(text)
	structured := rules.SystemDefaults().Structured
	if snap, err := t.store.UserRules.Load(); err == nil && snap != nil {
		structured = snap.Structured
	}
	return append(violations, rules.Check(text, wordCount, structured)...)
}

// executeRewriteCommit 处理打磨/重写章节的提交：覆盖终稿与摘要、更新字数、drain 队列。
// 跳过所有世界状态追加（timeline / foreshadow / relationship / state_changes）与弧边界检测，
// 这些已在章节原始提交时应用。
func (t *CommitChapterTool) executeRewriteCommit(
	ctx context.Context,
	chapter int,
	summary string,
	characters, keyEvents []string,
	timelineEvents []domain.TimelineEvent,
	foreshadowUpdates []domain.ForeshadowUpdate,
	relationshipChanges []domain.RelationshipEntry,
	stateChanges []domain.StateChange,
	castIntros []domain.CastIntro,
	hookType, dominantStrand string,
	progress *domain.Progress,
) (json.RawMessage, error) {
	// 1. 加载打磨后的正文
	content, wordCount, err := t.store.Drafts.LoadChapterContent(chapter)
	if err != nil {
		return nil, fmt.Errorf("rewrite: load chapter content: %w: %w", errs.ErrStoreRead, err)
	}
	if content == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", chapter, errs.ErrToolPrecondition)
	}
	if err := t.ensureDeAIGate(chapter, content); err != nil {
		return nil, err
	}

	// 2. drafts 与现终稿完全相同，说明 Writer 尚未落实审核意见。
	// 打磨是正常的可恢复控制流：返回结构化拒绝，要求基于 rewrite_brief
	// 做局部 edit_chapter，避免把一次过早提交记录成运行错误或诱发整章重写。
	// 显式重写仍维持硬失败，要求写入一份完整的新正文。
	existingFinal, _ := t.store.Drafts.LoadChapterText(chapter)
	recreateChapterFacts := existingFinal == ""
	if existingFinal != "" && existingFinal == content {
		if progress != nil && progress.Flow == domain.FlowPolishing {
			return json.Marshal(map[string]any{
				"chapter":         chapter,
				"committed":       false,
				"unchanged_draft": true,
				"reason":          fmt.Sprintf("第 %d 章草稿与终稿完全相同，审核意见尚未落实。", chapter),
				"next_step": fmt.Sprintf(
					"不要再次调用 commit_chapter，也不要整章重写。回看 novel_context.rewrite_brief 与当前完整草稿，使用 edit_chapter 对第 %d 章做至少一处有审核依据的局部实质改动；不要在 check_de_ai 已通过时调用 repair_de_ai_batch。改后 read_chapter(source=\"draft\") 回读，并在同一版草稿上重新通过 check_consistency、check_de_ai，再提交。",
					chapter,
				),
			})
		}
		return nil, fmt.Errorf("第 %d 章 drafts 与 chapters 内容完全相同，未检测到重写改动。请先调 draft_chapter(mode=write, chapter=%d) 写入重写后的完整新正文，再 commit_chapter: %w",
			chapter, chapter, errs.ErrToolPrecondition)
	}
	if err := t.ensureAdaptationGate(chapter, content); err != nil {
		return nil, err
	}
	if err := t.ensureSimulationGate(ctx, chapter, content); err != nil {
		return nil, err
	}
	if budgetRejection, err := t.checkWordBudgetGate(chapter, wordCount); err != nil {
		return nil, err
	} else if budgetRejection != nil {
		return json.Marshal(budgetRejection.result(progress.Flow))
	}

	// 3. 覆盖终稿
	if err := t.store.Drafts.SaveFinalChapter(chapter, content); err != nil {
		return nil, fmt.Errorf("rewrite: save final chapter: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.CaptureManuscriptContentProvenance(chapter, content); err != nil {
		return nil, fmt.Errorf("rewrite: freeze manuscript content provenance: %w: %w", errs.ErrStoreWrite, err)
	}

	// 3. 覆盖摘要
	if err := t.store.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    chapter,
		Summary:    summary,
		Characters: characters,
		KeyEvents:  keyEvents,
	}); err != nil {
		return nil, fmt.Errorf("rewrite: save summary: %w: %w", errs.ErrStoreWrite, err)
	}

	if recreateChapterFacts {
		if err := t.applyChapterFacts(chapter, timelineEvents, foreshadowUpdates, relationshipChanges, stateChanges, characters, castIntros); err != nil {
			return nil, err
		}
	}

	// 4. 更新字数（MarkChapterComplete 对已完成章节是幂等的：replaces word count, slice.Contains 防止重复入队）
	if err := t.store.Progress.MarkChapterComplete(chapter, wordCount, hookType, dominantStrand); err != nil {
		return nil, fmt.Errorf("rewrite: update word count: %w: %w", errs.ErrStoreWrite, err)
	}

	// 5. Drain 待处理队列；队列空时 CompleteRewrite 会自动把 flow 切回 writing
	if err := t.store.Progress.CompleteRewrite(chapter); err != nil {
		return nil, fmt.Errorf("rewrite: complete rewrite: %w: %w", errs.ErrStoreWrite, err)
	}

	// 6. Checkpoint
	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(chapter), "commit",
		fmt.Sprintf("chapters/%02d.md", chapter),
	); err != nil {
		return nil, fmt.Errorf("rewrite: checkpoint commit: %w: %w", errs.ErrStoreWrite, err)
	}

	// 7. 读取 drain 后的 Progress 快照，作为事实返回
	mode := "rewrite"
	if progress.Flow == domain.FlowPolishing {
		mode = "polish"
	}
	latest, _ := t.store.Progress.Load()
	remaining := []int{}
	nextChapter := chapter + 1
	flow := string(domain.FlowWriting)
	if latest != nil {
		remaining = append(remaining, latest.PendingRewrites...)
		nextChapter = latest.NextChapter()
		flow = string(latest.Flow)
	}
	drained := len(remaining) == 0

	// 队列清空后再判完结：返工提交不经过主路径 applyCompletion，完结只能在此触发。
	//   - 分层 + 正向写作：用质量级 layeredBookComplete（要求线索收束），未满足让位架构师。
	//   - 分层 + reopen 返工（ReopenedFromComplete）：返工只改已有章、不增减结构，按结构完整
	//     即重新完结——若因返工扰动了某条线索就卡在 writing，终卷末会落到越界续写死循环。
	//   - 非分层：写满 TotalChapters 即完结（返工不增减章数，原本就满）。
	bookComplete := false
	var completionAudit *CompletionAuditResult
	if drained && latest != nil {
		reComplete := false
		adaptationCompletion := t.store.Adaptation.Active()
		switch {
		case t.adaptationPlanComplete(latest):
			reComplete = true
		case latest.Layered && latest.ReopenedFromComplete:
			reComplete = t.layeredStructurallyComplete(latest)
		case latest.Layered:
			reComplete = t.layeredBookComplete(latest)
		default:
			reComplete = latest.TotalChapters > 0 && len(latest.CompletedChapters) >= latest.TotalChapters
		}
		auditAllowed := true
		if reComplete && adaptationCompletion {
			auditAllowed, completionAudit = t.completionAuditAllows()
		}
		if reComplete && auditAllowed {
			if cerr := t.store.Progress.MarkComplete(); cerr == nil {
				bookComplete = true
				if p, _ := t.store.Progress.Load(); p != nil {
					flow = string(p.Flow)
				}
			}
		}
	}

	// 同主路径：rewrite/polish 也做机械检查并附 rule_violations
	violations := t.checkRules(content, wordCount)
	return json.Marshal(map[string]any{
		"chapter":          chapter,
		"rewritten":        true,
		"mode":             mode,
		"word_count":       wordCount,
		"remaining_queue":  remaining,
		"queue_drained":    drained,
		"next_chapter":     nextChapter,
		"flow":             flow,
		"book_complete":    bookComplete,
		"completion_audit": completionAudit,
		"rule_violations":  violations,
	})
}

// buildSkipResult 为"章节已完成的重复提交"构造与正常 commit 对齐的事实返回。
// 协调者据此做后续决策（writer/editor/architect 派发），而不会因为拿到 prose 提示而幻觉。
func (t *CommitChapterTool) buildSkipResult(chapter int, progress *domain.Progress) (json.RawMessage, error) {
	_, wordCount, _ := t.store.Drafts.LoadChapterContent(chapter)

	result := domain.CommitResult{
		Chapter:     chapter,
		Committed:   true,
		WordCount:   wordCount,
		NextChapter: chapter + 1,
	}

	if progress != nil && progress.Layered {
		if boundary, _ := t.store.Outline.CheckArcBoundary(chapter); boundary != nil {
			result.ArcEnd = boundary.IsArcEnd
			result.VolumeEnd = boundary.IsVolumeEnd
			result.Volume = boundary.Volume
			result.Arc = boundary.Arc
			result.NeedsExpansion = boundary.NeedsExpansion
			result.NeedsNewVolume = boundary.NeedsNewVolume
			result.NextVolume = boundary.NextVolume
			result.NextArc = boundary.NextArc
		}
		result.ReviewRequired, result.ReviewReason = domain.ShouldArcReview(result.ArcEnd, result.VolumeEnd, result.Volume, result.Arc)
	} else if progress != nil {
		result.ReviewRequired, result.ReviewReason = domain.ShouldReview(len(progress.CompletedChapters))
	}

	if progress != nil {
		if progress.Phase == domain.PhaseComplete {
			result.BookComplete = true
		}
		result.Flow = string(progress.Flow)
	}

	return json.Marshal(result)
}

// loadCoreCharacterNameSet 加载 characters.json 中已有的角色名集合（含别名）。
// 用作 cast_ledger 的"已知核心"过滤集——核心角色不进次要名册。
// 加载失败时返回 nil（merge 时所有 characters 都进 ledger，可接受）。
func loadCoreCharacterNameSet(s *store.Store) map[string]bool {
	chars, err := s.Characters.Load()
	if err != nil || len(chars) == 0 {
		return nil
	}
	set := make(map[string]bool, len(chars)*2)
	for _, c := range chars {
		if c.Name != "" {
			set[c.Name] = true
		}
		for _, alias := range c.Aliases {
			if alias != "" {
				set[alias] = true
			}
		}
	}
	return set
}

// applyCompletion 判断本次 commit 是否使全书完结，若是则 MarkComplete 并返回 true。
//   - 非分层：写完约定总章数即完结。
//   - 分层：架构师显式 save_foundation type=complete_book 是主路径；这里再加一道
//     确定性兜底——当全书已客观满足完结条件（见 layeredBookComplete）时自动收尾。
//     防止模型在终点既不 append_volume 也不 complete_book，导致"写手裸跑越界章节 →
//     越界守卫拦截 → 反复重试"的 livelock（《凡骨》ch204..347 案例的根因）。
func (t *CommitChapterTool) applyCompletion(result *domain.CommitResult, progress *domain.Progress) (bool, *CompletionAuditResult) {
	if result == nil || progress == nil {
		return false, nil
	}
	if result.ReviewRequired {
		return false, nil
	}
	if t.adaptationPlanComplete(progress) {
		allowed, audit := t.completionAuditAllows()
		if !allowed {
			return false, audit
		}
		return t.markCompleteAfterRevalidation(), audit
	}
	if progress.Layered {
		if t.layeredBookComplete(progress) {
			if t.store.Adaptation.Active() {
				allowed, audit := t.completionAuditAllows()
				if !allowed {
					return false, audit
				}
				return t.markCompleteAfterRevalidation(), audit
			}
			return t.markCompleteAfterRevalidation(), nil
		}
		return false, nil
	}
	if progress.TotalChapters > 0 && result.NextChapter > progress.TotalChapters {
		if t.store.Adaptation.Active() {
			allowed, audit := t.completionAuditAllows()
			if !allowed {
				return false, audit
			}
			return t.markCompleteAfterRevalidation(), audit
		}
		return t.markCompleteAfterRevalidation(), nil
	}
	return false, nil
}

func (t *CommitChapterTool) markCompleteAfterRevalidation() bool {
	if err := t.store.RefreshCompletionRevalidationEvidence(); err != nil {
		slog.Warn("completion revalidation remains pending", "module", "tool", "err", err)
		return false
	}
	if err := t.store.Progress.MarkComplete(); err != nil {
		slog.Warn("mark complete rejected", "module", "tool", "err", err)
		return false
	}
	return true
}

func (t *CommitChapterTool) completionAuditAllows() (bool, *CompletionAuditResult) {
	if t.completionGate == nil {
		return true, nil
	}
	result, err := t.completionGate.EvaluateCompletion()
	if err != nil {
		slog.Warn("completion audit failed", "module", "tool", "err", err)
		_ = t.store.Progress.SetCompletionAudit("error", "")
		return false, &CompletionAuditResult{Applicable: true, Allowed: false, Status: "error", Warning: err.Error()}
	}
	_ = t.store.Progress.SetCompletionAudit(result.Status, result.ReportDigest)
	return result.Allowed, &result
}

// layeredStructurallyComplete 判定分层长篇是否"结构上写完"：返工队列空 + 无骨架弧待展开
// + 所有已展开章节都已写。这是确定性的终态事实，不含伏笔/长线等语义判断——用作"防终态
// 死循环"的安全网（返工排空后据此重新完结）。
func (t *CommitChapterTool) adaptationPlanComplete(progress *domain.Progress) bool {
	if progress == nil || !t.store.Adaptation.Active() {
		return false
	}
	plan, err := t.store.Adaptation.LoadPlan()
	if err != nil || plan == nil || len(plan.Chapters) == 0 {
		return false
	}
	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	for _, chapter := range progress.CompletedChapters {
		completed[chapter] = struct{}{}
	}
	for _, chapterPlan := range plan.Chapters {
		if _, ok := completed[chapterPlan.Chapter]; !ok {
			return false
		}
	}
	return true
}

func (t *CommitChapterTool) layeredStructurallyComplete(progress *domain.Progress) bool {
	// 1. 返工队列必须清空
	if len(progress.PendingRewrites) > 0 {
		return false
	}
	volumes, err := t.store.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return false
	}
	// 2. 不能还有骨架弧待展开（计划内仍有内容要写）
	for i := range volumes {
		for j := range volumes[i].Arcs {
			if !volumes[i].Arcs[j].IsExpanded() {
				return false
			}
		}
	}
	// 3. 已展开章节必须全部写完
	expanded := len(domain.FlattenOutline(volumes))
	return expanded > 0 && len(progress.CompletedChapters) >= expanded
}

// layeredBookComplete 用客观事实判断分层长篇是否真正写完，对照 architect-long.md 完结判定
// 清单里可量化的几项 + 结构性事实。结构完整之上再要求伏笔归零、长线收束——任一不满足都
// 让位给架构师继续 expand_arc / append_volume，绝不抢在故事没写完时收尾。无 compass 时保守
// 判为未完结。这是正向写作的"质量级"完结判定，比 layeredStructurallyComplete 更严。
func (t *CommitChapterTool) layeredBookComplete(progress *domain.Progress) bool {
	if !t.layeredStructurallyComplete(progress) {
		return false
	}
	// 4. 活跃伏笔必须归零（承诺已兑现）
	if active, aerr := t.store.World.LoadActiveForeshadow(); aerr != nil || len(active) > 0 {
		return false
	}
	// 5. 指南针活跃长线必须收束（无 compass / 长线未清都交回架构师裁定）
	compass, cerr := t.store.Outline.LoadCompass()
	if cerr != nil || compass == nil || len(compass.OpenThreads) > 0 {
		return false
	}
	return true
}
