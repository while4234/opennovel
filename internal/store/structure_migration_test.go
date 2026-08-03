package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestLayeredStructureMigrationRecoversEveryDurableStage(t *testing.T) {
	points := []string{
		migrationFailAfterWrite,
		migrationFailAfterValidate,
		migrationFailDuringSwitch,
		migrationFailBeforeLogCleanup,
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			dir, reordered := legacyLayeredMigrationFixture(t)
			st := NewStore(dir)
			failed := false
			st.Outline.migration.failpoint = func(current string) error {
				if current == point && !failed {
					failed = true
					return errors.New("injected interruption")
				}
				return nil
			}
			if err := st.Outline.SaveLayeredOutline(reordered); err == nil {
				t.Fatalf("SaveLayeredOutline succeeded at injected point %s", point)
			}
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(structureMigrationLogFile))); err != nil {
				t.Fatalf("durable migration log missing after %s: %v", point, err)
			}

			reopened := NewStore(dir)
			if err := reopened.Outline.SaveLayeredOutline(reordered); err != nil {
				t.Fatalf("resume migration after %s: %v", point, err)
			}
			assertMigratedChapterArtifacts(t, reopened)
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(structureMigrationLogFile))); !os.IsNotExist(err) {
				t.Fatalf("migration log remains after retry at %s: %v", point, err)
			}
		})
	}
}

func TestAdaptationStructureMigrationRecoversEveryDurableStage(t *testing.T) {
	points := []string{
		migrationFailAfterWrite,
		migrationFailAfterValidate,
		migrationFailDuringSwitch,
		migrationFailBeforeLogCleanup,
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			dir, reordered := legacyAdaptationMigrationFixture(t)
			st := NewStore(dir)
			failed := false
			st.Adaptation.migration.failpoint = func(current string) error {
				if current == point && !failed {
					failed = true
					return errors.New("injected interruption")
				}
				return nil
			}
			if err := st.Adaptation.SavePlan(reordered); err == nil {
				t.Fatalf("SavePlan succeeded at injected point %s", point)
			}

			reopened := NewStore(dir)
			if err := reopened.Adaptation.SavePlan(reordered); err != nil {
				t.Fatalf("resume adaptation migration after %s: %v", point, err)
			}
			plan, err := reopened.Adaptation.LoadPlan()
			if err != nil || plan == nil || len(plan.Chapters) != 2 {
				t.Fatalf("load migrated adaptation plan: plan=%+v err=%v", plan, err)
			}
			if plan.Chapters[0].Title != "target-two" || !reflect.DeepEqual(plan.Chapters[0].SourceChapters, []int{20, 21}) || plan.Chapters[0].SourceRange != (domain.SourceRange{From: 20, To: 21}) {
				t.Fatalf("source anchors changed during target reorder: %+v", plan.Chapters[0])
			}
			if body, _ := reopened.Drafts.LoadChapterText(1); body != "target-body-two" {
				t.Fatalf("adaptation body did not follow stable target ID: %q", body)
			}
			if check, _ := reopened.Adaptation.LoadCheck(1); check == nil || check.Summary != "target-check-two" {
				t.Fatalf("adaptation check did not follow stable target ID: %+v", check)
			}
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(structureMigrationLogFile))); !os.IsNotExist(err) {
				t.Fatalf("adaptation migration log remains after retry: %v", err)
			}
		})
	}
}

