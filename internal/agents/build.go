package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/host/flow"
	"github.com/voocel/ainovel-cli/internal/host/reminder"
	"github.com/voocel/ainovel-cli/internal/modelprofile"
	"github.com/voocel/ainovel-cli/internal/promptcompile"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
	"github.com/voocel/ainovel-cli/internal/userrules"
)

// agentToRole 把 subagent name 归一为 ModelSet 认得的 role 名。
// architect_short / architect_long 都共用同一个 architect role 配置。
// 跟 host.agentRoleName 同义，因为 build 与 host 互不依赖故各持一份。
func agentToRole(name string) string {
	if strings.HasPrefix(name, "architect_") {
		return "architect"
	}
	return name
}

// subagentMaxRetries 给所有 SubAgentConfig 与 Coordinator 统一的 LLM retry 上限。
// 退避策略：指数退避（受 maxDelay 上限约束），优先服从 server Retry-After。
// 配合 ToolsAreIdempotent=true 让 stream-idle / 503 / 短暂网络抖动这类 retryable
// 错误能在 subagent 层就近重试，而不是把整个 subagent 抛回 coordinator 重派发。
// 项目铁律一保证写类工具走 checkpoint+digest 幂等，重试是安全的。
const subagentMaxRetries = retrypolicy.MaxAttempts

func boundedAgentContextWindow(modelName string, modelWindow int, agent promptcompile.Agent) (int, int) {
	if modelWindow <= 0 {
		return modelWindow, bootstrap.CompactReserveTokens(modelWindow)
	}
	window := modelWindow
	profile := modelprofile.Resolve(modelName)
	if roleWindow := profile.ContextWindow(modelProfileRole(agent)); roleWindow > 0 {
		window = min(window, roleWindow)
	}
	reserve := bootstrap.CompactReserveTokens(window)
	slog.Info("角色运行时上下文窗口",
		"module", "context",
		"role", agent,
		"profile", profile.Name,
		"model", modelName,
		"model_window", modelWindow,
		"runtime_window", window,
		"reserve", reserve,
	)
	return window, reserve
}

func modelProfileRole(agent promptcompile.Agent) modelprofile.Role {
	switch agent {
	case promptcompile.AgentCoordinator:
		return modelprofile.RoleCoordinator
	case promptcompile.AgentArchitect:
		return modelprofile.RoleArchitect
	case promptcompile.AgentCharacter:
		return modelprofile.RoleCharacter
	case promptcompile.AgentWriter:
		return modelprofile.RoleWriter
	case promptcompile.AgentEditor:
		return modelprofile.RoleEditor
	default:
		return ""
	}
}

// UsageRecorder 是 BuildCoordinator 可选的用量回调；签名与 OnMessage 一致，
// 每条 agent 消息都会调一次，由 Host 层负责聚合。nil 表示不追踪。
type UsageRecorder func(agentName string, msg agentcore.AgentMessage)

// FlowBoundaryHook runs synchronously after a Coordinator tool that advances
// the durable story state succeeds. Host uses it to queue the next flow
// instruction before the Coordinator gets another LLM turn.
type FlowBoundaryHook func(toolName string)

// ApplyThinking 把某具体角色的推理强度应用到 live agent（运行时 /model 调整用）。
// coordinator → Agent.SetThinkingLevel；architect → 两个 architect_* 子代理；
// writer/editor → 对应子代理。空 level = 沿用模型/provider 默认。其它 role 名忽略。
type ApplyThinking func(role string, level agentcore.ThinkingLevel)

// ParseThinkingLevel 把配置字符串转 agentcore.ThinkingLevel。
// "" 合法（= 不覆盖/继承）；其余须是 off/low/medium/high/xhigh/max 之一，
// 否则返回 error（启动时降级当空并 warn，运行时把 error 回显给用户）。
func ParseThinkingLevel(s string) (agentcore.ThinkingLevel, error) {
	lv := agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(s))
	switch lv {
	case "", agentcore.ThinkingOff, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax:
		return lv, nil
	default:
		return "", fmt.Errorf("无效推理强度 %q（可选：off/low/medium/high/xhigh/max）", s)
	}
}

func ResolveThinkingForModel(model agentcore.ChatModel, level agentcore.ThinkingLevel) (agentcore.ThinkingLevel, bool) {
	return llm.ThinkingPolicyFor(model).Resolve(level)
}

func AvailableThinkingForModel(model agentcore.ChatModel) []agentcore.ThinkingLevel {
	return llm.ThinkingPolicyFor(model).Available
}

// roleThinking 解析某角色生效的推理强度；非法值降级为空（不覆盖）并 warn。
func roleThinking(cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	lv, err := ParseThinkingLevel(cfg.ResolveReasoningEffort(role))
	if err != nil {
		slog.Warn("忽略无效推理强度配置", "module", "agent", "role", role, "err", err)
		return ""
	}
	return lv
}

func resolvedRoleThinking(model agentcore.ChatModel, cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	resolved, _ := ResolveThinkingForModel(model, roleThinking(cfg, role))
	return resolved
}

