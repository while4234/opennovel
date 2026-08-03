package agents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/ainovel-cli/internal/store"
)

const summaryMaxAttempts = 3

type SummaryRetryHook func(agent string, retry, maxRetries int, delay time.Duration)

// contextManagerConfig 聚合 ContextManager 的全部配置参数。
type contextManagerConfig struct {
	Model            agentcore.ChatModel
	Store            *store.Store
	ContextWindow    int
	ReserveTokens    int
	KeepRecentTokens int
	Agent            string
	CommitOnProject  bool
	Summary          *corecontext.FullSummaryConfig
	ToolMicrocompact *corecontext.ToolResultMicrocompactConfig
	ExtraStrategies  []corecontext.Strategy
	OnSummaryRetry   SummaryRetryHook
}

// forceToolResultMicrocompact adapts agentcore's lossless tool-result
// microcompactor for byte-boundary overflow recovery. Agentcore's stock
// strategy is threshold-only; production also has a stricter compiled-byte
// boundary that can be crossed before token estimates request compaction.
type forceToolResultMicrocompact struct {
	strategy *corecontext.ToolResultMicrocompactStrategy
}

// coordinatorLatestHostTurnCheckpoint is the Coordinator's overflow recovery
// boundary. Every Host instruction is derived from durable workflow state, so
// older dispatch instructions and their subagent replies are superseded once a
// newer Host turn exists. Keeping only the latest turn preserves the exact
// current repair/audit task while preventing persisted audit JSON from being
// repeated across every subsequent dispatch.
type coordinatorLatestHostTurnCheckpoint struct{}

func (coordinatorLatestHostTurnCheckpoint) Name() string {
	return "coordinator_latest_host_turn"
}

func (s coordinatorLatestHostTurnCheckpoint) Apply(
	_ context.Context,
	_ []agentcore.AgentMessage,
	view []agentcore.AgentMessage,
	_ corecontext.Budget,
) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	return view, corecontext.StrategyResult{Name: s.Name()}, nil
}

func (s coordinatorLatestHostTurnCheckpoint) ForceApply(
	_ context.Context,
	_ []agentcore.AgentMessage,
	view []agentcore.AgentMessage,
	_ corecontext.Budget,
) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	for index := len(view) - 1; index >= 0; index-- {
		message, ok := view[index].(agentcore.Message)
		if !ok || message.Role != agentcore.RoleUser {
			continue
		}
		if index == 0 {
			return view, corecontext.StrategyResult{Name: s.Name()}, nil
		}
		next := append([]agentcore.AgentMessage(nil), view[index:]...)
		return next, corecontext.StrategyResult{
			Applied:     true,
			TokensSaved: corecontext.EstimateTotal(view) - corecontext.EstimateTotal(next),
			Name:        s.Name(),
		}, nil
	}
	return view, corecontext.StrategyResult{Name: s.Name()}, nil
}

type latestHostTurnContextManager struct {
	*corecontext.ContextEngine
	checkpoint coordinatorLatestHostTurnCheckpoint
}

func newLatestHostTurnContextManager(engine *corecontext.ContextEngine) *latestHostTurnContextManager {
	return &latestHostTurnContextManager{
		ContextEngine: engine,
		checkpoint:    coordinatorLatestHostTurnCheckpoint{},
	}
}

func (m *latestHostTurnContextManager) Project(
	ctx context.Context,
	messages []agentcore.AgentMessage,
) (agentcore.ContextProjection, error) {
	view, result, err := m.checkpoint.ForceApply(ctx, messages, messages, corecontext.Budget{})
	if err != nil {
		return agentcore.ContextProjection{}, err
	}
	if !result.Applied {
		return m.ContextEngine.Project(ctx, messages)
	}
	projection, err := m.ContextEngine.Project(ctx, view)
	if err != nil {
		return agentcore.ContextProjection{}, err
	}
	if projection.Messages == nil {
		projection.Messages = view
	}
	projection.ShouldCommit = true
	projection.CommitMessages = projection.Messages
	return projection, nil
}

func newForceToolResultMicrocompact(
	cfg corecontext.ToolResultMicrocompactConfig,
) *forceToolResultMicrocompact {
	return &forceToolResultMicrocompact{
		strategy: corecontext.NewToolResultMicrocompact(cfg),
	}
}

