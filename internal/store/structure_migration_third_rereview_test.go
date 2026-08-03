package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestPublicStateEntrypointsRecoverInterruptedStructureMigration(t *testing.T) {
	entrypoints := []struct {
		name string
		call func(*Store, int) error
	}{
		{
			name: "progress_load",
			call: func(st *Store, _ int) error {
				progress, err := st.Progress.Load()
				if err != nil {
					return err
				}
				if progress == nil || progress.TotalChapters != 1 {
					return errors.New("progress load exposed the pre-migration generation")
				}
				return nil
			},
		},
		{
			name: "progress_save",
			call: func(st *Store, _ int) error {
				if err := st.Progress.Save(&domain.Progress{TotalChapters: 1}); err != nil {
					return err
				}
				var progress domain.Progress
				if err := newIO(st.Dir()).ReadJSON("meta/progress.json", &progress); err != nil {
					return err
				}
				if progress.TotalChapters != 1 {
					return errors.New("progress save did not follow recovery")
				}
				return nil
			},
		},
		{
			name: "continuation_load",
			call: func(st *Store, revision int) error {
				snapshot, err := st.Continuation.LoadSnapshot()
				if err != nil {
					return err
				}
				if snapshot.Workflow.Revision != revision {
					return errors.New("continuation load exposed the pre-migration generation")
				}
				return nil
			},
		},
		{
			name: "continuation_update",
			call: func(st *Store, revision int) error {
				updated, err := st.Continuation.Update(revision, func(*domain.ContinuationSnapshot) error { return nil })
				if err != nil {
					return err
				}
				if updated.Workflow.Revision != revision+1 {
					return errors.New("continuation update did not start from the recovered generation")
				}
				return nil
			},
		},
		{
			name: "diagnostic_snapshot",
			call: func(st *Store, _ int) error {
				_ = st.CheckConsistency()
				var progress domain.Progress
				if err := newIO(st.Dir()).ReadJSON("meta/progress.json", &progress); err != nil {
					return err
				}
				if progress.TotalChapters != 1 {
					return errors.New("diagnostic snapshot did not pass through recovery")
				}
				return nil
			},
		},
	}

	for _, point := range allStructureMigrationFailurePoints() {
		for _, entrypoint := range entrypoints {
			t.Run(point+"/"+entrypoint.name, func(t *testing.T) {
				dir, revision := interruptedStateGenerationFixture(t, point)
				reopened := NewStore(dir)
				if err := entrypoint.call(reopened, revision); err != nil {
					t.Fatalf("first public entry did not recover %s: %v", point, err)
				}
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(structureMigrationLogFile))); !os.IsNotExist(err) {
					t.Fatalf("recovery left migration log after %s: %v", point, err)
				}
			})
		}
	}
}

