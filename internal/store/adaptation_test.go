package store

import (
	"os"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestAdaptationStoreSavesSourceSnapshotAndChecks(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source, err := s.Adaptation.SaveSourceChapter(1, "初遇", "原文第一章内容。")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if source.SHA256 == "" || source.Path == "" || source.Runes == 0 {
		t.Fatalf("source metadata incomplete: %+v", source)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}

	text, loaded, err := s.Adaptation.LoadSourceChapter(1)
	if err != nil {
		t.Fatalf("LoadSourceChapter: %v", err)
	}
	if text != "原文第一章内容。" {
		t.Fatalf("text=%q", text)
	}
	if loaded == nil || loaded.Title != "初遇" {
		t.Fatalf("source metadata not loaded: %+v", loaded)
	}

	check := domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: TextSHA256("草稿"),
		Passed:      true,
		CheckedAt:   "2026-06-29T00:00:00Z",
	}
	if err := s.Adaptation.SaveCheck(check); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	passed, saved, err := s.Adaptation.HasPassingCheck(1, check.DraftSHA256)
	if err != nil {
		t.Fatalf("HasPassingCheck: %v", err)
	}
	if !passed || saved == nil || saved.DraftSHA256 != check.DraftSHA256 {
		t.Fatalf("passing check mismatch: passed=%v saved=%+v", passed, saved)
	}
}

func TestAdaptationProposalDoesNotActivateProject(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	proposal := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Brief:         "按原著细节逐章改编",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "第一章",
			SourceChapters: []int{1},
		}},
	}
	if err := s.Adaptation.SaveProposal(proposal); err != nil {
		t.Fatalf("SaveProposal: %v", err)
	}
	if s.Adaptation.Active() {
		t.Fatal("proposal-only adaptation should not be active")
	}
	if plan, err := s.Adaptation.LoadPlan(); err != nil || plan != nil {
		t.Fatalf("LoadPlan should ignore proposal: plan=%+v err=%v", plan, err)
	}
	loaded, err := s.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if loaded == nil || loaded.Status != domain.AdaptationPlanStatusProposal {
		t.Fatalf("proposal status mismatch: %+v", loaded)
	}

	if err := s.Adaptation.SavePlan(proposal); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if !s.Adaptation.Active() {
		t.Fatal("confirmed adaptation plan should be active")
	}
	confirmed, err := s.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan confirmed: %v", err)
	}
	if confirmed == nil || confirmed.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("confirmed status mismatch: %+v", confirmed)
	}
}

func TestAdaptationPlanLoadNormalizesLegacyAndNestedWordBudget(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	legacy := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Brief:         "legacy",
		WordTolerance: 0.15,
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Legacy",
			SourceChapters: []int{1},
			SourceRunes:    1000,
			TargetRunes:    1100,
			TargetMinRunes: 900,
			TargetMaxRunes: 1200,
		}},
	}
	if err := s.Adaptation.io.WriteJSON(adaptationPlanFile, legacy); err != nil {
		t.Fatalf("WriteJSON legacy: %v", err)
	}
	loadedLegacy, err := s.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan legacy: %v", err)
	}
	chapter := loadedLegacy.Chapters[0]
	if loadedLegacy.Status != domain.AdaptationPlanStatusConfirmed {
		t.Fatalf("legacy status = %q, want confirmed", loadedLegacy.Status)
	}
	if chapter.WordBudget == nil || chapter.WordBudget.TargetRunes != 1100 || chapter.WordBudget.MinRunes != 900 || chapter.WordBudget.Tolerance != 0.15 {
		t.Fatalf("legacy word budget not mirrored: %+v", chapter.WordBudget)
	}
	if chapter.TargetRunes != 1100 || chapter.TargetMinRunes != 900 || chapter.TargetMaxRunes != 1200 {
		t.Fatalf("legacy target fields changed: %+v", chapter)
	}

	nested := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Status:      domain.AdaptationPlanStatusProposal,
		Brief:       "nested",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter:        1,
			Title:          "Nested",
			SourceChapters: []int{1, 2},
			OutlineEntry: domain.OutlineEntry{
				CoreEvent: "combined event",
				Hook:      "new hook",
				Scenes:    []string{"first", "second"},
			},
			WordBudget: &domain.AdaptationChapterWordBudget{
				SourceRunes: 2000,
				TargetRunes: 2300,
				MinRunes:    2100,
				MaxRunes:    2500,
			},
		}},
	}
	if err := s.Adaptation.io.WriteJSON(adaptationProposalFile, nested); err != nil {
		t.Fatalf("WriteJSON nested: %v", err)
	}
	loadedNested, err := s.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal nested: %v", err)
	}
	nestedChapter := loadedNested.Chapters[0]
	if nestedChapter.TargetRunes != 2300 || nestedChapter.TargetMinRunes != 2100 || nestedChapter.TargetMaxRunes != 2500 {
		t.Fatalf("nested word budget did not backfill legacy fields: %+v", nestedChapter)
	}
	if nestedChapter.CoreEvent != "combined event" || nestedChapter.Hook != "new hook" || len(nestedChapter.Scenes) != 2 {
		t.Fatalf("outline fields not preserved: %+v", nestedChapter.OutlineEntry)
	}
}