// BuildCoordinator 组装 Coordinator Agent 及其 SubAgent。
// 返回 Agent、AskUserTool、WriterRestorePack、Coordinator 的 ContextEngine 引用，
// 以及 ApplyThinking 闭包——Host 层 /model 切换时需要直接调 SetContextWindow +
// SetReserveTokens 联动新模型的窗口（writer/architect/editor 走 ContextManagerFactory
// 自动重建，不需要 ref；只有常驻的 coordinator 需要），并通过 ApplyThinking 联动各角色
// 推理强度。Host 层通过 Agent.Subscribe 获取事件流,不再需要 emit 回调。
func BuildCoordinator(
	cfg bootstrap.Config,
	store *store.Store,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	planningReviews *tools.PlanningReviewRunRegistry,
	recordUsage UsageRecorder,
	onFlowBoundary FlowBoundaryHook,
	onSummaryRetry SummaryRetryHook,
	onFailover bootstrap.FailoverReporter,
) (*agentcore.Agent, *subagent.Tool, *tools.AskUserTool, *ctxpack.WriterRestorePack, *corecontext.ContextEngine, ApplyThinking, error) {
	// 共享工具
	coordinatorContextTool := tools.NewContextToolWithOptions(store, bundle.References, cfg.Style, tools.ContextToolOptions{
		SimulationMode: cfg.EffectiveSimulationMode(),
		Role:           domain.SimulationRoleCoordinator,
	})
	architectContextTool := tools.NewContextToolWithOptions(store, bundle.References, cfg.Style, tools.ContextToolOptions{
		SimulationMode: cfg.EffectiveSimulationMode(),
		Role:           domain.SimulationRoleArchitect,
	})
	writerRoleContextTool := tools.NewContextToolWithOptions(store, bundle.References, cfg.Style, tools.ContextToolOptions{
		SimulationMode: cfg.EffectiveSimulationMode(),
		Role:           domain.SimulationRoleWriter,
	})
	editorContextTool := tools.NewContextToolWithOptions(store, bundle.References, cfg.Style, tools.ContextToolOptions{
		SimulationMode:  cfg.EffectiveSimulationMode(),
		Role:            domain.SimulationRoleEditor,
		PlanningReviews: planningReviews,
	})
	// 用户规则服务：归一化各来源 → 确定性合并 → 落盘本书快照。Coordinator 的
	// save_user_rules 工具复用它做运行中更新；归一化用 Default 模型（与 Host 开书侧一致）。
	userRulesSvc := userrules.NewService(store, models.Default, rules.DefaultOptions())
	readChapter := tools.NewReadChapterTool(store)
	askUser := tools.NewAskUserTool()
	completionGate := adapt.NewCompletionGate(store)
	simulationCheck := tools.NewSimulationCheckService(store, cfg.EffectiveSimulationMode())

	architectTools := []agentcore.Tool{
		architectContextTool,
		newFoundationRetryBoundaryTool(
			revisionFenceWrites(store.Revisions, tools.NewArchitectSaveFoundationTool(store, completionGate)),
		),
	}
	characterRuns := tools.NewCharacterRunRegistry()
	characterTools := []agentcore.Tool{
		tools.NewCharacterContextTool(store, characterRuns),
		revisionFenceWrites(store.Revisions, tools.NewSaveCharacterCandidateTool(store, characterRuns)),
		revisionFenceWrites(store.Revisions, tools.NewSaveCharacterReviewTool(store, characterRuns)),
		revisionFenceWrites(store.Revisions, tools.NewSaveCastPromotionCandidateTool(store)),
		revisionFenceWrites(store.Revisions, tools.NewSaveCastPromotionReviewTool(store)),
	}
	var writerTools []agentcore.Tool
	editorTools := []agentcore.Tool{
		editorContextTool,
		readChapter,
		revisionFenceWrites(store.Revisions, tools.NewCheckSimulationTool(simulationCheck)),
		revisionFenceWrites(store.Revisions, tools.NewSaveOriginalPlanningAuditTool(store, planningReviews)),
		revisionFenceWrites(store.Revisions, tools.NewSaveReviewTool(store)),
		revisionFenceWrites(store.Revisions, tools.NewSaveArcSummaryTool(store)),
		revisionFenceWrites(store.Revisions, tools.NewSaveVolumeSummaryTool(store)),
	}
	if err := validateAgentToolRegistry("architect", architectTools,
		"novel_context", "save_foundation"); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if err := validateAgentToolRegistry("editor", editorTools,
		"novel_context", "read_chapter", "check_simulation", "save_original_planning_audit", "save_review", "save_arc_summary", "save_volume_summary"); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if err := validateAgentToolRegistry("character", characterTools,
		"character_context", "save_character_candidate", "save_character_review",
		"save_cast_promotion_candidate", "save_cast_promotion_review"); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}

	reportFailover := func(ev bootstrap.FailoverEvent) {
		slog.Warn("provider 切换",
			"module", "agent",
			"role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err,
		)
		if onFailover != nil {
			onFailover(ev)
		}
	}

	// Architect 的一次自主 run 可能连续完成设定、骨架和首批细纲，不能在
	// run 中途换模型；以骨架规划阶段为主路由，未配置时继承 architect。
	architectModel := models.ForStageWithFailover(bootstrap.StageSkeleton, reportFailover)
	characterAnalysisModel := models.ForStageWithFailover(bootstrap.StageCharacterAnalysis, func(ev bootstrap.FailoverEvent) {
		ev.Role = bootstrap.StageRouteKey(bootstrap.StageCharacterAnalysis)
		reportFailover(ev)
	})
	characterReviewModel := models.ForStageWithFailover(bootstrap.StageCharacterReview, func(ev bootstrap.FailoverEvent) {
		ev.Role = bootstrap.StageRouteKey(bootstrap.StageCharacterReview)
		reportFailover(ev)
	})
	writerModel := models.ForStageWithFailover(bootstrap.StageWriting, reportFailover)
	editorModel := models.ForStageWithFailover(bootstrap.StageReview, reportFailover)
	coordinatorModel := models.ForRoleWithFailover("coordinator", reportFailover)
	architectModel = NewToolCallRepairModel(architectModel)
	characterAnalysisModel = NewToolCallRepairModel(characterAnalysisModel)
	characterReviewModel = NewToolCallRepairModel(characterReviewModel)
	writerModel = NewToolCallRepairModel(writerModel)
	editorModel = NewToolCallRepairModel(editorModel)
	coordinatorModel = NewToolCallRepairModel(coordinatorModel)
	architectShortModel := withProductionAgentBoundary(architectModel, store, "agent_architect_short")
	architectLongModel := withProductionAgentBoundary(architectModel, store, "agent_architect_long")
	characterModel := newCharacterModeModel(characterAnalysisModel, characterReviewModel)
	characterRuntimeModel := withProductionAgentBoundary(characterModel, store, "agent_character")
	writerModel = withProductionAgentBoundary(writerModel, store, "agent_writer")
	continuityAuditorModel := withProductionAgentBoundary(editorModel, store, "continuity_auditor")
	editorModel = withProductionAgentBoundary(editorModel, store, "agent_editor")
	coordinatorModel = withProductionAgentBoundary(coordinatorModel, store, "agent_coordinator")
	coordinatorModel = WithExecutionBarrierModel(coordinatorModel)
	writerTools = []agentcore.Tool{
		newWriterContextTool(writerRoleContextTool, store),
		newWriterReadChapterTool(readChapter, store),
		revisionFenceWrites(store.Revisions, newWriterChapterInferenceTool(tools.NewPlanChapterTool(store), store)),
		revisionFenceWrites(store.Revisions, newWriterDraftChapterTool(store)),
		revisionFenceWrites(store.Revisions, newWriterChapterInferenceTool(tools.NewEditChapterTool(store), store)),
		revisionFenceWrites(store.Revisions, newWriterChapterInferenceTool(tools.NewRepairDeAIBatchTool(store), store)),
		revisionFenceWrites(store.Revisions, newWriterChapterInferenceTool(tools.NewCheckConsistencyTool(store, continuityAuditorModel), store)),
		revisionFenceWrites(store.Revisions, newWriterChapterInferenceTool(tools.NewCheckAdaptationTool(store), store)),
		revisionFenceWrites(store.Revisions, newWriterChapterInferenceTool(tools.NewCheckDeAITool(store), store)),
		revisionFenceWrites(store.Revisions, newWriterChapterInferenceTool(tools.NewCheckSimulationTool(simulationCheck), store)),
		revisionFenceWrites(store.Revisions, newWriterChapterInferenceTool(
			tools.NewCommitChapterToolWithSimulation(store, completionGate, simulationCheck), store,
		)),
	}
	if err := validateAgentToolRegistry("writer", writerTools,
		"novel_context", "read_chapter", "plan_chapter", "draft_chapter", "edit_chapter", "repair_de_ai_batch",
		"check_consistency", "check_adaptation", "check_de_ai", "check_simulation", "commit_chapter"); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}

	// Coordinator 的 ContextManager 在 Agent 构造时一次性生成，按启动模型解析。
	// 运行中 /model 切换到更小窗口的模型时，建议用户显式配置 context_window 兜底。
	_, coordinatorModelName, _ := models.CurrentSelection("coordinator")
	coordinatorContextWindow, coordinatorSource := cfg.ResolveContextWindow(coordinatorModelName)
	// Writer 的 ContextManager 由工厂每次调用重建，窗口随模型 swap 动态跟随（见下方工厂）。
	_, writerModelName, _ := models.CurrentStageSelection(bootstrap.StageWriting)
	writerContextWindow, writerSource := cfg.ResolveContextWindow(writerModelName)
	bootstrap.LogContextWindowChoice("coordinator", coordinatorModelName, coordinatorContextWindow, coordinatorSource)
	bootstrap.LogContextWindowChoice("writer", writerModelName, writerContextWindow, writerSource)

	// modelLookup 写入 session 时给每条 assistant 消息附 _meta:{provider,model}，
	// 让 replay 不再依赖"当前 ModelSet"来反推历史 cost，运行中切换模型也能精确算。
	modelLookup := func(agentName string) (string, string) {
		role := agentToRole(agentName)
		if role == "architect" {
			provider, name, _ := models.CurrentStageSelection(bootstrap.StageSkeleton)
			return provider, name
		}
		if role == "writer" {
			provider, name, _ := models.CurrentStageSelection(bootstrap.StageWriting)
			return provider, name
		}
		if role == "editor" {
			provider, name, _ := models.CurrentStageSelection(bootstrap.StageReview)
			return provider, name
		}
		if role == "character" {
			if characterModel.Mode() == tools.CharacterRunReview {
				provider, name, _ := models.CurrentStageSelection(bootstrap.StageCharacterReview)
				return provider, name
			}
			provider, name, _ := models.CurrentStageSelection(bootstrap.StageCharacterAnalysis)
			return provider, name
		}
		provider, name, _ := models.CurrentSelection(role)
		return provider, name
	}
	baseOnMsg := store.Sessions.SubAgentLogger(modelLookup)
	onMsg := func(agentName, task string, msg agentcore.AgentMessage) {
		baseOnMsg(agentName, task, msg)
		if recordUsage != nil {
			recordUsage(agentName, msg)
		}
	}
	baseCoordinatorLog := store.Sessions.CoordinatorLogger(modelLookup)
	coordinatorOnMessage := func(msg agentcore.AgentMessage) {
		baseCoordinatorLog(msg)
		if recordUsage != nil {
			recordUsage("coordinator", msg)
		}
	}

	architectStopGuardFactory := func(_, task string) agentcore.StopGuard {
		return reminder.NewArchitectStopGuard(store, task)
	}
	architectThinking, _ := ResolveThinkingForModel(architectModel, roleThinking(cfg, "architect"))
	architectMicrocompact := architectToolResultMicrocompactConfig()
	architectShort := subagent.Config{
		Name:               "architect_short",
		Description:        "短篇规划师：为单卷、单冲突、高密度故事生成紧凑设定与扁平大纲",
		Model:              architectShortModel,
		SystemPrompt:       globalprompt.Apply(bundle.Prompts.ArchitectShort),
		Tools:              architectTools,
		MaxTurns:           15,
		MaxRetries:         subagentMaxRetries,
		ThinkingLevel:      architectThinking,
		ToolsAreIdempotent: true,
		OnMessage:          onMsg,
		StopAfterToolResult: func(toolName string, result json.RawMessage) bool {
			r := decodeSaveFoundationResult(toolName, result)
			return r.RetryInFreshContext || (r.Type == "outline" && r.FoundationReady)
		},
		StopGuardFactory: architectStopGuardFactory,
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			modelWindow, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			window, reserve := boundedAgentContextWindow(bootstrap.ModelName(model), modelWindow, promptcompile.AgentArchitect)
			return newLatestHostTurnContextManager(newContextManager(contextManagerConfig{
				Model: model, Store: store, ContextWindow: window, ReserveTokens: reserve,
				KeepRecentTokens: 14_000, Agent: "architect_short", CommitOnProject: true,
				ToolMicrocompact: architectMicrocompact, OnSummaryRetry: onSummaryRetry,
				ExtraStrategies: []corecontext.Strategy{
					newForceToolResultMicrocompact(*architectMicrocompact),
				},
			}))
		},
	}
	architectLong := subagent.Config{
		Name:                "architect_long",
		Description:         "长篇规划师：为连载型、可持续升级的故事生成分层设定与卷弧大纲",
		Model:               architectLongModel,
		SystemPrompt:        globalprompt.Apply(bundle.Prompts.ArchitectLong),
		Tools:               architectTools,
		MaxTurns:            20,
		MaxRetries:          subagentMaxRetries,
		ThinkingLevel:       architectThinking,
		ToolsAreIdempotent:  true,
		OnMessage:           onMsg,
		StopAfterToolResult: architectLongShouldStopAfterToolResult,
		StopGuardFactory:    architectStopGuardFactory,
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			modelWindow, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			window, reserve := boundedAgentContextWindow(bootstrap.ModelName(model), modelWindow, promptcompile.AgentArchitect)
			return newLatestHostTurnContextManager(newContextManager(contextManagerConfig{
				Model: model, Store: store, ContextWindow: window, ReserveTokens: reserve,
				KeepRecentTokens: 14_000, Agent: "architect_long", CommitOnProject: true,
				ToolMicrocompact: architectMicrocompact, OnSummaryRetry: onSummaryRetry,
				ExtraStrategies: []corecontext.Strategy{
					newForceToolResultMicrocompact(*architectMicrocompact),
				},
			}))
		},
	}

	character := subagent.Config{
		Name:               "character",
		Description:        "独立角色设计师：在分离的 analyze/review run 中生成或审核完整角色卡与计划关系",
		Model:              characterRuntimeModel,
		SystemPrompt:       globalprompt.Apply(bundle.Prompts.Character),
		Tools:              characterTools,
		MaxTurns:           16,
		MaxRetries:         subagentMaxRetries,
		ThinkingLevel:      resolvedRoleThinking(characterRuntimeModel, cfg, "character"),
		ToolsAreIdempotent: true,
		OnMessage:          onMsg,
		StopAfterToolResult: func(toolName string, _ json.RawMessage) bool {
			return toolName == "save_character_candidate" || toolName == "save_character_review" ||
				toolName == "save_cast_promotion_candidate" || toolName == "save_cast_promotion_review"
		},
		ContextManagerFactory: func(agentcore.ChatModel) agentcore.ContextManager {
			window, reserve := boundedCharacterContextWindow(cfg, models)
			return newContextManager(contextManagerConfig{
				Model:            characterRuntimeModel,
				Store:            store,
				ContextWindow:    window,
				ReserveTokens:    reserve,
				KeepRecentTokens: 16_000,
				Agent:            "character",
				OnSummaryRetry:   onSummaryRetry,
			})
		},
	}

	writerPrompt := bundle.Prompts.Writer
	writerPrompt += "\n\nRuntime tool contract: this Writer run has registered tools `novel_context`, `read_chapter`, `plan_chapter`, `draft_chapter`, `edit_chapter`, `repair_de_ai_batch`, `check_consistency`, `check_adaptation`, `check_de_ai`, `check_simulation`, and `commit_chapter`. Use these exact names. If a tool call is rejected as unavailable, do not invent a replacement name; report a tool-registry error so the Host can repair the runtime."
	if style, ok := bundle.Styles[cfg.Style]; ok {
		writerPrompt += "\n\n" + style
	}

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(store)

	writer := subagent.Config{
		Name:                "writer",
		Description:         "创作者：自主完成一章的构思、写作、自审和提交",
		Model:               writerModel,
		SystemPrompt:        globalprompt.Apply(writerPrompt),
		Tools:               writerTools,
		MaxTurns:            30,
		MaxRetries:          subagentMaxRetries,
		ThinkingLevel:       resolvedRoleThinking(writerModel, cfg, "writer"),
		ToolsAreIdempotent:  true,
		StopAfterTools:      []string{"commit_chapter"},
		StopAfterToolResult: writerShouldStopAfterToolResult,
		OnMessage:           onMsg,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return reminder.NewWriterStopGuard(store)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			// 每次 subagent(writer) 调用都会重建，从当前 runModel 读取最新模型名。
			// /model 切换 writer 后下一章自动用新窗口。
			modelWindow, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			window, reserve := boundedAgentContextWindow(bootstrap.ModelName(model), modelWindow, promptcompile.AgentWriter)
			microcompact := writerToolResultMicrocompactConfig()
			engine := newContextManager(contextManagerConfig{
				Model:            model,
				Store:            store,
				ContextWindow:    window,
				ReserveTokens:    reserve,
				KeepRecentTokens: 12_000,
				Agent:            "writer",
				CommitOnProject:  true,
				ToolMicrocompact: microcompact,
				ExtraStrategies: []corecontext.Strategy{
					ctxpack.NewStoreSummaryCompact(ctxpack.StoreSummaryCompactConfig{
						Store:            store,
						KeepRecentTokens: 12_000,
					}),
				},
				Summary: &corecontext.FullSummaryConfig{
					PostSummaryHooks:    []corecontext.PostSummaryHook{restore.Hook()},
					SystemPrompt:        globalprompt.Apply(ctxpack.WriterSummarySystemPrompt),
					SummaryPrompt:       ctxpack.WriterSummaryPrompt,
					UpdateSummaryPrompt: ctxpack.WriterUpdateSummaryPrompt,
					TurnPrefixPrompt:    ctxpack.WriterTurnPrefixPrompt,
				},
				OnSummaryRetry: onSummaryRetry,
			})
			return newWriterContextManager(engine, *microcompact)
		},
	}

	editor := subagent.Config{
		Name:               "editor",
		Description:        "审阅者：阅读原文，从结构和审美两个层面发现问题",
		Model:              editorModel,
		SystemPrompt:       globalprompt.Apply(bundle.Prompts.Editor),
		Tools:              editorTools,
		MaxTurns:           20,
		MaxRetries:         subagentMaxRetries,
		ThinkingLevel:      resolvedRoleThinking(editorModel, cfg, "editor"),
		ToolsAreIdempotent: true,
		OnMessage:          onMsg,
		// 仅摘要类终态产物命中即停；save_review 不再硬停——StopAfterTool 退出会绕过
		// StopGuard（agentcore loop.go），若 save_review 硬停，"被派生成弧摘要却先复核"
		// 的 editor 会在 save_review 处被砍断、够不到 save_arc_summary。评审/摘要任务的
		// 收尾改由任务感知的 NewEditorStopGuard 把关。
		StopAfterToolResult: func(toolName string, _ json.RawMessage) bool {
			return toolName == "save_original_planning_audit" || toolName == "save_arc_summary" || toolName == "save_volume_summary"
		},
		StopGuardFactory: func(_, task string) agentcore.StopGuard {
			return reminder.NewEditorStopGuard(store, task)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			modelWindow, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			window, reserve := boundedAgentContextWindow(bootstrap.ModelName(model), modelWindow, promptcompile.AgentEditor)
			microcompact := editorToolResultMicrocompactConfig()
			return newLatestHostTurnContextManager(newContextManager(contextManagerConfig{
				Model: model, Store: store, ContextWindow: window, ReserveTokens: reserve,
				KeepRecentTokens: 18_000, Agent: "editor", CommitOnProject: true,
				ToolMicrocompact: microcompact, OnSummaryRetry: onSummaryRetry,
				ExtraStrategies: []corecontext.Strategy{
					newForceToolResultMicrocompact(*microcompact),
				},
			}))
		},
	}

	subagentTool := subagent.New(architectShort, architectLong, character, writer, editor)

	coordinatorMicrocompact := coordinatorToolResultMicrocompactConfig()
	coordinatorContextWindow, coordinatorReserve := boundedAgentContextWindow(coordinatorModelName, coordinatorContextWindow, promptcompile.AgentCoordinator)
	coordinatorEngine := newContextManager(contextManagerConfig{
		Model:            coordinatorModel,
		Store:            store,
		ContextWindow:    coordinatorContextWindow,
		ReserveTokens:    coordinatorReserve,
		KeepRecentTokens: 8_000,
		Agent:            "coordinator",
		CommitOnProject:  true,
		ToolMicrocompact: coordinatorMicrocompact,
		ExtraStrategies: []corecontext.Strategy{
			coordinatorLatestHostTurnCheckpoint{},
		},
		OnSummaryRetry: onSummaryRetry,
	})

	coordinatorPrompt := bundle.Prompts.Coordinator + "\n\nCoordinator tool ownership contract: Coordinator itself only has `subagent`, `novel_context`, `save_user_rules`, and `reopen_book`. It does not directly own Writer tools such as `read_chapter`, `check_consistency`, `check_adaptation`, `check_simulation`, or `commit_chapter`; those are available inside the Writer subagent. Never tell the user that those Writer tools are missing merely because they are absent from Coordinator's own interface. After a Writer or Editor subagent returns, follow the Host route and dispatch the next required subagent; do not perform Writer checks yourself."
	agent := agentcore.NewAgent(
		agentcore.WithModel(coordinatorModel),
		agentcore.WithSystemPrompt(globalprompt.Apply(coordinatorPrompt)),
		agentcore.WithTools(
			newWorkflowSubagentTool(subagentTool),
			newCoordinatorContextTool(coordinatorContextTool),
			revisionFenceWrites(store.Revisions, tools.NewSaveUserRulesTool(userRulesSvc)),
			revisionFenceWrites(store.Revisions, tools.NewReopenBookTool(store)),
		),
		agentcore.WithMaxTurns(100_000),
		agentcore.WithOnMessage(coordinatorOnMessage),
		agentcore.WithToolsAreIdempotent(true),
		// subagent 是流程主通道；真实错误应显式返回给 Host，而不是在单次 run 内永久禁用工具。
		agentcore.WithMaxToolErrors(0),
		agentcore.WithMaxRetries(subagentMaxRetries),
		agentcore.WithContextManager(newLatestHostTurnContextManager(coordinatorEngine)),
		agentcore.WithStopGuard(reminder.NewStopGuard(
			store, nil, flow.NewPlanningReviewTaskPreparer(planningReviews),
		)),
		agentcore.WithMiddlewares(flowBoundaryMiddleware(onFlowBoundary)),
		// phase=complete 时硬拦截 subagent 派发，防止 Writer 死循环。
		agentcore.WithToolGate(combineToolGates(
			completePhaseGate(store),
			writerExpandedChapterGate(store),
		)),
	)
	// Coordinator 推理强度：无条件应用解析结果。未配置时为空（不发 thinking，用 provider
	// 默认），与各子代理（Config.ThinkingLevel 默认空）一致——避免覆盖 agentcore 默认
	// ThinkingLow 而对所有 provider 强制发 low（含会被强制开思考的 GLM/Ollama）。
	coordinatorThinking, _ := ResolveThinkingForModel(models.ForRole("coordinator"), roleThinking(cfg, "coordinator"))
	agent.SetThinkingLevel(coordinatorThinking)

	// 运行时联动各角色推理强度：coordinator 走 Agent，子代理走 subagentTool override。
	applyThinking := func(role string, level agentcore.ThinkingLevel) {
		switch role {
		case "coordinator":
			level, _ = ResolveThinkingForModel(models.ForRole("coordinator"), level)
			agent.SetThinkingLevel(level)
		case "architect":
			level, _ = ResolveThinkingForModel(models.ForRole("architect"), level)
			subagentTool.SetThinkingLevel("architect_short", level)
			subagentTool.SetThinkingLevel("architect_long", level)
		case "character":
			level, _ = ResolveThinkingForModel(models.ForRole("character"), level)
			subagentTool.SetThinkingLevel("character", level)
		case "writer", "editor":
			level, _ = ResolveThinkingForModel(models.ForRole(role), level)
			subagentTool.SetThinkingLevel(role, level)
		}
	}

	return agent, subagentTool, askUser, restore, coordinatorEngine, applyThinking, nil
}

