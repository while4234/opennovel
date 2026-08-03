package adapt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/litellm"
)

func TestBuildPlanFromBriefSupportsGranularities(t *testing.T) {
	reports := []domain.AdaptationSourceReport{
		{Chapter: 1, Title: "起", KeyEvents: []string{"主角入局"}},
		{Chapter: 2, Title: "承", KeyEvents: []string{"女主登场"}},
	}
	cases := []struct {
		name  string
		brief string
		want  string
	}{
		{name: "chapter default", brief: "逐章改写，主线不要走偏", want: domain.AdaptationGranularityChapter},
		{name: "arc", brief: "允许按弧合并拆分章节，但保留主线", want: domain.AdaptationGranularityArc},
		{name: "free", brief: "自由重构章节结构，核心命运不变", want: domain.AdaptationGranularityFree},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildPlanFromBrief(tc.brief, reports)
			if plan.Granularity != tc.want {
				t.Fatalf("granularity=%s, want %s", plan.Granularity, tc.want)
			}
			if len(plan.Chapters) != len(reports) {
				t.Fatalf("chapters=%d, want %d", len(plan.Chapters), len(reports))
			}
			if got := plan.Chapters[0].SourceChapters; len(got) != 1 || got[0] != 1 {
				t.Fatalf("source refs not preserved: %+v", got)
			}
			if len(plan.Chapters[0].PreserveEvents) == 0 {
				t.Fatalf("preserve events should come from source reports")
			}
		})
	}
}

func TestPrepareRunWorksAfterResetGenerated(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "Opening", "source chapter body")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	report := domain.AdaptationSourceReport{Chapter: 1, Title: "Opening", SourceSHA256: source.SHA256, Summary: "Ari starts", KeyEvents: []string{"Ari accepts the call"}}
	if err := st.Adaptation.SaveSourceReport(report); err != nil {
		t.Fatalf("SaveSourceReport: %v", err)
	}
	if err := st.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{report}); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := st.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Brief:       "old generated plan",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "Old", SourceChapters: []int{1}},
		},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: store.TextSHA256("old draft"),
		Passed:      true,
		CheckedAt:   "2026-06-29T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	if err := st.Adaptation.ResetGenerated(); err != nil {
		t.Fatalf("ResetGenerated: %v", err)
	}

	brief := "chapter rewrite with warmer relationship beats"
	seedConfirmedAdaptationTargetFoundation(t, st, manifest, brief)
	plan, err := PrepareRun(context.Background(), Deps{Store: st}, brief)
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if plan.Brief != brief || plan.Granularity != domain.AdaptationGranularityChapter {
		t.Fatalf("plan mismatch: %+v", plan)
	}
	if len(plan.Chapters) != 1 || len(plan.Chapters[0].SourceChapters) != 1 || plan.Chapters[0].SourceChapters[0] != 1 {
		t.Fatalf("chapter plan should come from source reports: %+v", plan.Chapters)
	}

	savedPlan, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if savedPlan == nil || savedPlan.Brief != brief {
		t.Fatalf("saved plan mismatch: %+v", savedPlan)
	}
	if check, err := st.Adaptation.LoadCheck(1); err != nil || check != nil {
		t.Fatalf("old checks should stay removed: check=%+v err=%v", check, err)
	}
	premise, err := st.Outline.LoadPremise()
	if err != nil {
		t.Fatalf("LoadPremise: %v", err)
	}
	if !strings.Contains(premise, brief) {
		t.Fatalf("adaptation brief should be persisted into premise: %q", premise)
	}
	sourceText, loadedSource, err := st.Adaptation.LoadSourceChapter(1)
	if err != nil {
		t.Fatalf("LoadSourceChapter: %v", err)
	}
	if sourceText != "source chapter body" || loadedSource == nil {
		t.Fatalf("source snapshot should remain available: text=%q source=%+v", sourceText, loadedSource)
	}
}

func TestConfirmAdaptationProposalPersistsTargetOutlinesAndProgress(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	proposal := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityArc,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "merge three source chapters into two target chapters",
		Chapters: []domain.AdaptationChapterPlan{
			{
				Chapter:        1,
				Title:          "Merged Opening",
				SourceChapters: []int{1, 2},
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "Ari combines the first two source turns.",
					Hook:      "A shared clue reframes both turns.",
					Scenes:    []string{"station", "archive"},
				},
			},
			{
				Chapter:        2,
				Title:          "Target Turn",
				SourceChapters: []int{3},
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "Ari pays off the third source turn.",
					Hook:      "The next door opens.",
					Scenes:    []string{"roof"},
				},
			},
		},
	}
	proposal.FoundationBinding = seedDirectConfirmedAdaptationTargetFoundation(t, st, 3, proposal.Brief)

	confirmed, err := ConfirmAdaptationProposal(context.Background(), Deps{Store: st}, proposal)
	if err != nil {
		t.Fatalf("ConfirmAdaptationProposal: %v", err)
	}
	if confirmed.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("confirmed status=%q, want confirmed", confirmed.Status)
	}
	flat, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(flat) != 2 || flat[0].Title != "Merged Opening" || flat[1].Title != "Target Turn" {
		t.Fatalf("flat outline should come from target plan: %+v", flat)
	}
	layered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	if got := domain.TotalChapters(layered); got != 2 {
		t.Fatalf("layered target chapters=%d, want 2: %+v", got, layered)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if progress == nil || progress.TotalChapters != 2 || len(progress.CompletedChapters) != 0 {
		t.Fatalf("progress should be reset to target count: %+v", progress)
	}
	savedPlan, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if savedPlan == nil || savedPlan.Status != domain.AdaptationPlanStatusConfirmed || len(savedPlan.Chapters) != 2 {
		t.Fatalf("confirmed plan not saved: %+v", savedPlan)
	}
}

func TestConfirmAdaptationProposalRejectsDuplicateTargetOutlines(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	proposal := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityArc,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "duplicate proposal should not become a confirmed plan",
		Chapters: []domain.AdaptationChapterPlan{
			{
				Chapter:        1,
				Title:          "Mirror Door Signal",
				SourceChapters: []int{1},
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "The team enters the tower archive and finds the sealed ledger before dawn.",
					Hook:      "The ledger names the missing witness.",
					Scenes:    []string{"archive entry", "sealed ledger"},
				},
			},
			{
				Chapter:        2,
				Title:          "Mirror Door Signal",
				SourceChapters: []int{2},
				OutlineEntry: domain.OutlineEntry{
					CoreEvent: "The team enters the tower archive and finds the sealed ledger before dawn.",
					Hook:      "The ledger names the missing witness.",
					Scenes:    []string{"archive entry", "sealed ledger"},
				},
			},
		},
	}
	proposal.FoundationBinding = seedDirectConfirmedAdaptationTargetFoundation(t, st, 2, proposal.Brief)

	_, err := ConfirmAdaptationProposal(context.Background(), Deps{Store: st}, proposal)
	if err == nil {
		t.Fatal("duplicate proposal should be rejected before confirmation")
	}
	if !strings.Contains(err.Error(), "chapter 2 duplicates outline beats from chapter 1") {
		t.Fatalf("error=%v, want duplicate outline rejection", err)
	}
}

func TestAdaptationTargetVolumesSplitsLargeParentVolumeIntoSmallArcs(t *testing.T) {
	plan := domain.AdaptationPlan{
		Brief: "case parent batch",
		Volumes: []domain.AdaptationVolumePlan{{
			Index:      1,
			Title:      "Case Parent",
			Theme:      "one coherent case",
			Goal:       "preserve the parent story movement",
			TargetFrom: 1,
			TargetTo:   10,
		}},
		Chapters: plannerSharedSourceRangePlans(1, 10, 1, 2, 10000),
	}

	volumes := adaptationTargetVolumes(plan)
	if len(volumes) != 1 {
		t.Fatalf("volumes=%d, want one parent volume", len(volumes))
	}
	arcs := volumes[0].Arcs
	if len(arcs) != 3 {
		t.Fatalf("arcs=%d, want 3 small generated arcs", len(arcs))
	}
	ranges := []struct {
		from int
		to   int
	}{
		{1, 4},
		{5, 8},
		{9, 10},
	}
	for idx, want := range ranges {
		arc := arcs[idx]
		if len(arc.Chapters) != want.to-want.from+1 {
			t.Fatalf("arc %d chapters=%d, want %d", idx+1, len(arc.Chapters), want.to-want.from+1)
		}
		if arc.Chapters[0].Chapter != want.from || arc.Chapters[len(arc.Chapters)-1].Chapter != want.to {
			t.Fatalf("arc %d range=%d-%d, want %d-%d", idx+1, arc.Chapters[0].Chapter, arc.Chapters[len(arc.Chapters)-1].Chapter, want.from, want.to)
		}
	}
	if volumes[0].Title != "Case Parent" {
		t.Fatalf("parent volume title = %q, want preserved parent title", volumes[0].Title)
	}
}

func TestConfirmAdaptationProposalRejectsDuplicateParentBatch(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	chapters := plannerSharedSourceRangePlans(1, 5, 1, 2, 5000)
	chapters[4].Title = chapters[0].Title
	chapters[4].OutlineEntry.Title = chapters[0].OutlineEntry.Title
	chapters[4].CoreEvent = chapters[0].CoreEvent
	chapters[4].OutlineEntry.CoreEvent = chapters[0].OutlineEntry.CoreEvent
	chapters[4].Hook = chapters[0].Hook
	chapters[4].OutlineEntry.Hook = chapters[0].OutlineEntry.Hook
	proposal := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityArc,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "parent duplicate should fail",
		Volumes: []domain.AdaptationVolumePlan{{
			Index:      1,
			Title:      "Case Parent",
			TargetFrom: 1,
			TargetTo:   5,
		}},
		Chapters: chapters,
	}
	proposal.FoundationBinding = seedDirectConfirmedAdaptationTargetFoundation(t, st, 2, proposal.Brief)

	_, err := ConfirmAdaptationProposal(context.Background(), Deps{Store: st}, proposal)
	if err == nil {
		t.Fatal("expected duplicate parent batch to be rejected")
	}
	if !strings.Contains(err.Error(), "parent batch contains duplicate chapter outline") {
		t.Fatalf("error=%v, want parent batch duplicate guidance", err)
	}
}

func TestBuildAdaptationProposalChapterPreserveDetailsUsesSourceRuneRanges(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source1, err := st.Adaptation.SaveSourceChapter(1, "One", strings.Repeat("一", 10))
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	source2, err := st.Adaptation.SaveSourceChapter(2, "Two", strings.Repeat("二", 20))
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	source3, err := st.Adaptation.SaveSourceChapter(3, "Three", strings.Repeat("三", 30))
	if err != nil {
		t.Fatalf("SaveSourceChapter 3: %v", err)
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 3,
		Chapters:     []domain.AdaptationSource{source1, source2, source3},
	}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	reports := []domain.AdaptationSourceReport{
		{Chapter: 1, Title: "One", SourceSHA256: source1.SHA256, Summary: "one", KeyEvents: []string{"event one"}},
		{Chapter: 2, Title: "Two", SourceSHA256: source2.SHA256, Summary: "two", KeyEvents: []string{"event two"}},
		{Chapter: 3, Title: "Three", SourceSHA256: source3.SHA256, Summary: "three", KeyEvents: []string{"event three"}},
	}
	for _, report := range reports {
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatalf("SaveSourceReport %d: %v", report.Chapter, err)
		}
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	seedConfirmedAdaptationTargetFoundation(t, st, manifest, "逐章保留原著细节")

	proposal, err := BuildAdaptationProposal(Deps{Store: st}, ProposalOptions{
		Brief:         "逐章保留原著细节",
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		WordTolerance: 0.15,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if proposal.Status != domain.AdaptationPlanStatusProposal {
		t.Fatalf("status=%s, want proposal", proposal.Status)
	}
	if st.Adaptation.Active() {
		t.Fatal("proposal should not activate adaptation project")
	}
	if len(proposal.Chapters) != 3 {
		t.Fatalf("chapters=%d, want 3", len(proposal.Chapters))
	}
	wantRanges := []struct {
		source int
		min    int
		max    int
	}{
		{source: 10, min: 9, max: 12},
		{source: 20, min: 17, max: 23},
		{source: 30, min: 26, max: 35},
	}
	for i, want := range wantRanges {
		chapter := proposal.Chapters[i]
		if chapter.Chapter != i+1 || len(chapter.SourceChapters) != 1 || chapter.SourceChapters[0] != i+1 {
			t.Fatalf("chapter mapping mismatch at %d: %+v", i, chapter)
		}
		if chapter.SourceRunes != want.source || chapter.TargetRunes != want.source {
			t.Fatalf("source/target runes mismatch at %d: %+v", i, chapter)
		}
		if chapter.TargetMinRunes != want.min || chapter.TargetMaxRunes != want.max {
			t.Fatalf("range mismatch at %d: got %d-%d want %d-%d", i, chapter.TargetMinRunes, chapter.TargetMaxRunes, want.min, want.max)
		}
	}
	if proposal.SourceTotalRunes != 60 || proposal.TargetTotalRunes != 60 || proposal.TargetMinRunes != 51 || proposal.TargetMaxRunes != 69 {
		t.Fatalf("total range mismatch: %+v", proposal)
	}
}

func TestBuildAdaptationProposalArcUsesPlannerForFewerTargetChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30})
	brief := "arc restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				"chapter": 1,
				"title": "Merged opening",
				"core_event": "Ari combines the first two source turns.",
				"hook": "A shared clue reframes both turns.",
				"scenes": ["station", "archive"],
				"source_chapters": [1, 2],
				"source_range": {"from": 1, "to": 2},
				"word_budget": {"source_words": 30, "target_words": 35, "min_words": 30, "max_words": 40, "tolerance": 0.15}
			},
			{
				"chapter": 2,
				"title": "New turn",
				"core_event": "Ari pays off the third source turn.",
				"hook": "The choice opens the next door.",
				"scenes": ["roof"],
				"source_chapters": [3],
				"source_range": {"from": 3, "to": 3},
				"word_budget": {"source_runes": 30, "target_runes": 32, "min_runes": 28, "max_runes": 38, "tolerance": 0.15}
			}
		]
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{
		Store: st,
		LLM:   llm,
		Prompts: Prompts{
			Planner: "planner system prompt",
		},
	}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityArc,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want 1", llm.calls)
	}
	if len(llm.got) != 1 || !strings.Contains(llm.got[0][0].TextContent(), "planner system prompt") {
		t.Fatalf("planner prompt not sent: %+v", llm.got)
	}
	plannerInput := llm.got[0][1].TextContent()
	if !strings.Contains(plannerInput, `"source_foundation"`) || !strings.Contains(plannerInput, `"source_reports"`) {
		t.Fatalf("planner input should include source foundation and reports: %s", plannerInput)
	}
	if proposal.Status != domain.AdaptationPlanStatusProposal || proposal.RewritePolicy != domain.AdaptationRewriteFullRewrite {
		t.Fatalf("proposal mode fields mismatch: %+v", proposal)
	}
	if len(proposal.Chapters) != 2 {
		t.Fatalf("chapters=%d, want planner-provided 2", len(proposal.Chapters))
	}
	if got := proposal.Chapters[0].SourceChapters; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("merged source anchors not preserved: %+v", got)
	}
	if proposal.TargetTotalRunes != 67 {
		t.Fatalf("target total=%d, want summed planner budget 67", proposal.TargetTotalRunes)
	}
	if st.Adaptation.Active() {
		t.Fatal("proposal should not activate adaptation project")
	}
	savedPlan, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if savedPlan != nil {
		t.Fatalf("BuildAdaptationProposal should not save confirmed plan: %+v", savedPlan)
	}
}

func TestBuildAdaptationProposalFreeUsesPlannerForMoreTargetChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	brief := "free restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free restructure",
		"planner": {"prompt": "adaptation-planner", "prompt_version": "v1", "model": "fake"},
		"chapters": [
			{
				"chapter": 1,
				"title": "Opening focus",
				"core_event": "Ari reframes the first source turn.",
				"hook": "The clue points inward.",
				"scenes": ["station"],
				"source_chapters": [1],
				"word_budget": {"source_runes": 10, "target_runes": 12, "min_runes": 10, "max_runes": 15, "tolerance": 0.15}
			},
			{
				"chapter": 2,
				"title": "Inserted bridge",
				"core_event": "Ari makes the missing emotional choice visible.",
				"hook": "The bridge forces a confession.",
				"scenes": ["alley"],
				"source_chapters": [1],
				"is_added": true,
				"word_budget": {"source_runes": 0, "target_runes": 10, "min_runes": 8, "max_runes": 12, "tolerance": 0.15}
			},
			{
				"chapter": 3,
				"title": "Second turn",
				"core_event": "Ari resolves the second source turn.",
				"hook": "The cost is named.",
				"scenes": ["archive"],
				"source_chapters": [2],
				"word_budget": {"source_runes": 20, "target_runes": 22, "min_runes": 18, "max_runes": 25, "tolerance": 0.15}
			}
		]
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityFree,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if len(proposal.Chapters) != 3 {
		t.Fatalf("chapters=%d, want planner-provided 3", len(proposal.Chapters))
	}
	if !proposal.Chapters[1].IsAdded || len(proposal.Chapters[1].SourceChapters) != 1 || proposal.Chapters[1].SourceChapters[0] != 1 {
		t.Fatalf("added chapter should keep source anchor: %+v", proposal.Chapters[1])
	}
	if proposal.Chapters[1].SourceRange.From != 1 || proposal.Chapters[1].SourceRange.To != 1 {
		t.Fatalf("added chapter source range should be derived from anchor: %+v", proposal.Chapters[1].SourceRange)
	}
	if proposal.Planner == nil || proposal.Planner.Prompt != "adaptation-planner" || proposal.Planner.Model != "fake" {
		t.Fatalf("planner metadata not preserved: %+v", proposal.Planner)
	}
}

func TestBuildAdaptationProposalSinglePlannerPromptHasExplicitJSONContract(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				"chapter": 1,
				"title": "Opening",
				"core_event": "Ari adapts the source.",
				"hook": "The clue points onward.",
				"scenes": ["archive"],
				"source_chapters": [1, 2],
				"source_range": {"from": 1, "to": 2},
				"word_budget": {"source_runes": 30, "target_runes": 32, "min_runes": 30, "max_runes": 34, "tolerance": 0.15},
				"preserve_events": ["source event"],
				"required_changes": ["adapt the beat"],
				"forbidden_moves": ["drop the source anchor"]
			}
		]
	}`}}}

	if _, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       "arc restructure",
		Granularity: domain.AdaptationGranularityArc,
	}); err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	prompt := llm.got[0][1].TextContent()
	if !strings.Contains(prompt, "top-level object must contain a chapters array") ||
		!strings.Contains(prompt, "Required shape") ||
		!strings.Contains(prompt, `Invalid shapes: {"chapter":1`) ||
		!strings.Contains(prompt, "Every chapter field must be an integer") {
		t.Fatalf("single planner prompt should contain explicit JSON contract: %s", prompt)
	}
}

func TestBuildAdaptationProposalFreeUsesChunkedPlannerForLongTargetChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20章"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20章",
			"target_chapter_count": 20,
			"mainline_rules": ["keep every source turn anchored"],
			"relationship_goals": ["slow emotional escalation"],
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 2)},
		{text: plannerBatchProposalJSON(5, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 12, 2, 3)},
		{text: plannerBatchProposalJSON(13, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 6 {
		t.Fatalf("planner calls=%d, want skeleton + 5 detail calls", llm.calls)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
	if proposal.Chapters[8].Chapter != 9 || proposal.Chapters[19].Chapter != 20 {
		t.Fatalf("batch chapters should keep absolute numbering: %+v", proposal.Chapters)
	}
	if proposal.Planner == nil || proposal.Planner.PromptVersion != "v1-chunked" {
		t.Fatalf("planner metadata should mark chunked run: %+v", proposal.Planner)
	}
	firstPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(firstPrompt, `"target_chapter_hint": 20`) ||
		!strings.Contains(firstPrompt, "do not mechanically mirror or compress source chapters") ||
		!strings.Contains(firstPrompt, "top-level object must contain a batches array") ||
		!strings.Contains(firstPrompt, "The host will concatenate all source-map portions") {
		t.Fatalf("skeleton prompt should carry long-form target and model-planned split instruction: %s", firstPrompt)
	}
	secondBatchPrompt := llm.got[3][1].TextContent()
	if !strings.Contains(secondBatchPrompt, `"target_from": 9`) ||
		!strings.Contains(secondBatchPrompt, `"target_to": 12`) ||
		!strings.Contains(secondBatchPrompt, `top-level object must be {"chapters":[...]}`) ||
		!strings.Contains(secondBatchPrompt, "Return exactly 4 chapter objects") ||
		!strings.Contains(secondBatchPrompt, `Invalid shapes: {"chapter":9`) {
		t.Fatalf("batch prompt should use skeleton-provided range and explicit JSON contract: %s", secondBatchPrompt)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 20 {
		t.Fatalf("chunked proposal should be saved as proposal only: %+v", saved)
	}
	if st.Adaptation.Active() {
		t.Fatal("proposal should not activate adaptation project")
	}
}

func TestBuildAdaptationProposalFreeDefaultsLongSourceToChunkedPlanner(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 17)
	for i := range runeCounts {
		runeCounts[i] = 10 + i
	}
	seedPreparedAdaptationSource(t, st, runeCounts)
	brief := "free long-form expansion without an explicit chapter count"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free long-form expansion without an explicit chapter count",
			"target_chapter_count": 18,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 8, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 9, "source_to": 16, "summary": "expand the middle"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 18, "source_from": 17, "source_to": 17, "summary": "resolve the ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 8)},
		{text: plannerBatchProposalJSON(5, 8, 1, 8)},
		{text: plannerBatchProposalJSON(9, 12, 9, 16)},
		{text: plannerBatchProposalJSON(13, 16, 9, 16)},
		{text: plannerBatchProposalJSON(17, 18, 17, 17)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityFree,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 6 {
		t.Fatalf("planner calls=%d, want default skeleton + 5 detail calls", llm.calls)
	}
	if len(proposal.Chapters) != 18 {
		t.Fatalf("chapters=%d, want 18", len(proposal.Chapters))
	}
	if proposal.Planner == nil || proposal.Planner.PromptVersion != "v1-chunked" {
		t.Fatalf("planner metadata should mark chunked run: %+v", proposal.Planner)
	}
	firstPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(firstPrompt, `"target_chapter_hint": 18`) {
		t.Fatalf("skeleton prompt should carry default target chapter hint: %s", firstPrompt)
	}
	if !strings.Contains(firstPrompt, `"source_map"`) || strings.Contains(firstPrompt, `"source_reports"`) {
		t.Fatalf("skeleton prompt should use compact source_map instead of full reports: %s", firstPrompt)
	}
}

func TestBuildAdaptationProposalLongSourceUsesChunkedPlannerDespiteSmallTarget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 20)
	for i := range runeCounts {
		runeCounts[i] = 10 + i
	}
	seedPreparedAdaptationSource(t, st, runeCounts)
	brief := "free restructure into 12\u7ae0"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 12 chapters",
			"target_chapter_count": 12,
			"batches": [
				{"index": 1, "title": "Compact full-book arc", "theme": "focused rewrite", "target_from": 1, "target_to": 12, "source_from": 1, "source_to": 20, "summary": "compress the whole source through one coherent arc"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 20)},
		{text: plannerBatchProposalJSON(5, 8, 1, 20)},
		{text: plannerBatchProposalJSON(9, 12, 1, 20)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityFree,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 4 {
		t.Fatalf("planner calls=%d, want skeleton + 3 detail calls", llm.calls)
	}
	if len(proposal.Chapters) != 12 {
		t.Fatalf("chapters=%d, want 12", len(proposal.Chapters))
	}
	firstPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(firstPrompt, `"target_chapter_hint": 12`) {
		t.Fatalf("skeleton prompt should preserve small target hint: %s", firstPrompt)
	}
	if !strings.Contains(firstPrompt, `"source_map"`) || strings.Contains(firstPrompt, `"source_reports"`) {
		t.Fatalf("long source should use compact source_map instead of full reports: %s", firstPrompt)
	}
}

func TestBuildAdaptationProposalVolumesLongSourceUsesReviewDespiteSmallTarget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 20)
	for i := range runeCounts {
		runeCounts[i] = 10 + i
	}
	seedPreparedAdaptationSource(t, st, runeCounts)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc rewrite into 12 chapters",
		"target_chapter_count": 12,
		"batches": [
			{"index": 1, "title": "Focused arc", "theme": "compression", "target_from": 1, "target_to": 12, "source_from": 1, "source_to": 20, "summary": "review the compact long-source arc before details"}
		]
	}`}}}

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              "arc rewrite into 12\u7ae0",
		Granularity:        domain.AdaptationGranularityArc,
		TargetChapterCount: 12,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalVolumesContext: %v", err)
	}
	if result == nil || result.VolumeReview == nil || result.Proposal != nil {
		t.Fatalf("long source should return volume review only, got %+v", result)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want one skeleton call", llm.calls)
	}
	if result.VolumeReview.TargetChapterCount != 12 {
		t.Fatalf("target chapters=%d, want 12", result.VolumeReview.TargetChapterCount)
	}
	firstPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(firstPrompt, `"source_map"`) || strings.Contains(firstPrompt, `"source_reports"`) {
		t.Fatalf("volume review prompt should use compact source_map instead of full reports: %s", firstPrompt)
	}
}

func TestBuildAdaptationProposalCoversSparseSourceAnchorsByExplicitRange(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30})
	brief := "arc restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				"chapter": 1,
				"title": "Merged opening",
				"core_event": "Ari merges the first two source turns.",
				"hook": "The merged pressure points forward.",
				"scenes": ["station"],
				"source_chapters": [1],
				"source_range": {"from": 1, "to": 2},
				"preserve_events": ["source event"],
				"required_changes": ["merge the first span"],
				"forbidden_moves": ["drop the second source chapter"]
			},
			{
				"chapter": 2,
				"title": "Closing turn",
				"core_event": "Ari resolves the final source turn.",
				"hook": "The ending opens a new question.",
				"scenes": ["archive"],
				"source_chapters": [3],
				"source_range": {"from": 3, "to": 3},
				"preserve_events": ["source event"],
				"required_changes": ["adapt the final span"],
				"forbidden_moves": ["drop the final source chapter"]
			}
		]
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityArc,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want single planner call", llm.calls)
	}
	if proposal.Chapters[0].WordBudget == nil || proposal.Chapters[0].WordBudget.SourceRunes != 30 {
		t.Fatalf("explicit source_range should drive covered source runes: %+v", proposal.Chapters[0])
	}
	if got := proposal.Chapters[0].SourceChapters; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("explicit source_range should expand saved source_chapters for later tools: %+v", proposal.Chapters[0])
	}
}

func TestBuildAdaptationProposalUsesParentSourceRangeForChunkedFinalCoverage(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 1)},
		{text: plannerBatchProposalJSON(5, 8, 1, 1)},
		{text: plannerBatchProposalJSON(9, 12, 2, 2)},
		{text: plannerBatchProposalJSON(13, 16, 2, 2)},
		{text: plannerBatchProposalJSON(17, 20, 3, 3)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if proposal == nil || len(proposal.Chapters) != 20 {
		t.Fatalf("proposal=%+v, want 20 chapters", proposal)
	}
	if got := proposal.Chapters[16].SourceRange; got.From != 3 || got.To != 4 {
		t.Fatalf("final parent source_range=%+v, want 3-4", got)
	}
	if got := proposal.Chapters[16].SourceChapters; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("final parent source_chapters=%+v, want expanded 3,4", got)
	}
}

func TestBuildAdaptationProposalDropsBudgetInvalidRuntimeSourceRange(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	reports := seedPreparedAdaptationSource(t, st, []int{30000, 10})
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		t.Fatalf("LoadSourceManifest: %v", err)
	}
	sourceFoundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		t.Fatalf("LoadSourceFoundation: %v", err)
	}
	opts := ProposalOptions{
		Brief:         "arc budget split",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	skeleton := plannerSkeleton{
		Granularity:        opts.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      opts.RewritePolicy,
		Brief:              opts.Brief,
		TargetChapterCount: 8,
		Batches: []plannerSkeletonBatch{
			testSourceMapSkeletonBatch(1, 1, 1, 1, 4),
			testSourceMapSkeletonBatch(2, 2, 2, 5, 8),
		},
	}
	runtime := newPlannerProposalRuntime(opts, manifest, skeleton.TargetChapterCount)
	runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	upsertPlannerProposalRuntimeBatch(runtime, skeleton.Batches[0], repeatedSourceRangePlans(1, 4, 1, 30000))
	upsertPlannerProposalRuntimeBatch(runtime, skeleton.Batches[1], plannerBatchPlans(5, 8, 2, 2))

	_, err = buildPlanFromPlannerSkeletonDetails(context.Background(), Deps{Store: st}, opts, reports, manifest, sourceFoundation, skeleton, runtime)
	if err == nil {
		t.Fatal("buildPlanFromPlannerSkeletonDetails should fail on under-split source budget")
	}
	if !strings.Contains(err.Error(), "planner batch 1 llm generate") {
		t.Fatalf("error=%v, want regeneration after pruning invalid runtime batch", err)
	}
	savedRuntime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		t.Fatalf("LoadProposalRuntime: %v", err)
	}
	if savedRuntime == nil || savedRuntime.Skeleton == nil {
		t.Fatalf("runtime should be retained: %+v", savedRuntime)
	}
	if len(savedRuntime.CompletedBatches) != 1 {
		t.Fatalf("completed batches=%d, want only unrelated batch retained: %+v", len(savedRuntime.CompletedBatches), savedRuntime.CompletedBatches)
	}
	if savedRuntime.CompletedBatches[0].SourceFrom != 2 || savedRuntime.CompletedBatches[0].TargetFrom != 5 {
		t.Fatalf("retained batch = %+v, want source 2 target 5-8", savedRuntime.CompletedBatches[0])
	}
}

