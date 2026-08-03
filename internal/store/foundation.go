package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	foundationCanonicalFile = "story_foundation.json"
	foundationRootDir       = "meta/foundation"
	foundationJournalFile   = foundationRootDir + "/journal.json"
	foundationManifestFile  = foundationRootDir + "/projections.json"
	foundationStageDir      = foundationRootDir + "/stage"

	foundationJournalPrepared             = "prepared"
	foundationJournalCanonicalCommitted   = "canonical_committed"
	foundationJournalProjectionsCommitted = "projections_committed"
	foundationJournalVersion              = 2

	foundationFailAfterStage      = "after_stage"
	foundationFailAfterJournal    = "after_journal"
	foundationFailAfterCanonical  = "after_canonical"
	foundationFailAfterProjection = "after_projection"
	foundationFailAfterManifest   = "after_manifest"
)

var foundationProjectLifecycles sync.Map

type FoundationConflictError struct {
	Expected int64
	Actual   int64
}

func (e *FoundationConflictError) Error() string {
	return fmt.Sprintf("story foundation revision conflict: expected %d, actual %d", e.Expected, e.Actual)
}

// FoundationLifecycleConflictError asks a caller to retry a mutation that
// overlapped a destructive project rollback.
type FoundationLifecycleConflictError struct {
	StartedEpoch          uint64
	CurrentEpoch          uint64
	StartedDuringRollback bool
}

func (e *FoundationLifecycleConflictError) Error() string {
	return fmt.Sprintf(
		"story foundation lifecycle conflict: rollback overlapped mutation (started_epoch=%d current_epoch=%d started_during_rollback=%t); retry",
		e.StartedEpoch,
		e.CurrentEpoch,
		e.StartedDuringRollback,
	)
}

type foundationMutationEpoch struct {
	value uint64
}

type foundationProjectLifecycle struct {
	projectMu sync.RWMutex
	// reviewMu serializes a Foundation generation and its durable checkpoint
	// across every Store instance opened for the same project.
	reviewMu sync.Mutex
	// Even epochs accept mutations. A rollback advances to an odd epoch before
	// taking projectMu and advances again only after releasing it.
	epoch atomic.Uint64
}

func (l *foundationProjectLifecycle) captureMutationEpoch() foundationMutationEpoch {
	return foundationMutationEpoch{value: l.epoch.Load()}
}

func (l *foundationProjectLifecycle) validateMutationEpoch(epoch foundationMutationEpoch) error {
	current := l.epoch.Load()
	startedDuringRollback := epoch.value%2 != 0
	if !startedDuringRollback && epoch.value == current {
		return nil
	}
	return &FoundationLifecycleConflictError{
		StartedEpoch:          epoch.value,
		CurrentEpoch:          current,
		StartedDuringRollback: startedDuringRollback,
	}
}

func (l *foundationProjectLifecycle) beginRollback() error {
	for {
		current := l.epoch.Load()
		if current%2 != 0 {
			return fmt.Errorf("story foundation rollback lifecycle is already active")
		}
		if l.epoch.CompareAndSwap(current, current+1) {
			return nil
		}
	}
}

func (l *foundationProjectLifecycle) endRollback() {
	l.epoch.Add(1)
}

type foundationProjectionManifest struct {
	Version          int               `json:"version"`
	Revision         int64             `json:"revision"`
	ContentSignature string            `json:"content_signature"`
	AuditSignature   string            `json:"audit_signature"`
	Files            map[string]string `json:"files"`
}

type foundationJournal struct {
	Version                     int                    `json:"version"`
	Stage                       string                 `json:"stage"`
	TransactionID               string                 `json:"transaction_id"`
	BaseRevision                int64                  `json:"base_revision"`
	BaseCanonicalSignature      string                 `json:"base_canonical_signature,omitempty"`
	CandidateCanonicalSignature string                 `json:"candidate_canonical_signature"`
	ContentSignature            string                 `json:"content_signature"`
	AuditSignature              string                 `json:"audit_signature"`
	Foundation                  domain.StoryFoundation `json:"foundation"`
}