func writerToolResultMicrocompactConfig() *corecontext.ToolResultMicrocompactConfig {
	// A validation boundary carries at most two recent results into the next
	// Writer phase (normally the draft plus its receipt, or two receipts).
	// Earlier context remains durable in the Store and can be re-read on demand;
	// retaining every source result makes the compiled request grow with chapter
	// length and report wording.
	return &corecontext.ToolResultMicrocompactConfig{
		KeepRecent:    2,
		IdleThreshold: 5 * time.Minute,
	}
}

// Architect batches intentionally keep the newest tool result only. A detail
// planning turn first loads a large authoritative context and then submits a
// sizeable save_foundation payload. If validation rejects that payload, forced
// context recovery must clear the older context result while retaining the
// proposed outline and the precise validation error needed for a lossless
// correction. The next fresh batch can always reload authoritative context.
func architectToolResultMicrocompactConfig() *corecontext.ToolResultMicrocompactConfig {
	return &corecontext.ToolResultMicrocompactConfig{
		KeepRecent:     1,
		ClearedMessage: "[Prior Architect tool result cleared; authoritative context remains in the current proposal and can be reloaded with novel_context.]",
	}
}

func coordinatorToolResultMicrocompactConfig() *corecontext.ToolResultMicrocompactConfig {
	// Host routes are reconstructed from durable project state before every
	// turn. Keep only the newest subagent/context receipt; older generated
	// outlines and audit reports are already persisted and otherwise make the
	// long-running Coordinator request grow once per arc.
	return &corecontext.ToolResultMicrocompactConfig{
		KeepRecent:     1,
		ClearedMessage: "[Prior Coordinator tool result cleared; authoritative outline, audits and progress remain in durable project state.]",
	}
}

