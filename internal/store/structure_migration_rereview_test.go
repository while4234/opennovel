package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestContinuationCommitRejectsActiveRevisionBeforePendingMigrationRecovery(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	impact, err := domain.NewRevisionImpact("continuation ownership", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "outline", Change: "revise",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Revisions.Start(fakeRevisionPolicy{}, StartRevisionInput{
		Intent: "active adaptation revision", Impact: impact, IdempotencyKey: "continuation-owner",
	}); err != nil {
		t.Fatal(err)
	}
	migrationLog := filepath.Join(dir, filepath.FromSlash(structureMigrationLogFile))
	if err := os.MkdirAll(filepath.Dir(migrationLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(migrationLog, []byte(`{"version":1,"stage":"planned"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before := continuationOwnershipProjectBytes(t, dir)
	if _, err := st.CommitContinuationPlan(1); !errors.Is(err, ErrActiveRevisionBlocksNormalFlow) {
		t.Fatalf("continuation commit error = %v", err)
	}
	if after := continuationOwnershipProjectBytes(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected continuation commit recovered or changed pending structure bytes")
	}
}

func continuationOwnershipProjectBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".lock") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIndexedExpansionAndAppendUseStructureMigration(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Title: "one",
		Theme: "start",
		Arcs: []domain.ArcOutline{{
			Index:             1,
			Title:             "first",
			Goal:              "open",
			EstimatedChapters: 1,
		}},
	}}); err != nil {
		t.Fatalf("save indexed skeleton: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{Layered: true}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	if err := st.ExpandArc(1, 1, []domain.OutlineEntry{{Title: "expanded", CoreEvent: "event", Hook: "hook"}}); err != nil {
		t.Fatalf("expand indexed arc: %v", err)
	}
	if err := st.AppendVolume(domain.VolumeOutline{
		Index: 2,
		Title: "two",
		Theme: "rise",
		Arcs: []domain.ArcOutline{{
			Index: 1,
			Title: "second",
			Goal:  "advance",
			Chapters: []domain.OutlineEntry{{
				Title: "appended", CoreEvent: "next", Hook: "next hook",
			}},
		}},
	}); err != nil {
		t.Fatalf("append indexed volume: %v", err)
	}
	if err := st.AppendSkeletonVolume(domain.VolumeOutline{
		Index: 3,
		Title: "three",
		Theme: "future",
		Arcs:  []domain.ArcOutline{{Index: 1, Title: "third", Goal: "later", EstimatedChapters: 1}},
	}); err != nil {
		t.Fatalf("append indexed skeleton volume: %v", err)
	}

	index, ok, err := st.Outline.migration.loadIndex()
	if err != nil || !ok {
		t.Fatalf("load structure index: ok=%t err=%v", ok, err)
	}
	if len(index.Volumes) != 3 || len(index.Arcs) != 3 || len(index.Chapters) != 2 {
		t.Fatalf("mutation did not update complete index: %+v", index)
	}
	for _, ref := range index.Chapters {
		if ref.ID == "" || ref.VolumeID == "" || ref.ArcID == "" {
			t.Fatalf("new chapter is not authoritative in index: %+v", ref)
		}
	}
	if err := st.Drafts.SaveFinalChapter(2, "appended body"); err != nil {
		t.Fatalf("save appended body: %v", err)
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("load expanded structure: %v", err)
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("repeat structural save: %v", err)
	}
	if body, err := st.Drafts.LoadChapterText(2); err != nil || body != "appended body" {
		t.Fatalf("canonicalized appended body was lost: body=%q err=%v", body, err)
	}
}

func TestStructureReadRecoversIncompleteSwitchBeforeExposure(t *testing.T) {
	dir, reordered := legacyLayeredMigrationFixture(t)
	st := NewStore(dir)
	failed := false
	st.Outline.migration.failpoint = func(point string) error {
		if point == migrationFailDuringProjection && !failed {
			failed = true
			return errors.New("interrupt projection")
		}
		return nil
	}
	if err := st.Outline.SaveLayeredOutline(reordered); err == nil {
		t.Fatal("migration unexpectedly completed")
	}
	loaded, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("structure read did not recover migration: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Title != "two" || loaded[1].Title != "one" {
		t.Fatalf("read exposed mixed structure: %+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(structureMigrationLogFile))); !os.IsNotExist(err) {
		t.Fatalf("recovered read left migration log: %v", err)
	}
}

func TestStructureMigrationRecoversRemovalAndIndexWindows(t *testing.T) {
	points := []string{
		migrationFailDuringRemoval,
		migrationFailBeforeIndexWrite,
		migrationFailAfterIndexWrite,
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			if err := st.Outline.SaveOutline([]domain.OutlineEntry{
				{Chapter: 1, Title: "one"},
				{Chapter: 2, Title: "two"},
			}); err != nil {
				t.Fatalf("save source outline: %v", err)
			}
			failed := false
			st.Outline.migration.failpoint = func(current string) error {
				if current == point && !failed {
					failed = true
					return errors.New("interrupt switch")
				}
				return nil
			}
			if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "one"}}); err == nil {
				t.Fatalf("migration unexpectedly completed at %s", point)
			}
			loaded, err := st.Outline.LoadOutline()
			if err != nil {
				t.Fatalf("recover %s before read: %v", point, err)
			}
			if len(loaded) != 1 || loaded[0].Title != "one" {
				t.Fatalf("recovered outline at %s = %+v", point, loaded)
			}
			index, ok, err := st.Outline.migration.loadIndex()
			if err != nil || !ok || len(index.Chapters) != 1 {
				t.Fatalf("recovered index at %s: index=%+v ok=%t err=%v", point, index, ok, err)
			}
		})
	}
}

func TestInterruptedFileReplacementIsRecoveredBeforeRead(t *testing.T) {
	dir := t.TempDir()
	io := newIO(dir)
	target := filepath.Join(dir, "state.json")
	backup := target + ".replace-backup"
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".tmp-orphan", []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadFile("state.json")
	if err != nil || string(data) != "old" {
		t.Fatalf("missing-target window was not restored: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("restored replacement left backup: %v", err)
	}

	if err := os.WriteFile(backup, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadFile("state.json")
	if err != nil || string(data) != "new" {
		t.Fatalf("installed-target window was not preserved: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("completed replacement left backup: %v", err)
	}
}

func TestMigrationOperationIdentityDistinguishesExactAndDifferentRetries(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		st, index := indexedOutlineFixture(t)
		payload := migrationPayload{Rel: "meta/operation-result.txt", Data: []byte("exact")}
		st.Outline.migration.failpoint = func(point string) error {
			if point == migrationFailAfterValidate {
				return errors.New("interrupt exact request")
			}
			return nil
		}
		if err := st.Outline.migration.save("same_kind", index, index, []migrationPayload{payload}); err == nil {
			t.Fatal("first exact request unexpectedly completed")
		}
		st.Outline.migration.failpoint = func(point string) error {
			if point == migrationFailAfterWrite {
				return errors.New("exact retry started a second operation")
			}
			return nil
		}
		if err := st.Outline.migration.save("same_kind", index, index, []migrationPayload{payload}); err != nil {
			t.Fatalf("exact retry was not recognized: %v", err)
		}
	})

	t.Run("different", func(t *testing.T) {
		st, index := indexedOutlineFixture(t)
		first := migrationPayload{Rel: "meta/operation-result.txt", Data: []byte("first")}
		second := migrationPayload{Rel: "meta/operation-result.txt", Data: []byte("second")}
		failed := false
		st.Outline.migration.failpoint = func(point string) error {
			if point == migrationFailAfterValidate && !failed {
				failed = true
				return errors.New("interrupt first request")
			}
			return nil
		}
		if err := st.Outline.migration.save("same_kind", index, index, []migrationPayload{first}); err == nil {
			t.Fatal("first different-request setup unexpectedly completed")
		}
		st.Outline.migration.failpoint = nil
		if err := st.Outline.migration.save("same_kind", index, index, []migrationPayload{second}); err != nil {
			t.Fatalf("different same-kind request was discarded: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(st.Dir(), filepath.FromSlash(second.Rel)))
		if err != nil || string(data) != "second" {
			t.Fatalf("different request did not execute: data=%q err=%v", data, err)
		}
	})
}

func TestArcBatchReviewFollowsStableIdentitiesAcrossReorder(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	volumes := []domain.VolumeOutline{
		{Index: 1, Title: "one", Theme: "one", Arcs: []domain.ArcOutline{{Index: 1, Title: "a", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "chapter-one"}}}}},
		{Index: 2, Title: "two", Theme: "two", Arcs: []domain.ArcOutline{{Index: 1, Title: "b", Chapters: []domain.OutlineEntry{{Chapter: 2, Title: "chapter-two"}}}}},
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("save structure: %v", err)
	}
	if err := st.World.SaveReview(domain.ReviewEntry{
		Chapter: 1, Scope: "arc_batch", Volume: 1, Arc: 1, BatchFrom: 1, BatchTo: 1,
		Summary: "stable review", Verdict: "accept", AffectedChapters: []int{1},
	}); err != nil {
		t.Fatalf("save arc-batch review: %v", err)
	}
	loaded, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	loaded[0], loaded[1] = loaded[1], loaded[0]
	if err := st.Outline.SaveLayeredOutline(loaded); err != nil {
		t.Fatalf("reorder structure: %v", err)
	}
	reviews, err := st.World.LoadArcBatchReviews(2, 1)
	if err != nil {
		t.Fatalf("load reordered arc-batch review: %v", err)
	}
	if len(reviews) != 1 || reviews[0].Summary != "stable review" || reviews[0].Chapter != 2 || reviews[0].BatchFrom != 2 || reviews[0].BatchTo != 2 || !reflect.DeepEqual(reviews[0].AffectedChapters, []int{2}) {
		t.Fatalf("arc-batch review did not follow identities: %+v", reviews)
	}
}

func TestCanonicalFactsRemainAuthoritativeOverStaleProjection(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	outline := []domain.OutlineEntry{{Chapter: 1, Title: "one"}, {Chapter: 2, Title: "two"}}
	if err := st.Outline.SaveOutline(outline); err != nil {
		t.Fatalf("save outline: %v", err)
	}
	if err := st.World.SaveTimeline([]domain.TimelineEvent{{Chapter: 1, Event: "canonical-new"}}); err != nil {
		t.Fatalf("save canonical facts: %v", err)
	}
	writeFixtureFile(t, dir, "timeline.json", `[{"chapter":1,"event":"stale-projection"}]`)
	loaded, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatal(err)
	}
	loaded[0], loaded[1] = loaded[1], loaded[0]
	if err := st.Outline.SaveOutline(loaded); err != nil {
		t.Fatalf("reorder with stale fact projection: %v", err)
	}
	timeline, err := st.World.LoadTimeline()
	if err != nil || len(timeline) != 1 || timeline[0].Event != "canonical-new" || timeline[0].Chapter != 2 {
		t.Fatalf("stale projection overrode canonical facts: timeline=%+v err=%v", timeline, err)
	}
	var projected []domain.TimelineEvent
	if err := newIO(dir).ReadJSON("timeline.json", &projected); err != nil {
		t.Fatal(err)
	}
	if len(projected) != 1 || projected[0].Event != "canonical-new" || projected[0].Chapter != 2 {
		t.Fatalf("numeric projection was not rebuilt from canonical facts: %+v", projected)
	}
}

func TestContinuationCommitRecoversThroughStructureTransaction(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	base := []domain.OutlineEntry{
		{Chapter: 1, Title: "one"},
		{Chapter: 2, Title: "two"},
		{Chapter: 3, Title: "three"},
	}
	if err := st.Outline.SaveOutline(base); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		TotalChapters: 3, CurrentChapter: 4, CompletedChapters: []int{1, 2, 3},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Continuation.InitializeSource("source-signature", 3); err != nil {
		t.Fatal(err)
	}
	snapshot := advanceContinuationToOutlineReview(t, st.Continuation)
	blocked := true
	st.Outline.migration.failpoint = func(point string) error {
		if blocked && point == migrationFailDuringProjection {
			return errors.New("interrupt continuation projection")
		}
		return nil
	}
	if _, err := st.CommitContinuationPlan(snapshot.Workflow.Revision); err == nil {
		t.Fatal("interrupted continuation commit unexpectedly succeeded")
	}
	blocked = false
	outline, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("structure read did not recover continuation transaction: %v", err)
	}
	if len(outline) != 5 || outline[3].Chapter != 4 || outline[4].Chapter != 5 {
		t.Fatalf("recovered continuation outline is mixed: %+v", outline)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress.TotalChapters != 5 {
		t.Fatalf("continuation progress was outside transaction: progress=%+v err=%v", progress, err)
	}
	recoveredSnapshot, err := st.Continuation.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if recoveredSnapshot.Workflow.Stage != domain.ContinuationStageReadyToWrite || recoveredSnapshot.Plan == nil {
		t.Fatalf("continuation workflow was outside transaction: %+v", recoveredSnapshot)
	}
	if _, err := st.CommitContinuationPlan(recoveredSnapshot.Workflow.Revision); err != nil {
		t.Fatalf("exact continuation retry after recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(continuationCommitJournalFile))); !os.IsNotExist(err) {
		t.Fatalf("committed continuation journal remains: %v", err)
	}
}

func indexedOutlineFixture(t *testing.T) (*Store, structureIndex) {
	t.Helper()
	st := NewStore(t.TempDir())
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "one"}}); err != nil {
		t.Fatalf("save indexed outline: %v", err)
	}
	index, ok, err := st.Outline.migration.loadIndex()
	if err != nil || !ok {
		t.Fatalf("load indexed outline: ok=%t err=%v", ok, err)
	}
	return st, index
}
