package domain

import (
	"fmt"
	"reflect"
	"strings"
)

const (
	FoundationReviewStatusCollecting = "collecting"
	FoundationReviewStatusPending    = "pending"
	FoundationReviewStatusApproved   = "approved"
)

var FoundationGenerationSections = []string{"premise", "characters", "planned_relationships", "world_rules"}

// ValidateFoundationPreservesCoreCast allows Architect to add supporting cast
// and relationships while treating every confirmed core member and relation as
// an immutable subset of the canonical StoryFoundation.
func ValidateFoundationPreservesCoreCast(foundation StoryFoundation, contract CoreCastContract) error {
	normalizedFoundation, err := NormalizeStoryFoundation(foundation)
	if err != nil {
		return err
	}
	normalizedContract, err := NormalizeCoreCastContract(contract)
	if err != nil {
		return err
	}
	characters := make(map[string]Character, len(normalizedFoundation.Characters))
	for _, character := range normalizedFoundation.Characters {
		characters[character.ID] = character
	}
	for _, expected := range ContractCharacters(normalizedContract) {
		actual, ok := characters[expected.ID]
		if !ok {
			return fmt.Errorf("confirmed core character %q is missing", expected.ID)
		}
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("confirmed core character %q was rewritten", expected.ID)
		}
	}
	relationships := make(map[string]CharacterRelationship, len(normalizedFoundation.Relationships))
	for _, relationship := range normalizedFoundation.Relationships {
		relationships[relationship.ID] = relationship
	}
	for _, expected := range normalizedContract.PlannedRelationships {
		actual, ok := relationships[expected.ID]
		if !ok {
			return fmt.Errorf("confirmed core relationship %q is missing", expected.ID)
		}
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("confirmed core relationship %q was rewritten", expected.ID)
		}
	}
	return nil
}

func ValidateFoundationComplete(foundation StoryFoundation, contract CoreCastContract) error {
	normalized, err := NormalizeStoryFoundation(foundation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(normalized.Premise) == "" {
		return fmt.Errorf("foundation premise is required")
	}
	if len(normalized.Characters) == 0 {
		return fmt.Errorf("foundation characters are required")
	}
	if len(normalized.WorldRules) == 0 {
		return fmt.Errorf("foundation world rules are required")
	}
	return ValidateFoundationPreservesCoreCast(normalized, contract)
}

func FoundationSectionsComplete(sections []string) bool {
	seen := make(map[string]struct{}, len(sections))
	for _, section := range sections {
		seen[strings.TrimSpace(section)] = struct{}{}
	}
	for _, required := range FoundationGenerationSections {
		if _, ok := seen[required]; !ok {
			return false
		}
	}
	return true
}

func AddFoundationSection(sections []string, section string) []string {
	section = strings.TrimSpace(section)
	if section == "" {
		return append([]string(nil), sections...)
	}
	for _, existing := range sections {
		if existing == section {
			return append([]string(nil), sections...)
		}
	}
	return append(append([]string(nil), sections...), section)
}
