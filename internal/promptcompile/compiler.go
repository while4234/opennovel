package promptcompile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	maxRoleCoreTokens     = 2_500
	maxModeContractTokens = 800
	maxRules              = 16
	maxForbiddenRules     = 8
	maxStyleDeltas        = 3
)

// Limits controls compiler-level prompt constraints. Zero fields use the
// production defaults.
type Limits struct {
	RoleCoreTokens     int
	ModeContractTokens int
	MaxRules           int
	MaxForbiddenRules  int
	MaxStyleDeltas     int
	AgentBudgets       map[Agent]Budget
}

// Compiler validates, renders, and measures mode-scoped prompts.
type Compiler struct {
	counter TokenCounter
	limits  Limits
}

// New constructs a compiler. A nil counter selects the project context
// manager's estimator; callers with an exact model tokenizer should inject it.
func New(counter TokenCounter) *Compiler {
	return NewWithLimits(counter, Limits{})
}

// NewWithLimits constructs a compiler with explicit limits, primarily for
// controlled tests and deployments with model-specific budgets.
func NewWithLimits(counter TokenCounter, limits Limits) *Compiler {
	if counter == nil {
		counter = AgentcoreEstimateCounter{}
	}
	limits = normalizeLimits(limits)
	return &Compiler{counter: counter, limits: limits}
}

// Compile assembles all five components without truncating any caller content.
func (c *Compiler) Compile(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	budget, err := c.validateRequest(request)
	if err != nil {
		return Result{}, err
	}

	rules, removed, forbidden, err := normalizeRules(request.Mode, request.Rules)
	if err != nil {
		return Result{}, err
	}
	if len(rules) > c.limits.MaxRules {
		return Result{}, validationError("too_many_rules", "natural-language constraints=%d, limit=%d", len(rules), c.limits.MaxRules)
	}
	if forbidden > c.limits.MaxForbiddenRules {
		return Result{}, validationError("too_many_forbidden_rules", "forbidden constraints=%d, limit=%d", forbidden, c.limits.MaxForbiddenRules)
	}

	rendered, err := renderComponents(request, rules)
	if err != nil {
		return Result{}, err
	}
	componentTokens, err := c.countComponents(ctx, rendered)
	if err != nil {
		return Result{}, err
	}
	if componentTokens[0].Tokens > c.limits.RoleCoreTokens {
		return Result{}, validationError("role_core_too_large", "role_core=%d tokens, limit=%d", componentTokens[0].Tokens, c.limits.RoleCoreTokens)
	}
	if componentTokens[1].Tokens > c.limits.ModeContractTokens {
		return Result{}, validationError("mode_contract_too_large", "mode_contract=%d tokens, limit=%d", componentTokens[1].Tokens, c.limits.ModeContractTokens)
	}

	prompt := joinRendered(rendered)
	total, err := c.counter.CountTokens(ctx, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("promptcompile: count complete prompt: %w", err)
	}
	if total < 0 {
		return Result{}, validationError("invalid_token_count", "counter returned %d for complete prompt", total)
	}

	diagnostics := Diagnostics{
		Agent:                 request.Agent,
		Mode:                  request.Mode,
		Components:            componentTokens,
		TotalTokens:           total,
		TargetTokens:          budget.TargetTokens,
		HardTokens:            budget.HardTokens,
		RuleCount:             len(rules),
		DeduplicatedRuleCount: removed,
		ForbiddenRuleCount:    forbidden,
		Strategy:              StrategyWithinTarget,
		StaticPrefixHash:      hashStaticPrefix(rendered),
	}
	if total > budget.HardTokens {
		diagnostics.Strategy = StrategySplitRequiredNoTruncation
		return Result{}, &SplitRequiredError{
			Agent:       request.Agent,
			Tokens:      total,
			Target:      budget.TargetTokens,
			Hard:        budget.HardTokens,
			Diagnostics: diagnostics,
		}
	}
	if total > budget.TargetTokens {
		diagnostics.Strategy = StrategyAboveTargetNoTruncation
	}
	return Result{
		Prompt:       prompt,
		SystemPrompt: joinRendered(rendered[:2]),
		UserPrompt:   joinRendered(rendered[2:]),
		Diagnostics:  diagnostics,
	}, nil
}

