package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

var ErrCoreCastContentSignatureMismatch = errors.New("core cast content signature does not match persisted content")

const (
	coreCastContractFile = "meta/cocreate/core_cast_contract.json"
	coreCastGateFile     = "meta/cocreate/core_cast_gate.json"
)

type CoreCastGateBinding struct {
	Version              int                 `json:"version"`
	Mode                 domain.CoreCastMode `json:"mode"`
	DraftRevision        int64               `json:"draft_revision"`
	DraftHash            string              `json:"draft_hash"`
	SourceSignature      string              `json:"source_signature,omitempty"`
	AdaptationIntentHash string              `json:"adaptation_intent_hash,omitempty"`
	UpdatedAt            string              `json:"updated_at"`
}

var coreCastProjectLocks sync.Map

type CoreCastConflictError struct {
	Expected   int64
	Actual     int64
	Signature  string
	Completion domain.CoreCastCompletionResult
}

func (e *CoreCastConflictError) Error() string {
	return fmt.Sprintf("core cast revision conflict: expected %d, actual %d", e.Expected, e.Actual)
}

type CoreCastSignatureConflictError struct {
	Expected   string
	Actual     string
	Revision   int64
	Completion domain.CoreCastCompletionResult
}

type CoreCastValidationError struct{ Err error }

func (e *CoreCastValidationError) Error() string { return e.Err.Error() }
func (e *CoreCastValidationError) Unwrap() error { return e.Err }

func (e *CoreCastSignatureConflictError) Error() string {
	return "core cast content signature conflict"
}

type CoreCastStore struct {
	io *IO
	mu *sync.Mutex
}

func newCoreCastStore(io *IO) *CoreCastStore {
	key, err := filepath.Abs(io.dir)
	if err != nil {
		key = io.dir
	}
	key = strings.ToLower(filepath.Clean(key))
	value, _ := coreCastProjectLocks.LoadOrStore(key, &sync.Mutex{})
	return &CoreCastStore{io: io, mu: value.(*sync.Mutex)}
}

