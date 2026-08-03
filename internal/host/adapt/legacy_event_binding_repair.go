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

// LegacyArcEventBindingRepairResult is the result of a targeted, contract-only
// repair. It never changes chapter prose or outline scenes.
type LegacyArcEventBindingRepairResult struct {
	Plan     domain.AdaptationPlan
	Chapters []int
	Attempts int
}

type legacyArcEventBindingPatch struct {
	Chapter        int      `json:"chapter"`
	EventIDs       []string `json:"event_ids"`
	PreserveEvents []string `json:"preserve_events"`
	Reason         string   `json:"reason,omitempty"`
}

type legacyArcEventBindingResponse struct {
	Chapters []legacyArcEventBindingPatch `json:"chapters"`
}

type legacyArcEventBindingChapterView struct {
	Chapter         int      `json:"chapter"`
	Title           string   `json:"title"`
	CoreEvent       string   `json:"core_event"`
	Scenes          []string `json:"scenes"`
	EventIDs        []string `json:"event_ids,omitempty"`
	PreserveEvents  []string `json:"preserve_events,omitempty"`
	SourceEvents    []string `json:"source_events,omitempty"`
	RequiredChanges []string `json:"required_changes,omitempty"`
}

// ReconcileLegacyArcEventBindings repairs only source-event ownership fields
// for a legacy confirmed arc plan. It is deliberately separate from budget
// repair: a budget error cannot authorize a story or event reassignment.
func ReconcileLegacyArcEventBindings(ctx context.Context, deps Deps, plan domain.AdaptationPlan) (LegacyArcEventBindingRepairResult, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if domain.NormalizeAdaptationGranularity(plan.Granularity) != domain.AdaptationGranularityArc {
		return LegacyArcEventBindingRepairResult{Plan: plan}, nil
	}
	qualityErr := ValidateAdaptationOutlineQuality(&plan, nil)
	issues := legacyArcEventBindingIssues(qualityErr)
	return reconcileLegacyArcEventBindings(ctx, deps, plan, issues, legacyArcEventBindingIssuesFromPlan)
}

// ReconcileLegacyArcEventBindingsForChapter narrows the repair/audit scope to
// the chapter that is about to be written. Legacy books may contain unrelated
// future-contract defects; loading those into one planner prompt is both
// wasteful and unsafe.
func ReconcileLegacyArcEventBindingsForChapter(ctx context.Context, deps Deps, plan domain.AdaptationPlan, targetChapter int) (LegacyArcEventBindingRepairResult, error) {
	ctx = modeldiag.WithStore(ctx, deps.Store)
	if domain.NormalizeAdaptationGranularity(plan.Granularity) != domain.AdaptationGranularityArc {
		return LegacyArcEventBindingRepairResult{Plan: plan}, nil
	}
	qualityErr := ValidateAdaptationChapterOutlineQuality(&plan, targetChapter)
	issues := legacyArcEventBindingIssues(qualityErr)
	return reconcileLegacyArcEventBindings(ctx, deps, plan, issues, func(candidate domain.AdaptationPlan) []AdaptationOutlineQualityIssue {
		return legacyArcEventBindingIssuesForChapter(candidate, targetChapter)
	})
}

