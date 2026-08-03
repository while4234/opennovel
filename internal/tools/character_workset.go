package tools

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	characterWorksetMaxFullCards  = 8
	characterWorksetMaxCompressed = 8
	characterWorksetBudgetBytes   = 16 * 1024
)

type characterIdentity struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Tier        string   `json:"tier,omitempty"`
	Role        string   `json:"role,omitempty"`
	Goal        string   `json:"goal,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
}

type characterConflict struct {
	CharacterID  string `json:"character_id"`
	Field        string `json:"field"`
	StaticValue  string `json:"static_value,omitempty"`
	DynamicValue string `json:"dynamic_value,omitempty"`
	Reason       string `json:"reason"`
}

type characterWorksetDiagnostics struct {
	RequestedIDs  []string `json:"requested_ids,omitempty"`
	FullIDs       []string `json:"full_ids,omitempty"`
	CompressedIDs []string `json:"compressed_ids,omitempty"`
	TrimmedIDs    []string `json:"trimmed_ids,omitempty"`
	CompactedIDs  []string `json:"compacted_ids,omitempty"`
	SelectionMode string   `json:"selection_mode"`
	BudgetBytes   int      `json:"budget_bytes"`
	EncodedBytes  int      `json:"encoded_bytes"`
}

type characterWorkset struct {
	Full        []domain.Character          `json:"full"`
	Compressed  []characterIdentity         `json:"compressed,omitempty"`
	Snapshots   []domain.CharacterSnapshot  `json:"snapshots,omitempty"`
	Conflicts   []characterConflict         `json:"static_dynamic_conflicts,omitempty"`
	Diagnostics characterWorksetDiagnostics `json:"diagnostics"`
}

func (t *ContextTool) buildCharacterWorkset(chapter int) (characterWorkset, error) {
	characters, err := t.store.Characters.Load()
	if err != nil {
		return characterWorkset{}, err
	}
	workset := characterWorkset{Diagnostics: characterWorksetDiagnostics{BudgetBytes: characterWorksetBudgetBytes}}
	if len(characters) == 0 {
		return workset, nil
	}
	entry, outlineErr := t.store.Outline.GetChapterOutline(chapter)
	requested := make([]string, 0)
	selectionMode := "legacy_name_fallback"
	if outlineErr == nil && entry != nil {
		requested = outlineCharacterIDs(*entry)
		if len(requested) > 0 {
			selectionMode = "stable_id"
		} else {
			text := strings.Join(append([]string{entry.Title, entry.CoreEvent, entry.Hook}, entry.Scenes...), " ")
			for _, character := range characters {
				if matchCharacter(text, character) {
					requested = append(requested, character.ID)
				}
			}
		}
	}
	workset.Diagnostics.SelectionMode = selectionMode
	workset.Diagnostics.RequestedIDs = stableUniqueIDs(requested)

	byID := make(map[string]domain.Character, len(characters))
	byName := make(map[string]domain.Character, len(characters)*2)
	for _, character := range characters {
		if id := strings.TrimSpace(character.ID); id != "" {
			byID[id] = character
		}
		byName[character.Name] = character
		for _, alias := range character.Aliases {
			byName[alias] = character
		}
	}
	activeIDs := append([]string(nil), workset.Diagnostics.RequestedIDs...)
	requestedSet := make(map[string]struct{}, len(activeIDs))
	for _, id := range activeIDs {
		requestedSet[id] = struct{}{}
	}
	if relationships, relErr := t.store.World.LoadRelationships(); relErr == nil {
		for _, relation := range relationships {
			sourceID := relation.SourceCharacterID
			if sourceID == "" {
				sourceID = byName[relation.CharacterA].ID
			}
			targetID := relation.TargetCharacterID
			if targetID == "" {
				targetID = byName[relation.CharacterB].ID
			}
			_, sourceRequested := requestedSet[sourceID]
			_, targetRequested := requestedSet[targetID]
			if sourceRequested || targetRequested {
				activeIDs = append(activeIDs, sourceID, targetID)
			}
		}
	}
	if changes, stateErr := t.store.World.LoadStateChanges(); stateErr == nil {
		for _, change := range changes {
			if change.Chapter < max(1, chapter-2) || change.Chapter >= chapter {
				continue
			}
			id := change.CharacterID
			if id == "" {
				id = byName[change.Entity].ID
			}
			activeIDs = append(activeIDs, id)
		}
	}
	// The protagonist remains actionable even when an older outline omitted it.
	for _, character := range characters {
		if normalizeCharacterTier(character.Tier) == "core" {
			activeIDs = append(activeIDs, character.ID)
			break
		}
	}
	activeIDs = stableUniqueIDs(activeIDs)
	for _, id := range activeIDs {
		if character, ok := byID[id]; ok {
			workset.Full = append(workset.Full, character)
		}
	}
	sort.SliceStable(workset.Full, func(i, j int) bool {
		return characterPriority(workset.Full[i], workset.Diagnostics.RequestedIDs) <
			characterPriority(workset.Full[j], workset.Diagnostics.RequestedIDs)
	})
	if len(workset.Full) > characterWorksetMaxFullCards {
		for _, character := range workset.Full[characterWorksetMaxFullCards:] {
			workset.Diagnostics.TrimmedIDs = append(workset.Diagnostics.TrimmedIDs, character.ID)
		}
		workset.Full = workset.Full[:characterWorksetMaxFullCards]
	}

	fullIDs := make(map[string]struct{}, len(workset.Full))
	for _, character := range workset.Full {
		fullIDs[character.ID] = struct{}{}
		workset.Diagnostics.FullIDs = append(workset.Diagnostics.FullIDs, character.ID)
	}
	for _, character := range characters {
		if _, full := fullIDs[character.ID]; full {
			continue
		}
		tier := normalizeCharacterTier(character.Tier)
		if tier != "core" && tier != "important" {
			continue
		}
		if len(workset.Compressed) >= characterWorksetMaxCompressed {
			workset.Diagnostics.TrimmedIDs = append(workset.Diagnostics.TrimmedIDs, character.ID)
			continue
		}
		workset.Compressed = append(workset.Compressed, compactCharacterIdentity(character))
		workset.Diagnostics.CompressedIDs = append(workset.Diagnostics.CompressedIDs, character.ID)
	}
	workset.attachDynamicCharacterState(t)
	workset.enforceBudget()
	return workset, nil
}

func outlineCharacterIDs(entry domain.OutlineEntry) []string {
	ids := append([]string(nil), entry.CharacterIDs...)
	for _, beat := range entry.CharacterBeats {
		ids = append(ids, beat.CharacterID)
	}
	for _, beat := range entry.RelationshipBeats {
		ids = append(ids, beat.SourceCharacterID, beat.TargetCharacterID)
	}
	return stableUniqueIDs(ids)
}

func stableUniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func normalizeCharacterTier(tier string) string {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" {
		return "important"
	}
	return tier
}

func characterPriority(character domain.Character, requested []string) int {
	for index, id := range requested {
		if id == character.ID {
			return index
		}
	}
	if normalizeCharacterTier(character.Tier) == "core" {
		return len(requested)
	}
	return len(requested) + 1
}

func compactCharacterIdentity(character domain.Character) characterIdentity {
	return characterIdentity{
		ID: character.ID, Name: character.Name,
		Aliases: compactStringList(character.Aliases, 4, 30),
		Tier:    character.Tier, Role: truncateRunes(character.Role, 80),
		Goal:        truncateRunes(character.Goal, 100),
		Constraints: compactStringList(character.Constraints, 4, 80),
	}
}

func (workset *characterWorkset) attachDynamicCharacterState(t *ContextTool) {
	if workset == nil || t == nil {
		return
	}
	fullByID := make(map[string]domain.Character, len(workset.Full))
	fullByName := make(map[string]domain.Character, len(workset.Full))
	for _, character := range workset.Full {
		fullByID[character.ID] = character
		fullByName[character.Name] = character
	}
	snapshots, err := t.store.Characters.LoadLatestSnapshots()
	if err == nil {
		for _, snapshot := range snapshots {
			character, ok := fullByID[snapshot.CharacterID]
			if !ok && snapshot.CharacterID == "" {
				character, ok = fullByName[snapshot.Name]
			}
			if !ok {
				continue
			}
			if snapshot.CharacterID == "" {
				snapshot.CharacterID = character.ID
			}
			workset.Snapshots = append(workset.Snapshots, snapshot)
			if snapshot.Name != "" && snapshot.Name != character.Name {
				workset.Conflicts = append(workset.Conflicts, characterConflict{
					CharacterID: character.ID, Field: "name",
					StaticValue: truncateRunes(character.Name, 160), DynamicValue: truncateRunes(snapshot.Name, 160),
					Reason: "dynamic snapshot cannot rename the static character card",
				})
			}
		}
	}
	changes, err := t.store.World.LoadStateChanges()
	if err != nil {
		return
	}
	for _, change := range changes {
		character, ok := fullByID[change.CharacterID]
		if !ok && change.CharacterID == "" {
			character, ok = fullByName[change.Entity]
		}
		if !ok || !isStaticCharacterField(change.Field) {
			continue
		}
		workset.Conflicts = append(workset.Conflicts, characterConflict{
			CharacterID: character.ID, Field: change.Field,
			DynamicValue: truncateRunes(change.NewValue, 160),
			Reason:       "runtime state attempted to override a static character field; Character Agent revision required",
		})
	}
}

func isStaticCharacterField(field string) bool {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "name", "identity", "role", "traits", "arc", "voice", "constraints", "goal", "long_term_motivation", "knowledge_boundary":
		return true
	default:
		return false
	}
}

func (workset *characterWorkset) enforceBudget() {
	if workset == nil {
		return
	}
	for {
		data, _ := json.Marshal(workset)
		workset.Diagnostics.EncodedBytes = len(data)
		if len(data) <= characterWorksetBudgetBytes {
			return
		}
		if len(workset.Full) > 1 {
			last := workset.Full[len(workset.Full)-1]
			workset.Full = workset.Full[:len(workset.Full)-1]
			workset.Diagnostics.FullIDs = workset.Diagnostics.FullIDs[:len(workset.Diagnostics.FullIDs)-1]
			workset.Diagnostics.TrimmedIDs = append(workset.Diagnostics.TrimmedIDs, last.ID)
			if len(workset.Compressed) < characterWorksetMaxCompressed {
				workset.Compressed = append(workset.Compressed, compactCharacterIdentity(last))
				workset.Diagnostics.CompressedIDs = append(workset.Diagnostics.CompressedIDs, last.ID)
			}
			filtered := workset.Snapshots[:0]
			for _, snapshot := range workset.Snapshots {
				if snapshot.CharacterID != last.ID {
					filtered = append(filtered, snapshot)
				}
			}
			workset.Snapshots = filtered
			continue
		}
		if len(workset.Compressed) > 0 {
			last := workset.Compressed[len(workset.Compressed)-1]
			workset.Compressed = workset.Compressed[:len(workset.Compressed)-1]
			workset.Diagnostics.CompressedIDs = workset.Diagnostics.CompressedIDs[:len(workset.Diagnostics.CompressedIDs)-1]
			workset.Diagnostics.TrimmedIDs = append(workset.Diagnostics.TrimmedIDs, last.ID)
			continue
		}
		if len(workset.Snapshots) > 0 {
			workset.Snapshots = workset.Snapshots[:len(workset.Snapshots)-1]
			continue
		}
		if len(workset.Conflicts) > 1 {
			workset.Conflicts = workset.Conflicts[:len(workset.Conflicts)-1]
			continue
		}
		if len(workset.Full) == 1 && !containsString(workset.Diagnostics.CompactedIDs, workset.Full[0].ID) {
			workset.Full[0] = compactFullCharacter(workset.Full[0])
			workset.Diagnostics.CompactedIDs = append(workset.Diagnostics.CompactedIDs, workset.Full[0].ID)
			continue
		}
		return
	}
}

func compactFullCharacter(character domain.Character) domain.Character {
	character.Name = truncateRunes(character.Name, 80)
	character.Aliases = compactStringList(character.Aliases, 4, 60)
	character.Role = truncateRunes(character.Role, 120)
	character.Description = truncateRunes(character.Description, 240)
	character.Arc = truncateRunes(character.Arc, 160)
	character.Traits = compactStringList(character.Traits, 6, 60)
	character.Faction = truncateRunes(character.Faction, 80)
	character.Goal = truncateRunes(character.Goal, 160)
	character.Motivation = truncateRunes(character.Motivation, 160)
	character.Conflict = truncateRunes(character.Conflict, 160)
	character.Voice = truncateRunes(character.Voice, 160)
	character.Constraints = compactStringList(character.Constraints, 6, 80)
	character.Notes = truncateRunes(character.Notes, 160)
	if len(character.ContrastDetails) > 3 {
		character.ContrastDetails = character.ContrastDetails[:3]
	}
	for index := range character.ContrastDetails {
		character.ContrastDetails[index].Surface = truncateRunes(character.ContrastDetails[index].Surface, 100)
		character.ContrastDetails[index].Depth = truncateRunes(character.ContrastDetails[index].Depth, 100)
	}
	if len(character.KeyBackstory) > 3 {
		character.KeyBackstory = character.KeyBackstory[:3]
	}
	for index := range character.KeyBackstory {
		character.KeyBackstory[index].Event = truncateRunes(character.KeyBackstory[index].Event, 100)
		character.KeyBackstory[index].Impact = truncateRunes(character.KeyBackstory[index].Impact, 100)
	}
	if character.InitialState != nil {
		state := *character.InitialState
		state.Identity = truncateRunes(state.Identity, 100)
		state.Situation = truncateRunes(state.Situation, 100)
		state.Emotion = truncateRunes(state.Emotion, 80)
		state.Resources = compactStringList(state.Resources, 4, 60)
		state.Relationships = truncateRunes(state.Relationships, 120)
		character.InitialState = &state
	}
	if character.KnowledgeBoundary != nil {
		boundary := *character.KnowledgeBoundary
		boundary.Known = compactStringList(boundary.Known, 6, 80)
		boundary.Unknown = compactStringList(boundary.Unknown, 6, 80)
		boundary.Misconceptions = compactStringList(boundary.Misconceptions, 4, 80)
		boundary.Forbidden = compactStringList(boundary.Forbidden, 4, 80)
		character.KnowledgeBoundary = &boundary
	}
	return character
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
