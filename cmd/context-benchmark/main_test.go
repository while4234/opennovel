package main

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestBuildSuiteUsesFortyCases(t *testing.T) {
	t.Parallel()
	cases := buildSuite(corpus{
		SourceText:        strings.Repeat("原作内容。", 200_000),
		Premise:           strings.Repeat("前提。", 2_000),
		Characters:        strings.Repeat("人物。", 2_000),
		WorldRules:        strings.Repeat("规则。", 2_000),
		Outline:           strings.Repeat("骨架。", 2_000),
		LayeredOutline:    strings.Repeat("提纲。", 20_000),
		SimulationProfile: strings.Repeat("画像。", 2_000),
		SourceFoundation:  strings.Repeat("基础。", 2_000),
		WrittenChapters:   strings.Repeat("正文。", 20_000),
		ChapterTenPlan:    strings.Repeat("计划。", 2_000),
		ChapterTenDraft:   strings.Repeat("草稿。", 5_000),
	})
	if got, want := len(cases), 40; got != want {
		t.Fatalf("case count = %d, want %d", got, want)
	}
	stageCounts := make(map[string]int)
	for _, testCase := range cases {
		stageCounts[testCase.Stage]++
	}
	if stageCounts["source_analysis"] != 10 {
		t.Fatalf("source analysis cases = %d, want 10", stageCounts["source_analysis"])
	}
	wantStageCounts := map[string]int{
		"skeleton_planning":      6,
		"detail_outline":         6,
		"initial_cocreate_draft": 6,
		"opening_draft":          3,
		"mid_continuation":       6,
		"arc_close_edit":         3,
	}
	for stage, want := range wantStageCounts {
		if stageCounts[stage] != want {
			t.Fatalf("%s cases = %d, want %d", stage, stageCounts[stage], want)
		}
	}
}

func TestAssembleContextHonorsTarget(t *testing.T) {
	t.Parallel()
	target := 4_000
	contextText := assembleContext([]string{
		strings.Repeat("甲乙丙丁。", 10_000),
		strings.Repeat("天地玄黄。", 10_000),
	}, target, "请完成任务")
	estimated := estimatePromptTokens(contextText, "请完成任务")
	if estimated > target {
		t.Fatalf("estimated tokens = %d, exceeds %d", estimated, target)
	}
	if estimated < target-100 {
		t.Fatalf("estimated tokens = %d, unexpectedly below %d", estimated, target-100)
	}
}

func TestValidateSourceAnalysisRequiresEverySegment(t *testing.T) {
	t.Parallel()
	testCase := benchmarkCase{Stage: "source_analysis", Structured: true, AnalysisSegments: 2}
	_, issues := validateResponse(testCase, `{"reports":[{"segment":"SEG-001","summary":"`+strings.Repeat("内容", 300)+`"}]}`, agentcore.StopReasonStop)
	if !contains(issues, "missing_segment_coverage") {
		t.Fatalf("issues = %v, want missing_segment_coverage", issues)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