func TestAdaptationPlanPersistsSourceDerivedSoftBudgets(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source1, err := s.Adaptation.SaveSourceChapter(1, "One", strings.Repeat("a", 100))
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	source2, err := s.Adaptation.SaveSourceChapter(2, "Two", strings.Repeat("b", 50))
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 2,
		Chapters:     []domain.AdaptationSource{source1, source2},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}

	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityArc,
		Brief:       "soft default budgets",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "One", SourceChapters: []int{1}},
			{Chapter: 2, Title: "Two", SourceChapters: []int{2}},
		},
	}
	if err := s.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	first, err := s.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan first: %v", err)
	}
	reloaded := NewStore(s.Dir())
	second, err := reloaded.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan second: %v", err)
	}

	if first.TargetTotalRunes != 150 || first.TargetMinRunes != 128 || first.TargetMaxRunes != 172 {
		t.Fatalf("plan totals = %+v", first)
	}
	if first.Chapters[0].WordBudget == nil || first.Chapters[0].TargetRunes != 100 ||
		first.Chapters[0].TargetMinRunes != 85 || first.Chapters[0].TargetMaxRunes != 115 {
		t.Fatalf("chapter 1 budget = %+v", first.Chapters[0])
	}
	if first.Chapters[1].WordBudget == nil || first.Chapters[1].TargetRunes != 50 ||
		first.Chapters[1].TargetMinRunes != 43 || first.Chapters[1].TargetMaxRunes != 57 {
		t.Fatalf("chapter 2 budget = %+v", first.Chapters[1])
	}
	if second.TargetTotalRunes != first.TargetTotalRunes ||
		second.TargetMinRunes != first.TargetMinRunes ||
		second.TargetMaxRunes != first.TargetMaxRunes ||
		second.Chapters[0].TargetMinRunes != first.Chapters[0].TargetMinRunes ||
		second.Chapters[1].TargetMaxRunes != first.Chapters[1].TargetMaxRunes {
		t.Fatalf("budgets changed across reload: first=%+v second=%+v", first, second)
	}
}

