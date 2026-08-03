package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type modelExpansionRecommendationPlanner struct {
	model   agentcore.ChatModel
	prompts assets.Prompts
	store   *storepkg.Store
}

func (h *Host) ExpansionPlanner() *ExpansionPlanner {
	if h == nil || h.store == nil || h.models == nil {
		return nil
	}
	recommender := &modelExpansionRecommendationPlanner{model: h.models.ForStageWithFailover(bootstrap.StageSkeleton, nil), prompts: h.bundle.Prompts, store: h.store}
	return NewExpansionPlanner(h.store, recommender)
}

func (planner *modelExpansionRecommendationPlanner) RecommendExpansion(ctx context.Context, expansionContext ExpansionContext, request domain.ExpansionRequest) (domain.ExpansionRecommendation, error) {
	if planner == nil || planner.model == nil {
		return domain.ExpansionRecommendation{}, expansionModelError("provider_error", fmt.Errorf("expansion recommendation model is unavailable"))
	}
	system := planner.prompts.NormalExpansionPlanner
	if expansionContext.Mode == domain.RevisionModeAdaptation {
		system = planner.prompts.AdaptationExpansionPlanner
	}
	payload, err := json.Marshal(struct {
		Context ExpansionContext        `json:"context"`
		Request domain.ExpansionRequest `json:"request"`
	}{expansionContext, request})
	if err != nil {
		return domain.ExpansionRecommendation{}, expansionModelError("invalid_schema", err)
	}
	recorder, beginErr := modeldiag.Begin(modeldiag.Request{Store: planner.store, Task: "expansion_recommender", System: system, User: payload, InputLimitBytes: expansionContextBudgetBytes, OutputLimitTokens: 6000, SelectorCounts: map[string]int{"chapters": 1}, SplitReason: "signed expansion context bundle", ContractSignature: domain.ContentSignature([]byte(request.IdempotencyKey))})
	if beginErr != nil {
		return domain.ExpansionRecommendation{}, expansionModelError("request_budget_exceeded", beginErr)
	}
	response, err := planner.model.Generate(ctx, []agentcore.Message{agentcore.SystemMsg(system), agentcore.UserMsg(string(payload))}, nil, agentcore.WithMaxTokens(6000), agentcore.WithJSONMode())
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return domain.ExpansionRecommendation{}, expansionModelError("provider_error", err)
	}
	if response == nil || strings.TrimSpace(response.Message.TextContent()) == "" {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
		return domain.ExpansionRecommendation{}, expansionModelError("empty_response", fmt.Errorf("empty expansion recommendation"))
	}
	text := strings.TrimSpace(response.Message.TextContent())
	if !strings.HasPrefix(text, "{") || !strings.HasSuffix(text, "}") {
		_ = recorder.Finish(modeldiag.StatusTruncated, text, response.Message.Usage)
		return domain.ExpansionRecommendation{}, expansionModelError("truncated_response", fmt.Errorf("truncated expansion recommendation"))
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	var recommendation domain.ExpansionRecommendation
	if err := decoder.Decode(&recommendation); err != nil {
		_ = recorder.Finish(modeldiag.StatusDecodeError, text, response.Message.Usage)
		return recommendation, expansionModelError("invalid_json", fmt.Errorf("decode expansion recommendation: %w", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		_ = recorder.Finish(modeldiag.StatusDecodeError, text, response.Message.Usage)
		return recommendation, expansionModelError("invalid_json", fmt.Errorf("expansion recommendation contains trailing JSON"))
	}
	recommendation.Location = request.Location
	if err := recommendation.Validate(expansionContext.Mode); err != nil {
		_ = recorder.Finish(modeldiag.StatusInvalidSchema, text, response.Message.Usage)
		return recommendation, expansionModelError("invalid_schema", err)
	}
	if err := recorder.Finish(modeldiag.StatusCompleted, text, response.Message.Usage); err != nil {
		return domain.ExpansionRecommendation{}, expansionModelError("content_store_failure", err)
	}
	return recommendation, nil
}

func expansionModelError(class string, err error) error {
	return &domain.ManuscriptRevisionError{Class: class, Err: err}
}
