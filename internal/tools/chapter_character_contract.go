package tools

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

func (t *PlanChapterTool) validateCharacterContracts(plan *domain.ChapterPlan) error {
	if plan == nil {
		return fmt.Errorf("chapter plan is required: %w", errs.ErrToolArgs)
	}
	outline, err := t.store.Outline.GetChapterOutline(plan.Chapter)
	if err != nil {
		// Legacy/adaptation fixtures may carry their chapter contract outside the
		// canonical outline store. Do not fabricate character constraints.
		if len(plan.Contract.Characters) == 0 {
			return nil
		}
		return fmt.Errorf("load chapter outline for character contract: %w", err)
	}
	requiredIDs := outlineCharacterIDs(*outline)
	if len(requiredIDs) == 0 {
		// Legacy name-only outlines remain writable without fabricated contracts.
		if len(plan.Contract.Characters) == 0 {
			return nil
		}
	}
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return fmt.Errorf("load confirmed StoryFoundation for character contract: %w", err)
	}
	canonical := make(map[string]domain.Character, len(foundation.Characters))
	for _, character := range foundation.Characters {
		canonical[character.ID] = character
	}
	provided := make(map[string]struct{}, len(plan.Contract.Characters))
	for index := range plan.Contract.Characters {
		contract := &plan.Contract.Characters[index]
		contract.CharacterID = strings.TrimSpace(contract.CharacterID)
		character, ok := canonical[contract.CharacterID]
		if !ok {
			return fmt.Errorf("character_contracts[%d] references unknown character_id %q: %w", index, contract.CharacterID, errs.ErrToolArgs)
		}
		if _, duplicate := provided[contract.CharacterID]; duplicate {
			return fmt.Errorf("duplicate character contract for %q: %w", contract.CharacterID, errs.ErrToolArgs)
		}
		provided[contract.CharacterID] = struct{}{}
		if strings.TrimSpace(contract.Goal) == "" ||
			strings.TrimSpace(contract.ImmediateMotivation) == "" ||
			strings.TrimSpace(contract.StartState) == "" ||
			len(nonEmptyStrings(contract.VoiceBehavior)) == 0 ||
			len(nonEmptyStrings(contract.MustPreserve)) == 0 {
			return fmt.Errorf("character contract %q requires goal, immediate_motivation, start_state, voice_behavior, and must_preserve: %w", contract.CharacterID, errs.ErrToolArgs)
		}
		if leak := firstKnowledgeLeak(*contract, character); leak != "" {
			return fmt.Errorf("character contract %q leaks knowledge %q; put it in may_learn and show acquisition on-page: %w", contract.CharacterID, leak, errs.ErrToolArgs)
		}
		contract.AllowedChanges = nonEmptyStrings(contract.AllowedChanges)
		contract.VoiceBehavior = nonEmptyStrings(contract.VoiceBehavior)
		contract.Known = nonEmptyStrings(contract.Known)
		contract.Unknown = nonEmptyStrings(contract.Unknown)
		contract.Misconceptions = nonEmptyStrings(contract.Misconceptions)
		contract.MayLearn = nonEmptyStrings(contract.MayLearn)
		contract.MustPreserve = nonEmptyStrings(contract.MustPreserve)
		contract.ForbiddenJumps = nonEmptyStrings(contract.ForbiddenJumps)
	}
	for _, id := range requiredIDs {
		if _, ok := provided[id]; !ok {
			return fmt.Errorf("character contract for outline character_id %q is required: %w", id, errs.ErrToolArgs)
		}
	}
	return nil
}

func firstKnowledgeLeak(contract domain.ChapterCharacterContract, character domain.Character) string {
	if character.KnowledgeBoundary == nil {
		return ""
	}
	forbidden := append([]string(nil), character.KnowledgeBoundary.Unknown...)
	forbidden = append(forbidden, character.KnowledgeBoundary.Forbidden...)
	for _, known := range contract.Known {
		for _, boundary := range forbidden {
			if sameContractFact(known, boundary) && !containsContractFact(contract.MayLearn, known) {
				return known
			}
		}
	}
	return ""
}

func sameContractFact(left, right string) bool {
	left = strings.ToLower(strings.TrimSpace(left))
	right = strings.ToLower(strings.TrimSpace(right))
	return left != "" && left == right
}

func containsContractFact(items []string, target string) bool {
	for _, item := range items {
		if sameContractFact(item, target) {
			return true
		}
	}
	return false
}

func nonEmptyStrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
