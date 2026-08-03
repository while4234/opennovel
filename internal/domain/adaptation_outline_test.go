package domain

import "testing"

func TestFindDuplicateAdaptationChapterOutlineUsesPreviousGroupsAndTopLevelFields(t *testing.T) {
	previous := []AdaptationChapterPlan{{
		Chapter: 1,
		Title:   "Mirror Door Signal",
		OutlineEntry: OutlineEntry{
			CoreEvent: "The team enters the tower archive and finds the sealed ledger before dawn.",
			Hook:      "The ledger names the missing witness.",
			Scenes:    []string{"archive entry", "sealed ledger"},
		},
	}}
	current := []AdaptationChapterPlan{{
		Chapter: 5,
		Title:   "Mirror Door Signal",
		OutlineEntry: OutlineEntry{
			CoreEvent: "The team enters the tower archive and finds the sealed ledger before dawn.",
			Hook:      "The ledger names the missing witness.",
			Scenes:    []string{"archive entry", "sealed ledger"},
		},
	}}

	duplicate, ok := FindDuplicateAdaptationChapterOutline(current, previous)
	if !ok {
		t.Fatal("expected duplicate against previous adaptation chapter group")
	}
	if duplicate.Chapter != 5 || duplicate.ExistingChapter != 1 {
		t.Fatalf("duplicate=%+v, want chapter 5 repeating chapter 1", duplicate)
	}
}
