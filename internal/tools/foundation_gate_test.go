package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestSaveFoundationFormalOutlineRequiresCurrentFoundationConfirmation(t *testing.T) {
	st := confirmedCoreCastToolStore(t)
	review := &domain.PlanningReview{Brief: "Lin must save home", StartPrompt: "start"}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatal(err)
	}
	formal, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	fence := &store.FoundationGenerationFence{Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision}
	if _, err = st.SaveFoundationPremise(fence, "Lin must save home without breaking the no-resurrection rule."); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveFoundationCharacters(fence, formal.Characters); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveFoundationRelationships(fence, formal.Relationships); err != nil {
		t.Fatal(err)
	}
	review, err = st.SaveFoundationWorldRules(fence, []domain.WorldRule{{ID: "rule-1", Rule: "No resurrection", Strength: domain.WorldRuleStrengthHard}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"type": "outline", "content": []domain.OutlineEntry{{Chapter: 1, Title: "Opening", CoreEvent: "Lin chooses duty", Hook: "The city closes", Scenes: []string{"Lin receives the warning"}}}})
	if _, err := NewSaveFoundationTool(st).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "foundation confirmation gate") {
		t.Fatalf("pending foundation allowed outline: %v", err)
	}
	approved, err := st.ConfirmFoundation(review.FoundationRevision, review.FoundationAuditSignature)
	if err != nil {
		t.Fatal(err)
	}
	approved.Kind = domain.PlanningReviewKindBlueprint
	approved.Status = domain.PlanningReviewStatusCollecting
	if err := st.RunMeta.SetPlanningReview(approved); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSaveFoundationTool(st).Execute(context.Background(), args); err != nil {
		t.Fatalf("confirmed foundation blocked outline: %v", err)
	}
}

func TestArchitectCannotWriteFoundationBeforeCharacterConfirmation(t *testing.T) {
	st := confirmedCoreCastToolStore(t)
	if _, err := st.BeginFoundationReview(&domain.PlanningReview{Brief: "complete creative brief"}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"type":                     "premise",
		"content":                  "# Premise\nLocked until character confirmation.",
		"foundation_generation":    1,
		"foundation_base_revision": 1,
	})
	if _, err := NewArchitectSaveFoundationTool(st).Execute(context.Background(), args); err == nil ||
		!strings.Contains(err.Error(), "must wait for the Character Agent") {
		t.Fatalf("Architect wrote before Character confirmation: %v", err)
	}
}

