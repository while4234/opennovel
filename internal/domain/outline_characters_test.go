package domain

import (
	"errors"
	"testing"
)

func TestPrepareOutlineCharactersUsesStableIDsAndLegacyAliasFallback(t *testing.T) {
	characters := []Character{
		{ID: "hero", Name: "林", Aliases: []string{"导师"}, Tier: "core"},
		{ID: "ally", Name: "岚", Tier: "important"},
	}
	entries := []OutlineEntry{
		{Chapter: 1, ID: "chapter-one", CoreEvent: "导师给出选择"},
		{Chapter: 2, ID: "chapter-two", CharacterIDs: []string{"ally"}, CharacterBeats: []OutlineCharacterBeat{{CharacterID: "hero", Goal: "阻止误判"}}},
	}
	got, err := PrepareOutlineCharacters(entries, characters)
	if err != nil {
		t.Fatalf("PrepareOutlineCharacters: %v", err)
	}
	if len(got[0].CharacterIDs) != 1 || got[0].CharacterIDs[0] != "hero" {
		t.Fatalf("legacy alias fallback = %+v", got[0].CharacterIDs)
	}
	if got[0].ID != "chapter-one" || got[0].Chapter != 1 {
		t.Fatalf("stable outline identity changed: %+v", got[0])
	}
	if len(got[1].CharacterIDs) != 2 {
		t.Fatalf("beat ID was not merged: %+v", got[1].CharacterIDs)
	}
}

func TestPrepareOutlineCharactersRoutesUnknownImportantRole(t *testing.T) {
	_, err := PrepareOutlineCharacters([]OutlineEntry{{
		Chapter:        3,
		CharacterIDs:   []string{"unknown"},
		TemporaryRoles: []TemporaryCharacterNeed{{Role: "new antagonist", Important: true}},
	}}, []Character{{ID: "hero", Name: "林"}})
	var gapErr *OutlineCharacterGapError
	if !errors.As(err, &gapErr) || len(gapErr.Gaps) != 2 {
		t.Fatalf("gap error = %#v", err)
	}
	for _, gap := range gapErr.Gaps {
		if gap.Route != "character" {
			t.Fatalf("gap route = %+v", gap)
		}
	}
}

func TestPrepareOutlineCharactersHydratesRelationshipIDFromFoundation(t *testing.T) {
	characters := []Character{
		{ID: "lin_shuran", Name: "林舒然"},
		{ID: "su_jinchen", Name: "苏瑾琛"},
	}
	relationships := []CharacterRelationship{{
		ID:                "rel_lin_su",
		SourceCharacterID: "lin_shuran",
		TargetCharacterID: "su_jinchen",
	}}
	entries := []OutlineEntry{{
		Chapter:   1,
		CoreEvent: "林舒然拒绝苏瑾琛的安排，关系进入新一轮控制与反控制。",
		RelationshipBeats: []OutlineRelationshipBeat{{
			RelationshipID:  "rel_lin_su",
			ExpectedAdvance: "表面顺从转为暗中反制",
		}},
	}}

	got, err := PrepareOutlineCharactersWithRelationships(entries, characters, relationships)
	if err != nil {
		t.Fatalf("PrepareOutlineCharactersWithRelationships: %v", err)
	}
	beat := got[0].RelationshipBeats[0]
	if beat.SourceCharacterID != "lin_shuran" || beat.TargetCharacterID != "su_jinchen" {
		t.Fatalf("relationship endpoints = %+v", beat)
	}
}

func TestPrepareOutlineCharactersNormalizesNamesAndUniqueConfirmedPair(t *testing.T) {
	characters := []Character{
		{ID: "lin_shuran", Name: "林舒然"},
		{ID: "su_jinchen", Name: "苏瑾琛", Aliases: []string{"苏总"}},
	}
	relationships := []CharacterRelationship{{
		ID:                "rel_lin_su",
		SourceCharacterID: "lin_shuran",
		TargetCharacterID: "su_jinchen",
	}}
	entries := []OutlineEntry{
		{
			Chapter: 1,
			RelationshipBeats: []OutlineRelationshipBeat{{
				SourceCharacterID: "林舒然",
				TargetCharacterID: "苏总",
			}},
		},
		{
			Chapter:   2,
			CoreEvent: "林舒然识破苏瑾琛的试探并改变撤离计划。",
			RelationshipBeats: []OutlineRelationshipBeat{{
				Scene: "二人在家宴后第一次正面交锋",
			}},
		},
	}

	got, err := PrepareOutlineCharactersWithRelationships(entries, characters, relationships)
	if err != nil {
		t.Fatalf("PrepareOutlineCharactersWithRelationships: %v", err)
	}
	for index, entry := range got {
		beat := entry.RelationshipBeats[0]
		if beat.RelationshipID != "rel_lin_su" ||
			beat.SourceCharacterID != "lin_shuran" ||
			beat.TargetCharacterID != "su_jinchen" {
			t.Fatalf("entry %d relationship beat = %+v", index+1, beat)
		}
	}
}

func TestPrepareOutlineCharactersDoesNotGuessAmbiguousRelationshipDirection(t *testing.T) {
	characters := []Character{
		{ID: "lin_shuran", Name: "林舒然"},
		{ID: "su_jinchen", Name: "苏瑾琛"},
	}
	relationships := []CharacterRelationship{
		{ID: "lin_to_su", SourceCharacterID: "lin_shuran", TargetCharacterID: "su_jinchen"},
		{ID: "su_to_lin", SourceCharacterID: "su_jinchen", TargetCharacterID: "lin_shuran"},
	}
	_, err := PrepareOutlineCharactersWithRelationships([]OutlineEntry{{
		Chapter:   3,
		CoreEvent: "林舒然与苏瑾琛重新评估彼此。",
		RelationshipBeats: []OutlineRelationshipBeat{{
			ExpectedAdvance: "互相试探加深",
		}},
	}}, characters, relationships)
	var gapErr *OutlineCharacterGapError
	if !errors.As(err, &gapErr) {
		t.Fatalf("ambiguous relationship should remain a gap: %v", err)
	}
}
