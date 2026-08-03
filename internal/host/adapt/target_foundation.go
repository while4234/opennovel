package adapt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/tools"
)

const targetFoundationPromptMarker = "TARGET STORY FOUNDATION (confirmed canonical truth; never rename, delete, or replace confirmed cast):"

func adaptationPlannerSystemPrompt(deps Deps) string {
	base := strings.TrimSpace(deps.Prompts.Planner)
	if deps.Store == nil {
		return base
	}
	target, err := deps.Store.Foundation.Load()
	if err != nil {
		return base
	}
	payload, err := json.Marshal(struct {
		Premise       string                         `json:"premise"`
		Characters    []domain.Character             `json:"confirmed_target_characters"`
		Relationships []domain.CharacterRelationship `json:"target_planned_relationships"`
		WorldRules    []domain.WorldRule             `json:"target_world_rules"`
	}{target.Premise, target.Characters, target.Relationships, target.WorldRules})
	if err != nil {
		return base
	}
	return base + "\n\n" + targetFoundationPromptMarker + "\n" + string(payload) +
		"\nKeep every planning claim explicitly separated as SOURCE FACT, TARGET ADAPTATION DECISION, or NEW ORIGINAL SETTING. SourceFoundation is read-only evidence; when it conflicts with this target foundation, the confirmed target decision governs the target story while the source discrepancy remains visible."
}

type TargetFoundationOptions struct {
	Brief                    string
	Feedback                 string
	ExpectedWorkflowRevision int
}

// GenerateTargetFoundation creates only target-story state. SourceFoundation
// remains immutable evidence and is never passed to a source write API here.
func GenerateTargetFoundation(_ context.Context, deps Deps, opts TargetFoundationOptions) (*domain.AdaptationFoundationReview, error) {
	if deps.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	source, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil || source == nil {
		return nil, fmt.Errorf("load immutable source foundation: %w", err)
	}
	gate, err := deps.Store.CoreCast.LoadGateBinding()
	if err != nil || gate == nil {
		return nil, fmt.Errorf("load adaptation core cast gate: %w", err)
	}
	currentContract, err := deps.Store.CoreCast.Load()
	if err != nil || currentContract == nil {
		return nil, fmt.Errorf("load adaptation core cast contract: %w", err)
	}
	sourceCharacters := domain.ResolveSourceCharacters(*source)
	sourceMajor := targetFoundationDispositionSources(sourceCharacters, *currentContract)
	contract, err := deps.Store.CoreCast.RequireConfirmedGate(*gate, sourceCharacters, sourceMajor, nil)
	if err != nil {
		return nil, fmt.Errorf("confirmed adaptation core cast is required: %w", err)
	}
	current, err := deps.Store.Foundation.Load()
	if err != nil {
		return nil, err
	}
	candidate := domain.CloneStoryFoundation(current)
	characterCandidate, lifecycle, binding, workflowErr := tools.CurrentCharacterWorkflow(deps.Store)
	usesCharacterWorkflow := false
	switch {
	case workflowErr == nil && characterCandidate != nil && lifecycle != nil:
		if lifecycle.Mode != domain.CharacterCardProjectAdaptation ||
			lifecycle.AnalysisStatus != domain.CharacterCardAnalysisCandidateReady ||
			lifecycle.ReviewStatus != domain.CharacterCardReviewPassed ||
			lifecycle.ConfirmationStatus != domain.CharacterCardConfirmed ||
			lifecycle.Candidate != binding.Candidate ||
			lifecycle.ReviewedCandidate != binding.Candidate ||
			lifecycle.ReviewedInputDigest != binding.InputDigest {
			return nil, fmt.Errorf("current reviewed adaptation Character Agent candidate is required")
		}
		candidate.Characters = append([]domain.Character(nil), characterCandidate.Foundation.Characters...)
		candidate.Relationships = append([]domain.CharacterRelationship(nil), characterCandidate.Foundation.Relationships...)
		candidate.RelationshipsReviewed = characterCandidate.Foundation.RelationshipsReviewed
		usesCharacterWorkflow = true
	case characterCandidate != nil || lifecycle != nil:
		return nil, fmt.Errorf("adaptation Character Agent candidate is stale: %w", workflowErr)
	default:
		// Legacy CoreCast-only projects keep their already-published target
		// baseline. They are not silently collapsed on every regeneration.
		// When no target cast exists, CoreCast is projected once as a migration
		// seed and remains explicitly unreviewed until Character Agent runs.
		if len(candidate.Characters) == 0 {
			candidate.Characters = domain.ContractCharacters(contract)
			candidate.Relationships = append([]domain.CharacterRelationship(nil), contract.PlannedRelationships...)
		}
		if err := requireLegacyAdaptationCoverageSeed(deps, contract); err != nil {
			return nil, err
		}
	}
	if !usesCharacterWorkflow {
		candidate.RelationshipsReviewed = false
	}
	candidate.Premise = targetFoundationPremise(source.Premise, opts.Brief, opts.Feedback)
	candidate.WorldRules = targetFoundationWorldRules(source.WorldRules, opts.Feedback)
	review, err := deps.Store.SaveAdaptationTargetFoundationCandidate(candidate, opts.ExpectedWorkflowRevision, opts.Brief, opts.Feedback)
	if err != nil {
		return nil, err
	}
	if usesCharacterWorkflow {
		if err := rebindPublishedAdaptationCharacters(deps, characterCandidate, lifecycle); err != nil {
			return nil, fmt.Errorf("rebind confirmed adaptation characters: %w", err)
		}
	}
	return review, nil
}

