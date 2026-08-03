package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestIndexedMissingCanonicalSummaryDoesNotRehydrateStaleProjectionDuringLaterMigration(t *testing.T) {
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
			name: "insert_before_missing_summary",
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
		for _, failpoint := range []string{migrationFailDuringProjection, migrationFailDuringRemoval} {
			t.Run(mutation.name+"/"+failpoint, func(t *testing.T) {
				dir := t.TempDir()
				st := NewStore(dir)
				seedTwoSummaryVolumes(t, st)
				if err := st.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Summary: "canonical-volume"}); err != nil {
					t.Fatal(err)
				}
				if err := st.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Summary: "canonical-arc"}); err != nil {
					t.Fatal(err)
				}

				index, ok, err := st.Outline.migration.loadIndexUnlocked()
				if err != nil || !ok {
					t.Fatalf("load indexed source: ok=%v err=%v", ok, err)
				}
				volumeRef, ok := index.volumeRef(1)
				if !ok {
					t.Fatal("missing source volume ref")
				}
				arcRef, ok := index.arcRef(1, 1)
				if !ok {
					t.Fatal("missing source arc ref")
				}

				missingCanonical := []string{
					volumeCanonicalRel(volumeRef.ID, "summary.json"),
					arcCanonicalRel(arcRef.ID, "summary.json"),
				}
				for _, rel := range missingCanonical {
					if err := st.Summaries.io.RemoveFile(rel); err != nil {
						t.Fatalf("remove canonical %s: %v", rel, err)
					}
				}
				if err := st.Summaries.io.WriteJSON(volumeSummaryRel(1), domain.VolumeSummary{Volume: 1, Summary: "stale-volume"}); err != nil {
					t.Fatal(err)
				}
				if err := st.Summaries.io.WriteJSON(arcSummaryRel(1, 1), domain.ArcSummary{Volume: 1, Arc: 1, Summary: "stale-arc"}); err != nil {
					t.Fatal(err)
				}

				volumes, err := st.Outline.LoadLayeredOutline()
				if err != nil {
					t.Fatal(err)
				}
				volumes = mutation.mutate(volumes)
				failed := false
				st.Outline.migration.failpoint = func(point string) error {
					if point == failpoint && !failed {
						failed = true
						return errors.New("interrupt indexed missing-summary migration")
					}
					return nil
				}
				if err := st.Outline.SaveLayeredOutline(volumes); err == nil {
					t.Fatalf("migration unexpectedly completed at %s", failpoint)
				}

				reopened := NewStore(dir)
				if err := reopened.RecoverStructureMigration(); err != nil {
					t.Fatalf("recover migration: %v", err)
				}
				for _, rel := range missingCanonical {
					assertSummaryPathMissing(t, reopened, rel)
				}
				for _, rel := range []string{
					volumeSummaryRel(1),
					arcSummaryRel(1, 1),
					volumeSummaryRel(2),
					arcSummaryRel(2, 1),
				} {
					assertSummaryPathMissing(t, reopened, rel)
				}
				if summary, err := reopened.Summaries.LoadVolumeSummary(2); err != nil || summary != nil {
					t.Fatalf("moved volume inherited stale summary: summary=%+v err=%v", summary, err)
				}
				if summary, err := reopened.Summaries.LoadArcSummary(2, 1); err != nil || summary != nil {
					t.Fatalf("moved arc inherited stale summary: summary=%+v err=%v", summary, err)
				}
			})
		}
	}
}

func assertSummaryPathMissing(t *testing.T, st *Store, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(st.Dir(), filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Fatalf("stale summary artifact remains at %s: %v", rel, err)
	}
}
