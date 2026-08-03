package domain

import "testing"

func TestCoreCastCompletionRequiresControlledProtagonistAndCompleteCharacter(t *testing.T) {
	contract := completeNormalCoreCast()
	completion := CoreCastCompletion(contract, nil, nil)
	if !completion.Complete {
		t.Fatalf("complete contract blocked: %+v", completion)
	}

	contract.Members[0].Importance = CoreCastImportanceMajorSupport
	contract.Members[0].Character.Motivation = ""
	contract.Members[0].NoCoreRelationships = false
	completion = CoreCastCompletion(contract, nil, nil)
	assertMissingCode(t, completion, "protagonist_required")
	assertMissingCode(t, completion, "motivation_required")
	assertMissingCode(t, completion, "relationship_or_declaration_required")
}

func TestCoreCastCompletionRejectsDanglingAndDuplicateRelationships(t *testing.T) {
	contract := completeNormalCoreCast()
	contract.Members = append(contract.Members, completeCoreCastMember("mara", "Mara", CoreCastImportanceMajorSupport))
	contract.Members[0].NoCoreRelationships = false
	contract.Members[1].NoCoreRelationships = false
	contract.PlannedRelationships = []CharacterRelationship{
		{ID: "one", SourceCharacterID: "lin", TargetCharacterID: "mara", Type: RelationshipTypeAlly},
		{ID: "two", SourceCharacterID: "mara", TargetCharacterID: "lin", Type: RelationshipTypeAlly},
		{ID: "three", SourceCharacterID: "lin", TargetCharacterID: "missing", Type: RelationshipTypeRival},
	}
	completion := CoreCastCompletion(contract, nil, nil)
	assertMissingCode(t, completion, "relationship_duplicate")
	assertMissingCode(t, completion, "relationship_target_missing")
}

func TestAdaptationCoreCastRequiresEverySourceMajorDisposition(t *testing.T) {
	contract := completeAdaptationCoreCast()
	sourceMajor := []SourceMajorCharacter{{ID: "source-lin", Name: "Lin"}, {ID: "source-mara", Name: "Mara"}}
	completion := CoreCastCompletion(contract, sourceMajor, sourceMajor)
	assertMissingCode(t, completion, "source_major_disposition_required")

	contract.SourceDispositions = append(contract.SourceDispositions, SourceCharacterDisposition{
		SourceCharacterID: "source-mara", Action: SourceDispositionExclude,
	})
	completion = CoreCastCompletion(contract, sourceMajor, sourceMajor)
	if !completion.Complete {
		t.Fatalf("valid source dispositions blocked: %+v", completion)
	}

	contract.SourceDispositions[1].TargetCharacterIDs = []string{"target-lin"}
	completion = CoreCastCompletion(contract, sourceMajor, sourceMajor)
	assertMissingCode(t, completion, "exclude_targets_forbidden")
}

func TestResolveSourceMajorCharactersUsesIDNameAndAliasAndBlocksAmbiguity(t *testing.T) {
	source := AdaptationSourceFoundation{Characters: []Character{
		{ID: "source-lin", Name: "Lin", Aliases: []string{"Captain"}},
		{ID: "source-mara", Name: "Mara", Aliases: []string{"Captain"}},
		{Name: "No ID", Aliases: []string{"Nameless"}},
	}}
	dossier := AdaptationCoCreateDossier{Batches: []AdaptationCoCreateDossierBatch{{
		MajorCharacters: []string{"source-lin", "Mara", "Nameless", "Captain", "Unknown"},
	}}}
	resolved, missing := ResolveSourceMajorCharacters(source, dossier)
	if len(resolved) != 3 {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved[2].ID == "" {
		t.Fatal("legacy source character did not receive a read-only deterministic reference")
	}
	result := completionFromMissing(missing)
	assertMissingCode(t, result, "source_major_ambiguous")
	assertMissingCode(t, result, "source_major_unresolved")
}

func TestResolveSourceMajorCharactersUsesReviewedFormalCastForV4(t *testing.T) {
	source := AdaptationSourceFoundation{
		Version: 4,
		Characters: []Character{
			{ID: "source-lead", Name: "Lead"},
			{ID: "source-important", Name: "Important Support"},
		},
	}
	dossier := AdaptationCoCreateDossier{Batches: []AdaptationCoCreateDossierBatch{{
		MajorCharacters: []string{"Lead", "Important Support", "evidence-only label"},
	}}}
	resolved, missing := ResolveSourceMajorCharacters(source, dossier)
	if len(missing) != 0 {
		t.Fatalf("v4 formal cast produced legacy dossier gaps: %+v", missing)
	}
	if len(resolved) != 2 || resolved[0].ID != "source-important" || resolved[1].ID != "source-lead" {
		t.Fatalf("resolved formal cast = %+v", resolved)
	}
}

func TestCoreCastSignatureBindsDraftSourceIntentAndSemanticContent(t *testing.T) {
	base, err := NormalizeCoreCastContract(completeAdaptationCoreCast())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*CoreCastContract)
	}{
		{"draft", func(value *CoreCastContract) { value.DraftRevision++ }},
		{"draft hash", func(value *CoreCastContract) { value.DraftHash = "other" }},
		{"source", func(value *CoreCastContract) { value.SourceSignature = "other" }},
		{"intent", func(value *CoreCastContract) { value.AdaptationIntentHash = "other" }},
		{"arc", func(value *CoreCastContract) { value.Members[0].Character.Arc = "changed" }},
		{"relationships", func(value *CoreCastContract) {
			value.PlannedRelationships = []CharacterRelationship{{SourceCharacterID: "target-lin", TargetCharacterID: "target-mara", Type: RelationshipTypeAlly}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneCoreCastContract(base)
			test.change(&changed)
			signature, err := CoreCastContentSignature(changed)
			if err != nil {
				t.Fatal(err)
			}
			if signature == base.ContentSignature {
				t.Fatalf("%s change did not invalidate signature", test.name)
			}
		})
	}
}

