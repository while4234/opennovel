package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestRollbackConfirmedAdaptationRestoresProposalAndDeletesWritingArtifacts(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "source", "source text")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath:   "source.txt",
		ChapterCount: 1,
		Chapters:     []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	plan := rollbackTestAdaptationPlan()
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if err := st.Outline.SavePremise("# Adapted\n\nconfirmed foundation"); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "Old outline"}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "final chapter"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseComplete,
		CurrentChapter:    2,
		TotalChapters:     1,
		CompletedChapters: []int{1},
		TotalWordCount:    12,
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback || preview.TargetStage != domain.RollbackStageProposal {
		t.Fatalf("preview = %+v, want proposal target", preview)
	}
	result, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !slices.Contains(result.DeletedPaths, "chapters") {
		t.Fatalf("deleted paths = %v, want chapters removed", result.DeletedPaths)
	}
	if plan, err := st.Adaptation.LoadPlan(); err != nil || plan != nil {
		t.Fatalf("confirmed plan should be removed, plan=%+v err=%v", plan, err)
	}
	proposal, err := st.Adaptation.LoadProposal()
	if err != nil {
		t.Fatalf("LoadProposal: %v", err)
	}
	if proposal == nil || proposal.Status != domain.AdaptationPlanStatusProposal || len(proposal.Chapters) != 1 {
		t.Fatalf("proposal not restored: %+v", proposal)
	}
	if text, err := st.Drafts.LoadChapterText(1); err != nil || text != "" {
		t.Fatalf("chapter text should be deleted, text=%q err=%v", text, err)
	}
	if premise, err := st.Outline.LoadPremise(); err != nil || premise != "" {
		t.Fatalf("adaptation foundation should be deleted, premise=%q err=%v", premise, err)
	}
	if manifest, err := st.Adaptation.LoadSourceManifest(); err != nil || manifest == nil {
		t.Fatalf("source manifest should be preserved, manifest=%+v err=%v", manifest, err)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if progress == nil || progress.Phase != domain.PhaseOutline || progress.CompletedChapters != nil {
		t.Fatalf("progress after rollback = %+v", progress)
	}
	next, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("second RollbackPreview: %v", err)
	}
	if next.TargetStage != domain.RollbackStageDraft {
		t.Fatalf("unvolumed proposal target = %q, want draft", next.TargetStage)
	}
}

func TestRollbackWritingClearsGenerationSessionsAndPreservesCoCreate(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter:   1,
		Title:     "Canonical chapter",
		CoreEvent: "The approved cast carries out the planned event.",
		Scenes:    []string{"The protagonist enters the approved opening scene."},
	}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{
		Phase:             domain.PhaseWriting,
		CurrentChapter:    2,
		InProgressChapter: 2,
		TotalChapters:     2,
		CompletedChapters: []int{1},
	}); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	coCreatePath := filepath.Join(dir, "meta", "sessions", "cocreate.jsonl")
	coCreate := []byte("{\"role\":\"user\",\"content\":\"preserve this creative decision\"}\n")
	if err := os.WriteFile(coCreatePath, coCreate, 0o644); err != nil {
		t.Fatalf("write co-create session: %v", err)
	}
	coordinatorPath := filepath.Join(dir, "meta", "sessions", "coordinator.jsonl")
	if err := os.WriteFile(coordinatorPath, []byte("stale coordinator history\n"), 0o644); err != nil {
		t.Fatalf("write coordinator session: %v", err)
	}
	agentDir := filepath.Join(dir, "meta", "sessions", "agents")
	if err := os.WriteFile(filepath.Join(agentDir, "writer-ch01.jsonl"), []byte("stale writer history\n"), 0o644); err != nil {
		t.Fatalf("write writer session: %v", err)
	}

	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback || preview.TargetStage != domain.RollbackStageChapterOutline {
		t.Fatalf("preview = %+v, want chapter-outline target", preview)
	}
	for _, rel := range []string{"meta/sessions/coordinator.jsonl", "meta/sessions/agents"} {
		if !slices.Contains(preview.DeletePaths, rel) {
			t.Fatalf("preview delete paths = %v, want %s", preview.DeletePaths, rel)
		}
	}
	if slices.Contains(preview.DeletePaths, "meta/sessions/cocreate.jsonl") {
		t.Fatalf("preview must preserve co-create history: %v", preview.DeletePaths)
	}

	result, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := os.Stat(coordinatorPath); !os.IsNotExist(err) {
		t.Fatalf("coordinator session remains after rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "writer-ch01.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("writer session remains after rollback: %v", err)
	}
	agentEntries, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatalf("read recreated agent session directory: %v", err)
	}
	if len(agentEntries) != 0 {
		t.Fatalf("recreated agent session directory is not empty: %v", agentEntries)
	}
	for _, rel := range []string{"meta/sessions/coordinator.jsonl", "meta/sessions/agents"} {
		if !slices.Contains(result.DeletedPaths, rel) {
			t.Fatalf("deleted paths = %v, want %s", result.DeletedPaths, rel)
		}
	}
	got, err := os.ReadFile(coCreatePath)
	if err != nil {
		t.Fatalf("read preserved co-create session: %v", err)
	}
	if string(got) != string(coCreate) {
		t.Fatalf("co-create session changed during rollback: got %q want %q", got, coCreate)
	}
}

