package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	SimulationContractVersion = "simulation_contract.v2"

	SimulationModeNormal     = "normal"
	SimulationModeReinforced = "reinforced"

	SimulationRoleCoordinator = "coordinator"
	SimulationRoleArchitect   = "architect"
	SimulationRoleWriter      = "writer"
	SimulationRoleEditor      = "editor"

	SimulationContractActive   = "active"
	SimulationContractDegraded = "degraded"
	SimulationContractInactive = "inactive"
)

// SimulationContract is the project-owned source of truth for imitation
// guidance. It deliberately stores only stable feature IDs; feature prose and
// evidence remain owned by the portable profile.
type SimulationContract struct {
	Version             string                   `json:"version"`
	Revision            int64                    `json:"revision"`
	CreatedAt           string                   `json:"created_at"`
	UpdatedAt           string                   `json:"updated_at"`
	ContractDigest      string                   `json:"contract_digest"`
	ProfileDigest       string                   `json:"profile_digest"`
	RequestedMode       string                   `json:"requested_mode"`
	EffectiveMode       string                   `json:"effective_mode"`
	Status              string                   `json:"status"`
	Reasons             []string                 `json:"reasons,omitempty"`
	FoundationRevision  int64                    `json:"foundation_revision"`
	FoundationDigest    string                   `json:"foundation_digest,omitempty"`
	CreativeBriefDigest string                   `json:"creative_brief_digest,omitempty"`
	Views               []SimulationContractView `json:"views,omitempty"`
	Exclusions          []SimulationExclusion    `json:"exclusions,omitempty"`
}

type SimulationContractView struct {
	Role       string   `json:"role"`
	Phase      string   `json:"phase"`
	Must       []string `json:"must,omitempty"`
	Should     []string `json:"should,omitempty"`
	Avoid      []string `json:"avoid,omitempty"`
	ByteBudget int      `json:"byte_budget"`
}

type SimulationExclusion struct {
	FeatureID string `json:"feature_id"`
	Reason    string `json:"reason"`
}

type SimulationContractInput struct {
	Profile             *SimulationProfileV2
	RequestedMode       string
	FoundationRevision  int64
	FoundationDigest    string
	CreativeBriefDigest string
	PreviousRevision    int64
	Now                 time.Time
}

type SimulationPolicyBudget struct {
	Role       string
	Phase      string
	MaxItems   int
	ByteBudget int
	Dimensions []string
}

func CompileSimulationContract(input SimulationContractInput) (SimulationContract, error) {
	requested := normalizeSimulationContractMode(input.RequestedMode)
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	contract := SimulationContract{
		Version:             SimulationContractVersion,
		Revision:            input.PreviousRevision + 1,
		CreatedAt:           now.Format(time.RFC3339),
		UpdatedAt:           now.Format(time.RFC3339),
		RequestedMode:       requested,
		EffectiveMode:       requested,
		Status:              SimulationContractInactive,
		FoundationRevision:  input.FoundationRevision,
		FoundationDigest:    strings.TrimSpace(input.FoundationDigest),
		CreativeBriefDigest: strings.TrimSpace(input.CreativeBriefDigest),
	}
	if input.Profile == nil {
		contract.Reasons = []string{"profile_missing"}
		return finalizeSimulationContract(contract)
	}
	if err := ValidateSimulationProfileV2(input.Profile); err != nil {
		contract.Reasons = []string{"profile_invalid"}
		return finalizeSimulationContract(contract)
	}
	contract.ProfileDigest = input.Profile.ProfileDigest
	contract.Status, contract.Reasons = simulationEffectiveStatus(*input.Profile, requested)
	if contract.Status == SimulationContractInactive {
		return finalizeSimulationContract(contract)
	}

	features := append([]SimulationFeature(nil), input.Profile.Features...)
	sort.Slice(features, func(i, j int) bool {
		left, right := simulationFeatureRank(features[i]), simulationFeatureRank(features[j])
		if left != right {
			return left > right
		}
		return features[i].ID < features[j].ID
	})
	for _, budget := range SimulationModePolicy(requested) {
		view, exclusions := selectSimulationContractView(features, requested, budget)
		contract.Views = append(contract.Views, view)
		contract.Exclusions = append(contract.Exclusions, exclusions...)
	}
	sort.Slice(contract.Views, func(i, j int) bool {
		if contract.Views[i].Role != contract.Views[j].Role {
			return contract.Views[i].Role < contract.Views[j].Role
		}
		return contract.Views[i].Phase < contract.Views[j].Phase
	})
	sort.Slice(contract.Exclusions, func(i, j int) bool {
		if contract.Exclusions[i].FeatureID != contract.Exclusions[j].FeatureID {
			return contract.Exclusions[i].FeatureID < contract.Exclusions[j].FeatureID
		}
		return contract.Exclusions[i].Reason < contract.Exclusions[j].Reason
	})
	return finalizeSimulationContract(contract)
}

