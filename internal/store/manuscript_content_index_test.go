package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestManuscriptContentIndexIsSignedAndRebuildsAfterCorruption(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "One"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "formal current prose"); err != nil {
		t.Fatal(err)
	}
	first, err := st.RebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Validate(); err != nil || len(first.Entries) != 1 || first.Entries[0].CurrentSHA256 == "" {
		t.Fatalf("index = %+v, err = %v", first, err)
	}
	if err := os.WriteFile(st.ManuscriptRevisions.io.path(manuscriptContentIndexPath), []byte(`{"version":1,"signature":"forged"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuilt.Validate(); err != nil || rebuilt.Signature != first.Signature {
		t.Fatalf("rebuilt = %+v, err = %v", rebuilt, err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "Renamed"}}); err != nil {
		t.Fatal(err)
	}
	structureRebuilt, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}
	if structureRebuilt.StructureSignature == rebuilt.StructureSignature {
		t.Fatal("structure mutation reused stale content index")
	}
}

func TestManuscriptContentIndexRejectsSameSizeSameMtimeExternalDrift(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "One"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "AAAA"); err != nil {
		t.Fatal(err)
	}
	first, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}
	outline, err := st.Outline.LoadOutline()
	if err != nil || len(outline) != 1 {
		t.Fatalf("outline = %+v err=%v", outline, err)
	}
	chapterPath := filepath.Join(st.Dir(), filepath.FromSlash(chapterCanonicalRel(outline[0].ID, "final.md")))
	info, err := os.Stat(chapterPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chapterPath, []byte("BBBB"), info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(chapterPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.CurrentSignature == first.CurrentSignature || rebuilt.Entries[0].CurrentSHA256 == first.Entries[0].CurrentSHA256 {
		t.Fatal("same-size same-mtime external drift reused the stale index")
	}
}

func TestManuscriptContentIndexRebuildsForLayeredCurrentAndSummaryDrift(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumeID := domain.LegacyStructureID(st.Dir(), domain.StructureKindVolume, "volume-one")
	arcID := domain.LegacyStructureID(st.Dir(), domain.StructureKindArc, "arc-one")
	chapterID := domain.LegacyStructureID(st.Dir(), domain.StructureKindChapter, "chapter-one")
	volumes := []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "One", CoreEvent: "event", Hook: "hook", Scenes: []string{"scene"}}},
		}},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "current one"); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "summary one"}); err != nil {
		t.Fatal(err)
	}
	first, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}

	if err := st.Drafts.SaveFinalChapter(1, "current two"); err != nil {
		t.Fatal(err)
	}
	current, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentSignature == first.CurrentSignature || current.Entries[0].CurrentSHA256 == first.Entries[0].CurrentSHA256 {
		t.Fatal("current prose drift reused stale content index")
	}

	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Summary: "summary two"}); err != nil {
		t.Fatal(err)
	}
	summary, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}
	if summary.SummarySignature == current.SummarySignature || summary.Entries[0].SummarySHA256 == current.Entries[0].SummarySHA256 {
		t.Fatal("summary drift reused stale content index")
	}

	volumes[0].Arcs[0].Chapters[0].Title = "Renamed"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	structure, err := st.LoadOrRebuildManuscriptContentIndex()
	if err != nil {
		t.Fatal(err)
	}
	if structure.StructureSignature == summary.StructureSignature {
		t.Fatal("layered structure drift reused stale content index")
	}
}