func TestRollbackAdaptationWithVolumesStopsAtVolumeReviewBeforeDraft(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	source, err := st.Adaptation.SaveSourceChapter(1, "source", "source text")
	if err != nil {
		t.Fatalf("SaveSourceChapter: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{
		SourcePath: "source.txt", ChapterCount: 1, Chapters: []domain.AdaptationSource{source},
	}); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}
	plan := rollbackTestAdaptationPlan()
	plan.Volumes = []domain.AdaptationVolumePlan{{
		Index: 1, Title: "Volume 1", TargetFrom: 1, TargetTo: 1, SourceFrom: 1, SourceTo: 1,
	}}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	first := mustRollbackPreview(t, st, domain.RollbackStageProposal)
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: first.PreviewHash}); err != nil {
		t.Fatalf("rollback to proposal: %v", err)
	}
	second := mustRollbackPreview(t, st, domain.RollbackStageVolumeOutline)
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: second.PreviewHash}); err != nil {
		t.Fatalf("rollback to volume outline: %v", err)
	}
	review, err := st.Adaptation.LoadVolumeReview()
	if err != nil || review == nil {
		t.Fatalf("LoadVolumeReview: review=%+v err=%v", review, err)
	}
	if len(review.Volumes) != 1 || review.TargetChapterCount != 1 || review.SourcePath != "source.txt" {
		t.Fatalf("restored volume review = %+v", review)
	}
	third := mustRollbackPreview(t, st, domain.RollbackStageDraft)
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: third.PreviewHash}); err != nil {
		t.Fatalf("rollback to co-create draft: %v", err)
	}
	if review, err := st.Adaptation.LoadVolumeReview(); err != nil || review != nil {
		t.Fatalf("volume review after draft rollback = %+v err=%v", review, err)
	}
	if manifest, err := st.Adaptation.LoadSourceManifest(); err != nil || manifest == nil {
		t.Fatalf("source manifest must survive draft rollback: manifest=%+v err=%v", manifest, err)
	}
	fourth := mustRollbackPreview(t, st, domain.RollbackStageBlank)
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: fourth.PreviewHash}); err != nil {
		t.Fatalf("rollback to blank: %v", err)
	}
	final, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("final RollbackPreview: %v", err)
	}
	if final.CanRollback {
		t.Fatalf("blank project should not roll back: %+v", final)
	}
}

func mustRollbackPreview(t *testing.T, st *Store, target domain.RollbackStage) domain.RollbackPreview {
	t.Helper()
	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback || preview.TargetStage != target {
		t.Fatalf("preview = %+v, want target %q", preview, target)
	}
	return preview
}

