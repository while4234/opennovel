package imp

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestMergeFoundationFromReportsBatchedSplitsAndRetries(t *testing.T) {
	reports := []domain.AdaptationSourceReport{
		testSourceReport(1, "Opening", "Alpha"),
		testSourceReport(2, "Turn", "Beta"),
		testSourceReport(3, "Reveal", "Gamma"),
	}
	llm := &scriptedStructuredLLM{
		responses: []structuredLLMResponse{
			{text: "not a valid foundation envelope"},
			{text: testFoundationMergeEnvelope("Batch Alpha")},
			{text: testFoundationMergeEnvelope("Batch Gamma")},
			{text: testFoundationMergeEnvelope("Partial Summary A")},
			{text: testFoundationMergeEnvelope("Partial Summary B")},
			{text: testFoundationMergeEnvelope("Final Source")},
		},
	}
	var events []FoundationMergeBatchEvent

	got, err := MergeFoundationFromReportsBatched(
		context.Background(),
		llm,
		"system ${chapter_count}",
		reports,
		StructuredCallOptions{MaxAttempts: 2, DisableStream: true, Sleep: noStructuredTestSleep},
		1800,
		func(ev FoundationMergeBatchEvent) { events = append(events, ev) },
	)
	if err != nil {
		t.Fatalf("MergeFoundationFromReportsBatched: %v", err)
	}
	if llm.calls < 4 {
		t.Fatalf("llm calls=%d, want at least 4", llm.calls)
	}
	if !strings.HasPrefix(got.Premise, "# ") {
		t.Fatalf("final premise missing heading: %q", got.Premise)
	}
	if outline := domain.FlattenOutline(got.Volumes); len(outline) != len(reports) {
		t.Fatalf("outline chapters=%d, want %d", len(outline), len(reports))
	}
	if len(events) < 3 || !events[len(events)-1].Final {
		t.Fatalf("expected batch progress plus final merge event, got %+v", events)
	}
}

func TestFoundationMergeReportBatchesRespectCompiledByteLimit(t *testing.T) {
	reports := make([]domain.AdaptationSourceReport, 17)
	for index := range reports {
		marker := strings.Repeat("梦中女孩因果事实", 80)
		reports[index] = domain.AdaptationSourceReport{
			Chapter:        index + 1,
			Title:          "章节",
			Summary:        marker,
			CharacterFacts: []string{marker},
			KeyEvents:      []string{marker},
			WorldRules:     []string{marker},
			Timeline: []domain.TimelineEvent{{
				Time:  "当晚",
				Event: marker,
			}},
		}
	}
	prompt := strings.Repeat("汇总规则", 400) + " ${chapter_count}"
	if bytes := foundationMergeReportRequestBytes(prompt, reports); bytes <= structuredInputLimitBytes {
		t.Fatalf("test setup request bytes=%d, want over %d", bytes, structuredInputLimitBytes)
	}

	batches := FoundationMergeReportBatchesForPrompt(reports, DefaultFoundationMergeRunes, prompt)
	if len(batches) <= 1 {
		t.Fatalf("compiled byte limit should split reports, got %d batch", len(batches))
	}
	for index, batch := range batches {
		if bytes := foundationMergeReportRequestBytes(prompt, batch); bytes > structuredInputLimitBytes {
			t.Fatalf("batch %d request bytes=%d, limit=%d", index+1, bytes, structuredInputLimitBytes)
		}
	}
}

func TestFoundationMergeCharacterDecoderMigratesLegacyFieldsWithoutSilentLoss(t *testing.T) {
	envelope := testFoundationMergeEnvelope("Legacy")
	envelope = replaceEnvelopeBody(t, envelope, "CHARACTERS", `[
	  {
	    "id":"hero",
	    "name":"Hero",
	    "role":"lead",
	    "gender":"unspecified",
	    "description":"tracks source causality",
	    "arc":"keeps moving",
	    "traits":["focused"],
	    "goals":["find the truth","protect the witness"],
	    "relationships":["trusts Mentor"]
	  }
	]`)
	result, err := parseFoundationMergeOutput(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Characters) != 1 ||
		result.Characters[0].Goal != "find the truth；protect the witness" ||
		!strings.Contains(result.Characters[0].Notes, "trusts Mentor") {
		t.Fatalf("migrated character = %+v", result.Characters)
	}
}

func TestFoundationMergeCharacterDecoderRejectsUnknownDriftField(t *testing.T) {
	envelope := testFoundationMergeEnvelope("Unknown")
	envelope = replaceEnvelopeBody(t, envelope, "CHARACTERS", `[
	  {"id":"hero","name":"Hero","role":"lead","gender":"unspecified","description":"x","arc":"y","traits":[],"future_destiny":"invented"}
	]`)
	if _, err := parseFoundationMergeOutput(envelope); err == nil ||
		!strings.Contains(err.Error(), "unsupported field") {
		t.Fatalf("unknown drift err = %v", err)
	}
}

func testSourceReport(chapter int, title, marker string) domain.AdaptationSourceReport {
	return domain.AdaptationSourceReport{
		Chapter:        chapter,
		Title:          title,
		Summary:        strings.Repeat(marker+" summary fact. ", 80),
		Characters:     []string{"Hero", marker},
		CharacterFacts: []string{marker + " changes the source causality."},
		KeyEvents:      []string{marker + " irreversible event"},
		WorldRules:     []string{marker + " continuity rule"},
		HookType:       "mystery",
		DominantStrand: "quest",
	}
}

