package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	// Adaptation prompts keep a larger structured-rule window than generic
	// prompts. Longer briefs remain complete in evidence and durable storage.
	AdaptationPromptMaxRules          = 64
	AdaptationPromptMaxForbiddenRules = 32
)

type AdaptationModePolicy string

const (
	AdaptationPolicyDetailPreservationWithSplit AdaptationModePolicy = "detail_preservation_with_split"
	AdaptationPolicyMainlinePreservation        AdaptationModePolicy = "mainline_preservation"
	AdaptationPolicyTargetCoherence             AdaptationModePolicy = "target_coherence"
)

func AdaptationModePolicyForGranularity(granularity string) AdaptationModePolicy {
	switch NormalizeAdaptationGranularity(granularity) {
	case AdaptationGranularityArc:
		return AdaptationPolicyMainlinePreservation
	case AdaptationGranularityFree:
		return AdaptationPolicyTargetCoherence
	default:
		return AdaptationPolicyDetailPreservationWithSplit
	}
}

type AdaptationEventOrigin string

const (
	AdaptationEventOriginSource AdaptationEventOrigin = "source"
	AdaptationEventOriginAdded  AdaptationEventOrigin = "added"
	AdaptationEventOriginTarget AdaptationEventOrigin = "target"
)

type AdaptationEventImportance string

const (
	AdaptationEventMainline   AdaptationEventImportance = "mainline"
	AdaptationEventSupporting AdaptationEventImportance = "supporting"
	AdaptationEventTexture    AdaptationEventImportance = "texture"
)

type AdaptationEvent struct {
	ID                 string                            `json:"event_id"`
	Description        string                            `json:"description"`
	Origin             AdaptationEventOrigin             `json:"origin"`
	Importance         AdaptationEventImportance         `json:"importance"`
	SourceChapter      int                               `json:"source_chapter,omitempty"`
	Evidence           string                            `json:"evidence,omitempty"`
	DependsOn          []string                          `json:"depends_on,omitempty"`
	RelationshipChange string                            `json:"relationship_change,omitempty"`
	Relationship       *AdaptationRelationshipTransition `json:"relationship,omitempty"`
	SettingClaims      []AdaptationSettingClaim          `json:"setting_claims,omitempty"`
	Required           bool                              `json:"required,omitempty"`
}

type AdaptationRelationshipTransition struct {
	Pair             string   `json:"pair"`
	From             string   `json:"from"`
	To               string   `json:"to"`
	AllowedFrom      []string `json:"allowed_from,omitempty"`
	RequiresEventIDs []string `json:"requires_event_ids,omitempty"`
}

type AdaptationSettingClaim struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type AdaptationSettingLock struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type AdaptationRuleKind string

const (
	AdaptationRuleRequired  AdaptationRuleKind = "required"
	AdaptationRuleForbidden AdaptationRuleKind = "forbidden"
	AdaptationRuleGuidance  AdaptationRuleKind = "guidance"
)

type AdaptationRule struct {
	ID          string             `json:"rule_id"`
	Kind        AdaptationRuleKind `json:"kind"`
	Text        string             `json:"text"`
	Mode        string             `json:"mode,omitempty"`
	ChapterFrom int                `json:"chapter_from,omitempty"`
	ChapterTo   int                `json:"chapter_to,omitempty"`
}

var (
	mainlineEventPattern = regexp.MustCompile(`初遇|相识|首次(?:见面|照面|相逢)|第一(?:次)?(?:见面|照面|相逢)|决定同行|结伴|形成[^，。；]{0,16}关系|遇劫|绑架|劫案|案发|案件|救(?:人|助)|身份(?:揭示|暴露)|揭[露晓]|真相|死亡|牺牲|命运|决裂|复合|订婚|结婚|恋爱|债务|报仇|决战|转折|伏笔|兑现`)
	textureEventPattern  = regexp.MustCompile(`吃饭|闲聊|天气|穿着|路过|日常琐事|环境描写`)
	chapterScopePattern  = regexp.MustCompile(`第\s*(\d+)\s*(?:[-—–至到]\s*(\d+)\s*)?章`)
)