func reconcileLegacyArcEventBindings(
	ctx context.Context,
	deps Deps,
	plan domain.AdaptationPlan,
	issues []AdaptationOutlineQualityIssue,
	validate func(domain.AdaptationPlan) []AdaptationOutlineQualityIssue,
) (LegacyArcEventBindingRepairResult, error) {
	var result LegacyArcEventBindingRepairResult
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withAdaptationPromptModeIfMissing(ctx, plan.Granularity)
	if len(issues) == 0 {
		result.Plan = plan
		return result, nil
	}
	model := deps.modelForStage("detail_outline")
	if model == nil {
		return result, fmt.Errorf("legacy arc event-binding repair planner is unavailable")
	}
	affected := legacyArcEventBindingAffectedChapters(plan, issues)
	if len(affected) == 0 {
		return result, fmt.Errorf("legacy arc event-binding repair has no target chapters")
	}
	base := cloneAdaptationPlan(plan)
	feedback := ""
	maxAttempts := deps.adaptationOutlineAuditRetryMaxAttempts() + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.Attempts = attempt
		prompt, err := buildLegacyArcEventBindingRepairPrompt(base, issues, affected, feedback)
		if err != nil {
			return result, err
		}
		text, err := generatePlannerTextForStage(
			ctx,
			StagePlan,
			model,
			legacyArcEventBindingSystemPrompt(deps.Prompts.Planner),
			prompt,
			adaptationPlannerSkeletonMaxTokens,
			nil,
			attempt,
			maxAttempts,
			fmt.Sprintf("旧大纲事件归属专项修复 %d/%d", attempt, maxAttempts),
			deps.modelCallMaxAttempts(),
		)
		if err != nil {
			feedback = "planner call failed: " + retrypolicy.SanitizeProviderError(err)
			if attempt == maxAttempts {
				return result, fmt.Errorf("legacy arc event-binding repair model call: %s", retrypolicy.SanitizeProviderError(err))
			}
			continue
		}
		patches, err := parseLegacyArcEventBindingRepair(text)
		if err != nil {
			feedback = err.Error()
			if attempt == maxAttempts {
				return result, fmt.Errorf("legacy arc event-binding repair response: %w", err)
			}
			continue
		}
		candidate := cloneAdaptationPlan(base)
		if err := applyLegacyArcEventBindingPatches(&candidate, patches, affected); err != nil {
			feedback = err.Error()
			if attempt == maxAttempts {
				return result, fmt.Errorf("legacy arc event-binding repair patch: %w", err)
			}
			continue
		}
		if remaining := validate(candidate); len(remaining) > 0 {
			feedback = formatLegacyArcEventBindingIssues(remaining)
			if attempt == maxAttempts {
				return result, fmt.Errorf("legacy arc event-binding repair did not pass contract audit: %s", feedback)
			}
			continue
		}
		result.Plan = candidate
		result.Attempts = attempt
		result.Chapters = append([]int(nil), affected...)
		return result, nil
	}
	return result, fmt.Errorf("legacy arc event-binding repair exhausted retries")
}

func legacyArcEventBindingSystemPrompt(plannerPrompt string) string {
	plannerPrompt = strings.TrimSpace(plannerPrompt)
	if plannerPrompt == "" {
		plannerPrompt = "You are an adaptation contract editor. Return only the requested JSON object."
	}
	return plannerPrompt +
		"\n\nmode_contract=arc/full_rewrite\n" +
		"\n\nTargeted event-binding repair contract:\n" +
		"- This is a legacy confirmed-plan contract repair, not a new outline generation.\n" +
		"- You may change only event_ids and preserve_events for the listed target chapters.\n" +
		"- Never change title, core_event, hook, scenes, budgets, source ranges, required_changes, story order, or chapter count.\n" +
		"- Keep each source event owned by exactly one target chapter. For preserve_details-style ownership, preserve_events must be a subset of that chapter's event_ids and the matching source beat must remain in the existing outline.\n" +
		"- Return one JSON object only: {\"chapters\":[{\"chapter\":18,\"event_ids\":[\"src-...\"],\"preserve_events\":[\"src-...\"],\"reason\":\"...\"}]}. Include every listed target chapter exactly once and no other chapter."
}

