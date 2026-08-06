package host

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/flow"
	"github.com/voocel/ainovel-cli/internal/host/reminder"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

func TestPlanningReviewResumeAndStopGuardReuseHostAuthorization(t *testing.T) {
	st := newPlanningReviewRouteStore(t)
	registry := tools.NewPlanningReviewRunRegistry()
	router := flow.NewDispatcher(nil, st, registry)
	h := &Host{store: st, router: router}

	prompt, err := h.initialRoutePrompt("resume", true)
	if err != nil {
		t.Fatal(err)
	}
	reviewID, err := registry.ResolveActive(tools.PlanningReviewSelector{Volume: 1})
	if err != nil {
		t.Fatalf("resolve Resume authorization: %v; prompt=%s; state=%+v", err, prompt, flow.LoadState(st))
	}
	for _, want := range []string{"subagent(editor", "planning_review, volume=1", "Host has authorized review_id", reviewID} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("initial Resume prompt omitted %q: %s", want, prompt)
		}
	}

	guard := reminder.NewStopGuard(st, nil, flow.NewPlanningReviewTaskPreparer(registry))
	decision := guard(context.Background(), agentcore.StopInfo{TurnIndex: 1})
	if decision.Allow || decision.InjectMessage == "" {
		t.Fatalf("StopGuard decision = %+v", decision)
	}
	if !strings.Contains(decision.InjectMessage, "Host has authorized review_id") ||
		!strings.Contains(decision.InjectMessage, reviewID) {
		t.Fatalf("StopGuard did not reuse active review ID: %s", decision.InjectMessage)
	}
	afterReminder, err := registry.ResolveActive(tools.PlanningReviewSelector{Volume: 1})
	if err != nil {
		t.Fatal(err)
	}
	if afterReminder != reviewID || registry.ActiveRunCount() != 1 {
		t.Fatalf("StopGuard replaced active cursor run: before=%q after=%q active=%d", reviewID, afterReminder, registry.ActiveRunCount())
	}

	registry.Clear()
	withoutAuthorization := guard(context.Background(), agentcore.StopInfo{TurnIndex: 2})
	if strings.Contains(withoutAuthorization.InjectMessage, "subagent(editor") ||
		strings.Contains(withoutAuthorization.InjectMessage, "planning_review, volume=1") {
		t.Fatalf("StopGuard reissued an unauthorized planning review: %s", withoutAuthorization.InjectMessage)
	}
	if !strings.Contains(withoutAuthorization.InjectMessage, "fresh Host authorization") {
		t.Fatalf("StopGuard did not return an unauthorized review to Dispatcher: %s", withoutAuthorization.InjectMessage)
	}
}

func newPlanningReviewRouteStore(t *testing.T) *storepkg.Store {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{
		Version:       domain.CoreCastContractVersion,
		Mode:          domain.CoreCastModeNormal,
		DraftRevision: 1,
		DraftHash:     "planning-review-route",
		Members: []domain.CoreCastMember{{
			Character: domain.Character{
				ID: "lead", Name: "Lead", Role: "protagonist", Description: "investigator",
				Goal: "find the truth", Motivation: "duty", Conflict: "institutional pressure", Arc: "chooses truth",
				Traits: []string{"persistent"}, Constraints: []string{"will preserve evidence"},
			},
			Importance:          domain.CoreCastImportanceProtagonist,
			Origin:              domain.CoreCastOriginOriginal,
			MainlineFunction:    "drives the investigation",
			NoCoreRelationships: true,
		}},
	}
	if _, err := st.CoreCast.SaveGateBinding(storepkg.CoreCastGateBinding{
		Mode: domain.CoreCastModeNormal, DraftRevision: contract.DraftRevision, DraftHash: contract.DraftHash,
	}); err != nil {
		t.Fatalf("save CoreCast gate: %v", err)
	}
	savedCast, err := st.CoreCast.SaveCAS(contract, 0)
	if err != nil {
		t.Fatalf("save CoreCast: %v", err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(savedCast.Revision, savedCast.ContentSignature, nil, nil, nil); err != nil {
		t.Fatalf("confirm CoreCast: %v", err)
	}
	if _, err := st.CoreCast.PublishConfirmed(st.Foundation, nil, nil, nil); err != nil {
		t.Fatalf("publish CoreCast: %v", err)
	}
	review := &domain.PlanningReview{
		Status: domain.PlanningReviewStatusCollecting,
		Kind:   domain.PlanningReviewKindBlueprint,
		Brief:  "bounded planning review",
	}
	if _, err := st.BeginFoundationReview(review); err != nil {
		t.Fatalf("begin Foundation: %v", err)
	}
	fence := &storepkg.FoundationGenerationFence{
		Generation:   review.FoundationGeneration,
		BaseRevision: review.FoundationBaseRevision,
	}
	if _, err := st.SaveFoundationPremise(fence, "A complete premise"); err != nil {
		t.Fatalf("save premise: %v", err)
	}
	if _, err := st.SaveFoundationCharacters(fence, domain.ContractCharacters(contract)); err != nil {
		t.Fatalf("save characters: %v", err)
	}
	if _, err := st.SaveFoundationRelationships(fence, nil); err != nil {
		t.Fatalf("save relationships: %v", err)
	}
	if _, err := st.SaveFoundationWorldRules(fence, []domain.WorldRule{{
		ID: "rule-1", Category: "society", Title: "Evidence persists", Rule: "Every action leaves evidence",
		Boundary: "Evidence cannot be erased without a trace", Strength: domain.WorldRuleStrengthHard,
	}}); err != nil {
		t.Fatalf("save world rules: %v", err)
	}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatalf("load Foundation: %v", err)
	}
	foundationAudit, err := domain.FoundationAuditSignature(foundation)
	if err != nil {
		t.Fatalf("sign Foundation: %v", err)
	}
	if _, err := st.ConfirmFoundation(foundation.Revision, foundationAudit); err != nil {
		t.Fatalf("confirm Foundation: %v", err)
	}
	volumes := []domain.VolumeOutline{
		{Index: 1, Title: "Opening", Theme: "discover", Arcs: []domain.ArcOutline{{Index: 1, Title: "Entry", Goal: "enter", EstimatedChapters: 3}}},
		{Index: 2, Title: "Closure", Theme: "resolve", Arcs: []domain.ArcOutline{{Index: 1, Title: "Truth", Goal: "resolve", EstimatedChapters: 3}}},
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatalf("save flat outline: %v", err)
	}
	if err := st.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: "The truth becomes public and every central promise closes.",
		OpenThreads:     []string{"who controls the evidence"}, EstimatedScale: "two volumes",
	}); err != nil {
		t.Fatalf("save compass: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseOutline, Layered: true}); err != nil {
		t.Fatal(err)
	}
	return st
}
