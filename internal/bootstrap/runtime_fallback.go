package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/litellm"
)

const RuntimeFallbackPoolReasonPrefix = "runtime_fallback_pool"
const transientInvalidArgumentMaxAttempts = 5

type RuntimeFallbackTarget struct {
	Provider string
	Model    string
	LLM      agentcore.ChatModel
	Reason   string
}

type RuntimeFallbackController interface {
	SelectRuntimeFallback(ctx context.Context, current ModelRef, attempted map[string]bool, err error) (RuntimeFallbackTarget, bool)
}

type RuntimeFallbackControllerFunc func(context.Context, ModelRef, map[string]bool, error) (RuntimeFallbackTarget, bool)

func (f RuntimeFallbackControllerFunc) SelectRuntimeFallback(ctx context.Context, current ModelRef, attempted map[string]bool, err error) (RuntimeFallbackTarget, bool) {
	return f(ctx, current, attempted, err)
}

var (
	runtimeFallbackDelay = retrypolicy.Delay
	runtimeFallbackWait  = retrypolicy.Wait
)

func RuntimeAutoSwitchCandidateProviders(cfg Config, currentProvider string, attempted map[string]bool) []string {
	if !cfg.ModelAutoSwitch.IsEnabled() {
		return nil
	}
	currentProvider = strings.TrimSpace(currentProvider)
	out := make([]string, 0, len(cfg.ModelAutoSwitch.FallbackBackends))
	seen := make(map[string]bool, len(cfg.ModelAutoSwitch.FallbackBackends))
	for _, raw := range cfg.ModelAutoSwitch.FallbackBackends {
		provider := strings.TrimSpace(raw)
		if provider == "" || seen[provider] || provider == currentProvider || attempted[provider] {
			continue
		}
		seen[provider] = true
		pc, ok := cfg.Providers[provider]
		if !ok || pc.Disabled {
			continue
		}
		if pc.RequiresAPIKey(provider) && strings.TrimSpace(pc.APIKey) == "" {
			continue
		}
		if len(cfg.CandidateModels(provider)) == 0 {
			continue
		}
		out = append(out, provider)
	}
	return out
}

type runtimeFallbackModel struct {
	role        string
	primary     *SwappableModel
	model       agentcore.ChatModel
	controller  RuntimeFallbackController
	report      FailoverReporter
	maxAttempts int
}

func newRuntimeFallbackModel(role string, primary *SwappableModel, model agentcore.ChatModel, cfg ModelAutoSwitchConfig, controller RuntimeFallbackController, report FailoverReporter) agentcore.ChatModel {
	if model == nil {
		return primary
	}
	if primary == nil || controller == nil || !cfg.IsEnabled() {
		return model
	}
	return &runtimeFallbackModel{
		role:        role,
		primary:     primary,
		model:       model,
		controller:  controller,
		report:      report,
		maxAttempts: cfg.EffectiveNetworkMaxAttempts(),
	}
}

func (m *runtimeFallbackModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	attempted := make(map[string]bool)
	networkAttempt := 0
	for {
		current := m.currentTarget()
		if current.model == nil {
			return nil, agentcore.ErrNoModel
		}
		attempted[current.provider] = true
		resp, err := current.model.Generate(ctx, messages, tools, opts...)
		if err == nil {
			return resp, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		next, retry, terminal := m.nextAfterFailure(ctx, current, attempted, err, &networkAttempt)
		switch {
		case retry:
			continue
		case next.model != nil:
			m.swapTo(current, next, terminal.reason, err)
			networkAttempt = 0
			continue
		default:
			return nil, terminal.wrap(err)
		}
	}
}

func (m *runtimeFallbackModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	out := make(chan agentcore.StreamEvent, 100)
	go func() {
		defer close(out)
		attempted := make(map[string]bool)
		networkAttempt := 0
		for {
			current := m.currentTarget()
			if current.model == nil {
				out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: agentcore.ErrNoModel}
				return
			}
			attempted[current.provider] = true
			source, resp, err := m.startAttempt(ctx, current, messages, tools, opts...)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: ctxErr}
					return
				}
				next, retry, terminal := m.nextAfterFailure(ctx, current, attempted, err, &networkAttempt)
				switch {
				case retry:
					continue
				case next.model != nil:
					m.swapTo(current, next, terminal.reason, err)
					networkAttempt = 0
					continue
				default:
					out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: terminal.wrap(err)}
					return
				}
			}
			if resp != nil {
				out <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message, StopReason: resp.Message.StopReason}
				return
			}
			if m.forwardStream(ctx, out, source, current, attempted, &networkAttempt) {
				return
			}
		}
	}()
	return out, nil
}

