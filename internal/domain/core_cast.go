package domain

import (
	"fmt"
	"sort"
	"strings"
)

const CoreCastContractVersion = 1

type CoreCastMode string

const (
	CoreCastModeNormal     CoreCastMode = "normal"
	CoreCastModeAdaptation CoreCastMode = "adaptation"
)

type CoreCastImportance string

const (
	CoreCastImportanceProtagonist   CoreCastImportance = "protagonist"
	CoreCastImportanceCoProtagonist CoreCastImportance = "co_protagonist"
	CoreCastImportanceMajorPOV      CoreCastImportance = "major_pov"
	CoreCastImportanceAntagonist    CoreCastImportance = "antagonist"
	CoreCastImportanceLoveInterest  CoreCastImportance = "love_interest"
	CoreCastImportanceMajorSupport  CoreCastImportance = "major_support"
	CoreCastImportanceUserImportant CoreCastImportance = "user_important"
)

type CoreCastOrigin string

const (
	CoreCastOriginOriginal CoreCastOrigin = "original"
	CoreCastOriginSource   CoreCastOrigin = "source"
)

type SourceDispositionAction string

const (
	SourceDispositionKeep    SourceDispositionAction = "keep"
	SourceDispositionRename  SourceDispositionAction = "rename"
	SourceDispositionMerge   SourceDispositionAction = "merge"
	SourceDispositionSplit   SourceDispositionAction = "split"
	SourceDispositionExclude SourceDispositionAction = "exclude"
)

type CoreCastMember struct {
	Character           Character          `json:"character"`
	Importance          CoreCastImportance `json:"importance"`
	Origin              CoreCastOrigin     `json:"origin"`
	MainlineFunction    string             `json:"mainline_function"`
	SourceCharacterIDs  []string           `json:"source_character_ids,omitempty"`
	InclusionRationale  string             `json:"inclusion_rationale,omitempty"`
	NoCoreRelationships bool               `json:"no_core_relationships,omitempty"`
}

type SourceCharacterDisposition struct {
	SourceCharacterID  string                  `json:"source_character_id"`
	Action             SourceDispositionAction `json:"action"`
	TargetCharacterIDs []string                `json:"target_character_ids,omitempty"`
	Rationale          string                  `json:"rationale,omitempty"`
}

type CoreCastPublishReceipt struct {
	Status             string `json:"status,omitempty"`
	ContentSignature   string `json:"content_signature,omitempty"`
	FoundationRevision int64  `json:"foundation_revision,omitempty"`
	PublishedAt        string `json:"published_at,omitempty"`
}

type CoreCastContract struct {
	Version              int                          `json:"version"`
	Mode                 CoreCastMode                 `json:"mode"`
	DraftRevision        int64                        `json:"draft_revision"`
	DraftHash            string                       `json:"draft_hash"`
	SourceSignature      string                       `json:"source_signature,omitempty"`
	AdaptationIntentHash string                       `json:"adaptation_intent_hash,omitempty"`
	Members              []CoreCastMember             `json:"members"`
	PlannedRelationships []CharacterRelationship      `json:"planned_relationships"`
	SourceDispositions   []SourceCharacterDisposition `json:"source_dispositions,omitempty"`
	ContentSignature     string                       `json:"content_signature"`
	ConfirmedSignature   string                       `json:"confirmed_signature,omitempty"`
	ConfirmedAt          string                       `json:"confirmed_at,omitempty"`
	PublishReceipt       CoreCastPublishReceipt       `json:"publish_receipt,omitempty"`
	Revision             int64                        `json:"revision"`
}

type SourceMajorCharacter struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

