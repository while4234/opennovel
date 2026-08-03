package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

type countingQualityModel struct {
	calls int
}

func (m *countingQualityModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(`{"facts":["anchor"]}`)},
		StopReason: agentcore.StopReasonStop,
	}}, nil
}

func (*countingQualityModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, nil
}

func (*countingQualityModel) SupportsTools() bool { return false }

func TestQualitySuiteCoversEveryConfigurableStageThreeTimes(t *testing.T) {
	t.Parallel()
	fixtures, err := loadQualityFixtures()
	if err != nil {
		t.Fatalf("loadQualityFixtures: %v", err)
	}
	cases := buildQualityCases(fixtures)
	if err := validateQualitySuite(cases); err != nil {
		t.Fatalf("validateQualitySuite: %v", err)
	}
	if got, want := len(cases), 24; got != want {
		t.Fatalf("case count = %d, want %d", got, want)
	}
	counts := make(map[string]int)
	for _, testCase := range cases {
		counts[testCase.Stage]++
	}
	for _, stage := range bootstrap.KnownModelStages {
		if counts[stage] != 3 {
			t.Fatalf("stage %q count = %d, want 3", stage, counts[stage])
		}
	}
}

func TestWriterToolingDiagnosticsStayOutsideRankedStages(t *testing.T) {
	t.Parallel()
	cases := buildWriterToolingCases()
	if got, want := len(cases), 3; got != want {
		t.Fatalf("diagnostic count = %d, want %d", got, want)
	}
	for _, testCase := range cases {
		if !testCase.Diagnostic || testCase.Stage != "writer_tooling" {
			t.Fatalf("diagnostic case %#v is not isolated from ranked stages", testCase)
		}
	}
}

func TestParseModelSpecsIncludesReasoningAndModelSlash(t *testing.T) {
	t.Parallel()
	specs, err := parseModelSpecs("grok-oauth/grok-4.5@xhigh,openrouter/google/gemini-3.1-pro@high")
	if err != nil {
		t.Fatalf("parseModelSpecs: %v", err)
	}
	if specs[0].ReasoningEffort != "xhigh" || specs[0].ID() != "grok-oauth__grok-4.5__xhigh" {
		t.Fatalf("first spec = %#v", specs[0])
	}
	if specs[1].Model != "google/gemini-3.1-pro" || specs[1].ReasoningEffort != "high" {
		t.Fatalf("model with slash = %#v", specs[1])
	}
}

func TestConfiguredQualityModelsEnumeratesPairsAndDefaults(t *testing.T) {
	t.Parallel()
	cfg := bootstrap.Config{
		ReasoningEffort: "medium",
		Providers: map[string]bootstrap.ProviderConfig{
			"b": {Models: []string{"m2"}},
			"a": {
				Models:                []string{"m1", "m2"},
				ModelReasoningEfforts: map[string]string{"m1": "xhigh"},
			},
		},
	}
	specs := configuredQualityModels(cfg)
	if got, want := len(specs), 3; got != want {
		t.Fatalf("configured models = %d, want %d: %#v", got, want, specs)
	}
	if specs[0] != (modelSpec{Provider: "a", Model: "m1", ReasoningEffort: "xhigh"}) {
		t.Fatalf("first spec = %#v", specs[0])
	}
	if specs[1].ReasoningEffort != "medium" || specs[2].ReasoningEffort != "medium" {
		t.Fatalf("fallback reasoning was not applied: %#v", specs)
	}
}

func TestStableAnonymousOrderDoesNotExposeIdentity(t *testing.T) {
	t.Parallel()
	attempts := []qualityAttempt{
		{Provider: "deepseek-infa", Model: "deepseek-v4-pro", Response: "alpha"},
		{Provider: "grok-oauth", Model: "grok-4.5", ReasoningEffort: "xhigh", Response: "beta"},
	}
	first := stableAnonymousOrder("case-1", attempts)
	second := stableAnonymousOrder("case-1", attempts)
	if qualityAttemptID(first[0]) != qualityAttemptID(second[0]) {
		t.Fatal("anonymous order is not stable")
	}
	for _, attempt := range first {
		if strings.Contains(attempt.Response, attempt.Provider) || strings.Contains(attempt.Response, attempt.Model) {
			t.Fatalf("fixture response unexpectedly leaks identity: %#v", attempt)
		}
	}
}

func TestHardChecksScoreStructuredCoverage(t *testing.T) {
	t.Parallel()
	testCase := qualityCase{
		Structured: true, MinRunes: 1, MaxRunes: 1000,
		RequiredTerms: []string{`"facts"`, "anchor"},
	}
	score, issues := scoreQualityHardChecks(testCase, `{"facts":["anchor"]}`, agentcore.StopReasonStop)
	if score != 100 || len(issues) != 0 {
		t.Fatalf("score = %.1f issues=%v, want 100 and no issues", score, issues)
	}
}

