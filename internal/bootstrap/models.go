package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/litellm"
)

// 长输出 + 长 ctx 场景下，reasoning-aware provider（mimo / deepseek-r1 等）
// 思考阶段如果 server 端不流式发 reasoning delta，SSE 整段会保持沉默。
// litellm 默认 watchdog 是 2 分钟，对 8000 字写作章节经常触发误杀。
// 5 分钟覆盖绝大多数实测案例（参见 tasks/todo.md plan→draft 思考时长统计），
// 仍小于 RequestTimeout 10 分钟，网络真死时仍能兜底。
const streamIdleTimeout = 5 * time.Minute

// FailoverEvent 表示一次显式 provider 切换。
// Reason 为短标签（rate_limit / timeout / stream_idle / network），用于结构化日志。
type FailoverEvent struct {
	Role         string
	Reason       string
	FromProvider string
	FromModel    string
	ToProvider   string
	ToModel      string
	Err          error
}

// FailoverReporter 在发生显式切换时被调用。
type FailoverReporter func(FailoverEvent)

type modelTarget struct {
	provider string
	name     string
	model    agentcore.ChatModel
}

// SwappableModel 是可热切换的 ChatModel 包装器。
// 已开始的请求继续使用旧实例；后续请求自动切到新实例。
type SwappableModel struct {
	*agentcore.SwappableModel
	mu       sync.RWMutex
	provider string
	name     string
	thinking agentcore.ThinkingLevel
}

func NewSwappableModel(provider, name string, model agentcore.ChatModel) *SwappableModel {
	return &SwappableModel{
		SwappableModel: agentcore.NewSwappableModel(model),
		provider:       provider,
		name:           name,
	}
}

func (m *SwappableModel) ProviderName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider
}

func (m *SwappableModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.SwappableModel.Generate(ctx, messages, tools, m.withThinking(opts)...)
}

func (m *SwappableModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return m.SwappableModel.GenerateStream(ctx, messages, tools, m.withThinking(opts)...)
}

func (m *SwappableModel) withThinking(opts []agentcore.CallOption) []agentcore.CallOption {
	m.mu.RLock()
	level := m.thinking
	m.mu.RUnlock()
	next := opts
	if level != "" {
		next = append([]agentcore.CallOption(nil), opts...)
		next = append(next, agentcore.WithThinking(level))
	}

	requested := agentcore.ResolveCallConfig(next).ThinkingLevel
	resolved, supported := llm.ThinkingPolicyFor(m).Resolve(requested)
	if supported {
		return next
	}
	if len(next) == len(opts) {
		next = append([]agentcore.CallOption(nil), opts...)
	}
	return append(next, agentcore.WithThinking(resolved))
}

func (m *SwappableModel) SetThinking(level string) {
	m.mu.Lock()
	m.thinking = agentcore.ThinkingLevel(strings.ToLower(strings.TrimSpace(level)))
	m.mu.Unlock()
}

func (m *SwappableModel) Info() llm.ModelInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if info, ok := m.SwappableModel.Current().(interface{ Info() llm.ModelInfo }); ok {
		modelInfo := info.Info()
		if modelInfo.Name == "" {
			modelInfo.Name = m.name
		}
		if modelInfo.Provider == "" {
			modelInfo.Provider = m.provider
		}
		return modelInfo
	}
	return llm.ModelInfo{
		Name:     m.name,
		Provider: m.provider,
	}
}

func (m *SwappableModel) Capabilities() llm.Capabilities {
	if cp, ok := m.SwappableModel.Current().(llm.CapabilityProvider); ok {
		return cp.Capabilities()
	}
	return llm.Capabilities{}
}

func (m *SwappableModel) Swap(provider, name string, model agentcore.ChatModel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SwappableModel.Swap(model)
	m.provider = provider
	m.name = name
}

func (m *SwappableModel) Current() (provider, name string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider, m.name
}

// ModelSet 持有按角色分配的模型实例，未配置的角色回退到默认模型。
type ModelSet struct {
	Default           *SwappableModel
	models            map[string]*SwappableModel
	fallbacks         map[string][]modelTarget
	config            Config
	runtimeController RuntimeFallbackController
}

// ForRole 返回指定角色的模型，未配置时返回默认模型。
func (ms *ModelSet) ForRole(role string) agentcore.ChatModel {
	if m, ok := ms.models[role]; ok {
		return newRuntimeFallbackModel(role, m, m, ms.config.ModelAutoSwitch, ms.runtimeController, nil)
	}
	return newRuntimeFallbackModel(role, ms.Default, ms.Default, ms.config.ModelAutoSwitch, ms.runtimeController, nil)
}