func (s *CoreCastStore) Load() (*domain.CoreCastContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

// LoadWithLegacySignatureRepair loads the current contract and, only when the
// persisted value is an unconfirmed and unpublished legacy draft, refreshes
// the signature produced by an older normalization contract. Confirmed or
// published evidence continues to fail closed.
func (s *CoreCastStore) LoadWithLegacySignatureRepair() (*domain.CoreCastContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if !errors.Is(err, ErrCoreCastContentSignatureMismatch) {
		return current, err
	}
	repaired, err := s.repairLegacyUnconfirmedSignatureUnlocked()
	if err != nil {
		return nil, err
	}
	return &repaired, nil
}

func (s *CoreCastStore) LoadGateBinding() (*CoreCastGateBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadGateBindingUnlocked()
}

func (s *CoreCastStore) SaveGateBinding(candidate CoreCastGateBinding) (CoreCastGateBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate.Version = 1
	candidate.DraftHash = strings.TrimSpace(candidate.DraftHash)
	candidate.SourceSignature = strings.TrimSpace(candidate.SourceSignature)
	candidate.AdaptationIntentHash = strings.TrimSpace(candidate.AdaptationIntentHash)
	if candidate.Mode != domain.CoreCastModeNormal && candidate.Mode != domain.CoreCastModeAdaptation {
		return CoreCastGateBinding{}, fmt.Errorf("core cast gate mode %q is invalid", candidate.Mode)
	}
	if candidate.DraftRevision <= 0 || candidate.DraftHash == "" {
		return CoreCastGateBinding{}, fmt.Errorf("core cast gate requires a positive semantic draft revision and normalized draft hash")
	}
	current, err := s.loadGateBindingUnlocked()
	if err != nil {
		return CoreCastGateBinding{}, err
	}
	if current != nil {
		if candidate.DraftRevision < current.DraftRevision {
			return CoreCastGateBinding{}, fmt.Errorf("core cast gate draft revision cannot move backwards")
		}
		if candidate.DraftRevision == current.DraftRevision {
			if candidate.Mode != current.Mode || candidate.DraftHash != current.DraftHash ||
				candidate.SourceSignature != current.SourceSignature || candidate.AdaptationIntentHash != current.AdaptationIntentHash {
				return CoreCastGateBinding{}, fmt.Errorf("core cast gate semantic binding conflicts at revision %d", candidate.DraftRevision)
			}
			return *current, nil
		}
	}
	candidate.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.io.WriteJSON(coreCastGateFile, candidate); err != nil {
		return CoreCastGateBinding{}, fmt.Errorf("save core cast gate binding: %w", err)
	}
	return candidate, nil
}

func (s *CoreCastStore) RequireConfirmedGate(expected CoreCastGateBinding, sourceCharacters, sourceMajor []domain.SourceMajorCharacter, sourceResolutionMissing []domain.CoreCastMissingItem) (domain.CoreCastContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, err := s.loadGateBindingUnlocked()
	if err != nil {
		return domain.CoreCastContract{}, err
	}
	if binding == nil {
		return domain.CoreCastContract{}, fmt.Errorf("core cast gate binding does not exist")
	}
	expected.DraftHash = strings.TrimSpace(expected.DraftHash)
	expected.SourceSignature = strings.TrimSpace(expected.SourceSignature)
	expected.AdaptationIntentHash = strings.TrimSpace(expected.AdaptationIntentHash)
	if binding.Mode != expected.Mode || binding.DraftRevision != expected.DraftRevision || binding.DraftHash != expected.DraftHash ||
		binding.SourceSignature != expected.SourceSignature || binding.AdaptationIntentHash != expected.AdaptationIntentHash {
		return domain.CoreCastContract{}, fmt.Errorf("core cast gate binding is stale for the current draft, source, or adaptation intent")
	}
	contract, err := s.requireCurrentUnlocked()
	if err != nil {
		return domain.CoreCastContract{}, err
	}
	if contract.Mode != binding.Mode || contract.DraftRevision != binding.DraftRevision || contract.DraftHash != binding.DraftHash ||
		contract.SourceSignature != binding.SourceSignature || contract.AdaptationIntentHash != binding.AdaptationIntentHash {
		return domain.CoreCastContract{}, fmt.Errorf("core cast contract is stale for the persisted gate binding")
	}
	if binding.Mode == domain.CoreCastModeAdaptation &&
		len(sourceCharacters) == 0 && len(sourceMajor) == 0 &&
		len(contract.SourceDispositions) > 0 {
		var source domain.AdaptationSourceFoundation
		if sourceErr := s.io.ReadJSON(adaptationSourceFoundationFile, &source); sourceErr == nil {
			sourceCharacters = domain.ResolveSourceCharacters(source)
			sourceMajor = coreCastDispositionSourceCharacters(sourceCharacters, *contract)
		} else if !os.IsNotExist(sourceErr) {
			return domain.CoreCastContract{}, fmt.Errorf(
				"load adaptation SourceFoundation for core cast gate: %w",
				sourceErr,
			)
		}
	}
	completion := mergeCoreCastCompletion(domain.CoreCastCompletion(*contract, sourceCharacters, sourceMajor), sourceResolutionMissing)
	if !completion.Complete {
		return domain.CoreCastContract{}, fmt.Errorf(
			"core cast confirmation gate is not satisfied: %s",
			strings.Join(completion.BlockingReasons, "; "),
		)
	}
	if contract.ConfirmedSignature != contract.ContentSignature {
		return domain.CoreCastContract{}, fmt.Errorf("core cast confirmation gate is not satisfied: confirmed signature is stale")
	}
	return *contract, nil
}

func coreCastDispositionSourceCharacters(
	sourceCharacters []domain.SourceMajorCharacter,
	contract domain.CoreCastContract,
) []domain.SourceMajorCharacter {
	disposed := make(map[string]struct{}, len(contract.SourceDispositions))
	for _, disposition := range contract.SourceDispositions {
		disposed[disposition.SourceCharacterID] = struct{}{}
	}
	out := make([]domain.SourceMajorCharacter, 0, len(disposed))
	for _, source := range sourceCharacters {
		if _, exists := disposed[source.ID]; exists {
			out = append(out, source)
		}
	}
	return out
}

func (s *CoreCastStore) SaveCAS(candidate domain.CoreCastContract, expectedRevision int64) (domain.CoreCastContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return domain.CoreCastContract{}, err
	}
	actual := int64(0)
	if current != nil {
		actual = current.Revision
	}
	if actual != expectedRevision {
		return domain.CoreCastContract{}, coreCastRevisionConflict(expectedRevision, actual, current, nil)
	}
	candidate.Revision = actual
	normalized, err := domain.NormalizeCoreCastContract(candidate)
	if err != nil {
		return domain.CoreCastContract{}, &CoreCastValidationError{Err: err}
	}
	normalized.ConfirmedSignature = ""
	normalized.ConfirmedAt = ""
	normalized.PublishReceipt = domain.CoreCastPublishReceipt{}
	if current != nil && normalized.ContentSignature == current.ContentSignature {
		normalized.ConfirmedSignature = current.ConfirmedSignature
		normalized.ConfirmedAt = current.ConfirmedAt
		normalized.PublishReceipt = current.PublishReceipt
	}
	if current != nil && coreCastEqual(*current, normalized) {
		return *current, nil
	}
	normalized.Revision = actual + 1
	if err := s.io.WriteJSON(coreCastContractFile, normalized); err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("save core cast contract: %w", err)
	}
	return normalized, nil
}