func SimulationModePolicy(mode string) []SimulationPolicyBudget {
	reinforced := normalizeSimulationContractMode(mode) == SimulationModeReinforced
	if reinforced {
		return []SimulationPolicyBudget{
			{Role: SimulationRoleArchitect, Phase: "planning", MaxItems: 12, ByteBudget: 7200, Dimensions: []string{"plot_design.", "hook_design.", "reader_engagement.", "pacing_density."}},
			{Role: SimulationRoleWriter, Phase: "chapter", MaxItems: 9, ByteBudget: 5600, Dimensions: []string{"style.", "lexicon.emotion_words", "lexicon.transition_words", "pacing_density."}},
			{Role: SimulationRoleEditor, Phase: "review", MaxItems: 12, ByteBudget: 6800, Dimensions: []string{"style.", "lexicon.emotion_words", "lexicon.transition_words", "plot_design.", "hook_design.", "reader_engagement.", "pacing_density."}},
		}
	}
	return []SimulationPolicyBudget{
		{Role: SimulationRoleArchitect, Phase: "planning", MaxItems: 4, ByteBudget: 2600, Dimensions: []string{"plot_design.", "hook_design.", "reader_engagement.", "pacing_density."}},
		{Role: SimulationRoleWriter, Phase: "chapter", MaxItems: 3, ByteBudget: 2000, Dimensions: []string{"style.", "lexicon.emotion_words", "lexicon.transition_words", "pacing_density."}},
		{Role: SimulationRoleEditor, Phase: "review", MaxItems: 4, ByteBudget: 2600, Dimensions: []string{"style.", "lexicon.emotion_words", "lexicon.transition_words", "plot_design.", "hook_design.", "reader_engagement.", "pacing_density."}},
	}
}

func ValidateSimulationContract(contract *SimulationContract) error {
	if contract == nil || contract.Version != SimulationContractVersion {
		return fmt.Errorf("unsupported simulation contract version")
	}
	if contract.Revision <= 0 {
		return fmt.Errorf("simulation contract revision must be positive")
	}
	if _, err := time.Parse(time.RFC3339, contract.CreatedAt); err != nil {
		return fmt.Errorf("simulation contract created_at is invalid")
	}
	if _, err := time.Parse(time.RFC3339, contract.UpdatedAt); err != nil {
		return fmt.Errorf("simulation contract updated_at is invalid")
	}
	if contract.RequestedMode != SimulationModeNormal && contract.RequestedMode != SimulationModeReinforced {
		return fmt.Errorf("simulation contract requested mode is invalid")
	}
	if contract.EffectiveMode != SimulationModeNormal && contract.EffectiveMode != SimulationModeReinforced {
		return fmt.Errorf("simulation contract effective mode is invalid")
	}
	switch contract.Status {
	case SimulationContractActive, SimulationContractDegraded, SimulationContractInactive:
	default:
		return fmt.Errorf("simulation contract status is invalid")
	}
	seenViews := map[string]struct{}{}
	for _, view := range contract.Views {
		key := view.Role + "\x00" + view.Phase
		if _, exists := seenViews[key]; exists {
			return fmt.Errorf("simulation contract views must be unique")
		}
		seenViews[key] = struct{}{}
		if view.ByteBudget <= 0 {
			return fmt.Errorf("simulation contract view byte budget is invalid")
		}
		seenFeatures := map[string]struct{}{}
		for _, ids := range [][]string{view.Must, view.Should, view.Avoid} {
			for _, id := range ids {
				if strings.TrimSpace(id) == "" {
					return fmt.Errorf("simulation contract feature id is empty")
				}
				if _, exists := seenFeatures[id]; exists {
					return fmt.Errorf("simulation contract feature appears more than once")
				}
				seenFeatures[id] = struct{}{}
			}
		}
	}
	expected, err := SimulationContractDigest(*contract)
	if err != nil {
		return err
	}
	if expected != contract.ContractDigest {
		return fmt.Errorf("simulation contract digest mismatch")
	}
	return nil
}