func (c *Compiler) validateRequest(request Request) (Budget, error) {
	if !request.Mode.valid() {
		return Budget{}, validationError("invalid_mode", "mode %q is not chapter, arc, or free", request.Mode)
	}
	budget, ok := c.limits.AgentBudgets[request.Agent]
	if !ok {
		return Budget{}, validationError("invalid_agent", "agent %q has no prompt budget", request.Agent)
	}
	if err := validateBudget(request.Agent, budget); err != nil {
		return Budget{}, err
	}
	if strings.TrimSpace(request.RoleCore.Text) == "" {
		return Budget{}, validationError("missing_role_core", "role_core is required")
	}
	if strings.TrimSpace(request.ModeContract.Text) == "" {
		return Budget{}, validationError("missing_mode_contract", "mode_contract is required")
	}
	if request.ModeContract.Mode == "" {
		return Budget{}, validationError("unscoped_mode_contract", "mode_contract must declare mode %q", request.Mode)
	}
	if err := validateComponentModes(request.Mode, request); err != nil {
		return Budget{}, err
	}
	if len(request.StyleDeltas) > c.limits.MaxStyleDeltas {
		return Budget{}, validationError("too_many_style_deltas", "style deltas=%d, limit=%d", len(request.StyleDeltas), c.limits.MaxStyleDeltas)
	}
	for i, delta := range request.StyleDeltas {
		if strings.TrimSpace(delta.ID) == "" || strings.TrimSpace(delta.Text) == "" {
			return Budget{}, validationError("invalid_style_delta", "style delta %d requires id and text", i)
		}
	}
	return budget, nil
}

func validateComponentModes(mode Mode, request Request) error {
	components := []struct {
		layer     Layer
		component Component
	}{
		{LayerRoleCore, request.RoleCore},
		{LayerModeContract, request.ModeContract},
		{LayerTaskContract, request.TaskContract},
		{LayerEvidencePacket, request.EvidencePacket},
	}
	for _, item := range components {
		if item.component.Mode != "" && item.component.Mode != mode {
			return validationError(
				"mixed_modes",
				"%s is scoped to %q while request mode is %q",
				item.layer,
				item.component.Mode,
				mode,
			)
		}
	}
	return nil
}

type renderedComponent struct {
	layer Layer
	text  string
}

func renderComponents(request Request, rules []Rule) ([]renderedComponent, error) {
	ruleJSON, err := json.Marshal(rules)
	if err != nil {
		return nil, fmt.Errorf("promptcompile: marshal task rules: %w", err)
	}
	task := strings.TrimSpace(request.TaskContract.Text)
	if len(rules) > 0 {
		if task != "" {
			task += "\n\n"
		}
		task += "<active_rules_json>\n" + string(ruleJSON) + "\n</active_rules_json>"
	}
	styleJSON, err := json.Marshal(request.StyleDeltas)
	if err != nil {
		return nil, fmt.Errorf("promptcompile: marshal style deltas: %w", err)
	}
	style := ""
	if len(request.StyleDeltas) > 0 {
		style = "<style_deltas_json>\n" + string(styleJSON) + "\n</style_deltas_json>"
	}
	return []renderedComponent{
		{layer: LayerRoleCore, text: strings.TrimSpace(request.RoleCore.Text)},
		{layer: LayerModeContract, text: strings.TrimSpace(request.ModeContract.Text)},
		{layer: LayerTaskContract, text: task},
		{layer: LayerEvidencePacket, text: strings.TrimSpace(request.EvidencePacket.Text)},
		{layer: LayerActiveStyleDelta, text: style},
	}, nil
}

func (c *Compiler) countComponents(ctx context.Context, rendered []renderedComponent) ([]ComponentTokens, error) {
	counts := make([]ComponentTokens, 0, len(rendered))
	for _, component := range rendered {
		tokens, err := c.counter.CountTokens(ctx, renderComponent(component))
		if err != nil {
			return nil, fmt.Errorf("promptcompile: count %s: %w", component.layer, err)
		}
		if tokens < 0 {
			return nil, validationError("invalid_token_count", "counter returned %d for %s", tokens, component.layer)
		}
		counts = append(counts, ComponentTokens{Layer: component.layer, Tokens: tokens})
	}
	return counts, nil
}

func renderComponent(component renderedComponent) string {
	return fmt.Sprintf("<prompt_component name=%q>\n%s\n</prompt_component>", component.layer, component.text)
}

func joinRendered(rendered []renderedComponent) string {
	parts := make([]string, 0, len(rendered))
	for _, component := range rendered {
		parts = append(parts, renderComponent(component))
	}
	return strings.Join(parts, "\n\n")
}

func hashStaticPrefix(rendered []renderedComponent) string {
	if len(rendered) < 2 {
		return ""
	}
	prefix := renderComponent(rendered[0]) + "\n\n" + renderComponent(rendered[1])
	sum := sha256.Sum256([]byte(prefix))
	return hex.EncodeToString(sum[:])
}