// ForRoleWithFailover 返回带有单次请求级 fallback 的角色模型。
// 仅当该角色显式配置了 fallbacks 时生效；未配置时退化为普通模型。
func (ms *ModelSet) ForRoleWithFailover(role string, report FailoverReporter) agentcore.ChatModel {
	primary, ok := ms.models[role]
	if !ok {
		return newRuntimeFallbackModel(role, ms.Default, ms.Default, ms.config.ModelAutoSwitch, ms.runtimeController, report)
	}
	targets := ms.fallbacks[role]
	if len(targets) == 0 {
		return newRuntimeFallbackModel(role, primary, primary, ms.config.ModelAutoSwitch, ms.runtimeController, report)
	}
	base := &failoverModel{
		role:      role,
		primary:   primary,
		fallbacks: append([]modelTarget(nil), targets...),
		report:    report,
	}
	return newRuntimeFallbackModel(role, primary, base, ms.config.ModelAutoSwitch, ms.runtimeController, report)
}

// ForStageWithFailover resolves a user-facing workflow stage first, then falls
// back to its Agent route. This keeps stage controls simple without removing
// the lower-level Agent routing used by advanced users.
func (ms *ModelSet) ForStageWithFailover(stage string, report FailoverReporter) agentcore.ChatModel {
	key := StageRouteKey(stage)
	if _, ok := ms.config.Roles[key]; ok {
		return ms.ForRoleWithFailover(key, report)
	}
	if model, ok := ms.models[key]; ok {
		return newRuntimeFallbackModel(key, model, model, ms.config.ModelAutoSwitch, ms.runtimeController, report)
	}
	return ms.ForRoleWithFailover(StageFallbackRole(stage), report)
}

func (ms *ModelSet) ForStage(stage string) agentcore.ChatModel {
	return ms.ForStageWithFailover(stage, nil)
}

// CurrentStageSelection reports whether the stage itself is overridden. An
// inherited Agent route remains visible in provider/model but explicit=false.
func (ms *ModelSet) CurrentStageSelection(stage string) (provider, model string, explicit bool) {
	key := StageRouteKey(stage)
	if sw, ok := ms.models[key]; ok {
		provider, model = sw.Current()
		_, explicit = ms.config.Roles[key]
		return provider, model, explicit
	}
	provider, model, _ = ms.CurrentSelection(StageFallbackRole(stage))
	return provider, model, false
}

func (ms *ModelSet) SetRuntimeFallbackController(controller RuntimeFallbackController) {
	ms.runtimeController = controller
}

// Summary 返回模型分配摘要（供日志使用）。
func (ms *ModelSet) Summary() string {
	var parts []string
	for role, m := range ms.models {
		provider, name := m.Current()
		parts = append(parts, fmt.Sprintf("%s=%s/%s", role, provider, name))
	}
	if len(parts) == 0 {
		provider, name := ms.Default.Current()
		return fmt.Sprintf("default=%s/%s", provider, name)
	}
	provider, name := ms.Default.Current()
	return fmt.Sprintf("default=%s/%s %s", provider, name, strings.Join(parts, " "))
}

// CurrentSelection 返回角色当前生效的 provider/model。
// role 为空或 "default" 时返回默认模型。
func (ms *ModelSet) CurrentSelection(role string) (provider, model string, explicit bool) {
	if role == "" || role == "default" {
		provider, model = ms.Default.Current()
		return provider, model, true
	}
	if sw, ok := ms.models[role]; ok {
		provider, model = sw.Current()
		return provider, model, true
	}
	provider, model = ms.Default.Current()
	return provider, model, false
}

// RegisterProvider updates the ModelSet config snapshot so a later Swap can
// instantiate provider/model pairs added while the TUI is already running.
func (ms *ModelSet) RegisterProvider(provider string, pc ProviderConfig) {
	if ms.config.Providers == nil {
		ms.config.Providers = make(map[string]ProviderConfig)
	}
	ms.config.Providers[provider] = pc
}

// ApplyReasoningConfig refreshes per-route defaults without rebuilding provider
// clients. Route-specific values override provider/model defaults at call time.
func (ms *ModelSet) ApplyReasoningConfig(cfg Config) {
	ms.config = cfg
	ms.Default.SetThinking(cfg.ResolveReasoningEffort("default"))
	for role, model := range ms.models {
		model.SetThinking(cfg.ResolveReasoningEffort(role))
	}
}

