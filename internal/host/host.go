package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/grokauth"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/flow"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	modelreg "github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/notify"
	"github.com/voocel/ainovel-cli/internal/rules"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
	"github.com/voocel/ainovel-cli/internal/userrules"
)

// Host 是运行时薄外壳。
// 职责：启动/恢复/干预注入/事件投影/模型管理。
// 不做任何调度决策，不做空闲续跑。
type Host struct {
	cfg                bootstrap.Config
	bundle             assets.Bundle
	store              *storepkg.Store
	models             *bootstrap.ModelSet
	coordinator        *agentcore.Agent
	characterAgent     agentcore.Tool
	characterWorkspace *CharacterWorkspaceService
	coordinatorCtxMgr  *corecontext.ContextEngine // 切 default/coordinator 模型时联动 SetContextWindow + SetReserveTokens
	thinkingApplier    agents.ApplyThinking       // /model 调推理强度时联动 live agent（coordinator + 子代理）
	askUser            *tools.AskUserTool
	writerRestore      *ctxpack.WriterRestorePack
	observer           *observer
	router             *flow.Dispatcher
	usage              *UsageTracker
	usageCancel        context.CancelFunc // 停掉 autoSaveLoop 并触发最后一次 flush
	budget             *BudgetSentinel    // 预算政策；未启用为 nil（方法 nil 安全）
	budgetDetach       func()
	pricingRefresh     *modelreg.PricingRefresh
	notifier           *notify.Notifier // 无人值守告警；未启用为 nil（Send nil 安全）

	events   chan Event
	streamCh chan string
	done     chan struct{}

	appendRuntimeQueue func(domain.RuntimeQueueItem) (domain.RuntimeQueueItem, error)

	doneMu     sync.Mutex
	doneClosed bool

	mu                              sync.Mutex
	adaptationPreflightMu           sync.Mutex
	lifecycle                       lifecycle
	autoResumeAttempts              int
	autoResumeCompleted             int
	autoResumeInFlight              bool
	resumeInFlight                  int
	pauseEpoch                      uint64
	normalFlowLease                 *storepkg.NormalFlowLease
	normalFlowActionRefs            int
	normalFlowScopedRefs            int
	normalFlowRunOwned              bool
	normalFlowCoCreateOwned         bool
	startingRun                     *hostStartingRun
	adaptationConfirmationFailpoint func(string) error
	closed                          bool
	cocreating                      bool // 阶段共创占用：paused 窗口内堵住 import/simulate/continue 的并发介入
	closeOnce                       sync.Once
}

type lifecycle string

type hostStartingRun struct {
	host              *Host
	cancel            context.CancelFunc
	ready             chan struct{}
	beforeLifecycle   lifecycle
	beforeAborting    bool
	beforeMessages    []agentcore.Message
	prompted          bool
	aborted           bool
	confirmationMutex bool
}

const (
	lifecycleIdle      lifecycle = "idle"
	lifecycleRunning   lifecycle = "running"
	lifecyclePaused    lifecycle = "paused"
	lifecycleCompleted lifecycle = "completed"
)

// New 创建 Host。
func New(cfg bootstrap.Config, bundle assets.Bundle) (*Host, error) {
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return nil, err
	}
	slog.Info("启动", "module", "boot", "provider", cfg.Provider, "model", cfg.ModelName, "output", cfg.OutputDir)

	store := storepkg.NewStore(cfg.OutputDir)
	if err := store.RecoverStructureMigration(); err != nil {
		return nil, fmt.Errorf("recover structure migration: %w", err)
	}
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}
	if err := store.CharacterWorkspace.RecoverInterrupted(); err != nil {
		return nil, fmt.Errorf("recover Character Agent workspace: %w", err)
	}
	// The de-AI stage is enabled when a Host owns a project. This deliberately
	// leaves already committed legacy chapters intact, while every newly drafted
	// or resumed chapter receives the same post-writing gate.
	if err := store.DeAI.Enable(); err != nil {
		return nil, fmt.Errorf("enable de-AI stage: %w", err)
	}

	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, fmt.Errorf("create models: %w", err)
	}
	slog.Info("模型就绪", "module", "boot", "summary", models.Summary())

	var h *Host
	models.SetRuntimeFallbackController(bootstrap.RuntimeFallbackControllerFunc(func(ctx context.Context, current bootstrap.ModelRef, attempted map[string]bool, err error) (bootstrap.RuntimeFallbackTarget, bool) {
		if h == nil {
			return bootstrap.RuntimeFallbackTarget{}, false
		}
		return h.selectRuntimeFallback(ctx, current, attempted, err)
	}))

	usage := NewUsageTracker(models, store)
	// 优先读 meta/usage.json；以下情况都走 sessions/*.jsonl 一次性回填：
	//   - 文件不存在（首次升级到带持久化的版本）
	//   - schema 版本不匹配（未来升级后丢弃旧格式）
	//   - 文件存在但损坏 / IO 错误（不能让坏数据让累计永久归零）
	// 回填完立即 SaveNow，把结果固化下来，下次启动直接 Load 命中。
	loaded, loadErr := usage.LoadFromStore()
	if loadErr != nil {
		slog.Warn("usage 加载失败，将尝试从 sessions 回填", "module", "usage", "err", loadErr)
	}
	if !loaded {
		if n, err := usage.ReplaySessions(cfg.OutputDir); err != nil {
			slog.Warn("usage replay 失败", "module", "usage", "err", err)
		} else if n > 0 {
			slog.Info("usage 从 session 回填完成", "module", "usage", "messages", n)
			if err := usage.SaveNow(); err != nil {
				slog.Warn("usage 回填后保存失败", "module", "usage", "err", err)
			}
		}
	}
	usageCtx, usageCancel := context.WithCancel(context.Background())
	usage.StartAutoSave(usageCtx)

	var router *flow.Dispatcher
	var budget *BudgetSentinel
	planningReviews := tools.NewPlanningReviewRunRegistry()
	coordinator, characterAgent, askUser, restore, coordinatorCtxMgr, applyThinking, buildErr := agents.BuildCoordinator(cfg, store, models, bundle, planningReviews, usage.Record, func(string) {
		if budget != nil && budget.HandleBoundary() {
			return
		}
		// A planning audit tool can open the durable user-review gate inside the
		// just-finished subagent call. Stop at that boundary immediately. Waiting
		// for another Coordinator turn first can trigger an unnecessary full
		// context summary on long proposal runs, leaving the UI looking busy even
		// though the reviewed proposal is already ready for the user.
		if h != nil && store.RunMeta.PlanningReviewPending() {
			h.stopForPlanningReview()
			return
		}
		// Character review is a durable human-confirmation boundary. Stop the
		// active Coordinator immediately after the review subagent persists a
		// passing result; otherwise the same model turn can freelance an
		// Architect dispatch before the user has published the candidate.
		if h != nil && characterConfirmationPending(store) {
			h.stopForCharacterConfirmation()
			return
		}
		if router != nil {
			router.Dispatch()
		}
	}, func(agent string, retry, maxRetries int, delay time.Duration) {
		if h == nil {
			return
		}
		summary := fmt.Sprintf("上下文摘要重试 (%d/%d): 模型返回空内容，%s 后重试", retry, maxRetries, delay)
		h.emitEvent(Event{
			Time:     time.Now(),
			Category: "SYSTEM",
			Agent:    agent,
			Summary:  summary,
			Detail:   summary,
			Kind:     "empty_context_summary",
			Level:    "warn",
		})
	}, func(event bootstrap.FailoverEvent) {
		if h != nil {
			h.reportModelFailover(event)
		}
	})
	if buildErr != nil {
		usageCancel()
		if err := usage.SaveNow(); err != nil {
			slog.Warn("Agent 构建失败后 usage 收尾落盘失败", "module", "usage", "err", err)
		}
		return nil, fmt.Errorf("build agent tool registry: %w", buildErr)
	}
	store.Signals.ClearStaleSignals()

	h = &Host{
		cfg:               cfg,
		bundle:            bundle,
		store:             store,
		models:            models,
		coordinator:       coordinator,
		characterAgent:    characterAgent,
		coordinatorCtxMgr: coordinatorCtxMgr,
		thinkingApplier:   applyThinking,
		askUser:           askUser,
		writerRestore:     restore,
		usage:             usage,
		usageCancel:       usageCancel,
		events:            make(chan Event, 4096),
		streamCh:          make(chan string, 2048),
		done:              make(chan struct{}, 4),
		lifecycle:         lifecycleIdle,
	}
	// Keep refresh ownership on Host so Close can cancel and join the cache
	// writer before a project directory or test home is removed.
	h.pricingRefresh = modelreg.StartPricingRefresh(modelreg.DefaultRegistry(), bootstrap.DefaultConfigDir())
	h.observer = newObserver(coordinator, store, h.emitEvent, h.emitDelta, h.emitClear)
	if cfg.Notify.IsEnabled() {
		h.notifier = notify.New(cfg.Notify.Command, cfg.Notify.Events)
	}
	// 预算哨兵订阅子代理边界事件执行停机；Dispatcher 由工具执行链同步触发，
	// 不再通过事件订阅抢占下一轮模型调用。
	if sentinel := NewBudgetSentinel(cfg.Budget,
		func() float64 { c, _, _, _, _ := usage.Totals(); return c },
		func(reason string) { h.abortWithEvent(reason, "error") },
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: "budget", Level: level, Title: "ainovel: 预算", Body: summary})
		},
	); sentinel != nil {
		h.budget = sentinel
		budget = sentinel
		usage.SetOnCost(sentinel.OnCost)
		h.budgetDetach = coordinator.Subscribe(sentinel.HandleEvent)
		// 计费盲区告警：模型不报 usage 时成本恒 0，预算永不触发——保险丝没接上必须喊人。
		usage.SetOnMissingUsage(func() {
			const blind = "预算盲区: 模型未返回 usage 数据，成本统计为 0，预算上限不会触发（自定义模型请确认注册表价格或上游 include_usage）"
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: blind, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: "budget", Level: "warn", Title: "ainovel: 预算", Body: blind})
		})
	}
	h.router = flow.NewDispatcher(coordinator, store, planningReviews)
	router = h.router
	// 重复指令告警：纯 telemetry，挂机时"模型可能在原地打转"值得喊人看一眼。
	// 事件流与 notify 成对发出——notify 只是屏内事件的离屏副本（架构 §2.3）。
	h.router.SetOnRepeat(func(agent, task string, n int) {
		body := fmt.Sprintf("同一指令已第 %d 次下达（%s）：%s", n, agent, task)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "指令重复: " + body, Level: "warn"})
		h.notifier.Send(notify.Notification{Kind: "repeat", Level: "warn", Title: "ainovel: 指令重复", Body: body})
	})

	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		slog.Error("初始化运行元信息失败", "module", "boot", "err", err)
	}
	h.characterWorkspace = NewCharacterWorkspaceService(store, h)

	return h, nil
}

// ── 生命周期 ──

// PrepareUserRules 在新建模式下生成本书用户规则快照（启动侧确定性，不经 Coordinator、不进主创作 Run）。
//
// 入参是用户的**原始**创作要求（未经 BuildStartPrompt 包装）——归一化要的是用户规则本身，
// 不是启动脚手架。入口须在 StartPrepared 之前调用一次（quick/cocreate 两条新建路径都走这里）。
//
// 归一化失败只降级不报错（增强路径）；只有快照无法落盘才返回 error 中止开书——
// 后续运行将没有稳定事实源（见设计 §失败与降级）。
func (h *Host) PrepareUserRules(rawPrompt string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	return h.prepareUserRules(rawPrompt, rules.SystemDefaults())
}

// PrepareExternalSourceUserRules 生成外部来源项目的用户规则快照。
// 导入续写与小说改编应保留禁语/疲劳词等机械基线，但不套用原创项目的默认章字数。
func (h *Host) PrepareExternalSourceUserRules(rawPrompt string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	return h.prepareUserRules(rawPrompt, rules.SystemDefaultsWithoutChapterWords())
}

func (h *Host) SetWordBudget(budget *domain.WordBudget) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	return h.store.RunMeta.SetWordBudget(budget)
}

func (h *Host) prepareUserRules(rawPrompt string, defaults rules.Candidate) error {
	svc := userrules.NewServiceWithSystemDefaults(h.store, h.models.Default, rules.DefaultOptions(), defaults)
	snap, err := svc.Build(context.Background(), rawPrompt)
	if err != nil {
		return fmt.Errorf("用户规则快照落盘失败，无法继续: %w", err)
	}
	logUserRulesSnapshot(snap)
	return nil
}

// ensureUserRules 惰性确保快照存在（老书无快照时按 system_defaults + rules 文件生成）。
// 恢复路径调用，让老书也能拿到 rules 文件的归一化结果。
func (h *Host) ensureUserRules() {
	svc := userrules.NewService(h.store, h.models.Default, rules.DefaultOptions())
	snap, err := svc.GetOrBuild(context.Background())
	if err != nil {
		slog.Warn("用户规则快照读取/生成失败，运行时将退到内置默认", "module", "rules", "err", err)
		return
	}
	logUserRulesSnapshot(snap)
}

// logUserRulesSnapshot 启动回显：让用户看到系统把规则理解成了什么（复用日志，不新增机制）。
func logUserRulesSnapshot(snap *rules.Snapshot) {
	if snap == nil {
		return
	}
	words := "未设置"
	if w := snap.Structured.ChapterWords; w != nil {
		words = fmt.Sprintf("%d-%d", w.Min, w.Max)
	}
	slog.Info("用户规则快照",
		"module", "rules",
		"status", string(snap.Status),
		"来源", snap.Sources,
		"章节字数", words,
		"禁用短语", len(snap.Structured.ForbiddenPhrases),
		"疲劳词", len(snap.Structured.FatigueWords),
	)
	if snap.Status == rules.StatusDegraded {
		slog.Warn("部分规则未能解析，已按 raw preferences 运行（可重新生成快照）",
			"module", "rules", "uncertain", snap.Uncertain)
	}
}

// StartPrepared 使用已编排完成的启动 prompt 开始创作。
func (h *Host) StartPrepared(promptText string) error {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("阶段共创进行中，请先结束共创")
	}
	h.mu.Unlock()
	if err := h.refuseNormalFlowDuringRevision(); err != nil {
		return err
	}

	promptText = strings.TrimSpace(promptText)
	if promptText == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	ownership, err := h.acquireNormalFlowOwnership("host:start")
	if err != nil {
		return err
	}
	defer ownership.Release()
	if err := h.store.Checkpoints.Reset(); err != nil {
		return fmt.Errorf("reset checkpoints: %w", err)
	}
	if err := h.store.Progress.Init("", 0); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}

	slog.Info("开始创作", "module", "host", "prompt_len", len(promptText))
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "开始创作", Level: "info"})
	h.observer.setAborting(false)
	// 先重置重复追踪并启用路由，再启动 Prompt，避免首轮事件先于 Enable 抵达
	h.router.ResetRepeat()
	h.router.Enable()
	runCtx, err := h.normalFlowContext(context.Background())
	if err != nil {
		return err
	}
	initialPrompt, err := h.initialRoutePrompt(promptText, false)
	if err != nil {
		return fmt.Errorf("prepare initial route: %w", err)
	}
	if err := h.coordinator.Prompt(runCtx, initialPrompt); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}

	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	ownership.TransferToRun()
	go h.waitDone()
	return nil
}

// StartAdaptationCharacterWorkflow starts the shared Character Agent before
// target Foundation generation. It preserves immutable source-analysis
// checkpoints and persists the confirmed adaptation brief as signed evidence.
func (h *Host) StartAdaptationCharacterWorkflow(promptText string) error {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("co-create is active")
	}
	h.mu.Unlock()
	if err := h.refuseNormalFlowDuringRevision(); err != nil {
		return err
	}
	promptText = strings.TrimSpace(promptText)
	if promptText == "" {
		return fmt.Errorf("adaptation character brief is required")
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	ownership, err := h.acquireNormalFlowOwnership("host:start-adaptation-character")
	if err != nil {
		return err
	}
	defer ownership.Release()
	manifest, err := h.store.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return fmt.Errorf("adaptation source manifest is required: %w", err)
	}
	intent, err := h.store.Adaptation.LoadCoCreateIntent()
	if err != nil || intent == nil {
		return fmt.Errorf("adaptation intent is required: %w", err)
	}
	coreCast, err := h.store.CoreCast.Load()
	if err != nil {
		return fmt.Errorf("load optional legacy adaptation CoreCast seed: %w", err)
	}
	coreCastSignature := ""
	if coreCast != nil {
		coreCastSignature = coreCast.ContentSignature
	}
	if err := h.store.Adaptation.SaveCharacterBrief(domain.AdaptationCharacterBrief{
		Version: 1, Brief: promptText,
		SourceSignature: storepkg.AdaptationSourceSignature(*manifest),
		IntentHash:      intent.IntentHash, CoreCastSignature: coreCastSignature,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	if err := h.store.Progress.Init("", 0); err != nil {
		return fmt.Errorf("init adaptation character progress: %w", err)
	}
	h.observer.setAborting(false)
	h.router.ResetRepeat()
	h.router.Enable()
	runCtx, err := h.normalFlowContext(context.Background())
	if err != nil {
		return err
	}
	if err := h.coordinator.Prompt(runCtx, "Complete the persisted adaptation Character Agent workflow before target Foundation planning."); err != nil {
		return fmt.Errorf("prompt adaptation character workflow: %w", err)
	}
	h.router.Dispatch()
	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	ownership.TransferToRun()
	go h.waitDone()
	return nil
}

// StartAdaptationPrepared uses an analyzed source snapshot plus a confirmed
// adaptation brief to prepare the new book foundation and enter writing.
func (h *Host) StartAdaptationPrepared(brief string) error {
	return h.StartAdaptationPreparedWithOptions(adapt.ProposalOptions{
		Brief:         brief,
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		WordTolerance: adapt.DefaultWordTolerance,
	})
}

func (h *Host) StartAdaptationPreparedWithOptions(options adapt.ProposalOptions) error {
	options.Brief = strings.TrimSpace(options.Brief)
	if options.Brief == "" {
		return fmt.Errorf("adaptation brief is required")
	}
	ownership, err := h.acquireNormalFlowOwnership("host:start-adaptation-prepared")
	if err != nil {
		return err
	}
	defer ownership.Release()
	if _, err := h.BuildAdaptationProposal(options); err != nil {
		return err
	}
	_, err = h.ConfirmAdaptationProposal()
	return err
}

func (h *Host) BuildAdaptationProposal(options adapt.ProposalOptions) (*domain.AdaptationPlan, error) {
	return h.BuildAdaptationProposalContext(context.Background(), options)
}

func (h *Host) BuildAdaptationProposalContext(ctx context.Context, options adapt.ProposalOptions) (*domain.AdaptationPlan, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return nil, fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return nil, fmt.Errorf("co-create is active")
	}
	h.mu.Unlock()

	options.Brief = strings.TrimSpace(options.Brief)
	if options.Brief == "" {
		return nil, fmt.Errorf("adaptation brief is required")
	}
	if _, _, err := adapt.ValidatePreparedSource(h.store, options.SourcePath); err != nil {
		return nil, err
	}
	if err := RequireCoreCastGate(h.store, domain.CoreCastModeAdaptation, true); err != nil {
		return nil, err
	}
	return adapt.BuildAdaptationProposalContext(ctx, h.adaptationDeps(), options)
}

func (h *Host) BuildAdaptationProposalVolumesContext(ctx context.Context, options adapt.ProposalOptions) (*adapt.ProposalStageResult, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return nil, fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return nil, fmt.Errorf("co-create is active")
	}
	h.mu.Unlock()

	options.Brief = strings.TrimSpace(options.Brief)
	if options.Brief == "" {
		return nil, fmt.Errorf("adaptation brief is required")
	}
	if _, _, err := adapt.ValidatePreparedSource(h.store, options.SourcePath); err != nil {
		return nil, err
	}
	if err := RequireCoreCastGate(h.store, domain.CoreCastModeAdaptation, true); err != nil {
		return nil, err
	}
	return adapt.BuildAdaptationProposalVolumesContext(ctx, h.adaptationDeps(), options)
}

func (h *Host) BuildAdaptationProposalVolumesForFoundationRevision(ctx context.Context, options adapt.ProposalOptions) (*adapt.ProposalStageResult, error) {
	if err := h.admitFoundationAdaptationWork(); err != nil {
		return nil, err
	}
	options.Brief = strings.TrimSpace(options.Brief)
	if options.Brief == "" {
		return nil, fmt.Errorf("adaptation brief is required")
	}
	return adapt.BuildAdaptationProposalVolumesContext(ctx, h.adaptationDeps(), options)
}

func (h *Host) BuildAdaptationProposalDetailsForFoundationRevision(ctx context.Context) (*domain.AdaptationPlan, error) {
	if err := h.admitFoundationAdaptationWork(); err != nil {
		return nil, err
	}
	return adapt.BuildAdaptationProposalDetailsContext(ctx, h.adaptationDeps(), adapt.ProposalDetailsOptions{})
}

func (h *Host) ConfirmAdaptationProposalForFoundationRevision() (*domain.AdaptationPlan, error) {
	if err := h.admitFoundationAdaptationWork(); err != nil {
		return nil, err
	}
	proposal, err := h.store.Adaptation.LoadProposal()
	if err != nil || proposal == nil {
		return nil, errors.Join(fmt.Errorf("adaptation proposal is required"), err)
	}
	if err := adapt.ValidateProposalOutlineUniqueness(*proposal); err != nil {
		return nil, err
	}
	var confirmed *domain.AdaptationPlan
	err = h.store.WithAdaptationConfirmationTransaction(func() error {
		var confirmErr error
		confirmed, confirmErr = adapt.ConfirmAdaptationProposal(context.Background(), h.adaptationDeps(), *proposal)
		return confirmErr
	})
	return confirmed, err
}

func (h *Host) admitFoundationAdaptationWork() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("host is closed")
	}
	if h.lifecycle == lifecycleRunning || h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("host is busy")
	}
	h.mu.Unlock()
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	return RequireCoreCastGate(h.store, domain.CoreCastModeAdaptation, true)
}

func (h *Host) ReviseAdaptationProposalContext(ctx context.Context, options adapt.ProposalRevisionOptions) (*domain.AdaptationPlan, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return nil, fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return nil, fmt.Errorf("co-create is active")
	}
	h.mu.Unlock()

	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := RequireCoreCastGate(h.store, domain.CoreCastModeAdaptation, true); err != nil {
		return nil, err
	}
	return adapt.ReviseAdaptationProposalContext(ctx, h.adaptationDeps(), options)
}

