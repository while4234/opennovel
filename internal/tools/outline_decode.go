package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// decodeOutlineEntries accepts the stable save_foundation contract as well as
// common model-produced wrappers and structured scene objects. Structured
// scenes are flattened to deterministic, readable strings without discarding
// any supplied field, because the durable OutlineEntry schema intentionally
// keeps scenes as prose-ready []string.
func decodeOutlineEntries(typeName, content string) ([]domain.OutlineEntry, error) {
	rawEntries, err := unwrapOutlineEntries([]byte(content))
	if err != nil {
		return nil, decodeFoundationJSON(typeName, content, &[]domain.OutlineEntry{})
	}
	entries := make([]domain.OutlineEntry, 0, len(rawEntries))
	for index, raw := range rawEntries {
		entry, decodeErr := decodeOutlineEntry(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("parse %s JSON entry %d: %w: %w", typeName, index+1, decodeErr, errs.ErrToolArgs)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func unwrapOutlineEntries(raw []byte) ([]json.RawMessage, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err == nil {
		return entries, nil
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	for _, key := range []string{"chapters", "outline", "entries"} {
		value, ok := wrapper[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(value, &entries); err != nil {
			return nil, err
		}
		return entries, nil
	}
	return nil, fmt.Errorf("expected an array or an object containing chapters")
}

func decodeOutlineEntry(raw json.RawMessage) (domain.OutlineEntry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.OutlineEntry{}, err
	}
	sceneRaw := fields["scenes"]
	delete(fields, "scenes")
	temporaryRolesRaw := fields["temporary_roles"]
	delete(fields, "temporary_roles")
	if err := normalizeOutlineBeatAliases(
		fields,
		"character_beats",
		[]string{"character_id", "scene", "goal", "obstacle", "choice_cost", "advance"},
		map[string][]string{
			"advance": {"state_advance", "progress", "state_change"},
		},
	); err != nil {
		return domain.OutlineEntry{}, fmt.Errorf("character_beats: %w", err)
	}
	if err := normalizeOutlineBeatAliases(
		fields,
		"relationship_beats",
		[]string{
			"relationship_id", "source_character_id", "target_character_id",
			"scene", "start", "expected_advance", "forbidden_jump",
		},
		map[string][]string{
			"start":            {"initial_state", "before"},
			"expected_advance": {"progress", "advance", "state_advance", "relationship_change"},
			"forbidden_jump":   {"boundary", "forbidden", "must_not"},
		},
	); err != nil {
		return domain.OutlineEntry{}, fmt.Errorf("relationship_beats: %w", err)
	}
	withoutScenes, err := json.Marshal(fields)
	if err != nil {
		return domain.OutlineEntry{}, err
	}
	var entry domain.OutlineEntry
	if err := json.Unmarshal(withoutScenes, &entry); err != nil {
		return domain.OutlineEntry{}, err
	}
	if len(sceneRaw) > 0 && !bytes.Equal(bytes.TrimSpace(sceneRaw), []byte("null")) {
		entry.Scenes, err = decodeOutlineScenes(sceneRaw)
		if err != nil {
			return domain.OutlineEntry{}, fmt.Errorf("scenes: %w", err)
		}
	}
	if len(temporaryRolesRaw) > 0 && !bytes.Equal(bytes.TrimSpace(temporaryRolesRaw), []byte("null")) {
		entry.TemporaryRoles, err = decodeTemporaryRoles(temporaryRolesRaw)
		if err != nil {
			return domain.OutlineEntry{}, fmt.Errorf("temporary_roles: %w", err)
		}
	}
	return entry, nil
}

func normalizeOutlineBeatAliases(
	fields map[string]json.RawMessage,
	field string,
	tupleFields []string,
	aliases map[string][]string,
) error {
	raw, ok := fields[field]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	beats, err := decodeOutlineBeatObjects(raw, tupleFields)
	if err != nil {
		return err
	}
	for _, beat := range beats {
		for canonical, alternatives := range aliases {
			if value, exists := beat[canonical]; exists && !isEmptyJSONText(value) {
				continue
			}
			for _, alternative := range alternatives {
				value, exists := beat[alternative]
				if !exists || isEmptyJSONText(value) {
					continue
				}
				beat[canonical] = value
				break
			}
		}
	}
	normalized, err := json.Marshal(beats)
	if err != nil {
		return err
	}
	fields[field] = normalized
	return nil
}

func decodeOutlineBeatObjects(
	raw json.RawMessage,
	tupleFields []string,
) ([]map[string]json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	beats := make([]map[string]json.RawMessage, 0)
	var collect func(any) error
	collect = func(current any) error {
		switch typed := current.(type) {
		case []any:
			if outlineBeatTuple(typed) {
				if len(typed) > len(tupleFields) {
					return fmt.Errorf(
						"beat tuple has %d fields, expected at most %d",
						len(typed),
						len(tupleFields),
					)
				}
				beat := make(map[string]json.RawMessage, len(typed))
				for index, item := range typed {
					encoded, err := json.Marshal(item)
					if err != nil {
						return err
					}
					if !isEmptyJSONText(encoded) {
						beat[tupleFields[index]] = encoded
					}
				}
				beats = append(beats, beat)
				return nil
			}
			for _, item := range typed {
				if err := collect(item); err != nil {
					return err
				}
			}
			return nil
		case map[string]any:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return err
			}
			var beat map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &beat); err != nil {
				return err
			}
			beats = append(beats, beat)
			return nil
		default:
			return fmt.Errorf("expected beat object or nested array, got %T", current)
		}
	}
	if err := collect(value); err != nil {
		return nil, err
	}
	return beats, nil
}

func outlineBeatTuple(items []any) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		switch item.(type) {
		case []any, map[string]any:
			return false
		}
	}
	return true
}

