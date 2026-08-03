package imp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

var importCharacterFields = map[string]struct{}{
	"id": {}, "name": {}, "aliases": {}, "role": {}, "description": {},
	"gender": {}, "arc": {}, "traits": {}, "tier": {}, "faction": {}, "goal": {},
	"motivation": {}, "conflict": {}, "voice": {}, "constraints": {},
	"contrast_details": {}, "key_backstory": {}, "initial_state": {},
	"knowledge_boundary": {}, "notes": {},
	// Legacy merge prompts emitted these fields even though domain.Character
	// could not consume them. They are migrated below rather than discarded.
	"goals": {}, "relationships": {},
}

func decodeCharactersJSON(label, body string, out *[]domain.Character, optional bool) error {
	body = stripFences(body)
	if strings.TrimSpace(body) == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%s array is empty", label)
	}
	segment, err := extractJSONSegment(body)
	if err != nil {
		return fmt.Errorf("extract %s JSON: %w", label, err)
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(segment), &objects); err != nil {
		return fmt.Errorf("parse %s JSON: %w", label, err)
	}
	characters := make([]domain.Character, 0, len(objects))
	for index, object := range objects {
		for key := range object {
			if _, supported := importCharacterFields[key]; !supported {
				return fmt.Errorf("%s[%d] contains unsupported field %q", label, index, key)
			}
		}
		if err := normalizeCharacterCompatibilityFields(object); err != nil {
			return fmt.Errorf("normalize %s[%d]: %w", label, index, err)
		}
		encoded, marshalErr := json.Marshal(object)
		if marshalErr != nil {
			return fmt.Errorf("encode %s[%d]: %w", label, index, marshalErr)
		}
		var character domain.Character
		if unmarshalErr := json.Unmarshal(encoded, &character); unmarshalErr != nil {
			return fmt.Errorf("parse %s[%d]: %w", label, index, unmarshalErr)
		}
		var legacy struct {
			Goals         stringListCompatibility `json:"goals"`
			Relationships stringListCompatibility `json:"relationships"`
		}
		if unmarshalErr := json.Unmarshal(encoded, &legacy); unmarshalErr != nil {
			return fmt.Errorf("parse legacy %s[%d]: %w", label, index, unmarshalErr)
		}
		if strings.TrimSpace(character.Goal) == "" && len(legacy.Goals) > 0 {
			character.Goal = strings.Join(legacy.Goals, "；")
		}
		if len(legacy.Relationships) > 0 {
			relationshipNote := "来源关系：" + strings.Join(legacy.Relationships, "；")
			if strings.TrimSpace(character.Notes) == "" {
				character.Notes = relationshipNote
			} else {
				character.Notes = strings.TrimSpace(character.Notes) + "；" + relationshipNote
			}
		}
		characters = append(characters, character)
	}
	if len(characters) == 0 && !optional {
		return fmt.Errorf("%s array is empty", label)
	}
	*out = characters
	return nil
}

func normalizeCharacterCompatibilityFields(object map[string]json.RawMessage) error {
	for _, field := range []string{"aliases", "traits", "constraints"} {
		raw, exists := object[field]
		if !exists {
			continue
		}
		var values stringListCompatibility
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		encoded, err := json.Marshal([]string(values))
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		object[field] = encoded
	}
	if err := normalizeContrastCompatibilityField(object); err != nil {
		return err
	}
	if err := normalizeBackstoryCompatibilityField(object); err != nil {
		return err
	}
	if err := normalizeInitialStateCompatibilityField(object); err != nil {
		return err
	}
	if err := normalizeKnowledgeBoundaryCompatibilityField(object); err != nil {
		return err
	}
	return nil
}