// EnsureAdaptationSourceEvents upgrades legacy source reports without another
// model call. Explicit analyzer classifications remain authoritative.
func EnsureAdaptationSourceEvents(report *AdaptationSourceReport) []AdaptationEvent {
	if report == nil {
		return nil
	}
	if len(report.SourceEvents) > 0 {
		out := append([]AdaptationEvent(nil), report.SourceEvents...)
		for index := range out {
			if strings.TrimSpace(out[index].ID) == "" {
				out[index].ID = StableAdaptationEventID(report.Chapter, index+1, out[index].Description)
			}
			if out[index].Origin == "" {
				out[index].Origin = AdaptationEventOriginSource
			}
			if out[index].SourceChapter <= 0 {
				out[index].SourceChapter = report.Chapter
			}
			if out[index].Importance == "" {
				out[index].Importance = ClassifyAdaptationSourceEvent(out[index].Description)
			}
			out[index].Required = out[index].Importance == AdaptationEventMainline
		}
		report.SourceEvents = out
		return out
	}
	out := make([]AdaptationEvent, 0, len(report.KeyEvents))
	for index, description := range report.KeyEvents {
		description = strings.TrimSpace(description)
		if description == "" {
			continue
		}
		importance := ClassifyAdaptationSourceEvent(description)
		out = append(out, AdaptationEvent{
			ID:            StableAdaptationEventID(report.Chapter, index+1, description),
			Description:   description,
			Origin:        AdaptationEventOriginSource,
			Importance:    importance,
			SourceChapter: report.Chapter,
			Evidence:      description,
			Required:      importance == AdaptationEventMainline,
		})
	}
	report.SourceEvents = out
	return out
}

func StableAdaptationEventID(sourceChapter, ordinal int, description string) string {
	normalized := normalizeAdaptationRuleText(description)
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("src-%04d-e%02d-%s", sourceChapter, ordinal, hex.EncodeToString(sum[:4]))
}

func ClassifyAdaptationSourceEvent(description string) AdaptationEventImportance {
	description = strings.TrimSpace(description)
	switch {
	case mainlineEventPattern.MatchString(description):
		return AdaptationEventMainline
	case textureEventPattern.MatchString(description):
		return AdaptationEventTexture
	default:
		return AdaptationEventSupporting
	}
}

// CompileAdaptationRules turns a raw brief into stable, de-duplicated long
// term rules. Text is stored once on the plan; chapters carry only rule IDs.
func CompileAdaptationRules(brief, granularity string) []AdaptationRule {
	mode := NormalizeAdaptationGranularity(granularity)
	parts := strings.FieldsFunc(brief, func(r rune) bool {
		return r == '\n' || r == '。' || r == '；' || r == ';'
	})
	seen := make(map[string]bool)
	rules := make([]AdaptationRule, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part)
		normalized := normalizeAdaptationRuleText(text)
		if normalized == "" {
			continue
		}
		kind := AdaptationRuleGuidance
		if strings.Contains(text, "禁止") || strings.Contains(text, "严禁") || strings.Contains(text, "不要") ||
			strings.Contains(text, "不得") || strings.Contains(text, "不能") || strings.Contains(text, "不可") {
			kind = AdaptationRuleForbidden
		} else if strings.Contains(text, "必须") || strings.Contains(text, "务必") || strings.Contains(text, "只能") ||
			strings.Contains(text, "保留") || strings.Contains(text, "需要") {
			kind = AdaptationRuleRequired
		}
		dedupeKey := string(kind) + ":" + normalized
		if seen[dedupeKey] {
			continue
		}
		seen[dedupeKey] = true
		from, to := adaptationRuleChapterScope(text)
		sum := sha256.Sum256([]byte(string(kind) + ":" + normalized))
		rules = append(rules, AdaptationRule{
			ID:          "brief-" + hex.EncodeToString(sum[:6]),
			Kind:        kind,
			Text:        text,
			Mode:        mode,
			ChapterFrom: from,
			ChapterTo:   to,
		})
	}
	return rules
}

