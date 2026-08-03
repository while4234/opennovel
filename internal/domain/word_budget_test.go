package domain

import "testing"

func TestParseWordBudgetFromTextTotalTarget(t *testing.T) {
	budget, ok := ParseWordBudgetFromText("写一部约20万字的都市悬疑小说", WordBudgetSourcePrompt)
	if !ok {
		t.Fatal("expected word budget")
	}
	if budget.TargetTotalWords != 200000 {
		t.Fatalf("target_total_words = %d, want 200000", budget.TargetTotalWords)
	}
	if budget.TotalMinWords != 180000 || budget.TotalMaxWords != 220000 {
		t.Fatalf("range = %d-%d, want 180000-220000", budget.TotalMinWords, budget.TotalMaxWords)
	}
	if budget.Source != WordBudgetSourcePrompt {
		t.Fatalf("source = %q, want prompt", budget.Source)
	}
}

func TestParseWordBudgetFromTextShortFiveThousand(t *testing.T) {
	budget, ok := ParseWordBudgetFromText("我想写一本短篇约5000字的NTR小说", WordBudgetSourcePrompt)
	if !ok {
		t.Fatal("expected word budget")
	}
	if budget.TargetTotalWords != 5000 || budget.TotalMinWords != 4500 || budget.TotalMaxWords != 5500 {
		t.Fatalf("budget = %+v", budget)
	}
}

func TestParseWordBudgetFromTextRange(t *testing.T) {
	budget, ok := ParseWordBudgetFromText("全书80-100万字，长篇群像", "")
	if !ok {
		t.Fatal("expected word budget")
	}
	if budget.TotalMinWords != 800000 || budget.TotalMaxWords != 1000000 || budget.TargetTotalWords != 900000 {
		t.Fatalf("budget = %+v", budget)
	}
}

func TestParseWordBudgetFromTextIgnoresPerChapter(t *testing.T) {
	if budget, ok := ParseWordBudgetFromText("每章3000字，共20章", ""); ok || budget != nil {
		t.Fatalf("per-chapter budget should not become total budget: %+v", budget)
	}
}

func TestWordBudgetWithPlannedChapters(t *testing.T) {
	base, ok := NewWordBudgetFromTarget(100000, WordBudgetSourceAPI)
	if !ok {
		t.Fatal("expected budget")
	}
	budget := base.WithPlannedChapters(20)
	if budget.PlannedChapters != 20 || budget.ChapterMinWords != 4500 || budget.ChapterMaxWords != 5500 {
		t.Fatalf("planned budget = %+v", budget)
	}
}