func (h *Host) ReviseAdaptationVolumeReviewContext(ctx context.Context, options adapt.ProposalRevisionOptions) (*domain.AdaptationVolumeReview, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return nil, fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return nil, fmt.Errorf("co-create is active")
	}
	h.mu.Unlock()

	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := RequireCoreCastGate(h.store, domain.CoreCastModeAdaptation, true); err != nil {
		return nil, err
	}
	return adapt.ReviseAdaptationVolumeReviewContext(ctx, h.adaptationDeps(), options)
}

func (h *Host) BuildAdaptationProposalDetailsContext(ctx context.Context, options adapt.ProposalDetailsOptions) (*domain.AdaptationPlan, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return nil, fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return nil, fmt.Errorf("co-create is active")
	}
	h.mu.Unlock()

	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := RequireCoreCastGate(h.store, domain.CoreCastModeAdaptation, true); err != nil {
		return nil, err
	}
	return adapt.BuildAdaptationProposalDetailsContext(ctx, h.adaptationDeps(), options)
}

func (h *Host) ConfirmAdaptationProposal() (*domain.AdaptationPlan, error) {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return nil, fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return nil, fmt.Errorf("co-create is active")
	}
	h.mu.Unlock()

	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := RequireCoreCastGate(h.store, domain.CoreCastModeAdaptation, true); err != nil {
		return nil, err
	}
	ownership, err := h.acquireNormalFlowOwnership("host:confirm-adaptation")
	if err != nil {
		return nil, err
	}
	defer ownership.Release()
	startupCtx, startupCancel := context.WithCancel(context.Background())
	startup, err := h.beginHostStartingRun(ownership, startupCancel)
	if err != nil {
		startupCancel()
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			startup.rollback()
		}
	}()
	proposal, err := h.store.Adaptation.LoadProposal()
	if err != nil {
		return nil, err
	}
	if proposal == nil {
		return nil, fmt.Errorf("adaptation proposal is required")
	}
	if err := adapt.ValidateProposalOutlineUniqueness(*proposal); err != nil {
		return nil, err
	}
	var plan *domain.AdaptationPlan
	err = h.store.WithAdaptationConfirmationTransaction(func() error {
		var persistErr error
		plan, persistErr = h.persistAdaptationConfirmationSteps(*proposal)
		if persistErr != nil {
			return persistErr
		}
		runCtx, contextErr := h.normalFlowContext(agents.WithExecutionBarrier(startupCtx, startup.ready))
		if contextErr != nil {
			return contextErr
		}
		if h.coordinator == nil {
			return fmt.Errorf("coordinator is unavailable")
		}
		if contextErr := startupCtx.Err(); contextErr != nil {
			return contextErr
		}
		if promptErr := h.coordinator.Prompt(runCtx, BuildAdaptationStartPrompt(*plan)); promptErr != nil {
			return fmt.Errorf("prompt: %w", promptErr)
		}
		startup.prompted = true
		// Keep Host admission locked from the final abort check through durable
		// transaction completion and publication of the start transition.
		h.mu.Lock()
		if h.startingRun != startup || startup.aborted || startupCtx.Err() != nil || h.lifecycle != lifecycleRunning {
			h.mu.Unlock()
			return context.Canceled
		}
		startup.confirmationMutex = true
		return nil
	})
	if err != nil {
		if startup.confirmationMutex {
			startup.confirmationMutex = false
			h.mu.Unlock()
		}
		return nil, err
	}
	slog.Info("start adaptation",
		"module", "host",
		"prompt_len", len(plan.Brief),
		"granularity", plan.Granularity,
		"rewrite_policy", plan.RewritePolicy,
		"word_tolerance", plan.WordTolerance,
	)
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "start adaptation", Level: "info"})
	h.refreshWriterRestore()
	h.router.ResetRepeat()
	h.router.Enable()
	h.router.Dispatch()
	h.startingRun = nil
	startup.confirmationMutex = false
	h.mu.Unlock()
	go h.waitDone()
	close(startup.ready)
	started = true
	return plan, nil
}

func (h *Host) GenerateAdaptationTargetFoundationContext(ctx context.Context, options adapt.TargetFoundationOptions) (*domain.AdaptationFoundationReview, error) {
	if err := RequireCoreCastGate(h.store, domain.CoreCastModeAdaptation, true); err != nil {
		return nil, err
	}
	return adapt.GenerateTargetFoundation(ctx, h.adaptationDeps(), options)
}

func (h *Host) beginHostStartingRun(ownership *normalFlowOwnership, cancel context.CancelFunc) (*hostStartingRun, error) {
	startup := &hostStartingRun{host: h, cancel: cancel, ready: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lifecycle == lifecycleRunning {
		return nil, fmt.Errorf("already running")
	}
	if h.cocreating {
		return nil, fmt.Errorf("co-create is active")
	}
	transferred := false
	ownership.once.Do(func() {
		if ownership.host == h && h.normalFlowLease != nil && h.normalFlowScopedRefs > 0 {
			h.normalFlowRunOwned = true
			h.normalFlowScopedRefs--
			transferred = true
		}
	})
	if !transferred {
		return nil, fmt.Errorf("normal flow ownership is unavailable")
	}
	startup.beforeLifecycle = h.lifecycle
	startup.beforeAborting = h.observer.aborting.Load()
	if h.coordinator != nil {
		startup.beforeMessages = h.coordinator.ExportMessages()
	}
	h.lifecycle = lifecycleRunning
	h.startingRun = startup
	h.observer.setAborting(false)
	return startup, nil
}

func (s *hostStartingRun) rollback() {
	if s == nil || s.host == nil {
		return
	}
	s.cancel()
	if s.prompted && s.host.coordinator != nil {
		s.host.coordinator.WaitForIdle()
		_ = s.host.coordinator.ImportMessages(s.beforeMessages)
	}
	h := s.host
	h.mu.Lock()
	if h.startingRun != s {
		h.mu.Unlock()
		return
	}
	h.startingRun = nil
	h.normalFlowRunOwned = false
	if s.aborted {
		h.lifecycle = lifecyclePaused
		h.observer.setAborting(true)
	} else {
		h.lifecycle = s.beforeLifecycle
		h.observer.setAborting(s.beforeAborting)
	}
	lease := h.detachUnusedNormalFlowLeaseLocked()
	h.mu.Unlock()
	h.releaseDetachedNormalFlowLease(lease)
}

func (h *Host) persistAdaptationConfirmation(proposal domain.AdaptationPlan) (*domain.AdaptationPlan, error) {
	var plan *domain.AdaptationPlan
	if err := h.store.WithAdaptationConfirmationTransaction(func() error {
		var err error
		plan, err = h.persistAdaptationConfirmationSteps(proposal)
		return err
	}); err != nil {
		return nil, err
	}
	return plan, nil
}

func (h *Host) persistAdaptationConfirmationSteps(proposal domain.AdaptationPlan) (*domain.AdaptationPlan, error) {
	if resetErr := h.store.Checkpoints.Reset(); resetErr != nil {
		return nil, fmt.Errorf("reset checkpoints: %w", resetErr)
	}
	if resetErr := h.store.Adaptation.ResetConfirmedArtifactsForProposal(); resetErr != nil {
		return nil, fmt.Errorf("reset confirmed adaptation artifacts: %w", resetErr)
	}
	if initErr := h.store.Progress.Init("", len(proposal.Chapters)); initErr != nil {
		return nil, fmt.Errorf("init progress: %w", initErr)
	}
	confirmed, confirmErr := adapt.ConfirmAdaptationProposal(context.Background(), h.adaptationDeps(), proposal)
	if confirmErr != nil {
		return nil, confirmErr
	}
	return confirmed, nil
}

func (h *Host) adaptationDeps() adapt.Deps {
	var llm imp.LLMChat
	var auditor imp.LLMChat
	var modelName string
	if h.models != nil {
		llm = h.models.ForStageWithFailover(bootstrap.StageSkeleton, h.reportAdaptationFailover)
		auditor = h.models.ForRoleWithFailover("auditor", h.reportAdaptationFailover)
		_, modelName, _ = h.models.CurrentStageSelection(bootstrap.StageSourceAnalysis)
	}
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	return adapt.Deps{
		Store:                 h.store,
		LLM:                   llm,
		Auditor:               auditor,
		ModelName:             modelName,
		ConfirmationFailpoint: h.adaptationConfirmationFailpoint,
		ModelForStage: func(stage string) imp.LLMChat {
			return h.models.ForStageWithFailover(stage, h.reportAdaptationFailover)
		},
		ModelCallMaxAttempts:                           cfg.ModelAutoSwitch.EffectiveNetworkMaxAttempts(),
		StructureRepairMaxAttempts:                     cfg.EffectiveStructureRepairMaxAttempts(),
		BudgetQualityMaxAttempts:                       cfg.EffectiveBudgetQualityMaxAttempts(),
		AdaptationOutlineAuditRetryMaxAttempts:         cfg.EffectiveAdaptationOutlineAuditRetryMaxAttempts(),
		ModelCallMaxAttemptsProvider:                   h.CurrentModelCallMaxAttempts,
		StructureRepairMaxAttemptsProvider:             h.CurrentStructureRepairMaxAttempts,
		BudgetQualityMaxAttemptsProvider:               h.CurrentBudgetQualityMaxAttempts,
		AdaptationOutlineAuditRetryMaxAttemptsProvider: h.CurrentAdaptationOutlineAuditRetryMaxAttempts,
		Prompts: adapt.Prompts{
			Foundation:      h.bundle.Prompts.ImportFoundation,
			FoundationMerge: h.bundle.Prompts.ImportFoundationMerge,
			Analyzer:        h.bundle.Prompts.ImportAnalyzer,
			Planner:         h.bundle.Prompts.AdaptationPlanner,
		},
	}
}

// Resume 恢复模式：从 checkpoint + progress 生成 resume prompt 并启动。
func (h *Host) Resume() (string, error) {
	return h.resume(false)
}

// ResumeFoundationRevision starts only the router owned by the current
// Foundation RevisionSession. It never acquires a normal-flow lease, and the
// dispatcher revalidates the persisted session fence before every dispatch.
func (h *Host) ResumeFoundationRevision() (string, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return "", fmt.Errorf("host is closed")
	}
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return "", fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return "", fmt.Errorf("co-create is in progress")
	}
	h.mu.Unlock()
	active, err := h.store.Revisions.Active()
	if err != nil || active == nil || active.Mode != domain.RevisionModeFoundation || active.Stage != domain.RevisionStageCandidateGenerating {
		if err != nil {
			return "", fmt.Errorf("load active Foundation planning revision: %w", err)
		}
		return "", fmt.Errorf("active Foundation planning revision is required")
	}
	if err := h.store.RequireConfirmedFoundation(); err != nil {
		return "", err
	}
	if h.coordinator != nil {
		h.coordinator.WaitForIdle()
	}
	prompt, label, err := buildResumePrompt(h.store)
	if err != nil {
		return "", err
	}
	if label == "" {
		label = "repairing planning after Foundation revision"
		prompt = label
	}
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	h.router.ResetRepeat()
	h.router.Enable()
	fence := storepkg.RevisionFence{Generation: active.Generation, SessionID: active.ID, Revision: active.Revision}
	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	if err := h.coordinator.Prompt(storepkg.ContextWithRevisionFence(context.Background(), fence), prompt); err != nil {
		h.mu.Lock()
		h.lifecycle = lifecycleIdle
		h.mu.Unlock()
		return "", fmt.Errorf("resume Foundation revision: %w", err)
	}
	h.router.DispatchFollowUp()
	go h.waitDone()
	return label, nil
}

func (h *Host) resume(keepNormalFlowLease bool) (string, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return "", fmt.Errorf("host is closed")
	}
	if keepNormalFlowLease && h.lifecycle == lifecyclePaused {
		h.mu.Unlock()
		return "", fmt.Errorf("automatic resume canceled by manual pause")
	}
	if !h.autoResumeInFlight {
		h.autoResumeAttempts = 0
		h.autoResumeCompleted = 0
	}
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return "", fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return "", fmt.Errorf("阶段共创进行中，请先结束共创")
	}
	resumeEpoch := h.pauseEpoch
	h.resumeInFlight++
	h.mu.Unlock()
	defer h.finishResumeAttempt()
	if err := h.ensureContinuationWritingAllowed(); err != nil {
		return "", err
	}
	if err := tools.RepairConfirmedCharacterWorkflowForResume(h.store); err != nil {
		return "", fmt.Errorf("repair confirmed Character workflow before resume: %w", err)
	}
	if err := RequireResumeCoreCastGate(h.store, true); err != nil {
		return "", err
	}
	if completed, err := h.finalizeLegacyCompletedBookBeforeResume(); err != nil {
		return "", err
	} else if completed {
		return "整本小说已通过完结审计", nil
	}
	if !keepNormalFlowLease {
		if h.coordinator != nil {
			h.coordinator.WaitForIdle()
		}
		// A paused run may have reached coordinator idle before its waitDone
		// goroutine releases ownership. Retire that epoch synchronously so the
		// resumed run cannot inherit a lease that waitDone is about to clear.
		h.releaseNormalFlowRunOwnership()
	}
	if err := h.refuseNormalFlowDuringRevision(); err != nil {
		return "", err
	}
	ownership, err := h.acquireNormalFlowOwnership("host:resume")
	if err != nil {
		return "", err
	}
	defer ownership.Release()
	if !keepNormalFlowLease {
		if err := h.restoreConfiguredModelRoutes(); err != nil {
			return "", fmt.Errorf("restore configured model routes: %w", err)
		}
	}
	pendingSteer := ""
	if meta, loadErr := h.store.RunMeta.Load(); loadErr == nil && meta != nil {
		pendingSteer = meta.PendingSteer
	}
	if _, err := h.store.ReconcilePendingRewriteProgress(); err != nil {
		return "", err
	}
	if err := h.budget.Refuse(); err != nil {
		return "", err
	}
	if _, err := h.prepareAdaptationPreflight(context.Background()); err != nil {
		return "", err
	}

	outlineRepair, err := prepareResumeOutlineRepair(h.store)
	if err != nil {
		return "", err
	}

	prompt, label, err := buildResumePrompt(h.store)
	if err != nil {
		return "", err
	}
	if label == "" {
		return "", nil // 新建模式，无恢复
	}
	if err := h.budget.Refuse(); err != nil {
		return "", err
	}

	slog.Info("恢复创作", "module", "host", "label", label)
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "恢复创作: " + label, Level: "info"})
	if notice := formatResumeOutlineRepairNotice(outlineRepair); notice != "" {
		slog.Warn("恢复前重复大纲处理", "module", "host", "detail", notice)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: notice, Level: "warn"})
	}
	for _, w := range h.store.CheckConsistency() {
		slog.Warn("一致性告警", "module", "host", "detail", w)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "一致性告警: " + w, Level: "warn"})
	}
	// 老书无快照时惰性生成（按 system_defaults + rules 文件归一化）；已有则廉价读取。
	h.ensureUserRules()
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	h.router.ResetRepeat()
	h.router.BeginResumeRecovery()
	h.router.Enable()
	runCtx, err := h.normalFlowContext(context.Background())
	if err != nil {
		return "", err
	}
	initialPrompt, err := h.initialRoutePrompt(prompt, true)
	if err != nil {
		return "", fmt.Errorf("prepare resume route: %w", err)
	}
	if err := h.coordinator.Prompt(runCtx, initialPrompt); err != nil {
		return "", fmt.Errorf("resume prompt: %w", err)
	}
	if pendingSteer != "" {
		if err := h.store.ClearHandledSteerIf(pendingSteer); err != nil {
			return "", fmt.Errorf("clear handled resume steer: %w", err)
		}
	}
	if !h.publishResumedLifecycle(resumeEpoch) {
		h.observer.setAborting(true)
		h.router.Disable()
		h.coordinator.Abort()
		h.coordinator.ClearAllQueues()
		return "", fmt.Errorf("resume canceled by manual pause")
	}
	ownership.TransferToRun()
	go h.waitDone()
	return label, nil
}

// initialRoutePrompt binds the first Coordinator turn to the route computed
// from durable state. Queueing this instruction after Prompt is racy because
// Prompt starts the model loop asynchronously.
func (h *Host) initialRoutePrompt(prompt string, resume bool) (string, error) {
	state := flow.LoadState(h.store)
	instruction := flow.Route(state)
	if resume {
		instruction = flow.RouteResume(state)
	}
	if instruction == nil {
		return prompt, nil
	}
	prepared, err := h.router.PrepareInitialInstruction(instruction)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(prompt) + "\n\n" + flow.FormatMessage(prepared), nil
}

// finalizeLegacyCompletedBookBeforeResume closes the narrow restart gap where
// every formal chapter is already committed but an old project has not yet
// passed the newer independent completion contract. It runs before normal-flow
// lease acquisition because a pending completion checkpoint intentionally
// fences ordinary writing after a restart.
func (h *Host) finalizeLegacyCompletedBookBeforeResume() (bool, error) {
	progress, err := h.store.Progress.Load()
	if err != nil {
		return false, fmt.Errorf("load progress before resume completion repair: %w", err)
	}
	if progress == nil || h.store.Adaptation.Active() || progress.Phase != domain.PhaseWriting ||
		progress.TotalChapters <= 0 || len(progress.CompletedChapters) != progress.TotalChapters ||
		len(progress.PendingRewrites) > 0 {
		return false, nil
	}
	audit, err := adapt.NewCompletionGate(h.store).EvaluateCompletion()
	if err != nil {
		_ = h.store.Progress.SetCompletionAudit("error", "")
		return false, fmt.Errorf("run completion audit before resume: %w", err)
	}
	_ = h.store.Progress.SetCompletionAudit(audit.Status, audit.ReportDigest)
	if !audit.Allowed {
		warning := strings.TrimSpace(audit.Warning)
		if warning == "" {
			warning = "independent completion audit did not pass"
		}
		return false, fmt.Errorf("repair completed legacy project before resume: %s", warning)
	}
	if err := h.store.RefreshCompletionRevalidationEvidence(); err != nil {
		return false, fmt.Errorf("refresh repaired completion evidence before resume: %w", err)
	}
	if err := h.store.Progress.MarkComplete(); err != nil {
		return false, fmt.Errorf("mark repaired legacy project complete before resume: %w", err)
	}
	return true, nil
}

// restoreConfiguredModelRoutes starts a new manual or scheduled run from the
// routes the user saved. Runtime failover may swap an in-memory route for the
// remainder of one run, but a later explicit Resume must try the configured
// primary before considering fallback providers again.
func (h *Host) restoreConfiguredModelRoutes() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.models == nil {
		return nil
	}
	return h.models.ApplyConfig(cloneHostRuntimeConfig(h.cfg))
}

// interventionMsg 把用户文本包装成 Coordinator 可识别的干预消息。
// Steer 与 Continue 共用同一 framing：两条入口的用户指令都带 `[用户干预]` 前缀，
// 才能稳定触发 coordinator.md 的干预分类。否则 Continue 的裸文本会绕过路由规则，
// Coordinator 失去分类锚点而误派子代理（曾导致"改已写章节"被派给 writer 撞 edit_chapter 守卫）。
func interventionMsg(text string) agentcore.Message {
	return agentcore.UserMsg("[用户干预] " + text)
}

// Continue 用指定 prompt 继续。停机后用户在输入框输入时调用。
func (h *Host) Continue(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	h.mu.Lock()
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("阶段共创进行中，请先结束共创")
	}
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()
	if err := h.refuseNormalFlowDuringRevision(); err != nil {
		return err
	}
	ownership, err := h.acquireNormalFlowOwnership("host:continue")
	if err != nil {
		return err
	}
	defer ownership.Release()
	if !running {
		if err := h.ensureContinuationWritingAllowed(); err != nil {
			return err
		}
		if err := h.budget.Refuse(); err != nil {
			return err
		}
		if _, err := h.prepareAdaptationPreflight(context.Background()); err != nil {
			return err
		}
	}
	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[继续] " + text, Level: "info"})

	if running {
		return h.withNormalFlowFence(func() error {
			h.coordinator.FollowUp(interventionMsg(text))
			return nil
		})
	}
	// 停机后 → 注入并自动恢复（恢复 run 也受预算前置约束）
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	h.refreshWriterRestore()
	h.observer.setAborting(false)
	runCtx, err := h.normalFlowContext(context.Background())
	if err != nil {
		return err
	}
	var injectErr error
	err = h.withNormalFlowFence(func() error {
		_, injectErr = h.coordinator.InjectContext(runCtx, interventionMsg(text))
		return injectErr
	})
	if err != nil {
		return fmt.Errorf("inject: %w", err)
	}
	h.mu.Lock()
	h.lifecycle = lifecycleRunning
	h.mu.Unlock()
	ownership.TransferToRun()
	go h.waitDone()
	return nil
}

func (h *Host) refuseNormalFlowDuringRevision() error {
	if h == nil || h.store == nil || h.store.Revisions == nil {
		return nil
	}
	active, err := h.store.Revisions.Active()
	if err != nil {
		return fmt.Errorf("read active revision before normal flow: %w", err)
	}
	if active == nil {
		if h.store.ManuscriptRevisions == nil {
			return nil
		}
		manuscript, manuscriptErr := h.store.ManuscriptRevisions.Active()
		if manuscriptErr != nil {
			return fmt.Errorf("read active manuscript revision before normal flow: %w", manuscriptErr)
		}
		if manuscript == nil {
			return nil
		}
		return fmt.Errorf("%w: %s", storepkg.ErrActiveRevisionBlocksNormalFlow, manuscript.RevisionID)
	}
	return fmt.Errorf("%w: %s", storepkg.ErrActiveRevisionBlocksNormalFlow, active.ID)
}

type normalFlowOwnership struct {
	host *Host
	once sync.Once
}

func (h *Host) acquireNormalFlowOwnership(owner string) (*normalFlowOwnership, error) {
	if h == nil || h.store == nil || h.store.Revisions == nil {
		return &normalFlowOwnership{}, nil
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = "host"
	}
	h.mu.Lock()
	if h.normalFlowLease != nil {
		if !h.normalFlowRunOwned && !h.normalFlowCoCreateOwned && h.normalFlowActionRefs == 0 && h.normalFlowScopedRefs == 0 {
			h.mu.Unlock()
			return nil, fmt.Errorf("normal flow lease has no live owner")
		}
		h.normalFlowScopedRefs++
		h.mu.Unlock()
		return &normalFlowOwnership{host: h}, nil
	}
	lease, err := h.store.Revisions.AcquireNormalFlow(owner)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	h.normalFlowLease = lease
	h.normalFlowScopedRefs = 1
	if h.router != nil {
		h.router.SetNormalFlowLease(lease)
	}
	h.mu.Unlock()
	return &normalFlowOwnership{host: h}, nil
}

func (h *Host) beginNormalFlowMutation() (func(), error) {
	ownership, err := h.acquireNormalFlowOwnership("host:mutation")
	if err != nil {
		return nil, err
	}
	return ownership.Release, nil
}

