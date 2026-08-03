package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestLegacyOutlineLoadHydratesIDsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`[{"chapter":1,"title":"one","core_event":"event","hook":"hook","scenes":[]}]`)
	outlinePath := filepath.Join(dir, "outline.json")
	if err := os.WriteFile(outlinePath, legacy, 0o644); err != nil {
		t.Fatalf("write legacy outline: %v", err)
	}

	st := NewStore(dir)
	first, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := NewStore(dir).Outline.LoadOutline()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(first) != 1 || first[0].ID == "" || first[0].ID != second[0].ID {
		t.Fatalf("legacy IDs are not deterministic: first=%+v second=%+v", first, second)
	}
	after, err := os.ReadFile(outlinePath)
	if err != nil {
		t.Fatalf("read legacy outline after load: %v", err)
	}
	if !bytes.Equal(after, legacy) {
		t.Fatalf("read-only load rewrote outline.json:\n%s", after)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read project dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "outline.json" {
		t.Fatalf("read-only load created files: %+v", entries)
	}
}

func TestOutlineSavePersistsLegacyIDsAndKeepsThemAcrossReorder(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`[
  {"chapter":1,"title":"one","core_event":"first","hook":"h1","scenes":[]},
  {"chapter":2,"title":"two","core_event":"second","hook":"h2","scenes":[]}
]`)
	if err := os.WriteFile(filepath.Join(dir, "outline.json"), legacy, 0o644); err != nil {
		t.Fatalf("write legacy outline: %v", err)
	}
	st := NewStore(dir)
	loaded, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("load legacy outline: %v", err)
	}
	firstID, secondID := loaded[0].ID, loaded[1].ID
	if err := st.Outline.SaveOutline(loaded); err != nil {
		t.Fatalf("persist legacy IDs: %v", err)
	}
	persisted, err := os.ReadFile(filepath.Join(dir, "outline.json"))
	if err != nil {
		t.Fatalf("read persisted outline: %v", err)
	}
	if !bytes.Contains(persisted, []byte(`"id"`)) {
		t.Fatalf("first structural save did not persist IDs: %s", persisted)
	}

	loaded[0], loaded[1] = loaded[1], loaded[0]
	loaded = append(loaded, domain.OutlineEntry{Title: "new", CoreEvent: "third", Hook: "h3"})
	if err := st.Outline.SaveOutline(loaded); err != nil {
		t.Fatalf("save reordered outline: %v", err)
	}
	reordered, err := st.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("load reordered outline: %v", err)
	}
	if reordered[0].ID != secondID || reordered[1].ID != firstID {
		t.Fatalf("reorder changed identities: %+v", reordered)
	}
	if reordered[0].Chapter != 1 || reordered[1].Chapter != 2 || reordered[2].Chapter != 3 {
		t.Fatalf("display chapter projection is not slice-ordered: %+v", reordered)
	}
	if reordered[2].ID == "" || reordered[2].ID == firstID || reordered[2].ID == secondID {
		t.Fatalf("new node did not receive a persistent unique ID: %+v", reordered[2])
	}
	newID := reordered[2].ID
	stable, err := NewStore(dir).Outline.LoadOutline()
	if err != nil {
		t.Fatalf("reload persisted outline: %v", err)
	}
	if stable[2].ID != newID {
		t.Fatalf("new node ID was not stable after reload: %q != %q", stable[2].ID, newID)
	}
}

