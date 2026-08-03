package domain

import "testing"

func TestAdaptationModePolicyForGranularity(t *testing.T) {
	cases := map[string]AdaptationModePolicy{
		AdaptationGranularityChapter: AdaptationPolicyDetailPreservationWithSplit,
		AdaptationGranularityArc:     AdaptationPolicyMainlinePreservation,
		AdaptationGranularityFree:    AdaptationPolicyTargetCoherence,
	}
	for mode, want := range cases {
		if got := AdaptationModePolicyForGranularity(mode); got != want {
			t.Fatalf("mode %s policy=%s want=%s", mode, got, want)
		}
	}
}

func TestEnsureAdaptationSourceEventsClassifiesStableMainline(t *testing.T) {
	report := AdaptationSourceReport{Chapter: 13, KeyEvents: []string{"百里冰遇劫，林逸飞出手相救", "两人在街边闲聊"}}
	first := EnsureAdaptationSourceEvents(&report)
	second := EnsureAdaptationSourceEvents(&report)
	if len(first) != 2 || first[0].Importance != AdaptationEventMainline || !first[0].Required {
		t.Fatalf("events=%+v", first)
	}
	if first[0].ID != second[0].ID {
		t.Fatalf("event ID is unstable: %q != %q", first[0].ID, second[0].ID)
	}
}

func TestClassifyFirstMeetingWithoutLegacyKeywordsAsMainline(t *testing.T) {
	if got := ClassifyAdaptationSourceEvent("二人第一次照面并决定同行"); got != AdaptationEventMainline {
		t.Fatalf("classification=%q", got)
	}
}

func TestCompileAdaptationRulesDeduplicatesAndScopes(t *testing.T) {
	rules := CompileAdaptationRules("必须保留初遇。必须保留初遇；第10-12章不要提前恋爱", AdaptationGranularityArc)
	if len(rules) != 2 {
		t.Fatalf("rules=%+v", rules)
	}
	if got := ApplicableAdaptationRules(rules, AdaptationGranularityArc, 9); len(got) != 1 {
		t.Fatalf("chapter 9 applicable=%+v", got)
	}
	if got := ApplicableAdaptationRules(rules, AdaptationGranularityArc, 11); len(got) != 2 {
		t.Fatalf("chapter 11 applicable=%+v", got)
	}
}

func TestCompileAdaptationRulesPreservesBriefOrder(t *testing.T) {
	rules := CompileAdaptationRules("总则说明。必须保留初遇。第10章不得提前恋爱", AdaptationGranularityArc)
	if len(rules) != 3 {
		t.Fatalf("rules=%+v", rules)
	}
	if rules[0].Text != "总则说明" || rules[1].Text != "必须保留初遇" || rules[2].Text != "第10章不得提前恋爱" {
		t.Fatalf("rule order=%+v", rules)
	}
}

func TestSelectAdaptationPromptRulesPrioritizesScopedAndDirectiveRules(t *testing.T) {
	rules := CompileAdaptationRules("普通说明一。普通说明二。必须保留初遇。第10章不得提前恋爱", AdaptationGranularityArc)
	selected := SelectAdaptationPromptRules(rules, 2, 1)
	if len(selected) != 2 {
		t.Fatalf("selected=%+v", selected)
	}
	if selected[0].Text != "第10章不得提前恋爱" || selected[1].Text != "必须保留初遇" {
		t.Fatalf("selection priority=%+v", selected)
	}
}

func TestValidateAdaptationRulesRejectsRequiredForbiddenConflict(t *testing.T) {
	rules := CompileAdaptationRules("必须保留初遇。不得保留初遇", AdaptationGranularityArc)
	if err := ValidateAdaptationRules(rules); err == nil {
		t.Fatal("required/forbidden conflict must fail before model invocation")
	}
}
