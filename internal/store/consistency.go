package store

import (
	"errors"
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// ConsistencyAudit persists the actionable result for one exact draft version.
// Failed audits are durable so a transient model error after the tool result
// does not force another expensive independent review of unchanged prose.
type ConsistencyAudit struct {
	Chapter     int                       `json:"chapter"`
	DraftSHA256 string                    `json:"draft_sha256"`
	Passed      bool                      `json:"passed"`
	Findings    []domain.ConsistencyIssue `json:"findings,omitempty"`
}

type ConsistencyStore struct{ io *IO }

func NewConsistencyStore(io *IO) *ConsistencyStore { return &ConsistencyStore{io: io} }

func (s *ConsistencyStore) AuditPath(chapter int) string {
	return fmt.Sprintf("meta/consistency/checks/%03d.json", chapter)
}

func (s *ConsistencyStore) SaveAudit(audit ConsistencyAudit) error {
	if audit.Chapter <= 0 {
		return fmt.Errorf("invalid consistency audit chapter %d", audit.Chapter)
	}
	return s.io.WriteJSON(s.AuditPath(audit.Chapter), audit)
}

func (s *ConsistencyStore) LoadAudit(chapter int) (*ConsistencyAudit, error) {
	if chapter <= 0 {
		return nil, fmt.Errorf("invalid consistency audit chapter %d", chapter)
	}
	var audit ConsistencyAudit
	if err := s.io.ReadJSON(s.AuditPath(chapter), &audit); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return &audit, nil
}
