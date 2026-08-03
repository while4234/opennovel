package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestOriginalCharacterContextDeduplicatesLegacyCastAndStartupPrompt(t *testing.T) {
	st := characterToolStore(t)
	brief := "必须出现心腹助理沈辞，冷面能干；保留全部角色约束。"
	startPrompt := "[创作要求]\n" + brief
	if err := st.RunMeta.SetPlanningReview(&domain.PlanningReview{
		Status: "collecting", Kind: "foundation", Brief: brief,
		StartPrompt: startPrompt, TargetTotalWords: 200000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UserRules.Save(&rules.Snapshot{
		Version: rules.SnapshotVersion, Status: rules.StatusDegraded,
		Structured:  rules.Structured{ChapterWords: &rules.WordRange{Min: 3000, Max: 5000}},
		Preferences: "[startup_prompt]\n" + startPrompt,
		Sources:     []string{"system_defaults", "startup_prompt"},
	}); err != nil {
		t.Fatal(err)
	}
	core := domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeNormal,
		DraftRevision: 1, DraftHash: "legacy-draft",
		Members: []domain.CoreCastMember{{
			Character: completeCharacterCandidate()[0], Importance: domain.CoreCastImportanceProtagonist,
			Origin: domain.CoreCastOriginOriginal, MainlineFunction: "legacy role",
			InclusionRationale: "legacy seed", NoCoreRelationships: true,
		}},
	}
	if _, err := st.CoreCast.SaveCAS(core, 0); err != nil {
		t.Fatal(err)
	}

	packet, _, err := buildCharacterContext(st, "bounded-original", CharacterRunAnalyze)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, brief) != 1 {
		t.Fatalf("brief occurrence count = %d; packet=%s", strings.Count(text, brief), text)
	}
	if _, exposed := packet["core_cast"]; strings.Contains(text, `"start_prompt"`) || exposed ||
		!strings.Contains(text, `"legacy_core_cast_binding"`) ||
		!strings.Contains(text, `"target_total_words":200000`) {
		t.Fatalf("original packet was not compacted safely: %s", text)
	}
}

func TestCharacterToolsAnalyzeAndIndependentReview(t *testing.T) {
	st := characterToolStore(t)
	registry := NewCharacterRunRegistry()
	binding := readCharacterContext(t, st, registry, "analyze-1", CharacterRunAnalyze)
	candidate := completeCharacterCandidate()
	result := saveCharacterCandidate(t, st, registry, "analyze-1", "candidate-1", binding, candidate)
	if result["saved"] != true || result["ready_for_review"] != true {
		t.Fatalf("analyze result = %+v", result)
	}
	foundation, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if foundation.Premise != "locked premise" || len(foundation.WorldRules) != 1 ||
		len(foundation.Characters) != 0 {
		t.Fatalf("analyze mutated canonical Foundation: %+v", foundation)
	}
	staged, err := st.CharacterCards.LoadCandidate()
	if err != nil || staged == nil || len(staged.Foundation.Characters) != 1 ||
		staged.Foundation.Characters[0].Name != "林澈" {
		t.Fatalf("staged candidate = %+v err=%v", staged, err)
	}

	reviewBinding := readCharacterContext(t, st, registry, "review-1", CharacterRunReview)
	reviewResult := saveCharacterReview(t, st, registry, "review-1", "review-key-1", reviewBinding, "pass", nil)
	if reviewResult["passed"] != true || reviewResult["final_status"] != string(domain.CharacterCardReviewPassed) {
		t.Fatalf("review result = %+v", reviewResult)
	}
	lifecycle, err := st.CharacterCards.Load(reviewBinding)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle == nil || lifecycle.RunID != "review-1" ||
		lifecycle.ReviewedCandidate != reviewBinding.Candidate ||
		lifecycle.ReviewedInputDigest != reviewBinding.InputDigest {
		t.Fatalf("review lifecycle = %+v", lifecycle)
	}
}

