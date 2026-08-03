package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestAdaptationSourceCharacterIndexAndCoverageIncludeNonCoreCast(t *testing.T) {
	source := &AdaptationSourceFoundation{
		SourceSignature: strings.Repeat("a", 64),
		Characters: []Character{
			{ID: "src-hero", Name: "林舟", Role: "主角", Traits: []string{"克制"}},
			{ID: "src-villain", Name: "顾沉", Role: "反派", Traits: []string{"强硬"}},
			{ID: "src-friend", Name: "周宁", Aliases: []string{"阿宁"}, Role: "朋友", Goal: "保护林舟", Traits: []string{"热心"}},
			{ID: "src-mentor", Name: "沈老师", Role: "导师", Motivation: "弥补旧错", Traits: []string{"审慎"}},
			{ID: "src-passerby", Name: "路人", Role: "路人", Traits: []string{}},
		},
	}
	reports := []AdaptationSourceReport{
		{
			Chapter: 1, SourceSHA256: strings.Repeat("1", 64),
			Characters: []string{"林舟", "顾沉", "阿宁", "沈老师", "路人"},
			KeyEvents:  []string{"林舟拒绝顾沉的交易"},
			Relationships: []RelationshipEntry{{
				CharacterA: "林舟", CharacterB: "沈老师", Relation: "师生关系受考验",
			}},
		},
		{
			Chapter: 2, SourceSHA256: strings.Repeat("2", 64),
			Characters: []string{"林舟", "阿宁"},
			KeyEvents:  []string{"周宁独自追回关键证据并交给林舟"},
		},
		{
			Chapter: 3, SourceSHA256: strings.Repeat("3", 64),
			Characters: []string{"林舟", "顾沉", "周宁", "沈老师"},
			KeyEvents:  []string{"沈老师承担代价阻止顾沉"},
			StateChanges: []StateChange{{
				Entity: "沈老师", Field: "立场", OldValue: "旁观", NewValue: "公开帮助林舟", Reason: "弥补旧错",
			}},
		},
	}
	dossier := &AdaptationCoCreateDossier{
		Batches: []AdaptationCoCreateDossierBatch{{
			MajorCharacters: []string{"林舟", "顾沉"},
		}},
	}

	index, err := BuildAdaptationSourceCharacterIndex(source, reports, dossier, nil)
	if err != nil {
		t.Fatal(err)
	}
	if index.Version != AdaptationSourceCharacterIndexVersion || len(index.InputSignature) != 64 {
		t.Fatalf("index identity = %+v", index)
	}
	if len(index.Characters) != 5 {
		t.Fatalf("characters = %+v", index.Characters)
	}
	friend := indexedSourceCharacter(t, index, "src-friend")
	if !reflect.DeepEqual(friend.Aliases, []string{"阿宁"}) || friend.AppearanceCount != 3 {
		t.Fatalf("friend index = %+v", friend)
	}
	mentor := indexedSourceCharacter(t, index, "src-mentor")
	if len(mentor.Relationships) == 0 || len(mentor.StateChanges) == 0 {
		t.Fatalf("mentor evidence = %+v", mentor)
	}
	passerby := indexedSourceCharacter(t, index, "src-passerby")
	if passerby.Named || passerby.CardEligible {
		t.Fatalf("generic passerby treated as named: %+v", passerby)
	}
	for _, id := range []string{"src-hero", "src-villain", "src-friend", "src-mentor"} {
		if entry := indexedSourceCharacter(t, index, id); !entry.CardEligible {
			t.Fatalf("formal character %s was rejected: %+v", id, entry)
		}
	}

	mappings := []CharacterSourceMapping{
		sourceMappingForTest("map-hero", CharacterSourceKeep, []string{"src-hero"}, []string{"target-hero"}),
		sourceMappingForTest("map-villain", CharacterSourceKeep, []string{"src-villain"}, []string{"target-villain"}),
		sourceMappingForTest("map-friend", CharacterSourceKeep, []string{"src-friend"}, []string{"target-friend"}),
		sourceMappingForTest("map-mentor", CharacterSourceKeep, []string{"src-mentor"}, []string{"target-mentor"}),
		sourceMappingForTest("map-passerby", CharacterSourceExclude, []string{"src-passerby"}, nil),
	}
	coverage, err := EvaluateAdaptationCharacterCoverage(index, mappings)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.SourceTotal != 5 || coverage.DecisionRequired != 4 ||
		coverage.Mapped != 5 || coverage.ExplicitlyExcluded != 1 ||
		coverage.Pending != 0 || coverage.BlockingGaps != 0 {
		t.Fatalf("coverage = %+v", coverage)
	}
	for _, decision := range coverage.Decisions {
		if decision.SourceCharacterID == "src-passerby" &&
			(decision.SuggestedTier != CharacterTierDecorative || decision.DecisionRequired) {
			t.Fatalf("passerby coverage = %+v", decision)
		}
	}

	changed := append([]AdaptationSourceReport(nil), reports...)
	changed[1].Summary = "changed report evidence"
	changedIndex, err := BuildAdaptationSourceCharacterIndex(source, changed, dossier, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedIndex.InputSignature == index.InputSignature {
		t.Fatal("source report change did not invalidate index signature")
	}
}

func TestFormalSourceCharacterPolicyKeepsEvidenceButFiltersWalkOnsAndUncertainIdentity(t *testing.T) {
	source := AdaptationSourceFoundation{
		SourceChapterCount: 3,
		Premise:            "A source story.",
		Characters: []Character{
			{ID: "an", Name: "安少廷", Role: "原作男主", Tier: "core", Goal: "查明真相", Motivation: "摆脱失控", Arc: "逐步面对自我"},
			{ID: "yuan", Name: "袁可欣", Aliases: []string{"黄裙女孩", "梦中女孩", "女孩", "奴儿"}, Role: "原作女主", Tier: "core", Goal: "寻求安全", Motivation: "摆脱控制", Arc: "尝试恢复主体性"},
			{ID: "doctor", Name: "王医生", Role: "医生", Tier: "secondary"},
			{ID: "postman", Name: "邮递员", Role: "邮递员", Tier: "secondary"},
			{ID: "master", Name: "真主人", Role: "身份不明的主人称谓", Tier: "important", Goal: "控制袁可欣"},
		},
		Relationships: []CharacterRelationship{
			{ID: "main", SourceCharacterID: "an", TargetCharacterID: "yuan", Type: RelationshipTypeOther, Direction: RelationshipDirectionDirected, Status: RelationshipStatusActive},
			{ID: "walk-on", SourceCharacterID: "doctor", TargetCharacterID: "an", Type: RelationshipTypeProfessional, Direction: RelationshipDirectionDirected, Status: RelationshipStatusResolved},
			{ID: "uncertain", SourceCharacterID: "master", TargetCharacterID: "yuan", Type: RelationshipTypeOther, Direction: RelationshipDirectionDirected, Status: RelationshipStatusActive},
		},
	}
	reports := []AdaptationSourceReport{
		{Chapter: 1, Characters: []string{"安少廷", "黄裙女孩", "王医生"}, KeyEvents: []string{"安少廷寻找黄裙女孩"}},
		{Chapter: 2, Characters: []string{"安少廷", "梦中女孩", "真主人"}, KeyEvents: []string{"安少廷的梦游身份与真主人称谓重叠"}},
		{Chapter: 3, Characters: []string{"安少廷", "袁可欣", "邮递员"}, KeyEvents: []string{"袁可欣作出改变关系的选择"}, StateChanges: []StateChange{{Entity: "袁可欣", Field: "立场", NewValue: "反抗"}}},
	}

	filtered, index, err := ApplyAdaptationSourceCharacterPolicy(source, reports)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Characters) != 5 {
		t.Fatalf("complete evidence index lost entries: %+v", index.Characters)
	}
	if got := characterIDs(filtered.Characters); !reflect.DeepEqual(got, []string{"an", "yuan"}) {
		t.Fatalf("formal source cast = %v, want [an yuan]", got)
	}
	if len(filtered.Relationships) != 1 || filtered.Relationships[0].ID != "main" {
		t.Fatalf("filtered relationships = %+v", filtered.Relationships)
	}
	if indexedSourceCharacter(t, index, "doctor").CardEligible ||
		indexedSourceCharacter(t, index, "postman").CardEligible ||
		indexedSourceCharacter(t, index, "master").CardEligible {
		t.Fatalf("walk-on or uncertain identity entered formal cast: %+v", index.Characters)
	}
	yuan := indexedSourceCharacter(t, index, "yuan")
	if yuan.AppearanceCount != 3 {
		t.Fatalf("cross-chapter aliases were not aggregated: %+v", yuan)
	}
}

