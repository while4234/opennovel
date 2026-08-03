package tools

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	adaptationSimilarityShingleRunes = 12
	adaptationSimilarityMinRunes     = 1000
	adaptationSimilarityLimit        = 0.985
)

var adaptationParentheticalResidueRe = regexp.MustCompile(`[（(][^）)\n]{0,48}(内心独白|心理活动|改编补充|改编说明|改编补丁|仅为示意|实际融入动作)[^）)\n]{0,96}[）)]`)

var adaptationForbiddenFragments = []string{
	"内心独白仅为示意",
	"实际融入动作",
	"仅为示意",
	"内心独白：",
	"内心独白:",
	"心理活动：",
	"心理活动:",
	"改编补充：",
	"改编补充:",
	"改编说明",
	"改编补丁",
}

func adaptationDraftQualityStatus(st *store.Store, chapter int, content string) ([]string, bool) {
	if st == nil || chapter <= 0 || !st.Adaptation.Active() {
		return nil, false
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return nil, false
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, chapter)
	if !ok {
		return nil, false
	}
	return adaptationDraftQualityIssues(st, plan, chapterPlan, chapter, content), true
}

func adaptationDraftQualityIssues(st *store.Store, plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, chapter int, content string) []string {
	if plan == nil {
		return nil
	}
	var issues []string
	issues = append(issues, adaptationResidueIssues(content)...)
	issues = append(issues, adaptationPlanningTermIssues(content)...)
	issues = append(issues, adaptationStyleIssues(content)...)
	if plan.RewritePolicy == domain.AdaptationRewritePreserveDetails {
		issues = append(issues, adaptationSourceSimilarityIssues(st, chapterPlan, chapter, content)...)
	}
	return issues
}

var adaptationPlanningTerms = []string{"情感锚点", "功能模块", "资源总管", "叙事功能"}

