package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// ManuscriptRevisionPage is a stable, metadata-only history page. Runtime
// candidates keep their content-addressed references, but prose bytes are
// loaded only by the explicit version endpoint.
type ManuscriptRevisionPage struct {
	Items      []domain.ManuscriptRevisionRuntime
	NextOffset int
	HasMore    bool
}

var (
	ErrManuscriptRevisionNotFound    = errors.New("manuscript revision not found")
	ErrManuscriptRevisionConflict    = errors.New("manuscript revision conflict")
	ErrManuscriptIdempotencyConflict = errors.New("manuscript idempotency conflict")
)

type manuscriptRuntimeIndex struct {
	ActiveRevisionID  string                                      `json:"active_revision_id,omitempty"`
	Revisions         map[string]domain.ManuscriptRevisionRuntime `json:"revisions"`
	Receipts          map[string]manuscriptRuntimeReceipt         `json:"receipts,omitempty"`
	ContentProvenance map[string]ManuscriptContentProvenance      `json:"content_provenance,omitempty"`
}

type ManuscriptContentProvenance struct {
	ChapterID             string              `json:"chapter_id"`
	ContentSHA256         string              `json:"content_sha256"`
	ApprovedOutlineSHA256 string              `json:"approved_outline_sha256"`
	Mode                  domain.RevisionMode `json:"mode"`
	AdaptationPlanSHA256  string              `json:"adaptation_plan_sha256,omitempty"`
	SourceManifestSHA256  string              `json:"source_manifest_sha256,omitempty"`
}

func manuscriptContentProvenanceKey(chapterID, contentSHA string) string {
	return strings.TrimSpace(chapterID) + ":" + strings.TrimSpace(contentSHA)
}

func (s *ManuscriptRevisionStore) BindContentProvenance(provenance ManuscriptContentProvenance) error {
	if strings.TrimSpace(provenance.ChapterID) == "" || len(strings.TrimSpace(provenance.ContentSHA256)) != 64 || len(strings.TrimSpace(provenance.ApprovedOutlineSHA256)) != 64 {
		return fmt.Errorf("manuscript content provenance is incomplete")
	}
	if provenance.Mode == domain.RevisionModeAdaptation {
		if len(provenance.AdaptationPlanSHA256) != 64 || len(provenance.SourceManifestSHA256) != 64 {
			return fmt.Errorf("adaptation content provenance is incomplete")
		}
	} else if provenance.AdaptationPlanSHA256 != "" || provenance.SourceManifestSHA256 != "" {
		return fmt.Errorf("normal content provenance must not contain adaptation bindings")
	}
	return s.revisions.withRevisionTransaction(func() error {
		index, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		key := manuscriptContentProvenanceKey(provenance.ChapterID, provenance.ContentSHA256)
		if previous, exists := index.ContentProvenance[key]; exists && previous != provenance {
			return fmt.Errorf("manuscript content provenance conflict")
		}
		index.ContentProvenance[key] = provenance
		return s.io.WriteJSON(manuscriptRuntimeIndexPath, index)
	})
}

func (s *ManuscriptRevisionStore) ContentProvenance(chapterID, contentSHA string) (*ManuscriptContentProvenance, error) {
	index, err := s.load()
	if err != nil {
		return nil, err
	}
	value, ok := index.ContentProvenance[manuscriptContentProvenanceKey(chapterID, contentSHA)]
	if !ok {
		return nil, ErrManuscriptRevisionNotFound
	}
	return &value, nil
}

type manuscriptRuntimeReceipt struct {
	Operation   string `json:"operation"`
	Fingerprint string `json:"fingerprint"`
	RevisionID  string `json:"revision_id"`
	Revision    int    `json:"revision"`
}

type ManuscriptRevisionStore struct {
	io        *IO
	revisions *RevisionStore
	content   *RevisionContentStore
}

func NewManuscriptRevisionStore(io *IO, revisions *RevisionStore) *ManuscriptRevisionStore {
	return &ManuscriptRevisionStore{io: io, revisions: revisions, content: NewRevisionContentStore(io)}
}

func (s *ManuscriptRevisionStore) Content() *RevisionContentStore { return s.content }

// SetWriteFaultForTesting installs a storage-layer fault at a real atomic-write
// stage. It exists so cross-package transaction tests can prove rollback after
// bytes have reached a temporary file or after the destination was backed up.
// Production code must not call it.
func (s *ManuscriptRevisionStore) SetWriteFaultForTesting(fault func(rel, stage string) error) func() {
	s.io.mu.Lock()
	s.io.writeFault = fault
	s.io.mu.Unlock()
	return func() {
		s.io.mu.Lock()
		s.io.writeFault = nil
		s.io.mu.Unlock()
	}
}

func (s *ManuscriptRevisionStore) Active() (*domain.ManuscriptRevisionRuntime, error) {
	index, err := s.load()
	if err != nil || index.ActiveRevisionID == "" {
		return nil, err
	}
	runtime, ok := index.Revisions[index.ActiveRevisionID]
	if !ok {
		return nil, fmt.Errorf("%w: active index points to %q", ErrManuscriptRevisionNotFound, index.ActiveRevisionID)
	}
	return cloneManuscriptRuntime(runtime), nil
}

