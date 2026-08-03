package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalRevisionPolicyRejectsOriginalNovelFields(t *testing.T) {
	impact := normalPolicyImpact(t, []RevisionImpactItem{{
		ArtifactID: "ch-1", ArtifactKind: StructureKindChapter, Change: "revise outline",
		Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency,
	}})
	session := RevisionSession{Mode: RevisionModeNormal, Impact: impact}
	payload := json.RawMessage(`{"title":"new outline","sourceChapters":[1]}`)
	version := normalPolicyVersion("ch-1", StructureKindChapter, payload)
	err := (NormalRevisionPolicy{}).ValidateCandidate(session, []ArtifactVersion{version})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("normal candidate with original-novel fields was accepted: %v", err)
	}
}

func TestNormalRevisionPolicyOrdersStructureOutlineAndMinimalProseApproval(t *testing.T) {
	impact := normalPolicyImpact(t, []RevisionImpactItem{
		{ArtifactID: "vol-2", ArtifactKind: StructureKindVolume, Change: "insert volume", Requirement: StructureImpactRequired, Cause: StructureImpactStructureChange},
		{ArtifactID: "ch-9", ArtifactKind: StructureKindChapter, Change: "rewrite affected chapter", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency, RequiresBodyRewrite: true},
		{ArtifactID: "ch-10", ArtifactKind: StructureKindChapter, Change: "display renumber only", Requirement: StructureImpactRecommended, Cause: StructureImpactDisplayRenumber},
	})
	stages, err := (NormalRevisionPolicy{}).ApprovalStages(impact)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{NormalApprovalStructure, NormalApprovalOutline, NormalApprovalProse}
	for index, stage := range stages {
		if index >= len(want) || stage.ID != want[index] {
			t.Fatalf("approval stages = %+v, want %v", stages, want)
		}
	}
	if len(stages) != len(want) {
		t.Fatalf("approval stages = %+v, want %v", stages, want)
	}

	session := RevisionSession{Mode: RevisionModeNormal, Impact: impact}
	versions := []ArtifactVersion{
		normalPolicyVersion("vol-2", StructureKindVolume, json.RawMessage(`{"entry_state":"old phase closed","independent_conflict":"new succession conflict","arc_progression":"alliances split and recombine","climax":"the capital changes hands","irreversible_outcome":"the realm is divided","cannot_fit_current_volume":"the prior phase has already paid off","soft_budget":{"estimated_chapters":6,"chapter_min_words":3000,"chapter_max_words":5000,"target_total_words":24000,"total_min_words":18000,"total_max_words":30000}}`)),
		normalPolicyVersion(NormalStructureSnapshotID, NormalArtifactStructureSnapshot, json.RawMessage(`[{"id":"vol-2","index":1,"arcs":[{"id":"arc-2","index":1,"chapters":[{"id":"ch-9","chapter":1}]}]}]`)),
	}
	if err := (NormalRevisionPolicy{}).ValidateCandidate(session, versions); err != nil {
		t.Fatalf("minimal affected candidate rejected: %v", err)
	}
}

func TestValidateNormalBatchPlanRejectsForbiddenContext(t *testing.T) {
	plan := BatchPlan{
		Batches: []BatchWork{{
			ID: "batch-001", Index: 1, ChapterIDs: []string{"ch-1"}, VolumeID: "vol-1", ArcID: "arc-1",
			Context: []BatchContextItem{{ID: "forbidden", Kind: BatchContextSourceAnchor, Necessary: true}},
		}},
		VolumeReviews:   []BatchAggregateReview{{ScopeID: "vol-1", Status: BatchReviewPending}},
		WholeBookReview: BatchAggregateReview{ScopeID: "whole-book", Status: BatchReviewPending},
	}
	if err := ValidateNormalBatchPlan(plan); err == nil {
		t.Fatal("normal batch plan accepted forbidden original-novel context")
	}
}

func TestNormalRevisionPolicyRejectsProseContentInReworkIntent(t *testing.T) {
	chapterID := LegacyStructureID("normal-prose-policy", StructureKindChapter, "one")
	impact := normalPolicyImpact(t, []RevisionImpactItem{
		{ArtifactID: chapterID, ArtifactKind: StructureKindChapter, Change: "rewrite", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency, RequiresBodyRewrite: true},
		{ArtifactID: "rework:" + chapterID, ArtifactKind: NormalArtifactProseReworkIntent, Change: "intent", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency},
		{ArtifactID: NormalProseReworkQueueID, ArtifactKind: NormalArtifactProseReworkQueue, Change: "queue", Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency},
	})
	session := RevisionSession{Mode: RevisionModeNormal, Impact: impact, ApprovalStages: []RevisionApprovalStage{{ID: NormalApprovalOutline}, {ID: NormalApprovalProse}}, Approvals: []RevisionApproval{{StageID: NormalApprovalOutline}}}
	versions := []ArtifactVersion{
		normalPolicyVersion("rework:"+chapterID, NormalArtifactProseReworkIntent, json.RawMessage(`{"chapter_id":"`+chapterID+`","current_number":1,"volume_id":"vol","arc_id":"arc","reason":"dependency","body":"forbidden generated prose"}`)),
		normalPolicyVersion(NormalProseReworkQueueID, NormalArtifactProseReworkQueue, json.RawMessage(`{"chapter_ids":["`+chapterID+`"]}`)),
	}
	if err := (NormalRevisionPolicy{}).ValidateCandidate(session, versions); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("prose body escaped PR-06 boundary: %v", err)
	}
}

func normalPolicyImpact(t *testing.T, items []RevisionImpactItem) RevisionImpact {
	t.Helper()
	for index := range items {
		if len(items[index].DependencyEvidence) == 0 {
			items[index].DependencyEvidence = []string{"current original-fiction structure"}
		}
	}
	items = append(items, RevisionImpactItem{
		ArtifactID: "normal-batch-plan", ArtifactKind: NormalArtifactBatchPlan, Change: "bounded work",
		Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency,
		DependencyEvidence: []string{"bounded generation and review"},
	})
	items = append(items, RevisionImpactItem{
		ArtifactID: NormalStructureSnapshotID, ArtifactKind: NormalArtifactStructureSnapshot, Change: "bind snapshot",
		Requirement: StructureImpactRequired, Cause: StructureImpactContentDependency,
		DependencyEvidence: []string{"accepted version binding"},
	})
	impact, err := NewRevisionImpact("normal revision", items)
	if err != nil {
		t.Fatal(err)
	}
	return impact
}

func normalPolicyBatchPlanPayload() json.RawMessage {
	return json.RawMessage(`{"batches":[{"id":"batch-001","index":1,"chapter_ids":["ch-9"],"volume_id":"vol-2","arc_id":"arc-2","estimated_output_words":3000,"status":"pending"}],"volume_reviews":[{"scope_id":"vol-2","status":"pending"}],"whole_book_review":{"scope_id":"whole-book","status":"pending"}}`)
}

func normalPolicyVersion(id, kind string, payload json.RawMessage) ArtifactVersion {
	return ArtifactVersion{
		ID: "version-" + id, ArtifactID: id, ArtifactKind: kind, Sequence: 1, Round: 1,
		Payload: payload, ContentSignature: JSONContentSignature(payload), CreatedAt: RevisionTimestamp(),
	}
}