func adaptationPlanningTermIssues(content string) []string {
	var found []string
	for _, term := range adaptationPlanningTerms {
		if strings.Contains(content, term) {
			found = append(found, term)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("adaptation_quality: planning terms leaked into prose: %s", strings.Join(found, ", "))}
}

type adaptationStyleCount struct {
	name      string
	count     int
	threshold int
}

var adaptationStylePatterns = []struct {
	name           string
	pattern        *regexp.Regexp
	perThousandMax int
}{
	{name: "corrective_not_but", pattern: regexp.MustCompile(`不是[^。！？\n]{1,32}(?:而是|是)`), perThousandMax: 3},
	{name: "silence_beat", pattern: regexp.MustCompile(`没有说话|沉默了|没开口`), perThousandMax: 4},
	{name: "simile", pattern: regexp.MustCompile(`像是|仿佛|如同|宛如`), perThousandMax: 8},
	{name: "em_dash", pattern: regexp.MustCompile(`——`), perThousandMax: 10},
}

func adaptationStyleIssues(content string) []string {
	runes := len([]rune(content))
	if runes == 0 {
		return nil
	}
	var exceeded []adaptationStyleCount
	for _, item := range adaptationStylePatterns {
		count := len(item.pattern.FindAllStringIndex(content, -1))
		threshold := max(1, (runes*item.perThousandMax+999)/1000)
		if count > threshold {
			exceeded = append(exceeded, adaptationStyleCount{name: item.name, count: count, threshold: threshold})
		}
	}
	if duplicate := repeatedLongSentence(content); duplicate != "" {
		exceeded = append(exceeded, adaptationStyleCount{name: "repeated_long_sentence", count: 2, threshold: 1})
	}
	sort.Slice(exceeded, func(i, j int) bool {
		left := exceeded[i].count - exceeded[i].threshold
		right := exceeded[j].count - exceeded[j].threshold
		if left != right {
			return left > right
		}
		return exceeded[i].name < exceeded[j].name
	})
	if len(exceeded) > 3 {
		exceeded = exceeded[:3]
	}
	issues := make([]string, 0, len(exceeded))
	for _, item := range exceeded {
		issues = append(issues, fmt.Sprintf("adaptation_style: %s count=%d exceeds chapter threshold=%d", item.name, item.count, item.threshold))
	}
	return issues
}

func repeatedLongSentence(content string) string {
	seen := make(map[string]bool)
	for _, sentence := range regexp.MustCompile(`[。！？\n]+`).Split(content, -1) {
		sentence = strings.Join(strings.Fields(sentence), "")
		if len([]rune(sentence)) < 18 {
			continue
		}
		if seen[sentence] {
			return sentence
		}
		seen[sentence] = true
	}
	return ""
}

func adaptationResidueIssues(content string) []string {
	if hit := adaptationParentheticalResidueRe.FindString(content); hit != "" {
		return []string{fmt.Sprintf("adaptation_quality: draft contains parenthetical patch label %q; rewrite it as normal prose", shortenRunes(hit, 80))}
	}
	for _, fragment := range adaptationForbiddenFragments {
		if strings.Contains(content, fragment) {
			return []string{fmt.Sprintf("adaptation_quality: draft contains patch label %q; rewrite it as normal prose", fragment)}
		}
	}
	return nil
}

func adaptationSourceSimilarityIssues(st *store.Store, chapterPlan domain.AdaptationChapterPlan, chapter int, content string) []string {
	if st == nil || !adaptationRequiresVisibleChanges(chapterPlan) {
		return nil
	}
	source := loadAdaptationSourceText(st, chapterPlan.SourceChapters)
	sourceRunes := compactNonSpaceRunes(source)
	draftRunes := compactNonSpaceRunes(content)
	if len(sourceRunes) < adaptationSimilarityMinRunes || len(draftRunes) < adaptationSimilarityMinRunes {
		return nil
	}
	similarity, ok := shingleJaccard(sourceRunes, draftRunes, adaptationSimilarityShingleRunes)
	if !ok || similarity < adaptationSimilarityLimit {
		return nil
	}
	return []string{fmt.Sprintf(
		"adaptation_source_similarity: chapter %d is %.1f%% similar to source refs despite required_changes; rewrite affected full scene units as new prose",
		chapter, similarity*100,
	)}
}

func adaptationChangeEvidenceIssues(plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, evidence []domain.AdaptationChangeEvidence) []string {
	if plan == nil ||
		(plan.RewritePolicy != domain.AdaptationRewritePreserveDetails && plan.RewritePolicy != domain.AdaptationRewriteFullRewrite) ||
		!adaptationRequiresExplicitChangeEvidence(plan, chapterPlan) {
		return nil
	}
	if len(evidence) == 0 {
		return []string{"adaptation_change_evidence: preserve_details/full_rewrite with required_changes must provide change_evidence"}
	}
	var issues []string
	for i, item := range evidence {
		if strings.TrimSpace(item.Change) == "" {
			issues = append(issues, fmt.Sprintf("adaptation_change_evidence: item %d missing change", i+1))
		}
		if strings.TrimSpace(item.Integration) == "" {
			issues = append(issues, fmt.Sprintf("adaptation_change_evidence: item %d missing integration", i+1))
		}
		if item.SourceChapter <= 0 && strings.TrimSpace(item.SourceAnchor) == "" {
			issues = append(issues, fmt.Sprintf("adaptation_change_evidence: item %d needs source_chapter or source_anchor", i+1))
		}
	}
	return issues
}

func adaptationRequiresVisibleChanges(chapterPlan domain.AdaptationChapterPlan) bool {
	if len(chapterPlan.RuleIDs) > 0 {
		return true
	}
	for _, change := range chapterPlan.RequiredChanges {
		if strings.TrimSpace(change) != "" {
			return true
		}
	}
	return false
}

func adaptationRequiresExplicitChangeEvidence(plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan) bool {
	if plan == nil || !adaptationRequiresVisibleChanges(chapterPlan) {
		return false
	}
	if plan.RewritePolicy == domain.AdaptationRewriteFullRewrite {
		return len(chapterPlan.RequiredChanges) > 0
	}
	return plan.RewritePolicy == domain.AdaptationRewritePreserveDetails
}

// Arc/full_rewrite chapters are allowed to transform an assigned source event
// into a new target-story beat. When the plan explicitly declares visible
// changes, requiring a verbatim source-event quote turns a valid rewrite into
// a repair loop. Preserve-details chapters, and full rewrites without an
// explicit change contract, retain the stricter body-evidence fallback.
func adaptationBodyEvidenceRequired(plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan) bool {
	if plan == nil {
		return true
	}
	return !(plan.RewritePolicy == domain.AdaptationRewriteFullRewrite && adaptationRequiresExplicitChangeEvidence(plan, chapterPlan))
}

func adaptationQualityRepairStep(issues []string, chapter int) string {
	for _, issue := range issues {
		switch {
		case strings.Contains(issue, "adaptation_quality"):
			return fmt.Sprintf("do not commit chapter %d; call draft_chapter(mode=\"write\") and 删除所有 meta labels, writing the material as normal narration, action, dialogue, or subtext", chapter)
		case strings.Contains(issue, "adaptation_source_similarity"):
			return fmt.Sprintf("do not commit chapter %d; read source refs, keep unaffected paragraphs if needed, and rewrite every required-change scene unit as original prose", chapter)
		case strings.Contains(issue, "adaptation_change_evidence"):
			return fmt.Sprintf("do not commit chapter %d; call check_adaptation with a non-empty change_evidence JSON array. Each item must include source_chapter or source_anchor, change, and integration. Do not put evidence only in summary", chapter)
		case strings.Contains(issue, "adaptation_body_evidence"):
			return fmt.Sprintf("do not commit chapter %d; use assigned_event_evidence from the tool result to identify each event_id, ensure every assigned event is present in prose, then call check_adaptation with a verbatim draft quote that proves that exact event", chapter)
		case strings.Contains(issue, "adaptation_style"):
			return fmt.Sprintf("do not commit chapter %d; rewrite the reported high-frequency style patterns, then rerun check_adaptation", chapter)
		}
	}
	return ""
}

func adaptationProseQualityRepairStep(issues []string, chapter int) string {
	for _, issue := range issues {
		if strings.Contains(issue, "adaptation_quality") || strings.Contains(issue, "adaptation_source_similarity") {
			return adaptationQualityRepairStep([]string{issue}, chapter)
		}
	}
	return ""
}

func cleanChangeEvidence(items []domain.AdaptationChangeEvidence) []domain.AdaptationChangeEvidence {
	out := make([]domain.AdaptationChangeEvidence, 0, len(items))
	for _, item := range items {
		item.SourceAnchor = strings.TrimSpace(item.SourceAnchor)
		item.Change = strings.TrimSpace(item.Change)
		item.Integration = strings.TrimSpace(item.Integration)
		if item.SourceChapter <= 0 && item.SourceAnchor == "" && item.Change == "" && item.Integration == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func cleanAdaptationBodyEvidence(items []domain.AdaptationBodyEvidence) []domain.AdaptationBodyEvidence {
	out := make([]domain.AdaptationBodyEvidence, 0, len(items))
	seen := make(map[string]bool)
	for _, item := range items {
		item.EventID = strings.TrimSpace(item.EventID)
		item.Quote = strings.TrimSpace(item.Quote)
		key := item.EventID + "\x00" + item.Quote
		if item.EventID == "" && item.Quote == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func adaptationBodyEvidenceIssues(
	plan *domain.AdaptationPlan,
	chapterPlan domain.AdaptationChapterPlan,
	content string,
	evidence []domain.AdaptationBodyEvidence,
	fulfilledByPriorChapter map[string]int,
) []string {
	if !adaptationBodyEvidenceRequired(plan, chapterPlan) {
		return nil
	}
	required := make(map[string]bool)
	descriptions := make(map[string]string)
	if plan != nil {
		for _, event := range append(append([]domain.AdaptationEvent(nil), plan.SourceEvents...), plan.TargetEventLedger...) {
			descriptions[event.ID] = event.Description
		}
	}
	for _, eventID := range chapterPlan.EventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID != "" {
			required[eventID] = true
		}
	}
	if len(required) == 0 {
		return nil
	}
	proved := make(map[string]bool)
	for eventID := range fulfilledByPriorChapter {
		if required[eventID] {
			proved[eventID] = true
		}
	}
	var issues []string
	for index, item := range evidence {
		if !required[item.EventID] {
			issues = append(issues, fmt.Sprintf("adaptation_body_evidence: item %d event_id %q is not assigned to this chapter", index+1, item.EventID))
			continue
		}
		if fulfilledByPriorChapter[item.EventID] > 0 {
			continue
		}
		if len([]rune(item.Quote)) < 4 || !strings.Contains(content, item.Quote) {
			issues = append(issues, fmt.Sprintf("adaptation_body_evidence: event %s quote is absent from the current draft", item.EventID))
			continue
		}
		if !adaptationEvidenceSupportsEvent(descriptions[item.EventID], item.Quote) {
			description := strings.TrimSpace(descriptions[item.EventID])
			if description == "" {
				issues = append(issues, fmt.Sprintf("adaptation_body_evidence: event %s quote exists but does not support the assigned event", item.EventID))
			} else {
				issues = append(issues, fmt.Sprintf("adaptation_body_evidence: event %s quote exists but does not support the assigned event (event: %s)", item.EventID, description))
			}
			continue
		}
		proved[item.EventID] = true
	}
	for eventID := range required {
		if !proved[eventID] {
			description := strings.TrimSpace(descriptions[eventID])
			if description == "" {
				issues = append(issues, fmt.Sprintf("adaptation_body_evidence: assigned event %s has no verified prose quote", eventID))
				continue
			}
			issues = append(issues, fmt.Sprintf("adaptation_body_evidence: assigned event %s has no verified prose quote (event: %s)", eventID, description))
		}
	}
	sort.Strings(issues)
	return issues
}

func adaptationEvidenceSupportsEvent(description, quote string) bool {
	description = strings.TrimSpace(description)
	quote = strings.TrimSpace(quote)
	if description == "" {
		return true
	}
	semanticGroups := []struct {
		eventPattern *regexp.Regexp
		quotePattern *regexp.Regexp
	}{
		{regexp.MustCompile(`初遇|相识|首次|第一次|照面|相逢`), regexp.MustCompile(`初遇|相识|首次|第一次|照面|相逢|见面|名字|自我介绍`)},
		{regexp.MustCompile(`案件|案发|线索|真相`), regexp.MustCompile(`案|线索|调查|真相|证据|嫌疑`)},
		{regexp.MustCompile(`遇劫|绑架|救|相助|出手`), regexp.MustCompile(`劫|绑|救|援|出手|脱险|拦`)},
		{regexp.MustCompile(`身份|揭露|揭晓|暴露`), regexp.MustCompile(`身份|真名|揭露|揭晓|暴露|原来`)},
		{regexp.MustCompile(`关系|恋爱|订婚|结婚|决裂|复合|债务`), regexp.MustCompile(`关系|信任|爱|订婚|结婚|决裂|复合|欠|还`)},
	}
	for _, group := range semanticGroups {
		if group.eventPattern.MatchString(description) {
			return group.quotePattern.MatchString(quote)
		}
	}
	descriptionRunes := []rune(strings.Join(strings.Fields(description), ""))
	for width := 4; width >= 2; width-- {
		for index := 0; index+width <= len(descriptionRunes); index++ {
			if strings.Contains(quote, string(descriptionRunes[index:index+width])) {
				return true
			}
		}
	}
	return false
}

func loadAdaptationSourceText(st *store.Store, refs []int) string {
	if st == nil || len(refs) == 0 {
		return ""
	}
	var parts []string
	for _, ref := range refs {
		text, _, err := st.Adaptation.LoadSourceChapter(ref)
		if err != nil {
			continue
		}
		text = strings.TrimSpace(text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func compactNonSpaceRunes(text string) []rune {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func shingleJaccard(a, b []rune, size int) (float64, bool) {
	if size <= 0 || len(a) < size || len(b) < size {
		return 0, false
	}
	left := make(map[string]struct{}, len(a)-size+1)
	for i := 0; i <= len(a)-size; i++ {
		left[string(a[i:i+size])] = struct{}{}
	}
	seenRight := make(map[string]struct{}, len(b)-size+1)
	common := 0
	union := len(left)
	for i := 0; i <= len(b)-size; i++ {
		shingle := string(b[i : i+size])
		if _, ok := seenRight[shingle]; ok {
			continue
		}
		seenRight[shingle] = struct{}{}
		if _, ok := left[shingle]; ok {
			common++
			continue
		}
		union++
	}
	if union == 0 {
		return 0, false
	}
	return float64(common) / float64(union), true
}

func shortenRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}
