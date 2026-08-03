package adapt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/promptcompile"
)

type fixedPromptCounter struct{ tokens int }

func (c fixedPromptCounter) CountTokens(context.Context, string) (int, error) { return c.tokens, nil }

type oversizedEvidenceCounter struct{}

func (oversizedEvidenceCounter) CountTokens(_ context.Context, text string) (int, error) {
	if strings.Contains(text, `"events"`) {
		return 50_000, nil
	}
	return 10, nil
}

func TestCompilePlannerCallLoadsOnlyCurrentMode(t *testing.T) {
	systemPrompt, userPrompt, diagnostics, err := compilePlannerCall(
		t.Context(),
		"planner role",
		`Planning input: {"granularity":"arc","events":["meet"]}`,
		fixedPromptCounter{tokens: 10},
	)
	if err != nil {
		t.Fatalf("compilePlannerCall: %v", err)
	}
	if diagnostics == nil || diagnostics.Mode != promptcompile.ModeArc {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if strings.Contains(systemPrompt, "target_coherence") || strings.Contains(systemPrompt, "detail_preservation_with_split") {
		t.Fatalf("system prompt mixed adaptation modes: %s", systemPrompt)
	}
	if !strings.Contains(userPrompt, `"events":["meet"]`) {
		t.Fatalf("evidence payload was changed: %s", userPrompt)
	}
}

func TestCompilePlannerCallClassifiesTargetFoundationAsEvidence(t *testing.T) {
	dynamicFoundation := targetFoundationPromptMarker + "\n" + strings.Repeat("confirmed target detail ", 1_000)
	systemPrompt, userPrompt, diagnostics, err := compilePlannerCall(
		t.Context(),
		"planner role\n\n"+dynamicFoundation,
		`Planning input: {"granularity":"arc","events":["meet"]}`,
		nil,
	)
	if err != nil {
		t.Fatalf("compilePlannerCall: %v", err)
	}
	if strings.Contains(systemPrompt, targetFoundationPromptMarker) {
		t.Fatal("dynamic target Foundation remained in the bounded role core")
	}
	if !strings.Contains(userPrompt, targetFoundationPromptMarker) ||
		!strings.Contains(userPrompt, strings.Repeat("confirmed target detail ", 100)) {
		t.Fatal("dynamic target Foundation was not preserved as planner evidence")
	}
	if diagnostics == nil || diagnostics.Components[0].Tokens >= diagnostics.Components[3].Tokens {
		t.Fatalf("component classification was not reflected in diagnostics: %+v", diagnostics)
	}
}

func TestCompilePlannerCallDoesNotTruncateOversizedJSON(t *testing.T) {
	payload := `{"granularity":"arc","events":["meet","case"]}`
	_, _, diagnostics, err := compilePlannerCall(t.Context(), "planner role", payload, oversizedEvidenceCounter{})
	var split *promptcompile.SplitRequiredError
	if !errors.As(err, &split) {
		t.Fatalf("expected SplitRequiredError, got %v", err)
	}
	if diagnostics != nil {
		t.Fatal("oversized prompt must not return a partial compiled payload")
	}
}

func TestCompilePlannerCallUsesExplicitModeForRepairPayloads(t *testing.T) {
	ctx := withAdaptationPromptContract(t.Context(), fixedPromptCounter{tokens: 10}, "free", "不得让人物无前因恋爱")
	_, userPrompt, diagnostics, err := compilePlannerCall(ctx, "planner role", `{"candidates":[{"chapter":2}]}`, promptTokenCounterFromContext(ctx))
	if err != nil {
		t.Fatalf("compile repair payload: %v", err)
	}
	if diagnostics == nil || diagnostics.Mode != promptcompile.ModeFree || diagnostics.RuleCount != 1 {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if !strings.Contains(userPrompt, "不得让人物无前因恋爱") {
		t.Fatalf("active structured rule was not compiled: %s", userPrompt)
	}
}

func TestCompilePlannerCallBoundsLongBriefWithoutRejectingIt(t *testing.T) {
	parts := make([]string, 0, 96)
	for index := 1; index <= 96; index++ {
		parts = append(parts, fmt.Sprintf("必须落实长篇约束%d", index))
	}
	brief := strings.Join(parts, "。")
	ctx := withAdaptationPromptContract(t.Context(), fixedPromptCounter{tokens: 10}, "arc", brief)
	payload := fmt.Sprintf(`{"granularity":"arc","brief":%q,"events":["meet"]}`, brief)
	_, userPrompt, diagnostics, err := compilePlannerCall(ctx, "planner role", payload, promptTokenCounterFromContext(ctx))
	if err != nil {
		t.Fatalf("compile long brief: %v", err)
	}
	if diagnostics == nil || diagnostics.RuleCount != domain.AdaptationPromptMaxRules {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	if !strings.Contains(userPrompt, "必须落实长篇约束96") {
		t.Fatalf("complete brief was not preserved in evidence payload")
	}
}

func TestCompilePlannerCallFailsClosedWithoutMode(t *testing.T) {
	if _, _, _, err := compilePlannerCall(t.Context(), "planner role", `{"candidates":[]}`, fixedPromptCounter{tokens: 10}); err == nil {
		t.Fatal("planner call without an explicit or structured mode must fail before model invocation")
	}
}
