package web

import "testing"

func TestObservabilityConfidenceThresholds(t *testing.T) {
	tests := []struct {
		calls, usage int
		want         string
	}{
		{calls: 30, usage: 29, want: "high"},
		{calls: 10, usage: 8, want: "medium"},
		{calls: 100, usage: 79, want: "low"},
		{calls: 9, usage: 9, want: "low"},
	}
	for _, test := range tests {
		group := observabilityGroup{Calls: test.calls, UsageCalls: test.usage}
		finalizeObservabilityGroup(&group)
		if group.Confidence != test.want {
			t.Fatalf("calls=%d usage=%d confidence=%s want=%s", test.calls, test.usage, group.Confidence, test.want)
		}
	}
}

func TestFinalizeObservabilityGroupDistinguishesUnsupportedAndIncompleteUsage(t *testing.T) {
	group := observabilityGroup{Provider: "openai", Model: "gpt-4.1", Calls: 10, UsageCalls: 8, Failures: 2, Retries: 1, LatencyMSSum: 5000}
	finalizeObservabilityGroup(&group)
	if group.CacheCapable == nil || !*group.CacheCapable {
		t.Fatalf("cache capability = %v, want known capable", group.CacheCapable)
	}
	if !group.UsageIncomplete || group.Coverage != .8 {
		t.Fatalf("usage completeness = %v coverage=%v", group.UsageIncomplete, group.Coverage)
	}
	if group.AvgLatencyMS != 500 || group.FailureRate != .2 || group.RetryRate != .1 {
		t.Fatalf("derived rates = latency %v failure %v retry %v", group.AvgLatencyMS, group.FailureRate, group.RetryRate)
	}
}