func TestPublicIDLessStructureRetriesPreserveGeneratedIDs(t *testing.T) {
	points := []string{
		migrationFailAfterWrite,
		migrationFailAfterValidate,
		migrationFailDuringSwitch,
		migrationFailDuringProjection,
		migrationFailBeforeIndexWrite,
		migrationFailAfterIndexWrite,
		migrationFailBeforeLogCleanup,
	}
	operations := []struct {
		name  string
		setup func(*testing.T, string) func(*Store) error
		load  func(*testing.T, *Store) structureIndex
	}{
		{
			name: "expand_arc",
			setup: func(t *testing.T, dir string) func(*Store) error {
				st := NewStore(dir)
				if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
					Index: 1, Title: "one", Theme: "one",
					Arcs: []domain.ArcOutline{{Index: 1, Title: "arc", EstimatedChapters: 1}},
				}}); err != nil {
					t.Fatal(err)
				}
				request := []domain.OutlineEntry{{Title: "expanded", CoreEvent: "event", Hook: "hook"}}
				return func(st *Store) error { return st.ExpandArc(1, 1, request) }
			},
			load: loadPublicStructureIndex,
		},
		{
			name: "append_volume",
			setup: func(t *testing.T, dir string) func(*Store) error {
				seedSingleExpandedVolume(t, NewStore(dir))
				request := domain.VolumeOutline{
					Index: 2, Title: "two", Theme: "two",
					Arcs: []domain.ArcOutline{{Index: 1, Title: "arc-two", Chapters: []domain.OutlineEntry{{Title: "two-one", CoreEvent: "event", Hook: "hook"}}}},
				}
				return func(st *Store) error { return st.AppendVolume(request) }
			},
			load: loadPublicStructureIndex,
		},
		{
			name: "append_skeleton_volume",
			setup: func(t *testing.T, dir string) func(*Store) error {
				seedSingleExpandedVolume(t, NewStore(dir))
				request := domain.VolumeOutline{
					Index: 2, Title: "two", Theme: "two",
					Arcs: []domain.ArcOutline{{Index: 1, Title: "arc-two", EstimatedChapters: 2}},
				}
				return func(st *Store) error { return st.AppendSkeletonVolume(request) }
			},
			load: loadPublicStructureIndex,
		},
		{
			name: "adaptation_save_plan",
			setup: func(_ *testing.T, _ string) func(*Store) error {
				request := domain.AdaptationPlan{
					Granularity: domain.AdaptationGranularityChapter,
					Volumes:     []domain.AdaptationVolumePlan{{Index: 1, Title: "volume", TargetFrom: 1, TargetTo: 1}},
					Chapters:    []domain.AdaptationChapterPlan{{Chapter: 1, Title: "target", SourceChapters: []int{1}}},
				}
				return func(st *Store) error { return st.Adaptation.SavePlan(request) }
			},
			load: loadPublicStructureIndex,
		},
	}

	for _, point := range points {
		for _, operation := range operations {
			t.Run(point+"/"+operation.name, func(t *testing.T) {
				dir := t.TempDir()
				call := operation.setup(t, dir)
				st := NewStore(dir)
				failed := false
				st.Outline.migration.failpoint = func(current string) error {
					if current == point && !failed {
						failed = true
						return errors.New("interrupt public ID-less request")
					}
					return nil
				}
				if err := call(st); err == nil {
					t.Fatalf("request unexpectedly completed at %s", point)
				}
				var log structureMigrationLog
				if err := newIO(dir).ReadJSON(structureMigrationLogFile, &log); err != nil {
					t.Fatalf("read interrupted request identity: %v", err)
				}
				firstTarget := log.TargetIndex

				reopened := NewStore(dir)
				if err := call(reopened); err != nil {
					t.Fatalf("exact public retry after %s: %v", point, err)
				}
				got := operation.load(t, reopened)
				if !structureIndexesEqual(got, firstTarget) {
					t.Fatalf("exact retry replaced first generated IDs:\nfirst=%+v\ngot=%+v", firstTarget, got)
				}
			})
		}
	}
}

func TestPublicIDLessSavePlanRetriesDuringRemoval(t *testing.T) {
	dir := t.TempDir()
	request := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Volumes:     []domain.AdaptationVolumePlan{{Index: 1, Title: "volume", TargetFrom: 1, TargetTo: 1}},
		Chapters:    []domain.AdaptationChapterPlan{{Chapter: 1, Title: "target", SourceChapters: []int{1}}},
	}
	st := NewStore(dir)
	failed := false
	st.Outline.migration.failpoint = func(point string) error {
		if point == migrationFailDuringRemoval && !failed {
			failed = true
			return errors.New("interrupt SavePlan cleanup")
		}
		return nil
	}
	if err := st.Adaptation.SavePlan(request); err == nil {
		t.Fatal("SavePlan unexpectedly completed during removal")
	}
	var log structureMigrationLog
	if err := newIO(dir).ReadJSON(structureMigrationLogFile, &log); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(dir)
	if err := reopened.Adaptation.SavePlan(request); err != nil {
		t.Fatalf("retry SavePlan after removal interruption: %v", err)
	}
	if got := loadPublicStructureIndex(t, reopened); !structureIndexesEqual(got, log.TargetIndex) {
		t.Fatalf("SavePlan removal retry changed IDs: first=%+v got=%+v", log.TargetIndex, got)
	}
}

