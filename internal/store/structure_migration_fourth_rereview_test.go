package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestScopedSummaryProjectionRemovesStaleNumericPaths(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func([]domain.VolumeOutline) []domain.VolumeOutline
	}{
		{
			name: "asymmetric_swap",
			mutate: func(volumes []domain.VolumeOutline) []domain.VolumeOutline {
				volumes[0], volumes[1] = volumes[1], volumes[0]
				return volumes
			},
		},
		{
			name: "insert_before_summarized_volume",
			mutate: func(volumes []domain.VolumeOutline) []domain.VolumeOutline {
				inserted := domain.VolumeOutline{
					Title: "inserted",
					Arcs: []domain.ArcOutline{{
						Title:    "inserted-arc",
						Chapters: []domain.OutlineEntry{{Title: "inserted-chapter"}},
					}},
				}
				return append([]domain.VolumeOutline{inserted}, volumes...)
			},
		},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			st := NewStore(t.TempDir())
			seedTwoSummaryVolumes(t, st)
			if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: "summary-one"}); err != nil {
				t.Fatal(err)
			}
			if err := st.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "arc-one"}); err != nil {
				t.Fatal(err)
			}

			volumes, err := st.Outline.LoadLayeredOutline()
			if err != nil {
				t.Fatal(err)
			}
			volumes = mutation.mutate(volumes)
			if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
				t.Fatal(err)
			}

			if summary, err := st.Summaries.LoadVolumeSummary(1); err != nil || summary != nil {
				t.Fatalf("unsummarized volume inherited stale projection: summary=%+v err=%v", summary, err)
			}
			if summary, err := st.Summaries.LoadArcSummary(1, 1); err != nil || summary != nil {
				t.Fatalf("unsummarized arc inherited stale projection: summary=%+v err=%v", summary, err)
			}
			for _, rel := range []string{volumeSummaryRel(1), arcSummaryRel(1, 1)} {
				if _, err := os.Stat(filepath.Join(st.Dir(), filepath.FromSlash(rel))); !os.IsNotExist(err) {
					t.Fatalf("stale numeric projection remains at %s: %v", rel, err)
				}
			}
			if err := st.Summaries.io.WriteJSON(volumeSummaryRel(1), domain.VolumeSummary{Volume: 1, Summary: "stale"}); err != nil {
				t.Fatal(err)
			}
			if err := st.Summaries.io.WriteJSON(arcSummaryRel(1, 1), domain.ArcSummary{Volume: 1, Arc: 1, Summary: "stale"}); err != nil {
				t.Fatal(err)
			}
			if summary, err := st.Summaries.LoadVolumeSummary(1); err != nil || summary != nil {
				t.Fatalf("indexed volume load fell back to stale legacy file: summary=%+v err=%v", summary, err)
			}
			if summary, err := st.Summaries.LoadArcSummary(1, 1); err != nil || summary != nil {
				t.Fatalf("indexed arc load fell back to stale legacy file: summary=%+v err=%v", summary, err)
			}
		})
	}
}

func TestScopedSummaryRemovalRecoversAcrossProjectionWindows(t *testing.T) {
	for _, point := range []string{migrationFailDuringProjection, migrationFailDuringRemoval} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			seedTwoSummaryVolumes(t, st)
			if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: "summary-one"}); err != nil {
				t.Fatal(err)
			}
			if err := st.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "arc-one"}); err != nil {
				t.Fatal(err)
			}
			volumes, err := st.Outline.LoadLayeredOutline()
			if err != nil {
				t.Fatal(err)
			}
			volumes[0], volumes[1] = volumes[1], volumes[0]
			failed := false
			st.Outline.migration.failpoint = func(current string) error {
				if current == point && !failed {
					failed = true
					return errors.New("interrupt scoped summary projection")
				}
				return nil
			}
			if err := st.Outline.SaveLayeredOutline(volumes); err == nil {
				t.Fatalf("migration unexpectedly completed at %s", point)
			}

			reopened := NewStore(dir)
			if summary, err := reopened.Summaries.LoadVolumeSummary(1); err != nil || summary != nil {
				t.Fatalf("recovery exposed stale volume summary: summary=%+v err=%v", summary, err)
			}
			if summary, err := reopened.Summaries.LoadArcSummary(1, 1); err != nil || summary != nil {
				t.Fatalf("recovery exposed stale arc summary: summary=%+v err=%v", summary, err)
			}
		})
	}
}