func (o *normalFlowOwnership) Release() {
	if o == nil {
		return
	}
	o.once.Do(func() {
		h := o.host
		if h == nil {
			return
		}
		h.mu.Lock()
		if h.normalFlowScopedRefs > 0 {
			h.normalFlowScopedRefs--
		}
		lease := h.detachUnusedNormalFlowLeaseLocked()
		h.mu.Unlock()
		h.releaseDetachedNormalFlowLease(lease)
	})
}

func (o *normalFlowOwnership) TransferToRun() {
	o.transfer(func(h *Host) { h.normalFlowRunOwned = true })
}

func (o *normalFlowOwnership) TransferToCoCreate() {
	o.transfer(func(h *Host) { h.normalFlowCoCreateOwned = true })
}

func (o *normalFlowOwnership) transfer(retain func(*Host)) {
	if o == nil {
		return
	}
	o.once.Do(func() {
		h := o.host
		if h == nil {
			return
		}
		h.mu.Lock()
		if h.normalFlowLease != nil && h.normalFlowScopedRefs > 0 {
			retain(h)
			h.normalFlowScopedRefs--
		}
		lease := h.detachUnusedNormalFlowLeaseLocked()
		h.mu.Unlock()
		h.releaseDetachedNormalFlowLease(lease)
	})
}

// BeginNormalFlowAction gives a Web action explicit ownership of the Host's
// durable normal-flow lease. A Web action may deliberately reuse a running
// coordinator's lease, but it never borrows an unrelated short Host call.
func (h *Host) BeginNormalFlowAction(owner string) (func(), error) {
	if h == nil || h.store == nil || h.store.Revisions == nil {
		return func() {}, nil
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = "web:action"
	}

	h.mu.Lock()
	if h.normalFlowLease != nil {
		if !h.normalFlowRunOwned && !h.normalFlowCoCreateOwned && h.normalFlowActionRefs == 0 {
			h.mu.Unlock()
			return nil, fmt.Errorf("normal flow is owned by another Host operation")
		}
		h.normalFlowActionRefs++
		h.mu.Unlock()
		return h.normalFlowActionRelease(), nil
	}
	lease, err := h.store.Revisions.AcquireNormalFlow(owner)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	h.normalFlowLease = lease
	h.normalFlowActionRefs = 1
	h.mu.Unlock()
	if h.router != nil {
		h.router.SetNormalFlowLease(lease)
	}
	return h.normalFlowActionRelease(), nil
}

func (h *Host) normalFlowActionRelease() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			if h.normalFlowActionRefs > 0 {
				h.normalFlowActionRefs--
			}
			lease := h.detachUnusedNormalFlowLeaseLocked()
			h.mu.Unlock()
			h.releaseDetachedNormalFlowLease(lease)
		})
	}
}

func (h *Host) detachUnusedNormalFlowLeaseLocked() *storepkg.NormalFlowLease {
	if h.normalFlowLease == nil || h.normalFlowRunOwned || h.normalFlowCoCreateOwned || h.normalFlowActionRefs > 0 || h.normalFlowScopedRefs > 0 {
		return nil
	}
	lease := h.normalFlowLease
	h.normalFlowLease = nil
	return lease
}

func (h *Host) releaseDetachedNormalFlowLease(lease *storepkg.NormalFlowLease) {
	if lease == nil {
		return
	}
	if h.router != nil {
		h.router.SetNormalFlowLease(nil)
	}
	if err := h.store.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
		slog.Warn("release normal flow revision fence failed", "module", "host", "err", err)
	}
}

func (h *Host) releaseNormalFlowRunOwnership() {
	if h == nil || h.store == nil || h.store.Revisions == nil {
		return
	}
	h.mu.Lock()
	h.normalFlowRunOwned = false
	lease := h.detachUnusedNormalFlowLeaseLocked()
	h.mu.Unlock()
	h.releaseDetachedNormalFlowLease(lease)
}

func (h *Host) releaseNormalFlowCoCreateOwnership() {
	if h == nil || h.store == nil || h.store.Revisions == nil {
		return
	}
	h.mu.Lock()
	h.normalFlowCoCreateOwned = false
	lease := h.detachUnusedNormalFlowLeaseLocked()
	h.mu.Unlock()
	h.releaseDetachedNormalFlowLease(lease)
}

func (h *Host) withNormalFlowFence(fn func() error) error {
	h.mu.Lock()
	lease := h.normalFlowLease
	h.mu.Unlock()
	if lease == nil {
		return fmt.Errorf("normal flow lease is not active")
	}
	fence, err := h.store.Revisions.FenceForNormalFlow(lease.Token)
	if err != nil {
		return err
	}
	return h.store.Revisions.WithFence(fence, fn)
}

func (h *Host) normalFlowContext(ctx context.Context) (context.Context, error) {
	h.mu.Lock()
	lease := h.normalFlowLease
	h.mu.Unlock()
	if lease == nil {
		return nil, fmt.Errorf("normal flow lease is not active")
	}
	fence, err := h.store.Revisions.FenceForNormalFlow(lease.Token)
	if err != nil {
		return nil, err
	}
	return storepkg.ContextWithRevisionFence(ctx, fence), nil
}

// NormalFlowActionContext binds a Web-owned background action to the active
// normal-flow lease. Writable agent tools reject contexts without this fence.
func (h *Host) NormalFlowActionContext(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return h.normalFlowContext(ctx)
}

// Steer 提交用户干预。
func (h *Host) Steer(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		if !running {
			return fmt.Errorf("set pending steer: %w", err)
		}
		return err
	}
	defer release()

	h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[用户干预] " + text, Level: "info"})

	msg := interventionMsg(text)
	if running {
		if _, err := h.coordinator.Inject(msg); err != nil {
			slog.Error("steer inject 失败", "module", "host", "err", err)
			return fmt.Errorf("steer inject: %w", err)
		}
		return nil
	}
	// 停机：持久化待下次启动 + 反馈系统状态（"已保存"是 USER 事件之外的系统提示）
	if err := h.store.RunMeta.SetPendingSteer(text); err != nil {
		return fmt.Errorf("set pending steer: %w", err)
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "干预已保存，下次启动时生效", Level: "info"})
	return nil
}

// Abort 暂停当前 coordinator。
func (h *Host) Abort() bool {
	return h.abortWithEvent("用户手动暂停当前创作", "warn")
}

func (h *Host) finishResumeAttempt() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.resumeInFlight > 0 {
		h.resumeInFlight--
	}
}

func (h *Host) publishResumedLifecycle(expectedPauseEpoch uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pauseEpoch != expectedPauseEpoch {
		return false
	}
	h.lifecycle = lifecycleRunning
	return true
}

func (h *Host) pauseActiveRun() (*hostStartingRun, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	active := h.lifecycle == lifecycleRunning || h.autoResumeInFlight || h.resumeInFlight > 0
	if !active {
		return nil, false
	}
	h.pauseEpoch++
	h.lifecycle = lifecyclePaused
	if h.startingRun != nil {
		h.startingRun.aborted = true
	}
	return h.startingRun, true
}

// abortWithEvent 以指定原因事件执行暂停。预算停机与手动暂停共用同一停机机制，
// 仅事件文案不同（预算停机=用户预先签署的 Abort 指令，语义等同手动暂停）。
func (h *Host) abortWithEvent(summary, level string) bool {
	startup, active := h.pauseActiveRun()
	if !active {
		return false
	}
	// 置位必须在 coordinator.Abort 之前：cancel 传播会立刻引发 stream init / subagent
	// 失败事件，observer 凭此标志识别为 abort 衍生噪声并抑制。
	h.observer.setAborting(true)
	h.router.Disable()
	if startup != nil {
		startup.cancel()
	}
	h.coordinator.Abort()
	h.coordinator.ClearAllQueues()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
	return true
}

// stopForPlanningReview ends the active Coordinator run at a durable review
// gate without marking the project as user-paused. The proposal and audit
// reports are already persisted when this is called; no model turn is needed
// to acknowledge the transition.
func (h *Host) stopForPlanningReview() bool {
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	if running {
		h.lifecycle = lifecycleIdle
	}
	h.mu.Unlock()
	if !running {
		return false
	}
	h.observer.setAborting(true)
	h.router.Disable()
	h.coordinator.Abort()
	h.coordinator.ClearAllQueues()
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  "规划自动审核完成，等待用户审核",
		Level:    "success",
	})
	return true
}

func characterConfirmationPending(st *storepkg.Store) bool {
	if st == nil {
		return false
	}
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(st)
	if err != nil || candidate == nil || lifecycle == nil {
		return false
	}
	return lifecycle.AnalysisStatus == domain.CharacterCardAnalysisCandidateReady &&
		lifecycle.ReviewStatus == domain.CharacterCardReviewPassed &&
		lifecycle.ConfirmationStatus == domain.CharacterCardUnconfirmed &&
		lifecycle.Candidate == binding.Candidate &&
		lifecycle.ReviewedCandidate == binding.Candidate &&
		lifecycle.ReviewedInputDigest == binding.InputDigest
}

// stopForCharacterConfirmation ends the active run at the persisted reviewed
// candidate. ConfirmCharacterCandidate publishes it and resumes the normal
// planning route; no Architect is allowed to run before that explicit action.
func (h *Host) stopForCharacterConfirmation() bool {
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	if running {
		h.lifecycle = lifecycleIdle
	}
	h.mu.Unlock()
	if !running {
		return false
	}
	h.observer.setAborting(true)
	h.router.Disable()
	h.coordinator.Abort()
	h.coordinator.ClearAllQueues()
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  "角色独立审核已通过，等待用户确认并发布本轮角色候选",
		Level:    "success",
	})
	return true
}

// Close 终止 coordinator 并关闭事件通道。
//
// Usage 持久化语义：先取消 autoSaveLoop（它自行 flush 最后一次 dirty 状态），
// 再补一次同步 SaveNow 收尾。已知缺口：AbortSilent 之后若仍有 in-flight LLM
// 调用回来，触发的 OnMessage → Record 会更新内存但**不会被持久化**。这部分
// "最末几百 token" 的丢失在下次启动时会由 session jsonl replay 自动补回。
func (h *Host) Close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.observer.setAborting(true)
	h.coordinator.AbortSilent()
	// Do not wait for the coordinator here. A model stream may ignore context
	// cancellation and remain blocked indefinitely; waitDone already owns run
	// finalization and releases its normal-flow lease only after durable terminal
	// state has been published. Waiting (or releasing that lease) here would
	// either deadlock Close or let a revision cross unfinished finalization.
	h.releaseNormalFlowCoCreateOwnership()
	if h.budgetDetach != nil {
		h.budgetDetach()
		h.budgetDetach = nil
	}
	if h.usageCancel != nil {
		h.usageCancel()
		h.usageCancel = nil
	}
	if h.pricingRefresh != nil {
		h.pricingRefresh.Close()
		h.pricingRefresh = nil
	}
	if err := h.usage.SaveNow(); err != nil {
		slog.Warn("usage 退出前落盘失败", "module", "usage", "err", err)
	}
	h.closeOnce.Do(func() {
		h.closeDone()
		close(h.events)
		close(h.streamCh)
	})
}

// waitDone 等待 coordinator 停机并发布终态事件；未完结创作会先走有上限的自动恢复。
//
// Run 结束后的处理：
//   - Phase=Complete  → 标记 completed，发"创作完成"事件
//   - 未完结且未超过恢复上限 → 从持久化 checkpoint 自动 Resume
//   - 其它                  → 标记 idle，发"Coordinator 停止"事件
//
// 手动暂停仍只保留 paused 状态，不会被自动恢复；恢复上限耗尽后也等待用户或定时任务。
func (h *Host) waitDone() {
	if h.coordinator != nil {
		h.coordinator.WaitForIdle()
	}
	h.observer.finalize()
	if h.tryRepairStoppedAdaptation() {
		return
	}
	if h.tryAutoResumeIncompleteRun() {
		return
	}
	// Successful repair/auto-resume paths return above after handing ownership
	// to the replacement run. A genuinely terminal run keeps its ownership
	// until lifecycle state, durable terminal events, and Done notification are
	// all finalized.
	defer h.releaseNormalFlowRunOwnership()

	h.mu.Lock()
	progress, _ := h.store.Progress.Load()
	if progress != nil && progress.Phase == domain.PhaseComplete {
		h.lifecycle = lifecycleCompleted
		summary := fmt.Sprintf("创作完成: %d 章 %d 字", len(progress.CompletedChapters), progress.TotalWordCount)
		h.mu.Unlock()
		slog.Info(summary, "module", "host")
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "success"})
		h.notifier.Send(notify.Notification{
			Kind: "run_end", Level: "info", Title: "ainovel: 创作完成",
			Body: h.runEndBody(progress.NovelName, summary),
		})
	} else {
		wasRunning := h.lifecycle == lifecycleRunning
		if wasRunning {
			h.lifecycle = lifecycleIdle
		}
		completed := 0
		name := ""
		if progress != nil {
			completed = len(progress.CompletedChapters)
			name = progress.NovelName
		}
		h.mu.Unlock()
		if wasRunning {
			summary := fmt.Sprintf("Coordinator 停止 (已完成 %d 章)", completed)
			slog.Warn(summary, "module", "host")
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "warn"})
			h.notifier.Send(notify.Notification{
				Kind: "run_end", Level: "warn", Title: "ainovel: 创作停止",
				Body: h.runEndBody(name, summary),
			})
		}
	}

	h.notifyDone()
}

const maxAutomaticResumeAttempts = 3

// hasIncompleteCreativeProgress reports whether an unexpected coordinator stop
// still has ordinary writing work to resume. A paused Host never reaches the
// automatic path because abortWithEvent changes lifecycle before cancellation.
func hasIncompleteCreativeProgress(progress *domain.Progress) bool {
	if progress == nil || progress.Phase != domain.PhaseWriting {
		return false
	}
	if progress.InProgressChapter > 0 {
		return true
	}
	return progress.TotalChapters > 0 && len(progress.CompletedChapters) < progress.TotalChapters
}

// tryAutoResumeIncompleteRun converts a transient coordinator stop into a
// bounded Resume. It deliberately resumes from persisted progress rather than
// injecting a guessed instruction, so pending rewrites, outline gates, and
// chapter checkpoints keep their existing routing rules.
func (h *Host) tryAutoResumeIncompleteRun() bool {
	progress, err := h.store.Progress.Load()
	if err != nil || !hasIncompleteCreativeProgress(progress) {
		return false
	}

	h.mu.Lock()
	if h.closed || h.lifecycle != lifecycleRunning || h.cocreating || h.autoResumeInFlight {
		h.mu.Unlock()
		return false
	}
	completed := len(progress.CompletedChapters)
	if completed > h.autoResumeCompleted {
		h.autoResumeAttempts = 0
		h.autoResumeCompleted = completed
	}
	if h.autoResumeAttempts >= maxAutomaticResumeAttempts {
		h.mu.Unlock()
		h.emitEvent(Event{
			Time:     time.Now(),
			Category: "SYSTEM",
			Summary:  fmt.Sprintf("自动恢复已达上限（同一进度%d次），等待手动恢复", maxAutomaticResumeAttempts),
			Level:    "warn",
		})
		return false
	}
	h.autoResumeAttempts++
	attempt := h.autoResumeAttempts
	h.autoResumeInFlight = true
	h.lifecycle = lifecycleIdle
	h.mu.Unlock()

	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("Coordinator 异常停止，自动恢复创作（第%d/%d次，已完成%d章）", attempt, maxAutomaticResumeAttempts, completed),
		Level:    "warn",
	})
	_, resumeErr := h.resume(true)
	h.mu.Lock()
	h.autoResumeInFlight = false
	h.mu.Unlock()
	if resumeErr != nil {
		h.emitEvent(Event{
			Time:     time.Now(),
			Category: "ERROR",
			Summary:  "自动恢复创作失败",
			Detail:   resumeErr.Error(),
			Level:    "error",
		})
		return false
	}
	return true
}

// tryRepairStoppedAdaptation handles the common legacy-plan failure where the
// Coordinator has no safe next route because the confirmed adaptation outline
// failed a plan-only gate. The repair runs at Host level, then the replacement
// Resume explicitly reuses the current run ownership so this is not a one-off
// chapter retry and no revision can enter between the two runs.
func (h *Host) tryRepairStoppedAdaptation() bool {
	state := flow.LoadState(h.store)
	if state.RevisionActive || !state.AdaptationOutlineBlocked {
		return false
	}
	report, err := h.prepareAdaptationPreflight(context.Background())
	if err != nil {
		h.emitEvent(Event{
			Time:     time.Now().UTC(),
			Category: "ERROR",
			Summary:  "创作停止前的大纲定向修复失败",
			Detail:   err.Error(),
			Level:    "error",
		})
		return false
	}
	if !report.Changed {
		return false
	}
	h.mu.Lock()
	if h.lifecycle != lifecycleRunning {
		h.mu.Unlock()
		return false
	}
	h.lifecycle = lifecycleIdle
	h.mu.Unlock()
	if _, err := h.resume(true); err != nil {
		h.emitEvent(Event{
			Time:     time.Now().UTC(),
			Category: "ERROR",
			Summary:  "大纲修复完成后恢复创作失败",
			Detail:   err.Error(),
			Level:    "error",
		})
		return false
	}
	return true
}

func (h *Host) notifyDone() {
	h.doneMu.Lock()
	defer h.doneMu.Unlock()
	if h.doneClosed || h.done == nil {
		return
	}
	select {
	case h.done <- struct{}{}:
	default:
	}
}

func (h *Host) closeDone() {
	h.doneMu.Lock()
	defer h.doneMu.Unlock()
	if h.doneClosed {
		return
	}
	h.doneClosed = true
	if h.done != nil {
		close(h.done)
	}
}

// runEndBody 组装 run_end 通知正文：书名 + 进度摘要 + 累计花费。
func (h *Host) runEndBody(novelName, summary string) string {
	if name := strings.TrimSpace(novelName); name != "" {
		summary = "《" + name + "》" + summary
	}
	cost, _, _, _, _ := h.usage.Totals()
	if cost > 0 {
		summary += fmt.Sprintf(" · 花费 $%.2f", cost)
	}
	return summary
}

// ── 通道 ──

// StreamClearSentinel 通过 streamCh 单条发送以示意"清空当前流式 round"。
// 不再用独立 clearCh —— 双通道无序导致 ✻ header 时常落到上一个 round 末尾。
const StreamClearSentinel = "\x00\x00CLEAR\x00\x00"

func (h *Host) Events() <-chan Event        { return h.events }
func (h *Host) Stream() <-chan string       { return h.streamCh }
func (h *Host) Done() <-chan struct{}       { return h.done }
func (h *Host) Dir() string                 { return h.store.Dir() }
func (h *Host) AskUser() *tools.AskUserTool { return h.askUser }

// RecordEvent lets Web-side long actions use the same durable event journal as
// Coordinator/Writer events. It intentionally has no separate persistence
// path, so reopening a project sees one monotonic queue instead of two logs.
func (h *Host) RecordEvent(ev Event) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		slog.Warn("record event blocked by revision ownership", "module", "host", "err", err)
		return
	}
	defer release()
	h.emitEvent(ev)
}

// ── 事件发射 ──