func TestAdaptationIDSavesAreIdempotentAcrossRestartReorderAndAppend(t *testing.T) {
	dir := t.TempDir()
	base := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "volume-one", TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 20},
		},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "target-one", SourceChapters: []int{10}, SourceRange: domain.SourceRange{From: 10, To: 10}},
			{Chapter: 2, Title: "target-two", SourceChapters: []int{20}, SourceRange: domain.SourceRange{From: 20, To: 20}},
		},
	}
	if err := NewStore(dir).Adaptation.SavePlan(base); err != nil {
		t.Fatalf("first SavePlan: %v", err)
	}
	first, err := NewStore(dir).Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("load first plan: %v", err)
	}
	if err := NewStore(dir).Adaptation.SavePlan(base); err != nil {
		t.Fatalf("ID-less SavePlan retry after restart: %v", err)
	}
	second, err := NewStore(dir).Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("load retried plan: %v", err)
	}
	if second.Volumes[0].ID != first.Volumes[0].ID || second.Chapters[0].ID != first.Chapters[0].ID || second.Chapters[1].ID != first.Chapters[1].ID {
		t.Fatalf("ID-less retry replaced IDs: first=%+v second=%+v", first, second)
	}

	reordered := base
	reordered.Chapters = append([]domain.AdaptationChapterPlan(nil), base.Chapters...)
	reordered.Chapters[0], reordered.Chapters[1] = reordered.Chapters[1], reordered.Chapters[0]
	reordered.Chapters = append(reordered.Chapters, domain.AdaptationChapterPlan{
		Chapter: 3, Title: "target-three", SourceChapters: []int{30}, SourceRange: domain.SourceRange{From: 30, To: 30},
	})
	reordered.Volumes = append([]domain.AdaptationVolumePlan(nil), base.Volumes...)
	reordered.Volumes[0].TargetTo = 3
	if err := NewStore(dir).Adaptation.SavePlan(reordered); err != nil {
		t.Fatalf("save ID-less reorder and append: %v", err)
	}
	third, err := NewStore(dir).Adaptation.LoadPlan()
	if err != nil {
		t.Fatalf("load reordered plan: %v", err)
	}
	if third.Chapters[0].ID != first.Chapters[1].ID || third.Chapters[1].ID != first.Chapters[0].ID {
		t.Fatalf("reordered chapters lost identities: %+v", third.Chapters)
	}
	if third.Chapters[2].ID == "" || third.Chapters[2].ID == first.Chapters[0].ID || third.Chapters[2].ID == first.Chapters[1].ID {
		t.Fatalf("appended target did not get a unique ID: %+v", third.Chapters[2])
	}
}

func TestAdaptationStructureSupportsImplicitArcBatchReviews(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Volumes: []domain.AdaptationVolumePlan{
			{Index: 1, Title: "volume-one", TargetFrom: 1, TargetTo: 3},
		},
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "one"},
			{Chapter: 2, Title: "two"},
			{Chapter: 3, Title: "three"},
		},
	}
	if err := st.Adaptation.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	review := domain.ReviewEntry{
		Chapter: 3, Scope: "arc_batch", Volume: 1, Arc: 1,
		BatchFrom: 1, BatchTo: 3, Verdict: "accept",
	}
	if err := st.World.SaveReview(review); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	reviews, err := st.World.LoadArcBatchReviews(1, 1)
	if err != nil {
		t.Fatalf("LoadArcBatchReviews: %v", err)
	}
	if len(reviews) != 1 || reviews[0].Chapter != 3 || reviews[0].Volume != 1 || reviews[0].Arc != 1 {
		t.Fatalf("reviews=%+v", reviews)
	}
}

func TestLegacyAdaptationStructureLoadsImplicitVolumeArc(t *testing.T) {
	dir := t.TempDir()
	io := newIO(dir)
	legacy := structureIndex{
		Version: structureSchemaVersion,
		Volumes: []structureVolumeRef{{ID: "vol_00000000000000000000000000000001", Number: 1}},
		Chapters: []structureChapterRef{
			{ID: "ch_00000000000000000000000000000001", Number: 1, VolumeID: "vol_00000000000000000000000000000001"},
		},
	}
	if err := io.WriteJSON(structureIndexFile, legacy); err != nil {
		t.Fatalf("write legacy index: %v", err)
	}
	index, ok, err := newStructureMigration(dir).loadIndex()
	if err != nil || !ok {
		t.Fatalf("loadIndex: ok=%t err=%v", ok, err)
	}
	arc, ok := index.arcRef(1, 1)
	if !ok || arc.ID == "" || index.Chapters[0].ArcID != arc.ID {
		t.Fatalf("implicit arc was not synthesized: index=%+v", index)
	}
}

func TestAdaptationProposalAndVolumeReviewIDLessRetriesKeepIDs(t *testing.T) {
	dir := t.TempDir()
	review := domain.AdaptationVolumeReview{Volumes: []domain.AdaptationVolumePlan{{Index: 1, Title: "review-volume", TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 2}}}
	if err := NewStore(dir).Adaptation.SaveVolumeReview(review); err != nil {
		t.Fatalf("first volume review: %v", err)
	}
	firstReview, _ := NewStore(dir).Adaptation.LoadVolumeReview()
	if err := NewStore(dir).Adaptation.SaveVolumeReview(review); err != nil {
		t.Fatalf("retry volume review: %v", err)
	}
	secondReview, _ := NewStore(dir).Adaptation.LoadVolumeReview()
	if firstReview.Volumes[0].ID != secondReview.Volumes[0].ID {
		t.Fatalf("volume review retry replaced ID")
	}

	proposal := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Volumes:     review.Volumes,
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "proposal-one", SourceChapters: []int{1}},
			{Chapter: 2, Title: "proposal-two", SourceChapters: []int{2}},
		},
	}
	if err := NewStore(dir).Adaptation.SaveProposal(proposal); err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	firstProposal, _ := NewStore(dir).Adaptation.LoadProposal()
	if err := NewStore(dir).Adaptation.SaveProposal(proposal); err != nil {
		t.Fatalf("retry proposal: %v", err)
	}
	secondProposal, _ := NewStore(dir).Adaptation.LoadProposal()
	if firstProposal.Volumes[0].ID != secondProposal.Volumes[0].ID || firstProposal.Chapters[0].ID != secondProposal.Chapters[0].ID {
		t.Fatalf("proposal retry replaced IDs: first=%+v second=%+v", firstProposal, secondProposal)
	}
}