func SimulationContractDigest(contract SimulationContract) (string, error) {
	contract.ContractDigest = ""
	data, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func SimulationContractCurrent(contract *SimulationContract, profile *SimulationProfileV2, mode string, foundationRevision int64, foundationDigest, briefDigest string) (bool, string) {
	if contract == nil {
		return false, "contract_missing"
	}
	if err := ValidateSimulationContract(contract); err != nil {
		return false, "contract_invalid"
	}
	if profile == nil || contract.ProfileDigest != profile.ProfileDigest {
		return false, "profile_digest_changed"
	}
	if contract.RequestedMode != normalizeSimulationContractMode(mode) {
		return false, "mode_changed"
	}
	if contract.FoundationRevision != foundationRevision || contract.FoundationDigest != strings.TrimSpace(foundationDigest) {
		return false, "foundation_revision_changed"
	}
	if contract.CreativeBriefDigest != strings.TrimSpace(briefDigest) {
		return false, "creative_brief_changed"
	}
	return true, ""
}

func (c SimulationContract) View(role, phase string) *SimulationContractView {
	for i := range c.Views {
		if c.Views[i].Role == role && c.Views[i].Phase == phase {
			view := c.Views[i]
			return &view
		}
	}
	return nil
}

func finalizeSimulationContract(contract SimulationContract) (SimulationContract, error) {
	digest, err := SimulationContractDigest(contract)
	if err != nil {
		return SimulationContract{}, err
	}
	contract.ContractDigest = digest
	if err := ValidateSimulationContract(&contract); err != nil {
		return SimulationContract{}, err
	}
	return contract, nil
}

func simulationEffectiveStatus(profile SimulationProfileV2, mode string) (string, []string) {
	switch profile.Health.State {
	case "stale":
		return SimulationContractInactive, []string{"profile_stale"}
	case "unknown":
		return SimulationContractInactive, []string{"profile_health_unknown"}
	}
	if len(profile.Features) == 0 {
		return SimulationContractInactive, []string{"profile_has_no_features"}
	}
	if profile.Health.State == "legacy" || profile.Analysis.Legacy {
		return SimulationContractDegraded, []string{"legacy_evidence_unknown", "advisory_only"}
	}
	var reasons []string
	if profile.Health.State == "portable_only" || !profile.Capabilities.LocalEvidence {
		reasons = append(reasons, "portable_only")
	}
	if !profile.Capabilities.SafetyIndex {
		reasons = append(reasons, "safety_index_unavailable")
	}
	if mode == SimulationModeReinforced && !profile.Capabilities.AnalysisSigned {
		reasons = append(reasons, "analysis_signature_unavailable", "must_disabled")
	}
	if len(reasons) > 0 {
		return SimulationContractDegraded, reasons
	}
	return SimulationContractActive, nil
}

func selectSimulationContractView(features []SimulationFeature, mode string, budget SimulationPolicyBudget) (SimulationContractView, []SimulationExclusion) {
	view := SimulationContractView{Role: budget.Role, Phase: budget.Phase, ByteBudget: budget.ByteBudget}
	var exclusions []SimulationExclusion
	var guidance []SimulationFeature
	var avoids []SimulationFeature
	for _, feature := range features {
		reason := simulationFeatureIneligible(feature, budget)
		if reason != "" {
			if reason == "source_surface_feature" || reason == "viewpoint_owned_by_target_story" {
				exclusions = append(exclusions, SimulationExclusion{FeatureID: feature.ID, Reason: reason})
			}
			continue
		}
		if feature.Safety == "avoid" {
			avoids = append(avoids, feature)
			continue
		}
		guidance = append(guidance, feature)
	}

	avoidLimit := budget.MaxItems / 3
	if avoidLimit < 1 {
		avoidLimit = 1
	}
	avoidCount := min(len(avoids), avoidLimit)
	guidanceCount := min(len(guidance), budget.MaxItems-avoidCount)
	avoidCount = min(len(avoids), budget.MaxItems-guidanceCount)
	guidanceCount = min(len(guidance), budget.MaxItems-avoidCount)

	for _, feature := range guidance[:guidanceCount] {
		if mode == SimulationModeReinforced && simulationFeatureCanBeMust(feature) {
			view.Must = append(view.Must, feature.ID)
		} else {
			view.Should = append(view.Should, feature.ID)
		}
	}
	for _, feature := range avoids[:avoidCount] {
		view.Avoid = append(view.Avoid, feature.ID)
	}
	return view, exclusions
}

func simulationFeatureIneligible(feature SimulationFeature, budget SimulationPolicyBudget) string {
	if feature.Disabled || feature.Safety == "blocked" || feature.Classification == "outlier" || feature.Classification == "contradictory" {
		return "feature_unavailable"
	}
	if strings.HasPrefix(feature.Dimension, "lexicon.common_words") ||
		strings.HasPrefix(feature.Dimension, "lexicon.scene_words") ||
		strings.HasPrefix(feature.Dimension, "lexicon.signature_phrases") {
		return "source_surface_feature"
	}
	if budget.Role == SimulationRoleWriter &&
		(feature.Dimension == "style.narrative_voice" || feature.Dimension == "style.perspective") {
		return "viewpoint_owned_by_target_story"
	}
	if len(feature.Roles) > 0 && !containsSimulationString(feature.Roles, budget.Role) {
		return "role_mismatch"
	}
	if len(feature.Phases) > 0 && !containsSimulationString(feature.Phases, budget.Phase) {
		return "phase_mismatch"
	}
	if len(feature.Phases) > 0 {
		return ""
	}
	for _, prefix := range budget.Dimensions {
		if strings.HasPrefix(feature.Dimension, prefix) {
			return ""
		}
	}
	return "dimension_mismatch"
}

func simulationFeatureCanBeMust(feature SimulationFeature) bool {
	if feature.Classification != "stable" || feature.Confidence == nil || *feature.Confidence < 0.9 ||
		feature.Coverage == nil || *feature.Coverage < 0.5 || feature.SupportCount < 2 {
		return false
	}
	return strings.HasPrefix(feature.Dimension, "plot_design.") ||
		strings.HasPrefix(feature.Dimension, "hook_design.") ||
		strings.HasPrefix(feature.Dimension, "pacing_density.")
}

func simulationFeatureRank(feature SimulationFeature) int64 {
	var rank int64
	if feature.Safety == "avoid" {
		rank += 1_000_000_000
	}
	switch feature.Classification {
	case "stable":
		rank += 100_000_000
	case "local":
		rank += 10_000_000
	case "legacy_unknown":
		rank += 1_000_000
	}
	if feature.Confidence != nil {
		rank += int64(*feature.Confidence * 100_000)
	}
	if feature.Coverage != nil {
		rank += int64(*feature.Coverage * 10_000)
	}
	rank += int64(feature.SupportCount)
	return rank
}

func normalizeSimulationContractMode(mode string) string {
	if strings.TrimSpace(mode) == SimulationModeReinforced {
		return SimulationModeReinforced
	}
	return SimulationModeNormal
}

func containsSimulationString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
