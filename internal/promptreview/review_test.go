package promptreview

import (
	"context"
	"testing"
	"unicode/utf8"
)

type runeCounter struct{}

func (runeCounter) CountTokens(context.Context, string) (int, error) { return 1, nil }

func TestReviewRejectsShortPromptThatLostCapability(t *testing.T) {
	report, err := Review(t.Context(), "只写正文", runeCounter{}, Policy{
		Role:      "writer",
		MaxTokens: 100,
		Capabilities: []Capability{{
			ID:          "independent_check",
			Description: "must retain independent evidence checking",
			AnyOf:       []string{"独立检查"},
		}},
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if report.Passed || len(report.Findings) != 1 || report.Findings[0].Code != "missing_capability" {
		t.Fatalf("report=%+v", report)
	}
}

type realRuneCounter struct{}

func (realRuneCounter) CountTokens(_ context.Context, text string) (int, error) {
	return utf8.RuneCountInString(text), nil
}

func TestReviewRejectsDuplicateParagraphs(t *testing.T) {
	paragraph := "这一段足够长，用来验证规范化之后的重复段落能够被确定性发现和报告。"
	report, err := Review(t.Context(), paragraph+"\n\n"+paragraph, realRuneCounter{}, Policy{Role: "test", MaxTokens: 1000})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if report.Passed || report.Findings[0].Code != "duplicate_paragraph" {
		t.Fatalf("report=%+v", report)
	}
}
