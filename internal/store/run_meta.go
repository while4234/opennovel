package store

import (
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// RunMetaStore 管理运行元信息（模型、干预历史、规划级别等）。
type RunMetaStore struct{ io *IO }

func NewRunMetaStore(io *IO) *RunMetaStore { return &RunMetaStore{io: io} }

// Save 保存运行元信息到 meta/run.json。
func (s *RunMetaStore) Save(meta domain.RunMeta) error {
	return s.io.WithWriteLock(func() error {
		current, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		var currentReview *domain.PlanningReview
		if current != nil {
			currentReview = current.PlanningReview
		}
		if err := validateOrdinaryPlanningReviewTransition(currentReview, meta.PlanningReview); err != nil {
			return fmt.Errorf("save run metadata: %w", err)
		}
		return s.saveUnlocked(meta)
	})
}

// Load 读取运行元信息。
func (s *RunMetaStore) Load() (*domain.RunMeta, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	return s.loadUnlocked()
}

func (s *RunMetaStore) loadUnlocked() (*domain.RunMeta, error) {
	var meta domain.RunMeta
	if err := s.io.ReadJSONUnlocked("meta/run.json", &meta); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &meta, nil
}

func (s *RunMetaStore) saveUnlocked(meta domain.RunMeta) error {
	return s.io.WriteJSONUnlocked("meta/run.json", meta)
}

// Init 初始化或更新运行元信息，保留已有的 SteerHistory。
func (s *RunMetaStore) Init(style, provider, model string) error {
	return s.io.WithWriteLock(func() error {
		existing, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		meta := domain.RunMeta{
			StartedAt: time.Now().Format(time.RFC3339),
			Provider:  provider,
			Style:     style,
			Model:     model,
		}
		if existing != nil {
			meta.SteerHistory = existing.SteerHistory
			meta.PendingSteer = existing.PendingSteer
			meta.PlanningTier = existing.PlanningTier
			meta.WordBudget = existing.WordBudget
			meta.PlanningReview = existing.PlanningReview
		}
		return s.saveUnlocked(meta)
	})
}

// AppendSteerEntry 追加用户干预记录。
func (s *RunMetaStore) AppendSteerEntry(entry domain.SteerEntry) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.SteerHistory = append(meta.SteerHistory, entry)
		return s.saveUnlocked(*meta)
	})
}

// SetPendingSteer 记录未完成的 Steer 指令。
func (s *RunMetaStore) SetPendingSteer(input string) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.PendingSteer = input
		return s.saveUnlocked(*meta)
	})
}

// ClearPendingSteer 清除已处理的 Steer 指令。
func (s *RunMetaStore) ClearPendingSteer() error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil || meta.PendingSteer == "" {
			return nil
		}
		meta.PendingSteer = ""
		return s.saveUnlocked(*meta)
	})
}

// SetPlanningTier 记录当前作品的规划级别。
func (s *RunMetaStore) SetPlanningTier(tier domain.PlanningTier) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.PlanningTier = tier
		return s.saveUnlocked(*meta)
	})
}

// SetWordBudget records or clears the book-level word budget contract.
func (s *RunMetaStore) SetWordBudget(budget *domain.WordBudget) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		if budget == nil {
			meta.WordBudget = nil
			return s.saveUnlocked(*meta)
		}
		normalized, ok := budget.Normalized()
		if !ok {
			meta.WordBudget = nil
			return s.saveUnlocked(*meta)
		}
		meta.WordBudget = &normalized
		return s.saveUnlocked(*meta)
	})
}

// SetPlanningReview records or clears the normal co-create blueprint review
// gate. The review is intentionally kept in run meta because it controls the
// current run, not the story canon itself.
func (s *RunMetaStore) SetPlanningReview(review *domain.PlanningReview) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		if err := validateOrdinaryPlanningReviewTransition(meta.PlanningReview, review); err != nil {
			return err
		}
		return s.setPlanningReviewUnlocked(meta, review)
	})
}

func (s *RunMetaStore) setPlanningReviewAuthoritative(review *domain.PlanningReview) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		return s.setPlanningReviewUnlocked(meta, review)
	})
}

func (s *RunMetaStore) setPlanningReviewUnlocked(meta *domain.RunMeta, review *domain.PlanningReview) error {
	if review == nil {
		meta.PlanningReview = nil
		return s.saveUnlocked(*meta)
	}
	cp := *review
	cp.FoundationSections = append([]string(nil), review.FoundationSections...)
	meta.PlanningReview = &cp
	return s.saveUnlocked(*meta)
}

func validateOrdinaryPlanningReviewTransition(current, next *domain.PlanningReview) error {
	if !planningReviewHasFoundationAuthority(current) && !planningReviewHasFoundationAuthority(next) {
		return nil
	}
	if current == nil || next == nil || current.FoundationStatus != domain.FoundationReviewStatusApproved ||
		next.FoundationStatus != domain.FoundationReviewStatusApproved || next.Kind == domain.PlanningReviewKindFoundation ||
		!sameFoundationAuthorityBinding(current, next) {
		return fmt.Errorf("foundation authority is Store-owned and cannot be created, cleared, or replaced by ordinary RunMeta persistence")
	}
	return nil
}

func planningReviewHasFoundationAuthority(review *domain.PlanningReview) bool {
	return review != nil && (review.FoundationStatus != "" || review.FoundationRevision != 0 ||
		review.FoundationAuditSignature != "" || review.CoreCastSignature != "" ||
		review.FoundationGeneration != 0 || review.FoundationBaseRevision != 0 ||
		len(review.FoundationSections) != 0 || review.FoundationFeedback != "" || review.FoundationConfirmedAt != "")
}

func sameFoundationAuthorityBinding(left, right *domain.PlanningReview) bool {
	if left == nil || right == nil {
		return false
	}
	return left.FoundationStatus == right.FoundationStatus &&
		left.FoundationRevision == right.FoundationRevision &&
		left.FoundationAuditSignature == right.FoundationAuditSignature &&
		left.CoreCastSignature == right.CoreCastSignature &&
		left.FoundationGeneration == right.FoundationGeneration &&
		left.FoundationBaseRevision == right.FoundationBaseRevision &&
		left.FoundationFeedback == right.FoundationFeedback &&
		left.FoundationConfirmedAt == right.FoundationConfirmedAt &&
		slices.Equal(left.FoundationSections, right.FoundationSections)
}

func (s *RunMetaStore) PlanningReview() (*domain.PlanningReview, error) {
	meta, err := s.Load()
	if err != nil || meta == nil || meta.PlanningReview == nil {
		return nil, err
	}
	cp := *meta.PlanningReview
	cp.FoundationSections = append([]string(nil), meta.PlanningReview.FoundationSections...)
	return &cp, nil
}

func (s *RunMetaStore) PlanningReviewPending() bool {
	review, err := s.PlanningReview()
	return err == nil && review != nil && review.Status == domain.PlanningReviewStatusPending
}

func (s *RunMetaStore) ClearPlanningReview() error {
	return s.SetPlanningReview(nil)
}