func (h *Host) emitEvent(ev Event) {
	defer func() { recover() }()
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	h.persistRuntimeEvent(ev)
	// 所有事件的唯一 slog 入口。observer 翻译的 agentcore 事件和 Host 自发的
	// SYSTEM 事件（Start/Abort/Resume…）都在这里落日志，避免 ESC abort 与外部
	// 终止原因需要在结构化事件中可区分。
	if ev.Summary != "" || ev.Detail != "" {
		level := slog.LevelInfo
		switch ev.Level {
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		// 日志记完整 Detail（排查用，不截断）；Detail 为空才回退到 Summary。
		msg := ev.Detail
		if msg == "" {
			msg = ev.Summary
		}
		attrs := []any{"module", "event", "category", ev.Category, "agent", ev.Agent}
		if ev.Kind != "" {
			attrs = append(attrs, "kind", ev.Kind)
		}
		slog.Log(context.Background(), level, msg, attrs...)
	}
	select {
	case h.events <- ev:
	default:
		select {
		case <-h.events:
		default:
		}
		select {
		case h.events <- ev:
		default:
		}
	}
}

// persistRuntimeEvent is the single durable event boundary. Persisting both
// start and finish states makes a project recoverable even when the process is
// interrupted between a tool call and its completion; the web session's
// hostEventID upsert collapses the replayed pair back to one visible row.
func (h *Host) persistRuntimeEvent(ev Event) {
	if h == nil || h.store == nil || h.store.Runtime == nil {
		return
	}
	priority := domain.RuntimePriorityBackground
	if ev.Category == "SYSTEM" || ev.Category == "ERROR" {
		priority = domain.RuntimePriorityControl
	}
	appendQueue := h.store.Runtime.AppendQueue
	if h.appendRuntimeQueue != nil {
		appendQueue = h.appendRuntimeQueue
	}
	if _, err := appendQueue(domain.RuntimeQueueItem{
		Time:     ev.Time,
		Kind:     domain.RuntimeQueueUIEvent,
		Priority: priority,
		Category: ev.Category,
		Summary:  ev.Summary,
		Payload:  ev,
	}); err != nil {
		// Do not recursively emit another event when the event ledger itself is
		// unavailable. The structured slog record is the last-resort diagnostic.
		slog.Error("runtime event persistence failed", "module", "event", "category", ev.Category, "summary", ev.Summary, "err", err)
		h.notifier.Send(notify.Notification{
			Kind: "runtime_log", Level: "error", Title: "ainovel: 日志落盘失败",
			Body: fmt.Sprintf("%s：%v", ev.Summary, err),
		})
	}
}

func (h *Host) emitDelta(delta string) {
	defer func() { recover() }()
	select {
	case h.streamCh <- delta:
	default:
		select {
		case <-h.streamCh:
		default:
		}
		select {
		case h.streamCh <- delta:
		default:
		}
	}
}

func (h *Host) emitClear() {
	// 通过 streamCh 走"sentinel"，保证与 emitDelta 在同一条通道里有序送达 TUI。
	h.emitDelta(StreamClearSentinel)
}

// ── Snapshot (TUI 状态聚合) ──

func (h *Host) Snapshot() UISnapshot {
	if err := h.store.RecoverStructureMigration(); err != nil {
		return UISnapshot{RecoveryError: err.Error()}
	}

	h.mu.Lock()
	state := h.lifecycle
	provider, model, _ := h.models.CurrentSelection("default")
	h.mu.Unlock()

	// 动态解析当前模型的上下文窗口，/model 切换后下一次 Snapshot 自动反映
	modelWindow, _ := h.cfg.ResolveContextWindow(model)
	cost, tokIn, tokOut, cacheRead, cacheWrite := h.usage.Totals()
	saved := h.usage.SavedUSD()
	overallCapable := h.usage.OverallCacheCapable()
	recentRead, recentInput, recentSamples := h.usage.OverallRecent()
	perAgent := h.usage.PerAgent()
	cacheStats := make([]AgentCacheStat, 0, len(perAgent))
	for _, a := range perAgent {
		cacheStats = append(cacheStats, AgentCacheStat{
			Role:            a.Role,
			Input:           a.Input,
			Output:          a.Output,
			CacheRead:       a.CacheRead,
			CacheWrite:      a.CacheWrite,
			Cost:            a.Cost,
			Saved:           a.Saved,
			CacheCapable:    a.CacheCapable,
			RecentCacheRead: a.RecentCacheRead,
			RecentInput:     a.RecentInput,
			RecentSamples:   a.RecentSamples,
		})
	}
	perModel := h.usage.PerModel()
	modelStats := make([]AgentCacheStat, 0, len(perModel))
	for _, a := range perModel {
		modelStats = append(modelStats, AgentCacheStat{
			Model:        a.Model,
			Input:        a.Input,
			Output:       a.Output,
			CacheRead:    a.CacheRead,
			CacheWrite:   a.CacheWrite,
			Cost:         a.Cost,
			Saved:        a.Saved,
			CacheCapable: a.CacheCapable,
		})
	}

	snap := UISnapshot{
		Provider:               provider,
		ModelName:              model,
		ModelContextWindow:     modelWindow,
		Style:                  h.cfg.Style,
		SimulationMode:         h.cfg.EffectiveSimulationMode(),
		RuntimeState:           string(state),
		IsRunning:              state == lifecycleRunning,
		TotalInputTokens:       tokIn,
		TotalOutputTokens:      tokOut,
		TotalCacheReadTokens:   cacheRead,
		TotalCacheWriteTokens:  cacheWrite,
		TotalCostUSD:           cost,
		TotalSavedUSD:          saved,
		BudgetLimitUSD:         h.budget.Limit(),
		OverallCacheCapable:    overallCapable,
		OverallRecentCacheRead: recentRead,
		OverallRecentInput:     recentInput,
		OverallRecentSamples:   recentSamples,
		CachePerAgent:          cacheStats,
		CachePerModel:          modelStats,
		MissingAssistantUsage:  h.usage.MissingAssistantUsage(),
	}

	progress, _ := h.store.Progress.Load()
	if progress != nil {
		snap.NovelName = strings.TrimSpace(progress.NovelName)
		snap.Phase = string(progress.Phase)
		snap.Flow = string(progress.Flow)
		snap.CurrentChapter = progress.CurrentChapter
		snap.TotalChapters = progress.TotalChapters
		snap.CompletedCount = len(progress.CompletedChapters)
		snap.TotalWordCount = progress.TotalWordCount
		snap.InProgressChapter = progress.InProgressChapter
		snap.PendingRewrites = progress.PendingRewrites
		snap.RewriteReason = progress.RewriteReason
		snap.Layered = progress.Layered
		if progress.CurrentVolume > 0 {
			snap.CurrentVolumeArc = fmt.Sprintf("第%d卷·第%d弧", progress.CurrentVolume, progress.CurrentArc)
		}
	}
	if snap.NovelName == "" {
		if premise, _ := h.store.Outline.LoadPremise(); premise != "" {
			snap.NovelName = domain.ExtractNovelNameFromPremise(premise)
		}
	}
	if meta, _ := h.store.RunMeta.Load(); meta != nil {
		snap.PendingSteer = meta.PendingSteer
		snap.WordBudget = meta.WordBudget
		snap.PlanningReview = planningReviewSummary(meta.PlanningReview)
	}
	if continuation, err := h.ContinuationSnapshot(); err == nil {
		snap.Continuation = continuation
	}

	snap.Agents = h.observer.agentSnapshots()
	h.fillContextStatus(&snap)
	snap.StatusLabel = deriveStatusLabel(snap)

	// 恢复标签
	if _, label, err := buildResumePrompt(h.store); err == nil && label != "" {
		snap.RecoveryLabel = label
	}

	h.fillDetails(&snap, progress)

	return snap
}

// fillContextStatus 填充 Coordinator 上下文健康度信息。
func (h *Host) fillContextStatus(snap *UISnapshot) {
	if h.coordinator == nil {
		return
	}
	if usage := h.coordinator.BaselineContextUsage(); usage != nil {
		snap.ContextTokens = usage.Tokens
		snap.ContextWindow = usage.ContextWindow
		snap.ContextPercent = usage.Percent
	}
	if ctx := h.coordinator.ContextSnapshot(); ctx != nil {
		snap.ContextScope = ctx.Scope
		snap.ContextStrategy = ctx.LastStrategy
		snap.ContextActiveMessages = ctx.ActiveMessages
		snap.ContextSummaryCount = ctx.SummaryMessages
		snap.ContextCompactedCount = ctx.LastCompactedCount
		snap.ContextKeptCount = ctx.LastKeptCount
		if snap.ContextTokens == 0 {
			if ctx.BaselineUsage != nil {
				snap.ContextTokens = ctx.BaselineUsage.Tokens
				snap.ContextWindow = ctx.BaselineUsage.ContextWindow
				snap.ContextPercent = ctx.BaselineUsage.Percent
			} else if ctx.Usage != nil {
				snap.ContextTokens = ctx.Usage.Tokens
				snap.ContextWindow = ctx.Usage.ContextWindow
				snap.ContextPercent = ctx.Usage.Percent
			}
		}
	}
}

// fillDetails 填充详情区:设定、角色、最近 commit/review/摘要。
func (h *Host) fillDetails(snap *UISnapshot, progress *domain.Progress) {
	if source, _ := h.store.Adaptation.LoadSourceFoundation(); source != nil {
		snap.AdaptationSourceFoundation = source
	}
	if coreCast, _ := h.store.CoreCast.Load(); coreCast != nil && coreCast.Mode == domain.CoreCastModeAdaptation {
		coreCastCopy := *coreCast
		snap.AdaptationCoreCast = &coreCastCopy
	}
	if review, _ := h.store.Adaptation.LoadTargetFoundationReview(); review != nil {
		snap.AdaptationFoundationReview = review
		if target, err := h.store.Foundation.Load(); err == nil {
			targetCopy := domain.CloneStoryFoundation(target)
			snap.TargetFoundation = &targetCopy
		}
	}
	if workflow, _ := h.store.Adaptation.LoadPlanningWorkflow(); workflow != nil {
		snap.AdaptationPlanningWorkflow = workflow
		if snap.AdaptationFoundationReview == nil && workflow.Stage == domain.AdaptationPlanningStageTargetFoundationGenerating && snap.AdaptationSourceFoundation != nil {
			state := domain.AdaptationFoundationReviewGenerating
			reason := ""
			if progress != nil && (len(progress.CompletedChapters) > 0 || progress.TotalWordCount > 0) {
				state = domain.AdaptationFoundationReviewReadonly
				reason = "正文已经存在；本期只展示迁移状态，不回退或改写目标设定"
			}
			snap.AdaptationFoundationReview = &domain.AdaptationFoundationReview{
				Version: domain.AdaptationFoundationReviewVersion, State: state,
				ReadonlyReason: reason, BlockingReasons: []string{"legacy adaptation requires explicit target Foundation checkpoint"},
			}
			if target, err := h.store.Foundation.Load(); err == nil {
				targetCopy := domain.CloneStoryFoundation(target)
				snap.TargetFoundation = &targetCopy
			}
		}
	}
	if review, _ := h.store.Adaptation.LoadVolumeReview(); review != nil {
		snap.AdaptationVolumeReview = review
		snap.VolumeReviewSummary = adaptationVolumeReviewSummary(review)
	}
	var proposal *domain.AdaptationPlan
	if loaded, _ := h.store.Adaptation.LoadProposal(); loaded != nil {
		proposal = loaded
		snap.AdaptationProposal = loaded
		snap.ProposalSummary = adaptationPlanSummary(loaded)
	}
	var plan *domain.AdaptationPlan
	if loaded, _ := h.store.Adaptation.LoadPlan(); loaded != nil {
		plan = loaded
		snap.AdaptationPlan = loaded
		snap.AdaptationSummary = adaptationPlanSummary(loaded)
	}
	adaptationChapters := activeAdaptationChapters(plan, proposal)

	if premise, _ := h.store.Outline.LoadPremise(); premise != "" {
		snap.Premise = truncate(premise, 80)
		snap.PremiseFull = premise
	}
	if outline, _ := h.store.Outline.LoadOutline(); len(outline) > 0 {
		for _, e := range outline {
			var adaptation *domain.AdaptationChapterPlan
			if chapter, ok := adaptationChapters[e.Chapter]; ok {
				adaptation = &chapter
			}
			snap.Outline = append(snap.Outline, outlineSnapshotFromEntry(e, progress, snap.WordBudget, adaptation))
		}
	} else if plan != nil {
		snap.Outline = outlineSnapshotsFromAdaptation(plan, progress, snap.WordBudget)
	} else if proposal != nil {
		snap.Outline = outlineSnapshotsFromAdaptation(proposal, progress, snap.WordBudget)
	}
	if progress != nil && progress.Layered {
		if compass, _ := h.store.Outline.LoadCompass(); compass != nil {
			snap.CompassDirection = compass.EndingDirection
			snap.CompassScale = compass.EstimatedScale
		}
		if volumes, _ := h.store.Outline.LoadLayeredOutline(); len(volumes) > 0 {
			snap.LayeredOutline = layeredVolumeSnapshots(volumes)
			for _, v := range volumes {
				if v.Index > progress.CurrentVolume {
					snap.NextVolumeTitle = v.Title
					break
				}
			}
		}
	}
	snap.SimulationProfile = nil
	snap.SimulationSummary = buildSimulationProfileSummary(h.store, snap.SimulationMode, snap.CurrentChapter)
	if chars, _ := h.store.Characters.Load(); len(chars) > 0 {
		snap.CharacterDetails = append([]domain.Character(nil), chars...)
		for _, c := range chars {
			label := c.Name
			if c.Role != "" {
				label += "（" + c.Role + "）"
			}
			snap.Characters = append(snap.Characters, label)
		}
	}
	if rules, _ := h.store.World.LoadWorldRules(); len(rules) > 0 {
		snap.WorldRules = append([]domain.WorldRule(nil), rules...)
	}
	if foundation, err := h.store.Foundation.Load(); err == nil {
		snap.PlannedRelationships = append([]domain.CharacterRelationship(nil), foundation.Relationships...)
		snap.FoundationAuditSignature, _ = domain.FoundationAuditSignature(foundation)
		if contract, loadErr := h.store.CoreCast.Load(); loadErr == nil && contract != nil {
			for _, member := range contract.Members {
				snap.CoreCharacterIDs = append(snap.CoreCharacterIDs, member.Character.ID)
			}
			snap.CoreCastPreserved = domain.ValidateFoundationPreservesCoreCast(foundation, *contract) == nil
		}
	}
	if candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(h.store); err != nil {
		snap.CharacterWorkflow = &CharacterWorkflowSummary{StateError: err.Error()}
	} else if candidate != nil {
		summary := &CharacterWorkflowSummary{
			Candidate:         &candidate.Foundation,
			CandidateRevision: candidate.Revision,
			CandidateDigest:   binding.Candidate.CharacterContentDigest,
		}
		if lifecycle != nil {
			summary.AnalysisStatus = lifecycle.AnalysisStatus
			summary.ReviewStatus = lifecycle.ReviewStatus
			summary.ConfirmationStatus = lifecycle.ConfirmationStatus
			summary.Completeness = append([]domain.CharacterCardCompletenessResult(nil), lifecycle.Completeness...)
			summary.Findings = append([]domain.CharacterCardReviewFinding(nil), lifecycle.Findings...)
			if lifecycle.Error != nil {
				copied := *lifecycle.Error
				summary.Error = &copied
			}
		}
		snap.CharacterWorkflow = summary
	}
	if ledger, _ := h.store.Cast.Load(); len(ledger) > 0 {
		snap.SupportingCount = len(ledger)
		recent, _ := h.store.Cast.RecentActive(5)
		for _, e := range recent {
			label := e.Name
			if e.BriefRole != "" {
				label += "（" + e.BriefRole + "）"
			}
			snap.RecentSupporting = append(snap.RecentSupporting, label)
		}
	}
	if progress != nil && len(progress.CompletedChapters) > 0 {
		lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
		wc := progress.ChapterWordCounts[lastCh]
		snap.LastCommitSummary = fmt.Sprintf("第%d章 %d字", lastCh, wc)
	}
	currentCh := 1
	if progress != nil && len(progress.CompletedChapters) > 0 {
		currentCh = progress.CompletedChapters[len(progress.CompletedChapters)-1]
	}
	if review, err := h.store.World.LoadLastReview(currentCh); err == nil && review != nil {
		snap.LastReviewSummary = fmt.Sprintf("verdict=%s %d个问题", review.Verdict, len(review.Issues))
		if len(review.AffectedChapters) > 0 {
			snap.LastReviewSummary += fmt.Sprintf(" 影响%v", review.AffectedChapters)
		}
	}
	if cp := h.store.Checkpoints.LatestGlobal(); cp != nil {
		snap.LastCheckpointName = fmt.Sprintf("%s.%s", cp.Scope, cp.Step)
	}
	if progress != nil {
		for i := len(progress.CompletedChapters) - 1; i >= 0 && len(snap.RecentSummaries) < 2; i-- {
			ch := progress.CompletedChapters[i]
			if summary, err := h.store.Summaries.LoadSummary(ch); err == nil && summary != nil {
				snap.RecentSummaries = append(snap.RecentSummaries,
					fmt.Sprintf("第%d章: %s", ch, truncate(summary.Summary, 50)))
			}
		}
	}
	snap.CreativeBlueprint = creativeBlueprintSummary(snap)
}

func deriveStatusLabel(s UISnapshot) string {
	switch {
	case s.Phase == string(domain.PhaseComplete):
		return "COMPLETE"
	case s.Flow == string(domain.FlowReviewing):
		return "REVIEW"
	case s.Flow == string(domain.FlowRewriting) || s.Flow == string(domain.FlowPolishing):
		return "REWRITE"
	case s.RuntimeState == "running":
		return "RUNNING"
	default:
		return "READY"
	}
}

// ── 模型管理 ──

var projectAgentModelRoles = []string{"coordinator", "architect", "character", "writer", "editor", "auditor"}

func (h *Host) selectRuntimeFallback(ctx context.Context, current bootstrap.ModelRef, attempted map[string]bool, cause error) (bootstrap.RuntimeFallbackTarget, bool) {
	h.mu.Lock()
	cfg := cloneHostRuntimeConfig(h.cfg)
	h.mu.Unlock()
	if !cfg.PersistProjectOverlay || !cfg.ModelAutoSwitch.IsEnabled() {
		return bootstrap.RuntimeFallbackTarget{}, false
	}

	for _, provider := range bootstrap.RuntimeAutoSwitchCandidateProviders(cfg, current.Provider, attempted) {
		model, ok := matchingRuntimeFallbackModel(cfg.CandidateModels(provider), current.Model)
		if !ok {
			continue
		}
		pc := cfg.Providers[provider]
		candidate, err := bootstrap.NewProviderModelWithConfig(cfg, provider, model, pc)
		if err != nil || candidate == nil || !candidate.SupportsTools() {
			continue
		}
		reason := fmt.Sprintf("%s:%s->%s", bootstrap.RuntimeFallbackPoolReasonPrefix, current.Provider, provider)
		return bootstrap.RuntimeFallbackTarget{
			Provider: provider,
			Model:    model,
			LLM:      candidate,
			Reason:   reason,
		}, true
	}
	return bootstrap.RuntimeFallbackTarget{}, false
}

func matchingRuntimeFallbackModel(models []string, current string) (string, bool) {
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(current)) {
			return model, true
		}
	}
	return "", false
}

func (h *Host) switchAllAgentModelsLocked(provider, model string) error {
	if err := h.models.Swap("default", provider, model); err != nil {
		return err
	}
	if h.cfg.Roles == nil {
		h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
	}
	h.cfg.Provider = provider
	h.cfg.ModelName = model
	h.cfg.RememberModelCandidate(provider, model)
	for _, role := range projectAgentModelRoles {
		if err := h.models.Swap(role, provider, model); err != nil {
			return err
		}
		rc := h.cfg.Roles[role]
		rc.Provider = provider
		rc.Model = model
		h.cfg.Roles[role] = rc
	}
	return nil
}

func (h *Host) recordAllProjectModelRoutesLocked(provider, model string) {
	h.recordProjectRouteLocked("default", provider, model)
	for _, role := range projectAgentModelRoles {
		h.recordProjectRouteLocked(role, provider, model)
	}
}

func (h *Host) ConfiguredProviders() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	providers := make([]string, 0, len(h.cfg.Providers))
	for name := range h.cfg.Providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers
}

func (h *Host) ConfiguredModels(provider string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.CandidateModels(provider)
}

func (h *Host) ProviderConfig(provider string) (bootstrap.ProviderConfig, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pc, ok := h.cfg.Providers[strings.TrimSpace(provider)]
	if !ok {
		return bootstrap.ProviderConfig{}, false
	}
	return cloneProviderConfig(pc), true
}

func (h *Host) ModelAutoSwitchConfig() bootstrap.ModelAutoSwitchConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneModelAutoSwitchConfig(h.cfg.ModelAutoSwitch)
}

func (h *Host) CurrentModelSelection(role string) (string, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	role = strings.ToLower(strings.TrimSpace(role))
	var provider, model string
	var explicit bool
	if strings.HasPrefix(role, "stage:") {
		provider, model, explicit = h.models.CurrentStageSelection(strings.TrimPrefix(role, "stage:"))
	} else {
		provider, model, explicit = h.models.CurrentSelection(role)
	}
	if h.cfg.PersistProjectOverlay && (role == "" || role == "default") {
		explicit = h.cfg.PersistProjectConfig != nil &&
			strings.TrimSpace(h.cfg.PersistProjectConfig.Provider) != "" &&
			strings.TrimSpace(h.cfg.PersistProjectConfig.ModelName) != ""
	}
	return provider, model, explicit
}

func (h *Host) SwitchModel(role, provider, model string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.switchModelLocked(role, provider, model)
}

func (h *Host) ClearModelRoute(role string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		return fmt.Errorf("default model route cannot inherit")
	}
	if !validModelRole(role) {
		return fmt.Errorf("unknown role %q", role)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	candidate := cloneHostRuntimeConfig(h.cfg)
	if candidate.Roles != nil {
		rc := candidate.Roles[role]
		rc.Provider = ""
		rc.Model = ""
		if roleConfigIsEmpty(rc) {
			delete(candidate.Roles, role)
		} else {
			candidate.Roles[role] = rc
		}
	}
	if err := candidate.ValidateBase(); err != nil {
		return err
	}
	if err := h.models.ApplyConfig(candidate); err != nil {
		return err
	}
	h.cfg = candidate
	h.clearProjectModelRouteOverlayLocked(role)
	h.syncProjectThinkingOverrideLocked(role)
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("save model route inheritance failed", "module", "host", "err", err)
	}
	h.applyThinkingLocked(role)
	h.refreshCoordinatorContextWindowLocked(role, h.cfg.ModelName)
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("model route now inherits project default: %s", role),
		Level:    "info",
	})
	return nil
}

func (h *Host) AddProviderModel(role, providerName string, providerConfig bootstrap.ProviderConfig, model string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	if !validModelRole(role) {
		return fmt.Errorf("unknown role %q", role)
	}

	h.mu.Lock()
	candidate, providerConfig, _, err := prepareAddedProviderModelConfig(h.cfg, role, providerName, providerConfig, model)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	probeModel, err := bootstrap.NewProviderModelWithConfig(candidate, providerName, model, providerConfig)
	if err != nil {
		return err
	}
	if err := addedModelConnectivityProbe(context.Background(), probeModel, bootstrap.ProviderConnectivityTimeout(providerConfig)); err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	candidate, providerConfig, providerWasConfigured, err := prepareAddedProviderModelConfig(h.cfg, role, providerName, providerConfig, model)
	if err != nil {
		return err
	}
	h.cfg = candidate
	if h.cfg.PersistProjectOverlay && !providerWasConfigured {
		if h.cfg.PersistProviders == nil {
			h.cfg.PersistProviders = make(map[string]bool)
		}
		h.cfg.PersistProviders[providerName] = true
	}
	h.models.RegisterProvider(providerName, providerConfig)
	if err := h.switchModelLocked(role, providerName, model); err != nil {
		return err
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("模型已添加：%s/%s", providerName, model),
		Level:    "info",
	})
	return nil
}

func (h *Host) RemoveProviderModel(providerName, model string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	candidate, err := RemoveProviderModelFromConfig(h.cfg, providerName, model)
	if err != nil {
		return err
	}
	if err := h.models.ApplyConfig(candidate); err != nil {
		return err
	}
	h.cfg = candidate
	h.removeProjectProviderModelLocked(providerName, model)
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("保存配置失败", "module", "host", "err", err)
	}
	h.applyThinkingLocked("default")
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("模型已删除：%s/%s", providerName, model),
		Level:    "info",
	})
	return nil
}

func AddProviderModelToConfig(ctx context.Context, cfg bootstrap.Config, role, providerName string, providerConfig bootstrap.ProviderConfig, model string) (bootstrap.Config, error) {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return bootstrap.Config{}, fmt.Errorf("provider and model are required")
	}
	if !validModelRole(role) {
		return bootstrap.Config{}, fmt.Errorf("unknown role %q", role)
	}
	candidate, providerConfig, _, err := prepareAddedProviderModelConfig(cfg, role, providerName, providerConfig, model)
	if err != nil {
		return bootstrap.Config{}, err
	}
	probeModel, err := bootstrap.NewProviderModelWithConfig(candidate, providerName, model, providerConfig)
	if err != nil {
		return bootstrap.Config{}, err
	}
	if err := addedModelConnectivityProbe(ctx, probeModel, bootstrap.ProviderConnectivityTimeout(providerConfig)); err != nil {
		return bootstrap.Config{}, err
	}
	return SelectProviderModelInConfig(candidate, role, providerName, model)
}

type ProviderModelUpdate struct {
	Role                    string
	OriginalProvider        string
	Provider                string
	Model                   string
	ProviderConfig          bootstrap.ProviderConfig
	NetworkMaxAttempts      int
	AutoSwitchCandidatePool bool
	SelectAfterSave         *bool
}

func (u ProviderModelUpdate) shouldSelectAfterSave() bool {
	return u.SelectAfterSave != nil && *u.SelectAfterSave
}

