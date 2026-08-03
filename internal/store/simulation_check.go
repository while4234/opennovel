package store

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/simulationcheck"
)

type SimulationCheckStore struct{ io *IO }

func NewSimulationCheckStore(io *IO) *SimulationCheckStore {
	return &SimulationCheckStore{io: io}
}

func (s *SimulationCheckStore) Path(chapter int) string {
	return fmt.Sprintf("meta/simulation_checks/%03d.json", chapter)
}

func (s *SimulationCheckStore) Load(chapter int) (*simulationcheck.Report, error) {
	if chapter <= 0 {
		return nil, fmt.Errorf("invalid simulation check chapter %d", chapter)
	}
	var report simulationcheck.Report
	if err := s.io.ReadJSON(s.Path(chapter), &report); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := simulationcheck.Validate(&report); err != nil {
		return nil, err
	}
	return &report, nil
}

// SaveCAS atomically advances a chapter's report revision. Concurrent Writer
// and Editor checks cannot silently overwrite one another.
func (s *SimulationCheckStore) SaveCAS(report simulationcheck.Report, expectedRevision int64) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()

	var current simulationcheck.Report
	actualRevision := int64(0)
	if err := s.io.ReadJSONUnlocked(s.Path(report.Chapter), &current); err == nil {
		if err := simulationcheck.Validate(&current); err != nil {
			return err
		}
		actualRevision = current.Revision
	} else if !os.IsNotExist(err) {
		return err
	}
	if actualRevision != expectedRevision {
		return fmt.Errorf("simulation check revision conflict: expected %d, actual %d", expectedRevision, actualRevision)
	}
	if report.Revision != expectedRevision+1 {
		return fmt.Errorf("simulation check revision must advance by one")
	}
	if err := simulationcheck.Validate(&report); err != nil {
		return err
	}
	return s.io.WriteJSONUnlocked(s.Path(report.Chapter), report)
}
