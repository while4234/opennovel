package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSnapshotIncludesSimulationAndBlueprintSummaries(t *testing.T) {
	h := newSimulationTestHost(t)

	if err := h.store.Outline.SavePremise("# 月城\n雾中的城市。"); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := h.store.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter:   1,
		Title:     "雨夜",
		CoreEvent: "主角收到旧案录音",
		Hook:      "录音来自未来",
		Scenes:    []string{"地铁站", "档案室"},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := h.store.Simulation.Save(domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		UpdatedAt: "2026-07-02T10:00:00Z",
		Corpus: domain.SimulationCorpusManifest{Sources: []domain.SimulationSource{
			{RelativePath: "sample-a.txt", SHA256: "sha-a"},
			{RelativePath: "sample-b.txt", SHA256: "sha-b"},
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"冷静近景"},
			},
			HookDesign: domain.SimulationHookDesign{
				HookTypes: []string{"反转钩子"},
			},
			ReaderEngagement: domain.SimulationReaderEngagement{
				Methods: []string{"线索奖励"},
			},
		},
	}); err != nil {
		t.Fatalf("Simulation.Save: %v", err)
	}

	snap := h.Snapshot()
	if snap.SimulationSummary == nil || !snap.SimulationSummary.Loaded || snap.SimulationSummary.SourceCount != 2 {
		t.Fatalf("simulation summary missing: %+v", snap.SimulationSummary)
	}
	if snap.SimulationProfile != nil {
		t.Fatal("regular snapshot must not include the compact simulation profile")
	}
	data, err := json.Marshal(snap.SimulationSummary)
	if err != nil {
		t.Fatalf("marshal simulation summary: %v", err)
	}
	if len(data) > 64<<10 {
		t.Fatalf("simulation summary is unbounded: %d bytes", len(data))
	}
	for _, forbidden := range []string{"sample-a.txt", "sample-b.txt", "source_reports", "source_dir"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("simulation summary leaked %q: %s", forbidden, data)
		}
	}
	if snap.CreativeBlueprint == nil || !snap.CreativeBlueprint.Loaded || snap.CreativeBlueprint.OutlineChapters != 1 {
		t.Fatalf("creative blueprint missing: %+v", snap.CreativeBlueprint)
	}
}

func TestSnapshotUsesAdaptationProposalForChapterVisibility(t *testing.T) {
	h := newSimulationTestHost(t)
	progress := &domain.Progress{
		NovelName:         "改编书",
		Phase:             domain.PhaseWriting,
		TotalChapters:     2,
		CompletedChapters: []int{1},
		TotalWordCount:    980,
		ChapterWordCounts: map[int]int{1: 980},
	}
	if err := h.store.Progress.Save(progress); err != nil {
		t.Fatalf("Progress.Save: %v", err)
	}
	if err := h.store.Adaptation.SaveProposal(domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityFree,
		RewritePolicy:    domain.AdaptationRewriteFullRewrite,
		Brief:            "改成雨城悬疑",
		SourceTotalRunes: 1800,
		TargetTotalRunes: 2200,
		Chapters: []domain.AdaptationChapterPlan{
			{
				Chapter:        1,
				Title:          "旧录音",
				SourceChapters: []int{1, 2},
				SourceRunes:    900,
				TargetRunes:    1100,
				TargetMinRunes: 935,
				TargetMaxRunes: 1265,
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "侦探发现录音时间错位",
					Hook:      "门外响起录音里的脚步声",
					Scenes:    []string{"事务所", "雨巷"},
				},
				WordBudget: &domain.AdaptationChapterWordBudget{
					SourceRunes: 900,
					TargetRunes: 1100,
					MinRunes:    935,
					MaxRunes:    1265,
					Tolerance:   0.15,
				},
				CoverageNote:    "合并前两章主线",
				RequiredChanges: []string{"现代化人物动机"},
				PreserveEvents:  []string{"保留录音线索"},
				ForbiddenMoves:  []string{"不要照抄原文对白"},
			},
		},
	}); err != nil {
		t.Fatalf("Adaptation.SaveProposal: %v", err)
	}

	snap := h.Snapshot()
	if snap.ProposalSummary == nil || snap.ProposalSummary.Status != domain.AdaptationPlanStatusProposal {
		t.Fatalf("proposal summary missing: %+v", snap.ProposalSummary)
	}
	if len(snap.Outline) != 1 {
		t.Fatalf("outline len = %d, want proposal chapter", len(snap.Outline))
	}
	chapter := snap.Outline[0]
	if chapter.WrittenWordCount != 980 {
		t.Fatalf("written words = %d, want 980", chapter.WrittenWordCount)
	}
	if chapter.WordBudget == nil || chapter.WordBudget.TargetRunes != 1100 || chapter.WordBudget.MinRunes != 935 {
		t.Fatalf("word budget missing: %+v", chapter.WordBudget)
	}
	if chapter.SourceCoverage == nil || chapter.SourceCoverage.From != 1 || chapter.SourceCoverage.To != 2 || chapter.SourceCoverage.Runes != 900 {
		t.Fatalf("source coverage missing: %+v", chapter.SourceCoverage)
	}
	if len(chapter.RequiredChanges) != 1 || len(chapter.PreserveEvents) != 1 || len(chapter.ForbiddenMoves) != 1 {
		t.Fatalf("adaptation detail fields missing: %+v", chapter)
	}
}