func normalizeRules(mode Mode, input []Rule) ([]Rule, int, int, error) {
	byID := make(map[string]Rule, len(input))
	byText := make(map[string]Rule, len(input))
	bySemanticKey := make(map[string]Rule, len(input))
	result := make([]Rule, 0, len(input))
	removed := 0
	forbidden := 0

	for index, candidate := range input {
		candidate.ID = strings.TrimSpace(candidate.ID)
		candidate.Text = strings.TrimSpace(candidate.Text)
		candidate.SemanticKey = normalizeRuleText(candidate.SemanticKey)
		if candidate.ID == "" || candidate.Text == "" || !candidate.Kind.valid() {
			return nil, 0, 0, validationError("invalid_rule", "rule %d requires rule_id, valid kind, and text", index)
		}
		if candidate.Mode != "" && candidate.Mode != mode {
			return nil, 0, 0, validationError("mixed_modes", "rule %q is scoped to %q while request mode is %q", candidate.ID, candidate.Mode, mode)
		}

		normalizedText := normalizeRuleText(candidate.Text)
		if normalizedText == "" {
			return nil, 0, 0, validationError("invalid_rule", "rule %q has no meaningful text after normalization", candidate.ID)
		}
		if previous, ok := byID[candidate.ID]; ok {
			if sameRule(previous, candidate) {
				removed++
				continue
			}
			return nil, 0, 0, validationError("rule_id_conflict", "rule_id %q has different definitions", candidate.ID)
		}
		if previous, ok := byText[normalizedText]; ok {
			if previous.Kind == candidate.Kind {
				byID[candidate.ID] = candidate
				if candidate.SemanticKey != "" {
					if semanticPrevious, exists := bySemanticKey[candidate.SemanticKey]; exists {
						if semanticPrevious.Kind != candidate.Kind {
							return nil, 0, 0, validationError("required_forbidden_conflict", "rules %q and %q share semantic key %q with different kinds", semanticPrevious.ID, candidate.ID, candidate.SemanticKey)
						}
						if normalizeRuleText(semanticPrevious.Text) != normalizedText {
							return nil, 0, 0, validationError("semantic_rule_duplicate", "rules %q and %q are synonymous under semantic key %q", semanticPrevious.ID, candidate.ID, candidate.SemanticKey)
						}
					}
					bySemanticKey[candidate.SemanticKey] = previous
				}
				removed++
				continue
			}
			return nil, 0, 0, validationError("required_forbidden_conflict", "rules %q and %q normalize to the same proposition with different kinds", previous.ID, candidate.ID)
		}
		if candidate.SemanticKey != "" {
			if previous, ok := bySemanticKey[candidate.SemanticKey]; ok {
				if previous.Kind != candidate.Kind {
					return nil, 0, 0, validationError("required_forbidden_conflict", "rules %q and %q share semantic key %q with different kinds", previous.ID, candidate.ID, candidate.SemanticKey)
				}
				return nil, 0, 0, validationError("semantic_rule_duplicate", "rules %q and %q are synonymous under semantic key %q", previous.ID, candidate.ID, candidate.SemanticKey)
			}
			bySemanticKey[candidate.SemanticKey] = candidate
		}

		byID[candidate.ID] = candidate
		byText[normalizedText] = candidate
		result = append(result, candidate)
		if candidate.Kind == RuleForbidden {
			forbidden++
		}
	}
	return result, removed, forbidden, nil
}

func sameRule(left, right Rule) bool {
	return left.Kind == right.Kind &&
		normalizeRuleText(left.Text) == normalizeRuleText(right.Text) &&
		normalizeRuleText(left.SemanticKey) == normalizeRuleText(right.SemanticKey) &&
		left.Mode == right.Mode
}

func normalizeRuleText(text string) string {
	text = strings.ToLower(norm.NFKC.String(strings.TrimSpace(text)))
	var builder strings.Builder
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func normalizeLimits(limits Limits) Limits {
	if limits.RoleCoreTokens <= 0 {
		limits.RoleCoreTokens = maxRoleCoreTokens
	}
	if limits.ModeContractTokens <= 0 {
		limits.ModeContractTokens = maxModeContractTokens
	}
	if limits.MaxRules <= 0 {
		limits.MaxRules = maxRules
	}
	if limits.MaxForbiddenRules <= 0 {
		limits.MaxForbiddenRules = maxForbiddenRules
	}
	if limits.MaxStyleDeltas <= 0 {
		limits.MaxStyleDeltas = maxStyleDeltas
	}
	if len(limits.AgentBudgets) == 0 {
		limits.AgentBudgets = cloneBudgets(defaultBudgets)
	} else {
		limits.AgentBudgets = cloneBudgets(limits.AgentBudgets)
	}
	return limits
}

func cloneBudgets(input map[Agent]Budget) map[Agent]Budget {
	result := make(map[Agent]Budget, len(input))
	for agent, budget := range input {
		result[agent] = budget
	}
	return result
}

func validationError(code, format string, args ...any) error {
	return &ValidationError{Code: code, Detail: fmt.Sprintf(format, args...)}
}
