package domain

import "testing"

func TestResolveChapterWordBudgetPolicyKeepsRecommendationSoft(t *testing.T) {
	runtime := WordBudgetRuntime{
		Target: WordBudgetTarget{TargetTotalWords: 200000, TotalMinWords: 180000, TotalMaxWords: 220000},
		Remaining: WordBudgetRemaining{
			MinWords: 180000,
			MaxWords: 220000,
			Chapters: 55,
		},
		CurrentChapter: CurrentChapterWordBudget{
			Chapter:             1,
			RecommendedMinWords: 3273,
			RecommendedMaxWords: 4000,
		},
	}
	policy, ok := ResolveChapterWordBudgetPolicy(runtime, 3000, 6000, true)
	if !ok {
		t.Fatal("expected policy")
	}
	if policy.HardMinWords != 2000 || policy.HardMaxWords != 7500 {
		t.Fatalf("safety range = %d-%d, want 2000-7500", policy.HardMinWords, policy.HardMaxWords)
	}
	if policy.WithinRecommendation(4165) {
		t.Fatal("4165 should remain above the soft recommendation")
	}
	if !policy.WithinHardRange(4165) || !policy.WithinHardRange(6573) {
		t.Fatalf("quality-preserving chapters should pass the safety range: %+v", policy)
	}
}

func TestResolveChapterWordBudgetPolicyDoesNotCompressLaterChapters(t *testing.T) {
	runtime := WordBudgetRuntime{
		Remaining: WordBudgetRemaining{
			MinWords: 7000,
			MaxWords: 7000,
			Chapters: 2,
		},
		CurrentChapter: CurrentChapterWordBudget{
			Chapter:             9,
			RecommendedMinWords: 3500,
			RecommendedMaxWords: 3500,
		},
	}
	policy, ok := ResolveChapterWordBudgetPolicy(runtime, 3000, 6000, true)
	if !ok {
		t.Fatal("expected policy")
	}
	if policy.HardMinWords != 2000 || policy.HardMaxWords != 7500 {
		t.Fatalf("approximate total compressed chapter safety range to %d-%d", policy.HardMinWords, policy.HardMaxWords)
	}
}

func TestResolveChapterWordBudgetPolicyKeepsRunawaySafetyFence(t *testing.T) {
	runtime := WordBudgetRuntime{
		CurrentChapter: CurrentChapterWordBudget{
			Chapter:             4,
			RecommendedMinWords: 3273,
			RecommendedMaxWords: 4000,
		},
	}
	policy, ok := ResolveChapterWordBudgetPolicy(runtime, 3000, 6000, true)
	if !ok {
		t.Fatal("expected policy")
	}
	if policy.WithinHardRange(1999) || policy.WithinHardRange(9001) {
		t.Fatalf("runaway safety fence did not reject extreme draft: %+v", policy)
	}
}
