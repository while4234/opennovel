package domain

import "testing"

func TestFindDuplicateOutlineEntries(t *testing.T) {
	duplicate, ok := FindDuplicateOutlineEntries([]OutlineEntry{
		{Chapter: 1, Title: "鹰符潜入与幻象识破", CoreEvent: "良逸发现妖风为幻象，找到地下祭台入口。", Hook: "他看见青小竹被困。"},
		{Chapter: 2, Title: "地宫之战：围杀尹浩", CoreEvent: "三人合力斩断阵旗。", Hook: "尹浩吐出黑雾。"},
		{Chapter: 3, Title: " 鹰符潜入 与 幻象识破 ", CoreEvent: "良逸发现妖风为幻象找到地下祭台入口", Hook: "他看见青小竹被困"},
	})
	if !ok {
		t.Fatal("expected duplicate outline")
	}
	if duplicate.Chapter != 3 || duplicate.ExistingChapter != 1 {
		t.Fatalf("duplicate = %+v, want chapter 3 repeating chapter 1", duplicate)
	}
}

func TestFindDuplicateOutlineEntriesUsesPreviousGroups(t *testing.T) {
	previous := []OutlineEntry{
		{Chapter: 4, Title: "黑风审讯", CoreEvent: "良逸逼问出密道钥匙。", Hook: "钥匙裂出血纹。"},
	}
	duplicate, ok := FindDuplicateOutlineEntries([]OutlineEntry{
		{Chapter: 8, Title: "黑风审讯", CoreEvent: "良逸逼问出密道钥匙。", Hook: "钥匙裂出血纹。"},
	}, previous)
	if !ok {
		t.Fatal("expected duplicate from previous group")
	}
	if duplicate.Chapter != 8 || duplicate.ExistingChapter != 4 {
		t.Fatalf("duplicate = %+v, want chapter 8 repeating chapter 4", duplicate)
	}
}

func TestFindDuplicateOutlineEntriesFlagsRepeatedLongTitle(t *testing.T) {
	duplicate, ok := FindDuplicateOutlineEntries([]OutlineEntry{
		{
			Chapter:   3,
			Title:     "Mirror Door Signal",
			CoreEvent: "The apprentice finds the locked archive key after the midnight drill and decides to protect the witness.",
			Hook:      "A blue mark appears on the sealed door.",
			Scenes:    []string{"Archive search", "Witness bargain", "Door mark"},
		},
		{
			Chapter:   8,
			Title:     "Mirror Door Signal",
			CoreEvent: "The apprentice discovers the missing archive key during a later midnight drill and appoints himself guardian of the witness.",
			Hook:      "The same blue mark is waiting on the sealed door.",
			Scenes:    []string{"Archive search", "Witness bargain", "Door mark"},
		},
	})
	if !ok {
		t.Fatal("expected repeated long title to be treated as a duplicate outline")
	}
	if duplicate.Chapter != 8 || duplicate.ExistingChapter != 3 || duplicate.Reason != "same chapter title" {
		t.Fatalf("duplicate = %+v, want chapter 8 repeating chapter 3 by title", duplicate)
	}
}

func TestFindDuplicateOutlineEntriesFlagsSimilarDetailedText(t *testing.T) {
	duplicate, ok := FindDuplicateOutlineEntries([]OutlineEntry{
		{
			Chapter:   3,
			Title:     "North Staircase",
			CoreEvent: "The team follows the hidden signal through the archive and finds the sealed ledger before dawn.",
			Hook:      "The ledger contains a name that should have been erased.",
			Scenes: []string{
				"Archive search under pressure",
				"Sealed ledger discovery",
			},
		},
		{
			Chapter:   8,
			Title:     "South Courtyard",
			CoreEvent: "The team follows the hidden signal through the archive and finds the sealed ledger before dawn.",
			Hook:      "The ledger contains a name that should have been erased.",
			Scenes: []string{
				"Archive search under pressure",
				"Sealed ledger discovery",
			},
		},
	})
	if !ok {
		t.Fatal("expected duplicate detailed outline")
	}
	if duplicate.Chapter != 8 || duplicate.ExistingChapter != 3 {
		t.Fatalf("duplicate = %+v, want chapter 8 repeating chapter 3", duplicate)
	}
	if duplicate.Reason != "similar core_event/hook/scenes" {
		t.Fatalf("reason = %q, want similar core_event/hook/scenes", duplicate.Reason)
	}
}

func TestFindOutlineSimilarityReviewCandidatesFlagsBorderlineSimilarity(t *testing.T) {
	candidates := FindOutlineSimilarityReviewCandidates([]OutlineEntry{
		{
			Chapter:   3,
			Title:     "North Archive",
			CoreEvent: "Mira enters the archive after curfew, trades evidence with the librarian, and learns that the sealed ledger names the deputy.",
			Hook:      "The deputy's name is written in fresh ink.",
			Scenes:    []string{"Archive search", "Librarian exchange"},
		},
		{
			Chapter:   8,
			Title:     "South Archive",
			CoreEvent: "Mira enters the archive before dawn, trades evidence with the librarian, and learns that a sealed ledger names the warden.",
			Hook:      "A fresh name waits on the sealed page.",
			Scenes:    []string{"Archive search", "Librarian bargain"},
		},
	})
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v, want one borderline candidate", candidates)
	}
	candidate := candidates[0]
	if candidate.Chapter != 8 || candidate.ExistingChapter != 3 {
		t.Fatalf("candidate = %+v, want chapter 8 reviewed against chapter 3", candidate)
	}
	if candidate.DetailSimilarity < outlineReviewSimilarity || candidate.DetailSimilarity >= outlineDetailTextSimilarity {
		t.Fatalf("detail similarity = %.3f, want review band", candidate.DetailSimilarity)
	}
}

func TestFindOutlineSimilarityReviewCandidatesIgnoresLowSimilarity(t *testing.T) {
	candidates := FindOutlineSimilarityReviewCandidates([]OutlineEntry{
		{
			Chapter:   3,
			Title:     "North Archive",
			CoreEvent: "Mira enters the archive after curfew, trades evidence with the librarian, and learns that the sealed ledger names the deputy.",
			Hook:      "The deputy's name is written in fresh ink.",
			Scenes:    []string{"Archive search", "Librarian exchange"},
		},
		{
			Chapter:   8,
			Title:     "Harbor Intercept",
			CoreEvent: "The crew follows a red mark across the dormitory, corners the guard, opens a locker, and finds a ledger naming the mayor.",
			Hook:      "The mayor's seal is hidden under a loose tile.",
			Scenes:    []string{"Dormitory chase", "Locker discovery"},
		},
	})
	if len(candidates) != 0 {
		t.Fatalf("low similarity should not need review, got %+v", candidates)
	}
}

func TestFindDuplicateOutlineEntriesIgnoresIncompleteSignatures(t *testing.T) {
	if duplicate, ok := FindDuplicateOutlineEntries([]OutlineEntry{
		{Chapter: 1, Title: "同题", CoreEvent: "", Hook: "同钩子"},
		{Chapter: 2, Title: "同题", CoreEvent: "", Hook: "同钩子"},
	}); ok {
		t.Fatalf("incomplete signatures should not duplicate, got %+v", duplicate)
	}
}