func TestRollbackNormalChapterOutlineCollapsesLayeredOutlineToVolumeSkeleton(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	layered := []domain.VolumeOutline{{
		Index: 1,
		Title: "Volume 1",
		Theme: "test",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "Arc 1",
			Goal:  "open",
			Chapters: []domain.OutlineEntry{
				{Chapter: 1, Title: "A"},
				{Chapter: 2, Title: "B"},
			},
		}},
	}}
	if err := st.Outline.SavePremise("# Story"); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := st.Outline.SaveLayeredOutline(layered); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(layered)); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusPending,
		Kind:   domain.PlanningReviewKindChapterOutline,
		Brief:  "draft",
	}); err != nil {
		t.Fatalf("SetPlanningReview: %v", err)
	}

	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback || preview.TargetStage != domain.RollbackStageVolumeOutline {
		t.Fatalf("preview = %+v, want volume outline target", preview)
	}
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if flat, err := st.Outline.LoadOutline(); err != nil || len(flat) != 0 {
		t.Fatalf("flat outline should be deleted, flat=%+v err=%v", flat, err)
	}
	collapsed, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	if len(collapsed) != 1 || len(collapsed[0].Arcs) != 1 {
		t.Fatalf("collapsed outline shape = %+v", collapsed)
	}
	arc := collapsed[0].Arcs[0]
	if len(arc.Chapters) != 0 || arc.EstimatedChapters != 2 {
		t.Fatalf("arc should be skeleton with estimated count, got %+v", arc)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatalf("PlanningReview: %v", err)
	}
	if review == nil || review.Kind != domain.PlanningReviewKindVolumeSplit {
		t.Fatalf("review = %+v, want volume split", review)
	}
}

func TestRollbackSafeRemoveRejectsEscapingPaths(t *testing.T) {
	ioStore := newIO(t.TempDir())
	for _, rel := range []string{"", ".", "..", "../outside", filepath.Join("..", "outside")} {
		if _, err := ioStore.RemoveAllRel(rel); err == nil {
			t.Fatalf("RemoveAllRel(%q) succeeded, want error", rel)
		}
	}
}

func TestRollbackToDraftRemovesCanonicalFoundationAndProjections(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.setPlanningReviewAuthoritative(&domain.PlanningReview{
		Status:                   domain.PlanningReviewStatusPending,
		Kind:                     domain.PlanningReviewKindBlueprint,
		Brief:                    "foundation rollback brief",
		FoundationStatus:         domain.FoundationReviewStatusApproved,
		FoundationRevision:       7,
		FoundationAuditSignature: "stale-audit",
		CoreCastSignature:        "stale-cast",
		FoundationGeneration:     3,
		FoundationBaseRevision:   6,
		FoundationSections:       []string{"premise", "characters", "world_rules", "planned_relationships"},
		FoundationFeedback:       "stale feedback",
		FoundationConfirmedAt:    "2026-07-29T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    "rollback premise",
		Characters: []domain.Character{{Name: "Lin"}},
		WorldRules: []domain.WorldRule{{Rule: "No reset"}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanRollback || preview.TargetStage != domain.RollbackStageDraft {
		t.Fatalf("rollback preview = %+v", preview)
	}
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range append([]string{foundationCanonicalFile, foundationManifestFile, foundationJournalFile}, foundationProjectionPaths()...) {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("foundation artifact remains after rollback: %s (%v)", rel, err)
		}
	}
	loaded, err := st.Foundation.Load()
	if err != nil || loaded.Revision != 0 || loaded.Premise != "" {
		t.Fatalf("foundation after rollback = %+v, %v", loaded, err)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil || review == nil {
		t.Fatalf("planning review after rollback = %+v, %v", review, err)
	}
	if review.FoundationStatus != "" || review.FoundationRevision != 0 ||
		review.FoundationAuditSignature != "" || review.CoreCastSignature != "" ||
		review.FoundationGeneration != 0 || review.FoundationBaseRevision != 0 ||
		len(review.FoundationSections) != 0 || review.FoundationFeedback != "" ||
		review.FoundationConfirmedAt != "" {
		t.Fatalf("rollback retained deleted Foundation binding: %+v", review)
	}
}

func TestRollbackSerializesWithConcurrentFoundationSave(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if err := store.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusPending,
		Kind:   domain.PlanningReviewKindBlueprint,
		Brief:  "concurrent rollback brief",
	}); err != nil {
		t.Fatal(err)
	}
	base, err := store.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    "base premise",
		Characters: []domain.Character{{Name: "Lin"}},
		WorldRules: []domain.WorldRule{{Rule: "No reset"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.RollbackPreview()
	if err != nil {
		t.Fatal(err)
	}

	savePaused := make(chan struct{})
	releaseSave := make(chan struct{})
	var pauseOnce sync.Once
	store.Foundation.failpoint = func(stage string) error {
		if stage == foundationFailAfterJournal {
			pauseOnce.Do(func() { close(savePaused) })
			<-releaseSave
		}
		return nil
	}
	saveDone := make(chan error, 1)
	go func() {
		candidate := domain.CloneStoryFoundation(base)
		candidate.Premise = "concurrent candidate"
		_, saveErr := store.Foundation.SaveCAS(candidate, base.Revision)
		saveDone <- saveErr
	}()
	select {
	case <-savePaused:
	case <-time.After(5 * time.Second):
		t.Fatal("foundation save did not reach journal barrier")
	}

	rollbackStarted := make(chan struct{})
	var rollbackStartedOnce sync.Once
	store.Foundation.lifecycleHook = func(stage string) {
		if stage == foundationLifecycleRollbackStarted {
			rollbackStartedOnce.Do(func() { close(rollbackStarted) })
		}
	}
	rollbackDone := make(chan error, 1)
	go func() {
		_, rollbackErr := store.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash})
		rollbackDone <- rollbackErr
	}()
	select {
	case <-rollbackStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("rollback did not enter the foundation lifecycle")
	}
	close(releaseSave)
	if err := <-saveDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-rollbackDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rollback deadlocked with foundation save")
	}

	for _, rel := range append([]string{foundationCanonicalFile, foundationManifestFile, foundationJournalFile, foundationStageDir}, foundationProjectionPaths()...) {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("foundation artifact remains after concurrent save and rollback: %s (%v)", rel, err)
		}
	}
}

