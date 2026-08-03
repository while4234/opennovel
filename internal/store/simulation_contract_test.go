package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestEnsureSimulationContractPersistsAndRefreshesBindings(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	saveStoreSimulationProfile(t, st, "feature-a")
	first, _, err := st.EnsureSimulationContract(domain.SimulationModeNormal)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := st.SimulationContracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.ContractDigest != first.ContractDigest || loaded.Revision != 1 {
		t.Fatalf("persisted contract=%+v, want revision 1 digest %s", loaded, first.ContractDigest)
	}

	same, _, err := st.EnsureSimulationContract(domain.SimulationModeNormal)
	if err != nil {
		t.Fatal(err)
	}
	if same.Revision != first.Revision || same.ContractDigest != first.ContractDigest {
		t.Fatal("unchanged bindings should reuse the current contract")
	}

	reinforced, _, err := st.EnsureSimulationContract(domain.SimulationModeReinforced)
	if err != nil {
		t.Fatal(err)
	}
	if reinforced.Revision != 2 || reinforced.RequestedMode != domain.SimulationModeReinforced {
		t.Fatalf("mode refresh=%+v, want revision 2 reinforced", reinforced)
	}

	saveStoreSimulationProfile(t, st, "feature-b")
	refreshed, _, err := st.EnsureSimulationContract(domain.SimulationModeReinforced)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Revision != 3 || refreshed.ProfileDigest == reinforced.ProfileDigest {
		t.Fatal("profile digest change must refresh the persisted contract")
	}
}

func TestSimulationContractStoreRejectsStaleRevision(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	contract, err := domain.CompileSimulationContract(domain.SimulationContractInput{
		RequestedMode: domain.SimulationModeNormal, Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SimulationContracts.SaveCAS(contract, 0); err != nil {
		t.Fatal(err)
	}
	next, err := domain.CompileSimulationContract(domain.SimulationContractInput{
		RequestedMode: domain.SimulationModeReinforced, PreviousRevision: 1, Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SimulationContracts.SaveCAS(next, 0); err == nil {
		t.Fatal("stale expected revision should be rejected")
	}
}

func TestEnsureSimulationContractReplacesPreviousContractVersion(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	saveStoreSimulationProfile(t, st, "feature-a")
	previous, err := domain.CompileSimulationContract(domain.SimulationContractInput{
		RequestedMode: domain.SimulationModeNormal, Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	previous.Version = "simulation_contract.v1"
	previous.ContractDigest, err = domain.SimulationContractDigest(previous)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(previous, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filepath.FromSlash(simulationContractPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	current, _, err := st.EnsureSimulationContract(domain.SimulationModeReinforced)
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.Version != domain.SimulationContractVersion ||
		current.Revision != 1 || current.RequestedMode != domain.SimulationModeReinforced {
		t.Fatalf("previous contract was not replaced: %+v", current)
	}
}

func saveStoreSimulationProfile(t *testing.T, st *Store, featureID string) {
	t.Helper()
	confidence, coverage := 0.95, 0.8
	profile := domain.SimulationProfileV2{
		Version:   domain.SimulationPortableProfileVersion,
		CreatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		UpdatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		Corpus: domain.SimulationPortableCorpus{
			Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceCount: 4,
		},
		Analysis: domain.SimulationAnalysisMetadata{
			SourceAnalysisSignature: "source", SchemaSignature: "schema", AggregationSignature: "aggregate",
		},
		Features: []domain.SimulationFeature{{
			ID: featureID, Dimension: "plot_design.opening_patterns", Statement: "open with a decision",
			Classification: "stable", SupportCount: 4, Confidence: &confidence, Coverage: &coverage, Safety: "guidance",
		}},
		Capabilities: domain.SimulationCapabilities{Portable: true, LocalEvidence: true, AnalysisSigned: true, SafetyIndex: true},
		Health:       domain.SimulationProfileHealth{State: "fresh"},
	}
	if err := domain.SetSimulationProfileDigest(&profile); err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.SavePortable(profile); err != nil {
		t.Fatal(err)
	}
}