func (m *runtimeFallbackModel) SupportsTools() bool {
	return m.model != nil && m.model.SupportsTools()
}

func (m *runtimeFallbackModel) ProviderName() string {
	if m.primary == nil {
		return ""
	}
	return m.primary.ProviderName()
}

func (m *runtimeFallbackModel) Info() llm.ModelInfo {
	if m.primary == nil {
		return llm.ModelInfo{}
	}
	return m.primary.Info()
}

func (m *runtimeFallbackModel) forwardStream(ctx context.Context, out chan<- agentcore.StreamEvent, source <-chan agentcore.StreamEvent, current modelTarget, attempted map[string]bool, networkAttempt *int) bool {
	forwarded := false
	pendingPrelude := make([]agentcore.StreamEvent, 0, 2)
	for ev := range source {
		switch ev.Type {
		case agentcore.StreamEventError:
			if ctxErr := ctx.Err(); ctxErr != nil {
				out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: ctxErr}
				return true
			}
			if ev.Err != nil && !forwarded {
				next, retry, terminal := m.nextAfterFailure(ctx, current, attempted, ev.Err, networkAttempt)
				switch {
				case retry:
					return false
				case next.model != nil:
					m.swapTo(current, next, terminal.reason, ev.Err)
					*networkAttempt = 0
					return false
				default:
					out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: terminal.wrap(ev.Err)}
					return true
				}
			}
			out <- ev
			return true
		case agentcore.StreamEventDone:
			for _, pending := range pendingPrelude {
				out <- pending
			}
			out <- ev
			return true
		default:
			if !forwarded && !streamEventHasMaterialOutput(ev) {
				pendingPrelude = append(pendingPrelude, ev)
				continue
			}
			for _, pending := range pendingPrelude {
				out <- pending
			}
			pendingPrelude = nil
			forwarded = true
			out <- ev
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: ctxErr}
		return true
	}
	err := &agentcore.PartialStreamError{}
	if !forwarded {
		next, retry, terminal := m.nextAfterFailure(ctx, current, attempted, err, networkAttempt)
		switch {
		case retry:
			return false
		case next.model != nil:
			m.swapTo(current, next, terminal.reason, err)
			*networkAttempt = 0
			return false
		default:
			out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: terminal.wrap(err)}
			return true
		}
	}
	out <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: runtimeFallbackTerminalError{err: err}}
	return true
}

func streamEventHasMaterialOutput(event agentcore.StreamEvent) bool {
	switch event.Type {
	case agentcore.StreamEventTextStart,
		agentcore.StreamEventTextEnd,
		agentcore.StreamEventThinkingStart,
		agentcore.StreamEventThinkingEnd:
		return false
	case agentcore.StreamEventTextDelta,
		agentcore.StreamEventThinkingDelta,
		agentcore.StreamEventToolCallDelta:
		return event.Delta != ""
	case agentcore.StreamEventToolCallStart,
		agentcore.StreamEventToolCallEnd:
		return true
	default:
		return true
	}
}

