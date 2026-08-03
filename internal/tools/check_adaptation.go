package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// CheckAdaptationTool records the adaptation review for the current draft.
type CheckAdaptationTool struct {
	store *store.Store
}

func NewCheckAdaptationTool(store *store.Store) *CheckAdaptationTool {
	return &CheckAdaptationTool{store: store}
}

func (t *CheckAdaptationTool) Name() string { return "check_adaptation" }
func (t *CheckAdaptationTool) Description() string {
	return "Adaptation-only gate: compare source refs, adaptation plan, and current draft before commit_chapter."
}
func (t *CheckAdaptationTool) Label() string { return "adaptation check" }

func (t *CheckAdaptationTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *CheckAdaptationTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *CheckAdaptationTool) Schema() map[string]any {
	changeEvidenceSchema := schema.Object(
		schema.Property("source_chapter", schema.Int("source chapter number for the changed scene")),
		schema.Property("source_anchor", schema.String("short source anchor or scene reference")),
		schema.Property("change", schema.String("required adaptation change that was applied")).Required(),
		schema.Property("integration", schema.String("how the change appears inside normal prose, not as a note")),
	)
	bodyEvidenceSchema := schema.Object(
		schema.Property("event_id", schema.String("event ID assigned by the current adaptation contract")).Required(),
		schema.Property("quote", schema.String("short verbatim quote from the current draft proving the event or state transition")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("target chapter number")).Required(),
		schema.Property("passed", schema.Bool("whether the draft satisfies the adaptation contract")).Required(),
		schema.Property("summary", schema.String("review summary: preserved source events and implemented changes")),
		schema.Property("issues", schema.Array("unmet requirements; any issue makes the check fail", schema.String(""))),
		// 允许模型省略中性默认值 []。代码仍会独立执行
		// adaptationChangeEvidenceIssues：需要证据的章节不会因省略字段而通过，
		// 但模型能收到具体缺项而不是在 schema 层反复空耗整轮。
		schema.Property("change_evidence", schema.Array("evidence that required changes were integrated into prose; omitted means []; use [] only when no visible source change is required", changeEvidenceSchema)),
		schema.Property("body_evidence", schema.Array("verbatim draft evidence for assigned event_ids when preserve_details applies or no explicit rewrite is declared; arc/full_rewrite chapters with required_changes should use change_evidence for transformed events instead of forcing source events to appear verbatim", bodyEvidenceSchema)),
	)
}

