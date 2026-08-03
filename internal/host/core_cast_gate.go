package host

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// RequireCoreCastGate revalidates the durable semantic binding and, for
// adaptation, derives every dependency from the current persisted artifacts.
func RequireCoreCastGate(st *store.Store, mode domain.CoreCastMode, publish bool) error {
	if st == nil || st.CoreCast == nil {
		return fmt.Errorf("core cast gate store is unavailable")
	}
	binding, err := st.CoreCast.LoadGateBinding()
	if err != nil {
		return err
	}
	if binding == nil {
		return fmt.Errorf("core cast gate binding does not exist")
	}
	if binding.Mode != mode {
		return fmt.Errorf("core cast gate mode is stale")
	}
	expected := *binding
	var sourceCharacters []domain.SourceMajorCharacter
	var sourceMajor []domain.SourceMajorCharacter
	var sourceMissing []domain.CoreCastMissingItem
	if mode == domain.CoreCastModeAdaptation {
		manifest, loadErr := st.Adaptation.LoadSourceManifest()
		if loadErr != nil {
			return fmt.Errorf("load current adaptation source manifest for core cast gate: %w", loadErr)
		}
		if manifest == nil {
			return fmt.Errorf("current adaptation source manifest is required for core cast gate")
		}
		dossier, loadErr := st.Adaptation.LoadCoCreateDossier()
		if loadErr != nil {
			return fmt.Errorf("load current adaptation dossier for core cast gate: %w", loadErr)
		}
		if dossier == nil {
			return fmt.Errorf("current adaptation dossier is required for core cast gate")
		}
		if !store.CoCreateDossierMatchesManifest(*dossier, *manifest, adapt.CoCreateDossierPromptVersion, adapt.CoCreateDossierBatchSize, adapt.CoCreateDossierBatchRuneLimit) {
			return fmt.Errorf("core cast gate adaptation dossier is stale for the current source manifest")
		}
		intent, loadErr := st.Adaptation.LoadCoCreateIntent()
		if loadErr != nil {
			return fmt.Errorf("load current adaptation intent for core cast gate: %w", loadErr)
		}
		if intent == nil {
			return fmt.Errorf("current adaptation intent is required for core cast gate")
		}
		intentHash := adapt.CoCreateIntentHash(*intent)
		if strings.TrimSpace(intent.IntentHash) != intentHash {
			return fmt.Errorf("core cast gate adaptation intent hash is stale")
		}
		briefing, loadErr := st.Adaptation.LoadCoCreateBriefing()
		if loadErr != nil {
			return fmt.Errorf("load current adaptation briefing for core cast gate: %w", loadErr)
		}
		sourceSignature := store.AdaptationSourceSignature(*manifest)
		if briefing == nil {
			if adapt.CoCreateBriefingTriggerReason(*dossier) != "" {
				return fmt.Errorf("current adaptation briefing is required for core cast gate")
			}
		} else if briefing.SourceSignature != sourceSignature || briefing.IntentHash != intentHash || len(adapt.PendingCoCreateBriefingDecisions(briefing)) != 0 {
			return fmt.Errorf("core cast gate adaptation briefing is stale or incomplete")
		}
		sourceFoundation, loadErr := st.Adaptation.LoadSourceFoundation()
		if loadErr != nil {
			return fmt.Errorf("load current adaptation source foundation for core cast gate: %w", loadErr)
		}
		if sourceFoundation == nil {
			return fmt.Errorf("current adaptation source foundation is required for core cast gate")
		}
		sourceCharacters = domain.ResolveSourceCharacters(*sourceFoundation)
		sourceMajor, sourceMissing = domain.ResolveSourceMajorCharacters(*sourceFoundation, *dossier)
		if contract, contractErr := st.CoreCast.Load(); contractErr == nil && contract != nil {
			candidate, candidateErr := st.CharacterCards.LoadCandidate()
			if candidateErr == nil && candidate != nil &&
				candidate.ProjectedCast.ContentSignature == contract.ContentSignature &&
				contract.ConfirmedSignature == contract.ContentSignature {
				sourceMajor = coreCastDispositionSources(sourceCharacters, *contract)
				sourceMissing = nil
			} else {
				currentCandidate, lifecycle, _, characterErr := tools.CurrentCharacterWorkflow(st)
				if characterErr == nil && currentCandidate != nil && lifecycle != nil &&
					lifecycle.Mode == domain.CharacterCardProjectAdaptation &&
					lifecycle.AnalysisStatus == domain.CharacterCardAnalysisCandidateReady &&
					lifecycle.ReviewStatus == domain.CharacterCardReviewPassed &&
					lifecycle.ConfirmationStatus == domain.CharacterCardConfirmed {
					characterSourceMajor, sourceErr := adaptationCoreSourceCharacters(
						st,
						lifecycle.SourceMappings,
						*contract,
					)
					if sourceErr == nil {
						sourceMajor = characterSourceMajor
						sourceMissing = nil
					}
				}
			}
		}
		expected.SourceSignature = sourceSignature
		expected.AdaptationIntentHash = intentHash
	} else {
		expected.SourceSignature = ""
		expected.AdaptationIntentHash = ""
	}
	if _, err := st.CoreCast.RequireConfirmedGate(expected, sourceCharacters, sourceMajor, sourceMissing); err != nil {
		return fmt.Errorf("core cast gate blocked: %w", err)
	}
	if publish {
		if _, err := st.CoreCast.PublishConfirmed(st.Foundation, sourceCharacters, sourceMajor, sourceMissing); err != nil {
			return fmt.Errorf("publish confirmed core cast: %w", err)
		}
	}
	return nil
}