func TestAdaptationPlanLoadNormalizesSplitFullRewriteBudgets(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source, err := s.Adaptation.SaveSourceChapter(1, "Long", strings.Repeat("a", 16000))
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}

	chapters := make([]domain.AdaptationChapterPlan, 0, 4)
	for chapter := 1; chapter <= 4; chapter++ {
		chapters = append(chapters, domain.AdaptationChapterPlan{
			Chapter:        chapter,
			Title:          "Split",
			SourceChapters: []int{1},
			SourceRange:    domain.SourceRange{From: 1, To: 1},
			SourceRunes:    16000,
			TargetRunes:    16000,
			TargetMinRunes: 14000,
			TargetMaxRunes: 18000,
			WordBudget: &domain.AdaptationChapterWordBudget{
				SourceRunes: 16000,
				TargetRunes: 16000,
				MinRunes:    14000,
				MaxRunes:    18000,
				Tolerance:   0.15,
			},
		})
	}
	legacy := domain.AdaptationPlan{
		Granularity:      domain.AdaptationGranularityArc,
		Status:           domain.AdaptationPlanStatusConfirmed,
		Brief:            "split old budget",
		WordTolerance:    0.15,
		SourceTotalRunes: 64000,
		TargetTotalRunes: 64000,
		TargetMinRunes:   56000,
		TargetMaxRunes:   72000,
		Chapters:         chapters,
	}
	if err := s.Adaptation.io.WriteJSON(adaptationPlanFile, legacy); err != nil {
		t.Fatalf("WriteJSON legacy: %v", err)
	}

	loaded, err := s.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if loaded.SourceTotalRunes != 16000 || loaded.TargetTotalRunes != 16000 ||
		loaded.TargetMinRunes != 13600 || loaded.TargetMaxRunes != 18400 {
		t.Fatalf("totals = %+v", loaded)
	}
	for _, chapter := range loaded.Chapters {
		if chapter.SourceRunes != 4000 || chapter.TargetRunes != 4000 ||
			chapter.TargetMinRunes != 3400 || chapter.TargetMaxRunes != 4600 {
			t.Fatalf("chapter budget not split: %+v", chapter)
		}
		if chapter.WordBudget == nil ||
			chapter.WordBudget.SourceRunes != 4000 ||
			chapter.WordBudget.TargetRunes != 4000 ||
			chapter.WordBudget.MinRunes != 3400 ||
			chapter.WordBudget.MaxRunes != 4600 {
			t.Fatalf("nested budget not split: %+v", chapter.WordBudget)
		}
	}
}

func TestAdaptationStoreSaveProposalClearsProposalRuntime(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:            1,
		Brief:              "runtime draft",
		SourcePath:         "source.txt",
		SourceChapterCount: 1,
		Granularity:        domain.AdaptationGranularityFree,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 1,
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}
	plan := domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityFree,
		RewritePolicy: domain.AdaptationRewriteFullRewrite,
		Brief:         "finished proposal",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "Target One", SourceChapters: []int{1}},
		},
	}
	if err := s.Adaptation.SaveProposal(plan); err != nil {
		t.Fatalf("SaveProposal: %v", err)
	}
	if runtime, err := s.Adaptation.LoadProposalRuntime(); err != nil || runtime != nil {
		t.Fatalf("LoadProposalRuntime after SaveProposal: runtime=%+v err=%v", runtime, err)
	}
	if err := s.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:            1,
		Brief:              "runtime draft",
		SourcePath:         "source.txt",
		SourceChapterCount: 1,
		Granularity:        domain.AdaptationGranularityFree,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 1,
	}); err != nil {
		t.Fatalf("SaveProposalRuntime again: %v", err)
	}
	if err := s.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if runtime, err := s.Adaptation.LoadProposalRuntime(); err != nil || runtime != nil {
		t.Fatalf("LoadProposalRuntime after SavePlan: runtime=%+v err=%v", runtime, err)
	}
}

