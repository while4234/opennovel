package store

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	continuationWorkflowFile = "meta/continuation/workflow.json"
	continuationProposalFile = "meta/continuation/proposal.json"
	continuationVolumesFile  = "meta/continuation/volumes.json"
	continuationOutlinesFile = "meta/continuation/outlines.json"
	continuationPlanFile     = "meta/continuation/plan.json"
)

// ErrContinuationNotInitialized means the imported-source baseline has not
// yet been registered as a continuation workflow.
var ErrContinuationNotInitialized = errors.New("continuation workflow is not initialized")

// ContinuationRevisionConflictError is returned for stale UI/API mutations.
type ContinuationRevisionConflictError struct {
	Expected int
	Actual   int
}

func (e *ContinuationRevisionConflictError) Error() string {
	return fmt.Sprintf("continuation revision conflict: expected %d, actual %d", e.Expected, e.Actual)
}

func IsContinuationRevisionConflict(err error) bool {
	var conflict *ContinuationRevisionConflictError
	return errors.As(err, &conflict)
}

// ContinuationStore owns the isolated candidate-planning files. Update keeps
// read/check/write under one lock, preventing stale concurrent review actions.
type ContinuationStore struct {
	io        *IO
	migration *structureMigration
}

func NewContinuationStore(io *IO, migrations ...*structureMigration) *ContinuationStore {
	var migration *structureMigration
	if len(migrations) > 0 {
		migration = migrations[0]
	}
	return &ContinuationStore{io: io, migration: migration}
}

func (s *ContinuationStore) withRecovery(fn func() error) error {
	if s.migration == nil {
		return fn()
	}
	return s.migration.withRead(fn)
}

func (s *ContinuationStore) withReadLock(fn func() error) error {
	return s.withRecovery(func() error {
		s.io.mu.RLock()
		defer s.io.mu.RUnlock()
		return fn()
	})
}

func (s *ContinuationStore) withWriteLock(fn func() error) error {
	return s.withRecovery(func() error {
		s.io.mu.Lock()
		defer s.io.mu.Unlock()
		return fn()
	})
}

func (s *ContinuationStore) InitializeSource(sourceSignature string, baseChapterCount int) (*domain.ContinuationSnapshot, error) {
	workflow, err := domain.NewContinuationWorkflow(sourceSignature, baseChapterCount)
	if err != nil {
		return nil, err
	}
	var result *domain.ContinuationSnapshot
	err = s.withWriteLock(func() error {
		current, loadErr := s.loadSnapshotUnlocked()
		if loadErr != nil && !errors.Is(loadErr, ErrContinuationNotInitialized) {
			return loadErr
		}
		if loadErr == nil &&
			current.Workflow.SourceSignature == workflow.SourceSignature &&
			current.Workflow.BaseChapterCount == workflow.BaseChapterCount {
			result = current
			return nil
		}

		result = &domain.ContinuationSnapshot{Workflow: workflow}
		return s.saveSnapshotUnlocked(result)
	})
	return result, err
}

func (s *ContinuationStore) LoadWorkflow() (*domain.ContinuationWorkflow, error) {
	snapshot, err := s.LoadSnapshot()
	if err != nil {
		return nil, err
	}
	workflow := snapshot.Workflow
	return &workflow, nil
}

func (s *ContinuationStore) LoadSnapshot() (*domain.ContinuationSnapshot, error) {
	var snapshot *domain.ContinuationSnapshot
	err := s.withReadLock(func() error {
		var err error
		snapshot, err = s.loadSnapshotUnlocked()
		return err
	})
	return snapshot, err
}

// Update atomically serializes workflow mutations in-process and increments
// Revision exactly once. Candidate artifacts may be added or set to nil by the
// callback; no canonical outline is touched here.
func (s *ContinuationStore) Update(expectedRevision int, mutate func(*domain.ContinuationSnapshot) error) (*domain.ContinuationSnapshot, error) {
	if mutate == nil {
		return nil, fmt.Errorf("continuation mutation is required")
	}
	var snapshot *domain.ContinuationSnapshot
	err := s.withWriteLock(func() error {
		var err error
		snapshot, err = s.loadSnapshotUnlocked()
		if err != nil {
			return err
		}
		if snapshot.Workflow.Revision != expectedRevision {
			return &ContinuationRevisionConflictError{Expected: expectedRevision, Actual: snapshot.Workflow.Revision}
		}
		before := snapshot.Workflow
		if err := mutate(snapshot); err != nil {
			return err
		}
		if snapshot.Workflow.SourceSignature != before.SourceSignature ||
			snapshot.Workflow.BaseChapterCount != before.BaseChapterCount {
			return fmt.Errorf("continuation source baseline is immutable")
		}
		if snapshot.Workflow.Stage != before.Stage {
			if err := domain.ValidateContinuationTransition(before.Stage, snapshot.Workflow.Stage); err != nil {
				return err
			}
		}
		if err := validateContinuationSnapshot(snapshot); err != nil {
			return err
		}
		snapshot.Workflow.Version = domain.ContinuationSchemaVersion
		snapshot.Workflow.Revision = before.Revision + 1
		snapshot.Workflow.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return s.saveSnapshotUnlocked(snapshot)
	})
	return snapshot, err
}