// FoundationStore owns one cross-section lock and one recoverable transaction
// for the canonical foundation and every compatibility projection.
type FoundationStore struct {
	io                   *IO
	lifecycle            *foundationProjectLifecycle
	projectMu            *sync.RWMutex
	withSemanticMutation func(string, func() error) error
	failpoint            func(string) error
	lifecycleHook        func(string)
	coreCast             *CoreCastStore
}

func newFoundationStore(io *IO) *FoundationStore {
	key, err := filepath.Abs(io.dir)
	if err != nil {
		key = filepath.Clean(io.dir)
	}
	key = strings.ToLower(filepath.Clean(key))
	lifecycleValue, _ := foundationProjectLifecycles.LoadOrStore(key, &foundationProjectLifecycle{})
	lifecycle := lifecycleValue.(*foundationProjectLifecycle)
	return &FoundationStore{io: io, lifecycle: lifecycle, projectMu: &lifecycle.projectMu}
}

func (s *FoundationStore) Load() (domain.StoryFoundation, error) {
	s.projectMu.Lock()
	defer s.projectMu.Unlock()
	if err := s.recoverUnlocked(); err != nil {
		return domain.StoryFoundation{}, fmt.Errorf("recover story foundation before load: %w", err)
	}
	return s.loadCurrentUnlocked(true)
}

func (s *FoundationStore) Recover() error {
	s.projectMu.Lock()
	defer s.projectMu.Unlock()
	return s.recoverUnlocked()
}

func (s *FoundationStore) PendingTransaction() (bool, error) {
	s.projectMu.RLock()
	defer s.projectMu.RUnlock()
	_, err := os.Stat(s.io.path(foundationJournalFile))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// ValidateStoryFoundationProjectionSet performs a read-only canonical and
// projection consistency check for clone/export boundaries. A legacy project
// is valid only when no canonical-only foundation artifact is present.
func ValidateStoryFoundationProjectionSet(dir string) error {
	store := newFoundationStore(newIO(dir))
	store.projectMu.RLock()
	defer store.projectMu.RUnlock()
	return store.validateProjectionSetUnlocked()
}

// WithCloneReadyStoryFoundationSnapshot holds the same project lock used by
// foundation saves while validating and copying a regular project clone.
func WithCloneReadyStoryFoundationSnapshot(dir string, copySnapshot func() error) error {
	if copySnapshot == nil {
		return fmt.Errorf("story foundation snapshot copy is required")
	}
	store := newFoundationStore(newIO(dir))
	store.projectMu.RLock()
	defer store.projectMu.RUnlock()
	if _, err := os.Lstat(store.io.path(foundationJournalFile)); err == nil {
		return fmt.Errorf("source project has a pending story foundation transaction; reopen it to recover before cloning")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect source story foundation transaction: %w", err)
	}
	if runtime, err := NewFoundationRevisionStore(newIO(dir)).LoadRuntime(); err != nil {
		return fmt.Errorf("inspect source Foundation revision before clone: %w", err)
	} else if runtime != nil && runtime.Active() {
		return fmt.Errorf("source project has an active Foundation revision %s; complete or cancel it before cloning", runtime.RevisionID)
	}
	if err := store.validateProjectionSetUnlocked(); err != nil {
		return fmt.Errorf("source story foundation is not clone-ready: %w", err)
	}
	return copySnapshot()
}

func (s *FoundationStore) validateProjectionSetUnlocked() error {
	value, err := s.loadCanonicalUnlocked()
	if os.IsNotExist(err) {
		return s.validateLegacyProjectionSetUnlocked()
	}
	if err != nil {
		return fmt.Errorf("load canonical story foundation: %w", err)
	}
	return s.verifyCommittedUnlocked(value)
}

func (s *FoundationStore) validateLegacyProjectionSetUnlocked() error {
	for _, rel := range []string{"planned_relationships.json", "planned_relationships.md"} {
		if _, err := os.Lstat(s.io.path(rel)); err == nil {
			return fmt.Errorf("canonical-only story foundation artifact %s exists without canonical foundation", rel)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect legacy story foundation artifact %s: %w", rel, err)
		}
	}
	entries, err := os.ReadDir(s.io.path(foundationRootDir))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect legacy story foundation metadata: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("canonical-only story foundation metadata %s exists without canonical foundation", filepath.ToSlash(filepath.Join(foundationRootDir, entries[0].Name())))
	}
	if _, err := s.loadLegacyUnlocked(); err != nil {
		return err
	}
	return nil
}

