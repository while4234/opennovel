package reminder

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	adaptpkg "github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/simulationcheck"
	"github.com/voocel/ainovel-cli/internal/store"
)

// subagentMaxConsecutiveBlocks 连续阻拦 N 次后升级为终止，避免弱模型死循环。
const subagentMaxConsecutiveBlocks = 3

// hardStopReasons 是无法用催促消息恢复的 provider 端拒答原因。注入
// "必须 commit" 对它们无效，反而每次产生一次完整 LLM 调用的 token 消耗，
// 并最终升级 escalate 后让 coordinator 重派整个 SubAgent，叠加多倍浪费
// （实测 ch02 撞 safety 时一次写章产生 3 次重派 17 次 LLM 调用、命中率
// 从 50% 跌到 2.8%）。
//
// 注意 StopReasonError / StopReasonAborted 不需要列入：agentcore 在
// loop.go 收到这两种 stop reason 时直接终止 run，根本不会调用 StopGuard。
// 这里只列那些会真正走到 StopGuard 的 provider 拒答语义。
var hardStopReasons = map[agentcore.StopReason]struct{}{
	"safety":         {},
	"content_filter": {},
}

// newCheckpointDeltaGuard 构造一个 StopGuard：
// 在 baseline 之后若未出现指定 step 的 checkpoint，则拒绝 end_turn。
// baseline 由调用方在 factory 时刻捕获，保证 per-run 语义正确。
func newCheckpointDeltaGuard(st *store.Store, agentName string, requiredSteps []string, blockMsg string) agentcore.StopGuard {
	return newCheckpointDeltaGuardFunc(st, agentName, requiredSteps, func() string { return blockMsg })
}

