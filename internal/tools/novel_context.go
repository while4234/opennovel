package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// References 嵌入的参考资料。
type References struct {
	// V0
	ChapterGuide      string
	HookTechniques    string
	QualityChecklist  string
	OutlineTemplate   string
	CharacterTemplate string
	ChapterTemplate   string
	// V1
	Consistency      string
	ContentExpansion string
	DialogueWriting  string
	// V2
	StyleReference                  string // 风格补充参考（可为空）
	LongformPlanning                string // 通用长篇规划参考
	Differentiation                 string // 通用差异化设计参考
	ArcTemplates                    string // 题材弧型模板（按 style 加载，可为空）
	AntiAITone                      string // 去 AI 味判据库（writer/editor 共用，全程注入）
	AdaptationWriter                string // 小说改编 Writer 专用追加规则（仅 adaptation_mode 注入）
	AdaptationEditorPreserveDetails string // preserve_details 改编 Editor 专用审阅规则
	AdaptationEditorFullRewrite     string // full_rewrite 改编 Editor 专用审阅规则
}

// ContextTool 组装当前章节所需上下文。
type ContextTool struct {
	store          *store.Store
	refs           References
	style          string
	simulationMode string
	simulationRole string
}

const (
	contextSimulationModeNormal      = "normal"
	contextSimulationModeReinforced  = "reinforced"
	writerChapterContextBudgetBytes  = 60 * 1024
	writerChapterSourceBudgetBytes   = 28 * 1024
	writerPolishingContextBytes      = 24 * 1024
	writerRecoveryContextBytes       = 8 * 1024
	planningContextBudgetBytes       = 60 * 1024
	planningContextSourceBytes       = 32 * 1024
	planningDetailContextSourceBytes = 36 * 1024
	planningReviewContextSourceBytes = 28 * 1024
	planningAuditContextSourceBytes  = 34 * 1024
	nearbyOutlineBeforeChapters      = 2
	nearbyOutlineAfterChapters       = 3
	maxOutlineRangeChapters          = 80
	maxDetailedArcOutlineChapters    = 40
)

type ContextToolOptions struct {
	SimulationMode string
	Role           string
}

// NewContextTool 创建上下文工具。
// user_rules 由 buildUserRules 直接读本书快照（meta/user_rules.json）注入，不再依赖加载选项。
func NewContextTool(store *store.Store, refs References, style string) *ContextTool {
	return NewContextToolWithOptions(store, refs, style, ContextToolOptions{})
}

func NewContextToolWithOptions(store *store.Store, refs References, style string, opts ContextToolOptions) *ContextTool {
	return &ContextTool{
		store:          store,
		refs:           refs,
		style:          style,
		simulationMode: normalizeContextToolSimulationMode(opts.SimulationMode),
		simulationRole: normalizeContextToolSimulationRole(opts.Role),
	}
}

func (t *ContextTool) Name() string { return "novel_context" }
func (t *ContextTool) Description() string {
	return "获取小说当前状态和创作上下文。" +
		"Coordinator 判断下一步必须使用 scope=status，只返回轻量 progress_status。" +
		"Architect 使用 scope=planning 获取有界基础设定；不要用 planning 做普通进度轮询。" +
		"传 chapter=N：额外返回该章的前情摘要、伏笔、角色状态、风格规则等写作上下文。" +
		"scope=summary：返回指定章节范围的摘要证据包，供弧摘要/卷摘要复用，避免无差别重读正文"
}
func (t *ContextTool) Label() string { return "加载上下文" }

// 纯读工具，可被并发调度。
func (t *ContextTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *ContextTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *ContextTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("scope", schema.Enum("Context scope. Empty defaults to chapter when chapter is set, otherwise planning. planning_detail is the Architect-only high-fidelity generation view for one volume/arc and requires volume+arc. planning_audit is the Editor/repair single-call evidence pack for at most four detailed chapters and requires volume+arc+from+to. planning_review is the Editor-only bounded view for a volume skeleton review. status returns progress only; summary returns a compact evidence pack for an inclusive chapter range.", "chapter", "outline_range", "summary", "planning", "planning_detail", "planning_audit", "planning_review", "status")),
		schema.Property("from", schema.Int("First chapter for scope=outline_range.")),
		schema.Property("to", schema.Int("Last chapter for scope=outline_range.")),
		schema.Property("volume", schema.Int("Volume number for scope=summary, planning_detail, planning_audit, or a single-volume planning_review.")),
		schema.Property("arc", schema.Int("Arc number for scope=planning_detail or planning_audit.")),
		schema.Property("from_volume", schema.Int("First volume for a planning_review batch.")),
		schema.Property("to_volume", schema.Int("Last volume for a planning_review batch.")),
		schema.Property("chapter", schema.Int("章节号。不传则返回进度状态和基础设定（Coordinator 用于判断下一步）；传入则额外返回该章的写作上下文（Writer 用）")),
	)
}