func ValidateAdaptationRules(rules []AdaptationRule) error {
	kindsByText := make(map[string]AdaptationRuleKind)
	for _, rule := range rules {
		normalized := normalizeAdaptationRuleSubject(rule.Text)
		if normalized == "" {
			continue
		}
		previous, exists := kindsByText[normalized]
		if exists && ((previous == AdaptationRuleRequired && rule.Kind == AdaptationRuleForbidden) ||
			(previous == AdaptationRuleForbidden && rule.Kind == AdaptationRuleRequired)) {
			return fmt.Errorf("adaptation rule conflict: the same requirement is both required and forbidden")
		}
		kindsByText[normalized] = rule.Kind
	}
	return nil
}

func normalizeAdaptationRuleSubject(text string) string {
	text = strings.TrimSpace(text)
	for {
		before := text
		for _, prefix := range []string{"必须", "需要", "应当", "应该", "不得", "禁止", "不要", "保留", "删除", "省略"} {
			text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
		}
		if text == before {
			break
		}
	}
	return normalizeAdaptationRuleText(text)
}

// SelectAdaptationPromptRules returns a bounded view for one model call while
// the complete brief and durable rule set remain untouched. Chapter-scoped
// rules and explicit directives take precedence over general guidance.
func SelectAdaptationPromptRules(rules []AdaptationRule, maxRules, maxForbidden int) []AdaptationRule {
	if maxRules <= 0 || len(rules) == 0 {
		return nil
	}

	scoped := make([]AdaptationRule, 0, len(rules))
	directives := make([]AdaptationRule, 0, len(rules))
	guidance := make([]AdaptationRule, 0, len(rules))
	for _, rule := range rules {
		switch {
		case rule.ChapterFrom > 0 || rule.ChapterTo > 0:
			scoped = append(scoped, rule)
		case rule.Kind == AdaptationRuleRequired || rule.Kind == AdaptationRuleForbidden:
			directives = append(directives, rule)
		default:
			guidance = append(guidance, rule)
		}
	}

	selected := make([]AdaptationRule, 0, min(len(rules), maxRules))
	forbidden := 0
	for _, bucket := range [][]AdaptationRule{scoped, directives, guidance} {
		for _, rule := range bucket {
			if len(selected) >= maxRules {
				return selected
			}
			if rule.Kind == AdaptationRuleForbidden {
				if maxForbidden > 0 && forbidden >= maxForbidden {
					continue
				}
				forbidden++
			}
			selected = append(selected, rule)
		}
	}
	return selected
}

func ApplicableAdaptationRules(rules []AdaptationRule, mode string, chapter int) []AdaptationRule {
	mode = NormalizeAdaptationGranularity(mode)
	out := make([]AdaptationRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Mode != "" && NormalizeAdaptationGranularity(rule.Mode) != mode {
			continue
		}
		if rule.ChapterFrom > 0 && chapter < rule.ChapterFrom {
			continue
		}
		if rule.ChapterTo > 0 && chapter > rule.ChapterTo {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func AdaptationRuleIDs(rules []AdaptationRule) []string {
	ids := make([]string, 0, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule.ID) != "" {
			ids = append(ids, rule.ID)
		}
	}
	return ids
}

func adaptationRuleChapterScope(text string) (int, int) {
	match := chapterScopePattern.FindStringSubmatch(text)
	if len(match) == 0 {
		return 0, 0
	}
	var from, to int
	_, _ = fmt.Sscanf(match[1], "%d", &from)
	to = from
	if len(match) > 2 && match[2] != "" {
		_, _ = fmt.Sscanf(match[2], "%d", &to)
	}
	return from, to
}

func normalizeAdaptationRuleText(text string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