func TestRollbackFirstRejectsFoundationSaveCASEnteredBeforeRollback(t *testing.T) {
	store, dir, base, preview := newFoundationRollbackFixture(t)
	mutationEntered := make(chan struct{})
	mutationMayLock := make(chan struct{})
	rollbackLocked := make(chan struct{})
	releaseRollback := make(chan struct{})
	var mutationOnce sync.Once
	var rollbackOnce sync.Once
	store.Foundation.lifecycleHook = func(stage string) {
		switch stage {
		case foundationLifecycleMutationEntered:
			mutationOnce.Do(func() { close(mutationEntered) })
			<-mutationMayLock
		case foundationLifecycleRollbackLocked:
			rollbackOnce.Do(func() { close(rollbackLocked) })
			<-releaseRollback
		}
	}

	saveDone := make(chan error, 1)
	go func() {
		candidate := domain.CloneStoryFoundation(base)
		candidate.Premise = "queued before rollback"
		_, saveErr := store.Foundation.SaveCAS(candidate, base.Revision)
		saveDone <- saveErr
	}()
	waitFoundationBarrier(t, mutationEntered, "foundation save did not capture its pre-rollback epoch")

	rollbackDone := make(chan error, 1)
	go func() {
		_, rollbackErr := store.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash})
		rollbackDone <- rollbackErr
	}()
	waitFoundationBarrier(t, rollbackLocked, "rollback did not acquire the foundation lifecycle lock")
	close(mutationMayLock)
	close(releaseRollback)
	if err := <-rollbackDone; err != nil {
		t.Fatal(err)
	}
	assertFoundationLifecycleConflict(t, <-saveDone, false)
	assertFoundationArtifactsAbsent(t, dir)

	store.Foundation.lifecycleHook = nil
	postRollback, err := store.Foundation.SaveCAS(domain.StoryFoundation{Premise: "post rollback"}, 0)
	if err != nil || postRollback.Revision != 1 || postRollback.Premise != "post rollback" {
		t.Fatalf("post-rollback CAS save = %+v, %v", postRollback, err)
	}
}