func TestLayeredReorderProjectsNumbersAndMovesArtifactsWithStableIDs(t *testing.T) {
	dir := t.TempDir()
	legacyLayered := []byte(`[
  {"index":1,"title":"one","theme":"one","arcs":[{"index":1,"title":"a","chapters":[{"chapter":1,"title":"c1","scenes":[]}]}]},
  {"index":2,"title":"two","theme":"two","arcs":[{"index":1,"title":"b","chapters":[{"chapter":2,"title":"c2","scenes":[]}]}]}
]`)
	if err := os.WriteFile(filepath.Join(dir, "layered_outline.json"), legacyLayered, 0o644); err != nil {
		t.Fatalf("write legacy layered outline: %v", err)
	}
	artifacts := map[string][]byte{
		"chapters/01.md":                   []byte("body-one"),
		"chapters/02.md":                   []byte("body-two"),
		"drafts/01.draft.md":               []byte("draft-one"),
		"drafts/02.draft.md":               []byte("draft-two"),
		"summaries/01.json":                []byte(`{"chapter":1,"summary":"summary-one"}`),
		"summaries/02.json":                []byte(`{"chapter":2,"summary":"summary-two"}`),
		"reviews/01.json":                  []byte(`{"chapter":1,"scope":"chapter","summary":"review-one","affected_chapters":[2]}`),
		"reviews/02.json":                  []byte(`{"chapter":2,"scope":"chapter","summary":"review-two","affected_chapters":[1]}`),
		"meta/adaptation/checks/0001.json": []byte(`{"chapter":1,"passed":true,"summary":"check-one"}`),
		"meta/adaptation/checks/0002.json": []byte(`{"chapter":2,"passed":true,"summary":"check-two"}`),
		"timeline.json":                    []byte(`[{"chapter":1,"event":"event-one"},{"chapter":2,"event":"event-two"}]`),
	}
	for rel, content := range artifacts {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir artifact %s: %v", rel, err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("write artifact %s: %v", rel, err)
		}
	}

	st := NewStore(dir)
	loaded, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("load layered outline: %v", err)
	}
	firstID, secondID := loaded[0].ID, loaded[1].ID
	firstChapterID, secondChapterID := loaded[0].Arcs[0].Chapters[0].ID, loaded[1].Arcs[0].Chapters[0].ID

	loaded[0], loaded[1] = loaded[1], loaded[0]
	if err := st.Outline.SaveLayeredOutline(loaded); err != nil {
		t.Fatalf("save reordered layered outline: %v", err)
	}
	reordered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("reload reordered layered outline: %v", err)
	}
	if reordered[0].ID != secondID || reordered[1].ID != firstID {
		t.Fatalf("volume reorder changed IDs: %+v", reordered)
	}
	if reordered[0].Arcs[0].Chapters[0].ID != secondChapterID || reordered[1].Arcs[0].Chapters[0].ID != firstChapterID {
		t.Fatalf("chapter reorder changed IDs: %+v", reordered)
	}
	if reordered[0].Index != 1 || reordered[1].Index != 2 || reordered[0].Arcs[0].Chapters[0].Chapter != 1 || reordered[1].Arcs[0].Chapters[0].Chapter != 2 {
		t.Fatalf("numeric order was not projected: %+v", reordered)
	}
	if body, _ := st.Drafts.LoadChapterText(1); body != "body-two" {
		t.Fatalf("chapter 1 body = %q, want body-two", body)
	}
	if draft, _ := st.Drafts.LoadDraft(2); draft != "draft-one" {
		t.Fatalf("chapter 2 draft = %q, want draft-one", draft)
	}
	if summary, _ := st.Summaries.LoadSummary(1); summary == nil || summary.Summary != "summary-two" || summary.Chapter != 1 {
		t.Fatalf("chapter 1 summary did not follow ID: %+v", summary)
	}
	if review, _ := st.World.LoadReview(1); review == nil || review.Summary != "review-two" || !reflect.DeepEqual(review.AffectedChapters, []int{2}) {
		t.Fatalf("chapter 1 review references did not follow IDs: %+v", review)
	}
	if check, _ := st.Adaptation.LoadCheck(1); check == nil || check.Summary != "check-two" || check.Chapter != 1 {
		t.Fatalf("chapter 1 adaptation check did not follow ID: %+v", check)
	}
	timeline, err := st.World.LoadTimeline()
	if err != nil || len(timeline) != 2 || timeline[0].Chapter != 2 || timeline[1].Chapter != 1 {
		t.Fatalf("timeline references did not follow IDs: %+v err=%v", timeline, err)
	}
	canonicalBody, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(chapterCanonicalRel(secondChapterID, "final.md"))))
	if err != nil || string(canonicalBody) != "body-two" {
		t.Fatalf("canonical chapter body = %q err=%v", canonicalBody, err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(structureMigrationLogFile))); !os.IsNotExist(err) {
		t.Fatalf("migration log did not converge: %v", err)
	}
}

func TestAdaptationTargetReorderPreservesSourceAnchors(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Volumes:     []domain.AdaptationVolumePlan{{Index: 1, Title: "target", TargetFrom: 1, TargetTo: 2, SourceFrom: 10, SourceTo: 21}},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "one", SourceChapters: []int{10, 11}, SourceRange: domain.SourceRange{From: 10, To: 11}, SourceSegments: []domain.AdaptationSourceSegment{{SourceChapter: 10, Sequence: 1, EventIDs: []string{"s-one"}}}},
			{Chapter: 2, Title: "two", SourceChapters: []int{20, 21}, SourceRange: domain.SourceRange{From: 20, To: 21}, SourceSegments: []domain.AdaptationSourceSegment{{SourceChapter: 20, Sequence: 1, EventIDs: []string{"s-two"}}}},
		},
	}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("save adaptation plan: %v", err)
	}
	loaded, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("load adaptation plan: %v", err)
	}
	anchorsByID := make(map[string]struct {
		chapters []int
		range_   domain.SourceRange
		segments []domain.AdaptationSourceSegment
	})
	for _, chapter := range loaded.Chapters {
		anchorsByID[chapter.OutlineEntry.ID] = struct {
			chapters []int
			range_   domain.SourceRange
			segments []domain.AdaptationSourceSegment
		}{append([]int(nil), chapter.SourceChapters...), chapter.SourceRange, append([]domain.AdaptationSourceSegment(nil), chapter.SourceSegments...)}
	}
	loaded.Chapters[0], loaded.Chapters[1] = loaded.Chapters[1], loaded.Chapters[0]
	if err := st.Adaptation.SavePlan(*loaded); err != nil {
		t.Fatalf("save reordered adaptation plan: %v", err)
	}
	reordered, err := st.Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("reload reordered adaptation plan: %v", err)
	}
	if reordered.Chapters[0].Chapter != 1 || reordered.Chapters[1].Chapter != 2 {
		t.Fatalf("target chapters were not projected from order: %+v", reordered.Chapters)
	}
	for _, chapter := range reordered.Chapters {
		want := anchorsByID[chapter.OutlineEntry.ID]
		if !reflect.DeepEqual(chapter.SourceChapters, want.chapters) || chapter.SourceRange != want.range_ || !reflect.DeepEqual(chapter.SourceSegments, want.segments) {
			t.Fatalf("source anchors changed for target %s: got %+v want %+v", chapter.OutlineEntry.ID, chapter, want)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(adaptationPlanFile)))
	if err != nil {
		t.Fatalf("read saved adaptation plan: %v", err)
	}
	var persisted domain.AdaptationPlan
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode saved adaptation plan: %v", err)
	}
	if persisted.Chapters[0].OutlineEntry.ID == "" || persisted.Volumes[0].ID == "" {
		t.Fatalf("target structure IDs were not persisted: %s", raw)
	}
}