func TestValidatePlannerBatchChapterBudgetGroupsClosesParentBatch(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	sourceRunesByChapter := map[int]int{
		19: 15000,
		20: 15000,
	}
	firstDetail := plannerSkeletonBatch{
		TargetFrom:       66,
		TargetTo:         67,
		DetailParentFrom: 66,
		DetailParentTo:   72,
		SourceFrom:       19,
		SourceTo:         20,
	}
	previous := plannerBudgetRangePlans(66, 67, 19, 20)
	if err := validatePlannerBatchChapterBudgetGroups(previous, nil, opts, sourceRunesByChapter, firstDetail); err != nil {
		t.Fatalf("non-closing detail batch should defer parent source budget split check: %v", err)
	}

	finalDetail := plannerSkeletonBatch{
		TargetFrom:       70,
		TargetTo:         72,
		DetailParentFrom: 66,
		DetailParentTo:   72,
		SourceFrom:       19,
		SourceTo:         20,
	}
	current := plannerBudgetRangePlans(70, 72, 19, 20)
	err := validatePlannerBatchChapterBudgetGroups(current, previous, opts, sourceRunesByChapter, finalDetail)
	if err == nil {
		t.Fatal("closing detail batch should reject under-split parent source range")
	}
	if !strings.Contains(err.Error(), "source_range 19-20 has 30000 source_runes") ||
		!strings.Contains(err.Error(), "at least 6 target chapters") {
		t.Fatalf("error=%v, want parent source budget split guidance", err)
	}

	enoughPrevious := plannerBudgetRangePlans(66, 69, 19, 20)
	if err := validatePlannerBatchChapterBudgetGroups(current, enoughPrevious, opts, sourceRunesByChapter, finalDetail); err != nil {
		t.Fatalf("closing detail batch should accept enough parent target chapters: %v", err)
	}
}

func TestPlannerBatchChapterValidatorAllowsSharedBatchSourceRange(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 2,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 3000},
			{Chapter: 2, Runes: 2724},
		},
	}
	batch := plannerSkeletonBatch{
		Index:            1,
		TargetFrom:       1,
		TargetTo:         4,
		DetailParentFrom: 1,
		DetailParentTo:   4,
		SourceFrom:       1,
		SourceTo:         2,
	}
	chapters := plannerSharedSourceRangePlans(1, 4, 1, 2, 5724)

	if err := plannerBatchChapterValidator(opts, manifest, batch)(chapters); err != nil {
		t.Fatalf("shared broad source_range should validate as one covered source arc: %v", err)
	}
}

func TestPlannerBatchChapterValidatorRejectsDuplicateOutlinePromise(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 2,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 1000},
			{Chapter: 2, Runes: 1000},
		},
	}
	batch := plannerSkeletonBatch{
		Index:            1,
		TargetFrom:       1,
		TargetTo:         2,
		DetailParentFrom: 1,
		DetailParentTo:   2,
		SourceFrom:       1,
		SourceTo:         2,
	}
	chapters := plannerSharedSourceRangePlans(1, 2, 1, 2, 2000)
	chapters[1].Title = chapters[0].Title
	chapters[1].OutlineEntry.Title = chapters[0].OutlineEntry.Title
	chapters[1].CoreEvent = chapters[0].CoreEvent
	chapters[1].Hook = chapters[0].Hook

	err := plannerBatchChapterValidator(opts, manifest, batch)(chapters)
	if err == nil {
		t.Fatal("duplicate title/core_event/hook should be rejected")
	}
	if !strings.Contains(err.Error(), "chapter 2 duplicates outline beats from chapter 1") {
		t.Fatalf("error=%v, want duplicate outline guidance", err)
	}
}

func TestPlannerBatchChapterValidatorRejectsDuplicateFromSameParentBatch(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 2,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 1000},
			{Chapter: 2, Runes: 1000},
		},
	}
	previous := plannerSharedSourceRangePlans(1, 4, 1, 2, 4000)
	current := plannerSharedSourceRangePlans(5, 8, 1, 2, 4000)
	current[0].Title = previous[1].Title
	current[0].OutlineEntry.Title = previous[1].OutlineEntry.Title
	current[0].CoreEvent = previous[1].CoreEvent
	current[0].OutlineEntry.CoreEvent = previous[1].OutlineEntry.CoreEvent
	current[0].Hook = previous[1].Hook
	current[0].OutlineEntry.Hook = previous[1].OutlineEntry.Hook
	batch := plannerSkeletonBatch{
		Index:            2,
		TargetFrom:       5,
		TargetTo:         8,
		DetailParentFrom: 1,
		DetailParentTo:   8,
		SourceFrom:       1,
		SourceTo:         2,
	}

	err := plannerBatchChapterValidator(opts, manifest, batch, previous)(current)
	if err == nil {
		t.Fatal("duplicate from same parent batch should be rejected")
	}
	if !strings.Contains(err.Error(), "chapter 5 duplicates outline beats from chapter 2") {
		t.Fatalf("error=%v, want same-parent duplicate guidance", err)
	}
}

func TestPlannerBatchChapterValidatorIgnoresDuplicateOutsideParentBatch(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 2,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 1000},
			{Chapter: 2, Runes: 1000},
		},
	}
	previous := plannerSharedSourceRangePlans(1, 4, 1, 2, 4000)
	current := plannerSharedSourceRangePlans(5, 8, 1, 2, 4000)
	current[0].Title = previous[1].Title
	current[0].OutlineEntry.Title = previous[1].OutlineEntry.Title
	current[0].CoreEvent = previous[1].CoreEvent
	current[0].OutlineEntry.CoreEvent = previous[1].OutlineEntry.CoreEvent
	current[0].Hook = previous[1].Hook
	current[0].OutlineEntry.Hook = previous[1].OutlineEntry.Hook
	batch := plannerSkeletonBatch{
		Index:            2,
		TargetFrom:       5,
		TargetTo:         8,
		DetailParentFrom: 5,
		DetailParentTo:   8,
		SourceFrom:       1,
		SourceTo:         2,
	}

	if err := plannerBatchChapterValidator(opts, manifest, batch, previous)(current); err != nil {
		t.Fatalf("duplicate outside parent batch should not be rejected: %v", err)
	}
}

func TestPlannerBatchChapterValidatorRequiresEnoughTargetsForSharedSourceRange(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 10,
		Chapters: []domain.AdaptationSource{
			{Chapter: 6, Runes: 1800},
			{Chapter: 7, Runes: 1800},
			{Chapter: 8, Runes: 2400},
		},
	}
	oneTargetBatch := plannerSkeletonBatch{
		Index:            2,
		TargetFrom:       3,
		TargetTo:         3,
		DetailParentFrom: 3,
		DetailParentTo:   3,
		SourceFrom:       6,
		SourceTo:         8,
	}
	err := plannerBatchChapterValidator(opts, manifest, oneTargetBatch)(plannerSharedSourceRangePlans(3, 3, 6, 8, 6000))
	if err == nil {
		t.Fatal("one target chapter should not carry source_range 6-8 with 6000 source runes")
	}
	if !strings.Contains(err.Error(), "source_range 6-8 has 6000 source_runes") ||
		!strings.Contains(err.Error(), "at least 2 target chapters") {
		t.Fatalf("error=%v, want source_range split guidance", err)
	}

	twoTargetBatch := oneTargetBatch
	twoTargetBatch.TargetTo = 4
	twoTargetBatch.DetailParentTo = 4
	if err := plannerBatchChapterValidator(opts, manifest, twoTargetBatch)(plannerSharedSourceRangePlans(3, 4, 6, 8, 6000)); err != nil {
		t.Fatalf("two target chapters sharing source_range 6-8 should pass after budget split: %v", err)
	}
}

func TestPlannerBatchChapterValidatorUsesParentRangeForSharedDetailSubBatches(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 25}
	for chapter := 14; chapter <= 25; chapter++ {
		manifest.Chapters = append(manifest.Chapters, domain.AdaptationSource{Chapter: chapter, Runes: 2647})
	}
	firstBatch := plannerSkeletonBatch{
		Index:            12,
		TargetFrom:       45,
		TargetTo:         48,
		DetailParentFrom: 45,
		DetailParentTo:   52,
		SourceFrom:       14,
		SourceTo:         25,
	}
	firstChapters := plannerSharedSourceRangePlans(45, 48, 14, 15, 30329)
	if err := plannerBatchChapterValidator(opts, manifest, firstBatch)(firstChapters); err != nil {
		t.Fatalf("non-closing detail batch should accept narrow model source_range and canonicalize to parent range: %v", err)
	}
	if firstChapters[0].SourceRange.From != 14 || firstChapters[0].SourceRange.To != 25 {
		t.Fatalf("first source_range=%+v, want parent range 14-25", firstChapters[0].SourceRange)
	}

	finalBatch := firstBatch
	finalBatch.Index = 13
	finalBatch.TargetFrom = 49
	finalBatch.TargetTo = 52
	finalChapters := plannerSharedSourceRangePlans(49, 52, 14, 15, 30329)
	if err := plannerBatchChapterValidator(opts, manifest, finalBatch, firstChapters)(finalChapters); err != nil {
		t.Fatalf("closing detail batch should validate parent source capacity instead of model's narrow source_range: %v", err)
	}
	if finalChapters[0].SourceRange.From != 14 || finalChapters[0].SourceRange.To != 25 {
		t.Fatalf("final source_range=%+v, want parent range 14-25", finalChapters[0].SourceRange)
	}
}

func TestPlannerBatchChapterValidatorStillRejectsInsufficientParentRangeCapacity(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 25}
	for chapter := 14; chapter <= 25; chapter++ {
		manifest.Chapters = append(manifest.Chapters, domain.AdaptationSource{Chapter: chapter, Runes: 2647})
	}
	batch := plannerSkeletonBatch{
		Index:            12,
		TargetFrom:       45,
		TargetTo:         48,
		DetailParentFrom: 45,
		DetailParentTo:   48,
		SourceFrom:       14,
		SourceTo:         25,
	}
	err := plannerBatchChapterValidator(opts, manifest, batch)(plannerSharedSourceRangePlans(45, 48, 14, 15, 30329))
	if err == nil {
		t.Fatal("parent range with too few target chapters should still fail")
	}
	if !strings.Contains(err.Error(), "source_range 14-25 has 31764 source_runes") ||
		!strings.Contains(err.Error(), "at least 6 target chapters") {
		t.Fatalf("error=%v, want parent source capacity guidance", err)
	}
}

func TestPlannerBatchChapterValidatorAllowsAcceptedBudgetDeviation(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 1,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 16000},
		},
	}
	batch := plannerSkeletonBatch{
		Index:            1,
		TargetFrom:       1,
		TargetTo:         1,
		DetailParentFrom: 1,
		DetailParentTo:   1,
		SourceFrom:       1,
		SourceTo:         1,
		Notes:            []string{plannerBudgetDeviationAcceptedNote},
	}

	if err := plannerBatchChapterValidator(opts, manifest, batch)(plannerSharedSourceRangePlans(1, 1, 1, 1, 16000)); err != nil {
		t.Fatalf("accepted compressed source range should skip parent budget split validation: %v", err)
	}
}

func TestValidatePlannerProposalAllowsAcceptedBudgetDeviationRange(t *testing.T) {
	opts := ProposalOptions{
		Brief:         "compress side material",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 1,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 16000},
		},
	}
	proposal := domain.AdaptationPlan{
		Granularity:   opts.Granularity,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: opts.RewritePolicy,
		Brief:         opts.Brief,
		Chapters:      plannerSharedSourceRangePlans(1, 1, 1, 1, 16000),
		Volumes: []domain.AdaptationVolumePlan{
			{
				Index:      1,
				Title:      "Compressed volume",
				TargetFrom: 1,
				TargetTo:   1,
				SourceFrom: 1,
				SourceTo:   1,
				Notes:      domain.TextList{plannerBudgetDeviationAcceptedNote},
			},
		},
	}

	if err := validatePlannerProposal(&proposal, opts, nil, manifest, nil); err != nil {
		t.Fatalf("accepted compressed volume should validate without repeated budget split failure: %v", err)
	}
	if proposal.Chapters[0].WordBudget == nil || proposal.Chapters[0].WordBudget.MaxRunes > domain.AdaptationModelChapterMaxRunes {
		t.Fatalf("accepted budget deviation should still normalize per-chapter word budget: %+v", proposal.Chapters[0].WordBudget)
	}
}

func TestValidatePlannerProposalRejectsDuplicateOutlinePromise(t *testing.T) {
	opts := ProposalOptions{
		Brief:         "reject duplicated detail plans",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 2,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 1000},
			{Chapter: 2, Runes: 1000},
		},
	}
	chapters := plannerSharedSourceRangePlans(1, 2, 1, 2, 1000)
	chapters[1].Title = chapters[0].Title
	chapters[1].OutlineEntry.Title = chapters[0].OutlineEntry.Title
	chapters[1].CoreEvent = chapters[0].CoreEvent
	chapters[1].Hook = chapters[0].Hook
	proposal := domain.AdaptationPlan{
		Granularity:   opts.Granularity,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: opts.RewritePolicy,
		Brief:         opts.Brief,
		Chapters:      chapters,
	}

	err := validatePlannerProposal(&proposal, opts, nil, manifest, nil)
	if err == nil {
		t.Fatal("complete planner proposal should reject duplicate outline promises")
	}
	if !strings.Contains(err.Error(), "chapter 2 duplicates outline beats from chapter 1") {
		t.Fatalf("error=%v, want duplicate outline rejection", err)
	}
}

func TestValidatePlannerProposalFreeAllowsCompressedSourceRangeButCapsTargetChapterBudget(t *testing.T) {
	opts := ProposalOptions{
		Brief:         "free compress long source into a new opening",
		Granularity:   domain.AdaptationGranularityFree,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 1,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 20067},
		},
	}
	proposal := domain.AdaptationPlan{
		Granularity:   opts.Granularity,
		Status:        domain.AdaptationPlanStatusProposal,
		RewritePolicy: opts.RewritePolicy,
		Brief:         opts.Brief,
		Chapters: []domain.AdaptationChapterPlan{
			{
				Chapter: 1,
				Title:   "New Opening",
				OutlineEntry: domain.OutlineEntry{
					Chapter:   1,
					Title:     "New Opening",
					CoreEvent: "Ari enters the adapted conflict through a compressed new premise.",
					Hook:      "Ari realizes the old route no longer exists.",
					Scenes:    []string{"station"},
				},
				SourceChapters: []int{1},
				SourceRange:    domain.SourceRange{From: 1, To: 1},
				WordBudget: &domain.AdaptationChapterWordBudget{
					SourceRunes: 20067,
					TargetRunes: 12000,
					MinRunes:    9000,
					MaxRunes:    15000,
					Tolerance:   0.15,
				},
				PreserveEvents:  []string{"source event"},
				RequiredChanges: []string{"compress the source material into a new structure"},
				ForbiddenMoves:  []string{"copy source prose"},
			},
		},
	}

	if err := validatePlannerProposal(&proposal, opts, nil, manifest, nil); err != nil {
		t.Fatalf("free proposal should allow compressed long source range: %v", err)
	}
	budget := proposal.Chapters[0].WordBudget
	if budget == nil {
		t.Fatal("free proposal should retain normalized word budget")
	}
	if budget.TargetRunes != adaptationPlannerModelChapterMaxRunes || budget.MaxRunes != adaptationPlannerModelChapterMaxRunes {
		t.Fatalf("free target chapter budget=%+v, want capped at %d", budget, adaptationPlannerModelChapterMaxRunes)
	}
	if proposal.TargetTotalRunes != budget.TargetRunes || proposal.TargetMaxRunes != budget.MaxRunes {
		t.Fatalf("proposal totals should match normalized free budget: total=%d max=%d budget=%+v", proposal.TargetTotalRunes, proposal.TargetMaxRunes, budget)
	}
}

func TestPlannerBatchChapterValidatorRejectsAnchorOutsideBatchSourceRange(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 10}
	batch := plannerSkeletonBatch{
		Index:            1,
		TargetFrom:       1,
		TargetTo:         1,
		DetailParentFrom: 1,
		DetailParentTo:   1,
		SourceFrom:       1,
		SourceTo:         2,
	}
	chapters := plannerSharedSourceRangePlans(1, 1, 1, 2, 1000)
	chapters[0].SourceChapters = []int{9}
	chapters[0].SourceRange = domain.SourceRange{From: 1, To: 9}

	err := plannerBatchChapterValidator(opts, manifest, batch)(chapters)
	if err == nil {
		t.Fatal("source anchor outside the parent batch source range should fail")
	}
	if !strings.Contains(err.Error(), "outside batch source range 1-2") {
		t.Fatalf("error=%v, want batch source range rejection", err)
	}
}

func TestPlannerBatchChapterValidatorCanonicalizesSharedRangeToParentBatch(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 4,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 1000},
			{Chapter: 2, Runes: 1000},
			{Chapter: 3, Runes: 1000},
			{Chapter: 4, Runes: 1000},
		},
	}
	batch := plannerSkeletonBatch{
		Index:            1,
		TargetFrom:       1,
		TargetTo:         1,
		DetailParentFrom: 1,
		DetailParentTo:   1,
		SourceFrom:       1,
		SourceTo:         4,
	}
	chapters := plannerSharedSourceRangePlans(1, 1, 1, 2, 1000)
	chapters[0].SourceChapters = []int{3}

	if err := plannerBatchChapterValidator(opts, manifest, batch)(chapters); err != nil {
		t.Fatalf("in-batch anchor should validate inside the parent coverage envelope: %v", err)
	}
	if chapters[0].SourceRange.From != 1 || chapters[0].SourceRange.To != 4 {
		t.Fatalf("source_range=%+v, want canonical parent range 1-4", chapters[0].SourceRange)
	}
}

func TestPlannerBatchChapterValidatorFreeKeepsSharedRangeWithoutArcBudgetSplit(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityFree,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 12,
		Chapters: []domain.AdaptationSource{
			{Chapter: 8, Runes: 12000},
			{Chapter: 9, Runes: 12000},
			{Chapter: 10, Runes: 12000},
			{Chapter: 11, Runes: 12000},
			{Chapter: 12, Runes: 13104},
		},
	}
	batch := plannerSkeletonBatch{
		Index:            1,
		TargetFrom:       1,
		TargetTo:         1,
		DetailParentFrom: 1,
		DetailParentTo:   1,
		SourceFrom:       8,
		SourceTo:         12,
	}
	chapter := plannerSharedSourceRangePlans(1, 1, 8, 12, 61104)
	chapter[0].WordBudget.TargetRunes = 12000
	chapter[0].WordBudget.MinRunes = 9000
	chapter[0].WordBudget.MaxRunes = 15000
	chapter[0].TargetRunes = 0
	chapter[0].TargetMinRunes = 0
	chapter[0].TargetMaxRunes = 0

	if err := plannerBatchChapterValidator(opts, manifest, batch)(chapter); err != nil {
		t.Fatalf("free detail batch should not require arc-style source-range budget split: %v", err)
	}
	if chapter[0].SourceRange.From != 8 || chapter[0].SourceRange.To != 12 {
		t.Fatalf("free detail source_range=%+v, want parent range 8-12", chapter[0].SourceRange)
	}
	if chapter[0].WordBudget.MaxRunes != 15000 {
		t.Fatalf("detail validator should defer final free target budget cap to proposal normalization, got %+v", chapter[0].WordBudget)
	}
}

func TestRemovePlannerProposalRuntimeBatchesForBudgetSplitErrorsDropsAllRanges(t *testing.T) {
	runtime := &domain.AdaptationProposalRuntime{
		CompletedBatches: []domain.AdaptationProposalRuntimeBatch{
			{Index: 1, TargetFrom: 13, TargetTo: 16, SourceFrom: 6, SourceTo: 7},
			{Index: 2, TargetFrom: 17, TargetTo: 20, SourceFrom: 6, SourceTo: 7},
			{Index: 3, TargetFrom: 73, TargetTo: 76, SourceFrom: 21, SourceTo: 23},
			{Index: 4, TargetFrom: 109, TargetTo: 112, SourceFrom: 31, SourceTo: 31},
		},
	}
	removed := removePlannerProposalRuntimeBatchesForBudgetSplitErrors(runtime, plannerProposalBudgetSplitErrors{
		{SourceFrom: 6, SourceTo: 7},
		{SourceFrom: 21, SourceTo: 23},
	})

	if removed != 3 {
		t.Fatalf("removed=%d, want 3", removed)
	}
	if len(runtime.CompletedBatches) != 1 || runtime.CompletedBatches[0].SourceFrom != 31 {
		t.Fatalf("remaining batches=%+v, want only unrelated source range", runtime.CompletedBatches)
	}
}

func TestPreparePlannerRuntimeAfterValidationErrorScansCompletedParents(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedDirectConfirmedAdaptationTargetFoundation(t, st, 1, "scan invalid completed parents")
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 31,
		Chapters: []domain.AdaptationSource{
			{Chapter: 21, Runes: 13893},
			{Chapter: 22, Runes: 41328},
			{Chapter: 23, Runes: 11923},
			{Chapter: 31, Runes: 7381},
		},
	}
	skeleton := plannerSkeleton{
		Granularity:        opts.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      opts.RewritePolicy,
		Brief:              "scan all invalid completed parents",
		TargetChapterCount: 112,
		Batches: []plannerSkeletonBatch{
			testSourceMapSkeletonBatch(9, 21, 23, 73, 86),
			testSourceMapSkeletonBatch(15, 31, 31, 109, 112),
		},
	}
	runtime := newPlannerProposalRuntime(opts, manifest, skeleton.TargetChapterCount)
	runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	runtime.CompletedBatches = []domain.AdaptationProposalRuntimeBatch{
		{Index: 22, TargetFrom: 73, TargetTo: 76, SourceFrom: 21, SourceTo: 23, Chapters: plannerBudgetRangePlans(73, 76, 21, 23)},
		{Index: 23, TargetFrom: 77, TargetTo: 80, SourceFrom: 21, SourceTo: 23, Chapters: plannerBudgetRangePlans(77, 80, 22, 23)},
		{Index: 24, TargetFrom: 81, TargetTo: 84, SourceFrom: 21, SourceTo: 23, Chapters: plannerBudgetRangePlans(81, 84, 22, 23)},
		{Index: 25, TargetFrom: 85, TargetTo: 86, SourceFrom: 21, SourceTo: 23, Chapters: plannerBudgetRangePlans(85, 86, 23, 23)},
		{Index: 33, TargetFrom: 109, TargetTo: 112, SourceFrom: 31, SourceTo: 31, Chapters: plannerBudgetRangePlans(109, 112, 31, 31)},
	}

	err := preparePlannerRuntimeAfterValidationError(
		Deps{Store: st},
		runtime,
		&plannerProposalBudgetSplitError{FirstChapter: 76, SourceFrom: 22, SourceTo: 23, SourceRunes: 53251, MinChapters: 11},
		opts,
		manifest,
		nil,
	)
	if err != nil {
		t.Fatalf("preparePlannerRuntimeAfterValidationError: %v", err)
	}
	if len(runtime.CompletedBatches) != 1 || runtime.CompletedBatches[0].SourceFrom != 31 {
		t.Fatalf("remaining batches=%+v, want only unrelated completed parent", runtime.CompletedBatches)
	}
	savedRuntime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		t.Fatalf("LoadProposalRuntime: %v", err)
	}
	if savedRuntime == nil || len(savedRuntime.CompletedBatches) != 1 || savedRuntime.CompletedBatches[0].SourceFrom != 31 {
		t.Fatalf("saved runtime=%+v, want only unrelated completed parent", savedRuntime)
	}
}

func TestPreparePlannerRuntimeAfterValidationErrorDropsDuplicateMainlineBatches(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedDirectConfirmedAdaptationTargetFoundation(t, st, 1, "drop duplicate mainline batches")
	first := plannerBatchPlans(1, 2, 1, 1)
	first[0].EventIDs = []string{"event-duplicate"}
	second := plannerBatchPlans(3, 4, 2, 2)
	second[1].EventIDs = []string{"event-duplicate"}
	runtime := &domain.AdaptationProposalRuntime{
		CompletedBatches: []domain.AdaptationProposalRuntimeBatch{
			{Index: 1, TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 1, Chapters: first},
			{Index: 2, TargetFrom: 3, TargetTo: 4, SourceFrom: 2, SourceTo: 2, Chapters: second},
			{Index: 3, TargetFrom: 5, TargetTo: 6, SourceFrom: 3, SourceTo: 3, Chapters: plannerBatchPlans(5, 6, 3, 3)},
		},
	}

	err := preparePlannerRuntimeAfterValidationError(
		Deps{Store: st},
		runtime,
		&arcMainlineBindingError{EventID: "event-duplicate", Count: 2, Chapters: []int{1, 4}},
		ProposalOptions{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("preparePlannerRuntimeAfterValidationError: %v", err)
	}
	if len(runtime.CompletedBatches) != 1 || runtime.CompletedBatches[0].Index != 3 {
		t.Fatalf("remaining batches=%+v, want only unrelated batch 3", runtime.CompletedBatches)
	}
	savedRuntime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		t.Fatalf("LoadProposalRuntime: %v", err)
	}
	if savedRuntime == nil || len(savedRuntime.CompletedBatches) != 1 || savedRuntime.CompletedBatches[0].Index != 3 {
		t.Fatalf("saved runtime=%+v, want only unrelated batch 3", savedRuntime)
	}
}

func TestPlannerSkeletonForDetailPromptScopesMainlineEventsToDetailBatch(t *testing.T) {
	parent := testSourceMapSkeletonBatch(1, 1, 4, 1, 8)
	parent.MainlineEventIDs = []string{"event-1", "event-2", "event-3"}
	detail := parent
	detail.TargetFrom = 5
	detail.TargetTo = 8
	detail.MainlineEventIDs = []string{"event-3"}

	context := plannerSkeletonForDetailPrompt(plannerSkeleton{Batches: []plannerSkeletonBatch{parent}}, detail)
	if len(context.Batches) != 1 {
		t.Fatalf("context batches=%+v, want one parent batch", context.Batches)
	}
	got := context.Batches[0]
	if got.TargetFrom != parent.TargetFrom || got.TargetTo != parent.TargetTo {
		t.Fatalf("parent target context changed to %d-%d", got.TargetFrom, got.TargetTo)
	}
	if !slices.Equal(got.MainlineEventIDs, []string{"event-3"}) {
		t.Fatalf("mainline_event_ids=%v, want only current detail assignment", got.MainlineEventIDs)
	}
}

func TestBuildPlanRejectsForeignMainlineBeforeFinalValidation(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedDirectConfirmedAdaptationTargetFoundation(t, st, 1, "reject foreign mainline")
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 2,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Runes: 1000},
			{Chapter: 2, Runes: 1000},
		},
	}
	reports := []domain.AdaptationSourceReport{
		{Chapter: 1, SourceEvents: []domain.AdaptationEvent{{ID: "event-1", Importance: domain.AdaptationEventMainline, SourceChapter: 1, Required: true}}},
		{Chapter: 2, SourceEvents: []domain.AdaptationEvent{{ID: "event-2", Importance: domain.AdaptationEventMainline, SourceChapter: 2, Required: true}}},
	}
	opts := ProposalOptions{
		Brief:         "repair duplicate mainline bindings",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	firstBatch := testSourceMapSkeletonBatch(1, 1, 1, 1, 1)
	firstBatch.MainlineEventIDs = []string{"event-1"}
	secondBatch := testSourceMapSkeletonBatch(2, 2, 2, 2, 2)
	secondBatch.MainlineEventIDs = []string{"event-2"}
	skeleton := plannerSkeleton{
		Granularity:        opts.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      opts.RewritePolicy,
		Brief:              opts.Brief,
		TargetChapterCount: 2,
		Batches:            []plannerSkeletonBatch{firstBatch, secondBatch},
	}
	first := plannerBatchPlans(1, 1, 1, 1)
	first[0].EventIDs = []string{"event-1"}
	second := plannerBatchPlans(2, 2, 2, 2)
	second[0].EventIDs = []string{"event-2", "event-1"}
	runtime := newPlannerProposalRuntime(opts, manifest, 2)
	runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	upsertPlannerProposalRuntimeBatch(runtime, firstBatch, first)
	upsertPlannerProposalRuntimeBatch(runtime, secondBatch, second)
	var progress []string
	opts.EmitProgress = func(_ Stage, _ int, _ int, message string, _ error) {
		progress = append(progress, message)
	}

	_, err := buildPlanFromPlannerSkeletonDetailsWithFinalRepairs(
		context.Background(), Deps{Store: st, Auditor: &scriptedAdaptLLM{}}, opts, reports, manifest, &domain.AdaptationSourceFoundation{}, skeleton, runtime, 1,
	)
	if err == nil || !strings.Contains(err.Error(), "planner batch 2 llm generate") {
		t.Fatalf("error=%v, want local validation to regenerate only polluted batch 2", err)
	}
	joinedProgress := strings.Join(progress, "\n")
	if !strings.Contains(joinedProgress, "已保留无效详情批次 2/2 的错误指纹") {
		t.Fatalf("progress=%v, want polluted cached batch checkpointed before clean regeneration", progress)
	}
	if strings.Contains(joinedProgress, "Final proposal validation discarded") {
		t.Fatalf("progress=%v, foreign mainline ID should not reach final validation", progress)
	}
	if len(runtime.CompletedBatches) != 2 || runtime.CompletedBatches[1].Audit == nil || runtime.CompletedBatches[1].Audit.LastErrorCategory != "foreign_event_id" {
		t.Fatalf("completed batches=%+v, want correct batch plus persisted polluted candidate", runtime.CompletedBatches)
	}
}