func normalizeContrastCompatibilityField(object map[string]json.RawMessage) error {
	raw, exists := object["contrast_details"]
	if !exists || string(raw) == "null" {
		return nil
	}
	var structured []domain.CharacterContrastDetail
	if err := json.Unmarshal(raw, &structured); err == nil {
		return nil
	}
	var values stringListCompatibility
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("contrast_details: want object array, string array, or string")
	}
	structured = make([]domain.CharacterContrastDetail, 0, len(values))
	for _, value := range values {
		surface, depth := splitContrastCompatibility(value)
		if surface != "" || depth != "" {
			structured = append(structured, domain.CharacterContrastDetail{Surface: surface, Depth: depth})
		}
	}
	encoded, err := json.Marshal(structured)
	if err != nil {
		return fmt.Errorf("contrast_details: %w", err)
	}
	object["contrast_details"] = encoded
	return nil
}

func splitContrastCompatibility(value string) (string, string) {
	value = strings.TrimSpace(value)
	for _, separator := range []string{"=>", "->", "→", "｜", "|", "；", ";"} {
		if parts := strings.SplitN(value, separator, 2); len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
	}
	return value, ""
}

func normalizeBackstoryCompatibilityField(object map[string]json.RawMessage) error {
	raw, exists := object["key_backstory"]
	if !exists || string(raw) == "null" {
		return nil
	}
	var structured []domain.CharacterBackstory
	if err := json.Unmarshal(raw, &structured); err == nil {
		return nil
	}
	var values stringListCompatibility
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("key_backstory: want object array, string array, or string")
	}
	structured = make([]domain.CharacterBackstory, 0, len(values))
	for _, value := range values {
		event, impact := splitContrastCompatibility(value)
		if event != "" || impact != "" {
			structured = append(structured, domain.CharacterBackstory{Event: event, Impact: impact})
		}
	}
	encoded, err := json.Marshal(structured)
	if err != nil {
		return fmt.Errorf("key_backstory: %w", err)
	}
	object["key_backstory"] = encoded
	return nil
}

func normalizeInitialStateCompatibilityField(object map[string]json.RawMessage) error {
	raw, exists := object["initial_state"]
	if !exists || string(raw) == "null" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err == nil {
		if resources, ok := fields["resources"]; ok {
			normalized, err := normalizeStringListRaw(resources)
			if err != nil {
				return fmt.Errorf("initial_state.resources: %w", err)
			}
			fields["resources"] = normalized
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("initial_state: %w", err)
		}
		object["initial_state"] = encoded
		return nil
	}
	var values stringListCompatibility
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("initial_state: want object, string array, or string")
	}
	encoded, err := json.Marshal(domain.CharacterInitialState{Situation: strings.Join([]string(values), "；")})
	if err != nil {
		return fmt.Errorf("initial_state: %w", err)
	}
	object["initial_state"] = encoded
	return nil
}

func normalizeKnowledgeBoundaryCompatibilityField(object map[string]json.RawMessage) error {
	raw, exists := object["knowledge_boundary"]
	if !exists || string(raw) == "null" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err == nil {
		for _, field := range []string{"known", "unknown", "misconceptions", "forbidden"} {
			if value, ok := fields[field]; ok {
				normalized, err := normalizeStringListRaw(value)
				if err != nil {
					return fmt.Errorf("knowledge_boundary.%s: %w", field, err)
				}
				fields[field] = normalized
			}
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("knowledge_boundary: %w", err)
		}
		object["knowledge_boundary"] = encoded
		return nil
	}
	var values stringListCompatibility
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("knowledge_boundary: want object, string array, or string")
	}
	encoded, err := json.Marshal(domain.CharacterKnowledgeBoundary{Known: []string(values)})
	if err != nil {
		return fmt.Errorf("knowledge_boundary: %w", err)
	}
	object["knowledge_boundary"] = encoded
	return nil
}

func normalizeStringListRaw(raw json.RawMessage) (json.RawMessage, error) {
	var values stringListCompatibility
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return json.Marshal([]string(values))
}

type stringListCompatibility []string

func (s *stringListCompatibility) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var values []string
	if err := json.Unmarshal(data, &values); err == nil {
		*s = values
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("want string or string array")
	}
	if strings.TrimSpace(value) != "" {
		*s = []string{value}
	}
	return nil
}
