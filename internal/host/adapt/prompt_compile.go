package adapt

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/promptcompile"
)

var adaptationPromptModePattern = regexp.MustCompile(`"granularity"\s*:\s*"(chapter|arc|free)"`)

type promptTokenCounterContextKey struct{}
type promptModeContextKey struct{}
type promptRulesContextKey struct{}

func withPromptTokenCounter(ctx context.Context, counter promptcompile.TokenCounter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if counter == nil {
		return ctx
	}
	return context.WithValue(ctx, promptTokenCounterContextKey{}, counter)
}

func promptTokenCounterFromContext(ctx context.Context) promptcompile.TokenCounter {
	if ctx == nil {
		return nil
	}
	counter, _ := ctx.Value(promptTokenCounterContextKey{}).(promptcompile.TokenCounter)
	return counter
}

func withAdaptationPromptContract(
	ctx context.Context,
	counter promptcompile.TokenCounter,
	mode string,
	brief string,
) context.Context {
	ctx = withPromptTokenCounter(ctx, counter)
	ctx = context.WithValue(ctx, promptModeContextKey{}, promptcompile.Mode(domain.NormalizeAdaptationGranularity(mode)))
	rules := domain.CompileAdaptationRules(brief, mode)
	rules = domain.SelectAdaptationPromptRules(rules, domain.AdaptationPromptMaxRules, domain.AdaptationPromptMaxForbiddenRules)
	return context.WithValue(ctx, promptRulesContextKey{}, plannerPromptRules(rules))
}

func withAdaptationPromptModeIfMissing(ctx context.Context, mode string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(promptModeContextKey{}).(promptcompile.Mode); ok {
		return ctx
	}
	return context.WithValue(ctx, promptModeContextKey{}, promptcompile.Mode(domain.NormalizeAdaptationGranularity(mode)))
}

func plannerPromptRules(rules []domain.AdaptationRule) []promptcompile.Rule {
	out := make([]promptcompile.Rule, 0, len(rules))
	for _, rule := range rules {
		kind := promptcompile.RuleGuidance
		switch rule.Kind {
		case domain.AdaptationRuleRequired:
			kind = promptcompile.RuleRequired
		case domain.AdaptationRuleForbidden:
			kind = promptcompile.RuleForbidden
		}
		out = append(out, promptcompile.Rule{
			ID: rule.ID, Kind: kind, Text: rule.Text, Mode: promptcompile.Mode(rule.Mode),
		})
	}
	return out
}

func compilePlannerCall(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	counter promptcompile.TokenCounter,
) (string, string, *promptcompile.Diagnostics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	mode, ok := ctx.Value(promptModeContextKey{}).(promptcompile.Mode)
	if !ok {
		mode, ok = promptModeFromPayload(userPrompt)
	}
	if !ok {
		return "", "", nil, fmt.Errorf("adaptation prompt mode is required before model invocation")
	}
	modeContract, err := promptcompile.AdaptationModeContract(mode, promptcompile.AgentPlanner)
	if err != nil {
		return "", "", nil, err
	}
	rules, _ := ctx.Value(promptRulesContextKey{}).([]promptcompile.Rule)
	roleCore, targetFoundationEvidence := splitPlannerRoleCoreAndEvidence(systemPrompt)
	compiler := promptcompile.NewWithLimits(counter, promptcompile.Limits{
		MaxRules:          domain.AdaptationPromptMaxRules,
		MaxForbiddenRules: domain.AdaptationPromptMaxForbiddenRules,
	})
	result, err := compiler.Compile(ctx, promptcompile.Request{
		Agent: promptcompile.AgentPlanner,
		Mode:  mode,
		RoleCore: promptcompile.Component{
			Text: roleCore,
		},
		ModeContract: promptcompile.Component{
			Text: modeContract,
			Mode: mode,
		},
		TaskContract: promptcompile.Component{
			Text: "Complete only the requested planning stage and preserve every structured event contract. Return the requested JSON shape without prose.",
			Mode: mode,
		},
		EvidencePacket: promptcompile.Component{
			Text: joinPlannerEvidence(targetFoundationEvidence, userPrompt),
			Mode: mode,
		},
		Rules: rules,
	})
	if err != nil {
		return "", "", nil, err
	}
	diagnostics := result.Diagnostics
	return result.SystemPrompt, result.UserPrompt, &diagnostics, nil
}

func splitPlannerRoleCoreAndEvidence(systemPrompt string) (string, string) {
	systemPrompt = strings.TrimSpace(systemPrompt)
	index := strings.Index(systemPrompt, targetFoundationPromptMarker)
	if index < 0 {
		return systemPrompt, ""
	}
	return strings.TrimSpace(systemPrompt[:index]), strings.TrimSpace(systemPrompt[index:])
}

func joinPlannerEvidence(parts ...string) string {
	joined := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			joined = append(joined, part)
		}
	}
	return strings.Join(joined, "\n\n")
}

func promptModeFromPayload(payload string) (promptcompile.Mode, bool) {
	match := adaptationPromptModePattern.FindStringSubmatch(payload)
	if len(match) != 2 {
		return "", false
	}
	mode := promptcompile.Mode(match[1])
	switch mode {
	case promptcompile.ModeChapter, promptcompile.ModeArc, promptcompile.ModeFree:
		return mode, true
	default:
		return "", false
	}
}