// SaveCAS atomically replaces all foundation sections when expectedRevision
// still matches. A read-only legacy aggregate has revision zero.
func (s *FoundationStore) SaveCAS(candidate domain.StoryFoundation, expectedRevision int64) (domain.StoryFoundation, error) {
	if s.withSemanticMutation == nil {
		return s.saveCAS(candidate, expectedRevision, false)
	}
	var saved domain.StoryFoundation
	err := s.withSemanticMutation("save story foundation", func() error {
		var err error
		saved, err = s.saveCAS(candidate, expectedRevision, false)
		return err
	})
	return saved, err
}

// SaveRevisionCAS is the Foundation-revision authority. It permits a reviewed
// candidate to replace confirmed core fields while retaining the same
// crash-recoverable canonical/projection journal used by ordinary saves.
func (s *FoundationStore) SaveRevisionCAS(candidate domain.StoryFoundation, expectedRevision int64) (domain.StoryFoundation, error) {
	if s.withSemanticMutation == nil {
		return s.saveCAS(candidate, expectedRevision, true)
	}
	var saved domain.StoryFoundation
	err := s.withSemanticMutation("publish reviewed foundation revision", func() error {
		var err error
		saved, err = s.saveCAS(candidate, expectedRevision, true)
		return err
	})
	return saved, err
}

func (s *FoundationStore) saveCoreCastCAS(candidate domain.StoryFoundation, expectedRevision int64) (domain.StoryFoundation, error) {
	return s.saveCAS(candidate, expectedRevision, true)
}

func (s *FoundationStore) saveCAS(candidate domain.StoryFoundation, expectedRevision int64, coreCastAuthorized bool) (domain.StoryFoundation, error) {
	epoch := s.lifecycle.captureMutationEpoch()
	s.runLifecycleHook(foundationLifecycleMutationEntered)
	s.projectMu.Lock()
	defer s.projectMu.Unlock()
	if err := s.lifecycle.validateMutationEpoch(epoch); err != nil {
		return domain.StoryFoundation{}, err
	}
	if err := s.recoverUnlocked(); err != nil {
		return domain.StoryFoundation{}, fmt.Errorf("recover story foundation before save: %w", err)
	}
	current, canonicalExists, err := s.loadForWriteUnlocked()
	if err != nil {
		return domain.StoryFoundation{}, err
	}
	if current.Revision != expectedRevision {
		return domain.StoryFoundation{}, &FoundationConflictError{Expected: expectedRevision, Actual: current.Revision}
	}
	return s.commitCandidateUnlocked(current, candidate, canonicalExists, coreCastAuthorized)
}

func (s *FoundationStore) updatePremise(content string) error {
	_, err := s.updateSection(func(value *domain.StoryFoundation) { value.Premise = content })
	return err
}

func (s *FoundationStore) updateCharacters(characters []domain.Character) error {
	_, err := s.updateSection(func(value *domain.StoryFoundation) { value.Characters = characters })
	return err
}

func (s *FoundationStore) updateRelationships(relationships []domain.CharacterRelationship, reviewed bool) error {
	_, err := s.updateSection(func(value *domain.StoryFoundation) {
		value.Relationships = relationships
		value.RelationshipsReviewed = reviewed
	})
	return err
}

func (s *FoundationStore) updateWorldRules(rules []domain.WorldRule) error {
	_, err := s.updateSection(func(value *domain.StoryFoundation) { value.WorldRules = rules })
	return err
}