func TestAdaptationIDLessSaveRejectsAmbiguousExistingMatchBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	plan := domain.AdaptationPlan{
		Granularity: domain.AdaptationGranularityChapter,
		Chapters: []domain.AdaptationChapterPlan{
			{Chapter: 1, Title: "same", SourceChapters: []int{1}},
			{Chapter: 2, Title: "same", SourceChapters: []int{1}},
		},
	}
	st := NewStore(dir)
	st.Adaptation.normalizeAdaptationPlan(&plan)
	plan.Chapters[0].ID = "ch_00000000000000000000000000000001"
	plan.Chapters[1].ID = "ch_00000000000000000000000000000002"
	if err := st.Adaptation.io.WriteJSON(adaptationPlanFile, plan); err != nil {
		t.Fatalf("seed ambiguous plan: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(adaptationPlanFile)))
	if err != nil {
		t.Fatalf("read seeded plan: %v", err)
	}
	plan.Chapters[0].ID = ""
	plan.Chapters[1].ID = ""
	if err := NewStore(dir).Adaptation.SavePlan(plan); err == nil {
		t.Fatal("ambiguous ID-less retry unexpectedly succeeded")
	}
	after, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(adaptationPlanFile)))
	if err != nil {
		t.Fatalf("read plan after rejection: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("ambiguous ID-less retry changed the durable plan")
	}
}

func TestStructurePathsCannotEscapeOutputRoot(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "escaped-structure.json")
	_ = os.Remove(outside)
	io := newIO(dir)
	if err := io.WriteFileUnlocked("../escaped-structure.json", []byte("escape")); err == nil {
		t.Fatal("WriteFileUnlocked accepted parent traversal")
	}
	malicious := []domain.VolumeOutline{{
		ID: "vol_../../escape", Index: 1, Title: "bad",
		Arcs: []domain.ArcOutline{{ID: "arc_../../escape", Index: 1, Chapters: []domain.OutlineEntry{{ID: "ch_../../escape", Chapter: 1}}}},
	}}
	if err := NewStore(dir).Outline.SaveLayeredOutline(malicious); err == nil {
		t.Fatal("SaveLayeredOutline accepted path-bearing structure IDs")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("structure write escaped output root: %v", err)
	}
}

func legacyLayeredMigrationFixture(t *testing.T) (string, []domain.VolumeOutline) {
	t.Helper()
	dir := t.TempDir()
	writeFixtureFile(t, dir, "layered_outline.json", `[
  {"index":1,"title":"one","theme":"one","arcs":[{"index":1,"title":"a","chapters":[{"chapter":1,"title":"chapter-one","scenes":[]}]}]},
  {"index":2,"title":"two","theme":"two","arcs":[{"index":1,"title":"b","chapters":[{"chapter":2,"title":"chapter-two","scenes":[]}]}]}
]`)
	writeFixtureFile(t, dir, "chapters/01.md", "body-one")
	writeFixtureFile(t, dir, "chapters/02.md", "body-two")
	writeFixtureFile(t, dir, "drafts/01.draft.md", "draft-one")
	writeFixtureFile(t, dir, "drafts/02.draft.md", "draft-two")
	writeFixtureFile(t, dir, "drafts/01.plan.json", `{"chapter":1,"title":"one","goal":"plan-one"}`)
	writeFixtureFile(t, dir, "drafts/02.plan.json", `{"chapter":2,"title":"two","goal":"plan-two"}`)
	writeFixtureFile(t, dir, "summaries/01.json", `{"chapter":1,"summary":"summary-one"}`)
	writeFixtureFile(t, dir, "summaries/02.json", `{"chapter":2,"summary":"summary-two"}`)
	writeFixtureFile(t, dir, "reviews/01.json", `{"chapter":1,"scope":"chapter","summary":"review-one","affected_chapters":[2]}`)
	writeFixtureFile(t, dir, "reviews/02.json", `{"chapter":2,"scope":"chapter","summary":"review-two","affected_chapters":[1]}`)
	writeFixtureFile(t, dir, "meta/adaptation/checks/0001.json", `{"chapter":1,"passed":true,"summary":"check-one"}`)
	writeFixtureFile(t, dir, "meta/adaptation/checks/0002.json", `{"chapter":2,"passed":true,"summary":"check-two"}`)
	writeFixtureFile(t, dir, "timeline.json", `[{"chapter":1,"event":"event-one"},{"chapter":2,"event":"event-two"}]`)
	writeFixtureFile(t, dir, "foreshadow_ledger.json", `[{"id":"seed","description":"seed","planted_at":1,"resolved_at":2,"status":"resolved"}]`)
	writeFixtureFile(t, dir, "relationship_state.json", `[{"character_a":"A","character_b":"B","relation":"met","chapter":1}]`)
	writeFixtureFile(t, dir, "meta/state_changes.json", `[{"chapter":2,"entity":"A","field":"status","new_value":"ready"}]`)
	loaded, err := NewStore(dir).Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("load legacy layered outline: %v", err)
	}
	loaded[0], loaded[1] = loaded[1], loaded[0]
	return dir, loaded
}

