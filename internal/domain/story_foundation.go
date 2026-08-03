package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	StoryFoundationSchemaVersion         = 3
	legacyStoryFoundationSchemaVersion   = 1
	previousStoryFoundationSchemaVersion = 2
	// Version 2 only renames mutual to bidirectional and adds undirected.
	// Version 3 adds optional complete-character-card fields. Empty v1/v2
	// fields remain omitted so legacy semantic signatures stay stable.
	// Retaining audit schema 1 keeps legacy mutual/bidirectional signatures
	// stable while the stored representation migrates on its next real save.
	storyFoundationAuditSchemaVersion = 1
)

type WorldRuleStrength string

const (
	WorldRuleStrengthHard WorldRuleStrength = "hard"
	WorldRuleStrengthSoft WorldRuleStrength = "soft"
)

type RelationshipType string

const (
	RelationshipTypeAlly         RelationshipType = "ally"
	RelationshipTypeRival        RelationshipType = "rival"
	RelationshipTypeFamily       RelationshipType = "family"
	RelationshipTypeRomantic     RelationshipType = "romantic"
	RelationshipTypeMentor       RelationshipType = "mentor"
	RelationshipTypeProfessional RelationshipType = "professional"
	RelationshipTypeOther        RelationshipType = "other"
)

type RelationshipDirection string

const (
	RelationshipDirectionDirected      RelationshipDirection = "directed"
	RelationshipDirectionBidirectional RelationshipDirection = "bidirectional"
	RelationshipDirectionUndirected    RelationshipDirection = "undirected"
	// RelationshipDirectionMutual is accepted only as a legacy wire value.
	RelationshipDirectionMutual RelationshipDirection = "mutual"
)

type RelationshipStatus string

const (
	RelationshipStatusPlanned  RelationshipStatus = "planned"
	RelationshipStatusActive   RelationshipStatus = "active"
	RelationshipStatusStrained RelationshipStatus = "strained"
	RelationshipStatusBroken   RelationshipStatus = "broken"
	RelationshipStatusResolved RelationshipStatus = "resolved"
)

// CharacterRelationship is a pre-writing relationship plan. Runtime chapter
// relationship state remains represented by RelationshipEntry.
type CharacterRelationship struct {
	ID                string                `json:"id,omitempty"`
	SourceCharacterID string                `json:"source_character_id"`
	TargetCharacterID string                `json:"target_character_id"`
	Type              RelationshipType      `json:"type"`
	Label             string                `json:"label,omitempty"`
	Direction         RelationshipDirection `json:"direction"`
	Status            RelationshipStatus    `json:"status"`
	Description       string                `json:"description,omitempty"`
	Since             string                `json:"since,omitempty"`
	Tags              []string              `json:"tags,omitempty"`
	Constraints       []string              `json:"constraints,omitempty"`
}

// StoryFoundation is the canonical target-story source for premise, planned
// cast relationships, characters, and world rules.
type StoryFoundation struct {
	SchemaVersion         int                     `json:"schema_version"`
	Revision              int64                   `json:"revision"`
	Premise               string                  `json:"premise"`
	Characters            []Character             `json:"characters"`
	Relationships         []CharacterRelationship `json:"relationships"`
	RelationshipsReviewed bool                    `json:"relationships_reviewed"`
	WorldRules            []WorldRule             `json:"world_rules"`
	UpdatedAt             string                  `json:"updated_at,omitempty"`
}

type FoundationSection string

const (
	FoundationSectionPremise               FoundationSection = "premise"
	FoundationSectionCharacters            FoundationSection = "characters"
	FoundationSectionRelationships         FoundationSection = "relationships"
	FoundationSectionRelationshipsReviewed FoundationSection = "relationships_reviewed"
	FoundationSectionWorldRules            FoundationSection = "world_rules"
)

func CloneStoryFoundation(in StoryFoundation) StoryFoundation {
	out := in
	if in.Characters != nil {
		out.Characters = make([]Character, len(in.Characters))
		for i := range in.Characters {
			out.Characters[i] = CloneCharacter(in.Characters[i])
		}
	}
	if in.Relationships != nil {
		out.Relationships = make([]CharacterRelationship, len(in.Relationships))
		for i := range in.Relationships {
			out.Relationships[i] = in.Relationships[i]
			out.Relationships[i].Tags = append([]string(nil), in.Relationships[i].Tags...)
			out.Relationships[i].Constraints = append([]string(nil), in.Relationships[i].Constraints...)
		}
	}
	if in.WorldRules != nil {
		out.WorldRules = make([]WorldRule, len(in.WorldRules))
		for i := range in.WorldRules {
			out.WorldRules[i] = in.WorldRules[i]
			out.WorldRules[i].Tags = append([]string(nil), in.WorldRules[i].Tags...)
		}
	}
	return out
}