func TestCoreCastCompletionRejectsAmbiguousIdentityRelationshipIDAndDispositionCardinality(t *testing.T) {
	contract := completeNormalCoreCast()
	other := completeCoreCastMember("mara", "Mara", CoreCastImportanceMajorSupport)
	other.Character.Aliases = []string{"Lin"}
	contract.Members = append(contract.Members, other)
	contract.Members[0].NoCoreRelationships = false
	contract.Members[1].NoCoreRelationships = false
	contract.PlannedRelationships = []CharacterRelationship{
		{ID: "same", SourceCharacterID: "lin", TargetCharacterID: "mara", Type: RelationshipTypeAlly},
		{ID: "same", SourceCharacterID: "mara", TargetCharacterID: "lin", Type: RelationshipTypeRival},
	}
	completion := CoreCastCompletion(contract, nil, nil)
	assertMissingCode(t, completion, "member_identity_ambiguous")
	assertMissingCode(t, completion, "relationship_id_duplicate")

	adaptation := completeAdaptationCoreCast()
	adaptation.SourceDispositions[0].TargetCharacterIDs = []string{"target-lin", "second"}
	completion = CoreCastCompletion(adaptation, []SourceMajorCharacter{{ID: "source-lin", Name: "Lin"}}, []SourceMajorCharacter{{ID: "source-lin", Name: "Lin"}})
	assertMissingCode(t, completion, "disposition_target_cardinality")
	adaptation.SourceDispositions = append(adaptation.SourceDispositions, SourceCharacterDisposition{SourceCharacterID: "unknown", Action: SourceDispositionExclude})
	completion = CoreCastCompletion(adaptation, []SourceMajorCharacter{{ID: "source-lin", Name: "Lin"}}, []SourceMajorCharacter{{ID: "source-lin", Name: "Lin"}})
	assertMissingCode(t, completion, "disposition_source_unknown")
}

func TestAdaptationCoreCastValidatesEveryMemberSourceReferenceAgainstAllSourceCharacters(t *testing.T) {
	contract := completeAdaptationCoreCast()
	sourceCharacters := []SourceMajorCharacter{{ID: "source-lin", Name: "Lin"}, {ID: "source-minor", Name: "Minor"}}
	sourceMajor := []SourceMajorCharacter{{ID: "source-lin", Name: "Lin"}}

	contract.Members[0].SourceCharacterIDs = []string{"unknown"}
	completion := CoreCastCompletion(contract, sourceCharacters, sourceMajor)
	assertMissingCode(t, completion, "member_source_character_unknown")

	contract.Members[0].SourceCharacterIDs = []string{"source-lin", "unknown"}
	completion = CoreCastCompletion(contract, sourceCharacters, sourceMajor)
	assertMissingCode(t, completion, "member_source_character_unknown")

	contract.Members[0].SourceCharacterIDs = []string{"source-lin", "source-minor"}
	completion = CoreCastCompletion(contract, sourceCharacters, sourceMajor)
	if !completion.Complete {
		t.Fatalf("valid non-major source reference blocked: %+v", completion)
	}
}

func completeNormalCoreCast() CoreCastContract {
	member := completeCoreCastMember("lin", "Lin", CoreCastImportanceProtagonist)
	member.NoCoreRelationships = true
	return CoreCastContract{Version: CoreCastContractVersion, Mode: CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash", Members: []CoreCastMember{member}}
}

func completeAdaptationCoreCast() CoreCastContract {
	member := completeCoreCastMember("target-lin", "Lin Recast", CoreCastImportanceProtagonist)
	member.Origin = CoreCastOriginSource
	member.SourceCharacterIDs = []string{"source-lin"}
	member.NoCoreRelationships = true
	return CoreCastContract{
		Version: CoreCastContractVersion, Mode: CoreCastModeAdaptation, DraftRevision: 1, DraftHash: "draft-hash",
		SourceSignature: "source-signature", AdaptationIntentHash: "intent-hash", Members: []CoreCastMember{member},
		SourceDispositions: []SourceCharacterDisposition{{SourceCharacterID: "source-lin", Action: SourceDispositionKeep, TargetCharacterIDs: []string{"target-lin"}}},
	}
}

func completeCoreCastMember(id, name string, importance CoreCastImportance) CoreCastMember {
	return CoreCastMember{
		Character:  Character{ID: id, Name: name, Role: "story role", Goal: "goal", Motivation: "motivation", Conflict: "conflict", Arc: "arc", Traits: []string{"trait"}, Constraints: []string{"constraint"}},
		Importance: importance, Origin: CoreCastOriginOriginal, MainlineFunction: "changes the mainline", InclusionRationale: "needed",
	}
}

func assertMissingCode(t *testing.T, result CoreCastCompletionResult, code string) {
	t.Helper()
	for _, item := range result.Missing {
		if item.Code == code {
			return
		}
	}
	t.Fatalf("missing code %q not found in %+v", code, result)
}