func (h *Host) ConfigureProviderModel(ctx context.Context, update ProviderModelUpdate) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	if ctx == nil {
		ctx = context.Background()
	}
	update.Role = strings.ToLower(strings.TrimSpace(update.Role))
	if update.Role == "" {
		update.Role = "default"
	}
	selectAfterSave := update.shouldSelectAfterSave()

	h.mu.Lock()
	base := h.cfg
	h.mu.Unlock()

	candidate, providerConfig, originalProvider, provider, model, err := prepareConfiguredProviderModelConfig(base, update)
	if err != nil {
		return err
	}
	if configuredProviderModelRequiresProbe(base, update, providerConfig, originalProvider, model) {
		probeModel, err := bootstrap.NewProviderModelWithConfig(candidate, provider, model, providerConfig)
		if err != nil {
			return err
		}
		if err := addedModelConnectivityProbe(ctx, probeModel, bootstrap.ProviderConnectivityTimeout(providerConfig)); err != nil {
			return err
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	candidate, providerConfig, originalProvider, provider, model, err = prepareConfiguredProviderModelConfig(h.cfg, update)
	if err != nil {
		return err
	}
	if h.cfg.PersistProjectOverlay {
		if h.cfg.PersistProviders == nil {
			h.cfg.PersistProviders = make(map[string]bool)
		}
		h.cfg.PersistProviders[provider] = true
		if originalProvider != "" && originalProvider != provider {
			delete(h.cfg.PersistProviders, originalProvider)
		}
		candidate.PersistProviders = h.cfg.PersistProviders
	}
	if err := h.models.ApplyConfig(candidate); err != nil {
		return err
	}
	h.cfg = candidate
	h.models.RegisterProvider(provider, providerConfig)
	if selectAfterSave {
		h.syncProjectModelOverlayLocked(update.Role, originalProvider, provider, model)
	} else {
		h.syncProjectProviderOverlayLocked(originalProvider, provider, model)
	}
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("save provider model config failed", "module", "host", "err", err)
	}
	if selectAfterSave {
		h.normalizeThinkingLocked(update.Role)
		h.applyThinkingLocked(update.Role)
		h.refreshCoordinatorContextWindowLocked(update.Role, model)
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("模型配置已更新：%s/%s", provider, model),
		Level:    "info",
	})
	return nil
}

// SyncInheritedProviderFromGlobal refreshes an already-open project host after
// the web global model registry edits or renames a provider. Project-owned
// providers keep their local credentials/config; inherited provider references
// and safe overlay metadata follow the global provider key.
func (h *Host) SyncInheritedProviderFromGlobal(globalCfg bootstrap.Config, originalProvider, provider string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	originalProvider = strings.TrimSpace(originalProvider)
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if originalProvider == "" {
		originalProvider = provider
	}
	pc, ok := globalCfg.Providers[provider]
	if !ok {
		return fmt.Errorf("provider %q is not configured", provider)
	}
	globalAutoSwitch := cloneModelAutoSwitchConfig(globalCfg.ModelAutoSwitch)

	h.mu.Lock()
	defer h.mu.Unlock()

	candidate := cloneHostRuntimeConfig(h.cfg)
	changed := false
	if originalProvider != provider {
		changed = renameProviderKeyAndReferencesInConfig(&candidate, originalProvider, provider)
		if candidate.PersistProviders != nil {
			if owned, ok := candidate.PersistProviders[originalProvider]; ok {
				delete(candidate.PersistProviders, originalProvider)
				candidate.PersistProviders[provider] = owned
				changed = true
			}
		}
	}
	if candidate.PersistProviders[provider] {
		projectProvider := candidate.Providers[provider]
		if projectProvider.Label != pc.Label {
			projectProvider.Label = pc.Label
			candidate.Providers[provider] = projectProvider
			changed = true
		}
	} else {
		if candidate.Providers == nil {
			candidate.Providers = make(map[string]bootstrap.ProviderConfig)
		}
		if !reflect.DeepEqual(candidate.Providers[provider], pc) {
			candidate.Providers[provider] = cloneProviderConfig(pc)
			changed = true
		}
	}
	if !reflect.DeepEqual(candidate.ModelAutoSwitch, globalAutoSwitch) {
		candidate.ModelAutoSwitch = globalAutoSwitch
		changed = true
	}
	if candidate.StructureRepairMaxAttempts != globalCfg.StructureRepairMaxAttempts {
		candidate.StructureRepairMaxAttempts = globalCfg.StructureRepairMaxAttempts
		changed = true
	}
	if candidate.BudgetQualityMaxAttempts != globalCfg.BudgetQualityMaxAttempts {
		candidate.BudgetQualityMaxAttempts = globalCfg.BudgetQualityMaxAttempts
		changed = true
	}
	if candidate.AdaptationOutlineAuditRetryMaxAttempts != globalCfg.AdaptationOutlineAuditRetryMaxAttempts {
		candidate.AdaptationOutlineAuditRetryMaxAttempts = globalCfg.AdaptationOutlineAuditRetryMaxAttempts
		changed = true
	}
	if candidate.PersistProjectConfig != nil {
		overlay := cloneProjectConfig(*candidate.PersistProjectConfig)
		overlayChanged := false
		if originalProvider != provider {
			overlayChanged = renameProviderKeyAndReferencesInConfig(&overlay, originalProvider, provider)
		}
		if clearProjectProviderLabel(&overlay, provider) {
			overlayChanged = true
		}
		if !reflect.DeepEqual(overlay.ModelAutoSwitch, globalAutoSwitch) {
			overlay.ModelAutoSwitch = globalAutoSwitch
			overlayChanged = true
		}
		if overlay.StructureRepairMaxAttempts != globalCfg.StructureRepairMaxAttempts {
			overlay.StructureRepairMaxAttempts = globalCfg.StructureRepairMaxAttempts
			overlayChanged = true
		}
		if overlay.BudgetQualityMaxAttempts != globalCfg.BudgetQualityMaxAttempts {
			overlay.BudgetQualityMaxAttempts = globalCfg.BudgetQualityMaxAttempts
			overlayChanged = true
		}
		if overlayChanged {
			candidate.PersistProjectConfig = &overlay
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := candidate.ValidateBase(); err != nil {
		return err
	}
	if err := h.models.ApplyConfig(candidate); err != nil {
		return err
	}
	h.cfg = candidate
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("save refreshed provider references failed", "module", "host", "err", err)
	}
	h.applyThinkingLocked("default")
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("model config refreshed: %s", provider),
		Level:    "info",
	})
	return nil
}

// SyncInheritedProviderModelRemovalFromGlobal removes a globally deleted model
// from an open project that inherits the provider. Project-owned providers keep
// their credentials and routes.
func (h *Host) SyncInheritedProviderModelRemovalFromGlobal(globalCfg bootstrap.Config, provider, model string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg.PersistProviders[provider] {
		return nil
	}

	candidate := cloneHostRuntimeConfig(h.cfg)
	removeInheritedProviderModelFromRuntime(&candidate, globalCfg, provider, model)
	if candidate.PersistProjectConfig != nil {
		overlay := cloneProjectConfig(*candidate.PersistProjectConfig)
		removeInheritedProviderModelFromOverlay(&overlay, globalCfg, provider, model)
		candidate.PersistProjectConfig = &overlay
	}
	delete(candidate.PersistProviders, provider)
	if len(candidate.PersistProviders) == 0 {
		candidate.PersistProviders = nil
	}
	if err := candidate.ValidateBase(); err != nil {
		return err
	}
	if err := h.models.ApplyConfig(candidate); err != nil {
		return err
	}
	h.cfg = candidate
	if err := h.persistConfigLocked(); err != nil {
		return err
	}
	h.applyThinkingLocked("default")
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("inherited model removed: %s/%s", provider, model),
		Level:    "info",
	})
	return nil
}

func removeInheritedProviderModelFromRuntime(cfg *bootstrap.Config, globalCfg bootstrap.Config, provider, model string) {
	if cfg.Provider == provider && cfg.ModelName == model {
		cfg.Provider = globalCfg.Provider
		cfg.ModelName = globalCfg.ModelName
	}
	removeProviderModelRoutes(cfg, provider, model)
	if globalPC, ok := globalCfg.Providers[provider]; ok {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]bootstrap.ProviderConfig)
		}
		cfg.Providers[provider] = cloneProviderConfig(globalPC)
	} else {
		delete(cfg.Providers, provider)
	}
	cfg.ModelAutoSwitch = cloneModelAutoSwitchConfig(globalCfg.ModelAutoSwitch)
	cfg.StructureRepairMaxAttempts = globalCfg.StructureRepairMaxAttempts
	cfg.BudgetQualityMaxAttempts = globalCfg.BudgetQualityMaxAttempts
	cfg.AdaptationOutlineAuditRetryMaxAttempts = globalCfg.AdaptationOutlineAuditRetryMaxAttempts
}

func removeInheritedProviderModelFromOverlay(cfg *bootstrap.Config, globalCfg bootstrap.Config, provider, model string) {
	if cfg.Provider == provider && cfg.ModelName == model {
		cfg.Provider = ""
		cfg.ModelName = ""
	}
	removeProviderModelRoutes(cfg, provider, model)
	if globalPC, ok := globalCfg.Providers[provider]; ok {
		if _, referenced := cfg.Providers[provider]; referenced {
			cfg.Providers[provider] = bootstrap.ProviderConfig{
				Label:  globalPC.Label,
				Models: append([]string(nil), globalPC.Models...),
			}
		}
	} else {
		delete(cfg.Providers, provider)
		cfg.ModelAutoSwitch.FallbackBackends = removeProviderCandidate(cfg.ModelAutoSwitch.FallbackBackends, provider)
	}
	delete(cfg.ProjectOwnedProviders, provider)
	if len(cfg.ProjectOwnedProviders) == 0 {
		cfg.ProjectOwnedProviders = nil
	}
	cfg.ModelAutoSwitch = cloneModelAutoSwitchConfig(globalCfg.ModelAutoSwitch)
	cfg.StructureRepairMaxAttempts = globalCfg.StructureRepairMaxAttempts
	cfg.BudgetQualityMaxAttempts = globalCfg.BudgetQualityMaxAttempts
	cfg.AdaptationOutlineAuditRetryMaxAttempts = globalCfg.AdaptationOutlineAuditRetryMaxAttempts
}

func removeProviderModelRoutes(cfg *bootstrap.Config, provider, model string) {
	for role, route := range cfg.Roles {
		if route.Provider == provider && route.Model == model {
			route.Provider = ""
			route.Model = ""
		}
		route.Fallbacks = removeModelRef(route.Fallbacks, provider, model)
		if roleConfigIsEmpty(route) {
			delete(cfg.Roles, role)
			continue
		}
		cfg.Roles[role] = route
	}
}

func (h *Host) SyncModelSettingsFromGlobal(globalCfg bootstrap.Config) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	h.mu.Lock()
	defer h.mu.Unlock()

	globalAutoSwitch := cloneModelAutoSwitchConfig(globalCfg.ModelAutoSwitch)
	candidate := cloneHostRuntimeConfig(h.cfg)
	changed := false
	if !reflect.DeepEqual(candidate.ModelAutoSwitch, globalAutoSwitch) {
		candidate.ModelAutoSwitch = globalAutoSwitch
		changed = true
	}
	if candidate.StructureRepairMaxAttempts != globalCfg.StructureRepairMaxAttempts {
		candidate.StructureRepairMaxAttempts = globalCfg.StructureRepairMaxAttempts
		changed = true
	}
	if candidate.BudgetQualityMaxAttempts != globalCfg.BudgetQualityMaxAttempts {
		candidate.BudgetQualityMaxAttempts = globalCfg.BudgetQualityMaxAttempts
		changed = true
	}
	if overlay := candidate.PersistProjectConfig; overlay != nil {
		nextOverlay := cloneProjectConfig(*overlay)
		overlayChanged := false
		if !reflect.DeepEqual(nextOverlay.ModelAutoSwitch, globalAutoSwitch) {
			nextOverlay.ModelAutoSwitch = globalAutoSwitch
			overlayChanged = true
		}
		if nextOverlay.StructureRepairMaxAttempts != globalCfg.StructureRepairMaxAttempts {
			nextOverlay.StructureRepairMaxAttempts = globalCfg.StructureRepairMaxAttempts
			overlayChanged = true
		}
		if nextOverlay.BudgetQualityMaxAttempts != globalCfg.BudgetQualityMaxAttempts {
			nextOverlay.BudgetQualityMaxAttempts = globalCfg.BudgetQualityMaxAttempts
			overlayChanged = true
		}
		if nextOverlay.AdaptationOutlineAuditRetryMaxAttempts != globalCfg.AdaptationOutlineAuditRetryMaxAttempts {
			nextOverlay.AdaptationOutlineAuditRetryMaxAttempts = globalCfg.AdaptationOutlineAuditRetryMaxAttempts
			overlayChanged = true
		}
		if overlayChanged {
			candidate.PersistProjectConfig = &nextOverlay
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := candidate.ValidateBase(); err != nil {
		return err
	}
	if err := h.models.ApplyConfig(candidate); err != nil {
		return err
	}
	h.cfg = candidate
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("save refreshed model settings failed", "module", "host", "err", err)
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  "global model retry settings refreshed",
		Level:    "info",
	})
	return nil
}

func ConfigureProviderModelInConfig(ctx context.Context, cfg bootstrap.Config, update ProviderModelUpdate) (bootstrap.Config, error) {
	update.Role = strings.ToLower(strings.TrimSpace(update.Role))
	if update.Role == "" {
		update.Role = "default"
	}
	candidate, providerConfig, originalProvider, provider, model, err := prepareConfiguredProviderModelConfig(cfg, update)
	if err != nil {
		return bootstrap.Config{}, err
	}
	if configuredProviderModelRequiresProbe(cfg, update, providerConfig, originalProvider, model) {
		probeModel, err := bootstrap.NewProviderModelWithConfig(candidate, provider, model, providerConfig)
		if err != nil {
			return bootstrap.Config{}, err
		}
		if err := addedModelConnectivityProbe(ctx, probeModel, bootstrap.ProviderConnectivityTimeout(providerConfig)); err != nil {
			return bootstrap.Config{}, err
		}
	}
	if !update.shouldSelectAfterSave() {
		return candidate, nil
	}
	return SelectProviderModelInConfig(candidate, update.Role, provider, model)
}

type ProviderModelTestResult struct {
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UseProxy  bool      `json:"use_proxy"`
	CheckedAt time.Time `json:"checked_at"`
}

