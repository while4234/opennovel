package store

import (
	"errors"
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/deai"
)

const deAIPolicyPath = "meta/deai/policy.json"

// DeAIPolicy is intentionally project-local. Enabling it on Host startup makes
// newly created and resumed books use the same post-writing quality stage,
// without retroactively invalidating already committed chapters.
type DeAIPolicy struct {
	Version int  `json:"version"`
	Enabled bool `json:"enabled"`
}

// DeAIStore persists the exact audit that belongs to a draft version.
type DeAIStore struct{ io *IO }

func NewDeAIStore(io *IO) *DeAIStore { return &DeAIStore{io: io} }

func (s *DeAIStore) Enable() error {
	if s == nil {
		return nil
	}
	policy, err := s.LoadPolicy()
	if err != nil {
		return err
	}
	if policy.Enabled && policy.Version >= deai.PolicyVersion {
		return nil
	}
	return s.io.WriteJSON(deAIPolicyPath, DeAIPolicy{Version: deai.PolicyVersion, Enabled: true})
}

func (s *DeAIStore) LoadPolicy() (*DeAIPolicy, error) {
	if s == nil {
		return nil, nil
	}
	var policy DeAIPolicy
	if err := s.io.ReadJSON(deAIPolicyPath, &policy); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DeAIPolicy{}, nil
		}
		return nil, err
	}
	return &policy, nil
}

func (s *DeAIStore) Enabled() (bool, error) {
	policy, err := s.LoadPolicy()
	if err != nil || policy == nil {
		return false, err
	}
	return policy.Enabled, nil
}

func (s *DeAIStore) AuditPath(chapter int) string {
	return fmt.Sprintf("meta/deai/checks/%03d.json", chapter)
}

func (s *DeAIStore) SaveAudit(audit deai.Audit) error {
	if audit.Chapter <= 0 {
		return fmt.Errorf("invalid de-AI audit chapter %d", audit.Chapter)
	}
	return s.io.WriteJSON(s.AuditPath(audit.Chapter), audit)
}

func (s *DeAIStore) LoadAudit(chapter int) (*deai.Audit, error) {
	if chapter <= 0 {
		return nil, fmt.Errorf("invalid de-AI audit chapter %d", chapter)
	}
	var audit deai.Audit
	if err := s.io.ReadJSON(s.AuditPath(chapter), &audit); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return &audit, nil
}
