package bootstrap

import (
	"fmt"
	"strings"

	"github.com/voocel/litellm"
	"github.com/voocel/litellm/provider/compat"
	"github.com/voocel/litellm/provider/grok"
)

const grokDefaultBaseURL = "https://api.x.ai/v1"

// newGrokProvider keeps the upstream provider for established models and adds
// the missing Grok 4.5 reasoning contract until litellm exposes it natively.
func newGrokProvider(model string, cfg compat.Config) (litellm.Provider, error) {
	if !isGrok45(model) {
		return grok.New(cfg)
	}
	return compat.New(cfg, compat.Spec{
		Name:     "grok",
		Endpoint: compat.EndpointSpec{BaseURL: grokDefaultBaseURL},
		Auth:     compat.AuthSpec{APIKeyRequired: true},
		Request: compat.RequestSpec{
			SupportsJSONSchema: true,
			Thinking:           mapGrok45Thinking,
			ProviderOptions:    mapGrok45ProviderOptions,
		},
		Response: compat.ResponseSpec{
			ModelFromResponse:         true,
			HasCompletionTokenDetails: true,
		},
		Capabilities: func(_ string, caps litellm.Capabilities) litellm.Capabilities {
			caps.Thinking.Supported = litellm.SupportYes
			caps.Thinking.Disable = litellm.SupportNo
			caps.Thinking.Efforts = []string{"low", "medium", "high", "xhigh"}
			caps.Thinking.BudgetTokens = litellm.SupportNo
			caps.Thinking.IncludeOutput = litellm.SupportNo
			caps.Structured.JSONSchema = litellm.SupportYes
			caps.Structured.Strict = litellm.SupportYes
			return caps
		},
	})
}

func isGrok45(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return normalized == "grok-4.5" || strings.HasPrefix(normalized, "grok-4.5-")
}

func mapGrok45Thinking(thinking *litellm.Thinking, _ string) (map[string]any, error) {
	if thinking == nil || thinking.Mode == litellm.ThinkingUnspecified {
		return nil, nil
	}
	if thinking.Mode != litellm.ThinkingEnabled {
		return nil, fmt.Errorf("grok: reasoning cannot be disabled for grok-4.5")
	}
	switch thinking.Effort {
	case "low", "medium", "high", "xhigh":
		return map[string]any{"reasoning_effort": thinking.Effort}, nil
	default:
		return nil, fmt.Errorf("grok: unsupported grok-4.5 reasoning_effort %q; use low, medium, high, or xhigh", thinking.Effort)
	}
}

func mapGrok45ProviderOptions(options litellm.ProviderOptions, body map[string]any, _ *litellm.Request) error {
	for key, value := range options {
		switch key {
		case "stop", "presence_penalty", "frequency_penalty", "presencePenalty", "frequencyPenalty":
			return fmt.Errorf("grok: provider option %q is not supported for grok-4.5", key)
		}
		if _, exists := body[key]; exists {
			return fmt.Errorf("grok: provider option %q conflicts with generated request field", key)
		}
		body[key] = value
	}
	return nil
}