func TestAdaptationStorePersistsVolumeReviewWithoutChapterDetails(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	review := domain.AdaptationVolumeReview{
		Granularity:        domain.AdaptationGranularityFree,
		Status:             domain.AdaptationPlanStatusVolumeReview,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		Brief:              "review long-form volume skeleton before chapter details",
		TargetChapterCount: 20,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "Opening", TargetFrom: 1, TargetTo: 8, SourceFrom: 1, SourceTo: 2},
			{Index: 2, Title: "Pressure", TargetFrom: 9, TargetTo: 16, SourceFrom: 2, SourceTo: 3},
			{Index: 3, Title: "Payoff", TargetFrom: 17, TargetTo: 20, SourceFrom: 3, SourceTo: 4},
		},
	}
	if err := s.Adaptation.SaveVolumeReview(review); err != nil {
		t.Fatalf("SaveVolumeReview: %v", err)
	}

	loaded, err := s.Adaptation.LoadVolumeReview()
	if err != nil {
		t.Fatalf("LoadVolumeReview: %v", err)
	}
	if loaded == nil {
		t.Fatal("volume review proposal was not saved")
	}
	if loaded.Status != domain.AdaptationPlanStatusVolumeReview {
		t.Fatalf("status=%q, want volume_review", loaded.Status)
	}
	if len(loaded.Volumes) != 3 || loaded.Volumes[1].Title != "Pressure" || loaded.Volumes[2].TargetTo != 20 {
		t.Fatalf("volume skeleton was not preserved: %+v", loaded.Volumes)
	}
	if proposal, err := s.Adaptation.LoadProposal(); err != nil || proposal != nil {
		t.Fatalf("volume review should not save chapter proposal: proposal=%+v err=%v", proposal, err)
	}
	if s.Adaptation.Active() {
		t.Fatal("volume review should not activate adaptation writing")
	}
}