type ProviderModelDiscoveryResult struct {
	Provider  string    `json:"provider"`
	Models    []string  `json:"models"`
	Supported bool      `json:"supported"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	UseProxy  bool      `json:"use_proxy"`
	CheckedAt time.Time `json:"checked_at"`
}

func (h *Host) TestProviderModel(ctx context.Context, role, providerName string, providerConfig bootstrap.ProviderConfig, model string) (ProviderModelTestResult, error) {
	h.mu.Lock()
	cfg := cloneProjectConfig(h.cfg)
	h.mu.Unlock()
	return TestProviderModelInConfig(ctx, cfg, role, providerName, providerConfig, model)
}

func (h *Host) TestConfiguredProviderModel(ctx context.Context, update ProviderModelUpdate) (ProviderModelTestResult, error) {
	h.mu.Lock()
	cfg := cloneProjectConfig(h.cfg)
	h.mu.Unlock()
	return TestConfiguredProviderModelInConfig(ctx, cfg, update)
}

func TestProviderModelInConfig(ctx context.Context, cfg bootstrap.Config, role, providerName string, providerConfig bootstrap.ProviderConfig, model string) (ProviderModelTestResult, error) {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	result := ProviderModelTestResult{
		Provider:  providerName,
		Model:     model,
		Status:    "error",
		CheckedAt: time.Now(),
	}
	if providerName == "" || model == "" {
		err := fmt.Errorf("provider and model are required")
		result.Message = err.Error()
		return result, err
	}
	if !validModelRole(role) {
		err := fmt.Errorf("unknown role %q", role)
		result.Message = err.Error()
		return result, err
	}
	candidate, providerConfig, _, err := prepareAddedProviderModelConfig(cfg, role, providerName, providerConfig, model)
	result.UseProxy = bootstrap.ProviderUsesProxy(providerName, model, providerConfig)
	if err != nil {
		result.Message = err.Error()
		return result, err
	}
	probeModel, err := bootstrap.NewProviderModelWithConfig(candidate, providerName, model, providerConfig)
	if err != nil {
		result.Message = err.Error()
		return result, err
	}
	if err := addedModelConnectivityProbe(ctx, probeModel, bootstrap.ProviderConnectivityTimeout(providerConfig)); err != nil {
		result.Message = err.Error()
		return result, err
	}
	result.Status = "ok"
	result.Message = "model test passed"
	return result, nil
}

func TestConfiguredProviderModelInConfig(ctx context.Context, cfg bootstrap.Config, update ProviderModelUpdate) (ProviderModelTestResult, error) {
	providerName := strings.TrimSpace(update.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(update.OriginalProvider)
	}
	model := strings.TrimSpace(update.Model)
	update.Provider = providerName
	update.Model = model
	result := ProviderModelTestResult{
		Provider:  providerName,
		Model:     model,
		Status:    "error",
		CheckedAt: time.Now(),
	}
	candidate, providerConfig, _, providerName, model, err := prepareConfiguredProviderModelConfig(cfg, update)
	result.Provider = providerName
	result.Model = model
	result.UseProxy = bootstrap.ProviderUsesProxy(providerName, model, providerConfig)
	if err != nil {
		result.Message = err.Error()
		return result, err
	}
	probeModel, err := bootstrap.NewProviderModelWithConfig(candidate, providerName, model, providerConfig)
	if err != nil {
		result.Message = err.Error()
		return result, err
	}
	if err := addedModelConnectivityProbe(ctx, probeModel, bootstrap.ProviderConnectivityTimeout(providerConfig)); err != nil {
		result.Message = err.Error()
		return result, err
	}
	result.Status = "ok"
	result.Message = "model test passed"
	return result, nil
}

func (h *Host) DiscoverProviderModels(ctx context.Context, providerName string, providerConfig bootstrap.ProviderConfig, model string) (ProviderModelDiscoveryResult, error) {
	h.mu.Lock()
	cfg := cloneProjectConfig(h.cfg)
	h.mu.Unlock()
	return DiscoverProviderModelsInConfig(ctx, cfg, providerName, providerConfig, model)
}

func (h *Host) DiscoverConfiguredProviderModels(ctx context.Context, update ProviderModelUpdate) (ProviderModelDiscoveryResult, error) {
	h.mu.Lock()
	cfg := cloneProjectConfig(h.cfg)
	h.mu.Unlock()
	return DiscoverConfiguredProviderModelsInConfig(ctx, cfg, update)
}

func DiscoverProviderModelsInConfig(ctx context.Context, cfg bootstrap.Config, providerName string, providerConfig bootstrap.ProviderConfig, model string) (ProviderModelDiscoveryResult, error) {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	result := ProviderModelDiscoveryResult{
		Provider:  providerName,
		Supported: false,
		Status:    "error",
		CheckedAt: time.Now(),
	}
	if providerName == "" {
		err := fmt.Errorf("provider is required")
		result.Message = err.Error()
		return result, err
	}
	if model == "" {
		if candidates := cfg.CandidateModels(providerName); len(candidates) > 0 {
			model = candidates[0]
		} else {
			model = cfg.ModelName
		}
	}
	candidate, providerConfig, _, err := prepareAddedProviderModelConfig(cfg, "default", providerName, providerConfig, model)
	result.UseProxy = bootstrap.ProviderUsesProxy(providerName, model, providerConfig)
	if err != nil {
		result.Models = fallbackDiscoveryModels(cfg, providerName, model)
		result.Message = err.Error()
		return result, err
	}
	models, supported, err := bootstrap.DiscoverProviderModels(ctx, candidate, providerName, model, providerConfig)
	result.Models = mergeDiscoveryModels(models, fallbackDiscoveryModels(candidate, providerName, model))
	result.Supported = supported
	if err != nil {
		result.Message = err.Error()
		if !supported {
			result.Status = "fallback"
			return result, nil
		}
		return result, err
	}
	if !supported {
		result.Status = "fallback"
		result.Message = "provider does not support live model discovery"
		return result, nil
	}
	result.Status = "ok"
	result.Message = "model discovery completed"
	return result, nil
}

func DiscoverConfiguredProviderModelsInConfig(ctx context.Context, cfg bootstrap.Config, update ProviderModelUpdate) (ProviderModelDiscoveryResult, error) {
	providerName := strings.TrimSpace(update.Provider)
	model := strings.TrimSpace(update.Model)
	result := ProviderModelDiscoveryResult{
		Provider:  providerName,
		Supported: false,
		Status:    "error",
		CheckedAt: time.Now(),
	}
	if providerName == "" {
		providerName = strings.TrimSpace(update.OriginalProvider)
	}
	update.Provider = providerName
	if model == "" {
		fallbackProvider := strings.TrimSpace(update.OriginalProvider)
		if fallbackProvider == "" {
			fallbackProvider = providerName
		}
		if candidates := cfg.CandidateModels(fallbackProvider); len(candidates) > 0 {
			model = candidates[0]
		} else {
			model = cfg.ModelName
		}
		update.Model = model
	}
	update.Model = model
	candidate, providerConfig, _, providerName, model, err := prepareConfiguredProviderModelConfig(cfg, update)
	result.Provider = providerName
	result.UseProxy = bootstrap.ProviderUsesProxy(providerName, model, providerConfig)
	if err != nil {
		result.Models = fallbackDiscoveryModels(cfg, providerName, model)
		result.Message = err.Error()
		return result, err
	}
	models, supported, err := bootstrap.DiscoverProviderModels(ctx, candidate, providerName, model, providerConfig)
	result.Models = mergeDiscoveryModels(models, fallbackDiscoveryModels(candidate, providerName, model))
	result.Supported = supported
	if err != nil {
		result.Message = err.Error()
		if !supported {
			result.Status = "fallback"
			return result, nil
		}
		return result, err
	}
	if !supported {
		result.Status = "fallback"
		result.Message = "provider does not support live model discovery"
		return result, nil
	}
	result.Status = "ok"
	result.Message = "model discovery completed"
	return result, nil
}

func RemoveProviderModelFromConfig(cfg bootstrap.Config, providerName, model string) (bootstrap.Config, error) {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return bootstrap.Config{}, fmt.Errorf("provider and model are required")
	}
	if _, ok := cfg.Providers[providerName]; !ok {
		return bootstrap.Config{}, fmt.Errorf("provider %q is not configured", providerName)
	}
	if cfg.Provider == providerName && cfg.ModelName == model {
		return bootstrap.Config{}, fmt.Errorf("cannot delete the current default model; switch default model first")
	}
	if !configHasProviderModel(cfg, providerName, model) {
		return bootstrap.Config{}, fmt.Errorf("model %q is not configured for provider %q", model, providerName)
	}

	candidate := cfg
	candidate.Providers = cloneProviderConfigs(cfg.Providers)
	if cfg.Roles != nil {
		candidate.Roles = make(map[string]bootstrap.RoleConfig, len(cfg.Roles))
		for name, rc := range cfg.Roles {
			rc.Fallbacks = append([]bootstrap.ModelRef(nil), rc.Fallbacks...)
			candidate.Roles[name] = rc
		}
	}

	pc := candidate.Providers[providerName]
	pc.Models = removeModelCandidate(pc.Models, model)
	delete(pc.ModelReasoningEfforts, model)
	candidate.Providers[providerName] = pc
	for role, rc := range candidate.Roles {
		if rc.Provider == providerName && rc.Model == model {
			rc.Provider = ""
			rc.Model = ""
		}
		rc.Fallbacks = removeModelRef(rc.Fallbacks, providerName, model)
		if roleConfigIsEmpty(rc) {
			delete(candidate.Roles, role)
			continue
		}
		candidate.Roles[role] = rc
	}
	if len(candidate.CandidateModels(providerName)) == 0 {
		delete(candidate.Providers, providerName)
		candidate.ModelAutoSwitch.FallbackBackends = removeProviderCandidate(candidate.ModelAutoSwitch.FallbackBackends, providerName)
	}
	if err := candidate.ValidateBase(); err != nil {
		return bootstrap.Config{}, err
	}
	return candidate, nil
}

func SelectProviderModelInConfig(cfg bootstrap.Config, role, provider, model string) (bootstrap.Config, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return bootstrap.Config{}, fmt.Errorf("provider and model are required")
	}
	if !validModelRole(role) {
		return bootstrap.Config{}, fmt.Errorf("unknown role %q", role)
	}
	if _, ok := cfg.Providers[provider]; !ok {
		return bootstrap.Config{}, fmt.Errorf("provider %q is not configured", provider)
	}
	candidate := cfg
	candidate.Providers = cloneProviderConfigs(cfg.Providers)
	if cfg.Roles != nil {
		candidate.Roles = make(map[string]bootstrap.RoleConfig, len(cfg.Roles))
		for name, rc := range cfg.Roles {
			rc.Fallbacks = append([]bootstrap.ModelRef(nil), rc.Fallbacks...)
			candidate.Roles[name] = rc
		}
	}
	candidate.RememberModelCandidate(provider, model)
	if role == "" || role == "default" {
		candidate.Provider = provider
		candidate.ModelName = model
	} else {
		if candidate.Roles == nil {
			candidate.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := candidate.Roles[role]
		rc.Provider = provider
		rc.Model = model
		candidate.Roles[role] = rc
	}
	if err := candidate.ValidateBase(); err != nil {
		return bootstrap.Config{}, err
	}
	return candidate, nil
}

func prepareAddedProviderModelConfig(cfg bootstrap.Config, role, providerName string, providerConfig bootstrap.ProviderConfig, model string) (bootstrap.Config, bootstrap.ProviderConfig, bool, error) {
	candidate := cfg
	candidate.Providers = cloneProviderConfigs(cfg.Providers)
	providerConfig = normalizeProviderConfigForSave(providerConfig)
	_, providerWasConfigured := cfg.Providers[providerName]
	if existing, ok := cfg.Providers[providerName]; ok {
		existing = normalizeProviderConfigForSave(existing)
		if !providerConfigCanAddModel(existing, providerConfig) {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, fmt.Errorf("provider %q already exists; use the existing provider flow to add models", providerName)
		}
		if providerConfig.ModelReasoningEfforts != nil {
			if existing.ModelReasoningEfforts == nil {
				existing.ModelReasoningEfforts = make(map[string]string)
			}
			existing.ModelReasoningEfforts[model] = providerConfig.ModelReasoningEfforts[model]
		}
		providerConfig = existing
	} else {
		if _, err := providerConfig.ProviderType(providerName); err != nil {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, err
		}
	}
	candidate.Providers[providerName] = providerConfig
	candidate.RememberModelCandidate(providerName, model)
	providerConfig = candidate.Providers[providerName]
	if err := validateAddedProviderModel(candidate, role, providerName, providerConfig, model); err != nil {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, false, err
	}
	return candidate, providerConfig, providerWasConfigured, nil
}

func prepareConfiguredProviderModelConfig(cfg bootstrap.Config, update ProviderModelUpdate) (bootstrap.Config, bootstrap.ProviderConfig, string, string, string, error) {
	role := strings.ToLower(strings.TrimSpace(update.Role))
	if role == "" {
		role = "default"
	}
	provider := strings.TrimSpace(update.Provider)
	originalProvider := strings.TrimSpace(update.OriginalProvider)
	model := strings.TrimSpace(update.Model)
	if originalProvider == "" {
		originalProvider = provider
	}
	if provider == "" || model == "" {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, "", "", "", fmt.Errorf("provider and model are required")
	}
	if !validModelRole(role) {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, "", "", "", fmt.Errorf("unknown role %q", role)
	}

	candidate := cfg
	candidate.Providers = cloneProviderConfigs(cfg.Providers)
	candidate.Roles = cloneRoleConfigs(cfg.Roles)
	candidate.ModelAutoSwitch = cloneModelAutoSwitchConfig(cfg.ModelAutoSwitch)
	if candidate.Providers == nil {
		candidate.Providers = make(map[string]bootstrap.ProviderConfig)
	}

	existing, providerWasConfigured := candidate.Providers[originalProvider]
	update.ProviderConfig = normalizeProviderConfigForSave(update.ProviderConfig)
	editingExisting := strings.TrimSpace(update.OriginalProvider) != ""
	if providerWasConfigured {
		existing = normalizeProviderConfigForSave(existing)
		if editingExisting {
			existing = mergeEditedProviderConfig(existing, update.ProviderConfig, model)
		} else {
			if !providerConfigCanAddModel(existing, update.ProviderConfig) {
				return bootstrap.Config{}, bootstrap.ProviderConfig{}, "", "", "", fmt.Errorf("provider %q already exists; use the existing provider flow to edit it", provider)
			}
			existing.Models = appendUniqueString(existing.Models, model)
		}
	} else {
		existing = normalizeProviderConfigForSave(update.ProviderConfig)
		existing.Models = appendUniqueString(existing.Models, model)
		if _, err := existing.ProviderType(provider); err != nil {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, "", "", "", err
		}
	}

	if provider != originalProvider {
		if _, ok := candidate.Providers[provider]; ok {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, "", "", "", fmt.Errorf("provider %q already exists", provider)
		}
		delete(candidate.Providers, originalProvider)
		renameProviderReferencesInConfig(&candidate, originalProvider, provider)
	}
	candidate.Providers[provider] = existing
	candidate.RememberModelCandidate(provider, model)
	if err := applyModelAutoSwitchUpdate(&candidate, provider, update.NetworkMaxAttempts, update.AutoSwitchCandidatePool); err != nil {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, "", "", "", err
	}

	if !update.shouldSelectAfterSave() {
		if err := candidate.ValidateBase(); err != nil {
			return bootstrap.Config{}, bootstrap.ProviderConfig{}, "", "", "", err
		}
		return candidate, candidate.Providers[provider], originalProvider, provider, model, nil
	}

	selected, err := SelectProviderModelInConfig(candidate, role, provider, model)
	if err != nil {
		return bootstrap.Config{}, bootstrap.ProviderConfig{}, "", "", "", err
	}
	return selected, selected.Providers[provider], originalProvider, provider, model, nil
}

func mergeEditedProviderConfig(existing, incoming bootstrap.ProviderConfig, model string) bootstrap.ProviderConfig {
	apiKey := strings.TrimSpace(incoming.APIKey)
	models := append([]string(nil), existing.Models...)
	if len(incoming.Models) > 0 {
		models = append([]string(nil), incoming.Models...)
	}
	merged := existing
	merged.Label = strings.TrimSpace(incoming.Label)
	merged.TemplateProvider = strings.TrimSpace(incoming.TemplateProvider)
	merged.Disabled = incoming.Disabled
	if incoming.UseProxy != nil {
		useProxy := *incoming.UseProxy
		merged.UseProxy = &useProxy
	}
	merged.RequestTimeoutSeconds = incoming.RequestTimeoutSeconds
	merged.ConnectivityTimeoutSeconds = incoming.ConnectivityTimeoutSeconds
	merged.Type = strings.TrimSpace(incoming.Type)
	merged.Auth = strings.TrimSpace(incoming.Auth)
	merged.AccountID = strings.TrimSpace(incoming.AccountID)
	authFile := strings.TrimSpace(incoming.AuthFile)
	if merged.UsesCodexAuth() && authFile == "" && existing.UsesCodexAuth() {
		merged.AuthFile = strings.TrimSpace(existing.AuthFile)
	} else {
		merged.AuthFile = authFile
	}
	merged.API = strings.TrimSpace(incoming.API)
	merged.BaseURL = strings.TrimSpace(incoming.BaseURL)
	if merged.UsesGrokOAuth() || merged.UsesCodexAuth() {
		merged.API = ""
		if merged.UsesCodexAuth() {
			merged.API = "responses"
		}
		merged.APIKey = ""
	} else if apiKey != "" {
		merged.APIKey = apiKey
	}
	merged.Models = appendUniqueString(models, model)
	if incoming.ModelReasoningEfforts != nil {
		if merged.ModelReasoningEfforts == nil {
			merged.ModelReasoningEfforts = make(map[string]string)
		}
		if effort := strings.TrimSpace(incoming.ModelReasoningEfforts[model]); effort != "" {
			merged.ModelReasoningEfforts[model] = effort
		} else {
			delete(merged.ModelReasoningEfforts, model)
		}
	}
	return merged
}

func normalizeProviderConfigForSave(pc bootstrap.ProviderConfig) bootstrap.ProviderConfig {
	pc.AuthFile = strings.TrimSpace(pc.AuthFile)
	if pc.UsesGrokOAuth() || pc.UsesCodexAuth() {
		pc.API = ""
		if pc.UsesCodexAuth() {
			pc.API = "responses"
		}
		pc.APIKey = ""
	}
	for model, effort := range pc.ModelReasoningEfforts {
		trimmedModel := strings.TrimSpace(model)
		trimmedEffort := strings.ToLower(strings.TrimSpace(effort))
		if trimmedModel == "" || trimmedEffort == "" {
			delete(pc.ModelReasoningEfforts, model)
			continue
		}
		if trimmedModel != model {
			delete(pc.ModelReasoningEfforts, model)
		}
		pc.ModelReasoningEfforts[trimmedModel] = trimmedEffort
	}
	if len(pc.ModelReasoningEfforts) == 0 {
		pc.ModelReasoningEfforts = nil
	}
	return pc
}

func configuredProviderModelRequiresProbe(cfg bootstrap.Config, update ProviderModelUpdate, providerConfig bootstrap.ProviderConfig, originalProvider, model string) bool {
	originalProvider = strings.TrimSpace(originalProvider)
	providerConfig = normalizeProviderConfigForSave(providerConfig)
	incomingConfig := normalizeProviderConfigForSave(update.ProviderConfig)
	if originalProvider == "" {
		originalProvider = strings.TrimSpace(update.OriginalProvider)
	}
	if originalProvider == "" {
		return true
	}
	existing, ok := cfg.Providers[originalProvider]
	if !ok {
		return true
	}
	existing = normalizeProviderConfigForSave(existing)
	if !configHasProviderModel(cfg, originalProvider, strings.TrimSpace(model)) {
		return true
	}
	if strings.TrimSpace(incomingConfig.APIKey) != "" {
		return true
	}
	return strings.TrimSpace(existing.Type) != strings.TrimSpace(providerConfig.Type) ||
		strings.TrimSpace(existing.Auth) != strings.TrimSpace(providerConfig.Auth) ||
		strings.TrimSpace(existing.AccountID) != strings.TrimSpace(providerConfig.AccountID) ||
		strings.TrimSpace(existing.AuthFile) != strings.TrimSpace(providerConfig.AuthFile) ||
		strings.TrimSpace(existing.API) != strings.TrimSpace(providerConfig.API) ||
		strings.TrimSpace(existing.BaseURL) != strings.TrimSpace(providerConfig.BaseURL)
}

func applyModelAutoSwitchUpdate(cfg *bootstrap.Config, provider string, attempts int, include bool) error {
	if attempts > 0 {
		normalized, err := bootstrap.NormalizeRuntimeNetworkMaxAttempts(attempts)
		if err != nil {
			return err
		}
		cfg.ModelAutoSwitch.NetworkMaxAttempts = normalized
	}
	cfg.ModelAutoSwitch.FallbackBackends = removeProviderCandidate(cfg.ModelAutoSwitch.FallbackBackends, provider)
	if include {
		cfg.ModelAutoSwitch.FallbackBackends = appendUniqueString(cfg.ModelAutoSwitch.FallbackBackends, provider)
		enabled := true
		cfg.ModelAutoSwitch.Enabled = &enabled
	} else if len(cfg.ModelAutoSwitch.FallbackBackends) == 0 {
		enabled := false
		cfg.ModelAutoSwitch.Enabled = &enabled
	}
	return nil
}

func renameProviderReferencesInConfig(cfg *bootstrap.Config, from, to string) {
	if cfg.Provider == from {
		cfg.Provider = to
	}
	if cfg.Roles != nil {
		for role, rc := range cfg.Roles {
			if rc.Provider == from {
				rc.Provider = to
			}
			for i := range rc.Fallbacks {
				if rc.Fallbacks[i].Provider == from {
					rc.Fallbacks[i].Provider = to
				}
			}
			cfg.Roles[role] = rc
		}
	}
	for i, provider := range cfg.ModelAutoSwitch.FallbackBackends {
		if strings.TrimSpace(provider) == from {
			cfg.ModelAutoSwitch.FallbackBackends[i] = to
		}
	}
}

// RenameProviderInConfig returns a copy of cfg with provider map keys and all
// provider references renamed. It is intentionally route-only: model choices
// remain project-specific unless the caller separately changes them.
func RenameProviderInConfig(cfg bootstrap.Config, from, to string) (bootstrap.Config, bool) {
	candidate := cloneProjectConfig(cfg)
	return candidate, renameProviderKeyAndReferencesInConfig(&candidate, from, to)
}

func renameProviderKeyAndReferencesInConfig(cfg *bootstrap.Config, from, to string) bool {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" || from == to {
		return false
	}
	changed := false
	if cfg.Provider == from {
		cfg.Provider = to
		changed = true
	}
	if cfg.Roles != nil {
		for role, rc := range cfg.Roles {
			roleChanged := false
			if rc.Provider == from {
				rc.Provider = to
				roleChanged = true
			}
			for i := range rc.Fallbacks {
				if rc.Fallbacks[i].Provider == from {
					rc.Fallbacks[i].Provider = to
					roleChanged = true
				}
			}
			if roleChanged {
				cfg.Roles[role] = rc
				changed = true
			}
		}
	}
	for i, value := range cfg.ModelAutoSwitch.FallbackBackends {
		if strings.TrimSpace(value) == from {
			cfg.ModelAutoSwitch.FallbackBackends[i] = to
			changed = true
		}
	}
	if cfg.Providers != nil {
		if pc, ok := cfg.Providers[from]; ok {
			delete(cfg.Providers, from)
			if existing, exists := cfg.Providers[to]; exists {
				pc = mergeProviderConfigForRename(existing, pc)
			}
			cfg.Providers[to] = pc
			changed = true
		}
	}
	return changed
}

func mergeProviderConfigForRename(existing, incoming bootstrap.ProviderConfig) bootstrap.ProviderConfig {
	out := existing
	if !providerConfigHasPrivateConfig(existing) && providerConfigHasPrivateConfig(incoming) {
		out = incoming
	}
	for _, model := range incoming.Models {
		out.Models = appendUniqueString(out.Models, model)
	}
	return out
}

func clearProjectProviderLabel(cfg *bootstrap.Config, provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" || cfg.Providers == nil {
		return false
	}
	pc, ok := cfg.Providers[provider]
	if !ok || pc.Label == "" {
		return false
	}
	pc.Label = ""
	cfg.Providers[provider] = pc
	return true
}

func setAllAgentModelRoutesInConfig(cfg *bootstrap.Config, provider, model string) {
	cfg.Provider = provider
	cfg.ModelName = model
	if cfg.Roles == nil {
		cfg.Roles = make(map[string]bootstrap.RoleConfig)
	}
	for _, role := range projectAgentModelRoles {
		rc := cfg.Roles[role]
		rc.Provider = provider
		rc.Model = model
		cfg.Roles[role] = rc
	}
}

func (h *Host) StartGrokLogin(accountID, accountName string) (grokauth.LoginStart, error) {
	return grokauth.StartLogin(accountID, accountName)
}

func (h *Host) PollGrokLogin() (grokauth.LoginPoll, error) {
	return grokauth.PollLogin(context.Background())
}

func (h *Host) CompleteGrokLogin(callbackInput string) (grokauth.AuthStatus, error) {
	return grokauth.CompleteLogin(context.Background(), callbackInput)
}

func (h *Host) GrokLoginStatus(accountID string) grokauth.AuthStatus {
	return grokauth.GetStatus(accountID)
}

func (h *Host) switchModelLocked(role, provider, model string) error {
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	previousProvider, previousModel, _ := h.models.CurrentSelection(role)
	h.cfg.RememberModelCandidate(previousProvider, previousModel)
	h.cfg.RememberModelCandidate(provider, model)
	if role == "" || role == "default" {
		if err := h.models.Swap("default", provider, model); err != nil {
			return err
		}
		h.cfg.Provider = provider
		h.cfg.ModelName = model
	} else {
		if err := h.models.Swap(role, provider, model); err != nil {
			return err
		}
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.Provider = provider
		rc.Model = model
		h.cfg.Roles[role] = rc
	}
	h.normalizeThinkingLocked(role)
	if role == "" || role == "default" {
		h.recordProjectRouteLocked("default", provider, model)
	} else {
		h.recordProjectRouteLocked(role, provider, model)
	}
	h.syncProjectThinkingOverrideLocked(role)
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("保存配置失败", "module", "host", "err", err)
	}
	h.models.ApplyReasoningConfig(h.cfg)
	h.applyThinkingLocked(role)
	// 切到未登记模型时打一行 warn，提示用户走了 128k 兜底——长篇容易被提前压缩。
	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	window, source := h.cfg.ResolveContextWindow(model)
	bootstrap.LogContextWindowChoice(logRole, model, window, source)

	// 切到 default/coordinator 时，联动 coordinator engine 的窗口与 reserve。
	// writer/architect/editor 走 ContextManagerFactory 自动按新模型重建，不需要联动。
	// 不联动会导致：1M→128k 切换时 coordinator engine 仍按 1M 算 threshold，
	// 累积 messages 超过 128k 就 API 报错；128k→1M 时阈值被钉在 96k，浪费长上下文。
	//
	// 关键：必须用 models.CurrentSelection("coordinator") 拿"coordinator 实际使用"的模型
	// 算窗口——而不是直接用切换目标的 model。当用户配了 roles.coordinator 单独模型时，
	// 切 default 不影响 coordinator 实际模型；用切换目标的窗口去 SetContextWindow 会错
	// 把 coordinator 阈值调到不相干的值（例：default 切 1M 模型时把 200k 的 coordinator
	// engine 阈值拉到 891k，写超 200k 直接爆 API）。
	if h.coordinator != nil && h.coordinatorCtxMgr != nil && (role == "" || role == "default" || role == "coordinator") {
		_, coordinatorModel, _ := h.models.CurrentSelection("coordinator")
		coordinatorWindow, coordSource := h.cfg.ResolveContextWindow(coordinatorModel)
		h.coordinator.SetContextWindow(coordinatorWindow)
		h.coordinatorCtxMgr.SetContextWindow(coordinatorWindow)
		h.coordinatorCtxMgr.SetReserveTokens(bootstrap.CompactReserveTokens(coordinatorWindow))
		// coordinator 实际模型与切换目标不同（用户切 default 但 coordinator 有专属 role）时，
		// 上面 LogContextWindowChoice 打的是 default 的窗口，与实际生效值不一致；补一行。
		if coordinatorModel != model {
			bootstrap.LogContextWindowChoice("coordinator", coordinatorModel, coordinatorWindow, coordSource)
		}
	}

	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("模型已切换：%s → %s/%s", role, provider, model),
		Level:    "info",
	})
	return nil
}

func (h *Host) refreshCoordinatorContextWindowLocked(role, model string) {
	if h.coordinator == nil || h.coordinatorCtxMgr == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "" && role != "default" && role != "coordinator" {
		return
	}
	_, coordinatorModel, _ := h.models.CurrentSelection("coordinator")
	if coordinatorModel == "" {
		coordinatorModel = model
	}
	window, _ := h.cfg.ResolveContextWindow(coordinatorModel)
	h.coordinator.SetContextWindow(window)
	h.coordinatorCtxMgr.SetContextWindow(window)
	h.coordinatorCtxMgr.SetReserveTokens(bootstrap.CompactReserveTokens(window))
}

func providerConfigCanAddModel(existing, incoming bootstrap.ProviderConfig) bool {
	if providerConfigIsEmpty(incoming) {
		return true
	}
	existing.Models = nil
	incoming.Models = nil
	existing.ModelReasoningEfforts = nil
	incoming.ModelReasoningEfforts = nil
	return reflect.DeepEqual(existing, incoming)
}

func providerConfigIsEmpty(pc bootstrap.ProviderConfig) bool {
	return pc.Type == "" &&
		pc.Label == "" &&
		pc.TemplateProvider == "" &&
		!pc.Disabled &&
		pc.UseProxy == nil &&
		pc.RequestTimeoutSeconds == 0 &&
		pc.ConnectivityTimeoutSeconds == 0 &&
		pc.Auth == "" &&
		pc.AccountID == "" &&
		pc.AuthFile == "" &&
		pc.API == "" &&
		pc.APIKey == "" &&
		pc.BaseURL == "" &&
		len(pc.Models) == 0 &&
		len(pc.ModelReasoningEfforts) == 0 &&
		len(pc.ExtraBody) == 0 &&
		len(pc.Extra) == 0
}

func fallbackDiscoveryModels(cfg bootstrap.Config, provider, model string) []string {
	return mergeDiscoveryModels(nil, append(cfg.CandidateModels(provider), strings.TrimSpace(model)))
}

func mergeDiscoveryModels(primary, fallback []string) []string {
	seen := make(map[string]bool, len(primary)+len(fallback))
	models := make([]string, 0, len(primary)+len(fallback))
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		models = append(models, model)
	}
	for _, model := range primary {
		add(model)
	}
	for _, model := range fallback {
		add(model)
	}
	return models
}

func (h *Host) persistConfigLocked() error {
	path := strings.TrimSpace(h.cfg.PersistPath)
	if path == "" {
		path = bootstrap.DefaultConfigPath()
	}
	if path == "" {
		return nil
	}
	if h.cfg.PersistProjectOverlay {
		return bootstrap.SaveConfig(path, h.projectOverlayConfigLocked())
	}
	return bootstrap.SaveConfig(path, h.cfg)
}

func (h *Host) projectOverlayConfigLocked() bootstrap.Config {
	overlay := bootstrap.Config{}
	if h.cfg.PersistProjectConfig != nil {
		overlay = cloneProjectConfig(*h.cfg.PersistProjectConfig)
	}
	overlay.ProjectOwnedProviders = cloneBoolMap(h.cfg.PersistProviders)
	overlay.Providers = h.projectOverlayProvidersLocked(overlay)
	return overlay
}

func (h *Host) ensureProjectOverlayLocked() *bootstrap.Config {
	if !h.cfg.PersistProjectOverlay {
		return nil
	}
	if h.cfg.PersistProjectConfig == nil {
		h.cfg.PersistProjectConfig = &bootstrap.Config{}
	}
	return h.cfg.PersistProjectConfig
}

func (h *Host) syncProjectModelOverlayLocked(role, originalProvider, provider, model string) {
	overlay := h.ensureProjectOverlayLocked()
	if overlay == nil {
		return
	}
	if originalProvider != "" && originalProvider != provider {
		delete(overlay.Providers, originalProvider)
		renameProviderReferencesInConfig(overlay, originalProvider, provider)
	}
	overlay.ModelAutoSwitch = cloneModelAutoSwitchConfig(h.cfg.ModelAutoSwitch)
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		h.recordProjectRouteLocked("default", provider, model)
		return
	}
	h.recordProjectRouteLocked(role, provider, model)
}

func (h *Host) syncProjectProviderOverlayLocked(originalProvider, provider, model string) {
	overlay := h.ensureProjectOverlayLocked()
	if overlay == nil {
		return
	}
	if originalProvider != "" && originalProvider != provider {
		delete(overlay.Providers, originalProvider)
		renameProviderReferencesInConfig(overlay, originalProvider, provider)
	}
	overlay.ModelAutoSwitch = cloneModelAutoSwitchConfig(h.cfg.ModelAutoSwitch)
	recordProjectProviderModel(overlay, provider, model)
}

func (h *Host) recordProjectRouteLocked(role, provider, model string) {
	overlay := h.ensureProjectOverlayLocked()
	if overlay == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		overlay.Provider = provider
		overlay.ModelName = model
	} else {
		if overlay.Roles == nil {
			overlay.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := overlay.Roles[role]
		rc.Provider = provider
		rc.Model = model
		overlay.Roles[role] = rc
	}
	recordProjectProviderModel(overlay, provider, model)
}

func (h *Host) clearProjectModelRouteOverlayLocked(role string) {
	overlay := h.ensureProjectOverlayLocked()
	if overlay == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" || overlay.Roles == nil {
		return
	}
	rc := overlay.Roles[role]
	rc.Provider = ""
	rc.Model = ""
	if roleConfigIsEmpty(rc) {
		delete(overlay.Roles, role)
		if len(overlay.Roles) == 0 {
			overlay.Roles = nil
		}
		return
	}
	overlay.Roles[role] = rc
}

func (h *Host) recordProjectThinkingLocked(role, level string) {
	overlay := h.ensureProjectOverlayLocked()
	if overlay == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		overlay.ReasoningEffort = level
		return
	}
	if overlay.Roles == nil {
		overlay.Roles = make(map[string]bootstrap.RoleConfig)
	}
	rc := overlay.Roles[role]
	rc.ReasoningEffort = level
	if roleConfigIsEmpty(rc) {
		delete(overlay.Roles, role)
		return
	}
	overlay.Roles[role] = rc
}

func (h *Host) syncProjectThinkingOverrideLocked(role string) {
	overlay := h.ensureProjectOverlayLocked()
	if overlay == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		if overlay.ReasoningEffort != "" {
			overlay.ReasoningEffort = h.cfg.ReasoningEffort
		}
		return
	}
	if overlay.Roles == nil {
		return
	}
	rc, ok := overlay.Roles[role]
	if !ok || rc.ReasoningEffort == "" {
		return
	}
	rc.ReasoningEffort = h.cfg.Roles[role].ReasoningEffort
	overlay.Roles[role] = rc
}

func (h *Host) removeProjectProviderModelLocked(provider, model string) {
	overlay := h.ensureProjectOverlayLocked()
	if overlay == nil {
		return
	}
	if overlay.Provider == provider && overlay.ModelName == model {
		overlay.Provider = ""
		overlay.ModelName = ""
	}
	for role, rc := range overlay.Roles {
		if rc.Provider == provider && rc.Model == model {
			rc.Provider = ""
			rc.Model = ""
		}
		rc.Fallbacks = removeModelRef(rc.Fallbacks, provider, model)
		if roleConfigIsEmpty(rc) {
			delete(overlay.Roles, role)
			continue
		}
		overlay.Roles[role] = rc
	}
	if pc, ok := overlay.Providers[provider]; ok {
		pc.Models = removeModelCandidate(pc.Models, model)
		delete(pc.ModelReasoningEfforts, model)
		if len(h.cfg.CandidateModels(provider)) == 0 {
			delete(overlay.Providers, provider)
		} else {
			overlay.Providers[provider] = pc
		}
	}
	if len(overlay.Providers) == 0 {
		overlay.Providers = nil
	}
	if len(overlay.Roles) == 0 {
		overlay.Roles = nil
	}
	if h.cfg.PersistProviders != nil && len(h.cfg.CandidateModels(provider)) == 0 {
		delete(h.cfg.PersistProviders, provider)
	}
}

func (h *Host) projectOverlayProvidersLocked(overlay bootstrap.Config) map[string]bootstrap.ProviderConfig {
	providers := make(map[string]bootstrap.ProviderConfig)
	for name, pc := range overlay.Providers {
		if h.cfg.PersistProviders[name] || providerConfigHasPrivateConfig(pc) {
			if current, ok := h.cfg.Providers[name]; ok {
				providers[name] = cloneProviderConfig(current)
			} else {
				providers[name] = cloneProviderConfig(pc)
			}
			continue
		}
		if len(pc.Models) > 0 {
			providers[name] = bootstrap.ProviderConfig{Models: append([]string(nil), pc.Models...)}
		}
	}

	addRouteModel := func(provider, model string) {
		provider = strings.TrimSpace(provider)
		model = strings.TrimSpace(model)
		if provider == "" || model == "" || providers[provider].APIKey != "" || h.cfg.PersistProviders[provider] {
			if provider != "" && model != "" && h.cfg.PersistProviders[provider] {
				pc := providers[provider]
				pc.Models = appendUniqueString(pc.Models, model)
				providers[provider] = pc
			}
			return
		}
		pc := providers[provider]
		pc.Models = appendUniqueString(pc.Models, model)
		providers[provider] = pc
	}
	addRouteModel(overlay.Provider, overlay.ModelName)
	for _, rc := range overlay.Roles {
		addRouteModel(rc.Provider, rc.Model)
		for _, fallback := range rc.Fallbacks {
			addRouteModel(fallback.Provider, fallback.Model)
		}
	}
	for _, provider := range overlay.ModelAutoSwitch.FallbackBackends {
		for _, model := range h.cfg.CandidateModels(provider) {
			addRouteModel(provider, model)
		}
	}
	if len(providers) == 0 {
		return nil
	}
	return providers
}

func configHasProviderModel(cfg bootstrap.Config, provider, model string) bool {
	for _, candidate := range cfg.CandidateModels(provider) {
		if strings.TrimSpace(candidate) == model {
			return true
		}
	}
	return false
}

func removeModelCandidate(values []string, model string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == model {
			continue
		}
		out = append(out, value)
	}
	return out
}

func removeProviderCandidate(values []string, provider string) []string {
	provider = strings.TrimSpace(provider)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == provider {
			continue
		}
		out = append(out, value)
	}
	return out
}

func removeModelRef(values []bootstrap.ModelRef, provider, model string) []bootstrap.ModelRef {
	if len(values) == 0 {
		return nil
	}
	out := make([]bootstrap.ModelRef, 0, len(values))
	for _, value := range values {
		if value.Provider == provider && value.Model == model {
			continue
		}
		out = append(out, value)
	}
	return out
}

func recordProjectProviderModel(cfg *bootstrap.Config, provider, model string) {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]bootstrap.ProviderConfig)
	}
	pc := cfg.Providers[provider]
	pc.Models = appendUniqueString(pc.Models, model)
	cfg.Providers[provider] = pc
}

func roleConfigIsEmpty(rc bootstrap.RoleConfig) bool {
	return rc.Provider == "" && rc.Model == "" && rc.ReasoningEffort == "" && len(rc.Fallbacks) == 0
}

func providerConfigHasPrivateConfig(pc bootstrap.ProviderConfig) bool {
	return pc.Type != "" ||
		pc.Auth != "" ||
		pc.AccountID != "" ||
		pc.AuthFile != "" ||
		pc.Disabled ||
		pc.API != "" ||
		pc.APIKey != "" ||
		pc.BaseURL != "" ||
		len(pc.ExtraBody) > 0 ||
		len(pc.Extra) > 0
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.TrimSpace(existing) == value {
			return values
		}
	}
	return append(values, value)
}

func cloneProjectConfig(cfg bootstrap.Config) bootstrap.Config {
	out := cfg
	out.ResumeSchedule.DailyTimes = append([]string(nil), cfg.ResumeSchedule.DailyTimes...)
	if cfg.ScheduledResumeEnabled != nil {
		enabled := *cfg.ScheduledResumeEnabled
		out.ScheduledResumeEnabled = &enabled
	}
	out.PersistPath = ""
	out.PersistProjectOverlay = false
	out.PersistProviders = nil
	out.PersistProjectConfig = nil
	out.ModelAutoSwitch = cloneModelAutoSwitchConfig(cfg.ModelAutoSwitch)
	out.Providers = cloneProviderConfigs(cfg.Providers)
	out.Roles = cloneRoleConfigs(cfg.Roles)
	out.ProjectOwnedProviders = cloneBoolMap(cfg.ProjectOwnedProviders)
	return out
}

func cloneHostRuntimeConfig(cfg bootstrap.Config) bootstrap.Config {
	out := cloneProjectConfig(cfg)
	out.OutputDir = cfg.OutputDir
	out.PersistPath = cfg.PersistPath
	out.PersistProjectOverlay = cfg.PersistProjectOverlay
	if cfg.PersistProviders != nil {
		out.PersistProviders = make(map[string]bool, len(cfg.PersistProviders))
		for provider, owned := range cfg.PersistProviders {
			out.PersistProviders[provider] = owned
		}
	}
	if cfg.PersistProjectConfig != nil {
		project := cloneProjectConfig(*cfg.PersistProjectConfig)
		out.PersistProjectConfig = &project
	}
	return out
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneModelAutoSwitchConfig(cfg bootstrap.ModelAutoSwitchConfig) bootstrap.ModelAutoSwitchConfig {
	out := cfg
	out.FallbackBackends = append([]string(nil), cfg.FallbackBackends...)
	if cfg.Enabled != nil {
		enabled := *cfg.Enabled
		out.Enabled = &enabled
	}
	return out
}

func cloneRoleConfigs(roles map[string]bootstrap.RoleConfig) map[string]bootstrap.RoleConfig {
	if len(roles) == 0 {
		return nil
	}
	out := make(map[string]bootstrap.RoleConfig, len(roles))
	for role, rc := range roles {
		rc.Fallbacks = append([]bootstrap.ModelRef(nil), rc.Fallbacks...)
		out[role] = rc
	}
	return out
}

func cloneProviderConfigs(providers map[string]bootstrap.ProviderConfig) map[string]bootstrap.ProviderConfig {
	if len(providers) == 0 {
		return nil
	}
	out := make(map[string]bootstrap.ProviderConfig, len(providers)+1)
	for name, provider := range providers {
		out[name] = cloneProviderConfig(provider)
	}
	return out
}

func cloneProviderConfig(provider bootstrap.ProviderConfig) bootstrap.ProviderConfig {
	provider.Models = append([]string(nil), provider.Models...)
	if len(provider.ModelReasoningEfforts) > 0 {
		cloned := make(map[string]string, len(provider.ModelReasoningEfforts))
		for model, effort := range provider.ModelReasoningEfforts {
			cloned[model] = effort
		}
		provider.ModelReasoningEfforts = cloned
	}
	provider.ExtraBody = cloneMapAny(provider.ExtraBody)
	provider.Extra = cloneMapAny(provider.Extra)
	return provider
}

func cloneMapAny(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for key, value := range m {
		out[key] = value
	}
	return out
}

func validModelRole(role string) bool {
	normalized := strings.ToLower(strings.TrimSpace(role))
	if strings.HasPrefix(normalized, "stage:") {
		for _, stage := range bootstrap.KnownModelStages {
			if normalized == bootstrap.StageRouteKey(stage) {
				return true
			}
		}
		return false
	}
	switch normalized {
	case "", "default", "coordinator", "architect", "character", "writer", "editor", "auditor":
		return true
	default:
		return false
	}
}

func validateAddedProviderModel(cfg bootstrap.Config, role, provider string, pc bootstrap.ProviderConfig, model string) error {
	cfg.Provider = provider
	cfg.ModelName = model
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]bootstrap.ProviderConfig)
	}
	cfg.Providers[provider] = pc
	return cfg.ValidateBase()
}

// concreteThinkingRoles 是可应用推理强度的具体角色（与 agents.ApplyThinking 路由一致）。
// 调 default 时按各角色 ResolveReasoningEffort 逐个重新应用。
var concreteThinkingRoles = []string{"coordinator", "architect", "character", "writer", "editor", "auditor"}

// CurrentThinking 返回某角色当前生效的推理强度原始串（供 /model 面板同步当前值）。
func (h *Host) CurrentThinking(role string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.ResolveReasoningEffort(strings.ToLower(strings.TrimSpace(role)))
}

func (h *Host) AvailableThinking(role string) []agentcore.ThinkingLevel {
	h.mu.Lock()
	model := h.models.ForRole(strings.ToLower(strings.TrimSpace(role)))
	h.mu.Unlock()
	return agents.AvailableThinkingForModel(model)
}

func (h *Host) normalizeThinkingLocked(role string) agentcore.ThinkingLevel {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		parsed, _ := agents.ParseThinkingLevel(h.cfg.ReasoningEffort)
		for _, r := range concreteThinkingRoles {
			resolved, ok := agents.ResolveThinkingForModel(h.models.ForRole(r), parsed)
			if !ok || resolved != parsed {
				h.cfg.ReasoningEffort = string(resolved)
				return resolved
			}
		}
		h.cfg.ReasoningEffort = string(parsed)
		return parsed
	}

	_, hasRoleThinking := h.cfg.Roles[role]
	hasRoleThinking = hasRoleThinking && h.cfg.Roles[role].ReasoningEffort != ""
	parsed, _ := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role))
	resolved, _ := agents.ResolveThinkingForModel(h.models.ForRole(role), parsed)
	if !hasRoleThinking {
		if resolved != parsed {
			h.cfg.ReasoningEffort = string(resolved)
		}
		return resolved
	}
	if h.cfg.Roles == nil {
		h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
	}
	rc := h.cfg.Roles[role]
	rc.ReasoningEffort = string(resolved)
	h.cfg.Roles[role] = rc
	return resolved
}

func (h *Host) applyThinkingLocked(role string) {
	if h.thinkingApplier == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		for _, r := range concreteThinkingRoles {
			lv, _ := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(r))
			h.thinkingApplier(r, lv)
		}
		return
	}
	lv, _ := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role))
	h.thinkingApplier(role, lv)
}

// SetRoleThinking 设置某角色（或 default）的推理强度：校验→持久化→联动 live agent→事件。
// 镜像 SwitchModel 的结构；与模型选择正交，可单独调整。level 为空 = 不覆盖（继承）。
func (h *Host) SetRoleThinking(role, level string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	h.mu.Lock()
	defer h.mu.Unlock()

	parsed, err := agents.ParseThinkingLevel(level)
	if err != nil {
		return err
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		for _, r := range concreteThinkingRoles {
			if resolved, ok := agents.ResolveThinkingForModel(h.models.ForRole(r), parsed); !ok || resolved != parsed {
				parsed = resolved
				break
			}
		}
	} else {
		parsed, _ = agents.ResolveThinkingForModel(h.models.ForRole(role), parsed)
	}
	h.recordProjectThinkingLocked(role, string(parsed))
	// 持久化：具体角色写 Roles[role].ReasoningEffort，default/"" 写顶层 ReasoningEffort。
	if role == "" || role == "default" {
		h.cfg.ReasoningEffort = string(parsed)
	} else {
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.ReasoningEffort = string(parsed)
		h.cfg.Roles[role] = rc
	}
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("保存配置失败", "module", "host", "err", err)
	}

	// 联动 live：具体角色直接应用；default 则遍历各具体角色按 ResolveReasoningEffort 重新应用
	// （已被角色级覆盖的保留自身，未覆盖的吃上新默认）。
	h.models.ApplyReasoningConfig(h.cfg)
	h.applyThinkingLocked(role)

	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	shown := string(parsed)
	if shown == "" {
		shown = "默认(继承)"
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("推理强度已切换：%s → %s", logRole, shown),
		Level:    "info",
	})
	return nil
}

func (h *Host) CurrentCoCreateTimeoutSeconds() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.EffectiveCoCreateTimeoutSeconds()
}

func (h *Host) ScheduledResumeEnabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.EffectiveScheduledResumeEnabled()
}

func (h *Host) SetScheduledResumeEnabled(enabled bool) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	h.mu.Lock()
	defer h.mu.Unlock()
	previous := cloneHostRuntimeConfig(h.cfg)
	if h.cfg.PersistProjectOverlay {
		path := strings.TrimSpace(h.cfg.PersistPath)
		if path != "" {
			if _, err := os.Stat(path); err == nil {
				latest, err := bootstrap.LoadConfigFile(path)
				if err != nil {
					return fmt.Errorf("reload project settings before scheduled resume update: %w", err)
				}
				h.cfg.PersistProjectConfig = &latest
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat project settings before scheduled resume update: %w", err)
			}
		}
	}
	h.cfg.ScheduledResumeEnabled = &enabled
	if overlay := h.ensureProjectOverlayLocked(); overlay != nil {
		overlay.ScheduledResumeEnabled = &enabled
	}
	if err := h.persistConfigLocked(); err != nil {
		h.cfg = previous
		return fmt.Errorf("save scheduled resume setting: %w", err)
	}
	return nil
}

func (h *Host) SetCoCreateTimeoutSeconds(seconds int) error {
	release, leaseErr := h.beginNormalFlowMutation()
	if leaseErr != nil {
		return leaseErr
	}
	defer release()
	normalized, err := bootstrap.NormalizeCoCreateTimeoutSeconds(seconds)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.cfg.CoCreateTimeoutSeconds = normalized
	if overlay := h.ensureProjectOverlayLocked(); overlay != nil {
		overlay.CoCreateTimeoutSeconds = normalized
	}
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("保存配置失败", "module", "host", "err", err)
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("共创超时已设置为 %d 秒", normalized),
		Level:    "info",
	})
	return nil
}

func (h *Host) CurrentCoCreateMaxTokens() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.EffectiveCoCreateMaxTokens()
}

func (h *Host) SetCoCreateMaxTokens(tokens int) error {
	release, leaseErr := h.beginNormalFlowMutation()
	if leaseErr != nil {
		return leaseErr
	}
	defer release()
	normalized, err := bootstrap.NormalizeCoCreateMaxTokens(tokens)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.cfg.CoCreateMaxTokens = normalized
	if overlay := h.ensureProjectOverlayLocked(); overlay != nil {
		overlay.CoCreateMaxTokens = normalized
	}
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("保存配置失败", "module", "host", "err", err)
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("共创输出上限已设置为 %d tokens", normalized),
		Level:    "info",
	})
	return nil
}

func (h *Host) CurrentModelCallMaxAttempts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.ModelAutoSwitch.EffectiveNetworkMaxAttempts()
}

func (h *Host) CurrentStructureRepairMaxAttempts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.EffectiveStructureRepairMaxAttempts()
}

func (h *Host) CurrentBudgetQualityMaxAttempts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.EffectiveBudgetQualityMaxAttempts()
}

func (h *Host) CurrentAdaptationOutlineAuditRetryMaxAttempts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.EffectiveAdaptationOutlineAuditRetryMaxAttempts()
}

func (h *Host) SetRetrySettings(modelCallMaxAttempts, structureRepairMaxAttempts, budgetQualityMaxAttempts, adaptationOutlineAuditRetryMaxAttempts int) error {
	release, leaseErr := h.beginNormalFlowMutation()
	if leaseErr != nil {
		return leaseErr
	}
	defer release()
	modelAttempts, err := bootstrap.NormalizeRuntimeNetworkMaxAttempts(modelCallMaxAttempts)
	if err != nil {
		return err
	}
	repairAttempts, err := bootstrap.NormalizeStructureRepairMaxAttempts(structureRepairMaxAttempts)
	if err != nil {
		return err
	}
	budgetAttempts, err := bootstrap.NormalizeBudgetQualityMaxAttempts(budgetQualityMaxAttempts)
	if err != nil {
		return err
	}
	auditAttempts, err := bootstrap.NormalizeAdaptationOutlineAuditRetryMaxAttempts(adaptationOutlineAuditRetryMaxAttempts)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	previous := cloneHostRuntimeConfig(h.cfg)
	h.cfg.ModelAutoSwitch.NetworkMaxAttempts = modelAttempts
	h.cfg.StructureRepairMaxAttempts = repairAttempts
	h.cfg.BudgetQualityMaxAttempts = budgetAttempts
	h.cfg.AdaptationOutlineAuditRetryMaxAttempts = auditAttempts
	if overlay := h.ensureProjectOverlayLocked(); overlay != nil {
		overlay.ModelAutoSwitch.NetworkMaxAttempts = modelAttempts
		overlay.StructureRepairMaxAttempts = repairAttempts
		overlay.BudgetQualityMaxAttempts = budgetAttempts
		overlay.AdaptationOutlineAuditRetryMaxAttempts = auditAttempts
	}
	if err := h.persistConfigLocked(); err != nil {
		h.cfg = previous
		return fmt.Errorf("save retry settings: %w", err)
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("重试设置已更新：模型调用最多 %d 次，结构修复最多 %d 次，预算复核最多 %d 次，改编章节纲质量审计失败后重试 %d 次", modelAttempts, repairAttempts, budgetAttempts, auditAttempts),
		Level:    "info",
	})
	return nil
}

func (h *Host) coCreateTimeout() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.CoCreateTimeout()
}

func (h *Host) coCreateMaxTokens() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.EffectiveCoCreateMaxTokens()
}

// ── 事件回放 ──

func (h *Host) ReplayQueue(afterSeq int64) ([]domain.RuntimeQueueItem, error) {
	if h.store == nil || h.store.Runtime == nil {
		return nil, nil
	}
	return h.store.Runtime.LoadQueueAfter(afterSeq)
}

// ── 共创 ──

// CoCreateStream 冷启动共创：从零澄清需求，产出整本书的创作指令。
func (h *Host) CoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return CoCreateReply{}, err
	}
	defer release()
	return coCreateStream(ctx, h.models, h.store.Sessions, h.coCreateTimeout(), h.coCreateMaxTokens(), coCreateSystemPromptWithSimulation(h.store, h.cfg.EffectiveSimulationMode()), history, onProgress, h.store)
}

// StageCoCreateStream 阶段共创：在已写内容的基础上规划后续方向。
// 系统提示 = 阶段 prompt + 当前故事状态摘要，让助手知道"已经写了什么"。
func (h *Host) StageCoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return CoCreateReply{}, err
	}
	defer release()
	return coCreateStream(ctx, h.models, h.store.Sessions, h.coCreateTimeout(), h.coCreateMaxTokens(), stageSystemPromptWithSimulation(h.store, h.cfg.EffectiveSimulationMode()), history, onProgress, h.store)
}

// ContinuationCoCreateStream uses the written source novel as immutable story
// context while the user and model consolidate a continuation Draft. Committing
// that Draft is handled by the continuation workflow; this method never resumes
// the writer by itself.
func (h *Host) ContinuationCoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return h.StageCoCreateStream(ctx, history, onProgress)
}

// AdaptCoCreateStream 改编共创：基于原书分析快照澄清改编目标。
func (h *Host) AdaptCoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return CoCreateReply{}, err
	}
	defer release()
	return coCreateStream(ctx, h.models, h.store.Sessions, h.coCreateTimeout(), h.coCreateMaxTokens(), adaptSystemPrompt(h.store), history, onProgress, h.store)
}

func (h *Host) EnsureAdaptationCoCreateBriefing(ctx context.Context, sourcePath string, intent domain.AdaptationCoCreateIntent) (*domain.AdaptationCoCreateBriefing, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if _, _, err := adapt.ValidatePreparedSource(h.store, sourcePath); err != nil {
		return nil, err
	}
	return adapt.EnsureCoCreateBriefing(ctx, h.adaptationDeps(), intent, h.adaptationProgressEmitter())
}

func (h *Host) EnsureAdaptationProposalCoCreateBriefing(ctx context.Context, sourcePath string, intent domain.AdaptationCoCreateIntent) (*domain.AdaptationCoCreateBriefing, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	if _, _, err := adapt.ValidatePreparedSource(h.store, sourcePath); err != nil {
		return nil, err
	}
	return adapt.EnsureProposalCoCreateBriefing(ctx, h.adaptationDeps(), intent, h.adaptationProgressEmitter())
}

func (h *Host) adaptationProgressEmitter() adapt.ProgressEmitter {
	return func(stage adapt.Stage, current, total int, message string, err error) {
		level := "info"
		detail := ""
		if err != nil {
			detail = err.Error()
			if stage == adapt.StageError {
				level = "error"
			} else {
				level = "warn"
			}
		} else if stage == adapt.StageDone {
			level = "success"
		}
		if strings.TrimSpace(message) == "" {
			message = detail
		}
		h.emitEvent(Event{
			Time:     time.Now().UTC(),
			Category: "ADAPT",
			Agent:    "web",
			Summary:  message,
			Detail:   detail,
			Kind:     string(stage),
			Level:    level,
		})
	}
}

func (h *Host) ResolveAdaptationCoCreateDecision(decisionID, optionID, customAnswer string) (*domain.AdaptationCoCreateBriefing, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	return h.store.Adaptation.ResolveCoCreateBriefingDecision(decisionID, optionID, customAnswer)
}

func (h *Host) ResolveAdaptationCoCreateDecisions(decisions []domain.AdaptationResolvedDecision) (*domain.AdaptationCoCreateBriefing, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	return h.store.Adaptation.ResolveCoCreateBriefingDecisions(decisions)
}

// stagePlanPrefix 把共创产出的"后续方向 brief"包装成一条阶段规划干预，交 Coordinator 裁定。
// 只贴 [阶段规划] 事实标记 + 中性陈述，不写死"怎么落地"——具体路由（compass / architect /
// save_user_rules）交给 coordinator.md 的「阶段规划」判据，避免与 prompt 形成第二真相源、
// 也不堵死风格类要求走 user_rules（守"分类裁定归 LLM"）。Continue 再叠加 [用户干预] 前缀。
const stagePlanPrefix = "[阶段规划] 我暂停创作，和共创助手一起梳理了下面的后续方向，请按你的干预分类裁定如何落地，然后继续创作。后续方向如下：\n\n"

// PauseForCoCreate 进入阶段共创：置共创占用标记，运行中则一并暂停 coordinator。
// 返回 false 表示无法进入（全书已完成或已在共创中），调用方忽略即可。
// 占用标记在共创窗口内堵住 import/simulate/start/resume/continue 的并发介入——
// 运行中暂停后 lifecycle=paused，现有 ==running 互斥失效，靠该标记补缺；
// 已停止（idle/paused）也允许进入，规划完经 Continue 续跑。
func (h *Host) PauseForCoCreate() bool {
	h.mu.Lock()
	if h.cocreating || h.lifecycle == lifecycleCompleted {
		h.mu.Unlock()
		return false
	}
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()
	ownership, err := h.acquireNormalFlowOwnership("host:co-create")
	if err != nil {
		return false
	}
	h.mu.Lock()
	if h.cocreating || h.lifecycle == lifecycleCompleted {
		h.mu.Unlock()
		ownership.Release()
		return false
	}
	h.cocreating = true
	h.mu.Unlock()
	ownership.TransferToCoCreate()

	// 运行中复用 abortWithEvent 停机（running→paused + setAborting + Abort + 事件），与手动
	// 暂停同序、不另抄一遍；已停止（idle/paused）只置标记，规划完经 Continue 续跑。
	if running {
		h.abortWithEvent("进入阶段共创，创作已暂停", "info")
	} else {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "进入阶段共创", Level: "info"})
	}
	return true
}

// ResumeFromCoCreate 结束阶段共创：把共创产出的后续方向作为干预注入并恢复创作。
// 清占用标记后复用 Continue 的停机注入路径（受预算前置约束）。
// 注：draft 为空时提前返回、不清标记是有意的（共创尚未结束）；TUI 侧 canStart() 守卫
// 与此处用同一"非空"判据，保证该路径不可达，cocreating 不会因此泄漏。
func (h *Host) ResumeFromCoCreate(draft string) error {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return fmt.Errorf("draft is required")
	}
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("not in co-create")
	}
	h.mu.Unlock()

	// PauseForCoCreate 的 Abort 是异步的：恢复前等旧 run 收敛，回到与手动暂停后 Continue
	// 一致的"真停机"前提，避免把续跑指令 steer 进正在退出的旧 run。非运行态进共创（未
	// Abort）时 coordinator 本就 idle，WaitForIdle 立即返回。
	if h.coordinator != nil {
		h.coordinator.WaitForIdle()
	}
	if err := h.refuseNormalFlowDuringRevision(); err != nil {
		return err
	}
	ownership, err := h.acquireNormalFlowOwnership("host:resume-co-create")
	if err != nil {
		return err
	}
	defer ownership.Release()
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("not in co-create")
	}
	h.cocreating = false
	h.mu.Unlock()

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "阶段共创完成，已注入后续方向并恢复创作", Level: "info"})
	if err := h.Continue(stagePlanPrefix + draft); err != nil {
		h.mu.Lock()
		h.cocreating = true
		h.mu.Unlock()
		return err
	}
	h.releaseNormalFlowCoCreateOwnership()
	return nil
}

// CancelCoCreate 放弃阶段共创：清占用标记，保持暂停态（用户可在输入框继续或重启 Resume）。
func (h *Host) CancelCoCreate() {
	if h.coordinator != nil {
		h.coordinator.WaitForIdle()
	}
	ownership, err := h.acquireNormalFlowOwnership("host:cancel-co-create")
	if err != nil {
		slog.Warn("cancel co-create could not acquire normal-flow ownership", "module", "host", "err", err)
		return
	}
	defer ownership.Release()
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return
	}
	h.cocreating = false
	h.mu.Unlock()
	h.releaseNormalFlowCoCreateOwnership()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "已退出阶段共创，创作保持暂停（可在输入框继续）", Level: "info"})
}

// ── 工具 ──

func (h *Host) refreshWriterRestore() {
	if h.writerRestore != nil {
		h.writerRestore.Refresh(h.store)
	}
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// ImportFrom 启动一次外部小说反推导入：切分 → 反推 foundation → 逐章分析落盘。
// 与 Coordinator 互斥；导入完成后调用方可立即 Resume() 续写。
// 返回的事件通道由 imp.Run 关闭，调用方负责消费（满则丢弃以防阻塞分析协程）。
var prepareContinuationImportUserRules = func(h *Host) error {
	return h.PrepareExternalSourceUserRules("")
}

var runContinuationImport = imp.Run

func (h *Host) ImportFrom(ctx context.Context, opts imp.Options) (<-chan imp.Event, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	if err := h.guardExclusive("导入"); err != nil {
		release()
		return nil, err
	}
	if continuation, err := h.ContinuationSnapshot(); err != nil {
		release()
		return nil, fmt.Errorf("inspect continuation state: %w", err)
	} else if continuation != nil && continuation.Workflow.Stage != domain.ContinuationStageSourceReady {
		release()
		return nil, fmt.Errorf("continuation planning already started at stage %q; roll it back before replacing the source", continuation.Workflow.Stage)
	}
	if err := prepareContinuationImportUserRules(h); err != nil {
		release()
		return nil, err
	}
	if source, err := h.store.Adaptation.LoadSourceManifest(); err != nil {
		release()
		return nil, fmt.Errorf("inspect adaptation state: %w", err)
	} else if source != nil {
		release()
		return nil, fmt.Errorf("project contains adaptation source state; create a new project or explicitly roll it back before importing a continuation source")
	}

	deps := imp.Deps{
		Store:      h.store,
		CommitTool: tools.NewCommitChapterTool(h.store, adapt.NewCompletionGate(h.store)),
		LLM:        h.models.ForStage(bootstrap.StageSourceAnalysis),
		Prompts: imp.Prompts{
			Foundation: h.bundle.Prompts.ImportFoundation,
			Analyzer:   h.bundle.Prompts.ImportAnalyzer,
		},
	}
	sourceSignature, err := sourceFileSignature(opts.SourcePath)
	if err != nil {
		release()
		return nil, fmt.Errorf("hash continuation source: %w", err)
	}
	events, err := runContinuationImport(ctx, deps, opts)
	if err != nil {
		release()
		return nil, err
	}
	if events == nil {
		release()
		return nil, fmt.Errorf("continuation import event stream is nil")
	}
	return holdNormalFlowStream(ctx, h.withContinuationImportFinalization(ctx, sourceSignature, events), release), nil
}

func sourceFileSignature(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (h *Host) withContinuationImportFinalization(ctx context.Context, sourceSignature string, source <-chan imp.Event) <-chan imp.Event {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan imp.Event, 32)
	go func() {
		defer close(out)
		canceled := false
		for event := range source {
			if !canceled {
				select {
				case <-ctx.Done():
					canceled = true
				default:
				}
			}
			if canceled {
				continue
			}
			if event.Stage == imp.StageDone {
				progress, err := h.store.Progress.Load()
				baseChapterCount := 0
				if progress != nil {
					baseChapterCount = progress.LatestCompleted()
				}
				if err == nil && baseChapterCount <= 0 {
					err = fmt.Errorf("import completed without committed source chapters")
				}
				if err == nil {
					_, err = h.store.Continuation.InitializeSource(sourceSignature, baseChapterCount)
				}
				if err != nil {
					event.Stage = imp.StageError
					event.Message = "初始化续写规划失败"
					event.Err = err
				} else {
					event.Message = fmt.Sprintf("导入完成：%d 章；已建立续写规划基线，等待确认 Draft", baseChapterCount)
				}
			}
			select {
			case out <- event:
			case <-ctx.Done():
				canceled = true
			}
		}
	}()
	return out
}

// PrepareAdaptationSource analyzes a source novel for adaptation without
// committing its chapters as final output.
var runAdaptationSource = adapt.RunSource

func (h *Host) PrepareAdaptationSource(ctx context.Context, sourcePath string) (<-chan adapt.Event, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	if err := h.guardExclusive("改编源书分析"); err != nil {
		release()
		return nil, err
	}
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	deps := adapt.Deps{
		Store: h.store,
		LLM:   h.models.ForStageWithFailover(bootstrap.StageSourceAnalysis, h.reportAdaptationFailover),
		ModelForStage: func(stage string) imp.LLMChat {
			return h.models.ForStageWithFailover(stage, h.reportAdaptationFailover)
		},
		ModelName: func() string {
			_, model, _ := h.models.CurrentStageSelection(bootstrap.StageSourceAnalysis)
			return model
		}(),
		ModelCallMaxAttempts:       cfg.ModelAutoSwitch.EffectiveNetworkMaxAttempts(),
		StructureRepairMaxAttempts: cfg.EffectiveStructureRepairMaxAttempts(),
		BudgetQualityMaxAttempts:   cfg.EffectiveBudgetQualityMaxAttempts(),
		Prompts: adapt.Prompts{
			Foundation:      h.bundle.Prompts.ImportFoundation,
			FoundationMerge: h.bundle.Prompts.ImportFoundationMerge,
			Analyzer:        h.bundle.Prompts.ImportAnalyzer,
			Planner:         h.bundle.Prompts.AdaptationPlanner,
		},
	}
	events, err := runAdaptationSource(ctx, deps, adapt.Options{SourcePath: sourcePath})
	if err != nil {
		release()
		return nil, err
	}
	if events == nil {
		release()
		return nil, fmt.Errorf("adaptation source event stream is nil")
	}
	return holdNormalFlowStream(ctx, events, release), nil
}

func (h *Host) reportAdaptationFailover(ev bootstrap.FailoverEvent) {
	from := ev.FromProvider + "/" + ev.FromModel
	to := ev.ToProvider + "/" + ev.ToModel
	slog.Warn("adaptation preparation provider failover",
		"module", "host",
		"from", from,
		"to", to,
		"reason", ev.Reason,
		"err", ev.Err)
	if !strings.HasPrefix(ev.Reason, bootstrap.RuntimeFallbackPoolReasonPrefix) {
		h.promoteAdaptationFailoverTarget(ev)
	}
	h.recordModelFailoverEvent("改编准备", ev)
}

func (h *Host) reportModelFailover(ev bootstrap.FailoverEvent) {
	h.recordModelFailoverEvent(modelFailoverRoleLabel(ev.Role), ev)
}

func (h *Host) recordModelFailoverEvent(roleLabel string, ev bootstrap.FailoverEvent) {
	from := modelRouteLabel(ev.FromProvider, ev.FromModel)
	to := modelRouteLabel(ev.ToProvider, ev.ToModel)
	if from == to || from == "" || to == "" {
		return
	}
	reason := modelFailoverReasonLabel(ev.Reason, ev.Err)
	summary := fmt.Sprintf("模型自动切换（%s）：%s → %s", roleLabel, from, to)
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Agent:    ev.Role,
		Summary:  summary,
		Detail:   fmt.Sprintf("原因：%s", reason),
		Kind:     "model_auto_switch",
		Level:    "warn",
	})
}

func modelRouteLabel(provider, model string) string {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		return model
	}
	if model == "" {
		return provider
	}
	return provider + "/" + model
}

func modelFailoverRoleLabel(role string) string {
	normalized := strings.ToLower(strings.TrimSpace(role))
	switch {
	case strings.Contains(normalized, "writing"), strings.Contains(normalized, "writer"):
		return "正文写作"
	case strings.Contains(normalized, "review"), strings.Contains(normalized, "editor"):
		return "质量审核"
	case strings.Contains(normalized, "skeleton"), strings.Contains(normalized, "architect"):
		return "结构规划"
	case strings.Contains(normalized, "coordinator"):
		return "流程协调"
	default:
		return "创作任务"
	}
}

func modelFailoverReasonLabel(reason string, err error) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	if err != nil {
		normalized += " " + strings.ToLower(err.Error())
	}
	switch {
	case strings.Contains(normalized, "insufficient_user_quota"), strings.Contains(normalized, "insufficient quota"), strings.Contains(normalized, "quota"), strings.Contains(normalized, "额度"):
		return "额度不足"
	case strings.Contains(normalized, "rate"):
		return "服务限流"
	case strings.Contains(normalized, "timeout"), strings.Contains(normalized, "stream_idle"), strings.Contains(normalized, "context deadline exceeded"):
		return "响应超时"
	case strings.Contains(normalized, "network"), strings.Contains(normalized, "disconnect"):
		return "网络连接失败"
	case strings.Contains(normalized, "empty"):
		return "模型返回空内容"
	case strings.Contains(normalized, "gateway"), strings.Contains(normalized, "unavailable"):
		return "服务暂不可用"
	case normalized == "":
		return "当前后端不可用"
	default:
		return "当前后端不可用"
	}
}

func (h *Host) promoteAdaptationFailoverTarget(ev bootstrap.FailoverEvent) {
	provider := strings.TrimSpace(ev.ToProvider)
	model := strings.TrimSpace(ev.ToModel)
	if provider == "" || model == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.switchAllAgentModelsLocked(provider, model); err != nil {
		slog.Warn("promote adaptation failover model failed",
			"module", "host",
			"provider", provider,
			"model", model,
			"err", err,
		)
		return
	}
	h.recordAllProjectModelRoutesLocked(provider, model)
	if err := h.persistConfigLocked(); err != nil {
		slog.Warn("save adaptation failover route failed", "module", "host", "err", err)
	}
}

// Simulate 读取 simulate 目录并生成或增量更新仿写画像。
func (h *Host) Simulate(ctx context.Context) (<-chan sim.Event, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working dir: %w", err)
	}
	return h.SimulateFromDir(ctx, filepath.Join(wd, "simulate"))
}

// SimulateFromDir reads the supplied simulate source directory. Web projects use
// this to keep uploaded corpus files inside the selected project root, while
// Simulate keeps the legacy cwd/simulate behavior for CLI and TUI users.
var runSimulation = sim.Run

func (h *Host) SimulateFromDir(ctx context.Context, dir string) (<-chan sim.Event, error) {
	return h.SimulateFromDirWithAction(ctx, dir, sim.ActionScan)
}

func (h *Host) SimulateFromDirWithAction(ctx context.Context, dir, action string) (<-chan sim.Event, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	if err := h.guardExclusive("生成仿写画像"); err != nil {
		release()
		return nil, err
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		release()
		return nil, fmt.Errorf("simulate source dir is required")
	}
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	sourceProvider, sourceModel, _ := h.models.CurrentStageSelection(bootstrap.StageSourceAnalysis)
	deps := sim.Deps{
		Store:                      h.store,
		LLM:                        h.models.ForStage(bootstrap.StageSourceAnalysis),
		ModelCallMaxAttempts:       cfg.ModelAutoSwitch.EffectiveNetworkMaxAttempts(),
		StructureRepairMaxAttempts: cfg.EffectiveStructureRepairMaxAttempts(),
		ModelIdentity:              strings.Trim(sourceProvider+"/"+sourceModel, "/"),
		Prompts: sim.Prompts{
			Source: h.bundle.Prompts.SimulationSource,
			Merge:  h.bundle.Prompts.SimulationMerge,
		},
	}
	events, err := runSimulation(ctx, deps, sim.Options{SourceDir: dir, Action: action})
	if err != nil {
		release()
		return nil, err
	}
	if events == nil {
		release()
		return nil, fmt.Errorf("simulation event stream is nil")
	}
	return holdNormalFlowStream(ctx, events, release), nil
}

// ImportSimulationProfile 导入此前生成的仿写画像。
var runSimulationImport = sim.RunImport

func (h *Host) ImportSimulationProfile(ctx context.Context, path string) (<-chan sim.Event, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	if err := h.guardExclusive("导入仿写画像"); err != nil {
		release()
		return nil, err
	}
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	deps := sim.Deps{
		Store:                      h.store,
		LLM:                        h.models.ForStage(bootstrap.StageSourceAnalysis),
		ModelCallMaxAttempts:       cfg.ModelAutoSwitch.EffectiveNetworkMaxAttempts(),
		StructureRepairMaxAttempts: cfg.EffectiveStructureRepairMaxAttempts(),
		Prompts: sim.Prompts{
			Merge: h.bundle.Prompts.SimulationMerge,
		},
	}
	events, err := runSimulationImport(ctx, deps, path)
	if err != nil {
		release()
		return nil, err
	}
	if events == nil {
		release()
		return nil, fmt.Errorf("simulation import event stream is nil")
	}
	return holdNormalFlowStream(ctx, events, release), nil
}

func holdNormalFlowStream[T any](ctx context.Context, source <-chan T, release func()) <-chan T {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan T, 32)
	go func() {
		defer close(out)
		defer release()
		canceled := false
		for event := range source {
			if canceled {
				continue
			}
			select {
			case out <- event:
			case <-ctx.Done():
				canceled = true
			}
		}
	}()
	return out
}

// guardExclusive 检查独占占用：coordinator 运行中或阶段共创窗口内时拒绝会改写状态的入口
// （import/simulate）。补上 paused 期间只查 ==running 的并发缺口。
func (h *Host) guardExclusive(action string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.lifecycle == lifecycleRunning:
		return fmt.Errorf("coordinator 运行中，请先暂停后再%s", action)
	case h.cocreating:
		return fmt.Errorf("阶段共创进行中，请先结束共创后再%s", action)
	}
	return nil
}

// Export 导出已完成章节为外部文件（当前仅支持 TXT）。
//
// 与 ImportFrom 不同：导出是只读操作（不动 Progress / Checkpoint），
// 因此**不要求 Coordinator 空闲**——写作中途也可以随时导出"现阶段成品"。
// 只读到 Progress.CompletedChapters + 章节终稿 + 大纲 + premise 的一致快照。
func (h *Host) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	return exp.Run(ctx, exp.Deps{Store: h.store}, opts)
}