func (s *forceToolResultMicrocompact) Name() string {
	return "force_tool_result_microcompact"
}

func (s *forceToolResultMicrocompact) Apply(
	ctx context.Context,
	transcript []agentcore.AgentMessage,
	view []agentcore.AgentMessage,
	budget corecontext.Budget,
) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	return s.compact(ctx, transcript, view, budget)
}

func (s *forceToolResultMicrocompact) ForceApply(
	ctx context.Context,
	transcript []agentcore.AgentMessage,
	view []agentcore.AgentMessage,
	budget corecontext.Budget,
) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	return s.compact(ctx, transcript, view, budget)
}

func (s *forceToolResultMicrocompact) compact(
	ctx context.Context,
	transcript []agentcore.AgentMessage,
	view []agentcore.AgentMessage,
	budget corecontext.Budget,
) ([]agentcore.AgentMessage, corecontext.StrategyResult, error) {
	next, result, err := s.strategy.Apply(ctx, transcript, view, budget)
	result.Name = s.Name()
	return next, result, err
}

func newContextManager(cfg contextManagerConfig) *corecontext.ContextEngine {
	var sc corecontext.FullSummaryConfig
	if cfg.Summary != nil {
		sc = *cfg.Summary
	}
	sc.Model = summaryCompatibleModelWithStore(cfg.Model, cfg.Store, cfg.Agent, cfg.OnSummaryRetry)
	if sc.KeepRecentTokens <= 0 {
		sc.KeepRecentTokens = cfg.KeepRecentTokens
	}

	var tc corecontext.ToolResultMicrocompactConfig
	if cfg.ToolMicrocompact != nil {
		tc = *cfg.ToolMicrocompact
	}

	strategies := []corecontext.Strategy{
		corecontext.NewToolResultMicrocompact(tc),
		corecontext.NewLightTrim(corecontext.LightTrimConfig{}),
	}
	strategies = append(strategies, cfg.ExtraStrategies...)
	strategies = append(strategies, corecontext.NewFullSummary(sc))

	engine := corecontext.NewEngine(corecontext.EngineConfig{
		ContextWindow:   cfg.ContextWindow,
		ReserveTokens:   cfg.ReserveTokens,
		CommitOnProject: cfg.CommitOnProject,
		Strategies:      strategies,
	})

	callback := contextRewriteCallback(cfg.Agent)
	engine.SetProjectHook(callback)
	engine.SetRecoverHook(callback)
	return engine
}

// summaryCompatibleModel keeps summary calls provider-neutral. Agentcore asks
// summaries to disable reasoning, but some OpenAI-compatible DeepSeek backends
// reject an explicit thinking=off field even though they accept the same call
// when the field is omitted. Appending ThinkingAuto restores the former,
// provider-default behavior while retaining all other call options.
func summaryCompatibleModel(model agentcore.ChatModel, agent string, onRetry SummaryRetryHook) agentcore.ChatModel {
	return summaryCompatibleModelWithStore(model, nil, agent, onRetry)
}

func summaryCompatibleModelWithStore(model agentcore.ChatModel, diagnosticStore *store.Store, agent string, onRetry SummaryRetryHook) agentcore.ChatModel {
	if model == nil {
		return nil
	}
	return &summaryModel{
		ChatModel: unwrapProductionAgentBoundary(model),
		store:     diagnosticStore,
		agent:     agent,
		onRetry:   onRetry,
		delay:     retrypolicy.Delay,
		wait:      retrypolicy.Wait,
	}
}

type summaryModel struct {
	agentcore.ChatModel
	store   *store.Store
	agent   string
	onRetry SummaryRetryHook
	delay   func(int) time.Duration
	wait    func(context.Context, time.Duration) error
}