func TestAdaptationStoreResetGeneratedPreservesSourceSnapshot(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source, err := s.Adaptation.SaveSourceChapter(1, "Opening", "source chapter body")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	if err := s.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{
		Premise: "# Source Book\n",
		Characters: []domain.Character{
			{Name: "Ari", Role: "lead", Description: "keeps the plot moving"},
		},
	}); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	if err := s.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{
		{Chapter: 1, Title: "Opening", KeyEvents: []string{"Ari accepts the call"}},
	}); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := s.Adaptation.SavePlan(domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityFree,
		Brief:       "old generated plan",
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "Old", SourceChapters: []int{1}},
		},
	}); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := s.Adaptation.SaveCheck(domain.AdaptationCheck{
		Chapter:     1,
		DraftSHA256: TextSHA256("old draft"),
		Passed:      true,
		CheckedAt:   "2026-06-29T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveCheck: %v", err)
	}
	if err := s.Adaptation.SaveProposalRuntime(domain.AdaptationProposalRuntime{
		Version:            1,
		Brief:              "old generated proposal runtime",
		SourcePath:         "source.txt",
		SourceChapterCount: 1,
		Granularity:        domain.AdaptationGranularityFree,
		RewritePolicy:      domain.AdaptationRewriteFullRewrite,
		TargetChapterCount: 1,
	}); err != nil {
		t.Fatalf("SaveProposalRuntime: %v", err)
	}

	if err := s.Adaptation.ResetGenerated(); err != nil {
		t.Fatalf("ResetGenerated: %v", err)
	}

	if _, err := os.Stat(s.Adaptation.io.path(adaptationPlanFile)); !os.IsNotExist(err) {
		t.Fatalf("plan file should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(s.Adaptation.io.path(adaptationCheckDir)); !os.IsNotExist(err) {
		t.Fatalf("checks directory should be removed, stat err=%v", err)
	}
	if runtime, err := s.Adaptation.LoadProposalRuntime(); err != nil || runtime != nil {
		t.Fatalf("LoadProposalRuntime after reset: runtime=%+v err=%v", runtime, err)
	}
	if plan, err := s.Adaptation.LoadPlan(); err != nil || plan != nil {
		t.Fatalf("LoadPlan after reset: plan=%+v err=%v", plan, err)
	}
	if check, err := s.Adaptation.LoadCheck(1); err != nil || check != nil {
		t.Fatalf("LoadCheck after reset: check=%+v err=%v", check, err)
	}

	manifest, err := s.Adaptation.LoadSourceManifest()
	if err != nil {
		t.Fatalf("LoadSourceManifest: %v", err)
	}
	if manifest == nil || manifest.SourcePath != "source.txt" || manifest.ChapterCount != 1 {
		t.Fatalf("source manifest not preserved: %+v", manifest)
	}
	text, loadedSource, err := s.Adaptation.LoadSourceChapter(1)
	if err != nil {
		t.Fatalf("LoadSourceChapter: %v", err)
	}
	if text != "source chapter body" || loadedSource == nil || loadedSource.Title != "Opening" {
		t.Fatalf("source chapter not preserved: text=%q source=%+v", text, loadedSource)
	}
	foundation, err := s.Adaptation.LoadSourceFoundation()
	if err != nil {
		t.Fatalf("LoadSourceFoundation: %v", err)
	}
	if foundation == nil || foundation.Premise != "# Source Book\n" {
		t.Fatalf("source foundation not preserved: %+v", foundation)
	}
	reports, err := s.Adaptation.LoadSourceReports()
	if err != nil {
		t.Fatalf("LoadSourceReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Title != "Opening" {
		t.Fatalf("source reports not preserved: %+v", reports)
	}
}

func TestAdaptationStoreLoadsSingleChapterReportsBeforeLegacyAggregate(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := s.Adaptation.SaveSourceReports([]domain.AdaptationSourceReport{
		{Chapter: 1, Title: "legacy", Summary: "legacy summary", KeyEvents: []string{"legacy event"}},
	}); err != nil {
		t.Fatalf("SaveSourceReports: %v", err)
	}
	if err := s.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{
		Chapter:      1,
		Title:        "single",
		SourceSHA256: "sha-1",
		Summary:      "single summary",
		KeyEvents:    []string{"single event"},
	}); err != nil {
		t.Fatalf("SaveSourceReport: %v", err)
	}

	reports, err := s.Adaptation.LoadSourceReports()
	if err != nil {
		t.Fatalf("LoadSourceReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Title != "single" {
		t.Fatalf("LoadSourceReports should prefer single files, got %+v", reports)
	}
}

func TestAdaptationStoreSourceFoundationBatchRoundTripAndClear(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	batch := domain.AdaptationSourceFoundationBatch{
		Version:         1,
		Kind:            "reports",
		Level:           0,
		Index:           1,
		SourceFrom:      1,
		SourceTo:        3,
		SourceSignature: "source-sig",
		InputSignature:  "input-sig",
		PromptVersion:   "prompt-v1",
		BatchRuneLimit:  70000,
		Foundation: domain.AdaptationSourceFoundation{
			Premise: "# Partial\n",
			Characters: []domain.Character{
				{Name: "Ari", Role: "lead"},
			},
		},
	}
	if err := s.Adaptation.SaveSourceFoundationBatch(batch); err != nil {
		t.Fatalf("SaveSourceFoundationBatch: %v", err)
	}
	loaded, err := s.Adaptation.LoadSourceFoundationBatch(0, 1)
	if err != nil {
		t.Fatalf("LoadSourceFoundationBatch: %v", err)
	}
	if loaded == nil || loaded.InputSignature != "input-sig" || loaded.Foundation.Premise != "# Partial\n" {
		t.Fatalf("loaded batch mismatch: %+v", loaded)
	}

	if err := s.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{
		Premise: "# Final\n",
		Characters: []domain.Character{
			{Name: "Ari", Role: "lead"},
		},
	}); err != nil {
		t.Fatalf("SaveSourceFoundation: %v", err)
	}
	loaded, err = s.Adaptation.LoadSourceFoundationBatch(0, 1)
	if err != nil {
		t.Fatalf("LoadSourceFoundationBatch after final save: %v", err)
	}
	if loaded != nil {
		t.Fatalf("source foundation batches should be cleared after final save: %+v", loaded)
	}
}

