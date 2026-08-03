package domain

import (
	"encoding/json"
	"testing"
)

func TestStoryFoundationLegacyJSONCompatibilityAndStableIDs(t *testing.T) {
	var character Character
	if err := json.Unmarshal([]byte(`{"name":"Lin","role":"hero","description":"desc","arc":"arc","traits":["brave"]}`), &character); err != nil {
		t.Fatal(err)
	}
	var rule WorldRule
	if err := json.Unmarshal([]byte(`{"category":"magic","rule":"Costs memory","boundary":"No free spell"}`), &rule); err != nil {
		t.Fatal(err)
	}
	first, err := NormalizeStoryFoundation(StoryFoundation{Characters: []Character{character}, WorldRules: []WorldRule{rule}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Characters[0].ID == "" || first.WorldRules[0].ID == "" {
		t.Fatalf("legacy IDs were not filled: %+v", first)
	}
	if first.WorldRules[0].Strength != WorldRuleStrengthHard {
		t.Fatalf("legacy strength = %q", first.WorldRules[0].Strength)
	}
	renamed := CloneStoryFoundation(first)
	renamed.Characters[0].Name = "Lin the Elder"
	renamed.WorldRules[0].Title = "Memory Price"
	second, err := NormalizeStoryFoundation(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Characters[0].ID != first.Characters[0].ID || second.WorldRules[0].ID != first.WorldRules[0].ID {
		t.Fatal("renaming changed an existing stable ID")
	}
}

func TestStoryFoundationV1MutualMigrationIsIdempotentAndSignatureStable(t *testing.T) {
	legacy := StoryFoundation{
		SchemaVersion: legacyStoryFoundationSchemaVersion,
		Characters:    []Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}},
		Relationships: []CharacterRelationship{{
			ID: "bond", SourceCharacterID: "lin", TargetCharacterID: "mara",
			Type: RelationshipTypeAlly, Direction: RelationshipDirectionMutual,
		}},
	}
	migrated, err := NormalizeStoryFoundation(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.SchemaVersion != StoryFoundationSchemaVersion || migrated.Relationships[0].Direction != RelationshipDirectionBidirectional {
		t.Fatalf("migration = %+v", migrated)
	}
	again, err := NormalizeStoryFoundation(migrated)
	if err != nil || again.Relationships[0].Direction != RelationshipDirectionBidirectional {
		t.Fatalf("second migration = %+v, %v", again, err)
	}
	legacySignature, err := FoundationContentSignature(legacy)
	if err != nil {
		t.Fatal(err)
	}
	currentSignature, err := FoundationContentSignature(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if currentSignature != legacySignature {
		t.Fatalf("legacy semantic signature drifted: %s != %s", legacySignature, currentSignature)
	}
}

func TestStoryFoundationDirectionsAreDistinctControlledSemantics(t *testing.T) {
	base := StoryFoundation{Characters: []Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}}}
	for _, direction := range []RelationshipDirection{RelationshipDirectionDirected, RelationshipDirectionBidirectional, RelationshipDirectionUndirected} {
		candidate := CloneStoryFoundation(base)
		candidate.Relationships = []CharacterRelationship{{ID: string(direction), SourceCharacterID: "lin", TargetCharacterID: "mara", Type: RelationshipTypeAlly, Direction: direction}}
		if _, err := NormalizeStoryFoundation(candidate); err != nil {
			t.Fatalf("direction %q rejected: %v", direction, err)
		}
	}
}

func TestStoryFoundationNormalizationRejectsAmbiguousIdentityAndRelationships(t *testing.T) {
	tests := []struct {
		name  string
		value StoryFoundation
	}{
		{
			name: "alias collision",
			value: StoryFoundation{Characters: []Character{
				{Name: "Lin", Aliases: []string{"Captain"}},
				{Name: "Mara", Aliases: []string{" captain "}},
			}},
		},
		{
			name:  "duplicate character id",
			value: StoryFoundation{Characters: []Character{{ID: "same", Name: "Lin"}, {ID: "same", Name: "Mara"}}},
		},
		{
			name: "dangling relationship",
			value: StoryFoundation{
				Characters:    []Character{{ID: "lin", Name: "Lin"}},
				Relationships: []CharacterRelationship{{SourceCharacterID: "lin", TargetCharacterID: "missing", Type: RelationshipTypeAlly}},
			},
		},
		{
			name: "self loop",
			value: StoryFoundation{
				Characters:    []Character{{ID: "lin", Name: "Lin"}},
				Relationships: []CharacterRelationship{{SourceCharacterID: "lin", TargetCharacterID: "lin", Type: RelationshipTypeOther}},
			},
		},
		{
			name: "duplicate semantic edge",
			value: StoryFoundation{
				Characters: []Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}},
				Relationships: []CharacterRelationship{
					{ID: "one", SourceCharacterID: "lin", TargetCharacterID: "mara", Type: RelationshipTypeAlly, Direction: RelationshipDirectionMutual},
					{ID: "two", SourceCharacterID: "mara", TargetCharacterID: "lin", Type: RelationshipTypeAlly, Direction: RelationshipDirectionMutual},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeStoryFoundation(test.value); err == nil {
				t.Fatal("invalid foundation was accepted")
			}
		})
	}
}