func TestRollbackFirstRejectsFoundationSectionUpdateEnteredDuringRollback(t *testing.T) {
	store, dir, _, preview := newFoundationRollbackFixture(t)
	rollbackLocked := make(chan struct{})
	releaseRollback := make(chan struct{})
	mutationEntered := make(chan struct{})
	var rollbackOnce sync.Once
	var mutationOnce sync.Once
	store.Foundation.lifecycleHook = func(stage string) {
		switch stage {
		case foundationLifecycleRollbackLocked:
			rollbackOnce.Do(func() { close(rollbackLocked) })
			<-releaseRollback
		case foundationLifecycleMutationEntered:
			mutationOnce.Do(func() { close(mutationEntered) })
		}
	}

	rollbackDone := make(chan error, 1)
	go func() {
		_, rollbackErr := store.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash})
		rollbackDone <- rollbackErr
	}()
	waitFoundationBarrier(t, rollbackLocked, "rollback did not acquire the foundation lifecycle lock")

	updateDone := make(chan error, 1)
	go func() { updateDone <- store.Outline.SavePremise("queued during rollback") }()
	waitFoundationBarrier(t, mutationEntered, "section update did not capture its rollback-active epoch")
	close(releaseRollback)
	if err := <-rollbackDone; err != nil {
		t.Fatal(err)
	}
	assertFoundationLifecycleConflict(t, <-updateDone, true)
	assertFoundationArtifactsAbsent(t, dir)

	store.Foundation.lifecycleHook = nil
	if err := store.Outline.SavePremise("post rollback section"); err != nil {
		t.Fatalf("post-rollback section save: %v", err)
	}
	loaded, err := store.Foundation.Load()
	if err != nil || loaded.Revision != 1 || loaded.Premise != "post rollback section" {
		t.Fatalf("post-rollback foundation = %+v, %v", loaded, err)
	}
}

func newFoundationRollbackFixture(t *testing.T) (*Store, string, domain.StoryFoundation, domain.RollbackPreview) {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if err := store.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: domain.PlanningReviewStatusPending,
		Kind:   domain.PlanningReviewKindBlueprint,
		Brief:  "foundation lifecycle rollback brief",
	}); err != nil {
		t.Fatal(err)
	}
	base, err := store.Foundation.SaveCAS(domain.StoryFoundation{
		Premise:    "base premise",
		Characters: []domain.Character{{Name: "Lin"}},
		WorldRules: []domain.WorldRule{{Rule: "No reset"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.RollbackPreview()
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanRollback || preview.TargetStage != domain.RollbackStageDraft {
		t.Fatalf("rollback preview = %+v", preview)
	}
	return store, dir, base, preview
}

func waitFoundationBarrier(t *testing.T, barrier <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-barrier:
	case <-time.After(5 * time.Second):
		t.Fatal(failure)
	}
}

func assertFoundationLifecycleConflict(t *testing.T, err error, startedDuringRollback bool) {
	t.Helper()
	var conflict *FoundationLifecycleConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("foundation mutation error = %T %v, want lifecycle conflict", err, err)
	}
	if conflict.StartedDuringRollback != startedDuringRollback {
		t.Fatalf("lifecycle conflict = %+v, started during rollback want %t", conflict, startedDuringRollback)
	}
	if startedDuringRollback {
		if conflict.StartedEpoch%2 == 0 {
			t.Fatalf("rollback-active mutation generation = %+v, want odd generation", conflict)
		}
		return
	}
	if conflict.StartedEpoch%2 != 0 {
		t.Fatalf("pre-rollback mutation generation = %+v, want even generation", conflict)
	}
	if conflict.StartedEpoch == conflict.CurrentEpoch {
		t.Fatalf("lifecycle conflict epochs = %+v, want advanced epoch", conflict)
	}
}

func assertFoundationArtifactsAbsent(t *testing.T, dir string) {
	t.Helper()
	for _, rel := range append([]string{foundationCanonicalFile, foundationManifestFile, foundationJournalFile, foundationStageDir}, foundationProjectionPaths()...) {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("foundation artifact remains after rollback: %s (%v)", rel, err)
		}
	}
}