// ApplyConfig refreshes the ModelSet after provider/model routes are removed
// or otherwise changed outside Swap. Existing role wrappers are swapped to the
// new target where possible so already-built agents stop using deleted models.
func (ms *ModelSet) ApplyConfig(cfg Config) error {
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return err
	}

	cache := make(map[string]agentcore.ChatModel)
	defaultModel, err := createModelFromConfig(cfg, cfg.Provider, cfg.ModelName, cfg.DefaultProviderConfig(), cache)
	if err != nil {
		return fmt.Errorf("default model: %w", err)
	}

	nextModels := make(map[string]*SwappableModel)
	nextFallbacks := make(map[string][]modelTarget)
	for role, rc := range cfg.Roles {
		if rc.Provider == "" && rc.Model == "" {
			continue
		}
		pc, ok := cfg.Providers[rc.Provider]
		if !ok {
			return fmt.Errorf("role %s references unknown provider %q: %w", role, rc.Provider, errs.ErrConfig)
		}
		model, err := createModelFromConfig(cfg, rc.Provider, rc.Model, pc, cache)
		if err != nil {
			return fmt.Errorf("role %s model: %w", role, err)
		}
		if existing, ok := ms.models[role]; ok {
			existing.Swap(rc.Provider, rc.Model, model)
			existing.SetThinking(cfg.ResolveReasoningEffort(role))
			nextModels[role] = existing
		} else {
			nextModels[role] = NewSwappableModel(rc.Provider, rc.Model, model)
			nextModels[role].SetThinking(cfg.ResolveReasoningEffort(role))
		}
		targets, err := buildFallbackTargets(role, rc.Fallbacks, cfg, cache)
		if err != nil {
			return err
		}
		if len(targets) > 0 {
			nextFallbacks[role] = targets
		}
	}
	for role, existing := range ms.models {
		if _, ok := nextModels[role]; ok {
			continue
		}
		existing.Swap(cfg.Provider, cfg.ModelName, defaultModel)
	}
	ms.Default.Swap(cfg.Provider, cfg.ModelName, defaultModel)
	ensureInheritedStageModels(nextModels, ms.models, ms.Default)
	ms.models = nextModels
	ms.fallbacks = nextFallbacks
	ms.ApplyReasoningConfig(cfg)
	return nil
}

// Swap 切换默认模型或指定角色模型。
// role 为空或 "default" 时切换默认模型；其他角色切换为显式覆盖。
func (ms *ModelSet) Swap(role, provider, model string) error {
	pc, ok := ms.config.Providers[provider]
	if !ok {
		return fmt.Errorf("provider %q is not configured: %w", provider, errs.ErrConfig)
	}
	next, err := createModelFromConfig(ms.config, provider, model, pc, make(map[string]agentcore.ChatModel))
	if err != nil {
		return fmt.Errorf("切换模型失败: %w", err)
	}

	if role == "" || role == "default" {
		ms.Default.Swap(provider, model, next)
		ms.config.Provider = provider
		ms.config.ModelName = model
		ms.config.RememberModelCandidate(provider, model)
		ms.refreshInheritedStageModels()
		ms.ApplyReasoningConfig(ms.config)
		return nil
	}

	if !knownRoles[role] {
		return fmt.Errorf("unknown role %q: %w", role, errs.ErrConfig)
	}

	if existing, ok := ms.models[role]; ok {
		existing.Swap(provider, model, next)
		if ms.config.Roles == nil {
			ms.config.Roles = make(map[string]RoleConfig)
		}
		rc := ms.config.Roles[role]
		rc.Provider = provider
		rc.Model = model
		ms.config.Roles[role] = rc
		ms.config.RememberModelCandidate(provider, model)
		ms.refreshInheritedStageModels()
		ms.ApplyReasoningConfig(ms.config)
		return nil
	}
	ms.models[role] = NewSwappableModel(provider, model, next)
	if ms.config.Roles == nil {
		ms.config.Roles = make(map[string]RoleConfig)
	}
	rc := ms.config.Roles[role]
	rc.Provider = provider
	rc.Model = model
	ms.config.Roles[role] = rc
	ms.config.RememberModelCandidate(provider, model)
	ms.refreshInheritedStageModels()
	ms.ApplyReasoningConfig(ms.config)
	return nil
}

