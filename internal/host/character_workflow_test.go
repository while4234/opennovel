package host

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	adaptpkg "github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestAdaptationCharacterWorkflowPublishesCompleteCastAndTargetFoundation(t *testing.T) {
	st, sourceBefore := adaptationCharacterWorkflowStore(t)
	registry := tools.NewCharacterRunRegistry()
	analyzeBinding := adaptationCharacterContextBinding(t, st, registry, "adaptation-analyze", tools.CharacterRunAnalyze)

	hero := completeHostCharacter()
	hero.ID = "target-hero"
	friend := completeHostCharacter()
	friend.ID = "target-friend"
	friend.Name = "Zhou Ning"
	friend.Role = "recurring friend"
	friend.Tier = string(domain.CharacterTierSecondary)
	friend.Goal = "Recover the missing evidence."
	friend.Motivation = "Protect the protagonist."
	friend.Conflict = ""
	mentor := completeHostCharacter()
	mentor.ID = "target-mentor"
	mentor.Name = "Mentor Shen"
	mentor.Role = "mentor"
	mentor.Tier = string(domain.CharacterTierSecondary)
	mentor.Goal = "Correct an old failure."
	mentor.Motivation = "Accept responsibility."
	mentor.Conflict = ""
	original := completeHostCharacter()
	original.ID = "target-original"
	original.Name = "Archivist Qiao"
	original.Role = "target-original archive custodian"
	original.Tier = string(domain.CharacterTierImportant)
	original.Goal = "Keep the archive auditable."
	original.Motivation = "Prevent another cover-up."
	original.Conflict = "Publishing the evidence endangers the witnesses the archivist must protect."

	relationships := []domain.CharacterRelationship{
		{
			ID: "rel-friend", SourceCharacterID: hero.ID, TargetCharacterID: friend.ID,
			Type: domain.RelationshipTypeAlly, Direction: domain.RelationshipDirectionBidirectional,
			Status: domain.RelationshipStatusPlanned, Description: "They recover evidence together.",
		},
		{
			ID: "rel-mentor", SourceCharacterID: mentor.ID, TargetCharacterID: hero.ID,
			Type: domain.RelationshipTypeMentor, Direction: domain.RelationshipDirectionDirected,
			Status: domain.RelationshipStatusPlanned, Description: "The mentor transfers responsibility without leaking future knowledge.",
		},
		{
			ID: "rel-archivist", SourceCharacterID: original.ID, TargetCharacterID: friend.ID,
			Type: domain.RelationshipTypeProfessional, Direction: domain.RelationshipDirectionDirected,
			Status: domain.RelationshipStatusPlanned, Description: "The archivist verifies the friend's evidence chain.",
		},
	}
	mappings := []domain.CharacterSourceMapping{
		adaptationMapping("map-hero", domain.CharacterSourceKeep, []string{"src-hero"}, []string{hero.ID}, domain.CharacterSourceAdaptationDecision),
		adaptationMapping("map-friend", domain.CharacterSourceKeep, []string{"src-friend"}, []string{friend.ID}, domain.CharacterSourceAdaptationDecision),
		adaptationMapping("map-mentor", domain.CharacterSourceKeep, []string{"src-mentor"}, []string{mentor.ID}, domain.CharacterSourceAdaptationDecision),
		adaptationMapping("map-passerby", domain.CharacterSourceExclude, []string{"src-passerby"}, nil, domain.CharacterSourceAdaptationDecision),
		adaptationMapping("map-original", domain.CharacterSourceTargetOriginal, nil, []string{original.ID}, domain.CharacterSourceOriginalAddition),
	}
	analyzeRequest := map[string]any{
		"run_id": "adaptation-analyze", "mode": "analyze", "idempotency_key": "adaptation-analyze-key",
		"base_revision":        analyzeBinding.Candidate.FoundationRevision,
		"base_audit_signature": analyzeBinding.Candidate.FoundationAuditSignature,
		"candidate_digest":     analyzeBinding.Candidate.CharacterContentDigest,
		"input_digest":         analyzeBinding.InputDigest,
		"analysis_summary":     "Complete source-backed cast with explicit decorative exclusion and one target-original function.",
		"characters":           []domain.Character{hero, friend, mentor, original},
		"relationships":        relationships, "relationships_reviewed": true, "source_mappings": mappings,
	}
	if _, err := tools.NewSaveCharacterCandidateTool(st, registry).Execute(
		context.Background(), mustCharacterJSON(t, analyzeRequest),
	); err != nil {
		t.Fatal(err)
	}

	// A new process can recover the staged candidate and continue with a
	// distinct independent review run.
	st = storepkg.NewStore(st.Dir())
	registry = tools.NewCharacterRunRegistry()
	reviewBinding := adaptationCharacterContextBinding(t, st, registry, "adaptation-review", tools.CharacterRunReview)
	reviewRequest := map[string]any{
		"run_id": "adaptation-review", "mode": "review", "idempotency_key": "adaptation-review-key",
		"base_revision":        reviewBinding.Candidate.FoundationRevision,
		"base_audit_signature": reviewBinding.Candidate.FoundationAuditSignature,
		"candidate_digest":     reviewBinding.Candidate.CharacterContentDigest,
		"input_digest":         reviewBinding.InputDigest,
		"verdict":              "pass", "summary": "Independent source fidelity, coverage, completeness, knowledge, and relationship review passed.",
		"findings": []domain.CharacterCardReviewFinding{},
	}
	if _, err := tools.NewSaveCharacterReviewTool(st, registry).Execute(
		context.Background(), mustCharacterJSON(t, reviewRequest),
	); err != nil {
		t.Fatal(err)
	}
	candidate, lifecycle, binding, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.Coverage == nil || lifecycle.Coverage.BlockingGaps != 0 ||
		lifecycle.Coverage.ExplicitlyExcluded != 1 {
		t.Fatalf("coverage = %+v", lifecycle.Coverage)
	}
	if lifecycle.ReviewStatus != domain.CharacterCardReviewPassed {
		t.Fatalf("review status=%s findings=%+v completeness=%+v", lifecycle.ReviewStatus, lifecycle.Findings, lifecycle.Completeness)
	}
	confirmRequest := CharacterConfirmationRequest{
		ExpectedCandidateRevision: candidate.Revision,
		CandidateDigest:           binding.Candidate.CharacterContentDigest,
		IdempotencyKey:            "adaptation-confirm-key",
	}
	firstConfirmation, err := ConfirmOriginalCharacterCandidate(st, confirmRequest)
	if err != nil {
		t.Fatal(err)
	}
	retryConfirmation, err := ConfirmOriginalCharacterCandidate(st, confirmRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !retryConfirmation.Idempotent ||
		retryConfirmation.FoundationRevision != firstConfirmation.FoundationRevision ||
		retryConfirmation.CandidateRevision != firstConfirmation.CandidateRevision {
		t.Fatalf("adaptation confirmation retry=%+v first=%+v", retryConfirmation, firstConfirmation)
	}

	workflow, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageTargetFoundationGenerating, -1)
	if err != nil {
		t.Fatal(err)
	}
	targetReview, err := adaptpkg.GenerateTargetFoundation(context.Background(), adaptpkg.Deps{Store: st}, adaptpkg.TargetFoundationOptions{
		Brief: "Preserve the complete reviewed cast.", ExpectedWorkflowRevision: workflow.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(target.Characters) != 4 || !foundationHasCharacter(target, "target-friend") ||
		!foundationHasCharacter(target, "target-mentor") || !foundationHasCharacter(target, "target-original") {
		t.Fatalf("target cast = %+v", target.Characters)
	}
	if foundationHasCharacter(target, "src-passerby") {
		t.Fatalf("excluded passerby entered target cast: %+v", target.Characters)
	}
	if !target.RelationshipsReviewed || len(target.Relationships) != len(relationships) {
		t.Fatalf("reviewed relationships were not preserved: %+v", target.Relationships)
	}
	reboundCandidate, reboundLifecycle, reboundBinding, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		t.Fatal(err)
	}
	if reboundCandidate.Foundation.Revision != targetReview.FoundationRevision ||
		reboundLifecycle.ConfirmationStatus != domain.CharacterCardConfirmed ||
		reboundLifecycle.Candidate != reboundBinding.Candidate ||
		reboundLifecycle.ReviewedCandidate != reboundBinding.Candidate {
		t.Fatalf("target Foundation did not rebind confirmed Character state: candidate=%+v lifecycle=%+v binding=%+v", reboundCandidate, reboundLifecycle, reboundBinding)
	}
	sourceAfter, err := st.Adaptation.LoadSourceFoundation()
	if err != nil || !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatalf("SourceFoundation mutated: before=%+v after=%+v err=%v", sourceBefore, sourceAfter, err)
	}
}

