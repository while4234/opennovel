package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const pendingMigrationWriteTimeout = 2 * time.Second

func TestPendingMigrationProgressWriteRecoversAndMutatesExactlyOnce(t *testing.T) {
	st := newPendingMigrationWriteStore(t)
	leavePendingMigrationThroughPublicWrite(t, st)

	recoveryCompletions := 0
	st.Outline.migration.failpoint = func(point string) error {
		if point == migrationFailBeforeLogCleanup {
			recoveryCompletions++
		}
		return nil
	}
	writes := 0
	err := runPendingMigrationWrite(t, func() error {
		return st.Progress.withWriteLock(func() error {
			writes++
			return st.Progress.saveUnlocked(&domain.Progress{NovelName: "recovered", TotalChapters: 2})
		})
	})
	if err != nil {
		t.Fatalf("recover pending migration before progress write: %v", err)
	}
	if recoveryCompletions != 1 || writes != 1 {
		t.Fatalf("recovery completions=%d writes=%d, want exactly one each", recoveryCompletions, writes)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), filepath.FromSlash(structureMigrationLogFile))); !os.IsNotExist(err) {
		t.Fatalf("migration journal remains after progress write: %v", err)
	}

	if err := runPendingMigrationWrite(t, func() error {
		return st.Progress.SetNovelName("second write")
	}); err != nil {
		t.Fatalf("second progress write: %v", err)
	}
	if recoveryCompletions != 1 {
		t.Fatalf("completed migration recovered more than once: %d", recoveryCompletions)
	}
}

func TestPendingMigrationCrossStoreProgressRetry(t *testing.T) {
	st := newPendingMigrationWriteStore(t)
	peer := NewStore(st.Dir())
	leavePendingMigrationThroughPublicWrite(t, st)

	recoveryCompletions := 0
	peer.Outline.migration.failpoint = func(point string) error {
		if point == migrationFailBeforeLogCleanup {
			recoveryCompletions++
		}
		return nil
	}
	if err := runPendingMigrationWrite(t, func() error {
		return peer.Progress.SetTotalChapters(7)
	}); err != nil {
		t.Fatalf("cross-store progress retry: %v", err)
	}
	if recoveryCompletions != 1 {
		t.Fatalf("cross-store recovery completions=%d, want 1", recoveryCompletions)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil || progress.TotalChapters != 7 {
		t.Fatalf("cross-store progress state = %+v, err=%v", progress, err)
	}
}

func TestPendingMigrationAdaptationCheckWritersDoNotReenterRevisionTransaction(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Store) error
		assert func(*testing.T, *Store)
	}{
		{
			name: "save",
			mutate: func(st *Store) error {
				return st.Adaptation.SaveCheck(domain.AdaptationCheck{Chapter: 1, DraftSHA256: "new", Summary: "new"})
			},
			assert: func(t *testing.T, st *Store) {
				check, err := st.Adaptation.LoadCheck(1)
				if err != nil || check == nil || check.Summary != "new" {
					t.Fatalf("saved check = %+v, err=%v", check, err)
				}
			},
		},
		{
			name:   "delete",
			mutate: func(st *Store) error { return st.Adaptation.DeleteCheck(1) },
			assert: func(t *testing.T, st *Store) {
				check, err := st.Adaptation.LoadCheck(1)
				if err != nil || check != nil {
					t.Fatalf("deleted check = %+v, err=%v", check, err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := newPendingMigrationWriteStore(t)
			if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{Chapter: 1, DraftSHA256: "old", Summary: "old"}); err != nil {
				t.Fatal(err)
			}
			leavePendingMigrationThroughPublicWrite(t, st)
			if err := runPendingMigrationWrite(t, func() error { return test.mutate(st) }); err != nil {
				t.Fatalf("%s check with pending migration: %v", test.name, err)
			}
			test.assert(t, st)
		})
	}
}