func editorToolResultMicrocompactConfig() *corecontext.ToolResultMicrocompactConfig {
	// An evidence mismatch intentionally asks Editor to reload planning_audit.
	// Keep the newest evidence/error only so the superseded 20-30 KB evidence
	// pack cannot make the corrective provider call cross the byte boundary.
	return &corecontext.ToolResultMicrocompactConfig{
		KeepRecent:     1,
		ClearedMessage: "[Prior Editor evidence cleared; reload returned the authoritative current outline and the audit tool preserves the exact evidence mismatch.]",
	}
}

func boundedCharacterContextWindow(cfg bootstrap.Config, models *bootstrap.ModelSet) (int, int) {
	window := 0
	for _, stage := range []string{bootstrap.StageCharacterAnalysis, bootstrap.StageCharacterReview} {
		_, modelName, _ := models.CurrentStageSelection(stage)
		modelWindow, _ := cfg.ResolveContextWindow(modelName)
		if roleWindow := modelprofile.Resolve(modelName).ContextWindow(modelprofile.RoleCharacter); roleWindow > 0 {
			modelWindow = min(modelWindow, roleWindow)
		}
		if modelWindow > 0 && (window == 0 || modelWindow < window) {
			window = modelWindow
		}
	}
	return window, bootstrap.CompactReserveTokens(window)
}

func validateAgentToolRegistry(role string, actual []agentcore.Tool, required ...string) error {
	available := make(map[string]struct{}, len(actual))
	for _, tool := range actual {
		if tool == nil || strings.TrimSpace(tool.Name()) == "" {
			return fmt.Errorf("%s tool registry contains an unnamed tool: %w", role, errs.ErrToolUnavailable)
		}
		name := strings.TrimSpace(tool.Name())
		if _, exists := available[name]; exists {
			return fmt.Errorf("%s tool registry contains duplicate tool %q: %w", role, name, errs.ErrToolUnavailable)
		}
		available[name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := available[name]; !ok {
			return fmt.Errorf("%s tool registry is missing required tool %q: %w", role, name, errs.ErrToolUnavailable)
		}
	}
	return nil
}

func flowBoundaryMiddleware(onBoundary FlowBoundaryHook) agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
		out, err := next(ctx, call.Args)
		// A subagent can persist a draft/checkpoint and then fail on a later
		// turn (max turns, context overflow, provider stream error). Recompute
		// the Host route even on that terminal error so the Coordinator receives
		// the durable recovery instruction before it can reuse the stale task.
		// Other boundary tools still require success.
		boundaryReached := err == nil && isFlowBoundaryTool(call.Name)
		if call.Name == "subagent" {
			boundaryReached = true
		}
		if onBoundary != nil && boundaryReached {
			onBoundary(call.Name)
		}
		return out, err
	}
}

func isFlowBoundaryTool(name string) bool {
	return name == "subagent" || name == "reopen_book"
}

// completePhaseGate 返回一个 ToolGate：phase=complete 时拒绝所有 subagent 派发。
// 防止 Coordinator LLM 在书完成后仍调用 Writer/Architect 导致死循环。
func completePhaseGate(st *store.Store) agentcore.ToolGate {
	return func(_ context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
		if req.Call.Name != "subagent" {
			return nil, nil
		}
		// fail-open：Load 出错或 progress 为空时一律放行，不因瞬时读错误卡死正常派发。
		// 唯一代价是 complete 期恰逢读失败时死锁可能复现（概率极低，可接受）。
		progress, _ := st.Progress.Load()
		if progress != nil && progress.Phase == domain.PhaseComplete {
			return &agentcore.GateDecision{
				Allowed: false,
				Reason:  "全书已完成（phase=complete），不能直接派子代理。若用户要返工已写章节，请先调用 reopen_book(chapters=[...]) 把书重新打开进入返工态（之后会自动派 writer 重写）；若用户要新增剧情，告知需新建项目。",
			}, nil
		}
		return nil, nil
	}
}