func (s *FoundationStore) updateSection(change func(*domain.StoryFoundation)) (domain.StoryFoundation, error) {
	epoch := s.lifecycle.captureMutationEpoch()
	s.runLifecycleHook(foundationLifecycleMutationEntered)
	s.projectMu.Lock()
	defer s.projectMu.Unlock()
	if err := s.lifecycle.validateMutationEpoch(epoch); err != nil {
		return domain.StoryFoundation{}, err
	}
	if err := s.recoverUnlocked(); err != nil {
		return domain.StoryFoundation{}, fmt.Errorf("recover story foundation before section update: %w", err)
	}
	current, canonicalExists, err := s.loadForWriteUnlocked()
	if err != nil {
		return domain.StoryFoundation{}, err
	}
	candidate := domain.CloneStoryFoundation(current)
	change(&candidate)
	return s.commitCandidateUnlocked(current, candidate, canonicalExists, false)
}

func (s *FoundationStore) commitCandidateUnlocked(current, candidate domain.StoryFoundation, canonicalExists, coreCastAuthorized bool) (domain.StoryFoundation, error) {
	candidate.SchemaVersion = domain.StoryFoundationSchemaVersion
	candidate.Revision = current.Revision
	candidate.UpdatedAt = current.UpdatedAt
	normalized, err := domain.NormalizeStoryFoundation(candidate)
	if err != nil {
		return domain.StoryFoundation{}, fmt.Errorf("normalize story foundation candidate: %w", err)
	}
	if !coreCastAuthorized {
		if err := s.validateCoreCastAuthorityUnlocked(normalized); err != nil {
			return domain.StoryFoundation{}, err
		}
	}
	currentContent, err := domain.FoundationContentSignature(current)
	if err != nil {
		return domain.StoryFoundation{}, fmt.Errorf("sign current story foundation: %w", err)
	}
	candidateContent, err := domain.FoundationContentSignature(normalized)
	if err != nil {
		return domain.StoryFoundation{}, fmt.Errorf("sign story foundation candidate: %w", err)
	}
	semanticChanged := currentContent != candidateContent
	if !canonicalExists {
		normalized.Revision = 1
		normalized.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	} else if semanticChanged {
		normalized.Revision = current.Revision + 1
		normalized.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if canonicalExists && !semanticChanged && foundationsEqualForStorage(current, normalized) && !s.canonicalFoundationNeedsMigrationUnlocked() {
		if err := s.verifyCommittedUnlocked(current); err == nil {
			return current, nil
		}
	}
	if err := s.commitTransactionUnlocked(current, normalized, canonicalExists); err != nil {
		return domain.StoryFoundation{}, err
	}
	return normalized, nil
}

func (s *FoundationStore) canonicalFoundationNeedsMigrationUnlocked() bool {
	var persisted domain.StoryFoundation
	if err := s.io.ReadJSON(foundationCanonicalFile, &persisted); err != nil {
		return true
	}
	if persisted.SchemaVersion != domain.StoryFoundationSchemaVersion {
		return true
	}
	for _, relationship := range persisted.Relationships {
		if relationship.Direction == "" || relationship.Direction == domain.RelationshipDirectionMutual {
			return true
		}
	}
	return false
}

func (s *FoundationStore) validateCoreCastAuthorityUnlocked(candidate domain.StoryFoundation) error {
	if s.coreCast == nil {
		return nil
	}
	contract, err := s.coreCast.loadUnlocked()
	if err != nil {
		return fmt.Errorf("validate confirmed core cast authority: %w", err)
	}
	if contract == nil || contract.ConfirmedSignature != contract.ContentSignature ||
		contract.PublishReceipt.Status != "published" || contract.PublishReceipt.ContentSignature != contract.ContentSignature {
		return nil
	}
	if err := domain.ValidateFoundationPreservesCoreCast(candidate, *contract); err != nil {
		return fmt.Errorf("confirmed core cast is authoritative for story foundation characters and relationships: %w", err)
	}
	return nil
}

func (s *FoundationStore) loadForWriteUnlocked() (domain.StoryFoundation, bool, error) {
	value, err := s.loadCanonicalUnlocked()
	if err == nil {
		return value, true, nil
	}
	if !os.IsNotExist(err) {
		return domain.StoryFoundation{}, false, fmt.Errorf("load canonical story foundation: %w", err)
	}
	legacy, legacyErr := s.loadLegacyUnlocked()
	return legacy, false, legacyErr
}

func (s *FoundationStore) loadCurrentUnlocked(verify bool) (domain.StoryFoundation, error) {
	value, err := s.loadCanonicalUnlocked()
	if err == nil {
		if verify {
			if err := s.verifyCommittedUnlocked(value); err != nil {
				return domain.StoryFoundation{}, err
			}
		}
		return value, nil
	}
	if !os.IsNotExist(err) {
		return domain.StoryFoundation{}, fmt.Errorf("load canonical story foundation: %w", err)
	}
	return s.loadLegacyUnlocked()
}

func (s *FoundationStore) loadCanonicalUnlocked() (domain.StoryFoundation, error) {
	var value domain.StoryFoundation
	if err := s.io.ReadJSON(foundationCanonicalFile, &value); err != nil {
		return domain.StoryFoundation{}, err
	}
	normalized, err := domain.NormalizeStoryFoundation(value)
	if err != nil {
		return domain.StoryFoundation{}, err
	}
	normalized.Revision = value.Revision
	normalized.UpdatedAt = value.UpdatedAt
	if normalized.Characters == nil {
		normalized.Characters = []domain.Character{}
	}
	if normalized.Relationships == nil {
		normalized.Relationships = []domain.CharacterRelationship{}
	}
	if normalized.WorldRules == nil {
		normalized.WorldRules = []domain.WorldRule{}
	}
	return normalized, nil
}

func (s *FoundationStore) loadLegacyUnlocked() (domain.StoryFoundation, error) {
	value := domain.StoryFoundation{SchemaVersion: domain.StoryFoundationSchemaVersion}
	premise, err := s.io.ReadFile("premise.md")
	if err != nil && !os.IsNotExist(err) {
		return domain.StoryFoundation{}, fmt.Errorf("load legacy premise: %w", err)
	}
	if err == nil {
		value.Premise = string(premise)
	}
	if err := s.io.ReadJSON("characters.json", &value.Characters); err != nil && !os.IsNotExist(err) {
		return domain.StoryFoundation{}, fmt.Errorf("load legacy characters: %w", err)
	}
	if err := s.io.ReadJSON("world_rules.json", &value.WorldRules); err != nil && !os.IsNotExist(err) {
		return domain.StoryFoundation{}, fmt.Errorf("load legacy world rules: %w", err)
	}
	// planned relationships are a projection only when a canonical foundation
	// exists; legacy aggregation deliberately starts with an empty plan.
	normalized, err := domain.NormalizeStoryFoundation(value)
	if err != nil {
		return domain.StoryFoundation{}, fmt.Errorf("aggregate legacy story foundation: %w", err)
	}
	return normalized, nil
}

func (s *FoundationStore) commitTransactionUnlocked(base, value domain.StoryFoundation, canonicalExists bool) error {
	files, manifest, err := foundationTransactionPayloads(value)
	if err != nil {
		return err
	}
	transactionID := fmt.Sprintf("r%d-%d", value.Revision, time.Now().UTC().UnixNano())
	stageRoot := foundationStageDir + "/" + transactionID
	for _, target := range sortedFoundationPaths(files) {
		if err := s.io.WriteFileUnlocked(stageRoot+"/"+target, files[target]); err != nil {
			return fmt.Errorf("stage story foundation file %s: %w", target, err)
		}
	}
	if err := s.injectFailure(foundationFailAfterStage); err != nil {
		return err
	}
	baseCanonicalSignature := ""
	if canonicalExists {
		baseCanonical, readErr := s.io.ReadFile(foundationCanonicalFile)
		if readErr != nil {
			return fmt.Errorf("read base story foundation canonical: %w", readErr)
		}
		baseCanonicalSignature = fileDigest(baseCanonical)
	}
	journal := foundationJournal{
		Version: foundationJournalVersion, Stage: foundationJournalPrepared, TransactionID: transactionID,
		BaseRevision: base.Revision, BaseCanonicalSignature: baseCanonicalSignature,
		CandidateCanonicalSignature: fileDigest(files[foundationCanonicalFile]),
		ContentSignature:            manifest.ContentSignature, AuditSignature: manifest.AuditSignature, Foundation: value,
	}
	if err := s.io.WriteJSON(foundationJournalFile, journal); err != nil {
		return fmt.Errorf("write story foundation journal: %w", err)
	}
	if err := s.injectFailure(foundationFailAfterJournal); err != nil {
		return err
	}
	return s.finishJournalUnlocked(journal)
}

func (s *FoundationStore) recoverUnlocked() error {
	var journal foundationJournal
	if err := s.io.ReadJSON(foundationJournalFile, &journal); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read story foundation journal: %w", err)
		}
		_, cleanupErr := s.io.RemoveAllRel(foundationStageDir)
		return cleanupErr
	}
	return s.finishJournalUnlocked(journal)
}