func newCheckpointDeltaGuardFunc(st *store.Store, agentName string, requiredSteps []string, blockMsg func() string) agentcore.StopGuard {
	var baseline int64
	if cp := st.Checkpoints.LatestGlobal(); cp != nil {
		baseline = cp.Seq
	}
	need := make(map[string]struct{}, len(requiredSteps))
	for _, s := range requiredSteps {
		need[s] = struct{}{}
	}
	var consecutive atomic.Int32
	var latestWriterProgressSeq atomic.Int64
	latestWriterProgressSeq.Store(baseline)
	return func(_ context.Context, info agentcore.StopInfo) agentcore.StopDecision {
		// 不可恢复错误：直接升级，不浪费一次催促。
		if _, hard := hardStopReasons[info.Message.StopReason]; hard {
			slog.Error("subagent stop_guard 检测到不可恢复停机，立即升级",
				"module", "host.reminder", "agent", agentName,
				"turn", info.TurnIndex, "stop_reason", info.Message.StopReason)
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		if agentName == "writer" && writerDraftNeedsBudgetRepair(st) {
			// Normal-original drafts outside the current per-chapter budget are
			// repaired by Host-owned bounded line segments. Let this Writer run
			// return immediately; injecting consistency/commit reminders here
			// makes the model hallucinate evidence because read_chapter correctly
			// refuses a full over-budget replay.
			consecutive.Store(0)
			return agentcore.StopDecision{Allow: true}
		}
		// 倒序扫描：新 checkpoint 在尾部，遇到 <= baseline 即可 break。
		all := st.Checkpoints.All()
		var writerProgressSeq int64
		for i := len(all) - 1; i >= 0; i-- {
			cp := all[i]
			if cp.Seq <= baseline {
				break
			}
			if writerProgressSeq == 0 && agentName == "writer" && cp.Scope.Kind == domain.ScopeChapter {
				writerProgressSeq = cp.Seq
			}
			_, required := need[cp.Step]
			// A Host-owned word-budget segment is a complete Writer dispatch,
			// even though the chapter itself is not ready to commit yet. Let the
			// Writer return so Coordinator/Router can issue the next durable
			// segment instead of injecting validation reminders into this run.
			budgetSegmentComplete := agentName == "writer" && strings.HasPrefix(cp.Step, "word_budget_edit_segment_")
			if required || budgetSegmentComplete {
				consecutive.Store(0)
				return agentcore.StopDecision{Allow: true}
			}
		}
		if writerProgressSeq > latestWriterProgressSeq.Load() {
			// Writer may need several validate/repair cycles before commit. A new
			// chapter checkpoint proves durable progress, so a later end-turn is
			// the first no-progress attempt after that work.
			latestWriterProgressSeq.Store(writerProgressSeq)
			consecutive.Store(0)
		}
		n := consecutive.Add(1)
		if n > subagentMaxConsecutiveBlocks {
			slog.Error("subagent stop_guard 连续阻拦超限，升级为终止",
				"module", "host.reminder", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
			return agentcore.StopDecision{Allow: false, Escalate: true}
		}
		slog.Warn("subagent stop_guard 拦截 end_turn",
			"module", "host.reminder", "agent", agentName, "turn", info.TurnIndex, "consecutive", n)
		return agentcore.StopDecision{Allow: false, InjectMessage: blockMsg()}
	}
}

func writerDraftNeedsBudgetRepair(st *store.Store) bool {
	if st == nil || st.Adaptation.Active() {
		return false
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return false
	}
	chapter := progress.InProgressChapter
	if chapter <= 0 {
		chapter = progress.CurrentChapter
	}
	if chapter <= 0 {
		return false
	}
	_, wordCount, err := st.Drafts.LoadChapterContent(chapter)
	if err != nil || wordCount <= 0 {
		return false
	}
	_, policy, ok, err := st.ChapterWordBudgetPolicy(progress, chapter)
	if err != nil || !ok {
		return false
	}
	return !policy.WithinHardRange(wordCount)
}

// NewWriterStopGuard 要求 writer 本轮至少产生一次成功的 commit_chapter。
func NewWriterStopGuard(st *store.Store) agentcore.StopGuard {
	return newCheckpointDeltaGuardFunc(st, "writer",
		[]string{"commit"},
		func() string { return writerStopBlockMessage(st) },
	)
}

func writerStopBlockMessage(st *store.Store) string {
	const generic = "你必须调用 commit_chapter 提交本章后才能结束。draft_chapter 只是保存草稿，不算完成。"
	if st == nil {
		return generic
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return generic
	}
	chapter := progress.InProgressChapter
	if chapter <= 0 {
		chapter = progress.CurrentChapter
	}
	if chapter <= 0 {
		return generic
	}
	if !st.Adaptation.Active() {
		if content, _, contentErr := st.Drafts.LoadChapterContent(chapter); contentErr == nil {
			if msg := writerConsistencyCheckBlockMessage(st, chapter, content); msg != "" {
				return msg
			}
			if msg := writerDeAICheckBlockMessage(st, chapter, content); msg != "" {
				return msg
			}
			if msg := writerSimulationCheckBlockMessage(st, chapter, content); msg != "" {
				return msg
			}
		}
		return generic
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed ||
		plan.RewritePolicy != domain.AdaptationRewritePreserveDetails {
		if content, _, contentErr := st.Drafts.LoadChapterContent(chapter); contentErr == nil {
			if msg := writerDeAICheckBlockMessage(st, chapter, content); msg != "" {
				return msg
			}
			if msg := writerSimulationCheckBlockMessage(st, chapter, content); msg != "" {
				return msg
			}
		}
		return generic
	}
	var chapterPlan *domain.AdaptationChapterPlan
	for i := range plan.Chapters {
		if plan.Chapters[i].Chapter == chapter {
			chapterPlan = &plan.Chapters[i]
			break
		}
	}
	if chapterPlan == nil {
		return generic
	}
	content, wordCount, err := st.Drafts.LoadChapterContent(chapter)
	if err != nil || wordCount == 0 {
		return generic
	}
	if chapterPlan.TargetMinRunes > 0 && wordCount < chapterPlan.TargetMinRunes {
		return fmt.Sprintf(
			"第 %d 章草稿只有 %d 字，低于 preserve_details 硬区间 %d-%d 字。不要调用 commit_chapter，也不要只输出改动片段；先 read_chapter(source=\"source\", chapter=%d) 读取原文，再用 draft_chapter(mode=\"write\") 写入完整章节，保留未受改编目标影响的原文细节并只重写受影响部分。",
			chapter, wordCount, chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes, chapter,
		)
	}
	if chapterPlan.TargetMaxRunes > 0 && wordCount > chapterPlan.TargetMaxRunes {
		return fmt.Sprintf(
			"第 %d 章草稿 %d 字，超过 preserve_details 硬区间 %d-%d 字。不要调用 commit_chapter；先按原文 source 对照重写完整章节并压缩到区间内。",
			chapter, wordCount, chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes,
		)
	}
	if msg := writerAdaptationCheckBlockMessage(st, chapter, content, *chapterPlan); msg != "" {
		return msg
	}
	if msg := writerConsistencyCheckBlockMessage(st, chapter, content); msg != "" {
		return msg
	}
	if msg := writerDeAICheckBlockMessage(st, chapter, content); msg != "" {
		return msg
	}
	if msg := writerSimulationCheckBlockMessage(st, chapter, content); msg != "" {
		return msg
	}
	return generic
}

func writerSimulationCheckBlockMessage(st *store.Store, chapter int, content string) string {
	if st == nil || st.Simulation == nil || st.SimulationContracts == nil ||
		st.SimulationChecks == nil || chapter <= 0 || content == "" {
		return ""
	}
	profile, err := st.Simulation.LoadPortable()
	if err != nil || profile == nil {
		return ""
	}
	contract, err := st.SimulationContracts.Load()
	if err != nil || contract == nil {
		return fmt.Sprintf("第 %d 章尚未建立当前仿写检查契约。下一次响应直接调用 check_simulation；不要再次修改正文或直接 commit_chapter。", chapter)
	}
	if contract.Status == domain.SimulationContractInactive {
		return ""
	}
	evidence, _ := st.Simulation.LoadLocalEvidence()
	var index *domain.SimulationSafetyIndex
	if evidence != nil && evidence.ProfileDigest == profile.ProfileDigest {
		index = evidence.SafetyIndex
	}
	report, err := st.SimulationChecks.Load(chapter)
	if err != nil {
		return fmt.Sprintf("第 %d 章无法读取仿写检查报告：%v。下一次响应直接调用 check_simulation。", chapter, err)
	}
	binding := simulationcheck.Binding{
		ProjectDigest:     store.TextSHA256(strings.ToLower(strings.TrimSpace(st.Dir()))),
		Chapter:           chapter,
		DraftDigest:       store.TextSHA256(content),
		ProfileDigest:     profile.ProfileDigest,
		ContractRevision:  contract.Revision,
		ContractDigest:    contract.ContractDigest,
		EffectiveMode:     contract.EffectiveMode,
		CheckerDigest:     simulationcheck.ConfigurationDigest(),
		SafetyIndexDigest: simulationcheck.SafetyIndexDigest(index),
	}
	if current, reason := simulationcheck.Current(report, binding); !current {
		return fmt.Sprintf("第 %d 章尚未对当前最终草稿完成仿写检查（%s）。下一次响应直接调用 check_simulation；不得复用旧报告或直接 commit_chapter。", chapter, reason)
	}
	if !report.Passed {
		return fmt.Sprintf("第 %d 章仿写检查未通过（copy=%s contract=%s）。按报告 remediation 修复后重跑全部受影响检查和 check_simulation；不得直接 commit_chapter。", chapter, report.CopyStatus, report.ContractStatus)
	}
	return ""
}

func writerConsistencyCheckBlockMessage(st *store.Store, chapter int, content string) string {
	if st == nil || chapter <= 0 || content == "" {
		return ""
	}
	digest := store.TextSHA256(content)
	checkpoint := st.Checkpoints.LatestByStep(domain.ChapterScope(chapter), "consistency_check")
	if checkpoint != nil && checkpoint.Digest == "sha256:"+digest {
		return ""
	}
	return fmt.Sprintf(
		"第 %d 章当前草稿尚未通过一致性审核，或审核后正文已改变。下一次响应先调用一次 read_chapter(chapter=%d, source=\"draft\") 取得当前完整草稿，再立即调用一次 check_consistency；不要调用 novel_context、draft_chapter 或 check_de_ai。不得因为新 Writer 上下文未携带正文就使用 MISSING_FROM_DRAFT；只有回读后确认计划场景确实完全缺失时才能使用该标记。逐场景核对时间、地点、POV、人物、事件顺序、信息边界、不可逆结果和下一章承接；语义相似但替换了既定起源事件或地点也必须报告为 blocking arc_beat_miss。修复全部 critical/error finding 并对同一版草稿复检通过后，才能进入后续校验。",
		chapter, chapter,
	)
}

func writerDeAICheckBlockMessage(st *store.Store, chapter int, content string) string {
	if st == nil || st.DeAI == nil || chapter <= 0 || content == "" {
		return ""
	}
	enabled, err := st.DeAI.Enabled()
	if err != nil || !enabled {
		return ""
	}
	audit, err := st.DeAI.LoadAudit(chapter)
	if err != nil {
		return fmt.Sprintf("第 %d 章无法读取去AI化审校结果：%v。先重新调用 check_de_ai。", chapter, err)
	}
	digest := store.TextSHA256(content)
	if audit == nil || audit.DraftSHA256 != digest {
		return fmt.Sprintf("第 %d 章尚未对当前草稿完成独立去AI化阶段。先调用 check_de_ai；若报告指出问题，按段落重写后重新执行全部检查。", chapter)
	}
	if !audit.Passed {
		plan := audit.Report.RepairPlan()
		batch := "按 repair_plan 的第一个类别做 1-8 处精确修订"
		if len(plan.Batches) > 0 {
			batch = fmt.Sprintf("先处理“%s”批次（%s）", plan.Batches[0].Label, plan.Batches[0].Instruction)
		}
		return fmt.Sprintf("第 %d 章未通过独立去AI化审校。报告 examples 是精确原文；%s，用 repair_de_ai_batch 落盘后立即重新 check_de_ai。仍有 repair finding 才处理下一类别，不要机械换同义词或整章重写。连续两批没有改善、或剧情结构也需要调整时，才回读全文后用 draft_chapter(mode=write)。去AI化通过后重跑 check_consistency、check_adaptation（如适用）。", chapter, batch)
	}
	return ""
}

func writerAdaptationCheckBlockMessage(st *store.Store, chapter int, content string, chapterPlan domain.AdaptationChapterPlan) string {
	if plan, err := st.Adaptation.LoadPlan(); err == nil && plan != nil && plan.Status == domain.AdaptationPlanStatusConfirmed &&
		domain.NormalizeAdaptationGranularity(plan.Granularity) == domain.AdaptationGranularityArc {
		if densityIssues := domain.ValidateArcChapterBudgetDensity(*plan); len(densityIssues) > 0 {
			return fmt.Sprintf("第 %d 章不能继续创作：旧改编大纲章节字数预算仍未通过创作前专项修复（%s）。不要修改正文；先由 Host 执行预算专项模型重分析并重新派发 writer。", chapter, densityIssues[0].Detail)
		}
		var qualityErr error
		if !domain.AdaptationOutlineQualityPassed(*plan) {
			qualityErr = adaptpkg.ValidateAdaptationChapterOutlineQuality(plan, chapter)
		}
		if qualityErr != nil {
			return fmt.Sprintf("第 %d 章不能继续创作：已确认的改编大纲契约未通过计划层校验：%v。不要修改正文、不要补 body_evidence、不要继续重试 check_adaptation；先修复 event_ids 的章节归属及对应章节提纲，再重新派发 writer。", chapter, qualityErr)
		}
	}
	digest := store.TextSHA256(content)
	check, err := st.Adaptation.LoadCheck(chapter)
	if err != nil {
		return fmt.Sprintf("第 %d 章无法读取 check_adaptation 结果：%v。不要调用 commit_chapter；先重新调用 check_adaptation。", chapter, err)
	}
	if check == nil {
		return fmt.Sprintf(
			"第 %d 章尚未通过 check_adaptation。不要调用 commit_chapter；先调用 check_adaptation。%s",
			chapter, writerChangeEvidenceInstruction(chapter, chapterPlan),
		)
	}
	if check.DraftSHA256 != digest {
		return fmt.Sprintf(
			"第 %d 章草稿已在上次 check_adaptation 后改变。不要调用 commit_chapter；必须对当前草稿重新调用 check_adaptation。%s",
			chapter, writerChangeEvidenceInstruction(chapter, chapterPlan),
		)
	}
	if check.Passed {
		if writerNeedsChangeEvidence(chapterPlan) && len(check.ChangeEvidence) == 0 {
			return fmt.Sprintf(
				"第 %d 章 check_adaptation 记录缺少 change_evidence。不要调用 commit_chapter；重新调用 check_adaptation，并把证据放进 change_evidence 参数。%s",
				chapter, writerChangeEvidenceInstruction(chapter, chapterPlan),
			)
		}
		return ""
	}
	return fmt.Sprintf(
		"第 %d 章最近一次 check_adaptation 未通过：%s。不要调用 commit_chapter；按失败原因修复后重新调用 check_adaptation。%s",
		chapter, strings.Join(check.Issues, "；"), writerAdaptationIssueInstruction(chapter, chapterPlan, check.Issues),
	)
}

func writerAdaptationIssueInstruction(chapter int, chapterPlan domain.AdaptationChapterPlan, issues []string) string {
	for _, issue := range issues {
		switch {
		case strings.Contains(issue, "adaptation_outline_contract") || strings.Contains(issue, "arc_event_"):
			return "这是上游改编大纲契约错误，不是正文缺失。不要编辑正文或反复补 body_evidence；先让协调器修复事件归属、preserve_events、required_changes 与对应章节剧情，再重新派发本章。"
		case strings.Contains(issue, "adaptation_change_evidence"):
			return writerChangeEvidenceInstruction(chapter, chapterPlan)
		case strings.Contains(issue, "adaptation_body_evidence"):
			return "先回读当前草稿，不要立即改写正文。用正文中逐字存在、能直接证明 assigned event 的短引文重新调用 check_adaptation(body_evidence=[...])；只有正文确实没有该事件时才修改正文，修改后必须重新执行全部检查。"
		case strings.Contains(issue, "adaptation_source_similarity"):
			return fmt.Sprintf("先 read_chapter(source=\"source\", chapter=%d) 对照原文，保留未受影响段落，只把 required_changes 影响的完整场景单元重写为新的小说正文。", chapter)
		case strings.Contains(issue, "adaptation_quality"):
			return "先用 draft_chapter(mode=\"write\") 删除括号补丁、内心独白标签、仅为示意等 meta 文本，把内容写成正常小说叙述、动作、对白或潜台词。"
		case strings.Contains(issue, "adaptation_word_contract"):
			return fmt.Sprintf("先按 preserve_details 字数硬区间修复完整章节，再调用 check_adaptation。当前目标区间是 %d-%d 字。", chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes)
		}
	}
	return "不要把失败原因只写进 summary；需要把对应字段作为工具参数传入。"
}

func writerChangeEvidenceInstruction(chapter int, chapterPlan domain.AdaptationChapterPlan) string {
	if !writerNeedsChangeEvidence(chapterPlan) {
		return "本章没有 required_changes 时 change_evidence 可传 []。"
	}
	sourceChapter := 0
	if len(chapterPlan.SourceChapters) > 0 {
		sourceChapter = chapterPlan.SourceChapters[0]
	}
	return fmt.Sprintf(
		"调用 check_adaptation 时必须传非空 change_evidence JSON array，不要只写在 summary。示例：check_adaptation({\"chapter\":%d,\"passed\":true,\"summary\":\"主线保留且改编目标已融入正文\",\"change_evidence\":[{\"source_chapter\":%d,\"source_anchor\":\"原文中被改动的场景锚点\",\"change\":\"说明 required_changes 中哪项被落实\",\"integration\":\"说明它如何自然出现在正文动作、对白、叙述或潜台词中\"}]})",
		chapter, sourceChapter,
	)
}

func writerNeedsChangeEvidence(chapterPlan domain.AdaptationChapterPlan) bool {
	for _, change := range chapterPlan.RequiredChanges {
		if strings.TrimSpace(change) != "" {
			return true
		}
	}
	return false
}

// NewArchitectStopGuard requires architect to persist at least one
// save_foundation checkpoint in this run. Foundation generation contract
// failures are deliberately returned at an after-tool boundary so Host can
// re-dispatch the same durable section in a clean subagent context.
func NewArchitectStopGuard(st *store.Store, task ...string) agentcore.StopGuard {
	checkpointGuard := newCheckpointDeltaGuard(st, "architect",
		[]string{
			"premise", "outline", "layered_outline", "characters", "world_rules",
			"expand_arc", "repair_arc", "repair_volume", "append_volume", "update_compass", "complete_book",
		},
		"你必须调用 save_foundation 将产出落盘后才能结束。只输出 Markdown/JSON 文字等于丢失。",
	)
	foundationGeneration := len(task) > 0 && strings.Contains(task[0], "foundation_generation=")
	if !foundationGeneration {
		return checkpointGuard
	}
	return func(ctx context.Context, info agentcore.StopInfo) agentcore.StopDecision {
		if info.Trigger == agentcore.StopTriggerAfterTool {
			// The result-aware stop hook requests this boundary only for either
			// durable success or retry_in_fresh_context. Both must return to Host:
			// success advances the route; retry starts a clean subagent context.
			return agentcore.StopDecision{Allow: true}
		}
		return checkpointGuard(ctx, info)
	}
}

// NewEditorStopGuard 要求 editor 本轮落盘与"任务"匹配的产物后才能结束。
//
// 任务感知：被派去生成摘要时，仅 save_review（复核）不算完成——必须产出对应摘要。
// 否则"被派生成弧摘要却先复核"的 editor 会满足旧的宽松判据提前结束，弧摘要永不落盘
// （配合 dispatcher 去重哑火曾导致卷中骨架弧死循环，详见 outline-exhaustion-livelock）。
// StopAfterTool 退出会绕过 StopGuard（loop.go），故 build.go 同步把 save_review 移出硬停，
// 让复核后能继续走到摘要工具，再由本 guard 把关收尾。
func NewEditorStopGuard(st *store.Store, task string) agentcore.StopGuard {
	switch {
	case strings.Contains(task, "save_original_planning_audit") || strings.Contains(task, "原创细纲"):
		return newCheckpointDeltaGuard(st, "editor", []string{"original_planning_audit"},
			"本次任务是审核原创细纲：你必须调用 save_original_planning_audit 落盘本批审核后才能结束。")
	case strings.Contains(task, "save_volume_summary") || strings.Contains(task, "卷摘要"):
		return newCheckpointDeltaGuard(st, "editor", []string{"volume_summary"},
			"本次任务是生成卷摘要：你必须调用 save_volume_summary 落盘后才能结束，save_review 复核不算完成。")
	case strings.Contains(task, "save_arc_summary") || strings.Contains(task, "弧摘要"):
		return newCheckpointDeltaGuard(st, "editor", []string{"arc_summary"},
			"本次任务是生成弧摘要：你必须调用 save_arc_summary 落盘后才能结束，save_review 复核不算完成。")
	default:
		// 评审或临时任务：任一审阅/摘要落盘即可（保持既有宽松行为）。
		return newCheckpointDeltaGuard(st, "editor",
			[]string{"review", "arc_review_batch", "arc_summary", "volume_summary"},
			"你必须调用 save_review / save_arc_summary / save_volume_summary 之一落盘结果后才能结束。")
	}
}
