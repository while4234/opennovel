package imp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	sourceOutlineArcSize          = 10
	DefaultFoundationMergeRunes   = 70000
	foundationPartialPremiseRunes = 2200
	foundationPartialFactRunes    = 320
)

type FoundationMergeBatchEvent struct {
	Index int
	Total int
	From  int
	To    int
	Final bool
}

func MergeFoundationFromReports(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	reports []domain.AdaptationSourceReport,
	opts StructuredCallOptions,
) (*FoundationResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("llm is nil")
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("no source reports to merge")
	}

	system := cleanLLMText(strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", len(reports))))
	user := cleanLLMText(buildFoundationMergeUserPrompt(reports))
	result, err := runStructuredCall(ctx, llm, []agentcore.Message{
		agentcore.SystemMsg(system),
		agentcore.UserMsg(user),
	}, parseFoundationMergeOutput, opts)
	if err != nil {
		return nil, err
	}
	result.Volumes = BuildSourceOutlineFromReports(reports)
	if got := len(domain.FlattenOutline(result.Volumes)); got != len(reports) {
		return nil, fmt.Errorf("generated source outline chapter count mismatch: got %d, want %d", got, len(reports))
	}
	if result.Compass != nil && result.Compass.LastUpdated == 0 {
		result.Compass.LastUpdated = len(reports)
	}
	return result, nil
}

func MergeFoundationFromReportsBatched(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	reports []domain.AdaptationSourceReport,
	opts StructuredCallOptions,
	batchRuneLimit int,
	onBatch func(FoundationMergeBatchEvent),
) (*FoundationResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("llm is nil")
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("no source reports to merge")
	}
	if batchRuneLimit <= 0 {
		batchRuneLimit = DefaultFoundationMergeRunes
	}

	batches := foundationMergeReportBatches(reports, batchRuneLimit, systemPrompt)
	if len(batches) <= 1 {
		if onBatch != nil {
			onBatch(FoundationMergeBatchEvent{
				Index: 1,
				Total: 1,
				From:  reports[0].Chapter,
				To:    reports[len(reports)-1].Chapter,
			})
		}
		return MergeFoundationFromReports(ctx, llm, systemPrompt, reports, opts)
	}

	partials := make([]FoundationMergePartial, 0, len(batches))
	for i, batch := range batches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if onBatch != nil {
			onBatch(FoundationMergeBatchEvent{
				Index: i + 1,
				Total: len(batches),
				From:  batch[0].Chapter,
				To:    batch[len(batch)-1].Chapter,
			})
		}
		result, err := MergeFoundationFromReports(ctx, llm, systemPrompt, batch, opts)
		if err != nil {
			return nil, fmt.Errorf("merge source foundation batch %d/%d (chapters %d-%d): %w",
				i+1, len(batches), batch[0].Chapter, batch[len(batch)-1].Chapter, err)
		}
		partials = append(partials, FoundationMergePartial{
			Index:  i + 1,
			From:   batch[0].Chapter,
			To:     batch[len(batch)-1].Chapter,
			Result: result,
		})
	}

	if onBatch != nil {
		onBatch(FoundationMergeBatchEvent{
			Index: len(batches) + 1,
			Total: len(batches) + 1,
			From:  reports[0].Chapter,
			To:    reports[len(reports)-1].Chapter,
			Final: true,
		})
	}
	result, err := MergeFoundationPartialsBatched(ctx, llm, systemPrompt, partials, len(reports), opts, batchRuneLimit, onBatch)
	if err != nil {
		return nil, err
	}
	result.Volumes = BuildSourceOutlineFromReports(reports)
	if got := len(domain.FlattenOutline(result.Volumes)); got != len(reports) {
		return nil, fmt.Errorf("generated source outline chapter count mismatch: got %d, want %d", got, len(reports))
	}
	if result.Compass != nil && result.Compass.LastUpdated == 0 {
		result.Compass.LastUpdated = len(reports)
	}
	return result, nil
}

type FoundationMergePartial struct {
	Index          int
	From           int
	To             int
	InputSignature string
	Result         *FoundationResult
}

func FoundationMergeReportBatches(reports []domain.AdaptationSourceReport, runeLimit int) [][]domain.AdaptationSourceReport {
	return foundationMergeReportBatches(reports, runeLimit, "")
}

// FoundationMergeReportBatchesForPrompt keeps each compiled merge request
// within both the model-profile rune budget and the structured-call byte cap.
func FoundationMergeReportBatchesForPrompt(reports []domain.AdaptationSourceReport, runeLimit int, systemPrompt string) [][]domain.AdaptationSourceReport {
	return foundationMergeReportBatches(reports, runeLimit, systemPrompt)
}