func combineToolGates(gates ...agentcore.ToolGate) agentcore.ToolGate {
	return func(ctx context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
		for _, gate := range gates {
			if gate == nil {
				continue
			}
			decision, err := gate(ctx, req)
			if err != nil {
				return nil, err
			}
			if decision != nil && !decision.Allowed {
				return decision, nil
			}
		}
		return nil, nil
	}
}

func writerExpandedChapterGate(st *store.Store) agentcore.ToolGate {
	return func(_ context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
		if req.Call.Name != "subagent" {
			return nil, nil
		}
		var args struct {
			Agent string `json:"agent"`
			Task  string `json:"task"`
		}
		if err := json.Unmarshal(req.Call.Args, &args); err != nil || args.Agent != "writer" {
			return nil, nil
		}
		chapter := chapterFromTask(args.Task)
		if chapter <= 0 {
			chapter = writerFallbackChapter(st)
		}
		if chapter <= 0 {
			return nil, nil
		}
		if err := tools.EnsureAdaptationChapterPlanned(st, chapter); err != nil {
			return &agentcore.GateDecision{
				Allowed: false,
				Reason:  err.Error() + "。请重新生成并确认改编规模后再派 writer。",
			}, nil
		}
		if err := tools.EnsureChapterExpanded(st, chapter); err != nil {
			return &agentcore.GateDecision{
				Allowed: false,
				Reason:  err.Error() + "。请改派 architect_long，调用 save_foundation(type=expand_arc) 展开下一弧，或 type=append_volume 追加并展开下一卷后再派 writer。",
			}, nil
		}
		return nil, nil
	}
}

func writerFallbackChapter(st *store.Store) int {
	if st == nil {
		return 0
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return 0
	}
	if len(progress.PendingRewrites) > 0 {
		return progress.PendingRewrites[0]
	}
	return progress.NextChapter()
}

var chapterTaskRe = regexp.MustCompile(`第\s*(\d+)\s*章`)

