package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const simulationContractPath = "meta/simulation_contract.json"

type SimulationContractStore struct{ io *IO }

func NewSimulationContractStore(io *IO) *SimulationContractStore {
	return &SimulationContractStore{io: io}
}

// EnsureSimulationContract deterministically refreshes the derived contract
// whenever its portable profile, requested mode, Foundation, or creative brief
// binding changes.
func (s *Store) EnsureSimulationContract(mode string) (*domain.SimulationContract, *domain.SimulationProfileV2, error) {
	if s == nil || s.Simulation == nil || s.SimulationContracts == nil {
		return nil, nil, nil
	}
	profile, err := s.Simulation.LoadPortable()
	if err != nil {
		return nil, nil, err
	}
	foundation, err := s.Foundation.Load()
	if err != nil {
		return nil, profile, err
	}
	foundationDigest, err := domain.FoundationContentSignature(foundation)
	if err != nil {
		return nil, profile, err
	}
	briefDigest, err := s.simulationBriefDigest()
	if err != nil {
		return nil, profile, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		current, loadErr := s.SimulationContracts.Load()
		if loadErr != nil {
			return nil, profile, loadErr
		}
		if ok, _ := domain.SimulationContractCurrent(
			current, profile, mode, foundation.Revision, foundationDigest, briefDigest,
		); ok {
			return current, profile, nil
		}
		previousRevision := int64(0)
		if current != nil {
			previousRevision = current.Revision
		}
		compiled, compileErr := domain.CompileSimulationContract(domain.SimulationContractInput{
			Profile: profile, RequestedMode: mode,
			FoundationRevision: foundation.Revision, FoundationDigest: foundationDigest,
			CreativeBriefDigest: briefDigest, PreviousRevision: previousRevision,
		})
		if compileErr != nil {
			return nil, profile, compileErr
		}
		if saveErr := s.SimulationContracts.SaveCAS(compiled, previousRevision); saveErr == nil {
			return &compiled, profile, nil
		} else if attempt == 2 {
			return nil, profile, saveErr
		}
	}
	return nil, profile, fmt.Errorf("simulation contract synchronization failed")
}

func (s *Store) simulationBriefDigest() (string, error) {
	snapshot, err := s.UserRules.Load()
	if err != nil {
		return "", err
	}
	if snapshot == nil {
		return "", nil
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s *SimulationContractStore) Load() (*domain.SimulationContract, error) {
	var contract domain.SimulationContract
	if err := s.io.ReadJSON(simulationContractPath, &contract); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := domain.ValidateSimulationContract(&contract); err != nil {
		if contract.Version != domain.SimulationContractVersion {
			return nil, nil
		}
		return nil, err
	}
	return &contract, nil
}

// SaveCAS atomically advances the contract revision and rejects a stale
// compiler. The portable profile remains independently immutable.
func (s *SimulationContractStore) SaveCAS(contract domain.SimulationContract, expectedRevision int64) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()

	var current domain.SimulationContract
	err := s.io.ReadJSONUnlocked(simulationContractPath, &current)
	actualRevision := int64(0)
	if err == nil {
		if validateErr := domain.ValidateSimulationContract(&current); validateErr != nil {
			if current.Version == domain.SimulationContractVersion {
				return validateErr
			}
		} else {
			actualRevision = current.Revision
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if actualRevision != expectedRevision {
		return fmt.Errorf("simulation contract revision conflict: expected %d, actual %d", expectedRevision, actualRevision)
	}
	if contract.Revision != expectedRevision+1 {
		return fmt.Errorf("simulation contract revision must advance by one")
	}
	if err := domain.ValidateSimulationContract(&contract); err != nil {
		return err
	}
	return s.io.WriteJSONUnlocked(simulationContractPath, contract)
}
