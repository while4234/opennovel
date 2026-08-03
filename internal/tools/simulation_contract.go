package tools

import (
	"encoding/json"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type simulationRoleContext struct {
	EffectiveMode  string                     `json:"effective_mode"`
	Status         string                     `json:"status"`
	Reasons        []string                   `json:"reasons,omitempty"`
	Contract       simulationContractIdentity `json:"contract"`
	Role           string                     `json:"role"`
	Phase          string                     `json:"phase"`
	Must           []simulationContextFeature `json:"must,omitempty"`
	Should         []simulationContextFeature `json:"should,omitempty"`
	Avoid          []simulationContextFeature `json:"avoid,omitempty"`
	SelectedCount  int                        `json:"selected_count"`
	ByteBudget     int                        `json:"byte_budget,omitempty"`
	SafetyBoundary string                     `json:"safety_boundary"`
}

type simulationContractIdentity struct {
	Revision      int64  `json:"revision,omitempty"`
	Digest        string `json:"digest,omitempty"`
	ProfileDigest string `json:"profile_digest,omitempty"`
}

type simulationContextFeature struct {
	ID         string   `json:"id"`
	Dimension  string   `json:"dimension"`
	Statement  string   `json:"statement"`
	Confidence *float64 `json:"confidence,omitempty"`
}

func ensureSimulationContract(st *store.Store, mode string) (*domain.SimulationContract, *domain.SimulationProfileV2, error) {
	if st == nil {
		return nil, nil, nil
	}
	return st.EnsureSimulationContract(mode)
}

func buildSimulationRoleContext(contract *domain.SimulationContract, profile *domain.SimulationProfileV2, role, phase string) simulationRoleContext {
	context := simulationRoleContext{
		EffectiveMode:  domain.SimulationModeNormal,
		Status:         domain.SimulationContractInactive,
		Reasons:        []string{"contract_missing"},
		Role:           role,
		Phase:          phase,
		SafetyBoundary: "portable_features_only; no raw source, source reports, local paths, names index, or signature phrases",
	}
	if contract == nil {
		return context
	}
	context.EffectiveMode = contract.EffectiveMode
	context.Status = contract.Status
	context.Reasons = append([]string(nil), contract.Reasons...)
	context.Contract = simulationContractIdentity{
		Revision: contract.Revision, Digest: contract.ContractDigest, ProfileDigest: contract.ProfileDigest,
	}
	if profile == nil || contract.Status == domain.SimulationContractInactive {
		return context
	}
	view := contract.View(role, phase)
	if view == nil {
		context.Reasons = appendUniqueSimulationReason(context.Reasons, "role_view_unavailable")
		return context
	}
	features := make(map[string]domain.SimulationFeature, len(profile.Features))
	for _, feature := range profile.Features {
		features[feature.ID] = feature
	}
	context.Must = resolveSimulationFeatures(view.Must, features)
	context.Should = resolveSimulationFeatures(view.Should, features)
	context.Avoid = resolveSimulationFeatures(view.Avoid, features)
	context.SelectedCount = len(context.Must) + len(context.Should) + len(context.Avoid)
	context.ByteBudget = view.ByteBudget
	trimSimulationRoleContextToBudget(&context)
	return context
}

func resolveSimulationFeatures(ids []string, features map[string]domain.SimulationFeature) []simulationContextFeature {
	resolved := make([]simulationContextFeature, 0, len(ids))
	for _, id := range ids {
		feature, ok := features[id]
		if !ok || strings.HasPrefix(feature.Dimension, "lexicon.signature_phrases") ||
			strings.HasPrefix(feature.Dimension, "lexicon.common_words") ||
			strings.HasPrefix(feature.Dimension, "lexicon.scene_words") {
			continue
		}
		resolved = append(resolved, simulationContextFeature{
			ID: feature.ID, Dimension: feature.Dimension, Statement: feature.Statement,
			Confidence: feature.Confidence,
		})
	}
	return resolved
}

func appendUniqueSimulationReason(reasons []string, reason string) []string {
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func trimSimulationRoleContextToBudget(context *simulationRoleContext) {
	if context == nil || context.ByteBudget <= 0 {
		return
	}
	size := func() int {
		data, _ := json.Marshal(struct {
			Must   []simulationContextFeature `json:"must,omitempty"`
			Should []simulationContextFeature `json:"should,omitempty"`
			Avoid  []simulationContextFeature `json:"avoid,omitempty"`
		}{context.Must, context.Should, context.Avoid})
		return len(data)
	}
	trimmed := false
	for size() > context.ByteBudget {
		switch {
		case len(context.Should) > 0:
			context.Should = context.Should[:len(context.Should)-1]
		case len(context.Must) > 0:
			context.Must = context.Must[:len(context.Must)-1]
		case len(context.Avoid) > 0:
			context.Avoid = context.Avoid[:len(context.Avoid)-1]
		default:
			return
		}
		trimmed = true
	}
	context.SelectedCount = len(context.Must) + len(context.Should) + len(context.Avoid)
	if trimmed {
		context.Reasons = appendUniqueSimulationReason(context.Reasons, "context_budget_trimmed")
	}
}

func (t *ContextTool) buildRoleBoundSimulationContext(
	result map[string]any,
	scope string,
	chapterPurpose chapterContextPurpose,
	warn func(string, error),
) {
	role, phase := t.simulationViewForScope(scope)
	if role == "" {
		return
	}
	if t.store.Adaptation.Active() {
		context := simulationRoleContext{
			EffectiveMode: domain.SimulationModeNormal,
			Status:        domain.SimulationContractInactive,
			Reasons:       []string{"adaptation_contract_has_priority"},
			Role:          role, Phase: phase,
			SafetyBoundary: "adaptation context is isolated from simulation guidance",
		}
		result["simulation_effective"] = context
		return
	}
	contract, profile, err := ensureSimulationContract(t.store, t.simulationMode)
	if err != nil {
		warn("simulation_contract", err)
		result["simulation_effective"] = simulationRoleContext{
			EffectiveMode: domain.SimulationModeNormal,
			Status:        domain.SimulationContractInactive,
			Reasons:       []string{"profile_or_contract_invalid"},
			Role:          role, Phase: phase,
			SafetyBoundary: "no simulation guidance is used when the contract cannot be validated",
		}
		return
	}
	context := buildSimulationRoleContext(contract, profile, role, phase)
	if t.simulationMode == domain.SimulationModeNormal && (contract == nil || contract.ProfileDigest == "") {
		// Preserve the historical no-profile default path byte-for-byte. An
		// explicit reinforced request still receives truthful inactive status.
		return
	}
	if scope == "chapter" && chapterPurpose == chapterContextRecovering {
		context.Must = nil
		context.Should = nil
		context.Avoid = nil
		context.SelectedCount = 0
		context.Reasons = appendUniqueSimulationReason(context.Reasons, "recovery_context_omits_drafting_guidance")
	}
	result["simulation_effective"] = context
	if context.Status == domain.SimulationContractInactive || context.SelectedCount == 0 {
		return
	}
	switch role {
	case domain.SimulationRoleArchitect, domain.SimulationRoleEditor:
		if scope != "chapter" {
			section, _ := result["planning_memory"].(map[string]any)
			if section == nil {
				section = map[string]any{}
				result["planning_memory"] = section
			}
			section["simulation_contract"] = context
		} else {
			attachSimulationChapterContract(result, context)
		}
	case domain.SimulationRoleWriter:
		attachSimulationChapterContract(result, context)
	}
	// Deprecated compatibility fields are generated from the canonical status,
	// never interpreted independently by prompts.
	result["simulation_profile"] = true
	result["simulation_mode"] = context.EffectiveMode
	result["simulation_compatibility"] = map[string]any{
		"source":       "simulation_effective",
		"remove_after": SimulationContractCompatibilityRemoval,
	}
}

const SimulationContractCompatibilityRemoval = "simulation_contract.v2"

func attachSimulationChapterContract(result map[string]any, context simulationRoleContext) {
	working, _ := result["working_memory"].(map[string]any)
	if working == nil {
		working = map[string]any{}
		result["working_memory"] = working
	}
	working["simulation_contract"] = context
}

func (t *ContextTool) simulationViewForScope(scope string) (string, string) {
	if t.simulationRole != "" {
		switch t.simulationRole {
		case domain.SimulationRoleCoordinator:
			return domain.SimulationRoleCoordinator, "status"
		case domain.SimulationRoleArchitect:
			return domain.SimulationRoleArchitect, "planning"
		case domain.SimulationRoleWriter:
			return domain.SimulationRoleWriter, "chapter"
		case domain.SimulationRoleEditor:
			return domain.SimulationRoleEditor, "review"
		}
	}
	switch scope {
	case "status":
		return domain.SimulationRoleCoordinator, "status"
	case "planning_review", "planning_audit":
		return domain.SimulationRoleEditor, "review"
	case "chapter":
		return domain.SimulationRoleWriter, "chapter"
	case "planning", "planning_detail":
		return domain.SimulationRoleArchitect, "planning"
	default:
		return "", ""
	}
}