func testFoundationMergeEnvelope(name string) string {
	return `=== PREMISE ===
# ` + name + `

Source facts merged in order.

=== CHARACTERS ===
[
  {"id":"hero","name":"Hero","role":"lead","gender":"unspecified","description":"tracks source causality","arc":"keeps moving","traits":["focused"]}
]

=== RELATIONSHIPS ===
[]

=== WORLD_RULES ===
[
  {"category":"continuity","rule":"source facts remain causal","boundary":"do not invent unsupported events"}
]

=== COMPASS ===
{
  "ending_direction":"preserve the source causal chain",
  "open_threads":["source mystery"],
  "estimated_scale":"source-sized"
}
`
}

func TestParseFoundationMergeOutputAllowsMissingOptionalCompass(t *testing.T) {
	output := strings.Split(testFoundationMergeEnvelope("Optional compass"), "\n=== COMPASS ===")[0]
	result, err := parseFoundationMergeOutput(output)
	if err != nil {
		t.Fatalf("parse foundation merge without compass: %v", err)
	}
	if result.Compass != nil {
		t.Fatalf("compass = %#v, want nil", result.Compass)
	}
	if len(result.Characters) != 1 || len(result.WorldRules) != 1 {
		t.Fatalf("required source settings were not preserved: %#v", result)
	}
}

func TestDecodeWorldRulesJSONAcceptsSingleObjectAndWrapper(t *testing.T) {
	for name, testCase := range map[string]struct {
		raw   string
		count int
	}{
		"single":      {raw: `{"category":"society","rule":"Names do not prove gender.","boundary":"Use source evidence."}`, count: 1},
		"wrapper":     {raw: `{"rules":[{"category":"dream","rule":"Dream memories persist.","boundary":"No unsupported reset."}]}`, count: 1},
		"named map":   {raw: `{"dream":{"category":"dream","rule":"Dream memories persist."},"identity":"Names remain stable."}`, count: 2},
		"string list": {raw: `["Dream memories persist.","Names remain stable."]`, count: 2},
	} {
		t.Run(name, func(t *testing.T) {
			var rules []domain.WorldRule
			if err := decodeWorldRulesJSON(testCase.raw, &rules); err != nil {
				t.Fatalf("decode world rules: %v", err)
			}
			if len(rules) != testCase.count || strings.TrimSpace(rules[0].Rule) == "" {
				t.Fatalf("rules = %#v", rules)
			}
		})
	}
}

func TestMergeFoundationPartialsDeterministicPreservesSettingsWithoutFinalModelCall(t *testing.T) {
	partials := []FoundationMergePartial{
		{
			Index: 1, From: 1, To: 10,
			Result: &FoundationResult{
				Premise: "# Opening\n\nFirst-stage pressure.",
				Characters: []domain.Character{{
					ID: "hero-a", Name: "Hero", Gender: "unspecified", Tier: "important",
					ContrastDetails: []domain.CharacterContrastDetail{{Surface: "calm", Depth: "afraid"}},
				}, {
					ID: "witness", Name: "Witness", Gender: "female", Tier: "important",
				}},
				Relationships: []domain.CharacterRelationship{{
					ID: "hero-witness", SourceCharacterID: "hero-a", TargetCharacterID: "witness",
					Type: domain.RelationshipTypeAlly, Direction: domain.RelationshipDirectionDirected,
					Status: domain.RelationshipStatusActive, Description: "protects",
				}},
				WorldRules: []domain.WorldRule{{Category: "society", Rule: "Evidence has consequences."}},
			},
		},
		{
			Index: 2, From: 11, To: 20,
			Result: &FoundationResult{
				Premise: "# Turn\n\nLater-stage pressure.",
				Characters: []domain.Character{{
					ID: "hero-b", Name: "Hero", Gender: "male", Tier: "core",
					KeyBackstory: []domain.CharacterBackstory{{Event: "failed once", Impact: "avoids delay"}},
				}, {
					ID: "witness", Name: "Witness", Gender: "female", Tier: "important",
				}, {
					ID: "other", Name: "Other", Aliases: []string{"Witness"}, Gender: "unspecified", Tier: "secondary",
				}},
				Relationships: []domain.CharacterRelationship{{
					ID: "hero-witness-later", SourceCharacterID: "hero-b", TargetCharacterID: "witness",
					Type: domain.RelationshipTypeAlly, Direction: domain.RelationshipDirectionDirected,
					Status: domain.RelationshipStatusStrained, Description: "trust erodes",
				}},
				WorldRules: []domain.WorldRule{{Category: "society", Rule: "Evidence has consequences.", Boundary: "No reset."}},
			},
		},
	}

	result, err := MergeFoundationPartialsDeterministic(partials, 20)
	if err != nil {
		t.Fatalf("deterministic merge: %v", err)
	}
	if len(result.Characters) != 3 {
		t.Fatalf("characters = %#v", result.Characters)
	}
	hero := result.Characters[0]
	if hero.Gender != "male" || hero.Tier != "core" || len(hero.ContrastDetails) != 1 || len(hero.KeyBackstory) != 1 {
		t.Fatalf("merged hero = %#v", hero)
	}
	if len(result.Relationships) != 1 || !strings.Contains(result.Relationships[0].Description, "trust erodes") {
		t.Fatalf("relationships = %#v", result.Relationships)
	}
	if len(result.WorldRules) != 1 || result.WorldRules[0].Boundary != "No reset." {
		t.Fatalf("world rules = %#v", result.WorldRules)
	}
	if !strings.Contains(result.Premise, "原著第 1-10 章") || !strings.Contains(result.Premise, "原著第 11-20 章") {
		t.Fatalf("premise = %q", result.Premise)
	}
}
