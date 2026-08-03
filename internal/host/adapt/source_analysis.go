package adapt

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
)

const sourceAnalyzerSceneBatchRunes = 16_000

func analyzeSourceChapterInSceneBatches(
	ctx context.Context,
	deps Deps,
	chapter int,
	title string,
	content string,
	total int,
	emit ProgressEmitter,
) (*imp.ChapterAnalysis, error) {
	batches, err := splitSourceAnalysisSceneBatches(content, sourceAnalyzerSceneBatchRunes)
	if err != nil {
		return nil, err
	}
	analyses := make([]*imp.ChapterAnalysis, 0, len(batches))
	for index, batch := range batches {
		batchTitle := title
		if len(batches) > 1 {
			batchTitle = fmt.Sprintf("%s（场景批次 %d/%d）", title, index+1, len(batches))
		}
		analysis, err := imp.AnalyzeChapterWithOptions(
			ctx, deps.modelForStage("source_analysis"), deps.Prompts.Analyzer, chapter, batchTitle, batch, "", "", nil,
			structuredCallOptionsWithDeps(deps, StageChapter, chapter, total, emit),
		)
		if err != nil {
			return nil, fmt.Errorf("analyze scene batch %d/%d: %w", index+1, len(batches), err)
		}
		analyses = append(analyses, analysis)
	}
	return mergeSourceChapterAnalyses(analyses), nil
}

func splitSourceAnalysisSceneBatches(content string, maxRunes int) ([]string, error) {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" {
		return nil, fmt.Errorf("source chapter content is empty")
	}
	if maxRunes <= 0 || len([]rune(content)) <= maxRunes {
		return []string{content}, nil
	}
	units := strings.Split(content, "\n\n")
	if len(units) == 1 {
		units = strings.Split(content, "\n")
	}
	var batches []string
	var current strings.Builder
	for _, rawUnit := range units {
		unit := strings.TrimSpace(rawUnit)
		if unit == "" {
			continue
		}
		if len([]rune(unit)) > maxRunes {
			return nil, fmt.Errorf("one source scene exceeds analyzer hard batch size; add a scene or paragraph boundary before analysis")
		}
		separatorRunes := 0
		if current.Len() > 0 {
			separatorRunes = 2
		}
		if current.Len() > 0 && len([]rune(current.String()))+separatorRunes+len([]rune(unit)) > maxRunes {
			batches = append(batches, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(unit)
	}
	if current.Len() > 0 {
		batches = append(batches, current.String())
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("source chapter contains no analyzable scene")
	}
	return batches, nil
}

func mergeSourceChapterAnalyses(analyses []*imp.ChapterAnalysis) *imp.ChapterAnalysis {
	out := &imp.ChapterAnalysis{}
	var summaries []string
	for _, analysis := range analyses {
		if analysis == nil {
			continue
		}
		if summary := strings.TrimSpace(analysis.Summary); summary != "" {
			summaries = append(summaries, summary)
		}
		out.Characters = appendUniqueStrings(out.Characters, analysis.Characters...)
		out.CharacterProfiles = appendUniqueCharacterProfiles(out.CharacterProfiles, analysis.CharacterProfiles...)
		out.CharacterFacts = appendUniqueStrings(out.CharacterFacts, analysis.CharacterFacts...)
		out.KeyEvents = appendUniqueStrings(out.KeyEvents, analysis.KeyEvents...)
		out.WorldRules = appendUniqueStrings(out.WorldRules, analysis.WorldRules...)
		out.TimelineEvents = append(out.TimelineEvents, analysis.TimelineEvents...)
		out.ForeshadowUpdates = append(out.ForeshadowUpdates, analysis.ForeshadowUpdates...)
		out.RelationshipChanges = append(out.RelationshipChanges, analysis.RelationshipChanges...)
		out.StateChanges = append(out.StateChanges, analysis.StateChanges...)
		if out.DominantStrand == "" {
			out.DominantStrand = analysis.DominantStrand
		}
		if analysis.HookType != "" {
			out.HookType = analysis.HookType
		}
	}
	out.Summary = clipText(strings.Join(summaries, "；"), 200)
	return out
}

func appendUniqueCharacterProfiles(existing []domain.Character, values ...domain.Character) []domain.Character {
	seen := make(map[string]int, len(existing)+len(values))
	for index, character := range existing {
		seen[characterProfileIdentity(character)] = index
	}
	for _, character := range values {
		key := characterProfileIdentity(character)
		if key == "" {
			continue
		}
		if index, exists := seen[key]; exists {
			existing[index] = richerCharacterProfile(existing[index], character)
			continue
		}
		seen[key] = len(existing)
		existing = append(existing, domain.CloneCharacter(character))
	}
	return existing
}

func characterProfileIdentity(character domain.Character) string {
	if id := strings.ToLower(strings.TrimSpace(character.ID)); id != "" {
		return "id:" + id
	}
	if name := strings.ToLower(strings.TrimSpace(character.Name)); name != "" {
		return "name:" + name
	}
	return ""
}

func richerCharacterProfile(current, incoming domain.Character) domain.Character {
	out := domain.CloneCharacter(current)
	fill := func(target *string, value string) {
		if strings.TrimSpace(*target) == "" {
			*target = strings.TrimSpace(value)
		}
	}
	fill(&out.ID, incoming.ID)
	fill(&out.Name, incoming.Name)
	fill(&out.Role, incoming.Role)
	fill(&out.Gender, incoming.Gender)
	fill(&out.Description, incoming.Description)
	fill(&out.Arc, incoming.Arc)
	fill(&out.Tier, incoming.Tier)
	fill(&out.Faction, incoming.Faction)
	fill(&out.Goal, incoming.Goal)
	fill(&out.Motivation, incoming.Motivation)
	fill(&out.Conflict, incoming.Conflict)
	fill(&out.Voice, incoming.Voice)
	fill(&out.Notes, incoming.Notes)
	out.Aliases = appendUniqueStrings(out.Aliases, incoming.Aliases...)
	out.Traits = appendUniqueStrings(out.Traits, incoming.Traits...)
	out.Constraints = appendUniqueStrings(out.Constraints, incoming.Constraints...)
	if out.InitialState == nil && incoming.InitialState != nil {
		state := *incoming.InitialState
		state.Resources = append([]string(nil), incoming.InitialState.Resources...)
		out.InitialState = &state
	}
	if out.KnowledgeBoundary == nil && incoming.KnowledgeBoundary != nil {
		boundary := *incoming.KnowledgeBoundary
		boundary.Known = append([]string(nil), incoming.KnowledgeBoundary.Known...)
		boundary.Unknown = append([]string(nil), incoming.KnowledgeBoundary.Unknown...)
		boundary.Misconceptions = append([]string(nil), incoming.KnowledgeBoundary.Misconceptions...)
		boundary.Forbidden = append([]string(nil), incoming.KnowledgeBoundary.Forbidden...)
		out.KnowledgeBoundary = &boundary
	}
	out.ContrastDetails = append(out.ContrastDetails, incoming.ContrastDetails...)
	out.KeyBackstory = append(out.KeyBackstory, incoming.KeyBackstory...)
	return out
}

func appendUniqueStrings(existing []string, values ...string) []string {
	seen := make(map[string]bool, len(existing)+len(values))
	for _, value := range existing {
		seen[strings.TrimSpace(value)] = true
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		existing = append(existing, value)
	}
	return existing
}
