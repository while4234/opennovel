package store

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const characterCardLifecycleFile = "meta/character_cards/lifecycle.json"
const characterCardCandidateFile = "meta/character_cards/candidate.json"

type CharacterCardLifecycleConflictError struct {
	Expected int64
	Actual   int64
}

func (e *CharacterCardLifecycleConflictError) Error() string {
	return fmt.Sprintf("character card lifecycle revision conflict: expected %d, actual %d", e.Expected, e.Actual)
}

// CharacterCardStore persists lifecycle and source-mapping metadata only.
// Canonical character content remains owned by FoundationStore.
type CharacterCardStore struct {
	io *IO
	mu sync.Mutex
}

func (s *CharacterCardStore) LoadCandidate() (*domain.CharacterCardCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadCandidateUnlocked()
}

func (s *CharacterCardStore) SaveCandidateCAS(
	candidate domain.CharacterCardCandidate,
	expectedRevision int64,
) (domain.CharacterCardCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.loadCandidateUnlocked()
	if err != nil {
		return domain.CharacterCardCandidate{}, err
	}
	actual := int64(0)
	if existing != nil {
		actual = existing.Revision
	}
	if actual != expectedRevision {
		return domain.CharacterCardCandidate{}, &CharacterCardLifecycleConflictError{
			Expected: expectedRevision,
			Actual:   actual,
		}
	}
	normalized, err := normalizeCharacterCardCandidate(candidate)
	if err != nil {
		return domain.CharacterCardCandidate{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if existing == nil {
		normalized.CreatedAt = now
	} else {
		normalized.CreatedAt = existing.CreatedAt
	}
	normalized.UpdatedAt = now
	normalized.Revision = actual + 1
	if existing != nil && characterCardCandidateEqual(*existing, normalized) {
		return *existing, nil
	}
	if err := s.io.WriteJSON(characterCardCandidateFile, normalized); err != nil {
		return domain.CharacterCardCandidate{}, fmt.Errorf("save character card candidate: %w", err)
	}
	return normalized, nil
}

// DiscardCandidate removes only the staged Character Agent candidate and its
// lifecycle sidecar. Canonical StoryFoundation and immutable adaptation source
// data are never touched.
func (s *CharacterCardStore) DiscardCandidate(expectedDigest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.loadCandidateUnlocked()
	if err != nil {
		return err
	}
	if existing == nil {
		return os.ErrNotExist
	}
	digest, err := domain.CharacterCardContentDigest(existing.Foundation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(expectedDigest) != digest {
		return fmt.Errorf("character candidate digest is stale")
	}
	if err := s.io.RemoveFile(characterCardCandidateFile); err != nil {
		return fmt.Errorf("discard character card candidate: %w", err)
	}
	if err := s.io.RemoveFile(characterCardLifecycleFile); err != nil {
		return fmt.Errorf("discard character card lifecycle: %w", err)
	}
	return nil
}

func newCharacterCardStore(io *IO) *CharacterCardStore {
	return &CharacterCardStore{io: io}
}

func (s *CharacterCardStore) Load(current domain.CharacterCardBinding) (*domain.CharacterCardLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.loadUnlocked()
	if err != nil || value == nil {
		return value, err
	}
	reconciled, err := domain.ReconcileCharacterCardLifecycle(*value, current)
	if err != nil {
		return nil, fmt.Errorf("reconcile character card lifecycle: %w", err)
	}
	return &reconciled, nil
}

func (s *CharacterCardStore) SaveCAS(
	candidate domain.CharacterCardLifecycle,
	expectedRevision int64,
	current domain.CharacterCardBinding,
) (domain.CharacterCardLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.loadUnlocked()
	if err != nil {
		return domain.CharacterCardLifecycle{}, err
	}
	actual := int64(0)
	if existing != nil {
		actual = existing.Revision
	}
	if actual != expectedRevision {
		return domain.CharacterCardLifecycle{}, &CharacterCardLifecycleConflictError{
			Expected: expectedRevision,
			Actual:   actual,
		}
	}
	candidate.Revision = actual
	normalized, err := domain.NormalizeCharacterCardLifecycle(candidate)
	if err != nil {
		return domain.CharacterCardLifecycle{}, fmt.Errorf("normalize character card lifecycle: %w", err)
	}
	normalized, err = domain.ReconcileCharacterCardLifecycle(normalized, current)
	if err != nil {
		return domain.CharacterCardLifecycle{}, fmt.Errorf("bind character card lifecycle: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if existing == nil {
		normalized.CreatedAt = now
	} else {
		normalized.CreatedAt = existing.CreatedAt
	}
	normalized.UpdatedAt = now
	if existing != nil && characterCardLifecycleEqual(*existing, normalized) {
		return *existing, nil
	}
	normalized.Revision = actual + 1
	if err := s.io.WriteJSON(characterCardLifecycleFile, normalized); err != nil {
		return domain.CharacterCardLifecycle{}, fmt.Errorf("save character card lifecycle: %w", err)
	}
	return normalized, nil
}

func (s *CharacterCardStore) loadUnlocked() (*domain.CharacterCardLifecycle, error) {
	var value domain.CharacterCardLifecycle
	if err := s.io.ReadJSON(characterCardLifecycleFile, &value); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load character card lifecycle: %w", err)
	}
	normalized, err := domain.NormalizeCharacterCardLifecycle(value)
	if err != nil {
		return nil, fmt.Errorf("normalize persisted character card lifecycle: %w", err)
	}
	normalized.Revision = value.Revision
	normalized.CreatedAt = value.CreatedAt
	normalized.UpdatedAt = value.UpdatedAt
	return &normalized, nil
}

func (s *CharacterCardStore) loadCandidateUnlocked() (*domain.CharacterCardCandidate, error) {
	var value domain.CharacterCardCandidate
	if err := s.io.ReadJSON(characterCardCandidateFile, &value); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load character card candidate: %w", err)
	}
	normalized, err := normalizeCharacterCardCandidate(value)
	if err != nil {
		return nil, fmt.Errorf("normalize persisted character card candidate: %w", err)
	}
	normalized.Revision = value.Revision
	normalized.CreatedAt = value.CreatedAt
	normalized.UpdatedAt = value.UpdatedAt
	return &normalized, nil
}

func normalizeCharacterCardCandidate(value domain.CharacterCardCandidate) (domain.CharacterCardCandidate, error) {
	if value.Version == 0 {
		value.Version = domain.CharacterCardCandidateVersion
	}
	if value.Version != domain.CharacterCardCandidateVersion || value.Revision < 0 {
		return domain.CharacterCardCandidate{}, fmt.Errorf("character card candidate contract is invalid")
	}
	foundation, err := domain.NormalizeStoryFoundation(value.Foundation)
	if err != nil {
		return domain.CharacterCardCandidate{}, fmt.Errorf("normalize character candidate foundation: %w", err)
	}
	value.Foundation = foundation
	if len(value.ProjectedCast.Members) > 0 {
		projected, normalizeErr := domain.NormalizeCoreCastContract(value.ProjectedCast)
		if normalizeErr != nil {
			return domain.CharacterCardCandidate{}, fmt.Errorf("normalize projected core cast: %w", normalizeErr)
		}
		value.ProjectedCast = projected
	}
	if value.Base.Candidate.FoundationRevision < 0 ||
		len(value.Base.Candidate.FoundationAuditSignature) != 64 ||
		len(value.Base.Candidate.CharacterContentDigest) != 64 ||
		len(value.Base.InputDigest) != 64 {
		return domain.CharacterCardCandidate{}, fmt.Errorf("character card candidate base binding is incomplete")
	}
	return value, nil
}

func characterCardLifecycleEqual(left, right domain.CharacterCardLifecycle) bool {
	left.Revision, right.Revision = 0, 0
	left.UpdatedAt, right.UpdatedAt = "", ""
	return reflect.DeepEqual(left, right)
}

func characterCardCandidateEqual(left, right domain.CharacterCardCandidate) bool {
	left.Revision, right.Revision = 0, 0
	left.UpdatedAt, right.UpdatedAt = "", ""
	return reflect.DeepEqual(left, right)
}