func (s *FoundationStore) finishJournalUnlocked(journal foundationJournal) error {
	if journal.Version != foundationJournalVersion || strings.TrimSpace(journal.TransactionID) == "" {
		return fmt.Errorf("story foundation journal identity is invalid")
	}
	if journal.Stage != foundationJournalPrepared && journal.Stage != foundationJournalCanonicalCommitted && journal.Stage != foundationJournalProjectionsCommitted {
		return fmt.Errorf("story foundation journal stage %q is invalid", journal.Stage)
	}
	normalized, err := domain.NormalizeStoryFoundation(journal.Foundation)
	if err != nil {
		return fmt.Errorf("story foundation journal candidate is invalid: stage=%s revision=%d", journal.Stage, journal.Foundation.Revision)
	}
	normalized.Revision = journal.Foundation.Revision
	normalized.UpdatedAt = journal.Foundation.UpdatedAt
	files, manifest, err := foundationTransactionPayloads(normalized)
	if err != nil {
		return fmt.Errorf("prepare story foundation recovery payloads: stage=%s revision=%d", journal.Stage, normalized.Revision)
	}
	if manifest.ContentSignature != journal.ContentSignature || manifest.AuditSignature != journal.AuditSignature {
		return fmt.Errorf("story foundation journal signature mismatch")
	}
	if fileDigest(files[foundationCanonicalFile]) != journal.CandidateCanonicalSignature {
		return fmt.Errorf("story foundation journal candidate signature mismatch")
	}
	stageRoot := foundationStageDir + "/" + journal.TransactionID
	for target, expected := range files {
		staged, readErr := s.io.ReadFile(stageRoot + "/" + target)
		if readErr != nil {
			return fmt.Errorf("read staged story foundation file %s: %w", target, readErr)
		}
		if fileDigest(staged) != fileDigest(expected) {
			return fmt.Errorf("staged story foundation file %s signature mismatch", target)
		}
	}
	canonicalState, err := s.matchJournalCanonicalStateUnlocked(journal, normalized)
	if err != nil {
		return err
	}
	if canonicalState != foundationCanonicalCandidate {
		canonical := files[foundationCanonicalFile]
		if err := s.io.WriteFileUnlocked(foundationCanonicalFile, canonical); err != nil {
			return fmt.Errorf("commit canonical story foundation: %w", err)
		}
	}
	journal.Stage = foundationJournalCanonicalCommitted
	if err := s.io.WriteJSON(foundationJournalFile, journal); err != nil {
		return fmt.Errorf("advance story foundation journal after canonical commit: %w", err)
	}
	if err := s.injectFailure(foundationFailAfterCanonical); err != nil {
		return err
	}
	for _, target := range foundationProjectionPaths() {
		staged := files[target]
		if err := s.io.WriteFileUnlocked(target, staged); err != nil {
			return fmt.Errorf("commit story foundation projection %s: %w", target, err)
		}
		if err := s.injectFailure(foundationFailAfterProjection); err != nil {
			return err
		}
	}
	stagedManifest := files[foundationManifestFile]
	if err := s.io.WriteFileUnlocked(foundationManifestFile, stagedManifest); err != nil {
		return fmt.Errorf("commit story foundation projection manifest: %w", err)
	}
	if err := s.injectFailure(foundationFailAfterManifest); err != nil {
		return err
	}
	journal.Stage = foundationJournalProjectionsCommitted
	if err := s.io.WriteJSON(foundationJournalFile, journal); err != nil {
		return fmt.Errorf("advance story foundation journal after projections: %w", err)
	}
	if err := s.verifyCommittedUnlocked(normalized); err != nil {
		return fmt.Errorf("verify committed story foundation: %w", err)
	}
	if err := s.io.RemoveFile(foundationJournalFile); err != nil {
		return fmt.Errorf("clean story foundation journal: %w", err)
	}
	if _, err := s.io.RemoveAllRel(stageRoot); err != nil {
		return fmt.Errorf("clean story foundation stage: %w", err)
	}
	return nil
}