func TestStoryFoundationSignaturesSeparateSemanticAndReviewMetadata(t *testing.T) {
	base, err := NormalizeStoryFoundation(StoryFoundation{
		Premise:       "A promise has a price.",
		Characters:    []Character{{ID: "lin", Name: "Lin"}, {ID: "mara", Name: "Mara"}},
		Relationships: []CharacterRelationship{{ID: "bond", SourceCharacterID: "lin", TargetCharacterID: "mara", Type: RelationshipTypeAlly}},
		WorldRules:    []WorldRule{{ID: "cost", Category: "magic", Rule: "Magic costs memory"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	changedMetadata := CloneStoryFoundation(base)
	changedMetadata.Revision = 99
	changedMetadata.UpdatedAt = "later"
	changedMetadata.RelationshipsReviewed = true
	contentA, _ := FoundationContentSignature(base)
	contentB, _ := FoundationContentSignature(changedMetadata)
	auditA, _ := FoundationAuditSignature(base)
	auditB, _ := FoundationAuditSignature(changedMetadata)
	if contentA != contentB || auditA != auditB {
		t.Fatal("revision, timestamp, or review state drifted semantic signatures")
	}
	reviewA, _ := FoundationReviewConfirmationSignature(base)
	reviewB, _ := FoundationReviewConfirmationSignature(changedMetadata)
	if reviewA == reviewB {
		t.Fatal("review confirmation signature ignored review state")
	}
	changedContent := CloneStoryFoundation(base)
	changedContent.Premise = "A different premise."
	contentC, _ := FoundationContentSignature(changedContent)
	if contentA == contentC {
		t.Fatal("semantic content change did not change signature")
	}
	equal, err := StoryFoundationSectionEqual(base, changedContent, FoundationSectionCharacters)
	if err != nil || !equal {
		t.Fatalf("unmodified section comparison = %v, %v", equal, err)
	}
	equal, err = StoryFoundationSectionEqual(base, changedContent, FoundationSectionPremise)
	if err != nil || equal {
		t.Fatalf("modified section comparison = %v, %v", equal, err)
	}
}

func TestCloneStoryFoundationIsDeep(t *testing.T) {
	original := StoryFoundation{
		Characters:    []Character{{Name: "Lin", Aliases: []string{"L"}, Constraints: []string{"honest"}}},
		Relationships: []CharacterRelationship{{Tags: []string{"old"}}},
		WorldRules:    []WorldRule{{Tags: []string{"magic"}}},
	}
	clone := CloneStoryFoundation(original)
	clone.Characters[0].Aliases[0] = "changed"
	clone.Characters[0].Constraints[0] = "changed"
	clone.Relationships[0].Tags[0] = "changed"
	clone.WorldRules[0].Tags[0] = "changed"
	if original.Characters[0].Aliases[0] != "L" || original.Characters[0].Constraints[0] != "honest" || original.Relationships[0].Tags[0] != "old" || original.WorldRules[0].Tags[0] != "magic" {
		t.Fatal("clone mutated original nested slices")
	}
}
