package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSimulationSummaryEnsuresReinforcedContractBeforeCoCreate(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)

	before, err := st.SimulationContracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if before != nil {
		t.Fatalf("contract unexpectedly exists before summary: %+v", before)
	}

	summary := buildSimulationProfileSummary(st, bootstrap.SimulationModeReinforced, 0)

	if summary == nil {
		t.Fatal("simulation summary is nil")
	}
	if summary.EffectiveMode != domain.SimulationModeReinforced {
		t.Fatalf("effective mode = %q, want reinforced", summary.EffectiveMode)
	}
	if summary.Contract == nil || !summary.Contract.Current {
		t.Fatalf("current contract missing from summary: %+v", summary.Contract)
	}
	if summary.Contract.Status != domain.SimulationContractActive {
		t.Fatalf("contract status = %q, want active", summary.Contract.Status)
	}

	persisted, err := st.SimulationContracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.RequestedMode != domain.SimulationModeReinforced {
		t.Fatalf("reinforced contract was not persisted: %+v", persisted)
	}
}

func TestSimulationActionsAllowReanalysisAfterPortableCorpusRestore(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)
	profile, err := st.Simulation.LoadPortable()
	if err != nil || profile == nil {
		t.Fatalf("load portable profile: profile=%v err=%v", profile, err)
	}
	simulateDir := filepath.Join(filepath.Dir(st.Dir()), "simulate")
	if err := os.MkdirAll(simulateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(simulateDir, "restored.txt"), []byte("synthetic corpus"), 0o644); err != nil {
		t.Fatal(err)
	}

	actions := simulationActions(st, profile, nil)
	if !actions.Rescan.Enabled || !actions.Reanalyze.Enabled {
		t.Fatalf("restored local corpus should enable analysis: %+v", actions)
	}
	if actions.Resynthesize.Enabled {
		t.Fatalf("missing local reports should keep resynthesis disabled: %+v", actions)
	}
}

func TestSimulationActionsExplainMissingPortableCorpus(t *testing.T) {
	st := newSimulationPromptTestStore(t, true)
	profile, err := st.Simulation.LoadPortable()
	if err != nil || profile == nil {
		t.Fatalf("load portable profile: profile=%v err=%v", profile, err)
	}

	actions := simulationActions(st, profile, nil)
	if actions.Rescan.Enabled || actions.Reanalyze.Enabled {
		t.Fatalf("missing corpus unexpectedly enabled analysis: %+v", actions)
	}
	if !strings.Contains(actions.Rescan.Reason, "重新上传原语料") {
		t.Fatalf("missing corpus reason is not actionable: %+v", actions)
	}
}