func coreCastDispositionSources(
	sourceCharacters []domain.SourceMajorCharacter,
	contract domain.CoreCastContract,
) []domain.SourceMajorCharacter {
	disposed := make(map[string]struct{}, len(contract.SourceDispositions))
	for _, disposition := range contract.SourceDispositions {
		disposed[disposition.SourceCharacterID] = struct{}{}
	}
	out := make([]domain.SourceMajorCharacter, 0, len(disposed))
	for _, source := range sourceCharacters {
		if _, exists := disposed[source.ID]; exists {
			out = append(out, source)
		}
	}
	return out
}

func RequireManagedCoreCastGate(st *store.Store, publish bool) error {
	binding, err := st.CoreCast.LoadGateBinding()
	if err != nil {
		return err
	}
	if binding == nil {
		return fmt.Errorf("core cast gate binding does not exist; formal resume requires a current explicitly confirmed core cast")
	}
	return RequireCoreCastGate(st, binding.Mode, publish)
}

// RequireResumeCoreCastGate permits the Character Agent to create or review
// the staged cast before the CoreCast contract exists. Every later resume
// still requires the ordinary confirmed gate.
func RequireResumeCoreCastGate(st *store.Store, publish bool) error {
	pending, err := AdaptationCharacterWorkflowPending(st)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	pending, err = OriginalCharacterWorkflowPending(st)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	return RequireManagedCoreCastGate(st, publish)
}

// AdaptationCharacterWorkflowPending reports only the pre-confirmation states
// that the Character Agent can advance. A passed review intentionally returns
// false so Resume remains blocked until explicit user confirmation.
func AdaptationCharacterWorkflowPending(st *store.Store) (bool, error) {
	binding, err := st.CoreCast.LoadGateBinding()
	if err != nil {
		return false, err
	}
	workflow, workflowErr := st.Adaptation.LoadPlanningWorkflow()
	if workflowErr != nil {
		return false, workflowErr
	}
	if binding != nil && binding.Mode == domain.CoreCastModeAdaptation &&
		workflow != nil && workflow.Stage == domain.AdaptationPlanningStageTargetFoundationGenerating {
		candidate, lifecycle, _, characterErr := tools.CurrentCharacterWorkflow(st)
		if characterErr != nil || candidate == nil || lifecycle == nil ||
			lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
			lifecycle.ReviewStatus != domain.CharacterCardReviewPassed {
			return true, nil
		}
	}
	return false, nil
}

// OriginalCharacterWorkflowPending reports the pre-confirmation states that
// the Character Agent can advance during initial Foundation generation. A
// passed review intentionally falls through to the CoreCast gate so Resume
// cannot publish a candidate without explicit user confirmation.
func OriginalCharacterWorkflowPending(st *store.Store) (bool, error) {
	if st == nil {
		return false, fmt.Errorf("core cast gate store is unavailable")
	}
	review, err := st.RunMeta.PlanningReview()
	if err != nil {
		return false, err
	}
	if review == nil ||
		review.Kind != domain.PlanningReviewKindFoundation ||
		review.Status != domain.PlanningReviewStatusCollecting {
		return false, nil
	}

	candidate, lifecycle, _, characterErr := tools.CurrentCharacterWorkflow(st)
	if characterErr != nil || candidate == nil || lifecycle == nil ||
		lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady {
		return true, nil
	}
	switch lifecycle.ReviewStatus {
	case domain.CharacterCardReviewNotReviewed, domain.CharacterCardReviewStale,
		domain.CharacterCardReviewFailed, domain.CharacterCardReviewInProgress:
		return true, nil
	default:
		return false, nil
	}
}
