package store

import (
	"errors"
	"fmt"
	"strings"
)

// ManuscriptRecoveryStatus is metadata-only and safe to expose to Web reads.
// It never includes manuscript content, local paths, or journal payloads.
type ManuscriptRecoveryStatus struct {
	Required  bool     `json:"required"`
	Class     string   `json:"class,omitempty"`
	Owners    []string `json:"owners,omitempty"`
	Message   string   `json:"message,omitempty"`
	Retryable bool     `json:"retryable"`
}

// ManuscriptRecoveryStatus reports durable owners in the same outer-to-inner
// order used by recovery. Reads may continue while Required is true; every
// write boundary must call RequireManuscriptWriteReady first.
func (s *Store) ManuscriptRecoveryState() ManuscriptRecoveryStatus {
	if s == nil {
		return ManuscriptRecoveryStatus{Required: true, Class: "publication_recovery_required", Message: "store is unavailable"}
	}
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	owners := make([]string, 0, 4)
	if s.commandRecoveryErr != nil {
		owners = append(owners, "adaptation_command")
	}
	if s.publicationRecoveryErr != nil {
		owners = append(owners, "revision_publication")
	}
	if s.manuscriptPublicationRecoveryErr != nil {
		owners = append(owners, "manuscript_publication")
	}
	if s.publicationAuthorityRecoveryErr != nil {
		owners = append(owners, "publication_authority")
	}
	if s.structureMigrationRecoveryErr != nil {
		owners = append(owners, "structure_migration")
	}
	if len(owners) == 0 {
		return ManuscriptRecoveryStatus{}
	}
	return ManuscriptRecoveryStatus{
		Required:  true,
		Class:     "publication_recovery_required",
		Owners:    owners,
		Message:   "durable manuscript recovery must complete before writes resume",
		Retryable: true,
	}
}

// RequireManuscriptWriteReady retries the canonical non-reentrant recovery
// sequence and returns the shared API error class on failure. The underlying
// cause is retained for logs while callers can safely map the class.
func (s *Store) RequireManuscriptWriteReady() error {
	if s == nil {
		return fmt.Errorf("publication_recovery_required: store is unavailable")
	}
	if err := s.RecoverStructureMigration(); err != nil {
		return fmt.Errorf("publication_recovery_required: %w", err)
	}
	status := s.ManuscriptRecoveryState()
	if status.Required {
		return fmt.Errorf("publication_recovery_required: %s", strings.Join(status.Owners, ","))
	}
	return nil
}

// IsPublicationRecoveryRequired lets HTTP/Host boundaries preserve one error
// envelope without matching implementation-specific journal text.
func IsPublicationRecoveryRequired(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "publication_recovery_required") || errors.Is(err, ErrPublicationRecoveryRequired))
}

var ErrPublicationRecoveryRequired = errors.New("publication recovery required")
