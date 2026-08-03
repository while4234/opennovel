package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/litellm"
)

func TestRuntimeAutoSwitchQuotaSwitchesImmediately(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{provider: "p1", model: "m1", errs: []error{errors.New("402 insufficient_quota")}}
	second := &scriptedRuntimeModel{provider: "p2", model: "m1"}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{order: []string{"p2"}, models: map[string]agentcore.ChatModel{"p2": second}}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(3), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "p2/m1" {
		t.Fatalf("response = %q, want p2/m1", got)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
	if len(controller.calls) != 1 {
		t.Fatalf("controller calls = %d, want 1", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchInsufficientUserQuotaSwitchesImmediately(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	quotaErr := errors.New("insufficient_user_quota: 预扣费额度失败, 用户剩余额度: ＄4.675172, 需要预扣费额度: ＄6.642374")
	first := &scriptedRuntimeModel{provider: "deepseek-yuanyu-0", model: "deepseek-v4-pro", errs: []error{quotaErr}}
	second := &scriptedRuntimeModel{provider: "deepseek-suifeng-0", model: "deepseek-v4-pro"}
	primary := NewSwappableModel("deepseek-yuanyu-0", "deepseek-v4-pro", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"deepseek-suifeng-0"},
		models: map[string]agentcore.ChatModel{"deepseek-suifeng-0": second},
	}

	model := newRuntimeFallbackModel("stage:writing", primary, primary, runtimeFallbackTestConfig(7), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "deepseek-suifeng-0/deepseek-v4-pro" {
		t.Fatalf("response = %q, want deepseek-suifeng-0/deepseek-v4-pro", got)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
	if len(controller.calls) != 1 {
		t.Fatalf("controller calls = %d, want 1", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchInsufficientUserQuotaAfterEmptyStreamPrelude(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	quotaErr := errors.New("insufficient_user_quota: 预扣费额度失败")
	first := &emptyPreludeErrorRuntimeModel{
		provider: "deepseek-yuanyu-0",
		model:    "deepseek-v4-pro",
		err:      quotaErr,
	}
	second := &scriptedRuntimeModel{provider: "deepseek-suifeng-0", model: "deepseek-v4-pro"}
	primary := NewSwappableModel("deepseek-yuanyu-0", "deepseek-v4-pro", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"deepseek-suifeng-0"},
		models: map[string]agentcore.ChatModel{"deepseek-suifeng-0": second},
	}

	model := newRuntimeFallbackModel("stage:writing", primary, primary, runtimeFallbackTestConfig(7), controller, nil)
	stream, err := model.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	var (
		response  string
		streamErr error
	)
	for event := range stream {
		switch event.Type {
		case agentcore.StreamEventDone:
			response = event.Message.TextContent()
		case agentcore.StreamEventError:
			streamErr = event.Err
		}
	}
	if streamErr != nil {
		t.Fatalf("stream error = %v, want fallback success", streamErr)
	}
	if response != "deepseek-suifeng-0/deepseek-v4-pro" {
		t.Fatalf("response = %q, want deepseek-suifeng-0/deepseek-v4-pro", response)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
}

func TestRuntimeAutoSwitchMonthlyUsageLimitSwitchesImmediately(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{
		provider: "codex-primary",
		model:    "gpt-5.5",
		errs:     []error{errors.New("Monthly usage limit reached. Resets in 18 days. To continue using this model, upgrade your plan.")},
	}
	second := &scriptedRuntimeModel{provider: "codex-backup", model: "gpt-5.5"}
	primary := NewSwappableModel("codex-primary", "gpt-5.5", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"codex-backup"},
		models: map[string]agentcore.ChatModel{"codex-backup": second},
		modelNames: map[string]string{
			"codex-backup": "gpt-5.5",
		},
	}

	model := newRuntimeFallbackModel("architect", primary, primary, runtimeFallbackTestConfig(3), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "codex-backup/gpt-5.5" {
		t.Fatalf("response = %q, want codex-backup/gpt-5.5", got)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
	if len(controller.calls) != 1 {
		t.Fatalf("controller calls = %d, want 1", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchRejectsDifferentModelTarget(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{provider: "grok", model: "grok-4.5", errs: []error{errors.New("402 insufficient_quota")}}
	second := &scriptedRuntimeModel{provider: "deepseek", model: "deepseek-v4-pro"}
	primary := NewSwappableModel("grok", "grok-4.5", first)
	controller := &runtimeFallbackControllerStub{
		order:      []string{"deepseek"},
		models:     map[string]agentcore.ChatModel{"deepseek": second},
		modelNames: map[string]string{"deepseek": "deepseek-v4-pro"},
	}

	model := newRuntimeFallbackModel("stage:co_create", primary, primary, runtimeFallbackTestConfig(1), controller, nil)
	if _, err := model.Generate(context.Background(), nil, nil); err == nil {
		t.Fatal("Generate error = nil, want original Grok quota error")
	}
	if second.Calls() != 0 {
		t.Fatalf("different model backend was called %d times", second.Calls())
	}
	provider, modelName := primary.Current()
	if provider != "grok" || modelName != "grok-4.5" {
		t.Fatalf("selection changed to %s/%s", provider, modelName)
	}
}

func TestRuntimeAutoSwitchRateLimitExceededSwitchesImmediately(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{provider: "deepseek-suifeng-0", model: "deepseek-v4-pro", errs: []error{errors.New("rate_limit_exceeded")}}
	second := &scriptedRuntimeModel{provider: "deepseek-suifeng-1", model: "deepseek-v4-pro"}
	primary := NewSwappableModel("deepseek-suifeng-0", "deepseek-v4-pro", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"deepseek-suifeng-1"},
		models: map[string]agentcore.ChatModel{"deepseek-suifeng-1": second},
		modelNames: map[string]string{
			"deepseek-suifeng-1": "deepseek-v4-pro",
		},
	}

	model := newRuntimeFallbackModel("architect", primary, primary, runtimeFallbackTestConfig(7), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "deepseek-suifeng-1/deepseek-v4-pro" {
		t.Fatalf("response = %q, want deepseek-suifeng-1/deepseek-v4-pro", got)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
	if len(controller.calls) != 1 {
		t.Fatalf("controller calls = %d, want 1", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchRateLimitExceededWalksDeepseekCandidatePool(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	yuanyu := &scriptedRuntimeModel{provider: "deepseek-yuanyu-0", model: "deepseek-v4-pro", errs: []error{errors.New("rate_limit_exceeded")}}
	suifeng0 := &scriptedRuntimeModel{provider: "deepseek-suifeng-0", model: "deepseek-v4-pro", errs: []error{errors.New("rate_limit_exceeded")}}
	suifeng1 := &scriptedRuntimeModel{provider: "deepseek-suifeng-1", model: "deepseek-v4-pro"}
	primary := NewSwappableModel("deepseek-yuanyu-0", "deepseek-v4-pro", yuanyu)
	controller := &runtimeFallbackControllerStub{
		order: []string{"deepseek-yuanyu-0", "deepseek-suifeng-0", "deepseek-suifeng-1"},
		models: map[string]agentcore.ChatModel{
			"deepseek-yuanyu-0":  yuanyu,
			"deepseek-suifeng-0": suifeng0,
			"deepseek-suifeng-1": suifeng1,
		},
		modelNames: map[string]string{
			"deepseek-yuanyu-0":  "deepseek-v4-pro",
			"deepseek-suifeng-0": "deepseek-v4-pro",
			"deepseek-suifeng-1": "deepseek-v4-pro",
		},
	}

	model := newRuntimeFallbackModel("architect", primary, primary, runtimeFallbackTestConfig(7), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "deepseek-suifeng-1/deepseek-v4-pro" {
		t.Fatalf("response = %q, want deepseek-suifeng-1/deepseek-v4-pro", got)
	}
	if yuanyu.Calls() != 1 || suifeng0.Calls() != 1 || suifeng1.Calls() != 1 {
		t.Fatalf("calls yuanyu=%d suifeng0=%d suifeng1=%d", yuanyu.Calls(), suifeng0.Calls(), suifeng1.Calls())
	}
	if len(controller.calls) != 2 {
		t.Fatalf("controller calls = %d, want 2", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchMissingTokenSwitchesImmediately(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{provider: "deepseek-yuanyu-0", model: "deepseek-v4-pro", errs: []error{errors.New("yuanyu backend has no token")}}
	second := &scriptedRuntimeModel{provider: "deepseek-suifeng-1", model: "deepseek-v4-pro"}
	primary := NewSwappableModel("deepseek-yuanyu-0", "deepseek-v4-pro", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"deepseek-suifeng-1"},
		models: map[string]agentcore.ChatModel{"deepseek-suifeng-1": second},
		modelNames: map[string]string{
			"deepseek-suifeng-1": "deepseek-v4-pro",
		},
	}

	model := newRuntimeFallbackModel("architect", primary, primary, runtimeFallbackTestConfig(3), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "deepseek-suifeng-1/deepseek-v4-pro" {
		t.Fatalf("response = %q, want deepseek-suifeng-1/deepseek-v4-pro", got)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
	if len(controller.calls) != 1 {
		t.Fatalf("controller calls = %d, want 1", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchInvalidTokenSwitchesImmediately(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{provider: "p1", model: "m1", errs: []error{errors.New("Invalid token (request id: 202607061029371978826938268d9d6KJEnuhoc)")}}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2"}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{order: []string{"p2"}, models: map[string]agentcore.ChatModel{"p2": second}}

	model := newRuntimeFallbackModel("architect", primary, primary, runtimeFallbackTestConfig(3), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "p2/m2" {
		t.Fatalf("response = %q, want p2/m2", got)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
	if len(controller.calls) != 1 {
		t.Fatalf("controller calls = %d, want 1", len(controller.calls))
	}
}

func TestExplicitRoleFailoverMissingTokenSwitchesImmediately(t *testing.T) {
	first := &scriptedRuntimeModel{provider: "deepseek-yuanyu-0", model: "deepseek-v4-pro", errs: []error{errors.New("yuanyu backend has no token")}}
	second := &scriptedRuntimeModel{provider: "deepseek-suifeng-1", model: "deepseek-v4-pro"}
	primary := NewSwappableModel("deepseek-yuanyu-0", "deepseek-v4-pro", first)
	var reports []FailoverEvent
	model := &failoverModel{
		role:    "architect",
		primary: primary,
		fallbacks: []modelTarget{{
			provider: "deepseek-suifeng-1",
			name:     "deepseek-v4-pro",
			model:    second,
		}},
		report: func(ev FailoverEvent) { reports = append(reports, ev) },
	}

	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "deepseek-suifeng-1/deepseek-v4-pro" {
		t.Fatalf("response = %q, want deepseek-suifeng-1/deepseek-v4-pro", got)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
	if len(reports) != 1 || reports[0].Reason != "auth_failed" {
		t.Fatalf("failover reports = %+v, want one auth_failed report", reports)
	}
	if provider, model := primary.Current(); provider != "deepseek-suifeng-1" || model != "deepseek-v4-pro" {
		t.Fatalf("current route = %s/%s, want switched fallback route", provider, model)
	}
}

func TestRuntimeAutoSwitchGatewayErrorSwitchesImmediately(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{
		provider: "p1",
		model:    "m1",
		errs:     []error{litellm.NewHTTPError("p1", 502, "<html><body>502 Bad Gateway</body></html>")},
	}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2"}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{order: []string{"p2"}, models: map[string]agentcore.ChatModel{"p2": second}}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(3), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "p2/m2" {
		t.Fatalf("response = %q, want p2/m2", got)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
	if len(controller.calls) != 1 {
		t.Fatalf("controller calls = %d, want 1", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchExhaustedGatewayErrorIsRetryable(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	gatewayErr := litellm.NewHTTPError("p1", 503, "<html><body>503 Service Unavailable</body></html>")
	first := &scriptedRuntimeModel{provider: "p1", model: "m1", errs: []error{gatewayErr}}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2", errs: []error{gatewayErr}}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"p2"},
		models: map[string]agentcore.ChatModel{"p2": second},
	}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(1), controller, nil)
	_, err := model.Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("Generate error = nil, want exhausted gateway error")
	}
	retryable, ok := err.(interface{ Retryable() bool })
	if !ok || !retryable.Retryable() {
		t.Fatalf("error retryable = %T %v, want Retryable true", err, err)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
}

func TestRuntimeAutoSwitchNetworkRetriesBeforeSwitching(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{
		provider: "p1",
		model:    "m1",
		errs:     []error{fmt.Errorf("temporary network: %w", agentcore.ErrProviderNetwork)},
	}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2"}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{order: []string{"p2"}, models: map[string]agentcore.ChatModel{"p2": second}}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(3), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "p1/m1" {
		t.Fatalf("response = %q, want p1/m1", got)
	}
	if first.Calls() != 2 || second.Calls() != 0 || len(controller.calls) != 0 {
		t.Fatalf("calls first=%d second=%d controller=%d", first.Calls(), second.Calls(), len(controller.calls))
	}
}

func TestRuntimeAutoSwitchRetriesGenericGatewayInvalidArgumentWithinBound(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	errs := make([]error, transientInvalidArgumentMaxAttempts-1)
	for index := range errs {
		errs[index] = litellm.NewHTTPError("openai", 400, `{"error":{"message":"Invalid Argument"}}`)
	}
	first := &scriptedRuntimeModel{
		provider: "deepseek-proxy",
		model:    "deepseek-v4-pro",
		errs:     errs,
	}
	primary := NewSwappableModel("deepseek-proxy", "deepseek-v4-pro", first)
	controller := &runtimeFallbackControllerStub{}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(7), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "deepseek-proxy/deepseek-v4-pro" {
		t.Fatalf("response = %q, want same-backend recovery", got)
	}
	if first.Calls() != transientInvalidArgumentMaxAttempts {
		t.Fatalf("calls = %d, want %d", first.Calls(), transientInvalidArgumentMaxAttempts)
	}
	if len(controller.calls) != 0 {
		t.Fatalf("controller calls = %d, want no provider switch", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchDoesNotRetryDetailedBadRequest(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{
		provider: "deepseek-proxy",
		model:    "deepseek-v4-pro",
		errs: []error{
			litellm.NewHTTPError("openai", 400, `{"error":{"message":"messages[2].tool_call_id is required"}}`),
		},
	}
	primary := NewSwappableModel("deepseek-proxy", "deepseek-v4-pro", first)
	controller := &runtimeFallbackControllerStub{}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(7), controller, nil)
	_, err := model.Generate(context.Background(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "tool_call_id") {
		t.Fatalf("Generate error = %v, want detailed bad request", err)
	}
	if first.Calls() != 1 {
		t.Fatalf("calls = %d, want no retry", first.Calls())
	}
	if len(controller.calls) != 0 {
		t.Fatalf("controller calls = %d, want no provider switch", len(controller.calls))
	}
}

func TestRuntimeAutoSwitchNetworkExhaustionSwitchesCandidate(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{
		provider: "p1",
		model:    "m1",
		errs: []error{
			fmt.Errorf("temporary network: %w", agentcore.ErrProviderNetwork),
			fmt.Errorf("temporary network: %w", agentcore.ErrProviderNetwork),
		},
	}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2"}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{order: []string{"p2"}, models: map[string]agentcore.ChatModel{"p2": second}}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(2), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "p2/m2" {
		t.Fatalf("response = %q, want p2/m2", got)
	}
	if first.Calls() != 2 || second.Calls() != 1 || len(controller.calls) != 1 {
		t.Fatalf("calls first=%d second=%d controller=%d", first.Calls(), second.Calls(), len(controller.calls))
	}
}

func TestRuntimeAutoSwitchTriesEachCandidateOnce(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	networkErr := fmt.Errorf("temporary network: %w", agentcore.ErrProviderNetwork)
	first := &scriptedRuntimeModel{provider: "p1", model: "m1", errs: []error{networkErr, networkErr}}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2", errs: []error{networkErr, networkErr}}
	third := &scriptedRuntimeModel{provider: "p3", model: "m3"}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"p2", "p3", "p1"},
		models: map[string]agentcore.ChatModel{"p2": second, "p3": third, "p1": first},
	}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(2), controller, nil)
	resp, err := model.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := responseText(resp); got != "p3/m3" {
		t.Fatalf("response = %q, want p3/m3", got)
	}
	if first.Calls() != 2 || second.Calls() != 2 || third.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d third=%d", first.Calls(), second.Calls(), third.Calls())
	}
	if len(controller.calls) != 2 {
		t.Fatalf("controller calls = %d, want 2", len(controller.calls))
	}
	if attempted := controller.calls[1].attempted; !attempted["p1"] || !attempted["p2"] {
		t.Fatalf("second controller attempted = %#v, want p1 and p2", attempted)
	}
}

func TestRuntimeAutoSwitchExhaustedErrorIsNotRetryable(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{provider: "p1", model: "m1", errs: []error{errors.New("402 insufficient_quota")}}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2", errs: []error{errors.New("402 insufficient_quota")}}
	third := &scriptedRuntimeModel{provider: "p3", model: "m3", errs: []error{errors.New("402 insufficient_quota")}}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"p2", "p3", "p1"},
		models: map[string]agentcore.ChatModel{"p2": second, "p3": third, "p1": first},
	}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(1), controller, nil)
	_, err := model.Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("Generate error = nil, want exhausted error")
	}
	retryable, ok := err.(interface{ Retryable() bool })
	if !ok || retryable.Retryable() {
		t.Fatalf("error retryable = %T %v, want Retryable false", err, err)
	}
	if first.Calls() != 1 || second.Calls() != 1 || third.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d third=%d", first.Calls(), second.Calls(), third.Calls())
	}
}

func TestRuntimeAutoSwitchExhaustedRateLimitExceededIsNotRetryable(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{provider: "p1", model: "m1", errs: []error{errors.New("rate_limit_exceeded")}}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2", errs: []error{errors.New("rate_limit_exceeded")}}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"p2"},
		models: map[string]agentcore.ChatModel{"p2": second},
	}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(1), controller, nil)
	_, err := model.Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("Generate error = nil, want exhausted rate-limit error")
	}
	retryable, ok := err.(interface{ Retryable() bool })
	if !ok || retryable.Retryable() {
		t.Fatalf("error retryable = %T %v, want Retryable false", err, err)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("calls first=%d second=%d", first.Calls(), second.Calls())
	}
}

func TestRuntimeAutoSwitchDoesNotSwitchProtectedError(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	first := &scriptedRuntimeModel{provider: "p1", model: "m1", errs: []error{errors.New("invalid request format")}}
	second := &scriptedRuntimeModel{provider: "p2", model: "m2"}
	primary := NewSwappableModel("p1", "m1", first)
	controller := &runtimeFallbackControllerStub{order: []string{"p2"}, models: map[string]agentcore.ChatModel{"p2": second}}

	model := newRuntimeFallbackModel("writer", primary, primary, runtimeFallbackTestConfig(1), controller, nil)
	_, err := model.Generate(context.Background(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid request format") {
		t.Fatalf("Generate error = %v, want original protected error", err)
	}
	if second.Calls() != 0 || len(controller.calls) != 0 {
		t.Fatalf("protected error switched: second=%d controller=%d", second.Calls(), len(controller.calls))
	}
}

func TestRuntimeFallbackStreamCancellationDoesNotRetryClosedStream(t *testing.T) {
	restoreRuntimeFallbackWait(t)
	primaryModel := newCancelThenCloseRuntimeModel()
	fallbackModel := &scriptedRuntimeModel{provider: "p2", model: "m1"}
	primary := NewSwappableModel("p1", "m1", primaryModel)
	controller := &runtimeFallbackControllerStub{
		order:  []string{"p2"},
		models: map[string]agentcore.ChatModel{"p2": fallbackModel},
	}
	model := newRuntimeFallbackModel("character", primary, primary, runtimeFallbackTestConfig(3), controller, nil)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := model.GenerateStream(ctx, nil, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	primaryModel.WaitStarted(t)
	cancel()

	var streamErr error
	for event := range stream {
		if event.Type == agentcore.StreamEventError {
			streamErr = event.Err
		}
	}
	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("stream error = %v, want context.Canceled", streamErr)
	}
	if primaryModel.Calls() != 1 {
		t.Fatalf("primary calls = %d, want 1", primaryModel.Calls())
	}
	if fallbackModel.Calls() != 0 || len(controller.calls) != 0 {
		t.Fatalf("cancellation triggered retry/fallback: fallback=%d controller=%d", fallbackModel.Calls(), len(controller.calls))
	}
}

func TestRuntimeAutoSwitchCandidateProvidersTreatFallbackBackendsAsPool(t *testing.T) {
	enabled := true
	cfg := Config{
		ModelAutoSwitch: ModelAutoSwitchConfig{
			Enabled:          &enabled,
			FallbackBackends: []string{"p3", "p1", "p2", "disabled", "no-key", "no-model", "p2"},
		},
		Providers: map[string]ProviderConfig{
			"p1":       {Type: "openai", APIKey: "sk-1", Models: []string{"m1"}},
			"p2":       {Type: "openai", APIKey: "sk-2", Models: []string{"m2"}},
			"p3":       {Type: "openai", APIKey: "sk-3", Models: []string{"m3"}},
			"disabled": {Type: "openai", APIKey: "sk-disabled", Models: []string{"m4"}, Disabled: true},
			"no-key":   {Models: []string{"m5"}},
			"no-model": {Type: "openai", APIKey: "sk-empty"},
		},
	}

	got := RuntimeAutoSwitchCandidateProviders(cfg, "p2", map[string]bool{"p3": true})
	want := []string{"p1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
	got = RuntimeAutoSwitchCandidateProviders(cfg, "missing", map[string]bool{})
	want = []string{"p3", "p1", "p2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates without current position = %#v, want %#v", got, want)
	}
	cfg.ModelAutoSwitch.FallbackBackends = nil
	if got := RuntimeAutoSwitchCandidateProviders(cfg, "p1", nil); len(got) != 0 {
		t.Fatalf("empty pool candidates = %#v, want empty", got)
	}
}

func runtimeFallbackTestConfig(maxAttempts int) ModelAutoSwitchConfig {
	enabled := true
	return ModelAutoSwitchConfig{
		Enabled:            &enabled,
		FallbackBackends:   []string{"p1", "p2", "p3"},
		NetworkMaxAttempts: maxAttempts,
	}
}

func restoreRuntimeFallbackWait(t *testing.T) {
	t.Helper()
	oldDelay := runtimeFallbackDelay
	oldWait := runtimeFallbackWait
	runtimeFallbackDelay = func(int) time.Duration { return 0 }
	runtimeFallbackWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() {
		runtimeFallbackDelay = oldDelay
		runtimeFallbackWait = oldWait
	})
}

type scriptedRuntimeModel struct {
	mu       sync.Mutex
	provider string
	model    string
	errs     []error
	calls    int
}

type emptyPreludeErrorRuntimeModel struct {
	mu       sync.Mutex
	provider string
	model    string
	err      error
	calls    int
}

type cancelThenCloseRuntimeModel struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
}

func newCancelThenCloseRuntimeModel() *cancelThenCloseRuntimeModel {
	return &cancelThenCloseRuntimeModel{started: make(chan struct{})}
}

func (m *cancelThenCloseRuntimeModel) Generate(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.markStarted()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *cancelThenCloseRuntimeModel) GenerateStream(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.markStarted()
	stream := make(chan agentcore.StreamEvent)
	go func() {
		<-ctx.Done()
		close(stream)
	}()
	return stream, nil
}

func (m *cancelThenCloseRuntimeModel) SupportsTools() bool { return true }

func (m *cancelThenCloseRuntimeModel) ProviderName() string { return "p1" }

func (m *cancelThenCloseRuntimeModel) Info() llm.ModelInfo {
	return llm.ModelInfo{Provider: "p1", Name: "m1"}
}

func (m *cancelThenCloseRuntimeModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *cancelThenCloseRuntimeModel) WaitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-m.started:
	case <-time.After(time.Second):
		t.Fatal("runtime model did not start")
	}
}

func (m *cancelThenCloseRuntimeModel) markStarted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls == 1 {
		close(m.started)
	}
}

func (m *emptyPreludeErrorRuntimeModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return nil, m.err
}

func (m *emptyPreludeErrorRuntimeModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	stream := make(chan agentcore.StreamEvent, 3)
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventTextStart}
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta}
	stream <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: m.err}
	close(stream)
	return stream, nil
}

func (m *emptyPreludeErrorRuntimeModel) SupportsTools() bool { return true }

func (m *emptyPreludeErrorRuntimeModel) ProviderName() string { return m.provider }

func (m *emptyPreludeErrorRuntimeModel) Info() llm.ModelInfo {
	return llm.ModelInfo{Provider: m.provider, Name: m.model}
}

func (m *emptyPreludeErrorRuntimeModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *scriptedRuntimeModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	call := m.calls
	m.calls++
	if call < len(m.errs) && m.errs[call] != nil {
		return nil, m.errs[call]
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(m.provider + "/" + m.model)},
	}}, nil
}

func (m *scriptedRuntimeModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.Generate(context.Background(), nil, nil)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message}
	close(ch)
	return ch, nil
}

func (m *scriptedRuntimeModel) SupportsTools() bool { return true }

func (m *scriptedRuntimeModel) ProviderName() string { return m.provider }

func (m *scriptedRuntimeModel) Info() llm.ModelInfo {
	return llm.ModelInfo{Provider: m.provider, Name: m.model}
}

func (m *scriptedRuntimeModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type runtimeFallbackControllerStub struct {
	order      []string
	models     map[string]agentcore.ChatModel
	modelNames map[string]string
	calls      []runtimeFallbackControllerCall
}

type runtimeFallbackControllerCall struct {
	current   ModelRef
	attempted map[string]bool
}

func (c *runtimeFallbackControllerStub) SelectRuntimeFallback(_ context.Context, current ModelRef, attempted map[string]bool, _ error) (RuntimeFallbackTarget, bool) {
	c.calls = append(c.calls, runtimeFallbackControllerCall{current: current, attempted: cloneAttemptedProviders(attempted)})
	for _, provider := range c.order {
		if attempted[provider] {
			continue
		}
		model, ok := c.models[provider]
		if !ok {
			continue
		}
		modelName := c.modelNames[provider]
		if modelName == "" {
			modelName = current.Model
		}
		return RuntimeFallbackTarget{
			Provider: provider,
			Model:    modelName,
			LLM:      model,
			Reason:   fmt.Sprintf("%s:%s->%s", RuntimeFallbackPoolReasonPrefix, current.Provider, provider),
		}, true
	}
	return RuntimeFallbackTarget{}, false
}

func responseText(resp *agentcore.LLMResponse) string {
	if resp == nil {
		return ""
	}
	return resp.Message.TextContent()
}
