package host

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type adaptationFoundationContext struct {
	Baseline         domain.FoundationAdaptationBaseline
	Contract         domain.CoreCastContract
	Plan             domain.AdaptationPlan
	Workflow         domain.AdaptationPlanningWorkflow
	SourceManifest   domain.AdaptationSourceManifest
	SourceFoundation domain.AdaptationSourceFoundation
}

func loadAdaptationFoundationContext(st *storepkg.Store) (*adaptationFoundationContext, error) {
	manifest, err := st.Adaptation.LoadSourceManifest()
	if err != nil || manifest == nil {
		return nil, fmt.Errorf("adaptation source manifest is unavailable: %w", err)
	}
	source, err := st.Adaptation.LoadSourceFoundation()
	if err != nil || source == nil {
		return nil, fmt.Errorf("adaptation source Foundation is unavailable: %w", err)
	}
	intent, err := st.Adaptation.LoadCoCreateIntent()
	if err != nil || intent == nil || strings.TrimSpace(intent.IntentHash) == "" {
		return nil, fmt.Errorf("adaptation intent baseline is unavailable: %w", err)
	}
	workflow, err := st.Adaptation.LoadPlanningWorkflow()
	if err != nil || workflow == nil || workflow.Revision <= 0 || !workflow.Stage.Valid() {
		return nil, fmt.Errorf("adaptation planning workflow baseline is unavailable: %w", err)
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil {
		return nil, fmt.Errorf("load confirmed adaptation plan: %w", err)
	}
	if plan == nil {
		plan, err = st.Adaptation.LoadProposal()
		if err != nil || plan == nil {
			return nil, fmt.Errorf("adaptation plan baseline is unavailable: %w", err)
		}
	}
	gate, err := st.CoreCast.LoadGateBinding()
	if err != nil || gate == nil || gate.Mode != domain.CoreCastModeAdaptation {
		return nil, fmt.Errorf("adaptation CoreCast gate is unavailable: %w", err)
	}
	currentContract, err := st.CoreCast.Load()
	if err != nil || currentContract == nil {
		return nil, fmt.Errorf("adaptation CoreCast contract is unavailable: %w", err)
	}
	sourceCharacters := domain.ResolveSourceCharacters(*source)
	sourceMajor := coreCastDispositionSources(sourceCharacters, *currentContract)
	contract, err := st.CoreCast.RequireConfirmedGate(*gate, sourceCharacters, sourceMajor, nil)
	if err != nil {
		return nil, fmt.Errorf("adaptation CoreCast confirmation is stale: %w", err)
	}
	sourcePayload, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	manifestSignature := domain.AdaptationSourceManifestContractSignature(*manifest)
	if strings.TrimSpace(contract.SourceSignature) != storepkg.AdaptationSourceSignature(*manifest) {
		return nil, fmt.Errorf("adaptation source binding is inconsistent")
	}
	if strings.TrimSpace(contract.AdaptationIntentHash) != strings.TrimSpace(intent.IntentHash) {
		return nil, fmt.Errorf("adaptation intent binding is inconsistent")
	}
	baseline := domain.FoundationAdaptationBaseline{
		SourceSignature:             domain.JSONContentSignature(sourcePayload),
		SourceManifestSignature:     manifestSignature,
		AdaptationIntentHash:        strings.TrimSpace(intent.IntentHash),
		WorkflowRevision:            workflow.Revision,
		WorkflowStage:               string(workflow.Stage),
		PlanSemanticSignature:       adaptationPlanSignature(*plan),
		PlanStoryContractSignature:  domain.AdaptationPlanStoryContractSignature(*plan),
		PlanOutlineQualitySignature: domain.AdaptationPlanOutlineQualitySignature(*plan),
	}
	if err := baseline.Validate(); err != nil {
		return nil, err
	}
	return &adaptationFoundationContext{Baseline: baseline, Contract: contract, Plan: *plan, Workflow: *workflow, SourceManifest: *manifest, SourceFoundation: *source}, nil
}

func validateAdaptationFoundationPlanningAudits(st *storepkg.Store, current *adaptationFoundationContext, candidate domain.StoryFoundation) (string, error) {
	if current == nil {
		return "", fmt.Errorf("adaptation planning context is required")
	}
	if err := adapt.ValidateAdaptationOutlineQuality(&current.Plan, &current.SourceManifest); err != nil {
		return "", fmt.Errorf("source-fidelity and outline-quality audit: %w", err)
	}
	if !domain.AdaptationOutlineQualityPassed(current.Plan) {
		return "", fmt.Errorf("outline-quality audit receipt is missing")
	}
	if _, err := st.RequireConfirmedAdaptationFoundation(); err != nil {
		return "", fmt.Errorf("target-consistency audit: %w", err)
	}
	if err := st.ValidateAdaptationArtifactBinding(current.Plan.FoundationBinding); err != nil {
		return "", fmt.Errorf("plan-contract audit: %w", err)
	}
	if !adaptationCoreCastMatchesCandidate(candidate, current.Contract) {
		return "", fmt.Errorf("character-mapping audit: confirmed CoreCast does not match the revised target Foundation")
	}
	if current.Workflow.Stage != domain.AdaptationPlanningStageConfirmed || current.Plan.Status != domain.AdaptationPlanStatusConfirmed {
		return "", fmt.Errorf("plan-contract audit: adaptation planning is not confirmed")
	}
	return fmt.Sprintf(
		"source_fidelity=%s; target_consistency=%s; character_mapping=%s; plan_contract=%s; outline_quality=%s",
		current.Baseline.SourceManifestSignature,
		current.Plan.FoundationBinding.TargetFoundationAuditSignature,
		current.Contract.ContentSignature,
		current.Baseline.PlanStoryContractSignature,
		current.Baseline.PlanOutlineQualitySignature,
	), nil
}

func adaptationFoundationEditableStage(stage domain.AdaptationPlanningStage) bool {
	switch stage {
	case domain.AdaptationPlanningStageFoundationReviewPending,
		domain.AdaptationPlanningStageVolumeReviewPending,
		domain.AdaptationPlanningStageProposalReviewPending,
		domain.AdaptationPlanningStageConfirmed:
		return true
	default:
		return false
	}
}

func adaptationReadonlyBaselineReason(err error) string {
	message := strings.ToLower(fmt.Sprint(err))
	switch {
	case strings.Contains(message, "source"):
		return "adaptation_source_inconsistent"
	case strings.Contains(message, "corecast"):
		return "adaptation_core_cast_inconsistent"
	case strings.Contains(message, "intent"):
		return "adaptation_intent_unavailable"
	case strings.Contains(message, "workflow"):
		return "adaptation_workflow_unavailable"
	case strings.Contains(message, "plan"):
		return "adaptation_plan_unavailable"
	default:
		return "adaptation_baseline_unavailable"
	}
}

func adaptationBaselineLoadError(err error) error {
	if strings.Contains(strings.ToLower(fmt.Sprint(err)), "source") {
		return foundationError(FoundationErrorSourceStale, err.Error())
	}
	return foundationError(FoundationErrorStale, err.Error())
}

func adaptationCoreCastMatchesCandidate(candidate domain.StoryFoundation, contract domain.CoreCastContract) bool {
	if contract.Mode != domain.CoreCastModeAdaptation || contract.ConfirmedSignature == "" || contract.ConfirmedSignature != contract.ContentSignature {
		return false
	}
	characters := make(map[string]domain.Character, len(candidate.Characters))
	for _, character := range candidate.Characters {
		characters[character.ID] = character
	}
	for _, member := range contract.Members {
		character, ok := characters[member.Character.ID]
		if !ok || !reflect.DeepEqual(character, member.Character) {
			return false
		}
	}
	relationships := make(map[string]domain.CharacterRelationship, len(candidate.Relationships))
	for _, relationship := range candidate.Relationships {
		relationships[relationship.ID] = relationship
	}
	for _, expected := range contract.PlannedRelationships {
		actual, ok := relationships[expected.ID]
		if !ok || !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	return true
}

func analyzeAdaptationFoundationImpact(impact domain.FoundationImpact, diff domain.FoundationDiff, dependencies *domain.FoundationDependencyManifest, contract domain.CoreCastContract, coreReconfirmed bool) domain.FoundationImpact {
	adaptation := &domain.FoundationAdaptationImpact{
		EvidenceLevel:                      "missing",
		RequiresCoreCastReconfirmation:     diff.CoreCastReconfirmation && !coreReconfirmed,
		SourceFidelityReview:               len(diff.Changes) > 0,
		TargetConsistencyReview:            len(diff.Changes) > 0,
		CharacterMappingReview:             len(diff.Changes) > 0,
		PlanContractReview:                 len(diff.Changes) > 0,
		OutlineQualityReview:               len(diff.Changes) > 0,
		AffectedProposal:                   len(diff.Changes) > 0,
		AffectedOutline:                    len(diff.Changes) > 0,
		RequiresAdaptationPlanConfirmation: adaptationFoundationRequiresPlanConfirmation(diff, contract),
	}
	if dependencies != nil {
		adaptation.EvidenceLevel = "structured"
		for _, entry := range dependencies.Entries {
			adaptation.SourceAnchorIDs = append(adaptation.SourceAnchorIDs, entry.SourceAnchorIDs...)
			adaptation.ContractIDs = append(adaptation.ContractIDs, entry.ContractIDs...)
		}
		adaptation.SourceAnchorIDs = sortedCompactStrings(adaptation.SourceAnchorIDs)
		adaptation.ContractIDs = sortedCompactStrings(adaptation.ContractIDs)
		if len(adaptation.SourceAnchorIDs) == 0 || len(adaptation.ContractIDs) == 0 {
			adaptation.EvidenceLevel = "target_only"
			impact.FullBook = true
			adaptation.ExpansionReasons = append(adaptation.ExpansionReasons, "adaptation_dependency_anchors_missing")
		}
	} else if len(diff.Changes) > 0 {
		adaptation.ExpansionReasons = append(adaptation.ExpansionReasons, "adaptation_dependency_evidence_missing")
	}
	for _, change := range diff.Changes {
		switch {
		case change.EntityType == domain.FoundationEntityPremise:
			adaptation.ExpansionReasons = append(adaptation.ExpansionReasons, "adaptation_mainline_changed")
		case change.CoreCastAffected:
			adaptation.ExpansionReasons = append(adaptation.ExpansionReasons, "adaptation_core_mapping_changed")
		case change.HardRuleAffected:
			adaptation.ExpansionReasons = append(adaptation.ExpansionReasons, "adaptation_target_lock_changed")
		}
	}
	adaptation.ExpansionReasons = sortedCompactStrings(adaptation.ExpansionReasons)
	impact.RequiresCoreCastConfirmation = adaptation.RequiresCoreCastReconfirmation
	impact.Adaptation = adaptation
	impact = signFoundationImpact(impact)
	return impact
}

func foundationDiffTouchesCharacterMapping(diff domain.FoundationDiff, contract domain.CoreCastContract) bool {
	coreCharacters := make(map[string]bool, len(contract.Members))
	for _, member := range contract.Members {
		coreCharacters[member.Character.ID] = true
	}
	for _, change := range diff.Changes {
		if change.EntityType == domain.FoundationEntityCharacter && coreCharacters[change.EntityID] {
			return true
		}
		if change.EntityType == domain.FoundationEntityRelationship && change.CoreCastAffected {
			return true
		}
	}
	return false
}

func adaptationFoundationRequiresPlanConfirmation(diff domain.FoundationDiff, contract domain.CoreCastContract) bool {
	if foundationDiffTouchesCharacterMapping(diff, contract) {
		return true
	}
	for _, change := range diff.Changes {
		if change.EntityType == domain.FoundationEntityPremise || change.HardRuleAffected || change.HighRisk {
			return true
		}
	}
	return false
}

func signFoundationImpact(impact domain.FoundationImpact) domain.FoundationImpact {
	impact.Signature = ""
	payload, _ := json.Marshal(impact)
	impact.Signature = domain.ContentSignature(payload)
	return impact
}

func sortedCompactStrings(values []string) []string {
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	values = slices.DeleteFunc(values, func(value string) bool { return value == "" })
	sort.Strings(values)
	return slices.Compact(values)
}