func chapterFromTask(task string) int {
	m := chapterTaskRe.FindStringSubmatch(task)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

type saveFoundationResult struct {
	Type                string `json:"type"`
	FoundationReady     bool   `json:"foundation_ready"`
	PlanningReview      string `json:"planning_review"`
	ContinuePlanning    bool   `json:"continue_planning"`
	AuditRequired       bool   `json:"audit_required"`
	VolumeBatchSaved    bool   `json:"volume_batch_saved"`
	RetryInFreshContext bool   `json:"retry_in_fresh_context"`
}

func decodeSaveFoundationResult(toolName string, result json.RawMessage) saveFoundationResult {
	if toolName != "save_foundation" {
		return saveFoundationResult{}
	}
	var r saveFoundationResult
	_ = json.Unmarshal(result, &r)
	return r
}

func architectLongShouldStopAfterToolResult(toolName string, result json.RawMessage) bool {
	r := decodeSaveFoundationResult(toolName, result)
	if r.RetryInFreshContext {
		return true
	}
	if r.PlanningReview == domain.PlanningReviewStatusPending {
		return true
	}
	if r.VolumeBatchSaved {
		return true
	}
	switch r.Type {
	case "expand_arc":
		return r.AuditRequired || !r.ContinuePlanning
	case "repair_arc", "complete_book":
		return true
	default:
		return false
	}
}

func writerShouldStopAfterToolResult(toolName string, result json.RawMessage) bool {
	var payload struct {
		DeferredToHost bool  `json:"deferred_to_host"`
		BudgetSegment  *int  `json:"budget_segment"`
		Passed         *bool `json:"passed"`
	}
	if json.Unmarshal(result, &payload) != nil {
		return false
	}
	if payload.DeferredToHost {
		return true
	}
	// A failed prose audit is a durable phase boundary. Ending this subagent
	// turn lets the Host redispatch a compact repair-only task from the saved
	// audit instead of carrying the full contract, draft, consistency receipt,
	// and audit report through another provider request.
	if toolName == "check_de_ai" && payload.Passed != nil && !*payload.Passed {
		return true
	}
	if toolName == "repair_de_ai_batch" {
		return true
	}
	return toolName == "edit_chapter" && payload.BudgetSegment != nil
}

type writerDraftChapterTool struct {
	inner *tools.DraftChapterTool
	store *store.Store
}

type writerContextTool struct {
	inner *tools.ContextTool
	store *store.Store
}

type coordinatorContextTool struct {
	inner *tools.ContextTool
}

// workflowSubagentTool keeps the Coordinator on the Host-owned synchronous
// route. The generic agentcore tool also exposes background/team/parallel
// modes, but those modes bypass the novel workflow checkpoint contract and a
// model can occasionally select them despite an exact Host instruction.
type workflowSubagentTool struct {
	inner agentcore.Tool
}

func newWorkflowSubagentTool(inner agentcore.Tool) agentcore.Tool {
	return &workflowSubagentTool{inner: inner}
}

func (t *workflowSubagentTool) Name() string { return t.inner.Name() }
func (t *workflowSubagentTool) Description() string {
	return "Run exactly one Host-directed novel workflow subagent synchronously. Provide only agent and task."
}
func (t *workflowSubagentTool) Schema() map[string]any {
	schema := t.inner.Schema()
	properties, _ := schema["properties"].(map[string]any)
	for _, key := range []string{"tasks", "chain", "background", "description", "team_name", "name", "color"} {
		delete(properties, key)
	}
	schema["required"] = []string{"agent", "task"}
	return schema
}
func (t *workflowSubagentTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *workflowSubagentTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *workflowSubagentTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Agent string `json:"agent"`
		Task  string `json:"task"`
		Model string `json:"model,omitempty"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	result, err := t.inner.Execute(ctx, canonical)
	if err != nil {
		return result, err
	}
	// Workflow subagents persist their authoritative result before returning.
	// The Host derives the next route from that durable checkpoint, so replaying
	// the full model output into Coordinator history only makes long planning
	// runs grow once per volume.
	return json.Marshal(struct {
		Completed  bool   `json:"completed"`
		Agent      string `json:"agent"`
		Checkpoint string `json:"checkpoint"`
	}{
		Completed:  true,
		Agent:      request.Agent,
		Checkpoint: "persisted; Host will route from the durable project state",
	})
}

func newCoordinatorContextTool(inner *tools.ContextTool) agentcore.Tool {
	return &coordinatorContextTool{inner: inner}
}

func (t *coordinatorContextTool) Name() string        { return t.inner.Name() }
func (t *coordinatorContextTool) Description() string { return t.inner.Description() }
func (t *coordinatorContextTool) Label() string       { return t.inner.Label() }
func (t *coordinatorContextTool) Schema() map[string]any {
	return t.inner.Schema()
}
func (t *coordinatorContextTool) ReadOnly(args json.RawMessage) bool {
	return t.inner.ReadOnly(args)
}
func (t *coordinatorContextTool) ConcurrencySafe(args json.RawMessage) bool {
	return t.inner.ConcurrencySafe(args)
}
func (t *coordinatorContextTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(args, &raw) != nil || hasPositiveChapter(raw["chapter"]) || hasExplicitContextScope(raw["scope"]) {
		return t.inner.Execute(ctx, args)
	}
	return t.inner.Execute(ctx, json.RawMessage(`{"scope":"status"}`))
}

func newWriterContextTool(inner *tools.ContextTool, st *store.Store) agentcore.Tool {
	return &writerContextTool{inner: inner, store: st}
}

const writerPriorChapterMaxRunes = 600

type writerReadChapterTool struct {
	inner agentcore.Tool
	store *store.Store
}

func newWriterReadChapterTool(inner agentcore.Tool, st *store.Store) agentcore.Tool {
	return &writerReadChapterTool{inner: inner, store: st}
}

func (t *writerReadChapterTool) Name() string { return t.inner.Name() }
func (t *writerReadChapterTool) Description() string {
	return t.inner.Description() + " Writer reads the active draft in full. Before a draft exists, only the immediately preceding chapter may be loaded as a bounded continuity tail; older history is already represented by novel_context. After a draft exists, prior-chapter reads return a lightweight continuity redirect because validation must use the current draft."
}
func (t *writerReadChapterTool) Label() string {
	if labeled, ok := t.inner.(agentcore.ToolLabeler); ok {
		return labeled.Label()
	}
	return t.inner.Name()
}
func (t *writerReadChapterTool) Schema() map[string]any {
	return toolSchemaWithoutRequired(toolSchemaWithoutRequired(t.inner.Schema(), "chapter"), "source")
}
func (t *writerReadChapterTool) StrictSchema() bool { return false }
func (t *writerReadChapterTool) ReadOnly(args json.RawMessage) bool {
	if readOnly, ok := t.inner.(agentcore.ReadOnlyTool); ok {
		return readOnly.ReadOnly(args)
	}
	return true
}
func (t *writerReadChapterTool) ConcurrencySafe(args json.RawMessage) bool {
	if safe, ok := t.inner.(agentcore.ConcurrencySafeTool); ok {
		return safe.ConcurrencySafe(args)
	}
	return true
}
func (t *writerReadChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return t.inner.Execute(ctx, args)
	}
	activeChapter := inferWriterDraftChapter(t.store)
	requestedChapter := positiveChapter(raw["chapter"])
	if requestedChapter <= 0 && activeChapter > 0 {
		requestedChapter = activeChapter
		encodedChapter, err := json.Marshal(requestedChapter)
		if err != nil {
			return nil, err
		}
		raw["chapter"] = encodedChapter
	}
	var source string
	_ = json.Unmarshal(raw["source"], &source)
	if strings.TrimSpace(source) == "" {
		source = "final"
		if requestedChapter > 0 && requestedChapter == activeChapter {
			if draft, err := t.store.Drafts.LoadDraft(requestedChapter); err == nil && draft != "" {
				source = "draft"
			}
		}
		encodedSource, err := json.Marshal(source)
		if err != nil {
			return nil, err
		}
		raw["source"] = encodedSource
	}
	if redirect, ok := t.redirectPriorChapterRead(activeChapter, requestedChapter); ok {
		return redirect, nil
	}
	if requestedChapter > 0 && activeChapter > 0 && requestedChapter != activeChapter {
		var requestedMax int
		_ = json.Unmarshal(raw["max_runes"], &requestedMax)
		if requestedMax <= 0 || requestedMax > writerPriorChapterMaxRunes {
			encodedMax, err := json.Marshal(writerPriorChapterMaxRunes)
			if err != nil {
				return nil, err
			}
			raw["max_runes"] = encodedMax
		}
	}
	nextArgs, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("augment Writer read_chapter args: %w", err)
	}
	result, err := t.inner.Execute(ctx, nextArgs)
	if err != nil {
		return nil, err
	}
	if requestedChapter > 0 && requestedChapter == activeChapter {
		result, err = attachPendingConsistencyRepair(t.store, requestedChapter, result)
		if err != nil {
			return nil, err
		}
		return attachPendingDeAIRepair(t.store, requestedChapter, result)
	}
	if requestedChapter <= 0 || activeChapter <= 0 || requestedChapter == activeChapter {
		return result, nil
	}
	var payload map[string]any
	if json.Unmarshal(result, &payload) != nil {
		return result, nil
	}
	payload["context_profile"] = "bounded_prior_continuity_tail"
	payload["continuity_evidence_complete"] = true
	payload["do_not_retry_for_more"] = true
	payload["hint"] = "This bounded tail is the complete prior-chapter evidence required for continuity; novel_context already includes previous_tail and recent summaries. Do not reread this or another prior chapter and do not increase max_runes. Proceed directly to plan_chapter or draft_chapter."
	return json.Marshal(payload)
}

func (t *writerReadChapterTool) redirectPriorChapterRead(activeChapter, requestedChapter int) (json.RawMessage, bool) {
	if t.store == nil || activeChapter <= 0 || requestedChapter <= 0 || requestedChapter >= activeChapter {
		return nil, false
	}
	draft, _ := t.store.Drafts.LoadDraft(activeChapter)
	draftExists := strings.TrimSpace(draft) != ""
	if !draftExists && requestedChapter == activeChapter-1 {
		return nil, false
	}
	reason := "older_history_is_already_in_novel_context"
	nextAction := "Use novel_context recent summaries and previous_tail; if exact continuity evidence is still required, read only the immediately preceding chapter once."
	if draftExists {
		reason = "active_draft_is_already_available"
		nextAction = "Read and validate the current draft; do not load any prior chapter."
	}
	payload := map[string]any{
		"chapter":                      requestedChapter,
		"active_chapter":               activeChapter,
		"context_profile":              "prior_history_redirect",
		"content_loaded":               false,
		"continuity_evidence_complete": true,
		"do_not_retry_for_more":        true,
		"reason":                       reason,
		"next_action":                  nextAction,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func attachPendingConsistencyRepair(st *store.Store, chapter int, result json.RawMessage) (json.RawMessage, error) {
	if st == nil || st.Consistency == nil || chapter <= 0 {
		return result, nil
	}
	audit, err := st.Consistency.LoadAudit(chapter)
	if err != nil || audit == nil || audit.Passed {
		return result, nil
	}
	content, _, err := st.Drafts.LoadChapterContent(chapter)
	if err != nil || content == "" || audit.DraftSHA256 != store.TextSHA256(content) {
		return result, nil
	}
	findings := make([]domain.ConsistencyIssue, 0, len(audit.Findings))
	for _, finding := range audit.Findings {
		if finding.Severity == "critical" || finding.Severity == "error" {
			findings = append(findings, finding)
		}
	}
	if len(findings) == 0 {
		return result, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		return result, nil
	}
	payload["pending_consistency_repair"] = map[string]any{
		"draft_sha256":                          audit.DraftSHA256,
		"do_not_repeat_consistency_before_edit": true,
		"findings":                              findings,
		"next_action":                           "Apply the exact actionable repairs with edit_chapter now. After the edit, rerun check_consistency on the changed draft.",
	}
	return json.Marshal(payload)
}

func attachPendingDeAIRepair(st *store.Store, chapter int, result json.RawMessage) (json.RawMessage, error) {
	if st == nil || st.DeAI == nil || chapter <= 0 {
		return result, nil
	}
	audit, err := st.DeAI.LoadAudit(chapter)
	if err != nil || audit == nil || audit.Passed {
		return result, nil
	}
	content, _, err := st.Drafts.LoadChapterContent(chapter)
	if err != nil || content == "" || audit.DraftSHA256 != store.TextSHA256(content) {
		return result, nil
	}
	checkpoint := st.Checkpoints.LatestByStep(domain.ChapterScope(chapter), "consistency_check")
	consistencyCurrent := checkpoint != nil && checkpoint.Digest == "sha256:"+audit.DraftSHA256
	plan := audit.Report.RepairPlan()
	if len(plan.Batches) == 0 {
		return result, nil
	}
	batch := plan.Batches[0]
	findings := make([]any, 0, len(batch.FindingCodes))
	for _, finding := range audit.Report.Findings {
		if finding.Severity != "repair" || !slices.Contains(batch.FindingCodes, finding.Code) {
			continue
		}
		findings = append(findings, map[string]any{
			"code":        finding.Code,
			"actual":      finding.Actual,
			"limit":       finding.Limit,
			"examples":    finding.Examples,
			"repair_hint": finding.RepairHint,
		})
	}
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		return result, nil
	}
	payload["pending_de_ai_repair"] = map[string]any{
		"draft_sha256":                          audit.DraftSHA256,
		"consistency_current":                   consistencyCurrent,
		"do_not_repeat_consistency_before_edit": true,
		"batch":                                 batch,
		"findings":                              findings,
		"next_action":                           "Use the exact examples above to call repair_de_ai_batch now. After the edit, rerun only check_de_ai. Run check_consistency once after de-AI passes.",
	}
	return json.Marshal(payload)
}

type writerChapterInferenceTool struct {
	inner agentcore.Tool
	store *store.Store
}

func newWriterChapterInferenceTool(inner agentcore.Tool, st *store.Store) agentcore.Tool {
	return &writerChapterInferenceTool{inner: inner, store: st}
}

func (t *writerChapterInferenceTool) Name() string        { return t.inner.Name() }
func (t *writerChapterInferenceTool) Description() string { return t.inner.Description() }
func (t *writerChapterInferenceTool) Label() string {
	if labeled, ok := t.inner.(agentcore.ToolLabeler); ok {
		return labeled.Label()
	}
	return t.inner.Name()
}
func (t *writerChapterInferenceTool) Schema() map[string]any {
	return toolSchemaWithoutRequired(t.inner.Schema(), "chapter")
}
func (t *writerChapterInferenceTool) StrictSchema() bool { return false }
func (t *writerChapterInferenceTool) ReadOnly(args json.RawMessage) bool {
	if readOnly, ok := t.inner.(agentcore.ReadOnlyTool); ok {
		return readOnly.ReadOnly(args)
	}
	return false
}
func (t *writerChapterInferenceTool) ConcurrencySafe(args json.RawMessage) bool {
	if safe, ok := t.inner.(agentcore.ConcurrencySafeTool); ok {
		return safe.ConcurrencySafe(args)
	}
	return false
}
func (t *writerChapterInferenceTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return t.inner.Execute(ctx, args)
	}
	if hasPositiveChapter(raw["chapter"]) {
		chapter := positiveChapter(raw["chapter"])
		if deferred, ok := t.deferPlanForExistingDraft(chapter); ok {
			return deferred, nil
		}
		if err := t.validateCommitIdentity(chapter); err != nil {
			return nil, err
		}
		return t.inner.Execute(ctx, args)
	}
	chapter := inferWriterDraftChapter(t.store)
	if chapter <= 0 {
		return nil, fmt.Errorf("chapter is required and cannot be inferred from current writing state")
	}
	if deferred, ok := t.deferPlanForExistingDraft(chapter); ok {
		return deferred, nil
	}
	encodedChapter, err := json.Marshal(chapter)
	if err != nil {
		return nil, err
	}
	raw["chapter"] = encodedChapter
	if err := t.validateCommitIdentity(chapter); err != nil {
		return nil, err
	}
	nextArgs, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("augment %s args: %w", t.inner.Name(), err)
	}
	return t.inner.Execute(ctx, nextArgs)
}

func (t *writerChapterInferenceTool) deferPlanForExistingDraft(chapter int) (json.RawMessage, bool) {
	if t == nil || t.inner == nil || t.inner.Name() != "plan_chapter" || t.store == nil || chapter <= 0 {
		return nil, false
	}
	draft, err := t.store.Drafts.LoadDraft(chapter)
	if err != nil || strings.TrimSpace(draft) == "" {
		return nil, false
	}
	payload, err := json.Marshal(map[string]any{
		"deferred_to_host": true,
		"chapter":          chapter,
		"draft_exists":     true,
		"plan_skipped":     true,
		"next_step":        "The current draft is authoritative and must not be replaced by a new plan. Host will resume its validation checkpoint.",
	})
	if err != nil {
		return nil, false
	}
	return payload, true
}

func (t *writerChapterInferenceTool) validateCommitIdentity(chapter int) error {
	if t.inner.Name() != "commit_chapter" {
		return nil
	}
	content, err := t.store.Drafts.LoadDraft(chapter)
	if err != nil || strings.TrimSpace(content) == "" {
		return nil
	}
	return validateWriterDraftIdentityContent(t.store, chapter, content)
}

func (t *writerContextTool) Name() string { return t.inner.Name() }
func (t *writerContextTool) Description() string {
	return t.inner.Description() + " Writer may load the full work package only for the active chapter. Planning/status scopes are unavailable to Writer. The returned outline and canonical character workset are authoritative. Use read_chapter(source=draft) once whenever validation requires exact current-draft quotes; prior-chapter prose remains bounded continuity evidence."
}
func (t *writerContextTool) Label() string { return t.inner.Label() }
func (t *writerContextTool) Schema() map[string]any {
	return t.inner.Schema()
}
func (t *writerContextTool) ReadOnly(args json.RawMessage) bool {
	return t.inner.ReadOnly(args)
}
func (t *writerContextTool) ConcurrencySafe(args json.RawMessage) bool {
	return t.inner.ConcurrencySafe(args)
}

func (t *writerContextTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return t.inner.Execute(ctx, args)
	}
	requestedScope := explicitContextScope(raw["scope"])
	if requestedScope != "" && requestedScope != "chapter" {
		return json.Marshal(map[string]any{
			"context_profile":      "writer_scope_redirect",
			"full_context_loaded":  false,
			"authoritative_source": "active chapter work package",
			"do_not_retry_scope":   true,
			"next_step":            "Call novel_context(scope=chapter) or omit scope. Writer must not load planning, planning_detail, planning_review, planning_audit, or status contexts.",
		})
	}
	chapter := inferWriterDraftChapter(t.store)
	requestedChapter := positiveChapter(raw["chapter"])
	if requestedChapter > 0 {
		if chapter > 0 && requestedChapter != chapter {
			return json.Marshal(map[string]any{
				"context_profile":            "cross_chapter_redirect",
				"requested_chapter":          requestedChapter,
				"active_chapter":             chapter,
				"full_context_loaded":        false,
				"prior_chapter_source":       "read_chapter",
				"do_not_retry_novel_context": true,
				"next_step":                  fmt.Sprintf("Call novel_context(chapter=%d) for the active work package only; use read_chapter(chapter=%d) if prior prose is needed for continuity.", chapter, requestedChapter),
			})
		}
		return t.executeActiveChapterContext(ctx, args)
	}
	if chapter <= 0 {
		return t.inner.Execute(ctx, args)
	}
	encodedChapter, err := json.Marshal(chapter)
	if err != nil {
		return nil, err
	}
	raw["chapter"] = encodedChapter
	nextArgs, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("augment novel_context args: %w", err)
	}
	return t.executeActiveChapterContext(ctx, nextArgs)
}

func (t *writerContextTool) executeActiveChapterContext(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	result, err := t.inner.Execute(ctx, args)
	if err != nil {
		return result, err
	}
	var payload map[string]any
	if json.Unmarshal(result, &payload) != nil {
		return result, nil
	}
	payload["writer_execution_contract"] = map[string]any{
		"authoritative":                true,
		"story_facts_source":           "working_memory.current_chapter_outline and episodic_memory.character_workset",
		"never_invent_replacement":     true,
		"planning_scope_forbidden":     true,
		"plan_only_if_outline_missing": true,
		"viewpoint_source":             "current chapter outline, never the imitation corpus",
		"next_step":                    "Draft this exact chapter from the saved outline and canonical characters; do not replace its genre, setting, plot, or cast.",
	}
	compactWriterCharacterWorkset(payload)
	chapter := inferWriterDraftChapter(t.store)
	if draft, loadErr := t.store.Drafts.LoadDraft(chapter); loadErr == nil && strings.TrimSpace(draft) != "" {
		compactWriterRecoveryContext(payload)
	}
	return json.Marshal(payload)
}

// compactWriterCharacterWorkset keeps every authoritative chapter character
// and every field that can change prose decisions, but removes duplicated
// biography prose that is already represented by the chapter-specific
// contracts, relationship state, and recent state changes. This is a
// structural projection rather than a summary: IDs, goals, voice, constraints,
// and knowledge boundaries remain verbatim.
func compactWriterCharacterWorkset(payload map[string]any) {
	episodic, ok := payload["episodic_memory"].(map[string]any)
	if !ok {
		return
	}
	workset, ok := episodic["character_workset"].(map[string]any)
	if !ok {
		return
	}
	cards := make([]any, 0, 8)
	for _, section := range []string{"full", "compressed"} {
		items, _ := workset[section].([]any)
		for _, item := range items {
			card, ok := item.(map[string]any)
			if !ok {
				continue
			}
			projected := make(map[string]any, 12)
			for _, key := range []string{
				"id", "name", "aliases", "role", "tier", "faction",
				"goal", "motivation", "conflict", "voice", "constraints",
				"knowledge_boundary",
			} {
				if value, exists := card[key]; exists {
					projected[key] = value
				}
			}
			cards = append(cards, projected)
		}
	}
	if len(cards) == 0 {
		return
	}
	requestedIDs := any(nil)
	if diagnostics, ok := workset["diagnostics"].(map[string]any); ok {
		requestedIDs = diagnostics["requested_ids"]
	}
	episodic["character_workset"] = map[string]any{
		"full": cards,
		"diagnostics": map[string]any{
			"requested_ids":  requestedIDs,
			"selection_mode": "stable_id",
			"projection":     "writer_quality_fields",
			"preserved_fields": []string{
				"id", "name", "aliases", "role", "tier", "faction",
				"goal", "motivation", "conflict", "voice", "constraints",
				"knowledge_boundary",
			},
		},
	}
}

// compactWriterRecoveryContext removes drafting-only duplication once a full
// draft exists. Validation still receives the exact chapter outline,
// canonical character constraints, relationship/state deltas, user rules, and
// style anchors. The draft itself arrives through read_chapter, so repeating
// prior tails, future promises, simulation scaffolding, and source references
// only consumes the provider's byte boundary without adding validation facts.
func compactWriterRecoveryContext(payload map[string]any) {
	if working, ok := payload["working_memory"].(map[string]any); ok {
		keepMapKeys(working,
			"current_chapter_outline",
			"user_rules",
			"word_budget",
			"rewrite_brief",
		)
	}
	if episodic, ok := payload["episodic_memory"].(map[string]any); ok {
		keepMapKeys(episodic,
			"character_workset",
			"recent_state_changes",
			"relationship_state",
			"foreshadow_ledger",
			"planning_tier",
		)
	}
	if references, ok := payload["reference_pack"].(map[string]any); ok {
		keepMapKeys(references, "style_anchors")
	}
	payload["writer_context_projection"] = map[string]any{
		"mode":                 "existing_draft_validation",
		"draft_source":         "read_chapter",
		"story_contract_kept":  true,
		"character_rules_kept": true,
		"next_step":            "Read the current draft once, then validate or locally repair it. Do not replan or redraft merely because drafting-only context was omitted.",
	}
}

func keepMapKeys(values map[string]any, keys ...string) {
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	for key := range values {
		if _, keep := allowed[key]; !keep {
			delete(values, key)
		}
	}
}

func hasExplicitContextScope(raw json.RawMessage) bool {
	return explicitContextScope(raw) != ""
}

func explicitContextScope(raw json.RawMessage) string {
	var scope string
	if len(raw) == 0 || json.Unmarshal(raw, &scope) != nil {
		return ""
	}
	return strings.TrimSpace(scope)
}

func newWriterDraftChapterTool(st *store.Store) agentcore.Tool {
	return &writerDraftChapterTool{
		inner: tools.NewDraftChapterTool(st),
		store: st,
	}
}

func (t *writerDraftChapterTool) Name() string        { return t.inner.Name() }
func (t *writerDraftChapterTool) Description() string { return t.inner.Description() }
func (t *writerDraftChapterTool) Label() string       { return t.inner.Label() }

func (t *writerDraftChapterTool) ReadOnly(args json.RawMessage) bool {
	return t.inner.ReadOnly(args)
}

func (t *writerDraftChapterTool) ConcurrencySafe(args json.RawMessage) bool {
	return t.inner.ConcurrencySafe(args)
}

func (t *writerDraftChapterTool) StrictSchema() bool { return false }

func (t *writerDraftChapterTool) Schema() map[string]any {
	schema := toolSchemaWithoutRequired(t.inner.Schema(), "chapter")
	if properties, ok := schema["properties"].(map[string]any); ok {
		properties["replace_out_of_budget"] = map[string]any{
			"type":        "boolean",
			"description": "Host-only recovery flag: replace a severely out-of-budget active draft only when the complete new candidate is already inside the hard budget.",
		}
	}
	return schema
}

func (t *writerDraftChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return t.inner.Execute(ctx, args)
	}
	chapter := positiveChapter(raw["chapter"])
	if chapter <= 0 {
		chapter = inferWriterDraftChapter(t.store)
	}
	if chapter <= 0 {
		return nil, fmt.Errorf("chapter is required and cannot be inferred from current writing state")
	}
	if writerShouldAuthorizeOutOfBudgetReplacement(t.store, chapter, raw["mode"]) {
		// The Host selects this recovery path from durable word-budget state.
		// Do not depend on the model faithfully echoing a control-only flag.
		raw["replace_out_of_budget"] = json.RawMessage("true")
	}
	if writerDraftWouldOverwriteActiveDraft(t.store, chapter, raw["mode"], raw["replace_out_of_budget"]) {
		existing, _ := t.store.Drafts.LoadDraft(chapter)
		return json.Marshal(map[string]any{
			"chapter":          chapter,
			"written":          false,
			"deferred_to_host": true,
			"draft_exists":     true,
			"draft_skipped":    true,
			"word_count":       utf8.RuneCountInString(existing),
			"next_step":        "A durable draft already exists for this active chapter. Do not overwrite or re-plan it after a validation error. End this turn now; Host will resume from the saved draft and continue read_chapter/check_consistency/check_de_ai/commit_chapter. A full rewrite is allowed only through an explicit rewrite or polishing queue.",
		})
	}
	if err := validateWriterDraftIdentity(t.store, chapter, raw["content"]); err != nil {
		return nil, err
	}
	if !hasPositiveChapter(raw["chapter"]) {
		encodedChapter, err := json.Marshal(chapter)
		if err != nil {
			return nil, err
		}
		raw["chapter"] = encodedChapter
	}
	nextArgs, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("augment draft_chapter args: %w", err)
	}
	return t.inner.Execute(ctx, nextArgs)
}

func writerShouldAuthorizeOutOfBudgetReplacement(st *store.Store, chapter int, rawMode json.RawMessage) bool {
	if st == nil || chapter <= 0 {
		return false
	}
	var mode string
	if json.Unmarshal(rawMode, &mode) != nil || mode != "write" {
		return false
	}
	existing, err := st.Drafts.LoadDraft(chapter)
	if err != nil || strings.TrimSpace(existing) == "" {
		return false
	}
	progress, progressErr := st.Progress.Load()
	_, policy, policyOK, policyErr := st.ChapterWordBudgetPolicy(progress, chapter)
	if progressErr != nil || progress == nil || policyErr != nil || !policyOK || policy.HardMaxWords <= 0 {
		return false
	}
	existingCount := utf8.RuneCountInString(existing)
	return existingCount > policy.HardMaxWords &&
		(existingCount-policy.HardMaxWords)*10 > policy.HardMaxWords
}

func writerDraftWouldOverwriteActiveDraft(st *store.Store, chapter int, rawMode, rawReplaceOutOfBudget json.RawMessage) bool {
	if st == nil || chapter <= 0 {
		return false
	}
	var mode string
	if json.Unmarshal(rawMode, &mode) != nil || mode != "write" {
		return false
	}
	existing, err := st.Drafts.LoadDraft(chapter)
	if err != nil || strings.TrimSpace(existing) == "" {
		return false
	}
	var replaceOutOfBudget bool
	if json.Unmarshal(rawReplaceOutOfBudget, &replaceOutOfBudget) == nil && replaceOutOfBudget {
		progress, progressErr := st.Progress.Load()
		_, policy, policyOK, policyErr := st.ChapterWordBudgetPolicy(progress, chapter)
		existingCount := utf8.RuneCountInString(existing)
		if progressErr == nil && progress != nil && policyErr == nil && policyOK &&
			!policy.WithinHardRange(existingCount) {
			return false
		}
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return true
	}
	authorizedRewrite := slices.Contains(progress.PendingRewrites, chapter) &&
		(progress.Flow == domain.FlowRewriting || progress.Flow == domain.FlowPolishing)
	return !authorizedRewrite
}

func validateWriterDraftIdentity(st *store.Store, chapter int, rawContent json.RawMessage) error {
	var content string
	if json.Unmarshal(rawContent, &content) != nil || strings.TrimSpace(content) == "" {
		return nil
	}
	return validateWriterDraftIdentityContent(st, chapter, content)
}

func validateWriterDraftIdentityContent(st *store.Store, chapter int, content string) error {
	outline, err := st.Outline.LoadOutline()
	if err != nil {
		return nil
	}
	var requiredIDs []string
	for _, entry := range outline {
		if entry.Chapter != chapter {
			continue
		}
		requiredIDs = append(requiredIDs, entry.CharacterIDs...)
		for _, beat := range entry.CharacterBeats {
			requiredIDs = append(requiredIDs, beat.CharacterID)
		}
		break
	}
	if len(requiredIDs) == 0 {
		return nil
	}
	required := make(map[string]struct{}, len(requiredIDs))
	for _, id := range requiredIDs {
		required[strings.TrimSpace(id)] = struct{}{}
	}
	characters, err := st.Characters.Load()
	if err != nil {
		return nil
	}
	var expected []string
	for _, character := range characters {
		if _, ok := required[character.ID]; !ok {
			continue
		}
		for _, name := range append([]string{character.Name}, character.Aliases...) {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			expected = append(expected, name)
			if strings.Contains(content, name) {
				return nil
			}
		}
	}
	if len(expected) == 0 {
		return nil
	}
	return fmt.Errorf(
		"draft chapter %d does not contain any canonical character required by the saved chapter outline (%s); reload novel_context for the active chapter and rewrite from the authoritative story contract: %w",
		chapter,
		strings.Join(expected, ", "),
		errs.ErrToolArgs,
	)
}

func hasPositiveChapter(raw json.RawMessage) bool {
	return positiveChapter(raw) > 0
}

func positiveChapter(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var chapter int
	if json.Unmarshal(raw, &chapter) != nil || chapter <= 0 {
		return 0
	}
	return chapter
}

func inferWriterDraftChapter(st *store.Store) int {
	if st == nil {
		return 0
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return 0
	}
	if progress.InProgressChapter > 0 {
		return progress.InProgressChapter
	}
	if len(progress.PendingRewrites) == 1 {
		return progress.PendingRewrites[0]
	}
	if progress.Flow == domain.FlowRewriting || progress.Flow == domain.FlowPolishing {
		return 0
	}
	next := progress.NextChapter()
	if next <= 0 || (progress.TotalChapters > 0 && next > progress.TotalChapters) {
		return 0
	}
	return next
}

func toolSchemaWithoutRequired(schema map[string]any, field string) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		out[key] = value
	}
	switch required := out["required"].(type) {
	case []string:
		out["required"] = removeRequiredString(required, field)
	case []any:
		next := make([]any, 0, len(required))
		for _, item := range required {
			if text, ok := item.(string); ok && text == field {
				continue
			}
			next = append(next, item)
		}
		out["required"] = next
	}
	return out
}

func removeRequiredString(required []string, field string) []string {
	next := make([]string, 0, len(required))
	for _, item := range required {
		if item != field {
			next = append(next, item)
		}
	}
	return next
}