type foundationCanonicalState int

const (
	foundationCanonicalMissing foundationCanonicalState = iota
	foundationCanonicalBase
	foundationCanonicalCandidate
)

func (s *FoundationStore) matchJournalCanonicalStateUnlocked(journal foundationJournal, candidate domain.StoryFoundation) (foundationCanonicalState, error) {
	canonical, err := s.io.ReadFile(foundationCanonicalFile)
	if os.IsNotExist(err) {
		if journal.BaseRevision == 0 && journal.BaseCanonicalSignature == "" {
			return foundationCanonicalMissing, nil
		}
		return 0, foundationJournalConflictError(journal, candidate.Revision, -1, "missing")
	}
	if err != nil {
		return 0, fmt.Errorf("read current story foundation canonical: %w", err)
	}
	var identity struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(canonical, &identity); err != nil {
		return 0, fmt.Errorf("decode current story foundation canonical identity: %w", err)
	}
	signature := fileDigest(canonical)
	if identity.Revision == journal.BaseRevision && signature == journal.BaseCanonicalSignature {
		return foundationCanonicalBase, nil
	}
	if identity.Revision == candidate.Revision && signature == journal.CandidateCanonicalSignature {
		return foundationCanonicalCandidate, nil
	}
	return 0, foundationJournalConflictError(journal, candidate.Revision, identity.Revision, signature)
}