func buildLegacyArcEventBindingRepairPrompt(plan domain.AdaptationPlan, issues []AdaptationOutlineQualityIssue, affected []int, feedback string) (string, error) {
	views := make([]legacyArcEventBindingChapterView, 0, len(affected))
	for _, number := range affected {
		chapter, ok := adaptationChapterByNumber(plan, number)
		if !ok {
			return "", fmt.Errorf("affected chapter %d is absent from plan", number)
		}
		views = append(views, legacyArcEventBindingChapterView{
			Chapter:         chapter.Chapter,
			Title:           truncateLegacyBudgetText(chapter.Title, 180),
			CoreEvent:       truncateLegacyBudgetText(chapter.CoreEvent, 420),
			Scenes:          truncateLegacyBudgetList(chapter.Scenes, 220),
			EventIDs:        append([]string(nil), chapter.EventIDs...),
			PreserveEvents:  append([]string(nil), chapter.PreserveEvents...),
			SourceEvents:    legacyBudgetEventDescriptions(plan, chapter),
			RequiredChanges: truncateLegacyBudgetList(chapter.RequiredChanges, 220),
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Chapter < views[j].Chapter })
	payload := struct {
		Brief            string                             `json:"brief,omitempty"`
		Issues           string                             `json:"issues"`
		AffectedChapters []legacyArcEventBindingChapterView `json:"affected_chapters"`
	}{
		Brief:            truncateLegacyBudgetText(plan.Brief, 2400),
		Issues:           formatLegacyArcEventBindingIssues(issues),
		AffectedChapters: views,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal legacy event-binding repair context: %w", err)
	}
	var b strings.Builder
	b.WriteString("Repair only the source-event ownership fields in this confirmed arc plan.\n")
	b.WriteString("The deterministic plan gate reported:\n")
	b.WriteString(formatLegacyArcEventBindingIssues(issues))
	b.WriteString("\nPlan context (JSON):\n")
	b.Write(data)
	if strings.TrimSpace(feedback) != "" {
		b.WriteString("\n\nPrevious candidate failed validation. Correct only event_ids/preserve_events and return the complete target set again:\n")
		b.WriteString(truncateLegacyBudgetText(feedback, 2600))
	}
	return b.String(), nil
}

func parseLegacyArcEventBindingRepair(text string) ([]legacyArcEventBindingPatch, error) {
	segments, err := extractPlannerJSONSegments(text)
	if err != nil {
		return nil, err
	}
	var candidates [][]legacyArcEventBindingPatch
	for _, segment := range segments {
		var response legacyArcEventBindingResponse
		if err := json.Unmarshal([]byte(segment), &response); err != nil || len(response.Chapters) == 0 {
			continue
		}
		candidates = append(candidates, response.Chapters)
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("expected one JSON event-binding patch, got %d", len(candidates))
	}
	return candidates[0], nil
}

func applyLegacyArcEventBindingPatches(plan *domain.AdaptationPlan, patches []legacyArcEventBindingPatch, affected []int) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}
	allowed := make(map[int]struct{}, len(affected))
	for _, chapter := range affected {
		allowed[chapter] = struct{}{}
	}
	seen := make(map[int]struct{}, len(patches))
	for _, patch := range patches {
		if _, ok := allowed[patch.Chapter]; !ok {
			return fmt.Errorf("patch contains unexpected chapter %d", patch.Chapter)
		}
		if _, duplicate := seen[patch.Chapter]; duplicate {
			return fmt.Errorf("chapter %d appears more than once", patch.Chapter)
		}
		seen[patch.Chapter] = struct{}{}
		if _, ok := adaptationChapterByNumber(*plan, patch.Chapter); !ok {
			return fmt.Errorf("patch chapter %d is absent from plan", patch.Chapter)
		}
		if hasDuplicateNonBlank(patch.EventIDs) || hasDuplicateNonBlank(patch.PreserveEvents) {
			return fmt.Errorf("chapter %d contains duplicate event IDs", patch.Chapter)
		}
		for _, eventID := range append(append([]string(nil), patch.EventIDs...), patch.PreserveEvents...) {
			if strings.TrimSpace(eventID) == "" {
				return fmt.Errorf("chapter %d contains a blank source event ID", patch.Chapter)
			}
		}
		for index := range plan.Chapters {
			if plan.Chapters[index].Chapter != patch.Chapter {
				continue
			}
			plan.Chapters[index].EventIDs = append([]string(nil), patch.EventIDs...)
			plan.Chapters[index].PreserveEvents = append([]string(nil), patch.PreserveEvents...)
			break
		}
	}
	if len(seen) != len(allowed) {
		return fmt.Errorf("event-binding patch covers %d of %d affected chapters", len(seen), len(allowed))
	}
	domain.ClearAdaptationOutlineQualityAudit(plan)
	return nil
}

