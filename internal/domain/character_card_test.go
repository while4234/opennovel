package domain

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestCompleteCharacterCardRoundTripCloneSignatureAndDiff(t *testing.T) {
	base := completeCharacterCardFoundation()
	normalized, err := NormalizeStoryFoundation(base)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SchemaVersion != StoryFoundationSchemaVersion {
		t.Fatalf("schema = %d", normalized.SchemaVersion)
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var decoded StoryFoundation
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NormalizeStoryFoundation(decoded)
	if err != nil {
		t.Fatal(err)
	}
	firstSignature, _ := FoundationContentSignature(normalized)
	reloadedSignature, _ := FoundationContentSignature(reloaded)
	if firstSignature != reloadedSignature {
		t.Fatalf("round-trip signature changed: %s != %s", firstSignature, reloadedSignature)
	}

	clone := CloneStoryFoundation(normalized)
	clone.Characters[0].ContrastDetails[0].Depth = "changed"
	clone.Characters[0].KeyBackstory[0].Impact = "changed"
	clone.Characters[0].InitialState.Resources[0] = "changed"
	clone.Characters[0].KnowledgeBoundary.Known[0] = "changed"
	if normalized.Characters[0].ContrastDetails[0].Depth == "changed" ||
		normalized.Characters[0].KeyBackstory[0].Impact == "changed" ||
		normalized.Characters[0].InitialState.Resources[0] == "changed" ||
		normalized.Characters[0].KnowledgeBoundary.Known[0] == "changed" {
		t.Fatal("CloneStoryFoundation shallow-copied character card fields")
	}

	candidate := CloneStoryFoundation(normalized)
	candidate.Characters[0].KnowledgeBoundary.Unknown = append(
		candidate.Characters[0].KnowledgeBoundary.Unknown,
		"the hidden heir",
	)
	diff, err := ComputeFoundationDiff(normalized, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Changes) != 1 ||
		!slices.Contains(diff.Changes[0].ChangedFields, "knowledge_boundary") {
		t.Fatalf("knowledge diff = %+v", diff.Changes)
	}
	changedSignature, _ := FoundationAuditSignature(candidate)
	originalSignature, _ := FoundationAuditSignature(normalized)
	if changedSignature == originalSignature {
		t.Fatal("knowledge boundary did not change audit signature")
	}
}

func TestCharacterCardCompletenessByTierAndCoreCastDeclaration(t *testing.T) {
	foundation := completeCharacterCardFoundation()
	foundation.Characters = append(foundation.Characters,
		Character{
			ID: "important", Name: "I", Role: "investigator", Gender: "female", Tier: "important",
			Description: "uncovers the main conspiracy", Goal: "find proof", Motivation: "clear a name",
			Conflict: "the witness lies", Arc: "trusts allies", Voice: "precise",
			InitialState:      &CharacterInitialState{Situation: "isolated"},
			KnowledgeBoundary: &CharacterKnowledgeBoundary{Unknown: []string{"who framed the suspect"}},
		},
		Character{
			ID: "secondary", Name: "S", Role: "courier", Gender: "male", Tier: "secondary",
			Goal: "pay a debt", ContrastDetails: []CharacterContrastDetail{{Surface: "careless", Depth: "observant"}},
			InitialState: &CharacterInitialState{Emotion: "anxious"},
		},
		Character{ID: "decorative", Name: "D", Tier: "decorative"},
	)
	foundation.Relationships = append(foundation.Relationships,
		CharacterRelationship{ID: "rel-important", SourceCharacterID: "core", TargetCharacterID: "important", Type: RelationshipTypeAlly, Direction: RelationshipDirectionDirected, Status: RelationshipStatusPlanned},
		CharacterRelationship{ID: "rel-secondary", SourceCharacterID: "secondary", TargetCharacterID: "core", Type: RelationshipTypeProfessional, Direction: RelationshipDirectionDirected, Status: RelationshipStatusPlanned},
	)
	results, err := EvaluateCharacterCardCompleteness(foundation, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Status != CharacterCardComplete {
			t.Fatalf("%s completeness = %+v", result.CharacterID, result)
		}
	}

	incomplete := CloneStoryFoundation(foundation)
	for i := range incomplete.Characters {
		if incomplete.Characters[i].ID == "secondary" {
			incomplete.Characters[i].Goal = ""
			incomplete.Characters[i].Motivation = ""
		}
	}
	results, err = EvaluateCharacterCardCompleteness(incomplete, nil)
	if err != nil {
		t.Fatal(err)
	}
	var secondary CharacterCardCompletenessResult
	for _, result := range results {
		if result.CharacterID == "secondary" {
			secondary = result
		}
	}
	if secondary.Status != CharacterCardIncomplete ||
		!hasCharacterCardMissingCode(secondary.Missing, "goal_or_motivation_required") {
		t.Fatalf("secondary completeness = %+v", secondary)
	}

	missingGender := CloneStoryFoundation(foundation)
	missingGender.Characters[0].Gender = ""
	results, err = EvaluateCharacterCardCompleteness(missingGender, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != CharacterCardIncomplete ||
		!hasCharacterCardMissingCode(results[0].Missing, "gender_required") {
		t.Fatalf("missing gender was not blocking: %+v", results[0])
	}

	missingKnowledge := CloneStoryFoundation(foundation)
	missingKnowledge.Characters[0].KnowledgeBoundary = nil
	results, err = EvaluateCharacterCardCompleteness(missingKnowledge, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != CharacterCardIncomplete ||
		!hasCharacterCardMissingCode(results[0].Missing, "knowledge_boundary_required") {
		t.Fatalf("missing knowledge boundary was not blocking: %+v", results[0])
	}

	noRelationships := completeCharacterCardFoundation()
	noRelationships.Relationships = nil
	noRelationships.RelationshipsReviewed = false
	contract := completeCharacterCardCoreCast(noRelationships.Characters[0])
	contract.Members[0].NoCoreRelationships = true
	results, err = EvaluateCharacterCardCompleteness(noRelationships, &contract)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != CharacterCardComplete {
		t.Fatalf("core declaration was not reused: %+v", results[0])
	}
	coreCompletion := CoreCastCompletion(contract, nil, nil)
	if !coreCompletion.Complete {
		t.Fatalf("shared core requirements conflict with CoreCast: %+v", coreCompletion)
	}
}

func TestCharacterCardLifecycleStalesOnContentOrInputChange(t *testing.T) {
	foundation := completeCharacterCardFoundation()
	binding, err := CharacterCardBindingFromFoundation(foundation, CharacterCardInputSignatures{
		CreativeBrief: "brief-a",
		CoreCast:      "cast-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := completeCharacterCardLifecycle(binding)
	normalized, err := NormalizeCharacterCardLifecycle(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ReviewStatus != CharacterCardReviewPassed {
		t.Fatalf("review status = %s", normalized.ReviewStatus)
	}

	changed := CloneStoryFoundation(foundation)
	changed.Characters[0].InitialState.Situation = "under arrest"
	changed.Revision++
	changedBinding, err := CharacterCardBindingFromFoundation(changed, binding.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ReconcileCharacterCardLifecycle(normalized, changedBinding)
	if err != nil {
		t.Fatal(err)
	}
	if stale.AnalysisStatus != CharacterCardAnalysisStale ||
		stale.ReviewStatus != CharacterCardReviewStale ||
		stale.ConfirmationStatus != CharacterCardConfirmationStale {
		t.Fatalf("content change did not stale lifecycle: %+v", stale)
	}

	inputBinding, err := CharacterCardBindingFromFoundation(foundation, CharacterCardInputSignatures{
		CreativeBrief: "brief-b",
		CoreCast:      "cast-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	stale, err = ReconcileCharacterCardLifecycle(normalized, inputBinding)
	if err != nil {
		t.Fatal(err)
	}
	if stale.ReviewStatus != CharacterCardReviewStale {
		t.Fatalf("input change review status = %s", stale.ReviewStatus)
	}
}

func TestUnreviewedCharacterCardCandidateCanBecomeStale(t *testing.T) {
	foundation := completeCharacterCardFoundation()
	binding, err := CharacterCardBindingFromFoundation(foundation, CharacterCardInputSignatures{CreativeBrief: "a"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := CharacterCardLifecycle{
		Version: CharacterCardLifecycleVersion, Mode: CharacterCardProjectOriginal,
		Candidate: binding.Candidate, Inputs: binding.Inputs,
		AnalysisStatus: CharacterCardAnalysisCandidateReady,
		ReviewStatus:   CharacterCardReviewNotReviewed, Findings: []CharacterCardReviewFinding{},
		ConfirmationStatus: CharacterCardUnconfirmed,
	}
	changedBinding, err := CharacterCardBindingFromFoundation(foundation, CharacterCardInputSignatures{CreativeBrief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ReconcileCharacterCardLifecycle(lifecycle, changedBinding)
	if err != nil {
		t.Fatal(err)
	}
	if stale.AnalysisStatus != CharacterCardAnalysisStale ||
		stale.ReviewStatus != CharacterCardReviewNotReviewed {
		t.Fatalf("unreviewed stale lifecycle = %+v", stale)
	}
	if _, err := NormalizeCharacterCardLifecycle(stale); err != nil {
		t.Fatalf("stale lifecycle cannot be persisted: %v", err)
	}
}

func TestCharacterSourceMappingsCoverAllActionsAndProjectCoreCast(t *testing.T) {
	binding, err := CharacterCardBindingFromFoundation(completeCharacterCardFoundation(), CharacterCardInputSignatures{})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := completeCharacterCardLifecycle(binding)
	lifecycle.Mode = CharacterCardProjectAdaptation
	lifecycle.SourceMappings = []CharacterSourceMapping{
		sourceMapping("keep", CharacterSourceKeep, []string{"s1"}, []string{"t1"}),
		sourceMapping("rename", CharacterSourceRename, []string{"s2"}, []string{"t2"}),
		sourceMapping("merge", CharacterSourceMerge, []string{"s3", "s4"}, []string{"t3"}),
		sourceMapping("split", CharacterSourceSplit, []string{"s5"}, []string{"t4", "t5"}),
		sourceMapping("exclude", CharacterSourceExclude, []string{"s6"}, nil),
		sourceMapping("original", CharacterSourceTargetOriginal, nil, []string{"t6"}),
	}
	if _, err := NormalizeCharacterCardLifecycle(lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCharacterSourceCoverage(
		lifecycle.SourceMappings,
		[]string{"s1", "s2", "s3", "s4", "s5", "s6"},
		[]string{"t1", "t2", "t3", "t4", "t5", "t6"},
	); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCharacterSourceCoverage(
		lifecycle.SourceMappings,
		[]string{"s1", "s2", "s3", "s4", "s5", "s6", "unmapped"},
		[]string{"t1"},
	); err == nil {
		t.Fatal("missing source mapping was accepted")
	}

	contract := completeCharacterCardCoreCast(completeCharacterCardFoundation().Characters[0])
	contract.Mode = CoreCastModeAdaptation
	contract.SourceSignature = "source"
	contract.AdaptationIntentHash = "intent"
	contract.SourceDispositions = []SourceCharacterDisposition{
		{SourceCharacterID: "s3", Action: SourceDispositionMerge, TargetCharacterIDs: []string{"core"}, Rationale: "combined"},
		{SourceCharacterID: "s4", Action: SourceDispositionMerge, TargetCharacterIDs: []string{"core"}, Rationale: "combined"},
		{SourceCharacterID: "s6", Action: SourceDispositionExclude, Rationale: "not relevant"},
	}
	projected, err := ProjectCoreCastSourceMappings(contract)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 2 {
		t.Fatalf("projected mappings = %+v", projected)
	}
	for _, mapping := range projected {
		if mapping.Action == CharacterSourceMerge && len(mapping.SourceCharacterIDs) != 2 {
			t.Fatalf("merge projection = %+v", mapping)
		}
	}
}

func completeCharacterCardFoundation() StoryFoundation {
	return StoryFoundation{
		SchemaVersion: 2,
		Revision:      7,
		Premise:       "A guarded heir must choose a side.",
		Characters: []Character{{
			ID: "core", Name: "Lin", Role: "protagonist", Gender: "female", Description: "the hidden heir",
			Arc: "accepts responsibility", Traits: []string{"guarded"}, Tier: "core",
			Goal: "protect the city", Motivation: "repay a debt", Conflict: "truth endangers allies",
			Voice: "brief and dry", Constraints: []string{"will not harm children"},
			ContrastDetails: []CharacterContrastDetail{{Surface: "cold", Depth: "quietly generous"}},
			KeyBackstory:    []CharacterBackstory{{Event: "failed rescue", Impact: "avoids promises"}},
			InitialState: &CharacterInitialState{
				Identity: "courier", Situation: "in hiding", Emotion: "wary",
				Resources: []string{"coded ledger"}, Relationships: "trusts only mentor",
			},
			KnowledgeBoundary: &CharacterKnowledgeBoundary{
				Known: []string{"the gate code"}, Unknown: []string{"their ancestry"},
				Misconceptions: []string{"mentor betrayed them"}, Forbidden: []string{"villain identity before chapter 8"},
			},
		}},
		RelationshipsReviewed: true,
		WorldRules:            []WorldRule{{ID: "rule", Category: "society", Rule: "Oaths bind houses.", Boundary: "No retroactive oath.", Strength: WorldRuleStrengthHard}},
	}
}

func completeCharacterCardCoreCast(character Character) CoreCastContract {
	return CoreCastContract{
		Version: CoreCastContractVersion, Mode: CoreCastModeNormal,
		DraftRevision: 1, DraftHash: "draft",
		Members: []CoreCastMember{{
			Character: character, Importance: CoreCastImportanceProtagonist,
			Origin: CoreCastOriginOriginal, MainlineFunction: "drives the succession conflict",
			NoCoreRelationships: true,
		}},
	}
}

func completeCharacterCardLifecycle(binding CharacterCardBinding) CharacterCardLifecycle {
	return CharacterCardLifecycle{
		Version:   CharacterCardLifecycleVersion,
		Mode:      CharacterCardProjectOriginal,
		Candidate: binding.Candidate,
		Inputs:    binding.Inputs, InputDigest: binding.InputDigest,
		AnalysisStatus:      CharacterCardAnalysisCandidateReady,
		ReviewStatus:        CharacterCardReviewPassed,
		ReviewedCandidate:   binding.Candidate,
		ReviewedInputDigest: binding.InputDigest,
		ReviewSummary:       "ready",
		Findings:            []CharacterCardReviewFinding{},
		ConfirmationStatus:  CharacterCardConfirmed,
		RunID:               "run-1",
		IdempotencyKey:      "key-1",
	}
}

func TestProjectCharacterCandidateCoreCastReportsConfirmedConflict(t *testing.T) {
	foundation := completeCharacterCardFoundation()
	projected, findings, err := ProjectCharacterCandidateCoreCast(foundation, nil)
	if err != nil || len(findings) != 0 {
		t.Fatalf("initial projection = %+v, findings=%+v, err=%v", projected, findings, err)
	}
	projected.ConfirmedSignature = projected.ContentSignature
	foundation.Characters[0].Goal = "abandon the city"
	_, findings, err = ProjectCharacterCandidateCoreCast(foundation, &projected)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 || !findings[0].Blocking ||
		findings[0].IssueType != "confirmed_core_cast_conflict" {
		t.Fatalf("confirmed conflict findings = %+v", findings)
	}
}

func TestProjectCharacterCandidateCoreCastMarksExplicitNewMaleLeadAsProtagonist(t *testing.T) {
	foundation := completeCharacterCardFoundation()
	lead := CloneCharacter(foundation.Characters[0])
	lead.ID = "new-male-lead"
	lead.Name = "Gu Heng"
	lead.Role = "新男主（主视角）"
	foundation.Characters = []Character{lead}

	projected, findings, err := ProjectCharacterCandidateCoreCast(foundation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v", findings)
	}
	if got := projected.Members[0].Importance; got != CoreCastImportanceProtagonist {
		t.Fatalf("new male lead importance = %q", got)
	}
}

func TestApplyCharacterSourceMappingsToCoreCastUsesOnlyFormalSourceIDs(t *testing.T) {
	contract := completeCharacterCardCoreCast(completeCharacterCardFoundation().Characters[0])
	contract.Mode = CoreCastModeAdaptation
	contract.SourceSignature = "source"
	contract.AdaptationIntentHash = "intent"
	mappings := []CharacterSourceMapping{
		{
			ID: "formal", Action: CharacterSourceKeep,
			SourceCharacterIDs: []string{"source-hero"}, TargetCharacterIDs: []string{"core"},
			Rationale: "preserve the reviewed source lead",
			Evidence: []CharacterSourceEvidence{{
				Kind: CharacterSourceAdaptationDecision, Reference: "reviewed source index",
			}},
		},
		{
			ID: "evidence", Action: CharacterSourceExclude,
			SourceCharacterIDs: []string{"evidence-only"}, Rationale: "not a formal card",
			Evidence: []CharacterSourceEvidence{{
				Kind: CharacterSourceAdaptationDecision, Reference: "reviewed source index",
			}},
		},
	}
	projected, err := ApplyCharacterSourceMappingsToCoreCast(contract, mappings, []string{"source-hero"})
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.SourceDispositions) != 1 ||
		projected.SourceDispositions[0].SourceCharacterID != "source-hero" {
		t.Fatalf("source dispositions = %+v", projected.SourceDispositions)
	}
	if projected.Members[0].Origin != CoreCastOriginSource ||
		len(projected.Members[0].SourceCharacterIDs) != 1 ||
		projected.Members[0].SourceCharacterIDs[0] != "source-hero" {
		t.Fatalf("source member projection = %+v", projected.Members[0])
	}
}

func sourceMapping(
	id string,
	action CharacterSourceMappingAction,
	sourceIDs, targetIDs []string,
) CharacterSourceMapping {
	evidenceKind := CharacterSourceAdaptationDecision
	if action == CharacterSourceTargetOriginal {
		evidenceKind = CharacterSourceOriginalAddition
	}
	return CharacterSourceMapping{
		ID: id, Action: action, SourceCharacterIDs: sourceIDs, TargetCharacterIDs: targetIDs,
		Rationale: "explicit adaptation decision",
		Evidence: []CharacterSourceEvidence{{
			Kind: evidenceKind, Reference: "report:characters", Summary: "reviewed",
		}},
	}
}

func hasCharacterCardMissingCode(items []CharacterCardMissingItem, code string) bool {
	for _, item := range items {
		if item.Code == code {
			return true
		}
	}
	return false
}
