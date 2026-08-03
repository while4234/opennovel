package adapt

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

// LegacyArcBudgetRepairResult is the result of a budget-only planner pass.
type LegacyArcBudgetRepairResult struct {
	Plan     domain.AdaptationPlan
	Chapters []int
	Attempts int
}

type legacyArcBudgetPatch struct {
	Chapter     int    `json:"chapter"`
	TargetRunes int    `json:"target_runes"`
	MinRunes    int    `json:"min_runes"`
	MaxRunes    int    `json:"max_runes"`
	Reason      string `json:"reason,omitempty"`
}

type legacyArcBudgetRepairResponse struct {
	Chapters []legacyArcBudgetPatch `json:"chapters"`
	Budgets  []legacyArcBudgetPatch `json:"budgets,omitempty"`
}

type legacyArcBudgetChapterView struct {
	Chapter           int      `json:"chapter"`
	Title             string   `json:"title"`
	CoreEvent         string   `json:"core_event"`
	Scenes            []string `json:"scenes"`
	SourceChapters    []int    `json:"source_chapters,omitempty"`
	SourceRunes       int      `json:"source_runes,omitempty"`
	RequiredChanges   []string `json:"required_changes,omitempty"`
	EventDescriptions []string `json:"event_descriptions,omitempty"`
	CurrentTarget     int      `json:"current_target_runes"`
	CurrentMin        int      `json:"current_min_runes"`
	CurrentMax        int      `json:"current_max_runes"`
	RecommendedMin    int      `json:"recommended_min_runes"`
}

// ReanalyzeLegacyArcChapterBudgets asks the planner to repair only the
// chapter budgets reported by the deterministic density gate. It retries with
// exact local validation feedback and rejects incomplete patches.
func ReanalyzeLegacyArcChapterBudgets(ctx context.Context, deps Deps, plan domain.AdaptationPlan) (LegacyArcBudgetRepairResult, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	var result LegacyArcBudgetRepairResult
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withAdaptationPromptModeIfMissing(ctx, plan.Granularity)
	if domain.NormalizeAdaptationGranularity(plan.Granularity) != domain.AdaptationGranularityArc {
		result.Plan = plan
		return result, nil
	}
	issues := domain.ValidateArcChapterBudgetDensity(plan)
	if len(issues) == 0 {
		result.Plan = plan
		return result, nil
	}
	model := deps.modelForStage("detail_outline")
	if model == nil {
		return result, fmt.Errorf("legacy arc budget repair planner is unavailable")
	}

	issueByChapter := make(map[int]domain.AdaptationChapterBudgetDensityIssue, len(issues))
	for _, issue := range issues {
		issueByChapter[issue.Chapter] = issue
	}
	base := cloneAdaptationPlan(plan)
	feedback := ""
	maxAttempts := deps.adaptationOutlineAuditRetryMaxAttempts() + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.Attempts = attempt
		prompt, err := buildLegacyArcBudgetRepairPrompt(base, issues, feedback)
		if err != nil {
			return result, err
		}
		text, err := generatePlannerTextForStage(
			ctx,
			StagePlan,
			model,
			legacyBudgetRepairSystemPrompt(deps.Prompts.Planner),
			prompt,
			adaptationPlannerSkeletonMaxTokens,
			nil,
			attempt,
			maxAttempts,
			fmt.Sprintf("旧大纲预算专项重分析 %d/%d", attempt, maxAttempts),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			feedback = "planner call failed: " + retrypolicy.SanitizeProviderError(err)
			if attempt == maxAttempts {
				return result, fmt.Errorf("legacy arc budget repair model call: %s", retrypolicy.SanitizeProviderError(err))
			}
			continue
		}
		patches, err := parseLegacyArcBudgetRepair(text)
		if err != nil {
			feedback = err.Error()
			if attempt == maxAttempts {
				return result, fmt.Errorf("legacy arc budget repair response: %w", err)
			}
			continue
		}
		candidate := cloneAdaptationPlan(base)
		if err := applyLegacyArcBudgetPatches(&candidate, patches, issueByChapter); err != nil {
			feedback = err.Error()
			if attempt == maxAttempts {
				return result, fmt.Errorf("legacy arc budget repair patch: %w", err)
			}
			continue
		}
		remaining := domain.ValidateArcChapterBudgetDensity(candidate)
		if len(remaining) > 0 {
			feedback = formatLegacyArcBudgetIssues(remaining)
			if attempt == maxAttempts {
				return result, fmt.Errorf("legacy arc budget repair did not pass density audit: %s", feedback)
			}
			continue
		}
		result.Plan = candidate
		result.Attempts = attempt
		result.Chapters = make([]int, 0, len(patches))
		for _, patch := range patches {
			result.Chapters = append(result.Chapters, patch.Chapter)
		}
		sort.Ints(result.Chapters)
		return result, nil
	}
	return result, fmt.Errorf("legacy arc budget repair exhausted retries")
}

