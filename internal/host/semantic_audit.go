package host

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

// EstimateSemanticAudit is deterministic and never calls a model.
func (h *Host) EstimateSemanticAudit(options adapt.SemanticAuditOptions) (*adapt.SemanticAuditEstimate, error) {
	return adapt.EstimateSemanticAudit(h.store, options)
}

// PrepareSemanticAudit freezes the auditable scope and input digest before the
// background model job starts.
func (h *Host) PrepareSemanticAudit(options adapt.SemanticAuditOptions) (*adapt.SemanticAuditRun, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	provider, model, _, err := h.semanticAuditModel(options)
	if err != nil {
		return nil, err
	}
	return adapt.PrepareSemanticAudit(h.store, options, provider, model)
}

func (h *Host) ExecuteSemanticAudit(ctx context.Context, runID string) error {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return err
	}
	defer release()
	run, err := adapt.LoadSemanticAuditRun(h.store, runID)
	if err != nil {
		return err
	}
	executionOptions := run.Options
	executionOptions.Provider = run.Provider
	executionOptions.Model = run.Model
	_, _, model, err := h.semanticAuditModel(executionOptions)
	if err != nil {
		return err
	}
	tracked := &semanticAuditUsageModel{ChatModel: model, tracker: h.usage, runID: runID}
	return adapt.ExecuteSemanticAudit(ctx, h.store, runID, tracked)
}

type semanticAuditUsageModel struct {
	agentcore.ChatModel
	tracker *UsageTracker
	runID   string
}

func (m *semanticAuditUsageModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	started := time.Now()
	response, err := m.ChatModel.Generate(ctx, messages, tools, opts...)
	stage := "semantic_audit"
	if len(messages) > 0 {
		if marker := strings.Index(messages[0].TextContent(), "Stage="); marker >= 0 {
			tail := messages[0].TextContent()[marker+6:]
			if end := strings.IndexAny(tail, ". \n"); end >= 0 {
				tail = tail[:end]
			}
			if strings.TrimSpace(tail) != "" {
				stage = strings.TrimSpace(tail)
			}
		}
	}
	status := "ok"
	if err != nil {
		status = "failed"
	}
	if response != nil {
		m.tracker.RecordWithContext(UsageCallContext{RunID: m.runID, Workflow: "adaptation_audit", Stage: stage, CallKind: "semantic_second_pass", Latency: time.Since(started), Status: status}, "auditor", response.Message)
	}
	return response, err
}

func (h *Host) LoadSemanticAudit(runID string) (*adapt.SemanticAuditRun, error) {
	return adapt.LoadSemanticAuditRun(h.store, runID)
}

func (h *Host) ResumeSemanticAudit(runID string) (*adapt.SemanticAuditRun, error) {
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	return adapt.ResumeSemanticAudit(h.store, runID)
}

func (h *Host) semanticAuditModel(options adapt.SemanticAuditOptions) (string, string, agentcore.ChatModel, error) {
	if _, err := agents.ParseThinkingLevel(strings.TrimSpace(options.ReasoningEffort)); err != nil {
		return "", "", nil, fmt.Errorf("invalid reasoning_effort: %w", err)
	}
	provider := strings.TrimSpace(options.Provider)
	modelName := strings.TrimSpace(options.Model)
	if (provider == "") != (modelName == "") {
		return "", "", nil, fmt.Errorf("provider and model must be supplied together")
	}
	if provider == "" {
		provider, modelName, _ = h.models.CurrentSelection("auditor")
		return provider, modelName, h.models.ForRoleWithFailover("auditor", nil), nil
	}

	h.mu.Lock()
	cfg := h.cfg
	pc, ok := cfg.Providers[provider]
	h.mu.Unlock()
	if !ok {
		return "", "", nil, fmt.Errorf("provider %q is not configured", provider)
	}
	model, err := bootstrap.NewProviderModelWithConfig(cfg, provider, modelName, pc)
	if err != nil {
		return "", "", nil, err
	}
	return provider, modelName, model, nil
}