func TestRollbackCompletedProjectFixtureDoesNotDeleteOutsideWhitelist(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AINOVEL_ROLLBACK_FIXTURE_OUTPUT"))
	if fixture == "" {
		t.Skip("set AINOVEL_ROLLBACK_FIXTURE_OUTPUT to a completed project output directory to run this safety test")
	}
	info, err := os.Stat(fixture)
	if err != nil || !info.IsDir() {
		t.Fatalf("fixture output directory is invalid: %s err=%v", fixture, err)
	}

	sandbox := filepath.Join(t.TempDir(), "output")
	if err := copyDir(fixture, sandbox); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	before, err := fileSet(sandbox)
	if err != nil {
		t.Fatalf("scan before: %v", err)
	}
	st := NewStore(sandbox)
	preview, err := st.RollbackPreview()
	if err != nil {
		t.Fatalf("RollbackPreview: %v", err)
	}
	if !preview.CanRollback {
		t.Fatalf("fixture cannot roll back: %+v", preview)
	}
	if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	after, err := fileSet(sandbox)
	if err != nil {
		t.Fatalf("scan after: %v", err)
	}
	for rel := range before {
		if after[rel] {
			continue
		}
		if !rollbackFixtureDeletionAllowed(rel) {
			t.Fatalf("fixture rollback deleted unexpected file: %s", rel)
		}
	}
}

func rollbackTestAdaptationPlan() domain.AdaptationPlan {
	return domain.AdaptationPlan{
		Granularity:   domain.AdaptationGranularityChapter,
		RewritePolicy: domain.AdaptationRewritePreserveDetails,
		Brief:         "adapt the source",
		Chapters: []domain.AdaptationChapterPlan{{
			Chapter: 1,
			Title:   "Target",
			OutlineEntry: domain.OutlineEntry{
				Chapter:   1,
				Title:     "Target",
				CoreEvent: "target event",
			},
		}},
	}
}

func rollbackFixtureDeletionAllowed(rel string) bool {
	rel = filepath.ToSlash(rel)
	allowedPrefixes := []string{
		"chapters/",
		"drafts/",
		"summaries/",
		"reviews/",
		"meta/runtime/",
		"meta/adaptation/checks/",
		"meta/snapshots/",
	}
	allowedExact := map[string]bool{
		"premise.md":                                  true,
		"story_foundation.json":                       true,
		"planned_relationships.json":                  true,
		"planned_relationships.md":                    true,
		"meta/foundation":                             true,
		"outline.json":                                true,
		"outline.md":                                  true,
		"layered_outline.json":                        true,
		"layered_outline.md":                          true,
		"characters.json":                             true,
		"characters.md":                               true,
		"world_rules.json":                            true,
		"world_rules.md":                              true,
		"timeline.json":                               true,
		"timeline.md":                                 true,
		"foreshadow_ledger.json":                      true,
		"foreshadow_ledger.md":                        true,
		"relationship_state.json":                     true,
		"relationship_state.md":                       true,
		"meta/progress.json":                          true,
		"meta/checkpoints.jsonl":                      true,
		"meta/state_changes.json":                     true,
		"meta/cast_ledger.json":                       true,
		"meta/last_commit.json":                       true,
		"meta/pending_commit.json":                    true,
		"meta/last_review.json":                       true,
		"meta/compass.json":                           true,
		"meta/outline_duplicate_scan.json":            true,
		"meta/outline_repair_finalization.json":       true,
		"meta/adaptation/plan.json":                   true,
		"meta/adaptation/proposal.json":               true,
		"meta/adaptation/proposal_runtime.json":       true,
		"meta/adaptation/proposal_volume_review.json": true,
	}
	if allowedExact[rel] {
		return true
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

func copyDir(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, dst, info.Mode())
	})
}

func copyFile(source, target string, mode os.FileMode) error {
	srcFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = srcFile.Close() }()

	dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dstFile, srcFile)
	closeErr := dstFile.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func fileSet(root string) (map[string]bool, error) {
	files := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = true
		return nil
	})
	return files, err
}