func foundationJournalConflictError(journal foundationJournal, candidateRevision, currentRevision int64, currentSignature string) error {
	return fmt.Errorf(
		"story foundation journal conflict: stage=%s base_revision=%d base_signature=%s candidate_revision=%d candidate_signature=%s current_revision=%d current_signature=%s",
		journal.Stage,
		journal.BaseRevision,
		shortFoundationSignature(journal.BaseCanonicalSignature),
		candidateRevision,
		shortFoundationSignature(journal.CandidateCanonicalSignature),
		currentRevision,
		shortFoundationSignature(currentSignature),
	)
}

func shortFoundationSignature(signature string) string {
	if len(signature) <= 12 {
		return signature
	}
	return signature[:12]
}

func (s *FoundationStore) verifyCommittedUnlocked(value domain.StoryFoundation) error {
	var manifest foundationProjectionManifest
	if err := s.io.ReadJSON(foundationManifestFile, &manifest); err != nil {
		return fmt.Errorf("read story foundation projection manifest: %w", err)
	}
	if manifest.Version != 1 || manifest.Revision != value.Revision {
		return fmt.Errorf("story foundation projection revision mismatch: canonical=%d manifest=%d", value.Revision, manifest.Revision)
	}
	content, err := domain.FoundationContentSignature(value)
	if err != nil {
		return err
	}
	audit, err := domain.FoundationAuditSignature(value)
	if err != nil {
		return err
	}
	if content != manifest.ContentSignature || audit != manifest.AuditSignature {
		return fmt.Errorf("story foundation canonical and projection manifest signatures differ")
	}
	expectedPaths := append([]string{foundationCanonicalFile}, foundationProjectionPaths()...)
	if len(manifest.Files) != len(expectedPaths) {
		return fmt.Errorf("story foundation projection manifest file set is incomplete")
	}
	for _, rel := range expectedPaths {
		data, err := s.io.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read story foundation committed file %s: %w", rel, err)
		}
		if fileDigest(data) != manifest.Files[rel] {
			return fmt.Errorf("story foundation committed file %s signature mismatch", rel)
		}
	}
	return nil
}