func TestAdaptationStoreLoadCompleteSourceReportsRequiresMatchingSHA(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	source1, err := s.Adaptation.SaveSourceChapter(1, "One", "chapter one")
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	source2, err := s.Adaptation.SaveSourceChapter(2, "Two", "chapter two")
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	if err := s.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 2,
		Chapters:     []domain.AdaptationSource{source1, source2},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}

	if err := s.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{
		Chapter:      1,
		Title:        "One",
		SourceSHA256: source1.SHA256,
		Summary:      "summary one",
		KeyEvents:    []string{"event one"},
	}); err != nil {
		t.Fatalf("SaveSourceReport 1: %v", err)
	}
	if reports, err := s.Adaptation.LoadCompleteSourceReports(); err != nil || reports != nil {
		t.Fatalf("incomplete reports should not load: reports=%+v err=%v", reports, err)
	}

	if err := s.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{
		Chapter:      2,
		Title:        "Two",
		SourceSHA256: "wrong-sha",
		Summary:      "summary two",
		KeyEvents:    []string{"event two"},
	}); err != nil {
		t.Fatalf("SaveSourceReport wrong SHA: %v", err)
	}
	if reports, err := s.Adaptation.LoadCompleteSourceReports(); err != nil || reports != nil {
		t.Fatalf("SHA mismatch should not load: reports=%+v err=%v", reports, err)
	}

	if err := s.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{
		Chapter:      2,
		Title:        "Two",
		SourceSHA256: source2.SHA256,
		Summary:      "summary two",
		KeyEvents:    []string{"event two"},
	}); err != nil {
		t.Fatalf("SaveSourceReport matching SHA: %v", err)
	}
	reports, err := s.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		t.Fatalf("LoadCompleteSourceReports: %v", err)
	}
	if len(reports) != 2 || reports[0].Chapter != 1 || reports[1].Chapter != 2 {
		t.Fatalf("complete reports mismatch: %+v", reports)
	}
}

func TestAdaptationStoreCoCreateDossierCurrentRequiresSourceSignatureAndPromptVersion(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source1, err := s.Adaptation.SaveSourceChapter(1, "One", "chapter one")
	if err != nil {
		t.Fatalf("SaveSourceChapter 1: %v", err)
	}
	source2, err := s.Adaptation.SaveSourceChapter(2, "Two", "chapter two")
	if err != nil {
		t.Fatalf("SaveSourceChapter 2: %v", err)
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 2,
		Chapters:     []domain.AdaptationSource{source1, source2},
	}
	if err := s.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	dossier := domain.AdaptationCoCreateDossier{
		Version:            1,
		PromptVersion:      "v-test",
		SourceChapterCount: 2,
		SourceSignature:    AdaptationSourceSignature(manifest),
		BatchSize:          40,
		Batches: []domain.AdaptationCoCreateDossierBatch{
			{Index: 1, SourceFrom: 1, SourceTo: 2, SourceSignature: AdaptationDossierBatchSpecs(manifest, 40, 0)[0].SourceSignature},
		},
	}
	if err := s.Adaptation.SaveCoCreateDossier(dossier); err != nil {
		t.Fatalf("SaveCoCreateDossier: %v", err)
	}
	if current, err := s.Adaptation.CoCreateDossierCurrent("v-test", 40); err != nil || !current {
		t.Fatalf("dossier should be current: current=%v err=%v", current, err)
	}
	if current, err := s.Adaptation.CoCreateDossierCurrent("v-next", 40); err != nil || current {
		t.Fatalf("prompt version mismatch should be stale: current=%v err=%v", current, err)
	}
	if current, err := s.Adaptation.CoCreateDossierCurrent("v-test", 20); err != nil || current {
		t.Fatalf("batch size mismatch should be stale: current=%v err=%v", current, err)
	}

	changed := manifest
	changed.Chapters[1].SHA256 = "changed"
	if err := s.Adaptation.SaveSourceManifest(changed); err != nil {
		t.Fatalf("SaveSourceManifest changed: %v", err)
	}
	if current, err := s.Adaptation.CoCreateDossierCurrent("v-test", 40); err != nil || current {
		t.Fatalf("source signature mismatch should be stale: current=%v err=%v", current, err)
	}
}