func TestPlannerSourceReportExcerptsForDetailScopesEventIDsToCurrentArcBatch(t *testing.T) {
	reports := []domain.AdaptationSourceReport{{
		Chapter: 1,
		SourceEvents: []domain.AdaptationEvent{
			{ID: "event-current", Importance: domain.AdaptationEventMainline, Required: true},
			{ID: "event-foreign", Importance: domain.AdaptationEventMainline, Required: true},
			{ID: "event-supporting", Importance: domain.AdaptationEventSupporting},
		},
	}}
	batch := plannerSkeletonBatch{MainlineEventIDs: []string{"event-current"}, AllowedEventIDs: []string{"event-current", "event-supporting"}}

	excerpts := plannerSourceReportExcerptsForDetail(reports, domain.AdaptationGranularityArc, batch)
	if len(excerpts) != 1 {
		t.Fatalf("excerpts=%+v, want one source report", excerpts)
	}
	gotIDs := adaptationEventIDs(excerpts[0].SourceEvents)
	if !slices.Equal(gotIDs, []string{"event-current", "event-supporting"}) {
		t.Fatalf("source event IDs=%v, want current mainline and stable supporting IDs", gotIDs)
	}
}

func TestPlannerRuntimeCompletedBudgetSplitErrorsSkipsIncompleteParents(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 23,
		Chapters: []domain.AdaptationSource{
			{Chapter: 21, Runes: 13893},
			{Chapter: 22, Runes: 41328},
			{Chapter: 23, Runes: 11923},
		},
	}
	skeleton := plannerSkeleton{
		Granularity:        opts.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      opts.RewritePolicy,
		Brief:              "do not scan incomplete parent",
		TargetChapterCount: 86,
		Batches: []plannerSkeletonBatch{
			testSourceMapSkeletonBatch(9, 21, 23, 73, 86),
		},
	}
	runtime := newPlannerProposalRuntime(opts, manifest, skeleton.TargetChapterCount)
	runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	runtime.CompletedBatches = []domain.AdaptationProposalRuntimeBatch{
		{Index: 22, TargetFrom: 73, TargetTo: 76, SourceFrom: 21, SourceTo: 23, Chapters: plannerBudgetRangePlans(73, 76, 21, 23)},
	}

	if errs := plannerRuntimeCompletedBudgetSplitErrors(runtime, opts, manifest); len(errs) != 0 {
		t.Fatalf("budget split errors=%v, want none for incomplete parent", errs)
	}
}

func TestPlannerRuntimeCompletedBudgetSplitErrorsUsesParentRangeForSharedFullRewrite(t *testing.T) {
	opts := ProposalOptions{
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
	}
	manifest := &domain.AdaptationSourceManifest{
		ChapterCount: 25,
		Chapters: []domain.AdaptationSource{
			{Chapter: 14, Runes: 15164},
			{Chapter: 15, Runes: 15165},
		},
	}
	for chapter := 16; chapter <= 25; chapter++ {
		manifest.Chapters = append(manifest.Chapters, domain.AdaptationSource{Chapter: chapter, Runes: 500})
	}
	skeleton := plannerSkeleton{
		Granularity:        opts.Granularity,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      opts.RewritePolicy,
		Brief:              "resume old narrow detail ranges",
		TargetChapterCount: 52,
		Batches: []plannerSkeletonBatch{
			testSourceMapSkeletonBatch(12, 14, 25, 45, 52),
		},
	}
	runtime := newPlannerProposalRuntime(opts, manifest, skeleton.TargetChapterCount)
	runtime.Skeleton = plannerRuntimeOutlineFromSkeleton(skeleton)
	runtime.CompletedBatches = []domain.AdaptationProposalRuntimeBatch{
		{Index: 12, TargetFrom: 45, TargetTo: 48, SourceFrom: 14, SourceTo: 25, Chapters: plannerSharedSourceRangePlans(45, 48, 14, 15, 30329)},
		{Index: 13, TargetFrom: 49, TargetTo: 52, SourceFrom: 14, SourceTo: 25, Chapters: plannerSharedSourceRangePlans(49, 52, 16, 25, 5000)},
	}

	if errs := plannerRuntimeCompletedBudgetSplitErrors(runtime, opts, manifest); len(errs) != 0 {
		t.Fatalf("budget split errors=%v, want none when parent range has enough target chapters", errs)
	}
}

func TestBuildAdaptationProposalResumesChunkedPlannerRuntimeAfterBatchFailure(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"mainline_rules": ["keep every source turn anchored"],
			"relationship_goals": ["slow emotional escalation"],
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 2)},
		{err: context.Canceled},
	}}

	_, err := BuildAdaptationProposal(Deps{Store: st, LLM: first}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err == nil {
		t.Fatal("first interrupted proposal build should fail")
	}
	if first.calls != 3 {
		t.Fatalf("first planner calls=%d, want skeleton + first detail batch + failed second detail batch", first.calls)
	}
	runtime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		t.Fatalf("LoadProposalRuntime: %v", err)
	}
	if runtime == nil || runtime.Skeleton == nil || len(runtime.CompletedBatches) != 1 {
		t.Fatalf("runtime should keep skeleton and first completed batch: %+v", runtime)
	}
	if runtime.CompletedBatches[0].TargetFrom != 1 || runtime.CompletedBatches[0].TargetTo != 4 {
		t.Fatalf("completed runtime batch = %+v", runtime.CompletedBatches[0])
	}
	if saved, err := st.Adaptation.LoadProposal(); err != nil || saved != nil {
		t.Fatalf("proposal should not be saved after interrupted run: proposal=%+v err=%v", saved, err)
	}

	second := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerBatchProposalJSON(5, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 12, 2, 3)},
		{text: plannerBatchProposalJSON(13, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}
	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: second}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal resume: %v", err)
	}
	if second.calls != 4 {
		t.Fatalf("resume planner calls=%d, want only remaining four detail batches", second.calls)
	}
	if len(proposal.Chapters) != 20 || proposal.Chapters[0].Chapter != 1 || proposal.Chapters[19].Chapter != 20 {
		t.Fatalf("resumed proposal chapters = %+v", proposal.Chapters)
	}
	firstResumePrompt := second.got[0][1].TextContent()
	if !strings.Contains(firstResumePrompt, `"target_from": 5`) || strings.Contains(firstResumePrompt, "Do not return chapter details") {
		t.Fatalf("resume should skip skeleton and first batch, prompt=%s", firstResumePrompt)
	}
	if runtime, err := st.Adaptation.LoadProposalRuntime(); err != nil || runtime != nil {
		t.Fatalf("runtime should be cleared after successful proposal save: runtime=%+v err=%v", runtime, err)
	}
}

func TestBuildAdaptationProposalRepairsChunkedSkeletonWithoutBatches(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"overall_arc": "model returned a high level arc but no machine usable batches",
			"key_turns": ["call", "choice", "return"],
			"pair": {"lead": "Ari", "partner": "Bea"}
		}`},
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 2)},
		{text: plannerBatchProposalJSON(5, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 12, 2, 3)},
		{text: plannerBatchProposalJSON(13, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 7 {
		t.Fatalf("planner calls=%d, want skeleton + repair + 5 detail calls", llm.calls)
	}
	repairPrompt := llm.got[1][1].TextContent()
	if !strings.Contains(repairPrompt, "previous planner response could not be used") ||
		!strings.Contains(repairPrompt, "top-level batches array") ||
		!strings.Contains(repairPrompt, "overall_arc") {
		t.Fatalf("repair prompt should explain missing-batches schema failure: %s", repairPrompt)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 20 {
		t.Fatalf("repaired chunked proposal should be saved: %+v", saved)
	}
}

func TestParsePlannerSkeletonWrapsSingleBatchObject(t *testing.T) {
	skeleton, err := parsePlannerSkeleton(`{
		"index": 1,
		"title": "Only volume",
		"theme": "opening pressure",
		"target_from": 1,
		"target_to": 4,
		"source_from": 1,
		"source_to": 2,
		"summary": "model returned one batch object"
	}`)
	if err != nil {
		t.Fatalf("parsePlannerSkeleton: %v", err)
	}
	if len(skeleton.Batches) != 1 {
		t.Fatalf("single batch object should be wrapped into batches: %+v", skeleton)
	}
	batch := skeleton.Batches[0]
	if batch.TargetFrom != 1 || batch.TargetTo != 4 || batch.SourceFrom != 1 || batch.SourceTo != 2 {
		t.Fatalf("wrapped batch mismatch: %+v", batch)
	}
	if skeleton.TargetChapterCount != 4 {
		t.Fatalf("target chapter count=%d, want 4", skeleton.TargetChapterCount)
	}
}

func TestParsePlannerSourceMapSkeletonExtractsUniqueJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "fenced", text: "```json\n" + testPlannerSourceMapSkeletonJSON("") + "\n```"},
		{name: "prose", text: "Planner output:\n" + testPlannerSourceMapSkeletonJSON("") + "\nUse this skeleton."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skeleton, err := parsePlannerSourceMapSkeleton(tc.text)
			if err != nil {
				t.Fatalf("parsePlannerSourceMapSkeleton: %v", err)
			}
			if len(skeleton.Batches) != 1 || skeleton.Batches[0].SourceFrom != 1 || skeleton.Batches[0].SourceTo != 2 {
				t.Fatalf("batches not decoded: %+v", skeleton.Batches)
			}
		})
	}
}

func TestParsePlannerSourceMapSkeletonAcceptsAliasesAndNestedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "chunks alias", text: `{"target_chapter_count":4,"chunks":[` + testPlannerSourceMapSkeletonBatchJSON() + `]}`},
		{name: "nested structure alias", text: `{"structure":{"target_chapter_count":4,"parts":[` + testPlannerSourceMapSkeletonBatchJSON() + `]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skeleton, err := parsePlannerSourceMapSkeleton(tc.text)
			if err != nil {
				t.Fatalf("parsePlannerSourceMapSkeleton: %v", err)
			}
			if len(skeleton.Batches) != 1 {
				t.Fatalf("batches=%d, want 1: %+v", len(skeleton.Batches), skeleton)
			}
			if skeleton.TargetChapterCount != 4 {
				t.Fatalf("target chapter count=%d, want 4", skeleton.TargetChapterCount)
			}
		})
	}
}

func TestParsePlannerSourceMapSkeletonIgnoresTopLevelTargetCountAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "chapter count", key: "chapter_count"},
		{name: "total chapters", key: "total_chapters"},
		{name: "target count", key: "target_count"},
		{name: "camel target count", key: "targetChapterCount"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := `{"` + tc.key + `":9,"batches":[` + testPlannerSourceMapSkeletonBatchJSON() + `]}`
			skeleton, err := parsePlannerSourceMapSkeleton(text)
			if err != nil {
				t.Fatalf("parsePlannerSourceMapSkeleton: %v", err)
			}
			if skeleton.TargetChapterCount != 0 {
				t.Fatalf("target chapter count=%d, want 0 for top-level %s alias", skeleton.TargetChapterCount, tc.key)
			}
		})
	}
}

func TestParsePlannerSourceMapSkeletonRejectsAmbiguousMultipleObjects(t *testing.T) {
	_, err := parsePlannerSourceMapSkeleton(testPlannerSourceMapSkeletonJSON("") + "\n" + testPlannerSourceMapSkeletonJSON(""))
	if err == nil {
		t.Fatal("parsePlannerSourceMapSkeleton should reject multiple complete JSON objects")
	}
	if !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "multiple complete JSON objects") {
		t.Fatalf("error=%v, want ambiguous multiple-object rejection", err)
	}
	if !errors.Is(err, errPlannerSourceMapMultipleJSON) {
		t.Fatalf("error=%v, want typed multiple-object error", err)
	}
}

func TestParsePlannerSourceMapSkeletonRejectsContainmentLikeAmbiguousMultipleObjects(t *testing.T) {
	first := testPlannerSourceMapSkeletonJSON("")
	second := `{"shadow":` + first + `,"batches":[` + testPlannerSourceMapSkeletonBatchJSON() + `]}`
	_, err := parsePlannerSourceMapSkeleton(first + "\n" + second)
	if err == nil {
		t.Fatal("parsePlannerSourceMapSkeleton should reject separate top-level JSON objects")
	}
	if !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "multiple complete JSON objects") {
		t.Fatalf("error=%v, want ambiguous multiple-object rejection", err)
	}
}

func TestParsePlannerSourceMapSkeletonRejectsNoJSONObject(t *testing.T) {
	_, err := parsePlannerSourceMapSkeleton("planner returned no structured data")
	if err == nil {
		t.Fatal("parsePlannerSourceMapSkeleton should reject responses with no JSON object")
	}
	if !strings.Contains(err.Error(), "no JSON object") {
		t.Fatalf("error=%v, want no JSON object rejection", err)
	}
}

func TestParsePlannerSourceMapSkeletonAcceptsStringRuleArrays(t *testing.T) {
	skeleton, err := parsePlannerSourceMapSkeleton(testPlannerSourceMapSkeletonJSON(
		`"mainline_rules":["keep source","preserve causality"],"relationship_goals":["slow escalation"]`,
	))
	if err != nil {
		t.Fatalf("parsePlannerSourceMapSkeleton: %v", err)
	}
	if strings.Join(skeleton.MainlineRules, "\n") != "keep source\npreserve causality" {
		t.Fatalf("mainline_rules=%q", skeleton.MainlineRules)
	}
	if strings.Join(skeleton.RelationshipGoals, "\n") != "slow escalation" {
		t.Fatalf("relationship_goals=%q", skeleton.RelationshipGoals)
	}
}

func TestParsePlannerSourceMapSkeletonRejectsRuleTypeDrift(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
	}{
		{
			name:  "mainline string",
			extra: `"mainline_rules":"keep source"`,
		},
		{
			name:  "relationship string",
			extra: `"relationship_goals":"slow escalation"`,
		},
		{
			name:  "mainline object",
			extra: `"mainline_rules":{"rule":"keep source"}`,
		},
		{
			name:  "relationship object array",
			extra: `"relationship_goals":[{"characters":["A","B"],"goal":"slow escalation"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePlannerSourceMapSkeleton(testPlannerSourceMapSkeletonJSON(tc.extra))
			if err == nil {
				t.Fatal("parsePlannerSourceMapSkeleton should reject rule type drift")
			}
		})
	}
}

func TestBuildPlannerRepairPromptRegeneratesAfterAmbiguousOutput(t *testing.T) {
	prompt := buildPlannerRepairPrompt(
		"skeleton",
		"small original request",
		`{"candidate_marker_one":true}\n{"candidate_marker_two":true}`,
		errPlannerSourceMapMultipleJSON,
		[]string{"Return exactly one JSON object."},
	)
	if !strings.Contains(prompt, `"regenerate_from_original": true`) {
		t.Fatalf("repair prompt should request regeneration: %s", prompt)
	}
	if strings.Contains(prompt, "previous_output") || strings.Contains(prompt, "candidate_marker") {
		t.Fatalf("repair prompt should not feed ambiguous candidates back to the model: %s", prompt)
	}
}

func TestBuildPlannerRepairPromptKeepsSingleInvalidOutput(t *testing.T) {
	prompt := buildPlannerRepairPrompt(
		"skeleton",
		"small original request",
		`{"single_invalid_marker":true}`,
		errors.New("missing top-level batches array"),
		[]string{"Return exactly one JSON object."},
	)
	if !strings.Contains(prompt, "previous_output") || !strings.Contains(prompt, "single_invalid_marker") {
		t.Fatalf("repair prompt should retain one structurally invalid candidate for correction: %s", prompt)
	}
	if strings.Contains(prompt, "regenerate_from_original") {
		t.Fatalf("ordinary repair should not force full regeneration: %s", prompt)
	}
}

func TestParsePlannerSourceMapSkeletonRejectsInvalidEnvelopeShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "missing batches", text: `{"overall_arc":"missing batch envelope"}`, want: "missing top-level batches array"},
		{name: "non-array batches", text: `{"batches":{"index":1}}`, want: "batches must be an array"},
		{name: "empty batches", text: `{"batches":[]}`, want: "empty batches array"},
		{name: "invalid json decode", text: `{"batches":[,]}`, want: "invalid JSON decode"},
		{name: "standalone batch object", text: testPlannerSourceMapSkeletonBatchJSON(), want: "standalone batch object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePlannerSourceMapSkeleton(tc.text)
			if err == nil {
				t.Fatalf("parsePlannerSourceMapSkeleton should reject %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func testPlannerSourceMapSkeletonJSON(extra string) string {
	fields := []string{
		`"granularity":"arc"`,
		`"status":"proposal"`,
		`"rewrite_policy":"full_rewrite"`,
		`"brief":"arc rewrite"`,
		`"target_chapter_count":4`,
		`"batches":[` + testPlannerSourceMapSkeletonBatchJSON() + `]`,
	}
	if strings.TrimSpace(extra) != "" {
		fields = append(fields, extra)
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func testPlannerSourceMapSkeletonBatchJSON() string {
	return `{"index":1,"title":"Opening volume","theme":"orientation","target_from":1,"target_to":4,"source_from":1,"source_to":2,"summary":"valid source-map skeleton batch"}`
}

func TestParsePlannerBatchPartialRejectsAmbiguousMultipleObjects(t *testing.T) {
	batch := plannerSkeletonBatch{Index: 1, TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}
	first := plannerBatchProposalJSON(1, 1, 1, 1)
	second := strings.Replace(first, "Target 1", "Alternative 1", 1)
	_, _, _, err := parsePlannerBatchPartial(first+"\n"+second, batch)
	if !errors.Is(err, errPlannerProposalMultipleJSON) {
		t.Fatalf("error=%v, want strict multiple-object rejection", err)
	}
}

func TestNormalizePlannerBatchChaptersCanonicalizesAnnotatedPreserveEventIDs(t *testing.T) {
	batch := plannerSkeletonBatch{TargetFrom: 1, TargetTo: 1}
	chapters := []domain.AdaptationChapterPlan{{
		Chapter:        1,
		EventIDs:       []string{"src-0298-e01-ad111f23"},
		PreserveEvents: []string{"src-0298-e01-ad111f23：事件一的完整描述"},
	}}
	normalized, err := normalizePlannerBatchChapters(chapters, batch)
	if err != nil {
		t.Fatalf("normalizePlannerBatchChapters: %v", err)
	}
	if got := normalized[0].PreserveEvents; len(got) != 1 || got[0] != "src-0298-e01-ad111f23" {
		t.Fatalf("preserve_events=%v", got)
	}
}

func TestCollectPlannerBatchChaptersClearsRepeatedRepairOutput(t *testing.T) {
	batch := plannerSkeletonBatch{Index: 1, TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{"summary":"invalid_marker_repair_one"}`},
		{text: plannerBatchProposalJSON(1, 1, 1, 1)},
	}}
	chapters, err := collectPlannerBatchChaptersWithRepair(
		withAdaptationPromptModeIfMissing(context.Background(), domain.AdaptationGranularityArc),
		llm,
		"system",
		"original detail request",
		`{"summary":"invalid_marker_initial"}`,
		batch,
		nil,
		nil,
		nil,
		1,
		1,
		"detail batch",
		2,
		1,
	)
	if err != nil {
		t.Fatalf("collectPlannerBatchChaptersWithRepair: %v", err)
	}
	if len(chapters) != 1 || len(llm.got) != 2 {
		t.Fatalf("chapters=%d calls=%d, want one chapter after two repairs", len(chapters), len(llm.got))
	}
	firstRepairPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(firstRepairPrompt, "invalid_marker_initial") {
		t.Fatalf("first repair should correct the initial candidate: %s", firstRepairPrompt)
	}
	secondRepairPrompt := llm.got[1][1].TextContent()
	if !strings.Contains(secondRepairPrompt, `"regenerate_from_original": true`) ||
		strings.Contains(secondRepairPrompt, "invalid_marker_repair_one") ||
		strings.Contains(secondRepairPrompt, "previous_output") {
		t.Fatalf("second repair should restart from the original detail request: %s", secondRepairPrompt)
	}
}

func TestCollectPlannerBatchChaptersClearsAmbiguousOutputImmediately(t *testing.T) {
	batch := plannerSkeletonBatch{Index: 1, TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1}
	first := strings.Replace(plannerBatchProposalJSON(1, 1, 1, 1), "Target 1", "candidate_marker_one", 1)
	second := strings.Replace(plannerBatchProposalJSON(1, 1, 1, 1), "Target 1", "candidate_marker_two", 1)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: plannerBatchProposalJSON(1, 1, 1, 1)}}}
	chapters, err := collectPlannerBatchChaptersWithRepair(
		withAdaptationPromptModeIfMissing(context.Background(), domain.AdaptationGranularityArc), llm, "system", "original detail request", first+"\n"+second,
		batch, nil, nil, nil, 1, 1, "detail batch", 1, 1,
	)
	if err != nil {
		t.Fatalf("collectPlannerBatchChaptersWithRepair: %v", err)
	}
	if len(chapters) != 1 || len(llm.got) != 1 {
		t.Fatalf("chapters=%d calls=%d, want one clean repair", len(chapters), len(llm.got))
	}
	repairPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(repairPrompt, `"regenerate_from_original": true`) ||
		strings.Contains(repairPrompt, "candidate_marker") ||
		strings.Contains(repairPrompt, "previous_output") {
		t.Fatalf("ambiguous detail output should be cleared immediately: %s", repairPrompt)
	}
}