func TestWritingLengthABCasesStateEveryHardBoundary(t *testing.T) {
	t.Parallel()
	fixtures, err := loadQualityFixtures()
	if err != nil {
		t.Fatalf("loadQualityFixtures: %v", err)
	}
	cases := buildWritingLengthABCases(fixtures)
	if err := validateWritingLengthABCases(cases); err != nil {
		t.Fatalf("validateWritingLengthABCases: %v", err)
	}
	if got, want := len(cases), 9; got != want {
		t.Fatalf("case count = %d, want %d", got, want)
	}
}

func TestWritingLengthGateMakesUnderLengthASevereFailure(t *testing.T) {
	t.Parallel()
	score, ratio, reason := applyWritingLengthGate(94, 2000, 2600)
	if reason != "below_minimum" {
		t.Fatalf("reason = %q, want below_minimum", reason)
	}
	if ratio >= 0.77 || score >= 39 {
		t.Fatalf("under-length score = %.1f ratio=%.3f, want severe penalty", score, ratio)
	}
	passed, ratio, reason := applyWritingLengthGate(94, 2800, 2600)
	if passed != 94 || ratio != 1 || reason != "" {
		t.Fatalf("passing result = %.1f ratio=%.1f reason=%q", passed, ratio, reason)
	}
	overTarget, ratio, reason := applyWritingLengthGate(94, 3600, 2600)
	if overTarget != 94 || ratio != 1 || reason != "" {
		t.Fatalf("over-target result = %.1f ratio=%.1f reason=%q", overTarget, ratio, reason)
	}
}

func TestParseQualityJudgementRequiresEveryAnonymousCandidate(t *testing.T) {
	t.Parallel()
	payload := map[string]any{
		"scores": []map[string]any{
			{"candidate": "A", "dimensions": []map[string]any{{"name": "准确", "score": 90}}, "overall": 90, "summary": "ok"},
			{"candidate": "B", "dimensions": []map[string]any{{"name": "准确", "score": 80}}, "overall": 80, "summary": "ok"},
		},
		"preference": "A",
		"confidence": 0.8,
	}
	data, _ := json.Marshal(payload)
	judgement, err := parseQualityJudgement(string(data), map[string]string{"A": "one", "B": "two"}, []string{"准确"})
	if err != nil {
		t.Fatalf("parseQualityJudgement: %v", err)
	}
	if len(judgement.Scores) != 2 {
		t.Fatalf("scores = %d, want 2", len(judgement.Scores))
	}
}

func TestParseQualityJudgementNormalizesPercentageConfidence(t *testing.T) {
	t.Parallel()
	payload := `{"scores":[{"candidate":"A","dimensions":[{"name":"准确","score":90}],"overall":90,"summary":"ok"}],"preference":"A","confidence":99}`
	judgement, err := parseQualityJudgement(payload, map[string]string{"A": "one"}, []string{"准确"})
	if err != nil {
		t.Fatalf("parseQualityJudgement: %v", err)
	}
	if judgement.Confidence != 0.99 {
		t.Fatalf("confidence = %v, want 0.99", judgement.Confidence)
	}
}

func TestRunQualityCandidateSkipsSuccessAndRetriesFailedRecord(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	spec := modelSpec{Provider: "provider", Model: "model"}
	testCase := qualityCase{
		ID: "case", Stage: bootstrap.StageSourceAnalysis, Structured: true,
		SystemPrompt: "system", TaskPrompt: "task", Context: "context",
		RequiredTerms: []string{`"facts"`, "anchor"}, MinRunes: 1, MaxRunes: 1000, MaxOutputTokens: 100,
	}
	path := qualityCandidatePath(outputDir, spec, testCase.ID)
	if err := writeJSONAtomic(path, qualityAttempt{Status: "success"}); err != nil {
		t.Fatalf("write success fixture: %v", err)
	}
	model := &countingQualityModel{}
	opts := options{OutputDir: outputDir, RequestTimeout: time.Second}
	if err := runQualityCandidate(context.Background(), opts, spec, model, testCase); err != nil {
		t.Fatalf("skip success: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("successful result triggered %d calls", model.calls)
	}
	if err := writeJSONAtomic(path, qualityAttempt{Status: "error", Error: "temporary"}); err != nil {
		t.Fatalf("write error fixture: %v", err)
	}
	if err := runQualityCandidate(context.Background(), opts, spec, model, testCase); err != nil {
		t.Fatalf("retry failed record: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("failed record triggered %d calls, want 1", model.calls)
	}
	var result qualityAttempt
	if err := readJSONFile(path, &result); err != nil {
		t.Fatalf("read retried result: %v", err)
	}
	if result.Status != "success" || result.HardScore != 100 {
		t.Fatalf("retried result = %#v", result)
	}
}