// NormalizeStoryFoundation returns a detached canonical value. Legacy rules
// without strength normalize to hard, preserving the historical interpretation
// that a world rule is binding unless explicitly marked soft.
func NormalizeStoryFoundation(in StoryFoundation) (StoryFoundation, error) {
	out := CloneStoryFoundation(in)
	if out.SchemaVersion == 0 {
		out.SchemaVersion = StoryFoundationSchemaVersion
	}
	if out.SchemaVersion != legacyStoryFoundationSchemaVersion &&
		out.SchemaVersion != previousStoryFoundationSchemaVersion &&
		out.SchemaVersion != StoryFoundationSchemaVersion {
		return StoryFoundation{}, fmt.Errorf("story foundation schema version %d is unsupported", out.SchemaVersion)
	}
	out.SchemaVersion = StoryFoundationSchemaVersion
	out.Premise = strings.TrimSpace(out.Premise)

	claims := make(map[string]string)
	for i := range out.Characters {
		character := &out.Characters[i]
		character.ID = strings.TrimSpace(character.ID)
		character.Name = strings.TrimSpace(character.Name)
		character.Role = strings.TrimSpace(character.Role)
		character.Gender = strings.ToLower(strings.TrimSpace(character.Gender))
		character.Description = strings.TrimSpace(character.Description)
		character.Arc = strings.TrimSpace(character.Arc)
		character.Tier = strings.TrimSpace(character.Tier)
		character.Faction = strings.TrimSpace(character.Faction)
		character.Goal = strings.TrimSpace(character.Goal)
		character.Motivation = strings.TrimSpace(character.Motivation)
		character.Conflict = strings.TrimSpace(character.Conflict)
		character.Voice = strings.TrimSpace(character.Voice)
		character.Notes = strings.TrimSpace(character.Notes)
		character.Aliases = normalizedStrings(character.Aliases)
		character.Traits = normalizedStrings(character.Traits)
		if character.Traits == nil {
			character.Traits = []string{}
		}
		character.Constraints = normalizedStrings(character.Constraints)
		normalizeCharacterCardFields(character)
		if character.Name == "" {
			return StoryFoundation{}, fmt.Errorf("character %d name is required", i)
		}
		if character.Gender != "" && character.Gender != "male" && character.Gender != "female" &&
			character.Gender != "nonbinary" && character.Gender != "unspecified" {
			return StoryFoundation{}, fmt.Errorf("character %d gender %q is invalid", i, character.Gender)
		}
		for _, label := range append([]string{character.Name}, character.Aliases...) {
			key := normalizedIdentity(label)
			if owner, exists := claims[key]; exists && owner != character.Name {
				return StoryFoundation{}, fmt.Errorf("character name or alias %q conflicts between %q and %q", label, owner, character.Name)
			}
			claims[key] = character.Name
		}
		if character.ID == "" {
			character.ID = stableFoundationID("char", normalizedIdentity(character.Name))
		}
	}

	for i := range out.WorldRules {
		rule := &out.WorldRules[i]
		rule.ID = strings.TrimSpace(rule.ID)
		rule.Category = strings.TrimSpace(rule.Category)
		rule.Title = strings.TrimSpace(rule.Title)
		rule.Rule = strings.TrimSpace(rule.Rule)
		rule.Boundary = strings.TrimSpace(rule.Boundary)
		rule.Tags = normalizedStrings(rule.Tags)
		if rule.Category == "" {
			rule.Category = "other"
		}
		if rule.Rule == "" {
			return StoryFoundation{}, fmt.Errorf("world rule %d text is required", i)
		}
		if rule.Strength == "" {
			rule.Strength = WorldRuleStrengthHard
		}
		if rule.ID == "" {
			rule.ID = stableFoundationID("rule", normalizedIdentity(rule.Category)+"\x00"+normalizedIdentity(rule.Rule)+"\x00"+normalizedIdentity(rule.Boundary))
		}
	}

	for i := range out.Relationships {
		relation := &out.Relationships[i]
		relation.ID = strings.TrimSpace(relation.ID)
		relation.SourceCharacterID = strings.TrimSpace(relation.SourceCharacterID)
		relation.TargetCharacterID = strings.TrimSpace(relation.TargetCharacterID)
		relation.Label = strings.TrimSpace(relation.Label)
		relation.Description = strings.TrimSpace(relation.Description)
		relation.Since = strings.TrimSpace(relation.Since)
		relation.Tags = normalizedStrings(relation.Tags)
		relation.Constraints = normalizedStrings(relation.Constraints)
		relation.Direction = NormalizeRelationshipDirection(relation.Direction)
		if relation.Status == "" {
			relation.Status = RelationshipStatusPlanned
		}
		if relation.ID == "" {
			relation.ID = stableFoundationID("rel", relationshipSemanticKey(*relation))
		}
	}

	sort.Slice(out.Characters, func(i, j int) bool { return out.Characters[i].ID < out.Characters[j].ID })
	sort.Slice(out.Relationships, func(i, j int) bool { return out.Relationships[i].ID < out.Relationships[j].ID })
	sort.Slice(out.WorldRules, func(i, j int) bool { return out.WorldRules[i].ID < out.WorldRules[j].ID })
	if err := ValidateStoryFoundation(out); err != nil {
		return StoryFoundation{}, err
	}
	return out, nil
}

