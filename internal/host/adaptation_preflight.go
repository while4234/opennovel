package host

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
)

// adaptationPreflightReport is intentionally small: the caller decides
// whether a successful repair should restart a stopped Coordinator.
type adaptationPreflightReport struct {
	Changed        bool
	BudgetMode     string
	BudgetAttempts int
	EventAttempts  int
	BudgetChapters []int
	EventChapters  []int
}

// prepareAdaptationPreflight is the single Host-level entry point for legacy
// adaptation contracts. It runs before Resume/Continue and can be called again
// after a Coordinator stops. Every repair is scoped to its owning contract:
// budget repair never changes story fields; event repair never changes prose.
func (h *Host) prepareAdaptationPreflight(ctx context.Context) (adaptationPreflightReport, error) {
	var report adaptationPreflightReport
	if h == nil || h.store == nil || h.store.Adaptation == nil {
		return report, nil
	}
	h.adaptationPreflightMu.Lock()
	defer h.adaptationPreflightMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := h.store.Adaptation.LoadPlan()
	if err != nil {
		return report, fmt.Errorf("load adaptation plan for preflight: %w", err)
	}
	if plan == nil || plan.Status != domain.AdaptationPlanStatusConfirmed ||
		domain.NormalizeAdaptationGranularity(plan.Granularity) != domain.AdaptationGranularityArc {
		return report, nil
	}

	current := *plan
	budgetBase := current
	budgetIssues := domain.ValidateArcChapterBudgetDensity(current)
	// Plans repaired by the previous deterministic migration have a reversible
	// backup but no durable mode marker. Re-analyze that legacy budget once,
	// merging only the new budget fields into today's plan.
	if len(budgetIssues) == 0 && current.BudgetRepair == nil {
		backup, _, backupErr := h.store.Adaptation.LoadLatestPlanBackup("auto-budget-density-repair")
		if backupErr != nil {
			return report, fmt.Errorf("load legacy budget migration backup: %w", backupErr)
		}
		if backup != nil {
			backupIssues := domain.ValidateArcChapterBudgetDensity(*backup)
			lineageMatches := domain.AdaptationPlanStoryContractSignature(*backup) == domain.AdaptationPlanStoryContractSignature(current) ||
				domain.AdaptationPlanBudgetRepairLineageSignature(*backup) == domain.AdaptationPlanBudgetRepairLineageSignature(current)
			if lineageMatches && len(backupIssues) > 0 {
				// Keep today's plot and event bindings as the prompt context. Borrow
				// only the legacy pre-repair budgets to identify the chapters that
				// still need a model decision; no backup story field is persisted.
				legacyChapters := make([]int, 0, len(backupIssues))
				for _, issue := range backupIssues {
					legacyChapters = append(legacyChapters, issue.Chapter)
				}
				budgetBase = current
				if err := mergeLegacyBudgetFields(&budgetBase, *backup, legacyChapters); err != nil {
					return report, fmt.Errorf("prepare legacy budget migration context: %w", err)
				}
				budgetIssues = domain.ValidateArcChapterBudgetDensity(budgetBase)
			}
		}
	}

	if len(budgetIssues) > 0 {
		h.emitEvent(Event{
			Time:     nowUTC(),
			Category: "SYSTEM",
			Summary:  fmt.Sprintf("旧改编大纲预算异常，开始预算专项模型重分析（影响 %d 章）", len(budgetIssues)),
			Level:    "warn",
			Detail:   formatBudgetPreflightIssueDetail(budgetIssues),
		})
		if _, err := h.store.Adaptation.Backup("model-budget-reanalysis-before"); err != nil {
			return report, fmt.Errorf("backup adaptation before model budget repair: %w", err)
		}
		result, modelErr := adapt.ReanalyzeLegacyArcChapterBudgets(ctx, h.adaptationDeps(), budgetBase)
		report.BudgetAttempts = result.Attempts
		report.BudgetChapters = append([]int(nil), result.Chapters...)
		if modelErr == nil {
			candidate := current
			if err := mergeLegacyBudgetFields(&candidate, result.Plan, result.Chapters); err != nil {
				return report, fmt.Errorf("merge model budget repair: %w", err)
			}
			domain.MarkAdaptationBudgetRepair(&candidate, "model", result.Attempts, result.Chapters, "legacy confirmed arc plan budget-only reanalysis passed density audit")
			markOutlineQualityIfComplete(&candidate, h)
			if err := h.store.Adaptation.SavePlan(candidate); err != nil {
				return report, fmt.Errorf("save model budget repair: %w", err)
			}
			current = candidate
			report.Changed = true
			report.BudgetMode = "model"
			h.emitEvent(Event{
				Time:     nowUTC(),
				Category: "SYSTEM",
				Summary:  fmt.Sprintf("预算专项模型重分析通过（重试 %d 次，影响第 %s 章），未修改剧情", result.Attempts, joinChapterNumbers(result.Chapters)),
				Level:    "success",
			})
		} else {
			// Last-resort deterministic expansion is explicit, durable, and
			// visible. It is never the normal route and will not be retried on
			// the next Resume because the plan records fallback mode.
			fallback := budgetBase
			repaired := domain.RepairArcChapterBudgetDensity(&fallback)
			if len(repaired) == 0 {
				return report, fmt.Errorf("model budget repair failed and deterministic fallback had no repair: %s", retrypolicy.SanitizeProviderError(modelErr))
			}
			safeModelErr := retrypolicy.SanitizeProviderError(modelErr)
			candidate := current
			if err := mergeLegacyBudgetFields(&candidate, fallback, repaired); err != nil {
				return report, fmt.Errorf("merge deterministic budget fallback: %w", err)
			}
			domain.MarkAdaptationBudgetRepair(&candidate, "deterministic_fallback", result.Attempts, repaired, "model budget-only reanalysis exhausted retries: "+safeModelErr)
			markOutlineQualityIfComplete(&candidate, h)
			if err := h.store.Adaptation.SavePlan(candidate); err != nil {
				return report, fmt.Errorf("save deterministic budget fallback: %w", err)
			}
			current = candidate
			report.Changed = true
			report.BudgetMode = "deterministic_fallback"
			report.BudgetAttempts = result.Attempts
			report.BudgetChapters = append([]int(nil), repaired...)
			h.emitEvent(Event{
				Time:     nowUTC(),
				Category: "SYSTEM",
				Summary:  fmt.Sprintf("预算专项模型重分析未通过，已执行最终确定性兜底（影响第 %s 章）", joinChapterNumbers(repaired)),
				Detail:   safeModelErr,
				Level:    "error",
			})
		}
	}

	// Re-load after the budget write so event repair cannot accidentally use a
	// stale plan or overwrite the budget marker.
	if refreshed, loadErr := h.store.Adaptation.LoadPlan(); loadErr != nil {
		return report, fmt.Errorf("reload adaptation plan after preflight: %w", loadErr)
	} else if refreshed != nil {
		current = *refreshed
	}

	target, targetErr := h.adaptationPreflightTargetChapter()
	if targetErr != nil {
		return report, targetErr
	}
	if target <= 0 {
		return report, nil
	}
	if outlineErr := adapt.ValidateAdaptationChapterOutlineQuality(&current, target); outlineErr != nil {
		h.emitEvent(Event{
			Time:     nowUTC(),
			Category: "SYSTEM",
			Summary:  fmt.Sprintf("创作前发现第 %d 章改编大纲契约异常，进入事件归属专项修复", target),
			Detail:   outlineErr.Error(),
			Level:    "warn",
		})
		if _, err := h.store.Adaptation.Backup("model-event-binding-repair-before"); err != nil {
			return report, fmt.Errorf("backup adaptation before event-binding repair: %w", err)
		}
		result, repairErr := adapt.ReconcileLegacyArcEventBindingsForChapter(ctx, h.adaptationDeps(), current, target)
		report.EventAttempts = result.Attempts
		report.EventChapters = append([]int(nil), result.Chapters...)
		if repairErr != nil {
			return report, fmt.Errorf("targeted adaptation outline repair failed: %w", repairErr)
		}
		if result.Attempts > 0 {
			candidate := result.Plan
			markOutlineQualityIfComplete(&candidate, h)
			if err := h.store.Adaptation.SavePlan(candidate); err != nil {
				return report, fmt.Errorf("save targeted adaptation outline repair: %w", err)
			}
			current = candidate
			report.Changed = true
			h.emitEvent(Event{
				Time:     nowUTC(),
				Category: "SYSTEM",
				Summary:  fmt.Sprintf("第 %d 章改编大纲事件归属专项修复通过（重试 %d 次）", target, result.Attempts),
				Level:    "success",
			})
		}
		if finalErr := adapt.ValidateAdaptationChapterOutlineQuality(&current, target); finalErr != nil {
			return report, fmt.Errorf("第 %d 章改编大纲仍未通过创作前门禁：%w", target, finalErr)
		}
	}
	return report, nil
}