func (t *CheckAdaptationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter        int                               `json:"chapter"`
		Passed         bool                              `json:"passed"`
		Summary        string                            `json:"summary"`
		Issues         []string                          `json:"issues"`
		ChangeEvidence []domain.AdaptationChangeEvidence `json:"change_evidence"`
		BodyEvidence   []domain.AdaptationBodyEvidence   `json:"body_evidence"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}

	plan, err := t.store.Adaptation.LoadPlan()
	if err != nil {
		return nil, fmt.Errorf("load adaptation plan: %w: %w", errs.ErrStoreRead, err)
	}
	if plan == nil {
		return nil, fmt.Errorf("current project is not in adaptation mode: %w", errs.ErrToolPrecondition)
	}
	if plan.Status != domain.AdaptationPlanStatusConfirmed {
		return nil, fmt.Errorf("adaptation plan is not confirmed: %w", errs.ErrToolPrecondition)
	}
	if densityIssues := domain.ValidateArcChapterBudgetDensity(*plan); len(densityIssues) > 0 {
		return nil, fmt.Errorf("改编项目第 %d 章预算仍未通过创作前专项修复：%s。请先由 Host 执行预算专项模型重分析，不要在正文阶段强行补预算: %w", a.Chapter, densityIssues[0].Detail, errs.ErrToolPrecondition)
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, a.Chapter)
	if !ok {
		return nil, fmt.Errorf("adaptation plan has no 第 %d 章: %w", a.Chapter, errs.ErrToolPrecondition)
	}

	content, wordCount, err := t.store.Drafts.LoadChapterContent(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("no draft found for chapter %d: %w", a.Chapter, errs.ErrToolPrecondition)
	}

	var missingSourceRefs []int
	sourceRefs := make(map[int]map[string]any)
	for _, ref := range chapterPlan.SourceChapters {
		text, source, readErr := t.store.Adaptation.LoadSourceChapter(ref)
		if readErr != nil {
			return nil, fmt.Errorf("load source chapter %d: %w: %w", ref, errs.ErrStoreRead, readErr)
		}
		if source == nil || strings.TrimSpace(text) == "" {
			missingSourceRefs = append(missingSourceRefs, ref)
			continue
		}
		sourceRefs[ref] = map[string]any{
			"title": source.Title,
			"runes": source.Runes,
		}
	}

	issues := cleanIssueList(a.Issues)
	issues = append(issues, adaptationPlanContractIssues(plan, a.Chapter)...)
	if len(missingSourceRefs) > 0 {
		issues = append(issues, fmt.Sprintf("source refs missing: %v", missingSourceRefs))
	}
	contract := buildAdaptationWordContract(t.store, plan, chapterPlan, a.Chapter, wordCount)
	warnings := adaptationWordContractWarnings(t.store, plan, chapterPlan, a.Chapter, wordCount)
	issues = append(issues, adaptationWordContractIssues(t.store, plan, chapterPlan, a.Chapter, wordCount)...)
	issues = append(issues, adaptationDraftQualityIssues(t.store, plan, chapterPlan, a.Chapter, content)...)
	changeEvidence := cleanChangeEvidence(a.ChangeEvidence)
	issues = append(issues, adaptationChangeEvidenceIssues(plan, chapterPlan, changeEvidence)...)
	bodyEvidence := cleanAdaptationBodyEvidence(a.BodyEvidence)
	fulfilledByPriorChapter := adaptationPriorChapterEventEvidence(t.store, plan, chapterPlan)
	issues = append(issues, adaptationBodyEvidenceIssues(plan, chapterPlan, content, bodyEvidence, fulfilledByPriorChapter)...)

	// Writer's passed flag is retained for wire compatibility, but independent
	// deterministic checks are the commit condition.
	passed := len(issues) == 0
	digest := store.TextSHA256(content)
	check := domain.AdaptationCheck{
		Chapter:        a.Chapter,
		DraftSHA256:    digest,
		Passed:         passed,
		Summary:        strings.TrimSpace(a.Summary),
		Issues:         issues,
		ChangeEvidence: changeEvidence,
		BodyEvidence:   bodyEvidence,
		CheckedAt:      time.Now().Format(time.RFC3339),
	}
	if err := t.store.Adaptation.SaveCheck(check); err != nil {
		return nil, fmt.Errorf("save adaptation check: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"chapter":                    a.Chapter,
		"passed":                     passed,
		"draft_sha256":               digest,
		"word_count":                 wordCount,
		"issues":                     issues,
		"word_contract_warnings":     warnings,
		"change_evidence":            changeEvidence,
		"body_evidence":              bodyEvidence,
		"assigned_event_evidence":    adaptationAssignedEventEvidence(plan, chapterPlan),
		"fulfilled_by_prior_chapter": fulfilledByPriorChapter,
		"required_change_evidence":   adaptationRequiredChangeEvidencePrompt(plan, chapterPlan),
		"source_refs":                sourceRefs,
		"next_step":                  adaptationCheckNextStep(passed, issues, contract, a.Chapter),
		"chapter_plan":               chapterPlan,
		"plan_granularity":           plan.Granularity,
		"rewrite_policy":             plan.RewritePolicy,
		"adaptation_word_contract":   contract,
	})
}

func adaptationPriorChapterEventEvidence(
	st *store.Store,
	plan *domain.AdaptationPlan,
	current domain.AdaptationChapterPlan,
) map[string]int {
	fulfilled := make(map[string]int)
	if st == nil || plan == nil || current.Chapter <= 1 || len(current.EventIDs) == 0 {
		return fulfilled
	}

	events := make(map[string]domain.AdaptationEvent)
	for _, event := range append(append([]domain.AdaptationEvent(nil), plan.SourceEvents...), plan.TargetEventLedger...) {
		events[strings.TrimSpace(event.ID)] = event
	}
	for _, rawEventID := range current.EventIDs {
		eventID := strings.TrimSpace(rawEventID)
		description := strings.TrimSpace(events[eventID].Description)
		if eventID == "" || description == "" {
			continue
		}
		for _, candidate := range plan.Chapters {
			if candidate.Chapter <= 0 || candidate.Chapter >= current.Chapter {
				continue
			}
			ownsEvent := false
			for _, candidateEventID := range candidate.EventIDs {
				if strings.TrimSpace(candidateEventID) == eventID {
					ownsEvent = true
					break
				}
			}
			if !ownsEvent {
				ownsEvent = adaptationEvidenceSupportsEvent(description, strings.Join(candidate.PreserveEvents, "\n"))
			}
			if !ownsEvent {
				continue
			}
			finalText, err := st.Drafts.LoadChapterText(candidate.Chapter)
			if err == nil && adaptationEvidenceSupportsEvent(description, finalText) {
				fulfilled[eventID] = candidate.Chapter
				break
			}
		}
	}
	return fulfilled
}

func adaptationAssignedEventEvidence(plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan) []map[string]any {
	if plan == nil || len(chapterPlan.EventIDs) == 0 {
		return nil
	}
	events := make(map[string]domain.AdaptationEvent)
	for _, event := range append(append([]domain.AdaptationEvent(nil), plan.SourceEvents...), plan.TargetEventLedger...) {
		events[strings.TrimSpace(event.ID)] = event
	}
	assigned := make([]map[string]any, 0, len(chapterPlan.EventIDs))
	for _, eventID := range chapterPlan.EventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID == "" {
			continue
		}
		event := events[eventID]
		assigned = append(assigned, map[string]any{
			"event_id":       eventID,
			"description":    strings.TrimSpace(event.Description),
			"source_chapter": event.SourceChapter,
			"importance":     event.Importance,
		})
	}
	return assigned
}

func adaptationCheckNextStep(passed bool, issues []string, contract adaptationWordContract, chapter int) string {
	if passed {
		return "adaptation check passed; continue with check_consistency if needed, then commit_chapter."
	}
	if repair := adaptationPlanContractRepairStep(issues); repair != "" {
		return "adaptation check failed: " + repair
	}
	if repair := adaptationProseQualityRepairStep(issues, chapter); repair != "" {
		return "adaptation check failed: " + repair
	}
	if repair := adaptationQualityRepairStep(issues, chapter); repair != "" {
		return "adaptation check failed: " + repair + ". Then call check_adaptation again with the corrected tool arguments."
	}
	if repair := adaptationWordContractRepairStep(contract, issues, chapter); repair != "" {
		return "adaptation check failed: " + repair + " Then call check_adaptation again."
	}
	return "adaptation check failed: fix issues, then call check_adaptation again."
}

func adaptationPlanContractIssues(plan *domain.AdaptationPlan, chapter int) []string {
	if plan == nil || domain.NormalizeAdaptationGranularity(plan.Granularity) != domain.AdaptationGranularityArc {
		return nil
	}
	issues := make([]string, 0)
	for _, issue := range domain.ValidateArcSourceEventBindings(*plan) {
		for _, owner := range issue.Chapters {
			if owner == chapter {
				issues = append(issues, fmt.Sprintf("adaptation_outline_contract: %s", issue.Detail))
				break
			}
		}
	}
	for _, issue := range domain.ValidateArcEventOutlineThemes(*plan) {
		if issue.TargetChapter == chapter {
			issues = append(issues, fmt.Sprintf("adaptation_outline_contract: %s", issue.Detail))
		}
	}
	for _, issue := range domain.ValidateArcChapterBudgetDensity(*plan) {
		if issue.Chapter == chapter {
			issues = append(issues, fmt.Sprintf("adaptation_outline_contract: %s", issue.Detail))
		}
	}
	sort.Strings(issues)
	return issues
}

func adaptationPlanContractRepairStep(issues []string) string {
	for _, issue := range issues {
		if strings.Contains(issue, "adaptation_outline_contract") || strings.Contains(issue, "arc_event_") {
			return "上游改编大纲的 event_ids 归属无效；停止正文修复，先修复确认后的章节契约（每个 source event_id 只能有一个 owner，且 event_ids、preserve_events、required_changes 与剧情归属一致），再重新执行本章检查。"
		}
	}
	return ""
}

func adaptationRequiredChangeEvidencePrompt(plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan) map[string]any {
	if plan == nil ||
		(plan.RewritePolicy != domain.AdaptationRewritePreserveDetails && plan.RewritePolicy != domain.AdaptationRewriteFullRewrite) ||
		!adaptationRequiresExplicitChangeEvidence(plan, chapterPlan) {
		return map[string]any{
			"required": false,
		}
	}
	return map[string]any{
		"required": true,
		"field":    "change_evidence",
		"note":     "Do not write evidence only in summary. Provide this field as a JSON array in the tool arguments.",
		"item_schema": map[string]string{
			"source_chapter": "source chapter number for the changed scene, or omit when source_anchor is enough",
			"source_anchor":  "short source anchor or scene reference",
			"change":         "required adaptation change that was applied",
			"integration":    "how the change appears inside normal prose, not as a note",
		},
		"example": []domain.AdaptationChangeEvidence{{
			SourceChapter: firstSourceChapter(chapterPlan),
			SourceAnchor:  "原文章节中被改动的场景锚点",
			Change:        "把改编 brief 要求的角色/关系/情节变化写入该场景",
			Integration:   "说明变化如何自然出现在正文动作、对白、叙述或潜台词中，而不是括号说明",
		}},
	}
}

func firstSourceChapter(chapterPlan domain.AdaptationChapterPlan) int {
	if len(chapterPlan.SourceChapters) == 0 {
		return 0
	}
	return chapterPlan.SourceChapters[0]
}

func cleanIssueList(items []string) []string {
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func findAdaptationChapterPlan(plan *domain.AdaptationPlan, chapter int) (domain.AdaptationChapterPlan, bool) {
	if plan == nil {
		return domain.AdaptationChapterPlan{}, false
	}
	for _, item := range plan.Chapters {
		if item.Chapter == chapter {
			return item, true
		}
	}
	return domain.AdaptationChapterPlan{}, false
}