func legacyArcEventBindingAffectedChapters(plan domain.AdaptationPlan, issues []AdaptationOutlineQualityIssue) []int {
	set := make(map[int]struct{})
	bindings := domain.ValidateArcSourceEventBindings(plan)
	for _, issue := range issues {
		if issue.TargetChapter > 0 {
			set[issue.TargetChapter] = struct{}{}
		}
		for _, chapter := range issue.AlternativeChapters {
			if chapter > 0 {
				set[chapter] = struct{}{}
			}
		}
		if issue.EventID == "" {
			continue
		}
		for _, binding := range bindings {
			if binding.EventID != issue.EventID {
				continue
			}
			for _, chapter := range binding.Chapters {
				if chapter > 0 {
					set[chapter] = struct{}{}
				}
			}
		}
	}
	out := make([]int, 0, len(set))
	for chapter := range set {
		out = append(out, chapter)
	}
	sort.Ints(out)
	return out
}

func legacyArcEventBindingIssues(err error) []AdaptationOutlineQualityIssue {
	qualityErr, ok := err.(*AdaptationOutlineQualityError)
	if !ok || qualityErr == nil {
		return nil
	}
	out := make([]AdaptationOutlineQualityIssue, 0, len(qualityErr.Issues))
	for _, issue := range qualityErr.Issues {
		if isLegacyArcEventBindingIssue(issue.Code) {
			out = append(out, issue)
		}
	}
	return out
}

func legacyArcEventBindingIssuesFromPlan(plan domain.AdaptationPlan) []AdaptationOutlineQualityIssue {
	return legacyArcEventBindingIssues(ValidateAdaptationOutlineQuality(&plan, nil))
}

func legacyArcEventBindingIssuesForChapter(plan domain.AdaptationPlan, chapter int) []AdaptationOutlineQualityIssue {
	return legacyArcEventBindingIssues(ValidateAdaptationChapterOutlineQuality(&plan, chapter))
}

func isLegacyArcEventBindingIssue(code string) bool {
	switch code {
	case outlineQualityIssueArcUnknownEvent,
		outlineQualityIssueArcDuplicateEvent,
		outlineQualityIssueArcPreserveUnbound,
		outlineQualityIssueArcPreserveMismatch,
		outlineQualityIssueArcEventMismatch:
		return true
	default:
		return false
	}
}

func formatLegacyArcEventBindingIssues(issues []AdaptationOutlineQualityIssue) string {
	ordered := append([]AdaptationOutlineQualityIssue(nil), issues...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].TargetChapter != ordered[j].TargetChapter {
			return ordered[i].TargetChapter < ordered[j].TargetChapter
		}
		if ordered[i].Code != ordered[j].Code {
			return ordered[i].Code < ordered[j].Code
		}
		return ordered[i].EventID < ordered[j].EventID
	})
	parts := make([]string, 0, len(ordered))
	for _, issue := range ordered {
		parts = append(parts, fmt.Sprintf("- [%s] %s", issue.Code, issue.Detail))
	}
	return strings.Join(parts, "\n")
}

func adaptationChapterByNumber(plan domain.AdaptationPlan, number int) (domain.AdaptationChapterPlan, bool) {
	for _, chapter := range plan.Chapters {
		if chapter.Chapter == number {
			return chapter, true
		}
	}
	return domain.AdaptationChapterPlan{}, false
}

func hasDuplicateNonBlank(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