// ModelName 从 ChatModel 中提取当前模型名，失败返回空字符串。
// 支持 SwappableModel 的热切换：调用时总是返回最新值。
func ModelName(m agentcore.ChatModel) string {
	if info, ok := m.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info().Name
	}
	return ""
}

// NewModelSet 根据配置创建多模型集合。
// 相同 provider+model 组合复用同一个实例。
func NewModelSet(cfg Config) (*ModelSet, error) {
	cache := make(map[string]agentcore.ChatModel)

	// 创建默认模型
	defaultPC := cfg.DefaultProviderConfig()
	defaultModel, err := createModelFromConfig(cfg, cfg.Provider, cfg.ModelName, defaultPC, cache)
	if err != nil {
		return nil, fmt.Errorf("default model: %w", err)
	}

	ms := &ModelSet{
		Default:   NewSwappableModel(cfg.Provider, cfg.ModelName, defaultModel),
		models:    make(map[string]*SwappableModel),
		fallbacks: make(map[string][]modelTarget),
		config:    cfg,
	}
	ms.Default.SetThinking(cfg.ResolveReasoningEffort("default"))

	// 创建角色覆盖模型
	for role, rc := range cfg.Roles {
		if rc.Provider == "" && rc.Model == "" {
			continue
		}
		pc, ok := cfg.Providers[rc.Provider]
		if !ok {
			return nil, fmt.Errorf("role %s references unknown provider %q: %w", role, rc.Provider, errs.ErrConfig)
		}
		m, err := createModelFromConfig(cfg, rc.Provider, rc.Model, pc, cache)
		if err != nil {
			return nil, fmt.Errorf("role %s model: %w", role, err)
		}
		ms.models[role] = NewSwappableModel(rc.Provider, rc.Model, m)
		ms.models[role].SetThinking(cfg.ResolveReasoningEffort(role))
		slog.Info("角色模型分配", "module", "config", "role", role, "provider", rc.Provider, "model", rc.Model)
		targets, err := buildFallbackTargets(role, rc.Fallbacks, cfg, cache)
		if err != nil {
			return nil, err
		}
		ms.fallbacks[role] = targets
	}
	ensureInheritedStageModels(ms.models, nil, ms.Default)
	ms.ApplyReasoningConfig(cfg)

	return ms, nil
}

func ensureInheritedStageModels(models, existing map[string]*SwappableModel, defaultModel *SwappableModel) {
	for _, stage := range KnownModelStages {
		key := StageRouteKey(stage)
		if _, ok := models[key]; ok {
			continue
		}
		fallback := defaultModel
		if roleModel, ok := models[StageFallbackRole(stage)]; ok {
			fallback = roleModel
		}
		provider, model := fallback.Current()
		if current, ok := existing[key]; ok {
			current.Swap(provider, model, fallback)
			models[key] = current
			continue
		}
		models[key] = NewSwappableModel(provider, model, fallback)
	}
}

func (ms *ModelSet) refreshInheritedStageModels() {
	for _, stage := range KnownModelStages {
		key := StageRouteKey(stage)
		if _, explicit := ms.config.Roles[key]; explicit {
			continue
		}
		fallback := ms.Default
		if roleModel, ok := ms.models[StageFallbackRole(stage)]; ok {
			fallback = roleModel
		}
		provider, model := fallback.Current()
		if stageModel, ok := ms.models[key]; ok {
			stageModel.Swap(provider, model, fallback)
			continue
		}
		ms.models[key] = NewSwappableModel(provider, model, fallback)
	}
}

func buildFallbackTargets(role string, fallbacks []ModelRef, cfg Config, cache map[string]agentcore.ChatModel) ([]modelTarget, error) {
	if len(fallbacks) == 0 {
		return nil, nil
	}
	targets := make([]modelTarget, 0, len(fallbacks))
	for _, fallback := range fallbacks {
		fpc, ok := cfg.Providers[fallback.Provider]
		if !ok {
			return nil, fmt.Errorf("role %s fallback references unknown provider %q: %w", role, fallback.Provider, errs.ErrConfig)
		}
		fm, err := createModelFromConfig(cfg, fallback.Provider, fallback.Model, fpc, cache)
		if err != nil {
			return nil, fmt.Errorf("role %s fallback %s/%s: %w", role, fallback.Provider, fallback.Model, err)
		}
		targets = append(targets, modelTarget{
			provider: fallback.Provider,
			name:     fallback.Model,
			model:    fm,
		})
	}
	return targets, nil
}