func TestDifferentPublicIDLessRequestsAreNotDiscarded(t *testing.T) {
	t.Run("expand_arc", func(t *testing.T) {
		st := NewStore(t.TempDir())
		if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
			Index: 1, Title: "one", Arcs: []domain.ArcOutline{{Index: 1, Title: "arc", EstimatedChapters: 1}},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := st.ExpandArc(1, 1, []domain.OutlineEntry{{Title: "first"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.ExpandArc(1, 1, []domain.OutlineEntry{{Title: "second"}}); err != nil {
			t.Fatalf("different expansion was discarded: %v", err)
		}
		outline, _ := st.Outline.LoadOutline()
		if len(outline) != 1 || outline[0].Title != "second" {
			t.Fatalf("different expansion did not execute: %+v", outline)
		}
	})

	t.Run("append_volume", func(t *testing.T) {
		st := NewStore(t.TempDir())
		seedSingleExpandedVolume(t, st)
		volume := func(index int, title string) domain.VolumeOutline {
			return domain.VolumeOutline{Index: index, Title: title, Arcs: []domain.ArcOutline{{Index: 1, Title: title, Chapters: []domain.OutlineEntry{{Title: title}}}}}
		}
		if err := st.AppendVolume(volume(2, "two")); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendVolume(volume(3, "three")); err != nil {
			t.Fatalf("different appended volume was discarded: %v", err)
		}
		volumes, _ := st.Outline.LoadLayeredOutline()
		if len(volumes) != 3 || volumes[2].Title != "three" {
			t.Fatalf("different appended volume did not execute: %+v", volumes)
		}
	})

	t.Run("append_skeleton_volume", func(t *testing.T) {
		st := NewStore(t.TempDir())
		seedSingleExpandedVolume(t, st)
		volume := func(index int, title string) domain.VolumeOutline {
			return domain.VolumeOutline{Index: index, Title: title, Arcs: []domain.ArcOutline{{Index: 1, Title: title, EstimatedChapters: 1}}}
		}
		if err := st.AppendSkeletonVolume(volume(2, "two")); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendSkeletonVolume(volume(3, "three")); err != nil {
			t.Fatalf("different skeleton volume was discarded: %v", err)
		}
		volumes, _ := st.Outline.LoadLayeredOutline()
		if len(volumes) != 3 || volumes[2].Title != "three" {
			t.Fatalf("different skeleton volume did not execute: %+v", volumes)
		}
	})

	t.Run("adaptation_save_plan", func(t *testing.T) {
		st := NewStore(t.TempDir())
		plan := domain.AdaptationPlan{Granularity: domain.AdaptationGranularityChapter, Chapters: []domain.AdaptationChapterPlan{{Chapter: 1, Title: "first", SourceChapters: []int{1}}}}
		if err := st.Adaptation.SavePlan(plan); err != nil {
			t.Fatal(err)
		}
		plan.Chapters[0].Title = "second"
		if err := st.Adaptation.SavePlan(plan); err != nil {
			t.Fatalf("different adaptation plan was discarded: %v", err)
		}
		loaded, _ := st.Adaptation.LoadPlan()
		if loaded == nil || loaded.Chapters[0].Title != "second" {
			t.Fatalf("different adaptation plan did not execute: %+v", loaded)
		}
	})
}

func TestRollbackRecoveryCoversSemanticStateAtEveryMigrationStage(t *testing.T) {
	for _, point := range allStructureMigrationFailurePoints() {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			seedRollbackRecoveryFixture(t, st)
			preview := mustRollbackPreview(t, st, domain.RollbackStageProposal)
			failed := false
			st.Outline.migration.failpoint = func(current string) error {
				if current == point && !failed {
					failed = true
					return errors.New("interrupt rollback")
				}
				return nil
			}
			if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash}); err == nil {
				t.Fatalf("rollback unexpectedly completed at %s", point)
			}

			reopened := NewStore(dir)
			progress, err := reopened.Progress.Load()
			if err != nil {
				t.Fatalf("first public read did not recover rollback: %v", err)
			}
			if progress == nil || progress.Phase != domain.PhaseOutline || progress.CompletedChapters != nil {
				t.Fatalf("recovered rollback progress is partial: %+v", progress)
			}
			if plan, err := reopened.Adaptation.LoadPlan(); err != nil || plan != nil {
				t.Fatalf("confirmed plan survived recovered rollback: plan=%+v err=%v", plan, err)
			}
			if proposal, err := reopened.Adaptation.LoadProposal(); err != nil || proposal == nil {
				t.Fatalf("rollback proposal was not durably restored: proposal=%+v err=%v", proposal, err)
			}
			if body, err := reopened.Drafts.LoadChapterText(1); err != nil || body != "" {
				t.Fatalf("writing artifact survived recovered rollback: body=%q err=%v", body, err)
			}
			if len(reopened.Checkpoints.All()) != 0 {
				t.Fatal("checkpoint cache survived recovered rollback")
			}
			if _, err := reopened.Continuation.LoadSnapshot(); !errors.Is(err, ErrContinuationNotInitialized) {
				t.Fatalf("continuation state survived recovered rollback: %v", err)
			}
		})
	}
}

