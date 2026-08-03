package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	characterWorkspaceFile    = "meta/character_workspace/runs.json"
	characterWorkspaceVersion = 1
)

var characterWorkspaceLocks sync.Map

type characterWorkspaceState struct {
	Version int                            `json:"version"`
	Runs    []domain.CharacterWorkspaceRun `json:"runs"`
}

type CharacterWorkspaceStore struct {
	io *IO
	mu *sync.Mutex
}

type CharacterWorkspaceConflictError struct {
	Message string
}

func (e *CharacterWorkspaceConflictError) Error() string {
	return strings.TrimSpace(e.Message)
}

func NewCharacterWorkspaceStore(io *IO) *CharacterWorkspaceStore {
	key, err := filepath.Abs(io.dir)
	if err != nil {
		key = io.dir
	}
	value, _ := characterWorkspaceLocks.LoadOrStore(strings.ToLower(filepath.Clean(key)), &sync.Mutex{})
	return &CharacterWorkspaceStore{io: io, mu: value.(*sync.Mutex)}
}

func (s *CharacterWorkspaceStore) Create(run domain.CharacterWorkspaceRun) (domain.CharacterWorkspaceRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	for _, existing := range state.Runs {
		if existing.IdempotencyKey != strings.TrimSpace(run.IdempotencyKey) || existing.Mode != run.Mode {
			continue
		}
		if existing.RequestFingerprint != strings.TrimSpace(run.RequestFingerprint) {
			return domain.CharacterWorkspaceRun{}, false, &CharacterWorkspaceConflictError{
				Message: "character workspace idempotency key was already used with different input",
			}
		}
		return existing, false, nil
	}
	for _, existing := range state.Runs {
		if existing.RunID == strings.TrimSpace(run.RunID) {
			return domain.CharacterWorkspaceRun{}, false, &CharacterWorkspaceConflictError{
				Message: "character workspace run ID already exists with a different request identity",
			}
		}
		if existing.Active() {
			return domain.CharacterWorkspaceRun{}, false, &CharacterWorkspaceConflictError{
				Message: "another character workspace run is active",
			}
		}
	}
	normalized, err := domain.NormalizeCharacterWorkspaceRun(run)
	if err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	normalized.Revision = 1
	state.Runs = append(state.Runs, normalized)
	if err := s.saveUnlocked(state); err != nil {
		return domain.CharacterWorkspaceRun{}, false, err
	}
	return normalized, true, nil
}

func (s *CharacterWorkspaceStore) Load(runID string) (*domain.CharacterWorkspaceRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}
	for _, run := range state.Runs {
		if run.RunID == strings.TrimSpace(runID) {
			copy := run
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *CharacterWorkspaceStore) FindByIdempotency(
	mode domain.CharacterWorkspaceRunMode,
	key string,
) (*domain.CharacterWorkspaceRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}
	for _, run := range state.Runs {
		if run.Mode == mode && run.IdempotencyKey == strings.TrimSpace(key) {
			copy := run
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *CharacterWorkspaceStore) Latest() (*domain.CharacterWorkspaceRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}
	if len(state.Runs) == 0 {
		return nil, nil
	}
	copy := state.Runs[len(state.Runs)-1]
	return &copy, nil
}

func (s *CharacterWorkspaceStore) Update(
	runID string,
	expectedRevision int64,
	mutate func(*domain.CharacterWorkspaceRun) error,
) (domain.CharacterWorkspaceRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return domain.CharacterWorkspaceRun{}, err
	}
	for i := range state.Runs {
		if state.Runs[i].RunID != strings.TrimSpace(runID) {
			continue
		}
		if state.Runs[i].Revision != expectedRevision {
			return domain.CharacterWorkspaceRun{}, &CharacterWorkspaceConflictError{
				Message: fmt.Sprintf(
					"character workspace run revision conflict: expected %d, actual %d",
					expectedRevision,
					state.Runs[i].Revision,
				),
			}
		}
		next := state.Runs[i]
		if mutate != nil {
			if err := mutate(&next); err != nil {
				return domain.CharacterWorkspaceRun{}, err
			}
		}
		next.Revision++
		normalized, err := domain.NormalizeCharacterWorkspaceRun(next)
		if err != nil {
			return domain.CharacterWorkspaceRun{}, err
		}
		state.Runs[i] = normalized
		if err := s.saveUnlocked(state); err != nil {
			return domain.CharacterWorkspaceRun{}, err
		}
		return normalized, nil
	}
	return domain.CharacterWorkspaceRun{}, os.ErrNotExist
}

func (s *CharacterWorkspaceStore) RecoverInterrupted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	changed := false
	now := domain.RevisionTimestamp()
	for i := range state.Runs {
		if !state.Runs[i].Active() {
			continue
		}
		state.Runs[i].Revision++
		state.Runs[i].Status = domain.CharacterWorkspaceInterrupted
		state.Runs[i].Stage = "interrupted"
		state.Runs[i].Error = &domain.CharacterCardError{
			Class:   "service_interrupted",
			Message: "service restarted before the Character Agent run completed",
		}
		state.Runs[i].UpdatedAt = now
		state.Runs[i].FinishedAt = now
		changed = true
	}
	if !changed {
		return nil
	}
	return s.saveUnlocked(state)
}

func (s *CharacterWorkspaceStore) loadUnlocked() (characterWorkspaceState, error) {
	var state characterWorkspaceState
	if err := s.io.ReadJSON(characterWorkspaceFile, &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return characterWorkspaceState{Version: characterWorkspaceVersion, Runs: []domain.CharacterWorkspaceRun{}}, nil
		}
		return characterWorkspaceState{}, fmt.Errorf("load character workspace runs: %w", err)
	}
	if state.Version != characterWorkspaceVersion {
		return characterWorkspaceState{}, fmt.Errorf("character workspace state version %d is unsupported", state.Version)
	}
	for i := range state.Runs {
		normalized, err := domain.NormalizeCharacterWorkspaceRun(state.Runs[i])
		if err != nil {
			return characterWorkspaceState{}, fmt.Errorf("normalize character workspace run %q: %w", state.Runs[i].RunID, err)
		}
		normalized.Revision = state.Runs[i].Revision
		state.Runs[i] = normalized
	}
	return state, nil
}

func (s *CharacterWorkspaceStore) saveUnlocked(state characterWorkspaceState) error {
	state.Version = characterWorkspaceVersion
	if state.Runs == nil {
		state.Runs = []domain.CharacterWorkspaceRun{}
	}
	return s.io.WriteJSON(characterWorkspaceFile, state)
}