// CompleteMissingGenders fills omitted structured identity metadata on an
// already confirmed contract. Existing values cannot be changed through this
// path. The explicit correction is re-signed and remains published.
func (s *CoreCastStore) CompleteMissingGenders(
	expectedRevision int64,
	genders map[string]string,
	foundationRevision int64,
) (domain.CoreCastContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.requireCurrentUnlocked()
	if err != nil {
		return domain.CoreCastContract{}, err
	}
	if current.Revision != expectedRevision {
		return domain.CoreCastContract{}, coreCastRevisionConflict(expectedRevision, current.Revision, current, nil)
	}
	if current.ConfirmedSignature != current.ContentSignature ||
		current.PublishReceipt.Status != "published" ||
		current.PublishReceipt.ContentSignature != current.ContentSignature {
		return domain.CoreCastContract{}, fmt.Errorf("missing-gender correction requires a confirmed published core cast")
	}
	candidate := *current
	candidate.Members = append([]domain.CoreCastMember(nil), current.Members...)
	pending := make(map[string]string, len(genders))
	for id, gender := range genders {
		id = strings.TrimSpace(id)
		gender = strings.ToLower(strings.TrimSpace(gender))
		switch gender {
		case "male", "female", "nonbinary", "unspecified":
		default:
			return domain.CoreCastContract{}, fmt.Errorf("core character %q gender %q is invalid", id, gender)
		}
		pending[id] = gender
	}
	for index := range candidate.Members {
		candidate.Members[index].Character = domain.CloneCharacter(current.Members[index].Character)
		character := &candidate.Members[index].Character
		gender, ok := pending[character.ID]
		if !ok {
			continue
		}
		if strings.TrimSpace(character.Gender) != "" {
			return domain.CoreCastContract{}, fmt.Errorf("core character %q already has gender %q; identity correction cannot overwrite it", character.ID, character.Gender)
		}
		character.Gender = gender
		delete(pending, character.ID)
	}
	if len(pending) > 0 {
		for id := range pending {
			return domain.CoreCastContract{}, fmt.Errorf("core character %q does not exist", id)
		}
	}
	candidate.ConfirmedSignature = ""
	candidate.ConfirmedAt = ""
	candidate.PublishReceipt = domain.CoreCastPublishReceipt{}
	normalized, err := domain.NormalizeCoreCastContract(candidate)
	if err != nil {
		return domain.CoreCastContract{}, &CoreCastValidationError{Err: err}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	normalized.Revision = current.Revision + 1
	normalized.ConfirmedSignature = normalized.ContentSignature
	normalized.ConfirmedAt = now
	normalized.PublishReceipt = domain.CoreCastPublishReceipt{
		Status:             "published",
		ContentSignature:   normalized.ContentSignature,
		FoundationRevision: foundationRevision,
		PublishedAt:        now,
	}
	if err := s.io.WriteJSON(coreCastContractFile, normalized); err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("save corrected core cast contract: %w", err)
	}
	return normalized, nil
}