// NewProviderModel creates one provider/model instance without mutating a ModelSet.
func NewProviderModel(providerKey, model string, pc ProviderConfig) (agentcore.ChatModel, error) {
	return NewProviderModelWithConfig(Config{}, providerKey, model, pc)
}

// NewProviderModelWithConfig creates one provider/model instance with runtime config.
func NewProviderModelWithConfig(cfg Config, providerKey, model string, pc ProviderConfig) (agentcore.ChatModel, error) {
	return createModelFromConfig(cfg, providerKey, model, pc, make(map[string]agentcore.ChatModel))
}

// createModelFromConfig 创建或复用 ChatModel 实例。
func createModelFromConfig(cfg Config, providerKey, model string, pc ProviderConfig, cache map[string]agentcore.ChatModel) (agentcore.ChatModel, error) {
	cacheKey := providerKey + "|" + model
	if m, ok := cache[cacheKey]; ok {
		return m, nil
	}

	providerType, err := pc.ProviderType(providerKey)
	if err != nil {
		return nil, fmt.Errorf("解析 provider 类型失败: %w", err)
	}
	if pc.UsesCodexAuth() {
		if strings.ToLower(strings.TrimSpace(providerType)) != "openai" {
			return nil, fmt.Errorf("provider %s auth %q requires openai type: %w", providerKey, pc.Auth, errs.ErrConfig)
		}
		m, err := createCodexAuthModel(cfg, providerKey, model, pc)
		if err != nil {
			return nil, err
		}
		cache[cacheKey] = m
		return m, nil
	}
	if strings.EqualFold(strings.TrimSpace(pc.Auth), ProviderAuthGrokOAuth) {
		if strings.ToLower(strings.TrimSpace(providerType)) != "grok" {
			return nil, fmt.Errorf("provider %s auth %q requires grok type: %w", providerKey, pc.Auth, errs.ErrConfig)
		}
		m, err := createGrokOAuthModel(cfg, providerKey, model, pc)
		if err != nil {
			return nil, err
		}
		cache[cacheKey] = m
		return m, nil
	}
	if m, handled, err := newProviderModelWithRuntimeOptions(cfg, providerKey, model, pc); handled {
		if err != nil {
			return nil, err
		}
		cache[cacheKey] = m
		return m, nil
	}
	providerExtra := cloneMap(pc.Extra)
	if pc.API != "" {
		if providerExtra == nil {
			providerExtra = make(map[string]any, 1)
		}
		providerExtra["api"] = pc.API
	}

	m, err := llm.NewModel(providerType, model,
		llm.WithAPIKey(pc.APIKey),
		llm.WithBaseURL(pc.BaseURL),
		llm.WithRequestTimeout(ProviderRequestTimeout(pc)),
		llm.WithStreamIdleTimeout(streamIdleTimeout),
		llm.WithProviderExtra(providerExtra),
		llm.WithExtra(pc.ExtraBody),
	)
	if err != nil {
		return nil, fmt.Errorf("provider %s (%s): %w: %w", providerKey, providerType, errs.ErrProvider, err)
	}
	wrapped := globalprompt.WrapModel(m)
	cache[cacheKey] = wrapped
	return wrapped, nil
}

func createGrokOAuthModel(cfg Config, providerKey, model string, pc ProviderConfig) (agentcore.ChatModel, error) {
	transport, _, err := ProviderTransport(cfg, providerKey, model, pc)
	if err != nil {
		return nil, err
	}
	provider, err := newGrokOAuthProviderWithTransport(cfg, providerKey, model, pc, transport)
	if err != nil {
		return nil, fmt.Errorf("provider %s (grok oauth): %w: %w", providerKey, errs.ErrProvider, err)
	}
	client, err := litellm.New(provider, litellm.WithStreamIdleTimeout(streamIdleTimeout))
	if err != nil {
		return nil, fmt.Errorf("provider %s (grok oauth): %w: %w", providerKey, errs.ErrProvider, err)
	}
	wrapped := globalprompt.WrapModel(llm.NewLiteLLMAdapter(model, client))
	if timeout := ProviderRequestTimeout(pc); timeout > 0 {
		return &requestTimeoutModel{model: wrapped, timeout: timeout}, nil
	}
	return wrapped, nil
}