func legacyBudgetRepairSystemPrompt(plannerPrompt string) string {
	plannerPrompt = strings.TrimSpace(plannerPrompt)
	if plannerPrompt == "" {
		plannerPrompt = "You are an adaptation planning editor. Return only the requested JSON object."
	}
	return plannerPrompt +
		"\n\nmode_contract=arc/full_rewrite\n" +
		"\n\nBudget-only repair contract:\n" +
		"- This is a legacy confirmed-plan repair, not a new outline generation.\n" +
		"- You may change only target_runes, min_runes, and max_runes for the listed chapters.\n" +
		"- Do not change title, core_event, hook, scenes, event_ids, preserve_events, required_changes, source ranges, chapter count, or story order.\n" +
		"- Use source rune volume, scene count, required changes, neighboring pacing, and the book total range to choose a reasonable budget.\n" +
		"- Return one JSON object only: {\"chapters\":[{\"chapter\":1,\"target_runes\":4000,\"min_runes\":3600,\"max_runes\":4600,\"reason\":\"...\"}]}. Include every listed chapter exactly once and no other chapter."
}

func buildLegacyArcBudgetRepairPrompt(plan domain.AdaptationPlan, issues []domain.AdaptationChapterBudgetDensityIssue, feedback string) (string, error) {
	issueByChapter := make(map[int]domain.AdaptationChapterBudgetDensityIssue, len(issues))
	for _, issue := range issues {
		issueByChapter[issue.Chapter] = issue
	}
	views := make([]legacyArcBudgetChapterView, 0, len(issues))
	for _, chapter := range plan.Chapters {
		issue, ok := issueByChapter[chapter.Chapter]
		if !ok {
			continue
		}
		views = append(views, legacyArcBudgetChapterView{
			Chapter:           chapter.Chapter,
			Title:             truncateLegacyBudgetText(chapter.Title, 180),
			CoreEvent:         truncateLegacyBudgetText(chapter.CoreEvent, 420),
			Scenes:            truncateLegacyBudgetList(chapter.Scenes, 240),
			SourceChapters:    append([]int(nil), chapter.SourceChapters...),
			SourceRunes:       chapter.SourceRunes,
			RequiredChanges:   truncateLegacyBudgetList(chapter.RequiredChanges, 240),
			EventDescriptions: legacyBudgetEventDescriptions(plan, chapter),
			CurrentTarget:     chapter.TargetRunes,
			CurrentMin:        chapter.TargetMinRunes,
			CurrentMax:        chapter.TargetMaxRunes,
			RecommendedMin:    issue.RecommendedMinRunes,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Chapter < views[j].Chapter })
	payload := struct {
		Brief            string                       `json:"brief,omitempty"`
		WordTolerance    float64                      `json:"word_tolerance,omitempty"`
		SourceTotalRunes int                          `json:"source_total_runes,omitempty"`
		TargetTotalRunes int                          `json:"target_total_runes,omitempty"`
		TargetMinRunes   int                          `json:"target_min_runes,omitempty"`
		TargetMaxRunes   int                          `json:"target_max_runes,omitempty"`
		AffectedChapters []legacyArcBudgetChapterView `json:"affected_chapters"`
	}{
		Brief:            truncateLegacyBudgetText(plan.Brief, 2400),
		WordTolerance:    plan.WordTolerance,
		SourceTotalRunes: plan.SourceTotalRunes,
		TargetTotalRunes: plan.TargetTotalRunes,
		TargetMinRunes:   plan.TargetMinRunes,
		TargetMaxRunes:   plan.TargetMaxRunes,
		AffectedChapters: views,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal legacy budget repair context: %w", err)
	}
	var b strings.Builder
	b.WriteString("Re-analyze only the chapter budgets in this confirmed adaptation plan.\n")
	b.WriteString("The deterministic audit reports these constraints:\n")
	b.WriteString(formatLegacyArcBudgetIssues(issues))
	b.WriteString("\nPlan context (JSON):\n")
	b.Write(data)
	if strings.TrimSpace(feedback) != "" {
		b.WriteString("\n\nPrevious candidate failed validation. Correct only the budget patch and return the complete set again:\n")
		b.WriteString(truncateLegacyBudgetText(feedback, 2400))
	}
	return b.String(), nil
}

func parseLegacyArcBudgetRepair(text string) ([]legacyArcBudgetPatch, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return nil, err
	}
	var candidates [][]legacyArcBudgetPatch
	for _, segment := range segments {
		var response legacyArcBudgetRepairResponse
		if err := json.Unmarshal([]byte(segment), &response); err != nil {
			continue
		}
		patches := response.Chapters
		if len(patches) == 0 {
			patches = response.Budgets
		}
		if len(patches) > 0 {
			candidates = append(candidates, patches)
		}
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("expected one JSON budget patch, got %d", len(candidates))
	}
	patches := candidates[0]
	if len(patches) == 0 {
		return nil, fmt.Errorf("budget patch contains no chapters")
	}
	return patches, nil
}