func TestAdaptationCharacterEligibilityRejectsWalkOnCardsAndRequiresSubstantiveTargetOriginal(t *testing.T) {
	index := AdaptationSourceCharacterIndex{
		Version:        AdaptationSourceCharacterIndexVersion,
		InputSignature: strings.Repeat("a", 64),
		Characters: []AdaptationSourceCharacterIndexEntry{
			{ID: "hero", CardEligible: true},
			{ID: "hero-alias", CardEligible: false},
			{ID: "doctor", CanonicalName: "王医生", CardEligible: false},
		},
	}
	targets := []Character{
		{ID: "hero-target", Name: "原作主角", Role: "主角", Tier: "core", Goal: "查明真相", Motivation: "责任", Conflict: "证据不足", Arc: "承担代价"},
		{ID: "doctor-target", Name: "王医生", Role: "医生", Tier: "secondary"},
	}
	mappings := []CharacterSourceMapping{
		sourceMappingForTest("hero-map", CharacterSourceKeep, []string{"hero"}, []string{"hero-target"}),
		sourceMappingForTest("doctor-map", CharacterSourceKeep, []string{"doctor"}, []string{"doctor-target"}),
	}
	if err := ValidateAdaptationCharacterCardEligibility(index, mappings, targets); err == nil ||
		!strings.Contains(err.Error(), "evidence-only") {
		t.Fatalf("walk-on keep mapping was accepted: %v", err)
	}

	targets = append(targets, Character{
		ID: "new-lead", Name: "沈砚", Role: "新男主（主视角）", Gender: "male", Tier: "core",
		Goal: "取得证据并救出女主", Motivation: "弥补过去的失职", Conflict: "必须在不惊动原作男主的情况下调查",
		Arc: "从职业旁观转为承担救援后果",
	})
	mappings = []CharacterSourceMapping{
		sourceMappingForTest("hero-map", CharacterSourceKeep, []string{"hero"}, []string{"hero-target"}),
		sourceMappingForTest("hero-alias-map", CharacterSourceMerge, []string{"hero-alias"}, []string{"hero-target"}),
		sourceMappingForTest("doctor-map", CharacterSourceExclude, []string{"doctor"}, nil),
		{
			ID: "new-lead-map", Action: CharacterSourceTargetOriginal, TargetCharacterIDs: []string{"new-lead"},
			Rationale: "承担原作无人能够替代的调查、主视角与最终救援主线",
			Evidence:  []CharacterSourceEvidence{{Kind: CharacterSourceOriginalAddition, Reference: "adaptation.intent"}},
		},
	}
	if err := ValidateAdaptationCharacterCardEligibility(index, mappings, targets); err != nil {
		t.Fatalf("substantive target-original character rejected: %v", err)
	}
}