func TestFillMissingPlannerBatchChaptersClearsRepeatedRepairOutput(t *testing.T) {
	batch := plannerSkeletonBatch{Index: 1, TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 2}
	existingPlan, err := parsePlannerProposalStrict(plannerBatchProposalJSON(1, 1, 1, 1))
	if err != nil {
		t.Fatalf("parse existing chapter: %v", err)
	}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{"summary":"invalid_marker_missing_fill"}`},
		{text: plannerBatchProposalJSON(2, 2, 2, 2)},
	}}
	chapters, err := fillMissingPlannerBatchChapters(
		withAdaptationPromptModeIfMissing(context.Background(), domain.AdaptationGranularityArc),
		llm,
		"system",
		"original detail request",
		plannerBatchProposalJSON(1, 1, 1, 1),
		batch,
		existingPlan.Chapters,
		[]int{2},
		errors.New("missing chapter 2"),
		nil,
		1,
		1,
		"detail batch",
		2,
		1,
	)
	if err != nil {
		t.Fatalf("fillMissingPlannerBatchChapters: %v", err)
	}
	if len(chapters) != 2 || len(llm.got) != 2 {
		t.Fatalf("chapters=%d calls=%d, want completed two-chapter batch", len(chapters), len(llm.got))
	}
	secondRepairPrompt := llm.got[1][1].TextContent()
	if !strings.Contains(secondRepairPrompt, `"regenerate_from_original": true`) ||
		strings.Contains(secondRepairPrompt, "invalid_marker_missing_fill") ||
		strings.Contains(secondRepairPrompt, "previous_output") {
		t.Fatalf("second missing-chapter repair should clear failed fill output: %s", secondRepairPrompt)
	}
	if !strings.Contains(secondRepairPrompt, "existing_chapters") || !strings.Contains(secondRepairPrompt, "Target 1") {
		t.Fatalf("clean missing-chapter retry must retain accepted chapters: %s", secondRepairPrompt)
	}
}

func TestBuildAdaptationPlannerSkeletonPromptUsesFoundationDigest(t *testing.T) {
	manifest := &domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 2,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, Title: "Source 1", Runes: 100},
			{Chapter: 2, Title: "Source 2", Runes: 120},
		},
	}
	sourceMap := []plannerSourceMapEntry{
		{
			Index:        1,
			SourceFrom:   1,
			SourceTo:     2,
			PlotPhase:    "opening",
			KeyCausality: []string{"Ari accepts the call"},
		},
	}
	foundation := testSourceFoundation()
	prompt, err := buildAdaptationPlannerSkeletonUserPrompt(ProposalOptions{
		Brief:              "free restructure into 8 chapters",
		Granularity:        domain.AdaptationGranularityFree,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 8,
	}, manifest, &foundation, sourceMap, 8)
	if err != nil {
		t.Fatalf("buildAdaptationPlannerSkeletonUserPrompt: %v", err)
	}
	if !strings.Contains(prompt, `"source_foundation"`) || !strings.Contains(prompt, "A compact source premise.") {
		t.Fatalf("skeleton prompt should include compact foundation digest: %s", prompt)
	}
	if !strings.Contains(prompt, "mainline_rules and relationship_goals must each be a JSON array containing strings only") {
		t.Fatalf("skeleton prompt should state the strict string-list contract: %s", prompt)
	}
	if strings.Contains(prompt, `"chapters"`) ||
		strings.Contains(prompt, "Opening") ||
		strings.Contains(prompt, "a promise is made") ||
		strings.Contains(prompt, "station") {
		t.Fatalf("skeleton prompt should not include foundation chapter details: %s", prompt)
	}
}

func TestBuildAdaptationProposalSplitsOversizedSkeletonBatchForDetails(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "One broad volume", "theme": "pressure", "target_from": 1, "target_to": 20, "source_from": 1, "source_to": 4, "summary": "model chose a broad long-form segment"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 4)},
		{text: plannerBatchProposalJSON(5, 8, 1, 4)},
		{text: plannerBatchProposalJSON(9, 12, 1, 4)},
		{text: plannerBatchProposalJSON(13, 16, 1, 4)},
		{text: plannerBatchProposalJSON(17, 20, 1, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 6 {
		t.Fatalf("planner calls=%d, want skeleton + 5 detail calls", llm.calls)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
	if len(proposal.Volumes) != 1 || proposal.Volumes[0].TargetFrom != 1 || proposal.Volumes[0].TargetTo != 20 {
		t.Fatalf("model-planned volume should remain intact: %+v", proposal.Volumes)
	}
	firstDetailPrompt := llm.got[1][1].TextContent()
	if !strings.Contains(firstDetailPrompt, "Return exactly 4 chapter objects") ||
		!strings.Contains(firstDetailPrompt, `"target_from": 1`) ||
		!strings.Contains(firstDetailPrompt, `"target_to": 4`) {
		t.Fatalf("first detail prompt should request only the first 4 chapters: %s", firstDetailPrompt)
	}
	secondDetailPrompt := llm.got[2][1].TextContent()
	if !strings.Contains(secondDetailPrompt, "Return exactly 4 chapter objects") ||
		!strings.Contains(secondDetailPrompt, `"target_from": 5`) ||
		!strings.Contains(secondDetailPrompt, `"target_to": 8`) ||
		!strings.Contains(secondDetailPrompt, `"previous_detail_chapters"`) {
		t.Fatalf("second detail prompt should request the second 4 chapters with prior context: %s", secondDetailPrompt)
	}
	thirdDetailPrompt := llm.got[5][1].TextContent()
	if !strings.Contains(thirdDetailPrompt, "Return exactly 4 chapter objects") ||
		!strings.Contains(thirdDetailPrompt, `"target_from": 17`) ||
		!strings.Contains(thirdDetailPrompt, `"target_to": 20`) {
		t.Fatalf("third detail prompt should request remaining 4 chapters: %s", thirdDetailPrompt)
	}
}

func TestBuildAdaptationPlannerBatchPromptScopesLargeSkeletonToCurrentParent(t *testing.T) {
	skeleton := plannerSkeleton{
		Granularity:        domain.AdaptationGranularityArc,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 300,
	}
	for index := 1; index <= 75; index++ {
		from := (index-1)*4 + 1
		skeleton.Batches = append(skeleton.Batches, plannerSkeletonBatch{
			Index:              index,
			Title:              fmt.Sprintf("Volume %d", index),
			Theme:              strings.Repeat(fmt.Sprintf("theme-%d ", index), 80),
			Summary:            strings.Repeat(fmt.Sprintf("summary-%d ", index), 120),
			TargetFrom:         from,
			TargetTo:           from + 3,
			TargetChapterCount: 4,
			SourceFrom:         index,
			SourceTo:           index,
		})
	}
	detailBatch := skeleton.Batches[0]
	prompt, err := buildAdaptationPlannerBatchUserPrompt(
		ProposalOptions{Brief: "large arc", Granularity: domain.AdaptationGranularityArc, RewritePolicy: domain.AdaptationRewriteFullRewrite},
		&domain.AdaptationSourceManifest{ChapterCount: 75},
		nil,
		skeleton,
		detailBatch,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("buildAdaptationPlannerBatchUserPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Volume 1") || strings.Contains(prompt, "Volume 75") {
		t.Fatalf("detail prompt should include only its parent skeleton batch")
	}
	if len(prompt) > 20000 {
		t.Fatalf("detail prompt unexpectedly retained the full skeleton: %d bytes", len(prompt))
	}
}

func TestBuildAdaptationPlannerBatchPromptOmitsFoundationArcTree(t *testing.T) {
	foundation := testSourceFoundation()
	foundation.Volumes = []domain.VolumeOutline{{Index: 1, Title: "Source volume"}}
	for index := 1; index <= 500; index++ {
		foundation.Volumes[0].Arcs = append(foundation.Volumes[0].Arcs, domain.ArcOutline{
			Index: index,
			Title: fmt.Sprintf("FOUNDATION-ARC-%d", index),
			Goal:  strings.Repeat("dense source foundation arc ", 80),
		})
	}
	batch := testSourceMapSkeletonBatch(1, 1, 4, 1, 4)
	prompt, err := buildAdaptationPlannerBatchUserPrompt(ProposalOptions{}, nil, &foundation, plannerSkeleton{Batches: []plannerSkeletonBatch{batch}}, batch, nil, nil)
	if err != nil {
		t.Fatalf("buildAdaptationPlannerBatchUserPrompt: %v", err)
	}
	if strings.Contains(prompt, "FOUNDATION-ARC-500") {
		t.Fatalf("detail prompt should not repeat the source foundation arc tree")
	}
	if !strings.Contains(prompt, "A compact source premise.") {
		t.Fatalf("detail prompt should retain compact global foundation facts")
	}
}

func TestBuildAdaptationPlannerBatchPromptRealProjectBudget(t *testing.T) {
	outputRoot := strings.TrimSpace(os.Getenv("AINOVEL_REAL_DETAIL_OUTPUT"))
	if outputRoot == "" {
		t.Skip("set AINOVEL_REAL_DETAIL_OUTPUT for a local real-project prompt budget check")
	}
	st := store.NewStore(outputRoot)
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		t.Fatalf("LoadSourceManifest: manifest=%v err=%v", manifest, err)
	}
	foundation, err := st.Adaptation.LoadSourceFoundation()
	if err != nil || foundation == nil {
		t.Fatalf("LoadSourceFoundation: foundation=%v err=%v", foundation, err)
	}
	reports, err := st.Adaptation.LoadSourceReports()
	if err != nil {
		t.Fatalf("LoadSourceReports: %v", err)
	}
	review, err := st.Adaptation.LoadVolumeReview()
	if err != nil || review == nil {
		t.Fatalf("LoadVolumeReview: review=%v err=%v", review, err)
	}
	skeleton := plannerSkeletonFromVolumeReview(*review)
	detailBatches := plannerDetailBatches(skeleton.Batches, adaptationPlannerRecommendedBatchMax)
	if len(detailBatches) == 0 {
		t.Fatal("real project has no detail batches")
	}
	batch := detailBatches[0]
	opts := proposalOptionsFromVolumeReview(*review)
	prompt, err := buildAdaptationPlannerBatchUserPrompt(opts, manifest, foundation, skeleton, batch, reportsForPlannerDetailBatch(reports, batch), nil)
	if err != nil {
		t.Fatalf("buildAdaptationPlannerBatchUserPrompt: %v", err)
	}
	ctx := withAdaptationPromptContract(context.Background(), nil, opts.Granularity, opts.Brief)
	_, _, diagnostics, err := compilePlannerCall(ctx, "planner role", prompt, nil)
	if err != nil {
		t.Fatalf("real detail prompt should compile before model invocation: %v", err)
	}
	t.Logf("real detail prompt bytes=%d estimated_tokens=%d target=%d hard=%d", len(prompt), diagnostics.TotalTokens, diagnostics.TargetTokens, diagnostics.HardTokens)
	if diagnostics.TotalTokens > diagnostics.TargetTokens {
		t.Fatalf("real detail prompt tokens=%d, want at or below target=%d", diagnostics.TotalTokens, diagnostics.TargetTokens)
	}
}

func TestReportsForPlannerDetailBatchSlicesLargeParentRangeProportionally(t *testing.T) {
	reports := make([]domain.AdaptationSourceReport, 27)
	for index := range reports {
		reports[index].Chapter = index + 1
	}
	parent := plannerSkeletonBatch{TargetFrom: 1, TargetTo: 15, SourceFrom: 1, SourceTo: 27}
	details := plannerDetailBatches([]plannerSkeletonBatch{parent}, 4)
	if len(details) != 4 {
		t.Fatalf("detail batches=%d, want 4", len(details))
	}
	first := reportsForPlannerDetailBatch(reports, details[0])
	last := reportsForPlannerDetailBatch(reports, details[len(details)-1])
	if len(first) != 8 || first[0].Chapter != 1 || first[len(first)-1].Chapter != 8 {
		t.Fatalf("first detail reports=%+v, want source chapters 1-8", first)
	}
	if last[0].Chapter != 22 || last[len(last)-1].Chapter != 27 {
		t.Fatalf("last detail report range=%d-%d, want 22-27", last[0].Chapter, last[len(last)-1].Chapter)
	}
}

func TestPlannerDetailBatchesAssignEachMainlineEventExactlyOnce(t *testing.T) {
	parent := plannerSkeletonBatch{
		TargetFrom:       1,
		TargetTo:         8,
		MainlineEventIDs: []string{"event-1", "event-2", "event-3"},
	}

	details := plannerDetailBatches([]plannerSkeletonBatch{parent}, 4)
	if len(details) != 2 {
		t.Fatalf("detail batches=%d, want 2", len(details))
	}
	counts := make(map[string]int)
	for _, detail := range details {
		for _, eventID := range detail.MainlineEventIDs {
			counts[eventID]++
		}
	}
	for _, eventID := range parent.MainlineEventIDs {
		if counts[eventID] != 1 {
			t.Fatalf("mainline event %s assigned %d times across detail batches, want exactly once", eventID, counts[eventID])
		}
	}
}

func TestMigrateLegacyDetailEventContractReopensOnlyBlockedParent(t *testing.T) {
	skeleton := plannerSkeleton{Batches: []plannerSkeletonBatch{
		{Index: 1, TargetFrom: 1, TargetTo: 4, SourceFrom: 1, SourceTo: 4},
		{Index: 2, TargetFrom: 5, TargetTo: 12, SourceFrom: 5, SourceTo: 12},
	}}
	passed := &domain.AdaptationDetailBatchAudit{
		Version: domain.AdaptationDetailAuditVersion, Status: domain.AdaptationDetailAuditPassed,
		DeterministicPassed: true, SemanticPassed: true,
	}
	blocked := &domain.AdaptationDetailBatchAudit{
		Version: domain.AdaptationDetailAuditVersion, Status: domain.AdaptationDetailAuditRepairPending,
		Findings: []domain.AdaptationDetailAuditFinding{{Code: outlineQualityIssueArcDuplicateEvent, Blocking: true}},
	}
	runtime := &domain.AdaptationProposalRuntime{
		CompletedBatches: []domain.AdaptationProposalRuntimeBatch{
			{TargetFrom: 1, TargetTo: 4, SourceFrom: 1, SourceTo: 4, Audit: passed},
			{TargetFrom: 5, TargetTo: 8, SourceFrom: 5, SourceTo: 12, Audit: passed},
			{TargetFrom: 9, TargetTo: 12, SourceFrom: 5, SourceTo: 12, Audit: blocked},
		},
		AuditCheckpoints: []domain.AdaptationDetailAuditCheckpoint{
			{Kind: "parent", ID: "parent-1", TargetFrom: 1, TargetTo: 4},
			{Kind: "parent", ID: "parent-2", TargetFrom: 5, TargetTo: 12},
			{Kind: "global", ID: "global-outline", TargetFrom: 1, TargetTo: 12},
		},
	}

	parent, removed, migrated := migrateLegacyDetailEventContractForBlockedBatch(runtime, &skeleton)
	if !migrated || removed != 2 || parent.TargetFrom != 5 || parent.TargetTo != 12 {
		t.Fatalf("migration parent=%+v removed=%d migrated=%v", parent, removed, migrated)
	}
	if skeleton.Batches[1].DetailEventContractVersion != plannerDetailEventContractVersionPartitioned {
		t.Fatalf("parent contract version=%d", skeleton.Batches[1].DetailEventContractVersion)
	}
	if len(runtime.CompletedBatches) != 1 || runtime.CompletedBatches[0].TargetFrom != 1 {
		t.Fatalf("remaining batches=%+v", runtime.CompletedBatches)
	}
	if len(runtime.AuditCheckpoints) != 1 || runtime.AuditCheckpoints[0].ID != "parent-1" {
		t.Fatalf("remaining checkpoints=%+v", runtime.AuditCheckpoints)
	}
}

func TestBuildAdaptationPlannerBatchPromptKeepsTwoSmallBatchesForContinuity(t *testing.T) {
	previous := make([]domain.AdaptationChapterPlan, 12)
	for index := range previous {
		chapter := index + 1
		previous[index] = domain.AdaptationChapterPlan{
			OutlineEntry: domain.OutlineEntry{
				Chapter:   chapter,
				Title:     fmt.Sprintf("Chapter %d", chapter),
				CoreEvent: fmt.Sprintf("Event %d", chapter),
				Hook:      fmt.Sprintf("Hook %d", chapter),
			},
			Chapter: chapter,
			Title:   fmt.Sprintf("Chapter %d", chapter),
		}
	}
	batch := testSourceMapSkeletonBatch(4, 1, 3, 13, 16)
	skeleton := plannerSkeleton{Batches: []plannerSkeletonBatch{batch}}
	prompt, err := buildAdaptationPlannerBatchUserPrompt(ProposalOptions{}, nil, nil, skeleton, batch, nil, previous)
	if err != nil {
		t.Fatalf("buildAdaptationPlannerBatchUserPrompt: %v", err)
	}
	if strings.Contains(prompt, `"chapter": 4`) || !strings.Contains(prompt, `"chapter": 5`) || !strings.Contains(prompt, `"chapter": 12`) {
		t.Fatalf("continuity context should retain exactly the latest eight chapters")
	}
	if !strings.Contains(prompt, "Continue from the latest previous_detail_chapters hook") {
		t.Fatalf("detail prompt should explicitly require cross-batch handoff continuity")
	}
}

func TestBuildAdaptationProposalFillsMissingChunkedBatchChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 3, 1, 2)},
		{text: plannerBatchProposalJSON(4, 4, 2, 2)},
		{text: plannerBatchProposalJSON(5, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 12, 2, 3)},
		{text: plannerBatchProposalJSON(13, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 7 {
		t.Fatalf("planner calls=%d, want skeleton + partial detail + missing fill + 4 remaining details", llm.calls)
	}
	if len(proposal.Chapters) != 20 || proposal.Chapters[3].Chapter != 4 {
		t.Fatalf("missing chapter should be merged into proposal: %+v", proposal.Chapters)
	}
	missingPrompt := llm.got[2][1].TextContent()
	if !strings.Contains(missingPrompt, `"missing_chapters"`) ||
		!strings.Contains(missingPrompt, "4") ||
		!strings.Contains(missingPrompt, "existing_chapters") ||
		!strings.Contains(missingPrompt, "Target 3") ||
		!strings.Contains(missingPrompt, "Return only the chapters listed in missing_chapters") {
		t.Fatalf("missing repair prompt should carry accepted chapters and only request chapter 4: %s", missingPrompt)
	}
}

func TestBuildAdaptationProposalFillsMissingChapterWordBudget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 2)},
		{text: plannerBatchProposalJSON(5, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 12, 2, 3)},
		{text: plannerBatchProposalJSONWithoutWordBudget(13, 16, 2, 3, 15)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 6 {
		t.Fatalf("planner calls=%d, want skeleton + 5 detail calls without repair", llm.calls)
	}
	chapter := proposal.Chapters[14]
	if chapter.Chapter != 15 || chapter.WordBudget == nil || chapter.WordBudget.TargetRunes <= 0 {
		t.Fatalf("chapter 15 word budget should be filled locally: %+v", chapter)
	}
	if chapter.TargetRunes != chapter.WordBudget.TargetRunes ||
		chapter.TargetMinRunes != chapter.WordBudget.MinRunes ||
		chapter.TargetMaxRunes != chapter.WordBudget.MaxRunes {
		t.Fatalf("legacy budget fields should mirror filled word_budget: %+v", chapter)
	}
}

func TestBuildAdaptationProposalDetailsSplitsLongSourceChapterBudget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{16000})
	review := domain.AdaptationVolumeReview{
		Granularity:        domain.AdaptationGranularityArc,
		Status:             domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "split the long source chapter by plot beats",
		TargetChapterCount: 4,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Long source split", TargetFrom: 1, TargetTo: 4, SourceFrom: 1, SourceTo: 1},
		},
	}
	if err := st.Adaptation.SaveVolumeReview(review); err != nil {
		t.Fatalf("SaveVolumeReview: %v", err)
	}
	if err := st.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:            1,
		Brief:              review.Brief,
		Granularity:        domain.AdaptationGranularityArc,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 4,
		Skeleton: &domain.AdaptationProposalRuntimeOutline{
			TargetChapterCount: 4,
			Batches: []domain.AdaptationProposalRuntimeSkeletonBatch{
				{Index: 1, Title: "Long source split", TargetFrom: 1, TargetTo: 4, SourceFrom: 1, SourceTo: 1},
			},
		},
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{
		text: plannerRepeatedSourceBudgetProposalJSON(1, 4, 1, 16000, 16000, 14000, 18000),
	}}}

	proposal, err := BuildAdaptationProposalDetailsContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalDetailsOptions{})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalDetailsContext: %v", err)
	}
	if len(proposal.Chapters) != 4 {
		t.Fatalf("chapters=%d, want 4", len(proposal.Chapters))
	}
	for _, chapter := range proposal.Chapters {
		if chapter.WordBudget == nil {
			t.Fatalf("chapter %d missing word budget", chapter.Chapter)
		}
		if chapter.TargetRunes != 4000 || chapter.WordBudget.TargetRunes != 4000 {
			t.Fatalf("chapter %d target budget=%+v, want split 4000", chapter.Chapter, chapter.WordBudget)
		}
		if chapter.TargetMaxRunes > adaptationPlannerModelChapterMaxRunes || chapter.WordBudget.MaxRunes > adaptationPlannerModelChapterMaxRunes {
			t.Fatalf("chapter %d max budget=%+v exceeds model chapter max", chapter.Chapter, chapter.WordBudget)
		}
	}
	if proposal.TargetTotalRunes != 16000 {
		t.Fatalf("target total=%d, want redistributed source total 16000", proposal.TargetTotalRunes)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesRequiresBudgetSplitForLongSourceChapter(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 1, SourceRunes: 16000}
	batches, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 1, 1, 1, 1),
	}, entry)
	if err != nil {
		t.Fatalf("normalizePlannerSourceMapSkeletonBatches should apply the capacity floor: %v", err)
	}
	if len(batches) != 1 || batches[0].TargetChapterCount != 3 {
		t.Fatalf("batches=%+v, want host-raised three target chapters", batches)
	}

	batches, err = normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 1, 1, 1, 3),
	}, entry)
	if err != nil {
		t.Fatalf("normalizePlannerSourceMapSkeletonBatches accepted split skeleton: %v", err)
	}
	if len(batches) != 1 || batches[0].TargetChapterCount != 3 {
		t.Fatalf("batches=%+v, want three target chapters", batches)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesUsesCapacityReviewForLowBudget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sourceRunes int
		count       int
	}{
		{name: "5319 in one target", sourceRunes: 5319, count: 1},
		{name: "10637 in two targets", sourceRunes: 10637, count: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 1, SourceRunes: tc.sourceRunes}
			batches, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{
				testSourceMapSkeletonBatch(1, 1, 1, 1, tc.count),
			}, entry)
			if err != nil {
				t.Fatalf("normalizePlannerSourceMapSkeletonBatches: %v", err)
			}
			if len(batches) != 1 || batches[0].TargetChapterCount != tc.count {
				t.Fatalf("batches=%+v, want count %d", batches, tc.count)
			}
			if plannerBudgetDeviationAccepted(batches[0]) {
				t.Fatalf("capacity-accepted batch should not be marked as deviation: %+v", batches[0])
			}
		})
	}
}

func TestPlannerBudgetGroupSplitUsesArcReviewCapacity(t *testing.T) {
	policy := plannerChapterBudgetPolicyForGranularity(domain.AdaptationGranularityArc)
	if policy == nil {
		t.Fatal("arc policy is nil")
	}
	if policy.SourceReviewCapacityRunes <= policy.MaxRunes {
		t.Fatalf("SourceReviewCapacityRunes=%d, want softer than MaxRunes=%d", policy.SourceReviewCapacityRunes, policy.MaxRunes)
	}

	chapters := plannerBudgetRangePlans(25, 36, 14, 20)
	group := plannerChapterBudgetGroup{
		Indexes:     plannerIndexRange(len(chapters)),
		SourceFrom:  14,
		SourceTo:    20,
		SourceRunes: 60049,
	}
	if err := plannerBudgetGroupSplitError(chapters, group, *policy); err != nil {
		t.Fatalf("arc final budget split should use review capacity, not hard max_runes: %v", err)
	}

	thinChapters := plannerBudgetRangePlans(25, 34, 14, 20)
	group.Indexes = plannerIndexRange(len(thinChapters))
	err := plannerBudgetGroupSplitError(thinChapters, group, *policy)
	var splitErr *plannerProposalBudgetSplitError
	if !errors.As(err, &splitErr) {
		t.Fatalf("under-split arc range should still fail, got %v", err)
	}
	wantMin := ceilPositiveDiv(group.SourceRunes, policy.SourceReviewCapacityRunes)
	if splitErr.MinChapters != wantMin {
		t.Fatalf("MinChapters=%d, want %d", splitErr.MinChapters, wantMin)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesRequiresLowBudgetRationale(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 4, SourceRunes: 29588}
	autoExpanded, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 1, 4, 1, 4),
	}, entry)
	if err != nil || len(autoExpanded) != 1 || autoExpanded[0].TargetChapterCount != 6 {
		t.Fatalf("low budget without compression rationale should be raised to six: batches=%+v err=%v", autoExpanded, err)
	}

	batch := testSourceMapSkeletonBatch(1, 1, 4, 1, 4)
	batch.BudgetDecision = "compress_or_merge"
	batch.BudgetReason = "merge side investigations and compress travel beats"
	batches, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{batch}, entry)
	if err != nil {
		t.Fatalf("normalizePlannerSourceMapSkeletonBatches should accept explicit compression rationale: %v", err)
	}
	if len(batches) != 1 || !plannerBudgetDeviationAccepted(batches[0]) {
		t.Fatalf("accepted low deviation should mark only the batch, got %+v", batches)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesUsesExactSubrangeRunes(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 10, SourceRunes: 50000}
	batches := []plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 1, 3, 1, 3),
		testSourceMapSkeletonBatch(2, 4, 6, 4, 6),
		testSourceMapSkeletonBatch(3, 7, 10, 7, 10),
	}
	sourceRunesByChapter := map[int]int{
		1:  5000,
		2:  5000,
		3:  5000,
		4:  6000,
		5:  6000,
		6:  6000,
		7:  4000,
		8:  5000,
		9:  5000,
		10: 5000,
	}

	if _, err := normalizePlannerSourceMapSkeletonBatchesForGranularity(batches, entry, domain.AdaptationGranularityArc); err != nil {
		t.Fatalf("proportional fallback should accept this split before exact runes are available: %v", err)
	}
	exact, err := normalizePlannerSourceMapSkeletonBatchesForGranularityWithSourceRunes(batches, entry, domain.AdaptationGranularityArc, sourceRunesByChapter)
	if err != nil {
		t.Fatalf("exact rune capacity should be applied without another model retry: %v", err)
	}
	if exact[1].TargetChapterCount != 4 {
		t.Fatalf("exact 18000-rune subrange should be raised to four targets: %+v", exact[1])
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesAllowsExplicitCompressionAfterBudgetReview(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 1, SourceRunes: 16000}
	batch := testSourceMapSkeletonBatch(1, 1, 1, 1, 1)
	batch.BudgetDecision = "compress_or_merge"
	batch.BudgetReason = "compress the source chapter into one target opening"

	batches, err := normalizePlannerSourceMapSkeletonBatchesAllowBudgetDeviation([]plannerSkeletonBatch{
		batch,
	}, entry)
	if err != nil {
		t.Fatalf("allow budget deviation should accept intentional compression after review: %v", err)
	}
	if len(batches) != 1 || batches[0].TargetChapterCount != 1 {
		t.Fatalf("batches=%+v, want compressed single target chapter", batches)
	}
	if !plannerBudgetDeviationAccepted(batches[0]) {
		t.Fatalf("accepted compression should be marked: %+v", batches[0])
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesFreeKeepsSourceRunesSoft(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 79, SourceTo: 79, SourceRunes: 20067}

	batches, err := normalizePlannerSourceMapSkeletonBatchesForGranularity([]plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 79, 79, 1, 1),
	}, entry, domain.AdaptationGranularityFree)
	if err != nil {
		t.Fatalf("free source-map skeleton should accept compressed long source chapter: %v", err)
	}
	if len(batches) != 1 || batches[0].TargetChapterCount != 1 {
		t.Fatalf("batches=%+v, want one free target chapter", batches)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesFreeAllowsSharedSourceMapRanges(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 8, SourceTo: 12, SourceRunes: 61104}
	batches := []plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 8, 10, 1, 3),
		testSourceMapSkeletonBatch(2, 9, 12, 4, 6),
	}

	normalized, err := normalizePlannerSourceMapSkeletonBatchesForGranularity(batches, entry, domain.AdaptationGranularityFree)
	if err != nil {
		t.Fatalf("free source-map skeleton should accept overlapping internal ranges: %v", err)
	}
	if len(normalized) != 2 {
		t.Fatalf("normalized batches=%d, want 2 story batches", len(normalized))
	}
	if normalized[0].SourceFrom != 8 || normalized[0].SourceTo != 10 ||
		normalized[1].SourceFrom != 9 || normalized[1].SourceTo != 12 {
		t.Fatalf("free batch source ranges should stay inside the entry without strict partitioning: %+v", normalized)
	}
	if normalized[0].TargetChapterCount != 3 || normalized[1].TargetChapterCount != 3 {
		t.Fatalf("target chapter counts should be preserved: %+v", normalized)
	}

	arcBatches, err := normalizePlannerSourceMapSkeletonBatches(batches, entry)
	if err != nil {
		t.Fatalf("strict arc/default normalizer should repair the shared boundary: %v", err)
	}
	if arcBatches[0].SourceFrom != 8 || arcBatches[0].SourceTo != 10 ||
		arcBatches[1].SourceFrom != 11 || arcBatches[1].SourceTo != 12 {
		t.Fatalf("arc ranges should be normalized to an inclusive partition: %+v", arcBatches)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesFreeAllowsRepeatedSingleSourceRange(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 29, SourceFrom: 140, SourceTo: 140, SourceRunes: 1000}
	batches := []plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 140, 140, 1, 1),
		testSourceMapSkeletonBatch(2, 140, 140, 2, 2),
	}

	normalized, err := normalizePlannerSourceMapSkeletonBatchesForGranularity(batches, entry, domain.AdaptationGranularityFree)
	if err != nil {
		t.Fatalf("free source-map skeleton should accept repeated single-source story batches: %v", err)
	}
	if len(normalized) != 2 {
		t.Fatalf("normalized batches=%d, want 2 repeated story batches", len(normalized))
	}

	_, err = normalizePlannerSourceMapSkeletonBatches(batches, entry)
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("strict arc/default normalizer should still reject non-advancing repeated range, got %v", err)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesAppliesRuneCapacityFloor(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 5, SourceFrom: 17, SourceTo: 20, SourceRunes: 67196}
	batches := []plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 17, 18, 1, 3),
		testSourceMapSkeletonBatch(2, 18, 20, 4, 6),
	}

	normalized, err := normalizePlannerSourceMapSkeletonBatches(batches, entry)
	if err != nil {
		t.Fatalf("long source range should converge without model budget repair: %v", err)
	}
	if got := plannerSkeletonBatchTargetCount(normalized); got != 12 {
		t.Fatalf("target chapter count=%d, want rune capacity floor 12", got)
	}
	if normalized[0].SourceFrom != 17 || normalized[0].SourceTo != 18 ||
		normalized[1].SourceFrom != 19 || normalized[1].SourceTo != 20 {
		t.Fatalf("overlap should be normalized to a strict partition: %+v", normalized)
	}
	if normalized[1].BudgetDecision != "expand_or_split" || normalized[1].BudgetReason == "" {
		t.Fatalf("host capacity adjustment should remain observable: %+v", normalized[1])
	}
}

func TestBuildAdaptationPlannerSkeletonUserPromptIncludesInitialSourceRuneBudgetNotes(t *testing.T) {
	prompt, err := buildAdaptationPlannerSkeletonUserPrompt(ProposalOptions{
		Brief:         "arc rewrite",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
	}, &domain.AdaptationSourceManifest{ChapterCount: 1}, nil, []plannerSourceMapEntry{
		{Index: 1, SourceFrom: 1, SourceTo: 1, SourceRunes: 16000},
	}, 0)
	if err != nil {
		t.Fatalf("buildAdaptationPlannerSkeletonUserPrompt: %v", err)
	}
	if !strings.Contains(prompt, `"source_map_budget_notes"`) ||
		!strings.Contains(prompt, "source_runes=16000") ||
		!strings.Contains(prompt, "should total at least 3") ||
		!strings.Contains(prompt, "source-map skeleton review") ||
		!strings.Contains(prompt, "word_budget.max_runes still must stay within 5000") ||
		!strings.Contains(prompt, "compress_or_merge") ||
		!strings.Contains(prompt, "first-pass budget guidance") {
		t.Fatalf("prompt should include initial long-source budget guidance: %s", prompt)
	}
}

func TestBuildAdaptationPlannerSkeletonUserPromptTreatsFreeSourceRunesAsSoft(t *testing.T) {
	prompt, err := buildAdaptationPlannerSkeletonUserPrompt(ProposalOptions{
		Brief:         "free rewrite",
		Granularity:   domain.AdaptationGranularityFree,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
	}, &domain.AdaptationSourceManifest{ChapterCount: 1}, nil, []plannerSourceMapEntry{
		{Index: 1, SourceFrom: 1, SourceTo: 1, SourceRunes: 16000},
	}, 0)
	if err != nil {
		t.Fatalf("buildAdaptationPlannerSkeletonUserPrompt: %v", err)
	}
	if !strings.Contains(prompt, `"source_map_budget_notes"`) ||
		!strings.Contains(prompt, "source_runes=16000") ||
		!strings.Contains(prompt, "density and context") ||
		!strings.Contains(prompt, "word_budget.max_runes") ||
		!strings.Contains(prompt, "fixed source_map range") ||
		!strings.Contains(prompt, "may share or overlap the same source range") {
		t.Fatalf("free prompt should keep source runes as soft density while retaining chapter max budget: %s", prompt)
	}
	if strings.Contains(prompt, "should total at least 4") ||
		strings.Contains(prompt, "source_map.source_runes must drive splitting") ||
		strings.Contains(prompt, "must strictly partition") ||
		strings.Contains(prompt, "strictly partition the provided source_map range") {
		t.Fatalf("free prompt should not require source-rune-driven chapter splitting: %s", prompt)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesIgnoresFutureRangeSpillover(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 40}
	batches, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 1, 10, 1, 3),
		testSourceMapSkeletonBatch(2, 11, 25, 1, 4),
		testSourceMapSkeletonBatch(3, 26, 40, 1, 5),
		testSourceMapSkeletonBatch(4, 41, 70, 1, 6),
	}, entry)
	if err != nil {
		t.Fatalf("normalizePlannerSourceMapSkeletonBatches should ignore future spillover batches: %v", err)
	}
	if len(batches) != 3 {
		t.Fatalf("batches=%d, want only the three in-range batches", len(batches))
	}
	if batches[len(batches)-1].SourceTo != 40 {
		t.Fatalf("last in-range source_to=%d, want 40", batches[len(batches)-1].SourceTo)
	}
}

func TestNormalizePlannerSkeletonReassignsDriftedTargetRanges(t *testing.T) {
	skeleton := plannerSkeleton{
		Granularity:        domain.AdaptationGranularityArc,
		Status:             domain.AdaptationPlanStatusProposal,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 156,
		Batches: []plannerSkeletonBatch{
			{Index: 1, Title: "Opening", Theme: "setup", TargetChapterCount: 164, TargetFrom: 1, TargetTo: 155, SourceFrom: 1, SourceTo: 10, Summary: "opening span"},
			{Index: 23, Title: "Drifted", Theme: "payoff", TargetChapterCount: 4, TargetFrom: 156, TargetTo: 159, SourceFrom: 11, SourceTo: 12, Summary: "model drifted the chapter range"},
		},
	}

	err := normalizePlannerSkeleton(&skeleton, ProposalOptions{
		Brief:         "arc rewrite",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
	}, &domain.AdaptationSourceManifest{ChapterCount: 12}, 0)
	if err != nil {
		t.Fatalf("normalizePlannerSkeleton should reassign drifted target ranges: %v", err)
	}
	if skeleton.Batches[1].TargetFrom != 165 || skeleton.Batches[1].TargetTo != 168 {
		t.Fatalf("drifted batch target range=%d-%d, want 165-168", skeleton.Batches[1].TargetFrom, skeleton.Batches[1].TargetTo)
	}
	if skeleton.TargetChapterCount != 168 {
		t.Fatalf("TargetChapterCount=%d, want normalized batch total 168", skeleton.TargetChapterCount)
	}
}

func TestBuildAdaptationProposalRetriesTransientPlannerGenerateError(t *testing.T) {
	restore := stubPlannerRetrySleep(t)
	defer restore()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{err: litellm.NewHTTPError("deepseek", 502, "<html><body>502 Bad Gateway</body></html>")},
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 2)},
		{text: plannerBatchProposalJSON(5, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 12, 2, 3)},
		{text: plannerBatchProposalJSON(13, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}
	var progress []Event

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              "free restructure into 20 chapters",
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
		EmitProgress:       captureAdaptProgress(&progress),
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 7 {
		t.Fatalf("planner calls=%d, want failed skeleton attempt + retry + 5 detail calls", llm.calls)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
	if !hasAdaptProgress(progress, "重试 2/7") || !hasAdaptProgress(progress, "provider gateway error: 502 Bad Gateway") {
		t.Fatalf("progress should expose retry count and model error: %+v", progress)
	}
}

func TestBuildAdaptationProposalRepairsChunkedBatchWithoutChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 20 chapters"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free restructure into 20 chapters",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
				{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
				{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
			]
		}`},
		{text: `{"summary":"batch outline only","key_turns":["setup","decision"]}`},
		{text: `{
			"chapter": "第1章",
			"title": "Only one chapter",
			"core_event": "The model still returned one chapter object instead of a chapters array.",
			"hook": "The schema is still wrong.",
			"scenes": ["station"],
			"source_chapters": [1],
			"source_range": {"from": 1, "to": 1},
			"word_budget": {"source_runes": 10, "target_runes": 12, "min_runes": 10, "max_runes": 14, "tolerance": 0.15},
			"preserve_events": ["source event"],
			"required_changes": ["repair the shape"],
			"forbidden_moves": ["single chapter object"]
		}`},
		{text: plannerBatchProposalJSON(1, 4, 1, 2)},
		{text: plannerBatchProposalJSON(5, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 12, 2, 3)},
		{text: plannerBatchProposalJSON(13, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 8 {
		t.Fatalf("planner calls=%d, want skeleton + failed detail + two repairs + remaining details", llm.calls)
	}
	repairPrompt := llm.got[2][1].TextContent()
	if !strings.Contains(repairPrompt, "shaped exactly like") ||
		!strings.Contains(repairPrompt, "chapters 1 through 4") {
		t.Fatalf("batch repair prompt should explain chapter schema failure: %s", repairPrompt)
	}
	missingRepairPrompt := llm.got[3][1].TextContent()
	if !strings.Contains(missingRepairPrompt, "missing_chapters") ||
		!strings.Contains(missingRepairPrompt, "existing_chapters") ||
		!strings.Contains(missingRepairPrompt, "Only one chapter") {
		t.Fatalf("missing-chapter repair prompt should include existing partial context: %s", missingRepairPrompt)
	}
	if len(proposal.Chapters) != 20 {
		t.Fatalf("chapters=%d, want 20", len(proposal.Chapters))
	}
}

func TestParsePlannerProposalCollectsLooseChapterObjects(t *testing.T) {
	proposal, err := parsePlannerProposal(`
		{"chapter":1,"title":"Loose One","coreEvent":"Ari finds the first clue.","hook":"The clue points onward.","scenes":["archive"],"sourceChapters":[1],"sourceRange":{"from":1,"to":1},"wordBudget":{"sourceRunes":10,"targetRunes":12,"minRunes":11,"maxRunes":13,"tolerance":0.15},"preserveEvents":["first source beat"],"requiredChanges":["adapt first beat"],"forbiddenMoves":["drop first anchor"]}
		{"chapter":2,"title":"Loose Two","coreEvent":"Ari chooses the second path.","hook":"The new route opens.","scenes":["station"],"sourceChapters":[2],"sourceRange":{"from":2,"to":2},"wordBudget":{"sourceRunes":20,"targetRunes":22,"minRunes":21,"maxRunes":23,"tolerance":0.15},"preserveEvents":["second source beat"],"requiredChanges":["adapt second beat"],"forbiddenMoves":["drop second anchor"]}
	`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if len(proposal.Chapters) != 2 || proposal.Chapters[0].Title != "Loose One" || proposal.Chapters[1].Title != "Loose Two" {
		t.Fatalf("loose chapter objects were not collected: %+v", proposal.Chapters)
	}
	if proposal.Chapters[0].CoreEvent == "" || proposal.Chapters[0].WordBudget == nil || len(proposal.Chapters[0].SourceChapters) != 1 {
		t.Fatalf("loose chapter aliases were not normalized: %+v", proposal.Chapters[0])
	}
}

func TestParsePlannerProposalCollectsSingleChapterObjectWithTextChapter(t *testing.T) {
	proposal, err := parsePlannerProposal(`{
		"chapter": "第1章",
		"title": "Text Chapter Number",
		"core_event": "Ari finds the first clue.",
		"hook": "The clue points onward.",
		"scenes": ["archive"],
		"source_chapters": [1],
		"source_range": {"from": 1, "to": 1},
		"word_budget": {"source_runes": 10, "target_runes": 12, "min_runes": 11, "max_runes": 13, "tolerance": 0.15},
		"preserve_events": ["first source beat"],
		"required_changes": ["adapt first beat"],
		"forbidden_moves": ["drop first anchor"]
	}`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if len(proposal.Chapters) != 1 || proposal.Chapters[0].Chapter != 1 || proposal.Chapters[0].Title != "Text Chapter Number" {
		t.Fatalf("text chapter object was not normalized: %+v", proposal.Chapters)
	}
}

func TestBuildAdaptationProposalRejectsChunkedSkeletonThatShrinksLongTarget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	brief := "free restructure into 50-60章"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free restructure into 50-60章",
		"target_chapter_count": 17,
		"batches": [
			{"index": 1, "title": "Compressed arc", "theme": "rushed", "target_from": 1, "target_to": 17, "source_from": 1, "source_to": 4, "summary": "too short"}
		]
	}`}}}

	_, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              brief,
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 60,
	})
	if err == nil {
		t.Fatal("BuildAdaptationProposal should reject a skeleton that ignores the long target")
	}
	if !strings.Contains(err.Error(), "ignores long-form scale hint") {
		t.Fatalf("error=%v, want long-form shrink rejection", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want stop after skeleton", llm.calls)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved != nil {
		t.Fatalf("rejected skeleton should not save proposal: %+v", saved)
	}
}

func TestInferTargetChapterCountFromBrief(t *testing.T) {
	cases := []struct {
		brief string
		want  int
	}{
		{brief: "plan 50-60章", want: 60},
		{brief: "plan 50 60章", want: 60},
		{brief: "规划20多章节", want: 25},
		{brief: "规划二十多章", want: 25},
		{brief: "规划五六十章", want: 60},
		{brief: "规划713章", want: 713},
		{brief: "第15章补一个误会", want: 0},
		{brief: "第290-291章联手施展南斗剑光", want: 0},
		{brief: "ch290-291章联手施展南斗剑光", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.brief, func(t *testing.T) {
			if got := inferTargetChapterCount(tc.brief); got != tc.want {
				t.Fatalf("inferTargetChapterCount(%q)=%d, want %d", tc.brief, got, tc.want)
			}
		})
	}
}

func TestPlannerTargetChapterCountUsesLongSourceScaleHint(t *testing.T) {
	manifest := &domain.AdaptationSourceManifest{ChapterCount: 713}
	got := plannerTargetChapterCount(ProposalOptions{
		Brief:       "arc rewrite without explicit target count",
		Granularity: domain.AdaptationGranularityArc,
	}, manifest)
	if got != 713 {
		t.Fatalf("target chapter scale hint=%d, want 713", got)
	}
	role := plannerTargetChapterHintRole(ProposalOptions{
		Brief:       "arc rewrite without explicit target count",
		Granularity: domain.AdaptationGranularityArc,
	}, manifest, got)
	if role != "source_scale_minimum" {
		t.Fatalf("hint role=%q, want source_scale_minimum", role)
	}
}

func TestBuildAdaptationProposalFillsMissingPlannerConstants(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	brief := "arc restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"brief": "model-side summary",
		"chapters": [
			{
				"chapter": 1,
				"title": "Merged opening",
				"core_event": "Ari combines both source turns.",
				"hook": "A shared clue reframes both turns.",
				"scenes": ["station", "archive"],
				"source_chapters": [1, 2],
				"source_range": {"from": 1, "to": 2},
				"word_budget": {"source_runes": 30, "target_runes": 35, "min_runes": 30, "max_runes": 40, "tolerance": 0.15}
			}
		]
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityArc,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if proposal.Granularity != domain.AdaptationGranularityArc ||
		proposal.Status != domain.AdaptationPlanStatusProposal ||
		proposal.RewritePolicy != domain.AdaptationRewriteFullRewrite ||
		proposal.Brief != brief {
		t.Fatalf("proposal constants were not restored: %+v", proposal)
	}
}

func TestBuildAdaptationProposalAcceptsLongBriefRuleSet(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	parts := make([]string, 0, 96)
	for index := 1; index <= 96; index++ {
		parts = append(parts, fmt.Sprintf("必须落实长篇约束%d", index))
	}
	brief := strings.Join(parts, "。")
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"chapters": [{
			"chapter": 1,
			"title": "Merged opening",
			"core_event": "The opening combines both source turns.",
			"hook": "A shared clue reframes both turns.",
			"scenes": ["station", "archive"],
			"source_chapters": [1, 2],
			"source_range": {"from": 1, "to": 2},
			"word_budget": {"source_runes": 30, "target_runes": 35, "min_runes": 30, "max_runes": 40, "tolerance": 0.15}
		}]
	}`}}}

	proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityArc,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if proposal.Brief != brief {
		t.Fatal("complete long brief was not preserved")
	}
	if len(proposal.Rules) != len(parts) {
		t.Fatalf("durable rules=%d, want %d", len(proposal.Rules), len(parts))
	}
}

