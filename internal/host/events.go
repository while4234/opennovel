package host

import (
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Event 是 TUI 消费的结构化事件。
//
// 对于 TOOL / DISPATCH 两类调用事件，同一次调用的开始与结束共用一个 ID：
// 开始时先发 FinishedAt 为零值的事件（TUI 渲染为"进行中"样式）；
// 结束时再发一条同 ID 的事件，填入 FinishedAt + Duration（+ Failed），
// TUI 按 ID 定位原行原地更新，避免"开始一行、完成又一行"的冗余。
//
// SYSTEM / ERROR / CONTEXT 等非调用类事件 ID 为空，每条独立追加。
type Event struct {
	ID         string    // 同一次调用的开始/结束共用；非调用事件为空
	Time       time.Time // 首次发出时间（开始时刻）
	FinishedAt time.Time // 零值 = 进行中；非零 = 已完成
	Failed     bool      // 已完成但失败（仅完成态有意义）
	Category   string    // DISPATCH / TOOL / SYSTEM / REVIEW / CHECK / ERROR / CONTEXT
	Agent      string    // 产生事件的 agent
	Summary    string
	Detail     string        // 完整文案，写入日志不截断供排查；为空回退 Summary。UI 只读 Summary
	Kind       string        // 错误分类（如 stream_idle），随日志输出供过滤/告警；为空不输出
	Level      string        // info / warn / error / success
	Depth      int           // 0 = coordinator 层, 1 = sub-agent 层
	Duration   time.Duration // 完成时的执行耗时
	Current    int           // 可选的 Web/长任务进度当前值
	Total      int           // 可选的 Web/长任务进度总值
}

// Running 返回事件是否处于进行中。
// 仅调用类事件（有 ID 的 TOOL / DISPATCH）可能进行中；其它类型总是返回 false。
func (e Event) Running() bool {
	return e.ID != "" && e.FinishedAt.IsZero()
}

// UISnapshot 是 TUI 渲染所需的聚合状态快照。
type UISnapshot struct {
	Provider           string
	NovelName          string
	ModelName          string
	ModelContextWindow int // 当前默认模型的上下文窗口（随 /model 切换实时解析）
	Style              string
	SimulationMode     string
	RuntimeState       string // idle / running / pausing / paused / completed
	StatusLabel        string
	Phase              string
	Flow               string
	CurrentChapter     int
	TotalChapters      int
	CompletedCount     int
	TotalWordCount     int
	WordBudget         *domain.WordBudget
	InProgressChapter  int
	PendingRewrites    []int
	RewriteReason      string
	PendingSteer       string
	RecoveryLabel      string
	RecoveryError      string
	IsRunning          bool
	Agents             []AgentSnapshot

	// 上下文
	ContextTokens         int
	ContextWindow         int
	ContextPercent        float64
	ContextScope          string
	ContextStrategy       string
	ContextActiveMessages int
	ContextSummaryCount   int
	ContextCompactedCount int
	ContextKeptCount      int

	// 累计用量（整个会话，跨所有 agent 与模型切换）
	TotalInputTokens      int
	TotalOutputTokens     int
	TotalCacheReadTokens  int
	TotalCacheWriteTokens int
	TotalCostUSD          float64
	TotalSavedUSD         float64 // 因 CacheRead 命中省下的美元（相对全按非缓存输入价计费）
	BudgetLimitUSD        float64 // 预算上限（config budget.book_usd）；0 = 未启用

	// 缓存诊断
	OverallCacheCapable    bool // 至少一个 role 跑过支持 prompt cache 的模型（区分"未启用"和"0% 命中"）
	OverallRecentCacheRead int  // 滑动窗最近 N 次的 cacheRead 总和
	OverallRecentInput     int  // 滑动窗最近 N 次的 input 总和
	OverallRecentSamples   int  // 滑动窗内的样本数（≤ recentSampleCap）

	// MissingAssistantUsage > 0 通常意味着上游 streaming 没按 OpenAI
	// stream_options.include_usage 协议发 final usage chunk（自建 proxy 常见），
	// 导致 UsageTracker 收不到任何累计数据。UI 据此明示用户排查 backend，
	// 不要让用户误以为是缓存模块本身坏了。
	MissingAssistantUsage int

	// 缓存 per-role 维度，按 CacheRead 降序，已过滤未消费 token 的 role
	CachePerAgent []AgentCacheStat
	CachePerModel []AgentCacheStat

	// 基础设定
	Premise                    string
	PremiseFull                string
	Outline                    []OutlineSnapshot
	Characters                 []string
	CharacterDetails           []domain.Character
	WorldRules                 []domain.WorldRule
	PlannedRelationships       []domain.CharacterRelationship
	FoundationAuditSignature   string
	CoreCharacterIDs           []string
	CoreCastPreserved          bool
	SupportingCount            int      // 配角名册中的次要角色总数
	RecentSupporting           []string // 最近活跃的次要角色（最多 5 个，按 LastSeenChapter 倒序）
	Layered                    bool
	LayeredOutline             []LayeredVolumeSnapshot
	CurrentVolumeArc           string
	NextVolumeTitle            string
	CompassDirection           string
	CompassScale               string
	SimulationProfile          *domain.SimulationCompactProfile
	SimulationSummary          *SimulationProfileSummary
	CreativeBlueprint          *CreativeBlueprintSummary
	PlanningReview             *PlanningReviewSummary
	CharacterWorkflow          *CharacterWorkflowSummary
	Continuation               *domain.ContinuationSnapshot
	AdaptationVolumeReview     *domain.AdaptationVolumeReview
	AdaptationProposal         *domain.AdaptationPlan
	AdaptationPlan             *domain.AdaptationPlan
	VolumeReviewSummary        *AdaptationVolumeReviewSummary
	ProposalSummary            *AdaptationPlanSummary
	AdaptationSummary          *AdaptationPlanSummary
	AdaptationSourceFoundation *domain.AdaptationSourceFoundation
	AdaptationCoreCast         *domain.CoreCastContract
	TargetFoundation           *domain.StoryFoundation
	AdaptationFoundationReview *domain.AdaptationFoundationReview
	AdaptationPlanningWorkflow *domain.AdaptationPlanningWorkflow

	// 详情
	LastCommitSummary  string
	LastReviewSummary  string
	LastCheckpointName string
	RecentSummaries    []string
}

// OutlineSnapshot 是大纲条目的展示摘要。
type CharacterWorkflowSummary struct {
	Candidate          *domain.StoryFoundation                  `json:"candidate,omitempty"`
	CandidateRevision  int64                                    `json:"candidate_revision"`
	CandidateDigest    string                                   `json:"candidate_digest,omitempty"`
	AnalysisStatus     domain.CharacterCardAnalysisStatus       `json:"analysis_status"`
	ReviewStatus       domain.CharacterCardReviewStatus         `json:"review_status"`
	ConfirmationStatus domain.CharacterCardConfirmationStatus   `json:"confirmation_status"`
	Completeness       []domain.CharacterCardCompletenessResult `json:"completeness,omitempty"`
	Findings           []domain.CharacterCardReviewFinding      `json:"findings,omitempty"`
	Error              *domain.CharacterCardError               `json:"error,omitempty"`
	StateError         string                                   `json:"state_error,omitempty"`
}

type OutlineSnapshot struct {
	Chapter          int
	Title            string
	CoreEvent        string
	Hook             string
	Scenes           []string
	WrittenWordCount int
	WordBudget       *ChapterBudgetSnapshot
	SourceCoverage   *SourceCoverageSnapshot
	PreserveEvents   []string
	RequiredChanges  []string
	ForbiddenMoves   []string
	CoverageNote     string
}

type SimulationProfileSummary struct {
	Loaded          bool                       `json:"loaded"`
	Version         string                     `json:"version,omitempty"`
	ProfileDigest   string                     `json:"profile_digest,omitempty"`
	UpdatedAt       string                     `json:"updated_at,omitempty"`
	SourceCount     int                        `json:"source_count"`
	ReportCount     int                        `json:"report_count"`
	CoveragePercent int                        `json:"coverage_percent"`
	HealthState     string                     `json:"health_state"`
	HealthReasons   []string                   `json:"health_reasons,omitempty"`
	AnalysisSigned  bool                       `json:"analysis_signed"`
	SynthesisSigned bool                       `json:"synthesis_signed"`
	Portable        bool                       `json:"portable"`
	LocalEvidence   bool                       `json:"local_evidence"`
	SafetyIndex     bool                       `json:"safety_index"`
	FeatureCounts   SimulationFeatureCounts    `json:"feature_counts"`
	SelectedMode    string                     `json:"selected_mode"`
	EffectiveMode   string                     `json:"effective_mode"`
	EffectiveReason string                     `json:"effective_reason,omitempty"`
	Contract        *SimulationContractSummary `json:"contract,omitempty"`
	Check           *SimulationCheckSummary    `json:"check,omitempty"`
	Actions         SimulationActionSummary    `json:"actions"`
	ModePreviews    []SimulationModePreview    `json:"mode_previews,omitempty"`
	DiagnosticError string                     `json:"diagnostic_error,omitempty"`
}

type SimulationFeatureCounts struct {
	Total         int `json:"total"`
	Stable        int `json:"stable"`
	Local         int `json:"local"`
	Outlier       int `json:"outlier"`
	Contradictory int `json:"contradictory"`
}

type SimulationActionCapability struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

type SimulationActionSummary struct {
	Rescan       SimulationActionCapability `json:"rescan"`
	Resynthesize SimulationActionCapability `json:"resynthesize"`
	Reanalyze    SimulationActionCapability `json:"reanalyze"`
}

type SimulationModePreview struct {
	Mode   string                  `json:"mode"`
	Status string                  `json:"status"`
	Reason string                  `json:"reason,omitempty"`
	Roles  []SimulationRolePreview `json:"roles,omitempty"`
}

type SimulationRolePreview struct {
	Role         string   `json:"role"`
	Phase        string   `json:"phase"`
	FeatureCount int      `json:"feature_count"`
	Must         int      `json:"must"`
	Should       int      `json:"should"`
	Avoid        int      `json:"avoid"`
	ByteBudget   int      `json:"byte_budget"`
	Dimensions   []string `json:"dimensions,omitempty"`
}

type SimulationContractSummary struct {
	Revision           int64                    `json:"revision"`
	Status             string                   `json:"status"`
	Current            bool                     `json:"current"`
	StaleReason        string                   `json:"stale_reason,omitempty"`
	ProfileDigest      string                   `json:"profile_digest,omitempty"`
	FoundationRevision int64                    `json:"foundation_revision"`
	CreativeBriefBound bool                     `json:"creative_brief_bound"`
	ExclusionCount     int                      `json:"exclusion_count"`
	ExclusionReasons   map[string]int           `json:"exclusion_reasons,omitempty"`
	Views              []SimulationContractView `json:"views,omitempty"`
}

type SimulationContractView struct {
	Role       string                     `json:"role"`
	Phase      string                     `json:"phase"`
	Must       int                        `json:"must"`
	Should     int                        `json:"should"`
	Avoid      int                        `json:"avoid"`
	ByteBudget int                        `json:"byte_budget"`
	Features   []SimulationFeatureSummary `json:"features,omitempty"`
}

type SimulationFeatureSummary struct {
	ID        string `json:"id"`
	Dimension string `json:"dimension"`
	Statement string `json:"statement"`
	Level     string `json:"level"`
}

type SimulationCheckSummary struct {
	State            string                  `json:"state"`
	Reason           string                  `json:"reason,omitempty"`
	Chapter          int                     `json:"chapter,omitempty"`
	CheckedAt        string                  `json:"checked_at,omitempty"`
	DraftCurrent     bool                    `json:"draft_current"`
	Capability       string                  `json:"capability,omitempty"`
	CapabilityReason string                  `json:"capability_reason,omitempty"`
	CopyStatus       string                  `json:"copy_status,omitempty"`
	ContractStatus   string                  `json:"contract_status,omitempty"`
	RiskCount        int                     `json:"risk_count"`
	MustTotal        int                     `json:"must_total"`
	MustMet          int                     `json:"must_met"`
	MustMissing      int                     `json:"must_missing"`
	AdvisoryCount    int                     `json:"advisory_count"`
	Risks            []SimulationRiskSummary `json:"risks,omitempty"`
}

type SimulationRiskSummary struct {
	Type         string `json:"type"`
	DraftExcerpt string `json:"draft_excerpt"`
	StartRune    int    `json:"start_rune"`
	LengthRunes  int    `json:"length_runes"`
}

type CreativeBlueprintSummary struct {
	Loaded           bool
	NovelName        string
	Premise          string
	OutlineChapters  int
	CharacterCount   int
	WorldRuleCount   int
	Layered          bool
	CompassDirection string
	CompassScale     string
}

type PlanningReviewSummary struct {
	Loaded                   bool
	Status                   string
	Kind                     string
	Brief                    string
	TargetTotalWords         int
	CreatedAt                string
	UpdatedAt                string
	FoundationStatus         string
	FoundationRevision       int64
	FoundationAuditSignature string
	CoreCastSignature        string
	FoundationGeneration     int64
	FoundationBaseRevision   int64
	FoundationFeedback       string
	FoundationConfirmedAt    string
}

type AdaptationPlanSummary struct {
	Loaded            bool
	Status            string
	Granularity       string
	RewritePolicy     string
	Brief             string
	Volumes           []domain.AdaptationVolumePlan
	ChapterCount      int
	SourceTotalRunes  int
	TargetTotalRunes  int
	TargetMinRunes    int
	TargetMaxRunes    int
	WordTolerance     float64
	MainlineRules     []string
	RelationshipGoals []string
}

type AdaptationVolumeReviewSummary struct {
	Loaded             bool
	Status             string
	Granularity        string
	RewritePolicy      string
	Brief              string
	Volumes            []domain.AdaptationVolumePlan
	TargetChapterCount int
	WordTolerance      float64
	MainlineRules      []string
	RelationshipGoals  []string
}

type ChapterBudgetSnapshot struct {
	TargetWords int
	MinWords    int
	MaxWords    int
	SourceRunes int
	TargetRunes int
	MinRunes    int
	MaxRunes    int
	Tolerance   float64
}

type SourceCoverageSnapshot struct {
	Chapters []int
	From     int
	To       int
	Runes    int
	IsAdded  bool
	Note     string
}

type LayeredVolumeSnapshot struct {
	Index        int
	Title        string
	Theme        string
	TargetFrom   int
	TargetTo     int
	ChapterCount int
}

// AgentSnapshot 是 Agent 状态的展示投影。
type AgentSnapshot struct {
	Name      string
	State     string
	TaskID    string
	TaskKind  string
	Summary   string
	Tool      string
	Turn      int
	Context   AgentContextSnapshot
	UpdatedAt time.Time
}

// AgentCacheStat 是单个 agent 的缓存命中累计（投影到左栏）。
// HitRate = CacheRead / Input；Input 在 litellm 层已统一为"含 CacheRead"语义。
//
// CacheCapable 用来区分两种 0% 命中：
//   - true  → 模型支持 prompt cache，0% 是 prompt 设计差或前缀不稳定，需要优化
//   - false → 模型/provider 不支持 prompt cache，0% 是预期，不必排查
//
// Recent* 是滑动窗（最近 N 次调用）的命中数据，对比累计可识别"前期拖累"vs"稳态低命中"。
type AgentCacheStat struct {
	Role            string
	Model           string
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	Cost            float64
	Saved           float64
	CacheCapable    bool
	RecentCacheRead int
	RecentInput     int
	RecentSamples   int
}

// AgentContextSnapshot 是 Agent 上下文使用情况。
type AgentContextSnapshot struct {
	Tokens          int
	ContextWindow   int
	Percent         float64
	Scope           string
	Strategy        string
	ActiveMessages  int
	SummaryMessages int
	CompactedCount  int
	KeptCount       int
}

// CoCreateMessage 是共创对话的消息。
type CoCreateMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CoCreateReply 是共创对话的 LLM 回复。Raw 保留模型完整四段原文，
// 用于写回 history 让下一轮模型看到自己上一轮的 [DRAFT]，从而真正在
// 已有草稿上累积更新（仅 Message 不含 [DRAFT]，会导致模型每轮凭对话重新归纳）。
// Suggestions 是 AI 主动给的"接下来你可能想说"，用户卡壳时按数字键一键填入输入框。
type CoCreateReply struct {
	Message     string
	Prompt      string
	CoreCast    *domain.CoreCastContract
	Ready       bool
	Suggestions []string
	Raw         string
}

// ReplayDeltaText 从运行时队列项中提取可回放的流式文本。
func ReplayDeltaText(item domain.RuntimeQueueItem) string {
	if payload, ok := item.Payload.(map[string]any); ok {
		if text, ok := payload["delta"].(string); ok {
			return text
		}
	}
	return ""
}

// BuildStartPrompt 将用户需求包装为 Coordinator 的启动 prompt。
// BuildStartPrompt wraps the user request for Coordinator startup.
func BuildStartPrompt(prompt string) string {
	return BuildStartPromptWithBudget(prompt, nil)
}

// BuildStartPromptWithBudget wraps the user request and makes any full-book
// length contract explicit before planning starts.
func BuildStartPromptWithBudget(prompt string, budget *domain.WordBudget) string {
	prompt = strings.TrimSpace(prompt)
	contract := formatWordBudgetStartBlock(budget)
	return "请根据以下创作要求开始创作一部小说。进入规划后，premise 第一行必须输出 `# 书名`。章节数量由你根据故事需要自行决定；若题材与冲突天然适合长篇连载，请优先规划为分层长篇结构，而不是压缩成短篇式梗概。\n\n" +
		contract +
		"[创作要求]\n" +
		prompt +
		"\n\n若某些细节未明确，请在不违背用户方向的前提下自行补全。"
}

func formatWordBudgetStartBlock(budget *domain.WordBudget) string {
	if budget == nil {
		return ""
	}
	normalized, ok := budget.NormalizedNoChapterRecalc()
	if !ok {
		return ""
	}
	return fmt.Sprintf("[篇幅契约]\n- target_total_words=%d，这是用于规划章节规模的全书总字数锚点，不是必须精确命中的硬上限，更不是每章字数。\n- total_min_words=%d，total_max_words=%d 是规划与进度预警参考；章节因完整剧情需要而合理膨胀时，不得为了追回总字数而压缩后续章节。\n- 常规小说单章正文约 3000-5000 字；planned_chapters 必须按 target_total_words / 3000-5000 估算。\n- 若 target_total_words <= 8000，默认按一篇连续短篇规划，不分章节；除非用户明确要求分章节，outline 只保存 1 个正文条目。\n- 先决定 planned_chapters，再通过 save_foundation 落盘大纲；系统会据此计算每章推荐区间并在写作阶段注入 working_memory.word_budget。\n- recommended_min_words / recommended_max_words 和用户声明的单章范围都是配速软建议，不是机械删改门槛。优先保持章节契约、完整场景因果和人物情感质量；只有越过更宽的异常膨胀安全边界时，才需要在提交前进行局部修复。\n\n",
		normalized.TargetTotalWords, normalized.TotalMinWords, normalized.TotalMaxWords)
}

// BuildAdaptationStartPrompt tells Coordinator that foundation and adaptation
// plan have already been prepared, so it should enter the normal writing flow.
func BuildAdaptationStartPrompt(plan domain.AdaptationPlan) string {
	return "当前项目是小说改编模式。原书 source snapshot、改编计划、foundation 和分层大纲已经落盘，不要重新从零规划，也不要把原文章节提交为正文。\n\n" +
		"[改编 brief]\n" + strings.TrimSpace(plan.Brief) + "\n\n" +
		"[当前有效改编模式]\n" + formatAdaptationRuntimeMode(plan) + "\n\n" +
		"[执行要求]\n" +
		adaptationRuntimeExecutionRequirements(plan) +
		"- 每章提交前必须完成 check_consistency 和 check_adaptation；check_adaptation 未通过不得 commit。\n" +
		"- 若连续校验失败，请暂停并报告失败章节和原因，不要静默跳过主线校验。\n\n" +
		"改编粒度：" + plan.Granularity
}

func formatAdaptationRuntimeMode(plan domain.AdaptationPlan) string {
	granularity := domain.NormalizeAdaptationGranularity(plan.Granularity)
	rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
	return fmt.Sprintf("granularity=%s\nrewrite_policy=%s\nmode_contract=%s/%s", granularity, rewritePolicy, granularity, rewritePolicy)
}

func adaptationRuntimeExecutionRequirements(plan domain.AdaptationPlan) string {
	granularity := domain.NormalizeAdaptationGranularity(plan.Granularity)
	switch granularity {
	case domain.AdaptationGranularityFree:
		return "- 立即按当前 progress/outline 派发 writer 从第 1 章开始逐章写改编正文。\n" +
			"- writer 必须先读 novel_context，并以 working_memory.adaptation_effective_mode 与 adaptation_contract 为准。\n" +
			"- 当前是 free/full_rewrite：source_chapters/source_range 只是后台覆盖率和背景锚点，不表示目标章对应原著章节，也不得把章节说成 preserve_details。\n" +
			"- 不要因为存在 source refs 就反复读取原文章节；只有缺少必要事实时，才按需读取一次 read_chapter(source=\"source\")。\n"
	case domain.AdaptationGranularityArc:
		return "- 立即按当前 progress/outline 派发 writer 从第 1 章开始逐章写改编正文。\n" +
			"- writer 必须先读 novel_context，并以 working_memory.adaptation_effective_mode 与 adaptation_contract 为准。\n" +
			"- 当前是 arc/full_rewrite：source_chapters/source_range 是主线与弧线锚点，必要时读取原文核对因果，但正文必须原创重写。\n"
	default:
		return "- 立即按当前 progress/outline 派发 writer 从第 1 章开始逐章写改编正文。\n" +
			"- writer 必须先读 novel_context，再按 adaptation_contract 读取 read_chapter(source=\"source\") 对照原文。\n" +
			"- 当前是 chapter/preserve_details：目标章与原文章节一一对应，未受影响的承接细节可以保留，受影响的完整场景单元必须原创重写。\n"
	}
}