func (s *ManuscriptRevisionStore) Load(revisionID string) (*domain.ManuscriptRevisionRuntime, error) {
	index, err := s.load()
	if err != nil {
		return nil, err
	}
	runtime, ok := index.Revisions[strings.TrimSpace(revisionID)]
	if !ok {
		return nil, ErrManuscriptRevisionNotFound
	}
	return cloneManuscriptRuntime(runtime), nil
}

func (s *ManuscriptRevisionStore) List(chapterID string, offset, limit int) (ManuscriptRevisionPage, error) {
	index, err := s.load()
	if err != nil {
		return ManuscriptRevisionPage{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	chapterID = strings.TrimSpace(chapterID)
	items := make([]domain.ManuscriptRevisionRuntime, 0, len(index.Revisions))
	for _, runtime := range index.Revisions {
		if chapterID != "" && !manuscriptRuntimeContainsChapter(runtime, chapterID) {
			continue
		}
		copy := *cloneManuscriptRuntime(runtime)
		copy.Candidates = nil
		copy.Batches = nil
		items = append(items, copy)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt == items[j].UpdatedAt {
			return items[i].RevisionID > items[j].RevisionID
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	if offset >= len(items) {
		return ManuscriptRevisionPage{Items: []domain.ManuscriptRevisionRuntime{}, NextOffset: offset}, nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return ManuscriptRevisionPage{Items: items[offset:end], NextOffset: end, HasMore: end < len(items)}, nil
}

// HasHistory reports whether any persisted revision references chapterID.
// It scans the metadata index only and never materializes candidate content.
func (s *ManuscriptRevisionStore) HasHistory(chapterID string) (bool, error) {
	chapterID = strings.TrimSpace(chapterID)
	if chapterID == "" {
		return false, nil
	}
	index, err := s.load()
	if err != nil {
		return false, err
	}
	for _, runtime := range index.Revisions {
		if manuscriptRuntimeContainsChapter(runtime, chapterID) {
			return true, nil
		}
	}
	return false, nil
}

func manuscriptRuntimeContainsChapter(runtime domain.ManuscriptRevisionRuntime, chapterID string) bool {
	if runtime.Baseline.ChapterID == chapterID {
		return true
	}
	for _, item := range runtime.Queue {
		if item.ChapterID == chapterID {
			return true
		}
	}
	return false
}

func (s *ManuscriptRevisionStore) Replay(idempotencyKey, operation string, input any) (*domain.ManuscriptRevisionRuntime, bool, error) {
	fingerprint, err := manuscriptFingerprint(operation, input)
	if err != nil {
		return nil, false, err
	}
	var result *domain.ManuscriptRevisionRuntime
	err = s.revisions.withRevisionTransaction(func() error {
		index, loadErr := s.loadUnlocked()
		if loadErr != nil {
			return loadErr
		}
		result, loadErr = replayManuscriptReceipt(index, idempotencyKey, operation, fingerprint)
		return loadErr
	})
	return result, result != nil, err
}

func (s *ManuscriptRevisionStore) Start(runtime domain.ManuscriptRevisionRuntime, idempotencyKey string) (*domain.ManuscriptRevisionRuntime, error) {
	return s.StartAtomic(runtime, idempotencyKey, nil)
}

// StartAtomic refreshes immutable manuscript evidence while the ownership
// transaction that installs the active revision is held.
func (s *ManuscriptRevisionStore) StartAtomic(runtime domain.ManuscriptRevisionRuntime, idempotencyKey string, refresh func(*domain.ManuscriptRevisionRuntime) error) (*domain.ManuscriptRevisionRuntime, error) {
	fingerprint, err := manuscriptFingerprint("start", struct {
		Mode            domain.RevisionMode
		PolicyID        string
		PolicyVersion   string
		Instruction     string
		InstructionKind domain.ManuscriptInstructionKind
		Baseline        domain.ManuscriptBaseline
		Queue           []domain.ManuscriptReworkItem
	}{runtime.Mode, runtime.PolicyID, runtime.PolicyVersion, runtime.Instruction, runtime.InstructionKind, runtime.Baseline, runtime.Queue})
	if err != nil {
		return nil, err
	}
	return s.mutate(idempotencyKey, "start", fingerprint, "", 0, func(index *manuscriptRuntimeIndex) (*domain.ManuscriptRevisionRuntime, error) {
		legacy, err := s.revisions.loadUnlocked()
		if err != nil {
			return nil, err
		}
		if legacy.ActiveSessionID != "" || legacy.NormalLease != nil || legacy.CommandFence != nil || legacy.Publication != nil {
			return nil, fmt.Errorf("%w: legacy revision or normal flow owns the project", ErrRevisionCommandInProgress)
		}
		if index.ActiveRevisionID != "" {
			return nil, fmt.Errorf("%w: %s", ErrRevisionCommandInProgress, index.ActiveRevisionID)
		}
		if refresh != nil {
			if err := refresh(&runtime); err != nil {
				return nil, err
			}
		}
		runtime.Revision = 1
		runtime.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := runtime.Validate(); err != nil {
			return nil, err
		}
		index.ActiveRevisionID = runtime.RevisionID
		index.Revisions[runtime.RevisionID] = runtime
		return &runtime, nil
	})
}

func activeManuscriptRevisionID(io *IO) (string, error) {
	var index manuscriptRuntimeIndex
	err := io.ReadJSON(manuscriptRuntimeIndexPath, &index)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(index.ActiveRevisionID), nil
}

func (s *ManuscriptRevisionStore) Mutate(revisionID string, expectedRevision int, idempotencyKey, operation string, input any, change func(*domain.ManuscriptRevisionRuntime) error) (*domain.ManuscriptRevisionRuntime, error) {
	fingerprint, err := manuscriptFingerprint(operation, input)
	if err != nil {
		return nil, err
	}
	return s.mutate(idempotencyKey, operation, fingerprint, revisionID, expectedRevision, func(index *manuscriptRuntimeIndex) (*domain.ManuscriptRevisionRuntime, error) {
		runtime, ok := index.Revisions[revisionID]
		if !ok {
			return nil, ErrManuscriptRevisionNotFound
		}
		if runtime.Revision != expectedRevision {
			return nil, fmt.Errorf("%w: expected %d actual %d", ErrManuscriptRevisionConflict, expectedRevision, runtime.Revision)
		}
		if err := change(&runtime); err != nil {
			return nil, err
		}
		runtime.Revision++
		runtime.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := runtime.Validate(); err != nil {
			return nil, err
		}
		index.Revisions[revisionID] = runtime
		if runtime.Stage == "completed" || runtime.Stage == "cancelled" {
			index.ActiveRevisionID = ""
		}
		return &runtime, nil
	})
}

func (s *ManuscriptRevisionStore) mutate(idempotencyKey, operation, fingerprint, revisionID string, expectedRevision int, change func(*manuscriptRuntimeIndex) (*domain.ManuscriptRevisionRuntime, error)) (*domain.ManuscriptRevisionRuntime, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("idempotency_key is required")
	}
	var result *domain.ManuscriptRevisionRuntime
	err := s.revisions.withRevisionTransaction(func() error {
		index, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if receipt, ok := index.Receipts[idempotencyKey]; ok {
			if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
				return ErrManuscriptIdempotencyConflict
			}
			runtime, ok := index.Revisions[receipt.RevisionID]
			if !ok || runtime.Revision < receipt.Revision {
				return ErrManuscriptRevisionNotFound
			}
			result = cloneManuscriptRuntime(runtime)
			return nil
		}
		result, err = change(index)
		if err != nil {
			return err
		}
		index.Receipts[idempotencyKey] = manuscriptRuntimeReceipt{Operation: operation, Fingerprint: fingerprint, RevisionID: result.RevisionID, Revision: result.Revision}
		return s.io.WriteJSON(manuscriptRuntimeIndexPath, index)
	})
	return result, err
}

const manuscriptRuntimeIndexPath = "meta/revisions/manuscript/index.json"

func (s *ManuscriptRevisionStore) load() (*manuscriptRuntimeIndex, error) {
	return s.loadUnlocked()
}

func (s *ManuscriptRevisionStore) loadUnlocked() (*manuscriptRuntimeIndex, error) {
	var index manuscriptRuntimeIndex
	err := s.io.ReadJSON(manuscriptRuntimeIndexPath, &index)
	if os.IsNotExist(err) {
		return &manuscriptRuntimeIndex{Revisions: make(map[string]domain.ManuscriptRevisionRuntime), Receipts: make(map[string]manuscriptRuntimeReceipt), ContentProvenance: make(map[string]ManuscriptContentProvenance)}, nil
	}
	if err != nil {
		return nil, err
	}
	if index.Revisions == nil {
		index.Revisions = make(map[string]domain.ManuscriptRevisionRuntime)
	}
	if index.Receipts == nil {
		index.Receipts = make(map[string]manuscriptRuntimeReceipt)
	}
	if index.ContentProvenance == nil {
		index.ContentProvenance = make(map[string]ManuscriptContentProvenance)
	}
	return &index, nil
}

func manuscriptFingerprint(operation string, input any) (string, error) {
	payload, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Input     any    `json:"input"`
	}{operation, input})
	if err != nil {
		return "", err
	}
	return domain.ContentSignature(payload), nil
}

func cloneManuscriptRuntime(runtime domain.ManuscriptRevisionRuntime) *domain.ManuscriptRevisionRuntime {
	payload, _ := json.Marshal(runtime)
	var clone domain.ManuscriptRevisionRuntime
	_ = json.Unmarshal(payload, &clone)
	return &clone
}

func manuscriptRuntimePath(revisionID string) string {
	return filepath.ToSlash(filepath.Join("meta", "revisions", "manuscript", revisionID+".json"))
}