func TestBuildAdaptationProposalRejectsPlannerWithNoChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	brief := "free restructure"
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"generated_at": "2026-07-02T00:00:00Z",
		"model": "fake",
		"notes": "metadata only",
		"prompt": "adaptation-planner",
		"prompt_version": "v1"
	}`}}}

	_, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       brief,
		Granularity: domain.AdaptationGranularityFree,
	})
	if err == nil {
		t.Fatal("BuildAdaptationProposal should reject planner output with no chapters")
	}
	if !strings.Contains(err.Error(), "planner proposal has no chapters") {
		t.Fatalf("error = %v, want no-chapters planner error", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want 1", llm.calls)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved != nil {
		t.Fatalf("unusable planner output should not save proposal: %+v", saved)
	}
}

func TestParsePlannerProposalAcceptsNestedChapterAliases(t *testing.T) {
	proposal, err := parsePlannerProposal(`{
		"adaptation_proposal": {
			"granularity": "free",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "free rewrite",
			"planner": {"notes": "singleton note"},
			"chapter_plans": [
				{
					"chapter": 1,
					"title": "Opening",
					"core_event": "A new opening reframes the source.",
					"hook": "The last image unsettles the lead.",
					"scenes": ["room", "street"],
					"source_chapters": [1],
					"source_range": {"from": 1, "to": 1},
					"word_budget": {"source_runes": 10, "target_runes": 20, "min_runes": 10, "max_runes": 30}
				}
			]
		}
	}`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if proposal.Granularity != domain.AdaptationGranularityFree || len(proposal.Chapters) != 1 {
		t.Fatalf("nested proposal not decoded: %+v", proposal)
	}
	if proposal.Chapters[0].Title != "Opening" || proposal.Chapters[0].CoreEvent == "" {
		t.Fatalf("chapter alias not decoded: %+v", proposal.Chapters[0])
	}
	if proposal.Planner == nil || len(proposal.Planner.Notes) != 1 || proposal.Planner.Notes[0] != "singleton note" {
		t.Fatalf("planner string note not decoded: %+v", proposal.Planner)
	}
}

func TestParsePlannerProposalAcceptsTargetChapterPlanAlias(t *testing.T) {
	proposal, err := parsePlannerProposal(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free rewrite",
		"targetChapterPlans": [
			{
				"chapter": 1,
				"title": "Opening",
				"core_event": "A new opening reframes the source.",
				"hook": "The last image unsettles the lead.",
				"scenes": ["room", "street"],
				"source_chapters": [1],
				"source_range": {"from": 1, "to": 1},
				"word_budget": {"source_runes": 10, "target_runes": 20, "min_runes": 10, "max_runes": 30}
			}
		]
	}`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if len(proposal.Chapters) != 1 || proposal.Chapters[0].Title != "Opening" {
		t.Fatalf("targetChapterPlans alias not decoded: %+v", proposal.Chapters)
	}
}

func TestParsePlannerProposalSkipsLeadingMetadataObject(t *testing.T) {
	proposal, err := parsePlannerProposal(`{"prompt":"adaptation-planner","prompt_version":"v1","model":"fake","notes":"metadata only"}
	{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free rewrite",
		"chapters": [
			{
				"chapter": 1,
				"title": "Opening",
				"core_event": "A new opening reframes the source.",
				"hook": "The last image unsettles the lead.",
				"scenes": ["room", "street"],
				"source_chapters": [1],
				"source_range": {"from": 1, "to": 1},
				"word_budget": {"source_runes": 10, "target_runes": 20, "min_runes": 10, "max_runes": 30}
			}
		]
	}`)
	if err != nil {
		t.Fatalf("parsePlannerProposal: %v", err)
	}
	if len(proposal.Chapters) != 1 || proposal.Chapters[0].Title != "Opening" {
		t.Fatalf("leading metadata object should be skipped: %+v", proposal.Chapters)
	}
}

func TestBuildAdaptationProposalRejectsInvalidPlannerOutput(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "arc",
		"status": "confirmed",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				"chapter": 1,
				"title": "Invalid",
				"core_event": "Ari moves.",
				"hook": "A bad status is rejected.",
				"scenes": ["station"],
				"source_chapters": [1, 2],
				"word_budget": {"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15}
			}
		]
	}`}}}

	if _, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       "arc restructure",
		Granularity: domain.AdaptationGranularityArc,
	}); err == nil {
		t.Fatal("BuildAdaptationProposal should reject invalid planner output")
	}
	proposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if proposal != nil {
		t.Fatalf("invalid planner output should not save proposal: %+v", proposal)
	}
}

func TestBuildAdaptationProposalRejectsInvalidPlannerWordBudgets(t *testing.T) {
	cases := []struct {
		name          string
		planFields    string
		chapterFields string
		wordBudget    string
		wantErr       string
	}{
		{
			name:       "proposal target total conflicts with chapter budgets",
			planFields: `"target_total_runes": 31`,
			wordBudget: `"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:    "target_total_runes",
		},
		{
			name:          "legacy chapter target conflicts with nested budget",
			chapterFields: `"target_runes": 31`,
			wordBudget:    `"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:       "target_runes",
		},
		{
			name:          "legacy chapter target min conflicts with nested budget",
			chapterFields: `"target_min_runes": 24`,
			wordBudget:    `"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:       "target_min_runes",
		},
		{
			name:          "legacy chapter target max conflicts with nested budget",
			chapterFields: `"target_max_runes": 36`,
			wordBudget:    `"source_runes": 30, "target_runes": 30, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:       "target_max_runes",
		},
		{
			name:       "nested target falls outside nested min max",
			wordBudget: `"source_runes": 30, "target_runes": 40, "min_runes": 25, "max_runes": 35, "tolerance": 0.15`,
			wantErr:    "within min_runes..max_runes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewStore(t.TempDir())
			if err := st.Init(); err != nil {
				t.Fatalf("Init: %v", err)
			}
			seedPreparedAdaptationSource(t, st, []int{10, 20})
			llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: plannerBudgetProposalJSON(tc.planFields, tc.chapterFields, tc.wordBudget)}}}

			if _, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
				Brief:       "arc restructure",
				Granularity: domain.AdaptationGranularityArc,
			}); err == nil {
				t.Fatal("BuildAdaptationProposal should reject invalid planner word budget")
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error=%q, want substring %q", err.Error(), tc.wantErr)
			}
			proposal, err := st.Adaptation.LoadProposal()
			if err != nil {
				t.Fatalf("LoadProposal: %v", err)
			}
			if proposal != nil {
				t.Fatalf("invalid planner word budget should not save proposal: %+v", proposal)
			}
		})
	}
}

func TestReviseAdaptationProposalSupportsChapterRangeAndVolumeTargets(t *testing.T) {
	cases := []struct {
		name          string
		options       ProposalRevisionOptions
		responses     []adaptLLMResponse
		wantCalls     int
		wantRevised   []int
		wantUnchanged []int
	}{
		{
			name: "single chapter",
			options: ProposalRevisionOptions{
				FromChapter: 3,
				Instruction: "raise the chapter three hook",
			},
			responses:     []adaptLLMResponse{{text: plannerRevisionProposalJSON(3, 3, 1, 1)}},
			wantCalls:     1,
			wantRevised:   []int{3},
			wantUnchanged: []int{2, 4},
		},
		{
			name: "reversed continuous range",
			options: ProposalRevisionOptions{
				FromChapter: 8,
				ToChapter:   5,
				Instruction: "smooth the middle four chapters",
			},
			responses:     []adaptLLMResponse{{text: plannerRevisionProposalJSON(5, 8, 2, 3)}},
			wantCalls:     1,
			wantRevised:   []int{5, 6, 7, 8},
			wantUnchanged: []int{4, 9},
		},
		{
			name: "specific volume",
			options: ProposalRevisionOptions{
				VolumeIndex: 2,
				Instruction: "make the second volume flow better",
			},
			responses: []adaptLLMResponse{
				{text: plannerVolumeRevisionSkeletonJSON(2, 5, 8, 2, 3)},
				{text: plannerRevisionProposalJSON(5, 8, 2, 3)},
			},
			wantCalls:     2,
			wantRevised:   []int{5, 6, 7, 8},
			wantUnchanged: []int{4, 9},
		},
		{
			name: "all volumes batches revision requests",
			options: ProposalRevisionOptions{
				VolumeIndex: -1,
				Instruction: "rebalance every volume",
			},
			responses: []adaptLLMResponse{
				{text: plannerRevisionProposalJSON(1, 8, 1, 3)},
				{text: plannerRevisionProposalJSON(9, 12, 3, 4)},
			},
			wantCalls:   2,
			wantRevised: []int{1, 8, 9, 12},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewStore(t.TempDir())
			if err := st.Init(); err != nil {
				t.Fatalf("Init: %v", err)
			}
			seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
			saveRevisionTestProposal(t, st)
			llm := &scriptedAdaptLLM{responses: tc.responses}

			updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, tc.options)
			if err != nil {
				t.Fatalf("ReviseAdaptationProposal: %v", err)
			}
			if llm.calls != tc.wantCalls {
				t.Fatalf("revision planner calls=%d, want %d", llm.calls, tc.wantCalls)
			}
			if len(updated.Chapters) != 12 {
				t.Fatalf("chapters=%d, want 12", len(updated.Chapters))
			}
			for _, chapter := range tc.wantRevised {
				got := updated.Chapters[chapter-1]
				if !strings.HasPrefix(got.Title, "Revised ") {
					t.Fatalf("chapter %d was not revised: %+v", chapter, got)
				}
			}
			for _, chapter := range tc.wantUnchanged {
				got := updated.Chapters[chapter-1]
				if !strings.HasPrefix(got.Title, "Original ") {
					t.Fatalf("chapter %d should be unchanged: %+v", chapter, got)
				}
			}
			if len(updated.Planner.Notes) == 0 || !strings.Contains(updated.Planner.Notes[len(updated.Planner.Notes)-1], tc.options.Instruction) {
				t.Fatalf("revision note missing instruction: %+v", updated.Planner)
			}
			saved, err := st.Adaptation.LoadProposal()
			if err != nil {
				t.Fatalf("LoadProposal: %v", err)
			}
			if saved == nil || saved.Chapters[tc.wantRevised[0]-1].Title != updated.Chapters[tc.wantRevised[0]-1].Title {
				t.Fatalf("revised proposal was not saved: saved=%+v updated=%+v", saved, updated)
			}
		})
	}
}

func TestReviseAdaptationProposalAllowsFinalVolumeEndingExpansion(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(3, 9, 14, 3, 4)},
		{text: plannerRevisionProposalJSON(9, 12, 3, 4)},
		{text: plannerRevisionProposalJSON(13, 14, 3, 4)},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "补充最后一卷结尾，新增两个章节",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposal: %v", err)
	}
	if len(updated.Chapters) != 14 {
		t.Fatalf("chapters=%d, want 14", len(updated.Chapters))
	}
	if !strings.HasPrefix(updated.Chapters[7].Title, "Original 8") {
		t.Fatalf("chapter 8 should stay unchanged: %+v", updated.Chapters[7])
	}
	for _, chapter := range []int{9, 12, 13, 14} {
		got := updated.Chapters[chapter-1]
		if !strings.HasPrefix(got.Title, "Revised ") {
			t.Fatalf("chapter %d was not revised/appended: %+v", chapter, got)
		}
	}
	if len(updated.Volumes) != 3 || updated.Volumes[2].TargetTo != 14 {
		t.Fatalf("final volume should extend to chapter 14: %+v", updated.Volumes)
	}
	if updated.Volumes[2].Title != "Revised volume 3" || updated.Volumes[2].Summary != "Replanned volume beats." {
		t.Fatalf("final volume metadata was not updated: %+v", updated.Volumes[2])
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 14 || len(saved.Volumes) != 3 || saved.Volumes[2].TargetTo != 14 || saved.Volumes[2].Title != "Revised volume 3" {
		t.Fatalf("expanded proposal was not saved: %+v", saved)
	}
}

func TestReviseAdaptationProposalLetsModelChooseVolumeExpansionForNaturalInstruction(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(3, 9, 14, 3, 4)},
		{text: plannerRevisionProposalJSON(9, 12, 3, 4)},
		{text: plannerRevisionProposalJSON(13, 14, 3, 4)},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "加上更多日常纯爱的言情章节，一直写到男女主结婚、怀孕、生了个女儿",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposal: %v", err)
	}
	if len(updated.Chapters) != 14 || updated.Volumes[2].TargetTo != 14 {
		t.Fatalf("model-selected expansion was not applied: chapters=%d volumes=%+v", len(updated.Chapters), updated.Volumes)
	}
	if !strings.Contains(llm.got[0][len(llm.got[0])-1].TextContent(), "expansion_decision") {
		t.Fatalf("volume skeleton prompt should ask the model for an expansion decision")
	}
}

func TestReviseAdaptationProposalRejectsModelExpansionDecisionWithoutNewChapters(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	unchangedSkeleton := plannerVolumeRevisionSkeletonJSONWithDecision(3, 9, 12, 3, 4, "expand")
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: unchangedSkeleton},
		{text: unchangedSkeleton},
		{text: unchangedSkeleton},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "add two new chapters to supplement the ending",
	})
	if err == nil {
		t.Fatalf("ReviseAdaptationProposal succeeded without required expansion: %+v", updated)
	}
	if !strings.Contains(err.Error(), "model chose expansion") {
		t.Fatalf("error should explain missing expansion, got: %v", err)
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 12 || saved.Volumes[2].TargetTo != 12 || saved.Volumes[2].Title != "Final volume" {
		t.Fatalf("proposal should remain unchanged after failed expansion: %+v", saved)
	}
}

func TestReviseAdaptationProposalRepairsVolumeDetailMissingWordBudget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(3, 9, 14, 3, 4)},
		{text: plannerRevisionProposalJSONMissingWordBudget(9, 12)},
		{text: plannerRevisionProposalJSON(9, 12, 3, 4)},
		{text: plannerRevisionProposalJSON(13, 14, 3, 4)},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "加上更多恋爱日常章节",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposal: %v", err)
	}
	if llm.calls != 4 {
		t.Fatalf("planner calls=%d, want skeleton + first detail + first detail repair + second detail", llm.calls)
	}
	if len(updated.Chapters) != 14 || updated.Chapters[8].WordBudget == nil {
		t.Fatalf("repaired proposal should include expanded chapters with word budgets: %+v", updated.Chapters[8])
	}
}

func TestReviseAdaptationProposalAllowsMiddleVolumeExpansionAndShiftsLaterVolumes(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(2, 5, 10, 2, 3)},
		{text: plannerRevisionProposalJSON(5, 8, 2, 3)},
		{text: plannerRevisionProposalJSON(9, 10, 2, 3)},
	}}

	updated, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 2,
		Instruction: "给第二卷新增两章剧情",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationProposal: %v", err)
	}
	if len(updated.Chapters) != 14 {
		t.Fatalf("chapters=%d, want 14", len(updated.Chapters))
	}
	for _, chapter := range []int{5, 8, 9, 10} {
		got := updated.Chapters[chapter-1]
		if !strings.HasPrefix(got.Title, "Revised ") {
			t.Fatalf("chapter %d was not revised/appended: %+v", chapter, got)
		}
	}
	if updated.Chapters[10].Chapter != 11 || !strings.HasPrefix(updated.Chapters[10].Title, "Original 9") {
		t.Fatalf("old chapter 9 should shift to target chapter 11: %+v", updated.Chapters[10])
	}
	if len(updated.Volumes) != 3 {
		t.Fatalf("volumes=%d, want 3: %+v", len(updated.Volumes), updated.Volumes)
	}
	if updated.Volumes[1].TargetFrom != 5 || updated.Volumes[1].TargetTo != 10 {
		t.Fatalf("volume 2 should extend to 5-10: %+v", updated.Volumes[1])
	}
	if updated.Volumes[1].Title != "Revised volume 2" || updated.Volumes[1].Summary != "Replanned volume beats." {
		t.Fatalf("volume 2 metadata was not updated: %+v", updated.Volumes[1])
	}
	if updated.Volumes[2].TargetFrom != 11 || updated.Volumes[2].TargetTo != 14 {
		t.Fatalf("volume 3 should shift to 11-14: %+v", updated.Volumes[2])
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 14 || saved.Volumes[2].TargetFrom != 11 || saved.Volumes[2].TargetTo != 14 {
		t.Fatalf("expanded middle-volume proposal was not saved: %+v", saved)
	}
}

func TestReviseAdaptationProposalRejectsFixedRangeCountChange(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: plannerRevisionProposalJSON(5, 9, 2, 3)}}}

	if _, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		FromChapter: 5,
		ToChapter:   8,
		Instruction: "add an extra middle chapter",
	}); err == nil {
		t.Fatal("ReviseAdaptationProposal should reject fixed-range chapter expansion")
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != 12 || !strings.HasPrefix(saved.Chapters[4].Title, "Original 5") {
		t.Fatalf("failed fixed-range revision should not save changes: %+v", saved)
	}
}

func TestReviseAdaptationProposalRejectsNoChangeRevision(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	saveRevisionTestProposal(t, st)
	original, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerVolumeRevisionSkeletonJSON(3, 9, 12, 3, 4)},
		{text: plannerRevisionNoChangeProposalJSON(t, original.Chapters, 9, 12)},
	}}

	if _, err := ReviseAdaptationProposal(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 3,
		Instruction: "make the final volume more emotional",
	}); err == nil {
		t.Fatal("ReviseAdaptationProposal should reject a no-change revision")
	} else if !strings.Contains(err.Error(), "no proposal changes") {
		t.Fatalf("error=%q, want no-change message", err.Error())
	}
	saved, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if saved == nil || len(saved.Chapters) != len(original.Chapters) || saved.Chapters[8].Title != original.Chapters[8].Title {
		t.Fatalf("saved proposal should remain unchanged: saved=%+v original=%+v", saved, original)
	}
	if len(saved.Planner.Notes) != len(original.Planner.Notes) {
		t.Fatalf("failed no-change revision should not append planner notes: saved=%+v original=%+v", saved.Planner, original.Planner)
	}
}

func TestBuildAdaptationProposalLongFreeReturnsVolumeReviewOnly(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "free long arc",
		"target_chapter_count": 20,
		"mainline_rules": ["keep every source turn anchored"],
		"batches": [
			{"index": 1, "title": "Opening volume", "theme": "orientation", "target_from": 1, "target_to": 8, "source_from": 1, "source_to": 2, "summary": "establish the new premise"},
			{"index": 2, "title": "Pressure volume", "theme": "choice", "target_from": 9, "target_to": 16, "source_from": 2, "source_to": 3, "summary": "expand the central conflict"},
			{"index": 3, "title": "Resolution volume", "theme": "payoff", "target_from": 17, "target_to": 20, "source_from": 3, "source_to": 4, "summary": "resolve the adapted ending"}
		]
	}`}}}

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:              "free long arc",
		Granularity:        domain.AdaptationGranularityFree,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposal: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("planner calls=%d, want skeleton-only volume review", llm.calls)
	}
	if result == nil || result.VolumeReview == nil || result.Proposal != nil {
		t.Fatalf("long free proposal should return volume review only: %+v", result)
	}
	review := result.VolumeReview
	if review.Status != domain.AdaptationPlanStatusVolumeReview {
		t.Fatalf("status=%q, want volume_review", review.Status)
	}
	if len(review.Volumes) != 3 || review.Volumes[2].TargetTo != 20 {
		t.Fatalf("volume review should expose model-planned volumes only: %+v", review.Volumes)
	}
	saved, err := st.Adaptation.LoadVolumeReview()
	if err != nil {
		t.Fatalf("LoadVolumeReview: %v", err)
	}
	if saved == nil || saved.Status != domain.AdaptationPlanStatusVolumeReview || len(saved.Volumes) != 3 {
		t.Fatalf("saved volume review mismatch: %+v", saved)
	}
	if savedProposal, err := st.Adaptation.LoadProposal(); err != nil || savedProposal != nil {
		t.Fatalf("volume review should not save full proposal yet: proposal=%+v err=%v", savedProposal, err)
	}
}

