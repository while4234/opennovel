package domain

import (
	"testing"
)

func TestFoundationDiffIsCanonicalAndStableIDBased(t *testing.T) {
	base := foundationDiffFixture()
	candidate := CloneStoryFoundation(base)
	candidate.Revision = 99
	candidate.UpdatedAt = "later"
	candidate.Characters = []Character{candidate.Characters[1], candidate.Characters[0]}
	candidate.Characters[0].Name = "  Supporting Hero  "
	candidate.WorldRules[0].Tags = []string{"beta", "alpha"}
	diff, err := ComputeFoundationDiff(base, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Changes) != 0 {
		t.Fatalf("metadata/order/whitespace produced diff: %+v", diff.Changes)
	}

	candidate = CloneStoryFoundation(base)
	candidate.Characters[0].ID = "char-replacement"
	diff, err = ComputeFoundationDiff(base, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Changes) != 2 || diff.Changes[0].Kind != FoundationChangeRemoved || diff.Changes[1].Kind != FoundationChangeAdded {
		t.Fatalf("same-name stable-ID replacement = %+v, want remove+add", diff.Changes)
	}
}

func TestFoundationImpactRequiresEvidenceAndExpandsHardRules(t *testing.T) {
	base := foundationDiffFixture()
	candidate := CloneStoryFoundation(base)
	candidate.Characters[1].Goal = "new local goal"
	diff, err := ComputeFoundationDiff(base, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := AnalyzeFoundationImpact(diff, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !impact.FullBook || impact.EvidenceLevel != "missing" {
		t.Fatalf("legacy impact = %+v", impact)
	}

	candidate = CloneStoryFoundation(base)
	candidate.WorldRules[0].Rule = "changed hard rule"
	diff, err = ComputeFoundationDiff(base, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewFoundationDependencyManifest(mustFoundationAuditSignature(t, base), []FoundationDependency{{ID: "dep-1", SourceEntityType: FoundationEntityWorldRule, SourceEntityID: "rule-1", DependentArtifactType: "chapter", DependentArtifactID: "chapter-1", ChapterID: "chapter-1", DependencyKind: "constraint", FoundationSignature: mustFoundationAuditSignature(t, base), DependentContentSignature: ContentSignature([]byte("chapter")), EvidenceSource: "generator_parameter"}})
	if err != nil {
		t.Fatal(err)
	}
	impact, err = AnalyzeFoundationImpact(diff, &manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !impact.FullBook || impact.Reasons[0].Code != "hard_world_rule_changed" {
		t.Fatalf("hard-rule impact = %+v", impact)
	}
}

func TestFoundationImpactAllowsStructuredLocalScope(t *testing.T) {
	base := foundationDiffFixture()
	candidate := CloneStoryFoundation(base)
	candidate.Characters[1].Goal = "new local goal"
	diff, err := ComputeFoundationDiff(base, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	signature := mustFoundationAuditSignature(t, base)
	manifest, err := NewFoundationDependencyManifest(signature, []FoundationDependency{{ID: "dep-local", SourceEntityType: FoundationEntityCharacter, SourceEntityID: "char-support", DependentArtifactType: "chapter", DependentArtifactID: "chapter-1", VolumeID: "volume-1", ArcID: "arc-1", ChapterID: "chapter-1", DependencyKind: "explicit_character_id", FoundationSignature: signature, DependentContentSignature: ContentSignature([]byte("chapter")), EvidenceSource: "structured_generation_output"}})
	if err != nil {
		t.Fatal(err)
	}
	impact, err := AnalyzeFoundationImpact(diff, &manifest)
	if err != nil {
		t.Fatal(err)
	}
	if impact.FullBook || impact.EvidenceLevel != "structured" || len(impact.AffectedChapterIDs) != 1 || impact.AffectedChapterIDs[0] != "chapter-1" {
		t.Fatalf("local impact = %+v", impact)
	}
}

func foundationDiffFixture() StoryFoundation {
	return StoryFoundation{SchemaVersion: StoryFoundationSchemaVersion, Revision: 3, Premise: "A stable premise", Characters: []Character{{ID: "char-core", Name: "Core Hero", Role: "lead", Description: "lead", Arc: "arc", Traits: []string{"brave"}}, {ID: "char-support", Name: "Supporting Hero", Role: "support", Description: "support", Arc: "arc", Traits: []string{"kind"}}}, WorldRules: []WorldRule{{ID: "rule-1", Category: "magic", Rule: "A hard rule", Boundary: "never break", Strength: WorldRuleStrengthHard, Tags: []string{"alpha", "beta"}}}}
}

func mustFoundationAuditSignature(t *testing.T, value StoryFoundation) string {
	t.Helper()
	signature, err := FoundationAuditSignature(value)
	if err != nil {
		t.Fatal(err)
	}
	return signature
}