func foundationMergeReportBatches(reports []domain.AdaptationSourceReport, runeLimit int, systemPrompt string) [][]domain.AdaptationSourceReport {
	if runeLimit <= 0 {
		runeLimit = DefaultFoundationMergeRunes
	}
	var batches [][]domain.AdaptationSourceReport
	var current []domain.AdaptationSourceReport
	currentRunes := 0
	for _, report := range reports {
		reportRunes := foundationMergeReportRunes(report)
		candidate := appendReportCandidate(current, report)
		exceedsRunes := currentRunes+reportRunes > runeLimit
		exceedsBytes := foundationMergeReportRequestBytes(systemPrompt, candidate) > structuredInputLimitBytes
		if len(current) > 0 && (exceedsRunes || exceedsBytes) {
			batches = append(batches, current)
			current = nil
			currentRunes = 0
		}
		current = append(current, report)
		currentRunes += reportRunes
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func appendReportCandidate(current []domain.AdaptationSourceReport, report domain.AdaptationSourceReport) []domain.AdaptationSourceReport {
	candidate := make([]domain.AdaptationSourceReport, len(current), len(current)+1)
	copy(candidate, current)
	return append(candidate, report)
}

func foundationMergeReportRequestBytes(systemPrompt string, reports []domain.AdaptationSourceReport) int {
	system := cleanLLMText(strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", len(reports))))
	user := cleanLLMText(buildFoundationMergeUserPrompt(reports))
	return structuredInputBytes([]agentcore.Message{
		agentcore.SystemMsg(system),
		agentcore.UserMsg(user),
	})
}

func foundationMergeReportRunes(report domain.AdaptationSourceReport) int {
	var sb strings.Builder
	writeFoundationMergeReport(&sb, report)
	return utf8.RuneCountInString(sb.String())
}

func MergeFoundationPartialsBatched(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	partials []FoundationMergePartial,
	totalReports int,
	opts StructuredCallOptions,
	batchRuneLimit int,
	onBatch func(FoundationMergeBatchEvent),
) (*FoundationResult, error) {
	if len(partials) == 0 {
		return nil, fmt.Errorf("no source foundation batches to merge")
	}
	if len(partials) == 1 {
		return partials[0].Result, nil
	}
	if batchRuneLimit <= 0 {
		batchRuneLimit = DefaultFoundationMergeRunes
	}

	level := 1
	current := partials
	for len(current) > 1 {
		groups := foundationMergePartialBatches(current, batchRuneLimit, systemPrompt, totalReports)
		if len(groups) == 1 {
			result, err := MergeFoundationPartials(ctx, llm, systemPrompt, groups[0], totalReports, opts)
			if err != nil {
				return nil, err
			}
			return result, nil
		}
		next := make([]FoundationMergePartial, 0, len(groups))
		for i, group := range groups {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if onBatch != nil {
				onBatch(FoundationMergeBatchEvent{
					Index: i + 1,
					Total: len(groups),
					From:  group[0].From,
					To:    group[len(group)-1].To,
					Final: true,
				})
			}
			result, err := MergeFoundationPartials(ctx, llm, systemPrompt, group, totalReports, opts)
			if err != nil {
				return nil, fmt.Errorf("merge source foundation summary level %d batch %d/%d (chapters %d-%d): %w",
					level, i+1, len(groups), group[0].From, group[len(group)-1].To, err)
			}
			next = append(next, FoundationMergePartial{
				Index:  i + 1,
				From:   group[0].From,
				To:     group[len(group)-1].To,
				Result: result,
			})
		}
		current = next
		level++
	}
	return current[0].Result, nil
}

func FoundationMergePartialBatches(partials []FoundationMergePartial, runeLimit int) [][]FoundationMergePartial {
	return foundationMergePartialBatches(partials, runeLimit, "", len(partials))
}

// FoundationMergePartialBatchesForPrompt keeps recursive summary requests
// within the same compiled-input limits as the actual structured call.
func FoundationMergePartialBatchesForPrompt(partials []FoundationMergePartial, runeLimit int, systemPrompt string, totalReports int) [][]FoundationMergePartial {
	return foundationMergePartialBatches(partials, runeLimit, systemPrompt, totalReports)
}

func foundationMergePartialBatches(partials []FoundationMergePartial, runeLimit int, systemPrompt string, totalReports int) [][]FoundationMergePartial {
	if runeLimit <= 0 {
		runeLimit = DefaultFoundationMergeRunes
	}
	var batches [][]FoundationMergePartial
	var current []FoundationMergePartial
	currentRunes := 0
	for _, partial := range partials {
		partialRunes := foundationMergePartialRunes(partial)
		candidate := appendPartialCandidate(current, partial)
		exceedsRunes := currentRunes+partialRunes > runeLimit
		exceedsBytes := foundationMergePartialRequestBytes(systemPrompt, candidate, totalReports) > structuredInputLimitBytes
		if len(current) > 0 && (exceedsRunes || exceedsBytes) {
			batches = append(batches, current)
			current = nil
			currentRunes = 0
		}
		current = append(current, partial)
		currentRunes += partialRunes
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func appendPartialCandidate(current []FoundationMergePartial, partial FoundationMergePartial) []FoundationMergePartial {
	candidate := make([]FoundationMergePartial, len(current), len(current)+1)
	copy(candidate, current)
	return append(candidate, partial)
}

func foundationMergePartialRequestBytes(systemPrompt string, partials []FoundationMergePartial, totalReports int) int {
	system := cleanLLMText(strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", totalReports)))
	user := cleanLLMText(buildFoundationPartialMergeUserPrompt(partials, totalReports))
	return structuredInputBytes([]agentcore.Message{
		agentcore.SystemMsg(system),
		agentcore.UserMsg(user),
	})
}

func foundationMergePartialRunes(partial FoundationMergePartial) int {
	if partial.Result == nil {
		return 0
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Partial %d: source chapters %d-%d\n", partial.Index, partial.From, partial.To)
	writeMergeFact(&sb, "Premise", compactFact(partial.Result.Premise, foundationPartialPremiseRunes))
	writePartialCharacters(&sb, partial.Result.Characters)
	writePartialRelationships(&sb, partial.Result.Relationships)
	writePartialWorldRules(&sb, partial.Result.WorldRules)
	writePartialCompass(&sb, partial.Result.Compass)
	return utf8.RuneCountInString(sb.String())
}

func MergeFoundationPartials(
	ctx context.Context,
	llm LLMChat,
	systemPrompt string,
	partials []FoundationMergePartial,
	totalReports int,
	opts StructuredCallOptions,
) (*FoundationResult, error) {
	if len(partials) == 0 {
		return nil, fmt.Errorf("no source foundation batches to merge")
	}
	system := cleanLLMText(strings.ReplaceAll(systemPrompt, "${chapter_count}", fmt.Sprintf("%d", totalReports)))
	user := cleanLLMText(buildFoundationPartialMergeUserPrompt(partials, totalReports))
	result, err := runStructuredCall(ctx, llm, []agentcore.Message{
		agentcore.SystemMsg(system),
		agentcore.UserMsg(user),
	}, parseFoundationMergeOutput, opts)
	if err != nil {
		return nil, fmt.Errorf("merge source foundation batch summaries: %w", err)
	}
	return result, nil
}

// MergeFoundationPartialsDeterministic assembles already validated batch
// foundations without asking one final model response to repeat the whole
// source book. Its memory grows with extracted settings, while each model call
// remains bounded by the report-batch limits used to produce the partials.
func MergeFoundationPartialsDeterministic(partials []FoundationMergePartial, totalReports int) (*FoundationResult, error) {
	if len(partials) == 0 {
		return nil, fmt.Errorf("no source foundation batches to assemble")
	}
	if len(partials) == 1 {
		return partials[0].Result, nil
	}

	result := &FoundationResult{
		Premise: buildDeterministicSourcePremise(partials),
	}
	characterKeys := make(map[string]int)
	usedCharacterIDs := make(map[string]bool)
	endpointIDs := make(map[string]string)

	for partialIndex, partial := range partials {
		if partial.Result == nil {
			continue
		}
		for _, incoming := range partial.Result.Characters {
			originalID := incoming.ID
			index := findMergedCharacter(characterKeys, incoming)
			if index < 0 {
				incoming.ID = uniqueFoundationID(incoming.ID, "source-character", usedCharacterIDs)
				index = len(result.Characters)
				result.Characters = append(result.Characters, incoming)
			} else {
				result.Characters[index] = mergeFoundationCharacter(result.Characters[index], incoming)
			}
			registerMergedCharacterKeys(characterKeys, result.Characters[index], index)
			endpointIDs[foundationEndpointKey(partialIndex, originalID)] = result.Characters[index].ID
		}
	}
	if err := validateSourceFoundationCharacters(result.Characters); err != nil {
		return nil, fmt.Errorf("assemble source characters: %w", err)
	}

	usedRelationshipIDs := make(map[string]bool)
	relationshipKeys := make(map[string]int)
	worldRuleKeys := make(map[string]int)
	for partialIndex, partial := range partials {
		if partial.Result == nil {
			continue
		}
		for _, incoming := range partial.Result.Relationships {
			sourceID := endpointIDs[foundationEndpointKey(partialIndex, incoming.SourceCharacterID)]
			targetID := endpointIDs[foundationEndpointKey(partialIndex, incoming.TargetCharacterID)]
			if sourceID == "" || targetID == "" {
				return nil, fmt.Errorf("assemble relationship %q: unresolved source endpoint", incoming.ID)
			}
			incoming.SourceCharacterID = sourceID
			incoming.TargetCharacterID = targetID
			key := foundationRelationshipIdentity(incoming)
			if index, ok := relationshipKeys[key]; ok {
				result.Relationships[index] = mergeFoundationRelationship(result.Relationships[index], incoming)
				continue
			}
			incoming.ID = uniqueFoundationID(incoming.ID, "source-relationship", usedRelationshipIDs)
			relationshipKeys[key] = len(result.Relationships)
			result.Relationships = append(result.Relationships, incoming)
		}
		for _, incoming := range partial.Result.WorldRules {
			key := normalizeFoundationIdentity(incoming.Category + "\x00" + incoming.Rule)
			if index, ok := worldRuleKeys[key]; ok {
				result.WorldRules[index] = mergeFoundationWorldRule(result.WorldRules[index], incoming)
				continue
			}
			worldRuleKeys[key] = len(result.WorldRules)
			result.WorldRules = append(result.WorldRules, incoming)
		}
		result.Compass = mergeFoundationCompass(result.Compass, partial.Result.Compass)
	}
	if err := validateSourceFoundationRelationships(result.Characters, result.Relationships); err != nil {
		return nil, fmt.Errorf("assemble source relationships: %w", err)
	}
	if result.Compass != nil {
		result.Compass.LastUpdated = totalReports
	}
	return result, nil
}

func buildDeterministicSourcePremise(partials []FoundationMergePartial) string {
	var sections []string
	seen := make(map[string]bool)
	for _, partial := range partials {
		if partial.Result == nil {
			continue
		}
		body := strings.TrimSpace(partial.Result.Premise)
		if newline := strings.IndexByte(body, '\n'); strings.HasPrefix(body, "#") && newline >= 0 {
			body = strings.TrimSpace(body[newline+1:])
		}
		key := normalizeFoundationIdentity(body)
		if body == "" || seen[key] {
			continue
		}
		seen[key] = true
		sections = append(sections, fmt.Sprintf("## 原著第 %d-%d 章\n%s", partial.From, partial.To, body))
	}
	return "# 原著全书设定\n\n" + strings.Join(sections, "\n\n")
}

func findMergedCharacter(keys map[string]int, character domain.Character) int {
	for _, key := range foundationCharacterIdentityKeys(character) {
		if index, ok := keys[key]; ok {
			return index
		}
	}
	return -1
}

func registerMergedCharacterKeys(keys map[string]int, character domain.Character, index int) {
	for _, key := range foundationCharacterIdentityKeys(character) {
		keys[key] = index
	}
}

func foundationCharacterIdentityKeys(character domain.Character) []string {
	var keys []string
	if id := normalizeFoundationIdentity(character.ID); id != "" {
		keys = append(keys, "id:"+id)
	}
	if normalized := normalizeFoundationIdentity(character.Name); normalized != "" {
		keys = append(keys, "name:"+normalized)
	}
	return keys
}

func normalizeFoundationIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), ""))
}

func uniqueFoundationID(candidate, prefix string, used map[string]bool) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		candidate = prefix
	}
	if !used[candidate] {
		used[candidate] = true
		return candidate
	}
	for suffix := 2; ; suffix++ {
		next := fmt.Sprintf("%s-%d", candidate, suffix)
		if !used[next] {
			used[next] = true
			return next
		}
	}
}

func foundationEndpointKey(partialIndex int, id string) string {
	return fmt.Sprintf("%d:%s", partialIndex, strings.TrimSpace(id))
}

func mergeFoundationCharacter(current, incoming domain.Character) domain.Character {
	current.Aliases = appendUniqueFoundationStrings(current.Aliases, incoming.Aliases...)
	current.Role = mergeFoundationText(current.Role, incoming.Role)
	current.Description = mergeFoundationText(current.Description, incoming.Description)
	current.Arc = mergeFoundationText(current.Arc, incoming.Arc)
	current.Traits = appendUniqueFoundationStrings(current.Traits, incoming.Traits...)
	current.Faction = mergeFoundationText(current.Faction, incoming.Faction)
	current.Goal = mergeFoundationText(current.Goal, incoming.Goal)
	current.Motivation = mergeFoundationText(current.Motivation, incoming.Motivation)
	current.Conflict = mergeFoundationText(current.Conflict, incoming.Conflict)
	current.Voice = mergeFoundationText(current.Voice, incoming.Voice)
	current.Constraints = appendUniqueFoundationStrings(current.Constraints, incoming.Constraints...)
	current.Notes = mergeFoundationText(current.Notes, incoming.Notes)
	current.Tier = strongerFoundationTier(current.Tier, incoming.Tier)
	current.Gender = mergeFoundationGender(current.Gender, incoming.Gender, &current.Notes)
	current.ContrastDetails = appendUniqueContrastDetails(current.ContrastDetails, incoming.ContrastDetails...)
	current.KeyBackstory = appendUniqueBackstory(current.KeyBackstory, incoming.KeyBackstory...)
	current.InitialState = mergeFoundationInitialState(current.InitialState, incoming.InitialState)
	current.KnowledgeBoundary = mergeFoundationKnowledgeBoundary(current.KnowledgeBoundary, incoming.KnowledgeBoundary)
	return current
}

func mergeFoundationText(current, incoming string) string {
	current = strings.TrimSpace(current)
	incoming = strings.TrimSpace(incoming)
	if current == "" {
		return incoming
	}
	if incoming == "" || strings.Contains(current, incoming) {
		return current
	}
	if strings.Contains(incoming, current) {
		return incoming
	}
	return current + "；" + incoming
}

func appendUniqueFoundationStrings(current []string, incoming ...string) []string {
	seen := make(map[string]bool, len(current)+len(incoming))
	for _, value := range current {
		seen[normalizeFoundationIdentity(value)] = true
	}
	for _, value := range incoming {
		value = strings.TrimSpace(value)
		key := normalizeFoundationIdentity(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		current = append(current, value)
	}
	return current
}

func strongerFoundationTier(current, incoming string) string {
	rank := map[string]int{"decorative": 1, "secondary": 2, "important": 3, "core": 4}
	if rank[incoming] > rank[current] {
		return incoming
	}
	if current == "" {
		return incoming
	}
	return current
}

func mergeFoundationGender(current, incoming string, notes *string) string {
	current = strings.ToLower(strings.TrimSpace(current))
	incoming = strings.ToLower(strings.TrimSpace(incoming))
	if incoming == "" {
		return current
	}
	if current == "" || current == "unspecified" {
		return incoming
	}
	if incoming == "" || incoming == "unspecified" || incoming == current {
		return current
	}
	*notes = mergeFoundationText(*notes, "跨批次性别证据冲突，保持未明确并要求使用姓名或身份称谓。")
	return "unspecified"
}

func appendUniqueContrastDetails(current []domain.CharacterContrastDetail, incoming ...domain.CharacterContrastDetail) []domain.CharacterContrastDetail {
	seen := make(map[string]bool, len(current)+len(incoming))
	for _, value := range current {
		seen[normalizeFoundationIdentity(value.Surface+"\x00"+value.Depth)] = true
	}
	for _, value := range incoming {
		key := normalizeFoundationIdentity(value.Surface + "\x00" + value.Depth)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		current = append(current, value)
	}
	return current
}

func appendUniqueBackstory(current []domain.CharacterBackstory, incoming ...domain.CharacterBackstory) []domain.CharacterBackstory {
	seen := make(map[string]bool, len(current)+len(incoming))
	for _, value := range current {
		seen[normalizeFoundationIdentity(value.Event+"\x00"+value.Impact)] = true
	}
	for _, value := range incoming {
		key := normalizeFoundationIdentity(value.Event + "\x00" + value.Impact)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		current = append(current, value)
	}
	return current
}

func mergeFoundationInitialState(current, incoming *domain.CharacterInitialState) *domain.CharacterInitialState {
	if current == nil {
		return incoming
	}
	if incoming == nil {
		return current
	}
	current.Identity = mergeFoundationText(current.Identity, incoming.Identity)
	current.Situation = mergeFoundationText(current.Situation, incoming.Situation)
	current.Emotion = mergeFoundationText(current.Emotion, incoming.Emotion)
	current.Resources = appendUniqueFoundationStrings(current.Resources, incoming.Resources...)
	current.Relationships = mergeFoundationText(current.Relationships, incoming.Relationships)
	return current
}

func mergeFoundationKnowledgeBoundary(current, incoming *domain.CharacterKnowledgeBoundary) *domain.CharacterKnowledgeBoundary {
	if current == nil {
		return incoming
	}
	if incoming == nil {
		return current
	}
	current.Known = appendUniqueFoundationStrings(current.Known, incoming.Known...)
	current.Unknown = appendUniqueFoundationStrings(current.Unknown, incoming.Unknown...)
	current.Misconceptions = appendUniqueFoundationStrings(current.Misconceptions, incoming.Misconceptions...)
	current.Forbidden = appendUniqueFoundationStrings(current.Forbidden, incoming.Forbidden...)
	return current
}

func foundationRelationshipIdentity(value domain.CharacterRelationship) string {
	sourceID, targetID := value.SourceCharacterID, value.TargetCharacterID
	if value.Direction == domain.RelationshipDirectionBidirectional || value.Direction == domain.RelationshipDirectionUndirected {
		if sourceID > targetID {
			sourceID, targetID = targetID, sourceID
		}
	}
	return normalizeFoundationIdentity(strings.Join([]string{sourceID, targetID, string(value.Type), string(value.Direction)}, "\x00"))
}

func mergeFoundationRelationship(current, incoming domain.CharacterRelationship) domain.CharacterRelationship {
	current.Label = mergeFoundationText(current.Label, incoming.Label)
	current.Description = mergeFoundationText(current.Description, incoming.Description)
	current.Since = mergeFoundationText(current.Since, incoming.Since)
	current.Tags = appendUniqueFoundationStrings(current.Tags, incoming.Tags...)
	current.Constraints = appendUniqueFoundationStrings(current.Constraints, incoming.Constraints...)
	if current.Status == "" || incoming.Status == domain.RelationshipStatusBroken || incoming.Status == domain.RelationshipStatusResolved {
		current.Status = incoming.Status
	}
	return current
}

func mergeFoundationWorldRule(current, incoming domain.WorldRule) domain.WorldRule {
	current.Title = mergeFoundationText(current.Title, incoming.Title)
	current.Boundary = mergeFoundationText(current.Boundary, incoming.Boundary)
	current.Tags = appendUniqueFoundationStrings(current.Tags, incoming.Tags...)
	if incoming.Strength == domain.WorldRuleStrengthHard {
		current.Strength = incoming.Strength
	}
	if incoming.Priority > current.Priority {
		current.Priority = incoming.Priority
	}
	return current
}

func mergeFoundationCompass(current, incoming *domain.StoryCompass) *domain.StoryCompass {
	if current == nil {
		return incoming
	}
	if incoming == nil {
		return current
	}
	current.EndingDirection = mergeFoundationText(current.EndingDirection, incoming.EndingDirection)
	current.OpenThreads = appendUniqueFoundationStrings(current.OpenThreads, incoming.OpenThreads...)
	if strings.TrimSpace(incoming.EstimatedScale) != "" {
		current.EstimatedScale = incoming.EstimatedScale
	}
	return current
}

func buildFoundationMergeUserPrompt(reports []domain.AdaptationSourceReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The following %d items are compact source-chapter fact reports. They are not original prose.\n", len(reports))
	sb.WriteString("Merge only these facts into a foundation. Preserve causal order and uncertainty.\n\n")
	for _, report := range reports {
		writeFoundationMergeReport(&sb, report)
	}
	return sb.String()
}

func writeFoundationMergeReport(sb *strings.Builder, report domain.AdaptationSourceReport) {
	title := strings.TrimSpace(report.Title)
	if title == "" {
		title = fmt.Sprintf("Chapter %d", report.Chapter)
	}
	fmt.Fprintf(sb, "## Chapter %d: %s\n", report.Chapter, cleanLLMText(title))
	writeMergeFact(sb, "Summary", report.Summary)
	writeMergeList(sb, "Appearing characters", report.Characters)
	writeMergeCharacterProfiles(sb, report.CharacterProfiles)
	writeMergeList(sb, "Character facts", report.CharacterFacts)
	writeMergeList(sb, "Key events", report.KeyEvents)
	writeMergeList(sb, "World rules", report.WorldRules)
	writeMergeFact(sb, "Hook type", report.HookType)
	writeMergeFact(sb, "Dominant strand", report.DominantStrand)
	writeTimelineFacts(sb, report.Timeline)
	writeForeshadowFacts(sb, report.Foreshadow)
	writeRelationshipFacts(sb, report.Relationships)
	writeStateChangeFacts(sb, report.StateChanges)
	sb.WriteString("\n")
}

func buildFoundationPartialMergeUserPrompt(partials []FoundationMergePartial, totalReports int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The source novel has %d chapter reports. They were merged into %d consecutive partial foundations to keep each request small.\n", totalReports, len(partials))
	sb.WriteString("Merge these partial foundations into one all-book foundation. Preserve source causal order and keep only facts supported by the partial foundations.\n")
	sb.WriteString("Return the same required === PREMISE ===, === CHARACTERS ===, === RELATIONSHIPS ===, === WORLD_RULES ===, and === COMPASS === sections.\n\n")
	sb.WriteString("Final-output budget: keep the response below 12000 Chinese characters so every JSON section closes correctly. ")
	sb.WriteString("Keep every recurring or narratively consequential character, but collapse aliases and omit unnamed walk-ons with no independent setting value. ")
	sb.WriteString("Use at most 12 characters, 20 relationships, and 15 deduplicated world rules. ")
	sb.WriteString("Core/important characters retain all evidence-backed fields; keep each scalar concise, each list to the strongest 5 facts, and contrast_details/key_backstory to the strongest 3 pairs. ")
	sb.WriteString("Do not spend the budget restating the same event in description, notes, relationships, and world rules.\n\n")
	for _, partial := range partials {
		result := partial.Result
		if result == nil {
			continue
		}
		fmt.Fprintf(&sb, "## Partial %d: source chapters %d-%d\n", partial.Index, partial.From, partial.To)
		writeMergeFact(&sb, "Premise", compactFact(result.Premise, foundationPartialPremiseRunes))
		writePartialCharacters(&sb, result.Characters)
		writePartialRelationships(&sb, result.Relationships)
		writePartialWorldRules(&sb, result.WorldRules)
		writePartialCompass(&sb, result.Compass)
		sb.WriteString("\n")
	}
	return sb.String()
}

func writeMergeCharacterProfiles(sb *strings.Builder, characters []domain.Character) {
	if len(characters) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Structured character profiles:")
	for _, character := range characters {
		encoded, err := json.Marshal(character)
		if err != nil {
			continue
		}
		fmt.Fprintf(sb, "  - %s\n", cleanLLMText(string(encoded)))
	}
}

func writePartialCharacters(sb *strings.Builder, characters []domain.Character) {
	if len(characters) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Characters:")
	for _, character := range characters {
		name := compactFact(character.Name, 80)
		if name == "" {
			continue
		}
		role := compactFact(character.Role, 80)
		fmt.Fprintf(sb, "  - %s", name)
		if len(character.Aliases) > 0 {
			fmt.Fprintf(sb, " [aliases: %s]", strings.Join(compactList(character.Aliases, 8, 80), ", "))
		}
		if role != "" {
			fmt.Fprintf(sb, " (%s)", role)
		}
		if gender := compactFact(character.Gender, 40); gender != "" {
			fmt.Fprintf(sb, " / gender: %s", gender)
		}
		if tier := compactFact(character.Tier, 40); tier != "" {
			fmt.Fprintf(sb, " / tier: %s", tier)
		}
		for _, fact := range []struct {
			label string
			value string
		}{
			{"description", character.Description},
			{"arc-so-far", character.Arc},
			{"goal", character.Goal},
			{"motivation", character.Motivation},
			{"conflict", character.Conflict},
			{"voice", character.Voice},
			{"notes", character.Notes},
		} {
			if value := compactFact(fact.value, foundationPartialFactRunes); value != "" {
				fmt.Fprintf(sb, " / %s: %s", fact.label, value)
			}
		}
		if len(character.Traits) > 0 {
			fmt.Fprintf(sb, " / traits: %s", strings.Join(compactList(character.Traits, 8, 80), ", "))
		}
		if len(character.Constraints) > 0 {
			fmt.Fprintf(sb, " / constraints: %s", strings.Join(compactList(character.Constraints, 8, 100), ", "))
		}
		writePartialContrastAndBackstory(sb, character)
		fmt.Fprintln(sb)
	}
}

func writePartialContrastAndBackstory(sb *strings.Builder, character domain.Character) {
	for _, detail := range character.ContrastDetails {
		if surface, depth := compactFact(detail.Surface, 120), compactFact(detail.Depth, 160); surface != "" || depth != "" {
			fmt.Fprintf(sb, " / contrast: %s => %s", surface, depth)
		}
	}
	for _, item := range character.KeyBackstory {
		if event, impact := compactFact(item.Event, 160), compactFact(item.Impact, 180); event != "" || impact != "" {
			fmt.Fprintf(sb, " / backstory: %s => %s", event, impact)
		}
	}
}

func writePartialRelationships(sb *strings.Builder, relationships []domain.CharacterRelationship) {
	if len(relationships) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Source relationships:")
	for _, relationship := range relationships {
		encoded, err := json.Marshal(relationship)
		if err == nil {
			fmt.Fprintf(sb, "  - %s\n", cleanLLMText(string(encoded)))
		}
	}
}

func writePartialWorldRules(sb *strings.Builder, rules []domain.WorldRule) {
	if len(rules) == 0 {
		return
	}
	fmt.Fprintln(sb, "- World rules:")
	for _, rule := range rules {
		line := compactFact(rule.Rule, foundationPartialFactRunes)
		if line == "" {
			continue
		}
		if strings.TrimSpace(rule.Category) != "" {
			line = compactFact(rule.Category, 80) + ": " + line
		}
		if strings.TrimSpace(rule.Boundary) != "" {
			line += " / boundary: " + compactFact(rule.Boundary, 180)
		}
		fmt.Fprintf(sb, "  - %s\n", line)
	}
}

func writePartialCompass(sb *strings.Builder, compass *domain.StoryCompass) {
	if compass == nil {
		return
	}
	fmt.Fprintln(sb, "- Compass:")
	writeMergeFact(sb, "Ending direction", compass.EndingDirection)
	writeMergeList(sb, "Open threads", compass.OpenThreads)
	writeMergeFact(sb, "Estimated scale", compass.EstimatedScale)
}

func writeMergeFact(sb *strings.Builder, label, value string) {
	value = compactFact(value, 800)
	if value == "" {
		return
	}
	fmt.Fprintf(sb, "- %s: %s\n", label, value)
}

func writeMergeList(sb *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(sb, "- %s:\n", label)
	for _, value := range values {
		value = compactFact(value, 500)
		if value != "" {
			fmt.Fprintf(sb, "  - %s\n", value)
		}
	}
}

func writeTimelineFacts(sb *strings.Builder, events []domain.TimelineEvent) {
	if len(events) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Timeline:")
	for _, event := range events {
		line := compactFact(event.Event, 500)
		if line == "" {
			continue
		}
		if strings.TrimSpace(event.Time) != "" {
			line = compactFact(event.Time, 120) + ": " + line
		}
		if len(event.Characters) > 0 {
			line += " (" + strings.Join(event.Characters, ", ") + ")"
		}
		fmt.Fprintf(sb, "  - %s\n", line)
	}
}

func writeForeshadowFacts(sb *strings.Builder, updates []domain.ForeshadowUpdate) {
	if len(updates) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Foreshadow:")
	for _, update := range updates {
		line := compactFact(update.Action+" "+update.ID, 160)
		if desc := compactFact(update.Description, 400); desc != "" {
			line += ": " + desc
		}
		if strings.TrimSpace(line) != "" {
			fmt.Fprintf(sb, "  - %s\n", line)
		}
	}
}

func writeRelationshipFacts(sb *strings.Builder, relations []domain.RelationshipEntry) {
	if len(relations) == 0 {
		return
	}
	fmt.Fprintln(sb, "- Relationships:")
	for _, relation := range relations {
		line := strings.TrimSpace(relation.CharacterA + " / " + relation.CharacterB)
		if desc := compactFact(relation.Relation, 400); desc != "" {
			line += ": " + desc
		}
		if strings.TrimSpace(line) != "/" {
			fmt.Fprintf(sb, "  - %s\n", line)
		}
	}
}

func writeStateChangeFacts(sb *strings.Builder, changes []domain.StateChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(sb, "- State changes:")
	for _, change := range changes {
		entity := compactFact(change.Entity, 120)
		field := compactFact(change.Field, 80)
		next := compactFact(change.NewValue, 260)
		if entity == "" || field == "" || next == "" {
			continue
		}
		line := fmt.Sprintf("%s.%s -> %s", entity, field, next)
		if reason := compactFact(change.Reason, 300); reason != "" {
			line += " because " + reason
		}
		fmt.Fprintf(sb, "  - %s\n", line)
	}
}

func parseFoundationMergeOutput(text string) (*FoundationResult, error) {
	text = cleanLLMText(text)
	env := parseTaggedEnvelope(text)
	if env == nil {
		return nil, fmt.Errorf("no === TAG === envelope found in foundation merge output")
	}
	if err := requireTags(env, "PREMISE", "CHARACTERS", "RELATIONSHIPS", "WORLD_RULES"); err != nil {
		return nil, err
	}

	premise := stripFences(env["PREMISE"])
	if !strings.HasPrefix(strings.TrimLeft(premise, " \t\n"), "#") {
		return nil, fmt.Errorf("premise must start with a Markdown heading line")
	}

	var characters []domain.Character
	if err := decodeCharactersJSON("characters", env["CHARACTERS"], &characters, false); err != nil {
		return nil, err
	}
	if err := validateSourceFoundationCharacters(characters); err != nil {
		return nil, err
	}

	var relationships []domain.CharacterRelationship
	if err := decodeJSON("relationships", env["RELATIONSHIPS"], &relationships); err != nil {
		return nil, err
	}
	if err := validateSourceFoundationRelationships(characters, relationships); err != nil {
		return nil, err
	}

	var worldRules []domain.WorldRule
	if err := decodeWorldRulesJSON(env["WORLD_RULES"], &worldRules); err != nil {
		return nil, err
	}

	var compass *domain.StoryCompass
	if rawCompass, ok := env["COMPASS"]; ok && strings.TrimSpace(stripFences(rawCompass)) != "" {
		var decoded domain.StoryCompass
		if err := decodeJSON("compass", rawCompass, &decoded); err != nil {
			return nil, err
		}
		compass = &decoded
	}

	return &FoundationResult{
		Premise:       premise,
		Characters:    characters,
		Relationships: relationships,
		WorldRules:    worldRules,
		Compass:       compass,
	}, nil
}

func decodeWorldRulesJSON(raw string, out *[]domain.WorldRule) error {
	cleaned := strings.TrimSpace(stripFences(raw))
	var value any
	if err := json.Unmarshal([]byte(cleaned), &value); err != nil {
		return fmt.Errorf("parse world_rules JSON: %w", err)
	}
	var rules []domain.WorldRule
	if err := appendDecodedWorldRules(value, "", &rules); err != nil {
		return err
	}
	if len(rules) == 0 {
		return fmt.Errorf("world_rules requires at least one evidence-backed rule")
	}
	*out = rules
	return nil
}

func appendDecodedWorldRules(value any, titleHint string, out *[]domain.WorldRule) error {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if err := appendDecodedWorldRules(item, "", out); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, key := range []string{"world_rules", "rules"} {
			if nested, ok := typed[key]; ok {
				return appendDecodedWorldRules(nested, "", out)
			}
		}
		if _, hasRule := typed["rule"]; hasRule {
			encoded, err := json.Marshal(typed)
			if err != nil {
				return fmt.Errorf("encode world_rules object: %w", err)
			}
			var rule domain.WorldRule
			if err := json.Unmarshal(encoded, &rule); err != nil {
				return fmt.Errorf("parse world_rules object: %w", err)
			}
			rule.Rule = strings.TrimSpace(rule.Rule)
			if rule.Rule == "" {
				return fmt.Errorf("world_rules object requires a non-empty rule")
			}
			if rule.Title == "" {
				rule.Title = strings.TrimSpace(titleHint)
			}
			*out = append(*out, rule)
			return nil
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := appendDecodedWorldRules(typed[key], key, out); err != nil {
				return err
			}
		}
		return nil
	case string:
		rule := strings.TrimSpace(typed)
		if rule == "" {
			return nil
		}
		*out = append(*out, domain.WorldRule{
			Category: "other",
			Title:    strings.TrimSpace(titleHint),
			Rule:     rule,
		})
		return nil
	default:
		return fmt.Errorf("world_rules contains unsupported %T value", value)
	}
}

func validateSourceFoundationCharacters(characters []domain.Character) error {
	allowed := map[string]bool{"male": true, "female": true, "nonbinary": true, "unspecified": true}
	ids := make(map[string]bool, len(characters))
	for index := range characters {
		character := &characters[index]
		character.ID = strings.TrimSpace(character.ID)
		if character.ID == "" || strings.TrimSpace(character.Name) == "" {
			return fmt.Errorf("characters[%d] requires stable id and name", index)
		}
		if ids[character.ID] {
			return fmt.Errorf("characters[%d] duplicates id %q", index, character.ID)
		}
		ids[character.ID] = true
		character.Gender = strings.ToLower(strings.TrimSpace(character.Gender))
		if !allowed[character.Gender] {
			return fmt.Errorf("characters[%d] gender must be male, female, nonbinary, or unspecified", index)
		}
		if character.Gender == "unspecified" {
			character.Constraints = appendStableReferenceConstraint(character.Constraints)
		}
	}
	return nil
}

func appendStableReferenceConstraint(constraints []string) []string {
	const constraint = "原著未明确性别；引用时使用姓名或身份称谓，不自行推断或切换他/她。"
	for _, existing := range constraints {
		if strings.TrimSpace(existing) == constraint {
			return constraints
		}
	}
	return append(constraints, constraint)
}

func validateSourceFoundationRelationships(characters []domain.Character, relationships []domain.CharacterRelationship) error {
	ids := make(map[string]bool, len(characters))
	for _, character := range characters {
		ids[strings.TrimSpace(character.ID)] = true
	}
	for index, relationship := range relationships {
		if strings.TrimSpace(relationship.ID) == "" {
			return fmt.Errorf("relationships[%d] requires stable id", index)
		}
		if !ids[strings.TrimSpace(relationship.SourceCharacterID)] || !ids[strings.TrimSpace(relationship.TargetCharacterID)] {
			return fmt.Errorf("relationships[%d] references unknown character id", index)
		}
		if relationship.SourceCharacterID == relationship.TargetCharacterID {
			return fmt.Errorf("relationships[%d] cannot be self-referential", index)
		}
	}
	return nil
}

func BuildSourceOutlineFromReports(reports []domain.AdaptationSourceReport) []domain.VolumeOutline {
	arcs := make([]domain.ArcOutline, 0, (len(reports)+sourceOutlineArcSize-1)/sourceOutlineArcSize)
	for start := 0; start < len(reports); start += sourceOutlineArcSize {
		end := start + sourceOutlineArcSize
		if end > len(reports) {
			end = len(reports)
		}
		arcReports := reports[start:end]
		arc := domain.ArcOutline{
			Index:             len(arcs) + 1,
			Title:             fmt.Sprintf("Source Chapters %d-%d", arcReports[0].Chapter, arcReports[len(arcReports)-1].Chapter),
			Goal:              outlineGoal(arcReports),
			EstimatedChapters: len(arcReports),
			Chapters:          make([]domain.OutlineEntry, 0, len(arcReports)),
		}
		for _, report := range arcReports {
			arc.Chapters = append(arc.Chapters, outlineEntryFromReport(report))
		}
		arcs = append(arcs, arc)
	}
	return []domain.VolumeOutline{{
		Index: 1,
		Title: "Source Novel",
		Theme: "Preserve the source causal chain and chapter anchors.",
		Arcs:  arcs,
	}}
}

func outlineEntryFromReport(report domain.AdaptationSourceReport) domain.OutlineEntry {
	title := strings.TrimSpace(report.Title)
	if title == "" {
		title = fmt.Sprintf("Chapter %d", report.Chapter)
	}
	scenes := compactList(report.KeyEvents, 5, 500)
	if len(scenes) == 0 && strings.TrimSpace(report.Summary) != "" {
		scenes = []string{compactFact(report.Summary, 500)}
	}
	return domain.OutlineEntry{
		Chapter:   report.Chapter,
		Title:     title,
		CoreEvent: firstNonEmpty(compactList(report.KeyEvents, 1, 500), compactFact(report.Summary, 500)),
		Hook:      outlineHook(report),
		Scenes:    scenes,
	}
}

func outlineGoal(reports []domain.AdaptationSourceReport) string {
	for _, report := range reports {
		if events := compactList(report.KeyEvents, 1, 500); len(events) > 0 {
			return events[0]
		}
		if summary := compactFact(report.Summary, 500); summary != "" {
			return summary
		}
	}
	return "Track the source chapters in order."
}

func outlineHook(report domain.AdaptationSourceReport) string {
	events := compactList(report.KeyEvents, len(report.KeyEvents), 500)
	if len(events) > 0 {
		hookType := compactFact(report.HookType, 80)
		if hookType != "" {
			return hookType + ": " + events[len(events)-1]
		}
		return events[len(events)-1]
	}
	return compactFact(report.HookType, 200)
}

func compactList(values []string, limit, maxRunes int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	for _, value := range values {
		value = compactFact(value, maxRunes)
		if value == "" {
			continue
		}
		out = append(out, value)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func compactFact(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(cleanLLMText(value)), " ")
	if value == "" || maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

func firstNonEmpty(values []string, fallback string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return fallback
}