func TestDurablePublicReceiptsSurviveLaterStructureChanges(t *testing.T) {
	for _, tt := range publicReceiptCases() {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			a, b := tt.setup(t, NewStore(dir))
			if err := a(NewStore(dir)); err != nil {
				t.Fatalf("first A: %v", err)
			}
			if err := b(NewStore(dir)); err != nil {
				t.Fatalf("B after A: %v", err)
			}
			reopened := NewStore(dir)
			if err := a(reopened); err != nil {
				t.Fatalf("delayed exact A after restart: %v", err)
			}
			tt.assert(t, reopened)
		})
	}
}

func TestInterruptedPublicAThenBDoesNotLoseB(t *testing.T) {
	for _, tt := range publicReceiptCases() {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			a, b := tt.setup(t, st)
			failed := false
			st.Outline.migration.failpoint = func(point string) error {
				if point == migrationFailAfterIndexWrite && !failed {
					failed = true
					return errors.New("lose A response")
				}
				return nil
			}
			if err := a(st); err == nil {
				t.Fatal("A unexpectedly returned success")
			}
			if err := b(st); err != nil {
				t.Fatalf("B after interrupted A: %v", err)
			}
			reopened := NewStore(dir)
			if err := a(reopened); err != nil {
				t.Fatalf("delayed A after interrupted A then B: %v", err)
			}
			tt.assert(t, reopened)
		})
	}
}

type publicReceiptCase struct {
	name   string
	setup  func(*testing.T, *Store) (func(*Store) error, func(*Store) error)
	assert func(*testing.T, *Store)
}

func publicReceiptCases() []publicReceiptCase {
	return []publicReceiptCase{
		{
			name: "expand_arc",
			setup: func(t *testing.T, st *Store) (func(*Store) error, func(*Store) error) {
				if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
					Title: "one", Arcs: []domain.ArcOutline{{Title: "arc", EstimatedChapters: 1}},
				}}); err != nil {
					t.Fatal(err)
				}
				a := []domain.OutlineEntry{{Title: "A"}}
				b := []domain.OutlineEntry{{Title: "B"}}
				return func(st *Store) error { return st.ExpandArc(1, 1, a) }, func(st *Store) error { return st.ExpandArc(1, 1, b) }
			},
			assert: func(t *testing.T, st *Store) {
				outline, err := st.Outline.LoadOutline()
				if err != nil || len(outline) != 1 || outline[0].Title != "B" {
					t.Fatalf("delayed A replaced B: outline=%+v err=%v", outline, err)
				}
			},
		},
		{
			name: "append_volume",
			setup: func(t *testing.T, st *Store) (func(*Store) error, func(*Store) error) {
				seedSingleExpandedVolume(t, st)
				a := expandedTestVolume(2, "A")
				b := expandedTestVolume(3, "B")
				return func(st *Store) error { return st.AppendVolume(a) }, func(st *Store) error { return st.AppendVolume(b) }
			},
			assert: assertThreeVolumesEndingInB,
		},
		{
			name: "append_skeleton_volume",
			setup: func(t *testing.T, st *Store) (func(*Store) error, func(*Store) error) {
				seedSingleExpandedVolume(t, st)
				a := skeletonTestVolume(2, "A")
				b := skeletonTestVolume(3, "B")
				return func(st *Store) error { return st.AppendSkeletonVolume(a) }, func(st *Store) error { return st.AppendSkeletonVolume(b) }
			},
			assert: assertThreeVolumesEndingInB,
		},
		{
			name: "adaptation_save_plan",
			setup: func(_ *testing.T, _ *Store) (func(*Store) error, func(*Store) error) {
				a := adaptationReceiptPlan("A")
				b := adaptationReceiptPlan("B")
				return func(st *Store) error { return st.Adaptation.SavePlan(a) }, func(st *Store) error { return st.Adaptation.SavePlan(b) }
			},
			assert: func(t *testing.T, st *Store) {
				plan, err := st.Adaptation.LoadPlan()
				if err != nil || plan == nil || len(plan.Chapters) != 1 || plan.Chapters[0].Title != "B" {
					t.Fatalf("delayed A replaced B: plan=%+v err=%v", plan, err)
				}
			},
		},
	}
}

