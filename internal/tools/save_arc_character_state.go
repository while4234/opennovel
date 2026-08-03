package tools

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

func (t *SaveArcSummaryTool) normalizeSnapshotIDs(
	snapshots []domain.CharacterSnapshot,
	styleRules *arcSummaryStyleRules,
) error {
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return fmt.Errorf("load StoryFoundation for character snapshots: %w", err)
	}
	byID := make(map[string]domain.Character, len(foundation.Characters))
	byName := make(map[string]domain.Character, len(foundation.Characters)*2)
	for _, character := range foundation.Characters {
		byID[character.ID] = character
		byName[character.Name] = character
		for _, alias := range character.Aliases {
			byName[alias] = character
		}
	}
	if len(byID) == 0 {
		// Old projects may have name-only snapshots before StoryFoundation
		// acquired canonical character cards.
		return nil
	}
	for index := range snapshots {
		character, ok, resolveErr := resolveCharacterEndpoint(snapshots[index].CharacterID, snapshots[index].Name, byID, byName)
		if resolveErr != nil {
			return fmt.Errorf("character_snapshots[%d]: %w", index, resolveErr)
		}
		if !ok {
			return fmt.Errorf("character_snapshots[%d] cannot resolve character_id or legacy name: %w", index, errs.ErrToolArgs)
		}
		snapshots[index].CharacterID = character.ID
		if strings.TrimSpace(snapshots[index].Name) == "" {
			snapshots[index].Name = character.Name
		}
	}
	if styleRules == nil {
		return nil
	}
	for index := range styleRules.Dialogue {
		character, ok, resolveErr := resolveCharacterEndpoint(styleRules.Dialogue[index].CharacterID, styleRules.Dialogue[index].Name, byID, byName)
		if resolveErr != nil {
			return fmt.Errorf("style_rules.dialogue[%d]: %w", index, resolveErr)
		}
		if !ok {
			return fmt.Errorf("style_rules.dialogue[%d] cannot resolve character_id or legacy name: %w", index, errs.ErrToolArgs)
		}
		styleRules.Dialogue[index].CharacterID = character.ID
		if strings.TrimSpace(styleRules.Dialogue[index].Name) == "" {
			styleRules.Dialogue[index].Name = character.Name
		}
	}
	return nil
}