func applyLegacyArcBudgetPatches(plan *domain.AdaptationPlan, patches []legacyArcBudgetPatch, issues map[int]domain.AdaptationChapterBudgetDensityIssue) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}
	seen := make(map[int]struct{}, len(patches))
	for _, patch := range patches {
		if _, duplicate := seen[patch.Chapter]; duplicate {
			return fmt.Errorf("chapter %d appears more than once", patch.Chapter)
		}
		seen[patch.Chapter] = struct{}{}
		issue, expected := issues[patch.Chapter]
		if !expected {
			return fmt.Errorf("patch contains unexpected chapter %d", patch.Chapter)
		}
		if patch.TargetRunes <= 0 || patch.MinRunes <= 0 || patch.MaxRunes <= 0 {
			return fmt.Errorf("chapter %d budget values must be positive", patch.Chapter)
		}
		if patch.MinRunes > patch.TargetRunes || patch.TargetRunes > patch.MaxRunes {
			return fmt.Errorf("chapter %d budget must satisfy min <= target <= max", patch.Chapter)
		}
		if patch.MaxRunes < issue.RecommendedMinRunes {
			return fmt.Errorf("chapter %d max budget %d is below required %d", patch.Chapter, patch.MaxRunes, issue.RecommendedMinRunes)
		}
		capacity := domain.AdaptationModelChapterMaxRunes
		if issue.RecommendedMinRunes > capacity {
			capacity = issue.RecommendedMinRunes
		}
		if patch.MaxRunes > capacity*2 {
			return fmt.Errorf("chapter %d max budget %d is implausibly large", patch.Chapter, patch.MaxRunes)
		}
		found := false
		for index := range plan.Chapters {
			chapter := &plan.Chapters[index]
			if chapter.Chapter != patch.Chapter {
				continue
			}
			chapter.TargetRunes = patch.TargetRunes
			chapter.TargetMinRunes = patch.MinRunes
			chapter.TargetMaxRunes = patch.MaxRunes
			if chapter.WordBudget == nil {
				chapter.WordBudget = &domain.AdaptationChapterWordBudget{SourceRunes: chapter.SourceRunes}
			}
			chapter.WordBudget.TargetRunes = patch.TargetRunes
			chapter.WordBudget.MinRunes = patch.MinRunes
			chapter.WordBudget.MaxRunes = patch.MaxRunes
			found = true
			break
		}
		if !found {
			return fmt.Errorf("patch chapter %d is absent from plan", patch.Chapter)
		}
	}
	if len(seen) != len(issues) {
		return fmt.Errorf("budget patch covers %d of %d affected chapters", len(seen), len(issues))
	}
	plan.TargetTotalRunes = 0
	plan.TargetMinRunes = 0
	plan.TargetMaxRunes = 0
	for _, chapter := range plan.Chapters {
		plan.TargetTotalRunes += chapter.TargetRunes
		plan.TargetMinRunes += chapter.TargetMinRunes
		plan.TargetMaxRunes += chapter.TargetMaxRunes
	}
	domain.ClearAdaptationOutlineQualityAudit(plan)
	return nil
}

func legacyBudgetEventDescriptions(plan domain.AdaptationPlan, chapter domain.AdaptationChapterPlan) []string {
	events := make(map[string]domain.AdaptationEvent, len(plan.SourceEvents))
	for _, event := range plan.SourceEvents {
		events[strings.TrimSpace(event.ID)] = event
	}
	ids := append(append([]string(nil), chapter.EventIDs...), chapter.PreserveEvents...)
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if _, ok := seen[id]; ok || id == "" {
			continue
		}
		event, ok := events[id]
		if !ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, fmt.Sprintf("%s: %s", id, truncateLegacyBudgetText(event.Description, 280)))
	}
	return out
}

func formatLegacyArcBudgetIssues(issues []domain.AdaptationChapterBudgetDensityIssue) string {
	ordered := append([]domain.AdaptationChapterBudgetDensityIssue(nil), issues...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Chapter < ordered[j].Chapter })
	parts := make([]string, 0, len(ordered))
	for _, issue := range ordered {
		parts = append(parts, fmt.Sprintf("- chapter %d: %s", issue.Chapter, issue.Detail))
	}
	return strings.Join(parts, "\n")
}

func truncateLegacyBudgetList(values []string, maxRunes int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = truncateLegacyBudgetText(value, maxRunes)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func truncateLegacyBudgetText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
