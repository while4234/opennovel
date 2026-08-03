package store

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type SimulationStore struct{ io *IO }

func NewSimulationStore(io *IO) *SimulationStore { return &SimulationStore{io: io} }

const (
	simulationProfilePath         = "meta/simulation_profile.json"
	simulationEvidencePath        = "meta/simulation_evidence.local.json"
	simulationMergeCheckpointPath = "meta/simulation_merge_checkpoint.json"
)

func (s *SimulationStore) Load() (*domain.SimulationProfile, error) {
	data, err := s.io.ReadFile(simulationProfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var header struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	if header.Version == domain.SimulationProfileVersion {
		var profile domain.SimulationProfile
		if err := json.Unmarshal(data, &profile); err != nil {
			return nil, err
		}
		if err := domain.ValidateSimulationProfile(&profile); err != nil {
			return nil, err
		}
		return &profile, nil
	}
	portable, err := domain.UnmarshalSimulationPortableProfile(data)
	if err != nil {
		return nil, err
	}
	var evidence *domain.SimulationLocalEvidence
	evidenceData, evidenceErr := s.io.ReadFile(simulationEvidencePath)
	if evidenceErr == nil {
		loaded, err := domain.UnmarshalSimulationLocalEvidence(evidenceData)
		if err != nil {
			return nil, err
		}
		evidence = &loaded
	} else if !os.IsNotExist(evidenceErr) {
		return nil, evidenceErr
	}
	profile, err := domain.SimulationProfileV2CompatibilityProfile(portable, evidence)
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *SimulationStore) Save(profile domain.SimulationProfile) error {
	if profile.Version == "" {
		profile.Version = domain.SimulationProfileVersion
	}
	if err := domain.ValidateSimulationProfile(&profile); err != nil {
		return err
	}
	portable, evidence, err := domain.ProjectSimulationProfileV1(profile)
	if err != nil {
		return err
	}
	portableData, err := domain.MarshalSimulationPortableProfile(portable)
	if err != nil {
		return err
	}
	evidenceData, err := domain.MarshalSimulationLocalEvidence(evidence)
	if err != nil {
		return err
	}
	// Evidence is installed first and bound to the future portable digest. If
	// the second atomic replacement fails, the previous portable profile will
	// ignore the mismatched evidence rather than expose a torn migration.
	if err := s.io.WriteFile(simulationEvidencePath, evidenceData); err != nil {
		return err
	}
	return s.io.WriteFile(simulationProfilePath, portableData)
}

func (s *SimulationStore) LoadPortable() (*domain.SimulationProfileV2, error) {
	data, err := s.io.ReadFile(simulationProfilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var header struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	if header.Version == domain.SimulationProfileVersion {
		portable, _, err := projectLegacySimulationProfile(data)
		if err != nil {
			return nil, err
		}
		return &portable, nil
	}
	portable, err := domain.UnmarshalSimulationPortableProfile(data)
	if err != nil {
		return nil, err
	}
	return &portable, nil
}

func (s *SimulationStore) SavePortable(profile domain.SimulationProfileV2) error {
	data, err := domain.MarshalSimulationPortableProfile(profile)
	if err != nil {
		return err
	}
	return s.io.WriteFile(simulationProfilePath, data)
}

func (s *SimulationStore) LoadLocalEvidence() (*domain.SimulationLocalEvidence, error) {
	data, err := s.io.ReadFile(simulationEvidencePath)
	if err != nil {
		if os.IsNotExist(err) {
			legacyData, legacyErr := s.io.ReadFile(simulationProfilePath)
			if os.IsNotExist(legacyErr) {
				return nil, nil
			}
			if legacyErr != nil {
				return nil, legacyErr
			}
			var header struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(legacyData, &header) != nil || header.Version != domain.SimulationProfileVersion {
				return nil, nil
			}
			_, evidence, projectErr := projectLegacySimulationProfile(legacyData)
			if projectErr != nil {
				return nil, projectErr
			}
			return &evidence, nil
		}
		return nil, err
	}
	evidence, err := domain.UnmarshalSimulationLocalEvidence(data)
	if err != nil {
		return nil, err
	}
	return &evidence, nil
}

func projectLegacySimulationProfile(data []byte) (
	domain.SimulationProfileV2,
	domain.SimulationLocalEvidence,
	error,
) {
	var legacy domain.SimulationProfile
	if err := json.Unmarshal(data, &legacy); err != nil {
		return domain.SimulationProfileV2{}, domain.SimulationLocalEvidence{}, err
	}
	if err := domain.ValidateSimulationProfile(&legacy); err != nil {
		return domain.SimulationProfileV2{}, domain.SimulationLocalEvidence{}, err
	}
	return domain.ProjectSimulationProfileV1(legacy)
}

// SaveAnalysis installs the bound local evidence before the portable profile.
// A torn second write is fail-closed because Load ignores evidence whose digest
// does not match the still-current portable artifact.
func (s *SimulationStore) SaveAnalysis(profile domain.SimulationProfileV2, evidence domain.SimulationLocalEvidence) error {
	if evidence.ProfileDigest != profile.ProfileDigest {
		return fmt.Errorf("simulation evidence/profile digest mismatch")
	}
	portableData, err := domain.MarshalSimulationPortableProfile(profile)
	if err != nil {
		return err
	}
	evidenceData, err := domain.MarshalSimulationLocalEvidence(evidence)
	if err != nil {
		return err
	}
	if err := s.io.WriteFile(simulationEvidencePath, evidenceData); err != nil {
		return err
	}
	return s.io.WriteFile(simulationProfilePath, portableData)
}

func (s *SimulationStore) Clear() error {
	for _, path := range []string{simulationMergeCheckpointPath, simulationContractPath, simulationEvidencePath, simulationProfilePath} {
		if err := s.io.RemoveFile(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *SimulationStore) LoadMergeCheckpoint() (*domain.SimulationMergeCheckpoint, error) {
	var checkpoint domain.SimulationMergeCheckpoint
	if err := s.io.ReadJSON(simulationMergeCheckpointPath, &checkpoint); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := domain.ValidateSimulationMergeCheckpoint(&checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func (s *SimulationStore) SaveMergeCheckpoint(checkpoint domain.SimulationMergeCheckpoint) error {
	if checkpoint.Version == "" {
		checkpoint.Version = domain.SimulationMergeCheckpointVersion
	}
	if err := domain.ValidateSimulationMergeCheckpoint(&checkpoint); err != nil {
		return err
	}
	return s.io.WriteJSON(simulationMergeCheckpointPath, checkpoint)
}

func (s *SimulationStore) ClearMergeCheckpoint() error {
	return s.io.RemoveFile(simulationMergeCheckpointPath)
}