// RepairLegacyUnconfirmedSignature migrates an unconfirmed legacy draft whose
// signature was produced by an older normalization contract. Confirmed or
// published evidence is never repaired through this path.
func (s *CoreCastStore) RepairLegacyUnconfirmedSignature() (domain.CoreCastContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repairLegacyUnconfirmedSignatureUnlocked()
}

func (s *CoreCastStore) repairLegacyUnconfirmedSignatureUnlocked() (domain.CoreCastContract, error) {
	var value domain.CoreCastContract
	if err := s.io.ReadJSON(coreCastContractFile, &value); err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("load legacy core cast contract: %w", err)
	}
	if strings.TrimSpace(value.ConfirmedSignature) != "" ||
		strings.TrimSpace(value.PublishReceipt.Status) != "" ||
		strings.TrimSpace(value.PublishReceipt.ContentSignature) != "" {
		return domain.CoreCastContract{}, fmt.Errorf("confirmed or published core cast evidence cannot be signature-migrated")
	}
	normalized, err := domain.NormalizeCoreCastContract(value)
	if err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("normalize legacy core cast contract: %w", err)
	}
	if strings.TrimSpace(value.ContentSignature) == normalized.ContentSignature {
		return normalized, nil
	}
	normalized.Revision = value.Revision + 1
	normalized.ConfirmedSignature = ""
	normalized.ConfirmedAt = ""
	normalized.PublishReceipt = domain.CoreCastPublishReceipt{}
	if err := s.io.WriteJSON(coreCastContractFile, normalized); err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("migrate legacy core cast signature: %w", err)
	}
	return normalized, nil
}

func (s *CoreCastStore) ConfirmCAS(expectedRevision int64, expectedSignature string, sourceCharacters, sourceMajor []domain.SourceMajorCharacter, sourceResolutionMissing []domain.CoreCastMissingItem) (domain.CoreCastContract, domain.CoreCastCompletionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.requireCurrentUnlocked()
	if err != nil {
		return domain.CoreCastContract{}, domain.CoreCastCompletionResult{}, err
	}
	completion := domain.CoreCastCompletion(*current, sourceCharacters, sourceMajor)
	completion = mergeCoreCastCompletion(completion, sourceResolutionMissing)
	if current.Revision != expectedRevision {
		return domain.CoreCastContract{}, completion, coreCastRevisionConflict(expectedRevision, current.Revision, current, &completion)
	}
	if strings.TrimSpace(expectedSignature) != current.ContentSignature {
		return domain.CoreCastContract{}, completion, &CoreCastSignatureConflictError{
			Expected: strings.TrimSpace(expectedSignature), Actual: current.ContentSignature, Revision: current.Revision, Completion: completion,
		}
	}
	if !completion.Complete {
		return domain.CoreCastContract{}, completion, &CoreCastValidationError{Err: fmt.Errorf("core cast contract is incomplete")}
	}
	if current.ConfirmedSignature == current.ContentSignature {
		return *current, completion, nil
	}
	current.ConfirmedSignature = current.ContentSignature
	current.ConfirmedAt = time.Now().UTC().Format(time.RFC3339Nano)
	current.Revision++
	if err := s.io.WriteJSON(coreCastContractFile, current); err != nil {
		return domain.CoreCastContract{}, completion, fmt.Errorf("confirm core cast contract: %w", err)
	}
	return *current, completion, nil
}

func (s *CoreCastStore) UnconfirmCAS(expectedRevision int64) (domain.CoreCastContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.requireCurrentUnlocked()
	if err != nil {
		return domain.CoreCastContract{}, err
	}
	if current.ConfirmedSignature == "" {
		return *current, nil
	}
	if current.Revision != expectedRevision {
		return domain.CoreCastContract{}, coreCastRevisionConflict(expectedRevision, current.Revision, current, nil)
	}
	current.ConfirmedSignature = ""
	current.ConfirmedAt = ""
	current.Revision++
	if err := s.io.WriteJSON(coreCastContractFile, current); err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("unconfirm core cast contract: %w", err)
	}
	return *current, nil
}

