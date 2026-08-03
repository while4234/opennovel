package store

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// ChapterWordBudgetPolicy resolves the current rolling recommendation and the
// enforceable user-declared chapter range from their authoritative stores.
func (s *Store) ChapterWordBudgetPolicy(progress *domain.Progress, chapter int) (domain.WordBudgetRuntime, domain.ChapterWordBudgetPolicy, bool, error) {
	if s == nil || s.RunMeta == nil {
		return domain.WordBudgetRuntime{}, domain.ChapterWordBudgetPolicy{}, false, nil
	}
	meta, err := s.RunMeta.Load()
	if err != nil {
		return domain.WordBudgetRuntime{}, domain.ChapterWordBudgetPolicy{}, false, fmt.Errorf("load run word budget: %w", err)
	}
	if meta == nil || meta.WordBudget == nil || meta.WordBudget.TargetTotalWords <= 0 {
		return domain.WordBudgetRuntime{}, domain.ChapterWordBudgetPolicy{}, false, nil
	}
	runtime, ok := meta.WordBudget.Runtime(progress, chapter)
	if !ok {
		return domain.WordBudgetRuntime{}, domain.ChapterWordBudgetPolicy{}, false, nil
	}

	declaredMin, declaredMax := 0, 0
	hasDeclaredRange := false
	if s.UserRules != nil {
		snapshot, loadErr := s.UserRules.Load()
		if loadErr != nil {
			return domain.WordBudgetRuntime{}, domain.ChapterWordBudgetPolicy{}, false, fmt.Errorf("load user chapter word range: %w", loadErr)
		}
		if snapshot != nil && snapshot.Structured.ChapterWords != nil {
			declaredMin = snapshot.Structured.ChapterWords.Min
			declaredMax = snapshot.Structured.ChapterWords.Max
			hasDeclaredRange = declaredMin > 0 || declaredMax > 0
		}
	}
	policy, ok := domain.ResolveChapterWordBudgetPolicy(runtime, declaredMin, declaredMax, hasDeclaredRange)
	return runtime, policy, ok, nil
}