type CoreCastMissingItem struct {
	Code        string `json:"code"`
	MemberID    string `json:"member_id,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
	Description string `json:"description"`
}

type CoreCastCompletionResult struct {
	Complete        bool                  `json:"complete"`
	Missing         []CoreCastMissingItem `json:"missing"`
	BlockingReasons []string              `json:"blocking_reasons"`
}

func NormalizeCoreCastContract(value CoreCastContract) (CoreCastContract, error) {
	out := cloneCoreCastContract(value)
	if out.Version == 0 {
		out.Version = CoreCastContractVersion
	}
	if out.Version != CoreCastContractVersion {
		return CoreCastContract{}, fmt.Errorf("core cast version %d is unsupported", out.Version)
	}
	if out.Mode != CoreCastModeNormal && out.Mode != CoreCastModeAdaptation {
		return CoreCastContract{}, fmt.Errorf("core cast mode %q is invalid", out.Mode)
	}
	if out.DraftRevision < 0 || out.Revision < 0 {
		return CoreCastContract{}, fmt.Errorf("core cast revisions cannot be negative")
	}
	out.SourceSignature = strings.TrimSpace(out.SourceSignature)
	out.DraftHash = strings.TrimSpace(out.DraftHash)
	out.AdaptationIntentHash = strings.TrimSpace(out.AdaptationIntentHash)
	out.ConfirmedSignature = strings.TrimSpace(out.ConfirmedSignature)
	out.ConfirmedAt = strings.TrimSpace(out.ConfirmedAt)
	out.PublishReceipt.Status = strings.TrimSpace(out.PublishReceipt.Status)
	out.PublishReceipt.ContentSignature = strings.TrimSpace(out.PublishReceipt.ContentSignature)
	out.PublishReceipt.PublishedAt = strings.TrimSpace(out.PublishReceipt.PublishedAt)

	for i := range out.Members {
		member := &out.Members[i]
		normalizeCharacter(&member.Character)
		member.MainlineFunction = strings.TrimSpace(member.MainlineFunction)
		member.InclusionRationale = strings.TrimSpace(member.InclusionRationale)
		member.SourceCharacterIDs = normalizedStrings(member.SourceCharacterIDs)
		if member.Character.ID == "" && member.Character.Name != "" {
			member.Character.ID = stableFoundationID("char", normalizedIdentity(member.Character.Name))
		}
	}
	for i := range out.PlannedRelationships {
		relation := &out.PlannedRelationships[i]
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
	for i := range out.SourceDispositions {
		disposition := &out.SourceDispositions[i]
		disposition.SourceCharacterID = strings.TrimSpace(disposition.SourceCharacterID)
		disposition.TargetCharacterIDs = normalizedStrings(disposition.TargetCharacterIDs)
		disposition.Rationale = strings.TrimSpace(disposition.Rationale)
	}
	sort.Slice(out.Members, func(i, j int) bool { return out.Members[i].Character.ID < out.Members[j].Character.ID })
	sort.Slice(out.PlannedRelationships, func(i, j int) bool { return out.PlannedRelationships[i].ID < out.PlannedRelationships[j].ID })
	sort.Slice(out.SourceDispositions, func(i, j int) bool {
		return out.SourceDispositions[i].SourceCharacterID < out.SourceDispositions[j].SourceCharacterID
	})
	if out.Members == nil {
		out.Members = []CoreCastMember{}
	}
	if out.PlannedRelationships == nil {
		out.PlannedRelationships = []CharacterRelationship{}
	}
	if out.SourceDispositions == nil {
		out.SourceDispositions = []SourceCharacterDisposition{}
	}
	signature, err := coreCastContentSignature(out)
	if err != nil {
		return CoreCastContract{}, err
	}
	out.ContentSignature = signature
	return out, nil
}

func CoreCastContentSignature(value CoreCastContract) (string, error) {
	normalized, err := NormalizeCoreCastContract(value)
	if err != nil {
		return "", err
	}
	return normalized.ContentSignature, nil
}

func coreCastContentSignature(value CoreCastContract) (string, error) {
	relationships := append([]CharacterRelationship(nil), value.PlannedRelationships...)
	for i := range relationships {
		if relationships[i].Direction == RelationshipDirectionBidirectional {
			relationships[i].Direction = RelationshipDirectionMutual
		}
	}
	return jsonSignature(struct {
		Version              int                          `json:"version"`
		Mode                 CoreCastMode                 `json:"mode"`
		DraftRevision        int64                        `json:"draft_revision"`
		DraftHash            string                       `json:"draft_hash"`
		SourceSignature      string                       `json:"source_signature,omitempty"`
		AdaptationIntentHash string                       `json:"adaptation_intent_hash,omitempty"`
		Members              []CoreCastMember             `json:"members"`
		Relationships        []CharacterRelationship      `json:"planned_relationships"`
		Dispositions         []SourceCharacterDisposition `json:"source_dispositions,omitempty"`
	}{value.Version, value.Mode, value.DraftRevision, value.DraftHash, value.SourceSignature, value.AdaptationIntentHash, value.Members, relationships, value.SourceDispositions})
}

func CoreCastCompletion(contract CoreCastContract, sourceCharacters, sourceMajor []SourceMajorCharacter) CoreCastCompletionResult {
	normalized, err := NormalizeCoreCastContract(contract)
	if err != nil {
		return completionFromMissing([]CoreCastMissingItem{{Code: "invalid_contract", Description: err.Error()}})
	}
	var missing []CoreCastMissingItem
	memberIDs := make(map[string]CoreCastMember, len(normalized.Members))
	memberIdentities := make(map[string]string, len(normalized.Members)*2)
	sourceCharacterIDs := make(map[string]struct{}, len(sourceCharacters))
	for _, source := range sourceCharacters {
		if id := strings.TrimSpace(source.ID); id != "" {
			sourceCharacterIDs[id] = struct{}{}
		}
	}
	hasProtagonist := false
	for _, member := range normalized.Members {
		id := member.Character.ID
		if id == "" {
			missing = appendMissing(missing, "member_id_required", id, "", "core character stable id is required")
		} else if _, exists := memberIDs[id]; exists {
			missing = appendMissing(missing, "duplicate_member_id", id, "", "core character id must be unique")
		}
		memberIDs[id] = member
		for _, label := range append([]string{member.Character.Name}, member.Character.Aliases...) {
			identity := normalizedIdentity(label)
			if identity == "" {
				continue
			}
			if owner, exists := memberIdentities[identity]; exists && owner != id {
				missing = appendMissing(missing, "member_identity_ambiguous", id, "", fmt.Sprintf("character name or alias %q is shared by multiple core characters", label))
				continue
			}
			memberIdentities[identity] = id
		}
		if member.Importance == CoreCastImportanceProtagonist || member.Importance == CoreCastImportanceCoProtagonist {
			hasProtagonist = true
		}
		if !validCoreCastImportance(member.Importance) {
			missing = appendMissing(missing, "importance_invalid", id, "", "controlled importance is required")
		}
		if !validCoreCastOrigin(member.Origin) {
			missing = appendMissing(missing, "origin_invalid", id, "", "controlled origin is required")
		}
		if normalized.Mode == CoreCastModeNormal && member.Origin != CoreCastOriginOriginal {
			missing = appendMissing(missing, "normal_origin_invalid", id, "", "normal co-create characters must have original origin")
		}
		if normalized.Mode == CoreCastModeAdaptation && member.Origin == CoreCastOriginSource && len(member.SourceCharacterIDs) == 0 {
			missing = appendMissing(missing, "source_character_ids_required", id, "", "source-derived character requires stable source character references")
		}
		if normalized.Mode == CoreCastModeAdaptation {
			for _, sourceID := range member.SourceCharacterIDs {
				if _, exists := sourceCharacterIDs[sourceID]; !exists {
					missing = appendMissing(missing, "member_source_character_unknown", id, sourceID, "core character source reference does not exist in the current source character set")
				}
			}
		}
		requireMemberText := []struct{ code, value, description string }{
			{"name_required", member.Character.Name, "character name is required"},
			{"role_required", member.Character.Role, "identity or story role is required"},
			{"mainline_function_required", member.MainlineFunction, "mainline function is required"},
			{"goal_required", member.Character.Goal, "goal is required"},
			{"motivation_required", member.Character.Motivation, "motivation is required"},
			{"conflict_required", member.Character.Conflict, "conflict is required"},
			{"arc_required", member.Character.Arc, "character arc is required"},
		}
		for _, required := range requireMemberText {
			if strings.TrimSpace(required.value) == "" {
				missing = appendMissing(missing, required.code, id, "", required.description)
			}
		}
		if len(member.Character.Traits) == 0 && strings.TrimSpace(member.Character.Voice) == "" {
			missing = appendMissing(missing, "traits_or_voice_required", id, "", "at least one trait or explicit voice is required")
		}
		if len(member.Character.Constraints) == 0 {
			missing = appendMissing(missing, "constraints_required", id, "", "key constraints are required")
		}
		if normalized.Mode == CoreCastModeAdaptation && member.Origin == CoreCastOriginOriginal && member.InclusionRationale == "" {
			missing = appendMissing(missing, "inclusion_rationale_required", id, "", "original adaptation character inclusion rationale is required")
		}
	}
	if normalized.Mode == CoreCastModeNormal && !hasProtagonist {
		missing = appendMissing(missing, "protagonist_required", "", "", "normal co-create requires a protagonist or co-protagonist")
	}
	relationshipCount := make(map[string]int, len(memberIDs))
	edges := make(map[string]struct{}, len(normalized.PlannedRelationships))
	relationshipIDs := make(map[string]struct{}, len(normalized.PlannedRelationships))
	for _, relationship := range normalized.PlannedRelationships {
		if relationship.ID == "" {
			missing = appendMissing(missing, "relationship_id_required", "", "", "core relationship stable id is required")
		} else if _, exists := relationshipIDs[relationship.ID]; exists {
			missing = appendMissing(missing, "relationship_id_duplicate", "", "", fmt.Sprintf("relationship id %q must be unique", relationship.ID))
		}
		relationshipIDs[relationship.ID] = struct{}{}
		if _, exists := memberIDs[relationship.SourceCharacterID]; !exists {
			missing = appendMissing(missing, "relationship_source_missing", "", "", fmt.Sprintf("relationship %q source does not exist", relationship.ID))
		}
		if _, exists := memberIDs[relationship.TargetCharacterID]; !exists {
			missing = appendMissing(missing, "relationship_target_missing", "", "", fmt.Sprintf("relationship %q target does not exist", relationship.ID))
		}
		if relationship.SourceCharacterID == relationship.TargetCharacterID {
			missing = appendMissing(missing, "relationship_self_loop", relationship.SourceCharacterID, "", "core relationship cannot be a self loop")
		}
		if !validRelationshipType(relationship.Type) || !validRelationshipStatus(relationship.Status) || !validRelationshipDirection(relationship.Direction) {
			missing = appendMissing(missing, "relationship_invalid", "", "", fmt.Sprintf("relationship %q has invalid controlled fields", relationship.ID))
		}
		key := relationshipSemanticKey(relationship)
		if _, exists := edges[key]; exists {
			missing = appendMissing(missing, "relationship_duplicate", "", "", fmt.Sprintf("relationship %q duplicates another semantic edge", relationship.ID))
		}
		edges[key] = struct{}{}
		relationshipCount[relationship.SourceCharacterID]++
		relationshipCount[relationship.TargetCharacterID]++
	}
	for id, member := range memberIDs {
		if relationshipCount[id] == 0 && !member.NoCoreRelationships {
			missing = appendMissing(missing, "relationship_or_declaration_required", id, "", "core relationship or explicit no-core-relationships declaration is required")
		}
	}
	if normalized.Mode == CoreCastModeAdaptation {
		missing = append(missing, adaptationDispositionMissing(normalized, sourceMajor, memberIDs)...)
	}
	return completionFromMissing(missing)
}

func ResolveSourceCharacters(source AdaptationSourceFoundation) []SourceMajorCharacter {
	resolved := make([]SourceMajorCharacter, 0, len(source.Characters))
	seen := make(map[string]struct{}, len(source.Characters))
	for _, character := range source.Characters {
		id := strings.TrimSpace(character.ID)
		if id == "" && strings.TrimSpace(character.Name) != "" {
			id = stableFoundationID("source-char", normalizedIdentity(character.Name))
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		resolved = append(resolved, SourceMajorCharacter{ID: id, Name: strings.TrimSpace(character.Name), Aliases: normalizedStrings(character.Aliases)})
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].ID < resolved[j].ID })
	return resolved
}

func ResolveSourceMajorCharacters(source AdaptationSourceFoundation, dossier AdaptationCoCreateDossier) ([]SourceMajorCharacter, []CoreCastMissingItem) {
	// SourceFoundation v4+ is already the reviewed formal-card projection:
	// only protagonists and important supporting characters remain in
	// Characters. Re-expanding "major" identities from the older dossier
	// would reintroduce evidence-only labels and make a confirmed Character
	// workflow impossible to publish.
	if source.Version >= 4 {
		return ResolveSourceCharacters(source), nil
	}
	identities := make(map[string][]SourceMajorCharacter)
	for _, character := range source.Characters {
		id := strings.TrimSpace(character.ID)
		if id == "" && strings.TrimSpace(character.Name) != "" {
			id = stableFoundationID("source-char", normalizedIdentity(character.Name))
		}
		major := SourceMajorCharacter{ID: id, Name: strings.TrimSpace(character.Name), Aliases: normalizedStrings(character.Aliases)}
		for _, label := range append([]string{id, major.Name}, major.Aliases...) {
			if key := normalizedIdentity(label); key != "" {
				identities[key] = append(identities[key], major)
			}
		}
	}
	seen := make(map[string]struct{})
	var resolved []SourceMajorCharacter
	var missing []CoreCastMissingItem
	for _, batch := range dossier.Batches {
		for _, label := range batch.MajorCharacters {
			key := normalizedIdentity(label)
			matches := uniqueSourceMajorMatches(identities[key])
			switch len(matches) {
			case 0:
				missing = appendMissing(missing, "source_major_unresolved", "", label, fmt.Sprintf("source major character %q cannot be resolved", label))
			case 1:
				if _, exists := seen[matches[0].ID]; !exists {
					seen[matches[0].ID] = struct{}{}
					resolved = append(resolved, matches[0])
				}
			default:
				missing = appendMissing(missing, "source_major_ambiguous", "", label, fmt.Sprintf("source major character %q is ambiguous", label))
			}
		}
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].ID < resolved[j].ID })
	return resolved, missing
}

func ContractCharacters(contract CoreCastContract) []Character {
	characters := make([]Character, 0, len(contract.Members))
	for _, member := range contract.Members {
		characters = append(characters, CloneCharacter(member.Character))
	}
	return characters
}

// ApplyCoreCastToFoundation replaces the confirmed core subset while
// preserving supporting characters and relationships outside that subset.
func ApplyCoreCastToFoundation(foundation StoryFoundation, contract CoreCastContract) StoryFoundation {
	out := CloneStoryFoundation(foundation)
	coreIDs := make(map[string]struct{}, len(contract.Members))
	for _, member := range contract.Members {
		coreIDs[member.Character.ID] = struct{}{}
	}
	characters := make([]Character, 0, len(out.Characters)+len(contract.Members))
	for _, character := range out.Characters {
		if _, core := coreIDs[character.ID]; !core {
			characters = append(characters, character)
		}
	}
	characters = append(characters, ContractCharacters(contract)...)
	relationships := make([]CharacterRelationship, 0, len(out.Relationships)+len(contract.PlannedRelationships))
	for _, relationship := range out.Relationships {
		_, sourceCore := coreIDs[relationship.SourceCharacterID]
		_, targetCore := coreIDs[relationship.TargetCharacterID]
		if !sourceCore || !targetCore {
			relationships = append(relationships, relationship)
		}
	}
	relationships = append(relationships, contract.PlannedRelationships...)
	out.Characters = characters
	out.Relationships = relationships
	out.RelationshipsReviewed = true
	return out
}

func cloneCoreCastContract(in CoreCastContract) CoreCastContract {
	out := in
	out.Members = append([]CoreCastMember(nil), in.Members...)
	for i := range out.Members {
		out.Members[i].Character = CloneCharacter(in.Members[i].Character)
		out.Members[i].SourceCharacterIDs = append([]string(nil), in.Members[i].SourceCharacterIDs...)
	}
	out.PlannedRelationships = append([]CharacterRelationship(nil), in.PlannedRelationships...)
	for i := range out.PlannedRelationships {
		out.PlannedRelationships[i].Tags = append([]string(nil), in.PlannedRelationships[i].Tags...)
		out.PlannedRelationships[i].Constraints = append([]string(nil), in.PlannedRelationships[i].Constraints...)
	}
	out.SourceDispositions = append([]SourceCharacterDisposition(nil), in.SourceDispositions...)
	for i := range out.SourceDispositions {
		out.SourceDispositions[i].TargetCharacterIDs = append([]string(nil), in.SourceDispositions[i].TargetCharacterIDs...)
	}
	return out
}

func adaptationDispositionMissing(contract CoreCastContract, sourceMajor []SourceMajorCharacter, members map[string]CoreCastMember) []CoreCastMissingItem {
	if strings.TrimSpace(contract.SourceSignature) == "" {
		return []CoreCastMissingItem{{Code: "source_signature_required", Description: "adaptation source signature is required"}}
	}
	var missing []CoreCastMissingItem
	if strings.TrimSpace(contract.AdaptationIntentHash) == "" {
		missing = appendMissing(missing, "intent_hash_required", "", "", "adaptation intent hash is required")
	}
	dispositions := make(map[string]SourceCharacterDisposition, len(contract.SourceDispositions))
	sourceIDs := make(map[string]struct{}, len(sourceMajor))
	for _, source := range sourceMajor {
		sourceIDs[source.ID] = struct{}{}
	}
	for _, disposition := range contract.SourceDispositions {
		if disposition.SourceCharacterID == "" {
			missing = appendMissing(missing, "disposition_source_required", "", "", "source disposition requires a source character id")
			continue
		}
		if _, exists := dispositions[disposition.SourceCharacterID]; exists {
			missing = appendMissing(missing, "disposition_duplicate", "", disposition.SourceCharacterID, "source character has conflicting dispositions")
		}
		if _, exists := sourceIDs[disposition.SourceCharacterID]; !exists {
			missing = appendMissing(missing, "disposition_source_unknown", "", disposition.SourceCharacterID, "source disposition refers to a character outside the resolved source-major set")
		}
		dispositions[disposition.SourceCharacterID] = disposition
		if !validDispositionAction(disposition.Action) {
			missing = appendMissing(missing, "disposition_action_invalid", "", disposition.SourceCharacterID, "source disposition action is invalid")
			continue
		}
		if disposition.Action == SourceDispositionExclude {
			if len(disposition.TargetCharacterIDs) != 0 {
				missing = appendMissing(missing, "exclude_targets_forbidden", "", disposition.SourceCharacterID, "exclude cannot contain target characters")
			}
			continue
		}
		if disposition.Action == SourceDispositionSplit && len(disposition.TargetCharacterIDs) < 2 {
			missing = appendMissing(missing, "disposition_targets_required", "", disposition.SourceCharacterID, "source disposition target mapping is incomplete")
		} else if disposition.Action != SourceDispositionSplit && len(disposition.TargetCharacterIDs) != 1 {
			missing = appendMissing(missing, "disposition_target_cardinality", "", disposition.SourceCharacterID, "keep, rename, and merge dispositions require exactly one target character")
		}
		seenTargets := make(map[string]struct{}, len(disposition.TargetCharacterIDs))
		for _, targetID := range disposition.TargetCharacterIDs {
			member, exists := members[targetID]
			if !exists {
				missing = appendMissing(missing, "disposition_target_missing", targetID, disposition.SourceCharacterID, "source disposition target does not exist")
				continue
			}
			if _, duplicate := seenTargets[targetID]; duplicate {
				missing = appendMissing(missing, "disposition_target_duplicate", targetID, disposition.SourceCharacterID, "source disposition repeats a target")
			}
			seenTargets[targetID] = struct{}{}
			if member.Origin != CoreCastOriginSource || !containsNormalized(member.SourceCharacterIDs, disposition.SourceCharacterID) {
				missing = appendMissing(missing, "disposition_direction_invalid", targetID, disposition.SourceCharacterID, "source target must reference the disposed source character")
			}
		}
	}
	for _, source := range sourceMajor {
		if _, exists := dispositions[source.ID]; !exists {
			missing = appendMissing(missing, "source_major_disposition_required", "", source.ID, fmt.Sprintf("source major character %q requires a disposition", source.Name))
		}
	}
	return missing
}

func completionFromMissing(missing []CoreCastMissingItem) CoreCastCompletionResult {
	reasons := make([]string, 0, len(missing))
	seen := make(map[string]struct{}, len(missing))
	for _, item := range missing {
		if _, exists := seen[item.Description]; exists {
			continue
		}
		seen[item.Description] = struct{}{}
		reasons = append(reasons, item.Description)
	}
	if missing == nil {
		missing = []CoreCastMissingItem{}
	}
	if reasons == nil {
		reasons = []string{}
	}
	return CoreCastCompletionResult{Complete: len(missing) == 0, Missing: missing, BlockingReasons: reasons}
}

func appendMissing(items []CoreCastMissingItem, code, memberID, sourceID, description string) []CoreCastMissingItem {
	return append(items, CoreCastMissingItem{Code: code, MemberID: memberID, SourceID: sourceID, Description: description})
}

func validCoreCastImportance(value CoreCastImportance) bool {
	switch value {
	case CoreCastImportanceProtagonist, CoreCastImportanceCoProtagonist, CoreCastImportanceMajorPOV, CoreCastImportanceAntagonist, CoreCastImportanceLoveInterest, CoreCastImportanceMajorSupport, CoreCastImportanceUserImportant:
		return true
	default:
		return false
	}
}

func validCoreCastOrigin(value CoreCastOrigin) bool {
	return value == CoreCastOriginOriginal || value == CoreCastOriginSource
}

func validDispositionAction(value SourceDispositionAction) bool {
	switch value {
	case SourceDispositionKeep, SourceDispositionRename, SourceDispositionMerge, SourceDispositionSplit, SourceDispositionExclude:
		return true
	default:
		return false
	}
}

func containsNormalized(values []string, wanted string) bool {
	wanted = normalizedIdentity(wanted)
	for _, value := range values {
		if normalizedIdentity(value) == wanted {
			return true
		}
	}
	return false
}

func uniqueSourceMajorMatches(values []SourceMajorCharacter) []SourceMajorCharacter {
	seen := make(map[string]struct{}, len(values))
	out := make([]SourceMajorCharacter, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value.ID]; exists {
			continue
		}
		seen[value.ID] = struct{}{}
		out = append(out, value)
	}
	return out
}