func TestPendingMigrationResetGeneratedRecoversBeforeDeleting(t *testing.T) {
	st := newPendingMigrationWriteStore(t)
	generated := []string{
		adaptationPlanFile,
		adaptationProposalFile,
		adaptationVolumeReviewFile,
		adaptationProposalRuntimeFile,
		adaptationPlanningWorkflowFile,
	}
	for _, rel := range generated {
		if err := st.Adaptation.io.WriteJSON(rel, map[string]string{"sentinel": rel}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Adaptation.SaveCheck(domain.AdaptationCheck{Chapter: 1, DraftSHA256: "reset", Summary: "reset"}); err != nil {
		t.Fatal(err)
	}
	generated = append(generated, checkRelPath(1))
	index, ok, err := st.Outline.migration.loadIndex()
	if err != nil || !ok || len(index.Chapters) == 0 {
		t.Fatalf("load canonical index: ok=%t err=%v", ok, err)
	}
	canonicalCheck := chapterCanonicalRel(index.Chapters[0].ID, "adaptation-check.json")
	generated = append(generated, canonicalCheck)
	leavePendingMigrationThroughPublicWrite(t, st)

	before := snapshotSelectedFiles(t, st.Dir(), generated)
	failed := false
	st.Outline.migration.failpoint = func(point string) error {
		if point == migrationFailAfterValidate && !failed {
			failed = true
			return errors.New("transient recovery failure")
		}
		return nil
	}
	if err := runPendingMigrationWrite(t, st.Adaptation.ResetGenerated); err == nil || !strings.Contains(err.Error(), "transient recovery failure") {
		t.Fatalf("ResetGenerated recovery failure = %v", err)
	}
	afterFailure := snapshotSelectedFiles(t, st.Dir(), generated)
	if !reflect.DeepEqual(before, afterFailure) {
		t.Fatal("ResetGenerated deleted generated state before migration recovery completed")
	}

	st.Outline.migration.failpoint = nil
	if err := runPendingMigrationWrite(t, st.Adaptation.ResetGenerated); err != nil {
		t.Fatalf("ResetGenerated retry: %v", err)
	}
	for _, rel := range generated {
		if _, err := os.Stat(filepath.Join(st.Dir(), filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("generated artifact %s remains after reset: %v", rel, err)
		}
	}
}

func TestPendingMigrationFormalWritesRejectOwnersByteIdentically(t *testing.T) {
	t.Run("active revision", func(t *testing.T) {
		st := newPendingMigrationWriteStore(t)
		leavePendingMigrationThroughPublicWrite(t, st)
		startAdaptationStoreRevision(t, st)
		assertRejectedPendingMigrationWriteIsByteIdentical(t, st, ErrActiveRevisionBlocksNormalFlow, func() error {
			return st.Progress.SetNovelName("must not write")
		})
	})

	t.Run("prepared command", func(t *testing.T) {
		st := newPendingMigrationWriteStore(t)
		leavePendingMigrationThroughPublicWrite(t, st)
		owner, err := st.Revisions.claimCommandFence("pending-command", "publish", "pending-command-fingerprint")
		if err != nil {
			t.Fatal(err)
		}
		defer owner.releaseCommandFence()
		assertRejectedPendingMigrationWriteIsByteIdentical(t, st, ErrRevisionCommandInProgress, func() error {
			return st.Adaptation.SaveCheck(domain.AdaptationCheck{Chapter: 1, Summary: "must not write"})
		})
	})

	t.Run("prepared publication", func(t *testing.T) {
		fixture := newNormalPublicationFixture(t, "pending-publication")
		snapshot, err := fixture.store.captureNormalRevisionFormalSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		leavePendingMigrationRaw(t, fixture.store, fixture.baseline)
		stop := errors.New("leave publication prepared")
		if err := fixture.store.beginNormalRevisionPublication(fixture.owner, fixture.owner.candidateDigest, snapshot, func() error { return stop }); !errors.Is(err, stop) {
			t.Fatalf("prepare publication: %v", err)
		}
		assertRejectedPendingMigrationWriteIsByteIdentical(t, fixture.store, ErrActiveRevisionBlocksNormalFlow, func() error {
			return fixture.store.Progress.SetTotalChapters(99)
		})
	})
}

func newPendingMigrationWriteStore(t *testing.T) *Store {
	t.Helper()
	return setupLayered(t, []domain.VolumeOutline{{
		Index: 1,
		Title: "one",
		Theme: "one",
		Arcs: []domain.ArcOutline{{
			Index:    1,
			Title:    "arc-one",
			Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "one-one", CoreEvent: "begin", Hook: "continue"}},
		}},
	}})
}

func leavePendingMigrationThroughPublicWrite(t *testing.T, st *Store) {
	t.Helper()
	failed := false
	st.Outline.migration.failpoint = func(point string) error {
		if point == migrationFailAfterWrite && !failed {
			failed = true
			return errors.New("leave pending migration")
		}
		return nil
	}
	err := st.AppendSkeletonVolume(domain.VolumeOutline{
		Index: 2,
		Title: "two",
		Theme: "two",
		Arcs:  []domain.ArcOutline{{Index: 1, Title: "arc-two"}},
	})
	if err == nil {
		t.Fatal("pending migration fixture unexpectedly completed")
	}
	st.Outline.migration.failpoint = nil
}

func leavePendingMigrationRaw(t *testing.T, st *Store, current []domain.VolumeOutline) {
	t.Helper()
	target := domain.CloneStructureSnapshot(current)
	target = append(target, domain.VolumeOutline{
		ID:    domain.LegacyStructureID("pending-publication", domain.StructureKindVolume, "volume-2"),
		Index: 2,
		Title: "pending",
		Theme: "pending",
		Arcs: []domain.ArcOutline{{
			ID:    domain.LegacyStructureID("pending-publication", domain.StructureKindArc, "arc-2"),
			Index: 1,
			Title: "pending arc",
		}},
	})
	source, ok, err := st.Outline.migration.loadIndex()
	if err != nil || !ok {
		t.Fatalf("load source structure: ok=%t err=%v", ok, err)
	}
	payloads, err := layeredOutlineMigrationPayloads(target)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	st.Outline.migration.failpoint = func(point string) error {
		if point == migrationFailAfterWrite && !failed {
			failed = true
			return errors.New("leave pending migration")
		}
		return nil
	}
	if err := st.Outline.migration.save("pending_publication_test", source, structureIndexFromLayered(target), payloads); err == nil {
		t.Fatal("raw pending migration fixture unexpectedly completed")
	}
	st.Outline.migration.failpoint = nil
}

func runPendingMigrationWrite(t *testing.T, write func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- write() }()
	select {
	case err := <-done:
		return err
	case <-time.After(pendingMigrationWriteTimeout):
		t.Fatal("formal write hung while recovering pending migration")
		return nil
	}
}

func assertRejectedPendingMigrationWriteIsByteIdentical(t *testing.T, st *Store, target error, write func() error) {
	t.Helper()
	before := snapshotPendingMigrationProject(t, st.Dir())
	err := runPendingMigrationWrite(t, write)
	if !errors.Is(err, target) {
		t.Fatalf("rejected formal write error = %v, want %v", err, target)
	}
	after := snapshotPendingMigrationProject(t, st.Dir())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected formal write changed migration or formal project bytes")
	}
}

func snapshotPendingMigrationProject(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
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
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".lock") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func snapshotSelectedFiles(t *testing.T, root string, paths []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(paths))
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read selected snapshot %s: %v", rel, err)
		}
		result[rel] = string(data)
	}
	return result
}