func TestBuildAdaptationProposalVolumeSkeletonSumsSourceMapBatchCounts(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 45)
	for i := range runeCounts {
		runeCounts[i] = 10
	}
	seedPreparedAdaptationSource(t, st, runeCounts)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "arc long rewrite",
			"target_chapter_count": 50,
			"batches": [
				{"index": 1, "title": "Opening expansion", "theme": "growth", "chapter_count": 50, "target_from": 1, "target_to": 50, "source_from": 1, "source_to": 40, "summary": "expand the first source-map batch"}
			]
		}`},
		{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "arc long rewrite",
			"target_chapter_count": 7,
			"batches": [
				{"index": 1, "title": "Final bridge", "theme": "payoff", "chapter_count": 7, "target_from": 1, "target_to": 7, "source_from": 41, "source_to": 45, "summary": "add room for the ending transition"}
			]
		}`},
	}}

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalOptions{
		Brief:       "arc long rewrite",
		Granularity: domain.AdaptationGranularityArc,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalVolumesContext: %v", err)
	}
	if llm.calls != 2 {
		t.Fatalf("planner calls=%d, want one call per source-map batch", llm.calls)
	}
	if result == nil || result.VolumeReview == nil {
		t.Fatalf("expected volume review, got %+v", result)
	}
	if result.VolumeReview.TargetChapterCount != 57 {
		t.Fatalf("target chapters=%d, want summed 57", result.VolumeReview.TargetChapterCount)
	}
	if len(result.VolumeReview.Volumes) != 2 || result.VolumeReview.Volumes[1].TargetFrom != 51 || result.VolumeReview.Volumes[1].TargetTo != 57 {
		t.Fatalf("volume ranges should be globally renumbered after summing source-map batches: %+v", result.VolumeReview.Volumes)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesRejectsOverlappingSourceRanges(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 6}
	batches := []plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 1, 4, 1, 4),
		testSourceMapSkeletonBatch(2, 3, 6, 5, 8),
	}

	normalized, err := normalizePlannerSourceMapSkeletonBatches(batches, entry)
	if err != nil {
		t.Fatalf("normalizePlannerSourceMapSkeletonBatches should repair overlapping source ranges: %v", err)
	}
	if normalized[0].SourceFrom != 1 || normalized[0].SourceTo != 4 ||
		normalized[1].SourceFrom != 5 || normalized[1].SourceTo != 6 {
		t.Fatalf("overlap should become a strict inclusive partition: %+v", normalized)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesAllowsBoundarySourceHandoff(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 6}
	batches := []plannerSkeletonBatch{
		testSourceMapSkeletonBatch(1, 1, 3, 1, 3),
		testSourceMapSkeletonBatch(2, 3, 6, 4, 7),
	}

	normalized, err := normalizePlannerSourceMapSkeletonBatches(batches, entry)
	if err != nil {
		t.Fatalf("normalizePlannerSourceMapSkeletonBatches should allow one-chapter boundary handoff: %v", err)
	}
	if len(normalized) != 2 {
		t.Fatalf("normalized batches=%d, want 2", len(normalized))
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesUsesChapterCountNotModelRange(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 3}
	batch := testSourceMapSkeletonBatch(1, 1, 3, 1, 4)
	batch.TargetChapterCount = 5

	normalized, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{batch}, entry)
	if err != nil {
		t.Fatalf("normalizePlannerSourceMapSkeletonBatches should accept chapter_count without trusting model target range: %v", err)
	}
	if len(normalized) != 1 {
		t.Fatalf("normalized batches=%d, want 1", len(normalized))
	}
	if normalized[0].TargetChapterCount != 5 {
		t.Fatalf("TargetChapterCount=%d, want model budget 5", normalized[0].TargetChapterCount)
	}
	if normalized[0].TargetFrom != 0 || normalized[0].TargetTo != 0 {
		t.Fatalf("model target range should be ignored before host numbering: %+v", normalized[0])
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesRejectsRunawayExpansion(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 10, SourceTo: 16}
	batch := testSourceMapSkeletonBatch(1, 10, 16, 1, 50)

	_, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{batch}, entry)
	if err == nil {
		t.Fatal("normalizePlannerSourceMapSkeletonBatches should request review for runaway target expansion")
	}
	if !strings.Contains(err.Error(), "above expected review ceiling") {
		t.Fatalf("error=%v, want review ceiling", err)
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesHighCeilingUsesSourceRunes(t *testing.T) {
	for _, count := range []int{10, 14} {
		t.Run(fmt.Sprintf("count_%d", count), func(t *testing.T) {
			entry := plannerSourceMapEntry{Index: 1, SourceFrom: 10, SourceTo: 10, SourceRunes: 57500}
			batches, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{
				testSourceMapSkeletonBatch(1, 10, 10, 1, count),
			}, entry)
			if err != nil {
				t.Fatalf("single high-rune source chapter should not be rejected by fixed ceiling 6: %v", err)
			}
			if len(batches) != 1 || batches[0].TargetChapterCount != count {
				t.Fatalf("batches=%+v, want count %d", batches, count)
			}
		})
	}
}

func TestNormalizePlannerSourceMapSkeletonBatchesRejectsSingleSourceRunawayWithoutRunesOrRationale(t *testing.T) {
	entry := plannerSourceMapEntry{Index: 1, SourceFrom: 10, SourceTo: 10}
	batch := testSourceMapSkeletonBatch(1, 10, 10, 1, 14)

	_, err := normalizePlannerSourceMapSkeletonBatches([]plannerSkeletonBatch{batch}, entry)
	if err == nil {
		t.Fatal("normalizePlannerSourceMapSkeletonBatches should reject unsupported single-source runaway expansion")
	}
	if !strings.Contains(err.Error(), "above expected review ceiling") {
		t.Fatalf("error=%v, want review ceiling", err)
	}
}

func TestPlannerChapterBudgetRepairInstructionsMentionSourceRuneMinimum(t *testing.T) {
	instructions := strings.Join(plannerChapterBudgetRepairInstructions(&plannerChapterBudgetQualityError{
		SourceFrom:  129,
		SourceTo:    133,
		MinCount:    4,
		SourceRunes: 15812,
		Direction:   "low",
	}), "\n")

	if !strings.Contains(instructions, "should be at least 4") ||
		!strings.Contains(instructions, `budget_decision="compress_or_merge"`) ||
		!strings.Contains(instructions, "source_runes=15812") ||
		!strings.Contains(instructions, "not a final word_budget.max_runes override") {
		t.Fatalf("repair instructions should expose source-rune minimum, got %q", instructions)
	}
}

func TestRepairPlannerSkeletonTextClarifiesSourceMapPartition(t *testing.T) {
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{"batches":[]}`}}}
	_, err := repairPlannerSkeletonText(
		context.Background(),
		llm,
		"system",
		"original source-map request",
		`{"batches":[]}`,
		fmt.Errorf("source-map range 162-188 overlaps at source chapter 179"),
		domain.AdaptationGranularityArc,
		false,
		nil,
		7,
		24,
		1,
	)
	if err != nil {
		t.Fatalf("repairPlannerSkeletonText: %v", err)
	}
	if len(llm.got) != 1 {
		t.Fatalf("planner calls=%d, want 1", len(llm.got))
	}
	prompt := llm.got[0][1].TextContent()
	if !strings.Contains(prompt, "source_from and source_to are model-owned source coverage") ||
		!strings.Contains(prompt, "host will normalize minor gaps, overlaps") ||
		!strings.Contains(prompt, "Do not spend repeated attempts on exact source-range boundary arithmetic") {
		t.Fatalf("repair prompt should clarify source-map partition ownership, got %s", prompt)
	}
}

func TestRepairPlannerSkeletonTextKeepsFreeSourceMapRangeFixed(t *testing.T) {
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{"batches":[]}`}}}
	_, err := repairPlannerSkeletonText(
		context.Background(),
		llm,
		"system",
		"original free source-map request",
		`{"batches":[]}`,
		fmt.Errorf("source-map range 140-140 does not advance past source chapter 140"),
		domain.AdaptationGranularityFree,
		false,
		nil,
		29,
		29,
		1,
	)
	if err != nil {
		t.Fatalf("repairPlannerSkeletonText: %v", err)
	}
	if len(llm.got) != 1 {
		t.Fatalf("planner calls=%d, want 1", len(llm.got))
	}
	prompt := llm.got[0][1].TextContent()
	if !strings.Contains(prompt, "sharing or overlapping source ranges across returned batches is valid") ||
		!strings.Contains(prompt, "Do not repair free/full_rewrite by forcing the source range into a strict partition") {
		t.Fatalf("free repair prompt should allow shared source-map ranges, got %s", prompt)
	}
	if strings.Contains(prompt, "strict sorted partition") {
		t.Fatalf("free repair prompt should not request arc-style strict partition, got %s", prompt)
	}
}

func TestRetryPlannerSkeletonChapterBudgetUsesGranularitySourceRangeRules(t *testing.T) {
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{"batches":[]}`}}}
	_, err := retryPlannerSkeletonChapterBudget(
		context.Background(),
		llm,
		"system",
		"original free source-map request",
		`{"batches":[]}`,
		&plannerChapterBudgetQualityError{SourceFrom: 140, SourceTo: 140, Count: 14, MaxCount: 6, Direction: "high"},
		domain.AdaptationGranularityFree,
		nil,
		29,
		29,
		1,
	)
	if err != nil {
		t.Fatalf("retryPlannerSkeletonChapterBudget: %v", err)
	}
	if len(llm.got) != 1 {
		t.Fatalf("planner calls=%d, want 1", len(llm.got))
	}
	prompt := llm.got[0][1].TextContent()
	if !strings.Contains(prompt, "sharing or overlapping source ranges across returned batches is valid") ||
		!strings.Contains(prompt, "Do not repair free/full_rewrite by forcing the source range into a strict partition") ||
		!strings.Contains(prompt, "budget_decision") {
		t.Fatalf("free budget retry prompt should keep free source-map range rules and budget fields, got %s", prompt)
	}
	if strings.Contains(prompt, "strict sorted partition") {
		t.Fatalf("free budget retry prompt should not request arc-style strict partition, got %s", prompt)
	}
}

func TestRepairPlannerBatchTextClarifiesSourceRangeBudgetRepair(t *testing.T) {
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{"chapters":[]}`}}}
	_, err := repairPlannerBatchText(
		withAdaptationPromptModeIfMissing(context.Background(), domain.AdaptationGranularityArc),
		llm,
		"system",
		"original detail request",
		`{"chapters":[]}`,
		plannerSkeletonBatch{Index: 22, TargetFrom: 73, TargetTo: 76, SourceFrom: 21, SourceTo: 23},
		&plannerProposalBudgetSplitError{FirstChapter: 76, SourceFrom: 22, SourceTo: 23, SourceRunes: 53251, MinChapters: 11},
		false,
		nil,
		22,
		33,
		"detail batch",
		1,
	)
	if err != nil {
		t.Fatalf("repairPlannerBatchText: %v", err)
	}
	if len(llm.got) != 1 {
		t.Fatalf("planner calls=%d, want 1", len(llm.got))
	}
	prompt := llm.got[0][1].TextContent()
	if !strings.Contains(prompt, "shared source_range budget coverage failure") ||
		!strings.Contains(prompt, "Do not fix a source_range budget error by lowering source_runes") ||
		!strings.Contains(prompt, "may share one broad source_range") {
		t.Fatalf("repair prompt should describe shared source_range budget repair, got %s", prompt)
	}
}

func TestRepairPlannerBatchTextRegeneratesPollutedEventBatchFromWhitelist(t *testing.T) {
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{"chapters":[]}`}}}
	batch := plannerSkeletonBatch{
		Index:            44,
		TargetFrom:       165,
		TargetTo:         168,
		MainlineEventIDs: []string{"EVT-165-A", "EVT-168-B"},
		AllowedEventIDs:  []string{"EVT-165-A", "EVT-168-B", "EVT-166-SUPPORT"},
	}
	previous := `{"chapters":[{"chapter":165,"event_ids":["EVT-228-FUTURE"]}]}`
	previousErr := fmt.Errorf("arc mainline event EVT-228-FUTURE is not assigned to detail batch 165-168; remove it from event_ids")

	_, err := repairPlannerBatchText(
		withAdaptationPromptModeIfMissing(context.Background(), domain.AdaptationGranularityArc), llm, "system", "original detail request", previous,
		batch, previousErr, true, nil, 44, 370, "detail batch", 1,
	)
	if err != nil {
		t.Fatalf("repairPlannerBatchText: %v", err)
	}
	prompt := llm.got[0][1].TextContent()
	for _, want := range []string{"EVT-165-A", "EVT-168-B", "EVT-166-SUPPORT", "Never invent a source ID", "Do not merely delete the foreign ID"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("event repair prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, previous) || strings.Contains(prompt, "previous_output") {
		t.Fatalf("polluted previous output should be removed from clean regeneration: %s", prompt)
	}
}

func TestShouldRegeneratePlannerBatchFromOriginalOnSemanticPollution(t *testing.T) {
	cases := []struct {
		name    string
		attempt int
		err     error
		want    bool
	}{
		{name: "foreign event on first repair", err: fmt.Errorf("event X is not assigned to detail batch 1-4"), want: true},
		{name: "duplicate outline on first repair", err: fmt.Errorf("chapter 4 duplicates outline beats from chapter 3"), want: true},
		{name: "ordinary parse error gets one feedback repair", err: fmt.Errorf("invalid JSON")},
		{name: "second repair always resets", attempt: 1, err: fmt.Errorf("invalid JSON"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRegeneratePlannerBatchFromOriginal(tc.attempt, tc.err); got != tc.want {
				t.Fatalf("shouldRegeneratePlannerBatchFromOriginal()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestCollectPlannerSourceMapSkeletonBatchesSkipsQualityRetryWithExplicitRationale(t *testing.T) {
	text := `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc long rewrite",
		"target_chapter_count": 4,
		"batches": [
			{"index": 1, "title": "Compressed opening", "theme": "compression", "chapter_count": 4, "source_from": 1, "source_to": 4, "budget_decision": "compress_or_merge", "budget_reason": "merge side investigations and compress travel beats", "summary": "focus on the main turn"}
		]
	}`
	llm := &scriptedAdaptLLM{}

	batches, err := collectPlannerSourceMapSkeletonBatches(
		context.Background(),
		llm,
		"system",
		"original source-map request",
		text,
		plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 4, SourceRunes: 29588},
		domain.AdaptationGranularityArc,
		nil,
		nil,
		1,
		1,
		2,
		1,
		1,
	)
	if err != nil {
		t.Fatalf("collectPlannerSourceMapSkeletonBatches: %v", err)
	}
	if llm.calls != 0 {
		t.Fatalf("explicit rationale should avoid quality retry LLM calls, got %d", llm.calls)
	}
	if len(batches) != 1 || !plannerBudgetDeviationAccepted(batches[0]) {
		t.Fatalf("accepted rationale should mark returned batch, got %+v", batches)
	}
}

func TestCollectPlannerSourceMapSkeletonBatchesAppliesCapacityFloorWithoutRetry(t *testing.T) {
	text := `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc long rewrite",
		"target_chapter_count": 4,
		"batches": [
			{"index": 1, "title": "Thin opening", "theme": "setup", "chapter_count": 4, "source_from": 1, "source_to": 4, "summary": "cover the opening source range"}
		]
	}`
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: text}}}

	batches, err := collectPlannerSourceMapSkeletonBatches(
		context.Background(),
		llm,
		"system",
		"original source-map request",
		text,
		plannerSourceMapEntry{Index: 1, SourceFrom: 1, SourceTo: 4, SourceRunes: 29588},
		domain.AdaptationGranularityArc,
		nil,
		nil,
		1,
		1,
		1,
		1,
		1,
	)
	if err != nil {
		t.Fatalf("capacity floor should converge without a model retry: %v", err)
	}
	if len(batches) != 1 || batches[0].TargetChapterCount != 6 {
		t.Fatalf("batches=%+v, want host-raised six target chapters", batches)
	}
	if llm.calls != 0 {
		t.Fatalf("planner calls=%d, want no retry for deterministic capacity correction", llm.calls)
	}
}

func TestBuildAdaptationProposalVolumeSkeletonBudgetQualityRetryDoesNotConsumeStructureRepair(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 45)
	for i := range runeCounts {
		runeCounts[i] = 10
	}
	seedPreparedAdaptationSource(t, st, runeCounts)
	lowBudget := `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc long rewrite",
		"target_chapter_count": 1,
		"batches": [
			{"index": 1, "title": "Thin opening", "theme": "setup", "chapter_count": 1, "source_from": 1, "source_to": 40, "summary": "cover the opening source-map range"}
		]
	}`
	acceptedLowBudget := `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc long rewrite",
		"target_chapter_count": 1,
		"batches": [
			{"index": 1, "title": "Compressed opening", "theme": "compression", "chapter_count": 1, "source_from": 1, "source_to": 40, "budget_decision": "compress_or_merge", "budget_reason": "compress the opening side material into one source-map range", "summary": "focus on the main opening turn"}
		]
	}`
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: lowBudget},
		{text: lowBudget},
		{text: lowBudget},
		{text: acceptedLowBudget},
		{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "arc long rewrite",
			"target_chapter_count": 7,
			"batches": [
				{"index": 1, "title": "Final bridge", "theme": "payoff", "chapter_count": 7, "source_from": 41, "source_to": 45, "summary": "add room for the ending transition"}
			]
		}`},
	}}
	progress := make([]Event, 0)

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{
		Store:                      st,
		LLM:                        llm,
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 3,
		BudgetQualityMaxAttempts:   3,
	}, ProposalOptions{
		Brief:        "arc long rewrite",
		Granularity:  domain.AdaptationGranularityArc,
		EmitProgress: captureAdaptProgress(&progress),
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalVolumesContext: %v", err)
	}
	if llm.calls != 5 {
		t.Fatalf("planner calls=%d, want initial + 3 configured quality retries + next source-map batch", llm.calls)
	}
	if result == nil || result.VolumeReview == nil || result.VolumeReview.TargetChapterCount != 8 {
		t.Fatalf("volume review mismatch: %+v", result)
	}
	if !hasAdaptProgress(progress, "质量重试第 1/3") || !hasAdaptProgress(progress, "质量重试第 3/3") {
		t.Fatalf("progress should expose quality retry attempts, got %+v", progress)
	}
	if len(llm.got) < 2 ||
		!strings.Contains(llm.got[1][1].TextContent(), "should be at least 4") ||
		!strings.Contains(llm.got[1][1].TextContent(), "compress_or_merge") {
		t.Fatalf("quality retry prompt should expose the computed review target and compression exception, got %+v", llm.got)
	}
	if hasAdaptProgress(progress, "结构无效") {
		t.Fatalf("budget quality retry should not consume structure repair attempts, got %+v", progress)
	}
	if !hasAdaptProgress(progress, "连续偏离预期") {
		t.Logf("accepted budget deviation progress was not emitted after direct rationale normalization: %+v", progress)
	}
}

func TestBuildAdaptationProposalVolumeSkeletonAllowsHighBudgetAfterQualityRetries(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 45)
	for i := range runeCounts {
		runeCounts[i] = 10
	}
	seedPreparedAdaptationSource(t, st, runeCounts)
	highBudget := `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "expand and split long chapters",
		"target_chapter_count": 300,
		"batches": [
			{"index": 1, "title": "Large opening", "theme": "scope", "chapter_count": 300, "source_from": 1, "source_to": 40, "summary": "cover the opening source-map range at a large target scale"}
		]
	}`
	acceptedHighBudget := `{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "expand and split long chapters",
		"target_chapter_count": 300,
		"batches": [
			{"index": 1, "title": "Expanded opening", "theme": "expansion", "chapter_count": 300, "source_from": 1, "source_to": 40, "budget_decision": "expand_or_split", "budget_reason": "split long source chapters and add relationship transitions", "summary": "divide the long source-map range into added relationship transitions"}
		]
	}`
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: highBudget},
		{text: highBudget},
		{text: acceptedHighBudget},
		{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "expand and split long chapters",
			"target_chapter_count": 7,
			"batches": [
				{"index": 1, "title": "Final bridge", "theme": "payoff", "chapter_count": 7, "source_from": 41, "source_to": 45, "summary": "add room for the ending transition"}
			]
		}`},
	}}
	progress := make([]Event, 0)

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{
		Store:                      st,
		LLM:                        llm,
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 2,
	}, ProposalOptions{
		Brief:        "expand and split long chapters",
		Granularity:  domain.AdaptationGranularityArc,
		EmitProgress: captureAdaptProgress(&progress),
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalVolumesContext: %v", err)
	}
	if result == nil || result.VolumeReview == nil || result.VolumeReview.TargetChapterCount != 307 {
		t.Fatalf("volume review mismatch: %+v", result)
	}
	if llm.calls != 4 {
		t.Fatalf("planner calls=%d, want initial + 2 quality retries + next source-map batch", llm.calls)
	}
	if !hasAdaptProgress(progress, "质量重试第 1/2") || !hasAdaptProgress(progress, "连续偏离预期") {
		t.Logf("accepted budget deviation progress was not emitted after direct rationale normalization: %+v", progress)
	}
}

func TestUpsertPlannerProposalRuntimeSkeletonBatchesRefreshesTargetChapterCount(t *testing.T) {
	runtime := &domain.AdaptationProposalRuntime{
		TargetChapterCount: 1,
		SkeletonBatches: []domain.AdaptationProposalRuntimeSkeletonBatch{
			{Index: 1, TargetFrom: 1, TargetTo: 4, TargetChapterCount: 4, SourceFrom: 1, SourceTo: 2},
		},
	}
	entry := plannerSourceMapEntry{Index: 2, SourceFrom: 3, SourceTo: 4}
	batches := []plannerSkeletonBatch{testSourceMapSkeletonBatch(2, 3, 4, 5, 8)}
	batches[0].BudgetDecision = "compress_or_merge"
	batches[0].BudgetReason = "compress side beats"

	upsertPlannerProposalRuntimeSkeletonBatches(runtime, entry, batches)

	if runtime.TargetChapterCount != 8 {
		t.Fatalf("TargetChapterCount=%d, want 8", runtime.TargetChapterCount)
	}
	if len(runtime.SkeletonBatches) != 2 ||
		runtime.SkeletonBatches[1].BudgetDecision != "compress_or_merge" ||
		runtime.SkeletonBatches[1].BudgetReason != "compress side beats" {
		t.Fatalf("runtime skeleton budget fields not preserved: %+v", runtime.SkeletonBatches)
	}
}

func TestBuildAdaptationProposalVolumesNormalizesCachedSourceMapCapacity(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 47)
	for i := range runeCounts {
		runeCounts[i] = 1
	}
	copy(runeCounts[:10], []int{5000, 5000, 5000, 6000, 6000, 6000, 4000, 5000, 5000, 5000})
	seedPreparedAdaptationSource(t, st, runeCounts)
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil {
		t.Fatalf("LoadSourceManifest: %v", err)
	}
	if manifest == nil {
		t.Fatal("source manifest missing")
	}
	specs := store.AdaptationDossierBatchSpecs(*manifest, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit)
	if len(specs) < 2 {
		t.Fatalf("source should split into at least two source-map ranges, got %+v", specs)
	}
	firstSpec := specs[0]
	secondSpec := specs[1]
	if firstSpec.SourceFrom != 1 || firstSpec.SourceTo < 7 {
		t.Fatalf("first source-map range should cover the skewed opening chapters, got %+v", firstSpec)
	}
	firstTailCount := firstSpec.SourceTo - 6
	secondCount := secondSpec.SourceTo - secondSpec.SourceFrom + 1
	oldFirstTargetCount := 3 + 3 + firstTailCount
	newFirstTargetCount := 3 + 4 + firstTailCount
	oldTargetCount := oldFirstTargetCount + secondCount
	newTargetCount := newFirstTargetCount + secondCount
	cachedFirstTailTargetTo := 6 + firstTailCount
	cachedTailTargetFrom := cachedFirstTailTargetTo + 1
	cachedTailTargetTo := cachedTailTargetFrom + secondCount - 1
	shiftedTailTargetFrom := newFirstTargetCount + 1
	shiftedTailTargetTo := shiftedTailTargetFrom + secondCount - 1
	runtimeWordTolerance := normalizeProposalWordTolerance(domain.AdaptationGranularityArc, DefaultWordTolerance)
	if err := st.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:            adaptationProposalRuntimeVersion,
		Brief:              "arc cached skeleton resume",
		SourcePath:         "source.txt",
		SourceChapterCount: 47,
		Granularity:        domain.AdaptationGranularityArc,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		WordTolerance:      runtimeWordTolerance,
		TargetChapterCount: oldTargetCount,
		SkeletonBatches: []domain.AdaptationProposalRuntimeSkeletonBatch{
			{Index: 1, Title: "Cached opening", Theme: "setup", Summary: "short opening", TargetFrom: 1, TargetTo: 3, TargetChapterCount: 3, SourceFrom: 1, SourceTo: 3},
			{Index: 2, Title: "Bad cached turn", Theme: "pressure", Summary: "undersplits the exact source subrange", TargetFrom: 4, TargetTo: 6, TargetChapterCount: 3, SourceFrom: 4, SourceTo: 6},
			{Index: 3, Title: "Cached bridge", Theme: "bridge", Summary: "rest of first source-map range", TargetFrom: 7, TargetTo: cachedFirstTailTargetTo, TargetChapterCount: firstTailCount, SourceFrom: 7, SourceTo: firstSpec.SourceTo},
			{Index: 4, Title: "Cached tail", Theme: "tail", Summary: "second source-map range should be reusable", TargetFrom: cachedTailTargetFrom, TargetTo: cachedTailTargetTo, TargetChapterCount: secondCount, SourceFrom: secondSpec.SourceFrom, SourceTo: secondSpec.SourceTo},
		},
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}
	runtimeBefore, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		t.Fatalf("LoadProposalRuntime before run: %v", err)
	}
	sourceRunesByChapter := sourceRunesByChapter(manifest)
	secondEntry := plannerSourceMapEntry{
		Index:       secondSpec.Index,
		SourceFrom:  secondSpec.SourceFrom,
		SourceTo:    secondSpec.SourceTo,
		SourceRunes: sourceRunesForRange(sourceRunesByChapter, secondSpec.SourceFrom, secondSpec.SourceTo),
	}
	reusedBefore, ok, reuseErr := plannerRuntimeSkeletonBatchesForSource(runtimeBefore, secondEntry, domain.AdaptationGranularityArc, sourceRunesByChapter)
	if reuseErr != nil || !ok || len(reusedBefore) != 1 {
		t.Fatalf("seeded tail cache should be reusable before replanning: reused=%+v ok=%t err=%v", reusedBefore, ok, reuseErr)
	}
	progress := make([]Event, 0)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: fmt.Sprintf(`{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc cached skeleton resume",
		"target_chapter_count": %d,
		"batches": [
			{"index": 1, "title": "Replanned opening", "theme": "setup", "chapter_count": 3, "source_from": 1, "source_to": 3, "summary": "keep the short opening"},
			{"index": 2, "title": "Replanned exact turn", "theme": "pressure", "chapter_count": 4, "source_from": 4, "source_to": 6, "summary": "split the exact long source subrange"},
			{"index": 3, "title": "Replanned bridge", "theme": "bridge", "chapter_count": %d, "source_from": 7, "source_to": %d, "summary": "reuse the remaining first range structure"}
		]
	}`, newFirstTargetCount, firstTailCount, firstSpec.SourceTo)}}}

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{
		Store:                    st,
		LLM:                      llm,
		ModelCallMaxAttempts:     1,
		BudgetQualityMaxAttempts: 1,
	}, ProposalOptions{
		Brief:         "arc cached skeleton resume",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		WordTolerance: DefaultWordTolerance,
		EmitProgress:  captureAdaptProgress(&progress),
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalVolumesContext: %v; progress=%+v", err, progress)
	}
	if llm.calls != 0 {
		t.Fatalf("planner calls=%d, want cached capacity normalized without replanning", llm.calls)
	}
	if result == nil || result.VolumeReview == nil || result.VolumeReview.TargetChapterCount != newTargetCount {
		t.Fatalf("volume review mismatch: %+v", result)
	}
	normalized := result.VolumeReview.Volumes[1]
	if normalized.SourceFrom != 4 || normalized.SourceTo != 6 || normalized.TargetFrom != 4 || normalized.TargetTo != 7 {
		t.Fatalf("normalized volume=%+v, want source 4-6 raised to target 4-7", normalized)
	}
	tail := result.VolumeReview.Volumes[len(result.VolumeReview.Volumes)-1]
	if tail.SourceFrom != secondSpec.SourceFrom || tail.SourceTo != secondSpec.SourceTo || tail.TargetFrom != shiftedTailTargetFrom || tail.TargetTo != shiftedTailTargetTo {
		t.Fatalf("reused tail volume=%+v, want source %d-%d shifted to target %d-%d", tail, secondSpec.SourceFrom, secondSpec.SourceTo, shiftedTailTargetFrom, shiftedTailTargetTo)
	}
	if !hasAdaptProgress(progress, "复用骨架规划第 1/2 批") {
		t.Fatalf("progress should report normalized cached skeleton reuse, got %+v", progress)
	}
	runtime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		t.Fatalf("LoadProposalRuntime: %v", err)
	}
	if runtime == nil || runtime.Skeleton == nil || len(runtime.SkeletonBatches) != 0 || runtime.Skeleton.TargetChapterCount != newTargetCount {
		t.Fatalf("saved runtime should hold normalized final skeleton and clear partial batches: %+v", runtime)
	}
}

func TestPlannerProposalRuntimeKeepsPartialSourceMapSkeletonForImplicitScale(t *testing.T) {
	runtime := &domain.AdaptationProposalRuntime{
		Version:            adaptationProposalRuntimeVersion,
		Brief:              "arc long rewrite",
		Granularity:        domain.AdaptationGranularityArc,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 79,
		SourceChapterCount: 490,
		SourcePath:         "source.txt",
		SkeletonBatches: []domain.AdaptationProposalRuntimeSkeletonBatch{
			{Index: 1, TargetFrom: 1, TargetTo: 7, TargetChapterCount: 7, SourceFrom: 1, SourceTo: 10},
		},
	}
	manifest := &domain.AdaptationSourceManifest{SourcePath: "source.txt", ChapterCount: 490}
	if !plannerProposalRuntimeMatches(runtime, ProposalOptions{
		Brief:         "arc long rewrite",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
	}, manifest, 490) {
		t.Fatal("implicit long-form scale hint should not discard a partial source-map skeleton checkpoint")
	}
	runtime.Version = adaptationProposalRuntimeLegacyVersion
	if !plannerProposalRuntimeMatches(runtime, ProposalOptions{
		Brief: "arc long rewrite", Granularity: domain.AdaptationGranularityArc, RewritePolicy: domain.AdaptationRewriteFullRewrite,
	}, manifest, 490) {
		t.Fatal("legacy runtime must be retained and audited instead of discarded")
	}
	runtime.Version = adaptationProposalRuntimeVersion
	runtime.Brief = "changed brief"
	if plannerProposalRuntimeMatches(runtime, ProposalOptions{
		Brief:         "arc long rewrite",
		Granularity:   domain.AdaptationGranularityArc,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
	}, manifest, 490) {
		t.Fatal("changed proposal brief should still discard the partial checkpoint")
	}
}

func TestRefuseProposalVolumeStageRegressionAfterDetailGenerationStarts(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageDetailsGenerating, -1); err != nil {
		t.Fatalf("SetPlanningWorkflowStage: %v", err)
	}
	if err := refuseProposalVolumeStageRegression(Deps{Store: st}); err == nil || !strings.Contains(err.Error(), "resume the detail stage") {
		t.Fatalf("detail-stage guard error=%v", err)
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, -1); err != nil {
		t.Fatalf("reset workflow stage: %v", err)
	}
	if err := st.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:          adaptationProposalRuntimeVersion,
		CompletedBatches: []domain.AdaptationProposalRuntimeBatch{{TargetFrom: 1, TargetTo: 4}},
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}
	if err := refuseProposalVolumeStageRegression(Deps{Store: st}); err == nil || !strings.Contains(err.Error(), "resume the detail stage") {
		t.Fatalf("completed-batch guard error=%v", err)
	}
}

func TestBuildAdaptationProposalVolumeSkeletonUsesConfiguredRepairBudget(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	runeCounts := make([]int, 45)
	for i := range runeCounts {
		runeCounts[i] = 10
	}
	seedPreparedAdaptationSource(t, st, runeCounts)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{"overall_arc":"not a usable source-map skeleton"}`},
		{text: `{"batches":"still not an array"}`},
		{text: `{"batches":[{"index":1,"title":"","theme":"","target_from":1,"target_to":50,"source_from":1,"source_to":40,"summary":""}]}`},
		{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "arc long rewrite",
			"target_chapter_count": 50,
			"batches": [
				{"index": 1, "title": "Opening expansion", "theme": "growth", "chapter_count": 50, "target_from": 1, "target_to": 50, "source_from": 1, "source_to": 40, "summary": "expand the first source-map batch"}
			]
		}`},
		{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "arc long rewrite",
			"target_chapter_count": 7,
			"batches": [
				{"index": 1, "title": "Final bridge", "theme": "payoff", "chapter_count": 7, "target_from": 1, "target_to": 7, "source_from": 41, "source_to": 45, "summary": "add room for the ending transition"}
			]
		}`},
	}}
	progress := make([]string, 0)

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{
		Store:                      st,
		LLM:                        llm,
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 3,
	}, ProposalOptions{
		Brief:       "arc long rewrite",
		Granularity: domain.AdaptationGranularityArc,
		EmitProgress: func(_ Stage, _ int, _ int, msg string, _ error) {
			progress = append(progress, msg)
		},
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalVolumesContext: %v", err)
	}
	if llm.calls != 5 {
		t.Fatalf("planner calls=%d, want initial + 3 repairs + second source-map batch", llm.calls)
	}
	secondRepairPrompt := llm.got[2][1].TextContent()
	if !strings.Contains(secondRepairPrompt, `"regenerate_from_original": true`) {
		t.Fatalf("second structural repair should restart from the original batch request: %s", secondRepairPrompt)
	}
	if strings.Contains(secondRepairPrompt, "still not an array") || strings.Contains(secondRepairPrompt, "previous_output") {
		t.Fatalf("second structural repair should not inherit the failed first repair: %s", secondRepairPrompt)
	}
	if result == nil || result.VolumeReview == nil || result.VolumeReview.TargetChapterCount != 57 {
		t.Fatalf("volume review mismatch: %+v", result)
	}
	foundThirdRepair := false
	for _, msg := range progress {
		if strings.Contains(msg, "3/3") {
			foundThirdRepair = true
			break
		}
	}
	if !foundThirdRepair {
		t.Fatalf("progress should show configured repair budget, got %q", progress)
	}
}