func TestCharacterCandidateEditInvalidatesPriorReview(t *testing.T) {
	st := characterToolStore(t)
	registry := NewCharacterRunRegistry()
	binding := readCharacterContext(t, st, registry, "analyze-edit", CharacterRunAnalyze)
	saveCharacterCandidate(t, st, registry, "analyze-edit", "candidate-edit", binding, completeCharacterCandidate())
	reviewBinding := readCharacterContext(t, st, registry, "review-edit", CharacterRunReview)
	saveCharacterReview(t, st, registry, "review-edit", "review-edit-key", reviewBinding, "pass", nil)

	candidate, err := st.CharacterCards.LoadCandidate()
	if err != nil || candidate == nil {
		t.Fatalf("candidate = %+v, %v", candidate, err)
	}
	candidate.Foundation.Characters[0].Goal = "Expose the conspiracy and rescue the missing witness."
	projected, _, err := domain.ProjectCharacterCandidateCoreCast(candidate.Foundation, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate.ProjectedCast = projected
	if _, err := st.CharacterCards.SaveCandidateCAS(*candidate, candidate.Revision); err != nil {
		t.Fatal(err)
	}
	_, lifecycle, _, err := CurrentCharacterWorkflow(st)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle == nil || lifecycle.ReviewStatus != domain.CharacterCardReviewStale ||
		lifecycle.ConfirmationStatus != domain.CharacterCardUnconfirmed {
		t.Fatalf("edited candidate lifecycle = %+v", lifecycle)
	}
}

func TestRebindConfirmedCharacterWorkflowIgnoresDownstreamPlanningRevision(t *testing.T) {
	st := characterToolStore(t)
	published, err := st.Foundation.Load()
	if err != nil {
		t.Fatal(err)
	}
	published.Characters = completeCharacterCandidate()
	published.RelationshipsReviewed = true
	published, err = st.Foundation.SaveCAS(published, published.Revision)
	if err != nil {
		t.Fatal(err)
	}
	initialReview := &domain.PlanningReview{
		Status:      domain.PlanningReviewStatusPending,
		Kind:        domain.PlanningReviewKindChapterOutline,
		Brief:       "the confirmed upstream creative brief",
		StartPrompt: "the confirmed upstream start prompt",
	}
	if err := st.RunMeta.SetPlanningReview(initialReview); err != nil {
		t.Fatal(err)
	}
	if err := st.UserRules.Save(&rules.Snapshot{
		Version: rules.SnapshotVersion, Status: rules.StatusReady,
		Preferences: "confirmed upstream writing preferences",
	}); err != nil {
		t.Fatal(err)
	}
	_, binding, inputs, _, err := CurrentCharacterCanonicalBinding(st)
	if err != nil {
		t.Fatal(err)
	}
	binding, err = domain.CharacterCardBindingFromFoundation(published, inputs)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := st.CharacterCards.SaveCandidateCAS(domain.CharacterCardCandidate{
		Version:    domain.CharacterCardCandidateVersion,
		Base:       binding,
		Foundation: published,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	completeness, err := domain.EvaluateCharacterCardCompleteness(published, nil)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := st.CharacterCards.SaveCAS(domain.CharacterCardLifecycle{
		Version:             domain.CharacterCardLifecycleVersion,
		Mode:                domain.CharacterCardProjectOriginal,
		Candidate:           binding.Candidate,
		Inputs:              binding.Inputs,
		InputDigest:         binding.InputDigest,
		Completeness:        completeness,
		AnalysisStatus:      domain.CharacterCardAnalysisCandidateReady,
		ReviewStatus:        domain.CharacterCardReviewPassed,
		ReviewedCandidate:   binding.Candidate,
		ReviewedInputDigest: binding.InputDigest,
		ConfirmationStatus:  domain.CharacterCardConfirmed,
		RunID:               "confirmed-run",
		IdempotencyKey:      "confirmed-key",
		SubmissionDigest:    strings.Repeat("a", 64),
	}, 0, binding)
	if err != nil {
		t.Fatal(err)
	}
	updated := domain.CloneStoryFoundation(published)
	updated.Premise = "A newly generated premise that does not change the cast."
	if _, err := st.Foundation.SaveCAS(updated, published.Revision); err != nil {
		t.Fatal(err)
	}
	revisedReview := *initialReview
	revisedReview.Brief = "a downstream chapter-outline revision that must not invalidate confirmed characters"
	revisedReview.StartPrompt = "a downstream regenerated planning prompt"
	if err := st.RunMeta.SetPlanningReview(&revisedReview); err != nil {
		t.Fatal(err)
	}
	if err := st.UserRules.Save(&rules.Snapshot{
		Version: rules.SnapshotVersion, Status: rules.StatusReady,
		Preferences: "updated downstream writing preferences",
	}); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, _, _, currentErr := CurrentCharacterWorkflow(store.NewStore(st.Dir()))
			errs <- currentErr
		}()
	}
	for range 2 {
		if currentErr := <-errs; currentErr != nil {
			t.Fatal(currentErr)
		}
	}
	reboundCandidate, reboundLifecycle, reboundBinding, err := CurrentCharacterWorkflow(st)
	if err != nil {
		t.Fatal(err)
	}
	if reboundCandidate.Revision <= candidate.Revision ||
		reboundLifecycle.Revision <= lifecycle.Revision ||
		reboundLifecycle.ConfirmationStatus != domain.CharacterCardConfirmed ||
		reboundLifecycle.Candidate != reboundBinding.Candidate ||
		reboundLifecycle.ReviewedCandidate != reboundBinding.Candidate {
		t.Fatalf("rebound candidate=%+v lifecycle=%+v binding=%+v", reboundCandidate, reboundLifecycle, reboundBinding)
	}
	if reboundBinding.Inputs.CreativeBrief != binding.Inputs.CreativeBrief {
		t.Fatalf("downstream planning revision changed confirmed creative brief: before=%q after=%q", binding.Inputs.CreativeBrief, reboundBinding.Inputs.CreativeBrief)
	}
	if reboundBinding.InputDigest == binding.InputDigest {
		t.Fatal("updated user_rules should still advance the rebound input digest")
	}
}

func TestConfirmedCharacterRebindAllowsOnlyMissingGenderCompletion(t *testing.T) {
	legacyFoundation := domain.StoryFoundation{
		SchemaVersion:         domain.StoryFoundationSchemaVersion,
		Revision:              3,
		Premise:               "locked premise",
		Characters:            completeCharacterCandidate(),
		Relationships:         []domain.CharacterRelationship{},
		RelationshipsReviewed: true,
		WorldRules:            []domain.WorldRule{{ID: "rule", Rule: "facts stay stable"}},
	}
	legacyFoundation.Characters[0].Gender = ""
	legacyCore, _, err := domain.ProjectCharacterCandidateCoreCast(legacyFoundation, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyCore, err = domain.NormalizeCoreCastContract(legacyCore)
	if err != nil {
		t.Fatal(err)
	}
	legacyInputs := domain.CharacterCardInputSignatures{
		CreativeBrief: strings.Repeat("a", 64),
		CoreCast:      legacyCore.ContentSignature,
		Additional: []domain.CharacterCardNamedSignature{{
			Name:      "user_rules",
			Signature: strings.Repeat("b", 64),
		}},
	}
	legacyBinding, err := domain.CharacterCardBindingFromFoundation(legacyFoundation, legacyInputs)
	if err != nil {
		t.Fatal(err)
	}

	canonical := domain.CloneStoryFoundation(legacyFoundation)
	canonical.Revision++
	canonical.Characters[0].Gender = "female"
	currentCore := cloneCoreCastForCharacterRepair(legacyCore)
	currentCore.Members[0].Character.Gender = "female"
	currentCore, err = domain.NormalizeCoreCastContract(currentCore)
	if err != nil {
		t.Fatal(err)
	}
	currentInputs := legacyInputs
	currentInputs.Additional = append([]domain.CharacterCardNamedSignature(nil), legacyInputs.Additional...)
	currentInputs.CoreCast = currentCore.ContentSignature
	currentInputs.Additional[0].Signature = strings.Repeat("c", 64)
	currentBinding, err := domain.CharacterCardBindingFromFoundation(canonical, currentInputs)
	if err != nil {
		t.Fatal(err)
	}
	candidate := domain.CharacterCardCandidate{
		Version:       domain.CharacterCardCandidateVersion,
		Base:          legacyBinding,
		Foundation:    legacyFoundation,
		ProjectedCast: legacyCore,
	}
	if !confirmedCharacterRebindCompatible(candidate, canonical, currentBinding, &currentCore) {
		t.Fatal("missing gender completion should be accepted as a metadata-only upgrade")
	}

	changed := domain.CloneStoryFoundation(canonical)
	changed.Characters[0].Goal = "Replace the previously confirmed story goal."
	changedBinding, err := domain.CharacterCardBindingFromFoundation(changed, currentInputs)
	if err != nil {
		t.Fatal(err)
	}
	if confirmedCharacterRebindCompatible(candidate, changed, changedBinding, &currentCore) {
		t.Fatal("a real Character content change was accepted as metadata completion")
	}

	currentInputs.Additional = append(currentInputs.Additional, domain.CharacterCardNamedSignature{
		Name:      "source_reports",
		Signature: strings.Repeat("d", 64),
	})
	changedEvidenceBinding, err := domain.CharacterCardBindingFromFoundation(canonical, currentInputs)
	if err != nil {
		t.Fatal(err)
	}
	if confirmedCharacterRebindCompatible(candidate, canonical, changedEvidenceBinding, &currentCore) {
		t.Fatal("non-user-rules Character evidence change was accepted")
	}
}

func TestCharacterReviewPassIsDowngradedByCompleteness(t *testing.T) {
	st := characterToolStore(t)
	registry := NewCharacterRunRegistry()
	binding := readCharacterContext(t, st, registry, "analyze-incomplete", CharacterRunAnalyze)
	incomplete := completeCharacterCandidate()
	incomplete[0].Motivation = ""
	saveCharacterCandidate(t, st, registry, "analyze-incomplete", "candidate-incomplete", binding, incomplete)

	reviewBinding := readCharacterContext(t, st, registry, "review-incomplete", CharacterRunReview)
	result := saveCharacterReview(t, st, registry, "review-incomplete", "review-incomplete-key", reviewBinding, "pass", nil)
	if result["passed"] != false || result["final_status"] != string(domain.CharacterCardReviewNeedsRevision) {
		t.Fatalf("incomplete review result = %+v", result)
	}
	findings, ok := result["findings"].([]any)
	if !ok || len(findings) == 0 {
		t.Fatalf("deterministic completeness finding missing: %+v", result)
	}
}

func TestCharacterReviewPassIsBlockedByMissingGender(t *testing.T) {
	st := characterToolStore(t)
	registry := NewCharacterRunRegistry()
	binding := readCharacterContext(t, st, registry, "analyze-missing-gender", CharacterRunAnalyze)
	incomplete := completeCharacterCandidate()
	incomplete[0].Gender = ""
	saveCharacterCandidate(t, st, registry, "analyze-missing-gender", "candidate-missing-gender", binding, incomplete)

	reviewBinding := readCharacterContext(t, st, registry, "review-missing-gender", CharacterRunReview)
	result := saveCharacterReview(t, st, registry, "review-missing-gender", "review-missing-gender-key", reviewBinding, "pass", nil)
	if result["passed"] != false || result["final_status"] != string(domain.CharacterCardReviewNeedsRevision) {
		t.Fatalf("missing-gender review result = %+v", result)
	}
	findings, ok := result["findings"].([]any)
	if !ok {
		t.Fatalf("missing-gender findings = %#v", result["findings"])
	}
	for _, item := range findings {
		finding, _ := item.(map[string]any)
		id, _ := finding["id"].(string)
		if strings.HasSuffix(id, ":gender_required") &&
			finding["blocking"] == true {
			return
		}
	}
	t.Fatalf("blocking gender finding missing: %+v", result)
}

func TestCharacterReviewRetryIsIdempotentOnlyForExactSubmission(t *testing.T) {
	st := characterToolStore(t)
	registry := NewCharacterRunRegistry()
	binding := readCharacterContext(t, st, registry, "analyze-for-retry", CharacterRunAnalyze)
	saveCharacterCandidate(t, st, registry, "analyze-for-retry", "candidate-for-retry", binding, completeCharacterCandidate())

	reviewBinding := readCharacterContext(t, st, registry, "review-retry", CharacterRunReview)
	request := reviewRequest("review-retry", "review-retry-key", reviewBinding, "pass", nil)
	tool := NewSaveCharacterReviewTool(st, registry)
	if _, err := tool.Execute(context.Background(), characterJSON(t, request)); err != nil {
		t.Fatal(err)
	}
	raw, err := tool.Execute(context.Background(), characterJSON(t, request))
	if err != nil {
		t.Fatalf("exact review retry should be idempotent: %v", err)
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result["idempotent"] != true {
		t.Fatalf("review retry result = %s", raw)
	}
	request.Summary = "different review content"
	if _, err := tool.Execute(context.Background(), characterJSON(t, request)); !errors.Is(err, errs.ErrToolConflict) {
		t.Fatalf("changed review retry err = %v", err)
	}
}

func TestCharacterToolsRejectModeDuplicateStaleAndUnknownFields(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		st := characterToolStore(t)
		registry := NewCharacterRunRegistry()
		binding := readCharacterContext(t, st, registry, "mode-run", CharacterRunAnalyze)
		request := candidateRequest("mode-run", "mode-key", binding, completeCharacterCandidate())
		request.Mode = CharacterRunReview
		if _, err := NewSaveCharacterCandidateTool(st, registry).Execute(context.Background(), characterJSON(t, request)); !errors.Is(err, errs.ErrToolConflict) {
			t.Fatalf("wrong mode err = %v", err)
		}
	})

	t.Run("duplicate and idempotent retry", func(t *testing.T) {
		st := characterToolStore(t)
		registry := NewCharacterRunRegistry()
		binding := readCharacterContext(t, st, registry, "duplicate-run", CharacterRunAnalyze)
		request := candidateRequest("duplicate-run", "duplicate-key", binding, completeCharacterCandidate())
		tool := NewSaveCharacterCandidateTool(st, registry)
		if _, err := tool.Execute(context.Background(), characterJSON(t, request)); err != nil {
			t.Fatal(err)
		}
		raw, err := tool.Execute(context.Background(), characterJSON(t, request))
		if err != nil {
			t.Fatalf("exact retry should be idempotent: %v", err)
		}
		var result map[string]any
		if json.Unmarshal(raw, &result) != nil || result["idempotent"] != true {
			t.Fatalf("retry result = %s", raw)
		}
		request.IdempotencyKey = "different-key"
		if _, err := tool.Execute(context.Background(), characterJSON(t, request)); !errors.Is(err, errs.ErrToolConflict) {
			t.Fatalf("duplicate different key err = %v", err)
		}
	})

	t.Run("stale", func(t *testing.T) {
		st := characterToolStore(t)
		registry := NewCharacterRunRegistry()
		binding := readCharacterContext(t, st, registry, "stale-run", CharacterRunAnalyze)
		foundation, _ := st.Foundation.Load()
		foundation.Premise = "user edited premise"
		if _, err := st.Foundation.SaveRevisionCAS(foundation, foundation.Revision); err != nil {
			t.Fatal(err)
		}
		request := candidateRequest("stale-run", "stale-key", binding, completeCharacterCandidate())
		if _, err := NewSaveCharacterCandidateTool(st, registry).Execute(context.Background(), characterJSON(t, request)); !errors.Is(err, errs.ErrToolConflict) ||
			!strings.Contains(err.Error(), "stale") {
			t.Fatalf("stale err = %v", err)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		tool := NewCharacterContextTool(characterToolStore(t), NewCharacterRunRegistry())
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{"run_id":"unknown","mode":"analyze","extra":true}`)); !errors.Is(err, errs.ErrToolArgs) {
			t.Fatalf("unknown field err = %v", err)
		}
	})
}

func TestCharacterContextIsBoundedAndNeverLoadsRawAdaptationText(t *testing.T) {
	st := characterToolStore(t)
	rawOutlineMarker := "RAW-OUTLINE-MUST-NOT-REACH-CHARACTER-" + strings.Repeat("x", 200_000)
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{
		Version:            1,
		SourceSignature:    strings.Repeat("a", 64),
		SourceChapterCount: 20,
		Characters: []domain.Character{{
			ID: "source-hero", Name: "原著主角", Role: "主角", Description: "source fact",
			Arc: "source arc", Traits: []string{"冷静"},
		}},
		Volumes: []domain.VolumeOutline{{
			Index: 1,
			Title: rawOutlineMarker,
			Theme: rawOutlineMarker,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	reports := make([]domain.AdaptationSourceReport, 0, 20)
	for chapter := 1; chapter <= 20; chapter++ {
		reports = append(reports, domain.AdaptationSourceReport{
			Chapter:        chapter,
			Title:          strings.Repeat("large report title", 500),
			Characters:     []string{"source-hero"},
			CharacterFacts: []string{strings.Repeat("large character fact", 500)},
		})
	}
	if err := st.Adaptation.SaveSourceReports(reports); err != nil {
		t.Fatal(err)
	}
	for _, report := range reports {
		if err := st.Adaptation.SaveSourceReport(report); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Adaptation.SaveCoCreateIntent(domain.AdaptationCoCreateIntent{
		Version: 1, RawRequest: "保留主线", IntentHash: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	contract := domain.CoreCastContract{
		Version:              domain.CoreCastContractVersion,
		Mode:                 domain.CoreCastModeAdaptation,
		DraftRevision:        1,
		DraftHash:            "draft",
		SourceSignature:      strings.Repeat("a", 64),
		AdaptationIntentHash: strings.Repeat("b", 64),
		Members: []domain.CoreCastMember{{
			Character:           completeCharacterCandidate()[0],
			Importance:          domain.CoreCastImportanceProtagonist,
			Origin:              domain.CoreCastOriginSource,
			MainlineFunction:    "drives plot",
			SourceCharacterIDs:  []string{"source-hero"},
			NoCoreRelationships: true,
		}},
		PlannedRelationships: []domain.CharacterRelationship{},
		SourceDispositions: []domain.SourceCharacterDisposition{{
			SourceCharacterID:  "source-hero",
			Action:             domain.SourceDispositionKeep,
			TargetCharacterIDs: []string{completeCharacterCandidate()[0].ID},
			Rationale:          "keep",
		}},
	}
	if _, err := st.CoreCast.SaveCAS(contract, 0); err != nil {
		t.Fatal(err)
	}
	registry := NewCharacterRunRegistry()
	raw, err := NewCharacterContextTool(st, registry).Execute(
		context.Background(),
		json.RawMessage(`{"run_id":"adapt-analyze","mode":"analyze"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "raw_source") && !strings.Contains(text, `"raw_source_included":false`) {
		t.Fatalf("context exposed raw source marker: %s", text)
	}
	if strings.Contains(text, "RAW-OUTLINE-MUST-NOT-REACH-CHARACTER") {
		t.Fatal("context exposed the source outline instead of bounded character evidence")
	}
	if !strings.Contains(text, `"project_mode":"adaptation"`) ||
		!strings.Contains(text, `"source_foundation"`) ||
		!strings.Contains(text, `"chapter_reports_omitted":true`) ||
		!strings.Contains(text, `"source_character_index"`) {
		t.Fatalf("adaptation evidence missing: %s", text)
	}
	if len(raw) > characterContextMaxBytes+2_048 {
		t.Fatalf("bounded context = %d bytes, want <= %d", len(raw), characterContextMaxBytes+2_048)
	}
}

func TestAdaptationCharacterCandidateBecomesStaleWhenInputsChange(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *store.Store)
	}{
		{
			name: "source foundation",
			mutate: func(t *testing.T, st *store.Store) {
				source, err := st.Adaptation.LoadSourceFoundation()
				if err != nil {
					t.Fatal(err)
				}
				source.Premise = "changed source fact"
				if err := st.Adaptation.SaveSourceFoundation(*source); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "adaptation intent",
			mutate: func(t *testing.T, st *store.Store) {
				if err := st.Adaptation.SaveCoCreateIntent(domain.AdaptationCoCreateIntent{
					Version: 1, RawRequest: "changed adaptation brief", IntentHash: strings.Repeat("c", 64),
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "core cast disposition",
			mutate: func(t *testing.T, st *store.Store) {
				core, err := st.CoreCast.Load()
				if err != nil {
					t.Fatal(err)
				}
				core.Members[0].MainlineFunction = "changed confirmed core function"
				if _, err := st.CoreCast.SaveCAS(*core, core.Revision); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := stagedAdaptationCharacterToolStore(t)
			test.mutate(t, st)
			if _, _, _, err := CurrentCharacterWorkflow(st); err == nil ||
				!strings.Contains(strings.ToLower(err.Error()), "stale") {
				t.Fatalf("stale workflow err = %v", err)
			}
		})
	}
}

func TestCharacterToolsExposeStrictSchemas(t *testing.T) {
	st := characterToolStore(t)
	registry := NewCharacterRunRegistry()
	for name, tool := range map[string]interface{ StrictSchema() bool }{
		"character_context":        NewCharacterContextTool(st, registry),
		"save_character_candidate": NewSaveCharacterCandidateTool(st, registry),
		"save_character_review":    NewSaveCharacterReviewTool(st, registry),
	} {
		if !tool.StrictSchema() {
			t.Fatalf("%s is not strict", name)
		}
	}
}

func characterToolStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewStore(testStoreDir(t))
	_, err := st.Foundation.SaveRevisionCAS(domain.StoryFoundation{
		SchemaVersion: domain.StoryFoundationSchemaVersion,
		Premise:       "locked premise",
		WorldRules: []domain.WorldRule{{
			ID: "rule-1", Category: "physics", Rule: "magic has a cost", Strength: domain.WorldRuleStrengthHard,
		}},
		Characters:    []domain.Character{},
		Relationships: []domain.CharacterRelationship{},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func stagedAdaptationCharacterToolStore(t *testing.T) *store.Store {
	t.Helper()
	st := characterToolStore(t)
	if err := st.Adaptation.SaveSourceFoundation(domain.AdaptationSourceFoundation{
		Version: 1, SourceSignature: strings.Repeat("a", 64), Premise: "source premise",
		Characters: []domain.Character{{
			ID: "source-hero", Name: "原著主角", Role: "主角", Description: "source fact",
			Arc: "source arc", Traits: []string{"冷静"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Adaptation.SaveCoCreateIntent(domain.AdaptationCoCreateIntent{
		Version: 1, RawRequest: "保留主线", IntentHash: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	gate, err := st.CoreCast.SaveGateBinding(store.CoreCastGateBinding{
		Mode: domain.CoreCastModeAdaptation, DraftRevision: 1, DraftHash: "draft",
		SourceSignature: strings.Repeat("a", 64), AdaptationIntentHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	core := domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeAdaptation,
		DraftRevision: gate.DraftRevision, DraftHash: gate.DraftHash, SourceSignature: gate.SourceSignature,
		AdaptationIntentHash: gate.AdaptationIntentHash,
		Members: []domain.CoreCastMember{{
			Character: completeCharacterCandidate()[0], Importance: domain.CoreCastImportanceProtagonist,
			Origin: domain.CoreCastOriginOriginal, MainlineFunction: "drives target story",
			InclusionRationale: "confirmed target lead", NoCoreRelationships: true,
		}},
	}
	saved, err := st.CoreCast.SaveCAS(core, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CoreCast.ConfirmCAS(saved.Revision, saved.ContentSignature, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	registry := NewCharacterRunRegistry()
	binding := readCharacterContext(t, st, registry, "adaptation-stale-analyze", CharacterRunAnalyze)
	request := candidateRequest("adaptation-stale-analyze", "adaptation-stale-key", binding, completeCharacterCandidate())
	request.SourceMappings = []domain.CharacterSourceMapping{{
		ID: "map-source-hero", Action: domain.CharacterSourceKeep,
		SourceCharacterIDs: []string{"source-hero"}, TargetCharacterIDs: []string{completeCharacterCandidate()[0].ID},
		Rationale: "keep source lead",
		Evidence: []domain.CharacterSourceEvidence{{
			Kind: domain.CharacterSourceAdaptationDecision, Reference: "fixture.intent", Summary: "keep decision",
		}},
	}}
	if _, err := NewSaveCharacterCandidateTool(st, registry).Execute(
		context.Background(), characterJSON(t, request),
	); err != nil {
		t.Fatal(err)
	}
	return st
}

func completeCharacterCandidate() []domain.Character {
	return []domain.Character{{
		ID:          "char-lin-che",
		Gender:      "female",
		Name:        "林澈",
		Aliases:     []string{},
		Role:        "调查者",
		Description: "以追查真相为职业，也害怕真相伤害家人",
		Arc:         "从控制信息到承担公开真相的后果",
		Traits:      []string{"克制", "敏锐"},
		Tier:        string(domain.CharacterTierCore),
		Faction:     "",
		Goal:        "查明失踪案",
		Motivation:  "保护妹妹免受同样伤害",
		Conflict:    "公开真相会摧毁家族声誉",
		Voice:       "短句，先问证据再表态",
		Constraints: []string{"不凭直觉指控无辜者"},
		ContrastDetails: []domain.CharacterContrastDetail{{
			Surface: "冷静", Depth: "面对妹妹时容易失去判断",
		}},
		KeyBackstory: []domain.CharacterBackstory{{
			Event: "曾误判证人", Impact: "现在必须交叉验证每条线索",
		}},
		InitialState: &domain.CharacterInitialState{
			Identity: "调查记者", Situation: "刚接到旧案新线索", Emotion: "戒备",
			Resources: []string{"匿名档案"}, Relationships: "与妹妹疏远",
		},
		KnowledgeBoundary: &domain.CharacterKnowledgeBoundary{
			Known: []string{"案件官方记录"}, Unknown: []string{"幕后主使"},
			Misconceptions: []string{"父亲已经离开本城"}, Forbidden: []string{"幕后主使真实身份"},
		},
		Notes: "",
	}}
}

func readCharacterContext(
	t *testing.T,
	st *store.Store,
	registry *CharacterRunRegistry,
	runID string,
	mode CharacterRunMode,
) domain.CharacterCardBinding {
	t.Helper()
	args := characterJSON(t, characterContextArgs{RunID: runID, Mode: mode})
	if _, err := NewCharacterContextTool(st, registry).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	_, binding, _, _, _, err := currentCharacterRunBinding(st, mode)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func candidateRequest(
	runID, key string,
	binding domain.CharacterCardBinding,
	characters []domain.Character,
) saveCharacterCandidateArgs {
	return saveCharacterCandidateArgs{
		RunID: runID, Mode: CharacterRunAnalyze, IdempotencyKey: key,
		BaseRevision:          binding.Candidate.FoundationRevision,
		BaseAuditSignature:    binding.Candidate.FoundationAuditSignature,
		CandidateDigest:       binding.Candidate.CharacterContentDigest,
		InputDigest:           binding.InputDigest,
		AnalysisSummary:       "generated from current evidence; uncertain choices are marked",
		Characters:            characters,
		Relationships:         []domain.CharacterRelationship{},
		RelationshipsReviewed: true,
		SourceMappings:        []domain.CharacterSourceMapping{},
	}
}

func saveCharacterCandidate(
	t *testing.T,
	st *store.Store,
	registry *CharacterRunRegistry,
	runID, key string,
	binding domain.CharacterCardBinding,
	characters []domain.Character,
) map[string]any {
	t.Helper()
	raw, err := NewSaveCharacterCandidateTool(st, registry).Execute(
		context.Background(),
		characterJSON(t, candidateRequest(runID, key, binding, characters)),
	)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func saveCharacterReview(
	t *testing.T,
	st *store.Store,
	registry *CharacterRunRegistry,
	runID, key string,
	binding domain.CharacterCardBinding,
	verdict string,
	findings []domain.CharacterCardReviewFinding,
) map[string]any {
	t.Helper()
	request := reviewRequest(runID, key, binding, verdict, findings)
	raw, err := NewSaveCharacterReviewTool(st, registry).Execute(context.Background(), characterJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func reviewRequest(
	runID, key string,
	binding domain.CharacterCardBinding,
	verdict string,
	findings []domain.CharacterCardReviewFinding,
) saveCharacterReviewArgs {
	if findings == nil {
		findings = []domain.CharacterCardReviewFinding{}
	}
	return saveCharacterReviewArgs{
		RunID: runID, Mode: CharacterRunReview, IdempotencyKey: key,
		BaseRevision:       binding.Candidate.FoundationRevision,
		BaseAuditSignature: binding.Candidate.FoundationAuditSignature,
		CandidateDigest:    binding.Candidate.CharacterContentDigest,
		InputDigest:        binding.InputDigest,
		Verdict:            verdict, Summary: "independent review of current candidate and evidence", Findings: findings,
	}
}

func characterJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