func TestSaveFoundationStaleFenceReturnsAuthoritativeRetryContract(t *testing.T) {
	st := confirmedCoreCastToolStore(t)
	review := &domain.PlanningReview{Brief: "complete creative brief", StartPrompt: "start"}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"type":                     "premise",
		"content":                  "# Canonical premise\nA complete story foundation.",
		"foundation_generation":    review.FoundationGeneration,
		"foundation_base_revision": review.FoundationBaseRevision + 1,
	})
	_, err := NewSaveFoundationTool(st).Execute(context.Background(), args)
	if err == nil {
		t.Fatal("stale Foundation fence was accepted")
	}
	for _, want := range []string{
		"authoritative retry contract",
		"type=premise",
		"foundation_generation=1",
		"foundation_base_revision=1",
		"do not switch section types",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestArchitectContextUsesCanonicalFoundationAndSeparatesRuntimeRelationships(t *testing.T) {
	st := confirmedCoreCastToolStore(t)
	review := &domain.PlanningReview{Brief: "canonical context", StartPrompt: "start"}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatal(err)
	}
	fence := &store.FoundationGenerationFence{Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	support := domain.Character{ID: "mara", Name: "Mara", Role: "witness", Goal: "expose the council", Motivation: "justice", Conflict: "fear of reprisal", Arc: "from silence to testimony", Constraints: []string{"never fabricates evidence"}}
	characters := append(append([]domain.Character(nil), foundation.Characters...), support)
	planned := []domain.CharacterRelationship{{
		ID: "rel-lin-mara", SourceCharacterID: "lin", TargetCharacterID: "mara", Type: domain.RelationshipTypeAlly,
		Direction: domain.RelationshipDirectionMutual, Status: domain.RelationshipStatusPlanned, Description: "Lin protects Mara's testimony",
	}}
	if _, err := st.SaveFoundationPremise(fence, "Lin and Mara expose the council without inventing evidence."); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationCharacters(fence, characters); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFoundationRelationships(fence, planned); err != nil {
		t.Fatal(err)
	}
	review, err = st.SaveFoundationWorldRules(fence, []domain.WorldRule{{ID: "rule-evidence", Rule: "Evidence cannot be fabricated", Strength: domain.WorldRuleStrengthHard}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConfirmFoundation(review.FoundationRevision, review.FoundationAuditSignature); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveRelationships([]domain.RelationshipEntry{{CharacterA: "Lin", CharacterB: "Mara", Relation: "RUNTIME_ONLY_RIVALRY", Chapter: 7}}); err != nil {
		t.Fatal(err)
	}

	result, err := NewContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	for _, required := range []string{
		`"foundation_audit_signature"`, review.FoundationAuditSignature, `"planned_relationships"`, `rel-lin-mara`,
		`"hard_world_rule_constraints"`, `Evidence cannot be fabricated`, `expose the council`, `from silence to testimony`,
		`relationship_state is runtime chapter evidence`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("architect context is missing %q: %s", required, text)
		}
	}
	if strings.Contains(text, "RUNTIME_ONLY_RIVALRY") {
		t.Fatalf("runtime relationship leaked into planned relationship context: %s", text)
	}
}

func confirmedCoreCastToolStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewStore(testStoreDir(t))
	confirmCoreCastForToolTest(t, st)
	return st
}

func confirmCoreCastForToolTest(t *testing.T, st *store.Store) {
	t.Helper()
	if existingBinding, loadErr := st.CoreCast.LoadGateBinding(); loadErr == nil && existingBinding != nil {
		if _, requireErr := st.CoreCast.RequireConfirmedGate(*existingBinding, nil, nil, nil); requireErr == nil {
			return
		}
	}
	binding, err := st.CoreCast.SaveGateBinding(store.CoreCastGateBinding{Mode: domain.CoreCastModeNormal, DraftRevision: 1, DraftHash: "draft-hash"})
	if err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeNormal, DraftRevision: binding.DraftRevision, DraftHash: binding.DraftHash,
		Members: []domain.CoreCastMember{{
			Character:  domain.Character{ID: "lin", Name: "Lin", Role: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "accept leadership", Traits: []string{"brave"}, Constraints: []string{"will not betray friends"}},
			Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal, MainlineFunction: "drives the central conflict", NoCoreRelationships: true,
		}},
	}
	saved, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func approveFoundationToolFixture(t *testing.T, st *store.Store) *domain.PlanningReview {
	t.Helper()
	confirmCoreCastForToolTest(t, st)
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		t.Fatal(err)
	}
	if review == nil {
		review = &domain.PlanningReview{}
	}
	review.Brief = "approved tool fixture"
	review.StartPrompt = "start"
	if review.FoundationStatus == domain.FoundationReviewStatusApproved {
		review, err = st.ReviseFoundation("refresh approved tool fixture")
		if err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := st.BeginFoundationReview(review); err != nil {
			t.Fatal(err)
		}
	}
	fence := &store.FoundationGenerationFence{Generation: review.FoundationGeneration, BaseRevision: review.FoundationBaseRevision}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	premise := strings.TrimSpace(foundation.Premise)
	if premise == "" {
		premise = "A complete approved premise"
	} else {
		premise += "\n\nApproved Foundation fixture."
	}
	if _, err = st.SaveFoundationPremise(fence, premise); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveFoundationCharacters(fence, foundation.Characters); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveFoundationRelationships(fence, foundation.Relationships); err != nil {
		t.Fatal(err)
	}
	rules := foundation.WorldRules
	if len(rules) == 0 {
		rules = []domain.WorldRule{{ID: "rule-approved", Rule: "Consequences cannot be reset", Strength: domain.WorldRuleStrengthHard}}
	}
	review, err = st.SaveFoundationWorldRules(fence, rules)
	if err != nil {
		t.Fatal(err)
	}
	review, err = st.ConfirmFoundation(review.FoundationRevision, review.FoundationAuditSignature)
	if err != nil {
		t.Fatal(err)
	}
	review.Status = domain.PlanningReviewStatusApproved
	if err := st.RunMeta.SetPlanningReview(review); err != nil {
		t.Fatal(err)
	}
	return review
}

func collectApprovedFoundationBlueprintFixture(t *testing.T, st *store.Store) *domain.PlanningReview {
	t.Helper()
	review := approveFoundationToolFixture(t, st)
	review.Kind = domain.PlanningReviewKindBlueprint
	review.Status = domain.PlanningReviewStatusCollecting
	if err := st.RunMeta.SetPlanningReview(review); err != nil {
		t.Fatal(err)
	}
	return review
}

func confirmAdaptationCoreCastForToolTest(t *testing.T, st *store.Store) {
	t.Helper()
	binding, err := st.CoreCast.SaveGateBinding(store.CoreCastGateBinding{
		Mode: domain.CoreCastModeAdaptation, DraftRevision: 1, DraftHash: "adaptation-draft",
		SourceSignature: "source-signature", AdaptationIntentHash: "adaptation-intent",
	})
	if err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeAdaptation,
		DraftRevision: binding.DraftRevision, DraftHash: binding.DraftHash,
		SourceSignature: binding.SourceSignature, AdaptationIntentHash: binding.AdaptationIntentHash,
		Members: []domain.CoreCastMember{{
			Character:  domain.Character{ID: "lin", Name: "Lin", Role: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "leadership", Traits: []string{"brave"}, Constraints: []string{"loyal"}},
			Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal,
			MainlineFunction: "drives the adaptation", InclusionRationale: "required by the confirmed target", NoCoreRelationships: true,
		}},
	}
	saved, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func confirmedCoreCharacterToolFixture() map[string]any {
	return map[string]any{
		"id": "lin", "name": "Lin", "role": "hero", "goal": "save home", "motivation": "duty", "conflict": "fear",
		"arc": "accept leadership", "traits": []string{"brave"}, "constraints": []string{"will not betray friends"},
	}
}

func confirmedCoreCharacterDomainFixture() domain.Character {
	return domain.Character{ID: "lin", Name: "Lin", Role: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "accept leadership", Traits: []string{"brave"}, Constraints: []string{"will not betray friends"}}
}