func (m *runtimeFallbackModel) nextAfterFailure(ctx context.Context, current modelTarget, attempted map[string]bool, err error, networkAttempt *int) (modelTarget, bool, runtimeFallbackDecision) {
	decision := classifyRuntimeFallbackError(err)
	if !decision.eligible {
		return modelTarget{}, false, decision
	}
	retryLimit := m.maxAttempts
	if decision.sameBackendMaxAttempts > 0 && retryLimit > decision.sameBackendMaxAttempts {
		retryLimit = decision.sameBackendMaxAttempts
	}
	if decision.networkRetry && *networkAttempt < retryLimit-1 {
		*networkAttempt++
		if waitErr := runtimeFallbackWait(ctx, runtimeFallbackDelay(*networkAttempt)); waitErr != nil {
			return modelTarget{}, false, runtimeFallbackDecision{}
		}
		return modelTarget{}, true, decision
	}
	if m.controller == nil {
		return modelTarget{}, false, decision
	}
	target, ok := m.controller.SelectRuntimeFallback(ctx, ModelRef{Provider: current.provider, Model: current.name}, cloneAttemptedProviders(attempted), err)
	if !ok || target.LLM == nil || target.Provider == "" || target.Model == "" || !sameRuntimeModelName(current.name, target.Model) {
		return modelTarget{}, false, decision
	}
	reason := strings.TrimSpace(target.Reason)
	if reason == "" {
		reason = fmt.Sprintf("%s:%s->%s", RuntimeFallbackPoolReasonPrefix, current.provider, target.Provider)
	}
	decision.reason = reason
	return modelTarget{provider: target.Provider, name: target.Model, model: target.LLM}, false, decision
}

func sameRuntimeModelName(current, candidate string) bool {
	return strings.EqualFold(strings.TrimSpace(current), strings.TrimSpace(candidate))
}

func (m *runtimeFallbackModel) swapTo(from, to modelTarget, reason string, err error) {
	m.primary.Swap(to.provider, to.name, to.model)
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

func (m *runtimeFallbackModel) currentTarget() modelTarget {
	if m.primary == nil {
		return modelTarget{}
	}
	provider, name := m.primary.Current()
	return modelTarget{provider: provider, name: name, model: m.model}
}

func (m *runtimeFallbackModel) startAttempt(ctx context.Context, target modelTarget, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, *agentcore.LLMResponse, error) {
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

type runtimeFallbackDecision struct {
	eligible               bool
	networkRetry           bool
	sameBackendMaxAttempts int
	reason                 string
}

func (d runtimeFallbackDecision) wrap(err error) error {
	if !d.eligible {
		return err
	}
	return runtimeFallbackTerminalError{
		err:       err,
		reason:    d.reason,
		retryable: d.retryableAfterFallbackExhaustion(err),
	}
}

func (d runtimeFallbackDecision) retryableAfterFallbackExhaustion(err error) bool {
	switch d.reason {
	case "network_interrupted", "overloaded", "timeout":
		return true
	case "rate_limit":
		return isRuntimeTemporaryRateLimitErrorMessage(err)
	default:
		return false
	}
}

type runtimeFallbackTerminalError struct {
	err       error
	reason    string
	retryable bool
}

func (e runtimeFallbackTerminalError) Error() string {
	if e.err == nil {
		return "runtime fallback exhausted"
	}
	return retrypolicy.SanitizeProviderError(e.err)
}

func (e runtimeFallbackTerminalError) Retryable() bool { return e.retryable }

func (e runtimeFallbackTerminalError) Unwrap() error { return e.err }

func classifyRuntimeFallbackError(err error) runtimeFallbackDecision {
	if err == nil || errors.Is(err, context.Canceled) {
		return runtimeFallbackDecision{}
	}
	var partial *agentcore.PartialStreamError
	if errors.As(err, &partial) || errors.Is(err, agentcore.ErrProviderStreamIdle) {
		return runtimeFallbackDecision{eligible: true, networkRetry: true, reason: "network_interrupted"}
	}
	classified := agentcore.ClassifyProvider(err)
	switch {
	case errors.Is(classified, agentcore.ErrProviderNetwork):
		return runtimeFallbackDecision{eligible: true, networkRetry: true, reason: "network_interrupted"}
	case errors.Is(classified, agentcore.ErrProviderQuota) || isRuntimeQuotaErrorMessage(err):
		return runtimeFallbackDecision{eligible: true, reason: "quota_exhausted"}
	case errors.Is(classified, agentcore.ErrProviderAuth) || isRuntimeAuthErrorMessage(err):
		return runtimeFallbackDecision{eligible: true, reason: "auth_failed"}
	case errors.Is(classified, agentcore.ErrProviderRateLimit) || isRuntimeRateLimitErrorMessage(err):
		return runtimeFallbackDecision{eligible: true, reason: "rate_limit"}
	case retrypolicy.IsProviderGatewayError(err):
		return runtimeFallbackDecision{eligible: true, reason: "overloaded"}
	case errors.Is(classified, agentcore.ErrProviderOverloaded):
		return runtimeFallbackDecision{eligible: true, reason: "overloaded"}
	case errors.Is(classified, agentcore.ErrProviderTimeout):
		return runtimeFallbackDecision{eligible: true, reason: "timeout"}
	case isTransientGatewayInvalidArgument(err):
		return runtimeFallbackDecision{
			eligible:               true,
			networkRetry:           true,
			sameBackendMaxAttempts: transientInvalidArgumentMaxAttempts,
			reason:                 "gateway_invalid_argument",
		}
	default:
		return runtimeFallbackDecision{}
	}
}

func isTransientGatewayInvalidArgument(err error) bool {
	var providerErr *litellm.LiteLLMError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != 400 {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(providerErr.Message))
	return message == "invalid argument" || message == "invalid_argument"
}

func isRuntimeAuthErrorMessage(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no token") ||
		strings.Contains(msg, "missing token") ||
		strings.Contains(msg, "token missing") ||
		strings.Contains(msg, "invalid token") ||
		strings.Contains(msg, "token invalid") ||
		strings.Contains(msg, "invalid bearer token") ||
		strings.Contains(msg, "authorization failed") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "empty token") ||
		strings.Contains(msg, "token required") ||
		strings.Contains(msg, "requires token") ||
		strings.Contains(msg, "without token") ||
		strings.Contains(msg, "no api key") ||
		strings.Contains(msg, "missing api key") ||
		strings.Contains(msg, "invalid api key") ||
		strings.Contains(msg, "incorrect api key") ||
		strings.Contains(msg, "api key missing") ||
		strings.Contains(msg, "api key required") ||
		strings.Contains(msg, "api key is required") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "401") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "未配置token") ||
		strings.Contains(msg, "没有token") ||
		strings.Contains(msg, "无token") ||
		strings.Contains(msg, "缺少token") ||
		strings.Contains(msg, "token为空")
}

