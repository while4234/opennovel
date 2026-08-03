package promptcompile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

type runeCounter struct{}

func (runeCounter) CountTokens(_ context.Context, text string) (int, error) {
	return utf8.RuneCountInString(text), nil
}

type failingCounter struct{}

func (failingCounter) CountTokens(_ context.Context, _ string) (int, error) {
	return 0, errors.New("tokenizer unavailable")
}

func baseRequest() Request {
	return Request{
		Agent: AgentWriter,
		Mode:  ModeArc,
		RoleCore: Component{
			Text: "write the assigned chapter",
		},
		ModeContract: Component{
			Text: "preserve assigned mainline events",
			Mode: ModeArc,
		},
		TaskContract: Component{
			Text: "write event evt-1",
			Mode: ModeArc,
		},
		EvidencePacket: Component{
			Text: `{"event_ids":["evt-1"],"source":"secret evidence"}`,
			Mode: ModeArc,
		},
		StyleDeltas: []StyleDelta{
			{ID: "em_dash", Text: "reduce em dashes", Example: "use a direct sentence"},
		},
		Rules: []Rule{
			{ID: "event.evt-1", Kind: RuleRequired, Text: "evt-1 must appear", Mode: ModeArc},
		},
	}
}

func permissiveLimits() Limits {
	return Limits{
		RoleCoreTokens:     10_000,
		ModeContractTokens: 10_000,
		MaxRules:           16,
		MaxForbiddenRules:  8,
		MaxStyleDeltas:     3,
		AgentBudgets: map[Agent]Budget{
			AgentWriter: {TargetTokens: 100_000, HardTokens: 200_000},
		},
	}
}

func TestCompileAssemblesExactlyFiveLayers(t *testing.T) {
	t.Parallel()

	result, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), baseRequest())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if count := strings.Count(result.Prompt, "<prompt_component name="); count != len(orderedLayers) {
		t.Fatalf("prompt layer count = %d, want %d\n%s", count, len(orderedLayers), result.Prompt)
	}
	previous := -1
	for _, layer := range orderedLayers {
		index := strings.Index(result.Prompt, fmt.Sprintf("name=%q", layer))
		if index < 0 {
			t.Fatalf("prompt is missing %s", layer)
		}
		if index <= previous {
			t.Fatalf("layer %s is out of order", layer)
		}
		previous = index
	}
	if !strings.Contains(result.Prompt, `<active_rules_json>`) {
		t.Fatal("rules must be rendered inside task_contract")
	}
	if !strings.Contains(result.Prompt, `{"event_ids":["evt-1"],"source":"secret evidence"}`) {
		t.Fatal("atomic evidence JSON changed during compilation")
	}
	if result.Diagnostics.RuleCount != 1 || result.Diagnostics.DeduplicatedRuleCount != 0 {
		t.Fatalf("unexpected rule diagnostics: %+v", result.Diagnostics)
	}
	if len(result.Diagnostics.Components) != len(orderedLayers) {
		t.Fatalf("component diagnostics = %d, want %d", len(result.Diagnostics.Components), len(orderedLayers))
	}
	if strings.Contains(result.SystemPrompt, "secret evidence") {
		t.Fatal("system prompt must contain only the stable role and mode prefix")
	}
	if !strings.Contains(result.UserPrompt, "secret evidence") || !strings.Contains(result.UserPrompt, "active_rules_json") {
		t.Fatal("task-scoped evidence and rules must stay in the user prompt")
	}
}

func TestCompileRejectsModeMixingInComponentsAndRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{
			name: "mode contract",
			mutate: func(request *Request) {
				request.ModeContract.Mode = ModeFree
			},
		},
		{
			name: "evidence",
			mutate: func(request *Request) {
				request.EvidencePacket.Mode = ModeChapter
			},
		},
		{
			name: "rule",
			mutate: func(request *Request) {
				request.Rules[0].Mode = ModeFree
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := baseRequest()
			test.mutate(&request)
			_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
			assertValidationCode(t, err, "mixed_modes")
		})
	}
}

func TestCompileRequiresExplicitModeContractScope(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	request.ModeContract.Mode = ""
	_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
	assertValidationCode(t, err, "unscoped_mode_contract")
}

func TestCompileDeduplicatesRulesByIDAndNormalizedText(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	request.Rules = []Rule{
		{ID: "rule-a", Kind: RuleRequired, Text: "保留，百里冰初遇！", Mode: ModeArc},
		{ID: "rule-a", Kind: RuleRequired, Text: "保留百里冰初遇", Mode: ModeArc},
		{ID: "rule-b", Kind: RuleRequired, Text: "保留百里冰初遇。", Mode: ModeArc},
	}
	result, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Diagnostics.RuleCount != 1 {
		t.Fatalf("rule count = %d, want 1", result.Diagnostics.RuleCount)
	}
	if result.Diagnostics.DeduplicatedRuleCount != 2 {
		t.Fatalf("deduplicated rules = %d, want 2", result.Diagnostics.DeduplicatedRuleCount)
	}
	if count := strings.Count(result.Prompt, "保留，百里冰初遇！"); count != 1 {
		t.Fatalf("retained rule count = %d, want 1", count)
	}
}