func TestNewStoreAndSameInstanceRecoveryKeepCheckpointCacheFailClosed(t *testing.T) {
	t.Run("constructor_recovery_error", func(t *testing.T) {
		dir := t.TempDir()
		st := NewStore(dir)
		if _, err := st.Checkpoints.Append(domain.Scope{Chapter: 1}, "written", "chapters/01.md", "old"); err != nil {
			t.Fatal(err)
		}
		migrationPath := filepath.Join(dir, filepath.FromSlash(structureMigrationLogFile))
		if err := os.MkdirAll(filepath.Dir(migrationPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(migrationPath, []byte(`{"version":`), 0o644); err != nil {
			t.Fatal(err)
		}

		reopened := NewStore(dir)
		if err := reopened.RecoverStructureMigration(); err == nil {
			t.Fatal("invalid startup migration was not reported")
		}
		if got := reopened.Checkpoints.All(); len(got) != 0 {
			t.Fatalf("constructor exposed pre-recovery checkpoint cache: %+v", got)
		}
	})

	t.Run("same_store_rollback_recovery", func(t *testing.T) {
		st := NewStore(t.TempDir())
		seedRollbackRecoveryFixture(t, st)
		preview := mustRollbackPreview(t, st, domain.RollbackStageProposal)
		failed := false
		st.Outline.migration.failpoint = func(point string) error {
			if point == migrationFailAfterIndexWrite && !failed {
				failed = true
				return errors.New("lose rollback response")
			}
			return nil
		}
		if _, err := st.Rollback(domain.RollbackRequest{Confirm: true, PreviewHash: preview.PreviewHash}); err == nil {
			t.Fatal("rollback unexpectedly completed")
		}
		st.Outline.migration.failpoint = nil
		if err := st.RecoverStructureMigration(); err != nil {
			t.Fatalf("recover rollback: %v", err)
		}
		if got := st.Checkpoints.All(); len(got) != 0 {
			t.Fatalf("checkpoint cache stayed in pre-rollback generation: %+v", got)
		}
	})
}

func seedTwoSummaryVolumes(t *testing.T, st *Store) {
	t.Helper()
	volumes := []domain.VolumeOutline{
		expandedTestVolume(1, "one"),
		expandedTestVolume(2, "two"),
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
}

func expandedTestVolume(index int, title string) domain.VolumeOutline {
	return domain.VolumeOutline{
		Index: index,
		Title: title,
		Arcs: []domain.ArcOutline{{
			Index:    1,
			Title:    title + "-arc",
			Chapters: []domain.OutlineEntry{{Title: title + "-chapter"}},
		}},
	}
}

func skeletonTestVolume(index int, title string) domain.VolumeOutline {
	return domain.VolumeOutline{
		Index: index,
		Title: title,
		Arcs: []domain.ArcOutline{{
			Index:             1,
			Title:             title + "-arc",
			EstimatedChapters: 1,
		}},
	}
}

func adaptationReceiptPlan(title string) domain.AdaptationPlan {
	return domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Volumes:     []domain.AdaptationVolumePlan{{Index: 1, Title: title, TargetFrom: 1, TargetTo: 1}},
		Chapters:    []domain.AdaptationChapterPlan{{Chapter: 1, Title: title, SourceChapters: []int{1}}},
	}
}

func assertThreeVolumesEndingInB(t *testing.T, st *Store) {
	t.Helper()
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) != 3 || volumes[1].Title != "A" || volumes[2].Title != "B" {
		t.Fatalf("delayed A changed B generation: volumes=%+v err=%v", volumes, err)
	}
}