func (s *ContinuationStore) Clear() error {
	return s.withWriteLock(func() error {
		for _, rel := range continuationArtifactFiles() {
			if err := s.io.RemoveFileUnlocked(rel); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *ContinuationStore) loadSnapshotUnlocked() (*domain.ContinuationSnapshot, error) {
	var workflow domain.ContinuationWorkflow
	if err := s.io.ReadJSONUnlocked(continuationWorkflowFile, &workflow); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrContinuationNotInitialized
		}
		return nil, err
	}
	snapshot := &domain.ContinuationSnapshot{Workflow: workflow}
	if err := readOptionalJSONUnlocked(s.io, continuationProposalFile, &snapshot.Proposal); err != nil {
		return nil, err
	}
	if err := s.io.ReadJSONUnlocked(continuationVolumesFile, &snapshot.Volumes); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := readOptionalJSONUnlocked(s.io, continuationOutlinesFile, &snapshot.Outlines); err != nil {
		return nil, err
	}
	if err := readOptionalJSONUnlocked(s.io, continuationPlanFile, &snapshot.Plan); err != nil {
		return nil, err
	}
	if err := validateContinuationSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("load continuation snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *ContinuationStore) saveSnapshotUnlocked(snapshot *domain.ContinuationSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("continuation snapshot is required")
	}
	if err := s.io.WriteJSONUnlocked(continuationWorkflowFile, snapshot.Workflow); err != nil {
		return err
	}
	if err := writeOptionalJSONUnlocked(s.io, continuationProposalFile, snapshot.Proposal); err != nil {
		return err
	}
	if len(snapshot.Volumes) == 0 {
		if err := s.io.RemoveFileUnlocked(continuationVolumesFile); err != nil {
			return err
		}
	} else if err := s.io.WriteJSONUnlocked(continuationVolumesFile, snapshot.Volumes); err != nil {
		return err
	}
	if err := writeOptionalJSONUnlocked(s.io, continuationOutlinesFile, snapshot.Outlines); err != nil {
		return err
	}
	return writeOptionalJSONUnlocked(s.io, continuationPlanFile, snapshot.Plan)
}

func readOptionalJSONUnlocked[T any](io *IO, rel string, target **T) error {
	var value T
	if err := io.ReadJSONUnlocked(rel, &value); err != nil {
		if os.IsNotExist(err) {
			*target = nil
			return nil
		}
		return err
	}
	*target = &value
	return nil
}

func writeOptionalJSONUnlocked[T any](io *IO, rel string, value *T) error {
	if value == nil {
		return io.RemoveFileUnlocked(rel)
	}
	return io.WriteJSONUnlocked(rel, value)
}

func continuationArtifactFiles() []string {
	return []string{
		continuationWorkflowFile,
		continuationProposalFile,
		continuationVolumesFile,
		continuationOutlinesFile,
		continuationPlanFile,
		continuationCommitJournalFile,
	}
}

func validateContinuationSnapshot(snapshot *domain.ContinuationSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("continuation snapshot is required")
	}
	workflow := snapshot.Workflow
	if workflow.Version != 0 && workflow.Version != domain.ContinuationSchemaVersion {
		return fmt.Errorf("unsupported continuation schema version %d", workflow.Version)
	}
	if !workflow.Stage.Valid() {
		return fmt.Errorf("invalid continuation stage %q", workflow.Stage)
	}
	if strings.TrimSpace(workflow.SourceSignature) == "" {
		return fmt.Errorf("continuation source signature is required")
	}
	if workflow.BaseChapterCount <= 0 {
		return fmt.Errorf("continuation base chapter count must be > 0")
	}
	if workflow.Revision <= 0 {
		return fmt.Errorf("continuation revision must be > 0")
	}
	if snapshot.Proposal != nil {
		if err := snapshot.Proposal.Validate(); err != nil {
			return err
		}
	}
	if len(snapshot.Volumes) > 0 {
		if snapshot.Proposal == nil {
			return fmt.Errorf("continuation volumes require a proposal")
		}
		if err := domain.ValidateContinuationVolumes(*snapshot.Proposal, snapshot.Volumes); err != nil {
			return err
		}
	}
	if snapshot.Outlines != nil {
		if snapshot.Proposal == nil {
			return fmt.Errorf("continuation outlines require a proposal")
		}
		chapters, err := domain.FlattenContinuationOutline(workflow.BaseChapterCount, *snapshot.Outlines)
		if err != nil {
			return err
		}
		if len(chapters) != snapshot.Proposal.TargetChapterCount {
			return fmt.Errorf("continuation outline has %d chapters, want %d", len(chapters), snapshot.Proposal.TargetChapterCount)
		}
	}
	if snapshot.Plan != nil {
		if snapshot.Outlines == nil || snapshot.Proposal == nil {
			return fmt.Errorf("continuation plan requires proposal and outlines")
		}
		if snapshot.Plan.SourceSignature != workflow.SourceSignature ||
			snapshot.Plan.BaseChapterCount != workflow.BaseChapterCount {
			return fmt.Errorf("continuation plan baseline does not match workflow")
		}
	}
	return nil
}