func TestBuildAdaptationProposalVolumeSkeletonDoesNotWrapSourceMapBatchObject(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: `{
			"index": 1,
			"title": "Single object",
			"theme": "would have been wrapped before",
			"target_from": 1,
			"target_to": 8,
			"source_from": 1,
			"source_to": 4,
			"summary": "this is not a top-level batches array"
		}`},
		{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "arc rewrite",
			"target_chapter_count": 20,
			"batches": [
				{"index": 1, "title": "Valid arc", "theme": "focused", "chapter_count": 20, "target_from": 1, "target_to": 20, "source_from": 1, "source_to": 4, "summary": "model repaired into the requested skeleton envelope"}
			]
		}`},
	}}

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{
		Store:                      st,
		LLM:                        llm,
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 2,
	}, ProposalOptions{
		Brief:              "arc rewrite",
		Granularity:        domain.AdaptationGranularityArc,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalVolumesContext: %v", err)
	}
	if llm.calls != 2 {
		t.Fatalf("planner calls=%d, want initial source-map skeleton + repair", llm.calls)
	}
	repairPrompt := llm.got[1][1].TextContent()
	if !strings.Contains(repairPrompt, "standalone batch object") {
		t.Fatalf("repair prompt should reject locally wrapped batch objects: %s", repairPrompt)
	}
	if result == nil || result.VolumeReview == nil || len(result.VolumeReview.Volumes) != 1 {
		t.Fatalf("volume review mismatch: %+v", result)
	}
}

func TestBuildAdaptationProposalVolumeSkeletonRegeneratesAfterAmbiguousOutput(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	first := `{"batches":[{"index":1,"title":"Candidate one","theme":"ambiguous","chapter_count":20,"source_from":1,"source_to":4,"summary":"candidate_marker_one"}]}`
	second := `{"batches":[{"index":1,"title":"Candidate two","theme":"ambiguous","chapter_count":20,"source_from":1,"source_to":4,"summary":"candidate_marker_two"}]}`
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: first + "\n" + second},
		{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "arc rewrite",
			"target_chapter_count": 20,
			"mainline_rules": [],
			"relationship_goals": [],
			"batches": [
				{"index": 1, "title": "Regenerated arc", "theme": "focused", "chapter_count": 20, "source_from": 1, "source_to": 4, "summary": "one unambiguous skeleton"}
			]
		}`},
	}}

	result, err := BuildAdaptationProposalVolumesContext(context.Background(), Deps{
		Store:                      st,
		LLM:                        llm,
		ModelCallMaxAttempts:       1,
		StructureRepairMaxAttempts: 2,
	}, ProposalOptions{
		Brief:              "arc rewrite",
		Granularity:        domain.AdaptationGranularityArc,
		TargetChapterCount: 20,
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalVolumesContext: %v", err)
	}
	if llm.calls != 2 {
		t.Fatalf("planner calls=%d, want initial source-map skeleton + clean regeneration", llm.calls)
	}
	repairPrompt := llm.got[1][1].TextContent()
	if !strings.Contains(repairPrompt, `"regenerate_from_original": true`) {
		t.Fatalf("repair prompt should request clean regeneration: %s", repairPrompt)
	}
	if strings.Contains(repairPrompt, "candidate_marker") || strings.Contains(repairPrompt, "previous_output") {
		t.Fatalf("repair prompt should not feed ambiguous candidates back to the model: %s", repairPrompt)
	}
	if result == nil || result.VolumeReview == nil || len(result.VolumeReview.Volumes) != 1 {
		t.Fatalf("volume review mismatch: %+v", result)
	}
}

func TestBuildAdaptationProposalChapterAndShortArcStayFullProposal(t *testing.T) {
	t.Run("chapter", func(t *testing.T) {
		st := store.NewStore(t.TempDir())
		if err := st.Init(); err != nil {
			t.Fatalf("Init: %v", err)
		}
		seedPreparedAdaptationSource(t, st, []int{10, 20})

		proposal, err := BuildAdaptationProposal(Deps{Store: st}, ProposalOptions{
			Brief:       "chapter rewrite",
			Granularity: domain.AdaptationGranularityChapter,
		})
		if err != nil {
			t.Fatalf("BuildAdaptationProposal: %v", err)
		}
		if proposal.Status != domain.AdaptationPlanStatusProposal || len(proposal.Chapters) != 2 {
			t.Fatalf("chapter proposal should stay full proposal: %+v", proposal)
		}
	})

	t.Run("short arc", func(t *testing.T) {
		st := store.NewStore(t.TempDir())
		if err := st.Init(); err != nil {
			t.Fatalf("Init: %v", err)
		}
		seedPreparedAdaptationSource(t, st, []int{10, 20, 30})
		llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
			"granularity": "arc",
			"status": "proposal",
			"rewrite_policy": "full_rewrite",
			"brief": "short arc restructure",
			"chapters": [
				{
					"chapter": 1,
					"title": "Short arc 1",
					"core_event": "Ari adapts the first source turn.",
					"hook": "The first hook points onward.",
					"scenes": ["station"],
					"source_chapters": [1],
					"source_range": {"from": 1, "to": 1},
					"word_budget": {"source_runes": 10, "target_runes": 12, "min_runes": 11, "max_runes": 13, "tolerance": 0.15}
				},
				{
					"chapter": 2,
					"title": "Short arc 2",
					"core_event": "Ari adapts the second source turn.",
					"hook": "The second hook escalates.",
					"scenes": ["archive"],
					"source_chapters": [2],
					"source_range": {"from": 2, "to": 2},
					"word_budget": {"source_runes": 20, "target_runes": 22, "min_runes": 21, "max_runes": 23, "tolerance": 0.15}
				},
				{
					"chapter": 3,
					"title": "Short arc 3",
					"core_event": "Ari adapts the third source turn.",
					"hook": "The third hook resolves.",
					"scenes": ["roof"],
					"source_chapters": [3],
					"source_range": {"from": 3, "to": 3},
					"word_budget": {"source_runes": 30, "target_runes": 32, "min_runes": 31, "max_runes": 33, "tolerance": 0.15}
				}
			]
		}`}}}

		proposal, err := BuildAdaptationProposal(Deps{Store: st, LLM: llm}, ProposalOptions{
			Brief:       "short arc restructure",
			Granularity: domain.AdaptationGranularityArc,
		})
		if err != nil {
			t.Fatalf("BuildAdaptationProposal: %v", err)
		}
		if proposal.Status != domain.AdaptationPlanStatusProposal || len(proposal.Chapters) != 3 {
			t.Fatalf("short arc proposal should include full chapter proposal: %+v", proposal)
		}
	})
}

func TestReviseAdaptationVolumeReviewUpdatesOnlySelectedVolume(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	if err := st.Adaptation.SaveVolumeReview(domain.AdaptationVolumeReview{
		Granularity:        domain.AdaptationGranularityFree,
		Status:             domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "free long arc",
		TargetChapterCount: 20,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Opening volume", TargetFrom: 1, TargetTo: 8, SourceFrom: 1, SourceTo: 2},
			{Index: 2, Title: "Pressure volume", TargetFrom: 9, TargetTo: 16, SourceFrom: 2, SourceTo: 3},
			{Index: 3, Title: "Resolution volume", TargetFrom: 17, TargetTo: 20, SourceFrom: 3, SourceTo: 4},
		},
	}); err != nil {
		t.Fatalf("SaveProposal: %v", err)
	}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{{text: `{
		"granularity": "free",
		"status": "volume_review",
		"rewrite_policy": "full_rewrite",
		"brief": "free long arc",
		"target_chapter_count": 22,
		"batches": [
			{"index": 2, "title": "Rebalanced pressure", "theme": "choice under cost", "expansion_decision": "expand", "target_from": 9, "target_to": 18, "source_from": 2, "source_to": 3, "summary": "expand only the middle volume"}
		]
	}`}}}

	updated, err := ReviseAdaptationVolumeReviewContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalRevisionOptions{
		VolumeIndex: 2,
		Instruction: "give the middle volume more room",
	})
	if err != nil {
		t.Fatalf("ReviseAdaptationVolumeReviewContext: %v", err)
	}
	if updated.Status != domain.AdaptationPlanStatusVolumeReview {
		t.Fatalf("volume review revision should stay review-only: %+v", updated)
	}
	if len(updated.Volumes) != 3 {
		t.Fatalf("volumes=%d, want 3: %+v", len(updated.Volumes), updated.Volumes)
	}
	if updated.Volumes[0].TargetFrom != 1 || updated.Volumes[0].TargetTo != 8 || updated.Volumes[0].Title != "Opening volume" {
		t.Fatalf("volume 1 should not be regenerated: %+v", updated.Volumes[0])
	}
	if updated.Volumes[1].Title != "Rebalanced pressure" || updated.Volumes[1].TargetFrom != 9 || updated.Volumes[1].TargetTo != 18 {
		t.Fatalf("volume 2 should be continuously updated: %+v", updated.Volumes[1])
	}
	if updated.Volumes[2].TargetFrom != 19 || updated.Volumes[2].TargetTo != 22 || updated.Volumes[2].Title != "Resolution volume" {
		t.Fatalf("later volume range should shift without regenerating metadata: %+v", updated.Volumes[2])
	}
}

func TestBuildAdaptationProposalDetailsFromVolumeReviewGeneratesFullProposalAndClearsReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	review := domain.AdaptationVolumeReview{
		Granularity:        domain.AdaptationGranularityFree,
		Status:             domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "free long arc",
		TargetChapterCount: 20,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Opening volume", TargetFrom: 1, TargetTo: 8, SourceFrom: 1, SourceTo: 2},
			{Index: 2, Title: "Pressure volume", TargetFrom: 9, TargetTo: 16, SourceFrom: 2, SourceTo: 3},
			{Index: 3, Title: "Resolution volume", TargetFrom: 17, TargetTo: 20, SourceFrom: 3, SourceTo: 4},
		},
	}
	if err := st.Adaptation.SaveVolumeReview(review); err != nil {
		t.Fatalf("SaveVolumeReview: %v", err)
	}
	if err := st.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:            1,
		Brief:              "free long arc",
		Granularity:        domain.AdaptationGranularityFree,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 20,
		Skeleton: &domain.AdaptationProposalRuntimeOutline{
			TargetChapterCount: 20,
			Batches: []domain.AdaptationProposalRuntimeSkeletonBatch{
				{Index: 1, Title: "Opening volume", TargetFrom: 1, TargetTo: 8, SourceFrom: 1, SourceTo: 2},
				{Index: 2, Title: "Pressure volume", TargetFrom: 9, TargetTo: 16, SourceFrom: 2, SourceTo: 3},
				{Index: 3, Title: "Resolution volume", TargetFrom: 17, TargetTo: 20, SourceFrom: 3, SourceTo: 4},
			},
		},
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerBatchProposalJSON(1, 4, 1, 2)},
		{text: plannerBatchProposalJSON(5, 8, 1, 2)},
		{text: plannerBatchProposalJSON(9, 12, 2, 3)},
		{text: plannerBatchProposalJSON(13, 16, 2, 3)},
		{text: plannerBatchProposalJSON(17, 20, 3, 4)},
	}}

	proposal, err := BuildAdaptationProposalDetailsContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalDetailsOptions{})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalDetailsContext: %v", err)
	}
	if proposal.Status != domain.AdaptationPlanStatusProposal || len(proposal.Chapters) != 20 {
		t.Fatalf("confirming volume review should generate full chapter proposal: %+v", proposal)
	}
	if runtime, err := st.Adaptation.LoadProposalRuntime(); err != nil || runtime != nil {
		t.Fatalf("volume review runtime should be cleared after details generation: runtime=%+v err=%v", runtime, err)
	}
	if savedReview, err := st.Adaptation.LoadVolumeReview(); err != nil || savedReview != nil {
		t.Fatalf("volume review should be cleared after details generation: review=%+v err=%v", savedReview, err)
	}
}

func TestBuildAdaptationProposalDetailsUsesCompactBudgetRepairPrompt(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{6000})
	foundation := testSourceFoundation()
	foundation.Premise = strings.Repeat("FOUNDATION_BLOAT", 2000)
	if err := st.Adaptation.SaveSourceFoundation(foundation); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	review := domain.AdaptationVolumeReview{
		Granularity:        domain.AdaptationGranularityArc,
		Status:             domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "repair one oversized volume before details",
		TargetChapterCount: 1,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Oversized volume", TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1, Summary: "source chapter is too large for one target chapter"},
		},
	}
	if err := st.Adaptation.SaveVolumeReview(review); err != nil {
		t.Fatalf("SaveVolumeReview: %v", err)
	}
	if err := st.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:            1,
		Brief:              review.Brief,
		Granularity:        review.Granularity,
		RewritePolicy:      review.RewritePolicy,
		TargetChapterCount: review.TargetChapterCount,
		Skeleton: &domain.AdaptationProposalRuntimeOutline{
			TargetChapterCount: review.TargetChapterCount,
			Batches: []domain.AdaptationProposalRuntimeSkeletonBatch{
				{Index: 1, Title: "Oversized volume", TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1},
			},
		},
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}
	arcRepairSkeleton := strings.Replace(
		plannerVolumeRevisionSkeletonJSONWithDecision(1, 1, 2, 1, 1, "expand"),
		`"granularity": "free"`,
		`"granularity": "arc"`,
		1,
	)
	llm := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: arcRepairSkeleton},
		{text: plannerProposalJSONFromChaptersForGranularity(t, domain.AdaptationGranularityArc, plannerSharedSourceRangePlans(1, 2, 1, 1, 6000))},
	}}

	proposal, err := BuildAdaptationProposalDetailsContext(context.Background(), Deps{Store: st, LLM: llm}, ProposalDetailsOptions{})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalDetailsContext: %v", err)
	}
	if len(proposal.Chapters) != 2 {
		t.Fatalf("proposal chapters=%d, want repaired 2 chapters", len(proposal.Chapters))
	}
	if len(llm.got) < 2 {
		t.Fatalf("llm calls=%d, want budget repair plus detail generation", len(llm.got))
	}
	budgetPrompt := llm.got[0][1].TextContent()
	if !strings.Contains(budgetPrompt, "Compact budget repair input") || !strings.Contains(budgetPrompt, `"budget_issue"`) {
		t.Fatalf("budget repair prompt should expose compact budget issue, got %s", budgetPrompt)
	}
	for _, forbidden := range []string{`"source_foundation"`, `"source_reports"`, `"all_volumes"`, "FOUNDATION_BLOAT"} {
		if strings.Contains(budgetPrompt, forbidden) {
			t.Fatalf("budget repair prompt should not include full-context field %s", forbidden)
		}
	}
	if len(budgetPrompt) > 12000 {
		t.Fatalf("budget repair prompt should stay compact, len=%d", len(budgetPrompt))
	}
}

func TestBuildAdaptationProposalDetailsResumesCompletedRuntimeBatch(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	seedPreparedAdaptationSource(t, st, []int{10, 20, 30, 40})
	review := domain.AdaptationVolumeReview{
		Granularity:        domain.AdaptationGranularityFree,
		Status:             domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "free long arc",
		TargetChapterCount: 8,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Opening volume", TargetFrom: 1, TargetTo: 8, SourceFrom: 1, SourceTo: 4},
		},
	}
	if err := st.Adaptation.SaveVolumeReview(review); err != nil {
		t.Fatalf("SaveVolumeReview: %v", err)
	}
	first := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerBatchProposalJSON(1, 4, 1, 2)},
		{err: context.Canceled},
	}}
	_, err := BuildAdaptationProposalDetailsContext(context.Background(), Deps{Store: st, LLM: first}, ProposalDetailsOptions{})
	if err == nil {
		t.Fatal("first details run should fail after saving the first batch")
	}
	if first.calls != 2 {
		t.Fatalf("first calls=%d, want first detail success plus second detail failure", first.calls)
	}
	runtime, err := st.Adaptation.LoadProposalRuntime()
	if err != nil {
		t.Fatalf("LoadProposalRuntime: %v", err)
	}
	if runtime == nil || len(runtime.CompletedBatches) != 1 {
		t.Fatalf("runtime should keep completed detail batch: %+v", runtime)
	}
	progress := make([]Event, 0)
	second := &scriptedAdaptLLM{responses: []adaptLLMResponse{
		{text: plannerBatchProposalJSON(5, 8, 3, 4)},
	}}
	proposal, err := BuildAdaptationProposalDetailsContext(context.Background(), Deps{Store: st, LLM: second}, ProposalDetailsOptions{
		EmitProgress: captureAdaptProgress(&progress),
	})
	if err != nil {
		t.Fatalf("BuildAdaptationProposalDetailsContext resume: %v", err)
	}
	if second.calls != 1 {
		t.Fatalf("resume calls=%d, want only remaining detail batch", second.calls)
	}
	if !hasAdaptProgress(progress, "已复用并校验 1/2 个章节详情批次") {
		t.Fatalf("resume progress should summarize reused detail batches, got %+v", progress)
	}
	if hasAdaptProgress(progress, "骨架规划完成") {
		t.Fatalf("resume progress should not imply that the skeleton was regenerated, got %+v", progress)
	}
	if len(proposal.Chapters) != 8 || proposal.Chapters[0].Chapter != 1 || proposal.Chapters[7].Chapter != 8 {
		t.Fatalf("resumed proposal chapters = %+v", proposal.Chapters)
	}
	if runtime, err := st.Adaptation.LoadProposalRuntime(); err != nil || runtime != nil {
		t.Fatalf("runtime should be cleared after successful details save: runtime=%+v err=%v", runtime, err)
	}
}

func TestShouldRetryPlannerGenerateStopsOnNonRetryableProviderError(t *testing.T) {
	err := nonRetryablePlannerError{msg: "rate_limit_exceeded"}

	if shouldRetryPlannerGenerate(context.Background(), err, 1, 14) {
		t.Fatal("non-retryable provider error should stop planner generate retries")
	}
}

func TestShouldRetryPlannerGenerateStopsOnHardLimitMessage(t *testing.T) {
	err := fmt.Errorf("rate_limit_exceeded")

	if shouldRetryPlannerGenerate(context.Background(), err, 1, 14) {
		t.Fatal("hard provider limit message should stop planner generate retries")
	}
}

func TestShouldRetryPlannerGenerateRetriesTemporaryRateLimitMessage(t *testing.T) {
	err := fmt.Errorf("too many requests")

	if !shouldRetryPlannerGenerate(context.Background(), err, 1, 14) {
		t.Fatal("temporary rate limit message should remain retryable")
	}
}

func TestShouldRetryPlannerGenerateRetriesRuntimeGatewayTerminalError(t *testing.T) {
	err := retryablePlannerError{err: litellm.NewHTTPError("p1", 503, "<html><body>503 Service Unavailable</body></html>")}

	if !shouldRetryPlannerGenerate(context.Background(), err, 1, 14) {
		t.Fatal("retryable runtime gateway terminal error should remain retryable")
	}
}

func TestPlannerGenerateAttemptLimitBrieflyRetriesBareAuthorizationFailure(t *testing.T) {
	err := nonRetryablePlannerError{msg: "authorization failed"}
	limit := plannerGenerateAttemptLimit(err, 14)
	if limit != adaptationPlannerAuthorizationMaxAttempts {
		t.Fatalf("attempt limit = %d, want %d", limit, adaptationPlannerAuthorizationMaxAttempts)
	}
	if !shouldRetryPlannerGenerate(context.Background(), err, 1, limit) {
		t.Fatal("first bare authorization failure should receive a short retry")
	}
	if shouldRetryPlannerGenerate(context.Background(), err, limit, limit) {
		t.Fatal("bare authorization failure should stop after the short retry budget")
	}
}

func TestPlannerGenerateAttemptLimitDoesNotRetryCredentialErrors(t *testing.T) {
	for _, message := range []string{"invalid token", "HTTP 401 unauthorized"} {
		err := nonRetryablePlannerError{msg: message}
		limit := plannerGenerateAttemptLimit(err, 14)
		if limit != 14 {
			t.Fatalf("%q attempt limit = %d, want configured limit", message, limit)
		}
		if shouldRetryPlannerGenerate(context.Background(), err, 1, limit) {
			t.Fatalf("credential error %q must remain non-retryable", message)
		}
	}
}

type nonRetryablePlannerError struct {
	msg string
}

func (e nonRetryablePlannerError) Error() string {
	return e.msg
}

func (e nonRetryablePlannerError) Retryable() bool {
	return false
}

type retryablePlannerError struct {
	err error
}

func (e retryablePlannerError) Error() string {
	return e.err.Error()
}

func (e retryablePlannerError) Unwrap() error {
	return e.err
}

func (e retryablePlannerError) Retryable() bool {
	return true
}

func plannerBudgetProposalJSON(planFields string, chapterFields string, wordBudget string) string {
	if strings.TrimSpace(planFields) != "" {
		planFields = strings.TrimSpace(planFields) + ","
	}
	if strings.TrimSpace(chapterFields) != "" {
		chapterFields = strings.TrimSpace(chapterFields) + ","
	}
	return fmt.Sprintf(`{
		%s
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "arc restructure",
		"chapters": [
			{
				%s
				"chapter": 1,
				"title": "Budgeted",
				"core_event": "Ari merges both source turns.",
				"hook": "The budget contradiction must be caught.",
				"scenes": ["station"],
				"source_chapters": [1, 2],
				"source_range": {"from": 1, "to": 2},
				"word_budget": {%s}
			}
		]
	}`, planFields, chapterFields, wordBudget)
}

func captureAdaptProgress(events *[]Event) ProgressEmitter {
	return func(stage Stage, current, total int, msg string, err error) {
		*events = append(*events, Event{
			Stage:   stage,
			Current: current,
			Total:   total,
			Message: msg,
			Err:     err,
		})
	}
}

func hasAdaptProgress(events []Event, fragment string) bool {
	for _, event := range events {
		if strings.Contains(event.Message, fragment) {
			return true
		}
		if event.Err != nil && strings.Contains(event.Err.Error(), fragment) {
			return true
		}
	}
	return false
}

func stubPlannerRetrySleep(t *testing.T) func() {
	t.Helper()
	original := plannerRetrySleep
	plannerRetrySleep = func(context.Context, time.Duration) error { return nil }
	return func() { plannerRetrySleep = original }
}

func plannerBatchProposalJSON(from, to, sourceFrom, sourceTo int) string {
	return plannerBatchProposalJSONWithOmittedBudget(from, to, sourceFrom, sourceTo, 0)
}

func plannerBatchPlans(from, to, sourceFrom, sourceTo int) []domain.AdaptationChapterPlan {
	plan, err := parsePlannerProposal(plannerBatchProposalJSON(from, to, sourceFrom, sourceTo))
	if err != nil {
		panic(err)
	}
	return plan.Chapters
}

func plannerTestOutlineParts(chapter, sourceChapter int, prefix string) (string, string, string, []string) {
	motifs := []struct {
		title string
		core  string
		hook  string
		scene string
	}{
		{"Harbor Cipher", "deciphers a salt-stained harbor ledger, bargains with a ferryman, and hides a brass key under the rain barrel", "a tide bell rings from a locked boathouse", "ferry ledger exchange"},
		{"Glass Observatory", "climbs the observatory stairs, repairs a cracked lens, and exposes the false comet signal before sunrise", "the telescope shows two moons", "observatory lens test"},
		{"Market Intercept", "trades a forged pass in the market, follows a spice vendor, and recovers a coded ribbon from the awning", "the ribbon carries the wrong family crest", "market tail"},
		{"Cistern Descent", "opens the dry cistern, maps the echoing tunnels, and rescues a courier trapped behind the iron grate", "water starts rising in a room marked dry", "cistern rescue"},
		{"Theater Switch", "joins the backstage crew, swaps the poisoned prop, and forces the actor to reveal the patron's signal", "the applause hides a second command", "backstage prop switch"},
		{"Rooftop Accord", "crosses the tiled roofs, bargains with a rival scout, and plants a lantern code above the magistrate's gate", "the rival asks for protection instead of coin", "rooftop bargain"},
		{"Archive Lock", "sorts the burned archive slips, reconstructs the missing index, and seals the witness name in a wax tube", "the index points to a living witness", "archive reconstruction"},
		{"Garden Trial", "tests the courtyard footprints, catches the gardener's false alibi, and uncovers the message inside a seed jar", "the seed jar rattles with metal", "courtyard footprint test"},
		{"Train Cipher", "boards the night train, swaps a baggage tag, and follows a conductor to the silent mail car", "the mailbag is stitched with a royal warning", "night train exchange"},
		{"Quarry Signal", "descends into the marble quarry, times the blasting horn, and uncovers a chalk route behind the powder shed", "the next blast is scheduled too early", "quarry horn timing"},
		{"Clockmaker Visit", "visits the clockmaker's attic, resets a stopped pendulum, and finds a coded gear in the workbench", "the gear turns without a spring", "attic pendulum reset"},
		{"Monastery Ledger", "copies the monastery ledger, questions the bell keeper, and traces a missing donation to a sealed crypt", "the bell rings thirteen times at noon", "monastery bell audit"},
		{"Canal Ambush", "rows through the canal fog, tricks a courier boat, and retrieves a tin cylinder from the tow rope", "the tow rope is cut from the wrong bank", "canal boat feint"},
		{"Library Trial", "debates the head librarian, compares three marginal notes, and exposes a planted confession in the atlas room", "the atlas page shows an impossible border", "library atlas debate"},
		{"Foundry Bargain", "enters the bronze foundry, cools a stolen mold, and forces the foreman to identify the midnight buyer", "the mold carries a fresh thumbprint", "foundry mold cooling"},
		{"Clinic Ledger", "checks the charity clinic ledger, follows a nurse's false errand, and finds medicine hidden in a prayer box", "the medicine label has tomorrow's date", "clinic errand shadow"},
		{"Lighthouse Code", "climbs the lighthouse stairs, changes the shutter rhythm, and reveals a ship waiting beyond the reef", "the light answers from an empty sea", "lighthouse shutter code"},
		{"Museum Swap", "guards the museum vault, switches a replica idol, and catches the curator signaling through a cracked mirror", "the mirror shows the vault from outside", "museum idol switch"},
		{"Desert Relay", "rides to the desert relay post, decodes a camel bell pattern, and rescues a messenger buried under canvas", "the bell pattern repeats a dead man's name", "relay post decoding"},
		{"Snowfield Trace", "crosses the snowfield markers, tests the frozen ink, and reveals the smuggler's route under the chapel wall", "the ink warms before the fire is lit", "snowfield ink test"},
		{"Opera Ledger", "audits the opera troupe accounts, follows a masked singer, and recovers a receipt hidden in a drum skin", "the drumbeat skips the murder hour", "opera account audit"},
		{"Bridge Oath", "holds the bridge checkpoint, bargains with a deserter, and plants a false oath token in the toll chest", "the deserter knows the new password", "bridge toll bargain"},
		{"Vineyard Map", "searches the vineyard press, stains a hidden map with grape ash, and corners the buyer in the tasting room", "the map blooms under sour wine", "vineyard map stain"},
		{"Island Beacon", "repairs the island beacon, tests a gull-feather cipher, and exposes a rescue boat that never launched", "the beacon smoke bends against the wind", "island beacon repair"},
	}
	motif := motifs[(chapter-1)%len(motifs)]
	title := fmt.Sprintf("Target %d %s", chapter, motif.title)
	if prefix != "" {
		title = fmt.Sprintf("%s %d %s", prefix, chapter, motif.title)
	}
	core := fmt.Sprintf("Ari %s.", motif.core)
	hook := fmt.Sprintf("%s.", motif.hook)
	scenes := []string{
		fmt.Sprintf("%s setup", motif.scene),
		fmt.Sprintf("%s consequence", motif.title),
	}
	return title, core, hook, scenes
}

func plannerTestScenesJSON(scenes []string) string {
	raw, err := json.Marshal(scenes)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func plannerBudgetRangePlans(from, to, sourceFrom, sourceTo int) []domain.AdaptationChapterPlan {
	plans := make([]domain.AdaptationChapterPlan, 0, to-from+1)
	for chapter := from; chapter <= to; chapter++ {
		title, core, hook, scenes := plannerTestOutlineParts(chapter, sourceFrom, "")
		plans = append(plans, domain.AdaptationChapterPlan{
			Chapter:        chapter,
			Title:          title,
			OutlineEntry:   domain.OutlineEntry{Chapter: chapter, Title: title, CoreEvent: core, Hook: hook, Scenes: scenes},
			SourceChapters: []int{sourceFrom, sourceTo},
			SourceRange:    domain.SourceRange{From: sourceFrom, To: sourceTo},
		})
	}
	return plans
}

func plannerIndexRange(count int) []int {
	indexes := make([]int, count)
	for idx := range indexes {
		indexes[idx] = idx
	}
	return indexes
}

func plannerSharedSourceRangePlans(from, to, sourceFrom, sourceTo, sourceRunes int) []domain.AdaptationChapterPlan {
	count := to - from + 1
	plans := make([]domain.AdaptationChapterPlan, 0, count)
	for chapter := from; chapter <= to; chapter++ {
		offset := chapter - from
		anchor := sourceFrom
		if sourceTo > sourceFrom && offset >= count/2 {
			anchor = sourceTo
		}
		chapterRunes := splitRunesForIndex(sourceRunes, count, offset)
		if chapterRunes <= 0 {
			chapterRunes = 1000
		}
		title, core, hook, scenes := plannerTestOutlineParts(chapter, anchor, "")
		plans = append(plans, domain.AdaptationChapterPlan{
			Chapter: chapter,
			Title:   title,
			OutlineEntry: domain.OutlineEntry{
				Chapter:   chapter,
				Title:     title,
				CoreEvent: core,
				Hook:      hook,
				Scenes:    scenes,
			},
			SourceChapters: []int{anchor},
			SourceRange:    domain.SourceRange{From: sourceFrom, To: sourceTo},
			WordBudget: &domain.AdaptationChapterWordBudget{
				SourceRunes: chapterRunes,
				TargetRunes: chapterRunes,
				MinRunes:    max(1, chapterRunes-1),
				MaxRunes:    chapterRunes + 1,
				Tolerance:   0.15,
			},
			PreserveEvents:  []string{"source event"},
			RequiredChanges: []string{"adapt the shared source arc"},
			ForbiddenMoves:  []string{"drop the source anchor"},
		})
	}
	return plans
}

func repeatedSourceRangePlans(from, to, sourceChapter, sourceRunes int) []domain.AdaptationChapterPlan {
	plans := make([]domain.AdaptationChapterPlan, 0, to-from+1)
	for chapter := from; chapter <= to; chapter++ {
		title, core, hook, scenes := plannerTestOutlineParts(chapter, sourceChapter, "")
		plans = append(plans, domain.AdaptationChapterPlan{
			Chapter: chapter,
			Title:   title,
			OutlineEntry: domain.OutlineEntry{
				Chapter:   chapter,
				Title:     title,
				CoreEvent: core,
				Hook:      hook,
				Scenes:    scenes,
			},
			SourceChapters: []int{sourceChapter},
			SourceRange:    domain.SourceRange{From: sourceChapter, To: sourceChapter},
			WordBudget: &domain.AdaptationChapterWordBudget{
				SourceRunes: sourceRunes,
				TargetRunes: sourceRunes,
				MinRunes:    sourceRunes - 1,
				MaxRunes:    sourceRunes + 1,
				Tolerance:   0.15,
			},
			PreserveEvents:  []string{"source event"},
			RequiredChanges: []string{"adapt the beat"},
			ForbiddenMoves:  []string{"drop the source anchor"},
		})
	}
	return plans
}

func plannerBatchProposalJSONWithoutWordBudget(from, to, sourceFrom, sourceTo int, omittedChapter int) string {
	return plannerBatchProposalJSONWithOmittedBudget(from, to, sourceFrom, sourceTo, omittedChapter)
}

func plannerRepeatedSourceBudgetProposalJSON(from, to, sourceChapter, sourceRunes, targetRunes, minRunes, maxRunes int) string {
	count := to - from + 1
	chapters := make([]string, 0, to-from+1)
	for chapter := from; chapter <= to; chapter++ {
		title, core, hook, scenes := plannerTestOutlineParts(chapter, sourceChapter, "")
		chapters = append(chapters, fmt.Sprintf(`{
			"chapter": %d,
			"title": %q,
			"core_event": %q,
			"hook": %q,
			"scenes": %s,
			"source_chapters": [%d],
			"source_range": {"from": %d, "to": %d},
			"word_budget": {"source_runes": %d, "target_runes": %d, "min_runes": %d, "max_runes": %d, "tolerance": 0.15},
			"preserve_events": ["source event"],
			"required_changes": ["adapt the beat"],
			"forbidden_moves": ["drop the source anchor"]
		}`, chapter, title, core, hook, plannerTestScenesJSON(scenes), sourceChapter, sourceChapter, sourceChapter, sourceRunes, targetRunes, minRunes, maxRunes))
	}
	return fmt.Sprintf(`{
		"granularity": "arc",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "split the long source chapter by plot beats",
		"target_total_runes": %d,
		"target_min_runes": %d,
		"target_max_runes": %d,
		"chapters": [%s]
	}`, targetRunes*count, minRunes*count, maxRunes*count, strings.Join(chapters, ","))
}

func plannerBatchProposalJSONWithOmittedBudget(from, to, sourceFrom, sourceTo int, omittedChapter int) string {
	count := to - from + 1
	sourceSpan := sourceTo - sourceFrom + 1
	chapters := make([]string, 0, count)
	for chapter := from; chapter <= to; chapter++ {
		sourceChapter := sourceFrom
		sourceRangeFrom := sourceFrom
		sourceRangeTo := sourceFrom
		if count > 0 && sourceSpan > 0 {
			sourceChapter = sourceFrom + (chapter-from)*sourceSpan/count
			sourceRangeFrom = sourceFrom + (chapter-from)*sourceSpan/count
			sourceRangeTo = sourceFrom + (chapter-from+1)*sourceSpan/count - 1
			if sourceRangeTo < sourceRangeFrom {
				sourceRangeTo = sourceRangeFrom
			}
			if chapter == to {
				sourceRangeTo = sourceTo
			}
		}
		sourceRunes := sourceChapter * 10
		targetRunes := sourceRunes + 2
		wordBudget := fmt.Sprintf(`,
			"word_budget": {"source_runes": %d, "target_runes": %d, "min_runes": %d, "max_runes": %d, "tolerance": 0.15}`, sourceRunes, targetRunes, targetRunes-1, targetRunes+1)
		if chapter == omittedChapter {
			wordBudget = ""
		}
		title, core, hook, scenes := plannerTestOutlineParts(chapter, sourceChapter, "")
		chapters = append(chapters, fmt.Sprintf(`{
			"chapter": %d,
			"title": %q,
			"core_event": %q,
			"hook": %q,
			"scenes": %s,
			"source_chapters": [%d],
			"source_range": {"from": %d, "to": %d}%s,
			"preserve_events": ["source event"],
			"required_changes": ["adapt the beat"],
			"forbidden_moves": ["drop the source anchor"]
		}`, chapter, title, core, hook, plannerTestScenesJSON(scenes), sourceChapter, sourceRangeFrom, sourceRangeTo, wordBudget))
	}
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"chapters": [%s]
	}`, strings.Join(chapters, ","))
}