func TestCompileRejectsConflictingRuleID(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	request.Rules = []Rule{
		{ID: "relationship", Kind: RuleRequired, Text: "show first meeting"},
		{ID: "relationship", Kind: RuleRequired, Text: "skip first meeting"},
	}
	_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
	assertValidationCode(t, err, "rule_id_conflict")
}

func TestCompileTracksIDsOfTextDuplicates(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	request.Rules = []Rule{
		{ID: "rule-a", Kind: RuleRequired, Text: "retain the encounter"},
		{ID: "rule-b", Kind: RuleRequired, Text: "retain the encounter"},
		{ID: "rule-b", Kind: RuleRequired, Text: "replace the encounter"},
	}
	_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
	assertValidationCode(t, err, "rule_id_conflict")
}

func TestCompileRejectsPunctuationOnlyRule(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	request.Rules = []Rule{{ID: "empty", Kind: RuleGuidance, Text: "，！？"}}
	_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
	assertValidationCode(t, err, "invalid_rule")
}

func TestCompileRejectsRequiredForbiddenConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		rules []Rule
	}{
		{
			name: "normalized text",
			rules: []Rule{
				{ID: "required", Kind: RuleRequired, Text: "百里冰初遇"},
				{ID: "forbidden", Kind: RuleForbidden, Text: "百里冰，初遇！"},
			},
		},
		{
			name: "semantic key",
			rules: []Rule{
				{ID: "required", Kind: RuleRequired, Text: "include the encounter", SemanticKey: "event:bailibing:first-meet"},
				{ID: "forbidden", Kind: RuleForbidden, Text: "omit their introduction", SemanticKey: "event:bailibing:first-meet"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := baseRequest()
			request.Rules = test.rules
			_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
			assertValidationCode(t, err, "required_forbidden_conflict")
		})
	}
}

func TestCompileRejectsSynonymousRulesWithDifferentIDs(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	request.Rules = []Rule{
		{ID: "meet-a", Kind: RuleRequired, Text: "show their first encounter", SemanticKey: "relationship:first-meet"},
		{ID: "meet-b", Kind: RuleRequired, Text: "establish how they met", SemanticKey: "relationship:first-meet"},
	}
	_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
	assertValidationCode(t, err, "semantic_rule_duplicate")
}

func TestCompileEnforcesRuleAndStyleLimitsAfterDeduplication(t *testing.T) {
	t.Parallel()

	t.Run("natural language rules", func(t *testing.T) {
		request := baseRequest()
		request.Rules = make([]Rule, 17)
		for index := range request.Rules {
			request.Rules[index] = Rule{ID: fmt.Sprintf("rule-%d", index), Kind: RuleRequired, Text: fmt.Sprintf("constraint %d", index)}
		}
		_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
		assertValidationCode(t, err, "too_many_rules")
	})

	t.Run("forbidden rules", func(t *testing.T) {
		request := baseRequest()
		request.Rules = make([]Rule, 9)
		for index := range request.Rules {
			request.Rules[index] = Rule{ID: fmt.Sprintf("forbidden-%d", index), Kind: RuleForbidden, Text: fmt.Sprintf("forbidden constraint %d", index)}
		}
		_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
		assertValidationCode(t, err, "too_many_forbidden_rules")
	})

	t.Run("style deltas", func(t *testing.T) {
		request := baseRequest()
		request.StyleDeltas = []StyleDelta{
			{ID: "1", Text: "a"},
			{ID: "2", Text: "b"},
			{ID: "3", Text: "c"},
			{ID: "4", Text: "d"},
		}
		_, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
		assertValidationCode(t, err, "too_many_style_deltas")
	})
}

func TestBudgetForMatchesProductionPlan(t *testing.T) {
	t.Parallel()

	want := map[Agent]Budget{
		AgentCoordinator:    {TargetTokens: 12_000, HardTokens: 16_000},
		AgentWriter:         {TargetTokens: 24_000, HardTokens: 32_000},
		AgentPlanner:        {TargetTokens: 28_000, HardTokens: 40_000},
		AgentArchitect:      {TargetTokens: 28_000, HardTokens: 40_000},
		AgentCharacter:      {TargetTokens: 32_000, HardTokens: 48_000},
		AgentEditor:         {TargetTokens: 32_000, HardTokens: 48_000},
		AgentAuditor:        {TargetTokens: 32_000, HardTokens: 48_000},
		AgentSourceAnalyzer: {TargetTokens: 20_000, HardTokens: 28_000},
	}
	for agent, expected := range want {
		actual, ok := BudgetFor(agent)
		if !ok {
			t.Fatalf("BudgetFor(%q) was not found", agent)
		}
		if actual != expected {
			t.Fatalf("BudgetFor(%q) = %+v, want %+v", agent, actual, expected)
		}
	}
}

