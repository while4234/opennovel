package host

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	manuscriptContextBudgetBytes       = 48 * 1024
	adaptationSourceContextBudgetUnits = 10000
	manuscriptSegmentTargetRunes       = 3200
	manuscriptSegmentMinRunes          = 2200
	manuscriptSegmentMaxRunes          = 5000
)

type ManuscriptGenerationContext struct {
	Mode           domain.RevisionMode `json:"mode"`
	Foundation     string              `json:"foundation,omitempty"`
	CurrentProse   string              `json:"current_prose"`
	AdjacentWindow []string            `json:"adjacent_window,omitempty"`
	SourceSegments []string            `json:"source_segments,omitempty"`
	OwnedEvents    []string            `json:"owned_events,omitempty"`
	Contracts      []string            `json:"contracts,omitempty"`
	BudgetBytes    int                 `json:"budget_bytes"`
}

func (s *ManuscriptRevisionService) buildGenerationContext(runtime domain.ManuscriptRevisionRuntime, item domain.ManuscriptReworkItem) (ManuscriptGenerationContext, error) {
	baseline, prose, err := s.CurrentChapter(item.ChapterID)
	if err != nil {
		return ManuscriptGenerationContext{}, err
	}
	entry, chapter, _, err := s.resolveChapter(item.ChapterID)
	if err != nil {
		return ManuscriptGenerationContext{}, err
	}
	foundation, err := s.store.Outline.LoadPremise()
	if err != nil {
		return ManuscriptGenerationContext{}, err
	}
	context := ManuscriptGenerationContext{
		Mode: runtime.Mode, Foundation: truncateManuscriptRunes(foundation, 4000), CurrentProse: prose,
		Contracts: []string{mustManuscriptJSON(entry), mustManuscriptJSON(baseline.NarrativeContract)},
	}
	for _, adjacent := range []int{chapter - 1, chapter + 1} {
		if adjacent <= 0 {
			continue
		}
		text, loadErr := s.store.Drafts.LoadChapterText(adjacent)
		if loadErr == nil && strings.TrimSpace(text) != "" {
			context.AdjacentWindow = append(context.AdjacentWindow, truncateManuscriptRunes(text, 2500))
		}
	}
	if summary, loadErr := s.store.Summaries.LoadSummary(chapter); loadErr == nil && summary != nil {
		context.Contracts = append(context.Contracts, mustManuscriptJSON(summary))
	}
	if runtime.Mode == domain.RevisionModeAdaptation {
		plan, loadErr := s.store.Adaptation.LoadPlan()
		if loadErr != nil || plan == nil {
			return ManuscriptGenerationContext{}, fmt.Errorf("adaptation plan is unavailable: %w", loadErr)
		}
		var planned *domain.AdaptationChapterPlan
		for index := range plan.Chapters {
			if plan.Chapters[index].ID == item.ChapterID || plan.Chapters[index].Chapter == chapter {
				planned = &plan.Chapters[index]
				break
			}
		}
		if planned == nil {
			return ManuscriptGenerationContext{}, fmt.Errorf("adaptation task-local chapter %q is not in the confirmed plan", item.ChapterID)
		}
		remaining := adaptationSourceContextBudgetUnits
		for _, sourceChapter := range planned.SourceChapters {
			text, _, sourceErr := s.store.Adaptation.LoadSourceChapter(sourceChapter)
			if sourceErr != nil {
				return ManuscriptGenerationContext{}, sourceErr
			}
			if strings.TrimSpace(text) == "" {
				return ManuscriptGenerationContext{}, fmt.Errorf("adaptation source chapter %d is unavailable", sourceChapter)
			}
			segment := truncateManuscriptRunes(text, remaining)
			context.SourceSegments = append(context.SourceSegments, segment)
			remaining -= len([]rune(segment))
			if remaining <= 0 {
				break
			}
		}
		context.OwnedEvents = append(context.OwnedEvents, planned.EventIDs...)
		context.OwnedEvents = append(context.OwnedEvents, planned.PreserveEvents...)
		context.Contracts = append(context.Contracts, mustManuscriptJSON(planned))
	}
	payload, err := json.Marshal(context)
	if err != nil {
		return ManuscriptGenerationContext{}, err
	}
	context.BudgetBytes = len(payload)
	if err := context.Validate(); err != nil {
		return context, err
	}
	return context, nil
}

func mustManuscriptJSON(value any) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}

func truncateManuscriptRunes(value string, limit int) string {
	runes := []rune(value)
	if limit >= 0 && len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func (c ManuscriptGenerationContext) Validate() error {
	if strings.TrimSpace(c.CurrentProse) == "" {
		return fmt.Errorf("current prose is required")
	}
	if c.BudgetBytes <= 0 || c.BudgetBytes > manuscriptContextBudgetBytes {
		return fmt.Errorf("manuscript context exceeds the 60 KiB writer budget")
	}
	if c.Mode == domain.RevisionModeNormal && (len(c.SourceSegments) > 0 || len(c.OwnedEvents) > 0) {
		return fmt.Errorf("normal manuscript context must not contain source or adaptation fields")
	}
	if c.Mode == domain.RevisionModeAdaptation {
		if len(c.SourceSegments) == 0 || len(c.Contracts) == 0 {
			return fmt.Errorf("adaptation manuscript context requires task-local source segments and contracts")
		}
		units := 0
		for _, segment := range c.SourceSegments {
			units += len([]rune(segment))
		}
		if units > adaptationSourceContextBudgetUnits {
			return fmt.Errorf("adaptation source context exceeds %d units", adaptationSourceContextBudgetUnits)
		}
	}
	return nil
}