func testSourceMapSkeletonBatch(index, sourceFrom, sourceTo, targetFrom, targetTo int) plannerSkeletonBatch {
	return plannerSkeletonBatch{
		Index:              index,
		Title:              fmt.Sprintf("Batch %d", index),
		Theme:              "focused arc",
		Summary:            "valid source-map skeleton batch",
		TargetFrom:         targetFrom,
		TargetTo:           targetTo,
		TargetChapterCount: targetTo - targetFrom + 1,
		SourceFrom:         sourceFrom,
		SourceTo:           sourceTo,
	}
}

func plannerVolumeRevisionSkeletonJSON(index, from, to, sourceFrom, sourceTo int) string {
	originalTo := map[int]int{1: 4, 2: 8, 3: 12}[index]
	decision := "keep"
	if originalTo > 0 && to > originalTo {
		decision = "expand"
	}
	return plannerVolumeRevisionSkeletonJSONWithDecision(index, from, to, sourceFrom, sourceTo, decision)
}

func plannerVolumeRevisionSkeletonJSONWithDecision(index, from, to, sourceFrom, sourceTo int, decision string) string {
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"target_chapter_count": %d,
		"batches": [{
			"index": %d,
			"title": "Revised volume %d",
			"theme": "rebalanced pressure",
			"expansion_decision": %q,
			"expansion_reason": "model judged the revised volume scope.",
			"summary": "Replanned volume beats.",
			"target_from": %d,
			"target_to": %d,
			"source_from": %d,
			"source_to": %d
		}]
	}`, to-from+1, index, index, decision, from, to, sourceFrom, sourceTo)
}

func plannerRevisionProposalJSON(from, to, sourceFrom, sourceTo int) string {
	count := to - from + 1
	sourceSpan := sourceTo - sourceFrom + 1
	chapters := make([]string, 0, count)
	for chapter := from; chapter <= to; chapter++ {
		sourceChapter := sourceFrom
		sourceRangeFrom := sourceFrom
		sourceRangeTo := sourceFrom
		if count > 0 && sourceSpan > 0 {
			sourceChapter = sourceFrom + (chapter-from)*sourceSpan/count
			sourceRangeFrom = sourceFrom + (chapter-from)*sourceSpan/count
			sourceRangeTo = sourceFrom + (chapter-from+1)*sourceSpan/count - 1
			if sourceRangeTo < sourceRangeFrom {
				sourceRangeTo = sourceRangeFrom
			}
			if chapter == to {
				sourceRangeTo = sourceTo
			}
		}
		sourceRunes := sourceChapter * 10
		targetRunes := sourceRunes + 2
		outlineChapter := ((chapter - 1) % 12) + 13
		title, core, hook, scenes := plannerTestOutlineParts(outlineChapter, sourceChapter, "Revised")
		title = strings.Replace(title, fmt.Sprintf("Revised %d ", outlineChapter), fmt.Sprintf("Revised %d ", chapter), 1)
		uniqueCore, uniqueHook, uniqueScene := plannerRevisionUniqueOutlineNote(chapter)
		core = fmt.Sprintf("%s %s", core, uniqueCore)
		hook = fmt.Sprintf("%s %s", hook, uniqueHook)
		scenes = append(scenes, uniqueScene)
		chapters = append(chapters, fmt.Sprintf(`{
			"chapter": %d,
			"title": %q,
			"core_event": %q,
			"hook": %q,
			"scenes": %s,
			"source_chapters": [%d],
			"source_range": {"from": %d, "to": %d},
			"word_budget": {"source_runes": %d, "target_runes": %d, "min_runes": %d, "max_runes": %d, "tolerance": 0.15},
			"preserve_events": ["source event"],
			"required_changes": ["apply the revision"],
			"forbidden_moves": ["drop the source anchor"]
		}`, chapter, title, core, hook, plannerTestScenesJSON(scenes), sourceChapter, sourceRangeFrom, sourceRangeTo, sourceRunes, targetRunes, targetRunes-1, targetRunes+1))
	}
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"chapters": [%s]
	}`, strings.Join(chapters, ","))
}

func plannerRevisionUniqueOutlineNote(chapter int) (string, string, string) {
	notes := []struct {
		core  string
		hook  string
		scene string
	}{
		{"Amber jurors compare wax seals under a lantern map.", "A cedar token appears in the judge's ink bowl.", "amber seal arbitration"},
		{"Cobalt divers recover a copper tube from the flooded sluice.", "A blue rope knot points upstream.", "cobalt sluice recovery"},
		{"Ivory couriers trade coded gloves beside a silent kennel.", "The hound refuses the clean glove.", "ivory courier kennel"},
		{"Saffron clerks audit temple bells during a dust storm.", "The missing bell note names a hidden donor.", "saffron bell audit"},
		{"Violet miners weigh glass ore against a forged charter.", "The ore glows only under stolen paper.", "violet quarry charter"},
		{"Silver pilots mark cloud routes over a broken aqueduct.", "The flight slate lists an impossible landing.", "silver aqueduct route"},
		{"Crimson tailors stitch testimony into a festival banner.", "The final thread spells the wrong oath.", "crimson banner testimony"},
		{"Indigo cooks hide a ledger inside salted plum jars.", "The sour jar rings like porcelain.", "indigo kitchen ledger"},
		{"Bronze cartographers erase a river from the winter atlas.", "The blank river stains the surveyor's hand.", "bronze atlas erasure"},
		{"Pearl locksmiths test seven keys in a rain-dark shrine.", "The sixth key opens a door without hinges.", "pearl shrine keys"},
		{"Verdant apothecaries label dream vials with false harvest dates.", "A green vial remembers tomorrow.", "verdant vial ledger"},
		{"Obsidian watchmen count ferry lamps from an empty pier.", "The dark lamp answers from below water.", "obsidian ferry watch"},
	}
	note := notes[(chapter-1)%len(notes)]
	return note.core, note.hook, note.scene
}

func plannerRevisionProposalJSONMissingWordBudget(from, to int) string {
	var chapters []string
	for chapter := from; chapter <= to; chapter++ {
		title, core, hook, scenes := plannerTestOutlineParts(chapter, chapter, "Incomplete revised")
		chapters = append(chapters, fmt.Sprintf(`{
			"chapter": %d,
			"title": %q,
			"core_event": %q,
			"hook": %q,
			"scenes": %s
		}`, chapter, title, core, hook, plannerTestScenesJSON(scenes)))
	}
	return fmt.Sprintf(`{
		"granularity": "free",
		"status": "proposal",
		"rewrite_policy": "full_rewrite",
		"brief": "chunk",
		"chapters": [%s]
	}`, strings.Join(chapters, ","))
}

func plannerRevisionNoChangeProposalJSON(t *testing.T, chapters []domain.AdaptationChapterPlan, from, to int) string {
	t.Helper()
	payload := map[string]any{
		"chapters": proposalChaptersInRange(chapters, from, to),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal no-change revision: %v", err)
	}
	return string(raw)
}

func plannerProposalJSONFromChapters(t *testing.T, chapters []domain.AdaptationChapterPlan) string {
	t.Helper()
	return plannerProposalJSONFromChaptersForGranularity(t, domain.AdaptationGranularityFree, chapters)
}

func plannerProposalJSONFromChaptersForGranularity(t *testing.T, granularity string, chapters []domain.AdaptationChapterPlan) string {
	t.Helper()
	payload := map[string]any{
		"granularity":    granularity,
		"status":         domain.AdaptationPlanStatusProposal,
		"rewrite_policy": domain.AdaptationRewriteFullRewrite,
		"brief":          "chunk",
		"chapters":       chapters,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal planner proposal: %v", err)
	}
	return string(raw)
}

func saveRevisionTestProposal(t *testing.T, st *store.Store) {
	t.Helper()
	chapters := make([]domain.AdaptationChapterPlan, 0, 12)
	sourceTotal := 0
	targetTotal := 0
	targetMinTotal := 0
	targetMaxTotal := 0
	for chapter := 1; chapter <= 12; chapter++ {
		sourceChapter := 1 + (chapter-1)*4/12
		sourceRunes := sourceChapter * 10
		targetRunes := sourceRunes + 2
		title, core, hook, scenes := plannerTestOutlineParts(chapter, sourceChapter, "Original")
		sourceTotal += sourceRunes
		targetTotal += targetRunes
		targetMinTotal += targetRunes - 1
		targetMaxTotal += targetRunes + 1
		chapters = append(chapters, domain.AdaptationChapterPlan{
			Chapter:        chapter,
			Title:          title,
			SourceChapters: []int{sourceChapter},
			SourceRunes:    sourceRunes,
			TargetRunes:    targetRunes,
			TargetMinRunes: targetRunes - 1,
			TargetMaxRunes: targetRunes + 1,
			SourceRange:    domain.SourceRange{From: sourceChapter, To: sourceChapter},
			WordBudget: &domain.AdaptationChapterWordBudget{
				SourceRunes: sourceRunes,
				TargetRunes: targetRunes,
				MinRunes:    targetRunes - 1,
				MaxRunes:    targetRunes + 1,
				Tolerance:   0.15,
			},
			PreserveEvents:  []string{"source event"},
			RequiredChanges: []string{"keep the original shape"},
			ForbiddenMoves:  []string{"drop the source anchor"},
			OutlineEntry: domain.OutlineEntry{
				Chapter:   chapter,
				Title:     title,
				CoreEvent: core,
				Hook:      hook,
				Scenes:    scenes,
			},
		})
	}
	plan := domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityFree,
		Status:           domain.AdaptationPlanStatusProposal,
		RewritePolicy:    domain.AdaptationRewriteFullRewrite,
		Brief:            "chunk",
		SourceTotalRunes: sourceTotal,
		TargetTotalRunes: targetTotal,
		TargetMinRunes:   targetMinTotal,
		TargetMaxRunes:   targetMaxTotal,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Opening volume", Theme: "orientation", TargetFrom: 1, TargetTo: 4, SourceFrom: 1, SourceTo: 2},
			{Index: 2, Title: "Middle volume", Theme: "pressure", TargetFrom: 5, TargetTo: 8, SourceFrom: 2, SourceTo: 3},
			{Index: 3, Title: "Final volume", Theme: "payoff", TargetFrom: 9, TargetTo: 12, SourceFrom: 3, SourceTo: 4},
		},
		Chapters: chapters,
		Planner:  &domain.AdaptationPlannerMeta{Prompt: "adaptation-planner", Model: "fake"},
	}
	if err := st.Adaptation.SaveProposal(plan); err != nil {
		t.Fatalf("SaveProposal: %v", err)
	}
}

func seedPreparedAdaptationSource(t *testing.T, st *store.Store, runeCounts []int) []domain.AdaptationSourceReport {
	t.Helper()
	sources := make([]domain.AdaptationSource, 0, len(runeCounts))
	reports := make([]domain.AdaptationSourceReport, 0, len(runeCounts))
	for i, runeCount := range runeCounts {
		chapter := i + 1
		title := "Source"
		body := strings.Repeat("a", runeCount)
		source, err := st.Adaptation.SaveSourceChapter(chapter, title, body)
		if err != nil {
			t.Fatalf("SaveSourceChapter %d: %v", chapter, err)
		}
		sources = append(sources, source)
		report := domain.AdaptationSourceReport{
			Chapter:      chapter,
			Title:        title,
			SourceSHA256: source.SHA256,
			Summary:      "source summary",
			KeyEvents:    []string{"source event"},
		}
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatalf("SaveSourceReport %d: %v", chapter, err)
		}
		reports = append(reports, report)
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: len(sources),
		Chapters:     sources,
	}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	seedCoCreateDossier(t, st, manifest)
	seedConfirmedAdaptationTargetFoundation(t, st, manifest, "test adaptation intent")
	return reports
}

func seedConfirmedAdaptationTargetFoundation(t *testing.T, st *store.Store, manifest domain.AdaptationSourceManifest, brief string) {
	t.Helper()
	intent := BuildCoCreateIntent(brief, domain.AdaptationGranularityArc, domain.AdaptationRewriteFullRewrite, DefaultWordTolerance)
	if err := st.Adaptation.SaveCoCreateIntent(intent); err != nil {
		t.Fatal(err)
	}
	binding, err := st.CoreCast.SaveGateBinding(store.CoreCastGateBinding{
		Mode: domain.CoreCastModeAdaptation, DraftRevision: 1, DraftHash: "test-draft",
		SourceSignature: store.AdaptationSourceSignature(manifest), AdaptationIntentHash: intent.IntentHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeAdaptation,
		DraftRevision: binding.DraftRevision, DraftHash: binding.DraftHash,
		SourceSignature: binding.SourceSignature, AdaptationIntentHash: binding.AdaptationIntentHash,
		Members: []domain.CoreCastMember{{
			Character:  domain.Character{ID: "ari", Name: "Ari", Role: "protagonist", Goal: "complete the adaptation", Motivation: "duty", Conflict: "source pressure", Arc: "chooses a target future", Traits: []string{"resolute"}, Constraints: []string{"respects source evidence"}},
			Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal,
			MainlineFunction: "drives the target mainline", InclusionRationale: "confirmed adaptation lead", NoCoreRelationships: true,
		}},
	}
	saved, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	workflow, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageTargetFoundationGenerating, -1)
	if err != nil {
		t.Fatal(err)
	}
	review, err := GenerateTargetFoundation(context.Background(), Deps{Store: st}, TargetFoundationOptions{Brief: brief, ExpectedWorkflowRevision: workflow.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConfirmAdaptationTargetFoundation(review.FoundationRevision, review.Binding.TargetFoundationAuditSignature); err != nil {
		t.Fatal(err)
	}
	current, err := st.Adaptation.LoadPlanningWorkflow()
	if err != nil || current == nil {
		t.Fatal(err)
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageSkeletonGenerating, current.Revision); err != nil {
		t.Fatal(err)
	}
}

func seedDirectConfirmedAdaptationTargetFoundation(t *testing.T, st *store.Store, chapterCount int, brief string) *domain.AdaptationFoundationBinding {
	t.Helper()
	sources := make([]domain.AdaptationSource, 0, chapterCount)
	reports := make([]domain.AdaptationSourceReport, 0, chapterCount)
	for chapter := 1; chapter <= chapterCount; chapter++ {
		source, err := st.Adaptation.SaveSourceChapter(chapter, fmt.Sprintf("Source %d", chapter), fmt.Sprintf("source body %d", chapter))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
		report := domain.AdaptationSourceReport{Chapter: chapter, Title: source.Title, SourceSHA256: source.SHA256, Summary: "source summary", KeyEvents: []string{"source event"}}
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatal(err)
		}
		reports = append(reports, report)
	}
	manifest := domain.AdaptationSourceManifest{SourcePath: "source.txt", ChapterCount: chapterCount, Chapters: sources}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if foundation, err := st.Adaptation.LoadSourceFoundation(); err != nil {
		t.Fatal(err)
	} else if foundation == nil {
		if err := st.Adaptation.SaveSourceFoundation(testSourceFoundation()); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatal(err)
	}
	seedConfirmedAdaptationTargetFoundation(t, st, manifest, brief)
	binding, err := st.CurrentAdaptationArtifactBinding()
	if err != nil {
		t.Fatal(err)
	}
	return &binding
}

func seedCoCreateDossier(t *testing.T, st *store.Store, manifest domain.AdaptationSourceManifest) {
	t.Helper()
	specs := store.AdaptationDossierBatchSpecs(manifest, CoCreateDossierBatchSize, CoCreateDossierBatchRuneLimit)
	batches := make([]domain.AdaptationCoCreateDossierBatch, 0, len(specs))
	for _, spec := range specs {
		batches = append(batches, domain.AdaptationCoCreateDossierBatch{
			Index:            spec.Index,
			SourceFrom:       spec.SourceFrom,
			SourceTo:         spec.SourceTo,
			SourceSignature:  spec.SourceSignature,
			PromptVersion:    CoCreateDossierPromptVersion,
			GeneratedAt:      "2026-07-05T00:00:00Z",
			PlotPhase:        fmt.Sprintf("source range %d-%d", spec.SourceFrom, spec.SourceTo),
			KeyCausality:     []string{"source causality stays anchored"},
			PlotThreads:      []string{"main source thread continues"},
			CharacterArcs:    []string{"Ari changes through the source arc"},
			WorldConstraints: []string{"source world rules remain binding"},
			MajorCharacters:  []string{"Ari"},
		})
	}
	sourceChapters := make([]domain.AdaptationDossierSourceSignature, 0, len(manifest.Chapters))
	for _, source := range manifest.Chapters {
		sourceChapters = append(sourceChapters, domain.AdaptationDossierSourceSignature{Chapter: source.Chapter, SHA256: source.SHA256})
	}
	if err := st.Adaptation.SaveCoCreateDossier(domain.AdaptationCoCreateDossier{
		Version:            coCreateDossierVersion,
		PromptVersion:      CoCreateDossierPromptVersion,
		SourcePath:         manifest.SourcePath,
		SourceChapterCount: manifest.ChapterCount,
		SourceSignature:    store.AdaptationSourceSignature(manifest),
		BatchSize:          CoCreateDossierBatchSize,
		BatchRuneLimit:     CoCreateDossierBatchRuneLimit,
		GeneratedAt:        "2026-07-05T00:00:00Z",
		Overview:           "test dossier",
		Mainline:           []string{"test mainline"},
		Batches:            batches,
		SourceChapters:     sourceChapters,
	}); err != nil {
		t.Fatalf("SaveCoCreateDossier: %v", err)
	}
}

func testSourceFoundation() domain.AdaptationSourceFoundation {
	return domain.AdaptationSourceFoundation{
		Premise: "# Source Book\n\nA compact source premise.",
		Characters: []domain.Character{
			{Name: "Ari", Role: "lead", Description: "keeps the plot moving", Arc: "chooses courage", Traits: []string{"focused"}},
		},
		WorldRules: []domain.WorldRule{
			{Category: "society", Rule: "No supernatural shortcuts", Boundary: "events stay grounded"},
		},
		Volumes: []domain.VolumeOutline{
			{
				Index: 1,
				Title: "Volume One",
				Theme: "Ari chooses the road",
				Arcs: []domain.ArcOutline{
					{
						Index: 1,
						Title: "Call",
						Goal:  "Ari commits",
						Chapters: []domain.OutlineEntry{
							{Title: "Opening", CoreEvent: "Ari accepts the call", Hook: "a promise is made", Scenes: []string{"station"}},
						},
					},
				},
			},
		},
		Compass: &domain.StoryCompass{
			EndingDirection: "Ari keeps the promise",
			OpenThreads:     []string{"who sent the call"},
			EstimatedScale:  "1 chapter",
		},
	}
}