func TestAdaptationDossierBatchSpecsSplitByRuneLimit(t *testing.T) {
	manifest := domain.AdaptationSourceManifest{
		ChapterCount: 5,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, SHA256: "sha-1", Runes: 60},
			{Chapter: 2, SHA256: "sha-2", Runes: 30},
			{Chapter: 3, SHA256: "sha-3", Runes: 80},
			{Chapter: 4, SHA256: "sha-4", Runes: 20},
			{Chapter: 5, SHA256: "sha-5", Runes: 10},
		},
	}
	specs := AdaptationDossierBatchSpecs(manifest, 40, 100)
	if len(specs) != 3 {
		t.Fatalf("specs=%+v, want 3 dynamic batches", specs)
	}
	wantRanges := [][2]int{{1, 2}, {3, 4}, {5, 5}}
	for i, want := range wantRanges {
		if specs[i].Index != i+1 || specs[i].SourceFrom != want[0] || specs[i].SourceTo != want[1] {
			t.Fatalf("spec %d=%+v, want range %d-%d", i, specs[i], want[0], want[1])
		}
	}
}

func TestAdaptationDossierBatchSpecsKeepsOversizedChapterWhole(t *testing.T) {
	manifest := domain.AdaptationSourceManifest{
		ChapterCount: 3,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, SHA256: "sha-1", Runes: 12000},
			{Chapter: 2, SHA256: "sha-2", Runes: 51000},
			{Chapter: 3, SHA256: "sha-3", Runes: 9000},
		},
	}

	specs := AdaptationDossierBatchSpecs(manifest, 40, 40000)
	if len(specs) != 3 {
		t.Fatalf("specs=%+v, want the oversized chapter isolated between adjacent batches", specs)
	}
	for index, spec := range specs {
		chapter := index + 1
		if spec.SourceFrom != chapter || spec.SourceTo != chapter {
			t.Fatalf("spec %d=%+v, want whole chapter %d without mid-chapter splitting", index, spec, chapter)
		}
	}
}

func TestCoCreateDossierMatchesManifestRequiresDynamicBatchRanges(t *testing.T) {
	manifest := domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 5,
		Chapters: []domain.AdaptationSource{
			{Chapter: 1, SHA256: "sha-1", Runes: 60},
			{Chapter: 2, SHA256: "sha-2", Runes: 30},
			{Chapter: 3, SHA256: "sha-3", Runes: 80},
			{Chapter: 4, SHA256: "sha-4", Runes: 20},
			{Chapter: 5, SHA256: "sha-5", Runes: 10},
		},
	}
	specs := AdaptationDossierBatchSpecs(manifest, 40, 100)
	batches := make([]domain.AdaptationCoCreateDossierBatch, 0, len(specs))
	for _, spec := range specs {
		batches = append(batches, domain.AdaptationCoCreateDossierBatch{
			Index:           spec.Index,
			SourceFrom:      spec.SourceFrom,
			SourceTo:        spec.SourceTo,
			SourceSignature: spec.SourceSignature,
		})
	}
	dossier := domain.AdaptationCoCreateDossier{
		Version:            1,
		PromptVersion:      "v-test",
		SourceChapterCount: manifest.ChapterCount,
		SourceSignature:    AdaptationSourceSignature(manifest),
		BatchSize:          40,
		BatchRuneLimit:     100,
		Batches:            batches,
	}
	if !CoCreateDossierMatchesManifest(dossier, manifest, "v-test", 40, 100) {
		t.Fatalf("dossier should match dynamic batch ranges")
	}
	missingLimit := dossier
	missingLimit.BatchRuneLimit = 0
	if CoCreateDossierMatchesManifest(missingLimit, manifest, "v-test", 40, 100) {
		t.Fatalf("dossier without matching rune limit should be stale")
	}
	wrongRange := dossier
	wrongRange.Batches[0].SourceTo = 3
	if CoCreateDossierMatchesManifest(wrongRange, manifest, "v-test", 40, 100) {
		t.Fatalf("dossier with wrong dynamic range should be stale")
	}
}
