package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestOriginalPlanningPassIsNotReusedAfterScopedContentChanges(t *testing.T) {
	st := approvedFoundationReviewTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	audit := domain.OriginalPlanningAudit{Scope: "arc", Volume: 1, Arc: 1, FromChapter: 1, ToChapter: 1, Verdict: "pass", Summary: "current"}
	if err := domain.BindOriginalPlanningAudit(&audit, volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.Save(audit); err != nil {
		t.Fatal(err)
	}
	if current, err := st.OriginalPlanningAudits.Get("arc", 1, 1); err != nil || current == nil {
		t.Fatalf("current pass missing: current=%+v err=%v", current, err)
	}

	volumes[0].Arcs[0].Chapters[0].CoreEvent = "a materially different consequence"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if stale, err := st.OriginalPlanningAudits.Get("arc", 1, 1); err != nil || stale != nil {
		t.Fatalf("stale pass was reused: stale=%+v err=%v", stale, err)
	}
	work, err := st.OriginalPlanningAudits.NextWork(st.Outline)
	if err != nil || work == nil || work.Kind != "audit_chapter" || work.FromChapter != 1 || work.ToChapter != 1 {
		t.Fatalf("next work after signature change = %+v err=%v", work, err)
	}
}

func TestSkeletonAuditProjectsDetailedChaptersOutOfItsSignature(t *testing.T) {
	st := approvedFoundationReviewTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	audit := domain.OriginalPlanningAudit{Scope: "skeleton_volume", Volume: 1, Verdict: "pass", Summary: "skeleton current"}
	if err := st.OriginalPlanningAudits.Save(audit); err != nil {
		t.Fatal(err)
	}
	volumes[0].Arcs[0].Chapters[0].CoreEvent = "detail changed after skeleton approval"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if current, err := st.OriginalPlanningAudits.Get("skeleton_volume", 1, 0); err != nil || current == nil {
		t.Fatalf("detail-only change invalidated skeleton projection: current=%+v err=%v", current, err)
	}
	volumes[0].Arcs[0].Goal = "a different causal phase contract"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if stale, err := st.OriginalPlanningAudits.Get("skeleton_volume", 1, 0); err != nil || stale != nil {
		t.Fatalf("changed skeleton contract reused old pass: stale=%+v err=%v", stale, err)
	}
}

func TestOriginalPlanningAuditBindsFoundationAndFailsClosedWhenFoundationChanges(t *testing.T) {
	st := approvedFoundationReviewTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	audit := domain.OriginalPlanningAudit{Scope: "arc", Volume: 1, Arc: 1, FromChapter: 1, ToChapter: 1, Verdict: "pass", Summary: "bound"}
	if err := st.OriginalPlanningAudits.Save(audit); err != nil {
		t.Fatal(err)
	}
	current, err := st.OriginalPlanningAudits.Get("arc", 1, 1)
	if err != nil || current == nil || current.FoundationRevision <= 0 || current.FoundationSignature == "" {
		t.Fatalf("foundation-bound audit = %+v err=%v", current, err)
	}
	if err := st.Foundation.updatePremise("a changed canonical premise"); err != nil {
		t.Fatal(err)
	}
	if stale, err := st.OriginalPlanningAudits.Get("arc", 1, 1); foundationReviewCode(err) != FoundationReviewErrorStale || stale != nil {
		t.Fatalf("foundation-stale audit did not fail closed: stale=%+v err=%v", stale, err)
	}
}

func TestOriginalPlanningAuditRejectsLegacyPassWithoutFoundationSignature(t *testing.T) {
	st := approvedFoundationReviewTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	audit := domain.OriginalPlanningAudit{Scope: "arc", Volume: 1, Arc: 1, FromChapter: 1, ToChapter: 1, Verdict: "pass", Summary: "legacy"}
	if err := domain.BindOriginalPlanningAudit(&audit, volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.io.WriteJSON(originalPlanningAuditsFile, []domain.OriginalPlanningAudit{audit}); err != nil {
		t.Fatal(err)
	}
	if legacy, err := st.OriginalPlanningAudits.Get("arc", 1, 1); err != nil || legacy != nil {
		t.Fatalf("legacy unsigned pass was reused: legacy=%+v err=%v", legacy, err)
	}
}

func TestInvalidateRepairConsumesFailedChapterAudit(t *testing.T) {
	st := approvedFoundationReviewTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	first := volumes[0].Arcs[0].Chapters[0]
	second := first
	second.ID = domain.LegacyStructureID("audit-test", domain.StructureKindChapter, "volume-1/arc-1/chapter-2")
	second.Chapter = 2
	second.Title = "Bridge"
	second.CoreEvent = "the warning forces a costly crossing"
	volumes[0].Arcs[0].Chapters = append(volumes[0].Arcs[0].Chapters, second)
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.Save(domain.OriginalPlanningAudit{
		Scope: "chapter", ScopeID: first.ID, FromChapter: 1, ToChapter: 1, Verdict: "pass", Summary: "chapter one passes",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.Save(domain.OriginalPlanningAudit{
		Scope: "chapter", ScopeID: second.ID, FromChapter: 2, ToChapter: 2, Verdict: "revise", Summary: "chapter two needs repair",
		Issues: []domain.OriginalPlanningAuditIssue{{Volume: 1, Arc: 1, FromChapter: 2, ToChapter: 2, Description: "missing consequence", RepairInstruction: "add an irreversible consequence"}},
	}); err != nil {
		t.Fatal(err)
	}

	work, err := st.OriginalPlanningAudits.NextWork(st.Outline)
	if err != nil || work == nil || work.Kind != "repair_arc" || work.FromChapter != 2 {
		t.Fatalf("work before repair = %+v err=%v", work, err)
	}
	if err := st.OriginalPlanningAudits.InvalidateRepair(1, 1, 2, 2); err != nil {
		t.Fatal(err)
	}

	work, err = st.OriginalPlanningAudits.NextWork(st.Outline)
	if err != nil || work == nil || work.Kind != "audit_chapter" || work.FromChapter != 2 || work.ToChapter != 2 {
		t.Fatalf("work after repair = %+v err=%v", work, err)
	}
}

func TestNextWorkIgnoresFailedChapterAuditAfterContentChanges(t *testing.T) {
	st := approvedFoundationReviewTestStore(t)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := originalAuditSignatureStructure()
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	failed := domain.OriginalPlanningAudit{
		Scope: "chapter", ScopeID: volumes[0].Arcs[0].Chapters[0].ID, FromChapter: 1, ToChapter: 1, Verdict: "revise", Summary: "chapter needs repair",
		Issues: []domain.OriginalPlanningAuditIssue{{Volume: 1, Arc: 1, FromChapter: 1, ToChapter: 1, Description: "missing consequence", RepairInstruction: "add an irreversible consequence"}},
	}
	if err := domain.BindOriginalPlanningAudit(&failed, volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.OriginalPlanningAudits.Save(failed); err != nil {
		t.Fatal(err)
	}
	if work, err := st.OriginalPlanningAudits.NextWork(st.Outline); err != nil || work == nil || work.Kind != "repair_arc" {
		t.Fatalf("current failed audit work = %+v err=%v", work, err)
	}

	volumes[0].Arcs[0].Chapters[0].CoreEvent = "the warning now forces an irreversible departure"
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	work, err := st.OriginalPlanningAudits.NextWork(st.Outline)
	if err != nil || work == nil || work.Kind != "audit_chapter" || work.FromChapter != 1 {
		t.Fatalf("work after signed failed audit became stale = %+v err=%v", work, err)
	}
}

func originalAuditSignatureStructure() []domain.VolumeOutline {
	return []domain.VolumeOutline{{
		ID: domain.LegacyStructureID("audit-test", domain.StructureKindVolume, "volume-1"), Index: 1, Title: "Opening", Theme: "trust",
		Arcs: []domain.ArcOutline{{
			ID: domain.LegacyStructureID("audit-test", domain.StructureKindArc, "volume-1/arc-1"), Index: 1, Title: "Arrival", Goal: "form an alliance",
			Chapters: []domain.OutlineEntry{{
				ID: domain.LegacyStructureID("audit-test", domain.StructureKindChapter, "volume-1/arc-1/chapter-1"), Chapter: 1, Title: "Gate", CoreEvent: "the leads meet", Hook: "a warning", Scenes: []string{"the guarded gate"},
			}},
		}},
	}}
}
