package promptcompile

import (
	"context"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
)

// TokenCounter is the integration point for a provider/model tokenizer. The
// compiler deliberately does not estimate from bytes or runes on its own.
type TokenCounter interface {
	CountTokens(ctx context.Context, text string) (int, error)
}

// AgentcoreEstimateCounter adapts the token estimator already used by the
// project's context manager. Providers with an exact tokenizer should inject
// their own TokenCounter into New instead.
type AgentcoreEstimateCounter struct{}

func (AgentcoreEstimateCounter) CountTokens(_ context.Context, text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	return corecontext.EstimateTokens(agentcore.UserMsg(text)), nil
}
