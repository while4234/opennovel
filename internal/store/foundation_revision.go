package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	foundationDependencyFile      = "meta/foundation/dependencies.json"
	foundationRevisionRuntimeFile = "meta/foundation/revisions/runtime.json"
	foundationRevisionReceiptFile = "meta/foundation/revisions/receipts.json"
)

var foundationRevisionLocks sync.Map

type FoundationRevisionStore struct {
	io *IO
	mu *sync.Mutex
}

type foundationApplyReceipt struct {
	Fingerprint string                           `json:"fingerprint"`
	Runtime     domain.FoundationRevisionRuntime `json:"runtime"`
}

func NewFoundationRevisionStore(io *IO) *FoundationRevisionStore {
	key, err := filepath.Abs(io.dir)
	if err != nil {
		key = io.dir
	}
	value, _ := foundationRevisionLocks.LoadOrStore(strings.ToLower(filepath.Clean(key)), &sync.Mutex{})
	return &FoundationRevisionStore{io: io, mu: value.(*sync.Mutex)}
}

func (s *FoundationRevisionStore) SavePreview(preview domain.FoundationRevisionPreview) error {
	if err := preview.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := foundationPreviewPath(preview.ID)
	var current domain.FoundationRevisionPreview
	if err := s.io.ReadJSON(path, &current); err == nil {
		if err := current.Validate(); err != nil {
			return fmt.Errorf("persisted foundation preview is invalid: %w", err)
		}
		if current.Signature != preview.Signature {
			return fmt.Errorf("foundation preview %q already exists with different content", preview.ID)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.io.WriteJSON(path, preview)
}

func (s *FoundationRevisionStore) LoadPreview(id string) (*domain.FoundationRevisionPreview, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("foundation preview id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var preview domain.FoundationRevisionPreview
	if err := s.io.ReadJSON(foundationPreviewPath(id), &preview); err != nil {
		return nil, err
	}
	if err := preview.Validate(); err != nil {
		return nil, fmt.Errorf("persisted foundation preview was modified: %w", err)
	}
	return &preview, nil
}

func (s *FoundationRevisionStore) SaveRuntime(runtime domain.FoundationRevisionRuntime) error {
	if runtime.Version != domain.FoundationRevisionSchemaVersion || strings.TrimSpace(runtime.RevisionID) == "" || strings.TrimSpace(runtime.SessionID) == "" || strings.TrimSpace(runtime.PreviewID) == "" || runtime.Generation == 0 || runtime.Attempt <= 0 || strings.TrimSpace(runtime.Stage) == "" {
		return fmt.Errorf("foundation revision runtime is incomplete")
	}
	if err := runtime.Impact.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.io.WriteJSON(foundationRevisionRuntimeFile, runtime)
}

func (s *FoundationRevisionStore) LoadRuntime() (*domain.FoundationRevisionRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var runtime domain.FoundationRevisionRuntime
	if err := s.io.ReadJSON(foundationRevisionRuntimeFile, &runtime); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if runtime.Version != domain.FoundationRevisionSchemaVersion || strings.TrimSpace(runtime.RevisionID) == "" || strings.TrimSpace(runtime.SessionID) == "" {
		return nil, fmt.Errorf("persisted foundation revision runtime is invalid")
	}
	return &runtime, nil
}

func (s *FoundationRevisionStore) LoadReceipt(key, fingerprint string) (*domain.FoundationRevisionRuntime, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipts, err := s.loadReceiptsUnlocked()
	if err != nil {
		return nil, false, err
	}
	receipt, ok := receipts[foundationReceiptKey(key)]
	if !ok {
		return nil, false, nil
	}
	if receipt.Fingerprint != fingerprint {
		return nil, false, ErrRevisionIdempotencyConflict
	}
	copy := receipt.Runtime
	return &copy, true, nil
}

func (s *FoundationRevisionStore) SaveReceipt(key, fingerprint string, runtime domain.FoundationRevisionRuntime) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipts, err := s.loadReceiptsUnlocked()
	if err != nil {
		return err
	}
	id := foundationReceiptKey(key)
	if existing, ok := receipts[id]; ok && existing.Fingerprint != fingerprint {
		return ErrRevisionIdempotencyConflict
	}
	receipts[id] = foundationApplyReceipt{Fingerprint: fingerprint, Runtime: runtime}
	return s.io.WriteJSON(foundationRevisionReceiptFile, receipts)
}

func (s *FoundationRevisionStore) LoadDependencies() (*domain.FoundationDependencyManifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var manifest domain.FoundationDependencyManifest
	if err := s.io.ReadJSON(foundationDependencyFile, &manifest); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("foundation dependency evidence is invalid: %w", err)
	}
	return &manifest, nil
}

func (s *FoundationRevisionStore) SaveDependencies(manifest domain.FoundationDependencyManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.io.WriteJSON(foundationDependencyFile, manifest)
}

func (s *Store) ValidateFoundationDependencies(manifest domain.FoundationDependencyManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return err
	}
	audits, err := s.OriginalPlanningAudits.Load()
	if err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		var value any
		switch entry.DependentArtifactType {
		case "chapter":
			for _, chapter := range domain.FlattenOutline(volumes) {
				if chapter.ID == entry.DependentArtifactID {
					value = chapter
					break
				}
			}
		case "arc":
			for _, volume := range volumes {
				for _, arc := range volume.Arcs {
					if arc.ID == entry.DependentArtifactID {
						value = arc
						break
					}
				}
			}
		case "volume":
			for _, volume := range volumes {
				if volume.ID == entry.DependentArtifactID {
					value = volume
					break
				}
			}
		case "planning_audit":
			for _, audit := range audits {
				if audit.ScopeID == entry.DependentArtifactID {
					value = audit
					break
				}
			}
		default:
			return fmt.Errorf("foundation dependency %q has unsupported dependent artifact type %q", entry.ID, entry.DependentArtifactType)
		}
		if value == nil {
			return fmt.Errorf("foundation dependency %q endpoint is missing", entry.ID)
		}
		payload, _ := json.Marshal(value)
		if domain.JSONContentSignature(payload) != entry.DependentContentSignature {
			return fmt.Errorf("foundation dependency %q dependent content changed", entry.ID)
		}
	}
	return nil
}

func (s *FoundationRevisionStore) loadReceiptsUnlocked() (map[string]foundationApplyReceipt, error) {
	receipts := make(map[string]foundationApplyReceipt)
	if err := s.io.ReadJSON(foundationRevisionReceiptFile, &receipts); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return receipts, nil
}

func foundationPreviewPath(id string) string {
	return filepath.ToSlash(filepath.Join("meta", "foundation", "revisions", "previews", strings.TrimSpace(id)+".json"))
}

func foundationReceiptKey(key string) string {
	return domain.ContentSignature([]byte(strings.TrimSpace(key)))
}

func FoundationRevisionFingerprint(value any) string {
	payload, _ := json.Marshal(value)
	return domain.ContentSignature(payload)
}

func IsFoundationPreviewMissing(err error) bool { return errors.Is(err, os.ErrNotExist) }
