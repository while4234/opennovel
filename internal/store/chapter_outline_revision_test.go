package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestReviseFlatChapterOutlineUpdatesFutureChapterOnly(t *testing.T) {
	store := setupFlatOutlineRevisionStore(t)
	if err := store.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		CurrentChapter:    2,
		TotalChapters:     3,
		CompletedChapters: []int{1},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	revised := testRevisedOutline(3)
	if err := store.ReviseChapterOutline(3, revised); err != nil {
		t.Fatalf("ReviseChapterOutline: %v", err)
	}
	got, err := store.Outline.GetChapterOutline(3)
	if err != nil {
		t.Fatalf("GetChapterOutline: %v", err)
	}
	if got.Title != revised.Title || got.CoreEvent != revised.CoreEvent || len(got.Scenes) != 2 {
		t.Fatalf("revised outline = %+v, want %+v", got, revised)
	}
	unchanged, _ := store.Outline.GetChapterOutline(2)
	if unchanged.Title != testOriginalOutline(2).Title {
		t.Fatalf("unrelated outline changed: %+v", unchanged)
	}
	progress, _ := store.Progress.Load()
	if len(progress.PendingRewrites) != 0 || progress.Flow != domain.FlowWriting {
		t.Fatalf("future revision should not queue rewrite: %+v", progress)
	}
}

func TestReviseChapterOutlineRejectsNoChangeWithoutInvalidatingArtifacts(t *testing.T) {
	store := setupFlatOutlineRevisionStore(t)
	if err := store.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		CurrentChapter:    2,
		TotalChapters:     3,
		CompletedChapters: []int{1},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := store.Drafts.SaveFinalChapter(1, "keep this final"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}

	err := store.ReviseChapterOutline(1, testOriginalOutline(1))
	if err == nil {
		t.Fatal("expected unchanged outline to be rejected")
	}
	if final, _ := store.Drafts.LoadChapterText(1); final != "keep this final" {
		t.Fatalf("unchanged revision invalidated final chapter: %q", final)
	}
	progress, _ := store.Progress.Load()
	if len(progress.PendingRewrites) != 0 || progress.Flow != domain.FlowWriting {
		t.Fatalf("unchanged revision mutated progress: %+v", progress)
	}
}