func headersFromProviderExtra(extra map[string]any) (map[string]string, error) {
	raw, ok := extra["headers"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch headers := raw.(type) {
	case map[string]string:
		out := make(map[string]string, len(headers))
		for key, value := range headers {
			out[key] = value
		}
		return out, nil
	case map[string]any:
		out := make(map[string]string, len(headers))
		for key, value := range headers {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("%q must be a string", key)
			}
			out[key] = text
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an object")
	}
}

func stringFromProviderExtra(extra map[string]any, key string) string {
	value, ok := extra[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

type failoverModel struct {
	role      string
	primary   *SwappableModel
	fallbacks []modelTarget
	report    FailoverReporter
}

func (m *failoverModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	current := m.currentTarget()
	resp, err := current.model.Generate(ctx, messages, tools, opts...)
	if err == nil {
		return resp, nil
	}

	next, reason, ok := m.pickFallback(current, err)
	if !ok {
		return nil, err
	}
	m.switchTo(current, next, reason, err)
	return next.model.Generate(ctx, messages, tools, opts...)
}

func (m *failoverModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	out := make(chan agentcore.StreamEvent, 100)

	go func() {
		defer close(out)

		current := m.currentTarget()
		fallbackUsed := false

	retry:
		source, resp, err := m.startAttempt(ctx, current, messages, tools, opts...)
		if err != nil {
			if !fallbackUsed {
				if next, reason, ok := m.pickFallback(current, err); ok {
					fallbackUsed = true
					m.switchTo(current, next, reason, err)
					current = next
					goto retry
				}
			}
			out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
			return
		}
		if resp != nil {
			out <- agentcore.StreamEvent{
				Type:       agentcore.StreamEventDone,
				Message:    resp.Message,
				StopReason: resp.Message.StopReason,
			}
			return
		}

		forwarded := false
		for ev := range source {
			switch ev.Type {
			case agentcore.StreamEventError:
				if ev.Err != nil && !forwarded && !fallbackUsed {
					if next, reason, ok := m.pickFallback(current, ev.Err); ok {
						fallbackUsed = true
						m.switchTo(current, next, reason, ev.Err)
						current = next
						goto retry
					}
				}
				out <- ev
				return
			case agentcore.StreamEventDone:
				out <- ev
				return
			default:
				forwarded = true
				out <- ev
			}
		}
	}()

	return out, nil
}

func (m *failoverModel) SupportsTools() bool {
	return m.primary != nil && m.primary.SupportsTools()
}

func (m *failoverModel) ProviderName() string {
	if m.primary == nil {
		return ""
	}
	return m.primary.ProviderName()
}

func (m *failoverModel) Info() llm.ModelInfo {
	if m.primary == nil {
		return llm.ModelInfo{}
	}
	return m.primary.Info()
}

func (m *failoverModel) currentTarget() modelTarget {
	if m.primary == nil {
		return modelTarget{}
	}
	provider, name := m.primary.Current()
	return modelTarget{
		provider: provider,
		name:     name,
		model:    m.primary,
	}
}

func (m *failoverModel) pickFallback(current modelTarget, err error) (modelTarget, string, bool) {
	if err == nil || current.model == nil {
		return modelTarget{}, "", false
	}
	if errors.Is(err, context.Canceled) {
		return modelTarget{}, "", false
	}

	decision := classifyRuntimeFallbackError(err)
	if !decision.eligible {
		return modelTarget{}, decision.reason, false
	}
	reason := decision.reason
	for _, target := range m.fallbacks {
		if target.provider == current.provider && target.name == current.name {
			continue
		}
		if target.model == nil {
			continue
		}
		return target, reason, true
	}
	return modelTarget{}, reason, false
}

func (m *failoverModel) reportFailover(from, to modelTarget, reason string, err error) {
	if m.report != nil {
		m.report(FailoverEvent{
			Role:         m.role,
			Reason:       reason,
			FromProvider: from.provider,
			FromModel:    from.name,
			ToProvider:   to.provider,
			ToModel:      to.name,
			Err:          err,
		})
	}
}

func (m *failoverModel) switchTo(from, to modelTarget, reason string, err error) {
	m.primary.Swap(to.provider, to.name, to.model)
	m.reportFailover(from, to, reason, err)
}

func (m *failoverModel) startAttempt(ctx context.Context, target modelTarget, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, *agentcore.LLMResponse, error) {
	if target.model == nil {
		return nil, nil, fmt.Errorf("no model configured")
	}

	streamCh, err := target.model.GenerateStream(ctx, messages, tools, opts...)
	if err == nil {
		return streamCh, nil, nil
	}

	resp, genErr := target.model.Generate(ctx, messages, tools, opts...)
	if genErr != nil {
		return nil, nil, genErr
	}
	return nil, resp, nil
}