func (h *Host) adaptationPreflightTargetChapter() (int, error) {
	progress, err := h.store.Progress.Load()
	if err != nil || progress == nil {
		return 0, err
	}
	if progress.Phase != domain.PhaseWriting {
		return 0, nil
	}
	if progress.InProgressChapter > 0 {
		return progress.InProgressChapter, nil
	}
	return progress.NextChapter(), nil
}

func mergeLegacyBudgetFields(current *domain.AdaptationPlan, repaired domain.AdaptationPlan, chapters []int) error {
	if current == nil {
		return fmt.Errorf("current plan is nil")
	}
	allowed := make(map[int]struct{}, len(chapters))
	for _, chapter := range chapters {
		allowed[chapter] = struct{}{}
	}
	for _, number := range chapters {
		var source, target *domain.AdaptationChapterPlan
		for index := range repaired.Chapters {
			if repaired.Chapters[index].Chapter == number {
				source = &repaired.Chapters[index]
				break
			}
		}
		for index := range current.Chapters {
			if current.Chapters[index].Chapter == number {
				target = &current.Chapters[index]
				break
			}
		}
		if source == nil || target == nil {
			return fmt.Errorf("budget repair chapter %d is absent from current or repaired plan", number)
		}
		target.TargetRunes = source.TargetRunes
		target.TargetMinRunes = source.TargetMinRunes
		target.TargetMaxRunes = source.TargetMaxRunes
		if source.WordBudget == nil {
			target.WordBudget = nil
		} else {
			copyBudget := *source.WordBudget
			target.WordBudget = &copyBudget
		}
	}
	for _, chapter := range current.Chapters {
		if _, ok := allowed[chapter.Chapter]; ok {
			continue
		}
		// No story fields are copied from the planner candidate. This loop is
		// intentionally empty; it documents the budget-only merge boundary.
	}
	current.TargetTotalRunes = 0
	current.TargetMinRunes = 0
	current.TargetMaxRunes = 0
	for _, chapter := range current.Chapters {
		current.TargetTotalRunes += chapter.TargetRunes
		current.TargetMinRunes += chapter.TargetMinRunes
		current.TargetMaxRunes += chapter.TargetMaxRunes
	}
	domain.ClearAdaptationOutlineQualityAudit(current)
	return nil
}

func markOutlineQualityIfComplete(plan *domain.AdaptationPlan, h *Host) {
	if plan == nil {
		return
	}
	var manifest *domain.AdaptationSourceManifest
	if h != nil && h.store != nil && h.store.Adaptation != nil {
		manifest, _ = h.store.Adaptation.LoadSourceManifest()
	}
	if err := adapt.ValidateAdaptationOutlineQuality(plan, manifest); err == nil {
		domain.MarkAdaptationOutlineQualityPassed(plan)
	}
}

func formatBudgetPreflightIssueDetail(issues []domain.AdaptationChapterBudgetDensityIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, fmt.Sprintf("第%d章：%s", issue.Chapter, issue.Detail))
	}
	return strings.Join(parts, "；")
}

func joinChapterNumbers(chapters []int) string {
	if len(chapters) == 0 {
		return "无"
	}
	out := make([]string, 0, len(chapters))
	for _, chapter := range chapters {
		out = append(out, fmt.Sprintf("%d", chapter))
	}
	return strings.Join(out, "、")
}

func nowUTC() time.Time { return time.Now().UTC() }