func ValidateStoryFoundation(value StoryFoundation) error {
	if value.SchemaVersion != StoryFoundationSchemaVersion {
		return fmt.Errorf("story foundation schema version %d is invalid", value.SchemaVersion)
	}
	if value.Revision < 0 {
		return fmt.Errorf("story foundation revision cannot be negative")
	}
	characterIDs := make(map[string]struct{}, len(value.Characters))
	for _, character := range value.Characters {
		if character.ID == "" || character.Name == "" {
			return fmt.Errorf("character id and name are required")
		}
		if _, err := normalizedCharacterTier(character.Tier); err != nil {
			return fmt.Errorf("character %q: %w", character.ID, err)
		}
		for _, contrast := range character.ContrastDetails {
			if contrast.Surface == "" || contrast.Depth == "" {
				return fmt.Errorf("character %q contrast details require surface and depth", character.ID)
			}
		}
		for _, backstory := range character.KeyBackstory {
			if backstory.Event == "" || backstory.Impact == "" {
				return fmt.Errorf("character %q key backstory requires event and impact", character.ID)
			}
		}
		if _, exists := characterIDs[character.ID]; exists {
			return fmt.Errorf("duplicate character id %q", character.ID)
		}
		characterIDs[character.ID] = struct{}{}
	}
	ruleIDs := make(map[string]struct{}, len(value.WorldRules))
	for _, rule := range value.WorldRules {
		if rule.ID == "" || rule.Rule == "" {
			return fmt.Errorf("world rule id and text are required")
		}
		if rule.Strength != WorldRuleStrengthHard && rule.Strength != WorldRuleStrengthSoft {
			return fmt.Errorf("world rule %q has invalid strength %q", rule.ID, rule.Strength)
		}
		if _, exists := ruleIDs[rule.ID]; exists {
			return fmt.Errorf("duplicate world rule id %q", rule.ID)
		}
		ruleIDs[rule.ID] = struct{}{}
	}
	relationshipIDs := make(map[string]struct{}, len(value.Relationships))
	edges := make(map[string]string, len(value.Relationships))
	for _, relation := range value.Relationships {
		if relation.ID == "" {
			return fmt.Errorf("relationship id is required")
		}
		if _, exists := relationshipIDs[relation.ID]; exists {
			return fmt.Errorf("duplicate relationship id %q", relation.ID)
		}
		relationshipIDs[relation.ID] = struct{}{}
		if _, exists := characterIDs[relation.SourceCharacterID]; !exists {
			return fmt.Errorf("relationship %q source character %q does not exist", relation.ID, relation.SourceCharacterID)
		}
		if _, exists := characterIDs[relation.TargetCharacterID]; !exists {
			return fmt.Errorf("relationship %q target character %q does not exist", relation.ID, relation.TargetCharacterID)
		}
		if relation.SourceCharacterID == relation.TargetCharacterID {
			return fmt.Errorf("relationship %q cannot be a self loop", relation.ID)
		}
		if !validRelationshipType(relation.Type) {
			return fmt.Errorf("relationship %q has invalid type %q", relation.ID, relation.Type)
		}
		if !validRelationshipDirection(relation.Direction) {
			return fmt.Errorf("relationship %q has invalid direction %q", relation.ID, relation.Direction)
		}
		if !validRelationshipStatus(relation.Status) {
			return fmt.Errorf("relationship %q has invalid status %q", relation.ID, relation.Status)
		}
		key := relationshipSemanticKey(relation)
		if existing, exists := edges[key]; exists {
			return fmt.Errorf("relationships %q and %q describe the same semantic edge", existing, relation.ID)
		}
		edges[key] = relation.ID
	}
	return nil
}