// PublishConfirmed publishes the exact confirmed cast into StoryFoundation.
// When a prior attempt reached Foundation but not the receipt write, section
// equality makes the retry record a receipt without increasing Foundation revision.
func (s *CoreCastStore) PublishConfirmed(foundation *FoundationStore, sourceCharacters, sourceMajor []domain.SourceMajorCharacter, sourceResolutionMissing []domain.CoreCastMissingItem) (domain.CoreCastContract, error) {
	if foundation == nil {
		return domain.CoreCastContract{}, fmt.Errorf("story foundation store is required")
	}
	if published, ok, err := s.currentPublication(foundation); err != nil {
		return domain.CoreCastContract{}, err
	} else if ok {
		return published, nil
	}
	if foundation.withSemanticMutation == nil {
		return s.publishConfirmed(foundation, sourceCharacters, sourceMajor, sourceResolutionMissing)
	}
	var published domain.CoreCastContract
	err := foundation.withSemanticMutation("publish confirmed core cast to story foundation", func() error {
		var err error
		published, err = s.publishConfirmed(foundation, sourceCharacters, sourceMajor, sourceResolutionMissing)
		return err
	})
	return published, err
}

// currentPublication recognizes an already-applied receipt before entering the
// Foundation semantic-mutation guard. This matters during a Foundation
// generation round: Character confirmation publishes the cast with the round
// token, while Resume revalidates the same gate without owning that token.
func (s *CoreCastStore) currentPublication(foundation *FoundationStore) (domain.CoreCastContract, bool, error) {
	s.mu.Lock()
	current, err := s.requireCurrentUnlocked()
	if err != nil {
		s.mu.Unlock()
		return domain.CoreCastContract{}, false, err
	}
	snapshot := *current
	s.mu.Unlock()
	if snapshot.PublishReceipt.Status != "published" ||
		snapshot.PublishReceipt.ContentSignature != snapshot.ContentSignature {
		return domain.CoreCastContract{}, false, nil
	}
	formal, err := foundation.Load()
	if err != nil {
		return domain.CoreCastContract{}, false, fmt.Errorf("load story foundation for core cast receipt: %w", err)
	}
	expected := domain.ApplyCoreCastToFoundation(formal, snapshot)
	charactersEqual, err := domain.StoryFoundationSectionEqual(formal, expected, domain.FoundationSectionCharacters)
	if err != nil {
		return domain.CoreCastContract{}, false, err
	}
	relationshipsEqual, err := domain.StoryFoundationSectionEqual(formal, expected, domain.FoundationSectionRelationships)
	if err != nil {
		return domain.CoreCastContract{}, false, err
	}
	reviewedEqual, err := domain.StoryFoundationSectionEqual(formal, expected, domain.FoundationSectionRelationshipsReviewed)
	if err != nil {
		return domain.CoreCastContract{}, false, err
	}
	if !charactersEqual || !relationshipsEqual || !reviewedEqual {
		return domain.CoreCastContract{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, err := s.requireCurrentUnlocked()
	if err != nil {
		return domain.CoreCastContract{}, false, err
	}
	if latest.Revision != snapshot.Revision ||
		latest.ContentSignature != snapshot.ContentSignature ||
		latest.PublishReceipt != snapshot.PublishReceipt {
		return domain.CoreCastContract{}, false, nil
	}
	return *latest, true, nil
}

func (s *CoreCastStore) publishConfirmed(foundation *FoundationStore, sourceCharacters, sourceMajor []domain.SourceMajorCharacter, sourceResolutionMissing []domain.CoreCastMissingItem) (domain.CoreCastContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.requireCurrentUnlocked()
	if err != nil {
		return domain.CoreCastContract{}, err
	}
	completion := mergeCoreCastCompletion(domain.CoreCastCompletion(*current, sourceCharacters, sourceMajor), sourceResolutionMissing)
	if !completion.Complete {
		return domain.CoreCastContract{}, fmt.Errorf(
			"core cast confirmation gate is not satisfied: %s",
			strings.Join(completion.BlockingReasons, "; "),
		)
	}
	if current.ConfirmedSignature == "" || current.ConfirmedSignature != current.ContentSignature {
		return domain.CoreCastContract{}, fmt.Errorf("core cast confirmation gate is not satisfied: confirmed signature is stale")
	}
	formal, err := foundation.Load()
	if err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("load story foundation for core cast publish: %w", err)
	}
	candidate := domain.CloneStoryFoundation(formal)
	candidate = domain.ApplyCoreCastToFoundation(candidate, *current)
	published, err := foundation.saveCoreCastCAS(candidate, formal.Revision)
	if err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("publish core cast to story foundation: %w", err)
	}
	receipt := domain.CoreCastPublishReceipt{
		Status: "published", ContentSignature: current.ContentSignature,
		FoundationRevision: published.Revision, PublishedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if current.PublishReceipt.Status == receipt.Status && current.PublishReceipt.ContentSignature == receipt.ContentSignature &&
		current.PublishReceipt.FoundationRevision == receipt.FoundationRevision {
		return *current, nil
	}
	current.PublishReceipt = receipt
	current.Revision++
	if err := s.io.WriteJSON(coreCastContractFile, current); err != nil {
		return domain.CoreCastContract{}, fmt.Errorf("save core cast publish receipt: %w", err)
	}
	return *current, nil
}

func (s *CoreCastStore) loadUnlocked() (*domain.CoreCastContract, error) {
	var value domain.CoreCastContract
	if err := s.io.ReadJSON(coreCastContractFile, &value); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load core cast contract: %w", err)
	}
	normalized, err := domain.NormalizeCoreCastContract(value)
	if err != nil {
		return nil, fmt.Errorf("normalize core cast contract: %w", err)
	}
	// Persisted signatures are evidence. Recompute and fail closed on tampering.
	if strings.TrimSpace(value.ContentSignature) != normalized.ContentSignature {
		return nil, ErrCoreCastContentSignatureMismatch
	}
	return &normalized, nil
}