func TestRemoveFileAndRemoveAllDeleteInterruptedReplacementState(t *testing.T) {
	removers := []struct {
		name string
		call func(*IO, string) error
	}{
		{name: "remove_file", call: func(io *IO, rel string) error { return io.RemoveFile(rel) }},
		{name: "remove_all", call: func(io *IO, rel string) error {
			_, err := io.RemoveAllRel(rel)
			return err
		}},
	}
	states := []struct {
		name       string
		withTarget bool
	}{
		{name: "backup_only"},
		{name: "target_and_backup", withTarget: true},
	}

	for _, remover := range removers {
		for _, state := range states {
			t.Run(remover.name+"/"+state.name, func(t *testing.T) {
				dir := t.TempDir()
				io := newIO(dir)
				target := filepath.Join(dir, "state.json")
				if state.withTarget {
					if err := os.WriteFile(target, []byte("new"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(target+".replace-backup", []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target+".tmp-orphan", []byte("temporary"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := remover.call(io, "state.json"); err != nil {
					t.Fatalf("remove interrupted replacement: %v", err)
				}
				if _, err := io.ReadFile("state.json"); !os.IsNotExist(err) {
					t.Fatalf("deleted state was revived: %v", err)
				}
				for _, path := range []string{target, target + ".replace-backup", target + ".tmp-orphan"} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("interrupted replacement path remains %s: %v", path, err)
					}
				}
			})
		}
	}
}

func interruptedStateGenerationFixture(t *testing.T, point string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "one"}, {Chapter: 2, Title: "two"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{TotalChapters: 2}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.Continuation.InitializeSource("source", 1)
	if err != nil {
		t.Fatal(err)
	}
	targetOutline, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatal(err)
	}
	targetOutline = targetOutline[:1]
	payloads, err := outlineMigrationPayloads(targetOutline, nil)
	if err != nil {
		t.Fatal(err)
	}
	progressPayload, err := jsonMigrationPayload("meta/progress.json", &domain.Progress{TotalChapters: 1})
	if err != nil {
		t.Fatal(err)
	}
	workflow := snapshot.Workflow
	workflow.Revision++
	workflowPayload, err := jsonMigrationPayload(continuationWorkflowFile, workflow)
	if err != nil {
		t.Fatal(err)
	}
	payloads = append(payloads, progressPayload, workflowPayload)
	source, ok, err := st.Outline.migration.loadIndex()
	if err != nil || !ok {
		t.Fatalf("load source index: ok=%t err=%v", ok, err)
	}
	failed := false
	st.Outline.migration.failpoint = func(current string) error {
		if current == point && !failed {
			failed = true
			return errors.New("interrupt shared recovery fixture")
		}
		return nil
	}
	if err := st.Outline.migration.save("shared_recovery_fixture", source, structureIndexFromOutline(targetOutline), payloads); err == nil {
		t.Fatalf("fixture unexpectedly completed at %s", point)
	}
	return dir, workflow.Revision
}

func seedSingleExpandedVolume(t *testing.T, st *Store) {
	t.Helper()
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1, Title: "one", Theme: "one",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "arc-one", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "one-one"}}}},
	}}); err != nil {
		t.Fatal(err)
	}
}

func loadPublicStructureIndex(t *testing.T, st *Store) structureIndex {
	t.Helper()
	index, ok, err := st.Outline.migration.loadIndex()
	if err != nil || !ok {
		t.Fatalf("load public structure index: ok=%t err=%v", ok, err)
	}
	return index
}

func structureIndexesEqual(left, right structureIndex) bool {
	leftJSON, leftErr := jsonMigrationPayload("left", left)
	rightJSON, rightErr := jsonMigrationPayload("right", right)
	return leftErr == nil && rightErr == nil && string(leftJSON.Data) == string(rightJSON.Data)
}

func seedRollbackRecoveryFixture(t *testing.T, st *Store) {
	t.Helper()
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SavePlan(rollbackTestAdaptationPlan()); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("# Adapted"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "Target"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "body"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseComplete, TotalChapters: 1, CompletedChapters: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.Scope{Chapter: 1}, "written", "chapters/01.md", "digest"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Continuation.InitializeSource("source", 1); err != nil {
		t.Fatal(err)
	}
}

func allStructureMigrationFailurePoints() []string {
	return []string{
		migrationFailAfterWrite,
		migrationFailAfterValidate,
		migrationFailDuringSwitch,
		migrationFailDuringProjection,
		migrationFailDuringRemoval,
		migrationFailBeforeIndexWrite,
		migrationFailAfterIndexWrite,
		migrationFailBeforeLogCleanup,
	}
}