func characterIDs(characters []Character) []string {
	ids := make([]string, 0, len(characters))
	for _, character := range characters {
		ids = append(ids, character.ID)
	}
	return ids
}

func TestAdaptationSourceCharacterIndexKeepsLegacyEvidenceAsUncertain(t *testing.T) {
	index, err := BuildAdaptationSourceCharacterIndex(&AdaptationSourceFoundation{
		Characters: []Character{{Name: "旧角色", Role: "配角", Traits: []string{}}},
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Characters) != 1 || len(index.Characters[0].Uncertainties) == 0 {
		t.Fatalf("legacy evidence = %+v", index)
	}
}

func TestAdaptationSourceCharacterIndexMergesUniqueReportIdentityButKeepsExplicitHomonymsDistinct(t *testing.T) {
	index, err := BuildAdaptationSourceCharacterIndex(
		&AdaptationSourceFoundation{Characters: []Character{
			{ID: "source-lin", Name: "Lin", Role: "investigator", Traits: []string{"careful"}},
		}},
		[]AdaptationSourceReport{{
			Chapter: 1, SourceSHA256: strings.Repeat("1", 64),
			CharacterProfiles: []Character{{
				ID: "chapter-lin", Name: "Lin", Goal: "Recover the archive.", Traits: []string{"persistent"},
			}},
			Characters: []string{"Lin"},
			KeyEvents:  []string{"Lin recovers the archive."},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Characters) != 1 {
		t.Fatalf("ID-less chapter profile duplicated SourceFoundation identity: %+v", index.Characters)
	}
	lin := indexedSourceCharacter(t, index, "source-lin")
	if lin.Profile.Goal != "Recover the archive." || lin.AppearanceCount != 1 ||
		!reflect.DeepEqual(lin.Profile.Traits, []string{"careful", "persistent"}) {
		t.Fatalf("merged profile = %+v", lin)
	}

	homonyms, err := BuildAdaptationSourceCharacterIndex(
		&AdaptationSourceFoundation{Characters: []Character{
			{ID: "source-alex-one", Name: "Alex", Role: "pilot"},
			{ID: "source-alex-two", Name: "Alex", Role: "doctor"},
		}},
		[]AdaptationSourceReport{{
			Chapter: 2, Characters: []string{"Alex"}, KeyEvents: []string{"Alex arrives."},
			CharacterProfiles: []Character{{Name: "Alex", Goal: "Ambiguous evidence must not invent a third identity."}},
		}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(homonyms.Characters) != 2 {
		t.Fatalf("explicit homonyms collapsed: %+v", homonyms.Characters)
	}
	for _, entry := range homonyms.Characters {
		if len(entry.Conflicts) == 0 {
			t.Fatalf("ambiguous homonym was not surfaced: %+v", entry)
		}
	}
}

func TestValidateCharacterSourceCoverageSupportsMergeSplitExcludeAndTargetOriginal(t *testing.T) {
	mappings := []CharacterSourceMapping{
		sourceMappingForTest("merge", CharacterSourceMerge, []string{"source-a", "source-b"}, []string{"target-ab"}),
		sourceMappingForTest("split", CharacterSourceSplit, []string{"source-c"}, []string{"target-c1", "target-c2"}),
		sourceMappingForTest("exclude", CharacterSourceExclude, []string{"source-d"}, nil),
		{
			ID: "original", Action: CharacterSourceTargetOriginal, TargetCharacterIDs: []string{"target-new"},
			Rationale: "target-only archive function",
			Evidence: []CharacterSourceEvidence{{
				Kind: CharacterSourceOriginalAddition, Reference: "fixture.intent", Summary: "target-only decision",
			}},
		},
	}
	if err := ValidateCharacterSourceCoverage(
		mappings,
		[]string{"source-a", "source-b", "source-c", "source-d"},
		[]string{"target-ab", "target-c1", "target-c2", "target-new"},
	); err != nil {
		t.Fatalf("valid merge/split/exclude/target-original coverage: %v", err)
	}
}

func TestValidateCharacterSourceCoverageRejectsUnknownEndpoints(t *testing.T) {
	mapping := sourceMappingForTest("map", CharacterSourceKeep, []string{"missing-source"}, []string{"target"})
	if err := ValidateCharacterSourceCoverage([]CharacterSourceMapping{mapping}, []string{"source"}, []string{"target"}); err == nil ||
		!strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("unknown source err = %v", err)
	}
	mapping = sourceMappingForTest("map", CharacterSourceKeep, []string{"source"}, []string{"missing-target"})
	if err := ValidateCharacterSourceCoverage([]CharacterSourceMapping{mapping}, []string{"source"}, []string{"target"}); err == nil ||
		!strings.Contains(err.Error(), "unknown target") {
		t.Fatalf("unknown target err = %v", err)
	}
}

func indexedSourceCharacter(
	t *testing.T,
	index AdaptationSourceCharacterIndex,
	id string,
) AdaptationSourceCharacterIndexEntry {
	t.Helper()
	for _, character := range index.Characters {
		if character.ID == id {
			return character
		}
	}
	t.Fatalf("source character %q missing", id)
	return AdaptationSourceCharacterIndexEntry{}
}

func sourceMappingForTest(
	id string,
	action CharacterSourceMappingAction,
	sourceIDs, targetIDs []string,
) CharacterSourceMapping {
	return CharacterSourceMapping{
		ID: id, Action: action, SourceCharacterIDs: sourceIDs, TargetCharacterIDs: targetIDs,
		Rationale: "fixture decision",
		Evidence: []CharacterSourceEvidence{{
			Kind: CharacterSourceAdaptationDecision, Reference: "fixture.intent", Summary: "fixture decision",
		}},
	}
}
