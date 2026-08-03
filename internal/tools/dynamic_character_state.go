package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

func (t *CommitChapterTool) validateChapterCommitCharacterIDs(chapter int, submitted []string) error {
	outline, err := t.store.Outline.GetChapterOutline(chapter)
	if err != nil || outline == nil || len(outline.CharacterIDs) == 0 {
		return nil
	}
	expected := append([]string(nil), outline.CharacterIDs...)
	actual := make([]string, 0, len(submitted))
	for _, id := range submitted {
		if id = strings.TrimSpace(id); id != "" {
			actual = append(actual, id)
		}
	}
	slices.Sort(expected)
	expected = slices.Compact(expected)
	slices.Sort(actual)
	actual = slices.Compact(actual)
	if slices.Equal(actual, expected) {
		return nil
	}
	return fmt.Errorf(
		"chapter %d character_ids do not match the authoritative chapter contract; got=%v want=%v. Discard the current summary and all remembered commit metadata, then copy character_ids and characters from the same-draft check_de_ai.commit_context: %w",
		chapter, actual, expected, errs.ErrToolPrecondition,
	)
}

var runtimeCharacterFieldNames = []string{
	"status", "location", "resources", "power",
	"short_term_motivation", "motivation", "knowledge",
	"emotion", "injury", "relation", "relationship",
}

var runtimeCharacterFields = func() map[string]struct{} {
	fields := make(map[string]struct{}, len(runtimeCharacterFieldNames))
	for _, field := range runtimeCharacterFieldNames {
		fields[field] = struct{}{}
	}
	return fields
}()

func (t *CommitChapterTool) validateDynamicCharacterUpdates(
	characterIDs []string,
	relationships []domain.RelationshipEntry,
	stateChanges []domain.StateChange,
) error {
	foundation, err := t.store.Foundation.Load()
	if err != nil {
		return fmt.Errorf("load StoryFoundation for dynamic character update: %w", err)
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
	for _, id := range characterIDs {
		if _, ok := byID[strings.TrimSpace(id)]; !ok {
			return fmt.Errorf("character_ids contains unknown StoryFoundation ID %q: %w", id, errs.ErrToolArgs)
		}
	}
	for index := range relationships {
		if err := normalizeRelationshipEndpoint(&relationships[index], byID, byName); err != nil {
			return fmt.Errorf("relationship_changes[%d]: %w", index, err)
		}
	}
	for index := range stateChanges {
		change := &stateChanges[index]
		if change.CharacterID == "" {
			if character, ok := byName[strings.TrimSpace(change.Entity)]; ok {
				change.CharacterID = character.ID
			}
		}
		if change.CharacterID == "" {
			continue // Non-character entity state remains compatible.
		}
		character, ok := byID[strings.TrimSpace(change.CharacterID)]
		if !ok {
			return fmt.Errorf("state_changes[%d] has unknown character_id %q: %w", index, change.CharacterID, errs.ErrToolArgs)
		}
		if strings.TrimSpace(change.Entity) != "" {
			if named, ok := byName[strings.TrimSpace(change.Entity)]; ok && named.ID != character.ID {
				return fmt.Errorf("state_changes[%d] character_id/name conflict: %w", index, errs.ErrToolArgs)
			}
		} else {
			change.Entity = character.Name
		}
		field := strings.ToLower(strings.TrimSpace(change.Field))
		if isStaticCharacterField(field) {
			return fmt.Errorf("state_changes[%d] attempts static field %q; create a Character Agent revision request instead: %w", index, change.Field, errs.ErrToolPrecondition)
		}
		if _, ok := runtimeCharacterFields[field]; !ok {
			return fmt.Errorf(
				"state_changes[%d] field %q is not an allowed dynamic character field; allowed=%v. Do not retry with a synonym: use one exact value from check_de_ai.commit_context.allowed_character_state_fields, or omit this state change: %w",
				index, change.Field, runtimeCharacterFieldNames, errs.ErrToolArgs,
			)
		}
	}
	return nil
}

func normalizeRelationshipEndpoint(
	entry *domain.RelationshipEntry,
	byID map[string]domain.Character,
	byName map[string]domain.Character,
) error {
	if entry == nil {
		return nil
	}
	source, sourceOK, err := resolveCharacterEndpoint(entry.SourceCharacterID, entry.CharacterA, byID, byName)
	if err != nil {
		return fmt.Errorf("source endpoint: %w", err)
	}
	target, targetOK, err := resolveCharacterEndpoint(entry.TargetCharacterID, entry.CharacterB, byID, byName)
	if err != nil {
		return fmt.Errorf("target endpoint: %w", err)
	}
	if sourceOK {
		entry.SourceCharacterID = source.ID
		if strings.TrimSpace(entry.CharacterA) == "" {
			entry.CharacterA = source.Name
		}
	}
	if targetOK {
		entry.TargetCharacterID = target.ID
		if strings.TrimSpace(entry.CharacterB) == "" {
			entry.CharacterB = target.Name
		}
	}
	return nil
}

func resolveCharacterEndpoint(
	id, name string,
	byID map[string]domain.Character,
	byName map[string]domain.Character,
) (domain.Character, bool, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		character, ok := byName[name]
		return character, ok, nil
	}
	character, ok := byID[id]
	if !ok {
		return domain.Character{}, false, fmt.Errorf("unknown character_id %q: %w", id, errs.ErrToolArgs)
	}
	if name != "" {
		named, namedOK := byName[name]
		if namedOK && named.ID != id {
			return domain.Character{}, false, fmt.Errorf("character_id %q conflicts with name %q: %w", id, name, errs.ErrToolArgs)
		}
	}
	return character, true, nil
}