func isEmptyJSONText(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var text string
	return json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) == ""
}

func decodeTemporaryRoles(raw json.RawMessage) ([]domain.TemporaryCharacterNeed, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	roles := make([]domain.TemporaryCharacterNeed, 0)
	var collect func(any, bool) error
	collect = func(current any, nested bool) error {
		switch typed := current.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				roles = append(roles, domain.TemporaryCharacterNeed{Role: typed})
			}
			return nil
		case map[string]any:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return err
			}
			var need domain.TemporaryCharacterNeed
			if err := json.Unmarshal(encoded, &need); err != nil {
				return err
			}
			if strings.TrimSpace(need.Role) != "" {
				roles = append(roles, need)
			}
			return nil
		case []any:
			if nested && temporaryRoleTuple(typed) {
				need, err := decodeTemporaryRoleTuple(typed)
				if err != nil {
					return err
				}
				if strings.TrimSpace(need.Role) != "" {
					roles = append(roles, need)
				}
				return nil
			}
			for _, item := range typed {
				if err := collect(item, true); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("expected temporary role string, object or tuple, got %T", current)
		}
	}
	if err := collect(value, false); err != nil {
		return nil, err
	}
	return roles, nil
}

func temporaryRoleTuple(items []any) bool {
	if len(items) == 0 || len(items) > 4 {
		return false
	}
	for _, item := range items {
		switch item.(type) {
		case []any, map[string]any:
			return false
		}
	}
	return true
}

func decodeTemporaryRoleTuple(items []any) (domain.TemporaryCharacterNeed, error) {
	fields := []string{"role", "scene", "purpose", "important"}
	values := make(map[string]any, len(items))
	for index, item := range items {
		values[fields[index]] = item
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return domain.TemporaryCharacterNeed{}, err
	}
	var need domain.TemporaryCharacterNeed
	if err := json.Unmarshal(encoded, &need); err != nil {
		return domain.TemporaryCharacterNeed{}, err
	}
	return need, nil
}

func decodeOutlineScenes(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, nil
		}
		return []string{single}, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	scenes := make([]string, 0, len(values))
	for _, value := range values {
		scene, err := stringifyOutlineScene(value)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(scene) != "" {
			scenes = append(scenes, scene)
		}
	}
	return scenes, nil
}

func stringifyOutlineScene(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		var value any
		if valueErr := json.Unmarshal(raw, &value); valueErr != nil {
			return "", valueErr
		}
		compact, compactErr := json.Marshal(value)
		return string(compact), compactErr
	}

	preferred := []string{
		"title", "name", "scene", "location", "time", "characters", "goal",
		"purpose", "conflict", "action", "turn", "choice", "cost", "outcome",
		"hook", "detail", "description",
	}
	rank := make(map[string]int, len(preferred))
	for index, key := range preferred {
		rank[key] = index
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, leftKnown := rank[keys[i]]
		right, rightKnown := rank[keys[j]]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value, err := readableSceneValue(object[key])
		if err != nil {
			return "", err
		}
		if value != "" {
			parts = append(parts, key+": "+value)
		}
	}
	return strings.Join(parts, "；"), nil
}

func readableSceneValue(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(compact), nil
}