func legacyAdaptationMigrationFixture(t *testing.T) (string, domain.AdaptationPlan) {
	t.Helper()
	dir := t.TempDir()
	writeFixtureFile(t, dir, adaptationPlanFile, `{
  "granularity":"chapter","status":"confirmed","volumes":[{"index":1,"title":"target-volume","target_from":1,"target_to":2,"source_from":10,"source_to":21}],
  "chapters":[
    {"chapter":1,"title":"target-one","source_chapters":[10,11],"source_range":{"from":10,"to":11}},
    {"chapter":2,"title":"target-two","source_chapters":[20,21],"source_range":{"from":20,"to":21}}
  ]
}`)
	writeFixtureFile(t, dir, "chapters/01.md", "target-body-one")
	writeFixtureFile(t, dir, "chapters/02.md", "target-body-two")
	writeFixtureFile(t, dir, "meta/adaptation/checks/0001.json", `{"chapter":1,"passed":true,"summary":"target-check-one"}`)
	writeFixtureFile(t, dir, "meta/adaptation/checks/0002.json", `{"chapter":2,"passed":true,"summary":"target-check-two"}`)
	plan, err := NewStore(dir).Adaptation.LoadPlan()
	if err != nil || plan == nil {
		t.Fatalf("load legacy adaptation plan: plan=%+v err=%v", plan, err)
	}
	plan.Chapters[0], plan.Chapters[1] = plan.Chapters[1], plan.Chapters[0]
	return dir, *plan
}

func assertMigratedChapterArtifacts(t *testing.T, st *Store) {
	t.Helper()
	if body, _ := st.Drafts.LoadChapterText(1); body != "body-two" {
		t.Fatalf("chapter 1 body=%q", body)
	}
	if draft, _ := st.Drafts.LoadDraft(2); draft != "draft-one" {
		t.Fatalf("chapter 2 draft=%q", draft)
	}
	if plan, _ := st.Drafts.LoadChapterPlan(1); plan == nil || plan.Goal != "plan-two" || plan.Chapter != 1 {
		t.Fatalf("chapter 1 plan=%+v", plan)
	}
	if summary, _ := st.Summaries.LoadSummary(1); summary == nil || summary.Summary != "summary-two" {
		t.Fatalf("chapter 1 summary=%+v", summary)
	}
	if review, _ := st.World.LoadReview(1); review == nil || review.Summary != "review-two" || !reflect.DeepEqual(review.AffectedChapters, []int{2}) {
		t.Fatalf("chapter 1 review=%+v", review)
	}
	if check, _ := st.Adaptation.LoadCheck(1); check == nil || check.Summary != "check-two" {
		t.Fatalf("chapter 1 check=%+v", check)
	}
	timeline, err := st.World.LoadTimeline()
	if err != nil || len(timeline) != 2 || timeline[0].Chapter != 2 || timeline[1].Chapter != 1 {
		t.Fatalf("timeline=%+v err=%v", timeline, err)
	}
	foreshadow, err := st.World.LoadForeshadowLedger()
	if err != nil || len(foreshadow) != 1 || foreshadow[0].PlantedAt != 2 || foreshadow[0].ResolvedAt != 1 {
		t.Fatalf("foreshadow=%+v err=%v", foreshadow, err)
	}
	relationships, err := st.World.LoadRelationships()
	if err != nil || len(relationships) != 1 || relationships[0].Chapter != 2 {
		t.Fatalf("relationships=%+v err=%v", relationships, err)
	}
	changes, err := st.World.LoadStateChanges()
	if err != nil || len(changes) != 1 || changes[0].Chapter != 1 {
		t.Fatalf("state changes=%+v err=%v", changes, err)
	}
}

func writeFixtureFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