// FoundationContentSignature covers semantic target-story content only. It
// intentionally excludes revision, timestamps, schema, and review UI state.
func FoundationContentSignature(value StoryFoundation) (string, error) {
	normalized, err := NormalizeStoryFoundation(value)
	if err != nil {
		return "", err
	}
	normalized = canonicalFoundationCollections(normalized)
	signatureValue := CloneStoryFoundation(normalized)
	for i := range signatureValue.Relationships {
		if signatureValue.Relationships[i].Direction == RelationshipDirectionBidirectional {
			signatureValue.Relationships[i].Direction = RelationshipDirectionMutual
		}
	}
	payload := struct {
		Premise       string                  `json:"premise"`
		Characters    []Character             `json:"characters"`
		Relationships []CharacterRelationship `json:"relationships"`
		WorldRules    []WorldRule             `json:"world_rules"`
	}{signatureValue.Premise, signatureValue.Characters, signatureValue.Relationships, signatureValue.WorldRules}
	return jsonSignature(payload)
}

// FoundationAuditSignature binds schema interpretation to semantic content for
// downstream audit evidence, while still excluding mutable audit metadata.
func FoundationAuditSignature(value StoryFoundation) (string, error) {
	content, err := FoundationContentSignature(value)
	if err != nil {
		return "", err
	}
	return jsonSignature(struct {
		SchemaVersion int    `json:"schema_version"`
		Content       string `json:"content_signature"`
	}{storyFoundationAuditSchemaVersion, content})
}

func FoundationReviewConfirmationSignature(value StoryFoundation) (string, error) {
	audit, err := FoundationAuditSignature(value)
	if err != nil {
		return "", err
	}
	return jsonSignature(struct {
		Audit    string `json:"audit_signature"`
		Reviewed bool   `json:"relationships_reviewed"`
	}{audit, value.RelationshipsReviewed})
}

func StoryFoundationSectionEqual(a, b StoryFoundation, section FoundationSection) (bool, error) {
	left, err := NormalizeStoryFoundation(a)
	if err != nil {
		return false, err
	}
	right, err := NormalizeStoryFoundation(b)
	if err != nil {
		return false, err
	}
	var lv, rv any
	switch section {
	case FoundationSectionPremise:
		lv, rv = left.Premise, right.Premise
	case FoundationSectionCharacters:
		lv, rv = left.Characters, right.Characters
	case FoundationSectionRelationships:
		lv, rv = left.Relationships, right.Relationships
	case FoundationSectionRelationshipsReviewed:
		lv, rv = left.RelationshipsReviewed, right.RelationshipsReviewed
	case FoundationSectionWorldRules:
		lv, rv = left.WorldRules, right.WorldRules
	default:
		return false, fmt.Errorf("unknown foundation section %q", section)
	}
	leftJSON, _ := json.Marshal(lv)
	rightJSON, _ := json.Marshal(rv)
	return string(leftJSON) == string(rightJSON), nil
}

func normalizedStrings(values []string) []string {
	if values == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := normalizedIdentity(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return normalizedIdentity(result[i]) < normalizedIdentity(result[j]) })
	return result
}

func normalizedIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func stableFoundationID(prefix, seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}

func relationshipSemanticKey(relation CharacterRelationship) string {
	source := relation.SourceCharacterID
	target := relation.TargetCharacterID
	direction := NormalizeRelationshipDirection(relation.Direction)
	if direction != RelationshipDirectionDirected && target < source {
		source, target = target, source
	}
	return strings.Join([]string{source, target, string(relation.Type), string(direction)}, "\x00")
}

// NormalizeRelationshipDirection centralizes the legacy wire migration.
func NormalizeRelationshipDirection(value RelationshipDirection) RelationshipDirection {
	switch value {
	case "", RelationshipDirectionMutual:
		return RelationshipDirectionBidirectional
	default:
		return value
	}
}

func validRelationshipDirection(value RelationshipDirection) bool {
	switch value {
	case RelationshipDirectionDirected, RelationshipDirectionBidirectional, RelationshipDirectionUndirected:
		return true
	default:
		return false
	}
}

func validRelationshipType(value RelationshipType) bool {
	switch value {
	case RelationshipTypeAlly, RelationshipTypeRival, RelationshipTypeFamily, RelationshipTypeRomantic, RelationshipTypeMentor, RelationshipTypeProfessional, RelationshipTypeOther:
		return true
	default:
		return false
	}
}

func validRelationshipStatus(value RelationshipStatus) bool {
	switch value {
	case RelationshipStatusPlanned, RelationshipStatusActive, RelationshipStatusStrained, RelationshipStatusBroken, RelationshipStatusResolved:
		return true
	default:
		return false
	}
}

func jsonSignature(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalFoundationCollections(value StoryFoundation) StoryFoundation {
	if value.Characters == nil {
		value.Characters = []Character{}
	}
	if value.Relationships == nil {
		value.Relationships = []CharacterRelationship{}
	}
	if value.WorldRules == nil {
		value.WorldRules = []WorldRule{}
	}
	return value
}