func (s *CoreCastStore) loadGateBindingUnlocked() (*CoreCastGateBinding, error) {
	var value CoreCastGateBinding
	if err := s.io.ReadJSON(coreCastGateFile, &value); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load core cast gate binding: %w", err)
	}
	value.DraftHash = strings.TrimSpace(value.DraftHash)
	value.SourceSignature = strings.TrimSpace(value.SourceSignature)
	value.AdaptationIntentHash = strings.TrimSpace(value.AdaptationIntentHash)
	if value.Version != 1 || (value.Mode != domain.CoreCastModeNormal && value.Mode != domain.CoreCastModeAdaptation) ||
		value.DraftRevision <= 0 || value.DraftHash == "" {
		return nil, fmt.Errorf("persisted core cast gate binding is invalid")
	}
	return &value, nil
}

func (s *CoreCastStore) requireCurrentUnlocked() (*domain.CoreCastContract, error) {
	current, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("core cast contract does not exist")
	}
	return current, nil
}

func coreCastRevisionConflict(expected, actual int64, current *domain.CoreCastContract, completion *domain.CoreCastCompletionResult) error {
	conflict := &CoreCastConflictError{Expected: expected, Actual: actual}
	if current != nil {
		conflict.Signature = current.ContentSignature
	}
	if completion != nil {
		conflict.Completion = *completion
	}
	return conflict
}

func coreCastEqual(left, right domain.CoreCastContract) bool {
	return left.ContentSignature == right.ContentSignature &&
		left.ConfirmedSignature == right.ConfirmedSignature &&
		left.ConfirmedAt == right.ConfirmedAt &&
		left.PublishReceipt == right.PublishReceipt
}

func mergeCoreCastCompletion(base domain.CoreCastCompletionResult, extra []domain.CoreCastMissingItem) domain.CoreCastCompletionResult {
	if len(extra) == 0 {
		return base
	}
	base.Missing = append(base.Missing, extra...)
	seen := make(map[string]struct{}, len(base.BlockingReasons)+len(extra))
	for _, reason := range base.BlockingReasons {
		seen[reason] = struct{}{}
	}
	for _, item := range extra {
		if _, exists := seen[item.Description]; !exists {
			base.BlockingReasons = append(base.BlockingReasons, item.Description)
			seen[item.Description] = struct{}{}
		}
	}
	base.Complete = false
	return base
}