func targetFoundationDispositionSources(
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

func rebindPublishedAdaptationCharacters(
	deps Deps,
	candidate *domain.CharacterCardCandidate,
	lifecycle *domain.CharacterCardLifecycle,
) error {
	canonical, binding, inputs, _, err := tools.CurrentCharacterCanonicalBinding(deps.Store)
	if err != nil {
		return err
	}
	binding, err = domain.CharacterCardBindingFromFoundation(canonical, inputs)
	if err != nil {
		return err
	}

	reboundCandidate := *candidate
	reboundCandidate.Foundation = canonical
	reboundCandidate.Base = binding
	savedCandidate, err := deps.Store.CharacterCards.SaveCandidateCAS(reboundCandidate, candidate.Revision)
	if err != nil {
		return err
	}

	reboundLifecycle := *lifecycle
	reboundLifecycle.Candidate = binding.Candidate
	reboundLifecycle.Inputs = binding.Inputs
	reboundLifecycle.InputDigest = binding.InputDigest
	reboundLifecycle.ReviewedCandidate = binding.Candidate
	reboundLifecycle.ReviewedInputDigest = binding.InputDigest
	if _, err := deps.Store.CharacterCards.SaveCAS(reboundLifecycle, lifecycle.Revision, binding); err != nil {
		return err
	}
	*candidate = savedCandidate
	return nil
}

func requireLegacyAdaptationCoverageSeed(deps Deps, contract domain.CoreCastContract) error {
	source, err := deps.Store.Adaptation.LoadSourceFoundation()
	if err != nil {
		return fmt.Errorf("load legacy adaptation source character evidence: %w", err)
	}
	reports, err := deps.Store.Adaptation.LoadCompleteSourceReports()
	if err != nil {
		return fmt.Errorf("load legacy adaptation source reports: %w", err)
	}
	dossier, err := deps.Store.Adaptation.LoadCoCreateDossier()
	if err != nil {
		return fmt.Errorf("load legacy adaptation dossier: %w", err)
	}
	index, err := domain.BuildAdaptationSourceCharacterIndex(source, reports, dossier, &contract)
	if err != nil {
		return fmt.Errorf("build legacy adaptation source character index: %w", err)
	}
	coverage, err := domain.EvaluateAdaptationCharacterCoverage(index, nil)
	if err != nil {
		return err
	}
	if coverage.DecisionRequired > len(contract.Members) {
		return fmt.Errorf(
			"legacy CoreCast-only target has %d decision-required source characters but only %d core members; run the shared Character Agent to map non-core characters",
			coverage.DecisionRequired, len(contract.Members),
		)
	}
	return nil
}

func targetFoundationPremise(sourcePremise, brief, feedback string) string {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		brief = "按已确认改编意图创作目标作品"
	}
	var out strings.Builder
	out.WriteString("# 目标改编作品\n\n")
	out.WriteString("## 目标改编决策\n\n")
	out.WriteString(brief)
	if feedback = strings.TrimSpace(feedback); feedback != "" {
		out.WriteString("\n\n审核修订决策：")
		out.WriteString(feedback)
	}
	if sourcePremise = strings.TrimSpace(sourcePremise); sourcePremise != "" {
		out.WriteString("\n\n## 原著事实依据（只读）\n\n")
		out.WriteString(sourcePremise)
	}
	return out.String()
}

func targetFoundationWorldRules(sourceRules []domain.WorldRule, feedback string) []domain.WorldRule {
	rules := make([]domain.WorldRule, 0, len(sourceRules)+1)
	for _, source := range sourceRules {
		rule := source
		rule.ID = ""
		rule.Category = "source-preserved"
		rule.Boundary = strings.TrimSpace(strings.Join([]string{
			"原著事实（只读）", strings.TrimSpace(source.Boundary), "目标改编决策：保留；后续修改必须另建目标规则",
		}, "；"))
		rules = append(rules, rule)
	}
	decision := "原著未明确的信息只能作为目标创作决策或不确定项，不能标记为原著事实"
	if feedback = strings.TrimSpace(feedback); feedback != "" {
		decision += "；本轮审核决策：" + feedback
	}
	rules = append(rules, domain.WorldRule{
		Category: "target-decision", Title: "来源与目标边界", Rule: decision,
		Boundary: "目标作品规则；不写回 SourceFoundation", Strength: domain.WorldRuleStrengthHard,
	})
	return rules
}
