package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
)

const (
	DefaultAdaptationGranularity   = domain.AdaptationGranularityChapter
	DefaultAdaptationRewritePolicy = domain.AdaptationRewritePreserveDetails
	DefaultAdaptationWordTolerance = adapt.DefaultWordTolerance
)

// PrepareAdaptNovel turns a confirmed adaptation brief into a startup plan.
func PrepareAdaptNovel(req Request) (Plan, error) {
	brief := strings.TrimSpace(req.UserPrompt)
	if brief == "" {
		return Plan{}, fmt.Errorf("adaptation brief is required")
	}
	if strings.TrimSpace(req.NovelPath) == "" {
		return Plan{}, fmt.Errorf("novel path is required")
	}
	granularity := normalizeAdaptationGranularity(req.AdaptGranularity)
	rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
	wordTolerance := AdaptationWordToleranceForGranularity(granularity, req.AdaptWordTolerance)
	return Plan{
		Mode:               ModeAdaptNovel,
		DisplayName:        "小说改编",
		RawPrompt:          brief,
		AdaptGranularity:   granularity,
		AdaptRewritePolicy: rewritePolicy,
		AdaptWordTolerance: wordTolerance,
		AdaptSourcePath:    strings.TrimSpace(req.NovelPath),
	}, nil
}

func DefaultAdaptationBrief(granularity, rewritePolicy string, wordTolerance float64) string {
	granularity = normalizeAdaptationGranularity(granularity)
	rewritePolicy = domain.AdaptationRewritePolicyForGranularity(granularity)
	return strings.TrimSpace(fmt.Sprintf(`## 改编模式

%s

## 用户目标

基于原书主线进行改编，暂无额外偏好输入。

## 主线保留规则

- %s`,
		AdaptationModeContract(granularity, wordTolerance),
		strings.Join(defaultAdaptationMainlineRules(granularity, rewritePolicy), "\n- ")))
}

func AdaptationModeContract(granularity string, wordTolerance float64) string {
	granularity = normalizeAdaptationGranularity(granularity)
	rewritePolicy := domain.AdaptationRewritePolicyForGranularity(granularity)
	wordToleranceLabel := FormatAdaptationWordTolerance(granularity, wordTolerance)
	return strings.TrimSpace(fmt.Sprintf(`granularity=%s
rewrite_policy=%s
word_tolerance=%s
%s`,
		granularity, rewritePolicy, wordToleranceLabel, adaptationModeContractDetail(granularity)))
}

func adaptationModeContractDetail(granularity string) string {
	switch normalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return strings.TrimSpace(`mode_contract=free/full_rewrite
source_reference_policy=optional_background_anchor
mode_scope=允许重构章节结构、人物关系推进和结局走向；source_chapters/source_range 只是后台覆盖率与必要事实锚点，不表示目标章对应原著某章。
mode_instruction=以确认后的改编提案、分卷和章节细纲为准写原创正文；不要按原著逐章对照，也不要因为存在 source refs 就反复读取原文章节。`)
	case domain.AdaptationGranularityArc:
		return strings.TrimSpace(`mode_contract=arc/full_rewrite
source_reference_policy=mainline_anchor
mode_scope=允许合并、拆分和重排章节；source_chapters/source_range 是主线与弧线锚点，不是逐字复用许可。
mode_instruction=保持原著核心主线、人物命运与因果顺序，用新的章节组织和原创正文完成改写。`)
	default:
		return strings.TrimSpace(`mode_contract=chapter/preserve_details
source_reference_policy=required_one_to_one
mode_scope=目标章与原文章节一一对应；source_chapters/source_range 是逐章映射，写作前需要对照原文章节事实。
mode_instruction=未受改编目标影响的原文事件承接和细节可以保留；受影响的完整场景单元必须原创重写。`)
	}
}

func defaultAdaptationMainlineRules(granularity, rewritePolicy string) []string {
	switch normalizeAdaptationGranularity(granularity) {
	case domain.AdaptationGranularityFree:
		return []string{
			"以确认后的改编提案、分卷和章节细纲为准，允许结构和结局相对原书发生重构。",
			"保持用户明确要求保留的核心人物、主线动机和关键因果；已经改写出的新剧情连续性优先于逐章回看原文。",
			"source_chapters/source_range 仅用于后台覆盖率与必要事实查证，不显示、理解或执行为“本章对应原著第几章”。",
		}
	case domain.AdaptationGranularityArc:
		return []string{
			"保持原书核心事件、人物命运和因果顺序不变，但允许合并、拆分、重排章节来形成新的卷弧节奏。",
			"source_chapters/source_range 用于主线锚点和覆盖率，不要求目标章与原文章节一一对应。",
			"正文必须是 full_rewrite 的原创新文本，不搬运原文段落。",
		}
	default:
		_ = rewritePolicy
		return []string{
			"保持原书核心事件、人物命运和因果顺序不变。",
			"每章写作前对照 source_chapters 指向的原文章节事实。",
			"未受改编目标影响的场景承接可以保留；受影响的完整场景单元必须原创重写。",
		}
	}
}

func AdaptationWordToleranceForGranularity(granularity string, wordTolerance float64) float64 {
	granularity = normalizeAdaptationGranularity(granularity)
	if domain.AdaptationRewritePolicyForGranularity(granularity) != domain.AdaptationRewritePreserveDetails {
		return 0
	}
	if wordTolerance <= 0 {
		return DefaultAdaptationWordTolerance
	}
	return wordTolerance
}

func FormatAdaptationWordTolerance(granularity string, wordTolerance float64) string {
	wordTolerance = AdaptationWordToleranceForGranularity(granularity, wordTolerance)
	if wordTolerance <= 0 {
		return "disabled"
	}
	return fmt.Sprintf("%.2f", wordTolerance)
}

func normalizeAdaptationGranularity(value string) string {
	if strings.TrimSpace(value) == "" {
		return DefaultAdaptationGranularity
	}
	return domain.NormalizeAdaptationGranularity(value)
}

func normalizeAdaptationRewritePolicy(value string) string {
	if strings.TrimSpace(value) == "" {
		return DefaultAdaptationRewritePolicy
	}
	return domain.NormalizeAdaptationRewritePolicy(value)
}