func (t *ContextTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter    int    `json:"chapter"`
		Scope      string `json:"scope"`
		From       int    `json:"from"`
		To         int    `json:"to"`
		Volume     int    `json:"volume"`
		Arc        int    `json:"arc"`
		FromVolume int    `json:"from_volume"`
		ToVolume   int    `json:"to_volume"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	scope := normalizeContextScope(a.Scope, a.Chapter)

	result := make(map[string]any)
	chapterPurpose := chapterContextWriting
	var warnings []string
	seenWarnings := make(map[string]struct{})
	warn := func(scope string, err error) {
		if err == nil || os.IsNotExist(err) {
			return
		}
		msg := fmt.Sprintf("%s 读取失败: %v", scope, err)
		if _, ok := seenWarnings[msg]; ok {
			return
		}
		seenWarnings[msg] = struct{}{}
		warnings = append(warnings, msg)
	}

	switch scope {
	case "status":
		t.buildProgressStatus(result)
	case "outline_range":
		if err := t.buildOutlineRangeContext(result, a.From, a.To, warn); err != nil {
			return nil, err
		}
	case "summary":
		if err := t.buildSummaryEvidenceContext(result, a.From, a.To, a.Volume, warn); err != nil {
			return nil, err
		}
	case "planning_review":
		t.buildProgressStatus(result)
		t.buildArchitectContext(result, warn)
		t.buildAdaptationPlanningContext(result, warn)
		if err := t.scopePlanningReviewContext(result, a.Volume, a.FromVolume, a.ToVolume); err != nil {
			return nil, err
		}
	case "planning_detail":
		t.buildProgressStatus(result)
		t.buildArchitectContext(result, warn)
		t.buildAdaptationPlanningContext(result, warn)
		if err := t.scopePlanningDetailContext(result, a.Volume, a.Arc); err != nil {
			return nil, err
		}
	case "planning_audit":
		t.buildProgressStatus(result)
		t.buildArchitectContext(result, warn)
		t.buildAdaptationPlanningContext(result, warn)
		if err := t.scopePlanningAuditContext(result, a.Volume, a.Arc, a.From, a.To, warn); err != nil {
			return nil, err
		}
	case "chapter":
		// Writer 路径：加载当前章工作包，长篇/大资料项目使用窗口化与源头压缩。
		seed := newChapterContextEnvelope()
		state := t.prepareChapterContext(a.Chapter, &seed, warn)
		chapterPurpose = state.purpose
		// A chapter request is assembled from chapter-owned source sections.
		// Polishing intentionally excludes premise/world/arc planning: the draft,
		// rewrite brief, contract and continuity facts are the authoritative scope.
		// Premise/world/arc planning belongs to Architect. Writer receives the
		// signed chapter contract and chapter-owned continuity evidence instead.
		seed.apply(result)
		t.buildChapterContext(result, state, warn)
		t.buildAdaptationChapterContext(result, a.Chapter, warn)
		// 数据语义标注（治复读交代）：episodic 是已写入正文的备忘，不是待写素材。
		// 只挂容器内，不进顶层镜像。
		if epi, ok := result["episodic_memory"].(map[string]any); ok && len(epi) > 0 {
			epi["_usage"] = "本容器为已写入正文的事实备忘（供一致性与衔接对照）；在新章正文中原样复述这些内容属于重复缺陷"
		}
	default:
		// Coordinator/Architect 路径：只返回状态 + 结构化数据，不加载全量原文
		t.buildProgressStatus(result)
		t.buildArchitectContext(result, warn)
		t.buildAdaptationPlanningContext(result, warn)
	}

	// 注入 working_memory.user_rules（canonical 路径）。架构师路径原本没有 working_memory，
	// 由 buildUserRules 按需新建只装 user_rules 的容器。快照缺失时退到内置默认，
	// 始终输出稳定结构，避免 LLM 看到 user_rules=null 走异常分支。
	t.buildRoleBoundSimulationContext(result, scope, chapterPurpose, warn)

	if scope == "chapter" || scope == "planning" || scope == "planning_detail" {
		t.buildUserRules(result, scope == "chapter" && chapterContextHasAuthoritativeOutline(result))
		t.buildWordBudget(result, a.Chapter)
	} else if scope == "planning_review" {
		t.buildWordBudget(result, a.Chapter)
	}
	if scope == "planning_detail" {
		deduplicatePlanningDetailContext(result)
	}
	if scope == "chapter" {
		result["context_profile"] = string(chapterPurpose)
	}

	if len(warnings) > 0 {
		result["_warnings"] = warnings
	}

	result["_loading_summary"] = buildLoadingSummary(result, a.Chapter)
	return json.Marshal(result)
}

func chapterContextHasAuthoritativeOutline(result map[string]any) bool {
	working, ok := result["working_memory"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = working["current_chapter_outline"]
	return ok
}

func normalizeContextScope(scope string, chapter int) string {
	switch strings.TrimSpace(scope) {
	case "outline_range":
		return "outline_range"
	case "summary":
		return "summary"
	case "chapter":
		if chapter > 0 {
			return "chapter"
		}
		return "planning"
	case "planning":
		return "planning"
	case "planning_detail":
		return "planning_detail"
	case "planning_review":
		return "planning_review"
	case "planning_audit":
		return "planning_audit"
	case "status":
		return "status"
	default:
		if chapter > 0 {
			return "chapter"
		}
		return "planning"
	}
}

func (t *ContextTool) buildSummaryEvidenceContext(
	result map[string]any,
	from int,
	to int,
	volume int,
	warn func(string, error),
) error {
	if volume > 0 && from == 0 && to == 0 {
		return t.buildVolumeSummaryEvidenceContext(result, volume, warn)
	}
	if from <= 0 || to < from {
		return fmt.Errorf("summary scope requires either volume or a valid inclusive chapter range: volume=%d from=%d to=%d", volume, from, to)
	}

	summaries := make([]domain.ChapterSummary, 0, to-from+1)
	missingChapters := make([]int, 0)
	for chapter := from; chapter <= to; chapter++ {
		summary, err := t.store.Summaries.LoadSummary(chapter)
		if err != nil {
			warn(fmt.Sprintf("summary.chapter.%d", chapter), err)
			missingChapters = append(missingChapters, chapter)
			continue
		}
		if summary == nil {
			missingChapters = append(missingChapters, chapter)
			continue
		}
		summaries = append(summaries, *summary)
	}
	result["chapter_summaries"] = summaries

	if review, err := t.store.World.LoadReview(to); err != nil {
		warn("summary.arc_review", err)
	} else if review != nil {
		result["arc_review"] = review
	}

	if snapshots, err := t.store.Characters.LoadLatestSnapshots(); err != nil {
		warn("summary.previous_character_snapshots", err)
	} else if len(snapshots) > 0 {
		result["previous_character_snapshots"] = snapshots
	}

	if timeline, err := t.store.World.LoadTimeline(); err != nil {
		warn("summary.timeline", err)
	} else {
		result["timeline"] = filterTimelineByChapter(timeline, from, to)
	}
	if relationships, err := t.store.World.LoadRelationships(); err != nil {
		warn("summary.relationships", err)
	} else {
		result["relationship_changes"] = filterRelationshipsByChapter(relationships, from, to)
	}
	if changes, err := t.store.World.LoadStateChanges(); err != nil {
		warn("summary.state_changes", err)
	} else {
		result["state_changes"] = filterStateChangesByChapter(changes, from, to)
	}
	if foreshadow, err := t.store.World.LoadForeshadowLedger(); err != nil {
		warn("summary.foreshadow", err)
	} else {
		result["foreshadow_changes"] = filterForeshadowByChapter(foreshadow, from, to)
	}

	result["summary_evidence"] = map[string]any{
		"from":                     from,
		"to":                       to,
		"expected_chapters":        to - from + 1,
		"available_summaries":      len(summaries),
		"missing_summary_chapters": missingChapters,
		"complete":                 len(missingChapters) == 0,
		"usage":                    "优先用本证据包生成摘要；只有 missing_summary_chapters 或评审证据明确指出缺口时，才定向回读对应章节，不要重读整个范围。",
	}
	return nil
}

func (t *ContextTool) buildVolumeSummaryEvidenceContext(
	result map[string]any,
	volume int,
	warn func(string, error),
) error {
	arcSummaries, err := t.store.Summaries.LoadArcSummaries(volume)
	if err != nil {
		warn("summary.arc_summaries", err)
	}
	result["arc_summaries"] = arcSummaries

	expectedArcs := make([]int, 0)
	if volumes, outlineErr := t.store.Outline.LoadLayeredOutline(); outlineErr != nil {
		warn("summary.layered_outline", outlineErr)
	} else {
		for _, candidate := range volumes {
			if candidate.Index != volume {
				continue
			}
			for _, arc := range candidate.Arcs {
				expectedArcs = append(expectedArcs, arc.Index)
			}
			break
		}
	}

	available := make(map[int]struct{}, len(arcSummaries))
	for _, summary := range arcSummaries {
		available[summary.Arc] = struct{}{}
	}
	missingArcs := make([]int, 0)
	for _, arc := range expectedArcs {
		if _, ok := available[arc]; !ok {
			missingArcs = append(missingArcs, arc)
		}
	}
	if snapshots, snapshotErr := t.store.Characters.LoadLatestSnapshots(); snapshotErr != nil {
		warn("summary.character_snapshots", snapshotErr)
	} else if len(snapshots) > 0 {
		result["character_snapshots"] = snapshots
	}
	result["summary_evidence"] = map[string]any{
		"kind":           "volume",
		"volume":         volume,
		"expected_arcs":  expectedArcs,
		"available_arcs": len(arcSummaries),
		"missing_arcs":   missingArcs,
		"complete":       len(expectedArcs) > 0 && len(missingArcs) == 0,
		"usage":          "优先用弧摘要生成卷摘要；只有 missing_arcs 非空时，才定向补取缺失弧证据，不要逐章重读整卷。",
	}
	return nil
}

func filterTimelineByChapter(items []domain.TimelineEvent, from, to int) []domain.TimelineEvent {
	result := make([]domain.TimelineEvent, 0, len(items))
	for _, item := range items {
		if item.Chapter >= from && item.Chapter <= to {
			result = append(result, item)
		}
	}
	return result
}

func filterRelationshipsByChapter(items []domain.RelationshipEntry, from, to int) []domain.RelationshipEntry {
	result := make([]domain.RelationshipEntry, 0, len(items))
	for _, item := range items {
		if item.Chapter >= from && item.Chapter <= to {
			result = append(result, item)
		}
	}
	return result
}

func filterStateChangesByChapter(items []domain.StateChange, from, to int) []domain.StateChange {
	result := make([]domain.StateChange, 0, len(items))
	for _, item := range items {
		if item.Chapter >= from && item.Chapter <= to {
			result = append(result, item)
		}
	}
	return result
}

func filterForeshadowByChapter(items []domain.ForeshadowEntry, from, to int) []domain.ForeshadowEntry {
	result := make([]domain.ForeshadowEntry, 0, len(items))
	for _, item := range items {
		plantedInRange := item.PlantedAt >= from && item.PlantedAt <= to
		resolvedInRange := item.ResolvedAt >= from && item.ResolvedAt <= to
		if plantedInRange || resolvedInRange {
			result = append(result, item)
		}
	}
	return result
}

func normalizeContextToolSimulationMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case contextSimulationModeReinforced:
		return contextSimulationModeReinforced
	default:
		return contextSimulationModeNormal
	}
}

func normalizeContextToolSimulationRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case domain.SimulationRoleCoordinator, domain.SimulationRoleArchitect,
		domain.SimulationRoleWriter, domain.SimulationRoleEditor:
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

// buildLoadingSummary 从已组装的 result 中统计各项数据量，生成一行可读摘要。
func buildLoadingSummary(result map[string]any, chapter int) string {
	var parts []string

	if chapter > 0 {
		parts = append(parts, fmt.Sprintf("ch=%d", chapter))
	} else {
		parts = append(parts, "architect")
	}
	if tier, ok := result["planning_tier"].(domain.PlanningTier); ok && tier != "" {
		parts = append(parts, fmt.Sprintf("tier=%s", tier))
	}

	// 卷弧位置
	if pos, ok := result["position"].(map[string]any); ok {
		parts = append(parts, fmt.Sprintf("V%dA%d", pos["volume"], pos["arc"]))
	}

	var items []string
	countSlice := func(key string) int {
		if v, ok := result[key]; ok {
			if s, ok := v.([]domain.Character); ok {
				return len(s)
			}
			// 通用 slice 反射
			return sliceLen(v)
		}
		return 0
	}

	// 角色
	if n := countSlice("character_snapshots"); n > 0 {
		items = append(items, fmt.Sprintf("角色:%d(快照)", n))
	} else if n := countSlice("characters"); n > 0 {
		items = append(items, fmt.Sprintf("角色:%d", n))
	}

	if working, ok := result["working_memory"].(map[string]any); ok && len(working) > 0 {
		items = append(items, fmt.Sprintf("工作记忆:%d", len(working)))
	}
	if episodic, ok := result["episodic_memory"].(map[string]any); ok && len(episodic) > 0 {
		items = append(items, fmt.Sprintf("情节记忆:%d", len(episodic)))
	}
	if planning, ok := result["planning_memory"].(map[string]any); ok && len(planning) > 0 {
		items = append(items, fmt.Sprintf("规划记忆:%d", len(planning)))
	}
	if foundation, ok := result["foundation_memory"].(map[string]any); ok && len(foundation) > 0 {
		items = append(items, fmt.Sprintf("基础记忆:%d", len(foundation)))
	}

	// 分层摘要
	if n := countSlice("volume_summaries"); n > 0 {
		items = append(items, fmt.Sprintf("卷摘要:%d", n))
	}
	if n := countSlice("arc_summaries"); n > 0 {
		items = append(items, fmt.Sprintf("弧摘要:%d", n))
	}
	if n := countSlice("recent_summaries"); n > 0 {
		items = append(items, fmt.Sprintf("章摘要:%d", n))
	}

	// 分层大纲
	if n := countSlice("layered_outline"); n > 0 {
		items = append(items, fmt.Sprintf("分层大纲:%d卷", n))
	}

	// 状态数据
	if n := countSlice("timeline"); n > 0 {
		items = append(items, fmt.Sprintf("时间线:%d", n))
	}
	if n := countSlice("foreshadow_ledger"); n > 0 {
		items = append(items, fmt.Sprintf("伏笔:%d", n))
	}
	if n := countSlice("relationship_state"); n > 0 {
		items = append(items, fmt.Sprintf("关系:%d", n))
	}
	if n := countSlice("recent_state_changes"); n > 0 {
		items = append(items, fmt.Sprintf("状态变化:%d", n))
	}
	if _, ok := result["previous_tail"]; ok {
		items = append(items, "前章尾部:ok")
	}
	if _, ok := result["style_rules"]; ok {
		items = append(items, "风格规则:ok")
	}
	if n := sliceLen(result["related_chapters"]); n > 0 {
		items = append(items, fmt.Sprintf("相关章:%d", n))
	}
	if selected, ok := result["selected_memory"].(map[string]any); ok && len(selected) > 0 {
		if n := sliceLen(selected["story_threads"]); n > 0 {
			items = append(items, fmt.Sprintf("线索召回:%d", n))
		}
		if n := sliceLen(selected["review_lessons"]); n > 0 {
			items = append(items, fmt.Sprintf("评审召回:%d", n))
		}
	}

	// 参考资料
	if refs, ok := result["references"].(map[string]string); ok && len(refs) > 0 {
		items = append(items, fmt.Sprintf("参考:%d项", len(refs)))
	}
	if pack, ok := result["reference_pack"].(map[string]any); ok && len(pack) > 0 {
		items = append(items, fmt.Sprintf("参考包:%d", len(pack)))
	}
	if _, ok := result["memory_policy"]; ok {
		items = append(items, "记忆策略:ok")
	}
	if _, ok := result["simulation_profile"]; ok {
		mode := "ok"
		if result["simulation_mode"] == contextSimulationModeReinforced {
			mode = contextSimulationModeReinforced
		}
		items = append(items, fmt.Sprintf("仿写模式:%s", mode))
	}
	if simulation, ok := result["simulation_effective"].(simulationRoleContext); ok {
		summary := fmt.Sprintf(
			"simulation_contract:r%d status=%s selected=%d",
			simulation.Contract.Revision,
			simulation.Status,
			simulation.SelectedCount,
		)
		if len(simulation.Reasons) > 0 {
			summary += " reasons=" + strings.Join(simulation.Reasons, ",")
		}
		items = append(items, summary)
	}
	if warnings, ok := result["_warnings"].([]string); ok && len(warnings) > 0 {
		items = append(items, fmt.Sprintf("告警:%d", len(warnings)))
	}
	if trimmed, ok := result["_trimmed"].([]string); ok && len(trimmed) > 0 {
		items = append(items, fmt.Sprintf("裁剪:%s", strings.Join(trimmed, ",")))
	}

	if len(items) > 0 {
		parts = append(parts, strings.Join(items, " "))
	}
	return strings.Join(parts, " | ")
}

// sliceLen 对 any 类型尝试取 slice 长度。
func sliceLen(v any) int {
	switch s := v.(type) {
	case []domain.ChapterSummary:
		return len(s)
	case []domain.ArcSummary:
		return len(s)
	case []domain.VolumeSummary:
		return len(s)
	case []domain.CharacterSnapshot:
		return len(s)
	case []domain.TimelineEvent:
		return len(s)
	case []domain.ForeshadowEntry:
		return len(s)
	case []domain.RelationshipEntry:
		return len(s)
	case []domain.StateChange:
		return len(s)
	case []domain.VolumeOutline:
		return len(s)
	case []domain.Character:
		return len(s)
	case []domain.RelatedChapter:
		return len(s)
	case []domain.RecallItem:
		return len(s)
	default:
		return 0
	}
}

// loadFilteredCharacters 按 Tier 和场景出场过滤角色。
// core/important 始终返回；secondary/decorative 只在当前章节大纲提及时返回。
func (t *ContextTool) loadFilteredCharacters(result map[string]any, chapter int, warn func(string, error)) {
	workset, err := t.buildCharacterWorkset(chapter)
	if err != nil {
		warn("character_workset", err)
		result["character_workset_warning"] = "bounded character workset unavailable; no unbounded fallback was injected"
		return
	}
	if len(workset.Full) == 0 && len(workset.Compressed) == 0 {
		return
	}
	result["character_workset"] = workset
	if len(workset.Snapshots) > 0 {
		result["character_snapshots"] = workset.Snapshots
	}
}

// matchCharacter 检查场景文本中是否包含角色的正式名或任一别名。
func matchCharacter(text string, c domain.Character) bool {
	if strings.Contains(text, c.Name) {
		return true
	}
	for _, alias := range c.Aliases {
		if strings.Contains(text, alias) {
			return true
		}
	}
	return false
}

// loadLayeredSummaries 分层摘要加载：卷摘要 + 当前卷弧摘要 + 弧内章摘要。
func (t *ContextTool) loadLayeredSummaries(result map[string]any, chapter, summaryWindow int, warn func(string, error)) {
	vol, arc, err := t.store.Outline.LocateChapter(chapter)
	if err != nil {
		warn("layered_outline_position", err)
		// 回退到扁平模式
		if summaries, err := t.store.Summaries.LoadRecentSummaries(chapter, summaryWindow); err == nil && len(summaries) > 0 {
			result["recent_summaries"] = compactChapterSummaries(summaries)
		} else {
			warn("recent_summaries", err)
		}
		return
	}

	// 1. 已完成卷的卷摘要
	if volSummaries, err := t.store.Summaries.LoadAllVolumeSummaries(); err == nil && len(volSummaries) > 0 {
		result["volume_summaries"] = compactVolumeSummaries(volSummaries, 2)
	} else {
		warn("volume_summaries", err)
	}

	// 2. 当前卷内已完成弧的弧摘要（不含当前弧）
	if arcSummaries, err := t.store.Summaries.LoadArcSummaries(vol); err == nil && len(arcSummaries) > 0 {
		var prior []domain.ArcSummary
		for _, s := range arcSummaries {
			if s.Arc < arc {
				prior = append(prior, s)
			}
		}
		if len(prior) > 0 {
			result["arc_summaries"] = compactArcSummaries(prior, 3)
		}
	} else {
		warn("arc_summaries", err)
	}

	// 3. 当前弧内最近 N 章的章摘要
	if summaries, err := t.store.Summaries.LoadRecentSummaries(chapter, summaryWindow); err == nil && len(summaries) > 0 {
		result["recent_summaries"] = compactChapterSummaries(summaries)
	} else {
		warn("recent_summaries", err)
	}
}

// loadLayeredCharacters Layered 模式下的角色加载：优先用最近快照，回退到原始设定 + Tier 过滤。
func (t *ContextTool) loadLayeredCharacters(result map[string]any, chapter int, warn func(string, error)) {
	snapshots, err := t.store.Characters.LoadLatestSnapshots()
	if err == nil && len(snapshots) > 0 {
		// The bounded workset joins only relevant snapshots by stable ID.
		t.loadFilteredCharacters(result, chapter, warn)
		return
	}
	warn("character_snapshots", err)
	// 无快照时回退到原始设定
	t.loadFilteredCharacters(result, chapter, warn)
}

// writerReferences returns a compact procedural reference pack. Story facts
// live in the signed chapter contract and episodic memory; generic guidance
// must never displace that authoritative material at the provider boundary.
func (t *ContextTool) writerReferences(chapter int, purpose chapterContextPurpose) map[string]string {
	refs := map[string]string{}
	addWithLimit := func(k, v string, limit int) {
		if v != "" {
			refs[k] = truncateRunes(v, limit)
		}
	}
	if purpose == chapterContextPolishing {
		// Polishing is a local prose operation. The exact anti-AI repair evidence
		// remains available through check_de_ai; this pack supplies only stable
		// prose constraints and the final quality checklist.
		addWithLimit("anti_ai_tone", t.refs.AntiAITone, 900)
		addWithLimit("quality_checklist", t.refs.QualityChecklist, 200)
		return refs
	}
	if purpose == chapterContextRecovering {
		// A full current draft will be read beside this package. Keep only the
		// stable validation constraints that are still needed after prose exists.
		addWithLimit("consistency", t.refs.Consistency, 150)
		addWithLimit("anti_ai_tone", t.refs.AntiAITone, 900)
		addWithLimit("quality_checklist", t.refs.QualityChecklist, 150)
		return refs
	}
	// New writing and substantive rewrites retain the core writing references,
	// with deterministic source limits rather than a post-build truncation pass.
	addWithLimit("consistency", t.refs.Consistency, 120)
	addWithLimit("hook_techniques", t.refs.HookTechniques, 100)
	addWithLimit("quality_checklist", t.refs.QualityChecklist, 120)
	// This is a core prose constraint, not an optional chapter-one reference.
	// Previously the bundle loaded AntiAITone but never placed it in Writer or
	// Editor context, so its most important long-form instructions were inert.
	addWithLimit("anti_ai_tone", t.refs.AntiAITone, 900)
	if chapter <= 3 {
		addWithLimit("chapter_guide", t.refs.ChapterGuide, 180)
		addWithLimit("dialogue_writing", t.refs.DialogueWriting, 120)
		addWithLimit("style_reference", t.refs.StyleReference, 160)
	}

	// 仅首章加载的补充参考
	if chapter <= 1 {
		addWithLimit("chapter_template", t.refs.ChapterTemplate, 80)
		addWithLimit("content_expansion", t.refs.ContentExpansion, 120)
	}
	return refs
}

func (t *ContextTool) architectReferences() map[string]string {
	refs := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			// Architect receives concise procedural references. Full templates are
			// useful while authoring assets, but repeating six multi-page documents
			// on every planning/status check displaces the actual saved foundation.
			refs[k] = truncateRunes(v, 250)
		}
	}
	add("outline_template", t.refs.OutlineTemplate)
	add("character_template", t.refs.CharacterTemplate)
	add("longform_planning", t.refs.LongformPlanning)
	add("differentiation", t.refs.Differentiation)
	add("style_reference", t.refs.StyleReference)
	add("arc_templates", t.refs.ArcTemplates)
	return refs
}

// foundationStatus 检查基础设定的完备性，返回缺失项列表。
// 与 save_foundation 工具共用 store.FoundationMissing 判定逻辑，保证 LLM 从
// novel_context 看到的 ready/missing 与 save_foundation 返回的 foundation_ready
// 永远一致（长篇 compass 必需项等细节不会漂移）。
func (t *ContextTool) foundationStatus() map[string]any {
	missing := t.store.FoundationMissing()
	status := map[string]any{"ready": len(missing) == 0}
	if len(missing) > 0 {
		status["missing"] = missing
	}
	if review, err := t.store.RunMeta.PlanningReview(); err == nil && review != nil && review.FoundationStatus != "" {
		status["review_status"] = review.FoundationStatus
		status["generation"] = review.FoundationGeneration
		status["confirmed"] = t.store.RequireConfirmedFoundation() == nil
	}
	if dependencies, err := t.store.FoundationRevisions.LoadDependencies(); err == nil && dependencies != nil {
		status["dependency_evidence"] = map[string]any{
			"version": dependencies.Version, "signature": dependencies.Signature,
			"foundation_signature": dependencies.FoundationSignature, "count": len(dependencies.Entries),
		}
	}
	if runtime, err := t.store.FoundationRevisions.LoadRuntime(); err == nil && runtime != nil && runtime.Active() {
		status["active_revision"] = map[string]any{"id": runtime.RevisionID, "stage": runtime.Stage, "impact_signature": runtime.Impact.Signature}
	}
	return status
}

// ContextSummary 返回当前状态的简要摘要（供日志使用）。
func (t *ContextTool) ContextSummary() string {
	var parts []string
	if p, _ := t.store.Outline.LoadPremise(); p != "" {
		parts = append(parts, "premise:ok")
	}
	if o, _ := t.store.Outline.LoadOutline(); o != nil {
		parts = append(parts, fmt.Sprintf("outline:%d chapters", len(o)))
	}
	if c, _ := t.store.Characters.Load(); c != nil {
		parts = append(parts, fmt.Sprintf("characters:%d", len(c)))
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, ", ")
}

// buildRelatedChapters 根据结构化数据反查与当前章相关的历史章节。
// 从伏笔、角色出场、状态变化、关系四个维度推荐，去重后最多返回 5 条。
// 所有数据通过参数传入，不做额外 IO。
func (t *ContextTool) buildRelatedChapters(
	chapter int,
	entry *domain.OutlineEntry,
	foreshadow []domain.ForeshadowEntry,
	relationships []domain.RelationshipEntry,
	stateChanges []domain.StateChange,
) []domain.RelatedChapter {
	const recentWindow = 10
	const maxResults = 5

	seen := make(map[int]struct{})
	var results []domain.RelatedChapter
	add := func(ch int, reason string) {
		if ch <= 0 || ch >= chapter {
			return
		}
		// 最近几章太近，不推荐
		if ch > chapter-recentWindow {
			return
		}
		if _, ok := seen[ch]; ok {
			return
		}
		seen[ch] = struct{}{}
		results = append(results, domain.RelatedChapter{Chapter: ch, Reason: reason})
	}

	// 拼接大纲文本用于关键词匹配
	outlineText := entry.Title + " " + entry.CoreEvent
	for _, s := range entry.Scenes {
		outlineText += " " + s
	}

	// 1. 伏笔反查：活跃伏笔的描述是否与当前章大纲相关
	for _, f := range foreshadow {
		if strings.Contains(outlineText, f.ID) || containsAny(outlineText, strings.Fields(f.Description)) {
			add(f.PlantedAt, fmt.Sprintf("伏笔%s(%s)埋设章", f.ID, truncateRunes(f.Description, 15)))
		}
		if len(results) >= maxResults {
			break
		}
	}

	// 2. 角色出场反查：批量单次遍历，IO 从 O(角色数×章节数) 降为 O(章节数)
	chars, _ := t.store.Characters.Load()
	outlineChars := matchOutlineCharacters(outlineText, chars)
	if len(outlineChars) > 0 {
		appearances := t.store.Summaries.FindCharacterAppearances(outlineChars, chapter, recentWindow)
		for _, name := range outlineChars {
			if len(results) >= maxResults {
				break
			}
			if ch, ok := appearances[name]; ok {
				add(ch, fmt.Sprintf("角色'%s'最后出场章", name))
			}
		}
	}

	// 3. 状态变化反查：在已加载的 slice 上操作，零 IO
	for _, name := range outlineChars {
		if len(results) >= maxResults {
			break
		}
		ch := findLastStateChange(stateChanges, name, chapter)
		if ch > 0 && ch <= chapter-recentWindow {
			add(ch, fmt.Sprintf("'%s'状态变化章", name))
		}
	}

	// 4. 关系反查：当前章涉及的角色对之间关系最后变化
	if len(relationships) > 0 && len(outlineChars) >= 2 {
		charSet := make(map[string]struct{}, len(outlineChars))
		for _, c := range outlineChars {
			charSet[c] = struct{}{}
		}
		for _, r := range relationships {
			if len(results) >= maxResults {
				break
			}
			_, aIn := charSet[r.CharacterA]
			_, bIn := charSet[r.CharacterB]
			if aIn && bIn {
				add(r.Chapter, fmt.Sprintf("%s-%s关系变化", r.CharacterA, r.CharacterB))
			}
		}
	}

	return results
}

// findLastStateChange 在已加载的状态变化列表中查找实体最近一次变化的章节号。
func findLastStateChange(changes []domain.StateChange, entity string, currentChapter int) int {
	for i := len(changes) - 1; i >= 0; i-- {
		if changes[i].Entity == entity && changes[i].Chapter < currentChapter {
			return changes[i].Chapter
		}
	}
	return 0
}

// matchOutlineCharacters 从大纲文本中匹配出场角色名。
func matchOutlineCharacters(text string, chars []domain.Character) []string {
	var matched []string
	for _, c := range chars {
		if strings.Contains(text, c.Name) {
			matched = append(matched, c.Name)
			continue
		}
		for _, alias := range c.Aliases {
			if strings.Contains(text, alias) {
				matched = append(matched, c.Name)
				break
			}
		}
	}
	return matched
}

// containsAny 检查 text 是否包含 words 中的任一词（至少 2 字才匹配，避免噪音）。
func containsAny(text string, words []string) bool {
	for _, w := range words {
		if len([]rune(w)) >= 2 && strings.Contains(text, w) {
			return true
		}
	}
	return false
}

func (t *ContextTool) selectStoryThreads(state contextBuildState) []domain.RecallItem {
	if state.currentEntry == nil {
		return nil
	}
	if len(state.foreshadow) < storyThreadRecallThreshold {
		return nil
	}

	const maxThreads = 5
	var items []domain.RecallItem
	seen := make(map[string]struct{})
	picked := make(map[string]struct{}) // 已选中的伏笔 ID，供账龄回填去重
	add := func(item domain.RecallItem) {
		key := item.Kind + "|" + item.Key + "|" + item.Summary
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		picked[item.Key] = struct{}{}
		items = append(items, item)
	}

	// 1. 相关性召回：与当前章 focus 词重叠的伏笔。
	focusTerms := recallFocusTerms(state.currentEntry, state.chapterPlan)
	focusText := strings.Join(focusTerms, " ")
	for _, entry := range state.foreshadow {
		if !matchesRecallTerms(entry.ID+" "+entry.Description, focusTerms) && !strings.Contains(focusText, entry.ID) {
			continue
		}
		add(domain.RecallItem{
			Kind:    "story_thread",
			Key:     entry.ID,
			Chapter: entry.PlantedAt,
			Reason:  "当前章可能需要承接既有伏笔",
			Summary: fmt.Sprintf("伏笔“%s”埋于第%d章：%s", entry.ID, entry.PlantedAt, truncateRunes(entry.Description, 30)),
		})
		if len(items) >= maxThreads {
			return items
		}
	}

	// 2. 账龄回填：与当前章无关、但久挂未回收的伏笔（最旧优先），补足剩余名额。
	//    补的是相关性召回天然的盲区——独自悬挂太久、却没在本章撞上关键词的那根线。
	for _, entry := range agingForeshadow(state.foreshadow, state.chapter, picked) {
		add(domain.RecallItem{
			Kind:    "story_thread",
			Key:     entry.ID,
			Chapter: entry.PlantedAt,
			Reason:  "伏笔久挂未回收，注意适时推进或回收",
			Summary: fmt.Sprintf("伏笔“%s”埋于第%d章，已 %d 章未回收：%s", entry.ID, entry.PlantedAt, state.chapter-entry.PlantedAt, truncateRunes(entry.Description, 30)),
		})
		if len(items) >= maxThreads {
			break
		}
	}

	return items
}

// agingForeshadow 返回账龄 ≥ foreshadowAgingChapters 的未回收伏笔，按最旧优先排序，
// 跳过 picked 中已被相关性召回选中的。入参 all 已是 active（未回收）列表，故无需再过滤状态。
func agingForeshadow(all []domain.ForeshadowEntry, chapter int, picked map[string]struct{}) []domain.ForeshadowEntry {
	var aging []domain.ForeshadowEntry
	for _, e := range all {
		if _, ok := picked[e.ID]; ok {
			continue
		}
		if e.PlantedAt <= 0 || chapter-e.PlantedAt < foreshadowAgingChapters {
			continue
		}
		aging = append(aging, e)
	}
	sort.SliceStable(aging, func(i, j int) bool {
		return aging[i].PlantedAt < aging[j].PlantedAt
	})
	return aging
}

func (t *ContextTool) selectReviewLessons(chapter int, warn func(string, error)) []domain.RecallItem {
	if chapter <= 1 {
		return nil
	}

	var items []domain.RecallItem
	seen := make(map[string]struct{})
	add := func(item domain.RecallItem) {
		key := item.Summary
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		items = append(items, item)
	}

	appendReview := func(review *domain.ReviewEntry) bool {
		if review == nil {
			return false
		}
		for i, miss := range review.ContractMisses {
			add(domain.RecallItem{
				Kind:    "review_lesson",
				Key:     fmt.Sprintf("review-%d-contract-%d", review.Chapter, i),
				Chapter: review.Chapter,
				Reason:  "最近审阅指出 contract 漏项",
				Summary: fmt.Sprintf("第%d章 contract 漏项：%s", review.Chapter, miss),
			})
			if len(items) >= 3 {
				return true
			}
		}
		for i, issue := range review.Issues {
			switch issue.Severity {
			case "", "warning", "error", "critical":
				add(domain.RecallItem{
					Kind:    "review_lesson",
					Key:     fmt.Sprintf("review-%d-issue-%d", review.Chapter, i),
					Chapter: review.Chapter,
					Reason:  "最近审阅指出需要避免重复问题",
					Summary: fmt.Sprintf("第%d章审阅提醒：%s", review.Chapter, truncateRunes(issue.Description, 36)),
				})
			}
			if len(items) >= 3 {
				return true
			}
		}
		return false
	}

	for ch := chapter - 1; ch >= max(chapter-3, 1); ch-- {
		review, err := t.store.World.LoadReview(ch)
		if err != nil {
			warn("review", err)
			continue
		}
		if appendReview(review) {
			return items
		}
	}

	globalReview, err := t.store.World.LoadLastReview(chapter - 1)
	if err != nil {
		warn("global_review", err)
	} else if appendReview(globalReview) {
		return items
	}
	return items
}

func recallFocusTerms(entry *domain.OutlineEntry, plan *domain.ChapterPlan) []string {
	if entry == nil {
		return nil
	}
	var terms []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			terms = append(terms, v)
		}
	}

	add(entry.Title)
	add(entry.CoreEvent)
	add(entry.Hook)
	for _, scene := range entry.Scenes {
		add(scene)
	}
	if plan != nil {
		add(plan.Goal)
		add(plan.Hook)
		for _, point := range plan.Contract.PayoffPoints {
			add(point)
		}
		add(plan.Contract.HookGoal)
	}
	return terms
}

func matchesRecallTerms(text string, terms []string) bool {
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if len([]rune(term)) < 2 {
			continue
		}
		if strings.Contains(text, term) || strings.Contains(term, text) {
			return true
		}
		if hasMeaningfulOverlap(term, text) {
			return true
		}
	}
	return false
}

func hasMeaningfulOverlap(a, b string) bool {
	ar := []rune(strings.TrimSpace(a))
	br := []rune(strings.TrimSpace(b))
	if len(ar) < 5 || len(br) < 5 {
		return false
	}
	shorter := len(ar)
	if len(br) < shorter {
		shorter = len(br)
	}
	threshold := 5
	switch {
	case shorter >= 12:
		threshold = 7
	case shorter >= 9:
		threshold = 6
	}
	return longestCommonSubstringRunes(ar, br) >= threshold
}

const storyThreadRecallThreshold = 6
const storyThreadRecallMinSelected = 2

// foreshadowAgingChapters：一条伏笔自埋设起超过这么多章仍未回收，视为"久挂"。
// 这类伏笔即使与当前章关键词无关，也回填进 story_threads，避免长篇里被彻底遗忘
// （相关性召回天然只看见与本章相关的线，看不见独自悬挂太久的那根）。
// 账龄是纯代码派生的事实（当前章 - 埋设章），只陈述"已挂 N 章未回收"，不下指令。
const foreshadowAgingChapters = 30

func longestCommonSubstringRunes(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	best := 0
	for i := 1; i <= len(a); i++ {
		curr := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] != b[j-1] {
				continue
			}
			curr[j] = prev[j-1] + 1
			if curr[j] > best {
				best = curr[j]
			}
		}
		prev = curr
	}
	return best
}

// truncateRunes 截断字符串到指定 rune 数。
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