func TestReviseFlatChapterOutlineResetsInProgressDraft(t *testing.T) {
	store := setupFlatOutlineRevisionStore(t)
	if err := store.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		CurrentChapter:    2,
		TotalChapters:     3,
		CompletedChapters: []int{1},
		InProgressChapter: 2,
		CompletedScenes:   []int{1},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := store.Drafts.SaveDraft(2, "old partial draft"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if err := store.Drafts.SaveChapterPlan(domain.ChapterPlan{Chapter: 2}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}

	if err := store.ReviseChapterOutline(2, testRevisedOutline(2)); err != nil {
		t.Fatalf("ReviseChapterOutline: %v", err)
	}
	progress, _ := store.Progress.Load()
	if progress.InProgressChapter != 0 || len(progress.CompletedScenes) != 0 {
		t.Fatalf("in-progress state not reset: %+v", progress)
	}
	if len(progress.PendingRewrites) != 0 {
		t.Fatalf("unfinished chapter must not enter completed rewrite queue: %v", progress.PendingRewrites)
	}
	if draft, _ := store.Drafts.LoadDraft(2); draft != "" {
		t.Fatalf("draft was not cleared: %q", draft)
	}
	if plan, _ := store.Drafts.LoadChapterPlan(2); plan != nil {
		t.Fatalf("chapter plan was not cleared: %+v", plan)
	}
}

func TestReviseFlatCompletedChapterReopensBookAndInvalidatesArtifacts(t *testing.T) {
	store := setupFlatOutlineRevisionStore(t)
	if err := store.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseComplete,
		Flow:              domain.FlowWriting,
		CurrentChapter:    4,
		TotalChapters:     3,
		CompletedChapters: []int{1, 2, 3},
		TotalWordCount:    600,
		ChapterWordCounts: map[int]int{1: 100, 2: 200, 3: 300},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := store.Drafts.SaveFinalChapter(2, "old final"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := store.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 2, Summary: "old summary"}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	if err := store.ReviseChapterOutline(2, testRevisedOutline(2)); err != nil {
		t.Fatalf("ReviseChapterOutline: %v", err)
	}
	progress, _ := store.Progress.Load()
	if progress.Phase != domain.PhaseWriting || !progress.ReopenedFromComplete || progress.Flow != domain.FlowRewriting {
		t.Fatalf("completed book was not reopened for rewriting: %+v", progress)
	}
	if len(progress.PendingRewrites) != 1 || progress.PendingRewrites[0] != 2 {
		t.Fatalf("pending rewrites = %v, want [2]", progress.PendingRewrites)
	}
	if progress.TotalWordCount != 400 {
		t.Fatalf("total word count = %d, want 400", progress.TotalWordCount)
	}
	if _, ok := progress.ChapterWordCounts[2]; ok {
		t.Fatalf("old chapter word count was not cleared: %v", progress.ChapterWordCounts)
	}
	if final, _ := store.Drafts.LoadChapterText(2); final != "" {
		t.Fatalf("old final was not cleared: %q", final)
	}
	if summary, _ := store.Summaries.LoadSummary(2); summary != nil {
		t.Fatalf("old summary was not cleared: %+v", summary)
	}
}

func TestReviseLayeredCompletedChapterQueuesArcPostprocess(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume",
		Theme: "Theme",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "Arc",
			Goal:  "Goal",
			Chapters: []domain.OutlineEntry{
				testOriginalOutline(1),
				testOriginalOutline(2),
			},
		}},
	}}
	store := setupLayered(t, volumes)
	if err := store.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := store.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseComplete,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CurrentChapter:    3,
		TotalChapters:     2,
		CompletedChapters: []int{1, 2},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	if err := store.ReviseChapterOutline(1, testRevisedOutline(1)); err != nil {
		t.Fatalf("ReviseChapterOutline: %v", err)
	}
	progress, _ := store.Progress.Load()
	if progress.Phase != domain.PhaseWriting || !progress.ReopenedFromComplete {
		t.Fatalf("layered completed book was not reopened: %+v", progress)
	}
	if len(progress.PendingRewrites) != 1 || progress.PendingRewrites[0] != 1 {
		t.Fatalf("pending rewrites = %v, want [1]", progress.PendingRewrites)
	}
	if len(progress.PendingArcPost) == 0 || progress.PendingArcPost[0].Volume != 1 || progress.PendingArcPost[0].Arc != 1 {
		t.Fatalf("affected arc was not queued for postprocess: %+v", progress.PendingArcPost)
	}
	layered, _ := store.Outline.GetChapterFromLayered(1)
	flat, _ := store.Outline.GetChapterOutline(1)
	if layered.Title != flat.Title || flat.Title != testRevisedOutline(1).Title {
		t.Fatalf("flat/layered outline projections diverged: layered=%+v flat=%+v", layered, flat)
	}
}

func setupFlatOutlineRevisionStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(t.TempDir())
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	entries := []domain.OutlineEntry{testOriginalOutline(1), testOriginalOutline(2), testOriginalOutline(3)}
	if err := store.Outline.SaveOutline(entries); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	return store
}

func testOriginalOutline(chapter int) domain.OutlineEntry {
	entries := []domain.OutlineEntry{
		{Title: "Harbor Ledger", CoreEvent: "A salt-stained ferry ledger reveals the smuggler's tide schedule.", Hook: "The tide bell rings inside a locked boathouse.", Scenes: []string{"Inspect the abandoned ferry", "Decode the tide marks"}},
		{Title: "Glass Observatory", CoreEvent: "A cracked telescope lens proves the comet signal was forged before sunrise.", Hook: "The repaired lens shows an impossible second moon.", Scenes: []string{"Climb the frozen dome", "Reconstruct the false signal"}},
		{Title: "Market Intercept", CoreEvent: "A forged pass changes hands in the spice market and redirects the pursuit.", Hook: "The ribbon bears the rival family's hidden crest.", Scenes: []string{"Shadow the spice courier", "Recover the exchanged pass"}},
	}
	entry := entries[chapter-1]
	entry.Chapter = chapter
	return entry
}

func testRevisedOutline(chapter int) domain.OutlineEntry {
	return domain.OutlineEntry{
		Chapter:   chapter,
		Title:     "Revised Chapter",
		CoreEvent: "A substantially revised event changes the chapter direction.",
		Hook:      "A new consequence appears.",
		Scenes:    []string{"Revised opening", "Revised confrontation"},
	}
}