func isRuntimeQuotaErrorMessage(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "402") ||
		strings.Contains(msg, "insufficient_user_quota") ||
		strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "quota_exceeded") ||
		strings.Contains(msg, "quota exhausted") ||
		strings.Contains(msg, "credit exhausted") ||
		strings.Contains(msg, "credits exhausted") ||
		strings.Contains(msg, "no credit") ||
		strings.Contains(msg, "balance not enough") ||
		strings.Contains(msg, "insufficient balance") ||
		strings.Contains(msg, "payment required") ||
		strings.Contains(msg, "billing hard limit") ||
		strings.Contains(msg, "usage limit reached") ||
		strings.Contains(msg, "usage limit exceeded") ||
		strings.Contains(msg, "usage limits exceeded") ||
		strings.Contains(msg, "reached your usage limit") ||
		strings.Contains(msg, "reached the usage limit") ||
		strings.Contains(msg, "monthly usage limit") ||
		strings.Contains(msg, "weekly usage limit") ||
		strings.Contains(msg, "daily usage limit")
}

func isRuntimeRateLimitErrorMessage(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate-limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "request limit") ||
		strings.Contains(msg, "requests limit") ||
		strings.Contains(msg, "rpm limit") ||
		strings.Contains(msg, "tpm limit")
}

func isRuntimeTemporaryRateLimitErrorMessage(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rate_limit_exceeded") ||
		strings.Contains(msg, "insufficient_user_quota") ||
		strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "quota_exceeded") ||
		strings.Contains(msg, "quota exhausted") ||
		strings.Contains(msg, "usage limit reached") ||
		strings.Contains(msg, "usage limit exceeded") ||
		strings.Contains(msg, "monthly usage limit") ||
		strings.Contains(msg, "balance not enough") ||
		strings.Contains(msg, "insufficient balance") {
		return false
	}
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate-limit")
}

func cloneAttemptedProviders(attempted map[string]bool) map[string]bool {
	if len(attempted) == 0 {
		return nil
	}
	out := make(map[string]bool, len(attempted))
	for key, value := range attempted {
		out[key] = value
	}
	return out
}
