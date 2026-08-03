package tools

import (
	"fmt"
	"math"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Full-rewrite/free chapter budgets are planning estimates. A complete,
// quality-passing chapter may reasonably exceed the estimate; only a material
// overage should remain visible as a budget warning. Preserve-details keeps
// its existing hard contract below.
const adaptationSoftBudgetOverageRatio = 0.25

// EnsureChapterExpanded verifies that a chapter is inside the currently
// expanded layered outline. Non-layered books and non-writing phases have no
// layered range constraint.
func EnsureChapterExpanded(st *store.Store, chapter int) error {
	if st == nil || chapter <= 0 {
		return nil
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil || !progress.Layered || progress.Phase != domain.PhaseWriting {
		return nil
	}
	boundary, err := st.Outline.CheckArcBoundary(chapter)
	if err != nil {
		return fmt.Errorf("check layered outline: %w: %w", errs.ErrStoreRead, err)
	}
	if boundary != nil {
		return nil
	}
	return fmt.Errorf(
		"第 %d 章不在分层大纲范围内：写作必须先 expand_arc 扩展弧或 append_volume 追加卷；若全书已完结请调 save_foundation type=complete_book: %w",
		chapter, errs.ErrToolPrecondition)
}

// EnsureAdaptationChapterPlanned is the physical boundary for adaptation
// projects: writer-facing tools may only touch chapters in the confirmed plan.
func EnsureAdaptationChapterPlanned(st *store.Store, chapter int) error {
	if st == nil || chapter <= 0 || !st.Adaptation.Active() {
		return nil
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil {
		return fmt.Errorf("load adaptation plan: %w: %w", errs.ErrStoreRead, err)
	}
	if plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return fmt.Errorf("改编计划尚未确认，不能进入写作: %w", errs.ErrToolPrecondition)
	}
	if _, ok := findAdaptationChapterPlan(plan, chapter); ok {
		return nil
	}
	return fmt.Errorf("改编计划中没有第 %d 章。当前 confirmed plan 只有 %d 章；如需增删/重排章节，请重新生成规模提案并确认: %w",
		chapter, len(plan.Chapters), errs.ErrToolPrecondition)
}

type adaptationWordContract struct {
	RewritePolicy       string   `json:"rewrite_policy"`
	Hard                bool     `json:"hard"`
	BudgetPolicy        string   `json:"budget_policy"`
	BudgetStatus        string   `json:"budget_status,omitempty"`
	Scope               string   `json:"scope,omitempty"`
	Chapter             int      `json:"chapter"`
	SourceRunes         int      `json:"source_runes,omitempty"`
	TargetRunes         int      `json:"target_runes,omitempty"`
	TargetMinRunes      int      `json:"target_min_runes,omitempty"`
	TargetMaxRunes      int      `json:"target_max_runes,omitempty"`
	SoftMaxRunes        int      `json:"soft_max_runes,omitempty"`
	ActualRunes         int      `json:"actual_runes,omitempty"`
	SourceTotalRunes    int      `json:"source_total_runes,omitempty"`
	TargetTotalRunes    int      `json:"target_total_runes,omitempty"`
	TargetTotalMinRunes int      `json:"target_total_min_runes,omitempty"`
	TargetTotalMaxRunes int      `json:"target_total_max_runes,omitempty"`
	SoftTotalMaxRunes   int      `json:"soft_total_max_runes,omitempty"`
	ProjectedTotalRunes int      `json:"projected_total_runes,omitempty"`
	TotalDeltaRunes     int      `json:"total_delta_runes,omitempty"`
	TotalDeltaRatio     float64  `json:"total_delta_ratio,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
}

func buildAdaptationWordContract(st *store.Store, plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, chapter, actualRunes int) adaptationWordContract {
	rewritePolicy := domain.NormalizeAdaptationRewritePolicy(plan.RewritePolicy)
	hard := rewritePolicy == domain.AdaptationRewritePreserveDetails
	contract := adaptationWordContract{
		RewritePolicy:       rewritePolicy,
		Hard:                hard,
		BudgetPolicy:        "soft",
		Chapter:             chapter,
		SourceRunes:         chapterPlan.SourceRunes,
		TargetRunes:         chapterPlan.TargetRunes,
		TargetMinRunes:      chapterPlan.TargetMinRunes,
		TargetMaxRunes:      chapterPlan.TargetMaxRunes,
		ActualRunes:         actualRunes,
		SourceTotalRunes:    plan.SourceTotalRunes,
		TargetTotalRunes:    plan.TargetTotalRunes,
		TargetTotalMinRunes: plan.TargetMinRunes,
		TargetTotalMaxRunes: plan.TargetMaxRunes,
	}
	if hard {
		contract.BudgetPolicy = "hard"
	}
	if plan.Granularity == domain.AdaptationGranularityFree {
		contract.Scope = "total"
	} else {
		contract.Scope = "chapter"
	}
	if !hard {
		contract.SoftMaxRunes = adaptationSoftBudgetMax(chapterPlan.TargetMaxRunes)
		contract.SoftTotalMaxRunes = adaptationSoftBudgetMax(plan.TargetMaxRunes)
	}
	contract.ProjectedTotalRunes = projectedAdaptationTotalRunes(st, chapter, actualRunes)
	if plan.TargetTotalRunes > 0 {
		contract.TotalDeltaRunes = contract.ProjectedTotalRunes - plan.TargetTotalRunes
		contract.TotalDeltaRatio = float64(contract.TotalDeltaRunes) / float64(plan.TargetTotalRunes)
		contract.TotalDeltaRatio = math.Round(contract.TotalDeltaRatio*10000) / 10000
	}
	contract.BudgetStatus = adaptationWordBudgetStatus(plan, contract)
	contract.Warnings = adaptationWordContractWarningsForContract(plan, contract)
	return contract
}

func adaptationSoftBudgetMax(plannedMax int) int {
	if plannedMax <= 0 {
		return 0
	}
	extra := int(math.Ceil(float64(plannedMax) * adaptationSoftBudgetOverageRatio))
	if extra < 1 {
		extra = 1
	}
	return plannedMax + extra
}

func adaptationWordBudgetStatus(plan *domain.AdaptationPlan, contract adaptationWordContract) string {
	if contract.ActualRunes <= 0 {
		return "not_started"
	}
	if contract.Hard {
		if contract.Scope != "total" {
			switch {
			case contract.TargetMinRunes > 0 && contract.ActualRunes < contract.TargetMinRunes:
				return "below_hard_range"
			case contract.TargetMaxRunes > 0 && contract.ActualRunes > contract.TargetMaxRunes:
				return "above_hard_range"
			default:
				return "within_hard_range"
			}
		}
		return "hard_total_contract"
	}
	if contract.Scope != "total" {
		switch {
		case contract.TargetMinRunes > 0 && contract.ActualRunes < contract.TargetMinRunes:
			return "below_planned_range"
		case contract.TargetMaxRunes <= 0 || contract.ActualRunes <= contract.TargetMaxRunes:
			return "within_planned_range"
		case contract.SoftMaxRunes <= 0 || contract.ActualRunes <= contract.SoftMaxRunes:
			return "within_soft_overage"
		default:
			return "materially_over_planned_range"
		}
	}
	if !isLastAdaptationChapter(plan, contract.Chapter) {
		return "planned_total_pending"
	}
	switch {
	case contract.TargetTotalMinRunes > 0 && contract.ProjectedTotalRunes < contract.TargetTotalMinRunes:
		return "below_planned_total"
	case contract.TargetTotalMaxRunes <= 0 || contract.ProjectedTotalRunes <= contract.TargetTotalMaxRunes:
		return "within_planned_total"
	case contract.SoftTotalMaxRunes <= 0 || contract.ProjectedTotalRunes <= contract.SoftTotalMaxRunes:
		return "within_soft_total_overage"
	default:
		return "materially_over_planned_total"
	}
}

func adaptationWordContractIssues(st *store.Store, plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, chapter, actualRunes int) []string {
	if plan == nil || plan.RewritePolicy != domain.AdaptationRewritePreserveDetails {
		return nil
	}
	contract := buildAdaptationWordContract(st, plan, chapterPlan, chapter, actualRunes)
	if contract.Scope != "total" && chapterPlan.TargetMinRunes > 0 && actualRunes < chapterPlan.TargetMinRunes {
		return []string{fmt.Sprintf("adaptation_word_contract: 第 %d 章 %d 字，低于硬区间 %d-%d 字",
			chapter, actualRunes, chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes)}
	}
	if contract.Scope != "total" && chapterPlan.TargetMaxRunes > 0 && actualRunes > chapterPlan.TargetMaxRunes {
		return []string{fmt.Sprintf("adaptation_word_contract: 第 %d 章 %d 字，超过硬区间 %d-%d 字",
			chapter, actualRunes, chapterPlan.TargetMinRunes, chapterPlan.TargetMaxRunes)}
	}
	if !isLastAdaptationChapter(plan, chapter) {
		return nil
	}
	if plan.TargetMinRunes > 0 && contract.ProjectedTotalRunes < plan.TargetMinRunes {
		return []string{fmt.Sprintf("adaptation_word_contract: 当前总字数 %d，低于来源总字数硬区间 %d-%d",
			contract.ProjectedTotalRunes, plan.TargetMinRunes, plan.TargetMaxRunes)}
	}
	if plan.TargetMaxRunes > 0 && contract.ProjectedTotalRunes > plan.TargetMaxRunes {
		return []string{fmt.Sprintf("adaptation_word_contract: 当前总字数 %d，超过来源总字数硬区间 %d-%d",
			contract.ProjectedTotalRunes, plan.TargetMinRunes, plan.TargetMaxRunes)}
	}
	return nil
}

func adaptationWordContractWarnings(st *store.Store, plan *domain.AdaptationPlan, chapterPlan domain.AdaptationChapterPlan, chapter, actualRunes int) []string {
	if plan == nil || plan.RewritePolicy == domain.AdaptationRewritePreserveDetails {
		return nil
	}
	contract := buildAdaptationWordContract(st, plan, chapterPlan, chapter, actualRunes)
	return append([]string(nil), contract.Warnings...)
}

func adaptationWordContractWarningsForContract(plan *domain.AdaptationPlan, contract adaptationWordContract) []string {
	if plan == nil || contract.Hard || contract.ActualRunes <= 0 {
		return nil
	}
	var warnings []string
	if contract.Scope != "total" && contract.TargetMinRunes > 0 && contract.ActualRunes < contract.TargetMinRunes {
		warnings = append(warnings, fmt.Sprintf("adaptation_word_contract_soft: chapter %d has %d runes, below soft range %d-%d",
			contract.Chapter, contract.ActualRunes, contract.TargetMinRunes, contract.TargetMaxRunes))
	}
	if contract.Scope != "total" && contract.TargetMaxRunes > 0 &&
		contract.ActualRunes > contract.SoftMaxRunes {
		warnings = append(warnings, fmt.Sprintf("adaptation_word_contract_soft: chapter %d has %d runes, above soft range %d-%d",
			contract.Chapter, contract.ActualRunes, contract.TargetMinRunes, contract.SoftMaxRunes))
	}
	if !isLastAdaptationChapter(plan, contract.Chapter) {
		return warnings
	}
	if contract.TargetTotalMinRunes > 0 && contract.ProjectedTotalRunes < contract.TargetTotalMinRunes {
		warnings = append(warnings, fmt.Sprintf("adaptation_word_contract_soft: projected total %d is below soft range %d-%d",
			contract.ProjectedTotalRunes, contract.TargetTotalMinRunes, contract.TargetTotalMaxRunes))
	}
	if contract.TargetTotalMaxRunes > 0 && contract.ProjectedTotalRunes > contract.SoftTotalMaxRunes {
		warnings = append(warnings, fmt.Sprintf("adaptation_word_contract_soft: projected total %d is above soft range %d-%d",
			contract.ProjectedTotalRunes, contract.TargetTotalMinRunes, contract.SoftTotalMaxRunes))
	}
	return warnings
}

func adaptationWordContractStatus(st *store.Store, chapter, actualRunes int) (adaptationWordContract, []string, bool) {
	if st == nil || chapter <= 0 || !st.Adaptation.Active() {
		return adaptationWordContract{}, nil, false
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed {
		return adaptationWordContract{}, nil, false
	}
	chapterPlan, ok := findAdaptationChapterPlan(plan, chapter)
	if !ok {
		return adaptationWordContract{}, nil, false
	}
	contract := buildAdaptationWordContract(st, plan, chapterPlan, chapter, actualRunes)
	return contract, adaptationWordContractIssues(st, plan, chapterPlan, chapter, actualRunes), true
}

func adaptationWordContractRepairStep(contract adaptationWordContract, issues []string, chapter int) string {
	if len(issues) == 0 || !contract.Hard {
		return ""
	}
	hasWordContractIssue := false
	for _, issue := range issues {
		if strings.Contains(issue, "adaptation_word_contract") {
			hasWordContractIssue = true
			break
		}
	}
	if !hasWordContractIssue {
		return ""
	}
	switch {
	case contract.Scope != "total" && contract.TargetMinRunes > 0 && contract.ActualRunes < contract.TargetMinRunes:
		return fmt.Sprintf(
			"不要再次调用 commit_chapter，也不要只提交改动片段；preserve_details 不是只写改动片段。先调用 read_chapter(source=\"source\", chapter=%d) 读完整原文章节；再用 draft_chapter(mode=\"write\") 写入完整章节正文，把未受改编目标影响的原文细节/场景承接保留进草稿，只重写受影响部分；当前 %d 字，必须进入硬区间 %d-%d 字。",
			chapter, contract.ActualRunes, contract.TargetMinRunes, contract.TargetMaxRunes,
		)
	case contract.Scope != "total" && contract.TargetMaxRunes > 0 && contract.ActualRunes > contract.TargetMaxRunes:
		return fmt.Sprintf(
			"不要再次调用 commit_chapter。先按 read_chapter(source=\"source\", chapter=%d) 对照原文，使用 draft_chapter(mode=\"write\") 重写完整章节并压缩到硬区间 %d-%d 字；当前 %d 字。",
			chapter, contract.TargetMinRunes, contract.TargetMaxRunes, contract.ActualRunes,
		)
	case strings.Contains(issues[0], "当前总字数"):
		return fmt.Sprintf(
			"不要再次调用 commit_chapter。preserve_details 使用全书总字数硬契约，当前预计总字数 %d，必须回到 %d-%d；请重写当前完整章节以修正总量。",
			contract.ProjectedTotalRunes, contract.TargetTotalMinRunes, contract.TargetTotalMaxRunes,
		)
	default:
		return "不要再次调用 commit_chapter；先按 adaptation_word_contract 修正完整草稿并重新 check_adaptation。"
	}
}

func projectedAdaptationTotalRunes(st *store.Store, chapter, actualRunes int) int {
	if st == nil {
		return actualRunes
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		return actualRunes
	}
	total := actualRunes
	for ch, count := range progress.ChapterWordCounts {
		if ch != chapter {
			total += count
		}
	}
	return total
}

func isLastAdaptationChapter(plan *domain.AdaptationPlan, chapter int) bool {
	if plan == nil || len(plan.Chapters) == 0 {
		return false
	}
	maxChapter := 0
	for _, item := range plan.Chapters {
		if item.Chapter > maxChapter {
			maxChapter = item.Chapter
		}
	}
	return chapter >= maxChapter
}