func foundationTransactionPayloads(value domain.StoryFoundation) (map[string][]byte, foundationProjectionManifest, error) {
	normalized, err := domain.NormalizeStoryFoundation(value)
	if err != nil {
		return nil, foundationProjectionManifest{}, err
	}
	normalized.Revision = value.Revision
	normalized.UpdatedAt = value.UpdatedAt
	if normalized.Characters == nil {
		normalized.Characters = []domain.Character{}
	}
	if normalized.Relationships == nil {
		normalized.Relationships = []domain.CharacterRelationship{}
	}
	if normalized.WorldRules == nil {
		normalized.WorldRules = []domain.WorldRule{}
	}
	canonical, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, foundationProjectionManifest{}, err
	}
	characters, err := json.MarshalIndent(normalized.Characters, "", "  ")
	if err != nil {
		return nil, foundationProjectionManifest{}, err
	}
	rules, err := json.MarshalIndent(normalized.WorldRules, "", "  ")
	if err != nil {
		return nil, foundationProjectionManifest{}, err
	}
	relationships, err := json.MarshalIndent(normalized.Relationships, "", "  ")
	if err != nil {
		return nil, foundationProjectionManifest{}, err
	}
	files := map[string][]byte{
		foundationCanonicalFile:      canonical,
		"premise.md":                 []byte(normalized.Premise),
		"characters.json":            characters,
		"characters.md":              []byte(renderCharacters(normalized.Characters)),
		"world_rules.json":           rules,
		"world_rules.md":             []byte(renderWorldRules(normalized.WorldRules)),
		"planned_relationships.json": relationships,
		"planned_relationships.md":   []byte(renderPlannedRelationships(normalized.Relationships)),
	}
	content, err := domain.FoundationContentSignature(normalized)
	if err != nil {
		return nil, foundationProjectionManifest{}, err
	}
	audit, err := domain.FoundationAuditSignature(normalized)
	if err != nil {
		return nil, foundationProjectionManifest{}, err
	}
	manifest := foundationProjectionManifest{
		Version: 1, Revision: normalized.Revision, ContentSignature: content,
		AuditSignature: audit, Files: make(map[string]string, len(files)),
	}
	for rel, data := range files {
		manifest.Files[rel] = fileDigest(data)
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, foundationProjectionManifest{}, err
	}
	files[foundationManifestFile] = manifestData
	return files, manifest, nil
}

func foundationProjectionPaths() []string {
	return []string{
		"premise.md", "characters.json", "characters.md", "world_rules.json", "world_rules.md",
		"planned_relationships.json", "planned_relationships.md",
	}
}

func sortedFoundationPaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for rel := range files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths
}

func renderPlannedRelationships(relations []domain.CharacterRelationship) string {
	var builder strings.Builder
	builder.WriteString("# 计划人物关系\n\n")
	for _, relation := range relations {
		arrow := "→"
		if relation.Direction == domain.RelationshipDirectionBidirectional || relation.Direction == domain.RelationshipDirectionMutual {
			arrow = "↔"
		} else if relation.Direction == domain.RelationshipDirectionUndirected {
			arrow = "—"
		}
		fmt.Fprintf(&builder, "- **%s %s %s**：%s", relation.SourceCharacterID, arrow, relation.TargetCharacterID, relation.Type)
		if relation.Label != "" {
			fmt.Fprintf(&builder, "（%s）", relation.Label)
		}
		fmt.Fprintf(&builder, "，状态：%s\n", relation.Status)
		if relation.Description != "" {
			fmt.Fprintf(&builder, "  - %s\n", relation.Description)
		}
	}
	return builder.String()
}

func foundationsEqualForStorage(a, b domain.StoryFoundation) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func fileDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *FoundationStore) injectFailure(stage string) error {
	if s.failpoint == nil {
		return nil
	}
	if err := s.failpoint(stage); err != nil {
		return fmt.Errorf("story foundation transaction %s: %w", stage, err)
	}
	return nil
}

const (
	foundationLifecycleMutationEntered = "mutation_entered"
	foundationLifecycleRollbackStarted = "rollback_started"
	foundationLifecycleRollbackLocked  = "rollback_locked"
)

func (s *FoundationStore) runLifecycleHook(stage string) {
	if s.lifecycleHook != nil {
		s.lifecycleHook(stage)
	}
}