func (m *summaryModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	opts = append(opts, agentcore.WithThinking(agentcore.ThinkingAuto))
	var lastResponse *agentcore.LLMResponse
	for attempt := 1; attempt <= summaryMaxAttempts; attempt++ {
		system, user := summaryDiagnosticInput(messages)
		recorder, beginErr := modeldiag.Begin(modeldiag.Request{Store: m.store, Task: "agent_context_summary", Batch: attempt, System: system, User: user, InputLimitBytes: 60 * 1024})
		if beginErr != nil {
			return nil, beginErr
		}
		response, err := m.ChatModel.Generate(ctx, messages, tools, opts...)
		if err != nil {
			_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
			return nil, err
		}
		lastResponse = response
		if summaryResponseHasContent(response) {
			if diagnosticErr := recorder.Finish(modeldiag.StatusCompleted, response.Message.TextContent(), response.Message.Usage); diagnosticErr != nil {
				return nil, diagnosticErr
			}
			return response, nil
		}
		if response == nil {
			_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
		} else {
			_ = recorder.Finish(modeldiag.StatusEmptyResponse, response.Message.TextContent(), response.Message.Usage)
		}
		if attempt == summaryMaxAttempts {
			break
		}

		retry := attempt
		delay := m.delay(retry)
		if m.onRetry != nil {
			m.onRetry(m.agent, retry, summaryMaxAttempts-1, delay)
		}
		if err := m.wait(ctx, delay); err != nil {
			return nil, err
		}
	}
	if lastResponse == nil {
		return nil, fmt.Errorf("summary model returned nil response")
	}
	// Return the final empty response so agentcore preserves its established
	// "summarization returned empty content" terminal diagnostic.
	return lastResponse, nil
}

func summaryDiagnosticInput(messages []agentcore.Message) (string, []byte) {
	if len(messages) == 0 {
		return "", nil
	}
	user := strings.Builder{}
	for _, message := range messages[1:] {
		user.WriteString(message.TextContent())
		user.WriteByte('\n')
	}
	return messages[0].TextContent(), []byte(user.String())
}

func (m *summaryModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	system, user := summaryDiagnosticInput(messages)
	recorder, beginErr := modeldiag.Begin(modeldiag.Request{Store: m.store, Task: "agent_context_summary_stream", System: system, User: user, InputLimitBytes: 60 * 1024})
	if beginErr != nil {
		return nil, beginErr
	}
	source, err := m.ChatModel.GenerateStream(ctx, messages, tools, append(opts, agentcore.WithThinking(agentcore.ThinkingAuto))...)
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return nil, err
	}
	output := make(chan agentcore.StreamEvent)
	go func() {
		defer close(output)
		var text strings.Builder
		for event := range source {
			if event.Type == agentcore.StreamEventTextDelta {
				text.WriteString(event.Delta)
			}
			if event.Type == agentcore.StreamEventDone {
				final := text.String()
				if final == "" {
					final = event.Message.TextContent()
				}
				status := modeldiag.StatusCompleted
				if strings.TrimSpace(final) == "" {
					status = modeldiag.StatusEmptyResponse
				}
				_ = recorder.Finish(status, final, event.Message.Usage)
			}
			if event.Type == agentcore.StreamEventError {
				_ = recorder.Finish(modeldiag.StatusProviderError, text.String(), nil)
			}
			select {
			case output <- event:
			case <-ctx.Done():
				_ = recorder.Finish(modeldiag.StatusProviderError, text.String(), nil)
				return
			}
		}
		_ = recorder.Finish(modeldiag.StatusProviderError, text.String(), nil)
	}()
	return output, nil
}

func summaryResponseHasContent(response *agentcore.LLMResponse) bool {
	if response == nil {
		return false
	}
	text := strings.TrimSpace(response.Message.TextContent())
	start := strings.Index(text, "<analysis>")
	end := strings.Index(text, "</analysis>")
	if start >= 0 && end >= start {
		text = strings.TrimSpace(text[:start] + text[end+len("</analysis>"):])
	}
	return text != ""
}

// contextRewriteCallback 创建上下文重写的日志回调。
// 新架构简化为只写 slog,不再写 runtime queue 和 UIEvent。
func contextRewriteCallback(agent string) func(corecontext.RewriteEvent) {
	return func(ev corecontext.RewriteEvent) {
		attrs := []any{
			"module", "context",
			"agent", agent,
			"reason", ev.Reason,
			"strategy", ev.Strategy,
			"committed", ev.Committed,
			"tokens_before", ev.TokensBefore,
			"tokens_after", ev.TokensAfter,
		}
		if info := ev.Info; info != nil {
			attrs = append(attrs,
				"msgs_before", info.MessagesBefore,
				"msgs_after", info.MessagesAfter,
				"compacted", info.CompactedCount,
				"kept", info.KeptCount,
				"duration_ms", info.Duration.Milliseconds(),
			)
		}
		slog.Warn("上下文重写", attrs...)
	}
}