func TestCompileReturnsSplitRequiredWithoutTruncation(t *testing.T) {
	t.Parallel()

	limits := permissiveLimits()
	limits.AgentBudgets[AgentWriter] = Budget{TargetTokens: 200, HardTokens: 260}
	request := baseRequest()
	request.EvidencePacket.Text = strings.Repeat("完整事件证据", 100)

	result, err := NewWithLimits(runeCounter{}, limits).Compile(t.Context(), request)
	if result.Prompt != "" {
		t.Fatal("hard-budget failure must not return a truncated prompt")
	}
	var split *SplitRequiredError
	if !errors.As(err, &split) {
		t.Fatalf("error = %T %v, want SplitRequiredError", err, err)
	}
	if split.Diagnostics.Strategy != StrategySplitRequiredNoTruncation {
		t.Fatalf("strategy = %q", split.Diagnostics.Strategy)
	}
	if split.Tokens <= split.Hard {
		t.Fatalf("split tokens=%d should exceed hard=%d", split.Tokens, split.Hard)
	}
}

func TestCompileReportsAboveTargetWithoutTruncation(t *testing.T) {
	t.Parallel()

	limits := permissiveLimits()
	limits.AgentBudgets[AgentWriter] = Budget{TargetTokens: 200, HardTokens: 2_000}
	request := baseRequest()
	request.EvidencePacket.Text = strings.Repeat("source", 40)

	result, err := NewWithLimits(runeCounter{}, limits).Compile(t.Context(), request)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Diagnostics.Strategy != StrategyAboveTargetNoTruncation {
		t.Fatalf("strategy = %q, want %q", result.Diagnostics.Strategy, StrategyAboveTargetNoTruncation)
	}
	if !strings.Contains(result.Prompt, request.EvidencePacket.Text) {
		t.Fatal("above-target prompt lost evidence")
	}
}

func TestCompileEnforcesStaticComponentLimits(t *testing.T) {
	t.Parallel()

	t.Run("role core", func(t *testing.T) {
		limits := permissiveLimits()
		limits.RoleCoreTokens = 80
		request := baseRequest()
		request.RoleCore.Text = strings.Repeat("role", 30)
		_, err := NewWithLimits(runeCounter{}, limits).Compile(t.Context(), request)
		assertValidationCode(t, err, "role_core_too_large")
	})

	t.Run("mode contract", func(t *testing.T) {
		limits := permissiveLimits()
		limits.ModeContractTokens = 80
		request := baseRequest()
		request.ModeContract.Text = strings.Repeat("mode", 30)
		_, err := NewWithLimits(runeCounter{}, limits).Compile(t.Context(), request)
		assertValidationCode(t, err, "mode_contract_too_large")
	})
}

func TestDiagnosticsDoNotRetainPromptContent(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	const secret = "PRIVATE_SOURCE_SENTENCE_91827"
	request.EvidencePacket.Text = secret
	result, err := NewWithLimits(runeCounter{}, permissiveLimits()).Compile(t.Context(), request)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	encoded, err := json.Marshal(result.Diagnostics)
	if err != nil {
		t.Fatalf("Marshal diagnostics: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("diagnostics leaked evidence: %s", encoded)
	}
}

func TestStaticPrefixHashOnlyTracksRoleAndMode(t *testing.T) {
	t.Parallel()

	compiler := NewWithLimits(runeCounter{}, permissiveLimits())
	first, err := compiler.Compile(t.Context(), baseRequest())
	if err != nil {
		t.Fatalf("Compile first: %v", err)
	}
	changedTask := baseRequest()
	changedTask.TaskContract.Text = "a different event"
	changedTask.EvidencePacket.Text = "different evidence"
	second, err := compiler.Compile(t.Context(), changedTask)
	if err != nil {
		t.Fatalf("Compile second: %v", err)
	}
	if first.Diagnostics.StaticPrefixHash != second.Diagnostics.StaticPrefixHash {
		t.Fatal("dynamic task/evidence changed the static prefix hash")
	}
	changedModeContract := baseRequest()
	changedModeContract.ModeContract.Text = "changed arc policy"
	third, err := compiler.Compile(t.Context(), changedModeContract)
	if err != nil {
		t.Fatalf("Compile third: %v", err)
	}
	if first.Diagnostics.StaticPrefixHash == third.Diagnostics.StaticPrefixHash {
		t.Fatal("mode contract change did not change static prefix hash")
	}
}

func TestCompilePropagatesTokenizerFailure(t *testing.T) {
	t.Parallel()

	_, err := NewWithLimits(failingCounter{}, permissiveLimits()).Compile(t.Context(), baseRequest())
	if err == nil || !strings.Contains(err.Error(), "tokenizer unavailable") {
		t.Fatalf("error = %v, want tokenizer failure", err)
	}
}

func TestDefaultCounterCanCompile(t *testing.T) {
	t.Parallel()

	result, err := New(nil).Compile(t.Context(), baseRequest())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Diagnostics.TotalTokens <= 0 {
		t.Fatal("default counter returned no tokens")
	}
}

func assertValidationCode(t *testing.T, err error, code string) {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want ValidationError(%s)", err, err, code)
	}
	if validation.Code != code {
		t.Fatalf("validation code = %q, want %q (error: %v)", validation.Code, code, err)
	}
}