func TestConfirmOriginalCharacterCandidatePublishesReviewedCandidateAndIsIdempotent(t *testing.T) {
	st, candidate, binding := stagedOriginalCharacterWorkflow(t)
	if !characterConfirmationPending(st) {
		t.Fatal("reviewed unconfirmed candidate did not open the user-confirmation boundary")
	}
	request := CharacterConfirmationRequest{
		ExpectedCandidateRevision: candidate.Revision,
		CandidateDigest:           binding.Candidate.CharacterContentDigest,
		IdempotencyKey:            "confirm-character-1",
	}
	first, err := ConfirmOriginalCharacterCandidate(st, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Idempotent || first.FoundationRevision <= binding.Candidate.FoundationRevision {
		t.Fatalf("first confirmation = %+v", first)
	}
	if characterConfirmationPending(st) {
		t.Fatal("confirmed candidate left the user-confirmation boundary open")
	}
	published, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(published.Characters) != 1 || published.Characters[0].ID != "char-investigator" ||
		!published.RelationshipsReviewed {
		t.Fatalf("published Foundation = %+v", published)
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(review.FoundationSections, "characters") ||
		!slices.Contains(review.FoundationSections, "planned_relationships") {
		t.Fatalf("Foundation sections = %+v", review.FoundationSections)
	}
	if err := RequireCoreCastGate(st, domain.CoreCastModeNormal, false); err != nil {
		t.Fatalf("confirmed Character candidate did not publish the normal CoreCast gate: %v", err)
	}

	restarted := storepkg.NewStore(st.Dir())
	retry, err := ConfirmOriginalCharacterCandidate(restarted, request)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Idempotent || retry.FoundationRevision != first.FoundationRevision ||
		retry.CandidateRevision != first.CandidateRevision {
		t.Fatalf("retry = %+v, first = %+v", retry, first)
	}
}

func TestOriginalCandidateUsesConfirmedBriefWhenLegacyPremiseIsEmpty(t *testing.T) {
	candidate := domain.StoryFoundation{}
	got := originalCandidateWithConfirmedBrief(candidate, "  已确认的中文共创前提  ")
	if got.Premise != "已确认的中文共创前提" {
		t.Fatalf("premise = %q", got.Premise)
	}

	candidate.Premise = "候选中已有前提"
	got = originalCandidateWithConfirmedBrief(candidate, "不应覆盖")
	if got.Premise != candidate.Premise {
		t.Fatalf("existing premise was overwritten: %q", got.Premise)
	}
}

func TestConfirmOriginalCharacterCandidateRejectsStaleFoundation(t *testing.T) {
	st, candidate, binding := stagedOriginalCharacterWorkflow(t)
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationWorldRules(&storepkg.FoundationGenerationFence{
		Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision,
	}, []domain.WorldRule{{
		ID:       "rule-concurrent",
		Category: "evidence",
		Rule:     "A concurrent author revision wins.",
		Strength: domain.WorldRuleStrengthHard,
	}}); err != nil {
		t.Fatal(err)
	}
	_, err = ConfirmOriginalCharacterCandidate(st, CharacterConfirmationRequest{
		ExpectedCandidateRevision: candidate.Revision,
		CandidateDigest:           binding.Candidate.CharacterContentDigest,
		IdempotencyKey:            "confirm-stale",
	})
	if err == nil {
		t.Fatal("expected stale Foundation confirmation failure")
	}
}

func TestEditOriginalCharacterCandidateInvalidatesReview(t *testing.T) {
	st, candidate, _ := stagedOriginalCharacterWorkflow(t)
	editedCharacters := append([]domain.Character(nil), candidate.Foundation.Characters...)
	editedCharacters[0].Goal = "Expose the conspiracy and rescue the missing witness."
	saved, lifecycle, err := EditOriginalCharacterCandidate(st, CharacterCandidateEditRequest{
		ExpectedCandidateRevision: candidate.Revision,
		Characters:                editedCharacters,
		Relationships:             candidate.Foundation.Relationships,
		RelationshipsReviewed:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision <= candidate.Revision ||
		lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
		lifecycle.ReviewStatus != domain.CharacterCardReviewStale ||
		lifecycle.ConfirmationStatus != domain.CharacterCardUnconfirmed {
		t.Fatalf("edited workflow candidate=%+v lifecycle=%+v", saved, lifecycle)
	}
	editedBinding, err := domain.CharacterCardBindingFromFoundation(saved.Foundation, lifecycle.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmOriginalCharacterCandidate(st, CharacterConfirmationRequest{
		ExpectedCandidateRevision: saved.Revision,
		CandidateDigest:           editedBinding.Candidate.CharacterContentDigest,
		IdempotencyKey:            "confirm-with-stale-review",
	}); err == nil {
		t.Fatal("edited candidate confirmation accepted without a fresh review")
	}
}

func TestEditOriginalCharacterCandidateKeepsUnreviewedLifecycleValid(t *testing.T) {
	st, candidate, binding := stagedOriginalCharacterWorkflow(t)
	_, lifecycle, _, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		t.Fatal(err)
	}
	unreviewed := *lifecycle
	unreviewed.ReviewStatus = domain.CharacterCardReviewNotReviewed
	unreviewed.ReviewedCandidate = domain.CharacterCardCandidateReference{}
	unreviewed.ReviewedInputDigest = ""
	unreviewed.ReviewSummary = ""
	if _, err := st.CharacterCards.SaveCAS(unreviewed, lifecycle.Revision, binding); err != nil {
		t.Fatal(err)
	}

	editedCharacters := append([]domain.Character(nil), candidate.Foundation.Characters...)
	editedCharacters[0].Goal = "Expose the conspiracy without rewriting unrelated character fields."
	saved, editedLifecycle, err := EditOriginalCharacterCandidate(st, CharacterCandidateEditRequest{
		ExpectedCandidateRevision: candidate.Revision,
		Characters:                editedCharacters,
		Relationships:             candidate.Foundation.Relationships,
		RelationshipsReviewed:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision <= candidate.Revision ||
		editedLifecycle.ReviewStatus != domain.CharacterCardReviewNotReviewed ||
		editedLifecycle.ReviewedCandidate.CharacterContentDigest != "" ||
		editedLifecycle.ReviewedInputDigest != "" {
		t.Fatalf("edited unreviewed workflow candidate=%+v lifecycle=%+v", saved, editedLifecycle)
	}
}

func stagedOriginalCharacterWorkflow(
	t *testing.T,
) (*storepkg.Store, domain.CharacterCardCandidate, domain.CharacterCardBinding) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	base, err := st.Foundation.SaveRevisionCAS(domain.StoryFoundation{
		SchemaVersion: domain.StoryFoundationSchemaVersion,
		Premise:       "An investigator must expose a conspiracy without losing her family.",
		WorldRules: []domain.WorldRule{{
			ID: "rule-evidence", Category: "mystery", Rule: "Every accusation requires two independent clues.",
			Strength: domain.WorldRuleStrengthHard,
		}},
		Characters:    []domain.Character{},
		Relationships: []domain.CharacterRelationship{},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginOriginalCharacterReview(&domain.PlanningReview{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "reviewed-original-brief",
	}); err != nil {
		t.Fatal(err)
	}
	_, canonicalBinding, inputs, _, err := tools.CurrentCharacterCanonicalBinding(st)
	if err != nil {
		t.Fatal(err)
	}
	candidateFoundation := domain.CloneStoryFoundation(base)
	candidateFoundation.Characters = []domain.Character{completeHostCharacter()}
	candidateFoundation.Relationships = []domain.CharacterRelationship{}
	candidateFoundation.RelationshipsReviewed = true
	projected, findings, err := domain.ProjectCharacterCandidateCoreCast(candidateFoundation, nil)
	if err != nil || len(findings) != 0 {
		t.Fatalf("project CoreCast findings=%+v err=%v", findings, err)
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(candidateFoundation, &projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range completeness {
		if item.Status != domain.CharacterCardComplete {
			t.Fatalf("candidate completeness = %+v", completeness)
		}
	}
	candidateBinding, err := domain.CharacterCardBindingFromFoundation(candidateFoundation, inputs)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := st.CharacterCards.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:       domain.CharacterCardCandidateVersion,
		Base:          canonicalBinding,
		Foundation:    candidateFoundation,
		ProjectedCast: projected,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CharacterCards.SaveCAS(domain.CharacterCardLifecycle{
		Version:             domain.CharacterCardLifecycleVersion,
		Mode:                domain.CharacterCardProjectOriginal,
		Candidate:           candidateBinding.Candidate,
		Inputs:              candidateBinding.Inputs,
		InputDigest:         candidateBinding.InputDigest,
		AnalysisSummary:     "deterministic candidate",
		Completeness:        completeness,
		AnalysisStatus:      domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:        domain.CharacterCardReviewPassed,
		ReviewedCandidate:   candidateBinding.Candidate,
		ReviewedInputDigest: candidateBinding.InputDigest,
		ReviewSummary:       "independent review passed",
		Findings:            []domain.CharacterCardReviewFinding{},
		ConfirmationStatus:  domain.CharacterCardUnconfirmed,
		SourceMappings:      []domain.CharacterSourceMapping{},
	}, 0, candidateBinding)
	if err != nil {
		t.Fatal(err)
	}
	return st, candidate, candidateBinding
}

func completeHostCharacter() domain.Character {
	return domain.Character{
		ID:          "char-investigator",
		Gender:      "female",
		Name:        "Lin Che",
		Aliases:     []string{},
		Role:        "protagonist investigator",
		Description: "A disciplined investigator torn between public truth and family safety.",
		Arc:         "She moves from controlling information to accepting the cost of public truth.",
		Traits:      []string{"disciplined", "observant"},
		Tier:        string(domain.CharacterTierCore),
		Goal:        "Expose the conspiracy.",
		Motivation:  "Protect her sibling from the same institutional harm.",
		Conflict:    "Publishing the truth may destroy her family.",
		Voice:       "Short evidence-first sentences.",
		Constraints: []string{"Never accuses without corroboration."},
		ContrastDetails: []domain.CharacterContrastDetail{{
			Surface: "calm", Depth: "loses judgment when her sibling is threatened",
		}},
		KeyBackstory: []domain.CharacterBackstory{{
			Event: "She once misidentified a witness.", Impact: "She now cross-checks every clue.",
		}},
		InitialState: &domain.CharacterInitialState{
			Identity: "investigator", Situation: "receives a new lead in a cold case", Emotion: "guarded",
			Resources: []string{"sealed archive"}, Relationships: "estranged from her sibling",
		},
		KnowledgeBoundary: &domain.CharacterKnowledgeBoundary{
			Known: []string{"official case record"}, Unknown: []string{"conspiracy leader"},
			Misconceptions: []string{"her father left the city"}, Forbidden: []string{"leader identity"},
		},
	}
}

func adaptationCharacterWorkflowStore(
	t *testing.T,
) (*storepkg.Store, *domain.AdaptationSourceFoundation) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	sourceOne, err := st.Adaptation.SaveSourceChapter(1, "Opening", "bounded source fixture one")
	if err != nil {
		t.Fatal(err)
	}
	sourceTwo, err := st.Adaptation.SaveSourceChapter(2, "Turn", "bounded source fixture two")
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.AdaptationSourceManifest{
		SourcePath: "fixture.txt", ChapterCount: 2,
		Chapters: []domain.AdaptationSource{sourceOne, sourceTwo},
	}
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	sourceSignature := storepkg.AdaptationSourceSignature(manifest)
	sourceFoundation := domain.AdaptationSourceFoundation{
		Version: 1, SourceSignature: sourceSignature, Premise: "# Source\n\nA bounded source fixture.",
		Characters: []domain.Character{
			{ID: "src-hero", Name: "Lin Che", Role: "source protagonist", Traits: []string{"disciplined"}},
			{ID: "src-friend", Name: "Zhou Ning", Role: "recurring friend", Goal: "recover evidence", Traits: []string{"loyal"}},
			{ID: "src-mentor", Name: "Mentor Shen", Role: "mentor", Motivation: "correct an old failure", Traits: []string{"careful"}},
			{ID: "src-passerby", Name: "路人", Role: "路人", Traits: []string{}},
		},
		WorldRules: []domain.WorldRule{{
			Category: "society", Rule: "Evidence must remain auditable.", Boundary: "No unsupported source claims.",
		}},
	}
	if err := st.Adaptation.SaveSourceFoundation(sourceFoundation); err != nil {
		t.Fatal(err)
	}
	reports := []domain.AdaptationSourceReport{
		{
			Chapter: 1, Title: sourceOne.Title, SourceSHA256: sourceOne.SHA256,
			Summary: "Lin Che begins the investigation.", Characters: []string{"Lin Che", "Zhou Ning", "路人"},
			CharacterFacts: []string{"Zhou Ning independently protects the evidence."},
			KeyEvents:      []string{"Lin Che and Zhou Ning preserve the first record."},
			Relationships: []domain.RelationshipEntry{{
				CharacterA: "Lin Che", CharacterB: "Zhou Ning", Relation: "recurring allies",
			}},
		},
		{
			Chapter: 2, Title: sourceTwo.Title, SourceSHA256: sourceTwo.SHA256,
			Summary: "Mentor Shen accepts responsibility.", Characters: []string{"Lin Che", "Zhou Ning", "Mentor Shen"},
			CharacterFacts: []string{"Mentor Shen acts to correct an old failure."},
			KeyEvents:      []string{"Mentor Shen transfers the archive to Lin Che."},
			StateChanges: []domain.StateChange{{
				Entity: "Mentor Shen", Field: "stance", OldValue: "silent", NewValue: "helps Lin Che", Reason: "accepts responsibility",
			}},
		},
	}
	for _, report := range reports {
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveCoCreateDossier(domain.AdaptationCoCreateDossier{
		Version: 1, SourceSignature: sourceSignature,
		Batches: []domain.AdaptationCoCreateDossierBatch{{
			MajorCharacters: []string{"Lin Che"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	intentHash := strings.Repeat("b", 64)
	if err := st.Adaptation.SaveCoCreateIntent(domain.AdaptationCoCreateIntent{
		Version: 1, RawRequest: "Preserve the cast evidence.", IntentHash: intentHash,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode: domain.CoreCastModeAdaptation, DraftRevision: 1, DraftHash: "fixture-draft",
		SourceSignature: sourceSignature, AdaptationIntentHash: intentHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.Adaptation.LoadSourceFoundation()
	if err != nil {
		t.Fatal(err)
	}
	return st, before
}

func adaptationCharacterContextBinding(
	t *testing.T,
	st *storepkg.Store,
	registry *tools.CharacterRunRegistry,
	runID string,
	mode tools.CharacterRunMode,
) domain.CharacterCardBinding {
	t.Helper()
	if _, err := tools.NewCharacterContextTool(st, registry).Execute(
		context.Background(),
		mustCharacterJSON(t, map[string]any{"run_id": runID, "mode": mode}),
	); err != nil {
		t.Fatal(err)
	}
	if mode == tools.CharacterRunAnalyze {
		_, binding, _, _, err := tools.CurrentCharacterCanonicalBinding(st)
		if err != nil {
			t.Fatal(err)
		}
		return binding
	}
	_, _, binding, err := tools.CurrentCharacterWorkflow(st)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func adaptationMapping(
	id string,
	action domain.CharacterSourceMappingAction,
	sourceIDs, targetIDs []string,
	kind domain.CharacterSourceEvidenceKind,
) domain.CharacterSourceMapping {
	return domain.CharacterSourceMapping{
		ID: id, Action: action, SourceCharacterIDs: sourceIDs, TargetCharacterIDs: targetIDs,
		Rationale: "fixture adaptation decision",
		Evidence: []domain.CharacterSourceEvidence{{
			Kind: kind, Reference: "fixture.character-index", Summary: "bounded fixture evidence",
		}},
	}
}

func mustCharacterJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func foundationHasCharacter(foundation domain.StoryFoundation, id string) bool {
	for _, character := range foundation.Characters {
		if character.ID == id {
			return true
		}
	}
	return false
}
