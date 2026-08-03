package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSimulationSaveWritesPortableProfileAndLocalEvidence(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	fingerprint := domain.SimulationSourceFingerprint("part.txt", strings.Repeat("a", 64))
	profile := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-02T00:00:00Z",
		Corpus: domain.SimulationCorpusManifest{
			SourceDir: `C:\private\corpus`,
			Sources: []domain.SimulationSource{{
				RelativePath: "part.txt",
				SHA256:       strings.Repeat("a", 64),
				Fingerprint:  fingerprint,
			}},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath: "part.txt",
			SHA256:       strings.Repeat("a", 64),
			Fingerprint:  fingerprint,
			Summary:      "local-only synthetic report",
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{NarrativeVoice: []string{"Use a close narrative distance."}},
		},
	}
	if err := st.Simulation.Save(profile); err != nil {
		t.Fatalf("Save: %v", err)
	}
	portableData, err := os.ReadFile(filepath.Join(root, simulationProfilePath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(portableData, []byte(domain.SimulationPortableProfileVersion)) {
		t.Fatalf("profile is not v2: %s", portableData)
	}
	for _, forbidden := range []string{"source_dir", "source_reports", "local-only synthetic report", `C:\\private`} {
		if bytes.Contains(portableData, []byte(forbidden)) {
			t.Fatalf("portable profile contains %q", forbidden)
		}
	}
	evidenceData, err := os.ReadFile(filepath.Join(root, simulationEvidencePath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(evidenceData, []byte("local-only synthetic report")) {
		t.Fatal("local evidence did not retain report")
	}
	loaded, err := st.Simulation.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Corpus.SourceDir != profile.Corpus.SourceDir ||
		len(loaded.SourceReports) != 1 || loaded.Synthesis.Style.NarrativeVoice[0] != profile.Synthesis.Style.NarrativeVoice[0] {
		t.Fatalf("compatibility load changed runtime profile: %+v", loaded)
	}
}

func TestSimulationLegacyProfileFeedsPortableContractAndSafetyEvidence(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	fingerprint := domain.SimulationSourceFingerprint("part.txt", strings.Repeat("a", 64))
	legacy := domain.SimulationProfile{
		Version:   domain.SimulationProfileVersion,
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-02T00:00:00Z",
		Corpus: domain.SimulationCorpusManifest{
			Sources: []domain.SimulationSource{{
				RelativePath: "part.txt",
				SHA256:       strings.Repeat("a", 64),
				Fingerprint:  fingerprint,
			}},
		},
		SourceReports: []domain.SimulationSourceReport{{
			RelativePath: "part.txt",
			SHA256:       strings.Repeat("a", 64),
			Fingerprint:  fingerprint,
			Summary:      "legacy safety evidence",
		}},
		Synthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{NarrativeVoice: []string{"Use close narrative distance."}},
		},
	}
	if err := st.Simulation.io.WriteJSON(simulationProfilePath, legacy); err != nil {
		t.Fatal(err)
	}

	portable, err := st.Simulation.LoadPortable()
	if err != nil || portable == nil || portable.Version != domain.SimulationPortableProfileVersion {
		t.Fatalf("legacy portable projection failed: profile=%+v err=%v", portable, err)
	}
	evidence, err := st.Simulation.LoadLocalEvidence()
	if err != nil || evidence == nil || len(evidence.SourceReports) != 1 ||
		evidence.SourceReports[0].Summary != "legacy safety evidence" {
		t.Fatalf("legacy evidence projection failed: evidence=%+v err=%v", evidence, err)
	}
}

func TestSimulationFailedV1MigrationDoesNotReplaceLegacyProfile(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	legacy := domain.SimulationProfile{Version: domain.SimulationProfileVersion}
	if err := st.Simulation.io.WriteJSON(simulationProfilePath, legacy); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, simulationProfilePath))
	if err != nil {
		t.Fatal(err)
	}
	st.Simulation.io.writeFault = func(rel, stage string) error {
		if rel == simulationProfilePath && stage == "after_temp_sync" {
			return errors.New("synthetic portable write failure")
		}
		return nil
	}
	if err := st.Simulation.Save(legacy); err == nil {
		t.Fatal("expected migration failure")
	}
	after, err := os.ReadFile(filepath.Join(root, simulationProfilePath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed migration replaced the legacy profile")
	}
}

func TestSimulationMergeCheckpointRoundTripAndClear(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}

	checkpoint := domain.SimulationMergeCheckpoint{
		Version:              domain.SimulationMergeCheckpointVersion,
		TotalReportCount:     1,
		ProcessedReportCount: 1,
		ProcessedBatchCount:  1,
		Reports: []domain.SimulationReportIdentity{{
			RelativePath: "a.txt",
			SHA256:       "sha-a",
		}},
		RollingSynthesis: domain.SimulationSynthesis{
			Style: domain.SimulationStyle{
				NarrativeVoice: []string{"close narration"},
			},
		},
	}
	if err := st.Simulation.SaveMergeCheckpoint(checkpoint); err != nil {
		t.Fatalf("SaveMergeCheckpoint: %v", err)
	}

	loaded, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatalf("LoadMergeCheckpoint: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected checkpoint")
	}
	if loaded.Reports[0].Fingerprint != domain.SimulationSourceFingerprint("a.txt", "sha-a") {
		t.Fatalf("fingerprint = %q", loaded.Reports[0].Fingerprint)
	}

	if err := st.Simulation.Save(domain.SimulationProfile{Version: domain.SimulationProfileVersion}); err != nil {
		t.Fatalf("Save profile: %v", err)
	}
	stillLoaded, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatalf("LoadMergeCheckpoint after profile save: %v", err)
	}
	if stillLoaded == nil {
		t.Fatal("profile save should not clear independent checkpoint")
	}

	if err := st.Simulation.ClearMergeCheckpoint(); err != nil {
		t.Fatalf("ClearMergeCheckpoint: %v", err)
	}
	cleared, err := st.Simulation.LoadMergeCheckpoint()
	if err != nil {
		t.Fatalf("LoadMergeCheckpoint after clear: %v", err)
	}
	if cleared != nil {
		t.Fatalf("checkpoint after clear = %+v", cleared)
	}
}
