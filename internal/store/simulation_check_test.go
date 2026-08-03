package store

import (
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/simulationcheck"
)

func TestSimulationCheckStorePersistsAndRejectsStaleRevision(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	report := simulationCheckStoreReport(t, 1)
	if err := st.SimulationChecks.SaveCAS(report, 0); err != nil {
		t.Fatalf("SaveCAS: %v", err)
	}
	loaded, err := st.SimulationChecks.Load(1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil || loaded.ReportDigest != report.ReportDigest {
		t.Fatalf("loaded report = %+v", loaded)
	}
	next := simulationCheckStoreReport(t, 2)
	if err := st.SimulationChecks.SaveCAS(next, 0); err == nil {
		t.Fatal("stale report revision overwrote current report")
	}
}

func simulationCheckStoreReport(t *testing.T, revision int64) simulationcheck.Report {
	t.Helper()
	digest := strings.Repeat("a", 64)
	report, err := simulationcheck.Finalize(simulationcheck.Report{
		Version: simulationcheck.ReportVersion, Revision: revision,
		ProjectDigest: digest, Chapter: 1, DraftDigest: digest,
		EffectiveMode: "normal", CheckerVersion: simulationcheck.CheckerVersion,
		CheckerDigest: simulationcheck.ConfigurationDigest(),
		CheckedAt:     time.Now().UTC().Format(time.RFC3339),
		Capability: simulationcheck.Capability{
			State: simulationcheck.CapabilityPartial, ContractChecks: true,
		},
		CopyStatus:     simulationcheck.StatusUnverified,
		ContractStatus: simulationcheck.StatusPass, Passed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return report
}
