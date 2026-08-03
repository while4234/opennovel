package adapt

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
)

func TestSplitSourceAnalysisSceneBatchesPreservesWholeParagraphs(t *testing.T) {
	first := strings.Repeat("甲", 9)
	second := strings.Repeat("乙", 9)
	third := strings.Repeat("丙", 9)
	batches, err := splitSourceAnalysisSceneBatches(strings.Join([]string{first, second, third}, "\n\n"), 20)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(batches) != 2 || batches[0] != first+"\n\n"+second || batches[1] != third {
		t.Fatalf("batches=%q", batches)
	}
}

func TestSplitSourceAnalysisSceneBatchesRejectsOneOversizedScene(t *testing.T) {
	if _, err := splitSourceAnalysisSceneBatches(strings.Repeat("甲", 21), 20); err == nil {
		t.Fatal("one oversized scene must not be cut at an arbitrary rune")
	}
}

func TestMergeSourceChapterAnalysesKeepsOrderedFacts(t *testing.T) {
	merged := mergeSourceChapterAnalyses([]*imp.ChapterAnalysis{
		{Summary: "前场", Characters: []string{"甲"}, KeyEvents: []string{"相遇"}, HookType: "mystery", DominantStrand: "quest"},
		{Summary: "后场", Characters: []string{"甲", "乙"}, KeyEvents: []string{"同行"}, HookType: "choice", DominantStrand: "fire"},
	})
	if got := strings.Join(merged.KeyEvents, ","); got != "相遇,同行" {
		t.Fatalf("events=%s", got)
	}
	if len(merged.Characters) != 2 || merged.HookType != "choice" || merged.DominantStrand != "quest" {
		t.Fatalf("merged=%+v", merged)
	}
}

func TestMergeSourceChapterAnalysesPreservesRichCharacterEvidence(t *testing.T) {
	merged := mergeSourceChapterAnalyses([]*imp.ChapterAnalysis{
		{CharacterProfiles: []domain.Character{{
			ID: "ari", Name: "Ari", Gender: "female",
			ContrastDetails: []domain.CharacterContrastDetail{{Surface: "reserved", Depth: "decisive"}},
		}}},
		{CharacterProfiles: []domain.Character{{
			ID:           "ari",
			KeyBackstory: []domain.CharacterBackstory{{Event: "lost a record", Impact: "verifies every lead"}},
		}}},
	})
	if len(merged.CharacterProfiles) != 1 {
		t.Fatalf("profiles=%+v", merged.CharacterProfiles)
	}
	got := merged.CharacterProfiles[0]
	if got.Gender != "female" || len(got.ContrastDetails) != 1 || len(got.KeyBackstory) != 1 {
		t.Fatalf("rich source evidence was lost: %+v", got)
	}
}
